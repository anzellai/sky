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

## Rules

- Grill each step against plan §4 before implementing.
- Verify each step (build + targeted test) before the next; full sweep at
  milestones only.
- Checkpoint (local commit) per step; push at milestones.
- No step ships with a known correctness hole from §5 unless explicitly
  escape-hatched + documented.
- Not compile-safe / not derivable → clear runtime error, never silent garbage.
