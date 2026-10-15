# Build speed: v0.27.8 against the perf/build-speed branch

The `xtask bench-gen` app (Model fields = update arms = N, 8 modules, seed 1)
built with `sky build --target web:app`, by `scripts/bench-build.sh --cases app
--sizes "50 100 200" --targets web:app --reps 1`.

- **before**: v0.27.8, commit `03d61922f` (`before.json`).
- **after**: branch `perf/build-speed`, compiler at commit `44770d5cd`
  (`after.json`; the `-dirty` in its version is the uncommitted benchmark
  script, not compiler source).
- **machine**: Apple silicon Mac, 8 CPUs, 16 GB, macOS, go1.26.1. One run of
  each. The two compilers ran back to back on the same machine state.

Scenarios: `first` is a fresh directory with the normal Sky Go cache; `warm`
the same directory again; `newdir` the same sources in a new directory;
`edit` one string literal changed in one view module (in the new directory);
`cold` a fresh directory with an empty temporary GOCACHE. Wall and CPU in
seconds (CPU is user + sys of `sky` and all its children), peak RSS of the
largest single process in MB.

| N | scenario | wall before | wall after | CPU before | CPU after | peak RSS before | peak RSS after |
|---|---|---|---|---|---|---|---|
| 50 | first | 40.12 | 39.78 | 80.21 | 164.37 | 692 | 894 |
| 50 | warm | 3.13 | 1.68 | 3.85 | 3.56 | 144 | 127 |
| 50 | newdir | 33.84 | 3.40 | 56.58 | 6.26 | 646 | 612 |
| 50 | edit | 29.56 | 23.47 | 38.32 | 39.15 | 602 | 595 |
| 50 | cold | 52.87 | 30.16 | 161.81 | 137.17 | 782 | 837 |
| 100 | first | 51.64 | 27.87 | 79.27 | 46.56 | 997 | 619 |
| 100 | warm | 10.96 | 2.47 | 11.56 | 4.62 | 278 | 183 |
| 100 | newdir | 49.98 | 4.20 | 79.00 | 7.38 | 991 | 601 |
| 100 | edit | 45.32 | 27.28 | 60.88 | 46.17 | 984 | 619 |
| 100 | cold | 70.22 | 31.49 | 181.33 | 140.46 | 955 | 846 |
| 200 | first | 131.48 | 36.66 | 183.85 | 63.57 | 2705 | 860 |
| 200 | warm | 65.22 | 4.55 | 65.24 | 7.54 | 880 | 437 |
| 200 | newdir | 157.08 | 5.51 | 278.65 | 9.04 | 2915 | 612 |
| 200 | edit | 125.59 | 37.46 | 163.91 | 63.65 | 2710 | 875 |
| 200 | cold | 168.18 | 33.44 | 273.82 | 155.89 | 2764 | 902 |

Reading it:

- The after `first` at N=50 is the first build of the branch's runtime in the
  shared Sky Go cache (the before run had just filled it with v0.27.8's
  runtime), so it compiled the runtime: 164 s of CPU. The N=100 and N=200
  `first` rows are the steady state.
- Before, the front end grew faster than the app: at N=200 each Sky.Spa leg
  spent about 14 s in typecheck and 16 s in lowering (`timings` in
  `before.json`); after, 0.6 to 1.6 s.
- After, `first` and `edit` are dominated by precompression (`brotli -11`)
  of the changed wasm bundle: 20.5 to 30.3 s of the 23.5 to 37.5 s wall at
  N=50 to 200 (`precompress dist` in the `timings` of `after.json`).
- `newdir` after is 3.4 to 5.5 s at every size, against 34 to 157 s before.
  This run does not count compiles; the counted proof that a copy compiles
  no Go package is `go_build_cache_flow::a_copy_in_a_new_directory_compiles_nothing`.

`docs/perf/build-budget.json` holds the budget the `build-perf` CI job
compares against; see its `note`.

## The budget run (`ci-shape.json`)

`scripts/bench-build.sh --reps 3 --cases "app skyshop" --write-budget
docs/perf/build-budget.json` on the same Mac, branch compiler: the shape the
`build-perf` CI job runs (generated app at N=50 and N=100 for `web:app` and
`web`, and `examples/13-skyshop`). Its medians (the `prime` scenario
excluded) are the `darwin-arm64` entry of `docs/perf/build-budget.json`.
Each rep's `edit` carries a per-run token, so it is a real edit-rebuild every
time (web:app: 23.8 to 27.5 s, most of it brotli of the new wasm).
