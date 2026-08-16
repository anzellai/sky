package rt

// CSRF protection — Phase 1.2. Default-on for Sky.Live's POST
// /_sky/event endpoint and Sky.Http.Server's state-mutating methods
// (POST/PUT/DELETE/PATCH). Closes the AI-deployed-app footgun where
// `Cmd.perform (Db.deleteAll dbConn) Deleted` is exposed to any
// origin that can convince a logged-in user's browser to POST.
//
// Why default-on:
//   * SameSite=Lax cookies are NOT sufficient — Chrome/Edge default-
//     Lax does NOT cover top-level POST navigation, and a misconfigured
//     subdomain can defeat it.
//   * AI writing Sky code doesn't think about CSRF. The framework
//     should give them protection by construction.
//   * Production users who genuinely need a webhook receiver opt out
//     explicitly via Middleware.withoutCsrf — visible in source review.
//
// Mechanism: double-submit cookie.
//
//   1. First request → server sets cookie `__sky_csrf=<32-byte-hex>;
//      Path=/; HttpOnly; SameSite=Strict; [Secure]` IF the request
//      is a GET (token only issued during read flows; POSTs that
//      lack the cookie are rejected before reaching this code).
//   2. Same response also surfaces the token to the page — Sky.Live
//      injects it into the inlined `__skyCsrfToken` JS variable.
//      Sky.Http.Server users access it via `Server.csrfToken req`
//      (the existing helper).
//   3. Every subsequent state-mutating request (POST/PUT/DELETE/
//      PATCH) MUST carry the token in the `X-Sky-Csrf` header.
//      `__skySend` does this automatically; user-written `fetch()`
//      calls need to add the header.
//   4. Server compares header to cookie with crypto/subtle. Match →
//      request proceeds; mismatch / missing → 403 + JSON
//      "{\"status\":\"csrf_invalid\"}".
//
// Why HttpOnly cookie + header (not just cookie or just header):
//   * Cookie alone (CSRF via cookie value comparison): the attacker
//     can read their OWN cookie and forge cross-origin requests.
//   * Header alone: not bound to the session; replay-attackable.
//   * Cookie + header double-submit: attacker's iframe can't read
//     the victim's HttpOnly cookie, so can't construct a matching
//     header. Safe.
//
// Why SameSite=Strict (not Lax):
//   * Strict refuses to send the cookie on top-level POST nav.
//     Combined with double-submit, two defences are better than one.
//   * The cost — the user can't bookmark a deep-link to a POST
//     endpoint that depends on CSRF — is acceptable for Sky.Live's
//     wire protocol where every state-mutating call comes from the
//     loaded SPA, not an external link.

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// csrfCookieMaxAgeSeconds is the CSRF cookie's lifetime. It MUST outlive the
// session it guards — including a session that SLIDES indefinitely via the SSE
// heartbeat while the tab sits IDLE (no GET/POST in flight to re-issue the
// cookie). Keying Max-Age to SKY_LIVE_TTL broke that: with e.g.
// SKY_LIVE_TTL=30m (the documented production pattern) the __sky_csrf cookie
// expired after 30m of idle while the server session kept sliding on the
// heartbeat, so the next event POST 403'd, got queued+retried (all 403), painted
// the reconnecting/offline banner, and stranded the user until a manual refresh
// re-issued the cookie via GET — the exact "idle 20-30min → disconnected →
// refresh fixes it" incident. The double-submit token's security is the
// cookie==header match, NOT a short cookie lifetime, so use a long fixed floor
// (30 days) decoupled from the session TTL, never below a longer configured TTL.
// Shares slidingCookieMaxAgeSeconds with the sky_sid session cookie
// (writeSessionCookie): both guard the same sliding session, so the rule lives
// in ONE place rather than being re-derived per cookie — the drift that left
// sky_sid on a TTL-keyed Max-Age after this one was fixed.
func csrfCookieMaxAgeSeconds() int {
	// §1.7's THIRD `LIVE_TTL` reader, and the one with a different default (30
	// days here against live.go's 30 minutes). It has no builder layer of its
	// own — there is no `withCsrfTtl` — so it passes "" and resolves through
	// the same shared rule as the other two. Routing it through `resolveTTL`
	// does not change what it reads today; it means the reader cannot acquire
	// a fourth precedence order later without the shared gate noticing.
	return slidingCookieMaxAgeSeconds(resolveTTL("", 30*24*time.Hour))
}

const (
	// SkyCsrfCookieName is the cookie that holds the session's CSRF
	// token. Read by the middleware on state-mutating requests;
	// also returned to the page for JS to echo in the header.
	SkyCsrfCookieName = "__sky_csrf"

	// SkyCsrfHeaderName is the request header that carries the
	// token on state-mutating requests. Matches the JS code in
	// runtime-go/rt/live.go __skySend.
	SkyCsrfHeaderName = "X-Sky-Csrf"
)

// csrfEnabled — global on/off switch. Default ON. Turned off by
// `<PREFIX>_CSRF=off|false|0`, which sky.toml's `[security] csrf =
// false` seeds. Opt-out for very specific cases (purely-stateless
// API, every endpoint reads via Bearer auth instead).
var csrfEnabled atomic.Bool

func init() {
	refreshCsrfEnabled()
	// Re-read after SetEnvPrefix / SetSkyDefault. The generated
	// init() seeds the sky.toml default AFTER this package's init()
	// has already run, so without this hook `[security] csrf = false`
	// would be written to the env too late to be seen — the same
	// stale-capture that logJSON / logThreshold register for.
	onEnvPrefixChange(refreshCsrfEnabled)
}

// refreshCsrfEnabled (re-)reads the CSRF switch from the environment.
//
// `<PREFIX>_CSRF=off|false|0` disables the global CSRF middleware
// before the first request lands. Intended for pure-API services
// authenticated via Bearer in Authorization (where the header itself
// acts as the CSRF defence — cross-origin browsers can't add custom
// headers without preflight).
//
// Default-secure: any other value, including unset, keeps CSRF ON.
// That is why this assigns both branches rather than only clearing —
// a re-read must be able to restore the default, not just drop it.
func refreshCsrfEnabled() {
	switch strings.ToLower(skyGetenv("CSRF")) {
	case "off", "false", "0":
		csrfEnabled.Store(false)
	default:
		csrfEnabled.Store(true)
	}
}

// SetCsrfEnabled toggles the global CSRF middleware. Tests use it for
// isolation; the sky.toml / env path goes through refreshCsrfEnabled.
func SetCsrfEnabled(on bool) {
	csrfEnabled.Store(on)
}

// IsCsrfEnabled returns the current state. Exposed for tests and
// for the JS template that injects __skyCsrfToken — it skips the
// inject when CSRF is disabled.
func IsCsrfEnabled() bool {
	return csrfEnabled.Load()
}

// CSRFMiddleware wraps the given handler with double-submit CSRF
// protection. Mounted by Sky.Live + Sky.Http.Server BEFORE the
// observability middleware (so a 403 CSRF rejection still gets
// metered as a request) but AFTER panic recovery.
//
// Behaviour:
//
//   - Request method is read-only (GET / HEAD / OPTIONS) → issue
//     cookie if missing (so first-paint sets it up), pass through.
//   - Path matches a `withoutCsrf` opt-out (registered via
//     `WithoutCsrf(path)` from user code) → pass through unchanged.
//   - Observability endpoints (/_sky/healthz, /_sky/readyz,
//     /_sky/metrics, /_sky/buildinfo, /_sky/sse) → pass through
//     (no state mutation; SSE is GET).
//   - State-mutating method (POST/PUT/DELETE/PATCH) → require
//     `X-Sky-Csrf` header matching `__sky_csrf` cookie. Both
//     present + equal → pass. Missing or mismatch → 403.
func CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !csrfEnabled.Load() {
			next.ServeHTTP(w, r)
			return
		}
		// Observability + SSE skip — see above.
		if isObservabilityPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// User opt-out path.
		if isWithoutCsrfPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// Credentialed-API exemption. A request bearing an `Authorization`
		// header (Bearer / Basic) authenticates via a NON-ambient credential:
		// the browser never auto-attaches it, and cross-origin JS can neither
		// read the token nor set the header without a CORS preflight the server
		// must approve. So such a request cannot be a CSRF vector — exempt it so
		// JSON / Bearer APIs work out of the box, without disabling CSRF
		// globally (SKY_CSRF=off) or per-route (WithoutCsrf). Cookie-session
		// browser POSTs (no Authorization header) stay fully protected. This is
		// the standard framework exemption (Django REST / Rails).
		if r.Header.Get("Authorization") != "" {
			next.ServeHTTP(w, r)
			return
		}

		method := r.Method
		isMutating := method == http.MethodPost ||
			method == http.MethodPut ||
			method == http.MethodDelete ||
			method == http.MethodPatch

		// Read or set the per-session cookie. We set on EVERY
		// response that doesn't already have the cookie (not just
		// GET) so a flow that starts with a POST still gets a
		// usable token issued; that POST will fail CSRF (no
		// header) but subsequent requests will succeed.
		cookieToken := ""
		if c, err := r.Cookie(SkyCsrfCookieName); err == nil {
			cookieToken = c.Value
		}
		newlyIssued := cookieToken == ""
		if newlyIssued {
			cookieToken = generateSkyCsrfToken()
		}
		// L5: issue the CSRF cookie as a PERSISTENT, SLIDING cookie — re-set on
		// every (non-observability) request with a fresh MaxAge, not a session
		// cookie. Pre-fix it had no MaxAge, so browsers that clear session
		// cookies on tab-discard / sleep-wake (Safari/ITP, Chrome tab discard)
		// dropped it while a long-lived Sky.Live SPA stayed open; the next POST
		// then regenerated a NEW cookie but the page still sent the OLD baked
		// header → 403 forever (the SPA never reloads to re-seed the token). A
		// persistent, re-issued cookie survives those evictions and slides with
		// activity, keyed to the session TTL so it outlives the session it
		// guards.
		//
		// SameSite policy: Strict by default (its own purpose). When
		// SKY_LIVE_FRAME_ANCESTORS opts this deploy into cross-origin embedding,
		// browsers silently drop a Strict cookie on every iframe request — POSTs
		// from the iframed app's own JS would 403 with "csrf_missing" because the
		// cookie never arrives. None+Secure lets the cookie ride; the X-Sky-Csrf
		// header-vs-cookie check (set by the SAME-ORIGIN iframed JS) remains the
		// actual CSRF gate, since cross-origin attackers can't read the cookie.
		// Secure mirrors the session cookie's rule exactly
		// (writeSessionCookie in live.go): TLS on the wire, or the
		// production env flag, or cross-origin iframe mode. Sharing
		// requestIsHTTPS + productionFromEnv keeps the two cookies
		// from drifting apart the way isProd/productionFromEnv did.
		sameSite := http.SameSiteStrictMode
		secure := requestIsHTTPS(r) || productionFromEnv()
		if crossOriginIframeMode() {
			sameSite = http.SameSiteNoneMode
			secure = true
		}
		http.SetCookie(w, &http.Cookie{
			Name:     SkyCsrfCookieName,
			Value:    cookieToken,
			Path:     "/",
			HttpOnly: true,
			MaxAge:   csrfCookieMaxAgeSeconds(),
			SameSite: sameSite,
			Secure:   secure,
		})
		if newlyIssued {
			// Stash the freshly-generated token on the request so downstream
			// handlers calling `CurrentCsrfToken(r)` (in particular Sky.Live's
			// HTML render) embed it into the page's inlined JS on the SAME
			// response that ships Set-Cookie. Without this the very first page
			// load got `__skyCsrfToken = ""` baked in, every state-mutating POST
			// had no `X-Sky-Csrf` header, and the middleware 403'd every click.
			r.AddCookie(&http.Cookie{
				Name:  SkyCsrfCookieName,
				Value: cookieToken,
			})
		}

		if !isMutating {
			next.ServeHTTP(w, r)
			return
		}

		// State-mutating: token MUST match cookie. Read from
		// `X-Sky-Csrf` header first (Sky.Live JS sets this on
		// every fetch). Fall back to a `__sky_csrf` form field for
		// traditional Sky.Http.Server HTML-form POSTs that don't
		// run JS. The form-field path calls `ParseForm` which
		// caches the parse on the request, so downstream
		// `r.FormValue("…")` reads still work.
		submitted := r.Header.Get(SkyCsrfHeaderName)
		if submitted == "" {
			ct := r.Header.Get("Content-Type")
			isFormEncoded := strings.HasPrefix(ct, "application/x-www-form-urlencoded") ||
				strings.HasPrefix(ct, "multipart/form-data")
			if isFormEncoded {
				_ = r.ParseForm()
				submitted = r.FormValue("__sky_csrf")
			}
		}
		// sendBeacon path — see csrfTokenFromJSONBody. navigator.sendBeacon
		// CANNOT set a header, so Sky.Live's unload flush carries the token
		// in its JSON body instead. Scoped to the Live event endpoint and
		// only reached when no header was supplied, so the fetch path is
		// untouched and no other route pays a body read.
		if submitted == "" && isLiveEventPath(r.URL.Path) &&
			strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			tok, ok := csrfTokenFromJSONBody(r)
			if !ok {
				csrfReject(w, "csrf_missing",
					"beacon body exceeded the CSRF peek limit or was not readable")
				return
			}
			submitted = tok
		}
		if submitted == "" || cookieToken == "" {
			csrfReject(w, "csrf_missing", "missing X-Sky-Csrf header / __sky_csrf form field, or __sky_csrf cookie")
			return
		}
		// crypto/subtle.ConstantTimeCompare returns 1 on equal,
		// 0 on different OR different-length. Defeats timing-attack
		// token discovery.
		if subtle.ConstantTimeCompare([]byte(submitted), []byte(cookieToken)) != 1 {
			csrfReject(w, "csrf_invalid", "submitted CSRF token does not match cookie")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// csrfBodyPeekMax bounds how much of a request body the middleware will
// buffer to find a body-borne CSRF token. Beacon batches are a handful of
// debounced field values plus an inputState snapshot — kilobytes. The
// multi-MB payloads /_sky/event legitimately carries (Event.onFile /
// Event.onImage ship a base64 data URL through the same endpoint) always
// travel by `fetch`, which SETS the X-Sky-Csrf header, so they short-circuit
// above and never reach the peek. A header-less JSON POST bigger than this
// is rejected outright rather than buffered.
const csrfBodyPeekMax = 1 << 20 // 1 MiB

// isLiveEventPath reports whether a path is the Sky.Live event endpoint.
// Root-mounted apps serve it at "/_sky/event"; sub-apps mounted in-process
// (the console at "/_sky/console") serve it at "<prefix>/_sky/event"
// (subapp_inprocess.go), hence the suffix match.
func isLiveEventPath(path string) bool {
	return path == "/_sky/event" || strings.HasSuffix(path, "/_sky/event")
}

// csrfTokenFromJSONBody extracts the `csrf` field from a JSON request body
// and RESTORES the body so the downstream handler still sees it intact.
//
// WHY THIS EXISTS. `navigator.sendBeacon(url, data)` takes exactly two
// arguments — there is no init/headers parameter, so a beacon PHYSICALLY
// CANNOT set X-Sky-Csrf. Sky.Live's unload flush
// (__skyFlushPendingBeacon in live.go) is a beacon, so before this it was
// rejected `csrf_missing` on every CSRF-enabled app and the user's final
// debounced keystrokes were dropped on tab close. The beacon does control
// its own body, so the token rides there.
//
// WHY THIS IS A REAL BIND, NOT AN EXEMPTION. It is the same double-submit
// property the header check has: the token is compared against the
// __sky_csrf COOKIE, and a cross-origin page cannot read that cookie, so it
// cannot populate the body half. Nothing here weakens the check — it only
// moves where the submitted half is read from, for one request shape that
// cannot use a header.
//
// WHY NOT application/x-www-form-urlencoded. Switching the beacon's Blob to
// a form encoding would have hit the EXISTING r.FormValue fallback with a
// one-line change, and it is UNSOUND: urlencoded (and text/plain, and
// multipart) are CORS-SAFELISTED content types, so a cross-origin
// sendBeacon using one fires with NO preflight. application/json is not
// safelisted, so a cross-origin beacon is preflighted and refused by the
// browser before it is ever sent. Keeping the JSON content type is
// load-bearing, which is why the caller gates on it.
//
// WHY NOT a query parameter. It would need no body read at all, but it puts
// the token in the URL, where proxy access logs and Referer headers capture
// it. The body keeps it off that surface.
//
// Returns ok=false when the body is unreadable or exceeds csrfBodyPeekMax,
// which the caller turns into a rejection — never a pass.
func csrfTokenFromJSONBody(r *http.Request) (string, bool) {
	if r.Body == nil {
		return "", true
	}
	// Read one byte past the ceiling so "exactly at the limit" is accepted
	// and "over" is detectable without buffering the whole oversize body.
	buf, err := io.ReadAll(io.LimitReader(r.Body, csrfBodyPeekMax+1))
	if err != nil {
		return "", false
	}
	if len(buf) > csrfBodyPeekMax {
		return "", false
	}
	// Restore the body unconditionally — handleEvent does its own
	// io.ReadAll under a MaxBytesReader, and a consumed body would turn
	// every beacon into an empty-JSON 400.
	r.Body = io.NopCloser(bytes.NewReader(buf))
	var probe struct {
		Csrf string `json:"csrf"`
	}
	// A malformed body is not a CSRF failure per se; it yields an empty
	// token, and the caller's `submitted == ""` check rejects it.
	_ = json.Unmarshal(buf, &probe)
	return probe.Csrf, true
}

// csrfReject writes a 403 with a JSON envelope explaining the
// failure mode. Same shape as our other "structured rejection"
// endpoints so client error handlers can pattern-match.
func csrfReject(w http.ResponseWriter, status, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	// The hint names the escape hatches so a machine client isn't left
	// guessing why a POST 403'd: CSRF only guards cookie-session browser
	// requests. An API caller either sends an Authorization header (auto-
	// exempt), sets SKY_CSRF=off, or exempts the route via WithoutCsrf(path).
	w.Write([]byte(`{"status":"` + status + `","reason":"` + reason +
		`","hint":"CSRF guards cookie-session browser POSTs. API clients: send an Authorization header (auto-exempt), or set SKY_CSRF=off, or exempt the route with WithoutCsrf(path)."}`))
}

// isObservabilityPath — true for paths the CSRF middleware skips
// because they're read-only (GET) or are the SSE connection (which
// runs over GET and is authenticated by session cookie alone).
//
// The /_sky/console family is included because the dashboard polls
// its API endpoints every 1s via plain fetch (no CSRF token to
// attach — the dashboard is a static HTML shell, not a Sky.Live
// app). Admin-auth is the production gate for these, layered
// inside the handlers themselves.
func isObservabilityPath(path string) bool {
	if !strings.HasPrefix(path, "/_sky/") {
		return false
	}
	// Console + console API subroutes — match by prefix.
	if path == "/_sky/console" || strings.HasPrefix(path, "/_sky/console/") {
		return true
	}
	// Specific endpoints that must always pass:
	switch path {
	case "/_sky/healthz", "/_sky/readyz", "/_sky/metrics",
		"/_sky/buildinfo", "/_sky/sse", "/_sky/config",
		// Sub-app observability ingest — POSTed to by children
		// via the push exporter. Has its own auth via
		// X-Sky-Ingest-Token (validated by HandleObservabilityIngest);
		// CSRF cookies are irrelevant because no browser is involved.
		// Without this exemption every child push hits 403 and
		// federation silently breaks.
		"/_sky/observability/ingest":
		return true
	}
	return false
}

// ─── User opt-out registry ────────────────────────────────────

// withoutCsrfPaths — registered via WithoutCsrf(path). Webhooks
// from external services (Stripe, GitHub, Slack) verify via HMAC
// signature, not session cookie — they need to bypass CSRF.
var withoutCsrfPaths atomic.Pointer[[]string]

func init() {
	empty := []string{}
	withoutCsrfPaths.Store(&empty)
}

// WithoutCsrf registers a path that bypasses CSRF protection.
// Idempotent (re-registering a path is a no-op).
//
// Use for webhook receivers that authenticate via vendor-provided
// HMAC signature in the request body:
//
//	WithoutCsrf("/webhooks/stripe")
//	WithoutCsrf("/webhooks/github")
//
// User code calls this from app startup (typically the
// equivalent of a `main` body before `Live.app` / `Server.listen`).
//
// Path matching is exact (no prefix wildcards). For a path family
// like `/webhooks/*`, register each leaf you actually mount.
func WithoutCsrf(path string) {
	for {
		old := withoutCsrfPaths.Load()
		for _, p := range *old {
			if p == path {
				return // already registered
			}
		}
		new_ := append([]string{}, *old...)
		new_ = append(new_, path)
		if withoutCsrfPaths.CompareAndSwap(old, &new_) {
			return
		}
	}
}

// ResetWithoutCsrf is a test-only helper to clear the registry
// between cases. Production never calls this.
func ResetWithoutCsrf() {
	empty := []string{}
	withoutCsrfPaths.Store(&empty)
}

func isWithoutCsrfPath(path string) bool {
	for _, p := range *withoutCsrfPaths.Load() {
		if csrfPatternMatch(p, path) {
			return true
		}
	}
	return false
}

// csrfPatternMatch reports whether request path `path` matches a
// registered exempt pattern `pat`. A `:name` segment in the
// pattern is a wildcard for exactly one path segment, so api
// routes like `POST /api/orders/:id/cancel` are exempt for every
// concrete id. A pattern with no `:` is matched exactly.
func csrfPatternMatch(pat, path string) bool {
	if pat == path {
		return true
	}
	if !strings.Contains(pat, ":") {
		return false
	}
	ps := strings.Split(strings.Trim(pat, "/"), "/")
	rs := strings.Split(strings.Trim(path, "/"), "/")
	if len(ps) != len(rs) {
		return false
	}
	for i := range ps {
		if strings.HasPrefix(ps[i], ":") {
			continue
		}
		if ps[i] != rs[i] {
			return false
		}
	}
	return true
}

// ─── Token generation ─────────────────────────────────────────

// generateSkyCsrfToken returns a fresh 32-byte hex CSRF token
// (~256 bits of randomness, well past any feasible brute-force).
//
// Falls back to an empty string on crypto/rand failure rather than
// returning a weak token — better to fail CSRF than to issue a
// guessable token. Empty cookie → 403 on subsequent state-mutation,
// which surfaces to the user instead of silently weakening security.
func generateSkyCsrfToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// CurrentCsrfToken extracts the CSRF token from the request's
// cookie. Used by Sky.Live's HTML render to inject the token into
// the page (`__skyCsrfToken` JS variable) so the client-side
// `__skySend` can echo it on every POST.
//
// Returns empty string when the cookie is absent — the next
// response will set it.
func CurrentCsrfToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	if c, err := r.Cookie(SkyCsrfCookieName); err == nil {
		return c.Value
	}
	return ""
}
