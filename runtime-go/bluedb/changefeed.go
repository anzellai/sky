package bluedb

import "sync/atomic"

// changefeed.go — the Phase-4a engine change-feed (§4.1). Every DURABLY-committed commit's
// decoded row-level changes are delivered to registered subscribers so a higher layer (the rt
// pump in 4b, or the EmbeddedBackend's internal reactive pump in 4a) can turn a write into a
// precise reactive delta. The engine stays authoritative and NON-BLOCKING: the single committer
// NEVER waits on a subscriber — a slow consumer DROPS its batch and latches overflow so it
// resyncs, so commit throughput is never held hostage (R1, the committer-never-stalls contract).
//
// The feed is fed from the TWO post-Apply(Sync) sites in committer.go (processBlindPhase1 +
// processTxn), strictly AFTER advanceDurableHi — durable-before-notify by construction (§7): a
// subscriber can never observe a non-durable change, and a sealed engine (durability fault) fires
// nothing. The KeyChange list is REUSED from the ring-append decode at those sites (not decoded a
// second time). The tenant tag travels on the batch as transient routing data (§3.4) — it is NOT
// part of the changelog and is never durably written (see CommitReq.Tenant).

// ChangeBatch is one durably-committed commit's decoded row-level changes plus its transient
// tenant routing tag (§4.1). One ChangeBatch per committed job (each carries a DISTINCT commitTs
// and the job's own write-time tenant tag). Post-commit the Change byte-slices are immutable;
// subscribers MUST NOT mutate them.
type ChangeBatch struct {
	CommitTs HLC         // the commit's assigned commitTs (monotone in commit order)
	Tenant   string      // the CommitReq.Tenant write-time tag (§3.4) — TRANSIENT, never durable
	Changes  []KeyChange // decoded at the committer ring-append site (reused, not re-decoded)
}

// changeFeedSub is one engine change-feed subscription. The channel is buffered; a full buffer
// DROPS the batch (non-blocking) and latches overflow so the drainer knows to resync (never a
// silent permanent loss — a dropped batch self-corrects via the resync path, §4.4).
type changeFeedSub struct {
	C        chan ChangeBatch
	overflow uint32 // atomic — set when >=1 batch was dropped since the last Overflowed() read
}

// Overflowed reports (and CLEARS) whether the feed dropped one or more batches since the last
// call because this subscriber's buffer was full. On true, the drainer has missed deltas and MUST
// resync (re-run its subscriptions' queries) rather than trust it saw every change.
func (s *changeFeedSub) Overflowed() bool {
	return atomic.SwapUint32(&s.overflow, 0) == 1
}

// subscribeChanges registers an engine change-feed subscriber. Every committed batch is delivered
// on the returned channel in commit order; a full channel drops the batch and latches overflow.
// Call the returned cancel to unregister (idempotent). Safe to call concurrently with commits and
// with other subscribe/cancel calls.
func (e *pebbleEngine) subscribeChanges(buf int) (*changeFeedSub, func()) {
	if buf <= 0 {
		buf = 1024
	}
	cs := &changeFeedSub{C: make(chan ChangeBatch, buf)}
	e.subMu.Lock()
	if e.changeSubs == nil {
		e.changeSubs = make(map[uint64]*changeFeedSub)
	}
	id := e.changeSubNext
	e.changeSubNext++
	e.changeSubs[id] = cs
	e.subMu.Unlock()

	var once uint32
	cancel := func() {
		if !atomic.CompareAndSwapUint32(&once, 0, 1) {
			return
		}
		e.subMu.Lock()
		delete(e.changeSubs, id)
		e.subMu.Unlock()
	}
	return cs, cancel
}

// hasChangeSubs reports whether any subscriber is registered so the committer can skip the whole
// emit (the common no-reactive process pays nothing).
func (e *pebbleEngine) hasChangeSubs() bool {
	e.subMu.RLock()
	n := len(e.changeSubs)
	e.subMu.RUnlock()
	return n > 0
}

// emitChangeBatch fans one durably-committed commit's changes to every subscriber, NON-BLOCKING.
// Called on the committer goroutine AFTER advanceDurableHi at the two §1.2 post-Apply sites — it
// MUST NOT block: a full subscriber channel drops the batch and latches its overflow flag.
func (e *pebbleEngine) emitChangeBatch(b ChangeBatch) {
	e.subMu.RLock()
	for _, cs := range e.changeSubs {
		select {
		case cs.C <- b:
		default:
			atomic.StoreUint32(&cs.overflow, 1)
		}
	}
	e.subMu.RUnlock()
}
