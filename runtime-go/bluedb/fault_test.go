package bluedb

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

var errSimNoSpace = errors.New("bluedb-test: simulated ENOSPC")

// faultyFile wraps a walFile and fails the Nth Write (1-based; 0 = never),
// simulating a mid-batch disk error.
type faultyFile struct {
	inner  walFile
	count  int32
	failOn int32
}

func (f *faultyFile) Write(p []byte) (int, error) {
	n := atomic.AddInt32(&f.count, 1)
	if fo := atomic.LoadInt32(&f.failOn); fo != 0 && n == fo {
		return 0, errSimNoSpace
	}
	return f.inner.Write(p)
}
func (f *faultyFile) Sync() error            { return f.inner.Sync() }
func (f *faultyFile) Truncate(n int64) error { return f.inner.Truncate(n) }
func (f *faultyFile) Close() error           { return f.inner.Close() }

// F1: a write that fails must not persist (no resurrection of a failed write),
// and the WAL must stay clean so a subsequent write recovers correctly.
func TestWriteErrorRollsBackNoResurrect(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.blue")

	var ff *faultyFile
	db, err := Open(path, Options{Sync: true, walWrap: func(w walFile) walFile {
		ff = &faultyFile{inner: w, failOn: 1} // fail the first write
		return ff
	}})
	if err != nil {
		t.Fatal(err)
	}

	if err := db.Put([]byte("a"), []byte("1")); err == nil {
		t.Fatal("expected write error on injected fault")
	}
	// Disk "recovers": subsequent writes succeed.
	atomic.StoreInt32(&ff.failOn, 0)
	if err := db.Put([]byte("b"), []byte("2")); err != nil {
		t.Fatalf("write after fault: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	if _, ok := db2.Get([]byte("a")); ok {
		t.Fatal("a: failed write was resurrected")
	}
	if v, ok := db2.Get([]byte("b")); !ok || string(v) != "2" {
		t.Fatalf("b = %q,%v; want 2,true (post-fault write lost)", v, ok)
	}
	if db2.Len() != 1 {
		t.Fatalf("Len = %d, want 1", db2.Len())
	}
}

// F1 property: under concurrency, a mid-batch write fault must never cause an
// ACKED write to be lost on recovery — this is the invariant the grill showed
// was broken (a torn record behind good records dropping acked data). The fault
// fires once (mid-stream), so at least one batch rolls back; every Put that
// returned nil must still be present after reopen.
func TestConcurrentWriteFaultNoAckedLoss(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.blue")

	var ff *faultyFile
	db, err := Open(path, Options{Sync: true, walWrap: func(w walFile) walFile {
		ff = &faultyFile{inner: w, failOn: 137} // fail one write mid-stream
		return ff
	}})
	if err != nil {
		t.Fatal(err)
	}

	const G, N = 8, 400
	var acked sync.Map // key -> struct{} for every Put that returned nil
	var wg sync.WaitGroup
	wg.Add(G)
	for g := 0; g < G; g++ {
		go func(g int) {
			defer wg.Done()
			for i := 0; i < N; i++ {
				k := fmt.Sprintf("g%d-k%04d", g, i)
				if err := db.Put([]byte(k), []byte("v")); err == nil {
					acked.Store(k, struct{}{})
				}
			}
		}(g)
	}
	wg.Wait()

	if atomic.LoadInt32(&ff.count) < 137 {
		t.Fatalf("fault never fired (only %d writes)", ff.count)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()

	ackedCount := 0
	acked.Range(func(k, _ any) bool {
		ackedCount++
		if _, ok := db2.Get([]byte(k.(string))); !ok {
			t.Fatalf("ACKED write %q lost after fault+recovery", k.(string))
		}
		return true
	})
	if db2.Len() != ackedCount {
		t.Fatalf("Len = %d, want %d (acked); a failed write was persisted",
			db2.Len(), ackedCount)
	}
	t.Logf("acked %d/%d writes; fault dropped %d; all acked survived reopen",
		ackedCount, G*N, G*N-ackedCount)
}

// F1 seal: if rollback itself is impossible, the engine refuses further writes
// rather than risk a torn WAL. (We can't easily fail Truncate portably, so this
// asserts the ErrFailed path via a Truncate-failing wrapper.)
func TestSealOnUnrollbackableError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.blue")

	var ff *sealFile
	db, err := Open(path, Options{Sync: true, walWrap: func(w walFile) walFile {
		ff = &sealFile{inner: w, failWrite: 1, failTruncate: true}
		return ff
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("a"), []byte("1")); err == nil {
		t.Fatal("expected write error")
	}
	// Rollback failed → engine sealed → further writes rejected.
	if err := db.Put([]byte("b"), []byte("2")); err != ErrFailed {
		t.Fatalf("after unrollbackable error: err = %v, want ErrFailed", err)
	}
	_ = db.Close()
}

type sealFile struct {
	inner        walFile
	n            int32
	failWrite    int32
	failTruncate bool
}

func (f *sealFile) Write(p []byte) (int, error) {
	if atomic.AddInt32(&f.n, 1) == f.failWrite {
		return 0, errSimNoSpace
	}
	return f.inner.Write(p)
}
func (f *sealFile) Sync() error { return f.inner.Sync() }
func (f *sealFile) Truncate(n int64) error {
	if f.failTruncate {
		return errors.New("bluedb-test: truncate failed")
	}
	return f.inner.Truncate(n)
}
func (f *sealFile) Close() error { return f.inner.Close() }
