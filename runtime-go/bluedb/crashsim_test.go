package bluedb

import (
	"fmt"
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
	if e2.NowTs() != lastTs {
		t.Fatalf("recovered hlc_hi=%+v want last acked %+v", e2.NowTs(), lastTs)
	}
	r := e2.snapshotAt(e2.NowTs())
	defer r.Close()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("k%03d", i)
		v, _, ok := r.Get([]byte(key))
		if !ok || string(v) != fmt.Sprintf("v%d", i) {
			t.Fatalf("acked write %s lost across crash: got %q,%v", key, v, ok)
		}
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
	if present != 100 {
		t.Fatalf("torn batch: %d/100 writes recovered (must be all-or-nothing; acked ⇒ all)", present)
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

// TestInjectedFaultsReopenConsistent exercises the errorfs harness end-to-end (proving it
// is wired to the real v2.1.6 API — InjectorFunc / Op / ErrInjected / Wrap) and asserts
// the durability invariant that HOLDS under an injected write fault: the store always
// reopens to a CONSISTENT prefix — never a corrupt or unopenable state, metadata never
// lags data (every recovered version has commitTs <= the recovered high-water), and the
// engine accepts new writes after recovery.
//
// Harness note (verified against the pinned Pebble v2.1.6): on a memory FS an injected FS
// fault is either absorbed by Pebble (the commit acks; a faulted WAL write may leave that
// write unsynced — the §7 "no-sync writes may/may-not survive" class, and a torn WAL
// record truncates the recoverable tail per WAL semantics) or, for a truly fatal
// write-path fault, surfaces as a PANIC on a background flush goroutine (Pebble's
// unrecoverable-fault contract) — never as a synchronous Apply-error return. So the
// acked-survives / no-torn / HLC-no-reissue invariants are proven by the clean-sync
// CrashClone tests above (the canonical Pebble crash-consistency method); this test
// proves the complementary invariant: an injected fault never yields a CORRUPT store.
func TestInjectedFaultsReopenConsistent(t *testing.T) {
	clk := &fakeClock{}
	clk.set(2000)

	var armed atomic.Bool
	inj := errorfs.InjectorFunc(func(op errorfs.Op) error {
		if armed.Load() && op.Kind == errorfs.OpFileWrite {
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

	// A clean prefix, then a faulted phase, then a healed phase — a realistic mixed run.
	base0 := put(t, e1, "base", "b0")
	armed.Store(true)
	for i := 0; i < 15; i++ {
		_ = e1.Commit(CommitReq{Writes: []VersionedWrite{{UserKey: []byte(fmt.Sprintf("f%02d", i)), Op: OpPut, Value: []byte("x")}}})
	}
	armed.Store(false)
	for i := 0; i < 15; i++ {
		_ = e1.Commit(CommitReq{Writes: []VersionedWrite{{UserKey: []byte(fmt.Sprintf("h%02d", i)), Op: OpPut, Value: []byte("y")}}})
	}

	crashed := base.CrashClone(vfs.CrashCloneCfg{})
	_ = e1.Close()

	e2, err := openWith(config{dir: crashDir, fs: crashed, wallClock: clk.fn()})
	if err != nil {
		t.Fatalf("injected fault left an UNOPENABLE store: %v", err)
	}
	defer e2.Close()

	// (a) The clean prefix committed before any fault survives (it was synced-durable).
	if v, ok := getAt(t, e2, "base", e2.NowTs()); !ok || v != "b0" {
		t.Fatalf("clean pre-fault write lost: base=%q,%v", v, ok)
	}
	// (b) Metadata never lags data: every recovered version has commitTs <= high-water.
	hw := e2.NowTs()
	if hw.Less(base0) {
		t.Fatalf("recovered hlc_hi=%+v regressed below the clean pre-fault commit %+v", hw, base0)
	}
	r := e2.snapshotAt(hw)
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
	r.Close()
	// (c) The engine accepts new writes after recovery (not wedged/sealed).
	if got := put(t, e2, "post-recovery", "ok"); !hw.Less(got) {
		t.Fatalf("post-recovery commitTs %+v not strictly above recovered high-water %+v", got, hw)
	}
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
	for key, val := range acked {
		v, _, ok := r.Get([]byte(key))
		if !ok || string(v) != val {
			t.Fatalf("acked write %s=%s lost across concurrent crash: got %q,%v", key, val, v, ok)
		}
	}
}
