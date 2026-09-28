//go:build !js

package rt

// live_session_header.go — Sky.Live sessions without cookies (v0.27).
//
// Some hosts cannot keep cookies: a native shell whose custom-scheme handler
// drops Set-Cookie (a WKWebView custom scheme has no cookie store), some
// embedded web views. For them an app opts into the HEADER session transport
// (`Live.withSessionTransport "header"`, `App.withSessionTransport
// HeaderToken`, or `SKY_LIVE_SESSION_TRANSPORT=header`). Nothing changes for
// an app that does not opt in.
//
// THE TOKEN. A page load mints a session token T (16 random bytes, 32 hex
// characters). The page carries T in its boot config (the non-executable
// <script type="application/json" id="sky-live-cfg"> block the client already
// reads under a strict CSP) and in the X-Sky-Session response header. The
// client sends T in the X-Sky-Session header on every event POST, sky-nav
// fetch, rotate exchange and SSE connection. Every Set-Cookie of the session
// is gone.
//
// ONLY A HASH IS STORED. The session id the store is keyed by is
// sessionTokenSID(T) = the first 32 hex characters of SHA-256 of T. The token
// itself exists only in the client, and in memory on the goroutines serving
// a request that presented it. A leaked session store (a database dump, a
// Redis snapshot) names no usable token, and the runtime never logs T.
//
// SSE. EventSource cannot set a header, so the client reads the stream with
// fetch() and a ReadableStream (the X-Sky-Session header rides on it like on
// any fetch). Where streaming fetch is unavailable it falls back to a one-time
// SSE ticket: POST /_sky/sse-ticket (with the header) returns a ticket that is
// single-use, bound to the session, and expires in sseTicketTTL; the client
// opens `EventSource("/_sky/sse?...&tk=<ticket>")`. Tickets live in the
// memory of the replica that issued them (a Sky.Live deployment is sticky).
//
// ROTATION (live_session_rotation.go) works the same way, with one change:
// the new id cannot be chosen at random, because the server keeps no token to
// hand back. It is DERIVED: newT = HMAC-SHA256(key = oldT, "sky.live.rotate:"
// + salt), newSid = sessionTokenSID(newT), and the alias record keeps only
// the salt. So the tab that holds the old token (and only it) can have the
// new one computed for it, without the server storing either:
//   - POST /_sky/rotate with the one-time ticket returns the new token in the
//     X-Sky-Session header and the JSON body; the client swaps it in;
//   - that tab's next event POST, SSE connect or sky-nav fetch that still
//     presents the old token inside the grace window gets the new token in
//     the X-Sky-Session response header.
// The old token is stamped on the goroutine trace context of the request
// (like the origin tab), so a rotation started by a Cmd.perform of that
// request can derive from it. A rotation with no token in scope (a Time.every
// tick) gets a random id; that session ends after the grace window.
//
// CSRF. The X-Sky-Session header is the CSRF defence: a cross-site form
// cannot set a header, and a cross-origin fetch that sets one needs a CORS
// preflight the runtime never grants. So header mode needs no double-submit
// cookie; a state-changing request is accepted only when it carries the
// header (or an Authorization header, or matches a CSRF exemption), and it
// still passes the Origin / Sec-Fetch-Site check (sameOriginGuard).
//
// A navigation (a reload, a typed URL) cannot carry a header, so a full page
// load starts a new session. In-app navigation (sky-nav) keeps it.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// skySessionHeader is the request / response header that carries the session
// token in header mode.
const skySessionHeader = "X-Sky-Session"

const (
	sessionTransportCookie = "cookie"
	sessionTransportHeader = "header"
)

// resolveSessionTransport resolves the session transport across all
// precedence layers (operator SKY_LIVE_SESSION_TRANSPORT > the
// Live.withSessionTransport builder > default "cookie"). An unrecognised
// winner keeps the cookie transport and says so: a typo must not change how
// sessions authenticate.
func resolveSessionTransport(builderVal string) string {
	for _, v := range configLayers("LIVE_SESSION_TRANSPORT", builderVal) {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "":
			continue
		case sessionTransportCookie:
			return sessionTransportCookie
		case sessionTransportHeader:
			return sessionTransportHeader
		default:
			fmt.Printf("[sky.live] WARNING: session transport %q is not recognised "+
				"(valid: cookie, header) — using \"cookie\". "+
				"Set SKY_LIVE_SESSION_TRANSPORT or Live.withSessionTransport.\n", v)
			return sessionTransportCookie
		}
	}
	return sessionTransportCookie
}

// Live_withSessionTransport — `Live.withSessionTransport : String ->
// AppConfig model msg -> AppConfig model msg` ("cookie" | "header").
func Live_withSessionTransport(transport, cfg any) any {
	return liveCfgSet(cfg, "SessionTransport", transport)
}

// ─── token ↔ session id ─────────────────────────────────────────────

// sessionTokenSID is the session id a token stands for: the first 32 hex
// characters of SHA-256("sky.live.session:" + token). Same shape as a
// cookie-mode id (validSessionID), so every store and alias path is unchanged.
func sessionTokenSID(token string) string {
	h := sha256.Sum256([]byte("sky.live.session:" + token))
	return hex.EncodeToString(h[:16])
}

// deriveRotatedToken is the token a rotation moves the holder of token to.
func deriveRotatedToken(token, salt string) string {
	m := hmac.New(sha256.New, []byte(token))
	m.Write([]byte("sky.live.rotate:" + salt))
	return hex.EncodeToString(m.Sum(nil)[:16])
}

// tokenThroughHops follows a rotation chain from token: each hop's salt
// derives the next token, which must hash to that hop's new id. ok is false
// when a hop has no salt (a cookie-mode or tokenless rotation) or the chain
// does not verify.
func tokenThroughHops(token string, hops []sessionAlias) (string, bool) {
	if token == "" || len(hops) == 0 {
		return "", false
	}
	cur := token
	for _, h := range hops {
		if h.Salt == "" || h.New == "" {
			return "", false
		}
		cur = deriveRotatedToken(cur, h.Salt)
		if subtle.ConstantTimeCompare([]byte(sessionTokenSID(cur)), []byte(h.New)) != 1 {
			return "", false
		}
	}
	return cur, true
}

// presentedSessionToken is the X-Sky-Session token of a request, "" when
// absent or not of the minted shape.
func presentedSessionToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	t := strings.TrimSpace(r.Header.Get(skySessionHeader))
	if !validSessionID(t) {
		return ""
	}
	return t
}

// presentedSID is the ONE resolver for the session id a request presents to
// an app: the session cookie (cookie mode), or the id of the X-Sky-Session
// token or of a redeemed SSE ticket (header mode). A cookie is never read in
// header mode.
func (app *liveApp) presentedSID(r *http.Request) string {
	if app == nil || r == nil {
		return ""
	}
	if !app.headerSessions {
		v, _ := readSessionCookie(r, app.cookieNameOrDefault())
		return v
	}
	if t := presentedSessionToken(r); t != "" {
		return sessionTokenSID(t)
	}
	if sid, ok := r.Context().Value(sseTicketSIDKey{}).(string); ok {
		return sid
	}
	return ""
}

// issueSession writes the session credential for sid on the response: the
// cookie in cookie mode. Header mode has nothing to write: the client already
// holds the token, and a new token is handed out only where it is minted or
// derived (writeSessionToken).
func (app *liveApp) issueSession(w http.ResponseWriter, r *http.Request, sid string) {
	if app.headerSessions {
		return
	}
	writeSessionCookie(r, w, app.cookieNameOrDefault(), sid, app.sessionTTL)
}

// writeSessionToken hands the client a (new) token.
func writeSessionToken(w http.ResponseWriter, token string) {
	if token == "" {
		return
	}
	w.Header().Set(skySessionHeader, token)
	w.Header().Set("Cache-Control", "no-store")
}

// tokenAfterRotation returns the token the holder of token (which names
// oldSid) has now that oldSid rotated, and whether it could be derived.
func (app *liveApp) tokenAfterRotation(token, oldSid string) (string, bool) {
	if !app.headerSessions || token == "" || sessionTokenSID(token) != oldSid {
		return "", false
	}
	_, hops, ok := app.followAlias(oldSid)
	if !ok {
		return "", false
	}
	return tokenThroughHops(token, hops)
}

// tokenThroughHopsFrom derives, from a presented token whose id rotated, the
// token of the id the rotation chain ends at.
func tokenThroughHopsFrom(app *liveApp, token string) (string, bool) {
	if !app.headerSessions || token == "" {
		return "", false
	}
	_, hops, ok := app.followAlias(sessionTokenSID(token))
	if !ok {
		return "", false
	}
	return tokenThroughHops(token, hops)
}

// ─── the token on the goroutine trace context ───────────────────────

type liveSessionTokenKeyT struct{}

// stampLiveSessionToken records, on the calling goroutine's trace context,
// the session token the request being served presented, so a rotation
// started by this request's Cmds can derive the next token. Like the origin
// tab it follows every Cmd.perform. The returned func restores the context.
func stampLiveSessionToken(token string) func() {
	if token == "" {
		return func() {}
	}
	prev := CurrentTraceContext()
	SetGoroutineTraceContext(context.WithValue(prev, liveSessionTokenKeyT{}, token))
	return func() { SetGoroutineTraceContext(prev) }
}

// currentLiveSessionToken returns the token stamped on this goroutine.
func currentLiveSessionToken() string {
	if v, ok := CurrentTraceContext().Value(liveSessionTokenKeyT{}).(string); ok {
		return v
	}
	return ""
}

// ─── one-time SSE tickets (the EventSource fallback) ────────────────

// sseTicketTTL is how long an SSE ticket can be redeemed. A variable so tests
// can shorten it.
var sseTicketTTL = 10 * time.Second

// sseTicketMax bounds the outstanding tickets per app (a client asks for one
// per SSE connect; a flood of unredeemed ones must not grow without limit).
const sseTicketMax = 4096

type sseTicket struct {
	sid     string
	tab     string
	expires time.Time
}

// sseTicketBook holds outstanding tickets by the SHA-256 of the ticket, so
// the book itself names no redeemable ticket.
type sseTicketBook struct {
	mu      sync.Mutex
	tickets map[string]sseTicket
}

func (app *liveApp) ticketBook() *sseTicketBook {
	app.sseTicketsOnce.Do(func() {
		if app.sseTickets == nil {
			app.sseTickets = &sseTicketBook{tickets: map[string]sseTicket{}}
		}
	})
	return app.sseTickets
}

// issue mints a ticket for sid and tab.
func (b *sseTicketBook) issue(sid, tab string, now time.Time) string {
	t := newLiveSessionID()
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, v := range b.tickets {
		if !now.Before(v.expires) {
			delete(b.tickets, k)
		}
	}
	if len(b.tickets) >= sseTicketMax {
		// Drop the one closest to expiry.
		var oldest string
		var at time.Time
		for k, v := range b.tickets {
			if oldest == "" || v.expires.Before(at) {
				oldest, at = k, v.expires
			}
		}
		delete(b.tickets, oldest)
	}
	b.tickets[ticketHash(t)] = sseTicket{sid: sid, tab: tab, expires: now.Add(sseTicketTTL)}
	return t
}

// redeem consumes a ticket: it answers once, before it expires, for the tab
// it was issued to.
func (b *sseTicketBook) redeem(ticket, tab string, now time.Time) (string, bool) {
	if ticket == "" {
		return "", false
	}
	k := ticketHash(ticket)
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.tickets[k]
	if !ok {
		return "", false
	}
	delete(b.tickets, k)
	if !now.Before(t.expires) {
		return "", false
	}
	if t.tab != "" && subtle.ConstantTimeCompare([]byte(t.tab), []byte(tab)) != 1 {
		return "", false
	}
	return t.sid, true
}

type sseTicketSIDKey struct{}

// withSSETicket resolves the `tk` query parameter of an SSE request in header
// mode: a valid ticket puts its session id on the request context for
// presentedSID. The ticket is consumed either way.
func (app *liveApp) withSSETicket(r *http.Request) *http.Request {
	if !app.headerSessions {
		return r
	}
	tk := r.URL.Query().Get("tk")
	if tk == "" {
		return r
	}
	sid, ok := app.ticketBook().redeem(tk, r.URL.Query().Get("tab"), time.Now())
	if !ok {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), sseTicketSIDKey{}, sid))
}

// handleSSETicket — POST /_sky/sse-ticket. Header mode only (404 otherwise).
// The request carries the X-Sky-Session token; a live session gets a ticket
// for `tab` (query or JSON body) that /_sky/sse?tk= redeems once.
func (app *liveApp) handleSSETicket(w http.ResponseWriter, r *http.Request) {
	if !app.headerSessions {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Tab string `json:"tab"`
	}
	body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	_ = json.Unmarshal(body, &req)
	bs := app.resolveBoundSession(r, "", req.Tab)
	switch bs.verdict {
	case sessionRotating:
		writeSessionRotating(w)
		return
	case sessionLost:
		writeSessionLost(w)
		return
	}
	if _, live := app.store.Get(bs.sid); !live || bs.sid == "" {
		writeSessionLost(w)
		return
	}
	ticket := app.ticketBook().issue(bs.sid, req.Tab, time.Now())
	w.Header().Set("X-Sky-Live", "1")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	out, _ := json.Marshal(map[string]any{"ticket": ticket, "ttlMs": sseTicketTTL.Milliseconds()})
	_, _ = w.Write(out)
}

// ─── CSRF in header mode ────────────────────────────────────────────

// headerSessionCSRF replaces the double-submit CSRF middleware for an app in
// header mode. It issues no CSRF cookie. A state-changing request passes when
// it carries the X-Sky-Session header (or an Authorization header, or matches
// a CSRF exemption) AND passes sameOriginGuard; every other one is refused.
// SKY_CSRF=off turns the check off, as it does in cookie mode.
func headerSessionCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !csrfEnabled.Load() || isObservabilityPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		mutating := r.Method == http.MethodPost || r.Method == http.MethodPut ||
			r.Method == http.MethodDelete || r.Method == http.MethodPatch
		if !mutating {
			next.ServeHTTP(w, r)
			return
		}
		if isWithoutCsrfRequest(r.Method, r.URL.Path) || r.Header.Get("Authorization") != "" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get(skySessionHeader) == "" {
			csrfReject(w, "csrf_missing", "header session transport: a state-changing request must carry the X-Sky-Session header")
			return
		}
		if reason, ok := sameOriginGuard(r); !ok {
			csrfReject(w, "csrf_origin", reason)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOriginGuard is the Origin / Sec-Fetch-Site check a header-mode request
// passes (the same rule as Server.rpc, rpc_guard.go): Sec-Fetch-Site
// same-origin or none passes; otherwise the Origin must be the app's public
// origin (SKY_PUBLIC_URL, else the request's own scheme and Host). A request
// with neither header is not from a browser and passes. Origin "null" is
// refused.
func sameOriginGuard(r *http.Request) (string, bool) {
	site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	if site == "same-origin" || site == "none" {
		return "", true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		if site == "" {
			return "", true
		}
		return "a " + site + " request without an Origin header is refused", false
	}
	if origin == "null" {
		return "Origin: null is refused", false
	}
	got, ok := normaliseOrigin(origin)
	if !ok {
		return "the Origin header is not a valid origin", false
	}
	for _, want := range rpcPublicOrigins(r) {
		if got == want {
			return "", true
		}
	}
	return "Origin " + got + " is not this app's origin", false
}
