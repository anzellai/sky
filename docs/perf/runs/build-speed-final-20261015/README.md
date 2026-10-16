# Build speed, final: v0.27.8 against v0.27.9

The milestone measurement of the build-speed work, taken after every change in
the v0.27.9 notes had landed. It supersedes the part-way run in
`../build-speed-20261015/` (compiler `44770d5cd`) for the release numbers.

- **before**: v0.27.8, commit `03d61922f` (`before.json`), built with
  `scripts/build.sh` in a separate worktree and target directory.
- **after**: branch `perf/build-speed` at `94c57af01` (`after.json`), which
  reports itself as `sky v0.27.9 (94c57af01)`.
- **machine**: Apple silicon Mac, 8 CPUs, 16 GB, macOS, go1.26.1. The two
  compilers ran back to back on the same machine state (before, then after),
  2026-10-15, 09:03 to 10:27 BST, with no other heavy job running.

## The benchmark (`before.json`, `after.json`)

`scripts/bench-build.sh --reps 3 --sizes "50 100 200" --cases "app skyshop"
--targets "web:app web"`, run from the branch tree, with `--sky` / `--tree`
pointing at the v0.27.8 worktree for `before`. The same `xtask bench-gen` app
(Model fields = update arms = N, 8 modules, seed 1) is built by both.

Scenarios: `prime` is a build in a fresh directory with the normal Sky Go
cache; `warm` the same directory again; `newdir` the same sources in a new,
deeper directory; `edit` one string literal changed in one view module (a
per-run token, so the edited program is new to the cache); `cold` a fresh
directory with an empty temporary GOCACHE. Wall and CPU in seconds (CPU is
user + sys of `sky` and all its children), peak RSS of the largest single
process in MB. Each cell is the median of 3, except `first`, which is rep 1 of
`prime` (see "Reading it").

### `--target web:app`

| N | scenario | wall before | wall after | CPU before | CPU after | peak RSS before | peak RSS after |
|---|---|---|---|---|---|---|---|
| 50 | first | 35.63 | 26.81 | 57.03 | 153.22 | 610 | 793 |
| 50 | warm | 3.13 | 1.19 | 3.93 | 2.96 | 145 | 127 |
| 50 | newdir | 33.74 | 2.87 | 56.58 | 5.73 | 664 | 581 |
| 50 | edit | 28.95 | 11.02 | 38.38 | 29.11 | 604 | 600 |
| 50 | cold | 53.48 | 25.90 | 161.12 | 133.76 | 756 | 782 |
| 100 | first | 69.56 | 11.64 | 157.60 | 31.42 | 1027 | 616 |
| 100 | warm | 17.20 | 1.29 | 14.07 | 3.08 | 275 | 138 |
| 100 | newdir | 50.59 | 3.03 | 79.11 | 5.69 | 985 | 590 |
| 100 | edit | 46.35 | 11.63 | 60.84 | 31.63 | 979 | 603 |
| 100 | cold | 70.38 | 26.82 | 178.73 | 134.71 | 987 | 784 |
| 200 | first | 139.35 | 13.98 | 189.07 | 44.09 | 2781 | 623 |
| 200 | warm | 64.94 | 1.63 | 64.87 | 3.18 | 773 | 171 |
| 200 | newdir | 129.70 | 3.85 | 183.23 | 6.93 | 2976 | 594 |
| 200 | edit | 123.45 | 12.82 | 163.85 | 44.25 | 2989 | 598 |
| 200 | cold | 153.52 | 27.20 | 277.86 | 140.25 | 3015 | 812 |

### `--target web` (Sky.Live)

| N | scenario | wall before | wall after | CPU before | CPU after | peak RSS before | peak RSS after |
|---|---|---|---|---|---|---|---|
| 50 | first | 7.32 | 2.67 | 20.32 | 3.67 | 655 | 610 |
| 50 | warm | 0.62 | 0.46 | 1.14 | 0.87 | 80 | 72 |
| 50 | newdir | 6.31 | 1.34 | 20.39 | 2.20 | 666 | 581 |
| 50 | edit | 2.50 | 1.75 | 6.17 | 3.60 | 594 | 580 |
| 50 | cold | 20.80 | 17.96 | 101.72 | 93.65 | 781 | 788 |
| 100 | first | 7.30 | 2.94 | 20.77 | 4.16 | 666 | 585 |
| 100 | warm | 0.72 | 0.55 | 1.17 | 1.11 | 107 | 81 |
| 100 | newdir | 6.54 | 1.43 | 20.77 | 2.35 | 698 | 586 |
| 100 | edit | 2.66 | 2.05 | 6.51 | 4.27 | 598 | 586 |
| 100 | cold | 21.31 | 18.61 | 102.98 | 98.14 | 789 | 762 |
| 200 | first | 7.79 | 2.53 | 22.06 | 4.89 | 691 | 592 |
| 200 | warm | 1.07 | 0.64 | 1.54 | 1.00 | 186 | 106 |
| 200 | newdir | 6.98 | 1.50 | 22.06 | 2.27 | 694 | 585 |
| 200 | edit | 3.20 | 2.22 | 7.77 | 4.96 | 603 | 591 |
| 200 | cold | 20.22 | 19.01 | 102.89 | 94.93 | 764 | 813 |

### `examples/13-skyshop` (Go FFI, 76k symbols; medians of 3)

| scenario | wall before | wall after | CPU before | CPU after | peak RSS before | peak RSS after |
|---|---|---|---|---|---|---|
| prime | 31.93 | 7.67 | 86.38 | 6.14 | 4041 | 1269 |
| warm | 4.14 | 3.20 | 3.29 | 3.33 | 313 | 310 |
| newdir | 26.72 | 5.34 | 80.75 | 5.85 | 3973 | 1272 |
| edit | 10.30 | 6.58 | 18.74 | 13.24 | 1297 | 1258 |
| cold | 52.51 | 55.99 | 227.54 | 202.85 | 3972 | 4015 |

## The genwhole shape (`genwhole200-*.log`, `genwhole200-*.time`)

`genwhole.py examples/62-app-notes <dir> 200` adds, per k in 1..200, two Model
fields, three Msg arms (a pure edit, a server read, a server write that reads
the whole Model) and an input row in the view. This is the real web:app shape
where the type-table, CAF and wire-record changes matter most. Each run
generates a fresh directory and changes the header text to a per-run token, so
the app packages are new to the Go cache; the runtime and stdlib packages were
already cached by the benchmark above. One `sky build --target web:app
src/Main.sky` per compiler with `SKY_TIMINGS=1` (the logs hold the per-phase
tables; scratch paths are replaced by `<scratch>`). One run each: the planned
three were stopped after the first pair, because one v0.27.8 run takes 26
minutes.

| genwhole N = 200, first build | v0.27.8 | v0.27.9 |
|---|---|---|
| wall | 1575.7 s | 29.3 s |
| CPU (user + sys) | 2171.7 s | 119.8 s |
| peak RSS (largest process) | 4,775 MB | 1,112 MB |

Before, each leg type-checked for about 238 s and lowered for about 263 s, and
the two legs ran one after the other (frontend 787 s, backend 781 s). After,
the legs run together (27.7 s for both); the largest step is in-process brotli
of the new wasm (20.4 s, beside the backend leg).

## The front end once (`front-end-once.md`)

The stream's own measurement of "run the front end once per program in a
split build" (`b5be6ed09`), copied as it was written: median of 3 warm runs,
against the branch before that change (`c4d17f467`), with the byte-identity
and diagnostics comparisons it ran.

## Reading it

- `first` is rep 1 of `prime`. Reps 2 and 3 of `prime` build the same seed-1
  sources again; under v0.27.9's `-trimpath` the Go cache then hits as it does
  for `newdir`, so their median is not a first build. v0.27.8 has no
  `-trimpath`, so its reps 2 and 3 compile again (medians 34.60, 62.87, 139.35
  s at N = 50, 100, 200 for web:app).
- The after `first` at N = 50 web:app is the first build of the branch's
  runtime in the shared Sky Go cache (the before run had just filled it with
  v0.27.8's runtime), so it compiled the runtime: 153 s of CPU. The N = 100
  and N = 200 rows are the steady state.
- `cold` empties GOCACHE, so it compiles the runtime and the standard library
  every time. The Sky.Live (`web`) cold build and the skyshop cold build are
  dominated by that and barely move (skyshop 52.5 s before, 56.0 s after: the
  76k-symbol FFI package compile is the same work in both).
- skyshop `prime` rep 1 after (47.8 s) compiled the FFI package under the new
  `-trimpath` key; the median (7.7 s) is the steady state. Before, every new
  directory compiles it again (`newdir` 26.7 s).
- After, a web:app `edit` is mostly in-process brotli of the changed wasm:
  at N = 200 rep 1, `precompress brotli (max)` is 10.0 s of the 12.8 s wall,
  beside a 6.1 s backend leg (`timings` in `after.json`).
