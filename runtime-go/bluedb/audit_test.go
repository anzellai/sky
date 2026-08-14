package bluedb

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"
)

// collSep is the L2 collection/pk separator baked into every data user-key
// (collName ‖ 0x1F ‖ pk — see the Txn.collResolver contract in txn.go). Its value,
// 0x1F == 31, is what makes N1 detonate at a realistic collection-name length.
const collSep byte = 0x1F

// dataUserKey builds a data user-key the way L2 does: collName ‖ 0x1F ‖ pk.
func dataUserKey(collName, pk string) []byte {
	k := make([]byte, 0, len(collName)+1+len(pk))
	k = append(k, collName...)
	k = append(k, collSep)
	k = append(k, pk...)
	return k
}

// collPrefix builds the scan prefix for a collection: collName ‖ 0x1F.
func collPrefix(collName string) []byte {
	return append([]byte(collName), collSep)
}

// TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes pins defect N1 (and N1b).
//
// pebbleReader.Iterate used to build its iterator bounds as `[]byte{tagData} ‖ prefix`,
// which ends in an arbitrary USER byte. skydbSplit reads a key's trailing byte as a
// suffix length, so a bound of length L ending in byte v is mis-parsed as
// bound[:L-v] iff 0 < v <= L-1.
//
// With prefix = collName ‖ 0x1F (len(bound) == n+2, trailing byte 31) that gives
// three regimes over the collection-name length n:
//
//	n <= 29 — correct, by luck of the length.
//	n == 30 — the LOWER bound's parsed prefix collapses to [0x00], i.e. the whole data
//	          keyspace, while the upper bound is still parsed correctly. Another
//	          collection's rows are returned, decoded and predicate-matched as if they
//	          belonged to this one. Silent cross-collection leakage.
//	n >= 31 — (all of them, not just 31) the upper bound is mis-parsed further left
//	          than the lower, so lower > upper. Pebble has no production assertion on
//	          inverted bounds: zero rows, Err() == nil, indistinguishable from an
//	          empty collection.
//
// Thirty is not an exotic collection name. The table below straddles all three
// regimes, plus n = 130, which is inside the content-independent regime (any trailing
// byte <= 127 is mis-parsed once the bound is long enough).
//
// The fix is in the CALLER: both bounds now end in 0x00, so skydbSplit returns len
// and both are bare prefixes. It is deliberately NOT in skydbSplit / comparer.go /
// keys.go — comparerName "skydb.mvcc.v1" is frozen into SSTable metadata and changing
// Split would change on-disk ordering, requiring skydb.mvcc.v2 plus a store rewrite.
func TestAuditN1IterateBoundsDoNotLeakAcrossPrefixes(t *testing.T) {
	for _, n := range []int{28, 29, 30, 31, 32, 33, 34, 130} {
		t.Run(fmt.Sprintf("collNameLen=%d", n), func(t *testing.T) {
			clk := &fakeClock{}
			clk.set(1000)
			e := openDisk(t, clk.fn())

			// nameB sorts strictly BEFORE nameA, so under the n == 30 collapse — where
			// the lower bound falls to the start of the data keyspace but the upper
			// bound still binds — nameB's row lands inside the scanned interval. A
			// name sorting after nameA would hide the leak.
			nameA := strings.Repeat("k", n)
			nameB := strings.Repeat("b", n)

			keyA := dataUserKey(nameA, "pk1")
			keyB := dataUserKey(nameB, "pk1")
			put(t, e, string(keyA), "rowA")
			put(t, e, string(keyB), "rowB")

			r := e.snapshotAt(e.NowTs())
			defer r.Close()

			c := r.Iterate(collPrefix(nameA))
			defer c.Close()

			var got []string
			for c.Next() {
				got = append(got, string(c.Key()))
			}
			scanErr := c.Err()

			if len(got) != 1 {
				t.Fatalf("collName len %d: Iterate(%q‖0x1F) returned %d rows %q, want exactly 1 (%q); err=%v\n"+
					"  >1 row  = cross-collection leakage (another collection's rows scanned as this one's)\n"+
					"  0 rows  = inverted bounds (a silent empty collection)",
					n, nameA, len(got), got, keyA, scanErr)
			}
			if got[0] != string(keyA) {
				t.Fatalf("collName len %d: Iterate returned %q, want %q", n, got[0], keyA)
			}
			if scanErr != nil {
				t.Fatalf("collName len %d: cursor err = %v, want nil", n, scanErr)
			}
		})
	}

	// N1b — scanPrefixMaterialize used to drain the base cursor and never consult
	// cur.Err(), so a failed scan was reported as an empty collection (or, worse, as
	// the buffered write-set alone: a plausible-looking partial collection that a
	// predicate then treats as the truth).
	//
	// The n >= 31 regime above is exactly this failure shape — zero rows where rows
	// exist — but at the Pebble level it produces no error to propagate, so the
	// inverted-bound check in Iterate now converts it into one. This sub-test drives
	// the propagation path directly with a failing cursor, which pins the contract
	// independently of how the error is produced.
	t.Run("N1b/failed-scan-surfaces-an-error-not-an-empty-collection", func(t *testing.T) {
		clk := &fakeClock{}
		clk.set(1000)
		e := openDisk(t, clk.fn())

		put(t, e, string(dataUserKey("orders", "pk1")), "row1")

		tx, err := e.Begin()
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Abort()

		// A buffered write under the same prefix: without the error check, the
		// write-set overlay alone would make the result look like a real, non-empty
		// collection of exactly one row.
		if err := tx.Put(dataUserKey("orders", "pk2"), []byte("row2")); err != nil {
			t.Fatalf("put: %v", err)
		}

		boom := errors.New("injected base-scan failure")
		cur := tx.materializeScan(collPrefix("orders"), &failingCursor{err: boom})
		defer cur.Close()

		var got []string
		for cur.Next() {
			got = append(got, string(cur.Key()))
		}
		if len(got) != 0 {
			t.Fatalf("materializeScan over a failed cursor returned %d rows %q, want 0 — "+
				"a partial/write-set-only collection is worse than an error", len(got), got)
		}
		if !errors.Is(cur.Err(), boom) {
			t.Fatalf("materializeScan swallowed the base-scan error: Err() = %v, want %v", cur.Err(), boom)
		}
	})
}

// TestAuditN5CorruptHlcHiRefusesOpenAndNeverReissuesTs pins defect N5.
//
// readMetaHLC used to test `len(v) < hlcEncodedLen` and return `{0,0}, nil`, making a
// TRUNCATED hlc_hi indistinguishable from an ABSENT one — the fresh-store sentinel.
// newHLCClock floors the commit clock to that value, so a corrupt hlc_hi restarts the
// clock from the bare wall clock and RE-ISSUES a commitTs that is already on disk. Two
// transactions then share one MVCC data key (userKey ‖ ~commitTs) and the later Set
// silently overwrites the earlier COMMITTED version. Irrecoverable, and invisible to
// every read.
//
// The fix is `!=` and an error: corruption refuses to open rather than guessing.
//
// This test asserts the CONSEQUENCE, not merely the shape. "openWith returns an error"
// alone would also pass against a wrong fix. Under the mutation openWith SUCCEEDS, the
// clock is re-seeded from the (frozen) wall clock, and the very next Commit hands back a
// commitTs that is NOT greater than the one already recorded on disk — which is what the
// final assertion catches.
func TestAuditN5CorruptHlcHiRefusesOpenAndNeverReissuesTs(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{}
	clk.set(5000) // frozen: the reopened clock cannot out-run the recorded ts on wall time alone

	e1, err := openWith(config{dir: dir, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	recorded := put(t, e1, "orders\x1fpk1", "v1")
	if err := e1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if recorded.IsZero() {
		t.Fatalf("recorded commitTs is the fresh-store sentinel — the fixture proves nothing")
	}

	// Truncate hlc_hi behind the engine's back: 3 bytes where 12 are required. This is the
	// bit-rot / partial-write shape, not a shape any writer in this package can produce.
	raw, err := pebble.Open(dir, &pebble.Options{Comparer: skydbComparer, Logger: quietLogger{}})
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if err := raw.Set(encodeMetaKey(metaHLCHi), []byte{0x01, 0x02, 0x03}, pebble.Sync); err != nil {
		t.Fatalf("corrupt hlc_hi: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	e2, openErr := openWith(config{dir: dir, wallClock: clk.fn()})
	if openErr != nil {
		// The fix: refuse to open. The message must NAME the key so an operator can act on
		// it — an anonymous "corrupt metadata" is not an actionable refusal.
		if !strings.Contains(openErr.Error(), metaHLCHi) {
			t.Fatalf("openWith rejected the corrupt store but the error does not name %q: %v", metaHLCHi, openErr)
		}
		return
	}
	defer e2.Close()

	// It opened over a 3-byte hlc_hi. Prove the consequence rather than asserting the
	// shape: the store MUST NOT be able to re-issue a commitTs at or below `recorded`.
	reopenedHigh := e2.NowTs() // the restart floor the corrupt read produced
	reissued := e2.Commit(CommitReq{
		Writes: []VersionedWrite{{UserKey: []byte("orders\x1fpk2"), Op: OpPut, Value: []byte("v2")}},
	})
	if reissued.Err != nil {
		t.Fatalf("commit after reopen: %v", reissued.Err)
	}
	if !recorded.Less(reissued.CommitTs) {
		t.Fatalf("openWith accepted a truncated %q (3 bytes, want %d) and the commit clock RESTARTED: "+
			"the reopened high-water is %+v (IsZero=%v — the fresh-store sentinel the corrupt read "+
			"forged), so the next commitTs is %+v, NOT greater than the already-committed %+v — "+
			"the next write to a shared key silently overwrites a committed version",
			metaHLCHi, hlcEncodedLen, reopenedHigh, reopenedHigh.IsZero(), reissued.CommitTs, recorded)
	}
	t.Fatalf("openWith accepted a truncated %q (3 bytes, want %d) instead of refusing to open; "+
		"a mis-sized meta value is corruption, not a fresh store", metaHLCHi, hlcEncodedLen)
}

// TestAuditC1CommitOnClosedChannelReturnsError pins defect C1: a commit against a closed
// engine MUST NOT report success.
//
// Commit declared an UNNAMED return. When `e.ch <- job` panics on a channel Close already
// closed, the deferred recover sent CommitResult{Err: ErrClosed} into job.done — a
// buffered channel nobody will ever read, because `return <-job.done` never executed. The
// function therefore returned the ZERO CommitResult: Err == nil. The caller sees a
// SUCCESSFUL commit for a write that was never enqueued, never applied, never durable.
//
// The fix is BOTH halves: name the return AND make the recover assign it. Naming the
// return alone is a no-op — res stays zero and the false ack survives behind a diff that
// looks like the fix. This test fails against that half-fix exactly as it fails against
// the original.
//
// Fully deterministic, no timing: the engine is hand-built in-package with ch already
// closed and closed still false, so isClosed() answers false, the send panics, and the
// recover fires — the precise interleaving the real Close race produces, without racing.
func TestAuditC1CommitOnClosedChannelReturnsError(t *testing.T) {
	e := &pebbleEngine{ch: make(chan *commitJob, maxBatch)}
	close(e.ch)

	// Precondition: the early-out guards must NOT be what returns the error, or the test
	// would pass without ever reaching the send.
	if e.sealed.Load() {
		t.Fatalf("fixture: engine reports sealed; the ErrSealed guard would short-circuit the send")
	}
	if e.isClosed() {
		t.Fatalf("fixture: engine reports closed; the ErrClosed guard would short-circuit the send")
	}

	res := e.Commit(CommitReq{
		Writes: []VersionedWrite{{UserKey: []byte("c1-key"), Op: OpPut, Value: []byte("v")}},
	})

	if res.Err == nil {
		t.Fatalf("Commit on a closed committer channel returned Err = nil (CommitTs %+v) — a FALSE ACK: "+
			"the write was never enqueued, never applied and is not durable, yet the caller is told it "+
			"committed. The recover must assign the NAMED return, not send into the unread job.done",
			res.CommitTs)
	}
	if !errors.Is(res.Err, ErrClosed) {
		t.Fatalf("Commit on a closed committer channel returned %v, want ErrClosed", res.Err)
	}
	if !res.CommitTs.IsZero() {
		t.Fatalf("failed Commit carries commitTs %+v, want the zero value — a non-zero ts on an error "+
			"result invites a caller to record a version that does not exist", res.CommitTs)
	}
}

// TestAuditH3ReaderGetSurfacesIoErrors pins defect H3.
//
// pebbleReader.Get returns (value, commitTs, ok) — no error channel. It used to discard
// the NewIter error and treat a failed SeekGE as "absent", so an unreadable SSTable block
// was INDISTINGUISHABLE from a missing key. Checking the NewIter error fixes nothing:
// pebble.Snapshot.NewIter (pebble/v2@v2.1.6 snapshot.go:62-69) returns a nil error
// unconditionally and panics on a closed snapshot instead. The fix is iter.Error() after
// positioning, latched onto the reader and exposed as Err() (mirroring Cursor.Err()).
//
// The harness injects errorfs.ErrInjected on every read of a *.sst file. Getting the read
// to actually TOUCH a file took three things, each of which the fixture would otherwise be
// silently vacuous without:
//
//   - a 256 KiB memTableSize plus an explicit db.Flush(), so the row lives in an SSTable
//     and not in the memtable, where no file read happens at all;
//   - a REOPEN before arming, because openWith builds a fresh pebble.Options and therefore
//     a fresh block cache — the writing engine's cache would serve the row from memory;
//   - PADDING: 400 rows of 2 KiB. A single-row store produces a one-block SSTable, and
//     Open's own hlc_hi / gc-threshold meta reads pull that one block into the new cache,
//     so the armed Get is served from memory and observes no fault. (That is not a
//     hypothetical — it is what the first cut of this test did, and it passed against the
//     UNFIXED reader.) With ~800 KiB spread over many blocks, the meta keys sit in a
//     different block from the target row and the target block is genuinely cold.
//
// The armed Get is asserted to reach the file (ok must be false), which is what keeps
// those three conditions honest: if any of them regresses, the read succeeds from cache
// and the test fails loudly instead of passing vacuously.
//
// Both assertions matter, and the second is the load-bearing one:
//
//  1. Get answers ok == false AND Err() != nil — the error is DISTINGUISHABLE from
//     absence. This is only the flag.
//  2. A Transact body doing that same Get FAILS instead of committing an insert. This is
//     what proves the fail-closed plumbing (Txn.Commit consulting reader.Err(), and
//     recordPoint refusing to log a failed read as present:false). Without it a green
//     assertion 1 would sit next to a reader whose lie still reaches the committer.
func TestAuditH3ReaderGetSurfacesIoErrors(t *testing.T) {
	const key = "h3-key0200" // mid-keyspace: its block is not the one Open's meta reads warm
	const val = "row-that-exists"

	var armed atomic.Bool
	inj := errorfs.InjectorFunc(func(op errorfs.Op) error {
		isRead := op.Kind == errorfs.OpFileRead || op.Kind == errorfs.OpFileReadAt
		if armed.Load() && isRead && strings.HasSuffix(op.Path, ".sst") {
			return errorfs.ErrInjected
		}
		return nil
	})
	fs := errorfs.Wrap(vfs.NewMem(), inj)

	clk := &fakeClock{}
	clk.set(3000)
	cfg := config{dir: crashDir, fs: fs, wallClock: clk.fn(), memTableSize: 256 << 10}

	// Write the row among enough padding to fill many blocks, then flush to an SSTable.
	e1, err := openWith(cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	padding := strings.Repeat("x", 2048)
	for i := 0; i < 400; i++ {
		k := fmt.Sprintf("h3-key%04d", i)
		if k == key {
			put(t, e1, k, val)
			continue
		}
		put(t, e1, k, padding)
	}
	if err := e1.db.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	// Sanity gate on the WRITING engine (its cache is discarded at Close, so this cannot
	// warm the reader below): the row is really there. Without it, the armed "absent"
	// could just as well mean the fixture never stored anything.
	rOK := e1.snapshotAt(e1.NowTs())
	v, _, ok := rOK.Get([]byte(key))
	rOK.Close()
	if !ok || string(v) != val {
		t.Fatalf("fixture: Get(%q) = %q,%v before injection — want %q", key, v, ok, val)
	}
	if err := e1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen: a fresh block cache, so the flushed row must be re-read from the file.
	e, err := openWith(cfg)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() {
		armed.Store(false) // never tear down under injection
		_ = e.Close()
	}()

	armed.Store(true)

	// ── Assertion 1: an I/O error is distinguishable from absence. ──
	r := e.snapshotAt(e.NowTs())
	gotV, _, gotOK := r.Get([]byte(key))
	readErr := r.Err()
	r.Close()
	if gotOK {
		t.Fatalf("Get under an injected SSTable read fault returned ok=true (%q) — the read was served "+
			"from the block cache / memtable and never touched a file, so this test proves NOTHING. "+
			"Fix the fixture (padding, flush, reopen), do not weaken the assertion.", gotV)
	}
	// Errorf, NOT Fatalf: assertion 2 below is the one that proves the fail-closed plumbing,
	// and it must still run when this flag is missing. A Fatalf here would mask it — the
	// mutation proof would only ever show assertion 1 going red, which says nothing about
	// whether Commit actually consults the flag.
	if readErr == nil {
		t.Errorf("Get swallowed an injected SSTable read fault: ok=false with Err() == nil — "+
			"an I/O error is INDISTINGUISHABLE from key %q being absent", key)
	} else if !errors.Is(readErr, errorfs.ErrInjected) {
		t.Errorf("Get surfaced %v, want the injected sentinel %v", readErr, errorfs.ErrInjected)
	}

	// ── Assertion 2 (the load-bearing one): the txn FAILS rather than inserting. ──
	// The body is the canonical read-modify-write: "if absent, insert". Under the fault the
	// Get cannot answer, so the only sound outcome is an error and NO write.
	var bodyRan int
	txErr := e.Transact(func(tx *Txn) error {
		bodyRan++
		if _, found := tx.Get([]byte(key)); !found {
			return tx.Put([]byte(key), []byte("INSERTED-OVER-AN-IO-ERROR"))
		}
		return nil
	})
	if bodyRan == 0 {
		t.Fatalf("Transact never ran the body — the test proves nothing about the commit boundary")
	}
	if txErr == nil {
		t.Errorf("Transact COMMITTED under an injected read fault: the body saw %q as absent because the "+
			"block could not be read, and inserted over it. Commit must fail closed on reader.Err().", key)
	} else if !errors.Is(txErr, errorfs.ErrInjected) {
		t.Errorf("Transact failed with %v, want the injected read error propagated (a conflict/retry would loop)", txErr)
	}

	// Disarm and prove the store is untouched: the pre-existing row still holds its
	// original value, i.e. no insert was laundered through.
	armed.Store(false)
	after := e.snapshotAt(e.NowTs())
	defer after.Close()
	av, _, aok := after.Get([]byte(key))
	if !aok {
		t.Fatalf("row %q vanished across the faulted txn", key)
	}
	if string(av) != val {
		t.Fatalf("row %q = %q after the faulted txn — the failed read was laundered into an INSERT", key, av)
	}
	if err := after.Err(); err != nil {
		t.Fatalf("post-fault reader reports Err() = %v, want nil", err)
	}
}

// failingCursor is a base cursor that yields nothing and reports an error, standing in
// for an I/O failure the test cannot force out of Pebble.
type failingCursor struct{ err error }

var _ Cursor = (*failingCursor)(nil)

func (c *failingCursor) Next() bool    { return false }
func (c *failingCursor) Key() []byte   { return nil }
func (c *failingCursor) Value() []byte { return nil }
func (c *failingCursor) CommitTs() HLC { return HLC{} }
func (c *failingCursor) Err() error    { return c.err }
func (c *failingCursor) Close()        {}

// TestAuditH1SnapshotReadTsIsPinnedWithItsSnapshot pins defect H1, deterministically.
//
// Engine.Snapshot used to build its reader in three unsynchronised steps: pick
// readTs = watermarkRegistry.Register() (== the IN-MEMORY HLC high-water), then
// db.NewSnapshot(). The committer bumps that high-water at hlc.next(), which happens
// BEFORE the batch is applied — so between next() and Apply the high-water names a
// commitTs that is assigned, not durable, and in no snapshot. A Snapshot taken in that
// window returned a reader announcing ReadTs() == that in-flight commitTs while its
// pinned view could not contain it: a "consistent view as of T" that is missing T, and
// a watermark token that floors GC at a commit which may never land.
//
// The window is real but narrow in wall-clock terms, so this test does not race for it:
// it drives e.hlc.next() DIRECTLY, which is precisely the state a committer leaves
// behind between timestamp assignment and Apply, and then asserts the property the fix
// establishes — Snapshot's readTs is durableHi (advanced only after Apply(Sync)
// RETURNS), never the in-memory high-water.
func TestAuditH1SnapshotReadTsIsPinnedWithItsSnapshot(t *testing.T) {
	clk := &fakeClock{}
	clk.set(5000)
	e := openDisk(t, clk.fn())

	// A clean, fully durable commit first, so durableHi is non-zero and the assertion
	// below cannot pass merely because everything is the {0,0} sentinel.
	committed := put(t, e, "h1-key", "v1")
	if durable := e.durableHi(); durable != committed {
		t.Fatalf("fixture: durableHi = %v after an acked commit, want the commitTs %v "+
			"(the committer must advance durableHi before it acks)", durable, committed)
	}

	// Simulate a committer that has ASSIGNED the next commitTs but has not applied it.
	// This is not a synthetic state: it is exactly what process() leaves between
	// hlc.next() and Apply(Sync).
	inFlight := e.hlc.next()
	if !committed.Less(inFlight) {
		t.Fatalf("fixture: hlc.next() = %v, want strictly above the last commit %v", inFlight, committed)
	}
	if hw := e.NowTs(); hw != inFlight {
		t.Fatalf("fixture: NowTs() = %v, want the un-applied %v — the in-memory high-water is "+
			"what the broken Snapshot used to hand out", hw, inFlight)
	}

	r, err := e.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	defer r.Close()

	if r.ReadTs() == inFlight {
		t.Fatalf("Snapshot().ReadTs() = %v — the ASSIGNED-but-unapplied commitTs. The reader claims "+
			"to be a consistent view as of a commit that is not durable and not in its pinned "+
			"snapshot, and its watermark token floors GC at a commit that may never land.", inFlight)
	}
	if got, want := r.ReadTs(), e.durableHi(); got != want {
		t.Fatalf("Snapshot().ReadTs() = %v, want durableHi = %v — readTs must be the "+
			"durably-applied high-water, chosen under durMu in the same critical section that "+
			"pins the snapshot and registers the token", got, want)
	}

	// The pinned view must actually serve the last durable commit at that readTs.
	v, ts, ok := r.Get([]byte("h1-key"))
	if !ok || string(v) != "v1" {
		t.Fatalf("Get(h1-key) = %q,%v at readTs %v — want v1; a readTs the snapshot cannot serve "+
			"is the other half of H1", v, ok, r.ReadTs())
	}
	if ts != committed {
		t.Fatalf("Get(h1-key) resolved commitTs %v, want %v", ts, committed)
	}
}

// TestAuditH1SnapshotSeesEveryCommitAtOrBelowItsReadTs is the PROPERTY behind H1:
// whatever readTs a Snapshot announces, its pinned view contains every commit at or
// below it. That is the whole content of "snapshot-consistent as of readTs".
//
// This arm is racy by nature — it hammers Snapshot() against a live committer hoping to
// land inside the assign→Apply window — so it SUPPORTS the proof rather than carrying
// it; TestAuditH1SnapshotReadTsIsPinnedWithItsSnapshot above is the deterministic one.
// It is worth running under -race regardless: the fix moves the readTs read inside
// durMu, and a torn read of durableHiVal would surface here.
func TestAuditH1SnapshotSeesEveryCommitAtOrBelowItsReadTs(t *testing.T) {
	clk := &fakeClock{}
	clk.set(7000)
	e := openDisk(t, clk.fn())

	const iterations = 300

	var lastCommitted atomic.Uint64 // index of the highest key whose commit has ACKED
	commitTs := make([]HLC, iterations)
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			select {
			case <-stop:
				return
			default:
			}
			res := e.Commit(CommitReq{Writes: []VersionedWrite{{
				UserKey: []byte(fmt.Sprintf("h1-prop-%04d", i)),
				Op:      OpPut,
				Value:   []byte("v"),
			}}})
			if res.Err != nil {
				return
			}
			commitTs[i] = res.CommitTs
			lastCommitted.Store(uint64(i) + 1) // i is durable+acked
		}
	}()

	for n := 0; n < iterations; n++ {
		r, err := e.Snapshot()
		if err != nil {
			t.Fatalf("Snapshot: %v", err)
		}
		readTs := r.ReadTs()
		// Only inspect keys whose commit had already ACKED when this snapshot was taken:
		// their commitTs is stable and non-racy to read.
		upto := int(lastCommitted.Load())
		for i := 0; i < upto; i++ {
			ts := commitTs[i]
			if readTs.Less(ts) {
				continue // committed above this reader's view — correctly invisible
			}
			if _, _, ok := r.Get([]byte(fmt.Sprintf("h1-prop-%04d", i))); !ok {
				r.Close()
				close(stop)
				<-done
				t.Fatalf("reader at readTs %v cannot see key %d committed at %v (<= readTs). "+
					"Its readTs names a commit outside its own pinned snapshot — defect H1.",
					readTs, i, ts)
			}
		}
		r.Close()
	}
	close(stop)
	<-done
}
