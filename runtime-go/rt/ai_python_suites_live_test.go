//go:build ailive && !js

package rt

// LIVE: the Python suites of the Unsloth driver and the cloud shim
// (testdata/train_driver_tests) under the pinned CPython the runtime installs
// (ai_pins.go aiPythonAssets, checked by its SHA-256): the interpreter the
// Unsloth toolchain and the GCP trainer image run them in. Mlx and Fake
// train without Python, so no backend that runs everywhere installs this
// CPython any more; this test installs it itself.
//
//	cd runtime-go && go test -tags ailive ./rt -run TestTheDriverSuitesPassUnderThePinnedCPython
//
// It needs the network (github.com) or the CPython already in the Std.Ai
// home (SKY_AI_HOME, which CI caches on ai_pins.go). Without either it FAILS,
// unless SKY_LIVE_TESTS=skip. CI runs it in rust-ci.yml `ai-train-live` and
// release.yml `gate-ai-train`.

import (
	"context"
	"net"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTheDriverSuitesPassUnderThePinnedCPython(t *testing.T) {
	a, ok := aiHostTool(aiPythonAssets)
	if !ok {
		t.Fatalf("no pinned CPython for this host")
	}
	installed := fileExists(filepath.Join(aiHome(), "python", aiPythonTag+"-"+a.target, "bin", "python3"))
	reachable := false
	if c, err := net.DialTimeout("tcp", "github.com:443", 5*time.Second); err == nil {
		c.Close()
		reachable = true
	}
	requireLive(t, "the pinned CPython", "give the test network access to github.com, or set SKY_AI_HOME to a Std.Ai home that holds it", installed || reachable)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	py, err := aiEnsurePython(ctx, aiToolchainEnv(aiHome()))
	if err != nil {
		t.Fatal(err)
	}
	suites, _ := filepath.Abs(filepath.Join("testdata", "train_driver_tests"))
	cmd := exec.CommandContext(ctx, py, "-I", "-B", "-m", "unittest", "discover", "-s", suites)
	cmd.Dir = suites
	cmd.Env = aiToolchainEnv(aiHome())
	out, err := cmd.CombinedOutput()
	report := string(out)
	if err != nil || !strings.Contains(report, "\nOK") {
		t.Fatalf("the driver's Python suites under %s:\n%s", py, report)
	}
	if !strings.Contains(report, "Ran ") {
		t.Fatalf("no suite ran:\n%s", report)
	}
	t.Logf("%s", report[strings.LastIndex(report, "Ran "):])
}
