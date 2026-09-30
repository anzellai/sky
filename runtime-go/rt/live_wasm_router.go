package rt

import "strings"

// live_wasm_router.go — which same-origin links the Sky.Spa client router
// (live_wasm.go spaInstallRouter) keeps in the app, and which it leaves to the
// browser. Portable, so the host tests pin it (live_wasm_router_test.go).
//
// The bug this closes (audit H-3). The router took EVERY same-origin <a href>
// that had no `sky-external` / `download` / `target=_blank`, pushed the URL and
// routed it in the client. A link to a path only the server serves never
// reached the server: "Sign out" (`App.api "GET /admin/logout"`) showed the
// in-app 404 and did not sign the user out, and a link to the Sky console
// (`/_sky/console`) did the same. The client routes only the paths it has a
// client route for; everything else is the server's, so the browser loads it.

// spaReservedServerPrefixes are the path spaces the Sky runtime serves on every
// backend (the console, metrics, the RPC and sub endpoints): never a client page.
var spaReservedServerPrefixes = []string{"/_sky/", "/_rpc/"}

// spaServerRoute marks a path the backend serves (an `App.api` route): a link to
// it is a full navigation even when a client route would also match it, since a
// page load of that path reaches the server route. Built by Spa_serverRoute and
// carried in the Spa config's route list beside the client routes.
type spaServerRoute struct {
	method  string // "" or "*" = any method
	pattern string
}

// Spa_serverRoute : String -> Route
//
// A server-only path (the `App.api` spec, `"GET /logout"` or `"/webhook"`) for
// the client router. It routes nothing in the client.
func Spa_serverRoute(spec any) any {
	s, _ := spec.(string)
	return parseSpaServerRoute(s)
}

func parseSpaServerRoute(spec string) spaServerRoute {
	spec = strings.TrimSpace(spec)
	if i := strings.IndexByte(spec, ' '); i > 0 {
		return spaServerRoute{method: strings.ToUpper(spec[:i]), pattern: strings.TrimSpace(spec[i+1:])}
	}
	return spaServerRoute{pattern: spec}
}

// asSpaServerRoutes picks the server-only entries out of the config's route list
// (asSpaRoutes skips them, so the client routing is unchanged).
func asSpaServerRoutes(v any) []spaServerRoute {
	if v == nil {
		return nil
	}
	var out []spaServerRoute
	for _, e := range asList(v) {
		if r, ok := e.(spaServerRoute); ok {
			out = append(out, r)
		}
	}
	return out
}

// matchesGet reports whether a GET of path reaches this server route. A
// pattern ending in "/" is a subtree mount (as in Go's ServeMux).
func (r spaServerRoute) matchesGet(path string) bool {
	switch r.method {
	case "", "*", "GET", "HEAD":
	default:
		return false
	}
	if r.pattern == "" {
		return false
	}
	if len(r.pattern) > 1 && strings.HasSuffix(r.pattern, "/") {
		return strings.HasPrefix(path, r.pattern) || path == strings.TrimSuffix(r.pattern, "/")
	}
	_, ok := spaMatchRoute(r.pattern, path)
	return ok
}

// spaLinkIsServerPath reports whether a click on a same-origin link to path
// must be a full browser navigation rather than a client route: a runtime path
// (/_sky/, /_rpc/), a server route (`App.api`), or a path no client route
// matches (a static file, a server page, a URL the app does not know: the
// server answers it, with its own not-found page when it has none).
func spaLinkIsServerPath(routes []spaRoute, server []spaServerRoute, path string) bool {
	p := spaRoutePath(path)
	for _, pre := range spaReservedServerPrefixes {
		if strings.HasPrefix(p, pre) || p == strings.TrimSuffix(pre, "/") {
			return true
		}
	}
	for _, r := range server {
		if r.matchesGet(p) {
			return true
		}
	}
	_, ok := spaResolveRoutes(routes, p)
	return !ok
}
