//go:build !js

package rt

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"sky-app/rt/telemetry"
)

// panickyBranch is a Task thunk that panics with a classified message. The
// function name is what the carried stack must show.
func panickyBranchForGoSkyTest(marker string) any {
	return func() any {
		panic("rt.IntDiv: integer division by zero (" + marker + ")")
	}
}

// recoverFrom runs fn and returns what it panicked with (nil when it did not).
func recoverFrom(fn func()) (rec any) {
	defer func() { rec = recover() }()
	fn()
	return nil
}

// TestTaskParallel_BranchPanicIsReRaisedOnCaller is the C-1 regression. A
// panic in a Task.parallel / Task.parallelN branch ran on a goroutine with no
// recover, so it killed the whole process: a server's per-request recovery
// never saw it. The branch panic must now reach the CALLER's goroutine, keep
// its message (so the classifier still names it) and carry the branch's own
// stack. Before the fix this test does not fail: it crashes the test binary.
func TestTaskParallel_BranchPanicIsReRaisedOnCaller(t *testing.T) {
	cases := map[string]func(tasks []any) any{
		"parallel":  func(tasks []any) any { return Task_parallel(tasks) },
		"parallelN": func(tasks []any) any { return Task_parallelN(2, tasks) },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			marker := fmt.Sprintf("par-branch-%s-%d", name, time.Now().UnixNano())
			tasks := []any{
				makeOkTask(1, 20*time.Millisecond, nil, nil),
				panickyBranchForGoSkyTest(marker),
				makeOkTask(3, 0, nil, nil),
			}
			rec := recoverFrom(func() { anyTaskInvoke(mk(tasks)) })
			if rec == nil {
				t.Fatal("the branch panic did not reach the caller")
			}
			msg := fmt.Sprintf("%v", rec)
			if !strings.Contains(msg, marker) {
				t.Fatalf("re-raised panic %q lost the original message", msg)
			}
			if kind, _ := classifyPanic(msg); kind != "DivisionByZero" {
				t.Fatalf("re-raised panic classifies as %q, want DivisionByZero", kind)
			}
			stack := string(withPanicOrigin(rec, []byte("CALLER-STACK")))
			if !strings.Contains(stack, "panickyBranchForGoSkyTest") {
				t.Fatalf("the carried stack does not show the branch frame:\n%s", stack)
			}
			if !strings.Contains(stack, "CALLER-STACK") {
				t.Fatalf("the caller stack is lost from the combined frame:\n%s", stack)
			}
		})
	}
}

// TestTaskParallel_NestedPanicKeepsTheInnermostStack: a panic re-raised by an
// inner Task.parallel and caught by an outer one is not wrapped twice, so the
// frame logged at the top is the one that panicked.
func TestTaskParallel_NestedPanicKeepsTheInnermostStack(t *testing.T) {
	marker := fmt.Sprintf("nested-%d", time.Now().UnixNano())
	inner := Task_parallel([]any{panickyBranchForGoSkyTest(marker)})
	rec := recoverFrom(func() { anyTaskInvoke(Task_parallel([]any{inner})) })
	p, ok := rec.(*skyPanic)
	if !ok {
		t.Fatalf("want a *skyPanic at the top, got %T", rec)
	}
	if _, nested := p.value.(*skyPanic); nested {
		t.Fatal("a re-raised panic was wrapped a second time")
	}
	if !strings.Contains(string(p.stack), "panickyBranchForGoSkyTest") {
		t.Fatalf("the outer panic lost the innermost frame:\n%s", p.stack)
	}
}

// parallelLateLogs returns logged Task.parallel panics carrying marker.
func parallelLateLogs(marker string) []telemetry.LogEntry {
	var out []telemetry.LogEntry
	for _, e := range telemetry.Default().RecentLogs(0) {
		if strings.Contains(e.Message, "Task.parallel") && strings.Contains(e.Fields["panicMsg"], marker) {
			out = append(out, e)
		}
	}
	return out
}

// TestTaskParallel_LatePanicIsLogged: a branch that panics AFTER the caller
// has already returned on another branch's Err cannot be re-raised. It must be
// logged through the classified panic path, never dropped, never a crash.
func TestTaskParallel_LatePanicIsLogged(t *testing.T) {
	marker := fmt.Sprintf("late-%d", time.Now().UnixNano())
	release := make(chan struct{})
	late := func() any {
		<-release
		panic("rt.IntDiv: integer division by zero (" + marker + ")")
	}
	r := anyTaskInvoke(Task_parallel([]any{makeErrTask("first", 0, nil, nil), late}))
	if r.Tag == 0 {
		t.Fatalf("want the first branch's Err, got %+v", r)
	}
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got := parallelLateLogs(marker); len(got) == 1 {
			if got[0].Fields["panicKind"] != "DivisionByZero" {
				t.Fatalf("late panic class = %q", got[0].Fields["panicKind"])
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the late branch panic was never logged (%d entries)", len(parallelLateLogs(marker)))
}

// TestGoSky_CarriesGoroutineLocalContext: a Task.parallel branch runs on a new
// goroutine, and the runtime keeps three things goroutine-local: the Sky.Live
// session stamp (ownership of processes, streams, sockets), the trace context
// (spans, the request id, the session token) and the SSR-settle guard (a GET
// must not mutate). A branch that lost them spawned unowned processes, logged
// without a trace, and ran destructive writes during a server-side render.
func TestGoSky_CarriesGoroutineLocalContext(t *testing.T) {
	sess := &liveSession{}
	type seen struct {
		sess   *liveSession
		reqID  string
		settle bool
	}
	probe := func() any {
		return Ok[any, any](seen{
			sess:   currentLiveSession(),
			reqID:  CurrentRequestID(),
			settle: InSsrSettle(),
		})
	}
	var r SkyResult[any, any]
	runWithLiveSession(sess, func() {
		RunWithTraceContext(WithRequestID(context.Background(), "req-gosky-1"), func() {
			enterSsrSettle()
			defer exitSsrSettle()
			r = anyTaskInvoke(Task_parallel([]any{probe, probe}))
		})
	})
	if r.Tag != 0 {
		t.Fatalf("parallel failed: %+v", r)
	}
	for i, v := range r.OkValue.([]any) {
		s := v.(seen)
		if s.sess != sess {
			t.Errorf("branch %d: session stamp lost", i)
		}
		if s.reqID != "req-gosky-1" {
			t.Errorf("branch %d: trace context lost (request id %q)", i, s.reqID)
		}
		if !s.settle {
			t.Errorf("branch %d: SSR-settle guard lost, a write would run during a GET render", i)
		}
	}
	// And a destructive kernel in a branch self-suppresses.
	w := anyTaskInvoke(Task_parallel([]any{func() any {
		if r := ssrSuppressedWrite("test.write"); r != nil {
			return r
		}
		return Ok[any, any]("WROTE")
	}}))
	if w.Tag != 0 {
		t.Fatalf("outside a settle the write must run: %+v", w)
	}
}

// TestGoSky_ClearsStampsWhenDone: the goroutine-local maps are keyed by
// goroutine id; a goSky goroutine must remove its entries when it ends, or
// every branch leaks one entry forever.
func TestGoSky_ClearsStampsWhenDone(t *testing.T) {
	sess := &liveSession{}
	before := goroutineCtxSize()
	runWithLiveSession(sess, func() {
		RunWithTraceContext(WithRequestID(context.Background(), "req-gosky-2"), func() {
			for i := 0; i < 50; i++ {
				done := make(chan struct{})
				goSky("test", func() { close(done) })
				<-done
			}
		})
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && goroutineCtxSize() > before {
		time.Sleep(5 * time.Millisecond)
	}
	if n := goroutineCtxSize(); n > before {
		t.Fatalf("trace stamps leaked: %d before, %d after", before, n)
	}
	n := 0
	liveSessionByGoroutine.Range(func(_, v any) bool {
		if v == sess {
			n++
		}
		return true
	})
	if n != 0 {
		t.Fatalf("%d session stamps leaked", n)
	}
}
