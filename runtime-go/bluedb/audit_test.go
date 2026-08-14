package bluedb

import (
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	var injected atomic.Int64
	inj := errorfs.InjectorFunc(func(op errorfs.Op) error {
		isRead := op.Kind == errorfs.OpFileRead || op.Kind == errorfs.OpFileReadAt
		if armed.Load() && isRead && strings.HasSuffix(op.Path, ".sst") {
			injected.Add(1)
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

	// ── The fixture rule: prove the fault was REACHED, not merely armed. ──
	// This is the assertion the prose above argues for, made MEASURED rather than
	// argued. G2.6 enumerates this injection site and requires the count check to
	// be present, because an injection test that cannot prove it injected is
	// indistinguishable from one that passed because nothing happened. Fatalf, not
	// Errorf: at zero injections every assertion below it is meaningless and would
	// mis-attribute a cache hit to the reader's error handling.
	if n := injected.Load(); n == 0 {
		t.Fatalf("the SSTable-read injector fired ZERO times — the armed Get was served from the "+
			"block cache or the memtable and never touched a file, so this test proves NOTHING. "+
			"Fix the fixture (padding, flush, reopen), do not weaken the assertions. (Get returned %q,%v)",
			gotV, gotOK)
	}
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

// TestAuditN4CloseDoesNotPanicConcurrentSnapshot pins the first half of defect N4: a
// reader pinned across Close.
//
// Every reader entry point ends in a pebble call that PANICS on a closed DB rather than
// returning an error — Snapshot.NewIter → DB.newIter (db.go:1061-1062) and
// DB.NewSnapshot (db.go:2062) both do `if err := d.closed.Load(); err != nil { panic }`.
// Engine.Snapshot's isClosed() check was therefore a TOCTOU, not a guard: Close could
// land between the check and the NewSnapshot. snapshotAt did not check at all. And Close
// neither waited for live readers nor invalidated them, so it closed the handle out from
// under whatever was mid-scan.
//
// The contract asserted here is total: under arbitrary interleaving, a snapshot request
// either succeeds or reports ErrClosed, and nothing panics — including reads issued on a
// reader that was pinned before Close began.
func TestAuditN4CloseDoesNotPanicConcurrentSnapshot(t *testing.T) {
	clk := &fakeClock{}
	clk.set(11000)
	e := openDisk(t, clk.fn())
	put(t, e, "n4-key", "v1")

	const workers = 16
	stop := make(chan struct{})
	finished := make(chan error, workers)

	for w := 0; w < workers; w++ {
		go func() {
			// A panic anywhere in here fails the test rather than killing the process
			// silently in another goroutine.
			defer func() {
				if r := recover(); r != nil {
					finished <- fmt.Errorf("PANIC in a concurrent reader: %v", r)
				}
			}()
			for {
				select {
				case <-stop:
					finished <- nil
					return
				default:
				}

				r, err := e.Snapshot()
				if err != nil {
					if !errors.Is(err, ErrClosed) {
						finished <- fmt.Errorf("Snapshot: %v, want nil or ErrClosed", err)
						return
					}
					finished <- nil // closed — this worker is done
					return
				}
				// Use the reader: Get and Iterate are the two paths that reach
				// pebble's panicking newIter.
				r.Get([]byte("n4-key"))
				c := r.Iterate(nil)
				for c.Next() {
				}
				c.Close()
				r.Close()

				// The time-travel path (reg == nil, no closed-check before the fix) —
				// the arm the first plan missed.
				tr := e.snapshotAt(HLC{WallMs: 11000, Logical: 1})
				tr.Get([]byte("n4-key"))
				tr.Close()
			}
		}()
	}

	// Let the workers get into the loop, then close underneath them.
	time.Sleep(20 * time.Millisecond)
	if err := e.Close(); err != nil {
		t.Fatalf("Close under concurrent readers: %v — want nil (the drain must let every "+
			"in-flight reader finish, not fail and not force the handle shut)", err)
	}
	close(stop)

	for i := 0; i < workers; i++ {
		select {
		case err := <-finished:
			if err != nil {
				t.Fatalf("%v", err)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("a reader goroutine never finished — Close returned while a reader was " +
				"still blocked or wedged")
		}
	}

	// After a completed Close the answer is a clean refusal, not a panic.
	if _, err := e.Snapshot(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Snapshot() after Close = %v, want ErrClosed", err)
	}
	if got := e.snapshotAt(HLC{WallMs: 11000, Logical: 1}).Err(); !errors.Is(got, ErrClosed) {
		t.Fatalf("snapshotAt() after Close reports Err() = %v, want ErrClosed", got)
	}
}

// TestAuditN4CloseWaitsForLiveReaders pins the other half of N4 — and it is the test the
// naive fix cannot pass.
//
// The obvious repair (hold closeMu.Lock() across the reader drain) DEADLOCKS
// deterministically, not under load: the reader here is held by an open transaction, and
// a transaction releases its reader on the way out of Txn.Commit → tx.e.Commit →
// isClosed() → closeMu.RLock(). With Close holding (or waiting for) the write lock, that
// RLock blocks, the deferred tx.reader.Close() never runs, the refcount never drops, and
// Close waits out its entire window every time any transaction is open. Hence the
// three-phase Close: the `closed` FLAG — not the lock — is what stops new pins, so the
// drain runs with closeMu released.
//
// The transaction is deliberate. A bare Snapshot reader would release without ever
// touching closeMu, and the naive implementation would pass.
func TestAuditN4CloseWaitsForLiveReaders(t *testing.T) {
	clk := &fakeClock{}
	clk.set(13000)
	e := openDisk(t, clk.fn())
	put(t, e, "n4-live", "v1")

	tx, err := e.Begin()
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- e.closeWithin(20 * time.Second) }()

	// Close must NOT complete while the transaction's reader is pinned.
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) while a reader was still pinned — it closed the Pebble "+
			"handle underneath a live reader, whose next operation panics inside pebble", err)
	case <-time.After(250 * time.Millisecond):
	}

	// The engine is sealed but NOT closed: the pinned reader still reads.
	if _, found := tx.Get([]byte("n4-live")); !found {
		t.Fatalf("a reader pinned before Close cannot read during the drain — the view was " +
			"torn down under it")
	}

	// Release it the way a real caller does. e.Commit refuses (ErrClosed — C1), and the
	// deferred reader.Close() is what unblocks the drain.
	if err := tx.Commit(); !errors.Is(err, ErrClosed) {
		t.Fatalf("tx.Commit() during close = %v, want ErrClosed", err)
	}

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close after the reader was released = %v, want nil", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("Close never returned after its last reader was released — the drain is " +
			"deadlocked against the reader-release path (closeMu held across the drain)")
	}
}

// TestAuditN4CloseWithLeakedReaderReportsRatherThanHangs is the arm that keeps the
// mitigation honest: a drain that can wait forever is a hang, and a drain wrapped in
// sync.Once that gives up is unrecoverable — the Once is consumed, so Close can never be
// retried and the directory lock is held for the life of the process.
//
// The documented choice: Close REPORTS (ErrReadersLive, naming the count), leaves the
// Pebble handle OPEN — closing it would panic the leaked reader's next operation, which
// is the very defect N4 is about — and stays retryable. Releasing the reader and calling
// Close again completes it.
func TestAuditN4CloseWithLeakedReaderReportsRatherThanHangs(t *testing.T) {
	clk := &fakeClock{}
	clk.set(17000)
	e := openDisk(t, clk.fn())
	put(t, e, "n4-leak", "v1")

	leaked, err := e.Snapshot() // deliberately never Closed until the end
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	closed := make(chan error, 1)
	go func() { closed <- e.closeWithin(150 * time.Millisecond) }()

	select {
	case err := <-closed:
		if !errors.Is(err, ErrReadersLive) {
			t.Fatalf("Close with a leaked reader = %v, want ErrReadersLive — a bounded, "+
				"named report is the whole point of the arm", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("Close HUNG on a leaked reader — the drain is unbounded")
	}

	// The handle is still open, which is what makes the report safe: the leaked reader
	// works instead of panicking.
	if v, _, ok := leaked.Get([]byte("n4-leak")); !ok || string(v) != "v1" {
		t.Fatalf("leaked reader Get = %q,%v after the failed Close — want v1,true; the engine "+
			"must not tear the handle down under a reader it just refused to wait for", v, ok)
	}

	// Retryable: release, close again, done. (A sync.Once here would make this
	// impossible — the engine would be stuck open forever.)
	leaked.Close()
	if err := e.Close(); err != nil {
		t.Fatalf("Close retry after releasing the leaked reader = %v, want nil — Close must "+
			"not be consumed by a failed drain", err)
	}
	// And terminal: a further Close replays the verdict rather than double-closing the DB
	// (pebble panics on that).
	if err := e.Close(); err != nil {
		t.Fatalf("third Close = %v, want the replayed nil verdict", err)
	}
}

// TestAuditN4BeginPathReaderClosesSnapshotBeforeItsPin closes the residual arm of N4 that
// C7 recorded rather than fixed: the ORDER of the two statements in pebbleReader.Close.
//
// The close drain counts watermark tokens (watermark.go's `pins`). waitDrained returns
// the instant the last token comes back, and Close's phase 3 then calls e.db.Close().
// pebbleReader.Close used to release the token BEFORE closing the *pebble.Snapshot,
// which leaves a window where the engine believes no reader is live while a snapshot is
// still registered with pebble.
//
// Begin() is the path that has no second line of defence: Snapshot()/snapshotAt() wrap
// their reader in a trackedReader whose OUTER pin spans the whole teardown, but
// beginSnapshot hands its bare *pebbleReader to Txn, and Txn.Commit/Abort call this Close
// directly. So the statement order IS the guarantee there.
//
// HOW IT IS PROVEN, and why it is not the obvious race. The obvious test — Begin, close
// the engine on a goroutine, Abort as the drain completes, assert Close returns nil — was
// written first and DISCARDED: under the mis-ordered Close it passed 60/60 rounds in three
// consecutive runs. The reader goroutine only has to execute one call (snap.Close) after
// releasing the token, while the closer has to be woken from a channel, re-take the
// registry lock, take closeMu and call db.Close; the reader wins essentially always. A
// fixture that cannot demonstrate it reached the fault proves nothing (see the plan's rule
// for injection fixtures), so it is not shipped merely because it is green.
//
// What is shipped is deterministic. The test takes the registry lock ITSELF, so the
// reader's Release blocks on it. Everything sequenced BEFORE the Release has therefore
// run by the time it parks; everything after has not. pebble's own open-snapshot count
// (Metrics().Snapshots.Count) is read at that instant: 0 means the snapshot was closed
// first — the token is about to be handed back with nothing outstanding — and 1 means the
// engine was about to tell its close drain "no reader is live" while pebble still had the
// snapshot registered, which is the whole defect. Close's phase 3 would then run
// e.db.Close() under it and report "leaked snapshots: N open snapshots on DB"
// (pebble db.go:1818). There is no timing luck in either direction: the sleep only gives
// the goroutine time to REACH the blocking point, and a longer sleep can only strengthen
// the conclusion.
func TestAuditN4BeginPathReaderClosesSnapshotBeforeItsPin(t *testing.T) {
	clk := &fakeClock{}
	clk.set(23000)
	e := openDisk(t, clk.fn())
	put(t, e, "n4-order", "v1")

	r, err := e.beginSnapshot() // exactly what Begin() pins: a BARE pebbleReader
	if err != nil {
		t.Fatalf("beginSnapshot: %v", err)
	}
	// The premise: this reader's watermark token is its ONLY pin. If Begin ever grows a
	// trackedReader wrapper the ordering stops being load-bearing here, and this fixture
	// should be revisited rather than silently kept.
	if r.reg == nil || r.snap == nil {
		t.Fatalf("fixture: beginSnapshot returned reg=%v snap=%v — both must be set or the "+
			"ordering under test does not exist", r.reg, r.snap)
	}
	if n := e.db.Metrics().Snapshots.Count; n != 1 {
		t.Fatalf("fixture: %d open pebble snapshots after beginSnapshot, want exactly 1", n)
	}

	// Park the reader's Release on the registry lock.
	e.reg.mu.Lock()
	closeReturned := make(chan struct{})
	go func() {
		r.Close()
		close(closeReturned)
	}()
	time.Sleep(100 * time.Millisecond) // reach the blocking point; longer only helps

	select {
	case <-closeReturned:
		e.reg.mu.Unlock()
		t.Fatalf("fixture: pebbleReader.Close() completed while the registry lock was held — " +
			"it never took the lock, so this test observes nothing")
	default:
	}

	open := e.db.Metrics().Snapshots.Count
	e.reg.mu.Unlock()
	<-closeReturned

	if open != 0 {
		t.Errorf("pebbleReader.Close() was about to hand its watermark token back with %d pebble "+
			"snapshot(s) STILL OPEN. The token is what the close drain counts, so between that "+
			"release and snap.Close() the engine believes no reader is live while pebble still "+
			"has the snapshot registered — Close's phase 3 then runs e.db.Close() under it and "+
			"reports \"leaked snapshots\". Close the snapshot FIRST; release the token LAST.", open)
	}
	if n := e.db.Metrics().Snapshots.Count; n != 0 {
		t.Errorf("after pebbleReader.Close() returned, %d pebble snapshot(s) still open, want 0", n)
	}
	// And the drain agrees the reader is gone — the two halves are consistent, which is
	// what "released LAST" is supposed to buy.
	if live := e.reg.waitDrained(5 * time.Second); live != 0 {
		t.Errorf("after pebbleReader.Close() returned, the close drain still counts %d live "+
			"reader(s), want 0", live)
	}
}

// TestAuditN3BackgroundFatalDoesNotKillTheProcess pins the half of defect N3 that no
// recover in this package could ever have covered.
//
// quietLogger.Fatalf used to panic. That was a deliberate choice for ONE site —
// pebble's applyInternal (db.go:882-897) calls Logger.Fatalf on a WAL commit fault and
// then FALLS THROUGH to `return nil`, so a silent Fatalf means Apply(Sync) reports
// success for a write that never reached disk. Panicking converted that into a seal,
// synchronously, on the committer goroutine, where process()'s recover was waiting.
//
// But pebble calls Fatalf from ~36 sites, and the ENOSPC/EIO ones run on flush and
// compaction goroutines: version_set.go:671 (logAndApply's "any error here is fatal"
// arm, covering MANIFEST write/flush/sync/set-current), compaction.go:349/369/1317,
// compaction_picker.go:1962. A panic on one of those stacks unwinds a goroutine this
// package never started and cannot recover — so in a Sky app a disk-full during a
// background flush KILLED THE PROCESS.
//
// The fix cannot simply silence Fatalf either: with a latch and no consumer on the
// background path, a broken MANIFEST is swallowed and the engine goes on acking commits
// as durable. So the latch is consumed at five points, and the committer's is placed
// BEFORE the `if err != nil {seal} else {advanceDurableHi; ring append; emit}` branch —
// after it, a lost write would have advanced durableHi and fired the change feed.
//
// FIXTURE. errorfs injects on MANIFEST-* writes and syncs; a small memTableSize forces
// frequent flushes, and each flush completion runs logAndApply, which writes the
// MANIFEST on a background goroutine. Per the plan's injection rule the injector COUNTS
// its invocations and the test fails if the count is zero — an injection test that
// cannot prove it injected is indistinguishable from one that passed because nothing
// happened.
//
// UNDER THE MUTATION (restore `panic(msg)` in Fatalf) the background flush goroutine
// panics with no recover anywhere on its stack and the whole `go test` binary dies. That
// is the falsification, and it is unmissable.
func TestAuditN3BackgroundFatalDoesNotKillTheProcess(t *testing.T) {
	var armed atomic.Bool
	var injected atomic.Int64
	// TRANSIENT, and that is deliberate rather than gentle. A PERMANENTLY failing MANIFEST
	// wedges pebble: the flush cannot complete, memtables accumulate, and every writer
	// stalls inside Apply — measured, the follow-up Commit below never returns. That state
	// says nothing about whether the latch is consumed, because no consumption point is
	// ever reached. Injecting once models the realistic fault (a transient EIO) and leaves
	// the store writable, so the assertions about what the NEXT commit does are observable.
	// The fatal is still fatal: pebble has declared the store unrecoverable and the engine
	// must seal regardless of the disk having recovered.
	const injectAtMost = 1
	inj := errorfs.InjectorFunc(func(op errorfs.Op) error {
		if !armed.Load() || !strings.Contains(op.Path, "MANIFEST-") {
			return nil
		}
		switch op.Kind {
		case errorfs.OpFileWrite, errorfs.OpFileSync, errorfs.OpFileSyncData, errorfs.OpFileSyncTo:
			if injected.Load() < injectAtMost {
				injected.Add(1)
				return errorfs.ErrInjected
			}
		}
		return nil
	})
	fs := errorfs.Wrap(vfs.NewMem(), inj)

	clk := &fakeClock{}
	clk.set(31000)
	// 64 KiB memtable: a few hundred padded rows force repeated background flushes, and
	// every flush completion is a MANIFEST write on a goroutine we do not own.
	e, err := openWith(config{dir: crashDir, fs: fs, wallClock: clk.fn(), memTableSize: 64 << 10})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		armed.Store(false) // never tear down under injection
		// BOUNDED, because this engine is expected to be unclosable. Close's phase 2 does
		// wg.Wait() on the committer, and the committer is parked inside an Apply that
		// pebble will never complete once the MANIFEST flush has failed. The mem FS is
		// discarded with the test, so leaking the handle costs nothing; hanging the suite
		// would cost everything.
		done := make(chan struct{})
		go func() { _ = e.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Logf("Close did not return in 5s — expected: pebble wedges its writers behind a " +
				"failed MANIFEST flush, so the committer never leaves Apply")
		}
	}()

	// A clean prefix, so the store is real and the flush machinery is warm before arming.
	padding := strings.Repeat("p", 2048)
	for i := 0; i < 40; i++ {
		put(t, e, fmt.Sprintf("n3-clean%04d", i), padding)
	}

	armed.Store(true)

	// Enough padded rows to overflow the 64 KiB memtable a few times over. The writes run
	// on their own goroutine because a pebble whose flushes keep failing eventually stalls
	// writers, and a stall must fail this test by timeout rather than hang it.
	wrote := make(chan struct{})
	go func() {
		defer close(wrote)
		for i := 0; i < 120; i++ {
			e.Commit(CommitReq{Writes: []VersionedWrite{{
				UserKey: []byte(fmt.Sprintf("n3-fault%04d", i)), Op: OpPut, Value: []byte(padding),
			}}})
		}
	}()

	// Wait for a BACKGROUND fatal to be latched. This is the flush goroutine's Fatalf
	// arriving on a stack no recover in this package is on — the whole of N3.
	deadline := time.Now().Add(30 * time.Second)
	var latched bool
	for time.Now().Before(deadline) {
		if _, ok := e.fatal.takeFatal(); ok {
			latched = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// ── The fixture rule: prove the fault was REACHED, not merely armed. ──
	if n := injected.Load(); n == 0 {
		t.Fatalf("the MANIFEST injector fired ZERO times — no background fatal was ever provoked, "+
			"so this test proves NOTHING about background Fatalf. Fix the fixture (memTableSize, "+
			"row size, row count), do not weaken the assertions. (latched=%v)", latched)
	} else {
		t.Logf("MANIFEST injector fired %d times", n)
	}

	// ── Surviving to here IS the primary assertion. ──
	// Under the mutation the process is already gone: the panic is raised on a pebble
	// flush goroutine, and no recover in this package is on that stack.

	// Errorf, not Fatalf: the assertions below are about whether anything CONSULTS the
	// latch, which is the interesting half, and a Fatalf here would mask them.
	if !latched {
		t.Errorf("the injector fired %d times but no pebble fatal was latched in 30s — either the "+
			"injected op never reached a Logger.Fatalf site, or takeFatal CLEARED the latch on the "+
			"first read (it must not: a clear-on-read latch loses a second fatal and charges a "+
			"background fatal to an innocent batch)", injected.Load())
	}

	// A commit issued after the background fatal must NOT ack success. This is the arm a
	// fix that merely silences Fatalf fails: with the latch unconsumed, the WAL is fine,
	// Apply returns nil, and the engine keeps acking a store pebble has declared
	// unrecoverable as durable. Bounded, so a pebble write-stall reports rather than hangs.
	ack := make(chan CommitResult, 1)
	go func() {
		ack <- e.Commit(CommitReq{Writes: []VersionedWrite{{
			UserKey: []byte("n3-after-fatal"), Op: OpPut, Value: []byte("x"),
		}}})
	}()
	select {
	case res := <-ack:
		if res.Err == nil {
			t.Errorf("Commit after a background pebble fatal acked nil (commitTs %+v) — the engine "+
				"is reporting durability on a store pebble has declared unrecoverable", res.CommitTs)
		}
	case <-time.After(30 * time.Second):
		t.Errorf("Commit after a background pebble fatal never returned in 30s")
	}
	if !e.sealed.Load() {
		t.Errorf("the engine did NOT seal after a background pebble fatal — a broken MANIFEST " +
			"was swallowed and the engine would go on accepting writes")
	}

	// The latch is STICKY: neither the wait loop above nor the committer's consumption may
	// have cleared it.
	if _, ok := e.fatal.takeFatal(); !ok && latched {
		t.Errorf("the fatal latch was CLEARED by a read — takeFatal must not clear, or a second " +
			"fatal is lost and a background fatal can be charged to an innocent batch")
	}

	// The padding loop is NOT expected to finish: whichever of its commits was inside Apply
	// when the MANIFEST flush failed is parked there for good, and its caller is parked on
	// job.done. That is pebble's wedge, not a defect in this fix — and it is precisely why
	// the door check above exists, since it is the only thing that gives a NEW writer an
	// answer. Logged, not asserted, because asserting either outcome would be asserting
	// pebble's scheduling.
	select {
	case <-wrote:
	case <-time.After(5 * time.Second):
		t.Logf("the padding write loop is still parked — expected: pebble wedges the in-flight " +
			"Apply behind the failed MANIFEST flush")
	}
}

// TestAuditN3SynchronousWalFaultStillErrorsTheAck guards the OTHER direction of N3, and
// the pair is the point: neither test alone is a proof.
//
// The background test above is passed by a "fix" that simply makes Fatalf a no-op — the
// process survives precisely because nothing happens. But db.go:885 calls Fatalf on a
// fatal WAL commit error and then FALLS THROUGH to `return nil`, so silencing Fatalf
// makes Apply(Sync) return nil for a write that never reached durable storage: the
// committer acks Err:nil and the acked⇒durable contract is broken deterministically.
//
// This test injects on the WAL fsync (the *.log file, the same seam
// TestInjectedFaultsReopenConsistent uses) and asserts a commit under that fault acks an
// ERROR. It fails against a silenced Fatalf, and it fails against a latch that is never
// consumed at the committer's Apply — which is exactly what makes the five consumption
// points, and not just the latch, the fix.
//
// The injector counts its invocations and the test fails at zero, per the plan's rule.
func TestAuditN3SynchronousWalFaultStillErrorsTheAck(t *testing.T) {
	var armed atomic.Bool
	var injected atomic.Int64
	inj := errorfs.InjectorFunc(func(op errorfs.Op) error {
		isSync := op.Kind == errorfs.OpFileSync || op.Kind == errorfs.OpFileSyncData || op.Kind == errorfs.OpFileSyncTo
		if armed.Load() && isSync && strings.HasSuffix(op.Path, ".log") {
			injected.Add(1)
			return errorfs.ErrInjected
		}
		return nil
	})
	fs := errorfs.Wrap(vfs.NewMem(), inj)

	clk := &fakeClock{}
	clk.set(37000)
	e, err := openWith(config{dir: crashDir, fs: fs, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() {
		armed.Store(false)
		_ = e.Close()
	}()

	// Clean baseline: the WAL path works, so a later error is attributable to the fault.
	if res := e.Commit(CommitReq{Writes: []VersionedWrite{{
		UserKey: []byte("n3-wal-clean"), Op: OpPut, Value: []byte("v"),
	}}}); res.Err != nil {
		t.Fatalf("fixture: clean commit failed before any injection: %v", res.Err)
	}

	armed.Store(true)

	// Precondition that makes this test discriminate. Nothing is latched yet, so Commit's
	// DOOR check (consumption point 6) cannot be what produces the error below: the fault
	// happens during THIS commit's own Apply, on the committer goroutine, and only the
	// post-Apply fold (consumption point 3) can turn it into an ack. Without this
	// precondition the test would also pass with the post-Apply fold deleted — the second
	// commit would trip the door and `sawError` would still be true.
	if _, latched := e.fatal.takeFatal(); latched {
		t.Fatalf("fixture: a fatal was already latched before the first faulting commit — the " +
			"door check, not the post-Apply fold, would be under test")
	}

	first := e.Commit(CommitReq{Writes: []VersionedWrite{{
		UserKey: []byte("n3-wal-fault00"), Op: OpPut, Value: []byte("v"),
	}}})

	if n := injected.Load(); n == 0 {
		t.Fatalf("the WAL-fsync injector fired ZERO times — the commit never reached an fsync of " +
			"a *.log file, so this test proves NOTHING. Fix the fixture, do not weaken the assertion.")
	}
	if first.Err == nil {
		t.Errorf("the FIRST commit under an injected WAL-fsync fault acked Err == nil (commitTs "+
			"%+v). pebble's applyInternal (db.go:885) calls Logger.Fatalf and then FALLS THROUGH "+
			"to `return nil`, so Apply reports success for a write that never reached durable "+
			"storage. A Fatalf that is merely SILENCED produces exactly this, and so does a latch "+
			"that is never folded into the result of the committer's Apply.", first.CommitTs)
	}
	if !e.sealed.Load() {
		t.Errorf("the engine did not seal after a WAL durability fault — subsequent commits would " +
			"be accepted against a store whose last write was lost")
	}
	// And the seal holds: a later commit is refused rather than acked.
	if after := e.Commit(CommitReq{Writes: []VersionedWrite{{
		UserKey: []byte("n3-wal-after"), Op: OpPut, Value: []byte("v"),
	}}}); after.Err == nil {
		t.Errorf("a commit after the WAL durability fault acked nil (commitTs %+v)", after.CommitTs)
	}
}

// TestAuditN6UndecodablePayloadCannotHoleTheValidationWindow pins defect N6 — the
// fail-open C1's agent found, and the reason C6b is a sweep rather than a point fix.
//
// decodePayload silently returned nil on a decode error, and its docstring argued the
// case: "a malformed payload validates as 'no changes' for that job, never a false accept
// of a later txn against garbage". That answers the wrong question. Validating against
// GARBAGE would over-reject, which is safe. Validating against a HOLE under-rejects,
// which is not, and a hole is what nil produces: `pending` is the intra-batch half of the
// SSI validation window — the changes of jobs already written into THIS drain's batch —
// so a job that commits while contributing nothing to it is a job whose committed changes
// a later txn in the same window never sees.
//
// THE ASSERTION IS THE CONSEQUENCE, NOT THE SHAPE. "decodePayload returns an error" would
// also be satisfied by a fix that returns the error and then ignores it. What must be
// impossible is the HISTORY: a blind job commits a change to K, and a transaction that
// READ K at a readTs below it commits too, in the same drain window. Exactly one of them
// may commit.
//
// The control arm is what makes the main arm mean anything. With a WELL-FORMED payload
// carrying the same KeyChange, the later txn MUST be rejected — that proves `pending` and
// validate() really do catch this conflict, so "both committed" in the main arm is
// genuinely the payload's absence from the window and not some unrelated gap in
// validation.
//
// Driven through e.process directly: that is the deterministic form of the one shape that
// matters here, a single drained batch holding both jobs. Waiting for the group committer
// to coalesce them would be a race.
func TestAuditN6UndecodablePayloadCannotHoleTheValidationWindow(t *testing.T) {
	const key = "n6-key"

	// A payload that is NOT a valid payloadFmtV1 blob. Asserted, not assumed: if
	// DecodeChangelogPayload ever accepted this, the whole fixture would be vacuous.
	corrupt := []byte("n6-not-a-payload")
	if _, err := DecodeChangelogPayload(corrupt); err == nil {
		t.Fatalf("fixture: %q DECODES — the test would exercise the clean path and prove nothing", corrupt)
	}
	wellFormed := EncodeChangelogPayload([]KeyChange{{Pk: []byte(key), Op: OpPut, Record: []byte("v2")}})
	if _, err := DecodeChangelogPayload(wellFormed); err != nil {
		t.Fatalf("fixture: the control payload does not decode: %v", err)
	}

	// run drives ONE drain window holding a blind job that writes `key` with the given
	// payload, followed by a txn that READ `key` at the pre-batch readTs.
	run := func(t *testing.T, payload []byte) (blind, txn CommitResult) {
		t.Helper()
		clk := &fakeClock{}
		clk.set(41000)
		e := openDisk(t, clk.fn())

		base := put(t, e, key, "v1") // the version the txn's read observed
		readTs := e.durableHi()
		if readTs != base {
			t.Fatalf("fixture: durableHi %v != the committed ts %v", readTs, base)
		}

		blindJob := &commitJob{
			req: CommitReq{
				Writes:           []VersionedWrite{{UserKey: []byte(key), Op: OpPut, Value: []byte("v2")}},
				ChangelogPayload: payload,
			},
			done: make(chan CommitResult, 1),
		}
		txnJob := &commitJob{
			req: CommitReq{
				Writes: []VersionedWrite{{UserKey: []byte("n6-other"), Op: OpPut, Value: []byte("x")}},
				ReadTs: readTs,
				// The txn read `key` and saw the version committed at `base`. Any change to
				// `key` committed after readTs must conflict with it.
				ReadSet: &ReadSet{points: map[string]pointRead{
					key: {versionSeen: base, present: true},
				}},
			},
			done: make(chan CommitResult, 1),
		}

		e.process([]*commitJob{blindJob, txnJob}) // ONE batch, blind first — the N6 shape
		return <-blindJob.done, <-txnJob.done
	}

	t.Run("control/a-well-formed-payload-makes-the-later-txn-conflict", func(t *testing.T) {
		blind, txn := run(t, wellFormed)
		if blind.Err != nil {
			t.Fatalf("control: the blind job with a well-formed payload failed: %v", blind.Err)
		}
		if !errors.Is(txn.Err, ErrConflict) {
			t.Fatalf("control: the txn that READ %q committed (%v) even though a blind job in the "+
				"SAME drain window changed it. `pending` + validate() do not detect this conflict at "+
				"all, so the main arm below cannot distinguish a holed window from a broken "+
				"validator — fix this before trusting either.", key, txn.Err)
		}
	})

	t.Run("N6/an-undecodable-payload-must-not-let-both-commit", func(t *testing.T) {
		blind, txn := run(t, corrupt)

		// THE CONSEQUENCE. Exactly one of these two may commit.
		if blind.Err == nil && txn.Err == nil {
			t.Errorf("BOTH committed. The blind job wrote %q at %+v and the txn that had READ %q at "+
				"an earlier readTs committed at %+v in the same drain window. The blind job's payload "+
				"did not decode, so it contributed NOTHING to `pending`, and the txn validated "+
				"against a window missing that job's committed change — under-rejection, i.e. a "+
				"serializability break. An undecodable payload must fail the job CLOSED.",
				key, blind.CommitTs, key, txn.CommitTs)
		}

		// And the specific remedy: the undecodable job is the one refused, with a decode
		// error rather than ErrConflict (a retry would decode identically and loop).
		if blind.Err == nil {
			t.Errorf("the blind job with an undecodable payload COMMITTED (commitTs %+v) — its "+
				"changes can never enter the validation window or the recent-changes ring, so every "+
				"concurrent transaction below its commitTs validates against a hole", blind.CommitTs)
		} else {
			if errors.Is(blind.Err, ErrConflict) {
				t.Errorf("the undecodable job was refused with ErrConflict, which Transact RETRIES — "+
					"the payload decodes identically on every attempt, so the txn would loop to its "+
					"retry bound instead of surfacing the fault: %v", blind.Err)
			}
			if !blind.CommitTs.IsZero() {
				t.Errorf("the refused job carries commitTs %+v, want the zero value", blind.CommitTs)
			}
		}
	})
}

// TestAuditC6bBlindPathRingAppendCannotBeHoledEither is the SECOND instance of N6's class,
// in the same file, and the reason the sweep exists.
//
// processBlindPhase1's ring append used to sit AFTER Apply and read
// `derr == nil && len(chg) > 0`. A blind commit whose payload would not decode was
// therefore written durably and acked, while its changes never entered the recent-changes
// ring. Nothing in that drain window notices — an all-blind window has no `pending` and no
// validation at all. The victim is a CONCURRENT open transaction in a DIFFERENT window,
// whose readTs is below this commit: it validates against ring.after(readTs), and the
// change is not there. Same under-rejection, different door.
//
// The assertion is again the consequence: an open transaction that READ the key must not
// be able to commit alongside a durable blind change to it. Under the pre-fix code the
// blind job acks nil, the ring never learns of it, and the transaction commits clean.
func TestAuditC6bBlindPathRingAppendCannotBeHoledEither(t *testing.T) {
	const key = "c6b-key"
	clk := &fakeClock{}
	clk.set(43000)
	e := openDisk(t, clk.fn())

	base := put(t, e, key, "v1")
	readTs := e.durableHi()

	// An ALL-BLIND drain window (no ReadSet anywhere) → processBlindPhase1, the path under
	// test. The payload does not decode.
	corrupt := []byte("c6b-not-a-payload")
	if _, err := DecodeChangelogPayload(corrupt); err == nil {
		t.Fatalf("fixture: %q decodes — the test would prove nothing", corrupt)
	}
	blindJob := &commitJob{
		req: CommitReq{
			Writes:           []VersionedWrite{{UserKey: []byte(key), Op: OpPut, Value: []byte("v2")}},
			ChangelogPayload: corrupt,
		},
		done: make(chan CommitResult, 1),
	}
	e.process([]*commitJob{blindJob})
	blind := <-blindJob.done

	// Now a transaction that began BEFORE that window (readTs = base) and read the key.
	txnJob := &commitJob{
		req: CommitReq{
			Writes: []VersionedWrite{{UserKey: []byte("c6b-other"), Op: OpPut, Value: []byte("x")}},
			ReadTs: readTs,
			ReadSet: &ReadSet{points: map[string]pointRead{
				key: {versionSeen: base, present: true},
			}},
		},
		done: make(chan CommitResult, 1),
	}
	e.process([]*commitJob{txnJob})
	txn := <-txnJob.done

	if blind.Err == nil && txn.Err == nil {
		t.Errorf("BOTH committed. The all-blind window durably wrote %q at %+v with a payload that "+
			"does not decode, so the recent-changes ring never learned of the change; the "+
			"transaction that had READ %q at readTs %+v then validated against ring.after(readTs) "+
			"— a window missing a committed change — and committed at %+v. The ring append is not "+
			"an optimisation on this path; it is what makes a concurrent txn's window complete.",
			key, blind.CommitTs, key, readTs, txn.CommitTs)
	}
	if blind.Err == nil {
		t.Errorf("the blind job with an undecodable payload committed at %+v — decode BEFORE the "+
			"batch is built, so the only available remedy (abort) is still reachable", blind.CommitTs)
	}
}

// TestAuditC6bAdvanceOnAnUnknownTokenIsAnError is one row of the C6b sweep.
//
// watermarkRegistry.Advance used to return nil after doing nothing when the token was not
// live. That is fail-open in N6's direction — the caller is told a safety property holds
// when it does not. Advance's contract is "this reader's readTs has moved forward and the
// GC floor is pinned at it"; a nil return for a token that is not in `live` pins nothing,
// so a reactive binding would go on reading at a readTs GC is free to collect.
func TestAuditC6bAdvanceOnAnUnknownTokenIsAnError(t *testing.T) {
	clk := &fakeClock{}
	clk.set(47000)
	e := openDisk(t, clk.fn())
	ts := put(t, e, "c6b-adv", "v1")

	tok, _, err := e.reg.Register()
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	// A live token advances normally — the fixture proves the error below is about
	// liveness and not about the readTs or the threshold.
	if err := e.reg.Advance(tok, ts); err != nil {
		t.Fatalf("Advance on a LIVE token = %v, want nil", err)
	}

	e.reg.Release(tok)
	if err := e.reg.Advance(tok, ts); !errors.Is(err, ErrUnknownReader) {
		t.Fatalf("Advance on a RELEASED token = %v, want ErrUnknownReader. Returning nil tells "+
			"the caller its readTs is registered and the GC floor is pinned at it when nothing "+
			"is pinned — the caller then reads at a readTs GC may collect underneath it", err)
	}
	if err := e.reg.Advance(ReaderToken(9999), ts); !errors.Is(err, ErrUnknownReader) {
		t.Fatalf("Advance on a never-issued token = %v, want ErrUnknownReader", err)
	}
}

// TestAuditC6bCorruptColdStartSeedRaisesTheRingFloor is one row of the C6b sweep.
//
// openWith seeds the recent-changes ring from the durable changelog tail, and used to
// drop both a Tail error and a per-entry decode error on the floor: `terr == nil` /
// `derr == nil` with no else. The ring IS the SSI validation window, so a partially
// seeded ring whose floor still claims to cover the whole range is a window with a hole —
// N6's shape at open time.
//
// Refusing to open would be too harsh (the store itself is fine), and the correct
// degradation already exists: a readTs below `floor` reports spilled=true and the txn
// validates via the durable Changelog.Tail instead. So the remedy is to make the ring
// ADMIT the range it could not seed, by raising the floor to persistedHi.
//
// The fixture corrupts a changelog entry's PAYLOAD behind the engine's back — the key
// still parses, so Tail returns the entry and DecodeChangelogPayload is what fails, which
// is the arm that had no error path at all.
func TestAuditC6bCorruptColdStartSeedRaisesTheRingFloor(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{}
	clk.set(53000)

	e1, err := openWith(config{dir: dir, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ts := commitWithLog(t, e1, "c6b-seed", "v1", "chg-seed")
	persistedHi := e1.NowTs()
	if err := e1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if persistedHi.IsZero() || persistedHi != ts {
		t.Fatalf("fixture: persistedHi %v / commitTs %v — the floor assertion needs a real ts", persistedHi, ts)
	}

	// Corrupt the PAYLOAD (not the key) of that changelog entry.
	raw, err := pebble.Open(dir, &pebble.Options{Comparer: skydbComparer, Logger: quietLogger{}})
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if err := raw.Set(encodeChangelogKey(ts), []byte("not-a-payload"), pebble.Sync); err != nil {
		t.Fatalf("corrupt changelog payload: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("raw close: %v", err)
	}

	e2, err := openWith(config{dir: dir, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("reopen over a corrupt changelog payload = %v; the store itself is intact and "+
			"the ring has a correct degradation, so refusing to open is the wrong remedy", err)
	}
	defer e2.Close()

	// The seed could not be completed, so the ring must not claim to cover below
	// persistedHi. Any reader below it now takes after()'s spilled branch and validates
	// against the durable changelog — which fails closed on the same corrupt entry
	// (changelog.Tail + changelogTailChanges → ErrConflict) rather than silently.
	if e2.recent.floor != persistedHi {
		t.Fatalf("after a failed cold-start seed the ring floor is %+v, want persistedHi %+v. A "+
			"lower floor means after(readTs) answers `not spilled` for a range the ring does NOT "+
			"hold, so a transaction validates against a window with a hole — under-rejection",
			e2.recent.floor, persistedHi)
	}
	if _, spilled := e2.recent.after(HLC{WallMs: 53000, Logical: 0}); !spilled {
		t.Fatalf("a readTs below the raised floor did not report spilled — the floor is not " +
			"actually diverting those validations to the durable changelog")
	}
}

// TestAuditN4ChangelogAndGCDoNotRaceCloseIntoAPanic is the reproduction that shipped
// broken: Engine.Changelog() handed back a raw *pebble.DB with NO closed-check and NO
// pin, and Engine.GC() checked isClosed() and then used the handle anyway. Both are the
// N4 class the close-drain exists to close, and both were reachable from the EXPORTED
// surface — a caller holding a Changelog across Close (or running a GC pass on its own
// goroutine) took an unrecovered "pebble: closed" panic, on ITS goroutine, where nothing
// in this package can catch it.
//
// The shape is deliberate. The workers keep calling AFTER Close has returned, so on the
// broken code the panic is not a narrow interleaving to be won — it is certain: every
// call after phase 3 hits a closed handle. That is what makes this a gate rather than a
// lottery (it detonated on round 0 when the fix was reverted). On the fixed code the same
// calls are answered with ErrClosed, which is the OTHER half of the assertion: the remedy
// must be a typed error, not a hang and not a silent empty result.
//
// The verdict per worker is (panicked, unexpected-error). Anything other than nil or
// ErrClosed fails — an engine that answered a post-close Tail with (nil, nil) would look
// like an empty changelog to a caller, which is the fail-open version of the same bug.
func TestAuditN4ChangelogAndGCDoNotRaceCloseIntoAPanic(t *testing.T) {
	e, err := openWith(config{dir: t.TempDir()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Seed: something for Tail to walk and for the GC pass to scan.
	for i := 0; i < 8; i++ {
		r := e.Commit(CommitReq{
			Writes:           []VersionedWrite{{UserKey: dataUserKey("c", fmt.Sprintf("k%d", i)), Op: OpPut, Value: []byte("v")}},
			ChangelogPayload: logPayload(fmt.Sprintf("seed-%d", i)),
		})
		if r.Err != nil {
			t.Fatalf("seed commit %d: %v", i, r.Err)
		}
	}

	type verdict struct {
		what     string
		panicked any
		badErr   error
	}
	const workers = 3
	const ops = 3
	stop := make(chan struct{})
	results := make(chan verdict, workers*ops)

	spin := func(what string, op func() error) {
		go func() {
			v := verdict{what: what}
			defer func() {
				v.panicked = recover()
				results <- v
			}()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if err := op(); err != nil && !errors.Is(err, ErrClosed) {
					v.badErr = err
					return
				}
			}
		}()
	}

	// A Changelog obtained BEFORE Close and held across it — the exported-value-outlives-
	// the-engine shape, and the one the Judge reproduced.
	held := e.Changelog()
	for i := 0; i < workers; i++ {
		spin("held Changelog().Tail", func() error {
			_, err := held.Tail(HLC{})
			return err
		})
		spin("fresh Changelog().Tail", func() error {
			_, err := e.Changelog().Tail(HLC{})
			return err
		})
		spin("GC", func() error {
			_, err := e.GC()
			return err
		})
	}

	// Let the workers get going, then close underneath them and let them keep calling.
	time.Sleep(20 * time.Millisecond)
	closeErr := e.closeWithin(5 * time.Second)
	time.Sleep(30 * time.Millisecond) // post-close calls — certain to hit the closed handle
	close(stop)

	deadline := time.After(30 * time.Second)
	for i := 0; i < workers*ops; i++ {
		select {
		case v := <-results:
			if v.panicked != nil {
				t.Errorf("%s PANICKED racing Close: %v\n"+
					"A pebble handle operation on a closed DB panics unconditionally, on the CALLER's "+
					"goroutine. The remedy is the N4 check-and-pin (pinIfOpen): check `closed` and take "+
					"the liveness pin under ONE closeMu.RLock, so Close's drain waits for the call or "+
					"the call is refused with ErrClosed.", v.what, v.panicked)
			}
			if v.badErr != nil {
				t.Errorf("%s returned %v; want nil or ErrClosed", v.what, v.badErr)
			}
		case <-deadline:
			t.Fatalf("worker %d/%d did not report within 30s — a close-drain deadlock is as much a "+
				"failure here as a panic", i+1, workers*ops)
		}
	}
	if closeErr != nil {
		t.Fatalf("Close returned %v. With the pins held only for the duration of each call, the "+
			"bounded drain must complete: a persistent ErrReadersLive means a pin is being leaked "+
			"on some path (an early return without its unpin).", closeErr)
	}

	// The barrier holds after Close has fully returned, too — the terminal state ANSWERS,
	// it does not panic. Guarded so a regression here is a test failure rather than a
	// panic that takes the whole test binary (and every later test) down with it.
	terminal := func(what string, op func() error) {
		t.Helper()
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s PANICKED on a fully closed engine: %v", what, r)
				}
			}()
			err = op()
		}()
		if err != nil && !errors.Is(err, ErrClosed) {
			t.Errorf("%s on a fully closed engine = %v, want ErrClosed", what, err)
		} else if err == nil {
			t.Errorf("%s on a fully closed engine returned NO error; a terminal engine must "+
				"answer ErrClosed, not a silent empty result", what)
		}
	}
	terminal("Tail", func() error { _, err := held.Tail(HLC{}); return err })
	terminal("GC", func() error { _, err := e.GC(); return err })
}

// TestAuditPostAckDurabilityPanicIsNotSilentlyAbsorbed pins the POST-ACK arm of both
// commit paths' durability recover.
//
// processBlindPhase1's guard used to read `if r := recover(); r != nil && !acked`. recover()
// is not conditional on the `&&` — it runs first and CONSUMES the panic — so once the acks
// had gone out, a panic was swallowed with no seal, no repanic and no log, and the single
// writer goroutine returned to its range loop from an unexplained fault. The window is
// real: `defer b.Close()` is registered AFTER that defer and therefore runs BEFORE it, on
// every path including the fully-acked one.
//
// The fault is injected (postAckFaultInject) because nothing else reaches that window on a
// healthy handle — the genuine sources are broken pebble invariants. The test drives
// process() directly on the TEST goroutine (the mkJob/e.process shape the other white-box
// commit tests use), so the re-panic is observable instead of taking the process down.
//
// Both halves are asserted: the panic must escape (not be absorbed) AND the engine must be
// sealed, and the ack that already went out must still carry its real result — a post-ack
// fault must not retroactively rewrite a durable commit's verdict.
func TestAuditPostAckDurabilityPanicIsNotSilentlyAbsorbed(t *testing.T) {
	catch := func(t *testing.T, e *pebbleEngine, jobs []*commitJob) any {
		t.Helper()
		var got any
		func() {
			defer func() { got = recover() }()
			e.process(jobs)
		}()
		return got
	}

	t.Run("blind-path", func(t *testing.T) {
		clk := &fakeClock{}
		clk.set(71000)
		e := openDisk(t, clk.fn())

		postAckFaultInject.Store(true)
		t.Cleanup(func() { postAckFaultInject.Store(false) })

		job := mkJob("post-ack-blind", "v1", "cl-blind")
		got := catch(t, e, []*commitJob{job})
		if got == nil {
			t.Fatalf("a panic raised AFTER processBlindPhase1's acks went out was SILENTLY ABSORBED. " +
				"recover() consumes the panic unconditionally, so `r != nil && !acked` is not a " +
				"no-op in the acked case — it is a fail-open that hides an unexplained fault in the " +
				"single writer goroutine. The post-ack arm must seal and re-panic.")
		}
		if !e.sealed.Load() {
			t.Fatalf("the post-ack fault did not SEAL the engine; a recovered durability fault must " +
				"leave the engine refusing writes, not accepting them")
		}
		res := <-job.done
		if res.Err != nil {
			t.Fatalf("the ack that had ALREADY gone out was rewritten to %v; a fault after the ack "+
				"must not retroactively fail a commit that was applied and acked", res.Err)
		}
	})

	t.Run("txn-path", func(t *testing.T) {
		clk := &fakeClock{}
		clk.set(72000)
		e := openDisk(t, clk.fn())

		key := "post-ack-txn"
		base := put(t, e, key, "v1")
		readTs := e.durableHi()

		postAckFaultInject.Store(true)
		t.Cleanup(func() { postAckFaultInject.Store(false) })

		// A txn job with a clean read-set → validates, applies, acks; then the seam fires.
		txnJob := &commitJob{
			req: CommitReq{
				Writes:  []VersionedWrite{{UserKey: []byte(key), Op: OpPut, Value: []byte("v2")}},
				ReadTs:  readTs,
				ReadSet: &ReadSet{points: map[string]pointRead{key: {versionSeen: base, present: true}}},
			},
			done: make(chan CommitResult, 1),
		}
		got := catch(t, e, []*commitJob{txnJob})
		if got == nil {
			t.Fatalf("a panic raised AFTER processTxn's acks went out was SILENTLY ABSORBED. With " +
				"every job already in the acked set there is nobody left to inform, so absorbing " +
				"makes the fault unobservable in the process it happened in.")
		}
		if !e.sealed.Load() {
			t.Fatalf("the post-ack fault did not SEAL the engine on the transactional path")
		}
		res := <-txnJob.done
		if res.Err != nil {
			t.Fatalf("the ack that had ALREADY gone out was rewritten to %v", res.Err)
		}
	})
}

// TestAuditN4GCPassIsPinnedAgainstAConcurrentClose gates the OTHER half of GAP 1, and it
// needs a different shape from the Changelog one — which is the point of it existing.
//
// gc.go's `if e.isClosed()` DOES answer a call made after Close has returned, so the
// spin-workers-then-Close shape (TestAuditN4ChangelogAndGCDoNotRaceCloseIntoAPanic)
// passes against the broken GC code: verified by mutation, that test stays green with the
// pin reverted. The GC defect is a genuine TOCTOU — the check is a check with no pin, so
// Close's phase 3 can run e.db.Close() at any point AFTER it returned false, and every
// later handle op in the pass (NewBatch/NewIter, the side Apply, persistThreshold's Apply)
// panics unconditionally. A gate for it has to put Close INSIDE the window, not after it.
//
// So the window is made wide and the wideness is MEASURED rather than assumed: the data
// keyspace is loaded until one control pass takes a real, timed duration, and the fixture
// FAILS (rather than silently passing) if a pass is too quick to hold Close inside it.
// Close is then called one fifth of the way through a pass. Under the mutation that lands
// db.Close() mid-pass every time; under the fix Close's phase-2 drain waits on the GC pin
// and the pass completes normally — which is the second assertion here, since a drain that
// did NOT wait would show up as a nil error from a pass that panicked.
func TestAuditN4GCPassIsPinnedAgainstAConcurrentClose(t *testing.T) {
	e, err := openWith(config{dir: t.TempDir()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// One real commit so durableHi > 0 → the advanced threshold T is non-zero and GC does
	// NOT take its `T.IsZero()` early return (which would skip the whole scan).
	if r := e.Commit(CommitReq{Writes: []VersionedWrite{{UserKey: dataUserKey("c", "anchor"), Op: OpPut, Value: []byte("v")}}}); r.Err != nil {
		t.Fatalf("anchor commit: %v", r.Err)
	}

	// Bulk-load ONE version per user-key, directly through the handle: a single version is
	// never collectible (the newest version below T is always kept), so every pass scans the
	// same keyspace and the measured duration stays valid for the timed pass below.
	loaded := 0
	load := func(n int) {
		t.Helper()
		b := e.db.NewBatch()
		defer b.Close()
		for i := 0; i < n; i++ {
			k := encodeDataKey([]byte(fmt.Sprintf("bulk/%08d", loaded+i)), HLC{WallMs: 1, Logical: 1})
			if err := b.Set(k, []byte{markerPut}, nil); err != nil {
				t.Fatalf("bulk set: %v", err)
			}
		}
		if err := e.db.Apply(b, pebble.NoSync); err != nil {
			t.Fatalf("bulk apply: %v", err)
		}
		loaded += n
	}

	const wantPass = 60 * time.Millisecond
	var pass time.Duration
	for attempt := 0; attempt < 4; attempt++ {
		load(300_000)
		t0 := time.Now()
		if _, err := e.GC(); err != nil {
			t.Fatalf("control GC pass: %v", err)
		}
		if pass = time.Since(t0); pass >= wantPass {
			break
		}
	}
	if pass < wantPass {
		t.Fatalf("fixture: a GC pass over %d keys takes only %s (want >= %s). The gate needs a pass "+
			"long enough for Close's phase 3 to land INSIDE it; raise the load per attempt.",
			loaded, pass, wantPass)
	}

	type gcOutcome struct {
		err      error
		panicked any
	}
	out := make(chan gcOutcome, 1)
	go func() {
		var o gcOutcome
		defer func() {
			o.panicked = recover()
			out <- o
		}()
		_, o.err = e.GC()
	}()

	// One fifth into the pass: past the entry check by a wide margin, and with ~80% of the
	// pass — the NewIter, the whole scan, the side Apply — still ahead of it.
	time.Sleep(pass / 5)
	var closeErr error
	func() {
		// Guarded because the unpinned pass panics Close ITSELF, not only the pass: pebble's
		// DB.Close unrefs the file cache and panics "element has outstanding references" when
		// an iterator from the racing pass is still live (file_cache.go → genericcache). That
		// is the same defect seen from the other side, and it deserves a named failure rather
		// than an unrecovered panic that takes every later test in the binary with it.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Close PANICKED with an unpinned GC pass in flight: %v\n"+
					"The pass holds a live Pebble iterator that the phase-2 drain knows nothing "+
					"about, because gc.go took no pin. The drain must be able to see the pass.", r)
			}
		}()
		closeErr = e.closeWithin(60 * time.Second)
	}()

	select {
	case o := <-out:
		if o.panicked != nil {
			t.Fatalf("the in-flight GC pass PANICKED when Close closed the handle underneath it: %v\n"+
				"`if e.isClosed()` is a CHECK WITH NO PIN: nothing stops phase 3 from running "+
				"e.db.Close() after it returns false, and the pass's next handle operation "+
				"(NewBatch/NewIter, the side Apply, persistThreshold's Apply) panics unconditionally "+
				"on the caller's goroutine. The remedy is pinIfOpen held for the WHOLE pass.", o.panicked)
		}
		if o.err != nil && !errors.Is(o.err, ErrClosed) {
			t.Fatalf("the in-flight GC pass returned %v; want nil (it was pinned and completed) or ErrClosed", o.err)
		}
	case <-time.After(90 * time.Second):
		t.Fatalf("the in-flight GC pass never reported — a pin held across a wait Close depends on " +
			"is a deadlock, which fails this gate exactly as a panic does")
	}
	if closeErr != nil {
		t.Fatalf("Close returned %v; with the pass pinned, the bounded drain must wait it out and "+
			"then close cleanly", closeErr)
	}
}
