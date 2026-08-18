package rt

import (
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// newRevocationTestApp builds a minimal liveApp whose update INCREMENTS a
// counter and bumps model["n"], so a dispatch that actually ran is observable.
// Returns the app, the dispatch counter, and the memory store.
func newRevocationTestApp(t *testing.T) (*liveApp, *atomic.Int64, *memoryStore) {
	t.Helper()
	var updates atomic.Int64
	store := newMemoryStore(time.Minute)
	app := &liveApp{
		update: func(msg, model any) any {
			updates.Add(1)
			m, _ := model.(map[string]any)
			nn := map[string]any{}
			for k, v := range m {
				nn[k] = v
			}
			cur, _ := nn["n"].(int)
			nn["n"] = cur + 1
			return SkyTuple2{V0: nn, V1: Cmd_none()}
		},
		view:    func(model any) any { return velement("div", nil, nil) },
		store:   store,
		locker:  newSessionLocker(),
		msgTags: map[string]int{},
	}
	t.Cleanup(func() { ResetRevocationGate() })
	return app, &updates, store
}

func newBoundSession(t *testing.T, app *liveApp, store *memoryStore, sid, uid string, boundAt int64) *liveSession {
	t.Helper()
	init := velement("div", nil, nil)
	assignSkyIDs(&init, "r")
	sess := &liveSession{
		sid:       sid,
		model:     map[string]any{"n": 0},
		handlers:  map[string]any{},
		prevTree:  &init,
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
		userID:    uid,
		boundAt:   boundAt,
	}
	sess.app.Store(app)
	store.Set(sid, sess)
	return sess
}

// dispatchUnderLock mirrors the funnel callers: every real caller holds sess.mu
// around app.dispatch (handleEvent, runPerformBody, the tick/subscriber loops).
func dispatchUnderLock(app *liveApp, sess *liveSession, msg any) string {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	return app.dispatch(sess, msg)
}

// ─── Gate 4 (LOAD-BEARING): a server-initiated dispatch after revocation
// mutates NOTHING. This exercises the exact call the Time.every tick /
// Cmd.perform completion make — app.dispatch under sess.mu — on a revoked
// session. Mutation that reddens it: gate only at handleEvent (not in the
// dispatch funnel) → the ticker's app.dispatch runs update → model["n"]
// increments and updates>0.
func TestServerInitiatedDispatchAfterRevokeMutatesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "g4.db")
	db := openFileAuthDb(t, path)
	app, updates, store := newRevocationTestApp(t)
	setRevocationGate(db, 0)

	sess := newBoundSession(t, app, store, "sid-g4", "42", 100)

	// A tick BEFORE revoke runs normally (proves the harness dispatches).
	if body := dispatchUnderLock(app, sess, "Tick"); body == "" {
		t.Fatalf("pre-revoke tick should have rendered a body")
	}
	if updates.Load() != 1 {
		t.Fatalf("pre-revoke: expected 1 update, got %d", updates.Load())
	}
	nBefore := sess.model.(map[string]any)["n"].(int)

	// Revoke, then a server-initiated tick must mutate NOTHING.
	mustOk(t, runTaskRes(t, Auth_revokeUser(db, "42")), "revokeUser")
	body := dispatchUnderLock(app, sess, "Tick")
	if body != "" {
		t.Fatalf("gate 4: a revoked session's tick must produce no frame, got %q", body)
	}
	if got := updates.Load(); got != 1 {
		t.Fatalf("gate 4 (LOAD-BEARING): update() must NOT run for a revoked session — ran %d times", got)
	}
	if nAfter := sess.model.(map[string]any)["n"].(int); nAfter != nBefore {
		t.Fatalf("gate 4: revoked session model mutated (n %d -> %d)", nBefore, nAfter)
	}
	if !sess.evicted.Load() {
		t.Fatalf("gate 4: a revoked dispatch must EVICT the session")
	}
}

// ─── Gate 5: eviction retires goroutines. After a revoked verdict sess.done is
// closed, so a goroutine selecting on it exits. -race clean.
func TestEvictionRetiresGoroutines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "g5.db")
	db := openFileAuthDb(t, path)
	app, _, store := newRevocationTestApp(t)
	setRevocationGate(db, 0)
	sess := newBoundSession(t, app, store, "sid-g5", "42", 100)

	// A ticker-like goroutine that lives until sess.done fires.
	exited := make(chan struct{})
	go func() {
		<-sess.done
		close(exited)
	}()

	mustOk(t, runTaskRes(t, Auth_revokeUser(db, "42")), "revokeUser")
	_ = dispatchUnderLock(app, sess, "Tick") // triggers eviction

	select {
	case <-exited:
		// goroutine retired
	case <-time.After(2 * time.Second):
		t.Fatalf("gate 5: sess.done must close on eviction so goroutines retire")
	}
	// The store no longer holds the session (Delete ran).
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := store.Get("sid-g5"); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("gate 5: evicted session must be removed from the store")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ─── Gate 3 (G1): an UNBOUND session under an ENABLED gate is a LOUD no-op,
// never a silent Active. The dispatch runs normally (anonymous behaviour
// preserved) BUT the loud-signal counter increments. Mutation that reddens it:
// make unbound silently Active (drop the unboundGateHits.Add) → count stays 0.
func TestUnboundSessionUnderEnabledGateIsLoudNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "g3.db")
	db := openFileAuthDb(t, path)
	app, updates, store := newRevocationTestApp(t)
	setRevocationGate(db, 0)

	// A session that never bound (userID == "").
	sess := newBoundSession(t, app, store, "sid-unbound", "", 0)

	before := unboundGateHits.Load()
	body := dispatchUnderLock(app, sess, "Tick")
	if body == "" {
		t.Fatalf("gate 3: an unbound session must dispatch normally (anonymous), got empty body")
	}
	if updates.Load() != 1 {
		t.Fatalf("gate 3: an unbound session's update must run (anonymous behaviour)")
	}
	if sess.evicted.Load() {
		t.Fatalf("gate 3: an unbound session must NOT be evicted")
	}
	if unboundGateHits.Load() <= before {
		t.Fatalf("gate 3 (G1): an unbound session under an enabled gate must raise the LOUD signal, not pass silently")
	}
}

// ─── Gate 3 (G1) part 2 + the gate function under handleInitial semantics: a
// bound revoked session is BLOCKED by the gate; an active bound session and a
// fresh-bind-after-revoke session are NOT. This is exactly the verdict
// handleInitial consults before running init Cmds (gate 6 at the unit level).
func TestAccessGateVerdicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gv.db")
	db := openFileAuthDb(t, path)
	app, _, store := newRevocationTestApp(t)
	setRevocationGate(db, 0)

	// Active user → not blocked.
	active := newBoundSession(t, app, store, "sid-active", "7", 100)
	active.mu.Lock()
	if app.accessGateBlocks(active) {
		t.Fatalf("active user must not be blocked")
	}
	active.mu.Unlock()

	mustOk(t, runTaskRes(t, Auth_revokeUser(db, "9")), "revokeUser 9")

	// Bound BEFORE the revoke → blocked + evicted.
	revoked := newBoundSession(t, app, store, "sid-revoked", "9", 1)
	revoked.mu.Lock()
	blocked := app.accessGateBlocks(revoked)
	revoked.mu.Unlock()
	if !blocked {
		t.Fatalf("a session bound before the revoke must be blocked")
	}

	// A FRESH bind AFTER the revoke (boundAt in the far future) → NOT blocked.
	fresh := newBoundSession(t, app, store, "sid-fresh", "9", 1<<40)
	fresh.mu.Lock()
	if app.accessGateBlocks(fresh) {
		t.Fatalf("gate 2/3: a fresh bind after revoke must NOT be blocked (revoke != ban)")
	}
	fresh.mu.Unlock()
}

// ─── Gate 8 at the live layer: revoke via replica A's Db; a dispatch on replica
// B's app (its OWN Db handle onto the same file, its OWN store) is evicted — the
// gate read the shared TABLE, not B's cached session.
func TestCrossReplicaEvictionViaSharedTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "g8.db")
	dbA := openFileAuthDb(t, path)
	dbB := openFileAuthDb(t, path)

	// Replica B: its own app + store + gate wired to dbB.
	appB, updatesB, storeB := newRevocationTestApp(t)
	setRevocationGate(dbB, 0)
	sessB := newBoundSession(t, appB, storeB, "sid-b", "42", 100)

	// Revoke through replica A's handle.
	mustOk(t, runTaskRes(t, Auth_revokeUser(dbA, "42")), "revoke via A")

	// B's dispatch must now be evicted — proving B read the shared table, not
	// its own (still-live) session state.
	body := dispatchUnderLock(appB, sessB, "Tick")
	if body != "" {
		t.Fatalf("gate 8: B's dispatch on a revoked user must produce no frame, got %q", body)
	}
	if updatesB.Load() != 0 {
		t.Fatalf("gate 8: B's update must NOT run for a user revoked on A")
	}
	if !sessB.evicted.Load() {
		t.Fatalf("gate 8: B must evict the session revoked on A (shared-table read)")
	}
}

// ─── The gate is inert when the feature is not enabled (no Live.withRevocation):
// zero cost, no eviction, dispatch runs. Guards against a false-positive gate.
func TestGateInertWhenNotEnabled(t *testing.T) {
	app, updates, store := newRevocationTestApp(t)
	// NOTE: setRevocationGate NOT called → gate disabled.
	sess := newBoundSession(t, app, store, "sid-off", "42", 100)
	body := dispatchUnderLock(app, sess, "Tick")
	if body == "" || updates.Load() != 1 || sess.evicted.Load() {
		t.Fatalf("gate disabled: dispatch must run normally (body=%q updates=%d evicted=%v)",
			body, updates.Load(), sess.evicted.Load())
	}
}
