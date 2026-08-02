package bluedb

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// ErrClosed is returned by writes after Close.
var ErrClosed = errors.New("bluedb: closed")

const (
	chanBuf  = 4096 // commit queue depth
	maxBatch = 1024 // max records fsync'd in one group commit
)

// Options configure a DB.
type Options struct {
	// Sync (default true) fsyncs each group commit before acking writers —
	// survives power loss. Set false for the relaxed tier (survives a process
	// crash but not power loss); see docs/bluedb/durability.md § "knob".
	Sync bool
}

// DB is the embedded BlueDB engine: a durable, group-committed key-value store
// with an in-memory (memtable) working set. Point reads are served from memory;
// writes go through the write-ahead log with group commit, then apply to the
// memtable. All methods are safe for concurrent use.
type DB struct {
	path string

	f *os.File // WAL, opened append-only

	mu  sync.RWMutex      // guards mem
	mem map[string][]byte // the working set (point keyspace)

	cmu    sync.RWMutex // coordinates writers vs Close on the commit channel
	closed bool

	ch   chan *commitReq
	wg   sync.WaitGroup
	sync bool

	seq     uint64 // atomic: monotonic record seq
	batches uint64 // atomic: group-commit fsyncs performed
	writes  uint64 // atomic: individual writes committed
}

type commitReq struct {
	rec  []byte
	ent  entry
	done chan error
}

// Open opens (creating if needed) the BlueDB store at path, replaying the WAL to
// recover the last durable state and truncating any torn tail from a prior
// crash.
func Open(path string, opts ...Options) (*DB, error) {
	sync := true
	if len(opts) > 0 {
		sync = opts[0].Sync
	}
	db := &DB{
		path: path,
		mem:  make(map[string][]byte),
		ch:   make(chan *commitReq, chanBuf),
		sync: sync,
	}

	// Recover: replay the WAL into the memtable, find the last valid offset.
	maxSeq, validEnd, err := replay(path, func(e entry) { applyEntry(db.mem, e) })
	if err != nil {
		return nil, err
	}
	db.seq = maxSeq

	// Drop any torn trailing record so appends start on a clean boundary.
	if fi, e := os.Stat(path); e == nil && fi.Size() > validEnd {
		if e := os.Truncate(path, validEnd); e != nil {
			return nil, e
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	db.f = f
	syncDir(filepath.Dir(path)) // make the file's directory entry durable

	db.wg.Add(1)
	go db.committer()
	return db, nil
}

// Get returns the value for key and whether it is present. The returned slice is
// owned by the DB and must not be mutated by the caller.
func (db *DB) Get(key []byte) ([]byte, bool) {
	db.mu.RLock()
	v, ok := db.mem[string(key)]
	db.mu.RUnlock()
	return v, ok
}

// Put durably stores value under key. It returns only after the write is on
// stable storage (fsync'd, in Sync mode).
func (db *DB) Put(key, value []byte) error { return db.write(opPut, key, value) }

// Delete durably removes key.
func (db *DB) Delete(key []byte) error { return db.write(opDelete, key, nil) }

func (db *DB) write(op uint8, key, value []byte) error {
	db.cmu.RLock()
	if db.closed {
		db.cmu.RUnlock()
		return ErrClosed
	}
	e := entry{
		seq:   atomic.AddUint64(&db.seq, 1),
		op:    op,
		key:   append([]byte(nil), key...),
		value: append([]byte(nil), value...),
	}
	req := &commitReq{rec: encodeRecord(e), ent: e, done: make(chan error, 1)}
	db.ch <- req // committer is alive until Close drains the channel
	db.cmu.RUnlock()
	return <-req.done
}

// committer is the single writer goroutine. It group-commits: it grabs the first
// queued request, drains whatever else is queued (up to maxBatch), writes them
// all, fsyncs ONCE, applies them to the memtable, and acks the whole batch.
// Under load the batch grows, so a higher write rate rides the same fsync.
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
		db.commit(batch)
	}
}

func (db *DB) commit(batch []*commitReq) {
	var werr error
	for _, r := range batch {
		if _, e := db.f.Write(r.rec); e != nil {
			werr = e
			break
		}
	}
	if werr == nil && db.sync {
		werr = db.f.Sync() // the one durability fsync for the batch
	}
	if werr == nil {
		db.mu.Lock()
		for _, r := range batch {
			applyEntry(db.mem, r.ent)
		}
		db.mu.Unlock()
	}
	atomic.AddUint64(&db.batches, 1)
	atomic.AddUint64(&db.writes, uint64(len(batch)))
	for _, r := range batch {
		r.done <- werr
	}
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

// Stats reports group-commit behaviour. batches is the number of fsyncs;
// writes is the number of individual mutations committed. writes/batches is the
// average group-commit batch size — the higher it is under load, the better the
// fsync amortization.
func (db *DB) Stats() (batches, writes uint64) {
	return atomic.LoadUint64(&db.batches), atomic.LoadUint64(&db.writes)
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
