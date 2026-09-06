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

// SSR-P3 per-route resolution (design §4.1). Spa_ssrResolveModel must fold the
// route matched by the request path into the model's Page field, exactly as the
// wasm client does at boot — so the server renders each route's OWN content.
func TestSpaSSRResolveModel_SetsPagePerRoute(t *testing.T) {
	routes := []any{Spa_route("/", "HomePage"), Spa_route("/items", "ItemsPage")}
	notFound := any("NotFoundPage")
	model := map[string]any{"Page": "HomePage", "items": []any{}}

	// A matching path folds that route's page into the model.
	got := Spa_ssrResolveModel(routes, notFound, model, "/items")
	if Field(got, "Page") != "ItemsPage" {
		t.Fatalf("expected Page=ItemsPage for /items, got %v", Field(got, "Page"))
	}
	// The root resolves to its own page.
	root := Spa_ssrResolveModel(routes, notFound, model, "/")
	if Field(root, "Page") != "HomePage" {
		t.Fatalf("expected Page=HomePage for /, got %v", Field(root, "Page"))
	}
	// An unmatched path falls back to the notFound page.
	miss := Spa_ssrResolveModel(routes, notFound, model, "/nope")
	if Field(miss, "Page") != "NotFoundPage" {
		t.Fatalf("expected Page=NotFoundPage for an unmatched path, got %v", Field(miss, "Page"))
	}
}

// SSR-P3 data-resolved settle (design §4.2). Spa_ssrSettle must run the perform
// command's task, map its Result to a Msg, and fold it through `update` — so the
// server renders a data-BEARING model. This is what makes a route's real content
// crawlable. The update here mimics a `GotItems (Ok raw)` fold that splits the
// read text into the model's items.
func TestSpaSSRSettle_FoldsAGetSafeReadIntoTheModel(t *testing.T) {
	model := map[string]any{"Page": "ItemsPage", "items": []any{}}

	// cmd0 = Cmd.perform (read task) toMsg — the read returns Ok "a\nb\nc".
	task := func() any { return Ok[any, any]("a\nb\nc") }
	toMsg := func(result any) any { return result } // identity: msg == the Result
	cmd := cmdT{kind: "perform", task: task, toMsg: toMsg}

	// update : msg -> model -> ( model, Cmd ). On an Ok result, split into items.
	update := func(msg any, m any) any {
		res, _ := msg.(SkyResult[any, any])
		items := []any{}
		if res.Tag == 0 {
			for _, line := range strings.Split(AsString(res.OkValue), "\n") {
				items = append(items, line)
			}
		}
		next := RecordUpdate(m, map[string]any{"items": items})
		return SkyTuple2{V0: next, V1: Cmd_none()}
	}

	settled := Spa_ssrSettle(model, cmd, update)
	items, ok := Field(settled, "items").([]any)
	if !ok || len(items) != 3 || items[0] != "a" || items[2] != "c" {
		t.Fatalf("expected the read to settle into items=[a b c], got %v", Field(settled, "items"))
	}
	// The unrelated Page field must survive the fold.
	if Field(settled, "Page") != "ItemsPage" {
		t.Fatalf("settle must preserve unrelated fields; Page=%v", Field(settled, "Page"))
	}
}

// A batch of performs settles each leaf once.
func TestSpaSSRSettle_BatchFoldsEachPerform(t *testing.T) {
	model := map[string]any{"a": "", "b": ""}
	mk := func(val, field string) cmdT {
		return cmdT{
			kind:  "perform",
			task:  func() any { return Ok[any, any](val) },
			toMsg: func(r any) any { return map[string]any{"field": field, "val": r} },
		}
	}
	update := func(msg any, m any) any {
		mm := msg.(map[string]any)
		res := mm["val"].(SkyResult[any, any])
		next := RecordUpdate(m, map[string]any{mm["field"].(string): res.OkValue})
		return SkyTuple2{V0: next, V1: Cmd_none()}
	}
	cmd := cmdT{kind: "batch", batch: []any{mk("A", "a"), mk("B", "b")}}
	settled := Spa_ssrSettle(model, cmd, update)
	if Field(settled, "a") != "A" || Field(settled, "b") != "B" {
		t.Fatalf("batch settle must fold each perform; got a=%v b=%v", Field(settled, "a"), Field(settled, "b"))
	}
}
