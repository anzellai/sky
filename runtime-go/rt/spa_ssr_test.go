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
