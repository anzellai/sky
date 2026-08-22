package rt

import (
	"fmt"
	"strings"
)

// Sky.Spa — client-side TEA entry kernels + config builders + routing (portable
// core; no build tag, so `sky check`'s host go-build and the wasm build both see
// them).
//
// A Sky.Spa app is written exactly like a Sky.Live app — Model / Msg / pure
// update / view over the renderer-agnostic Element/Html — but the TEA loop runs
// on the CLIENT (compiled to GOOS=js GOARCH=wasm) instead of the server.
//
//   main = Spa.app (Spa.config { init = .., update = .., view = .., subscriptions = .. }
//                     |> Spa.withRoutes [ Spa.route "/" Home ]
//                     |> Spa.withNotFound NotFound)
//
// lowers to:
//
//   rt.AnyTaskRun(rt.TaskCoerceT[Error, struct{}](
//       rt.Spa_app(rt.Spa_withNotFound(notFound,
//                   rt.Spa_withRoutes(routes, rt.Spa_config(cfg))))))
//
// mirroring Sky.Live's Live_config / Live_withX / Live_app shape (live_config.go
// / live.go), so the existing task-forcing codegen path drives it unchanged.

// Spa_config materialises the config record `{ init, update, view,
// subscriptions }` into a map[string]any keyed by the PascalCase names spaRun
// reads via rt.Field, exactly as Live_config does — so the withX builders can
// attach optional fields (Routes / NotFound / OnNavigate) onto the same map.
// (It used to be the identity on the record; a map is required now that the
// builders clone-and-set. spaRun's Field(cfg,"…") reads a map by key, so this is
// transparent to an app that attaches no builders.)
func Spa_config(req any) any {
	return map[string]any{
		"Init":          Field(req, "Init"),
		"Update":        Field(req, "Update"),
		"View":          Field(req, "View"),
		"Subscriptions": Field(req, "Subscriptions"),
	}
}

// Spa_app wraps the client TEA loop in a Task thunk. AnyTaskRun forces it at
// main; spaRun is build-split — the real single-threaded wasm driver lives in
// live_wasm.go (//go:build js); spa_notjs.go carries a no-op for the normal
// build so `sky build` (which go-builds the emitted app for the host) links.
func Spa_app(cfg any) any {
	return func() any { return spaRun(cfg) }
}

// spaRoute is the runtime representation of a Sky.Spa `Route` (Spa_route). Path
// is the pattern (`/thing/:id`); Page is the page value or a page constructor
// that captured params are applied to. Kept portable so the wasm driver and any
// host-side check see the same type.
type spaRoute struct {
	path string
	page any
}

// Spa_route builds a route from a path pattern and a page value/constructor.
// Mirrors Live_route (live.go) but returns the portable spaRoute so the js
// driver can read it without depending on any //go:build !js type.
func Spa_route(path any, page any) any {
	return spaRoute{path: fmt.Sprintf("%v", path), page: page}
}

// spaCfgSet returns a shallow clone of the AppConfig map with key=val set. It
// clones (Go maps are reference types) so sibling derivations in a builder chain
// never alias one base map — the same invariant liveCfgSet documents
// (live_config.go). Defensive: a non-map cfg starts a fresh map (the required
// fields were set by Spa_config in the normal chain).
func spaCfgSet(cfg any, key string, val any) any {
	src, ok := cfg.(map[string]any)
	if !ok {
		src = map[string]any{}
	}
	out := make(map[string]any, len(src)+1)
	for k, v := range src {
		out[k] = v
	}
	out[key] = val
	return out
}

// Spa_withRoutes stores the route list verbatim under "Routes"; spaRun reads it
// and resolves the URL against it. Order matters (literal before :param).
func Spa_withRoutes(routes, cfg any) any { return spaCfgSet(cfg, "Routes", routes) }

// Spa_withNotFound stores the 404 page value under "NotFound".
func Spa_withNotFound(page, cfg any) any { return spaCfgSet(cfg, "NotFound", page) }

// Spa_withOnNavigate stores the `page -> msg` callback under "OnNavigate".
func Spa_withOnNavigate(fn, cfg any) any { return spaCfgSet(cfg, "OnNavigate", fn) }

// ── Route matching (portable pure helpers) ──────────────────────────
//
// Reimplements Sky.Live's matchRoute / splitPath algorithm (live.go:1600-1624)
// client-side, verbatim in behaviour, so the server file is NOT touched and the
// Sky.Live path stays byte-identical. Kept here (portable) rather than in the
// js-only driver so a host-side test can exercise it directly.

// spaSplitPath trims leading/trailing '/' so `/a/b/` and `/a/b` match the same,
// then splits on '/'. Mirrors live.go splitPath.
func spaSplitPath(p string) []string {
	p = strings.Trim(p, "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

// spaMatchRoute compares a pattern like `/thing/:id` against a path, returning
// the ordered captured segment values on success. Mirrors live.go matchRoute.
func spaMatchRoute(pattern, path string) ([]string, bool) {
	patSegs := spaSplitPath(pattern)
	pathSegs := spaSplitPath(path)
	if len(patSegs) != len(pathSegs) {
		return nil, false
	}
	var params []string
	for i, ps := range patSegs {
		if strings.HasPrefix(ps, ":") {
			params = append(params, pathSegs[i])
		} else if ps != pathSegs[i] {
			return nil, false
		}
	}
	return params, true
}

// spaResolveRoutes walks the routes in order and returns the first match's page
// (with any captured string params applied to a page constructor), or false
// when nothing matches. A page that is a constructor gets each captured segment
// passed as a String argument (v1 supports String params; a typed-Int param
// constructor is a documented parity follow-up — the server's reflect-based
// coerceRouteParam is //go:build !js and is not carried onto the client).
func spaResolveRoutes(routes []spaRoute, path string) (any, bool) {
	for _, r := range routes {
		if params, ok := spaMatchRoute(r.path, path); ok {
			return spaFillPage(r.page, params), true
		}
	}
	return nil, false
}

// spaFillPage applies captured string params to a page constructor. A plain
// (non-function) page value is returned as-is.
func spaFillPage(page any, params []string) any {
	if len(params) == 0 || !isFunc(page) {
		return page
	}
	curr := page
	for _, p := range params {
		if !isFunc(curr) {
			break
		}
		curr = sky_call(curr, p)
	}
	return curr
}

// asSpaRoutes narrows the "Routes" config value (a Sky list of Route) to a
// []spaRoute. A non-route element is skipped rather than trapping.
func asSpaRoutes(v any) []spaRoute {
	if v == nil {
		return nil
	}
	var out []spaRoute
	for _, e := range asList(v) {
		if r, ok := e.(spaRoute); ok {
			out = append(out, r)
		}
	}
	return out
}
