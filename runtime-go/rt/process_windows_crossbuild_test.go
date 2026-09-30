//go:build unix && !js

package rt

import (
	"os"
	"os/exec"
	"testing"
)

// TestProcessWindowsPathCompiles — the Windows implementation of
// Sky.Core.Process (process_nounix.go) and its tests (process_nounix_test.go)
// are compiled on every run of this suite by vetting the package for
// GOOS=windows. A Unix host never builds those files otherwise, so an edit
// that broke them (a renamed helper, a Unix-only syscall) would ship a
// Windows runtime that does not compile. The tests themselves run on a
// Windows host: a PTY spawn is `Err Unavailable`, only `Kill` is delivered.
func TestProcessWindowsPathCompiles(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the go toolchain is required to cross-check the Windows build: %v", err)
	}
	cmd := exec.Command(gobin, "vet", ".")
	cmd.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64", "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("GOOS=windows go vet of the runtime failed: %v\n%s", err, out)
	}
}
