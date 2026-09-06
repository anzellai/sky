//go:build !js

package rt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The backend SSR render kernels (spa_ssr_notjs.go). These are what the
// generated Sky.Spa backend route calls to turn a route-resolved model into the
// first-paint HTML document. Design: docs/skyspa/ssr-design.md §4.1 / §4.3.

// A <title> head node carrying one text child — the shape `withHead` yields.
func ssrTitleNode(text string) VNode {
	return VNode{
		Kind:     "element",
		Tag:      "title",
		Children: []VNode{{Kind: "text", Text: text}},
	}
}

func TestSpaSSRRenderBody_EmitsHydrationContract(t *testing.T) {
	// A view root: <div><button onclick>Go</button></div>. Rendering it must
	// stamp sky-ids (the client hydrates against them) and the real tags/text.
	view := VNode{
		Kind: "element",
		Tag:  "div",
		Children: []VNode{{
			Kind:     "element",
			Tag:      "button",
			Events:   map[string]any{"click": "Go"},
			Children: []VNode{{Kind: "text", Text: "Go"}},
		}},
	}
	body := Spa_ssrRenderBody(view)
	for _, want := range []string{"<div", "<button", "sky-id=", ">Go<"} {
		if !strings.Contains(body, want) {
			t.Fatalf("Spa_ssrRenderBody body missing %q:\n%s", want, body)
		}
	}
	if strings.TrimSpace(body) == "" {
		t.Fatal("Spa_ssrRenderBody produced an EMPTY body — a crawler would see nothing")
	}
}

func TestRenderSpaHead_RunsBuilderAndSerialises(t *testing.T) {
	head := func(m any) any {
		return []any{ssrTitleNode("Per-Route Title")}
	}
	got := RenderSpaHead(head, struct{}{})
	if !strings.Contains(got, "<title>Per-Route Title</title>") {
		t.Fatalf("RenderSpaHead did not render the per-route <title>: %q", got)
	}
	// The kernel alias must delegate to the same path.
	if Spa_ssrRenderHead(head, struct{}{}) != got {
		t.Fatal("Spa_ssrRenderHead must match RenderSpaHead byte-for-byte")
	}
}

func TestRenderSpaHead_NilHeadIsEmpty(t *testing.T) {
	if got := RenderSpaHead(nil, struct{}{}); got != "" {
		t.Fatalf("RenderSpaHead(nil) must be empty (a page with no withHead), got %q", got)
	}
}

func TestSpaSSRWasmName_ResolvesHashedThenFallsBack(t *testing.T) {
	dir := t.TempDir()
	// Fallback when no wasm present.
	if got := Spa_ssrWasmName(dir); got != "main.wasm" {
		t.Fatalf("empty dist must fall back to main.wasm, got %q", got)
	}
	// A missing directory also falls back, never panics or empties the loader.
	if got := Spa_ssrWasmName(filepath.Join(dir, "does-not-exist")); got != "main.wasm" {
		t.Fatalf("unreadable dist must fall back to main.wasm, got %q", got)
	}
	// The content-hashed wasm wins when present.
	if err := os.WriteFile(filepath.Join(dir, "wasm_exec.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.abc123def456.wasm"), []byte("w"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Spa_ssrWasmName(dir); got != "main.abc123def456.wasm" {
		t.Fatalf("must resolve the hashed wasm, got %q", got)
	}
}

func TestSpaSSRPage_EndToEndDocument(t *testing.T) {
	head := RenderSpaHead(func(m any) any { return []any{ssrTitleNode("Home")} }, struct{}{})
	body := Spa_ssrRenderBody(VNode{Kind: "element", Tag: "main", Children: []VNode{{Kind: "text", Text: "hello"}}})
	page := Spa_ssrPage(head, body, "main.deadbeef0000.wasm", "")
	for _, want := range []string{
		"<title>Home</title>",         // per-route head
		"data-sky-ssr",                // hydrate marker on #app
		`id="app"`,                    // mount id the client keys off
		"hello",                       // real server-rendered body content
		"main.deadbeef0000.wasm",      // content-hashed wasm loader
		"instantiateStreaming",        // the boot script
	} {
		if !strings.Contains(page, want) {
			t.Fatalf("SpaSSRPage missing %q:\n%s", want, page)
		}
	}
	// The body must sit INSIDE #app, not leave it empty (the BUG-1 regression).
	if strings.Contains(page, `id="app" data-sky-ssr="1"></div>`) {
		t.Fatal("SpaSSRPage left #app EMPTY — SSR body was not spliced in")
	}
}
