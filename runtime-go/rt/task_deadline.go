// task_deadline.go — Task.timeout and Task.remaining: a deadline that travels
// with a Task.
//
// `Task.timeout ms task` runs `task` under a deadline `ms` milliseconds away
// and fails with a Timeout-kind Error (domain "task", code
// "DeadlineExceeded") when the task has not finished by then. The deadline
// travels with the work:
//
//   - It lives in the goroutine-local trace context (goroutine_context.go),
//     under skyDeadlineKey, so every goSky goroutine started inside the task
//     (Task.parallel, parallelN, spawn, a nested Task.timeout) inherits it.
//   - A nested Task.timeout keeps the EARLIER deadline: an inner
//     `Task.timeout 5000` inside an outer `Task.timeout 1000` ends at the
//     outer's deadline.
//   - The kernels that wait read it: Http.get / post / request carry it as
//     the request context (the client stops at the deadline) and refuse to
//     start once it has passed; Sync.pop / popWithin stop waiting at it; and
//     Task.remaining reports the time left.
//
// A timed-out task is ABANDONED, not stopped. Go cannot stop a goroutine from
// outside, so the task keeps running in the background until its current step
// returns; its result is discarded and a later panic in it is logged. The
// deadline context is cancelled when Task.timeout returns, so an Http call or
// a Sync wait in the abandoned task ends at once; any other effect already
// running (a database query, a file write) finishes in the background.
//
// Only the Sky deadline governs. The deadline context is built on
// context.WithoutCancel of the caller's trace context: it keeps the trace
// parentage and the request id, but the end of the HTTP request that started
// the work (which cancels r.Context()) does not cancel a deadline-bound task.
//
// Under wasm (a Sky.Spa client) there is no goroutine-local store
// (task_go_ctx_js.go, rt_core_shims_js.go): each Task.timeout races its own
// timer, a nested task does not see an outer deadline, Task.remaining is
// Nothing, and the timer can only fire while the task waits (a fetch, a
// sleep), because wasm runs one goroutine at a time.

package rt

import (
	"context"
	"strconv"
	"time"
)

// skyDeadlineKey is the context key of the Sky deadline (a time.Time).
type skyDeadlineKey struct{}

// The task domain's failure codes (docs/errors/error-system.md).
const (
	taskDomain               = "task"
	taskCodeDeadlineExceeded = "DeadlineExceeded"
)

// skyDeadlineOf returns the Sky deadline a context carries.
func skyDeadlineOf(ctx context.Context) (time.Time, bool) {
	if ctx == nil {
		return time.Time{}, false
	}
	dl, ok := ctx.Value(skyDeadlineKey{}).(time.Time)
	return dl, ok
}

// currentSkyDeadline returns the Sky deadline of the calling goroutine.
func currentSkyDeadline() (time.Time, bool) {
	return skyDeadlineOf(CurrentTraceContext())
}

// skyRemaining returns the time left before the calling goroutine's deadline
// (never negative), and false when there is no deadline.
func skyRemaining() (time.Duration, bool) {
	dl, ok := currentSkyDeadline()
	if !ok {
		return 0, false
	}
	left := time.Until(dl)
	if left < 0 {
		left = 0
	}
	return left, true
}

// withSkyDeadline derives the deadline context for a Task.timeout of `d`
// from the calling goroutine's trace context. The deadline is the earlier of
// now+d and the deadline already in force.
func withSkyDeadline(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	dl := time.Now().Add(d)
	if prev, ok := skyDeadlineOf(parent); ok && prev.Before(dl) {
		dl = prev
	}
	base := context.WithValue(context.WithoutCancel(parent), skyDeadlineKey{}, dl)
	return context.WithDeadline(base, dl)
}

// deadlineExceededError is the Error every deadline-aware kernel returns once
// the deadline has passed: kind Timeout, domain "task", code
// "DeadlineExceeded", retryable (a Timeout is transient), with the operation
// that noticed it in the "op" attribute.
func deadlineExceededError(op string, timeoutMs int) any {
	f := DomainFailure{
		Domain:       taskDomain,
		Code:         taskCodeDeadlineExceeded,
		Retryable:    true,
		RetryAfterMs: -1,
		Attrs:        [][2]string{{"op", op}},
	}
	msg := op + ": the deadline passed before the task finished"
	if timeoutMs >= 0 {
		f = f.With("timeoutMs", strconv.Itoa(timeoutMs))
		msg = op + ": the task did not finish within " + strconv.Itoa(timeoutMs) + " ms"
	}
	return ErrDomain(errKindTimeout, msg, f)
}

// Task_timeout : Int -> Task Error a -> Task Error a
//
// Runs `task` on a goSky goroutine (so it keeps the caller's session stamp,
// trace context and SSR-settle guard) stamped with the deadline context, and
// waits for the earlier of its result and the deadline. A branch panic is
// re-raised on the caller, as Task.parallel does; a panic after the deadline
// is logged. A negative `ms` counts as 0. A deadline that has already passed
// fails at once without starting the task.
func Task_timeout(msArg any, task any) any {
	return func() any {
		ms := AsInt(msArg)
		if ms < 0 {
			ms = 0
		}
		ctx, cancel := withSkyDeadline(CurrentTraceContext(), time.Duration(ms)*time.Millisecond)
		defer cancel()
		if ctx.Err() != nil {
			return Err[any, any](deadlineExceededError("Task.timeout", ms))
		}
		c := newParallelCollector("Task.timeout", 1)
		gc := captureGoCtx().withTrace(ctx)
		goSkyFrom(gc, "Task.timeout", func() {
			r := forceTask(task)
			c.send(parallelItem{tag: r.Tag, ok: r.OkValue, err: r.ErrValue})
		}, func(p *skyPanic) { c.sendPanic(0, p) })
		var it parallelItem
		select {
		case it = <-c.ch:
		case <-ctx.Done():
			// A result that arrived together with the deadline wins.
			select {
			case it = <-c.ch:
			default:
				c.finish()
				return Err[any, any](deadlineExceededError("Task.timeout", ms))
			}
		}
		c.finish()
		if it.panicked != nil {
			reraiseSkyPanic(it.panicked)
		}
		if it.tag != 0 {
			return Err[any, any](it.err)
		}
		return Ok[any, any](it.ok)
	}
}

// Task_remaining : Task e (Maybe Int)
//
// The milliseconds left before the current deadline (0 once it has passed),
// or Nothing when no Task.timeout is in force. Always Nothing in a Sky.Spa
// client.
func Task_remaining() any {
	return func() any {
		left, ok := skyRemaining()
		if !ok {
			return Ok[any, any](Nothing[any]())
		}
		return Ok[any, any](Just[any](int(left / time.Millisecond)))
	}
}
