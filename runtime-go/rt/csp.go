package rt

// SKY_CSP=strict — the runtime sends a strict Content-Security-Policy itself.
//
// Every page Sky serves runs under `script-src 'self' 'wasm-unsafe-eval'` with
// no hashes, no nonces, no 'unsafe-inline' and no 'unsafe-eval': the Sky.Live
// client (live_client_asset.go), the Sky.Spa boot loader (spa_boot.go) and the
// legacy console shell (console_html.go) are same-origin files, and per-page
// data is in <script type="application/json"> blocks. A reverse proxy can send
// that policy; SKY_CSP=strict lets an app with no proxy have the same
// protection.
//
// Contract (csp_strict_test.go):
//   - opt-in: unset / "off" leaves every header exactly as before;
//   - never overwrites: a Content-Security-Policy already on the response (set
//     by the app, e.g. Server.withHeader, or by a middleware) wins;
//   - an unknown value is logged once, naming the accepted values, and sends
//     no policy (never a silent no-op).

import (
	"net/http"
	"strings"
	"sync"
)

// strictCSPBase is the policy minus frame-ancestors (appended per call from
// SKY_LIVE_FRAME_ANCESTORS). style-src keeps 'unsafe-inline' because Std.Ui
// renders style attributes; 'wasm-unsafe-eval' lets Sky.Spa instantiate its
// wasm and does not allow JS eval.
const strictCSPBase = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; " +
	"connect-src 'self'; object-src 'none'; base-uri 'self'; form-action 'self'"

var cspBadValueOnce sync.Once

// strictCSPEnabled reports whether SKY_CSP (prefix-aware) asks for the strict
// policy.
func strictCSPEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(skyGetenv("CSP")))
	switch v {
	case "strict":
		return true
	case "", "off", "none", "false", "0":
		return false
	}
	cspBadValueOnce.Do(func() {
		logEmit(logLevelWarn, "warn",
			"unknown "+skyEnvName("CSP")+" value "+`"`+v+`"`+
				" — accepted values are strict and off; no Content-Security-Policy is sent",
			nil)
	})
	return false
}

// strictCSPPolicy returns the full strict policy, with frame-ancestors taken
// from SKY_LIVE_FRAME_ANCESTORS when set and 'self' otherwise.
func strictCSPPolicy() string {
	fa := strings.TrimSpace(skyGetenv("LIVE_FRAME_ANCESTORS"))
	if fa == "" {
		fa = "'self'"
	}
	return strictCSPBase + "; frame-ancestors " + fa
}

// applyStrictCSP adds the strict policy to h when SKY_CSP=strict and h has no
// Content-Security-Policy yet. It reports whether it set one.
func applyStrictCSP(h http.Header) bool {
	if h.Get("Content-Security-Policy") != "" || !strictCSPEnabled() {
		return false
	}
	h.Set("Content-Security-Policy", strictCSPPolicy())
	return true
}

// withStrictCSP wraps a handler (a static file server) so its responses carry
// the strict policy under SKY_CSP=strict. The header is set BEFORE the inner
// handler runs, so a handler that sets its own policy still replaces it.
func withStrictCSP(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		applyStrictCSP(w.Header())
		h.ServeHTTP(w, r)
	})
}
