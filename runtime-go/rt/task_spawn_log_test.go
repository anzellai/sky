package rt

import (
	"strings"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

// spawnPanicLogs returns the ring entries logged for a Task.spawn panic whose
// raw message contains `marker`.
func spawnPanicLogs(marker string) []telemetry.LogEntry {
	var out []telemetry.LogEntry
	for _, e := range telemetry.Default().RecentLogs(0) {
		if strings.Contains(e.Message, "Task.spawn") && strings.Contains(e.Fields["panicMsg"], marker) {
			out = append(out, e)
		}
	}
	return out
}

// TestTaskSpawn_PanicIsLoggedClassified is the regression for Task.spawn
// swallowing panics. `Task_spawn` recovered with a bare `recover()` and
// dropped the value, so a spawned background loop that panicked vanished
// without a trace. The panic must now reach the log through the classified
// panic path (panic class + errId + hint), and the parent must keep running.
func TestTaskSpawn_PanicIsLoggedClassified(t *testing.T) {
	const marker = "spawned-task-boom-7c1f"
	done := make(chan struct{})
	task := func() any {
		defer close(done)
		panic("rt.IntDiv: integer division by zero (" + marker + ")")
	}

	// Forcing the spawn returns Ok(unit) at once; the panic happens on the
	// background goroutine and must not reach this goroutine.
	r := anyTaskInvoke(Task_spawn(task))
	if r.Tag != 0 {
		t.Fatalf("Task.spawn returned %+v, want Ok ()", r)
	}
	<-done

	deadline := time.Now().Add(5 * time.Second)
	var got []telemetry.LogEntry
	for time.Now().Before(deadline) {
		if got = spawnPanicLogs(marker); len(got) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("want exactly 1 logged Task.spawn panic carrying %q, got %d", marker, len(got))
	}
	e := got[0]
	if e.Level != "error" {
		t.Errorf("level = %q, want error", e.Level)
	}
	if e.Fields["panicKind"] != "DivisionByZero" {
		t.Errorf("panicKind = %q, want DivisionByZero (the classifier must run)", e.Fields["panicKind"])
	}
	if len(e.Fields["errId"]) != 8 {
		t.Errorf("errId = %q, want an 8-char correlation id", e.Fields["errId"])
	}
	if !strings.Contains(e.Message, "Sky panic in Task.spawn: DivisionByZero") {
		t.Errorf("message %q does not name Task.spawn and the class", e.Message)
	}
}

// TestTaskSpawn_ErrResultDoesNotLogPanic: a spawned task that ends in Err is a
// normal outcome, not a panic; no panic line is written for it.
func TestTaskSpawn_ErrResultDoesNotLogPanic(t *testing.T) {
	const marker = "spawned-task-err-3b9d"
	done := make(chan struct{})
	task := func() any {
		defer close(done)
		return Err[any, any](marker)
	}
	anyTaskInvoke(Task_spawn(task))
	<-done
	time.Sleep(20 * time.Millisecond)
	if n := len(spawnPanicLogs(marker)); n != 0 {
		t.Fatalf("an Err result was logged as a panic %d time(s)", n)
	}
}
