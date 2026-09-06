package rt

import (
	"fmt"
	"strconv"
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

// SpaFns is the reflect-free client dispatch table (docs/skyspa/prod-web.md
// Path A). Codegen emits it for a Sky.Spa app in place of the all-`any` TEA
// config struct: each field is a typed adapter CLOSURE that calls the app's
// concrete update/view/init/subscriptions directly (type assertions, not
// `reflect.Value.Call`) and repacks the tuple result at concrete type. The
// wasm driver invokes these closures, so the client TEA loop dispatches with
// zero reflection — the prerequisite for a TinyGo-compiled web client. The
// server (Sky.Live/Tui/Webview) path never constructs a SpaFns; it keeps its
// reflect-based dispatch unchanged.
//
//   - Init  : flags        -> ( model, Cmd msg ) repacked as SkyTuple2
//   - Update: msg, model   -> ( model, Cmd msg ) repacked as SkyTuple2
//   - View  : model        -> Html msg  (as any)
//   - Subs  : model        -> Sub msg   (as any; nil when the config omits it)
type SpaFns struct {
	Init   func(any) SkyTuple2
	Update func(any, any) SkyTuple2
	View   func(any) any
	Subs   func(any) any
}

// Spa_config materialises the config into a map[string]any that the withX
// builders (Routes / NotFound / OnNavigate) clone-and-set onto and spaRun reads
// via rt.Field. The Sky.Spa target passes a reflect-free `SpaFns` (typed
// adapter closures); it is stored whole under "Fns" and the driver invokes its
// closures directly. A non-SpaFns argument (host `sky build` go-build, or an
// older/foreign emit) falls back to the record-reflect form so the code still
// links — the wasm client always receives a SpaFns from codegen.
func Spa_config(req any) any {
	if fns, ok := req.(SpaFns); ok {
		return map[string]any{"Fns": fns}
	}
	return map[string]any{
		"Init":          Field(req, "Init"),
		"Update":        Field(req, "Update"),
		"View":          Field(req, "View"),
		"Subscriptions": Field(req, "Subscriptions"),
	}
}

// asSpaFns unwraps the SpaFns the driver reads from the config map ("Fns"
// key), reflect-free (a plain type assertion). A zero SpaFns (nil closures) is
// returned when the value is absent/foreign — codegen guarantees a SpaFns for
// every real Sky.Spa client, so this only guards the host-build no-op path.
func asSpaFns(v any) SpaFns {
	if f, ok := v.(SpaFns); ok {
		return f
	}
	return SpaFns{}
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
	// intParam marks a route whose `:param` segments are Ints (Spa_routeInt):
	// each captured segment is parsed with strconv.Atoi before it is applied to
	// the page constructor, and a segment that is NOT a valid integer makes the
	// route fail to match (so `/todo/abc` falls through to the next route / the
	// 404 rather than constructing a bogus page).
	intParam bool
}

// Spa_route builds a route from a path pattern and a page value/constructor.
// Mirrors Live_route (live.go) but returns the portable spaRoute so the js
// driver can read it without depending on any //go:build !js type.
func Spa_route(path any, page any) any {
	return spaRoute{path: fmt.Sprintf("%v", path), page: page}
}

// Spa_routeInt builds a route whose captured `:param` segments are Ints: the
// page is a constructor taking Int(s) (`TodoPage : Int -> Page`). The captured
// segment is parsed to an int before it reaches the constructor, and a
// non-integer segment makes the route not match. This is the typed-Int route
// param — the runtime cannot introspect the constructor's expected type under
// the erased ABI, so the Int-ness is declared at the route, not inferred.
func Spa_routeInt(path any, page any) any {
	return spaRoute{path: fmt.Sprintf("%v", path), page: page, intParam: true}
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

// Spa_withHead stores the `model -> List (Html msg)` head builder under "Head".
// The SSR backend route reads it and renders the per-route <head> for SEO
// (renderAppHead-shape, live.go). The wasm client IGNORES it — head stays
// server-owned, exactly as Sky.Live does (head is honoured only on the initial
// GET). Stored on the config map so it survives the withX builder chain.
func Spa_withHead(fn, cfg any) any { return spaCfgSet(cfg, "Head", fn) }

// Spa_withModelDecoder stores the `String -> Result Error model` decoder under
// "ModelDecoder". The wasm client applies it to the SSR-embedded `#sky-model`
// JSON blob to reconstruct the TYPED initial model (design §4.5), booting from
// the server-resolved data instead of re-running the effectful `init`. The
// server IGNORES it (it embeds via Codec.toJson; the decode is a client concern),
// exactly as `Head` is a server concern the client ignores. Stored on the config
// map so it survives the withX builder chain.
func Spa_withModelDecoder(fn, cfg any) any { return spaCfgSet(cfg, "ModelDecoder", fn) }

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
			if r.intParam {
				// A non-integer segment means this Int route does not match;
				// keep looking (fall through to the next route / the 404).
				if page, filled := spaFillPageInt(r.page, params); filled {
					return page, true
				}
				continue
			}
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

// spaFillPageInt is spaFillPage for an Int route: each captured segment is
// parsed with strconv.Atoi and applied to the constructor as a Go int (Sky Int
// == Go int, per rt.AsInt). Returns (_, false) when a segment is not a valid
// integer, so the route does not match. A plain (non-function) page value has
// nothing to parse and is returned as-is.
func spaFillPageInt(page any, params []string) (any, bool) {
	if len(params) == 0 || !isFunc(page) {
		return page, true
	}
	curr := page
	for _, p := range params {
		if !isFunc(curr) {
			break
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, false
		}
		curr = sky_call(curr, n)
	}
	return curr, true
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
