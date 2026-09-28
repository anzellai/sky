//go:build !js

package rt

// process_ring.go — the offset-addressed output ring behind Sky.Core.Process.
//
// Every byte a child writes to one stream gets an ABSOLUTE offset: the first
// byte is offset 0, the next is 1, and so on for the life of the process. The
// ring keeps the most recent `cap` bytes. A reader asks for "the bytes from
// offset N" and gets back what the ring still holds from N onward, the offset
// to ask for next, and a flag that says whether some bytes between N and the
// oldest retained byte were overwritten before it asked (it fell behind).
//
// Two properties this buys, and why they matter:
//
//   - The child is never blocked by a slow reader. The pump goroutine drains
//     the pipe into the ring as fast as the child writes; when the ring is
//     full the OLDEST bytes are overwritten. A child that fills a pipe buffer
//     and blocks on write (the classic `cmd.Output()` deadlock with a reader
//     that waits for exit first) cannot happen.
//   - A reader can resume. The offset is the whole reader state, so a Task
//     loop, a Sub that was dropped and re-added, and a Sky.Live session that
//     restarted its subscription all continue from where they were.

import "sync"

// defaultProcessRingBytes is the per-stream ring capacity when the command
// does not set one (1 MiB).
const defaultProcessRingBytes = 1 << 20

// maxProcessChunkBytes bounds one readFrom / one Output event, so one read of
// a 1 MiB backlog does not produce a single 1 MiB Msg.
const maxProcessChunkBytes = 64 << 10

type outRing struct {
	mu    sync.Mutex
	buf   []byte // grows lazily up to limit, then used circularly
	limit int
	start int64 // absolute offset of the oldest retained byte
	end   int64 // absolute offset one past the newest byte (total written)
	eof   bool  // the stream reached end of file; no byte will follow `end`
	// changed is closed and replaced on every write / eof, so any number of
	// waiters can select on it next to a stop or timeout channel.
	changed chan struct{}
}

func newOutRing(limit int) *outRing {
	if limit <= 0 {
		limit = defaultProcessRingBytes
	}
	return &outRing{limit: limit, changed: make(chan struct{})}
}

// signalLocked wakes every waiter. Caller holds r.mu.
func (r *outRing) signalLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// write appends p. Never blocks on a reader: when the ring is full the oldest
// bytes are overwritten and `start` advances.
func (r *outRing) write(p []byte) {
	if len(p) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Only the last `limit` bytes of an oversized write can survive; the rest
	// are counted (they had offsets) and dropped at once.
	if len(p) > r.limit {
		skip := len(p) - r.limit
		r.end += int64(skip)
		r.start = r.end
		p = p[skip:]
	}
	retained := r.end + int64(len(p)) - r.start
	if retained > int64(r.limit) {
		retained = int64(r.limit)
	}
	r.growLocked(int(retained))
	size := int64(len(r.buf))
	for len(p) > 0 {
		pos := r.end % size
		n := copy(r.buf[pos:], p)
		p = p[n:]
		r.end += int64(n)
	}
	if r.end-r.start > size {
		r.start = r.end - size
	}
	r.signalLocked()
}

// growLocked makes the buffer hold at least `need` bytes (need <= limit). The
// buffer grows by doubling so a process that prints a few lines does not cost
// a full megabyte; a byte at absolute offset o always lives at o % len(buf).
func (r *outRing) growLocked(need int) {
	if need <= len(r.buf) {
		return
	}
	size := len(r.buf) * 2
	if size < 4096 {
		size = 4096
	}
	for size < need {
		size *= 2
	}
	if size > r.limit {
		size = r.limit
	}
	nb := make([]byte, size)
	if old := int64(len(r.buf)); old > 0 {
		for o := r.start; o < r.end; o++ {
			nb[o%int64(size)] = r.buf[o%old]
		}
	}
	r.buf = nb
}

// closeEOF marks the stream finished. Idempotent.
func (r *outRing) closeEOF() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.eof {
		return
	}
	r.eof = true
	r.signalLocked()
}

// ringChunk is one read result.
type ringChunk struct {
	data    []byte
	from    int64 // absolute offset of data[0] (> the asked offset when dropped)
	next    int64 // the offset to pass to the next read
	dropped bool  // bytes between the asked offset and `from` were overwritten
	eof     bool  // the stream ended and `next` is its final offset
}

// readFrom returns up to max bytes starting at offset, without waiting.
//
// An offset below the oldest retained byte reads from the oldest byte and sets
// `dropped`. An offset beyond the end (a reader that invented one) reads from
// the end: there is nothing to return, and next is the current end. A
// negative offset reads from the oldest retained byte without `dropped` — it
// is how a reader says "whatever you still have".
func (r *outRing) readFrom(offset int64, max int) ringChunk {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.readLocked(offset, max)
}

func (r *outRing) readLocked(offset int64, max int) ringChunk {
	if max <= 0 {
		max = maxProcessChunkBytes
	}
	c := ringChunk{}
	from := offset
	if from < 0 {
		from = r.start
	} else if from < r.start {
		from = r.start
		c.dropped = true
	}
	if from > r.end {
		from = r.end
	}
	n := r.end - from
	if n > int64(max) {
		n = int64(max)
	}
	if n > 0 {
		c.data = make([]byte, n)
		size := int64(len(r.buf))
		for i := int64(0); i < n; {
			pos := (from + i) % size
			k := copy(c.data[i:], r.buf[pos:])
			i += int64(k)
		}
	}
	c.from = from
	c.next = from + n
	c.eof = r.eof && c.next == r.end
	return c
}

// wait returns a channel that is closed when the ring changes after this call,
// plus a snapshot of whether data beyond offset (or eof) is already available.
func (r *outRing) wait(offset int64) (ready bool, changed <-chan struct{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if offset < r.start || offset < r.end || r.eof {
		return true, r.changed
	}
	return false, r.changed
}

// snapshot reports the ring's [start, end) and eof, for tests and readers.
func (r *outRing) snapshot() (start, end int64, eof bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.start, r.end, r.eof
}
