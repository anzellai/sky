# CLAUDE.md

> **Quick orientation.** Sky is an Elm-family functional language
> compiling to typed Go. The compiler is now the **Rust rewrite**
> (cargo workspace at `rust/`, primary); the retired Haskell
> compiler lives under `legacy-haskell-compiler/` and serves as the
> **differential oracle** (`sky-out/sky`). Current line is
> **v0.18.x** — the Rust compiler drives every verb (`build` /
> `run` / `check` / `watch` / `test` / `fmt` / `lsp` / `doc` / the
> FFI + dep verbs), emits the same phased pipeline log the oracle
> prints, and reproduces the oracle's Elm-style type-error output
> (RHS-anchored span + filename + source-context window). `sky
> upgrade` self-updates from GitHub releases; `sky watch` honours
> `--clear`/`--debounce`/`--interval`/`--kill-timeout`/`--watch`.
> All the language/runtime baselines carry forward unchanged:
> type-directed lowering + Go generics on parametric record
> aliases, same-module polymorphic re-instantiation, the
> wildcard-`any` soundness gate, auto-TCO + CPS list ops on
> constant Go stack, the documented rt.Coerce residual surface,
> and "if it compiles it works". The Rust verification sweep
> (`cargo test --workspace` + the xtask gate suite
> roundtrip/resolve/infer/reject/build-run/coerce-floor/repro/fuzz +
> golden + the example sweep, cross-checked byte-for-byte against
> the oracle) is the source of truth — green-everywhere is a hard
> release gate.

## Import qualifier rules (v0.17.5+)

Every non-aliased `import M exposing (…)` also registers `M`'s
last segment as an auto-qualifier — `import Sky.Core.Prelude
exposing (..)` binds `Prelude.<name>` too.  Two imports MAY both
try to bind the same qualifier.  Sky resolves that at
canonicalisation time:

* **Explicit alias wins.**  Given `import Std.Db as Db` and a bare
  `import Lib.Db exposing (conn)` in the same file, `Db.<x>`
  resolves to `Std.Db` and `conn` (unqualified) resolves to
  `Lib.Db`.  The bare import's `Db` auto-qualifier is suppressed
  silently — you get what you wrote.  Same-module double imports
  (`Std.Ui as Ui` + `Std.Ui exposing (Element)`) are always fine
  because both bindings resolve to the same canonical module.
* **Two bare, different modules — E1001.**  `import State` +
  `import App.State` both auto-register `State` for DIFFERENT
  modules.  There's no explicit alias to break the tie, so the
  compiler emits `[E1001] Import error: two imports both bind the
  qualifier "State"` with a fix-it suggesting `import App.State
  as AppState`.
* **Two explicit `as X` aliases for different modules — E1001.**
  User error; no way to disambiguate.

Rationale + gate: see `Sky.Canonicalise.Module`'s
`effectiveQualifier` + `detectImportAliasCollisions`.  Regression
spec: `Sky.Canonicalise.DualImportCollisionSpec`.

## `main` entry points (v0.17.5+)

The runtime auto-forces a Task-typed `main` — the generated Go
`func main()` wraps the entry expression in `rt.AnyTaskRun`
unconditionally.  So `main = Cli.program cfg`, `main = Tui.app
cfg`, `main = Webview.app cfg`, and `main = Live.app cfg` all run
their tasks with no trailing `|> Task.run`.

The trailing `|> Task.run` is a no-op at the program entry.
Module-level `Task.run` at a top-level binding (`apiKey =
System.getenv "K" |> Task.run |> Result.withDefault ""`) is still
load-bearing — that runs at binding-init time before `main` is
entered.

## Current state (v0.17.5)

| Surface | Status |
|---|---|
| Type-directed lowering + Go generics on parametric record aliases | ✅ shipped baseline (v0.15) — `Compile.hs` `globalRegionTypes` + `LowerCtx` |
| Same-module polymorphic re-instantiation + wildcard-`any` soundness gate | ✅ shipped baseline (v0.15) |
| Layer 3 stdlib (every kernel module surfaced as Sky source) | ✅ shipped — `sky-stdlib/{Sky/Core,Std,Sky/Http}/*.sky` |
| `Ffi.kernel` mechanism + auto-TCO | ✅ shipped |
| `sky doc` (terminal + HTTP server) / `sky watch` / `sky doctor` / `sky console` | ✅ shipped |
| Sky Console embedded mode + sub-app mount + observability federation | ✅ shipped — v0.16.0 inline; v0.16.1 isolated SSE + HubExporter |
| `sky console-serve` hub (OTLP receivers + SQLite hot store) | ✅ shipped — v0.16.4 |
| Hub UI — multi-service dashboard, drill-down tabs, SSE updates | ✅ shipped — v0.16.4-5 (`runtime-go/rt/console_app/main.go` regenerated from `sky-bundled/console/src/`) |
| `Hub_currentIdentity` kernel + Sky.Live session identity persistence (gob round-trip) | ✅ shipped — v0.16.5 |
| Runtime tenant-prefix SQL enforcement (`HubStoreReaderWithTenant`) | ✅ shipped — v0.16.6 |
| Sky.Webview v0.1 (desktop, macOS) | ✅ shipped — `runtime-go/rt/webview.go`, `sky-stdlib/Std/Webview.sky` |
| Typed-emit fix (resolveWrapParams enclosing-T-var gate) | ✅ shipped — v0.17.0 — closes wrong-typed wrap class |
| rt.Coerce residual surface documented across 8 safety classes | ✅ shipped — v0.17.0 — `docs/v0.17/rt-coerce-residual-surface.md` |
| `scopeStateRef` IORef contract + machine-verified audit spec | ✅ shipped — v0.17.0 — `Compile.hs:496-595` + `Sky.Build.ScopeStateRefAuditSpec` |
| Per-panic-class emission-time regression locks | ✅ shipped — v0.17.0 — `Sky.Build.PanicClassGateSpec` |
| Sealed-iface classifier arm — raw `.(SealedIface)` routes via `rt.Coerce[T]` (§8 non-regression) | ✅ shipped — v0.17.0 — `Compile.hs` `classifyCoerceTarget` + `coerceArg` + `coerceSubject` + `legacyTcoCase` |
| coerceCallArgsAt identity-recovery gate — α-renamed T9000-space synth-vars fall through to erase-scoped `any` widening | ✅ shipped — v0.17.2 — `Compile.hs:16743` + `Sky.Build.TVarSubstitutionLeakSpec` — closes `undefined: T9001` blocker for polymorphic `Cfg msg` view functions with let-bound field access |
| LSP Go-FFI alias diagnostic false-positive close — `runPipelineSt` seeds FFI kernel maps from `LoadedFfiTables` (cached via `ServerState.ssFfiFns`/`ssFfiMods`) | ✅ shipped — v0.17.3 — `src/Sky/Lsp/Server.hs` + `src/Sky/Build/Compile.hs` — closes `Undefined name: <FfiAlias>.<name>` LSP false positive that CLI never emitted |
| LSP cross-module externals gap close — `typecheckWorkspace` 5-round bounded canonicalise+solve fixpoint threading `buildCrossModuleExternalsWithMods` output round-to-round | ✅ shipped — v0.17.3 — `src/Sky/Build/Compile.hs` — closes unannotated wrapper HM-inferred-as-fully-polymorphic (`Lib.Db.exec`) + cross-module alias body expansion (`AppCfg`) false positives |
| Neovim LSP integration suite gate — 17/17 hover/completion/goto-def tests | ✅ green — v0.17.3 — `scripts/lsp-test-nvim.sh` — hover on kernel calls/fields/type names/functions/constructors/lambda params/case patterns, completion on qualified insert-text/field/let-binding, goto-def on type names/functions/constructors/let bindings/lambda params/fields |
| 40-example sweep + 981 cabal specs + 17 LSP integration tests | ✅ green |

## When users ask for an app — the architecture decision matrix

**You (Claude) are the front line for any user who asks "build me X
in Sky".** Before writing more than a one-file proof of concept,
reach alignment with the user on the six decisions below.
Production-grade code does not survive guesswork.

### The six decisions to confirm

1. **App shape** — match the matrix.  Sky.Live for web UI, Sky.Http.Server
   for headless API, Sky.Cli for one-shot / cron, Sky.Tui for terminal
   UI, Sky.Webview for desktop.
2. **Persistence** — SQLite (single-file, embeds) / PostgreSQL
   (Cloud SQL) / Firestore / Redis / none.
3. **Auth** — none / `Std.Auth` (cookies + JWT, you own users) /
   OAuth (Google/GitHub via Go SDK) / external (Auth0 / Clerk /
   Cognito).
4. **Sky.Live session store** — memory (dev only) / sqlite / redis /
   postgres / firestore. Required even when the user picks a
   different primary DB.
5. **Deployment target** — local binary / Docker / Cloud Run via
   SkyDeploy / Kubernetes / VM under systemd.
6. **Observability scope** — local logs only / per-app embedded
   console / push to central `sky console-serve` hub / OTel
   collector (Honeycomb / Tempo / Datadog).

### App shape matrix

| User wants…                              | Use                | Entry point shape                  | Notes |
|------------------------------------------|--------------------|------------------------------------|-------|
| Web app (forms, real-time, UI state)     | **Sky.Live**       | `Std.Live.app cfg`                 | HTTP-first; SSE patches; sessions + cookies + routing built in. |
| HTTP / JSON API (no browser UI)          | **Sky.Http.Server**| `Server.listen 8000 [...]`         | Routes + middleware (CORS / rate-limit / logging / basic-auth). |
| Multi-tenant SaaS / dashboard            | **Sky.Live + auth-app gate** | `Live.app { consoleAuth = … }` | Pair with `sky console-serve` hub for shared telemetry; tenant scope enforced at SQL layer (v0.16.6). |
| Background job / cron worker             | **Sky.Cli**        | `main = Task.run scheduledWork`    | No UI loop; `Task.parallel` for fan-out. |
| Terminal UI (TUI)                        | **Sky.Tui**        | `Std.Tui.app cfg`                  | Same view code as Sky.Live. |
| One-shot CLI tool                        | **Sky.Cli**        | `main = Task.run cliCmd`           | Argparse via `System.args`. |
| Desktop app                              | **Sky.Webview**    | `Std.Webview.app cfg`              | macOS in v0.1; Linux / Windows in v0.2. |
| WebSocket-driven feed                    | **Sky.Http.Server.WebSocket** | `Server.upgrade req` | Bidirectional; `nhooyr.io/websocket`. |
| Server-sent stream (LLM tokens, SSE)     | **Sky.Http.Server.Stream** | `Server.Stream.emit` | Mirror of `Sky.Core.Http.Stream`. |

### Pinned defaults (always apply unless the user overrules)

| Concern              | Default                                                          |
|----------------------|------------------------------------------------------------------|
| View layer           | `Std.Ui` (typed no-CSS DSL).  `Std.Html` only for wrapping raw markup. |
| Auth                 | `Std.Auth` — bcrypt + HS256 JWT cookies.  Never `fmt.Sprintf("%v", secret)`. |
| Forms with passwords | `Ui.form [Ui.onSubmit DoSignIn]` with typed record arg.  Never per-keystroke `onInput` on password. |
| DB                   | `Std.Db` + SQLite for prototypes; PostgreSQL for multi-instance deploys. |
| Money / decimals     | `Std.Money` on `Std.Decimal`.  Never raw `Float` for currency. |
| Concurrency          | `Cmd.batch` / `Task.parallel`.  In-process pub/sub via `Cmd.publish` + `Sub.subscribeTopic`. |
| Observability        | `Std.Log` structured logs; `/_sky/console` auto-mounted; `OTEL_EXPORTER_OTLP_ENDPOINT` for external collector. |
| Errors               | `Result Error a` / `Task Error a`.  Never `String` as error type. |
| No raw HTML / JS     | `Std.Ui` HTML-escapes everything.  `data-sky-eval` forbidden. |

### `sky.toml` shape per decision

```toml
name = "<project>"
version = "0.1.0"
entry = "src/Main.sky"

[live]                          # Sky.Live apps only
port = 8000
store = "sqlite"                # memory / sqlite / redis / postgres / firestore
storePath = "sessions.db"
ttl = "30m"

[database]                      # persistence != none
driver = "sqlite"               # sqlite / postgres
url = "DATABASE_URL"

[auth]                          # auth != none
cookie = "sky_sid"
ttl = "24h"
# secret comes from SKY_AUTH_TOKEN_SECRET — never commit it

[log]
format = "json"
level  = "info"
```

### Production gate — surface to the user

Sky locks down the dev console + banner + metrics endpoint when
`ENV` is anything other than unset / `dev` / `development` /
`local`.  When the user mentions "deploy" / "production" / "Cloud
Run" / "Kubernetes":

* Confirm `ENV=production` will be set on the runtime.
* Confirm `SKY_AUTH_TOKEN_SECRET` is at least 32 bytes.
* Confirm `SKY_CONSOLE_AUTH` is set (`token` or `app`).  Production
  with `SKY_CONSOLE_AUTH` unset emits a warn log and refuses to
  mount `/_sky/console`.
* Confirm session store is NOT memory when there is more than one
  replica.

### When in doubt — one focused question

If the request is ambiguous, ask one focused question per
ambiguity rather than heroically guessing.  Production-grade means
the app survives a restart, scales horizontally without losing
state, refuses cross-tenant reads (v0.16.6 SQL-WHERE gate),
doesn't paint a permanent error banner on transient failures, and
emits structured logs every operator can trace.  All of that is
achievable with the stdlib defaults — but only if you asked the
right six questions first.

**Examples (32 total — `examples/00`-`examples/31`).** Each builds clean
from a wiped slate (`rm -rf sky-out .skycache .skydeps && sky build`).
`examples/00-standard-libs` is the stdlib smoke test (120 assertions).
`examples/13-skyshop` is the Stripe-SDK-scale benchmark (76k FFI
symbols). `examples/26-ui-showcase` exercises every Std.Ui layout
primitive for visual-regression review.
`examples/30-sse-server-demo` exercises `Sky.Http.Server.Stream`.
`examples/31-webview-stopwatch-ui` exercises Sky.Webview (macOS).
Categories: CLI (8), Sky.Tui (5), Sky.Live + Sky.Http.Server (13),
GUI (2 — Fyne + Sky.Webview), build-only fixtures (2), Sky.Webview
WebGL2 spike (1).

## Non-negotiables

### 0. Goal fidelity in autonomous loops — INVIOLABLE

When the user gives an autonomous mandate (`/loop AUTONOMOUS until
<goal>`, `/loop AUTONOMOUS <goal>`, or any equivalent multi-iteration
directive), the goal as the user worded it is the ONLY authority on
"done".

This rule applies to ALL autonomous mandates — current and future,
v0.17 / v0.18 / every compiler-cycle close, every product mandate,
every session resumed after compaction. It is **structural**: it
survives compaction, new sessions, and any redefinition I might
attempt under pressure.

#### The four hard rules

1. **The user's goal is captured VERBATIM** at mandate start and
   stored at `.claude/AUTONOMOUS_GOAL.md` in the project repo (so it
   survives clones, compactions, and new sessions). Subsequent
   iterations READ this file at entry and quote the goal back BEFORE
   doing anything else. If the file doesn't exist and an autonomous
   mandate is live, I reconstruct it from the user's most recent
   goal-setting message — using their words, not mine.

2. **I cannot declare "done".** Only an independent adversarial
   **Judge agent** spawned with a fresh context, given the verbatim
   goal, and verifying the ACTUAL claim (not a narrower lens I
   picked) can return "100% achieved". I MUST NOT scope, soften, or
   interpret the goal to fit what I shipped. Any "but/except/
   however/caveat/modulo/essentially/mostly" in the Judge's report
   → NOT done.

3. **Drift detection at every iteration.** Before each
   implementation step, I cross-check the planned step against the
   verbatim goal. If the step addresses a redefined / narrower /
   unrelated scope, I reset to the goal. Phrases that signal drift
   and are FORBIDDEN in any "complete" framing:
   - "criterion B OR clause", "load-bearing-but-pure", "documented
     as X" (when the goal said "deleted" / "removed" / "no impurity")
   - "shipped for the scope of [my chosen subtask]"
   - "iter N criteria all green" (when "iter N criteria" are MY
     definition, not the user's verbatim goal)
   - "deferred to Stage 6+", "spec backlog", "technical debt",
     "pre-existing", "out of scope for this iter"
   - "session boundary", "clean handoff"

4. **The only stop condition is a genuine implementation blocker.**
   I halt ONLY when I cannot proceed without user input (external
   auth wall, irreversible action requiring sign-off, ambiguous
   user-decision required). I describe the blocker concretely,
   await user direction, then CONTINUE the loop with their decision
   — I do NOT treat the blocker as "done".

#### The continuous-Judge loop protocol

```
iter_entry:
  1. Read .claude/AUTONOMOUS_GOAL.md (create from user's words if
     missing AND mandate is live)
  2. Quote the goal verbatim in a 1-line restate (drift gate)
  3. Spawn Judge agent (fresh context, see template below) — pass
     the verbatim goal + current branch SHA + read access to repo

  IF Judge says "100% ACHIEVED + VERIFIED":
    → PushNotification user with final outcome
    → Stop. Do NOT spawn another iteration.

  IF Judge says "NOT 100%":
    → Architect agent plans the closure of Judge's top gaps
    → Adversarial grillers attack the plan (>=2 in parallel)
    → Refine plan if grillers flag blocking concerns
    → Executor agents implement (parallel where independent)
    → SINGLE milestone verification at end of batch
    → Re-spawn Judge for re-verdict

  IF implementation blocker:
    → Document the blocker
    → PushNotification user describing what direction is needed
    → Wait for user response
    → On response: incorporate direction, resume the loop
```

#### Judge agent prompt template

> You are an INDEPENDENT adversarial Judge verifying whether the
> user-set goal has been 100% achieved on the Sky compiler at
> `/Users/anzel/works/playground/sky`, branch `<branch>` @ `<SHA>`.
>
> USER'S VERBATIM GOAL (read it from
> `.claude/AUTONOMOUS_GOAL.md`):
> ```
> <verbatim_goal_block>
> ```
>
> VERIFY the LITERAL claim, not a narrower interpretation.
> Examples of disqualifying findings:
>   * Goal says "100% fully typed e2e" → ANY `rt.Coerce` in
>     well-typed user code disqualifies. ANY `any` in emitted Go
>     for a fully-HM-typed expression disqualifies.
>   * Goal says "no runtime panics" → ANY panic class with a
>     known unfixed reproduction disqualifies.
>   * Goal says "if it compiles it works" → ANY Sky program that
>     passes `sky check` but fails `go build` OR panics at
>     runtime under well-typed semantics disqualifies.
>   * Goal says "rock solid + future-proof" → ANY architectural
>     band-aid, ANY "deferred to later" item, ANY known pending
>     compiler task in the umbrella scope disqualifies.
>
> Map every disqualifying finding to a concrete file:line +
> reproduction. List in priority order.
>
> Final verdict — EXACTLY one of:
>   * "VERDICT: 100% ACHIEVED + VERIFIED — <one-line proof>"
>   * "VERDICT: NOT ACHIEVED — <N> gaps; highest priority: <gap>"
>
> Forbidden in PASS verdict: "but", "except", "however",
> "caveat", "mostly", "essentially", "for the scope of", "modulo".

#### Workflow tool — auto-launch without re-asking

Once an autonomous mandate is live (`.claude/AUTONOMOUS_GOAL.md`
exists), `Workflow` tool invocations targeting that mandate run
WITHOUT a separate permission prompt to the user. The mandate
itself IS the durable permission. Re-prompting per workflow
invocation pauses progress and violates the continuous-Judge loop
protocol above. The user's edits to the goal file count as
ongoing direction; explicit instructions in chat take precedence
over default settings.

If the user has not granted a session-scoped allowance for
`Workflow` already, I add it once (settings.json) and proceed.
I do NOT pause iterations waiting for click-through approval.

### 0.1 Remote-push discipline — minimize CI noise

Local commits are checkpoints. Pushing to remote triggers CI for
every push. Constant per-commit pushes burn CI minutes, fail-spam
the branch status, and obscure real progress.

#### Rules

1. **Local commits are free; pushes are expensive.** Commit
   liberally to checkpoint progress on the feature branch. Only
   push to `origin` at meaningful milestones.

2. **A "meaningful milestone" is one of**:
   - A Judge agent verified phase boundary (e.g. "T1 leak class
     architecturally closed + verified")
   - An umbrella task closed (#383, #595, #644, #660, etc.)
   - A user-requested checkpoint (e.g. user said "push the
     current state")
   - A genuine blocker preventing further local work where the
     user needs to see what's pushed

3. **A new commit is NOT a milestone.** Neither is "all 3
   sequential gates green" if those gates verify only my narrow
   scope. Neither is "iter N shipped".

4. **Squash before push when sensible.** Many checkpoint commits
   at one milestone → squash to one well-described commit at push
   time. Preserve a tag/branch locally if I want to keep history.

5. **The user can override.** If they say "push now", push.

Forbidden patterns:
  * Pushing per /loop iteration just because gates went green.
  * Pushing a docstring fix as its own commit + push.
  * "I want CI to validate this" → that's what local gates are for.

### 0.2 Test-cadence discipline — no needless full-suite + wakeup cycles

The slow-progress pattern: edit → full cargo test suite → schedule
25-30 min wakeup → repeat. **This pattern is FORBIDDEN.**

#### Rules

1. **During implementation work, use the narrowest gate that
   proves the change is correct.** Targeted spec match (`--match
   "FooBar"`), single-example build, incremental build. Run these
   in seconds, not minutes.

2. **Full cabal test suite + full example sweep + verify scripts
   run ONLY at milestone boundaries.** A milestone is the same
   definition as 0.1 above. Not "I made a change". Not "I want to
   be safe".

3. **ScheduleWakeup is a SAFETY NET, not a pacing mechanism.** Its
   purpose is recovering from a genuinely stuck workflow / external
   event we cannot directly observe. It is NEVER used to "wait for
   cabal-test to finish" — `timeout N cabal-test` with `Bash`
   returns when done; no wakeup needed.

4. **Architecture + planning happen UP FRONT.** I do not edit
   code, run tests, edit again, run tests again — that's the
   debugging anti-pattern. I plan the full closure path with an
   architecture agent first, the executor agent(s) implement it
   coherently, THEN tests verify.

5. **Workflows over loop-of-edits.** When orchestrating multi-step
   work, use the `Workflow` tool (deterministic JS script that
   fans out agents). The Workflow runs to completion in one
   invocation. No ScheduleWakeup gaps between steps.

6. **Long-running test runs in the background.** When a full test
   suite IS warranted at a milestone, use `Bash run_in_background:
   true` so I am NOT blocked waiting. I do not ScheduleWakeup; the
   notification arrives when the test completes.

Forbidden patterns:
  * "Iter N shipped → run full suite → wake up in 30min for iter
    N+1" (a) iter N isn't a milestone, (b) full suite isn't
    justified, (c) wakeup wastes 30min.
  * "Wait for cabal-test for 25min via ScheduleWakeup" — use
    `run_in_background` instead.
  * Re-running the example sweep more than once per milestone.

Concrete cadence:
  * **Per change**: `cargo test -p <crate> <testname>` (narrowest
    crate + test filter that proves the change)
  * **Per phase boundary (multiple changes)**: rebuild + a couple
    of representative `cargo test -p <crate>` runs
  * **Per milestone**: full `cargo test --workspace` + the xtask
    gate suite (`cargo run -p xtask -- <gate>`) + example-sweep +
    verify-cli, in background, notified when complete

### 0.3 Architectural-mechanism citation — INVIOLABLE for compiler workflows

A compiler-level workflow that proposes closing a strategic goal via
a tactic MUST cite an architectural mechanism from the canonical
reference. Optimism without mechanism is forbidden in agent prompts
and judge verdicts.

#### The five hard rules

1. **Architecture reference is Phase 0.** All compiler-level
   workflows MUST begin by consulting `docs/rust-rewrite/` (the
   primary Rust-compiler architecture reference; the legacy
   `docs/architecture/sky-compiler-architecture.md` documents the
   retired Haskell pipeline and is kept for historical context)
   and, where stdlib semantics are touched,
   `docs/architecture/sky-stdlib-correctness.md`,
   before claiming a tactic closes a strategic goal. The first
   phase of every compiler workflow's JS DAG is
   `phase('Architecture-Consult')`. Tactics proposed without
   consulting the reference document are rejected at workflow
   entry.

   **Criterion #3 deletion-target wording (locked 2026-06-24).**
   Earlier framings of `.claude/AUTONOMOUS_GOAL.md` criterion #3
   read "`globalCgEnv` + `globalGoSigMap` IORefs DELETED". That
   wording UNDER-SPECIFIED the bridge IORefs (`scopeStateRef`)
   and the successor CAFs (`getCgEnvFromScope`, env-CAFs) that
   surfaced during the iter 17 / 37 / 42 / Class-A swap attempts.
   The locked wording is:

   > Criterion #3 = `{globalCgEnv, globalGoSigMap, scopeStateRef,
   > env-CAFs}` DELETED **AND** any residual IORef in `Compile.hs`
   > carries a machine-verified single-writer / single-reader
   > monotonic contract (see
   > `docs/v0.17-roadmap/phase-A-iter-0-anonrecords-contract.md`
   > for the `globalAnonRecords` precedent and the
   > `Sky.Build.AnonRecordWriterAuditSpec` verification gate).

   **This is NOT a relaxation** — it is a precise specification
   of the substantive purity guarantee. The original "DELETE"
   wording is satisfied by deleting the named IORefs; the
   "machine-verified contract" clause closes the loophole that
   would otherwise let an unnamed bridge IORef survive under a
   "load-bearing-but-pure" reframe (forbidden per §0 hard rule
   3). The contract has TWO parts:
   - Source-level contract docstring naming the writer site +
     reader sites + monotonic invariant (e.g. "register-on-
     first-mention; never overwrites; end-of-module barrier").
   - Spec gate (cabal-test) that builds a multi-module fixture
     and asserts the invariant programmatically — a write that
     overwrites OR reads a stale value MUST fail the gate.

   Any "close" claim against criterion #3 cites BOTH the named-
   IORef deletions AND the surviving-IORef contract+spec
   pair. Judge verdicts that PASS without the second citation
   are rejected.

2. **Tactical vs strategic feasibility.** Agents claim TACTICAL
   feasibility ("can I implement this change in N hours / one
   session?"). STRATEGIC feasibility ("does this tactic close the
   user goal?") is a USER-level decision taken AFTER the
   architecture reference is consulted and a mechanism is cited.
   An agent that conflates the two — claims "this closes the goal"
   without architectural citation — is wrong by construction.

3. **N-strikes circuit-breaker.** If 3 consecutive iterations fail
   to materially close the same criterion via the same lever, the
   next workflow MUST start with re-classification — NOT another
   attempt. Re-classification means: re-read the architecture
   reference, identify whether the criterion is in the irreducible
   floor (§8 of the reference), and escalate to the user with the
   floor citation. Continuing to retry the same lever past 3
   strikes is forbidden and counts as drift under §0 rule 3.

4. **Optimism-without-citation is forbidden.** Agent prompts must
   require, and judge verdicts must check, that any "close" claim
   names:
   - The Compile.hs / runtime / Solve site (with line citation)
   - The LowerCtx field, Solve reader, or runtime contract being
     consulted
   - The §6 origin category and §7 lever being activated
   A claim of "this closes rt.Coerce category X" without the §7
   lever name + the source-line citation is rejected. A judge
   that returns PASS without verifying the citations failed its
   adversarial duty.

5. **Floor-touching tactics need user authorisation.** Tactics
   that touch the irreducible floor (§8 of the reference — Go FFI
   return, gob/JSON wire decode, TEA reflect.MakeFunc dispatch)
   MUST escalate to the user before spending iterations.
   **AUTHORIZED 2026-06-23**: user has explicitly authorised
   floor-touching tactics for v0.17 close (literal-zero
   rt.Coerce via runtime rewrite — see
   `docs/v0.17-roadmap/literal-zero-close-plan.md`).

#### Workflow Phase-0 template (mandatory entry phase)

```js
phase('Architecture-Consult')
const archRef = await agent({
  prompt: `Read docs/architecture/sky-compiler-architecture.md.
For the proposed tactic <X>:
  1. Locate the §6 rt.Coerce origin category it would target.
  2. Identify the §7 architectural lever it would activate.
  3. Verify the lever is NOT in §8 (the irreducible floor) — OR
     confirm user-authorisation for floor-touching tactics is
     present.
  4. Cite the Compile.hs site (with line) + LowerCtx field /
     Solve reader / runtime contract being consulted.
If you cannot make all four citations, return cannotJustify=true
with a description of what's missing.`,
  schema: ARCH_REF_SCHEMA
})
if (archRef.cannotJustify) {
  return { halted: 'no architectural justification', missing: archRef.missing }
}
if (archRef.inFloor && !userAuthorizedFloor) {
  return { halted: 'tactic touches irreducible floor; user authorization required' }
}
// proceed to tactical phases
```

#### Forbidden patterns

* Agent prompts: "design and implement a fix for X" without
  requiring the architecture reference be consulted first.
* Judge verdicts: "VERDICT: 100% ACHIEVED" without listing the
  §6 categories closed + §7 levers activated + §8 floor sites
  documented.
* Workflows: skipping `phase('Architecture-Consult')` to "save
  time" — the architecture phase IS the time-saver because it
  short-circuits re-discovering the floor.
* Iteration N+1 after 3 consecutive failures on the same lever
  without re-classification.

#### Companion canonical references

- `docs/rust-rewrite/` — primary Rust-compiler architecture
  reference: pipeline (Parse → Canon → Type → Lower → Emit),
  workspace + crate layout, rt.Coerce origin catalog,
  architectural levers, irreducible floor, verbatim-goal verdict.
- `docs/architecture/sky-compiler-architecture.md` — legacy
  Haskell-pipeline reference, retained for historical context.
- `docs/architecture/sky-stdlib-correctness.md` — Sky.Core
  algebraic laws, Std.Ui layout invariants, Std.Html + Sky.Live
  TEA architecture, Std.Db + Std.Auth security invariants,
  cross-backend parity, per-module correctness verdicts.

These are the durable ground truth across sessions, agents, and
workflows. `docs/rust-rewrite/` is the FIRST source consulted on
any compiler change (stdlib-correctness for stdlib changes) — not
the in-memory model, not prior session context, not optimistic
"we can do it" framing.

### 0.4 Session methodology — phase pattern + agents + grilling + verify

The patterns below are the durable approach used on every non-trivial
work item. They are LOAD-BEARING — sessions that skipped them
historically produced cascade regressions (iter 17 / 37 / 42 /
Class-A swap attempts). Future sessions follow these by default.

#### Phase pattern (decide → plan → execute → verify)

1. **Decide what's IN scope** before doing any work. Write an explicit
   scope decision with rationale (what's in, what's deferred, what
   the success criterion is). Per CLAUDE.md §0 rule 1 — verbatim
   user goal is captured at `.claude/AUTONOMOUS_GOAL.md` for
   autonomous mandates.
2. **Plan** the execution as discrete additive phases. Each phase
   ships its own commit. Phase boundaries are checkpoints —
   verifiable, revertable, and shippable in isolation.
3. **Execute** one phase at a time. Per CLAUDE.md §0.2 — narrow
   gates per change, full sweep at milestone boundaries only.
4. **Verify** at every phase boundary. Per CLAUDE.md §0 — Judge
   agent verification at the close, fresh-context, adversarial.

This is the pattern that shipped v0.17.0 (typed-emit fix +
documented rt.Coerce surface + scopeStateRef contract + panic-class
gate locks) in additive phases with zero regression.

#### Agent + grilling pattern (for non-trivial work)

For any work where solo execution carries cascade risk
(Compile.hs surgery, multi-system changes, broad audits), the
DEFAULT pattern is:

1. **Architecture-Consult agent** (Phase 0) — fresh-context agent
   reads `docs/architecture/sky-compiler-architecture.md` +
   `docs/architecture/sky-stdlib-correctness.md`, cites §6
   rt.Coerce origin + §7 lever + §8 floor for the proposed
   tactic. Returns PROCEED / REVISE / ABORT.
2. **Adversarial grill** (Phase 0b) — the architecture proposal
   is grilled BEFORE implementation. Grill questions:
   - G1: Could this produce false negatives (gaps the regression
     wouldn't catch)?
   - G2: Could this produce false positives (over-eager
     rejection)?
   - G3: Estimated cost? Time budget bounded?
   - G4: Layering clean? Dependency direction correct?
   - G5: Does this close the criterion, or just document a partial
     close?
3. **Implement** with the grilled plan — phase boundaries
   commit + verify.
4. **Judge re-verify** at close — fresh-context Judge agent runs
   the actual verification commands, returns PASS / NOT ACHIEVED
   with concrete file:line citations.

Agent prompts include FORBIDDEN PHRASES in PASS verdicts
("but / except / however / caveat / mostly / essentially / for
the scope of / modulo"). These signals indicate the verdict is
drifting from the literal claim.

#### Three-leg soundness stool (for soundness claims)

A soundness claim ("no runtime panics from well-typed Sky") is
verified by THREE independent legs, not one:

1. **Runtime classification leg** — Go-side tests (e.g.
   `runtime-go/rt/panic_recover_test.go`) proving the panic
   surface is correctly classified.
2. **Emission-time leg** — Sky.Build specs (e.g.
   `Sky.Build.PanicClassGateSpec`) proving the lowering does NOT
   emit raw panic-prone Go ops AND the safety net is wired.
3. **Real-world e2e leg** — example sweep + verify-cli + Playwright
   + fuzzer (`Sky.Build.WellTypedFuzzerSpec` 10k iter) proving
   real-world + random programs do not panic.

A single-leg "proof" is NOT a proof; ship all three legs.

#### N-strikes circuit-breaker (per CLAUDE.md §0.2, reinforced)

3 consecutive failures on the same architectural lever (iter
17 / 37 / 42 / Class-A swap pattern) FORBIDS a 4th attempt without
re-classification. Re-classification means:

1. Re-read `docs/architecture/sky-compiler-architecture.md`
   §6/§7/§8.
2. Identify whether the criterion sits in the irreducible floor
   (§8). If yes — escalate to user with the floor citation.
3. Author a postmortem of what the 3 prior attempts missed.
4. Get explicit user authorization for the 4th attempt with the
   postmortem cited.

Without re-classification, a 4th attempt counts as drift per CLAUDE.md
§0 rule 3 and the session is forfeit.

#### Reframed vs literal goal handling

When the user reframes a goal mid-mandate (e.g. v0.17 "100% fully
typed" → "rock solid + ~100% sound with documented surface"):

- The verbatim goal at `.claude/AUTONOMOUS_GOAL.md` REMAINS the
  literal anchor. Don't overwrite it without user direction.
- The reframe is a SHIPPING SCOPE decision, not a goal change.
  Both readings must be verified at close — Judge returns
  separate LITERAL and REFRAMED verdicts. The reframe says which
  is required for the release; the literal verdict tracks
  long-term progress.
- Per CLAUDE.md §0 rule 3 — phrases like "for the scope of",
  "shipped under the reframe", "essentially closed" in a literal
  verdict are forbidden. Be precise about which goal a closure
  satisfies.

#### Push discipline (per CLAUDE.md §0.1, reinforced)

Local commits are checkpoints; remote pushes are CI invocations.
The right cadence is BATCH at milestones, not per-commit. A
milestone is one of:

- A Judge-verified phase boundary
- An umbrella task closed
- A user-requested checkpoint
- A genuine blocker requiring CI cross-platform verification

Per-commit pushes burn CI minutes, fail-spam branch status, and
obscure real progress. The pattern that worked: ship 3-5
related commits LOCALLY, run a milestone Judge verification,
then push once. v0.17.0 closure shipped this way (5 commits in
2 pushes vs the 6 individual pushes that preceded the
correction).

#### Context discipline (the underlying constraint)

Per `docs/session-protocol.md` (folded here): Claude has a
finite context window. On this codebase, two patterns burn it
fast: (1) task-list reminders compound after every Bash call,
(2) reactive grep-read-edit cycles produce dozens of small
operations.

Mitigations:

- **Read with `offset` + `limit`**, never naked Read on files >
  1000 lines. For `Compile.hs` (23k lines), know the line number
  before reading.
- **Delegate exploration to agents.** Audit-style questions go
  to `Explore` subagents whose context isolates the burn.
- **Batch Bash calls** when independent. Chained `&&` beats
  three separate invocations.
- **Scripts over individual invocations.** `scripts/cabal-test.sh`
  encapsulates timeouts + resource guards; raw `cabal test` does
  not.
- **Don't use TaskCreate / TaskUpdate / TaskGet** unless the user
  explicitly asks. The task list (~400 entries deep) is appended
  after every Bash call as a reminder; cleanup costs more than
  skipping it. Plan + execute via phase boundary commits instead.

#### Stop conditions and honesty

- **Bounded surface = bounded session.** Touching >5 files or
  >200 lines without a clear delegation strategy is a flag —
  delegate to a focused agent or stop and checkpoint.
- **"I can't finish this in this session" is a valid outcome**
  with a checkpoint file (e.g. `docs/v0.17/session-N-checkpoint.md`).
  "I'll keep trying" without a path forward is not.
- **3 attempts on the same approach → halt and reclassify**, not
  a 4th retry. Per N-strikes above.

### 1. Memory safety — `scripts/mem-guard.sh` MUST run during dev

A runaway `sky` / `cargo` / `rustc` / `rust-analyzer` process
has previously force-powered-off the host Mac. Treat the absence of
mem-guard like a missing `set -e`.

```bash
nohup ./scripts/mem-guard.sh > /tmp/mem-guard.out 2>&1 &
disown                                # survives shell exit
```

Defaults (16 GB Mac): per-process kill at 6 GB RSS for compiler
tooling (`sky` / `cargo` / `rustc` / `rust-analyzer` / `cc1` /
`ld` / `go` / `gopls` / `sky-ffi-inspect`; legacy `cabal` / `ghc`
/ `ghc-iserv` / `haskell-language-server` still covered); 10 GB
panic tier for the dev-session host
(`claude` / `node` / `ghostty`); system-pressure floor kicks in
when free + inactive + speculative memory drops below 1.2 GB. Tune
via `MEM_GUARD_PROC_MB` / `MEM_GUARD_PANIC_MB` /
`MEM_GUARD_SYS_FLOOR_MB`. `MEM_GUARD_DRY=1` runs in log-only mode.
Never silence a kill by raising the threshold — the kill means the
process was on a path to OOM the machine. Fix the underlying
compiler bug.

### 2. Background-task hygiene — clean up before declaring "done"

Long sessions accumulate orphan `run_in_background` zsh wait-loops
that eventually exhaust the per-uid process table
(`fork: retry: Resource temporarily unavailable`). When that
happens `mem-guard.sh` silently dies and the user's binaries get
killed instantly on launch.

End-of-mission checklist:

```bash
# Orphan polling loops
ps -u $USER -o pid,command | awk '/while pgrep|until ! pgrep/ && /\/bin\/zsh -c/ {print $1}' | xargs -n1 kill -9 2>/dev/null

# Stray sleeps + verification leftovers
ps -u $USER -o pid,ppid,command | awk '$3 == "sleep" && $2 != 1 {print $1}' | xargs -n1 kill -9 2>/dev/null
pkill -f "playwright"; pkill -f "chromium"
pkill -f "examples/.*/sky-out/app"

# mem-guard alive?
pgrep -f mem-guard.sh >/dev/null || (nohup ./scripts/mem-guard.sh > /tmp/mem-guard.out 2>&1 & disown)
```

**Prefer the Monitor tool** over `run_in_background` + polling.
Monitor delivers events without leaving a wait-loop subprocess.

### 3. Test / build timeout gate — every long-running command MUST be timeout-bounded

A test or build that hangs forever is a silent task waster. We
have already lost 7 hours waiting on a stuck `Sky.Cli.Watch`
subprocess. Never again.

Rules:
- **`cargo test` and the xtask gate suite MUST run under
  `timeout`**: `timeout 3600 cargo test --workspace` /
  `timeout 3600 cargo run -p xtask -- <gate>` (60 min hard
  ceiling). If 60 min is not enough, that's a flaky test —
  bisect it, don't widen the ceiling.
- **Per-spec timeouts.** Hspec specs that exec subprocesses
  (`sky build` / `sky watch` / `sky test`) MUST wrap the child in
  `timeout 60` or use Hspec's `Test.Hspec.Wai`-style timeout
  combinators. A test that doesn't time out cannot be re-run.
- **Example sweep already enforces this** via
  `run_with_timeout 10` in `scripts/example-sweep.sh`. Don't
  remove or widen those calls without a real reason.
- **Background `run_in_background` shell commands** that wait
  on a process MUST `kill -KILL` it after a finite wait —
  default 600 s ceiling. Never `wait $PID` unbounded.
- **Monitors** in dev-loop tooling (sky watch, sky doctor)
  watching for state changes MUST have a heartbeat / max-wait
  so a wedged child doesn't poison the parent.

If you see a process running > 30 min that you can't justify,
kill it and file a bug. Never wait it out.

### 4. No-deferral principle — every known bug enters the pipeline

> **Reinforced 2026-06-07** — Sky Lang aspires to be the industrial
> best-in-class language + toolchain for fullstack pure-functional
> development (UI/UX/DX/security/scalability + extensive stdlib).
> SkyDeploy is the leading moat — the AI-era platform on which
> startups and businesses build. Both reputations compound. A
> "known broken edge case" today is a tomorrow's compounding
> problem. The session's value is architectural progress, not the
> tag — when the correct fix takes multi-session/days/weeks, that
> is a reason to start, not a reason to defer. **Default response
> to a hard problem: analyse root cause → research the
> architecturally correct approach (existing roadmap docs / RFCs
> / improvement plans) → execute, even when it requires multiple
> sessions.** The tempting tactical workaround is forbidden
> unless the user explicitly accepts the trade-off after hearing
> it.

If a bug surfaces during dev, sweep, CI, or testing — **whether
introduced by your current work or pre-existing** — it MUST
enter the task pipeline immediately and be fixed in the next
appropriate patch release. The phrases "pre-existing flake",
"defer to v0.X", "known issue, ignore" are forbidden as
shipping excuses.

Rules:
- **Spotted = filed.** Any test failure / sweep failure /
  runtime panic / log error you observe gets a task created on
  the spot. No mental "I'll look at it later".
- **Pipeline groups related fixes.** Bundle related bug fixes
  into the next patch release (v0.15.x) to reduce notification
  noise — don't tag per fix.
- **Closing tasks requires actual fix, not workaround.** A
  documented workaround in CLAUDE.md is acceptable as a TEMPORARY
  bridge while the actual fix is in flight, NOT as a permanent
  resolution.
- **"Pre-existing" is investigation context, not a verdict.**
  When a failure pre-dates your work it tells you the bug is
  older and the fix can ship in its own commit (not bundled
  with your unrelated work), but it does NOT excuse skipping
  the fix.

The user has the right to interrupt with "ship this without
fixing X" — only that explicit override allows shipping with a
known unfixed issue. Default is fix-first.

### 5. SkyDeploy redeploy follows every Sky release

Every Sky compiler / stdlib release that's been tagged (`vX.Y.Z`)
MUST be paired with a SkyDeploy redeploy of the matching version:

```bash
cd ~/works/playground/skydeploy
# 1. Bump SKY_VERSION in all 5 refs:
#    - sky-tools/Dockerfile
#    - deploy/Dockerfile
#    - agent-service/Dockerfile
#    - build-image/Dockerfile
#    - control-plane/deploy/setup-remote.sh
# 2. Commit + push origin main.
# 3. Bounded redeploy:
timeout 1200 bash control-plane/deploy/deploy.sh
```

**Graceful degradation on auth failure.** If `gcloud` auth has
expired (token revoked, refresh needed, SSO challenge required)
or any other deploy-side blocker fires, do NOT retry indefinitely:

1. Detect via the bounded `timeout`'s exit code OR `gcloud auth`
   complaints in stderr.
2. **Park the redeploy.** The bump commit on skydeploy `main` is
   already pushed — that's the durable artifact.
3. **Warn the user explicitly**: "SkyDeploy redeploy parked due to
   `<reason>` — please `gcloud auth login` and re-run
   `control-plane/deploy/deploy.sh` when convenient. Sky compiler
   work continuing." Include the exact gcloud command they need.
4. **Continue Sky compiler/stdlib work** without blocking on the
   deploy.

The deploy is downstream consumption of the release; the release
itself is the authoritative artifact (tag + GitHub release). Sky's
flow does not block on operational state outside the compiler
repo.

### 6. Disk hygiene — unused build caches MUST be pruned

The Go toolchain on macOS does NOT auto-prune its build cache. In
one session of heavy Sky compilation + example sweeps + agent
worktrees, `~/Library/Caches/go-build` grew to 202 GB and pushed a
927 GB disk to 100% full. This blocks every subsequent build /
test / agent task.

End-of-mission checklist (run BEFORE declaring a release shipped
when a sweep has run):

```bash
# 1. Worktrees from finished agents — wipe the directories
#    after the cherry-pick is on main. Each carries a full
#    .skycache + sky-out ≈ 1.5 GB.
for wt in $(ls .claude/worktrees/ 2>/dev/null); do
    # Skip the one currently running an agent; check via TaskList
    # before bulk-removing.
    : keep-if-active
done
rm -rf .claude/worktrees/agent-<sha-of-completed-agent>

# 2. Tell git about it
git worktree prune --verbose

# 3. Go build cache — safe; rebuilds on next `go build`. Reclaims
#    multi-GB after a sweep; multi-tens-of-GB after multiple
#    sweeps.
go clean -cache

# 4. /tmp leftovers — sweep logs + deploy artifacts.
rm -f /tmp/sky-build-*.log /tmp/cabal-*.log /tmp/skydeploy-cp-linux /tmp/skydeploy-*.log

# 5. Sanity check
df -h /
```

NOT to do without explicit user ask:

- `go clean -modcache` (`~/go/pkg/mod`) — deletes ~50–70 GB but
  every project re-downloads modules on next build (slow + needs
  network).
- `rm -rf dist-newstyle/` — cabal full rebuild ≈ 5 min.
- Wiping `.skycache/ffi/` in `examples/13-skyshop/` — 15+ min of
  Stripe SDK introspection on next sweep.

**Automatic hygiene** (added 2026-06-03 PR13). `scripts/build.sh` AND
`scripts/example-sweep.sh` end with a 5-GB-threshold check on
`~/Library/Caches/go-build`; over-threshold triggers `go clean -cache`
automatically. So after any compiler rebuild or example sweep the
cache caps at ~5 GB before the next operation can re-bloat it. Periodic
manual hygiene is no longer required for normal workflows. The recipe
in `## Disk hygiene` above is still the right escape hatch when you
need to reclaim aggressively (e.g., before spawning many agents).
Worktree dir cleanup after EVERY agent cherry-pick remains manual.

When the host shows < 5 GB free, ABORT the next agent spawn until
cleanup completes — an agent that runs into ENOSPC mid-build leaves
half-written artifacts that are harder to recover than a clean
build.

### 7. Core principles

1. **If it compiles, it works.** Every known runtime panic class
   has a regression test in `runtime-go/rt/*_test.go` or
   `test/Sky/**Spec.hs`. Defence in depth (panic recovery +
   `Err`-return at Task boundaries) is the floor, not the
   foundation.
2. **Dev experience first.** Clear errors, predictable behaviour,
   no user-written FFI.
3. **Root-cause fixes only.** Never suppress type errors or
   warnings. A defensive cover-up that hides a contract violation
   IS a violation.
4. **Production-grade architecture.** Scales to the Stripe SDK
   (76k FFI symbols). Stays maintainable.
5. **AI-written Sky code defaults to Std.Ui + Std.Auth + Std.Db.**
   Each is reviewed for security + scalability — UI/UX/DX/security
   are not afterthoughts.

### 8. Non-regression rules (enforced by `cargo test`)

- **No `Result String a` / `Task String a`** in public surfaces.
  Use `Result Error a` / `Task Error a`.
- **No `Std.IoError`, no `RemoteData`** — both deleted pre-v1.
- **No runtime panic from well-typed Sky code.**
- **No silent numeric coercion** — `AsIntChecked` is the fallible
  variant; `OrZero` suffix marks display-only lenient helpers.
- **No raw `.(T)` assertions on any-typed thunks** — route via
  `rt.Coerce[T]`.
- **Record field enumeration sorts by `_fieldIndex`** before any
  emission that depends on field order.
- **Secrets are typed** — `Auth.signToken` / `verifyToken` take
  `String`, not `any`. `fmt.Sprintf("%v", secret)` is forbidden.
- **`sky check` ≡ `sky build`** — both invoke `go build` on the
  emitted Go.
- **New AST nodes require explicit walker arms** in
  `Canonicalise/{Expression,Pattern,Type}.hs`,
  `Type/Constrain/{Expression,Pattern}.hs`,
  `Type/Exhaustiveness.hs`, `Format/Format.hs`, `Build/Compile.hs`,
  and the LSP's `exprTokens` / `exprIdents` / `exprAllRefs` /
  `refsInExpr` / `collectSemTokens` / `collectReferences`. Don't
  rely on `_ -> []` catchalls.

### 9. Testing rules

- **Every new feature / bug becomes a regression test** before the
  fix lands. The failing test is the discovery artefact.
- **Cabal specs** for compile-time behaviour;
  `runtime-go/rt/*_test.go` for runtime helpers;
  `tests/**/*Test.sky` for stdlib semantics; `sky test <file>` is
  the user-facing runner.
- **Runtime verification on every push.** `sky verify` builds AND
  runs each example; `scripts/verify-all-web.sh` drives the
  Sky.Live + Sky.Http.Server scenarios through Playwright;
  `scripts/verify-cli.sh` covers CLI / Sky.Cli / Sky.Tui apps.
  `--build-only` doesn't catch the "click is a no-op" class of
  regression.

## Effect boundary — Task-everywhere (v0.10.0+)

Single rule: **every observable side effect returns `Task Error a`.**

| Tier | Type | Examples |
|---|---|---|
| Pure | bare `a` | `String.length`, `List.map`, `Crypto.sha256`, `Encoding.base64Encode`, `Time.timeString`, `System.getenvOr` |
| Fallible-pure | `Result e a` / `Maybe a` | `String.toInt`, JSON decoders, `Encoding.base64Decode`, `Auth.hashPassword` |
| Effects | `Task Error a` | `File.*`, `Http.*`, `Process.run`, `Io.*`, `Db.*`, `Auth.{register, login, setRole}`, `Crypto.{randomBytes, randomToken}`, `Time.{sleep, now, unixMillis}`, `Random.*`, `Log.*`, `System.*` (except `getenvOr`) |
| Diverging | `Int -> a` | `System.exit` (polymorphic return — never comes back) |

**Default-supplied helpers stay bare.** `System.getenvOr key def : String`,
`Maybe.withDefault`, `Result.withDefault`, `Db.getString`/`getInt`/`getBool`
— the default plugs the failure case at the call site.

**Auto-force `let _ = TaskExpr`.** The lowerer wraps the discarded
expression in `rt.AnyTaskRun` so the side effect fires:

```elm
let
    _ = println "step 1"             -- auto-forced
    _ = Log.infoWith "saving" [...]  -- auto-forced
in
    continue
```

Top-level module bindings of Task-typed values still require explicit
`Task.run`:

```elm
apiKey =
    System.getenv "OPENAI_KEY" |> Task.run |> Result.withDefault ""
```

**Result/Task bridges:**

| Helper | Type |
|---|---|
| `Task.fromResult` | `Result e a -> Task e a` |
| `Task.andThenResult` | `(a -> Result e b) -> Task e a -> Task e b` |
| `Result.andThenTask` | `(a -> Task e b) -> Result e a -> Task e b` |
| `Task.mapError` | `(e -> e2) -> Task e a -> Task e2 a` |
| `Task.onError` | `(e -> Task e2 a) -> Task e a -> Task e2 a` |

No `Result.fromTask` exists by design — keep effectful pipelines in
Task; the runtime entry boundary (CLI `main`, `Cmd.perform`, HTTP
handler return) is what executes them.

**Two-level error pattern** (`07-todo-cli` + `18-job-queue`):

1. `errId = Crypto.randomToken 4` — short correlation ID
2. `Log.errorWith op [ "errId", errId, "error", Error.toString e ]` — server-side structured log
3. `Task.fail (Error.unexpected ("Operation failed (ref " ++ errId ++ ")"))` — user-facing message

Per app shape: CLI → `Task.run … |> Task.onError reportError`;
Sky.Http.Server → `Task.onError` recovers to a 4xx/5xx Response;
Sky.Live → `Cmd.perform task ResultMsg`, dispatch updates
`notification` / `historyError` in Model.

## Type-directed lowering (v0.15.x)

The compiler now propagates HM types through to the Go IR. Sub-
expressions at lambda bodies, record-field inits, list elements,
and call args lower with the slot's typed Go form. This closes
the long-standing parametric-record-alias bug class (every Surface
1/2/3 is shipped — see `docs/v1-rfc/type-soundness-deep-analysis.md`
for the full architecture write-up).

### Mechanics

- **Solver writes per-region types.** `Sky.Type.Solve` carries a
  `RegionTypes :: Map A.Region T.Type` IORef alongside the existing
  state. After unification settles, every constrained region has a
  concrete type readable from `globalRegionTypes` during lowering.
- **`LowerCtx`** carries the optional "expected type for this
  position" down through `exprToGoExpectGo`. When a slot has a
  known typed shape (record-field, call-arg, list-element), the
  child expression sees it and lowers with that shape.
- **`coerceToFieldType`** elides redundant `rt.Coerce` wraps when
  the emitted Go expression's static type already matches the
  target (Stage D — saves both runtime work and codegen noise).

### Go generics on parametric record aliases

A `type alias Cfg msg = { onSubmit : msg, label : String, ... }`
emits:

```go
type Cfg_R[T1 any] struct {
    OnSubmit T1
    Label    string
    ...
}
```

Per-instance instantiations carry their type args explicitly:
`Cfg_R[Msg]`, `Cfg_R[Int]`, etc. This means callback fields keep
their typed callee parameter (no `func(any) any` fallback), and
cross-alias passing now works without the alias-chain workaround.

Subset-record cases (a function uses only some fields) synthesise
`_skysynth_<alias>_<var>` TVars so the alias's missing parameters
still flow as Go T-vars through the inferred sig.

### Same-module polymorphic re-instantiation

Sibling references to **polymorphic** annotated TypedDefs in the
same module now emit `CForeign` and alpha-rename per call site —
so `f : Cfg msg -> msg` called with `msg=Int` AND `msg=Bool` in
the same module both work. Non-polymorphic / wildcard-only sigs
still use `CLocal` (shared env var); identity-based unification
on nominal aliases needs the shared path, and wildcard-`any`
binding needs the body ↔ caller UF var chain to keep soundness.

### Wildcard-`any` soundness gate

`Sky.Canonicalise.Type.freeTypeVars` collects EVERY type-variable
name including `"any"`. `Instantiate.fromAnnotation` then filters
`"any"` out and `buildEnv` gives each occurrence its own fresh UF
var — that pair is load-bearing for `any`'s wildcard semantics.
Any new gate on "is this annotation polymorphic?" MUST check
`any (/= "any") freeVars`, not `not (null freeVars)`. Mis-gating
would treat wildcard-only sigs as polymorphic, diverge body ↔
caller UF vars under fresh-per-call-site re-instantiation, and
silently accept wrong return types.

## Go reserved-name rewriting

Sky compiles to Go but Sky's identifier rules are stricter than
Go's (Sky banishes keywords at parse time; Go *tolerates* shadowing
predeclared types like `string` / `error`). To keep emitted Go safe
and free from accidental-shadow gotchas, every Sky identifier in
`reservedGoNames` (`src/Sky/Build/Compile.hs:4058`) is rewritten
at codegen with a trailing `_`.

```
init → init_       (Go's func init() is auto-called at package load)
string → string_   (avoid shadowing Go's predeclared type)
error → error_     (avoid shadowing Go's predeclared interface)
for → for_         (Go syntactic keyword)
true → true_       (Go predeclared constant)
```

The list covers four tiers:

1. `init` — special-cased with a code comment; load-bearing for
   Sky.Live + Sky.Webview's `init = …` TEA convention.
2. **Predeclared funcs** — `new`, `make`, `len`, `cap`, `copy`,
   `append`, `delete`, `panic`, `recover`, `print`, `println`,
   `clear`, `min`, `max`, `complex`, `imag`, `real`, `close`.
3. **Reserved keywords** — all 23 Go keywords (`for`, `case`,
   `type`, `func`, …). `if`/`else`/`nil` not in list because the
   Sky parser rejects them as identifiers first.
4. **Predeclared types + constants** — `bool`, `byte`, `rune`,
   `string`, `error`, `any`, `comparable`, every `int*`/`uint*`/
   `float*`/`complex*` size, `true`, `false`, `iota`, `nil`.

**Rule for AI-written Sky code.** `init = init` is safe (LHS is a
record-field key → Go field `Init`; RHS is a binding ref → Go
identifier `init_`). Same for `view = view`, `update = update`,
etc. — every TEA app uses this idiom and it lowers correctly.

**Special-cased outside the list.** `main` is the program entry —
the Sky binding `main` in `module Main exposing (main)` emits as
Go's `func main()` (the program entry), not as `main_`. A
user-named binding `main` in any other module would module-prefix
to `Mod_main` and never collide.

**Module-prefix safety net.** Every top-level Sky binding becomes
`<Mod>_<name>` in Go (`Main_view`, `Std_Ui_layout`). So the
reserved list only matters for locals + parameters within
functions. The audit gate before adding any new entry: grep
`examples/*/sky-out/main.go` for the bare identifier outside any
`Mod_…` token — if there are no hits, the patch is purely
future-proofing.

## Memory safety + efficiency audit (v0.15.x)

### Stdlib stack behaviour

| Tier | Functions | Status |
|---|---|---|
| O(1) pure Sky | `head`, `tail`, `cons`, `isEmpty`, `Maybe.withDefault`, `Maybe.map`/`andThen`/`isJust`/`isNothing`/`map2-5`/`andMap`, `Result.withDefault`/`map`/`andThen`/`mapError`/`map2-5`/`andMap` | Stack-safe always |
| Tail-recursive (auto-TCO) | `foldl`, `find`, `any`, `all`, `member`, `drop`, `reverseHelp`, `indexedMapHelp`, `length`, `range`, `zip`, `concatMap`, `indexedMap` | Compiles to `for { ... continue }`; constant stack. v0.17 added `length`/`range`/`zip`/`concatMap`/`indexedMap` (CPS / accumulator rewrites — Limitation #8 closed). |
| CPS-shipped (v0.17) | `map`, `filter`, `foldr`, `concat`, `take`, `append`, `Maybe.combine`, `Result.combine` | Rewritten to CPS / accumulator form; constant stack via tail-recursive helper. Together with the auto-TCO tier, all 13/13 list ops in scope run on constant Go stack. |

**TCO mechanism.** The lowerer detects tail-position self-calls in
`Can.Case` / `Can.If` / `Can.Let` bodies via
`Sky.Build.TailCallOpt.isTailRecursive`. Tail-recursive function
bodies emit as `[GoForever <stmts>]` where each tail call becomes
`<param reassignment + coercion>; continue` and every other tail
position emits `return <coerceReturnExprT goRetType expr>`. Func-typed
params skip `coerceArg` (its `eraseTypeParams` would rewrite
`T1 → any`); other params route through the existing coercion path.

### Runtime hot paths

- **FFI boundary** — every `Ffi.callTask` / `Ffi.callPure` is wrapped
  in `runWithRecover` (panic → `Err`).
- **`rt.SkyCall`** — reflect.MakeFunc per HOF call site, ~100 ns per
  element. Bounded.
- **`rt.AsList` / `rt.AsListT[T]`** — bounded slice cast / per-element
  coercion.
- **`rt.Coerce[T]`** — type assertion fast-path; reflect-backed
  map→struct narrowing when needed (closes the Db.query → typed-record
  panic class).
- **TEA dispatch** — `SkyTuple2` fast-path before reflect fallback
  (~40 % faster on Apple M1).

### Session store bounds

| Store | Bounded by |
|---|---|
| `memory` | `sync.Map` + TTL cleanup goroutine; user count × session size |
| `sqlite` | Disk + connection pool |
| `redis` / `postgres` | External service config |
| `firestore` | GCP quota |

### Compile-time + runtime memory protections

- `[live] maxBodyBytes` (default 5 MiB) — POST cap on
  `/_sky/event` (raise for file uploads).
- `SKY_LIVE_QUEUE_MAX` (default 50) — POST retry queue cap.
- **HM solver budget** — `SKY_SOLVER_BUDGET` (default
  `max(5,000,000, constraint_count × 200)`). Caps `solveHelp`
  invocations per `solve` call; trips with a clear error rather
  than letting unbounded heap consumption OOM the host.
- **DCE** — whole-program Sky-side dead-code elimination prunes
  unreachable defs + FFI bindings before lowering.

### Synchronous-panic gate (v0.15.43)

Every emitted `func main()` starts with
`defer rt.LogPanicAndExit()`. The deferred call's `recover()`
catches whatever escaped the synchronous Sky path (Sky.Cli /
Sky.Tui / batch jobs — every `main = Task.run …` shape that's
not a server), classifies the panic (DivisionByZero,
TypeMismatch, CoerceFailure, ComparisonMismatch, IndexOutOfRange,
NilDereference, CompilerBug, Unexpected), emits a structured
Error log line with a 4-byte errId, and exits 1 — instead of
dumping a Go stack. `SKY_LOG_FORMAT=json` honours the JSON shape.

Reachable-from-Sky panic sites: `rt.IntDiv` / `rt.Rem` / `rt.Div`
(div-by-zero), `rt.AsInt` / `AsFloat` / `AsBool` (heterogeneous
slice / untyped FFI return), `rt.cmp`, `rt.Coerce` (3 variants),
`rt.skyCallDirect`, plus Go-runtime `index out of range` /
nil-deref. Compiler-bug-contract panic sites: `coerceInner`,
`Unreachable`, `Ffi.kernel` — surface as `CompilerBug` with a
"please report" hint. Full site audit:
`docs/v0.15.x-hardening/audits/CYCLE-06-PC-panic-site-audit.md`.

Sky.Http.Server handlers already have a per-request defer/recover
(at `rt.go:6863`) — they emit a 500 instead of crashing. The
Cmd.perform goroutine wraps `rt.SafeGo`. The top-level recover
closes the remaining synchronous surface.

## Build & test

```bash
sky init [name]                    # new project
sky build src/Main.sky             # compile → sky-out/app
sky run src/Main.sky               # build + run
sky watch src/Main.sky             # file-watch rebuild + restart
sky check src/Main.sky             # type-check + go build
sky fmt src/Main.sky               # opinionated formatter
sky test tests/MyTest.sky          # Sky.Test runner
sky db status                      # Std.Db migrations: applied / pending / drift
sky db migrate                     # apply pending Std.Db migrations, then exit
sky doc Module                     # terminal docs
sky doc --serve [--port 8080]      # browsable HTTP doc server (auto-opens browser)
sky doc --tui                      # interactive terminal doc browser (Sky.Tui)
sky doc --list                     # list every documented module
sky doctor [--fix] [--verbose]     # project / environment health checks
sky console [--port 8025]          # standalone Std.Ui Sky Console
sky console --tui                  # same source, Sky.Tui backend
sky add github.com/some/package    # add Go FFI binding
sky remove <package>
sky install                        # regen missing FFI + go.mod deps
sky update                         # update deps
sky upgrade                        # self-upgrade binary
sky upgrade-claude                 # refresh ./CLAUDE.md from binary's embedded template
sky clean                          # remove sky-out/ dist/
sky lsp                            # JSON-RPC LSP server (stdio)
sky --version                      # `sky dev` on local builds; CI injects release version
```

**Never run `sky build` from the repo root** — overwrites the
compiler binary in `sky-out/`. Always `cd` into the example dir
first:

```bash
cd examples/01-hello-world && sky build src/Main.sky
```

### `sky watch` rules

- Watched scope (strict allowlist, no `.skywatchignore`): `sky.toml`
  + the entry-point's directory (recursive `.sky` walk) + `tests/`
  if present. Generated dirs (`sky-out/`, `.skycache/`, `.skydeps/`,
  `rust/target/`, `dist-newstyle/`, `node_modules/`, `.git/`) excluded.
- Build-error policy: on a failing rebuild, the previously-running
  binary stays alive. The next successful build kills + respawns.
- Caches: `.skycache/source.hash` (full short-circuit on
  unchanged source), `.skycache/lowered/` (per-module IR),
  `.skycache/ffi/*.skyi` (HM types — never regenerated; explicit
  `sky add/install` step). Typical warm rebuild: 1-3 s.

### Release checklist (non-negotiable)

1. Rebuild: `( cd rust && cargo build --release -p sky ) && cp rust/target/release/sky sky-out/sky`
2. Smoke-test: `sky-out/sky --version` (must print version, not start a server)
3. Test sweep: `cargo test --workspace` + the xtask gate suite (`cargo run -p xtask -- <gate>` for each of roundtrip / resolve / infer / reject / fuzz / coerce-floor / repro / build-run / golden) — zero failures
4. Clean-build every example: loop over `examples/*/`, `rm -rf sky-out .skycache .skydeps`, `sky build src/Main.sky`
5. **Sky.Live + Sky.Http.Server runtime verify** — `scripts/verify-all-web.sh` (Playwright + the structural-events + round-trip-dispatch checks)
6. **CLI / Tui / Cli runtime verify** — `scripts/verify-cli.sh`
7. **`sky check`** on the largest example: `cd examples/12-skyvote && sky check`
8. **From-scratch flow** — `sky init mytest` in a temp dir, `sky build && sky run`, `sky add fmt`, `sky remove fmt`, `sky upgrade`
9. **CI parity** — `.github/workflows/ci.yml` matches the local verify scripts

If step 5 or 6 fails, fix root cause then re-run from step 1. Never
tag with a known runtime failure.

## Environment variables

Configuration precedence: **process env > `.env` > `sky.toml`**.

### Sky.Live (`[live]` section)

| Env var | sky.toml key | Default |
|---|---|---|
| `SKY_LIVE_PORT` | `port` | 8000 |
| `SKY_LIVE_TTL` | `ttl` | `30m` |
| `SKY_LIVE_STORE` | `store` | `memory` (memory \| sqlite \| redis \| postgres \| firestore) |
| `SKY_LIVE_STORE_PATH` | `storePath` | `DATABASE_URL` / `REDIS_URL` fallback |
| `SKY_LIVE_STATIC_DIR` | `static` | none |
| `SKY_LIVE_INPUT` | `input` | none |
| `SKY_LIVE_MAX_BODY_BYTES` | `maxBodyBytes` | 5242880 (5 MiB) |
| `SKY_LIVE_BANNER` | — | `on` (off / 0 / false to disable) |
| `SKY_LIVE_RETRY_BASE_MS` | — | 500 |
| `SKY_LIVE_RETRY_MAX_MS` | — | 16000 |
| `SKY_LIVE_RETRY_MAX_ATTEMPTS` | — | 10 |
| `SKY_LIVE_QUEUE_MAX` | — | 50 |
| `SKY_LIVE_HELLO_TIMEOUT_MS` | — | 8000 |
| `SKY_LIVE_HEARTBEAT_TTL_MS` | — | 35000 |
| `SKY_LIVE_SSE_BUFFER` | — | 16 (clamped to [1, 1024]; drops surfaced as `sky_live_sse_drops_total{session}`) |
| `SKY_LIVE_BASE_PATH` | — | (set by `MountSubApp`) |

### Logging (`[log]` section)

| Env | sky.toml | Values |
|---|---|---|
| `SKY_LOG_FORMAT` | `format` | `plain` (default) \| `json` |
| `SKY_LOG_LEVEL` | `level` | `debug` \| `info` (default) \| `warn` \| `error` |

### Auth, Console, Production gate

```dotenv
ENV=production              # gates dev console + banner OFF + /_sky/metrics behind auth
SKY_AUTH_TOKEN_SECRET=…     # ≥32 bytes — Sky errors at startup if shorter
SKY_AUTH_TOKEN_TTL=24h
SKY_AUTH_COOKIE=sky_sid

SKY_CONSOLE_EMBED=on        # off/0/false suppresses dev console mount
SKY_DEV_BANNER=on           # off suppresses the floating link (keeps mount)
SKY_CONSOLE_URL=/_sky/console
SKY_SUBAPP_VERBOSE=0        # 1 forwards spawned-child stdout/stderr to parent terminal
SKY_BIN=…                   # override `sky` binary path for SpawnSkyConsole
SKY_ADMIN_TOKEN=…           # /_sky/metrics + /_sky/console require Bearer in production
                            # (legacy: SKY_METRICS_TOKEN / SKY_CONSOLE_TOKEN_SECRET still honoured)
SKY_CONSOLE_AUTH=token      # v0.16.0+ three-mode console auth gate:
                            #   token → __Host-sky_console cookie + login POST form
                            #           (HKDF-derived signing key from SKY_CONSOLE_TOKEN)
                            #   app   → row-poly consoleAuth callback on Live.app cfg
                            #           (Request -> Task Error (Maybe Identity))
                            #   off   → console doesn't mount at all
                            # Production (ENV != dev/development/local) AND unset →
                            # mount declines + emits `console.disabled reason=auth-unset`
                            # warn log. Dev mode + unset → preserves v0.15.x open-in-dev
                            # behaviour.
SKY_CONSOLE_TOKEN=…         # v0.16.0+ secret used to derive the __Host- cookie HMAC key
                            # via HKDF-SHA256(secret, build-commit, "sky-console-cookie").
                            # Token-mode login form accepts THIS value verbatim.
SKY_CONSOLE_EMBED_ORIGIN=…  # v0.16.0+ opt-in for the URL handshake (?token=<JWT> →
                            # session cookie). Must be set to the EXACT origin of the
                            # embedding iframe (SkyDeploy control-plane). Unset → the
                            # URL handshake is disabled entirely. Closes the cookie/
                            # JWT confusion attack surface from the security review.
SKY_CONSOLE_DB_PATH=…       # when set, telemetry dual-writes every
                            # log/metric/span to the SQLite file at this
                            # path (WAL mode, 24h log/span retention,
                            # 7d metric retention). SkyDeploy injects
                            # `/data/console.db` on Pro+ tenants so the
                            # bundled console mini-app can render
                            # history beyond the 10k-line / 1k-span
                            # in-RAM caps. Unset → pure in-RAM (default).

# v0.16.1+ — HubExporter (in-process OTLP push to a remote console hub)
SKY_CONSOLE_HUB=…           # https://… OTLP endpoint. Unset → exporter off.
SKY_CONSOLE_HUB_TOKEN=…     # ≥32-byte bearer. Refuses to start if shorter.
SKY_CONSOLE_BATCH_INTERVAL_MS=2000  # 2 s on VMs; 200 ms in serverless.
SKY_CONSOLE_SPOOL_MODE=auto # auto | file | memory. Auto-detects via
                            # K_SERVICE / AWS_LAMBDA_FUNCTION_NAME → memory.
SKY_CONSOLE_SPOOL_PATH=…    # file mode. Default: /var/lib/sky/console-spool.db
                            # (linux) / ~/Library/Application Support/sky/…
SKY_CONSOLE_SPOOL_RETENTION=168h    # delete rows older than this
SKY_CONSOLE_SPOOL_MAX_BYTES=104857600  # 100 MB hard cap; oldest evicted
```

The production gate is `ENV` then `SKY_ENV` fallback. Unset OR
set to `dev` / `development` / `local` → dev mode. Anything else
(`production`, `prod`, `staging`, `qa`, `preview`, …) → production
mode (console + banner gone, metrics auth on). Same gate governs
three things; no chance of leaking a dev surface.

### Env prefix (multi-tenant)

```toml
[env]
prefix = "MYAPP"            # internal SKY_*_ vars become MYAPP_*_
```

Only Sky's internal namespace is affected. User code calling
`System.getenv "DATABASE_URL"` reads the raw name.
`System.setenv name value : Task Error ()` / `System.unsetenv` are
the runtime escape hatch.

### Compiler internals (build-time only)

`SKY_DCE=0` disables DCE. `SKY_SOLVER_BUDGET=N` overrides the HM
solver step cap (0 = disable, default uses constraint-count ×
factor). `SKY_SOLVER_BUDGET_FACTOR=K` overrides the multiplier
(default 200).

## Standard library — Layer 3 (every kernel module is Sky source)

Source location: `sky-stdlib/{Sky/Core,Std,Sky/Http}/*.sky`.

Each binding is either:

1. **Pure Sky** — recursive / case-based implementation (lists,
   Maybes, Results).
2. **`Ffi.kernel "Name"` alias** — Sky-source declaration with HM
   signature; the compiler's Stage-4 rewrite routes call sites
   directly to the existing typed kernel dispatch (no runtime
   overhead, `sky doc` still surfaces the entry).

### Pure (no I/O, no Task wrap)

| Module | Path | Key functions |
|---|---|---|
| `Basics` | `Sky.Core.Basics` (autoloaded via `Sky.Core.Prelude`) | identity, always, not, toString, modBy, clamp, fst, snd, compare, negate, abs, sqrt, min, max |
| `String` | `Sky.Core.String` | 38 entries — length, reverse, append, split, join, contains/containsIn, startsWith/startsWithIn, endsWith/endsWithIn (haystack-first In-suffixed companions added v0.15.47), toInt, fromInt, toFloat, fromFloat, toUpper, toLower, trim/trimStart/trimEnd, replace, slice, dropLeft, dropRight (v0.16.31 — Elm-shaped rune-based), isEmpty, fromChar, toList, fromList, repeat, padLeft, padRight, casefold, equalFold, isEmail, isUrl, words, lines, concat |
| `List` | `Sky.Core.List` | map, filter, foldl, foldr, length, head, tail, take, drop, append, concat, concatMap, reverse, member, any, all, range, zip, find, isEmpty, indexedMap, cons + reverseHelp/indexedMapHelp |
| `Dict` | `Sky.Core.Dict` (kernel) | empty, insert, get, remove, member, keys, values, toList, fromList, map, foldl, union |
| `Set` | `Sky.Core.Set` (kernel) | empty, insert, remove, member, union, diff, intersect, fromList, toList, size |
| `Maybe` | `Sky.Core.Maybe` | withDefault, map, andThen, map2-5, andMap, combine, isJust, isNothing |
| `Result` | `Sky.Core.Result` | withDefault, map, andThen, mapError, map2-5, andMap, combine |
| `Math` | `Sky.Core.Math` | 36 entries — abs, min, max; sqrt, pow, cbrt, hypot; exp, exp2, log, log2, log10; floor, ceil, round, trunc; sin, cos, tan; asin, acos, atan, atan2; sinh, cosh, tanh, asinh, acosh, atanh; mod, remainder; pi, e, phi, sqrt2, inf, nan |
| `Regex` | `Sky.Core.Regex` | match, find, findAll, replace, split |
| `Char` | `Sky.Core.Char` | isAlpha, isDigit, isLower, isUpper, toUpper, toLower |
| `Path` | `Sky.Core.Path` | base, dir, ext, isAbsolute |
| `Crypto` | `Sky.Core.Crypto` | sha256, sha512, sha1, md5, hmacSha256, hmacSha512, rsaSha256Sign, rsaSha256Verify, constantTimeEqual (pure); aesGcmEncrypt/Decrypt, chacha20Encrypt/Decrypt, aesKeyFromPassword, chachaKeyFromPassword (Result Error String — symmetric encryption, AEAD); randomBytes, randomToken (Task — entropy) |
| `Bytes` | `Sky.Core.Bytes` | empty, length, isEmpty, fromString/toString (UTF-8 lossy via Maybe), fromHex/toHex, fromBase64/toBase64, append, slice |
| `Jwt` | `Sky.Core.Jwt` | encode, decode (HS256 + RS256 — signature + `exp`/`nbf` checked); `hs256`/`rs256` algorithms; `claims` builder — issuer/subject/audience/expiresAt/notBefore/issuedAt/jwtId/withClaim |
| `Encoding` | `Sky.Core.Encoding` | base64Encode/Decode, urlEncode/Decode, hexEncode/Decode |
| `JsonEnc` | `Sky.Core.Json.Encode` | string, int, float, bool, null, list (Elm-style `(a -> Value) -> List a -> Value`), object, encode |
| `JsonDec` | `Sky.Core.Json.Decode` | string/int/float/bool, decodeString, field, at, index, list, map, andThen, succeed, fail, oneOf, map2-4 |
| `JsonDecP` | `Sky.Core.Json.Decode.Pipeline` | required, optional, custom, requiredAt |
| `Uuid` | `Sky.Core.Uuid` | v4, v7 (bare zero-arg — called without `()`), parse |
| `Decimal` | `Std.Decimal` | Arbitrary-precision arithmetic (shopspring/decimal). 42 entries. Banker's round, percent helpers. |
| `Money` | `Std.Money` | Currency-typed Money on Decimal + ISO 4217 enum (50+ codes + crypto). 44 entries. `allocate` (fair split), conversion rates. |

### Effects (`Task Error a`)

| Module | Path | Key functions |
|---|---|---|
| `Task` | `Sky.Core.Task` | succeed, fail, map, andThen, perform, sequence, parallel, lazy, run, fromResult, andThenResult, mapError, onError; **retryWith** + `RetryPolicy e` + `ShouldRetry e` ADT (RetryAlways \| RetryWhen (e -> Bool)). Build via linearBackoff / exponentialBackoff / defaultRetryPolicy; decorate via withJitter / withMaxAttempts / withBaseMs / withKind / withRetryOn (alias for retryOn). v0.15.50+ ShouldRetry is HM-pure (portable to Rust / WASM backends). |
| `Cmd` | `Std.Cmd` | none, batch, perform, publish (echo-by-default pub/sub from update return), publishNoEcho (opt-out echo — broker skips publisher's own subscription) |
| `Sub` | `Std.Sub` | none, every, batch, subscribeTopic (pub/sub receive) |
| `PubSub` | `Std.PubSub` | publish (Task-shaped — callable from raw `api` handlers / post-init / scheduled jobs; complements `Cmd.publish` which is bound to update-returns), publishNoEcho (Task-shaped no-echo — sets the broker's SkipOrigin bit for v0.16+ cross-process tier propagation) |
| `Time` | `Sky.Core.Time` | now, sleep, every, unixMillis, format/formatISO8601/formatRFC3339/formatHTTP, addMillis, diffMillis, timeString |
| `Std.Time` | `Std.Time` | 32 entries. IANA zones, addMonths/Years (month-end CLAMPED), dayOfWeek (ISO Mon=1..Sun=7), weekOfYear (ISO 8601), startOfDay/Week/Month/Year, diffDays/Hours/Minutes/Seconds. v0.15.48+ adds `*Utc` infallible companions (`dayOfWeekUtc` / `startOfDayUtc` / `yearUtc` / etc. — `Int -> Int` shape, plug "UTC" at the call site so server-internal callers don't thread `Result.withDefault 0`). |
| `Random` | `Sky.Core.Random` | int, float, range, choice, shuffle, weighted (entropy-backed); seed, seededInt, seededFloat, seededChoice (deterministic splitmix64) |
| `Http` | `Sky.Core.Http` | get, post, request (custom method/headers/body/timeout via `HttpRequest`), defaultRequest/withMethod/withHeader/withTimeout/withBody builders, parseQuery; typed `HttpResponse = { status : Int, body : String, headers : Dict String String }` |
| `File` | `Sky.Core.File` | readFile, readFileLimit, readFileBytes, writeFile, append, exists, remove, mkdirAll, readDir, isDir, tempFile, tempDir, copy, rename |
| `Io` | `Sky.Core.Io` | readLine, writeStdout, writeStderr |
| `System` | `Sky.Core.System` | args, getArg, getenv, getenvOr (bare), getenvInt, getenvBool, setenv, unsetenv, cwd, loadEnv, exit |
| `Process` | `Sky.Core.Process` | run (subprocess) |
| `Db` | `Std.Db` | open, connect, close, exec, execRaw, query, insertRow, getById, updateById, deleteById, findOneByField, findManyByField, findByConditions, unsafeFindWhere, queryDecode, withTransaction, migrate (versioned forward-only schema migrations + `_sky_migrations` + checksum guard), getField, getString, getInt, getBool. **v0.16.26+ typed parameter binding**: `SqlValue` ADT (`SqlString` / `SqlInt` / `SqlFloat` / `SqlBool` / `SqlBytes` / `SqlDecimal` / `SqlTime` / `SqlMoney` / `SqlNull SqlValue`) gives mixed-type SQL params as a homogeneous `List SqlValue` — closes the no-workaround gap for `INSERT … VALUES (?, ?, ?)` mixing `String + Maybe Int + Bool`. 8 `fromMaybe*` helpers for nullable columns. `SqlField` (`SetField SqlValue` / `OmitField`) + `Db.updateFields conn table whereCols setFields` for PATCH semantics with column-omit support; `Db.insertFields conn table fields` is the INSERT counterpart — `OmitField` columns drop from the SQL so the database applies DEFAULT (all-omit → `INSERT … DEFAULT VALUES`); `Db.insertFieldsReturning conn table fields projection decoder` (#586) appends `RETURNING <projection>` and decodes returned rows via `Std.Db.Decode` so you can pick up assigned autoincrement ids / applied DEFAULTs at INSERT time (SQLite ≥ 3.35 / PostgreSQL). Money serialises lossless as `"ISO_CODE AMOUNT"` TEXT — paired with `Db.Decode.money` for round-trip. |
| `Auth` | `Std.Auth` | register, login, setRole (Task) + hashPassword, hashPasswordCost, verifyPassword, passwordStrength, signToken, verifyToken (Result); v0.15.48+ signTokenWithClaims / verifyTokenWithAlgorithm — typed-builder aliases over Sky.Core.Jwt for fine-grained algorithm + claims control |
| `Log` | `Std.Log` | println, debug, info, warn, error, debugWith, infoWith, warnWith, errorWith |
| `Trace` | `Std.Trace` | span, event, attr — opt-in app-level tracing spans. Tier-1 spans (HTTP/session/Msg/DB/Auth/Http/File) are automatic; see `docs/observability.md` |
| `Server` | `Sky.Http.Server` | param, queryParam, header, getCookie, static (Layer 3 surface); higher-level `get/post/listen/text/json/html` stay kernel-only |
| `Stream` | `Sky.Http.Server.Stream` | stream, emit, finish, withContentType — server-side streaming HTTP responses (SSE / LLM token forwarding / chunked downloads). Mirror of `Sky.Core.Http.Stream` (which reads upstream bodies as Sub events). See `docs/skylive/http-streaming.md` §"Server-side" + `examples/30-sse-server-demo`. Synchronous bridge: `Sky.Core.Http.Stream.forEachChunk hdl body` (v0.15.41+) drains an upstream stream from inside a plain Sky.Http.Server handler goroutine — needed for the relay shape (upstream chunks → `Server.Stream.emit` downstream chunk-for-chunk; no Sky.Live update loop required). See `docs/skylive/http-streaming.md` §"Synchronous relay" + `examples/32-sse-relay`. |
| `Middleware` | `Sky.Http.Middleware` | withCors, withLogging, withBasicAuth, withRateLimit, **withCsrf** (v0.17 task #663 — double-submit cookie pattern; safe-method passes set `__Host-sky_csrf`, unsafe methods require matching `X-Csrf-Token` header or `_csrf` form field; constant-time compare; 403 + clear diagnostic on missing/mismatched token) |
| `Head` | `Std.Live.Head` | v0.15.58+. Per-page `<head>` injection — `title` / `meta name content` / `metaProperty property content` (OG) / `link [(k, v)...]` / `canonical href` / `jsonLd body` / `themeColor color` / `rss href title`. Opt in via optional `head : Model -> List (Html msg)` field on `Live.app` cfg; runtime splices the rendered list into `<head>` after baseline meta + before inline `<style>`. Absent field → byte-identical to pre-v0.15.58 output. |
| `Console` | `Std.Live.Console` | v0.16.0+. `Identity` type alias (`{ subject, email, claims : Dict String String }`) for the optional row-poly `consoleAuth : Request -> Task Error (Maybe Identity)` field on `Live.app` cfg. Framework calls the callback before mounting `/_sky/console` when `SKY_CONSOLE_AUTH=app`. `Nothing` → 403 + `console.auth.denied` audit log; `Just identity` → set `__Host-sky_console` cookie + allow. Same row-open pattern as v0.15.58 `head` — absent field → byte-identical to pre-v0.16.0 output. |
| `RateLimit` | `Sky.Http.RateLimit` | allow |
| `WebSocket` | `Sky.Core.WebSocket` (client) + `Sky.Http.Server.WebSocket` (server) | v0.15.46+. Bidirectional sockets — collab editor ops, multiplayer, bidirectional LLM chat, financial feeds. Client: `connect` / `connectWith` / `send` / `sendBinary` / `close` / `closeWithCode` (Task-tier) + `onOpen` / `onMessage` / `onClose` / `onError` (Sub-tier). Server: `upgrade` (returns from a Sky.Http.Server handler) + `sendToClient` / `sendBinaryToClient` / `broadcast` / `closeClient`. Built on `nhooyr.io/websocket`. Default 30 s heartbeat + 1 MiB max message + 64-frame read buffer. Server production gate: empty `originPatterns` returns 403 when `ENV=production`. **Stdlib typed-record convention (v0.15.46+): every typed record ships with a `default*` constructor + `with*` builder per field — always compose via builders so future field additions don't break call sites.** See `examples/33-websocket-echo`. |
| `Cache` | `Std.Cache` | v0.15.47+. LRU + TTL in-memory cache, `Cache k v` parametric on key and value. `CacheCfg` ships with `defaultCfg` + `withMaxEntries` / `withTTL` / `withMaxBytes` per v0.15.46 convention. `new` / `get` / `put` / `remove` / `clear` / `size` / `stats` (monotone hits/misses/evictions). Backed by `hashicorp/golang-lru/v2`; lazy TTL expiry (no background goroutine). |
| `Email` | `Std.Email` | v0.15.47+. Resend / SES / SendGrid / SMTP under one `EmailProvider` ADT. `EmailMessage` + `Attachment` typed records ship with `defaultMessage { from, to, subject }` + `with*` builders (`withCc` / `withBcc` / `withTextBody` / `withHtmlBody` / `withAttachment` / `withReplyTo`). `Email.send provider msg : Task Error String` returns the provider message id. `SKY_EMAIL_DRY_RUN=1` short-circuits for tests; `SKY_EMAIL_ENDPOINT_<PROVIDER>` overrides URLs for fixtures. |
| `Compression` | `Std.Compression` | v0.15.47+. `gzip` / `gunzip` (RFC 1952) + `zstdCompress` / `zstdDecompress` (RFC 8478). Operates on `String` (Bytes alias). Built on `compress/gzip` (stdlib) + `klauspost/compress/zstd`. |
| `Csv` | `Std.Csv` | v0.15.47+. `parse` / `parseWithDelimiter` (returns `Csv = { header, rows }`), `encode` / `encodeWithDelimiter` (RFC 4180 quoting), `parseStreamFromFile` for buffered large-file reading. Built on `encoding/csv` (stdlib). |
| `Config` | `Std.Config` | v0.15.47+. Typed TOML / YAML / JSON decoders mirroring `Sky.Core.Json.Decode`'s shape — same `string` / `int` / `float` / `bool` / `nullable` / `field` / `at` / `list` / `succeed` / `fail` / `map` / `andThen` combinators. `decodeToml` / `decodeYaml` / `decodeJson` + `loadFromFile` (extension dispatch). Backends: `BurntSushi/toml` + `gopkg.in/yaml.v3` + stdlib `encoding/json`. |
| `ToString` | `Sky.Core.ToString` | v0.15.48+. Naming-consistency surface: `fromInt`/`fromFloat`/`fromBool`/`fromTime` route to the canonical kernels — zero overhead, exists for editor / `sky doc` discoverability. AI-written code is encouraged to default to `ToString.fromInt n` rather than memorising the per-type kernel sub-namespace. |
| `Pure` | `Sky.Core.Pure` | v0.15.50+. Uniform `() -> Task Error a` companion surface for runtime-arity-0 stdlib bindings (`uuidV4` / `uuidV7` / `timeNow` / `timeUnixMillis` / `systemArgs` / `systemCwd` / `systemLoadEnv` / `ioReadLine` / `dbConnect`). Closes Limitation #7 for new code without renaming any existing surface — every `Pure.*` is a tail-call alias to the canonical kernel, typed `SkyTask[Error, T]` end-to-end. Existing names + shapes unchanged. |

### Diverging

`System.exit : Int -> a` — process termination, polymorphic return.

### Prelude (autoloaded via `Sky.Core.Prelude exposing (..)`)

`Result (Ok/Err)`, `Maybe (Just/Nothing)`, `identity`, `not`,
`always`, `fst`, `snd`, `clamp`, `modBy`, `errorToString`.

## Sky.Live + Sky.Http.Server

### Live.app shape

```elm
main =
    Live.app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" HomePage, route "/about" AboutPage ]
        , notFound = HomePage
        }
```

HTTP-first (full HTML on load, patches on events), SSE
subscriptions, session stores (memory / sqlite / redis / postgres /
firestore), type-safe events, VNode diffing.

**`init` is per-session, not per-page-reload.** First request from a
browser with no `sky_sid` cookie fires `init`. Browser reload while
the session is alive RESTORES Model from the session store — `init`
does NOT run. To force a fresh `init` (demo reset / e2e bootstrap):
`Cmd.perform (Cookie.expire "sky_sid")` then reload. If the goal is
"my other tab missed an update", reach for `Cmd.publish` instead —
reload-as-resync is a missing broadcast, not a feature gap. Details
in `docs/skylive/overview.md` §"Session lifecycle — when `init` runs".

### init's `req` shape (v0.16.7 #417 + v0.16.8 #423)

`init` receives a `req` value that carries the full request
context:

| Field | Type | Source |
|---|---|---|
| `req.path` | `String` | URL path |
| `req.query` | `String` | raw `?...` (no parser yet — parse via `Sky.Core.Http.parseQuery` if needed) |
| `req.params` | `Dict String String` | matched-route `:name` segments (#417) |
| `req.method` | `String` | request method (#423) |
| `req.headers` | `Dict String String` | request headers, canonical case (#423) |
| `req.cookies` | `Dict String String` | parsed cookies (#423) |

Session bootstrap in init is now a one-line read:

```elm
init req =
    let sid = Maybe.withDefault "" (Dict.get "sky_sid" req.cookies) in
    ( { session = lookupSession sid }, Cmd.none )
```

No `Cmd.perform /api/whoami` round-trip needed for first render.
Apps that ignore `req` build byte-identical to the pre-v0.16.7
shape (row-poly extension).

### Per-page `<head>` injection (v0.15.58+)

Add an optional `head : Model -> List (Html msg)` field on the
`Live.app` cfg. The runtime calls it once per full GET (initial
page load + sky-nav navigation) and splices the returned list
into `<head>` AFTER the runtime's required `<meta charset>` /
`<meta viewport>` / `<meta sky-base>` tags and BEFORE the inline
`<style>` reset. The HM signature is row-open (`appExt` row var),
so existing apps that omit the field type-check + build unchanged
— the output is byte-identical to the pre-v0.15.58 wrap.

```elm
import Std.Live.Head as Head

main =
    Live.app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" HomePage, route "/blog/:slug" BlogPostPage ]
        , notFound = HomePage
        , head = headFor
        }


headFor : Model -> List (Html Msg)
headFor model =
    [ Head.title (titleFor model.page)
    , Head.meta "description" (descriptionFor model.page)
    , Head.canonical (canonicalFor model.page)
    , Head.metaProperty "og:title" (titleFor model.page)
    , Head.metaProperty "og:image" "https://example.com/og.png"
    , Head.themeColor "#1a1a2e"
    , Head.rss "/rss.xml" "Site Blog"
    , Head.jsonLd (jsonLdFor model.page)  -- raw JSON string body
    ]
```

`Std.Live.Head` helpers (all return `Html msg` so they compose
into the same list):

| Helper | Emits |
|---|---|
| `title : String -> Html msg` | `<title>…</title>` |
| `meta : String -> String -> Html msg` | `<meta name="…" content="…">` |
| `metaProperty : String -> String -> Html msg` | `<meta property="…" content="…">` (Open Graph, Facebook) |
| `link : List (String, String) -> Html msg` | `<link …>` with arbitrary attr pairs |
| `canonical : String -> Html msg` | `<link rel="canonical" href="…">` |
| `jsonLd : String -> Html msg` | `<script type="application/ld+json">…</script>` (raw JSON body) |
| `themeColor : String -> Html msg` | `<meta name="theme-color" content="…">` |
| `rss : String -> String -> Html msg` | `<link rel="alternate" type="application/rss+xml" …>` |

Pair with `Std.Html.node "link" […] []` for cases the helpers
don't cover (preload hints, custom favicon shapes, …).

**SSE patches scope to `<body>`** — head updates require a full
reload. That matches the typical case (head is derived from page
identity; in-app navigation already triggers a sky-nav fetch +
full-body patch + history push). For a UI that swaps `<head>`
contents on every Msg, drop the `head` field and emit a
`<title>`/`<meta>` inside `view` via `Html.node` — the diff layer
patches normal DOM nodes regardless of position.

### URL routing + history

The `routes` field maps URL paths to Page values. The runtime
matches incoming URLs in declaration order, captures `:param`
segments, and reflect-calls the Page constructor with the captured
values (always `String`). Declaration order matters — put
literals before patterns (`/apps/new` before `/apps/:slug`, or
"new" matches as a slug).

```elm
type Page
    = LoginPage
    | DashboardPage
    | NewAppPage
    | AppDetailPage String         -- :slug delivers a String
    | InsightsPage

routes =
    [ route "/" LoginPage
    , route "/auth/sign-in" LoginPage
    , route "/apps" DashboardPage
    , route "/apps/new" NewAppPage             -- literal before pattern
    , route "/apps/:slug" AppDetailPage        -- ctor: String -> Page
    , route "/insights" InsightsPage
    ]
notFound = LoginPage
```

**URL-from-Page** (keeping the address bar in step with programmatic
`Navigate` Msgs): emit a sentinel `<div>` with `data-sky-path` on
every render. The runtime pushes / replaces history when the value
differs from `location.pathname` — called from BOTH `__skyPatch`
(full-body / `sky-nav` fetches) AND `__skyApplyPatches` (SSE
patches), so all in-app navigation updates the URL.

```elm
import Std.Html as Html
import Std.Html.Attributes as Attr

urlSync : Model -> Element msg
urlSync model =
    Ui.html
        (Html.node "div"
            [ Attr.attribute "data-sky-path" (currentPath model) ]
            []
        )

-- Place urlSync inside the view's top-level column, next to the shell.
```

`data-sky-path` is typed (no JS-in-string, no `new Function()`,
works under strict CSP, no XSS surface). Leave the element in the
DOM after the runtime processes it — removing it orphans its
`sky-id` and the next attribute patch silently skips (the
patch's `querySelector('[sky-id=…]')` returns null). The
path-check (`location.pathname !== p`) keeps the call idempotent.

For **link navigation**, add `sky-nav` to the `<a>` — the runtime
intercepts the click, fetches the URL with `X-Sky-Nav: 1`,
full-body-patches, and pushes history. No app code needed.

```elm
Html.a [ Attr.href "/apps", Attr.attribute "sky-nav" "" ] [ Html.text "Dashboard" ]
```

**Back / Forward** is handled by the runtime: a popstate listener
re-fetches the URL with `X-Sky-Nav: 1` and patches. App code does
NOT need anything for Back to work.

`data-sky-eval` (older, runs the attribute via `new Function()`) is
CSP-incompatible (`script-src` without `'unsafe-eval'` blocks it)
AND only fires from `__skyPatch`, not from SSE patches. Use
`data-sky-path` for URL updates; specific-purpose typed attributes
for other one-off post-patch effects.

**Auth gates around routes.** For public-vs-authenticated apps:

- Let Sky.Live route the URL to a page as usual.
- In `pageBody` / view, outer-case on `model.session`: signed-out
  always renders the sign-in surface regardless of page.
- Use a single `currentPath : Model -> String` (not a per-page
  `pathForPage`) that returns the sign-in URL when `session =
  Nothing`, otherwise dispatches on `model.page`. So the address bar
  follows what the user actually sees.

```elm
currentPath : Model -> String
currentPath model =
    case model.session of
        Nothing -> "/auth/sign-in"
        Just _ ->
            case model.page of
                LoginPage          -> "/apps"            -- authed at sign-in → bounce
                DashboardPage      -> "/apps"
                NewAppPage         -> "/apps/new"
                AppDetailPage slug -> "/apps/" ++ slug
                InsightsPage       -> "/insights"
                AdminUsersPage     -> "/users"
```

**Slug ↔ subdomain convention.** When apps deploy under a wildcard
domain (`*.platform.app`), prefer slug-keyed URLs (`/apps/<slug>`)
that match the subdomain (`<slug>.platform.app`) — bookmarkable,
follows renames. Carry the slug on the Page constructor; handlers
that need the numeric id resolve it via a `findBySlug` helper.

### Async commands

`update msg model` returns `(Model, Cmd Msg)`. `Cmd.perform task
toMsg` runs the task in a goroutine; result dispatches back as a
Msg through SSE.

```elm
update msg model =
    case msg of
        FetchData ->
            ( { model | loading = True }
            , Cmd.perform (Http.get "/api/data") DataLoaded )
        DataLoaded result ->
            ( { model | loading = False, data = Result.withDefault "" result }
            , Cmd.none )
```

### Wire-event arg shapes

| Event | Element | Args |
|---|---|---|
| `click`, `focus`, `blur`, `mouseover`/`mouseout` | any | `[]` |
| `input`/`change` | checkbox | `[checked : Bool]` |
| `input`/`change` | radio | `[checked : Bool]` (use `onClick` per radio instead — see below) |
| `input`/`change` | number / range | `[value : Float]` |
| `input`/`change` | text / textarea / select | `[value : String]` |
| `submit` | form | `[formData]` — Dict String String OR typed record alias |
| `keydown`/`keyup`/`keypress` | any | `[key : String]` |

### Radio convention — `onClick` per label, not `onInput`

A radio's `input` event reports `checked=True` (Bool), not the
chosen value. Bind a fully-applied Msg per choice via `onClick`:

```elm
label [ for "role-guardian", onClick (UpdateRole "guardian") ]
    [ input [ type "radio", name "role", value "guardian", id "role-guardian" ] []
    , text "Guardian"
    ]
```

The `for`/`id` pairing lets the browser toggle the radio natively;
`onClick` carries the typed Msg.

### Forms with passwords (mandatory pattern)

**Use `onSubmit` with form data, NOT `onInput` per keystroke on
password fields.**

```elm
type alias AuthCreds = { email : String, password : String }
type Msg = UpdateEmail String | DoSignIn AuthCreds

view model =
    form [ onSubmit DoSignIn ]
        [ input [ type "email", name "email", value model.email, onInput UpdateEmail ] []
        , input [ type "password", name "password" ] []  -- no value, no onInput
        , button [ type "submit" ] [ text "Sign in" ]
        ]
```

Three reasons:

1. **Password managers** (1Password / Bitwarden / browser autofill)
   watch DOM mutations on password inputs. Every server-driven
   re-render with `value=…` triggers a re-prompt/re-fill cycle.
2. **Secret never lives in Model** — no `onInput UpdatePassword`
   Msg → no Model field → never serialised into Redis / Postgres /
   Firestore session stores.
3. **Race-free submit** — form submit reads the live DOM value, not
   a debounced keystroke.

The `DoSignIn AuthCreds` constructor takes a typed record. The wire
driver decodes form data directly into the Go struct via
case-insensitive `json.Unmarshal` (`State_AuthCreds_R{Email,
Password}`). No per-Msg decoder boilerplate.

### Connection status banner

Bottom-pinned, three states:

- **connected** — `display:none`.
- **reconnecting** — amber `Reconnecting…`. Shown when SSE drops or
  POST `/_sky/event` fails. 500 ms grace before painting.
- **offline** — red `Connection lost — refresh to retry`. Reached
  after `SKY_LIVE_RETRY_MAX_ATTEMPTS` (default 10, ~2 min). The
  runtime keeps retrying in the background so a healed proxy
  recovers without a refresh.

POST failures while reconnecting land in `__skyEventQueue` (FIFO,
capped at `SKY_LIVE_QUEUE_MAX`) and replay on SSE `hello`. Server
seq ordering tolerates late delivery.

**Reverse-proxy hardening.** Every `/_sky/sse` sends
`X-Accel-Buffering: no` + 2 KB padding + immediate `event: hello`
handshake + heartbeats every 15 s. Every `/_sky/event` POST carries
`X-Sky-Live: 1`. Client: `connected` only flips on `hello` (never
raw `EventSource.open`); 8 s watchdog reopens on missing hello;
35 s watchdog reopens on missing heartbeat. POST 200 OK without
`X-Sky-Live: 1` is treated as wedged-proxy and rerouted.

Localise via `status = { reconnecting = "Reconnexion…", offline = "Connexion perdue" }` on `Live.app`'s cfg record. Partial overrides fall back to English defaults. Strings JSON-encoded, rendered via `textContent` (never `innerHTML`).

### Input preservation across re-renders

Three failure modes closed:

1. **Empty patches** JSON-ack instead of HTML-fallback (preserves
   uncontrolled fields like password).
2. **Full-body swap** preserves EVERY uncontrolled INPUT /
   TEXTAREA / SELECT, not just `document.activeElement`.
3. **Open `<select>` defence** — `__skyApplyPatches` skips any
   patch where the target is the focused select / contains it /
   is contained by it. Tick subscriptions accumulate state on the
   server; next user interaction reconciles.

### Sky.Http.Server

```elm
main =
    Server.listen 8000
        [ Server.get "/" (\_ -> Task.succeed (Server.text "Hello!"))
        , Server.get "/api/users/:id" getUser
        , Server.post "/api/data" handlePost
        , Server.static "/assets" "./public"
        ]
```

Routes: `get/post/put/delete/any` | groups with prefix | cookies
(HttpOnly, Secure, SameSite) | extractors: `param`, `queryParam`,
`header`, `getCookie` | responses: `text`, `json`, `html`,
`withStatus`, `redirect` | middleware: `Handler -> Handler`.

**Handler annotation (v0.16.4+).** Named handlers ascribe at
head position with the `Handler` alias:

```elm
import Sky.Http.Server exposing (Handler)

getUser : Handler
getUser req = ...
```

`Handler` is a transparent alias for
`Request -> Task Error Response`, exported from
`Sky.Http.Server`. The long-form `: Request -> Task Error Response`
still works — pick whichever reads better at the call site. Same
pattern works for any function-typed alias:
`view : Renderer Msg`, `decodeUser : Decoder User`, etc. This is
canonical Elm shape; head-position alias unfolding was closed by
contributor PR #123.

## Sky Console + sub-app mount + observability

Every Sky.Live and Sky.Http.Server app auto-mounts a Std.Ui dev
console at `/_sky/console` in dev mode, with structured logging,
Prometheus metrics, distributed tracing — no separate stack to
stand up.

| Surface | What it is |
|---|---|
| `🔍 Console` link | Floating bottom-right anchor injected into every dev-mode page. Same-origin link to `/_sky/console`. |
| `/_sky/console/*` | Reverse-proxied to a bundled Sky.Live mini-app spawned as a child process. |
| `/_sky/metrics` | Prometheus scrape endpoint (Bearer-gated in production). `sky_live_requests_total{route,status}`, `sky_live_request_seconds`, error counters. |
| `/_sky/healthz` · `/_sky/readyz` | Liveness + readiness probes. |
| `/_sky/buildinfo` | Commit SHA, build timestamp, Sky version. |
| `/_sky/observability/ingest` | Sub-app log/metric/span push endpoint. |
| Structured logs | Every `Log.*` carries level + message + request-correlation ID. HTTP access log automatic. |
| Trace spans | Every HTTP request opens a span; `rt.RecordTrace` adds child spans. Exported to OpenTelemetry if `OTEL_EXPORTER_OTLP_ENDPOINT` is set. |

### `rt.MountSubApp`

```go
import "your-app/rt"
rt.MountSubApp(mux, "/billing", rt.SpawnBinary("./billing-app"))
rt.MountSubApp(mux, "/admin",   rt.SpawnBinary("./admin-app"))
rt.MountSubApp(mux, "/docs",    rt.SpawnBinary("./hugo-server"))
```

Each child runs as its own process — its own session store,
update loop, cookies, zero shared state. Reverse proxy gives the
user one port and one origin. Cost: ~5 MB RAM + ~5 ms per request
hop.

### Sub-app observability federation

Each sub-app spawns `rt.PushExporter` (background goroutine) that
batches logs / metrics / spans and POSTs every 2 s to
`<parent>/_sky/observability/ingest` with namespace labelling.
Single Prometheus scrape on the parent covers the tree. Auth:
shared secret via `X-Sky-Ingest-Token` (auto-generated per parent
boot; constant-time compare).

### Production gate

`productionFromEnv()` reads `ENV` then `SKY_ENV`. Unset / `dev` /
`development` / `local` → dev mode. Anything else → production
(console + banner gone, metrics auth on).

**`SKY_LIVE_BASE_PATH`** — set automatically when a Sky.Live app
runs as a sub-app. Causes: page wrap injects `<meta name="sky-base">`
so inlined JS prefixes `/_sky/event` etc.; dev banner suppressed;
`MountObservabilityEndpoints` skipped (parent owns the endpoints);
`maybeAutoMountConsole` early-returns (no recursive auto-mounts).

## Std.Ui — typed no-CSS layout DSL

Layered above `Std.Html`; renders to inline-styled HTML on the
server. Pick `row` / `column` / `el` for layout, attach typed
attributes from `Background` / `Border` / `Font` / `Region`
sub-modules, never write CSS.

```elm
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Ui.Border as Border
import Std.Ui.Font as Font

view model =
    Ui.layout []
        (Ui.row
            [ Ui.spacing 12, Ui.padding 16
            , Background.color (Ui.rgb 255 102 0)
            , Border.rounded 4
            ]
            [ Ui.button [] { onPress = Just Decrement, label = Ui.text "−" }
            , Ui.el [ Font.size 24, Font.bold ] (Ui.text (String.fromInt model.count))
            , Ui.button [] { onPress = Just Increment, label = Ui.text "+" }
            ])
```

### Three idioms AI tooling MUST get right

1. **Forms with sensitive inputs use `Ui.form` + `onSubmit
   DoSignIn`, NOT `onInput` per keystroke on password fields.**
   See the password pattern in the Sky.Live section above.

2. **Real `<input>` elements use `Ui.input`, NOT `Ui.el [
   htmlAttribute "type" "text" ]`.** `Ui.el` builds a Node that
   renders as `<div>` — browsers ignore `type=` / `value=` on
   non-inputs.

3. **Std.Ui-heavy modules (~25+ polymorphic `Element Msg`
   helpers) MUST be split across multiple modules.** A monolithic
   `Main.sky` can blow the HM type-checker heap (Limitation #17).
   Canonical split: `State.sky` (types, no Std.Ui imports) /
   `Update.sky` / `View/Common.sky` / one View module per page /
   `Main.sky` dispatcher. See `examples/19-skyforum`'s 8-module
   form for the working shape.

4. **`Input.*` size / layout attrs apply to the wrapper, form
   attrs stay on the inner control.** Every `Std.Ui.Input.*` call
   (text / multiline / email / username / search / currentPassword
   / newPassword / slider / checkbox / radio / radioRow) routes
   layout attrs (`Ui.width`/`Ui.height`/`Ui.padding`/`Ui.spacing`/
   `Ui.alignX`/`Ui.alignY`/`Ui.nearby`/`Ui.pointer`/`Ui.overflow`)
   to the outer wrapper `wrapWithLabel` emits, while form / event /
   visual attrs stay on the inner `<input>` / `<textarea>`. So
   `Input.multiline [Ui.height Ui.fill] {...}` inside a column-fill
   parent fills the parent; `Background.color (Ui.rgb 240 240 240)`
   colours the textarea itself, not the wrapper.

### `Ui.fill` emission (v0.15.55+, refined v0.15.56)

`Ui.fill` lowers asymmetrically per the parent's flex direction:

| Position | CSS emitted |
|---|---|
| Main-axis fill | `flex-grow: N; min-{w,h}: 0;` |
| Cross-axis HEIGHT fill (row child) | nothing — relies on flex default `align-items: stretch` |
| Cross-axis WIDTH fill (column / el / textColumn child) | `width: 100%;` |

The asymmetry closes a real bug class. CSS Flexbox §9.8 resolves
`%` against a parent's USED size only when "definite"; a flex-
grow-derived height is indefinite. Row parents commonly have
indefinite heights → the pre-v0.15.55 `height: 100%` on cross-
axis fill collapsed every child to text-content height (issue
#63 — three-pane app shell, Input.multiline → 22/51 px). Width
keeps `100%` because column-parent widths are typically definite
AND it survives the `[Ui.width fill, Ui.centerX]` cascade — the
canonical centred-page-content shape.

**v0.15.56 F4 `align-self` single-emission contract.** The
cross-axis fill emitters dropped their redundant `align-self:
stretch` declaration — `stretch` is the default `align-items`
value, so it was a no-op AND created a cascade conflict with
explicit alignment attrs (`Ui.centerX/Y`, `alignLeft/Right/Top/
Bottom`). Post-F4 invariant: at most ONE `align-self`
declaration per element, sourced from `alignSelfX/Y` only.
User-visible rendering identical to v0.15.55; code is
order-independent.

### Void-element pseudo-class / animation / transition / media-
### query style hoist (v0.15.57+ — #409)

Pseudo-class rules (`Background.activeColor` / `hoverColor` /
`focusColor`), CSS transitions (`Std.Ui.Transition.attribute`),
keyframe animations (`Std.Ui.Animation.attribute`), and breakpoint
media queries (`Ui.breakpoint Ui.mobile [...]`) all emit a
sky-id-scoped `<style>` element to apply the rule. Pre-v0.15.57
the runtime prepended that `<style>` as the FIRST CHILD of the
carrying element — fine for `<div>` / `<button>` / etc., but
silently DROPPED on void HTML elements (`<input>`, `<img>`,
`<br>`, `<hr>`, …) because `renderVNode` skips children for
void tags (the self-closing `/>` ends the element).

Post-v0.15.57: the style block is hoisted to a SIBLING slot
immediately after the void element. CSS selector still keys off
the void element's sky-id, so the rule applies correctly. This
means:

```elm
Input.text
    [ Background.color (Ui.rgb 240 240 240)
    , Background.activeColor (Ui.rgb 200 100 50)   -- now works on <input>
    , Background.hoverColor  (Ui.rgb 50 50 200)    -- @media (hover: hover) gate
    ]
    cfg
```

The `<input>` inside `Input.text`'s wrapper renders with a
sibling `<style data-sky-pc="<input-sky-id>">` carrying the
`:active` + `:hover` rules. No call-site change needed for
existing code — the runtime fix is transparent.

### `Ui.layoutWith` — wrapper customisation (v0.15.56)

```elm
Ui.layoutWith { wrapperAttrs : [Attr msg], rootAttrs : [Attr msg] } -> Element msg -> Html
```

Additive entry point. `wrapperAttrs` reach the outer 100 vh
`<div>` page wrapper (Background.color for page-wide dark mode,
Font.color / Font.family for document-wide typography, Border /
class / aria-* / data-* for analytics / a11y landmark routing).
`rootAttrs` apply to the root element (same as `Ui.layout`'s
argument).

`Ui.layout attrs el` is now `Ui.layoutWith { wrapperAttrs = [],
rootAttrs = attrs } el` — byte-identical for existing call
sites. Reach for `layoutWith` when you need the wrapper to take
visual styles (dark page, custom font cascade, page background
image).

### Surface highlights

Full reference: `docs/skyui/overview.md`.

- **Entry points**: `layout : List Attr -> Element -> Html` +
  `layoutWith : { wrapperAttrs : List Attr, rootAttrs : List Attr } -> Element -> Html`
  (v0.15.56 — reach the page wrapper for dark mode / Font cascade /
  flex-direction override).
- **Layout**: `el`, `row`, `column`, `wrappedRow`, `grid` +
  `gridColumns N` (CSS-Grid auto-fit), `paragraph`, `textColumn`,
  `text`, `none`, `html`.
- **Sized elements**: `link { url, label }`, `image { src,
  description }`, `button { onPress, label }`, `input`, `form
  onSubmit`.
- **Length**: `px`, `fill`, `fillPortion Int`, `content`,
  `shrink`, `minimum Int Length`, `maximum Int Length`, `vh Int`,
  `vw Int`.
- **Padding**: `padding Int`, `paddingXY x y` (X-first, Y-second),
  `paddingEach { top, right, bottom, left }`, `spacing Int`.
- **Alignment**: `centerX`, `centerY`, `alignLeft`, `alignRight`,
  `alignTop`, `alignBottom`, `pointer`.
- **Overflow**: `clip`, `clipX`, `clipY`, `scrollbars`,
  `scrollbarX`, `scrollbarY`.
- **Nearby**: `above`, `below`, `onLeft`, `onRight`, `inFront`,
  `behind` (absolute-positioned overlays).
- **Events**: `onClick msg`, `onSubmit msg`, `onInput (String ->
  msg)`, `onChange`, `onFocus`, `onMouseOver/Out`, `onKeyDown`,
  `onFile (String -> msg)`, `onImage (String -> msg)`.
- **File/image upload hints**: `fileMaxSize Int` (bytes —
  browser-side cap, not security), `fileMaxWidth Int`,
  `fileMaxHeight Int`.
- **Colour**: `rgb`, `rgba`, `white`, `black`, `transparent`.
- **Sub-modules**: `Background` (color, image, linearGradient,
  hoverColor/focusColor/focusVisibleColor/activeColor/disabledColor),
  `Border` (color, width, widthEach, rounded, solid/dashed/dotted,
  shadow, glow, innerShadow, hoverColor/focusColor/activeColor/
  hoverWidth/hoverRounded), `Font` (color, family, size, weight,
  bold/semiBold/regular/light/extraBold/black, italic, underline,
  noDecoration, letterSpacing, alignLeft/Right/Center/Justify,
  sansSerif/serif/monospace, hoverColor/focusColor/activeColor/
  disabledColor/hoverSize), `Region` (semantic landmarks routed
  to `<h1..h6>`, `<main>`, `<nav>`, `<aside>`, `<footer>`, aria-*),
  `Input` (button, text, multiline, email, username, search,
  currentPassword, newPassword, checkbox, radio, radioRow,
  slider), `Lazy` (LRU-cached subtrees, `SKY_UI_LAZY_CAP=N`),
  `Keyed` (sky-key for diff identity), `Responsive`
  (classifyDevice, adapt — Model-driven branching that needs a
  typed Msg dispatch).
- **Pseudo-classes** (`:hover` / `:focus-visible` / `:active` /
  `:disabled`) — per sub-module `on<State>` helpers above + generic
  `Ui.onPseudo : PseudoClass -> List (Attribute msg) -> Attribute msg`
  escape hatch for selector combinations no sub-module covers.
  `PseudoClass`: `Ui.hover`, `Ui.focus`, `Ui.focusVisible`,
  `Ui.active`, `Ui.disabled`. `focusColor` targets `:focus-visible`
  (safer default — only fires on keyboard nav, never on click-
  induced focus rings); use `Ui.onPseudo Ui.focus [...]` for
  sticky-focus behaviour. `:hover` rules are AUTO-WRAPPED in
  `@media (hover: hover)` by the runtime so they don't fire as
  sticky-hover on touch devices (the classic mobile "tap-and-stay-
  hovered" bug). Renders a sky-id-scoped `<style data-sky-pc=...>`
  child via the same pattern as media queries. Composes with
  `Ui.breakpoint` via natural nesting — breakpoint wraps the
  element, pseudo-rule attaches to the element inside.
- **Media queries + breakpoints** (`Ui.mediaQuery` / `Ui.breakpoint`
  / `Breakpoint` ADT) — CSS-driven viewport-conditional styling
  with instant CSS-engine reactivity (no JS round-trip, no Model
  field, no re-render). Typed `Breakpoint`: `Mobile`, `Tablet`,
  `Desktop`, `SmAndUp`, `MdAndUp`, `LgAndUp`, `XlAndUp`
  (Tailwind cuts), `DarkMode`, `LightMode`, `ReducedMotion`,
  `TouchDevice`, `Portrait`, `Landscape`, `Custom Int Int` (minPx
  maxPx; 0 = unset). `Ui.mediaQuery query [attrs] child` is the
  escape hatch for any raw CSS media-query string. Renders a
  wrapper `<div>` + a sky-id-scoped `<style>` child:
  `<style data-sky-mq="<sid>">@media <q> { [sky-id="<sid>"] { <rules> } }</style>`
  — two breakpoints on the same page can't cross-contaminate.
  Composes via nesting; Sky.Tui silently ignores `<style>`;
  Sky.Webview honours media queries identically to Sky.Live.
  Pick `Ui.breakpoint` when the layout transition needs no typed
  Msg; pick `Std.Ui.Responsive` when it does.
- **Transitions + animations** (`Std.Ui.Transition` /
  `Std.Ui.Animation` / `Std.Ui.Transform`) — typed CSS transitions
  + keyframe animations declared on a Sky.Ui element. The browser
  handles frame timing — no JS round-trip, no Model field. Both
  rules AUTO-WRAPPED in `@media (prefers-reduced-motion: no-preference)`
  by default for a11y; opt out via `Transition.attributeUnsafe` /
  `respectReducedMotion = False` on the Animation Spec ONLY when
  motion is semantically required (loading spinner, progress
  indicator). `Transition.attribute [property "background-color",
  duration 200, easing easeOut]` builds the CSS transition shorthand
  from typed `Step`s; pair with `Background.hoverColor` so the
  browser animates the change between base + `:hover` states.
  `Animation.attribute { name, duration, easing, delay, iterations,
  fillMode, respectReducedMotion, keyframes }` builds a keyframe
  spec; `keyframes : List (Int, List Transform.Prop)` is
  `[(percent, [Transform.opacity 0.0, Transform.translateY 10]),
  ...]`. `Transform.{translateX, translateY, translate, scale,
  scaleXY, rotate, skewX, skewY, opacity}` are the typed property
  helpers — `transform`-shaped ones join into ONE `transform:`
  shorthand per keyframe, `opacity` emits standalone. Two elements
  naming their animation `"fadeIn"` with different keyframes don't
  collide globally because the runtime auto-suffixes the
  @keyframes name with the element's sky-id-derived ident
  (`fadeIn__r_1_div_0`). Renders a sky-id-scoped
  `<style data-sky-tr=...>` + `<style data-sky-anim=...>` child via
  the same pattern as pseudo-classes / media queries.
- **Aspect ratio + grid tracks** (`Ui.aspectRatio` /
  `Ui.aspectRatioWH` / `Ui.square` / `Ui.widescreen` / `Ui.fullHd` /
  `Ui.cinemascope` + `Std.Ui.Grid.tracks` / `Grid.columns` /
  `Grid.rows`) — typed proportional sizing + explicit CSS-grid
  track lists. `Ui.aspectRatio 1.777` / `Ui.aspectRatioWH 16 9`
  lock an element to a width-to-height ratio (pair with
  `Ui.width Ui.fill` so the unset axis auto-scales). `Std.Ui.Grid`
  exposes a typed `Track` ADT (`fr`, `px`, `auto`, `minContent`,
  `maxContent`, `minmax`, `repeat`, `repeatAutoFit`,
  `repeatAutoFill`) + the attribute entry points; reach for it on
  sidebar layouts (`[fr 1, px 200, fr 1]`), content-aware columns
  (`[auto, fr 1]`), or responsive card grids
  (`[repeatAutoFit (minmax (px 240) (fr 1))]`). The lighter-weight
  `Ui.gridColumns N` (auto-fill `minmax(Npx, 1fr)`) stays for the
  common-case product-card grid. Both lower to inline CSS via the
  existing AttrStyle channel — no runtime injection pass.

| Need | Reach for |
|---|---|
| Square avatars, 16:9 video embeds | `Ui.square` / `Ui.widescreen` / `Ui.aspectRatioWH w h` |
| Custom decimal ratio (e.g. 2.35:1 cinemascope) | `Ui.aspectRatio Float` |
| Product-card grid (all tracks same min-width) | `Ui.gridColumns N` |
| Sidebar layout / mixed track types | `Std.Ui.Grid.columns [ fr 1, px 200, fr 1 ]` |
| Responsive card grid (re-flow on resize) | `Grid.columns [ Grid.repeatAutoFit (Grid.minmax (Grid.px 240) (Grid.fr 1)) ]` |
| Both axes set explicitly | `Grid.tracks cols rows` |

```elm
-- Mobile-first: column on phones, row above 768.
Ui.breakpoint Ui.mobile
    [ Ui.htmlAttribute "style" "flex-direction: column;" ]
    (Ui.row [ Ui.spacing 16 ] [ sidebar, main ])

-- Dark-mode background, no model field required.
Ui.breakpoint Ui.darkMode
    [ Background.color (Ui.rgb 18 18 24) ]
    pageBody

-- Raw query for cases no typed Breakpoint covers.
Ui.mediaQuery "(min-resolution: 2dppx)"
    [ Background.image "hero@2x.png" ]
    hero
```

### File / image upload pattern

```elm
Ui.input
    [ Ui.htmlAttribute "type" "file"
    , Ui.htmlAttribute "accept" "image/*"
    , Ui.onImage AvatarSelected           -- AvatarSelected : String -> Msg
    , Ui.fileMaxSize   2_000_000          -- 2 MB cap (browser-side)
    , Ui.fileMaxWidth  800                -- resize before upload (JPEG @ 0.85)
    , Ui.fileMaxHeight 800
    ]
```

Callback receives the data URL. Decode with
`Std.Encoding.base64Decode` → upload via `Http.post`. Ensure
`[live] maxBodyBytes` ≥ your `fileMaxSize`.

## Sky.Tui v1

A TEA backend rendering `Std.Ui` to ANSI cells. Same
`init`/`update`/`view`/`subscriptions` shape as `Sky.Live`.

```elm
type alias Cfg model msg =
    { init          : () -> (model, Cmd msg)
    , update        : msg -> model -> (model, Cmd msg)
    , view          : model -> Element msg
    , subscriptions : model -> Sub msg
    , onKey         : KeyEvent -> msg                  -- optional
    , guard         : msg -> model -> Result Error ()  -- optional; same as Live.app's guard
    , canvasWidth   : Int                              -- default 1280 logical px
    , canvasHeight  : Int                              -- default 720
    }

main = Tui.app cfg |> Task.run
```

**Logical-pixel canvas** — `canvasWidth × canvasHeight` defines
the design surface. Runtime computes `pxPerCell*` from terminal
size and converts `Ui.padding 8` / `Ui.px N` to cells. Default
1280×720 matches a typical web canvas.

**Coverage**: ~95 %+ of Std.Ui primitives. Unsupported attrs
(gradients, fine letter-spacing, image fills) emit a deduped
`tuiWarn`; `SKY_TUI_QUIET=1` suppresses. Wide chars (CJK + emoji
+ ZWJ) via `github.com/rivo/uniseg`. Bracketed paste capped at
1 MiB. Modified arrows (Ctrl/Shift/Alt) pass to user `onKey`.

**Reliability floor**: `safeGo` restores TTY on panic; external
signals (SIGTERM/SIGHUP/SIGQUIT/SIGINT) trapped → teardown →
`exit 128+signum`; `sanitiseRune` strips control bytes from user
text; `tuiMaxContentH = 50,000` hard cap with 10,000 soft warn;
`TERM=dumb` / non-TTY stdin refused with friendly error.

**Sky.Cli password mode** — `Cli.readPassword : () -> Task Error
String` reads stdin with echo disabled (`golang.org/x/term`'s
`ReadPassword`). Password never echoes; never lands in scrollback.

## Sky.Webview v0.1 (desktop)

Cross-backend mirror of `Live.app` + `Tui.app` — same TEA shape,
native desktop window via the system webview (WKWebView on macOS,
WebView2 on Windows, WebKitGTK on Linux) using `webview_go`. No
HTTP server, no SSE, no session store — the bridge is in-process
`Bind` + `Eval`.

```elm
import Std.Webview as Webview

main =
    Webview.app
        { init = init
        , update = update
        , view = view                  -- view : Model -> Element msg
        , subscriptions = subscriptions
        , window = { title = "Sky App", size = ( 800, 600 ) }
        }
        |> Task.run
```

Reuses Sky.Live's renderer (`HtmlToVNode`, `assignSkyIDs`,
`renderVNode`, `diffTrees`) — same `view` function paints
identically across Sky.Live (web), Sky.Tui (terminal),
Sky.Webview (desktop). XSS hardening parity:
focus-preserving DOM replacer, `__skyReviveScripts` for
late-injected `<script>` tags.

`WindowCfg` is closed (`{ title : String, size : (Int, Int) }`)
in v0.1 for clean missing-field type errors. v0.2 reopens it for
`alwaysOnTop` / `transparent` / `decorated` and adds tray icons,
global hotkeys, native file dialogs, and Windows + Linux smoke
validation. v0.1 ships macOS only.

Sky-stdlib path: `sky-stdlib/Std/Webview.sky`. Runtime: `runtime-go/rt/webview.go` (build tag `cgo && darwin` for v0.1; widens v0.2 with smoke for Linux/Windows). Stub at `webview_stub.go` covers `!cgo || !darwin` so non-macOS builds link cleanly and surface a runtime `Err Error` on call. Example: `examples/31-webview-stopwatch-ui`.

**`sky build` cgo-detect.** Normally `sky build` runs `CGO_ENABLED=0 go build` first (static-binary preference) and only retries with cgo on failure. When the emitted `main.go` contains `rt.Webview_app` (i.e. the project uses Sky.Webview), the build runner flips straight to `CGO_ENABLED=1` on the first attempt — otherwise the stub would compile cleanly and the resulting binary would silently exit at runtime. Look for `(built with cgo — Sky.Webview requires it; …)` in the build log to confirm.

**Std.Ui convention** — your `view` function MUST wrap its output in `Ui.layout [] (...)` to convert `Element` → `Html` before the renderer (`HtmlToVNode`) processes it. A raw `Ui.column [...]` body produces a blank window. Same convention as Sky.Live (see `examples/19-skyforum`, `examples/26-ui-showcase`).

## Language syntax

```elm
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Log exposing (println)

type Msg = Increment | Decrement

update : Msg -> Int -> Int
update msg count =
    case msg of
        Increment -> count + 1
        Decrement -> count - 1

main =
    println (String.fromInt (update Increment 0))
```

`|>` `<|` pipelines | `::` cons | `\x -> x + 1` lambdas | `let…in`
| `case…of` (exhaustiveness checked) | `{ record | field = value }`
update | `module M exposing (..)` | `import M as Alias exposing (func)`.

**Multiline strings** — triple-quoted, `{{expr}}` interpolation:

```elm
html = """<div class="card">
    <h1>{{title}}</h1>
    <p>{{description}}</p>
</div>"""
```

Single `{` is literal. Interpolation expressions can be
identifiers, field access (`{{record.field}}`), qualified names
(`{{String.fromInt n}}`), or function calls.

Escape with backslash: `\{{` emits a literal `{{` (no interpolation).
Use this to ship Mustache / Handlebars / shell-script placeholders to
downstream tooling without Sky hijacking them. `\\` collapses to a
single literal backslash; other `\X` sequences are preserved verbatim
(regex `\d+`, paths `\test`, etc).

## Active limitations

Real current compiler limitations users must work around. v0.15
closed several earlier-listed items; the surviving list below is
verified against HEAD.

1. **No higher-kinded types.** HM only.
2. **No `where` clauses.** Use `let…in`.
3. **No custom operators.**
4. ~~**Negative literal arguments need parens.**~~ — CLOSED in
   v0.17 (Limitation #4 / task #632).  `f -1` now parses as
   `f (-1)` — the application-argument parser peeks one char
   ahead and admits `-<digit>` (no whitespace between) as a
   unary-negate prefix introducing a negative-literal argument.
   `f - 1` (spaces around `-`) remains binary subtraction.
   Identifier-shaped negatives (`f -x` for negate-of-x) still
   require parens because Sky has no unary-negate operator on
   bindings.  Closes `Math.atan2 0 -1`, `Time.addDays today -1`,
   `Stripe.backoff 100 -50`, etc.  Regression:
   `Sky.Parse.NegativeLiteralArg` (5 cases).
5. ~~**`Dict.toList` typed-key inference is inline-only.**~~ —
   CLOSED in v0.17 PR-23 verification.  Both inline
   (`Dict.toList (Dict.fromList [(1, "a")])`) and let-bound
   (`let d = Dict.fromList [...] in Dict.toList d`) routes through
   the typed `rt.Dict_toListIntKey` path and emits the typed
   `[]rt.T2[int, string]` return.  The let-region propagation fix
   landed via v0.17 PR-13 structural σ and the per-region typed
   GoType pipeline (PR-5).  No workaround needed.
6. ~~**`sky check` does not fully model Go interface satisfaction.**~~
   — CLOSED in v0.17 (Limitation #6 / task #633).  The PR-21b axiom
   in `Sky.Type.Unify` (`isFfiInterfacePair` + `implementsInterface`)
   reads the Inspector's `implements` registry to admit qualified
   pairs where one side implements the other via Go's structural
   interface satisfaction — both directions are treated as
   widenings to preserve HM principal types.  Empirically verified
   against every real-world FFI surface:
   * Fyne — `Widget.Label@fyne` implements `CanvasObject@fyne` ✓
     (used in `examples/11-fyne-stopwatch`)
   * Stripe — `Iter@stripe/customer` / `Client@stripe/customer`
     implements `Token@encoding/json` ✓ (used in
     `examples/13-skyshop`)
   * google.golang.org/api/iterator — `Pager` / `PageInfo`
     implements multiple ✓
   * base64 — `CorruptInputError` / `Encoding` implements `Token`,
     `Ordered`, etc. ✓
   * firebase, http, io, json, strings, context — all NULLARY opaque
     pairs ✓
   The "concrete-satisfies-interface checks fall through" speculation
   from the original CLAUDE.md note never materialised in practice
   because Go's interface system uses concrete (nullary) interface
   names and the Inspector flattens parametric Go generic interfaces
   to their nullary form for the implements table.  Regression
   gates: `examples/11-fyne-stopwatch` (Fyne Label → CanvasObject)
   and `examples/13-skyshop` (Stripe `Iter` → `Token`) clean-build
   sweep.  Should a parametric Go-generic interface case ever
   appear in a real FFI surface, the axiom needs extending to
   walk arg lists pairwise — track via a fresh limitation if it
   surfaces.
7. ~~**Zero-arg calls follow the binding's declared type, not its
   FFI-vs-kernel origin.**~~ — CLOSED in v0.17 (Limitation #7,
   PR-A through PR-D).  The strict-HM arity gate now fires at
   `constrainCall` (k-a + u-a, calling `: T` with `()`) AND at
   the `Can.VarKernel` / `Can.VarTopLevel` arms (k-b + u-b,
   bare reference of `: () -> X` in a non-arrow value slot)
   with the actionable `[E2007]` diagnostic ("declared as D-arg,
   called with S args").  See
   `### Closed in v0.17 (kept here for grep)` below for the
   full PR-A→PR-D commit log + spec gates.

   **v0.15.50 mitigation — `Sky.Core.Pure`.** New code targeting
   a uniform `() -> Task Error a` shape can import
   `Sky.Core.Pure as Pure` and call the additive companions
   (`Pure.uuidV4 ()`, `Pure.timeNow ()`, etc.). Existing names +
   shapes unchanged. Pure.* lowers to the canonical kernel with
   typed `SkyTask[Error, T]` shape.
8. ~~**Recursive list ops grew the Go stack O(N).**~~
   — CLOSED in v0.17 (Limitation #8, 13/13 list ops on constant
   Go stack).  See `### Closed in v0.17 (kept here for grep)`
   below for the per-op commit log and spec gates.
9. ~~**Zero-arg `Css.*` keyword constants require `()`**~~ —
   CLOSED in v0.17 PR-26.  `Css.zero` / `Css.auto` / `Css.none`
   / `Css.transparent` / `Css.currentColor` / `Css.systemFont`
   are now bare-value constants (`: Length` / `: Color` /
   `: String`) — call them WITHOUT `()`.  The v0.17
   canonicaliser tightening (PR-19) closed the original Go
   init() ordering interaction that motivated the `() -> X`
   workaround, so pure-literal constants ship as plain values
   matching Sky's stdlib convention (`Dict.empty`,
   `Maybe.Nothing`, etc.).
10. ~~**Multi-line function signatures.**~~ — CLOSED in v0.17
    PR-25 / #628.  Both forms parse cleanly:
    `name\n    : T` (colon on continuation line) AND
    `name\n    : T1\n    -> T2\n    -> T3` (arrow on continuation
    line, off-side-rule indented).  `typeAnnotation` retries with
    `freshLine + checkIndent + "->"` when the arrow doesn't appear
    on the current line.  No workaround needed.
### Closed in v0.17 (kept here for grep)

- ~~**α-renamed T9000-space synth-vars leak into `rt.Coerce[T9001]`
  slots — `go build` rejects with `undefined: T9001` for polymorphic
  `Cfg msg` view functions that let-bind an `msg`-typed field.**~~
  — CLOSED in v0.17.2 (`38cde3e6` on `fix/v0.17.2-tvar-substitution-leak`).
  Root cause: `identityRecovered` in `coerceCallArgsAt`'s fallback
  arm (`Compile.hs:16712-16721`) — added in v0.16.13 #530 for
  parametric-record-alias args — self-pinned every tvar in the
  α-renamed paramType to itself. When `alphaRenameCalleeTVars`
  (Compile.hs:16657) had moved the callee's T1/T2 into fake
  9000-space (T1 → T9001) for caller-scope isolation, identity
  recovery re-anchored T9001 to itself, `substituteOnly` treated
  it as "bound", the erase-scoped fallback (which would have
  widened to `any`) was skipped, and the raw `T9001` shipped into
  the emitted Go. Fix: one filter clause on `identityRecovered` at
  `Compile.hs:16743` —

  ```haskell
  , enclosingTypeParamInScopeCtx ctx tv
  ```

  Identity is only sound when Monomorphise's
  `substTypeParamsInString` has a live caller tvar to rewrite.
  α-renamed synth-vars (never in caller scope) fall through to
  `substituteOnly`'s `outOfScope → eraseScopedCtx` widening to
  `any`. Go's call-site inference then pins the callee's generic
  consistently across sibling args. Sibling recovery arms
  (`bareRecovered` / `structuralRecovered`) already had strong
  pinning guards; the identity branch was the outlier.

  Non-regression: Issue #521 fixture (`docs/v0.16.x-console/parametric-cfg-repro/`)
  still preserves `Widget_Cfg_R[T2]` casts through Monomorphise's
  `Widget_view__Msg_Msg_...(cfg Widget_Cfg_R[Msg])` specialisation.
  Companion specs `UnannotatedParametricCfgView` +
  `UnannotatedParametricCfgUserHelper` still PASS.

  Regression spec: `Sky.Build.TVarSubstitutionLeakSpec` (3
  examples — build-clean fixture, no 4-digit T leak, runtime
  output correct). Verification: 981 cabal examples / 0 failures
  / 6 pending; 26/26 example sweep; skydeploy control-plane
  Editor_view (the shape that blocked v0.17.1 → skydeploy) builds
  clean.

- ~~**Zero-arg calls follow the binding's declared type, not its
  FFI-vs-kernel origin.**~~ — CLOSED in v0.17 (Limitation #7,
  multi-PR plan PR-A through PR-D landed across iter 29-32).  The
  strict-HM arity gate at `src/Sky/Type/Constrain/Expression.hs`
  rejects both shape classes empirically:
  * **k-a / u-a** — calling `: T` with `()` at a Call site.
    `println (Uuid.v4 ())` (where `Uuid.v4 : Task Error String`,
    declared 0-arg) rejects with `[E2007] Arity mismatch —
    \`Sky.Core.Uuid.v4\` declared as 0-arg, called with 1 args.`
  * **k-b / u-b** — bare reference of `: () -> X` in a non-arrow
    value slot.  `doNow : Task Error Int; doNow = Time.now`
    rejects with `[E2007] Arity mismatch — \`Sky.Core.Time.now\`
    declared as 1-arg, called with 0 args.`
  Multi-PR commit log (each gate flipped a prior `pendingWith` arm
  in `Sky.Type.StrictHmArityGateSpec` to live `CompileErr`):
  * PR-A — `ccf3c010` iter 29: `CArityMismatch` constraint
    constructor + `Sky.Type.Solve.solveHelpBody` arm +
    `countConstraints` arm + diagnostic code `E2007`.  Scaffolding
    only — proves the constructor is reachable end-to-end.
    Regression spec `Sky.Type.ArityMismatchScaffoldSpec` (6
    gates).
  * PR-B — `53d529f4` iter 30: pure
    `Sky.Type.Constrain.Expression.declaredArity ::
    T.Annotation -> Int` helper (structural TLambda peel; no
    fresh UF vars; no solver interaction).  Cross-module
    externals trace verified safe — `globalExternals` /
    `globalSameModAnnots` annotations are post-canonicalisation
    so head-alias unfold (PR #123) has already peeled the
    TAlias.  Regression specs
    `Sky.Type.DeclaredArityHelperSpec` (9 unit tests) +
    `Sky.Type.StrictHmArityGateSpec.h-a-cross` (cross-module
    HeadAlias positive).
  * PR-C — `d1394fbc` iter 31: gate wired at `constrainCall`.
    New `arityGateCall` + `arityGateForKernel` +
    `arityGateForTopLevel` + `maybeEmitArityMismatch` compose
    PR-A's constructor with PR-B's helper.  Emit at CAnd index 0
    so the [E2007] diagnostic short-circuits legacy CEqual.
    Wildcard-`any` filter (`any (/= "any") freeVars`) preserves
    real polymorphism on the v0.15.1 CForeign per-call-site
    re-instantiation path.  k-a + u-a flipped from pendingWith
    to live.  Companion
    `Sky.Type.Limitation7CurrentLooseAcceptanceSpec.u-a` diagnostic
    upgraded from generic "Variable 'foo' type mismatch" to
    actionable [E2007].
  * PR-D — `389883cb` iter 32: gate wired at the
    `Can.VarKernel` / `Can.VarTopLevel` arms.  New
    `valueSlotGateForKernel` / `valueSlotGateForTopLevel` /
    `maybeEmitValueSlotMismatch` + `SlotShape` ADT
    (`Unknown` / `Arrow` / `Value`).  Classification rule:
    expected payload's structural shape decides — TVar → skip
    (slot type unresolved); TLambda / TAlias-unfolding-to-TLambda
    → skip (slot wants a function); TType / TRecord / TTuple /
    TUnit → fire when D >= 1 (slot wants a value, our declared
    `: () -> X` is a function).  k-b + u-b flipped from
    pendingWith to live.
  Strict-HM gate spec final state: `Sky.Type.StrictHmArityGateSpec`
  9 examples / 0 failures / **0 pending** — 5 live POSITIVE
  assertions (h-a HeadAlias unfold / p-a Pure.* canonical / wp-a
  real polymorphism / h-a-cross cross-module HeadAlias / wa-a
  wildcard-only soundness) + 4 live NEGATIVE assertions (k-a /
  k-b / u-a / u-b).  Companion
  `Sky.Type.Limitation7CurrentLooseAcceptanceSpec` 6/0.
  Real-world stress gates: `examples/13-skyshop` (76k FFI symbols)
  + `examples/26-ui-showcase` (rt.Coerce=288 + rt.AsListT=190 at
  floor) both clean-build with no false positives.
  Implementation note: the v0.15.50 `Sky.Core.Pure` mitigation
  surface stays as the canonical Pure.* companion for new code
  targeting a uniform `() -> Task Error a` shape; it pairs with
  the strict gate so the actionable diagnostic now points users
  at the right call shape.

- ~~**Recursive list ops grew the Go stack O(N).**~~
  — CLOSED in v0.17 (Limitation #8, 13/13 list ops on constant
  Go stack). All thirteen previously-recursive operations across
  `Sky.Core.List` / `Sky.Core.Maybe` / `Sky.Core.Result` now
  compile to constant Go stack via CPS / accumulator rewrites
  (paired with the auto-TCO tail-call optimiser in
  `Sky.Build.TailCallOpt`). Per-op closure log:
  * `List.map` — CPS rewrite, `8e5dbd4f`
  * `List.filter` — CPS rewrite, `a0b63e4e` (FilterSpec)
  * `List.foldr` — delegation rewrite, `5b4bc25b` (FoldrSpec)
  * `List.concat` — CPS rewrite, `23672c00` (ConcatSpec)
  * `List.take` — CPS rewrite, `ebf79807` (TakeSpec)
  * `List.append` — CPS rewrite, `243067f2` (AppendSpec)
  * `Maybe.combine` — delegation rewrite, `d3039da7`
    (MaybeCombineSpec)
  * `Result.combine` — CPS rewrite, `e4dc625b`
    (ResultCombineSpec)
  * `List.length` — CPS rewrite, `c274ecaf` (LengthSpec)
  * `List.range` — CPS rewrite, `5be2702d` (RangeSpec)
  * `List.zip` — CPS rewrite, `538daed6` (ZipSpec)
  * `List.indexedMap` — CPS rewrite, `8ac38af0` (IndexedMapSpec)
  * `List.concatMap` — direct-accumulator rewrite, iter 27
    (ConcatMapSpec — `concatMap fn list =
    reverseHelp (concatMapHelp fn list []) []` with
    `concatMapHelp fn list acc` walking left-to-right and
    reverse-prepending each `fn x` chunk onto `acc`; the natural
    delegation `concat (map fn list)` triggers HM cross-module
    over-unification on polymorphic `map` instances, so the
    direct accumulator is the correct fix).
  Per-op specs live under `test/Sky/Build/CpsStackConstantBound/`
  and assert the rewritten body emits the auto-TCO marker pattern
  (no recursive Go-stack growth). For huge inputs (1M+ elements)
  the primitives now stay O(1) on Go stack — the historical
  "200k+ elements → stack overflow" workaround note is no longer
  needed. Closes Limitation #8 in full.

### Closed in v0.16 (kept here for grep)

- ~~`Std.Db.exec` / `Std.Db.query` reject mixed-type parameter lists
  (`[String, Maybe Int, Bool]` fails HM with E2001 because List is
  homogeneous); no workaround that doesn't violate the no-stringify /
  no-Ffi.toAny rule~~ — closed in v0.16.26 (#582). New `SqlValue` ADT
  in `Std.Db`: `SqlString | SqlInt | SqlFloat | SqlBool | SqlBytes |
  SqlDecimal | SqlTime | SqlMoney | SqlNull SqlValue` (9 variants
  total, recursive SqlNull carries a type-witness for typed NULL
  binding). `List SqlValue` flows through `Db.exec` / `Db.query`
  with full per-column type fidelity to the driver. 8 `fromMaybe*`
  helpers cover Maybe-lifting without inline case-of. Money
  round-trips via `"ISO_CODE AMOUNT"` TEXT (paired with
  `Db.Decode.money` on the read side). Example:
  ```
  Db.exec conn
      "INSERT INTO orders (id, customer, total, paid_at) VALUES (?, ?, ?, ?)"
      [ SqlInt orderId
      , SqlString customerUuid
      , SqlMoney total
      , fromMaybeTime maybePaidAt
      ]
  ```

- ~~Partial-update / PATCH semantics need three states per field
  (explicit value / explicit NULL / column omitted) but Db.exec only
  models the first two; the "leave alone" state required generating
  different SQL strings outside the stdlib~~ — closed in v0.16.26
  (#582). New `SqlField` ADT (`SetField SqlValue` / `OmitField`) +
  `Db.updateFields db table whereCols setFields` builder generates
  dynamic UPDATE that includes only `SetField` columns. Column-name
  validation rejects identifiers outside `[A-Za-z0-9_.]` so the
  dynamic SQL can't open an injection vector. All-OmitField
  short-circuits to 0 rows (no empty SET clause).

- ~~`Std.Db.exec` / `Std.Db.query` reject `Maybe a` params at the
  database/sql driver layer ("unsupported type rt.SkyMaybe[int],
  a struct")~~ — closed in v0.16.24 (#574). Runtime `dbBindArg`
  helper reflect-walks any arg with the SkyMaybe shape and
  substitutes `nil` for Nothing / the unwrapped value for Just.
  Applied at both `Db_exec` and `Db_query` binding sites;
  `Db_queryDecode` inherits via `Db_query`. Now you can write
  `Db.exec conn "INSERT … VALUES (?, ?)" [ Just "Alice", Nothing ]`
  and Nothing binds as SQL NULL.

- ~~`import Std.Db.Decode exposing (Decoder, ...)` errors with
  "module Std.Db.Decode does not expose type Decoder" — but
  Decoder is globally available as a kernel-implicit Prelude
  type~~ — closed in v0.16.24 (#576). `Canonicalise.Module`
  `checkItem` now accepts 15 kernel-implicit Prelude types in
  `exposing (...)` lists as a no-op when the dep module doesn't
  declare them: `Decoder`, `Value`, `Attribute`, `Handler`,
  `Middleware`, `Session`, `Store`, `Route`, `VNode`, `Request`,
  `Response`, `Cmd`, `Sub`, `Db`, `Error`. The import is
  redundant (the names are already globally in scope) but no
  longer rejected. Regression: `ExposingSpec` "#576: kernel-
  implicit Prelude type re-exposure".

- ~~`Std.Db.Decode.nullable` requires double-naming the column
  (`nullable "age" (int "age")`); silently mis-gates when the
  two column names differ~~ — closed in v0.16.24 (#577). **Breaking
  signature change**: `nullable : Decoder a -> Decoder (Maybe a)`
  (drops the leading column-name arg). `DbDecoder` gains a `cols`
  field that primitive decoders populate; combinators (`map`,
  `andMap`, `map2..5`, `andThen`) propagate via `dbUnionCols`.
  `nullable` checks all of inner's columns for NULL/absent before
  delegating — handles both single-column and composed-decoder
  cases. Migration:
  ```
  -- before: Decode.nullable "age" (Decode.int "age")
  -- after:  Decode.nullable (Decode.int "age")
  ```

- ~~Sky.Live runtime: sky-nav click + popstate (Back/Forward)
  handlers don't check `r.ok` before passing the fetch body to
  `__skyPatch`. A 404 "session not found" body (server lost our
  session_id store entry — TTL expiry, store-restart, store-
  config change, cross-deploy cookie collision) gets passed
  verbatim to `__skyPatch` and replaces the entire `<body>` with
  plain text — user sees "session not found" as the whole page
  in a serif font, indistinguishable from a generic crash~~ —
  closed in v0.16.16. Both .then chains in `liveJSWithCfg…`
  gate on `r.ok` before invoking `__skyPatch`; on non-OK the
  click path navigates to the link URL (`window.location.href =
  href`) and the popstate path reloads the current URL — both
  trigger the runtime's initial-page handler which always
  succeeds (GET / creates a fresh session when the cookie is
  invalid). Regression: `TestSkyNavFetchChecksOk` verifies the
  embedded JS contains ≥2 `if (!r.ok)` occurrences. The recovery
  behaviour stays reload (universal sane default — works for
  apps with no auth, with auth, with stateful cart/cookie state).
  Apps that need richer behaviour can opt-in to a configurable
  `onSessionLost` cfg field — design tracked but not shipped in
  v0.16.x; reload is the floor.

- ~~Unannotated cross-module `view : Cfg msg -> Element msg`
  miscompiles to `any(cfg).(Cfg_R[any])` casts that panic at
  runtime~~ — closed by Issue #521.  The lowerer now pushes the
  enclosing Go function's typeParams into `LowerCtx` via
  `withScopedEnclosingTypeParams` (Compile.hs) BEFORE the body's
  GoExpr tree is constructed.  `substituteOnly`'s erase-fallback
  consults the scope via `enclosingTypeParamInScope`; in-scope
  TVars are preserved through `eraseTypeParamsExceptScope` so the
  monomorphiser's token-level `substTypeParamsInString` can
  rewrite them per call-site instantiation.  Same fix closes the
  broader `Foo_R[any]`-cast-panic class for parametric record
  aliases (sibling family: #261/#262/#263/#461/#463/#465/#467).
  Regression: `Sky.Build.UnannotatedParametricCfgViewSpec`.

### Closed in v0.15 (kept here for grep)

- ~~Head-position type alias of a function signature dropped
  parameters at canonicalisation~~ — closed in v0.16.4 via
  contributor PR #123 (arthurmaciel). `arrowArgs` / `arrowResultN`
  used to peel only `TLambda`, so `view : Renderer Msg` over
  `type alias Renderer msg = Model -> Element msg` (canonical Elm
  syntax) failed because the alias-reference is a nominal `TType`
  at that point. New `unfoldHeadAlias` in
  `Sky.Canonicalise.Module` peels a `TAlias` at the head of the
  annotation before the split, with a visited-set guarding mutual
  recursion. Head-only: argument / return leaf types keep their
  nominal form, so existing typed lowering of ordinary
  `f : Rec -> String` signatures is byte-identical.
  `Sky.Http.Server.Handler` moved here as its canonical home
  (was in `Sky.Http.Middleware` per v0.16.3 #464); Middleware
  imports it from Server. AI-written code can now write
  `myHandler : Handler` directly. Regression:
  `Sky.Canonicalise.HeadAliasFunctionSig` (5 cases).
- ~~Cons-pattern length-guard shared between arms (#402)~~ —
  closed in v0.15.54. The codegen previously emitted only
  `len(subj) >= 1` per cons step, so `a :: b :: c :: _` and
  `a :: b :: _` shared the same `>= 2` guard, letting a
  2-element list enter the longer arm and panic at
  `IndexOutOfRange`. New `consChainLength` walks the chain to
  emit the correct `>= N` / `== N` per arm.
- ~~Same-named local lambdas across modules pollute the typed
  lowerer's region snapshot~~ — closed in v0.15.30 via the
  scoped `LowerCtx` cascade. Per-module env ledger
  (`Solve.SolvedTypes._stPerModuleEnv`) consulted via
  `lookupSolvedVarScoped` at each lookup site; sentinel
  `GoDeclRaw` entries bracket each dep's `[GoDecl]` to switch
  `globalCurrentDepModule` during render. Multi-modules can now
  share `let encodeOne x = …` shapes without typed-codegen
  cross-contamination.
- ~~Anonymous records in function signatures~~ — closed in v0.13
  (`processReq : Int -> { name : String, age : Int } -> String`
  parses cleanly).
- ~~Let bindings with parameters after multi-line case~~ — `let
  mark j = …` after a `case … of` arm now parses.
- ~~Zero-arity functions reading env vars memoised at init()~~ —
  `apiKey = System.getenvOr "K" "def"` now reads runtime env.
- ~~`exposing (Type(..))` for user-module ADT constructors~~ —
  user `type Color = Red | Green` exporting `Color(..)` and
  imported `exposing (Color(..))` now exposes unqualified
  constructors.
- ~~`import X as Alias` leaks the alias into codegen~~ —
  `import Lib.Db as Chat` now emits `Lib_Db_Message_R` based on
  the source module, not the alias.
- ~~`let` bindings don't support forward references~~ — `let a = b
  + 1; b = 5 in a` now compiles and evaluates correctly.
- ~~Parametric record alias bugs (Surfaces 1, 2, 3)~~ — closed
  by v0.15 type-directed lowering + Go generics on parametric
  records. See `docs/v1-rfc/type-soundness-deep-analysis.md` for
  the full architecture write-up.
- ~~Same-module polymorphic call pinned by first instantiation~~
  — sibling refs to polymorphic annotated TypedDefs now alpha-
  rename per call site (`f : Cfg msg -> msg` called with `msg=Int`
  and `msg=Bool` in the same module both work).
- ~~Wildcard-`any` return type silently accepted against typed
  slot~~ — `view : Model -> any` returning a String against an
  expected `Model -> Html msg` now correctly surfaces as a type
  error (v0.15.1 same-mod CForeign wildcard-gate fix).
- ~~Unknown qualified name (`NotARealModule.foo`) silently passed
  canonicaliser~~ — closed in v0.15.42 (audit §3.1). The
  canonicaliser now flags any qualified ref whose qualifier is
  neither a kernel module, an import alias, nor present in
  `_qualVars`/`_qualCtors`, with a Did-you-mean suggestion via
  Levenshtein distance. Pre-fix Sky printed "Compilation successful"
  and `go build` then rejected with `undefined: NotARealModule_foo`.
- ~~"Compilation successful" printed before `go build` ran~~ —
  closed in v0.15.42 (audit §3.4). Sky lowering prints
  "Sky lowering succeeded"; "Compilation successful" only fires
  after Go returns 0. Failure path prints "Sky lowering succeeded
  but `go build` failed:" before the Go diagnostic.
- ~~User `type Result a = Just a | Nothing` silently shadows the
  Prelude-exposed Maybe/Result constructors~~ — closed in v0.15.42
  (audit §3.2). The canonicaliser now rejects any user-defined ADT
  whose type name OR constructor name collides with a Prelude-
  exposed entry (Int/Float/Bool/String/Char/List/Maybe/Result/Task/
  Error/True/False/Just/Nothing/Ok/Err) with a hard error naming
  the canonical stdlib origin.

## Workflow rules

- **Always run mem-guard.** See "Non-negotiables" above.
- **Always clean up background tasks** before declaring "done".
- **`sky fmt` after editing `.sky` / `.skyi` files.** Two passes
  are byte-identical (formatter is idempotent).
- **`-f` flag with `rm` / `cp`** to avoid interactive prompts.
- **Never add co-author wording** to commits.
- **Never tag a release** without explicit user ask.
- **Never run `sky build` from the repo root** — overwrites the
  compiler binary in `sky-out/`.
- **Cancel in-progress CI runs on `main` before pushing** (newer
  commit supersedes them; never cancel release/tag runs).

```bash
gh run list --branch main --status in_progress --workflow CI --json databaseId --jq '.[].databaseId' \
    | xargs -I{} gh run cancel {} 2>/dev/null
git push origin main
```

## Project layout

```
rust/                             -- Sky compiler (Rust, PRIMARY — cargo workspace)
  crates/sky/                 -- CLI + `sky` binary entry point
  crates/syntax/                  -- lexer + parser
  crates/hir/                     -- name resolution, canonicalisation
  crates/ty/                      -- HM inference, exhaustiveness
  crates/lower/                   -- type-directed lowering + IR
  crates/codegen/                 -- Go IR + printer
  crates/ffi/                     -- FFI binding generation
  crates/fmt/                     -- opinionated formatter (Elm-compatible)
  crates/sky-lsp/                 -- language server
  crates/xtask/                   -- gate suite (roundtrip/resolve/infer/…/golden)
  rust-toolchain.toml             -- pinned Rust toolchain
legacy-haskell-compiler/          -- retired Haskell compiler (historical)
  src/Sky/…                       -- former Parse/Canonicalise/Type/Build/…
  app/Main.hs                     -- former CLI entry point
  sky-compiler.cabal              -- former cabal project
runtime-go/rt/                    -- Go runtime (embedded)
sky-stdlib/                       -- Sky-side stdlib (embedded)
sky-bundled/console/              -- Sky Console mini-app
sky-bundled/doc/                  -- sky doc HTTP server mini-app
tools/sky-ffi-inspect/            -- Go package introspector (TH-embedded)
templates/CLAUDE.md               -- Template for `sky init` projects
examples/                         -- 27 example projects
docs/                             -- User + contributor documentation
```

## Template sync (non-negotiable)

When stdlib, syntax, Sky.Live APIs, or CLI commands change,
**`templates/CLAUDE.md`** + **the matching `docs/*` files** MUST
be updated in the same commit. AI assistants use these to write
Sky code in user projects.

| Concern | User doc |
|---|---|
| Stdlib reference | `docs/stdlib.md` |
| `Std.Auth` | `docs/skyauth/overview.md` |
| `Std.Db` | `docs/skydb/overview.md` |
| Sky.Live runtime | `docs/skylive/overview.md` + `docs/skylive/architecture.md` |
| `Std.Ui` | `docs/skyui/overview.md` |
| Sky.Tui | `docs/skytui/overview.md` |
| CLI commands | `docs/tooling/cli.md` |
| LSP capabilities | `docs/tooling/lsp.md` |
| `sky.toml` schema | `docs/sky-toml.md` |
| Brand-new module | "What's in the box" in `README.md` |
