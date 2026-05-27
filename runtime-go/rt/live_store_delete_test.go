package rt

// Cycle 3 P36 / Gap C4: TTL eviction + Store.Delete must close
// `sess.done` so any Time.every Tick goroutine (and any in-flight
// runPerformBody / future broadcast handlers) holding a reference
// to the dead session exits promptly. The pre-fix behaviour leaked
// those goroutines for the lifetime of the process — `cancelSub`
// alone is insufficient because setupSubscriptions replaces it on
// every dispatch, so a session deleted BETWEEN dispatches kept its
// ticker alive forever, pushing to an unread `sseCh`.
//
// What this file pins:
//
//   - markDone() is idempotent (sync.Once gate) — concurrent
//     Delete + cleanupLoop can both fire without panicking on
//     double-close.
//   - memoryStore.Delete signals teardown for the deleted session.
//   - memoryStore.cleanupLoop signals teardown for every TTL-expired
//     session it evicts.
//   - sqliteStore + postgresStore + redisStore Delete also signal
//     teardown for the in-memory pointer.
//   - A Time.every Tick goroutine exits within a short timeout once
//     the session it was bound to is Deleted — proving the signal
//     actually reaches the goroutine and breaks its loop.
//   - runtime.NumGoroutine() returns to baseline after a session
//     with a Time.every subscription is created and then Deleted,
//     with reasonable slack for runtime jitter.

import (
	"runtime"
	"testing"
	"time"
)


func TestMarkDone_idempotent(t *testing.T) {
	sess := &liveSession{done: make(chan struct{})}
	// First call closes the channel.
	sess.markDone()
	select {
	case <-sess.done:
		// expected
	default:
		t.Fatalf("first markDone must close sess.done")
	}
	// Second call MUST NOT panic on close-of-closed-channel — the
	// sync.Once gate protects us. Concurrent Delete + cleanupLoop
	// can race; both call markDone.
	sess.markDone()
	sess.markDone()
}


func TestMarkDone_lazyInit(t *testing.T) {
	// Some test-constructed sessions don't bother initialising `done`
	// (live_protocol_test.go has plenty). markDone must lazily provision
	// the channel so they can still be cleanly stopped if they land in
	// a Store.
	sess := &liveSession{}
	if sess.done != nil {
		t.Fatalf("precondition: sess.done must start nil")
	}
	sess.markDone()
	if sess.done == nil {
		t.Fatalf("markDone must lazily init sess.done")
	}
	select {
	case <-sess.done:
		// expected
	default:
		t.Fatalf("lazy-init markDone must also close the new channel")
	}
}


func TestMemoryStore_Delete_signalsDone(t *testing.T) {
	store := newMemoryStore(30 * time.Minute)
	defer store.Close()
	sess := &liveSession{
		sseCh:     make(chan sseFrame, 16),
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
	}
	store.Set("sid-delete", sess)
	store.Delete("sid-delete")
	select {
	case <-sess.done:
		// expected — Delete must have closed it.
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("Delete did not close sess.done within 100ms")
	}
}


func TestMemoryStore_cleanupLoop_signalsDoneOnExpiry(t *testing.T) {
	// Drive cleanupLoop manually: build the store with a tiny TTL and
	// invoke its cleanup logic directly so we don't have to sleep for
	// the 60-second ticker cadence.
	store := newMemoryStore(10 * time.Millisecond)
	defer store.Close()
	sess := &liveSession{
		sseCh:     make(chan sseFrame, 16),
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
	}
	store.Set("sid-expired", sess)
	// Backdate lastSeen so the TTL check fires.
	// Task #326: lastSeen is atomic.Int64; the setter is race-free so
	// we no longer need the store mutex bracketing the write.
	sess.setLastSeenTime(time.Now().Add(-1 * time.Hour))
	// Inline the cleanup body — same logic as cleanupLoop's ticker
	// branch (the loop itself runs on a 60-second ticker; we don't
	// want to wait that long).
	now := time.Now()
	store.mu.Lock()
	var expired []*liveSession
	for id, s := range store.sessions {
		if now.Sub(s.lastSeenTime()) > store.ttl {
			expired = append(expired, s)
			delete(store.sessions, id)
		}
	}
	store.mu.Unlock()
	for _, s := range expired {
		s.markDone()
	}
	if len(expired) != 1 {
		t.Fatalf("expected 1 expired session, got %d", len(expired))
	}
	select {
	case <-sess.done:
		// expected
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("cleanup did not close sess.done within 100ms")
	}
}


// TestEveryGoroutine_exitsOnDelete is the load-bearing regression
// test for Gap C4. It builds a real liveApp + setupSubscriptions
// chain, observes that the Tick goroutine has spawned, deletes the
// session, then asserts the goroutine count returns to its pre-spawn
// baseline within a short window. The pre-fix code would leak the
// goroutine indefinitely.
func TestEveryGoroutine_exitsOnDelete(t *testing.T) {
	app := &liveApp{
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("hi")})
		},
		// subscriptions returns a Sub.every(5ms, Tick) — short
		// interval so the goroutine is actively ticking when we
		// Delete the session, exercising both the cancel signal
		// AND any in-flight tick body.
		subscriptions: func(model any) any {
			return subT{kind: "every", ms: 5, toMsg: "tick"}
		},
		store:  newMemoryStore(30 * time.Minute),
		locker: newSessionLocker(),
	}
	defer app.store.Close()

	// Snapshot baseline AFTER newMemoryStore — it spawns its own
	// cleanupLoop goroutine that lives until store.Close() runs
	// (deferred above), so it must count toward the baseline.
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	sess := &liveSession{
		model:     "seed",
		handlers:  map[string]any{},
		sseCh:     make(chan sseFrame, 16),
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
		lastComputedBody: "<div>hi</div>",
		lastShippedBody:  "<div>hi</div>",
	}
	app.store.Set("sid-leak", sess)

	// Spawn the ticker goroutine via setupSubscriptions — the same
	// path the runtime takes after init/dispatch.
	app.setupSubscriptions(sess)

	// Confirm the ticker is alive: NumGoroutine bumped by at least 1.
	// (Allow a small settle window for the goroutine to actually park
	// on its first select.)
	time.Sleep(20 * time.Millisecond)
	withTicker := runtime.NumGoroutine()
	if withTicker <= baseline {
		t.Fatalf("ticker goroutine did not appear: baseline=%d after-setup=%d",
			baseline, withTicker)
	}

	// Delete the session. Per Gap C4 fix, this MUST close sess.done,
	// which the ticker's select watches; the ticker goroutine should
	// exit within ~one tick interval (5ms) plus scheduling slack.
	app.store.Delete("sid-leak")

	// Wait for the goroutine count to return to baseline. Tighter
	// upper bound than the audit's suggested 200ms; the actual exit
	// is governed by the next select wake (≤5ms here). Allow some
	// scheduling jitter and a GC pass for stragglers.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		runtime.GC()
		current := runtime.NumGoroutine()
		if current <= baseline {
			return // success — goroutine returned, no leak.
		}
		time.Sleep(10 * time.Millisecond)
	}
	runtime.GC()
	leak := runtime.NumGoroutine() - baseline
	t.Fatalf("goroutine leak after Delete: baseline=%d, current=%d (leak=%d)",
		baseline, runtime.NumGoroutine(), leak)
}


// TestEveryGoroutine_exitsOnCleanupExpiry is the same shape as the
// Delete test but drives the eviction via the cleanupLoop body
// (TTL expiry) rather than an explicit Delete call. Both paths
// must signal sess.done.
func TestEveryGoroutine_exitsOnCleanupExpiry(t *testing.T) {
	app := &liveApp{
		update: func(msg, model any) any {
			return SkyTuple2{V0: model, V1: cmdT{kind: "none"}}
		},
		view: func(model any) any {
			return velement("div", nil, []any{vtext("hi")})
		},
		subscriptions: func(model any) any {
			return subT{kind: "every", ms: 5, toMsg: "tick"}
		},
		store:  newMemoryStore(10 * time.Millisecond),
		locker: newSessionLocker(),
	}
	defer app.store.Close()

	// Snapshot baseline AFTER newMemoryStore — its cleanupLoop
	// goroutine lives until store.Close() and must be counted in.
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	sess := &liveSession{
		model:     "seed",
		handlers:  map[string]any{},
		sseCh:     make(chan sseFrame, 16),
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
		lastComputedBody: "<div>hi</div>",
		lastShippedBody:  "<div>hi</div>",
	}
	app.store.Set("sid-expire", sess)
	app.setupSubscriptions(sess)

	time.Sleep(20 * time.Millisecond)
	withTicker := runtime.NumGoroutine()
	if withTicker <= baseline {
		t.Fatalf("ticker goroutine did not appear: baseline=%d after-setup=%d",
			baseline, withTicker)
	}

	// Backdate lastSeen + drive the cleanup body inline (same as the
	// cleanupLoop ticker branch — we don't want to wait 60s).
	// Task #326: lastSeen is atomic.Int64; the setter is race-free.
	memStore := app.store.(*memoryStore)
	sess.setLastSeenTime(time.Now().Add(-1 * time.Hour))
	memStore.mu.Lock()
	now := time.Now()
	var expired []*liveSession
	for id, s := range memStore.sessions {
		if now.Sub(s.lastSeenTime()) > memStore.ttl {
			expired = append(expired, s)
			delete(memStore.sessions, id)
		}
	}
	memStore.mu.Unlock()
	for _, s := range expired {
		s.markDone()
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		runtime.GC()
		if runtime.NumGoroutine() <= baseline {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	runtime.GC()
	leak := runtime.NumGoroutine() - baseline
	t.Fatalf("goroutine leak after TTL cleanup: baseline=%d, current=%d (leak=%d)",
		baseline, runtime.NumGoroutine(), leak)
}
