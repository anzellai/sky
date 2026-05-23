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
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
)

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

// csrfEnabled — global on/off switch. Default ON. Sky.toml
// [security] csrf = false sets this to false via the runtime
// startup path. Opt-out for very specific cases (purely-stateless
// API, every endpoint reads via Bearer auth instead).
var csrfEnabled atomic.Bool

func init() {
	csrfEnabled.Store(true)
	// SKY_CSRF=off|false|0 disables the global CSRF middleware
	// before the first request lands. Intended for pure-API
	// services authenticated via Bearer in Authorization (where
	// the header itself acts as the CSRF defence — cross-origin
	// browsers can't add custom headers without preflight). The
	// sky.toml [security] csrf = false toml-side plumbing routes
	// through here too once it lands. Default-secure: any other
	// value, including unset, keeps CSRF on.
	switch strings.ToLower(os.Getenv("SKY_CSRF")) {
	case "off", "false", "0":
		csrfEnabled.Store(false)
	}
}

// SetCsrfEnabled toggles the global CSRF middleware. Called from
// the runtime startup path when sky.toml [security] csrf = false.
// Tests use it for isolation.
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
		if cookieToken == "" {
			cookieToken = generateSkyCsrfToken()
			// SameSite policy: Strict by default (its own purpose). When
			// SKY_LIVE_FRAME_ANCESTORS opts this deploy into cross-origin
			// embedding, browsers will silently drop a Strict cookie on
			// every iframe request — POSTs from the iframed app's own JS
			// would 403 with "csrf_missing" because the cookie never
			// arrives. None+Secure lets the cookie ride; the X-Sky-Csrf
			// header-vs-cookie check (set by the SAME-ORIGIN iframed JS)
			// remains the actual CSRF gate, since cross-origin attackers
			// can't read the cookie value to forge the header.
			sameSite := http.SameSiteStrictMode
			secure := r.TLS != nil
			if crossOriginIframeMode() {
				sameSite = http.SameSiteNoneMode
				secure = true
			} else if proto := r.Header.Get("X-Forwarded-Proto"); proto == "https" {
				secure = true
			}
			http.SetCookie(w, &http.Cookie{
				Name:     SkyCsrfCookieName,
				Value:    cookieToken,
				Path:     "/",
				HttpOnly: true,
				SameSite: sameSite,
				Secure:   secure,
			})
			// Also stash the freshly-generated token on the
			// request so downstream handlers calling
			// `CurrentCsrfToken(r)` (in particular Sky.Live's
			// HTML render) can embed it into the page's inlined
			// JS on the SAME response that ships Set-Cookie.
			// Without this the very first page load got
			// `__skyCsrfToken = ""` baked in, every state-
			// mutating POST had no `X-Sky-Csrf` header, and
			// the middleware 403'd every click. Silent in
			// production because a refresh would set the cookie
			// the next time, but a SPA like Sky.Live never
			// reloads — every click POSTs through the same JS
			// instance, so the page-load embed is the only
			// chance to seed `__skyCsrfToken`.
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

// csrfReject writes a 403 with a JSON envelope explaining the
// failure mode. Same shape as our other "structured rejection"
// endpoints so client error handlers can pattern-match.
func csrfReject(w http.ResponseWriter, status, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	w.Write([]byte(`{"status":"` + status + `","reason":"` + reason + `"}`))
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
