//go:build !js

package rt

import (
	"os"
	"testing"
)

// A process started with a stripped PATH (a systemd unit with
// `Environment=PATH=`, a launchd job, an exec from a supervisor that clears the
// environment) must still find the system tools the runtime runs. They are
// looked up at their absolute paths first, and on PATH only after that.

// TestPsIsFoundWithAnEmptyPath: the embedded-PostgreSQL liveness probe asks
// `ps` what a pid is. With PATH empty, a bare `ps` is not found and a running
// postmaster reads as "not PostgreSQL".
func TestPsIsFoundWithAnEmptyPath(t *testing.T) {
	t.Setenv("PATH", "")
	cmd, ok := processCommand(os.Getpid())
	if !ok || cmd == "" {
		t.Fatalf("processCommand(self) with PATH=\"\" = (%q, %v), want this test binary's command line", cmd, ok)
	}
}

// TestSystemToolPrefersAnAbsolutePath: an absolute candidate that exists wins
// over a PATH lookup, and a name found nowhere is returned unchanged so the
// exec reports the real error.
func TestSystemToolPrefersAnAbsolutePath(t *testing.T) {
	t.Setenv("PATH", "")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got := systemTool("not-a-real-tool", "/no/such/dir/not-a-real-tool", self); got != self {
		t.Fatalf("systemTool picked %q, want the first existing candidate %q", got, self)
	}
	if got := systemTool("not-a-real-tool", "/no/such/dir/not-a-real-tool"); got != "not-a-real-tool" {
		t.Fatalf("systemTool with no candidate found = %q, want the bare name", got)
	}
}
