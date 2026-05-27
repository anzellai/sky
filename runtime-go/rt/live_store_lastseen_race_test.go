package rt

// Task #326: lastSeen race regression.
//
// `memoryStore.Get` held `s.mu.RLock()` (a READ lock) and wrote
// `sess.lastSeen = time.Now()` to the shared `*liveSession` pointer.
// Two concurrent /_sky/event requests for the same session both
// passed the RLock-only gate and raced on the struct field — the
// race detector flagged it as a clean double-write on the same
// 24-byte time.Time value. Equivalent pattern was present in
// sqliteStore / postgresStore / redisStore (Set wrote
// `sess.lastSeen = time.Now()` while sibling Get callers could be
// holding the same pointer via memCache).
//
// Fix: lastSeen is now atomic.Int64 (UnixNano). All reads/writes go
// through the lastSeenTime / touchLastSeen / setLastSeenTime
// helpers. Race-free under any combination of RLock / Lock / no
// lock at all.
//
// This file pins the contract with adversarial Get / Set / Delete
// patterns. The Sky.Live runtime would not normally mix all three
// against the same session id from many goroutines, but the race
// detector flags any shared-pointer racy access — so if a future
// refactor reverts to a plain `time.Time` field these tests will
// fail under `-race`.

import (
	"sync"
	"testing"
	"time"
)

// pollutorCount keeps every test sub-routine bounded; large enough to
// reliably surface a race on a multi-core box, small enough to keep
// the suite fast.
const pollutorCount = 32

// concurrentGetSetDelete pounds the store with concurrent Get / Set /
// Delete + lastSeen reads against the SAME session id. Under -race,
// any racy access to `sess.lastSeen` (or its in-process replacement)
// trips the detector.
func concurrentGetSetDelete(t *testing.T, store SessionStore) {
	t.Helper()
	sess := &liveSession{
		sseCh:     make(chan sseFrame, 4),
		cancelSub: make(chan struct{}),
		done:      make(chan struct{}),
	}
	store.Set("sid-race", sess)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Get-pollutors — these are the readers that race-detected against
	// concurrent Get-writers in the v0.15.21-and-earlier code.
	for i := 0; i < pollutorCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = store.Get("sid-race")
				}
			}
		}()
	}

	// Set-pollutors — overwrite the same session id (idempotent for
	// memoryStore; for sqlite/postgres/redis it also exercises the Set
	// path's lastSeen-write site).
	for i := 0; i < pollutorCount/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					store.Set("sid-race", sess)
				}
			}
		}()
	}

	// lastSeen-readers — independent calls to sess.lastSeenTime() must
	// be race-free even while Get/Set are stamping the field.
	for i := 0; i < pollutorCount/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = sess.lastSeenTime()
				}
			}
		}()
	}

	// Let the pollutors actually pollute for a short window.
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

func TestLastSeenRace_MemoryStore(t *testing.T) {
	store := newMemoryStore(30 * time.Minute)
	defer store.Close()
	concurrentGetSetDelete(t, store)
}

func TestLastSeenRace_SQLiteStore(t *testing.T) {
	store, err := newSQLiteStore("file::memory:?cache=shared", 30*time.Minute)
	if err != nil {
		t.Fatalf("newSQLiteStore: %v", err)
	}
	defer store.Close()
	concurrentGetSetDelete(t, store)
}

// TestLastSeenAtomic_RoundtripsZeroValue — a freshly-constructed
// liveSession with zero lastSeen reads back as time.Time{} via
// lastSeenTime() so existing IsZero / now.Sub callers keep their
// pre-atomic semantics.
func TestLastSeenAtomic_RoundtripsZeroValue(t *testing.T) {
	sess := &liveSession{}
	if got := sess.lastSeenTime(); !got.IsZero() {
		t.Errorf("zero liveSession.lastSeen should read back as zero time.Time, got %v", got)
	}
}

// TestLastSeenAtomic_RoundtripsSetValue — setLastSeenTime + lastSeenTime
// round-trip to within 1ns (atomic.Int64 stores UnixNano).
func TestLastSeenAtomic_RoundtripsSetValue(t *testing.T) {
	sess := &liveSession{}
	want := time.Date(2026, 5, 27, 12, 0, 0, 123456789, time.UTC)
	sess.setLastSeenTime(want)
	got := sess.lastSeenTime()
	if !got.Equal(want) {
		t.Errorf("round-trip mismatch: want %v, got %v", want, got)
	}
}

// TestLastSeenAtomic_TouchAdvances — successive touchLastSeen calls
// never regress (monotonic per-process wall clock assumption).
func TestLastSeenAtomic_TouchAdvances(t *testing.T) {
	sess := &liveSession{}
	sess.touchLastSeen()
	first := sess.lastSeenTime()
	if first.IsZero() {
		t.Fatal("touchLastSeen should stamp a non-zero time")
	}
	time.Sleep(2 * time.Millisecond)
	sess.touchLastSeen()
	second := sess.lastSeenTime()
	if !second.After(first) {
		t.Errorf("second touchLastSeen should produce a later time: first=%v second=%v",
			first, second)
	}
}
