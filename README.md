# Sky

[sky-lang.org](https://sky-lang.org) · [Examples](examples/) · [Docs](docs/)

> **Experimental** — Sky is an opinionated, AI-friendly programming language under active development. APIs and internals may change between minor versions.

Sky is a fullstack programming language that combines **Go's pragmatism** with the **elegance of pure-functional, ML-family languages**. Write functional, strongly-typed code with a batteries-included stdlib — `Sky.Live` for server-driven UI, `Sky.Tui` for terminal UI (sharing the same `Std.Ui` code), `Std.Db` for SQL persistence, `Std.Auth` for sessions, `Sky.Core.Error` for unified error handling — import any Go package with auto-generated FFI bindings (no hand-written glue), and ship a single portable binary. Sky's explicit types, exhaustive pattern matching, and strict `Task` effect boundary make it **AI-friendly by design**: both humans and LLMs tend to write code that compiles the first time.

```elm
module Main exposing (main)

import Std.Log exposing (println)

main =
    println "Hello from Sky!"
```

## Current state (v0.15.x)

- **Type-directed lowering throughout (v0.15).** Sub-expressions at
  lambda bodies, record-field inits, list elements, and call args
  lower with the slot's typed Go form propagated. Closes the long-
  standing parametric-record-alias bug class (callback fields keep
  their typed callee parameter; cross-alias passing works without
  the alias-chain workaround; inline lambdas in record fields keep
  their typed shape). Architecture write-up:
  [docs/v1-rfc/type-soundness-deep-analysis.md](docs/v1-rfc/type-soundness-deep-analysis.md).
- **Go generics on parametric record aliases (v0.15).** A
  `type alias Cfg msg = { onSubmit : msg, label : String, ... }` now
  emits `type Cfg_R[T1 any] struct { OnSubmit T1; Label string; ... }`
  with per-instance type args. Stripe-SDK-scale benchmark
  (`examples/13-skyshop`, 76 k FFI symbols) still tree-shakes
  `main.go` 14 k → 4 k lines (−71 %) and `stripe_bindings.go`
  326 k → 58 k lines (−82 %).
- **Same-module polymorphic re-instantiation (v0.15).** Annotated
  `f : Cfg msg -> msg` called with `msg=Int` AND `msg=Bool` in the
  same module both work — sibling references alpha-rename per call
  site. Wildcard-`any` sigs stay on the shared-env path so body ↔
  caller unification chains keep soundness.
- **Layer 3 stdlib — every kernel module is Sky source** (carried
  forward from v0.14). Browse via
  `sky-stdlib/{Sky/Core,Std,Sky/Http}/*.sky`, or `sky doc --serve`
  for a browsable HTTP doc server with type-signature search
  (Hoogle-style), in-module symbol filter, and Markdown rendering.
- **Auto-TCO.** Every Sky function with tail-position self-recursion
  compiles to a `for { ... continue }` Go loop. Constant Go stack
  regardless of input size. Applies to user code, not just stdlib.
- **Sky Console + sub-app mount + observability federation.** Every
  Sky.Live / Sky.Http.Server app auto-mounts a Std.Ui dashboard at
  `/_sky/console` in dev mode. Prometheus metrics at `/_sky/metrics`
  (Bearer-gated in production). `rt.MountSubApp` hosts any Sky binary
  (or any HTTP server) under any URL prefix; logs / metrics / spans
  push back to the parent for one-scrape observability across the
  tree.
- **LSP, dev tooling.** Hover + goto-def for every USED symbol class.
  `sky watch` (file-watch rebuild + restart with sticky-on-error
  policy), `sky doctor` (project + env health checks), `sky console`
  (standalone Std.Ui dashboard — Live or Tui backend).
- **27 example projects** covering CLI, Sky.Tui, Sky.Live + Sky.Http
  apps, databases (SQLite / PostgreSQL / Firestore), payments
  (Stripe), auth, GUI (Fyne), a Reddit/HN-style forum, and a
  visual-regression Std.Ui showcase.

## What Sky brings together

- **Go compilation target** — fast builds, single static binary,
  access to the full Go ecosystem (databases, HTTP servers, cloud
  SDKs).
- **Pure-functional ML-family front-end** — Hindley-Milner type
  inference, algebraic data types, exhaustive pattern matching, pure
  functions, model/update/view/subscriptions architecture (TEA).
- **Server-driven UI** — DOM diffing, SSE subscriptions, session
  management on the server. No client-side framework. (Same
  architectural style popularised by Phoenix LiveView; design +
  implementation independent.)

Sky compiles to Go. One binary runs your API, DB access, and
server-rendered interactive UI — one codebase, one language, one
deployment artifact.

> Sky's surface syntax is deliberately compatible with the Elm language
> (BSD-3-Clause, © Evan Czaplicki and contributors) and several files
> in the type-inference core are derivative works adapted from
> elm/compiler. Full attribution + licence text in [NOTICE.md](NOTICE.md).

## Implementation

The compiler is in **Haskell** (GHC 9.4+). Single `sky` binary. Runtime
in Go (`runtime-go/rt/`), embedded into the binary via Template
Haskell — no separate install. See
[docs/compiler/journey.md](docs/compiler/journey.md) for the TS → Go →
self-hosted Sky → Haskell history.

## What's in the box

Six killer modules cover the common needs of any modern web app — no
plugins, no separate services, no `npm install`.

### Sky.Live — server-driven UI

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

Full TEA loop (`init / update / view / subscriptions`), async work via
`Cmd.perform`, persistent sessions across deploys (memory / SQLite /
Redis / Postgres / Firestore), input-authority protocol that protects
the user's typed value across re-renders, reverse-proxy-hardened SSE
with auto-reconnect + retry queue. See
[Sky.Live overview](docs/skylive/overview.md).

### Std.Ui — typed no-CSS layout DSL

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

Build a UI from typed primitives (`row`, `column`, `el`, `paragraph`,
`textColumn`, `link`, `image`, `button`, `input`, `form`, `html`) and
typed attributes from focused sub-modules (`Background.color`,
`Border.rounded`, `Font.size`, `Region.heading`, …). Renders to
inline-styled HTML with semantic tags dispatched from `Region.*`
(`<h1..h6>`, `<main>`, `<nav>`, `<aside>`, `<footer>`). Forms with the
password best-practice pattern (`Ui.form` + `Ui.onSubmit` decoding
formData into a typed record — secret never enters Model). File /
image upload with browser-side resize hints. Same source code runs in
both `Sky.Live` (browser) and `Sky.Tui` (terminal — see below). See
[Sky.Ui overview](docs/skyui/overview.md). Prior-art attribution:
[NOTICE.md](NOTICE.md).

### Sky.Tui — terminal UI with the same Std.Ui code

```elm
-- shared.sky — both Live and Tui share this view + update
view model =
    Ui.column [ Ui.spacing 8, Ui.padding 16 ]
        [ Ui.el [ Font.bold, Font.size 24 ] (Ui.text (String.fromInt model.count))
        , Ui.row [ Ui.spacing 4 ]
            [ Ui.button [] { onPress = Just Decrement, label = Ui.text "−" }
            , Ui.button [] { onPress = Just Increment, label = Ui.text "+" }
            ]
        ]
```

```elm
-- Main.sky (web)             -- MainTui.sky (terminal)
main = Live.app cfg            main = Tui.app cfg |> Task.run
```

Same `update` semantics, same `view` widgets, two completely different
output targets. Sky.Tui handles bracketed paste, wide chars (CJK +
emoji + ZWJ), focus rings, scroll wheel, mouse press, viewport pixel
canvas (1280×720 logical px maps to cells), resize via SIGWINCH. See
[Sky.Tui overview](docs/skytui/overview.md) and
[examples/22-tui-stopwatch-ui](examples/22-tui-stopwatch-ui) for a
stopwatch in <100 lines that runs in both backends.

### Std.Auth — authentication, in the box

```elm
Auth.register db "alice@example.com" password
    |> Task.andThenResult (\uid ->
        Auth.signToken secret (Dict.fromList [("sub", String.fromInt uid)]) 86400)
```

bcrypt password hashing, HMAC-SHA256 JWTs, plus optional DB-backed
`register` / `login` / `setRole`. Minimum-32-byte secret enforcement,
constant-time compare, configurable cost. See
[Std.Auth overview](docs/skyauth/overview.md).

### Std.Db — one API for SQLite + PostgreSQL

```elm
Db.withTransaction db (\tx ->
    Db.exec tx "UPDATE accounts SET balance = balance - ? WHERE id = ?" [amount, fromId]
        |> Task.andThen (\_ ->
            Db.exec tx "UPDATE accounts SET balance = balance + ? WHERE id = ?" [amount, toId]))
```

Parameter-safe queries, transactions, conventional CRUD helpers
(`insertRow` / `getById` / `updateById` / `deleteById` /
`findOneByField` / `findManyByField` / `findByConditions`). Switch
driver in `sky.toml`; never touch it again in your code. See
[Std.Db overview](docs/skydb/overview.md).

### Sky Console + observability + sub-app mount

Every Sky.Live / Sky.Http.Server app ships with:

| Surface | What it is |
|---|---|
| `🔍 Console` link | Floating bottom-right anchor injected into every dev-mode page. Same-origin link to `/_sky/console`. |
| `/_sky/console` | Bundled Std.Ui dashboard reverse-proxied behind your app. Tabs: Overview · Metrics · Logs · Traces · Errors. Auto-aggregates everything from your app + every mounted sub-app. |
| `/_sky/metrics` | Prometheus scrape endpoint (Bearer-gated in production). `sky_live_requests_total{route,status}`, `sky_live_request_seconds`, custom counters via `rt.RecordCounter`. |
| `/_sky/healthz` / `/_sky/readyz` / `/_sky/buildinfo` | k8s / Cloud Run probes + build metadata. |
| Structured logs | `Log.info` / `.warn` / `.error` / `.infoWith` with level + message + request-correlation ID; HTTP access log automatic. |
| Trace spans | Every HTTP request opens a span; `rt.RecordTrace` adds children. Exports to OpenTelemetry if `OTEL_EXPORTER_OTLP_ENDPOINT` is set. |

**Sub-app mount** — host multiple Sky apps under one binary, with
federated observability:

```go
// Inside your parent Sky app's generated main.go
rt.MountSubApp(mux, "/billing", rt.SpawnBinary("./billing-app"))
rt.MountSubApp(mux, "/admin",   rt.SpawnBinary("./admin-app"))
rt.MountSubApp(mux, "/docs",    rt.SpawnBinary("./hugo-server"))
```

Each sub-app runs as its own child process — own session store, own
update loop, own cookies, zero shared state. The reverse proxy gives
the user a single port and origin. **Observability federates
automatically**: every log / metric / span the child emits gets pushed
back to the parent labelled `subapp="billing"`, so one Prometheus
scrape on the parent covers the whole tree.

```bash
sky run                      # dev — console, banner, logs/metrics on
ENV=production sky-out/app   # prod — console gone, /_sky/metrics behind Bearer auth
```

### Std.Decimal + Std.Money + Std.Time — production-grade arithmetic + time

```elm
-- Exact arithmetic — 0.1 + 0.2 = 0.3 genuinely
total = Dec.add (Dec.fromString "0.1" |> okOr Dec.zero)
                (Dec.fromString "0.2" |> okOr Dec.zero)

-- Currency-typed Money — USD vs JPY rejected at compile time
subTotal = Money.fromMajor Money.USD 100
tax      = Money.percentOf (Dec.fromString "8.875" |> okOr Dec.zero) subTotal
total    = Money.add subTotal tax       -- "$108.88"

-- Fair-split invoice — sums to $100 exactly
parts = Money.allocate 3 (Money.fromMajor Money.USD 100)
        -- → [$33.34, $33.33, $33.33]

-- Timezone-aware (no /usr/share/zoneinfo needed; embedded tzdata)
nextMonth = Stime.addMonths 1 today    -- Jan 31 + 1 → Feb 28/29 clamped
```

`Decimal` backed by `shopspring/decimal`; `Money` enforces currency-
match at the type level; `Std.Time` ships embedded `time/tzdata`. ISO
4217 enum covers 50+ codes + crypto (BTC, ETH, USDT, USDC). Full
surface: [Standard library reference](docs/stdlib.md).

## Quick start

```bash
# macOS / Linux — single-binary install
curl -fsSL https://raw.githubusercontent.com/anzellai/sky/main/install.sh | sh

# or with Docker
docker run --rm -v $(pwd):/app -w /app anzel/sky sky --help
```

> **Prerequisite:** [Go](https://go.dev) 1.21+ — Sky compiles to Go.

```bash
sky init hello
cd hello
sky run src/Main.sky                  # build + run
sky watch src/Main.sky                # rebuild + restart on save
sky doc --serve                       # browsable API docs (any browser)
sky doctor                            # health checks
```

See [docs/getting-started.md](docs/getting-started.md) for a
walkthrough.

## Going to production

Two things flip Sky from dev to production: a config block in
`sky.toml` and a small set of env vars. Both read at process start —
no rebuild needed.

### `sky.toml` (compiled defaults — checked into your repo)

```toml
[live]
port         = 8000         # default if SKY_LIVE_PORT not set
store        = "postgres"   # memory | sqlite | redis | postgres | firestore
ttl          = "24h"
maxBodyBytes = 5242880      # 5 MiB cap on /_sky/event POST

[log]
format = "json"             # plain (dev default) | json (prod default)
level  = "info"

[auth]
tokenTtl       = "24h"
cookie         = "sky_sid"
# tokenSecret read from SKY_AUTH_TOKEN_SECRET (never put secrets in sky.toml)
```

### `.env` / process env (deploy-time secrets + per-env overrides)

```dotenv
ENV=production              # gates dev console + banner OFF; /_sky/metrics behind auth
SKY_LIVE_PORT=8080          # or honour PORT (Cloud Run / Fly / Heroku)
SKY_LIVE_STORE=postgres
DATABASE_URL=postgres://…   # fallback when SKY_LIVE_STORE_PATH unset
SKY_AUTH_TOKEN_SECRET=…     # ≥32 bytes; Sky errors at startup if shorter
SKY_LOG_FORMAT=json
SKY_LOG_LEVEL=info
SKY_ADMIN_TOKEN=…           # /_sky/metrics + /_sky/console require Bearer in prod
                            # (legacy: SKY_METRICS_TOKEN / SKY_CONSOLE_TOKEN_SECRET still honoured)

# v0.16.0 — console auth (BREAKING in production):
SKY_CONSOLE_AUTH=token      # token | app | off — in production, UNSET = FATAL EXIT at boot
SKY_CONSOLE_TOKEN=…         # 32-byte hex; required when SKY_CONSOLE_AUTH=token

# v0.16.1+ — HubExporter (push telemetry to a remote console hub)
SKY_CONSOLE_HUB=…           # https://… OTLP HTTP+protobuf endpoint
SKY_CONSOLE_HUB_TOKEN=…     # ≥32-byte bearer
SKY_CONSOLE_SPOOL_MODE=auto # auto | file | memory (auto picks memory on serverless)

# v0.16.1+ — OTel export is HTTP/protobuf ONLY (no gRPC dep, smaller binary):
# OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318  # MUST be HTTP port (not gRPC 4317)
# OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf               # 'grpc' triggers boot warning + disables export
```

Precedence: **process env > `.env` > `sky.toml`**. The `.env` file is
auto-loaded but never overrides real env vars — so a `docker run -e
ENV=production` always wins.

The `productionFromEnv()` gate (`ENV` then `SKY_ENV`, anything outside
`{dev, development, local}` counts as production) governs three things:
dev console mount, `🔍 Console` banner, `/_sky/metrics` auth. One env
var, one switch.

Reference: [Sky.Live overview — env precedence](docs/skylive/overview.md#environment-variable-precedence).

### Building from source

```bash
# easiest path on any system with nix
nix develop                # GHC 9.4.8 + Go + system deps, sandboxed
./scripts/build.sh --clean
```

See [docs/development.md](docs/development.md) for the full build +
test story (pinned toolchain, reproducible Nix builds, contributor
guide).

## Documentation

| Area | Link |
|---|---|
| Getting started | [docs/getting-started.md](docs/getting-started.md) |
| `sky.toml` reference | [docs/sky-toml.md](docs/sky-toml.md) |
| Language syntax | [docs/language/syntax.md](docs/language/syntax.md) |
| Types | [docs/language/types.md](docs/language/types.md) |
| Pattern matching | [docs/language/pattern-matching.md](docs/language/pattern-matching.md) |
| Modules | [docs/language/modules.md](docs/language/modules.md) |
| Go FFI interop | [docs/ffi/go-interop.md](docs/ffi/go-interop.md) |
| FFI design | [docs/ffi/ffi-design.md](docs/ffi/ffi-design.md) |
| Error system | [docs/errors/error-system.md](docs/errors/error-system.md) |
| **Standard library reference** | [docs/stdlib.md](docs/stdlib.md) |
| **Std.Auth overview** | [docs/skyauth/overview.md](docs/skyauth/overview.md) |
| **Std.Db overview** | [docs/skydb/overview.md](docs/skydb/overview.md) |
| Sky.Live overview | [docs/skylive/overview.md](docs/skylive/overview.md) |
| Sky.Live architecture | [docs/skylive/architecture.md](docs/skylive/architecture.md) |
| Std.Ui overview | [docs/skyui/overview.md](docs/skyui/overview.md) |
| Sky.Tui overview | [docs/skytui/overview.md](docs/skytui/overview.md) |
| Compiler architecture | [docs/compiler/architecture.md](docs/compiler/architecture.md) |
| Compiler pipeline | [docs/compiler/pipeline.md](docs/compiler/pipeline.md) |
| Compiler journey (TS → Go → Sky → Haskell) | [docs/compiler/journey.md](docs/compiler/journey.md) |
| CLI reference | [docs/tooling/cli.md](docs/tooling/cli.md) |
| Testing (`sky test`) | [docs/tooling/testing.md](docs/tooling/testing.md) |
| LSP | [docs/tooling/lsp.md](docs/tooling/lsp.md) |
| Known limitations | [docs/KNOWN_LIMITATIONS.md](docs/KNOWN_LIMITATIONS.md) |
| Development & contributing | [docs/development.md](docs/development.md) |

## Status

- **Core principle: "if it compiles, it works."** Every known runtime
  panic class has a regression test in `runtime-go/rt/*_test.go` or
  `test/Sky/**Spec.hs`. Defence in depth (panic recovery + `Err`-
  return at Task boundaries) is the floor.
- **27 example projects** under `examples/` — clean build from a wiped
  slate is a release gate.
- **`sky verify`** is the canonical runtime check: builds AND runs each
  example, hits HTTP endpoints, honours per-example `verify.json`
  scenarios.
- **Test matrix** — 306 cabal hspec specs + 25+ runtime Go tests +
  70-file `test-files/*.sky` self-test loop + format idempotency +
  Playwright browser sweep across every Sky.Live / Sky.Http.Server
  example.
- **FFI generation** — Stripe SDK (8 896 types), Firestore, Fyne, and
  others auto-bind.

## Contributing

Issues + PRs welcome. See the docs tree for architecture context
before opening a structural PR.

## Licence

[Apache 2.0](LICENSE) — © 2025–2026 Anzel Lai. Includes patent grant +
trademark clause. Prior-art attribution for derivative files (parts of
the type-inference core adapted from elm/compiler under BSD-3-Clause)
lives in [NOTICE.md](NOTICE.md). Contributions accepted under the same
Apache 2.0 terms — see [CONTRIBUTING.md](CONTRIBUTING.md).

> Sky was previously distributed under the MIT licence (releases up to
> and including v0.10.0). Those releases remain available under their
> original MIT terms; v0.10.1 onwards ships under Apache 2.0.
