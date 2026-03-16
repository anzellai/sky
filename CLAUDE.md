# CLAUDE.md

## Project Overview

Sky is an experimental programming language inspired by **Elm**, compiling to **Go**. The repo contains a compiler, CLI, formatter, LSP, and Helix editor integration — all written in **TypeScript**.

## Architecture & Pipeline

```
source → lexer → layout filtering → parser → AST → module graph → type checker → Go emitter
```

```
src/
  compiler.ts          — Core compilation pipeline orchestration
  ast/ast.ts           — AST node definitions
  lexer/lexer.ts       — Indentation-aware lexer
  parser/              — Pratt-style parser with layout filtering
    parser.ts, filter-layout.ts, operator-table.ts, sections.ts
  modules/resolver.ts  — Module resolution & dependency graph
  types/               — HM type system (infer, unify, checker, adt, exhaustiveness, patterns)
  core-ir/core-ir.ts   — Core Intermediate Representation
  go-ir/go-ir.ts       — Go Intermediate Representation
  lower/               — AST → CoreIR → GoIR lowering + dead-binding elimination
  emit/go-emitter.ts   — Go code generation
  interop/go/          — Go FFI: collect-foreign, generate-bindings, generate-wrappers, inspect-package
  pkg/                 — Package manager (installer, lockfile, manifest, registry, resolver)
  lsp/                 — Language Server (completion, definition, hover, signature, formatter)
  stdlib/              — Core/Std library .sky files (Prelude, Maybe, String, Cmd, Task, Sub, Log, etc.)
  cli/                 — CLI commands (init, add, remove, install, update, build, run, fmt)
  bin/                 — Entry points: sky.ts, sky-lsp.ts, build-binary.js
  utils/               — Helpers (assets.ts, path.ts)
```

## Build & Test

```bash
npm run build          # TypeScript → dist/
node dist/bin/sky.js fmt examples/simple/src/Main.sky
node dist/bin/sky.js build examples/simple/src/Main.sky
node dist/bin/sky.js run examples/01-hello-world/src/Main.sky
```

## Critical Rules

1. **TypeScript only** — Never commit `.js` files in `src/` (except `src/bin/build-binary.js`).
2. **Indentation parser** — The parser uses the column of the first token as the minimum indentation reference. Do not tighten rules that break slightly unaligned input.
3. **Formatter (Elm-style)** — 4-space indent, leading commas, `let`/`in` always multiline, 80-char line width.
4. **Universal unifiers** — `JsValue`, `Foreign`, and variants are universal unifiers for interop. Do not remove.
5. **Prelude** — `Sky.Core.Prelude` is implicitly imported everywhere.
6. **Go FFI** — Wrappers accept `any` params with internal type assertions. Always overwrite `00_sky_helpers.go`. Emitted packages prefixed `sky_` (except `main`).
7. **AST lowering** — Uppercase identifiers = Constructors unless declared as `foreign import` (then lower as Variable). Don't inject `GoTypeAssertExpr` on FFI return values.
8. **Pipeline operators** — `|>` and `<|` (Elm-style).

## Examples

Located in `examples/` with numbered directories:
- `01-hello-world` — Basic hello world
- `02-go-stdlib` — Using Go standard library (crypto, encoding, net/http, time)
- `03-tea-external` — TEA architecture with external packages
- `04-local-pkg` — Multi-module project with local packages
- `05-mux-server` — HTTP server with gorilla/mux + godotenv

## Language Syntax (Elm-like)

```elm
module Main exposing (main)

import Ui exposing (column, text)

main =
    column { style = { padding = "20px" } }
        [ text {} "Hello from Sky" ]
```
