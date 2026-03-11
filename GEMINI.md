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

import Uuid exposing (v4)

add a b =
    a + b

main =
    add (v4 ()) 3
```

**Design goals:**
- simple functional syntax
- Elm-style pipeline operators
- Hindley–Milner type inference
- deterministic formatting
- zero-friction NPM integration (via `import Uuid`)
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

Sky features automatic invisible interop with the NPM ecosystem. 

1. **Resolution**: When a user writes `import Uuid`, the compiler checks for `Uuid.sky`. If missing, `resolveNpmImport` automatically queries `@types/uuid` or `uuid`, extracts the TypeScript signature, and converts it to a Sky type (e.g. `String -> String`).
2. **Stubbing**: It generates a `.sky` stub inside `.skycache/ffi/Sky/FFI/Uuid.sky` and a sidecar `.json` tracking the true NPM package name.
3. **Graphing**: The compiler treats these as `Sky.FFI.Uuid` internally.
4. **Typing**: The `Foreign` type is used as a fallback for complex generic TS types. `Foreign` acts as `any` during type unification to prevent the compiler from crashing on unsupported NPM types.
5. **Emission**: During JS Code Generation, `import * as Uuid from "uuid"` is cleanly injected into the JS output, and explicit exposes are destructured (`const { v4 } = Uuid;`).

---

## Core Principles for AI Agents

To prevent regressions, strictly adhere to the following rules:

### 1. Type Environment Propagation
Do **not** revert `checkModule` to checking modules in isolation. 
The module graph topologically sorts dependencies. In `compiler.ts`, the typed `Scheme`s of exported functions are collected into `moduleExports` and passed into `checkModule(..., { imports: importsMap })`. This is what allows `App.Main` to know the types of functions imported from `Sky.FFI.Uuid`. 

### 2. The `Foreign` Type
The type system strictly enforces Hindley-Milner type inference, but **`Foreign` is the exception**. In `src/type-system/unify.ts`, `Foreign` deliberately unifies with **anything** (like TypeScript's `any`). Do not remove this logic, or the compiler will reject NPM packages with complex types.

### 3. FFI Stub Generation
If making changes to `resolveNpmImport.ts` or `collect-foreign.ts`:
- FFI `.sky` stubs must always expose everything: `module Sky.FFI.Name exposing (..)`. If it says `exposing ()`, the `compiler.ts` logic will hide the NPM functions from importers!
- The accompanying `.json` file (`{ "packageName": "uuid" }`) is crucial. `collectForeignImports` relies on it to dynamically generate bindings at compile-time without needing an AST `ForeignImportDeclaration`.

### 4. JavaScript Emission & Execution
Sky targets two distinct runtime environments via `js-emitter.ts`:
- **`sky run` (ES Modules)**: Node.js runs the emitted code directly from `dist/`. The compiler automatically emits a `package.json` with `{"type": "module"}` in the output directory.
- **`sky compile` (CommonJS Bundle)**: `esbuild` bundles the code into `bundle.cjs`, which is packaged by `pkg`.
- **Constraint**: Emitted code must execute properly in both. For entry-point detection, use the existing hybrid check (checking `require.main === module` for CJS, and `import.meta.url` for ESM).

### 5. AST & Formatter Stability
Modifying the parser (e.g. adding a new expression type like `UnitExpression`) requires updating:
1. `src/ast.ts`
2. `src/parser.ts`
3. `src/formatter/formatter.ts` (Ensure it renders deterministically!)
4. `src/type-system/infer.ts` (Type Inference)
5. `src/codegen/js-emitter.ts` (JS Output)

### 6. Formatting Invariants
The formatter (`src/formatter/formatter.ts`) relies on a builder pattern (`Doc`). Do not use native `Array.join(", ")` on AST nodes. Always use `concat()`, `joinDocs()`, and `text()`. The formatter invariant must hold: `fmt(fmt(code)) == fmt(code)`.

---

## Testing Changes

After modifications always run:

```bash
npm run build
```

Then test against an example project:

```bash
# Verify it runs (ESM pipeline)
sky run examples/Main.sky  

# Verify it compiles to a standalone binary (esbuild + pkg CJS pipeline)
sky compile examples/Main.sky  
./dist/examples-app

# Verify formatting
sky fmt examples/Main.sky  
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
- standard library