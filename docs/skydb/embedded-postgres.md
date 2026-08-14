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
>
> Two details P2 found on contact with the code. The `XDG_RUNTIME_DIR` branch is
> itself length-checked and degrades to `/tmp` when the user's runtime dir is
> long — it is the *user's* value, and an unchecked branch just relocates the
> overflow. And the fallback is the literal `/tmp`, not `std::env::temp_dir()`:
> on macOS the latter is a ~49-byte per-user path under `/var/folders/`, which
> spends half the budget before the hash is appended.
>
> The hash is FNV-1a/128 truncated to 64 bits, not `DefaultHasher`, because it is
> *persisted*: `DefaultHasher`'s output is explicitly not stable across Rust
> releases, and a compiler upgrade must not orphan every running cluster.

`pg_ctl start` builds its command line and hands it to `/bin/sh`, so the socket
directory is shell-interpreted on the way to the postmaster. A path carrying a
quote, a `$` or a space cannot be made safe by quoting, so `sky db start`
rejects it with the reason rather than passing it through. The two paths sky
derives are safe by construction; the half that is not is `$XDG_RUNTIME_DIR`.

Development clusters are tuned small (`shared_buffers` in the tens of MB), so
several idle projects cost tens of megabytes each rather than hundreds. P2
writes a marked, idempotent block into the generated `postgresql.conf`:
`shared_buffers = 32MB` (against PostgreSQL's own 128MB default, allocated up
front whether or not a query is ever served), `max_connections = 50`,
`work_mem = 4MB`, `maintenance_work_mem = 32MB`, `max_wal_size = 256MB`,
`min_wal_size = 64MB`, `autovacuum_max_workers = 1`, `listen_addresses = ''`.
A measured idle cluster on PostgreSQL 14 comes to ~36MB across the postmaster
and its six auxiliary processes.

Every one of those is a **resource** knob. Nothing that changes what a query
means is set — not `fsync`, not `wal_level` — because a development cluster that
behaves differently from production reintroduces, in a subtler form, exactly the
divergence this feature exists to remove. `unix_socket_directories` is likewise
absent: the hashed path is re-derived from the environment and passed as
`-k` on every start, so it is never frozen into a file that a moved
`XDG_RUNTIME_DIR` would silently invalidate.

`initdb` runs with `--auth-local=trust --auth-host=reject`. Trust costs nothing
here because the socket *is* the access control — a 0700 directory owned by the
developer — and it spares every `psql` a password prompt; host auth is rejected
outright as a second lock on a door that `listen_addresses = ''` has already
bricked up.

**Production, several apps on one host — one shared cluster.** Per-app clusters
would mean a postmaster, a WAL, an autovacuum launcher, a backup job and a
tuning pass *each*. Instead: one tuned cluster, **database-per-app and
role-per-app**. The role boundary is load-bearing, not hygiene — an app's
credentials must not be able to read another app's database, and that is
enforced by PostgreSQL roles rather than by convention.

## The registry

`sky db ps` needs to see clusters it did not start, so a machine-level registry
(`~/.sky/clusters.json`, or `$SKY_HOME/clusters.json`) maps project path → data
dir, socket path, pid, version. Entries are reaped when the process is gone,
because processes die without deregistering.

`sky db status` is already taken by migration status, and `sky db init` by the
migration scaffold. The cluster verbs are therefore `sky db start`,
`sky db stop`, `sky db ps` (`--all` across projects).

**Reaping is two-legged, and both legs matter.** `kill(pid, 0)` alone answers
"is *a* process alive", not "is *my postmaster* alive": after a `SIGKILL` the
stale `postmaster.pid` still names a number the kernel is free to hand to
something else, and `sky db ps` would then report a database that is not there.
So a pid is only believed when the process also *looks* like a postmaster
(`ps -o command=`), and P2 clears a pid file only after that check fails —
deleting a live postmaster's pid file would let a second postmaster open the
same data directory, which is how a development database gets corrupted.

What reaping does with an entry depends on what is gone:

| Observation | Registry effect | `sky db ps` |
|---|---|---|
| Postmaster serving the data dir | pid adopted (even if restarted outside `sky`) | `running` |
| Data dir present, nothing serving it | **pid zeroed** | `stopped` |
| Data dir gone (`rm -rf .skydata`, project deleted) | entry dropped | absent |

Zeroing rather than deleting is what makes "a dead pid is never *reported* as
running" structural: the number is erased at reap time, so no later code path
can print it. An idle-but-initialised cluster stays listed, which is the useful
answer to "what does this machine have".

## Where the binaries come from

P2 *discovers*; P3 provisions. The order is fixed, and it is the order of
decreasing explicitness:

1. `SKY_POSTGRES_BIN` — an operator's or a test's deliberate choice.
2. `~/.sky/postgres/<version>/bin` — the P3 cache, newest major first. It does
   not exist yet, which is fine: it is simply skipped.
3. `PATH` — a system PostgreSQL.

A candidate counts only if it holds all of `initdb`, `pg_ctl` and `postgres`.
`psql` is deliberately not required — it is a client convenience, and demanding
it would reject a perfectly usable server-only distribution.

`SKY_POSTGRES_BIN` set but incomplete is an **error, not a fall-through**.
Quietly moving on to the next candidate would hand the user a cluster from an
installation they did not choose, which is worse than the typo they made.

When nothing is found, the message names all three lookups and gives a command
for each way out (install, point `SKY_POSTGRES_BIN`, or the not-yet-built
`sky db provision --embed`). "PostgreSQL not found" on its own sends the reader
to the source to work out what was even looked for.

## Lifecycle

Two entry points, deliberately different, so casual use does not accumulate
clusters:

- **`sky run`** starts a cluster if needed and stops it on exit, ref-counted so
  two `sky run`s on one project do not fight. Ephemeral.
- **`sky db start`** is explicit and persistent — it stays up until stopped.
  This is the mode for running `./sky-out/app` repeatedly.

### Opting in, and what P4 injects

The opt-in is `sky.toml` **`[database] embedded = true`**. Earlier drafts of this
document sketched a `[data]` section; `[database]` is what P1 actually landed —
it already owns `driver`, `path`/`url`, the four pool knobs and `isolation` — and
a second section describing the same subsystem would leave a reader two places to
look and no rule for which wins. `embedded` is the one key in that section that
seeds **no** environment variable: it is read by the toolchain, not by the app.

What `sky run` injects is `<PREFIX>_DB_PATH` (the `[env] prefix` namespace, so a
project with a private namespace gets its own name and not a variable nothing
reads), carrying `postgresql:///postgres?host=<socket dir>`. The `postgresql://`
prefix is load-bearing: it is the shape both `rt.detectDriver` and the compiler's
`driver_for_dsn` classify as Postgres, and `?host=<dir>` is libpq's documented
way to name a unix socket directory. No user and no password — local auth is
`trust` and the client defaults the role to the OS user, which is the superuser
`initdb` created. The database is `postgres`, the one `initdb` always makes;
database-per-app is the shared-cluster problem and belongs to P6.

The `--db-push` / `--db-migrate` / `--db-seed` steps run against the same DSN.
They are separate `sky db …` processes, so the variable is passed to each of
them explicitly — without that the app would boot onto an unmigrated cluster.

### The reference count

The registry entry gains two fields, both `#[serde(default)]` so a P2 registry
still loads: `explicit` (a user asked for this cluster by name) and `refs` (the
live `sky run` / `sky watch` invocations depending on it). A run's exit stops the
postmaster only when `!explicit && refs.is_empty()`.

**A pid is not a reference**, for the same reason `postmaster.pid` is not
liveness. A `SIGKILL`ed `sky run` never releases, the kernel is free to hand its
pid to something else, and a ref believed on pid alone pins that project's
cluster up for the rest of the session — `sky run` would have created a database
nothing can close. So each ref records the holder's own `ps -o command=` line at
acquire time and is believed only while the pid is alive **and** still runs that
command; with no `ps` at all, aliveness is enough, because dropping a ref we
cannot verify tears a running app's database out from under it.

Every registry writer prunes stale refs on the way through, so the corpse of a
killed run is cleared by the next `sky run`, `sky db start`, `sky db stop` or
`sky db ps` — and the *next* ordinary `sky run` then finds itself alone and takes
the cluster down on its own way out.

The release holds the registry lock **across** the `pg_ctl stop`. The decision
"no one else needs this" and the shutdown acting on it have to be one step, or a
`sky run` starting in the gap takes a reference to a postmaster already on its
way down.

> **What P4 does NOT close: `Ctrl-C`.** SIGINT is delivered to the whole
> foreground process group, so `sky run` dies alongside the app and never runs
> its release. Catching it needs either `unsafe` (the `sky` crate is
> `#![forbid(unsafe_code)]`, and `nix`'s `sigaction` is unsafe) or a new
> signal-handling dependency, and neither is worth spending here: the cluster is
> left running with a stale reference, the reference is pruned by the next
> registry read, and the next clean `sky run` in that project stops it. The end
> state is the same as `sky db start`, and it self-heals. P5 needs a real signal
> path for `--embed`'s drain-then-stop ordering; that is where the dependency
> decision belongs.

### An explicit DSN alongside `embedded = true`

Refused, with the offending source named — the same rule this document already
fixes for `./app --embed`, applied to the development path. Four sources are
checked, and only the first is reported, because a stack of four complaints about
one mistake is harder to act on than one:

| Order | Source |
|---|---|
| 1 | `<PREFIX>_DB_PATH` in the environment |
| 2 | `DATABASE_URL` in the environment |
| 3 | `sky.toml` `[database] path` |
| 4 | `sky.toml` `[database] url` |

The environment is checked first because it is the more surprising of the two:
nothing in the repository records it. The refusal happens **before the build**,
so a misconfigured project does not sit through a compile to be told.

> The design brief called this variable `SKY_DB_URL`. **No such variable exists**
> — `runtime-go/rt/db_auth.go` reads `<PREFIX>_DB_PATH` and falls back to a bare
> `DATABASE_URL`. P4 checks the two that are real.

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

Three of these are not specific to `--embed` — they are properties of pointing
any postmaster at a data directory, so **P2 already closes them for
`sky db start`** and P5 reuses the same handling:

| Failure | P2 behaviour |
|---|---|
| Double start | Sky-level message naming the data dir and offering `sky db stop`; PostgreSQL's raw "another server might be running" is translated, and an unrecognised failure is passed through verbatim rather than dressed up |
| Already running | **Success no-op.** The verb states a desired end state, so a script that runs it before every task must not have to tell "started it" from "it was already up" |
| Stale `postmaster.pid` after `SIGKILL` | Detected and cleared — but only once the named pid fails the two-legged liveness check above |
| Major-version mismatch | Refused before any start, naming both majors and pointing at `pg_upgrade` or `SKY_POSTGRES_BIN` |
| Half-finished `initdb` | A data dir with no `PG_VERSION` is reported as such; a failed `initdb` removes its own wreckage so the next run does not diagnose the wrong bug |

`sky db stop` is idempotent for the same reason `start` is: stopping a cluster
that is already down succeeds, so the verb is safe in a shell trap.

The remaining two are genuinely `--embed`-only:

- **`--embed` together with an explicit `SKY_DB_URL` is an error**, not a
  precedence puzzle. A deploy that silently ignores the operator's DSN and
  writes to local disk instead must fail loudly at startup.
- **A dead child.** If PostgreSQL exits, the app exits non-zero and lets the
  supervisor restart the tree. Restarting in place hides a failing disk until
  it is an outage.

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

## Related fixes this depends on — **closed in P1**

Two live defects in the shipped runtime sat directly under this work. Both are
fixed in `runtime-go/rt/db_pool.go`; the user-facing surface is documented under
[`sky.toml` `[database]`](../sky-toml.md#database).

1. **No isolation level was ever set.** `Db_withTransaction` called bare
   `d.conn.Begin()`; a search of `runtime-go/rt/` for `SERIALIZABLE`,
   `LevelSerializable`, `BEGIN IMMEDIATE` or `_txlock` returned nothing.
   PostgreSQL's default is then READ COMMITTED, so `Store.transaction` provided
   atomicity and no isolation guarantee beyond it.

   **P1 makes isolation requestable, and deliberately does NOT change the
   default.** `sql.TxOptions` is now threaded through `BeginTx`, driven by
   `[database] isolation`; unset reproduces `Begin()` exactly. Raising the
   default to SERIALIZABLE silently would surface `40001 serialization_failure`
   to apps that have never seen one and have no retry — a breaking change in a
   bug fix's clothing. Worse, a safe retry requires the transaction body to be
   **replayable**, and a Sky `Task` body may have sent mail or charged a card
   before the conflict was detected. That contract does not exist in the type
   system yet, so `[database] txRetry` is opt-in, defaults to 0, and states the
   requirement it imposes at the point of use.
2. **The PostgreSQL connection pool was unconfigured.** The `SetMaxOpenConns(1)`
   clamp was correctly SQLite-only, but the comment at the fall-through claimed
   "their connection pool defaults are already sane". Go's `database/sql`
   defaults are `MaxOpenConns = 0` (unlimited), `MaxIdleConns = 2`, and no
   connection lifetime — unlimited backends under burst against a server whose
   own default `max_connections` is 100, with constant reconnect churn below
   that.

   **P1 configures all four knobs, sized from the deployment.** The runtime
   reuses the existing `IsServerless()` detector (`serverless.go` — the same
   signal `exporter.go` varies its flush cadence on) rather than growing a
   second one: VM gets 4 connections per CPU clamped 4–32 with a 5-minute idle
   reap, serverless gets 2 per CPU clamped 2–8 with a 60-second one, because
   many small instances each holding a pool is how a connection storm happens
   and a frozen instance must give its backends back. The false comment is gone,
   replaced by what is actually true.

## Phases

Each phase ships its own commit and is verifiable in isolation.

| Phase | Deliverable |
|---|---|
| **P1** ✅ | Isolation levels + deployment-aware pool configuration (independent of everything below) — `runtime-go/rt/db_pool.go`, gated by `db_pool_test.go` |
| **P2** ✅ | Cluster supervisor: data dir, `initdb`, hashed socket path, `sky db start` / `stop` / `ps`, the registry — `rust/crates/sky/src/db_cluster.rs`, gated by its unit tests + `tests/db_cluster_flow.rs` (a live cycle from a project path deep enough to overflow `sun_path`) |
| **P2b** | CI bundle build: PostgreSQL from source per platform, pinned configure line, SBOM, the GPL/LGPL/AGPL link gate, `NOTICE.md` entry |
| **P3** | `sky db provision --embed` — fetch Sky's own bundle, checksum, pin |
| **P4** ✅ | `sky run` / `sky watch` integration: `[database] embedded`, DSN injection, the ref count — `rust/crates/sky/src/db_cluster.rs` + `main.rs`, gated by its unit tests + `tests/db_run_cluster_flow.rs` (two overlapping `sky run`s against a real PostgreSQL) |
| **P5** | `sky build --embed` and `./app --embed`, including the failure modes above |
| **P6** | Shared-cluster service mode: database-per-app, role-per-app, generated unit + backup timer |
