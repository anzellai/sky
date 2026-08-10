# Ledger — multi-tenant double-entry bookkeeping

Member **A** of the Layer-2 CI corpus. A small but real Sky.Live app whose
reason to exist is that **the identical source tree runs on SQLite *and*
PostgreSQL, with the driver chosen from the environment.**

Sign-up / sign-in, a chart of accounts, journal entries with an explicit
`(date, id)` order, a live balance view, a period close that runs off the
update loop, and a CSV export.

## The driver-from-env mechanism (verified, with citations)

There is **no driver name and no DSN anywhere in `src/`**. `Books.conn` calls
`Db.connect ()` — the unit form — and everything else follows from that:

| Step | Where |
|---|---|
| `sky.toml` `[database] path` / `url` is parsed into a `SKY_DB_PATH` **default** | `rust/crates/project/src/build.rs:808` |
| emitted as `rt.SetSkyDefault("DB_PATH", …)` in the program's `init()` | `rust/crates/lower/src/lower.rs:789` |
| `SetSkyDefault` is **set-if-unset**, so the shell (and `.env`) always win | `runtime-go/rt/env_prefix.go:107`, `runtime-go/rt/dotenv.go:37` |
| `Db.connect ()` reads `SKY_DB_PATH`, falling back to `DATABASE_URL` | `runtime-go/rt/db_auth.go:212-218` |
| the **driver is inferred from the DSN shape** — `postgres://` / `postgresql://` / libpq keyword form → `pgx`, everything else → `sqlite` | `runtime-go/rt/db_auth.go:337-351` (`detectDriver`) |
| every downstream concern (placeholders, DDL types, migration op rendering) branches on `d.driver` at run time | `db_auth.go:62-104`, `db_codec.go:140`, `db_migrate_ops.go:176`, `schema_kernel.go:131-161` |

```bash
# SQLite
SKY_DB_PATH=./ledger.db                                        ./sky-out/app
# PostgreSQL — same binary, same source, no rebuild
SKY_DB_PATH='postgres://user@host:5432/ledger?sslmode=disable' ./sky-out/app
DATABASE_URL='postgres://user@host:5432/ledger?sslmode=disable' ./sky-out/app
```

> **`[database] driver` in `sky.toml` is decorative.** It is parsed into
> `SKY_DB_DRIVER` (`build.rs:802`) and **nothing in `runtime-go/` ever reads
> it**. `driver = "postgres"` with a SQLite path opens SQLite, and vice versa.
> The DSN is the only thing that decides. (`docs/sky-toml.md:202` and
> `docs/skydb/overview.md:558` both advertise `SKY_DB_DRIVER` as a working
> env var — it is not.)

## The port comes from the environment too

`src/` contains **no port literal in bind position** — a Sky.Live app never
names its port in source. `sky.toml`'s `[live] port` is only a default
(`build.rs:820` → `rt.SetPortDefault` → `SetSkyDefault("LIVE_PORT", …)`,
`runtime-go/rt/dotenv.go:29`), and `SKY_LIVE_PORT` is read *after* the config
field in `runtime-go/rt/live.go:3665`, so the environment wins.

## Readiness line

Once the process is up it prints, in order:

```
ledger: driver=<sqlite|postgres>
ledger: allocate 100 -> [34, 33, 33] sum=100 exact=true
ledger: listening on <port>
Sky.Live listening on :<port>
```

`Sky.Live listening on :<port>` is emitted by the runtime immediately before
`ListenAndServe` (`runtime-go/rt/live.go:3916`) and is the deterministic
line for a harness to wait on. The three `ledger:` lines come from the
memoised `banner` CAF in `src/Main.sky`, so they print exactly **once** per
process regardless of how many sessions open.

## Running it

```bash
SKY=../../sky-out/sky            # the worktree compiler

$SKY build src/Main.sky          # embeds db/migrations/*.json into the binary

# --- SQLite arm -------------------------------------------------------
SKY_DB_PATH=./ledger.db SKY_DB_OP=migrate ./sky-out/app   # apply migrations
SKY_DB_PATH=./ledger.db $SKY db seed                      # demo tenant + journal
$SKY build src/Main.sky                                   # (see caveat below)
SKY_DB_PATH=./ledger.db SKY_LIVE_PORT=8471 ./sky-out/app

# --- PostgreSQL arm (identical source, identical binary) --------------
DSN='postgres://skytest@127.0.0.1:5432/ledger?sslmode=disable'
SKY_DB_PATH="$DSN" SKY_DB_OP=migrate ./sky-out/app
SKY_DB_PATH="$DSN" $SKY db seed
$SKY build src/Main.sky
SKY_DB_PATH="$DSN" SKY_LIVE_PORT=8472 ./sky-out/app

$SKY test tests/LedgerTest.sky   # 17 pure-domain assertions, no DB needed
```

> **Caveat for a harness.** `sky db seed` / `sky db status` / `sky db migrate
> --gen` build a *temporary* entry module and **overwrite `sky-out/app`**
> (`rust/crates/sky/src/main.rs:2089-2126`, `build_temp_db_entry`). Rebuild
> before starting the server, or the binary you launch is the seed/status
> shim. `SKY_DB_OP=migrate ./sky-out/app` does **not** clobber anything —
> it runs the embedded migrations in the real binary.

> Deleting a SQLite file without its `-wal` / `-shm` sidecars leaves the
> stale WAL behind and the next open fails with `disk I/O error (522)`.
> Remove all three.

## Endpoints a harness can assert on

| Path | What it proves |
|---|---|
| `GET /api/health` | serving; which driver actually opened |
| `GET /api/selfcheck` | `Money.allocate` residue: `{"whole":100,"parts":[34,33,33],"total":100,"exact":true}` |
| `GET /api/journal.json?org=N` | the journal in its guaranteed `entry_date ASC, id ASC` order |
| `GET /api/journal.csv?org=N` | `Std.Csv` export of the same rows |
| `GET /api/balances?org=N` | per-account `SUM(amount_minor)` + the trial balance (always `0`) |
| `GET /`, `/accounts`, `/journal`, `/close`, `/sign-in` | the Sky.Live pages |

The seed inserts four entries whose **ids ascend 1,2,3,4 while their dates go
Mar, Jan, Feb, Jan**, so only a genuine `ORDER BY entry_date ASC, id ASC` can
reproduce the expected `2, 4, 3, 1`. Insertion order cannot.

## Layout

```
sky.toml                 defaults only — every key is overridable from the env
db/migrations/*.json     COMMITTED, dialect-neutral migration ops
db/schema.json           type-derived snapshot (regenerated by `sky db migrate --gen`)
src/Books.sky            connection + row types + codecs + stores + `Store.Project`
src/Amounts.sky          Std.Money / Std.Decimal, allocation residue, double-entry invariant
src/Repo.sky             every DB access; tenant filter + explicit ordering live here
src/State.sky            TEA types (Page / Msg / Model / wire forms)
src/Update.sky           the reducer
src/Api.sky              Live.api JSON + CSV endpoints
src/View/Common.sky      shared Std.Ui vocabulary
src/View/SignIn.sky      sign-in + sign-up forms
src/View/Pages.sky       balances / accounts / journal / close
src/Main.sky             main, `db : Store.Project`, `seed : Db -> Task Error ()`
tests/LedgerTest.sky     17 pure-domain assertions
```
