# Sky

[sky-lang.org](https://sky-lang.org) · [Docs](docs/) · [Examples](examples/)

> **Status: v0.19.x release candidate.** Public APIs are stable for the
> v1.0 line; minor versions ship features additively. Internals can
> still change between minor versions. The compiler is now written in
> Rust (cargo workspace at `rust/`) — the typed-Go output and the
> "if it compiles, it works" guarantee carry over unchanged. The
> retired Haskell compiler stays under `legacy-haskell-compiler/` as
> the differential oracle.

Sky is a **fullstack functional language that compiles to typed Go**.
You write Elm-style syntax — explicit types, exhaustive pattern matching,
no runtime exceptions — and ship a single static binary with a
batteries-included stdlib, observability built in, and any Go package
just an `import` away.

```elm
module Main exposing (main)

import Std.Log exposing (println)

main =
    println "Hello from Sky!"
```

```bash
sky init hello && cd hello && sky run src/Main.sky
```

## Why Sky

- **If it compiles, it works.** Every side effect returns
  `Task Error a`; every fallible value returns `Result Error a`;
  `sky check` invokes `go build` on the emitted Go so any shape
  mismatch surfaces at type-check time. There is no runtime null,
  no uncaught exception, no silent numeric coercion.
- **One language, every shape.** The same `init / update / view /
  subscriptions` source compiles to a server-rendered web app
  (Sky.Live), a terminal UI (Sky.Tui), or a native desktop window
  (Sky.Webview).
- **Batteries included.** Auth, database, HTTP client + server,
  WebSocket, JSON, JWT, CSV, email, encryption, observability —
  every primitive a real app needs is in the stdlib (`Std.Db`,
  `Std.Auth`, `Std.Ui`, `Std.Cache`, `Std.Email`, …) and
  documented with `sky doc --serve`.
- **Go's whole ecosystem.** `sky add github.com/some/package` —
  the compiler introspects the Go package and generates strict,
  typed Sky bindings. No hand-written FFI glue. Stripe SDK
  (~76k FFI symbols) compiles and tree-shakes to a 4k-line
  `main.go`.
- **AI-friendly by design.** Explicit annotations, exhaustive
  pattern matching, no implicit coercions, no exceptions. LLMs
  generate code that compiles the first time. The shipped
  `CLAUDE.md` and `sky init`'s starter `CLAUDE.md` give any
  AI assistant the load-bearing context to scaffold production
  apps directly.
- **One binary out the back.** Every project compiles to a
  static Go binary. Deploy with `scp`, with Docker, or as a CLI
  you `brew install`.

## Why the compiler is in Rust

The Haskell compiler carried Sky through v0.17. The push to v1 — "if it
compiles, it works" end to end, on an architecture that stays
maintainable — surfaced limits in it that were structural rather than
incidental: a monolithic multi-thousand-line lowering pass, mutable
`IORef` compiler state that fought the very purity Sky promises its
users, and an HM solver that needed hard memory budgets to stay bounded.
The rewrite moves the compiler to a Rust cargo
workspace of small, single-responsibility crates — lexer/parser, name
resolution, HM inference, type-directed lowering, Go codegen, FFI,
formatter, LSP — so each architectural decision sits behind a real
module boundary instead of inside one file, and a query-DAG core makes
incremental rebuilds and cross-module analysis robust by construction.
Rust earns its place specifically: it compiles the corpus faster and
with a lower, more predictable memory profile, and its algebraic enums,
enforced exhaustiveness, and absence of null mirror the discipline Sky
itself enforces — the compiler is now written in the same style it
compiles. The typed-Go output and the "if it compiles, it works"
contract carry over unchanged; every one of v0.17's hard-won learnings
is now a crate boundary, a gate, or a test, which is what makes the v1
goals reachable. The retired Haskell compiler stays under
[`legacy-haskell-compiler/`](legacy-haskell-compiler/) as a
byte-for-byte differential oracle until v1 is tagged.

## Hello, Sky

A counter web app — type-checked, server-driven, no JavaScript.

```elm
module Main exposing (main)

import Std.Cmd as Cmd
import Std.Live exposing (app, config, route)
import Std.Sub as Sub
import Std.Ui as Ui
import Std.Ui.Font as Font


type Msg
    = Increment
    | Decrement


type alias Model = { count : Int }


init : a -> ( Model, Cmd Msg )
init _ = ( { count = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Increment -> ( { model | count = model.count + 1 }, Cmd.none )
        Decrement -> ( { model | count = model.count - 1 }, Cmd.none )


view : Model -> Ui.Element Msg
view model =
    Ui.layout []
        (Ui.row [ Ui.spacing 16, Ui.padding 24 ]
            [ Ui.button [] { onPress = Just Decrement, label = Ui.text "−" }
            , Ui.el [ Font.size 24 ] (Ui.text (String.fromInt model.count))
            , Ui.button [] { onPress = Just Increment, label = Ui.text "+" }
            ])


main =
    app
        (config
            { init = init
            , update = update
            , view = view
            , subscriptions = \_ -> Sub.none
            , routes = [ route "/" () ]
            , notFound = ()
            }
        )
```

```bash
sky run src/Main.sky    # http://localhost:8000
```

Add `Std.Tui.app cfg` to ship the same `view` to a terminal
canvas, or `Std.Webview.app cfg` for a native desktop window.

## Install

```bash
# macOS / Linux — single-binary install
curl -fsSL https://raw.githubusercontent.com/anzellai/sky/main/install.sh | sh

# or build from source (Rust toolchain required to build the compiler)
git clone https://github.com/anzellai/sky
cd sky/rust && cargo build --release -p sky
# or install straight to ~/.local/bin:
#   cargo install --path rust/crates/sky --root ~/.local --locked
```

The `sky` binary embeds the runtime, stdlib, and Sky Console.
End users only need `sky` on PATH and Go 1.21+ available for
codegen.

## Pick your shape

Match the application to the right surface — every shape uses the
same TEA-style `init / update / view / subscriptions`.

| What you're building                    | Surface           | Entry point                     | Default deployment   |
|-----------------------------------------|-------------------|---------------------------------|----------------------|
| Web app (server-driven, real-time)      | **Sky.Live**      | `Live.app (Live.config {…})`    | Cloud Run / VM       |
| HTTP / JSON API (no UI)                 | **Sky.Http.Server** | `Server.listen 8000 [...]`    | Cloud Run / VM     |
| Terminal UI (TUI)                       | **Sky.Tui**       | `Tui.app (Tui.config {…})`      | `brew install` / CLI |
| CLI tool (no UI loop)                   | **Sky.Cli**       | `main = Task.run ...`           | `brew install`       |
| Native desktop app                      | **Sky.Webview**   | `Webview.app { … }`             | `.app` / `.exe`      |

> ### ⚠ Upgrading from v0.18 → v0.19? `Live.app` is a breaking change
>
> The TEA app config is now a **typed builder**, not a row-open record literal:
>
> ```elm
> -- v0.18 (old)                          -- v0.19 (new)
> Live.app { init = …, update = …,        Live.app
>            view = …, subscriptions = …,      (Live.config { init = …, update = …
>            routes = [...], notFound = …,                    , view = …, subscriptions = …
>            head = headFor }                                 , routes = [...], notFound = … }
>                                                  |> Live.withHead headFor)
> ```
>
> The six required fields go in `Live.config { … }`; optional fields
> (`head` / `guard` / `analytics` / …) attach with `|> withX`. Same for
> `Tui.app` / `Tui.program` / `Cli.program`; `Webview.app` is unchanged. Raw
> `api` endpoints are now `Request -> Task Error Response` (record request, Task
> return) and live in the `routes` list. Full mechanical guide:
> [`docs/v0.19/migration-builder-cfg.md`](docs/v0.19/migration-builder-cfg.md).

Every backend shares `Std.Ui` for layout, `Std.Auth` for sessions,
`Std.Db` for persistence, `Std.Log` / `Std.Trace` for
observability, and `Sky.Core.*` for pure primitives.

## What ships with Sky

A short tour. Full reference at `sky doc --serve` or
[docs/stdlib.md](docs/stdlib.md).

| Module                 | What it gives you                                                                 |
|------------------------|-----------------------------------------------------------------------------------|
| `Std.Ui`               | Typed no-CSS layout DSL (`row`/`column`/`el`/`button`/`input` + `Background`/`Border`/`Font`/`Region` subs). Renders to inline-styled HTML, ANSI cells, or native Webview from the same source. |
| `Std.Live`             | Sky.Live runtime — TEA app + SSE patches + session stores (memory / sqlite / redis / postgres / firestore) + routing + cookies + auth gates. |
| `Sky.Http.Server`      | HTTP server with typed routes, middleware (CORS / logging / rate-limit / basic-auth), streaming responses, WebSocket upgrade. |
| `Std.Auth`             | bcrypt password hashing, HS256 / RS256 JWT, register / login / roles. Typed secrets — never `fmt.Sprintf("%v", token)`. |
| `Std.Db`               | SQLite + PostgreSQL via one interface. Connection pool, prepared statements, versioned migrations, `Db.RowDecoder`, `withTransaction`. |
| `Std.Db.Schema`        | Typed, dialect-safe schema DSL — define tables as values; `createTable` emits the correct `CREATE TABLE` for SQLite **and** Postgres from one definition (no `INTEGER`-overflow / `AUTOINCREMENT`-vs-`BIGSERIAL` drift). |
| `Std.Money` + `Std.Decimal` | Arbitrary-precision Decimal + currency-typed Money (50+ ISO 4217 codes + crypto) with `allocate` for fair splits and conversion rates. |
| `Std.Cache`            | LRU + TTL in-memory cache, parametric on key + value, monotone stats. |
| `Std.Analytics`        | Typed product analytics — typed event props (`Money` lossless, `Pii` redactable), consent-gated + anonymous by default, opt-in Sky.Live auto page-views, SQLite store, and a Sky Console **Analytics** tab (counts / recent / revenue-by-currency). |
| `Std.Email`            | Resend / SES / SendGrid / SMTP under one typed `EmailProvider`. `SKY_EMAIL_DRY_RUN=1` for tests. |
| `Std.Compression` / `Std.Csv` / `Std.Config` | gzip / zstd; RFC 4180 CSV; TOML / YAML / JSON decoders that mirror `Sky.Core.Json.Decode`. |
| `Sky.Core.WebSocket`   | Client + server bidirectional sockets. |
| `Sky.Core.Crypto`      | SHA-256 / 512, HMAC, RSA sign/verify, AES-GCM, ChaCha20, scrypt password derivation, AEAD constants. |
| `Std.Webview`          | Native desktop window (macOS in v0.1; Linux / Windows in v0.2). |

## Observability — built in

Every Sky.Live and Sky.Http.Server app auto-mounts:

- `/_sky/console` — Std.Ui dashboard with overview, logs,
  metrics, traces, errors (production-gated via
  `SKY_CONSOLE_AUTH`).
- `/_sky/metrics` — Prometheus scrape endpoint
  (`sky_live_requests_total{route,status}`, latency histograms,
  drop counters).
- `/_sky/healthz` / `/_sky/readyz` — liveness + readiness probes.
- `/_sky/buildinfo` — commit, build timestamp, Sky version.

Run **`sky console serve`** to stand up a central hub that
multiple Sky apps push telemetry to via the `HubExporter`
(OTLP/HTTP). See [docs/v0.16.x-console/HUB.md](docs/v0.16.x-console/HUB.md)
for the multi-service dashboard, tenant isolation, and the
3-layer auth defense-in-depth model.

`OTEL_EXPORTER_OTLP_ENDPOINT` is honoured for the standard
OpenTelemetry collector — point at Honeycomb, Grafana Tempo,
Datadog, etc.

## Going to production

```toml
# sky.toml
name = "myapp"
version = "1.0.0"
entry = "src/Main.sky"

[live]
port = 8000
store = "sqlite"          # memory / sqlite / redis / postgres / firestore
storePath = "sessions.db"
ttl = "30m"

[database]
driver = "sqlite"         # sqlite / postgres
url = "DATABASE_URL"

[auth]
cookie = "sky_sid"
ttl = "24h"
# tokenSecret read from SKY_AUTH_TOKEN_SECRET (never put secrets in sky.toml)

[log]
format = "json"           # plain / json
level  = "info"
```

```bash
ENV=production \
SKY_AUTH_TOKEN_SECRET="$(openssl rand -base64 48)" \
SKY_CONSOLE_AUTH=app SKY_CONSOLE_TOKEN="$(openssl rand -base64 48)" \
sky build src/Main.sky && ./sky-out/app
```

The production gate is `ENV` (then `SKY_ENV` fallback). Unset
or `dev` / `development` / `local` → dev mode. Anything else
locks down the dev console, banner, and metrics endpoint.

Deploy with `scp` + your favourite supervisor, drop the binary
into a Docker `FROM scratch` image, or run it directly on any
platform with a Go 1.22+ runtime.

## Documentation

- **[Getting started](docs/getting-started.md)** — install + your
  first app in 5 minutes.
- **[Stdlib reference](docs/stdlib.md)** — every module, every
  function, indexed by tier (Pure / Fallible-pure / Task /
  Diverging).
- **[Sky.Live](docs/skylive/overview.md)** — server-driven UI
  + SSE patches + sessions + routing.
- **[Std.Ui](docs/skyui/overview.md)** — typed no-CSS layout DSL.
- **[Sky.Tui](docs/skytui/overview.md)** — terminal backend for
  `Std.Ui`.
- **[Sky.Webview](docs/skywebview/overview.md)** — native desktop
  window.
- **[Std.Auth](docs/skyauth/overview.md)** — sessions + JWT + roles.
- **[Std.Db](docs/skydb/overview.md)** — SQLite + PostgreSQL.
- **[`sky.toml`](docs/sky-toml.md)** — every config key.
- **[CLI](docs/tooling/cli.md) / [LSP](docs/tooling/lsp.md) /
  [Testing](docs/tooling/testing.md)**.
- **[Known limitations](docs/KNOWN_LIMITATIONS.md)** —
  current-version constraints + workarounds.
- **[Compiler journey](docs/compiler/journey.md)** — how Sky got
  here (historical context, kept for contributors).

## Examples

~50 examples ship in [`examples/`](examples/). Each builds clean
from a wiped slate (`rm -rf sky-out .skycache .skydeps && sky build`).

| Range  | Category                              |
|--------|---------------------------------------|
| 01-08  | Hello / CLI / Go-FFI / file / system  |
| 09-12  | Sky.Cli / Sky.Tui counters & TODOs    |
| 13     | Stripe-SDK-scale FFI benchmark (76k symbols) |
| 14-25  | Sky.Live + Sky.Http.Server apps       |
| 26     | `examples/26-ui-showcase` — every Std.Ui primitive |
| 29-31  | Sky.Webview + WebGL spike             |
| 32-33  | SSE relay + WebSocket echo            |
| 34-38  | Multi-tier + composite-test apps      |
| 39     | Two Sky.Live apps → one hub (v0.16.6) |

## Contributing

Issues and PRs welcome at
[github.com/anzellai/sky](https://github.com/anzellai/sky). The
[Rust compiler architecture](docs/rust-rewrite/) write-up is the
right starting point for compiler work (the Haskell-era
[docs/compiler/](docs/compiler/) notes are kept as historical
reference). Run `cargo test --workspace` plus the xtask gate
suite (`cargo run -p xtask -- <gate>`) before any PR;
`scripts/example-sweep.sh` validates every example builds.

## Licence

[Apache 2.0](LICENSE) — © 2025–2026 Anzel Lai. Includes patent grant +
trademark clause. Prior-art attribution for derivative files (parts of
the type-inference core adapted from elm/compiler under BSD-3-Clause)
lives in [NOTICE.md](NOTICE.md). Contributions accepted under the same
Apache 2.0 terms — see [CONTRIBUTING.md](CONTRIBUTING.md).

> Sky was previously distributed under the MIT licence (releases up to
> and including v0.10.0). Those releases remain available under their
> original MIT terms; v0.10.1 onwards ships under Apache 2.0.
