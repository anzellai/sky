package rt

// Sky.Spa server-side render (SSR) + hydration support — the PORTABLE core
// (links into both the !js backend that renders and the js client that
// hydrates). Design: docs/skyspa/ssr-design.md.
//
// Two pieces live here:
//
//   - the SSR model seed decode (spaDecodeModelBlob) and the boot plan
//     (spaPlanBoot). The hydrate-or-rebuild decision itself, and the text-run
//     split that makes the server DOM match the client tree, are in
//     spa_hydrate.go.
//
//   - SpaSSRPage — assembles the full first-paint HTML document (per-route
//     <head>, server-rendered body inside #app, base CSS, embedded initial
//     model, wasm loader) that the SSR backend route serves in place of the
//     empty-#app static shell.

import (
	"reflect"
	"strings"
)

// spaSSRMarker is the attribute the SSR backend stamps on the mount so the wasm
// boot path knows the HTML was server-rendered and takes the hydrate branch
// (rather than today's wipe-and-rebuild spaMount). Its presence ALSO means an
// embedded model blob (#sky-model) is available to prime spaModel.
const spaSSRMarker = "data-sky-ssr"

// spaDecodeModelBlob turns the SSR-embedded `#sky-model` JSON blob back into the
// TYPED initial model by applying the app's model DECODER (design §4.5). The
// decoder is the Sky closure `\s -> Codec.fromJson (Codec.auto blank) s` the
// auto-split wires onto `Spa.config` (`Spa_withModelDecoder`, stored under
// "ModelDecoder"); applying it reconstructs a value of the SAME Go shape the
// backend embedded, which is exactly what the reflect-free client adapters
// assert (`a0.(Main_Model_R)`) — so priming `spaModel` from it never trips the
// hard type assertions a generic `map[string]any` prime would.
//
// Portable (no build tag) so a host Go test can exercise the JSON→typed-model
// round-trip without a browser. Returns (model, true) only on a decode Ok; a nil
// decoder, an empty blob, or a decode Err returns (nil, false) so the caller
// falls back to running `init` — never a wrong/partial prime.
func spaDecodeModelBlob(blob string, decoder any) (any, bool) {
	if decoder == nil || strings.TrimSpace(blob) == "" {
		return nil, false
	}
	return spaResultOk(sky_call(decoder, blob))
}

// spaResultOk extracts the Ok value of a Sky `Result` produced by a `sky_call`.
// The decoder is typed `String -> Result Error a`, so its erased return is a
// `SkyResult[SkyADT, any]` (Error erases to SkyADT) or a `SkyResult[any, any]`;
// a reflection fallback handles any other `E`/`A` instantiation so the extractor
// is not coupled to one monomorphisation. Returns (OkValue, true) when the tag
// is Ok (0); (nil, false) for an Err or an unrecognised value.
func spaResultOk(r any) (any, bool) {
	switch v := r.(type) {
	case SkyResult[any, any]:
		return v.OkValue, v.Tag == 0
	case SkyResult[SkyADT, any]:
		return v.OkValue, v.Tag == 0
	case nil:
		return nil, false
	}
	// Reflection fallback: read the {Tag, OkValue} fields of any SkyResult[E,A].
	rv := reflect.ValueOf(r)
	if rv.Kind() == reflect.Struct {
		tag := rv.FieldByName("Tag")
		ok := rv.FieldByName("OkValue")
		if tag.IsValid() && ok.IsValid() && tag.Kind() == reflect.Int {
			return ok.Interface(), tag.Int() == 0
		}
	}
	return nil, false
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
	return SpaSSRPageSettled(headHTML, bodyHTML, wasmName, modelJSON, "")
}

// SpaSSRPageSettled is SpaSSRPage with the `data-sky-settled` marker on #app:
// `settled` is the space-separated list of commands the server FINISHED for
// this page ("init", "nav"; spaSettledAttr). An empty list tells the client
// that it must still run both.
func SpaSSRPageSettled(headHTML, bodyHTML, wasmName, modelJSON, settled string) string {
	return SpaSSRPageSeeded(headHTML, bodyHTML, wasmName, modelJSON, settled, nil)
}

// SpaSSRPageSeeded is SpaSSRPageSettled plus the `data-sky-seed-fields`
// marker (R2): the model fields the server settled for THIS page. On a full
// load the client takes these fields from the `#sky-model` seed and restores
// every other field from localStorage (spaMergeStoredOverSeed).
// spaSSRNoScriptCSS turns the first-paint loading overlay off when scripts
// cannot run. The overlay (`data-sky-hydrating`, liveBaseCSS) blocks clicks
// until the wasm client clears it, and only script clears it (the client, or
// the 12 s safety timer). With JS disabled the server-rendered page was
// covered by "Loading…" forever, so its links and its forms (a native POST
// that the injected __sky_csrf token keeps valid) could not be used.
const spaSSRNoScriptCSS = `<noscript><style>html[data-sky-hydrating]{cursor:auto}` +
	`html[data-sky-hydrating]::before,html[data-sky-hydrating]::after{display:none}</style></noscript>`

func SpaSSRPageSeeded(headHTML, bodyHTML, wasmName, modelJSON, settled string, seedFields []string) string {
	var b strings.Builder
	b.WriteString(`<!doctype html>` + "\n")
	// `data-sky-hydrating` drives the first-paint loading affordance (progress
	// cursor + top bar, liveBaseCSS) until the wasm client boots and hydrates,
	// then clears it (spaClearHydratingMarker). Without it a click on the
	// server-rendered DOM before the (heavy) wasm loads is silently dead.
	b.WriteString(`<html lang="en" data-sky-hydrating="1">` + "\n")
	b.WriteString(`<head>`)
	b.WriteString(`<meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(headHTML)
	b.WriteString(`<style>`)
	b.WriteString(liveBaseCSS)
	b.WriteString(`</style>`)
	b.WriteString(spaSSRNoScriptCSS)
	b.WriteString(`</head>` + "\n")
	b.WriteString(`<body>`)
	// The server-rendered view + the SSR marker so the client hydrates.
	b.WriteString(`<div id="app" ` + spaSettledMarker + `="` + settled + `" ` +
		spaSeedFieldsMarker + `="` + spaSeedFieldsAttr(seedFields) + `" ` + spaSSRMarker + `="1">`)
	b.WriteString(bodyHTML)
	b.WriteString(`</div>`)
	// Embedded initial model (JSON-escaped against a `</script>` break-out).
	b.WriteString(`<script id="sky-model" type="application/json">`)
	b.WriteString(escapeModelForScript(modelJSON))
	b.WriteString(`</script>`)
	// The wasm loader — same shape as the static shell. Assets are referenced by
	// ROOT-ABSOLUTE URL (leading `/`): the static shell serves `../frontend/dist`
	// at `/`, so a bare relative `wasm_exec.js` on a cold two-segment deep-link
	// (`/blog/<slug>`) resolves to `/blog/wasm_exec.js` (404 → text/html → "Go is
	// not defined") and the wasm never boots. A leading slash is correct at any
	// route depth.
	b.WriteString(`<script src="/wasm_exec.js"></script>`)
	b.WriteString(`<script>const go=new Go();WebAssembly.instantiateStreaming(fetch(`)
	b.WriteString(jsStringLit(rootAbsoluteAsset(wasmName)))
	b.WriteString(`),go.importObject).then((res)=>{go.run(res.instance);});`)
	// Safety net for the blocking hydration overlay: the client normally clears
	// `data-sky-hydrating` after it hydrates (spaClearHydratingMarker). If the wasm
	// never boots (a failed fetch/instantiate on a flaky network), drop the
	// blocking overlay after 12s so the page is not locked forever.
	b.WriteString(`setTimeout(function(){document.documentElement.removeAttribute('data-sky-hydrating')},12000);</script>`)
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

// rootAbsoluteAsset makes an asset name served from the frontend dist root a
// root-absolute URL by ensuring exactly one leading slash. The wasm name is a
// bare `main.<hash>.wasm`; an already-rooted value is left unchanged.
func rootAbsoluteAsset(name string) string {
	return "/" + strings.TrimPrefix(name, "/")
}

// jsStringLit renders a double-quoted JS string literal for the wasm URL,
// escaping the characters that could break out of the literal.
func jsStringLit(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`)
	return `"` + r.Replace(s) + `"`
}

// spaSettledMarker is the attribute the SSR page stamps on #app naming the
// commands the server FINISHED while it rendered the page (register M,
// SPA-10). Its value is a space-separated token list:
//
//   - "init" — init's command ran to the end on the server (or was empty);
//   - "nav"  — the route's onNavigate command ran to the end on the server.
//
// "Ran to the end" is Spa_ssrSettleFull's verdict: every leaf ran, no
// follow-up was left un-chased and no destructive effect was suppressed. The
// client boots from the `#sky-model` seed and skips exactly the commands named
// here; any other command still runs once on the client.
const spaSettledMarker = "data-sky-settled"

// spaSeedFieldsMarker is the attribute naming the model fields the server
// settled for this page (R2, see SpaSSRPageSeeded).
const spaSeedFieldsMarker = "data-sky-seed-fields"

// spaSeedFieldsAttr renders the seed-field list (deduplicated, in order). A
// field name is a Sky identifier, so it never needs HTML escaping; anything
// else is dropped rather than written into the attribute.
func spaSeedFieldsAttr(fields []string) string {
	seen := map[string]bool{}
	var out []string
	for _, f := range fields {
		bad := strings.IndexFunc(f, func(r rune) bool {
			return !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z')
		}) >= 0
		if f == "" || bad || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// spaSettledAttr renders the marker's token list.
func spaSettledAttr(initDone, navDone bool) string {
	var toks []string
	if initDone {
		toks = append(toks, "init")
	}
	if navDone {
		toks = append(toks, "nav")
	}
	return strings.Join(toks, " ")
}

// spaParseSettled reads the marker's token list.
func spaParseSettled(attr string) (initDone, navDone bool) {
	for _, t := range strings.Fields(attr) {
		switch t {
		case "init":
			initDone = true
		case "nav":
			navDone = true
		}
	}
	return
}

// spaBootPlanT is what the client runs at boot besides the first paint.
type spaBootPlanT struct {
	// runInitCmd: run init's command after the mount.
	runInitCmd bool
	// prePaintNav: run onNavigate through update BEFORE the first paint (SPA-9,
	// a cold boot from init, so the first client paint matches the server's).
	prePaintNav bool
	// navAfterMount: fire onNavigate after the mount.
	navAfterMount bool
}

// spaPlanBoot decides the boot sequence (live_wasm.go spaRun). `seeded` is true
// when the client booted from the SSR `#sky-model` seed; `settled` is the
// page's data-sky-settled value; `navHook` is true when the app routes and set
// onNavigate; `twoStep` is true for a localStorage restore painted in two steps.
//
// Sky.Live runs onNavigate once per navigation. A seeded boot already holds the
// result of the server's run, so running it again on the client repeats the
// load (and, before this rule, the repeated RPC was built from a model that did
// not match the page). It is skipped only when the server says it finished it.
func spaPlanBoot(seeded bool, settled string, navHook, twoStep bool) spaBootPlanT {
	initDone, navDone := spaParseSettled(settled)
	p := spaBootPlanT{runInitCmd: !(seeded && initDone)}
	if !navHook {
		return p
	}
	switch {
	case seeded && navDone:
		// The seed carries the finished navigation.
	case !seeded && !twoStep:
		p.prePaintNav = true
	default:
		p.navAfterMount = true
	}
	return p
}
