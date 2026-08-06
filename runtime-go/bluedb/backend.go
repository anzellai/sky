package bluedb

// backend.go — the Phase-3a Go `Backend` interface (§1.2 of docs/bluedb/phase3-api-design.md)
// and its backend-independent value types. `Backend` is the EMBEDDED-FAMILY contract: the
// embedded adapter (*EmbeddedBackend, embedded.go) satisfies it over the Phase-1/2 Engine, and a
// future cluster adapter satisfies it too. The SQL backends satisfy the *Persist verb contract*
// via the ported Sky `Store` arm, NOT a Go `Backend` (Design B, §1.1) — that is Phase 3b/3c.
//
// Phase 3a scope: this is the Go embedded core, provable with Go tests. The Sky `Std.Persist`
// port + the `Std.Codec` non-order-preserving marker (this file already takes an explicit
// not-orderable `ColType` in a `CollSchema`) are Phase 3b; the SQL adapters + dialect-aware
// renderer + KV≡SQL parity + the capability check are Phase 3c.

import "errors"

// Backend is the minimal contract every Persist universal verb dispatches to (§1.2). The method
// set is deliberately minimal — exactly the universal verbs. Handle-scoped: one Backend per open
// connection.
type Backend interface {
	// ---- CRUD ----

	// Get resolves the collection's primary key → the stored row (codec JSON blob), decoded by
	// the caller's codec. Absent → (nil, false, nil).
	Get(coll CollSchema, key string) (row []byte, ok bool, err error)

	// Put upserts a row by its self-assigned primary key (no generated-field fill). cols carries
	// the typed indexed/PK column values the adapter feeds encodeIndexKey (the ADAPTED
	// indexFieldValues thread, §2.3); it may be nil, in which case the adapter decodes them from
	// the row's codec JSON.
	Put(coll CollSchema, key string, row []byte, cols []ColValue) error

	// Insert inserts and returns the row with GENERATED fields filled — serial PK, defaultNow
	// timestamps, defaultWith app-computed defaults. `row` is the codec JSON of the record with
	// generated columns omitted; the return is the codec JSON of the persisted row.
	Insert(coll CollSchema, row []byte, cols []ColValue) (filled []byte, err error)

	// Delete removes by primary key. Missing key → nil (idempotent).
	Delete(coll CollSchema, key string) error

	// ---- Query ----

	// Query runs a RESOLVED plan (Cond already lowered to leaves + column-resolved, orders,
	// limit, offset) and returns the matching rows as codec JSON blobs, in the plan's order.
	Query(coll CollSchema, plan QueryPlan) (rows [][]byte, err error)

	// Count runs the same plan's WHERE and returns the row count.
	Count(coll CollSchema, plan QueryPlan) (int, error)

	// ---- Transaction ----

	// Transaction runs a pure body under the backend's serializable transaction (embedded →
	// Engine.Transact / SSI, bounded retry → ErrConflict). The body sees a TxHandle exposing
	// txGet/txPut/txInsert/txDelete/txQuery ONLY. txQuery inside a txn MUST record a read-set
	// (§2.6, the SSI crux) — see embeddedTx.Query.
	Transaction(fn func(tx TxHandle) error) error

	// ---- Escape hatch ----

	// SelectRaw runs an arbitrary SQL-shaped read (JOIN / GROUP BY / aggregate) and decodes each
	// row into a projection via the caller's codec. SQL-native on the SQL adapters; on the
	// embedded backend it is the single-collection raw-scan+in-RAM-eval fallback for shapes the
	// Cond algebra can't express (§4.4). Cross-collection JOINs are SQL-only (Decision 5).
	SelectRaw(sql string, params []ColValue) (rows [][]byte, err error)

	// ---- Capability probe (Decision 5) ----
	Capabilities() Capabilities
	Close() error
}

// TxHandle is the effect-free transaction surface exposed to a Transaction body (the purity
// gate). Query records a read-set — the SSI crux (§2.6).
type TxHandle interface {
	Get(coll CollSchema, key string) (row []byte, ok bool, err error)
	Put(coll CollSchema, key string, row []byte, cols []ColValue) error
	Insert(coll CollSchema, row []byte, cols []ColValue) (filled []byte, err error)
	Delete(coll CollSchema, key string) error
	Query(coll CollSchema, plan QueryPlan) (rows [][]byte, err error)
}

// CrossInstanceReactive is what v1 gates — NOT single-instance watch (§1.2/§5). Single-instance
// watch works on EVERY backend today via in-process pub/sub. CrossInstanceReactive is implemented
// in v1 only by the embedded commit-path (+ a future Postgres LISTEN/NOTIFY / Redis broker). A
// backend that does NOT implement it fails the boot check ONLY for a multi-replica app that
// declares cross-instance reactive bindings. Phase 3a defines the SEAM; Phase 4 wires the
// commit-path evaluation.
type CrossInstanceReactive interface {
	// Watch registers (collection, resolvedCond) and returns a subscription whose channel
	// delivers scoped Changes evaluated in the commit path (L4, Phase 4).
	Watch(coll CollSchema, plan QueryPlan) (Subscription, error)
}

// Subscription is a live reactive subscription (the Phase-4 seam). Phase 3a returns
// ErrReactiveSeamPhase4 — the commit-path evaluation is not wired here.
type Subscription interface {
	Changes() <-chan Change
	Close()
}

// Transition is the membership transition a reactive delta represents for a subscription's
// result set (§2.2). A subscriber folds it into its maintained list: Enter appends, Leave removes,
// Stay refreshes the row in place (and re-sorts when OrderChanged, §2.5).
type Transition uint8

const (
	// ChangeEnter — the row now matches the query and did not before (insert-in / update-in).
	ChangeEnter Transition = iota
	// ChangeLeave — the row no longer matches (delete / update-out / a displayed pk that stopped
	// hitting the footprint). Record is nil on a Leave (delete carries no body; §2.2 truth table).
	ChangeLeave
	// ChangeStay — the row matched before and still matches (in-range update). OrderChanged marks
	// a Stay whose ordering-column coordinate moved → the subscriber must re-sort (§2.5, A#1).
	ChangeStay
)

// Change is one row-level change delivered to a reactive subscriber (§2.2). Record is Just for
// Enter/Stay and nil for Leave (load-bearing: a delete/update-out carries no body).
type Change struct {
	Coll         CollID
	Pk           []byte
	Op           Op
	Record       []byte
	Transition   Transition // membership transition (§2.2)
	OrderChanged bool        // (A#1) a Stay whose sort-key column moved — re-sort (§2.5)
}

// Capabilities is the per-backend probe (Decision 5, §1.2). InProcessReactive is TRUE on every
// backend — single-instance watch never fails the capability check.
type Capabilities struct {
	InProcessReactive     bool // always true (KV + sqlite + pg)
	CrossInstanceReactive bool // commit-path / NOTIFY-backed (embedded true; sqlite/pg false in v1)
	SerializableTxn       bool // SSI (embedded) / SERIALIZABLE (pg) / BEGIN IMMEDIATE (sqlite)
	DeterministicTxn      bool // replayable command log (embedded/cluster only)
	Joins                 bool // native JOIN/GROUP BY via SelectRaw+SQL (sqlite/pg true)
}

// ── L0-derived schema (backend-independent) ──────────────────────────────────────────────────

// CollSchema is the L0-derived description the adapter needs (§1.2): name + stable CollID, the
// primary-key column, the ordered column list with mapped engine ColTypes + generated/unique
// flags, the declared single-column-ascending secondary indexes, and the generated-column set.
// In Phase 3a a test/caller constructs this directly (with an explicit not-orderable ColType for
// Money/Decimal/blob/Codec.map-backed columns).
//
// TODO(phase3b): DERIVE this from the Sky `Store`/`Codec` (colsOf + indexFieldValues, ADAPTED to
// carry ColType) + the new `Std.Codec` non-order-preserving MARKER that routes a Codec.map /
// Money / Decimal / unresolved column to the fallback engine ColType (§2.3).
type CollSchema struct {
	Name      string
	ID        CollID
	Key       string // primary-key column (snake)
	Cols      []ColSpec
	Indexes   []IndexSpec
	Generated map[string]bool // columns Insert/Put omit (engine fills)
}

// ColSpec is one column: its name, the mapped ENGINE ColType (range-optimized int/text/bool or a
// fallback real/money/blob — the not-orderable marker for 3a), and its flags.
type ColSpec struct {
	Name      string
	Type      ColType
	Unique    bool
	Generated bool
}

// IndexSpec is one declared secondary index. Phase-3a v1 scope is SINGLE-COLUMN ASCENDING only
// (§2.5) — composite/descending need NEW L0 builders and are OUT of v1. Unique marks the index as
// a UNIQUE constraint enforced via SSI (§2.7).
type IndexSpec struct {
	ID     IndexID
	Name   string
	Col    string
	Type   ColType
	Unique bool
}

// ColValue is one typed, injection-safe bound value (§1.2). Bytes is the NORMALIZED encoding used
// everywhere the column meets the encoder (write coord, pre-image, scan bound) — produced by the
// value constructors (IntVal/TextVal/BoolVal/MoneyVal/RealVal/BlobVal, indexer.go) and by
// decodeColumns from a codec JSON blob, so a Put coord and a Scan bound byte-match by construction
// (R-2.1 at the L3 boundary).
type ColValue struct {
	Type  ColType
	Bytes []byte
	Null  bool
}

// OrderSpec is one ordering clause in priority order.
type OrderSpec struct {
	Col  string
	Desc bool
}

// QueryPlan is the RESOLVED query (§1.2): the Cond tree already column-resolved + lowered to
// leaves, plus orders/limit/offset. Backend-independent. In Phase 3a a test/caller builds the
// CondNode directly; in Phase 3b Store.planJson decodes into it.
type QueryPlan struct {
	Where  CondNode
	Orders []OrderSpec
	Limit  int // -1 = none
	Offset int
}

// ── typed errors ─────────────────────────────────────────────────────────────────────────────

var (
	// ErrUniqueViolation is the deterministic duplicate-key rejection an insert/update on a
	// `unique` column returns when the unique-index point key is already owned by another row
	// (§2.7). errors.Is-friendly. This is the embedded arm's mirror of the SQL `UNIQUE` DDL
	// rejection — the LOSER of two concurrent inserts gets this (its ErrConflict retry re-reads
	// the now-present unique key), NOT a raw ErrConflict.
	ErrUniqueViolation = errors.New("bluedb: unique constraint violation")

	// ErrReactiveSeamPhase4 marks the cross-instance reactive Watch seam that Phase 4 wires into
	// the commit path. Phase 3a leaves the seam (§1.2, §5).
	ErrReactiveSeamPhase4 = errors.New("bluedb: cross-instance reactive Watch is a Phase-4 commit-path seam")

	// ErrSelectRawSQLOnly is returned by the embedded backend's SelectRaw for arbitrary SQL text:
	// the embedded engine cannot consume SQL. Single-collection filtered/projected reads go
	// through Query (the raw-scan + in-RAM eval path); cross-collection JOIN/GROUP BY is SQL-only
	// (Decision 5, §4.4).
	ErrSelectRawSQLOnly = errors.New("bluedb: SelectRaw SQL text is SQL-backend-only; use Query on the embedded backend (Decision 5)")

	// ErrUnknownCollection is returned when an operation references a collection the embedded
	// registry has never seen (Register was not called).
	ErrUnknownCollection = errors.New("bluedb: unknown collection (not registered)")
)

// ── data-key + reserved-keyspace layout (§2.4) ───────────────────────────────────────────────

const (
	// collSep separates collName from pk in a data userKey (userKey = collName ‖ 0x1F ‖ pk).
	// The collName prefix is load-bearing beyond namespacing: buildReq parses it to stamp each
	// KeyChange.Coll per-change (§2.1).
	collSep byte = 0x1F // ASCII Unit Separator
	// uniqTag discriminates a STORED unique-index point key from the data keyspace (§2.4/§2.7):
	// uniqKey = collName ‖ 0x1E ‖ indexName ‖ 0x1F ‖ encodeIndexKey(value). 0x1E (Record
	// Separator) can never collide with the 0x1F-delimited data keyspace.
	uniqTag byte = 0x1E // ASCII Record Separator
)

// dataUserKey builds the collection-namespaced data key: collName ‖ 0x1F ‖ pk (§2.4).
func dataUserKey(coll, pk string) []byte {
	out := make([]byte, 0, len(coll)+1+len(pk))
	out = append(out, coll...)
	out = append(out, collSep)
	out = append(out, pk...)
	return out
}

// dataCollPrefix returns collName ‖ 0x1F — the reader.Iterate prefix scoping a scan to one
// collection's rows.
func dataCollPrefix(coll string) []byte {
	out := make([]byte, 0, len(coll)+1)
	out = append(out, coll...)
	out = append(out, collSep)
	return out
}

// uniqUserKey builds the reserved STORED unique-index point key (§2.7):
// collName ‖ 0x1E ‖ indexName ‖ 0x1F ‖ encodeIndexKey(colType, value). Its stored value is the
// owning row's pk. Distinct from the data keyspace (0x1E vs the data key's 0x1F).
func uniqUserKey(coll, indexName string, colType ColType, valBytes []byte) []byte {
	enc := encodeIndexKey(0, colType, valBytes)
	out := make([]byte, 0, len(coll)+1+len(indexName)+1+len(enc))
	out = append(out, coll...)
	out = append(out, uniqTag)
	out = append(out, indexName...)
	out = append(out, collSep)
	out = append(out, enc...)
	return out
}

// collNameOf extracts the collection name from a data userKey OR a stored unique key — the prefix
// up to the first collSep (0x1F) or uniqTag (0x1E). Used by the per-collection resolver + indexer
// (§2.1).
func collNameOf(userKey []byte) string {
	for i := 0; i < len(userKey); i++ {
		if userKey[i] == collSep || userKey[i] == uniqTag {
			return string(userKey[:i])
		}
	}
	return string(userKey)
}
