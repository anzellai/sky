package bluedb

import (
	"fmt"
	"sync"
	"testing"

	"github.com/cockroachdb/pebble/v2"
)

// rawHas reports whether the EXACT physical data-key version (userKey@ts) is present
// in Pebble (bypasses MVCC resolution) — the probe for "GC physically deleted this
// version" vs "kept it".
func rawHas(t *testing.T, e *pebbleEngine, key string, ts HLC) bool {
	t.Helper()
	v, closer, err := e.db.Get(encodeDataKey([]byte(key), ts))
	if err == pebble.ErrNotFound {
		return false
	}
	if err != nil {
		t.Fatalf("rawHas %q: %v", key, err)
	}
	_ = v
	_ = closer.Close()
	return true
}

// TestGCDropsStaleVersionsBelowT: three versions of K, empty live set → T advances to
// the high-water; the two oldest-shadowed version is physically dropped, the newest
// version < T is kept, the version >= T is kept.
func TestGCDropsStaleVersionsBelowT(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())

	t1 := put(t, e, "K", "v1")
	t2 := put(t, e, "K", "v2")
	t3 := put(t, e, "K", "v3")

	st, err := e.GC()
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if !st.Advanced || st.Threshold != t3 {
		t.Fatalf("expected T advanced to high-water %+v, got %+v (advanced=%v)", t3, st.Threshold, st.Advanced)
	}
	if st.VersionsDeleted != 1 {
		t.Fatalf("expected 1 stale version deleted, got %d", st.VersionsDeleted)
	}
	if rawHas(t, e, "K", t1) {
		t.Fatalf("stale version K@t1 (strictly older than newest<T) should be physically deleted")
	}
	if !rawHas(t, e, "K", t2) {
		t.Fatalf("newest version < T (K@t2) must be kept")
	}
	if !rawHas(t, e, "K", t3) {
		t.Fatalf("version >= T (K@t3) must be kept")
	}
	// The live read is unaffected.
	if v, ok := getAt(t, e, "K", t3); !ok || v != "v3" {
		t.Fatalf("Get(K,t3) after GC = %q,%v want v3", v, ok)
	}
}

// TestGCKeepsNewestBelowFloorAndSoleVersion: a key whose only/newest version sits
// strictly below the floor is KEPT (a reader at exactly T must still resolve it); a
// key's sole remaining version is never dropped.
func TestGCKeepsNewestBelowFloorAndSoleVersion(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())

	t1 := put(t, e, "K", "v1") // K's only version, below the floor we'll set
	t2 := put(t, e, "OTHER", "o1")

	st, err := e.GC()
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if st.Threshold != t2 {
		t.Fatalf("expected T=%+v (high-water), got %+v", t2, st.Threshold)
	}
	if st.VersionsDeleted != 0 {
		t.Fatalf("no version should be deleted (each key has a single version), got %d", st.VersionsDeleted)
	}
	if !rawHas(t, e, "K", t1) {
		t.Fatalf("K's sole version (newest < T) must be kept")
	}
	// A reader at exactly the floor resolves K to its below-floor value.
	if v, ok := getAt(t, e, "K", t2); !ok || v != "v1" {
		t.Fatalf("Get(K, T)=%q,%v want v1 (newest <= readTs)", v, ok)
	}
}

// TestGC2aReaderProtected is the grill 2a TOCTOU test: a reader that atomically
// registers (readTs just at/above T) pins the GC floor via min-over-live, so the
// version it needs is NOT collected. Only after the reader releases does GC advance
// past it and collect.
func TestGC2aReaderProtected(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())

	t1 := put(t, e, "K", "v1")

	// Atomic pick-and-register: the reader's readTs is t1 (current high-water) and its
	// token is recorded in the SAME critical section (no NowTs()-then-register gap).
	r, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if r.ReadTs() != t1 {
		t.Fatalf("reader readTs=%+v want t1=%+v", r.ReadTs(), t1)
	}

	_ = put(t, e, "K", "v2")
	_ = put(t, e, "K", "v3")

	st, err := e.GC()
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	// The register barrier holds T down to the live reader's readTs; T cannot advance
	// past t1, so t1 is at/above T and is protected.
	if st.Threshold != t1 {
		t.Fatalf("GC floor must be pinned to live reader readTs %+v, got %+v", t1, st.Threshold)
	}
	if !rawHas(t, e, "K", t1) {
		t.Fatalf("2a violation: version the live reader needs (K@t1) was GC'd")
	}
	if v, _, ok := r.Get([]byte("K")); !ok || string(v) != "v1" {
		t.Fatalf("protected reader Get(K)=%q,%v want v1", v, ok)
	}
	r.Close() // release the token

	// With the live set empty, GC advances to the high-water and collects the stale
	// version the reader no longer needs.
	st2, err := e.GC()
	if err != nil {
		t.Fatalf("GC(2): %v", err)
	}
	if !st2.Threshold.IsZero() && st2.Threshold == t1 {
		t.Fatalf("after release GC should advance past t1, T stuck at %+v", st2.Threshold)
	}
	if rawHas(t, e, "K", t1) {
		t.Fatalf("after reader release, stale K@t1 should be collectible")
	}
}

// TestGC2bPhysicalOnly is the grill 2b test: GC's physical deletes do NOT bump hlc_hi
// and do NOT append a changelog entry. With a live reader holding the floor down so
// retention trims nothing, the changelog is byte-for-byte unchanged across the pass.
func TestGC2bPhysicalOnly(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	dir := t.TempDir()
	e, err := openWith(config{dir: dir, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Pin the floor at the first commit so retention trims nothing.
	t1 := put(t, e, "K", "v1")
	r, err := e.Snapshot()
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	_ = t1

	commitWithLog(t, e, "K", "v2", "chg-b")
	commitWithLog(t, e, "K", "v3", "chg-c")
	// Another explicit-payload commit so there IS a changelog to (not) mutate.
	commitWithLog(t, e, "M", "m1", "chg-a")

	before, err := e.Changelog().Tail(HLC{})
	if err != nil {
		t.Fatalf("tail before: %v", err)
	}
	hiBefore := e.NowTs()

	st, err := e.GC()
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if st.Threshold != r.ReadTs() {
		t.Fatalf("floor should be pinned to the reader (%+v), got %+v", r.ReadTs(), st.Threshold)
	}

	// (a) No hlc_hi bump.
	if hiAfter := e.NowTs(); hiAfter != hiBefore {
		t.Fatalf("GC bumped hlc_hi: before=%+v after=%+v", hiBefore, hiAfter)
	}
	// (b) No changelog entry appended, and (floor pinned) none trimmed → byte-identical.
	after, err := e.Changelog().Tail(HLC{})
	if err != nil {
		t.Fatalf("tail after: %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("changelog length changed across GC: before=%d after=%d", len(before), len(after))
	}
	for i := range before {
		if before[i].CommitTs != after[i].CommitTs || string(before[i].Payload) != string(after[i].Payload) {
			t.Fatalf("changelog entry %d changed across GC: %+v -> %+v", i, before[i], after[i])
		}
	}
	// (c) Reopen: recovered hlc_hi is the last COMMIT's, never GC-advanced.
	r.Close()
	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	e2, err := openWith(config{dir: dir, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer e2.Close()
	if e2.NowTs() != hiBefore {
		t.Fatalf("recovered hlc_hi=%+v want last-commit %+v (GC must not persist a higher hlc_hi)", e2.NowTs(), hiBefore)
	}
}

// TestGCChangelogRetentionTrimsBelowT: with an empty live set, GC advances T to the
// high-water and range-deletes every changelog entry with commitTs strictly below T;
// entries at/above T survive; nothing is appended.
func TestGCChangelogRetentionTrimsBelowT(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())

	commitWithLog(t, e, "k0", "x", "chg-a")
	commitWithLog(t, e, "k1", "x", "chg-b")
	tsC := commitWithLog(t, e, "k2", "x", "chg-c") // newest = high-water

	st, err := e.GC()
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if !st.ChangelogTrimmed || st.Threshold != tsC {
		t.Fatalf("expected retention trim at T=%+v, got trimmed=%v T=%+v", tsC, st.ChangelogTrimmed, st.Threshold)
	}
	entries, err := e.Changelog().Tail(HLC{})
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	// Only the entry at commitTs == T (not < T) survives.
	if len(entries) != 1 || logMarker(t, entries[0].Payload) != "chg-c" || entries[0].CommitTs != tsC {
		t.Fatalf("retention: got %d entries (first=%q), want 1 (chg-c at T)", len(entries), firstMarker(t, entries))
	}
	// No new entry above the last commit.
	tail, err := e.Changelog().Tail(tsC)
	if err != nil {
		t.Fatalf("tail-after: %v", err)
	}
	if len(tail) != 0 {
		t.Fatalf("GC appended a changelog entry: %d entries after T", len(tail))
	}
}

// TestGCSnapshotTooOld: a readTs below the GC threshold T is rejected. Register's
// defensive branch (readTs < T) and Advance's guard both surface ErrSnapshotTooOld.
func TestGCSnapshotTooOld(t *testing.T) {
	// Registry-level: a persisted threshold above the high-water rejects Register.
	high := HLC{WallMs: 5000}
	reg := newWatermarkRegistry(func() HLC { return HLC{WallMs: 1000} }, high)
	if _, _, err := reg.Register(); err != ErrSnapshotTooOld {
		t.Fatalf("Register with readTs < T should be ErrSnapshotTooOld, got %v", err)
	}

	// Advance-level: after GC advances T, advancing a token below T is rejected.
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())
	_ = put(t, e, "K", "v1")
	t2 := put(t, e, "K2", "v2")
	tok, readTs, err := e.reg.Register()
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	defer e.reg.Release(tok)
	if _, err := e.GC(); err != nil { // empty-live besides tok=readTs=t2 → T=t2
		t.Fatalf("GC: %v", err)
	}
	_ = readTs
	// A token cannot be advanced to a readTs below the (now advanced) threshold.
	if err := e.reg.Advance(tok, HLC{WallMs: 1}); err != ErrSnapshotTooOld {
		t.Fatalf("Advance below T should be ErrSnapshotTooOld, got %v", err)
	}
	_ = t2
}

// TestGCPersistsThresholdMonotone: T survives a reopen and never regresses.
func TestGCPersistsThresholdMonotone(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	dir := t.TempDir()
	e, err := openWith(config{dir: dir, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = put(t, e, "K", "v1")
	t2 := put(t, e, "K", "v2")
	st, err := e.GC()
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if st.Threshold != t2 {
		t.Fatalf("T=%+v want %+v", st.Threshold, t2)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	e2, err := openWith(config{dir: dir, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer e2.Close()
	if got := e2.reg.Threshold(); got != t2 {
		t.Fatalf("persisted T not recovered: got %+v want %+v", got, t2)
	}
}

// TestGCConcurrentWithCommitter: GC runs concurrently with a firehose of commits;
// no acked write is lost, no panic (run under -race). GC's physical keys are disjoint
// from the committer's fresh-commitTs writes (C1 amendment).
func TestGCConcurrentWithCommitter(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())

	const n = 300
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < n; i++ {
			r := e.Commit(CommitReq{Writes: []VersionedWrite{{
				UserKey: []byte(fmt.Sprintf("k%03d", i%20)), // reuse keys → many stale versions
				Op:      OpPut,
				Value:   []byte(fmt.Sprintf("v%d", i)),
			}}})
			if r.Err != nil {
				t.Errorf("commit %d: %v", i, r.Err)
				return
			}
		}
	}()
	// Interleave GC passes.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			if _, err := e.GC(); err != nil {
				t.Errorf("GC pass %d: %v", i, err)
				return
			}
		}
	}()
	wg.Wait()

	// Final state: the newest value for every key is intact.
	hw := e.NowTs()
	r := e.snapshotAt(hw)
	defer r.Close()
	for i := 0; i < 20; i++ {
		key := fmt.Sprintf("k%03d", i)
		if _, _, ok := r.Get([]byte(key)); !ok {
			t.Fatalf("newest version of %s lost after concurrent GC", key)
		}
	}
}

// TestAdvanceThresholdClampsToDurableHi is Fix-3 (b): a pure unit test on
// advanceThreshold. With an in-memory high-water of tNew but durableHi = tOld (< tNew)
// and an empty live set, the advanced threshold must clamp to tOld — never the
// not-yet-durable tNew.
func TestAdvanceThresholdClampsToDurableHi(t *testing.T) {
	tOld := HLC{WallMs: 1000}
	tNew := HLC{WallMs: 5000}
	reg := newWatermarkRegistry(func() HLC { return tNew }, HLC{})
	reg.durableHi = func() HLC { return tOld } // durable high-water lags the in-memory one

	got, advanced := reg.advanceThreshold()
	if !advanced || got != tOld {
		t.Fatalf("advanceThreshold must clamp candidate (tNew=%+v) to durableHi (tOld=%+v): got %+v advanced=%v", tNew, tOld, got, advanced)
	}
	if th := reg.Threshold(); tOld.Less(th) {
		t.Fatalf("Threshold %+v exceeds durableHi %+v (clamp failed)", th, tOld)
	}
}

// TestGCThresholdNeverExceedsDurableHi is Fix-3 (a): after any GC pass the persisted
// threshold T is <= durableHi (<= durable hlc_hi). Here every commit is durable, so T
// reaches the high-water and the clamp is a no-op — but the invariant still holds.
func TestGCThresholdNeverExceedsDurableHi(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())

	_ = put(t, e, "K", "v1")
	_ = put(t, e, "K", "v2")
	t3 := put(t, e, "K", "v3")

	st, err := e.GC()
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	// The direct Fix-3 invariant: persisted T <= durableHi.
	if e.durableHi().Less(st.Threshold) {
		t.Fatalf("GC threshold %+v exceeded durableHi %+v (Fix-3 clamp violated)", st.Threshold, e.durableHi())
	}
	// All durable → T reaches the high-water.
	if st.Threshold != t3 {
		t.Fatalf("T should reach the durable high-water %+v, got %+v", t3, st.Threshold)
	}
}

// TestGCThresholdClampSurvivesCrashNoReaderWedge is Fix-3 (c): the crash regression. It
// reproduces the dangerous interleave — the committer has ASSIGNED a commitTs for an
// in-flight commit (the in-memory high-water races ahead) but has NOT yet Apply(Sync)'d,
// so durableHi still trails. A GC in that window must NOT persist a threshold above the
// durable high-water; otherwise a crash before the in-flight commit's Apply-Sync recovers
// hlc_hi < gc_threshold → every reader wedges on ErrSnapshotTooOld. Advancing the
// in-memory HLC directly reproduces the interleave deterministically (no timing hook).
func TestGCThresholdClampSurvivesCrashNoReaderWedge(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{}
	clk.set(1000)
	e, err := openWith(config{dir: dir, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// A durable commit: durableHi == persisted hlc_hi == tLast.
	tLast := put(t, e, "K", "v1")

	// In-flight-but-not-applied: the in-memory HLC races ahead of durableHi.
	e.hlc.next()
	tAhead := e.hlc.next()
	if !tLast.Less(tAhead) {
		t.Fatalf("setup: tAhead %+v not ahead of tLast %+v", tAhead, tLast)
	}
	if e.durableHi() != tLast {
		t.Fatalf("durableHi should still be tLast %+v (no Apply since), got %+v", tLast, e.durableHi())
	}

	// GC in the window. Pre-Fix-3 it advances T to the in-memory high-water (tAhead) and
	// persists it durably though only tLast is durable. Fix-3 clamps T to durableHi=tLast.
	st, err := e.GC()
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if st.Threshold != tLast {
		t.Fatalf("GC threshold must clamp to durableHi %+v (not the in-memory high-water %+v), got %+v", tLast, tAhead, st.Threshold)
	}
	if err := e.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen: recovered hlc_hi == tLast; persisted gc_threshold == tLast. Invariant
	// gc_threshold <= hlc_hi holds → a fresh reader is NOT wedged.
	e2, err := openWith(config{dir: dir, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer e2.Close()

	hw := e2.NowTs()
	if hw.Less(e2.reg.Threshold()) {
		t.Fatalf("recovered hlc_hi %+v < gc_threshold %+v — Fix-3 clamp failed, readers would wedge", hw, e2.reg.Threshold())
	}
	r, err := e2.Snapshot()
	if err != nil {
		t.Fatalf("reader WEDGED after reopen: %v — gc_threshold outran the durable hlc_hi", err)
	}
	r.Close()
	// Post-recovery commits land at/above T, not in the trimmed changelog tail.
	got := put(t, e2, "K", "v2")
	if got.Less(e2.reg.Threshold()) {
		t.Fatalf("post-recovery commit %+v landed below gc_threshold %+v (changelog-trim tail)", got, e2.reg.Threshold())
	}
}

// --- test helpers ---

func commitWithLog(t *testing.T, e *pebbleEngine, key, val, log string) HLC {
	t.Helper()
	r := e.Commit(CommitReq{
		Writes:           []VersionedWrite{{UserKey: []byte(key), Op: OpPut, Value: []byte(val)}},
		ChangelogPayload: logPayload(log),
	})
	if r.Err != nil {
		t.Fatalf("commitWithLog %q: %v", key, r.Err)
	}
	return r.CommitTs
}
