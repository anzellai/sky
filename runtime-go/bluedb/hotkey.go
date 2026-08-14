package bluedb

import (
	"bytes"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// hotkey.go — Phase 2b hot-key pessimistic fallback (§6). Two cooperating pieces:
//
//   - hotKeyTable: the committer observes validation aborts (recordAbort) and promotes a
//     genuinely-contended POINT key to "hot". The driver asks anyHot/hotSubset to decide
//     whether to switch a starving optimistic txn to the strict-2PL lease path (§6.2/§6.3).
//   - leaseManager: a per-hot-key FIFO lease queue. A holder is the sole active writer of
//     the key it holds, so its Phase-C commit cannot lose the validation race on that point
//     key (§6.3). Deadlock-free because Transact acquires a txn's WHOLE hot-key set up front
//     in ascending bytes.Compare order (strict-2PL — the whole-set canonical-order acquire is
//     what the grill rework requires; NEVER acquire-on-discovery, §6.3/§6.4). Release is
//     DRIVER-side (defer releaseAll); a dedicated lease-reaper goroutine (pebble_engine.go
//     `go e.leaseReaper()` — NOT the committer) is the timeout backstop for a driver that
//     crashes between Commit-return and its release defer (§6.3).
//
// Both structs are guarded by their own mutex. The design's "single-writer, no lock" holds
// for the ring (recent_changes.go) but NOT here: recordAbort runs on the committer goroutine
// while anyHot/hotSubset/acquire run on the driver goroutine, so these maps are genuinely
// cross-goroutine and take a lock. The lock is off the blind-write hot path entirely — it is
// touched only by transactional aborts + the lease path, never by e.Commit / processBlindPhase1.

// hotThreshold is the recent-abort count at which a POINT key is promoted to hot. Low so a
// genuinely contended key switches to the lease quickly; decays back below it when contention
// ends (see decay()).
const hotThreshold = 2

// defaultLeaseTimeout is the lease-reaper goroutine's reclaim age for a held lease (§6.3). Set
// well above a normal commit latency so it never expires a merely-slow legitimate holder and
// reintroduces the race; it fires only for a driver that crashed holding the lease. Tests
// inject a short timeout via config.leaseTimeout.
const defaultLeaseTimeout = 5 * time.Second

// hotKeyTable tracks per-point-key recent abort counts and the hot set (§6.2).
type hotKeyTable struct {
	mu     sync.Mutex
	aborts map[string]int
	hot    map[string]bool
	leases *leaseManager // consulted for stickiness: a key stays hot while its lease is contended

	// hotN mirrors len(hot) as a lock-free atomic (Fix-3). The blind-write path reads it WITHOUT
	// taking mu: when hotN == 0 AND leases.waiterN == 0 nothing is hot anywhere, so the OLTP
	// firehose skips the whole hot-key check with a single pair of atomic loads (zero added lock
	// cost — the ~49k/s blind path is preserved). Maintained in lockstep with `hot` under mu
	// (increment on a false→true promotion in recordAbort, decrement on retirement in decay). A
	// momentarily-stale read is benign: it is a LIVENESS gate, not a correctness one — validation
	// still prevents every lost update regardless of which path a blind write takes.
	hotN atomic.Int64
}

func newHotKeyTable(leases *leaseManager) *hotKeyTable {
	return &hotKeyTable{
		aborts: make(map[string]int),
		hot:    make(map[string]bool),
		leases: leases,
	}
}

// recordAbort is called by the committer (process/processTxn) when validate() reports a POINT
// culprit (§4.3). Only point culprits reach here — a range/predicate conflict has no single
// key to lease (§6.2/§6.4), so the committer gates the call on validate's pointConflict flag.
// Committer-goroutine caller, but locked because the driver reads these maps concurrently.
func (h *hotKeyTable) recordAbort(culprit []byte) {
	if len(culprit) == 0 {
		return
	}
	k := string(culprit)
	h.mu.Lock()
	h.aborts[k]++
	if h.aborts[k] >= hotThreshold && !h.hot[k] { // false→true promotion only → keep hotN == len(hot)
		h.hot[k] = true
		h.hotN.Add(1)
	}
	h.mu.Unlock()
}

// isHot reports whether a key is currently on the lease path. Sticky: a promoted key stays hot
// while its lease queue is contended (holders/waiters present), so leasing — which STOPS the
// aborts that promoted it — cannot let the key cool mid-contention and oscillate back to the
// optimistic path (§6.3 auto-retirement is driven by decay() only once the queue drains).
func (h *hotKeyTable) isHot(userKey []byte) bool {
	k := string(userKey)
	h.mu.Lock()
	hot := h.hot[k]
	h.mu.Unlock()
	if hot {
		return true
	}
	return h.leases.hasWaiters(k)
}

// anyHot reports whether ANY of a txn's touched point keys is hot — the driver's signal to
// switch from optimistic retry to the strict-2PL lease path (§5.1/§6.2).
func (h *hotKeyTable) anyHot(touched [][]byte) bool {
	for _, k := range touched {
		if h.isHot(k) {
			return true
		}
	}
	return false
}

// hotSubset returns the touched keys that are currently hot, in ascending bytes.Compare order
// — the canonical acquisition order that makes strict-2PL deadlock-free (§6.3/§6.4). Deduped.
func (h *hotKeyTable) hotSubset(touched [][]byte) [][]byte {
	seen := make(map[string]bool, len(touched))
	var out [][]byte
	for _, k := range touched {
		if h.isHot(k) {
			s := string(k)
			if !seen[s] {
				seen[s] = true
				out = append(out, append([]byte(nil), k...))
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i], out[j]) < 0 })
	return out
}

// decay is called periodically by the reaper. A hot key whose lease queue has fully drained
// (contention ended) has its abort count decremented; once it falls below the threshold the key
// is retired from the hot set and returns to the optimistic fast path (§6.3 auto-retirement).
func (h *hotKeyTable) decay() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k := range h.hot {
		if h.leases.hasWaiters(k) {
			continue // still contended → keep hot (stickiness)
		}
		if h.aborts[k] > 0 {
			h.aborts[k]--
		}
		if h.aborts[k] < hotThreshold {
			delete(h.hot, k)
			delete(h.aborts, k)
			h.hotN.Add(-1) // retire → keep hotN == len(hot)
		}
	}
}

// ── FIFO lease manager (§6.3) ──────────────────────────────────────────────────────────────

// leaseTicket is one queued/held lease request for a single hot key.
type leaseTicket struct {
	key       string
	granted   chan struct{} // closed when this ticket becomes the head (the active holder)
	grantedAt time.Time     // when it was granted — drives the reaper's staleness check
	active    bool          // currently the head/holder
	done      bool          // released (driver) or reclaimed (reaper) — release is then a no-op
}

// leaseQueue is the FIFO waiter list for one hot key. queue[0] is the active holder once granted.
type leaseQueue struct {
	waiters []*leaseTicket
}

// leaseManager arbitrates per-hot-key FIFO leases (§6.3). Driver goroutines acquire/release;
// a dedicated lease-reaper goroutine (NOT the committer) reclaims a stale head. All under one
// mutex — off the blind path.
type leaseManager struct {
	mu      sync.Mutex
	queues  map[string]*leaseQueue
	timeout time.Duration
	now     func() time.Time // injectable clock (tests)

	// waiterN is the total live ticket count across all queues, mirrored as a lock-free atomic
	// (Fix-3). The blind-write fast gate reads it WITHOUT mu: waiterN == 0 (with hotKeys.hotN == 0)
	// means no lease is contended anywhere → the firehose skips the hot-key check entirely. Kept
	// in lockstep with the queues under mu: +1 on acquire, -1 when a ticket leaves (driver release
	// via removeLocked, or reaper reclaim). A stale read is benign (liveness gate only).
	waiterN atomic.Int64
}

func newLeaseManager(timeout time.Duration) *leaseManager {
	if timeout <= 0 {
		timeout = defaultLeaseTimeout
	}
	return &leaseManager{
		queues:  make(map[string]*leaseQueue),
		timeout: timeout,
		now:     time.Now,
	}
}

// acquire enqueues a ticket for key and grants it immediately if the queue was empty. The
// caller then blocks on <-ticket.granted. FIFO: a ticket is granted only when every earlier
// ticket on the key has been released (or reaper-reclaimed) → arrival-order service, no
// starvation among lease holders (§6.4).
func (lm *leaseManager) acquire(key string) *leaseTicket {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	q := lm.queues[key]
	if q == nil {
		q = &leaseQueue{}
		lm.queues[key] = q
	}
	t := &leaseTicket{key: key, granted: make(chan struct{})}
	q.waiters = append(q.waiters, t)
	lm.waiterN.Add(1) // mirror len(all queues) for the lock-free blind-write gate (Fix-3)
	if len(q.waiters) == 1 {
		lm.grantHeadLocked(q)
	}
	return t
}

// grantHeadLocked grants the head of q (idempotent). Caller holds lm.mu.
func (lm *leaseManager) grantHeadLocked(q *leaseQueue) {
	if len(q.waiters) == 0 {
		return
	}
	head := q.waiters[0]
	if !head.active {
		head.active = true
		head.grantedAt = lm.now()
		close(head.granted)
	}
}

// release drops a ticket from its queue and grants the next waiter if the ticket was the head.
// Idempotent + safe if the ticket was already reaper-reclaimed (the driver's defer releaseAll
// still runs after a crash-recovery reclaim — it becomes a no-op). Driver goroutine.
func (lm *leaseManager) release(t *leaseTicket) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.removeLocked(t)
}

// removeLocked removes t from its queue and re-grants the head if needed. Caller holds lm.mu.
func (lm *leaseManager) removeLocked(t *leaseTicket) {
	if t.done {
		return
	}
	q := lm.queues[t.key]
	if q == nil {
		return
	}
	idx := -1
	for i, w := range q.waiters {
		if w == t {
			idx = i
			break
		}
	}
	if idx < 0 {
		t.done = true
		return // already removed (e.g. reaper-reclaimed) — waiterN was decremented then
	}
	wasHead := idx == 0
	t.done = true
	q.waiters = append(q.waiters[:idx], q.waiters[idx+1:]...)
	lm.waiterN.Add(-1) // this ticket left the queue → keep waiterN == total live tickets (Fix-3)
	if len(q.waiters) == 0 {
		delete(lm.queues, t.key)
		return
	}
	if wasHead {
		lm.grantHeadLocked(q)
	}
}

// reap is the lease-reaper goroutine's timeout backstop (§6.3): it reclaims any lease whose active
// holder has held it longer than lm.timeout — the driver-crashed-before-releasing case. Reclaiming
// a merely-slow legitimate holder is avoided by setting timeout well above commit latency;
// even if it did fire, correctness is preserved (the reclaimed holder loses point-key
// exclusivity → its Commit conflicts → it retries, §6.4). Called by the lease-reaper goroutine
// (pebble_engine.go `go e.leaseReaper()`), NOT the committer.
func (lm *leaseManager) reap() {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	now := lm.now()
	for _, q := range lm.queues {
		if len(q.waiters) == 0 {
			continue
		}
		head := q.waiters[0]
		if head.active && now.Sub(head.grantedAt) > lm.timeout {
			// Reclaim the stale head: mark done, drop it, grant the next waiter.
			head.done = true
			q.waiters = q.waiters[1:]
			lm.waiterN.Add(-1) // reclaimed ticket left the queue → keep waiterN == total live tickets (Fix-3)
			lm.grantHeadLocked(q)
			// Fix-5 note (harmless, deliberately NOT fixed): when this reclaim drains the queue to
			// empty, the now-empty *leaseQueue is left in lm.queues (pruned only lazily by a future
			// removeLocked, which won't come for a reaped ticket). hasWaiters reports len==0 and
			// decay retires the key, and waiterN stays accurate (0), so the stale empty entry is
			// inert — a bounded, rare (crashed-driver-only) memory wart, not a correctness issue.
		}
	}
}

// hasWaiters reports whether key has any holder/waiter — the stickiness signal for isHot.
func (lm *leaseManager) hasWaiters(key string) bool {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	q := lm.queues[key]
	return q != nil && len(q.waiters) > 0
}
