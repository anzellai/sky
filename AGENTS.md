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
byte-for-byte. Current line: **v0.20.x**.

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
   | Minimal Linux | ~250 MB |
   | Sky app binary (Go, idle) | ~30–40 MB |
   | PostgreSQL base (`shared_buffers = 32MB`) | ~36 MB |
   | PG backends, ~5–10 MB each | ~40–70 MB at 6–10 active |
   | Sky.Live sessions, ~10–100 KB each | ~10 MB at 200 concurrent |
   | **Total** | **~390 MB** |

   Two things worth telling the user rather than letting them discover:
   **CPU, not RAM, is the binding constraint** — Sky.Live renders and diffs
   views server-side, so a burstable instance exhausts its baseline CPU
   allowance long before its memory, and a 0.25-vCPU-baseline instance is a
   demo host whatever its RAM says. And **a single instance has no replica**:
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
| HTTP/JSON API (no browser UI) | **Sky.Http.Server** | `Server.listen 8000 [...]` |
| Background job / cron | **Sky.Cli** | `main = Task.run work` |
| Terminal UI | **Sky.Tui** | `Tui.app cfg` |
| One-shot CLI | **Sky.Cli** | `main = Task.run cliCmd` |
| Desktop app | **Sky.Webview** | `Webview.app cfg` (macOS in v0.1) |
| WebSocket feed | **Sky.Http.Server.WebSocket** | `Server.upgrade req` |
| SSE / token stream | **Sky.Http.Server.Stream** | `Server.Stream.emit` |

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
| **Observability** | `Std.Log` structured logs; the dev console auto-mounts at `/_sky/console`; `OTEL_EXPORTER_OTLP_ENDPOINT` for an external collector. |
| **Sky.Live navigation** | Every internal link is `sky-nav` (one persistent SSE per session). Bare `<a href>` only to deliberately leave the app. |
| **Password forms** | `Ui.form [Ui.onSubmit DoSignIn]` with a typed record; never per-keystroke `onInput` on a password field. |
| **No raw HTML/JS** | `Std.Ui` HTML-escapes everything; `data-sky-eval` is forbidden. |

The app-shape details (Sky.Live TEA loop, routing, session lifecycle, forms,
`Std.Ui` layout, Sky.Tui, Sky.Webview) live in `docs/skylive/`, `docs/skyui/`,
`docs/skytui/`, `docs/skywebview/`. Read the relevant one before building.

### Production gate (when the user says deploy / prod / Cloud Run / K8s)

- `ENV=production` set on the runtime (locks the dev console + banner + metrics).
- `SKY_AUTH_TOKEN_SECRET` ≥ 32 bytes; `SKY_CONSOLE_AUTH` set (`token` or `app`).
- Multi-replica → a **shared** session store (`redis`/`postgres`), sticky
  sessions keyed on `sky_sid`, and cross-instance pub/sub (`store=redis` or
  `SKY_LIVE_BROKER_URL`). `memory` and `sqlite` are single-instance only.

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
sky add <go/module> | remove | install | update                # Go FFI deps
sky doctor [--fix] | upgrade | upgrade-claude | clean
```

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

> **Bundles are not published yet.** `sky db provision --embed` resolves a
> release built by `.github/workflows/postgres-bundle.yml`, and no
> `postgres-bundle-v*` tag has been cut — so it 404s until one is, unless
> pointed at a local bundle. `SKY_POSTGRES_BIN` or a system PostgreSQL works
> today for `sky db start`. Full design: `docs/skydb/embedded-postgres.md`.

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
- **Secrets are typed** — `Auth.signToken`/`verifyToken` take `String`, never `any`.
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
- **Timeout-bound every long command.** `cargo test` / xtask gates run under
  `timeout` (60 min ceiling). A test that hangs is a bug to bisect, not wait out.
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
| Stdlib correctness (algebraic laws, invariants) | `docs/architecture/sky-stdlib-correctness.md` |
| Sky.Live runtime + architecture | `docs/skylive/overview.md`, `docs/skylive/architecture.md` |
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
