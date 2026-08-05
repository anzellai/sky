package bluedb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// G1 — refuse to open a WAL whose version header is NEWER than this binary
// understands, and DO NOT truncate it (that would destroy data a newer binary
// wrote). The exact failure the version header exists to prevent.
func TestG1RefuseNewerWalVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.blue")

	// Hand-build a WAL: magic + a version we don't understand (99) + one valid record.
	var buf []byte
	buf = append(buf, walMagic...)
	buf = append(buf, byte(99)) // newer than walVersion
	buf = append(buf, encodeRecord(entry{seq: 1, op: opPut, key: []byte("k"), value: []byte("v")})...)
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = Open(path)
	if err == nil {
		t.Fatal("Open succeeded on a newer-version WAL; want a refuse-to-open error")
	}
	if !strings.Contains(err.Error(), "newer binary") {
		t.Fatalf("error = %q; want it to mention a newer binary", err.Error())
	}

	// Critically: the file must be UNTOUCHED (not truncated).
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Size() != before.Size() {
		t.Fatalf("newer-version WAL was modified on refused Open: %d -> %d bytes (must be untouched)",
			before.Size(), after.Size())
	}
}

// G1 migration — a LEGACY headerless WAL (records from offset 0, written before
// the version header existed) must replay identically to the pre-change engine.
// This is the backward-compat guarantee: existing on-disk data keeps working.
func TestG1LegacyHeaderlessReplays(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.blue")

	// A legacy WAL: raw records at offset 0, NO magic/version header.
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
	// Sanity: this legacy file does NOT start with the header.
	if string(buf[0:4]) == walMagic {
		t.Fatal("test fixture unexpectedly starts with the WAL magic")
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open on legacy headerless WAL: %v", err)
	}
	defer db.Close()

	// a deleted; b=2; c=3.
	if db.Len() != 2 {
		t.Fatalf("Len = %d, want 2 (legacy replay)", db.Len())
	}
	if v, ok := db.Get([]byte("b")); !ok || string(v) != "2" {
		t.Fatalf("b = %q,%v; want 2,true", v, ok)
	}
	if v, ok := db.Get([]byte("c")); !ok || string(v) != "3" {
		t.Fatalf("c = %q,%v; want 3,true", v, ok)
	}
	if _, ok := db.Get([]byte("a")); ok {
		t.Fatal("a should have been deleted by the legacy WAL")
	}

	// A legacy WAL with only valid records is not truncated: it replays IDENTICALLY.
	fi, _ := os.Stat(path)
	if fi.Size() != int64(len(buf)) {
		t.Fatalf("legacy WAL size changed on Open: %d -> %d (must be identical)", len(buf), fi.Size())
	}
}

// G1 roundtrip — a fresh DB writes the version header, and reopening replays the
// records that follow it.
func TestG1FreshWalHasHeaderAndRoundtrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.blue")

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("x"), []byte("1")); err != nil {
		t.Fatal(err)
	}
	if err := db.Put([]byte("y"), []byte("2")); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < walHeaderLen || string(b[0:4]) != walMagic || b[4] != walVersion {
		show := b
		if len(show) > 8 {
			show = show[:8]
		}
		t.Fatalf("fresh WAL does not begin with magic+version; got % x", show)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	if v, ok := db2.Get([]byte("x")); !ok || string(v) != "1" {
		t.Fatalf("x = %q,%v; want 1,true", v, ok)
	}
	if v, ok := db2.Get([]byte("y")); !ok || string(v) != "2" {
		t.Fatalf("y = %q,%v; want 2,true", v, ok)
	}
}

// G2 fuzz — flip a byte in a MIDDLE record. A valid record follows the corrupted
// one, so this is mid-file corruption (not a torn tail): Open must REFUSE and
// must NOT truncate the file — the valid tail is preserved for recovery/backup.
func TestG2MidFileScribbleRefusesAndPreservesTail(t *testing.T) {
	for seed := int64(1); seed <= 60; seed++ {
		rng := rand.New(rand.NewSource(seed))
		dir := t.TempDir()
		path := filepath.Join(dir, "app.blue")

		db, err := Open(path)
		if err != nil {
			t.Fatalf("seed %d: open: %v", seed, err)
		}
		const N = 60
		for i := 0; i < N; i++ {
			k := fmt.Sprintf("k%04d", i)
			v := fmt.Sprintf("val-%d-%d", i, rng.Intn(1_000_000))
			if err := db.Put([]byte(k), []byte(v)); err != nil {
				t.Fatalf("seed %d: put: %v", seed, err)
			}
		}
		if err := db.Close(); err != nil {
			t.Fatalf("seed %d: close: %v", seed, err)
		}

		orig, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// Flip a byte in the middle half — guaranteed to leave valid records both
		// before AND after the corruption.
		lo := walHeaderLen + len(orig)/4
		hi := len(orig) - len(orig)/4
		off := lo + rng.Intn(hi-lo)
		corrupt := append([]byte(nil), orig...)
		corrupt[off] ^= 0xFF
		if err := os.WriteFile(path, corrupt, 0o600); err != nil {
			t.Fatal(err)
		}

		before, _ := os.Stat(path)
		_, err = Open(path)
		if err == nil {
			t.Fatalf("seed %d: Open succeeded on mid-file corruption (byte %d); want a refuse-to-open error",
				seed, off)
		}
		if !strings.Contains(err.Error(), "corruption") {
			t.Fatalf("seed %d: error = %q; want a corruption refusal", seed, err.Error())
		}
		after, _ := os.Stat(path)
		if before.Size() != after.Size() {
			t.Fatalf("seed %d: file truncated on refused Open: %d -> %d (must be untouched — the later valid records must NOT be lost)",
				seed, before.Size(), after.Size())
		}
	}
}

// G2 counterpart — a genuine torn TAIL (an interrupted final write, nothing valid
// after it) must STILL recover: truncate only the partial tail, keep the prefix,
// and stay writable.
func TestG2TornTailStillRecovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.blue")

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

	// Chop a few bytes off the end → the final record is now a partial write.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b[:len(b)-3], 0o600); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("torn tail must recover, got error: %v", err)
	}
	defer db2.Close()
	if db2.Len() != N-1 {
		t.Fatalf("Len = %d, want %d (only the partial final record dropped)", db2.Len(), N-1)
	}
	for i := 0; i < N-1; i++ {
		if _, ok := db2.Get([]byte(fmt.Sprintf("key%02d", i))); !ok {
			t.Fatalf("key%02d lost after torn-tail recovery", i)
		}
	}
	// The truncated tail must leave a clean append boundary.
	if err := db2.Put([]byte("fresh"), []byte("v")); err != nil {
		t.Fatalf("append after torn recovery: %v", err)
	}
}

// RED→GREEN — the discovery artifact for the WAL v2 commit-record fix. The SAME
// physical shape — an acked prefix, then an in-flight group with an interior HOLE
// (a torn record followed by a later valid record) — is UNRECOVERABLE under v1 (no
// durable commit boundary → recovery sees valid-after-torn → classifies mid-file
// corruption → REFUSES → strands the acked prefix) but RECOVERABLE under v2 (the
// per-group commit record frames the acked prefix, so recovery truncates the
// un-acked in-flight tail and opens clean). This is the exact bug WAL v2 fixes.
func TestV1StrandsWhatV2Recovers(t *testing.T) {
	// A torn record: well-framed (header length is honest) but its payload CRC
	// won't match — the reader reads the whole payload, then rejects it. This is
	// the "interior page hole" a power-loss-during-fsync leaves.
	tornPut := func(seq uint64, key, val string) []byte {
		rec := encodeRecord(entry{seq: seq, op: opPut, key: []byte(key), value: []byte(val)})
		rec[len(rec)-1] ^= 0xFF // corrupt the last payload byte → CRC mismatch
		return rec
	}

	// --- v1 (the bug): [header v1][valid a][torn][valid b], NO commit records. ---
	dir := t.TempDir()
	v1path := filepath.Join(dir, "v1.blue")
	var v1 []byte
	v1 = append(v1, walMagic...)
	v1 = append(v1, byte(1)) // legacy version-1 header
	v1 = append(v1, encodeRecord(entry{seq: 1, op: opPut, key: []byte("a"), value: []byte("1")})...)
	v1 = append(v1, tornPut(2, "mid", "xxxx")...)
	v1 = append(v1, encodeRecord(entry{seq: 3, op: opPut, key: []byte("b"), value: []byte("2")})...)
	if err := os.WriteFile(v1path, v1, 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(v1path)
	if _, err := Open(v1path); err == nil {
		t.Fatal("v1: Open succeeded, but v1 has no commit boundary — it MUST refuse (the bug: it strands 'a')")
	} else if !strings.Contains(err.Error(), "corruption") {
		t.Fatalf("v1: error = %q; want a corruption refusal (documents the stranding bug)", err.Error())
	}
	after, _ := os.Stat(v1path)
	if before.Size() != after.Size() {
		t.Fatal("v1: refused Open must not truncate (the acked 'a' is stranded, not lost)")
	}

	// --- v2 (the fix): [header v2][a][commit a][torn][b], b un-committed. ---
	v2path := filepath.Join(dir, "v2.blue")
	var v2 []byte
	v2 = append(v2, walHeaderBytes()...) // version-2 header
	v2 = append(v2, encodeRecord(entry{seq: 1, op: opPut, key: []byte("a"), value: []byte("1")})...)
	v2 = append(v2, encodeRecord(commitEntry(1, 1))...) // 'a' is the acked prefix
	v2 = append(v2, tornPut(2, "mid", "xxxx")...)        // in-flight torn record …
	v2 = append(v2, encodeRecord(entry{seq: 3, op: opPut, key: []byte("b"), value: []byte("2")})...)
	// … followed by a later VALID data record but NO commit → an un-acked in-flight
	// tail. (This is what would REFUSE under v1's valid-after-torn rule.)
	if err := os.WriteFile(v2path, v2, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := Open(v2path)
	if err != nil {
		t.Fatalf("v2: Open REFUSED — the commit-record fix must recover the acked prefix, not strand it: %v", err)
	}
	defer db.Close()
	if v, ok := db.Get([]byte("a")); !ok || string(v) != "1" {
		t.Fatalf("v2: acked 'a' = %q,%v; want 1,true (the fix recovers what v1 stranded)", v, ok)
	}
	if _, ok := db.Get([]byte("mid")); ok {
		t.Fatal("v2: torn in-flight record surfaced")
	}
	if _, ok := db.Get([]byte("b")); ok {
		t.Fatal("v2: un-committed in-flight record 'b' surfaced — it was never acked")
	}
	if db.Len() != 1 {
		t.Fatalf("v2: Len = %d, want 1 (only the committed prefix)", db.Len())
	}
}

// G2 residual — corrupting the FINAL commit record TRUNCATES the last group and
// recovers the prefix (it does NOT refuse). This is the documented, irreducible
// residual of WAL v2: a rotted last commit is byte-indistinguishable from an
// in-flight last group whose commit never landed, so recovery fails toward
// availability (truncate + recover) rather than refusing. Contrast with a mid-file
// scribble (TestG2MidFileScribble…) which leaves a WHOLE valid committed group
// behind the corruption → refuse (G2 preserved).
func TestG2FinalCommitCorruptTruncatesLastGroup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.blue")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const N = 12
	for i := 0; i < N; i++ {
		if err := db.Put([]byte(fmt.Sprintf("key%02d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// The WAL ends with the final group's commit record (frame = 8 header + 13
	// payload = 21 bytes). Flip a byte in its payload → torn commit, everything
	// before it intact.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)-1] ^= 0xFF
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("a rotted FINAL commit must truncate + recover (not refuse — it is indistinguishable from an in-flight last group): %v", err)
	}
	defer db2.Close()
	if db2.Len() != N-1 {
		t.Fatalf("Len = %d, want %d (only the last group dropped)", db2.Len(), N-1)
	}
	for i := 0; i < N-1; i++ {
		if _, ok := db2.Get([]byte(fmt.Sprintf("key%02d", i))); !ok {
			t.Fatalf("key%02d lost after final-commit-corrupt recovery", i)
		}
	}
	if _, ok := db2.Get([]byte(fmt.Sprintf("key%02d", N-1))); ok {
		t.Fatalf("key%02d survived — its group's commit was corrupt, so the group is un-acked", N-1)
	}
	if err := db2.Put([]byte("fresh"), []byte("v")); err != nil {
		t.Fatalf("append after recovery: %v", err)
	}
}

// G3 — a torn-tail recovery emits a structured log line AND bumps the recovery
// metric (it used to be silent).
func TestG3RecoveryTruncationLogAndMetric(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.blue")

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Append a torn tail: a header claiming a big payload, then too few bytes.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	var hdr [8]byte
	binary.LittleEndian.PutUint32(hdr[0:4], 0xDEADBEEF)
	binary.LittleEndian.PutUint32(hdr[4:8], 65536)
	_, _ = f.Write(hdr[:])
	_, _ = f.Write([]byte{1, 2, 3})
	_ = f.Close()

	// Capture the log output + the recovery-metric delta across this Open.
	var logbuf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logbuf)
	defer log.SetOutput(prev)

	t0, b0 := RecoveryStats()
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("torn-tail recovery: %v", err)
	}
	defer db2.Close()
	t1, b1 := RecoveryStats()

	if t1 != t0+1 {
		t.Fatalf("recoveryTruncations = %d, want %d (one truncation)", t1, t0+1)
	}
	if b1 <= b0 {
		t.Fatalf("recoveryBytesDiscarded did not increase (%d -> %d)", b0, b1)
	}
	if !strings.Contains(logbuf.String(), "truncated torn WAL tail") {
		t.Fatalf("expected a recovery log line, got: %q", logbuf.String())
	}
}
