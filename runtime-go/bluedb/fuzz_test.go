package bluedb

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// appendTornTail writes an 8-byte header claiming a large payload, then too few
// bytes — a guaranteed torn record (short payload) — to simulate a crash mid-
// write. Recovery must stop at it and drop it, leaving the acked prefix intact.
func appendTornTail(t *testing.T, path string, extra int) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], 0xDEADBEEF) // crc (irrelevant — torn first)
	binary.LittleEndian.PutUint32(hdr[4:8], 65536)      // claim a big payload
	_, _ = f.Write(hdr[:])
	_, _ = f.Write(make([]byte, extra)) // but only `extra` (< 65536) bytes → torn
	_ = f.Close()
}

// applyOps runs a random op sequence against db, mirroring the effect of every
// ACKED (nil-returning) op into oracle. Returns the ops applied.
func fuzzOps(t *testing.T, db *DB, oracle map[string]string, rng *rand.Rand, n, keyspace int) {
	t.Helper()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k%d", rng.Intn(keyspace))
		switch rng.Intn(12) {
		case 0, 1: // delete
			if err := db.Delete([]byte(key)); err == nil {
				delete(oracle, key)
			}
		case 2: // checkpoint
			_ = db.Checkpoint()
		default: // put
			val := fmt.Sprintf("v%d-%d", i, rng.Intn(1_000_000))
			if err := db.Put([]byte(key), []byte(val)); err == nil {
				oracle[key] = val
			}
		}
	}
}

func assertMatchesOracle(t *testing.T, tag string, db *DB, oracle map[string]string) {
	t.Helper()
	if db.Len() != len(oracle) {
		t.Fatalf("%s: Len=%d want %d", tag, db.Len(), len(oracle))
	}
	for k, v := range oracle {
		got, ok := db.Get([]byte(k))
		if !ok || string(got) != v {
			t.Fatalf("%s: key %q = %q,%v want %q", tag, k, got, ok, v)
		}
	}
}

// FUZZ 1 — the core recovery property: for ANY random sequence of Put/Delete/
// Checkpoint, reopening the store recovers exactly the acked state. Catches
// recovery / checkpoint / delete / seq bugs across a huge input space.
func TestFuzzOpsReopenMatchesOracle(t *testing.T) {
	// Mostly NoSync for speed (Close flushes the OS buffer, so a graceful-restart
	// still exercises the full recovery path) with a Sync subset for the fsync'd
	// durability path.
	for seed := int64(1); seed <= 60; seed++ {
		rng := rand.New(rand.NewSource(seed))
		path := filepath.Join(t.TempDir(), "app.blue")
		oracle := map[string]string{}

		opts := Options{
			Sync:            seed%5 == 0, // ~20% Sync (fsync'd), rest NoSync (fast)
			CheckpointEvery: []int{0, 1, 7, 50}[rng.Intn(4)],
			MaxValueBytes:   []int{0, 0, 4096}[rng.Intn(3)],
		}
		db, err := Open(path, opts)
		if err != nil {
			t.Fatalf("seed %d: open: %v", seed, err)
		}
		fuzzOps(t, db, oracle, rng, 30+rng.Intn(150), 30)
		// Reopen (restart). In Sync mode every acked write is already fsync'd, so
		// a graceful Close and a crash recover the same durable state; Close here
		// avoids leaking the committer while proving the recovery property.
		if err := db.Close(); err != nil {
			t.Fatalf("seed %d: close: %v", seed, err)
		}
		// Crash sim: on ~half the seeds, leave a torn tail on the WAL (an
		// interrupted write). Recovery must stop at it and still recover the full
		// acked oracle — this fuzzes the CRC/length framing + validEnd-truncate.
		if rng.Intn(2) == 0 {
			appendTornTail(t, path, rng.Intn(16))
		}
		db2, err := Open(path)
		if err != nil {
			t.Fatalf("seed %d: reopen: %v", seed, err)
		}
		assertMatchesOracle(t, fmt.Sprintf("seed %d", seed), db2, oracle)

		// A second round of ops on the reopened DB must also recover — exercises
		// post-recovery seq continuation + snapshot-then-more-writes.
		fuzzOps(t, db2, oracle, rng, 20+rng.Intn(80), 30)
		if err := db2.Close(); err != nil {
			t.Fatalf("seed %d: close2: %v", seed, err)
		}
		db3, err := Open(path)
		if err != nil {
			t.Fatalf("seed %d: reopen2: %v", seed, err)
		}
		assertMatchesOracle(t, fmt.Sprintf("seed %d round2", seed), db3, oracle)
		db3.Close()
	}
}

// FUZZ 2 — crash DURING a write: a fault fires at a random point in the
// sequence. Every op that returned nil is acked and must survive; the failed op
// must NOT corrupt state or resurrect/lose an acked write. The DB rolls the
// failed batch back and continues.
func TestFuzzWriteFaultPreservesAckedState(t *testing.T) {
	// NoSync: the fault is on Write and the rollback is on the file, both
	// independent of fsync — so NoSync exercises the crash-during-write path
	// fully, and fast.
	for seed := int64(1); seed <= 40; seed++ {
		rng := rand.New(rand.NewSource(seed))
		path := filepath.Join(t.TempDir(), "app.blue")
		oracle := map[string]string{}

		failAt := int32(5 + rng.Intn(150)) // fail one write mid-stream
		var ff *faultyFile
		db, err := Open(path, Options{Sync: false, CheckpointEvery: 1 + rng.Intn(40),
			walWrap: func(w walFile) walFile {
				ff = &faultyFile{inner: w, failOn: failAt}
				return ff
			}})
		if err != nil {
			t.Fatalf("seed %d: open: %v", seed, err)
		}

		fuzzOps(t, db, oracle, rng, 120+rng.Intn(120), 25)
		// If the fault fired, "recover the disk" so later ops can succeed too.
		atomic.StoreInt32(&ff.failOn, 0)
		fuzzOps(t, db, oracle, rng, 50, 25)

		if err := db.Close(); err != nil {
			t.Fatalf("seed %d: close: %v", seed, err)
		}
		db2, err := Open(path)
		if err != nil {
			t.Fatalf("seed %d: reopen: %v", seed, err)
		}
		assertMatchesOracle(t, fmt.Sprintf("fault seed %d", seed), db2, oracle)
		db2.Close()
	}
}

// FUZZ 3 — CONCURRENT writers on DISJOINT key ranges, so group-commit batches
// actually form (batch size > 1) and the multi-write commit/apply path is
// exercised, while each key still has a well-defined last writer (its owning
// goroutine's serial ops) → a deterministic oracle. Reopen must match.
func TestFuzzConcurrentDisjointRecovers(t *testing.T) {
	for seed := int64(1); seed <= 20; seed++ {
		rng := rand.New(rand.NewSource(seed))
		path := filepath.Join(t.TempDir(), "app.blue")
		db, err := Open(path, Options{
			Sync:            seed%4 == 0,
			CheckpointEvery: []int{0, 11, 97}[rng.Intn(3)],
		})
		if err != nil {
			t.Fatalf("seed %d: open: %v", seed, err)
		}
		const G = 8
		var mu sync.Mutex
		oracle := map[string]string{}
		var wg sync.WaitGroup
		for g := 0; g < G; g++ {
			wg.Add(1)
			go func(g int, gseed int64) {
				defer wg.Done()
				r := rand.New(rand.NewSource(gseed))
				for i := 0; i < 150+r.Intn(150); i++ {
					key := fmt.Sprintf("g%d-k%d", g, r.Intn(20)) // disjoint per goroutine
					if r.Intn(8) == 0 {
						if db.Delete([]byte(key)) == nil {
							mu.Lock()
							delete(oracle, key)
							mu.Unlock()
						}
					} else {
						val := fmt.Sprintf("v%d-%d", i, r.Intn(1_000_000))
						if db.Put([]byte(key), []byte(val)) == nil {
							mu.Lock()
							oracle[key] = val
							mu.Unlock()
						}
					}
				}
			}(g, seed*1000+int64(g))
		}
		wg.Wait()
		batches, writes, _ := db.Stats()
		if err := db.Close(); err != nil {
			t.Fatalf("seed %d: close: %v", seed, err)
		}
		db2, err := Open(path)
		if err != nil {
			t.Fatalf("seed %d: reopen: %v", seed, err)
		}
		assertMatchesOracle(t, fmt.Sprintf("concurrent seed %d (batch~%.1f)", seed,
			float64(writes)/float64(max64(batches, 1))), db2, oracle)
		db2.Close()
	}
}

func max64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func TestForEachSnapshotAndEarlyStop(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "app.blue"), Options{Sync: false})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, k := range []string{"a", "b", "c"} {
		_ = db.Put([]byte(k), []byte("v"+k))
	}
	seen := map[string]string{}
	db.ForEach(func(k, v []byte) bool { seen[string(k)] = string(v); return true })
	if len(seen) != 3 || seen["a"] != "va" || seen["c"] != "vc" {
		t.Fatalf("ForEach saw %v", seen)
	}
	// Early stop after 2.
	n := 0
	db.ForEach(func(k, v []byte) bool { n++; return n < 2 })
	if n != 2 {
		t.Fatalf("early stop visited %d, want 2", n)
	}
}

// ForEach must not block or race a concurrent writer (the snapshot-under-short-
// lock fix). Run under -race.
func TestForEachConcurrentWrites(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "app.blue"), Options{Sync: false})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 2000; i++ {
			_ = db.Put([]byte(fmt.Sprintf("k%d", i%100)), []byte("v"))
		}
		close(done)
	}()
	for {
		select {
		case <-done:
			return
		default:
			db.ForEach(func(k, v []byte) bool { _ = len(v); return true })
		}
	}
}

func TestMaxValueBytesGuard(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "app.blue"), Options{Sync: true, MaxValueBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Put([]byte("k"), make([]byte, 100)); err != ErrTooLarge {
		t.Fatalf("oversized Put: err=%v want ErrTooLarge", err)
	}
	// key length contributes to the bound: a 60-byte key + 10-byte value = 70 > 64.
	if err := db.Put(make([]byte, 60), make([]byte, 10)); err != ErrTooLarge {
		t.Fatalf("oversized key+value: err=%v want ErrTooLarge", err)
	}
	// boundary equality (== MaxValueBytes) is allowed.
	if err := db.Put([]byte("k"), make([]byte, 63)); err != nil {
		t.Fatalf("boundary Put (1+63=64): %v", err)
	}
	if _, ok := db.Get([]byte("k")); !ok {
		t.Fatal("in-bounds value not stored")
	}
	// Delete is exempt from the value-size guard.
	if err := db.Delete([]byte("k")); err != nil {
		t.Fatalf("Delete must be exempt: %v", err)
	}
	// Checkpoint is exempt.
	if err := db.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint must be exempt: %v", err)
	}
}

func TestMaxKeysGuard(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "app.blue"), Options{Sync: false, MaxKeys: 3})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, k := range []string{"a", "b", "c"} {
		if err := db.Put([]byte(k), []byte("v")); err != nil {
			t.Fatalf("Put %q: %v", k, err)
		}
	}
	// 4th distinct key → ErrFull.
	if err := db.Put([]byte("d"), []byte("v")); err != ErrFull {
		t.Fatalf("over-capacity new key: err=%v want ErrFull", err)
	}
	// Overwrite of an existing key is always allowed.
	if err := db.Put([]byte("a"), []byte("v2")); err != nil {
		t.Fatalf("overwrite at capacity: %v", err)
	}
	// After a delete, a new key fits again.
	_ = db.Delete([]byte("a"))
	if err := db.Put([]byte("d"), []byte("v")); err != nil {
		t.Fatalf("new key after delete: %v", err)
	}
}
