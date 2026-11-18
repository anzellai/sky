//go:build !js

package rt

// task_deadline_outbound.go — the Task.timeout deadline on every outbound
// network call a Sky program makes (task_deadline.go has the deadline itself).
//
// Every kernel that sends an HTTP request, dials a socket, or waits for data
// from one follows the same three rules:
//
//   - Refuse before sending once the deadline has passed (skyDeadlineRefusal,
//     or httpDeadlineRefusal with Http.withMinRemaining's minimum when the
//     request carries one). A refused call never reaches the network.
//   - Send a unary call (request, answer) with skyCallContext: the calling
//     goroutine's trace context, without its cancellation, bounded by the
//     call's own timeout AND the deadline. The call stops at the deadline.
//   - Bound only the OPEN of a stream or a socket by the deadline
//     (skyCancelAtDeadline): the stream itself outlives the task that opened
//     it, so a `Task.timeout 5000 (Http.Stream.open req)` waits at most 5 s
//     for the answer to start and then streams for as long as it runs. Each
//     READ of a stream or a socket stops at the READER's deadline
//     (skyDeadlineTimer): `Http.Stream.forEachChunk`, `WebSocket.receive` /
//     `receiveWithin` / `forEachMessage`, `Ai_streamNext`.
//
// A failure after the deadline is reported as the deadline's DeadlineExceeded
// error (skyDeadlinePassed), not as the transport error the cancellation
// caused, so the program sees one error for one cause.
//
// Not bound: the start of a shared local AI engine and the downloads that
// provision it (a model pull, an engine fetch). They are shared by every
// caller and cached, so one caller's deadline must not cancel them; the
// caller's Task.timeout still stops WAITING for them.

import (
	"context"
	"time"
)

// skyCallContext is the context of one outbound call: the calling goroutine's
// trace context (span parentage, request id) without its cancellation,
// bounded by `timeout` (none when timeout <= 0) and by the Sky deadline when
// one is in force. It replaces context.WithTimeout(context.Background(), d)
// in a kernel that runs on the Task's goroutine.
func skyCallContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx := context.WithoutCancel(CurrentTraceContext())
	dl, ok := currentSkyDeadline()
	if timeout > 0 {
		if own := time.Now().Add(timeout); !ok || own.Before(dl) {
			dl, ok = own, true
		}
	}
	if ok {
		return context.WithDeadline(ctx, dl)
	}
	return context.WithCancel(ctx)
}

// skyDeadlineRefusal refuses a call before it is sent once the Sky deadline
// has passed. nil means send it.
func skyDeadlineRefusal(op string) any {
	return httpDeadlineRefusal(op, 0)
}

// skyDeadlinePassed is httpDeadlinePassed under its general name: the
// deadline's error when the calling goroutine's deadline has passed, else nil.
func skyDeadlinePassed(op string) any {
	return httpDeadlinePassed(op)
}

// aiDeadlineTaskErr is aiTaskErr for a Std.Ai call that may have been stopped
// by the deadline: the deadline's error once it has passed, else the AI
// failure.
func aiDeadlineTaskErr(op string, err error) any {
	if r := httpDeadlinePassed(op); r != nil {
		return r
	}
	return aiTaskErr(err)
}

// skyCancelAtDeadline arms `cancel` to run at the calling goroutine's Sky
// deadline, for the open of a stream or a socket. Call the returned disarm
// once the open has finished: the stream then lives on without the deadline.
// With no deadline in force it arms nothing.
func skyCancelAtDeadline(cancel context.CancelFunc) (disarm func()) {
	left, ok := skyRemaining()
	if !ok {
		return func() {}
	}
	t := time.AfterFunc(left, cancel)
	return func() { t.Stop() }
}

// skyDeadlineTimer is a channel that fires at the calling goroutine's Sky
// deadline, for a read that waits on a select, and its stop function. With no
// deadline in force the channel is nil (never fires).
func skyDeadlineTimer() (<-chan time.Time, func()) {
	left, ok := skyRemaining()
	if !ok {
		return nil, func() {}
	}
	t := time.NewTimer(left)
	return t.C, func() { t.Stop() }
}
