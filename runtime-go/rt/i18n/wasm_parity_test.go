//go:build sky_cldr_all && !js

package i18n

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

// TestWasmFormatsLikeNative builds this package for js/wasm (what a Sky.Spa
// client links), runs the corpus under Node, and compares every line with
// the native run. A server render and the client's hydration must agree,
// so any difference is a failure.
func TestWasmFormatsLikeNative(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("SKY_LIVE_TESTS") == "skip" {
			t.Skip("SKY_LIVE_TESTS=skip: no node")
		}
		t.Fatalf("Node.js (`node` on PATH) is required: install Node.js 18 or newer; this test runs the js/wasm build under node (SKY_LIVE_TESTS=skip skips it)")
	}
	goroot := runtime.GOROOT()
	loader := filepath.Join(goroot, "lib", "wasm", "wasm_exec_node.js")
	if _, err := os.Stat(loader); err != nil {
		t.Fatalf("the Go toolchain has no Wasm Node loader: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "i18n.test.wasm")
	build := exec.CommandContext(ctx, filepath.Join(goroot, "bin", "go"), "test", "-c", "-tags", "sky_cldr_all", "-o", bin, ".")
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the js/wasm test binary failed: %v\n%s", err, out)
	}
	cmd := exec.CommandContext(ctx, node, loader, bin, "-test.run=^TestParityCorpus$", "-test.count=1")
	cmd.Env = []string{"PATH=" + filepath.Dir(node)}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the js/wasm corpus run failed: %v\n%s", err, out)
	}
	var wasm []string
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "PARITY\t"); ok {
			wasm = append(wasm, rest)
		}
	}
	native := parityCorpus()
	for i := range native {
		native[i] = strings.ReplaceAll(native[i], "\n", "\\n")
	}
	if len(wasm) != len(native) {
		t.Fatalf("js/wasm gave %d lines, native %d", len(wasm), len(native))
	}
	n := 0
	for i := range native {
		if strings.HasPrefix(native[i], "ERR") || strings.Contains(native[i], "\tERR ") {
			t.Errorf("native corpus line has an error: %s", native[i])
		}
		if wasm[i] != native[i] {
			t.Errorf("line %d differs:\n  native %q\n  wasm   %q", i, native[i], wasm[i])
		}
		n++
	}
	t.Logf("ASSERTIONS: %d", n)
}
