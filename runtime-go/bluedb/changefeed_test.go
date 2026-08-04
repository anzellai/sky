package bluedb

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func openTestDBCF(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(dir + "/cf.blue")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func drainOne(t *testing.T, sub *ChangeSub) []ChangeEvent {
	t.Helper()
	select {
	case ev := <-sub.C:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for change event")
		return nil
	}
}

// A put, a delete, and a batch each surface on the feed with the right op/key/value.
func TestChangeFeedBasic(t *testing.T) {
	db := openTestDBCF(t)
	sub, cancel := db.Subscribe(64)
	defer cancel()

	if err := db.Put([]byte("k1"), []byte("v1")); err != nil {
		t.Fatal(err)
	}
	ev := drainOne(t, sub)
	if len(ev) != 1 || !ev[0].IsPut() || string(ev[0].Key) != "k1" || string(ev[0].Value) != "v1" {
		t.Fatalf("put event = %+v", ev)
	}

	if err := db.Delete([]byte("k1")); err != nil {
		t.Fatal(err)
	}
	ev = drainOne(t, sub)
	if len(ev) != 1 || !ev[0].IsDelete() || string(ev[0].Key) != "k1" {
		t.Fatalf("delete event = %+v", ev)
	}

	b := NewBatch()
	b.Put([]byte("a"), []byte("1"))
	b.Put([]byte("b"), []byte("2"))
	b.Delete([]byte("a"))
	if err := db.WriteBatch(b); err != nil {
		t.Fatal(err)
	}
	ev = drainOne(t, sub)
	if len(ev) != 3 {
		t.Fatalf("batch event count = %d want 3 (%+v)", len(ev), ev)
	}
	// commit order preserved; seqs strictly increasing across the whole feed
	var last uint64
	for _, e := range ev {
		if e.Seq <= last {
			t.Fatalf("seq not monotonic: %d after %d", e.Seq, last)
		}
		last = e.Seq
	}
}

// Cancelled subscribers stop receiving; a second cancel is a no-op.
func TestChangeFeedUnsubscribe(t *testing.T) {
	db := openTestDBCF(t)
	sub, cancel := db.Subscribe(8)
	if err := db.Put([]byte("x"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	drainOne(t, sub)
	cancel()
	cancel() // idempotent
	if err := db.Put([]byte("y"), []byte("2")); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-sub.C:
		t.Fatalf("received after cancel: %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
	if db.hasSubs() {
		t.Fatal("hasSubs true after cancel")
	}
}

// The keystone: a subscriber that NEVER drains must not stall the committer. Writes
// keep succeeding fast, and the subscriber's overflow flag latches.
func TestChangeFeedSlowConsumerNeverStalls(t *testing.T) {
	db := openTestDBCF(t)
	sub, cancel := db.Subscribe(4) // tiny buffer, never drained
	defer cancel()

	start := time.Now()
	for i := 0; i < 2000; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%04d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	elapsed := time.Since(start)
	if elapsed > 15*time.Second {
		t.Fatalf("2000 writes took %v — a full change-feed buffer stalled the committer", elapsed)
	}
	if !sub.Overflowed() {
		t.Fatal("expected overflow flag after flooding a 4-slot buffer with 2000 writes")
	}
	// Overflowed clears on read.
	if sub.Overflowed() {
		t.Fatal("overflow flag did not clear after read")
	}
	// The store is still fully functional after the flood.
	if err := db.Put([]byte("after"), []byte("ok")); err != nil {
		t.Fatal(err)
	}
	if v, ok := db.Get([]byte("after")); !ok || string(v) != "ok" {
		t.Fatalf("store broken after overflow: %q,%v", v, ok)
	}
}

// No subscribers → the emit path is skipped and writes are unaffected (smoke).
func TestChangeFeedNoSubscribers(t *testing.T) {
	db := openTestDBCF(t)
	if db.hasSubs() {
		t.Fatal("hasSubs true with no subscribers")
	}
	if err := db.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatal(err)
	}
}

// Concurrent subscribers all see every committed batch; -race clean.
func TestChangeFeedConcurrentSubs(t *testing.T) {
	db := openTestDBCF(t)
	const nSubs = 8
	const nWrites = 200
	var wg sync.WaitGroup
	counts := make([]int, nSubs)
	subs := make([]*ChangeSub, nSubs)
	cancels := make([]func(), nSubs)
	for i := 0; i < nSubs; i++ {
		subs[i], cancels[i] = db.Subscribe(nWrites + 16)
	}
	for i := 0; i < nSubs; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got := 0
			for got < nWrites {
				select {
				case ev := <-subs[i].C:
					got += len(ev)
				case <-time.After(3 * time.Second):
					return
				}
			}
			counts[i] = got
		}(i)
	}
	for i := 0; i < nWrites; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%03d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	for i, c := range counts {
		if c != nWrites {
			t.Fatalf("sub %d saw %d events want %d", i, c, nWrites)
		}
	}
	for _, c := range cancels {
		c()
	}
}
