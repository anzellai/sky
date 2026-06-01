# CLAUDE.md

> **Quick orientation.** Sky is an Elm-family functional language compiling to
> typed Go via a Haskell compiler (GHC 9.4.8). The current branch ships
> v0.15.x: type-directed lowering throughout (lambdas, record-field inits,
> list elements, call args all lower with the slot's typed Go form
> propagated), Go generics on parametric record aliases
> (`type Cfg_R[T1 any] struct{...}`), same-module polymorphic call
> re-instantiation, and the wildcard-`any` soundness fix. Layer-3 stdlib
> migration, `Ffi.kernel`, auto-TCO, `sky doc` all carry forward from
> v0.14. The Phase B verification sweep
> (27 examples + 120 Sky.Test assertions + 306 cabal specs) is the source
> of truth — green-everywhere is a hard release gate.

## Current state

| Surface | Status |
|---|---|
| **v0.15 type-directed lowering** (Stages A → F) | ✅ shipped — `Compile.hs` `globalRegionTypes` + `LowerCtx` |
| **v0.15 Go generics on parametric record aliases** | ✅ shipped — `Cfg_R[T1 any]` + per-instance type args |
| **v0.15 same-module polymorphic re-instantiation** | ✅ shipped — sibling refs to polymorphic annotated TypedDefs alpha-rename per call site |
| **v0.15 wildcard-`any` soundness gate** | ✅ shipped — same-mod CForeign now gates on non-`any` freeVar |
| Layer 3 stdlib (Sky source for every kernel module) | ✅ shipped — `sky-stdlib/{Sky/Core,Std,Sky/Http}/*.sky` |
| `Ffi.kernel` mechanism (Sky-source bindings route to typed kernel dispatch) | ✅ shipped |
| Auto-TCO for tail-recursive Sky functions | ✅ shipped |
| `sky doc` (terminal + HTTP server with type-sig + Markdown + fuzzy search) | ✅ shipped |
| `sky watch` / `sky doctor` / `sky console` / `sky upgrade-claude` | ✅ shipped |
| Sky Console + sub-app mount + observability federation | ✅ shipped |
| Sky.Webview v0.1 (desktop, macOS) — `Webview.app cfg` via `webview_go` | ✅ shipped — `runtime-go/rt/webview.go`, `sky-stdlib/Std/Webview.sky` |
| 32-example sweep + 120 Sky.Test assertions + 410+ cabal specs | ✅ green |

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

### 1. Memory safety — `scripts/mem-guard.sh` MUST run during dev

A runaway `sky` / `cabal` / `ghc` / `haskell-language-server` process
has previously force-powered-off the host Mac. Treat the absence of
mem-guard like a missing `set -e`.

```bash
nohup ./scripts/mem-guard.sh > /tmp/mem-guard.out 2>&1 &
disown                                # survives shell exit
```

Defaults (16 GB Mac): per-process kill at 6 GB RSS for compiler
tooling (`sky` / `cabal` / `ghc` / `ghc-iserv` / `cc1` / `ld` /
`haskell-language-server` / `hls-wrapper` / `gopls` /
`sky-ffi-inspect`); 10 GB panic tier for the dev-session host
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
- **`cabal test` MUST run under `timeout`**:
  `timeout 3600 cabal test` (60 min hard ceiling). If 60 min is
  not enough, that's a flaky test — bisect it, don't widen the
  ceiling.
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

Periodic hygiene (set a Calendar reminder if you run sweeps
daily): `go clean -cache` weekly. Worktree dir cleanup after EVERY
agent cherry-pick.

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

### 8. Non-regression rules (enforced by `cabal test`)

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
| Tail-recursive (auto-TCO) | `foldl`, `find`, `any`, `all`, `member`, `drop`, `reverseHelp`, `indexedMapHelp` | Compiles to `for { ... continue }`; constant stack |
| Non-tail-recursive (pure Sky) | `map`, `filter`, `foldr`, `length`, `concat`, `concatMap`, `take`, `append`, `range`, `zip`, `indexedMap`, `Maybe.combine`, `Result.combine` | O(N) stack — fine for typical UI lists; theoretical risk at 1M+ elements. For huge inputs prefer `foldl`-based accumulator patterns. |

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
  `dist-newstyle/`, `node_modules/`, `.git/`) excluded.
- Build-error policy: on a failing rebuild, the previously-running
  binary stays alive. The next successful build kills + respawns.
- Caches: `.skycache/source.hash` (full short-circuit on
  unchanged source), `.skycache/lowered/` (per-module IR),
  `.skycache/ffi/*.skyi` (HM types — never regenerated; explicit
  `sky add/install` step). Typical warm rebuild: 1-3 s.

### Release checklist (non-negotiable)

1. Rebuild: `cabal install --overwrite-policy=always --installdir=./sky-out --install-method=copy exe:sky`
2. Smoke-test: `sky-out/sky --version` (must print version, not start a server)
3. Cabal test sweep: `cabal test` — zero failures, pending count matches prior
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
SKY_CONSOLE_DB_PATH=…       # when set, telemetry dual-writes every
                            # log/metric/span to the SQLite file at this
                            # path (WAL mode, 24h log/span retention,
                            # 7d metric retention). SkyDeploy injects
                            # `/data/console.db` on Pro+ tenants so the
                            # bundled console mini-app can render
                            # history beyond the 10k-line / 1k-span
                            # in-RAM caps. Unset → pure in-RAM (default).
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
| `String` | `Sky.Core.String` | 36 entries — length, reverse, append, split, join, contains/containsIn, startsWith/startsWithIn, endsWith/endsWithIn (haystack-first In-suffixed companions added v0.15.47), toInt, fromInt, toFloat, fromFloat, toUpper, toLower, trim/trimStart/trimEnd, replace, slice, isEmpty, fromChar, toList, fromList, repeat, padLeft, padRight, casefold, equalFold, isEmail, isUrl, words, lines, concat |
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
| `File` | `Sky.Core.File` | readFile, readFileLimit, readFileBytes, writeFile, append, exists, remove, mkdirAll, readDir, isDir, tempFile, copy, rename |
| `Io` | `Sky.Core.Io` | readLine, writeStdout, writeStderr |
| `System` | `Sky.Core.System` | args, getArg, getenv, getenvOr (bare), getenvInt, getenvBool, setenv, unsetenv, cwd, loadEnv, exit |
| `Process` | `Sky.Core.Process` | run (subprocess) |
| `Db` | `Std.Db` | open, connect, close, exec, execRaw, query, insertRow, getById, updateById, deleteById, findOneByField, findManyByField, findByConditions, unsafeFindWhere, queryDecode, withTransaction, migrate (versioned forward-only schema migrations + `_sky_migrations` + checksum guard), getField, getString, getInt, getBool |
| `Auth` | `Std.Auth` | register, login, setRole (Task) + hashPassword, hashPasswordCost, verifyPassword, passwordStrength, signToken, verifyToken (Result); v0.15.48+ signTokenWithClaims / verifyTokenWithAlgorithm — typed-builder aliases over Sky.Core.Jwt for fine-grained algorithm + claims control |
| `Log` | `Std.Log` | println, debug, info, warn, error, debugWith, infoWith, warnWith, errorWith |
| `Trace` | `Std.Trace` | span, event, attr — opt-in app-level tracing spans. Tier-1 spans (HTTP/session/Msg/DB/Auth/Http/File) are automatic; see `docs/observability.md` |
| `Server` | `Sky.Http.Server` | param, queryParam, header, getCookie, static (Layer 3 surface); higher-level `get/post/listen/text/json/html` stay kernel-only |
| `Stream` | `Sky.Http.Server.Stream` | stream, emit, finish, withContentType — server-side streaming HTTP responses (SSE / LLM token forwarding / chunked downloads). Mirror of `Sky.Core.Http.Stream` (which reads upstream bodies as Sub events). See `docs/skylive/http-streaming.md` §"Server-side" + `examples/30-sse-server-demo`. Synchronous bridge: `Sky.Core.Http.Stream.forEachChunk hdl body` (v0.15.41+) drains an upstream stream from inside a plain Sky.Http.Server handler goroutine — needed for the relay shape (upstream chunks → `Server.Stream.emit` downstream chunk-for-chunk; no Sky.Live update loop required). See `docs/skylive/http-streaming.md` §"Synchronous relay" + `examples/32-sse-relay`. |
| `Middleware` | `Sky.Http.Middleware` | withCors, withLogging, withBasicAuth, withRateLimit |
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

### Surface highlights

Full reference: `docs/skyui/overview.md`.

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
4. **Negative literal arguments need parens.** `f -1` parses as
   subtraction. Use `f (-1)`.
5. **`Dict.toList` typed-key inference is inline-only.**
   `Dict.toList (Dict.fromList [(1, "a")])` chained in the same
   expression returns real `Int` keys (v0.15.45 closed the soundness
   hole for that shape). For let-bound intermediates — `let d =
   Dict.fromList […] in Dict.toList d` — the solver doesn't expose
   `d`'s typed shape at the use-site's region, so the routing falls
   back to the legacy String-key path. Workaround: inline the chain
   directly, or wrap the result in a typed accessor
   (`d |> Dict.toList`). v0.16+ tracking covers the let-region
   propagation fix.
6. **`sky check` does not fully model Go interface satisfaction.**
   Opaque FFI types unify with each other; concrete-satisfies-
   interface checks fall through.
7. **Zero-arg calls follow the binding's declared type, not its
   FFI-vs-kernel origin.** Bare `Uuid.v4` works because its stdlib
   sig is `v4 : String`. `Time.now ()` / `Time.unixMillis ()` /
   `FyneApp.new ()` are *all* needed because their sigs are
   `() -> Task Error a` / `() -> any`. Calling a `: String`
   binding with `()` triggers a known codegen bug for arity-0
   kernels (`Uuid.v4 ()` mis-applies the unit); stick to the
   declared shape. Dict / Set / Maybe / Result stay bare for
   their `empty` / `none` etc. because those have non-function
   types too.

   **v0.15.50 mitigation — `Sky.Core.Pure`.** New code
   targeting a uniform `() -> Task Error a` shape can import
   `Sky.Core.Pure as Pure` and call the additive companions —
   `Pure.uuidV4 ()` / `Pure.uuidV7 ()` / `Pure.timeNow ()` /
   `Pure.timeUnixMillis ()` / `Pure.systemArgs ()` /
   `Pure.systemCwd ()` / `Pure.systemLoadEnv ()` /
   `Pure.ioReadLine ()` / `Pure.dbConnect ()`. Existing names +
   shapes unchanged. Pure.* lowers to the canonical kernel with
   typed `SkyTask[Error, T]` shape (no `any` widening).
8. **Non-tail-recursive list operations are O(N) on Go stack.**
   `map`, `filter`, `foldr`, `length`, `concat`, `take`,
   `append`, `range`, `zip`, `concatMap`, `indexedMap`,
   `Maybe.combine`, `Result.combine` recurse. Tail-recursive
   operations (`foldl`, `find`, `any`, `all`, `member`, `drop`,
   `reverseHelp`) are auto-TCO'd to constant stack. For very
   large lists (200k+ elements) prefer the tail-recursive
   accumulator pattern.
9. **Zero-arg `Css.*` keyword constants require `()`** —
   `Css.zero ()`, `Css.auto ()`, `Css.none ()`. Bare form is now
   a clean type error rather than the silent function-pointer
   leak it used to be, but the `()` is still required.
10. **Multi-line function signatures.** `name\n    : T` (the `:`
    on a continuation line) parses cleanly. Continuation INSIDE
    the type body (`T1\n    -> T2`) is not supported — extract a
    `type alias` for the whole arrow type.
### Closed in v0.15 (kept here for grep)

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
src/                              -- Sky compiler (Haskell, GHC 9.4+)
  Sky/Parse/                      -- lexer, layout filter, parser
  Sky/Canonicalise/               -- name resolution, import validation
  Sky/Type/                       -- HM inference, exhaustiveness
  Sky/Build/                      -- orchestration, FFI generator, TCO
  Sky/Generate/Go/                -- Go IR + printer
  Sky/Lsp/                        -- language server
  Sky/Format/                     -- opinionated formatter (Elm-compatible)
  Sky/Doc/                        -- sky doc — index, terminal, HTTP render
app/Main.hs                       -- CLI entry point
runtime-go/rt/                    -- Go runtime (embedded via TH)
sky-stdlib/                       -- Sky-side stdlib (embedded via TH)
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
