# Sky

[sky-lang.org](https://sky-lang.org) · [Examples](examples/) · [Docs](docs/)

> **Experimental · v1.0+** — Sky is under active development. APIs and internals may change between minor versions.

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

## Current implementation

The compiler is written in **Haskell** (GHC 9.4+). It handles parsing, Hindley-Milner type inference, canonicalisation, formatting, LSP, and Go codegen. Previous implementations (TypeScript bootstrap, Go, self-hosted Sky) are preserved under `legacy-ts-compiler/` and `legacy-sky-compiler/` for historical reference.

See [docs/compiler/journey.md](docs/compiler/journey.md) for the full compiler history.

## Quick start

```bash
# macOS / Linux
curl -fsSL https://raw.githubusercontent.com/anzellai/sky/main/install.sh | sh

# Or with Docker
docker run --rm -v $(pwd):/app -w /app anzel/sky sky --help
```

> **Prerequisite:** [Go](https://go.dev) 1.21+ installed — Sky compiles to Go.

Create and run a project:

```bash
sky init hello
cd hello
sky run src/Main.sky
```

See [docs/getting-started.md](docs/getting-started.md) for a full walkthrough.

## Documentation

| Area | Link |
|------|------|
| Getting started | [docs/getting-started.md](docs/getting-started.md) |
| Language syntax | [docs/language/syntax.md](docs/language/syntax.md) |
| Types | [docs/language/types.md](docs/language/types.md) |
| Pattern matching | [docs/language/pattern-matching.md](docs/language/pattern-matching.md) |
| Modules | [docs/language/modules.md](docs/language/modules.md) |
| Go FFI interop | [docs/ffi/go-interop.md](docs/ffi/go-interop.md) |
| FFI design | [docs/ffi/ffi-design.md](docs/ffi/ffi-design.md) |
| Error system | [docs/errors/error-system.md](docs/errors/error-system.md) |
| Sky.Live overview | [docs/skylive/overview.md](docs/skylive/overview.md) |
| Sky.Live architecture | [docs/skylive/architecture.md](docs/skylive/architecture.md) |
| Compiler architecture | [docs/compiler/architecture.md](docs/compiler/architecture.md) |
| Compiler pipeline | [docs/compiler/pipeline.md](docs/compiler/pipeline.md) |
| Compiler journey (TS→Go→Sky→Haskell) | [docs/compiler/journey.md](docs/compiler/journey.md) |
| Version history | [docs/compiler/versions.md](docs/compiler/versions.md) |
| CLI reference | [docs/tooling/cli.md](docs/tooling/cli.md) |
| LSP | [docs/tooling/lsp.md](docs/tooling/lsp.md) |

## Status

- **v1.0 — adversarial audit remediation complete (2026-04-16).** All 23 P0–P3 items across soundness, security, cleanup, and tooling landed with regression tests. See [docs/AUDIT_REMEDIATION.md](docs/AUDIT_REMEDIATION.md) for the per-item tracker and [docs/compiler/v1-soundness-audit.md](docs/compiler/v1-soundness-audit.md) for the earlier v1 audit findings.
- **Core principle — "if it compiles, it works"** — now true for every path in `cabal test`, the example sweep, and the runtime Go test matrix. Residual future-work (fully-typed emitted Go, Sky-test harness) tracked in [docs/PRODUCTION_READINESS.md](docs/PRODUCTION_READINESS.md) as P4.
- **18 example projects** under `examples/` covering CLI, HTTP servers, full-stack Sky.Live apps, databases (SQLite, PostgreSQL, Firestore), payments (Stripe), auth, and GUI (Fyne).
- **`sky verify`** is the canonical runtime check: builds *and* runs every example, hits HTTP endpoints, honours per-example `verify.json` scenarios (status code + body substring assertions). CI runs `sky verify` across the full example set.
- **Test matrix:** 47-example hspec suite + ~20 runtime Go tests + 67-file `test-files/*.sky` self-test loop + format idempotency across every example source file.
- **FFI generation:** Stripe SDK (8,896 types), Firestore, Fyne, and others auto-bind.

## Contributing

Issues and PRs welcome. See the docs tree for architecture context before opening a structural PR.

## Licence

MIT.
