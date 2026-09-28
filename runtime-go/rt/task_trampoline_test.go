package rt

import (
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
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
	avg := func(xs []uint64) float64 {
		var sum float64
		for _, x := range xs {
			sum += float64(x)
		}
		return sum / float64(len(xs))
	}
	first, second := avg(s[:half]), avg(s[half:])
	const slack = 4 << 20
	t.Logf("%s: %d iterations, %d samples, heap first-half %.0f B, second-half %.0f B, min %d, max %d",
		what, iterations, len(samples), first, second, minU(s), maxU(s))
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
