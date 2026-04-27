# CLAUDE.md

## Language Convention

All documentation, comments, variable names, function names, and user-facing strings **must use British English spelling** (`optimise`, `behaviour`, `colour`, `initialise`, `serialise`, `catalogue`). Exceptions: protocol identifiers (LSP `initialize`), CSS/HTML properties (`color`), Go stdlib names.

## Core Principles (Non-Negotiable)

1. **If it compiles, it works.** The 2026-04-15 adversarial audit
   documented 23 counterexamples across P0 (soundness floor), P1
   (security), P2 (soundness cleanup), and P3 (tooling). All 23 are
   remediated with regression tests (see `docs/AUDIT_REMEDIATION.md`
   — completion marker landed 2026-04-16). The principle now holds
   for every path exercised by `cabal test`, `scripts/example-sweep.sh`,
   and `runtime-go/rt/*_test.go`. Residual P4 items (fully-typed
   codegen to eliminate `any` in emitted Go, and Sky-test harness
   port) remain as future-work tracked in `docs/PRODUCTION_READINESS.md`.
   Defence in depth (panic recovery + `Err` return at Task boundaries)
   remains the reliability floor under the v1.0 milestone.
2. **Dev experience is top priority.** Clear errors, predictable behaviour, no user-written FFI.
3. **Root-cause fixes only.** Fix at the correct abstraction layer. **Never suppress type errors or warnings.**
4. **Production-grade architecture.** Must scale to large Go packages (Stripe SDK). Must remain maintainable.

## Memory Safety (Non-Negotiable)

**The mem-guard kill-switch MUST be running during any Sky compiler dev session, example build sweep, or LSP-heavy editing on macOS.** A runaway `sky build` / `sky lsp` / `cabal` / `ghc` / `haskell-language-server` process has previously pinned the entire Mac to swap and forced a hard power-off — losing unsaved work in every app, not just the offending terminal. This is a recurring risk class, not a one-off. Treat it like a missing `set -e`: the absence is the bug.

Start it once at the top of the session and leave it running:

```bash
nohup ./scripts/mem-guard.sh > /tmp/mem-guard.out 2>&1 &
disown        # so it survives if the host shell exits
tail -f /tmp/mem-guard.log    # optional, watch what it does
```

Defaults (16GB Mac):
- Per-process kill at **6 GB RSS** for `sky` / `sky-ffi-inspect` / `cabal` / `ghc` / `ghc-iserv` / `cc1` / `ld` / `haskell-language-server` / `hls-wrapper` / `gopls`.
- Per-process kill at **10 GB RSS** (panic tier) for `claude` / `node` / `ghostty` — these are the *host* of the dev session, so the threshold is higher and the system-pressure rule additionally requires the host itself to already be over 4 GB before sacrificing it.
- System-pressure floor: when free + inactive + speculative memory drops below **1.2 GB**, kill the heaviest watched process immediately. macOS swap thrashing escalates to lock-up within seconds once free memory falls past this point — the 2-second poll interval is sized to act inside that window.

Tune via env vars: `MEM_GUARD_PROC_MB`, `MEM_GUARD_PANIC_MB`, `MEM_GUARD_SYS_FLOOR_MB`, `MEM_GUARD_INTERVAL`. `MEM_GUARD_DRY=1` runs in log-only mode for verification. The script never touches kernel processes, WindowServer, launchd, or anything outside its watch list.

**Workflow rules:**
- **Before** kicking off `cabal test` or the full `for d in examples/*/; do … done` clean-slate sweep, confirm `pgrep -f mem-guard.sh` returns a PID. If it doesn't, start it first.
- **When extending the watch list** (new compiler tool, new dev binary), add the regex to both `ALWAYS_KILL_RE` / `PANIC_KILL_RE` in `scripts/mem-guard.sh` AND mention the addition here so future sessions don't accidentally shrink the safety net.
- **If the guard fires**, log the full kill line (`grep KILL /tmp/mem-guard.log`) into the related issue or commit message — repeated kills on the same binary are a real compiler/LSP bug, not just an unlucky build.
- **Never disable the guard to silence a kill.** A kill means the offending process was on a path to OOM the machine; the fix is in the compiler/LSP, not in raising the threshold past available RAM.

## v0.10.0 Stdlib Consolidation (BREAKING)

The standard library was deduplicated. Old modules with overlapping surface have been dropped or renamed. This is a one-shot migration — there are **no compat shims**, by design.

**Renamed:**
- Sky kernel `Os` → `System`. Frees the `Os` qualifier for the Go FFI `os` package (sky-log et al. need stdin / stderr / fileWriteString from Go's std library and previously hit a kernel-vs-FFI namespace collision). Migration: `Os.exit` → `System.exit`, `Os.getenv` → `System.getenv`, `Os.cwd` → `System.cwd`, `Os.args` → `System.args`. `import Sky.Core.Os as Os` → `import Sky.Core.System as System`.

**Dropped (folded into other modules):**
- `Args.*` → `System.{args, getArg}`. `Args.getArgs ()` is now `System.args ()`. `Args.getArg n` is `System.getArg n` (now returns `Task Error (Maybe String)` per Task-everywhere — see migration helper in sky-env's Main.sky).
- `Env.*` → `System.{getenv, getenvOr, getenvInt, getenvBool}`. `Env.getOrDefault key def` → `System.getenvOr key def`, `Env.getInt key` → `System.getenvInt key`, `Env.require key` → `System.getenv key` (already errors on missing).
- `Slog.*` → `Log.*With`. `Slog.info msg attrs` → `Log.infoWith msg attrs` (same for warn/error/debug). The plain-message `Log.info msg` form keeps working — the `With`-suffix variants take the structured `(msg, [k,v,k,v,…])` shape Slog had.
- `Sha256.*` → `Crypto.sha256`. The chain `Sha256.sum256 (String.toBytes s) |> Result.andThen Hex.encodeToString` collapses to `Crypto.sha256 s` (returns the hex digest directly).
- `Hex.*` → `Encoding.hexEncode` / `Encoding.hexDecode`.

**Shrunk:**
- `Process.*` keeps only `run` (subprocess execution). `Process.exit` / `getEnv` / `getCwd` / `loadEnv` all moved to `System.*`.

**New:**
- `System.getenvOr key default` — no-error env read with fallback. **Bare `String` return** (not `Task Error String`) — see "default-supplied helpers stay bare" rule below the Effect Boundary table. Module-top-level usage stays sync: `apiKey _ = System.getenvOr "OPENAI_KEY" ""`.
- `System.getenvInt key` / `System.getenvBool key` — typed env reads, `Task Error Int|Bool` (CAN fail on missing or unparseable, hence Task).
- `System.getArg n` — single-arg lookup as `Task Error (Maybe String)`.
- `System.loadEnv ()` — load `.env` file (was `Process.loadEnv`).
- `Log.infoWith msg attrs` / `warnWith` / `errorWith` / `debugWith` — structured-log variants with key/value list (replaces `Slog.*`).
- sky.toml `[log] format = "plain" | "json"` and `[log] level = "debug" | "info" | "warn" | "error"`. Seeds `SKY_LOG_FORMAT` / `SKY_LOG_LEVEL` defaults at compile time. Env vars still override at runtime — same precedence as `[live]`.

**Compiler infrastructure:**
- Bare-name aliases for every kernel module added to `staticKernelModules`. `Log.error` resolves without `import Std.Log` because `Log` is in the kernel registry.
- Auto-force `let _ = TaskExpr` discard semantics (already in v1.0+ doctrine, formalised here): the lowerer wraps the discarded expression in `rt.AnyTaskRun` so the side effect fires.
- `main`'s body is wrapped in `rt.AnyTaskRun` too — `main = println X` actually prints under Task-everywhere (regression discovered when migrating examples).
- Canonicaliser falls back to the kernel registry for unimported qualifiers — `Crypto.sha256` works without `import Sky.Core.Crypto`. Bug fixed: previously emitted bare `Crypto_sha256(arg)` (no `rt.` prefix), failing `go build`.
- **Dep module HM errors are FATAL.** Pass-2 type errors in dep modules used to silently degrade to `any`-typed bindings, which let real type bugs (Task being case-matched as Result, `/` instead of `//`, `Bool -> Attribute` partial-applied as `Attribute`) ship and surface as runtime symptoms like `[AUTH] Admin ensured: 0x102…` (a func-pointer of an unforced thunk being string-split). Now the build aborts with `TYPE ERROR (Mod): …` and the user fixes the dep before shipping. Pass-1 errors stay tolerated because some deps need pass 2 to disambiguate via cross-module externals. Regression test: `test/Sky/Build/DepHmFatalSpec.hs`.

The migration script at `/tmp/migrate-v0.10.sh` (see git history of this commit) handles the bulk rewrites in any external repo. Manual touch-ups needed for: `case Args.getArg n of Just/Nothing` patterns (now wrap with `Task.run |> Result.withDefault Nothing`), and `Sha256.sum256 + Hex.encodeToString` chains (collapse to `Crypto.sha256`).

## Non-Regression Rules

These constraints are enforced by `sky verify`, `test/Sky/ErrorUnificationSpec.hs`, and the audit-remediation specs under `test/Sky/**`. Violating them breaks the repo:

- **No `Result String a`** in any public surface. Use `Result Error a`.
- **No `Task String a`** in any public surface. Use `Task Error a`.
- **No `Std.IoError`** — the pre-v1 error ADT was deleted.
- **No `RemoteData`** — the pre-v1 async-state type was deleted.
- **No runtime panic from well-typed Sky code.** Every known panic class has a regression test in `runtime-go/rt/*_test.go` or `test/Sky/**Spec.hs`.
- **No silent numeric coercion.** `AsInt` / `AsBool` / `AsFloat` returning zero on type mismatch is a violation of P0-2 (see `docs/AUDIT_REMEDIATION.md`). New code uses the fallible `AsIntChecked` variants; lenient display-only helpers carry the suffix `OrZero` so the intent is visible at every call site.
- **No raw `.(T)` assertions on generated any-typed thunks.** Typed codegen must route through a runtime `Coerce` helper (see P0-3). Grep gate: `sky-out/main.go` files contain no `any(body).(T)` patterns outside the `Coerce*` helpers.
- **Record field enumeration sorts by `_fieldIndex`.** Any `Map.toList fields` in codegen that feeds field order (struct decl, auto-ctor, destructure) sorts by declaration index before emission. Violating this swaps auto-ctor parameters (P0-4).
- **Secrets are typed.** `Auth.signToken` / `Auth.verifyToken` take `String`, not `any`. `fmt.Sprintf("%v", secret)` is forbidden — explicit `.(string)` assertion on a typed boundary, with minimum-length validation (P1-4).
- **`sky check` is a superset of `sky build`.** `sky check` runs the Go emitter and invokes `go build` on the output. If `sky build` would fail, `sky check` fails (P0-1). Regression test: for every fixture in `test-files/`, both commands agree on accept/reject.
- **New AST nodes must be matched explicitly in every walker.** When you add a constructor to `Src.Expr_` / `Src.Pattern_` / `Src.TypeAnnotation_` in `src/Sky/AST/Source.hs`, grep for EVERY case-on-AST in these modules and add an explicit arm:
  - `src/Sky/Canonicalise/Expression.hs`, `Pattern.hs`, `Type.hs`
  - `src/Sky/Type/Constrain/Expression.hs`, `Pattern.hs`
  - `src/Sky/Type/Exhaustiveness.hs`
  - `src/Sky/Format/Format.hs`
  - `src/Sky/Build/Compile.hs` (lowerer)
  - `src/Sky/Lsp/Server.hs` — `exprTokens`, `exprIdents`, `exprAllRefs`, `refsInExpr`, `collectSemTokens`, `collectReferences`, plus anything that walks expressions for hover / definition / rename

  **Do NOT rely on `_ -> []` catchalls** — they silently drop the new node and surface as bugs that look nothing like "missing AST case" (semanticTokens hangs, definition jump fails, rename misses references). If a walker genuinely has no token/reference to emit for the new node, write `Src.MyNode inner -> walker inner` (transparent recurse) or `Src.MyNode _ -> []` (explicit no-op) — never leave it to the catchall. Regression artefact: the `Src.Paren` node landed in 85ef8d1 but wasn't wired into `Sky/Lsp/Server.hs`'s four walkers until v0.9.2 — broke `textDocument/definition` for every identifier inside parens AND hung `handleSemanticTokens` (pattern-match exception swallowed by the outer `try` loop).

## Testing Rules

- **Every new language feature or runtime helper needs a test.** Cabal specs for compile-time behaviour; `runtime-go/rt/*_test.go` for runtime helpers; `tests/**/*Test.sky` for stdlib semantics.
- **Every bug becomes a regression test** *before* landing the fix. The failing test is the discovery artefact; without it, the class comes back. Audit items specifically require the test to fail against HEAD~1 and pass at HEAD.
- **`sky test <file>` is the user-facing runner.** See `sky-stdlib/Sky/Test.sky` for the API.
- **Runtime verification on every push.** `sky verify` builds and runs each example, catching panics and HTTP failures that `--build-only` misses.

## Tooling Rules

- **CLI commands must be correct end-to-end.** `sky build` / `sky run` / `sky check` / `sky fmt` / `sky test` all auto-regen missing FFI bindings and propagate exit codes.
- **LSP capabilities must match `docs/tooling/lsp.md`.** If you add a capability, document it. If a feature is incomplete, narrow the claim — don't lie in docs.
- **Formatter must be idempotent.** Two passes produce byte-identical output. Fixtures in `test/Sky/Format/FormatSpec.hs` guard this.

## Effect Boundary: Task-everywhere (v0.10.0+)

Single rule: **every observable side effect returns `Task Error a`.** No two-tier split, no per-function decision overhead. (Previously the v0.9.6 doctrine carved out `println` / `Slog` / `Os.getenv` / `Time.now` as "sync convenience effects" — that's gone; the lowerer's `let _ = TaskExpr` auto-force makes the Task-everywhere shape ergonomic without `do`-notation.)

| Tier | Type | Examples | Why |
|---|---|---|---|
| **Pure** | bare `a` | `String.length`, `List.map`, `Crypto.{sha256,sha512,md5,hmacSha256}`, `Encoding.{base64,url,hex}Encode`, `Time.timeString`, `System.getenvOr` (default supplied → can't fail) | Referentially transparent, deterministic. |
| **Fallible-pure** | `Result e a` / `Maybe a` | `String.toInt`, JSON decoders, `Encoding.{base64,url,hex}Decode`, `Auth.{hashPassword, verifyPassword, signToken, verifyToken}` | Pure CPU work that can fail on malformed input. Result is a value, not an effect. |
| **Effects** | `Task Error a` | `File.*`, `Http.*`, `Process.run`, `Io.*`, `Db.*`, `Auth.{register, login, setRole}`, `Crypto.{randomBytes, randomToken}`, `Time.{sleep, now, unixMillis}`, `Random.*`, `Log.{println, info, warn, error, debug, infoWith, warnWith, errorWith, debugWith}`, `System.{getenv, getenvInt, getenvBool, cwd, args, getArg, loadEnv}`, `Live.app` | Anything that touches the outside world (clock, env, stdout, disk, network, DB, entropy). Composes uniformly with `Task.parallel` / `Cmd.perform` / `Task.andThen`. |
| **Diverging** | polymorphic `Int -> a` | `System.exit` | Function never returns (process terminates). Polymorphic return makes it usable as the last expression in any case branch without forcing every branch to be Task-shaped. |

**Default-supplied helpers stay bare.** `System.getenvOr key def : String`, `Maybe.withDefault def m : a`, `Result.withDefault def r : a`, `Db.getFieldOr def row k : any`, `Db.get{String,Int,Bool} k row : String|Int|Bool` — none of these can fail because the default plugs the failure case at the call site. Wrapping them in `Task` / `Result` / `Maybe` would force every call into `Task.run … |> Result.withDefault def` boilerplate — the exact pattern the helper exists to avoid. Reserve the wrap for genuinely fallible operations (no default supplied, parse may reject input, I/O may error).

### Auto-force `let _ = TaskExpr`

The lowerer special-cases `let _ = X in Y` discards: when X has type `Task e a`, it emits `_ = rt.AnyTaskRun(X)` instead of bare `_ = X`. The Task thunk is forced and its Result discarded — the side effect fires, the user gets the same eager-discard ergonomics they always had:

```elm
let
    _ = println "step 1"            -- Task auto-forced; print fires
    _ = println "step 2"            -- same
    _ = Log.infoWith "saving" [...]
in
    continue
```

This is the entire reason Task-everywhere is viable in Sky despite no `do`-notation. Without auto-force, every kernel-Task call would need explicit `Task.run` wrapping at the discard site, and forgetting would silently drop the side effect.

`rt.AnyTaskRun` handles both shapes defensively: forces `func() any` thunks, passes bare values through wrapped in Ok. Auto-force at non-Task discard sites is therefore safe (negligible runtime cost: one type-assertion).

**Rule that complements auto-force:** `let _ = X in Y` is the canonical "fire-and-forget" idiom. Don't manually wrap in `Task.run` for the discard case — write `let _ = task` and let the lowerer DTRT. Use `Task.run` explicitly when you need the `Result` for further inspection.

### Top-level bindings stay explicit

The auto-force magic applies **only** to `let _ = X` discards. Top-level module bindings of `Task`-typed values still require explicit `Task.run`:

```elm
-- Module top-level
apiKey =
    System.getenv "OPENAI_KEY"
        |> Task.run
        |> Result.withDefault ""
```

This is the deliberate single-rule trade-off: one piece of compiler magic (`let _ =` auto-force) covers the pervasive debug-trace pattern; everything else is explicit Task plumbing. No surprises about whether your value is wrapped or not.

### Two-level error handling pattern

When a Task fails inside an effectful op, the canonical pattern (demonstrated in `examples/18-job-queue/src/Main.sky`'s `withErrorReporting` and `examples/07-todo-cli/src/Main.sky`'s `reportError`) is:

1. **Generate a short correlation ID** (`Crypto.randomToken 4`) — typically 4 bytes hex.
2. **Server-side: structured log** via `Log.errorWith opName [ "errId", errId, "error", Error.toString e ]` — ops can grep their logs by the ID.
3. **Client-side: user-friendly message** via `Task.fail (Error.unexpected ("Operation failed (ref " ++ errId ++ ")"))` — a user complaining "save failed ref a3f9" maps directly to the log line.

Per app shape:
- **CLI** (`07-todo-cli`): `main = Task.run (chain |> Task.onError reportError)` where `reportError` logs + prints to stderr + `System.exit 1`.
- **Sky.Http.Server** (`08-notes-app`): handlers return `Task Error Response`; `Task.onError` recovers errors to a 4xx/5xx Response with the logged errId in the body.
- **REST API**: same as Http.Server, but the recovered Response is `Server.json (errorJson errId)` instead of HTML.
- **Sky.Live** (`18-job-queue`, `12-skyvote`, `13-skyshop`, etc.): `Cmd.perform task ResultMsg` dispatches; the `ResultMsg` handler updates a `notification` / `historyError` field in Model that the `view` renders as a banner.

FFI boundary mapping: Go `(T, error)` → `Result Error T` (the FFI trust boundary is Result; user wrappers / kernel sigs lift to Task where appropriate) | Go `error` → `Result Error ()` | panics → `Err` | nil → `Maybe` / `Result`.

### Result/Task bridges

Every Go FFI call returns `Result Error T` (the synchronous trust boundary). When the surrounding pipeline is a `Task` (Sky.Http handler, Sky.Live `Cmd.perform`, `main` returning `Task`), three bridge helpers flatten what would otherwise be nested `case`-on-Result inside `Task.andThen` lambdas:

| Helper | Type | When to reach for it |
|---|---|---|
| `Task.fromResult` | `Result e a -> Task e a` | Lift a Result-returning step (FFI call, parser) into a Task pipeline |
| `Task.andThenResult` | `(a -> Result e b) -> Task e a -> Task e b` | Chain a Result-returning step after a Task |
| `Result.andThenTask` | `(a -> Task e b) -> Result e a -> Task e b` | Chain a Task-returning step after a Result |

```elm
-- Without bridges (nested case inside Task.andThen)
case Db.connect dbUrl of
    Ok db ->
        case Db.query db "SELECT ..." of
            Ok rows ->
                Http.post url (encode rows) |> Task.andThen handleResponse
            Err e -> Task.fail e
    Err e -> Task.fail e

-- With bridges (flat pipeline)
Db.connect dbUrl
    |> Task.fromResult
    |> Task.andThenResult (\db -> Db.query db "SELECT ...")
    |> Task.andThen (\rows -> Http.post url (encode rows))
    |> Task.andThen handleResponse
```

There is **no** `Result.fromTask` / `Task -> Result` bridge. `Task.run` exists in the kernel for the runtime entry boundary, but user code should keep effectful pipelines in `Task` and let the boundary (CLI `main`, `Cmd.perform`, HTTP handler return) execute it. Collapsing a Task to a Result blocks the caller and erases effect tracking from the type — usually a sign the surrounding function should return `Task` itself.

## Environment Variable Precedence

Configuration values resolve in this order (highest priority first):

1. **System environment variables** (`export PORT=8080`, Docker `ENV`, CI vars)
2. **`.env` file** in the working directory (auto-loaded at startup, never overrides existing env vars)
3. **`sky.toml`** defaults (compiled into the binary via `init()`, only set when not already present)

This follows the standard convention (godotenv, Docker): system env vars always win so production deployments can override `.env` defaults without editing files. The `.env` file is for local development convenience.

Sky.Live env vars (sky.toml keys live under `[live]` — there is no `[live.session]` section): `SKY_LIVE_PORT` (`port`), `SKY_LIVE_TTL` (`ttl`), `SKY_LIVE_STORE` (`store` — `memory` / `sqlite` / `redis` / `postgres`), `SKY_LIVE_STORE_PATH` (`storePath` — sqlite file or `host:port` / `redis://…` / `postgres://…` URL), `SKY_LIVE_STATIC_DIR` (`static`), `SKY_LIVE_INPUT` (`input`), `SKY_LIVE_POLL_INTERVAL` (`poll_interval`), `SKY_LIVE_MAX_BODY_BYTES` (`maxBodyBytes` — cap for `/_sky/event` POST body, default `5242880` = 5 MiB; bump for `Event.onFile` / `Event.onImage` uploads larger than that). Postgres falls back to `DATABASE_URL` and Redis to `REDIS_URL` when `SKY_LIVE_STORE_PATH` is unset (Redis defaults further to `localhost:6379`). Auth: `SKY_AUTH_TOKEN_TTL`, `SKY_AUTH_COOKIE`. Connection-status banner: `SKY_LIVE_BANNER` (default `on`; `off` / `0` / `false` to disable the chrome but keep the POST retry queue active), `SKY_LIVE_RETRY_BASE_MS` (default `500`), `SKY_LIVE_RETRY_MAX_MS` (default `16000`), `SKY_LIVE_RETRY_MAX_ATTEMPTS` (default `10`), `SKY_LIVE_QUEUE_MAX` (default `50`).

**Logging (v0.10.0+)**: `SKY_LOG_FORMAT` (`plain` default | `json`) and `SKY_LOG_LEVEL` (`debug` | `info` default | `warn` | `error`) control `Log.*` output. Project-level defaults via sky.toml `[log] format = "json" / level = "info"` — same three-layer precedence (env > `.env` > `sky.toml`). Switch to JSON in production by setting `SKY_LOG_FORMAT=json` in the deployment env; no rebuild required.

**Compiler internals (2026-04-27+)**: `SKY_SOLVER_BUDGET` (default `5000000`) caps the HM constraint-solver step count per `solve` invocation. Exceeding the cap aborts with a clear `TYPE ERROR: constraint solver exceeded budget` rather than letting pathological constraint patterns OOM the host (Limitation #17 hardening). Set to `0` to disable the bound entirely — debug-only escape hatch, NOT recommended for shipping. Set to a higher integer if you legitimately need more headroom (e.g. a generated codebase with 10× more declarations than typical), though in practice every shippable Sky module to date type-checks in well under 100K steps.

## Project Overview

Sky is a pure functional, ML-family language compiling to Go. The compiler is written in Haskell (GHC 9.4+) and ships as a single `sky` binary. Runtime binaries are Go output — single-file, statically-linked, no external runtime needed. See `docs/compiler/journey.md` for why the compiler moved TS → Go → Sky → Haskell. (Surface syntax is Elm-compatible; several files in `src/Sky/Type/`, `src/Sky/AST/`, `src/Sky/Reporting/`, `src/Sky/Parse/Primitives.hs` are derivative works adapted from elm/compiler under BSD-3-Clause — see `NOTICE.md`.)

## Architecture

```
source → lexer → layout filtering → parser → AST → module graph → type checker → Go emitter
```

```
src/                              -- Sky compiler (Haskell, GHC 9.4+)
  Sky/Parse/                      -- lexer, layout filter, parser
  Sky/Canonicalise/               -- name resolution, import validation
  Sky/Type/                       -- HM inference, exhaustiveness
  Sky/Build/                      -- orchestration + FFI generator
  Sky/Generate/Go/                -- Go IR + printer
  Sky/Lsp/                        -- language server
  Sky/Format/                     -- opinionated formatter (Elm-compatible output)
app/Main.hs                       -- CLI entry point
runtime-go/rt/                    -- Go runtime (embedded via Template Haskell)
sky-stdlib/                       -- Sky-side stdlib (embedded)
tools/sky-ffi-inspect/            -- Go package introspector (embedded via TH;
                                     self-provisions to XDG cache on first use
                                     so releases ship a single `sky` binary)
legacy-ts-compiler/               -- Legacy TypeScript bootstrap (reference only)
legacy-sky-compiler/              -- Legacy self-hosted Sky compiler (reference only)
templates/CLAUDE.md               -- Template for `sky init` projects
examples/                         -- 18 example projects
```

See `docs/compiler/journey.md` for the TS → Go → Sky → Haskell history.

## Template Sync (Non-Negotiable)

When stdlib, syntax, Sky.Live APIs, or CLI commands change, **`templates/CLAUDE.md` MUST be updated**. AI assistants use this template to write Sky code in user projects.

**User-facing doc sync** — these files mirror the same surface from a user (not AI) perspective and MUST be updated in the same commit when the underlying API changes:

- **`docs/stdlib.md`** — comprehensive user-facing stdlib reference. When you add / rename / remove a function in any kernel module, update the matching table here.
- **`docs/skyauth/overview.md`** — when `Std.Auth` surface or config changes (`hashPassword`, `signToken`, `register`, `login`, `setRole`, `[auth]` keys, env vars).
- **`docs/skydb/overview.md`** — when `Std.Db` surface or config changes (`open`, `query`, `withTransaction`, CRUD helpers, `[database]` keys, env vars).
- **`docs/skylive/overview.md`** + **`docs/skylive/architecture.md`** — when Sky.Live runtime, `Live.app` shape, session-store options, or `[live]` env vars change.
- **`docs/skyui/overview.md`** — when `Std.Ui` surface or sub-modules change (`Std.Ui.{Background, Border, Font, Region, Input, Lazy, Keyed, Responsive}`), or when an idiom like the form/onSubmit pattern or file/image upload shape evolves.
- **`README.md` "What's in the box" section** — if you add a brand-new killer module (top-level `Std.Foo`), add a callout there.

The dense reference inside CLAUDE.md (this file, "Standard Library" section) and `templates/CLAUDE.md` is for AI; the `docs/*` files are for humans. Keep both in lockstep.

## Building Examples

**NEVER run `sky build` for examples from the repo root** — it overwrites the compiler binary in `sky-out/`. Always `cd` into the example directory first:
```bash
cd examples/01-hello-world && sky build src/Main.sky
```

## Git Push / Release Checklist

1. `cabal install --overwrite-policy=always --installdir=./sky-out --install-method=copy exe:sky` — rebuild compiler
2. `sky-out/sky --version` — must print version, NOT start a server
3. `cabal test` — cabal test suite must pass (18/18 ExampleSweep + TypedFfi + ErrorUnification specs)
4. **Clean-slate validation of ALL examples (mandatory before every push/tag):**
   ```bash
   for d in examples/*/; do
       cd "$d" && rm -rf sky-out .skycache .skydeps
       # run `sky install` first if sky.toml has [go.dependencies]
       sky build src/Main.sky   # must succeed
       ./sky-out/app            # must run (kill servers after verifying HTTP 200)
       cd ../..
   done
   ```
   Every example must build **and** run from a completely clean slate. If any example fails, fix it before pushing. No exceptions.
5. `cd examples/12-skyvote && sky check` — 0 errors
6. Test in temp dir: `sky init mytest`, `sky build && sky run`, `sky add fmt`, `sky remove fmt`, `sky upgrade`
7. Verify `.github/workflows/ci.yml` matches build steps

## CI/CD Rules

When pushing to `main`, cancel any in-progress **CI build** runs (not release runs) since the new commit supersedes them:
```bash
# Cancel in-progress CI runs on main before pushing
gh run list --branch main --status in_progress --workflow CI --json databaseId --jq '.[].databaseId' | xargs -I{} gh run cancel {} 2>/dev/null
git push origin main
```
Never cancel **release** runs (triggered by tags) — those produce binaries users download.

## Shell Commands

Always use `-f` flag with `rm` and `cp` (`rm -f`, `rm -rf`, `cp -f`).

## Build & Test

```bash
sky init [name]                   # Create new project
sky build src/Main.sky            # Compile → sky-out/app
sky run src/Main.sky              # Build and run
sky check src/Main.sky            # Type-check only
sky fmt src/Main.sky              # Format (opinionated, Elm-compatible)
sky test tests/MyTest.sky         # Run a Sky test module (exposing `tests : List Test`)
sky add github.com/some/package   # Add dependency + generate bindings
sky remove <package>              # Remove dependency
sky install                       # Install deps + generate missing bindings
sky update                        # Update deps to latest
sky upgrade                       # Self-upgrade binary
sky lsp                           # Language Server (JSON-RPC/stdio)
sky clean                         # Remove sky-out/ dist/
sky --version                     # `sky dev` on local builds; CI injects the release version
```

Local builds read the compiler version from `app/VERSION` (literal `dev`). Release CI overwrites that file with the git tag before `cabal install`. Don't bump `sky-compiler.cabal`'s `version:` field — it's pinned to `0.0.0` by design so local and CI artefacts stay distinguishable.

## Code Formatting (`sky fmt`)

Opinionated formatter, no configuration. Output is Elm-compatible (4-space indent, leading commas, "one line or each on its own line"):
- 4-space indentation (never tabs)
- No max line width — short on one line, long ones break
- "One line or each on its own line" for args, list items, record fields
- Leading commas for multi-line lists/records
- Trailing newline; two blank lines between declarations

```elm
-- Pipelines
value
    |> transform1
    |> transform2 arg1

-- Records: leading commas when multi-line
{ firstName = "Alice"
, lastName = "Smith"
}

-- Case
case msg of
    Increment ->
        count + 1
    Decrement ->
        count - 1

-- Let/in
let
    x = compute
in
    result

-- else if: flat chains
if x > 0 then
    positive
else if x < 0 then
    negative
else
    zero
```

Safety: formatter refuses to write if output loses >1/3 of code lines (prevents silent deletion from partial AST).

## Standard Library

Single canonical module per concern after the v0.10.0 consolidation. Every kernel module is reachable via its bare name (e.g. `import Log` is the same as `import Std.Log as Log`); the `Sky.Core.X` / `Std.X` long paths are kept for cross-language familiarity but you can usually drop them.

### Pure (no I/O, no Task wrap)
| Module | Path | Key functions |
|---|---|---|
| `Basics` | `Sky.Core.Basics`, autoloaded via `Sky.Core.Prelude` | identity, always, not, toString, modBy, clamp, fst, snd, compare, negate, abs, sqrt, min, max |
| `String` | `Sky.Core.String` | length, reverse, append, split, join, contains, startsWith, endsWith, toInt, fromInt, toFloat, fromFloat, toUpper, toLower, trim, trimStart, trimEnd, replace, slice, isEmpty, toBytes, fromBytes, fromChar, toChar, left, right, padLeft, padRight, repeat, lines, words, isValid, normalize, normalizeNFD, casefold, equalFold, graphemes, isEmail, isUrl, slugify, htmlEscape, truncate, ellipsize |
| `List` | `Sky.Core.List` | map, filter, foldl, foldr, length, head, tail, take, drop, append, concat, concatMap, reverse, sort, sortBy, member, any, all, range, zip, filterMap, parallelMap, isEmpty, indexedMap, find, cons |
| `Dict` | `Sky.Core.Dict` | empty, insert, get, remove, member, keys, values, toList, fromList, map, foldl, union |
| `Set` | `Sky.Core.Set` | empty, insert, remove, member, union, diff, intersect, fromList, toList, size |
| `Maybe` | `Sky.Core.Maybe` | withDefault, map, andThen, map2..5, andMap, combine, traverse |
| `Result` | `Sky.Core.Result` | withDefault, map, andThen, mapError, map2..5, andMap, combine, traverse, andThenTask |
| `Math` | `Sky.Core.Math` | sqrt, pow, abs, floor, ceil, round, sin, cos, tan, pi, e, log, min, max |
| `Regex` | `Sky.Core.Regex` | match, find, findAll, replace, split |
| `Char` | `Sky.Core.Char` | isUpper, isLower, isDigit, isAlpha, toUpper, toLower |
| `Path` | `Sky.Core.Path` | join, dir, base, ext, isAbsolute, safeJoin |
| `Crypto` | `Sky.Core.Crypto` | sha256, sha512, md5, hmacSha256, constantTimeEqual, randomBytes, randomToken (random* return Task — entropy) |
| `Encoding` | `Sky.Core.Encoding` | base64Encode/Decode, urlEncode/Decode, hexEncode/Decode |
| `Json.Encode` (alias `JsonEnc`) | `Sky.Core.Json.Encode` | string, int, float, bool, null, list, object, encode |
| `Json.Decode` (alias `JsonDec`) | `Sky.Core.Json.Decode` | decodeString, string, int, float, bool, field, index, list, map, andThen, succeed, fail, oneOf, at, map2..5 |
| `Json.Decode.Pipeline` (alias `JsonDecP`) | `Sky.Core.Json.Decode.Pipeline` | required, optional, custom, requiredAt |
| `Uuid` | `Sky.Core.Uuid` | v4, v7, parse |

### Effects (`Task Error a`)
| Module | Path | Key functions |
|---|---|---|
| `Task` | `Sky.Core.Task` | succeed, fail, map, andThen, perform, sequence, parallel, lazy, run, fromResult, andThenResult, mapError, onError |
| `Cmd` | `Std.Cmd` | none, batch, perform |
| `Sub` | `Std.Sub` | none, every |
| `Time` | `Sky.Core.Time` | now, sleep, every, unixMillis, formatISO8601, formatRFC3339, formatHTTP, format, parseISO8601, parse, addMillis, diffMillis, timeString |
| `Random` | `Sky.Core.Random` | int, float, choice, shuffle |
| `Http` | `Sky.Core.Http` | get, post, request |
| `File` | `Sky.Core.File` | readFile, readFileLimit, readFileBytes, writeFile, append, mkdirAll, readDir, exists, remove, isDir, tempFile, copy, rename |
| `Io` | `Sky.Core.Io` | readLine, readBytes, writeStdout, writeStderr, writeString |
| **`System`** | `Sky.Core.System` | args, getArg, getenv, getenvOr, getenvInt, getenvBool, cwd, exit, loadEnv |
| `Process` | `Sky.Core.Process` | run (subprocess execution only) |
| `Db` | `Std.Db` | connect, open, close, exec, execRaw, query, queryDecode, insertRow, getById, updateById, deleteById, findWhere, withTransaction, getField, getFieldOr, getString, getInt, getBool |
| `Auth` | `Std.Auth` | hashPassword, verifyPassword, signToken, verifyToken, register, login, setRole, hashPasswordCost, passwordStrength |
| **`Log`** | `Std.Log` | println, debug, info, warn, error, debugWith, infoWith, warnWith, errorWith, with |

### Web / Live / UI
| Module | Path | Key functions |
|---|---|---|
| `Server` | `Sky.Http.Server` | listen, get/post/put/delete/any, static, text/json/html, withStatus, redirect, param, queryParam, header, getCookie, cookie, withCookie, withHeader, method, formValue, body, path, group, use |
| `Live` | `Std.Live` | app, route, api |
| `Event` | `Std.Live.Events` / `Std.Html.Events` | onClick, onInput, onChange, onSubmit, on*, onImage, onFile, fileMaxWidth/Height/Size |
| `Html` | `Std.Html` | text, div, span, p, h1..h6, a, button, input, form, … (~70 elements + render/escape helpers) |
| `Attr` | `Std.Html.Attributes` | class, id, style, type/value/href/src, checked/disabled/required, … (~60 attrs + boolAttribute/dataAttribute) |
| `Css` | `Std.Css` | stylesheet, rule, property, px/rem/em/pct/hex/rgba, color/background/padding/margin/font*, transition, grid*, flex*, … (~120) |
| `Ui` | `Std.Ui` | Typed no-CSS layout DSL. **Layout**: el/row/column/paragraph/textColumn/text/none/button/input/form/link/image/html, layout. **Length**: px/fill/fillPortion/content/shrink/minimum/maximum (`fill : Length` is bare — for proportional fills use `fillPortion n`; `minimum n l` / `maximum n l` constrain a length). **Padding**: padding/paddingXY/paddingEach/spacing. **Align**: centerX/Y/alignLeft/Right/Top/Bottom/pointer. **Overflow**: clip/clipX/clipY/scrollbars/scrollbarX/scrollbarY. **Nearby**: above/below/onLeft/onRight/inFront/behind. **Color**: rgb/rgba/white/black/transparent. **Events**: onClick/onSubmit/onInput/onChange/onFocus/onMouseOver/onMouseOut/onKeyDown/onFile/onImage, fileMaxSize/Width/Height. **Attrs**: htmlAttribute/style/class/name. Sub-modules: `Std.Ui.Background` (color/image/linearGradient/gradient), `Std.Ui.Border` (color/width/widthEach/rounded/solid/dashed/dotted/shadow/glow/innerShadow), `Std.Ui.Font` (color/family/size/weight/bold/semiBold/regular/light/extraBold/black/italic/underline/letterSpacing/wordSpacing/sansSerif/serif/monospace/alignLeft/alignRight/alignCenter/center/justify), `Std.Ui.Region` (heading/mainContent/navigation/footer/aside/label/announce/announceUrgently — renderer dispatches `<h1..h6>`/`<main>`/`<nav>`/`<footer>`/`<aside>` + aria-label/aria-live), `Std.Ui.Input` (button/text/multiline/checkbox/email/username/search/currentPassword/newPassword/radio/radioRow/slider + labelAbove/Below/Left/Right/Hidden + placeholder + option), `Std.Ui.Lazy` (lazy/lazy2..lazy5 — currently no-op wrappers), `Std.Ui.Keyed` (keyed), `Std.Ui.Responsive` (classifyDevice/adapt). Renders to inline-styled HTML via Std.Html — no CSS files. Full reference: docs/skyui/overview.md. Prior-art attribution: NOTICE.md. |
| `RateLimit` | `Sky.Http.RateLimit` | allow |
| `Middleware` | `Sky.Http.Middleware` | withCors, withLogging, withBasicAuth, withRateLimit |

### Low-level FFI proxies
| Module | Path | Key functions |
|---|---|---|
| `Context` | `Context` (Go context) | background, todo, withValue, withCancel |
| `Fmt` | `Fmt` (Go fmt) | sprint, sprintf, sprintln, errorf |
| `Ffi` | `Sky.Ffi` | call, callPure, callTask, has, isPure |

### Diverging
- `System.exit : Int -> a` — process termination, polymorphic return.

### Prelude (implicitly imported via `Sky.Core.Prelude exposing (..)`)
`Result (Ok/Err)`, `Maybe (Just/Nothing)`, `identity`, `not`, `always`, `fst`, `snd`, `clamp`, `modBy`, `errorToString`

### Concurrency
```elm
Task.parallel : List (Task err a) -> Task err (List a)  -- goroutine-backed, first error short-circuits
Task.lazy : (() -> a) -> Task err a                      -- defer computation
List.parallelMap : (a -> b) -> List a -> List b          -- pure goroutine map
```

## Go FFI / Interop Model

### Golden Rule: Users never write FFI code

Pipeline: `sky add pkg` → inspector extracts types → compiler classifies functions → generates `.skyi` + Go wrapper with panic recovery → DCE strips unused → `sky install` auto-generates missing bindings. Large packages (>50KB) use `sky-ffi-gen` for usage-driven bindings.

### Type Mapping

**Every FFI call returns `Result Error T`.** The boundary is a trust
boundary — same trust-boundary discipline as a typed FFI port. See `docs/ffi/boundary-philosophy.md`.

This applies UNIFORMLY: method calls, constructors (`newX`), field
getters, field setters, and package-level var reads all return
`Result Error T`. No exceptions — an infallible getter still needs to
survive nil receivers and concurrent data races, and we don't want
callers checking "is this the kind of FFI that can fail?". Runtime
wrappers: `SkyFfiFieldGet` / `SkyFfiFieldSet` wrap in `Ok(value)` on
success, `Err(ErrFfi _)` on nil / missing field. Generated T-variants
(`Go_Pkg_fieldT`) return `SkyResult[any, T]` so static callers and
`|> Result.andThen` chains see the same shape. Generated any-variants
(`Go_Pkg_field`) delegate to the reflect helpers.

| Go return | Sky type |
|---|---|
| `string` / `int`/`int64` / `float64` / `bool` (element types) | `String` / `Int` / `Float` / `Bool` |
| `T` (single, no error) | `Result Error T` |
| `*T` (single pointer, no error) | `Result Error T` (opaque; nil-deref → Err via recover) |
| `(T, error)` / `error` | `Result Error T` / `Result Error ()` |
| `(T, bool)` (comma-ok) | `Result Error (Maybe T)` |
| `(T, *NamedErr)` where NamedErr implements error | `Result Error T` |
| `(T, U)` (no error/bool) | `Result Error (T, U)` |
| `*sql.DB` / `[]T` / `map[string]V` | `Result Error Db` / `Result Error (List T)` / `Result Error (Dict String V)` |
| Go struct / Go interface | Opaque type (constructor + getters + setters / method bindings, all wrapped in Result) |
| void | `Result Error ()` |

Bare `*T` returns are NOT wrapped in Maybe — Go SDK builder chains
(Firestore, Stripe) rely on chaining pointer returns. Defer-recover
catches downstream nil-deref and surfaces `Err(ErrFfi(...))`.

Nil-receiver checks are added to every method/getter/setter wrapper —
calling on a nil opaque returns `Err(ErrFfi "nil receiver: T.M")` instead
of panicking.

### Opaque Struct Pattern (Builder)

Go structs are opaque — use generated constructors and pipeline setters (value first, struct second for `|>`). Every FFI call returns `Result Error T`; the example below shows the typical "stitch values out of Results then call" pattern using `Result.andThen`:

```elm
-- Constructor: newTypeName () -> Result Error TypeName
-- Getter: typeNameFieldName : TypeName -> Result Error FieldType
-- Setter: typeNameSetFieldName : FieldType -> TypeName -> Result Error TypeName

createSession : String -> Result Error CheckoutSession
createSession successUrl =
    Stripe.newCheckoutSessionParams ()
        |> Result.andThen (Stripe.checkoutSessionParamsSetMode "payment")
        |> Result.andThen (Stripe.checkoutSessionParamsSetSuccessURL successUrl)
        |> Result.andThen Stripe.newCheckoutSession
```

Pointer fields auto-wrapped — pass plain values. For nested structs, build inner first. Boundary failure (panic, type mismatch) surfaces as `Err`; user code chains via `Result.andThen` / `withDefault` / `case`.

## Sky.Live

Server-driven UI with the TEA architecture (model / update / view / subscriptions):
```elm
main =
    Live.app
        { init = init, update = update, view = view, subscriptions = subscriptions
        , routes = [ route "/" HomePage, route "/about" AboutPage ], notFound = HomePage
        }
```
HTTP-first (full HTML on load, patches on events), SSE subscriptions, session stores (memory/SQLite/Redis/PostgreSQL/Firestore), type-safe events, VNode diffing, security (cookies, rate limiting, CORS).

### Async Commands (Cmd.perform)

`update` returns `(Model, Cmd Msg)`. Use `Cmd.perform` to run long-running Tasks in background goroutines — results are dispatched back to `update` via SSE:

```elm
type Msg = FetchData | DataLoaded (Result Error String)

update msg model =
    case msg of
        FetchData ->
            ( { model | loading = True }
            , Cmd.perform (Http.get "/api/data") DataLoaded
            )
        DataLoaded result ->
            ( { model | loading = False, data = Result.withDefault "" result }
            , Cmd.none
            )
```

| Function | Type | Description |
|----------|------|-------------|
| `Cmd.none` | `Cmd msg` | No-op (most update branches) |
| `Cmd.perform` | `Task err a -> (Result err a -> msg) -> Cmd msg` | Run task async, dispatch result as Msg |
| `Cmd.batch` | `List (Cmd msg) -> Cmd msg` | Run multiple commands concurrently |

Concurrency: commands run in goroutines with session locking (same as subscriptions). Model is read fresh from the session store on completion — safe for multi-instance deployments.

### View-event bindings: radio groups + checkboxes

Wire-level events dispatch these args (see `__skyExtractArgs`):

| Event | Element | Args sent |
|---|---|---|
| `click`, `focus`, `blur`, `mouseover`/`mouseout`, `mousedown`/`mouseup` | any | `[]` (just the Msg) |
| `input`, `change` | `<input type="checkbox">` | `[checked : Bool]` |
| `input`, `change` | `<input type="radio">` | `[checked : Bool]` (always `True` at selection — usually not what you want) |
| `input`, `change` | `<input type="number">`/`range` | `[value : Float]` |
| `input`, `change` | text inputs, `<textarea>`, `<select>` | `[value : String]` |
| `submit` | `<form>` | `[formData]` — accepts `Dict String String` OR a typed record alias (v0.9.8+) |
| `keydown`/`keyup`/`keypress` | any | `[key : String]` |

**Radio convention: use `onClick` on each label/input, not `onInput`.** A radio's `input` event fires on selection but reports `[checked=True]` (a boolean), not the chosen `value`. Binding a typed constructor like `UpdateRole : String -> Msg` to `onInput` would get a `Bool` at runtime and the dispatch drops the event with a `Msg decode error` in the log.

```elm
-- Preferred: one fully-applied Msg per radio, dispatched on click.
choiceRow =
    label [ for "role-guardian", onClick (UpdateRole "guardian") ]
        [ input [ type "radio", name "role", value "guardian", id "role-guardian" ] []
        , text "Guardian"
        ]
```

The `for`/`id` pairing lets the browser toggle the radio natively on label click; the `onClick` on the label carries the fully-applied Msg (zero wire args) so no type coercion happens server-side. Same pattern works for checkbox groups when you want per-choice Msg variants.

### Forms with passwords (and other sensitive inputs)

**Use `onSubmit` with form data, not `onInput` per keystroke.** The pattern:

```elm
type alias AuthCreds =
    { email : String, password : String }

type Msg
    = UpdateEmail String
    | DoSignIn AuthCreds

view model =
    form [ onSubmit DoSignIn ]
        [ input
            [ type "email"
            , name "email"            -- required: name is the formData key
            , value model.email       -- email is fine to round-trip via Model
            , onInput UpdateEmail
            ] []
        , input
            [ type "password"
            , name "password"
            -- no `value` attr (don't round-trip the secret through DOM)
            -- no `onInput`     (don't dispatch per keystroke)
            ] []
        , button [ type "submit" ] [ text "Sign in" ]
        ]

update msg model =
    case msg of
        UpdateEmail e ->
            ( { model | email = e }, Cmd.none )

        DoSignIn creds ->
            -- creds.email and creds.password come straight from the typed
            -- record decode at the dispatch boundary (v0.9.8+).
            ( model, Cmd.perform (signIn creds) GotAuth )
```

**Why this matters**:

1. **Password manager extensions** (1Password, Bitwarden, browser autofill) watch DOM mutations on password inputs. Every server-driven re-render with `value=…` looks like the form changed and triggers a re-prompt / re-fill cycle. Submitting only on form submit eliminates that churn — the input's DOM stays untouched between user keystrokes and submit.

2. **Secret never lives in Model.** Without an `onInput UpdateAuthPassword` Msg, there's no Model field to populate, so the password never gets serialised into the session store (Redis, Postgres, etc.). It exists only in the browser DOM until the form submits, then briefly in the `DoSignIn` Msg's record argument until `update` consumes it. Compare to per-keystroke handlers where every Sky.Live session carries a partial password through every store round-trip.

3. **Race-free submit.** Per-keystroke onInput debounces (~150 ms) can drop the last keystroke if the user hits Enter before the debounce settles — the auth attempt then sees the wrong password and the user retries blind. Form submit reads the live DOM value, so whatever is in the input at submit time is what gets sent.

The `DoSignIn AuthCreds` constructor takes a typed record — v0.9.8's typed Msg dispatch decodes the wire form data directly into `State_AuthCreds_R{Email, Password}` via `json.Unmarshal`'s case-insensitive field matching. No runtime guessing, no per-Msg decoder boilerplate. Same pattern applies to API keys, credit-card details, anything you don't want resident in the session store.

### Connection status banner (v0.9.9+)

Sky.Live's runtime injects a bottom-pinned status banner separate from the user's `view`. Three states:

- **connected** — `display:none`, no chrome.
- **reconnecting** — amber bar `Reconnecting…`. Shown when the SSE connection drops or a POST `/_sky/event` fails (network blip, deploy in progress, transient 5xx). 500ms grace period before painting so a one-off blip doesn't flicker.
- **offline** — red bar `Connection lost — refresh to retry`. Reached after `SKY_LIVE_RETRY_MAX_ATTEMPTS` failed retries (default 10, ≈ 2 min of exponential backoff).

POST failures while reconnecting land in `__skyEventQueue` (FIFO, capped at `SKY_LIVE_QUEUE_MAX`); the SSE `open` handler drains the queue eagerly when the server comes back, so a click during the outage replays once the connection re-establishes. Server-side seq ordering tolerates the late delivery.

**What users see during a deploy / restart**: the page pauses, banner shows `Reconnecting…` for the duration of the cutover, then clears and any in-flight clicks replay. With a persistent session store (Redis / Postgres / Firestore / SQLite) the user's Model rides through unchanged. With the memory store, the cookie still resolves but the session is gone — the user re-inits to `init`'s output. **For production deployments with restart/deploy expected, use a persistent session store**.

Opt-out via `SKY_LIVE_BANNER=off` (or `0` / `false`) keeps the retry queue active but suppresses the chrome — useful when an app renders its own connection UI in the user's view. Override the styling via `#__sky-status { ... !important }` in the user's stylesheet; the runtime uses inline styles + max z-index so user CSS wins on conflict.

### Dispatch error handling

`dispatch()` is wrapped in `defer/recover`. Two classes are handled cleanly:

1. **Msg decode errors** — client's wire args don't fit the Msg constructor's parameter types (e.g. radio `onInput` bound to `String -> Msg`). `applyMsgArgs` detects the mismatch before `reflect.Call`, logs a targeted message (constructor name, expected type, actual arg), and drops the event. No model mutation.
2. **User-code panics** — anything inside `update`/`view`/`guard` that panics (typed-FFI boundary mismatches, nil deref in user code, etc.) is caught, stack-traced to stderr, and the event is dropped. Session state stays consistent so the next event dispatches normally.

Both paths return an empty body; the client sees an empty patch list and the DOM is unchanged. Check server logs for `[sky.live] dispatch panic recovered` or `[sky.live] Msg decode error` when debugging an event that "does nothing".

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
Routes: `get/post/put/delete/any` | Groups with prefix | Cookies (HttpOnly, Secure, SameSite) | Extractors: `param`, `queryParam`, `header`, `getCookie` | Responses: `text`, `json`, `html`, `withStatus`, `redirect` | Middleware: `Handler -> Handler`

## Std.Ui — typed no-CSS layout DSL

Layered above `Std.Html`; renders to inline-styled HTML on the server side and Sky.Live's wire ferries diffs to the browser. Pick `row` / `column` / `el` for layout, attach typed attributes from `Background` / `Border` / `Font` / `Region` sub-modules, never write CSS. Full user-facing reference: `docs/skyui/overview.md`. Prior-art attribution: `NOTICE.md`.

```elm
import Std.Ui as Ui
import Std.Ui exposing (Element)
import Std.Ui.Background as Background
import Std.Ui.Border as Border
import Std.Ui.Font as Font

view : Model -> any
view model =
    Ui.layout []
        (Ui.row
            [ Ui.spacing 12, Ui.padding 16
            , Background.color (Ui.rgb 255 102 0)
            , Font.color (Ui.rgb 255 255 255)
            , Border.rounded 4
            ]
            [ Ui.button [] { onPress = Just Decrement, label = Ui.text "−" }
            , Ui.el [ Font.size 24, Font.bold ] (Ui.text (String.fromInt model.count))
            , Ui.button [] { onPress = Just Increment, label = Ui.text "+" }
            ])
```

**Three idioms AI tooling MUST get right when writing Sky.Ui code:**

1. **Forms with sensitive inputs use `Ui.form` + `Ui.onSubmit DoSignIn`, NOT `onInput` per keystroke on password fields.** The wire driver decodes formData `{"username":"...","password":"..."}` into a typed `LoginForm` record via case-insensitive `json.Unmarshal`. Three wins: password manager extensions stop seeing DOM mutations on every render, the secret never enters Model so never serialises into Redis/Postgres/Firestore session stores, race-free submit reads live DOM not a debounced keystroke. The username field MAY round-trip via `value` + `onInput`; the password field MUST NOT. See `examples/19-skyforum/src/View/Login.sky` for the canonical shape.

2. **For real `<input>` elements, use `Ui.input`, NOT `Ui.el [ htmlAttribute "type" "text" ]`.** `Ui.el` builds `Node` which renders as `<div>` — browsers ignore `type=`/`value=` on non-input elements and never fire input events on a div. `Ui.input` builds `TaggedNode "input"` which the renderer routes to a real `<input>` with self-closing void emission.

3. **For Std.Ui-heavy modules (~25+ polymorphic `Element Msg` helpers + many nested calls), split the view layer across multiple modules.** A single monolithic Main.sky can blow the HM type-checker heap (CLAUDE.md Limitation #17 — "HM type-checker heap exhaustion on Std.Ui-heavy modules"). The canonical split is `State.sky` (types + pure helpers, no Std.Ui imports) / `Update.sky` / `View/Common.sky` / one View module per page / `Main.sky` dispatcher. `examples/19-skyforum`'s 8-module form delivers the full Reddit-style feature surface and type-checks in 1.11 s / 369 MB; the equivalent monolithic `test-fixtures/heap-bound-fence.sky` (kept as the regression artefact) allocates 2.6 GB/s pre-fix.

**Surface highlights** (full table in `docs/skyui/overview.md`):
- Layout: `el` / `row` / `column` / `paragraph` / `textColumn` / `text` / `none` / `html` (escape hatch wrapping a Std.Html VNode)
- Sized elements: `link` (with `{url, label}` cfg), `image` (with `{src, description}` cfg), `button` (with `{onPress, label}` cfg), `input` (real `<input>` element), `form` (with `onSubmit msg`)
- Length: `px` / `fill` / `fillPortion` / `content` / `shrink` / `min` / `max`
- Padding: `padding` / `paddingXY` / `paddingEach` / `spacing`
- Attributes: `width` / `height` / `centerX` / `centerY` / `alignLeft` / `alignRight` / `alignTop` / `alignBottom` / `pointer` / `style` / `class` / `htmlAttribute` / `name`
- Overflow: `clip` / `clipX` / `clipY` / `scrollbars` / `scrollbarX` / `scrollbarY`
- Nearby: `above` / `below` / `onLeft` / `onRight` / `inFront` / `behind` (absolute-positioned overlays for tooltips, popovers, badges; renderer wraps parent with `position: relative`)
- Events: `onClick msg` / `onSubmit msg` / `onInput (String -> msg)` / `onChange (String -> msg)` / `onFocus msg` / `onMouseOver msg` / `onMouseOut msg` / `onKeyDown msg` / `onFile (String -> msg)` / `onImage (String -> msg)`
- File/image hints: `fileMaxSize Int` (bytes) / `fileMaxWidth Int` / `fileMaxHeight Int` (resize before upload)
- Colour: `rgb Int Int Int` / `rgba Int Int Int Float` / `white` / `black` / `transparent`
- Sub-modules:
  - `Std.Ui.Background` — `color` / `image url` / `linearGradient angle stops` / `gradient css`
  - `Std.Ui.Border` — `color` / `width` / `widthEach {top,right,bottom,left}` / `rounded` / `solid` / `dashed` / `dotted` / `shadow {offsetX,offsetY,blur,spread,color}` / `glow blur color` / `innerShadow {…}`
  - `Std.Ui.Font` — `color` / `family` / `size` / `weight` / `bold` / `semiBold` / `regular` / `light` / `extraBold` / `black` / `italic` / `underline` / `letterSpacing em` / `wordSpacing em` / `sansSerif` / `serif` / `monospace`
  - `Std.Ui.Region` — semantic landmarks routed to real HTML tags by the renderer: `heading n` (`<h1>`..`<h6>`) / `mainContent` (`<main>`) / `navigation` (`<nav>`) / `footer` (`<footer>`) / `aside` (`<aside>`) / `label text` (`aria-label`) / `announce` (`aria-live="polite"`) / `announceUrgently` (`aria-live="assertive"`)
  - `Std.Ui.Input` — typed form controls: `button` / `text` / `multiline` / `email` / `username` / `search` / `currentPassword {show: Bool}` / `newPassword {show: Bool}` / `checkbox` / `radio {options, selected, …}` / `radioRow {…}` / `slider {min, max, step, value, …}` + `option value labelEl` (RadioOption ctor) + `labelAbove` / `labelBelow` / `labelLeft` / `labelRight` / `labelHidden` / `placeholder`
  - `Std.Ui.Lazy` — `lazy` / `lazy2` … `lazy5` (no-op wrappers today; runtime memo deferred)
  - `Std.Ui.Keyed` — `keyed` (emits `sky-key` for diff identity)
  - `Std.Ui.Responsive` — `classifyDevice` / `adapt {phone, tablet, desktop}`

**File / image upload pattern:**
```elm
type Msg = ... | AvatarSelected String | ...

Ui.input
    [ Ui.htmlAttribute "type" "file"
    , Ui.htmlAttribute "accept" "image/*"
    , Ui.onImage AvatarSelected           -- AvatarSelected : String -> Msg
    , Ui.fileMaxSize   2_000_000          -- 2MB browser-side cap (not security)
    , Ui.fileMaxWidth  800                -- auto-resize + JPEG @ 0.85 before upload
    , Ui.fileMaxHeight 800
    ]
```
Callback receives the data URL (`data:image/jpeg;base64,...`) as a single `String`. Decode with `Std.Encoding.base64Decode` → upload via `Http.post`. Server-side, ensure `[live] maxBodyBytes` in `sky.toml` is ≥ your `fileMaxSize` (default 5 MiB).

**`Ui.none` workaround:** the canonicaliser strips the type parameter from cross-module references to `Std.Ui.none` today — use `Ui.text ""` where you'd want `Ui.none`. An empty Text node renders identically (just an empty inline span).

## Language Syntax

Sky's surface syntax is deliberately Elm-compatible — modules, `case`/`let`/`if`, record literals, ADTs, `|>` pipelines port over with mechanical edits. See `NOTICE.md` for prior-art attribution.

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

Key syntax: `|>` `<|` pipelines | `::` cons | `\x -> x + 1` lambdas | `let...in` | `case...of` with exhaustiveness | `{ record | field = value }` update | `module M exposing (..)` / `import M as Alias exposing (func)`

### Multiline Strings

Triple-quoted strings preserve newlines and indentation. Interpolation uses `{{expr}}`:

```elm
html =
    """<div class="card">
    <h1>{{title}}</h1>
    <p>{{description}}</p>
</div>"""
```

Single braces `{` are literal — safe for JavaScript, CSS, JSON, SQL. Interpolation expressions can be identifiers, field access (`{{record.field}}`), qualified names (`{{String.fromInt n}}`), or function calls (`{{String.fromInt count}}`).

## Examples

| # | Name | Description |
|---|------|-------------|
| 01 | hello-world | Basic println |
| 02 | go-stdlib | Go stdlib (crypto, encoding, time, http) |
| 03 | tea-external | TEA with external packages (UUID, godotenv) |
| 04 | local-pkg | Multi-module with local imports |
| 05 | mux-server | HTTP server with gorilla/mux |
| 06 | json | JSON encoding/decoding |
| 07 | todo-cli | SQLite CLI todo app |
| 08 | notes-app | Full CRUD web app with database |
| 09 | live-counter | Sky.Live counter with SSE |
| 10 | live-component | Sky.Live component protocol |
| 11 | fyne-stopwatch | Desktop GUI with Fyne |
| 12 | skyvote | Full Sky.Live voting app with auth |
| 13 | skyshop | E-commerce: Stripe, Firebase, i18n |
| 14 | task-demo | Task effect boundary demo |
| 15 | http-server | Sky.Http.Server with routing + cookies |
| 16 | skychess | Sky.Live chess game with AI, SQLite persistence |
| 17 | skymon | Sky.Live monitoring dashboard with metrics, alerts |
| 18 | job-queue | Async Cmd.perform demo with Time.sleep, Random.int, Cmd.batch |
| 19 | skyforum | Reddit/HN-style forum on Std.Ui — 8 modules, per-user vote tracking + downvote, threaded comments, form-driven password sign-in |

## Compiler Optimisation Strategy (keep up to date)

**This section must be kept current.** Any session changing the compiler pipeline, codegen, or build system must update it.

### Current Optimisations (implemented)

1. **Stale file cleanup** — `rm -f sky-out/sky_ffi_*.go sky-out/sky_*.go sky-out/live_init.go` at build start
2. **Empty wrapper deletion** — DCE deletes FFI wrapper files with no remaining functions
3. **Native DCE** (`bin/sky-dce`) — single-pass wrapper + main.go DCE, 27s → 1s
4. **Var declaration preservation** — DCE preserves all `var` decls (type constructors, FFI aliases)
5. **Large .skyi filtering** (`bin/skyi-filter`) — Stripe SDK: 147K→9K lines in 90ms
6. **Combined FFI imports** — deduplicate before loading (was parsing 8.4MB Stripe SDK 40+ times)
7. **FFI light path** — skip type-check + lowering for `.skyi`, generate constructors + wrapper vars only
8. **Parallel module lowering** — `List.parallelMap` with goroutines, ~300% CPU
9. **Parallel FFI loading/wrapper copying** — concurrent `skyi-filter` and file I/O
10. **String.join in hot paths** — O(n²) → O(n) in lowerer
11. **Incremental compilation** — `.skycache/lowered/` cache, skip type-check + lowering on warm builds
12. **Usage-driven FFI** (`sky-ffi-gen`) — Stripe 8896 types → only referenced symbols
13. **Runtime optimisations** — `sky_equal` type-switch, `sky_asString` via `strconv`, ASCII fast paths
14. **ADT structs** (v0.7.10+) — `SkyADT{Tag: N, SkyName: "Name", V0: val}`, integer tag matching, struct field access
15. **Type annotations** — `// sky:type funcName : Type` comments on all declarations
16. **Single-binary release** — `tools/sky-ffi-inspect/` Go source embedded via TH (`Sky.Build.EmbeddedInspector`); materialises + go-builds to `$XDG_CACHE_HOME/sky/tools/sky-ffi-inspect-<hash>/` on first `sky add`. Cold-start ~4s, warm instant. Content-hash keys the cache dir so `sky upgrade` auto-invalidates. Dev workflow still picks up `bin/sky-ffi-inspect` via ancestor walk so contributors don't rebuild per branch.

### Historical Fixes (all resolved)

All issues below are FIXED — listed for context if debugging regressions:

- **Formatter** — 7 fixes for output compatibility; all 32 modules format+compile; idempotent output
- **Parser** — `(expr).field` support, `parseCaseBranches` nesting fix (`branchCol` tracking), long-line splits, `getLexemeAt1` field access
- **Lowerer** — nested case IIFEs, ADT sub-pattern matching, cons pattern `len == N`, string pattern double-quoting, local variable shadowing by `exposedStdlib` (check `paramNames` first), hardcoded `Css.` prefix vs import aliases, let-binding hoisting (3-round bootstrap)
- **Type checker** — working since v0.7.2; inner case extraction across Types/Unify/Infer/Adt modules
- **FFI** — `.skycache` path resolution, Task boundary, Go generics filtered, keyword conflicts, IIFE invocation, type alias emission, interface pointer dereference, zero-arity params, callback function types, method/constant collision, slice-of-pointer types, namespace collisions
- **Lexer** — `alias` removed from keywords (contextual only)
- **Type safety audit** — 33 gaps fixed: case fallthrough panics, FFI panic recovery, float-aware arithmetic, rune-based strings, numeric sorting, typed FFI boundaries, session ADT rebuilding, exhaustiveness checking

### Known Limitations (v0.11.0)

These are current compiler limitations users must work around. Items marked ~~strikethrough~~ + FIXED are kept for context (their entries explain the failure mode + the fix); the active list is everything not struck through.

1. **No anonymous records in function signatures** — Record types must be defined as type aliases; inline `{ field : Type }` in annotations is not supported. Typed codegen cannot name an un-aliased record for struct emission.
2. **No higher-kinded types** — No `Functor`, `Monad`, etc. Use concrete types. (Intentional — Hindley-Milner only.)
3. **No `where` clauses** — Use `let...in` instead. (Intentional.)
4. **No custom operators** — Only built-in operators (`|>`, `<|`, `++`, `::`, etc.). (Intentional.)
5. **Negative literal arguments need parentheses** — `f -1` parses as `f - 1` (subtraction). Use `f (-1)` — matches Elm's behaviour.
6. **`Dict.toList` returns string keys** — Sky's `Dict` uses `map[string]any` internally, so `Dict.toList` returns string keys even for `Dict Int v`. Arithmetic on these keys silently produces 0. **Workaround**: iterate over known key ranges with `Dict.get` instead of using `Dict.toList`.
7. **`sky check` does not fully model Go interface satisfaction** — Opaque FFI types unify with each other (v0.7.21 fix), but the checker still cannot verify that a concrete Go type (e.g. `Label`) satisfies a named Go interface (e.g. `CanvasObject`). Calls like `Fyne.windowSetContent window label` may fail `sky check` but compile and run correctly.
8. **Zero-arg FFI functions take `()` in Sky, not bare-name** — the inspector emits a `() -> R` signature for every zero-param Go function (`emitSkyiFn` in `src/Sky/Build/FfiGen.hs:1542`), so the Sky-side call is `Uuid.newString ()` / `Stripe.newCheckoutSessionParams ()` / `FyneApp.new ()`. (Sky-side **kernel** zero-arity bindings — `Uuid.v4`, `Time.now`, etc. — are the opposite: kernel-registered as bare values, called without `()`. The distinction is FFI-vs-kernel, not zero-arg-or-not.)
9. **Let bindings with parameters after multi-line case** — A let binding like `mark j = expr` after a `case ... of` in the same let block causes the parser to misinterpret it as a new top-level declaration. **Workaround**: use lambdas (`\j -> expr`) or extract to a top-level function.
10. **Zero-arity functions reading env vars** — Zero-arity functions are memoised and their import aliases evaluate at Go init time (before `.env` is loaded). If a zero-arity function reads `Os.getenv`, the value is cached as empty. **Workaround**: add a dummy `_` parameter: `getConfig _ = Os.getenv "KEY"`.
11. **`exposing (Type(..))` doesn't expose ADT constructors for user modules** — `import MyModule exposing (MyType(..))` does not bring `MyType`'s constructors into scope for user-defined modules. The canonicaliser only resolves constructors when full dep info is available (kernel modules work). **Workaround**: use `import MyModule exposing (..)` to expose everything, or qualify constructors: `MyModule.MyConstructor`.
12. **Typed codegen keeps `any` inside runtime kernels.** The typed surface is zero-`any`, but the v1.0-era reflection dispatch lives on inside `Dict_*`, `List_map`, `Html_render`. User code never sees it; observable cost is ~5% CPU vs. a hypothetical fully-generic runtime. Scheduled for the post-v1.0 runtime port (see the Typed Codegen TODO section).
13. **Zero-arg `Css.*` constants require `()`** — `Css.zero`, `Css.auto`, `Css.none`, `Css.transparent`, `Css.inherit`, `Css.initial`, `Css.borderBox`, `Css.systemFont`, `Css.monoFont`, `Css.userSelectNone` are kernel bindings exposed as `() -> String` to sidestep zero-arity memoisation (which would interact badly with Go's `init()` ordering). Write `Css.margin (Css.zero ())` not `Css.margin Css.zero` — the latter serialises the function pointer (e.g. `0xc00001c0a0`) into the stylesheet. Sky's type checker doesn't flag the miss today because the `() -> String` surface unifies with any String slot via HM's let-polymorphism default. **Pattern**: any `Css.X` that names a CSS keyword (not a value constructor like `px`/`rem`/`hex`) takes `()`.
14. **`import X as Alias` leaks the alias into codegen for exposed record/ADT types** — `import Lib.Db as Chat` causes `Message` to be emitted as `Chat_Message_R` instead of the module-prefixed `Lib_Db_Message_R`, breaking cross-module resolution when another module imports it unaliased. **Workaround**: use `import Lib.Db exposing (Message, …)` (or qualify without alias). Aliases on modules that only expose functions (no types) are unaffected.
15. **`let` bindings don't support forward references** — Helpers inside a `let` block must be defined *before* their consumers in source order. `let writeAll db = … insertRow db ts …; insertRow db ts = …` fails `go build` with `undefined: insertRow` even though both are visible at the let-block scope. **Workaround**: reorder so dependencies come first. Surfaced when refactoring 18-job-queue/saveSnapshot's nested-andThen chain into named helpers; the lowerer emits each let binding sequentially without the let-as-mutually-recursive-rec semantics that Haskell `where` (and Sky's own top-level decls) provides. Worth a future fix — the canonicaliser already knows the full set of let names in the block.
16. **Some kernel functions still missing HM type signatures** — Status as of 2026-04-27. **Dangerous-class gap is closed** (commits `f77888c` + 2026-04-27 follow-up): the `feat/effect-boundary-audit` branch added 57/66, then 10 more (`Server.static`, `Middleware.{withCors, withLogging, withBasicAuth, withRateLimit}`, `Http.{get, post}`, `JsonDec.map4`, `JsonDecP.{custom, requiredAt}`). `System.cwd` and `System.exit` were already registered (the v0.10.0 renames of `Os.cwd` / `Process.exit`). Regression fence: `test/Sky/Build/KernelSigCoverageSpec.hs` source-greps each entry — drops trip the spec at build time. **Remaining dangerous gap**: `Ffi.callTask` (deferred — its `String -> List a -> Task Error b` shape needs more thought about whether `List a` should be `List any` heterogeneous; lower priority since Sky.Ffi is the explicit FFI escape hatch). **Bare-type sweep landed 2026-04-27**: 46 additional sigs across `Char.*` (6 — predicates + case helpers), `Crypto.*` (5 — sha256/sha512/md5/hmacSha256/constantTimeEqual), `Path.*` (4 — base/dir/ext/isAbsolute), `Math.*` (5 — e/sin/cos/tan/log), `Time.*` (6 — format helpers + addMillis/diffMillis), `String.*` (6 — casefold/equalFold/isEmail/isUrl/trimEnd/trimStart), `Css.*` (19 — Sprintf-returning length/transform/value helpers + `()`-keyword constants per Limitation #13). All return Go primitives (`String` / `Int` / `Bool` / `Float`); the runtime functions were inspected per-entry to ensure the sig matches the actual return shape. **Deliberately NOT added** (would cause `rt.Coerce[T]` runtime panic at the boundary): `Css.*` helpers returning opaque `cssRule` / `cssProp` / `cssMediaRule` (~100 entries), `Html.*` returning `vnode` (66), `Attr.*` returning `attr` struct (49), `Event.*` returning `eventPair` (24). These need the typed-codegen runtime port (post-v1.0 — see "Typed Codegen TODO") before they can be safely sigged. **Symptom of a dangerous-class regression** (kernel sig disagreeing with runtime return shape) is a runtime panic like `rt.AsInt: expected numeric value, got rt.SkyMaybe[interface{}]` — when you see this, check `lookupKernelType` (`src/Sky/Type/Constrain/Expression.hs`) against the matching `runtime-go/rt/*.go` helper.
17. **~~HM type-checker heap exhaustion on Std.Ui-heavy modules.~~ FIXED 2026-04-27.** **Root cause turned out NOT to be a compiler-internal quadratic** — bisect showed it was a single ill-typed line in `sky-stdlib/Std/Ui/Input.sky`'s `inputBase` helper: `:: Ui.onInput (cfg.onChange cfg.text)` — passing a `Msg` (the result of applying the callback to the text) where `Ui.onInput : (String -> msg) -> Attribute msg` expects a function. Because `inputBase` was polymorphic over `msg`, HM didn't reject immediately; it tried to unify `msg = (String -> someMsg)` and propagated that constraint through every Std.Ui call site, hitting combinatorial explosion (~2.6 GB/s allocation, 4-5 GB RSS in 10s — enough to lock the Mac, which is why `scripts/mem-guard.sh` exists). The fix shipped in `dc1359b` (2026-04-26): `Ui.onInput cfg.onChange` — pass the function directly. Bisect verification: reverting only `Std.Ui.Input.sky` to its pre-`dc1359b` state reproduces the OOM exactly (783 MB allocated, 543 MB max residency at 256 MB cap); current state allocates 122 MB / 1.6 MB residency. Regression fence lives in `test/Sky/Build/HeapBoundedHmSpec.hs` — re-runs `sky check` on `examples/19-skyforum/test-fixtures/heap-bound-fence.sky` (the 689-line monolithic reproducer, kept specifically for this) under `+RTS -M256M`. **Defensive bound landed 2026-04-27** (commit follows this entry — see git log for the exact hash). The HM solver now caps total `solveHelp` invocations per `solve` call at `SKY_SOLVER_BUDGET` steps (default 5,000,000 — ~50× the largest legitimate Std.Ui-heavy module measured). When the cap is exceeded, the solver short-circuits with a clear, actionable `TYPE ERROR: constraint solver exceeded budget (N operations)` rather than letting unbounded heap consumption OOM the host. The error message points at the most likely cause (mistyped polymorphic helper) and includes the env-var override path. **Both stdlib-side and user-side pathological constraint shapes are now covered** — the HeapBoundedHmSpec catches stdlib regressions at test time; the defensive bound catches user-side patterns at compile time. SKY_SOLVER_BUDGET=0 disables the bound (escape hatch — debug only). Regression specs: `test/Sky/Build/SolverBudgetSpec.hs` (default budget passes / low budget trips with clear error / 0 disables). **Workarounds for non-skyforum code that hits a similar shape**: (a) inline event handlers at use sites instead of via typed `(String -> Msg)` helper functions, (b) prefer flat `List.map` rendering over mutually-recursive view helpers, (c) split heavy view modules per the `examples/19-skyforum` 8-module pattern. The `scripts/mem-guard.sh` rule in "Memory Safety (Non-Negotiable)" stays in force as a host-side safety net.
18. **~~Typed codegen monomorphises `(String -> msg)` callback params + empty-list-in-typed-ctor-arg.~~ FIXED 2026-04-26.** Two related bugs in one limitation, both closed by `[compile] fix Limitation #18` (commit `fa6cbf0`):
    - **Empty-list-in-typed-ctor-arg**: `Item 1 "x" []` against a `type alias Item = { ..., tags : List String }` shipped `Item(1, "x", []any{})` — `go build` rejected `[]any{} as []string`. Root cause: `collectFuncTypesWith` only registered param types for `Can.TypedDef` bindings; auto-generated record ctors are unannotated `Can.Def`s, so `_cg_funcParamTypes[Item]` was empty and `coerceArg` short-circuited. Fix: walk `Can._aliases` too and register `(qualName, fieldGoTys, qualName ++ "_R")` for each `DataRecord` alias. Empty list now coerces via `rt.AsListT[string]([]any{})` — fully typed (Go generic, no reflect). Regression test: `test/Sky/Build/RecordCtorEmptyListSpec.hs`.
    - **`(String -> Msg)` helper callback param**: a helper `textField : String -> String -> (String -> Msg) -> Element Msg` got `cb func(string) any` in its emitted Go sig (load-bearing widening — Sky lambdas always lower to `func(any) any` and Go has no function-type covariance, so the helper sig must accept the widest shape). But the call-site `textField "u" "" Msg_X` shipped the typed `Msg_X : func(string) Msg` raw — `go build` rejected. Root cause: `safeReturnTypeWith` returned bare `"any"` for `T.TLambda`, so `_cg_funcParamTypes[textField]` knew the param was "any" and `coerceArg` short-circuited. Fix: `safeReturnTypeWith` now renders `T.TLambda` as `func(X) any` (matching what `renderHofParamTy` emits at sig time) — this gives `coerceArg` the `func(` prefix it needs to route call-site args through `rt.Coerce[func(X) any]`. The reflect.MakeFunc adapter handles both Sky lambdas (`func(any) any`) and typed Msg ctors (`func(string) Msg`) uniformly. Pragmatic — not "fully typed" in the strict sense (the `any` tail return is a structural compromise) but unblocks user code today; truly fully-typed HOFs need lambda lowering to preserve types (post-v1 work in "Typed Codegen TODO"). Regression test: `test/Sky/Build/HofTypedMsgSpec.hs`. The pre-existing `CompileSpec` "Result-typed lambda params" test (line 80-97) is the regression fence — `renderHofParamTy` is unchanged so Bug #1 from sky-chat ep07 stays fixed.

### Recently Fixed (listed for regression context)

#### v0.11.x (post-v0.11.0 — real-world Std.Ui port findings round 3, 2026-04-27)

- **Qualified type annotation under `import M as Alias` ignored the alias map** — FIXED. `resolveTypeQual` in `src/Sky/Canonicalise/Type.hs` only handled hardcoded built-in module qualifiers (List/Maybe/Result/Task/Dict/Set) and fell through to a literal `Canonical qualifier` for everything else. So under `import Std.Ui as Ui`, an annotation like `mkColor : Ui.Color` canonicalised to `TType (Canonical "Ui") "Color" []`, while bare `Color` (via `import Std.Ui exposing (Color)`) canonicalised to `TType (Canonical "Std.Ui") "Color" []`. HM rejected the two as different types with the cryptic `Type mismatch: Color vs Color` (same display, different identity). Fix: thread an `aliasMap` (built from `Src._imports srcMod` via `buildImportAliasMap`) through `canonicaliseTypeAnnotationWithAliases` so `resolveTypeQualWith` consults it before the literal-qualifier fallback. Regression spec: `test/Sky/Canonicalise/QualifiedTypeAliasSpec.hs`.

- **Wrong-shape Msg constructor passed to `Ui.onInput` was silently no-op'd at codegen** — RESOLVED INDIRECTLY by the round-2 closed-record exactness + externals-filter-dropped fixes. Pre-round-2: `Ui.input [ Ui.onInput DoSignIn ]` (where `DoSignIn : AuthCreds -> Msg`) passed sky check silently and the runtime's `makeFuncAdapter` reflect adapter substituted a no-op handler. Post-round-2 (already shipped in `47f3a43`): sky check correctly rejects with `Foreign 'DoSignIn': (AuthCreds) -> Msg vs String -> Msg`, pointing at the wrong-shape arg. Verified during round-3 probing — no additional fix needed.

#### v0.11.x (post-v0.11.0 — real-world Std.Ui port findings, 2026-04-27)

- **`Std.Ui.onSubmit` rejected `(record -> Msg)` constructors in-module** — FIXED. The kernel + wrapper were typed `forall msg. msg -> Attribute msg`, which forced `msg = (record -> Msg)` when callers passed e.g. `Ui.onSubmit DoSignIn` (where `DoSignIn : LoginForm -> Msg`). Surrounding `Element Msg` annotations then failed unification with `Element ((LoginForm) -> Msg)`. Asymmetry: cross-module callers (e.g. `examples/19-skyforum/src/View/Login.sky`) accidentally passed because the externals path uses a more permissive typing. Runtime always handled both shapes via `applyMsgArgs` + `decodeMsgArg`'s `reflect.New(paramT)` + `json.Unmarshal` — only HM was rejecting valid Sky code. Fix: widen both `Std.Ui.onSubmit` and `Std.Ui.Events.onSubmit` to `(a -> Attribute b)`. Regression spec: `test/Sky/Type/UiOnSubmitTypedRecordSpec.hs`.

- **Sky function names colliding with Go keywords sanitised at definition only, not at call sites** — FIXED. Definition path (`emitFunctionDecl` ~line 2048 of `Sky/Build/Compile.hs`) used `goSafeName` so a Sky `go : Int -> Int` emitted as `func go_(n int)`. But the matching `Can.Call (VarTopLevel _ name)` branch (~line 3633) used the raw `name` for Main-module callees, emitting `go(rt.CoerceInt(41))` which Go's parser interpreted as a goroutine launch (`syntax error: unexpected keyword go, expected expression`). Fix: apply `goSafeName` in the call-site branch too (both Main-module and cross-module paths, defensively). Affects all Go reserved words listed in `reservedGoNames`: `go`/`defer`/`chan`/`make`/`len`/`type`/`func`/etc. Regression spec: `test/Sky/Build/GoKeywordCollisionSpec.hs`.

- **`Std.Ui.Font` missing text-alignment helpers** — FIXED. `Font.alignLeft` / `Font.alignRight` / `Font.alignCenter` / `Font.center` (alias for alignCenter, matching elm-ui) / `Font.justify` added. New `AttrFontAlign String` variant in the `Attribute` ADT with renderer dispatch `text-align: <value>;`. Note these compile to CSS `text-align` so they affect inline content, not the element's placement (`Ui.centerX` / `Ui.alignLeft` etc. handle that). Surfaced from real-world Std.Ui port wanting `Font.center` for centred text inside cards.

- **Doc bug: `Ui.min` / `Ui.max` documented but actual API is `minimum` / `maximum`** — FIXED. CLAUDE.md (line 433), `templates/CLAUDE.md` (line 1377), `docs/skyui/overview.md` (lines 71/109/110/346), `docs/stdlib.md` (line 497) all said `min/max` but the source has `minimum`/`maximum`. Same fix-up pass also clarified that `Ui.fill : Length` is bare (no arg) and `Ui.fillPortion Int` takes the proportional weight — earlier text confusingly said `fill Int`.

#### v0.11.0 (release — `feat/std-ui`)

- **Multi-line `module/import ... exposing (…)` silently drops exports** — FIXED. The exposing-list parser used `spaces` (no newlines) between items, so the canonical `sky fmt`-shape multi-line form fell through `oneOfWithFallback` returning `ExposingList []`. Imports vanished silently; module-header failures were downgraded to warnings and the build proceeded with 0 modules. Fix: `freshLine` inside the parens (newlines layout-irrelevant) + parse-errors-at-module-graph-stage-are-FATAL. Regression spec: `test/Sky/Parse/MultiLineExposingSpec.hs`.

- **Cons-with-constructor pattern doesn't check head's tag** — FIXED. `case xs of (Ctor x) :: _ -> body` lowered to a guard that only checked `len(list) >= 1`, ignoring the head's constructor. The body's bindings then assumed the head IS the matched constructor and extracted field 0 from whatever was at the head — runtime panic with `interface conversion: …` when the head was a sister variant. Fix: new `consHeadCondition` / `patternConditionForExpr` helpers in the lowerer emit a head-discriminator check joined to the length test via `&&`. Regression spec: `test/Sky/Build/ConsCtorPatternSpec.hs`.

- **Cross-branch HM with `any`-typed ADT payload** — FIXED. Distinct occurrences of `T.TVar "any"` shared a single fresh unification variable via the solver's `_varCache`. So `case x of AttrA s -> Just s | AttrB v -> Just v` (where AttrA holds String and AttrB holds `any`) collapsed `any` to String, and `AttrB 42` at construction sites failed with `Type mismatch: Int vs String`. Fix: in `Sky.Type.Solve.typeToVar`, treat `T.TVar "any"` as a wildcard — every occurrence creates a fresh unification variable, never shared. Regression spec: `test/Sky/Type/AnyWildcardSpec.hs`.

- **Tuple-pattern in lambda arg shares element types across siblings** — FIXED. `patternBindings` for `Can.PTuple` bound element types to STATIC names (`_tup_0`, `_tup_1`). These collapsed via `_varCache`, so multiple tuple destructures in the same definition shared element-type vars — `\(name, r) -> ...` and `\(name, msg) -> ...` from sibling lambdas conflated their elements, surfacing as `Variable 'msg' type mismatch`. Fix: new `patternBindingsIO` (used by `constrainLambda`) mints fresh per-occurrence type-var names + emits structural `T.CEqual` constraints tying the outer ty to the pattern's structure. Regression spec: `test/Sky/Type/TupleLambdaSpec.hs`.

- **`/=` operator panics on polymorphic generic params** — FIXED. The `/=` operator lowered to Go-native `!=`, which fails with `incomparable types in type set` for `func[T any](a, b T) ...` because `any` doesn't satisfy `comparable`. Fix: lower `/=` to the new `rt.NotEq` runtime helper (mirrors `rt.Eq` shape). Restores symmetry with `==` and unblocks polymorphic comparison helpers like `Sky.Test.notEqual`. Regression spec covered in `test/Sky/Type/TupleLambdaSpec.hs`.

- **`sky fmt` collapses long imports to single-line** — FIXED. The formatter emitted exposing clauses on one line regardless of length, AND collapsed user-written multi-line forms back to single-line on round-trip. Fix: new `fmtExposingClause` helper auto-breaks past ~100 chars (matches `elm-format` convention) for both module headers and imports. Idempotent. Regression specs in `test/Sky/Format/FormatSpec.hs` (3 new cases).

- **`sky test` for passing modules was xfail** — FIXED. The pending entry combined the two bugs above (tuple-pattern + `/=`). Both fixes flipped the spec from `xit` → `it`. The `sky test` contract is now fully tested for both passing AND failing test modules. (Was the only `pending` in the cabal sweep; now zero pending.)

- **Std.Ui surface complete** — every previously ⚠️/❌ item in the surface-coverage table now ✅ except `Std.Ui.Lazy` (no-op wrappers; runtime memo deferred). NEW: `Std.Ui.Background.{image, linearGradient, gradient}`, `Std.Ui.Border.{widthEach, solid, dashed, dotted, shadow, glow, innerShadow}`, `Std.Ui.Font.{italic, underline, letterSpacing, wordSpacing, semiBold, extraBold, black}`, `Std.Ui.Region.{aside, announce, announceUrgently}` plus renderer dispatch from `Description` to real semantic HTML tags (`<main>` / `<nav>` / `<aside>` / `<footer>` / `<h1..h6>`), `Std.Ui.{above, below, onLeft, onRight, inFront, behind}` with absolute-positioning render, `Std.Ui.{clip, scrollbars}` + axis variants, `Std.Ui.html` (escape hatch wrapping a Std.Html VNode via the new `Raw` Element variant), `Std.Ui.Input.{email, username, search, currentPassword, newPassword, radio, radioRow, slider}`. Compiler-side: `Html.aside` registered in the kernel registry. Full reference: `docs/skyui/overview.md`.

- **Apache 2.0 relicense + NOTICE.md attribution** — Sky relicensed from MIT to Apache 2.0 (existing v0.10.0-and-earlier releases keep their MIT terms). Brings patent grant, trademark clause (`Sky` name protected from fork-misuse), and the NOTICE-file mechanism. `NOTICE.md` documents prior-art for `Std.Ui` (mdgriffith/elm-ui), `Sky.Live` (Phoenix LiveView), and the ten elm/compiler-derived files in `src/Sky/Type/` + `src/Sky/AST/`. `CONTRIBUTING.md` sets the inbound = outbound expectation. Defensive endorsement-clause cleanup across docs and source comments removes promotional uses of upstream project names while preserving factual technical references.

- **LSP false-positive on TEA + `Live.app`** — HEURISTICALLY SUPPRESSED. The LSP's `runPipeline` calls `Constrain.constrainModule canMod` (no externals), so the kernel-shape record param of `Live.app` false-positives with `Type mismatch: { ... } vs { ... }` even though `sky check` (with externals loaded) types it cleanly. Interim fix: `isLikelyExternalsFalsePositive` in `src/Sky/Lsp/Server.hs` detects the truncated-record signature and drops it from diagnostics. Trade-off: a genuine record-vs-record mismatch involving large records would also be silently dropped — but those are rare in user code. **Proper fix**: extract `loadProjectExternals` from `Sky.Build.Compile` so the LSP can populate its own externals cache; the heuristic can then be removed. Tracked, not in this release.

#### v0.7.x – v0.10.x

- **Partial application of multi-arg functions through higher-order combinators** — FIXED in v0.9.10. `List.indexedMap myTwoArgFn xs` (and any other HOF that drove the call one arg at a time via `skyCallOne`) used to panic with `reflect: Call with too few input arguments` whenever `myTwoArgFn` was a top-level multi-arg binding emitted as a Go N-ary func. Same panic class hit auto-generated FFI setters partially applied in pipelines (`|> Result.andThen (Stripe.checkoutSessionParamsSetMode "payment")`) and let-bound multi-arg helpers (`Task.andThen (insertRow db)`). Fix: `skyCallOne` now detects `NumIn() > 1` and returns a `func(any) any` curried closure (`curryRemainingArgs`) that captures partial args until the arity is satisfied, then dispatches via `skyCallDirect`. Recursive — works for 3+ arg functions too. Resolves the previous Limitations #14 and #17. Regression tests: `runtime-go/rt/skycall_curry_test.go`.
- **Sky.Live `onImage` / `onFile` driver was dead** — FIXED in v0.9.10. `renderVNode` unconditionally prefixed event names with `sky-`, so an `eventPair{name: "sky-image"}` rendered as `sky-sky-image="…"` while the JS side-channel handler read `data-sky-ev-sky-image` — neither side rendezvoused. Plus the JS `__skySend(fileId, e.target.result)` and `__skySend(imageId, dataUrl)` passed bare strings as the wire `args`, but the server's `Args []json.RawMessage` rejected the unmarshal. Fix: `renderVNode` special-cases event names starting with `sky-` (the marker for side-channel meta-events) and emits as `data-sky-ev-<name>`; both `__skySend` call sites now wrap the data URL in `[…]`. Plain DOM events (click/input/etc.) keep `sky-<eventName>` since `__skyBindOne` queries by that selector. Also added the missing `Event_onFile` runtime kernel + canonicaliser whitelist entry (was documented but never implemented), and `fileMaxSize`/`fileMaxWidth`/`fileMaxHeight` attribute keys aligned to the `data-sky-ev-sky-file-max-*` shape the JS reads.
- **Sky.Live status banner + POST retry queue** — ADDED in v0.9.9. SSE drops or POST `/_sky/event` failures now show a bottom-pinned amber `Reconnecting…` banner after 500ms grace; failed POSTs land in `__skyEventQueue` (FIFO, capped at 50) and replay on SSE reopen or successful retry. Falls to red `Connection lost — refresh to retry` after 10 retry attempts (~2 min total backoff). Tunables via `SKY_LIVE_BANNER` / `SKY_LIVE_RETRY_*` / `SKY_LIVE_QUEUE_MAX` env vars. With a persistent session store (Redis/Postgres/SQLite/Firestore) the user's Model rides through deploy restarts unchanged — visible UX is "the page paused for a few seconds, then resumed."
- **Sky.Live Msg dispatch decodes wire arg into the constructor's typed Go param** — FIXED in v0.9.8. `<form onSubmit=...>` extracting form data as `map[string]any` couldn't be assigned to a `Dict String String -> Msg` constructor (typed-codegen lowered the param to `map[string]string`); reflect's `AssignableTo` rejected it and the submit silently dropped. Same gap for typed-record-arg Msgs (`DoSignIn AuthCreds`). Fix: `decodeMsgArg` uses `reflect.New(paramT)` + `json.Unmarshal(raw, ptr.Interface())` to decode wire bytes directly into the concrete Go type — Go's case-insensitive struct field matching does the lowercase-Sky-field → PascalCase-Go-field mapping for free.
- **Sky.Live session-store env vars + sky.toml keys** — DOC FIX in v0.9.10. CLAUDE.md / templates / docs claimed `SKY_LIVE_SESSION_STORE` / `SKY_LIVE_SESSION_URL` and a `[live.session]` table. The runtime actually reads `SKY_LIVE_STORE` / `SKY_LIVE_STORE_PATH` (with `DATABASE_URL` / `REDIS_URL` fallbacks) and the sky.toml keys live under `[live]` directly as `store` / `storePath`. Apps that set the wrong names silently fell through to the memory store. `SKY_LIVE_STATIC_DIR` was the inverse — docs had the right name; runtime read `SKY_STATIC_DIR`. Both runtime + emitted-init-block now read the documented `SKY_LIVE_STATIC_DIR` first; legacy `SKY_STATIC_DIR` kept as backward-compat fallback.
- **FFI-opaque types in Sky type aliases fall back to `any`** — FIXED in v0.9.7. Sky type aliases like `type alias SourceScanner = { scanner : Bufio.Scanner, label : String }` emitted a dangling `Bufio_Scanner` Go type identifier (no corresponding `type X = …` declaration), breaking `go build` with `undefined: Bufio_Scanner`. Tracking Sky-defined union/ADT names in CodegenEnv (`_cg_unionNames`) so `solvedTypeToGo` can distinguish "Sky-defined union (alias is emitted)" from "FFI-opaque (no Go alias exists)" — the latter falls back to `any`.
- **Effect Boundary Audit + Result/Task bridges** — LANDED in v0.9.6. `Std.Db.*` migrated from `Result Error a` to `Task Error a` (real I/O composes via Cmd.perform / Task.parallel goroutines). New stdlib bridges: `Task.fromResult`, `Task.andThenResult`, `Result.andThenTask`, `Task.mapError`, `Task.onError`. Two-tier doctrine documented (real I/O = Task, sync convenience effects = sync). Typed-codegen fix: `Db` (and any `rt.SkyTask` param) now coerces through `rt.TaskCoerceT[…]` instead of a raw `.(T)` assertion.
- **Nested `case...of`** — FIXED in v0.7.21. `caseDepth` counter in `LowerCtx` generates unique `__subject_N` variables per nesting level. Triple-nested case expressions compile and run correctly.
- **FFI callback wrapping** — FIXED in v0.7.21. `mapGoFuncType` parses arbitrary Go callback signatures (not just `func(ResponseWriter, *Request)`).
- **`sky check` Go callback function types** — FIXED in v0.7.21. Callback parsing in `TypeMapper.sky` handles `func(...)` types properly.
- **Non-exhaustive case expressions** — FIXED. Now a compile error (was a dead binding in Infer.sky). Shows missing patterns with source context.
- **Multi-module stdlib alias collision** — FIXED. `isStdlibCallee` checks `ctx.importAliases` instead of a hardcoded whitelist. `import Std.Db as Db` alongside `import Lib.Db as Db` works.
- **Lexer: `from` keyword blocked parameter names** — FIXED. Same class as the earlier `alias` bug. Removed `from` from `isKeyword` in Token.sky. Was the root cause of the cons-pattern-in-recursive-functions symptom.
- **`bin` field in sky.toml respected** — FIXED. `cmdBuild`, `cmdRun`, and the typed-build path now read `bin` from sky.toml and produce the configured binary path (defaults to `app`).
- **Cross-module zero-arg ADT constructors emitted as function calls** — FIXED. `lowerQualifiedImport` in Lower.sky now consults `ctx.importedConstructors` and emits `Piece_King` (value) for zero-arg constructors instead of `Piece_King()` (call). Multi-arg constructors retain the existing call form so `Piece.Box 42` still works.
- **Applicative combinators for Result and Task** — ADDED in v0.7.25. `Result.map2/3/4/5`, `Result.andMap`, `Result.combine`, `Result.traverse`, plus matching `Task.map2/3/4/5`, `Task.andMap`. Solves the heterogeneous-Result-combine and homogeneous-list-of-Results cases without needing nested case or `andThen` lambdas. Also upgraded `sky_call2`/`sky_call3` and added `sky_call4`/`sky_call5` to handle both curried and uncurried multi-arg Sky functions, fixing a latent issue where local-module functions passed to higher-order helpers crashed at runtime.
- **Auto record constructors from type aliases** — ADDED in v0.7.26. Every `type alias Foo = { ... }` declaration auto-generates a constructor function `Foo : field1Type -> field2Type -> ... -> Foo` (Elm convention). Eliminates `makeFoo` boilerplate and lets type aliases drop directly into `Result.map3 Foo (parseA ...) (parseB ...) (parseC ...)`. Implemented as a post-parse `elaborateModule` step in `Parser.sky` that synthesizes `TypeAnnotDecl` + `FunDecl` for each record type alias, skipping when the user has defined a value with the same name. Also extended the parser dispatcher to accept `TkUpperIdentifier` as a value-level declaration name so users can override the auto-generated constructor with their own implementation. Field declaration order in the type alias becomes positional API for the constructor.
- **Type system overhaul (annotations now load-bearing)** — FIXED in v0.7.28. Three coordinated changes restore "if it compiles, it works" for annotated functions:
  1. **Pretty-printer renames quantified vars to `a, b, c`** in `Types.sky`. `formatScheme` and `formatTypePairForError` rename TVars consistently within a single error or hover, so users see `Cannot unify a -> Int with Int` instead of `Cannot unify t108 -> Int with Int`. All unification error messages now use `cannotUnifyMsg` which calls `formatTypePairForError`. `TypedDecl.prettyType` uses `formatScheme` so LSP hovers show real types.
  2. **`inferFunctionSelfUnify` uses the annotation as the scheme** when present and the body validates against it. The new `applyAnnotationConstraint` helper unifies inferred body type with resolved annotation type, then uses the annotation type (substituted) as the function's stored scheme. Without this, `f : String -> Int -> String; f s n = s` was registered as `forall a b. a -> b -> a` (the inferred body type), and call sites could pass any types — silently ignoring the annotation.
  3. **`preRegisterFunctions` uses the annotation when present** so use sites in earlier declarations of the same module see the user's declared type instead of a polymorphic placeholder. Forward references and mutual recursion now respect annotations.
  - **Cross-module type alias resolution** in `registerTypeAliases` and `Resolver.typeExprToScheme`: both now accept the imported alias dict and combine it with the local paramMap, so an alias body like `myCounter : Counter` (where Counter is from another module) gets the resolved record substituted inline at registration time.
  - **`Adt.resolveAnnotation`** added: walks an annotation TypeExpr collecting unique TypeVar names, allocates a fresh ID per name, builds a paramMap, and resolves. This makes polymorphic annotations like `f : a -> b -> a` get distinct TVar IDs (previously all `TypeVar` references got hardcoded ID 0).
  - **Verified**: annotated `Decode.succeed makeStr |> Pipeline.required "a" Decode.string |> Pipeline.required "b" Decode.string` (where `makeStr : String -> Int -> String`) is now caught by `sky check` with `Pipeline operator: Type mismatch: String vs Int` instead of silently passing.

- **Zero-arity declaration memoisation (Ref bug fix)** — FIXED in v0.7.30. The lowerer treated top-level zero-parameter declarations like `counter = Ref.new 0` as functions, re-evaluating the body on every reference. This broke `Ref.new`, `Dict.empty` singletons, and any other values that must be created once. Fix: zero-arity declarations now emit memoised functions (`var _memo_X; var _memoOk_X bool; func X() { if !_memoOk_X { _memo_X = <body>; _memoOk_X = true }; return _memo_X }`). The calling convention is unchanged — both same-module and cross-module references call the function, but the body evaluates only once. The runtime alias registry `Ref` in `Unify.sky` now works as a true singleton.
- **`sky init` CLAUDE.md template embedded in binary** — FIXED in v0.7.30. The template is now in `bootstrap/runtime/templates/CLAUDE.md` and embedded via `//go:embed runtime/*`. Installed binaries no longer need a `templates/` directory on disk; `readEmbeddedTemplate` reads from the binary's embedded FS. Falls back to disk path lookup for repo dev builds.
- **Task.perform returns Result uniformly** — FIXED in v0.7.29. The helper used to unwrap `Ok` values while keeping `Err` as `SkyResult`. Now returns `sky_runTask` result directly so `case Task.perform t of Ok x -> ... ; Err e -> ...` works for both branches.

- **Async Cmd.perform for Sky.Live** — ADDED in v0.8.0. `update` returns `(Model, Cmd Msg)` where `Cmd.perform task toMsg` spawns a goroutine. On completion, the result is dispatched as a Msg through the full update/view/diff/SSE cycle with session locking. `Cmd.batch` runs multiple commands concurrently. Recursive: cmd-triggered updates can spawn more cmds.
- **Time.sleep + Random.int lowerer mappings** — ADDED in v0.8.0. `Time.sleep : Int -> Task Error ()` and `Random.int/float/choice/shuffle` now have Go implementations and lowerer mappings. Type signatures in Resolver for compile-time checking.
- **Constructor partial application** — FIXED in v0.8.0. `checkPartialIdent` now checks `importedConstructors` for ADT constructor arities, not just `localFunctionArity`. Fixes `JobDone jid` (partial apply of 2-arg constructor) generating invalid Go.
- **MultilineStringExpr AST node** — ADDED in v0.8.0. The parser creates `MultilineStringExpr` for `"""..."""` strings instead of desugaring at parse time. The formatter preserves triple-quoted strings. The lowerer desugars at codegen time with `{{expr}}` interpolation handling.
- **Formatter style improvements** — FIXED in v0.8.0. Tuples break vertically with leading commas. Function args indent 4 spaces (not aligned to callee column). Parenthesised expressions stay compact on one line.
- **Skyshop env var race condition** — FIXED in v0.8.0. Zero-arity functions reading `Os.getenv` were memoised and evaluated at Go init time (before `.env` loaded). Fix: add `_` parameter to prevent memoisation.

- **Nested typed-map narrowing at the FFI boundary** — FIXED in v0.9-dev (feat/typed-codegen). `rt.Coerce[T]` / `coerceInner` / `AsListT` / `AsDict` now delegate to recursive `narrowReflectValue` / `coerceMapValue` / `coerceSliceValue` helpers so `[]any` → `[]map[string]string` (each element being a `map[string]any` from a SQL row) converts correctly. Before this, 08-notes-app login and 13-skyshop product listing both showed empty results even though the DB returned rows.
- **Curried lambda adapter recursion** — FIXED in v0.9-dev. `adaptFuncValue` (the MakeFunc worker behind `makeFuncAdapter`) wraps each inner `func(any) any` returned by a Sky curried lambda. Without this, `rt.Coerce[func(map[string]string) func(string) rt.SkyResponse]` lost the inner function and call sites panicked with `reflect.Value.Call: call of nil function`.
- **`rt.AsList` accepts typed slices** — FIXED in v0.9-dev. It used to only handle `[]any`; typed slices (`[]map[string]string` from annotated `Lib_Notes_getNotes`) went to `nil` and downstream `List.isEmpty` wrongly reported empty, rendering "No notes yet" where data existed.
- **`Html_render` with server-rendered form events** — FIXED in v0.9-dev. `renderVNode` was called with `nil` handlers; a form with `onSubmit="return confirm(...)"` panicked on `handlers[id] = msg`. Now `Html_render` always provides an empty `map[string]any{}`.
- **`Db_getField` on typed session rows** — FIXED in v0.9-dev. The runtime helper only handled `map[string]any`; annotated `authenticateUser` returned `map[string]string` and every getField silently returned `""`, so signin always said "invalid email or password". Now accepts both.
- **Literal patterns constrain scrutinee** — FIXED in v0.9-dev. `PStr`/`PInt`/`PBool`/`PChr`/`PUnit` in `instantiatePattern` emit a `CEqual` constraint on the scrutinee, so `case foo of "idle" -> _ ; "ready" -> _` now forces `foo : String` and later `foo == "other"` is type-checked. Before this, the string-literal patterns left the scrutinee polymorphic and the wrongly-typed `==` surfaced as a runtime panic once typed codegen stopped boxing everything.
- **Incremental Go dep re-seeding** — FIXED in v0.9-dev. `copyRuntime` overwrites `sky-out/go.mod` on every incremental build, wiping the project's transitive Go deps and making `go build` fail with "missing module". Fix: re-run `seedGoDependencies` after `copyRuntime` on the incremental path too.
- **Entry-module TypedDef generic instantiation** — FIXED in v0.9-dev. Entry-module `entryInferredSigs` now includes TypedDefs with their annotation type (not just the solved type), so call sites emit the right generic instantiation and `init_` / `update_` are never emitted as "cannot use generic function without instantiation".
- **Cross-module HM with polymorphic externals** — FIXED in v0.9-dev. A second dep-solve pass after external annotation resolution lets imports that depend on stdlib generics (`Result.map2`, `Dict.get`) type-check in a consistent order.
- **Redis session store actually implemented** — FIXED in v0.9-dev. `chooseStore` advertised `"redis"` in docs/CLAUDE.md/templates but `runtime-go/rt/live_store.go` only shipped memory/sqlite/postgres; `store = "redis"` silently fell back to memory. Added `redisStore` using `github.com/redis/go-redis/v9`, same gob-blob wire format as sqlite/postgres, native Redis TTL (no cleanup goroutine) refreshed on every `Get`. Accepts `redis://…` URLs and bare `host:port`. Tested with miniredis in `runtime-go/rt/live_store_redis_test.go` (7 cases: round-trip, Delete, TTL refresh, natural expiry, closure rejection, URL-form dial, `chooseStore` factory).

**Coding constraints**: none active. (The "no nested case" rule is no longer required as of v0.7.21.)

### Techniques from TS Compiler (to port)

1. **Symbol-level tree-shaking** — collect wrapper refs during lowering, filter to referenced only (Stripe 40K→~50)
2. **Selective import emission** — only emit imports for referenced packages (currently emits all 18)
3. **go.mod/go.sum preservation** — only delete `.go` files, reuse Go compiled objects
4. **Single-pass emission** — track imports during lowering, no second pass

### Build Times

| Project | Modules | Cold | Warm |
|---|---|---|---|
| hello-world | 1 | <1s | <1s |
| skyvote | 32+2 FFI | 1.7s | 1.7s |
| **skyshop** | 43+14 FFI | **1:30** | **0:59** |
| compiler | 28 | 5.6s | 5.6s |

### Typed Codegen (v0.9 / `feat/typed-codegen`)

Typed Go emission is LIVE. The goal of v1.0 — "zero `any` in generated
Go signatures" — is met on the branch. Every example in
`examples/*` emits typed `func Foo(a int, b string) rt.SkyResult[Error, T]`
rather than `func f(a any) any`. Entry-level invariant: **0 real-`any`
sigs** across the 20-project sweep.

What landed to make this work — keep in mind when editing the compiler
or runtime:

1. **HM infer is authoritative.** `Sky/Type/Constrain/Expression.hs`
   resolves annotations against cross-module type aliases before
   registering the scheme, so `f : Dict String String -> Result Error T`
   in module A reaches module B's call site with the record inlined.
   Annotations survive into codegen via `typeStrWithAliasesReg` /
   `splitInferredSigWithReg` in `Sky/Build/Compile.hs`.
2. **TVar defaulting.** Unresolved type variables default by position:
   error-slot → `Sky.Core.Error.Error`; ok-slot (Result) and
   return-only → `rt.SkyValue` (a named `any` alias used to mean
   "runtime-tagged value"). Anything still polymorphic at emission
   time is monomorphised at the call site via `rt.Coerce[T]`.
3. **`lookupKernelType` feeds runtime kernel sigs.** Db.open,
   Db.query, Db.exec, Db.execRaw, Db.connect, Context.background/todo,
   Fmt.sprint*, Css.rgb/rgba/hsl/hsla/shadow are typed at the
   inference layer so callers know the real Go signature.
4. **Runtime coercion helpers** (runtime-go/rt/rt.go) bridge the
   typed surface to the (still any-heavy) runtime: `rt.Coerce[T]`,
   `rt.AsListT[T]`, `rt.AsMapT[V]`, `rt.AsDict`, plus the recursive
   trio `narrowReflectValue` / `coerceMapValue` / `coerceSliceValue`.
   They handle the chain SQL row → `map[string]any` → typed
   `map[string]string` including the list-of-maps case that 08/13
   exercise. String targets stringify heterogeneous values so mixed
   SQL columns (int `verified`, int64 `id`, []byte `hash`) collapse
   to a uniform map.
5. **Curried Sky lambdas wrap recursively.** `adaptFuncValue` in
   `runtime-go/rt/rt.go` is the MakeFunc worker behind
   `makeFuncAdapter`. It recurses: when a Sky-returned inner func
   doesn't match the target's next arrow, it wraps again. Without
   this, `Coerce[func(map[string]string) func(string) rt.SkyResponse]`
   over a Sky `func(any) any { return func(any) any {...} }` zero'd
   the inner func and every call-site like requireAuth → route
   handler blew up with `reflect.Value.Call: call of nil function`.
6. **Literal patterns constrain scrutinee.** `PStr`, `PInt`, `PBool`,
   `PChr`, `PUnit` in `instantiatePattern` now emit
   `CEqual reg CString stringType` etc. Before this, a case branch
   `case foo of "ready" -> _` left `foo` polymorphic and downstream
   `foo == "idle"` compared against `any` at runtime, surfacing as a
   panic once typed codegen stopped boxing.
7. **`Html_render` + `Unify.Server_renderResponse` never pass nil
   handlers.** VNode trees with `onSubmit` events (plain HTML forms
   on server-rendered pages) would panic with
   `assignment to entry in nil map` otherwise.

#### Things we tried that didn't work (don't re-attempt without reading
these first)

- **Narrowing `Live.app.init`'s request-record type.** Making
  `init : Dict String String -> (Model, Cmd Msg)` looks nice, but HM
  then narrows the record type to whatever the first app uses
  (Firestore nested maps in 13-skyshop), breaking every other app.
  Kept `init`'s request argument as a polymorphic TVar — callers
  plug in whatever shape their framework supplies.
- **Attempting full Go-struct records.** `{ name : String, age : Int }`
  → named Go struct (`State_R`, `Model_R`) works for annotated record
  aliases, but anonymous records in function signatures still can't
  be named because HM can't backfill a struct name. We emit the
  typed struct only when the user writes a `type alias` for the
  shape; inline records in signatures stay any-boxed.
- **Monomorphisation over Go generics.** Rolled back after Stripe's
  SDK blew the emit size up ~5× because every call site reinstantiated
  opaque wrappers. Using Go generics (e.g. `SkyResult[E, A]`) is
  cheaper and GHC-free.
- **Zero-arity env lookups at init time.** Memoised zero-arity
  declarations reading `Os.getenv` evaluate at Go `init()` — before
  `.env` is loaded. Workaround is an explicit `_` param; still the
  guidance in the Known Limitations section.

#### Typed-codegen TODO (carry into v1.0)

- **Eliminate the `any` return in runtime kernels.** Helpers like
  `rt.Dict_get`, `rt.List_map`, `rt.Html_render` still return `any`
  internally; the typed surface calls `rt.Coerce[T]` on the result.
  Porting them to generics (`Dict_getT[V]`, `List_mapT[A, B]`) drops
  the reflect dance.
- **Record struct for `update` / `view` signatures.** TEA apps still
  return `(Model, Cmd Msg)` via `any` tuple; emitting a named
  `State_R` tuple shape would let Go catch Msg/Model misalignment.
- **Smarter cache invalidation.** `.skycache/lowered/` is hashed per
  module source, but the hash doesn't cover imported module
  annotations, so a downstream annotation edit doesn't always
  invalidate dependent modules.
- **Selective import emission.** Generated Go still imports all 18
  Sky runtime subpackages even when the example only uses two.
- **Sky-test harness in typed codegen.** `sky test` currently uses
  the any-heavy path; port to typed once stdlib matches.
- **Lambda lowering preserves types** (scoped 2026-04-27). Sky
  lambdas always lower to `func(any) any` regardless of their
  HM-inferred input/output types. This forces `rt.Coerce[func(X)
  Y]` adapters at every call site that passes a typed function
  (Msg constructor or sky-lambda) to a typed-callback helper
  param. Limitation #18's fix made the adapter path correct, but
  it's reflect.MakeFunc — not "fully typed" in the strict sense.
  **Current cost is small**: across the 19-example sweep there
  are only **11 such adapters** total (5 in 19-skyforum, 5 in
  08-notes-app, 1 in 10-live-component) — measured 2026-04-27.
  At that scale the runtime cost (~100 ns × 11 call sites) is
  negligible vs. the cost of the architectural rewrite. Proper
  fix needs: (1) the lowerer to thread HM-inferred types into
  lambda emission, (2) `curryLambdaPat` to optionally take a
  typed Go signature, (3) lambda body to coerce `any`-typed
  param uses to the typed shape, (4) every callback shape across
  the codebase to handle the typed/untyped split. Multi-week
  scope — deferred until either the adapter count climbs into
  the hundreds OR a dedicated session is set aside for this
  specific architectural change. Tracked separately because
  doing it half-way leaves the old behaviour intact for
  unhandled cases — which IS a workaround, against the
  no-workarounds principle.
