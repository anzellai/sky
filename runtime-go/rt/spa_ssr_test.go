package rt

import (
	"strings"
	"testing"
)

// rawNode builds a raw-HTML VNode (used by the SSR hydration tests).
func rawNode(s string) VNode { return VNode{Kind: "raw", Text: s} }

// ── SSR client-leg: the JSON blob → typed model decode (design §4.5) ──
//
// The wasm boot path (spaBootFromSSRModel) reads the `#sky-model` blob from the
// DOM (js-only) and hands it to the PORTABLE spaDecodeModelBlob, which applies
// the app's model decoder (`Spa_withModelDecoder`) and extracts the Ok model.
// These tests exercise that portable core without a browser: a fake decoder
// closure stands in for the synthesised `\s -> Codec.fromJson (Codec.auto blank)`
// so the round-trip (blob → typed model, and the fall-back paths) is host-tested.

// A typed model shape standing in for the app's Main_Model_R — the decode must
// return THIS Go type (not a map[string]any) so the reflect-free adapters' hard
// `a0.(modelR)` assertion holds.
type ssrTestModel struct {
	Page  string
	Items []string
}

func TestSpaDecodeModelBlob_returnsTypedModelOnOk(t0 *testing.T) {
	want := ssrTestModel{Page: "ItemsPage", Items: []string{"Alpha", "Beta"}}
	// A decoder that parses the blob and returns Ok(typed model), like the
	// synthesised `Codec.fromJson (Codec.auto blank)` does.
	decoder := func(blob any) any {
		if s, ok := blob.(string); ok && strings.Contains(s, "ItemsPage") {
			return Ok[SkyADT, any](want)
		}
		return Err[SkyADT, any](SkyADT{SkyName: "Error"})
	}
	got, ok := spaDecodeModelBlob(`{"page":"ItemsPage","items":["Alpha","Beta"]}`, decoder)
	if !ok {
		t0.Fatalf("a decoder returning Ok must yield (model, true)")
	}
	m, isTyped := got.(ssrTestModel)
	if !isTyped {
		t0.Fatalf("decode must return the TYPED model, got %T", got)
	}
	if m.Page != "ItemsPage" || len(m.Items) != 2 || m.Items[0] != "Alpha" {
		t0.Fatalf("decoded model mismatch: %+v", m)
	}
}

func TestSpaDecodeModelBlob_fallsBackOnErr(t0 *testing.T) {
	decoder := func(any) any { return Err[SkyADT, any](SkyADT{SkyName: "Error"}) }
	if _, ok := spaDecodeModelBlob(`{"bad":true}`, decoder); ok {
		t0.Fatalf("a decoder returning Err must yield ok=false (fall back to init)")
	}
}

func TestSpaDecodeModelBlob_noDecoderOrEmptyBlob(t0 *testing.T) {
	called := false
	decoder := func(any) any { called = true; return Ok[SkyADT, any](ssrTestModel{}) }
	// Empty / whitespace blob → no decode attempted.
	if _, ok := spaDecodeModelBlob("   ", decoder); ok {
		t0.Fatalf("an empty blob must yield ok=false")
	}
	if called {
		t0.Fatalf("the decoder must not run on an empty blob")
	}
	// Nil decoder (no Spa_withModelDecoder wired) → no decode.
	if _, ok := spaDecodeModelBlob(`{"page":"Home"}`, nil); ok {
		t0.Fatalf("a nil decoder must yield ok=false")
	}
}

func TestSpaResultOk_tagsAndReflectionFallback(t0 *testing.T) {
	// Concrete SkyResult[any,any].
	if v, ok := spaResultOk(Ok[any, any]("x")); !ok || v.(string) != "x" {
		t0.Fatalf("Ok[any,any] must extract its value")
	}
	if _, ok := spaResultOk(Err[any, any]("boom")); ok {
		t0.Fatalf("Err[any,any] must report not-ok")
	}
	// The decoder's real erased shape: SkyResult[SkyADT, any].
	if v, ok := spaResultOk(Ok[SkyADT, any](42)); !ok || v.(int) != 42 {
		t0.Fatalf("Ok[SkyADT,any] must extract its value")
	}
	// Reflection fallback for any other E/A instantiation.
	if v, ok := spaResultOk(SkyResult[string, int]{Tag: 0, OkValue: 7}); !ok || v.(int) != 7 {
		t0.Fatalf("reflection fallback must extract Ok of an arbitrary SkyResult[E,A]")
	}
	if _, ok := spaResultOk(SkyResult[string, int]{Tag: 1, ErrValue: "e"}); ok {
		t0.Fatalf("reflection fallback must report not-ok for an Err")
	}
	// A non-Result value is never Ok.
	if _, ok := spaResultOk("not a result"); ok {
		t0.Fatalf("a non-Result value must report not-ok")
	}
}

func TestSpaSSRPage_servesRealBodyHeadModelNotEmptyDiv(t0 *testing.T) {
	body := `<h1 sky-id="r.0#h1">Welcome</h1>`
	head := `<title>Welcome — My Site</title><meta name="description" content="hi">`
	page := SpaSSRPage(head, body, "main.abc123.wasm", `{"page":"Home"}`)

	// The server-rendered body lands INSIDE #app — not the empty div the static
	// WASM_INDEX_HTML shell ships (the BUG-1 symptom SSR fixes).
	if strings.Contains(page, `<div id="app"></div>`) {
		t0.Fatalf("SSR page must not ship an EMPTY #app:\n%s", page)
	}
	if !strings.Contains(page, `="1">`+body) {
		t0.Fatalf("server-rendered body must sit inside the (marked) #app:\n%s", page)
	}
	if !strings.Contains(page, `<div id="app" `) {
		t0.Fatalf("the mount must still be #app (client looks it up by that id):\n%s", page)
	}
	// The per-route <head> is present and is NOT the hardcoded <title>Sky.Spa</title>.
	if !strings.Contains(page, head) {
		t0.Fatalf("withHead-derived <head> must be spliced into the document head:\n%s", page)
	}
	if strings.Contains(page, "<title>Sky.Spa</title>") {
		t0.Fatalf("SSR page must not carry the hardcoded default title:\n%s", page)
	}
	// The base reset is present so first paint is styled before wasm boots.
	if !strings.Contains(page, liveBaseCSS) {
		t0.Fatalf("SSR page must inline the base CSS reset:\n%s", page)
	}
	// The hydration affordance: `<html>` carries `data-sky-hydrating` at first
	// paint and the base CSS drives the progress cursor + top bar, so a click
	// before the wasm boots is visibly "loading", not silently dead. The client
	// clears the marker after it hydrates (spaClearHydratingMarker).
	if !strings.Contains(page, `data-sky-hydrating`) {
		t0.Fatalf("SSR page <html> must carry the data-sky-hydrating marker:\n%s", page)
	}
	if !strings.Contains(liveBaseCSS, `html[data-sky-hydrating]`) {
		t0.Fatalf("base CSS must define the hydration affordance (progress cursor + bar)")
	}
	// The overlay BLOCKS interaction (pointer-events:auto) until hydration, so a
	// pre-boot click is caught + told to wait, not swallowed into a dead control.
	if !strings.Contains(liveBaseCSS, `html[data-sky-hydrating]::after`) ||
		!strings.Contains(liveBaseCSS, `pointer-events:auto`) {
		t0.Fatalf("base CSS must define the blocking hydration overlay (::after, pointer-events:auto)")
	}
	// A safety timeout drops the blocking marker if the wasm never boots.
	if !strings.Contains(page, `removeAttribute('data-sky-hydrating')`) {
		t0.Fatalf("SSR page must carry the hydration-overlay safety timeout:\n%s", page)
	}
	// The initial model is embedded for the client to prime spaModel from.
	if !strings.Contains(page, `id="sky-model"`) || !strings.Contains(page, `{"page":"Home"}`) {
		t0.Fatalf("SSR page must embed the initial model blob:\n%s", page)
	}
	// A marker tells the boot path this HTML was SSR-rendered (→ hydrate path).
	if !strings.Contains(page, spaSSRMarker) {
		t0.Fatalf("SSR page must carry the SSR marker for the boot path:\n%s", page)
	}
	// The wasm loader references the content-hashed wasm name.
	if !strings.Contains(page, "main.abc123.wasm") {
		t0.Fatalf("SSR page must load the content-hashed wasm:\n%s", page)
	}
}

// A cold deep-link (`/blog/<slug>`) served by the SSR backend must reference its
// assets by ROOT-ABSOLUTE URL (`/wasm_exec.js`, `/main.<hash>.wasm`), never by a
// bare relative name. The static shell serves `../frontend/dist` at `/`, so a
// relative `wasm_exec.js` on a two-segment page resolves to `/blog/wasm_exec.js`
// (404 → text/html → "Go is not defined") and the wasm never boots. The leading
// slash makes the URLs correct at any route depth.
func TestSpaSSRPage_referencesRootAbsoluteAssets(t0 *testing.T) {
	page := SpaSSRPage(`<title>Post</title>`, `<h1>Post</h1>`, "main.abc123.wasm", `{"page":"Post"}`)

	if !strings.Contains(page, `<script src="/wasm_exec.js">`) {
		t0.Fatalf("SSR page must load wasm_exec.js by root-absolute URL (/wasm_exec.js):\n%s", page)
	}
	// The bare relative form must be gone (it breaks on any 2+ segment path).
	if strings.Contains(page, `<script src="wasm_exec.js">`) {
		t0.Fatalf("SSR page must NOT reference a bare relative wasm_exec.js:\n%s", page)
	}
	if !strings.Contains(page, `fetch("/main.abc123.wasm")`) {
		t0.Fatalf("SSR page must fetch the wasm by root-absolute URL:\n%s", page)
	}
	if strings.Contains(page, `fetch("main.abc123.wasm")`) {
		t0.Fatalf("SSR page must NOT fetch a bare relative wasm name:\n%s", page)
	}
}

// Seeded-boot navigation (register M, SPA-10): which of init's command, the
// pre-paint onNavigate and the post-mount onNavigate the client runs, from the
// seed decision and the page's `data-sky-settled` marker.
func TestSpaPlanBoot_SeededAndSettledSkipsTheSecondNavigation(t *testing.T) {
	cases := []struct {
		name                     string
		seeded                   bool
		settled                  string
		nav, twoStep             bool
		runInit, pre, afterMount bool
	}{
		// The reported defect: the server settled onNavigate into the seed.
		{"seeded, both settled", true, "init nav", true, false, false, false, false},
		{"seeded, nav settled only", true, "nav", true, false, true, false, false},
		// Anything the server did not finish still runs once on the client.
		{"seeded, nothing settled", true, "", true, false, true, false, true},
		{"seeded, init settled, nav not", true, "init", true, false, false, false, true},
		{"seeded, restored two-step, nav settled", true, "init nav", true, true, false, false, false},
		// Not seeded (no SSR, or the decode failed): today's behaviour.
		{"cold, no seed", false, "init nav", true, false, true, true, false},
		{"cold, two-step restore", false, "", true, true, true, false, true},
		{"no onNavigate hook", true, "init nav", false, false, false, false, false},
	}
	for _, c := range cases {
		p := spaPlanBoot(c.seeded, c.settled, c.nav, c.twoStep)
		if p.runInitCmd != c.runInit || p.prePaintNav != c.pre || p.navAfterMount != c.afterMount {
			t.Errorf("%s: got runInit=%v pre=%v afterMount=%v, want %v %v %v",
				c.name, p.runInitCmd, p.prePaintNav, p.navAfterMount, c.runInit, c.pre, c.afterMount)
		}
	}
}
