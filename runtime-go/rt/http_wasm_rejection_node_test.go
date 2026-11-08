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

// Every child imports the actual production runtime. The public fake fetch
// uses no network/server/env fixture. Separate processes expose every reason
// even when the old formatter fatally panics in the first callback.
func TestWasmFetchRejectionValues(t *testing.T) {
	node := requireNode(t)
	goroot := runtime.GOROOT()
	loader := filepath.Join(goroot, "lib", "wasm", "wasm_exec_node.js")
	if _, err := os.Stat(loader); err != nil {
		t.Fatalf("the pinned Go toolchain has no Wasm Node loader: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "fetch-rejections.test.wasm")
	build := exec.CommandContext(ctx, filepath.Join(goroot, "bin", "go"), "test", "-c", "-o", bin, "./noisewasm/")
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the actual fetch rejection test failed: %v\n%s", err, out)
	}
	cases := []string{
		"rejection-error", "rejection-string", "rejection-number", "rejection-boolean",
		"rejection-message-getter", "rejection-to-string", "rejection-no-to-string", "rejection-noncallable-to-string",
	}
	started, passed := 0, 0
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			childCtx, childCancel := context.WithTimeout(ctx, time.Minute)
			defer childCancel()
			selector := "^TestWasmFetchRejectionFormatting$/^" + name + "$"
			cmd := exec.CommandContext(childCtx, node, loader, bin, "-test.run="+selector, "-test.count=1", "-test.v")
			cmd.Env = []string{"PATH=" + filepath.Dir(node)}
			out, err := cmd.CombinedOutput()
			if cmd.Process != nil {
				started++
			}
			if err != nil {
				t.Fatalf("actual rejection case failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "--- PASS: TestWasmFetchRejectionFormatting/"+name+" ") {
				t.Fatalf("the actual rejection case did not execute and pass:\n%s", out)
			}
			passed++
		})
	}
	if started != len(cases) || passed != len(cases) {
		t.Errorf("actual public rejection Node processes: started=%d passed=%d required=%d", started, passed, len(cases))
	}
}
