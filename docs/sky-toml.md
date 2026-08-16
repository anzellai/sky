# `sky.toml` — project manifest reference

> **Status**: the Rust compiler (`rust/`, `cargo build --release -p sky`)
> is the primary Sky compiler; the Haskell compiler is preserved under
> `legacy-haskell-compiler/`. Verified by the example sweep + compiler test
> suite (`cargo test` + xtask gates). See
> [`compiler/versions.md`](compiler/versions.md) for the changelog.


Every Sky project has a `sky.toml` at its root. It declares
metadata, build settings, dependencies, and runtime defaults.
Created automatically by `sky init`; hand-edited as the project
grows.

The format is TOML — sections in `[brackets]`, key-value pairs
underneath, comments with `#`. Section order does not matter.

## Minimal example

```toml
[project]
name    = "my-app"
version = "0.1.0"
```

That's enough — every other field has a sensible default.

## All sections at a glance

| Section              | Purpose                                              |
|----------------------|------------------------------------------------------|
| `[project]`          | Name, version, entry file, output binary name        |
| `[go.dependencies]`  | Go packages to auto-bind via `sky add`               |
| `[dependencies]`     | Sky-source dependencies (other Sky projects)         |
| `[live]`             | Sky.Live runtime config (port, sessions, …)          |
| `[auth]`             | Std.Auth defaults (JWT secret, cookie, TTL)          |
| `[database]`         | Std.Db default connection (the DSN selects the driver) |
| `[log]`              | Std.Log default format and level                     |
| `[env]`              | Env-var namespace prefix (v0.11.5+)                  |
| `[security]`         | CSRF opt-out                                         |

Every key seeded into the runtime is **only applied when the
corresponding env var is unset**. So shell env / `.env` always wins
over `sky.toml`. Production deployments override config without
editing files.

---

## `[project]`

Project metadata. Top-level keys are also accepted (no
`[project]` header required) for compatibility with older
manifests.

```toml
[project]
name    = "my-app"        # used in error messages and the binary
version = "0.1.0"         # informational only
entry   = "src/Main.sky"  # default source file passed to sky build
root    = "src"           # source root for module resolution
bin     = "app"           # output binary name → sky-out/app
```

| Key       | Type   | Default          | Meaning                                |
|-----------|--------|------------------|----------------------------------------|
| `name`    | string | `"sky-project"`  | Project name (informational)           |
| `version` | string | `"0.1.0"`        | Semver (informational)                 |
| `entry`   | string | `"src/Main.sky"` | Default file for `sky build` / `run`   |
| `root`    | string | `"src"`          | Source-root prefix for module imports  |
| `bin`     | string | `"app"`          | Output binary name in `sky-out/`       |

---

## `[go.dependencies]`

Go modules to auto-bind into Sky. Each entry maps the Go module
path to a version pin (or `"latest"`). `sky add` writes here for
you; `sky install` regenerates bindings to match.

```toml
[go.dependencies]
"github.com/google/uuid"        = "v1.6.0"
"github.com/joho/godotenv"      = "v1.5.1"
"github.com/stripe/stripe-go/v76" = "v76.20.0"
```

Generated bindings land under `.skycache/ffi/` (Sky-side `.skyi`
files) and `.skycache/go/` (Go wrappers). Don't commit those —
they're reproducible from `sky.toml` + the imported source.

Use `sky remove <pkg>` to drop a dependency cleanly. See
[ffi/go-interop.md](ffi/go-interop.md) for the FFI model.

---

## `[dependencies]`

Sky-source dependencies — other Sky projects you want to import.
Path or git URL → version. Resolved into `.skydeps/` on
`sky install`.

```toml
[dependencies]
"github.com/anzellai/sky-stripe" = "v0.2.1"
```

Less commonly used than Go deps; most reusable code in the
ecosystem ships as Go modules so existing `go.mod` projects can
consume them too.

---

## `[live]`

Sky.Live (server-driven UI) runtime config. Every key seeds an
env-var default at startup, namespaced by `[env] prefix`
(default `SKY_`). These seeds sit BELOW an explicit `Live.withX`
builder call in code, which in turn sits below the operator's
environment (shell or `.env`) — see [Precedence](#precedence).
See the [Sky.Live overview](skylive/overview.md) for the full
picture.

```toml
[live]
port         = 8000              # HTTP listener port
store        = "sqlite"          # session store: memory / sqlite / redis / postgres
storePath    = "./sessions.db"   # file path or connection URL
ttl          = 1800              # session TTL in seconds (30 min)
static       = "public"          # static asset directory served at /static
maxBodyBytes = 5242880           # cap for /_sky/event POST body (5 MiB)
```

| Key            | Env var                       | Default     | Meaning                                                    |
|----------------|-------------------------------|-------------|------------------------------------------------------------|
| `port`         | `<PREFIX>_LIVE_PORT`          | `8000`      | HTTP listener port                                         |
| `store`        | `<PREFIX>_LIVE_STORE`         | `memory`    | `memory` / `sqlite` / `redis` / `postgres`   |
| `storePath`    | `<PREFIX>_LIVE_STORE_PATH`    | (empty)     | sqlite file path, or `host:port` / `redis://…` / `postgres://…` URL |
| `ttl`          | `<PREFIX>_LIVE_TTL`           | `1800`      | Session TTL in seconds                                     |
| `static`       | `<PREFIX>_LIVE_STATIC_DIR`    | (empty)     | Static asset directory served at `/static`                 |
| `maxBodyBytes` | `<PREFIX>_LIVE_MAX_BODY_BYTES`| `5242880`   | Max `/_sky/event` POST body (bump for `Event.onFile` uploads)|

Postgres falls back to `DATABASE_URL` and Redis to `REDIS_URL`
when `storePath` is unset (Redis defaults further to
`localhost:6379`).

Connection-status banner config is env-only (not in sky.toml):
`<PREFIX>_LIVE_BANNER` (default `on`), `<PREFIX>_LIVE_RETRY_BASE_MS`
(default `500`), `<PREFIX>_LIVE_RETRY_MAX_MS` (default `16000`),
`<PREFIX>_LIVE_RETRY_MAX_ATTEMPTS` (default `10`),
`<PREFIX>_LIVE_QUEUE_MAX` (default `50`).

---

## `[auth]`

Std.Auth defaults. `Std.Auth` is a library, not a framework layer:
`signToken secret claims expirySeconds` takes the secret + TTL as
**arguments**, and the session cookie is set by your handler
(`Server.setCookie`). So these keys don't reconfigure the runtime
directly — each is **seeded into a `SKY_AUTH_*` env var that your
code reads** at the call site:

```elm
secret = System.getenvOr "SKY_AUTH_TOKEN_SECRET" "dev-secret"
ttl    = System.getenvOr "SKY_AUTH_TOKEN_TTL" "86400" |> String.toInt |> Result.withDefault 86400
cookie = System.getenvOr "SKY_AUTH_COOKIE" "sky_auth"

token  = Auth.signToken secret claims ttl
```

`tokenTtl` / `cookieName` / `driver` are seeded from `sky.toml`. **The secret
is not** — see the note below — so the first line reads an env var nothing
seeds, which is the point.

Production overrides via shell env / `.env` win over the sky.toml
seed (same precedence as every other key).

```toml
[auth]
secret     = "do-not-ship-this-default"
tokenTtl   = 86400             # 24 h
cookieName = "sky_auth"
driver     = "jwt"             # jwt / session / oauth
```

| Key          | Env var                       | Default      | Meaning                              |
|--------------|-------------------------------|--------------|--------------------------------------|
| `tokenTtl`   | `<PREFIX>_AUTH_TOKEN_TTL`     | `86400`      | JWT lifetime in seconds              |
| `cookieName` | `<PREFIX>_AUTH_COOKIE`        | `sky_auth`   | Session cookie name                  |
| `driver`     | `<PREFIX>_AUTH_DRIVER`        | `jwt`        | `jwt` / `session` / `oauth`          |

> **`secret` is NOT a sky.toml key.** It appears in the example block above only
> to be explicit that it does not work: the compiler deliberately refuses to
> seed a signing key from a file that is normally committed to source control.
> Set **`SKY_AUTH_TOKEN_SECRET`** in the environment (shell, `.env`, secret
> manager). A `secret = "…"` line in sky.toml is inert, and since v0.19.14 the
> build warns about it rather than ignoring it silently.
>
> **The name matters and this note used to give the wrong one.** It said
> `<PREFIX>_AUTH_SECRET`. The production gate reads the literal, unprefixed
> `SKY_AUTH_TOKEN_SECRET` (`rust/crates/sky/src/main.rs:3692`), and that is
> the name `sky init` writes into the generated `.env` (`main.rs:1300`).
> Nothing in the tree reads `SKY_AUTH_SECRET`, prefixed or not — so a reader
> who followed the old instruction would set a variable no one looks at and
> still fail the `ENV=production` gate.

Keys are **camelCase**. `session_ttl` is not `tokenTtl`; it is nothing, and two
examples in this repository shipped it for months advertising a 24-hour session
they never got. Any key in a runtime config section that Sky does not read now
produces a build warning naming the accepted keys.

---

## `[database]`

Std.Db default connection. `Db.connect ()` (unit form) reads
`<PREFIX>_DB_PATH` to find the database — set this here once and
all calls pick it up automatically.

**The driver is derived from the connection string, not configured.** A
`postgres://` / `postgresql://` URL (or a libpq `host=… user=…` DSN) opens
Postgres; anything else is a SQLite file path. That single rule is what the
runtime applies, and every dialect-specific behaviour downstream follows from it.

```toml
[database]
path   = "./app.db"        # sqlite file path or postgres URL → the driver
# url  = "postgres://…"    # alias for `path` (same DB_PATH)
driver = "sqlite"          # OPTIONAL assertion — must agree with the DSN above
```

| Key      | Env var                  | Default   | Meaning                                          |
|----------|--------------------------|-----------|--------------------------------------------------|
| `path`   | `<PREFIX>_DB_PATH`       | (empty)   | File path or connection URL — **selects the driver** |
| `url`    | `<PREFIX>_DB_PATH`       | (empty)   | Alias for `path` (postgres DSN)                  |
| `driver` | *(none)*                 | (unset)   | Optional consistency assertion; see below        |
| `embedded` | *(none)*               | `false`   | `sky run` supervises a local cluster and provisions the DSN — see [below](#embedded-postgresql) |
| `postgresVersion` | *(none)*        | (unset)   | The PostgreSQL `sky db provision --embed` fetched and `sky db start` prefers |

`driver` does **not** select anything. It is checked against `path`/`url` at
build time and a contradiction is reported — `driver = "postgres"` beside
`path = "./app.db"` warns that the app will open SQLite. To choose the engine at
run time, set the DSN (`SKY_DB_PATH` / `DATABASE_URL`), not a driver name.

> Before v0.19.9 this key emitted a `<PREFIX>_DB_DRIVER` env var that **nothing
> in the runtime ever read**, so a mismatched `driver` was silently ignored and
> the app quietly opened the other engine. The variable is no longer emitted.

### Connection pool (PostgreSQL)

**You should not need these.** The runtime sizes the pool from the deployment it
detects, and the defaults are chosen rather than inherited. Reach for them when
you know your server's `max_connections` budget and how many app instances share
it — that is a fact about your deployment which the app cannot see.

```toml
[database]
url             = "postgres://…"
maxOpenConns    = 12        # ceiling on simultaneous backends
maxIdleConns    = 12        # keep them; below open causes reconnect churn
connMaxLifetime = "30m"     # retire a connection so a failover heals
connMaxIdleTime = "5m"      # reap one that has gone quiet
```

| Key | Env var | Default (VM) | Default (serverless) |
|---|---|---|---|
| `maxOpenConns` | `<PREFIX>_DB_MAX_OPEN_CONNS` | 4 × CPU, clamped 4–32 | 2 × CPU, clamped 2–8 |
| `maxIdleConns` | `<PREFIX>_DB_MAX_IDLE_CONNS` | = `maxOpenConns` | = `maxOpenConns` |
| `connMaxLifetime` | `<PREFIX>_DB_CONN_MAX_LIFETIME` | `30m` | `30m` |
| `connMaxIdleTime` | `<PREFIX>_DB_CONN_MAX_IDLE_TIME` | `5m` | `60s` |

Durations accept Go syntax (`"30m"`, `"90s"`, `"1h30m"`) or a bare integer read
as seconds. `0` disables a limit.

**The sizing is deployment-aware because the right number is not a property of
the app — it is a property of how many copies of it there are.** On a VM the app
is one process. On request-billed serverless the platform runs many small
instances and each holds its own pool, so the per-instance number that is
conservative on a VM is a connection storm across fifty of them. The runtime
reuses the same `K_SERVICE` / `AWS_LAMBDA_FUNCTION_NAME` detection the telemetry
exporter uses to vary its flush cadence. Force it either way with
`SKY_RUNTIME_MODE=serverless` / `=vm`.

Two things follow from the serverless defaults that are worth knowing: the pool
is small, so an operator running high per-instance request concurrency (Cloud Run
defaults to 80) may genuinely need to raise `maxOpenConns` — and raising it is a
decision about the server's connection budget, which is why it is explicit rather
than automatic. And `connMaxIdleTime` is short, because a frozen instance keeps
its TCP connections and therefore the PostgreSQL backend *processes* behind them
alive while doing no work at all.

**These keys are PostgreSQL-only.** SQLite is pinned to a single connection by
its global writer lock — raising it reintroduces the `SQLITE_BUSY` class — so
setting them alongside a SQLite DSN logs a warning and changes nothing.

> Before v0.20.3 none of this existed: the runtime clamped SQLite and let
> PostgreSQL fall through on Go's `database/sql` defaults, under a comment
> asserting those defaults were "already sane". They are `MaxOpenConns = 0`
> (**unlimited**), `MaxIdleConns = 2`, and no connection lifetime — so a burst
> opened backends until PostgreSQL answered `FATAL: sorry, too many clients
> already`, and below that threshold the pool churned connections because only
> two stayed idle.

### Transaction isolation

```toml
[database]
isolation = "serializable"   # default: the driver's own level
txRetry   = 3                # default: 0 — read the warning below first
```

| Key | Env var | Default | Meaning |
|---|---|---|---|
| `isolation` | `<PREFIX>_DB_ISOLATION` | (unset) | Level `Std.Db.transaction` begins at |
| `txRetry` | `<PREFIX>_DB_TX_RETRY` | `0` | Retry budget for a `40001` / `40P01` conflict |

`isolation` accepts `read uncommitted`, `read committed`, `repeatable read` and
`serializable`, in any case and with spaces, hyphens or underscores. Unset means
the driver's own default, which on PostgreSQL is READ COMMITTED — **that is the
shipped behaviour and adding this key does not change it.** Raising the default
silently would start surfacing serialization failures to apps that have never
seen one.

> **`txRetry` requires a replayable transaction body — the runtime cannot check
> this for you.** Retrying a serialization failure means running the body AGAIN.
> A `Task` body may already have sent an email, charged a card or called a
> third-party API before the conflicting write was detected, and `ROLLBACK` undoes
> none of that: the database's half of the work is atomic, the outside world's
> half is not. Enable it only when every effect inside the body is either a write
> on the same transaction or genuinely idempotent. It is off by default for this
> reason.

Both keys are PostgreSQL-only. SQLite transactions already serialise on the
single pooled connection, so there is no weaker level to ask for and no `40001`
to retry; setting either alongside a SQLite DSN warns and changes nothing.

### Embedded PostgreSQL

```toml
[database]
embedded = true             # sky supervises a local cluster and provisions the DSN
postgresVersion = "18.6"    # written by `sky db provision --embed`
```

| Key | Env var | Default | Meaning |
|---|---|---|---|
| `embedded` | *(none — a toolchain key)* | `false` | `sky run` / `sky watch` start a per-project PostgreSQL and inject its DSN |
| `postgresVersion` | *(none — a toolchain key)* | (unset) | The PostgreSQL major/minor this project is developed against |

With `embedded = true`, `sky run` starts this project's cluster (`.skydata/pg/`,
a unix socket outside the project — see
[embedded PostgreSQL](skydb/embedded-postgres.md)) and hands the app
`<PREFIX>_DB_PATH`. The app is unchanged: it calls `Db.connect ()` and reads a
DSN, exactly as it does against a managed server. That is the point — the binary
never learns which tier provisioned its database.

The lifetime follows the verb. `sky run` is **ephemeral** and stops the cluster
when it exits, ref-counted so two concurrent runs do not stop each other's
database. `sky db start` is **persistent** and stays up until `sky db stop`,
including across a `sky run` that used it — that is the mode for running
`./sky-out/app` repeatedly.

Unlike every other key here, `embedded` and `postgresVersion` set no environment
variable. They are read by the `sky` toolchain, not by the app.

`postgresVersion` is written by `sky db provision --embed`, which fetches Sky's
own PostgreSQL build into `~/.sky/postgres/<version>/` (checksum-verified,
installed atomically) so the project needs no system PostgreSQL. The pin is not
decoration: binary discovery prefers the pinned version over a newer cached one,
so a checkout on another machine gets the PostgreSQL the project states rather
than whichever that machine provisioned last. `SKY_POSTGRES_BIN` still outranks
it, and a pin with nothing provisioned for it is skipped — pin, then run
`sky db provision --embed` (or `sky doctor --fix`) to fetch it.

> **`embedded = true` alongside `path` / `url` / `SKY_DB_PATH` / `DATABASE_URL`
> is an error, not a precedence rule.** There is no safe answer: preferring the
> cluster means the app writes to a throwaway local directory while you believe
> it is talking to the server you named, and preferring the DSN means the opt-in
> is a line of configuration that does nothing. `sky run` names the offending
> source and both ways out, and refuses before it builds.

The cluster's `postgresql.conf` is generated from the machine it is starting on
and **re-rendered on every start**, immediately before the postmaster spawns.
That matters because `max_connections` and `shared_buffers` need a restart
rather than a reload, and a restart is exactly what is about to happen — so
resizing the host from 2 vCPU to 8, or restoring a data directory onto a
different machine, retunes the cluster on the next boot instead of leaving it
sized for the machine it was created on while the app's pools track the new one.

Only resource and planner-cost knobs are set; nothing that changes what a query
means or how durable it is. Settings you add **outside** the managed block are
preserved, and since PostgreSQL takes the last occurrence of a setting, anything
after the block's end marker wins.

`max_connections` is sized from what one app process can actually demand — the
app's own pool *and* the runtime's pools for analytics, Sky.Live sessions and
telemetry — doubled to cover the window where a restarting process overlaps the
one it replaces, plus PostgreSQL's reserved superuser slots and headroom for a
psql session. You should not need to set it; `--max-connections` on
`sky db provision` is there when you do.

**`maxOpenConns` moves that number.** The app's pool is the term every other one
is a share of, so raising it raises the whole process's demand and the cluster
follows: `maxOpenConns = 64` on a 1-core host takes the process from 20 backends
to 92, and the generated `max_connections` grows to cover it. The clamps above
(a dev cluster's 100, a shared cluster's 500) bound what Sky *derives* from the
machine; they do not overrule a number you stated, because a cluster smaller
than the pool it was told about is an app strangling itself on its own
configuration. The generated conf names the app-pool term it was sized for, so
the arithmetic can be checked from the file. Setting `maxOpenConns = 0`
(UNLIMITED) is the one case no `max_connections` can cover — the cluster is
sized for the default pool instead and the conf says so.

For this to work the knob has to be visible to the command that provisions the
cluster, not only to the app: `sky db start` and `sky run` read `sky.toml`, the
project's `.env` and their own environment, in the runtime's own precedence
(environment, then `.env`, then `sky.toml`). A knob exported for the app's
service unit alone is invisible to a `sky db provision --shared` run on the same
host — state the cluster's size with `--max-connections` there.

A **shared** cluster (`sky db provision --shared`) is sized the same way with
one extra factor: it serves every app on the host rather than one, so the
per-process demand is multiplied by the apps a machine that size is expected to
carry — one per four cores, capped at four, because a Sky process asks for four
connections per core and expects to use several of them. Passing
`--max-connections` overrides the derivation entirely; the flag is how an
operator who genuinely runs a fleet states the number.

### Analytics and telemetry writes

These sinks are batched behind a single buffered writer and trade a bounded
crash-loss window for throughput. The full behaviour — the bounded queue, the
drop policy and its counter, the shutdown flush, and connection sharing — is in
[observability](observability.md#how-analytics-and-telemetry-reach-the-database).

| Env var | Default | Meaning |
|---|---|---|
| `SKY_ANALYTICS_SYNCHRONOUS_COMMIT` | `off` | `on` makes analytics writes wait for the WAL fsync at commit. The default trades a few hundred ms of server-crash loss for throughput. Per-transaction (`SET LOCAL`) — never cluster-wide, and never applied to the app's own pool. |
| `SKY_TELEMETRY_SYNCHRONOUS_COMMIT` | `off` | The same, for the console's log / metric / span writes. |
| `SKY_ANALYTICS_DB_PATH` | `.sky/analytics.db` | Where analytics persists. A `postgres://` value puts it in that database; anything else is a local SQLite file. Falls back to `SKY_CONSOLE_DB_PATH`, then `DATABASE_URL` when that is a PostgreSQL DSN. |
| `SKY_ANALYTICS_RETENTION` | (unset — keep everything) | Delete events older than this. Go duration (`720h`) or a day form (`90d`). |

### Garbage collection

There is **no `sky.toml` knob for the collector, and that is deliberate.** At
startup the runtime derives `GOMEMLIMIT` from detected machine memory — the
cgroup limit before `/proc/meminfo`, so a container is sized to itself and not
to its host — after subtracting the OS and, under `--embed`, the cluster's own
`shared_buffers`, and sets `GOGC=400` under that bound. Measured at **+19%
throughput and 759 MB peak RSS** at 500 concurrent sessions on the PostgreSQL
store (`docs/perf/runs/gogc-postgres-20260816/`).

The escape hatch is Go's own, because that is the one that already exists and
already works from a container image or a systemd unit that never reads
`sky.toml`:

| Env var | Default | Meaning |
|---|---|---|
| `GOGC` | (derived — `400`; Go's `100` on serverless and on machines too small for the bound) | Go's own heap-growth multiplier. **Set it and sky derives nothing for it.** |
| `GOMEMLIMIT` | (derived — three quarters of RAM after the OS and any embedded cluster) | Go's own soft memory limit. **Set it and sky derives nothing for it.** Setting one of these does not suppress the other. |
| `SKY_GC_QUIET` | (unset) | Suppresses the one-line `[sky.gc]` startup banner on stderr. For a one-shot CLI whose stderr is somebody else's input; it does not change what is derived. |

A value written into `sky.toml` would travel to machines it was not sized for,
which is the whole reason the figure is derived at runtime rather than
configured. Sizing detail, including the floor below which the runtime declines
to tune at all: [embedded PostgreSQL](skydb/embedded-postgres.md).

---

## `[jobs]` *(v0.19.14+)*

`Std.Jobs` queue backend. Same shape as `[live] store` — the keys seed env
defaults the runtime reads, and shell env still wins without a rebuild.

```toml
[jobs]
store     = "postgres"          # memory (default) / sqlite / postgres
storePath = "postgres://…"      # sqlite: file path · postgres: DSN
```

| Key         | Env var                     | Default          | Meaning                          |
|-------------|-----------------------------|------------------|----------------------------------|
| `store`     | `<PREFIX>_JOBS_STORE`       | `memory`         | `memory` / `sqlite` / `postgres` |
| `storePath` | `<PREFIX>_JOBS_STORE_PATH`  | `./_sky/jobs.db` | sqlite path, or the Postgres DSN |

`store_path` is accepted as a spelling of `storePath`, because that is the name
the runtime's own error message used.

**`memory` is single-instance and volatile** — enqueued jobs are lost on restart
and are never shared between replicas. That is fine for development and is a
deliberate opt-in; it is not a default to deploy on. With `ENV=production` set,
a `sqlite`/`postgres` store that cannot be opened is a **hard startup failure**
rather than a silent fall back to the memory queue.

> This section was referenced by the runtime's error messages and parsed by
> nothing until v0.19.14: setting `[jobs] store` did exactly nothing, while in
> production the app refused to start and told the operator to set the key they
> had just set.

---

## `[log]`

Std.Log default format and threshold. Both seed env-var
defaults; runtime env still overrides without recompile.

```toml
[log]
format = "json"            # plain (default) / json
level  = "info"            # debug / info / warn / error
```

| Key      | Env var                | Default     | Values                          |
|----------|------------------------|-------------|---------------------------------|
| `format` | `<PREFIX>_LOG_FORMAT`  | `plain`     | `plain` / `json`                |
| `level`  | `<PREFIX>_LOG_LEVEL`   | `info`      | `debug` / `info` / `warn` / `error` |

Switch to JSON in production by setting
`<PREFIX>_LOG_FORMAT=json` in the deployment env — no rebuild
required.

---

## `[env]` *(v0.11.5+)*

Namespace prefix for Sky's internal runtime env-var reads. The
default prefix is `SKY`, so the runtime reads `SKY_LIVE_PORT`,
`SKY_AUTH_TOKEN_TTL`, `SKY_LOG_FORMAT`, etc.

Projects running multiple Sky binaries on the same host can
declare a private namespace to avoid collision:

```toml
[env]
prefix = "FENCE"
```

The compiler emits `rt.SetEnvPrefix("FENCE")` at the top of the
generated `init()`. From there, the runtime reads
`FENCE_LIVE_PORT`, `FENCE_AUTH_TOKEN_TTL`, `FENCE_LOG_FORMAT`,
etc. The user's shell / `.env` / docker env supplies the
prefixed names too.

| Key      | Default | Meaning                                                   |
|----------|---------|-----------------------------------------------------------|
| `prefix` | `SKY`   | Namespace for runtime env-var reads. Trims trailing `_`.  |

What's affected by the prefix:

- All Sky-internal namespaces: `LIVE_*`, `AUTH_*`, `LOG_*`,
  `DB_*`, `ENV`, `STATIC_DIR` (and the legacy alias).
- All sky.toml-derived defaults — the generated init() emits
  `rt.SetSkyDefault("LIVE_TTL", "1800")`, which under prefix
  `FENCE` becomes `FENCE_LIVE_TTL=1800`.

What's NOT affected:

- User code calling `System.getenv "DATABASE_URL"` — those names
  are passed through raw.
- Standard non-Sky fallbacks: `DATABASE_URL`, `REDIS_URL`,
  `PORT` (consulted by Sky.Live's session-store config when the
  prefixed override is unset).
- The compile-time-only `SKY_SOLVER_BUDGET` knob, read by the
  Haskell compiler itself.

Backwards-compatible: omit `[env] prefix` and behaviour matches
every prior Sky version exactly.

For values not known until runtime (derived from a startup flag,
computed from another secret), use `System.setenv name value`
from your code — it's a `Task Error ()` returning helper that
mutates the process env without Go FFI.

---

## `[security]`

```toml
[security]
csrf = false     # default: true — leave it on unless you are sure
```

| Key    | Env var         | Default | Meaning |
|--------|-----------------|---------|---------|
| `csrf` | `SKY_CSRF`      | `true`  | Global CSRF middleware on/off |

**`csrf`** turns off Sky's global CSRF middleware. Leave it on for
anything a browser talks to. The one case that justifies `false` is a
purely-stateless API where every endpoint authenticates from a `Bearer`
token in the `Authorization` header — a cross-origin page cannot add
that header without a preflight, so the header itself is the CSRF
defence. If any endpoint authenticates from a **cookie**, turning this
off is a vulnerability.

Equivalent at runtime: `SKY_CSRF=off` (or `false` / `0`).

### There is no `[security] env`

Which environment a binary is running in is **not** a sky.toml key, and
never has been. Set the `ENV` environment variable on the deployment:

```bash
ENV=production ./sky-out/app
```

`ENV` (or the namespaced `<PREFIX>_ENV`, e.g. `SKY_ENV`) is what gates
the dev console, metrics auth, and the `Secure` attribute on session
cookies. Anything other than `dev` / `development` / `local` counts as
production.

The reason it is not a build-time key is that one binary gets promoted
dev → staging → prod; a value baked in at compile time could not be
right for all three. Writing `[security] env` into sky.toml now
produces a build warning naming this variable.

---

## Precedence

Configuration values resolve in this order (highest priority
first):

1. **System environment variables** (`export VAR=…`, Docker
   `ENV`, k8s, CI vars).
2. **`.env` file** in the working directory (auto-loaded at
   startup; never overrides existing env vars).
3. **Explicit builder calls in code** (`Live.withPort`,
   `Live.withStore`, `Live.withStorePath`, `Live.withTtl`,
   `Live.withIdleEvict`).
4. **`sky.toml`** defaults (compiled into the binary's
   `init()`; only set when the corresponding env var is unset).
5. **Hardcoded runtime fallbacks** (e.g. port `8080`, TTL `30m`).

Standard godotenv / Docker convention: production deployments
always win over `.env` and `sky.toml` so you can override
settings without editing files.

Layers 1, 2 and 4 meet in the *same* environment variable —
`sky.toml` keys are seeded into their env vars at startup — but
the runtime records which values it seeded itself, so a
`sky.toml`-derived default never counts as "the operator set
this". The one rule, spelled out:

> **operator env (shell or `.env`) → `withX` builder call →
> seeded default (`sky.toml` / compiler) → hardcoded fallback**

So an operator can always override the binary without a rebuild,
and an explicit `withX` call in code always beats the `sky.toml`
seed while still losing to the operator.

---

## Tooling

- `sky init [name]` — scaffolds `sky.toml` with sensible defaults.
- `sky add github.com/foo/bar` — adds a Go dep + version pin.
- `sky remove <pkg>` — removes a Go dep cleanly.
- `sky install` — re-resolves deps and regenerates missing bindings.
- `sky update` — bumps deps to latest within their semver constraints.

`sky.toml` is hand-editable any time — the compiler re-reads it on
every build.
