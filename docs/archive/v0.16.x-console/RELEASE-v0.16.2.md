# v0.16.2 — composite test apps + measured verification

> **Honest framing**: v0.16.2 ships the v0.16.2 RFC's STRUCTURAL
> delivery — 4 composite test apps, 5 measurement experiments,
> reproducible BEFORE / AFTER baselines, and 3 fixed bugs.
>
> The RFC's wall-time goal (< 10 min cold-cache) was **NOT met**
> because the cull-legacy-specs step found only 1 spec safely
> deletable without losing regression coverage. The composites
> add ~5400 LOC of test SURFACE but cannot substitute for the
> small specs' byte-level regression contracts (e.g. "this CSS
> string never appears in emitted output", "kernel rt.X is
> routed"). v0.16.3 will close that gap via composite-side
> absence-check augmentation.

## What shipped

### 4 composite test apps

| App | Surface | LOC |
|---|---|---|
| `examples/35-composite-generics` | List / Dict (typed) / Maybe / Result / Json / Encoding / Crypto / Math / Decimal / Regex / Pure | 1191 |
| `examples/36-composite-server` | Sky.Http.Server / Auth / Db / PubSub / Cache / RateLimit / CSV / Middleware | 1313 |
| `examples/37-composite-live-shop` | Sky.Live / Std.Ui / Std.Ui.Chart / Std.Live.Head / pub-sub / sessions | 1722 |
| `examples/38-composite-ui-multibackend` | Std.Ui across Sky.Live + Sky.Tui + Sky.Webview (shared `view`) | 983 |

Each composite:
- Builds clean from a wiped slate (`rm -rf sky-out .skycache .skydeps && sky build`)
- Boots + serves real traffic / paints real output (where applicable)
- Carries Sky.Test assertions in `tests/Composite*Test.sky` (43 assertions total)
- Stays under the RFC's 30-unique-generic-instantiation budget per composite

### 5 measurement experiments

Under `docs/v0.16.x-console/composite-test-experiments/`:

- `cold-cache-baseline.sh` — wall, peak GOCACHE, peak RSS, disk Δ
- `warm-cache-baseline.sh` — second-run cache-hit speed
- `disk-pressure-experiment.sh` — sweep under 30 GB placeholder
- `memory-pressure-experiment.sh` — sweep with 4 GB Python hog
- `scale-projection.sh` — N synthetic fixtures vs wall + cache slope

Each appends a row to its `*-RUN_LOG.md` for reproducible
comparison across runs / committers.

### Measured baselines

| Experiment | Budget | BEFORE (24c28567) | AFTER (92626eb2) | Δ |
|---|---|---|---|---|
| cold-cache wall | < 600s | 2222s (37 min) | 2183s (36.4 min) | **-39s** (-1.8%) |
| cold-cache peak cache | < 5 GB | 81.26 GB | 81.22 GB | ~0 |
| cold-cache peak RSS | < 4 GB | 2.66 GB ✓ | 2.66 GB ✓ | ~0 |
| cold-cache disk Δ | minimal | +5.46 GB | **-10.29 GB** | **15.75 GB reclaimed** |
| examples | green | 502 / 0 fails | 501 / 0 fails | -1 (cull) |

The disk Δ delta is the visible impact of #459's trap fix:
GOCACHE now reclaimed at end-of-run instead of orphaned.

### Bundled bug fixes

- **#459** — `scripts/cabal-test.sh` cleanup trap actually fires.
  `exec cabal test` was replacing the bash process, discarding
  the EXIT trap. Hit 2026-06-04 after the v0.16.1 cabal sweep —
  81 GB orphan in nix-shell's TMPDIR. Now: 80 GB reclaimed
  per run (visible in the disk-Δ baseline above).
- **#460** — `copyRuntime` writes a `.sky-runtime-fingerprint`
  sentinel under `sky-out/rt/` and wipes stale `*.go` on
  drift. Closes the SkyDeploy 0.15.59 → 0.16.1 bump regression
  where PR10-G's deleted `console_loop.go` lingered in
  downstream apps. Regression test at
  `test/Sky/Build/RuntimeFingerprintSpec.hs`.
- **#462** — `String.padLeft N ch s` renders the pad char
  correctly via `padChar` helper. Previously emitted decimal
  codepoint (`padLeft 5 ' ' "X"` → `"3232323232X"`).
  Regression test at `runtime-go/rt/string_pad_test.go`.

### Bugs surfaced + filed (NOT fixed in v0.16.2)

Composite-building exercised the stdlib in real-world patterns
that the small fixture specs never hit. 7 new bugs surfaced and
were filed for v0.16.3+ fixing — workarounds inlined in the
composites so they ship green:

- **#461** — cross-module `Set a` panics at `rt.Coerce`
- **#463** — partial application of 3-arg typed FFI kernel miscompiles
- **#464** — `Sky.Http.Middleware` `Handler` type undeclared
- **#465** — 2-arg partial application of typed FFI kernel miscompiles (sibling of #463)
- **#466** — `Server.listen` ignores Method — same path + different method panics
- **#467** — `Server.json |> Server.withStatus` runtime panic
- **#468** — `Middleware.withRateLimit` incompatible with typed `SkyTask`
- **#469** — `Middleware.withLogging` / `withCors` swallow `StreamHandler` sentinel

These are HIGH-VALUE finds. Real-world app-building surfaces
real-world bugs that synthetic test fixtures cannot.

### Cull: 1 spec deleted, 22 kept

The cull-legacy-specs agent ran byte-level verification on every
spec the composite agents flagged. Found only `PubSubPublishTaskSpec`
safely subsumable — the rest guard narrow regression contracts
(CSS byte-string absence, kernel routing pins, runtime branches)
that the composites' end-to-end shape doesn't pin.

This is an honest finding. The RFC's "composites replace ~73
specs" premise was over-optimistic; the realistic path is
composite-side **absence-check augmentation** (Sky.Test assertions
that grep the emitted `sky-out/main.go` for the same byte-strings
the legacy specs assert), then aggressive cull. That's v0.16.3.

## Migration

No user-visible breaking changes. The 4 composite apps are
additive examples; the spec deletion only affects the cabal
test suite organisation.

## Files

- 4 composite apps: `examples/35-` / `36-` / `37-` / `38-`
- 5 experiment scripts: `docs/v0.16.x-console/composite-test-experiments/`
- 1 deleted spec: `test/Sky/Build/PubSubPublishTaskSpec.hs`
- 3 bug-fix commits on main: `1a0eeea0` `ca206e94` `b0dc7857`
- 7 newly-filed bugs in the task pipeline

## v0.16.3 scope

1. Composite-side absence-check augmentation — extend
   `tests/Composite*Test.sky` with `Test.contains` /
   `Test.notContains` over `examples/*/sky-out/main.go` so the
   CSS / kernel-routing assertions LIVE inside the composites.
2. Aggressive cull pass — once augmentation lands, 30+ specs
   become safely deletable.
3. Re-baseline. Goal: < 10 min wall cold-cache.
4. Fix bugs #461, #463-#469.
5. Then: the original `sky console serve` hub work (was on
   v0.16.3 before this re-scoping).
