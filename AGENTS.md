# AGENTS.md

> Agent-agnostic guide for AI tools (Claude, Copilot, Cursor, Codex, …)
> working in the Sky repository. It is the **source of truth**; tool-specific
> files layer on top of it (Claude Code reads `CLAUDE.md`, which imports this
> file and adds Claude-Code-only operational rules).
>
> **Progressive disclosure.** This file is deliberately lean. It teaches the
> language, the app-building decisions, and the non-negotiable rules, then
> points you at two authoritative, always-current sources for depth:
>
> - **`sky doc <Module>`** — the live stdlib API (typed signatures + summaries +
>   examples), generated from the stdlib source, so it never drifts. Prefer it
>   over any hand-copied API table. `sky doc --list` enumerates every module;
>   `sky doc --serve` opens a browsable server.
> - **`docs/`** — deep dives (architecture, Sky.Live, Std.Db, Std.Ui, auth,
>   tooling). The map is in the [Deep dives](#deep-dives) table below.

## What Sky is

Sky is an Elm-family, purely-functional language that compiles to **typed Go**.
One language for the whole stack: web UIs (Sky.Live), HTTP/JSON APIs, CLIs, TUIs,
desktop apps, background jobs. The design goal is **"if it compiles, it works"** —
no user-written FFI, no nulls, no runtime panics from well-typed code, clear
errors, batteries-included stdlib.

The compiler is the **Rust rewrite** (cargo workspace at `rust/`). The retired
Haskell compiler lives under `legacy-haskell-compiler/` and serves as a
**differential oracle** (`sky-out/sky`) the Rust output is checked against
byte-for-byte. Current line: **v0.22.x**.

## Language essentials

Sky's surface is Elm. If your training on Elm is thin, read this section
carefully — it is the part that catches models out.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)      -- Result/Maybe/identity/… autoloaded
import Sky.Core.List as List
import Std.Log exposing (println)

type alias User = { name : String, age : Int }   -- record alias
type Msg = Increment | Decrement                 -- tagged union (ADT)

greet : User -> String                            -- type annotation (optional but preferred)
greet u =
    "Hi " ++ u.name

update : Msg -> Int -> Int
update msg count =
    case msg of                                   -- case is exhaustiveness-checked
        Increment -> count + 1
        Decrement -> count - 1

main =
    println (greet { name = "Ada", age = 40 })
```

Core syntax you must get right:

- **Pipelines**: `x |> f |> g` (left-to-right), `f <| x` (right-to-left).
- **Lambdas**: `\x -> x + 1`. **Cons**: `head :: tail`. **Lists**: `[ 1, 2, 3 ]`.
- **`let … in`** for locals; **`case … of`** for pattern matching (must be exhaustive).
- **Record update**: `{ user | age = 41 }` (keeps every other field).
- **Import aliasing**: `import Std.Db as Db exposing (Store)`.
- **Multiline strings**: triple-quoted with `{{expr}}` interpolation; `\{{` escapes.
- **Negative literal args**: `f -1` parses as `f (-1)`; `f - 1` is subtraction.
- No custom operators, no `where` clauses (use `let`), no higher-kinded types (HM only).

**Effect boundary — the single most important rule.** Every observable side
effect returns `Task Error a`. Pure code is bare `a`; fallible-pure is
`Result e a` / `Maybe a`; effects are `Task Error a`. Never use `String` as an
error type — always `Result Error a` / `Task Error a`.

```elm
-- pure          : String.length, List.map, Crypto.sha256
-- fallible-pure : String.toInt : String -> Maybe Int
--                 Encoding.base64Decode : String -> Result Error String
-- effect        : File.read, Http.get, Db.query, Time.now  → Task Error a
```

`let _ = someTask` auto-forces the task (fires the effect). A top-level
zero-arg binding is **memoised** (a CAF — evaluated once, cached): `db =
Task.run (Db.connect ())` opens one shared pool; but `newId = Task.run Uuid.v4`
freezes to ONE uuid — for a fresh value per call make it a function
(`newId _ = …`; call `newId ()`). The compiler warns when a memoised binding
forces a fresh-value kernel (Uuid/Random/Time/Crypto.random).

Full effect surface, bridges (`Task.fromResult`, `Task.onError`, …), and the
two-level error pattern: `docs/stdlib.md` + `sky doc Sky.Core.Task`.

## Writing a Sky app — interview first, then architect

**You are the front line. Lead with questions — do not guess.** When a user
says "build me X in Sky", your first job is not to write code; it's to
understand *what they are actually building* and *what it has to survive*, then
propose the scope + architecture. Production-grade code does not survive
guesswork, and over-engineering a weekend project wastes everyone's time. Ask
**one focused question per genuine ambiguity**, propose sensible defaults for
the rest, and confirm the shape before building beyond a proof of concept.

### Question 0 — what is this for? (the tier drives everything)

Ask this first. A single answer reshapes most of the decisions below:

| Tier | Meaning | Leads to |
|---|---|---|
| **Prototype / pet / internal** | one user or a trusted few, runs on one machine, "lost on restart is fine", ships today | `sqlite` (or `memory`) · session store `memory`/`sqlite` · auth optional · single local binary or one VM · local logs + the embedded console · **no** Postgres/Redis/K8s ceremony |
| **Production** | real users depend on it, must survive restarts + scale + failures, has an SLA | Postgres · **shared** session store (redis/postgres) · `Std.Auth` (or OAuth) · sticky sessions + cross-instance pub/sub if multi-replica · `ENV=production` gate · structured logs + OTel / console hub · file migrations committed |

Don't force production ceremony on a pet project, and don't let a "quick
internal tool" that's clearly headed for real users ship on `memory` sessions.
If the user is unsure, ask what happens if the process restarts and how many
people will use it — that usually settles the tier.

### The other decisions to confirm

Once the tier is known, most of these have an obvious default — surface the
non-obvious ones as questions:

1. **App shape** — the biggest fork. Web UI? headless API? terminal? desktop?
   one-shot job? (see the matrix below). Full-stack (server-driven UI) is Sky's
   sweet spot — **Sky.Live** — one language, no separate front-end. Reach for
   **Sky.Http.Server** only when there is no browser UI, **Sky.Tui** for a
   terminal UI, **Sky.Cli** for a job/CLI, **Sky.Webview** for desktop.
2. **Persistence** — SQLite (embeds, single-file — great default for tier-1) /
   PostgreSQL (multi-instance) / Firestore / Redis / none.
3. **Auth** — none / `Std.Auth` (you own users) / OAuth / external (Auth0/Clerk).
4. **Sky.Live session store** — memory (dev only) / sqlite / redis / postgres.
   *Required even when the primary DB differs.* Shared store the moment there is
   more than one replica.
5. **Deployment target** — local binary / Docker / Cloud Run / Kubernetes / VM.
   Ask what the host *is*, because a Sky.Live app plus its own embedded
   PostgreSQL fits comfortably in **1 GB**, and most people over-provision:

   | | RAM |
   |---|---|
   | Sky app binary (Go), idle | **~21–27 MB** (measured, `26-ui-showcase` on an e2-small) |
   | Sky app, settled under real traffic | **~56 MB** (measured mean, sky-lang.org on an e2-micro; a 45-min window after ~40 h of process uptime) |
   | Embedded PostgreSQL — the whole tree, as `MemAvailable` actually falls | **+21.9 MB** idle · **+28.4 MB** once sessions are written through it |
   | Sky.Live sessions, **625–650 kB each on x86, PostgreSQL store, stock `GOGC=100`** (the shipped default raises this — see below) | ~65 MB at 100 concurrent |
   | **Base, before sessions — measured whole-machine, not a sum of the rows** | **~382 MB** app alone · **~410 MB** with embedded PostgreSQL carrying the sessions |

   Every row: `docs/perf/runs/gcp-embed-postgres-20260815/sweep.tsv`, analysed
   in `docs/perf/skylive-interaction-cost.md:1044-1100`; the settled-app row is
   that document's row 7. The base line is `MemTotal − MemAvailable` on the
   measured machine (2,023,888 kB total; median idle `MemAvailable` 1,632,340
   / 1,603,636 kB for the two configurations), so it already contains the OS
   and needs no OS row added to it.

   > **This table used to open with "Minimal Linux | ~250 MB" and close with
   > "Base, before sessions | ~380 MB", and the rows between them no longer
   > added up to it** — the ~380 was left in place when the PostgreSQL rows
   > were corrected downward, so it had become an orphan of an earlier
   > arithmetic. The OS row is deleted rather than adjusted: no run in
   > `docs/perf/runs/` measured a bare Linux image, and on the machine that
   > *was* measured the OS plus an idle app came to ~382 MB, so ~250 MB was
   > not a conservative allowance either. A "PG backends" row also sat here
   > carrying no MB value at all; its content was a process count, not RAM,
   > and it has moved into the paragraph below — with the count corrected.

   **PostgreSQL is a fixed block, not a per-session tax.** Its tree's RSS
   slope against established sessions is −10 kB / +22 kB per session — zero
   within noise (`docs/perf/skylive-interaction-cost.md:156-158`), because the
   pool holds a flat **6-connection pool** (`dbSharedAuxPoolSizeFor(2) = 6`) at
   25, 50 and 100 concurrent sessions; `pg_backends_max` reads **7** — the pool
   plus the 1-Hz sampler's own psql
   (`gcp-embed-postgres-20260815/sweep.tsv`: 7 in the config-C rows at 50 and
   100 and one of the three at n=25, the other two a documented sampler bug,
   `README:78-82`) — and **7, occasionally 8, at 100 / 300 / 500**
   (`gcp-x86-capacity-20260816/README.md:49-53`, against
   `max_connections = 56`). Earlier text here and in
   `skylive-interaction-cost.md` said `pg_backends_max` was **6**; the column
   reads 7 (the 6 is the pool).

   **Sessions are the number that decides the instance** — and the per-session
   figure moves with the collector, which is why it changed. At the stock
   `GOGC=100` a session costs **625–650 kB on x86 with a PostgreSQL session
   store** and 451–531 kB with the memory store, measured as an OLS-free slope
   across n = 100 → 500 on `examples/19-skyforum` at a 94-element view
   (`docs/perf/runs/gcp-x86-capacity-20260816/`). Sky now ships **`GOGC=400`
   under a derived `GOMEMLIMIT`** (below), and `GOGC` multiplies the slope, not
   just the baseline: 100 → 400 scales it **2.9×** on the same app and store
   (`docs/perf/runs/gogc-postgres-20260816/`). The x86 slope at the shipped
   default is **unmeasured** — do not multiply the two runs together and quote
   the product; this programme's projections have been wrong by several-fold,
   repeatedly. What bounds memory at the shipped default is the derived
   `GOMEMLIMIT` itself, not a per-session figure.

   > Quote a per-session number **with its view size and its `GOGC`**, or it
   > will be wrong. This table long carried ~1.35–1.42 MB, which was measured
   > on a *different app* — `26-ui-showcase` at 384 elements, memory store — and
   > is not the cost of a session in general.

   **CPU binds a full order of magnitude before memory on real GCE
   instances.** The e2-micro's ~450-session memory ceiling against its
   25–50-session CPU knee is a **≈9–18× gap** — a ratio of two measured
   observations, not itself a directly measured quantity. An
   e2-micro *holds* ~450 sessions and is **unusable past ~50** (knee 25–50,
   peak ~18 interactions/sec, and at 250 sessions it fails 74–91% of
   interactions across three repeats, 96% at 500; commit `ba3c3b1d`,
   `docs/perf/runs/gcp-x86-20260815/micro-noagent.tsv`). The
   ~450 is an **observation, not a division**: asked for 500, that machine
   established **447** (`gcp-x86-20260815/micro-noagent.tsv`, n=500 row, which
   also records 96% of interactions failing there) with `MemAvailable` down to
   **~43 MB** (`micro-rss-n500-r1-memexhaustion.txt`) — so it does not move
   when the per-session slope is restated. No comparable memory ceiling was
   ever *reached* on e2-small: it established **500 of 500** in all three
   repeats of the same sweep (`small-noagent.tsv`).
   Re-measured at commit `3ed83c08` with embedded PostgreSQL carrying the
   sessions (`docs/perf/runs/gcp-x86-capacity-20260816/`): an **e2-small
   sustains 64.3 int/s at 300 sessions** (failure knee between 100 and 300),
   an **e2-medium 261.5** (knee above 500). Quote throughput with its commit
   — these figures predate later optimisation work. Sizing on memory alone
   overstates an e2-micro by roughly an order of magnitude (≈9–18×).

   **Count physical cores, not vCPUs.** A GCE vCPU is an SMT thread:
   `e2-standard-8` is **4 cores × 2 threads**, not 8 cores. Four threads on four
   distinct physical cores serve **1,568 int/s**; the same four threads on two
   physical cores serve **1,097** — 70%. The second thread on a core is worth
   ~1.27×, not 2× (`docs/perf/runs/gomaxprocs-scaling-20260816/`). Any capacity
   number derived from a vCPU count overstates the machine by roughly that
   factor. Counted in physical cores, throughput scales at **79–80% efficiency
   per doubling** (same run) — a larger instance is a legitimate route when
   sized on cores.

   **The GC default is derived, not configured.** At startup the runtime sizes
   `GOMEMLIMIT` from detected machine memory — the cgroup limit first, so a
   container is not sized to its host — after subtracting the OS and, under
   `--embed`, the cluster's own `shared_buffers`, and sets `GOGC=400` under it.
   Measured: **+19% throughput at 759 MB peak RSS** at 500 sessions on the
   PostgreSQL store, against 1,827 MB for a bare `GOGC=800`. An explicit `GOGC`
   or `GOMEMLIMIT` in the environment always wins, and a machine too small to
   hold the bound is left on Go's defaults entirely. There is no `sky.toml`
   knob: the Go env vars are the escape hatch, and they work even when the
   process is launched by something that never reads `sky.toml`.

   Two things to tell a user picking a burstable e2 instance: **the first run
   after idling overstates sustained capacity by ~2.7×** — a rested e2-small
   measured 183.5 int/s, then 58–71 for six consecutive runs (seven-run soak,
   `docs/perf/runs/gcp-x86-capacity-20260816/`; the same decay on e2-micro
   read 17.5 → 9.6 → 9.5/s, `docs/perf/runs/gcp-x86-20260815/`) — so plan
   with the sustained figure, never the first number they see. And a figure
   measured under a container CPU quota was optimistic against real hardware
   by **2.5–5×**.

   The **diff** is not the cost — ~128 ns per VNode, under 1% of an
   interaction — but the interaction as a whole **does track view size**,
   because `view(model)` re-runs in full every interaction:
   `cost_ms ≈ 0.124 + 0.018 × elements` over the three smallest views (30–94
   elements) on one core (`docs/perf/runs/forum-rebaseline-20260816/`; the
   all-seven-sizes fit is `−0.147 + 0.0197 × elements`). Optimising the differ buys
   nothing; trimming a large view is a real lever. And **a single instance
   has no replica**:
   `sky db provision --shared` generates a backup timer, a single `--embed` app
   does not, so a `pg_dump` schedule is the operator's to add. Sizing detail:
   `docs/skydb/embedded-postgres.md`.
6. **Observability** — local logs + embedded console / central console hub /
   OTel collector (Honeycomb / Tempo / Datadog).

When in doubt, ask one focused question rather than heroically guessing — then
build with the stdlib defaults, which already make the app survive a restart,
scale horizontally, refuse cross-tenant reads, and emit traceable logs *when the
tier calls for it*.

### App-shape matrix

| User wants | Use | Entry point |
|---|---|---|
| Web app (forms, real-time, UI state) | **Sky.Live** | `Live.app cfg` |
| Client-rendered SPA / cross-platform client loop | **Sky.Spa** | `Spa.app cfg` |
| HTTP/JSON API (no browser UI) | **Sky.Http.Server** | `Server.listen 8000 [...]` |
| Background job / cron | **Sky.Cli** | `main = Task.run work` |
| Terminal UI | **Sky.Tui** | `Tui.app cfg` |
| One-shot CLI | **Sky.Cli** | `main = Task.run cliCmd` |
| Desktop app | **Sky.Webview** | `Webview.app cfg` (macOS in v0.1) |
| WebSocket feed | **Sky.Http.Server.WebSocket** | `Server.upgrade req` |
| SSE / token stream | **Sky.Http.Server.Stream** | `Server.Stream.emit` |
| One source, many targets (web/terminal/desktop) | **Std.App** | `App.app { init, update, view, subscriptions }` + `--target` |

**`Std.App` — the unified builder (the cross-platform default; `Std.Ui` view).**
For an app that should run across several shapes from ONE source, write
`app = App.app { init, update, view : model -> Element msg, subscriptions }
|> App.withNotFound … |> …` and `main = App.run app`; a build-time `--target
family[:variant]` picks the backend (optional — bare defaults to `web`). You never
import `Std.Live`/`Spa`/`Tui`/`Cli`/`Webview` yourself. Delivery = family, native =
platform: `web` · `tablet` → **Sky.Live**; `desktop` → Sky.Live in a native
window; `terminal:tui|cli` → **Sky.Tui/Cli**; `web:app` · `desktop:mac|windows|
linux` · `tablet:ipad|android` · `mobile:ios|android` → **Sky.Spa** (client wasm).
The build rewrites `App.run` → the target's `run<Backend>` (DCE prunes the rest)
and, for the Spa targets, **synthesises a `Spa.app` from your `App.app`** and feeds
the existing auto-split — so client targets need **no** separate `Std.Spa` entry.
`web` requires `App.withNotFound` (compile-enforced). Invalid combos are rejected
at parse time (`web:ios` → *"did you mean `mobile:ios`?"*). `sky check` type-checks
the core (target-scoped for a specific backend). `Std.Html` views → use `Sky.Live`
directly. See `docs/skyapp/overview.md` + `sky doc Std.App`.

### Pinned defaults — the preferred way to write Sky

**Default to these. Deviate only when the user explicitly asks for something
else, or the use case genuinely rules the default out.** They are what keep
AI-written Sky consistent, secure, portable, and reviewable — each was chosen
for UX/DX/security/scalability, not by accident.

| Concern | Default (and why) |
|---|---|
| **View layer** | `Std.Ui` — the typed, no-CSS layout DSL — **not** `Std.Html`. The same `Element` view renders across **Sky.Live (web), Sky.Tui (terminal), and Sky.Webview (desktop)**, so one view function is cross-platform; `Std.Ui` also HTML-escapes everything. Reach for `Std.Html` *only* to wrap raw markup `Std.Ui` can't express. |
| **Database** | **Tier-driven.** Pet / prototype / simple / single-instance → **SQLite** (embeds, single file, zero ops). Production / multi-instance → **PostgreSQL**. (Future: **BlueDB** — the Sky-native reactive data layer, WIP — will become the default once it lands; prefer it when it ships.) Always model records through **`Std.Db.Store` + `Std.Codec`** (one codec drives JSON + dialect-safe DB); drop to raw `Std.Db` only for joins / aggregates / transactions. The *same app code* works on SQLite and Postgres — only the driver differs. |
| **Auth** | The internal **`Std.Auth`** module by default (bcrypt + HS256 JWT cookies — you own the users). OAuth (Google/GitHub) or external (Auth0/Clerk) only when the user needs them. Never `fmt.Sprintf("%v", secret)` — secrets are typed. |
| **Serialization** | `Std.Codec` (`Codec.auto blank`) for record↔JSON+DB from one definition. Raw `Json.Encode/Decode` only for a shape a codec can't express (legacy/third-party wire formats). |
| **Money / decimals** | `Std.Money` on `Std.Decimal`. **Never** raw `Float` for currency. |
| **Errors** | `Result Error a` / `Task Error a`. **Never** `String` as an error type. |
| **Concurrency** | `Cmd.batch` / `Task.parallel`; in-process pub/sub via `Cmd.publish` + `Sub.subscribeTopic`. |
| **Observability** | `Std.Log` structured logs; the dev console auto-mounts at `/_sky/console`; `OTEL_EXPORTER_OTLP_ENDPOINT` for an external collector. Telemetry **storage** is tunable via `Sky.Config.withTelemetry*` builders (or `SKY_TELEMETRY_*` env, which overrides them): counter/histogram coalescing windows to cut DB rows, and `withTelemetryDbCapacity` for the hourly size-report "near full" flag. See `docs/observability.md` + `sky doc Sky.Config`. |
| **Sky.Live navigation** | Every internal link is `sky-nav` (one persistent SSE per session). Bare `<a href>` only to deliberately leave the app. |
| **Password forms** | `Ui.form [Ui.onSubmit DoSignIn]` with a typed record; never per-keystroke `onInput` on a password field. |
| **No raw HTML/JS** | `Std.Ui` HTML-escapes everything; `data-sky-eval` is forbidden. |

The app-shape details (Sky.Live TEA loop, routing, session lifecycle, forms,
`Std.Ui` layout, Sky.Tui, Sky.Webview) live in `docs/skylive/`, `docs/skyui/`,
`docs/skytui/`, `docs/skywebview/`. Read the relevant one before building.

### Production gate (when the user says deploy / prod / Cloud Run / K8s)

**This is the same checklist the app prints on every dev start**, under its
`listening` line. One list, in two places, deliberately — see
`runtime-go/rt/startup_report.go`.

- `ENV` set to anything that is **not** `dev` / `development` / `local` (the
  runtime tests for the dev spellings, not for the literal `production`). Locks
  the dev console + banner + metrics.
- `SKY_CONSOLE_AUTH` = `token` \| `app` \| `off`. With `token`, also set
  **`SKY_CONSOLE_TOKEN`** — without it the console falls back to an
  auto-generated `.sky/console-token`, which a container regenerates every boot
  and no operator can read. With `token`/`app` set and no console mounted the
  app **exits 1**; `off` is the way to declare the surface intentionally absent.
- `SKY_ADMIN_TOKEN` for the `/_sky/metrics` bearer. (`SKY_METRICS_TOKEN` and
  `SKY_CONSOLE_TOKEN_SECRET` are back-compat aliases for it, not separate
  settings.)
- Multi-replica → a **shared** session store (`redis`/`postgres`), sticky
  sessions keyed on `sky_sid`, and cross-instance pub/sub (`store=redis` or
  `SKY_LIVE_BROKER_URL`). `memory` and `sqlite` are single-instance only.

> **`SKY_AUTH_TOKEN_SECRET` is not a runtime setting, and this gate used to say
> it was.** Nothing in `runtime-go/` reads it: `sky_sid` is unsigned random hex,
> and `Auth.signToken` takes its secret as a Sky-level *argument*. The name is a
> convention in user code (`System.getenvOr "SKY_AUTH_TOKEN_SECRET"`) that only
> `sky doctor` knows about. If you use `Std.Auth`, whatever variable you feed
> into `Auth.signToken` must be ≥ 32 bytes; if you don't, setting it changes
> nothing.

Env var reference: `docs/sky-toml.md` + `docs/skylive/architecture.md`.

## Build & test

```bash
sky init [name] [--production]   # new project
sky build src/Main.sky           # compile → sky-out/app
sky run src/Main.sky             # build + run  (--profile for pprof)
sky check src/Main.sky           # type-check + go build  (≡ sky build)
sky watch src/Main.sky           # file-watch rebuild + restart
sky verify                       # project pre-release gate: fmt + check + build + tests
sky fmt src/Main.sky             # opinionated formatter (idempotent)
sky test tests/MyTest.sky        # Sky.Test runner (SKY_TEST_JSON=<path> → per-case JSON report)
sky doc <Module>                 # stdlib docs (--serve / --tui / --list / --export <dir>)
sky db init | migrate --gen | migrate | seed | status | push   # file-based migrations
sky db start | stop [--all] | ps [--all]                       # local PostgreSQL cluster
sky db provision --embed                                       # fetch + pin a PostgreSQL bundle
sky db provision --shared [--service] [--app <name>]           # one host cluster, role-per-app
sky build --embed src/Main.sky   # bundle PostgreSQL INTO the binary; ./sky-out/app --embed
sky spa-split src/Main.sky --out .split --build   # explicit Sky.Spa split (advanced)
sky add <go/module> | remove | install | update                # Go FFI deps
sky doctor [--fix] | upgrade | upgrade-claude | clean
```

**Sky.Spa entries auto-split.** `sky build src/Main.sky` on a `Spa.app` app
derives + builds the wasm frontend + native backend under `.split/` (no manual
`spa-split`); `sky run src/Main.sky` then runs the backend, which serves the
frontend + `/_rpc` same-origin. `--target desktop|ios|android` (frontend shell)
and `--embed` (bundle PostgreSQL into the backend) COMPOSE with the split, they
do not disable it. `sky check` type-checks the shared source directly; `sky
spa-split <entry> --out <dir>` is the explicit form when you want the artefacts
kept at a chosen path. (Recursion is impossible: the generated projects carry a
`[spa] generated = true` marker that `sky build`/`sky run` never re-split.)

**Embedded PostgreSQL — dev/prod engine parity.** `Std.Db` is dialect-safe
across SQLite and Postgres, and that gap is a real tax: `Codec.auto` cannot
encode `Money`/`Decimal`, and there is no `NUMERIC` DDL kind, while `Std.Money`
is the pinned currency default. Running the same engine in development that you
run in production is now the easy path.

The rule that makes it work: **the app never knows which tier it is in.** It
consumes a DSN (`<PREFIX>_DB_PATH`, or `DATABASE_URL`) — only the provisioner
changes. `sky run` supervises a per-project cluster; `./app --embed` runs its
own; an operator sets a DSN; a shared host cluster issues one per app. Opt in
with `[database] embedded = true`. `--embed` alongside an explicit DSN is an
**error**, not a precedence rule.

> **Bundles are published.** `sky db provision --embed` resolves
> **`postgres-bundle-v18.6`** (built by `.github/workflows/postgres-bundle.yml`,
> cut 2026-08-19) and fetches a self-contained, licence-clean PostgreSQL 18.6
> tarball for the host platform — linux/darwin × amd64/arm64, each with an SBOM
> and verified against the release's `SHA256SUMS`. The tree vendors its
> permissive deps (openssl/icu/xml2/zstd/lz4/zlib) under `@rpath`/`$ORIGIN`, so
> it drops into a **glibc** image (debian-slim, distroless-base) with no host
> packages. `SKY_POSTGRES_BIN` or a system PostgreSQL still works for `sky db
> start` if you'd rather not fetch. Full design: `docs/skydb/embedded-postgres.md`.

**Where embedded PostgreSQL can run.** The bundle is glibc-linked and needs a
glibc userland (with `libstdc++`), a **durable writable data dir**, and the run
user in `/etc/passwd`. Verified empirically (`sky db provision --embed` + a Sky
app querying it, under Apple `container`):

| Target | Embedded PG |
|---|---|
| macOS / glibc-Linux laptop · EC2 / GCE / any glibc Linux VM | ✅ (a VM's persistent disk is ideal) |
| Container: `debian-slim` / `distroless-base` **+ a mounted volume** | ✅ (`server_version=18.6` returned) |
| Container: **Alpine** (musl) or **`FROM scratch`** | ❌ no glibc loader; `gcompat` still dies on libstdc++ + glibc-fortify symbols — needs a musl/static bundle |
| **Cloud Run** | ⚠️ warm single instance **+ a mounted volume** only (FS is in-memory/ephemeral) |
| **AWS Lambda / GCP Cloud Functions** | ❌ stateless/ephemeral — pair with managed PG |
| **Windows** (native) | ❌ no Windows bundle + the machinery is unix-socket only → WSL2 or a system PostgreSQL via `DATABASE_URL` |

Rule of thumb: durable, single-owner storage → embedded PG on a **VM or a
container-with-a-volume**; stateless serverless → **managed PG** (Cloud SQL /
RDS). Where the bundle cannot run the runtime **refuses loudly** (the "PostgreSQL
binaries do not run" check), it does not silently lose data.

**Never run `sky build` from the repo root** — it overwrites the compiler binary
in `sky-out/`. Always `cd` into the example/project dir first.

Compiler development (Rust): `cd rust && cargo build --release -p sky`. The
verification sweep — `cargo test --workspace` + the xtask gate suite
(`cargo run -p xtask -- <gate>` for roundtrip / resolve / infer / reject /
build-run / coerce-floor / repro / golden / fuzz) + the example sweep
(`scripts/example-sweep.sh`) + behavioural conformance (`scripts/conformance.sh`)
— is the source of truth. Green-everywhere is a hard release gate. Corpus gates
are necessary but **not sufficient**: a change is not verified until it also
passes the full example sweep + a real app (see
`docs/rust-rewrite/13-change-verification-and-edge-cases.md`).

**Live tests fail rather than skip.** Some tests need a real environment — a
PostgreSQL installation, a Go toolchain, the `sqlite3` client — and `cargo test`
**fails** when one is missing, naming what to install. That is deliberate:
fourteen tests covering the shared-cluster security boundary used to end their
probe with `eprintln!(…); return;`, and with and without a cluster they printed
byte-identical verdict lines (`ok. 14 passed`), so CI — which installed no
PostgreSQL in the only job that ran them — had never run one of them. If you
genuinely cannot provide the environment, say so out loud:

```bash
SKY_LIVE_TESTS=skip cargo test --workspace   # the ONLY way to skip a live test
```

New live tests gate through `rust/crates/sky/src/live_gate.rs`
(`live_gate::required(Need::Postgres, <your probe>)`), and
`rust/crates/xtask/tests/live_tests_are_not_silently_skipped.rs` fails the build
on the shapes that used to be written instead.

`xtask coerce-floor` takes the same variable for the same reason. Its golden
locks a runtime-narrowing floor **per project**, and a project whose generated
FFI surface is absent (`sky-ffi/` and `.skydeps/` are `.gitignore`d, so a fresh
checkout has neither) cannot be measured — which used to be filed under "did not
emit (not gated)" while the run reported PASS on the remainder, measuring 56 of
61 rows. An unmeasurable row now FAILS, naming the `(cd <project-dir> && sky
install)` that fixes it; `SKY_LIVE_TESTS=skip` downgrades it to a loud
`UNMEASURED` block, and `--bless` refuses under a shortfall rather than write a
golden mixing measured rows with carried-forward ones. Both verdict lines state
how many of the golden's rows the run actually covered.

`cargo run --release -p xtask -- harness` runs the registered gates through the
gate harness, which enforces each gate's budget by `killpg`, requires an exact
assertion count, and refuses to report PASS when it cannot establish a verdict
(`NOT RUN` / `UNPROVEN` → `UNKNOWN`, exit non-zero). Every registered gate
declares a falsifying mutation — an empty set fails the build — and
`--verify-falsifiers` proves the mutation makes the gate red. See
`docs/tooling/gate-harness.md`.

## Non-negotiable code rules (enforced by `cargo test`)

These apply to any Sky code you write or any compiler change you make:

- **No `Result String a` / `Task String a`** in public surfaces — use `Error`.
- **No runtime panic from well-typed Sky code.** Every known panic class has a
  regression test (`runtime-go/rt/*_test.go` / `test/**Spec` / conformance).
- **No `Std.IoError`, no `RemoteData`** — both removed pre-v1.
- **No silent numeric coercion** — `AsIntChecked` is fallible; `OrZero` marks
  display-only lenient helpers.
- **No raw `.(T)` assertions on any-typed thunks** — route via `rt.Coerce[T]`.
- **Record field enumeration sorts by `_fieldIndex`** before order-dependent emission.
- **Secrets are typed** — every secret-bearing argument is the opaque
  `Sky.Core.Secret.Secret`, never `String`/`any`: `Auth.signToken`/`verifyToken`/
  `signSlidingToken`, `Jwt.hs256` and `Jwt.rs256` (the RSA *signing* key; the
  public verify key is `Jwt.rs256Verify : String`), the `Crypto` AEAD keys
  (`aesGcmEncrypt`/`Decrypt`, `chacha20*`, `aesKeyFromPassword` — which also
  *returns* a `Secret`), and `Http.withBearer`/`withApiKey`. `Cli.readPassword`
  *returns* a `Secret`. A `Secret` redacts itself in every print/log/JSON path;
  wrap at the boundary (`Secret.fromEnv "VAR"` / `Secret.fromString runtimeStr`)
  and unwrap only via the greppable `Secret.reveal`. `Crypto.hmacSha256` stays a
  general `String`-keyed primitive (its key is not always a secret). See
  `docs/security/secret-migration.md`.
- **`sky check` ≡ `sky build`** — both invoke `go build` on the emitted Go.
- **Root-cause fixes only.** Never suppress a type error or warning; a defensive
  cover-up that hides a contract violation IS a violation.

### Engineering norms (repo work)

- **No-deferral principle.** A bug you spot — yours or pre-existing, in dev, CI,
  or a sweep — enters the pipeline immediately and is fixed in the next patch.
  "Pre-existing / defer / known issue" are not shipping excuses. The user may
  explicitly override with "ship without fixing X"; that is the only exception.
- **Every feature/bug becomes a regression test** before the fix lands — the
  failing test is the discovery artefact. Compile-time behaviour → cargo/`hspec`
  specs; runtime helpers → `runtime-go/rt/*_test.go`; stdlib semantics →
  `tests/**/*Test.sky`; behaviour → `tests/conformance/`.
- **Timeout-bound every long command — through the shim, never a bare
  `timeout`.** `cargo test` / xtask gates run under a 60 min ceiling. A test
  that hangs is a bug to bisect, not wait out. In a script,
  `source scripts/lib/with-timeout.sh` and call `with_timeout <secs> <cmd...>`;
  `rust/crates/xtask/tests/scripts_bound_time_portably.rs` fails the build on a
  bare `timeout`. GNU coreutils `timeout` is absent on stock macOS and was
  absent here when the nix shell supplying it went away — at which point
  `timeout 1200 go test -race ./rt/... | tail -8` printed `command not found`
  to stderr, took `tail`'s status, and reported **exit 0 having run nothing**.
  Where the bound only wraps `go test`, prefer its own `-timeout` (it dumps
  stacks and names the hung test) with `with_timeout` outside as the backstop.
- **A gate whose prerequisite is missing FAILS, naming what to install.** Never
  skip, never pass. Shell gates use `require_tool <name> <hint>` from
  `scripts/lib/require-tool.sh`; Rust live tests use
  `rust/crates/sky/src/live_gate.rs`. Both take the same opt-out —
  `SKY_LIVE_TESTS=skip` — and both treat any other value as an error rather
  than guessing.
- **A gate may not measure a compiler older than the tree.** `sky-out/sky` is
  installed by exactly one line (`scripts/build.sh:80`), so a bare
  `cargo build --release -p sky` leaves `rust/target/release/sky` fresh and
  `sky-out/sky` untouched — and every consumer then measures whatever
  `build.sh` last produced. Sixteen scripts read that path and none checked it.
  Source `scripts/lib/fresh-compiler.sh` and call
  `require_fresh_compiler "$SKY" "$ROOT"` (Node: `scripts/lib/fresh-compiler.mjs`;
  it runs the shell library rather than reimplementing it). It FAILS naming
  `./scripts/build.sh`, and unlike `require_tool` it has **no opt-out** — a tree
  that has the sources can always rebuild, so "I cannot fix this" is never true.
  The check is content-aware where content is provable: the build bakes a
  fingerprint of the embedded asset trees into the binary
  (`sky-embed-fp-v1:<sha256>`, `rust/crates/ffi/build.rs::fingerprint`), so a
  legitimate prebuilt binary under fresh-checkout mtimes passes when only
  embed-root mtimes moved and the content matches, and a `touch`ed binary whose
  embedded content is from another tree FAILS regardless of mtimes — never
  `touch sky-out/sky`. The compiler's own Rust sources carry no baked witness
  and remain mtime-compared.
  `rust/crates/xtask/tests/gates_measure_a_fresh_compiler.rs` fails the build on
  a new consumer that skips the check, on a script bash 3.2 cannot parse, on
  the root lists drifting from the `stage(…)` calls in
  `rust/crates/ffi/build.rs` (the staging authority — parsed, not copied), and
  on the shell and Rust fingerprint constructions disagreeing. The loud symptom
  cost a full diagnosis: a sweep after a bare `cargo build` reported 22 of 22
  conformance suites FAILED on a consistent tree. The quiet one is worse — a
  stale binary that PASSES certifies source it never compiled.
- **The embed never contains a hidden or gitignored file.** `build.rs` staging
  drops dot-files and dot-directories as a class: running a bundled console
  writes a runtime secret at `sky-bundled/<app>/.sky/console-token`
  (gitignored, 0600), and before the class rule it was staged into
  `embedded-assets/` and baked into every locally-built `sky` binary —
  recoverable by `grep` from the binary and re-materialised into
  `~/.cache/sky/assets/<hash>/` by any standalone run. Two gates in
  `gates_measure_a_fresh_compiler.rs` enforce it: staging a planted
  `.sky`/`.env`/`.skydata` fixture must drop them, and everything staged from
  the real repo is `git check-ignore`d. Consequence for contributors: a file a
  build must embed may not be hidden, and a hidden file is never a compiler
  input (the freshness walks skip them too, so a runtime-written token cannot
  turn gates red).
- **Disk hygiene.** `scripts/build.sh` + `scripts/example-sweep.sh` auto-prune the
  Go build cache at a 5 GB threshold; the `xtask build-run` gate self-guards
  disk before the sweep. Reclaim manually (`go clean -cache`, worktree cleanup)
  when under 5 GB free.
- **Template + doc sync (non-negotiable).** When stdlib / syntax / Sky.Live APIs /
  CLI verbs change, update **this file**, `templates/CLAUDE.md` (+ `templates/AGENTS.md`),
  and the matching `docs/*` in the **same commit** (see the [Deep dives](#deep-dives)
  table). Kernel-only module docs (`Std.Live`, `Std.Tui`, `Std.Jobs`, the
  kernel-only `Sky.Http.Server` verbs) are hand-curated in
  `rust/crates/project/src/kernel_api.rs`; the `kernel_api_covers_registered_kernel_functions`
  gate fails CI on drift.
- **Release = write the notes, then the version claims follow.** `CHANGELOG.md`'s
  newest `## vX.Y.Z` heading is the single source of truth for "what version is
  this". Files that state the CURRENT line — `README.md`'s status banner and this
  file's "Current line" — are checked against it by
  `rust/crates/xtask/tests/docs_state_the_current_version.rs`, so a release that
  forgets them goes red. (`README.md` sat on "v0.19.x release candidate" through
  the whole v0.20 line before that gate existed: the first thing a reader or an
  agent learns about the project, a full minor out of date.) Historical mentions
  ("v0.17 closed …", "shipped in v0.16.6") are facts about the past and are
  deliberately NOT rewritten.
- **Live docs — examples can't rot.** `docs/` is *only* live reference; frozen
  per-version roadmaps and legacy material live under `docs/history/` (excluded
  from gating). `scripts/doc-examples.sh` is the live-docs gate: it `sky check`s
  every full-module (`module Main …`) Sky example in the live docs, so a doc
  example that stops compiling fails CI. Run it after touching docs; opt an
  intentionally-erroring example out with a `-- doc-example: skip` line. The
  stdlib API itself never drifts — it comes from `sky doc <Module>`, generated
  from source.

## Active limitations (verified against HEAD)

Real current constraints you must work around:

1. No higher-kinded types (HM only). 2. No `where` clauses (use `let`).
3. No custom operators.

Most historical limitations (negative-literal args, `Dict.toList` typed keys,
interface satisfaction, zero-arg call arity, recursive-list-op stack growth,
multi-line signatures, head-position alias unfolding, mixed-type SQL params,
`Maybe` SQL params) are **closed** in v0.17–v0.19. The authoritative,
version-tracked list is `docs/KNOWN_LIMITATIONS.md` — check it, not memory,
before assuming a limitation still holds.

## Deep dives

`sky doc <Module>` is the live API. These docs are the design + semantics behind it:

| Topic | Doc |
|---|---|
| Full stdlib reference | `docs/stdlib.md` (+ `sky doc <Module>`) |
| Language + errors | `docs/language/`, `docs/errors/` |
| Compiler architecture (Rust, primary) | `docs/rust-rewrite/` |
| Change verification / edge cases | `docs/rust-rewrite/13-change-verification-and-edge-cases.md` |
| Runtime narrowing — origins, levers, **the floor authority** | `docs/rust-rewrite/14-runtime-narrowing-taxonomy.md` |
| Stdlib correctness (algebraic laws, invariants) | `docs/architecture/sky-stdlib-correctness.md` |
| Sky.Live runtime + architecture | `docs/skylive/overview.md`, `docs/skylive/architecture.md` |
| Sky.Spa — client-side TEA in wasm | `docs/skyspa/overview.md` (design: `docs/skyspa/design.md`, `docs/skyspa/auto-split.md`) |
| `Std.App` — one builder, one `--target` | `docs/skyapp/overview.md` (design: `docs/design/unified-app-builder.md`) |
| `Std.Ui` layout DSL | `docs/skyui/overview.md` |
| Sky.Tui / Sky.Webview | `docs/skytui/`, `docs/skywebview/` |
| `Std.Auth` | `docs/skyauth/overview.md` |
| `Std.Db` / Codec / Store / migrations | `docs/skydb/overview.md` |
| CLI + LSP | `docs/tooling/cli.md`, `docs/tooling/lsp.md` |
| `sky.toml` + env vars | `docs/sky-toml.md` |
| Observability / console | `docs/observability.md` |
| Getting started | `docs/getting-started.md`, `README.md` |

## The test corpus — two layers, and where `examples/` now sits

Three bodies of code carry regression duty, and they are not interchangeable.
Which one a change belongs in is decided by what the change can break.

| Layer | Where | What it is for |
|---|---|---|
| **Layer 1** — the combinatorial corpus | `corpus/manifest.toml` + `xtask corpus` | Systematic VARIATION of language + type shapes, generated, expected values *constructed* by the generator. Membership is the manifest; no gate calls `read_dir`. Every historical defect is a pinned coordinate whose distance-1 neighbourhood is expanded automatically. |
| **Layer 2** — real-world projects | `apps/manifest.toml` (`apps/ledger`, `apps/relay`, `apps/fieldbook`, `sky-bundled/`, `examples/13-skyshop`, `rust/crates/sky/tests/*_flow.rs`) | Surfaces exercised IN COMBINATION as a user would build them: session/SSE/CSRF lifecycle, a real SQL engine (**the only Postgres coverage in the repo**), multi-replica topology, cross-backend `Std.Ui`, the CLI verbs. The class Layer 1 is structurally blind to. |
| **`examples/`** | `examples/00`–`examples/55` | **Documentation samples** — and the standing corpus for the *compiler-facing* ratchets that key on them. |

### The `examples/` contract

`examples/` **keeps its path** (renaming would break every doc link for no gain)
and changes its **contract**:

- **Primary role: documentation.** An example exists to be *read*. It shows one
  coherent thing a user would build, and it is referenced from `docs/`.
- **Retained regression role: the compiler-facing ratchets only.** `roundtrip`,
  `infer`, `shared-world`, `coerce-floor` and the stdout goldens key on
  `examples/`, and those keys are load-bearing — `coerce-floor` locks a
  runtime-narrowing floor per project and the goldens pin whole-program stdout.
- **Product-facing regression duty moved to Layer 2.** "Does the session survive
  an idle SSE hold", "does `liveInto` actually deliver on Postgres", "does the
  app refuse to start when its shared store is unreachable" are asserted by
  `apps/*` gates, not by an example that happens to use the feature.
- **An example is not deleted to make CI faster.** It is deleted only when the
  coverage ledger shows every surface it owns is owned elsewhere. Several
  examples are the *sole* owner of a stdlib module or a `sky.toml` surface;
  `xtask coverage-ledger` generates that table and `--check` fails if a surface
  gets weaker without an accounted `[[weakening]]`.

`00-standard-libs` is the stdlib smoke test; `13-skyshop` the Stripe-SDK-scale
FFI benchmark (76k symbols) and Layer-2 member D; `19-skyforum` the canonical
multi-module Sky.Live form; `26-ui-showcase` every `Std.Ui` primitive. Each
builds clean from a wiped slate (`rm -rf sky-out .skycache .skydeps && sky
build`). Read the example closest to what you're building first.

### Coverage ledgers — three files, all generated

`docs/coverage/` is the accounting, and **no document, ledger or verdict may
quote a number a script did not produce**:

- `denominators.json` — `xtask denominators`. How much surface exists. A
  DECREASE fails unless `removals.toml` accounts for it with a `[[removal]]`.
- `ledger.json` / `ledger.md` — `xtask coverage-ledger`. How strongly each
  surface is covered, today versus under the new architecture, plus the
  mechanically generated sole-ownership table. A surface getting **weaker**
  fails unless `removals.toml` accounts for it with a `[[weakening]]`.
- `falsifier-proofs.json` — `xtask harness --verify-falsifiers`. Which gates
  have been proven able to fail, and when.

## Project layout

```
rust/                     Sky compiler (Rust, PRIMARY — cargo workspace)
  crates/{syntax,hir,ty,lower,codegen,ffi,fmt,sky-lsp,project,xtask,sky}
legacy-haskell-compiler/  retired Haskell compiler (differential oracle)
runtime-go/rt/            Go runtime (embedded)
sky-stdlib/               Sky-side stdlib (embedded)
sky-bundled/              Sky Console + doc-server mini-apps
templates/                `sky init` project templates (CLAUDE.md / AGENTS.md)
examples/                 example projects
docs/                     user + contributor documentation
```
