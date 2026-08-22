# Sky.Spa v1 — progress tracker (autonomous build)

> **Resume protocol:** read this file + `.claude/AUTONOMOUS_GOAL.md` first. This
> tracks the unattended build of the desktop/mobile-first explicit-boundary
> Sky.Spa v1. Updated at every phase boundary. Work branch: `exp/spa`. Verified
> prototype baseline: `exp/spa-prototype`.

## Status: P5 DONE ✅ — P6 next (Judge + docs/templates + final sweep)

| Phase | State | Notes |
|---|---|---|
| P1 — productionize + land partition | ✅ **done** | partition + census/kernel/coverage fixes on `exp/spa` (@c39dd8a0). Full §0.2.1 verified SERIALLY green: `cargo test --workspace`=0, example-sweep=0, conformance=0, 29 harness gates pass, entry_exit_contract 3/3, `GOOS=js` rt build=0, Sky.Live 09/19 build=0, spa render ALL PASS. |
| P2 — client-side diff renderer | ✅ **done** | Full re-render replaced by `diffTrees(prev,new,clientState)` → `[]Patch` applied by sky-id (`spaApplyPatches`); `spaPrev *VNode` kept across dispatches; first render still full-mounts. Focus/caret/dirty-input authority ported from `__skyApplyPatches` to the Go/syscall.js Patch VALUE model. `spa-input/` acceptance test 23/23 PASS (focus retained, caret preserved mid-string + across programmatic value write, value not clobbered, node identity stable, 0 elements created per keystroke = minimal patch); `spa-counter/` still ALL PASS. `diffTrees`/`live_core.go` UNCHANGED — all edits in the two `//go:build js` files, so Sky.Live server diff is untouched. |
| P3 — interpretCmd real effects | ✅ **done** | `interpretCmd` runs real effects: `Cmd.perform` on a per-perform cooperatively-scheduled goroutine (wasm single thread, NOT an OS thread) that dispatches `toMsg(result)`; sync kernels (`Time.now`/`Random`) return inline; async `Http.get`/`post` split to a browser-`fetch` kernel (`http_wasm.go`) that BLOCKS on a channel the Promise fills and returns a real `Result` (required by typed-emit's `TaskCoerceT`). `subscriptions : model -> Sub msg` added to `Std.Spa.config`; the driver reconciles `Sub.every` timers after every dispatch (start/stop/leave via setInterval/clearInterval). `Cmd.publish` = documented client no-op. Headless acceptance: spa-perform / spa-sub / spa-http ALL PASS; spa-counter + spa-input (23/23) still PASS. `live_core.go` + all `!js` runtime UNCHANGED; only shared change is the `Http_get`/`Http_post` build-split (host net/http impl moved verbatim to `http_notjs.go`). |
| P4 — Std.Spa v1 + explicit boundary | ✅ **done** | Client-side routing (History API) + the explicit typed server boundary. `Std.Spa` gains `Route`/`route`/`withRoutes`/`withNotFound`/`withOnNavigate` (opt-in builders — config stays the 4 TEA fields so the 5 route-less spa apps keep compiling; same `route`/`withOnNavigate` names as Sky.Live) and `getJson`/`postJson` (pure Sky over `Cmd.perform`+`Http`+`Codec`, NO new kernel). Runtime: `spa_core.go` (portable) config-map + `Spa_route`/`Spa_with*` + `spaMatchRoute`/`spaResolveRoutes` (reimplements Sky.Live's `matchRoute`/`splitPath` client-side — **`live.go` NOT touched**); `live_wasm.go` (js-only) deep-link at mount + document-click interception (pushState, skips external/target=_blank/download/sky-external/modified-click) + popstate + `RecordUpdate` sets `model.Page` (Live convention) + onNavigate via TEA `step`. Router installed ONLY when routes exist. **shared vs js:** shared portable changes = `spa_core.go`, `Std/Spa.sky`, `kernel_surface.rs`, `docs/coverage/`; js-only = `live_wasm.go`. Tests: `spa-router/` routing ALL PASS (deep-link, intercepted no-reload nav, pushState, onNavigate, popstate, notFound, external passthrough); `spa-boundary/` real wasm-client↔stateless-Sky-backend round-trip ALL PASS with ONE symlinked `Shared.sky` (shared-type-flows-both-ways proven: a field added to Shared breaks BOTH compiles). Verify (serial): build.sh=0, `GOOS=js` rt build=0, `go build ./...`+`go test ./rt/...`=0, `cargo test -p sky`=0, kernel_surface+dark-module ratchets+both census `--check`=PASS, `sky doc Std.Spa` OK, Sky.Live 09+19 clean-slate build + run HTTP 200 (sky-nav intact), all 5 prior spa apps rebuilt+headless ALL PASS. |
| P5 — real e2e example + verify | ✅ **done** | **`examples/60-spa-todos`** — the culminating full-stack Sky.Spa app. Client (wasm) = `Model { page, ui, data }`, **`Std.Ui` Element view** (cross-platform; DONE-list criterion 6 — the Spa client renderer paints `Element` to the DOM, verified headlessly), pure client-local UI (new-todo text, filter, edit buffer) with zero round-trip, durable data via `Spa.getJson`/`postJson` + shared `Std.Codec`, History-API filter routes. Server = stateless `Sky.Http.Server` + SQLite (`Std.Db.Store`), re-validates every request, `Server.api` (CSRF-bypassed stateless JSON API), serves the client same-origin. `shared/Shared.sky` symlinked (mode 120000) into both — one wire contract. **e2e** `run_roundtrip.sh` (real wasm client ↔ real backend, headless): **24/24 PASS** — durable add/toggle/rename/delete persist (backend curl truth), pure UI (typing + 3 filter navs + edit buffer) makes provably **0** network calls, routing changes the view without reload, a reloaded client rehydrates from the backend. **Bundle:** `main.wasm` **9,493,611 B raw / 2,519,062 B gzip** (desktop/mobile-embed weight; web is the documented v2 open decision). **Browser:** extension DISCONNECTED (`list_connected_browsers` → `[]`); app serves same-origin (`/`, `/main.wasm`, `/wasm_exec.js` all HTTP 200), `run.sh` + README give the exact URL/steps — headless is the acceptance proof, real-browser pixel check pending the extension. **Verify (serial):** build.sh=0, `GOOS=js` rt build=0, `go build ./...`+`go test ./rt/...`=0, `cargo test -p sky`=0, `gates_measure_a_fresh_compiler` 21/21 (both run scripts carry the fresh-compiler guard; also fixed the pre-existing boundary-script miss on the branch base), denominators+coverage-ledger `--check`=PASS (no regen needed), coerce-floor golden gains the two measured rows (client narrow=238, server narrow=131), example builds clean-slate, Sky.Live 09+19 build+run HTTP 200, all 7 prior spa apps PASS. |
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
- (P4) DEVIATION from the literal brief ("routes in `config`"): routing is
  attached with **opt-in builders** (`withRoutes`/`withNotFound`/`withOnNavigate`)
  and `config` stays the four TEA fields. Rationale: a required `routes`/
  `notFound` field would break all five shipped route-less spa apps and force a
  meaningless `notFound` on a counter; Sky.Live itself attaches every optional
  via `withX`, so a routed Spa app reads the same (same `route`/`withOnNavigate`
  names). Full Phase-0 findings + API + five-pillar check:
  `docs/skyspa/p4-routing-and-boundary.md`.
- (P4) The boundary helpers (`getJson`/`postJson`) are **pure Sky** over
  `Cmd.perform` + `Http.get`/`post` + `Codec.fromJson`/`toJson` +
  `Task.andThenResult` — NO new runtime kernel, so zero census/kernel surface
  for them (only the routing kernels register). `decodeResponse` treats a
  non-2xx as `Err` (a 4xx/5xx is a completed round trip carrying a value, per
  `http_wasm.go`, not a transport failure). Untrusted-client SECURITY is
  first-class in the module doc + the design note (backend re-validates/
  re-authorizes; helpers carry no ambient authority).
- (P4) `live.go` is **NOT touched** — client route matching
  (`spaMatchRoute`/`spaSplitPath`) reimplements the server algorithm in the
  portable `spa_core.go`, so the Sky.Live server path is byte-identical. Only
  `live_wasm.go` (js-only) gained the router; the shared portable change is
  `spa_core.go` (config-map materialisation + route kernels + matcher).
- (P4) Boundary test structure: ONE `spa-boundary/shared/Shared.sky`
  **symlinked** into both `client/src/` and `server/src/` (committed as git
  symlinks, mode 120000), so it is literally one source. Two `module Main`
  entries in one project co-compile (dir-scan pulls both), which made the client
  wasm reference `Server_listen` — so client and server are **separate
  projects** sharing the symlinked module. `run_roundtrip.sh` reproduces the
  whole thing.
- (P3) deferred to P4/later (in scope there): `Cmd.publish` client fan-out is a
  server-boundary concern (P4); only `Sub.every` timers are wired (topic/stream/
  ws subs later); client `HttpResponse.Headers` empty (status+body only);
  `Http_getT`/`Http_request` still net/http under js (latent, not on the client
  path). None block P3's acceptance.
- (P5) FINDING — **`Std.Ui` renders under the Spa client renderer** (criterion 6
  is met, not deferred). `Std.Ui.layout` is pure Sky that lowers `Element` →
  `Std.Html` VNodes, and `dom_render_wasm.go`'s `buildDOM` paints any VNode
  (tag + attrs incl. `class`/`style`, text, raw-HTML span) — so no renderer
  change was needed. Confirmed with a throwaway `Std.Ui` Spa probe (rendered +
  updated client-side) BEFORE committing the app to `Std.Ui`; the app's headless
  e2e is the standing proof. So the app view is `Std.Ui`, NOT `Std.Html` — the
  same `Element` view could target Live/Tui/Webview.
- (P5) DECISION — the backend's `/api/*` are **`Server.api`** routes, not
  `Server.get`/`post`. A Sky.Spa client talks to a STATELESS JSON API with no
  cookie session; `Server.post` applies browser-form CSRF (a 403
  `csrf_missing`), which guards nothing here and broke the round-trip. `Server.api`
  bypasses CSRF by design; security rests on the handler re-validating +
  re-reading authoritative data (it does), and a real app adds an
  `Authorization` header the backend verifies. (Diagnosed live: the first
  headless run's POST returned the CSRF JSON and nothing persisted.)
- (P5) DECISION — client uses **relative** API URLs (`/api/todos`), so the
  browser path is same-origin (the backend serves the client via
  `Server.static "/" "../public"`) with no CORS, and the headless runner wraps
  `fetch` to resolve relative URLs against the backend base. `SKY_DB_PATH` (not
  a sky.toml `[database].path`, which trips the config-migration advisory) sets
  the SQLite file; `TODOS_PORT` picks a unique high port (default 8951).
- (P5) GATE — `gates_measure_a_fresh_compiler` scans EVERY `.sh` for a
  non-comment `sky-out/sky` and fails it unless the script also sources
  `scripts/lib/fresh-compiler.sh` + calls `require_fresh_compiler`. Both P5 run
  scripts carry it; also fixed `spa-boundary/run_roundtrip.sh`, a **pre-existing**
  miss on this worktree's exp/spa base (the coordinator had fixed it on exp/spa
  after the branch point). A `.cjs` harness running a pre-built `main.wasm` does
  NOT trip it.
- (P5) CENSUS — `examples/60-spa-todos/{client,server}` are each a
  `sky.toml`+`src/` project, so coerce-floor's recursive walk discovers them and
  they need golden rows (like `examples/39-hub-demo/*`). `--bless` refuses under
  a subset shortfall, so the two rows were measured with
  `xtask coerce-floor --only=…` (client narrow=238, server narrow=131; adapter=0,
  dispatch=0 both) and hand-added — the header sanctions hand-editing. denominators
  + coverage-ledger `--check` both PASS unchanged (the app uses only
  already-covered stdlib surface).
- (P5) BROWSER — the Chrome extension is DISCONNECTED
  (`mcp__claude-in-chrome__list_connected_browsers` → `[]`), so the pixel-level
  in-browser check is env-blocked, exactly as design.md §9 anticipated. Not a
  blocker: the app serves same-origin and `run.sh`/README document the exact URL
  (`http://localhost:8951/`) and click-through; the 24/24 headless full-loop is
  the acceptance proof.
- (P6) CORRECTION (my diagnosis was wrong; the P6b agent disproved it with
  measurements): the CI `build-corpus` TIMEOUT (18-min cap → job `cancelled`) on
  the P5 push was NOT caused by the Sky.Spa work. `examples/60-spa-todos` is a
  client/server BUNDLE with no top-level `src/`, so build-run's single-level
  discovery never built it (client OR server); the client's native build is ~1.5s
  regardless. build-corpus is chronically ~16.6 min against its 18-min cap
  (measured at P3, which was green), so a slow runner tips it — pre-existing
  infra/chronic-budget variance, not a Spa regression. A re-run on a normal
  runner passes. P6b (a Shape::Spa skip + a bundle-descent that would have ADDED
  the server + 2 hub apps as new cold builds) was DISCARDED: its premise was
  false and its bundle-descent would WORSEN the chronic build-corpus budget. The
  todos remains verified e2e by its committed `run_roundtrip.sh` (24/24) + P5's
  build; it is intentionally out of build-run (bundle structure). The chronic
  build-corpus budget is a PRE-EXISTING main-branch concern to flag to the user,
  out of scope for Spa v1 (and the gate forbids raising the ceiling).

## v1 STATUS — built, gate-green, awaiting user sign-off (2026-08-22)

**All 6 phases complete + landed on `exp/spa` (@78ac6339, pushed).** Verification:
- **CI GREEN cross-platform (PR #189, fails=0)** on the final state — independent
  Linux+macOS run of the full test suite.
- **Full §0.2.1 sweep GREEN locally**: `cargo test --workspace`=0, T1 harness
  VERDICT PASS, T2 behaviour-corpus=0, full example-sweep=0, conformance=0.
- **Every acceptance test re-verified by the coordinator** across phase merges:
  client-diff focus/caret (spa-input 23/23), effects (spa-perform/sub/http),
  routing (spa-router 13/13), explicit boundary (spa-boundary round-trip 5/5,
  shared-codec-both-ways), todos e2e (24/24: durable CRUD persisted, pure-UI
  zero-network, reload rehydrates), Sky.Live 09/19 unbroken, docs 14/14.

**The one step NOT completed: the dedicated fresh-context adversarial Judge
AGENT.** Four background agents (P1-tail, P3-tail, P6a, the Judge) stalled on the
SAME agent-runtime pattern — hanging immediately after spawning a background
build/test and waiting on it (the Judge's last line: "kick off the compiler build
in the background"). This is an environment/agent-runtime issue, not a defect in
the work. Per N-strikes, not retried a 5th time. The independent verification is
instead carried by CI-green (cross-platform) + the full local sweep + the
per-phase re-verifications above. For a true independent adversarial pass the user
can run `/code-review ultra` (cloud, avoids the local-agent stall).

**Genuine env-blocker (needs the user):** the real-Chrome pixel-level browser
check — the Chrome extension is disconnected (`list_connected_browsers → []`).
The app is served same-origin (HTTP 200) + headless-e2e-verified; only the visual
click-through is pending the extension. Not a defect; documented open path.

**Deferred to v2 (documented, per user greenlight of option (a)):** production-web
bundle (TinyGo/Sky→JS; current ~2.5 MB gzip = desktop/mobile-embed), the
auto-split (auto-split.md), `Cmd.publish` client fan-out, typed-Int route params.

**Awaiting the user:** (1) final sign-off / independent review if wanted; (2) the
merge-to-main decision (gated per standing rule — NOT merged); (3) connect the
Chrome extension for the visual browser check if desired.
