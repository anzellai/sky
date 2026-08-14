package bluedb

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2"
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
