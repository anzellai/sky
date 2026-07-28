# Auto-migration architecture — grilled design (v1: manual)

> Status: **design + grill.** v1 ships with **manual** `Db.migrate` (versioned,
> checksummed). This document is the correctness-first architecture for
> *generating* those migrations from the derived schema, so it can be built
> deliberately rather than bolted on. It is the answer to "we need a way to
> automate this — with a grilled design architecture to ensure correctness."

## The problem

`Store.fromCodec` / `Table` derive the **target** schema from the type. But
`createTable IF NOT EXISTS` never alters an existing table — so once the type
evolves (add/remove/retype a field), the live DB drifts from the target and the
app breaks (missing column on write, decode error on read). Deriving CREATE is
not deriving *evolution*. Migration is where most ORMs either get dangerous
(auto-drop/auto-alter that loses data) or give up (hand-written SQL forever). The
goal: **generate correct, reviewable, versioned migrations from `target − current`,
and never silently lose data.**

## The pipeline

```
derived target schema  ─┐
                        ├─→  diff  ─→  planned ops  ─→  classify  ─→  emit migration
live DB current schema ─┘                                (safe/unsafe)   (Db.migrate step)
   (introspection)
```

1. **Target** — from the codec/store shape: columns (name, type, nullable), PK,
   unique, indexes.
2. **Current** — introspected from the live DB: SQLite `PRAGMA table_info` /
   `PRAGMA index_list`; Postgres `information_schema.columns` / `pg_indexes`.
3. **Diff** — set difference by column/index name → add / drop / change ops.
4. **Classify** each op safe vs unsafe (below).
5. **Emit** — render dialect-correct `ALTER`/`CREATE`, wrap as a checksummed,
   versioned `Db.migrate` step. **Generated, not applied** — the developer
   reviews + commits it (so the migration is durable and auditable, and CI runs
   the *same* migration prod will).

## The grill — correctness hazards, each with a rule

Severity: **BLOCKER** (must be handled or the tool is dangerous) / **MAJOR** /
**MINOR**.

- **BLOCKER — rename is indistinguishable from drop+add.** `slug → handle`
  reads as "drop `slug`, add `handle`" → **data loss**. Introspection can't know
  it's a rename. **Rule:** the generator NEVER auto-drops. A removed column is
  reported as *pending drop* requiring explicit developer opt-in (`--allow-drop`
  or an explicit rename directive `Migrate.rename "slug" "handle"`). Default
  keeps the orphaned column.

- **BLOCKER — destructive/lossy changes are opt-in only.** Drop column, narrow a
  type (`text → int`, `bigint → int`), drop NOT-NULL-less... any op that can lose
  or reject data is **generated but commented-out / gated**, never silently
  applied. **Rule:** additive + widening ops auto-apply; lossy ops require
  explicit confirmation and are emitted with a `-- DESTRUCTIVE:` marker.

- **BLOCKER — adding NOT NULL to a table with existing rows fails/locks.**
  `ALTER … ADD COLUMN x NOT NULL` on a non-empty table errors without a DEFAULT.
  **Rule:** a new NOT NULL column MUST carry a DEFAULT (or be added nullable +
  backfilled + then constrained — the expand/backfill/contract sequence). The
  generator refuses a bare `ADD … NOT NULL` and emits the safe three-step form.

- **BLOCKER — SQLite's ALTER is weak; Postgres's is full.** SQLite (pre-3.35)
  can't drop columns and can't alter a column type — the portable path is the
  **table-rebuild** (create new table → copy → drop → rename) inside a
  transaction. Postgres does `ALTER COLUMN TYPE … USING`. **Rule:** the emitter
  is dialect-aware (same as `schemaRenderTable`); a "change type" op renders the
  rebuild dance on SQLite and `ALTER … TYPE` on Postgres — and both are gated as
  potentially-lossy.

- **MAJOR — type changes need a cast that can fail.** `int → text` is safe;
  `text → int` needs `USING x::int` and fails on non-numeric rows. **Rule:**
  classify each type transition (widen=safe / narrow=unsafe / incompatible=block)
  from a fixed lattice; unsafe ones are gated + carry the `USING`/rebuild form.

- **MAJOR — blob (JSON) columns evolve inside the app, not the DDL.** An ADT/
  nested field is one TEXT column; changing the ADT's shape doesn't change the
  column, but old rows hold old-shape JSON. **Rule:** the codec's decoder must
  tolerate old shapes (optional fields, variant aliases — see plan §4.A); the
  migration tool can't help here and says so. Data backfill of blob contents is
  an app-level data migration, separate from DDL.

- **MAJOR — checksums + ordering (reuse `Db.migrate`).** Each generated step is
  content-hashed; editing an applied migration is rejected (the existing
  `_sky_migrations` guard). Steps are ordered and forward-only. **Rule:** the
  generator appends a NEW step; it never mutates an applied one. Re-running the
  generator against an up-to-date DB produces an empty diff (idempotent).

- **MAJOR — introspection fidelity.** SQLite affinity vs declared type, Postgres
  type aliases (`int4`/`integer`), bool-as-int (SQLite) vs BOOLEAN (Postgres),
  bigint-vs-int. A naive diff sees spurious "changes." **Rule:** normalize both
  sides through the *same* dialect type-mapping used by `schemaRenderTable`
  before diffing, so `int` (target) ↔ `INTEGER`/`int4` (current) compares equal.

- **MAJOR — FK / dependency ordering.** Create referenced tables before
  referencing ones; drop in reverse. **Rule:** topological order by FK edges;
  cycles reported, not guessed.

- **MINOR — index/unique churn.** Add/drop indexes are non-destructive (dropping
  an index loses no data); auto-apply, but a dropped UNIQUE that the app relies
  on is flagged.

- **MINOR — dev vs prod parity.** The generator must run against the *prod*
  dialect's introspection, not dev SQLite, or it emits SQLite-shaped diffs.
  **Rule:** generate against the target deployment's driver.

## The one guarantee this must make

**No silently-lossy migration.** Additive + widening changes auto-apply;
everything that can lose or reject data is *generated but gated behind explicit
opt-in*, dialect-correct, and reviewable as a committed, checksummed `Db.migrate`
step before it ever runs in production. A tool that auto-drops or auto-narrows is
worse than hand-written SQL.

## Proposed surface (when built)

```
sky db diff            # show target − current (no changes applied)
sky db migrate --gen   # generate the next migration step from the diff
                       #   additive/widening inline; DESTRUCTIVE ops commented + gated
sky db migrate         # apply pending steps (unchanged; existing verb)
```

- `Migrate.rename "old" "new"` — an explicit directive so a rename isn't a
  lossy drop+add.
- `--allow-drop` / uncommenting a `-- DESTRUCTIVE:` line — the only ways a lossy
  op runs.

## v1 shipped — the safe additive core

`Store.migrate conn store` implements the **non-destructive** slice of the
pipeline (the #1 real-world need — a record gains a field):

- Table absent → **create** it (dialect-correct DDL from the codec).
- Table exists → introspect current columns (SQLite `PRAGMA table_info` /
  Postgres `information_schema.columns`), diff against the codec's columns, and
  **`ALTER TABLE … ADD COLUMN`** each missing one (nullable — the codec supplies
  defaults on read, so legacy rows decode cleanly). Dialect-correct type
  (`INTEGER` on SQLite, `BIGINT` on Postgres).
- **Idempotent** — re-running an up-to-date table applies nothing (returns `[]`).
- Returns the applied statements for logging/audit.
- **Never drops, renames, or retypes** — those are the gated/destructive ops
  above (§grill) and stay manual: a dropped field just leaves an orphan column
  (harmless; the codec ignores unknown columns on read), and rename/retype need
  an explicit migration so no data is silently lost.

Verified e2e on SQLite AND Postgres: a v1 `(id, name)` table migrates to a v2
`(id, name, age, email)` store — adds `age`/`email`, the legacy row decodes with
defaults, the second migrate is a no-op.

## Still to build (the destructive/diff tooling)

`sky db diff` (report `target − current` without applying) and `sky db migrate
--gen` (emit a checksummed `Db.migrate` step, additive inline + `-- DESTRUCTIVE:`
gated) — built against the grill above so "never silently lossy" is designed in.
Manual `Db.migrate` (versioned, checksummed) remains for hand-authored evolution.
