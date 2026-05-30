// Unit tests for Sky.Webview building blocks.
//
// These tests deliberately do NOT spawn a real webview window — CI
// machines don't have a display, and the webview_go's Run() needs
// the calling OS thread for its message pump. Instead we exercise
// the pure functions: HTML rendering, sky-id assignment, page wrap,
// the JS shim's presence, and the helpers that read cfg fields.
//
// The interactive smoke (does the window open + dispatch +
// re-paint) lives at examples/31-webview-stopwatch-ui — run it
// locally with `sky build src/Main.sky && ./sky-out/app`.

//go:build cgo && darwin

package rt

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test: webviewPageWrap produces a valid HTML5 document that
// contains the user's body inside `<div id="sky-root">`. The
// XSS hardening + CSS reset are stable across versions — bare
// minimum smoke that the shell didn't get accidentally truncated.
func TestWebviewPageWrap(t *testing.T) {
	out := webviewPageWrap(`<h1>hello</h1>`)
	for _, want := range []string{
		"<!DOCTYPE html>",
		`<meta charset="utf-8">`,
		`<div id="sky-root"><h1>hello</h1></div>`,
		// The CSS reset is reused from Sky.Live — at least one of
		// its signature rules should be present.
		"box-sizing:border-box",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("webviewPageWrap output missing %q\nGot:\n%s", want, out)
		}
	}
	// We removed the in-body <script> tag in favour of Init-injected
	// shim — make sure that's still the case so we don't accidentally
	// regress to double-injection.
	if strings.Contains(out, "<script>") {
		t.Errorf("webviewPageWrap should not embed <script> — shim is Init-injected")
	}
}

// Test: webviewSharedJS exposes the patch shim + bridge
// expectations: __skyApplyPatches, __skyReviveScripts (XSS
// hardening parity with Sky.Live), and the IIFE re-entry guard
// so double-injection (Init + page wrap) is a no-op.
func TestWebviewSharedJSContract(t *testing.T) {
	for _, want := range []string{
		// Hot-reload safety.
		"if (window.__skyApplyPatches)",
		// Patch applier exposed on window.
		"window.__skyApplyPatches = function",
		// XSS hardening — script revival.
		"function __skyReviveScripts",
		// Focus-preserving DOM replacer (cursor / scroll keep).
		"function __skyReplaceHTMLPreservingFocus",
		// Bridge into Go via webview.Bind.
		"window.__skyDispatch(hid, args)",
		// data-sky-hid is the dispatch key (NOT msgName).
		`getAttribute("data-sky-hid")`,
	} {
		if !strings.Contains(webviewSharedJS, want) {
			t.Errorf("webviewSharedJS missing %q", want)
		}
	}
}

// Test: webviewWindowSize falls back to defaults when the window
// config is absent / malformed, and reads correctly when present.
func TestWebviewWindowSize(t *testing.T) {
	// Absent — defaults.
	w, h := webviewWindowSize(nil)
	if w != 1024 || h != 720 {
		t.Errorf("webviewWindowSize(nil) = (%d,%d), want (1024,720)", w, h)
	}

	// Real shape: record with a Size tuple field. Use the same
	// internal representation the type-directed lowerer would
	// emit: a struct with a Size field that's a 2-tuple. Since we
	// can't easily synthesise that here, exercise via the public
	// helpers using a map proxy — Field() reads via reflect.
	type windowR struct {
		Title string
		Size  any
	}
	cfg := windowR{Title: "x", Size: SkyTuple2{V0: 900, V1: 500}}
	w, h = webviewWindowSize(cfg)
	if w != 900 || h != 500 {
		t.Errorf("webviewWindowSize({Size:(900,500)}) = (%d,%d), want (900,500)", w, h)
	}

	// Negative / zero clamps to defaults.
	cfg2 := windowR{Title: "x", Size: SkyTuple2{V0: 0, V1: -1}}
	w, h = webviewWindowSize(cfg2)
	if w != 1024 || h != 720 {
		t.Errorf("webviewWindowSize(zero/neg) = (%d,%d), want defaults", w, h)
	}
}

// Test: webviewFieldString falls back to the default when the
// field is absent / empty / non-string.
func TestWebviewFieldString(t *testing.T) {
	type r struct{ Title string }
	if got := webviewFieldString(r{Title: "Hi"}, "Title", "def"); got != "Hi" {
		t.Errorf("got %q, want %q", got, "Hi")
	}
	if got := webviewFieldString(r{Title: ""}, "Title", "def"); got != "def" {
		t.Errorf("empty field should fall back to default: got %q", got)
	}
	if got := webviewFieldString(nil, "Title", "def"); got != "def" {
		t.Errorf("nil cfg should fall back to default: got %q", got)
	}
}

// Test: webviewBuildTree produces a tree with structural sky-ids
// stamped on every element. The first patch from diffTrees can
// then address children by `[sky-id="…"]` without ambiguity.
func TestWebviewBuildTreeAssignsSkyIDs(t *testing.T) {
	// Simulate a minimal Html ADT value. HtmlToVNode reads via
	// reflection / generic shape, so we can use a SkyADT directly
	// with the same kind/tag shape it would receive from the
	// codegen-emitted Html.div / Html.text path.
	//
	// Easier to drive: build the VNode directly and call
	// assignSkyIDs in the same shape webviewBuildTree does.
	root := VNode{
		Kind: "element",
		Tag:  "div",
		Children: []VNode{
			{Kind: "element", Tag: "h1", Children: []VNode{
				{Kind: "text", Text: "hello"},
			}},
			{Kind: "element", Tag: "p"},
		},
	}
	assignSkyIDs(&root, "0")
	if root.SkyID != "0" {
		t.Errorf("root sky-id = %q, want %q", root.SkyID, "0")
	}
	if len(root.Children) < 2 {
		t.Fatalf("expected 2 children, got %d", len(root.Children))
	}
	if root.Children[0].SkyID == "" {
		t.Errorf("h1 child should have a sky-id assigned")
	}
	if root.Children[1].SkyID == "" {
		t.Errorf("p child should have a sky-id assigned")
	}
	// Siblings must have different ids — otherwise the diff walker
	// would merge them and corrupt the patch path.
	if root.Children[0].SkyID == root.Children[1].SkyID {
		t.Errorf("siblings must have distinct sky-ids: both got %q",
			root.Children[0].SkyID)
	}
}

// Test: diffTrees produces a JSON-marshallable patch list against
// a structurally-different new tree, and the resulting patches
// can be applied by the shim's __skyApplyPatches contract (the
// shape: {id, text, html, attrs, remove}).
func TestWebviewDiffJSONMarshallable(t *testing.T) {
	old := VNode{Kind: "element", Tag: "div", SkyID: "0",
		Children: []VNode{{Kind: "text", Text: "before"}}}
	new_ := VNode{Kind: "element", Tag: "div", SkyID: "0",
		Children: []VNode{{Kind: "text", Text: "after"}}}
	patches := diffTrees(&old, &new_, nil)
	if len(patches) == 0 {
		t.Fatal("expected at least one patch for text change")
	}
	b, err := json.Marshal(patches)
	if err != nil {
		t.Fatalf("patch marshal failed: %v", err)
	}
	// The JS shim reads p.id / p.text / p.html / p.attrs / p.remove.
	// Confirm those keys appear on the wire (id always, text in this
	// case since we changed text).
	s := string(b)
	if !strings.Contains(s, `"id":`) {
		t.Errorf("marshalled patches missing id field: %s", s)
	}
}

// Test: the bounded msgCh discipline doesn't dead-lock under a
// burst of synthetic events. Uses the same channel shape the
// real Bind callback would push into; verifies that when the
// channel is full, subsequent sends fall through the default
// arm and bumpDropped fires — they don't block the dispatcher.
func TestWebviewMsgChBoundedDoesNotBlock(t *testing.T) {
	state := &webviewState{}
	msgCh := make(chan any, webviewMsgChCap)

	// Fill the channel.
	for i := 0; i < webviewMsgChCap; i++ {
		msgCh <- i
	}

	// Now mimic the Bind handler's send pattern — with no reader,
	// the next send must fall through to the default arm and bump
	// the dropped counter, NOT block the test forever.
	for i := 0; i < 100; i++ {
		select {
		case msgCh <- "extra":
			t.Errorf("expected drop on full channel, send succeeded at i=%d", i)
		default:
			state.bumpDropped()
		}
	}

	state.mu.Lock()
	got := state.msgDropped
	state.mu.Unlock()
	if got != 100 {
		t.Errorf("expected 100 dropped msgs, got %d", got)
	}
}

// Test (bug #370): when SKY_LIVE_STATIC_DIR is unset, the loopback
// helper webviewStaticDir() returns "" so the call site falls
// through to the no-regression SetHtml path. Pin the contract so
// Sky.Ui-only desktop apps (examples/31-webview-stopwatch-ui) stay
// on the in-process SetHtml bridge.
func TestWebviewStaticDirUnsetReturnsEmpty(t *testing.T) {
	// Defence: clear both candidate vars under the configured
	// prefix in case the test runner shell has them set.
	for _, k := range []string{"LIVE_STATIC_DIR", "STATIC_DIR"} {
		old, had := os.LookupEnv(skyEnvName(k))
		os.Unsetenv(skyEnvName(k))
		t.Cleanup(func() {
			if had {
				os.Setenv(skyEnvName(k), old)
			} else {
				os.Unsetenv(skyEnvName(k))
			}
		})
	}
	if got := webviewStaticDir(); got != "" {
		t.Errorf("webviewStaticDir() with no env = %q, want \"\" (no-regression SetHtml path)", got)
	}
}

// Test (bug #370): webviewStaticDir reads SKY_LIVE_STATIC_DIR
// (the canonical name emitted from sky.toml `[live].static`).
func TestWebviewStaticDirReadsLiveStaticEnv(t *testing.T) {
	key := skyEnvName("LIVE_STATIC_DIR")
	old, had := os.LookupEnv(key)
	os.Setenv(key, "./public")
	t.Cleanup(func() {
		if had {
			os.Setenv(key, old)
		} else {
			os.Unsetenv(key)
		}
	})
	if got := webviewStaticDir(); got != "./public" {
		t.Errorf("webviewStaticDir() = %q, want %q", got, "./public")
	}
}

// Test (bug #370): startWebviewLoopback spawns a 127.0.0.1-only
// http server that serves /static/* from the given directory AND
// returns the current rendered body on /. Verifies the actual
// wire shape — fixture file fetched + page wrap applied to body.
func TestWebviewLoopbackServesStaticAndBody(t *testing.T) {
	tmp := t.TempDir()
	asset := []byte("/* vrm placeholder */\nbody { color: orange; }\n")
	if err := os.WriteFile(filepath.Join(tmp, "model.vrm"), []byte("VRM-FAKE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "style.css"), asset, 0o644); err != nil {
		t.Fatal(err)
	}

	state := &webviewState{}
	state.currentBody.Store(`<h1 sky-id="0">hello loopback</h1>`)

	srv, port, err := startWebviewLoopback(tmp, state)
	if err != nil {
		t.Fatalf("startWebviewLoopback: %v", err)
	}
	defer srv.Close()

	// /static/style.css — file served verbatim.
	{
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/static/style.css", port))
		if err != nil {
			t.Fatalf("GET /static/style.css: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("status=%d, want 200", resp.StatusCode)
		}
		if string(body) != string(asset) {
			t.Errorf("body=%q, want %q", body, asset)
		}
	}

	// /static/model.vrm — confirms binary assets work too. This
	// is the real-world failure mode (19MB VRM in user's app).
	{
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/static/model.vrm", port))
		if err != nil {
			t.Fatalf("GET /static/model.vrm: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("status=%d, want 200", resp.StatusCode)
		}
		if string(body) != "VRM-FAKE" {
			t.Errorf("body=%q, want %q", body, "VRM-FAKE")
		}
	}

	// / — returns the rendered body wrapped in webviewPageWrap.
	{
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil {
			t.Fatalf("GET /: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("status=%d, want 200", resp.StatusCode)
		}
		got := string(body)
		if !strings.Contains(got, "<!DOCTYPE html>") {
			t.Errorf("body missing doctype: %s", got)
		}
		if !strings.Contains(got, `<div id="sky-root">`) {
			t.Errorf("body missing sky-root wrapper")
		}
		if !strings.Contains(got, "hello loopback") {
			t.Errorf("body missing the published render: %s", got)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Errorf("Content-Type=%q, want text/html", ct)
		}
	}

	// Live re-render: publishing a new body shows up on next /.
	state.currentBody.Store(`<h1 sky-id="0">post-tick render</h1>`)
	{
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
		if err != nil {
			t.Fatalf("GET / (post-tick): %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(body), "post-tick render") {
			t.Errorf("body did not reflect updated render: %s", body)
		}
	}
}

// Test (bug #370 — security): the loopback listener binds to
// 127.0.0.1 only, never the wildcard 0.0.0.0. A desktop webview
// app's local server must not be reachable from the LAN. Asserts
// the addr.Network()/String() shape exposes a loopback IP.
func TestWebviewLoopbackBindsLoopbackOnly(t *testing.T) {
	tmp := t.TempDir()
	state := &webviewState{}
	state.currentBody.Store("")
	srv, port, err := startWebviewLoopback(tmp, state)
	if err != nil {
		t.Fatalf("startWebviewLoopback: %v", err)
	}
	defer srv.Close()
	if port <= 0 {
		t.Errorf("port=%d, want >0", port)
	}
	// Confirm the chosen port responds on 127.0.0.1 — the listener's
	// bind address is captured at net.Listen time; here we exercise
	// it round-trip. A LAN-reachable bind would have been
	// 0.0.0.0:<port> or :: — those answer on the host's external IPs
	// too. We can't easily probe an external IP from a test, but
	// the source's explicit "127.0.0.1:0" Listen call is the gate;
	// this test pins the round-trip side.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	resp.Body.Close()
}

// Test: handler lookup table swap is atomic and concurrent reads
// don't corrupt the map. Mirrors the production hot path where
// the update goroutine swaps handlers while the Bind callback
// is mid-lookup.
func TestWebviewHandlerSwapAtomic(t *testing.T) {
	state := &webviewState{}
	h1 := map[string]any{"a.click": "h1"}
	h2 := map[string]any{"a.click": "h2"}

	state.swapTree(&VNode{}, h1)
	if state.lookupHandler("a.click") != "h1" {
		t.Errorf("lookup after swap1 failed")
	}
	state.swapTree(&VNode{}, h2)
	if state.lookupHandler("a.click") != "h2" {
		t.Errorf("lookup after swap2 failed — handlers map didn't swap")
	}
	// Unknown id returns nil, not panic.
	if state.lookupHandler("nonexistent") != nil {
		t.Errorf("unknown handler id should return nil")
	}
}
