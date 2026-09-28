//go:build !js

package rt

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// The runtime must compile for every target a Sky app is built for, not only
// the host: Windows (the compiler ships for Windows, so `sky build` there
// compiles this package for Windows) and the Sky.Spa wasm client. Before
// v0.27.0 the Windows build was broken by Unix-only calls in the embedded
// PostgreSQL supervisor and the Sky.Tui resize watcher, and nothing noticed:
// no gate compiled the runtime for Windows. Platform-specific code lives in
// build-tagged files (process_unix.go / process_nounix.go and friends); this
// test compiles the package for each target so a new Unix-only call in a
// shared file goes red here.
func TestRuntimeCompilesForEveryTarget(t *testing.T) {
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		p, lerr := exec.LookPath("go")
		if lerr != nil {
			t.Fatalf("no go toolchain to cross-compile with: %v", lerr)
		}
		goBin = p
	}
	targets := []struct{ goos, goarch string }{
		{"windows", "amd64"},
		{"js", "wasm"},
		{"linux", "amd64"},
		{"darwin", "arm64"},
		{"freebsd", "amd64"},
	}
	for _, tg := range targets {
		tg := tg
		t.Run(tg.goos+"_"+tg.goarch, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(ctx, goBin, "build", "-o", os.DevNull, ".")
			cmd.Env = append(os.Environ(), "GOOS="+tg.goos, "GOARCH="+tg.goarch, "CGO_ENABLED=0")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("the runtime does not compile for %s/%s: %v\n%s", tg.goos, tg.goarch, err, out)
			}
		})
	}
}
