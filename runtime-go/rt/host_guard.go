//go:build !js

package rt

// host_guard.go — the loopback dev listener refuses a foreign Host header.
//
// # The attack
//
// The dev listener binds 127.0.0.1 (resolveBindHost), and in dev the console
// is open with no login (console_auth_v2.go). Binding loopback stops another
// MACHINE from reaching it. It does not stop another WEBSITE: a page on
// evil.example can re-point its own DNS name at 127.0.0.1 (DNS rebinding).
// The browser then treats http://evil.example:8000 as the page's own origin,
// so the same-origin policy lets that page read every response the local
// server sends back: console logs, traces, analytics, app pages. The one
// thing the browser cannot hide is the name it used, which arrives as
// `Host: evil.example:8000`.
//
// # The guard
//
// When the listener is bound to a loopback address, every request whose Host
// is not an allowed name gets a 403 before any route runs (Sky.Live,
// Sky.Http.Server, the console, SSE, the Sky.Spa backend: they all share the
// one listener handler). Allowed by default:
//
//   - localhost, *.localhost, and any loopback IP literal (127.0.0.0/8, ::1);
//   - the bind address itself;
//   - 10.0.2.2, the Android emulator's alias for the host (app_url.rs);
//   - the host of SKY_APP_URL, the address a native shell loads;
//   - a request with no Host at all (HTTP/1.0 — no browser sends one).
//
// <PREFIX>_ALLOWED_HOSTS (SKY_ALLOWED_HOSTS by default) adds names for a LAN
// phone, a dev proxy (`app.test`) or Codespaces (`*.app.github.dev`); `*`
// turns the guard off.
//
// When the listener is NOT loopback (production binds all interfaces, a
// container, an operator's SKY_HOST=0.0.0.0) the guard does not apply: a
// reverse proxy may legitimately rewrite Host, and there the production
// console gate is the defence.
//
// In production the guard does not apply on a loopback bind either. The
// documented layout "production process on loopback behind a local proxy"
// (ENV=production SKY_HOST=127.0.0.1) receives the public Host from the
// proxy, and the guard answered it 403. The guard exists for the OPEN dev
// console; a production console is authenticated, and a rebinding page
// reaches only what the proxy already serves publicly, without the victim's
// cookies. The residual: an internal production app that uses a loopback
// bind as its only access control, on a host where a browser also runs.
//
// Two listeners keep the guard in every mode: the Sky.Webview loopback server
// (webviewLoopbackGuard) and a Sky.Live server in a desktop window
// (std_app_desktop.go). Neither is ever behind a proxy.
//
// <PREFIX>_PUBLIC_URL (comma list, the same variable the /_rpc origin guard
// reads) also admits its hosts, so a dev or staging proxy name is listed once.

import (
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// hostAllowList is the set of Host names the loopback listener answers.
type hostAllowList struct {
	disabled bool
	exact    map[string]bool
	suffixes []string // ".localhost" style: matches any name that ends with it
}

// normaliseHostName lower-cases a host, strips a port, IPv6 brackets and one
// trailing dot, so `LOCALHOST.:8000` and `localhost` compare equal.
func normaliseHostName(hostport string) string {
	h := strings.TrimSpace(hostport)
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	h = strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
	h = strings.TrimSuffix(h, ".")
	return strings.ToLower(h)
}

// newHostAllowList builds the allow list for a listener bound to bindHost. It
// reads <PREFIX>_ALLOWED_HOSTS and SKY_APP_URL when it is called, so a listener
// builds it once at start-up.
func newHostAllowList(bindHost string) hostAllowList {
	a := hostAllowList{
		exact:    map[string]bool{"localhost": true, "10.0.2.2": true},
		suffixes: []string{".localhost"},
	}
	if b := normaliseHostName(bindHost); b != "" {
		a.exact[b] = true
	}
	// SKY_APP_URL is the address a native shell loads (App.withAppUrl). It is
	// read unprefixed, as the build and the desktop shell read it.
	if raw := strings.TrimSpace(os.Getenv("SKY_APP_URL")); raw != "" {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			a.exact[normaliseHostName(u.Host)] = true
		}
	}
	for _, part := range strings.Split(skyGetenv("PUBLIC_URL"), ",") {
		if u, err := url.Parse(strings.TrimSpace(part)); err == nil && u.Host != "" {
			a.exact[normaliseHostName(u.Host)] = true
		}
	}
	for _, entry := range allowedHostEntries() {
		switch {
		case entry == "*":
			a.disabled = true
		case strings.HasPrefix(entry, "*."):
			a.suffixes = append(a.suffixes, entry[1:])
		case strings.HasPrefix(entry, "."):
			a.suffixes = append(a.suffixes, entry)
		default:
			a.exact[normaliseHostName(entry)] = true
		}
	}
	return a
}

// allowedHostEntries splits <PREFIX>_ALLOWED_HOSTS into lower-cased entries.
func allowedHostEntries() []string {
	var out []string
	for _, e := range strings.Split(skyGetenv("ALLOWED_HOSTS"), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// allows reports whether a request's Host header names this server.
func (a hostAllowList) allows(hostHeader string) bool {
	if a.disabled || strings.TrimSpace(hostHeader) == "" {
		return true
	}
	h := normaliseHostName(hostHeader)
	if a.exact[h] {
		return true
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsLoopback() {
		return true
	}
	for _, s := range a.suffixes {
		if strings.HasSuffix(h, s) && len(h) > len(s) {
			return true
		}
	}
	return false
}

// hostGuardApplies reports whether the listener bound to bindHost gets the
// Host guard: a loopback bind outside production, or any loopback bind of a
// desktop window.
func hostGuardApplies(bindHost string) bool {
	if !isLoopbackBindHost(bindHost) {
		return false
	}
	return desktopWindowActive() || !isProductionMode()
}

// hostGuardMiddleware wraps a listener's whole handler. When the guard
// applies (hostGuardApplies) a request with a foreign Host gets a 403 that
// names the variables that admit it; otherwise it returns next unchanged.
func hostGuardMiddleware(bindHost string, next http.Handler) http.Handler {
	if !hostGuardApplies(bindHost) {
		return next
	}
	return guardHosts(bindHost, next)
}

// guardHosts applies the Host check to next, whatever the mode.
func guardHosts(bindHost string, next http.Handler) http.Handler {
	allow := newHostAllowList(bindHost)
	if allow.disabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !allow.allows(r.Host) {
			rejectForeignHost(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// rejectForeignHost writes the guard's 403.
func rejectForeignHost(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusForbidden)
	_, _ = w.Write([]byte("403 Forbidden: this server is bound to loopback and does not answer Host " +
		strings.TrimSpace(r.Host) + ". Add the name to " + skyEnvName("ALLOWED_HOSTS") +
		" (comma list, *.example.test wildcards, * turns the check off) or to " +
		skyEnvName("PUBLIC_URL") + ", or run with ENV=production behind your proxy.\n"))
}

// devWebSocketOriginPatterns are the WebSocket origin patterns used when an
// app sets no Ws.withOriginPatterns outside production. coder/websocket
// already accepts a client with no Origin and a same-host page; these add a
// loopback dev server on another port (a Vite or other front end) and every
// host listed in <PREFIX>_ALLOWED_HOSTS. A `*` entry is NOT turned into an
// origin wildcard: it disables the Host check, and it must not also let every
// website open a socket.
func devWebSocketOriginPatterns() []string {
	out := []string{
		"localhost", "localhost:*",
		"127.0.0.1", "127.0.0.1:*",
		`\[::1\]`, `\[::1\]:*`, // path.Match syntax: brackets escaped
		"*.localhost", "*.localhost:*",
	}
	for _, e := range allowedHostEntries() {
		if e == "*" {
			continue
		}
		if strings.HasPrefix(e, ".") {
			e = "*" + e
		}
		host := e
		if h, _, err := net.SplitHostPort(e); err == nil {
			host = h
			if strings.Contains(host, ":") {
				host = `\[` + host + `\]` // path.Match: a literal bracket is escaped
			}
		}
		out = append(out, host, host+":*")
	}
	return out
}

// webviewLoopbackGuard is the Host guard of the Sky.Webview loopback server.
// It applies in every mode: that server is never behind a proxy, and it
// serves the user's rendered data to a browser engine on this machine.
func webviewLoopbackGuard(next http.Handler) http.Handler {
	return guardHosts("127.0.0.1", next)
}
