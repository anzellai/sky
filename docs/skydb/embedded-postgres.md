# Embedded PostgreSQL

> **Status: in progress.** This document is the scope decision and the design.
> Phases are listed at the end; each ships its own commit.

Sky's data story has a seam in it. `Std.Db` is dialect-safe across SQLite and
Postgres, which means every feature must be designed twice and the differences
must be papered over. That tax is not theoretical — it has already produced two
defects that reached users: `Codec.auto` cannot encode `Money`/`Decimal` at all,
and there is no `NUMERIC`/`DECIMAL` DDL kind anywhere, while `Std.Money` on
`Std.Decimal` is `AGENTS.md`'s pinned currency default. A currency type that
cannot round-trip through the pinned store is the seam showing.

The fix is not to delete SQLite. It is to make **running the same engine in
development that you run in production** the easy path, so the dialect gap stops
being something every app walks into by accident.

## The principle

**The app binary never knows which tier it is in.** It consumes a DSN. What
changes across tiers is only *who provisions that DSN*:

| Tier | Who provisions |
|---|---|
| Development | `sky` supervises a local cluster and injects the DSN |
| Production, single app | the app itself, under `--embed`, or an operator-set DSN |
| Production, several apps on one host | one shared cluster; each app gets a DSN |
| Managed/hosted | the platform injects the DSN |

One code path in the app, several provisioning strategies. This is what makes
"the app just works" a fact rather than an aspiration, and it means the binary
under test locally is byte-identical to the one in production.

## What is NOT built

- **The `sky` binary is never self-replaced.** Self-replacement is fragile
  (permissions, a live process rewriting itself, partial writes) and this repo
  has already been bitten by binary-overwriting: `sky build` from the repo root
  is banned precisely because it clobbers `sky-out/sky`.
- **No silent fallback to SQLite** when an embedded cluster is unreachable.
  Falling back would reintroduce the exact dialect drift this feature exists to
  remove — the app would work locally and fail in production, which is the
  failure mode, not the mitigation.
- **No runtime fetch on the production path.** A first run that needs the
  network is acceptable in development and is not acceptable on a server.

## Distribution: build-time embedding

`sky build --embed` bundles a PostgreSQL distribution into the app binary via
`go:embed`, the same mechanism the compiler already uses for the Go runtime and
the Sky stdlib. The result is genuinely self-contained: one file on a bare host
gives an app and its database.

The costs are real and stated up front: the binary grows by roughly 150–250 MB,
and it becomes platform-specific, so cross-compilation needs the target's
PostgreSQL bundle present.

For development, `sky db provision --embed` fetches a platform bundle once into
a versioned, checksum-verified cache (`~/.sky/postgres/<version>/`) and records
the pin in `sky.toml` — the same shape as `sky add go/module` writing `.skydeps`.

## Clusters: one per project in development, one shared in production

These are different problems and they get different answers.

**Development — one cluster per project.** Projects stay self-contained
(`rm -rf .skydata` resets one), and two projects pinned to different PostgreSQL
majors do not fight. Clusters listen on a **unix socket, not a TCP port**:
socket paths sidestep port allocation entirely, so two `sky db start`s cannot
race, and nothing is exposed to the network by accident.

> **Socket path length is a real constraint.** The `sockaddr_un` path limit is
> ~107 bytes on Linux. A socket inside a deeply nested project directory
> overflows it and fails obscurely. Sockets therefore live in a short hashed
> path (`$XDG_RUNTIME_DIR/sky/<hash>/`, falling back to `/tmp/sky-<hash>/`)
> keyed to the project, never inside the project directory itself.

Development clusters are tuned small (`shared_buffers` in the tens of MB), so
several idle projects cost tens of megabytes each rather than hundreds.

**Production, several apps on one host — one shared cluster.** Per-app clusters
would mean a postmaster, a WAL, an autovacuum launcher, a backup job and a
tuning pass *each*. Instead: one tuned cluster, **database-per-app and
role-per-app**. The role boundary is load-bearing, not hygiene — an app's
credentials must not be able to read another app's database, and that is
enforced by PostgreSQL roles rather than by convention.

## The registry

`sky db ps` needs to see clusters it did not start, so a machine-level registry
(`~/.sky/clusters.json`) maps project path → data dir, socket path, pid, version.
Entries are reaped when the process is gone, because processes die without
deregistering.

`sky db status` is already taken by migration status, and `sky db init` by the
migration scaffold. The cluster verbs are therefore `sky db start`,
`sky db stop`, `sky db ps` (`--all` across projects).

## Lifecycle

Two entry points, deliberately different, so casual use does not accumulate
clusters:

- **`sky run`** starts a cluster if needed and stops it on exit, ref-counted so
  two `sky run`s on one project do not fight. Ephemeral.
- **`sky db start`** is explicit and persistent — it stays up until stopped.
  This is the mode for running `./sky-out/app` repeatedly.

### `./app --embed`

1. Resolve the data dir (`--data-dir` / `SKY_DATA_DIR`). Never a temp path:
   production data lives here.
2. First run: extract, `initdb`, write a `postgresql.conf` tuned from detected
   RAM and CPU — the app and the database now share a machine.
3. Start PostgreSQL as a child in its own process group, on a unix socket.
4. Wait for readiness, connect, boot the app.
5. On `SIGTERM`: stop accepting → drain → **then** `pg_ctl stop -m fast`.
   Ordering matters; stopping the database first turns a clean deploy into a
   page of errors.

### Failure modes that must be handled, not discovered

- **`--embed` together with an explicit `SKY_DB_URL` is an error**, not a
  precedence puzzle. A deploy that silently ignores the operator's DSN and
  writes to local disk instead must fail loudly at startup.
- **Two instances against one data dir** must fail with a Sky-level message.
  PostgreSQL's `postmaster.pid` will refuse; the user should not have to read
  the raw error to understand it.
- **A dead child.** If PostgreSQL exits, the app exits non-zero and lets the
  supervisor restart the tree. Restarting in place hides a failing disk until
  it is an outage.
- **`SIGKILL`** leaves a stale `postmaster.pid`. The next `--embed` start must
  detect and clear it rather than refusing to boot.
- **Major-version mismatch** between the embedded binaries and an existing data
  dir must be detected and reported, never attempted. This is where
  `pg_upgrade` eventually lands.

## Related fixes this depends on

Two live defects in the shipped runtime sit directly under this work.

1. **No isolation level is ever set.** `Db_withTransaction` calls bare
   `d.conn.Begin()`; a search of `runtime-go/rt/` for `SERIALIZABLE`,
   `LevelSerializable`, `BEGIN IMMEDIATE` or `_txlock` returns nothing.
   PostgreSQL's default is then READ COMMITTED, so `Store.transaction` provides
   atomicity and no isolation guarantee beyond it.
2. **The PostgreSQL connection pool is unconfigured.** The `SetMaxOpenConns(1)`
   clamp is correctly SQLite-only, but the comment at the fall-through claims
   "their connection pool defaults are already sane". Go's `database/sql`
   defaults are `MaxOpenConns = 0` (unlimited), `MaxIdleConns = 2`, and no
   connection lifetime — unlimited backends under burst against a server whose
   own default `max_connections` is 100, with constant reconnect churn below
   that. Sizing should also be deployment-aware: the runtime already detects
   serverless (`K_SERVICE`) and varies exporter cadence on it, and the same
   signal should pick pool defaults, since many small instances each holding a
   pool is how a connection storm happens.

## Phases

Each phase ships its own commit and is verifiable in isolation.

| Phase | Deliverable |
|---|---|
| **P1** | Isolation levels + deployment-aware pool configuration (independent of everything below) |
| **P2** | Cluster supervisor: data dir, `initdb`, hashed socket path, `sky db start` / `stop` / `ps`, the registry |
| **P3** | `sky db provision --embed` — fetch, checksum, pin |
| **P4** | `sky run` integration, ref-counted |
| **P5** | `sky build --embed` and `./app --embed`, including the failure modes above |
| **P6** | Shared-cluster service mode: database-per-app, role-per-app, generated unit + backup timer |
