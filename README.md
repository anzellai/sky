# Sky

[sky-lang.org](https://sky-lang.org) · [Examples](examples/) · [Docs](docs/)

> **Experimental · v0.9** — Sky is under active development. APIs and internals may change between minor versions.

Sky is an experimental fullstack programming language that combines **Go's pragmatism** with **Elm's elegance**. You write functional, strongly-typed code and ship a single portable binary.

```elm
module Main exposing (main)

import Std.Log exposing (println)

main =
    println "Hello from Sky!"
```

## What Sky brings together

- **Go** — fast compilation, single static binary, access to the full Go ecosystem (databases, HTTP servers, cloud SDKs).
- **Elm** — Hindley-Milner type inference, algebraic data types, exhaustive pattern matching, pure functions, The Elm Architecture.
- **Phoenix LiveView** — server-driven UI with DOM diffing, SSE subscriptions, session management. No client-side framework required.

Sky compiles to Go. One binary runs your API, DB access, and server-rendered interactive UI — one codebase, one language, one deployment artifact.

## Why Sky exists

I've worked professionally with Go, Elm, TypeScript, Python, Dart, Java, and others for years. Each has strengths, but none gave me everything I wanted: **simplicity, strong guarantees, functional programming, fullstack capability, and portability** — all in one language.

The pain point that kept coming back: startups and scale-ups building React/TypeScript frontends talking to a separate backend, creating friction at every boundary — different type systems, duplicated models, complex build pipelines, and the constant uncertainty of "does this actually work?" that comes with the JS ecosystem. Maintenance becomes the real cost, not the initial build.

I always wanted to combine Go's tooling (fast builds, single binary, real concurrency, massive ecosystem) with Elm's developer experience (if it compiles, it works; refactoring is fearless; the architecture scales). Then, inspired by Phoenix LiveView, I saw how a server-driven UI could eliminate the frontend/backend split entirely — one language, one model, one deployment.

The first attempt compiled Sky to JavaScript with the React ecosystem as the runtime. It worked, but Sky would have inherited all the problems I was trying to escape — npm dependency chaos, bundle configuration, and the fundamental uncertainty of a dynamically-typed runtime. So I started over with Go as the compilation target: Elm's syntax and type system on the frontend, Go's ecosystem and binary output on the backend, with auto-generated FFI bindings that let you `import` any Go package and use it with full type safety.

Building a programming language is typically a years-long effort. What made Sky possible in weeks was AI-assisted development — first with Gemini CLI, then settling on Claude Code, which fits my workflow and let me iterate on the compiler architecture rapidly. I designed the language semantics, the pipeline, the FFI strategy, and the Live architecture; AI tooling helped me execute at a pace that would have been impossible alone.

Sky is named for having no limits. It's experimental, opinionated, and built for one developer's ideal workflow — but if it resonates with yours, I'd love to hear about it.

## Current implementation

The compiler is written in **Haskell** (GHC 9.4+). It handles parsing, Hindley-Milner type inference, canonicalisation, formatting, LSP, and Go codegen. Previous implementations (TypeScript bootstrap, Go, self-hosted Sky) are preserved under `legacy-ts-compiler/` and `legacy-sky-compiler/` for historical reference.

See [docs/compiler/journey.md](docs/compiler/journey.md) for the full compiler history.

## Quick start

```bash
# macOS / Linux — single-binary install
curl -fsSL https://raw.githubusercontent.com/anzellai/sky/main/install.sh | sh

# custom installation path
curl -fsSL https://raw.githubusercontent.com/anzellai/sky/main/install.sh | sh -s -- --dir ~/.local/bin

# Or with Docker
docker run --rm -v $(pwd):/app -w /app anzel/sky sky --help
```

> **Prerequisite:** [Go](https://go.dev) 1.21+ installed — Sky compiles to Go and uses Go's toolchain to produce your binary.

Create and run a project:

```bash
sky init hello
cd hello
sky run src/Main.sky
```

Sky ships as a **single `sky` executable**. The FFI-introspection
helper (`sky-ffi-inspect`) is embedded and self-provisions into
`$XDG_CACHE_HOME/sky/tools/` on first `sky add` — no second binary
to install or keep on `$PATH`.

See [docs/getting-started.md](docs/getting-started.md) for a walkthrough.

### Building from source

Contributors: see [docs/development.md](docs/development.md) for the
full build + test story, including the pinned GHC/Go toolchain, the
`./scripts/build.sh` entrypoint, and reproducible builds via Nix:

```bash
# quickest path on any system with nix
nix develop            # GHC 9.4.8 + Go + every system dep, sandboxed
./scripts/build.sh --clean
```

## Documentation

| Area                                 | Link                                                                   |
| ------------------------------------ | ---------------------------------------------------------------------- |
| Getting started                      | [docs/getting-started.md](docs/getting-started.md)                     |
| Language syntax                      | [docs/language/syntax.md](docs/language/syntax.md)                     |
| Types                                | [docs/language/types.md](docs/language/types.md)                       |
| Pattern matching                     | [docs/language/pattern-matching.md](docs/language/pattern-matching.md) |
| Modules                              | [docs/language/modules.md](docs/language/modules.md)                   |
| Go FFI interop                       | [docs/ffi/go-interop.md](docs/ffi/go-interop.md)                       |
| FFI design                           | [docs/ffi/ffi-design.md](docs/ffi/ffi-design.md)                       |
| Error system                         | [docs/errors/error-system.md](docs/errors/error-system.md)             |
| Sky.Live overview                    | [docs/skylive/overview.md](docs/skylive/overview.md)                   |
| Sky.Live architecture                | [docs/skylive/architecture.md](docs/skylive/architecture.md)           |
| Compiler architecture                | [docs/compiler/architecture.md](docs/compiler/architecture.md)         |
| Compiler pipeline                    | [docs/compiler/pipeline.md](docs/compiler/pipeline.md)                 |
| Compiler journey (TS→Go→Sky→Haskell) | [docs/compiler/journey.md](docs/compiler/journey.md)                   |
| Version history                      | [docs/compiler/versions.md](docs/compiler/versions.md)                 |
| CLI reference                        | [docs/tooling/cli.md](docs/tooling/cli.md)                             |
| Testing (`sky test`)                 | [docs/tooling/testing.md](docs/tooling/testing.md)                     |
| LSP                                  | [docs/tooling/lsp.md](docs/tooling/lsp.md)                             |
| Development & contributing           | [docs/development.md](docs/development.md)                             |

## Status

- **v0.9 — adversarial audit remediation complete (2026-04-16).** All 23 P0–P3 items across soundness, security, cleanup, and tooling landed with regression tests. See [docs/AUDIT_REMEDIATION.md](docs/AUDIT_REMEDIATION.md) for the per-item tracker and [docs/compiler/v1-soundness-audit.md](docs/compiler/v1-soundness-audit.md) for the soundness audit findings.
- **Core principle — "if it compiles, it works"** — aspirational. Now holds for every path in `cabal test`, the example sweep, and the runtime Go test matrix. v1.0 requires production usage and bug-fixes to earn the label. Residual future-work (fully-typed emitted Go, Sky-test harness) tracked in [docs/PRODUCTION_READINESS.md](docs/PRODUCTION_READINESS.md) as P4.
- **18 example projects** under `examples/` covering CLI, HTTP servers, full-stack Sky.Live apps, databases (SQLite, PostgreSQL, Firestore), payments (Stripe), auth, and GUI (Fyne).
- **`sky verify`** is the canonical runtime check: builds _and_ runs every example, hits HTTP endpoints, honours per-example `verify.json` scenarios (status code + body substring assertions). CI runs `sky verify` across the full example set.
- **Test matrix:** 47-example hspec suite + ~20 runtime Go tests + 67-file `test-files/*.sky` self-test loop + format idempotency across every example source file.
- **FFI generation:** Stripe SDK (8,896 types), Firestore, Fyne, and others auto-bind.

## Contributing

Issues and PRs welcome. See the docs tree for architecture context before opening a structural PR.

## Licence

MIT.
