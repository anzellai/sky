package rt

import "testing"

// `Task.map2` … `map5` and `Task.andMap` were advertised by
// `hir::KERNEL_FUNCTIONS` with no runtime symbol behind them: every call
// died at codegen with `[E4005] Task has no member map2`. These tests pin
// the three things the Sky signatures now promise — the RESULT, the number
// of times each argument task is forced, and the ORDER those forces happen
// in — because the signature alone cannot say any of them.

// A task that records that it ran, in order, and yields `v`.
func recordingTask(log *[]string, name string, v any) any {
	return func() any {
		*log = append(*log, name)
		return Ok[any, any](v)
	}
}

func failingTask(log *[]string, name string, e any) any {
	return func() any {
		*log = append(*log, name)
		return Err[any, any](e)
	}
}

func TestTaskMap2CombinesAndForcesLeftToRightExactlyOnce(t *testing.T) {
	var log []string
	add := func(a, b any) any { return AsInt(a) + AsInt(b) }
	got := AnyTaskRun(Task_map2(add,
		recordingTask(&log, "a", 1),
		recordingTask(&log, "b", 2)))

	r, ok := got.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		t.Fatalf("Task_map2 did not yield Ok: %#v", got)
	}
	if AsInt(r.OkValue) != 3 {
		t.Fatalf("Task_map2 = %v, want 3", r.OkValue)
	}
	if len(log) != 2 || log[0] != "a" || log[1] != "b" {
		t.Fatalf("forcing order/count = %v, want exactly [a b] — a task forced "+
			"twice runs its effect twice", log)
	}
}

func TestTaskMap2ShortCircuitsWithoutForcingTheRight(t *testing.T) {
	var log []string
	add := func(a, b any) any { return AsInt(a) + AsInt(b) }
	got := AnyTaskRun(Task_map2(add,
		failingTask(&log, "a", "boom"),
		recordingTask(&log, "b", 2)))

	r, ok := got.(SkyResult[any, any])
	if !ok || r.Tag == 0 {
		t.Fatalf("Task_map2 over a failing left did not yield Err: %#v", got)
	}
	if len(log) != 1 || log[0] != "a" {
		t.Fatalf("forcing log = %v, want [a] — the right task must NOT run "+
			"after the left fails (its effect would be unrecoverable)", log)
	}
}

func TestTaskMap3Map4Map5CombineInOrder(t *testing.T) {
	cat := func(parts ...any) any {
		out := ""
		for _, p := range parts {
			out += AsString(p)
		}
		return out
	}

	var log3 []string
	r3 := AnyTaskRun(Task_map3(
		func(a, b, c any) any { return cat(a, b, c) },
		recordingTask(&log3, "a", "x"),
		recordingTask(&log3, "b", "y"),
		recordingTask(&log3, "c", "z")))
	if v := r3.(SkyResult[any, any]); v.Tag != 0 || AsString(v.OkValue) != "xyz" {
		t.Fatalf("Task_map3 = %#v, want Ok \"xyz\"", r3)
	}
	if len(log3) != 3 || log3[0] != "a" || log3[1] != "b" || log3[2] != "c" {
		t.Fatalf("map3 forcing order = %v, want [a b c]", log3)
	}

	var log4 []string
	r4 := AnyTaskRun(Task_map4(
		func(a, b, c, d any) any { return cat(a, b, c, d) },
		recordingTask(&log4, "a", "w"),
		recordingTask(&log4, "b", "x"),
		recordingTask(&log4, "c", "y"),
		recordingTask(&log4, "d", "z")))
	if v := r4.(SkyResult[any, any]); v.Tag != 0 || AsString(v.OkValue) != "wxyz" {
		t.Fatalf("Task_map4 = %#v, want Ok \"wxyz\"", r4)
	}
	if len(log4) != 4 {
		t.Fatalf("map4 forced %d task(s), want 4: %v", len(log4), log4)
	}

	var log5 []string
	r5 := AnyTaskRun(Task_map5(
		func(a, b, c, d, e any) any { return cat(a, b, c, d, e) },
		recordingTask(&log5, "a", "v"),
		recordingTask(&log5, "b", "w"),
		recordingTask(&log5, "c", "x"),
		recordingTask(&log5, "d", "y"),
		recordingTask(&log5, "e", "z")))
	if v := r5.(SkyResult[any, any]); v.Tag != 0 || AsString(v.OkValue) != "vwxyz" {
		t.Fatalf("Task_map5 = %#v, want Ok \"vwxyz\"", r5)
	}
	if len(log5) != 5 {
		t.Fatalf("map5 forced %d task(s), want 5: %v", len(log5), log5)
	}
}

// The argument order is the part a wrong signature would silently invert:
// `Task_andMap(ta, tfn)` is VALUE first, FUNCTION second, matching
// `Maybe.andMap ma mfn` / `Result.andMap ra rfn`. Swapping the two
// type-checks perfectly and produces the wrong answer, so it is pinned here.
func TestTaskAndMapTakesValueFirstAndForcesFunctionFirst(t *testing.T) {
	var log []string
	triple := func(n any) any { return AsInt(n) * 3 }

	got := AnyTaskRun(Task_andMap(
		recordingTask(&log, "value", 7),
		recordingTask(&log, "fn", triple)))

	r, ok := got.(SkyResult[any, any])
	if !ok || r.Tag != 0 {
		t.Fatalf("Task_andMap did not yield Ok: %#v", got)
	}
	if AsInt(r.OkValue) != 21 {
		t.Fatalf("Task_andMap = %v, want 21 (7 * 3) — a swapped argument order "+
			"still type-checks, so the value is the only witness", r.OkValue)
	}
	if len(log) != 2 || log[0] != "fn" || log[1] != "value" {
		t.Fatalf("forcing order = %v, want [fn value] — `Maybe.andMap` and "+
			"`Result.andMap` both scrutinise the FUNCTION container first, and "+
			"for Task that order is observable", log)
	}
}

func TestTaskAndMapPropagatesFunctionFailure(t *testing.T) {
	var log []string
	got := AnyTaskRun(Task_andMap(
		recordingTask(&log, "value", 7),
		failingTask(&log, "fn", "no fn")))

	r, ok := got.(SkyResult[any, any])
	if !ok || r.Tag == 0 {
		t.Fatalf("Task_andMap over a failing function did not yield Err: %#v", got)
	}
	if len(log) != 1 || log[0] != "fn" {
		t.Fatalf("forcing log = %v, want [fn] — the value task must not run", log)
	}
}
