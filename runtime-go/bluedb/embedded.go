package bluedb

// embedded.go — the EmbeddedBackend: the Phase-3a embedded adapter implementing the Go `Backend`
// interface (backend.go) over the Phase-1/2 Engine. It is the KV-arm's Go receiver (§3.3). CRUD
// blind writes + snapshot reads; Query/Count as PK-ordered scan + in-RAM bluedbEvalCond (§2.4
// recommendation); Transaction over Engine.Transact with the txn-Query read-set contract (§2.6)
// and `unique` enforced via SSI (§2.7); the multi-collection per-change Coll stamping wired via
// the engine's SetCollResolver (§2.1).

import (
	"bytes"
	"encoding/json"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// EmbeddedBackend adapts a bluedb.Engine to the Backend contract. It holds a registry of
// per-collection CollSchemas keyed by name (so the per-change collResolver + the multi-collection
// indexer resolve collName → schema/CollID, §2.1/§3.4) and an in-process serial-id source for
// generated PKs.
type EmbeddedBackend struct {
	eng Engine

	mu      sync.RWMutex
	byName  map[string]*CollSchema
	serials map[string]*atomic.Int64
}

var (
	_ Backend               = (*EmbeddedBackend)(nil)
	_ CrossInstanceReactive = (*EmbeddedBackend)(nil)
	_ TxHandle              = (*embeddedTx)(nil)
)

// NewEmbeddedBackend wraps an Engine. The engine's lifecycle is the caller's; Close() closes it.
func NewEmbeddedBackend(eng Engine) *EmbeddedBackend {
	return &EmbeddedBackend{
		eng:     eng,
		byName:  map[string]*CollSchema{},
		serials: map[string]*atomic.Int64{},
	}
}

// Register installs (or refreshes) a collection's schema so the resolver + indexer see it. Public
// so a caller can pre-register a transaction's collections; every verb also auto-registers the
// CollSchema it is handed (ensureRegistered), so an explicit Register is only needed for a
// collection a transaction body touches but the verb signature does not name.
func (b *EmbeddedBackend) Register(cs CollSchema) {
	cp := cs // copy — the registry owns its schema
	b.mu.Lock()
	b.byName[cs.Name] = &cp
	b.mu.Unlock()
}

func (b *EmbeddedBackend) ensureRegistered(cs CollSchema) {
	b.mu.RLock()
	_, ok := b.byName[cs.Name]
	b.mu.RUnlock()
	if !ok {
		b.Register(cs)
	}
}

func (b *EmbeddedBackend) schemaByName(name string) *CollSchema {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.byName[name]
}

// collResolver derives a data/unique userKey's owning CollID from its collName prefix (§2.1).
func (b *EmbeddedBackend) collResolver(userKey []byte) CollID {
	if cs := b.schemaByName(collNameOf(userKey)); cs != nil {
		return cs.ID
	}
	return 0
}

// indexerFn is the ONE multi-collection indexer closure installed on a txn: it parses collName
// from the userKey and emits THAT collection's coords (§2.1). A stored unique-key value (a pk,
// not JSON) decodes to no columns → no coords.
func (b *EmbeddedBackend) indexerFn(userKey, record []byte) []IndexCoord {
	cs := b.schemaByName(collNameOf(userKey))
	if cs == nil {
		return nil
	}
	return buildIndexer(cs)(userKey, record)
}

// installTxn wires the multi-collection indexer + per-change resolver onto a transaction (§2.1).
func (b *EmbeddedBackend) installTxn(tx *Txn) {
	tx.SetIndexer(b.indexerFn)
	tx.SetCollResolver(b.collResolver)
}

// ── CRUD ─────────────────────────────────────────────────────────────────────────────────────

func (b *EmbeddedBackend) Get(coll CollSchema, key string) ([]byte, bool, error) {
	b.ensureRegistered(coll)
	r, err := b.eng.Snapshot()
	if err != nil {
		return nil, false, err
	}
	defer r.Close()
	v, _, ok := r.Get(dataUserKey(coll.Name, key))
	if !ok {
		return nil, false, nil
	}
	return append([]byte(nil), v...), true, nil
}

func (b *EmbeddedBackend) Put(coll CollSchema, key string, row []byte, _ []ColValue) error {
	b.ensureRegistered(coll)
	if len(uniqueColRefs(&coll)) > 0 {
		// A unique column needs the read+write SSI enforcement (§2.7) — route through a txn.
		return b.eng.Transact(func(tx *Txn) error {
			b.installTxn(tx)
			return b.txWrite(tx, &coll, key, row, false)
		})
	}
	return b.blindPut(&coll, key, row)
}

func (b *EmbeddedBackend) Insert(coll CollSchema, row []byte, _ []ColValue) ([]byte, error) {
	b.ensureRegistered(coll)
	filled, pk, err := b.fillGenerated(&coll, row)
	if err != nil {
		return nil, err
	}
	err = b.eng.Transact(func(tx *Txn) error {
		b.installTxn(tx)
		return b.txWrite(tx, &coll, pk, filled, true) // require-new: duplicate pk → ErrUniqueViolation
	})
	if err != nil {
		return nil, err
	}
	return filled, nil
}

func (b *EmbeddedBackend) Delete(coll CollSchema, key string) error {
	b.ensureRegistered(coll)
	if len(uniqueColRefs(&coll)) > 0 {
		// Remove the row's stored unique keys atomically with the row (§2.7 upkeep).
		return b.eng.Transact(func(tx *Txn) error {
			b.installTxn(tx)
			return b.txDelete(tx, &coll, key)
		})
	}
	return b.blindDelete(&coll, key)
}

// blindPut is the fast autocommit upsert (ReadSet == nil → the engine's blind fast path, §3.3).
// It emits NewIndex coords so a concurrent txn scanner validates against the insert; an
// index-less collection pays ZERO extra (buildIndexer returns nil without decoding the row).
func (b *EmbeddedBackend) blindPut(cs *CollSchema, key string, row []byte) error {
	uk := dataUserKey(cs.Name, key)
	coords := buildIndexer(cs)(uk, row)
	chg := KeyChange{Coll: cs.ID, Pk: uk, Op: OpPut, Record: row, NewIndex: coords}
	res := b.eng.Commit(CommitReq{
		Writes:           []VersionedWrite{{UserKey: uk, Op: OpPut, Value: row}},
		ChangelogPayload: EncodeChangelogPayload([]KeyChange{chg}),
	})
	return res.Err
}

// blindDelete emits OldIndex coords from the pre-image so a concurrent scanner sees the row
// LEAVING its range (§2.4). Idempotent — a missing key commits nothing.
func (b *EmbeddedBackend) blindDelete(cs *CollSchema, key string) error {
	uk := dataUserKey(cs.Name, key)
	r, err := b.eng.Snapshot()
	if err != nil {
		return err
	}
	pre, _, ok := r.Get(uk)
	var old []IndexCoord
	if ok {
		old = buildIndexer(cs)(uk, append([]byte(nil), pre...))
	}
	r.Close()
	if !ok {
		return nil
	}
	chg := KeyChange{Coll: cs.ID, Pk: uk, Op: OpDelete, OldIndex: old}
	res := b.eng.Commit(CommitReq{
		Writes:           []VersionedWrite{{UserKey: uk, Op: OpDelete}},
		ChangelogPayload: EncodeChangelogPayload([]KeyChange{chg}),
	})
	return res.Err
}

// txWrite is the shared transactional write path (§2.7). requireNew=true is an INSERT (a present
// pk → ErrUniqueViolation for the duplicate PK); requireNew=false is an upsert/update. It reads
// then writes each `unique` column's stored point key so two concurrent inserts of the same value
// conflict at validation; on an update that CHANGES a unique value it removes the old point key.
func (b *EmbeddedBackend) txWrite(tx *Txn, cs *CollSchema, pk string, row []byte, requireNew bool) error {
	uk := dataUserKey(cs.Name, pk)

	pre, existed := tx.Get(uk) // records a point read of the pk
	if requireNew && existed {
		return ErrUniqueViolation // duplicate primary key
	}
	var preCols map[string]ColValue
	if existed {
		preCols, _ = decodeColumns(cs, pre)
	}

	cols, err := decodeColumns(cs, row)
	if err != nil {
		return err
	}

	for _, u := range uniqueColRefs(cs) {
		cv := cols[u.col]
		// Remove the old unique key when an update changes a previously-set unique value.
		if preCols != nil {
			if old, ok := preCols[u.col]; ok && !old.Null && !bytes.Equal(old.Bytes, cv.Bytes) {
				_ = tx.Delete(uniqUserKey(cs.Name, u.indexName, u.typ, old.Bytes))
			}
		}
		if cv.Null { // SQL semantics: NULLs are not unique-constrained (many NULLs allowed)
			continue
		}
		uKey := uniqUserKey(cs.Name, u.indexName, u.typ, cv.Bytes)
		if owner, ok := tx.Get(uKey); ok && string(owner) != pk { // point read of the unique key
			return ErrUniqueViolation
		}
		if err := tx.Put(uKey, []byte(pk)); err != nil { // reserve/refresh the unique key
			return err
		}
	}
	return tx.Put(uk, row)
}

// txDelete removes a row and its stored unique keys inside a transaction (§2.7 upkeep).
func (b *EmbeddedBackend) txDelete(tx *Txn, cs *CollSchema, pk string) error {
	uk := dataUserKey(cs.Name, pk)
	pre, ok := tx.Get(uk)
	if !ok {
		return nil
	}
	preCols, _ := decodeColumns(cs, pre)
	for _, u := range uniqueColRefs(cs) {
		cv, has := preCols[u.col]
		if !has || cv.Null {
			continue
		}
		_ = tx.Delete(uniqUserKey(cs.Name, u.indexName, u.typ, cv.Bytes))
	}
	return tx.Delete(uk)
}

// ── Query / Count (autocommit — single snapshot read, no read-set, §2.6) ─────────────────────

func (b *EmbeddedBackend) Query(coll CollSchema, plan QueryPlan) ([][]byte, error) {
	b.ensureRegistered(coll)
	r, err := b.eng.Snapshot()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	rows := b.scanFilter(r, &coll, &plan.Where)
	return orderAndPage(&coll, rows, plan), nil
}

func (b *EmbeddedBackend) Count(coll CollSchema, plan QueryPlan) (int, error) {
	b.ensureRegistered(coll)
	r, err := b.eng.Snapshot()
	if err != nil {
		return 0, err
	}
	defer r.Close()
	return len(b.scanFilter(r, &coll, &plan.Where)), nil
}

// scanFilter is the PK-ordered scan + in-RAM bluedbEvalCond (§2.4 recommendation) over one
// collection's rows.
//
// TODO(phase3b/4): back a declared range-optimized filter with a real stored secondary-index
// SEEK (O(log n + k)) instead of a full PK scan. Phase 3a's PK-scan + in-RAM eval is correct and
// parity-provable; the seek fast-path locks the stored-index key layout alongside the Phase-4
// reactive entries.
func (b *EmbeddedBackend) scanFilter(r Reader, cs *CollSchema, cond *CondNode) [][]byte {
	var out [][]byte
	cur := r.Iterate(dataCollPrefix(cs.Name))
	for cur.Next() {
		row := append([]byte(nil), cur.Value()...)
		cols, err := decodeColumns(cs, row)
		if err != nil {
			continue
		}
		if bluedbEvalCond(cols, cond) {
			out = append(out, row)
		}
	}
	cur.Close()
	return out
}

// ── Transaction (§2.6 read-set contract lives in embeddedTx.Query) ───────────────────────────

func (b *EmbeddedBackend) Transaction(fn func(tx TxHandle) error) error {
	return b.eng.Transact(func(tx *Txn) error {
		b.installTxn(tx)
		return fn(&embeddedTx{b: b, tx: tx})
	})
}

// embeddedTx is the effect-free transaction surface (the purity gate) driving one Txn attempt.
type embeddedTx struct {
	b  *EmbeddedBackend
	tx *Txn
}

func (t *embeddedTx) Get(coll CollSchema, key string) ([]byte, bool, error) {
	t.b.ensureRegistered(coll)
	v, ok := t.tx.Get(dataUserKey(coll.Name, key)) // records a point read
	if !ok {
		return nil, false, nil
	}
	return v, true, nil
}

func (t *embeddedTx) Put(coll CollSchema, key string, row []byte, _ []ColValue) error {
	t.b.ensureRegistered(coll)
	return t.b.txWrite(t.tx, &coll, key, row, false)
}

func (t *embeddedTx) Insert(coll CollSchema, row []byte, _ []ColValue) ([]byte, error) {
	t.b.ensureRegistered(coll)
	filled, pk, err := t.b.fillGenerated(&coll, row)
	if err != nil {
		return nil, err
	}
	if err := t.b.txWrite(t.tx, &coll, pk, filled, true); err != nil {
		return nil, err
	}
	return filled, nil
}

func (t *embeddedTx) Delete(coll CollSchema, key string) error {
	t.b.ensureRegistered(coll)
	return t.b.txDelete(t.tx, &coll, key)
}

// Query is the txn-scoped read-set contract — THE phantom-hole fix (§2.6). A single indexable
// range/equality leaf on a declared range-optimized index routes through Txn.ScanRange (records
// the PRECISE index interval); anything else (OR/nested, non-declared column, not-orderable
// colType, IS-NULL — §2.3) routes through Txn.ScanCollection (records the collection witness).
// Either way the full predicate is re-applied as an exact in-RAM filter of the returned rows, so
// an over-approximate range is sound. It NEVER does a bare reader.Iterate that records nothing.
func (t *embeddedTx) Query(coll CollSchema, plan QueryPlan) ([][]byte, error) {
	t.b.ensureRegistered(coll)
	var cur Cursor
	if hit, ok := classifyIndexable(&coll, &plan.Where); ok {
		cur = t.tx.ScanRange(hit.idx.ID, hit.idx.Type, hit.loVal, hit.hiVal)
	} else {
		cur = t.tx.ScanCollection(coll.ID, dataCollPrefix(coll.Name))
	}
	var rows [][]byte
	for cur.Next() {
		row := append([]byte(nil), cur.Value()...)
		cols, err := decodeColumns(&coll, row)
		if err != nil {
			continue
		}
		if bluedbEvalCond(cols, &plan.Where) {
			rows = append(rows, row)
		}
	}
	cur.Close()
	return orderAndPage(&coll, rows, plan), nil
}

// ── escape hatch + capability + reactive seam ────────────────────────────────────────────────

// SelectRaw on the embedded backend cannot consume SQL text (§4.4). Single-collection
// filtered/projected reads go through Query; cross-collection JOIN/GROUP BY is SQL-only.
//
// TODO(phase3c): the SQL adapters (SQLite/Postgres) satisfy the Persist verb contract via the
// ported Sky `Store` arm (Design B), where SelectRaw is the driver query verbatim.
func (b *EmbeddedBackend) SelectRaw(_ string, _ []ColValue) ([][]byte, error) {
	return nil, ErrSelectRawSQLOnly
}

func (b *EmbeddedBackend) Capabilities() Capabilities {
	return Capabilities{
		InProcessReactive:     true,
		CrossInstanceReactive: true, // embedded commit-path (seam ships 3a; eval wired Phase 4)
		SerializableTxn:       true, // SSI (Decision 4)
		DeterministicTxn:      true, // replayable command log
		Joins:                 false,
	}
}

func (b *EmbeddedBackend) Close() error { return b.eng.Close() }

// Watch is the cross-instance reactive SEAM (§1.2/§5). Phase 3a leaves it; Phase 4 wires the
// commit-path evaluation of the resolved plan.
//
// TODO(phase3b/4): promote bluedbChangeAffectsQuery / bluedbQuerySub into the commit path and
// deliver scoped Changes on the Subscription channel.
func (b *EmbeddedBackend) Watch(_ CollSchema, _ QueryPlan) (Subscription, error) {
	return nil, ErrReactiveSeamPhase4
}

// ── generated-field fill (§1.2 Insert) ───────────────────────────────────────────────────────

// fillGenerated fills a collection's generated columns not supplied by the caller: a serial int
// PK from the in-process sequence, and a defaultNow timestamp (millis on an int column, RFC3339
// text otherwise). Returns the filled codec JSON + the resolved primary-key string. (Durable
// serial across restart is a Phase-3b refinement; the in-process sequence is correct within a
// process and sufficient for the Go-level Phase-3a suite.)
func (b *EmbeddedBackend) fillGenerated(cs *CollSchema, row []byte) (filled []byte, pk string, err error) {
	obj := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(row)) > 0 {
		if err := json.Unmarshal(row, &obj); err != nil {
			return nil, "", err
		}
	}
	for i := range cs.Cols {
		spec := &cs.Cols[i]
		generated := spec.Generated || (cs.Generated != nil && cs.Generated[spec.Name])
		if !generated {
			continue
		}
		if rv, present := obj[spec.Name]; present && !isJSONNull(rv) {
			continue // caller supplied it
		}
		base := spec.Type &^ colDescendingFlag
		switch {
		case spec.Name == cs.Key && base == ColInt: // serial PK
			obj[spec.Name] = json.RawMessage(strconv.FormatInt(b.nextSerial(cs.Name), 10))
		case base == ColInt: // an int default-now (epoch millis)
			obj[spec.Name] = json.RawMessage(strconv.FormatInt(time.Now().UTC().UnixMilli(), 10))
		default: // text default-now
			ts, _ := json.Marshal(time.Now().UTC().Format(time.RFC3339Nano))
			obj[spec.Name] = ts
		}
	}
	pkRaw, ok := obj[cs.Key]
	if ok {
		pk = pkStringOf(pkRaw)
	}
	filled, err = json.Marshal(obj)
	if err != nil {
		return nil, "", err
	}
	return filled, pk, nil
}

// nextSerial returns the next in-process serial id for a collection.
// TODO(phase3b): make the serial DURABLE across restart (an engine sequence key) — the in-process
// counter is correct within a process and sufficient for the Phase-3a Go suite.
func (b *EmbeddedBackend) nextSerial(name string) int64 {
	b.mu.Lock()
	c := b.serials[name]
	if c == nil {
		c = &atomic.Int64{}
		b.serials[name] = c
	}
	b.mu.Unlock()
	return c.Add(1)
}

func pkStringOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(trimJSONSpace(raw)) // a JSON number → its text form
}

// ── unique-column helpers (§2.7) ─────────────────────────────────────────────────────────────

type uniqueColRef struct {
	col       string
	typ       ColType
	indexName string
}

// uniqueColRefs collects a collection's unique columns from BOTH declared unique indexes and
// ColSpec.Unique flags (deduped) — the columns txWrite/txDelete maintain a stored unique key for.
func uniqueColRefs(cs *CollSchema) []uniqueColRef {
	seen := map[string]bool{}
	var out []uniqueColRef
	for i := range cs.Indexes {
		idx := &cs.Indexes[i]
		if idx.Unique && !seen[idx.Col] {
			seen[idx.Col] = true
			out = append(out, uniqueColRef{col: idx.Col, typ: idx.Type, indexName: idx.Name})
		}
	}
	for i := range cs.Cols {
		c := &cs.Cols[i]
		if c.Unique && !seen[c.Name] {
			seen[c.Name] = true
			out = append(out, uniqueColRef{col: c.Name, typ: c.Type, indexName: c.Name})
		}
	}
	return out
}
