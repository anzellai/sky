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

The compiler is written in **TypeScript** and compiles `.sky files` to **JavaScript**. 

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
  runtime/             // Built-in JS runtimes (React, Node, Interop)
  stdlib/              // Core and Std library modules
  cli.ts               // CLI entrypoint (build, run, compile)
```

---

## Language Goals

Sky syntax intentionally mirrors **Elm** where possible.

**Example:**
```elm
module Main exposing (main)

import Ui exposing (column, text)

main =
    column { style = { padding = "20px" } }
        [ text {} "Hello from Sky" ]
```

**Design goals:**
- simple functional syntax
- Elm-style pipeline operators (`|>` and `<|`)
- Hindley–Milner type inference
- deterministic Elm-style formatting (leading commas, multi-line records)
- zero-friction NPM integration
- Elm-style Effect System (Cmd, Task, Sub)

---

## CLI Commands

```bash
sky build file.sky     # Builds to dist/ as ES Modules
sky run file.sky       # Builds and immediately executes the entrypoint
sky bundle             # Builds and bundles sky/sky-lsp into standalone binaries
sky fmt file.sky       # Formats code (Elm-style)
sky ast file.sky       # Dumps AST
sky deps file.sky      # Prints topological dependency order
sky tokens file.sky    # Dumps Lexer tokens
sky repl               # Interactive REPL
```

Formatter also supports stdin: `sky fmt -`

---

## Effect System (TEA)

Sky implements the **Elm Architecture (TEA)** pattern:

1.  **`Std.Cmd msg`**: Represents side-effects (HTTP, Random, IO).
2.  **`Std.Sub msg`**: Represents subscriptions to external events (Time, Sockets).
3.  **`Std.Task err value`**: Represents an asynchronous unit of work.
4.  **`Std.Program`**: Defines application logic (`init`, `update`, `view`, `subscriptions`).

The runtime (`src/runtime/program-react.ts` or `src/runtime/program-node.ts`) interprets these pure data structures.

---

## JS Interop & Sky.Interop

Sky uses a safe, decoder-based boundary for JS interop:

- **`JsValue`**: A safe type for opaque JavaScript values.
- **`Decoder a`**: Safely extracts Sky values from `JsValue`.
- **`Sky.Interop`**: The boundary module for decoders and encoders.
- **`Sky.FFI.*`**: Low-level, raw generated bindings from NPM packages.
- **`Std.*`**: Curated, idiomatic Sky wrappers around raw FFI.

---

## Core Principles for AI Agents

To prevent regressions, strictly adhere to the following rules:

### 1. Type Environment Propagation & LSP
Do **not** revert `checkModule` to checking modules in isolation. 
The module graph topologically sorts dependencies. In `compiler.ts`, the typed `Scheme`s of exported functions (including qualified names like `Std.Cmd.none`) are collected and passed into `checkModule`.

### 2. Universal Unifiers (`JsValue` and `Foreign`)
The type system treats `JsValue`, `Foreign`, and their variants as universal unifiers in `src/type-system/unify.ts`. Do not remove this logic, as it allows Sky to safely interact with dynamically typed JavaScript.

### 3. JavaScript Emission & Target Awareness
Sky supports multiple targets (`web`, `node`, `native`) configured in `sky.toml`.
- **Target Mapping**: `js-emitter.ts` automatically maps generic FFI imports (like `@sky/runtime/program`) to target-specific implementations (like `program-node.js`).
- **Relative Imports**: Emitted JS uses relative paths for internal runtime modules to ensure portability.

### 4. Layout Parser & Formatter
Sky uses an indentation-based parser.
- **Indentation**: Standard indentation is 4 spaces.
- **Formatter**: Produces Elm-style layouts with leading commas for multi-line structures. It uses `hardline` to force vertical breaks in records, lists, and `if/let` expressions.

### 5. Standard Library Prelude
`Sky.Core.Prelude` is implicitly imported into every file. It includes essential globals like `console` and escape hatches like `unsafeAny`.

---

## Testing Changes

After modifications always run:

```bash
npm run build
npm run bundle
```

Then test against an example project:

```bash
cd src/Examples/Ui/Counter
sky build src/Main.sky
node dist/Main.js
```

Verify LSP still runs:
```bash
sky-lsp --stdio
```
