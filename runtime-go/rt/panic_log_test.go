package rt

// Defect 3 regression — Sky.Live leaked full goroutine stacks in
// production while Sky.Http.Server did not. Same panic, ENV=production:
//
//	Sky.Http.Server  "[sky.http] panic GET /checkout (*errors.errorString)"   53 bytes
//	Sky.Live         "[sky.live] panic handling GET /checkout: boom
//	                  goroutine 26 [running]:..."                           1252 bytes
//
// The COVERAGE half of this — "no tenth site dumps a raw stack" — is
// `rust/crates/xtask/tests/panic_stacks_are_production_gated.rs`, which
// enumerates every `debug.Stack()` caller in the runtime. This file pins
// the POLICY those sites now share.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureStderr runs fn with os.Stderr redirected to a pipe and returns
// what was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	prev := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	fn()
	_ = w.Close()
	os.Stderr = prev
	out := <-done
	_ = r.Close()
	return out
}

// chdirTemp moves into a per-test scratch dir so `.skylog/panic.log`
// writes never touch the repo or another agent's worktree.
func chdirTemp(t *testing.T) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

func TestLogRecoveredPanic_ProductionEmitsNoStack(t *testing.T) {
	chdirTemp(t)
	restore := withEnvVars(t, "production", "")
	defer restore()

	out := captureStderr(t, func() {
		LogRecoveredPanic("sky.live", "GET /checkout", errors.New("boom"))
	})

	if strings.Contains(out, "goroutine ") {
		t.Fatalf("production stderr carries a goroutine stack:\n%s", out)
	}
	if strings.Contains(out, "boom") {
		t.Fatalf("production stderr carries the panic message verbatim:\n%s", out)
	}
	if !strings.Contains(out, "[sky.live] panic GET /checkout (*errors.errorString)") {
		t.Fatalf("production stderr should name the class and the route: %q", out)
	}
	// The frame is MOVED, not dropped.
	frame, err := os.ReadFile(filepath.Join(".skylog", "panic.log"))
	if err != nil {
		t.Fatalf("production must persist the full frame: %v", err)
	}
	if !strings.Contains(string(frame), "goroutine ") || !strings.Contains(string(frame), "boom") {
		t.Fatalf(".skylog/panic.log is missing the frame:\n%s", frame)
	}
}

func TestLogRecoveredPanic_DevKeepsTheStack(t *testing.T) {
	chdirTemp(t)
	restore := withEnvVars(t, "dev", "")
	defer restore()

	out := captureStderr(t, func() {
		LogRecoveredPanic("sky.live", "GET /checkout", errors.New("boom"))
	})
	if !strings.Contains(out, "goroutine ") || !strings.Contains(out, "boom") {
		t.Fatalf("dev stderr must keep the full frame for fast feedback:\n%s", out)
	}
}

// Sky.Live and Sky.Http.Server must not diverge again: the same panic
// through either subsystem produces the same SHAPE of production line.
func TestLivePanicAndHttpPanicAgreeInProduction(t *testing.T) {
	chdirTemp(t)
	restore := withEnvVars(t, "production", "")
	defer restore()

	httpOut := captureStderr(t, func() {
		logPanicFrame("GET", "/checkout", errors.New("boom"))
	})
	liveOut := captureStderr(t, func() {
		LogRecoveredPanic("sky.live", "GET /checkout", errors.New("boom"))
	})

	for name, out := range map[string]string{"sky.http": httpOut, "sky.live": liveOut} {
		if strings.Contains(out, "goroutine ") {
			t.Fatalf("%s leaks a stack in production:\n%s", name, out)
		}
	}
	// Byte counts within a tag-length of each other, not 53 vs 1252.
	if d := len(liveOut) - len(httpOut); d > 8 || d < -8 {
		t.Fatalf("production panic lines diverge: sky.http=%d bytes %q, sky.live=%d bytes %q",
			len(httpOut), httpOut, len(liveOut), liveOut)
	}
}

// The structured-log field obeys the same policy: compressed frames in
// dev, a pointer to .skylog/panic.log in production.
func TestPanicStackForLog_ProductionRedactsAndPersists(t *testing.T) {
	chdirTemp(t)
	restore := withEnvVars(t, "production", "")
	defer restore()

	got := panicStackForLog("sky.live.view", "view(model)", errors.New("boom"),
		capturePanicStack(), 8)
	if strings.Contains(got, "goroutine ") || strings.Contains(got, ".go:") {
		t.Fatalf("structured-log stack field leaks frames in production: %q", got)
	}
	if got != panicStackHint {
		t.Fatalf("expected the .skylog pointer, got %q", got)
	}
	if _, err := os.Stat(filepath.Join(".skylog", "panic.log")); err != nil {
		t.Fatalf("frame not persisted: %v", err)
	}
}

func TestPanicStackForLog_DevKeepsFrames(t *testing.T) {
	chdirTemp(t)
	restore := withEnvVars(t, "dev", "")
	defer restore()

	got := panicStackForLog("sky.live.view", "view(model)", errors.New("boom"),
		capturePanicStack(), 8)
	if !strings.Contains(got, ".go:") {
		t.Fatalf("dev structured-log stack field should carry frames: %q", got)
	}
}
