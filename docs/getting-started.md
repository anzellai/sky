# Getting started

> **v0.15 state**: type-directed lowering throughout — lambdas,
> record-field inits, list elements, and call args all carry the
> slot's typed Go form. Go generics on parametric record aliases
> (`type Cfg_R[T1 any] struct{...}`). Same-module polymorphic call
> re-instantiation. Layer-3 stdlib (every kernel module surfaced as
> Sky source), whole-program DCE (Stripe-SDK scale: −82 % source),
> auto-TCO for tail-recursive Sky functions, real runtime
> verification across all 27 examples (120 stdlib assertions + 306
> cabal specs). See [`compiler/versions.md`](compiler/versions.md)
> for the full changelog.

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

## Watch mode (hot reload)

For tight feedback loops, use `sky watch` instead — it rebuilds and respawns the binary on every save, reusing all the existing compile caches:

```bash
sky watch                        # entry: src/Main.sky
sky watch src/Main.sky           # explicit
sky watch --no-run               # rebuild only (no spawn)
sky watch --watch=docs/notes.md  # add an extra path (repeatable)
```

The watcher follows a strict allowlist: `sky.toml`, the entry-point's directory (recursive `.sky` walk), and `tests/` if present. Generated dirs (`sky-out/`, `.skycache/`, `.skydeps/`) are excluded.

A failing rebuild is non-destructive — the previously-running binary keeps serving while you fix the typo. The next successful build kills + respawns. Ctrl-C cleans up cleanly with no zombie processes.

For Sky.Live apps: pair `sky watch` with a persistent session store (sqlite/redis) and your in-progress UI state survives every restart — the runtime's SSE handshake auto-reconnects, the input-preservation rules keep typed values intact. Watch will print a one-line tip at startup if your sky.toml is configured with the memory store.

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
