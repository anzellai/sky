package bluedb

import (
	"sync"
	"sync/atomic"

	"github.com/cockroachdb/pebble/v2"
	"github.com/cockroachdb/pebble/v2/vfs"
)

// maxBatch caps a single group-commit drain window (§3.2). Mirrors the old
// hand-built committer's cap.
const maxBatch = 1024

// quietLogger silences Pebble's stderr chatter. Pebble's Logger has THREE methods
// (Infof/Errorf/Fatalf) — the design doc's two-method assumption was wrong.
type quietLogger struct{}

func (quietLogger) Infof(string, ...any)  {}
func (quietLogger) Errorf(string, ...any) {}
func (quietLogger) Fatalf(string, ...any) {}

// config carries Open parameters, including the test seams (injectable clock + FS).
type config struct {
	dir       string
	fs        vfs.FS          // nil ⇒ disk default
	wallClock wallClockMillis // nil ⇒ system clock
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

	closeMu   sync.Mutex
	closed    bool
	closeOnce sync.Once
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
		ch:  make(chan *commitJob),
	}
	e.reg = newWatermarkRegistry(e.hlc.highWater, persistedThreshold)

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
