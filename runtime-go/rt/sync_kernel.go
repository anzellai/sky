package rt

import (
	"fmt"
	"sync"
	"time"
)

// sync_kernel.go — Std.Sync: shared mutable state for Task programs.
//
// Pure Sky code needs none of this: a value never changes, so nothing races.
// These are for Task programs and servers where several Tasks run at once
// (`Task.parallel`, `Task.spawn`, concurrent HTTP handlers) and must share a
// counter, a cache or a work queue:
//
//	Ref a    — one mutable cell: get / set / update / compareAndSwap
//	Mutex    — withLock m task runs `task` with `m` held
//	Queue a  — a bounded FIFO channel: push (blocks while full), pop /
//	           popWithin (block until an item, or a timeout), close
//
// Every operation is a Task (a thunk): nothing happens until the Task runs.
//
// Representation. Each value is a Sky ADT (`Ref__Internal`,
// `Mutex__Internal`, `Queue__Internal`) whose one field holds the Go pointer,
// the way `Decimal__Internal` holds a decimal. Sky code never matches on it
// (the constructors are not exported), equality is identity (two refs are
// equal only when they are the same ref), and the value is freed by the Go
// collector when nothing refers to it. It is process-local: it does not
// survive a restart and must not be stored in a Sky.Live model (the session
// store cannot serialise it).

type syncRef struct {
	mu sync.Mutex
	v  any
}

func (*syncRef) String() string { return "<Ref>" }

type syncMutex struct{ mu sync.Mutex }

func (*syncMutex) String() string { return "<Mutex>" }

// syncQueue is a bounded FIFO. `changed` is closed and replaced on every
// state change (push, pop, close), so a waiter takes the current channel
// under the lock, releases the lock, and wakes when anything moved — with a
// timer beside it for popWithin. No waiter can miss a wake-up: the channel it
// waits on was taken while the state it checked was current.
type syncQueue struct {
	mu      sync.Mutex
	items   []any
	cap     int
	closed  bool
	changed chan struct{}
}

func (*syncQueue) String() string { return "<Queue>" }

func (q *syncQueue) signalLocked() {
	close(q.changed)
	q.changed = make(chan struct{})
}

func syncBox(name string, p any) SkyADT {
	return SkyADT{Tag: 0, SkyName: name, Fields: []any{p}}
}

// syncUnbox returns the pointer a Sync value carries, or an InvalidInput
// Error for a value of another shape (never a panic).
func syncUnbox[T any](v any, name string) (*T, any) {
	if adt, ok := v.(SkyADT); ok && adt.SkyName == name && len(adt.Fields) == 1 {
		if p, ok := adt.Fields[0].(*T); ok && p != nil {
			return p, nil
		}
	}
	return nil, ErrInvalidInput(fmt.Sprintf("Std.Sync: expected a %s, got %T", name, v))
}

// ── Ref ─────────────────────────────────────────────────────────────

// Sync_newRef : a -> Task Error (Ref a)
func Sync_newRef(initial any) any {
	return func() any {
		return Ok[any, any](syncBox("Ref__Internal", &syncRef{v: initial}))
	}
}

// Sync_get : Ref a -> Task Error a
func Sync_get(refArg any) any {
	return func() any {
		r, e := syncUnbox[syncRef](refArg, "Ref__Internal")
		if e != nil {
			return Err[any, any](e)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		return Ok[any, any](r.v)
	}
}

// Sync_set : a -> Ref a -> Task Error ()
func Sync_set(v, refArg any) any {
	return func() any {
		r, e := syncUnbox[syncRef](refArg, "Ref__Internal")
		if e != nil {
			return Err[any, any](e)
		}
		r.mu.Lock()
		r.v = v
		r.mu.Unlock()
		return Ok[any, any](struct{}{})
	}
}

// Sync_update : (a -> a) -> Ref a -> Task Error a
//
// Applies `f` to the current value and stores the result, atomically: no
// other get / set / update / compareAndSwap on the ref sees a state between
// the read and the write. Returns the new value. `f` is pure Sky, so holding
// the lock while it runs cannot deadlock on another Sync value.
func Sync_update(f, refArg any) any {
	return func() any {
		r, e := syncUnbox[syncRef](refArg, "Ref__Internal")
		if e != nil {
			return Err[any, any](e)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		nv := SkyCall(f, r.v)
		r.v = nv
		return Ok[any, any](nv)
	}
}

// Sync_compareAndSwap : a -> a -> Ref a -> Task Error Bool
//
// `compareAndSwap expected new ref` stores `new` only when the current value
// equals `expected` (Sky `==`), and reports whether it did.
func Sync_compareAndSwap(expected, nv, refArg any) any {
	return func() any {
		r, e := syncUnbox[syncRef](refArg, "Ref__Internal")
		if e != nil {
			return Err[any, any](e)
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if !deepEq(r.v, expected) {
			return Ok[any, any](false)
		}
		r.v = nv
		return Ok[any, any](true)
	}
}

// ── Mutex ───────────────────────────────────────────────────────────

// Sync_newMutex : () -> Task Error Mutex
func Sync_newMutex(_ any) any {
	return func() any {
		return Ok[any, any](syncBox("Mutex__Internal", &syncMutex{}))
	}
}

// Sync_withLock : Mutex -> Task e a -> Task e a
//
// Runs `task` with the mutex held and releases it however the task ends (Ok,
// Err, or a recovered panic). Not re-entrant: a `withLock m` inside a
// `withLock m` on the same mutex waits for itself.
func Sync_withLock(mArg, task any) any {
	return func() any {
		m, e := syncUnbox[syncMutex](mArg, "Mutex__Internal")
		if e != nil {
			return Err[any, any](e)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		return forceTask(task)
	}
}

// ── Queue ───────────────────────────────────────────────────────────

// Sync_newQueue : Int -> Task Error (Queue a)
func Sync_newQueue(capArg any) any {
	return func() any {
		c := AsInt(capArg)
		if c < 1 {
			return Err[any, any](ErrInvalidInput(fmt.Sprintf(
				"Sync.newQueue: capacity %d, want 1 or more", c)))
		}
		return Ok[any, any](syncBox("Queue__Internal",
			&syncQueue{cap: c, changed: make(chan struct{})}))
	}
}

// Sync_push : a -> Queue a -> Task Error ()
//
// Appends `v`, waiting while the queue is full. `Err Unavailable` when the
// queue is closed (also when it closes while this push waits).
func Sync_push(v, qArg any) any {
	return func() any {
		q, e := syncUnbox[syncQueue](qArg, "Queue__Internal")
		if e != nil {
			return Err[any, any](e)
		}
		for {
			q.mu.Lock()
			if q.closed {
				q.mu.Unlock()
				return Err[any, any](ErrUnavailable("Sync.push: the queue is closed"))
			}
			if len(q.items) < q.cap {
				q.items = append(q.items, v)
				q.signalLocked()
				q.mu.Unlock()
				return Ok[any, any](struct{}{})
			}
			wait := q.changed
			q.mu.Unlock()
			<-wait
		}
	}
}

// syncPop takes the oldest item. timeout < 0 waits without limit.
func syncPop(qArg any, timeout time.Duration, op string) any {
	q, e := syncUnbox[syncQueue](qArg, "Queue__Internal")
	if e != nil {
		return Err[any, any](e)
	}
	var deadline <-chan time.Time
	if timeout >= 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		deadline = t.C
	}
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			v := q.items[0]
			q.items[0] = nil
			q.items = q.items[1:]
			q.signalLocked()
			q.mu.Unlock()
			return Ok[any, any](Just[any](v))
		}
		if q.closed {
			q.mu.Unlock()
			return Ok[any, any](Nothing[any]())
		}
		wait := q.changed
		q.mu.Unlock()
		select {
		case <-wait:
		case <-deadline:
			return Err[any, any](makeError(4, "Timeout",
				fmt.Sprintf("%s: nothing arrived within %d ms", op, timeout.Milliseconds())))
		}
	}
}

// Sync_pop : Queue a -> Task Error (Maybe a)
//
// The oldest item, waiting until one arrives: `Just item`, or `Nothing`
// once the queue is closed and empty.
func Sync_pop(qArg any) any {
	return func() any { return syncPop(qArg, -1, "Sync.pop") }
}

// Sync_popWithin : Int -> Queue a -> Task Error (Maybe a)
//
// `pop`, waiting at most `ms` milliseconds: `Err Timeout` when nothing
// arrived in time.
func Sync_popWithin(msArg, qArg any) any {
	return func() any {
		ms := AsInt(msArg)
		if ms < 0 {
			ms = 0
		}
		return syncPop(qArg, time.Duration(ms)*time.Millisecond, "Sync.popWithin")
	}
}

// Sync_close : Queue a -> Task Error ()   (idempotent)
func Sync_close(qArg any) any {
	return func() any {
		q, e := syncUnbox[syncQueue](qArg, "Queue__Internal")
		if e != nil {
			return Err[any, any](e)
		}
		q.mu.Lock()
		if !q.closed {
			q.closed = true
			q.signalLocked()
		}
		q.mu.Unlock()
		return Ok[any, any](struct{}{})
	}
}

// Sync_size : Queue a -> Task Error Int   (items waiting now)
func Sync_size(qArg any) any {
	return func() any {
		q, e := syncUnbox[syncQueue](qArg, "Queue__Internal")
		if e != nil {
			return Err[any, any](e)
		}
		q.mu.Lock()
		defer q.mu.Unlock()
		return Ok[any, any](len(q.items))
	}
}
