# Stream G: run the front end once per Sky.Spa build

Machine: 16 GB Mac. Release builds. Median of 3 warm runs. Parent = the `sky build`
process (its own SKY_TIMINGS table). base = perf/build-speed HEAD c4d17f467.

## Whole build, N=200

| app | path | front half (parent) | split step | build wall | parent peak RSS | front-end note |
|---|---|---|---|---|---|---|
| genwhole 200, direct Spa entry | base | 0.65 s | 0.62 s | 4.70 s | 328 MB | 2 loads, 2 checks |
| genwhole 200, direct Spa entry | new | 0.66 s | 0.15 s | 4.20 s | 330 MB | 1 load, 1 check |
| bench-gen 200, direct Spa entry | base | 0.28 s | 0.19 s | 1.54 s | 155 MB | 2 loads, 2 checks |
| bench-gen 200, direct Spa entry | new | 0.28 s | 0.05 s | 1.45 s | 153 MB | 1 load, 1 check |
| genwhole 200, --target web:app | base | 0.67 s | 0.62 s | 4.78 s | 328 MB | 2 loads, 2 checks |
| genwhole 200, --target web:app | new | 0.66 s | 0.62 s | 4.79 s | 329 MB | 2 loads, 2 checks |
| bench-gen 200, --target web:app | base | 0.28 s | 0.20 s | 1.62 s | 151 MB | 2 loads, 2 checks |
| bench-gen 200, --target web:app | new | 0.28 s | 0.20 s | 1.63 s | 153 MB | 2 loads, 2 checks |

"Direct Spa entry" = the staged `.skyapp/web-app` project (the synthesised
`Spa.app` entry) built as its own project. Base counts are by reading the code
(the base binary has no counter). Neither generated app keeps a continuation on
the server, so the shared second check does not show in these rows; the
fixture test shows it (2 checks -> 1).

An earlier cut held the front half's `CheckOutput.def_types` alive through the
split. The web:app parent peak then rose to 385-396 MB in most runs (a control
binary with no behaviour change measured 327-331 MB). The per-def types are now
dropped before the split (no analysis reads them); peak is back to 329 MB.

## Byte identity

- `sky spa-split` output, base vs new: identical for genwhole 100, genwhole
  200, bench-gen 200, spa-split-multimodule fixture, and both direct-entry
  projects (10, 10, 20, 10, 10, 20 files).
- `sky build` (reused db) split vs base `sky spa-split`: identical for
  direct genwhole 200, direct bench-gen 200, spa-split-multimodule.

## Diagnostics

111 runs, base vs new, stdout + stderr + exit, `durationMs` masked: 0 differ.
- reject corpus (89 files): `sky check --format json`.
- spa-split-multimodule variants (clean, client-arm, server-arm, shared module,
  server module, two errors, name error): `sky check --format json` and
  `sky build --format json`.
- 62-app-notes variants (clean, client-arm, server-arm, shared module, server
  module): `sky check/build --target web:app --format json`.

## Tests

- project `spa_split_checks_once` (new): red (1 load, 2 checks) -> green (1, 1).
- sky `spa_split_flow::a_direct_spa_build_runs_the_front_end_once` (new): red
  (`sky check` 3 loads, 3 checks) -> green (check 2/2, build 1/1, split
  byte-identical to a fresh `sky spa-split`).
- project lib `spa` (59), `timings` (2), `spa_server_chain` (10): pass.
- spa_split_flow diagnostic and refusal tests (16 + the new one): pass.
