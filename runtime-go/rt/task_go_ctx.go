//go:build !js

package rt

import "context"

// goCtx is the goroutine-local context a goSky goroutine inherits from the
// goroutine that started it (task_go.go).
type goCtx struct {
	sess   *liveSession    // Sky.Live session stamp (live_session_ctx.go)
	trace  context.Context // trace context, request id, session token
	settle *ssrSettleState // SSR-settle guard (spa_ssr_safe.go)
}

// captureGoCtx reads the calling goroutine's stamps.
func captureGoCtx() goCtx {
	gc := goCtx{sess: currentLiveSession()}
	if v, ok := goroutineCtx.Load(currentGoroutineID()); ok {
		if ctx, ok := v.(context.Context); ok {
			gc.trace = ctx
		}
	}
	if v, ok := ssrSettleGoroutines.Load(currentGoroutineID()); ok {
		gc.settle, _ = v.(*ssrSettleState)
	}
	return gc
}

// run stamps the calling (new) goroutine with the captured context, runs fn,
// and removes every stamp it set, so no goroutine-id entry outlives it.
func (gc goCtx) run(fn func()) {
	if gc.sess != nil {
		setGoroutineLiveSession(gc.sess)
		defer clearGoroutineLiveSession()
	}
	if gc.trace != nil {
		SetGoroutineTraceContext(gc.trace)
		defer ClearGoroutineTraceContext()
	}
	if gc.settle != nil {
		// The branch shares the settle's state record: a write it
		// suppresses marks the whole settle incomplete.
		ssrSettleGoroutines.Store(currentGoroutineID(), gc.settle)
		defer exitSsrSettle()
	}
	fn()
}

// skyPanicStack captures the stack of the goroutine that panicked. The one
// capture point is capturePanicStack (panic_log.go).
func skyPanicStack() []byte { return capturePanicStack() }
