package rt

import (
	"strings"
	"testing"
)

// The Sky.Spa SSR + hydration exit-criterion (design docs/skyspa/ssr-design.md
// §4.4 / §8 risk 1). The `sky-id`-presence check is NOT sufficient: three
// server/client DOM divergences line up by sky-id yet corrupt on the first
// structural diff — `raw` nodes (server inline vs client <span>-wrap), adjacent
// text children (server concatenates → one browser text node vs client keeps N),
// and `textarea` (server splices value as child text vs client models a
// property). `spaHydratableVNode` is the POSITIVE structural check a hydrate
// walk consults to fall back to a full `spaMount` rebuild instead of adopting a
// mismatched tree. These tests pin that it flags each divergence and passes a
// clean tree.

// `el(tag, attrs, children...)` + `txt(s)` are shared test helpers from
// live_skyid_test.go. `rawNode` is local to the SSR parity tests.
func rawNode(s string) VNode { return VNode{Kind: "raw", Text: s} }

func TestSpaHydratable_cleanTreeIsHydratable(t0 *testing.T) {
	// A plain element tree with single text children and no raw/textarea is
	// byte-structurally identical server vs client — hydratable.
	vn := el("div", nil,
		el("h1", nil, txt("Title")),
		el("p", nil, txt("one paragraph")),
		el("button", nil, txt("click")),
	)
	ok, reason := spaHydratableVNode(vn)
	if !ok {
		t0.Fatalf("a clean element/single-text tree must be hydratable, got reason %q", reason)
	}
}

func TestSpaHydratable_rawNodeIsNotHydratable(t0 *testing.T) {
	// Server writes raw n.Text inline (live_core.go:444); client buildDOM wraps
	// it in a <span> (dom_render_wasm.go:74-78) → structural divergence.
	vn := el("div", nil, rawNode("<b>markup</b>"))
	ok, reason := spaHydratableVNode(vn)
	if ok {
		t0.Fatalf("a raw node must NOT be hydratable (server inline vs client <span>-wrap)")
	}
	if !strings.Contains(reason, "raw") {
		t0.Fatalf("reason must name the raw divergence, got %q", reason)
	}
}

func TestSpaHydratable_adjacentTextIsNotHydratable(t0 *testing.T) {
	// Server concatenates adjacent escaped text into one run (one browser text
	// node); client keeps them as separate VNode children → node-count mismatch.
	vn := el("p", nil, txt("Hello, "), txt("world"))
	ok, reason := spaHydratableVNode(vn)
	if ok {
		t0.Fatalf("adjacent text children must NOT be hydratable (server coalesces, client keeps N)")
	}
	if !strings.Contains(reason, "adjacent") {
		t0.Fatalf("reason must name the adjacent-text divergence, got %q", reason)
	}
}

func TestSpaHydratable_textareaValueIsNotHydratable(t0 *testing.T) {
	// Server splices <textarea> value as child text; client models it as the DOM
	// .value property → divergence.
	vn := VNode{Kind: "element", Tag: "textarea", Attrs: map[string]string{"value": "draft"}}
	ok, reason := spaHydratableVNode(vn)
	if ok {
		t0.Fatalf("a textarea with a value must NOT be hydratable (server child-text vs client property)")
	}
	if !strings.Contains(reason, "textarea") {
		t0.Fatalf("reason must name the textarea divergence, got %q", reason)
	}
}

func TestSpaHydratable_emptyTextareaIsHydratable(t0 *testing.T) {
	// A textarea with NO value has no child-text splice on the server, so it is
	// hydratable — the divergence is specifically the spliced value.
	vn := el("form", nil, VNode{Kind: "element", Tag: "textarea"})
	ok, reason := spaHydratableVNode(vn)
	if !ok {
		t0.Fatalf("an empty textarea must be hydratable, got reason %q", reason)
	}
}

func TestSpaHydratable_divergenceDeepInTreeIsCaught(t0 *testing.T) {
	// The check must recurse — a raw node nested several levels down still makes
	// the whole tree non-hydratable (a subtree diff would corrupt).
	vn := el("div", nil, el("section", nil, el("article", nil, rawNode("<i>x</i>"))))
	ok, _ := spaHydratableVNode(vn)
	if ok {
		t0.Fatalf("a raw node deep in the tree must make the tree non-hydratable")
	}
}

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
