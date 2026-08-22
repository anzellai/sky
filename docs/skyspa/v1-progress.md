# Sky.Spa v1 — progress tracker (autonomous build)

> **Resume protocol:** read this file + `.claude/AUTONOMOUS_GOAL.md` first. This
> tracks the unattended build of the desktop/mobile-first explicit-boundary
> Sky.Spa v1. Updated at every phase boundary. Work branch: `exp/spa`. Verified
> prototype baseline: `exp/spa-prototype`.

## Status: P2 DONE ✅ — P3 next (interpretCmd real effects)

| Phase | State | Notes |
|---|---|---|
| P1 — productionize + land partition | ✅ **done** | partition + census/kernel/coverage fixes on `exp/spa` (@c39dd8a0). Full §0.2.1 verified SERIALLY green: `cargo test --workspace`=0, example-sweep=0, conformance=0, 29 harness gates pass, entry_exit_contract 3/3, `GOOS=js` rt build=0, Sky.Live 09/19 build=0, spa render ALL PASS. |
| P2 — client-side diff renderer | ✅ **done** | Full re-render replaced by `diffTrees(prev,new,clientState)` → `[]Patch` applied by sky-id (`spaApplyPatches`); `spaPrev *VNode` kept across dispatches; first render still full-mounts. Focus/caret/dirty-input authority ported from `__skyApplyPatches` to the Go/syscall.js Patch VALUE model. `spa-input/` acceptance test 23/23 PASS (focus retained, caret preserved mid-string + across programmatic value write, value not clobbered, node identity stable, 0 elements created per keystroke = minimal patch); `spa-counter/` still ALL PASS. `diffTrees`/`live_core.go` UNCHANGED — all edits in the two `//go:build js` files, so Sky.Live server diff is untouched. |
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
- (P1) LESSON: never run two heavy suites (cargo test / example-sweep) concurrently
  in ONE worktree — they clobber shared `examples/*/sky-out` + Go/sky caches and
  produce FALSE failures (a spurious entry_exit_contract + sweep fail that both
  vanished when re-run serially in a quiet tree). Run suites serially; verify a
  failure in isolation before treating it as real.
- (P2) DEVIATION from a literal `__skyApplyPatches` port: NO blanket "drop
  value/checked/selected on a focused field". The server drops them because a
  Sky.Live keystroke is async (debounced, unacked) so the DOM can hold a value
  the server hasn't seen. A Sky.Spa dispatch is SYNCHRONOUS + client-authoritative
  — the keystroke updates the model before the re-render, so there is never an
  unacked value. `diffTrees`' `clientState` alignment (live_core.go:1596) already
  skips a value patch precisely when model == what the DOM shows (the user's own
  typing), which makes typing a minimal patch set; the only value patches that
  reach a focused input are genuine PROGRAMMATIC changes (model != DOM) which
  SHOULD apply — and do, with caret snapshot/restore so the cursor never jumps.
  Kept from the server port: open-<select> defence, focus-containing text/HTML
  guards, caret/scroll snapshot+restore, idempotent setAttribute.
- (P2) KNOWN GAP (honest): the focused input's DOM node IDENTITY is preserved
  only on the attr/text-patch path (the tested typing case — no ancestor rebuild).
  If an ancestor HTML/child-count patch fires while an input inside it is focused,
  `rebuildChildrenPreservingFocus` restores focus + caret + value by sky-id but
  the node is re-created (identity changes). Real browsers keep IME/composition
  state on the live node; the server solves this by splicing the live node into
  the parsed HTML (`__skyReplaceHTMLPreservingFocus`). Client-side node-splice is
  deferred — not needed for the P2 typing acceptance gate, flagged for a later
  pass if a real app hits it.
