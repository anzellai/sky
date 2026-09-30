// task_go.go — goSky: the one way the runtime starts a goroutine that runs
// Sky code.
//
// A bare `go func() { … sky_call … }()` has two defects, and every one of the
// runtime's fan-out sites had at least one of them:
//
//  1. No panic net. A classified panic (division by zero, a CoerceFailure, a
//     JsonEncodeFailure) on a goroutine with no `recover` kills the whole
//     process. The per-request recovery in Sky.Http.Server and Sky.Live runs on
//     the REQUEST goroutine and never sees it. `Task.parallel` was the worst
//     case (C-1): one bad branch took a live server down.
//
//  2. Lost goroutine-local context. The runtime keeps three things keyed by
//     goroutine id: the Sky.Live session stamp (which session owns a spawned
//     process, stream or socket), the trace context (spans, request id, the
//     session token a rotation needs) and the SSR-settle guard (a GET render
//     must not run a destructive write). A new goroutine starts with none of
//     them, so a branch spawned unowned processes, logged without a trace and
//     could write during a server-side render.
//
// goSky fixes both in one place:
//
//	goSky(context, fn)               fire and forget; a panic is LOGGED
//	                                 classified (as Task.spawn always did)
//	goSkyWith(context, fn, onPanic)  a panic is handed to onPanic as a
//	                                 *skyPanic, on the new goroutine; the
//	                                 waiting caller re-raises it with
//	                                 reraiseSkyPanic, or logs it with
//	                                 logSkyPanic when nobody waits any more
//
// A *skyPanic carries the ORIGINAL recovered value and the stack of the
// goroutine that panicked. Its message is the original message, so
// classifyPanic still names the class after a re-raise, and the panic loggers
// (panic_log.go) print the original frame first, then the frame it was
// re-raised on (withPanicOrigin). A panic re-raised by an inner goSky and
// caught by an outer one is never wrapped twice.

package rt

import "fmt"

// skyPanic is a panic recovered on a goSky goroutine, carried to the goroutine
// that re-raises or logs it.
type skyPanic struct {
	value any    // the original recovered value
	stack []byte // the stack of the goroutine that panicked
}

// Error returns the original panic's message, so `fmt.Sprintf("%v", p)` and
// classifyPanic see exactly what they would have seen without the carrier.
func (p *skyPanic) Error() string { return fmt.Sprint(p.value) }

// Unwrap exposes an original error value to errors.Is / errors.As.
func (p *skyPanic) Unwrap() error {
	if e, ok := p.value.(error); ok {
		return e
	}
	return nil
}

// asSkyPanic wraps a value recovered on a goSky goroutine. It must be called
// from the deferred function that recovered, while the panicking frames are
// still on the stack, so the captured stack shows where the panic happened. A
// value that is already a *skyPanic (re-raised by an inner goSky) is returned
// unchanged, keeping the innermost stack.
func asSkyPanic(r any) *skyPanic {
	if p, ok := r.(*skyPanic); ok {
		return p
	}
	return &skyPanic{value: r, stack: skyPanicStack()}
}

// panicValue returns the original value behind a carried panic (for a `%T`
// in a log line), or r itself.
func panicValue(r any) any {
	if p, ok := r.(*skyPanic); ok {
		return p.value
	}
	return r
}

// withPanicOrigin returns the stack a panic logger should print for r. For a
// carried panic that is the ORIGINAL goroutine's stack followed by the stack
// it was re-raised on; for any other value it is `stack` unchanged.
func withPanicOrigin(r any, stack []byte) []byte {
	p, ok := r.(*skyPanic)
	if !ok || len(p.stack) == 0 {
		return stack
	}
	out := make([]byte, 0, len(p.stack)+len(stack)+64)
	out = append(out, p.stack...)
	out = append(out, "\n[re-raised on the waiting goroutine]\n"...)
	out = append(out, stack...)
	return out
}

// reraiseSkyPanic raises a carried panic on the calling goroutine (the one
// that waited for the branch), where the caller's own recovery sees it.
func reraiseSkyPanic(p *skyPanic) { panic(p) }

// logSkyPanic logs a carried panic that nobody waits for any more (a branch
// that panicked after its caller already returned), through the classified
// panic log. The process keeps running.
func logSkyPanic(context string, p *skyPanic) {
	logClassifiedPanic("sky.task", context, p)
}

// goSky starts fn on a new goroutine that carries the caller's goroutine-local
// context (Live session stamp, trace context, SSR-settle guard). A panic in fn
// is recovered and logged classified under `context`; it never ends the
// process.
func goSky(context string, fn func()) {
	goSkyWith(context, fn, nil)
}

// goSkyWith is goSky with a panic hand-off. When fn panics, onPanic receives
// the carried panic on the new goroutine; the goroutine that waits for fn
// re-raises it (reraiseSkyPanic). A nil onPanic logs it (logSkyPanic). A panic
// inside onPanic itself is logged, never propagated.
func goSkyWith(context string, fn func(), onPanic func(*skyPanic)) {
	gc := captureGoCtx()
	go gc.run(func() {
		defer func() {
			r := recover()
			if r == nil {
				return
			}
			p := asSkyPanic(r)
			if onPanic == nil {
				logSkyPanic(context, p)
				return
			}
			defer func() {
				if r2 := recover(); r2 != nil {
					logSkyPanic(context, asSkyPanic(r2))
				}
			}()
			onPanic(p)
		}()
		fn()
	})
}
