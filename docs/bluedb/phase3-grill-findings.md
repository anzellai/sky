# Phase 3 boundary grill — findings + fix plan

Grill of `feat/bluedb` @ `ba91ad41` (Phase-3c). Two fresh-context adversaries.
**Phase 3 is NOT closed** until every blocking finding is fixed + a fresh Judge PASSes.

## Grill A — SQL-arm serializability parity (3 BLOCKING)

The load-bearing goal (#2): *real SERIALIZABLE via SSI, UNIFIED across sqlite/postgres/bluedb.*
The embedded arm delivers real SSI (Phase 2). The SQL arm does NOT match it:

- **A1 (BLOCKING) — Postgres silently runs READ COMMITTED.**
  `Std.Persist.transaction` SqlConn arm (`Persist.sky:292-293`) → `Db.withTransaction`
  → `d.conn.Begin()` (`runtime-go/rt/db_auth.go:1383`) = `BeginTx(ctx, nil)` = driver
  default. pgx pool has no isolation config + no `SetMaxOpenConns(1)` (that clamp is
  sqlite-only, `db_auth.go:318-319`), so Postgres genuinely interleaves txns under
  READ COMMITTED → write-skew/phantoms. Same logical program is serializable on
  embedded+sqlite, NOT on Postgres. **Mis-documented**: `phase3-api-design.md:262-263/
  303/351` + the public docstring `Persist.sky:281` assert SERIALIZABLE. Affirmative
  false guarantee, not an honest forced-semantics subset.

- **A2 (BLOCKING) — SQLite issues bare `BEGIN DEFERRED`, not the doc's `BEGIN IMMEDIATE`.**
  Serializability on sqlite currently rests incidentally on `SetMaxOpenConns(1)`
  (`db_auth.go:319`) serializing all access onto one connection — not on the txn mode
  the design names. Stated mechanism ≠ delivered mechanism.

- **A3 (BLOCKING) — no conflict-retry + divergent error taxonomy on the SQL arm.**
  Phase-2 bounded retry is embedded-only (`embedded_kernel.go:513-533`, retries
  `bluedb.ErrConflict`). SQL arm has none. Embedded conflict → typed `Conflict`
  (code 8); SQL failure → `Ffi` (code 2) / `Unexpected` (code 10). A uniform
  `retryWith`/typed-`Conflict` loop cannot span both arms → unified-API contract
  breaks. A pg 40001 serialization_failure would leak as `Ffi`, never `Conflict`.

- **A4 (supporting) — parity gate can't catch this.** `examples/57-persist-parity`
  has zero `transaction`/concurrent/conflict/retry — single-threaded CRUD only.

## Grill B — parity honesty + materialisation + injection (2 BLOCKING + 4 substantive)

- **F1 (BLOCKING) — the parity gate does NOT fail closed.** `examples/57-persist-parity/
  src/Main.sky:157-177` `report` returns `Task.fail` on divergence, but the emitted
  `main` wraps in `rt.AnyTaskRun` (`rt.go:6000`) which returns `Ok/Err` and never
  inspects it nor `os.Exit(1)`s; `defer rt.LogPanicAndExit()` catches panics only. So a
  real KV≠SQL divergence prints both lists, skips `PARITY PASS`, and exits **0** —
  `run.sh` (which doesn't grep for `PARITY PASS`) goes green. Proven by simulating the
  exact `report` divergence → exit 0. The gate is print-and-eyeball, proves nothing in CI.
  - **General hole (separate compiler item):** ANY CLI `main : Task Error ()` that fails
    exits 0 silently. That is an "if it compiles it works" violation (a failing entry
    task should exit non-zero). Tracked below; broader blast radius (differential gate).

- **F2 (BLOCKING) — relational-only Persist apps STILL link Pebble.** `build.rs:469`
  `persist_needed = source.contains("rt.Embedded_")`. Every universal verb's `case conn
  of` has a `KvConn ->` arm calling `rt.Embedded_*` (e.g. `Persist.sky:218-231,383-388`);
  DCE is per-binding, not per-branch, so both arms always emit. A `connectRelational`-only
  app therefore emits `rt.Embedded_*` → materialises `bluedb/` + links Pebble (proven:
  42 MB binary, 12867 pebble symbols). `build.rs:467-468` + `phase3-status.md` §3c-4 claim
  the opposite. (The `01-hello-world` non-Persist claim IS true and not disputed.)

- **F3 (substantive) — non-ASCII LIKE diverges from Postgres.** `asciiFold`
  (`cond.go:226-231`) folds only A–Z; pg `ILIKE` unicode-folds (É↔é). `'ÉCLAIR' ILIKE
  '%é%'` = TRUE on pg, FALSE on embedded. Gate seed is 100% ASCII so never exercised.
  "case-insensitive ASCII" is honest for embedded+sqlite, NOT what pg ILIKE does.

- **F4 (substantive) — NULLS placement + isNull/notNull shipped but untested.** No
  nullable field, no isNull probe in the gate → forced `NULLS FIRST/LAST`
  (`Persist.sky:726-732`, `indexer.go:193-200`) + `CondIsNull` (`cond.go:75-80`) unproven.

- **F5 (substantive) — integer ordering non-discriminating.** `age` ∈ {25,30,42,55}, all
  2-digit → lexical == numeric; a text-vs-numeric collation bug would be invisible.

- **F6 (substantive) — column/table identifiers reach SQL unvalidated.**
  `renderCondSql`/`orderTermSql`/IN/`SELECT * FROM` interpolate the raw column
  (`Persist.sky:365,622-623,642,726-732`); no `[A-Za-z0-9_.]` validation (Store DOES
  validate, `Store.sky:979`). Values bind as `?` (safe); identifiers are the surface — a
  user-chosen sort column would inject.

- **F7 — CLEARED.** `Db_dialect` classification sound; no misclassifying pg driver string.

## Consolidated fix plan (this closes Phase 3)

**Batch 1 — serializable SQL transaction (A1/A2/A3), core goal #2.**
- New serializable-txn kernel: pg → `BeginTx{Isolation: LevelSerializable}` (pg SERIALIZABLE
  = SSI, exact parity w/ embedded); sqlite → driver-correct `BEGIN IMMEDIATE` (verify the
  actual sqlite driver honours it; DSN `_txlock=immediate` or raw). Keep generic
  `Db.withTransaction` (default isolation) for raw Std.Db users.
- Bounded conflict-retry catching pg 40001 serialization_failure + sqlite BUSY/locked →
  map to the typed `Conflict` error (code 8, matching embedded) + retry with the same
  bounded policy. One uniform error+retry contract across arms.
- Truthful docs: correct `phase3-api-design.md` + `phase3-status.md` + `Persist.sky:281`
  docstring to the ACTUAL per-backend mechanism.
- **Discriminating proof:** a two-writer write-skew e2e that FAILS under READ COMMITTED and
  PASSES under the fix, on live pg (CI-gated) + sqlite + embedded.

**Batch 2 — gate integrity (F1-local, F4, F5, F3).**
- Parity gate fails closed: `report` divergence → `System.exit 1` (via `Task.onError`), and
  `run.sh` asserts `PARITY PASS` present + exit 0. Simulated divergence MUST exit non-zero.
- Add nullable column + isNull/notNull probes (F4); discriminating int values {2,10,100} (F5).
- F3: document the LIKE forced-subset as ASCII-only precisely (non-ASCII case-fold is OUT of
  the forced subset, backend-specific) + a test proving ASCII case-insensitivity is identical
  three-way. (Non-blocking; honest scoping, not a defer.)

**Batch 3 — security (F6).** Validate column/table identifiers `[A-Za-z0-9_.]` in Persist's
renderer (reuse the Store convention); a genuine typo/injection fails fast before SQL.

**Batch 4 — materialisation honesty (F2).** Prefer the clean fix: make a relational-only
Persist app NOT reference `rt.Embedded_*` (evaluate: split the embedded impl so its kernels
are only statically named when the embedded connector is imported). If that is a large
architectural refactor beyond this phase, correct the claim to the TRUTH (any Persist app
links Pebble; only non-Persist apps skip it) in `build.rs` + `phase3-status.md`, add a
relational-only example proving it builds+runs, and file the split as an explicit tracked
Phase-4/5 optimisation (honest scoping with the reason, NOT a silent defer).

**Separate compiler item (F1-general):** CLI `main : Task Error ()` failing → exit non-zero.
Compiler/runtime-semantics change (needs differential oracle + example sweep). Handle after
Phase-3 stdlib batch; do not bundle with the stdlib fix.

## Fix plan (drafted; finalise after Grill B)

1. **Serializable SQL transaction kernel.** Persist SQL-arm `transaction` must issue
   the strongest isolation matching embedded SSI:
   - Postgres: `BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})`
     (pg SERIALIZABLE = SSI — exact parity with embedded).
   - SQLite: `BEGIN IMMEDIATE` (upfront write lock; with the single-writer clamp =
     serializable, and no lock-upgrade deadlock). Verify `sql.LevelSerializable`
     handling per driver; fall back to explicit `BEGIN IMMEDIATE` if the driver
     ignores TxOptions.
   Keep generic `Db.withTransaction` (default isolation) for raw Std.Db users; add a
   distinct serializable entry the Persist arm calls.
2. **Bounded conflict-retry + typed `Conflict` on the SQL arm.** Catch pg 40001 (and
   sqlite SQLITE_BUSY/locked), map to the typed `Conflict` error, retry with the same
   bounded policy as embedded → one uniform error+retry contract across arms.
3. **Docs truthful.** Correct `phase3-api-design.md` + `phase3-status.md` +
   `Persist.sky:281` docstring to the ACTUAL delivered mechanism per backend.
4. **Concurrent write-skew parity proof.** Extend the parity gate (or a new e2e) with a
   two-writer write-skew scenario that FAILS under READ COMMITTED and PASSES under the
   serializable fix — proving A1/A4 closed on a live Postgres (CI-gated) + sqlite +
   embedded.
5. Re-run: cargo build, go test -race, parity gate, hello-world-no-bluedb, then a
   fresh-context Judge on the verbatim goal.
