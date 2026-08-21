# Sky.Spa v1 — progress tracker (autonomous build)

> **Resume protocol:** read this file + `.claude/AUTONOMOUS_GOAL.md` first. This
> tracks the unattended build of the desktop/mobile-first explicit-boundary
> Sky.Spa v1. Updated at every phase boundary. Work branch: `exp/spa`. Verified
> prototype baseline: `exp/spa-prototype`.

## Status: P1 in progress (productionize + land the partition)

| Phase | State | Notes |
|---|---|---|
| P1 — productionize + land partition | 🔨 in progress | prototype merged into `exp/spa` (merge commit); now: rebuild compiler, full §0.2.1 gates green, Sky.Live unbroken |
| P2 — client-side diff renderer | ⏳ | diffTrees + __skyApplyPatches reuse; focus/cursor test |
| P3 — interpretCmd real effects | ⏳ | perform async, Time/Http, subscriptions |
| P4 — Std.Spa v1 + explicit boundary | ⏳ | config(routes/subs), routing, Http+Codec server boundary |
| P5 — real e2e example + verify | ⏳ | client UI + stateless Sky backend; browser e2e; bundle number |
| P6 — Judge + docs/templates + final sweep | ⏳ | fresh-context Judge vs DONE list |

## Verified baseline (do not re-litigate)

- Emit path proven: real Sky.Spa app → wasm → renders client-side (headless ALL
  PASS, zero server). Bundle 7.51 MB raw / 2.04 MB gzip (desktop/mobile-embed
  weight; web is a later bet).
- Partition: the ONLY hard js blocker is `modernc.org/sqlite`→`libc`; net/http/os/
  syscall compile under js. `rt` is js-buildable; Sky.Live unbroken (verified).
- Impl surfaces (from `exp/spa-prototype`, now merged): `live_core.go` (93 TEA
  decls), `rt_server.go`, `spa_core.go`/`live_wasm.go`/`dom_render_wasm.go`,
  `Std/Spa.sky`, ~70 files tagged `//go:build !js`, `console_app`/`hub` js
  placeholders.

## Known P1 risks to handle

- **fresh-compiler gate:** `runtime-go` changed → rebuild `sky-out/sky` via
  `scripts/build.sh` before any gate runs (per CLAUDE.md norms).
- **census/coverage ratchets:** new `Std.Spa` module + `Spa_config`/`Spa_app`
  kernels + new runtime files/build-tags may trip `kernel_api_covers_*`,
  `config-surface`, `denominators`, `coverage-ledger`. Surface INCREASE is fine;
  register the new kernels in `rust/crates/project/src/kernel_api.rs`.
- **gofmt:** go-1.26 toolchain vs repo 1.25 formatting — new files must be
  gofmt-clean; do not reformat untouched files.
- `spa-counter/` sits at repo root (prototype test app) — P5 replaces it with a
  real example under `examples/`.

## Decisions / deviations log

- (P0) auto-split (v2) deferred; v1 is explicit-boundary. Web (TinyGo/JS)
  deferred. Both per user greenlight of option (a).
