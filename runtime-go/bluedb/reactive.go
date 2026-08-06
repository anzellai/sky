package bluedb

import (
	"bytes"
	"encoding/json"
	"sync"
	"sync/atomic"
)

// reactive.go — the Phase-4a query-scoped reactive engine (§2/§3/§4/§7), all in the bluedb layer
// (it imports ONLY stdlib + the package's own types — NO rt import, the layering rule). It owns:
//
//   - the subscription REGISTRY, strictly partitioned by (collection, tenant) — the fail-closed
//     tenant gate (§4.5): a delta tagged T visits ONLY byCollTenant[coll][T]; a ""-tagged delta
//     visits ONLY the "" bucket; there is NO wildcard/all-tenants code path (B#2).
//   - the DELTA-MATCH transition matcher (§2.2): coordHit(New)/resultPks belt + residual predicate
//     re-eval → Enter / Leave / Stay (+ the A#1 order-churn re-sort signal, §2.5).
//   - REGISTER-LIVE-FIRST setup (§7, A#2): register (buffer) → pin readTs → baseline (seed
//     resultPks) → drain-buffer-and-go-live under one lock hold → no miss window.
//   - an internal PUMP goroutine draining the engine change-feed (changefeed.go) into the registry
//     (in 4b this pump moves to rt; the local match + registry stay here).
//
// Delivery is NON-BLOCKING throughout: a full per-subscription channel drops + latches the sub's
// resync flag (never a permanent silent loss — the drop self-corrects via resync, §4.4), and the
// pump/committer are never stalled by a slow consumer (R1).

// reactiveDeliverBuf is the default per-subscription `out` channel buffer. A full buffer drops +
// latches resync (§4.4) rather than blocking the pump — so this bounds the burst a slow consumer
// absorbs before a drop-and-resync, never a correctness floor.
const reactiveDeliverBuf = 256

// ── the subscription registry (fail-closed tenant partition, §4.5) ─────────────────────────────

type subID uint64

// collTenantKey is the STRICT partition key (§3.4/§4.5): the fan-out for a delta tagged T on
// collection C visits ONLY byCollTenant[{C, T}]. No default/wildcard bucket exists.
type collTenantKey struct {
	coll   CollID
	tenant string
}

// reactiveRegistry holds live subscriptions bucketed by (collection, tenant). Guarded by mu; the
// pump snapshots a bucket's slice under mu before dispatching so register/unregister never race a
// dispatch.
type reactiveRegistry struct {
	mu           sync.Mutex
	byCollTenant map[collTenantKey][]*subscription
	subs         map[subID]*subscription
	next         subID
}

func newReactiveRegistry() *reactiveRegistry {
	return &reactiveRegistry{
		byCollTenant: map[collTenantKey][]*subscription{},
		subs:         map[subID]*subscription{},
	}
}

func (r *reactiveRegistry) register(s *subscription) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.next++
	s.id = r.next
	key := collTenantKey{coll: s.coll, tenant: s.tenant}
	r.byCollTenant[key] = append(r.byCollTenant[key], s)
	r.subs[s.id] = s
}

func (r *reactiveRegistry) unregister(s *subscription) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := collTenantKey{coll: s.coll, tenant: s.tenant}
	lst := r.byCollTenant[key]
	for i, x := range lst {
		if x == s {
			r.byCollTenant[key] = append(lst[:i], lst[i+1:]...)
			break
		}
	}
	if len(r.byCollTenant[key]) == 0 {
		delete(r.byCollTenant, key)
	}
	delete(r.subs, s.id)
}

// dispatchLocal is THE fail-closed fan-out (§4.5). For each change it looks up ONLY the
// (change.Coll, batch.Tenant) bucket and computes the §2.2 transition per matched subscription. A
// tenant-A sub is NEVER visited for a tenant-B (or "") delta — the strict partition is the reactive
// analogue of the v0.16.6 SQL-WHERE tenant gate. A batch's changes may span collections (a
// multi-collection txn) but share ONE tenant tag (the writer's).
func (r *reactiveRegistry) dispatchLocal(batch ChangeBatch) {
	if len(batch.Changes) == 0 {
		return
	}
	// Snapshot the per-collection bucket slices for THIS batch's tenant under the lock, so a
	// concurrent register/unregister can't mutate the slice we iterate.
	r.mu.Lock()
	buckets := make(map[CollID][]*subscription)
	for i := range batch.Changes {
		coll := batch.Changes[i].Coll
		if _, done := buckets[coll]; done {
			continue
		}
		src := r.byCollTenant[collTenantKey{coll: coll, tenant: batch.Tenant}]
		cp := make([]*subscription, len(src))
		copy(cp, src)
		buckets[coll] = cp
	}
	r.mu.Unlock()

	for i := range batch.Changes {
		ch := &batch.Changes[i]
		for _, sub := range buckets[ch.Coll] {
			sub.consider(batch.CommitTs, ch)
		}
	}
}

// markResyncAll latches EVERY live subscription's resync flag. Called when the pump's engine
// change-feed subscription overflows: a dropped engine batch has an UNKNOWN tenant/collection, so
// the no-miss choice is to resync everyone (over-resync, never under-notify, §4.4). A resync
// re-runs the sub's own query against its own view — safe and tenant-scoped by construction.
func (r *reactiveRegistry) markResyncAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.subs {
		atomic.StoreUint32(&s.overflow, 1)
	}
}

// ── one subscription (§3.1) ────────────────────────────────────────────────────────────────────

// subscription is a footprint + a delivery channel + a scope key held in the bluedb registry
// (§3.1). The delivery channel is a plain Go channel (no rt type — the layering that keeps bluedb
// free of an rt import). resultPks is the tracked result set (the Leave belt + A#1, §2.3).
type subscription struct {
	id        subID
	coll      CollID
	tenant    string      // the OPAQUE scope key (§3.4); "" ⇒ the process-global/no-tenant bucket
	schema    *CollSchema // for the residual bluedbEvalCond + pk extraction
	plan      QueryPlan
	footprint *ReadSet  // the SAME struct SSI uses (ranges precise, else collWitness)
	orderIdx  []IndexID // declared order-column indexes for the A#1 order-churn signal (§2.5)
	out       chan Change

	reg *reactiveRegistry
	b   *EmbeddedBackend
	tok ReaderToken // watermark token pinning readTs; released on Close (§7.6)

	mu        sync.Mutex
	resultPks map[string]bool // key: string(KeyChange.Pk) == the data userKey (collName‖0x1F‖pk)
	lastTs    HLC             // highest applied commitTs — monotone apply + dedup (§4.3)
	buffering bool            // register-live-first: buffer raw deltas until baseline seeded (§7)
	buf       []ChangeBatch   // raw deltas captured during setup (each with a single change)
	closed    bool
	overflow  uint32 // atomic — a full `out` (or an engine-feed drop) latched a resync need
}

// Changes implements Subscription: the channel precise Changes are delivered on.
func (s *subscription) Changes() <-chan Change { return s.out }

// NeedsResync reports (and CLEARS) whether this subscription dropped one or more deliveries (its
// `out` buffer was full, or the engine feed overflowed) since the last call — the subscriber MUST
// re-run its query to self-heal (§4.4). A dropped delta NEVER causes permanent silent loss.
func (s *subscription) NeedsResync() bool { return atomic.SwapUint32(&s.overflow, 0) == 1 }

// Close unregisters the subscription (stops the fan-out) and releases its watermark token so a
// long-lived subscription never pins the GC floor forever (§7.6). Idempotent.
func (s *subscription) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.mu.Unlock()

	s.reg.unregister(s)
	if s.tok != 0 {
		if eng, ok := s.b.eng.(*pebbleEngine); ok {
			eng.reg.Release(s.tok)
		}
	}
	close(s.out)
}

// consider is the pump's per-change entry. During setup (buffering) it captures the raw change;
// live, it dedups (commitTs <= lastTs ⇒ already covered by the baseline/a prior apply) then applies
// the §2.2 transition. It acquires s.mu, so the setup drain (which holds s.mu across the whole
// baseline-seed → buffer-drain → go-live flip) serializes any concurrent live delta behind it — the
// no-miss / no-double invariant of register-live-first (§7).
func (s *subscription) consider(commitTs HLC, ch *KeyChange) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if s.buffering {
		// KeyChange byte-slices are immutable post-commit → a shallow struct copy is safe.
		s.buf = append(s.buf, ChangeBatch{CommitTs: commitTs, Changes: []KeyChange{*ch}})
		return
	}
	if !s.lastTs.IsZero() && !s.lastTs.Less(commitTs) {
		return // commitTs <= lastTs — already applied / in the baseline (dedup, §4.3)
	}
	s.applyChangeLocked(commitTs, ch)
}

// applyChangeLocked is the membership-transition matcher (§2.2). s.mu MUST be held. It advances
// lastTs monotonically, then decides Enter / Leave / Stay from the tracked resultPks belt (§2.3)
// + coordHit(New) + the residual predicate, delivering a typed Change and updating resultPks.
func (s *subscription) applyChangeLocked(commitTs HLC, ch *KeyChange) {
	if s.lastTs.Less(commitTs) {
		s.lastTs = commitTs
	}
	pk := string(ch.Pk)
	wasDisplayed := s.resultPks[pk] // the authoritative "currently shown" belt (§2.3 Leg 2)

	if ch.Op == OpDelete {
		// A delete NEVER re-evals (Record=nil); it fires Leave purely on the belt (§2.2 truth
		// table). This closes the classic "deletes silently drop" bug (both blind + txn paths now
		// emit OldIndex, and the belt is independent of it).
		if wasDisplayed {
			delete(s.resultPks, pk)
			s.deliver(Change{Coll: ch.Coll, Pk: cloneBytes(ch.Pk), Op: OpDelete, Transition: ChangeLeave})
		}
		return
	}

	// OpPut. entered = does the NEW position hit the footprint? residual = does the new record
	// satisfy the full predicate? nowIn requires BOTH (the range is over-approximate; residual
	// excludes a boundary/non-matching row — truth-table row "no match → none").
	entered := s.hitFootprint(ch.NewIndex)
	nowIn := entered && s.recordMatches(ch.Record)

	switch {
	case nowIn && wasDisplayed:
		// Stay — an in-range update. Re-sort iff an order-column coord moved (§2.5, A#1).
		orderChanged := s.orderCoordChanged(ch.NewIndex, ch.OldIndex)
		s.deliver(Change{
			Coll: ch.Coll, Pk: cloneBytes(ch.Pk), Op: OpPut, Record: cloneBytes(ch.Record),
			Transition: ChangeStay, OrderChanged: orderChanged,
		})
	case nowIn && !wasDisplayed:
		// Enter — insert-in or update-into-range.
		s.resultPks[pk] = true
		s.deliver(Change{Coll: ch.Coll, Pk: cloneBytes(ch.Pk), Op: OpPut, Record: cloneBytes(ch.Record), Transition: ChangeEnter})
	case !nowIn && wasDisplayed:
		// Leave — update-out-of-range (incl. the autocommit-blind path, now that blindPut emits
		// OldIndex + the belt tracks the pk). Record nil on a Leave.
		delete(s.resultPks, pk)
		s.deliver(Change{Coll: ch.Coll, Pk: cloneBytes(ch.Pk), Op: OpPut, Transition: ChangeLeave})
	default:
		// !nowIn && !wasDisplayed → none.
	}
}

// deliver pushes a Change onto `out` NON-BLOCKING. A full channel (slow consumer) latches the
// resync flag instead of blocking the pump — the drop self-corrects via NeedsResync (§4.4), never
// a permanent silent loss.
func (s *subscription) deliver(ch Change) {
	select {
	case s.out <- ch:
	default:
		atomic.StoreUint32(&s.overflow, 1)
	}
}

// hitFootprint reports whether the change's coords hit the subscription's footprint. For a PRECISE
// (ranges/indexWitness) footprint it is the coord byte-range test (coordHit). For a CONSERVATIVE
// collection-witness footprint (a non-indexable predicate — OR/nested/Money/IS-NULL, §3.3) EVERY
// change to the collection is "entered"; the residual predicate (recordMatches) then decides exact
// membership from the row body — so a witness sub is precise-per-row on a PUT with a body, not a
// full re-query. (A resync re-query still self-heals any coverage gap after an overflow drop.)
func (s *subscription) hitFootprint(coords []IndexCoord) bool {
	if s.footprint == nil {
		return false
	}
	if len(s.footprint.collWitness) > 0 && s.footprint.collWitness[s.coll] {
		return true
	}
	return coordHit(s.footprint, coords)
}

// recordMatches applies the full resolved predicate to the change's new record body — the residual
// filter (§2.2). A body-less change (a delete, or a coord-only nudge) never matches here.
func (s *subscription) recordMatches(record []byte) bool {
	if len(record) == 0 || s.schema == nil {
		return false
	}
	cols, err := decodeColumns(s.schema, record)
	if err != nil {
		return false
	}
	return bluedbEvalCond(cols, &s.plan.Where)
}

// orderCoordChanged reports whether any declared order-column index coord differs between the row's
// NEW and OLD positions (§2.5, A#1) — the precise order-churn signal for a Stay. Only fires when the
// order column is a declared index (its coord is present in both New and Old); an unindexed order
// column relies on the app's pk-keyed re-sort belt (out of the Go-layer's scope).
func (s *subscription) orderCoordChanged(newC, oldC []IndexCoord) bool {
	for _, id := range s.orderIdx {
		nv := coordFor(newC, id)
		ov := coordFor(oldC, id)
		if nv == nil || ov == nil {
			continue // can't compare precisely → defer to the app re-sort belt
		}
		if !bytes.Equal(nv, ov) {
			return true
		}
	}
	return false
}

func coordFor(coords []IndexCoord, id IndexID) []byte {
	for i := range coords {
		if coords[i].Index == id {
			return coords[i].Key
		}
	}
	return nil
}

func cloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	return append([]byte(nil), b...)
}

// ── footprint construction (§3.1) ──────────────────────────────────────────────────────────────

// buildFootprint derives a subscription's ReadSet footprint from a resolved plan — the SAME
// classifier the SSI txn-Query read-set uses (classifyIndexable, §3). A single-column
// range/equality on a declared range-optimized index → a PRECISE range (byte-matching a KeyChange
// coord because both go through encodeScanRange/encodeIndexKey); anything else → the conservative
// collection witness (§3.3). We do NOT over-claim precise deltas for arbitrary Cond.
func buildFootprint(cs *CollSchema, plan *QueryPlan) *ReadSet {
	if hit, ok := classifyIndexable(cs, &plan.Where); ok {
		lo, hi := encodeScanRange(hit.idx.ID, hit.idx.Type, hit.loVal, hit.hiVal)
		return &ReadSet{ranges: []indexRange{{index: hit.idx.ID, lo: lo, hi: hi}}}
	}
	return &ReadSet{collWitness: map[CollID]bool{cs.ID: true}}
}

// orderIndexIDs resolves a plan's ORDER-BY columns to their declared range-optimized index ids —
// the order-witness set for the A#1 order-churn signal (§2.5).
func orderIndexIDs(cs *CollSchema, orders []OrderSpec) []IndexID {
	var out []IndexID
	for _, o := range orders {
		if idx, ok := declaredRangeIndex(cs, o.Col); ok {
			out = append(out, idx.ID)
		}
	}
	return out
}

// pkUserKeyOfRow extracts a stored row's data userKey (collName‖0x1F‖pk) — the SAME key a
// KeyChange.Pk carries — so a baseline row and a delta reference the same resultPks entry. The pk
// string form matches fillGenerated's pkStringOf (JSON string unquoted / JSON number → its text).
func pkUserKeyOfRow(cs *CollSchema, row []byte) (string, bool) {
	var raw map[string]json.RawMessage
	if json.Unmarshal(row, &raw) != nil {
		return "", false
	}
	rv, ok := raw[cs.Key]
	if !ok {
		return "", false
	}
	return string(dataUserKey(cs.Name, pkStringOf(rv))), true
}
