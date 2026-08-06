package bluedb

import (
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
// lease it touches, it can only lose to a range/non-hot conflict OR the rare transitional race
// where a key promotes to hot between an optimistic attempt's anyHot check and its commit; a
// held-lease holder re-runs and wins as the transient drains. Generously bounded so a
// point-contended txn never returns ErrConflict, yet a genuine range conflict under the held
// leases still degrades to a typed ErrConflict rather than hanging.
const maxLeaseAttempts = 32

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
	indexer func(userKey, record []byte) []IndexCoord
	coll    CollID // owning collection id stamped on emitted KeyChanges

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
// collection-level fallback witness).
func (tx *Txn) SetCollection(coll CollID) { tx.coll = coll }

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

// Scan records the FULL index range as a read-set entry (the SSI crux, §2.2) — even a
// zero-row scan records the interval — then returns an ordered cursor over the range with
// the write-set merged in (read-your-writes over a range). lo/hi are the CLOSED encoded
// bounds (built via encodeScanRange, through the ONE encoder). For a range-optimized index.
func (tx *Txn) Scan(index IndexID, lo, hi []byte) Cursor {
	tx.ranges = append(tx.ranges, indexRange{
		index: index,
		lo:    append([]byte(nil), lo...),
		hi:    append([]byte(nil), hi...),
	})
	return tx.scanMaterialize(index, lo, hi, false, nil)
}

// ScanRange is a convenience over Scan that builds the encoded bounds from value-space
// bounds through encodeScanRange — so the descending invert+swap lives inside the one
// bound-builder (§2.2). loVal ≤ hiVal in VALUE order.
func (tx *Txn) ScanRange(index IndexID, colType ColType, loVal, hiVal []byte) Cursor {
	lo, hi := encodeScanRange(index, colType, loVal, hiVal)
	return tx.Scan(index, lo, hi)
}

// ScanFallback is the conservative fail-safe scan for a fallback colType (real/money/blob)
// or an IS-NULL predicate (§2.2). It records an index-level witness (any change touching
// this index conflicts) AND records each returned row as a point read — over-rejects, never
// under-rejects. match decides which rows the predicate returns.
func (tx *Txn) ScanFallback(index IndexID, match func(userKey, record []byte) bool) Cursor {
	tx.indexWitness[index] = true
	return tx.scanMaterialize(index, nil, nil, true, match)
}

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
			Coll:     tx.coll,
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
// Release is DRIVER-side (defer releaseAll); the committer-side reaper (leaseManager.reap) is
// the timeout backstop for a driver that crashes between Commit-return and its release defer.
func (e *pebbleEngine) transactUnderLeases(body func(tx *Txn) error) error {
	leasePathCalls.Add(1)

	// ── Phase A: discover the full hot-key set, holding NO lease. ──
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

	// ── Phase B: acquire ALL leases in canonical order (strict-2PL: acquire-all-then-run). ──
	tickets := make([]*leaseTicket, 0, len(hot))
	for _, k := range hot {
		t := e.leases.acquire(string(k))
		<-t.granted
		tickets = append(tickets, t)
	}
	defer func() { // driver releases; the committer reaper is the crash backstop
		for _, t := range tickets {
			e.leases.release(t)
		}
	}()

	// ── Phase C: run the pure body under the held leases (bounded). ──
	var lastErr error
	for attempt := 0; attempt < maxLeaseAttempts; attempt++ {
		tx, err := e.Begin() // snapshot AFTER the prior holders' durable commits
		if err != nil {
			return err
		}
		if berr := body(tx); berr != nil {
			tx.Abort()
			return berr
		}
		err = tx.Commit()
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrConflict) {
			return err
		}
		lastErr = err
		backoff(attempt)
	}
	if lastErr == nil {
		lastErr = ErrConflict
	}
	return lastErr
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

// scanMaterialize builds an ordered, write-set-merged cursor over the rows a scan returns
// (read-your-writes over a range, §7.5). For a range-optimized scan a row is IN when any of
// its indexer coords for `index` falls in the closed [lo, hi]; for a fallback scan `match`
// decides and each returned row is recorded as a point read. O(rows) — a Phase-2 test/L2
// materialization; Phase 3 backs scans with real secondary-index storage.
func (tx *Txn) scanMaterialize(index IndexID, lo, hi []byte, fallback bool, match func([]byte, []byte) bool) Cursor {
	rows := map[string][]byte{}
	tsOf := map[string]HLC{}

	cur := tx.reader.Iterate(nil)
	for cur.Next() {
		k := append([]byte(nil), cur.Key()...)
		v := append([]byte(nil), cur.Value()...)
		if tx.rowMatches(k, v, index, lo, hi, fallback, match) {
			rows[string(k)] = v
			tsOf[string(k)] = cur.CommitTs()
			if fallback {
				tx.recordPoint(k, cur.CommitTs(), true)
			}
		}
	}
	cur.Close()

	// Overlay the write-set: a buffered put that lands in range appears; a buffered delete
	// masks; a buffered put that moved OUT of range drops a previously-in-range row.
	for _, key := range tx.order {
		bw := tx.writes[key]
		if bw.op == OpDelete {
			delete(rows, key)
			delete(tsOf, key)
			continue
		}
		if tx.rowMatches([]byte(key), bw.value, index, lo, hi, fallback, match) {
			rows[key] = bw.value
		} else {
			delete(rows, key)
			delete(tsOf, key)
		}
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

func (tx *Txn) rowMatches(k, v []byte, index IndexID, lo, hi []byte, fallback bool, match func([]byte, []byte) bool) bool {
	if fallback {
		return match != nil && match(k, v)
	}
	for _, c := range tx.indexCoords(k, v) {
		if c.Index == index && inRangeClosed(lo, hi, c.Key) {
			return true
		}
	}
	return false
}

// sliceCursor is an ordered cursor over a materialized row slice.
type sliceCursor struct {
	keys, vals [][]byte
	tss        []HLC
	i          int
}

var _ Cursor = (*sliceCursor)(nil)

func (c *sliceCursor) Next() bool {
	c.i++
	return c.i < len(c.keys)
}
func (c *sliceCursor) Key() []byte   { return c.keys[c.i] }
func (c *sliceCursor) Value() []byte { return c.vals[c.i] }
func (c *sliceCursor) CommitTs() HLC { return c.tss[c.i] }
func (c *sliceCursor) Err() error    { return nil }
func (c *sliceCursor) Close()        {}
