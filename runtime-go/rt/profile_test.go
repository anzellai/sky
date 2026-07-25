package rt

import (
	"strings"
	"testing"
	"time"
)

// A goroutine dump like `pprof.Lookup("goroutine").WriteTo(_, 2)` produces:
// one `goroutine N [state]:` header per goroutine, some annotated with a long
// block duration (`[chan receive, 3 minutes]`).
const sampleDump = `goroutine 1 [chan receive, 3 minutes]:
main.main()
	/app/main.go:10 +0x20
goroutine 7 [running]:
runtime/pprof.writeGoroutineStacks()
goroutine 9 [IO wait]:
internal/poll.runtime_pollWait()
`

func TestRenderReportCountsStatesAndFlagsHang(t *testing.T) {
	out := renderReport("hang — no exit after 30s", 30*time.Second, sampleDump)

	// Reason + a hang verdict must surface.
	if !strings.Contains(out, "Reason:** hang") {
		t.Errorf("missing reason line:\n%s", out)
	}
	if !strings.Contains(out, "Likely hang") {
		t.Errorf("timeout reason must render the hang verdict:\n%s", out)
	}
	// The 3-minutes-blocked goroutine must be counted in the long-blocked total.
	if !strings.Contains(out, "blocked for minutes") {
		t.Errorf("a goroutine blocked for minutes must be reported:\n%s", out)
	}
	// State breakdown: three goroutines, three distinct states.
	if !strings.Contains(out, "Goroutines:** 3") {
		t.Errorf("expected 3 goroutines counted:\n%s", out)
	}
	for _, st := range []string{"chan receive", "running", "IO wait"} {
		if !strings.Contains(out, st) {
			t.Errorf("state %q missing from breakdown:\n%s", st, out)
		}
	}
}

func TestRenderReportNoHangVerdictOnCleanExit(t *testing.T) {
	// A normal exit with only a running goroutine must NOT print a hang verdict.
	dump := "goroutine 1 [running]:\nmain.main()\n"
	out := renderReport("exit", 5*time.Millisecond, dump)
	if strings.Contains(out, "Likely hang") {
		t.Errorf("clean exit must not render a hang verdict:\n%s", out)
	}
	if !strings.Contains(out, "Reason:** exit") {
		t.Errorf("missing exit reason:\n%s", out)
	}
}
