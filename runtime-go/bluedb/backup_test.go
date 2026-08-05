package bluedb

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestBackupRoundTrips: Put N keys, Backup(dest), Open(dest) → all N keys present
// with the correct values.
func TestBackupRoundTrips(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir)
	defer db.Close()

	const n = 200
	for i := 0; i < n; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%03d", i)), []byte(fmt.Sprintf("v%d", i))); err != nil {
			t.Fatal(err)
		}
	}

	dest := filepath.Join(dir, "backup.blue")
	if err := db.Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	bak, err := Open(dest)
	if err != nil {
		t.Fatalf("Open(dest): %v", err)
	}
	defer bak.Close()

	if got := bak.Len(); got != n {
		t.Fatalf("backup has %d keys, want %d", got, n)
	}
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("v%d", i)
		if got := mustGet(t, bak, fmt.Sprintf("k%03d", i)); got != want {
			t.Fatalf("backup key k%03d = %q, want %q", i, got, want)
		}
	}
}

// TestBackupVerifiesClean: Verify(dest) on the backup reports OK with a clean WAL
// and a clean snapshot.
func TestBackupVerifiesClean(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir)
	defer db.Close()

	for i := 0; i < 50; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}

	dest := filepath.Join(dir, "backup.blue")
	if err := db.Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	rep, err := Verify(dest)
	if err != nil {
		t.Fatalf("Verify(dest): %v", err)
	}
	if !rep.OK {
		t.Fatalf("backup Verify not OK: %+v", rep)
	}
	if rep.WalStatus != VerifyClean {
		t.Fatalf("backup WAL status = %v, want clean", rep.WalStatus)
	}
	if !rep.SnapExists || rep.SnapStatus != SnapClean {
		t.Fatalf("backup snapshot status = %v (exists=%v), want clean", rep.SnapStatus, rep.SnapExists)
	}
}

// TestBackupLeavesLiveWALUntouched: a backup writes ONLY to dest — the live
// store's WAL file size is unchanged, the checkpoints count is unchanged, and the
// live store still Gets every key.
func TestBackupLeavesLiveWALUntouched(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "app.blue")
	db := open(t, dir)
	defer db.Close()

	const n = 100
	for i := 0; i < n; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%03d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}

	walBefore := fileSizeT(t, walPath)
	_, _, ckptsBefore := db.Stats()

	dest := filepath.Join(dir, "backup.blue")
	if err := db.Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	if walAfter := fileSizeT(t, walPath); walAfter != walBefore {
		t.Fatalf("live WAL size changed by backup: before %d, after %d", walBefore, walAfter)
	}
	if _, _, ckptsAfter := db.Stats(); ckptsAfter != ckptsBefore {
		t.Fatalf("backup bumped checkpoint count: before %d, after %d", ckptsBefore, ckptsAfter)
	}
	// The live store still holds everything.
	if got := db.Len(); got != n {
		t.Fatalf("live store has %d keys after backup, want %d", got, n)
	}
	for i := 0; i < n; i++ {
		if got := mustGet(t, db, fmt.Sprintf("k%03d", i)); got != "v" {
			t.Fatalf("live key k%03d = %q after backup, want v", i, got)
		}
	}
	// And it can still accept writes (committer not wedged).
	if err := db.Put([]byte("after"), []byte("ok")); err != nil {
		t.Fatalf("Put after backup: %v", err)
	}
}

// TestBackupPointInTimeConsistency: Put A,B; Backup(dest); Put C on the live
// store. The backup has A,B but NOT C; the live store has A,B,C.
func TestBackupPointInTimeConsistency(t *testing.T) {
	dir := t.TempDir()
	db := open(t, dir)
	defer db.Close()

	if err := db.Put([]byte("A"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("B"), []byte("2")); err != nil {
		t.Fatal(err)
	}

	dest := filepath.Join(dir, "backup.blue")
	if err := db.Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	// Written AFTER the backup — must not appear in the backup.
	if err := db.Put([]byte("C"), []byte("3")); err != nil {
		t.Fatal(err)
	}

	bak, err := Open(dest)
	if err != nil {
		t.Fatalf("Open(dest): %v", err)
	}
	defer bak.Close()

	if got := mustGet(t, bak, "A"); got != "1" {
		t.Fatalf("backup A = %q, want 1", got)
	}
	if got := mustGet(t, bak, "B"); got != "2" {
		t.Fatalf("backup B = %q, want 2", got)
	}
	if _, ok := bak.Get([]byte("C")); ok {
		t.Fatal("backup contains C — must be a point-in-time snapshot BEFORE C was written")
	}
	if bak.Len() != 2 {
		t.Fatalf("backup has %d keys, want exactly 2 (A,B)", bak.Len())
	}

	// The live store has all three.
	if _, ok := db.Get([]byte("C")); !ok {
		t.Fatal("live store missing C")
	}
	if db.Len() != 3 {
		t.Fatalf("live store has %d keys, want 3 (A,B,C)", db.Len())
	}
}

// TestBackupSelfClobberRejected: a dest that would clobber the live WAL or the
// live snapshot is rejected, and the live store is fully intact afterward.
func TestBackupSelfClobberRejected(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "app.blue")
	db := open(t, dir)
	defer db.Close()

	for i := 0; i < 20; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	// Force a snapshot to exist on disk so a clobber would be observable.
	if err := db.Checkpoint(); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	walBefore := fileSizeT(t, walPath)
	snapBefore := fileSizeT(t, walPath+".snap")

	for _, bad := range []string{walPath, walPath + ".snap"} {
		if err := db.Backup(bad); err == nil {
			t.Fatalf("Backup(%q) should be rejected (would clobber the live store)", bad)
		}
	}

	// Empty dest is rejected too.
	if err := db.Backup(""); err == nil {
		t.Fatal(`Backup("") should be rejected`)
	}

	// Live store untouched: files unchanged, data intact, still writable.
	if got := fileSizeT(t, walPath); got != walBefore {
		t.Fatalf("live WAL changed by a rejected backup: before %d, after %d", walBefore, got)
	}
	if got := fileSizeT(t, walPath+".snap"); got != snapBefore {
		t.Fatalf("live snapshot changed by a rejected backup: before %d, after %d", snapBefore, got)
	}
	if db.Len() != 20 {
		t.Fatalf("live store has %d keys, want 20", db.Len())
	}
	if err := db.Put([]byte("still"), []byte("writable")); err != nil {
		t.Fatalf("Put after rejected backup: %v", err)
	}
}

func fileSizeT(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatalf("stat %q: %v", p, err)
	}
	return fi.Size()
}
