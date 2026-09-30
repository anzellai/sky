package rt

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"reflect"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The Task trampoline. A Sky loop written as plain recursion —
//
//	step n = work |> Task.andThen (\_ -> step (n + 1))
//
// — used to nest Go frames on every step (anyTaskInvoke forced the
// continuation's task INSIDE the frame that forced its source), and two
// million steps died with Go's fatal "stack overflow". A fatal error is not a
// panic: no recover site sees it, the process exits. The tests below run the
// recursion in a CHILD process so that the old failure shows up as a failed
// assertion in the parent rather than killing the whole test binary.

const trampolineChildEnv = "SKY_TASK_TRAMPOLINE_CHILD"

// runTrampolineChild re-runs this test binary with only `name` selected and
// the child marker set, and returns its combined output and error.
func runTrampolineChild(t *testing.T, name string) (string, error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^"+name+"$", "-test.v")
	cmd.Env = append(os.Environ(), trampolineChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// recStep builds the task of one recursion step exactly as generated code
// does: the typed boundary (TaskCoerceT) around the any-typed combinator.
func recStep(limit, n int) SkyTask[any, int] {
	if n >= limit {
		return TaskCoerceT[any, int](AnyTaskSucceed(any(n)))
	}
	return TaskCoerceT[any, int](AnyTaskAndThen(any(func(_w0 any) any {
		return any(recStep(limit, n+1))
	}), any(TaskCoerceT[any, int](AnyTaskSucceed(any(n))))))
}

// TestTaskAndThenRecursion_TwoMillionSteps is the regression for the
// recursive-andThen stack overflow. The child caps the goroutine stack at
// 1 MB, so even a few bytes of growth per step overflows long before two
// million steps.
func TestTaskAndThenRecursion_TwoMillionSteps(t *testing.T) {
	if os.Getenv(trampolineChildEnv) == "" {
		out, err := runTrampolineChild(t, t.Name())
		if err != nil || !strings.Contains(out, "trampoline-ok 2000000") {
			t.Fatalf("andThen recursion of 2,000,000 steps did not finish (err=%v); child output tail:\n%s",
				err, tail(out, 25))
		}
		return
	}
	prev := debug.SetMaxStack(1 << 20)
	defer debug.SetMaxStack(prev)
	r := anyTaskInvoke(recStep(2_000_000, 0))
	if r.Tag != 0 {
		t.Fatalf("Err %v", r.ErrValue)
	}
	t.Logf("trampoline-ok %d", r.OkValue)
}

// TestTaskOnErrorRecursion_TwoMillionSteps: recursion through the error
// channel (`Task.onError (\_ -> retry (n + 1))`) is a continuation too, and
// must not grow the stack either.
func TestTaskOnErrorRecursion_TwoMillionSteps(t *testing.T) {
	if os.Getenv(trampolineChildEnv) == "" {
		out, err := runTrampolineChild(t, t.Name())
		if err != nil || !strings.Contains(out, "trampoline-ok 2000000") {
			t.Fatalf("onError recursion of 2,000,000 steps did not finish (err=%v); child output tail:\n%s",
				err, tail(out, 25))
		}
		return
	}
	prev := debug.SetMaxStack(1 << 20)
	defer debug.SetMaxStack(prev)
	var retry func(n int) any
	retry = func(n int) any {
		if n >= 2_000_000 {
			return AnyTaskSucceed(n)
		}
		return Task_onError(func(_ any) any { return retry(n + 1) }, AnyTaskFail("again"))
	}
	r := anyTaskInvoke(retry(0))
	if r.Tag != 0 {
		t.Fatalf("Err %v", r.ErrValue)
	}
	t.Logf("trampoline-ok %d", r.OkValue)
}

// TestTaskLeftNestedChain_TwoMillionSteps: a chain built by a Go-side fold
// (`acc = acc |> Task.map inc` two million times) nests the SOURCE rather
// than the continuation. Its frames live on the interpreter's heap stack.
func TestTaskLeftNestedChain_TwoMillionSteps(t *testing.T) {
	if os.Getenv(trampolineChildEnv) == "" {
		out, err := runTrampolineChild(t, t.Name())
		if err != nil || !strings.Contains(out, "trampoline-ok 2000000") {
			t.Fatalf("left-nested chain of 2,000,000 steps did not finish (err=%v); child output tail:\n%s",
				err, tail(out, 25))
		}
		return
	}
	prev := debug.SetMaxStack(1 << 20)
	defer debug.SetMaxStack(prev)
	inc := func(v any) any { return v.(int) + 1 }
	acc := AnyTaskSucceed(0)
	for i := 0; i < 1_000_000; i++ {
		acc = Task_map(inc, acc)
		acc = AnyTaskAndThen(func(v any) any { return AnyTaskSucceed(v.(int) + 1) }, acc)
	}
	r := anyTaskInvoke(acc)
	if r.Tag != 0 {
		t.Fatalf("Err %v", r.ErrValue)
	}
	t.Logf("trampoline-ok %d", r.OkValue)
}

func tail(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// soakSeconds is how long the constant-heap soaks run. Default 3 s keeps the
// unit suite fast; SKY_TASK_SOAK_SECONDS=10 runs the full check.
func soakSeconds() time.Duration {
	if s, err := strconv.Atoi(os.Getenv("SKY_TASK_SOAK_SECONDS")); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return 3 * time.Second
}

// heapSampler samples the live heap (after a forced GC) at a fixed interval
// from a side goroutine while a Task runs, and reports whether it trends up.
type heapSampler struct {
	stop    chan struct{}
	done    chan struct{}
	samples []uint64
}

func startHeapSampler(every time.Duration) *heapSampler {
	h := &heapSampler{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(h.done)
		tk := time.NewTicker(every)
		defer tk.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-tk.C:
				runtime.GC()
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				h.samples = append(h.samples, m.HeapAlloc)
			}
		}
	}()
	return h
}

func (h *heapSampler) finish() []uint64 {
	close(h.stop)
	<-h.done
	return h.samples
}

// assertNoHeapGrowth fails when the second half of the samples sits
// materially above the first half. A retained frame or closure per iteration
// would grow the heap by tens of MB per second at these iteration rates.
func assertNoHeapGrowth(t *testing.T, what string, samples []uint64, iterations int) {
	t.Helper()
	if len(samples) < 6 {
		t.Fatalf("%s: only %d heap samples", what, len(samples))
	}
	// Skip the first sample: it includes warm-up allocations.
	s := samples[1:]
	half := len(s) / 2
	// Compare half MEDIANS, not means. A sample taken after runtime.GC() can
	// still hold what the running loop allocated after the mark phase, so one
	// sample can read 3x the others (a CI run saw 34 MB among 10 MB samples)
	// and move a mean past the slack with no leak. A retained frame per
	// iteration grows the heap by hundreds of MB over these iteration counts,
	// which moves every sample of the second half, so the median sees it.
	median := func(xs []uint64) float64 {
		c := append([]uint64(nil), xs...)
		sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
		m := len(c) / 2
		if len(c)%2 == 0 {
			return (float64(c[m-1]) + float64(c[m])) / 2
		}
		return float64(c[m])
	}
	first, second := median(s[:half]), median(s[half:])
	// The slack scales with the live heap. HeapAlloc is the whole process,
	// and the package's other tests run beside this one: on a macOS runner the
	// heap sat near 80 MB and the half medians differed by 5-7 MB from one
	// sample to the next, with no trend (min and max in both halves), which a
	// fixed 4 MB slack read as growth. A leak is far larger than this slack:
	// retaining even 8 bytes per iteration adds iterations*4 bytes between the
	// half medians, over 100 MB at the tens of millions of iterations these
	// loops run.
	slack := math.Max(4<<20, first/5)
	leakSignal := float64(iterations) * 4
	t.Logf("%s: %d iterations, %d samples, heap first-half %.0f B, second-half %.0f B, min %d, max %d, slack %.0f B, 8-byte-leak signal %.0f B",
		what, iterations, len(samples), first, second, minU(s), maxU(s), slack, leakSignal)
	if second > first+slack {
		t.Fatalf("%s: live heap grew from %.0f to %.0f bytes over %d iterations; samples %v",
			what, first, second, iterations, samples)
	}
}

func minU(xs []uint64) uint64 {
	m := xs[0]
	for _, x := range xs {
		if x < m {
			m = x
		}
	}
	return m
}

func maxU(xs []uint64) uint64 {
	m := xs[0]
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}

// TestTaskForever_ConstantHeap: Task.forever over a task that itself chains
// andThen/map holds a constant live heap while it runs.
func TestTaskForever_ConstantHeap(t *testing.T) {
	deadline := time.Now().Add(soakSeconds())
	iterations := 0
	tick := AnyTaskAndThen(func(v any) any {
		iterations++
		if iterations%4096 == 0 && time.Now().After(deadline) {
			return AnyTaskFail("stop")
		}
		return Task_map(func(x any) any { return x }, AnyTaskSucceed(v))
	}, TaskCoerceT[any, int](AnyTaskSucceed(1)))
	h := startHeapSampler(200 * time.Millisecond)
	r := anyTaskInvoke(Task_forever(tick))
	samples := h.finish()
	if r.Tag != 1 || r.ErrValue != "stop" {
		t.Fatalf("got %+v, want Err stop", r)
	}
	assertNoHeapGrowth(t, "Task.forever", samples, iterations)
}

// TestTaskRecursiveForever_ConstantHeap: the same service loop written as
// plain recursion (`loop _ = tick |> Task.andThen (\_ -> loop ())`) must also
// hold a constant heap: nothing may keep the previous iteration reachable.
func TestTaskRecursiveForever_ConstantHeap(t *testing.T) {
	deadline := time.Now().Add(soakSeconds())
	iterations := 0
	var loop func() SkyTask[any, int]
	loop = func() SkyTask[any, int] {
		return TaskCoerceT[any, int](AnyTaskAndThen(any(func(_ any) any {
			iterations++
			if iterations%4096 == 0 && time.Now().After(deadline) {
				return AnyTaskFail("stop")
			}
			return any(loop())
		}), any(TaskCoerceT[any, int](AnyTaskSucceed(iterations)))))
	}
	h := startHeapSampler(200 * time.Millisecond)
	r := anyTaskInvoke(loop())
	samples := h.finish()
	if r.Tag != 1 || r.ErrValue != "stop" {
		t.Fatalf("got %+v, want Err stop", r)
	}
	assertNoHeapGrowth(t, "recursive andThen loop", samples, iterations)
}

// TestTaskCoerceT_IsFree: converting between SkyTask instantiations — what
// the compiler emits at every typed Task boundary — allocates nothing. A
// wrapper per conversion would make a long-running loop allocate per step.
func TestTaskCoerceT_IsFree(t *testing.T) {
	base := AnyTaskSucceed(1)
	typed := TaskCoerceT[any, int](base)
	var sink any
	allocs := testing.AllocsPerRun(1000, func() {
		a := TaskCoerceT[SkyADT, int](base)
		b := TaskCoerceT[any, int](any(a))
		sink = any(TaskCoerceT[string, any](any(b)))
	})
	if allocs != 0 {
		t.Fatalf("TaskCoerceT allocated %.1f times per conversion chain, want 0", allocs)
	}
	if n, _ := taskNodeOf(sink); n != typed.n {
		t.Fatalf("TaskCoerceT must keep the node: got %p, want %p", n, typed.n)
	}
	if r := anyTaskInvoke(sink); r.Tag != 0 || r.OkValue != 1 {
		t.Fatalf("converted task ran to %+v", r)
	}
}

// expectCoercePanic runs f and requires a panic whose message classifies as
// CoerceFailure — the loud, classified failure a site must give instead of
// passing an unforced Task on as a value.
func expectCoercePanic(t *testing.T, what string, f func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Fatalf("%s: want a classified panic, got none", what)
		}
		msg := fmt.Sprint(r)
		if kind, _ := classifyPanic(msg); kind != "CoerceFailure" {
			t.Fatalf("%s: panic %q classifies as %s, want CoerceFailure", what, msg, kind)
		}
	}()
	f()
}

type taskHolder struct {
	Run SkyTask[SkyADT, int]
}

// TestTaskLike_EveryForceSiteRecognisesASkyTask: each site that forces or
// converts a value that may be a Task sees the SkyTask node, never takes the
// "not a function, return it as a value" branch.
func TestTaskLike_EveryForceSiteRecognisesASkyTask(t *testing.T) {
	ran := 0
	mk := func() any {
		return AnyTaskAndThen(func(v any) any { ran++; return AnyTaskSucceed(v.(int) + 1) }, AnyTaskSucceed(41))
	}
	check := func(site string, got any) {
		t.Helper()
		tag, ok, _ := anyResultView(got)
		if tag != 0 || ok != 42 {
			t.Fatalf("%s: got %#v, want Ok 42", site, got)
		}
	}
	check("AnyTaskRun", AnyTaskRun(mk()))
	check("anyTaskInvoke", anyTaskInvoke(mk()))
	check("SkyCall (zero args)", SkyCall(mk()))
	check("sky_call (Cmd.perform shape)", sky_call(mk(), nil))
	check("RunAny", TaskCoerceT[SkyADT, int](mk()).RunAny())
	check("Task_run (typed)", any(Task_run(TaskCoerceT[SkyADT, int](mk()))))
	if ran != 6 {
		t.Fatalf("continuation ran %d times, want 6 (once per force)", ran)
	}

	// Conversions INTO a typed Task slot keep the node or wrap a thunk as
	// a leaf; none zeroes the slot.
	thunk := func() any { return Ok[any, any](42) }
	for name, v := range map[string]any{"node": AnyTaskSucceed(42), "thunk": thunk} {
		check("Coerce/"+name, AnyTaskRun(Coerce[SkyTask[SkyADT, int]](v)))
		nv := narrowReflectValue(reflect.ValueOf(v), reflect.TypeOf(SkyTask[SkyADT, int]{}))
		check("narrowReflectValue/"+name, AnyTaskRun(nv.Interface()))
		cv := coerceReflectArg(reflect.ValueOf(v), reflect.TypeOf(SkyTask[SkyADT, int]{}))
		check("coerceReflectArg/"+name, AnyTaskRun(cv.Interface()))
		sv := skyValueAsType(v, reflect.TypeOf(SkyTask[SkyADT, int]{}))
		check("skyValueAsType/"+name, AnyTaskRun(sv.Interface()))
		check("coerceInner/"+name, AnyTaskRun(coerceInner[SkyTask[SkyADT, int]](v)))
		rec := narrowReflectValue(reflect.ValueOf(map[string]any{"run": v}), reflect.TypeOf(taskHolder{}))
		check("record field/"+name, AnyTaskRun(rec.Interface().(taskHolder).Run))
		// A typed Go function taking a Task parameter, called through the
		// reflect dispatcher.
		takes := func(tk SkyTask[SkyADT, int]) any { return AnyTaskRun(tk) }
		check("skyCallDirect/"+name, SkyCall(takes, v))
	}

	// A Task inside a Sky.Live model is rejected, as a func was.
	if err := validateSessionValue(taskHolder{Run: TaskCoerceT[SkyADT, int](AnyTaskSucceed(1))}, "model"); err == nil ||
		!strings.Contains(err.Error(), "Task") {
		t.Fatalf("validateSessionValue must reject a Task field, got %v", err)
	}
}

// TestTaskLike_FallbacksPanicClassified: every place that cannot force what
// it was handed fails loudly, never returning the unforced value.
func TestTaskLike_FallbacksPanicClassified(t *testing.T) {
	expectCoercePanic(t, "zero SkyTask", func() { anyTaskInvoke(SkyTask[any, int]{}) })
	expectCoercePanic(t, "func with a parameter forced as a Task", func() {
		anyTaskInvoke(func(x any) any { return x })
	})
	expectCoercePanic(t, "AnyTaskRun of a func with a parameter", func() {
		AnyTaskRun(func(x any) any { return x })
	})
	expectCoercePanic(t, "Task applied to an argument (SkyCall)", func() { SkyCall(AnyTaskSucceed(1), 2) })
	expectCoercePanic(t, "Task applied to an argument (curried walk)", func() { skyCallOne(AnyTaskSucceed(1), 2) })
	expectCoercePanic(t, "Task handed to Task.lazy as its thunk", func() {
		anyTaskInvoke(Task_lazy(AnyTaskSucceed(1)))
	})
	expectCoercePanic(t, "andThen continuation returning a non-thunk func", func() {
		anyTaskInvoke(AnyTaskAndThen(func(_ any) any { return func(a, b any) any { return a } }, AnyTaskSucceed(1)))
	})
}

// TestTaskSemantics_Preserved pins the observable contract of every
// combinator the interpreter folds: effect order, short-circuit, error
// mapping, recovery, sequence order and first-error stop, lazy re-run.
func TestTaskSemantics_Preserved(t *testing.T) {
	var log []string
	step := func(name string, v any) any {
		return mkTask(taskLeaf, func() any { log = append(log, name); return Ok[any, any](v) }, nil, nil)
	}
	fail := func(name string, e any) any {
		return mkTask(taskLeaf, func() any { log = append(log, name); return Err[any, any](e) }, nil, nil)
	}

	// andThen order + map + mapError identity on Ok.
	log = nil
	r := anyTaskInvoke(Task_mapError(func(e any) any { return "mapped " + e.(string) },
		Task_map(func(v any) any { return v.(int) * 10 },
			AnyTaskAndThen(func(v any) any { return step("b", v.(int)+1) }, step("a", 1)))))
	if r.Tag != 0 || r.OkValue != 20 || strings.Join(log, ",") != "a,b" {
		t.Fatalf("andThen/map: %+v log %v", r, log)
	}

	// Short-circuit: nothing after an Err runs; mapError maps it; onError
	// recovers it.
	log = nil
	r = anyTaskInvoke(Task_mapError(func(e any) any { return "mapped " + e.(string) },
		AnyTaskAndThen(func(v any) any { return step("never", v) }, fail("x", "boom"))))
	if r.Tag != 1 || r.ErrValue != "mapped boom" || strings.Join(log, ",") != "x" {
		t.Fatalf("short-circuit/mapError: %+v log %v", r, log)
	}
	r = anyTaskInvoke(Task_onError(func(e any) any { return AnyTaskSucceed("recovered " + e.(string)) }, fail("y", "e1")))
	if r.Tag != 0 || r.OkValue != "recovered e1" {
		t.Fatalf("onError: %+v", r)
	}
	r = anyTaskInvoke(Task_onError(func(e any) any { return AnyTaskSucceed("unused") }, AnyTaskSucceed(7)))
	if r.Tag != 0 || r.OkValue != 7 {
		t.Fatalf("onError on Ok: %+v", r)
	}

	// sequence: in order, first error stops the rest.
	log = nil
	r = anyTaskInvoke(Task_sequence([]any{step("1", 1), step("2", 2), step("3", 3)}))
	if r.Tag != 0 || fmt.Sprint(r.OkValue) != "[1 2 3]" || strings.Join(log, ",") != "1,2,3" {
		t.Fatalf("sequence: %+v log %v", r, log)
	}
	log = nil
	r = anyTaskInvoke(Task_sequence([]any{step("1", 1), fail("2", "stop"), step("3", 3)}))
	if r.Tag != 1 || r.ErrValue != "stop" || strings.Join(log, ",") != "1,2" {
		t.Fatalf("sequence first error: %+v log %v", r, log)
	}
	r = anyTaskInvoke(Task_sequence([]any{}))
	if r.Tag != 0 || len(r.OkValue.([]any)) != 0 {
		t.Fatalf("empty sequence: %+v", r)
	}
	// A sequence nested in a sequence, and a sequence run twice (per-run
	// state must not leak between runs).
	inner := Task_sequence([]any{AnyTaskSucceed("a"), AnyTaskSucceed("b")})
	outer := Task_sequence([]any{inner, inner})
	for i := 0; i < 2; i++ {
		r = anyTaskInvoke(outer)
		if r.Tag != 0 || fmt.Sprint(r.OkValue) != "[[a b] [a b]]" {
			t.Fatalf("nested sequence run %d: %+v", i, r)
		}
	}

	// lazy re-runs its thunk on every force.
	calls := 0
	lz := Task_lazy(func(_ any) any { calls++; return calls })
	anyTaskInvoke(lz)
	r = anyTaskInvoke(lz)
	if r.OkValue != 2 || calls != 2 {
		t.Fatalf("lazy: %+v calls %d", r, calls)
	}

	// fromResult / andThenResult / Result.andThenTask.
	if r = anyTaskInvoke(Task_fromResult(Err[any, any]("e"))); r.Tag != 1 || r.ErrValue != "e" {
		t.Fatalf("fromResult Err: %+v", r)
	}
	if r = anyTaskInvoke(Task_fromResult(Ok[any, int](3))); r.Tag != 0 || r.OkValue != 3 {
		t.Fatalf("fromResult typed Ok: %+v", r)
	}
	if r = anyTaskInvoke(Task_andThenResult(func(v any) any { return Ok[any, any](v.(int) + 1) }, AnyTaskSucceed(1))); r.OkValue != 2 {
		t.Fatalf("andThenResult: %+v", r)
	}
	if r = anyTaskInvoke(Result_andThenTask(func(v any) any { return AnyTaskSucceed(v.(int) * 2) }, Ok[any, any](4))); r.OkValue != 8 {
		t.Fatalf("Result.andThenTask: %+v", r)
	}

	// A kernel thunk returning a Result of another instantiation is that
	// Result, not an Ok wrapping it.
	if r = anyTaskInvoke(func() any { return Err[string, int]("typed err") }); r.Tag != 1 || r.ErrValue != "typed err" {
		t.Fatalf("typed Result from a thunk: %+v", r)
	}
	// A bare value is Ok value (the documented kernel trust boundary).
	if r = anyTaskInvoke(5); r.Tag != 0 || r.OkValue != 5 {
		t.Fatalf("bare value: %+v", r)
	}

	// Typed companions.
	tt := Task_andThen(func(a int) SkyTask[string, int] { return Task_succeed[string, int](a + 1) }, Task_succeed[string, int](1))
	if tr := Task_run(Task_mapT(func(a int) int { return a * 3 }, tt)); tr.Tag != 0 || tr.OkValue != 6 {
		t.Fatalf("typed andThen/mapT: %+v", tr)
	}
	if tr := Task_run(Task_sequenceT([]SkyTask[string, int]{Task_succeed[string, int](1), Task_succeed[string, int](2)})); tr.Tag != 0 || fmt.Sprint(tr.OkValue) != "[1 2]" {
		t.Fatalf("typed sequenceT: %+v", tr)
	}
}

// TestTaskConcurrentRuns: one task value run from many goroutines at once.
// Nodes are immutable; per-run state (frames, sequence accumulators) is
// local to each run. Run under -race.
func TestTaskConcurrentRuns(t *testing.T) {
	task := Task_sequence([]any{
		Task_map(func(v any) any { return v.(int) + 1 }, AnyTaskSucceed(1)),
		AnyTaskAndThen(func(v any) any { return AnyTaskSucceed(v.(int) * 2) }, AnyTaskSucceed(3)),
	})
	done := make(chan string, 16)
	for i := 0; i < 16; i++ {
		go func() {
			r := anyTaskInvoke(task)
			done <- fmt.Sprint(r.Tag, r.OkValue)
		}()
	}
	for i := 0; i < 16; i++ {
		if got := <-done; got != "0 [2 6]" {
			t.Fatalf("concurrent run: %s", got)
		}
	}
}
