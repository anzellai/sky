package bluedb

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// maxBatch caps a single group-commit drain window (§3.2). Mirrors the old
// hand-built committer's cap.
const maxBatch = 1024

// defaultMaxRingEntries is the in-RAM recent-changes ring's hard entry cap (Fix-8, §4.2). Each
// entry is one committed transaction's KeyChange list. Above the cap the oldest entries spill
// to Changelog.Tail (a Pebble read), bounding RAM regardless of reader liveness. Chosen large
// so a healthy system (ring bounded by reader lag well under this) never spills; the cap is the
// backstop against a leaked/never-Release'd reader token pinning the GC floor low. Tests inject
// a small cap via config.maxRingEntries to force the spill path.
const defaultMaxRingEntries = 100_000

// fatalLatch records the FIRST Logger.Fatalf pebble ever reports, so the engine can
// convert it into an error instead of dying (defect N3). It is a standalone value, NOT
// a field on pebbleEngine, and that is forced: opts.Logger must be installed BEFORE
// pebble.Open, which is before the engine struct exists. openWith constructs the latch,
// installs quietLogger{lat: lat}, and wires e.fatal = lat once Open has returned.
//
// The fields are atomics because they are NOT optional here: pebble calls Fatalf from
// ~36 sites, and the flush/compaction ones (version_set.go:671's logAndApply arm) run
// on background goroutines that race every consumer.
//
// takeFatal DOES NOT CLEAR. A clear-on-read latch loses a second fatal, and worse,
// charges a background fatal to whichever innocent batch happened to read it first.
// A pebble fatal is unrollbackable by construction, so "sticky" is the correct shape:
// once set, every consumption point keeps failing until the engine is rebuilt.
type fatalLatch struct {
	set atomic.Bool
	msg atomic.Value // string; stored after set, so readers tolerate the gap
}

// record latches msg iff nothing is latched yet. Nil-receiver-safe: a latch-less
// quietLogger (see Fatalf) never reaches here.
func (l *fatalLatch) record(msg string) {
	if l == nil {
		return
	}
	if l.set.CompareAndSwap(false, true) {
		l.msg.Store(msg)
	}
}

// takeFatal reports the latched fatal, if any. It does NOT clear — see the type doc.
// Nil-receiver-safe so hand-built engines in tests need no latch.
func (l *fatalLatch) takeFatal() (string, bool) {
	if l == nil || !l.set.Load() {
		return "", false
	}
	if m, ok := l.msg.Load().(string); ok {
		return m, true
	}
	// set is published before msg; a reader in that window still knows a fatal happened,
	// which is the load-bearing half.
	return "(pebble fatal recorded; message not yet published)", true
}

// quietLogger routes Pebble's Logger. Pebble's Logger has THREE methods
// (Infof/Errorf/Fatalf) — the design doc's two-method assumption was wrong.
//
// Infof is silenced (chatter). Errorf is NOT: pebble logs at error level while a store
// is degrading — failing flushes, failing compactions — and swallowing that means the
// first thing an operator ever learns about a dying store is the seal.
//
// Fatalf is the whole of defect N3. It MUST NOT be a no-op: on a WAL fsync failure
// pebble's applyInternal (db.go:882-897, pinned v2.1.6) calls Logger.Fatalf(...) and
// then FALLS THROUGH to `return nil`, so a silent Fatalf makes Apply(Sync) return nil
// for a write that never reached durable storage — an Err:nil ack for a lost write.
// But it must not PANIC either, which is what it used to do: pebble calls Fatalf from
// flush and compaction goroutines (version_set.go:671, compaction.go:349/369/1317,
// compaction_picker.go:1962), and NO recover in this package can reach those stacks. A
// disk-full or EIO on a background flush therefore killed the whole Sky app process.
//
// So Fatalf LATCHES, and the engine consumes the latch at every point where it would
// otherwise claim success — see takeFatal's call sites. That is what makes a background
// fatal a sealed engine and an errored ack rather than either a crash or a silent lie.
type quietLogger struct{ lat *fatalLatch }

// quietLoggerErrorBudget bounds Errorf output, and the bound is not cosmetic. Pebble
// retries a failing flush in a tight loop, so an UNBOUNDED stderr Errorf emitted 121,145
// lines in a single run of TestAuditN3BackgroundFatalDoesNotKillTheProcess — burying the
// seal it exists to precede, and making the failure mode of a degrading store "the log
// disk filled up". The first few lines carry the whole diagnostic value: what failed, on
// what file. After the budget the engine's own seal is the signal.
const quietLoggerErrorBudget = 32

var quietLoggerErrors atomic.Int64

func (quietLogger) Infof(string, ...any) {}

func (quietLogger) Errorf(format string, args ...any) {
	n := quietLoggerErrors.Add(1)
	if n > quietLoggerErrorBudget {
		return
	}
	fmt.Fprintf(os.Stderr, "bluedb: pebble: "+format+"\n", args...)
	if n == quietLoggerErrorBudget {
		fmt.Fprintf(os.Stderr, "bluedb: pebble: further error-level messages suppressed "+
			"(budget %d); a fatal will still seal the engine\n", quietLoggerErrorBudget)
	}
}

func (l quietLogger) Fatalf(format string, args ...any) {
	msg := fmt.Sprintf("bluedb: pebble fatal: "+format, args...)
	if l.lat == nil {
		// No latch ⇒ nobody will ever consume this. Panicking is the defect N3 is about,
		// but silently discarding a fatal is strictly worse, so an unlatched logger keeps
		// the old fail-loud behaviour. Only raw pebble handles opened outside openWith
		// (test fixtures) land here; every engine path installs a latch.
		panic(msg)
	}
	l.lat.record(msg)
}

// config carries Open parameters, including the test seams (injectable clock + FS).
type config struct {
	dir          string
	fs           vfs.FS          // nil ⇒ disk default
	wallClock    wallClockMillis // nil ⇒ system clock
	memTableSize uint64          // 0 ⇒ Pebble default; a small value forces early SSTable spills
	// manualDrain (test seam): when true the auto committer goroutine is NOT started, so a
	// test can enqueue several jobs and drain them into ONE deterministic batch (exercises the
	// intra-batch `pending` validation path). Uses submitForTest/drainOnceForTest (test file).
	manualDrain bool
	// maxRingEntries overrides the recent-changes ring's hard entry cap (Fix-8, §4.2). 0 ⇒
	// defaultMaxRingEntries. Tests set it small to force the spill-to-Changelog.Tail path.
	maxRingEntries int
	// leaseTimeout overrides the hot-key lease reaper's reclaim age (§6.3). 0 ⇒
	// defaultLeaseTimeout. Tests set it short to exercise the crashed-driver backstop.
	leaseTimeout time.Duration
}

// commitJob is one enqueued Commit awaiting the committer.
type commitJob struct {
	req  CommitReq
	done chan CommitResult
}

// pebbleEngine is the L1 Engine backed by Pebble.
type pebbleEngine struct {
	db  *pebble.DB
	hlc *hlcClock
	reg *watermarkRegistry

	ch chan *commitJob
	wg sync.WaitGroup

	sealed atomic.Bool

	// fatal is the pebble Logger.Fatalf latch (defect N3), wired from the standalone
	// fatalLatch openWith had to build before opts.Logger existed. Consumed — never
	// cleared — at every point the engine would otherwise report success: after
	// pebble.Open, in Close, after each committer Apply, and after each GC Apply.
	fatal *fatalLatch

	// recent is the in-RAM recent-changes ring (§4.2), mutated ONLY by the committer
	// goroutine (append/after/trim). trimReqs marshals GC's trim(T) onto the committer so
	// the ring stays single-writer — GC enqueues, the committer drains at the top of each
	// drain (Fix-3/R-2.9). Coalescing: a dropped request is re-sent (higher T) on the next
	// GC pass; trim is monotone so over-retaining briefly is safe.
	recent   *recentRing
	trimReqs chan HLC

	// Phase-2b hot-key strict-2PL lease machinery (§6). hotKeys is fed by the committer
	// (recordAbort on a point-read conflict) and read by the driver (anyHot/hotSubset);
	// leases is the per-hot-key FIFO queue the driver acquires/releases. reaperStop signals
	// the committer-side lease-reaper goroutine to exit at Close. NONE of these are touched by
	// the blind-write path (e.Commit / processBlindPhase1) — the OLTP firehose is unaffected.
	hotKeys    *hotKeyTable
	leases     *leaseManager
	reaperStop chan struct{}

	// durableHi is the highest commitTs whose Apply(Sync) has RETURNED (i.e. is
	// durable on the WAL), advanced by the committer AFTER each successful Apply —
	// never at HLC-assignment time. It is the durable analogue of the in-memory
	// high-water (hlc.highWater() == c.last), which the committer bumps at next()
	// BEFORE the Apply. GC's advanceThreshold clamps its candidate to durableHi so
	// the persisted GC threshold T can never outrun what's durable (§5.2, Fix-3):
	// otherwise a crash between GC's threshold-Sync and a later commit's Apply-Sync
	// could recover hlc_hi < gc_threshold → every reader wedges on ErrSnapshotTooOld
	// and post-recovery commits fall into the trimmed changelog tail. Monotone up.
	durMu        sync.Mutex
	durableHiVal HLC

	// Phase-4 change-feed (changefeed.go): the post-durability delta stream an rt pump (4b) or
	// the EmbeddedBackend's internal reactive pump (4a) drains. subMu guards the subscriber set;
	// emit is NON-BLOCKING (a full subscriber channel drops its batch + latches overflow) so the
	// single committer is NEVER stalled by a slow drain (R1). Nil map ⇒ nobody listening ⇒
	// hasChangeSubs() short-circuits the emit entirely (zero cost on the no-reactive firehose).
	subMu         sync.RWMutex
	changeSubs    map[uint64]*changeFeedSub
	changeSubNext uint64

	// Close is a THREE-PHASE, RETRYABLE state machine (defect N4) — not a sync.Once.
	//
	//	closed   — phase 1. Set under closeMu.Lock(). From here on no NEW reader can be
	//	           pinned and no commit is accepted. It is the FLAG, not the lock, that
	//	           holds that line, which is what lets phase 2 run with closeMu released.
	//	dbClosed — phase 3. The Pebble handle is gone; the engine is terminal.
	//	closeErr — the result of e.db.Close(), replayed to every later Close call so a
	//	           second Close (t.Cleanup after an explicit one, say) is idempotent
	//	           rather than a second db.Close() — which pebble PANICS on (db.go:1698).
	//
	// closeMu is an RWMutex so the hot read paths (Commit's isClosed, beginSnapshot's
	// check-and-pin) take it shared and never serialize against each other.
	closeMu  sync.RWMutex
	closed   bool
	dbClosed bool
	closeErr error
}

// durableHi returns the highest durably-applied commitTs (guards the GC clamp).
func (e *pebbleEngine) durableHi() HLC {
	e.durMu.Lock()
	defer e.durMu.Unlock()
	return e.durableHiVal
}

// advanceDurableHi raises durableHi to ts iff ts is higher (monotone). Called by
// the committer AFTER a successful Apply(Sync), before acking.
func (e *pebbleEngine) advanceDurableHi(ts HLC) {
	e.durMu.Lock()
	defer e.durMu.Unlock()
	if e.durableHiVal.Less(ts) {
		e.durableHiVal = ts
	}
}

var _ Engine = (*pebbleEngine)(nil)

// Open opens (or creates) a BlueDB engine at dir with the LOCKED comparer.
func Open(dir string) (Engine, error) {
	return openWith(config{dir: dir})
}

func openWith(cfg config) (*pebbleEngine, error) {
	// N3: the latch has to exist before opts, because opts.Logger has to exist before
	// pebble.Open, which is before there is any engine to hang it on.
	fatal := &fatalLatch{}
	opts := &pebble.Options{
		Comparer: skydbComparer,
		Logger:   quietLogger{lat: fatal},
	}
	if cfg.fs != nil {
		opts.FS = cfg.fs
	}
	if cfg.memTableSize != 0 {
		opts.MemTableSize = cfg.memTableSize
	}
	db, err := pebble.Open(cfg.dir, opts)
	if err != nil {
		return nil, err
	}
	// N3 consumption point 1/5 — a fatal DURING Open. version_set.go:191/196/202
	// (MANIFEST flush / sync / set-current in createManifest) fire here, and Open's own
	// error propagation from those sites is not something to rely on: a latched fatal
	// means the store this handle describes is not sound, so refuse the handle.
	if msg, ok := fatal.takeFatal(); ok {
		_ = db.Close()
		return nil, fmt.Errorf("%w: %s", ErrPebbleFatal, msg)
	}

	persistedHi, err := readMetaHLC(db, metaHLCHi)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	persistedThreshold, err := readMetaHLC(db, metaGCThreshold)
	if err != nil {
		_ = db.Close()
		return nil, err
	}

	e := &pebbleEngine{
		db:    db,
		hlc:   newHLCClock(persistedHi, cfg.wallClock),
		fatal: fatal, // N3: the latch quietLogger has been writing to since before Open
		// Everything on disk up to persistedHi (metaHLCHi) was written in a synced,
		// committed batch, so the durable high-water starts AT persistedHi (Fix-3). A
		// fresh store has persistedHi = {0,0}. Seeding it here (not {0,0}) lets the first
		// post-reopen GC advance T normally even before a new commit lands.
		durableHiVal: persistedHi,
		// Buffered so concurrent writers ENQUEUE without blocking, letting the committer's
		// drain coalesce many in-flight commits into ONE Apply(Sync)/one fsync (§3.2 group
		// commit — the throughput lever, since the single committer forgoes Pebble's commit
		// pipeline). FIFO delivery preserves the commitTs assignment order → the total order
		// is unchanged. Cap = maxBatch (one full drain window).
		ch: make(chan *commitJob, maxBatch),
	}
	e.reg = newWatermarkRegistry(e.hlc.highWater, persistedThreshold)
	// Wire the durable-high accessor so GC's advanceThreshold can clamp T ≤ durableHi.
	e.reg.durableHi = e.durableHi

	// Recent-changes ring + trim channel (§4.2). Cold-start rebuild: seed the ring from the
	// durable changelog tail above the GC floor so a txn that begins right after Open has its
	// validation window served in-RAM (the retention invariant: the ring covers everything
	// above T). Floored at persistedThreshold — the same watermark version-GC uses.
	e.recent = newRecentRing()
	e.recent.maxEntries = cfg.maxRingEntries
	if e.recent.maxEntries == 0 {
		e.recent.maxEntries = defaultMaxRingEntries
	}
	e.trimReqs = make(chan HLC, 8)
	// C6b: a seed that cannot be completed raises the floor to persistedHi instead of
	// leaving a partially-seeded ring behind. The ring IS the validation window, and a
	// window with a hole under-rejects — the same class as N6. Failing to open on a
	// transient read error would be too harsh (the store is fine), and the correct
	// degradation already exists: a readTs below `floor` reports spilled=true and the txn
	// validates via the durable Changelog.Tail instead. So the remedy is to make the ring
	// admit it does not cover that range.
	//
	// Nothing today can observe the difference — a txn's readTs is durableHi, which right
	// after Open equals persistedHi, so its window is empty either way — but that argument
	// rests entirely on Txn being the only producer of a CommitReq.ReadSet. That is a
	// property of today's call graph, not of this function, which is exactly the kind of
	// premise a fail-open should not be resting on.
	seedFailed := false
	if tail, terr := (&changelog{db: db}).Tail(persistedThreshold); terr != nil {
		seedFailed = true
	} else {
		for _, entry := range tail {
			chg, derr := DecodeChangelogPayload(entry.Payload)
			if derr != nil {
				seedFailed = true
				break
			}
			if len(chg) > 0 {
				e.recent.append(entry.CommitTs, chg)
			}
		}
	}
	if seedFailed && e.recent.floor.Less(persistedHi) {
		e.recent.floor = persistedHi
	}
	// Floor at least at the persisted GC threshold (the same watermark version-GC uses). Use
	// max, not assign: a small-cap cold-start seed may have already spilled and raised the
	// floor above persistedThreshold — never lower it back (monotone, Fix-8 correctness).
	if e.recent.floor.Less(persistedThreshold) {
		e.recent.floor = persistedThreshold
	}

	// Phase-2b hot-key lease machinery (§6). Wired before the committer starts so recordAbort
	// (committer goroutine) always sees a non-nil hotKeys.
	e.leases = newLeaseManager(cfg.leaseTimeout)
	e.hotKeys = newHotKeyTable(e.leases)
	e.reaperStop = make(chan struct{})

	if !cfg.manualDrain {
		e.wg.Add(2)
		go e.committer()
		go e.leaseReaper()
	}
	return e, nil
}

// leaseReaper is the committer-side timeout backstop for the hot-key leases (§6.3). It wakes
// periodically and reclaims any lease whose active holder has held it past leases.timeout — the
// driver-crashed-before-release case (release is driver-side, so without this a crashed holder
// would wedge the FIFO queue forever). It also decays the hot set so a key whose contention has
// ended retires to the optimistic fast path (§6.3 auto-retirement). Runs only in the auto-
// committer configuration (manualDrain tests drive process() directly and take no leases).
func (e *pebbleEngine) leaseReaper() {
	defer e.wg.Done()
	interval := e.leases.timeout / 4
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-e.reaperStop:
			return
		case <-t.C:
			e.leases.reap()
			e.hotKeys.decay()
		}
	}
}

// beginSnapshot backs BOTH Begin() and Snapshot() (§3.4, R-2.8) — it is the single
// reader-construction path, which is what keeps H1 fixed: a second construction site is
// exactly how Snapshot drifted into picking an un-applied readTs. In ONE critical section under durMu it reads
// readTs = durableHi (the durably-applied high-water, advanced post-Apply), pins the Pebble
// snapshot AFTER that read (so snap ⊇ every commit ≤ readTs), and registers the token at
// readTs (flooring GC's T at readTs → the retention invariant). Holding durMu across the
// RegisterAt serializes against GC's advanceThreshold (which reads durableHi under durMu
// then takes w.mu — never both at once), so GC cannot advance T past readTs between the pin
// and the registration. durMu is never nested under w.mu anywhere, so there is no lock cycle.
// Defect N4: the closed-check and the pin are taken under ONE closeMu.RLock. A bare
// isClosed() followed by db.NewSnapshot() is a TOCTOU against Close — NewSnapshot on a
// closed DB panics unconditionally (pebble db.go:2062 → d.closed.Load()) — and a reader
// pinned after Close read its refcount is invisible to the drain. Holding the read lock
// across RegisterAt makes the two mutually exclusive with Close's phase 1: either the
// pin is registered before `closed` is set (and the drain waits for it), or the check
// sees `closed` and refuses. There is no third interleaving.
//
// The section takes no unbounded wait (no channel receive, no lock held by a blocked
// party), so Close's pending Lock() — which blocks new RLocks behind it — is never
// starved and never waits on a reader.
//
// The reader returned here goes to Begin() and is closed by Txn.Commit/Abort, so the
// watermark token is its ONLY liveness pin — it gets no outer pin, unlike the
// Snapshot()/snapshotAt paths (see trackedReader). That makes it wholly dependent on
// pebbleReader.Close() releasing the token as its LAST statement, after snap.Close()
// has returned; the previous order left a window in which a transaction ending exactly
// as the drain completed still held an unclosed snapshot at phase 3. That is fixed in
// reader.go and pinned by TestAuditN4BeginPathReaderClosesSnapshotBeforeItsPin — do not
// reorder those two statements.
func (e *pebbleEngine) beginSnapshot() (*pebbleReader, error) {
	e.closeMu.RLock()
	defer e.closeMu.RUnlock()
	if e.closed {
		return nil, ErrClosed
	}
	e.durMu.Lock()
	readTs := e.durableHiVal
	snap := e.db.NewSnapshot() // ordered AFTER the durableHi read → snap ⊇ every commit ≤ readTs
	tok, err := e.reg.RegisterAt(readTs)
	e.durMu.Unlock()
	if err != nil {
		_ = snap.Close()
		return nil, err
	}
	return &pebbleReader{snap: snap, readTs: readTs, tok: tok, reg: e.reg}, nil
}

// pinIfOpen is the NON-READER arm of the N4 check-and-pin, and it is deliberately the
// same mechanism beginSnapshot/snapshotAtChecked already use rather than a second
// lifecycle scheme: verify Close's phase 1 has not run AND take the liveness pin under
// ONE closeMu.RLock, then do the work lock-free and unpin at the end.
//
// WHY THE CALLERS NEED IT. Every operation that touches e.db outside the committer
// goroutine is a TOCTOU against Close otherwise, because pebble does not degrade on a
// closed handle — it panics unconditionally (db.go: NewIter/Apply/NewSnapshot all check
// d.closed and panic "pebble: closed"). `isClosed()` alone is a CHECK with no pin, so
// Close's phase 3 db.Close() can land between the check and the very next statement;
// closeWithin's phase-2 drain waits only on e.reg's pins, so an operation holding no pin
// is invisible to it. That is what let Changelog().Tail race Close into an unrecovered
// panic (reproduced on round 0) and what left gc.go's whole pass unprotected after its
// isClosed() returned false. Holding the pin makes the two mutually exclusive: either the
// pin is taken before `closed` is set (and the drain waits for it), or the check sees
// `closed` and the caller gets ErrClosed. There is no third interleaving.
//
// The pin is reg.pin() — LIVENESS ONLY, not a `live` registration — because these callers
// have no readTs and must not floor GC (GC itself is one of them; registering would make
// a pass clamp its own candidate).
//
// LOCK ORDER: closeMu → w.mu, a prefix of the package order (closeMu → durMu → w.mu →
// hlcMu); reg.pin takes only w.mu and returns without waiting on anything, so the section
// takes no unbounded wait and never starves Close's pending Lock().
//
// The caller MUST unpin on every path (defer), and the unpin MUST be ordered AFTER the
// last use of e.db — including an iterator's Close. Releasing early re-opens the exact
// window this closes.
func (e *pebbleEngine) pinIfOpen() (ReaderToken, error) {
	e.closeMu.RLock()
	defer e.closeMu.RUnlock()
	if e.closed {
		return 0, ErrClosed
	}
	return e.reg.pin(), nil
}

// enqueueTrim is GC's hand-off of a new threshold T to the committer (Fix-3). Non-blocking:
// a full channel drops the request and a later GC pass re-sends a higher T (trim is monotone
// → dropping only over-retains briefly). GC NEVER touches the ring directly.
func (e *pebbleEngine) enqueueTrim(T HLC) {
	if e.trimReqs == nil {
		return
	}
	select {
	case e.trimReqs <- T:
	default:
	}
}

// drainTrimRequests applies any GC-enqueued trim(T) on the COMMITTER goroutine, at the top of
// each drain, before the ring is otherwise touched (Fix-3). Coalesces to the highest T seen.
func (e *pebbleEngine) drainTrimRequests() {
	var maxT HLC
	got := false
	for {
		select {
		case t := <-e.trimReqs:
			if !got || maxT.Less(t) {
				maxT, got = t, true
			}
		default:
			if got {
				e.recent.trim(maxT)
			}
			return
		}
	}
}

// readMetaHLC reads an HLC-valued metadata key. ABSENCE is the fresh-store
// sentinel {0,0}; a PRESENT value of the wrong length is CORRUPTION and is
// returned as an error, not silently folded into that sentinel (N5).
//
// The distinction is load-bearing. hlc_hi is the restart floor: newHLCClock
// floors the clock to it, so a corrupt hlc_hi read as {0,0} restarts the clock
// from the bare wall clock and can RE-ISSUE a commitTs already on disk. Two
// transactions then share a data key (the MVCC key is userKey ‖ ~commitTs), the
// later Set silently overwrites the earlier committed version, and no read can
// tell. That is irrecoverable, so openWith refuses to open instead of guessing.
//
// The length test is exact (!=, not <) and safe to rely on: every writer emits
// exactly hlcEncodedLen bytes via encodeHLC (committer.go, gc.go), an absent key
// returns pebble.ErrNotFound and is handled above, and nothing ever Sets an
// empty meta value.
//
// TRADE-OFF, deliberately taken: this makes any future WIDENING of a meta value
// (say a 13-byte hlc_hi carrying an extra field) a hard refuse-to-open for older
// binaries rather than a lenient truncating read — and there is no repair verb
// yet, so a store corrupted here needs an out-of-band fix. That is the correct
// side to err on: refusing to open is recoverable by rolling the binary forward,
// re-issuing a committed commitTs is not.
func readMetaHLC(db *pebble.DB, name string) (HLC, error) {
	v, closer, err := db.Get(encodeMetaKey(name))
	if err == pebble.ErrNotFound {
		return HLC{}, nil
	}
	if err != nil {
		return HLC{}, err
	}
	defer closer.Close()
	if len(v) != hlcEncodedLen {
		return HLC{}, fmt.Errorf(
			"bluedb: corrupt metadata %q: got %d bytes, want %d — refusing to open "+
				"(a mis-sized hlc_hi would restart the commit clock and re-issue a committed commitTs)",
			name, len(v), hlcEncodedLen)
	}
	return decodeHLC(v), nil
}

func (e *pebbleEngine) NowTs() HLC { return e.hlc.highWater() }

// Changelog hands back the post-commit stream reader. The engine pointer travels with it
// (N4): the returned value outlives this call — it is on the exported Engine interface, so
// a caller may hold it across a Close — and its Tail does a Pebble read, so the reader
// itself has to take the check-and-pin. `&changelog{db: e.db}` alone was a raw handle with
// no lifecycle at all.
func (e *pebbleEngine) Changelog() Changelog       { return &changelog{db: e.db, e: e} }
func (e *pebbleEngine) Readers() WatermarkRegistry { return e.reg }

// snapshotCalls counts Engine.Snapshot() invocations — a test seam (mirrors validateCalls /
// changelogTailCalls). It lets the blindPut fast-path test assert an index-less autocommit Put
// pays ZERO pre-image snapshots. Package-level so tests can snapshot it around one Put.
var snapshotCalls atomic.Int64

// Snapshot pins a frozen, lock-free consistent view for an ad-hoc (transaction-less)
// read: readTs, the Pebble snapshot seqnum and the watermark token are all chosen in
// ONE critical section (§2.5 invariant, C4).
//
// Defect H1. This used to be built in three UNSYNCHRONISED steps —
// reg.Register() (which picks readTs = the in-memory HLC high-water), then
// db.NewSnapshot(). Both halves are wrong, and independently:
//
//   - readTs was the in-memory high-water, which the committer bumps at hlc.next()
//     BEFORE the Apply. So a readTs could name a commitTs that has been ASSIGNED but
//     is not yet applied — in flight, not durable, and not in any snapshot. The
//     reader then claims to be a consistent view as of a commit that does not exist
//     yet, and (worse) floors GC at it.
//   - even with the right readTs, the token and the snapshot pin were taken at
//     different instants with no lock between them, so a commit landing in the gap
//     is in the pin but not under the readTs, or vice versa.
//
// beginSnapshot (:beginSnapshot) already solves exactly this for Begin(): under durMu
// it reads readTs = durableHi (advanced only AFTER Apply(Sync) returns), pins the
// snapshot after that read, and registers the token at that same readTs before
// releasing durMu — which also serializes against GC's advanceThreshold. Snapshot is
// therefore a thin wrapper over it rather than a second, subtly different
// construction; the two paths cannot drift apart because there is only one of them.
//
// Note what the fix is NOT: hoisting a durableHi read into watermarkRegistry.Register.
// advanceThreshold reads durableHi before w.mu deliberately (a stale-LOW value clamps
// the GC threshold more conservatively, which is always safe); a stale-low value is
// NOT safe for CHOOSING a readTs — it would silently hand back an older view than the
// caller's own just-acked commit. The atomicity has to live where the snapshot is
// pinned, which is here.
//
// LOCK ORDER (whole package): closeMu → durMu → w.mu → hlcMu. Register/candidateLocked
// call w.highWater() → hlc.highWater() while holding w.mu, which is why hlcMu is last;
// beginSnapshot takes durMu then w.mu (via RegisterAt) and never the reverse. No path
// inverts this — do not introduce one.
func (e *pebbleEngine) Snapshot() (Reader, error) {
	snapshotCalls.Add(1)
	r, err := e.beginSnapshot()
	if err != nil {
		// Explicitly nil, NOT `return e.beginSnapshot()`: returning a nil *pebbleReader
		// through a Reader result would hand back a non-nil interface holding a nil
		// pointer, and every `if r != nil` at a call site would be wrong.
		return nil, err
	}
	return e.track(r), nil
}

// track wraps a reader in the close-drain outer pin (N4). See trackedReader.
func (e *pebbleEngine) track(r *pebbleReader) *trackedReader {
	return &trackedReader{pebbleReader: r, reg: e.reg, outer: e.reg.pin()}
}

// trackedReader is a reader whose liveness pin outlives its own Close work.
//
// Its ORIGINAL job — bridging the gap pebbleReader.Close left between releasing the
// watermark token and closing the *pebble.Snapshot — is now done one layer down, in
// pebbleReader.Close's statement order. What remains is the job only this type can do:
// snapshotAtChecked's time-travel reader takes NO watermark token at all (reg == nil,
// deliberately, so a below-floor readTs cannot drag T down or fail on
// ErrSnapshotTooOld), so without an outer pin it would be INVISIBLE to the close drain.
// The pin is taken at construction and dropped only after the inner Close has returned,
// which keeps that ordering guarantee true for this path as well.
//
// On the Snapshot() path the two mechanisms now overlap, and deliberately so: the outer
// pin is strictly conservative (a reader counts as live for the whole of its teardown),
// and one path with two independent reasons to be correct is cheaper than two
// constructions. Everything Reader requires other than Close is promoted from the
// embedded *pebbleReader, so there is no second implementation of the read path.
type trackedReader struct {
	*pebbleReader
	reg   *watermarkRegistry
	outer ReaderToken
	once  sync.Once
}

var _ Reader = (*trackedReader)(nil)

func (r *trackedReader) Close() {
	// once: a double Close must not drop a pin the registry has since reissued to a
	// different reader (tokens are monotone, but unpin is by value).
	r.once.Do(func() {
		r.pebbleReader.Close()
		r.reg.unpin(r.outer)
	})
}

// deadReader is the Reader a closed engine hands back on the legacy snapshotAt path,
// which has no error result to return (see snapshotAt). Every read answers "nothing,
// and here is why" — Err() is ErrClosed, so a caller that fails closed on Err (as
// Txn.Commit does) cannot mistake it for an empty store.
type deadReader struct{ err error }

var _ Reader = (*deadReader)(nil)

func (r *deadReader) Get([]byte) ([]byte, HLC, bool) { return nil, HLC{}, false }
func (r *deadReader) Iterate([]byte) Cursor          { return &pebbleCursor{err: r.err} }
func (r *deadReader) Err() error                     { return r.err }
func (r *deadReader) ReadTs() HLC                    { return HLC{} }
func (r *deadReader) Close()                         {}

// snapshotAtChecked pins a reader at an EXPLICIT readTs (time-travel read). Not part of
// the public Engine surface (§3.1 drops caller-supplied readTs to close the 2a TOCTOU);
// it exercises the MVCC read-resolution core (§2.5) at arbitrary versions and backs the
// versioned round-trip / snapshot-isolation tests.
//
// Defect N4, the arm the first plan missed: this path used to call db.NewSnapshot()
// with NO closed-check at all (an immediate panic if Close had run) and to build its
// reader with reg == nil — so it took no token, and a live time-travel reader was
// INVISIBLE to the close drain. Both are fixed here: the check-and-pin is one
// closeMu.RLock section, exactly as beginSnapshot's.
//
// The pin is reg.pin(), NOT a `live` registration: a time-travel readTs is deliberately
// allowed to sit below the GC floor, so registering it would either drag T back down or
// (via RegisterAt) fail the read with ErrSnapshotTooOld. Liveness and the GC floor are
// separate questions and this is the caller that proves it.
func (e *pebbleEngine) snapshotAtChecked(readTs HLC) (Reader, error) {
	e.closeMu.RLock()
	defer e.closeMu.RUnlock()
	if e.closed {
		return nil, ErrClosed
	}
	return e.track(&pebbleReader{snap: e.db.NewSnapshot(), readTs: readTs}), nil
}

// snapshotAt is the single-result form snapshotAtChecked's callers use. It is kept
// because the signature is load-bearing for the ported tests, and a closed engine is
// not a state any of them reach; the closed case returns a deadReader (Err() ==
// ErrClosed) rather than a nil Reader, so a caller that ignores the distinction gets an
// answer that fails closed instead of a nil dereference.
func (e *pebbleEngine) snapshotAt(readTs HLC) Reader {
	r, err := e.snapshotAtChecked(readTs)
	if err != nil {
		return &deadReader{err: err}
	}
	return r
}

// Commit enqueues the request to the single committer and blocks until durable.
//
// The result is a NAMED return, and that is load-bearing (C1). `e.ch <- job` panics if
// Close raced us and closed the channel; the deferred recover must convert that into
// ErrClosed. It cannot do so by sending into job.done — `return <-job.done` never
// executed, so nothing will ever read that buffered channel, and the function would
// return the ZERO CommitResult: Err == nil. A commit against a closed engine would
// report SUCCESS. The recover therefore assigns `res` directly.
//
// Naming the return WITHOUT rewriting the deferred body is a no-op that looks like a
// fix — res stays zero and the false ack survives. The two halves only work together,
// which is why TestAuditC1CommitOnClosedChannelReturnsError exists.
//
// The recover is also the RIGHT mechanism here: holding closeMu across the send to make
// the race impossible would stall Close behind a full e.ch (the committer must drain it,
// and Close takes the same lock), trading a false ack for a deadlock.
func (e *pebbleEngine) Commit(req CommitReq) (res CommitResult) {
	if e.sealed.Load() {
		return CommitResult{Err: ErrSealed}
	}
	// N3 consumption point 6/6 — the DOOR, and it is NOT one of the plan's five. It was
	// added because running the fixture showed the five are unreachable in the case that
	// matters most.
	//
	// After a background MANIFEST fatal, pebble does not degrade — it WEDGES. logAndApply
	// treats any MANIFEST error as fatal by design (version_set.go:664-672: "preferred to
	// attempting to unwind various file and b-tree reference counts"), so the flush never
	// completes, memtables accumulate, and every writer blocks inside Apply. Measured with
	// a SINGLE injected MANIFEST write error: the next Commit did not return in 30s, the
	// engine never sealed, and Close could not run either — because all three of those
	// consume the latch AFTER an Apply that will never finish.
	//
	// So without this check the fix trades a process kill for a silent, permanent hang of
	// every writer, which is not obviously an improvement. With it, a latched fatal is a
	// prompt typed error at the door and the app can fail over. The cost on the firehose
	// is one atomic load, the same as the sealed check above.
	if msg, ok := e.fatal.takeFatal(); ok {
		e.sealed.Store(true)
		return CommitResult{Err: fmt.Errorf("%w: %w: %s", ErrSealed, ErrPebbleFatal, msg)}
	}
	if e.isClosed() {
		return CommitResult{Err: ErrClosed}
	}

	// Fix-3 (liveness): a BLIND write (ReadSet == nil) whose target key is currently HOT must not
	// firehose past a lease-holding RMW txn — a blind-write flood on a hot key would otherwise
	// exhaust that RMW's retries and starve it to ErrConflict while it holds every lease. Route
	// such a blind write through the SAME FIFO lease the transactional path uses, so it queues
	// BEHIND the lease holder and the RMW makes progress. This is DRIVER-side (before enqueue) —
	// the committer is the single writer and MUST NOT block on a lease. The overwhelming common
	// case (no hot key anywhere) is a single pair of atomic loads and takes NO lock, so the OLTP
	// firehose pays zero (blindHotLeases returns nil immediately). Not a correctness change —
	// validation already prevents lost updates; blind last-write-wins stays the caller's choice.
	if req.ReadSet == nil {
		if tickets := e.blindHotLeases(req); len(tickets) > 0 {
			defer e.releaseLeases(tickets)
		}
	}

	job := &commitJob{req: req, done: make(chan CommitResult, 1)}
	defer func() {
		// A send on a closed channel panics if Close raced us; recover to ErrClosed.
		// Assign the NAMED result — job.done is unread on this path (see the doc comment).
		if r := recover(); r != nil {
			res = CommitResult{Err: ErrClosed}
		}
	}()
	e.ch <- job
	return <-job.done
}

// blindHotLeases returns the FIFO lease tickets a blind write must hold before committing to a
// currently-hot target key (Fix-3), acquired in canonical bytes.Compare order — the SAME
// whole-set canonical-order acquisition the transactional lease path uses, so a blind write that
// touches several hot keys cannot deadlock against a transactional holder. The lock-free atomic
// gate (hotKeys.hotN == 0 AND leases.waiterN == 0 ⇒ nothing hot anywhere) makes the no-hot-key
// firehose pay a single pair of atomic loads and take NO lock. Returns nil when no target is hot.
func (e *pebbleEngine) blindHotLeases(req CommitReq) []*leaseTicket {
	if e.hotKeys == nil || e.leases == nil {
		return nil // not wired (manualDrain never reaches here — it bypasses e.Commit)
	}
	// Lock-free fast gate: no key is hot and no lease is contended → zero-cost firehose path.
	if e.hotKeys.hotN.Load() == 0 && e.leases.waiterN.Load() == 0 {
		return nil
	}
	// Something is hot somewhere — precisely check THIS req's write keys (blind writes have no
	// reads, so the touched set is exactly the write-set keys).
	keys := make([][]byte, 0, len(req.Writes))
	for i := range req.Writes {
		keys = append(keys, req.Writes[i].UserKey)
	}
	hot := e.hotKeys.hotSubset(keys) // dedup + ascending bytes.Compare order (deadlock-free acquire)
	if len(hot) == 0 {
		return nil
	}
	tickets := make([]*leaseTicket, 0, len(hot))
	for _, k := range hot {
		t := e.leases.acquire(string(k))
		<-t.granted
		tickets = append(tickets, t)
	}
	return tickets
}

// releaseLeases releases every held lease ticket (driver-side; the lease-reaper goroutine is the
// crash backstop). Shared by the blind-write path (Fix-3) and the transactional lease path (Fix-4).
func (e *pebbleEngine) releaseLeases(tickets []*leaseTicket) {
	for _, t := range tickets {
		e.leases.release(t)
	}
}

// isClosed reports whether Close's phase 1 has run. Deliberately a SHORT shared lock
// with nothing else inside it: e.Commit calls this on every write, and widening it over
// e.Commit's blindHotLeases (which blocks on <-t.granted) would hold the read side of
// closeMu across an unbounded wait and wedge Close's phase 1 behind a lease queue.
func (e *pebbleEngine) isClosed() bool {
	e.closeMu.RLock()
	defer e.closeMu.RUnlock()
	return e.closed
}

// defaultCloseDrain bounds how long Close waits for live readers to be released before
// it gives up and reports (see Close). Generous enough that no correct caller — one that
// closes its readers and its transactions — ever notices it; short enough that a leak is
// a report at a human timescale rather than a hang.
const defaultCloseDrain = 10 * time.Second

// Close quiesces the engine in THREE phases, and the lock is RELEASED between them.
// That structure is the fix for defect N4, and the obvious alternative is not merely
// slower — it deadlocks deterministically.
//
//  1. under closeMu.Lock(): set closed, close(e.ch), close(e.reaperStop). UNLOCK.
//  2. NO lock held: wg.Wait() for the committer + reaper, then wait for every live
//     reader to be released, bounded by `timeout`.
//  3. under closeMu.Lock(): e.db.Close().
//
// WHY THE LOCK CANNOT BE HELD ACROSS PHASE 2. Every reader-release path runs through
// code that takes closeMu: Txn.Commit → e.Commit → isClosed(). Go's RWMutex blocks new
// RLocks once a writer is waiting, so if Close held (or were waiting for) the write lock
// across the drain, an open transaction's Commit would block in isClosed(), its deferred
// tx.reader.Close() would never run, the reader would never be released, and Close would
// burn its entire timeout — every time any transaction is open, not under load. What
// keeps the line during phase 2 is the `closed` FLAG, which is already set: every pin
// site (beginSnapshot, snapshotAtChecked) checks it under a shared lock, so no NEW
// reader can appear while the drain runs. The flag is the barrier; the lock is not.
//
// WHY THE DRAIN IS BOUNDED AND WHY Close IS RETRYABLE. This used to be a sync.Once. A
// fallible drain inside a Once is unrecoverable: a drain that times out consumes the
// Once, so Close can never be retried — the directory lock is then held for the life of
// the process with no way to release it, or (if we forced db.Close() anyway) the DB is
// closed underneath readers whose very next operation panics inside pebble. So the
// choice here is explicit: on a drain timeout Close REPORTS (ErrReadersLive, naming the
// count) and leaves the engine open and closeable. The engine never closes the Pebble
// handle while a reader is pinned, which is precisely why no reader can be raced into a
// panic. Releasing the leaked reader and calling Close again completes the close.
func (e *pebbleEngine) Close() error { return e.closeWithin(defaultCloseDrain) }

func (e *pebbleEngine) closeWithin(timeout time.Duration) error {
	// ── Phase 1: seal. Refuse new work; stop the background goroutines. ──
	e.closeMu.Lock()
	if e.dbClosed {
		defer e.closeMu.Unlock()
		return e.closeErr // terminal: replay the verdict, never db.Close() twice (pebble panics)
	}
	if !e.closed {
		e.closed = true
		close(e.ch) // committer drains remaining jobs then returns
		if e.reaperStop != nil {
			close(e.reaperStop) // lease reaper exits (§6.3)
		}
	}
	e.closeMu.Unlock()

	// ── Phase 2: quiesce. NO lock held — see the doc comment. ──
	e.wg.Wait() // idempotent: returns immediately on a retry
	if live := e.reg.waitDrained(timeout); live > 0 {
		return fmt.Errorf("%w: %d reader(s) still pinned after %s — the Pebble handle is "+
			"deliberately still OPEN (closing it would panic the next operation on those "+
			"readers); release them and call Close again", ErrReadersLive, live, timeout)
	}

	// ── Phase 3: close the handle. ──
	e.closeMu.Lock()
	defer e.closeMu.Unlock()
	if e.dbClosed { // a concurrent Close won the race and already did it
		return e.closeErr
	}
	e.closeErr = e.db.Close()
	// N3 consumption point 2/5 — a fatal nobody else got to. A background flush or
	// compaction can latch one at any instant, including after the last commit acked, and
	// Close is the last moment the process can be told. Joined rather than replacing
	// db.Close's own verdict: both are real and neither subsumes the other.
	if msg, ok := e.fatal.takeFatal(); ok {
		e.closeErr = errors.Join(e.closeErr, fmt.Errorf("%w: %s", ErrPebbleFatal, msg))
	}
	e.dbClosed = true
	return e.closeErr
}
