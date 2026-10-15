# `examples/13-skyshop`: empty Go cache, after the `-p` fix

A cold build (empty Go cache) of `examples/13-skyshop`, the Stripe-SDK-scale
FFI example, compared with v0.27.8 after commit `82c74a396` ("go build -p: plan
the generated FFI package with only the console beside it").

- **Machine**: the 16 GB Apple silicon Mac (8 CPUs) used for the rest of the
  build-speed work, go1.26.1, 2026-10-15. With other work on the machine, the
  available memory read 5.8 to 7 GB.
- **Compilers**: v0.27.8 (`03d61922f`) and the `perf/stack-release` branch at
  `82c74a396`, merged into `perf/build-speed`.

## Source of these numbers

The raw run files are gone: they were not kept in the session's scratch
directory. The numbers below are the agent's recorded output, as written into
the commit message of `82c74a396` at the time of the runs (`git show
82c74a396`). Nothing here was measured again.

## Before the fix

A first cold build measured 56.0 s on the branch against 52.5 s on v0.27.8.
Three more runs each, alternating, gave medians of 50.5 s (branch) and 52.0 s
(v0.27.8). The branch's `go build` took 51.5 s in one run and 44.3 to 44.6 s in
the others. The slow run had chosen `-p 2`.

Cause: with 5.8 to 7 GB available, the room is under the 6 GB small-room line,
so each compile beside the largest counts 0.7 GB. For skyshop the largest is
the 4.1 GB `skyffi` package, which left room for one other compile: `-p 2`.
v0.27.8 chose `-p 4` to `-p 6` there.

## Forced `-p`, cold build

| `-p` | `go build` wall |
|---|---|
| 2 | 52.2 s |
| 3 | 46.7 s |
| 4 | 45.8 s |
| 5 | 44.5 s |
| 8 | 45.6 s |

The sum of all Go processes peaked at 3.9 to 4.1 GB at every setting. A `go
build -debug-trace` of the cold build showed only `rt/console_app` (2.4 s) and
the build stamp (0.01 s) compiling beside the 19.2 s `skyffi` compile.

## After the fix

With 6.0 GB available the plan is now `-p 4`; with 7.4 GB it is Go's default.

| | branch (`82c74a396`) | v0.27.8 (`03d61922f`) |
|---|---|---|
| whole build, median, alternating runs | 49.9 s | 50.8 s |
| `go build` range | 44.1 to 45.1 s | 44.8 to 45.0 s |

A cold skyshop build compiles the Go runtime, the Go standard library and the
Stripe SDK either way, so it barely changes between the two versions. This run
shows that the `-p` sizing no longer makes it slower.
