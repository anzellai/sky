# Codec derivation + `Std.Db.Table` — design & production grill

> Status: **design/spec, nothing built.** This captures the plan agreed in
> discussion plus an adversarial production review. Read the grill (§4) before
> committing to build — several items are ship-blockers, not polish.

## 1. Vision

Write the mapping for a type **once**, reuse it for DB persistence *and* JSON:

```elm
type alias Product =
    { id : String, slug : String, priceMinor : Int, active : Bool
    , category : Category, note : Maybe String }

productCodec : Codec Product
productCodec = Codec.auto blankProduct        -- one line; derived

products : Table Product
products =
    Table.named "products" blankProduct        -- columns/read/write derived
        |> Table.primaryKey "id"
        |> Table.unique "slug"
        |> Table.index "category"
        |> Table.orderBy "sortOrder"

-- JSON, same codec:
Codec.toJson productCodec p                     -- : String
Codec.fromJson productCodec body                -- : Result Error Product
```

A `Table` declares only the DB-design facts the *type* can't express
(primaryKey / unique / index / orderBy / FKs / defaults). Columns, column types,
nullability, read/write, and JSON all derive from the record.

## 2. Why this needs a compiler change (the erasure wall)

- Records → Go structs (fine to reflect).
- **Nullary enums lower to a Go `int` alias** — the constructor *names are erased*
  and, worse, a nullary-enum field reflects as plain `int`, indistinguishable
  from a real `Int` field. So reflection alone can neither name it nor even
  detect it.
- Data-carrying ADTs lower to a tagged struct `{Tag, Name, Fields}` — the name
  survives for *encode*, but *decode* can't reconstruct (no name→constructor
  table).

Two compiler-emitted artifacts close this, mirroring the existing
`RegisterGobType(Record{})`:

1. **`sky:"…"` struct tags** on every record field carrying the field's declared
   Sky type (name + kind: scalar/enum/adt/maybe/record/tuple/list/money/…). This
   is what lets reflection know a field that *looks like* `int` is actually
   `Category`, and drives column naming.
2. **An ADT constructor registry** per custom type: `constructorName ↔ Tag ↔
   constructor-fn ↔ arg-count/arg-type-keys`. Enables decode (reconstruct by
   name) and readable enum names.

Tags are metadata only — no change to value representation or memory layout, so
low blast-radius. The registry is emitted in `init()` like gob registration.

## 3. Phased plan

- **P0 — Compiler: field tags + ADT registry.** Emit `sky:"…"` tags; emit
  `rt.RegisterAdt(typeKey, variants…)`. Verify: reflection can resolve every
  field's Sky type and every ADT's constructors. Gate: a roundtrip test over the
  example corpus.
- **P1 — Runtime default codec + `Std.Codec` core.** Reflection-driven
  bidirectional codec built **once per type and cached**; `Codec a` opaque;
  `Codec.auto`; hand-written combinators (`object`/`field`/`buildObject`,
  `custom`/`variantN`/`buildCustom`, `enum`, `maybe`, `list`, `tuple2/3`, `dict`,
  `recursive`, primitives, `map`). Prove against JSON first (round-trip law
  fuzzer) — JSON is the simpler target and has no schema-migration confound.
- **P2 — JSON integration.** `Json.encode value` (auto), `Codec.toJson/fromJson`,
  `Codec.toValue/fromValue`. Depth/size limits on decode. Backward/forward-compat
  field policy.
- **P3 — DB integration into `Std.Db.Table`.** Derive columns + read + write from
  the codec; DB storage rule (fields→columns, enum→TEXT, ADT/tuple/list/nested→
  JSON-in-TEXT, scalars→typed, Maybe→nullable). Keep `withCodec` (custom format)
  + raw `Std.Db.Schema` (exotic DDL) + raw SQL (queries) as escape hatches.
- **P4 — Migrations bridge.** Derived schema *generates* target DDL; integrate
  with versioned `Db.migrate` for evolution (see §4.A — this is not optional).

## 4. Production grill

Severity: **BLOCKER** (design is wrong/unsafe without it) / **MAJOR** (real apps
hit it, needs a first-class answer) / **MINOR** (document + escape hatch).

### A. Schema evolution & migrations

- **BLOCKER — derivation gives CREATE, not ALTER.** `createTable IF NOT EXISTS`
  never alters an existing table. Add a field to `Product` in v2 and the prod
  table is missing the column → inserts fail / reads error. The derived schema is
  only correct on a *green* database. **Mitigation:** the Table layer must (a)
  make the derived schema *generate* the target DDL, and (b) hand evolution to
  versioned `Db.migrate` (checksummed, forward-only). Offer `Table.diff` /
  `sky db migrate --from-tables` that emits an `ALTER` migration from
  (introspected current schema) → (derived target). Never silently rely on
  `createTable` in prod.
- **MAJOR — rename is a data-loss trap.** Rename a field → new column; old data
  orphaned in the old column, or lost. Derivation can't know it's a rename vs
  drop+add. **Mitigation:** renames are explicit migration operations; document
  that the derived schema treats a rename as drop+add and you must write the
  migration.
- **MAJOR — ADT/enum wire drift vs stored blobs.** Rename an ADT constructor or
  enum variant → old rows (TEXT name / tagged JSON) no longer decode. **Mitigation:**
  codec must support **aliases** (`Codec.variantAliases`) and a decode fallback;
  enum name changes need a data backfill migration.
- **MAJOR — rolling deploys: old + new code hit one DB.** During a rollout both
  schema versions coexist. Auto-decode of a row written by new code (extra
  column / new enum value) by old code must not crash. **Mitigation:** decode
  ignores unknown columns; unknown enum name → a decode `Result Err` the caller
  handles, never a panic. Expand-migrate-contract discipline documented.
- **MINOR — dev(SQLite)↔prod(Postgres) drift** already handled by the dialect
  mapping, but the derived-vs-migrated schemas must not diverge; the migration
  generator must use the same renderer.

**Must-not-ship-without:** a real migration story. Derived `createTable` is for
greenfield + tests; production evolution goes through `Db.migrate`, and the
derivation must *generate* those migrations rather than pretend they're free.

### B. Performance & scale

- **BLOCKER (if done naively) — build the codec once, not per value.** Reflecting
  every field of every row on every call is O(rows×fields) reflection. The
  reflection walk must produce **closures once per type**, cached in a
  concurrent registry; encode/decode of a value then runs closures, no
  reflection. `Codec.auto` must memoize. Registry lookups: precompute at codec
  build time, not per value.
- **MAJOR — `SELECT *` + JSON-in-TEXT blobs.** Derived reads fetch all columns
  incl. large blob columns; blobs bloat rows, hurt cache locality, and can't be
  indexed/queried. **Mitigation:** `Table.select` with an explicit projection +
  a projection-shaped record (join/summary pattern); document that ADT/nested
  fields become opaque blobs — normalize if hot.
- **MAJOR — materializing `List a` for huge results.** `all`/`select` build a
  full list in memory. **Mitigation:** a streaming/cursor variant
  (`Table.stream` → `Sub`/fold) for large scans; document the cap.
- **MAJOR — N+1** from no eager-loading. **Mitigation:** document the
  `WHERE id IN (…)` batch pattern; provide a `Table.selectIn` helper.
- **MINOR — allocations/GC** from `[]any` + JSON. Escape hatch: hand-written
  codec / raw SQL for the hottest 1%. Registry map reads must be lock-free
  (built at init).

**Must-not-ship-without:** codecs built once per type and cached; a benchmark
gate proving decode of N rows is O(N) closure calls, not O(N) reflection walks.

### C. Security

- **BLOCKER — mass assignment.** Auto-decoding a *whole record* from a request
  lets a client set fields they must not: `id`, `ownerId`, `isAdmin`,
  `priceMinor`, `createdAt`. This is the classic Rails strong-params CVE class.
  **Mitigation:** never bind a request straight into a persistence record. Decode
  into an explicit *input* type (only client-settable fields), or provide
  `Codec.readOnly "field"` / a `Codec.pick`/`omit` so the wire codec ≠ the DB
  record. Make the safe path the easy path; document loudly.
- **BLOCKER — untrusted-JSON DoS.** Deeply nested / huge JSON → stack overflow or
  memory blow-up during auto-decode. **Mitigation:** hard **depth limit** +
  **input size limit** + array/string length caps in the decoder; return
  `Result Err`, never panic. Wire into the existing `[live] maxBodyBytes`.
- **MAJOR — ADT reconstruction from attacker tag.** Unknown/mismatched tag or
  wrong arg arity must be a clean decode error, never a panic or type-confusion.
  Validate tag ∈ registry and arg count/types before constructing.
- **MAJOR — PII/secret auto-serialization.** Auto `Json.encode` will happily emit
  a `passwordHash`, `token`, or PII field into a response/log. **Mitigation:**
  field-level `Codec.redact`/`sensitive`, and never auto-encode a record that
  carries secrets to a client. Consider a `Secret a` type the codec refuses to
  encode by default.
- **MAJOR — identifier injection.** `findBy conn t col value` / `select`'s raw
  tail: column names must be validated against the table's known columns (or the
  set `[A-Za-z0-9_]`), and quoted. Values already parameterized. The raw `tail`
  is developer SQL — document "never concatenate user input."
- **MINOR — error messages leaking internal type structure** to clients. Split
  internal (logged) vs client-facing decode errors.

**Must-not-ship-without:** mass-assignment safety (input type ≠ record, or
field-level read-only) and untrusted-decode depth/size limits.

### D. Correctness & data fidelity

- **BLOCKER — Money/Decimal must never touch float.** Auto-deriving a `Money`/
  `Decimal` field as a JSON number / Go float is silent precision loss on money.
  **Mitigation:** the codec must recognize `Money`/`Decimal` (via the tag) and
  encode as **string** (JSON string; DB TEXT) losslessly. This is why P0 tags
  must carry "kind", not just presence.
- **BLOCKER — int64 > 2^53 in JSON.** JSON numbers are float64; large ids
  (snowflakes), nanos, and big counters lose precision. **Mitigation:** policy —
  encode `Int` as a JSON number only within the safe range, or make large-int
  fields strings via a `Codec.int64AsString`; at minimum document + provide the
  opt-in. DB side (BIGINT) is fine.
- **BLOCKER — recursive types.** `type Tree = Node Tree Tree`, comment threads,
  org charts → `Codec.auto` recurses forever building the codec. **Mitigation:**
  `Codec.recursive`/lazy self-reference (elm-codec pattern) and cycle detection
  in `Codec.auto`.
- **MAJOR — underivable types fail loudly.** Functions, `Task`/`Cmd`, opaque
  handles (`Db`, `Decoder`, `Value`), phantom types can't be codec'd. A record
  with such a field must produce a **clear error** ("cannot derive Codec for
  field `x : Task …`"), ideally at first `Codec.auto` call, never silent garbage.
- **MAJOR — 3-valued NULL / optional.** Absent key vs JSON `null` vs present; DB
  `NULL` vs empty string. Fix the policy: `Maybe a` field — absent OR null →
  `Nothing`, present → `Just`; encode `Nothing` → omit key (or explicit null —
  pick one and be consistent). Non-`Maybe` field missing on decode → error (or
  fall back to the witness's value for forward-compat — decide).
- **MAJOR — round-trip law.** `decode (encode x) == x` must hold; fuzz it. Watch:
  float rounding, `Dict`/`Set` ordering (sort keys), enum-name collisions across
  types (namespace by type), tuple-vs-list ambiguity, nested `Maybe (Maybe a)`.
- **MINOR — Float NaN/Infinity** (JSON can't represent) → error or null,
  documented.

**Must-not-ship-without:** Money/Decimal-as-string, int64-JSON policy, recursive
support, and a round-trip fuzz gate.

### E. DB production features

- **BLOCKER — composite primary keys.** `primaryKey "id"` is single; multi-tenant
  `(tenant_id, id)` and join tables need composite. **Mitigation:**
  `Table.primaryKey [ "tenant_id", "id" ]`.
- **MAJOR — transactions across Table ops.** insert/update/delete must accept a
  transaction handle so multi-row writes are atomic. **Mitigation:** the `conn`
  arg must abstract over connection *and* `withTransaction` tx.
- **MAJOR — multi-tenancy footgun.** Forgetting the `WHERE tenant_id = ?` filter
  leaks cross-tenant data. Derivation doesn't help. **Mitigation:** a
  tenant-scoped table wrapper (`Table.scoped "tenant_id" tenantId`) that injects
  the filter into every read/write; pairs with the v0.16.x SQL-WHERE gate.
- **MAJOR — id strategy.** app-generated (UUID/text) vs DB serial vs
  auto-increment — not derivable. **Mitigation:** `Table.id` (text PK) vs
  `Table.serial` declarations; `insertReturning` for DB-assigned ids.
- **MAJOR — FKs / relations.** Not derived. Model via explicit columns + explicit
  join queries (no ORM relations). Document the pattern; `Table.selectIn` helper.
- **MAJOR — reserved identifiers.** A field `order`/`user`/`select` → a reserved
  SQL column name. **Mitigation:** always quote emitted identifiers per dialect.
- **MAJOR — upsert / ON CONFLICT, created_at/updated_at, optimistic-concurrency
  version columns, soft-delete** — common needs. **Mitigation:** first-class
  `Table.upsert`, `Table.timestamps`, `Table.version` helpers, or documented raw
  patterns. At least upsert + timestamps should be first-class.
- **MAJOR — pagination.** `all` returns everything. **Mitigation:** cursor + limit
  helpers; never unbounded in prod.
- **MINOR — DEFAULT values, CHECK constraints, generated columns** → declared or
  raw Schema.

**Must-not-ship-without:** composite primary keys and transaction-compatible
insert/update/delete.

### F. Wire / API versioning

- **BLOCKER (for public APIs) — auto-codec couples the wire to the internal
  type.** Rename an internal field → silent breaking change to every API client.
  Auto-derivation is right for **internal services + DB**; **public/versioned API
  contracts should use an explicit `Codec`** as the decoupling boundary.
  **Mitigation:** document the boundary rule loudly; make explicit codecs
  ergonomic; consider per-version DTO records.
- **MAJOR — three naming worlds.** Sky `camelCase`, JSON conventionally
  `snake_case` or `camelCase`, DB `snake_case`. Auto maps field→snake_case for DB
  and (?)→ for JSON. **Mitigation:** a configurable naming strategy on the codec
  (`Codec.withNaming`), default camelCase-JSON + snake_case-DB, overridable.
- **MAJOR — forward/backward compat as types evolve** ties back to §A/§D
  optional-field + alias policy.

**Must-not-ship-without:** an explicit statement that auto-codec is for internal
+ DB, and explicit codecs are the public-API boundary — plus the naming strategy
hook.

## 5. The must-not-ship-without list (consolidated)

1. **Migrations**: derived schema *generates* `Db.migrate` DDL; `createTable` is
   greenfield/test only. (§A)
2. **Codec built once per type + cached**, with a benchmark gate. (§B)
3. **Mass-assignment safety**: input type ≠ persistence record, or field-level
   read-only/pick. (§C)
4. **Untrusted-decode limits**: depth + size + length caps, errors not panics.
   (§C)
5. **Money/Decimal as string** (never float); **int64-in-JSON** policy. (§D)
6. **Recursive-type support** + **clear errors for underivable types**. (§D)
7. **Round-trip fuzz gate** `decode(encode x) == x`. (§D)
8. **Composite primary keys** + **transaction-compatible writes**. (§E)
9. **Explicit-codec boundary for public APIs** + **naming strategy** hook. (§F)
10. **Identifier quoting** (reserved words) + **column-name validation** in
    findBy/select. (§C/§E)

## Known compiler issue (found during S1b)

**Multi-arg function values passed as arguments call curried-vs-uncurried
inconsistently.** The elm-codec `custom`/`variantN` matcher (a `\h0 h1 … value ->
case …` where each `hN` is a multi-arg handler like `String -> Int -> Value`)
panics at runtime: `skyCallDirect: argument 1 type mismatch — function expects
func(string, int), got func(interface {})`. A value of type `A -> B -> C` gets
compiled as an uncurried `func(A,B)C` at the call site but a curried
`func(A)func(B)C` at the value site; they meet at `skyCallDirect`. This is the
first-class-callable-value class (cf. the v0.18.1 fixes). **Worked around** in
`Std.Codec` by using a 1-arg encode matcher (`taggedUnion : (v -> (String, List
Value)) -> …`) — no multi-arg handler values. The underlying codegen bug should
still be fixed (enters the pipeline); until then, avoid passing multi-arg
lambdas as function arguments that are later applied to multiple args at once.

## 6. Open questions for the user

1. **Migrations**: is auto-generating `Db.migrate` DDL from the derived schema in
   scope now, or is "derived `createTable` for dev + hand-written migrations for
   prod" acceptable for v1?
2. **Mass assignment**: prefer separate input/DTO records (simple, explicit) or
   field-level `pick`/`omit`/`readOnly` on the codec?
3. **Public API wire**: mandate explicit codecs at public boundaries (auto only
   internal+DB), or invest in wire-stability tooling for auto-codecs?
4. **int64 in JSON**: numbers-in-safe-range only, or opt-in string encoding, or
   always-string for `Int64`-flavored fields?
5. **Scope of v1 DB helpers**: which are first-class vs raw — upsert, timestamps,
   soft-delete, version columns, pagination, tenant-scoping?
6. **The witness**: accept `Codec.auto blank` (explicit zero value), or invest in
   a type-directed compiler pass so `Codec.auto` needs no witness?
