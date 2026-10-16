# Build speed at the final commit: v0.27.8 against v0.27.9

The build-speed benchmark taken again on the last commit of the v0.27.9 work,
after the runtime fixes (404 for an unknown path, the brotli boot loader,
system tools by absolute path) and the falsifier-proof commits had landed. It
replaces `../build-speed-final-20261015/` (compiler `94c57af01`) as the source
of the v0.27.9 release numbers.

- **before**: v0.27.8, commit `03d61922f`. v0.27.8 was **not** rebuilt or run
  again for this directory. The before columns are read from the committed
  `../build-speed-final-20261015/before.json`, which was measured on the same
  machine on the same day (09:03 to 10:27 BST).
- **after**: branch `perf/build-speed` at `9a665f22e` (`after.json`). It reports
  itself as `sky v0.27.9 (9a665f22e)`.
- **machine**: Apple silicon Mac, 8 CPUs, 16 GB, macOS, go1.26.1. The run
  started at 20:57 BST and ended at 21:18 BST. It held the heavy-build lock, so
  no other build of this work ran in parallel. Other processes on the machine
  were not stopped.

## The benchmark (`after.json`, `bench-head.log`)

`scripts/bench-build.sh --reps 3 --sizes "50 100 200" --cases "app skyshop"
--targets "web:app web"`. The arguments are the same as in
`../build-speed-final-20261015/`. The compiler is `sky-out/sky` of this tree,
built by `scripts/build.sh` immediately before the run. Scenarios, units and
`first` are as described there: wall and CPU in seconds (CPU is user + sys of
`sky` and all its children), and peak RSS of the largest single process in MB.
Each cell is the median of 3, except `first`, which is rep 1 of `prime`.

### `--target web:app`

| N | scenario | wall before | wall after | CPU before | CPU after | peak RSS before | peak RSS after |
|---|---|---|---|---|---|---|---|
| 50 | first | 35.63 | 6.88 | 57.03 | 10.45 | 610 | 589 |
| 50 | warm | 3.13 | 1.39 | 3.93 | 3.07 | 145 | 127 |
| 50 | newdir | 33.74 | 3.93 | 56.58 | 6.42 | 664 | 588 |
| 50 | edit | 28.95 | 14.85 | 38.38 | 32.26 | 604 | 585 |
| 50 | cold | 53.48 | 33.68 | 161.12 | 141.56 | 756 | 775 |
| 100 | first | 69.56 | 4.47 | 157.60 | 10.72 | 1027 | 611 |
| 100 | warm | 17.20 | 1.35 | 14.07 | 3.11 | 275 | 139 |
| 100 | newdir | 50.59 | 3.11 | 79.11 | 5.81 | 985 | 586 |
| 100 | edit | 46.35 | 12.95 | 60.84 | 32.91 | 979 | 587 |
| 100 | cold | 70.38 | 28.71 | 178.73 | 138.62 | 987 | 767 |
| 200 | first | 139.35 | 13.10 | 189.07 | 44.20 | 2781 | 621 |
| 200 | warm | 64.94 | 1.64 | 64.87 | 3.24 | 773 | 170 |
| 200 | newdir | 129.70 | 3.64 | 183.23 | 6.75 | 2976 | 594 |
| 200 | edit | 123.45 | 12.88 | 163.85 | 44.38 | 2989 | 614 |
| 200 | cold | 153.52 | 31.80 | 277.86 | 136.53 | 3015 | 805 |

### `--target web` (Sky.Live)

| N | scenario | wall before | wall after | CPU before | CPU after | peak RSS before | peak RSS after |
|---|---|---|---|---|---|---|---|
| 50 | first | 7.32 | 2.91 | 20.32 | 3.86 | 655 | 611 |
| 50 | warm | 0.62 | 0.51 | 1.14 | 1.06 | 80 | 71 |
| 50 | newdir | 6.31 | 1.44 | 20.39 | 2.46 | 666 | 601 |
| 50 | edit | 2.50 | 1.87 | 6.17 | 3.82 | 594 | 582 |
| 50 | cold | 20.80 | 20.42 | 101.72 | 100.51 | 781 | 746 |
| 100 | first | 7.30 | 1.91 | 20.77 | 3.88 | 666 | 581 |
| 100 | warm | 0.72 | 0.67 | 1.17 | 1.09 | 107 | 80 |
| 100 | newdir | 6.54 | 1.50 | 20.77 | 2.28 | 698 | 590 |
| 100 | edit | 2.66 | 2.02 | 6.51 | 4.04 | 598 | 581 |
| 100 | cold | 21.31 | 19.68 | 102.98 | 95.42 | 789 | 779 |
| 200 | first | 7.79 | 3.14 | 22.06 | 4.87 | 691 | 587 |
| 200 | warm | 1.07 | 0.70 | 1.54 | 1.00 | 186 | 105 |
| 200 | newdir | 6.98 | 1.65 | 22.06 | 2.63 | 694 | 591 |
| 200 | edit | 3.20 | 2.64 | 7.77 | 5.26 | 603 | 588 |
| 200 | cold | 20.22 | 30.78 | 102.89 | 107.09 | 764 | 746 |

### `examples/13-skyshop` (Go FFI, 76k symbols; medians of 3)

| scenario | wall before | wall after | CPU before | CPU after | peak RSS before | peak RSS after |
|---|---|---|---|---|---|---|
| prime | 31.93 | 7.87 | 86.38 | 6.11 | 4041 | 1268 |
| warm | 4.14 | 3.21 | 3.29 | 3.32 | 313 | 311 |
| newdir | 26.72 | 4.72 | 80.75 | 5.84 | 3973 | 1270 |
| edit | 10.30 | 6.87 | 18.74 | 13.85 | 1297 | 1266 |
| cold | 52.51 | 49.88 | 227.54 | 214.51 | 3972 | 4081 |

## The genwhole shape (`genwhole200-head.log`, `genwhole200-head.time`)

The same recipe as `../build-speed-final-20261015/`:
`genwhole.py examples/62-app-notes <dir> 200` (the script is committed there),
the header text changed to a per-run token so the app packages are new to the
Go cache, then one `sky build --target web:app src/Main.sky` with
`SKY_TIMINGS=1`. One run. Scratch paths in the log are replaced by `<scratch>`.

| genwhole N = 200, first build | v0.27.8 | v0.27.9 (`9a665f22e`) |
|---|---|---|
| wall | 1575.7 s | 29.7 s |
| CPU (user + sys) | 2171.7 s | 117.5 s |
| peak RSS (largest process) | 4,775 MB | 1,100 MB |

The v0.27.8 column is the committed `../build-speed-final-20261015/genwhole200-before.time`.
v0.27.8 was not run again. After, the two legs run together (28.2 s for both):
the backend leg takes 16.4 s, the frontend leg 8.2 s, and the in-process brotli
of the new wasm 19.8 s beside the backend leg.

## Reading it

- Against `94c57af01`, most medians move by small amounts in both directions.
  N = 200 web:app, after: `94c57af01` then `9a665f22e`: first 13.98 / 13.10 s,
  warm 1.63 / 1.64 s, newdir 3.85 / 3.64 s, edit 12.82 / 12.88 s, cold
  27.20 / 31.80 s.
- The `cold` wall times vary more than the other cells, because a cold build
  compiles the runtime and the standard library on all CPUs and other processes
  on the machine take CPU time from it. Sky.Live N = 200 cold took 36.3, 30.8
  and 20.6 s in its three reps, with CPU 110.0, 107.1 and 101.8 s. The CPU
  medians (`cold` 136.5 s after against 140.3 s at `94c57af01` for web:app
  N = 200) show no increase in work.
- `first` at N = 50 web:app is low here (6.9 s, against 26.8 s at
  `94c57af01`). That run had to compile the branch's runtime into the Go cache.
  This run found the runtime already compiled by the gate runs earlier the same
  day.
