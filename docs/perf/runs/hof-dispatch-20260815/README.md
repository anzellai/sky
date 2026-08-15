# HOF dispatch — A/B for the `coerce_if_needed` eta-expansion

What the `Func → Func` eta-expansion in `rust/crates/lower/src/lower.rs`
is worth, measured rather than estimated. Background and the corrected
cost model: `docs/architecture/sky-compiler-architecture.md` §5.3.

## The control

Both arms are **the same worktree at the same commit**, compiled by two
`sky` binaries that differ by exactly one thing: the `func_shape_eta`
branch in `coerce_if_needed`, gated off for the "before" arm. Same Go
toolchain, same app source, same `sky.toml`.

That matters more than it sounds. The obvious baseline — a compiler
binary lying around from an earlier commit — would have folded a week of
unrelated changes (including the `Std.Ui` six-scan fusion) into the
delta. This isolates the one branch.

Verified before measuring: the two app binaries differ (`cmp`), the
"after" emitted Go carries 13 eta sites and the "before" carries 0.

## Conditions

| | |
|---|---|
| Host | Apple M1, 8 cores, 16 GB, macOS 26.5.2 — arm64 |
| Branch | `perf/hof-dispatch-codegen` @ `e613cbec` |
| App | `examples/26-ui-showcase` (384 elements) |
| `GOMAXPROCS` | **1** on the app — the CPU-bound regime, where a codegen change is visible |
| Load | `tools/skyliveload`, loopback, 25 sessions, **closed loop** (`-think 0`), 20 s window, 5 s warmup, 3 s ramp |
| Session store | `memory` |
| Run order | `before, after` × 3, **alternating**, so thermal drift and burst-credit decay land on both arms equally |
| Readiness | polled, then the listening pid is asserted to be ours — a stale app on the port would otherwise serve every request |

## Result

| arm | run 1 | run 2 | run 3 | mean | p95 |
|---|---|---|---|---|---|
| before (adapter) | 132.18 | 137.06 | 137.18 | **135.47** | ~580 ms |
| after (eta) | 184.88 | 184.88 | 184.92 | **184.89** | ~362 ms |

interactions/sec; error rate 0 and `valid: true` on all six runs.

**1.36× throughput.** The ranges do not overlap: the slowest "after" run
(184.88) is 35% above the fastest "before" run (137.18), so the gain is
far outside the observed spread. The "before" arm varies by 3.7%, the
"after" arm by 0.02%.

## Allocation, on the isolated shape

The wall-clock number above is a whole-request figure. The mechanism is
measured directly by `rust/crates/sky/tests/hof_dispatch_shape.rs`, which
runs `testing.AllocsPerRun` against the **real emitted Go**: a six-marker
scan over six attributes (36 element visits) allocates **318** times with
the adapter and **126** without — 5.3 allocations per element visit
removed. That test is the regression gate; a counter does not flake with
machine load the way a wall-clock budget would.

The residual 126 is not the adapter. It is the other erasure on the same
path, which this change deliberately does not touch: `rt.AsListT[any]`
rebuilds the list per `List.any` call, and the runtime's `SkyLen` /
`SkyElem` / `SkyTailSlice` take `x any`, re-boxing the slice header per
access. Typed variants of those are the obvious next step and are **not**
part of this change.

## What these numbers do not say

- **Do not extrapolate the microbenchmark ratio to a request.** A large
  share of an interaction is network syscall that no compiler change
  touches. The per-element allocation ratio is 2.5×; the whole-request
  throughput ratio is 1.36×.
- **Closed loop is the sensitive regime, not the typical one.** With real
  think time the server spends proportionally more time idle and the
  delta shrinks.
- **arm64, one app.** `docs/perf/skylive-remote-validation.md` found x86
  differs by ~30% on the memory figure. Ratios should travel better than
  absolutes, but this is one app shape (`Std.Ui`-heavy) chosen *because*
  it is the one the change targets — an app that never passes a typed
  callback to a polymorphic HOF gains nothing.

## Reproducing

`bench.sh` in this directory. It needs `tools/skyliveload` built, and the
two compiler binaries — build the second by gating off the
`func_shape_eta` call in `coerce_if_needed` and rebuilding.
