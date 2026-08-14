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

## Licensing and distribution

Sky is Apache-2.0 and ships a `NOTICE.md`. Bundling a database engine into a
distributed binary is a licence question, and it is answered here rather than at
release time.

**PostgreSQL itself is redistributable.** The PostgreSQL Licence is permissive
(BSD/MIT-shaped): use, copy, modify and distribute, commercially, embedded,
provided the copyright notice and the disclaimer are retained. It is compatible
with Apache-2.0.

**The risk is not PostgreSQL, it is what a given build links.** A stock
distribution pulls in libraries with their own terms, and the sharp one is
`psql`, which links **GNU readline (GPL-3.0)**. Bundling a stock `psql` would
put a GPL-linked binary inside an Apache-2.0 product.

The resolution is to ship the server and not the interactive client. `--embed`
needs `postgres`, `initdb`, `pg_ctl`, and `pg_dump`/`pg_restore` for backups. It
does not need `psql`. **`psql` is therefore deliberately excluded from the
shipped set** — not an oversight to be helpfully corrected later.

### Bundles are built from source, in CI

Sky does **not** redistribute a third party's prebuilt binaries. Doing so would
mean inheriting someone else's configure line, their linked dependencies, and
their continued availability — none of which we control, all of which we would
be shipping.

Instead, PostgreSQL is built from source in Sky's own CI with a pinned version
and a known configure line, published as release artifacts. That buys full
control of the licence surface, reproducibility, an auditable SBOM, exact
version pinning, and no third-party availability risk. The cost is a
per-platform build matrix; PostgreSQL compiles in roughly 10–20 minutes, so this
is a per-release job, not a per-commit one.

The configure line excludes, at minimum: `--without-readline` (GPL),
`--without-systemd` (LGPL), and the `--without-perl --without-python
--without-tcl` procedural languages. OpenSSL is 3.x only (Apache-2.0; the
pre-3.0 dual licence is messier). zlib, lz4, zstd, ICU and libxml2 are
permissive and may be linked.

### Extensions

`contrib` ships with PostgreSQL core under the same permissive licence, so it is
built and included at no licence cost and little size: `pg_trgm`, `pgcrypto`,
`hstore`, `citext`, `btree_gin`, `btree_gist`, `pg_stat_statements`,
`postgres_fdw`. `pgoutput` is built into core and is what logical replication
runs on.

Two third-party extensions are included, both under the PostgreSQL Licence and
both small: **pgvector** (embeddings are common enough that its absence is the
thing people notice) and **pg_partman** (the standard tool for the time-range
partitioning that makes append-heavy tables workable).

Three widely-used extensions are **excluded, on licence grounds, deliberately**:

| Extension | Licence | Why excluded |
|---|---|---|
| PostGIS | GPL-2.0 | copyleft inside an Apache-2.0 distribution |
| TimescaleDB | Timescale License (TSL) | source-available, not permissive |
| Citus | AGPL-3.0 | network copyleft |

This table exists so the question stays settled. Anyone needing them points
`SKY_DB_URL` at an external PostgreSQL, which costs nothing architecturally —
the app only ever consumes a DSN.

Shipping an extension makes it *available*; `CREATE EXTENSION` is still
per-database.

### The gate

An SBOM is generated per bundle in CI, listing every linked library and its
licence, and **a gate fails the build if a bundle carries anything GPL, LGPL or
AGPL**.

Two properties of that gate are load-bearing:

1. **It runs against the actual binaries, not the configure line.** A configure
   flag records an intention; the built artifact records what happened.
2. **It walks every shared object in the bundle, not just `postgres`.** An
   extension is a `.so` loaded by `dlopen` at runtime — it is never linked into
   the server binary. A gate that inspected only the main executable would pass
   a bundle containing a GPL extension in `lib/`, while appearing to check
   exactly the thing it missed.

`NOTICE.md` carries the PostgreSQL copyright and licence text.

Note that hosting is not distribution: running PostgreSQL on a server triggers
no redistribution obligation under any of these licences. This section is about
the `sky` toolchain and `--embed` binaries, which are distributed.

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
| **P2b** | CI bundle build: PostgreSQL from source per platform, pinned configure line, SBOM, the GPL/LGPL/AGPL link gate, `NOTICE.md` entry |
| **P3** | `sky db provision --embed` — fetch Sky's own bundle, checksum, pin |
| **P4** | `sky run` integration, ref-counted |
| **P5** | `sky build --embed` and `./app --embed`, including the failure modes above |
| **P6** | Shared-cluster service mode: database-per-app, role-per-app, generated unit + backup timer |
