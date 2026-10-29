# Sky

**Elm's safety. Go's reach. One binary for the whole stack.**

[Website](https://sky-lang.org) · [Tour](https://anzellai.github.io/sky/learn/index.html) · [Docs](https://anzellai.github.io/sky/) · [API](https://anzellai.github.io/sky/reference.html) · [Examples](examples/) · [Changelog](CHANGELOG.md)

- **If it compiles, it works.** No nulls, no exceptions, exhaustive `case`, and every effect is a typed `Task`. `sky check` runs `go build` on the output, so a shape mismatch fails at check time, not in production.
- **One language, every shape.** The same `init / update / view` runs as a server-driven web app, a wasm client, a terminal UI, a CLI, or a desktop and mobile app. You pick with `--target`.
- **Batteries, not dependencies.** Database, auth, durable workflows, AI agents, Excel and Word files, and any Go package through `sky add`. It all compiles to one binary.

## Install

You need [Go 1.25 or later](https://go.dev/dl/) on your `PATH`, because Sky compiles to Go.

```bash
curl -fsSL https://raw.githubusercontent.com/anzellai/sky/main/install.sh | sh
sky init hello && cd hello
sky run src/Main.sky
```

macOS and Linux. Windows through WSL. `sky upgrade` keeps it current and shows the migration notes for each release.

**Using an AI coding agent?** Every `sky init` project ships `AGENTS.md` and `CLAUDE.md`. Point your agent at them and ask for the app you want. `sky doc <Module>` gives it exact signatures, and `sky check --format json` gives it machine-readable errors.

## Where are you coming from?

| | Why Sky | Start here |
|---|---|---|
| **Elm or Haskell** | Elm syntax and The Elm Architecture, now on the server, in the terminal and on the desktop. Effects are typed Tasks, not ports. | [Coming from another language](docs/learn/18-coming-from-other-languages.md) |
| **Go** | Typed Go comes out. `sky add` turns any Go module into typed Sky bindings, with no glue code to write. A Go error, nil or panic arrives as an `Err`. | [Go interop](docs/ffi/go-interop.md) |
| **Node, Python or Java** | No `node_modules`, no virtualenv, no JVM. One binary to copy to a server. Errors are values, so a case you forgot fails the build, not a request. | [Your first web app](docs/learn/11-first-web-app.md) |
| **AI-assisted coding** | Strict types catch what the model gets wrong. The stdlib has providers, durable agents, a policy firewall, evals, training and serving. | [AI tooling](docs/learn/19-ai-tooling.md) · [Std.Ai](docs/ai/) |

## Show me

### A full-stack todo app with a database, in one file

No JavaScript, no ORM, no SQL. One `Codec` drives the table, the reads and the writes. `update` runs on the server, and the page updates over one SSE connection.

```elm
todos : Store Todo
todos =
    Store.fromCodec "todos" (Codec.auto { id = 0, title = "" })
        |> Store.serial "id"


addTodo : Db -> String -> Task Error (List Todo)
addTodo db title =
    Store.insert db todos { id = 0, title = title }
        |> Task.andThen (\_ -> Store.all db todos)


update : Db -> Msg -> Model -> ( Model, Cmd Msg )
update db msg model =
    case msg of

        Draft text ->
            ( { model | draft = text }, Cmd.none )

        Add ->
            ( { model | draft = "" }
            , Cmd.perform (addTodo db model.draft) Loaded
            )

        Loaded (Ok rows) ->
            ( { model | todos = rows, problem = "" }, Cmd.none )

        Loaded (Err e) ->
            ( { model | problem = errorToString e }, Cmd.none )
```

The whole program, with its view and `main`, is in [docs/showcase.md](docs/showcase.md). It runs on SQLite. Change one line and it runs on PostgreSQL.

### Any Go package, typed

```bash
sky add github.com/google/uuid
```

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Result as Result
import Std.Log exposing (println)
import Github.Com.Google.Uuid as Uuid


main =
    case Uuid.newString () of

        Ok uuid ->
            println ("Generated UUID: " ++ uuid)

        Err e ->
            println ("Failed to generate UUID: " ++ errorToString e)
```

This is [`examples/03-tea-external`](examples/03-tea-external). Sky reads the Go package and writes the bindings. A Go `(T, error)` becomes `Result Error T`, and a nil or a panic becomes an `Err`, never a crash. The Stripe SDK (about 76,000 symbols) builds this way in [`examples/13-skyshop`](examples/13-skyshop).

### A durable AI agent that the model cannot overrule

```elm
firewall : Policy.Action -> Policy.Decision
firewall act =
    Policy.requireApprovalAbove Policy.Low act


replyBody : Ctx -> Ticket -> Task Error String
replyBody ctx ticket =
    Durable.step
        ctx
        chatCodec
        "draft"
        (Provider.chat
            provider
            [ Provider.system "Draft a short, kind reply."
            , Provider.user ticket.text
            ])
        |> Task.andThen
               (\draft ->
                   Durable.step
                       ctx
                       Codec.string
                       "send"
                       (send ticket draft.content))


send : Ticket -> String -> Task Error String
send ticket reply =
    Policy.gate
        firewall
        (Policy.action "email.send" ticket.customer Policy.Low)
        (deliver ticket reply)
        |> Task.map outcome
```

The model only proposes an action. Your pure `firewall` decides, and an action above `Low` risk waits for a human. Each `Durable.step` is journalled, so a crash resumes the run and never pays for the same model call twice. The whole program is in [docs/showcase.md](docs/showcase.md), and a full Slack bot is [`examples/66-slack-agent`](examples/66-slack-agent).

### One source, many targets

```bash
sky build src/Main.sky                          # web app, server-driven (Sky.Live)
sky build --target web:app src/Main.sky         # client-side wasm (Sky.Spa)
sky build --target terminal:tui src/Main.sky    # full-screen terminal UI
sky build --target desktop src/Main.sky         # native desktop window (macOS)
sky build --target mobile:ios src/Main.sky      # iOS app (needs Xcode)
```

The same `view : Model -> Element Msg` paints to the DOM, to terminal cells and to a native web view.

## What you get

- **Safety.** Hindley-Milner inference, exhaustive pattern matching, `Result` and `Maybe` instead of null, and typed `Secret` values that redact themselves in every log.
- **App shapes.** One `Std.App` entry point for a server-driven web UI, a wasm client for web, desktop, iOS and Android, a terminal UI and a CLI. `Sky.Http.Server` for HTTP and JSON APIs. [Std.App](docs/skyapp/overview.md)
- **Data.** `Std.Db` on SQLite or PostgreSQL with the same code, one `Std.Codec` for JSON, XML and tables, and file-based migrations. `sky build --embed` puts PostgreSQL inside your binary, and an app plus its database fits in 1 GB. [Std.Db](docs/skydb/overview.md) · [Sizing](docs/perf/sizing.md)
- **Files and documents.** `Std.Office.Xlsx` and `Std.Office.Docx` read and write Excel and Word files. `Std.Xml` and `Std.Zip` read hostile input safely. `Std.Image` and `Std.Image.Canvas` resize, re-encode and draw images on the server. [Stdlib](docs/stdlib.md)
- **Auth, money and crypto.** `Std.Auth` (bcrypt, JWT, roles), `Std.Money` on exact decimals, and XChaCha20, Ed25519, X25519 and Noise sessions for encryption and signatures.
- **Durable workflows.** `Std.Durable` runs multi-step processes that survive a restart. [Design](docs/design/durable-execution.md)
- **AI.** `Std.Ai`: swappable providers (OpenAI, any compatible endpoint, or a managed local model), durable agents, a policy firewall, cost tracing in `Std.Money`, datasets, evals, a model registry, LoRA training, GPU serving and on-device models. [Guides](docs/ai/)
- **Tooling.** `sky fmt`, `sky test`, `sky fuzz`, `sky watch`, an LSP, `sky doc` for the live API, and an OpenAPI spec from your source with `sky doc --api openapi`. [CLI](docs/tooling/cli.md)
- **Operations.** Structured logs, Prometheus metrics, health checks, OpenTelemetry and a built-in console at `/_sky/console`. [Observability](docs/observability.md)

## Real examples

There are 73 projects in [`examples/`](examples/). Start with these:

| Example | What it shows |
|---|---|
| [`19-skyforum`](examples/19-skyforum) | A multi-module forum on Sky.Live |
| [`13-skyshop`](examples/13-skyshop) | A shop with Stripe and Firebase through `sky add` |
| [`60-spa-todos`](examples/60-spa-todos) | One app on web, desktop, iOS and Android |
| [`26-ui-showcase`](examples/26-ui-showcase) | Every `Std.Ui` element |
| [`23-tui-todo`](examples/23-tui-todo) | A terminal todo app |
| [`65-metadata-service`](examples/65-metadata-service) | A stateless JSON API |
| [`66-slack-agent`](examples/66-slack-agent) | A durable Slack bot behind a policy firewall |
| [`72-ai-support-desk`](examples/72-ai-support-desk) | A support desk with the full AI lifecycle |

## Status

> **Status: v0.29.x, pre-1.0.** A minor version can include breaking changes.
> Each one is listed in the [CHANGELOG](CHANGELOG.md) with a migration guide
> under [`docs/migration/`](docs/migration/), and `sky upgrade` shows it. See
> [VERSIONING.md](VERSIONING.md) and the
> [known limitations](docs/KNOWN_LIMITATIONS.md).

The compiler is a single Rust binary that embeds the Go runtime, the stdlib and the Sky Console. The earlier compilers are in [docs/history/legacy-compilers.md](docs/history/legacy-compilers.md).

## Learn more

- [Learn Sky](https://anzellai.github.io/sky/learn/index.html): from your first app to a deployed web app.
- [Guides](https://anzellai.github.io/sky/guide/index.html) and the [API reference](https://anzellai.github.io/sky/reference.html). Offline: `sky doc --serve`.
- [Deploying](docs/learn/17-deploying.md): Docker, VMs, embedded PostgreSQL and the production settings.

## Contributing

Issues and pull requests are welcome. Start with [CONTRIBUTING.md](CONTRIBUTING.md) and the [compiler architecture](docs/rust-rewrite/). To build from source: `cd rust && cargo build --release -p sky`.

## Licence

[Apache 2.0](LICENSE). © 2025 to 2026 Anzel Lai. See [NOTICE.md](NOTICE.md) for prior-art attribution. Releases up to v0.10.0 remain available under their original MIT terms.
