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

The compiler is written in **TypeScript**. **Do not add or revert to `.js` files in the `src/` directory.**

---

## Architecture & Pipeline

Compilation pipeline:

`source` → `lexer` → `layout filtering` → `parser` → `AST` → `module graph` → `type checker` → `Go emitter`

Main source structure:

```text
src/
  compiler.ts          // Core compilation pipeline orchestration
  ast/                 // AST definitions
  lexer/               // Indentation-aware Lexer
  parser/              // Pratt-style parser with layout filtering
  modules/             // Module resolution & dependency graph
  types/               // Type system (infer, unify, checker, adt)
  core-ir/             // Core Intermediate Representation
  go-ir/               // Go Intermediate Representation
  emit/                // Go code generation
  lower/               // AST to CoreIR to GoIR lowering passes
  interop/             // Go FFI and package inspection
  pkg/                 // Package manager (installer, lockfile, manifest)
  lsp/                 // Language Server & Formatter
  stdlib/              // Core and Std library modules (.sky files)
  cli/                 // CLI command implementation
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
- zero-friction Go interop (replacing former NPM focus)
- Elm-style Effect System (Cmd, Task, Sub)

---

## CLI Commands

```bash
sky init               # Initializes a new Sky project
sky add <pkg>          # Adds a dependency
sky build file.sky     # Builds to Go and compiles to binary
sky run file.sky       # Builds and immediately executes
sky fmt file.sky       # Formats code (Elm-style)
sky lsp                # Starts Language Server
```

---

## Performance & Optimization

### 1. Incremental Compilation
`compiler.ts` implements a module-level cache based on `mtime` to skip redundant work.

### 2. FFI Caching
`.skycache/go` stores generated bindings to avoid re-inspecting Go packages unnecessarily.

### 3. Tree-shaking
The compiler performs dead-binding elimination on the CoreIR and tree-shakes FFI wrappers during emission.

---

## Core Principles for AI Agents

To prevent regressions, strictly adhere to the following rules:

### 1. Source Integrity
**All source code is TypeScript.** Never commit `.js` files in `src/` (except for specific build scripts like `src/bin/build-binary.js`). If you see `.js` files appearing in `src/`, they are likely build artifacts and should be removed.

### 2. Layout Parser & Robust Indentation
Sky uses an indentation-based parser.
- **Indentation Reference**: The parser uses the column of the *first* token in an expression (like a function application or a branch) as the minimum indentation reference for multi-line continuations.
- **Recovery**: The parser is designed to be robust against imperfectly aligned code, allowing `sky fmt` to fix it. Do not tighten parsing rules such that they break on slightly unaligned input that is otherwise unambiguous.

### 3. Formatter Rules (Elm-style)
- **Indentation**: Standard indentation is 4 spaces.
- **Let Expressions**: `let` and `in` MUST always span multiple lines. 
- **Let Bindings**: 
    - Single-line if the result fits within 80 characters.
    - Multi-line (value indented on a new line after `=`) if it exceeds 80 characters.
- **Structures**: Use leading commas for multi-line records and lists. Force vertical breaks in large structures using `hardline`.

### 4. Universal Unifiers
The type system treats `JsValue`, `Foreign`, and their variants as universal unifiers. Do not remove this logic as it facilitates interop.

### 5. Standard Library Prelude
`Sky.Core.Prelude` is implicitly imported into every file. It includes essential types like `Maybe` and `Result`.

---

## Testing Changes

After modifications always run:

```bash
npm run build
```

Then test against an example project:

```bash
node dist/bin/sky.js fmt examples/simple/src/Main.sky
node dist/bin/sky.js build examples/simple/src/Main.sky
```

Verify the output in `dist/` and ensure the formatter produced correctly aligned code.
