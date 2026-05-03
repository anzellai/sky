# `sky.toml` — project manifest reference

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
| `[database]`         | Std.Db default driver + connection                   |
| `[log]`              | Std.Log default format and level                     |
| `[env]`              | Env-var namespace prefix (v0.11.5+)                  |

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
(default `SKY_`). See the
[Sky.Live overview](skylive/overview.md) for the full picture.

```toml
[live]
port         = 8000              # HTTP listener port
store        = "sqlite"          # session store: memory / sqlite / redis / postgres / firestore
storePath    = "./sessions.db"   # file path or connection URL
ttl          = 1800              # session TTL in seconds (30 min)
static       = "public"          # static asset directory served at /static
maxBodyBytes = 5242880           # cap for /_sky/event POST body (5 MiB)
```

| Key            | Env var                       | Default     | Meaning                                                    |
|----------------|-------------------------------|-------------|------------------------------------------------------------|
| `port`         | `<PREFIX>_LIVE_PORT`          | `8000`      | HTTP listener port                                         |
| `store`        | `<PREFIX>_LIVE_STORE`         | `memory`    | `memory` / `sqlite` / `redis` / `postgres` / `firestore`   |
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

Std.Auth runtime defaults. Auth.signToken / verifyToken read
the secret from env at runtime, so the sky.toml value is just a
seed — production should always override via shell env or `.env`.

```toml
[auth]
secret     = "do-not-ship-this-default"
tokenTtl   = 86400             # 24 h
cookieName = "sky_auth"
driver     = "jwt"             # jwt / session / oauth
```

| Key          | Env var                       | Default      | Meaning                              |
|--------------|-------------------------------|--------------|--------------------------------------|
| `secret`     | `<PREFIX>_AUTH_SECRET`        | (empty)      | JWT signing key — **MUST** override in prod |
| `tokenTtl`   | `<PREFIX>_AUTH_TOKEN_TTL`     | `86400`      | JWT lifetime in seconds              |
| `cookieName` | `<PREFIX>_AUTH_COOKIE`        | `sky_auth`   | Session cookie name                  |
| `driver`     | `<PREFIX>_AUTH_DRIVER`        | `jwt`        | `jwt` / `session` / `oauth`          |

---

## `[database]`

Std.Db default connection. `Db.connect ()` (unit form) reads
`<PREFIX>_DB_PATH` to find the database — set this here once and
all calls pick it up automatically.

```toml
[database]
driver = "sqlite"          # sqlite / postgres
path   = "./app.db"        # sqlite file path or postgres URL
```

| Key      | Env var                  | Default   | Meaning                          |
|----------|--------------------------|-----------|----------------------------------|
| `driver` | `<PREFIX>_DB_DRIVER`     | `sqlite`  | `sqlite` / `postgres`            |
| `path`   | `<PREFIX>_DB_PATH`       | (empty)   | File path or connection URL      |

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

## Precedence

Configuration values resolve in this order (highest priority
first):

1. **System environment variables** (`export VAR=…`, Docker
   `ENV`, k8s, CI vars).
2. **`.env` file** in the working directory (auto-loaded at
   startup; never overrides existing env vars).
3. **`sky.toml`** defaults (compiled into the binary's
   `init()`; only set when the corresponding env var is unset).

Standard godotenv / Docker convention: production deployments
always win over `.env` and `sky.toml` so you can override
settings without editing files.

---

## Tooling

- `sky init [name]` — scaffolds `sky.toml` with sensible defaults.
- `sky add github.com/foo/bar` — adds a Go dep + version pin.
- `sky remove <pkg>` — removes a Go dep cleanly.
- `sky install` — re-resolves deps and regenerates missing bindings.
- `sky update` — bumps deps to latest within their semver constraints.

`sky.toml` is hand-editable any time — the compiler re-reads it on
every build.
