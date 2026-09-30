//go:build !js

// rpc_guard.go — the cookie-authenticated RPC route kind (`Server.rpc`).
//
// WHY A SEPARATE ROUTE KIND. `Server.api` is for machine clients that
// authenticate with a Bearer token or an HMAC signature. It is exempt from
// the double-submit CSRF token, and that is correct for it: a browser never
// attaches those credentials by itself. The Sky.Spa auto-split used it for
// every `POST /_rpc/<Msg>`, but those endpoints authenticate with the
// `sky_sid` session COOKIE, which the browser DOES attach by itself. So a
// cross-origin page could fire a CORS-simple request (`Content-Type:
// text/plain`, no custom header, no preflight) at an RPC and the handler ran
// with the victim's session. SameSite=Lax on `sky_sid` narrowed that to
// same-site attackers (a sibling subdomain, another localhost port) and to
// browsers without SameSite, but did not close it.
//
// The double-submit token cannot be used here: the CSRF cookie is HttpOnly
// and the wasm client's fetch has no way to read it. So a `Server.rpc` route
// keeps the CSRF exemption and runs this guard instead, before the handler:
//
//  1. The request method must be the route's method (POST for the split).
//  2. The body must be `Content-Type: application/json`. JSON is not a
//     CORS-safelisted content type, so every CORS-capable browser sends a
//     preflight for a cross-origin JSON POST, and the backend answers no
//     preflight. A form or text/plain body is refused outright.
//  3. `Sec-Fetch-Site: same-origin` (or `none`, a user-initiated request)
//     passes. Any other value needs an `Origin` that equals the app's public
//     origin. `Origin: null` (a sandboxed frame, a data: URL, some
//     redirects) is refused.
//  4. A request with neither `Origin` nor `Sec-Fetch-Site` is not from a
//     modern browser. It passes (with the JSON body of rule 2): a non-browser
//     client holds no victim's cookie, so it can only act as itself.
//
// The app's public origin is `SKY_PUBLIC_URL` when it is set (one URL, or a
// comma-separated list, for a proxy that rewrites Host or a tunnel), else the
// request's own `Host` with the scheme the request arrived on (TLS, or the
// proxy's X-Forwarded-Proto — see requestIsHTTPS). `X-Forwarded-Host` is not
// read: the runtime has no trusted-proxy setting, and SKY_PUBLIC_URL is the
// explicit way to name a public host that differs from the Host header.
//
// Native shells (mobile / desktop, rust/crates/sky/src/app_url.rs) load the
// backend's own http(s) URL in the web view, so their fetches are
// same-origin and pass rule 3 like any browser tab.

package rt

import (
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
)

// Server_rpc registers a cookie-authenticated RPC route. Sky surface:
//
//	Server.rpc : String -> (Request -> Task Error Response) -> Route
//
// `spec` is "METHOD /path"; an omitted method means POST (an RPC carries a
// JSON body). The route is exempt from the double-submit CSRF token for its
// method only, and serverRouteMux runs rpcRequestGuard before the handler.
func Server_rpc(spec any, handler any) any {
	s := fmt.Sprintf("%v", spec)
	method, pattern := "POST", strings.TrimSpace(s)
	if idx := strings.Index(s, " "); idx > 0 {
		method = strings.ToUpper(strings.TrimSpace(s[:idx]))
		pattern = strings.TrimSpace(s[idx+1:])
	}
	WithoutCsrfMethod(method, pattern)
	return SkyRoute{Method: method, Path: pattern, Handler: handler, Rpc: true}
}

// rpcRequestGuard applies the Server.rpc checks. It writes the refusal and
// returns false when the request must not reach the handler.
func rpcRequestGuard(w http.ResponseWriter, r *http.Request, method string) bool {
	if method != "" && !strings.EqualFold(r.Method, method) {
		w.Header().Set("Allow", method)
		rpcRefuse(w, http.StatusMethodNotAllowed, "rpc_method",
			"this RPC endpoint accepts "+method+" only")
		return false
	}
	// E-4: a tab running another wire schema (an old wasm across a deploy)
	// is told to reload before the handler runs (spa_wire.go).
	if spaWireCheck(r.Header.Get(spaWireHeader)) == spaWireMismatch {
		w.Header().Set("X-Sky-Status", "reload")
		w.Header().Set("Cache-Control", "no-store")
		rpcRefuse(w, http.StatusConflict, "rpc_wire",
			"this page was built for another version of the app; reload it")
		return false
	}
	if !rpcIsJSON(r.Header.Get("Content-Type")) {
		rpcRefuse(w, http.StatusForbidden, "rpc_content_type",
			"an RPC request body must be Content-Type: application/json")
		return false
	}
	site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	if site == "same-origin" || site == "none" {
		return true
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		if site == "" {
			// Not a browser: no ambient cookie to abuse.
			return true
		}
		rpcRefuse(w, http.StatusForbidden, "rpc_origin",
			"a "+site+" request without an Origin header is refused")
		return false
	}
	if origin == "null" {
		rpcRefuse(w, http.StatusForbidden, "rpc_origin",
			"Origin: null is refused")
		return false
	}
	got, ok := normaliseOrigin(origin)
	if !ok {
		rpcRefuse(w, http.StatusForbidden, "rpc_origin",
			"the Origin header is not a valid origin")
		return false
	}
	for _, want := range rpcPublicOrigins(r) {
		if got == want {
			return true
		}
	}
	rpcRefuse(w, http.StatusForbidden, "rpc_origin",
		"Origin "+got+" is not this app's origin")
	return false
}

// rpcIsJSON reports whether a Content-Type header names application/json
// (parameters such as charset are allowed).
func rpcIsJSON(ct string) bool {
	if ct == "" {
		return false
	}
	mt, _, err := mime.ParseMediaType(ct)
	return err == nil && mt == "application/json"
}

// rpcPublicOrigins returns the origins an RPC request may come from:
// SKY_PUBLIC_URL (comma-separated) when set, else the request's own
// scheme + Host.
func rpcPublicOrigins(r *http.Request) []string {
	if raw := strings.TrimSpace(skyGetenv("PUBLIC_URL")); raw != "" {
		var out []string
		for _, part := range strings.Split(raw, ",") {
			if o, ok := normaliseOrigin(strings.TrimSpace(part)); ok {
				out = append(out, o)
			}
		}
		return out
	}
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	if o, ok := normaliseOrigin(scheme + "://" + r.Host); ok {
		return []string{o}
	}
	return nil
}

// normaliseOrigin reduces a URL or an Origin header value to
// `scheme://host[:port]`, lower-cased, with the scheme's default port
// dropped. Any path is ignored, so SKY_PUBLIC_URL may be written with a
// trailing slash.
func normaliseOrigin(s string) (string, bool) {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]" // IPv6 literal
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, true
}

// rpcRefuse writes the guard's refusal as a small JSON envelope that names
// the setting an operator changes when the refusal is a misconfiguration
// rather than an attack.
func rpcRefuse(w http.ResponseWriter, status int, code, reason string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	hint := "Sky.Spa RPCs accept same-origin JSON requests only. Behind a proxy that rewrites the Host header, or a tunnel, set SKY_PUBLIC_URL to the URL the browser uses (for example https://app.example.com)."
	fmt.Fprintf(w, `{"status":%q,"reason":%q,"hint":%q}`, code, reason, hint)
}
