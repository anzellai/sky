# AGENTS.md — Sky Language Project

This is a [Sky](https://github.com/anzellai/sky) project: a pure functional,
Elm-family language that compiles to typed Go and ships as a single `sky` binary
(you also need Go 1.21+). **If it compiles, it works** — every side effect returns
`Task Error a`, every fallible value returns `Result Error a`, and `sky check`
runs `go build` on the emitted Go, so shape mismatches surface at check time. No
runtime panics from well-typed code, no nil leakage, no silent numeric coercion.

Type annotations are load-bearing: `f : String -> Int -> Result Error Profile`
rejects a body that would infer wider. Inline records aren't allowed in
signatures — give any record in a signature a `type alias`.

## The full API lives in `sky doc` — not here

This file is orientation only. For **complete, always-current signatures + docs**:

```sh
sky doc --list            # every module (stdlib + your project), incl. Std.Live etc.
sky doc Std.Ui            # one module's exported bindings, with types + summaries
sky doc --serve           # browsable HTML API at http://localhost:8080
```

Reach for `sky doc <Module>` whenever you need an exact signature. Do not
memorise or inline signatures here — they drift; `sky doc` doesn't.

## Choose the app shape first

| You want… | Use | Entry |
|---|---|---|
| Web app (forms, real-time, UI state) | **Sky.Live** | `Live.app (Live.config {…})` |
| Client-rendered SPA / cross-platform client loop | **Sky.Spa** | `Spa.app (Spa.config {…})` |
| HTTP/JSON API (no browser UI) | **Sky.Http.Server** | `Server.listen 8000 [...]` |
| Terminal UI | **Sky.Tui** | `Tui.app (Tui.config {…})` |
| Desktop app (macOS) | **Sky.Webview** | `Webview.app { … }` (closed record) |
| One-shot CLI / cron | **Sky.Cli** | `main = Task.run cmd` |

Before scaffolding more than a proof of concept, confirm with the user:
**persistence** (SQLite default / Postgres for multi-instance / none), **auth**
(`Std.Auth` / OAuth / external), **session store** for Sky.Live (memory dev /
sqlite single-instance / redis|postgres for multi-replica), and **deploy target**.

## Std.Ui is the default for application interfaces

Build UI with **`Std.Ui`** — a typed, no-CSS layout DSL (`row`/`column`/`el` +
typed attributes from `Background`/`Border`/`Font`/`Input`/`Region`). It
HTML-escapes everything and renders identically across Sky.Live, Sky.Tui, and
Sky.Webview. Reach for `Std.Html` only to wrap raw markup. Never write CSS
strings; never emit raw HTML/JS (`data-sky-eval` is forbidden).

```elm
import Std.Ui as Ui
import Std.Ui.Font as Font

view model =
    Ui.layout []
        (Ui.column [ Ui.spacing 12, Ui.padding 16 ]
            [ Ui.el [ Font.size 24, Font.bold ] (Ui.text model.title)
            , Ui.button [] { onPress = Just Save, label = Ui.text "Save" }
            ])
```

The `<main>` landmark element is `Std.Html.mainNode` (not `main`, which would
collide with your program's `main` entry point). Prefer `Std.Ui.Region` for
landmarks anyway.

## Pinned defaults (apply unless the user overrules)

| Concern | Default |
|---|---|
| UI | `Std.Ui` (typed, no CSS). `Std.Html` only for raw markup. |
| Sky.Live navigation | Every internal link is `sky-nav` (`Attr.attribute "sky-nav" ""` on `<a>`). ONE persistent SSE per session; a plain `<a href>` full-reload opens a fresh SSE each page and can freeze the tab. |
| Auth | `Std.Auth` — bcrypt + HS256 JWT cookies. `Auth.login` / `Auth.register` return `Task Error Int` (the user id). Never `fmt`-print a secret. |
| Password forms | `Ui.form [Ui.onSubmit DoSignIn]` with a typed record arg. Never per-keystroke `onInput` on a password field. |
| DB | Records → `Std.Db.Store` + `Std.Codec` (one codec drives JSON **and** dialect-safe DB). SQLite for prototypes, PostgreSQL for multi-instance. Schema via committed file migrations (`sky db migrate --gen`). See **Database** below for the layer choice + the `sky doc` API source of truth. |
| Serialization | `Std.Codec` — **the portable default** for turning a record into JSON and back. ONE codec (`Codec.auto blank`) gives you `Codec.toJson` / `Codec.fromJson` AND, if you persist it, the dialect-safe DB mapping — from a single definition, with no drift between your JSON and DB shapes. Same codec works on every backend (Sky.Live / Http.Server / Cli). Reach for raw `Sky.Core.Json.Encode` / `Sky.Core.Json.Decode` only for a JSON shape a codec can't express — a custom/legacy wire format, or decoding third-party JSON you don't own (there, a hand-written `Decoder` + `Decode.decodeString` is right). |
| Money / decimals | `Std.Money` on `Std.Decimal`. Never raw `Float` for currency. |
| Concurrency | `Cmd.batch` / `Task.parallel`; in-process pub/sub via `Cmd.publish` + `Sub.subscribeTopic`. |
| Errors | `Result Error a` / `Task Error a`. Never `String` as the error type. |
| Logs | `Std.Log` structured logs; `/_sky/console` auto-mounts in dev. |
| Product analytics | `Std.Analytics` — typed events (`Money`/`Pii` props), consent **`Granted` by default** (privacy apps downgrade via a banner + `setConsent`), opt-in Sky.Live auto page-views (`Live.withAnalytics { pageViews = True }` + `Live.withAnalyticsIdentify (\model -> Maybe String)` to attribute the signed-in user — `Just id` identifies, `Nothing`/`Just ""` un-identifies so a signed-out session reverts to anonymous), SQLite/Postgres store + Sky Console **Analytics** tab. Query/aggregate stored events with the typed `Std.Db.Store` API — `Analytics.eventsStore : Store AnalyticsEvent` + `Analytics.openStore : () -> Task Error Db` (`recentEvents` returns `List AnalyticsEvent` typed rows, not `List String`). |

## Database

**For any function's exact signature + docs, run `sky doc <Module>`** (e.g.
`sky doc Std.Db.Store`, `sky doc Std.Codec`, `sky doc Std.Db.Schema`,
`sky doc Std.Db`). That is always current with your compiler — prefer it over
memorised signatures. This section covers *which layer to reach for*, not the API.

**Pick the highest layer that fits** (they compose — mix freely):

| Your need | Layer | Why |
|---|---|---|
| Records with straightforward CRUD (**default**) | `Std.Db.Store` + `Std.Codec` | ONE codec per type drives JSON encode/decode **and** dialect-safe DB read/write/schema — no hand-written row mappers. |
| Records, JSON not needed | `Std.Db.Table` | Reflection record↔row mapper (camelCase↔snake_case), no codec to write. |
| Explicit schema / DDL only | `Std.Db.Schema` | Typed, dialect-safe `CREATE TABLE` — one definition, correct on SQLite AND Postgres (no `INTEGER`-millis-overflow / `AUTOINCREMENT`-vs-`BIGSERIAL` drift). Reach the migration tooling with `db = Schema.toProject allTables` (`Store.Project` for `sky db push` / `migrate --gen`; tables/PK/UNIQUE/DEFAULTs carry, indexes stay on `createSchema`). |
| Joins, aggregates, transactions, custom SQL | `Std.Db` — `query` / `exec` / `withTransaction` + `SqlValue` | The escape hatch the mappers don't model. |

### Default — `Store` + `Codec`

```elm
import Std.Codec as Codec exposing (Codec)
import Std.Db.Store as Store exposing (Store)

type alias Product =
    { id : String, name : String, priceMinor : Int, tags : List String }

products : Store Product
products =
    Store.fromCodec "products"
        (Codec.auto { id = "", name = "", priceMinor = 0, tags = [] })
        |> Store.primaryKey "id"

-- write:  Store.insert conn products p  ·  Store.insertMany conn products [p1, p2]
--         Store.update conn products p (by PK)  ·  Store.upsert conn products p
--         Store.delete conn products "id" "p1"  ·  Store.deleteWhere conn products cond
--         Store.updateWhere conn products cond p  (compound / ownership WHERE)
-- patch:  Store.setFields conn products pk [("stock", SqlInt 5)]  (by PK — only named cols)
--         Store.updateFields conn products cond [("status", SqlString "sold")]  (by Cond)
--         Store.adjust conn products cond [("stock", -qty)]  (atomic SET col = col + delta)
-- read:   Store.all conn products  ·  Store.findBy conn products "id" "p1"
--         Store.selectRaw conn projCodec "<any JOIN / GROUP BY SQL>" params
```

`Codec.auto` columns (and JSON keys) are **snake_case** by default — `priceMinor`
→ `price_minor`, the DB convention — so a plain `Codec.auto blank` works against a
standard schema (`Codec.autoCamel` keeps camelCase); use an explicit
`Codec.field "col" .field` codec only for a custom name.

**Schema/DDL builders** pipe onto the store (each accepts the record field OR the
snake column): `serial "id"` (auto-increment PK) · `unique "email"` ·
`defaultNow "created_at"` · `defaultText/defaultInt/defaultBool "col" v` ·
`touchOnUpdate "updated_at"` (DB-stamped on insert AND auto-bumped to `now()` on
every update) · `defaultWith "id" (\_ -> SqlValue)` (app-computed default, e.g. a
UUID PK) · `generated [ "id", "created_at" ]` (columns `insert`/`update` omit so
the DB fills them). `Store.create conn store` emits the dialect-correct DDL;
`Store.upsert` is INSERT … ON CONFLICT DO UPDATE (idempotent config rows).

**JOINs / aggregates** → `Store.selectRaw codec "<SQL>" params`: you write the SQL
(joins, `GROUP BY`, `COUNT`), a codec decodes each row into a typed projection
record. Store stays a single-table mapper — deliberately not an ORM. Exact
signatures: `sky doc Std.Db.Store` / `sky doc Std.Codec`.

**Compose reads with the query builder** — filters bind as `SqlValue` params
(injection-safe), so you never touch a SQL string:

Conditions are composable `Cond` values — leaves (`eq`/`neq`/`gt`/`gte`/`lt`/`lte`/
`like`/`isNull`/`notNull`/`inList`) combine with `and_`/`or_`/`not_`, applied with
`where_` (multiple `where_` AND together). Values bind as `SqlValue` params
(injection-safe) — `import Std.Db exposing (SqlValue(..))` for unqualified
`SqlInt`/`SqlString`/`SqlBool`. Column names accept EITHER the record field
(`"priceMinor"`) OR the snake column (`"price_minor"`) — the builder resolves
it — and a typo fails fast with the actual column list before touching the DB:

```elm
Store.query products
    |> Store.where_ (Store.eq "active" (SqlBool True))
    |> Store.where_                                 -- grouped OR, AND'd with the rest
        (Store.or_ [ Store.gt "priceMinor" (SqlInt 5000)
                   , Store.eq "category" (SqlString "sale") ])
    |> Store.orderAsc "sortOrder" |> Store.orderDesc "createdAt"
    |> Store.limit 20 |> Store.offset 0
    |> Store.toList conn                            -- terminal: toList / toMaybe / count

-- filter by a TYPED value (enum / Money / Time / Codec.map) via its codec — no
-- manual SqlString/SqlInt, no drift from how the column is stored:
Store.query orders
    |> Store.where_ (Store.eq "status" (Store.sqlOf statusCodec Shipped))
    |> Store.toList conn

-- transactions stay in the Store namespace (alias over Db.withTransaction);
-- Store ops compose by taking the `tx` handle:
Store.transaction conn (\tx ->
    Store.insert tx orders o
        |> Task.andThen (\_ -> Store.insert tx orderItems i))
```

Only drop to raw **`Std.Db`** (`query` / `exec` / `withTransaction` + `SqlValue`)
for joins / aggregates / CTEs the builder doesn't model. **Import both qualified**
(`import Std.Db.Store as Store` + `import Std.Db as Db`) — never `exposing (..)` on
both, since `query`/`migrate` overlap; qualified is the intended usage.

`Codec.auto blank` derives the codec from a **zero-value witness** record:
scalars → columns, `Maybe` → nullable, `List`/nested-record → JSON blob, nullary
enums → readable names. **Data-carrying ADTs** need an explicit codec — build one
with `Codec.object`/`Codec.field`/`Codec.buildObject` (records) or
`Codec.taggedUnion`/`Codec.var0..3` (ADTs). Run `sky doc Std.Codec` for those.

### Schema migrations — committed files, no live DB needed

```bash
sky db init                    # scaffold db/migrations/ + db/schema.json
sky db migrate --gen add_stock # diff types vs snapshot → a committed migration file
sky db status                  # ✓ applied / ○ pending vs the live ledger
sky db migrate                 # apply committed migrations (dialect-correct, once each)
sky db seed                    # run the entry module's  seed : Db -> Task Error ()
sky db reset [table]           # empty data (all declared tables, or one); keeps schema + ledger — prompts / --yes
sky db drop [table]            # drop tables (all + ledger, or one); fresh "never migrated" state — prompts / --yes
```

### Running PostgreSQL in development

`Std.Db` is dialect-safe across SQLite and Postgres, but the gap is real —
`Codec.auto` cannot encode `Money`/`Decimal`, and there is no `NUMERIC` DDL
kind. If production is Postgres, develop on Postgres.

```bash
sky db start | stop | ps       # a per-project cluster on a unix socket
sky build --embed src/Main.sky # bundle PostgreSQL INTO the binary → ./sky-out/app --embed
```

Opt in with `[database] embedded = true` in `sky.toml`. **The app never knows
which tier it is in** — it consumes a DSN (`<PREFIX>_DB_PATH`, or
`DATABASE_URL`); only the provisioner changes. `sky run` supervises the cluster
for you; in production an operator sets a DSN and the same binary just works.
Passing `--embed` *and* an explicit DSN is an error, not a precedence rule.

`sky db start` needs PostgreSQL binaries — `SKY_POSTGRES_BIN`, a provisioned
bundle, or a system install.

**Where embedded PostgreSQL can run.** `sky db provision --embed` fetches a
self-contained, **glibc**-linked bundle (`postgres-bundle-v18.6`) — it needs a
glibc userland with `libstdc++`, a **durable writable data dir**, and the run
user present in `/etc/passwd` (the runtime's created-user path handles the last):

| Target | Embedded PG |
|---|---|
| macOS / glibc-Linux laptop · EC2 / GCE / any glibc Linux VM | ✅ (a VM's persistent disk is ideal) |
| Container: `debian-slim` / `distroless-base` **+ a mounted volume** | ✅ |
| Container: **Alpine** (musl) or **`FROM scratch`** | ❌ needs a musl/static build |
| **Cloud Run** | ⚠️ warm single instance **+ a mounted volume** only |
| **AWS Lambda / GCP Cloud Functions** | ❌ stateless/ephemeral → use managed PG |
| **Windows** (native) | ❌ use WSL2, or a system PostgreSQL via `DATABASE_URL` |

Rule of thumb: storage that must survive a restart and be single-owner → embedded
PG on a **VM or a container-with-a-volume**; stateless functions → **managed PG**
(Cloud SQL / RDS). Not glibc / not durable → the runtime refuses loudly (a "the
PostgreSQL binaries do not run" check), it does not corrupt or vanish data.

Expose a `db : Store.Project` binding for `--gen`:
`db = Store.project [ Store.toTable products, Store.toTable orders ]`, from
`module Main exposing (main, db, seed)`. On deploy `sky build` embeds the
migrations, so `SKY_DB_OP=migrate ./app` self-migrates with no source tree.

**Connection** comes from config — `SKY_DB_PATH` (SQLite) or `DATABASE_URL`
(Postgres), or `[database]` in `sky.toml`; `Db.connect ()` reads them. The handle
is a **memoised top-level value** (`db = Task.run (Db.connect ())`) — never in Model.

### A local PostgreSQL, so dev runs what production runs

```bash
sky db start        # initdb on first use + start; already running is a no-op
sky db ps [--all]   # this project's cluster, or every one on the machine
sky db stop [--all] # pg_ctl stop -m fast
```

One cluster per project in `.skydata/pg/` (gitignored), on a **unix socket** —
no port to race over and nothing on the network. `sky db start` prints the DSN
to put in `DATABASE_URL`. Tuned small (`shared_buffers = 32MB`), so an idle
project cluster costs tens of megabytes. Needs PostgreSQL on `PATH`, or point
`SKY_POSTGRES_BIN` at a `bin` directory holding `initdb`/`pg_ctl`/`postgres`.

Worth reaching for the moment the app is headed for Postgres in production:
developing on SQLite and deploying on Postgres is how dialect differences reach
users. The app code does not change — only the DSN does.

## Effect boundary — Task everywhere

Every observable side effect returns `Task Error a` (`File.*`, `Http.*`, `Db.*`,
`Time.now`, `Random.*`, `Log.*`, `System.*` except `getenvOr`). Pure stays bare
(`String.*`, `List.*`, `Crypto.sha256`); fallible-pure returns `Result e a` /
`Maybe a` (`String.toInt`, JSON decoders). A discarded `let _ = TaskExpr` is
auto-forced. Bridge with `Task.fromResult` / `Result.andThenTask` /
`Task.onError`. Top-level `apiKey = System.getenv "K" |> Task.run |> Result.withDefault ""`
still needs the explicit `Task.run`.

**Top-level bindings are memoised — evaluated once, then cached.** A
zero-parameter top-level binding is a single VALUE: `apiKey` reads the env
once, `db = Task.run (Db.connect ())` opens ONE shared connection pool. If
you need a FRESH value per use — a UUID, the current time, a random number
— make it a function, not a binding: `newId : () -> String; newId _ =
Task.run Uuid.v4 |> Result.withDefault ""`, called `newId ()`. `newId =
Task.run Uuid.v4` at top level freezes to one UUID forever (the compiler
warns). A bare `x = Uuid.v4` (an un-forced `Task` value) is fine.

Two-level error pattern: log a structured line with a short `errId` server-side,
return a user-facing `Task.fail (Error.unexpected ("... ref " ++ errId))`.

## Language syntax

```elm
module Main exposing (main)
import Sky.Core.Prelude exposing (..)
import Std.Log exposing (println)

type Msg = Increment | Decrement

update : Msg -> Int -> Int
update msg count =
    case msg of
        Increment -> count + 1
        Decrement -> count - 1

main = println (String.fromInt (update Increment 0))
```

`|>` `<|` pipelines · `::` cons · `\x -> x + 1` lambdas · `let…in` ·
`case…of` (exhaustiveness-checked) · `{ rec | field = value }` update ·
`import M as Alias exposing (name)`. Triple-quoted multiline strings support
`{{expr}}` interpolation (escape as `\{{`).

Every non-aliased `import M exposing (..)` also binds `M.<name>` as an
auto-qualifier. An `exposing` name that the module doesn't export is a hard
error (`[E1011] NOT EXPOSED`).

## Sky.Live essentials

`Live.app` takes a typed **builder** config (v0.19): `Live.config { init, update,
view, subscriptions, routes, notFound }` builds an opaque `AppConfig`; attach
optional fields with `withX` builders in a pipe:

```elm
import Std.Live exposing (app, config, route, withHead)

main =
    app
        (config
            { init = init, update = update, view = view
            , subscriptions = subscriptions
            , routes = [ route "/" Home ], notFound = Home
            }
            |> withHead headFor      -- optional; also withGuard/withAnalytics/withStatic/…
        )
```

**Migrating an older project:** if you see the pre-v0.19 record literal
`Live.app { init = …, update = …, …, head = … }` (or `Tui.app` / `Tui.program` /
`Cli.program`), migrate it — that form is REMOVED and won't compile. Keep the six
required fields (`init`/`update`/`view`/`subscriptions`/`routes`/`notFound`) inside
`config { … }`, and move every OPTIONAL field to a `|> withX` in the pipe
(`head` → `withHead`, `guard` → `withGuard`, `analytics` → `withAnalytics`,
`onKey` → `withOnKey`, `onLine` → `withOnLine`, …); add `config` + the `withX`
names you use to the `exposing (…)` list. The compiler error for the old form
prints this same recipe. `Webview.app` keeps its closed record. Full guide:
`docs/v0.19/migration-builder-cfg.md`. Same pattern for `Tui.app` /
`Tui.program` (`Tui.config` + `withOnKey`) and `Cli.program` (`Cli.config` +
`withOnLine`). `init` runs
per-session (a reload restores Model from the store; it does NOT re-run `init`).
`init` receives a `req` with `path` / `query` / `params` / `method` / `headers` /
`cookies`. `update msg model` returns `(Model, Cmd Msg)`; `Cmd.perform task ToMsg`
runs a task in a goroutine and dispatches the result back over SSE.

Wire-event args: text/select → `[value : String]`; number/range → `[Float]`;
checkbox → `[Bool]`; submit → `[formData]` (a `Dict String String` or a typed
record alias); keydown → `[key : String]`. Radios: one `onClick (Choose v)` per
label, not `onInput`.

Password forms use `onSubmit` with a typed record (`DoSignIn AuthCreds`), never
`value=`/`onInput` on the password input — so the secret never enters the Model
or the session store, and password managers don't re-prompt.

## Commands

```sh
sky init [name] [--production]  # new project — SQLite default; --production = Postgres one-DB + docker-compose
sky build src/Main.sky       # compile → sky-out/app
sky run src/Main.sky         # build + run   (--profile for runtime CPU/mem/hang profiling)
sky check src/Main.sky       # type-check + go build (keeps no binary — but DOES compile)
sky verify                   # one-shot project gate: fmt + check + build + tests
sky test tests/MyTest.sky    # Sky.Test runner (SKY_TEST_JSON=<path> also writes a per-case JSON report)
sky fmt src/Main.sky         # format (always run after editing .sky)
sky doc <Module> | --list    # API docs (the source of truth for signatures)
sky watch src/Main.sky       # rebuild + restart on save
sky add <go/pkg> | remove | install | update   # Go FFI deps
```

Run `sky verify` before you consider a change done — it runs fmt-clean +
type-check + production build + every `tests/*.sky` suite, and exits non-zero on
any failure.

**`sky check` is not a cheap tier.** It is `sky build` minus keeping the
artifact: both invoke `go build` on the emitted Go. Do not design a "fast
check-only" CI step around it — there is no saving to collect, and assuming
there was is how a whole test-architecture proposal got built on a false
premise.

**Put every suite where your runner will find it.** A test file nothing runs is
worse than no test: it reads as coverage and asserts nothing, and it rots
silently until the day someone tries to run it. If you script your own suite
runner, discover suites **recursively** — a flat `tests/*Test.sky` glob hid 22
suites and ~280 assertions in this project's own history, every one of which had
stopped compiling by the time anyone noticed.

## sky.toml

```toml
name    = "myapp"
version = "0.1.0"
entry   = "src/Main.sky"
bin     = "app"          # output binary name

[source]
root = "src"

[live]                   # Sky.Live apps
port  = 8000
store = "sqlite"         # memory | sqlite | redis | postgres
ttl   = 1800

[database]               # persistence
driver = "sqlite"
path   = "app.db"

# There is no [auth] section — Std.Auth is a library. signToken takes the
# secret + TTL as arguments; SKY_AUTH_TOKEN_SECRET (≥32 bytes) comes from the
# environment, never sky.toml. A residual [auth] key warns and does nothing.

[log]                    # structured logging
format = "plain"         # plain (dev) | json (production)
level  = "info"          # debug | info | warn | error
```

### How to pick — and what to change as you grow

Only `name` / `version` / `entry` are required; add a section **only when you use
that feature**. Config precedence is **process env > `.env` > `withX` builder
calls in code > `sky.toml`**, so every value here can be overridden at deploy
time without editing the file (and secrets / connection strings should be —
never commit them). An explicit builder call (`Live.withPort`, `Live.withStore`,
`Live.withStorePath`, `Live.withTtl`, `Live.withIdleEvict`) beats the `sky.toml`
seed but still loses to the operator's environment.

Cross-cutting config (log, database, sessions, jobs, csrf, telemetry) also has a
typed front door — a top-level `config` binding built with **`Sky.Config`**
`withX` builders (`config = Config.default |> Config.withDatabase (…) |> …`),
resolving through the SAME precedence (operator env still wins). It includes
telemetry **storage** tuning — `withTelemetryAggregationWindow` /
`withTelemetryHistogramWindow` (coalesce metric rows to cut DB growth) and
`withTelemetryDbCapacity` (the size-report "near full" flag). Run
`sky doc Sky.Config` for the full set; each builder names its `SKY_TELEMETRY_*`
env override.

**Persistence tier by traffic (the quick call — pick one, set `[live] store` +
`[database]` to match):**

| Scale | Sessions + data | Why |
|---|---|---|
| Single instance, **low–medium** traffic | **`sqlite`** | One local file — sessions + app data on one host, survives restart, zero external services. The right default for a prototype or a single VM. |
| Production, **medium–heavy** traffic | **`postgres`** | Shared across replicas → run several instances behind a load balancer; one managed database for durable data + sessions. |
| Production, **global / heavy** traffic | **`postgres` + `redis`** | Postgres for durable data + sessions; Redis adds the cross-instance pub/sub broker (broadcast to users across replicas — chat/collab/presence) and a fast session cache. Set `store = "redis"`, or keep Postgres sessions and point pub/sub at Redis in code with `Sky.Config.withLiveBroker "redis://host:6379"` (or the `SKY_LIVE_BROKER_URL` env, which still wins). |

The app code is identical across all three — you change only the config. `memory`
is dev-only (per-process, lost on restart).

- **`[live] store`** — where Sky.Live keeps session state. Start `memory` (dev;
  lost on restart). One instance → `sqlite` (a local file; survives restart).
  More than one replica → `postgres` or `redis` (shared across instances; `redis`
  also gives the cross-instance pub/sub broker you need for chat / collab /
  same-user-two-devices). **Your app code never changes — only this value** (or
  `SKY_LIVE_STORE`). `memory` and `sqlite` are both single-instance.
- **`[live] ttl`** — session lifetime in seconds (idle sessions slide on activity).
- **`[live] port`** — dev port; a deploy typically sets `SKY_LIVE_PORT` instead.
- **`[database]`** — your APPLICATION data (separate from sessions). `sqlite` for a
  prototype / single host; `postgres` for production / multiple instances. Leave
  the real connection string to `DATABASE_URL` (env), not the committed file.
- **No `[auth]` section** — `Std.Auth` is a library, not a config layer, so
  there is nothing to seed. The inert block was deleted; a residual `[auth]`
  key now warns. Configure it from code: `Auth.signToken` takes its secret + TTL
  as arguments, so pass the secret from your own environment variable
  (`SKY_AUTH_TOKEN_SECRET`, ≥32 bytes) rather than expecting the file or the
  runtime to supply it.
- **`[log]`** — `plain`/`info` while developing; `json`/`warn` in production (JSON
  logs are what a log aggregator ingests).

**Going to production — set these via env (not the file):** `ENV=production`
(locks the dev console + banner off, gates `/_sky/metrics` behind auth),
`SKY_CONSOLE_AUTH` with `SKY_CONSOLE_TOKEN`; a SHARED `SKY_LIVE_STORE`
(`redis`/`postgres`) **and** load-balancer sticky sessions keyed on the `sky_sid`
cookie if you run more than one replica.

## Non-negotiables

- **Types over strings for errors** — `Result Error a` / `Task Error a`, never `Result String a`.
- **No raw HTML/JS** — `Std.Ui` escapes everything; `data-sky-eval` is forbidden.
- **Secrets are typed** — `Auth.signToken`/`verifyToken` take `String`; never `fmt.Sprintf("%v", secret)`.
- **Money is `Std.Money`**, never `Float`.
- **`sky fmt` after editing**, **`sky verify` before shipping.**
- **Production gate**: set `ENV=production`, and `SKY_CONSOLE_AUTH` (`token` or `app`) with `SKY_CONSOLE_TOKEN`; use a shared session store (redis/postgres) + sticky sessions when you run more than one replica. `SKY_AUTH_TOKEN_SECRET` is **not** a runtime setting — nothing in the runtime reads it (`sky_sid` is unsigned random hex, and `Auth.signToken` takes its secret as a Sky-level *argument*). It is a convention in your own code that only `sky doctor` knows about: if you use `Std.Auth`, whatever variable you feed into `Auth.signToken` must be ≥ 32 bytes; if you don't, setting it changes nothing.
- **Sky.Live resilience (automatic)**: an explicitly-configured `store` (postgres/sqlite/redis) that can't connect at boot **fails loud in production** (the app refuses to start) instead of silently using memory — so make sure `DATABASE_URL` is reachable, or set `SKY_LIVE_STORE=memory` to opt in to in-memory sessions. `/_sky/readyz` returns 503 when the store/DB is down. Keep `view` a **pure** function of the model (no `Time.now`/`Random` in `view`); enable `SKY_LIVE_VIEW_DETERMINISM_CHECK=1` in dev to catch violations.

When a signature or module is unclear, run `sky doc <Module>` — it is complete
and current. This file is not.
