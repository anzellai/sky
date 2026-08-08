# Std.Persist overview (BlueDB)

> **Status**: real and usable, not yet the default. `Std.Persist` is the
> user-facing surface over **BlueDB**, the Sky-native embedded data engine
> (Pebble + MVCC + serializable commit validation), *and* over SQLite/Postgres
> through the same verbs. The embedded engine, the unified API, serializable
> transactions, and Sky.Live reactivity are shipped and gated by
> `examples/56`–`examples/59` in the example sweep. `Std.Db` + `Std.Db.Store`
> remains the **pinned default** for record persistence — see
> [When to choose which](#when-to-choose-which). Read
> [Limitations](#limitations) before you build on this; several of them
> (embedded queries are full scans, reactivity is embedded-only) will decide
> whether it fits.

**One set of verbs, three backends.** Declare a `Collection` (a name + a
`Std.Codec` + a primary key + indexes) and the same
`get`/`put`/`insert`/`delete`/`query`/`count`/`transaction` verbs persist it to
the **embedded** BlueDB engine, to **SQLite**, or to **PostgreSQL**. The codec
drives JSON *and* the storage mapping — write it once. On the embedded backend
you additionally get **reactivity**: a Sky.Live Model field that stays live from
a query with no `Msg`, no `Sub`, and no manual re-fetch.

```elm
module Main exposing (main)

import Sky.Core.Task as Task
import Std.Codec as Codec exposing (Codec)
import Std.Persist as P
import Std.Log exposing (println)


type alias Todo =
    { id : String, title : String, priority : Int, done : Bool }


todoCodec : Codec Todo
todoCodec =
    Codec.auto { id = "", title = "", priority = 0, done = False }


todos : P.Collection Todo
todos =
    P.collection "todos" todoCodec
        |> P.key "id"
        |> P.index "priority"


main =
    P.connectKeyValue "data/app.bluedb"
        |> Task.andThen
            (\store ->
                P.insert store todos { id = "t1", title = "buy milk", priority = 2, done = False }
                    |> Task.andThen (\_ -> P.all store todos)
                    |> Task.andThen
                        (\rows ->
                            println ("todos: " ++ String.fromInt (List.length rows))
                        )
            )
        |> Task.run
```

No `CREATE TABLE`, no migration step, no driver in the code — the collection
declaration *is* the schema.

## When to choose which

`Std.Persist` is not a replacement for `Std.Db`; it is a different trade.

| Reach for | When |
|---|---|
| **`Std.Db.Store` + `Std.Codec`** *(the pinned default)* | Anything relational: joins, aggregates, reporting, a schema you evolve with committed file migrations (`sky db migrate --gen`), an existing SQL database, indexed lookups over large tables, or anything a DBA/analyst will also query. The mature, most-exercised path. |
| **`Std.Persist` on embedded** (`connectKeyValue`) | Single-process apps with a **modest working set** that want zero database to operate — a `Sky.Webview` desktop app, a CLI's local state, a single-replica internal tool — and especially a Sky.Live app that wants a **live-updating list** (see [Reactivity](#reactivity)), which is the one thing no other Sky backend offers. |
| **`Std.Persist` on relational** (`connectRelational`) | You want the collection-style API and one portable code path, but need SQLite or Postgres as the real store (multi-replica, backups, existing ops). Note the [parity subset](#backends-and-portability); reactivity does **not** work here. |

Three consequences to weigh before committing:

- **Reactivity is embedded-only.** A reactive binding on a relational
  connection never live-updates, and the runtime kills the process in production
  rather than serve a silently frozen list. See [Reactivity](#reactivity).
- **The embedded engine is single-writer and local.** Each replica has its own
  store directory; two replicas do **not** share data. Embedded is a
  single-instance choice.
- **Embedded reads are full collection scans today.** Declared indexes do not
  yet accelerate reads on the embedded backend — see
  [Indexes](#indexes-what-they-actually-do). Choose it for convenience and
  reactivity, not because you expect index-seek performance over a large
  collection.

## Collections

A `Collection a` is a name, a codec, a primary-key column, and zero or more
secondary indexes. It is a plain value — declare it once at the top level and
share it.

```elm
users : P.Collection User
users =
    P.collection "users" (Codec.auto { id = "", email = "", tenant = "", age = 0 })
        |> P.key "id"            -- primary-key column; defaults to "id"
        |> P.index "email"       -- secondary index (single-column, ascending)
        |> P.index "tenant"
```

**Column names come from the codec, not from the Sky field name.**
`Codec.auto` snake_cases (`priceMinor` → `price_minor`) — the same convention as
`Std.Db.Store`. `Codec.autoCamel` keeps camelCase; `Codec.autoWith` or a
hand-built `Codec.object` codec names columns explicitly. Every column string
you pass to `P.key`, `P.index`, `P.where_`, `P.orderAsc` / `P.orderDesc` must be
the **codec's** column name.

`P.collection` also accepts a non-record codec, but a scalar collection has no
columns to index or filter on — collections are meant for records.

### Indexes — what they actually do

Be precise about this, because the name is misleading in this version:

- On the **embedded** backend, a query is a **primary-key-ordered scan of the
  whole collection with the predicate evaluated in memory**. A declared index
  does **not** turn into a seek. What `P.index` buys you there is *transactional
  precision* — a declared, order-preserving column lets a serializable
  transaction record a tight index-range read-set instead of a coarse one, which
  means fewer false conflicts — and it feeds the reactive change-feed matching.
- On the **relational** backend the collection's table is created for you, and
  the underlying database plans the query as it normally would.

Indexes are **single-column and ascending only**. There are no compound
indexes, no unique secondary indexes, and no partial indexes. Order-preserving
column kinds (int / text / bool) get the tight range treatment; kinds that are
not order-preserving (`Money`, `Codec.map` wrappers, blobs) fall back to a
conservative witness — correct, just less selective.

One sharp edge: an index (or filter, or sort) naming a column that **is not in
the codec** does not raise an error. It is classified as not-order-preserving
and silently takes the conservative path. Only characters are validated (see
[Queries](#queries)), not existence — so check your column spelling against the
codec.

## Reads and writes

Every verb takes the connection first, then the collection.

| Verb | Type | Notes |
|---|---|---|
| `P.get conn coll key` | `Task Error (Maybe a)` | by primary key; `Nothing` when absent |
| `P.put conn coll rec` | `Task Error ()` | upsert by the record's own primary key |
| `P.insert conn coll rec` | `Task Error a` | fails on a duplicate primary key |
| `P.delete conn coll key` | `Task Error ()` | idempotent — a missing key is a no-op |
| `P.all conn coll` | `Task Error (List a)` | every record |
| `P.count conn coll` | `Task Error Int` | every record, counted |
| `P.selectRaw conn codec sql params` | `Task Error (List row)` | **relational only** — see below |

`P.insert` returns the persisted row on the embedded backend, so engine-filled
columns come back. On the relational backend it returns the record you passed
in, unchanged — if you rely on a database-filled default, read the row back with
`P.get`.

There is no update-by-query, no bulk insert, and no aggregate beyond `count`.
Read, modify, and `P.put` (inside a transaction when it matters), or drop to
`selectRaw` on the relational arm.

`P.selectRaw` is the JOIN / `GROUP BY` / aggregate escape hatch: you write the
SQL, a codec decodes each row into a typed projection. It is **relational-only**
— the embedded engine has no cross-collection join and returns an error for SQL
text. It also does not create the table first, so call it only after a verb that
does.

## Queries

Compose a `Query`, then run it with a terminal. Values bind as parameters, so
you never build a SQL string.

```elm
P.query orders
    |> P.where_ (P.eq "status" (P.string "open"))
    |> P.where_ (P.or_ [ P.gt "total_minor" (P.int 10000), P.eq "priority" (P.string "high") ])
    |> P.orderDesc "created_at"
    |> P.limit 50
    |> P.offset 100
    |> P.toList conn          -- terminals: toList / toMaybe / toCount
```

Leaves: `eq` · `neq` · `gt` · `gte` · `lt` · `lte` · `like` · `isNull` ·
`notNull` · `inList`. Combinators: `and_` · `or_` · `not_`. Multiple `where_`
calls AND together, so OR and nesting are first-class. Values are built with the
typed constructors `P.string` / `P.int` / `P.float` / `P.bool`. An empty
`inList` matches nothing.

Ordering is by call order (`orderAsc` / `orderDesc` append). Null placement is
**forced identical on every backend**: NULLs sort FIRST ascending, LAST
descending. `like` is forced **case-insensitive for ASCII** everywhere (`LIKE`
on SQLite, `ILIKE` on Postgres, ASCII case-folding in the embedded engine);
non-ASCII case-folding is *not* guaranteed and differs by backend.

Column and table identifiers are interpolated into SQL on the relational arm, so
they are validated on **both** arms before the query runs: an identifier must be
non-empty and match `[A-Za-z0-9_.]`, or the task fails with a clear error. This
is a character-class gate against injection — it does not check that the column
exists. A legitimate column name containing a hyphen or non-ASCII characters is
rejected; name your columns accordingly.

## Transactions

`P.transaction conn (\tx -> …)` runs the body inside a **serializable**
transaction with bounded, automatic conflict retry. The body is handed a
connection bound to the transaction — `get` / `put` / `query` on `tx` compose
atomically — and a body that returns `Task.fail` rolls the transaction back and
propagates the error.

```elm
module Main exposing (main)

import Sky.Core.Error as Error exposing (Error, ErrorKind(..))
import Sky.Core.Task as Task exposing (Task)
import Std.Codec as Codec
import Std.Persist as P
import Std.Log exposing (println)


type alias Account =
    { id : String, owner : String, balanceMinor : Int, closed : Bool }


accounts : P.Collection Account
accounts =
    P.collection "accounts"
        (Codec.auto { id = "", owner = "", balanceMinor = 0, closed = False })
        |> P.key "id"
        |> P.index "owner"


-- Column names are the CODEC's names: `Codec.auto` snake_cases, so the
-- `balanceMinor` field is the `balance_minor` column.
richOpenAccounts : P.Conn cap -> Task Error (List Account)
richOpenAccounts conn =
    P.query accounts
        |> P.where_ (P.eq "closed" (P.bool False))
        |> P.where_ (P.gte "balance_minor" (P.int 10000))
        |> P.orderDesc "balance_minor"
        |> P.limit 20
        |> P.toList conn


-- Read-modify-write, atomically. Works unchanged on every backend.
withdraw : P.Conn cap -> String -> Int -> Task Error ()
withdraw conn accountId amount =
    P.transaction conn
        (\tx ->
            P.get tx accounts accountId
                |> Task.andThen
                    (\found ->
                        case found of
                            Nothing ->
                                Task.fail (Error.withMessage "no such account" Error.notFound)

                            Just acct ->
                                if acct.balanceMinor < amount then
                                    Task.fail (Error.invalidInput "insufficient funds")

                                else
                                    P.put tx accounts { acct | balanceMinor = acct.balanceMinor - amount }
                    )
        )


isConflict : Error -> Bool
isConflict err =
    case err of
        Error Conflict _ ->
            True

        _ ->
            False


main =
    P.connectKeyValue "data/bank.bluedb"
        |> Task.andThen
            (\conn ->
                withdraw conn "a1" 500
                    |> Task.onError
                        (\err ->
                            if isConflict err then
                                println "too much contention — try again"

                            else
                                Task.fail err
                        )
                    |> Task.andThen (\_ -> richOpenAccounts conn)
                    |> Task.andThen
                        (\rows -> println ("rich accounts: " ++ String.fromInt (List.length rows)))
            )
        |> Task.run
```

**What SERIALIZABLE actually means here.** The *observable* contract is the same
on all three backends — concurrent transactions behave as if run one at a time,
and a transaction that cannot be serialized fails with the typed `Conflict`
error — but the mechanism differs, and so does where the cost lands:

| Backend | Mechanism | What that means in practice |
|---|---|---|
| Embedded (`connectKeyValue`) | SSI — optimistic, commit-time validation against the transaction's read set, including index ranges | Readers never block writers. Conflicts surface *at commit*, so a transaction can do all its work and then fail. |
| PostgreSQL | `BEGIN … SERIALIZABLE` (Postgres's own SSI) | Same shape as embedded. |
| SQLite | `BEGIN IMMEDIATE` (upfront write lock) plus a single-connection clamp | Writers serialize by taking the lock up front rather than validating at commit. This is **not** SSI; it is serializable because there is only ever one writer. |

Note what that last row implies: on SQLite the isolation level is not really the
thing doing the work — the single-writer clamp is. Write-skew is genuinely
prevented, but if you want to *test* that your code is safe under real
optimistic concurrency, test it against embedded or Postgres.

Retries are bounded and automatic. When they are exhausted the task fails with
`Error` kind `Conflict` on every backend, so one handler covers all three.
`Error.isRetryable` **does not** treat `Conflict` as retryable (it covers
`Timeout` / `Network` / `Unavailable`), so match the kind yourself — that is
what `isConflict` above is for.

By the time you see this the automatic retries have already happened, so surface
"someone else changed this — try again" to the user rather than looping.

## Reactivity

On the **embedded** backend a collection query can drive a Sky.Live Model field
directly. `P.liveInto` builds a binding; `Live.withReactive` registers it. When
a write changes the query's result set the framework re-runs the query, folds
the fresh list into the Model, and the normal diff repaints. No `Msg`, no `Sub`,
no manual refresh.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Codec as Codec
import Std.Cmd as Cmd
import Std.Live as Live
import Std.Persist as P
import Std.Sub as Sub
import Std.Ui as Ui


type alias Message =
    { id : String, room : String, body : String, at : Int }


messages : P.Collection Message
messages =
    P.collection "messages" (Codec.auto { id = "", room = "", body = "", at = 0 })
        |> P.key "id"
        |> P.index "at"


-- A memoised top-level binding: ONE engine per path, opened when first forced.
-- `withReactive` needs a concrete Conn while the config is built, which is what
-- the `Sync` variant is for. On open failure it yields an inert handle rather
-- than failing to type-check.
store : P.Conn P.KeyValue
store =
    P.connectKeyValueSync "data/chat.bluedb"


type alias Model =
    { messages : List Message }


type Msg
    = NoOp


type Page
    = HomePage


init : a -> ( Model, Cmd Msg )
init _ =
    ( { messages = [] }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update _ model =
    ( model, Cmd.none )


view model =
    Ui.layout []
        (Ui.column [] (List.map (\m -> Ui.text m.body) model.messages))


subscriptions : Model -> Sub Msg
subscriptions _ =
    Sub.none


main =
    Live.app
        (Live.config
            { init = init
            , update = update
            , view = view
            , subscriptions = subscriptions
            , routes = [ Live.route "/" HomePage ]
            , notFound = HomePage
            }
            |> Live.withReactive
                (\_ ->
                    [ P.liveInto store
                        messages
                        (P.query messages |> P.orderDesc "at" |> P.limit 50)
                        (\rows model -> { model | messages = rows })
                    ]
                )
        )
        |> Task.run
```

Writes are ordinary `P.insert` / `P.put` calls from `update` (via
`Cmd.perform`); you do **not** refresh the list afterwards. `withReactive` takes
`model -> List (LiveBinding model)`, so bindings may depend on Model state and
one app can hold several over different collections — the element type is hidden
inside the binding, so a heterogeneous list type-checks.

`liveInto` + `withReactive` is the **entire** reactive surface. There is no
lower-level `watch` / `Change` / `Subscription` API to drop down to.

### The real constraints

This is the most constrained part of the module. All of these are load-bearing.

**1. Embedded connections only.** `P.liveInto` accepts any `Conn` and
type-checks against a relational one, but a relational connection cannot
live-deliver: the binding paints once from its initial query and then never
updates again. Rather than let that go stale silently, the runtime gates it:

- **Development** — the binding paints once and the runtime logs a
  `reactive.sql-unsupported` warning telling you it will never update.
- **Production** — the process prints `[sky.persist] FATAL: …` and exits.

The same classification applies to an embedded connection that failed to open:
`connectKeyValueSync` yields an inert handle on failure, which cannot
live-deliver either, so a bad store path is a hard exit in production rather
than a silently empty list.

**2. Single-instance assertion.** The embedded engine is a local store, so a
multi-replica deploy using reactivity would silently serve stale reads — a write
on replica 1 never reaches a session on replica 2. Topology is not detectable at
runtime, so an explicit operator assertion is required: a reactive app on a
local single-writer backend outside development **must** set
`SKY_DATA_REACTIVE_SCOPE=single-instance` (or `[data] reactiveScope =
"single-instance"` in `sky.toml`), or the process exits. `single-instance` is
the only accepted value; matching is case- and whitespace-insensitive. In
development it warns once so you set it before the first staging deploy.

This is an *assertion*, not a check. Asserting it while actually running several
replicas restores exactly the staleness it exists to prevent.

**3. The check runs on the first session, not at process start.** Both fatal
verdicts above are evaluated once per process, the first time a session starts
its reactive bindings. An app can therefore pass a health check and then exit
when the first user loads a page. Exercise a reactive page in staging — do not
treat "it started" as "it booted clean".

**4. Live updates are scoped to the session's verified tenant.** A write is
tagged with the writing goroutine's session tenant — the framework-verified
`tenant` claim, never a value read off a record column — and a subscription only
ever sees writes carrying its own tenant. The partition is strict and
fail-closed: **a write made outside a live session carries the empty tenant.**
That includes background jobs, CLI paths, plain `Sky.Http.Server` handlers, and
startup seeding. So if your sessions carry a `tenant` claim, a write from a
background job will **not** wake their reactive bindings, and there is no
override to stamp one. Single-tenant apps with no tenant claim are unaffected —
everything is tagged with the empty tenant and matches.

**5. It is a whole-query re-run, not a delta.** The framework re-runs your query
and replaces the list. Bound it with `limit`: a reactive binding over an
unbounded collection re-reads the entire result set on every matching commit,
and on the embedded backend that read is a full collection scan. Bursts of
commits are coalesced into one re-query, and delivery is at-least-once — a
duplicate refresh is harmless because the whole list is replaced.

**6. A wedged binding fails quietly.** If a binding's refresh panics, that
binding stops updating; the process survives and the app is not told. Treat a
list that has stopped moving as a possible symptom.

## Backends and portability

| | Embedded (`connectKeyValue`) | SQLite (`connectRelational`) | PostgreSQL (`connectRelational`) |
|---|---|---|---|
| CRUD verbs, query builder, ordering, paging | ✅ | ✅ | ✅ |
| Serializable transactions + typed `Conflict` | ✅ (SSI) | ✅ (single-writer + `BEGIN IMMEDIATE`) | ✅ (SSI) |
| Forced NULL ordering + ASCII-insensitive `LIKE` | ✅ | ✅ | ✅ |
| `selectRaw` (JOIN / aggregate) | ❌ errors | ✅ | ✅ |
| Reactivity (`liveInto`) | ✅ | ❌ | ❌ |
| Indexed reads | ❌ full scan | ✅ | ✅ |
| Multi-replica | ❌ local store | ❌ single file | ✅ |
| Schema handling | implicit from the collection | implicit `CREATE TABLE IF NOT EXISTS` per verb | same |

`connectKeyValue` takes an explicit directory path in code — one engine is
shared per path, so a memoised top-level binding gives the whole app one store.
`connectRelational ()` reads the project's data config, using the same
resolution as `Db.connect ()`.

**The portable subset is guaranteed, not incidental.**
`examples/57-persist-parity` runs the *same* collection and query source against
the embedded engine and against SQL and asserts the results are identical for
equality, nullable and non-null `ORDER BY`, integer ranges over discriminating
values, `inList`, `isNull` / `notNull`, ASCII `LIKE`, `count`, and `insert` —
and fails the build if they diverge. Anything outside that subset — non-ASCII
`LIKE`, `selectRaw`, reactivity — is explicitly not covered.

## Configuration

The relational arm reads the unified `[data]` section of `sky.toml`, which also
subsumes the legacy `[database]`, `[live] store`, and `[analytics]` sections and
**wins** over them on conflict, regardless of file order:

```toml
[data]
url = "postgres://user:pass@host/db"   # alias: path
reactiveScope = "single-instance"      # required for reactive Persist outside dev
```

**The DSN alone selects the driver.** A `postgres://` or `postgresql://` URL
(or a libpq keyword string containing `host=` and `user=`) opens PostgreSQL;
anything else is treated as a SQLite path. `[data] driver` / `[database] driver`
is accepted by the config parser but **the runtime does not read it** — setting
`driver = "postgres"` alongside a `./app.db` path silently opens SQLite. Set the
URL correctly and treat `driver` as documentation.

The full key list and env-var mapping is in
[`../sky-toml.md`](../sky-toml.md#data-v020--unified-data-config). The embedded
store path is **not** configured here — it is the argument you pass to
`connectKeyValue` / `connectKeyValueSync`.

`sky data` is an exact alias for `sky db`, matching the unified `[data]`
spelling. Its verbs operate on `Std.Db` projects — see
[Limitations](#limitations).

## Limitations

Current and honest, as of this version.

**Maturity and tooling**

- **Not the default.** `Std.Db.Store` + `Std.Codec` is still the pinned default
  and the far more exercised path.
- **No migration tooling for collections.** `Std.Db.Store` exposes
  `Store.toTable` / `Store.project` so `sky db migrate --gen` can diff a schema;
  `Std.Persist` has no equivalent. The relational arm issues an idempotent
  `CREATE TABLE IF NOT EXISTS` before each verb, so a *new* collection appears by
  itself — but a *changed* collection (renamed or retyped column) is not migrated
  for you, on either backend.
- **The Sky Console's read-only Data tab is not shipped.** The fail-closed
  data-access layer behind it exists; the console UI does not.

**Query and collection model**

- **Embedded reads are full collection scans** with the predicate applied in
  memory. Declared indexes serve transaction read-set precision and the reactive
  change feed, not read speed. A stored-index seek is not implemented.
- **Indexes are single-column and ascending only** — no compound, unique, or
  partial indexes.
- **No `unique` / `serial` / generated-column builders** on `Collection`; only
  `key` and `index`.
- **The generated primary-key sequence is an in-process counter**, not durable
  across restarts.
- **No update-by-query, no bulk verbs, no aggregates beyond `count`.**
- **No cross-collection joins on embedded.** `selectRaw` is relational-only and
  does not create the table first.
- **A filter, sort, or index column that is not in the codec degrades silently**
  to the conservative path instead of erroring.
- **Identifiers are restricted to `[A-Za-z0-9_.]`** on both arms, so a
  hyphenated or non-ASCII column name is rejected even on embedded, where no SQL
  is built.
- **`insert` returns a different thing per backend** — the engine-filled row on
  embedded, the record you passed on relational.
- **Non-ASCII `LIKE` is backend-specific** and outside the parity contract.

**Reactivity**

- **Embedded-only, single-instance, tenant-partitioned, whole-query re-run,
  at-least-once, silent on a wedged binding, and gated on first session rather
  than at boot.** All detailed in
  [The real constraints](#the-real-constraints). Cross-instance live delivery
  (Postgres `LISTEN` / `NOTIFY`) is not implemented.
- **There is no lower-level watch API** — `liveInto` is the only entry point.

**Packaging and adjacent surfaces**

- **A relational-only app still links the embedded engine.** Every universal
  verb carries an embedded branch and dead-code elimination is per-binding, not
  per-branch, so the Pebble subtree is linked in even if you only ever call
  `connectRelational`. It builds and runs correctly; it costs tens of megabytes
  of binary size.
- **Sky.Live session storage is not backed by `Std.Persist`.** Sessions use the
  existing `Std.Live` stores (`memory` / `sqlite` / `redis` / `postgres`);
  moving them onto a collection is an open design decision
  (`docs/bluedb/adr-001-sessions-as-collection.md`). `[data] sessionStore` /
  `sessionPath` simply re-map onto those stores.
- **On a SQL session backend, an acknowledged Sky.Live transition survives a
  process crash but not host power loss** — those stores run with
  `synchronous=NORMAL`. There is no per-write durability marker to opt into a
  stricter mode.
- **`[data] sessionVersion` must be bumped by hand** when the session Model
  changes meaning in a way the encoder cannot see (reordered ADT constructors,
  remapped integers). Forgetting decodes stale bytes under the new meaning.

## See also

- `sky doc Std.Persist` — the live, never-drifting API with exact signatures.
- [`../skydb/overview.md`](../skydb/overview.md) — `Std.Db` / `Std.Db.Store` /
  `Std.Codec` / migrations, the pinned default.
- [`../skylive/overview.md`](../skylive/overview.md) — the Sky.Live TEA loop
  `withReactive` plugs into.
- [`../sky-toml.md`](../sky-toml.md) — `[data]` keys and env vars.
- `examples/56-persist-embedded` · `examples/57-persist-parity` ·
  `examples/58-persist-relational-only` · `examples/59-persist-live` — the four
  runnable examples this document describes.
