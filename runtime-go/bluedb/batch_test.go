package bluedb

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"
)

func openNoCkpt(t *testing.T, path string) *DB {
	t.Helper()
	db, err := Open(path, Options{Sync: false, CheckpointEvery: 0})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return db
}

func getStr(db *DB, k string) (string, bool) {
	v, ok := db.Get([]byte(k))
	return string(v), ok
}

// A committed batch survives a clean reopen with ALL its mutations applied.
func TestWriteBatchAtomicReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.blue")
	db := openNoCkpt(t, path)
	_ = db.Put([]byte("old-index"), []byte("stale"))
	// A representative index-maintenance batch: drop old index, add new, write primary.
	b := NewBatch().
		Delete([]byte("old-index")).
		Put([]byte("new-index"), []byte("pk-1")).
		Put([]byte("pk-1"), []byte(`{"email":"ada@x"}`))
	if err := db.WriteBatch(b); err != nil {
		t.Fatalf("writebatch: %v", err)
	}
	db.Close()

	db2 := openNoCkpt(t, path)
	defer db2.Close()
	if _, ok := getStr(db2, "old-index"); ok {
		t.Fatal("old-index should be deleted by the batch")
	}
	if v, ok := getStr(db2, "new-index"); !ok || v != "pk-1" {
		t.Fatalf("new-index: %q %v", v, ok)
	}
	if v, ok := getStr(db2, "pk-1"); !ok || v != `{"email":"ada@x"}` {
		t.Fatalf("pk-1: %q %v", v, ok)
	}
}

// A TORN batch record (crash mid-write) applies NONE of its mutations — never a
// subset — while everything committed before it stays intact.
func TestWriteBatchTornAppliesNone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.blue")
	db := openNoCkpt(t, path)
	_ = db.Put([]byte("a"), []byte("1")) // committed before the batch
	b := NewBatch().Put([]byte("b"), []byte("2")).Put([]byte("c"), []byte("3"))
	if err := db.WriteBatch(b); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Simulate a crash mid-batch-record: cut the last byte of the WAL (the batch
	// record is the tail) so its CRC/length no longer validates.
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, fi.Size()-1); err != nil {
		t.Fatal(err)
	}

	db2 := openNoCkpt(t, path)
	defer db2.Close()
	if v, ok := getStr(db2, "a"); !ok || v != "1" {
		t.Fatalf("committed key a must survive: %q %v", v, ok)
	}
	if _, ok := getStr(db2, "b"); ok {
		t.Fatal("torn batch: b must NOT be applied")
	}
	if _, ok := getStr(db2, "c"); ok {
		t.Fatal("torn batch: c must NOT be applied (no subset)")
	}
}

// A corrupted mutCount decodes as torn (whole record dropped), not a partial batch.
func TestBatchDecodeMutCountLie(t *testing.T) {
	// Encode a 2-mutation batch, then lie: bump mutCount to 3 → decodeBatch must
	// reject (the 3rd mut overflows the payload).
	rec := encodeRecord(entry{seq: 5, op: opBatch, muts: []mutation{
		{op: opPut, key: []byte("k1"), value: []byte("v1")},
		{op: opPut, key: []byte("k2"), value: []byte("v2")},
	}})
	// payload starts at rec[8]; mutCount is payload bytes [9:13].
	rec[8+9] = 3 // mutCount low byte 2 → 3
	// recompute CRC so it isn't caught as a CRC mismatch — we want to prove the
	// STRUCTURAL check rejects a valid-CRC-but-lying record.
	fixCRC(rec)
	payload := rec[8:]
	if _, ok := decodePayload(payload); ok {
		t.Fatal("mutCount=3 with 2 muts must decode as torn (false)")
	}

	// And trailing bytes (mutCount too small) must also reject.
	rec2 := encodeRecord(entry{seq: 6, op: opBatch, muts: []mutation{
		{op: opPut, key: []byte("k1"), value: []byte("v1")},
		{op: opPut, key: []byte("k2"), value: []byte("v2")},
	}})
	rec2[8+9] = 1 // claim 1 mut → the 2nd mut's bytes are trailing
	fixCRC(rec2)
	if _, ok := decodePayload(rec2[8:]); ok {
		t.Fatal("mutCount=1 with 2 muts of data must decode as torn (trailing bytes)")
	}

	// mutCount==0 is never written (empty batch is an API no-op) → torn.
	rec0 := encodeRecord(entry{seq: 7, op: opBatch, muts: []mutation{
		{op: opPut, key: []byte("k"), value: []byte("v")},
	}})
	rec0[8+9] = 0
	fixCRC(rec0)
	if _, ok := decodePayload(rec0[8:]); ok {
		t.Fatal("mutCount==0 must decode as torn")
	}

	// A HUGE mutCount with a valid CRC must be rejected by the pre-alloc bound —
	// no OOM, no panic (snapshot F4 parity).
	recH := encodeRecord(entry{seq: 8, op: opBatch, muts: []mutation{
		{op: opPut, key: []byte("k"), value: []byte("v")},
	}})
	recH[8+9], recH[8+10], recH[8+11], recH[8+12] = 0xff, 0xff, 0xff, 0xff // count = 4.29e9
	fixCRC(recH)
	if _, ok := decodePayload(recH[8:]); ok {
		t.Fatal("huge mutCount must decode as torn (bounded before alloc)")
	}
}

// Intra-batch: applied in staged order, last-writer-wins, identical live and on reopen.
func TestWriteBatchIntraOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.blue")
	db := openNoCkpt(t, path)
	b := NewBatch().
		Put([]byte("k"), []byte("v1")).
		Put([]byte("k"), []byte("v2")). // overwrites v1
		Put([]byte("gone"), []byte("x")).
		Delete([]byte("gone")) // removes it
	if err := db.WriteBatch(b); err != nil {
		t.Fatal(err)
	}
	check := func(db *DB, where string) {
		if v, ok := getStr(db, "k"); !ok || v != "v2" {
			t.Fatalf("%s: k should be v2, got %q %v", where, v, ok)
		}
		if _, ok := getStr(db, "gone"); ok {
			t.Fatalf("%s: gone should be deleted", where)
		}
	}
	check(db, "live")
	db.Close()
	db2 := openNoCkpt(t, path)
	defer db2.Close()
	check(db2, "reopen")
}

func TestWriteBatchGuards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.blue")
	db, _ := Open(path, Options{Sync: false, MaxValueBytes: 16})
	defer db.Close()
	// empty batch = no-op, no error
	if err := db.WriteBatch(NewBatch()); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
	if err := db.WriteBatch(nil); err != nil {
		t.Fatalf("nil batch: %v", err)
	}
	// a mutation over MaxValueBytes → ErrTooLarge
	big := NewBatch().Put([]byte("k"), make([]byte, 100))
	if err := db.WriteBatch(big); err != ErrTooLarge {
		t.Fatalf("oversize mut: want ErrTooLarge, got %v", err)
	}
}

// Fuzz-lite: random single ops + batches, reopen must match an in-memory oracle.
func TestWriteBatchOracleReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.blue")
	db := openNoCkpt(t, path)
	oracle := map[string]string{}
	apply := func(op uint8, k, v string) {
		if op == opDelete {
			delete(oracle, k)
		} else {
			oracle[k] = v
		}
	}
	for i := 0; i < 500; i++ {
		if i%3 == 0 {
			// a batch of 2-4 muts
			b := NewBatch()
			n := 2 + i%3
			for j := 0; j < n; j++ {
				k := fmt.Sprintf("k%d", (i*7+j)%40)
				if (i+j)%5 == 0 {
					b.Delete([]byte(k))
					apply(opDelete, k, "")
				} else {
					v := fmt.Sprintf("v%d-%d", i, j)
					b.Put([]byte(k), []byte(v))
					apply(opPut, k, v)
				}
			}
			if err := db.WriteBatch(b); err != nil {
				t.Fatal(err)
			}
		} else {
			k := fmt.Sprintf("k%d", i%40)
			v := fmt.Sprintf("s%d", i)
			_ = db.Put([]byte(k), []byte(v))
			apply(opPut, k, v)
		}
	}
	db.Close()

	db2 := openNoCkpt(t, path)
	defer db2.Close()
	got := map[string]string{}
	db2.ForEach(func(k, v []byte) bool { got[string(k)] = string(v); return true })
	if len(got) != len(oracle) {
		t.Fatalf("key count: reopen %d vs oracle %d", len(got), len(oracle))
	}
	for k, want := range oracle {
		if got[k] != want {
			t.Fatalf("key %q: reopen %q vs oracle %q", k, got[k], want)
		}
	}
}

// A write fault on the batch record rolls the batch back (F1): none of its muts
// apply, it reports the error, the engine continues, and reopen excludes it.
func TestWriteBatchWriteFaultRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "b.blue")
	db, err := Open(path, Options{Sync: true, CheckpointEvery: 0, walWrap: func(w walFile) walFile {
		return &faultyFile{inner: w, failOn: 2} // write #1 = the Put; #2 = the batch
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("a"), []byte("1")); err != nil {
		t.Fatal(err) // write #1 succeeds
	}
	b := NewBatch().Put([]byte("b"), []byte("2")).Put([]byte("c"), []byte("3"))
	if err := db.WriteBatch(b); err == nil {
		t.Fatal("batch write should have faulted")
	}
	// live: a intact, b/c never applied
	if v, ok := db.Get([]byte("a")); !ok || string(v) != "1" {
		t.Fatalf("a must survive: %q %v", v, ok)
	}
	if _, ok := db.Get([]byte("b")); ok {
		t.Fatal("b must not be applied after a faulted batch")
	}
	db.Close()

	db2 := openNoCkpt(t, path)
	defer db2.Close()
	if v, ok := getStr(db2, "a"); !ok || v != "1" {
		t.Fatalf("reopen a: %q %v", v, ok)
	}
	if _, ok := getStr(db2, "b"); ok {
		t.Fatal("reopen: b must be absent")
	}
}

// fixCRC recomputes the record's CRC over its (possibly-edited) payload.
func fixCRC(rec []byte) {
	binary.LittleEndian.PutUint32(rec[0:4], crc32.ChecksumIEEE(rec[8:]))
}
