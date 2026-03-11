# GEMINI.md

This document provides guidance for AI agents (Gemini CLI, etc.) modifying the Sky compiler codebase.

## Project Overview

Sky is an experimental programming language inspired by Elm.

The repository contains:

- a compiler
- a CLI tool
- a formatter
- a Language Server (LSP)
- Helix editor integration

The compiler is written in **TypeScript** and compiles `.sky` files to **JavaScript (ES modules)**.

---

## Architecture

Compilation pipeline:

source
↓
lexer
↓
layout filtering
↓
parser
↓
AST
↓
type checker
↓
JS emitter

Main source structure:

src/
  lexer.ts
  parser.ts
  ast.ts

  type-system/
    checker.ts
    infer.ts
    unify.ts
    env.ts
    types.ts

  codegen/
    js-emitter.ts

  formatter/
    formatter.ts

  lsp/
    server.ts
    symbol-index.ts
    find-node.ts

  cli.ts
  compiler.ts

---

## Language Goals

Sky syntax intentionally mirrors **Elm** where possible.

Example:

module Foo exposing (main)

add a b =
    a + b

main =
    add 2 3

Design goals:

- simple syntax
- Elm-style pipeline operators
- functional programming
- Hindley–Milner type inference
- deterministic formatting
- strong editor tooling

---

## CLI Commands

sky build file.sky  
sky run file.sky  
sky fmt file.sky  
sky ast file.sky  
sky tokens file.sky  
sky repl  

Formatter also supports stdin:

sky fmt -

---

## LSP Server

The language server is implemented in:

src/lsp/server.ts

Current capabilities:

- diagnostics
- hover
- go-to-definition
- autocomplete
- formatting via CLI

Helix configuration example:

[[language]]
name = "sky"
scope = "source.sky"
file-types = ["sky"]
grammar = "elm"

language-servers = ["sky-lsp"]

formatter = { command = "sky", args = ["fmt", "-"] }

auto-format = true

[language-server.sky-lsp]
command = "sky-lsp"
args = ["--stdio"]

---

## Formatter Rules

The formatter must be deterministic.

Rules:

1. Exactly one blank line after the module declaration.
2. Exactly one blank line between top-level declarations.
3. Four-space indentation.
4. Elm-style spacing.

Example:

module Foo exposing (main)

add a b =
    a + b

main =
    add 2 3

Formatter invariant:

fmt(fmt(code)) == fmt(code)

---

## Code Guidelines

Preserve AST stability.

The AST structure should not change unnecessarily because the formatter, LSP, and type checker depend on it.

Avoid string-based parsing. Always operate on the AST where possible.

Prefer small pure functions.

---

## Testing Changes

After modifications always run:

npm run build

Then test:

sky build examples/Main.sky  
sky run examples/Main.sky  
sky fmt examples/Main.sky  

Verify LSP still runs:

sky-lsp --stdio

---

## Future Work

Potential improvements:

- project-wide symbol index
- module graph type checking
- Elm-style pipeline formatting
- tree-sitter grammar
- package manager
- standard library



# README.md

# Sky

Sky is an experimental programming language inspired by **Elm**.

It aims to provide:

- simple functional syntax
- strong type inference
- predictable formatting
- excellent editor tooling

The compiler is written in **TypeScript** and targets **JavaScript**.

---

## Example

module Examples.Simple.Main exposing (main)

add a b =
    a + b

main =
    add 2 3

Run it:

sky run src/Examples/Simple/Main.sky

---

## Features

Current capabilities:

- Elm-style syntax
- Hindley–Milner type inference
- CLI compiler
- deterministic formatter
- Language Server (LSP)
- Helix editor support
- JavaScript code generation

---

## Installation

Build the compiler:

npm install  
npm run build  
npm link  

This installs:

sky  
sky-lsp  

---

## CLI

sky build file.sky  
sky run file.sky  
sky fmt file.sky  
sky ast file.sky  
sky tokens file.sky  
sky repl  

---

## Formatter

Format a file:

sky fmt file.sky

Format via stdin:

sky fmt -

The formatter enforces a consistent style similar to Elm.

---

## LSP (Language Server)

Sky includes an LSP server providing:

- diagnostics
- hover types
- go-to-definition
- autocomplete

Run the server:

sky-lsp --stdio

---

## Helix Setup

Add to:

~/.config/helix/languages.toml

[[language]]
name = "sky"
scope = "source.sky"
file-types = ["sky"]
grammar = "elm"

language-servers = ["sky-lsp"]

formatter = { command = "sky", args = ["fmt", "-"] }

auto-format = true

[language-server.sky-lsp]
command = "sky-lsp"
args = ["--stdio"]

---

## Project Structure

src/
  lexer.ts
  parser.ts
  ast.ts
  compiler.ts
  cli.ts

  codegen/
  formatter/
  lsp/
  type-system/

---

## Development

Build:

npm run build

Test:

sky run src/Examples/Simple/Main.sky

---

## Status

Sky is currently an experimental language focused on:

- compiler architecture
- language ergonomics
- developer tooling

The language design is still evolving.