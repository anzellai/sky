package rt

import (
	"runtime"
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

// TestTaskParallelN_BoundsConcurrency: with limit=3, at most 3 tasks run at
// once (Task_parallel would run all of them). Tracks live concurrency via an
// atomic max and asserts it never exceeds the limit — the whole point of B6.
func TestTaskParallelN_BoundsConcurrency(t *testing.T) {
	const limit = 3
	const total = 20
	var cur, max atomic.Int32
	mk := func() any {
		return func() any {
			c := cur.Add(1)
			for {
				m := max.Load()
				if c <= m || max.CompareAndSwap(m, c) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			cur.Add(-1)
			return Ok[any, any](0)
		}
	}
	tasks := make([]any, total)
	for i := range tasks {
		tasks[i] = mk()
	}
	tag, okV, errV := anyResultView(SkyCall(Task_parallelN(limit, tasks)))
	if tag != 0 {
		t.Fatalf("expected Ok, got Err(%v)", errV)
	}
	if list, ok := okV.([]any); !ok || len(list) != total {
		t.Fatalf("expected %d results, got %v (%T)", total, okV, okV)
	}
	if got := max.Load(); got > limit {
		t.Fatalf("parallelN ran %d tasks concurrently; the limit was %d — bound not enforced", got, limit)
	}
	if max.Load() < 2 {
		t.Fatalf("parallelN never ran more than one task at a time (max=%d) — it is not actually parallel", max.Load())
	}
}

// TestTaskParallelN_NoLeakOnEarlyError: a fast error at the head of a large
// batch must (a) short-circuit to Err, (b) STOP the dispatcher launching the
// tail, and (c) leave no leaked goroutines once the <=limit in-flight workers
// drain. This is the goroutine-leak regression the v1 audit (B6) asked for.
func TestTaskParallelN_NoLeakOnEarlyError(t *testing.T) {
	time.Sleep(50 * time.Millisecond) // let transient goroutines settle
	base := runtime.NumGoroutine()
	const total = 30
	var started, finished atomic.Int32
	tasks := make([]any, 0, total)
	tasks = append(tasks, makeErrTask("boom", 5*time.Millisecond, &started, &finished))
	for i := 0; i < total-1; i++ {
		tasks = append(tasks, makeOkTask(i, 200*time.Millisecond, &started, &finished))
	}
	tag, _, errV := anyResultView(SkyCall(Task_parallelN(4, tasks)))
	if tag == 0 {
		t.Fatalf("expected Err from the failing task, got Ok")
	}
	if errV != "boom" {
		t.Fatalf("expected Err(\"boom\"), got Err(%v)", errV)
	}
	// With limit=4 and an early error, the dispatcher must halt — far fewer than
	// all 30 tasks should ever have started.
	if s := started.Load(); s >= total {
		t.Fatalf("parallelN launched all %d tasks despite an early error (started=%d) — dispatcher did not halt", total, s)
	}
	// In-flight workers (<=4, each <=200ms) plus the dispatcher must all exit.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= base+1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if leaked := runtime.NumGoroutine() - base; leaked > 1 {
		t.Fatalf("parallelN leaked ~%d goroutines after an early error (base=%d, now=%d)", leaked, base, runtime.NumGoroutine())
	}
}
