//go:build !js

package rt

import (
	"context"
	"encoding/gob"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type adtGobBootModel struct{ Value any }

// This must use a separately compiled PRODUCTION-only executable, rather than
// re-executing this test binary: console_inline_test imports console_app, whose
// boot type graphs happen to register SkyADT and can mask this exact bug.
func TestSkyADTBootRegistrationInProductionOnlyProcess(t *testing.T) {
	model := adtGobBootModel{Value: SkyADT{
		Tag: 37, SkyName: "UserCarrier",
		Fields: []any{
			"keep-original-text", int64(7), true, []any{"tail", int(-3)},
			SkyADT{Tag: 9, SkyName: "NestedUser", Fields: []any{"inner"}},
		},
	}}
	// This is encodeSession's actual production dynamic registration path,
	// not a hand-written gob.Register call or a child-side test registration.
	// The outer concrete struct decodes structurally into the child's blank
	// model; only its any slot needs the runtime carrier's global wire name.
	gobRegisterAll(model)
	path := filepath.Join(t.TempDir(), "carrier.gob")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encodeErr := gob.NewEncoder(file).Encode(model)
	closeErr := file.Close()
	if encodeErr != nil {
		t.Fatalf("production value-walked fixture encode failed: %v", encodeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// Use this binary's Go toolchain, independent of wrapper scripts on PATH.
	// Serial package compilation bounds this small probe's compiler fan-out.
	goTool := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goTool += ".exe"
	}
	command := exec.CommandContext(ctx, goTool, "run", "-p=1", "-gcflags=sky-app/rt=-c=2", "./testdata/adt_gob_boot", path)
	command.WaitDelay = 5 * time.Second
	output, runErr := command.CombinedOutput()
	// Report only the probe's fixed-prefix diagnostic. Never dump inherited
	// environment, compiler wrapper context or arbitrary subprocess output.
	diagnostic := "no probe marker; subprocess/toolchain failed before decode"
	passed := false
	for _, line := range strings.Split(string(output), "\n") {
		if line == "ADT_GOB_PROBE_OK" {
			passed = true
		}
		if strings.HasPrefix(line, "ADT_GOB_PROBE_ERR:") {
			diagnostic = line
		}
	}
	if ctx.Err() != nil {
		t.Fatalf("production-only Gob probe exceeded its bound: %v", ctx.Err())
	}
	if runErr != nil || !passed {
		t.Fatalf("production-only Gob restart failed: %v; %s", runErr, diagnostic)
	}
}
