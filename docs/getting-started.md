# Getting started

## Install

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/anzellai/sky/main/install.sh | sh
```

**Prerequisite:** [Go](https://go.dev) 1.21+ must be on your `PATH` — Sky compiles to Go source and invokes `go build`.

Verify:

```bash
sky --version
```

## Create a new project

```bash
sky init hello
cd hello
```

This scaffolds:

```
hello/
    sky.toml              -- project manifest
    src/
        Main.sky          -- entry module
    CLAUDE.md             -- template for AI coding assistants
```

## Build and run

```bash
sky run src/Main.sky
```

Under the hood:

1. The compiler reads `sky.toml`.
2. It auto-regenerates any missing Go FFI bindings declared under `[go.dependencies]` into `.skycache/ffi/` and `.skycache/go/`.
3. It lowers your Sky source to `sky-out/main.go`.
4. It copies the runtime + generated wrappers into `sky-out/rt/`.
5. It invokes `go build -o sky-out/app`.
6. It executes `sky-out/app`.

## Add a Go dependency

```bash
sky add github.com/google/uuid
```

`sky add` fetches the Go module, inspects its public API, and generates typed bindings under `.skycache/`. You can then `import Github.Com.Google.Uuid as Uuid` in your Sky source.

See [ffi/go-interop.md](ffi/go-interop.md) for the full FFI story.

## A minimal TEA-style app

```elm
module Main exposing (main)

import Std.Log exposing (println)
import Sky.Core.Prelude exposing (..)


type Msg
    = Increment
    | Decrement


update : Msg -> Int -> Int
update msg count =
    case msg of
        Increment ->
            count + 1

        Decrement ->
            count - 1


main =
    println (String.fromInt (update Increment 0))
```

## Next steps

- [`sky.toml` reference](sky-toml.md) — every project-config field.
- [Language syntax](language/syntax.md) — operators, lambdas, let/in, case/of, pipelines.
- [Pattern matching](language/pattern-matching.md) — destructuring, exhaustiveness.
- [Modules](language/modules.md) — imports, `exposing`, visibility.
- [Sky.Live](skylive/overview.md) — server-driven UI with DOM diffing.
- [CLI reference](tooling/cli.md) — every `sky` command.
