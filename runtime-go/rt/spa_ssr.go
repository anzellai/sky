package rt

// Sky.Spa server-side render (SSR) + hydration support — the PORTABLE core
// (links into both the !js backend that renders and the js client that
// hydrates). Design: docs/skyspa/ssr-design.md.
//
// Two pieces live here:
//
//   - spaHydratableVNode — the POSITIVE structural parity check
//     that decides whether a server-rendered DOM can be hydrated in place, or
//     whether the client must fall back to a full spaMount rebuild. This is the
//     P1 exit-criterion mechanism (§4.4 / §8 risk 1): the `sky-id`-presence
//     check alone is NOT fail-safe because three server/client divergences line
//     up by sky-id yet corrupt on the first structural diff. Consulting this
//     check turns a silent corruption into a caught, correct rebuild.
//
//   - SpaSSRPage — assembles the full first-paint HTML document (per-route
//     <head>, server-rendered body inside #app, base CSS, embedded initial
//     model, wasm loader) that the SSR backend route serves in place of the
//     empty-#app static shell.

import "strings"

// spaSSRMarker is the attribute the SSR backend stamps on the mount so the wasm
// boot path knows the HTML was server-rendered and takes the hydrate branch
// (rather than today's wipe-and-rebuild spaMount). Its presence ALSO means an
// embedded model blob (#sky-model) is available to prime spaModel.
const spaSSRMarker = "data-sky-ssr"

// spaHydratableVNode is the recursive structural parity check. It returns false
// (with a reason) when the tree contains any of the three known server/client
// DOM divergences that a `sky-id`-presence check cannot see:
//
//   - a "raw" node: the server writes n.Text INLINE (live_core.go:444) while the
//     client buildDOM WRAPS it in a <span> (dom_render_wasm.go:74-78), so the
//     hydrated DOM has one fewer/other element than the client tree believes.
//   - adjacent text children: the server concatenates escaped text into one run
//     (live_core.go:440), which the browser parses to a SINGLE text node, while
//     the client keeps them as N separate VNode/text children.
//   - a <textarea> carrying a value: the server splices the value as child text
//     (live_core.go textarea block) while the client models it as the DOM
//     `.value` property, so the child structure differs.
//
// Anything else — element trees with at most one text child per parent and no
// raw/valued-textarea — is byte-structurally identical on both sides and safe to
// hydrate.
func spaHydratableVNode(n VNode) (bool, string) {
	switch n.Kind {
	case "raw":
		return false, "raw node: server renders inline, client wraps in <span> (structural mismatch)"
	case "text":
		return true, ""
	}

	// A <textarea> whose value is present splices as child text server-side but
	// is a property client-side.
	if n.Tag == "textarea" {
		if v, ok := n.Attrs["value"]; ok && v != "" {
			return false, "textarea value: server splices child text, client sets the .value property"
		}
		// A textarea can also carry its value as spliced child text directly.
		for _, c := range n.Children {
			if c.Kind == "text" && c.Text != "" {
				return false, "textarea value: server splices child text, client sets the .value property"
			}
		}
	}

	// Adjacent text/raw children coalesce differently across the boundary.
	prevWasTextual := false
	for _, c := range n.Children {
		textual := c.Kind == "text" || c.Kind == "raw"
		if textual && prevWasTextual {
			return false, "adjacent text: server concatenates into one browser text node, client keeps separate children"
		}
		prevWasTextual = textual
		if ok, reason := spaHydratableVNode(c); !ok {
			return false, reason
		}
	}
	return true, ""
}

// SpaSSRPage assembles the first-paint HTML document served by the SSR backend
// route. It replaces the static WASM_INDEX_HTML shell's EMPTY `<div id="app">`
// with the server-rendered `bodyHTML` (carrying the sky-id/data-sky-* hydration
// contract), splices the per-route `headHTML` (from withHead via RenderSpaHead)
// into the document head, inlines the base CSS reset so first paint is styled
// before wasm boots, embeds `modelJSON` for the client to prime spaModel from,
// and loads the content-hashed `wasmName`. The `data-sky-ssr` marker on #app
// tells the boot path to hydrate rather than rebuild.
func SpaSSRPage(headHTML, bodyHTML, wasmName, modelJSON string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html>` + "\n")
	b.WriteString(`<html lang="en">` + "\n")
	b.WriteString(`<head>`)
	b.WriteString(`<meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(headHTML)
	b.WriteString(`<style>`)
	b.WriteString(liveBaseCSS)
	b.WriteString(`</style>`)
	b.WriteString(`</head>` + "\n")
	b.WriteString(`<body>`)
	// The server-rendered view + the SSR marker so the client hydrates.
	b.WriteString(`<div id="app" ` + spaSSRMarker + `="1">`)
	b.WriteString(bodyHTML)
	b.WriteString(`</div>`)
	// Embedded initial model (JSON-escaped against a `</script>` break-out).
	b.WriteString(`<script id="sky-model" type="application/json">`)
	b.WriteString(escapeModelForScript(modelJSON))
	b.WriteString(`</script>`)
	// The wasm loader — same shape as the static shell.
	b.WriteString(`<script src="wasm_exec.js"></script>`)
	b.WriteString(`<script>const go=new Go();WebAssembly.instantiateStreaming(fetch(`)
	b.WriteString(jsStringLit(wasmName))
	b.WriteString(`),go.importObject).then((res)=>{go.run(res.instance);});</script>`)
	b.WriteString(`</body></html>`)
	return b.String()
}

// escapeModelForScript makes a JSON string safe to inline inside a
// `<script type="application/json">` element: only `<` needs neutralising so a
// `</script>` (or `<!--`) inside a string value cannot terminate the element.
// Replacing `<` with its JSON `<` escape keeps the payload valid JSON.
func escapeModelForScript(json string) string {
	return strings.ReplaceAll(json, "<", `<`)
}

// jsStringLit renders a double-quoted JS string literal for the wasm URL,
// escaping the characters that could break out of the literal.
func jsStringLit(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`)
	return `"` + r.Replace(s) + `"`
}
