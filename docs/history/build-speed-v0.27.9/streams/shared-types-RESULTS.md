# Stream C results: shared type table (branch perf/shared-types)

All numbers: `sky build` with `SKY_TIMINGS=1` and `/usr/bin/time -l`; `go build`
is skipped by a measurement-only `go` shim (`streamC/fakego/go`), so the leg
measured is type check + lowering only. "spaN" = the backend leg of the
generated Sky.Spa app (`genwhole.py distinct`, examples/62-app-notes, Model of
2N+5 fields, N whole-model server arms), split once with the base compiler.
"recN" = one N-field record and its `Codec.object … |> Codec.field …` chain
(`genrec.py`). Peak RSS = `time -l` maximum resident set.

Baselines: `before` = a571bf139 (this branch's start); `base` = f14136c3b
(perf/wire-dedupe tip, the merge base of the final branch; its stdlib differs,
so the final byte diff is against `base`). Phase rows P0-P3 were built in the
shared target dir before the isolation fix below; P4 and final were built with
every input of this tree touched first.

| run | spa100 tc / lower | spa200 tc / lower | spa400 tc / lower | spa400 RSS | rec400 tc / lower | rec800 tc / lower | rec1200 tc / lower | rec1200 RSS |
|---|---|---|---|---|---|---|---|---|
| before | 1.52 / 2.96 s | 5.65 / 10.49 s | 23.33 / 40.50 s | 5,163 MB | 0.46 / 0.61 s | 1.69 / 1.89 s | 3.74 / 4.01 s | 3,740 MB |
| base | 1.53 / 3.04 s | 5.78 / 10.60 s | 23.99 / 41.29 s | 4,032 MB | 0.46 / 0.62 s | 1.73 / 1.92 s | 3.82 / 4.03 s | 3,741 MB |
| P0 counters | 1.52 / 3.02 s | 5.65 / 10.56 s | 23.40 / 40.46 s | 5,653 MB | 0.46 / 0.61 s | 1.73 / 1.92 s | 3.77 / 4.04 s | 3,743 MB |
| P1 table read-back | 1.69 / 2.85 s | 5.73 / 9.54 s | 23.58 / 36.23 s | 4,386 MB | 0.46 / 0.56 s | 1.68 / 1.71 s | 3.74 / 3.60 s | 3,739 MB |
| P2 goty per ref, no world clone | 1.51 / 1.58 s | 5.84 / 5.75 s | 23.88 / 22.68 s | 3,822 MB | 0.46 / 0.36 s | 1.70 / 1.23 s | 3.90 / 2.64 s | 3,681 MB |
| P3 access/update fast path | 1.33 / 1.38 s | 4.93 / 5.05 s | 20.43 / 19.82 s | 4,896 MB | 0.45 / 0.35 s | 1.66 / 1.19 s | 3.63 / 2.53 s | 3,741 MB |
| P4 sub-root sharing, checker table | 0.70 / 0.88 s | 2.44 / 3.09 s | 9.34 / 11.94 s | 3,252 MB | 0.15 / 0.12 s | 0.47 / 0.28 s | 0.95 / 0.54 s | 150 MB |
| final (068c8a436) | 0.69 / 0.89 s | 2.44 / 3.12 s | 9.35 / 12.80 s | 3,175 MB | 0.15 / 0.12 s | 0.46 / 0.28 s | 0.97 / 0.55 s | 151 MB |

Counters at spa400 (typecheck | lower), P0 -> final:
- readback 102.9 M | 134.1 M -> 12.2 M | 20.9 M
- dictkey 119.2 M | 0 -> 4.0 M | 0
- owned_rebuild 0 | 101.2 M -> 810 | 8.8 M
- intern 0 | 284.2 M -> 6.0 M | 39.3 M
- occurs 84.4 M | 72.9 M -> 66.9 M | 55.4 M
- record_fields 209.2 M | 223.5 M -> 53.4 M | 59.0 M
At rec1200: readback 24.6 M | 24.7 M -> 70 K | 120 K; occurs 14.6 M | 7.4 M (unchanged).

Still super-linear: spa100 -> 200 -> 400 type check 0.69 -> 2.44 -> 9.35 s.
A `sample` profile of the final spa400 leg puts most of it in
`UnionFind::unify` (8.7 k of ~15.7 k samples), with the occurs check 4.0 k,
instantiation (`instantiate` + `ty_to_var`) 1.8 k, the call-site record harvest
2.0 k and the check world 1.9 k. Two causes, both in the unifier, not in
read-back: (1) binding an instantiated variable to a large structure walks the
whole structure for the occurs check; (2) every call that passes the Model to an
annotated function instantiates the declared Model record and unifies it field
by field, and the lowering path's #166 / D2 close does the same per update.
See "Not done" below.

## Ratchet (skydb/tests/typed_table_growth.rs, sizes 100/200/400)

- Before (P0, logs ratchet-before.log): 18 counter series grew with ratio 3.9-4.0
  per doubling (readback, occurs, record_fields, dictkey, owned_rebuild, intern
  on both shapes). goty was already linear.
- After: `type_table_work_is_linear_in_record_size` (readback, owned_rebuild,
  dictkey, intern, goty) passes; all ratios 2.0. The ignored
  `unifier_work_is_linear_in_record_size` (occurs, record_fields) still fails
  (codec_chain occurs 3.92, model_app lower record_fields 3.95).

## Equivalence checks

- Emitted Go, base vs final: 177 Go files from the 59 examples that build
  without `sky install` (incl. the Sky.Spa split examples) identical, same file
  set; the 7 that fail (5 need `sky install`, 2 have no `src/Main.sky`) fail
  with identical output. Generated app: spa100/200/400 backend main.go and
  rec400/800/1200 main.go identical; `sky spa-split` output of the spa200 app
  identical (8 .sky files).
- Diagnostics: `sky check --format json` over the 89 reject-corpus files, base
  vs new: all 96 diagnostic lines identical (code, message, range); only
  `durationMs` and the root path differ in the summary line.
- LSP: diagnostics, hover at every identifier start and inlay hints over the
  reject corpus plus 5 extra files (the rec400 codec, examples 12, 44, 53, 55),
  base vs new: 5,538 lines identical (5,111 hovers, 151 diagnostics, 132 inlays).
- World: `world_is_the_same_with_and_without_fast_paths` (World `==`, all
  channels incl. value_sigs) over the stdlib, every example, generated programs.
- Differential oracle (`infer_readback_tests`): every def of the stdlib, every
  example, the accept + reject corpora and 4 generated programs, in the lower,
  tooling and check modes: table read-back == owned read-back (memo on
  top-level roots and on all sub-roots), fast path == general path (errors,
  variables allocated, every recorded type, obligations), checker table ==
  owned list (order, duplicates, values, Dict-key lists, last-wins lookups).
  Falsified: memoising tainted roots fails `cyclic_subroot_is_not_shared`;
  skipping the dead row's allocation fails the corpus differential.
- coerce-floor: identical table to base on all 71 measured rows. Both base and
  final FAIL the gate with +3 narrow in 63-app-chat and 60-spa-todos
  client/server: this widening comes from perf/wire-dedupe (the new
  Std.Codec repeated-key decoder), not from this stream.

## Isolation fix found on the way

The shared CARGO_TARGET_DIR is used by two worktrees whose workspace crates get
the same unit hashes. Cargo then treats a unit built from the other worktree as
fresh when this tree's files are older, and links the other tree's code. Seen
directly: after a test build in a perf/wire-dedupe worktree, this tree's
`project` crate compiled against that tree's `ty` (no `ty::counters`). Every
build and test here now touches all inputs of its tree first (`claim.sh`).
Builds by other streams in the same dir have the same exposure.
