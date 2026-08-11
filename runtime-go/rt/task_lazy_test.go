package rt

// Regression gate: `Task.lazy` must have a runtime kernel.
//
// `sky-stdlib/Sky/Core/Task.sky:131` declares `lazy : (() -> a) -> Task e a` as
// `Ffi.kernel "Task_lazy"`, and `rust/crates/lower/src/kernel.rs:256` lowers it
// to `rt.Task_lazy`. No such Go symbol existed, so the function type-checked
// and then failed at codegen with [E4005] — a direct violation of "if it
// compiles, it works". `apps/ledger/src/Update.sky:336` documents the
// workaround it forced.
//
// The gate is arranged so the kernel's CONTRACT is pinned, not merely its
// existence: a `Task_lazy` that ran the thunk eagerly would satisfy "the symbol
// resolves" while destroying the only reason the combinator exists.

import "testing"

// Task.lazy defers: constructing the Task must not run the thunk. Only running
// the Task may.
func TestTaskLazy_DefersUntilRun(t *testing.T) {
	runs := 0
	task := Task_lazy(func() any {
		runs++
		return 42
	})
	if runs != 0 {
		t.Fatalf("Task.lazy ran its thunk at construction time (%d runs) — "+
			"the whole point of `lazy` is that it does not", runs)
	}
	res, ok := AnyTaskRun(task).(SkyResult[any, any])
	if !ok {
		t.Fatalf("Task.lazy did not produce a Task: %T", AnyTaskRun(task))
	}
	if res.Tag != 0 {
		t.Fatalf("Task.lazy failed: %+v", res.ErrValue)
	}
	if got := AsIntOrZero(res.OkValue); got != 42 {
		t.Errorf("value: got %v want 42", got)
	}
	if runs != 1 {
		t.Errorf("thunk runs after one Task.run: got %d want 1", runs)
	}
}

// The Sky signature is `(() -> a) -> Task e a`, and a Sky unit-taking lambda
// lowers to a one-argument Go func. Both that and the zero-argument shape must
// force, since which one codegen emits depends on how the thunk was written.
func TestTaskLazy_AcceptsUnitTakingThunk(t *testing.T) {
	task := Task_lazy(func(_ any) any { return "computed" })
	res, ok := AnyTaskRun(task).(SkyResult[any, any])
	if !ok {
		t.Fatalf("Task.lazy did not produce a Task: %T", AnyTaskRun(task))
	}
	if res.Tag != 0 {
		t.Fatalf("Task.lazy failed: %+v", res.ErrValue)
	}
	if got := AsString(res.OkValue); got != "computed" {
		t.Errorf("value: got %q want %q", got, "computed")
	}
}

// Re-running the Task re-runs the thunk. `lazy` defers work; it does not
// memoise it (a memoising `lazy` would be a different combinator, and the
// stdlib's CAF rules already cover memoisation).
func TestTaskLazy_ReRunsOnEachRun(t *testing.T) {
	runs := 0
	task := Task_lazy(func() any {
		runs++
		return runs
	})
	AnyTaskRun(task)
	AnyTaskRun(task)
	if runs != 2 {
		t.Errorf("thunk runs after two Task.run: got %d want 2", runs)
	}
}
