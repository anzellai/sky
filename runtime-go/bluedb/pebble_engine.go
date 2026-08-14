package bluedb

import (
	"fmt"
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

// quietLogger routes Pebble's Logger. Pebble's Logger has THREE methods
// (Infof/Errorf/Fatalf) — the design doc's two-method assumption was wrong.
//
// Infof/Errorf are silenced (chatter). Fatalf MUST NOT be a no-op: on a WAL
// fsync failure Pebble's applyInternal (db.go:882-897, pinned v2.1.6) calls
// Logger.Fatalf(...) and then FALLS THROUGH to `return nil`. The stock logger's
// Fatalf does os.Exit(1); a no-op would make Apply(Sync) return nil for a write
// that never reached durable storage — the committer would ack Err:nil for a lost
// write, breaking the acked⇒durable contract deterministically. So Fatalf PANICS.
// The panic unwinds synchronously through Apply on the committer goroutine (the
// WAL sync is synchronous under pebble.Sync + !noSyncWait), where process()'s
// deferred recover catches it while acked==false, seals the engine, and delivers
// an ERRORED ack for the in-flight batch. A nil ack therefore always means durable.
// We panic (not os.Exit) so the fault is contained + converted to a fail-loud seal
// rather than crashing the host process.
type quietLogger struct{}

func (quietLogger) Infof(string, ...any)  {}
func (quietLogger) Errorf(string, ...any) {}
func (quietLogger) Fatalf(format string, args ...any) {
	panic(fmt.Sprintf("bluedb: pebble fatal: "+format, args...))
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

	closeMu   sync.Mutex
	closed    bool
	closeOnce sync.Once
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
	opts := &pebble.Options{
		Comparer: skydbComparer,
		Logger:   quietLogger{},
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
		db:  db,
		hlc: newHLCClock(persistedHi, cfg.wallClock),
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
	if tail, terr := (&changelog{db: db}).Tail(persistedThreshold); terr == nil {
		for _, entry := range tail {
			if chg, derr := DecodeChangelogPayload(entry.Payload); derr == nil && len(chg) > 0 {
				e.recent.append(entry.CommitTs, chg)
			}
		}
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
func (e *pebbleEngine) beginSnapshot() (*pebbleReader, error) {
	if e.isClosed() {
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

func (e *pebbleEngine) Changelog() Changelog       { return &changelog{db: e.db} }
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
	return r, nil
}

// snapshotAt pins a reader at an EXPLICIT readTs (time-travel read). Not part of the
// public Engine surface (§3.1 drops caller-supplied readTs to close the 2a TOCTOU);
// it exercises the MVCC read-resolution core (§2.5) at arbitrary versions and backs
// the versioned round-trip / snapshot-isolation tests.
func (e *pebbleEngine) snapshotAt(readTs HLC) Reader {
	return &pebbleReader{
		snap:   e.db.NewSnapshot(),
		readTs: readTs,
	}
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

func (e *pebbleEngine) isClosed() bool {
	e.closeMu.Lock()
	defer e.closeMu.Unlock()
	return e.closed
}

// Close drains the committer and closes Pebble.
func (e *pebbleEngine) Close() error {
	var err error
	e.closeOnce.Do(func() {
		e.closeMu.Lock()
		e.closed = true
		close(e.ch) // committer drains remaining jobs then returns
		if e.reaperStop != nil {
			close(e.reaperStop) // lease reaper exits (§6.3)
		}
		e.closeMu.Unlock()
		e.wg.Wait()
		err = e.db.Close()
	})
	return err
}
