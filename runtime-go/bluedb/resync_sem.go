package bluedb

import (
	"runtime"
	"sync"
)

// resync_sem.go — the Phase-4c resync thundering-herd bound (NB-3, design §9 "NB-3 — RESYNC
// THUNDERING-HERD BOUND"). When the engine change-feed overflows (a high-write × high-N burst
// drops >=1 batch of unknown tenant), markResyncAll latches EVERY live sub's resync flag. Without a
// bound, N affected subscriptions would each re-run their query against the engine AT ONCE — an
// N-way synchronized stampede (the resync storm). This file bounds it three ways:
//
//   (a) per-sub COALESCING — markResyncAll sets ONE atomic flag per sub regardless of how many
//       batches dropped, so a burst collapses to exactly ONE re-query per affected sub (see
//       subscription.overflow + NeedsResync). The count of re-queries is therefore ≤ affected subs,
//       never × the drop count.
//   (b) a GLOBAL re-query SEMAPHORE (this file, default K = min(GOMAXPROCS, 8)) — a re-query holds
//       one of K slots for the duration of its engine scan, so the engine sees AT MOST K concurrent
//       reactive scans, not N. The remaining subs queue on the semaphore and run as slots free.
//   (c) the caller debounces per-sub (the rt reactiveLoop coalesces a change burst into one refresh;
//       the resync latch is read-and-cleared once per drain).
//
// The semaphore is PROCESS-GLOBAL (a package-level var): every reactive re-query in the process —
// resync-driven OR change-driven — shares the same K slots, so the total concurrent engine-scan
// pressure from reactivity is capped regardless of how many backends / sessions are live.

// reQuerySemCap is the global reactive re-query concurrency cap K (NB-3). Default min(GOMAXPROCS, 8):
// enough parallelism to use the machine, low enough that an N-way stampede can't swamp the engine.
func reQuerySemCap() int {
	k := runtime.GOMAXPROCS(0)
	if k > 8 {
		k = 8
	}
	if k < 1 {
		k = 1
	}
	return k
}

var (
	reQuerySemOnce sync.Once
	reQuerySem     chan struct{}
)

func ensureReQuerySem() chan struct{} {
	reQuerySemOnce.Do(func() { reQuerySem = make(chan struct{}, reQuerySemCap()) })
	return reQuerySem
}

// AcquireReQuery blocks until one of the K global reactive re-query slots is free, then returns the
// release func the caller MUST defer. Wrapping every reactive re-query's engine scan in
// Acquire/release is what enforces "the engine sees ≤ K concurrent reactive scans" (NB-3 (b)).
func AcquireReQuery() func() {
	sem := ensureReQuerySem()
	sem <- struct{}{}
	return func() { <-sem }
}

// ReQuerySlots reports the global re-query concurrency cap K — for observability + the NB-3 bound
// assertion in the bench (concurrent re-queries must stay ≤ ReQuerySlots()).
func ReQuerySlots() int { return cap(ensureReQuerySem()) }

// ResyncPending drives the bounded resync fan-out (NB-3). It snapshots every live subscription that
// has latched a resync need (NeedsResync — read-and-cleared, so per-sub coalescing already collapsed
// the burst to one), then runs `reQuery` for each one CONCURRENTLY but capped at K by the global
// semaphore. It returns the number of subs resynced (≤ live subs). `reQuery` is the caller's own
// re-run-the-query-against-my-view closure (rt owns the Sky-side re-query; bluedb owns the bound).
//
// Correctness: the semaphore caps CONCURRENCY (≤ K in flight), never the total (every pending sub
// runs exactly once), so no affected sub is starved — over-resync never under-notify (§4.4).
func (r *reactiveRegistry) ResyncPending(reQuery func(*subscription)) int {
	r.mu.Lock()
	var pending []*subscription
	for _, s := range r.subs {
		if s.NeedsResync() {
			pending = append(pending, s)
		}
	}
	r.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range pending {
		wg.Add(1)
		go func(s *subscription) {
			defer wg.Done()
			release := AcquireReQuery()
			defer release()
			reQuery(s)
		}(s)
	}
	wg.Wait()
	return len(pending)
}

// Coll exposes a subscription's collection id (for a resync re-query to know what to re-scan).
func (s *subscription) Coll() CollID { return s.coll }

// Tenant exposes a subscription's scope key (for a tenant-scoped resync re-query).
func (s *subscription) Tenant() string { return s.tenant }
