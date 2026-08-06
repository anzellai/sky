# BlueDB Phase 3 — Logical API (L3) + backend adapters

> **Status:** architecture design, `feat/bluedb`. This is the doc Phase 3
> **grills**, then implements. No production code here — Go interfaces + struct
> sketches, the Sky port audit, the codec-driven-indexer wiring, the parity test
> plan, a phased sub-plan, and a risk register (the grill seed).
>
> **Builds ON:** the committed, Judge-verified Phase-1 engine + Phase-2 SSI
> transaction at `runtime-go/bluedb/` (`engine.go`, `txn.go`, `index_key.go`,
> `keychange.go`, `validate.go`, `committer.go`, `reader.go`, `readset.go`,
> `keys.go`). Every `runtime-go/bluedb/` `file:line` is relative to that package.
>
> **Reference checkout:** the proven prior-art (`exp/bluedb`) is at
> `.claude/worktrees/ref-exp-bluedb/`. Every citation prefixed `ref:` is relative
> to that worktree (`sky-stdlib/…`, `runtime-go/rt/…`). The prior L3 surface
> works and is largely the moat; this doc says precisely what PORTs, what ADAPTs,
> and what is NEW.
>
> **Realizes:** clean-slate-architecture.md §L3 (Logical API), **Decision 5**
> (backend parity — the RESOLVED runtime-loud capability check, NOT a compile-time
> union gate), §7 Phase 3; phase2-txn-design.md §1 (the `Txn` API the embedded
> adapter drives) + §2.2/§3.3 (the ONE canonical `encodeIndexKey` the indexer must
> reuse).

---

## 0. TL;DR — the one-paragraph thesis

Phase 3 re-homes the proven backend-invisible surface — the phantom-tag `Conn cap`
front, the universal verbs (`get`/`put`/`insert`/`delete`/`query`/`transaction`/
`watch`), and the shared `Cond`/`Query` algebra — so that three backends satisfy the
**Persist verb contract**: an **embedded adapter over the new engine (Phases 1–2)**,
**SQLite**, and **Postgres**. The proven `case conn of` dispatch stays in Sky (the SQL
arm is `Std.Db.Store`, itself pure Sky — so `buildSqlQuery` is reused, never
re-rendered in Go); only the KV arm's kernels are rebuilt to route to the new engine
through a **Go `Backend` interface** (the *embedded-family* contract, cluster-ready).
The single hardest deliverable is
**the real codec-driven indexer**: Phase 2 stubbed `tx.indexer` (`txn.go:62`,
installed via `SetIndexer`, `txn.go:95`); Phase 3 wires it from **L0** — a
`Collection`'s `Codec` + declared indexes become a Go closure `func(userKey, record
[]byte) []IndexCoord` that emits each index coord through the **ONE canonical
`encodeIndexKey`** (`index_key.go:57`) — *the same encoder* `Txn.ScanRange`'s bounds
go through (`txn.go:145` → `index_key.go:109`). That single-encoder identity is what
makes serializable-index-validation REAL at the L3 boundary (Phase-2 grill R-2.1),
not a test stub. On the Sky side, almost everything **PORTs** (`ref:Persist.sky`,
`ref:Store.sky`, `ref:Codec.sky`); the KV arms **ADAPT** (the manual colType-tag /
`indexFieldValues` plumbing collapses onto the codec + the ordered engine); the
`Backend` interface + the embedded adapter + the indexer are **NEW**. The gate is
**SQL≡KV parity for a DOCUMENTED SEMANTICS SUBSET** (equality, non-null ordering,
integer ranges, ASCII `LIKE` under a forced collation): `examples/55-persist-query`
— identical `Cond`/`Query` source run on `connectKeyValue` (embedded) and
`connectRelational` (SQL) — produces byte-identical labelled output on the new
engine, and `selectRaw` works. Dialect-divergent shapes (nullable `ORDER BY`,
`LIKE` case, numeric-vs-text) are pinned to ONE forced semantics via a
dialect-aware renderer + a matching `bluedbEvalCond` (§4.2, §8) — "byte-identical
for ALL `Cond`/`Query`" is provably false because the two SQL dialects diverge
from EACH OTHER (§0.6, R4-1). `watch`/`live` are **reactive on every backend
single-instance** (in-process pub/sub — the SQL write arms already
`publishChangeKernel`, `ref:Persist.sky:390`); the Decision-5 capability check
guards only **CROSS-INSTANCE** reactivity, never single-instance watch (§5, R5-1).

**The SSI is SOUND at the L3 wiring** (this doc's grill closed four phantom
holes that the first draft left under-specified): a txn-scoped `Query` records a
read-set (§2.6); an `isNull`/`notNull` predicate and a non-order-preserving
(`Money`/`Codec.map`) indexed column route to the conservative fallback witness,
never a range (§2.3); a multi-collection `transaction` stamps each `KeyChange`
with its OWN collection (§2.1 — a bounded engine `Txn`/`buildReq` change, no
format change); and `unique` is enforced across concurrent inserts via an
SSI read+write of the unique-index point key (§2.7).

---

## 0.5 Grill outcomes (Phase-3 design close)

A 2-adversary grill (+ this doc's revision) closed the design. The Phase-1/2
engine and the single canonical `encodeIndexKey` (`index_key.go:57`) are SOUND;
the L3 WIRING was under-specified and, as first drafted, would have shipped an
UNSOUND SSI (four phantom holes) and OVER-CLAIMED parity. Every fix below is a
bounded revision/decision — no engine-core or key-format rework.

**Confirmed decisions (resolved, not open):**

- **Design B, decisively.** Keep the Persist universal verbs as pure-Sky
  `case conn of` (§1.1). The Go `Backend` interface is the **embedded-family**
  contract; the SQL backends satisfy the *verb contract* via the ported Sky
  `Store` SQL-render — never a Go `Backend`. B ships TWO Cond-compilers (SQL text
  for the relational arm; plan-JSON + `bluedbEvalCond` for the embedded arm);
  Design A would add a THIRD — a Go re-implementation of `renderCond`, a pure
  drift surface duplicating a proven Sky renderer. **Honesty note: A does not help
  parity and B does not hurt it.** The dual-compile is INHERENT to having a
  non-SQL backend, in BOTH designs — an embedded engine cannot consume SQL text,
  so *some* second Cond→plan compiler exists regardless. B's choice is only WHERE
  the SQL text is rendered (Sky, reused) vs re-rendered (Go, drift). R3-3 is
  therefore CLOSED in favour of B. (Design A retained in §3.3 only as the
  documented rejected alternative.)
- **Zero `hir`/`kernel_api` compiler change — CONFIRMED TRUE.** The rebuilt KV-arm
  verbs (`Ffi.kernel "Embedded_*"`) resolve GENERICALLY: `detect_kernel_alias`
  (`rust/crates/lower/src/lower.rs:1124`) → `alias_go_name` (`kernel.rs:26`) →
  `rt.<Raw>` (the fallthrough at `kernel.rs:32`, since `Embedded`/`Persist`/
  `BlueDB` are absent from `kernel_go_name_opt`). Typing comes from the `.sky`
  annotation; `Std.Persist` is a real `.sky` file so `sky doc` reads it and no
  `kernel_api.rs` gate applies. R6 (compiler touch-points) stays minimal.

**Correctness-critical fixes (mandatory pre-implementation — the SSI-soundness set):**

| # | Hole (as first drafted) | Fix | §  |
|---|---|---|---|
| 1 | txn-scoped `Query` = "PK scan + in-RAM eval", **no read-set** → predicate phantom (SI, not serializable) | `Query` inside a txn MUST record a read-set: indexable range/eq leaf on a declared range-optimized index → `Txn.ScanRange` + residual in-RAM filter; anything else → `Txn.WitnessCollection` (coarse but sound) | §2.6 |
| 2 | `isNull`/`notNull` over an index range MISSES a concurrent `insert {f=Nothing}` (NULL emits no coord) → under-reject | an IS-NULL/IS-NOT-NULL predicate routes to `WitnessCollection` (or a point read-set), NEVER an index range | §2.3 |
| 3 | `Money`-as-text (`Codec.map … Codec.string`) has shape `SScalar CText` → mapped `ColText` (range-optimized) → lexical order ≠ numeric → wrong `orderAsc` + range under-reject; the claimed `ColMoney` fallback has NO trigger | add a **non-order-preserving marker** to `Std.Codec` so `Codec.map`/non-primitive-backed columns route to the conservative fallback; `colTypeFor`'s `Nothing` default (`ref:Persist.sky:347-348`) routes UNRESOLVED fields to the fallback too (small stdlib change) | §2.3 |
| 4 | Phase-2 `Txn` is single-collection (`buildReq` stamps EVERY change with the one `tx.coll`, `txn.go:247`); a multi-collection `transaction` (the point of transactions) stamps all changes with the LAST collection → a `WitnessCollection(other)` reader misses them → under-reject | derive `Coll` **per-change from the userKey prefix** in `buildReq`; carry a per-collection indexer. A bounded engine `Txn`/`buildReq` change — NO key-format change | §2.1 |
| 5 | `unique` unenforced on embedded (coords are witnesses, not stored unique keys → two concurrent `insert email='x'` both commit; SQL arm's UNIQUE DDL rejects one → parity break) | enforce `unique` via SSI: an insert/update READS + WRITES the unique-index point key; the second concurrent insert conflicts at validation | §2.7 |

**Parity + reactivity honesty fixes:**

- **6.** Parity gate reframed from "byte-identical for ALL" → "byte-identical for the
  DOCUMENTED PARITY SUBSET, with dialect-divergent shapes pinned to one forced
  semantics" (§0.6, §4.2, §8). The two SQL dialects diverge from each other; a
  single embedded backend cannot be "byte-identical" to two mutually-divergent
  SQL backends without forcing.
- **7.** `renderCond`/`orderTail` (`ref:Store.sky:1230,1290`) reclassified PORT →
  **ADAPT** — they need dialect-aware `NULLS FIRST/LAST` + a `LIKE`-collation
  decision to reach even subset parity (§3.1, §4.2).
- **8.** Reactivity corrected: `watch`/`live` are NOT typed on the `KeyValue` tag
  (`watchCollection : Collection a`, `ref:Persist.sky:947`, no `Conn`;
  `live : Conn cap`, `ref:Persist.sky:979`, any backend). Single-instance watch
  works on ALL backends TODAY via in-process pub/sub. The capability check guards
  only cross-instance reactivity (§5).

**Scoped OUT of v1:** composite + descending indexes (L0's `index : String` is
single-column ascending only; they need NEW L0 builders — §2.5). The stored
secondary-index seek fast-path is Phase 3b/4 (§2.4); Phase 3a Query is PK-scan +
in-RAM eval + the read-set contract (§2.6), which is correct and parity-provable.

---

## 0.6 Parity is a documented forced-semantics SUBSET — not "byte-identical for all"

"Byte-identical across embedded/SQLite/Postgres for every `Cond`/`Query`" is
**provably false**, because the two SQL dialects diverge from EACH OTHER — no
embedded eval can match both at once without a forced choice:

| Divergence | SQLite | Postgres | Current renderer | v1 resolution |
|---|---|---|---|---|
| `ORDER BY <nullable>` | NULLs FIRST (ASC) | NULLs LAST (ASC) | `orderTail` emits no `NULLS FIRST/LAST` (`ref:Store.sky:1304-1309`) — takes each dialect's default → they DIFFER | dialect-aware `orderTail` emits an explicit `NULLS FIRST` (forced) on both; `bluedbEvalCond` orders NULLs first to match |
| `LIKE` case | case-INSENSITIVE for ASCII | case-SENSITIVE | `renderCond` emits bare `LIKE` (`ref:Store.sky:1234-1235`) → they DIFFER | force ONE: emit `LIKE` + a forced collation on SQLite (or document `LIKE` as case-sensitive-ASCII and adjust the SQLite render); `bluedbEvalCond` matches the forced choice |
| int-column vs text literal | dynamic type affinity coerces | stricter typing may error/differ | column resolves to `ColInt`; a text literal is a schema-vs-leaf mismatch (R3-1) | the parity subset requires literals to match the column's declared `ColType`; a genuine type mismatch is rejected identically (schema colType is the single authority, §2.2) |
| empty `inList` | `1 = 0` | `1 = 0` | `renderCond` normalizes `CondIn []` to `("1 = 0", [])` on BOTH (`ref:Store.sky:1250-1251`) | **already parity-clean** — removed from the worry list; `bluedbEvalCond` matches `1=0` (always-false) |

So the gate (§8) is: **byte-identical for the parity subset — equality, non-null
ordering, integer ranges, ASCII `LIKE` under a forced collation, empty `inList` —
with the dialect-divergent shapes pinned to one forced semantics by a dialect-aware
renderer whose forcing `bluedbEvalCond` mirrors.** This is honest and enforceable;
"identical for everything" was neither.

---

## 1. The `Backend` Go interface

### 1.1 The load-bearing prior-art fact — the SQL arm is pure SKY

The prior art dispatches every universal verb in **Sky** via `case conn of`
(`ref:Persist.sky:212-439`): `SqlConn db → Store.*` vs `KvConn store → BlueDB.coll*`.
Crucially, **both arms are Sky above the FFI boundary**: the SQL arm is `Std.Db.Store`
(itself pure Sky — `buildSqlQuery`/`toList`/`upsert` render SQL text in Sky and call
the `Db_query`/`Db_exec` Go kernels), and only the KV arm crosses into a
BlueDB-specific `Ffi.kernel "BlueDB_coll*"`. Only **three** Persist bindings are
themselves kernels: `persistKeyString` (`Persist_keyString`), `publishChangeKernel`
(`Persist_publishChange`), and the compiler-synthesised `liveInto` (`Persist_liveInto`).

This has a decisive consequence for "a Go `Backend` interface that new-engine +
SQLite + Postgres all satisfy": **the Cond→SQL render lives in Sky, not Go.** A
single Go interface whose SQL implementation renders `QueryPlan → SQL` would have to
**re-implement `Store.buildSqlQuery` in Go**, duplicating a proven, tested Sky renderer
— directly against the task's "reuse the proven `Std.Db` + `Cond→SQL`" directive. So
the honest resolution is a **layered** one:

- **The Go `Backend` interface is the EMBEDDED-FAMILY contract** (embedded now,
  cluster later) — the thing the new-engine adapter implements over `bluedb.Engine`.
- **SQLite + Postgres satisfy the *Persist verb contract* through the ported Sky
  `Store` arm** (`case conn of → SqlConn → Store.*`), reusing `buildSqlQuery`
  verbatim — NOT through a Go `Backend` impl. Dialect is a runtime property of the
  `Db` handle, exactly as today (`ref:Persist.sky:196-198`).
- The **phantom tag stays** and the **`case conn of` STAYS in Sky** (PORT). Phase 3's
  only rewrite of the dispatch is that the KV arm's callee — the `BlueDB.coll*`
  kernels — is **rebuilt to route to the new embedded engine** (the Go `Backend`),
  instead of the retired RAM-map kernels.

So: the "one interface embedded+sqlite+postgres all satisfy" is realized at **two
altitudes** — the **Sky `Conn cap` verb contract** (every verb compiles + runs on all
three, the user-visible uniformity) sitting above a **Go `Backend` interface for the
embedded family** (the internal engine contract). This is Design B. Design A (collapse
`case conn of` into one Go interface, re-render Cond→SQL in Go) is preserved as the
grill alternative (§3.3, R3-3) — it gives a literal single Go interface at the cost of
duplicating the SQL renderer and a bigger diff from proven code.

The phantom tag gates two things at compile time, unchanged:

- **Raw-KV escape hatch.** `Std.BlueDB`'s string-KV tier stays a separate surface
  reached only through a KV-capable handle (Decision 5 — raw KV is an explicit escape
  hatch, not a Persist universal verb).

**Reactivity is NOT gated on the `KeyValue` tag — the first draft's claim was
false.** `watchCollection : Collection a -> (Change -> msg) -> Sub msg`
(`ref:Persist.sky:947`) takes **no `Conn`** at all, and `live : Conn cap -> Query
a -> …` (`ref:Persist.sky:979`) is typed on `Conn cap` — ANY backend, its docstring
says "Works on every backend (KV + SQLite + Postgres)". The mechanism is
**backend-agnostic in-process pub/sub**: every SQL write arm ALREADY calls
`publishChangeKernel` exactly as the KV arms do (`ref:Persist.sky:390`, the
`SqlConn` delete arm). So **single-instance `watch` on `sqlite`/`postgres` WORKS
TODAY** — it is not embedded-only. What is embedded-only in v1 is *cross-instance*
reactivity: an in-process broker only reaches subscribers in the SAME process, so a
change written on replica A does not wake a `watch` on replica B unless a
cross-process broker (embedded-commit-path / Redis / Postgres `NOTIFY`) carries it.
The capability check (§5) therefore guards **cross-instance** reactivity, never
single-instance watch — and it is NOT a compile-time `KeyValue`-tag guarantee
(there is no such tag on these bindings). Decision 5's "runtime-loud, no silent
stale" still holds, refocused on the cross-instance case.

### 1.2 The interface (Go-side, embedded-family — package `bluedb` or a thin `rt` shim)

This is the contract the **embedded adapter** implements. It is written so a future
**cluster** adapter satisfies it too. A thin Go SQL adapter MAY also implement it
(Design A) if the grill chooses to re-render Cond→SQL in Go; under the recommended
Design B, the SQL backends satisfy the *Persist verb contract* via the Sky `Store`
arm and never construct a Go `Backend`.

```go
// Backend is the minimal contract every Persist universal verb dispatches to.
// The embedded adapter (over runtime-go/bluedb Engine/Txn), the SQLite adapter,
// and the Postgres adapter each satisfy it. Method set is deliberately minimal:
// exactly the universal verbs, no more. Handle-scoped (one Backend per open conn).
type Backend interface {
    // ---- CRUD (portable, no leak) ----
    // Get resolves the collection's primary key → the stored row (JSON blob),
    // decoded by the caller's codec. Absent → (nil, false).
    Get(coll CollSchema, key string) (row []byte, ok bool, err error)

    // Put upserts a row by its self-assigned primary key (no generated-field fill).
    Put(coll CollSchema, key string, row []byte, cols []ColValue) error

    // Insert inserts and returns the row with GENERATED fields filled — serial PK,
    // defaultNow timestamps, defaultWith app-computed defaults. `row` is the codec
    // JSON of the record with generated columns omitted; the return is the codec
    // JSON of the persisted row (re-read / RETURNING).
    Insert(coll CollSchema, row []byte, cols []ColValue) (filled []byte, err error)

    // Delete removes by primary key. Missing key → nil (idempotent).
    Delete(coll CollSchema, key string) error

    // ---- Query (portable, no leak) ----
    // Query runs a RESOLVED plan (Cond already lowered to leaves + column-resolved,
    // orders, limit, offset) and returns the matching rows as codec JSON blobs, in
    // the plan's order. Same QueryPlan drives every backend (§4).
    Query(coll CollSchema, plan QueryPlan) (rows [][]byte, err error)

    // Count runs the same plan's WHERE and returns the row count.
    Count(coll CollSchema, plan QueryPlan) (int, error)

    // ---- Transaction (portable API; guarantee leaks — see §5) ----
    // Transaction runs a pure body under the backend's serializable transaction:
    //   embedded → Engine.Transact (SSI, Decision 4), bounded retry → ErrConflict
    //   sql      → Db_withSerializableTransaction (pg BeginTx SERIALIZABLE /
    //              sqlite BEGIN IMMEDIATE over a pinned conn), bounded retry →
    //              typed Conflict (code 8), UNIFORM with embedded. NOT the
    //              generic Db.withTransaction (default isolation = READ
    //              COMMITTED on pg), which stays for raw Std.Db users.
    // The body sees a Tx handle exposing txGet/txPut/txDelete/txQuery ONLY.
    // txQuery inside a txn MUST record a read-set (the SSI crux) — an indexable
    // leaf on a declared range-optimized index records a precise Txn.ScanRange;
    // anything else records a Txn.WitnessCollection. Without this a txn-scoped
    // Query is Snapshot Isolation (predicate phantoms commit), not Serializable.
    // See §2.6 — the read-set contract is what makes SSI real at the Query boundary.
    Transaction(fn func(tx TxHandle) error) error

    // ---- Escape hatch (portable) ----
    // SelectRaw runs arbitrary SQL-shaped read (JOIN / GROUP BY / aggregate) and
    // decodes each row into a projection via the caller's codec. On embedded this
    // is the raw-scan+in-RAM-eval fallback for shapes the Cond algebra can't express
    // (§4.4); on SQL it is the driver query verbatim.
    SelectRaw(sql string, params []ColValue) (rows [][]byte, err error)

    // ---- Capability probe (Decision 5) ----
    Capabilities() Capabilities
    Close() error
}

// CrossInstanceReactive is what v1 gates — NOT single-instance watch. Single-instance
// watch works on EVERY backend today via in-process pub/sub (the SQL write arms call
// publishChangeKernel, ref:Persist.sky:390). CrossInstanceReactive is implemented in
// v1 only by the embedded commit-path (+ a future Postgres LISTEN/NOTIFY / Redis
// broker). A backend that does NOT implement it fails the boot check ONLY for an app
// that declares cross-instance reactive bindings (Live.withReactive / liveInto) AND
// is configured to run multi-replica (§5).
type CrossInstanceReactive interface {
    // Watch registers (collection, resolvedCond) and returns a subscription whose
    // channel delivers scoped Changes evaluated in the commit path (L4, Phase 4).
    // Phase 3 defines the SEAM; Phase 4 wires the commit-path evaluation.
    Watch(coll CollSchema, plan QueryPlan) (Subscription, error)
}

type Capabilities struct {
    // InProcessReactive is TRUE on every backend (in-process pub/sub) — the field
    // exists for completeness; single-instance watch never fails the capability check.
    InProcessReactive     bool // always true (KV + sqlite + pg)
    CrossInstanceReactive bool // commit-path / NOTIFY-backed (embedded true; sqlite/pg false in v1)
    SerializableTxn       bool // SSI (embedded) or SERIALIZABLE (pg) or BEGIN IMMEDIATE (sqlite)
    DeterministicTxn      bool // replayable command log (embedded/cluster only)
    Joins                 bool // native JOIN/GROUP BY via SelectRaw+SQL (sqlite/pg true)
}
```

Supporting value types shared across adapters:

```go
// CollSchema is the L0-derived, backend-independent description the adapter needs:
// name, key field/column, the codec's column list + colTypes, the declared indexes,
// the generated-column set. Built once per Collection at connect/first-use (§3.4).
type CollSchema struct {
    Name        string
    Key         string           // primary-key column (snake)
    Cols        []ColSpec        // ordered; name + ColType + generated? + unique?
    Indexes     []IndexSpec      // declared secondary indexes (name, cols, asc/desc)
    Generated   map[string]bool  // columns Insert/Put omit (DB/engine fills)
}
type ColSpec   struct { Name string; Type ColType; Unique bool; Generated bool }
type IndexSpec struct { ID IndexID; Name string; Cols []IndexColSpec; Unique bool }
type IndexColSpec struct { Col string; Type ColType; Desc bool }

// ColValue is one typed, injection-safe bound value — the same currency Store's
// SqlValue already uses on the SQL side (ref:Store.sky SqlValue). Carries its
// ColType so the embedded adapter can feed encodeIndexKey and the SQL adapter can
// bind the driver param.
type ColValue struct { Type ColType; Bytes []byte; Null bool }

// QueryPlan is the RESOLVED query: the Cond tree already column-resolved + lowered
// to leaves, plus orders/limit/offset. Produced by Store.planJson (ref:Store.sky:
// 1054-1194), decoded once per query. Backend-independent (§4).
type QueryPlan struct {
    Where  CondNode      // resolved predicate tree (leaves carry col + ColType + ColValue)
    Orders []OrderSpec   // (col, asc/desc) in priority order
    Limit  int           // -1 = none
    Offset int
}
```

**How each backend satisfies the Persist verb contract** (Design B — the recommended
layering; the "Go `Backend`" column is the embedded-family Go interface, the "SQL arm"
column is the ported Sky `Store` path reached via `case conn of`):

| Persist verb | Embedded — via Go `Backend` | SQLite / Postgres — via Sky `Store` arm |
|---|---|---|
| `get`/`put`/`insert`/`delete` | `Backend.Get/Put/Insert/Delete` over `Engine.Commit` + snapshot read | `Store.getByKey`/`upsert`/`insert`/`deleteByKey` (`ref:Persist.sky:217,231,261,387`) |
| `toList`/`toCount` (`query`) | `Backend.Query/Count` — ordered scan + in-RAM `bluedbEvalCond` (§4.4) | `Store.toList/count (buildSqlQuery …)` (`ref:Persist.sky:718,737`) |
| `transaction` | `Backend.Transaction` → `Engine.Transact` — **SSI**, bounded retry → typed `Conflict` (code 8) | `sqlSerializableTransaction` → `Db_withSerializableTransaction` (`rt/db_auth.go`): pg `BeginTx SERIALIZABLE`, sqlite `BEGIN IMMEDIATE` (pinned conn) + bounded retry → typed `Conflict` (code 8). The generic `Db.withTransaction` (default isolation) is NOT used here — it stays for raw `Std.Db` users. |
| `selectRaw` | `Backend.SelectRaw` — raw scan + in-RAM eval | `Store.selectRaw` (driver query) |
| `watch`/`live` (single-instance) | in-process pub/sub — works today | ✅ in-process pub/sub — `publishChangeKernel` on the SQL write arms (`ref:Persist.sky:390`) — works today |
| `watch`/`live` (CROSS-instance) | `Backend.(CrossInstanceReactive).Watch` — seam here; commit-path eval = Phase 4 | ❌ in v1 — boot-fatal ONLY for a multi-replica app declaring `Live.withReactive`/`liveInto` (§5); LISTEN/NOTIFY = post-v1 |

`Capabilities()` is reported per backend: embedded `{InProcessReactive,
CrossInstanceReactive, SerializableTxn, DeterministicTxn}`; sqlite
`{InProcessReactive, SerializableTxn}`; postgres `{InProcessReactive,
SerializableTxn, Joins}`. **Every** backend has `InProcessReactive` — single-instance
watch never trips the check. `CrossInstanceReactive` is a **separate Go interface**
the embedded adapter implements and the SQL path does not (in v1) — so the check is an
explicit `backend.(CrossInstanceReactive)` assertion (§5), never a silent nil-method
call, and it fires ONLY when the app both declares a boot-visible reactive binding
(`Live.withReactive`/`liveInto`) AND is configured multi-replica. A single-replica SQL
app with `watchCollection` in `subscriptions` is fully supported and never boot-fatal.

---

## 2. The embedded adapter + THE REAL codec-driven indexer (the crux)

This is where **L0 (codec/schema) connects to L2 (SSI)**. Phase 2 shipped the SSI
core with a **stubbed** indexer; Phase 3 makes it real.

### 2.1 What Phase 2 left as the seam

- `Txn.indexer func(userKey, record []byte) []IndexCoord` (`txn.go:62`) — nil by
  default (Phase-1 blind path). Installed via `Txn.SetIndexer(fn)` (`txn.go:95`);
  the owning collection id via `Txn.SetCollection(coll CollID)` (`txn.go:99`).
- The indexer is called through the single private helper `indexCoords`
  (`txn.go:217`) on **Put** (`txn.go:174` → `bw.newIndex`), on the **pre-image** for
  update/delete OldIndex (`txn.go:213` → `bw.oldIndex`), and during **scan
  materialization** (`txn.go:576`). Its output flows into `KeyChange.NewIndex`/
  `OldIndex` at `buildReq` (`txn.go:246-253`).
- `encodeIndexKey(indexID IndexID, colType ColType, value []byte) []byte`
  (`index_key.go:57`) is the SOLE producer of index-coordinate bytes; direction
  rides in the `ColType` high bit (`colDescendingFlag = 0x80`, `Descending(c)` at
  `index_key.go:29`). `ColType`: `ColInt=1`/`ColText=2`/`ColBool=3` are
  range-optimized; `ColReal=4`/`ColMoney=5`/`ColBlob=6` fall back to the
  conservative witness (`index_key.go:15-24`).
- `Txn.ScanRange(index IndexID, colType ColType, loVal, hiVal []byte) Cursor`
  (`txn.go:145`) builds its `[lo,hi)` bounds via `encodeScanRange` (`index_key.go:109`),
  which calls the **same** `encodeIndexKey` — so a scan bound and a change coord
  can never drift (Phase-2 grill R-2.1). `Txn.Scan(index, lo, hi)` (`txn.go:133`) is
  the pre-encoded low-level form; `ScanFallback(index, match)` (`txn.go:154`) is the
  conservative-witness form for unsupported colTypes.

**The Phase-2 test supplied a trivial indexer.** Phase 3 supplies the REAL one from
L0.

**Correction to the first draft: it is NOT true that "nothing in the engine
changes."** Installing the real indexer is a pure install, yes — but the Phase-2
`Txn` is **single-collection**, and a Persist `transaction` is inherently
**multi-collection** (the whole point: atomically write `orders` AND `inventory`).
The Phase-2 `Txn` carries ONE `indexer` + ONE `coll CollID` (`txn.go:62-63`), and
`buildReq` stamps EVERY emitted `KeyChange` with that single `tx.coll`
(`txn.go:246-253`, `Coll: tx.coll`). If a transaction writes two collections, BOTH
changes are stamped with whichever collection was `SetCollection`'d last → a
concurrent `WitnessCollection(orders)` reader (which the validator matches via
`rs.collWitness[ch.Coll]`, `validate.go:41`) MISSES the order-write (mis-stamped
`inventory`) → **under-reject** (a phantom commits); and Phase-4 reactivity keys
off `ch.Coll` → the wrong subscription wakes. A unified ACID DB MUST support
multi-collection transactions, so this is a **required, bounded engine change** (no
key-format change):

- **Derive `Coll` per-change from the userKey prefix.** The data key is
  `userKey = collName ‖ 0x1F ‖ pk` (§2.4). `buildReq` already has each write's `uk`
  in hand (`txn.go:242-245`); change the `KeyChange{Coll: tx.coll, …}` at
  `txn.go:246-253` to `KeyChange{Coll: tx.collOf(uk), …}`, where `collOf` splits the
  `0x1F`-delimited prefix and maps `collName → CollID` via a resolver the adapter
  installs.
- **Carry a per-collection indexer.** Replace the single `SetCollection(coll
  CollID)` (`txn.go:99`) with a resolver install — either a
  `map[string]CollID` + a `collOf(userKey) CollID` method, or (cleaner) a single
  `collResolver func(userKey []byte) (CollID, indexerFn)` the adapter builds over
  the embedded registry's per-collection `CollSchema` (§3.4). `SetIndexer`'s
  closure signature (`func(userKey, record) []IndexCoord`, `txn.go:95`) is unchanged
  because the userKey already namespaces the collection — the installed closure
  parses `collName` from the userKey, looks up that collection's `CollSchema`, and
  emits its coords. So `Put`/`Delete`/pre-image (`indexCoords`, `txn.go:217`) each
  get the RIGHT collection's coords with no signature churn.
- **The fallback witnesses stay id-keyed.** `WitnessCollection(coll)` /
  `ScanFallback(index, …)` already take explicit ids (`txn.go:154,162`), so the read
  side witnesses the correct `CollID`/`IndexID` per collection; with `buildReq` now
  stamping the write's `Coll` correctly, `validate.go:41,60` matches soundly.

Net: one focused change to `Txn`'s collection field + `buildReq`'s `Coll` stamping
+ the `SetCollection`→resolver install. The commit/validation format, the changelog
payload, and `encodeIndexKey` are **untouched**.

### 2.2 From `Collection` (L0) to the installed indexer

A `Collection a` (`ref:Persist.sky:124-128`) carries `Store a` (→ `Codec a`) + the
declared `indexes : List String`. The embedded adapter derives, once per collection
(§3.4), a `CollSchema` and from it **two coordinated closures built from the same
`IndexSpec` list**:

1. **The indexer** installed on every `Txn`:

```go
// buildIndexer produces the closure Txn.SetIndexer installs. For a put/pre-image
// it decodes the record's indexed columns and emits ONE IndexCoord per declared
// index, each Key produced by the CANONICAL encodeIndexKey. Reused verbatim as the
// scan-bound encoder's sibling — same schema, same encoder, no drift.
func buildIndexer(cs CollSchema) func(userKey, record []byte) []IndexCoord {
    return func(userKey, record []byte) []IndexCoord {
        cols := decodeIndexedColumns(cs, record) // codec → map[col]ColValue (§2.3)
        out := make([]IndexCoord, 0, len(cs.Indexes))
        for _, idx := range cs.Indexes {
            key := encodeIndexEntry(idx, cols)   // §2.4 — single-col or composite
            out = append(out, IndexCoord{Index: idx.ID, Key: key})
        }
        return out
    }
}

// encodeIndexEntry — the ONE place the schema meets the encoder. v1: single-column
// ascending only → encodeIndexKey(idx.ID, colTypeOf(idx, cols), value), where
// colTypeOf routes Money/Codec.map/unresolved to a fallback ColType (§2.3). The
// composite (encodeCompositeKey, index_key.go:161) + descending (colDescendingFlag,
// Descending(), index_key.go:29) paths EXIST in the engine but are OUT of v1 scope —
// L0 has no builder to declare them (§2.5). Wiring them later moves the composite
// fail-loud guard to Collection-construction time.
func encodeIndexEntry(idx IndexSpec, cols map[string]ColValue) []byte { … }
```

2. **The scan-bound builder** the adapter's `Query` uses to turn a `Cond` range
leaf on an indexed column into `Txn.ScanRange(idx.ID, colType, loVal, hiVal)` —
which internally calls `encodeScanRange → encodeIndexKey`. **Because both the indexer
and the scan-bound builder resolve `(idx.ID, colType)` from the same `IndexSpec` and
feed the same `encodeIndexKey`, the coord a `Put` emits and the bound a `Scan`
records are byte-identical by construction** — the R-2.1 single-encoder guarantee
holds end-to-end at the L3 boundary, not just inside Phase-2's unit tests.

### 2.3 Record ↔ row (column) mapping — the codec is the mapper

The adapter never hand-writes a struct mapper. The **codec IS the schema**
(`ref:Codec.sky` — `Shape = SRecord (List (String, ColType)) | SScalar | SBlob`,
`ref:Codec.sky:79`; `Store.colsOf` already surfaces the column list,
`ref:Store.sky`). Two directions:

- **Record → columns (for indexer + Put params).** The stored value is the codec
  JSON blob (`Codec.toJson` — already produced Sky-side at the `put` call,
  `ref:Persist.sky:242`). The adapter needs only the **indexed + PK columns** as
  `ColValue`s. It gets them from the `ColValue` list the Sky verb already threads
  (the current KV arm passes `indexFieldValues coll record` + `Store.colsOf`,
  `ref:Persist.sky:243-244`) — **ADAPTED** to carry `ColType` so `encodeIndexKey`
  can pick its order-preserving encoding. So the mapping is: Sky computes the
  typed column values from the record via the codec's shape; the adapter encodes
  them. No reflection in the adapter beyond decoding the JSON blob's indexed fields
  on the pre-image path (`decodeIndexedColumns`, §2.2).
- **Columns → record (for reads).** `Query`/`Get` return the stored JSON blob
  verbatim; the Sky verb decodes it with `Codec.fromJson (codecOf coll)`
  (mirrors the current `BlueDB.collGetValue (codecOf coll) …`,
  `ref:Persist.sky:220`). So the row's canonical form on the embedded engine is
  **the codec JSON blob**, with the indexed/PK columns *also* materialized as index
  entries + the PK key. This is the same "JSON blob + scalar index columns" layout
  the prior collection kernel used (`ref:bluedb_collection_kernel.go`), now backed
  by the ordered engine instead of a map.

**The colType mapping (the one place two type vocabularies meet) — and the Money/
`Codec.map` erasure hole the grill found.** The codec declares
`type ColType = CText | CInt | CReal | CBool | CBlob | CNull ColType`
(`ref:Codec.sky:69-75`) — note there is **NO `CMoney`/`CDecimal`**. Today the KV
plumbing flattens it to a **string** via `colTypeKind`
(`ref:Codec.sky:349-369` → `"text"`/`"int"`/`"real"`/`"bool"`/`"blob"`, `"?"`-suffixed
for `CNull`) and threads that string in the `indexFieldValues` triples
(`ref:Persist.sky:309-321`). The engine's encoder wants `bluedb.ColType`
(`ColInt=1`/`ColText=2`/`ColBool=3` range-optimized; `ColReal=4`/`ColMoney=5`/
`ColBlob=6` fallback, `index_key.go:15-24`). The adapter maps **codec `ColType` →
engine `ColType`** ONCE per column in `CollSchema` derivation. The naïve map
(`CInt→ColInt`, `CText→ColText`, `CBool→ColBool`, `CReal→ColReal`, `CBlob→ColBlob`) is
where the **sharpest bug** lives:

> **`Money`-as-text has shape `SScalar CText`, NOT a Money-tagged shape.** A Money
> column is a `Codec.map to from Codec.string` newtype wrapper (or `Store.sqlOf` over
> such a codec). But `Codec.map` **preserves the inner shape**: `map to from (Codec c)
> = Codec { …, shp = c.shp }` (`ref:Codec.sky:196-198`). So `Money`-as-text has shape
> `SScalar CText` → `colTypeFor` returns `"text"` → the naïve map picks `ColText`
> (RANGE-OPTIMIZED). But the lexical byte order of `"USD 100.00"` is NOT the numeric
> order of the amount — so `orderAsc "price"` sorts wrong (a **parity break** vs the
> SQL arm, which orders the stored value per its column affinity) AND a range
> `Scan(price in [lo,hi])` under-rejects. **The claimed `ColMoney` fallback has NO
> trigger** — nothing ever produces a `ColMoney` from the codec, because the codec
> has no Money shape to key off.

**Fix (a small `Std.Codec` change): add a non-order-preserving marker.** The codec is
the single source of truth for a column's DB mapping, so the marker belongs there —
NOT in ad-hoc adapter guessing. Two acceptable shapes (pick one at implementation):

- a new nullary `ColType` case (e.g. `CNotOrderable`, or a `CMoney`) that
  `Store.sqlOf`/the Money/`Decimal` codecs and any explicit "opaque text" codec emit;
  OR
- a "not-orderable" bit on `Shape`/`Codec` that **`Codec.map` SETS** — since a `map`
  over a scalar produces a value whose *textual* order is unknown to the codec, the
  conservative default for ANY `Codec.map`-wrapped column is not-orderable.

Either way, `colTypeFor` (`ref:Persist.sky:334`) and the `Shape`→`CollSchema.Cols`
derivation route any indexed `Codec.map` / non-primitive-backed column to the engine's
**conservative fallback** `ColType` (real/money/blob class) → the indexer emits a
coord but the byte-range test is never applied; validation uses the collection/index
witness (§2.6). **Also fix `colTypeFor`'s `Nothing -> "text"` default**
(`ref:Persist.sky:347-348`, and the `_ -> "text"` at `:351`): an index field NOT
resolved in the codec's shape (a scalar/blob codec, or a typo) must route to the
**fallback**, NOT to range-optimized text — an unresolved field whose real domain is
unknown must never be validated by a tight byte-range that could under-reject. So the
mapping is: primitive `CInt`/`CText`/`CBool` with a directly-order-preserving shape →
range-optimized engine `ColType`; `CReal`/`CBlob` / any `Codec.map`-wrapped /
Money/`Decimal` / unresolved → the fallback engine `ColType`; `CNull inner` → `inner`'s
mapping + a NULL-handling flag (and see IS-NULL routing below). This map is the SINGLE
authority both the indexer (write) and the scan-bound builder (query) read.

**IS-NULL / IS-NOT-NULL predicates → fallback witness (wired, not just stated).**
A `Nothing` value emits **no index coordinate** — `Codec.maybe`'s `Nothing` encodes as
JSON `null` (`ref:Codec.sky:173-174`), the codec's `CNull` shape carries the base type,
and the indexer skips a NULL (no value to encode). So an `isNull` predicate evaluated
as an index RANGE would MISS a concurrent `insert {field = Nothing}` → **under-reject**.
Therefore an `isNull`/`notNull` predicate MUST route to `Txn.WitnessCollection` (or a
point read-set of the actual rows), **never** to a `Txn.ScanRange`. This is wired in
the §2.6 Query-lowering: an `isNull`/`notNull` leaf is classified as non-range and
forces the collection witness for the scanned collection.

**Fidelity contract (grill target, §9 R3-1).** The colType a column is indexed under
MUST match the codec's declared mapping for that field, and the value bytes fed to
`encodeIndexKey` MUST be the same normalized encoding on write (Put's `newIndex`),
pre-image (`OldIndex`), and query (ScanRange's bound). The adapter derives ALL from one
`CollSchema.Cols` entry (which carries the mapped engine `ColType`, incl. the
not-orderable marker) — a single source — so a drift is a schema-construction bug,
caught by the parity gate (§8) and a dedicated encode-identity property test (§2.5).

### 2.4 Storage layout on the engine (data key + index entries)

The embedded engine stores **one versioned data key per record** and **secondary
indexes are validation coordinates, not (yet) stored index keys**:

- **Data key.** `userKey = collName ‖ 0x1F ‖ pk` (collection-namespaced, so one
  store holds many isolated collections — the current `collNameOf` namespacing,
  `ref:Persist.sky:180-184`). The value is the codec JSON blob. Written via
  `Engine.Commit(CommitReq{Writes: [{UserKey, OpPut, blob}], …})` for a blind put,
  or buffered via `Txn.Put` inside a transaction. **The `collName` prefix is
  load-bearing beyond namespacing: `buildReq` parses it to stamp each `KeyChange.Coll`
  PER-CHANGE (§2.1), so a multi-collection transaction attributes each write to its own
  collection.**
- **Unique-index point keys (STORED, in 3a).** A `unique` column materializes a stored
  point key `collName ‖ 0x1E ‖ indexName ‖ 0x1F ‖ encodeIndexKey(value) → pk` that
  inserts/updates read-then-write (§2.7). This is the ONE stored-index keyspace Phase 3a
  ships (bounded, required for `unique` correctness + SQL parity); it is separate from
  and independent of the deferred general secondary-index seek keys below.
- **Index coordinates.** For validation (SSI), the indexer emits `IndexCoord`s that
  ride in the `KeyChange.NewIndex`/`OldIndex` of the changelog payload — they are
  *witnesses for the validator*, not separately-stored keys. **Open decision
  (§9 R3-2): does Phase 3 ALSO materialize stored secondary-index keys** (`0x02 ‖
  indexID ‖ encodeIndexKey ‖ pk → ∅`) so `Query` can do a real O(log n + k) index
  **seek**, or does it keep Phase-2's in-RAM scan-materialize (`txn.go:523`, the
  comment at `txn.go:520-522` flags "Phase 3 is expected to back Scan with real
  secondary-index storage")? **Recommendation:** Phase 3a ships Query over the
  **primary-key ordered scan + in-RAM Cond eval** (correct, reuses `bluedbEvalCond`,
  parity-provable, no new stored keys) and Phase 3b/Phase 4 adds stored index keys
  for the seek fast-path. The parity gate does not require the seek — it requires
  identical *results*. This keeps 3a's surface small and defers the index-storage
  format (a second irreversible-ish key layout) until the reactive path (Phase 4)
  needs the same entries. **This must be grilled** — see R3-2.

### 2.5 Why this makes SSI real (not a stub)

Phase 2 proved: given `tx.indexer` emitting coords through `encodeIndexKey`, and
`Txn.ScanRange` recording ranges through the same encoder, the committer's
`validate()` (`validate.go:27`) catches predicate phantoms — a `WHERE status='open'`
scan records `status_idx[u|open, u|open]`, and a concurrent `INSERT status='open'`
emits a `NewIndex` coord in that range → `coordHit` (`validate.go:57`) → conflict.
**Phase 3's only job is to make `tx.indexer` emit the coords for EVERY declared
index of the collection under transaction** — driven by `CollSchema.Indexes`, using
the same `encodeIndexKey`. A property test (mirroring Phase-2's `index_key_test.go`)
asserts: for every **v1 declared index shape** (single `int`/`text`/`bool`, and the
`real`/`money`/`blob`/not-orderable fallback), the indexer's emitted coord bytes
byte-match the scan-bound bytes `Query` would build for the same value — closing R-2.1
at the L3 boundary. A collection whose indexed column is a fallback colType
(real/money/`Codec.map`/blob/unresolved) installs the **conservative witness** path
(`Txn.ScanFallback` + `WitnessCollection`), so SSI stays correct (coarser, more aborts)
— never a silent under-reject.

**Scope: v1 indexes are SINGLE-COLUMN ASCENDING only.** L0's declared-index builder
is `index : String` (`ref:Persist.sky` `index`/`indexesOf` — a bare field name), which
carries neither a second column nor a direction. So composite and descending indexes
are **out of v1 scope** — the first draft's "covers composite/descending" claim was
vacuous: no L0 surface can even DECLARE them. They are documented FUTURE work needing
NEW L0 builders (e.g. `indexDesc : String` and `indexOn : List (String, Dir)`). The
engine's `encodeCompositeKey` (`index_key.go:161`) + `Descending` flag
(`index_key.go:29`) already exist and are correct, but they are NOT reachable from L0
in v1. **If composite is ever wired**, note that `encodeCompositeKey`'s fixed-width-
prefix guard currently `panic`s at ENCODE time (`checkCompositeLayout`,
`index_key.go:144-164`); that guard MUST move to **Collection-construction** time so a
bad composite layout fails LOUD when the `Collection` is built (a construction error),
never mid-transaction at a runtime encode. The v1 encode-identity property test
therefore covers single-column ascending + the fallback classes only.

### 2.6 The txn-scoped `Query` read-set contract (SSI at the Query boundary — the phantom hole)

**The first draft's phantom hole.** It specified a txn-scoped `Query` as a "PK-ordered
scan + in-RAM `bluedbEvalCond`" with **no read-set recording**. That is Snapshot
Isolation, NOT Serializable: a transaction body doing
`q |> where_ (eq "status" "open") |> toList` concurrent with an
`insert {status="open"}` — each at its own begin-snapshot — sees the other's write as
absent, both validate trivially, both commit → the inserted "open" row is a **phantom**
the scanning txn never saw. The engine has the machinery to catch this (`Txn.Scan`
records an index range as a read-set entry, `txn.go:131-140`; `validate` matches a
concurrent change's `NewIndex`/`OldIndex` coord against it, `validate.go:44-50`), but
only if the L3 Query LOWERING routes into it. So the contract:

**A `Query` evaluated INSIDE a `transaction` body (`txQuery`) MUST record a read-set.**
The resolved `Cond` is decomposed:

- **A single indexable range/equality leaf on a DECLARED, range-optimized index**
  (`eq`/`gt`/`gte`/`lt`/`lte`/a bounded range on an `int`/`text`/`bool` column that
  has a declared secondary index) → route through **`Txn.ScanRange(idx.ID, colType,
  loVal, hiVal)`** (`txn.go:145`). This records the PRECISE index interval as a
  read-set `indexRange`. Any residual predicate the leaf does not cover (a second
  conjunct, a `LIKE` tail) is applied as an **in-RAM filter of the returned rows** —
  sound because `Put` emits `NewIndex` + `OldIndex` coords for ALL declared columns
  (`txn.go:174,213`), so any concurrent insert/update whose row would enter the
  scanned range emits a coord IN that range → `coordHit` → conflict, whether or not it
  also satisfies the residual predicate (over-reject on the residual is safe).
- **ANYTHING ELSE** — an `OR`/nested predicate, a predicate over a NON-declared column,
  a fallback-colType (real/money/`Codec.map`/blob) column, or an `isNull`/`notNull`
  leaf (§2.3) → route through **`Txn.WitnessCollection(collID)`** (`txn.go:159-162`).
  The coarsest safe witness: ANY change to that collection in the window conflicts. The
  scan itself still materializes correctly (PK scan + in-RAM `bluedbEvalCond`); the
  witness is purely the read-set entry that keeps it serializable.

**The HONEST coarse-abort consequence (the first draft hid this).** Under
`WitnessCollection`, **every** write to a collection aborts **every** concurrent
transaction that scanned it — even writes that could not possibly change the scan's
result. A v1 that leans on `WitnessCollection` (because most predicates are OR/nested/
fallback/non-declared) is serializability-**CORRECT but COARSE**: a hot collection with
many concurrent scanning transactions will see high abort/retry rates. **Declared
range-optimized indexes are precisely what turn a coarse collection-witness into a
tight range-witness** — they are the mechanism that makes concurrency non-trivial, not
a mere read speed-up. This is stated out loud so an app author knows: to get real write
concurrency under transactions, declare a range-optimized (`int`/`text`/`bool`) index
on the column your transactional queries filter, and keep those queries to a single
indexable leaf. (An **autocommit** `Query` outside any `transaction` is a single
snapshot read and records no read-set — there is no window to validate against; only
the txn-scoped path needs this contract.)

### 2.7 `unique` enforcement on the embedded engine — via SSI (parity + consistency)

**The gap.** On the SQL arm, a `unique` column is a `UNIQUE` DDL constraint — two
concurrent `insert email='x'` → the DB rejects one. On the embedded engine as first
drafted, index coords are **validation witnesses, not stored unique keys**, so two
concurrent inserts of `email='x'` — each at its own snapshot — **neither reads the
other**, both validate, both commit → a duplicate. That is both a consistency bug and
a **parity break** (SQL rejects one, embedded accepts both).

**Fix: enforce `unique` via SSI — an insert/update READS then WRITES a stored
unique-index point key.** This is the Phase-2 Decision-4 note realized ("the
unique-constraint TOCTOU becomes a read-set entry on the unique-index key"). Unlike the
deferred general secondary-index seek keys (§2.4), the unique-index point key IS stored
in Phase 3a — it is a small, bounded, well-defined keyspace:

- **Keyspace.** A reserved unique key `uniqKey = collName ‖ 0x1E ‖ indexName ‖ 0x1F ‖
  encodeIndexKey(colType, value)`, stored as a normal engine data key (its own
  `userKey`) whose value is the owning row's PK. (`0x1E` distinguishes it from the
  `0x1F`-delimited data keyspace of §2.4 so the two never collide.)
- **Insert wiring.** An `insert` (or an `upsert`/`update` that SETS a `unique` column)
  under the transaction:
  1. `tx.Get(uniqKey)` — **records a point read** of `uniqKey` (`recordPoint`,
     `txn.go:122`). If PRESENT at the txn's snapshot → return a typed unique-violation
     error (app-level, deterministic, mirrors the SQL `UNIQUE` rejection).
  2. `tx.Put(uniqKey, pk)` — **writes** the unique key (buffered; emitted as a
     `KeyChange` with `Pk == uniqKey` at commit).
- **Why two concurrent inserts conflict.** Both `Get(uniqKey)` see it absent at their
  snapshots and both `Put(uniqKey, pk)`. The committer serializes them: the first
  commits (writes `uniqKey`). The second's read-set has a **point read** of `uniqKey`;
  the validation window now contains the first's `KeyChange` with `Pk == uniqKey` →
  `validate` returns a **point conflict** (`validate.go:35-39`) → `ErrConflict` → the
  driver retries the second → on re-run `Get(uniqKey)` now sees it PRESENT → returns
  the unique-violation error. Exactly one insert wins; the loser gets a clean
  unique-violation, matching the SQL arm.
- **Update/delete upkeep.** An update that CHANGES a `unique` value writes the new
  `uniqKey` (read+write as above) AND deletes the old `uniqKey` (`tx.Delete(oldUniqKey)`
  from the pre-image); a delete removes the row's `uniqKey`. An update that does not
  touch the unique column leaves the unique keyspace alone.

This makes `unique` a first-class, SSI-enforced constraint on the embedded engine with
NO new format — it reuses the engine's existing point-read + point-conflict machinery,
and the stored unique keyspace is the minimal stored-key surface Phase 3a must ship
(the general secondary-index seek keys of §2.4 remain deferred).

---

## 3. The Persist Sky port — PORT / ADAPT / NEW

### 3.1 The audit table

| Sky component | ref:file:line | Verdict | Note |
|---|---|---|---|
| `Conn cap` phantom + `Relational`/`KeyValue` tags | `Persist.sky:109-120` | **ADAPT (KV payload only)** | Keep the two-constructor `SqlConn Db \| KvConn <handle>` + tags + mint-only-via-connect. Only the KV constructor's payload changes: `BlueDB.Store` → the new embedded handle (an opaque id into the rebuilt registry, §3.2). SQL constructor unchanged. |
| `connectRelational` / `connectKeyValue` | `Persist.sky:199-207` | **ADAPT** | Same signatures + tags. `connectRelational` unchanged (`Db.connect`). `connectKeyValue` opens the new embedded engine (`bluedb.Engine`) instead of the retired RAM-map `BlueDB.open`, returning the embedded handle. |
| `Collection a` + `collection`/`index`/`key`/`unique` builders | `Persist.sky:124-172` | **PORT (L0 shape) + ADAPT (unique enforcement)** | The L0 shape (`{store, keyField, indexes}`) is exactly right; `index : String` is single-column-ascending only (composite/descending are v1-out, §2.5). `unique` is derived from the codec-driven `Store` — on the SQL arm it is a `UNIQUE` DDL constraint (already enforced); on the embedded arm it is **NEWLY enforced via SSI** (read+write a stored unique-index point key, §2.7) so the two arms agree. |
| Universal verbs `get`/`put`/`insert`/`delete`/`count`/`all` | `Persist.sky:212-439` | **PORT (dispatch) + ADAPT (KV callee)** | The `case conn of` STAYS in Sky — verbatim. The `SqlConn` arm is unchanged (`Store.*`). The `KvConn` arm's callee — `BlueDB.coll*` — is rebuilt to route to the new embedded engine (§3.3). Verb signatures byte-identical. |
| KV-arm colType-tag + `indexFieldValues`/`indexFieldTypes` plumbing | `Persist.sky:307-357,511-633` | **ADAPT→collapse** | The manual `(fieldValuesWithTypes, colTypes)` threading fed the hand-built KV index. On the ordered engine the adapter derives index columns from `CollSchema`; the Sky verb passes typed `ColValue`s once. `findAllByIndexRange`'s "errors on real/blob/money, use SQL" caveat (`:574-580`) → the conservative-witness fallback (§2.5), no longer an error. |
| Query facade `query`/`where_`/`eq`…/`orderAsc`…/`limit`/`toList`/`toMaybe`/`toCount` | `Persist.sky:665-908` | **PORT** | Re-exported `Cond`/`Query` from `Store` (§4). Already the right vocabulary; unchanged. |
| `transaction` + `txGet`/`txPut`/`txDelete` | `Persist.sky` (txn surface) | **ADAPT** | Signature PORTs (`Conn cap -> (Tx cap -> Task Error a) -> Task Error a`). Body routes to `Backend.Transaction`: embedded → `Engine.Transact`; sql → `withTransaction`. The `Tx cap` handle exposes only txn verbs (the purity gate, phase2 §5.3). |
| Reactivity `Change`/`watch`/`watchCollection`/`live`/`liveInto` | `Persist.sky:929-1075` | **PORT (single-instance) + REBUILD (cross-instance, Phase 4)** | Single-instance in-process pub/sub PORTs unchanged — `watchCollection : Collection a` (`:947`, no `Conn`) + `live : Conn cap` (`:979`, any backend), fed by `publishChangeKernel` on both KV and SQL write arms (`:390`); it WORKS on all backends today. These are NOT typed on a `KeyValue` tag (the first draft's claim was wrong). Phase 3 defines the `CrossInstanceReactive.Watch` **seam** only; the `condPlan` carried-but-unused (`:970`) is what Phase 4 wires into the commit path. Phase 3 ships the cross-instance capability check (§5) — which does NOT boot-fatal single-instance watch. |
| `persistKeyString` (reflective PK extractor) | `Persist.sky` → `ref:persist_kernel.go:18` | **PORT** | `Ffi.kernel "Persist_keyString"` — foundation-independent record→key. |

### 3.2 The `Conn` (ADAPT detail — KV payload only)

```elm
-- Before (ref:Persist.sky:118-120): KvConn carries the retired RAM-map handle.
-- type Conn cap = SqlConn Db | KvConn BlueDB.Store

-- After: SQL arm unchanged; KV arm carries the new embedded handle. cap stays phantom.
type Conn cap
    = SqlConn Db
    | KvConn EmbeddedHandle         -- EmbeddedHandle = opaque Int id into the rebuilt registry

connectRelational : () -> Task Error (Conn Relational)      -- unchanged: Task.map SqlConn (Db.connect ())
connectKeyValue   : String -> Task Error (Conn KeyValue)    -- Task.map KvConn (Embedded.open path)
```

The embedded registry (a rebuild of `ref:bluedb_kernel.go:33-370`'s handle table,
ADAPTED to store a `bluedb.Engine`-backed `Backend` not a `*BlueDB`) resolves an
`EmbeddedHandle` → the embedded `Backend`. The SQL arm keeps the `Db` handle exactly as
today. So the phantom `cap` still pins the RAW-KV escape hatch at the mint sites, and
the two arms stay physically distinct. Note this does NOT gate reactivity:
`watchCollection`/`live` are not typed on `KeyValue` (§1.1), so the phantom tag does
not compile-restrict `watch` to the embedded arm — single-instance watch is supported
on every backend, and the cross-instance capability check is runtime, not tag-based
(§5).

### 3.3 The kernel wiring — `case conn of` stays; the KV arm's kernels are rebuilt

**The dispatch stays in Sky (PORT).** Each universal verb keeps its `case conn of`
body verbatim; the `SqlConn` arm is unchanged; the `KvConn` arm's callee — the
`BlueDB.coll*` kernels — is **rebuilt** to route to the embedded `Backend`:

```elm
get : Conn cap -> Collection a -> String -> Task Error (Maybe a)
get conn coll keyStr =
    case conn of
        SqlConn db ->                                   -- UNCHANGED
            Store.getByKey db (storeOf coll) keyStr
        KvConn h ->                                     -- KV arm: callee rebuilt
            Embedded.collGetValue (codecOf coll) h (collNameOf coll) keyStr
```

The KV-arm kernels are `Ffi.kernel "Embedded_coll*"` (or the retained `BlueDB_coll*`
names, rebuilt) — which resolve **generically** to `rt.Embedded_coll*` via
`detect_kernel_alias` (`lower.rs:1124`) → `alias_go_name` (`kernel.rs:26`) → `rt.<Raw>`
(no `hir::KERNEL_FUNCTIONS` entry, §6). Their Go bodies are the NEW embedded adapter:

```go
// runtime-go/rt/persist_embedded.go — the rebuilt KV-arm kernels over bluedb.Engine.
func Embedded_collPut(hArg, collArg, pkArg, jsonArg, fvtArg, colsArg any) any {
    b := embeddedBackendOf(hArg)               // registry → *EmbeddedBackend
    cs := b.schema(asString(collArg), colsArg, fvtArg) // CollSchema (memoized, §3.4)
    return b.Put(cs, asString(pkArg), asBytes(jsonArg), colValuesOf(fvtArg))
    // *EmbeddedBackend satisfies the Go Backend interface (§1.2). Put is a BLIND
    // write → it calls Engine.Commit directly; a blind Commit has NO Txn, so there is
    // no tx.indexer to install. Instead Put computes the coords with buildIndexer(cs)
    // (§2.2) and encodes them into the CommitReq's KeyChange NewIndex/OldIndex
    // (ChangelogPayload) directly. tx.SetIndexer(buildIndexer(cs)) is installed ONLY
    // on the TRANSACTION path (Backend.Transaction → Engine.Transact → per-Txn), where
    // a Txn exists to call it (§2, §2.1's per-collection resolver).
}
```

The KV arm's **wire shape is preserved from the prior art** — `Embedded_collPut` takes
the same `(coll, pk, json, indexFieldValues, cols)` args as `BlueDB_collPut`
(`ref:bluedb_collection_kernel.go:228`), so the Sky verb bodies barely change (the
callee module swaps; the arg-threading of `indexFieldValues`/`Store.colsOf`
is PORTed, then ADAPTED to carry `ColType` for `encodeIndexKey`, §2.3). This keeps the
diff small and reuses the proven Sky verb bodies.

**Where the Go `Backend` interface lives:** `*EmbeddedBackend` (the KV-arm kernels'
receiver) implements the Go `Backend` interface of §1.2. The interface is the
*embedded-family* seam — it exists so a cluster adapter drops in later without changing
the KV-arm kernels — not a per-verb FFI kernel. The SQL arm never constructs a
`Backend`; it stays the Sky `Store` path (§1.1).

**Alternative kept for the grill (R3-3):** Design A — collapse `case conn of` into ONE
constructor `Conn BackendHandle` + thin `Persist_get`/`Persist_query` kernels that
dispatch on a Go `Backend` the handle resolves to, with the SQL `Backend` re-rendering
`Cond→SQL` in Go. This gives a literal single Go interface all three satisfy (the
task's phrasing), at the cost of (a) duplicating the proven `Store.buildSqlQuery`
renderer in Go, (b) losing the "reuse buildSqlQuery" directive, (c) a much larger diff,
and (d) per-call `CollSchema` (de)serialization across the FFI. **Recommended: Design B
above** — it honors "reuse `buildSqlQuery`", keeps the diff small, and still delivers a
Go `Backend` interface for the engine family the new engine belongs to. The grill
decides which framing to ship.

### 3.4 CollSchema derivation + memoization (embedded arm)

The embedded arm's `CollSchema` is built Go-side from the args the KV kernels **already
receive** — `Store.colsOf` (the `(col, kindWithFlags)` list carrying `!`/`u`/`dnow`/
`touch`, `ref:Persist.sky:244`) + `indexFieldValues`/`indexFieldTypes` (the
`(field, value, colType)` triples, `ref:Persist.sky:309-321`, ADAPTED to carry
`ColType`) + `collNameOf`. No new schema-JSON FFI channel is needed; Design B reuses the
proven arg-threading. It is memoized in the embedded registry keyed by
`(handle, collName)` — the **single L0 read** the indexer (§2.2) consumes. (The SQL DDL
stays Sky-side in `Store`, unchanged — Design B does not route DDL through the Go
`Backend`.) The embedded registry also assigns stable `CollID`/`IndexID`s (the
`keychange.go` uint32 ids) so the SSI validator + Phase-4 reactivity name the same
collection/index across restarts.

---

## 4. The SQL adapters (SQLite, Postgres) + parity

### 4.1 The SQL "adapters" ARE the ported Sky `Store` arm (Design B)

Under the recommended layering (§1.1), there is **no new Go SQL adapter** — the SQLite
and Postgres "adapters" are the **`SqlConn` arm of the ported Sky `case conn of`**,
which is already `Std.Db.Store` over `Std.Db`. `Persist.toList (SqlConn db)` is
`Store.toList (buildSqlQuery q)` (`ref:Persist.sky:718`) → the resolved `Cond`/orders/
limit rendered to parameterized SQL in Sky and executed via `Db_query`; `get`/`put`/
`insert`/`delete` are `Store.getByKey`/`upsert`/`insert`/`deleteByKey`
(`ref:Persist.sky:217,231,261,387`). SQLite and
Postgres are one code path parameterized by the `Std.Db` driver — dialect is a runtime
property of the `Db` handle, the same fact that makes the `Relational` tag un-splittable
(`ref:Persist.sky:196-198`). **This is exactly why "reuse `buildSqlQuery`" is honored:
the SQL render is never re-implemented in Go.**

**But two renderers ADAPT (not PORT) to reach even subset parity.** `renderCond`
(`ref:Store.sky:1230`) and `orderTail` (`ref:Store.sky:1290`) are reclassified
**PORT → ADAPT** because they currently emit dialect-DEFAULT SQL that diverges between
SQLite and Postgres (§0.6): `orderTail` emits no `NULLS FIRST/LAST` (`:1304-1309`) so a
nullable `ORDER BY` takes each dialect's opposite default; `renderCond` emits a bare
`LIKE` (`:1234-1235`) whose case-sensitivity differs. To hit the parity subset these
must become **dialect-aware** — emit an explicit `NULLS FIRST` (a forced choice) on
both, and make a `LIKE`-collation decision (force one) — with the matching
`bluedbEvalCond` mirroring the forced semantics. This is a small ADAPT of two Sky
renderers, NOT a Go re-implementation; the rest of the SQL arm PORTs unchanged. (Empty
`inList` is ALREADY parity-clean — `renderCond` normalizes `CondIn []` to `1 = 0` on
both dialects, `:1250-1251` — so it needs no ADAPT.)

(Design A would instead build a Go SQL `Backend` that re-renders `Cond→SQL` in Go — see
§3.3 alternative + R3-3. Recommended: do not.)

### 4.2 The parity claim — one `Cond`/`Query`, two compilers

The `Cond`/`Query` algebra already dual-compiles: to SQL via `Store.buildSqlQuery`
AND to a plan JSON via `Store.planJson`/`condPlanJson` (`ref:Store.sky:1054-1194`)
that the engine evaluates with `bluedbEvalCond` (`ref:bluedb_query_kernel.go:338`,
the PORT jewel). Phase 3 keeps **both compilers** and points the plan-JSON consumer at
the new engine's `Query`. **The dual-compile is inherent, not a Design-B artifact:** an
embedded engine cannot consume SQL text, so a second Cond→plan compiler exists in ANY
design (§0.5). Design B renders the SQL text ONCE in Sky (`buildSqlQuery`, reused);
Design A would re-render it a THIRD time in Go (pure drift). So B ships two compilers,
A three — A does not help parity and B does not hurt it. The parity gate (§8) proves
the two B compilers produce identical results **for the documented subset** (§0.6) —
NOT for every `Cond`/`Query`, because the two SQL dialects diverge from each other and
no single embedded eval can match both without the forced-semantics ADAPT of §4.1.
Column resolution (record-field↔snake-column via `camelToSnake`), injection-safe
binding, and `guardCols` (`ref:Store.sky:991,1201`) are backend-independent and run
once, above the split — so a typo fails fast identically on both arms.

### 4.3 Codec-driven schema on the SQL arm too

`serial`/`unique`/`defaultNow`/`touchOnUpdate`/`generated` are already enforced on the
SQL arm from the one `Store` declaration (`ref:Store.sky` schema builders +
`ref:Codec.sky` shape). The SQL adapter's `Insert` uses `insertFieldsReturning`
(`RETURNING` on pg / SQLite ≥ 3.35) to fill generated fields — matching the embedded
adapter's `Insert` return contract. So `Persist.insert` returns a row with the serial
PK + `defaultNow` filled on **all three** backends (parity requirement).

### 4.4 SelectRaw + joins

`SelectRaw` (JOIN / GROUP BY / aggregate) is SQL-native on the SQL adapters (driver
query + codec-decoded projection — `Store.selectRaw`, `ref:Store.sky`). On the
embedded adapter, `SelectRaw` is the **raw-scan escape hatch**: it cannot do a real
SQL JOIN, so v1 supports the single-collection filtered/projected read (raw scan +
in-RAM `bluedbEvalCond`) and **documents joins/aggregates as SQL-only** (Decision 5 —
not pretended portable). The parity gate exercises `selectRaw` on a shape both arms
support (single-collection filter/project); cross-collection JOIN parity is explicitly
**out of scope** (SQL-only, honest per Decision 5).

---

## 5. The capability check (Decision 5 — runtime-loud, CROSS-INSTANCE only)

**What is NOT gated: single-instance watch.** `watch`/`live` work on EVERY backend
single-instance, via in-process pub/sub (`publishChangeKernel` on both KV and SQL write
arms, `ref:Persist.sky:390`). A single-replica app with `watchCollection` on `sqlite`
is fully supported — **do NOT boot-fatal it** (the first draft's "watch is
embedded-only, boot-fatal on `Relational`" would have DELETED a working feature). The
check exists ONLY for **cross-instance** reactivity: an in-process broker reaches only
subscribers in the SAME process, so a multi-replica app needs a cross-process broker
(embedded-commit-path / Redis / Postgres `NOTIFY`) or a change on replica A silently
fails to wake a `watch` on replica B.

**There is no compile-time `KeyValue`-tag guarantee** — `watchCollection : Collection
a` / `live : Conn cap` are not typed on a capability tag (§1.1). Decision 5's
runtime-loud "never a silent stale" therefore applies to the cross-instance case, and
it must be split BY BINDING KIND because the two kinds differ in boot-visibility:

1. **Boot-visible bindings → hard-fatal AT BOOT.** `Live.withReactive
   reactiveQueries` and `liveInto` are declared on the app config at construction, so
   the runtime KNOWS at startup that the app has cross-instance-reactive bindings. If
   the app is configured **multi-replica** (`[data] backend` is a shared store AND the
   deploy declares >1 replica) and the wired backend does NOT implement
   `CrossInstanceReactive` (§1.2), it is a **hard fatal at boot** — `Log.error` +
   `os.Exit(1)` — with the exact message ("app declares `Live.withReactive` but
   backend=sqlite can't do cross-process reactivity across replicas — use the embedded
   commit-path, a Redis broker, or Postgres NOTIFY"). NEVER a silent stale. A
   single-replica deploy of the same app boots clean (in-process broker suffices).
2. **`watchCollection` in `subscriptions` → NOT boot-visible.** A `subscriptions`
   binding is evaluated per-Model at runtime, so its `watchCollection` use is not
   necessarily known at boot. The strongest guarantee here is **hard-fatal at first
   subscription** (when the runtime first evaluates a `watchCollection` on a
   multi-replica non-`CrossInstanceReactive` backend) — still loud, still no silent
   stale, but "at first subscription", NOT "at boot". Document this asymmetry rather
   than over-promise a boot check the binding shape can't support.
3. **CI / deploy PREFLIGHT.** `sky doctor` (and the SkyDeploy preflight) boots with the
   target `[data]` + replica config and runs the `backend.(CrossInstanceReactive)`
   assertion for the boot-visible bindings BEFORE production, catching the mismatch in
   CI. (It cannot preflight a `watchCollection` that only appears under a runtime Model
   branch — that is the boot-visibility gap (2) covers.)

**Optional compiler WARN (DX, not correctness).** A Sky-compiler lint MAY cross-check
`Live.withReactive`/`liveInto`/`watchCollection` call-sites against `[data] backend` +
replica count in `sky.toml` and warn on a multi-replica non-reactive backend. This is a
nicety layered ON TOP of the runtime checks (which carry the guarantee), and is gated by
whether reading `sky.toml` at compile time is in scope (§6, R5-2).

`transaction` guarantee leak (SSI everywhere; deterministic-replay embedded/cluster
only) and joins (SQL-only) are documented, not gated — same API, documented guarantee
difference (Decision 5).

---

## 6. Compiler / stdlib touch-points (minimal)

**No `hir::KERNEL_FUNCTIONS` change, no `kernel_api.rs` change.** Confirmed against
the primary Rust compiler:

- The rebuilt KV-arm verbs (`Ffi.kernel "Embedded_collPut"`, …) — like the retired
  `BlueDB_*` and the retained `Persist_keyString`/`Persist_publishChange` — resolve
  **generically**: `detect_kernel_alias` (`rust/crates/lower/src/lower.rs:1124`)
  detects the `name = Ffi.kernel "Raw"` body → `alias_go_name(raw)`
  (`rust/crates/lower/src/kernel.rs:26-33`) → `rt.<Raw>` (the fallthrough at
  `kernel.rs:32`, since `Embedded`/`Persist`/`BlueDB` are not in the special
  `kernel_go_name_opt` map). So a rebuilt `Embedded_collPut` verb needs only (a) the
  Sky `.sky` alias declaration and (b) the exported Go `func Embedded_collPut(args
  ...any) any`. Verified: the prior art ships 32 `Persist_*`/`BlueDB_*` kernels this way
  with **zero** hir/kernel_api entries (`ref:grep func Persist_/func BlueDB_`), so
  renaming/re-homing the KV kernels onto the new engine needs **no compiler change**.
  (If Design A is chosen instead, its new `Persist_get`/`Persist_query` kernels resolve
  identically — still no hir change.)
- **`sky doc` needs no `kernel_api.rs` entry** because `Std.Persist` is a real `.sky`
  file — `sky doc` reads its signatures + `-- |` summaries from source. `kernel_api.rs`
  is only for kernel-ONLY modules (no `.sky`), which Persist is not.
- **The one compile-time addition** (optional, §5 "Optional compiler WARN"): the
  cross-instance-reactivity lint. If accepted, it is a small check in the Sky compiler
  that reads `sky.toml`'s `[data] backend` + replica count and warns on a
  `Live.withReactive`/`liveInto`/`watchCollection` call-site paired with a multi-replica
  non-`CrossInstanceReactive` backend. This is NOT required for correctness (the
  runtime boot/first-subscription checks + `sky doctor` preflight carry the guarantee);
  it is a DX nicety. It gates NOTHING about single-instance watch (which is always
  fine). **Grill: is reading `sky.toml` at compile time in scope, or is this a
  `sky doctor`-only check?** (§9 R6-1).

**Where the new engine plugs into the runtime.** `runtime-go/rt` does **not** yet
import `runtime-go/bluedb` (verified — no `bluedb` import in `rt/*.go`). Phase 3 adds
that import: the embedded adapter (a new `rt/persist_embedded.go` or a
`bluedb`-subpackage adapter) constructs a `bluedb.Engine` (via its pebble constructor)
and wraps it as a `Backend`. The Go module is `sky-app` (single module), so
`rt` importing `bluedb` is a normal intra-module import — no go.mod change. The
embedded adapter is the ONLY new code that touches the engine's Go API
(`Engine.Commit`/`Snapshot`/`Transact`/`Begin` + `Txn.SetIndexer`/`SetCollection`/
`ScanRange`).

---

## 7. Phased sub-plan

Strict ordering; each sub-phase is shippable + grillable.

### Phase 3a — `Backend` + embedded adapter + REAL indexer + Persist-on-embedded + KV parity subset

- **Build:**
  1. The Go `Backend` interface + value types (§1.2) + the handle registry (ADAPT of
     `ref:bluedb_kernel.go:33-370`).
  2. **The bounded engine `Txn`/`buildReq` change (§2.1)** — per-change `Coll` derived
     from the userKey prefix + a per-collection indexer resolver replacing the single
     `SetCollection`. This is a `runtime-go/bluedb` change (no key-format change); land
     it FIRST so multi-collection transactions are sound before the adapter drives them.
  3. The **embedded adapter** over `bluedb.Engine`: `Get`/`Put`/`Insert`/`Delete`
     (blind writes + snapshot reads), `Query`/`Count` (PK ordered scan + in-RAM
     `bluedbEvalCond`, §2.4 recommendation + the §2.6 read-set contract for the
     txn-scoped path),
     `Transaction` (`Engine.Transact`), `SelectRaw` (raw-scan fallback).
  4. **The real codec-driven indexer** (§2) — `buildIndexer` from `CollSchema`
     (single-column-ascending + the Money/`Codec.map`/unresolved → fallback routing of
     §2.3), installed via `Txn.SetIndexer` + the per-collection resolver; the
     `encodeIndexEntry` sharing `encodeIndexKey` with the scan-bound builder; the
     encode-identity property test.
  5. **The txn-scoped `Query` read-set contract (§2.6)** — Cond decomposition:
     indexable range/eq leaf on a declared range-optimized index → `Txn.ScanRange` +
     residual in-RAM filter; else (OR/nested/non-declared/fallback/**IS-NULL**, §2.3) →
     `Txn.WitnessCollection`. The `Std.Codec` non-order-preserving marker (§2.3) + the
     `colTypeFor` fallback-default fix land here.
  6. **`unique` enforcement via SSI (§2.7)** — the stored unique-index point keyspace;
     insert/update read+write the point key; the concurrent-duplicate negative test.
  7. The Persist Sky port on the embedded arm: `KvConn` payload swap (§3.2), the
     rebuilt `Embedded_coll*` KV-arm kernels over the `Backend` (§3.3), `CollSchema`
     derivation + memoization (§3.4). The `case conn of` dispatch + verb signatures
     are PORTed verbatim.
  8. **KV parity subset** — the `examples/55` probes that BOTH arms already agree on
     (equality, non-null ordering, integer ranges) run KV-vs-KV consistency + the SSI
     suite; the full SQL≡KV subset gate is 3b (needs the SQL arm + dialect-aware
     renderer).
- **Success:** a Persist app on `connectKeyValue` does `get/put/insert/delete/query`
  correctly on the new engine; a multi-collection transaction stamps each write with its
  OWN collection and a `WitnessCollection` on either collection conflicts correctly; a
  txn-scoped `Query` records a read-set (range on a declared index; collection witness
  otherwise) and a predicate phantom is REJECTED (not committed); an `isNull` predicate
  and a `Money`/`Codec.map` indexed column route to the fallback witness (never a
  range); two concurrent `insert email='x'` → exactly one commits, the other gets a
  unique-violation; the encode-identity property test green for every **v1** index shape
  (single int/text/bool + the fallback classes — composite/descending are v1-out, §2.5).
- **Reuse:** `bluedb` engine (Phases 1–2, whole); `bluedbEvalCond`
  (`ref:bluedb_query_kernel.go:338`, PORT); `Persist_keyString`
  (`ref:persist_kernel.go:18`, PORT); `Codec`/`Store` codec-as-schema (PORT, + the
  small `Std.Codec` non-order-preserving-marker ADAPT); `Store.planJson`/`condPlanJson`
  (`ref:Store.sky:1054-1194`, ADAPT consumer).

### Phase 3b — SQL adapters + dialect-aware renderer + forced-subset parity + capability check

- **Build:**
  1. The SQL arm of the ported `case conn of` (Design B — NO new Go SQL `Backend`, §4.1)
     over `Std.Db`/`Store`. `get`/`put`/`insert`/`delete`/`query`/`count`/`selectRaw`/
     `transaction` via the existing SQL surface; codec-driven schema (serial/unique/
     defaultNow/touchOnUpdate/generated) with `insertFieldsReturning` for `Insert` fill.
  2. **The dialect-aware renderer ADAPT (§4.1)** — `renderCond`/`orderTail`
     (`ref:Store.sky:1230,1290`) emit forced `NULLS FIRST` + a forced `LIKE` collation;
     the matching `bluedbEvalCond` mirrors the forced semantics so the embedded arm
     agrees with the forced SQL.
  3. Route `connectRelational` → the SQL arm; the unified `Conn` verb contract now backs
     both arms.
  4. The **cross-instance capability check** (§5): `Capabilities()` per backend; the
     boot-visible-binding boot hard-fatal + the `watchCollection` first-subscription
     fatal; the `sky doctor` preflight; (optional) the compiler warn. Single-instance
     watch is NEVER gated.
- **Success:** `examples/55-persist-query`'s SQL≡KV parity gate green on the new engine
  for the **documented subset** (§0.6, §8) — `get/put/insert/delete/query`
  byte-identical across embedded/SQLite/Postgres for equality / non-null ordering /
  integer ranges / ASCII `LIKE` under the forced collation; the dialect-divergent probes
  (nullable `ORDER BY`, `LIKE` case) match under the forced semantics; `selectRaw` works;
  a single-replica `watch`-on-`sqlite` app boots CLEAN; a multi-replica app declaring
  `Live.withReactive` on a non-`CrossInstanceReactive` backend **hard-fails at boot**
  with the exact message (negative test).
- **Reuse:** `Std.Db`/`Store` SQL arm (PORT, + the `renderCond`/`orderTail` ADAPT);
  `buildSqlQuery`/`selectRaw`/`insertFieldsReturning` (`ref:Store.sky`, PORT);
  `Std.Db.Schema`/`Decode`/`Migrate` (PORT).

**Deferred to Phase 4 (explicit, not a Phase-3 gap):** `CrossInstanceReactive.Watch`'s
commit-path evaluation (the seam ships in 3a; the promotion of
`bluedbChangeAffectsQuery`/`bluedbQuerySub` into the commit path is Phase 4); stored
GENERAL secondary-index seek keys for the O(log n + k) seek fast-path (§2.4 — Phase 3
uses PK-scan + in-RAM eval + the read-set contract, correct + parity-provable; the seek
is a Phase 3b/4 optimization once the index-key layout is locked alongside the reactive
entries). NOTE: the STORED unique-index point keys (§2.7) are NOT deferred — they ship
in 3a because `unique` correctness + SQL parity require them.

---

## 8. The SQL≡KV parity test plan

**What "byte-identical for the SUBSET" means.** `examples/55-persist-query` runs the
SAME Sky `Cond`/`Query`/CRUD source against two conns and prints labelled output:
`Persist.connectKeyValue "…" |> runBackend "KV "` then `connectRelational () |>
runBackend "SQL"` (`ref:examples/55-persist-query/src/Main.sky:140-142`). Parity =
the `KV ` lines and the `SQL` lines are **identical after stripping the label prefix**
FOR THE DOCUMENTED SUBSET (§0.6) — same rows, same order, same counts, for every
subset probe: `count`, an `or_` predicate (`idle | age>50`), a `LIKE %o%`, and an
indexed range (`20<age<40 asc`) (`ref:Main.sky:90-135`). The gate is a diff of the two
label-stripped streams == empty. Dialect-divergent probes (below) are asserted equal
ONLY under the forced semantics the §4.1 renderer + `bluedbEvalCond` pin — NOT against
each dialect's raw default (which would never agree).

**The plan:**

1. **Port example 55 onto the new engine.** `connectKeyValue` now opens the embedded
   adapter; `connectRelational` the SQL arm. Run under `sky run`; assert the
   KV/SQL subset streams match (a wrapper script diffs them, as the current gate does).
2. **Extend the probe matrix** to cover the forced-semantics edge cases (§0.6, §9 R4-1),
   each a labelled probe that must diff-clean KV vs SQL *under the forced semantics*:
   NULL ordering (`orderAsc` on a nullable column → forced `NULLS FIRST`; embedded eval
   orders NULLs first to match); `LIKE` case (forced ASCII collation; embedded eval
   matches the forced choice); `inList` empty (`1 = 0` on all three — already clean,
   §0.6); `not_` over `isNull`; and int-column-vs-int-literal ranges (a text literal on
   an int column is a schema/leaf type mismatch, rejected identically, R3-1 — NOT part
   of the "coerces silently" subset). Descending index ranges are OUT of scope (§2.5).
3. **Insert/generated-field parity.** `Persist.insert` returns the row with serial PK
   + `defaultNow` filled; assert the returned record's shape matches across backends
   (the value of `defaultNow` differs by clock, so assert *presence + type*, and
   assert the serial PK is a positive int on both).
4. **SelectRaw parity** on the single-collection filter/project shape both arms
   support (§4.4). Cross-collection JOIN is asserted SQL-only (documented, not gated).
5. **Embedded SSI soundness (the L3-wiring fixes — mandatory, embedded only).** Driven
   by the REAL indexer (not the trivial test indexer):
   - **Predicate phantom (§2.6):** a txn scanning `where_ (eq "status" "open")`
     concurrent with an `insert {status="open"}` → the scanning txn ABORTS (read-set
     recorded); write-skew / lost-update from Phase-2's suite still rejected; blind-write
     fast path unaffected.
   - **Multi-collection txn (§2.1):** a txn writing `orders` + `inventory` stamps each
     `KeyChange` with its OWN collection — a concurrent `WitnessCollection(orders)`
     reader conflicts with the order-write (proves the per-change `Coll` derivation).
   - **IS-NULL / Money fallback (§2.3):** an `isNull` predicate and a `Money`/`Codec.map`
     indexed column route to `WitnessCollection` (verified: a concurrent
     `insert {field=Nothing}` / `insert {price=…}` conflicts; NOT silently missed).
   - **`unique` via SSI (§2.7):** two concurrent `insert email='x'` → exactly ONE
     commits; the other returns a unique-violation (matches the SQL arm's `UNIQUE`
     rejection — a parity assertion too).
6. **Capability negative test (§5).** A single-replica `watch`-on-`sqlite` fixture boots
   CLEAN (single-instance watch is never gated). A multi-replica fixture declaring
   `Live.withReactive` on a non-`CrossInstanceReactive` backend boots to a hard-fatal
   with the exact message; the embedded backend boots clean.

Parity runs in the example sweep (`scripts/example-sweep.sh`) + a dedicated diff
wrapper, per CLAUDE.md's "corpus gates are necessary but not sufficient — a change is
verified only after the full sweep + a real app."

---

## 9. Risks / open questions (post-grill: resolved + residual)

The four SSI-soundness holes + the parity-honesty + reactivity items are RESOLVED in
the design (§0.5, §2.1/§2.3/§2.6/§2.7, §0.6, §5) — they are mandatory pre-implementation
work, no longer open questions. What remains open is implementation-verification and
perf/scope.

**R3-1 — codec record↔row mapping fidelity (the indexer's correctness). RESOLVED
(design) → verify (impl).** The concrete divergence the grill found is `Money`-as-text
via `Codec.map … Codec.string`: `Codec.map` preserves the inner shape
(`ref:Codec.sky:196-198` `shp = c.shp`), so shape is `SScalar CText` → the naïve map
picks the range-optimized `ColText`, whose lexical order ≠ numeric order → wrong
`orderAsc` + range under-reject. **Fixed** by the `Std.Codec` non-order-preserving
marker + `colTypeFor` fallback-default (§2.3): Money/`Decimal`/`Codec.map`/unresolved
columns route to the conservative fallback `ColType`, never range-optimized. BOTH the
indexer (write) and the scan-bound builder (query) read colType from the one
`CollSchema.Cols` entry, so they cannot disagree; the encode-identity property test
(§2.5) machine-checks byte-match per v1 shape. **Residual grill (impl):** enumerate every
`Cond`-leaf value source (`Store.sqlOf`, a `Codec.map` wrapper, Money/Time/enum) and
confirm each resolves to the SAME `CollSchema.Cols` colType the write used — a leaf that
guesses a different type is a bug the property test must catch.

**R3-2 — stored index keys. PARTIALLY RESOLVED.** The general secondary-index seek keys
stay deferred (§2.4) — Phase 3a Query is PK-scan + in-RAM `bluedbEvalCond` + the §2.6
read-set contract, which is CORRECT and parity-provable (O(N) per query, not the seek's
O(log n + k); acceptable for v1 fixture sizes). **But the STORED unique-index point keys
are NOT deferred** — they ship in 3a because `unique` correctness + SQL parity require
them (§2.7). So one bounded stored keyspace lands now; the general seek layout is Phase
3b/4. **Residual grill:** does the in-RAM scan-materialize (`txn.go:523`) scale to the
parity fixtures without the general seek? (Correctness does not depend on it; only
throughput does — R4-2.)

**R3-3 — Design B vs Design A. CLOSED in favour of B.** The grill confirmed B decisively
(§0.5). SQLite/Postgres satisfy the *Persist verb contract* via the ported Sky `Store`
arm; the Go `Backend` interface is the embedded-family contract. **Honesty note:** the
dual-compile (SQL text for the relational arm; plan-JSON + `bluedbEvalCond` for the
embedded arm) is INHERENT to having a non-SQL backend, in BOTH designs — A does not help
parity and B does not hurt it (§4.2). A adds a THIRD compiler (a Go re-impl of
`renderCond`), pure drift, against "reuse `buildSqlQuery`". Design A is retained only as
the documented rejected alternative (§3.3). No longer an open framing choice.

**R4-1 — Cond/Query parity across KV vs SQL. RESOLVED as a documented forced-semantics
SUBSET (§0.6).** "Byte-identical for ALL `Cond`/`Query`" is PROVABLY FALSE — the two SQL
DIALECTS diverge from each other (nullable `ORDER BY`: SQLite NULLs-first vs Postgres
NULLs-last, `orderTail` emits no `NULLS FIRST/LAST` `ref:Store.sky:1304-1309`; `LIKE`
case; int-vs-text). The gate is byte-identical for the SUBSET (equality, non-null
ordering, integer ranges, ASCII `LIKE` under a forced collation), with the divergent
shapes pinned to ONE forced semantics via a dialect-aware `renderCond`/`orderTail` ADAPT
(§4.1) whose forcing `bluedbEvalCond` mirrors. Empty `inList` is ALREADY parity-clean
(`renderCond` normalizes `CondIn []` → `1 = 0` on both, `ref:Store.sky:1250-1251`) —
removed from the worry list. **Residual grill:** the exact forced `LIKE` collation choice
(ASCII-case-insensitive vs -sensitive) and its `bluedbEvalCond` mirror — pick one, doc
it, test it (§8 step 2).

**R4-2 — kernel dispatch + WitnessCollection abort rate on the hot path. OPEN (perf).**
Two perf questions: (a) the KV-arm verb is an `Ffi.kernel "Embedded_coll*"` `any`-in/out
call — is the blind-write path (engine ~49k/s, phase2-status) preserved through the L3
kernel, or does per-call boxing + `CollSchema` memo-lookup + indexer install throttle it?
Measure `Persist.put` vs raw `Engine.Commit`; confirm a blind put installs the indexer
once, not per-Commit. (b) **New, from §2.6:** a `WitnessCollection`-heavy transactional
workload is serializability-correct but COARSE — every write to a collection aborts every
concurrent scanning txn. Measure the abort/retry rate on a contended fixture and confirm
declaring a range-optimized index converts the coarse witness to a tight range-witness.

**R5-1 — the capability check's boot-visibility. RESOLVED by splitting per binding kind
(§5).** `Live.withReactive`/`liveInto` are boot-visible → hard-fatal AT BOOT (for a
multi-replica non-`CrossInstanceReactive` backend). `watchCollection` in `subscriptions`
is NOT boot-visible (evaluated per-Model) → hard-fatal at FIRST subscription at best.
Single-instance watch is NEVER gated (in-process pub/sub works on all backends,
`ref:Persist.sky:390`). No compile-time `KeyValue`-tag guarantee exists — the first
draft's claim was wrong. **Residual:** the runtime plumbing to detect "multi-replica"
(replica count from `[data]`/deploy config) at boot.

**R5-2 — the compiler warn reading `sky.toml` (§6). OPEN (DX scope).** The optional
cross-instance-reactivity lint would couple the compiler to `sky.toml`'s `[data]` +
replica count. **Grill:** is that coupling in scope, or should it be `sky doctor`-only,
leaving the compiler warn (if any) backend-agnostic ("app declares reactive queries →
needs a cross-instance-reactive backend for multi-replica")? Correctness does not depend
on it (the runtime checks carry the guarantee).

**R6-1 — the `[data]` config subsuming `[database]`. OPEN.** Phase 3 opens
`connectRelational` from `[data]`/`[database]`; the full `[data]` collapse is Phase 5.
**Grill:** does Phase 3 read `[database]` (current) or `[data]` (new) for the SQL arm's
driver? If the two coexist during 3→5, state the precedence.

**R2-1 (NEW) — the multi-collection engine change stays format-stable.** §2.1 changes
`Txn`/`buildReq` to derive `Coll` per-change from the userKey prefix + a per-collection
indexer resolver. **Grill:** verify this is genuinely a `Txn`-level change with NO change
to the CommitReq / changelog-payload / `encodeIndexKey` on-disk format — a
`Coll`-stamping change must not perturb the durable bytes. Confirm the Phase-1/2 blind
fast path (no Txn) is untouched, and the per-collection resolver correctly parses
`collName` from the `0x1F`-delimited userKey for every collection in a multi-collection
transaction.

---

## Appendix — one-line orientation

**PORT (verbatim):** the `Conn` phantom tags + mint-only-via-connect, the
`case conn of` universal-verb dispatch, the `Collection` L0 shape (single-column-
ascending `index` only), the `Cond`/`Query` facade, `Codec`/`Store` codec-as-schema,
`bluedbEvalCond`, `Persist_keyString`, single-instance in-process pub/sub reactivity
(`watchCollection`/`live` — works on all backends via `publishChangeKernel`), and the
**whole SQL arm** except the two dialect renderers (`buildSqlQuery` reused, not
re-rendered in Go).
**ADAPT:** the `KvConn` payload (`BlueDB.Store` → embedded handle); the KV-arm kernels
(`BlueDB_coll*` → `Embedded_coll*` over `bluedb.Engine`); the KV colType-tag plumbing →
codec-derived `CollSchema` carrying `ColType`; the `planJson` consumer → the new
engine's `Query`; **`renderCond`/`orderTail` → dialect-aware (forced `NULLS FIRST` +
`LIKE` collation) for subset parity (§4.1)**; **`Std.Codec` → a non-order-preserving
marker so `Money`/`Codec.map`/unresolved index columns route to the fallback (§2.3)**.
**NEW:** the Go `Backend` interface (embedded-family); the embedded adapter over
`bluedb.Engine`; **the real codec-driven indexer** emitting `IndexCoord`s through the
ONE `encodeIndexKey` the scan bounds already use; **the txn-scoped `Query` read-set
contract (§2.6)** (indexable leaf → `ScanRange`; else → `WitnessCollection`; IS-NULL →
witness); **the multi-collection `Txn`/`buildReq` change (§2.1)** (per-change `Coll` +
per-collection indexer — a bounded engine change, no format change); **`unique`
enforced via SSI (§2.7)** (stored unique-index point key, read+write). Together these
make SSI SOUND at the L3 boundary — no phantom hole.
**The gate:** SQL≡KV parity on `examples/55` on the new engine, **byte-identical for the
documented forced-semantics subset** (§0.6), plus the embedded SSI-soundness suite
(predicate phantom / multi-collection / IS-NULL+Money fallback / concurrent-unique).
