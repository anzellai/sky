package bluedb

import (
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// crashsim_test.go — the CORRECTED crash-fuzz for the WAL v2 durability floor.
//
// It models a POWER LOSS during a group's fsync: the acked prefix is fsync'd
// (durable, never mangled) and ONE in-flight group beyond it — written but not yet
// fsync'd — is mangled at 4 KiB sector granularity. The invariant under test: Open
// NEVER refuses on a holed UN-ACKED tail (the pre-v2 stranding bug), every ACKED
// write survives, and an in-flight group is all-or-nothing (its records never
// half-apply).
//
// Fidelity fixes folded in (from the two grills):
//   - `durable` is SEEDED from the on-disk WAL at install (walWrap runs AFTER
//     recovery has already written the header via the raw fd), and only a Sync
//     PROMOTES pending → durable. So `durable` is exactly the fsync-acked bytes.
//   - Sync=false models a PROCESS crash: pending is PRESERVED (no mangle). A
//     power-loss mangle is applied ONLY under Sync=true.
//   - Mangle is at 4 KiB SECTOR granularity on the pending region only; `durable`
//     is never touched. There is NO reorder-as-swap (unphysical on a real disk).
//   - The in-flight group uses REAL framing (encodeRecord data×N + a real commit
//     record) with seqs continuing the actual post-Close db.seq — the exact shape
//     a real crash-during-fsync would leave, not an ad-hoc byte blob.

const sectorSize = 4096

// crashSimFile wraps the WAL file to track the durability boundary: bytes are
// `pending` (written, not yet fsync'd — lost or mangled on power loss) until a
// Sync PROMOTES them to `durable` (survive power loss). A checkpoint Truncate(0)
// legitimately discards durable (the WAL is recreated). It is the mechanism behind
// the fidelity of the fuzz: after a Sync=true run, `durable` holds exactly the
// acked prefix.
type crashSimFile struct {
	inner   walFile
	durable []byte // fsync-acked bytes — survive power loss, never mangled
	pending []byte // written-not-synced — lost/mangled on power loss
}

// crashSimWrap builds a walWrap that installs a crashSimFile, SEEDING durable from
// the on-disk WAL (the header recovery already wrote via the raw fd — walWrap runs
// after that), and stashes the instance in *out for the test to read post-Close.
func crashSimWrap(path string, out **crashSimFile) func(walFile) walFile {
	return func(w walFile) walFile {
		seed, _ := os.ReadFile(path)
		c := &crashSimFile{inner: w, durable: append([]byte(nil), seed...)}
		*out = c
		return c
	}
}

func (c *crashSimFile) Write(p []byte) (int, error) {
	c.pending = append(c.pending, p...)
	return c.inner.Write(p)
}
func (c *crashSimFile) Sync() error {
	e := c.inner.Sync()
	// Promote: an fsync makes every pending byte durable.
	c.durable = append(c.durable, c.pending...)
	c.pending = c.pending[:0]
	return e
}
func (c *crashSimFile) Truncate(n int64) error {
	// A checkpoint Truncate(0) recreates the WAL — durable legitimately shrinks.
	if n < int64(len(c.durable)) {
		c.durable = c.durable[:n]
	}
	c.pending = c.pending[:0]
	return c.inner.Truncate(n)
}
func (c *crashSimFile) Close() error { return c.inner.Close() }

// --- power-loss mangle modes (4 KiB sector granularity, pending region only) ---

func numSectors(buf []byte) int { return (len(buf) + sectorSize - 1) / sectorSize }

// mangleTail drops a suffix of unsynced pages at a sector boundary — the tail of
// the in-flight write never reached the platter. It always drops AT LEAST the last
// sector (the group's commit lives at the tail), so the in-flight group is never
// left fully intact: a power loss during the fsync means the group did NOT fully
// land, hence is never acked.
func mangleTail(buf []byte, rng *rand.Rand) []byte {
	s := numSectors(buf)
	if s <= 1 {
		return []byte(nil) // the whole (sub-sector) write is lost
	}
	cut := rng.Intn(s) * sectorSize // [0, (s-1)*sectorSize] → always < len(buf)
	return append([]byte(nil), buf[:cut]...)
}

// mangleHole zeroes ONE interior sector, leaving a LATER sector intact — the exact
// interior-page-hole shape (torn record then a later valid record) that caused the
// pre-v2 stranding bug.
func mangleHole(buf []byte, rng *rand.Rand) []byte {
	out := append([]byte(nil), buf...)
	s := numSectors(out)
	if s < 2 {
		zeroSector(out, 0)
		return out
	}
	hole := rng.Intn(s - 1) // any but the last sector → a later sector survives
	zeroSector(out, hole)
	return out
}

// mangleZeroSubset zeroes a random NON-EMPTY subset of sectors (at least one, so
// the in-flight group is always left incomplete — a page that didn't land).
func mangleZeroSubset(buf []byte, rng *rand.Rand) []byte {
	out := append([]byte(nil), buf...)
	s := numSectors(out)
	zeroed := false
	for i := 0; i < s; i++ {
		if rng.Intn(2) == 0 {
			zeroSector(out, i)
			zeroed = true
		}
	}
	if !zeroed {
		zeroSector(out, rng.Intn(s)) // force at least one lost page
	}
	return out
}

// mangleGarble fills a random sector with random bytes.
func mangleGarble(buf []byte, rng *rand.Rand) []byte {
	out := append([]byte(nil), buf...)
	s := numSectors(out)
	g := rng.Intn(s)
	lo := g * sectorSize
	hi := lo + sectorSize
	if hi > len(out) {
		hi = len(out)
	}
	for i := lo; i < hi; i++ {
		out[i] = byte(rng.Intn(256))
	}
	return out
}

func zeroSector(buf []byte, s int) {
	lo := s * sectorSize
	hi := lo + sectorSize
	if lo >= len(buf) {
		return
	}
	if hi > len(buf) {
		hi = len(buf)
	}
	for i := lo; i < hi; i++ {
		buf[i] = 0
	}
}

// --- synthesized in-flight groups (real framing) ---

// synthPutGroup builds an in-flight group of len(values) put records with
// contiguous seqs continuing from startSeq, closed by a real commit record.
func synthPutGroup(startSeq uint64, keys []string, values [][]byte) []byte {
	var buf []byte
	seq := startSeq
	for i := range values {
		seq++
		buf = append(buf, encodeRecord(entry{seq: seq, op: opPut, key: []byte(keys[i]), value: values[i]})...)
	}
	buf = append(buf, encodeRecord(commitEntry(seq, len(values)))...)
	return buf
}

// synthBatchGroup builds an in-flight group of ONE opBatch record (the committer
// writes a batch as a single record → the group has one DATA record) + a commit
// with count 1.
func synthBatchGroup(startSeq uint64, muts []mutation) []byte {
	seq := startSeq + 1
	var buf []byte
	buf = append(buf, encodeRecord(entry{seq: seq, op: opBatch, muts: muts})...)
	buf = append(buf, encodeRecord(commitEntry(seq, 1))...)
	return buf
}

func filled(n int, b byte) []byte {
	v := make([]byte, n)
	for i := range v {
		v[i] = b
	}
	return v
}

// TestFuzzPowerLossDropsUnsyncedPages — the headline invariant. Sync=true; an
// in-flight group beyond the acked prefix is mangled at sector granularity by
// every power-loss mode. Open must NEVER refuse on the holed un-acked tail, every
// acked write must survive, and db.Len() must equal the acked count (the in-flight
// group's records never leak in).
func TestFuzzPowerLossDropsUnsyncedPages(t *testing.T) {
	modes := []struct {
		name string
		fn   func([]byte, *rand.Rand) []byte
	}{
		{"TAIL", mangleTail},
		{"HOLE", mangleHole},
		{"ZERO_SUBSET", mangleZeroSubset},
		{"GARBLE", mangleGarble},
	}
	for _, mode := range modes {
		for seed := int64(1); seed <= 25; seed++ {
			rng := rand.New(rand.NewSource(seed))
			path := filepath.Join(t.TempDir(), "app.blue")

			var cs *crashSimFile
			db, err := Open(path, Options{Sync: true, walWrap: crashSimWrap(path, &cs)})
			if err != nil {
				t.Fatalf("%s seed %d: open: %v", mode.name, seed, err)
			}
			acked := map[string]string{}
			M := 10 + rng.Intn(30)
			for i := 0; i < M; i++ {
				k := fmt.Sprintf("k%04d", i)
				v := fmt.Sprintf("val-%d-%d", i, rng.Intn(1_000_000))
				if err := db.Put([]byte(k), []byte(v)); err != nil {
					t.Fatalf("%s seed %d: put: %v", mode.name, seed, err)
				}
				acked[k] = v
			}
			highSeq := db.seq // post-Close-stable committer seq (read after Close)
			if err := db.Close(); err != nil {
				t.Fatalf("%s seed %d: close: %v", mode.name, seed, err)
			}

			// durable = the fsync-acked prefix (crashSimFile promoted it on each
			// Put's Sync). An in-flight group of 3 records × ~one-sector values
			// spans ≥3 sectors so HOLE/GARBLE land inside it.
			durable := append([]byte(nil), cs.durable...)
			grp := synthPutGroup(highSeq,
				[]string{"inflight-0", "inflight-1", "inflight-2"},
				[][]byte{filled(4000, 'a'), filled(4000, 'b'), filled(4000, 'c')})
			mangled := mode.fn(grp, rng)

			final := append(append([]byte(nil), durable...), mangled...)
			if err := os.WriteFile(path, final, 0o600); err != nil {
				t.Fatal(err)
			}

			db2, err := Open(path)
			if err != nil {
				t.Fatalf("%s seed %d: Open REFUSED on a holed UN-ACKED tail (the pre-v2 stranding bug): %v",
					mode.name, seed, err)
			}
			for k, v := range acked {
				if got, ok := db2.Get([]byte(k)); !ok || string(got) != v {
					t.Fatalf("%s seed %d: acked %q = %q,%v; want %q", mode.name, seed, k, got, ok, v)
				}
			}
			if db2.Len() != M {
				t.Fatalf("%s seed %d: Len = %d, want %d (an in-flight record leaked in)", mode.name, seed, db2.Len(), M)
			}
			// The un-acked in-flight keys must be absent.
			for _, k := range []string{"inflight-0", "inflight-1", "inflight-2"} {
				if _, ok := db2.Get([]byte(k)); ok {
					t.Fatalf("%s seed %d: un-acked in-flight key %q surfaced", mode.name, seed, k)
				}
			}
			db2.Close()
		}
	}
}

// TestPowerLossSurvivingCommitAfterHole — the DETERMINISTIC adversarial case that
// REDs a naive discriminator: an in-flight group [valid-data][torn-data][valid-
// commit] where the middle data record is holed but the group's own commit sector
// SURVIVES intact. A discriminator that refuses on a bare surviving commit would
// strand the acked prefix (reintroducing the bug). The correct group-granularity
// discriminator sees NO complete valid group (the group is incomplete — one data
// record is torn AND the commit's count won't match a partial run), so it
// truncates the whole in-flight group and recovers the acked prefix.
func TestPowerLossSurvivingCommitAfterHole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	var cs *crashSimFile
	db, err := Open(path, Options{Sync: true, walWrap: crashSimWrap(path, &cs)})
	if err != nil {
		t.Fatal(err)
	}
	acked := map[string]string{}
	const M = 12
	for i := 0; i < M; i++ {
		k := fmt.Sprintf("acked%02d", i)
		v := fmt.Sprintf("v%d", i)
		if err := db.Put([]byte(k), []byte(v)); err != nil {
			t.Fatal(err)
		}
		acked[k] = v
	}
	highSeq := db.seq
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	durable := append([]byte(nil), cs.durable...)

	// Craft records that fill EXACT 4 KiB sectors so the hole lands squarely on the
	// middle data record while data-1 (sector 0) and the commit (sector 2) survive.
	// A put record = 8 (frame) + 8 (seq) + 1 (op) + 4 (klen) + klen + vlen. Keys
	// "inflight-0"/"inflight-1" are 10 bytes → record size 31 + vlen; vlen 4065 →
	// exactly 4096 bytes, one sector.
	const vlen = sectorSize - (8 + 8 + 1 + 4 + 10)
	grp := synthPutGroup(highSeq,
		[]string{"inflight-0", "inflight-1"},
		[][]byte{filled(vlen, 'a'), filled(vlen, 'b')})
	// Layout: data-1 [0,4096), data-2 [4096,8192), commit [8192,8213). Hole sector 1.
	holed := append([]byte(nil), grp...)
	zeroSector(holed, 1)

	// Sanity: sector 0 (data-1) and sector 2 (commit) are byte-identical to grp.
	if string(holed[:sectorSize]) != string(grp[:sectorSize]) {
		t.Fatal("fixture: data-1 sector unexpectedly changed")
	}
	if string(holed[2*sectorSize:]) != string(grp[2*sectorSize:]) {
		t.Fatal("fixture: commit sector unexpectedly changed")
	}
	// Sanity: the surviving commit record decodes (a bare valid commit that the
	// naive discriminator would wrongly refuse on).
	commitOff := 2 * sectorSize
	if _, ok := decodeCommit(holed[commitOff+8:]); !ok {
		t.Fatal("fixture: the group's commit did not survive the hole (test can't prove the discriminator)")
	}

	if err := os.WriteFile(path, append(append([]byte(nil), durable...), holed...), 0o600); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("Open REFUSED on [valid,torn,valid-commit] in-flight group (naive-discriminator bug): %v", err)
	}
	defer db2.Close()
	for k, v := range acked {
		if got, ok := db2.Get([]byte(k)); !ok || string(got) != v {
			t.Fatalf("acked %q = %q,%v; want %q", k, got, ok, v)
		}
	}
	if db2.Len() != M {
		t.Fatalf("Len = %d, want %d (in-flight group must be dropped whole)", db2.Len(), M)
	}
	for _, k := range []string{"inflight-0", "inflight-1"} {
		if _, ok := db2.Get([]byte(k)); ok {
			t.Fatalf("un-acked in-flight key %q surfaced (surviving-commit strand)", k)
		}
	}
	// The recovered store is writable on a clean boundary.
	if err := db2.Put([]byte("post"), []byte("ok")); err != nil {
		t.Fatalf("append after in-flight-group recovery: %v", err)
	}
}

// TestFuzzTornMidWriteBatchAllOrNothing — the in-flight group is ONE opBatch
// record + its commit. A HOLE in the batch (variant A) or a mangled commit
// (variant B) must leave the batch ALL-OR-NOTHING: none of its muts apply, the
// acked prefix is intact, and Open succeeds (it truncates the un-acked group).
func TestFuzzTornMidWriteBatchAllOrNothing(t *testing.T) {
	variants := []struct {
		name   string
		mangle func(grp []byte) []byte
	}{
		{"HOLE-in-batch", func(grp []byte) []byte {
			out := append([]byte(nil), grp...)
			zeroSector(out, 1) // an interior sector of the multi-sector batch record
			return out
		}},
		{"mangle-only-commit", func(grp []byte) []byte {
			out := append([]byte(nil), grp...)
			// Flip a byte inside the trailing commit record's payload → torn commit,
			// batch record left fully intact.
			out[len(out)-1] ^= 0xFF
			return out
		}},
	}
	for _, variant := range variants {
		for seed := int64(1); seed <= 15; seed++ {
			rng := rand.New(rand.NewSource(seed))
			path := filepath.Join(t.TempDir(), "app.blue")

			var cs *crashSimFile
			db, err := Open(path, Options{Sync: true, walWrap: crashSimWrap(path, &cs)})
			if err != nil {
				t.Fatalf("%s seed %d: open: %v", variant.name, seed, err)
			}
			acked := map[string]string{}
			M := 8 + rng.Intn(12)
			for i := 0; i < M; i++ {
				k := fmt.Sprintf("k%04d", i)
				v := fmt.Sprintf("v%d", i)
				if err := db.Put([]byte(k), []byte(v)); err != nil {
					t.Fatalf("%s seed %d: put: %v", variant.name, seed, err)
				}
				acked[k] = v
			}
			highSeq := db.seq
			if err := db.Close(); err != nil {
				t.Fatalf("%s seed %d: close: %v", variant.name, seed, err)
			}
			durable := append([]byte(nil), cs.durable...)

			// A batch spanning ≥3 sectors (5 muts × ~2500-byte values).
			muts := make([]mutation, 5)
			for j := range muts {
				muts[j] = mutation{
					op:    opPut,
					key:   []byte(fmt.Sprintf("batch-key-%d", j)),
					value: filled(2500, byte('A'+j)),
				}
			}
			grp := synthBatchGroup(highSeq, muts)
			if numSectors(grp) < 3 {
				t.Fatalf("%s seed %d: batch group only %d sectors, want ≥3", variant.name, seed, numSectors(grp))
			}
			final := append(append([]byte(nil), durable...), variant.mangle(grp)...)
			if err := os.WriteFile(path, final, 0o600); err != nil {
				t.Fatal(err)
			}

			db2, err := Open(path)
			if err != nil {
				t.Fatalf("%s seed %d: Open REFUSED on an un-acked torn batch group: %v", variant.name, seed, err)
			}
			for k, v := range acked {
				if got, ok := db2.Get([]byte(k)); !ok || string(got) != v {
					t.Fatalf("%s seed %d: acked %q = %q,%v; want %q", variant.name, seed, k, got, ok, v)
				}
			}
			// All-or-nothing: NONE of the batch's muts applied.
			for j := range muts {
				if _, ok := db2.Get([]byte(fmt.Sprintf("batch-key-%d", j))); ok {
					t.Fatalf("%s seed %d: batch mutation %d applied — a torn batch must be all-or-nothing", variant.name, seed, j)
				}
			}
			if db2.Len() != M {
				t.Fatalf("%s seed %d: Len = %d, want %d", variant.name, seed, db2.Len(), M)
			}
			db2.Close()
		}
	}
}

// TestProcessCrashNoSyncPreservesPending — Sync=false models a PROCESS crash (not
// power loss): unsynced pages are NOT mangled (the OS buffer flushes them intact).
// An in-flight group whose commit never got written (the process died between the
// data write and the commit write) is truncated whole; the acked prefix survives.
func TestProcessCrashNoSyncPreservesPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	var cs *crashSimFile
	db, err := Open(path, Options{Sync: false, walWrap: crashSimWrap(path, &cs)})
	if err != nil {
		t.Fatal(err)
	}
	acked := map[string]string{}
	const M = 15
	for i := 0; i < M; i++ {
		k := fmt.Sprintf("k%02d", i)
		v := fmt.Sprintf("v%d", i)
		if err := db.Put([]byte(k), []byte(v)); err != nil {
			t.Fatal(err)
		}
		acked[k] = v
	}
	highSeq := db.seq
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// Close flushed the OS buffer; the on-disk file is the acked prefix. A process
	// crash mid-group leaves the group's DATA records but no commit (the commit
	// write never ran). Pending is PRESERVED (no mangle) — this is the process-crash
	// contract, distinct from a power-loss mangle.
	durable, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var grp []byte
	grp = append(grp, encodeRecord(entry{seq: highSeq + 1, op: opPut, key: []byte("inflight"), value: []byte("x")})...)
	// (no commit — the process died before writing it)
	if err := os.WriteFile(path, append(append([]byte(nil), durable...), grp...), 0o600); err != nil {
		t.Fatal(err)
	}

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("Open refused on an un-committed trailing group: %v", err)
	}
	defer db2.Close()
	for k, v := range acked {
		if got, ok := db2.Get([]byte(k)); !ok || string(got) != v {
			t.Fatalf("acked %q = %q,%v; want %q", k, got, ok, v)
		}
	}
	if _, ok := db2.Get([]byte("inflight")); ok {
		t.Fatal("un-committed trailing record surfaced (no commit landed → must be dropped)")
	}
	if db2.Len() != M {
		t.Fatalf("Len = %d, want %d", db2.Len(), M)
	}
}

// sanity: the crashSimFile promotes pending → durable only on Sync, matching the
// real fsync-acked boundary (guards the fuzz's fidelity).
func TestCrashSimDurableTracksFsync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.blue")
	var cs *crashSimFile
	db, err := Open(path, Options{Sync: true, walWrap: crashSimWrap(path, &cs)})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 7; i++ {
		if err := db.Put([]byte(fmt.Sprintf("k%d", i)), []byte("v")); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs.pending) != 0 {
		t.Fatalf("pending = %d bytes after Sync'd Close, want 0 (all promoted)", len(cs.pending))
	}
	if string(cs.durable) != string(onDisk) {
		t.Fatalf("durable (%d bytes) != on-disk (%d bytes) — the fuzz's durable seed is wrong", len(cs.durable), len(onDisk))
	}
	// The header + 7 single-record groups each carry a trailing commit record.
	if string(onDisk[0:4]) != walMagic || onDisk[4] != walVersion {
		t.Fatalf("on-disk WAL not v2: % x", onDisk[0:5])
	}
}
