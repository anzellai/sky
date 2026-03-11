# GEMINI.md

This document provides guidance for AI agents (Gemini CLI, etc.) modifying the Sky compiler codebase. **Please read the "Core Principles for AI Agents" section carefully before making changes.**

## Project Overview

Sky is an experimental programming language inspired by **Elm**.

The repository contains:

- a compiler
- a CLI tool
- a formatter
- a Language Server (LSP)
- Helix editor integration

The compiler is written in **TypeScript** and compiles `.sky` files to **JavaScript**. It has built-in support for Native Executable generation using `esbuild` and `pkg`.

---

## Architecture & Pipeline

Compilation pipeline:

`source` → `lexer` → `layout filtering` → `parser` → `AST` → `module graph` → `type checker` → `JS emitter`

Main source structure:

```text
src/
  lexer.ts
  parser.ts
  ast.ts
  compiler.ts          // Core compilation pipeline orchestration
  module-graph.ts      // Topological sort & NPM resolution

  type-system/
    checker.ts         // Environment building & inference kick-off
    infer.ts           // Hindley-Milner type inference
    unify.ts           // Type Unification (Robinson)
    env.ts             // Lexical Type Environment
    types.ts

  codegen/
    js-emitter.ts      // AST to JavaScript

  ffi/                 // NPM & Foreign Function interop
    resolve-npm-import.ts
    collect-foreign.ts
    extract-exports.ts // TS compiler API extraction
    convert-types.ts   // TS -> Sky Type translation

  formatter/
  lsp/
  cli.ts               // CLI entrypoint (build, run, compile)
```

---

## Language Goals

Sky syntax intentionally mirrors **Elm** where possible.

**Example:**
```elm
module Examples.Simple.Main exposing (main)

import Express exposing (express, application_get, application_listen, response_json)

-- Prelude is implicitly imported! (identity, always, unsafeCastFromJson, etc.)
pingHandler req res next =
    response_json res (unsafeCastFromJson "{\"status\": \"ok\", \"ping\": 1}")

main =
    application_listen (application_get (express ()) "/ping" pingHandler) 3042 "localhost" 0 (\err -> ())
```

**Design goals:**
- simple functional syntax
- Elm-style pipeline operators (`|>` and `<|`)
- Hindley–Milner type inference
- deterministic Elm-style formatting (multi-line records, etc.)
- zero-friction NPM integration (via `import Express`)
- fast native binaries

---

## CLI Commands

```bash
sky build file.sky     # Builds to dist/ as ES Modules
sky run file.sky       # Builds and immediately executes the entrypoint
sky compile file.sky   # Builds, bundles (esbuild), and packages as a standalone native binary (pkg)
sky fmt file.sky       # Formats code (Elm-style)
sky ast file.sky       # Dumps AST
sky deps file.sky      # Prints topological dependency order
sky tokens file.sky    # Dumps Lexer tokens
sky repl               # Interactive REPL
```

Formatter also supports stdin: `sky fmt -`

---

## NPM Interop & FFI (Foreign Function Interface)

Sky features automatic invisible interop with the NPM ecosystem. When a user writes `import Express`:

1. **Resolution**: If missing, `resolveNpmImport` automatically queries `@types/express` or `express`, extracting the TypeScript signatures.
2. **Auto-Currying (Thunk Generation)**: It generates a JavaScript wrapper (`__sky_ffi_express.js`) that automatically translates between Sky's purely functional curried world and Node's OOP flat world.
   - Variadic arrays (`...args: T[]`) are flattened to single arguments.
   - JS Class/Interface methods (like `Application.get`) are extracted, lowercased, and exposed as standalone functions (`application_get`) where the first argument is always the `instance`.
   - JS Callbacks are automatically wrapped in asynchronous thunks so Promises can be awaited natively across the boundary.
3. **Stubbing**: It generates a `.sky` stub inside `.skycache/ffi/Sky/FFI/Express.sky` and a sidecar `.json` tracking the true NPM package name.
4. **Typing**: The `Foreign` type is used as a fallback for complex generic TS types. `Foreign` acts as `any` during type unification to prevent the compiler from crashing on unsupported NPM types.

---

## Core Principles for AI Agents

To prevent regressions, strictly adhere to the following rules:

### 1. Type Environment Propagation & LSP
Do **not** revert `checkModule` to checking modules in isolation. 
The module graph topologically sorts dependencies. In `compiler.ts`, the typed `Scheme`s of exported functions are collected into `moduleExports` and passed into `checkModule(..., { imports: importsMap })`. This is what allows the Type Checker and the Language Server (LSP) to know the types of functions imported from other files or FFI stubs. The LSP specifically calls `typeCheckProject` so it receives the fully enriched graph.

### 2. The `Foreign` Type
The type system strictly enforces Hindley-Milner type inference, but **`Foreign` is the exception**. In `src/type-system/unify.ts`, `Foreign` deliberately unifies with **anything** (like TypeScript's `any`). Do not remove this logic, or the compiler will reject NPM packages with complex types.

### 3. FFI Stub Generation
If making changes to `resolveNpmImport.ts` or `collect-foreign.ts`:
- FFI `.sky` stubs must always expose everything: `module Sky.FFI.Name exposing (..)`. If it says `exposing ()`, the `compiler.ts` logic will hide the NPM functions from importers!
- The accompanying `.json` file (`{ "packageName": "uuid" }`) is crucial. `collectForeignImports` relies on it to dynamically generate bindings at compile-time without needing an AST `ForeignImportDeclaration`.
- Globals like `JSON` are explicitly skipped by the FFI NPM resolver but injected natively as `Foreign` bindings.

### 4. JavaScript Emission & Execution
Sky targets two distinct runtime environments via `js-emitter.ts`:
- **`sky run` (ES Modules)**: Node.js runs the emitted code directly from `dist/`. The compiler automatically emits a `package.json` with `{"type": "module"}` in the output directory.
- **`sky compile` (CommonJS Bundle)**: `esbuild` bundles the code into `bundle.cjs`, which is packaged by `pkg`.
- **Constraint**: Emitted code must execute properly in both. For entry-point detection, use the existing hybrid check (checking `require.main === module` for CJS, and `import.meta.url` for ESM).

### 5. Layout Parser (Indentation Rules)
Sky uses an ML/Haskell-style indentation parser (via `src/parser/filter-layout.ts`). All top-level declarations MUST start at column 1. The core parser `parseApplication` and `parseExpression` explicitly break their loops if the next token's indentation drops to `column === 1` or lower than the scoped `minColumn`. Do not change these checks to arbitrary token lookaheads.

### 6. Standard Library Prelude
The compiler implicitly injects `import Sky.Core.Prelude exposing (..)` into every file during `parseModule`. Do not inject it if the file already imports it (to prevent formatter duplication loops). The `module-graph.ts` resolver intercepts `Sky.Core.*` requests and reads them directly from the compiler's bundled `src/stdlib` directory.

### 7. Formatter & AST Stability
The formatter (`src/formatter/formatter.ts`) relies on a builder pattern (`Doc`). 
- Do not use native `Array.join(", ")` on AST nodes. Always use `concat()`, `joinDocs()`, and `text()`. 
- **`hardline` vs `line`:** Always use `hardline` for Elm-style multi-line structures (like Record Expressions `{ a = 1 \n , b = 2 }` and pipelines `|>` / `<|`). `line` dynamically flattens into a space if it fits the 80-char width, which breaks strict vertical Elm formatting.

---

## Testing Changes

After modifications always run:

```bash
npm run build
```

Then test against an example project:

```bash
# Verify it runs (ESM pipeline)
cd sky-examples/ApiServer
sky run

# Verify it compiles to a standalone binary (esbuild + pkg CJS pipeline)
sky compile
./dist/api-server

# Verify formatting
sky fmt src/App/Main.sky  
```

Verify LSP still runs:
```bash
sky-lsp --stdio
```

---

## Future Work

Potential improvements:
- project-wide symbol index
- module graph type checking across cyclic boundaries
- tree-sitter grammar
- explicit List literal parsing `[1, 2, 3]`
- Let-expression parsing `let x = 1 in x`