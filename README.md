# Sky

[sky-lang.org](https://sky-lang.org) · [Examples](examples/) · [Docs](docs/)

> **Experimental · v0.13** — Sky is under active development. APIs and internals may change between minor versions.

Sky is an experimental fullstack programming language that combines **Go's pragmatism** with the **elegance of pure-functional, ML-family languages**. You write functional, strongly-typed code with a batteries-included stdlib — `Sky.Live` for server-driven UI, `Sky.Tui` for terminal UI (sharing the same Std.Ui code), `Std.Db` for SQL persistence, `Std.Auth` for sessions, `Sky.Core.Error` for unified error handling — import any Go package with auto-generated FFI bindings (no hand-written glue), and ship a single portable binary. Sky's explicit types, exhaustive pattern matching, and strict `Task` effect boundary make it **AI-friendly by design**: both humans and LLMs tend to write code that compiles the first time.

### What's new in v0.13

- **Fully-typed Go output, end-to-end.** Every USED Sky symbol — vars, functions, lambdas, ADTs, records — emits a typed Go signature. No bare `any` for used code. Only genuinely-generic (`[T any]`) or genuinely-unused declarations may use `any` (and DCE prunes the latter). See workstreams A–G in `CLAUDE.md`.
- **Whole-program Sky DCE.** Pre-lowering dead-code elimination in the Haskell phase. Stripe-SDK-scale measurement on `examples/13-skyshop`: `main.go` 14 k → 4 k lines (−71 %); `stripe_bindings.go` 326 k → 58 k lines (−82 %); FFI type-alias bloat 80 847 → 29.
- **LSP 100 %.** Hover + goto-definition for every USED symbol class (function, type alias, ADT ctor, record-field, kernel call, lambda param, let-binding, case-pattern binder). 17 cabal-fenced end-to-end tests through real Neovim via headless driver — plus huge-FFI coverage on skyshop's 76 141-symbol Stripe catalogue.
- **Runtime hardening.** Map→struct narrowing in `rt.Coerce[T]` closes the Db.query → typed-record panic class. Reflect-adapter arg narrowing closes the `[]map[string]any` → `map[string]string` typed-callback class. `rt.AsMapAny` widener closes the symmetric `map[string]string` → `map[string]any` polymorphic-callee class. Tuple dispatch fast-path (~40 % faster per TEA update). Static-dir favicon serving from root so browser auto-requests don't 404. All fixes have regression specs in `runtime-go/rt/*_test.go`.
- **Full end-to-end verification.** All 25 examples build clean from a wiped state; Sky.Live + Sky.Http.Server apps drive sign-up / sign-in / CRUD / sign-out scenarios via Playwright with screen-recording artefacts (`scripts/verify-all-web.sh` with `SKY_RECORD=1`); Sky.Tui + Sky.Cli apps run panic-free.

### Post-v0.13 additions

- **Sky Console + sub-app mount + universal observability.** Every Sky.Live / Sky.Http.Server app auto-mounts a Std.Ui-written dev console at `/_sky/console` in dev mode — visit the path or click the injected "🔍 Console" floating link. The console is its own self-contained Sky.Live mini-app spawned as a child process and reverse-proxied behind your app: single port, same origin, zero shared state. **Same primitive (`rt.MountSubApp`) generalises**: mount any Sky binary (or any HTTP server) under any URL prefix to host billing widgets, admin panels, mini-apps under the parent's listener. Sub-apps push their logs / metrics / spans back to the parent's `/_sky/observability/ingest` labelled by namespace — one Prometheus scrape covers the whole tree. Also runs standalone via `sky console` (Sky.Live in the browser) or `sky console --tui` (Sky.Tui in the terminal — same source, different backend). See ["Sky Console + observability + sub-app mount"](#sky-console--observability--sub-app-mount--production-grade-out-of-the-box) below for what ships.
- **Production-mode gate** for dev-only features. `ENV` (or `SKY_ENV`) unset OR set to `dev` / `development` / `local` → dev mode (console + banner shown). Anything else (`production`, `prod`, `staging`, `qa`, `preview`, …) → console + banner hidden, `/_sky/metrics` gated behind Bearer auth. Single source of truth across the runtime — see ["Going to production"](#going-to-production) below.
- **HM merge bug fixed: entry-local lambda params no longer shadow dep top-levels.** `let xs = List.filter f model.logs` in a dep module had its result type silently mistyped as the entry module's lambda-param type (when a same-named param existed) — generating `rt.Coerce[WrongType](xs)` and panicking at runtime. `typesWithDeps` in `src/Sky/Build/Compile.hs` now detects entry+dep structural conflicts and falls back to safe any-routing. Regression fence: `test/Sky/Build/EntryLocalShadowsDepSpec.hs`.
- **Sub-app process-tree leak fixed.** `sky console` now installs SIGTERM / SIGHUP / SIGINT handlers that propagate to its `app-live` child; `MountSubApp` uses `cmd.Cancel = SIGTERM` + `WaitDelay` so each level of the tree gets a chance to tear down its own children before SIGKILL escalation; bundled console sets `SKY_CONSOLE_EMBED=off` for its own child so it doesn't recursively auto-mount yet another console under itself. Routine "http: proxy error: context canceled" log noise silenced via a custom `proxy.ErrorHandler`.

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

Sky is **batteries-included**. Five killer modules cover the common needs of any modern web app — no plugins, no separate services, no `npm install`:

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

### Sky.Tui — terminal UI with the same Std.Ui code

TEA in the terminal. Same `init / update / view / subscriptions` shape as Sky.Live. The `view` returns a `Std.Ui.Element msg` — the same `view` function runs in the browser via `Live.app` AND in the terminal via `Tui.app`. No code duplication.

```elm
-- shared.sky — both Live and Tui share this view + update
module App exposing (init, update, view, subscriptions)

import Std.Ui as Ui
import Std.Ui.Font as Font

type Msg = Increment | Decrement

init _ = ( { count = 0 }, Cmd.none )

update msg model =
    case msg of
        Increment -> ( { model | count = model.count + 1 }, Cmd.none )
        Decrement -> ( { model | count = model.count - 1 }, Cmd.none )

view model =
    Ui.column
        [ Ui.spacing 8, Ui.padding 16 ]
        [ Ui.el [ Font.bold, Font.size 24 ] (Ui.text (String.fromInt model.count))
        , Ui.row [ Ui.spacing 4 ]
            [ Ui.button [] { onPress = Just Decrement, label = Ui.text "−" }
            , Ui.button [] { onPress = Just Increment, label = Ui.text "+" }
            ]
        ]

subscriptions _ = Sub.none
```

Then run the same code on either backend:

```elm
-- Main.sky (web — serves at localhost:8000)
import Std.Live as Live
import App
main = Live.app
    { init = App.init, update = App.update, view = App.view
    , subscriptions = App.subscriptions
    , routes = [ Live.route "/" () ], notFound = ()
    }
```

```elm
-- MainTui.sky (terminal — renders ANSI cells)
import Std.Tui as Tui
import App
main =
    Tui.app
        { init = App.init, update = App.update, view = App.view
        , subscriptions = App.subscriptions
        , onKey = \k -> if k.value == "+" then App.Increment else App.Decrement
        }
        |> Task.run
```

Same `update` semantics, same `view` widgets, two completely different output targets. Sky.Tui handles bracketed paste, wide chars (CJK + emoji), focus rings, scroll wheel, mouse press, viewport pixel canvas (1280×720 logical px maps to cell sizes), and resize via SIGWINCH. See [Sky.Tui v1](docs/skytui/overview.md) and [examples/22-tui-stopwatch-ui](examples/22-tui-stopwatch-ui) for a stopwatch in <100 lines that runs in both backends.


Plus typed events (`onClick / onSubmit / onInput`), forms with the password best-practice pattern (`Ui.form` + `onSubmit DoSignIn` decoding wire formData into a typed record — secret never enters Model), and file/image upload with browser-side resize hints (`Ui.onImage AvatarSelected, Ui.fileMaxWidth 800`). See [Sky.Ui overview](docs/skyui/overview.md).

### Sky Console + observability + sub-app mount — production-grade out of the box

This is the big one: **every Sky.Live and Sky.Http.Server app ships with a built-in dev console, structured logging, Prometheus metrics, distributed tracing, and the ability to host any number of mini-apps under one binary** — no separate Grafana stack to stand up, no separate dashboard to wire, no separate process supervisor. The same `sky build` you'd write for a tiny demo gives you the operational surface you'd otherwise spend weeks bolting on.

**What you get the moment you run `sky run`:**

| Surface | What it is |
|---|---|
| `🔍 Console` floating link | Injected into every page in dev mode. Click → opens the in-binary dashboard. |
| `/_sky/console` | A bundled Std.Ui dashboard reverse-proxied behind your app. Tabs: Overview · Metrics · Logs · Traces · Errors. Auto-aggregates everything from your app + every sub-app you mount. |
| `/_sky/metrics` | Prometheus scrape endpoint (token-gated in prod). `sky_live_requests_total{route,status}`, `sky_live_request_seconds`, response-byte histograms, error counters, custom counters via `rt.RecordCounter`. |
| `/_sky/healthz` · `/_sky/readyz` | Liveness + readiness probes for k8s / Cloud Run / Fly / Render / Railway. |
| `/_sky/buildinfo` | Commit SHA, build timestamp, Sky version — useful in deploy diffs. |
| Structured logs | Every `Log.info / .warn / .error / .infoWith` carries level + message + request-correlation ID; HTTP access log is automatic ("GET / 200 (3ms)"); 4xx → warn, 5xx → error. |
| Trace spans | Every HTTP request opens a span; `rt.RecordTrace` adds child spans. Visible in the Traces tab + exported to OpenTelemetry if `OTEL_EXPORTER_OTLP_ENDPOINT` is set. |

```bash
sky run          # dev — console, banner, logs/metrics all on
ENV=production sky-out/app   # prod — console + banner gone, /_sky/metrics gated behind Bearer auth
```

**Sub-app mount — host multiple Sky apps under one binary, with federated observability:**

```go
// Inside your parent Sky app's generated main.go (one-line patch
// — a Sky-side `Live.app { subApps = [...] }` API is next):
import "your-app/rt"

rt.MountSubApp(mux, "/billing",  rt.SpawnBinary("./billing-app"))
rt.MountSubApp(mux, "/admin",    rt.SpawnBinary("./admin-app"))
rt.MountSubApp(mux, "/docs",     rt.SpawnBinary("./hugo-server"))
```

Each sub-app runs as its own child process — its own session store, its own update loop, its own session cookies, zero shared state. The reverse proxy gives the user a single port and a single origin. **Observability federates automatically**: every log / metric / span the child emits gets pushed back to the parent labelled `subapp="billing"`, so one Prometheus scrape on the parent's `/_sky/metrics` covers the whole tree. PromQL `sum by (subapp) (rate(sky_live_requests_total[1m]))` works without per-sub-app scrape jobs. The console's tabs read the same store — view all your sub-apps' traffic in one place.

**Why this matters** — typical "production-ready" web-app setup: pick a backend framework, pick a frontend framework, glue them, pick a logger, pick a metrics library, stand up Prometheus, stand up Grafana, stand up an OTel collector, wire a tracing library, write a dashboard, build an admin panel as a separate service, deploy four containers. Sky gives you the same operational surface with a single `sky build`. Read [Sky.Live overview — Dev console + sub-app mount](docs/skylive/overview.md#dev-console--auto-mounted-at-_skyconsole) for the full mechanism.

### Std.Decimal + Std.Money + Std.Time — production-grade arithmetic + time

For the things every real app gets wrong: floating-point money, banker's rounding, currency-typed arithmetic, IANA timezones.

```elm
import Std.Decimal as Dec
import Std.Money as Money exposing (Currency)
import Std.Time as Stime

-- Exact arithmetic — 0.1 + 0.2 is genuinely 0.3
total = Dec.add (Dec.fromString "0.1" |> okOr Dec.zero)
                (Dec.fromString "0.2" |> okOr Dec.zero)

-- Currency-typed Money — add USD to JPY at compile time, not at runtime
subTotal = Money.fromMajor Money.USD 100
tax      = Money.percentOf (Dec.fromString "8.875" |> okOr Dec.zero) subTotal
total    = Money.add subTotal tax       -- "$108.88"

-- Fair-split invoice
parts = Money.allocate 3 (Money.fromMajor Money.USD 100)
        -- → [$33.34, $33.33, $33.33] — sums to $100 exactly

-- Timezone-aware date arithmetic (no /usr/share/zoneinfo needed)
nextMonth = Stime.addMonths 1 today      -- Jan 31 + 1 → Feb 28/29 (clamped)
weekend   = Stime.isWeekend now
```

`Decimal` backed by `shopspring/decimal`; `Money` enforces currency-match at the type level; `Time` ships embedded `time/tzdata` so containers without `/usr/share/zoneinfo` still work. ISO 4217 currency enum covers 50+ codes (USD, EUR, GBP, JPY, CHF, …) plus crypto (BTC, ETH, USDT, USDC) plus `CurrencyRaw String` for the long tail. Full surface: [Standard library reference — `Std.Decimal` / `Std.Money` / `Std.Time`](docs/stdlib.md#stddecimal--arbitrary-precision-decimal-arithmetic).

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

### Going to production

Two things flip Sky from dev mode to production mode: a config block in `sky.toml` and a small set of env vars in your container / runner. Both are read at process start so they take effect on the next deploy — no rebuild needed.

#### `sky.toml` (compiled defaults — checked into your repo)

```toml
[live]
port        = 8000          # default if SKY_LIVE_PORT not set
store       = "postgres"    # memory | sqlite | redis | postgres | firestore
ttl         = "24h"         # session lifetime
maxBodyBytes = 5242880      # 5 MiB cap on /_sky/event POST (raise for file uploads)
# storePath = "postgres://…"  # explicit; otherwise picks up DATABASE_URL

[log]
format = "json"             # plain (dev default) | json (prod default)
level  = "info"             # debug | info | warn | error

[auth]
tokenTtl       = "24h"
cookie         = "sky_sid"
# tokenSecret read from SKY_AUTH_TOKEN_SECRET (never put secrets in sky.toml!)

[env]
# prefix = "MYAPP"          # namespace SKY_*_ env vars under MYAPP_* — only if you run
                            # multiple Sky binaries on the same host with colliding internal keys
```

Reference: [`sky.toml` full schema](docs/sky-toml.md).

#### `.env` (deploy-time secrets + per-environment overrides)

```dotenv
# ─── Sky operational ────────────────────────────────────────────────
ENV=production              # gates dev console + banner OFF; gates /_sky/metrics behind auth
SKY_LIVE_PORT=8080          # or honour the platform's PORT env (Cloud Run / Fly / Heroku)
SKY_LIVE_STORE=postgres     # overrides sky.toml [live] store
DATABASE_URL=postgres://…   # standard fallback Sky reads when SKY_LIVE_STORE_PATH unset

# ─── Logging ────────────────────────────────────────────────────────
SKY_LOG_FORMAT=json         # structured logs ship to stdout → your log aggregator picks up
SKY_LOG_LEVEL=info

# ─── Auth secrets ───────────────────────────────────────────────────
SKY_AUTH_TOKEN_SECRET=…     # ≥32 bytes; Sky errors at startup if shorter

# ─── Observability ──────────────────────────────────────────────────
SKY_METRICS_TOKEN=…         # /_sky/metrics requires `Authorization: Bearer <this>` in prod
# OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4318
                            # if set, traces are also exported to your OTel collector

# ─── Sub-app federation (if you use rt.MountSubApp) ─────────────────
# SKY_INGEST_TOKEN=…        # auto-generated per parent boot; override only if children
                            # run on different hosts
```

Sky reads env vars in priority order: **process env > `.env` file > `sky.toml`**. The `.env` file is auto-loaded at startup and never overrides real env vars — so a `docker run -e ENV=production` always wins over what's in `.env`. Production deployments override `.env` entries via the platform's secret store; `.env` itself is for local dev convenience and should not contain real secrets in git.

The `productionFromEnv()` gate (`ENV` then `SKY_ENV`, anything outside `{dev, development, local}` counts as production) governs **three** things simultaneously: dev console mount, `🔍 Console` banner, `/_sky/metrics` auth requirement. One env var, one switch, no chance of leaking a dev surface to prod.

Reference: [Sky.Live overview — env precedence + prefix](docs/skylive/overview.md#environment-variable-precedence).

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
| **`sky.toml` reference**             | [docs/sky-toml.md](docs/sky-toml.md)                                   |
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

- **v0.11.x — DX (`sky watch`), Sky.Live hot-reload story, install perf (2026-05-07).** New `sky watch` command runs a file-watch-driven rebuild + restart loop with bounded SIGTERM/SIGKILL lifecycle and a build-error policy that keeps the previously-running binary alive on broken saves. Sky.Live's hot-reload chain closes four gaps that previously left the browser DOM stuck on the old view: SSE reconnect-resync (forces a fresh full-body push after every handshake), persistent `outSeq` (so the new process's resync frame isn't dropped by the client's stale-frame guard), session-loss probe with hard reload (recovers from memory-store restart and `sky.toml [live] store` changes), and `X-Sky-Live: 1` markers on 404 paths so the probe distinguishes real Sky.Live responses from proxy-rewritten ones. `sky install` for projects with extensive Go FFI (Stripe SDK, Firebase, Firestore, …) gets a chunked-multi inspector mode (one `packages.Load` per chunk dedupes shared transitive deps) + parallel chunks (`SKY_INSTALL_PARALLEL`, default `min(numProcessors, 4)`) + trimmed loader-mode flags (dropped `NeedSyntax | NeedTypesInfo`, byte-identical output). Skyshop benchmark: 67.5 s → 58.5 s real, 17 % CPU reduction. See [docs/compiler/versions.md](docs/compiler/versions.md) for the full entry.
- **v0.11.0 — `Std.Ui` typed no-CSS layout DSL + 5 root-cause compiler fixes + Apache 2.0 (2026-04-27).** New `Std.Ui` surface (`row` / `column` / `el` / `paragraph` / `link` / `image` / `button` / `input` / `form` + `Background` / `Border` / `Font` / `Region` / `Input` / `Lazy` / `Keyed` / `Responsive` sub-modules) renders to inline-styled HTML with semantic tag dispatch (`<main>` / `<nav>` / `<aside>` / `<footer>` / `<h1..h6>`) — no CSS files. `examples/19-skyforum` is the end-to-end demo. Five long-standing compiler bugs fixed at root cause (multi-line `exposing (…)`, cons-with-constructor pattern, `any` wildcard sharing, tuple-pattern in lambda, `/=` on polymorphic generics) — every fix has a regression spec. `sky fmt` auto-breaks long imports past ~100 chars; `sky test` exits 0 for passing modules. See [docs/compiler/versions.md](docs/compiler/versions.md) for the full entry.
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
