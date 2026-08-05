package bluedb

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// statBeforeAfter runs fn and asserts the file at path is byte-for-byte and
// mtime unchanged across it — the read-only contract Verify must never break.
func assertUnchanged(t *testing.T, path string, fn func()) {
	t.Helper()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}
	origBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}
	fn()
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if before.Size() != after.Size() {
		t.Fatalf("file size changed across Verify: %d -> %d (Verify must be read-only)", before.Size(), after.Size())
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("file mtime changed across Verify: %v -> %v (Verify must be read-only)", before.ModTime(), after.ModTime())
	}
	nowBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(origBytes) != string(nowBytes) {
		t.Fatal("file contents changed across Verify (Verify must be read-only)")
	}
}

func TestVerifyCleanDbIsReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const N = 25
	for i := 0; i < N; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%03d", i)), []byte(fmt.Sprintf("v-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	var rep VerifyReport
	assertUnchanged(t, path, func() {
		r, e := Verify(path)
		if e != nil {
			t.Fatalf("Verify: %v", e)
		}
		rep = r
	})

	if rep.WalStatus != VerifyClean {
		t.Fatalf("WalStatus = %q, want %q", rep.WalStatus, VerifyClean)
	}
	if !rep.OK {
		t.Fatal("OK = false, want true for a clean DB")
	}
	if rep.WalRecords != N {
		t.Fatalf("WalRecords = %d, want %d", rep.WalRecords, N)
	}
	if rep.FirstBadOffset != -1 {
		t.Fatalf("FirstBadOffset = %d, want -1", rep.FirstBadOffset)
	}
	if rep.WalVersion != int(walVersion) {
		t.Fatalf("WalVersion = %d, want %d", rep.WalVersion, walVersion)
	}
	if !rep.WalExists {
		t.Fatal("WalExists = false, want true")
	}
	if rep.SnapStatus != SnapAbsent {
		t.Fatalf("SnapStatus = %q, want %q (no checkpoint taken)", rep.SnapStatus, SnapAbsent)
	}
}

func TestVerifyMidFileScribbleIsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const N = 60
	for i := 0; i < N; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%04d", i)), []byte(fmt.Sprintf("val-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	orig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the middle half → valid records both before AND after.
	off := walHeaderLen + len(orig)/2
	corrupt := append([]byte(nil), orig...)
	corrupt[off] ^= 0xFF
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}

	var rep VerifyReport
	assertUnchanged(t, path, func() {
		r, e := Verify(path)
		if e != nil {
			t.Fatalf("Verify: %v", e)
		}
		rep = r
	})

	if rep.WalStatus != VerifyCorruption {
		t.Fatalf("WalStatus = %q, want %q", rep.WalStatus, VerifyCorruption)
	}
	if rep.OK {
		t.Fatal("OK = true, want false for mid-file corruption")
	}
	if rep.FirstBadOffset < 0 {
		t.Fatalf("FirstBadOffset = %d, want the offset of the record containing the scribble", rep.FirstBadOffset)
	}
	// The bad record starts at or before the scribbled byte (its start is the
	// stop-point) and after the header.
	if rep.FirstBadOffset < int64(walHeaderLen) || rep.FirstBadOffset > int64(off) {
		t.Fatalf("FirstBadOffset = %d, want in [%d, %d] (record containing byte %d)", rep.FirstBadOffset, walHeaderLen, off, off)
	}
	if rep.Detail == "" {
		t.Fatal("Detail empty, want a human note for corruption")
	}
}

func TestVerifyTornTailIsOK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const N = 20
	for i := 0; i < N; i++ {
		if err := db.Put([]byte(fmt.Sprintf("key%02d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Chop 3 bytes off the end → the final record is now a partial write.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b[:len(b)-3], 0o600); err != nil {
		t.Fatal(err)
	}

	var rep VerifyReport
	assertUnchanged(t, path, func() {
		r, e := Verify(path)
		if e != nil {
			t.Fatalf("Verify: %v", e)
		}
		rep = r
	})

	if rep.WalStatus != VerifyTornTail {
		t.Fatalf("WalStatus = %q, want %q", rep.WalStatus, VerifyTornTail)
	}
	if !rep.OK {
		t.Fatal("OK = false, want true — a torn tail is recoverable (Open truncates + recovers)")
	}
	if rep.FirstBadOffset < 0 {
		t.Fatalf("FirstBadOffset = %d, want the offset of the torn final record", rep.FirstBadOffset)
	}
	// N-1 valid records precede the torn tail.
	if rep.WalRecords != N-1 {
		t.Fatalf("WalRecords = %d, want %d (only the final record is torn)", rep.WalRecords, N-1)
	}
}

func TestVerifyNewerVersionUnsupported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	var buf []byte
	buf = append(buf, walMagic...)
	buf = append(buf, byte(99)) // newer than walVersion
	buf = append(buf, encodeRecord(entry{seq: 1, op: opPut, key: []byte("k"), value: []byte("v")})...)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}

	var rep VerifyReport
	assertUnchanged(t, path, func() {
		r, e := Verify(path)
		if e != nil {
			t.Fatalf("Verify: %v", e)
		}
		rep = r
	})

	if rep.WalStatus != VerifyVersionUnsupported {
		t.Fatalf("WalStatus = %q, want %q", rep.WalStatus, VerifyVersionUnsupported)
	}
	if rep.WalVersion != 99 {
		t.Fatalf("WalVersion = %d, want 99", rep.WalVersion)
	}
	if rep.OK {
		t.Fatal("OK = true, want false for an unsupported version")
	}
}

func TestVerifyLegacyHeaderlessScans(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	recs := []entry{
		{seq: 1, op: opPut, key: []byte("a"), value: []byte("1")},
		{seq: 2, op: opPut, key: []byte("b"), value: []byte("2")},
		{seq: 3, op: opDelete, key: []byte("a")},
		{seq: 4, op: opPut, key: []byte("c"), value: []byte("3")},
	}
	var buf []byte
	for _, e := range recs {
		buf = append(buf, encodeRecord(e)...)
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	if string(buf[0:4]) == walMagic {
		t.Fatal("fixture unexpectedly starts with the WAL magic")
	}

	var rep VerifyReport
	assertUnchanged(t, path, func() {
		r, e := Verify(path)
		if e != nil {
			t.Fatalf("Verify: %v", e)
		}
		rep = r
	})

	if rep.WalVersion != 0 {
		t.Fatalf("WalVersion = %d, want 0 (legacy headerless)", rep.WalVersion)
	}
	if rep.WalStatus != VerifyClean {
		t.Fatalf("WalStatus = %q, want %q", rep.WalStatus, VerifyClean)
	}
	if rep.WalRecords != len(recs) {
		t.Fatalf("WalRecords = %d, want %d", rep.WalRecords, len(recs))
	}
	if !rep.OK {
		t.Fatal("OK = false, want true for a clean legacy WAL")
	}
}

func TestVerifyMissingWalIsFresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.blue")
	rep, err := Verify(path)
	if err != nil {
		t.Fatalf("Verify on missing file should not error: %v", err)
	}
	if rep.WalExists {
		t.Fatal("WalExists = true, want false")
	}
	if rep.WalStatus != VerifyClean {
		t.Fatalf("WalStatus = %q, want %q for a fresh/absent store", rep.WalStatus, VerifyClean)
	}
	if !rep.OK {
		t.Fatal("OK = false, want true — Open on a fresh store succeeds")
	}
}

func TestVerifySnapshotClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	rep, err := Verify(path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.SnapStatus != SnapClean {
		t.Fatalf("SnapStatus = %q, want %q", rep.SnapStatus, SnapClean)
	}
	if !rep.SnapExists {
		t.Fatal("SnapExists = false, want true after checkpoint")
	}
	if rep.SnapCoveredSeq == 0 {
		t.Fatal("SnapCoveredSeq = 0, want > 0 after a checkpoint over 10 writes")
	}
	if !rep.OK {
		t.Fatal("OK = false, want true (clean WAL + clean snapshot)")
	}
}

func TestVerifyCorruptSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Checkpoint(); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	snapPath := path + ".snap"
	sb, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the snapshot body → CRC mismatch (magic+version intact).
	sb[len(sb)/2] ^= 0xFF
	if err := os.WriteFile(snapPath, sb, 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := Verify(path)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.SnapStatus != SnapCorrupt {
		t.Fatalf("SnapStatus = %q, want %q", rep.SnapStatus, SnapCorrupt)
	}
	if rep.OK {
		t.Fatal("OK = true, want false — a corrupt snapshot blocks Open")
	}
}
