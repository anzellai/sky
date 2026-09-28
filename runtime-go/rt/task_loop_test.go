package rt

import (
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

// stepValue builds a Task.Step value the way generated code does for a
// stdlib ADT: an rt.SkyADT carrying the constructor NAME. The tags are
// deliberately NOT 0 and 1, so a decoder that keyed on tag order instead of
// the name would take the wrong branch.
func stepValue(name string, payload any) any {
	tag := 41
	if name == "Done" {
		tag = 17
	}
	return SkyADT{Tag: tag, SkyName: name, Fields: []any{payload}}
}

// countdown is the step function of a loop that counts `n` down to zero and
// finishes with the number of steps taken.
func countdown(state any) any {
	s := state.(SkyTuple2)
	n, steps := s.V0.(int), s.V1.(int)
	if n == 0 {
		return AnyTaskSucceed(stepValue("Done", steps))
	}
	return AnyTaskSucceed(stepValue("Loop", SkyTuple2{V0: n - 1, V1: steps + 1}))
}

// TestTaskLoop_TwoMillionStepsConstantStack is the regression for the
// recursive-andThen stack overflow. With the goroutine stack capped at 1 MB,
// two million steps must finish. A loop that grew the stack by even a few
// bytes per step would exceed the cap and the Go runtime would kill the test
// binary (a fatal error, not a failing assertion — which is still a red run).
func TestTaskLoop_TwoMillionStepsConstantStack(t *testing.T) {
	const steps = 2_000_000
	prev := debug.SetMaxStack(1 << 20)
	defer debug.SetMaxStack(prev)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	task := Task_loop(countdown, SkyTuple2{V0: steps, V1: 0})
	r := anyTaskInvoke(task)

	runtime.GC()
	runtime.ReadMemStats(&after)

	if r.Tag != 0 {
		t.Fatalf("Task.loop returned Err %v, want Ok", r.ErrValue)
	}
	if got := r.OkValue.(int); got != steps {
		t.Fatalf("Task.loop finished after %d steps, want %d", got, steps)
	}
	// Flat memory: the loop keeps one state, so the live heap after the run
	// must not grow with the step count. Two million retained frames or
	// closures would be hundreds of MB; 16 MB is generous slack for the test
	// process itself.
	const slack = 16 << 20
	if after.HeapInuse > before.HeapInuse+slack {
		t.Fatalf("live heap grew from %d to %d bytes across %d steps; Task.loop must run in constant memory",
			before.HeapInuse, after.HeapInuse, steps)
	}
	if after.StackInuse > before.StackInuse+slack {
		t.Fatalf("stack in use grew from %d to %d bytes across %d steps",
			before.StackInuse, after.StackInuse, steps)
	}
}

// TestTaskLoop_ErrShortCircuits: an Err from a step ends the loop with that
// error, and no further step runs.
func TestTaskLoop_ErrShortCircuits(t *testing.T) {
	calls := 0
	step := func(state any) any {
		calls++
		n := state.(int)
		if n == 3 {
			return AnyTaskFail("stop at 3")
		}
		return AnyTaskSucceed(stepValue("Loop", n+1))
	}
	r := anyTaskInvoke(Task_loop(step, 0))
	if r.Tag != 1 || r.ErrValue != "stop at 3" {
		t.Fatalf("got %+v, want Err \"stop at 3\"", r)
	}
	if calls != 4 {
		t.Fatalf("step ran %d times, want 4 (states 0,1,2,3)", calls)
	}
}

// TestTaskLoop_DoneOnFirstStep: Done on the first step returns at once.
func TestTaskLoop_DoneOnFirstStep(t *testing.T) {
	step := func(state any) any { return AnyTaskSucceed(stepValue("Done", "x")) }
	r := anyTaskInvoke(Task_loop(step, 0))
	if r.Tag != 0 || r.OkValue != "x" {
		t.Fatalf("got %+v, want Ok \"x\"", r)
	}
}

// TestTaskLoop_DecodesByNameNotTag: a Loop whose tag is 0 (the tag a naive
// decoder would read as the FIRST constructor) and a Done whose tag is 1 must
// still be decoded by name. Swapped tags make any tag-based decoder loop
// forever or stop early.
func TestTaskLoop_DecodesByNameNotTag(t *testing.T) {
	step := func(state any) any {
		n := state.(int)
		if n == 5 {
			return AnyTaskSucceed(SkyADT{Tag: 0, SkyName: "Done", Fields: []any{n}})
		}
		return AnyTaskSucceed(SkyADT{Tag: 1, SkyName: "Loop", Fields: []any{n + 1}})
	}
	r := anyTaskInvoke(Task_loop(step, 0))
	if r.Tag != 0 || r.OkValue != 5 {
		t.Fatalf("got %+v, want Ok 5", r)
	}
}

// TestTaskLoop_StructShapeStep: a typed struct exposing SkyName + Fields is
// decoded the same way (the reflect path shared with readShouldRetry).
func TestTaskLoop_StructShapeStep(t *testing.T) {
	type stepStruct struct {
		Tag     int
		SkyName string
		Fields  []any
	}
	step := func(state any) any {
		n := state.(int)
		if n == 2 {
			return AnyTaskSucceed(stepStruct{Tag: 9, SkyName: "Done", Fields: []any{"end"}})
		}
		return AnyTaskSucceed(stepStruct{Tag: 8, SkyName: "Loop", Fields: []any{n + 1}})
	}
	r := anyTaskInvoke(Task_loop(step, 0))
	if r.Tag != 0 || r.OkValue != "end" {
		t.Fatalf("got %+v, want Ok \"end\"", r)
	}
}

// TestTaskLoop_UnknownStepPanicsClassified: a step that yields something that
// is not a Step value is a compiler bug. It must fail loudly with a classified
// panic, never pick a branch by guessing.
func TestTaskLoop_UnknownStepPanicsClassified(t *testing.T) {
	step := func(state any) any { return AnyTaskSucceed(42) }
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected a panic for a non-Step value")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "Task.Step") {
			t.Fatalf("panic %q does not name Task.Step", msg)
		}
		if kind, _ := classifyPanic(msg); kind != "CoerceFailure" {
			t.Fatalf("classifyPanic kind = %q, want CoerceFailure", kind)
		}
	}()
	anyTaskInvoke(Task_loop(step, 0))
}

// TestTaskLoop_PanicPropagates: a panic inside a step is not swallowed by the
// loop; it reaches the caller's recover site exactly as a panic in any other
// Task does.
func TestTaskLoop_PanicPropagates(t *testing.T) {
	step := func(state any) any { panic("boom in step") }
	defer func() {
		if r := recover(); r != "boom in step" {
			t.Fatalf("recovered %v, want the step's own panic", r)
		}
	}()
	anyTaskInvoke(Task_loop(step, 0))
	t.Fatal("unreachable: the step panics")
}

// TestTaskForever_StopsOnErr: Task.forever re-runs its task until the task
// fails, then returns that error. Two million successful runs first prove the
// same constant-stack property as Task.loop.
func TestTaskForever_StopsOnErr(t *testing.T) {
	const runs = 2_000_000
	prev := debug.SetMaxStack(1 << 20)
	defer debug.SetMaxStack(prev)
	n := 0
	var task any = func() any {
		n++
		if n > runs {
			return Err[any, any]("done")
		}
		return Ok[any, any](struct{}{})
	}
	r := anyTaskInvoke(Task_forever(task))
	if r.Tag != 1 || r.ErrValue != "done" {
		t.Fatalf("got %+v, want Err \"done\"", r)
	}
	if n != runs+1 {
		t.Fatalf("task ran %d times, want %d", n, runs+1)
	}
}

// TestRecursiveAndThen_OverflowsTheSameCap is the control for the constant-
// stack tests above: it proves the 1 MB cap they run under is tight enough to
// catch the defect. The same countdown written as plain `andThen` recursion
// (the shape Task.loop replaces) is run in a child process — a Go stack
// overflow is fatal, so it cannot run in this one — and must die with Go's
// "stack exceeds" error.
func TestRecursiveAndThen_OverflowsTheSameCap(t *testing.T) {
	if os.Getenv("SKY_TASK_LOOP_CONTROL") == "1" {
		debug.SetMaxStack(1 << 20)
		var count func(n int) any
		count = func(n int) any {
			if n == 0 {
				return AnyTaskSucceed(0)
			}
			return AnyTaskAndThen(func(_ any) any { return count(n - 1) }, AnyTaskSucceed(n))
		}
		anyTaskInvoke(count(2_000_000))
		os.Exit(0)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRecursiveAndThen_OverflowsTheSameCap$")
	cmd.Env = append(os.Environ(), "SKY_TASK_LOOP_CONTROL=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("recursive andThen over 2,000,000 steps finished under a 1 MB stack; the control no longer proves anything:\n%s", out)
	}
	if !strings.Contains(string(out), "stack exceeds") {
		t.Fatalf("child failed, but not with a stack overflow: %v\n%.2000s", err, out)
	}
}
