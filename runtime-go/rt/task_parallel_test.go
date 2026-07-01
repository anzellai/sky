package rt

import (
	"sync/atomic"
	"testing"
	"time"
)

// makeOkTask returns a thunk that, when invoked via SkyCall, sleeps
// for the given duration then returns Ok(value).
func makeOkTask(value any, sleep time.Duration, started, finished *atomic.Int32) any {
	return func() any {
		if started != nil {
			started.Add(1)
		}
		if sleep > 0 {
			time.Sleep(sleep)
		}
		if finished != nil {
			finished.Add(1)
		}
		return Ok[any, any](value)
	}
}

// makeErrTask returns a thunk that sleeps then returns Err(value).
func makeErrTask(value any, sleep time.Duration, started, finished *atomic.Int32) any {
	return func() any {
		if started != nil {
			started.Add(1)
		}
		if sleep > 0 {
			time.Sleep(sleep)
		}
		if finished != nil {
			finished.Add(1)
		}
		return Err[any, any](value)
	}
}

// TestTaskParallelAllOk: all tasks succeed → Ok with results in input order.
func TestTaskParallelAllOk(t *testing.T) {
	tasks := []any{
		makeOkTask("a", 0, nil, nil),
		makeOkTask("b", 0, nil, nil),
		makeOkTask("c", 0, nil, nil),
	}
	thunk := Task_parallel(tasks)
	res := SkyCall(thunk)
	tag, okV, errV := anyResultView(res)
	if tag != 0 {
		t.Fatalf("expected Ok, got Err(%v)", errV)
	}
	list, ok := okV.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", okV)
	}
	if len(list) != 3 {
		t.Fatalf("expected 3 results, got %d", len(list))
	}
	if list[0] != "a" || list[1] != "b" || list[2] != "c" {
		t.Fatalf("results out of order: %v", list)
	}
}

// TestTaskParallelEmpty: empty input list → Ok([]).
func TestTaskParallelEmpty(t *testing.T) {
	thunk := Task_parallel([]any{})
	res := SkyCall(thunk)
	tag, okV, _ := anyResultView(res)
	if tag != 0 {
		t.Fatalf("expected Ok, got tag=%d", tag)
	}
	list, ok := okV.([]any)
	if !ok || len(list) != 0 {
		t.Fatalf("expected empty []any, got %v (%T)", okV, okV)
	}
}

// TestTaskParallelFirstErrShortCircuits: when one task errors quickly
// and another would take a long time, Task_parallel returns the Err
// BEFORE the slow task completes. This is the core documented
// semantic that pre-fix-runtime violated (it waited for all tasks
// via WaitGroup.Wait()).
func TestTaskParallelFirstErrShortCircuits(t *testing.T) {
	var started, finished atomic.Int32
	tasks := []any{
		makeErrTask("boom", 10*time.Millisecond, &started, &finished),
		makeOkTask("slow", 2*time.Second, &started, &finished),
	}
	t0 := time.Now()
	thunk := Task_parallel(tasks)
	res := SkyCall(thunk)
	elapsed := time.Since(t0)

	tag, _, errV := anyResultView(res)
	if tag == 0 {
		t.Fatalf("expected Err, got Ok")
	}
	if errV != "boom" {
		t.Fatalf("expected Err(\"boom\"), got Err(%v)", errV)
	}
	// Must return BEFORE the 2s slow task naturally completes.
	// Generous 500ms ceiling for scheduling + GC jitter.
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Task_parallel did NOT short-circuit: took %v (expected < 500ms)", elapsed)
	}
}

// TestTaskParallelPreservesOrderWithJitter: results MUST land at their
// input index even when goroutines finish out of order.
func TestTaskParallelPreservesOrderWithJitter(t *testing.T) {
	tasks := []any{
		makeOkTask(1, 30*time.Millisecond, nil, nil),
		makeOkTask(2, 5*time.Millisecond, nil, nil),
		makeOkTask(3, 20*time.Millisecond, nil, nil),
		makeOkTask(4, 1*time.Millisecond, nil, nil),
	}
	thunk := Task_parallel(tasks)
	res := SkyCall(thunk)
	tag, okV, _ := anyResultView(res)
	if tag != 0 {
		t.Fatalf("expected Ok, got tag=%d", tag)
	}
	list := okV.([]any)
	for i, want := range []any{1, 2, 3, 4} {
		if list[i] != want {
			t.Fatalf("position %d: want %v, got %v", i, want, list[i])
		}
	}
}

// TestTaskParallelAllErr: every task errors → return first observed Err.
func TestTaskParallelAllErr(t *testing.T) {
	tasks := []any{
		makeErrTask("e1", 0, nil, nil),
		makeErrTask("e2", 0, nil, nil),
	}
	thunk := Task_parallel(tasks)
	res := SkyCall(thunk)
	tag, _, errV := anyResultView(res)
	if tag == 0 {
		t.Fatalf("expected Err, got Ok")
	}
	// Either e1 or e2 may win; both are valid documented behaviour
	// (declaration order is best-effort under concurrent dispatch).
	if errV != "e1" && errV != "e2" {
		t.Fatalf("expected Err(e1) or Err(e2), got Err(%v)", errV)
	}
}
