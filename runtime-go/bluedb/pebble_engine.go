package bluedb

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// maxBatch caps a single group-commit drain window (§3.2). Mirrors the old
// hand-built committer's cap.
const maxBatch = 1024

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

	e.wg.Add(1)
	go e.committer()
	return e, nil
}

// readMetaHLC reads an HLC-valued metadata key; returns {0,0} if absent.
func readMetaHLC(db *pebble.DB, name string) (HLC, error) {
	v, closer, err := db.Get(encodeMetaKey(name))
	if err == pebble.ErrNotFound {
		return HLC{}, nil
	}
	if err != nil {
		return HLC{}, err
	}
	defer closer.Close()
	if len(v) < hlcEncodedLen {
		return HLC{}, nil
	}
	return decodeHLC(v), nil
}

func (e *pebbleEngine) NowTs() HLC { return e.hlc.highWater() }

func (e *pebbleEngine) Changelog() Changelog       { return &changelog{db: e.db} }
func (e *pebbleEngine) Readers() WatermarkRegistry { return e.reg }

// Snapshot atomically registers a reader token + picks readTs, then pins a Pebble
// snapshot seqnum for a frozen, lock-free consistent view (§2.5 invariant, C4).
func (e *pebbleEngine) Snapshot() (Reader, error) {
	if e.isClosed() {
		return nil, ErrClosed
	}
	tok, readTs, err := e.reg.Register()
	if err != nil {
		return nil, err
	}
	snap := e.db.NewSnapshot()
	return &pebbleReader{
		snap:   snap,
		readTs: readTs,
		tok:    tok,
		reg:    e.reg,
	}, nil
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
func (e *pebbleEngine) Commit(req CommitReq) CommitResult {
	if e.sealed.Load() {
		return CommitResult{Err: ErrSealed}
	}
	if e.isClosed() {
		return CommitResult{Err: ErrClosed}
	}
	job := &commitJob{req: req, done: make(chan CommitResult, 1)}
	defer func() {
		// A send on a closed channel panics if Close raced us; recover to ErrClosed.
		if r := recover(); r != nil {
			job.done <- CommitResult{Err: ErrClosed}
		}
	}()
	e.ch <- job
	return <-job.done
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
		e.closeMu.Unlock()
		e.wg.Wait()
		err = e.db.Close()
	})
	return err
}
