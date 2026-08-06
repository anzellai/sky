package rt

// embedded_kernel.go — the Sky↔Go bridge kernels for the BlueDB embedded backend (Phase 3b of
// docs/bluedb/phase3-api-design.md §6). Each `Ffi.kernel "Embedded_*"` in Std.Persist resolves
// GENERICALLY to `rt.Embedded_*` via the lowerer's alias_go_name fallthrough (kernel.rs:32) — no
// compiler change. The kernels are thin: decode Sky args (JSON strings + a schema descriptor +
// a resolved plan), call bluedb.EmbeddedBackend (embedded.go), and return the Task-shaped result
// (a `func() any` thunk yielding Ok/Err), exactly mirroring the Db_* kernels' shape.
//
// The codec NEVER crosses into Go: Std.Codec encodes/decodes records to/from JSON strings IN Sky,
// and the boundary carries only JSON blobs + a schema descriptor JSON + a plan JSON. The Go side
// derives every index/PK column FROM the stored JSON blob via the CollSchema (decodeColumns in
// indexer.go), so no reflection on Sky records is needed here.
//
// Handle threading: connectKeyValue mints an opaque EmbeddedStore = a *bluedb.EmbeddedBackend
// (autocommit) OR, inside a transaction body, a bluedb.TxHandle (the tx surface). BOTH satisfy
// bluedb.TxHandle, so the CRUD/query kernels are uniform — the handle carried in KvConn decides
// autocommit vs txn (§2.6). Count/Transaction/SelectRaw need the concrete *EmbeddedBackend.

import (
	"encoding/json"
	"errors"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"

	"sky-app/bluedb"
)

// The embedded handle registry. A Sky `EmbeddedStore` is an integer id (a `KvConn Int`) — NOT a
// raw pointer, because a Sky opaque handle threaded through typed codegen needs a value with a
// stable representation (a nullary ADT lowers to int, a raw pointer can't ride a typed slot). The
// registry maps an id → the handle value: a `*EmbeddedBackend` for a connection (autocommit), a
// `bluedb.TxHandle` for a transaction body. `byPath` dedupes engine opens so a memoised
// `connectKeyValue` CAF (and repeated opens of one directory) share ONE engine.
var (
	embeddedRegistryMu sync.Mutex
	embeddedByID       = map[int64]any{}
	embeddedByPath     = map[string]int64{}
	embeddedNextID     int64
)

func embeddedRegister(v any) int64 {
	embeddedRegistryMu.Lock()
	defer embeddedRegistryMu.Unlock()
	embeddedNextID++
	id := embeddedNextID
	embeddedByID[id] = v
	return id
}

func embeddedUnregister(id int64) {
	embeddedRegistryMu.Lock()
	delete(embeddedByID, id)
	embeddedRegistryMu.Unlock()
}

func embeddedLookup(idArg any) any {
	embeddedRegistryMu.Lock()
	defer embeddedRegistryMu.Unlock()
	return embeddedByID[int64(AsInt(idArg))]
}

// embeddedOpenRegistry opens (or reuses) an engine at path and returns its registry id.
func embeddedOpenRegistry(path string) (int64, error) {
	embeddedRegistryMu.Lock()
	if id, ok := embeddedByPath[path]; ok {
		embeddedRegistryMu.Unlock()
		return id, nil
	}
	embeddedRegistryMu.Unlock()

	eng, err := bluedb.Open(path)
	if err != nil {
		return 0, err
	}
	b := bluedb.NewEmbeddedBackend(eng)

	embeddedRegistryMu.Lock()
	defer embeddedRegistryMu.Unlock()
	if id, ok := embeddedByPath[path]; ok { // lost a race — reuse the winner, drop ours
		_ = b.Close()
		return id, nil
	}
	embeddedNextID++
	id := embeddedNextID
	embeddedByID[id] = b
	embeddedByPath[path] = id
	return id, nil
}

// errEmbeddedAppAbort is the sentinel returned from a transaction body when the Sky-level Task
// yields Err: it rolls the engine txn back WITHOUT triggering the SSI conflict-retry loop (only
// bluedb.ErrConflict retries), and the captured Sky Err is propagated out verbatim.
var errEmbeddedAppAbort = errors.New("embedded: transaction body returned Err")

// ── handle resolution ────────────────────────────────────────────────────────────────────────

// embeddedTxHandle resolves a handle id to the uniform bluedb.TxHandle surface. Both
// *EmbeddedBackend (autocommit) and the tx handle (txn body) implement it, so every CRUD/query
// kernel is backend-vs-txn agnostic (§2.6).
func embeddedTxHandle(idArg any) (bluedb.TxHandle, bool) {
	h, ok := embeddedLookup(idArg).(bluedb.TxHandle)
	return h, ok
}

// embeddedBackend resolves a handle id to the concrete *EmbeddedBackend (needed for Count /
// Transaction / SelectRaw, which are not on the tx surface).
func embeddedBackend(idArg any) (*bluedb.EmbeddedBackend, bool) {
	b, ok := embeddedLookup(idArg).(*bluedb.EmbeddedBackend)
	return b, ok
}

// ── schema descriptor (Sky JSON → bluedb.CollSchema) ──────────────────────────────────────────

// embeddedSchemaJSON is the Sky-built descriptor Std.Persist threads per call. Names are the
// codec's (snake) column names; `type` is the engine kind string from Codec.colEngineKind —
// "int"/"text"/"bool" (range-optimized) or "real"/"money"/"blob"/"notorderable" (fallback), with
// a trailing "?" for nullable. The Sky side is the single authority on the colType mapping,
// including the §2.3 non-order-preserving marker.
type embeddedSchemaJSON struct {
	Name    string `json:"name"`
	Key     string `json:"key"`
	Cols    []struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		Unique    bool   `json:"unique"`
		Generated bool   `json:"generated"`
	} `json:"cols"`
	Indexes []struct {
		Name   string `json:"name"`
		Col    string `json:"col"`
		Type   string `json:"type"`
		Unique bool   `json:"unique"`
	} `json:"indexes"`
}

// embeddedColType maps an engine kind string to a bluedb.ColType. A trailing "?" (nullable) is
// stripped — NULL is a per-value flag the decoder handles, not a distinct ColType. "notorderable"
// (a Codec.map / unresolved column, §2.3) routes to the fallback ColBlob so rangeOptimized is
// false and the column is validated by the witness, never a byte-range.
func embeddedColType(kind string) bluedb.ColType {
	kind = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(kind)), "?")
	switch kind {
	case "int":
		return bluedb.ColInt
	case "text":
		return bluedb.ColText
	case "bool":
		return bluedb.ColBool
	case "real":
		return bluedb.ColReal
	case "money":
		return bluedb.ColMoney
	default: // "blob", "notorderable", unknown → conservative fallback (never range-optimized)
		return bluedb.ColBlob
	}
}

// stableID derives a stable, collision-resistant 32-bit id from a string (collection / index
// name). Same name → same id across calls, so the per-change collResolver + index matching are
// consistent within and across transactions.
func stableID(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	v := h.Sum32()
	if v == 0 {
		v = 1 // 0 is the resolver's "unknown collection" sentinel
	}
	return v
}

// parseEmbeddedSchema decodes the Sky schema descriptor into a bluedb.CollSchema, deriving stable
// CollID/IndexID from names and the Generated set from the col flags.
func parseEmbeddedSchema(schemaArg any) (bluedb.CollSchema, error) {
	var d embeddedSchemaJSON
	if err := json.Unmarshal([]byte(AsString(schemaArg)), &d); err != nil {
		return bluedb.CollSchema{}, err
	}
	cs := bluedb.CollSchema{
		Name:      d.Name,
		ID:        bluedb.CollID(stableID(d.Name)),
		Key:       d.Key,
		Generated: map[string]bool{},
	}
	for _, c := range d.Cols {
		gen := c.Generated
		cs.Cols = append(cs.Cols, bluedb.ColSpec{
			Name:      c.Name,
			Type:      embeddedColType(c.Type),
			Unique:    c.Unique,
			Generated: gen,
		})
		if gen {
			cs.Generated[c.Name] = true
		}
	}
	for _, ix := range d.Indexes {
		name := ix.Name
		if name == "" {
			name = ix.Col
		}
		cs.Indexes = append(cs.Indexes, bluedb.IndexSpec{
			ID:     bluedb.IndexID(stableID(d.Name + "\x00" + name)),
			Name:   name,
			Col:    ix.Col,
			Type:   embeddedColType(ix.Type),
			Unique: ix.Unique,
		})
	}
	return cs, nil
}

// embeddedPkFromRow extracts the primary-key string from a stored/insert row's JSON, reading the
// key column (string verbatim; a JSON number → its text form). Mirrors the engine's pkStringOf.
func embeddedPkFromRow(cs *bluedb.CollSchema, row []byte) (string, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(row, &obj); err != nil {
		return "", err
	}
	raw, ok := obj[cs.Key]
	if !ok {
		return "", errors.New("embedded: record has no primary-key field " + strconv.Quote(cs.Key))
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	return strings.TrimSpace(string(raw)), nil
}

// ── plan descriptor (Sky JSON → bluedb.QueryPlan) ─────────────────────────────────────────────

type embeddedPlanJSON struct {
	Where  *embeddedCondJSON `json:"where"`
	Orders []struct {
		Col  string `json:"col"`
		Desc bool   `json:"desc"`
	} `json:"orders"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type embeddedCondJSON struct {
	Op   string             `json:"op"`
	Col  string             `json:"col"`
	Type string             `json:"type"`
	Val  *embeddedValJSON   `json:"val"`
	Vals []embeddedValJSON  `json:"vals"`
	Kids []embeddedCondJSON `json:"kids"`
}

type embeddedValJSON struct {
	Type string `json:"type"`
	V    string `json:"v"`
}

// embeddedColValue builds a normalized bluedb.ColValue from a leaf's (kind, textual value). The
// bytes MUST match what decodeColumns produces from the stored JSON (§2.3 fidelity), so it reuses
// the SAME value constructors (IntVal/TextVal/BoolVal/RealVal/MoneyVal/blob-as-text).
func embeddedColValue(kind, v string) bluedb.ColValue {
	base := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(kind)), "?")
	switch base {
	case "int":
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return bluedb.IntVal(n)
	case "text":
		return bluedb.TextVal(v)
	case "bool":
		b := v == "true" || v == "1" || v == "TRUE" || v == "True"
		return bluedb.BoolVal(b)
	case "real":
		f, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return bluedb.RealVal(f)
	case "money":
		return bluedb.MoneyVal(v)
	default: // blob / notorderable / unknown → text bytes (byte-equal on equality)
		return bluedb.BlobVal([]byte(v))
	}
}

var embeddedCondOps = map[string]bluedb.CondOp{
	"true": bluedb.CondTrue, "eq": bluedb.CondEq, "ne": bluedb.CondNe, "neq": bluedb.CondNe,
	"gt": bluedb.CondGt, "gte": bluedb.CondGte, "lt": bluedb.CondLt, "lte": bluedb.CondLte,
	"like": bluedb.CondLike, "in": bluedb.CondIn, "isnull": bluedb.CondIsNull,
	"notnull": bluedb.CondNotNull, "and": bluedb.CondAnd, "or": bluedb.CondOr, "not": bluedb.CondNot,
}

func buildEmbeddedCond(c *embeddedCondJSON) bluedb.CondNode {
	if c == nil {
		return bluedb.CondNode{Op: bluedb.CondTrue}
	}
	op, ok := embeddedCondOps[strings.ToLower(c.Op)]
	if !ok {
		op = bluedb.CondTrue
	}
	node := bluedb.CondNode{Op: op, Col: c.Col, Type: embeddedColType(c.Type)}
	// Leaf values are normalized under the COLUMN's engine kind (c.Type), so a leaf value's bytes
	// + ColType match what decodeColumns produces from the stored JSON for that column (§2.3
	// fidelity) — the value constructor for each kind sets the matching ColType, so no override is
	// needed.
	switch op {
	case bluedb.CondAnd, bluedb.CondOr, bluedb.CondNot:
		for i := range c.Kids {
			node.Kids = append(node.Kids, buildEmbeddedCond(&c.Kids[i]))
		}
	case bluedb.CondIn:
		for i := range c.Vals {
			node.Vals = append(node.Vals, embeddedColValue(c.Type, c.Vals[i].V))
		}
	case bluedb.CondIsNull, bluedb.CondNotNull, bluedb.CondTrue:
		// no value
	default: // eq/ne/gt/gte/lt/lte/like
		if c.Val != nil {
			node.Val = embeddedColValue(c.Type, c.Val.V)
		}
	}
	return node
}

func parseEmbeddedPlan(planArg any) (bluedb.QueryPlan, error) {
	raw := strings.TrimSpace(AsString(planArg))
	if raw == "" {
		return bluedb.QueryPlan{Where: bluedb.CondNode{Op: bluedb.CondTrue}, Limit: -1}, nil
	}
	var d embeddedPlanJSON
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return bluedb.QueryPlan{}, err
	}
	plan := bluedb.QueryPlan{
		Where:  buildEmbeddedCond(d.Where),
		Limit:  d.Limit,
		Offset: d.Offset,
	}
	if d.Where == nil {
		plan.Where = bluedb.CondNode{Op: bluedb.CondTrue}
	}
	for _, o := range d.Orders {
		plan.Orders = append(plan.Orders, bluedb.OrderSpec{Col: o.Col, Desc: o.Desc})
	}
	return plan, nil
}

// stringRowsToSkyList wraps [][]byte JSON rows as a Sky `List String`.
func stringRowsToSkyList(rows [][]byte) any {
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, string(r))
	}
	return out
}

// ── kernels ──────────────────────────────────────────────────────────────────────────────────

// Embedded_open : String -> Task Error Int
// Opens (or reuses) a pebble-backed embedded engine at `path` and returns its registry-id handle
// (wrapped as a `Conn` in Sky). A registry dedupes by path so repeated opens share ONE engine.
func Embedded_open(pathArg any) any {
	return func() any {
		path := strings.TrimSpace(AsString(pathArg))
		if path == "" {
			return Err[any, any](ErrInvalidInput("Embedded.open: empty path (pass a directory for the embedded store)"))
		}
		id, err := embeddedOpenRegistry(path)
		if err != nil {
			return Err[any, any](ErrIo("Embedded.open: " + err.Error()))
		}
		return Ok[any, any](int(id))
	}
}

// Embedded_get : EmbeddedStore -> String(schema) -> String(key) -> Task Error (Maybe String)
func Embedded_get(storeArg, schemaArg, keyArg any) any {
	return func() any {
		h, ok := embeddedTxHandle(storeArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Embedded.get: invalid store handle"))
		}
		cs, err := parseEmbeddedSchema(schemaArg)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Embedded.get: bad schema: " + err.Error()))
		}
		row, found, err := h.Get(cs, AsString(keyArg))
		if err != nil {
			return Err[any, any](ErrFfi("Embedded.get: " + err.Error()))
		}
		if !found {
			return Ok[any, any](makeMaybeNothing())
		}
		return Ok[any, any](makeMaybeJust(string(row)))
	}
}

// Embedded_put : EmbeddedStore -> String(schema) -> String(rowJson) -> Task Error ()
// Upserts by the row's self-assigned primary key (extracted from the JSON key column).
func Embedded_put(storeArg, schemaArg, rowArg any) any {
	return func() any {
		h, ok := embeddedTxHandle(storeArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Embedded.put: invalid store handle"))
		}
		cs, err := parseEmbeddedSchema(schemaArg)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Embedded.put: bad schema: " + err.Error()))
		}
		row := []byte(AsString(rowArg))
		pk, err := embeddedPkFromRow(&cs, row)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Embedded.put: " + err.Error()))
		}
		// Phase-4b write-time tenant tag (§3.4): on an AUTOCOMMIT write the *EmbeddedBackend
		// stamps CommitReq.Tenant from the VERIFIED session tenant of the goroutine running the
		// write (currentSessionTenant() — "" when unstamped: raw Http.Server handler, background,
		// CLI — fail-closed). Inside a `transaction` body the handle is the tx surface and the tag
		// was set on the Txn by Embedded_transaction, so route through the plain TxHandle.
		if b, ok := embeddedBackend(storeArg); ok {
			if err := b.PutTenant(cs, pk, row, nil, currentSessionTenant()); err != nil {
				return Err[any, any](embeddedWriteErr("Embedded.put", err))
			}
			return Ok[any, any](nil)
		}
		if err := h.Put(cs, pk, row, nil); err != nil {
			return Err[any, any](embeddedWriteErr("Embedded.put", err))
		}
		return Ok[any, any](nil)
	}
}

// Embedded_insert : EmbeddedStore -> String(schema) -> String(rowJson) -> Task Error String
// Inserts, filling generated columns (serial PK, defaultNow), returns the persisted row JSON.
func Embedded_insert(storeArg, schemaArg, rowArg any) any {
	return func() any {
		h, ok := embeddedTxHandle(storeArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Embedded.insert: invalid store handle"))
		}
		cs, err := parseEmbeddedSchema(schemaArg)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Embedded.insert: bad schema: " + err.Error()))
		}
		// Phase-4b write-time tenant tag (§3.4) — see Embedded_put.
		if b, ok := embeddedBackend(storeArg); ok {
			filled, err := b.InsertTenant(cs, []byte(AsString(rowArg)), nil, currentSessionTenant())
			if err != nil {
				return Err[any, any](embeddedWriteErr("Embedded.insert", err))
			}
			return Ok[any, any](string(filled))
		}
		filled, err := h.Insert(cs, []byte(AsString(rowArg)), nil)
		if err != nil {
			return Err[any, any](embeddedWriteErr("Embedded.insert", err))
		}
		return Ok[any, any](string(filled))
	}
}

// Embedded_delete : EmbeddedStore -> String(schema) -> String(key) -> Task Error ()
func Embedded_delete(storeArg, schemaArg, keyArg any) any {
	return func() any {
		h, ok := embeddedTxHandle(storeArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Embedded.delete: invalid store handle"))
		}
		cs, err := parseEmbeddedSchema(schemaArg)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Embedded.delete: bad schema: " + err.Error()))
		}
		// Phase-4b write-time tenant tag (§3.4) — see Embedded_put.
		if b, ok := embeddedBackend(storeArg); ok {
			if err := b.DeleteTenant(cs, AsString(keyArg), currentSessionTenant()); err != nil {
				return Err[any, any](embeddedWriteErr("Embedded.delete", err))
			}
			return Ok[any, any](nil)
		}
		if err := h.Delete(cs, AsString(keyArg)); err != nil {
			return Err[any, any](embeddedWriteErr("Embedded.delete", err))
		}
		return Ok[any, any](nil)
	}
}

// Embedded_query : EmbeddedStore -> String(schema) -> String(planJson) -> Task Error (List String)
func Embedded_query(storeArg, schemaArg, planArg any) any {
	return func() any {
		h, ok := embeddedTxHandle(storeArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Embedded.query: invalid store handle"))
		}
		cs, err := parseEmbeddedSchema(schemaArg)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Embedded.query: bad schema: " + err.Error()))
		}
		plan, err := parseEmbeddedPlan(planArg)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Embedded.query: bad plan: " + err.Error()))
		}
		rows, err := h.Query(cs, plan)
		if err != nil {
			return Err[any, any](ErrFfi("Embedded.query: " + err.Error()))
		}
		return Ok[any, any](stringRowsToSkyList(rows))
	}
}

// Embedded_count : EmbeddedStore -> String(schema) -> String(planJson) -> Task Error Int
func Embedded_count(storeArg, schemaArg, planArg any) any {
	return func() any {
		cs, err := parseEmbeddedSchema(schemaArg)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Embedded.count: bad schema: " + err.Error()))
		}
		plan, err := parseEmbeddedPlan(planArg)
		if err != nil {
			return Err[any, any](ErrInvalidInput("Embedded.count: bad plan: " + err.Error()))
		}
		// Autocommit → the backend's Count (no row materialization). Inside a txn → Query + len
		// (records the §2.6 read-set), since the tx surface has no Count.
		if b, ok := embeddedBackend(storeArg); ok {
			n, err := b.Count(cs, plan)
			if err != nil {
				return Err[any, any](ErrFfi("Embedded.count: " + err.Error()))
			}
			return Ok[any, any](n)
		}
		h, ok := embeddedTxHandle(storeArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Embedded.count: invalid store handle"))
		}
		rows, err := h.Query(cs, plan)
		if err != nil {
			return Err[any, any](ErrFfi("Embedded.count: " + err.Error()))
		}
		return Ok[any, any](len(rows))
	}
}

// Embedded_transaction : Int -> (Int -> Task Error a) -> Task Error a
// Runs the Sky body under the engine's serializable transaction (SSI, bounded conflict-retry).
// Each attempt registers the fresh tx handle under a new id and calls the body with it; the Sky
// `transaction` verb wraps that id back into a KvConn, so the body's get/put/query dispatch to the
// SAME Txn (§2.6 read-set). A body that yields Err rolls back and the Err propagates (no retry).
func Embedded_transaction(storeArg, bodyArg any) any {
	return func() any {
		b, ok := embeddedBackend(storeArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Embedded.transaction: invalid store handle (nested transactions are not supported)"))
		}
		var captured SkyResult[any, any]
		ran := false
		// Phase-4b write-time tenant tag (§3.4): stamp the VERIFIED session tenant on the Txn so
		// every commit the body performs carries it — captured HERE (on the session goroutine),
		// never re-derived inside the engine. currentSessionTenant() is "" for an unstamped writer.
		txErr := b.TransactionTenant(currentSessionTenant(), func(tx bluedb.TxHandle) error {
			txID := embeddedRegister(tx)
			defer embeddedUnregister(txID)
			task := SkyCall(bodyArg, int(txID))
			res := anyTaskInvoke(task)
			captured = res
			ran = true
			if res.Tag != 0 {
				return errEmbeddedAppAbort // Sky Err → roll back, propagate (no retry)
			}
			return nil
		})
		if txErr != nil && !errors.Is(txErr, errEmbeddedAppAbort) {
			return Err[any, any](embeddedWriteErr("Embedded.transaction", txErr))
		}
		if !ran {
			return Err[any, any](ErrUnexpected("Embedded.transaction: body never ran"))
		}
		return captured // Ok value on commit, or the captured Sky Err on app-abort
	}
}

// Embedded_selectRaw : EmbeddedStore -> String(sql) -> List SqlValue -> Task Error (List String)
// The embedded engine cannot consume SQL text; single-collection filtered reads go through
// Embedded_query. Cross-collection JOIN/GROUP BY is SQL-backend-only (Decision 5) — returns the
// typed SQL-only error. Wired for the full Backend surface; the SQL adapters land in Phase 3c.
func Embedded_selectRaw(storeArg, sqlArg, _ any) any {
	return func() any {
		b, ok := embeddedBackend(storeArg)
		if !ok {
			return Err[any, any](ErrInvalidInput("Embedded.selectRaw: invalid store handle"))
		}
		_, err := b.SelectRaw(AsString(sqlArg), nil)
		if err != nil {
			return Err[any, any](ErrInvalidInput(err.Error()))
		}
		return Ok[any, any]([]any{})
	}
}

// embeddedWriteErr maps a bluedb write error to a Sky Error, surfacing a unique-constraint
// violation as a distinct InvalidInput (the deterministic duplicate-key rejection, §2.7) so app
// code can distinguish it from a storage failure.
func embeddedWriteErr(op string, err error) any {
	if errors.Is(err, bluedb.ErrUniqueViolation) {
		return ErrInvalidInput(op + ": unique constraint violation")
	}
	// A retry-exhausted SSI conflict surfaces as the typed Conflict Error (code
	// 8) — the SAME class the SQL arm's Db_withSerializableTransaction maps a pg
	// 40001 / SQLite BUSY to, so a caller's uniform retryWith / typed-Conflict
	// handling spans both backends (BlueDB Phase-3 A3).
	if errors.Is(err, bluedb.ErrConflict) {
		return ErrConflict(op + ": transaction conflict (serialization failed after retries)")
	}
	return ErrFfi(op + ": " + err.Error())
}
