package bluedb

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"
	"github.com/cockroachdb/pebble/v2/vfs/errorfs"
)

// The conformance-oracle fault harness (§7). The old crash-corpus scenarios port; the
// injection harness is net-new on Pebble's fault-injecting VFS. Verified against the
// pinned Pebble v2.1.6 errorfs API: Injector.MaybeError(op errorfs.Op) error, Op is a
// struct {Kind OpKind, Path string, Offset int64}, InjectorFunc adapts a func, Wrap(fs,
// inj) wraps a base vfs.FS, ErrInjected is the injected sentinel. Crash simulation uses
// vfs.NewCrashableMem + (*MemFS).CrashClone, which yields a filesystem containing exactly
// the last-SYNCED data (UnsyncedDataPercent:0 ⇒ deterministic). Every acked commit went
// through Apply(pebble.Sync), so acked ⇒ synced ⇒ present in the crash clone.

const crashDir = "bluedb"

// TestCrashAckedWritesSurvive — the acked⇒survives invariant (§7 invariant 1). Commit
// several writes (all acked via Apply(Sync)); take a crash clone (only synced data);
// reopen; every acked write is present and the recovered high-water matches the data.
func TestCrashAckedWritesSurvive(t *testing.T) {
	clk := &fakeClock{}
	clk.set(2000)
	fs := vfs.NewCrashableMem()

	e1, err := openWith(config{dir: crashDir, fs: fs, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	const n = 50
	var lastTs HLC
	for i := 0; i < n; i++ {
		lastTs = put(t, e1, fmt.Sprintf("k%03d", i), fmt.Sprintf("v%d", i))
	}

	crashed := fs.CrashClone(vfs.CrashCloneCfg{}) // exactly last-synced data
	if err := e1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	e2, err := openWith(config{dir: crashDir, fs: crashed, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer e2.Close()

	// metadata + data recover TOGETHER (C3): high-water == max data version.
	//
	// Errorf, NOT Fatalf, and the two halves are read INDEPENDENTLY (the data loop
	// below reads at `lastTs`, not at the recovered high-water). Both choices are
	// the same C3 lesson: a Fatalf on the first of several assertions masks the
	// later ones, so a mutation proof would only ever demonstrate the metadata
	// half — and reading the data at the recovered high-water would conflate "the
	// row is gone" with "the row is invisible below a high-water that also went
	// missing". G2.9a's mutation has to move the DATA assertion; keep it reachable.
	if e2.NowTs() != lastTs {
		t.Errorf("recovered hlc_hi=%+v want last acked %+v", e2.NowTs(), lastTs)
	}
	r := e2.snapshotAt(lastTs)
	defer r.Close()
	var missing []string
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k%03d", i)
		v, _, ok := r.Get([]byte(key))
		if !ok || string(v) != fmt.Sprintf("v%d", i) {
			missing = append(missing, fmt.Sprintf("%s(got %q,%v)", key, v, ok))
		}
	}
	if len(missing) > 0 {
		// The wording is load-bearing: G2.9a declares "acked write missing after
		// restart" as the assertion its mutation must make fire, and the gate
		// surfaces this line verbatim. Bounded to five examples — an unbounded
		// per-key Errorf emitted 121,145 lines in one C8 fixture run.
		t.Errorf("acked write missing after restart: %d/%d acked writes absent from the crash clone (first: %s)",
			len(missing), n, strings.Join(missing[:min(len(missing), 5)], ", "))
	}
}

// TestCrashNoTornBatch — the all-or-nothing invariant (§7 invariant 2). A single commit
// carrying MANY writes is atomic on disk: after a crash it is entirely present or
// entirely absent, never partially applied. Here it is acked (Sync) so it is entirely
// present.
func TestCrashNoTornBatch(t *testing.T) {
	clk := &fakeClock{}
	clk.set(2000)
	fs := vfs.NewCrashableMem()
	e1, err := openWith(config{dir: crashDir, fs: fs, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	writes := make([]VersionedWrite, 0, 100)
	for i := 0; i < 100; i++ {
		writes = append(writes, VersionedWrite{UserKey: []byte(fmt.Sprintf("m%03d", i)), Op: OpPut, Value: []byte("x")})
	}
	res := e1.Commit(CommitReq{Writes: writes})
	if res.Err != nil {
		t.Fatalf("batch commit: %v", res.Err)
	}

	crashed := fs.CrashClone(vfs.CrashCloneCfg{})
	if err := e1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	e2, err := openWith(config{dir: crashDir, fs: crashed, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer e2.Close()

	r := e2.snapshotAt(e2.NowTs())
	defer r.Close()
	present := 0
	for i := 0; i < 100; i++ {
		if _, _, ok := r.Get([]byte(fmt.Sprintf("m%03d", i))); ok {
			present++
		}
	}
	// Two distinct failures, distinguished because they have distinct causes: NONE
	// of an acked batch surviving is a durability break (the ack was a lie), while
	// SOME of it surviving is an atomicity break (the batch was torn). Collapsing
	// them into one message would let G2.9a's mutation report the atomicity
	// assertion as its falsification, which is not the property it certifies.
	if present == 0 {
		t.Errorf("acked write missing after restart: the entire acked 100-write batch is absent from the crash clone")
	} else if present != 100 {
		t.Errorf("torn batch: %d/100 writes recovered (must be all-or-nothing; acked ⇒ all)", present)
	}
}

// TestCrashHLCNoReissue — the restart-floor invariant (§7 invariant 3, net-new R8). A
// backward wall clock across a crash must NOT re-issue a commitTs: the first
// post-restart commitTs is strictly greater than the persisted high-water, so no key
// ever sees two versions at one commitTs.
func TestCrashHLCNoReissue(t *testing.T) {
	clk := &fakeClock{}
	clk.set(9000) // high wall clock
	fs := vfs.NewCrashableMem()
	e1, err := openWith(config{dir: crashDir, fs: fs, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	hi := put(t, e1, "K", "v1")

	crashed := fs.CrashClone(vfs.CrashCloneCfg{})
	if err := e1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	clk.set(100) // wall clock rewound far into the past across the "crash"
	e2, err := openWith(config{dir: crashDir, fs: crashed, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer e2.Close()

	next := put(t, e2, "K", "v2")
	if !hi.Less(next) {
		t.Fatalf("restart floor violated: hi=%+v next=%+v (must be strictly greater despite backward clock)", hi, next)
	}
	// Both versions distinct at the key — no collision at one commitTs.
	if v, ok := getAt(t, e2, "K", hi); !ok || v != "v1" {
		t.Fatalf("old version clobbered by a re-issued commitTs: Get(K,hi)=%q,%v", v, ok)
	}
	if v, ok := getAt(t, e2, "K", next); !ok || v != "v2" {
		t.Fatalf("new version missing: Get(K,next)=%q,%v", v, ok)
	}
}

// TestSealContractRefusesWrites — the fail-loud seal contract (§7 "seal on
// unrollbackable error"): once sealed, the engine refuses every write path loudly with
// ErrSealed (Commit AND GC), never a silent partial write. The committer seals on a
// durability fault: (a) an Apply(Sync) that returns an error, and (b) a synchronous
// durability panic that unwinds through Apply on the committer goroutine (recovered →
// seal + errored ack). See the harness note below on how Pebble v2.1.6 surfaces faults.
func TestSealContractRefusesWrites(t *testing.T) {
	clk := &fakeClock{}
	clk.set(2000)
	e := openDisk(t, clk.fn())

	_ = put(t, e, "A", "a") // healthy commit

	// Simulate the post-fault state (a durability fault having sealed the engine).
	e.sealed.Store(true)

	r := e.Commit(CommitReq{Writes: []VersionedWrite{{UserKey: []byte("B"), Op: OpPut, Value: []byte("b")}}})
	if r.Err != ErrSealed {
		t.Fatalf("sealed engine must refuse Commit with ErrSealed, got %v", r.Err)
	}
	if _, err := e.GC(); err != ErrSealed {
		t.Fatalf("sealed engine must refuse GC with ErrSealed, got %v", err)
	}
}

// TestInjectedFaultsReopenConsistent is the fail-stop durability regression (Fix-1). It
// injects a WAL fsync fault and asserts the acked⇒durable invariant DETERMINISTICALLY:
// a nil ack ALWAYS means durable, and once a durability fault hits, the engine seals and
// every subsequent commit returns an error — never a nil-acked-but-absent write.
//
// Root cause it locks: Pebble's applyInternal (db.go:882-897, v2.1.6) calls
// Logger.Fatalf(...) on a fatal WAL commit error and then FALLS THROUGH to `return nil`.
// A no-op Fatalf (the pre-Fix-1 quietLogger) makes Apply(Sync) return nil for a write
// that never fsync'd → the committer acks Err:nil for a lost write. Fix-1 makes Fatalf
// PANIC; under pebble.Sync + !noSyncWait the WAL sync is synchronous, so the panic
// unwinds through Apply on the committer goroutine, where process()'s recover seals +
// delivers an errored ack while acked==false. This test proves that contract end-to-end.
func TestInjectedFaultsReopenConsistent(t *testing.T) {
	clk := &fakeClock{}
	clk.set(2000)

	var armed atomic.Bool
	var injected atomic.Int64
	// Arm a fault on the WAL fsync (the *.log file). Under Apply(pebble.Sync) the WAL
	// sync is synchronous, so this fault surfaces through Apply → Fatalf-panic → seal.
	// The injector COUNTS its invocations: G2.6 enumerates this site and requires the
	// count check, because a fixture that cannot prove it injected is indistinguishable
	// from one that passed because nothing happened.
	inj := errorfs.InjectorFunc(func(op errorfs.Op) error {
		isSync := op.Kind == errorfs.OpFileSync || op.Kind == errorfs.OpFileSyncData || op.Kind == errorfs.OpFileSyncTo
		if armed.Load() && isSync && strings.HasSuffix(op.Path, ".log") {
			injected.Add(1)
			return errorfs.ErrInjected
		}
		return nil
	})
	base := vfs.NewCrashableMem()
	fs := errorfs.Wrap(base, inj)

	e1, err := openWith(config{dir: crashDir, fs: fs, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// The durability invariant under test: every nil-acked commit MUST survive reopen.
	type ack struct{ key, val string }
	var durable []ack

	// A clean prefix — every commit acks nil (no fault armed) and must survive.
	for i := 0; i < 10; i++ {
		key, val := fmt.Sprintf("clean%02d", i), fmt.Sprintf("c%d", i)
		r := e1.Commit(CommitReq{Writes: []VersionedWrite{{UserKey: []byte(key), Op: OpPut, Value: []byte(val)}}})
		if r.Err != nil {
			t.Fatalf("clean commit %d unexpectedly failed: %v", i, r.Err)
		}
		durable = append(durable, ack{key, val})
	}

	// Arm the WAL-sync fault. The invariant: NO commit from here on may ack nil-yet-absent.
	// Either it acks nil AND is durable (record it), or it acks an error (fail-stop).
	armed.Store(true)
	var sawSeal bool
	for i := 0; i < 15; i++ {
		key, val := fmt.Sprintf("fault%02d", i), fmt.Sprintf("f%d", i)
		r := e1.Commit(CommitReq{Writes: []VersionedWrite{{UserKey: []byte(key), Op: OpPut, Value: []byte(val)}}})
		if r.Err == nil {
			durable = append(durable, ack{key, val}) // nil ack ⇒ must be durable
		} else {
			sawSeal = true
		}
	}
	// ── The fixture rule, and it must come FIRST. ──
	// At zero injections `!sawSeal` is true for the innocent reason that no fault was
	// ever delivered, and its message would blame the engine for swallowing a fault
	// that never happened. Ordering the fixture check ahead of it is what keeps the
	// diagnosis attributable.
	if n := injected.Load(); n == 0 {
		t.Fatalf("the WAL-fsync injector fired ZERO times — no commit in the armed window reached "+
			"an fsync of a *.log file, so this test proves NOTHING about acked⇒durable. Fix the "+
			"fixture, do not weaken the assertions. (sawSeal=%v)", sawSeal)
	}
	if !sawSeal {
		t.Fatalf("armed WAL-fsync fault produced NO errored ack — a durability fault was silently swallowed (acked⇒durable broken)")
	}
	// Once sealed, every further commit must return an error, never nil.
	rAfter := e1.Commit(CommitReq{Writes: []VersionedWrite{{UserKey: []byte("after-seal"), Op: OpPut, Value: []byte("x")}}})
	if rAfter.Err == nil {
		t.Fatalf("commit after a durability fault acked nil — the sealed engine must refuse all writes")
	}

	crashed := base.CrashClone(vfs.CrashCloneCfg{})
	_ = e1.Close() // horked-DB close; error/none both tolerated (durability already proven)

	e2, err := openWith(config{dir: crashDir, fs: crashed, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("injected fault left an UNOPENABLE store: %v", err)
	}
	defer e2.Close()

	// The core invariant: EVERY nil-acked commit is present after reopen (nil ⇒ durable).
	r := e2.snapshotAt(e2.NowTs())
	defer r.Close()
	for _, a := range durable {
		v, _, ok := r.Get([]byte(a.key))
		if !ok || string(v) != a.val {
			t.Fatalf("nil-acked commit %s=%s ABSENT after reopen — acked⇒durable violated: got %q,%v", a.key, a.val, v, ok)
		}
	}
	// Metadata never lags data: every recovered version has commitTs <= high-water.
	hw := e2.NowTs()
	c := r.Iterate(nil)
	for c.Next() {
		if hw.Less(c.CommitTs()) {
			t.Fatalf("recovered version %q@%+v is ABOVE the recovered high-water %+v (metadata lagged data)", c.Key(), c.CommitTs(), hw)
		}
	}
	if err := c.Err(); err != nil {
		t.Fatalf("cursor error over recovered store: %v", err)
	}
	c.Close()
}

// TestCrashConcurrentNoAckedLoss — under concurrent writer load, every commit that
// returned nil survives a crash clone (§7 "concurrent fault, no acked loss").
func TestCrashConcurrentNoAckedLoss(t *testing.T) {
	clk := &fakeClock{}
	clk.set(2000)
	fs := vfs.NewCrashableMem()
	e1, err := openWith(config{dir: crashDir, fs: fs, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	const g = 8
	const per = 25
	var mu sync.Mutex
	acked := map[string]string{}
	var wg sync.WaitGroup
	for w := 0; w < g; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				key := fmt.Sprintf("g%d-k%d", w, i)
				val := fmt.Sprintf("v%d-%d", w, i)
				r := e1.Commit(CommitReq{Writes: []VersionedWrite{{UserKey: []byte(key), Op: OpPut, Value: []byte(val)}}})
				if r.Err == nil {
					mu.Lock()
					acked[key] = val
					mu.Unlock()
				}
			}
		}(w)
	}
	wg.Wait()

	crashed := fs.CrashClone(vfs.CrashCloneCfg{})
	_ = e1.Close()
	e2, err := openWith(config{dir: crashDir, fs: crashed, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer e2.Close()

	r := e2.snapshotAt(e2.NowTs())
	defer r.Close()
	var missing []string
	for key, val := range acked {
		v, _, ok := r.Get([]byte(key))
		if !ok || string(v) != val {
			missing = append(missing, fmt.Sprintf("%s=%s(got %q,%v)", key, val, v, ok))
		}
	}
	if len(missing) > 0 {
		// Read at the RECOVERED high-water, so "missing" here covers both a lost
		// row and a row stranded above a high-water that was itself lost. Either
		// way the commit acked nil and cannot be read back after restart, which is
		// exactly the acked⇒durable break the phrase names.
		sort.Strings(missing)
		t.Errorf("acked write missing after restart: %d/%d nil-acked concurrent writes unreadable at the recovered high-water %+v (first: %s)",
			len(missing), len(acked), e2.NowTs(), strings.Join(missing[:min(len(missing), 5)], ", "))
	}
}
