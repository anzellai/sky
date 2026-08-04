package rt

import (
	"sync"
	"testing"
	"time"
)

// R2 regression: encodeSession reads + reflect-walks sess.model. The bluedb
// reap re-encodes still-fresh sessions to refresh their durable blob; doing
// that OFF sess.mu races a concurrent dispatch reassigning sess.model → an
// unrecoverable `concurrent map read and map write`. The fix encodes under
// sess.mu. This drives the real reap re-encode branch concurrently with model
// reassignment; under -race it fails if the reap encode ever leaves the lock.
func TestReapEncodeUnderLockR2(t *testing.T) {
	dir := t.TempDir()
	store, err := newBlueDBStore(dir+"/sess.blue", time.Millisecond)
	if err != nil {
		t.Fatalf("newBlueDBStore: %v", err)
	}

	sess := &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
		model:     map[string]any{"n": float64(0), "s": "init"},
	}
	// Persist with an OLD lastSeen so reap sees the durable blob as stale…
	sess.setLastSeenTime(time.Now().Add(-time.Hour))
	store.Set("sid-r2", sess)
	// …but keep the LIVE session fresh so reap takes the re-encode branch.
	sess.setLastSeenTime(time.Now())

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				sess.mu.Lock()
				sess.model = map[string]any{"n": float64(i), "s": "x"}
				sess.touchLastSeen()
				i++
				sess.mu.Unlock()
			}
		}
	}()

	for i := 0; i < 400; i++ {
		// Re-stale the durable blob each iteration so EVERY reap takes the
		// re-encode branch (a re-encode refreshes it to now, which would
		// otherwise skip on the next pass). Under sess.mu, so it serializes with
		// the dispatcher and the reap encode — exactly the discipline being pinned.
		sess.mu.Lock()
		sess.setLastSeenTime(time.Now().Add(-time.Hour))
		store.Set("sid-r2", sess)
		sess.setLastSeenTime(time.Now())
		sess.mu.Unlock()
		store.reap(time.Now()) // re-encodes the fresh session — must stay under sess.mu
	}
	close(stop)
	wg.Wait()
	_ = store.Close()
}

// R1 regression: server-initiated (async) dispatches must persist their Model
// mutation, so it survives a restart. Before the fix, only human-interaction
// paths called store.Set — Cmd.perform / Time.every / pub-sub / reactive
// refresh mutated sess.model + shipped an (acked) SSE frame the server then
// forgot on restart. This drives persistSession (as the async paths now do) and
// verifies the mutation is durable across a store reopen (a "restart").
func TestAsyncDispatchPersistsR1(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/sess.blue"
	store, err := newBlueDBStore(path, time.Hour)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	app := &liveApp{store: store}
	sess := &liveSession{
		sid:       "s1",
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
		model:     map[string]any{"n": float64(1)},
	}
	store.Set("s1", sess) // initial persist (as mount would)

	// Simulate an async dispatch: mutate the Model, then persist under sess.mu.
	sess.mu.Lock()
	sess.model = map[string]any{"n": float64(42)}
	app.persistSession(sess)
	sess.mu.Unlock()
	_ = store.Close()

	// "Restart": reopen the file + Get (decodes from disk, no memCache).
	store2, err := newBlueDBStore(path, time.Hour)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store2.Close()
	got, ok := store2.Get("s1")
	if !ok {
		t.Fatal("session not persisted across restart")
	}
	m, _ := got.model.(map[string]any)
	if m["n"] != float64(42) {
		t.Fatalf("async Model mutation not persisted: n=%v want 42", m["n"])
	}
}
