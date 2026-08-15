# Embedded PostgreSQL

> **Status: shipped in v0.20.3.** This document is the design and the record of
> how it was reached — the scope decision, what was deliberately not built, and
> what each phase found on contact with the code. The phase table at the end is
> kept as history rather than as a plan.
>
> Two things a reader should know before relying on it. **The PostgreSQL version
> pinned for bundles is 18.6** (`scripts/skydb/build-postgres-bundle.sh`); where
> this document quotes a measurement taken against 14.21, that is the version
> the measurement was actually run on and the figure is left as measured rather
> than restated. And **no `postgres-bundle-v*` release is cut yet**, so
> `sky db provision --embed` cannot fetch one — `SKY_POSTGRES_BIN`, a local
> bundle, or a system PostgreSQL are the working paths today.

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

The costs are real and stated up front: the binary grows by roughly the size of
the compressed bundle — call it **25–30 MB** — and it becomes platform-specific,
so cross-compilation needs the target's PostgreSQL bundle present.

> **The original "150–250 MB" was ~3× too high**, and P5b measured the real
> figure rather than re-estimating it. The archive is embedded *compressed* (it
> has to stay a tar inside the embedded FS — see below), so what the binary
> carries is the gzip, not the ~77 MB tree. Measured on a
> `postgres-14.21-darwin-arm64` bundle of 7,543,905 bytes: the `--embed` binary
> came to 39,937,538 bytes against 32,306,818 for the same program without the
> flag. That is a delta of 7,630,720 — the archive plus **86,815 bytes** of
> `embed.FS` metadata and section alignment. Embedding costs the archive's own
> size and essentially nothing else, so a real ~25 MB release bundle lands at
> ~25 MB.

### `sky build --embed` (P5b)

Three moving parts: the flag, the archive staged beside the emitted `main.go`,
and two generated calls.

- **The bundle is embedded AS A TAR, and unpacked at first start.** `go:embed`
  forces mode 0444 on every file it carries and cannot represent a symlink at
  all. Embedding the *extracted* tree therefore yields a `postgres` that cannot
  be executed and no `libpq.5.dylib` — a binary that builds, ships, and fails on
  the deployed host. `sky build --embed` writes
  `sky-out/postgres-bundle.tar.gz` plus a generated
  `sky-out/pg_embed_bundle_gen.go` holding the `//go:embed` and the two
  assignments (`rt.EmbeddedPostgresBundle`, `rt.EmbeddedPostgresBundleName`).
  The archive is embedded under a **fixed** name (`postgres-bundle.tar.gz`): a
  `go:embed` path is a literal and cannot carry a version or a platform. That
  fixed name is why P5b also had to change what the runtime's extraction marker
  records. It keyed on the archive's *name*, which is the one thing a rebuild
  never changes — so a binary rebuilt onto a different PostgreSQL matched the
  existing marker, skipped extraction, and ran the **previous** server against a
  data directory the new build expected. The marker now records a sha256 of the
  archive's bytes. (The test that was supposed to catch this changed the name as
  well as the content, so it passed; it now holds the name fixed, which is what
  the compiler actually does.)
- **The start and stop calls go in `func main()`, never in an `init()`.** They
  are emitted by the lowerer (`lower_main`), directly under
  `defer rt.LogPanicAndExit()`. This is not a style preference: `[database]
  path` / `url` reach the runtime as `rt.SetSkyDefault("DB_PATH", …)` in the
  prologue `init()`, Go runs every `init()` before `main`, and calling from
  `main` is what makes those two config sources visible to `--embed`'s
  ambiguity check. **P5b proved this by mutation rather than asserting it.**
  With the calls moved into a generated `init()` in a file named
  `embedded_postgres_gen.go` — which sorts before `main.go`, and is exactly the
  filename a maintainer would reach for — a project carrying
  `[database] path = "notes.db"` *and* `--embed` started a cluster and wrote to
  it, exit 0. Restored, the same binary refuses with the conflict named and
  exits 1.
- **The migration call goes in `main` too — and after the start.** A project
  with `db/migrations/` gets a generated `embedded_migrations.go`, and P5b
  emitted `rt.MaybeApplyEmbeddedMigrationsAndExit()` from *its* `init()`. By the
  rule directly above, that made `SKY_DB_OP=migrate ./app --embed` impossible by
  construction: the migration ran before `main`, so before the cluster existed,
  and the binary exited with `could not open database for embedded migrations`.
  The two constraints pull in opposite directions — the start call cannot move
  into an `init()` to meet the migration, because that re-opens the ambiguity
  hole — and `main`, immediately after the start, is the only placement that
  satisfies both. The generated `init()` now only ASSIGNS
  `rt.SkyEmbeddedMigrations`, which has no ordering requirement beyond "before
  `main` reads it". Gated by
  `rust/crates/project/tests/embedded_main_prologue.rs` (the emitted order) and
  `TestOwnershipLiveEmbedMigrateAppliesAgainstTheStartedCluster` (a real
  migration against a real embedded cluster).
- **All four calls are emitted for every program, `--embed` or not.** A build
  without the flag links no bundle, so `MaybeStartEmbeddedPostgres` returns on
  its first line, `StopEmbeddedPostgres` is a nil check, and
  `MaybeApplyEmbeddedMigrationsAndExit` returns unless `SKY_DB_OP` is set and
  the project baked migrations in. Emitting them conditionally would buy nothing
  measurable and would make `./app --embed` on an ordinary build ignore the flag
  in silence. As shipped, an ordinary binary asked to `--embed` says so, and
  names every place it looked.

**Where the bundle comes from, and the decision behind it: `sky build --embed`
provisions on demand.** It does not require a prior `sky db provision --embed`.
The rule this document sets is "no runtime fetch on a *production* path", and a
build is not a production path — it happens on a developer's machine or a CI
runner, both of which already fetch Go modules and Sky dependencies. Refusing to
build until a second command had been run would make the first
`sky build --embed` on a clean checkout fail with an instruction instead of a
binary. The property the rule protects — that a *deployed* `./app --embed` never
reaches the network — is untouched, because by then everything is inside it.

Resolution order is most-local-first, and only the last step needs a network:

1. `$SKY_HOME/postgres-bundles/postgres-<version>-<platform>.tar.gz` — a bundle
   cache kept *beside* `postgres/`, never inside it, because that directory is
   what `sky db start`'s discovery enumerates.
2. For the **host** platform only: `$SKY_HOME/postgres/<version>/` re-tarred.
   P3's provision cache holds the extracted tree and `go:embed` cannot take a
   tree, so the tree is re-packed rather than re-downloaded — which means a
   machine that has provisioned once builds `--embed` offline forever after.
3. The release, fetched and checksum-verified through P3's own
   manifest-first machinery (`fetch_verified_archive`).

The version is `[database] postgresVersion` when the project pins one, read
through P3's reader, so a project cannot be developed against one major and
shipped carrying another.

**Cross-compilation asks for the target's bundle by name.** `GOOS` / `GOARCH`
are the cross-compilation lever for the whole `sky build` pipeline (`go build`
inherits this process's environment), so `--embed` reads the same two variables.
A target Sky publishes a bundle for is fetched; a target it does not
(`GOOS=windows`) is refused up front, before anything is compiled, naming the
four platforms that exist. What it never does is embed the host's binaries into
another platform's binary — that failure would surface at first start on the
deployed host, hours after the build that caused it.

Two smaller properties, both gated: a `--embed` build that cannot stage its
bundle **fails** rather than quietly producing a database-less binary; and a
build *without* `--embed` **deletes** the staged archive, the generated Go and
the stamp, so one `--embed` build does not make every later ordinary build of
that project 25 MB heavier.

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
> **The budget is measured on the socket FILE, not the directory** — and the
> limit is not 107 everywhere. `sizeof(sun_path) - 1` is 107 on Linux and
> **103 on macOS**. PostgreSQL then appends `.s.PGSQL.<port>` (14 bytes) and
> creates a `.lock` five bytes longer, so a check against the *directory* path
> is ~19 bytes optimistic. Both implementations budget **92** bytes, which sits
> under the smaller platform limit with room for the lock file
> (`maxSocketPath` in Go, `MAX_SOCKET_PATH` in Rust — one number, two sides,
> pinned by `TestSocketBudgetIsMeasuredOnTheSocketFile`).
>
> Nineteen bytes is exactly the size of a bug that passes on a developer's
> machine and fails on a host with a longer prefix.
>
> The hash is FNV-1a/128 truncated to 64 bits, not `DefaultHasher`, because it is
> *persisted*: `DefaultHasher`'s output is explicitly not stable across Rust
> releases, and a compiler upgrade must not orphan every running cluster.
>
> **What is hashed is the PostgreSQL DATA DIRECTORY, not the project — and P5b
> found the two implementations disagreeing about exactly that.** Rust hashed
> the project path; Go hashed `<dataRoot>/pg`. The hash *function* was identical
> and a docstring claimed the two "name the same socket directory for the same
> path"; nothing checked the input. The consequence landed squarely on
> `--embed`: an app run in a project whose cluster `sky db start` or `sky run`
> had already brought up found the live `postmaster.pid`, adopted it, then
> probed a socket directory that did not exist — 60 seconds of `waitReady`, then
> `PostgreSQL did not accept connections within 1m0s` and exit 1, with a healthy
> postmaster running the whole time. In the other direction `sky db start`
> printed a `psql -h …` hint pointing at a socket that was not there.
>
> The data directory is the input both sides now use, and it is the right one
> rather than the convenient one: `./app --embed --data-dir /var/lib/app` has no
> project to hash, and one-socket-per-data-directory is the property that
> actually matters. Both sides also resolve symlinks as far as the path exists
> before hashing (`resolved_path` / `resolvedPath`) — `.skydata/pg` does not
> exist until the first `initdb`, so plain canonicalisation cannot be used, and
> on macOS `/tmp/x` and `/private/tmp/x` are one directory that hashes two ways.
>
> The gate is **one pinned literal per side**, not a comparison of the two
> implementations: `the_socket_directory_for_a_pinned_project_is_a_pinned_constant`
> (Rust) and `TestTheSocketDirectoryForAPinnedProjectIsAPinnedConstant` (Go) both
> assert `/sky/pinned/project` → `/tmp/sky-3b7c436bcb7e1ee0`. Two implementations
> compared only to each other can drift together, which is what they did.

`pg_ctl start` builds its command line and hands it to `/bin/sh`, so the socket
directory is shell-interpreted on the way to the postmaster. A path carrying a
quote, a `$` or a space cannot be made safe by quoting, so `sky db start`
rejects it with the reason rather than passing it through. The two paths sky
derives are safe by construction; the half that is not is `$XDG_RUNTIME_DIR`.

> **The socket directory is not the only argument that goes through that
> shell.** `start_postmaster` in `pg_ctl.c` interpolates the executable, the
> `-D` data directory, the `-o` post-options *and* the `-l` log file into one
> string and hands the lot to `/bin/sh -c`. P5 verified this against
> PostgreSQL 14.21 by pointing each at a path containing `$(touch …)` and
> watching the file appear: all three ran. P2 shell-checks only the socket
> directory, so a project whose own path carries a `$(…)` or a backtick would
> still have it executed through `-D` and `-l` — those two paths are derived
> from the project directory, which is the user's, not sky's. Closing it is a
> one-line reuse of `socket_dir_is_shell_safe` on the data dir and the log
> path in `run_pg_ctl_start`. `pg_ctl stop` does **not** shell out; only
> `start` does.
>
> **Closed in P3.** `run_pg_ctl_start` now runs the same predicate over `-D` and
> `-l`, and `start_cluster` runs it again *before* `initdb` — a project whose
> path cannot be handed to pg_ctl can never be started, and initialising a
> cluster first would leave the user a data directory for a database they will
> never be able to run. The gate is a project directory literally named
> `inj$(touch pwned)dir` driven against a stand-in `pg_ctl` that reproduces
> `start_postmaster`'s one behaviour — build a single string, hand it to
> `/bin/sh -c` — and it asserts the marker file does not appear. With the
> refusal removed it does appear, and the shell's own error then names the
> directory with the substitution already expanded away.

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

### `sky db provision --shared` (P6)

The verb is `sky db provision` because that verb already means "make the
PostgreSQL this machine needs exist": `--embed` provisions the *binaries*,
`--shared` provisions the *cluster* they run, and `--shared --app <name>`
provisions one app's slice of it. A new top-level verb would have split one
operator story across two nouns, and the cluster verbs (`sky db start` / `stop` /
`ps`) are spoken for by the per-project development supervisor — reusing them
for a machine-wide service would make `sky db stop` mean "my project's cluster"
in one directory and "every app on this host" in another. `--app` is separate
from the cluster provision because apps arrive one at a time, long after the
cluster was tuned, and adding the fifth must not restart the four serving
traffic.

```bash
sky db provision --shared --service --backup --start   # once per host
sky db provision --shared --app orders                 # once per app; prints its DSN
```

Everything lives under one **state directory** — `/var/lib/sky` on Linux,
`/usr/local/var/sky` on macOS, `--state-dir` to move it. The socket directory is
a *sibling* of the data directory rather than a child, and that is not tidiness:
PostgreSQL requires the data directory to be 0700, so a socket inside it is
unreachable by every user except the one running the postmaster — which is every
app on a shared host. For the same reason the socket directory is `0755` and
`unix_socket_permissions` is `0777`: those two numbers *are* the mechanism for
"several apps, under several accounts, on one host", the access control here
being authentication rather than file modes. Tightened to `0700` every generated
artefact still reads correctly and every app under another account fails at
connect with `Permission denied`, so the live gate `stat`s both. A state
directory that is relative, ephemeral (`/tmp`,
`/var/tmp`, `/dev/shm`, `$TMPDIR`, `/var/folders`), inside a Sky project, or
shell-unsafe is refused up front.

**The security property, and the two things that actually enforce it.**

- **`REVOKE ALL ON DATABASE … FROM PUBLIC`.** `PUBLIC` is an implicit member of
  every role and may **connect to every database** by default, so
  database-per-app plus role-per-app buys nothing on its own — app A connects to
  app B's database as a matter of course. `template1` is hardened too, and for a
  second reason: before PostgreSQL 15 `PUBLIC` also holds `CREATE` on every
  `public` schema, and `template1`'s is **copied into every database created
  after it**. The bundle pins 18.6, where that default is already closed, but a
  shared cluster may be an operator's existing server, so it is applied rather
  than assumed.
- **`scram-sha-256` in a `pg_hba.conf` sky generates WHOLE.** The file is
  first-match-wins and `initdb` writes `local all all trust` near the top; a
  `scram-sha-256` rule *appended* below it is never reached. The file would look
  right in review, every app would authenticate with `trust`, any local process
  could connect as any role simply by claiming to be it, and every `REVOKE`
  behind that would be decoration. The superuser keeps `peer` — the kernel's own
  answer to "which uid connected" — so sky administers the cluster with no
  password stored anywhere.
- **And the running cluster is made to *read* that file.** Writing
  `pg_hba.conf` is not applying it: the postmaster reads it at startup and on
  SIGHUP, so a cluster that was **already up** — the adopted case, which is the
  primary one — goes on enforcing whatever it read then, indefinitely, while the
  file on disk reviews correctly. That is silent in exactly the case where
  silence is fatal: an adopted `md5` cluster fails loudly at the next connection,
  and a cluster sky started reads the new file, but an adopted `trust` cluster
  keeps accepting any password from anyone. So a provision that finds the cluster
  running reloads it, and **proves the reload took** — `pg_conf_load_time()` must
  advance and `pg_hba_file_rules` must report no parse error, since `pg_ctl
  reload` reports success for a reload the postmaster then discards. Sky also
  asks the server for its `hba_file` and `config_file` and refuses when they are
  not the files it wrote: a distribution package keeps both under
  `/etc/postgresql`, where a hardened file in the data directory is inert.
- **`--app <name>` will not take over a role it did not create.** The
  pre-existing branch used to `ALTER ROLE … PASSWORD` and print the result as the
  app's DSN. For the account that ran `--shared` — the bootstrap superuser, whose
  name is not a constant and so cannot be in the reserved list — that handed one
  app every other app's data, and gave the operator's own account a password it
  did not choose. For an operator's `analytics` or a previous tenant's role it
  handed the new app the old one's identity and took the old one's password away.
  Three questions are asked of any role that already exists, and any one of them
  is a refusal: does it hold `SUPERUSER` / `CREATEROLE` / `CREATEDB` /
  `REPLICATION` / `BYPASSRLS`; **is it a member of any role** (`pg_auth_members`);
  and did sky create it — recorded as a comment on the role itself, so the answer
  survives a state directory that was restored or lost.
  `validate_app_name` refuses the current account outright, before any connection.

  The membership question is the other half of the first, and it is asked because
  attributes are only one of the two ways PostgreSQL holds privilege. `GRANT beta
  TO alpha` leaves every `rol*` column false, so a refusal that reads attributes
  alone sees an ordinary role — and `--app alpha --rotate-password` then prints a
  DSN that reads beta's data. All three questions are asked of a role **sky
  itself created**, too: sky's comment says who made the role, not what an
  operator has done to it since.

- **`--app <name>` will not take over a DATABASE it did not create either.** A
  role and a database of the same name are independent objects, and the
  combination that reaches an operator's data is the one where the *role* is
  absent: a `metrics` database made by hand years ago, with no `metrics` login
  role. That skipped the role refusal entirely (it is reached only when the role
  exists), skipped `CREATE DATABASE` (it exists), and ran the rest against their
  data — `REVOKE ALL ON DATABASE metrics FROM PUBLIC`, which takes the operator's
  own role's `CONNECT` away while the command reports success, and `ALTER SCHEMA
  public OWNER TO metrics`, which hands the schema to the new app whose DSN is
  printed in the same breath. Since adopting an operator's existing server is the
  documented primary case for `--shared`, this was reachable by design rather than
  by mishap. Databases now carry sky's comment exactly as roles do, and one
  without it is refused. The gate is the general form: after a refused `--app`
  run, every database sky did not create is byte-identical — owner, ACL and the
  `public` schema's owner.

- **`--app <name>` will not issue credentials against a cluster nobody hardened.**
  Its only guard was that `PG_VERSION` exists, so it ran none of what `--shared`
  runs — not the check that the server reads sky's files, not the `pg_hba.conf`
  reload, not the hardening SQL — and then printed a DSN and the sentence below
  about every database sky provisioned. Against a cluster still carrying
  `initdb`'s `local all all trust` that sentence is false in the way that
  matters: any local process may connect as any role by claiming to be it, and
  every `REVOKE` behind it is decoration. Reachable by pointing `--state-dir` at
  an existing cluster instead of running `--shared` first, which is exactly the
  deviation an operator makes when they already have a PostgreSQL and take
  `--app` for the part they need. The question is asked by attempt like the rest:
  a connection as the app's own role, over the app's own DSN, with a password
  that is deliberately not the app's, is required to fail with `28P01`. A
  connection means `trust`; any other refusal means a method under which the
  printed DSN would not work either.

All three are gated by attempt, not by inspection: `an_apps_credentials_cannot_reach_another_apps_database`
provisions two apps against a live cluster, has each write a row, then connects
**as app A with app A's own password to app B's database** and requires SQLSTATE
`42501`, and connects **as app B with app A's password** and requires `28P01`.
Every probe reads on its failing branch, so the mutation evidence is the leaked
data itself: deleting the `REVOKE` yields `alpha connected to beta's database and
read Ok([[Some("secrets")]])`, and turning the hba line to `trust` yields
`alpha's password authenticated as beta, which then read Ok(Some("beta-secret"))`.
The same two questions are put to **`pg_dump`** — a real libpq client that knows
nothing about sky's own protocol client — using the DSN exactly as printed.

> **The scope of the guarantee is the databases sky provisions**, plus `postgres`
> and `template1`, which it hardens. `REVOKE … FROM PUBLIC` is per-database and
> PostgreSQL has no cluster-wide default to set, so a database an operator
> creates by hand in this cluster keeps `PUBLIC`'s `CONNECT` and every app role
> can reach it. `sky db provision --shared --app` therefore says "refused by
> every database sky provisioned but `<app>`", which is what is true.

**Sky speaks the PostgreSQL protocol itself** (`rust/crates/sky/src/pg_wire.rs`),
because there is nothing in the shipped set to speak it with: `psql` is excluded
on licence grounds, `createdb`/`createuser` are not shipped either and could not
run the `REVOKE`s, and `postgres --single` needs the cluster *stopped* — which on
a shared host means taking every other app down to add one. It is ~450 lines: a
startup packet, SCRAM-SHA-256 (RFC 7677, carrying the RFC's own vectors as unit
tests), and simple queries. `md5` and **cleartext** are deliberately
unimplemented so a mis-edited `pg_hba.conf` cannot downgrade the cluster in
silence — cleartext being the weaker of the two, since answering it puts an app's
password on the wire as it stands.

**Tuning is derived from the host** — `shared_buffers` at a quarter of RAM
capped at 8GB, `effective_cache_size`, `work_mem` divided by `max_connections`,
parallel workers from the CPU count — and it is a *replaceable* marked block, not
the append-only one the development profile uses, because a shared cluster is
re-tuned when the host changes. `SKY_PG_TUNE_MEM_MB` states the budget for
containers, where `/proc/meminfo` reports the host's RAM and not the cgroup
limit. The development profile's rule holds unchanged: resource and planner-cost
knobs only, nothing that changes what a query means.

> **`effective_io_concurrency` cannot be set unconditionally, and P6 found this
> by starting a cluster rather than by reading a manual.** On a platform without
> `posix_fadvise` — macOS is one — a non-zero value is a configuration ERROR, not
> a hint: `FATAL: configuration file … contains errors`, and the postmaster never
> accepts a connection. A generated block carrying `200` therefore produces a
> cluster that cannot start, on the machine most likely to try it first. It is
> now omitted where the platform lacks the call, and `HostFacts` carries that as
> a fact about the host alongside RAM and CPU.

**The service unit exists so the cluster's lifecycle is the OS's**, and the
signal is the whole of it. PostgreSQL reads `SIGTERM` as *smart* shutdown — wait
for every client to disconnect, with no timeout — and `SIGINT` as *fast*. systemd
sends `SIGTERM` by default, so the unit sets **`KillSignal=SIGINT`**; without it
a cluster with one live app connection never stops, hits `TimeoutStopSec`, takes
a `SIGKILL`, and performs crash recovery on **every reboot**. `Type=exec` rather
than `notify`, because the bundle is built `--without-systemd` and cannot send
`READY=1`.

launchd has no `KillSignal` at all, so the plist runs a generated wrapper that
traps `SIGTERM` and sends the postmaster `SIGINT`. The wrapper waits **twice**:
POSIX `wait` returns the moment a trapped signal is handled, with the postmaster
still checkpointing, so a single `wait` would let launchd reap the job mid-flush.
That claim is gated live — a real postmaster, a client connection held open, a
`SIGTERM` to the wrapper, and an assertion that it is down within 30 seconds.
With the wrapper sending `SIGTERM` instead, it is not: the gate fails at 31s with
the smart shutdown still waiting on that one connection.

**The backup is `pg_dump --format=custom` on a timer**, into
`<state>/backups`, renamed into place so a `.part` from an interrupted run is
never mistaken for a backup, with a retention `find`. The app list is read at
**run time** from the file `--app` maintains, so an app provisioned after the
timer was generated is backed up without regenerating anything. The gate restores
the dump into a fresh database and reads the row back — with `--format=custom`
dropped, the file still exists and still holds the data, and `pg_restore` says
`input file appears to be a text format dump. Please use psql.`, which is the
whole difference between a file and a backup.

**Restore with `pg_restore --create --dbname postgres <dump>`**, and the flag is
the boundary rather than a convenience. The archive does carry the database's own
ACL — the `REVOKE … FROM PUBLIC` that keeps every other app out — but as a
DATABASE-section entry, and `pg_restore` applies that section *only* with
`--create`. Restored into a database made by hand instead, the recovered database
carries `PUBLIC`'s default `CONNECT` and every app role on the cluster can read
it: the cross-tenant read this phase exists to prevent, reintroduced by the
recovery of the app it protects. Sky runs no restore itself, so the command and
its reason are written into the generated script, and the live gate performs the
recovery for real — it drops `alpha` as a disaster would, rebuilds it with
`--create`, reads the row back, and requires `42501` when `beta` tries the same.
Retention is `find -mtime "+$KEEP_DAYS"`, and
`--backup-keep` is range-checked (1-3650) because `0` reads as "older than 24
hours" and would have the nightly job delete every dump but the newest.

The backups are also protected as *files*: the script runs `umask 077` and the
directory is `0700`. A dump is every row of an app's database and
`globals-*.sql` is every role's SCRAM verifier, so world-readable backups are a
cross-tenant read taken from the filesystem, needing no authentication, no
`CONNECT` and no SQL at all. The gate `stat`s the directory and every dump it
produced.

> **Roles are cluster-wide, and `pg_dumpall` is not in Sky's bundle.** A
> `pg_dump` of one database restores into a cluster with no `orders` role by
> failing on every `OWNER TO`. The script uses `pg_dumpall --globals-only` when
> the installation has it and **says so in its log when it does not**, rather
> than producing a backup that cannot be restored unattended. Adding
> `pg_dumpall` to `SHIPPED_BINARIES` in `scripts/skydb/build-postgres-bundle.sh`
> would close this; it links libpq and not readline, so it costs nothing on
> licence grounds.

Sky writes the unit files into `<state>/service` and prints the `sudo` lines to
install them. It does not install them itself: that means writing under `/etc` or
`/Library`, and a tool that silently acquires privileges is worse than one that
prints two lines. `sky db provision --shared` also **refuses to run as root** —
`initdb` refuses too, and for the same reason: the data directory would be owned
by root while the service ran the postmaster as somebody else.

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

> **"Looks like a postmaster" is the EXECUTABLE, not a substring of the command
> line.** P2 matched `postgres` anywhere in the `ps` output, which says yes to
> `./app --embed --data-dir /var/lib/postgres-data` and to
> `go test -run TestStopPostgresOnSignal` — P5a's own test process was
> classified that way. Since this is the second leg of the two-legged check, the
> consequences are the ones the leg exists to prevent: `sky db ps` reports a
> database that is not there, and a start refuses for as long as the recycled pid
> lives. P3 matches `argv[0]`'s basename against `postgres`/`postmaster`
> (tolerating the trailing colon of a rewritten process title).

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
2. `~/.sky/postgres/<version>/bin` — the P3 cache. **Pinned version first, then
   newest major.** An empty or absent cache is simply skipped.
3. `PATH` — a system PostgreSQL.

A candidate counts only if it holds all of `initdb`, `pg_ctl` and `postgres`.
`psql` is deliberately not required — it is a client convenience, and demanding
it would reject a perfectly usable server-only distribution.

`SKY_POSTGRES_BIN` set but incomplete is an **error, not a fall-through**.
Quietly moving on to the next candidate would hand the user a cluster from an
installation they did not choose, which is worse than the typo they made.

When nothing is found, the message names all three lookups and gives a command
for each way out (install, point `SKY_POSTGRES_BIN`, or
`sky db provision --embed`). "PostgreSQL not found" on its own sends the reader
to the source to work out what was even looked for.

> **The pin has to choose, or it is decoration.** P3 records
> `[database] postgresVersion` in `sky.toml`, and step 2 orders the cache by it
> before falling back to newest-first — otherwise a project that states which
> PostgreSQL it is developed against would still get whichever one the machine
> provisioned last, and "explicit and reproducible" would be a claim about a
> file nothing reads. The pin orders the CACHE GROUP only: it never outranks
> `SKY_POSTGRES_BIN`, which is someone deliberately overriding, and a pin with
> nothing provisioned for it is not a candidate rather than a synthesised path.

### `sky db provision --embed` (P3)

Fetches the platform bundle P2b built, into `~/.sky/postgres/<version>/`
(`$SKY_HOME` overrides the root), and records the pin. Four properties are
load-bearing, and each has a gate that has been observed failing:

- **The checksum is verified against the bytes on disk, before anything is
  extracted.** The release's `SHA256SUMS` is fetched *first*, so the archive is
  never downloaded without something to check it against, and the digest is
  taken from the file that landed rather than from the bytes we meant to write —
  a truncated transfer is exactly what would otherwise pass. A mismatch names
  both digests and installs nothing.
- **The install is atomic.** The archive is extracted into a staging directory
  **outside** `~/.sky/postgres/` and renamed into place. Staging *inside* it
  would be worse than useless: the cache directory is what discovery enumerates,
  so a half-extracted tree there is a *candidate* — a `bin/` holding a truncated
  `postgres` that `sky db start` would select and fail on, much later and much
  more confusingly than at the point of the interrupted download.
- **Provisioning what is already provisioned is a fast success** that makes no
  request at all. A cache entry counts as provisioned only when every required
  binary is present *and executable*: `go:embed` yields mode 0444 and a
  file-exists check would accept a `postgres` that cannot be run.
- **An unsupported platform is refused with a way out**, never a download of
  something that cannot execute. Windows is named as out of scope rather than
  reported as an unknown platform.

Offline installs are first-class, because the machine that needs a database is
not always the machine with a network:

```bash
sky db provision --embed                          # fetch + verify + pin
sky db provision --embed --from ./postgres-18.6-linux-amd64.tar.gz \
                        --checksum <sha256>       # from a local file
sky doctor --fix                                  # pre-warm the cache
```

`--from` takes the checksum from `--checksum`, or from a `SHA256SUMS` sitting
beside the archive (so "copy the release directory across" just works). With
neither, it **refuses** — an air-gapped copy is where a corrupted file is most
likely and least visible. When the network is unreachable, the failure names the
`--from` route and `SKY_POSTGRES_BIN` rather than a curl exit code.

`sky doctor` reports a project with `[database] embedded = true` and no
reachable PostgreSQL, and `--fix` pre-warms the cache. That fix deliberately
does **not** write the pin: `doctor --fix` is contracted to leave `sky.toml`
alone, and pinning a version is a decision the project makes.

The bundle contract is P2b's, consumed rather than re-invented: release tag
`postgres-bundle-v<version>`, asset `postgres-<version>-<platform>.tar.gz`
holding one top-level directory, and a `sha256sum`-format `SHA256SUMS` listing
every asset. `$SKY_POSTGRES_BUNDLE_URL` overrides the release URL for a mirror
or an internal host. The version sky asks for is checked against the build
script's `PG_VERSION` by a unit test — the two files are the only places the
number lives, and a bump to one and not the other is a 404 for every user.

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
4. Wait for readiness. If `SKY_DB_OP=migrate` / `status` is set and the binary
   carries embedded migrations, apply or report them against the cluster just
   started, and exit — a deployed binary self-migrates with no source tree and
   no `sky` on the host. Otherwise connect and boot the app.
5. On `SIGTERM`: stop accepting → drain → **then** `pg_ctl stop -m fast`.
   Ordering matters; stopping the database first turns a clean deploy into a
   page of errors. The last step is skipped for a cluster this process adopted
   rather than started.

P5 ships this as `runtime-go/rt/pg_embed*.go`. Four details it settled on
contact with the code:

- **The postmaster is exec'd directly, not via `pg_ctl start`.** The app has to
  be able to *observe* the database dying, and `pg_ctl` daemonises — leaving a
  pid to poll rather than a child to `wait` for, and polling cannot tell a dead
  postmaster from a recycled pid. Exec'ing it directly also removes `/bin/sh`
  from the start path entirely (see the `pg_ctl` note above).
- **Readiness is a connection, not a file.** The socket appears before crash
  recovery finishes and `postmaster.pid`'s status line lags, so both would
  report a database that immediately refuses queries. `pg_isready -d postgres`
  when the distribution has it, a real connection otherwise. Without the
  explicit `-d`, `pg_isready` defaults the database name to the OS user and
  every boot writes a pair of `FATAL: database "<user>" does not exist` lines
  into the server log before the app has run a query.
- **The shutdown sequence needs a completion barrier, not just a call.** Each
  app shape installs its own `SIGTERM` handler and calls `RunShutdownHooks`
  too; whichever goroutine arrives second finds the chain already claimed and
  returns *at once*, with the drain still in flight. `awaitShutdownHooks`
  (`runtime-go/rt/shutdown.go`) is what makes "drained" true rather than
  merely called. The listeners register with `RegisterAcceptStopper`, which is
  what gives the first phase something to do.
- **Every exit routes through `rt.ExitProcess`, and a failed start cleans up
  after itself.** `os.Exit` does not run deferred functions, so any exit reached
  after `rt.MaybeStartEmbeddedPostgres()` skips generated `main`'s
  `defer rt.StopEmbeddedPostgres()` and leaves the postmaster running with
  nothing left to stop it — which the *next* run adopts and, by the rule below,
  never stops either. One such exit is therefore not one orphaned database; it
  is a database that outlives every subsequent run. `Std.System.exit` — the
  ordinary way a `Sky.Cli` job ends, and a one-shot job under `--embed` is
  exactly the case — was one of nine such sites; so were the port-in-use paths,
  the profiler watchdog, the console invariant and three terminal-runtime
  handlers. All of them now call `rt.ExitProcess`, and
  `runtime-go/rt/pg_embed_exit_audit_test.go` reads the package's syntax tree to
  keep the list honest: only `pg_embed.go` (which defines `ExitProcess`, and
  whose other exits fire when the database has already gone) and
  `panic_recover.go` (which runs as `main`'s *first* defer, so the stop —
  registered second — has already run) may call `os.Exit` directly. Separately,
  `boot()` stops what it spawned when it fails *after* `spawn` — a readiness
  timeout leaves a live postmaster the exiting process is the last one able to
  stop.
- **The data directory is refused if it is one the system may empty** —
  `/tmp`, `/var/tmp`, `/dev/shm`, `$TMPDIR`, macOS's `/var/folders`. Under
  `--embed` that directory holds the app's only copy of its data, and a cluster
  that silently reinitialises looks exactly like an app that lost every row.
- **An app stops only the cluster it STARTED.** Adoption (the row below) made
  `./app --embed` connect to a live postmaster it did not own — and then stop it
  on the way out, because `StopEmbeddedPostgres` did not consult `adopted`. So a
  developer who ran `sky db start` and then their own built binary lost the
  cluster the moment the binary exited: silently, once per run, and against the
  contract `sky db start` states ("explicit and persistent — it stays up until
  stopped"), which `sky run` already honours by ref-counting. `stopPostgres`
  now returns early for an adopted cluster, naming it and how to stop it.

  > **The registry is deliberately NOT written from Go.** `sky run`'s
  > ref-counting lives in `~/.sky/clusters.json` (`ClusterEntry.explicit`,
  > `RunRef`, `prune_refs`), and a Go writer could in principle join it. It
  > would have to reproduce the lock protocol with its stale-lock rule, the
  > tmp-and-rename write, the canonical project-path key, the `ps -o command=`
  > capture and the two-legged ref liveness — exactly, and with no shared test
  > holding the two implementations together, so the next format change breaks
  > a binary rather than a build. And it would be writing a file that does not
  > apply where `--embed` actually runs: a deployed binary on a server has no
  > project directory, no `sky` toolchain, and nothing that reads `~/.sky`.
  > "Stop only what you started" needs no shared state at all, and it gets the
  > `sky db start` case right, which is the reported one. What it gives up is
  > reaping: an app that is `SIGKILL`ed leaves an orphan, its successor adopts
  > it, and nothing takes it down until `sky db stop`. A database left running
  > is the cheaper of the two mistakes, and it is the state `sky db ps` and
  > `sky db stop` exist for.
  >
  > One case remains open and is NOT closed by this rule: two concurrent
  > `./app --embed` processes on the same data directory, where the one that
  > started the cluster exits first and stops it under the one that adopted it.
  > The adopter does not lose data silently — `watchAdopted` sees the
  > postmaster go, prints it, and exits non-zero so a supervisor restarts the
  > tree — but it is an avoidable exit. Closing it needs the shared ref count,
  > i.e. the registry, i.e. a decision about whether `--embed` participates in
  > it at all.

### Failure modes that must be handled, not discovered

Three of these are not specific to `--embed` — they are properties of pointing
any postmaster at a data directory, so **P2 already closes them for
`sky db start`** and P5 reuses the same handling:

| Failure | P2 behaviour |
|---|---|
| Double start | Sky-level message naming the data dir and offering `sky db stop`; PostgreSQL's raw "another server might be running" is translated, and an unrecognised failure is passed through verbatim rather than dressed up |
| Already running | **Success no-op.** The verb states a desired end state, so a script that runs it before every task must not have to tell "started it" from "it was already up" |
| Stale `postmaster.pid` after `SIGKILL` | Detected and cleared — but only once the named pid fails the two-legged liveness check above |
| Orphaned postmaster after the APP is `SIGKILL`ed | Adopted, not refused. The postmaster is in its own process group and outlives its parent; it is the right server on the right data directory, and refusing to boot would need a human every time |
| Major-version mismatch | Refused before any start, naming both majors and pointing at `pg_upgrade` or `SKY_POSTGRES_BIN` |
| Half-finished `initdb` | A data dir with no `PG_VERSION` is reported as such; a failed `initdb` removes its own wreckage so the next run does not diagnose the wrong bug |

`sky db stop` is idempotent for the same reason `start` is: stopping a cluster
that is already down succeeds, so the verb is safe in a shell trap.

> **What the stale-pid handling is actually for.** P5's first live gate for it
> was vacuous, and the mutation that proved so is worth recording: PostgreSQL
> clears a `postmaster.pid` naming a plainly-dead process *itself*
> (`CreateLockFile` in `miscinit.c`), so deleting sky's own handling changed
> nothing. The case that needs sky is the one the two-legged check exists for —
> the pid has been **recycled** by an unrelated live process. PostgreSQL then
> sees a live pid, concludes another postmaster is running, and refuses to
> start *permanently*, accusing a process that has nothing to do with it. The
> gate now stands up a live `sleep`, writes its pid into the lock file, and
> asserts the cluster still boots.
>
> **P2's Rust gate had the identical defect, and P3 proved it by mutation.**
> `a_sigkilled_postmaster_leaves_a_stale_pidfile_that_the_next_start_clears`
> passed with `clear_stale_pidfile` deleted — it was asserting PostgreSQL's
> behaviour, not sky's. Rewritten around a recycled pid, the same mutation makes
> it red with PostgreSQL's own refusal ("another PostgreSQL server is already
> using this data directory"). The impostor is a script named `postgres-helper`,
> so the one fixture also gates the executable-versus-substring check above; it
> must **not** be a copy of `/bin/sleep`, because on macOS a copied platform
> binary fails its code-signature check and is killed at exec — which silently
> returns the gate to the dead-pid case it was rewritten to escape. The test now
> asserts the impostor is alive and carries `postgres` in its command line
> before it asserts anything about sky.

The remaining two are genuinely `--embed`-only:

- **`--embed` together with an explicit DSN is an error**, not a precedence
  puzzle. A deploy that silently ignores the operator's DSN and writes to local
  disk instead must fail loudly at startup. The names checked are the ones the
  runtime actually reads — `<PREFIX>_DB_PATH` and `DATABASE_URL`, per the note
  above; `SKY_DB_URL` is not one of them.
- **A dead child.** If PostgreSQL exits, the app exits non-zero and lets the
  supervisor restart the tree. Restarting in place hides a failing disk until
  it is an outage.

## Sizing a host — what this actually costs to run

The common deployment is one Sky.Live app plus its embedded PostgreSQL on one
small cloud instance. Measured components:

| | RAM |
|---|---|
| Minimal Linux | ~250 MB |
| Sky app binary (Go, idle) | ~30–40 MB |
| PostgreSQL base — postmaster + 6 auxiliaries at `shared_buffers = 32MB` | **36 MB** (measured) |
| PG backends — one process per *active* connection, ~5–10 MB each | ~40–70 MB at 6–10 active |
| Sky.Live sessions — one Model gob per session, ~10–100 KB typical | ~10 MB at 200 concurrent |
| **Total** | **~390 MB** |

**1 GB is comfortable.** The pool ceiling (14 on 2 cores, see below) is a
ceiling and not an allocation — `database/sql` opens lazily, so a host pays for
what is in flight.

**RAM is not the binding constraint; CPU is.** Sky.Live renders views on the
server and diffs them per update, so on a burstable instance the baseline CPU
allowance runs out before the memory does. A 0.25-vCPU-baseline instance is a
demo host, whatever its RAM says.

Three things that bite, in the order they bite:

1. **Backups are the operator's.** A single instance has no replica.
   `sky db provision --shared` generates a backup timer; a single `--embed` app
   does not get one. "I lost everything" is the failure mode of exactly this
   setup, and it is not one the tooling currently prevents.
2. **Idle sessions evict after 5 minutes** (`defaultIdleEvict`, disableable
   with `SKY_LIVE_IDLE_EVICT=0`). Active SSE-connected sessions do not evict and
   there is **no hard count or byte cap**. For typical Models that is ~10 MB at
   200 concurrent and irrelevant; an app whose Model carries a large list or a
   cached dataset is the one way many active sessions exhaust a small host.
3. **Disk.** Data, WAL and the extracted bundle (~77 MB) — ample on a 30 GB
   volume, tight on a 10 GB one.

The economic argument for `--embed` is here rather than in the ergonomics: a
managed PostgreSQL instance typically costs as much again as a small VM, so
embedding turns a two-line bill into one, and costs 36 MB.

## Connections and capacity

The pool sizing in [`sky.toml`](../sky-toml.md#connection-pool-postgresql) says
what each pool is. This section says what they add up to, because the number
that matters to a server is the sum.

### The arithmetic

One Sky app process opens **four PostgreSQL-facing pools**: app data
(`db_auth.go`), analytics (`analytics_store.go`), the Sky.Live session store
(`live_store.go`, the `pgx` path) and telemetry (`telemetry/persist.go`). The
app pool is 4 × CPU clamped 4–32; the three aux pools take a quarter-share of
it, clamped 2–8 (`dbAuxPoolConfig`).

| Cores | App pool | 3 aux pools | Total per process |
|---|---|---|---|
| 2 | 8 | 6 | **14** |
| 4 | 16 | 12 | **28** |
| 8+ | 32 | 24 | **56** |

Against PostgreSQL's default `max_connections = 100`: one 8-core instance takes
56 and is fine. **Two of them take 112, and the second is refused.** On a host
running several Sky apps this is the binding constraint long before CPU or disk
is.

Say the improvement honestly next to that. Before the pool change these were
**unlimited** — a burst opened one backend per concurrent request and reached
`FATAL: sorry, too many clients already` under exactly the load you had scaled
up to serve. Bounded is much better than unbounded. It is not the same as
tuned: the quarter-share exists to stop three helpers each asking for a full
app pool, not because 56 is a target.

### Write characteristics, as they are today

- **Analytics writes are row-at-a-time.** One
  `INSERT INTO analytics_events (…) VALUES (…)` per event — pgx's simple
  protocol does not batch `;`-separated statements, and a source comment in
  `analytics_store.go` records that. Row-at-a-time is fsync-bound: order
  5–10k inserts/s, against the 100k–500k rows/s that `COPY` or multi-row
  inserts reach on the same hardware.
- **Telemetry is bounded by construction.** A single buffered flusher goroutine
  does all the writing, which is why it does not open a backend per concurrent
  flush.
- **Nothing splits analytics or metrics onto a separate database
  automatically.** Each takes a DSN. The same DSN means the same database; a
  different one means a different database. The split is available and
  unopinionated.

### Guidance — NOT IMPLEMENTED

Everything in this subsection is a recommendation for an operator, or for a
later change to Sky. **None of it is shipped behaviour**, and nothing in Sky
does any of it for you today:

1. **Batch the analytics inserts** (`COPY`, or multi-row `VALUES`). The single
   biggest lever available — 10–50×.
2. **`synchronous_commit = off` on the analytics/telemetry connection only.**
   It is a per-transaction setting, so app data keeps full durability while
   telemetry trades a few hundred milliseconds of loss-on-crash for throughput.
3. **Time-partition the event tables**, BRIN on the timestamp, and `DROP` old
   partitions rather than `DELETE` them. A drop is instant; a delete leaves
   dead tuples that autovacuum then fights the app for.
4. **Reach for a connection pooler earlier than instinct suggests.** By the
   table above, PgBouncer earns its place at *two 8-core instances* — not at
   some distant scale.

At small-to-medium traffic all of this is comfortably fine. The failure mode to
watch for is not latency in the thing being written: at real analytics volume
the per-row inserts compete with OLTP for the same WAL and shared buffers, so
you would feel it as slow **transactional** queries rather than slow analytics.

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
`<PREFIX>_DB_PATH` (or `DATABASE_URL`) at an external PostgreSQL, which costs
nothing architecturally — the app only ever consumes a DSN.

Shipping an extension makes it *available*; `CREATE EXTENSION` is still
per-database.

### The gate

An SBOM is generated per bundle in CI, listing every linked library and its
licence, and **a gate fails the build if a bundle carries anything GPL, LGPL or
AGPL**.

Four properties of that gate are load-bearing:

1. **It runs against the actual binaries, not the configure line.** A configure
   flag records an intention; the built artifact records what happened.
2. **It walks every shared object in the bundle, not just `postgres`.** An
   extension is a `.so` loaded by `dlopen` at runtime — it is never linked into
   the server binary. A gate that inspected only the main executable would pass
   a bundle containing a GPL extension in `lib/`, while appearing to check
   exactly the thing it missed.
3. **A symbolic link is part of what the bundle ships.** `find -type f` does
   not match one and `[ -f ]` follows one, and those two facts together made
   the same GNU readline a `GATE FAIL` as a regular file in `lib/` and a `GATE
   PASS` as a link — while a link pointing at the build machine's copy also
   resolved every dependency on it to `bundle:lib/…`, reporting `unvendored
   deps: 0` for a file the bundle did not contain. A bundle's `lib/` is mostly
   links: it is assembled with `cp -Rf`, which preserves PostgreSQL's soname
   chains. Links are enumerated, classified by their own name *and* their
   target's, and one whose chain leaves the bundle is unvendored by definition.
4. **A fixture has to prove it planted what it claims to plant.** The fixtures
   link libraries they never call, because the gate classifies a dependency by
   its recorded NAME — and GNU ld on Debian/Ubuntu links `--as-needed` by
   default, which drops an unreferenced library from `DT_NEEDED` altogether. So
   on Linux the planted library was never recorded, `objdump -p` found one
   dependency in the whole bundle (`libc`), and the gate returned `GATE PASS` on
   a bundle built to carry GNU readline. The suite read 11/11 on macOS, where
   the load command is recorded unconditionally, and 6/11 on Linux at the same
   commit. Fixtures now link with `-Wl,--no-as-needed` AND assert the record
   exists before asserting anything about the verdict, so a broken fixture says
   "the FIXTURE is broken, not the gate" instead of looking like a gate that
   stopped rejecting. Relatedly, the scanner now refuses to report a verdict at
   all when `objdump` / `otool` is missing: a dependency reader that is absent
   and an object with no dependencies are otherwise the same observation, and
   the second one reads as a clean bundle.

Each of the three rejection causes — copyleft, unclassified, unvendored — has a
fixture in `scripts/skydb/test-licence-gate.sh` that isolates it, and the suite
asserts *which* cause fired rather than only that the exit code was 1. Without
that, the unvendored arm could be deleted outright and the suite still reported
7 passed, 0 failed.

The suite runs per-commit as `licence-gate-linux` / `licence-gate-macos` in
`rust-ci.yml`, inside the `ci-green` fan-in, so a red verdict blocks a merge; it
runs again in `postgres-bundle.yml` ahead of the build matrix, where it blocks
publication. It ran only in the latter to begin with, on a `pull_request`
trigger — reporting on every PR and able to block none of them.

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
| **P3** ✅ | `sky db provision --embed` — fetch Sky's own bundle, checksum-before-extract, atomic install, the `[database] postgresVersion` pin, the offline `--from` route and the `sky doctor --fix` pre-warm — `rust/crates/sky/src/db_provision.rs`, gated by its unit tests + `tests/db_provision_flow.rs` (a real download over a local HTTP server, a corrupt archive, an interrupted extract, and a `SIGKILL`ed provision) |
| **P4** ✅ | `sky run` / `sky watch` integration: `[database] embedded`, DSN injection, the ref count — `rust/crates/sky/src/db_cluster.rs` + `main.rs`, gated by its unit tests + `tests/db_run_cluster_flow.rs` (two overlapping `sky run`s against a real PostgreSQL) |
| **P5a** ✅ | The runtime supervisor behind `./app --embed`: data-dir resolution, bundle extraction, `initdb`, RAM/CPU-derived tuning, a postmaster child in its own process group, readiness, the ordered `SIGTERM` sequence, and all five failure modes — `runtime-go/rt/pg_embed.go` + `pg_embed_bundle.go` + `pg_embed_conf.go`, gated by `pg_embed*_test.go` (including a live cycle against a real PostgreSQL and a subprocess that proves the app exits non-zero when its database dies) |
| **P5b** ✅ | `sky build --embed`: the compiler flag, the `go:embed` of the platform bundle, on-demand provisioning with an offline re-pack of P3's cache, the cross-compilation refusal, and the two calls emitted into `func main()` — `rust/crates/sky/src/db_embed.rs` + `project/src/build.rs` (`write_postgres_bundle`) + `lower/src/lower.rs` (`lower_main`), gated by their unit tests plus the pinned socket-derivation literals on both sides |
| **P6** ✅ | Shared-cluster service mode: `sky db provision --shared` (+ `--app`, `--service`, `--backup`), the host-derived production tuning, the whole-file `pg_hba.conf`, the `REVOKE … FROM PUBLIC` boundary, the systemd unit / launchd job + shutdown wrapper, and the backup timer — `rust/crates/sky/src/db_shared.rs` + `pg_wire.rs` (a minimal SCRAM-SHA-256 protocol client, because the shipped bundle has none), gated by their unit tests, `src/db_shared/live_tests.rs` (two apps on a live cluster, a cross-tenant read attempted as app A and refused, a dump restored into a fresh database, and a SIGTERM'd wrapper) and `tests/db_shared_flow.rs` (the real binary, with `pg_dump` asked the same question) |
