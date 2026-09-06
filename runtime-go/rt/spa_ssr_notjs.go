//go:build !js

package rt

// Sky.Spa server-side render (SSR) — the BACKEND render kernels. Portable-core
// pieces (the structural parity check + the page assembler) live in the
// build-tag-free spa_ssr.go; these are the pieces that only make sense on the
// native backend that RENDERS (they wrap `renderAppHead` from live.go, //go:build
// !js, and read the built frontend dist), so they carry the `!js` tag and are
// absent from the wasm client. Design: docs/skyspa/ssr-design.md §4.1 / §4.3.
//
// The generated backend route (rust/crates/project/src/spa_split.rs::gen_backend)
// references these as `Ffi.kernel "Spa_ssr*"` aliases; that reference is also what
// keeps `renderAppHead` / `HtmlRenderWithHandlers` LIVE in the backend binary —
// link-time DCE drops them until an SSR route calls them (design §1). None of
// these touch liveSession / SSE / broker state — they are pure over their inputs
// plus one read-only dist directory scan.

import (
	"os"
	"reflect"
	"strings"
)

// RenderSpaHead runs the app's optional `head : model -> List (Html msg)`
// builder for a route-resolved model and serialises the returned list to the
// per-route `<head>` HTML, reusing Sky.Live's `renderAppHead` VERBATIM (the same
// renderVNode pipeline, the same discarded-handlers contract — head nodes carry
// no wire events). Returns "" when there is no head builder or it yields no
// nodes, byte-identical to a Sky.Live page with no `withHead`.
func RenderSpaHead(head, model any) string {
	return renderAppHead(head, model)
}

// Spa_ssrRenderHead is the `Ffi.kernel "Spa_ssrRenderHead"` alias the generated
// backend calls: `spaSsrRenderHead spaHead_ model0`. Thin wrapper over
// RenderSpaHead so the render logic has one home.
func Spa_ssrRenderHead(head, model any) string {
	return RenderSpaHead(head, model)
}

// Spa_ssrRenderBody is the `Ffi.kernel "Spa_ssrRenderBody"` alias: it renders
// `view(model0)` (a Sky `Html` ADT) to the body HTML that fills `#app`, via the
// shared `HtmlRenderWithHandlers` façade at idPrefix "r" — the SAME id prefix +
// style-injection pipeline the wasm client's first render uses
// (live_wasm.go:328, assignSkyIDs "r"), so every `sky-id` the server stamps is
// the one the client recomputes and hydrates against. The returned handler
// table is discarded: the SERVER never dispatches, the client rebuilds it
// locally from the in-memory VNode (design §3).
func Spa_ssrRenderBody(node any) string {
	body, _ := HtmlRenderWithHandlers(node, "r")
	return body
}

// Spa_ssrPage is the `Ffi.kernel "Spa_ssrPage"` alias assembling the first-paint
// document via the portable SpaSSRPage (spa_ssr.go): per-route head + the
// server-rendered body inside a `data-sky-ssr`-marked `#app` + base CSS reset +
// the embedded resolved-model JSON (design §4.5 — the route-resolved, settled
// model the client can boot from) + the content-hashed wasm loader.
func Spa_ssrPage(head, body, wasmName, modelJSON any) string {
	return SpaSSRPage(AsString(head), AsString(body), AsString(wasmName), AsString(modelJSON))
}

// Spa_ssrWasmName resolves the content-hashed wasm filename the SSR page must
// load, by scanning the built frontend `dist` directory for its single
// `main.<hash>.wasm` (stage_web_bundle prunes every older one, so there is
// exactly one — rust/crates/sky/src/main.rs:2389-2402). The generated backend
// serves that same directory via `Server.static "/" "../frontend/dist"`, so it
// passes that path here. Falls back to the un-hashed `main.wasm` when the
// directory is unreadable or carries no hashed wasm (a bare `main.wasm` deploy),
// so the loader is never empty.
func Spa_ssrWasmName(dir any) string {
	entries, err := os.ReadDir(AsString(dir))
	if err != nil {
		return "main.wasm"
	}
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, "main.") && strings.HasSuffix(n, ".wasm") {
			return n
		}
	}
	return "main.wasm"
}

// Spa_ssrResolveModel resolves the requested URL path to the route's page and
// folds it into the model EXACTLY as the wasm client does at boot
// (spaResolveRoutes + RecordUpdate {"Page": page}, live_wasm.go:163-175), so the
// server renders the SAME per-route view the client would compute for that path.
// This is what makes SSR per-route (design §4.1): the P1 handler rendered only
// the root; this resolves `/`, `/items`, `/posts/:id`, … each to its own page.
//
//   - A path matching a route → that route's page (a page constructor gets the
//     captured `:param` segments applied, via spaFillPage).
//   - No match + a notFound page set → the notFound page.
//   - No match + no notFound → the model's Page is left unchanged (mirrors
//     spaApplyURL's fall-through).
//
// `routesV` is the same `List (Spa.Route msg)` value the client config carries;
// `asSpaRoutes` narrows it to the portable []spaRoute the matcher walks.
func Spa_ssrResolveModel(routesV, notFound, model, path any) any {
	routes := asSpaRoutes(routesV)
	if len(routes) == 0 {
		return model
	}
	page, ok := spaResolveRoutes(routes, AsString(path))
	if !ok {
		if notFound == nil {
			return model
		}
		page = notFound
	}
	return RecordUpdate(model, map[string]any{"Page": page})
}

// Spa_ssrSettle drives the data-resolved SSR step (design §4.2): it runs the
// initial command's GET-safe reads to a settled model server-side, so the first
// paint carries REAL per-route content (the blog post body, the item list) that
// a crawler sees — not the empty loading state P1 rendered.
//
// SOUNDNESS / the GET-safe boundary. This kernel is only EMITTED by the
// generated backend when the synthesis proved, statically over the app's `init`
// source, that `cmd0` is a curated GET-safe read (spa_split.rs gen_backend's
// allowlist scan — File.readFile / Http.get / Db.query / Db.findOneByField, the
// idempotent reads). That is where the fail-closed decision is made: an `init`
// whose `cmd0` is a write, a non-deterministic effect, or anything unrecognised
// gets the chrome-only handler instead and this kernel is never referenced. So
// this runner does NOT need to (and cannot, under the erased Task ABI) classify
// the task itself — the caller already proved it safe. It runs at most ONE round
// (design §10 q1: a single read-round covers "load the page's data once"); the
// update's returned follow-up Cmd is intentionally not chased, bounding the GET.
func Spa_ssrSettle(model, cmd, update any) any {
	return spaSsrSettleRound(model, cmd, update)
}

// spaSsrSettleRound folds every perform leaf of `cmd` into `model` via `update`,
// once. A batch fans out over its members (each still one round). Mirrors the
// server's runPerformBody task→toMsg→update shape (live.go:3806-3811) but
// synchronous and lock-free — SSR renders on the request goroutine, no session.
func spaSsrSettleRound(model, cmd, update any) any {
	c, ok := cmd.(cmdT)
	if !ok {
		return model
	}
	m := model
	switch c.kind {
	case "batch":
		for _, sub := range c.batch {
			m = spaSsrSettleRound(m, sub, update)
		}
	case "perform":
		// Run the read (task : () -> Result), then map its Result to a Msg and
		// fold it — the same two sky_call steps runPerformBody uses (live.go:3809).
		result := sky_call(c.task, nil)
		msg := sky_call(c.toMsg, result)
		// `update : Msg -> Model -> ( Model, Cmd )` is emitted as a typed 2-arg Go
		// func; SkyCall dispatches it reflect-tolerantly whether it arrives curried
		// or 2-arg. Its result is a TYPED tuple `T2[Model, any]` (not the erased
		// T2[any,any]), so extract V0 by field, not by a T2[any,any] assertion.
		pair := SkyCall(update, msg, m)
		if next, ok := tupleFirstField(pair); ok {
			m = next
		}
	}
	return m
}

// tupleFirstField extracts field V0 of any `T2[A, B]` tuple value regardless of
// its concrete instantiation — the settle folds an `update` whose result is a
// TYPED `T2[Model, any]`, which a `T2[any, any]` assertion would miss. Returns
// (value, true) for a struct carrying a V0 field, else (nil, false).
func tupleFirstField(pair any) (any, bool) {
	rv := reflect.ValueOf(pair)
	if rv.Kind() == reflect.Struct {
		if f := rv.FieldByName("V0"); f.IsValid() {
			return f.Interface(), true
		}
	}
	return nil, false
}
