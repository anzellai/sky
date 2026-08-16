// live_session_ctx.go — goroutine-local liveSession lookup
// (Cycle 4 HS prereq).
//
// HTTP streaming kernels (Sky.Core.Http.Stream.open / close) need
// to register/unregister their stream handle on the CURRENT session,
// so cleanup on session disconnect can sweep every owned stream.
// But the Sky FFI surface is `func(any) any` — there's no session
// pointer in the signature, and threading one through would be a
// sweeping breaking change.
//
// Solution mirrors the goroutine-local trace context (see
// goroutine_context.go for the architecture justification): the
// dispatch path stamps the session pointer on the goroutine before
// invoking any user code (update / Cmd.perform / subscriber
// callbacks), and kernel code that needs the session reads it back
// via currentLiveSession().
//
// A nil return is the natural "no live session in scope" answer —
// HttpStream_open invoked from a top-level Task.run (CLI use) OR
// from a unit test simply sees nil and the stream becomes
// caller-owned (no auto-cleanup).

package rt

import "sync"

// liveSessionByGoroutine — sync.Map keyed by goroutine id, value
// is *liveSession. Parallel storage to goroutineCtx; see
// goroutine_context.go for the rationale.
var liveSessionByGoroutine sync.Map // map[int64]*liveSession

// currentLiveSession returns the *liveSession stamped on the
// calling goroutine, or nil when none is set.
func currentLiveSession() *liveSession {
	gid := currentGoroutineID()
	if v, ok := liveSessionByGoroutine.Load(gid); ok {
		if sess, ok := v.(*liveSession); ok {
			return sess
		}
	}
	return nil
}

// setGoroutineLiveSession stamps the calling goroutine with a
// *liveSession. Pairs with `defer clearGoroutineLiveSession()`.
// A nil sess clears the stamp.
func setGoroutineLiveSession(sess *liveSession) {
	gid := currentGoroutineID()
	if sess == nil {
		liveSessionByGoroutine.Delete(gid)
		return
	}
	liveSessionByGoroutine.Store(gid, sess)
}

// clearGoroutineLiveSession removes the calling goroutine's stamp.
func clearGoroutineLiveSession() {
	gid := currentGoroutineID()
	liveSessionByGoroutine.Delete(gid)
}

// runWithLiveSession is the canonical pattern. Equivalent to:
//
//	setGoroutineLiveSession(sess)
//	defer clearGoroutineLiveSession()
//	fn()
//
// Encapsulating the pair makes it impossible to forget the defer.
// A nil sess degrades to running fn() with no stamp (no-op
// propagation) — matches RunWithTraceContext's nil-ctx behaviour.
func runWithLiveSession(sess *liveSession, fn func()) {
	if sess != nil {
		setGoroutineLiveSession(sess)
		defer clearGoroutineLiveSession()
	}
	fn()
}
