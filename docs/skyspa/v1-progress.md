# Sky.Spa v1 — progress tracker (autonomous build)

> **Resume protocol:** read this file + `.claude/AUTONOMOUS_GOAL.md` first. This
> tracks the unattended build of the desktop/mobile-first explicit-boundary
> Sky.Spa v1. Updated at every phase boundary. Work branch: `exp/spa`. Verified
> prototype baseline: `exp/spa-prototype`.

## Status: P3 DONE ✅ — P4 next (Std.Spa v1 + explicit boundary)

| Phase | State | Notes |
|---|---|---|
| P1 — productionize + land partition | ✅ **done** | partition + census/kernel/coverage fixes on `exp/spa` (@c39dd8a0). Full §0.2.1 verified SERIALLY green: `cargo test --workspace`=0, example-sweep=0, conformance=0, 29 harness gates pass, entry_exit_contract 3/3, `GOOS=js` rt build=0, Sky.Live 09/19 build=0, spa render ALL PASS. |
| P2 — client-side diff renderer | ✅ **done** | Full re-render replaced by `diffTrees(prev,new,clientState)` → `[]Patch` applied by sky-id (`spaApplyPatches`); `spaPrev *VNode` kept across dispatches; first render still full-mounts. Focus/caret/dirty-input authority ported from `__skyApplyPatches` to the Go/syscall.js Patch VALUE model. `spa-input/` acceptance test 23/23 PASS (focus retained, caret preserved mid-string + across programmatic value write, value not clobbered, node identity stable, 0 elements created per keystroke = minimal patch); `spa-counter/` still ALL PASS. `diffTrees`/`live_core.go` UNCHANGED — all edits in the two `//go:build js` files, so Sky.Live server diff is untouched. |
| P3 — interpretCmd real effects | ✅ **done** | `interpretCmd` runs real effects: `Cmd.perform` on a per-perform cooperatively-scheduled goroutine (wasm single thread, NOT an OS thread) that dispatches `toMsg(result)`; sync kernels (`Time.now`/`Random`) return inline; async `Http.get`/`post` split to a browser-`fetch` kernel (`http_wasm.go`) that BLOCKS on a channel the Promise fills and returns a real `Result` (required by typed-emit's `TaskCoerceT`). `subscriptions : model -> Sub msg` added to `Std.Spa.config`; the driver reconciles `Sub.every` timers after every dispatch (start/stop/leave via setInterval/clearInterval). `Cmd.publish` = documented client no-op. Headless acceptance: spa-perform / spa-sub / spa-http ALL PASS; spa-counter + spa-input (23/23) still PASS. `live_core.go` + all `!js` runtime UNCHANGED; only shared change is the `Http_get`/`Http_post` build-split (host net/http impl moved verbatim to `http_notjs.go`). |
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
- (P3) FINDING that overrode the spec's async tactic: typed codegen wraps a
  `Cmd.perform` Task in `rt.TaskCoerceT[E,A]` (rt.go:6007), which RUNS the task
  and coerces its **synchronous** return to the declared result type. The spec
  proposed "async Http returns a Promise; dispatch from `.then`/`.catch`, no
  goroutine" — but a Promise/placeholder return is coerced to `HttpResponse`
  the instant the task is invoked and panics (`rt.jsAsync cannot be cast to
  HttpResponse`) before it can settle. So the task MUST return a real
  `SkyResult` when called. Correct mechanism (verified): the client `Http.get`
  kernel (`http_wasm.go`) issues `globalThis.fetch` and **blocks the goroutine
  on a channel** the Promise's `.then`/`.catch` fill, returning the settled
  `Result` — the canonical Go/wasm "await a JS Promise" pattern. Each
  `Cmd.perform` runs on its own **cooperatively-scheduled goroutine** (wasm is
  single-threaded — this is NOT an OS thread) so the block yields to the browser
  event loop rather than freezing it, mirroring the server's `go runPerform`
  minus the SSE/lock. A sync task (`Time.now`/`Random`) returns at once on its
  goroutine and dispatches. So "no goroutine" from the P3 brief could not hold
  under typed-emit; a single cooperative goroutine per perform is the minimal
  correct shape.
- (P3) DECISION — `Cmd.publish` / `publishNoEcho` are a **documented client
  no-op** (not a silent TODO — the interpreter arm carries the rationale, and
  it never drops silently in a way that matters because there is nothing to
  deliver to). Sky.Live pub/sub fans a message across *sessions* (other
  users/tabs) through the server broker; a Sky.Spa client is a single browser
  tab with no peer and no session bus, so an in-tab bus would be surface with
  no consumer in the single-tab TEA model. Cross-tab / cross-user fan-out is a
  server concern and routes through P4's explicit boundary. (Also out of the
  P3 "subscriptions = timers" scope: `Sub.subscribeTopic`/stream/websocket subs
  are not wired on the client in v1 — only `Sub.every`.)
- (P3) SHARED-FILE TOUCH (verified no-regression): the only non-`//go:build js`
  change is splitting `Http_get`/`Http_post` out of `stdlib_extra.go` — the
  net/http bodies moved **verbatim** into `http_notjs.go` (`//go:build !js`),
  the browser-fetch impls into `http_wasm.go` (`//go:build js`); both return
  the identical `Task Error HttpResponse` shape. `Http_getT` / `Http_request` /
  `parseQuery` stay in `stdlib_extra.go` (host net/http; not on the P3 client
  path). Verified: `go test ./rt/...` green, examples 09/19 build + run.
- (P3) LESSON (shared scratchpad): the first `scripts/build.sh` run wrote to
  `scratchpad/build.log`, which a sibling agent (a `wt-perfinv` worktree)
  clobbered — the log showed a foreign tree's crates and a bogus `BUILD_EXIT=1`
  that isn't even a string `build.sh` emits. Re-running with a **unique** log
  filename (`build-a4026-*.log`) showed the real build: green, crates from THIS
  worktree, `sky-out/sky` installed here. Parallel agents share one scratchpad;
  never use a fixed filename there.
- (P3) WATCH-ITEM before merge-to-main: CI `ci-green` = the T1 tier-budget gate
  (`scripts/ci/assert-tier-budget.sh`, ceiling 990s incl. grace) went red once at
  `build-corpus`=999s. main's build-corpus is very noisy (763/901/918s observed),
  the js files are `//go:build js` so they DON'T add to the normal build-corpus
  compile, and my net non-js additions are small (extractions + one stdlib
  module) — so 999 reads as a noisy outlier nudged over the line, not a real
  regression. A fresh CI run gets a fresh timing. IF it recurs persistently as we
  add P4/P5 surface, FIX THE CRITICAL PATH (build-corpus), do NOT raise the
  ceiling (the gate forbids it). Re-check on each push; must be green before
  asking to merge to main.
- (P3) deferred to P4/later (in scope there): `Cmd.publish` client fan-out is a
  server-boundary concern (P4); only `Sub.every` timers are wired (topic/stream/
  ws subs later); client `HttpResponse.Headers` empty (status+body only);
  `Http_getT`/`Http_request` still net/http under js (latent, not on the client
  path). None block P3's acceptance.
