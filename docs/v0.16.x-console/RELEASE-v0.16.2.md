# v0.16.2 — composite test apps + measured verification

> Draft release notes. Finalised when all 4 composites land + legacy
> specs culled + the cold-cache "after" baseline lands inside RFC
> budget (< 10 min wall, < 5 GB peak GOCACHE, < 4 GB peak RSS).

## What

v0.16.2 inverts the cabal-test design that was breaking at scale.
Pre-v0.16.2: ~200 small per-feature fixtures × Go generic
monomorphisation = 81 GB GOCACHE + 37 min cold-cache wall on a
2024 M1. Past 60 min on cold-cabal-store. Documented as the
v0.16.1 ship-cycle pain.

v0.16.2 swaps that for **4 composite test apps** that each
exercise a broad stdlib surface, replacing ~73 small specs. Same
test surface, single Go-generics-per-app instantiation set.

## Composite apps shipped

| App | Surface | Subsumes (count) |
|---|---|---|
| `examples/35-composite-generics` | List / Dict (typed) / Maybe / Result / Json / Encoding / Crypto / Math / Decimal / Regex / Pure | ~7 specs |
| `examples/36-composite-server` | Sky.Http.Server / Auth / Db / PubSub / Cache / RateLimit / CSV / Middleware | ~5 specs |
| `examples/37-composite-live-shop` | Sky.Live / Std.Ui / Std.Ui.Chart / Std.Live.Head / pub-sub / sessions | ~13 specs |
| `examples/38-composite-ui-multibackend` | Std.Ui across Sky.Live + Sky.Tui + Sky.Webview | (TBD) |

## Measured verification

5 experiment scripts under
`docs/v0.16.x-console/composite-test-experiments/` produce
reproducible run-logs:

| Experiment | Budget | v0.16.1 baseline | v0.16.2 (target / actual) |
|---|---|---|---|
| cold-cache-baseline.sh | wall < 600s | wall **2222s** (37min) | < 600s (TBD) |
| cold-cache-baseline.sh | cache < 5 GB | **81.26 GB** | < 5 GB (TBD) |
| cold-cache-baseline.sh | RSS < 4 GB | **2.66 GB** ✓ | < 4 GB (TBD) |
| warm-cache-baseline.sh | wall < 120s | — | < 120s (TBD) |
| disk-pressure-experiment.sh | completes OR fast-fail | — | green (TBD) |
| memory-pressure-experiment.sh | mem-guard fires | — | green (TBD) |
| scale-projection.sh | linear slope | — | linear (TBD) |

## Bundled bug fixes

- **#459** — `scripts/cabal-test.sh` cleanup trap actually fires
  (was leaking 81 GB orphan GOCACHE per run via `exec`).
- **#460** — Sky compiler's `copyRuntime` wipes stale `rt/*.go`
  on runtime-fingerprint drift (closes the v0.16.1 SkyDeploy
  upgrade regression: deleted `console_loop.go` / `subapp.go`
  lingered on downstream apps, breaking `go build` with
  duplicate-declaration).
- **#462** — `String.padLeft` / `padRight` render the pad char
  correctly. Previously `padLeft 5 ' ' "X"` produced
  `"3232323232X"`.

## Bugs surfaced + filed (not fixed in v0.16.2)

The composite-app exercise surfaced 6 new compiler / runtime
bugs in real-world Sky usage patterns, each with documented
workarounds inside the composites:

- **#461** — Cross-module `Set a` return panics at `rt.Coerce`
- **#463** — Partial application of 3-arg typed FFI kernel miscompiles
- **#464** — Sky.Http.Middleware `Handler` type undeclared
- **#465** — 2-arg partial application of typed FFI kernel miscompiles (sibling of #463)
- **#466** — `Server.listen` registers routes by path only; same path + different method panics
- **#467** — `Server.json |> Server.withStatus` panics at runtime
- **#468** — `Middleware.withRateLimit` runtime incompatible with typed `SkyTask`
- **#469** — `Middleware.withLogging` / `withCors` swallow `StreamHandler` sentinel

These are followups for v0.16.x patches as a separate bundle —
the composite tests pin the working surface, the bug list pins
the broken surface.

## CI parity

`--skip=Sky.Build.VerifyAll` is **dropped** from the CI workflow.
The composites cover the surface VerifyAll previously protected.

## Migration

No user-visible breaking changes. The composites are additive
examples; the legacy fixture spec deletions only affect the
cabal test suite's internal organisation.

## Files

- 4 composite apps under `examples/35-`/`36-`/`37-`/`38-`
- 5 measurement scripts under `docs/v0.16.x-console/composite-test-experiments/`
- Updated CI workflow at `.github/workflows/ci.yml`
- Legacy fixture spec deletions across `test/Sky/Build/*Spec.hs` (~73 files)

## Composite-app metrics (cumulative)

- ~5,400 LOC across 4 composites
- ~50 Sky.Test assertions across composite test files
- < 30 generic instantiations per composite (RFC scalability invariant)
- 4 backend-agnostic Std.Ui demonstrations (composite 04)
