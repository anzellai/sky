package bluedb

import "sync/atomic"

// Change-feed (P-R1 of the reactive scope-sync layer). Every committed mutation is
// delivered to registered subscribers so a higher layer can turn a write into a
// reactive UI update. The engine stays authoritative and NON-BLOCKING: the single
// committer never waits on a subscriber — a slow consumer drops its delta and is
// told to resync, so throughput is never held hostage.

// ChangeEvent is one committed key mutation.
type ChangeEvent struct {
	Op    uint8  // opPut | opDelete
	Key   []byte // committed key — immutable post-commit; subscribers MUST NOT mutate
	Value []byte // put value; nil for a delete
	Seq   uint64 // monotonic per-event ordinal, in commit order
}

// IsPut reports whether the event is a put (vs a delete).
func (e ChangeEvent) IsPut() bool { return e.Op == opPut }

// IsDelete reports whether the event is a delete.
func (e ChangeEvent) IsDelete() bool { return e.Op == opDelete }

type changeSub struct {
	ch       chan []ChangeEvent
	overflow uint32 // atomic: set when a batch was dropped (subscriber must resync)
}

// ChangeSub is a change-feed subscription handle. Read committed batches from C
// (each element is one group commit's mutations, in commit order).
type ChangeSub struct {
	C   <-chan []ChangeEvent
	sub *changeSub
}

// Overflowed reports (and clears) whether the feed dropped one or more batches
// since the last call because this subscriber's buffer was full. On true, the
// subscriber has missed deltas and MUST resync (re-read / re-run its queries)
// rather than trust it saw every change.
func (s *ChangeSub) Overflowed() bool {
	return atomic.SwapUint32(&s.sub.overflow, 0) == 1
}

// Subscribe registers a change-feed subscriber. Every committed batch of mutations
// is delivered as one []ChangeEvent on the returned channel, in commit order. The
// channel is buffered (buf, default 1024); if it fills because the consumer is
// slow, further batches are DROPPED (not blocked) and Overflowed() latches true so
// the consumer knows to resync — the engine's single committer is NEVER stalled by
// a subscriber. Call the returned cancel to unregister. Safe to call concurrently
// with writes and with other Subscribe/cancel calls.
func (db *DB) Subscribe(buf int) (*ChangeSub, func()) {
	if buf <= 0 {
		buf = 1024
	}
	cs := &changeSub{ch: make(chan []ChangeEvent, buf)}
	db.subMu.Lock()
	if db.subs == nil {
		db.subs = make(map[uint64]*changeSub)
	}
	id := db.subNext
	db.subNext++
	db.subs[id] = cs
	db.subMu.Unlock()

	var once uint32
	cancel := func() {
		if !atomic.CompareAndSwapUint32(&once, 0, 1) {
			return
		}
		db.subMu.Lock()
		delete(db.subs, id)
		db.subMu.Unlock()
	}
	return &ChangeSub{C: cs.ch, sub: cs}, cancel
}

// hasSubs reports whether any subscriber is registered, so the committer can skip
// building change events when nobody is listening (the common no-reactive case).
func (db *DB) hasSubs() bool {
	db.subMu.RLock()
	n := len(db.subs)
	db.subMu.RUnlock()
	return n > 0
}

// buildChanges turns a committed writes-batch into change events (commit order).
// Called on the committer goroutine after the memtable lock is released; the key/
// value byte slices are the req's own copies (cloned at enqueue) and are immutable
// once committed, so they are referenced directly (no re-copy).
func (db *DB) buildChanges(writes []*commitReq) []ChangeEvent {
	var out []ChangeEvent
	for _, r := range writes {
		switch r.op {
		case opPut:
			out = append(out, ChangeEvent{Op: opPut, Key: r.key, Value: r.value, Seq: atomic.AddUint64(&db.changeSeq, 1)})
		case opDelete:
			out = append(out, ChangeEvent{Op: opDelete, Key: r.key, Seq: atomic.AddUint64(&db.changeSeq, 1)})
		case opBatch:
			for _, m := range r.muts {
				out = append(out, ChangeEvent{Op: m.op, Key: m.key, Value: m.value, Seq: atomic.AddUint64(&db.changeSeq, 1)})
			}
		}
	}
	return out
}

// emitChanges fans a committed batch out to all subscribers, non-blocking. Runs on
// the committer goroutine AFTER db.mu is released (never under the memtable lock),
// and MUST NOT block — a full subscriber channel drops the batch and latches its
// overflow flag.
func (db *DB) emitChanges(changes []ChangeEvent) {
	db.subMu.RLock()
	for _, cs := range db.subs {
		select {
		case cs.ch <- changes:
		default:
			atomic.StoreUint32(&cs.overflow, 1)
		}
	}
	db.subMu.RUnlock()
}
