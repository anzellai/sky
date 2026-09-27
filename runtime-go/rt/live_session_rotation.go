//go:build !js

// live_session_rotation.go — Sky.Live session-id rotation, the session
// cookie name, and the stable per-session key.
//
// THE DEFECT this closes (session fixation). The page GET adopted any
// presented session cookie, and Live.bindSessionUser stamped the signed-in
// user onto that SAME id. Whoever planted the id in the victim's browser (an
// id they got by visiting the site, through a sibling subdomain, plain HTTP
// or XSS) then shared the victim's signed-in session.
//
// The rules now:
//
//  1. Every CHANGE of bound user (the first bind and an account switch)
//     moves the session to a new id. The SAME *liveSession object is
//     re-keyed in the store (rekeySession), so the acting tab's open SSE
//     connection stays attached and the per-session state is untouched.
//
//  2. The old id never again grants access. The store keeps an alias record
//     old → new. Inside a short grace window (liveRotateGrace) a request
//     that presents ONLY the old cookie is told "session-rotating" (retry,
//     no reload); after the window it is "session-lost", and a page GET
//     mints a fresh id instead of re-adopting the old one.
//
//  3. The new id reaches only the tab that performed the sign-in. Binding
//     runs on the dispatch goroutine, usually inside a Cmd.perform, with no
//     ResponseWriter, and an open SSE stream cannot set a cookie. So the
//     rotation records the ORIGIN TAB (carried from the event POST, or the
//     page GET, through the goroutine trace context into every Cmd.perform
//     it spawns) and:
//       - pushes a `rotate` frame with a one-time ticket to that tab's SSE
//         connection only; the client POSTs the ticket to /_sky/rotate,
//         which checks old cookie + tab + ticket and sets the new cookie;
//       - gives the new cookie directly to that tab's next event POST, SSE
//         connect or sky-nav GET that still carries the old cookie;
//       - closes every OTHER SSE connection of the session. A connection
//         the fixation attacker opened before the sign-in must not keep
//         receiving the signed-in view. The victim's other tabs reconnect;
//         they present the new cookie once the acting tab has stored it in
//         the shared cookie jar, and their stale body sid is accepted as an
//         alias of that cookie (the cookie stays the authority).
//     Pushing the new id to every stream, or letting the old cookie act for
//     a grace window, would hand the signed-in session to exactly the
//     attacker rotation exists to lock out.
//
//  4. The durable snapshot moves to the new id and the old key is deleted
//     (durableCtx.rotate); a queued write can not recreate it.
//
//  5. The alias record lives in the session store, so every replica of a
//     multi-instance deploy (sqlite / postgres / redis) resolves it.
//
// Cookie name: `__Host-<name>` when the cookie is Secure (HTTPS, a TLS
// proxy, or the production gate), `<name>` over plain HTTP. `__Host-`
// cannot be set by a sibling subdomain or over plain HTTP, which closes the
// easiest planting routes. Both names are read (the `__Host-` one wins), so a
// browser holding the old name keeps its session and is moved to the new
// name on its next response.

package rt

import (
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// liveRotateGrace is how long an old session id answers "session-rotating"
// (retry) rather than "session-lost" (reload) after a rotation. A variable so
// tests can shorten it.
var liveRotateGrace = 60 * time.Second

// hostCookiePrefix is the RFC 6265bis `__Host-` name prefix: the cookie must
// be Secure, Path=/ and carry no Domain, so no other host can set it.
const hostCookiePrefix = "__Host-"

// ─── session id format ──────────────────────────────────────────────

// newLiveSessionID mints a session id: 16 random bytes, lowercase hex.
func newLiveSessionID() string {
	b := make([]byte, 16)
	if _, err := crand.Read(b); err != nil {
		// crypto/rand does not fail on supported platforms; a zero id would
		// be shared by every caller, so refuse loudly rather than continue.
		panic("sky.live: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// validSessionID reports whether s has the exact shape newLiveSessionID
// produces (32 lowercase hex characters). A presented cookie of any other
// shape was not minted by this runtime and is never adopted.
func validSessionID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ─── cookie name ────────────────────────────────────────────────────

// sessionCookieBase returns the unprefixed cookie name for an app's
// configured name ("" → "sky_sid").
func sessionCookieBase(name string) string {
	if name == "" {
		name = "sky_sid"
	}
	if len(name) > len(hostCookiePrefix) && name[:len(hostCookiePrefix)] == hostCookiePrefix {
		name = name[len(hostCookiePrefix):]
	}
	return name
}

// sessionCookieSameSite is the SameSite mode of the session cookie: Lax,
// or None in cross-origin iframe mode (see writeSessionCookie).
func sessionCookieSameSite() http.SameSite {
	if crossOriginIframeMode() {
		return http.SameSiteNoneMode
	}
	return http.SameSiteLaxMode
}

// sessionCookieNameFor is the ONE resolver for the name a session cookie is
// WRITTEN under: `__Host-<base>` when the cookie will be Secure, else
// `<base>`. Plain-HTTP LAN testing therefore keeps working (a `__Host-`
// cookie there would be rejected by the browser and every load would mint a
// new session).
func sessionCookieNameFor(r *http.Request, base string) string {
	base = sessionCookieBase(base)
	if cookieSecureFor(r, base, sessionCookieSameSite()) {
		return hostCookiePrefix + base
	}
	return base
}

// readSessionCookie is the ONE resolver for the session id a request
// PRESENTS. It reads `__Host-<base>` first, then `<base>`, so a browser that
// still holds the pre-v0.27 name keeps its session, and a legacy cookie
// planted next to a real `__Host-` one never wins. Empty values are ignored.
func readSessionCookie(r *http.Request, base string) (value string, legacy bool) {
	if r == nil {
		return "", false
	}
	base = sessionCookieBase(base)
	if c, err := r.Cookie(hostCookiePrefix + base); err == nil && c.Value != "" {
		return c.Value, false
	}
	if c, err := r.Cookie(base); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}

// isSessionCookieName reports whether a cookie name is either spelling of
// the session cookie `base`.
func isSessionCookieName(name, base string) bool {
	base = sessionCookieBase(base)
	return name == base || name == hostCookiePrefix+base
}

// ─── origin tab (goroutine trace context) ───────────────────────────

type liveOriginTabKeyT struct{}

var liveOriginTabKey = liveOriginTabKeyT{}

// stampLiveOriginTab records, on the calling goroutine's trace context, the
// browser tab whose request is being handled. runCmd captures the trace
// context for every Cmd.perform it spawns, so the tab follows the whole
// perform chain (event → perform → result dispatch → perform → bind). The
// returned func restores the previous context.
func stampLiveOriginTab(tab string) func() {
	if tab == "" {
		return func() {}
	}
	prev := CurrentTraceContext()
	SetGoroutineTraceContext(context.WithValue(prev, liveOriginTabKey, tab))
	return func() { SetGoroutineTraceContext(prev) }
}

// withLiveOriginTab runs fn with tab stamped as the origin tab.
func withLiveOriginTab(tab string, fn func()) {
	restore := stampLiveOriginTab(tab)
	defer restore()
	fn()
}

// currentLiveOriginTab returns the origin tab stamped on this goroutine, or
// "" when the work was not started by a browser request (a Time.every tick,
// a pub/sub delivery).
func currentLiveOriginTab() string {
	if v, ok := CurrentTraceContext().Value(liveOriginTabKey).(string); ok {
		return v
	}
	return ""
}

// ─── alias records ──────────────────────────────────────────────────

// sessionAlias is what the store keeps under a retired session id.
type sessionAlias struct {
	// New is the id the session moved to. Empty: the session ended
	// (Live.endSession, revocation) and the id is dead.
	New string `json:"n"`
	// Tab is the browser tab that performed the change of user; only it may
	// exchange the old cookie for the new one.
	Tab string `json:"t"`
	// TicketHash is the hex SHA-256 of the one-time ticket pushed to Tab's
	// SSE connection.
	TicketHash string `json:"h"`
	// GraceUntil (unix ns): until then the old cookie gets
	// "session-rotating"; afterwards it is dead.
	GraceUntil int64 `json:"g"`
}

func (a sessionAlias) inGrace(now time.Time) bool {
	return a.New != "" && now.UnixNano() < a.GraceUntil
}

func encodeAlias(a sessionAlias) string {
	b, _ := json.Marshal(a)
	return string(b)
}

func decodeAlias(s string) (sessionAlias, bool) {
	var a sessionAlias
	if err := json.Unmarshal([]byte(s), &a); err != nil {
		return sessionAlias{}, false
	}
	return a, true
}

func ticketHash(ticket string) string {
	h := sha256.Sum256([]byte(ticket))
	return hex.EncodeToString(h[:])
}

// ─── liveSession accessors ──────────────────────────────────────────

// currentSID returns the session's id, following a rotation. Safe from any
// goroutine (the rotated id is an atomic pointer).
func (s *liveSession) currentSID() string {
	if p := s.rotSid.Load(); p != nil {
		return *p
	}
	return s.sid
}

// setSID records a new id for an already-shared session.
func (s *liveSession) setSID(v string) {
	s.rotSid.Store(&v)
}

// stableKey returns the per-session key that survives rotation. Sessions
// persisted before the key existed adopt their current id once, so an app
// that keyed data by the session cookie keeps finding it.
func (s *liveSession) stableKey() string {
	if p := s.key.Load(); p != nil {
		return *p
	}
	k := s.currentSID()
	if k == "" {
		k = newLiveSessionID()
	}
	s.key.CompareAndSwap(nil, &k)
	return *s.key.Load()
}

func (s *liveSession) setStableKey(k string) {
	if k == "" {
		return
	}
	s.key.Store(&k)
}

// sseConnKicked returns a channel that is closed when the connection was
// closed by a rotation (nil-safe: an unknown id returns a closed channel).
func (s *liveSession) sseConnKicked(id uint64) <-chan struct{} {
	s.sseConnMu.Lock()
	c, ok := s.sseConns[id]
	s.sseConnMu.Unlock()
	if !ok || c.kick == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return c.kick
}

// rotateSSEConns delivers fr to the connections of keepTab and closes every
// other connection of the session. Closed connections are removed from the
// fan-out set at once, so no later frame reaches them.
func (s *liveSession) rotateSSEConns(keepTab string, fr sseFrame) {
	s.sseConnMu.Lock()
	var keep, drop []*sseConn
	for id, c := range s.sseConns {
		if keepTab != "" && c.tab == keepTab {
			keep = append(keep, c)
			continue
		}
		drop = append(drop, c)
		delete(s.sseConns, id)
	}
	s.sseConnMu.Unlock()
	for _, c := range drop {
		c.kickOnce.Do(func() {
			if c.kick != nil {
				close(c.kick)
			}
		})
	}
	for _, c := range keep {
		select {
		case c.ch <- fr:
		default:
			// A full buffer drops the ticket; the tab still gets the new
			// cookie on its next event POST or SSE reconnect.
			recordSseDrop(s.currentSID())
		}
	}
}

// ─── rotation ───────────────────────────────────────────────────────

// rotateSessionLocked moves sess to a fresh id. PRECONDITION: the caller
// holds sess.mu. Returns the new id.
func (app *liveApp) rotateSessionLocked(sess *liveSession, originTab string) string {
	old := sess.currentSID()
	if app == nil || app.store == nil || old == "" {
		return old
	}
	newSid := newLiveSessionID()
	ticket := newLiveSessionID()
	now := time.Now()

	// Re-key the SAME object. storeMu serialises this against every other
	// store write of the session (persistSession, the idle-evict persist),
	// so none of them can write the session back under the old id.
	sess.storeMu.Lock()
	app.store.rekeySession(old, newSid, sess)
	sess.setSID(newSid)
	sess.storeMu.Unlock()
	app.store.putAlias(old, sessionAlias{
		New:        newSid,
		Tab:        originTab,
		TicketHash: ticketHash(ticket),
		GraceUntil: now.Add(liveRotateGrace).UnixNano(),
	})

	// Pub/sub subscriptions carry the session id as their no-echo owner;
	// re-subscribe under the new id.
	sess.activeSubsMu.Lock()
	regs := make([]*subRegistration, 0, len(sess.activeSubs))
	for _, r := range sess.activeSubs {
		if r != nil {
			regs = append(regs, r)
		}
	}
	sess.activeSubs = nil
	sess.activeSubsMu.Unlock()
	for _, reg := range regs {
		if reg.cancel != nil {
			reg.cancel()
		}
	}
	if len(regs) > 0 && app.subscriptions != nil && sess.model != nil {
		app.setupSubscriptions(sess)
	}

	payload, _ := json.Marshal(map[string]string{"ticket": ticket})
	sess.rotateSSEConns(originTab, sseFrame{event: "rotate", data: string(payload)})

	app.durable.rotate(old, newSid, sess.model)
	dropSessionBinding(app, old)
	return newSid
}

// endSessionNow retires sess for good: its store entry, durable snapshot and
// binding row are deleted and its id is tombstoned, so a later request with
// that cookie gets a fresh session. Used by Live.endSession and the
// revocation eviction.
func (app *liveApp) endSessionNow(sess *liveSession) {
	if app == nil || sess == nil {
		return
	}
	sid := sess.currentSID()
	app.durable.retire(sid)
	dropSessionBinding(app, sid)
	if app.store != nil && sid != "" {
		app.store.putAlias(sid, sessionAlias{})
	}
}

// ─── resolving a presented id ───────────────────────────────────────

// followAlias walks the alias chain from sid (a session can rotate twice in
// quick succession) to the live id it ends at. ok is false when the chain
// ends at a dead id. hops holds every alias passed.
func (app *liveApp) followAlias(sid string) (final string, hops []sessionAlias, ok bool) {
	cur := sid
	for i := 0; i < 4; i++ {
		a, has := app.store.getAlias(cur)
		if !has {
			if i == 0 {
				return "", nil, false
			}
			_, live := app.store.Get(cur)
			return cur, hops, live
		}
		hops = append(hops, a)
		if a.New == "" {
			return "", hops, false
		}
		if _, live := app.store.Get(a.New); live {
			return a.New, hops, true
		}
		cur = a.New
	}
	return "", hops, false
}

// aliasChainAllows reports whether every hop is still in its grace window
// and was started by tab.
func aliasChainAllows(hops []sessionAlias, tab string, now time.Time) bool {
	if tab == "" || len(hops) == 0 {
		return false
	}
	for _, h := range hops {
		if !h.inGrace(now) || h.Tab == "" ||
			subtle.ConstantTimeCompare([]byte(h.Tab), []byte(tab)) != 1 {
			return false
		}
	}
	return true
}

// sessionVerdict classifies a request's session cookie for the event, SSE
// and rotate endpoints.
type sessionVerdict int

const (
	sessionBound    sessionVerdict = iota // act on sid
	sessionLost                           // unknown / dead / not bound
	sessionRotating                       // old cookie inside the grace window: retry
)

// boundSession is the result of resolveBoundSession.
type boundSession struct {
	sid     string
	verdict sessionVerdict
	// setCookie: the request presented the OLD cookie from the tab that
	// rotated; the response must set the new cookie.
	setCookie bool
	// tellSid: the response must carry X-Sky-Sid so the tab updates the sid
	// it echoes.
	tellSid bool
}

// resolveBoundSession is the security boundary for every endpoint that acts
// on a session. The cookie is the authority (see boundSessionID); `claimed`
// (a body sid) may only agree with it, or be an alias of it; `tab` is the
// requesting tab, which may exchange an old cookie only when it is the tab
// that rotated.
func (app *liveApp) resolveBoundSession(r *http.Request, claimed, tab string) boundSession {
	if app == nil || r == nil || app.store == nil {
		return boundSession{verdict: sessionLost}
	}
	val, _ := readSessionCookie(r, app.cookieNameOrDefault())
	if val == "" {
		return boundSession{verdict: sessionLost}
	}
	eq := func(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
	if _, live := app.store.Get(val); live {
		if claimed == "" || eq(claimed, val) {
			return boundSession{sid: val, verdict: sessionBound}
		}
		// Another tab of the same browser: the jar already holds the new
		// cookie, the tab's JS still echoes an older sid.
		if final, _, ok := app.followAlias(claimed); ok && eq(final, val) {
			return boundSession{sid: val, verdict: sessionBound, tellSid: true}
		}
		return boundSession{verdict: sessionLost}
	}
	final, hops, ok := app.followAlias(val)
	if len(hops) == 0 {
		// Not a rotated id: an unknown session. The caller's store.Get
		// answers session-lost; keep the id so a same-request race with a
		// fresh mint behaves as before.
		return boundSession{sid: val, verdict: sessionBound}
	}
	now := time.Now()
	if !ok || !hops[0].inGrace(now) {
		return boundSession{verdict: sessionLost}
	}
	if aliasChainAllows(hops, tab, now) &&
		(claimed == "" || eq(claimed, val) || eq(claimed, final)) {
		return boundSession{sid: final, verdict: sessionBound, setCookie: true, tellSid: true}
	}
	return boundSession{verdict: sessionRotating}
}

// writeSessionRotating answers a request that presented an old cookie inside
// the grace window, from a tab that did not rotate. The client retries; by
// then the acting tab has stored the new cookie in the shared jar. The body
// never names the new id.
func writeSessionRotating(w http.ResponseWriter) {
	w.Header().Set("X-Sky-Live", "1")
	w.Header().Set("X-Sky-Status", "session-rotating")
	w.Header().Set("Retry-After", "1")
	http.Error(w, "session rotating", http.StatusConflict)
}

// writeRotatingPage answers a page GET that presented an old cookie inside
// the grace window: a small page that reloads itself, with no Set-Cookie
// (a fresh cookie here would overwrite the new one in the victim's jar).
func writeRotatingPage(w http.ResponseWriter) {
	setSecurityHeaders(w.Header())
	w.Header().Set("X-Sky-Live", "1")
	w.Header().Set("X-Sky-Status", "session-rotating")
	w.Header().Set("Retry-After", "1")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Refresh", "1")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = io.WriteString(w, "<!DOCTYPE html><html><head><meta charset=\"utf-8\"><title>Signing in</title></head>"+
		"<body><p>Signing in. This page reloads in a moment.</p></body></html>")
}

// pageSessionID resolves the session id for a page GET (handleInitial), and
// writes the session cookie. ok is false when it already answered the
// request (the rotating retry page).
//
//   - no cookie, or a cookie of the wrong shape → a fresh id;
//   - a live id → that id (the cookie slides);
//   - a rotated id: inside the grace window the rotating tab (X-Sky-Tab on a
//     sky-nav fetch) gets the new id, any other request the retry page;
//     after it, a fresh id;
//   - an ended id → a fresh id;
//   - any other well-formed id → adopted (a restart of a memory store, or
//     another replica: the durable snapshot is restored under it).
func (app *liveApp) pageSessionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	base := app.cookieNameOrDefault()
	val, _ := readSessionCookie(r, base)
	if val != "" && validSessionID(val) && app.store != nil {
		if _, live := app.store.Get(val); live {
			writeSessionCookie(r, w, base, val, app.sessionTTL)
			return val, true
		}
		final, hops, ok := app.followAlias(val)
		if len(hops) == 0 {
			writeSessionCookie(r, w, base, val, app.sessionTTL)
			return val, true
		}
		now := time.Now()
		if ok && hops[0].inGrace(now) {
			if aliasChainAllows(hops, r.Header.Get("X-Sky-Tab"), now) {
				writeSessionCookie(r, w, base, final, app.sessionTTL)
				w.Header().Set("X-Sky-Sid", final)
				return final, true
			}
			writeRotatingPage(w)
			return "", false
		}
	}
	sid := newLiveSessionID()
	writeSessionCookie(r, w, base, sid, app.sessionTTL)
	return sid, true
}

// ─── /_sky/rotate ───────────────────────────────────────────────────

// handleRotate exchanges the old session cookie for the new one. The tab
// that rotated received a one-time ticket on its SSE connection; it POSTs
// {tab, ticket} here (the CSRF middleware checks the request like any other
// mutating POST). The old cookie, the tab and the ticket must all match the
// alias record. A request that already carries a live cookie gets its own id
// back, so a retried exchange is harmless.
func (app *liveApp) handleRotate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Tab    string `json:"tab"`
		Ticket string `json:"ticket"`
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || json.Unmarshal(body, &req) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	base := app.cookieNameOrDefault()
	val, _ := readSessionCookie(r, base)
	if val == "" || app.store == nil {
		writeSessionLost(w)
		return
	}
	reply := func(sid string, setCookie bool) {
		if setCookie {
			writeSessionCookie(r, w, base, sid, app.sessionTTL)
		}
		w.Header().Set("X-Sky-Live", "1")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		out, _ := json.Marshal(map[string]string{"sid": sid})
		_, _ = w.Write(out)
	}
	if _, live := app.store.Get(val); live {
		reply(val, false)
		return
	}
	final, hops, ok := app.followAlias(val)
	now := time.Now()
	if !ok || len(hops) == 0 || !hops[0].inGrace(now) {
		writeSessionLost(w)
		return
	}
	if req.Ticket == "" || !aliasChainAllows(hops, req.Tab, now) {
		http.Error(w, "rotation refused", http.StatusForbidden)
		return
	}
	th := ticketHash(req.Ticket)
	matched := false
	for _, h := range hops {
		if subtle.ConstantTimeCompare([]byte(h.TicketHash), []byte(th)) == 1 {
			matched = true
		}
	}
	if !matched {
		http.Error(w, "rotation refused", http.StatusForbidden)
		return
	}
	reply(final, true)
}

// ─── kernels ────────────────────────────────────────────────────────

// Live.sessionKey : () -> Task Error String
//
// A key for the current live session that stays the same for the session's
// whole life, including across the session-id rotation at sign-in. Use it to
// key per-session data instead of the session cookie (which changes at every
// sign-in). Outside a live dispatch it fails: there is no session.
func Live_sessionKey(_ any) any {
	return func() any {
		sess := currentLiveSession()
		if sess == nil {
			return Err[any, any](ErrInvalidInput("Live.sessionKey: no live session in scope"))
		}
		return Ok[any, any](sess.stableKey())
	}
}

// Live.endSession : () -> Task Error ()
//
// End the current live session (sign-out, "start over"): its server state,
// durable snapshot and user binding are deleted, its open tabs get
// session-lost and reload, and the old session cookie is never adopted
// again — the reload starts a fresh session. Outside a live dispatch it is a
// no-op.
func Live_endSession(_ any) any {
	return func() any {
		sess := currentLiveSession()
		if sess == nil {
			return Ok[any, any](struct{}{})
		}
		app := sess.app.Load()
		if !sess.evicted.CompareAndSwap(false, true) {
			return Ok[any, any](struct{}{})
		}
		app.endSessionNow(sess)
		// A Task runs on a Cmd.perform goroutine, which never holds sess.mu,
		// so the teardown can run here, before the Task returns: the next
		// request with the old cookie already finds nothing.
		sid := sess.currentSID()
		sess.markDone()
		if app != nil && app.store != nil && sid != "" {
			app.store.Delete(sid)
		}
		return Ok[any, any](struct{}{})
	}
}

// writeSSERotating answers an SSE connect that presented an old cookie
// inside the grace window from a tab that did not rotate: a short stream
// with a `rotating` event and a 1 s retry hint, so the client reconnects
// instead of treating the session as lost.
func writeSSERotating(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("X-Sky-Live", "1")
	w.Header().Set("X-Sky-Status", "session-rotating")
	_, _ = io.WriteString(w, "retry: 1000\nevent: rotating\ndata: {}\n\n")
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
