// live_revocation.go — the Sky.Live side of PULL-model revocation: the
// session<->user binding, the in-funnel access gate, and eviction.
//
// See auth_revocation.go for the shared-state model + the admin write APIs and
// the Go verdict helper (authAccessState). This file is the enforcement point:
//
//   - Live.bindSessionUser (Live_bindSessionUser) binds the current session to
//     an application user at auth time, so the gate has a subject to check. For
//     sliding-auth (token) apps the binding is auto-stamped from the verified
//     token `sub` (see AuthSlidingMiddleware) and the app never has to remember.
//
//   - accessGateBlocks is called at the TOP of app.dispatch (the single funnel
//     every mutation path routes through: handleEvent, Cmd.perform completion,
//     Time.every ticks, pub/sub + stream subscribers) AND at handleInitial. A
//     Revoked/Disabled verdict EVICTS the session (markDone → every goroutine
//     retires; store.Delete → the blob is gone) and blocks the dispatch, so a
//     revoked session mutates NOTHING and the browser gets session-lost.
//
// The gate keys off sess.userID (the canonicalSub bound above) and reads the
// verdict FRESH from the shared Db every eval (or a ≤TTL per-replica cache),
// never from the session blob — so revoking on replica A stops a dispatch on
// replica B.

package rt

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"time"
)

// ─── auto-bind request-context channel (token apps) ─────────────────

type autoBindSubKeyT struct{}

// autoBindSubKey carries a verified token subject from the auth middleware to
// the session handlers, so a token app auto-binds without an app one-shot.
var autoBindSubKey = autoBindSubKeyT{}

// withAutoBindSub stamps a verified subject onto a request context.
func withAutoBindSub(ctx context.Context, sub string) context.Context {
	return context.WithValue(ctx, autoBindSubKey, sub)
}

// autoBindSubFromContext reads a verified subject stamped by the auth
// middleware, or "" when none is present (session apps / anonymous requests).
func autoBindSubFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v := ctx.Value(autoBindSubKey); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// ─── binding ────────────────────────────────────────────────────────

// bindSessionUserTo stamps the (canonical) user id + immutable bind epoch on a
// session and persists it. Shared by the explicit kernel and the auto-bind
// path. Re-binding to the SAME user is idempotent (keeps the original boundAt —
// re-stamping boundAt on every request would let a slid token walk boundAt past
// a later revoked_at and dodge the revoke); re-binding to a DIFFERENT user
// (account switch) resets both.
func (app *liveApp) bindSessionUserTo(sess *liveSession, uid string, now int64) {
	if sess == nil || uid == "" {
		return
	}
	sess.mu.Lock()
	changed := false
	if sess.userID != uid {
		sess.userID = uid
		sess.boundAt = now
		changed = true
	} else if sess.boundAt == 0 {
		sess.boundAt = now
		changed = true
	}
	sid := sess.sid
	sess.mu.Unlock()
	if changed && app != nil && app.store != nil && sid != "" {
		app.store.Set(sid, sess)
	}
}

// Live.bindSessionUser : String -> Task Error ()
//
// Bind the CURRENT live session to an application user at auth time. Call it in
// your login handler's `update` branch (or the Cmd it performs) once the user
// is authenticated — it stamps the session so revokeUser/disableUser can evict
// it. The runtime resolves "current session" from the dispatch goroutine
// (currentLiveSession); called outside a live dispatch it is a no-op Ok (e.g. a
// top-level Task.run in a CLI has no session).
func Live_bindSessionUser(uid any) any {
	capUid := uid
	return func() any {
		sub := canonicalSub(capUid)
		if sub == "" {
			return Err[any, any](ErrInvalidInput("Live.bindSessionUser: empty / non-identifying userId"))
		}
		sess := currentLiveSession()
		if sess == nil {
			// No live session in scope — CLI / top-level Task.run. Binding is a
			// session-scoped effect, so this is a benign no-op, not an error.
			return Ok[any, any](struct{}{})
		}
		sess.app.Load().bindSessionUserTo(sess, sub, time.Now().Unix())
		return Ok[any, any](struct{}{})
	}
}

// ─── the gate ───────────────────────────────────────────────────────

// unboundWarned gates the stderr warning to once per process. An atomic (not a
// sync.Once) so ResetRevocationGate can clear it without the data race that
// reassigning a sync.Once would create against a concurrent gate eval.
var unboundWarned atomic.Bool

// unboundGateHits counts how many times an ENABLED gate met an UNBOUND session.
// It is the observable "loud, not silent" signal (gate G1): a mutation that
// makes an unbound session a SILENT Active would leave this at 0 and redden the
// assertion. The stderr warning is emitted at most once (unboundWarnedOnce);
// the counter increments every time.
var unboundGateHits atomic.Int64

// accessGateBlocks is the single revocation gate. Returns true when the caller
// must NOT proceed with the dispatch (the session was evicted, or is already
// being torn down). Returns false when dispatch should run normally — which is
// ALWAYS the case when the gate is not enabled or the session is unbound
// (preserving today's behaviour for public/anonymous apps).
//
// Ordering guarantees:
//   - Not enabled (no Live.withRevocation) → false, zero cost.
//   - Already evicted → true (a late async dispatch on a corpse mutates nothing).
//   - Unbound (userID == "") under an ENABLED gate → LOUD warn-once, then false
//     (no-op → normal dispatch). Never a silent Active for a forgotten bind.
//   - Bound + Active → false. Bound + Revoked/Disabled → EVICT + true.
//
// A DB read error inside authAccessState fails OPEN (Active) — a transient
// outage must not log every user out; the login lock-out + token revokedCheck
// remain as the durable stops.
//
// PRECONDITION: the caller holds sess.mu (every dispatch funnel caller —
// handleEvent, runPerformBody, the tick / subscriber / stream loops — and
// handleInitial's render block do). The gate therefore reads sess.userID /
// sess.boundAt WITHOUT re-locking; sess.mu is not reentrant.
func (app *liveApp) accessGateBlocks(sess *liveSession) bool {
	gate := getRevocationGate()
	if gate == nil {
		return false // feature not enabled
	}
	if sess == nil {
		return false
	}
	if sess.evicted.Load() {
		return true // corpse — block, mutate nothing
	}
	uid := sess.userID
	boundAt := sess.boundAt
	if uid == "" {
		// Enabled but this session never bound. Loud (observable via the
		// counter, printed once) — a forgotten Live.bindSessionUser is a
		// security hole, never a silent Active pass.
		unboundGateHits.Add(1)
		if unboundWarned.CompareAndSwap(false, true) {
			fmt.Fprintf(os.Stderr,
				"[WARN] Sky.Live revocation gate is enabled but a session reached "+
					"dispatch UNBOUND (no Live.bindSessionUser / auto-bind). That "+
					"session cannot be revoked. Bind users at login.\n")
		}
		return false // preserve anonymous behaviour
	}
	state, err := gate.accessStateCached(uid, boundAt)
	if err != nil {
		// Fail OPEN — see the doc comment.
		return false
	}
	if state == accessActive {
		return false
	}
	// Revoked or Disabled → evict.
	app.evictForAccess(sess)
	return true
}

// evictForAccess retires a session on a revocation/disable verdict. The teardown
// (markDone → goroutines exit; store.Delete → blob removed) runs in its OWN
// goroutine so it NEVER executes under the sess.mu the dispatch funnel holds —
// markDone cancels subscriptions and closes streams/sockets whose callbacks
// could otherwise contend with the caller's held lock. Correctness does not
// depend on the teardown finishing before the dispatch returns: the gate sits
// at the TOP of every dispatch, so any later tick/subscriber that races ahead
// of markDone hits the gate first (evicted flag is already set) and no-ops. The
// evicted flag is CAS'd so the teardown is spawned exactly once.
func (app *liveApp) evictForAccess(sess *liveSession) {
	if !sess.evicted.CompareAndSwap(false, true) {
		return // already evicting
	}
	sid := sess.sid
	go func() {
		// markDone first so sess.done closes promptly even if the store no
		// longer holds the sid; store.Delete then removes the blob (and calls
		// markDone again — idempotent via doneOnce). The evicted flag set above
		// makes any concurrent store.Set a no-op, so the corpse never resurrects.
		sess.markDone()
		if app != nil && app.store != nil && sid != "" {
			app.store.Delete(sid)
		}
	}()
}
