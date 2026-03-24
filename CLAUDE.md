# CLAUDE.md

## Project Overview

Sky is an experimental programming language inspired by **Elm**, compiling to **Go**. The repo contains a compiler, CLI, formatter, LSP, and Helix editor integration -- the bootstrap compiler is written in **TypeScript**, and a **self-hosted compiler is written in Sky itself** (`sky-compiler/`).

## Architecture & Pipeline

```
source -> lexer -> layout filtering -> parser -> AST -> module graph -> type checker -> Go emitter
                                                                         ↑ binding index (.idx)
                                                                         ↑ lazy symbol resolution
```

```
src/
  compiler.ts          -- Core compilation pipeline orchestration
  ast/ast.ts           -- AST node definitions
  lexer/lexer.ts       -- Indentation-aware lexer
  parser/              -- Pratt-style parser with layout filtering
    parser.ts, filter-layout.ts, operator-table.ts, sections.ts
  modules/resolver.ts  -- Module resolution & dependency graph
  types/               -- HM type system (infer, unify, checker, adt, exhaustiveness, patterns)
  core-ir/core-ir.ts   -- Core Intermediate Representation
  go-ir/go-ir.ts       -- Go Intermediate Representation
  lower/               -- AST -> CoreIR -> GoIR lowering + dead-binding elimination
  emit/go-emitter.ts   -- Go code generation
  interop/go/          -- Go FFI: collect-foreign, generate-bindings, generate-wrappers, inspect-package
  pkg/                 -- Package manager (manifest, installer, lockfile, registry, resolver)
  live/                -- Sky.Live compiler support
  runtime/             -- Sky.Live Go runtime files
  lsp/                 -- Language Server (completion, definition, hover, signature, formatter)
  stdlib/              -- Core/Std library .sky files (Prelude, Maybe, String, Cmd, Task, Sub, Log,
                          Html, Css, Live, Char, Tuple, Bitwise, Set, Array, File, Path, Process,
                          Ref, Args, Json.Decode, Json.Encode, Platform, Debug, etc.)
  cli/                 -- CLI commands (init, add, remove, install, update, build, run, dev, fmt, check, clean, upgrade)
  bin/                 -- Entry points: sky.ts, sky-lsp.ts, build-binary.js
  utils/               -- Helpers (assets.ts, path.ts)
```

### Self-Hosted Compiler (`sky-compiler/`)

The Sky compiler rewritten in Sky itself. Compiles all 15 examples successfully.

```
sky-compiler/
  sky.toml                       -- Project manifest
  src/
    Main.sky                     -- Entry point (CLI arg handling)
    Compiler/
      Token.sky                  -- Token types and source positions
      Lexer.sky                  -- Indentation-aware tokenizer
      ParserCore.sky             -- Shared parser state, helpers, layout filtering
      Parser.sky                 -- Module/import/declaration/type parsing
      ParserExpr.sky             -- Expression parsing (Pratt-style precedence)
      ParserPattern.sky          -- Pattern parsing
      Ast.sky                    -- AST node definitions (Expression, Pattern, Type, Declaration)
      GoIr.sky                   -- Go Intermediate Representation types
      Emit.sky                   -- Go source code emitter
      Types.sky                  -- HM type system core (Type, Scheme, Substitution)
      Env.sky                    -- Type environment with lexical scoping
      Unify.sky                  -- Robinson unification with occurs check
      Adt.sky                    -- ADT registration and constructor scheme generation
      PatternCheck.sky           -- Pattern type checking and binding extraction
      Infer.sky                  -- Algorithm W type inference
      Exhaustive.sky             -- Exhaustiveness checking for case expressions
      Checker.sky                -- Module-level type checking orchestration
      Lower.sky                  -- AST → GoIR lowering (constructors, case, operators)
      Resolver.sky               -- Module resolution and stdlib type environment
      Pipeline.sky               -- Full compilation pipeline orchestration
```

**Building the self-hosted compiler:**
```bash
sky build sky-compiler/src/Main.sky   # Compile the compiler
./dist/app input.sky                  # Use it to compile Sky files
```

**Key design decisions:**
- Generates standalone Go (inline runtime helpers, no `sky_wrappers` dependency)
- Avoids tuple patterns in constructor case branches (TS compiler codegen limitation)
- Uses `fst`/`snd` instead of tuple destructuring in `Just`/`Ok` patterns
- Parser split across 4 modules to avoid Go 1.26 inlining OOM on deeply nested IIFEs
- `consumeOperator` returns exact operator length (no trailing char consumption)
- Column-based scope boundaries in expression parser (stops at column ≤ previous token)

## Build & Test

```bash
npm run build          # TypeScript -> dist/
npm run bundle         # esbuild + pkg -> native binaries in bin/
npm test               # Run test suite (vitest)
npm run test:watch     # Watch mode tests
node dist/bin/sky.js fmt examples/simple/src/Main.sky
node dist/bin/sky.js build examples/01-hello-world/src/Main.sky
node dist/bin/sky.js run examples/01-hello-world/src/Main.sky
sky fmt src/Main.sky           # Format .sky/.skyi files (always run after changes)
sky check src/Main.sky         # Type-check without compiling (reports all diagnostics)
sky clean                      # Remove dist/, .skycache/, .skydeps/
sky upgrade                    # Self-update to latest GitHub release
sky --version                  # Show embedded version (e.g. "sky v0.2.3")
```

## Critical Rules

1. **TypeScript only** -- Never commit `.js` files in `src/` (except `src/bin/build-binary.js`).
2. **Indentation parser** -- The parser uses the column of the first token as the minimum indentation reference. Do not tighten rules that break slightly unaligned input.
3. **Formatter (Elm-style)** -- 4-space indent, leading commas, `let`/`in` always multiline, 80-char line width. **Always run `sky fmt` on `.sky` and `.skyi` files after any changes** (`sky fmt <file>.sky` or `sky fmt <file>.skyi`).
4. **Universal unifiers** -- `JsValue`, `Foreign`, and variants are universal unifiers for interop. Do not remove. Named Go FFI types (e.g., `Db`, `Rows`, `Response`) are **opaque and strict** -- they must match exactly by name during unification and cannot be used interchangeably. This guarantees type safety at the Go boundary.
5. **Prelude** -- `Sky.Core.Prelude` is implicitly imported everywhere. Provides `Result`, `Maybe`, `identity`, `not`, `always`, `fst`, `snd`, `clamp`, `modBy`, `errorToString`, `js`.
6. **Go FFI** -- Wrappers accept `any` params with safe assertion helpers (`sky_asInt`, `sky_asString`, `sky_asFunc`, etc.) that return zero values instead of panicking. Always overwrite `00_sky_helpers.go`. Emitted packages prefixed `sky_` (except `main`). Auto-generated bindings: struct methods become `{Type}{Method}` (e.g., `db.Query` → `dbQuery`), fields become `{Type}{Field}`, constants/vars become zero-arg functions (getters), exported vars also get setters (`setVarName`). Generic Go functions (with type parameters) are automatically excluded from bindings. Binding index (`bindings.idx`) enables lazy symbol resolution — only referenced symbols are loaded, making massive packages like Stripe SDK (40K+ symbols) compile in seconds.
6a. **Type constraints** -- `comparable`, `number`, and `appendable` are enforced type constraints. `comparable` allows `Int`, `Float`, `String`, `Bool`, `Char`, and tuples/lists thereof. `number` allows `Int` and `Float`. `appendable` allows `String` and `List`. These are checked during unification.
7. **Pointer safety** -- Go `*primitive` types (`*string`, `*int`, etc.) map to `Maybe T` in Sky. Opaque struct pointers (`*sql.DB`) stay as their type name (`Db`). Go `(T, bool)` comma-ok returns map to `Maybe T`. Go `(T1, T2, ..., error)` multi-return maps to `Result Error (TupleN T1 T2 ...)`.
8. **AST lowering** -- Uppercase identifiers = Constructors unless declared as `foreign import` (then lower as Variable). Don't inject `GoTypeAssertExpr` on FFI return values. ADT constructors generate Go constructor functions for cross-module use. `Result` and `Maybe` use named runtime types (`SkyResult`/`SkyMaybe` in `sky_wrappers`) — never emit anonymous struct literals for these. Well-known constructors (`Ok`/`Err`/`Just`/`Nothing`) always use `SkyOk`/`SkyErr`/`SkyJust`/`SkyNothing` wrapper functions.
9. **Pipeline operators** -- `|>` and `<|` (Elm-style). `::` (cons) works in both patterns and expressions: `1 :: 2 :: []` builds `[1, 2]`. Elm-compatible operators: `/=` (not-equal, alias for `!=`), `//` (integer division). Both `!=` and `/=` are supported for not-equal; both `/` and `//` work for division (`//` always returns `Int`).
10. **Sub type** -- `Std.Sub` is a normal ADT module (not an FFI wrapper). `Sub` has constructors `SubNone`, `SubTimer Int msg`, `SubBatch (List (Sub msg))`. The Go runtime walks these values to set up SSE subscriptions.
11. **Embedded assets** -- `src/utils/assets.ts` contains embedded stdlib and `SKY_VERSION`. Must be updated whenever stdlib `.sky` files change. `SKY_VERSION` is set by `build-binary.js`: CI passes `SKY_VERSION` env var from the git tag; local builds use `package.json version + "-dev"`.
12. **VNode emission** -- `Std.Html` functions return VNode records (`{ tag, attrs, children, text }`), not HTML strings. Attributes are `(key, value)` tuples. The Go runtime converts these via `MapToVNode` -- no HTML parsing needed. Non-Live apps use `render`/`toString` to convert VNode records to HTML strings. HTML5 semantic elements that clash with common identifiers use suffixed names: `headerNode`, `footerNode`, `mainNode`, `codeNode`, `linkNode`, `styleNode`, `titleNode`.
13. **Go reserved words** -- The Go lowerer (`sanitizeGoIdent`) appends `_` to Sky identifiers that clash with Go keywords (`go`, `type`, `func`, `var`, `return`, etc.). Never use Go keywords as Sky variable names in stdlib code.
14. **Distribution** -- Release binaries via `git tag v0.x.0 && git push --tags`. CI builds for macOS (arm64/x64), Linux (arm64/x64), Windows (x64) with `SKY_VERSION` embedded from the git tag. Users install via `curl -fsSL .../install.sh | sh`, Docker, or `sky upgrade`. The CLI checks for updates (once per 24h) after `sky add/install/build` and shows a notice if a newer release is available.
15. **Unicode safety** -- Never use `JSON.stringify` to quote Sky string/char literals in the formatter or emitter. `JSON.stringify` escapes non-ASCII characters to `\uXXXX`, which the lexer then misparses as literal `u{XXXX}` text. Use the formatter's `quoteString()`/`quoteChar()` helpers instead, which preserve unicode as-is. The esbuild config must include `charset: "utf8"` and `build-binary.js` must use backtick template literals (not `JSON.stringify`) when embedding assets.
16. **Session concurrency** -- Sky.Live uses two layers of concurrency control: (1) per-session in-process mutex (`SessionLocker`) prevents races between event handling and SSE ticks within a single server, (2) optimistic concurrency via `Session.Version` field prevents races across multiple server instances sharing a database. All `SessionStore.Set` calls use `WHERE version = N` semantics and callers retry up to 3 times on conflict.
18. **Loading overlay** -- Sky.Live renders a `#sky-loader` overlay during server round-trips. Shown automatically for all events except `onInput` (typing). Hidden on response, SSE push, or poll. Users can restyle via `#sky-loader` and `.sky-spinner` CSS selectors. The overlay has an 80ms delay to avoid flicker on fast responses.
19. **Client-side eval** -- `data-sky-eval` attribute executes arbitrary JavaScript on the client after DOM patching (e.g., `skySignOut()` for Firebase sign-out). The element is removed after execution. Use sparingly — only for client-side side effects that can't be expressed as Sky messages.
20. **Ephemeral redirect** -- `checkoutUrl` in the model is cleared on session restoration to prevent infinite redirect loops. The `data-sky-redirect` mechanism is single-use per render cycle.
21. **Security** -- Sky.Live enforces session cookie validation (SID in request must match HttpOnly cookie), request body size limits (10MB), per-IP rate limiting (30 req/s), and security headers (X-Content-Type-Options, X-Frame-Options, Referrer-Policy) on all API endpoints. The optional `guard` function in the `app` config provides declarative message authorization: `guard : Msg -> Model -> Result String ()`. Rejected messages are silently dropped (empty patch response). Always define a `guard` for apps with admin or auth-gated operations — View-level checks alone are not sufficient since `__sky_send` is a public API.

## Compiler Performance & FFI Pipeline

The compiler uses several optimizations for fast builds even with massive Go dependencies:

1. **Binding index** (`bindings.idx`) -- JSON index generated alongside `.skyi` during `sky install`. The resolver loads the index instead of parsing 200K+ line `.skyi` files. Only referenced symbols are materialized into the type environment (lazy resolution).
2. **O(N) type environment** -- `TypeEnvironment.addMut()` for mutable batch insertion of declarations. Eliminates O(N²) clone-on-extend that caused hangs with large Go packages.
3. **bindingsOnly mode** -- `.skyi` and `.skydeps` modules skip full HM type inference in Pass 2. Type annotations are parsed directly without running the inference algorithm.
4. **Wrapper tree-shaking** -- Symbols collected during GoIR lowering (not post-compile file scanning). `InspectResult` pre-filtered to only needed symbols before wrapper generation. For Stripe SDK: 40K+ symbols → ~25 used → 380 lines of wrapper code.
5. **Inspection cache** -- `inspect.json` cached to disk per Go package. Invalidated by `go.sum` changes. Subsequent builds skip the expensive Go inspector process.
6. **FFI constant auto-call** -- The lowerer detects non-function FFI types from `moduleExports` and automatically wraps constant/variable references in `GoCallExpr` (zero-arg Go wrapper functions must be called).

## Package Management

### sky.toml

```toml
name = "my-project"
version = "0.1.0"
entry = "src/Main.sky"             # optional: app entry point
bin = "dist/app"                   # optional: output binary path

[source]
root = "src"

[lib]                              # optional: makes this a library
exposing = ["MyLib.Foo", "MyLib.Bar"]

[dependencies]                     # Sky packages
"github.com/someone/sky-utils" = "latest"

[go.dependencies]                  # Go packages
"github.com/google/uuid" = "latest"

[live]                             # Sky.Live config
port = 4000
input = "debounce"                 # "debounce" | "blur"
poll_interval = 0                  # ms (0 = SSE only)

[live.session]
store = "memory"                   # memory | sqlite | redis | postgresql | firestore
```

Sky.Live config is embedded at compile time but overridable at runtime via env vars or `.env` file. Env var names mirror sky.toml: `SKY_LIVE_PORT`, `SKY_LIVE_INPUT`, `SKY_LIVE_POLL_INTERVAL`, `SKY_LIVE_SESSION_STORE`, `SKY_LIVE_SESSION_PATH`, `SKY_LIVE_SESSION_URL`, `SKY_LIVE_STATIC_DIR`, `SKY_LIVE_TTL`. Priority: compiled defaults < sky.toml < env vars < .env file. Non-Live apps can use `Process.loadEnv ""` to load `.env` files and `Process.getEnv` to read env vars.

### Package Types

- **App**: has `entry`, no `[lib]` -- runnable application
- **Library**: has `[lib]`, no `entry` -- exposes modules for import
- **Both**: has `entry` and `[lib]` -- app that also exposes modules
- No `[lib]` = all modules are internal/private

### Auto-detection (`sky add`)

- `sky add github.com/...` checks remote for `sky.toml` vs `go.mod`
- Sky packages: cloned to `.skydeps/`, added to `[dependencies]`
- Go packages: `go get` into `.skycache/gomod/`, added to `[go.dependencies]`
- Transitive deps (Sky and Go) of Sky packages are installed recursively

### Module Resolution for Dependencies

- `.skydeps/` packages: resolver reads each dep's `sky.toml` for `source.root`
- Only modules listed in `[lib].exposing` are importable
- No `[lib]` section = nothing is publicly importable
- Three import syntaxes are supported (all resolve to the same file):
  - **Stripped**: `import Tailwind as Tw` (cleanest, recommended)
  - **Prefixed**: `import SkyTailwind.Tailwind as Tw` (PascalCase package name + module)
  - **Full path**: `import Github.Com.Anzellai.SkyTailwind.Tailwind as Tw` (mirrors dependency URL)
- Resolution precedence: local `src/` modules > `.skydeps/` packages > stdlib. Local modules shadow dependency modules; use full/prefixed path to disambiguate.

## Examples

Located in `examples/` with numbered directories:
- `01-hello-world` -- Basic hello world
- `02-go-stdlib` -- Using Go standard library (crypto, encoding, net/http, time)
- `03-tea-external` -- TEA architecture with external packages
- `04-local-pkg` -- Multi-module project with local packages
- `05-mux-server` -- HTTP server with gorilla/mux + godotenv
- `06-json` -- JSON encoding and decoding (Elm-compatible)
- `07-todo-cli` -- Todo app with SQLite and CLI args
- `08-notes-app` -- Full CRUD web app with database and auth
- `09-live-counter` -- Sky.Live counter with routing and SSE subscriptions (Time.every)
- `10-live-component` -- Sky.Live component protocol with auto-wiring
- `11-fyne-stopwatch` -- Desktop GUI with Fyne toolkit
- `12-skyvote` -- Full Sky.Live app with SQLite, auth, voting, SSE auto-refresh
- `13-skyshop` -- Full e-commerce Sky.Live app: products, cart, Stripe Go SDK checkout, admin panel, i18n, image uploads, order management, Firebase Auth

## Language Syntax (Elm-like)

```elm
module Main exposing (main)

import Std.Log exposing (println)
import Sky.Core.String as String

main =
    let
        message = "Hello from Sky!"
        upper = String.toUpper message
    in
    println upper
```
