//go:build !js

package rt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Std.Native.insertAtCursor, run for real in Go's js/wasm build under Node.js
// (rt/nativecursorwasm) against a stand-in DOM: an insert at a middle caret,
// a replaced selection, the remembered selection of a control that lost focus
// to a menu button (and is not moved by a caret drift the person did not
// make), an unknown id, a non-text element and no document. Off the client
// the kernel is Err Unavailable (TestNativeInsertAtCursorIsUnavailableOffClient).
func TestNativeInsertAtCursorInWasm(t *testing.T) {
	node := requireNode(t)
	goroot := runtime.GOROOT()
	execJS := filepath.Join(goroot, "lib", "wasm", "wasm_exec_node.js")
	if _, err := os.Stat(execJS); err != nil {
		t.Fatalf("the Go toolchain has no lib/wasm/wasm_exec_node.js: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "nativecursor.test.wasm")
	build := exec.CommandContext(ctx, filepath.Join(goroot, "bin", "go"), "test", "-c", "-o", bin, "./nativecursorwasm/")
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the wasm test failed: %v\n%s", err, out)
	}
	// wasm_exec.js refuses a large command line plus environment: give node
	// only its PATH.
	cmd := exec.CommandContext(ctx, node, execJS, bin, "-test.count=1", "-test.v")
	cmd.Env = []string{"PATH=" + filepath.Dir(node)}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the wasm test failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "--- PASS: TestWasmInsertAtCursor") {
		t.Fatalf("TestWasmInsertAtCursor did not pass:\n%s", out)
	}
	if n := strings.Count(string(out), "    --- PASS: TestWasmInsertAtCursor/"); n != 7 {
		t.Fatalf("want 7 passing subtests, got %d:\n%s", n, out)
	}
	// Std.Native.selection / setSelection (native_selection_wasm.go), in the
	// same build: the focused and the remembered selection, character
	// positions over UTF-16, clamping, and the three error kinds.
	if !strings.Contains(string(out), "--- PASS: TestWasmSelection") {
		t.Fatalf("TestWasmSelection did not pass:\n%s", out)
	}
	if n := strings.Count(string(out), "    --- PASS: TestWasmSelection/"); n != 8 {
		t.Fatalf("want 8 passing selection subtests, got %d:\n%s", n, out)
	}
}

// Off the client (a server, a CLI, a terminal UI, and Sky.Live, whose update
// runs on the server) there is no DOM: Err Unavailable, never a panic.
func TestNativeInsertAtCursorIsUnavailableOffClient(t *testing.T) {
	thunk, ok := Native_insertAtCursor("body", "x").(func() any)
	if !ok {
		t.Fatalf("Native_insertAtCursor must return a Task thunk")
	}
	res, ok := thunk().(SkyResult[any, any])
	if !ok || res.Tag != 1 {
		t.Fatalf("off-client insertAtCursor must be Err, got %#v", thunk())
	}
	if msg := extractErrMsg(res); !strings.Contains(msg, "client-only") {
		t.Errorf("the Err must say it is client-only, got %q", msg)
	}
	if kind := errorKindName(res.ErrValue); kind != "Unavailable" {
		t.Errorf("want Err Unavailable, got %q", kind)
	}
}

// Native.selection / setSelection off the client: Err Unavailable, never a
// panic, like insertAtCursor.
func TestNativeSelectionIsUnavailableOffClient(t *testing.T) {
	for name, task := range map[string]any{
		"selection":    Native_selection("body"),
		"setSelection": Native_setSelection("body", 0, 1),
	} {
		thunk, ok := task.(func() any)
		if !ok {
			t.Fatalf("%s must return a Task thunk", name)
		}
		res, ok := thunk().(SkyResult[any, any])
		if !ok || res.Tag != 1 {
			t.Fatalf("off-client %s must be Err, got %#v", name, thunk())
		}
		if msg := extractErrMsg(res); !strings.Contains(msg, "client-only") {
			t.Errorf("%s: the Err must say it is client-only, got %q", name, msg)
		}
		if kind := errorKindName(res.ErrValue); kind != "Unavailable" {
			t.Errorf("%s: want Err Unavailable, got %q", name, kind)
		}
	}
}
