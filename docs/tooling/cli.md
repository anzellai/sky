# CLI reference

> **Status**: the Rust compiler (`rust/`, `cargo build --release -p sky`)
> is the primary Sky compiler; the Haskell compiler is preserved under
> `legacy-haskell-compiler/`. Verified by the example sweep + compiler test
> suite (`cargo test` + xtask gates). See
> [`../compiler/versions.md`](../compiler/versions.md) for the changelog.


Every `sky` subcommand. Run `sky --help` for the authoritative list.

## Build & run

### `sky build [path]`

Compile a Sky source file to a Go binary under `sky-out/`.

```bash
sky build src/Main.sky
```

Pipeline:

1. Parse `sky.toml` for `[go.dependencies]` and `[dependencies]`.
2. Auto-regenerate any missing FFI bindings in `.skycache/`.
3. Resolve modules, type-check, lower to Go under `sky-out/`.
4. Invoke `go build` → `sky-out/app` (or the `bin` name set in `sky.toml`).

**`--embed`** bundles a PostgreSQL distribution into the binary, so
`./sky-out/app --embed` is a self-contained app *and* database on a bare host —
one file, no system PostgreSQL, no `DATABASE_URL`.

```bash
sky build --embed src/Main.sky
./sky-out/app --embed                       # starts its own cluster
./sky-out/app --embed --data-dir /var/lib/myapp
```

- **What it costs.** The binary grows by the compressed bundle — about 25–30 MB
  — and becomes platform-specific. A build *without* the flag pays nothing: no
  bundle is linked, and any archive an earlier `--embed` build staged is removed.
- **Where the bundle comes from.** The pin is `[database] postgresVersion`.
  `sky build --embed` uses `$SKY_HOME/postgres-bundles/`, else re-packs an
  existing `sky db provision --embed` cache (no network), else fetches and
  checksum-verifies the release. A prior `sky db provision` is **not** required.
- **Cross-compiling.** `GOOS` / `GOARCH` select the target's bundle, so
  `GOOS=linux GOARCH=arm64 sky build --embed …` embeds Linux/arm64 PostgreSQL.
  A target Sky publishes no bundle for is refused before the build starts — the
  host's binaries are never embedded into another platform's binary.
- **`--embed` plus an explicit DSN is an error**, at app startup, naming the
  source. There is no precedence that does not either ignore the operator's
  database or make the flag inert.

`--embed` belongs on `sky build`, not on `sky run` — see below.

### `sky run [path]`

For development you do not need `--embed` (and `sky run --embed` is refused with
a pointer): set `[database] embedded = true` in `sky.toml` and `sky run` starts a
local cluster, injects the DSN, and stops it on exit. See
[`sky db start`](#sky-db-start--sky-db-stop--sky-db-ps--the-local-postgresql-cluster).

`sky build` + execute the resulting binary.

**`--profile`** turns on runtime profiling of the app (not the compiler) — for
when an app hangs, spins the CPU, or eats memory and you can't tell which. It
writes a `profile/` directory next to your project on stop:

| File | What |
|---|---|
| `REPORT.md` | Human-readable summary: stop reason (exit / panic / signal / hang), wall time, goroutine count + a state breakdown, and a **⚠️ Likely hang** verdict when goroutines sit blocked. Start here. |
| `cpu.pprof` | CPU profile for the whole run — `go tool pprof -http=: cpu.pprof` for a flame graph. |
| `heap.pprof` | Heap profile at stop — `go tool pprof -http=: heap.pprof`. |
| `goroutines.txt` | Full goroutine stack dump; the top frame of each blocked goroutine is where it's stuck. |

Options:

- `--profile-dir <dir>` — where to write (default `profile/`, relative to the project root).
- `--profile-timeout <dur>` — if the app hasn't exited after `<dur>` (e.g. `30s`, `2m`), dump profiles with a hang verdict and exit. **Opt-in** — leave it off for a server (which "hangs" by design); it still profiles until you Ctrl-C it.

```sh
sky run src/Main.sky --profile                     # profile until exit / Ctrl-C
sky run src/Main.sky --profile --profile-timeout 30s   # + auto-dump if it hangs
```

The stop fires whichever comes first: normal exit / panic, a signal
(SIGINT/SIGTERM/SIGQUIT), or the timeout. `--profile` off means zero overhead —
profiling is armed purely by an env var the flag sets, and the emitted Go is
byte-identical either way.

### `sky verify [project]`

The one-command pre-release gate for a project. Run inside a project dir (or
pass its path) and it runs, in order, stopping non-zero on the first failure:

1. **fmt** — every `.sky` file under the source root + `tests/` is already
   `sky fmt`-clean.
2. **check** — type-checks + `go build`s and emits the production binary
   (`sky check` ≡ `sky build` minus the artefact, so this one build covers both).
3. **test** — every `tests/*.sky` suite passes.

```sh
sky verify           # gate the current project
sky verify path/to/app
```

In the **compiler repo** (a dir with `examples/`), `sky verify` instead builds
AND runs every example — the runtime smoke sweep. `sky verify --help` documents
both modes.

### `sky check [path]`

Fully validate the program. `sky check` is a strict superset of `sky build`:
it runs parsing, canonicalisation, HM inference, Go codegen, *and* invokes
`go build` on the emitted output — without producing a runnable binary. If
`sky build` would fail, `sky check` fails with the same error. This is the
soundness gate — editor integrations should use it directly.

### `sky watch [path]`

File-watch-driven hot rebuild + restart. Watched scope is a strict
allowlist: `sky.toml` + the entry-point's directory (recursive `.sky`
walk) + `tests/` at the project root if present. Generated dirs
(`sky-out/`, `.skycache/`, `.skydeps/`, `node_modules/`, `.git/`) are
excluded.

```bash
sky watch                           # entry: src/Main.sky
sky watch src/Main.sky              # explicit entry
sky watch --no-run                  # rebuild only (don't spawn)
sky watch --clear                   # clear screen between rebuilds
sky watch --interval=200            # poll interval ms (default 200)
sky watch --debounce=150            # debounce after a change (default 150)
sky watch --kill-timeout=3000       # SIGTERM grace before SIGKILL
sky watch --watch=docs/notes.md     # additional path (repeatable)
```

**Build-error policy:** on a failing rebuild, the previously-running
binary stays alive. The next successful build kills + respawns. A
typo halfway through a save doesn't tear down the dev session.

**Caches reused:** `.skycache/source.hash` (full short-circuit on
unchanged source), `.skycache/lowered/` (per-module IR),
`.skycache/ffi/*.skyi` (HM types — never regenerated by watch).
Typical warm rebuild: 1-3 s.

**Signals:** Ctrl-C and SIGTERM both go through the clean-teardown
path. Sky.Live's SSE handshake auto-reconnects post-restart (banner
shows "Reconnecting…" for ~1 s then clears).

### `sky doc [target] [--list] [--serve] [--tui] [--port N]`

Browsable API documentation for the Sky stdlib + project + deps.
Four modes:

```bash
sky doc Sky.Core.String          # terminal: list every symbol with its HM signature
sky doc List                     # shorthand for Sky.Core.List
sky doc --list                   # print every documented module
sky doc --serve                  # HTTP doc server (auto-opens browser)
sky doc --serve --port 8081      # custom port
sky doc --tui                    # interactive terminal doc browser (Sky.Tui)
```

`--serve` and `--tui` are mutually exclusive — `--serve` runs the
Sky.Http.Server bundle; `--tui` runs the Sky.Tui bundle. Both
consume the same on-disk catalogue rendered to `.skycache/doc-out/`
under the project root.

The HTTP server (default `:8080`) renders:

* **Per-module pages** with HM signatures, Markdown-rendered doc
  comments, and an in-module symbol filter (counter shows `X / Y`).
* **Fuzzy search** by name, module, OR **type signature**
  (Hoogle-style, case-insensitive — `string -> int` or
  `String -> Int` both find `String.length`, `String.toInt` etc.).
* **FFI binding browsing** — every imported Go-pkg FFI dep lists its
  surface alongside the stdlib.
* **Live reload** if the underlying project changes (re-runs the
  index build on each request).

The server is a Sky.Live mini-app bundled into the compiler binary
(`sky-bundled/doc/`), spawned as a child + reverse-proxied behind
the `sky doc --serve` entry point.

`--tui` runs the Sky.Tui sibling (`sky-bundled/doc/src/MainTui.sky`)
which reads the same JSON catalogue and renders an interactive
terminal view: ↑/↓ navigate, Enter expands the highlighted entry,
`/` focuses the search box, Esc clears, Ctrl-C quits.

### `sky doctor [--fix] [--verbose]`

Project + environment health checks. v0.15.48 shipped **15 checks**
total — the foundational 5 (`sky.toml` syntax, stale `.skycache/`,
stale `sky-out/`, port-in-use, missing FFI) plus 10 tooling-polish
additions:

| Check id | Severity | What it covers |
|---|---|---|
| `go-toolchain` | error | Go ≥ 1.22 on PATH |
| `ffi-cache-orphan` | warn | `.skycache/ffi/*.skyi` with no `.skydeps/` source |
| `missing-lockfile` | info | `.skydeps/` populated but no `sky.lock` |
| `auth-secret-short` / `auth-secret-missing` | error / warn | `SKY_AUTH_TOKEN_SECRET` ≥ 32 bytes when `[live]` / `[auth]` set |
| `ci-parity` | info | `.github/workflows/ci.yml` invokes `sky build` / `cargo test` / verify-*.sh |
| `stdlib-version-drift` | warn (fixable) | `.skycache` generated by a different Sky compiler version |
| `toml-unknown-section` | info | sky.toml top-level keys outside the known set |
| `subapp-bin-missing` | warn | `[subapp]` `bin = "..."` paths are executable |
| `check-smoke` | info | reminder to run `sky check` when build is current |
| `govulncheck-*` | info | flags govulncheck availability for Go-runtime CVE scanning |

The Sky compiler repo also gets `mem-guard` (Info: running
mem-guard.sh check) — gated to the compiler repo so user projects
don't see it.

```bash
sky doctor                       # report only
sky doctor --fix                 # apply safe fixes (clean stale caches, etc.)
sky doctor --verbose             # print check-id alongside each finding
```

Exit codes: `0` clean, `1` warnings, `2` errors. CI-friendly.

### `sky verify [example]`

CI canonical runtime check. Iterates every directory under `examples/`
(or the named one), builds, runs, and asserts runtime behaviour:

- HTTP examples: hits `/` (and any routes declared in `examples/<n>/verify.json`)
  and checks status codes + body substrings.
- GUI examples (Fyne): skipped on headless CI via `SKY_SKIP_GUI=1`.

Output lines: `runtime ok: <name>`, `FAIL scenario: ...`, `FAIL build: ...`,
`[skip] <name>: ...`. Exit code is non-zero if any example fails.

Scenario file format:

```json
{
    "requests": [
        { "method": "GET", "path": "/",           "expectStatus": 200, "expectBody": ["Hello"] },
        { "method": "GET", "path": "/api/status", "expectStatus": 200, "expectBody": ["status"] }
    ]
}
```

### `sky test <file>`

Run a Sky test module. See [`testing.md`](testing.md).

## Database

### `sky db status [FILE]` · `sky db migrate [FILE]`

Inspect and apply `Std.Db` schema migrations. `FILE` defaults to
`src/Main.sky`.

```bash
sky db status      # report applied / pending / drifted migrations, then exit
sky db migrate     # apply all pending migrations in order, then exit
```

Both build the project, then run it in **DB-ops mode** — the app's
`Db.migrate` call does the work and exits *before serving*. The
underlying mechanism is the `SKY_DB_OP` env var (`status` / `migrate`),
usable directly in a deploy pipeline: `SKY_DB_OP=migrate ./sky-out/app`.

- `sky db status` exits **non-zero on drift** (an applied migration
  whose SQL was edited) — use it as a CI schema-drift gate.
- `sky db migrate` exits non-zero if a migration fails — run it as a
  pre-cutover deploy step so a bad migration blocks the rollout.
- There is no `migrate <singlefile>`: migrations are an ordered,
  checksum-tracked set; `migrate` always applies every pending one.

See [Sky.Db — Schema migrations](../skydb/overview.md#schema-migrations).

### `sky db migrate --gen [NAME]` — file-based migrations (no live DB)

When you define your schema with `Std.Db.Store` + `Std.Codec` and expose a
`db : Store.Project` binding, `--gen` derives migration **files** from the
types — **no database connection required**:

```bash
sky db init                      # scaffold db/migrations/ + db/schema.json (once)
sky db migrate --gen init        # first migration: CREATE TABLE for every store
# …add a field to a record, then:
sky db migrate --gen add_stock   # → addColumn (required cols get a safe backfill DEFAULT)
sky db status                    # ✓ applied / ○ pending per committed file vs the ledger
sky db migrate                   # apply the committed db/migrations/*.json to the live DB
sky db seed                      # run the entry module's seed : Db -> Task Error ()
```

`sky db status` compares the committed files against the live `_sky_migrations`
ledger and **exits non-zero while anything is pending** — a ready-made "is this
DB up to date?" deploy gate. `sky db seed` runs the `seed` binding your entry
module exposes (`module Main exposing (main, db, seed)`), against the live DB.

### `sky db push` — no-migration-files dev sync

```bash
sky db push        # create missing tables + add new columns to match your types
```

The fast prototyping loop (Prisma-style `db push`): syncs the live DB to your
current `db : Store.Project` with **no migration files** — creates each missing
table and adds any new (nullable) columns. Additive + idempotent; never drops or
retypes. Use `sky db migrate --gen` once your schema stabilises and you want
reviewable, committed history for production.

### `sky db reset [table]` · `sky db drop [table]` — destructive resets

```bash
sky db reset            # empty EVERY declared table (keep schema + the ledger)
sky db reset users      # empty just the `users` table
sky db drop             # drop EVERY declared table + `_sky_migrations`
sky db drop users       # drop just the `users` table (ledger untouched)
```

Both operate on the tables your entry module declares via `db : Store.Project`
(each `Table` carries its `name` / `cols` / `pk`) — **not** on other tables that
happen to share the database.

- **`sky db reset`** EMPTIES the data and resets autoincrement counters, but
  KEEPS the schema and the `_sky_migrations` ledger. On Postgres it runs one
  `TRUNCATE … RESTART IDENTITY CASCADE`; on SQLite it `DELETE`s each table and
  clears `sqlite_sequence` (foreign-key enforcement is toggled off for the
  operation). The fast "wipe my dev data, keep the tables" loop.
- **`sky db drop`** removes the tables. Dropping ALL declared tables also drops
  `_sky_migrations`, returning the database to a fresh "never ran migrate/push"
  state; a single-table drop (`sky db drop users`) leaves the ledger alone.
  Uses `DROP TABLE IF EXISTS … CASCADE` (Postgres) / `DROP TABLE IF EXISTS …`
  with FK enforcement off (SQLite).

Both are **destructive**, so both prompt before doing anything:

```
This will reset 3 table(s) in sqlite — type 'yes' to continue:
```

- `--yes` / `-y` skips the prompt (scripts, CI, container entrypoints).
- On a **non-TTY** without `--yes`, the command refuses rather than guess.
- In **production** (`ENV` / `SKY_ENV` in `{production, prod, staging}`) it
  refuses unless `--yes` is passed explicitly.

**Scope note.** `reset` / `drop` only touch the tables your `Store.Project`
declares. For a TOTAL wipe of a shared database (every table + extensions +
sequences, including ones Sky doesn't know about), use your database's own
tooling — e.g. `DROP SCHEMA public CASCADE; CREATE SCHEMA public;` on Postgres,
or delete the SQLite file.

### `sky db start` · `sky db stop` · `sky db ps` — the local PostgreSQL cluster

Every verb above talks to a database. These three *are* the database: they
supervise a local PostgreSQL cluster for the project you are standing in, so
development runs the same engine production does instead of the SQLite that
quietly diverges from it. The design is
[`docs/skydb/embedded-postgres.md`](../skydb/embedded-postgres.md).

```bash
sky db start        # initdb on first use, then start; already running is a no-op
sky db ps           # this project's cluster
sky db ps --all     # every Sky-managed cluster on the machine
sky db stop         # stop this project's cluster (pg_ctl stop -m fast)
sky db stop --all   # stop all of them
```

```
$ sky db start
sky db start: PostgreSQL 16.3 running (pid 41277).
  data:   /Users/dev/shop/.skydata/pg
  socket: /tmp/sky-9f2c1a4b7e03d5c8
  log:    /Users/dev/shop/.skydata/postgres.log

Connect with:
  psql -h /tmp/sky-9f2c1a4b7e03d5c8 postgres
  DSN: postgresql:///postgres?host=/tmp/sky-9f2c1a4b7e03d5c8
```

- **One cluster per project**, in `.skydata/pg/` (gitignored by `sky init`).
  `rm -rf .skydata` resets exactly one project, and two projects on different
  PostgreSQL majors never fight.
- **A unix socket, never a TCP port** — so two `sky db start`s cannot race over
  a port and nothing is exposed to the network. The socket lives in a short
  hashed directory *outside* the project (`$XDG_RUNTIME_DIR/sky/<hash>/`, else
  `/tmp/sky-<hash>/`), because `sockaddr_un` caps a socket path at ~107 bytes
  and a deeply nested project overflows it.
- **Tuned small** — `shared_buffers = 32MB` against PostgreSQL's 128MB default,
  so an idle project cluster costs tens of megabytes rather than hundreds. Only
  resource knobs are set; nothing that changes what a query means.
- **A machine-level registry** at `~/.sky/clusters.json` is what lets
  `sky db ps --all` see clusters this shell did not start. It is reconciled on
  every read: a dead pid is erased, and a vanished data dir is dropped.
- **Idempotent by design.** Starting a running cluster and stopping a stopped
  one both succeed, so both are safe in a script or a shell trap.

The binaries are discovered, in order, from `SKY_POSTGRES_BIN`, then
`~/.sky/postgres/<version>/bin` (pinned version first, then newest), then
`PATH`; a directory must hold `initdb`, `pg_ctl` and `postgres` to count.
`SKY_POSTGRES_BIN` set but incomplete is an error rather than a fall-through —
silently using a different installation is worse than the typo. Set `SKY_HOME`
to relocate the registry (tests and CI).

### `sky db provision --embed` — fetch PostgreSQL, no system install needed

Populates the middle entry of that discovery order with Sky's own build of
PostgreSQL, so a machine with no PostgreSQL at all can still run
`sky db start`.

```bash
sky db provision --embed                 # fetch, verify, install, pin
sky db provision --embed --force         # re-install over an existing cache
sky db provision --embed --from ./postgres-18.6-linux-amd64.tar.gz \
                        --checksum <sha256>   # offline, from a local file
```

```
$ sky db provision --embed
sky db provision: fetching https://github.com/anzellai/sky/releases/download/…
sky db provision: PostgreSQL 18.6 installed.
  /Users/dev/.sky/postgres/18.6/bin
  pinned in sky.toml ([database] postgresVersion = "18.6")

Next: sky db start
```

- **Verified before it is trusted.** The release's `SHA256SUMS` is fetched
  first, the downloaded archive is hashed on disk, and a mismatch installs
  nothing and says so. A corrupt or truncated download never reaches the cache.
- **Installed atomically** — extracted to scratch and renamed into place, so an
  interrupted provision leaves no half-populated `bin/` for discovery to find.
- **Idempotent.** Already provisioned is a fast success with no request made.
- **Pinned** in `sky.toml` as `[database] postgresVersion`, and discovery
  prefers that version, so a checkout on another machine gets the PostgreSQL the
  project states.
- **Offline-capable** via `--from` (with `--checksum`, or a `SHA256SUMS` beside
  the archive). `sky doctor --fix` pre-warms the cache for a project with
  `[database] embedded = true`. `SKY_POSTGRES_BUNDLE_URL` points at a mirror.

Bundles are built from source in Sky's CI for linux-amd64, linux-arm64,
darwin-amd64 and darwin-arm64; `psql` is deliberately excluded (GNU readline is
GPL-3.0). Windows is out of scope — use a system PostgreSQL and
`SKY_POSTGRES_BIN`.

> `sky db init` and `sky db status` belong to the migration engine documented
> above and are unchanged. The cluster verbs are `start` / `stop` / `ps` /
> `provision`.

### Running migrations as part of `sky run`

`sky run` takes `--db-push`, `--db-migrate`, and `--db-seed` flags that run those
steps (in that order) before the app starts — the container-entrypoint
"migrate-then-serve" shape. Any step failing aborts the run.

```bash
sky run --db-migrate --db-seed src/Main.sky   # apply committed migrations, seed, serve
sky run --db-push src/Main.sky                # dev: sync schema, serve
```

### Deploying — self-migrating binaries

When a project has `db/migrations/`, `sky build` **embeds the migrations into the
binary**. A deployed binary then self-migrates with **no source tree and no `sky`
toolchain on the host**:

```bash
SKY_DB_OP=migrate ./app   # apply the embedded migrations, print a summary, exit
SKY_DB_OP=status  ./app   # report applied / pending, exit
./app                     # (unset) boot + serve — no migration on boot
```

Run `SKY_DB_OP=migrate ./app` once as a deploy step (a single owner), then start
your replicas normally — booting without `SKY_DB_OP` never migrates, so scaling
out is safe. The connection comes from the app's usual config (`DATABASE_URL` /
`<PREFIX>_DB_PATH`).

How it works — gen builds a temporary DB-free entry (`main =
Store.dumpSchema db`), captures the type-derived schema, and diffs it against
the committed snapshot `db/schema.json`:

- **new table** → `createTable`; **new required column** → `addColumn NOT NULL
  DEFAULT <zero>` (backfills existing rows); **new `Maybe` column** → nullable.
- **dropped column / table / type change** → **quarantined** in a `destructive`
  array in the migration file, *never auto-applied* (the "never silently lossy"
  rule). On a TTY, gen instead **asks**: a drop can be resolved as a
  `(r)ename` (rewritten to one `renameColumn`, data preserved), a confirmed
  `(d)rop`, or `(s)kip`; a required new column can take a custom backfill
  default. Non-interactive (CI / piped) runs keep the safe quarantine defaults.

The output is committed to git (`db/migrations/*.json` + `db/schema.json`), so
review + history live in the repo. `sky db migrate` (with `db/migrations/`
present) applies the files through the same checksummed `_sky_migrations`
ledger as the code-defined path — at most once each, **dialect-correct** for
the live connection (one file → correct on SQLite *and* Postgres).

## Cache & cleanup

### `sky clean`

Removes:

- `sky-out/` — compiled binary + Go source
- `.skycache/` — generated FFI bindings, lowered-module cache, incremental state
- `.skydeps/` — Sky source dependencies (if any)
- `dist/` — release archives

Rebuild from scratch with `sky build` after `sky clean`.

## Dependencies

### `sky add [--go|--sky] <pkg>[@version]`

Adds a dependency. With neither flag, `sky add` **smart-resolves** whether the
target is a Go module or a Sky-source package and routes accordingly; `--go` /
`--sky` force one path and skip the probe.

For a **Go module** it fetches the module, runs the FFI inspector, generates the
FFI surface (`sky-ffi/<slug>.{skyi,kernel.json}` + `sky-ffi/go/<slug>_bindings.go`
— a gitignored build artifact, regenerated by `sky install`), and records the
dependency under `["go.dependencies"]`. For a **Sky package** it `git clone`s the
repo into `.skydeps/<slug>/` and records it under `[dependencies]`.

**Smart resolution (deterministic, content-based):**

1. An undotted import head (`net/http`, `io`) is stdlib → **Go**, no probe.
2. Otherwise the import path is cloned (a Sky package always lives at its repo
   root). If the clone succeeds and the repo has a `[lib]` table in its
   `sky.toml` → **Sky** (the cloned tree is kept, no re-fetch). If it has no
   `[lib]` (a Sky *app*, or a `src/`-less Go repo) the tree is discarded and it
   falls through to Go. If the clone fails (a Go package *subpath* like
   `github.com/stripe/stripe-go/v84/customer`, or a vanity path like
   `golang.org/x/sync/errgroup`, is not a clonable URL) it falls through to Go.
3. **Go:** `go get <path>@<spec>` resolves the package (it walks a package path
   up to its module and handles vanity redirects + major-version subdirs). On
   success the Go surface is generated.
4. If neither resolves → an actionable error suggesting `--go` / `--sky`.

Tie-break: a repo that is *both* a Go module and a Sky `[lib]` package resolves
to **Sky** (that is the case the smart default exists for —
`sky add github.com/org/sky-widgets`); pass `--go` to force the Go surface. A
private Sky package whose probe clone fails on auth looks like "not a repo" and
falls to Go — use `--sky` to force it.

**Version handling (Go-native):**

```bash
sky add github.com/google/uuid           # pin-by-default: records the resolved
                                          # version, e.g. "v1.6.0"
sky add github.com/google/uuid@v1.5.0    # pin an exact version / branch / commit
sky add github.com/google/uuid@latest    # explicit float — pulls latest on every
                                          # install/rebuild (a broken latest API
                                          # fails the build, forcing a fix)
sky add github.com/stripe/stripe-go/v84
```

- **Pin-by-default:** with no `@version`, `sky add` records the *exact* resolved
  version — reproducible builds out of the box. Write `@latest` (or edit
  `sky.toml` to `"latest"`) to opt into floating.
- **Re-adding upserts:** `sky add pkg@v2` when `pkg` is already declared updates
  the recorded version (the most recent `sky add` wins), so `sky.toml` never
  disagrees with the regenerated surface.
- The spec is passed straight to `go get pkg@<spec>` — `latest`, an exact
  `vX.Y.Z`, a branch, or a commit SHA. Non-Go semver *constraints* (`>=`, `~`,
  `^`) are rejected (Go uses MVS, not constraint solving).

**Forcing the kind — `sky add --go` / `sky add --sky`:**

```bash
sky add github.com/anzellai/sky-tailwind              # smart-resolves → Sky
sky add --sky github.com/anzellai/sky-tailwind@v1.2.0 # force Sky, pin a tag/SHA
sky add --go  github.com/some/polyglot-repo           # force Go for a repo that
                                                       # is both a Go module and
                                                       # a Sky [lib] package
```

`--sky` trusts the looser `src/` marker (you asserted it is a Sky package), so it
also accepts a Sky package that predates the `[lib]` convention. `--go` skips the
clone probe entirely (offline-friendly for a known Go module). A Sky package is
`git clone`d into the gitignored `.skydeps/<slug>/` and recorded under
`[dependencies]`, with the same version semantics as Go deps (pin-by-default;
`latest`/branch float; exact tag/SHA pin). `sky install` fetches every declared
`[dependencies]`; `sky build` is read-only and errors *`run 'sky install'`* if a
declared Sky dep isn't fetched; `sky remove --sky <path>` drops the entry and its
`.skydeps/` tree.

The FFI inspector (`sky-ffi-inspect`) is embedded in the `sky`
binary and self-provisions into `$XDG_CACHE_HOME/sky/tools/` on
first use — no separate install required. Cold start costs one
`go build` (~4s); subsequent calls are instant. Content-hashed
cache means `sky upgrade` invalidates the helper automatically.

Overrides, in probe order:

1. `$SKY_FFI_INSPECTOR` — absolute path to a pre-built helper.
2. `bin/sky-ffi-inspect` in the cwd or any ancestor (dev workflow).
3. Embedded fallback (default for installed binaries).

### `sky remove [--go|--sky] <pkg>`

Drops a dependency. With no flag it routes by which `sky.toml` section declares
the package: a `[dependencies]` entry (Sky package) removes the `[dependencies]`
line and its `.skydeps/<slug>/` tree; a `["go.dependencies"]` entry (Go module)
removes the line, the generated `sky-ffi/<slug>.*` surface, and the `go.mod`
require (`go mod tidy`). The routing is a local section lookup — no probe.
`--go` / `--sky` force one path.

### `sky install`

Regenerates the FFI surface from the declared dependencies. For each
`["go.dependencies"]` entry it ensures the `go.mod` pin, then generates any
**absent** surface and re-inspects each **present** one. Because `sky-ffi/` is a
gitignored build artifact (not a committed reproducibility anchor), a present
surface that no longer matches a fresh inspection — e.g. after a toolchain or
dependency-version change — is simply **refreshed** in place (reported as
`refreshed`), never a hard failure. Unchanged surfaces are left untouched
(`verified`). For each `[dependencies]` entry it clones any Sky package that is
absent or whose pinned ref drifted. Idempotent.

### `sky update`

Re-floats `latest`/branch-pinned `[go.dependencies]` to their newest versions and
regenerates their surfaces. **Exact-pinned deps (`vX.Y.Z` / commit SHA) are left
untouched** — bump a pin explicitly with `sky add pkg@<newversion>`.

### `sky upgrade`

Self-upgrades the `sky` binary from the latest GitHub release. After a successful
upgrade it **prints the release notes** for every version between your old and new
binary, flagging any release with breaking changes or a migration section.

```bash
sky upgrade           # upgrade, then print the notes for each version jumped
sky upgrade --notes   # preview the notes for (current, latest] WITHOUT upgrading
sky upgrade --force    # install the latest release even from a dev build
```

Notes come from the GitHub Release body (mirrored from
[`CHANGELOG.md`](../../CHANGELOG.md)); the fetch is best-effort and never fails
the upgrade.

**Automatic update nudge.** When you run a `sky` command interactively and a newer
release exists, `sky` prints a one-line "a new release is available — run `sky
upgrade`" note to stderr. The check is **cached** (`~/.cache/sky/update-check.json`,
`%LOCALAPPDATA%\sky` on Windows) and refreshed at most once a day in a **detached
background process**, so it never slows a command or blocks on the network. The
nudge itself prints at most once a day. It stays out of your way entirely when
output isn't interactive: it's suppressed unless stderr is a TTY (so scripts / CI
/ pipes never see it), for dev builds, and for `lsp` / `fmt` / `--version`. Set
`SKY_NO_UPDATE_CHECK=1` to disable it completely.

### `sky upgrade-claude`

Refreshes the cwd's `CLAUDE.md` from the template embedded in the
running `sky` binary at build time. Useful after `sky upgrade` —
the binary's embedded template moves with new releases (new stdlib
APIs, deprecation notes, current limitations) but a project's
`CLAUDE.md` is a snapshot taken at `sky init` time and won't auto-
update.

Behaviour:

- Always overwrites `./CLAUDE.md` (the file is AI-context, not
  hand-edited project source).
- Backs the prior file up to `./CLAUDE.md.bak` so an accidental run
  on a project that customised the file is recoverable.
- Prints a one-line summary including the byte-count delta and the
  `sky` version that produced the new template, so you can see at a
  glance whether the template actually changed.

```bash
$ sky upgrade-claude
Refreshed CLAUDE.md (118432 → 132422 bytes, from sky v0.11.1)
  previous version saved as CLAUDE.md.bak
```

## Dev console

### `sky console [--port N] [--tui]`

Runs the bundled Sky Console mini-app standalone. The console is
also auto-mounted at `/_sky/console` inside every Sky.Live and
Sky.Http.Server app in dev mode — the standalone form is for
ad-hoc inspection when you don't have (or don't want to start)
a host app.

```bash
sky console                # Sky.Live in the browser on :8025
sky console --port 8030    # different port
sky console --tui          # same UI rendered through Sky.Tui in your terminal
```

The source lives in `sky-bundled/console/` (embedded into the sky
binary via Template Haskell). First invocation builds into
`$XDG_CACHE_HOME/sky/console-<version>/` (~3–10 s); subsequent
runs are instant. Cache keys include the version string so
`sky upgrade` auto-invalidates.

Live and TUI variants share the same `State.sky` + `View.sky` —
only the entry-point module switches between `Live.app` and
`Tui.app`. Cached binaries are kept side-by-side (`app-live` /
`app-tui`) so switching backends doesn't trigger a rebuild.

**Env flags** (full reference in CLAUDE.md):

- `SKY_CONSOLE_EMBED=off` — opt-out of the auto-mount inside user
  apps (the standalone CLI still works).
- `SKY_DEV_BANNER=off` — opt-out of the floating "🔍 Console"
  banner without disabling the mount.
- `SKY_CONSOLE_URL=https://...` — override the banner's href
  (e.g. to point at a remote shared dashboard).
- `ENV=production` (or `SKY_ENV=…` outside `{dev, development,
  local}`) — production-mode gate; suppresses console + banner
  entirely and gates `/_sky/metrics` behind auth.

## Formatting

### `sky fmt <file>`

Opinionated, deterministic, no configuration (output is Elm-compatible):

- 4-space indent, no tabs.
- Leading commas for multi-line lists/records.
- Pipelines broken onto new lines.
- Refuses to overwrite if the formatter would lose more than one-third of the source lines (guards against partial-parse deletions).

## Editor integration

### `sky lsp`

Starts the Language Server over JSON-RPC / stdio. Used by the Helix and Zed integrations and any LSP-aware editor.

See [`lsp.md`](lsp.md) for configuration snippets.

## Layout

Sky writes generated artefacts to predictable locations — everything under `.skycache/` and `sky-out/` is regenerable. Nothing generated lives alongside your source.

```
project/
    src/                  -- your Sky source
    sky.toml              -- manifest
    .skycache/
        ffi/              -- .skyi signatures + kernel.json registries
        go/               -- generated Go FFI wrappers
        lowered/          -- incremental lowered-module cache
    .skydeps/             -- Sky source deps (if any)
    sky-out/              -- compiled binary + lowered main.go + rt/
```
