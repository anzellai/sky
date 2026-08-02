package bluedb

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// ErrClosed is returned by writes after Close.
var ErrClosed = errors.New("bluedb: closed")

// ErrFailed is returned once the engine has hit an unrecoverable write error it
// could not roll back — it refuses further writes rather than risk a torn WAL.
var ErrFailed = errors.New("bluedb: engine failed (unrecoverable write error)")

const (
	chanBuf  = 4096 // commit queue depth
	maxBatch = 1024 // max records fsync'd in one group commit
)

// opCheckpoint is an internal marker request routed through the commit channel
// so a checkpoint runs in the single committer goroutine, between batches — it is
// never encoded to the WAL.
const opCheckpoint uint8 = 3

// walFile is the WAL's file handle, an interface so tests can inject write
// faults (*os.File satisfies it in production).
type walFile interface {
	Write([]byte) (int, error)
	Sync() error
	Truncate(int64) error
	Close() error
}

// Options configure a DB.
type Options struct {
	// Sync (default true) fsyncs each group commit before acking writers —
	// survives power loss. Set false for the relaxed tier (survives a process
	// crash but not power loss); see docs/bluedb/durability.md § "knob".
	Sync bool
	// CheckpointEvery, if > 0, auto-checkpoints (snapshot + WAL truncation) after
	// roughly this many writes, bounding WAL replay time and disk growth. 0 =
	// checkpoint only on explicit Checkpoint().
	CheckpointEvery int

	// walWrap, test-only, wraps the WAL file to inject faults.
	walWrap func(walFile) walFile
}

// DB is the embedded BlueDB engine: a durable, group-committed key-value store
// with an in-memory (memtable) working set and periodic snapshots. Point reads
// are served from memory; writes go through the write-ahead log with group
// commit, then apply to the memtable. All methods are safe for concurrent use.
type DB struct {
	walPath  string
	snapPath string

	f walFile // WAL, opened append-only

	mu  sync.RWMutex      // guards mem
	mem map[string][]byte // the working set (point keyspace)

	cmu    sync.RWMutex // coordinates writers vs Close on the commit channel
	closed bool
	failed atomic.Bool // set on an unrecoverable write error

	ch   chan *commitReq
	wg   sync.WaitGroup
	sync bool

	// committer-owned (single goroutine; no locking needed):
	seq             uint64 // monotonic record seq, in commit order
	walSize         int64  // current WAL byte length (for batch rollback)
	writesSinceCkpt int
	checkpointEvery int

	batches     uint64 // atomic: group-commit fsyncs
	writes      uint64 // atomic: individual mutations committed
	checkpoints uint64 // atomic: snapshots taken
}

type commitReq struct {
	op    uint8
	key   []byte
	value []byte
	done  chan error
}

// Open opens (creating if needed) the BlueDB store at path, loading the latest
// snapshot and replaying the WAL tail after it, and truncating any torn tail
// from a prior crash.
func Open(path string, opts ...Options) (*DB, error) {
	var o Options
	o.Sync = true
	if len(opts) > 0 {
		o = opts[0]
	}
	db := &DB{
		walPath:         path,
		snapPath:        path + ".snap",
		ch:              make(chan *commitReq, chanBuf),
		sync:            o.Sync,
		checkpointEvery: o.CheckpointEvery,
	}

	// Recover: snapshot first, then the WAL tail after the snapshot's seq.
	mem, coveredSeq, err := loadSnapshot(db.snapPath)
	if err != nil {
		return nil, err
	}
	if mem == nil {
		mem = make(map[string][]byte)
	}
	db.mem = mem

	maxSeq, validEnd, err := replay(path, coveredSeq, func(e entry) { applyEntry(db.mem, e) })
	if err != nil {
		return nil, err
	}
	db.seq = maxSeq

	// Drop any torn trailing record so appends start on a clean boundary.
	truncated := false
	if fi, e := os.Stat(path); e == nil && fi.Size() > validEnd {
		if e := os.Truncate(path, validEnd); e != nil {
			return nil, e
		}
		truncated = true
	}
	db.walSize = validEnd

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	var wf walFile = f
	if o.walWrap != nil {
		wf = o.walWrap(wf)
	}
	db.f = wf
	// F5: make the torn-tail truncation durable so it can't reappear next crash.
	if truncated {
		_ = db.f.Sync()
	}
	syncDir(filepath.Dir(path))

	db.wg.Add(1)
	go db.committer()
	return db, nil
}

// Get returns the value for key and whether it is present. The returned slice is
// owned by the DB and must not be mutated by the caller (no-copy for read speed;
// see docs/bluedb/capacity.md).
func (db *DB) Get(key []byte) ([]byte, bool) {
	db.mu.RLock()
	v, ok := db.mem[string(key)]
	db.mu.RUnlock()
	return v, ok
}

// Put durably stores value under key. It returns only after the write is on
// stable storage (fsync'd, in Sync mode).
func (db *DB) Put(key, value []byte) error { return db.enqueue(opPut, key, value) }

// Delete durably removes key.
func (db *DB) Delete(key []byte) error { return db.enqueue(opDelete, key, nil) }

// Checkpoint forces a snapshot + WAL truncation now and returns when durable.
func (db *DB) Checkpoint() error { return db.enqueue(opCheckpoint, nil, nil) }

func (db *DB) enqueue(op uint8, key, value []byte) error {
	db.cmu.RLock()
	if db.closed {
		db.cmu.RUnlock()
		return ErrClosed
	}
	if db.failed.Load() {
		db.cmu.RUnlock()
		return ErrFailed
	}
	req := &commitReq{
		op:    op,
		key:   append([]byte(nil), key...),
		value: append([]byte(nil), value...),
		done:  make(chan error, 1),
	}
	db.ch <- req // committer is alive until Close drains the channel
	db.cmu.RUnlock()
	return <-req.done
}

// committer is the single writer goroutine. It group-commits: grabs the first
// queued request, drains whatever else is queued (up to maxBatch), and processes
// the batch. Assigning seq here (not in the caller) makes seq monotonic in
// commit order, which the snapshot's coveredSeq relies on.
func (db *DB) committer() {
	defer db.wg.Done()
	for first := range db.ch {
		batch := []*commitReq{first}
	drain:
		for len(batch) < maxBatch {
			select {
			case r, ok := <-db.ch:
				if !ok {
					break drain // channel closed & drained
				}
				batch = append(batch, r)
			default:
				break drain
			}
		}
		db.process(batch)
	}
}

func (db *DB) process(batch []*commitReq) {
	writes := batch[:0:0]
	var ckpts []*commitReq
	for _, r := range batch {
		if r.op == opCheckpoint {
			ckpts = append(ckpts, r)
		} else {
			writes = append(writes, r)
		}
	}

	var werr error
	if len(writes) > 0 {
		start := db.walSize // rollback point for the whole batch
		var written int64
		for _, r := range writes {
			db.seq++
			rec := encodeRecord(entry{seq: db.seq, op: r.op, key: r.key, value: r.value})
			n, e := db.f.Write(rec)
			written += int64(n)
			if e != nil {
				werr = e
				break
			}
		}
		if werr == nil && db.sync {
			werr = db.f.Sync() // the one durability fsync for the batch
		}

		if werr == nil {
			// Success: commit is durable — advance the WAL length, apply, ack.
			db.walSize = start + written
			db.mu.Lock()
			for _, r := range writes {
				applyEntry(db.mem, entry{op: r.op, key: r.key, value: r.value})
			}
			db.mu.Unlock()
			db.writesSinceCkpt += len(writes)
		} else {
			// F1: a write (or its fsync) failed mid-batch. The WAL may now hold a
			// partial/torn record followed by nothing — but if we kept appending,
			// a later good record would sit BEHIND a torn one and recovery (which
			// stops at the first torn record) would silently drop it. Roll the
			// batch back to `start` so the WAL never contains a torn record
			// followed by good ones. Nothing in this batch is applied → the whole
			// batch reports failure (all-or-nothing).
			if terr := db.f.Truncate(start); terr != nil {
				// Can't roll back → refuse all further writes rather than risk a
				// torn WAL. (walSize stays; recovery will stop at the torn record,
				// dropping only this failed batch.)
				db.failed.Store(true)
				log.Printf("[bluedb] write failed and rollback failed (%v / %v) — engine sealed", werr, terr)
			} else {
				if db.sync {
					_ = db.f.Sync()
				}
				// walSize unchanged (== start); seq has gaps, which is harmless.
			}
		}

		atomic.AddUint64(&db.batches, 1)
		if werr == nil {
			atomic.AddUint64(&db.writes, uint64(len(writes)))
		}
		for _, r := range writes {
			r.done <- werr
		}
	}

	if werr != nil {
		for _, r := range ckpts {
			r.done <- werr
		}
		return
	}

	forced := len(ckpts) > 0
	auto := db.checkpointEvery > 0 && db.writesSinceCkpt >= db.checkpointEvery
	if forced || auto {
		cerr := db.doCheckpoint()
		if cerr != nil {
			// F2: never swallow a checkpoint failure — the WAL keeps growing until
			// it clears, so the operator must see it.
			log.Printf("[bluedb] checkpoint failed: %v (WAL will keep growing until this clears)", cerr)
		}
		for _, r := range ckpts {
			r.done <- cerr
		}
	} else {
		for _, r := range ckpts {
			r.done <- nil
		}
	}
}

// doCheckpoint materializes the memtable at the current committed seq into a
// durable snapshot, then truncates the WAL. Runs only in the committer, so seq
// and the memtable are stable. Correct across a crash between the snapshot
// install and the truncate: recovery skips WAL records with seq <= coveredSeq.
func (db *DB) doCheckpoint() error {
	db.mu.RLock()
	seq := db.seq
	snap := make(map[string][]byte, len(db.mem))
	for k, v := range db.mem {
		snap[k] = v
	}
	db.mu.RUnlock()

	if err := writeSnapshotAtomic(db.snapPath, seq, snap); err != nil {
		return err
	}
	if err := db.f.Truncate(0); err != nil {
		return err
	}
	if db.sync {
		_ = db.f.Sync()
	}
	db.walSize = 0
	db.writesSinceCkpt = 0
	atomic.AddUint64(&db.checkpoints, 1)
	return nil
}

// Close flushes in-flight writes, stops the committer, and closes the WAL.
func (db *DB) Close() error {
	db.cmu.Lock()
	if db.closed {
		db.cmu.Unlock()
		return nil
	}
	db.closed = true
	close(db.ch) // no more sends can happen (guarded by cmu); committer drains
	db.cmu.Unlock()

	db.wg.Wait() // committer finishes the queued batches + acks them
	return db.f.Close()
}

// Len returns the number of live keys.
func (db *DB) Len() int {
	db.mu.RLock()
	n := len(db.mem)
	db.mu.RUnlock()
	return n
}

// ForEach calls fn for every live key/value, in unspecified order, holding a read
// lock for the whole scan (writes block meanwhile — keep fn fast). Returning
// false stops iteration. The slices are owned by the DB; do not retain or mutate.
func (db *DB) ForEach(fn func(key, value []byte) bool) {
	db.mu.RLock()
	defer db.mu.RUnlock()
	for k, v := range db.mem {
		if !fn([]byte(k), v) {
			return
		}
	}
}

// Stats reports group-commit + checkpoint behaviour. writes/batches is the mean
// group-commit batch size (higher under load = better fsync amortization).
func (db *DB) Stats() (batches, writes, checkpoints uint64) {
	return atomic.LoadUint64(&db.batches),
		atomic.LoadUint64(&db.writes),
		atomic.LoadUint64(&db.checkpoints)
}

func applyEntry(mem map[string][]byte, e entry) {
	if e.op == opDelete {
		delete(mem, string(e.key))
		return
	}
	mem[string(e.key)] = e.value
}

// syncDir fsyncs a directory so a newly created file's entry is durable. Best
// effort — not all platforms/filesystems support it.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
