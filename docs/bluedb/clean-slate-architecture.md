# BlueDB / Std.Persist — clean-slate rebuild architecture

> **Status:** architecture design, `feat/bluedb` (off `main`). This is the doc the
> next phase **grills**, then implements. No production code is specified here —
> interfaces, decisions, a port audit, a phased roadmap, and a risk register.
>
> **Reference checkout:** the prior implementation (`exp/bluedb`) is at
> `.claude/worktrees/ref-exp-bluedb/`. **Every `file:line` citation below is
> relative to that worktree** unless it names a `docs/` path. The prior layer
> works and much of it is the moat; this doc says precisely what carries forward,
> what adapts, and what gets rebuilt — and why the *foundation* (not the surface)
> is what we are replacing.

---

## 0. TL;DR — the one-paragraph thesis

The prior data layer grew **outward-in**: a hand-built KV engine (WAL + in-RAM
`map[string][]byte` memtable + full-snapshot checkpoint, single-writer, no
isolation), then a KV surface (`Std.BlueDB`), then a unified front
(`Std.Persist`) bolted over separate backends, then reactivity, then admin. The
**surface is good and largely portable**; the **foundation cannot deliver three
things structurally**: (1) real ACID isolation (there is *no* MVCC, *no* snapshot
isolation, *no* multi-key read-modify-write transaction — `db.go:114`,
`db.go:254`, `db.go:342-346`); (2) scale (the working set is RAM-resident with a
hard `MaxKeys`/`ErrFull` ceiling and an O(working-set) stop-the-world checkpoint —
`db.go:94-97`, `db.go:602-609`); (3) query-scoped reactivity in the commit path
(today it is a **collection-scoped re-query loop** — `live_reactive.go:11-12`
states this outright — with the *actual* query-scoped delta engine sitting in the
tree as **unwired dead code** — `bluedb_reactive.go:94-142`).

We rebuild the **engine** on **ordered on-disk storage + MVCC + a portable
transaction**, and we **collapse the config to one `[data]` section + one CRUD/query
front door** (joins/aggregates/txn-guarantees/raw-KV remain distinct escape hatches
— see the Grill outcomes below and Decision 5). We do **not** hand-build
an LSM. We **embed Pebble** as the ordered-storage substrate and put the entire
moat — codec-as-schema (L0), deterministic transactions (L2), query-scoped
reactivity in the commit path (L4) — *above* it. The prior README's own
architecture diagram already names Pebble as the storage row
(`docs/bluedb/README.md:214-217`); the strategy doc already locates the moat in
the transaction + reactive layers, not the bytes on disk
(`docs/bluedb/strategy.md:54-67`). This doc formalizes that and resolves the five
hard calls.

---

## Grill outcomes (Phase 0 close)

This doc was subjected to a 3-adversary grill. **The foundation SURVIVED and
stands** — the build-vs-embed call (embed Pebble), MVCC-timestamp-in-key, and the
single-writer committer are validated and must NOT be redesigned. Grill A built
real binaries: embedding Pebble cross-compiles cgo-free to every Sky target and
costs +10–18 MB on the existing floor — empirically buildable. But **two headline
claims were proven false and folded in as corrections**, plus ~10 specific fixes.

**Two headline claims CORRECTED:**

1. **Isolation is NOT "serializable for free."** Key-granular read-set validation
   gives only **Snapshot Isolation** — it rejects lost-update + point write-skew but
   MISSES predicate phantoms (a key-set cannot witness the *absence* of a row).
   **The user chose REAL SERIALIZABILITY (SSI)**, delivered via **index-RANGE
   read-set validation** (Decision 4): a scan records the index *range* it traversed,
   and commit-time validation checks whether any committed change in `(readTs,
   commitTs]` fell into that range → catches phantoms → genuine serializable on a
   single node.

2. **Backend parity is NOT a compile-time union gate.** `watch` CANNOT be gated to
   `Conn (KeyValue | PostgresNotify)` — a capability UNION is not expressible in
   Sky's HM (no type classes, no HKT), the Postgres tag is un-mintable, and the
   backend axis is **irreducibly RUNTIME** (the build env holds no `DATABASE_URL`;
   the image is built once and the backend injected at boot — HM types cannot depend
   on a runtime value, so compile-time backend-capability safety is impossible *by
   theorem*). The resolved design (Decision 5) is a **compiler-WARN + runtime-loud
   HARD-FATAL check + CI/deploy preflight**, never a silent stale.

**The specific fixes folded in** (each updates its section + risk-register entry):
R1 (single committer-gated emit funnel + a durability tier — ephemeral input state
renders without fsync, semantic transitions persist-before-ack, + a group-commit
window); R4 (retry BOUND + typed `Conflict` into `update()` + always-ack-on-error +
a committer-ordered pessimistic fallback for hot keys); R3 (an advancing
per-reader `readTs` watermark, not a pinned snapshot; GC floor = min over live
readers); R8 (HLC restart floor = `max(persisted_high_water+1, wall_clock)` +
in-batch invariant + crash test); R9 (session-blob schema-version tag — unified
migration INHERITS the gap, does not fix it); the N-scale fan-out gate (O(writes) is
the re-eval win, NOT O(N) SSE fan-out — the N=2 demo hides it); and auto-admin
honesty (shipped is a READ-only browser; a scalar-only edit form is net-new work
gated on the `record_fieldset_collision` bug).

**The single irreversible Phase-1 gate: the Pebble `Comparer`.** `Comparer.Name` is
baked into SSTable metadata and cannot change after the first SSTable is written —
so the custom Comparer (Split + Name) that MVCC + prefix-bloom + compaction-filter
GC require must be designed and LOCKED before Phase 1 writes any data.

---

## 1. Goals, the 5 dev-pains it kills, non-goals

### 1.1 Goals

1. **One data model, one API, one config, one migration, one admin.** A developer
   who wants to persist data names *no backend* in code (the 99.99% path). Raw KV
   and dialect SQL remain, marked as escape hatches.
2. **Real ACID on the embedded engine.** Snapshot reads + serializable multi-key
   transactions from day one — not "single-writer serialization + atomic
   WriteBatch" (which is atomicity + durability, *not* isolation —
   `db.go:342-346`).
3. **Scale without a code rewrite.** Ordered on-disk storage removes the RAM
   ceiling and the checkpoint pause. `embedded → postgres → cluster` is a
   one-line `sky.toml` change, never an app-code change.
4. **Reactivity is an output of commit, not a bolt-on.** Subscriptions are
   query-scoped from day one. The commit path evaluates registered query
   predicates against each committed change and fans out only to affected
   subscribers.
5. **Keep the moat.** Codec-as-schema, the verified-sync-unit reactive scoping
   model, the OLTP-hot-path/magic-first product bets, the durability contract, and
   the crash-consistency test methodology all carry forward
   (`docs/bluedb/strategy.md:42-76`, `docs/bluedb/unit-architecture.md:16-23`,
   `docs/bluedb/durability.md:7,41-44,211-228`).

### 1.2 The five developer pains it must kill

| # | Pain today | Kill |
|---|---|---|
| **P1** | **"Which database?"** — an app juggles `[database]` (app data) + `[live].store` (sessions) + `[analytics].dbPath` (three configs, three mental models — `docs/bluedb/README.md:151-170`); the backend is *named in code*; sqlite→postgres *touches code*. | ONE `[data]` section; the backend is invisible in app code; graduation is a config line. |
| **P2** | **Schema drift.** The same record shape is redefined in the JSON codec, the DB columns, the migration, and the admin form — hand-kept in sync. | ONE typed collection declaration (record + `Codec.auto`) derives all four (L0). |
| **P3** | **No transaction you can trust.** No isolation; a `Time.every` ticker + a click is "one scheduling window from crashing the SERVER" via `concurrent map read and map write` (`docs/bluedb/state-sync-and-broadcast-grill.md:17-24`); async Model mutations are acked but never persisted (R1, `:10-16`). | Snapshot reads + serializable `Persist.transaction`; the read/write race dissolves by construction (L1/L2). |
| **P4** | **Reactivity is a separate system.** You wire pub/sub, sockets, and re-query by hand; on SQL it is *silently stale* (`docs/bluedb/reactive-sync-design.md:302-306,399-402`); on KV it is *collection-scoped* re-query and O(N²) at scale (`docs/bluedb/unit-architecture.md:213-217`). | `watch` / `live` fall out of the commit path, query-scoped, degrading to over-refresh, never under-skip (L4). |
| **P5** | **Scale cliff.** RAM-resident working set + `ErrFull` (`db.go:94-97,421-427`), O(working-set) checkpoint pause (`db.go:602-609`), single-writer, and moving off it is the parked "v2 rework" (`docs/bluedb/roadmap.md:190-197`). | Ordered on-disk storage + MVCC as the *foundation*, not a deferred v2. |

### 1.3 Non-goals (explicit)

- **Not a hand-built LSM.** See Decision 1. We embed Pebble.
- **Not multi-process write on one embedded file.** One committer per open file is
  the permanent, correct floor (`docs/bluedb/roadmap.md:199-203`). "More writers"
  means more concurrency *into* the committer (done) or more shards each
  single-writer (cluster tier).
- **Not SQL-first.** SQL is the analytics/interop bridge, read-mostly; it never
  owns the hot write path (`docs/bluedb/strategy.md:25-39`).
- **Not the distributed cluster in this phase.** The cluster tier (Calvin-style
  deterministic Sky transactions over range shards) is *designed for* here so the
  embedded API doesn't have to change to reach it — but it is a later multi-quarter
  effort. The embedded engine is the deliverable that de-risks the bet.
- **Not client-side/offline replicas.** Sky.Live is server-authoritative; there is
  no client DB and therefore no optimistic-rebase problem
  (`docs/bluedb/reactive-sync-design.md:19-22,70-85`).

---

## 2. The five-layer architecture

```
┌ L0  DECLARATION — one typed collection (record + Codec.auto) is the source ─────┐
│     of truth: derives schema · migration diff · reactive change-shape ·         │
│     admin forms · query typing. The Sky.Live Model is just a collection.        │
├ L3  LOGICAL API — Persist: get/put/insert/delete · query builder · watch ·      │
│     transaction. Backend-invisible; dispatches to a Backend interface.          │
│     (drawn above L1/L2 because it is what app code sees)                         │
├ L2  TRANSACTION / CONSISTENCY — ONE Persist.transaction conn (\tx -> …):        │
│     SQL → BEGIN/COMMIT · embedded → MVCC snapshot + index-RANGE read-set         │
│     validated commit (SSI = serializable) · cluster → Calvin det. exec (pure).   │
├ L4  REACTIVITY — the changelog IS an output of commit; query-scoped subs are    │
│     evaluated in the commit path; fan-out only to affected subscribers;         │
│     row/query/collection + tenant scoping fall out; cross-instance via broker.  │
├ L1  ENGINE — ordered storage (Pebble) · MVCC (version-in-key, HLC commit ts) ·  │
│     single-writer committer assigning versions · WAL/atomic-batch from Pebble.  │
└──────────────────────────────────────────────────────────────────────────────────┘
        Backend adapters: BlueDB(embedded) · SQLite · Postgres · [Cluster]
```

The layering rule: **L1 is bytes + versions; L2 is ordering + isolation; L3 is
vocabulary; L4 is push; L0 is the type that generates them all.** The moat is
L0 + L2 + L4. L1 is deliberately commoditized (Pebble).

### L0 — Declaration (source of truth)

**Responsibility.** One place defines a collection; everything else is derived.
A collection is a Sky record type + `Codec.auto blank` + key/index/constraint
declarations. From that single value the system derives: the storage row codec
(JSON blob + scalar columns), the relational schema (for the SQL backend + admin),
the migration diff shape, the reactive change payload shape, and the compile-time
query typing.

**Why it already exists and ports unchanged.** `Std.Codec` (`Codec.sky`, whole
file) is pure Sky over `Json.Encode/Decode` + three reflection kernels
(`Codec_autoEnc`/`autoDecoder`/`autoCols`, `Codec.sky:372-394`). It produces a
`Shape = SRecord (List (String, ColType)) | SScalar ColType | SBlob`
(`Codec.sky:79`) that already drives **three** consumers: JSON, the DB columns
(via `Store.codecColumns`), and the KV index colType tags (via `colTypeKind`,
`Codec.sky:349`). This is the "codec duality as the schema" differentiator
(`docs/bluedb/strategy.md:56-61`). It knows nothing about RAM-residence or
reactivity — it is the cleanest carry-forward in the stack and **anchors** the
rebuild.

**Interface (unchanged from today, re-homed under Persist):**

```elm
type alias Collection a =
    { store   : Store a          -- Store.fromCodec "users" (Codec.auto blank) |> Store.primaryKey "id"
    , key     : String
    , indexes : List String
    , unique  : List String
    }

collection : Store a -> Collection a
index      : String -> Collection a -> Collection a
unique     : String -> Collection a -> Collection a
```

The **Model-is-a-collection** identity (`Live.autoBlueDB` — the whole Model is one
scope-keyed collection row) is the phase-1 magic and re-ports directly
(`docs/bluedb/README.md:63-99`).

### L1 — Engine (the real rebuild)

**Responsibility.** Ordered, durable, versioned bytes. Given a key and a read
timestamp, return the value at that version (snapshot read). Given a batch of
versioned writes at a commit timestamp, apply them atomically and durably. Provide
ordered forward/backward iteration for O(log n + k) range scans. Provide a
post-commit changelog stream carrying the commit timestamp.

**What it removes** (the structural gaps, all cited):
- The RAM ceiling — Pebble spills to SSTables on disk; the working set is a block
  cache, not the whole store (`db.go:94-97` today mandates "bound it to fit RAM";
  `docs/bluedb/capacity.md:6-10` "does not spill to disk").
- The O(working-set) checkpoint pause — Pebble compaction is incremental and
  background (`db.go:602-609` today copies the entire map on the committer
  goroutine).
- The lack of ordering — Pebble iteration is natively ordered, so the manual
  order-preserving index-key encoder (`bluedb_index_kernel.go`, whole file) and
  the scan-then-`sort.Slice` executor (`db.go:742-765`, `bluedb_query_kernel.go`)
  both disappear.

**Interface (the Go-side engine contract the adapter implements):**

```
Engine.Snapshot(readTs HLC) -> Reader          // MVCC consistent read view
Reader.Get(key) -> (value, version, ok)
Reader.Iter(lo, hi) -> ordered cursor          // O(log n + k)
Engine.Commit(batch []VersionedWrite, commitTs HLC) -> error   // single-writer, atomic
Engine.Changelog() -> stream of Committed{ commitTs, []KeyChange }   // post-commit
```

**What ports into it.** The **group-commit discipline** (single committer, one
fsync amortized over a ≤1024-write batch, roll-back-to-clean-boundary on
mid-batch fault — `db.go:445-559`, `db.go:530-549`) is a *design* that carries
forward as how we drive Pebble's `Batch` + `Apply(Sync)` from one committer
goroutine. The **WAL v2 torn-tail-vs-corruption discriminator**
(`wal.go:443-549,676-739`) is the *crown jewel of the old engine* but Pebble owns
its own WAL — so this ports as **understanding + a test oracle**, not code (see
Decision 1's honesty note). `flock` (`flock_unix.go:14-16`) ports as-is —
single-writer file locking is needed regardless of storage engine.

### L2 — Transaction / consistency (the missing ACID-I + the Sky moat)

**Responsibility.** ONE portable transaction that the app writes once and runs on
every backend, with a precisely-stated isolation level per backend.

```elm
Persist.transaction : Conn cap -> (Tx cap -> Task Error a) -> Task Error a
```

**Per-backend realization:**
- **SQL (SQLite / Postgres)** → real `BEGIN … COMMIT`; isolation set to the
  backend's serializable mode (`SERIALIZABLE` on Postgres, `BEGIN IMMEDIATE` +
  serialized writer on SQLite). `Std.Db.withTransaction` already exists and the
  `Store.transaction` alias over it ports (`Store.sky:918`).
- **Embedded (BlueDB)** → **MVCC snapshot + index-range validated commit**
  (Decision 4). The transaction body reads at a snapshot `readTs`, buffers its
  writes AND records every index RANGE it scanned, and the single-writer committer
  assigns `commitTs`, validates that no committed change in `(readTs, commitTs]` fell
  into a read key *or* a scanned range, and applies atomically or aborts for retry.
  Recording ranges (not just keys) is what upgrades this from snapshot isolation to
  genuine **serializability (SSI)** — a key-only read-set cannot witness a phantom
  insert. No 2PC and no lock manager — the single committer *is* the serialization
  point.
- **Cluster (future)** → **Calvin-style deterministic execution**. Consensus
  (Raft) orders the transaction *commands*; because the transaction body is pure
  Sky — total, deterministic *by the type system* — every replica executes the
  same body against the same ordered inputs and reaches byte-identical state, with
  **no 2PC lock coordination**. This is the differentiator no SQL-first DB can copy
  (`docs/bluedb/strategy.md:61-67`): Sky's purity *gives* the determinism property
  those databases spend enormous runtime effort enforcing.

**The invariant that makes all three sound:** the transaction body is pure Sky —
it may read and compute and return writes, but it **cannot emit effects/Cmds**
(the same rule the reactive fold already enforces —
`docs/bluedb/reactive-sync-design.md:319`). Purity is what makes the embedded
validated-commit re-runnable on abort and the cluster body replayable across
replicas.

### L3 — Logical API (backend-invisible)

**Responsibility.** The one surface app code names. Dispatches to a `Backend`
interface. The 99.99% path never names a backend.

**This layer largely already exists and PORTS.** `Std.Persist` (`Persist.sky`)
is a phantom-tagged capability front: `type Conn cap = SqlConn Db | KvConn
BlueDB.Store` (`Persist.sky:118-120`), minted only by `connectRelational : () ->
Task Error (Conn Relational)` and `connectKeyValue : String -> Task Error (Conn
KeyValue)` (`Persist.sky:199,205`), so calling a KV-only verb on a relational conn
is a **compile error**, not a runtime one. The universal verbs
(`get`/`put`/`insert`/`delete`/`count`/`all`) dispatch by `case conn of`
(`Persist.sky:212-439`). The **query builder is the already-shared `Cond`/`Query`
algebra** re-exported from `Std.Db.Store` (`Store.sky:710-912`,
`Persist.sky:665-908`) — leaves `eq/neq/gt/gte/lt/lte/like/isNull/notNull/inList`,
combinators `and_/or_/not_`, ordering, paging, terminals `toList/toMaybe/toCount`
— injection-safe, `guardCols`-checked. **The new Persist API keeps this
vocabulary verbatim; it is already the right one.**

**Interface (the Go-side `Backend` the engine + SQL drivers implement):**

```
Backend.Get / Put / Insert / Delete(collection, key/record) -> …
Backend.Query(collection, resolvedCond, orders, limit, offset) -> rows
Backend.Transaction(fn) -> …
Backend.Watch(collection, resolvedCond) -> subscription        // L4 hook
```

**What ADAPTS.** Every `KvConn` arm that threads `colType` tags +
`indexFieldValues`/`indexFieldTypes` (`Persist.sky:307-357`) exists only to feed
the *manual* order-preserving KV index; on the ordered engine those collapse. The
`findAllByIndexRange` "errors on real/blob/money, use SQL" caveat
(`Persist.sky:574-580`) disappears. The `planJson`/`condPlanJson` KV serializer
(`Store.sky:1054-1194`) survives (a commit-path engine still needs the *resolved*
`Cond` to test row membership) but its **consumer** changes from a scan evaluator
to a commit-path predicate.

### L4 — Reactivity (in the commit path)

**Responsibility.** The changelog is an *output* of commit. Subscriptions are
**query-scoped from day one**: a session registers `(collection, resolvedCond)`;
the commit path evaluates each committed row-change against each registered
predicate and fans out a precise per-query delta only to affected subscribers.
Row / query / collection scoping and **tenant** scoping fall out naturally;
cross-instance fan-out reuses the existing broker.

**The critical finding: the query-scoped engine already exists as dead code.**
`bluedb_reactive.go` defines `bluedbQuerySub` (tracks `coll`, `cond`,
`resultPks`) and `bluedbChangeAffectsQuery` (`bluedb_reactive.go:94-142`) — precise
enter/leave/in-place logic reusing the row predicate `bluedbEvalCond`
(`bluedb_query_kernel.go:338`) — but **nothing in the live loop instantiates
it**. `Persist.live` even computes and carries a `condPlan` for it that the
current loop ignores (`Persist.sky:968`, "carried but unused"). Today the loop
refreshes at **collection scope** (`live_reactive.go:11-12`: "v1 refreshes at
COLLECTION scope … the P3 overlap engine … is a later optimization"). **The
rebuild promotes this dead code into the commit path** — that promotion *is* L4.

**What ports.** The verified-sync-unit scoping model — key the broadcast by *who
shares state* (the tenant, read from **verified** `SessionIdentity`), not by *what
table changed* — carries forward wholesale; it is a security model, not a storage
mechanism (`docs/bluedb/unit-architecture.md:16-23,72-84`). The topic naming +
broker fan-out (`reactive:<tenant>:<coll>`) and the cross-instance Redis broker
(`live_redis_broker.go`, selected by `store=redis`/`SKY_LIVE_BROKER_URL`) port.
The re-query-nudge-is-self-healing property ("any nudge heals all prior misses" —
`docs/bluedb/unit-architecture.md:54-60`) carries forward as the *fallback* on
overflow/resync, with precise deltas as the fast path.

**What rebuilds.** `live_reactive.go`'s per-collection re-query loop
(`startReactive`/`reactiveLoop`/`reactiveRefreshOnce`, `:82-366`) becomes an
apply-scoped-delta-then-frame path. The SSE-frame tail, panic-rollback, and
lock discipline (`:301-366`) are TEA-render concerns and port. Crucially, with a
per-tenant *verified* topic the change body can carry the record safely again
("tenant-mates are entitled to it") → the overlap engine works → **O(writes)** not
O(N×M) — "the two models stop fighting once the topic is the tenant"
(`docs/bluedb/unit-architecture.md:170-177`).

### DX collapse (what L0–L4 buy the developer)

**Honest framing (grill fix #11): the CONFIG collapse is the real headline win, not
"one model" for the whole API.** `[data]` subsuming three configs + one front door
for CRUD+query is genuine and proven. But joins/GROUP BY/aggregates, transaction
*guarantees*, and raw-KV remain **distinct escape hatches** (Decision 5's own
leaks) — do not over-claim a single uniform model across all of it.

- **One config** — `[data]` subsumes `[database]` + `[live].store` + `[analytics]`
  (`docs/bluedb/README.md:151-170`). **This is the strongest, cleanest win.**
- **One CRUD + query front door** — `Persist` get/put/insert/delete + `Cond`/`Query`,
  no backend named in code. (Joins/aggregates stay `selectRaw`/SQL; raw-KV stays the
  `Std.BlueDB` escape hatch — §Decision 5.)
- **One migration** — `sky data migrate` diffs declared collections vs recorded
  schema, spanning session + app + analytics stores (but see R9: the session Model
  blob is NOT a declared collection today — the rebuild must add a blob version tag).
- **One auto-derived admin console** — the Data tab renders a READ browse + `Cond`
  filter from L0 (a scalar-field edit form is a future add-on — §5.7)
  (`sky-bundled/console/src/DataTab.sky`).

**This is a BREAKING migration** for apps already on `exp/bluedb`'s surfaces
(skydeploy, sky-lang.org, darraghstudio): the `[data]` config + reactive wiring
change, though the `Persist` CRUD call sites largely survive (PORT).

---

## 3. The five hard decisions — RESOLVED

### Decision 1 — Build the storage engine vs EMBED a proven Go LSM

**RESOLUTION: EMBED Pebble as the L1 ordered-storage substrate. Build MVCC (L1
version layer) + transaction (L2) + reactivity (L4) + codec/Persist (L0/L3) on
top. Do NOT hand-build an LSM.**

**Rationale.**

1. **The moat is not L1.** The two Sky-only differentiators are codec-as-schema
   (L0) and deterministic transactions (L2) — plus query-scoped reactivity (L4).
   None of those is "bytes on disk." "Reject SQL-first … forfeit the only unfair
   advantage" is about the *transaction/reactive* surface, not the storage format
   (`docs/bluedb/strategy.md:25-39,54-67`). A hand-built LSM spends multi-month
   correctness budget on a layer that is **not** where Sky wins.

2. **The prior design already chose Pebble.** The README architecture row is
   literally "Storage: Pebble (Go LSM) — embedded, or per-shard in cluster"
   (`docs/bluedb/README.md:214-217`); the north-star table says "Write-optimized
   LSM (Pebble) + large block cache + bloom filters"
   (`docs/bluedb/README.md:41`). The hand-built engine was a *phase-1 expedient*,
   not the intended end-state.

3. **Pebble gives us exactly the substrate primitives MVCC needs, correctly:**
   ordered iteration, atomic indexed batches, consistent **snapshots**, a WAL with
   crash recovery, background compaction, and — decisively — a **first-class MVCC
   key encoding and compaction-filter GC** already battle-tested by CockroachDB
   (Pebble *is* Cockroach's storage engine). We are not the first to put MVCC on
   Pebble; the pattern is proven.

4. **Honest cost/risk of the alternative.** The hand-built engine is genuinely
   well-engineered where it counts (the torn-tail discriminator,
   `wal.go:443-549`), but it is **~2500 lines of non-test Go** that still has: no
   ordering, no MVCC, a RAM ceiling, an O(N) checkpoint, and an *admitted
   irreducible durability residual* (rot of the last acked group is silently
   truncated — `docs/bluedb/durability.md:98-119`). Re-deriving ordering +
   MVCC + spill-to-disk + incremental compaction from scratch is re-implementing
   Pebble, badly, on the critical path. That is the multi-month correctness risk we
   refuse.

5. **Does embedding undercut the "Sky-native engine" moat?** No — and this is the
   crux. **The moat is in L2 + L4, and we own those 100%.** CockroachDB embeds
   Pebble and no one calls Cockroach "not a real database." The Sky-native claim is
   "a pure-Sky deterministic transaction the type system guarantees" + "reactivity
   that is an output of commit" — both live *above* Pebble, in code we write. If
   anything, embedding Pebble **strengthens** the moat by freeing the entire build
   budget for L2/L4.

**The hybrid, precisely:** Pebble owns `{ordered SSTables, atomic batch, snapshot,
WAL, compaction}`. We own `{HLC/version assignment, single-writer committer,
read-set validation, the changelog-as-commit-output, query-scoped fan-out, the
codec row layout, the Persist/transaction/watch surface}`. The MVCC version layer
is *our* key encoding on top of Pebble keys (Decision 3).

**The custom Pebble `Comparer` is an IRREVERSIBLE Phase-1 gate (grill fix #7).** Our
versioned key encoding requires a **custom Pebble `Comparer` (its `Split` + `Name`
functions)** — this is NOT "free inheritance of Pebble's iteration semantics" as a
casual reading of Decision 3 might suggest. The `Comparer` is what teaches Pebble to
split `<user-key> 0x00 <inverted commitTs>` into prefix + version so that
prefix-bloom point reads and the compaction-filter GC (Decision 3) work at all.
Critically, **`Comparer.Name` is baked into every SSTable's metadata and CANNOT be
changed after data lands** — a mismatched name refuses to open the store. So the
Comparer is a **day-1 format commitment** that must be designed and LOCKED *before
the first SSTable is written*. It is called out as an explicit **Phase-1 success
gate**.

**Empirically-verified build facts (grill A built real binaries):**
- `CGO_ENABLED=0` cross-compiles Pebble to every Sky target (Pebble is pure Go).
- Set **`-tags pebblegozstd`** in the build runner so the cgo-RETRY path (the one
  `sky build` falls back to) is *also* cgo-free — otherwise a cgo retry could pull a
  cgo zstd.
- **Silence Pebble's default `Logger`** (it logs to stderr by default — route to
  `Std.Log` or discard).
- Binary grows **+10–18 MB** on the existing ~30 MB floor — acceptable.
- **Trim the transitive surface** (Pebble pulls sentry / prometheus transitively) via
  build tags / `go.mod replace` so the flagship binary stays lean.

**What this costs us from the old engine:** we do **not** reuse `wal.go` /
`db.go`'s committer bytes (Pebble replaces them). We carry their *lessons* — the
group-commit driving pattern, the ack-only-after-recoverable contract
(`docs/bluedb/durability.md:7`), and the torn-tail test **scenarios**
(`crashsim_test.go`, `fault_test.go`, `backup_test.go`) become a **conformance
oracle** run against the Pebble-backed engine. **Correction (grill fix #7):** only
the crash-corpus *scenarios* port directly — the fault-injection **HARNESS is
net-new**, re-expressed via Pebble's `errorfs` (fault-injecting VFS), NOT the old
`walWrap` hook (which was WAL-v2-format-bound and is gone with the WAL). Budget the
`errorfs` harness build in Phase 1.

### Decision 2 — LSM vs B-tree (given we embed, this is "which Pebble mode / would a B-tree fit better")

**RESOLUTION: LSM (Pebble). It is the correct fit for the North Star.**

**Rationale.** The North Star is the **OLTP hot path — fast, frequent, small
read+writes** — which is a *firehose of small point writes* (a reactive Model
mutating on every keystroke/click/tick — `docs/bluedb/strategy.md:42-53`,
`docs/bluedb/README.md:27-33`). That is precisely the **write-optimized** profile
an LSM serves best: writes are memtable appends + sequential WAL, amortized by
group commit; a B-tree pays random-write page I/O + write amplification on the same
workload. Point reads stay RAM-speed via the block cache + bloom filters (the
working set is hot). Range scans are O(log n + k) via ordered SSTable iteration —
the thing the old engine lacked. MVCC-version GC maps to an LSM compaction filter
naturally (Decision 3). A B-tree would win only on read-mostly + large-scan
workloads, which is explicitly the *analytics surface* we keep separate and never
let slow the hot path (`docs/bluedb/strategy.md:46-48`). **LSM.**

### Decision 3 — MVCC design

**RESOLUTION: MVCC-timestamp-in-key (Cockroach/Pebble style), single-writer commit
timestamp assignment via HLC, GC via compaction filter below the oldest open
snapshot.**

**Version storage — timestamp *in the key*, not per-key version chains.** Encode
the storage key as `<user-key> 0x00 <commitTs: inverted big-endian HLC>`, so that
for a given user-key the **newest version sorts first**. This makes a snapshot read
a single seek: `SeekGE(<user-key> 0x00 <inverted readTs>)` returns the first
version with `commitTs ≤ readTs`. This is superior to per-key version chains
(pointer-chasing, separate GC bookkeeping) and it is exactly how Pebble's own MVCC
layer works — but note this ordering does NOT come for free: it requires the custom
`Comparer` (Split + Name) that Decision 1 flags as the irreversible Phase-1 format
gate. Tombstones are versioned deletes (a version with an empty/marker value).

**Snapshot reads.** A reader holds a Pebble snapshot + a `readTs`. Every `Get`/
`Iter` filters to `commitTs ≤ readTs`, seeing a **consistent** point-in-time view.
This dissolves the old engine's read/write data race by construction — no more
"aliases the live memtable" (`bluedb_query_kernel.go` multi-step seek→Get seeing
different committed states) and no more `concurrent map read and map write` server
crash (`docs/bluedb/state-sync-and-broadcast-grill.md:17-24`).

**GC of old versions.** A Pebble **compaction filter** drops any version whose
`commitTs` is below the **GC threshold** = the timestamp of the *oldest still-open
snapshot* (open reader / long transaction / reactive binding). Versions above the
threshold are retained for readers; a single latest version per key is retained
unconditionally. GC is therefore incremental + background (rides compaction) — no
stop-the-world, the antithesis of the old O(N) checkpoint.

**Interaction with the single-writer committer.** The committer is retained as the
serialization point — it is the *correct, permanent floor*
(`docs/bluedb/roadmap.md:199-201`). It assigns a strictly-monotonic **HLC**
`commitTs` per committed transaction/batch and writes versioned keys at that
`commitTs` in one Pebble atomic batch. Because one committer assigns all
timestamps, timestamps are a total order for free — no clock-skew coordination on
a single node (HLC matters at the cluster tier).

**Crash-consistency of the whole thing.** Pebble's WAL + atomic batch guarantee
that a committed batch is all-or-nothing durable; on recovery Pebble replays its
WAL to the last durable batch. Our commit metadata (the HLC high-water, the
changelog cursor) is written **inside the same atomic batch** as the data versions
(a reserved metadata key) so it can never diverge from the data — the same
"single-batch is load-bearing" discipline the old serial-counter used
(`docs/bluedb/schema-enforcement-design.md:70-78`). The ack-only-after-recoverable
contract (`docs/bluedb/durability.md:7`) is preserved: we ack a commit only after
Pebble's `Apply(Sync)` returns.

**HLC restart safety (grill fix #6 — REQUIRED, unsafe without it).** Persisting the
HLC high-water in the same batch closes the metadata/data *divergence* question but
is NOT sufficient alone. On restart the committer MUST initialize `HLC =
max(persisted_high_water + 1, wall_clock)` — reading the high-water is not enough, it
must **FLOOR the clock to it**. Otherwise a backward clock step (NTP correction, VM
migration, scheduler reschedule) re-issues a *used* `commitTs` → two distinct
versions collide at one key → silent corruption. Two enforced guards accompany it:
(a) an **in-batch invariant** — the committer refuses to `Apply` any data batch that
lacks the metadata (high-water) key; (b) a **crash-corpus test** asserting no
re-issued `commitTs` across recovery (kill -9 mid-commit + clock-rewind fault
injection). See §6.4 and R8.

### Decision 4 — The transaction commit protocol (embedded)

**RESOLUTION: Optimistic — single-writer committer + snapshot read + commit-time
index-RANGE read-set validation. Isolation level provided: genuine SERIALIZABILITY
(SSI), the level the user chose. Single-key blind writes are their own fast-path
transaction that skips validation.**

**The grill correction (headline #1).** An earlier draft claimed *key-granular*
read-set validation gives serializable "for free." It does NOT — it gives only
**Snapshot Isolation**. Key-granular validation rejects lost-update and *point*
write-skew, but MISSES **predicate phantoms**: two txns both scan `WHERE
status='open'`, both see the empty set, both insert a matching row, both commit → an
invariant over "open" rows is violated, because a key-set read-set literally cannot
witness the ABSENCE of a row that did not yet exist. The fix is to validate on the
index RANGE a scan traversed, not just the keys it read.

**The precise protocol for `Persist.transaction conn (\tx -> body)`:**

1. **Begin.** Capture `readTs` = current HLC high-water. The body reads at the
   Pebble snapshot pinned to `readTs`, recording into a **read-set**: (a) every
   *point key* it reads (with the version it saw), AND (b) every index **RANGE** it
   scans — e.g. a `WHERE status='open'` scan records `status_idx[u|open, u|open]`,
   the exact traversed interval, not just the keys that happened to be present.
   Recording the range is what lets validation witness a phantom insert. Writes are
   buffered into a **write-set** (not applied). Reads see the transaction's own
   buffered writes (read-your-writes) by overlaying the write-set on the snapshot.
2. **Commit (in the single committer).** The committer, processing this transaction
   in serialization order, assigns `commitTs`. It **validates**: for each committed
   `KeyChange` in `(readTs, commitTs]`, does it (a) touch a point key in the
   read-set, OR (b) fall INTO any scanned index range? Either → conflict.
   **REQUIRED implementation constraint:** the changelog MUST be **indexed by
   `commitTs`** (an ordered structure — a Pebble range over the changelog keyspace,
   or an in-RAM ordered map) so validation is **O(commits-since-readTs)**, a bounded
   walk of only the recent tail — NOT an O(N) full scan of the changelog. This is
   the difference between the validation being a cheap tail-read and a scalability
   sink; state it explicitly in the Phase-2 build.
   - **Clean** → apply the write-set at `commitTs` in one Pebble atomic batch;
     emit the changelog entry; ack.
   - **Conflict** → abort; the runtime **retries** the body against a fresh
     snapshot (bounded — see Decision 4's retry policy + §6.1 and R4). The body is
     pure, so retry is safe and side-effect-free (the no-Cmds-in-txn invariant, §L2).
3. **Fast path.** A single-key **blind** `put`/`insert`/`delete` with no prior reads
   is its own transaction with an **empty read-set** → validation is a no-op → it is
   a blind versioned write. **The OLTP hot path is therefore unaffected by SSI
   validation** — the range-scan cost is paid only by transactions that actually
   scan and then write.

**What isolation this actually provides — stated precisely.** Index-range read-set
validation against a single total-order committer yields **genuine serializable
(SSI)** execution: a transaction commits only if neither its point reads NOR its
scanned predicate ranges would have changed at `commitTs`, so the committed history
is equivalent to a serial order (the committer's order) — and predicate phantoms are
caught, which plain SI would miss. Because the single committer also assigns the
commit timestamp *after* validation, we get **strict serializability** on a single
node (real-time order respected). At the cluster tier, HLC + Calvin command ordering
preserves serializability; strict serializability there costs a timestamp-ordering
round (out of scope for the embedded deliverable).

**Why optimistic, not pessimistic locking.** The North Star is *frequent small*
writes with *low contention per key* (a reactive Model is mostly single-owner per
session/scope). Optimistic validation is cheapest when conflicts are rare and
avoids a lock manager entirely — the single committer is the only coordination
point. High-contention hot keys (counters) are handled by **sharded aggregates**
(`docs/bluedb/README.md:44`), not by pessimistic locks. The old unique-constraint
TOCTOU proof (per-value stripe lock, deadlock-ordered acquisition —
`docs/bluedb/schema-enforcement-design.md:80-94`) becomes, under MVCC, a read-set
entry on the unique-index **point key** — a specific-value existence check that
key-granular validation already catches; validation subsumes the stripe-lock dance.
(Note this is the *point-key* case — it is caught even under plain SI, and does NOT
generalize to "serializable for free"; the phantom case above is precisely what
needs the index-range extension.)

### Decision 5 — Backend parity (be honest about where it leaks)

**RESOLUTION: `get/put/insert/delete` + the `Cond`/`Query` builder are genuinely
identical across BlueDB + SQLite + Postgres. `transaction` is portable in *API* but
its *guarantees* leak. `watch`/`live` are first-class only on reactive-capable
backends, guarded by a **compiler-WARN + runtime-loud HARD-FATAL check + deploy
preflight** — NOT a compile-time type gate. Joins/aggregates are an explicit SQL
escape hatch.**

**The grill correction (headline #2).** An earlier draft claimed `watch` is
compile-time-gated to `Conn (KeyValue | PostgresNotify)` and "you cannot silently
get stale reactivity — it will not type-check." That is **false and impossible by
theorem**:
- A **UNION of capability tags is not expressible in Sky's HM** — no type classes,
  no HKT (CLAUDE.md Active Limitations). `Conn (KeyValue | PostgresNotify)` is not a
  real type.
- The Postgres capability tag is **un-mintable**: `connectRelational` returns tag
  `Relational` for BOTH sqlite and postgres — dialect is a RUNTIME property
  (`Persist.sky:196-198`), so the type cannot distinguish "postgres-with-notify"
  from "sqlite-no-notify."
- **The backend axis is irreducibly RUNTIME.** The build environment (CI / dev) does
  not and must not hold the production `DATABASE_URL`; Sky/SkyDeploy builds the image
  ONCE and injects the backend via env at boot. **HM types cannot depend on a
  runtime value**, so compile-time backend-capability safety is impossible.

**Where the abstraction is real (no leak):**
- **CRUD + query builder.** The `Cond`/`Query` algebra already compiles to *both*
  SQL (`Store.buildSqlQuery`) and a plan JSON evaluated by the engine
  (`Store.planJson` → `bluedbEvalCond`) — this dual compilation is *proven today*
  (SQL≡KV parity in `examples/55-persist-query`, `docs/bluedb/roadmap.md:40-42`).
  Column resolution (record-field↔snake-column), injection-safe binding, and
  `guardCols` are backend-independent (`Store.sky:991,1201`).
- **Codec-driven schema semantics** (serial/unique/defaultNow/touchOnUpdate) are
  enforced on both arms from one declaration (`docs/bluedb/schema-enforcement-design.md:37-38`).

**Where it irreducibly leaks (and how we surface it honestly):**
- **`watch` / `live` reactivity — the RESOLVED three-part safety net (not a compile
  gate).** Reactivity is first-class on the embedded engine (commit-path, L4);
  Postgres reactivity is a post-v1 LISTEN/NOTIFY bridge with its own explicit
  capability handle (ties to R10); SQLite has no cross-process notify. Because the
  backend is a runtime fact, safety is delivered by:
  1. **Compile-VISIBLE requirement → compiler WARN.** Whether an app *uses* `watch`
     is a static fact the compiler sees. If it does, the compiler emits a build-time
     WARN: "this app requires a reactive-capable backend."
  2. **Runtime-loud HARD-FATAL check at boot.** At startup the runtime wires
     `[data] backend` and MATCHES the declared reactive requirement against the
     actual backend's capabilities. On mismatch it is a **hard fatal at startup**
     with a clear message ("app uses reactive `watch` but backend=sqlite can't do
     cross-process reactivity — use embedded or add the NOTIFY bridge") — **NEVER a
     silent stale read.**
  3. **CI / deploy PREFLIGHT.** A `sky doctor` / deploy preflight boots with the
     target `[data]` config and asserts capabilities BEFORE production.

  The **embedded default is always reactive-capable**, so ~99% of apps never hit the
  check. This is strictly weaker than a compile-time proof, but it is the *strongest
  guarantee physically possible* given the backend is injected at boot.
- **`transaction` guarantees.** Serializable (SSI, Decision 4) EVERYWHERE;
  additionally *deterministic-replayable* only on embedded/cluster (we own the
  command log there — the SQL engine executes effects, we don't own its log). Same
  API, documented guarantee difference. The deterministic-transaction *moat* is
  embedded/cluster-only by nature.
- **Joins / GROUP BY / aggregates.** SQL-only; on BlueDB these stay the
  `selectRaw` escape hatch or move to the analytics/Postgres-wire read surface
  (`docs/bluedb/roadmap.md:65`, `Store.sky:499`). Not pretended to be portable.
- **Consistency knob.** `Strong` is the default everywhere; `BoundedStaleness`/
  `Eventual` follower-reads are a cluster-tier feature — a no-op degrade on
  embedded (`docs/bluedb/README.md:79`).

**The honest one-liner:** CRUD + query is a perfect abstraction; reactivity and
deterministic transactions are the *magic* and are first-class only where we own
the engine — which is exactly the strategy ("the magic is the business; SQL is the
bridge" — `docs/bluedb/strategy.md:76`). We make the leak **loud** — a
compiler-warn + a hard-fatal boot check + a deploy preflight — never a silent
runtime stale. (Compile-time proof is available only on the embedded KV path, where
the capability is fixed by construction; the cross-backend axis is runtime by
theorem.)

---

## 4. Port-vs-rebuild audit

Verdicts oriented toward: Pebble substrate + MVCC + portable transaction +
query-scoped commit-path reactivity. Every row cites the reference worktree.

### 4.1 Engine (`runtime-go/bluedb/`)

| Component | file:line | Verdict | Note |
|---|---|---|---|
| RAM-map storage + O(N) checkpoint + RAM ceiling | `db.go:113-114,254-259,602-609,94-97` | **REBUILD** → Pebble | The entire reason for the rebuild. |
| Order-preserving key codec ("R1") in engine | *absent* — only `sort.Slice`+`string<` at `db.go:754` | **N/A** | Does not exist in the engine; the real R1 is in the index kernel (§4.3). Pebble ordering absorbs it. |
| WAL v2 commit-record + torn-tail discriminator | `wal.go:49-61,146-157,443-549,676-739` | **REBUILD-as-oracle** | Pebble owns the WAL. Port the *lessons* + the crash test corpus as a conformance oracle. |
| Group-commit (single committer, one fsync, roll-back-clean) | `db.go:445-559,481-512,530-549` | **ADAPT** | Port the *driving pattern* over Pebble `Batch`+`Apply(Sync)`; committer retained as the serialization + HLC-assignment point. |
| Concurrency: single-writer; **no MVCC/SI/txn**; WriteBatch=atomic-write-only | `db.go:120,445-463,485,342-346,785-793` | **REBUILD** | MVCC + validated commit (Decisions 3/4) replaces this. WriteBatch's *atomic-multi-key-write API shape* informs the write-set. |
| Change-feed (post-commit, non-blocking, key-scoped/global) | `db.go:523-529`, `changefeed.go:11-17,91-122` | **ADAPT** | Fan-out-after-commit pattern ports; events must carry `commitTs` and route query-scoped (L4). |
| `flock` single-writer file lock | `flock_unix.go:14-16`, `db.go:214-219` | **PORT** | Needed by any embedded engine. |
| `verify` (read-only integrity scanner, runs before Open) | `verify.go:79-95,149-315` | **REBUILD** (format-bound) | The *pattern* (read-only twin of recovery, CI-gating exit code) ports; impl is WAL-v2-bound → re-expressed against Pebble + our metadata. |
| `backup` (hot, committer-consistent, no live-WAL truncation) | `db.go:271-290,636-669` | **REBUILD** (impl); API ports | Becomes a Pebble checkpoint/SSTable-set clone; the `Backup(dest)` API + route-through-committer idea survive. |
| Crash/fault/backup **test corpus** | `crashsim_test.go`, `fault_test.go`, `backup_test.go` | **PORT-as-oracle** | The three durability properties (torn-tail recovery, write-fault rollback, consistent hot backup) become the acceptance spec for the new engine. |

### 4.2 Logical / codec / query stdlib (`sky-stdlib/`)

| Component | file:line | Verdict | Note |
|---|---|---|---|
| `Std.Codec` (auto/derivation/`Shape`/`ColType`) | `Codec.sky` (whole) | **PORT** | Foundation-independent; the anchor of the rebuild (L0). |
| `Std.Persist` phantom-tag front + universal verbs + query facade | `Persist.sky:109-908` | **PORT** | The backend-invisible surface (L3); keep verbatim. |
| `Std.Persist` KV arms (colType tags, indexFieldValues) | `Persist.sky:307-357,511-633` | **ADAPT** | Manual-KV-index plumbing collapses under ordered storage. |
| `Std.Persist` reactivity surface (`Change`/`watchCollection`/`live`) | `Persist.sky:929-1075` | **REBUILD** | Collection-scoped nudge → query-scoped delta (L4). `condPlan` already carried (`:968`) — wire it. |
| `Std.Db.Store` query builder (`Cond`/`Query`) + writes + SQL arm | `Store.sky:306-918` | **PORT** | Already the shared vocabulary; the new Persist matches it (it already does). |
| `Store.planJson`/`condPlanJson` KV bridge | `Store.sky:1054-1194` | **ADAPT** | Serializer survives; consumer moves scan→commit-path predicate. |
| `Std.BlueDB` raw string-KV tier (`put`/`get`/`scan`/`batch`) | `BlueDB.sky:84-369` | **PORT** | Foundation-independent escape hatch. |
| `Std.BlueDB` collection tier (contract) | `BlueDB.sky:546-766` | **ADAPT** | `coll*` API shape reasonable; colType params become dead. |
| `Std.BlueDB` raw index tier (`putIndexed`/`findByIndexRange`/`reindex`) | `BlueDB.sky:420-539` | **DROP** | Unordered-map workaround; native ordered seeks replace it. |
| `Std.Db.{Schema,Decode,Migrate}` | those files | **PORT** | SQL-only, orthogonal to the engine. |
| `Std.Db.Table` | `Table.sky` | **DROP** | Already deprecated v0.19+ (`Table.sky:2-8`). |

### 4.3 Go kernels + reactivity + session store (`runtime-go/rt/`)

| Component | file:line | Verdict | Note |
|---|---|---|---|
| `Persist_keyString` reflective key extractor | `persist_kernel.go:18` | **PORT** | Foundation-independent record→key. |
| Engine handle registry + open-once + raw KV | `bluedb_kernel.go:33-370` | **ADAPT** | Registry/open-once port; `bluedbMaxKeys` OOM-guard + "RAM-resident" assumption drop; pump-start hook moves into the commit path. |
| Collection kernel (collPut atomicity, stripe locks, defaults, serial, unique) | `bluedb_collection_kernel.go` (whole, esp. `:228`) | **REBUILD** | A *simulation* of an ordered transactional store on a map. Uniqueness/serial → native constraints; defaults → codec-side insert transform; index upkeep → native secondary-index maintenance in the commit path. Contract informs; impl does not port. |
| Cond evaluator `bluedbEvalCond` (row predicate) | `bluedb_query_kernel.go:338` | **PORT** | Reusable jewel #1 — "does this row satisfy this Cond." Used by both query exec and change-affects-query. |
| KV query execution (seek/scan/sort/cap) | `bluedb_query_kernel.go:482-566` | **REBUILD** | Unordered scan+sort+row-cap → native ordered range scan + index-driven planning. |
| Order-preserving index kernel (the real "R1") | `bluedb_index_kernel.go` (whole, encoder `:85`) | **DROP** | Hand-built ordered keys over a map. Pebble's comparator absorbs the colType→ordering mapping. |
| Change decode + publish + tenant topic + broker fan-out | `bluedb_reactive.go:56-234` | **ADAPT** | Topic naming + verified-tenant scoping + broker PORT; the "record body always `""`" nudge *relaxes* under per-tenant query-scoped deltas. |
| **Query-overlap engine** `bluedbChangeAffectsQuery`/`bluedbQuerySub` | `bluedb_reactive.go:94-142` | **PROMOTE** | Reusable jewel #2 — *already written, unwired*. Move into the commit path = L4. |
| Per-session collection-scoped reactive loop | `live_reactive.go:82-366` | **REBUILD** (render tail PORTs) | Collection re-query → apply-scoped-delta-then-frame. SSE/panic-rollback/lock tail (`:301-366`) ports. |
| Session store on BlueDB (8-byte TTL prefix + ForEach-reap + memCache TOCTOU) | `live_store_bluedb.go` (whole) | **ADAPT** | `SessionStore` interface ports unchanged; the manual TTL/reap/fresh-check dance simplifies to a thin adapter on native expiry + snapshot reads. |
| Cross-instance broker (in-proc + Redis) | `live_pubsub_task.go`, `live_redis_broker.go` | **PORT** | Foundation-independent fan-out. |

### 4.4 Admin / CLI / examples

| Component | location | Verdict | Note |
|---|---|---|---|
| Console **Data** tab (browse/edit over `/_sky/console/api/data`) | `sky-bundled/console/src/DataTab.sky` | **PORT/ADAPT** | GUI is L0-derived; re-point at the new backend adapter; keep the hardened endpoint (bearer, no loopback bypass, readwrite-gated, audit-logged — `docs/bluedb/README.md:141-149`). |
| Offline CLI `sky bluedb <path> {stats,keys,scan,get,put,delete,verify,backup,compact}` | `runtime-go/cmd/sky-bluedb` | **ADAPT** | Verbs re-expressed over the Pebble engine + `verify`/`backup` rebuilt (§4.1). |
| `bluedb-console` mini-app | `sky-bundled/bluedb-console/` | **ADAPT** | Re-point at new engine. |
| Examples `53-bluedb-migration`, `55-persist-query` | `examples/` | **PORT-as-conformance** | Become the e2e acceptance demos + the SQL≡KV parity gate for the new backend. |

**Bottom line.** The **Sky-side surface** (Codec, Persist front, `Cond`/`Query`,
raw-KV escape hatch, SQL arms) is largely **PORT** — it is already well-factored
around one codec + one query algebra. The **Go KV implementation**
(`bluedb_collection_kernel.go`, `bluedb_index_kernel.go`, the query executor, the
whole `runtime-go/bluedb/` engine) is the primary **REBUILD** target — it is a
coherent but large emulation of ordered+transactional storage on an unordered RAM
map, and Pebble + MVCC deletes it. The **two jewels to carry verbatim** are
`bluedbEvalCond` (row predicate) and `bluedbChangeAffectsQuery`/`bluedbQuerySub`
(the already-written query-scoped delta logic) — **promoting the latter into the
commit path is exactly the L4 target.**

---

## 5. The DX surface (concrete Sky sketch)

### 5.1 Collection declaration (L0 — one source of truth)

```elm
import Std.Codec as Codec
import Std.Db.Store as Store
import Std.Persist as Persist

type alias User =
    { id : Int, name : String, age : Int, status : String }

blankUser : User
blankUser = { id = 0, name = "", age = 0, status = "" }

-- ONE declaration → schema + migration + admin form + query typing + change shape
users : Persist.Collection User
users =
    Persist.collection
        (Store.fromCodec "users" (Codec.auto blankUser) |> Store.primaryKey "id")
        |> Persist.index "status"
        |> Persist.index "age"
        |> Persist.unique "name"
```

(This is the *current, working* shape from `examples/55-persist-query` — it ports
unchanged.)

### 5.2 CRUD + query (L3 — backend-invisible)

```elm
-- conn : Persist.Conn cap  — obtained from the framework; app code never names a backend
getUser  conn id   = Persist.get conn users (String.fromInt id)      -- Task Error (Maybe User)
saveUser conn u    = Persist.put conn users u                        -- upsert; assigns serial id if unset
newUser  conn u    = Persist.insert conn users u                     -- returns row with DB-filled fields
dropUser conn id   = Persist.delete conn users (String.fromInt id)

activeAdults : Persist.Query User
activeAdults =
    Persist.query users
        |> Persist.where_ (Persist.eq "status" (Persist.string "active"))
        |> Persist.where_ (Persist.gte "age" (Persist.int 18))
        |> Persist.orderDesc "age"
        |> Persist.limit 50
-- terminal: Persist.toList conn activeAdults : Task Error (List User)
```

Same `Cond`/`Query` compiles to SQL on a Postgres conn and to an ordered
index-planned scan on the embedded conn — identical source (§Decision 5).

### 5.3 Transaction (L2 — portable, serializable)

```elm
transferPoints : Persist.Conn cap -> Int -> Int -> Int -> Task Error ()
transferPoints conn fromId toId n =
    Persist.transaction conn
        (\tx ->
            Persist.txGet tx users (String.fromInt fromId)
                |> Task.andThen (\mFrom ->
                Persist.txGet tx users (String.fromInt toId)
                    |> Task.andThen (\mTo ->
                        case ( mFrom, mTo ) of
                            ( Just a, Just b ) ->
                                Persist.txPut tx users { a | age = a.age - n }
                                    |> Task.andThen (\_ ->
                                       Persist.txPut tx users { b | age = b.age + n })
                            _ ->
                                Task.fail (Error.unexpected "account not found")))
        )
-- Body is PURE (no Cmds). Embedded: snapshot read + index-range read-set validation
-- → SSI (serializable), bounded auto-retry (then typed Conflict). SQL: BEGIN/COMMIT.
-- Cluster: deterministic replay.
```

### 5.4 Reactivity (L4 — query-scoped, falls out of commit)

```elm
-- Declarative: a query result folded into the Model, kept live by the commit path.
main =
    app
        (config { init = init, update = update, view = view
                , subscriptions = subscriptions, routes = routes, notFound = Home }
            |> Live.withReactive
                 [ Persist.liveInto users activeAdults .visibleUsers ]   -- ← query-scoped
        )

-- Or the whole-Model magic (Model IS the collection):
main = app (config { … } |> Live.autoBlueDB)

-- Or manual: a nudge Sub for bespoke handling.
subscriptions model =
    Persist.watch users activeAdults OnUsersChanged     -- delivers a scoped Change
```

`liveInto`/`watch` register `(collection, resolvedCond)`; the commit path evaluates
each committed row-change against the predicate (`bluedbChangeAffectsQuery`,
promoted) and pushes a precise delta to affected sessions over the existing SSE +
broker — scoped to the verified tenant.

### 5.5 Config — one `[data]` section (subsumes three)

```toml
name  = "myapp"
entry = "src/Main.sky"

[data]
backend = "embedded"          # embedded (default) | postgres | cluster
path    = "data/app.blue"     # embedded file
# url   = "DATABASE_URL"      # backend=postgres
# url   = "BLUEDB_CLUSTER_URL"# backend=cluster
scope   = "user"              # session | user | tenant | global  (reactive sync unit)
consistency = "strong"        # strong (default) | snapshot | bounded <ms> | eventual

# sessions + app data + analytics ALL live here. Reactive. One store.
# Graduate embedded → postgres → cluster by changing `backend` — app code unchanged.
```

Replaces today's `[database]` + `[live].store`/`storePath` + `[analytics].dbPath`
(`docs/bluedb/README.md:151-177`).

### 5.6 Migration — one command, one diff

```bash
sky data migrate --gen [name]   # diff declared collections vs recorded schema → migration file
sky data migrate                # apply committed migrations (checksummed _sky_migrations ledger)
sky data status                 # applied / pending, spanning session + app + analytics stores
```

DB-free diff of declared L0 collections vs the recorded schema snapshot,
dialect-safe, spanning **all** stores in the one `[data]` backend. Reuses the
file-migration machinery (`Std.Db.Migrate`, PORT). Layout-version-in-manifest +
boot-migration + write-version-last crash safety carries forward
(`docs/bluedb/schema-enforcement-design.md:46-56`).

### 5.7 Auto-admin (honest scope — grill fix #10)

`/_sky/console` → **Data** tab, auto-derived from L0. **What ships is a READ-ONLY
browser + `Cond` filter:** browse any collection (ordered range scan + cursor
pagination) and run a `Cond` filter. This is achievable and ports — `DataTab.sky`
is read-only today (per its own header). The endpoint stays hardened (bearer, no
loopback bypass, readwrite-gated per env, custom header vs CSRF, values bounded,
every mutation audit-logged, session stores excluded from writes —
`docs/bluedb/README.md:141-149`).

**A scalar-field EDIT form is a future add-on with documented limits — NOT a port,
and gated.** It is net-new work, and:
- It works only for **flat SCALAR fields.** The codec maps relations / enum-choices
  / validation / nested records to a JSON blob; the generic form cannot structure or
  validate those — they are out of scope for the auto-form.
- **It is GATED on the open `record_fieldset_collision` codegen bug.** The generic
  `{field, value}` form record shape is exactly what triggers the CoerceFailure
  (`record_fieldset_collision_erased`, memory). The edit form cannot ship until that
  bug is fixed, or it must use a tuple (not a named `{field,value}` record) as the
  documented workaround.

So: auto-derived READ browse + filter now; scalar-only edit form later, with limits.

---

## 6. Concurrency + crash-consistency model (spelled out)

### 6.1 Write path (single committer, MVCC, group commit)

1. All mutations — single-key writes and transaction commits alike — funnel to
   **one committer goroutine per open file**. This is the permanent, correct floor
   (`docs/bluedb/roadmap.md:199-201`); "more writers" = more concurrency *into* it.
2. The committer assigns a strictly-monotonic HLC `commitTs`, runs read-set
   validation for transactions (Decision 4), and writes versioned keys
   (`<user-key> 0x00 <inverted commitTs>`) plus the commit-metadata key into **one
   Pebble atomic batch**, then `Apply(Sync)` (group-committed: one fsync amortized
   across all writes queued in the ~1ms window — the pattern from `db.go:445-559`).
3. **Ack only after `Apply(Sync)` returns** — the durability contract
   (`docs/bluedb/durability.md:7`). A commit is durable iff its Pebble batch is on
   disk; commit metadata cannot diverge from data (same batch).

**Bounded retry + hot-key fallback (grill fix #4 — closes the R4×R1 UI-freeze).**
Optimistic abort+retry (Decision 4) under a contended read-modify-write key can
STARVE an individual transaction; combined with persist-before-ack (R1) the losing
user's SSE frame would never ack → **the UI freezes**. The rebuild bounds this:

- **Retry BOUND.** A transaction retries at most N times with backoff. On exhausting
  the bound it returns a typed **`Conflict`** error into `update()` (the standard
  `Result Error a` convention) — the app decides (surface a toast, re-fetch, etc.).
- **The frame MUST ack on the error path.** Even when the transaction fails with
  `Conflict`, the SSE frame is acked (with the error result) — it MUST NEVER hang.
  This is the specific fix for the R4×R1 interaction.
- **Committer-ordered pessimistic fallback for detected hot keys.** The single
  committer detects repeated aborts on a specific key and switches THAT key to
  serialized execution: it queues the contending writers and re-runs each pure body
  against the just-committed state in arrival order. This is bounded + starvation-free
  and is a **natural extension of the single-committer floor** — NOT a new general
  lock manager. Contention resolves in FIFO order instead of livelocking.
- **Append-only / CRDT-style shared work** modeled as **blind unique-key inserts**
  has an **empty read-set → never conflicts** (fine — it takes the fast path and
  never enters retry). Shared *counters* stay on sharded aggregates.

### 6.2 Read path (snapshot isolation, lock-free)

- A reader (query, transaction body, reactive binding) pins a Pebble **snapshot**
  + a `readTs`. `Get`/`Iter` filter to `commitTs ≤ readTs` → a consistent view,
  **no locks, no committer coordination**.
- This *structurally eliminates* the old read/write races: R2 (`concurrent map read
  and map write` crashing the server — `docs/bluedb/state-sync-and-broadcast-grill.md:17-24`)
  cannot occur (readers never touch a mutable map); the off-lock encode race
  dissolves (encode reads a snapshot, not the live model).

### 6.3 Version GC (grill fix #5 — the advancing-watermark design, not a false dichotomy)

An earlier framing posed a false either/or: a reactive reader either holds a
*continuous* Pebble snapshot (bloat) OR *re-pins* per evaluation (races GC). The
resolved design avoids both:

- **A reactive binding holds only a `readTs` + its query predicate — NOT a pinned
  Pebble snapshot.** It advances its `readTs` **forward** to each `commitTs` it
  processes from the changelog. It only ever needs versions `≥ its current
  position`, so it never pins old versions and never races GC (it moves monotonically
  with the committer).
- **GC floor = the MIN over all live readers of their current-processed
  `commitTs`** — an *advancing watermark*, not a fixed pin. Retention is bounded by
  the maximum reader lag, and the compaction filter drops everything below the
  watermark. Incremental + background, no stop-the-world.
- **The one genuinely irreducible case:** a long ANALYTICS scan that needs a
  *consistent snapshot at a single `readTs`* for minutes. That reader legitimately
  pins one timestamp. This is an honest per-query tradeoff — either accept
  bounded bloat for the scan's lifetime, OR cap snapshot age and return a
  `snapshot-too-old` error to that scan. It is NOT a free "max-snapshot-age" policy
  applied globally: a blanket cap would silently break a *legitimate* long scan, so
  the cap (if enabled) is a per-query opt-in with an explicit error, never a silent
  truncation. (See R3 — RESOLVED.)

### 6.4 Crash consistency (the guarantees, and the residual)

- **Torn-tail / power loss.** Pebble's WAL replay recovers to the last durable
  atomic batch. Un-acked in-flight writes are cleanly discarded; acked writes
  survive. This replaces the hand-built torn-tail discriminator
  (`wal.go:443-549`) with Pebble's proven recovery — and we **prove no regression**
  by running the old crash corpus (`crashsim_test.go`, `fault_test.go`) as a
  conformance oracle against the new engine.
- **Mid-file corruption.** Pebble checksums SSTable blocks + WAL records; a rotted
  block fails a read (fail-closed), it does not silently truncate. This is *better*
  than the old engine's admitted residual (rot of the last acked group silently
  truncated — `docs/bluedb/durability.md:98-119`), which does not recur because
  Pebble does not group-truncate on a checksum failure.
- **HLC restart safety (grill fix #6 — REQUIRED).** The same-atomic-batch discipline
  closes metadata/data *divergence*, but does NOT alone stop `commitTs` re-issue. On
  restart the committer MUST init `HLC = max(persisted_high_water + 1, wall_clock)` —
  it must **floor the clock to the persisted high-water**, not merely read it. A
  backward clock step (NTP / VM migration / reschedule) that re-issued a used
  `commitTs` would collide two versions at one key. Enforced by: (a) an **in-batch
  invariant** — the committer refuses to Apply a data batch lacking the metadata
  high-water key; (b) a **crash-corpus test** asserting no re-issued `commitTs` after
  recovery under clock-rewind fault injection. Unsafe without both. (See R8.)
- **`verify`** (rebuilt) stays the read-only, runs-before-open, CI-gating integrity
  scanner (`docs/bluedb/backup-and-restore.md:66-72`).
- **The crash-fuzz harness is day-one, not bolted on** — "a durability claim
  without this harness green is not a claim" (`docs/bluedb/durability.md:228`). The
  injection matrix (kill -9 at every fsync boundary, torn write, 4KiB-sector
  power-loss drop, disk-full, idempotency replay — `:211-224`) reproduces against
  the Pebble engine.

### 6.5 The async-persist durability boundary (RESOLVED — the committer-gated funnel + a durability tier; see Risk R1)

The old grill's R1 — *async Model mutations (Cmd.perform completion, Time.every
tick, pub-sub, WebSocket, reactive refresh) are acked to the browser but never
persisted* (`docs/bluedb/state-sync-and-broadcast-grill.md:10-16`) — is a
**correctness gap the rebuild must close, not inherit.**

**One committer-gated chokepoint (grill fix #3).** "Route through the committer
before ack" is only structural if ALL FIVE async emit paths collapse into ONE
funnel. Those paths are: Cmd.perform completion, Time.every, pub-sub, WebSocket
delivery, and reactive refresh (`state-sync-and-broadcast-grill.md:13-16`). Every
one MUST flow through a single chokepoint — `applyModelDelta → committer.commit(Sync)
→ emitFrame` — and **NO path may emit a frame on its own.** The likely offender to
audit and re-route is WebSocket `sendToClient`, which today can deliver directly.
If any path retains its own emit, the R1 data-loss class recurs.

**A durability TIER — not fsync-per-keystroke (grill fix #3).** Persist-before-ack
applied to *every* mutation would tax the North-Star firehose: a single typing user
has NO concurrency to amortize the group-commit fsync, so a naive
persist-before-ack would pay one fsync per keystroke. The tier:
- **Ephemeral input state** (mid-type text, transient UI state) renders WITHOUT an
  fsync — it is not a semantic commit and losing it on a crash is acceptable.
- **Semantic state transitions** (a submit, a status change, anything a user would
  expect to survive a restart) persist-before-ack.
- **Plus a ~1–5 ms group-commit coalescing window** (Nagle-style) so even a single
  user's burst of semantic writes amortizes into ONE fsync.

**Explicitly NOT an option:** persist-THEN-ack with an in-flight window (ack the
frame, persist afterward) — that is **"the R1 bug renamed"** and reintroduces the
exact acked-but-not-durable gap. The ordering is persist-then-ack is forbidden;
persist-**before**-ack (within the tier + coalescing window) is the rule.

MVCC makes the semantic-path commit cheap (snapshot encode, no lock), and the
policy — "acked semantic transition ⇒ durable, for async paths too" — is enforced
structurally at the single funnel, never left per-call-site. (See R1 — RESOLVED.)

---

## 7. Phased implementation roadmap (bottom-up)

Each phase is a **shippable + grillable** unit with its own success criteria and an
explicit reuse list. Ordering is strict: no phase builds on an unproven lower one.

### Phase 0 — Architecture consult + grill (this doc)
- **Deliverable:** this doc, grilled by ≥2 adversarial reviewers on the five hard
  decisions + the risk register.
- **Success:** the build-vs-embed call, the isolation-level claim, and the
  parity-leak boundary survive adversarial review, OR are revised with rationale.
- **Reuse:** the exp/bluedb docs (`docs/bluedb/*`), this audit.

### Phase 1 — Engine substrate (Pebble + MVCC + single-writer committer)
- **Build:** the L1 `Engine` interface over Pebble — the **custom `Comparer` (Split
  + Name) FIRST** (see the irreversible-gate below), versioned key encoding
  (`<key> 0x00 <inverted HLC>`), snapshot reads, single-writer group-commit
  committer assigning HLC `commitTs` **with the restart-floor rule**
  (`HLC = max(persisted_high_water+1, wall_clock)`, grill fix #6),
  commit-metadata-in-batch + the in-batch invariant, compaction-filter GC, `flock`,
  changelog stream carrying `commitTs` **indexed by `commitTs`** (ordered, for O(1)
  validation-tail reads, grill fix #1), and the Pebble `errorfs` fault-injection
  harness (net-new — grill fix #7).
- **IRREVERSIBLE Phase-1 GATE — the Pebble `Comparer` (grill fix #7).** The custom
  `Comparer` (Split + Name) is a **day-1 format commitment**: `Comparer.Name` is
  baked into every SSTable's metadata and CANNOT change after data lands. It is
  REQUIRED for prefix-bloom point reads + compaction-filter GC (not free). It MUST
  be designed and LOCKED before the first SSTable is written. **This is the single
  irreversible decision in the whole rebuild.**
- **Build facts to bake into the runner (grill fix #7).** `CGO_ENABLED=0`
  cross-compiles Pebble to all targets; set `-tags pebblegozstd` so the cgo-RETRY
  build path stays cgo-free; silence Pebble's default `Logger`; expect +10–18 MB
  binary; trim the transitive surface (sentry/prometheus via build tags / `replace`).
- **Success:** the `Comparer` is locked; point read/write p99 at/below the old
  engine's (~1µs cached read), group-commit throughput ≥ old (~51k durable writes/s
  at concurrency — `docs/bluedb/capacity.md:56-63`), **ordered range scan
  O(log n + k)**, no RAM ceiling (spills to disk); the **old crash corpus
  scenarios pass green** on the new `errorfs` harness; and a **clock-rewind crash
  test proves no re-issued `commitTs`** after recovery (grill fix #6).
- **Reuse:** group-commit driving pattern (`db.go:445-559`); `flock`
  (`flock_unix.go`); crash/fault/backup test **scenarios** as oracle
  (`crashsim_test.go`,`fault_test.go`,`backup_test.go`) — via net-new `errorfs`, not
  the old `walWrap` hook; the ack-only-after-recoverable contract
  (`docs/bluedb/durability.md:7`).

### Phase 2 — MVCC transaction + validated commit (L2, embedded)
- **Build:** `Persist.transaction` embedded path — snapshot read + read-set
  capture + write-set buffer + committer-side validation + auto-retry; single-key
  blind-write fast path; the no-Cmds-in-txn purity gate.
- **Success:** a **serializability conformance suite** (write-skew rejected, lost
  update prevented, read-your-writes, retry-on-conflict, hot-key fast path stays
  single-append) green under `-race`; the unique-constraint edge cases from
  `docs/bluedb/schema-enforcement-design.md:92-94` (self-upsert, unique-is-serial-
  pk, NULL-skip) re-proven under MVCC.
- **Reuse:** WriteBatch atomic-multi-key *API shape* (`db.go:314-347`);
  unique/serial *contract* (`bluedb_collection_kernel.go:228`,
  `docs/bluedb/schema-enforcement-design.md:70-94`).

### Phase 3 — Logical API + backend adapters (L3)
- **Build:** re-home `Std.Persist`'s phantom-tag front + universal verbs +
  `Cond`/`Query` builder onto the new `Backend` interface; embedded adapter (over
  Phases 1–2), SQLite adapter, Postgres adapter; codec-driven schema semantics on
  all arms.
- **Success:** `examples/55-persist-query`'s **SQL≡KV parity gate** green on the
  new engine; `get/put/insert/delete/query` byte-identical results across
  embedded/SQLite/Postgres; `selectRaw` escape hatch works.
- **Reuse:** `Persist.sky:109-908` (PORT), `Store.sky:306-918` (PORT),
  `Codec.sky` (PORT), `persist_kernel.go:18` (PORT), `bluedbEvalCond`
  (`bluedb_query_kernel.go:338`, PORT).

### Phase 4 — Query-scoped reactivity in the commit path (L4)
- **Build:** promote `bluedbChangeAffectsQuery`/`bluedbQuerySub`
  (`bluedb_reactive.go:94-142`) into the commit path — register
  `(collection, resolvedCond)`, evaluate committed changes against predicates,
  fan out precise deltas to affected sessions scoped by **verified** sync unit;
  wire `Persist.live`/`liveInto`/`watch` (the `condPlan` already carried,
  `Persist.sky:968`); re-home the SSE-frame/panic-rollback tail
  (`live_reactive.go:301-366`).
- **Success:** the two-browser live-counter demo (`docs/bluedb/README.md:179-204`);
  a query-scoped test proving a delete of an on-screen row re-runs (the pk-erasing
  bug — `docs/bluedb/reactive-sync-design.md:296-301` — cannot recur); query
  **RE-EVALUATION** is **O(writes)** per tenant, not O(N×M)
  (`docs/bluedb/unit-architecture.md:170-177`); the convergence-hazard checklist
  (freshness token, periodic safety re-query, model-dependent-filter re-register,
  pre-render dirty-check, TOCTOU snapshot guard —
  `docs/bluedb/unit-architecture.md:182-197`) green.
- **Honest N-scale gate (grill fix #9).** "O(writes) per tenant" is the query
  *re-evaluation* win — it is **NOT** SSE fan-out, which is still **O(N)** to the N
  live sessions in the tenant (the same wall as any LiveView system, R7). The N=2
  two-browser demo HIDES that O(N) fan-out wall. So Phase 4 additionally requires
  EITHER: (a) a **realistic-N shared-feed fan-out benchmark** (hundreds of sessions
  per tenant) demonstrating the fan-out cost is characterized and acceptable, OR (b)
  an explicit **SCOPE decision in these criteria** deferring high-N shared feeds to
  Phase 6 (keyed render + horizontal spread) — not merely mentioned in R7 prose. The
  headline states the O(writes)-vs-O(N) distinction plainly: re-evaluation is
  O(writes); delivery to N sessions is O(N).
- **Reuse:** the two jewels (PROMOTE); verified-sync-unit scoping
  (`docs/bluedb/unit-architecture.md:16-23`, PORT); broker fan-out
  (`live_redis_broker.go`, PORT); reactive-fold-emits-no-Cmds invariant
  (`docs/bluedb/reactive-sync-design.md:319`).

### Phase 5 — DX collapse: config + migration + admin + session store (L0 end-to-end)
- **Build:** the one `[data]` config subsuming three; `sky data migrate/--gen/status`
  spanning all stores; the session-store adapter on the new engine (native expiry
  replaces the 8-byte-TTL dance — `live_store_bluedb.go`); the auto-derived Data
  tab; `Live.autoBlueDB` whole-Model magic.
- **Success:** an app deletes `[database]`+`[live].store`+`[analytics]`, adds
  `[data]`, and sessions+app+analytics unify into one reactive store surviving a
  restart; `Live.autoBlueDB` counter demo; the async-persist durability boundary
  (§6.5) enforced (acked semantic async mutations survive restart — closes R1).
- **Session-blob migration is a REQUIREMENT here, not "solved" (grill fix #8).**
  Unified `sky data migrate` **INHERITS** the session-blob gap — it does not fix it
  for free. Today the session Model blob has NO schema-version tag: a breaking Model
  change silently RESETS sessions (`migration.md:9-20`), and §5.6 scopes `sky data
  migrate` to *declared collections* only — the gob Model blob is not a declared
  collection, so the reset recurs unless we act. Phase 5 MUST therefore also: (a) add
  a **blob schema-version tag**; (b) make `sky data migrate` **COVER the session
  Model shape** (not just declared app/analytics collections); (c) provide a
  **structural** `withMigrate` (idempotent by construction, not hand-guarded); (d)
  define an **atomicity / rollback story** across the three heterogeneous
  representations (session blob, app collections, analytics) — today it is
  forward-only, no rollback, no cross-store atomicity. (See R9.)
- **Reuse:** `Std.Db.Migrate`/`Schema`/`Decode` (PORT); `SessionStore` interface
  (PORT); Data tab (`sky-bundled/console/src/DataTab.sky`, ADAPT); hardened data
  endpoint (`docs/bluedb/README.md:141-149`).

### Phase 6+ (future, designed-for-not-built) — cluster tier
- Range shards + multi-Raft + HLC + Calvin-style deterministic Sky transactions
  (L2 cluster path), Postgres-wire **read** surface for BI/interop, follower reads
  / bounded-staleness. The embedded API (Phases 1–5) does not change to reach this
  — that is the whole point of designing L2/L4 backend-agnostic now.

---

## 8. Risk register (post-grill — resolutions folded in)

> The 3-adversary grill is closed. R1/R3/R4/R5/R7/R8/R9 are **RESOLVED** with the
> designs folded into the sections above; R2 is **VALIDATED** (foundation stands);
> R6/R10 remain genuine open items for their respective phases.

**R1 — Async-persist durability boundary — RESOLVED (§6.5).** "acked ⇒ durable"
for *async* paths (Cmd completion, Time.every, pub-sub, WebSocket, reactive
refresh) is enforced structurally: all five paths collapse into ONE committer-gated
funnel (`applyModelDelta → committer.commit(Sync) → emitFrame`); no path emits on
its own (audit WebSocket `sendToClient`). A **durability tier** answers the latency
question: ephemeral input state renders without fsync; semantic transitions
persist-**before**-ack; a ~1–5 ms group-commit window amortizes a single user's
burst into one fsync. **Persist-THEN-ack with an in-flight window is rejected — it
is "the R1 bug renamed."** (`docs/bluedb/state-sync-and-broadcast-grill.md:10-16`.)

**R2 — Build-vs-embed — VALIDATED.** The grill confirmed the moat is L2/L4, not L1,
and that embedding Pebble is empirically buildable (grill A built real binaries:
`CGO_ENABLED=0` cross-compiles, `-tags pebblegozstd` keeps the cgo-retry path
cgo-free, +10–18 MB, transitive surface trimmable). Pebble is pure Go (no cgo
supply-chain surface). The foundation SURVIVED and stands. **Fallback if a later
phase disproves composition:** BadgerDB (weaker range/compaction) or bbolt (wrong
write profile, Decision 2) — but no evidence yet that we need it.

**R3 — MVCC GC vs long-lived readers — RESOLVED (§6.3).** No continuous-snapshot vs
re-pin dichotomy: a reactive binding holds only a `readTs` + predicate (NOT a pinned
Pebble snapshot) and advances `readTs` forward to each processed `commitTs`; the GC
floor is the **advancing watermark** = min over live readers of their
current-processed `commitTs` → retention bounded by max reader lag, no GC race. The
only irreducible case — a long analytics scan needing one consistent `readTs` for
minutes — is an honest per-query tradeoff (bounded bloat for the scan's lifetime, or
an opt-in `snapshot-too-old` error), never a silent global cap that would break a
legit long scan.

**R4 — Retry storms — RESOLVED (§6.1, Decision 4).** Bounded: N retries + backoff;
on exhaustion a typed **`Conflict`** error returns into `update()` AND the SSE frame
**acks on the error path** (never hangs — this is the R4×R1 UI-freeze fix). The
single committer detects a repeatedly-aborting hot key and switches it to a
**committer-ordered pessimistic fallback** (queue writers, re-run each pure body
against the just-committed state in arrival order — bounded, starvation-free, a
natural extension of the single-committer floor, not a new lock manager).
Append-only/CRDT shared work modeled as blind unique-key inserts has an empty
read-set → never conflicts.

**R5 — Parity leak — RESOLVED (Decision 5); the compile-gate over-claim is
withdrawn.** A capability UNION is NOT expressible in Sky's HM, the Postgres tag is
un-mintable (dialect is runtime, `Persist.sky:196-198`), and the backend axis is
irreducibly RUNTIME (image built once, backend injected at boot; HM types cannot
depend on a runtime value). So parity safety is a **runtime-loud HARD-FATAL boot
check + a compiler WARN + a CI/deploy preflight**, NOT a compile-time type gate.
Compile-time safety exists only on the embedded KV path where the capability is
fixed by construction. (R5's original suspicion — "does the leak escape the type
system" — was correct.) "Serializable on SQLite via BEGIN IMMEDIATE + serialized
writer" is a real single-writer serialization guarantee, documented as such.

**R6 — Verified-identity prerequisite for the reactive magic.** The whole L4
tenant-scoping is inert without framework-verified `SessionIdentity` on the
*standard* Std.Auth login path — historically `sess.identity` was populated only by
the sub-app mount gate, so for the exact tenant-isolated SaaS the magic targets,
`identityValid=false` → tier-2 never fires
(`docs/bluedb/unit-architecture.md:154-168`). **Open:** is `Live.withIdentify`
(shipped, `:385-387`) sufficient, and are re-scope-on-login/logout (F5) + expiry
re-validation (F4) enforced by default?

**R7 — The O(N) broadcast floor is fundamental — reclassification HONORED in the
Phase-4 criteria (grill fix #9).** Query-scoped deltas get us to O(writes) for the
query **RE-EVALUATION**, but delivery to N sessions in one tenant is still **O(N)
SSE fan-out** per write — the same wall as any LiveView system
(`docs/bluedb/unit-architecture.md:213-217`). The N=2 two-browser demo HIDES this.
Phase 4's success criteria now REQUIRE either a realistic-N (hundreds/tenant)
shared-feed fan-out benchmark OR an explicit criterion scoping high-N shared feeds
to Phase 6 (keyed render + horizontal spread) — not just R7 prose. The
O(writes)-vs-O(N) distinction is stated in the Phase-4 headline.

**R8 — Crash-consistency of *our* metadata on Pebble — RESOLVED, sound AS DESIGNED
with the added rules (Decision 3, §6.4).** Same-atomic-batch closes the
metadata/data *divergence* question. But it is UNSAFE without two added rules: (a)
on restart the committer inits `HLC = max(persisted_high_water + 1, wall_clock)` —
it must **floor the clock to the persisted high-water**, not merely read it, else a
backward clock step (NTP/VM-migration/reschedule) re-issues a used `commitTs` →
version collision; (b) an ENFORCED in-batch invariant (refuse to Apply a data batch
lacking the metadata key) + a **crash-corpus test** asserting no re-issued
`commitTs` after recovery under clock-rewind fault injection. Sound with the
init-floor rule + invariant + crash test; unsafe without them.

**R9 — Migration spanning three stores — RESOLVED as a Phase-5 REQUIREMENT; unified
migration INHERITS the session-blob gap, does not fix it (grill fix #8).** The
session Model blob has NO schema-version tag today (a breaking Model change silently
RESETS sessions — `migration.md:9-20`), and §5.6 scopes `sky data migrate` to
declared collections only — the gob Model blob is not a declared collection, so the
reset recurs. Phase 5 MUST: add a blob schema-version tag; have `sky data migrate`
COVER the session Model shape; provide a *structural* (not hand-guarded)
`withMigrate`; and define an atomicity/rollback story across the three
heterogeneous representations (today: forward-only, no rollback, no cross-store
atomicity).

**R10 — Reactivity on the SQL bridge (external writers).** Commit-path reactivity
is native only where *we* own the commit. A row changed by an external Postgres
writer (a cron job, another service) needs `LISTEN/NOTIFY` to reach the reactive
engine (`docs/bluedb/reactive-sync-design.md:302-306`). **Grill:** is the
LISTEN/NOTIFY bridge in scope, or do we honestly document "reactivity covers Sky-
originated writes; external SQL writes need the bridge (optional add-on)"? Silent
staleness here is the P4 pain re-emerging on the graduation path.

---

## Appendix — one-line orientation

**Carry forward:** the durability contract + group-commit driving pattern +
fail-closed recovery discipline + codec-as-schema (L0) + the `Cond`/`Query` algebra
(L3) + verified-sync-unit reactive scoping (L4) + the two dead-code jewels
(`bluedbEvalCond`, `bluedbChangeAffectsQuery`) + the OLTP-hot-path/magic-first
product bets.
**Retire (the foundation):** RAM-resident memtable + full-snapshot checkpoint +
the hand-built WAL + the manual order-preserving index kernel + the collection-
scoped re-query loop + the raw index-blind batch + the single-mutable-Model
persistence races — replaced by **Pebble + MVCC + validated-commit transaction +
query-scoped commit-path reactivity**.
**The moat was never the bytes on disk.** It is L0 + L2 + L4 — and embedding Pebble
is what frees the budget to build them right.
