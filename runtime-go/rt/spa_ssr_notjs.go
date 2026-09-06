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
// the (optional, P1-empty) embedded model + the content-hashed wasm loader.
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
