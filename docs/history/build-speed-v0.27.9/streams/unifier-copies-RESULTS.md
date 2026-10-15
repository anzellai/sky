# Stream U results: unifier copies, occurs buffers, update closes, shared world

Branch perf/shared-types, worktree sky-buildperf-types. Base = 068c8a436
(binary streamC/sky-final, copied to streamU/sky-base). New = 1a5b6850c.

Commits:
- 122eaa79e ty: bind a variable without copying the structure it binds to (item 1)
- f6faec33b ty: reuse the occurs check's visited marks and stack (item 2)
- 7273453d9 ty: skip the #166 / D2 update close when the base already is the record (item 3, + doc 06)
- 1a5b6850c ty, skydb: share the memoised world instead of copying it per call (item 4)

## Measurements (same machine, base then new, back to back)

`sky build` with SKY_TIMINGS=1 and `/usr/bin/time -l`; `go build` skipped by
streamC/fakego. Fixtures are streamC's (spaN = backend of the generated
Sky.Spa app, split once with the old compiler; recN = one N-field record
codec). Typecheck and lowering are separate phases in the SKY_TIMINGS table.

| run | typecheck base -> new | lower base -> new | peak RSS base -> new |
|---|---|---|---|
| spa100 | 0.78 -> 0.50 s | 0.88 -> 0.67 s | 339 -> 298 MB |
| spa200 | 2.43 -> 1.73 s | 3.12 -> 2.27 s | 1012 -> 833 MB |
| spa400 | 9.38 -> 6.65 s | 12.05 -> 8.66 s | 2835 -> 2927 MB |
| rec400 | 0.15 -> 0.08 s | 0.12 -> 0.09 s | 80 -> 73 MB |
| rec800 | 0.46 -> 0.21 s | 0.29 -> 0.15 s | 108 -> 101 MB |
| rec1200 | 0.93 -> 0.41 s | 0.53 -> 0.26 s | 151 -> 144 MB |

Typecheck phase memory at spa400: 1423 -> 966 MB. Peak RSS at spa400 moves
with the lowering phase (2456 -> 2926 MB in the phase column); one run each, so
treat the peak as noisy.

`sky spa-split` of the whole app (this is where `encodable::check` calls
`type_world` once per Model field, item 4):

| app | wall base -> new | peak RSS base -> new |
|---|---|---|
| spa100 | 0.56 -> 0.29 s | 108 -> 101 MB |
| spa200 | 1.32 -> 0.67 s | 234 -> 229 MB |
| spa400 | 4.30 -> 2.09 s | 606 -> 592 MB |

Counters at spa400 (typecheck | lower), base -> new:
- record_fields 53.4 M | 59.0 M -> 49.5 M | 45.3 M (fewer record maps copied)
- occurs 66.9 M | 55.4 M -> unchanged (same walk, now allocation free)
- readback, owned_rebuild, dictkey, intern, goty: unchanged.
At rec1200: record_fields 4.39 M | 2.21 M -> 2.94 M | 1.48 M.

Profile of the new spa400 backend build (`sample`, 1 ms, 12.5 k samples):
check_modules 2.9 k, lower 4.8 k (infer_query 2.6 k), check world build
1.4 k. Top self time: memcmp + SmolStr::cmp (about 3.0 k, BTreeMap name
compares on record field maps), malloc/free (about 1.5 k), occurs 0.5 k,
Ty clone/drop 0.66 k, ty_to_var 0.2 k self (0.54 k inclusive). The update
close skip fires (skip_ground_close 112 inclusive, of which the walk 46).

Still super-linear: spa100 -> 200 -> 400 typecheck 0.50 -> 1.73 -> 6.65 s,
lowering 0.67 -> 2.27 -> 8.66 s.

## Ratchet (skydb/tests/typed_table_growth.rs)

`unifier_work_is_linear_in_record_size` still fails, so it stays ignored and
no bound was changed:
- codec_chain occurs 3.92 / 3.92, record_fields 3.85 / 3.91 (typecheck / lower)
- model_app typecheck occurs 2.00, record_fields 2.00 (already linear at base)
- model_app lower occurs 3.73, record_fields 3.62 (base: record_fields 3.95;
  the absolute value at 400 fell from 1.58 M to 0.21 M)
No cost became linear that was not linear before, so no new ratchet was
added. `type_world_and_infer_are_memoised_within_a_revision` now pins the
world sharing.

## Equivalence proofs

- Emitted Go: every example built with base and new (fakego): 59 + 5 + 2
  exits identical, same file set of 34,328 `.go` files under sky-out, 0
  byte differences. Build logs differ only in the output path.
- Generated apps: spa100/200/400 backend main.go and rec400/800/1200 main.go
  identical; `sky spa-split` output of spa100/200/400 identical (10 files each).
- `sky check --format json` over the 89 reject-corpus files: every diagnostic
  line identical (only durationMs and paths differ).
- LSP snapshot (streamC's harness: diagnostics, hover at every identifier
  start, inlay hints over the reject corpus + 5 files): 5,538 lines identical
  to streamC's base snapshot.
- Differential oracle `infer_readback_tests` (stdlib, every example, accept +
  reject corpora, generated programs incl. a new `record_closes` program, in
  lower / tooling / check modes, skip on vs off): pass.
  `world_is_the_same_with_and_without_fast_paths`: pass (skip covered by the
  same seam). `ground_close_is_skipped_and_changes_nothing`: pass.
- Gates: xtask infer, roundtrip, reject, repro, resolve PASS. coerce-floor:
  the same table as base (the gate log matches streamC's final log line for
  line); it FAILS on base and new alike with +3 narrow in 63-app-chat and
  60-spa-todos client/server (from perf/wire-dedupe, see streamC). Golden not
  touched. cargo test -p ty -p lower -p skydb -p sky-lsp: 537 passed, 0
  failed. project spa unit tests: 52 passed.

## Red / green

| test | before fix | after fix |
|---|---|---|
| unify::tests::binding_a_variable_moves_the_structure | FAILED (500 fields copied) | ok |
| unify::tests::occurs_marks_are_per_call | n/a (new mechanism, no old equivalent) | ok |
| readback_tests::ground_close_is_skipped_and_changes_nothing | FAILED (0 closes skipped) | ok |
| skydb incremental type_world_and_infer_are_memoised_within_a_revision | FAILED (type_world must share the memoised world) | ok |

Red runs: streamU/u1-red.log, u3-red.log, u4-red.log (the fix reverted in
place, then restored).

## Counter-based tests

No test asserts an exact counter value. The growth ratchet compares ratios;
record_fields counts fewer copies now, which is the intended effect.
