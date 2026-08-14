package bluedb

import (
	"bytes"
	"errors"
	"math/rand"
	"sort"
	"sync/atomic"
	"time"
)

// maxTxnAttempts bounds the optimistic retry loop (§5.1). On exhaustion Transact returns a
// typed ErrConflict.
const maxTxnAttempts = 8

// maxLeaseAttempts bounds the strict-2PL Phase-C run (§6.3). While the txn holds every hot-key
// lease it touches, it is the sole active writer of those point keys among BOTH other lease-path
// txns AND blind writes (Fix-3 routes a blind write to a hot key through the same FIFO lease), so
// a purely point-contended RMW makes progress and commits on its held keys. It CAN still return a
// typed ErrConflict — HONESTLY, in exactly two cases: (1) a genuine range/predicate conflict
// (non-leaseable, §6.4) under the held leases; (2) transiently, a body whose touched hot-key set
// diverges from the held set (Fix-4 handles this by re-discovering + re-acquiring the expanded set
// up to maxLeaseRediscover, then surfacing ErrConflict only if it never stabilizes). Generously
// bounded so the rare promote-between-check-and-commit transient drains rather than hanging.
const maxLeaseAttempts = 32

// maxLeaseRediscover bounds Fix-4's re-discovery loop: a data-dependent body can, under
// contention, touch a NEW hot key each time it re-runs (branch divergence on a value another txn
// flips). Each such divergence expands the held lease set and re-acquires the WHOLE set in
// canonical order (still deadlock-free). The touched-key set is finite, so it stabilizes; this cap
// is the honest bound after which a never-stabilizing body surfaces a typed ErrConflict instead of
// livelocking.
const maxLeaseRediscover = 8

// leasePathCalls counts transactUnderLeases entries — a test seam: the range-contention
// conformance test asserts it stays 0 (predicate contention is never leased, §6.4).
var leasePathCalls atomic.Int64

// errTxnDone is returned by a Txn op after Commit/Abort.
var errTxnDone = errors.New("bluedb: transaction already finished")

// Txn is one transaction attempt (§1.2). NOT safe for concurrent use by multiple goroutines
// (a transaction body is sequential). It pins a begin-snapshot (readTs = durableHi, §3.4),
// records a read-set (point keys + scanned index ranges + fallback witnesses), and buffers a
// write-set with read-your-writes overlay; Commit funnels ONE CommitReq to the committer.
type Txn struct {
	e      *pebbleEngine
	reader *pebbleReader
	readTs HLC // == durableHi at Begin; the (readTs, commitTs] window's lower edge (R-2.8)

	// read-set (§2).
	points       map[string]pointRead // key: string(userKey)
	ranges       []indexRange
	collWitness  map[CollID]bool // conservative fallback (§2.2)
	indexWitness map[IndexID]bool

	// write-set (§1.3) — buffered, applied atomically at Commit; last-write-wins.
	writes map[string]*bufferedWrite // key: string(userKey)
	order  []string                  // deterministic write order (CommitReq.Writes / KeyChange)

	// indexer maps a (userKey, record) to its index coordinates. Phase 3 populates it from
	// L0 (Codec + Collection.indexes); Phase-2 tests supply a trivial indexer via SetIndexer.
	// A multi-collection transaction installs ONE indexer closure that parses collName from
	// the userKey and emits THAT collection's coords (§2.1) — the signature is unchanged
	// because the userKey already namespaces the collection.
	indexer func(userKey, record []byte) []IndexCoord
	coll    CollID // single-collection fallback id stamped when no resolver is installed

	// collResolver, when installed (SetCollResolver, §2.1), derives each write's owning
	// CollID from its data userKey prefix (collName ‖ 0x1F ‖ pk). A Persist transaction is
	// inherently MULTI-collection; buildReq attributes each KeyChange to its OWN collection so
	// a concurrent WitnessCollection(X) reader can never miss a write mis-stamped as the last
	// SetCollection'd id (the phantom hole §2.1 closes). nil ⇒ every change uses `coll` (the
	// Phase-2 single-collection behaviour). This is a per-change attribution change ONLY — the
	// on-disk key format, the changelog payload, and encodeIndexKey are all untouched.
	collResolver func(userKey []byte) CollID

	// tenant is the Phase-4 TRANSIENT reactive routing tag (§3.4) this txn's commit carries on
	// its CommitReq.Tenant. Set via SetTenant BEFORE Commit; stamped by the writing identity.
	// Default "" — a no-verified-tenant write. NEVER durably written (see CommitReq.Tenant).
	tenant string

	done bool
}

type bufferedWrite struct {
	op           Op
	value        []byte       // put: row bytes; delete: nil
	newIndex     []IndexCoord // put: positions the row enters; nil for delete
	oldIndex     []IndexCoord // update/delete: positions vacated (from the pre-image, §1.4)
	preimageRead bool         // the pre-image was read once (also recorded as a point read)
}

// Begin opens a single-attempt transaction pinned via the begin-snapshot path (§3.4).
func (e *pebbleEngine) Begin() (*Txn, error) {
	r, err := e.beginSnapshot()
	if err != nil {
		return nil, err
	}
	return &Txn{
		e:            e,
		reader:       r,
		readTs:       r.readTs,
		points:       make(map[string]pointRead),
		collWitness:  make(map[CollID]bool),
		indexWitness: make(map[IndexID]bool),
		writes:       make(map[string]*bufferedWrite),
	}, nil
}

// SetIndexer installs the record→coordinate mapper for the collections this txn touches
// (Phase-3 supplies the real one from the schema; Phase-2 tests supply a trivial one).
func (tx *Txn) SetIndexer(fn func(userKey, record []byte) []IndexCoord) { tx.indexer = fn }

// SetCollection stamps the owning collection id on emitted KeyChanges (drives the
// collection-level fallback witness). The single-collection fallback used when no per-change
// resolver is installed (§2.1).
func (tx *Txn) SetCollection(coll CollID) { tx.coll = coll }

// SetCollResolver installs the per-change collection resolver (§2.1). buildReq then derives
// each emitted KeyChange's Coll from its data userKey (collName ‖ 0x1F ‖ pk) via fn, so a
// multi-collection transaction attributes every write to its OWN collection. When unset, all
// changes fall back to the single SetCollection id — the Phase-2 single-collection behaviour.
func (tx *Txn) SetCollResolver(fn func(userKey []byte) CollID) { tx.collResolver = fn }

// SetTenant stamps the transient Phase-4 reactive routing tag (§3.4) buildReq copies onto
// CommitReq.Tenant. Never durably written. "" ⇒ no verified tenant.
func (tx *Txn) SetTenant(tenant string) { tx.tenant = tenant }

// collOf returns the owning collection id for a data userKey: the installed resolver's
// per-change attribution, else the single SetCollection fallback (§2.1).
func (tx *Txn) collOf(userKey []byte) CollID {
	if tx.collResolver != nil {
		return tx.collResolver(userKey)
	}
	return tx.coll
}

// ReadTs exposes the pinned begin-snapshot readTs (durableHi at Begin).
func (tx *Txn) ReadTs() HLC { return tx.readTs }

// Get resolves userKey with read-your-writes overlay, then records a point read (§1.3). An
// overlaid key (the txn's own buffered write) is NOT added to the read-set — the txn's own
// write is not a dependency (this is what makes self-upsert not self-conflict, §7.5).
func (tx *Txn) Get(userKey []byte) (value []byte, ok bool) {
	if bw, exists := tx.writes[string(userKey)]; exists {
		if bw.op == OpDelete {
			return nil, false
		}
		return append([]byte(nil), bw.value...), true
	}
	v, ts, present := tx.reader.Get(userKey)
	tx.recordPoint(userKey, ts, present)
	if !present {
		return nil, false
	}
	return v, true
}

func (tx *Txn) recordPoint(userKey []byte, ts HLC, present bool) {
	k := string(userKey)
	if _, exists := tx.points[k]; !exists {
		tx.points[k] = pointRead{versionSeen: ts, present: present}
	}
}

// ── EXCISED IN STAGE 2 (P1-STAGE2-PLAN §"Excision") ────────────────────────────────────
//
// Txn.Scan, Txn.ScanRange, Txn.ScanFallback, Txn.scanMaterialize and Txn.rowMatches are
// NOT ported. They are the ONLY writers of ReadSet.ranges and ReadSet.indexWitness, and
// they depend on index_key.go (encodeScanRange / ColType), whose presence would pull the
// P2 index surface into this stage.
//
// The consequence, stated plainly rather than left implicit: Stage 2 ships an SSI
// validator (validate.go) whose range-conflict and index-witness arms are STRUCTURALLY
// UNREACHABLE. **Stage 2's serializability claim covers point reads and collWitness
// only.** No Stage-2 gate may assert range-conflict or index-witness detection; mutating
// inRangeClosed to `return false` must not change any Stage-2 gate.
// TestStage2ReadSetRangesHaveNoProducer (stage2_readset_test.go) pins this.
//
// P2 restores them together with index_key.go, and must close AUDIT-N2 (readset.go)
// BEFORE Txn.Scan returns.

// WitnessCollection records a collection-level fallback witness (§2.2): any change to coll
// in the window conflicts. The coarsest safe witness — used when even the index is unknown
// (e.g. a full-collection predicate over an unsupported colType).
func (tx *Txn) WitnessCollection(coll CollID) { tx.collWitness[coll] = true }

// Put buffers an upsert (§1.3). newIndex = indexer(userKey, value); oldIndex is derived once
// from the pre-image at readTs (§1.4). Last-write-wins within the txn.
func (tx *Txn) Put(userKey, value []byte) error {
	if tx.done {
		return errTxnDone
	}
	bw := tx.bufferedFor(userKey)
	tx.ensurePreimage(userKey, bw)
	bw.op = OpPut
	bw.value = append([]byte(nil), value...)
	bw.newIndex = tx.indexCoords(userKey, value)
	return nil
}

// Delete buffers a versioned delete (§1.3). oldIndex from the pre-image (§1.4).
func (tx *Txn) Delete(userKey []byte) error {
	if tx.done {
		return errTxnDone
	}
	bw := tx.bufferedFor(userKey)
	tx.ensurePreimage(userKey, bw)
	bw.op = OpDelete
	bw.value = nil
	bw.newIndex = nil
	return nil
}

func (tx *Txn) bufferedFor(userKey []byte) *bufferedWrite {
	k := string(userKey)
	bw := tx.writes[k]
	if bw == nil {
		bw = &bufferedWrite{}
		tx.writes[k] = bw
		tx.order = append(tx.order, k)
	}
	return bw
}

// ensurePreimage reads the pre-image at readTs once, derives OldIndex from it, and records
// it as a point read — an update/delete depends on the row's prior state, so a concurrent
// change to it MUST conflict (the lost-update guard, §1.4/§7.4).
func (tx *Txn) ensurePreimage(userKey []byte, bw *bufferedWrite) {
	if bw.preimageRead {
		return
	}
	bw.preimageRead = true
	pre, ts, ok := tx.reader.Get(userKey)
	tx.recordPoint(userKey, ts, ok)
	if ok {
		bw.oldIndex = tx.indexCoords(userKey, pre)
	}
}

func (tx *Txn) indexCoords(userKey, record []byte) []IndexCoord {
	if tx.indexer == nil {
		return nil
	}
	return tx.indexer(userKey, record)
}

// Commit builds one CommitReq and funnels it to the committer (§4). Returns ErrConflict
// (validation failed — the driver retries), a durability error, or nil. Idempotent after the
// first call; Close()s the reader.
func (tx *Txn) Commit() error {
	if tx.done {
		return nil
	}
	tx.done = true
	defer tx.reader.Close()
	return tx.e.Commit(tx.buildReq()).Err
}

// buildReq assembles the one CommitReq this txn funnels to the committer — the write-set as
// VersionedWrites, the KeyChange list encoded into the opaque payload, the pinned readTs, and
// the read-set. Shared by Commit and the test-seam so a test can drive a chosen interleaving.
func (tx *Txn) buildReq() CommitReq {
	writes := make([]VersionedWrite, 0, len(tx.order))
	changes := make([]KeyChange, 0, len(tx.order))
	for _, k := range tx.order {
		bw := tx.writes[k]
		uk := []byte(k)
		writes = append(writes, VersionedWrite{UserKey: uk, Op: bw.op, Value: bw.value})
		changes = append(changes, KeyChange{
			Coll:     tx.collOf(uk),
			Pk:       uk,
			Op:       bw.op,
			Record:   bw.value,
			NewIndex: bw.newIndex,
			OldIndex: bw.oldIndex,
		})
	}

	var payload []byte
	if len(changes) > 0 {
		payload = EncodeChangelogPayload(changes)
	}

	return CommitReq{
		Writes:           writes,
		ChangelogPayload: payload,
		ReadTs:           tx.readTs,
		Tenant:           tx.tenant, // transient reactive routing tag (§3.4) — never durable
		ReadSet: &ReadSet{
			points:       tx.points,
			ranges:       tx.ranges,
			collWitness:  tx.collWitness,
			indexWitness: tx.indexWitness,
		},
	}
}

// Abort releases the snapshot without committing (§1.3) — Close()s the reader → releases the
// watermark token. Idempotent.
func (tx *Txn) Abort() {
	if tx.done {
		return
	}
	tx.done = true
	tx.reader.Close()
}

// touchedKeys is the union of the txn's write-set keys and its point-read keys (§6.2) — the
// point keys whose hotness the driver checks (anyHot/hotSubset) to decide the lease path. Index
// ranges / witnesses are NOT included: they have no single key to lease (§6.4).
func (tx *Txn) touchedKeys() [][]byte {
	seen := make(map[string]bool, len(tx.order)+len(tx.points))
	out := make([][]byte, 0, len(tx.order)+len(tx.points))
	for _, k := range tx.order { // write-set
		if !seen[k] {
			seen[k] = true
			out = append(out, []byte(k))
		}
	}
	for k := range tx.points { // point reads (incl. pre-image reads)
		if !seen[k] {
			seen[k] = true
			out = append(out, []byte(k))
		}
	}
	return out
}

// Transact runs the optimistic loop (§5.1): Begin → body → Commit; on ErrConflict it re-runs
// body against a FRESH snapshot (bounded retry + backoff), and returns nil on durable commit
// or a typed ErrConflict after the retry bound. The body MUST be pure (re-runnable, no
// external effects) — the Txn verbs are effect-free (buffer/record only until Commit), so a
// body built solely from them is automatically re-runnable (§5.3). A blind single-key write
// (no reads → empty read-set) validates trivially → one append.
//
// Phase 2b (§6): a genuinely-contended POINT key — detected by the committer via repeated
// validation aborts (recordAbort) — switches this txn to the strict-2PL committer-arbitrated
// FIFO lease path (transactUnderLeases) for starvation-freedom. The check runs BOTH before the
// optimistic commit (so a txn touching an already-hot key does not even try to lose the race)
// AND after an ErrConflict (the abort that just promoted the key). Range/predicate contention
// has NO lease (§6.4) → it stays on this bounded optimistic path → typed ErrConflict.
func (e *pebbleEngine) Transact(body func(tx *Txn) error) error {
	var lastErr error
	for attempt := 0; attempt < maxTxnAttempts; attempt++ {
		tx, err := e.Begin()
		if err != nil {
			return err
		}
		if berr := body(tx); berr != nil { // a body (logic) error, NOT a conflict
			tx.Abort()
			return berr
		}
		touched := tx.touchedKeys()
		// §6: a touched POINT key is already hot → don't gamble an optimistic commit that
		// would lose the race; go straight to the strict-2PL lease path.
		if e.hotKeys.anyHot(touched) {
			tx.Abort()
			return e.transactUnderLeases(body)
		}
		err = tx.Commit() // Commit already Close()s the reader (success OR error)
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrConflict) {
			return err // durability error → propagate
		}
		lastErr = err
		// §6: the conflict just fed recordAbort on the committer — re-check hotness. If the
		// culprit POINT key crossed the threshold, switch to the lease path now.
		if e.hotKeys.anyHot(touched) {
			return e.transactUnderLeases(body)
		}
		backoff(attempt)
	}
	if lastErr == nil {
		lastErr = ErrConflict
	}
	return lastErr // retry bound exhausted → typed, surfaced by Phase 3
}

// transactUnderLeases is the strict-2PL lease path (§6.3, the GRILL-REWORKED, deadlock-free
// version). It never acquires a lease on mid-body discovery — the concrete X<Y deadlock the
// grill found is prevented ONLY by discovering the WHOLE hot-key set first, then acquiring
// every lease in canonical bytes.Compare order before running the committing attempt.
//
//	Phase A — DISCOVER: run body once (holding NO lease) purely to observe the touched keys,
//	          then Abort (commits nothing, no external effect — §5.3 purity).
//	Phase B — ACQUIRE : take ALL hot-key leases in ascending bytes.Compare order (one global
//	          lock order over a total order → no hold-and-wait cycle → deadlock-free).
//	Phase C — RUN     : re-run the pure body under the held set. As the sole active writer of
//	          every hot key it holds, it cannot lose the validation race on those point keys;
//	          a range/non-hot conflict still returns ErrConflict (honest — no predicate lease).
//
// Release is DRIVER-side (defer releaseAll); a dedicated lease-reaper goroutine (leaseManager.reap,
// run by pebble_engine.go `go e.leaseReaper()` — NOT the committer) is the timeout backstop for a
// driver that crashes between Commit-return and its release defer.
func (e *pebbleEngine) transactUnderLeases(body func(tx *Txn) error) error {
	leasePathCalls.Add(1)

	// ── Phase A: discover the initial hot-key set, holding NO lease. ──
	tx0, err := e.Begin()
	if err != nil {
		return err
	}
	berr := body(tx0)
	touched := tx0.touchedKeys()
	tx0.Abort() // discard — this run committed nothing
	if berr != nil {
		return berr
	}
	hot := e.hotKeys.hotSubset(touched) // ascending bytes.Compare order (§6.4)
	if len(hot) == 0 {
		return e.Transact(body) // cooled between detection and here → back to optimistic
	}

	// held is the current lease set (a membership set). Fix-4 grows it when Phase C discovers a
	// hot key outside it, and RE-acquires the whole expanded set in canonical order each round.
	held := make(map[string]bool, len(hot))
	for _, k := range hot {
		held[string(k)] = true
	}

	var lastErr error
	for rediscover := 0; rediscover < maxLeaseRediscover; rediscover++ {
		// ── Phase B: acquire ALL held leases in canonical bytes.Compare order (strict-2PL:
		// acquire-all-then-run; one global lock order over a total order → no hold-and-wait
		// cycle → deadlock-free). Nothing is held across this acquire — the prior round released
		// everything before re-acquiring the expanded set, so the canonical order is never
		// violated by a partial hold. ──
		ordered := sortedKeys(held)
		tickets := make([]*leaseTicket, 0, len(ordered))
		for _, k := range ordered {
			t := e.leases.acquire(k)
			<-t.granted
			tickets = append(tickets, t)
		}

		// ── Phase C: run the pure body under the held leases (bounded). ──
		committed := false
		var newHot [][]byte
		for attempt := 0; attempt < maxLeaseAttempts; attempt++ {
			tx, err := e.Begin() // snapshot AFTER the prior holders' durable commits
			if err != nil {
				e.releaseLeases(tickets)
				return err
			}
			if berr := body(tx); berr != nil {
				tx.Abort()
				e.releaseLeases(tickets)
				return berr
			}
			// Fix-4: did this run touch a hot key OUTSIDE the held set? A data-dependent body can
			// branch onto a different key than Phase A saw (or a key that has since gone hot).
			// Such a key is unleased → the commit would race it unprotected → livelock. Instead,
			// ABORT, expand the held set, and re-acquire the WHOLE set in canonical order.
			if extra := e.unheldHot(tx.touchedKeys(), held); len(extra) > 0 {
				tx.Abort()
				newHot = extra
				break
			}
			err = tx.Commit()
			if err == nil {
				committed = true
				break
			}
			if !errors.Is(err, ErrConflict) {
				e.releaseLeases(tickets)
				return err
			}
			lastErr = err
			backoff(attempt)
		}

		// Release the whole held set BEFORE re-acquiring (deadlock-free: never hold across an
		// acquire of a lower-ordered key) or returning.
		e.releaseLeases(tickets)

		if committed {
			return nil
		}
		if len(newHot) > 0 {
			for _, k := range newHot {
				held[string(k)] = true
			}
			continue // re-acquire the expanded set next round
		}
		// Phase C exhausted maxLeaseAttempts with ErrConflict on the held keys → a genuine
		// range/predicate conflict under the leases (§6.4) → honest typed ErrConflict.
		if lastErr == nil {
			lastErr = ErrConflict
		}
		return lastErr
	}

	// Fix-4: the touched hot-key set never stabilized within the re-discovery cap → honest bound.
	if lastErr == nil {
		lastErr = ErrConflict
	}
	return lastErr
}

// sortedKeys returns the set's keys in ascending order. Go string ordering is bytewise, identical
// to bytes.Compare, so this IS the canonical lease-acquisition order (§6.4) — deadlock-free.
func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// unheldHot returns the touched keys that are currently hot but NOT in the held lease set — the
// Fix-4 signal to expand + re-acquire. Deduped; returned in touched-order (the caller only unions
// them into the held set, which re-sorts on the next acquire).
func (e *pebbleEngine) unheldHot(touched [][]byte, held map[string]bool) [][]byte {
	seen := make(map[string]bool, len(touched))
	var extra [][]byte
	for _, k := range touched {
		s := string(k)
		if held[s] || seen[s] {
			continue
		}
		if e.hotKeys.isHot(k) {
			seen[s] = true
			extra = append(extra, append([]byte(nil), k...))
		}
	}
	return extra
}

// backoff sleeps an exponentially-growing, jittered interval to dampen the two-txns-
// ping-ponging livelock (§5.1 / Decision 4 R4).
func backoff(attempt int) {
	base := time.Duration(1<<uint(attempt)) * 100 * time.Microsecond
	if base > 5*time.Millisecond {
		base = 5 * time.Millisecond
	}
	time.Sleep(base + time.Duration(rand.Int63n(int64(base)+1)))
}

// ScanCollection is the coarse-but-sound read-set path for a txn-scoped Query whose predicate
// no declared range-optimized index can tighten (§2.6): an OR/nested predicate, a predicate on
// a non-declared or not-order-preserving column, or an IS-NULL/IS-NOT-NULL leaf (§2.3). It
// records a COLLECTION-level witness (any change to coll in the window conflicts — catching a
// concurrent INSERT of a brand-new pk, which a set of point reads would miss) AND materializes
// every row under the collection's data-key prefix with the write-set merged in. Pairing the
// witness with the materialization in one method makes the read-set contract structural — a
// txn-Query can never do a bare reader.Iterate that records nothing.
func (tx *Txn) ScanCollection(coll CollID, prefix []byte) Cursor {
	tx.WitnessCollection(coll)
	return tx.scanPrefixMaterialize(prefix)
}

// scanPrefixMaterialize returns an ordered, write-set-merged cursor over every visible row whose
// userKey begins with prefix (read-your-writes over the collection). It records NOTHING — the
// caller (ScanCollection) owns the read-set entry.
func (tx *Txn) scanPrefixMaterialize(prefix []byte) Cursor {
	return tx.materializeScan(prefix, tx.reader.Iterate(prefix))
}

// materializeScan drains cur, overlays the write-set and returns the ordered result.
// Split out of scanPrefixMaterialize so the error path below is directly testable
// with a cursor that fails (the base reader only fails on I/O the test cannot force).
func (tx *Txn) materializeScan(prefix []byte, cur Cursor) Cursor {
	rows := map[string][]byte{}
	tsOf := map[string]HLC{}

	for cur.Next() {
		k := append([]byte(nil), cur.Key()...)
		rows[string(k)] = append([]byte(nil), cur.Value()...)
		tsOf[string(k)] = cur.CommitTs()
	}
	// Defect N1b: a cursor that stopped on an error must NOT be reported as an empty
	// (or, worse, write-set-only) collection. Overlaying buffered writes on a failed
	// base read yields a plausible-looking partial collection that a predicate then
	// treats as the truth. Surface the error and return no rows.
	scanErr := cur.Err()
	cur.Close()
	if scanErr != nil {
		return &sliceCursor{i: -1, err: scanErr}
	}

	// Overlay the write-set (only keys under this collection's prefix): a buffered put appears,
	// a buffered delete masks.
	for _, key := range tx.order {
		if !bytes.HasPrefix([]byte(key), prefix) {
			continue
		}
		bw := tx.writes[key]
		if bw.op == OpDelete {
			delete(rows, key)
			delete(tsOf, key)
			continue
		}
		rows[key] = bw.value
	}

	keys := make([]string, 0, len(rows))
	for k := range rows {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sc := &sliceCursor{i: -1}
	for _, k := range keys {
		sc.keys = append(sc.keys, []byte(k))
		sc.vals = append(sc.vals, rows[k])
		sc.tss = append(sc.tss, tsOf[k])
	}
	return sc
}

// sliceCursor is an ordered cursor over a materialized row slice.
type sliceCursor struct {
	keys, vals [][]byte
	tss        []HLC
	i          int
	err        error // N1b: a failed base scan, surfaced instead of an empty result
}

var _ Cursor = (*sliceCursor)(nil)

func (c *sliceCursor) Next() bool {
	if c.err != nil {
		return false
	}
	c.i++
	return c.i < len(c.keys)
}
func (c *sliceCursor) Key() []byte   { return c.keys[c.i] }
func (c *sliceCursor) Value() []byte { return c.vals[c.i] }
func (c *sliceCursor) CommitTs() HLC { return c.tss[c.i] }
func (c *sliceCursor) Err() error    { return c.err }
func (c *sliceCursor) Close()        {}
