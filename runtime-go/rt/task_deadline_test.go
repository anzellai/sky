//go:build !js

package rt

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// deadlineFailure asserts that a Task result is the deadline's error (kind
// Timeout, domain "task", code "DeadlineExceeded", retryable) and returns its
// failure.
func deadlineFailure(t *testing.T, r SkyResult[any, any]) DomainFailure {
	t.Helper()
	if r.Tag == 0 {
		t.Fatalf("want Err DeadlineExceeded, got Ok %v", r.OkValue)
	}
	if tag, ok := errorKindTagOf(r.ErrValue); !ok || tag != errKindTimeout {
		t.Fatalf("want kind Timeout, got tag %d (%v)", tag, ok)
	}
	f, ok := DomainOf(r.ErrValue)
	if !ok {
		t.Fatalf("the error carries no domain failure: %v", r.ErrValue)
	}
	if f.Domain != "task" || f.Code != "DeadlineExceeded" || !f.Retryable {
		t.Fatalf("want task/DeadlineExceeded retryable, got %+v", f)
	}
	return f
}

// remainingProbe is a Task that reports Task.remaining (-1 for Nothing).
func remainingProbe() any {
	return func() any {
		m := anyTaskInvoke(Task_remaining()).OkValue
		if v, ok := maybeJustInt(m); ok {
			return Ok[any, any](v)
		}
		return Ok[any, any](-1)
	}
}

func maybeJustInt(m any) (int, bool) {
	mb, ok := m.(SkyMaybe[any])
	if !ok || mb.Tag != 0 {
		return 0, false
	}
	return AsInt(mb.JustValue), true
}

// withPastDeadline runs fn on a goroutine stamped with a Sky deadline `d`
// from now (negative: already passed).
func withDeadlineIn(d time.Duration, fn func()) {
	ctx := context.WithValue(context.Background(), skyDeadlineKey{}, time.Now().Add(d))
	RunWithTraceContext(ctx, fn)
}

func TestTaskTimeout_FiresWithDeadlineExceeded(t *testing.T) {
	start := time.Now()
	r := anyTaskInvoke(Task_timeout(50, makeOkTask("late", 3*time.Second, nil, nil)))
	if el := time.Since(start); el > time.Second {
		t.Fatalf("Task.timeout 50 returned after %v", el)
	}
	f := deadlineFailure(t, r)
	if v, _ := f.Attr("timeoutMs"); v != "50" {
		t.Fatalf("timeoutMs attribute = %q, want 50", v)
	}
	if v, _ := f.Attr("op"); v != "Task.timeout" {
		t.Fatalf("op attribute = %q, want Task.timeout", v)
	}
}

func TestTaskTimeout_ResultInTimeIsReturned(t *testing.T) {
	r := anyTaskInvoke(Task_timeout(2000, makeOkTask(7, 0, nil, nil)))
	if r.Tag != 0 || r.OkValue != 7 {
		t.Fatalf("want Ok 7, got %+v", r)
	}
	e := anyTaskInvoke(Task_timeout(2000, makeErrTask("boom", 0, nil, nil)))
	if e.Tag == 0 || e.ErrValue != "boom" {
		t.Fatalf("an Err in time must pass through unchanged, got %+v", e)
	}
}

func TestTaskTimeout_PassedDeadlineDoesNotStartTheTask(t *testing.T) {
	var started atomic.Int32
	var r SkyResult[any, any]
	withDeadlineIn(-time.Second, func() {
		r = anyTaskInvoke(Task_timeout(5000, makeOkTask(1, 0, &started, nil)))
	})
	deadlineFailure(t, r)
	time.Sleep(20 * time.Millisecond)
	if started.Load() != 0 {
		t.Fatal("a task whose deadline had already passed was started")
	}
}

// The deadline travels: an inner Task.timeout keeps the earlier outer
// deadline, and Task.remaining inside sees it.
func TestTaskTimeout_NestedMinimumWins(t *testing.T) {
	start := time.Now()
	r := anyTaskInvoke(Task_timeout(300, Task_timeout(10000, remainingProbe())))
	if r.Tag != 0 {
		t.Fatalf("probe failed: %+v", r)
	}
	if left := AsInt(r.OkValue); left < 0 || left > 300 {
		t.Fatalf("inner Task.remaining = %d, want 0..300 (the outer deadline)", left)
	}
	// And the inner timeout fires at the outer deadline.
	start = time.Now()
	r = anyTaskInvoke(Task_timeout(10000, Task_timeout(60, makeOkTask(1, 3*time.Second, nil, nil))))
	if el := time.Since(start); el > time.Second {
		t.Fatalf("nested timeout returned after %v", el)
	}
	deadlineFailure(t, r)
	r = anyTaskInvoke(Task_timeout(60, Task_timeout(10000, makeOkTask(1, 3*time.Second, nil, nil))))
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("outer-bound nested timeout returned after %v", el)
	}
	deadlineFailure(t, r)
}

func TestTaskTimeout_ParallelBranchSeesTheDeadline(t *testing.T) {
	r := anyTaskInvoke(Task_timeout(1000, Task_parallel([]any{remainingProbe(), remainingProbe()})))
	if r.Tag != 0 {
		t.Fatalf("parallel failed: %+v", r)
	}
	for i, v := range r.OkValue.([]any) {
		if left := AsInt(v); left <= 0 || left > 1000 {
			t.Fatalf("branch %d: Task.remaining = %d, want 1..1000", i, left)
		}
	}
}

func TestTaskRemaining_NothingWithoutADeadline(t *testing.T) {
	r := anyTaskInvoke(remainingProbe())
	if r.OkValue != -1 {
		t.Fatalf("Task.remaining outside Task.timeout = %v, want Nothing", r.OkValue)
	}
}

func TestTaskTimeout_BranchPanicIsReRaisedOnCaller(t *testing.T) {
	marker := fmt.Sprintf("timeout-branch-%d", time.Now().UnixNano())
	rec := recoverFrom(func() { anyTaskInvoke(Task_timeout(2000, panickyBranchForGoSkyTest(marker))) })
	if rec == nil {
		t.Fatal("the task panic did not reach the caller")
	}
	if !strings.Contains(fmt.Sprintf("%v", rec), marker) {
		t.Fatalf("re-raised panic lost its message: %v", rec)
	}
}

// slowServer answers after `wait`, and reports on `cancelled` when the client
// gave up on the request first.
func slowServer(t *testing.T, wait time.Duration, cancelled chan<- struct{}, hits *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// Read the body: net/http notices a closed connection (and cancels
		// r.Context()) only once the request body has been consumed.
		_, _ = io.ReadAll(r.Body)
		select {
		case <-r.Context().Done():
			select {
			case cancelled <- struct{}{}:
			default:
			}
		case <-time.After(wait):
			_, _ = w.Write([]byte("slow"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Found while adding Task.timeout: Http.request sent its request with
// context.Background(), so it carried neither the trace context nor any
// cancellation. A deadline-bound Http.request ran on in the background until
// the client timeout. The request must now stop at the deadline.
func TestHttp_CallsAreCancelledAtTheDeadline(t *testing.T) {
	calls := map[string]func(url string) any{
		"get":  func(u string) any { return Http_get(u) },
		"post": func(u string) any { return Http_post(u, "{}") },
		"request": func(u string) any {
			return Http_request(map[string]any{
				"method": "GET", "url": u, "body": "", "headers": []any{},
				"timeout": 30000, "followRedirects": true, "maxRedirects": 10,
			})
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			cancelled := make(chan struct{}, 1)
			var hits atomic.Int32
			srv := slowServer(t, 5*time.Second, cancelled, &hits)
			start := time.Now()
			r := anyTaskInvoke(Task_timeout(150, call(srv.URL)))
			if el := time.Since(start); el > 2*time.Second {
				t.Fatalf("Http.%s under Task.timeout 150 returned after %v", name, el)
			}
			deadlineFailure(t, r)
			select {
			case <-cancelled:
			case <-time.After(2 * time.Second):
				t.Fatalf("Http.%s: the server never saw the request cancelled", name)
			}
		})
	}
}

func TestHttp_RefusesBeforeSending(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	var hits atomic.Int32
	srv := slowServer(t, 0, cancelled, &hits)

	// Less time left than withMinRemaining asks for: DeadlineTooShort.
	req := map[string]any{
		"method": "GET", "url": srv.URL, "body": "", "headers": []any{},
		"timeout": 30000, "followRedirects": true, "maxRedirects": 10,
		"minRemaining": 5000,
	}
	r := anyTaskInvoke(Task_timeout(1000, Http_request(req)))
	if r.Tag == 0 {
		t.Fatalf("want a refusal, got Ok %v", r.OkValue)
	}
	f, ok := DomainOf(r.ErrValue)
	if !ok || f.Domain != "http" || f.Code != "DeadlineTooShort" || f.Retryable {
		t.Fatalf("want http/DeadlineTooShort not retryable, got %+v (%v)", f, ok)
	}
	if tag, _ := errorKindTagOf(r.ErrValue); tag != errKindTimeout {
		t.Fatalf("want kind Timeout, got %d", tag)
	}
	if v, _ := f.Attr("minRemainingMs"); v != "5000" {
		t.Fatalf("minRemainingMs = %q", v)
	}

	// Enough time left: it is sent.
	req["minRemaining"] = 100
	if r := anyTaskInvoke(Task_timeout(5000, Http_request(req))); r.Tag != 0 {
		t.Fatalf("a call with enough time left was refused: %+v", r)
	}

	// The default refuses only once the deadline has passed.
	before := hits.Load()
	for name, call := range map[string]func() any{
		"get":     func() any { return Http_get(srv.URL) },
		"post":    func() any { return Http_post(srv.URL, "{}") },
		"request": func() any { delete(req, "minRemaining"); return Http_request(req) },
	} {
		var r SkyResult[any, any]
		withDeadlineIn(-time.Millisecond, func() { r = anyTaskInvoke(call()) })
		f := deadlineFailure(t, r)
		if !strings.HasPrefix(func() string { v, _ := f.Attr("op"); return v }(), "Http.") {
			t.Fatalf("%s: op attribute %+v", name, f)
		}
	}
	if hits.Load() != before {
		t.Fatalf("a refused call reached the server (%d requests)", hits.Load()-before)
	}
}

func TestSyncPop_StopsAtTheDeadline(t *testing.T) {
	q := anyTaskInvoke(Sync_newQueue(1)).OkValue
	for name, pop := range map[string]func() any{
		"popWithin": func() any { return Sync_popWithin(10000, q) },
		"pop":       func() any { return Sync_pop(q) },
	} {
		start := time.Now()
		var r SkyResult[any, any]
		withDeadlineIn(80*time.Millisecond, func() { r = anyTaskInvoke(pop()) })
		if el := time.Since(start); el > 2*time.Second {
			t.Fatalf("Sync.%s under an 80 ms deadline returned after %v", name, el)
		}
		f := deadlineFailure(t, r)
		if v, _ := f.Attr("op"); v != "Sync."+name {
			t.Fatalf("op attribute = %q", v)
		}
	}
	// A shorter own timeout still wins with its own Timeout error.
	var r SkyResult[any, any]
	withDeadlineIn(5*time.Second, func() { r = anyTaskInvoke(Sync_popWithin(20, q)) })
	if _, ok := DomainOf(r.ErrValue); r.Tag == 0 || ok {
		t.Fatalf("want popWithin's own Timeout, got %+v", r)
	}
}

// No goroutine-local entry outlives a Task.timeout, whether the task finished
// in time or was abandoned at the deadline.
func TestTaskTimeout_LeavesNoGoroutineContext(t *testing.T) {
	RunWithTraceContext(WithRequestID(context.Background(), "req-timeout-leak"), func() {
		for i := 0; i < 20; i++ {
			anyTaskInvoke(Task_timeout(1000, makeOkTask(i, 0, nil, nil)))
			anyTaskInvoke(Task_timeout(5, makeOkTask(i, 40*time.Millisecond, nil, nil)))
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && stampsWithRequestID("req-timeout-leak") != 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if n := stampsWithRequestID("req-timeout-leak"); n != 0 {
		t.Fatalf("%d trace stamps leaked", n)
	}
}
