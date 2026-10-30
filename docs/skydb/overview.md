# Std.Db overview

> **Status**: the Rust compiler (`rust/`, `cargo build --release -p sky`)
> is the Sky compiler (the earlier compilers were removed in v0.29.0).
> Verified by the example sweep + compiler test
> suite (`cargo test` + xtask gates). See
> [`../history/compiler/journey.md`](../history/compiler/journey.md) for the changelog.


**One database API, two backends.** `Std.Db` works identically against SQLite and PostgreSQL — pick the driver in `sky.toml`, never touch it again in your code. For record-shaped tables the **recommended default is `Std.Db.Store` + `Std.Codec`**: write one codec per type and let the Store drive the schema, the reads, and the writes — no hand-written SQL, no row mappers. A Store query also joins two stores (`join` / `leftJoin`), groups and aggregates (`groupBy` / `aggregate` / `having`), projects (`select`), filters on a subquery (`inQuery` / `exists`), and upserts (`upsert` / `upsertWith`). Drop down to raw `Db.exec` / `Db.query` only for what it cannot express: a join of three or more tables, a window function, a CTE, a regular-expression match, or a trigger (such as a counter column the database maintains on each insert) (the [escape hatch](#dropping-to-raw-sql) shown later).

```elm
module Main exposing (main)

import Std.Codec as Codec
import Std.Db as Db
import Std.Db.Store as Store exposing (Store)
import Sky.Core.Task as Task
import Std.Log exposing (println)


type alias Todo =
    { id : Int, text : String, done : Bool }


todos : Store Todo
todos =
    Store.fromCodec "todos" (Codec.auto { id = 0, text = "", done = False })
        |> Store.serial "id"            -- auto-increment Int PK


main =
    Db.connect ()                       -- reads `[database]` from sky.toml
        |> Task.andThen
            (\conn ->
                Store.create conn todos
                    |> Task.andThen (\_ -> Store.insert conn todos { id = 0, text = "Write the doc", done = False })
                    |> Task.andThen (\_ -> Store.all conn todos)
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
keeps camelCase; **`Codec.autoWith [ ("active", intBool) ] blank`** overrides one
field's codec (a Bool stored 0/1, a custom enum) while auto-deriving the rest; a
fully custom mapping uses `Codec.object`/`Codec.field "col" .field`.

### Schema / DDL builders

Pipe these onto the store — each accepts the record **field** name *or* the snake
column (a typo fails fast with the column list):

| Builder | Effect |
|---|---|
| `Store.primaryKey "id"` | mark the PK (String/UUID key you provide) |
| `Store.serial "id"` | auto-increment PK (`INTEGER … AUTOINCREMENT` / `BIGSERIAL`) |
| `Store.unique "email"` | `UNIQUE` constraint |
| `Store.defaultNow "created_at"` | a text column: `DEFAULT now()`/`datetime('now')`, stamped on insert (on SQLite `insert`/`insertMany`/`upsert` bind the app clock) |
| `Store.touchOnUpdate "updated_at"` | stamped on insert **and auto-bumped to `now()` on every `update`** — no raw SQL |
| `Store.defaultText/defaultInt/defaultBool/defaultFloat "col" v` | literal column `DEFAULT` (`defaultBool` → `TRUE`/`FALSE` on Postgres, `1`/`0` on SQLite; `defaultFloat` refuses NaN and Infinity) |
| `Store.index [ "customer_id", "created_at" ]` | `CREATE INDEX` named `ix_<table>_<h>` |
| `Store.uniqueOn [ "customer_id", "code" ]` | a unique index, `ux_<table>_<h>`; every column must be required (NULLs never conflict) |
| `Store.uniqueWhere [ "slug" ] (Store.neq "slug" (SqlString ""))` | a partial unique index; the condition is written as constants (`eq`/`neq`/`gt`/…, `isNull`/`notNull`, `inList`, `eqCol`…, `and_`/`or_`/`not_`) |
| `Store.defaultWith "id" (\_ -> SqlValue)` | **app-side** computed default at insert (e.g. a UUID PK via the unit-arg `Task.run` idiom) |
| `Store.generated [ "id", "created_at" ]` | columns `insert`/`update` OMIT so the DB fills them |

`<h>` is 8 hex digits of a hash of the table, the columns, the unique flag
and the predicate, so every definition has its own name, and a changed
`uniqueWhere` condition gives a new index. `create` and `migrate` create the
declared indexes.

**`Store.migrate` and `sky db migrate --gen` follow one rule** (user decision
D1): "A `Maybe` field gives a nullable column, any other field gives NOT NULL.
A declared builder default wins (`defaultInt` / `defaultText` / `defaultBool` /
`defaultNow`, plus a new `defaultFloat`), else the zero value of the type.
Existing columns are never altered. No builder makes a required field's column
nullable." Both paths render an added column through one runtime function, so
the DDL is the same per dialect:

| Field and builder | Added column (SQLite / PostgreSQL) |
|---|---|
| `Maybe`, no default | `ADD COLUMN c <type>` (nullable) |
| `Maybe` + a declared default | nullable, `DEFAULT <d>` |
| required + `defaultInt/Text/Bool/Float` | `NOT NULL DEFAULT <d>` |
| required, no builder | `NOT NULL DEFAULT 0` / `''` / false |
| required JSON (blob) field | `NOT NULL DEFAULT '<json>'`, where `<json>` is the field's value in the codec's zero value (`Codec.zeroJson`: the blank record of `Codec.auto`, or `[]` / `""` / `0` / `null` per field of a `Codec.object` codec) |
| required JSON field whose codec has no zero (a `taggedUnion` or `result` field of a `Codec.object` codec, a `Schema.blob` column) | `NOT NULL` with no default: an empty table takes it, a table with 1 or more rows is `Err` (a `sky db migrate` file fails at the same count, with the same advice) |
| `defaultNow` (text) | PostgreSQL `NOT NULL DEFAULT now()`; SQLite `NOT NULL DEFAULT '<time of the ALTER>'` |
| `unique` | added, then a unique index; a required unique column on a table with 2 or more rows is `Err` (the same count as a `sky db migrate` file) |

Notes: `migrate` adds and creates only, it never drops, renames or retypes,
and it never makes an existing nullable column NOT NULL. Run it from one
process: two replicas that migrate at once can race on `ADD COLUMN`. On
PostgreSQL, `CREATE INDEX` locks writes to the table while it runs, and from
PostgreSQL 11 `ADD COLUMN … NOT NULL DEFAULT <constant or now()>` needs no
rewrite (`now()` stamps the rows that exist with the time of the ALTER). A zero
`''` cannot decode for a nullary enum, a `Codec.map` wrapper or a validated
string: declare a default for such a field.

A required `unique` column in a `--gen` migration file is an `addColumn` op
(`NOT NULL DEFAULT <d>`) and then an `addIndex` op. On a table with 2 or more
rows every row takes the same default, so the index cannot be built: `sky db
migrate` rolls the migration back and names the table, the column and the fix.
Give each row its own value in a data step, a `{"kind": "raw", "sql": "UPDATE t
SET c = …"}` op between the two ops, or make the field a `Maybe`.

The `_sky_migrations` ledger checks a `--gen` migration file by a checksum of
its **ops** (a canonical form of the op list, the same on every dialect), not
by the SQL they render to. A later change to how an op renders never makes an
applied file look edited; an edit to the file is still a `checksum mismatch`.
A ledger that v0.28 wrote is upgraded on the first `sky db migrate`, with no
configuration (see `docs/migration/v0.29.md`).

`pushProject` and `dumpSchema` (so `sky db migrate --gen` and `sky db push`)
refuse a store whose builders recorded a problem (for example an unknown column
in `unique`, `index` or `uniqueOn`), naming the column, before any DDL.

### Writes

`insert` · **`insertMany`** (one multi-row INSERT — bulk / time-series) · `update`
(by PK, whole record) · `updateWhere` (by `Cond`, whole record) · **`setFields`**
(PATCH by PK — `SET` only the named columns) · **`updateFields`** (PATCH by
`Cond`) · **`adjust`** (atomic `SET col = col + delta` for counters/stock) ·
**`upsert`** (`INSERT … ON CONFLICT(pk) DO UPDATE` — idempotent config rows;
needs a non-generated PK) · `delete` · `deleteWhere`.

Partial-column PATCH — the way to set one or two columns without rewriting the
whole record (or naming a column that isn't in the codec's read shape):

```elm
-- mark an order shipped, touching only two columns
Store.setFields conn orders (SqlString orderId)
    [ ( "status", SqlString "shipped" ), ( "tracking", SqlString code ) ]

-- atomic stock decrement (value depends on the current value — no read first)
Store.adjust conn products (Store.eq "id" (SqlString pid)) [ ( "stock", -qty ) ]
```

**Conflict rules, expression SET, copy and RETURNING (v0.29.0).** Each renders
ONE statement, checks every column against the codec first, and binds every
value as a parameter. They need SQLite 3.35+ (the bundled engine is 3.46) or
PostgreSQL 9.5+.

| Verb | Statement | Returns |
|---|---|---|
| `insertOrIgnore conn s row` | `INSERT … ON CONFLICT DO NOTHING` (any unique key) | 1 written, 0 already there |
| `insertOrIgnoreOn [ "dedupe_key" ] conn s row` | `… ON CONFLICT (dedupe_key) DO NOTHING` | 1 or 0 |
| `upsertWith conn s row { conflictOn, onConflict }` | `… ON CONFLICT (cols) DO UPDATE SET …` | rows affected |
| `updateSet conn s cond [ setCol / setValue / setWhen ]` | `UPDATE … SET c = other_col, d = CASE WHEN … END` | rows affected |
| `copyInto` / `copyIntoOrIgnore conn target query [ ( col, from / const ) ]` | `INSERT … SELECT …` | rows inserted |
| `insertReturning conn s row` | `INSERT … RETURNING …` | the stored row |
| `updateFieldsReturning conn s cond sets` | `UPDATE … RETURNING …` | the updated rows |
| `deleteWhereReturning conn s cond` | `DELETE … RETURNING …` | the deleted rows |

```elm
-- claim once: of two concurrent calls with one key, exactly one gets 1
Store.insertOrIgnore conn sentEmails { key = "welcome:" ++ userId, sentAt = now }

-- a counter row per day, customer and event, in one atomic statement
Store.upsertWith conn dailyCounts row
    { conflictOn = [ "day", "customer_id", "event" ]
    , onConflict =
        [ Store.addExcluded "count"      -- count = daily_counts.count + excluded.count
        , Store.keepMin "first_at"       -- the smaller; a NULL side gives the other
        , Store.keepMax "last_at"
        , Store.setExcluded "source"     -- source = excluded.source
        ]
    }

-- set columns from expressions, every row from its own values
Store.updateSet conn orderLines (Store.eq "wanted_at" (SqlInt 0))
    [ Store.setCol "wanted_at" "status_at"
    , Store.setWhen "status" (Store.eq "status" (SqlString "ai")) (SqlString "open") Store.keep
    ]

-- a copy or a roll-up from any query (after select / aggregate, a result field)
Store.copyInto conn enquiries
    (Store.query supportRequests |> Store.where_ (Store.gt "created_at" (SqlInt cutoff)))
    [ ( "id", Store.from "id" ), ( "kind", Store.const (SqlString "support") ), ( "customer_id", Store.from "customer_id" ) ]

-- the read-after-write of a claim, in one statement
Store.updateFieldsReturning conn jobs
    (Store.and_ [ Store.eq "id" (SqlString jobId), Store.isNull "owner" ])
    [ ( "owner", SqlString me ) ]     -- [ job ] for the one caller that won, [] for the others
```

The rules, all checked before any statement:

- `conflictOn` (and `insertOrIgnoreOn`) is the primary key, a `unique` column
  or a `uniqueOn` set (not a `uniqueWhere`), of required columns: both engines
  treat NULLs as distinct, so a nullable key never conflicts.
- A conflict rule never names the primary key, a `conflictOn` column, or a
  generated column (`serial`, `defaultNow`, `defaultWith`, `generated`), and a
  column has one rule. `addExcluded` needs a required `Int` / `Float` column.
  `keepMin` / `keepMax` refuse a bool or JSON column. An empty rule list is
  `DO NOTHING`. `touchOnUpdate` columns take the clock on the update.
- `upsertWith` returns rows affected; `upsert` returns the primary key for an
  Int-keyed store (see Writes above).
- `updateSet`: a target is a codec column, not the primary key or a generated
  column, set once. `setCol` copies a column of the same kind (a nullable source
  needs a nullable target). `setWhen target cond value Store.keep` leaves the
  other rows as they are; `Store.otherwise v` sets `v` on them. Each CASE
  parameter is cast to the column type, so it works on PostgreSQL for `Int` and
  `Bool` columns. An empty list runs nothing and returns 0.
- `copyInto`: the source is any query, read as a subquery. `from` names a column
  of its rows (after `select` / `aggregate`, a result field), `const` binds one
  value for every row. Kinds match, a nullable source needs a nullable target,
  every required target column without a default is mapped, and so is every
  `defaultWith` column (the copy does not compute it). A `serial` column is never
  mapped. A join source needs `select` first. `copyIntoOrIgnore` skips a row that
  breaks a unique key.
- RETURNING decodes each row through the codec. `insertReturning` gives the row
  with its `serial` id and `defaultNow` stamp, and fails if no row comes back.
  A write that the database refuses (a UNIQUE violation in `UPDATE … RETURNING`)
  is an `Err`, never an empty list. An empty `updateFieldsReturning` set list is
  an `Err`. Inside a GET server-side render every one of these writes is
  suppressed, as `Db.exec` is.

Counters that the database keeps by itself (an aggregate table kept current by
triggers) stay raw SQL: write the trigger in a migration.

### Reads — query builder

Composable, injection-safe `Cond` values; you never touch a SQL string:

```elm
Store.query users
    |> Store.where_ (Store.eq "verified" (SqlInt 1))
    |> Store.where_ (Store.or_ [ Store.endsWith "email" "@work.io", Store.gt "createdAt" (SqlString cutoff) ])
    |> Store.orderDesc "createdAt"
    |> Store.limit 20
    |> Store.toList conn        -- terminals: toList / toRows / toMaybe / count
```

Leaves (`eq`/`neq`/`gt`/`gte`/`lt`/`lte`/`like`/`isNull`/`notNull`/`inList "col" v`)
combine with `and_`/`or_`/`not_`; multiple `where_` clauses AND together (so OR /
nesting is first-class). `Store.sqlOf codec value` filters by a **typed** value
(enum / `Money` / `Time` / a `Codec.map` wrapper) via its codec. Whole-table /
single-row shortcuts: `all` / `findBy`. `Store.toSql conn q` returns the SQL text
(with `?`) and the parameters a query renders on that connection's dialect, or
the `Err` it would fail with.

**Every name is checked before any statement runs.** A column argument is a
codec column (the record field or its snake name) or a `jsonText` reference. A
table, column, alias or JSON key must match `[A-Za-z_][A-Za-z0-9_]*` and be at
most 63 bytes, the primary key must be a codec column, and a builder that names
an unknown column (`Store.unique "emial"`) makes the first verb fail with that
name. `findBy` and `delete` follow the same rule. A negative `limit` or `offset`
is an `Err`; `offset` without `limit` works on both dialects. There is no
reserved-word list: a column named `order` or `user` stays a driver error on the
dialect that refuses it. **Never build a column argument from user input:** a
`jsonText` reference can read any key of a JSON column.

**Text matching.** `like` uses its pattern as given: `%` and `_` are wildcards,
it does not escape `%`, `_` or `\`, and it is case-sensitive on PostgreSQL but
ignores ASCII case on SQLite. `Store.like "email" ("%" ++ domain)` matches wrong
addresses when `domain` holds `_`. For text a user typed, use the escaped forms, which match the
same way on both dialects:

| Condition | Matches |
|---|---|
| `Store.contains "name" "50%"` | the text contains the needle; `%`, `_`, `!` are literal; case-sensitive |
| `Store.startsWith "name" "Ab"` / `Store.endsWith "email" "@work.io"` | a prefix / suffix, case-sensitive |
| `Store.eqCi "name" "ada"` | equal, ignoring case (`LOWER(col) = LOWER(?)`) |
| `Store.containsCi "email" "WORK"` | contains, ignoring case |

An empty needle matches every non-NULL row. The column must be a text column.
SQLite's `LOWER` folds ASCII letters only; PostgreSQL folds all letters. To
escape a `like` pattern by hand, prefix `!`, `%` and `_` with `!` and use raw
SQL with `ESCAPE '!'`.

**Regular expressions: not offered, on purpose.** SQLite has no built-in
`REGEXP` (a portable form would need a function registered on every
connection), so `Std.Db.Store` has no regex condition. Store a normalised
column (lower case, punctuation removed) when you write the row, and match it
with `contains` / `startsWith` / `endsWith` / `eqCi` / `containsCi`. A regex
that must run in the database stays raw SQL for the one dialect you run.

**Column against column.** `eqCol`, `neqCol`, `gtCol`, `gteCol`, `ltCol` and
`lteCol` compare two columns of the same kind:
`Store.where_ (Store.gtCol "last_staff_at" "customer_read_at")`.

**Inside a JSON column.** `Store.jsonText "props" "source"` names one key of a
JSON text column and works wherever a column name does (a condition,
`groupBy`, `keyAs`, `selectAs`, an order term). It reads the key's text, or
NULL when the key is missing. A JSON `true` reads `"true"` on both dialects. A
number reads as its text, which can differ (`1e2` reads `100.0` on SQLite and
`100` on PostgreSQL), and an object differs in spacing. The column must be a
text or blob column, and a compared value must be `SqlString`. A row with
malformed JSON makes the query `Err`, never a short list.

### Grouped aggregates

`groupBy` + `aggregate` give one row per group, decoded by a result codec.
Each field of the result codec is covered by exactly one `Agg`:

```elm
type alias Tally = { customerId : String, sent : Int, opened : Int, lastSentAt : Int }

Store.query invoices
    |> Store.where_ (Store.gt "sent_at" (SqlInt since))
    |> Store.groupBy [ "customer_id" ]
    |> Store.aggregate (Codec.auto { customerId = "", sent = 0, opened = 0, lastSentAt = 0 })
        [ Store.key "customer_id"
        , Store.countAs "sent"
        , Store.countIfAs "opened" (Store.gt "opened_at" (SqlInt 0))
        , Store.maxAs "last_sent_at" "sent_at"
        ]
    |> Store.having (Store.gte "sent" (SqlInt 2))
    |> Store.orderDesc "sent"
    |> Store.toRows conn                           -- : Task Error (List Tally)
```

| `Agg` | Renders | Result field |
|---|---|---|
| `key "col"` / `keyAs "field" "col"` | the group column (it must be in `groupBy`) | the column's kind; `Maybe` if the column is nullable |
| `countAs "field"` | `COUNT(*)` | `Int` |
| `countIfAs "field" cond` | rows of the group that match `cond` (0 when none) | `Int` |
| `countDistinctAs "field" "col"` | `COUNT(DISTINCT col)` | `Int` |
| `sumAs "field" "col"` | the sum of an `Int` / `Float` column, 0 when empty | the column's kind |
| `minAs` / `maxAs "field" "col"` | the smallest / largest value (not a `Bool` column) | the column's kind; `Maybe` when the column is nullable or there is no `groupBy` |

`having` and the order terms after `aggregate` name result fields. Without
`groupBy` the result is one row over the selection: an empty table gives zero
counts and sums and `Nothing` for `minAs` / `maxAs`. A wrong kind, an unknown
column or field, a `key` that is not grouped, or a field with no `Agg` is an
`Err` before any statement runs. Text compares by the database collation
(SQLite `BINARY`, PostgreSQL the database collation), so `minAs` / `maxAs`, `gt`
and `ORDER BY` on text can differ between the dialects.

### Projection, DISTINCT, newest row per key

```elm
Store.query orders |> Store.select summaryCodec |> Store.toList conn
    -- each field of summaryCodec reads the column of the same name

Store.query orders |> Store.selectColumn "customer_id" Codec.string |> Store.distinct |> Store.toList conn
    -- : Task Error (List String)

Store.query events
    |> Store.selectAs sourcedCodec [ ( "source", Store.jsonText "props" "source" ) ]

Store.query orders
    |> Store.where_ (Store.eq "status" (SqlString "open"))
    |> Store.latestPer [ "customer_id" ] "created_at"   -- the newest open order per customer
    |> Store.toList conn
```

A nullable column needs a `Maybe` field. With `distinct` and a projection,
every order column must be a selected column. `latestPer` never picks a NULL as
the newest, breaks a tie by the larger primary key, and does not combine with
`distinct`, `groupBy` or `aggregate`. `count` counts the rows such a query
returns.

**NULL ordering.** An order term on a nullable column sorts NULLs first
ascending and last descending, on both dialects (SQLite's own default, now
explicit for PostgreSQL). A term on a required column renders as before.

### Joins of two stores

```elm
Store.join invoices customers
        (Store.onAll [ ( "region_id", "region_id" ), ( "customer_id", "id" ) ])
    |> Store.whereLeft (Store.eq "region_id" (SqlString regionId))
    |> Store.whereRight (Store.eq "archived" (SqlBool False))
    |> Store.orderLeftDesc "sent_at"
    |> Store.limit 50
    |> Store.toPairs conn                  -- Task Error (List ( Invoice, Customer ))

Store.leftJoin invoices customers (Store.on "customer_id" "id")
    |> Store.toPairsMaybe conn             -- Task Error (List ( Invoice, Maybe Customer ))
```

One statement. In SQL the left store is `a` and the right store is `b`, and
each result column has a quoted alias (`a.sent_at AS "a__sent_at"`), so two
tables with the same column names never clash and PostgreSQL keeps a camelCase
name. Each side decodes through its own codec.

- **Names.** `whereLeft` / `whereRight` and `orderLeftAsc` / `orderLeftDesc` /
  `orderRightAsc` / `orderRightDesc` read one side. In `where_`, `orderAsc`,
  `select`, `groupBy` and an `Agg`, a bare name reads the store that has it;
  a name both stores have must be written `"a.col"` or `"b.col"`, else the
  query is an `Err`. ON columns must be of one kind. Every name is checked
  against its own codec before the query runs.
- **LEFT join.** `whereRight` goes into the ON clause, so it picks the right
  row and never drops a left row (a `where_` on a right column filters rows as
  usual). A right column can be NULL in any row, so `select` and an `Agg` need
  a `Maybe` field for it.
- **Reshape a join.** `select` / `selectAs`, `groupBy` + `aggregate` and
  `count` take a join: `Store.join bills clients (Store.on "client_id" "id")
  |> Store.groupBy [ "b.name" ] |> Store.aggregate totalCodec [ Store.keyAs
  "name" "b.name", Store.sumAs "total" "a.amount" ]`.
- **Limits.** A join reads two stores: a join of three tables is not
  expressible (use `selectRaw`). `latestPer` does not take a join.
  `whereLeft` / `whereRight` / `orderLeft*` / `orderRight*` on a query that is
  not a join are an `Err` when it runs (the type cannot tell).

### Subquery conditions

```elm
-- Delete the rows of old batches in ONE statement: rows added meanwhile are seen.
Store.deleteWhere conn importRows
    (Store.inQuery "batch_id"
        (Store.query importBatches
            |> Store.where_ (Store.lt "created_at" (SqlInt cutoff))
            |> Store.column "id"))

-- Customers with no invoice sent since a date.
Store.query customers
    |> Store.where_
        (Store.notExists invoices
            (Store.correlate "customer_id" "id")
            (Store.gt "sent_at" (SqlInt since)))
```

`column "id" q` names one column of a query (a `Subquery`), checked against the
inner store's codec. `inQuery` / `notInQuery` compare an outer column with it
(the two of one kind). `exists` / `notExists` take a store, the `correlate inner
outer` pairs that tie it to the outer row (`correlateAll` for several), and a
condition on the inner store (`Store.and_ []` for none). They work in `where_`,
in a join, and in the writes that take a `Cond` (`deleteWhere`, `updateWhere`,
`updateFields`, `adjust`). The inner query is `sq1` (`sq2` inside it), and it
takes `where_`, `orderAsc` / `orderDesc`, `limit` and `offset` only (a `limit`
or `offset` needs an order); a `select`, `aggregate`, `distinct`, `latestPer` or
join there is an `Err`. Subquery conditions do not apply in `having`.

**NULL rules.** The subquery leaves out its NULLs (`… AND sq1.id IS NOT NULL`),
so an outer NULL never matches `inQuery`, and `notInQuery` (or `not_ (inQuery
…)`) keeps an outer value that is not in the set even when the inner column
holds NULL. An outer NULL is left out by `notInQuery` when the inner set has
rows, and kept when it is empty (`NULL NOT IN ()` is true on both engines).

### `Store.selectRaw` — the last escape hatch

`selectRaw` runs **any** SQL and decodes each row into a typed **projection**
record via a codec (the sqlx split: you own the SQL, the codec owns the mapping
— no ORM, no relations, no N+1). Use it for a shape the builders do not model
(a join of three tables, a window function, a CTE):

```elm
type alias Pair = { ideaId : String, author : String }

Store.selectRaw conn (Codec.auto { ideaId = "", author = "" })
    "SELECT i.id AS idea_id, u.name AS author FROM ideas i JOIN users u ON u.id = i.author_id"
    []                                             -- : Task Error (List Pair)
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

**Reaching the migration tooling — `Schema.toProject`.** `sky db push` and
`sky db migrate --gen` read a `db : Store.Project` from your entry module. A
`Schema.Table`-based app bridges into that with one line — no rewrite into
codec stores:

```elm
-- Data.sky
allTables : List Schema.Table
allTables = [ products, users, orders, ... ]

-- Main.sky (or re-export from Data)
db : Store.Project
db = Schema.toProject allTables
```

Table name, primary key, `UNIQUE`, `NOT NULL`, `DEFAULT`, autoincrement and
the `withIndex` / `withUniqueIndex` indexes (with their own names) carry
through, so `sky db push` creates the columns and missing indexes and
`migrate --gen` diffs them (`addIndex` / `dropIndex`). A column added to an
existing table follows the `Store.migrate` rule above. `defaultNow` on a
`timestamp` / `bigInt` / `int` column is epoch milliseconds. Ints widen to
`BIGINT` under the codec pipeline (safe; no millis overflow).

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

Every operation that touches the disk returns `Task Error a` (per the [Task-everywhere doctrine](../../AGENTS.md#language-essentials)). Parameter-supplied helpers (`Db.getString`, `Db.getInt`) return bare values because the default plugs the failure case at the call site.

### Connect / open / close

| Function | Type | Notes |
|---|---|---|
| `Db.connect` | `() -> Task Error Db` | Reads the DSN from `sky.toml` `[database] path`/`url` (or `SKY_DB_PATH` / `DATABASE_URL`); the driver follows from its shape. Preferred shape. |
| `Db.open` | `String -> String -> Task Error Db` | Takes a DSN, with the driver name as documentation — the **shape of the DSN still decides**, so `Db.open "postgres" "./app.db"` opens SQLite. |
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

These return bare values — see [default-supplied helpers stay bare](../../AGENTS.md#language-essentials). Reach for a typed decoder via `Db.queryDecode` when "missing" needs to fail loud.

## Walkthrough — CRUD with transactions

A canonical flow: create the table, insert rows in a transaction (atomic), and query back — with `Std.Db.Store` driving the schema, the writes, and the reads from one codec.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Codec as Codec
import Std.Db as Db
import Std.Db.Store as Store exposing (Store)
import Std.Log exposing (println)


type alias Todo =
    { id   : Int
    , text : String
    , done : Bool
    }


todos : Store Todo
todos =
    Store.fromCodec "todos" (Codec.auto { id = 0, text = "", done = False })
        |> Store.serial "id"


main =
    Db.connect ()
        |> Task.andThen
            (\conn ->
                Store.create conn todos
                    |> Task.andThen
                        (\_ ->
                            -- All three inserts atomic. If any fails, none commit.
                            Store.transaction conn
                                (\tx ->
                                    Store.insert tx todos { id = 0, text = "Write the doc", done = False }
                                        |> Task.andThen (\_ -> Store.insert tx todos { id = 0, text = "Ship the release", done = False })
                                        |> Task.andThen (\_ -> Store.insert tx todos { id = 0, text = "Take a break", done = False })
                                )
                        )
                    |> Task.andThen (\_ -> Store.all conn todos)
                    |> Task.andThen
                        (\todoList ->
                            println
                                ("Loaded "
                                    ++ String.fromInt (List.length todoList)
                                    ++ " todos"
                                )
                        )
            )
        |> Task.run
```

Partial-column updates without rewriting the whole record: `Store.setFields` /
`Store.updateFields` PATCH only the named columns, and `Store.adjust` runs an
atomic `SET col = col + delta` (counters, stock levels) — see
[the Store writes list](#writes) above.

### Dropping to raw SQL

When you need SQL the Store can't express (a join of three or more tables, a
window function, a CTE, a regular-expression match, a trigger such as a counter
column the database maintains, a hand-tuned query), drop to raw `Db.exec` / `Db.execRaw` / `Db.queryDecode` with a
hand-written row decoder. The row shape from the runtime is `Dict String String`
— every column lands stringified, and the typed accessors (`Db.getInt` /
`Db.getString` / `Db.getBool`) parse on read with a default-supplied fallback.

```elm
import Sky.Core.Error as Error exposing (Error)


-- Decode one row into a Todo (or fail loudly).
decodeTodo : Dict String String -> Result Error Todo
decodeTodo row =
    Ok
        (Todo
            (Db.getInt "id" row)
            (Db.getString "text" row)
            (Db.getBool "done" row)
        )


loadTodos : Db -> Task Error (List Todo)
loadTodos db =
    Db.execRaw db
        """CREATE TABLE IF NOT EXISTS todos (
            id    INTEGER PRIMARY KEY AUTOINCREMENT,
            text  TEXT    NOT NULL,
            done  INTEGER NOT NULL DEFAULT 0
        )"""
        |> Task.andThen
            (\_ ->
                Db.withTransaction db
                    (\tx ->
                        Db.exec tx "INSERT INTO todos (text) VALUES (?)" [ "Write the doc" ]
                            |> Task.andThen (\_ -> Db.exec tx "INSERT INTO todos (text) VALUES (?)" [ "Ship the release" ])
                    )
            )
        |> Task.andThen
            (\_ ->
                Db.queryDecode db
                    "SELECT id, text, done FROM todos ORDER BY id"
                    []
                    decodeTodo
            )
```

## Configuration — `[database]` section

`sky.toml`:

```toml
[database]
path = "./app.db"          # SKY_DB_PATH — and it is this that selects the driver
```

**The driver comes from the connection string's shape, not from a config key.** A
`postgres://` / `postgresql://` URL (or a libpq `host=… user=…` DSN) opens
Postgres; anything else is a SQLite file path.

For Postgres, point `path` at a `postgres://...` URL or set `DATABASE_URL`
(Postgres-conventional fallback):

```toml
[database]
# Connection string from DATABASE_URL — never commit a real one to sky.toml.
```

An optional `driver = "sqlite" | "postgres"` may be declared as an **assertion**:
it selects nothing, but the build reports a contradiction between it and
`path`/`url`, so `driver = "postgres"` beside `./app.db` no longer opens SQLite
in silence. (Before v0.19.9 it emitted a `SKY_DB_DRIVER` variable that nothing in
the runtime read.)

`.env`:

```
DATABASE_URL=postgres://user:pass@localhost:5432/myapp
```

Three-layer precedence (highest wins): process env → `.env` file → `sky.toml`. See [environment-variable precedence](../sky-toml.md#precedence).

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

**What a transaction guarantees, precisely.** It is atomic — all of it lands or none of it does. Its *isolation* is the driver's default, which on PostgreSQL is READ COMMITTED and on SQLite is whatever the single pooled connection serialises into. So the balance-transfer above is atomic, but under READ COMMITTED a concurrent transfer touching the same rows can still interleave in ways a `SELECT`-then-`UPDATE` written as two statements will not notice. Where that matters, do the arithmetic in SQL (`balance = balance - ?`, as above) rather than reading a value into Sky and writing it back, or raise the level with `[database] isolation` in [`sky.toml`](../sky-toml.md#transaction-isolation) — and read what that key says about retries before enabling one, because a retried transaction body runs twice.

### Result/Task bridges

Decoders are `Result`-shaped, but DB calls are `Task`. Three helpers compose them without nested `case`:

| Helper | Type | When |
|---|---|---|
| `Task.fromResult` | `Result e a -> Task e a` | Lift a Result into a Task pipeline |
| `Task.andThenResult` | `(a -> Result e b) -> Task e a -> Task e b` | Chain a Result step after a Task |
| `Result.andThenTask` | `(a -> Task e b) -> Result e a -> Task e b` | Chain a Task step after a Result |

See [the effect boundary](../../AGENTS.md#language-essentials) for the full cheatsheet.

## Production checklist

- **Connection pooling is on by default, and sized by Sky rather than inherited.** `Db.open` returns a `*sql.DB`, and `Db.connect` configures it: SQLite is pinned to one connection by its global writer lock, and PostgreSQL gets a bounded pool whose size depends on whether the runtime detects a VM or request-billed serverless — because the number that matters is how many *instances* hold a pool, not how many the app opens. Override per-deployment with `[database] maxOpenConns` / `maxIdleConns` / `connMaxLifetime` / `connMaxIdleTime`; see [`sky.toml`](../sky-toml.md#connection-pool-postgresql). Before v0.20.3 the PostgreSQL pool was unconfigured — `MaxOpenConns = 0`, meaning unlimited backends against a server whose own `max_connections` default is 100.
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
                (Store.all model.db todos)
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
  `name`, a `checksum` of the SQL, `applied_at` and `kind` (`sql` here,
  `ops` for a `sky db migrate` file) of every applied migration.
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
