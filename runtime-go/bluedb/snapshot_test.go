package bluedb

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A checkpoint compacts the WAL (truncates it) and the data survives a reopen
// from the snapshot + WAL-tail.
func TestCheckpointCompactsAndRecovers(t *testing.T) {
	dir := t.TempDir()
	wal := filepath.Join(dir, "app.blue")

	db, err := Open(wal)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%03d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatal(err)
	}

	// Snapshot exists; the WAL was compacted to just its version header (G1: the
	// freshly recreated log re-writes the header so an old binary can't misparse a
	// newer format — see writeSnapshotAtomic / doCheckpoint).
	if _, err := os.Stat(wal + ".snap"); err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	if fi, _ := os.Stat(wal); fi.Size() != walHeaderLen {
		t.Fatalf("WAL not compacted to header after checkpoint: %d bytes (want %d)", fi.Size(), walHeaderLen)
	}

	// Writes after the checkpoint go to the fresh WAL.
	for i := 100; i < 150; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%03d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(wal)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if db2.Len() != 150 {
		t.Fatalf("after reopen Len = %d, want 150 (snapshot 100 + WAL 50)", db2.Len())
	}
	for i := 0; i < 150; i++ {
		if _, ok := db2.Get([]byte(fmt.Sprintf("k%03d", i))); !ok {
			t.Fatalf("k%03d lost across checkpoint+reopen", i)
		}
	}
}

// A crash (torn tail) AFTER a checkpoint: snapshot data + post-checkpoint WAL
// both survive, torn tail dropped.
func TestCheckpointThenCrashRecovers(t *testing.T) {
	dir := t.TempDir()
	wal := filepath.Join(dir, "app.blue")

	db, _ := Open(wal)
	for i := 0; i < 60; i++ {
		_ = db.Put([]byte(fmt.Sprintf("k%02d", i)), []byte("v"))
	}
	_ = db.Checkpoint()
	for i := 60; i < 90; i++ {
		_ = db.Put([]byte(fmt.Sprintf("k%02d", i)), []byte("v"))
	}
	_ = db.Close()

	// Simulate a crash mid-write of a 91st record: garbage tail on the WAL.
	f, _ := os.OpenFile(wal, os.O_WRONLY|os.O_APPEND, 0o644)
	_, _ = f.Write([]byte{0x11, 0x22, 0x33, 0x44, 0x50, 0x00, 0x00, 0x00, 0x01})
	f.Close()

	db2, err := Open(wal)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if db2.Len() != 90 {
		t.Fatalf("Len = %d, want 90 (60 snapshot + 30 WAL, torn dropped)", db2.Len())
	}
}

// The crash-window guard: a STALE pre-snapshot record left in the WAL (seq <=
// coveredSeq) must be skipped, never resurrecting a value the snapshot
// superseded. This is the correctness crux of in-place WAL truncation.
func TestStaleRecordSkippedBySeq(t *testing.T) {
	dir := t.TempDir()
	wal := filepath.Join(dir, "app.blue")

	db, _ := Open(wal)
	_ = db.Put([]byte("a"), []byte("1")) // seq 1
	_ = db.Put([]byte("b"), []byte("2")) // seq 2
	_ = db.Checkpoint()                  // coveredSeq = 2; snapshot has a=1,b=2
	_ = db.Delete([]byte("a"))           // seq 3, in WAL (a removed)
	_ = db.Close()

	// Simulate the crash window: a stale record with an OLD seq (<= coveredSeq)
	// left in the WAL. Recovery must SKIP it and not resurrect "a".
	stale := encodeRecord(entry{seq: 1, op: opPut, key: []byte("a"), value: []byte("STALE")})
	f, _ := os.OpenFile(wal, os.O_WRONLY|os.O_APPEND, 0o644)
	_, _ = f.Write(stale)
	f.Close()

	db2, err := Open(wal)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if _, ok := db2.Get([]byte("a")); ok {
		t.Fatal("stale pre-snapshot record resurrected a deleted key")
	}
	if v, ok := db2.Get([]byte("b")); !ok || string(v) != "2" {
		t.Fatalf("b = %q,%v; want 2,true", v, ok)
	}
}

func TestAutoCheckpointBoundsWAL(t *testing.T) {
	dir := t.TempDir()
	wal := filepath.Join(dir, "app.blue")

	db, err := Open(wal, Options{Sync: true, CheckpointEvery: 500})
	if err != nil {
		t.Fatal(err)
	}
	// Overwrite the same small keyspace many times: auto-checkpoints should keep
	// both the live set and the WAL small.
	for i := 0; i < 5000; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%d", i%50)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	_, _, checkpoints := db.Stats()
	if checkpoints == 0 {
		t.Fatal("auto-checkpoint never fired")
	}
	if db.Len() != 50 {
		t.Fatalf("Len = %d, want 50", db.Len())
	}
	_ = db.Close()

	db2, err := Open(wal)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	if db2.Len() != 50 {
		t.Fatalf("after reopen Len = %d, want 50", db2.Len())
	}
	t.Logf("auto-checkpoints: %d over 5000 writes", checkpoints)
}

func TestCorruptSnapshotIsAnError(t *testing.T) {
	dir := t.TempDir()
	wal := filepath.Join(dir, "app.blue")
	db, _ := Open(wal)
	_ = db.Put([]byte("a"), []byte("1"))
	_ = db.Checkpoint()
	_ = db.Close()

	// Flip a byte in the snapshot body → CRC must catch it on load.
	b, _ := os.ReadFile(wal + ".snap")
	b[20] ^= 0xFF
	_ = os.WriteFile(wal+".snap", b, 0o644)

	if _, err := Open(wal); err == nil {
		t.Fatal("corrupt snapshot loaded silently; want error")
	}
}
