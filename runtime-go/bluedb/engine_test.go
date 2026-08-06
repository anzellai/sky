package bluedb

import (
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeClock is an injectable, rewindable wall clock (millis) for the HLC tests.
type fakeClock struct{ ms atomic.Int64 }

func (c *fakeClock) set(ms int64)        { c.ms.Store(ms) }
func (c *fakeClock) fn() wallClockMillis { return func() int64 { return c.ms.Load() } }

func openDisk(t *testing.T, clock wallClockMillis) *pebbleEngine {
	t.Helper()
	e, err := openWith(config{dir: t.TempDir(), wallClock: clock})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}

func put(t *testing.T, e *pebbleEngine, key, val string) HLC {
	t.Helper()
	r := e.Commit(CommitReq{Writes: []VersionedWrite{{UserKey: []byte(key), Op: OpPut, Value: []byte(val)}}})
	if r.Err != nil {
		t.Fatalf("put %q: %v", key, r.Err)
	}
	return r.CommitTs
}

func del(t *testing.T, e *pebbleEngine, key string) HLC {
	t.Helper()
	r := e.Commit(CommitReq{Writes: []VersionedWrite{{UserKey: []byte(key), Op: OpDelete}}})
	if r.Err != nil {
		t.Fatalf("delete %q: %v", key, r.Err)
	}
	return r.CommitTs
}

func getAt(t *testing.T, e *pebbleEngine, key string, ts HLC) (string, bool) {
	t.Helper()
	r := e.snapshotAt(ts)
	defer r.Close()
	v, _, ok := r.Get([]byte(key))
	return string(v), ok
}

// TestVersionedRoundTrip: Put K@t1=v1, K@t2=v2 (t2>t1); reads resolve per-version.
func TestVersionedRoundTrip(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())

	t1 := put(t, e, "K", "v1")
	t2 := put(t, e, "K", "v2")
	if !t1.Less(t2) {
		t.Fatalf("expected t1 < t2, got %+v %+v", t1, t2)
	}

	if v, ok := getAt(t, e, "K", t2); !ok || v != "v2" {
		t.Fatalf("Get(K,t2)=%q,%v want v2,true", v, ok)
	}
	if v, ok := getAt(t, e, "K", t1); !ok || v != "v1" {
		t.Fatalf("Get(K,t1)=%q,%v want v1,true", v, ok)
	}
	t0 := HLC{WallMs: 999, Logical: 0} // strictly below t1
	if v, ok := getAt(t, e, "K", t0); ok {
		t.Fatalf("Get(K,t0<t1)=%q,%v want absent", v, ok)
	}
}

// TestSnapshotIsolation: a reader at readTs sees the newest version <= readTs even
// as newer commits land; the equal-length-prefix boundary (C1) never returns a
// neighbouring key's value.
func TestSnapshotIsolation(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())

	t1 := put(t, e, "K", "v1")
	r := e.snapshotAt(t1) // frozen view as of t1
	defer r.Close()

	_ = put(t, e, "K", "v2") // newer commit lands after the snapshot
	if v, _, ok := r.Get([]byte("K")); !ok || string(v) != "v1" {
		t.Fatalf("frozen reader Get(K)=%q,%v want v1,true", v, ok)
	}

	// C1 boundary: "aa" written LATER than readTs, "ab" (equal length) written at/before.
	// Reading "aa" as-of an early readTs must be ABSENT, not "ab"'s value.
	tab := put(t, e, "ab", "AB") // "ab" exists at tab
	_ = put(t, e, "aa", "AA")    // "aa" exists only AFTER tab
	rb := e.snapshotAt(tab)
	defer rb.Close()
	if v, _, ok := rb.Get([]byte("aa")); ok {
		t.Fatalf("C1 leak: Get(aa)@tab=%q returned present; want absent (aa is newer)", v)
	}
	if v, _, ok := rb.Get([]byte("ab")); !ok || string(v) != "AB" {
		t.Fatalf("Get(ab)@tab=%q,%v want AB,true", v, ok)
	}
}

// TestTombstone: delete K@t2; Get(K,t2)=absent, Get(K,t1)=v1.
func TestTombstone(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())

	t1 := put(t, e, "K", "v1")
	t2 := del(t, e, "K")
	if !t1.Less(t2) {
		t.Fatalf("expected t1 < t2")
	}
	if v, ok := getAt(t, e, "K", t2); ok {
		t.Fatalf("Get(K,t2 after delete)=%q,%v want absent", v, ok)
	}
	if v, ok := getAt(t, e, "K", t1); !ok || v != "v1" {
		t.Fatalf("Get(K,t1)=%q,%v want v1,true", v, ok)
	}
}

// TestHLCMonotonicRestartFloor: commit at a high ts, reopen with a BACKWARD wall
// clock; the next commitTs is strictly greater than the persisted high-water (no
// re-issue, no collision) — §3.3 floor.
func TestHLCMonotonicRestartFloor(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{}
	clk.set(5000)

	e1, err := openWith(config{dir: dir, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open1: %v", err)
	}
	hi := put(t, e1, "K", "v1")
	if err := e1.Close(); err != nil {
		t.Fatalf("close1: %v", err)
	}

	// Reopen with the wall clock rewound far into the past.
	clk.set(1000)
	e2, err := openWith(config{dir: dir, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open2: %v", err)
	}
	defer e2.Close()

	next := put(t, e2, "K", "v2")
	if !hi.Less(next) {
		t.Fatalf("restart floor violated: persisted hi=%+v, next=%+v (must be strictly greater despite backward clock)", hi, next)
	}
	if next == hi {
		t.Fatalf("commitTs re-issued after restart: %+v", next)
	}
	// The old version and the new must be distinct at the key.
	if v, ok := getAt(t, e2, "K", hi); !ok || v != "v1" {
		t.Fatalf("old version clobbered: Get(K,hi)=%q,%v", v, ok)
	}
	if v, ok := getAt(t, e2, "K", next); !ok || v != "v2" {
		t.Fatalf("new version missing: Get(K,next)=%q,%v", v, ok)
	}
}

// TestMetadataInBatch: after a commit, reopen recovers hlc_hi consistent with the
// max data version; the logical-batch invariant refuses a batch missing hlc_hi.
func TestMetadataInBatch(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{}
	clk.set(3000)

	e1, err := openWith(config{dir: dir, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open1: %v", err)
	}
	maxTs := put(t, e1, "K", "v1")
	_ = put(t, e1, "K2", "v2")
	last := put(t, e1, "K", "v1b")
	maxTs = last
	if err := e1.Close(); err != nil {
		t.Fatalf("close1: %v", err)
	}

	e2, err := openWith(config{dir: dir, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open2: %v", err)
	}
	defer e2.Close()
	// Recovered high-water must equal the last (max) durable commitTs.
	if hw := e2.NowTs(); hw != maxTs {
		t.Fatalf("recovered hlc_hi=%+v, want max data version=%+v", hw, maxTs)
	}

	// The enforced invariant: a logical batch (hasWrites) missing hlc_hi is refused.
	if err := enforceLogicalBatchInvariant(true, false); err != ErrMissingCommitMetadata {
		t.Fatalf("logical batch missing hlc_hi should be refused, got %v", err)
	}
	if err := enforceLogicalBatchInvariant(true, true); err != nil {
		t.Fatalf("well-formed logical batch should pass, got %v", err)
	}
	if err := enforceLogicalBatchInvariant(false, false); err != nil {
		t.Fatalf("non-logical (GC-exempt) batch should pass, got %v", err)
	}
}

// TestGroupCommitBasic: N concurrent Puts → all durable, one contiguous
// (strictly-increasing, distinct) commitTs order; all values readable.
func TestGroupCommitBasic(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())

	const n = 200
	var wg sync.WaitGroup
	results := make([]HLC, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r := e.Commit(CommitReq{Writes: []VersionedWrite{{
				UserKey: []byte(fmt.Sprintf("k%03d", i)),
				Op:      OpPut,
				Value:   []byte(fmt.Sprintf("v%d", i)),
			}}})
			results[i] = r.CommitTs
			errs[i] = r.Err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("commit %d failed: %v", i, err)
		}
	}
	// Group-commit assigns ONE commitTs per drained batch, so writes sharing a drain
	// window share a commitTs. The invariant is: the DISTINCT commitTs values form a
	// contiguous, strictly-increasing total order (no group re-issues an earlier ts),
	// and every assigned ts is <= the final high-water.
	sorted := append([]HLC(nil), results...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].Less(sorted[b]) })
	var distinct []HLC
	for i, ts := range sorted {
		if i == 0 || distinct[len(distinct)-1] != ts {
			distinct = append(distinct, ts)
		}
	}
	for i := 1; i < len(distinct); i++ {
		if !distinct[i-1].Less(distinct[i]) {
			t.Fatalf("distinct commitTs not strictly increasing at %d: %+v then %+v", i, distinct[i-1], distinct[i])
		}
	}
	if len(distinct) < 1 || len(distinct) > n {
		t.Fatalf("distinct commitTs count=%d out of range [1,%d]", len(distinct), n)
	}
	// All writes durable + readable at the final high-water.
	hw := e.NowTs()
	r := e.snapshotAt(hw)
	defer r.Close()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k%03d", i)
		v, _, ok := r.Get([]byte(key))
		if !ok || string(v) != fmt.Sprintf("v%d", i) {
			t.Fatalf("key %s = %q,%v want v%d", key, v, ok, i)
		}
	}
}

// TestChangelogWrite verifies the opaque L1 changelog payload round-trips keyed by
// commitTs, ascending, tail-readable.
func TestChangelogWrite(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())

	payloads := []string{"chg-a", "chg-b", "chg-c"}
	var tss []HLC
	for i, p := range payloads {
		r := e.Commit(CommitReq{
			Writes:           []VersionedWrite{{UserKey: []byte(fmt.Sprintf("k%d", i)), Op: OpPut, Value: []byte("x")}},
			ChangelogPayload: []byte(p),
		})
		if r.Err != nil {
			t.Fatalf("commit %d: %v", i, r.Err)
		}
		tss = append(tss, r.CommitTs)
	}

	entries, err := e.Changelog().Tail(HLC{})
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(entries) != len(payloads) {
		t.Fatalf("tail len=%d want %d", len(entries), len(payloads))
	}
	for i, ent := range entries {
		if string(ent.Payload) != payloads[i] {
			t.Fatalf("entry %d payload=%q want %q", i, ent.Payload, payloads[i])
		}
		if ent.CommitTs != tss[i] {
			t.Fatalf("entry %d ts=%+v want %+v", i, ent.CommitTs, tss[i])
		}
	}
	// Tail after the first commit returns only the later two.
	after, err := e.Changelog().Tail(tss[0])
	if err != nil {
		t.Fatalf("tail-after: %v", err)
	}
	if len(after) != 2 || string(after[0].Payload) != "chg-b" {
		t.Fatalf("tail(after t0) = %d entries, first=%q; want 2 starting chg-b", len(after), firstPayload(after))
	}
}

func firstPayload(e []ChangelogEntry) string {
	if len(e) == 0 {
		return ""
	}
	return string(e[0].Payload)
}

// TestIterateOrdered: the snapshot cursor returns distinct user-keys in ascending
// order, newest visible version per key, tombstones skipped — an O(log n + k)
// ordered scan (not scan-then-sort).
func TestIterateOrdered(t *testing.T) {
	clk := &fakeClock{}
	clk.set(1000)
	e := openDisk(t, clk.fn())

	put(t, e, "user:03", "c0")
	put(t, e, "user:01", "a0")
	put(t, e, "user:02", "b0")
	put(t, e, "user:01", "a1") // newer version of user:01
	del(t, e, "user:02")       // user:02 tombstoned
	put(t, e, "other", "z")    // outside the "user:" prefix

	hw := e.NowTs()
	r := e.snapshotAt(hw)
	defer r.Close()

	c := r.Iterate([]byte("user:"))
	defer c.Close()

	type kv struct{ k, v string }
	var got []kv
	for c.Next() {
		got = append(got, kv{string(c.Key()), string(c.Value())})
	}
	if err := c.Err(); err != nil {
		t.Fatalf("cursor err: %v", err)
	}
	want := []kv{{"user:01", "a1"}, {"user:03", "c0"}} // user:02 tombstoned, "other" out of prefix
	if len(got) != len(want) {
		t.Fatalf("iterate got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("iterate[%d]=%+v want %+v (full: %v)", i, got[i], want[i], got)
		}
	}
}
