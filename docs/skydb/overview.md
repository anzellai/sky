# Std.Db overview

> **Status**: the Rust compiler (`rust/`, `cargo build --release -p sky`)
> is the primary Sky compiler; the Haskell compiler is preserved under
> `legacy-haskell-compiler/`. Verified by the example sweep + compiler test
> suite (`cargo test` + xtask gates). See
> [`../compiler/journey.md`](../compiler/journey.md) for the changelog.


**One database API, two backends.** `Std.Db` is a thin, parameter-safe wrapper over `database/sql` that works identically against SQLite and PostgreSQL. Pick the driver in `sky.toml`; never touch it again in your code.

```elm
module Main exposing (main)

import Std.Db as Db
import Sky.Core.Task as Task
import Std.Log exposing (println)


main =
    Db.connect ()                       -- reads `[database]` from sky.toml
        |> Task.andThen
            (\db ->
                Db.exec db
                    "CREATE TABLE IF NOT EXISTS todos (id INTEGER PRIMARY KEY, text TEXT NOT NULL, done INTEGER NOT NULL DEFAULT 0)"
                    []
                    |> Task.andThen (\_ -> Db.exec db "INSERT INTO todos (text) VALUES (?)" [ "Write the doc" ])
                    |> Task.andThen (\_ -> Db.query db "SELECT id, text, done FROM todos" [])
                    |> Task.andThen
                        (\rows ->
                            println ("Got " ++ String.fromInt (List.length rows) ++ " todos")
                        )
            )
        |> Task.run
```

## `Std.Db.Store` + `Std.Codec` — codec-driven persistence (recommended default)

For record-shaped tables, write **one `Codec`** per type (with `Std.Codec`) and
let `Std.Db.Store` drive the schema, the reads, and the writes — no hand-written
SQL, no row mappers, no `SqlValue` lists. The same codec also serves JSON.

```elm
import Std.Codec as Codec exposing (Codec)
import Std.Db.Store as Store exposing (Store)
import Std.Db as Db exposing (SqlValue(..))

type alias User =
    { id : String, email : String, verified : Int, createdAt : String }

users : Store User
users =
    Store.fromCodec "users" (Codec.auto { id = "", email = "", verified = 0, createdAt = "" })
        |> Store.primaryKey "id"        -- or Store.serial "id" for an auto-increment Int PK
        |> Store.unique "email"
        |> Store.defaultInt "verified" 0
        |> Store.defaultNow "createdAt"

-- Store.create conn users            : Task Error ()      (dialect-correct DDL)
-- Store.insert conn users user       : Task Error Int
-- Store.all    conn users            : Task Error (List User)
-- Store.findBy conn users "id" "u1"  : Task Error (Maybe User)
```

**`Codec.auto blank`** reflection-derives the codec from a zero-value witness:
scalars → typed columns, `Maybe` → nullable, list / nested-record / data-ADT →
JSON TEXT blob, nullary enum → readable name. Columns + JSON keys are
**snake_case** by default (`priceMinor` → `price_minor`); **`Codec.autoCamel`**
keeps `Codec.autoWith [ ("active", intBool) ] blank` overrides one field's codec (a Bool stored 0/1, a custom enum) while auto-deriving the rest; camelCase; a custom mapping uses `Codec.object`/`Codec.field "col" .field`.

### Schema / DDL builders

Pipe these onto the store — each accepts the record **field** name *or* the snake
column (a typo fails fast with the column list):

| Builder | Effect |
|---|---|
| `Store.primaryKey "id"` | mark the PK (String/UUID key you provide) |
| `Store.serial "id"` | auto-increment PK (`INTEGER … AUTOINCREMENT` / `BIGSERIAL`) |
| `Store.unique "email"` | `UNIQUE` constraint |
| `Store.defaultNow "created_at"` | `DEFAULT now()`/`datetime('now')`, DB-stamped on insert |
| `Store.touchOnUpdate "updated_at"` | stamped on insert **and auto-bumped to `now()` on every `update`** — no raw SQL |
| `Store.defaultText/defaultInt "col" v` | literal column `DEFAULT` |
| `Store.defaultWith "id" (\_ -> SqlValue)` | **app-side** computed default at insert (e.g. a UUID PK via the unit-arg `Task.run` idiom) |
| `Store.generated [ "id", "created_at" ]` | columns `insert`/`update` OMIT so the DB fills them |

### Writes

`insert` · **`insertMany`** (one multi-row INSERT — bulk / time-series) · `update`
(by PK) · `updateWhere` (by `Cond`) · **`upsert`** (`INSERT … ON CONFLICT(pk) DO
UPDATE` — idempotent config rows; needs a non-generated PK) · `delete` ·
`deleteWhere`.

### Reads — query builder

Composable, injection-safe `Cond` values; you never touch a SQL string:

```elm
Store.query users
    |> Store.where_ (Store.eq "verified" (SqlInt 1))
    |> Store.where_ (Store.or_ [ Store.like "email" "%@work.io", Store.gt "createdAt" (SqlString cutoff) ])
    |> Store.orderDesc "createdAt"
    |> Store.limit 20
    |> Store.toList conn        -- terminals: toList / toMaybe / count
```

Leaves (`eq`/`neq`/`gt`/`gte`/`lt`/`lte`/`like`/`isNull`/`notNull`/`inList "col" v`)
combine with `and_`/`or_`/`not_`; multiple `where_` clauses AND together (so OR /
nesting is first-class). `Store.sqlOf codec value` filters by a **typed** value
(enum / `Money` / `Time` / a `Codec.map` wrapper) via its codec. Whole-table /
single-row shortcuts: `all` / `findBy`.

### JOINs and aggregates → `Store.selectRaw`

A single-table Store can't express a JOIN or `GROUP BY` — so `selectRaw` runs
**any** SQL and decodes each row into a typed **projection** record via a codec
(the sqlx split: you own the SQL, the codec owns the mapping — no ORM, no
relations, no N+1):

```elm
type alias Tally = { ideaId : String, votes : Int }

Store.selectRaw conn (Codec.auto { ideaId = "", votes = 0 })
    "SELECT idea_id, COUNT(*) AS votes FROM votes GROUP BY idea_id"
    []                                             -- : Task Error (List Tally)
```

Raw `Std.Db` (`query`/`exec`/`withTransaction`) remains the escape hatch for
anything else. `Store.transaction conn (\tx -> …)` groups Store ops atomically.
`Store.toTable` + `Store.project` build a `db : Store.Project` for
`sky db migrate --gen` (see [Schema migrations](#schema-migrations)). Import
`Std.Db.Store` and `Std.Db` **qualified** — `query`/`migrate` overlap.

**Exact signatures are the source of truth in `sky doc`:**
`sky doc Std.Db.Store` · `sky doc Std.Codec`.

## Typed schema — `Std.Db.Schema` (dialect-safe DDL)

Hand-written `CREATE TABLE` is the one place the "two backends, one API"
promise leaks: `INTEGER` is 8-byte on SQLite but 4-byte on Postgres (a
millisecond timestamp overflows it), `AUTOINCREMENT` is `BIGSERIAL` on
Postgres, and `datetime('now')` is `now()`. Develop on SQLite, deploy on
Postgres, and these bite you in production.

`Std.Db.Schema` closes that: define the table as a **typed value**, and
`Schema.createTable` emits the dialect-correct DDL for whichever backend the
connection uses. The **same definition is right on both** — Ecto/Diesel in
spirit (explicit, composable), no magic ORM.

```elm
import Std.Db.Schema as Schema exposing (text, int, bigInt, bool)

products : Schema.Table
products =
    Schema.table "products"
        [ Schema.id "id"                                  -- TEXT PRIMARY KEY
        , text "slug" |> Schema.notNull |> Schema.unique
        , text "name" |> Schema.notNull
        , int "price_minor" |> Schema.notNull |> Schema.defaultInt 0
        , bool "active" |> Schema.notNull |> Schema.defaultBool True
        , bigInt "created_at" |> Schema.notNull |> Schema.defaultInt 0
        ]
        |> Schema.withIndex "idx_products_slug" [ "slug" ]

setup : Db -> Task Error ()
setup conn =
    Schema.createTable conn products
```

`created_at` above renders as `INTEGER` on SQLite (which is 8-byte, so millis
fit) and `BIGINT` on Postgres — one `bigInt` declaration, correct on both.

**Column types** (`Schema.<type> "name"`): `text`, `int`, `bigInt`, `real`,
`bool`, `timestamp`, `blob`, `json`, plus `id` (TEXT primary key — the common
Sky pattern) and `serial` (auto-increment integer PK → `INTEGER PRIMARY KEY
AUTOINCREMENT` on SQLite, `BIGSERIAL PRIMARY KEY` on Postgres).

**Modifiers** (pipe them on): `primaryKey`, `notNull`, `unique`,
`autoIncrement`, `defaultInt n`, `defaultText s`, `defaultBool b`, `defaultNow`
(`datetime('now')` / `now()`), `references "table" "col"` (foreign key).

**Type mapping** — each backend's natural column type, with dev==prod
consistency at the *decoded-value* level (via `Std.Db.Decode`): `bool` →
`BOOLEAN` on Postgres / `INTEGER` 0/1 on SQLite (a `SqlBool` param binds to
both, and `Decode.bool` reads both back to a Sky `Bool`); `bigint`/`timestamp`
→ `BIGINT` on Postgres / `INTEGER` on SQLite (the overflow-safe one — both read
as int64); `real`→`DOUBLE PRECISION`, `blob`→`BYTEA` on Postgres; `json`→`TEXT`
on both. `createTable` is idempotent (`CREATE TABLE / INDEX IF NOT EXISTS`);
`createSchema conn tables` runs a list in order.

`Schema` only builds the tables — you still write `Db.exec` / `Db.query` for
data. It removes the one dialect-specific string from your app; the parameter
layer (below) already handles the rest.

> **Naming tip.** `Schema.text` collides with `Std.Html`/`Std.Ui`'s `text` if
> both are exposed unqualified. In a module that already does
> `import Std.Html exposing (..)` (or `Std.Ui`), import the schema module
> qualified — `import Std.Db.Schema as Schema` and write `Schema.text "col"` —
> rather than `exposing (text)`. `int` / `bigInt` / `bool` don't collide.

`Schema` is declarative table setup (idempotent `CREATE … IF NOT EXISTS`). For
*versioned* schema evolution with checksums and an applied-migrations ledger,
use `Db.migrate` — the two are complementary (see `examples/36-composite-server`
for the migration-tooling shape).

## `Std.Db.Table` — one definition, no decoder, no `SqlValue` lists

`Schema` + a hand-written decoder + hand-written `insertFields` means restating
your columns three times. `Std.Db.Table` collapses that: **one `Table a` value —
carrying a zero-value witness of your record — is the single source of truth**
for the DDL, typed reads, and typed writes. The record type declares the columns
(the runtime reflects the Go struct it lowers to); field ↔ column is
camelCase ↔ snake_case, and type ↔ column is the same dialect-safe mapping
(`Int`→BIGINT, `Bool`→bool, `Maybe a`→nullable, `String`→TEXT, `Float`→REAL).

```elm
import Std.Db.Table as T exposing (Table)

type Category = Stickers | Bookmarks | Prints

type alias Product =
    { id : String, slug : String, priceMinor : Int, active : Bool
    , category : Category, note : Maybe String }

blank : Product
blank = { id = "", slug = "", priceMinor = 0, active = False, category = Stickers, note = Nothing }

products : Table Product
products =
    T.table "products" blank
        |> T.primaryKey "id"
        |> T.unique "slug"
        |> T.enum "category" [ ( Stickers, "stickers" ), ( Bookmarks, "bookmarks" ), ( Prints, "prints" ) ]

-- setup:  T.createTable conn products
-- read:   T.all conn products                                   : Task Error (List Product)
--         T.select conn products "WHERE active = ? ORDER BY slug" [ SqlBool True ]
--         T.findBy conn products "slug" "sticker-pack"          : Task Error (Maybe Product)
-- write:  T.insert conn products p / T.update conn products "id" p.id p / T.delete conn products "id" p.id
```

**The boundary (the sqlx split):** `Table` owns the record↔row *mapping*; **SQL
stays SQL**. `select` takes a raw `WHERE`/`ORDER BY`/`JOIN`/`LIMIT` tail, and a
join decodes into *any* record whose fields match the projection — define an
`OrderSummary = { id, reference, itemCount }` and
`T.select conn orderSummary "SELECT o.id, o.reference, COUNT(i.id) AS item_count FROM orders o JOIN order_items i … GROUP BY o.id" []`.
No relations / eager-loading magic — associations are an explicit second query.

**Enums & custom types.** Nullary enums lower to a runtime int (no name), so map
them with explicit `(value, name)` pairs via `T.enum` (stable across reordering,
stored as readable TEXT). For any other type — data-carrying ADTs, JSON blobs,
nested records — use `T.codec "col" encode decode` with your own `encode : v ->
String` / `decode : String -> v` (the runtime calls them).

**When to drop down.** `Schema` / `Decode` / `SqlValue` remain the escape hatch
for the cases `Table` doesn't cover (partial indexes, bespoke projections,
performance-critical hot paths). `Table` is the default for single-table CRUD.

## What's in the surface

Every operation that touches the disk returns `Task Error a` (per the [Task-everywhere doctrine](../../CLAUDE.md#effect-boundary-task-everywhere-v0100)). Parameter-supplied helpers (`Db.getString`, `Db.getInt`) return bare values because the default plugs the failure case at the call site.

### Connect / open / close

| Function | Type | Notes |
|---|---|---|
| `Db.connect` | `() -> Task Error Db` | Reads driver + dsn from `sky.toml` `[database]` (or `SKY_DB_*` / `DATABASE_URL`). Preferred shape. |
| `Db.open` | `String -> String -> Task Error Db` | Explicit driver + dsn. `Db.open "sqlite" "./app.db"` / `Db.open "postgres" "postgres://..."`. |
| `Db.close` | `Db -> Task Error ()` | Releases the connection pool |

### Statements

| Function | Type | Notes |
|---|---|---|
| `Db.exec` | `Db -> String -> List a -> Task Error Int` | Parameterised insert / update / delete; returns affected rows. v0.16.26+: passing `List SqlValue` gives per-column type fidelity; v0.16.24+: `Maybe a` binds as SQL NULL / unwrapped value directly. |
| `Db.execRaw` | `Db -> String -> Task Error Int` | DDL or multi-statement script — **no** parameter binding (vulnerable to injection if `sql` is built from user input). Use for `CREATE TABLE`, `CREATE INDEX`. |
| `Db.query` | `Db -> String -> List a -> Task Error (List (Dict String String))` | Returns rows as `Dict String String` (every column stringified at the boundary). Same param semantics as `Db.exec`. |
| `Db.queryDecode` | `Db -> String -> List a -> b -> Task Error (List b)` | Decoder is parametric — typically a `Dict String String -> Result Error a` function; failures abort the whole query |
| `Db.updateFields` | `Db -> String -> List (String, SqlValue) -> List (String, SqlField) -> Task Error Int` | **v0.16.26+** PATCH-style update with dynamic SQL. `SetField v` includes the column with `?` placeholder; `OmitField` drops it from the SET clause entirely (database keeps existing value). Column-name validation prevents SQL injection via identifiers. |
| `Db.insertFields` | `Db -> String -> List (String, SqlField) -> Task Error Int` | **v0.16.29+ (#585)** INSERT counterpart of `updateFields`. `SetField v` includes the column with `?` placeholder; `OmitField` drops it from the column list so the database applies its `DEFAULT`. All columns `OmitField` → `INSERT INTO <table> DEFAULT VALUES`. Same identifier validation + `dbBindArg` normalisation as `updateFields`. |
| `Db.insertFieldsReturning` | `Db -> String -> List (String, SqlField) -> String -> Decoder a -> Task Error (List a)` | **v0.16.30+ (#586)** Decoding counterpart of `insertFields`. Appends `RETURNING <projection>` (caller-controlled — same trust model as `queryDecode`'s SQL), then decodes each returned row through `decoder`. Requires SQLite ≥ 3.35 (Mar 2021) or PostgreSQL. Unblocks emission of `id` / `created_at` autodefaults + sky-sqlgen's `@omit` + RETURNING shapes. |

#### Typed parameter binding via `SqlValue` (v0.16.26+)

Sky's HM keeps `List a` homogeneous, so mixed-type SQL params (e.g. `String + Maybe Int + Bool`) need a tagged variant. The `SqlValue` ADT in `Std.Db` covers SQLite's 5 storage classes plus PostgreSQL's common extensions:

```elm
type SqlValue
    = SqlString String       -- TEXT / VARCHAR / CHAR / UUID-as-text / JSON-as-text
    | SqlInt Int             -- INTEGER / SMALLINT / BIGINT / SERIAL
    | SqlFloat Float         -- REAL / DOUBLE PRECISION
    | SqlBool Bool           -- BOOLEAN
    | SqlBytes String        -- BLOB / BYTEA
    | SqlDecimal Decimal     -- NUMERIC / DECIMAL
    | SqlTime Int            -- TIMESTAMP / DATE / TIMETZ (Unix millis)
    | SqlMoney Money         -- TEXT as "ISO_CODE AMOUNT" (lossless round-trip)
    | SqlNull SqlValue       -- typed NULL via wrapped type-witness
```

Maybe-lifting helpers cover the common nullable-column case: `fromMaybeString` / `fromMaybeInt` / `fromMaybeFloat` / `fromMaybeBool` / `fromMaybeBytes` / `fromMaybeDecimal` / `fromMaybeTime` / `fromMaybeMoney`.

```elm
-- INSERT with mixed types — no stringify, no Ffi.toAny
Db.exec conn
    "INSERT INTO orders (id, customer, total, paid_at) VALUES (?, ?, ?, ?)"
    [ SqlInt orderId
    , SqlString customerUuid
    , SqlMoney total                 -- serialises as "USD 1234.56"
    , fromMaybeTime maybePaidAt      -- nullable column
    ]
```

For partial UPDATEs where you want to skip columns entirely (PATCH semantics — set this, clear that, leave the rest alone), `Db.updateFields` takes a `List (String, SqlField)`:

```elm
type SqlField
    = SetField SqlValue     -- column = ?, bind value (which may be SqlNull)
    | OmitField              -- column not in SET clause; database keeps existing value

Db.updateFields conn "orders"
    [ ("id", SqlInt orderId) ]                                    -- WHERE
    [ ("status",  SetField (SqlString "refunded"))                -- change
    , ("paid_at", SetField (SqlNull (SqlTime 0)))                 -- explicit NULL
    , ("notes",   OmitField)                                      -- leave alone
    ]
-- → UPDATE orders SET status = ?, paid_at = ? WHERE id = ?
```

For INSERTs with DEFAULT-omittable columns (set this, NULL that, let the database fill the rest), `Db.insertFields` is the INSERT counterpart — same `SqlField` three-state model, no WHERE clause:

```elm
Db.insertFields conn "items"
    [ ("name",   SetField (SqlString "Widget"))                   -- value
    , ("status", OmitField)                                       -- → DEFAULT
    , ("note",   SetField (SqlString "first batch"))              -- value
    ]
-- → INSERT INTO items (name, note) VALUES (?, ?)
--   (status omitted; database applies its DEFAULT)
```

All columns `OmitField` → `INSERT INTO <table> DEFAULT VALUES` (one all-defaults row).  Returns the affected-row count.

When you need the values the database picked — autoincrement `id`, `DEFAULT created_at`, a generated column — pair with `Db.insertFieldsReturning` instead:

```elm
Db.insertFieldsReturning conn "items"
    [ ("name",   SetField (SqlString "Widget"))
    , ("status", OmitField)                    -- → DEFAULT 'pending'
    , ("note",   SetField (SqlString "first batch"))
    ]
    "id, status"                               -- RETURNING clause
    rowDecoder
-- → INSERT INTO items (name, note) VALUES (?, ?)
--      RETURNING id, status
-- decoded as List Row (typically one row).
```

The projection string is a caller-controlled SQL fragment — the same trust model as `queryDecode`'s SQL.  Schema-derived literals (sky-sqlgen) are safe; user input is not.  Requires SQLite ≥ 3.35 (Mar 2021) or PostgreSQL — same as every other `RETURNING` use already in `Std.Db`.

Money round-trips via `Std.Db.Decode.money` on the read side — paired with `SqlMoney` on the write side for lossless single-TEXT-column storage that survives PostgreSQL `NUMERIC + CHAR(3)` if you decompose at the call site instead.

### Conventional CRUD (auto-generated SQL)

For any table with an `id` column, these save you from hand-writing SELECT/UPDATE/DELETE:

| Function | Type | Notes |
|---|---|---|
| `Db.insertRow` | `Db -> String -> Dict String String -> Task Error Int` | Returns new row id |
| `Db.getById` | `Db -> String -> String -> Task Error (Maybe (Dict String String))` | Single row by primary key (id is a string at the wire boundary). `Nothing` when missing. |
| `Db.updateById` | `Db -> String -> String -> Dict String String -> Task Error Int` | Returns affected rows |
| `Db.deleteById` | `Db -> String -> String -> Task Error Int` | Returns affected rows |
| `Db.findOneByField` | `Db -> String -> String -> a -> Task Error (Maybe (Dict String String))` | Single-row equality lookup |
| `Db.findManyByField` | `Db -> String -> String -> a -> Task Error (List (Dict String String))` | All matches by equality |
| `Db.findByConditions` | `Db -> String -> Dict String String -> Task Error (List (Dict String String))` | AND-joined equality across every key/value in the conditions dict |
| `Db.unsafeFindWhere` | `Db -> String -> String -> List a -> Task Error (List (Dict String String))` | Raw WHERE + bound params — clause is appended verbatim, **vulnerable to injection** if built from user input |

### Transactions

| Function | Type | Notes |
|---|---|---|
| `Db.withTransaction` | `Db -> (Db -> Task Error a) -> Task Error a` | Commits on `Ok`, rolls back on `Err` automatically |

### Row accessors (default-supplied → bare return)

| Function | Type | Notes |
|---|---|---|
| `Db.getField` | `String -> row -> String` | Reads a field as a String (the canonical row-element shape) |
| `Db.getString` | `String -> row -> String` | Same as `getField` — kept for symmetry with the typed helpers below |
| `Db.getInt` | `String -> row -> Int` | Parses to Int; 0 when missing or unparseable |
| `Db.getBool` | `String -> row -> Bool` | Parses to Bool; False when missing |

These return bare values — see [default-supplied helpers stay bare](../../CLAUDE.md#effect-boundary-task-everywhere-v0100). Reach for a typed decoder via `Db.queryDecode` when "missing" needs to fail loud.

## Walkthrough — CRUD with transactions

A canonical flow: create the table, insert rows in a transaction (atomic), query back, and decode into a typed record.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Sky.Core.Result as Result
import Std.Db as Db
import Std.Log exposing (println)
import Sky.Core.Error as Error exposing (Error)


type alias Todo =
    { id   : Int
    , text : String
    , done : Bool
    }


-- Decode one row into a Todo (or fail loudly).
-- Row shape from the runtime is `Dict String String` — every
-- column lands stringified, and the typed accessors
-- (`Db.getInt` / `Db.getString` / `Db.getBool`) parse on read
-- with a default-supplied fallback.
decodeTodo : Dict String String -> Result Error Todo
decodeTodo row =
    Ok
        (Todo
            (Db.getInt "id" row)
            (Db.getString "text" row)
            (Db.getBool "done" row)
        )


main =
    Db.connect ()
        |> Task.andThen
            (\db ->
                Db.execRaw db
                    """CREATE TABLE IF NOT EXISTS todos (
                        id    INTEGER PRIMARY KEY AUTOINCREMENT,
                        text  TEXT    NOT NULL,
                        done  INTEGER NOT NULL DEFAULT 0
                    )"""
                    |> Task.andThen
                        (\_ ->
                            -- All three inserts atomic. If any fails, none commit.
                            Db.withTransaction db
                                (\tx ->
                                    Db.exec tx "INSERT INTO todos (text) VALUES (?)" [ "Write the doc" ]
                                        |> Task.andThen (\_ -> Db.exec tx "INSERT INTO todos (text) VALUES (?)" [ "Ship the release" ])
                                        |> Task.andThen (\_ -> Db.exec tx "INSERT INTO todos (text) VALUES (?)" [ "Take a break" ])
                                )
                        )
                    |> Task.andThen
                        (\_ ->
                            Db.queryDecode db
                                "SELECT id, text, done FROM todos ORDER BY id"
                                []
                                decodeTodo
                        )
                    |> Task.andThen
                        (\todos ->
                            println
                                ("Loaded "
                                    ++ String.fromInt (List.length todos)
                                    ++ " todos"
                                )
                        )
            )
        |> Task.run
```

## Configuration — `[database]` section

`sky.toml`:

```toml
[database]
driver = "sqlite"          # SKY_DB_DRIVER (sqlite | postgres)
path   = "./app.db"        # SKY_DB_PATH (sqlite file)
```

For Postgres, point `path` at a `postgres://...` URL or set `DATABASE_URL` (Postgres-conventional fallback):

```toml
[database]
driver = "postgres"
# Connection string from DATABASE_URL — never commit a real one to sky.toml.
```

`.env`:

```
DATABASE_URL=postgres://user:pass@localhost:5432/myapp
```

Three-layer precedence (highest wins): process env → `.env` file → `sky.toml`. See [environment-variable precedence](../../CLAUDE.md#environment-variable-precedence).

## Patterns

### Always parameterise

`Db.exec` and `Db.query` take a `List any` of bind values. Driver-specific placeholders are inserted automatically (`?` for SQLite, `$1, $2, ...` for Postgres) — your code stays portable.

```elm
-- ✅ Safe
Db.exec db "INSERT INTO users (email) VALUES (?)" [ email ]

-- ❌ SQL injection — never do this
Db.execRaw db ("INSERT INTO users (email) VALUES ('" ++ email ++ "')")
```

### Decode at the boundary

For anything beyond a debug log, decode rows into a typed record at the query site. `Db.queryDecode` short-circuits on the first `Err` from your decoder, so a partial / malformed row aborts the whole load instead of silently producing zero values further down:

```elm
Db.queryDecode db
    "SELECT id, email, role FROM users WHERE active = 1"
    []
    decodeUser  -- Dict String any -> Result Error User
```

### Group with transactions

Anything that mutates two or more rows together belongs inside `Db.withTransaction`:

```elm
Db.withTransaction db
    (\tx ->
        Db.exec tx "UPDATE accounts SET balance = balance - ? WHERE id = ?" [ amount, fromId ]
            |> Task.andThen (\_ -> Db.exec tx "UPDATE accounts SET balance = balance + ? WHERE id = ?" [ amount, toId ])
    )
```

If either UPDATE returns an error (FK violation, deadlock, anything), the runtime calls `ROLLBACK` and surfaces the `Err` to your caller. Both succeed → `COMMIT`.

### Result/Task bridges

Decoders are `Result`-shaped, but DB calls are `Task`. Three helpers compose them without nested `case`:

| Helper | Type | When |
|---|---|---|
| `Task.fromResult` | `Result e a -> Task e a` | Lift a Result into a Task pipeline |
| `Task.andThenResult` | `(a -> Result e b) -> Task e a -> Task e b` | Chain a Result step after a Task |
| `Result.andThenTask` | `(a -> Task e b) -> Result e a -> Task e b` | Chain a Task step after a Result |

See [Result/Task bridges](../../CLAUDE.md#resulttask-bridges) for the full cheatsheet.

## Production checklist

- **Connection pooling is on by default.** `Db.open` returns a `*sql.DB` — Go's `database/sql` manages the pool. No per-request open/close.
- **Set explicit timeouts** for production. The default driver timeouts are generous; tighten via the connection URL (`?statement_timeout=5s` for Postgres).
- **Never embed secrets in `sky.toml`.** Use `DATABASE_URL` from the environment in production; keep `sky.toml` for local-dev defaults only.
- **Index columns you query**. The `findOneByField` / `findManyByField` / `findByConditions` helpers don't add indexes — that's still a deliberate schema decision.
- **Use `Db.migrate` for schema changes**. Versioned, forward-only, checksum-tracked — see the [Schema migrations](#schema-migrations) section below. Wire `sky db status` into CI as a drift gate; run `sky db migrate` ahead of cutover so a bad migration blocks a deploy rather than crash-looping the app.

## Sky.Live integration

Inside a Sky.Live `update`, dispatch DB work via `Cmd.perform`:

```elm
type Msg
    = LoadTodos
    | TodosLoaded (Result Error (List Todo))


update msg model =
    case msg of
        LoadTodos ->
            ( { model | loading = True }
            , Cmd.perform
                (Db.queryDecode model.db "SELECT * FROM todos" [] decodeTodo)
                TodosLoaded
            )

        TodosLoaded (Ok todos) ->
            ( { model | todos = todos, loading = False }, Cmd.none )

        TodosLoaded (Err _) ->
            ( { model | loading = False, error = Just "could not load todos" }
            , Cmd.none
            )
```

The DB call runs in a goroutine; the result comes back as a Msg through the same SSE channel as user events.

## Schema migrations

`Db.migrate` applies versioned, forward-only schema migrations. A
migration is a record — a stable `name` and the `sql` that applies
it:

```elm
import Std.Db as Db
import Std.Db exposing (Migration)

migrations : List Migration
migrations =
    [ { name = "0001_users", sql = """
        CREATE TABLE users (
            id    INTEGER PRIMARY KEY,
            email TEXT NOT NULL UNIQUE
        )
      """ }
    , { name = "0002_posts", sql = """
        CREATE TABLE posts (
            id        INTEGER PRIMARY KEY,
            author_id INTEGER NOT NULL,
            title     TEXT NOT NULL
        )
      """ }
    ]

main =
    Db.connect ()
        |> Task.andThen (\db -> Db.migrate db migrations)
        |> Task.run
```

How it works:

- A `_sky_migrations` table (created automatically) records the
  `name`, a `checksum` of the SQL, and `applied_at` of every
  applied migration.
- On each `migrate` call, migrations whose `name` is not yet
  recorded run **in list order, each in its own transaction**, and
  are recorded. Already-recorded migrations are skipped.
- `migrate` returns the names applied this run — empty when the
  schema was already current, so it is safe to call on every
  start-up.
- **Checksum guard.** If the SQL of an already-applied migration
  changes, `migrate` fails loudly (`checksum mismatch`) rather
  than silently diverging. Treat a shipped migration's `name` and
  `sql` as immutable.
- **Forward-only.** There are no down migrations. To undo a
  change, ship a new compensating migration.

For zero-downtime deploys use the expand/contract pattern — a
migration must be safe under both the old and new code, since they
overlap briefly during a rollout: add a nullable column, deploy
code that writes it, backfill in a later migration, and only drop
the old column once nothing reads it.

### Inspecting & applying from the CLI

The migration list lives in your app (`migrations : List Migration`),
so the `sky` CLI drives it through the built binary:

```bash
sky db status     # report applied / pending / drifted, then exit
sky db migrate    # apply all pending migrations in order, then exit
```

Both build the project, then run it in **DB-ops mode**: the app's
`Db.migrate` call detects the mode, does the work, and exits *before
serving*. Behind the scenes this is the `SKY_DB_OP` environment
variable (`status` / `migrate`), so a deploy pipeline that can't run
the `sky` CLI can use it directly:

```bash
SKY_DB_OP=migrate ./sky-out/app   # apply migrations, exit 0 (1 on failure)
SKY_DB_OP=status  ./sky-out/app   # print the status report, exit 0
```

`sky db status` exits **non-zero when it detects drift** (an applied
migration whose SQL was edited) — wire it into CI as a schema-drift
gate. `sky db migrate` exits non-zero if a migration fails, so a
deploy step running it ahead of cutover blocks a bad rollout instead
of crash-looping the app.

There is no `sky db migrate <file>`: migrations are an ordered,
checksum-tracked set — `migrate` always means "apply every pending
one, in order."

## See also

- [`examples/07-todo-cli`](../../examples/07-todo-cli/) — SQLite CLI todo app, showcases `withTransaction` and `queryDecode`
- [`examples/08-notes-app`](../../examples/08-notes-app/) — Full CRUD web app on SQLite, with auth
- [`examples/16-skychess`](../../examples/16-skychess/) — Sky.Live game with persistent move history
- [Sky.Auth overview](../skyauth/overview.md) — uses `Db` for `register` / `login` / `setRole`
- [Standard library reference](../stdlib.md) — full kernel surface
