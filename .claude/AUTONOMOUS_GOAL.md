# Autonomous goal — Codec derivation stack (v0.19.x)

**Set:** 2026-07-28. **Branch:** `feat/std-analytics`. **Mode:** autonomous +
grilling. (Supersedes the completed kernel-metadata-unification + Std.Analytics
+ Std.Db.Schema/Table mandates — all DONE on this branch, ship together in
v0.19.x.)

## Verbatim user goal (the authority on "done")

> turn this into a concrete build sequence... and start autonomously. grill each
> step + implement correctly for our concerns. for migrations, for now manually?
> but we do need a way to automate this -- will need a grilled design
> architecture to ensure correctness

"this" = `docs/v0.19/codec-derivation-plan.md` (design + production grill).

## Definition of done

A working, verified `Std.Codec` + `Std.Db.Table` stack:
1. `Std.Codec` — one bidirectional codec per type drives JSON encode + decode
   (round-trip fuzz gate).
2. DB `Table` reads/writes/creates from a codec (dialect-safe SQLite + Postgres).
3. `Codec.auto` derives the codec from the record type (compiler: field tags +
   ADT registry) so enums/ADTs need no hand-mapping.
4. Every production concern from the grill (plan §4/§5) implemented OR documented
   + escape-hatched — esp. mass-assignment safety, untrusted-decode limits,
   Money/Decimal-as-string, int64-JSON policy, recursive types, composite keys,
   transaction-safe writes, public-API wire boundary.
5. Auto-migration: grilled architecture doc; v1 uses manual `Db.migrate`; derived
   schema can *generate* migration DDL.

## Build sequence (each step: grill → implement → verify → checkpoint)

- **S1 — `Std.Codec` core + JSON (pure Sky).** `Codec a` = `{ enc, dec, shape }`
  on `Sky.Core.Json.Encode/Decode`. Combinators: primitives, maybe, list, dict,
  tuple, object/field/optionalField/buildObject, custom/variantN/buildCustom,
  enum, map, lazy (recursion). `toJson`/`fromJson`. Gate: round-trip fuzz over
  records + ADTs + recursive + Money.
- **S2 — DB interpreter on `Codec`.** `Table.fromCodec` derives columns (from
  `shape`), read (decode rows), write (encode). Scalar fields→columns; nested/
  ADT/tuple/list→JSON-in-TEXT; Maybe→nullable. Composite PK, tx-safe writes,
  identifier quoting. Reconcile with shipped reflection `Std.Db.Table`.
- **S3 — Compiler: field tags + ADT registry (P0).** Emit `sky:"…"` field tags
  (name + kind incl. money/decimal/int64) + `rt.RegisterAdt`. Gate: reflection
  resolves every field's Sky type + every ADT's constructors over the corpus.
- **S4 — `Codec.auto` (runtime derive).** Reflection + registry builds a `Codec`
  once per type, cached. Underivable-type errors; recursive detection.
- **S5 — Production hardening.** Mass-assignment (`pick`/`omit`/input types),
  redaction, untrusted-decode limits (depth/size), naming strategy, wire docs.
- **S6 — Auto-migration architecture (grilled).** Introspect current schema →
  derive target → emit checksummed `Db.migrate` ops. v1 manual.

## Progress

- **S1 ✅** — `Std.Codec` core + JSON (records, primitives, Maybe, list, map).
  Round-trip verified. Commit 17c417f8.
- **S1b ✅** — ADT codecs (`taggedUnion`/`varN` + `enum`). Round-trip verified
  (enum, 0/2/3-arg variants). Commit 91f58fdc. Found + documented a compiler
  codegen bug (multi-arg function values; worked around with 1-arg matcher).
- **S2 ✅** — `Std.Db.Store` codec-driven DB (create/insert/all/select/findBy/
  delete). One codec → schema + read + write; scalars→columns, ADT/nested→JSON
  blob, Maybe→nullable. Verified e2e on SQLite AND Postgres (identical output;
  dialect-correct DDL). Runtime bridge: `runtime-go/rt/db_codec.go`.
- **S3 ✅** — compiler emits `sky:"name,type"` field tags on record structs
  (`crates/codegen/src/lib.rs`). Metadata-only; all gates green (roundtrip,
  divergences, build-run, coerce-floor re-blessed, sweep 29/0). Commit dd0a139a.
- **S4 ✅** — `Codec.auto` (reflection derive). `runtime-go/rt/codec_auto.go` +
  `Std.Codec.auto`. `Codec.auto blankUser` derives a codec for scalars/Maybe/
  nested-records/lists/nullary-enum-as-ordinal; data ADTs error (need explicit
  taggedUnion). Verified e2e: JSON round-trip + Store on SQLite AND Postgres
  (nested address blob, Maybe, lists all round-trip).
- **S5 ✅** — hardening. S5a: readable enum names in `Codec.auto` (codegen
  `rt.RegisterEnum` + runtime registry + tag-typed walkers; enums store names,
  incl. in Maybe/lists). S5b: `Codec.fromJsonSafe` (untrusted-decode size guard)
  + documented mass-assignment (input-record pattern), naming, and public-wire
  boundary rules. Commits a7416b9a, e279f64d. (Non-code items are enforced
  patterns per plan §5.)
- **S6 ✅** — auto-migration architecture (`docs/v0.19/auto-migration-architecture.md`,
  grilled; v1 manual `Db.migrate`). Commit b8d5b744.

- **S7 ✅ (file-based migration IMPLEMENTATION — beyond S6's v1-manual scope).**
  The user pivoted: since stores are `Db.table`-style pure values, generate +
  commit migration FILES (git-reviewed, no live DB for diff), apply them
  non-interactively. Shipped + verified on SQLite AND Postgres:
  - **Op renderer + apply engine** — `runtime-go/rt/db_migrate_ops.go`
    (`renderMigOp` dialect SQL for createTable/addColumn/dropColumn/renameColumn/
    addIndex/dropIndex/raw; identifier-validated) + `Std.Db.Migrate.migrateOps`
    (checksummed `_sky_migrations` ledger, at-most-once). Unit-tested both
    dialects incl. injection rejection.
  - **DB-free schema-dump** — `Store.project` / `Store.toTable` / `dumpSchema`
    (`Db_dumpProject` prints schema JSON between markers; pure via lazy CAFs) +
    nullability carried through (`?` kind suffix + `ColType.CNull`).
  - **`sky db migrate --gen [name]`** (`crates/sky/src/db_migrate.rs` +
    `cmd_db_gen`) — builds the dump entry, diffs vs `db/schema.json`, writes
    `db/migrations/<ts>_<name>.json` + snapshot. New required col → `addColumn
    NOT NULL DEFAULT <zero>` (safe backfill); Maybe → nullable; drop/retype →
    **quarantined** in a `destructive` array (never auto-applied).
  - **Interactive gen (TTY)** — dropped col → (r)ename [→ one `renameColumn`,
    data preserved] / (d)rop / (s)kip; required col → custom backfill default.
    Non-TTY keeps the safe quarantine defaults (CI-deterministic). Pure rewrite
    core unit-tested (6 db_migrate tests).
  - **`sky db migrate`** (`cmd_db_apply`) — concatenates committed files, applies
    via the ledger, dialect-correct, idempotent (2nd run = 0). Verified: SQLite
    INTEGER/TEXT vs Postgres bigint/text from the SAME files; quarantined drop is
    a no-op (column preserved). Commits 2a7cb6ab, 0fcd6787, ef1bb18b, a515dc3e.
  - **Operational verbs (Phase 4c, commit 9af5b2ec):** `sky db init` (scaffold),
    `sky db status` (committed files vs the live `_sky_migrations` ledger; ✓/○ per
    file; exits non-zero while pending — deploy gate), `sky db seed` (runs the
    entry module's exposed `seed : Db -> Task Error ()`). Marker-based temp entry
    for status (queries the ledger `name` column; tolerates fresh DB); shared
    `build_temp_db_entry` helper. Verified e2e SQLite: init → gen → status(pending,
    exit 1) → migrate → status(up to date, exit 0) → seed.
  - Docs: `docs/tooling/cli.md` file-based migration section (all verbs).
  - **Remaining (optional):** `sky db push` (rebrand `Store.migrate` live-additive),
    `sky run --db-migrate --db-seed` one-shot flags, binary-embedded migrations for
    deploy (so a built app can self-migrate without the source tree).

**ALL STEPS DONE.** The codec-derivation stack is complete: one `Codec` (or
`Codec.auto blank`) → JSON + dialect-safe DB, readable enums, verified SQLite +
Postgres. Migration automation designed (v1 manual). Remaining future work is
the auto-migration IMPLEMENTATION (S6 designed it) + optional `Codec.pick`/`omit`
+ the multi-arg-function-value codegen bug (plan §"Known compiler issue").

Vision realized: `Store.fromCodec "users" (Codec.auto blankUser) |> Store.primaryKey "id"`
— one line each for the codec + the table; JSON + DB from the type.

## Rules

- Grill each step against plan §4 before implementing.
- Verify each step (build + targeted test) before the next; full sweep at
  milestones only.
- Checkpoint (local commit) per step; push at milestones.
- No step ships with a known correctness hole from §5 unless explicitly
  escape-hatched + documented.
- Not compile-safe / not derivable → clear runtime error, never silent garbage.
