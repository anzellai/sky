# BlueDB Phase 3 — status

> Tracks the phased sub-plan in `phase3-api-design.md` §7. Phase 3a is the Go embedded core +
> the SSI-wiring soundness, provable with Go tests. Phases 3b and 3c are the Sky port + the SQL
> adapters/parity.

## Phase 3a — Go `Backend` + embedded adapter + real indexer + SSI-wiring soundness — **DONE**

Landed in `runtime-go/bluedb/`. Builds `go build ./bluedb/...`, cross-compiles
`CGO_ENABLED=0 GOOS=linux GOARCH=amd64`, `go vet` clean, `go test ./bluedb/ -race` green
(64 prior + 12 new; the new SSI-soundness suite + contention suite green at `-race -count=5`,
no hang). Blind-write throughput unchanged (~50k durable-writes/s — the adapter never taxes the
engine blind fast path). NO on-disk format change (the comparer / `skydb.mvcc.v1`, the commit +
durability path, and `encodeIndexKey` are untouched).

### What shipped (Go only — provable with Go tests)

1. **Multi-collection `Txn` engine change (§2.1)** — `txn.go`. A `Txn` now carries an optional
   per-change `collResolver func(userKey []byte) CollID` (installed via `SetCollResolver`);
   `buildReq` stamps each emitted `KeyChange.Coll` via `collOf(uk)` — the resolver's per-change
   attribution from the `collName ‖ 0x1F ‖ pk` userKey prefix, else the single `SetCollection`
   fallback. The single-collection Phase-2 behaviour is preserved (resolver nil ⇒ `tx.coll`), so
   all 64 prior tests stay green. The installed indexer is likewise ONE multi-collection closure
   that parses `collName` from the userKey (its signature is unchanged — the userKey already
   namespaces the collection). NO key-format change.
2. **Go value types + `Backend` interface (§1.2)** — `backend.go`. `CollSchema` / `ColSpec` /
   `IndexSpec` / `ColValue` / `QueryPlan` / `OrderSpec`, the `Backend` interface (Get / Put /
   Insert / Delete / Query / Count / Transaction / SelectRaw / Capabilities / Close), the
   `TxHandle` txn surface, the separate `CrossInstanceReactive` interface (+ `Subscription` /
   `Change` seam), `Capabilities`, and the data-key + reserved-unique-keyspace layout
   (`0x1F` data separator, `0x1E` unique-key tag).
3. **Embedded adapter (`*EmbeddedBackend`, §3.3)** — `embedded.go`. Implements `Backend` +
   `CrossInstanceReactive` over the Phase-1/2 `Engine`: blind CRUD writes + snapshot reads,
   Query/Count as PK-ordered scan + in-RAM `bluedbEvalCond`, Transaction over `Engine.Transact`
   with the read-set contract, generated-field fill (serial PK + defaultNow), a per-collection
   schema registry driving the resolver + indexer.
4. **Codec-driven indexer `buildIndexer(CollSchema)` (§2.2)** — `indexer.go`. Emits one
   `IndexCoord` per declared single-column-ascending index via the SAME `encodeIndexKey` the
   scan-bound builder uses (byte-match by construction — R-2.1 at the L3 boundary; asserted by
   the encode-identity property test). NULL/absent emits no coord (§2.3). Record↔column mapping
   is the codec JSON blob (`decodeColumns`) with the value constructors
   (`IntVal`/`TextVal`/`BoolVal`/`RealVal`/`MoneyVal`/`BlobVal`) producing byte-identical
   normalization.
5. **txn-`Query` read-set contract (§2.6)** — `embeddedTx.Query` + `classifyIndexable` (`cond.go`).
   A single indexable range/eq leaf on a declared range-optimized index → `Txn.ScanRange`
   (precise index-range read-set) + residual in-RAM filter; anything else → `Txn.ScanCollection`
   (records the collection witness AND materializes — never a bare `reader.Iterate` that records
   nothing).
6. **IS-NULL + not-orderable → fallback witness (§2.3)** — an `isNull`/`notNull` leaf, and a
   predicate on a not-order-preserving `ColType` (Money/Decimal/blob/real — passed as an explicit
   not-orderable engine `ColType` in the `CollSchema`), route to `WitnessCollection`, never an
   index byte-range. A not-orderable column NEVER yields a range coord for validation.
7. **`unique` via SSI (§2.7)** — `txWrite`/`txDelete` read-then-write a reserved stored
   unique-index point key `collName ‖ 0x1E ‖ indexName ‖ 0x1F ‖ encodeIndexKey(value) → pk`. Two
   concurrent inserts of the same value conflict at validation (the loser's point read of the
   unique key hits the winner's write); the loser's retry re-reads the now-present key and returns
   a deterministic `ErrUniqueViolation`.
8. **Go SSI-soundness conformance suite** — `phase3a_ssi_test.go` + `phase3a_test.go`:
   `TxnQueryPhantomRejected`, `MultiCollectionStamping`, `NotOrderableMoneyFallback`,
   `IsNullFallback`, `UniqueViaSSI`, `RangeOptimizedPrecise`, `IndexerEncodeIdentity`,
   `IndexerNullEmitsNoCoord`, `CRUDRoundTrip`, `QueryOrderingAndPaging`, `BlindPutStaysOnFastPath`,
   `CapabilitiesAndSeams`.

### Deferred with the adapter (documented `// TODO(phase3b/c)` seams)

- **General stored secondary-index seek keys** — Phase 3a Query is PK-scan + in-RAM eval + the
  read-set contract (correct + parity-provable). The O(log n + k) seek fast-path over stored
  secondary-index keys is Phase 3b/4. (The STORED unique-index point keys of §2.7 are NOT
  deferred — they ship in 3a.)
- **Durable serial across restart** — the generated-PK sequence is an in-process counter in 3a;
  a durable engine sequence is a Phase-3b refinement.
- **`CrossInstanceReactive.Watch` commit-path evaluation** — the seam ships in 3a
  (`EmbeddedBackend.Watch` returns `ErrReactiveSeamPhase4`); Phase 4 wires the commit-path
  evaluation of the resolved plan.
- **Composite + descending indexes** — out of v1 scope (§2.5): L0's `index : String` is
  single-column ascending only; they need NEW L0 builders. The engine's `encodeCompositeKey` +
  `Descending` flag exist but are not reachable from L0 in v1.

## Phase 3b — Sky `Std.Persist` port + `Std.Codec` non-order-preserving MARKER + a real Sky e2e — **DONE**

The first real Sky↔Go↔engine use of the embedded backend. `sky build` + `sky run` of
`examples/56-persist-embedded` exercises insert / equality-query / range-query / count /
transaction (read-modify-write) on the new engine with correct asserted output.

### What shipped

1. **Go `rt.Embedded_*` kernels (§6)** — `runtime-go/rt/embedded_kernel.go`. Thin
   `Ffi.kernel`-shaped bridges (`func(...any) any` returning a `func() any { Ok/Err }` Task thunk,
   mirroring the `Db_*` kernels): `Embedded_open`/`Embedded_get`/`Embedded_put`/`Embedded_insert`/
   `Embedded_delete`/`Embedded_query`/`Embedded_count`/`Embedded_transaction`/`Embedded_selectRaw`.
   They decode Sky args (a JSON **schema descriptor** → `bluedb.CollSchema`, a JSON row blob, a
   JSON **plan** → `bluedb.QueryPlan`), call `bluedb.EmbeddedBackend`, and Task-shape the result.
   The codec NEVER crosses to Go — the boundary is JSON strings only; the CollSchema + the row blob
   drive the indexer + read-set. **Handle unification:** `connectKeyValue` mints a
   `*EmbeddedBackend`; a `transaction` body is handed the `bluedb.TxHandle` — BOTH satisfy
   `bluedb.TxHandle`, so the CRUD/query kernels are autocommit-vs-txn agnostic (§2.6). A
   path-keyed registry dedupes engines so a memoised `connectKeyValue` CAF shares one engine.
2. **`Std.Codec` non-order-preserving marker (§2.3)** — `sky-stdlib/Std/Codec.sky`. New `ColType`
   case `CNotOrderable ColType` (carries the physical inner type). `Codec.map` now SETS it on any
   scalar shape it wraps (`notOrderableShape` — Money-as-text, `intBool`, any bijection whose byte
   order the codec can't vouch for). `colTypeKind` maps `CNotOrderable inner` transparently to
   `inner`'s PHYSICAL kind (so `Std.Db.Store`/DDL are unchanged — the two `ColType` matches in
   `Codec.colTypeKind` + `Store.colTypeStr` both keep the physical mapping). The NEW exposed
   `colEngineKind` SURFACES the marker → `"notorderable"`, which the embedded backend maps to the
   fallback `ColBlob` (never range-optimized). Existing order-preserving scalars (`CInt`/`CText`/
   `CBool`) are untouched.
3. **`Std.Persist` embedded arm (§1/§3)** — `sky-stdlib/Std/Persist.sky` (new). The phantom-tag
   `Conn cap` (KV arm), `Collection a` (`collection`/`key`/`index` builders over a `Codec a`),
   `connectKeyValue`, the universal verbs (`get`/`put`/`insert`/`delete`/`all`/`count`/
   `transaction`) dispatching via `case conn of` to the `Embedded_*` kernels, and a self-contained
   `Cond`/`Query` builder (`where_`/`eq`/`gt`/`orderAsc`/`toList`/`toCount`/…) that serializes to
   the plan JSON the kernel decodes. `colTypeFor` uses `Codec.colEngineKind` and routes an
   UNRESOLVED field to the fallback (§2.3), NOT range-optimized text. The SqlValue currency is
   reused from `Std.Db`.
4. **Sky e2e** — `examples/56-persist-embedded`. Declares a `Todo` collection (`Codec.auto` +
   indexes on `priority`/`done`), inserts 4 rows, runs `eq`+`orderAsc` and `gte`+`orderDesc`
   queries, counts, and a `transaction` read-modify-write, printing asserted results.

### Packaging note (build-system, NOT compiler-semantics)

The §6 zero-**compiler**-change claim HELD end to end: no change to `hir`/`ty`/`lower`/`kernel_api`
— `Ffi.kernel "Embedded_*"` resolves generically to `rt.Embedded_*` via the lowerer's
`alias_go_name` fallthrough, and the Sky pipeline (parse→canon→type→lower) accepts the new stdlib
unchanged. But `rt/embedded_kernel.go` is the FIRST `rt → sky-app/bluedb` import, and the
`bluedb` package was not previously shipped into user projects, so two **build-system** files
changed to materialise it (packaging, not compiler semantics):
`rust/crates/ffi/build.rs` (`stage_runtime` now stages `runtime-go/bluedb`) and
`rust/crates/project/src/build.rs` (`write_out` now materialises `bluedb/` beside `rt/`). In 3b
this was UNconditional (every project compiled the Pebble subtree); Phase 3c makes it conditional
(below).

### Deferred to 3b→later (documented)

- **Codec.map marker through `autoWith` overrides** — a hand-written `Codec.object |> field`
  codec preserves the `CNotOrderable` marker (via `colTypeOf`); an `autoWith` override round-trips
  through kind-STRINGS (`autoColsK`) and loses it. Fine for 3b (queries on mapped-override columns
  aren't a target); a full fix threads a `"notorderable"` string through the auto-cols kernel.
- **Collection `unique`/`serial`/generated builders** — the embedded engine + `Embedded_insert`
  support generated-PK fill + `unique` SSI, but `Std.Persist.Collection` only exposes
  `key`/`index` in 3b (records carry their own PK). Surfacing `unique`/`serial` on the builder is
  additive.
- **`watch`/`live`** — single-instance in-process pub/sub reactivity is NOT wired in this Sky
  surface yet (the engine seam is Phase 4). Deferred with the reactive path.

## Phase 3c — SQL adapters + dialect-aware renderer + KV≡SQL parity + conditional materialisation — **DONE** → Phase 3 COMPLETE

The relational (`SqlConn`) arm of the ported `case conn of` (Design B — NO new Go SQL `Backend`),
the dialect-aware forced-semantics renderer, the runnable KV≡SQL parity gate (proven LIVE against
embedded + SQLite + Postgres), and the conditional-materialisation build fix.

### What shipped

1. **Std.Persist relational arm (§1/§3/§4.1)** — `sky-stdlib/Std/Persist.sky`. New `Relational`
   phantom tag + `SqlConn Db` constructor + `connectRelational : () -> Task Error (Conn Relational)`.
   Every universal verb (`get`/`put`/`insert`/`delete`/`toList`/`toCount`/`transaction`) gains a
   `SqlConn db ->` arm; `selectRaw` is added (SQL-native on the relational arm; embedded is
   single-collection-scan-only, §4.4). CRUD + codec-driven DDL reuse `Std.Db.Store`
   (`fromCodec`/`primaryKey`/`create`/`upsert`/`insert`/`findBy`/`delete`); queries render
   dialect-aware SQL in Sky and decode via the reused `Db_queryObjects` kernel (row columns →
   codec JSON → `Codec.fromJson`). `transaction` → `sqlSerializableTransaction`
   (`Db_withSerializableTransaction`, see the boundary-grill closure §"A1/A2/A3" below), NOT the
   generic `Db.withTransaction`. The `case conn of` dispatch stays in Sky; the SQL text is never
   re-rendered in Go (Design B).

2. **Dialect-aware, forced-semantics renderer (§0.6/§4.1)** — in `Std.Persist` (NOT `Std.Db.Store`,
   so legacy `Store` SQL users are byte-for-byte UNCHANGED — the "gate the new behaviour"
   requirement). Forced semantics a single logical query renders identically on BOTH dialects:
   - **`ORDER BY` null placement** — explicit `NULLS FIRST` (ascending) / `NULLS LAST` (descending)
     on both SQLite and Postgres, matching the embedded `orderAndPage` (`runtime-go/bluedb/indexer.go`).
   - **`LIKE` collation** — forced **case-insensitive ASCII**: `LIKE` on SQLite (already
     ASCII-case-insensitive) / `ILIKE` on Postgres. The embedded `likeMatch`
     (`runtime-go/bluedb/cond.go`) now folds ASCII case to mirror it. The dialect is read via the
     new pure `Db.dialect : Db -> String` kernel (`Db_dialect` → "postgres"/"sqlite").
   Injection-safe (values bind as `?` params; `Db.query`'s `rebind` rewrites to `$n` on Postgres).

3. **KV≡SQL parity gate (§0.6/§8)** — `examples/57-persist-parity` (+ `run.sh`). The SAME
   `Collection` + `Cond`/`Query`/CRUD source runs on the embedded engine AND a relational backend;
   the program self-asserts byte-identical results on the forced-semantics subset (equality, non-null
   AND nullable `ORDER BY` with forced NULLS FIRST, DISCRIMINATING integer ranges/order (2/10/42/100),
   `inList`, `isNull`/`notNull`, case-insensitive ASCII `LIKE`, `or_`+`orderDesc`, count, insert). The
   gate is **fail-closed** (`System.exit 1` on divergence — see the boundary-grill closure §F1 below).
   Proven LIVE:
   - **embedded ≡ SQLite** — self-contained, `sky run` prints `PARITY PASS` (the always-runnable gate).
   - **embedded ≡ Postgres** — `DATABASE_URL=postgres://… ./run.sh` (run manually; verified live
     against a Postgres 16 instance, `PARITY PASS`). NOT CI-gated: `scripts/example-sweep.sh` runs
     `57-persist-parity` via its `run.sh` with no `DATABASE_URL`, i.e. the embedded ≡ SQLite arm only.
     The CI job that owns a `postgres:16` service container (`integration-postgres`) sets up Go but no
     Rust toolchain, so it cannot build the compiler this example needs. The discriminating
     SERIALIZABLE-vs-READ-COMMITTED proof that IS CI-gated is `rt.TestWriteSkewPostgres` (below).
     The `%a%` LIKE probe is the discriminator: it returns
     `Alice` (capital A) on Postgres, proving `ILIKE` (not a case-sensitive bare `LIKE`) was emitted.
   Unit coverage without a live Postgres: `runtime-go/rt/db_dialect_test.go` (dialect classification)
   + `runtime-go/bluedb/like_test.go` (forced case-insensitive ASCII `LIKE`).

4. **Conditional `bluedb` materialisation (the 3b build regression fix)** —
   `rust/crates/project/src/build.rs`. `write_out` materialises `bluedb/` (and the `rt` files that
   import `sky-app/bluedb`) ONLY when the emitted `main.go` statically references an
   `rt.Embedded_*` kernel. **The importer set is DERIVED, not listed.** It was a hardcoded name list
   when `rt/embedded_kernel.go` was the only importer; Phase-4b and Phase-5e each added another
   (`rt/bluedb_reactive.go`, `rt/console_data.go`) and the Phase-5e one was not added to the list,
   which broke `go build` for EVERY non-Persist example with `package sky-app/bluedb is not in std`.
   `materialise_rt` now reads each file's imports (`go_file_imports_bluedb`), gated by
   `project::build::every_bluedb_importing_rt_file_is_skipped_without_persist`. **HONEST SCOPE (boundary-grill F2 — corrected):** the gate is really
   "non-Persist app" vs "ANY Persist app", NOT "relational-only" vs "embedded". Every universal verb's
   `case conn of` has a `KvConn -> rt.Embedded_*` arm, and Sky's DCE is **per-binding, not per-branch**,
   so a `connectRelational`-only program STILL emits `rt.Embedded_*` and STILL links Pebble. Verified:
   `examples/01-hello-world` (non-Persist) clean-builds with NO `sky-out/bluedb/` and NO
   `sky-out/rt/embedded_kernel.go`; `examples/58-persist-relational-only` (relational-only Persist)
   builds + runs correctly but DOES link Pebble (honest); `examples/56-persist-embedded` gets bluedb and
   runs. **Tracked optimisation (Phase 4/5):** pruning Pebble from a relational-only Persist app needs
   per-branch DCE (or splitting the embedded-arm kernels behind a module the relational path never
   imports) so the unreached `KvConn` arm's `rt.Embedded_*` references vanish — a compiler-DCE change
   beyond Phase 3. Documented here, not silently deferred.

### Verification

`go build ./...` + `go vet ./rt/ ./bluedb/` clean; `go test ./rt/ ./bluedb/ -race -count=1` green.
`cargo build --release -p sky` clean. `examples/57-persist-parity` embedded≡SQLite≡Postgres `PARITY
PASS`; `examples/56-persist-embedded` builds+runs (with bluedb); `examples/01-hello-world` builds
WITHOUT bluedb. `sky fmt` idempotent on the edited `.sky`. **No compiler-semantics change** — the
relational arm is pure stdlib Sky + reused `Std.Db` kernels; the only new kernel is the pure
`Db_dialect`, resolved generically (no `hir`/`kernel_api` entry).

## Phase-3 boundary-grill closure (A1–A3, F1–F6)

A 2-adversary boundary grill (`docs/bluedb/phase3-grill-findings.md`) found the SQL-arm
`transaction` did NOT match the embedded serializable guarantee and the parity gate did not fail
closed. Closed as follows:

- **A1/A2/A3 — serializable SQL transaction (the core fix).** The Persist SQL arm's `transaction`
  now dispatches to **`Db_withSerializableTransaction`** (`runtime-go/rt/db_auth.go`), NOT the
  generic `Db.withTransaction` (which was `d.conn.Begin()` = default isolation = READ COMMITTED on
  Postgres → write-skew/phantoms). Per backend:
  - **Postgres** → `BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})` = real
    SERIALIZABLE (= SSI, exact parity with the embedded engine).
  - **SQLite** → an explicit `BEGIN IMMEDIATE … COMMIT` over a **pinned `*sql.Conn`** (an upfront
    write lock). `modernc.org/sqlite` IGNORES `sql.TxOptions.Isolation` and only honours a
    per-connection `_txlock` DSN, so `BeginTx(LevelSerializable)` would silently run `BEGIN
    DEFERRED` — the immediate lock is driven manually. (Note A2: with `SetMaxOpenConns(1)` the two
    transactions are ALSO serialized onto one connection, so on SQLite the isolation mode cannot be
    the DISCRIMINATOR — `BEGIN IMMEDIATE` is the STATED mechanism, MaxOpenConns(1) is the
    additional serializer. The discriminating proof is on Postgres.)
  - **Bounded conflict-retry** (maxAttempts = 8, mirroring `bluedb.Transact`): a pg `40001`
    serialization_failure / `40P01` deadlock (and a SQLite BUSY/locked) maps to the **SAME typed
    `Conflict` Error (code 8)** the embedded arm now surfaces (`embeddedWriteErr` maps
    `bluedb.ErrConflict` → `ErrConflict`), and is retried under the same bound. So a caller's uniform
    `Task.retryWith` / typed-`Conflict` handling spans BOTH backends. The generic `Db.withTransaction`
    is UNCHANGED (default isolation, no retry) for raw `Std.Db` users.
  - **Discriminating proof:** `runtime-go/rt/persist_writeskew_test.go` runs the classic on-call
    doctors write-skew concurrently. On **live Postgres** it PROVES discrimination: READ COMMITTED
    → 0 on-call (invariant VIOLATED), serializable path → 1 on-call (HELD). Embedded (real SSI) and
    SQLite (BEGIN IMMEDIATE + MaxOpenConns(1)) always HOLD.

- **F1 — the parity gate fails closed.** `examples/57-persist-parity` wraps `main` in
  `Task.onError` that prints `PARITY FAIL` and calls **`System.exit 1`** (a bare `Task.fail` at
  `main` is swallowed by `rt.AnyTaskRun` → exit 0). `run.sh` additionally asserts `PARITY PASS`
  present AND exit 0. A perturbed expected value exits non-zero — the gate now proves something.

- **F4 — nullable column + `isNull`/`notNull` + nullable ORDER BY** are now exercised
  (`nickname : Maybe String`; probes for `isNull`/`notNull` + `orderAsc "nickname"` forcing NULLS
  FIRST three-way).

- **F5 — discriminating integer values.** Ages are `2/10/42/100` (lexical ≠ numeric), so an int
  `ORDER BY`/range surfaces a text-vs-numeric collation bug.

- **F3 — ASCII-LIKE scope (honest, explicit).** The forced `LIKE` collation is case-insensitive for
  **ASCII ONLY**. Non-ASCII case-folding is OUT of the forced-semantics subset and is
  backend-specific: Postgres `ILIKE` unicode-folds (`É`↔`é`); SQLite's `LIKE` and the embedded
  `likeMatch` (`asciiFold`, `runtime-go/bluedb/cond.go`) do NOT. The gate seed is ASCII so the three
  backends agree; a non-ASCII `LIKE` is deliberately not part of the parity contract. The `%A%`
  (uppercase-pattern) probe proves ASCII case-insensitivity is identical three-way.

- **F6 — identifier injection gate.** `Std.Persist` validates every column/table identifier against
  `[A-Za-z0-9_.]` (`isSafeIdent`/`guardIdents`) BEFORE it reaches the interpolated SQL (values always
  bind as `?` params — identifiers are the surface). A dynamic/typo sort or filter column fails fast
  with a clear error. Proven by `examples/58-persist-relational-only` (`IDENT GUARD OK` — a malicious
  `ORDER BY "balance; DROP TABLE accounts"` is rejected).

- **F2 — materialisation honesty** — see §"What shipped" point 4 above (corrected: any Persist app
  links Pebble; pruning it from relational-only apps is a tracked Phase-4/5 per-branch-DCE item).

### Deferred to Phase 4 (explicit, not a Phase-3 gap)

- **Cross-instance capability check (§5)** — `Capabilities()` is reported per backend today
  (`bluedb.EmbeddedBackend.Capabilities`), and single-instance watch is NEVER gated. The boot /
  first-subscription hard-fatal for a multi-replica app on a non-`CrossInstanceReactive` backend is
  wired with the reactive path (Phase 4), since the reactive bindings (`Live.withReactive`/`liveInto`/
  `watchCollection`) it guards are themselves Phase 4. There is no silent-stale risk in Phase 3 —
  the Persist surface does not yet expose `watch`/`live`.
- **`selectRaw` on the embedded arm** — SQL-native on the relational arm (`Store.selectRaw`); the
  embedded arm returns the documented SQL-only error (cross-collection JOIN/GROUP BY is SQL-only,
  §4.4). Single-collection embedded `selectRaw` is a Phase-4 refinement.
- **Collection `unique`/`serial`/generated builders on the SQL arm** — the SQL DDL is codec-driven
  today (columns + PK); surfacing `unique`/`serial`/`defaultNow` on the `Persist.Collection` builder
  (so both arms enforce them from one declaration) is additive (also deferred in 3b).
