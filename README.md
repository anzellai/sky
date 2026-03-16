# Sky

Sky is an experimental programming language inspired by [Elm](https://elm-lang.org/), compiling to [Go](https://go.dev/). It features Elm-like syntax with Hindley-Milner type inference, algebraic data types, pattern matching, and seamless Go interop via FFI.

The compiler, CLI, formatter, and LSP are all written in TypeScript.

```
source → lexer → layout filtering → parser → AST → module graph → type checker → Go emitter
```

## Language Syntax

```elm
module Main exposing (main)

import Std.Log exposing (println)

main =
    println "Hello from Sky!"
```

Sky supports Elm-style features including:
- Module system with `exposing` and qualified imports
- Algebraic data types and pattern matching
- `let`/`in` expressions
- Pipeline operators (`|>` and `<|`)
- Go FFI via `foreign import`
- TEA (The Elm Architecture) for structuring applications

## Prerequisites

- [Node.js](https://nodejs.org/) (v18+)
- [Go](https://go.dev/) (for running compiled output)

## Building from Source

```bash
# Install dependencies
npm install

# Compile TypeScript to dist/
npm run build
```

After building, the compiler is available at `dist/bin/sky.js`:

```bash
node dist/bin/sky.js --help
```

You can add `dist/bin/` to your `PATH` for convenience:

```bash
export PATH="/path/to/sky/dist/bin:$PATH"
```

### Building a Self-Contained Binary

To produce a standalone native binary that requires no Node.js runtime:

```bash
npm run bundle
```

This bundles the compiler with esbuild, embeds the standard library, and uses [pkg](https://github.com/vercel/pkg) to produce native executables in `bin/`:

- `bin/sky` — the compiler CLI
- `bin/sky-lsp` — the language server

Copy these anywhere on your `PATH`:

```bash
cp bin/sky /usr/local/bin/sky
cp bin/sky-lsp /usr/local/bin/sky-lsp
```

## Usage

### Build a Sky project

```bash
sky build examples/01-hello-world/src/Main.sky
```

This compiles `.sky` files to Go, then builds the Go binary.

### Run a Sky project

```bash
sky run examples/01-hello-world/src/Main.sky
```

### Format Sky files

```bash
sky fmt examples/01-hello-world/src/Main.sky
```

The formatter follows Elm conventions: 4-space indent, leading commas, `let`/`in` always multiline, 80-char line width.

### Package Management

```bash
sky init          # Initialize a new Sky project
sky add <pkg>     # Add a dependency
sky remove <pkg>  # Remove a dependency
sky install       # Install dependencies from lockfile
sky update        # Update dependencies
```

## Examples

The `examples/` directory contains working projects:

| Example | Description |
|---------|-------------|
| `01-hello-world` | Basic hello world |
| `02-go-stdlib` | Using Go standard library (crypto, encoding, net/http, time) |
| `03-tea-external` | TEA architecture with external packages |
| `04-local-pkg` | Multi-module project with local packages |
| `05-mux-server` | HTTP server with gorilla/mux + godotenv |
| `06-json` | JSON encoding and decoding |
| `07-todo-cli` | Todo app with SQLite and complex FFI types |

Run any example with:

```bash
sky run examples/01-hello-world/src/Main.sky
```

## Editor Support

An LSP server is included for editor integration. The LSP provides completions, go-to-definition, hover information, signature help, and formatting.

```bash
sky-lsp
```

## Project Structure

```
src/
  compiler.ts          — Core compilation pipeline
  ast/                 — AST node definitions
  lexer/               — Indentation-aware lexer
  parser/              — Pratt-style parser with layout filtering
  modules/             — Module resolution & dependency graph
  types/               — HM type system (infer, unify, exhaustiveness, patterns)
  lower/               — AST → CoreIR → GoIR lowering
  emit/                — Go code generation
  interop/go/          — Go FFI bindings
  pkg/                 — Package manager
  lsp/                 — Language server
  stdlib/              — Standard library (.sky files)
  cli/                 — CLI commands
  bin/                 — Entry points
```

## License

This project is experimental and under active development.
