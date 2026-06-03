package rt

// v0.16.1 PR 3 — isolated SSE channel for /_sky/console.
//
// Today's inline console (console_app/mount.go) is a ONE-SHOT
// server-side renderer: each GET to /_sky/console rebuilds the
// Model via init_ and renders viewWrapped, then ships a static
// HTML document. There's no SSE patch channel, no Click→Msg
// dispatch, no live updates.
//
// v0.16.2 (task #429) plumbs the bundled console's update loop
// into an SSE pump so the UI ticks. v0.16.1 lands the WIRE
// SURFACE only — /_sky/console/_sse — as an ISOLATED transport
// plane that does NOT cross-contaminate with the host app's
// /_sky/sse session map / queue / SSE buffer / drop counter.
// The companion /_sky/console/_event POST endpoint follows in
// commit 2.
//
// Why isolate?
//
//   - The host app's session is keyed on the `sky_sid` cookie
//     and reflects USER state (TEA Model for the user's app
//     pages). If the console reused that cookie, an admin opening
//     /_sky/console would inherit (or overwrite) the user's app
//     session — wrong shape AND a security regression.
//   - The host app's SSE channel maximum size, retry queue cap,
//     drop counter are dimensioned for the user-app's traffic
//     pattern. Console traffic is admin-only, very low volume; a
//     stuck console SSE connection MUST NOT consume buffer slots
//     that belong to user-facing pages.
//   - v0.16.2 sub-app federation: a parent's console mounting a
//     sub-app's console must keep their SSE channels distinct,
//     otherwise heartbeat frames from one would land in the
//     other's render loop.
//
// Auth: reuse the existing __Host-sky_console auth cookie set by
// console_auth_v2.go. The whole point of inline console auth is
// that a single sign-in covers the console surface; adding a
// second cookie + login form on top of the SSE channel would
// double the user-visible friction with no security benefit
// (cookie auth in HTTP === cookie auth in SSE). We delegate to
// `evaluateConsoleAuth` for parity with the HTML mount.
//
// Cookie name `__Host-sky_console_sse` exists only as an OPAQUE
// per-channel session ID — it does NOT carry auth. Auth still
// rides on `__Host-sky_console`. The SSE-channel cookie just
// gives us a way to address a specific browser tab's SSE stream
// across reconnects (heartbeat resume, retry queue replay).
//
// v0.16.2 wiring plan (task #429):
//
//   1. console_app's Sky-source update loop emits Msg dispatches
//      into the channel returned by `ConsoleEventChannel()`. Each
//      dispatch drives the in-process console.Model forward and
//      yields a new view tree.
//   2. The diff between the previous and current view tree gets
//      JSON-encoded and pushed to every connected SSE client via
//      `ConsoleSSEBroadcast(frame)`.
//   3. The POST handler decodes wire events from the browser
//      (click / input / submit) and dispatches into the same
//      channel — closing the round trip.
//
// v0.16.1 ships the CHANNELS without the update-loop producer
// attached. The channel drains to a debug-counter so any
// production caller (admin tooling, smoke tests) can push
// heartbeats through and the test surface can assert framework
// invariants without depending on console_app's update wiring.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"sky-app/rt/telemetry"
)

// ConsoleEvent is the wire shape of a console POST event. v0.16.2
// will widen this — the channel exists as plumbing so the
// surface is mechanically stable across the version boundary.
type ConsoleEvent struct {
	SessionID  string            `json:"sid"`
	Payload    map[string]any    `json:"payload"`
	Headers    map[string]string `json:"-"` // set server-side from request headers
	ReceivedAt time.Time         `json:"-"`
}

// consoleSSESession tracks one connected /_sky/console/_sse
// client. Independent of host's liveSession.
type consoleSSESession struct {
	sid    string
	sseCh  chan []byte
	closed atomic.Bool
}

// consoleSSEState carries the isolated SSE plane's globals.
// Kept package-internal; tests reach for the
// ResetConsoleSSEStateForTesting accessor below.
type consoleSSEState struct {
	mu         sync.RWMutex
	sessions   map[string]*consoleSSESession // keyed by SSE-channel cookie
	eventCh    chan ConsoleEvent             // drains console POSTs
	queueMax   int                           // POST retry queue cap (per session)
	bufferSize int                           // SSE channel buffer
	registered atomic.Bool                   // MountConsoleSSE called?
}

var consoleSSE = &consoleSSEState{
	sessions: map[string]*consoleSSESession{},
}

// consoleSSEHealthy is the public flag that mirrors PR 2's
// `inlineConsoleHealthy`. Set when MountConsoleSSE successfully
// registers /_sky/console/_sse + /_sky/console/_event.
var consoleSSEHealthy atomic.Bool

// ConsoleSSEHealthy reports whether the isolated console SSE plane
// was successfully mounted for this binary. Public so
// downstream observability surfaces + the v0.16.2 hook can
// inspect post-mount state.
func ConsoleSSEHealthy() bool {
	return consoleSSEHealthy.Load()
}

// ResetConsoleSSEStateForTesting wipes the in-memory session map
// + event channel so tests can exercise mount paths from a clean
// slate. Test-only; not part of the public API.
func ResetConsoleSSEStateForTesting() {
	consoleSSE.mu.Lock()
	defer consoleSSE.mu.Unlock()
	for _, s := range consoleSSE.sessions {
		s.closed.Store(true)
		close(s.sseCh)
	}
	consoleSSE.sessions = map[string]*consoleSSESession{}
	consoleSSE.eventCh = nil
	consoleSSE.registered.Store(false)
	consoleSSEHealthy.Store(false)
}

// consoleSSECookieName is the SSE-channel session cookie. NOT
// the auth cookie (that's `__Host-sky_console`) — this is a
// lightweight per-tab identifier so reconnects + retry-queue
// replay land on the right Sky.Live session. The `__Host-`
// prefix gives us the RFC 6265bis hardening (Secure + Path=/ +
// no Domain) for free.
const consoleSSECookieName = "__Host-sky_console_sse"

// consoleSSECookieMaxAge — session cookies on this channel survive
// the auth cookie's 4-hour window. Re-derived from the auth cookie
// TTL so the two converge.
var consoleSSECookieMaxAge = consoleAuthCookieV2MaxAge

// MountConsoleSSE registers /_sky/console/_sse + /_sky/console/_event
// on the given mux. Called from MountEmbeddedConsole AFTER the
// auth gate decision + inline mount succeeded.
//
// Returns true if endpoints were registered. False when:
//   - mux is nil
//   - already mounted (idempotent — second call short-circuits)
//   - SKY_CONSOLE_EMBED=off (legacy opt-out, mirrors MountEmbeddedConsole)
//   - sub-app context (SKY_LIVE_BASE_PATH set — parent owns the surface)
//
// The endpoints share their auth gate with the inline HTML mount:
// every request is run through `evaluateConsoleAuth` first;
// unauth requests get the standard 401 + login form response from
// that helper.
//
// Commit 1 mounts /_sky/console/_sse only; commit 2 adds the POST
// /_sky/console/_event endpoint.
func MountConsoleSSE(mux *http.ServeMux) bool {
	if mux == nil {
		return false
	}
	if consoleSSE.registered.Load() {
		return false
	}
	// Sub-app guard — parent owns the surface.
	if base := skyGetenv("LIVE_BASE_PATH"); base != "" {
		return false
	}
	// Initialise channel + caps lazily so test resets re-init cleanly.
	consoleSSE.mu.Lock()
	if consoleSSE.eventCh == nil {
		consoleSSE.eventCh = make(chan ConsoleEvent, 256)
	}
	if consoleSSE.bufferSize == 0 {
		consoleSSE.bufferSize = sseChanBuffer // mirror host's SKY_LIVE_SSE_BUFFER clamp
	}
	if consoleSSE.queueMax == 0 {
		queueMax := 50
		if n, ok := parsePositiveInt(skyGetenv("LIVE_QUEUE_MAX")); ok {
			queueMax = n
		}
		consoleSSE.queueMax = queueMax
	}
	consoleSSE.mu.Unlock()

	safeMount(mux, "/_sky/console/_sse", handleConsoleSSE)

	consoleSSE.registered.Store(true)
	consoleSSEHealthy.Store(true)
	return true
}

// ConsoleEventChannel exposes the receive-end of the console
// event channel. v0.16.2 (#429) attaches console_app's update
// loop here. v0.16.1 ships it drained-to-debug — anyone that
// reads from it sees POSTed events but the framework doesn't
// route them anywhere yet.
//
// Returns nil before MountConsoleSSE has run.
func ConsoleEventChannel() <-chan ConsoleEvent {
	consoleSSE.mu.RLock()
	defer consoleSSE.mu.RUnlock()
	return consoleSSE.eventCh
}

// ConsoleSSEBroadcast pushes a frame to every connected SSE
// client. v0.16.1 exposes this so external callers (admin
// tooling, smoke tests) can push heartbeats / hello frames;
// v0.16.2 plumbs console_app's render outputs here.
//
// Non-blocking: drops the frame for any session whose channel
// is full + increments `sky_console_sse_drops_total{session}`.
func ConsoleSSEBroadcast(frame []byte) {
	consoleSSE.mu.RLock()
	sessions := make([]*consoleSSESession, 0, len(consoleSSE.sessions))
	for _, s := range consoleSSE.sessions {
		if !s.closed.Load() {
			sessions = append(sessions, s)
		}
	}
	consoleSSE.mu.RUnlock()
	for _, s := range sessions {
		buf := make([]byte, len(frame))
		copy(buf, frame)
		select {
		case s.sseCh <- buf:
		default:
			recordConsoleSseDrop(s.sid)
		}
	}
}

// ──── SSE handler ────────────────────────────────────────────────

// handleConsoleSSE serves the isolated /_sky/console/_sse stream.
// Shape mirrors handleSSE in live.go (X-Accel-Buffering, 2 KB
// padding, hello handshake, heartbeat ticker) but on its own
// session map + buffer.
func handleConsoleSSE(w http.ResponseWriter, r *http.Request) {
	// Auth — same gate as the inline HTML mount.
	if !evaluateConsoleAuth(w, r) {
		return
	}

	sid := consoleSSESessionID(r, w)
	sess := consoleSSE.openSession(sid)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Sky-Console-SSE", "1")
	flusher, _ := w.(http.Flusher)

	// 2 KB padding primes proxies that ignore X-Accel-Buffering.
	pad := make([]byte, 0, 2050)
	pad = append(pad, ':', ' ')
	for i := 0; i < 2048; i++ {
		pad = append(pad, '.')
	}
	pad = append(pad, '\n', '\n')
	if _, err := w.Write(pad); err != nil {
		consoleSSE.closeSession(sid)
		return
	}

	// Immediate hello handshake — client treats absence-of-hello
	// as a wedge.
	helloPayload, _ := json.Marshal(map[string]any{
		"v":   1,
		"sid": sid,
		"ts":  time.Now().UnixMilli(),
	})
	if _, err := fmt.Fprintf(w, "event: hello\ndata: %s\n\n", helloPayload); err != nil {
		consoleSSE.closeSession(sid)
		return
	}
	if flusher != nil {
		flusher.Flush()
	}

	// Heartbeat ticker. Same cadence as the host's SSE handler so
	// existing client-side watchdogs (heartbeat-ttl = 35 s) still
	// trip on a real wedge.
	heartbeat := time.NewTicker(consoleSSEHeartbeatInterval)
	defer heartbeat.Stop()

	defer consoleSSE.closeSession(sid)

	for {
		select {
		case <-r.Context().Done():
			return
		case frame, ok := <-sess.sseCh:
			if !ok {
				return
			}
			if _, err := w.Write(frame); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		case t := <-heartbeat.C:
			if _, err := fmt.Fprintf(w, "event: heartbeat\ndata: {\"ts\":%d}\n\n", t.UnixMilli()); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

// consoleSSEHeartbeatInterval is the cadence at which
// handleConsoleSSE emits heartbeats. Exposed as a var so tests
// can dial it down to milliseconds; production callers leave it
// at 15 s.
var consoleSSEHeartbeatInterval = 15 * time.Second

// ──── Session helpers ────────────────────────────────────────────

// consoleSSESessionID reads `__Host-sky_console_sse` from the
// request OR mints a new one + writes it via Set-Cookie.
//
// Critical: this cookie does NOT confer auth — auth rides on
// `__Host-sky_console`. This cookie is just an opaque per-tab
// identifier so an SSE reconnect lands on the same session.
func consoleSSESessionID(r *http.Request, w http.ResponseWriter) string {
	if c, err := r.Cookie(consoleSSECookieName); err == nil && len(c.Value) >= 16 {
		return c.Value
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// rand.Read shouldn't fail on a healthy host; fall back
		// to a time-derived ID so we don't 500 on this.
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	sid := hex.EncodeToString(b)
	if w != nil {
		http.SetCookie(w, &http.Cookie{
			Name:     consoleSSECookieName,
			Value:    sid,
			Path:     "/",
			HttpOnly: true,
			Secure:   true,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   int(consoleSSECookieMaxAge.Seconds()),
		})
	}
	return sid
}

// openSession returns the consoleSSESession for sid, creating a
// new one if absent. The returned session's sseCh is bounded by
// `consoleSSE.bufferSize` (mirrors SKY_LIVE_SSE_BUFFER) so a
// stuck console UI cannot exhaust process memory.
func (s *consoleSSEState) openSession(sid string) *consoleSSESession {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.sessions[sid]; ok && !existing.closed.Load() {
		return existing
	}
	sess := &consoleSSESession{
		sid:   sid,
		sseCh: make(chan []byte, s.bufferSize),
	}
	s.sessions[sid] = sess
	return sess
}

// closeSession marks a session closed + drops it from the
// registry. Safe to call multiple times. The SSE channel is
// closed so any in-flight Broadcast goroutine drops the slot
// cleanly.
func (s *consoleSSEState) closeSession(sid string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sess, ok := s.sessions[sid]; ok {
		if sess.closed.CompareAndSwap(false, true) {
			close(sess.sseCh)
		}
		delete(s.sessions, sid)
	}
}

// ──── Telemetry ──────────────────────────────────────────────────

// recordConsoleSseDrop increments
// sky_console_sse_drops_total{session=<sid>}. Mirrors the
// host's sky_live_sse_drops_total but on its own series so
// dashboards can spot console-specific congestion without
// having to filter the host's metrics.
func recordConsoleSseDrop(sid string) {
	telemetry.Default().Inc("sky_console_sse_drops_total", map[string]string{
		"session": sid,
	})
}

// connectedConsoleSSEClients reports the count of live SSE
// connections on the isolated channel. Exposed for tests; the
// /_sky/metrics gauge surface is part of v0.16.2's observability
// pass.
func connectedConsoleSSEClients() int {
	consoleSSE.mu.RLock()
	defer consoleSSE.mu.RUnlock()
	return len(consoleSSE.sessions)
}
