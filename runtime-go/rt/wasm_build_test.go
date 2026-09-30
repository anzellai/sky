//go:build !js

package rt

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// The runtime must compile for the Sky.Spa wasm client (GOOS=js). An untagged
// file that calls a server-only (`//go:build !js`) function breaks every
// `--target web:app` build, and nothing else notices: the native build and
// the native tests stay green. Three such files reached the v0.27.0 merge
// (lazycaf.go, auth_verify_json.go, server_add_cookie.go).
func TestRuntimeBuildsForWasm(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: the wasm build takes a few seconds")
	}
	gobin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(gobin); err != nil {
		gobin = "go"
	}
	cmd := exec.Command(gobin, "build", "-o", os.DevNull, ".")
	cmd.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("GOOS=js GOARCH=wasm go build of the runtime failed: %v\n%s", err, out)
	}
}
