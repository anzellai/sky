# Sky

[sky-lang.org](https://sky-lang.org) · [Examples](examples/) · [Docs](docs/)

> **Experimental · v0.10** — Sky is under active development. APIs and internals may change between minor versions.

Sky is an experimental fullstack programming language that combines **Go's pragmatism** with the **elegance of pure-functional, ML-family languages**. You write functional, strongly-typed code with a batteries-included stdlib — `Sky.Live` for server-driven UI, `Std.Db` for SQL persistence, `Std.Auth` for sessions, `Sky.Core.Error` for unified error handling — import any Go package with auto-generated FFI bindings (no hand-written glue), and ship a single portable binary. Sky's explicit types, exhaustive pattern matching, and strict `Task` effect boundary make it **AI-friendly by design**: both humans and LLMs tend to write code that compiles the first time.

```elm
module Main exposing (main)

import Std.Log exposing (println)

main =
    println "Hello from Sky!"
```

## What Sky brings together

- **A Go compilation target** — fast compilation, single static binary, access to the full Go ecosystem (databases, HTTP servers, cloud SDKs).
- **A pure-functional, ML-family front-end** — Hindley-Milner type inference, algebraic data types, exhaustive pattern matching, pure functions, model/update/view/subscriptions architecture (TEA).
- **Server-driven UI** — DOM diffing, SSE subscriptions, session management on the server. No client-side framework required. (Same architectural style popularised by Phoenix LiveView; design + implementation independent.)

Sky compiles to Go. One binary runs your API, DB access, and server-rendered interactive UI — one codebase, one language, one deployment artifact.

> Sky's surface syntax is deliberately compatible with the Elm language (BSD-3-Clause, © Evan Czaplicki and contributors) and several files in the type-inference core are derivative works adapted from elm/compiler. Full attribution and licence text in [NOTICE.md](NOTICE.md).

## Why Sky exists

I've worked professionally with Go, Elm, TypeScript, Python, Dart, Java, and others for years. Each has strengths, but none gave me everything I wanted: **simplicity, strong guarantees, functional programming, fullstack capability, and portability** — all in one language.

The pain point that kept coming back: startups and scale-ups building React/TypeScript frontends talking to a separate backend, creating friction at every boundary — different type systems, duplicated models, complex build pipelines, and the constant uncertainty of "does this actually work?" that comes with the JS ecosystem. Maintenance becomes the real cost, not the initial build.

I always wanted to combine Go's tooling (fast builds, single binary, real concurrency, massive ecosystem) with the developer experience that strong static types and pure functions give you (if it compiles, it works; refactoring is fearless; the architecture scales). After seeing what Phoenix LiveView demonstrated about server-driven UI, I wanted that same architectural style — one language, one model, one deployment, no frontend/backend split.

The first attempt compiled Sky to JavaScript with the React ecosystem as the runtime. It worked, but Sky would have inherited all the problems I was trying to escape — npm dependency chaos, bundle configuration, and the fundamental uncertainty of a dynamically-typed runtime. So I started over with Go as the compilation target: a Hindley-Milner type system + ML-family syntax on the frontend, Go's ecosystem and binary output on the backend, with auto-generated FFI bindings that let you `import` any Go package and use it with full type safety.

Building a programming language is typically a years-long effort. What made Sky possible in weeks was AI-assisted development — first with Gemini CLI, then settling on Claude Code, which fits my workflow and let me iterate on the compiler architecture rapidly. I designed the language semantics, the pipeline, the FFI strategy, and the Live architecture; AI tooling helped me execute at a pace that would have been impossible alone.

Sky is named for having no limits. It's experimental, opinionated, and built for one developer's ideal workflow — but if it resonates with yours, I'd love to hear about it.

## Current implementation

The compiler is written in **Haskell** (GHC 9.4+). It handles parsing, Hindley-Milner type inference, canonicalisation, formatting, LSP, and Go codegen. Previous implementations (TypeScript bootstrap, Go, self-hosted Sky) are preserved under `legacy-ts-compiler/` and `legacy-sky-compiler/` for historical reference.

See [docs/compiler/journey.md](docs/compiler/journey.md) for the full compiler history.

## What's in the box

Sky is **batteries-included**. Three killer modules cover the common needs of any modern web app — no plugins, no separate services, no `npm install`:

### Sky.Live — server-driven UI

The TEA pattern (model / update / view / subscriptions), but the server is authoritative. No client framework, no JSON API contracts, no bundler. Browser runs ~2 KB of JS for DOM diffing + SSE — that's it.

```elm
type Msg = Increment | Decrement

update msg model =
    case msg of
        Increment -> ( { model | count = model.count + 1 }, Cmd.none )
        Decrement -> ( { model | count = model.count - 1 }, Cmd.none )

view model =
    div []
        [ button [ onClick Increment ] [ text "+" ]
        , span [] [ text (String.fromInt model.count) ]
        , button [ onClick Decrement ] [ text "-" ]
        ]
```

Full TEA loop with `init / update / view / subscriptions`, async work via `Cmd.perform`, persistent sessions across deploys (memory / SQLite / Redis / Postgres / Firestore). See [Sky.Live overview](docs/skylive/overview.md).

### Std.Auth — authentication, in the box

bcrypt password hashing, HMAC-SHA256 JWTs, plus optional DB-backed `register` / `login` that creates the users table for you. No `passport`, no `bcryptjs`, no auth microservice.

```elm
Auth.register db "alice@example.com" password
    |> Task.andThenResult
        (\uid ->
            Auth.signToken secret (Dict.fromList [ ( "sub", String.fromInt uid ) ]) 86400
        )
```

Production-grade defaults: minimum-32-byte secret enforcement, constant-time password compare, configurable bcrypt cost, rate-limit-friendly. See [Sky.Auth overview](docs/skyauth/overview.md).

### Std.Db — one API for SQLite + Postgres

Parameter-safe queries, transactions, conventional CRUD helpers (`insertRow` / `getById` / `updateById` / `deleteById`), row decoders. Switch driver in `sky.toml`; never touch it again in your code.

```elm
Db.withTransaction db (\tx ->
    Db.exec tx "UPDATE accounts SET balance = balance - ? WHERE id = ?" [ amount, fromId ]
        |> Task.andThen (\_ ->
            Db.exec tx "UPDATE accounts SET balance = balance + ? WHERE id = ?" [ amount, toId ]
        )
)
```

See [Std.Db overview](docs/skydb/overview.md).

### Std.Ui — typed layout DSL (no CSS files)

Build a UI from typed primitives (`row`, `column`, `el`, `paragraph`, `textColumn`, `link`, `image`, `button`, `input`, `form`, `html`) and typed attributes from focused sub-modules (`Background.color`, `Border.rounded`, `Border.shadow`, `Font.size`, `Font.italic`, `Region.heading`, `Region.mainContent`, …). Renders to inline-styled HTML on the server side with semantic tags (`<main>`, `<nav>`, `<aside>`, `<footer>`, `<h1>`..`<h6>`) dispatched from `Region.*`; Sky.Live ferries diffs to the browser. Form controls (`Input.email`/`Input.newPassword`/`Input.radio`/`Input.slider`/…), nearby positioning (`Ui.above`/`Ui.below`/`Ui.inFront`/…), and overflow control (`Ui.clip`/`Ui.scrollbars`) all on the same flat surface. Same mental model as Sky's stdlib elsewhere — no CSS, no class names, no flexbox quirks. (Prior-art attribution: see [NOTICE.md](NOTICE.md).)

```elm
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Ui.Font as Font

view model =
    Ui.layout []
        (Ui.row
            [ Ui.spacing 12, Ui.padding 16, Background.color (Ui.rgb 255 102 0) ]
            [ Ui.button [] { onPress = Just Decrement, label = Ui.text "−" }
            , Ui.el [ Font.size 24, Font.bold ] (Ui.text (String.fromInt model.count))
            , Ui.button [] { onPress = Just Increment, label = Ui.text "+" }
            ])
```

Plus typed events (`onClick / onSubmit / onInput`), forms with the password best-practice pattern (`Ui.form` + `onSubmit DoSignIn` decoding wire formData into a typed record — secret never enters Model), and file/image upload with browser-side resize hints (`Ui.onImage AvatarSelected, Ui.fileMaxWidth 800`). See [Sky.Ui overview](docs/skyui/overview.md).

### Plus the rest of the stdlib

Crypto, JSON, HTTP client/server, file I/O, time, regex, encoding (base64 / hex / URL), structured logging, UUIDs, async tasks, parallel execution. See [Standard library reference](docs/stdlib.md) for the full surface.

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
| **Standard library reference**       | [docs/stdlib.md](docs/stdlib.md)                                       |
| **Sky.Auth overview**                | [docs/skyauth/overview.md](docs/skyauth/overview.md)                   |
| **Std.Db overview**                  | [docs/skydb/overview.md](docs/skydb/overview.md)                       |
| Sky.Live overview                    | [docs/skylive/overview.md](docs/skylive/overview.md)                   |
| Sky.Live architecture                | [docs/skylive/architecture.md](docs/skylive/architecture.md)           |
| **Std.Ui overview** (typed layout DSL)  | [docs/skyui/overview.md](docs/skyui/overview.md)                    |
| Compiler architecture                | [docs/compiler/architecture.md](docs/compiler/architecture.md)         |
| Compiler pipeline                    | [docs/compiler/pipeline.md](docs/compiler/pipeline.md)                 |
| Compiler journey (TS→Go→Sky→Haskell) | [docs/compiler/journey.md](docs/compiler/journey.md)                   |
| Version history                      | [docs/compiler/versions.md](docs/compiler/versions.md)                 |
| CLI reference                        | [docs/tooling/cli.md](docs/tooling/cli.md)                             |
| Testing (`sky test`)                 | [docs/tooling/testing.md](docs/tooling/testing.md)                     |
| LSP                                  | [docs/tooling/lsp.md](docs/tooling/lsp.md)                             |
| Development & contributing           | [docs/development.md](docs/development.md)                             |

## Status

- **v0.10 — stdlib consolidation + soundness gaps closed (2026-04-25, BREAKING).** Single canonical module per concern (drop `Args` / `Env` / `Sha256` / `Hex` / `Slog`; rename `Os` → `System`; shrink `Process` to `run`); type errors in dep modules and FFI / kernel return shapes now abort the build instead of silently degrading to `any`-typing. See [docs/V0.10.0_PR_SUMMARY.md](docs/V0.10.0_PR_SUMMARY.md) for the full migration guide.
- **v0.9 — adversarial audit remediation complete (2026-04-16).** All 23 P0–P3 items across soundness, security, cleanup, and tooling landed with regression tests. See [docs/AUDIT_REMEDIATION.md](docs/AUDIT_REMEDIATION.md) for the per-item tracker and [docs/compiler/v1-soundness-audit.md](docs/compiler/v1-soundness-audit.md) for the soundness audit findings.
- **Core principle — "if it compiles, it works"** — aspirational. Now holds for every path in `cabal test`, the example sweep, and the runtime Go test matrix. v1.0 requires production usage and bug-fixes to earn the label. Residual future-work (fully-typed emitted Go, Sky-test harness) tracked in [docs/PRODUCTION_READINESS.md](docs/PRODUCTION_READINESS.md) as P4.
- **19 example projects** under `examples/` covering CLI, HTTP servers, full-stack Sky.Live apps, databases (SQLite, PostgreSQL, Firestore), payments (Stripe), auth, GUI (Fyne), and a Reddit/HackerNews-style forum on Std.Ui (`19-skyforum`).
- **`sky verify`** is the canonical runtime check: builds _and_ runs every example, hits HTTP endpoints, honours per-example `verify.json` scenarios (status code + body substring assertions). CI runs `sky verify` across the full example set.
- **Test matrix:** 47-example hspec suite + ~20 runtime Go tests + 67-file `test-files/*.sky` self-test loop + format idempotency across every example source file.
- **FFI generation:** Stripe SDK (8,896 types), Firestore, Fyne, and others auto-bind.

## Contributing

Issues and PRs welcome. See the docs tree for architecture context before opening a structural PR.

## Licence

[Apache 2.0](LICENSE) — © 2025–2026 Anzel Lai.

This includes a patent grant from contributors and a trademark clause; see the licence text for the full terms. Prior-art attribution for derivative-work files (notably parts of the type-inference core, adapted from elm/compiler under BSD-3-Clause) lives in [NOTICE.md](NOTICE.md). Contributions are accepted under the same Apache 2.0 terms — see [CONTRIBUTING.md](CONTRIBUTING.md).

> Sky was previously distributed under the MIT licence (releases up to and including v0.10.0). Those releases remain available under their original MIT terms; v0.10.1 onwards ships under Apache 2.0.
