# CLAUDE.md

## Project Overview

Sky is an experimental programming language inspired by **Elm**, compiling to **Go**. The repo contains a compiler, CLI, formatter, LSP, and Helix editor integration -- all written in **TypeScript**.

## Architecture & Pipeline

```
source -> lexer -> layout filtering -> parser -> AST -> module graph -> type checker -> Go emitter
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
  stdlib/              -- Core/Std library .sky files (Prelude, Maybe, String, Cmd, Task, Sub, Log, Html, Css, Live, etc.)
  cli/                 -- CLI commands (init, add, remove, install, update, build, run, dev, fmt)
  bin/                 -- Entry points: sky.ts, sky-lsp.ts, build-binary.js
  utils/               -- Helpers (assets.ts, path.ts)
```

## Build & Test

```bash
npm run build          # TypeScript -> dist/
npm run bundle         # esbuild + pkg -> native binaries in bin/
node dist/bin/sky.js fmt examples/simple/src/Main.sky
node dist/bin/sky.js build examples/01-hello-world/src/Main.sky
node dist/bin/sky.js run examples/01-hello-world/src/Main.sky
```

## Critical Rules

1. **TypeScript only** -- Never commit `.js` files in `src/` (except `src/bin/build-binary.js`).
2. **Indentation parser** -- The parser uses the column of the first token as the minimum indentation reference. Do not tighten rules that break slightly unaligned input.
3. **Formatter (Elm-style)** -- 4-space indent, leading commas, `let`/`in` always multiline, 80-char line width.
4. **Universal unifiers** -- `JsValue`, `Foreign`, and variants are universal unifiers for interop. Do not remove.
5. **Prelude** -- `Sky.Core.Prelude` is implicitly imported everywhere. Provides `Result`, `Maybe`, `identity`, `errorToString`.
6. **Go FFI** -- Wrappers accept `any` params with internal type assertions. Always overwrite `00_sky_helpers.go`. Emitted packages prefixed `sky_` (except `main`).
7. **Pointer safety** -- Go `*primitive` types (`*string`, `*int`, etc.) map to `Maybe T` in Sky. Opaque struct pointers (`*sql.DB`) stay as their type name (`Db`).
8. **AST lowering** -- Uppercase identifiers = Constructors unless declared as `foreign import` (then lower as Variable). Don't inject `GoTypeAssertExpr` on FFI return values. ADT constructors generate Go constructor functions for cross-module use.
9. **Pipeline operators** -- `|>` and `<|` (Elm-style).
10. **Sub type** -- `Std.Sub` is a normal ADT module (not an FFI wrapper). `Sub` has constructors `SubNone`, `SubTimer Int msg`, `SubBatch (List (Sub msg))`. The Go runtime walks these values to set up SSE subscriptions.
11. **Embedded assets** -- `src/utils/assets.ts` contains embedded stdlib. Must be updated whenever stdlib `.sky` files change.
12. **VNode emission** -- `Std.Html` functions return VNode records (`{ tag, attrs, children, text }`), not HTML strings. Attributes are `(key, value)` tuples. The Go runtime converts these via `MapToVNode` -- no HTML parsing needed. Non-Live apps use `render`/`toString` to convert VNode records to HTML strings.

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
```

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
