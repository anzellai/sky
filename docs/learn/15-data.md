# Data with Std.Db

Most apps need to store data. In Sky the default is a **codec-driven store**: you
write one codec, and it drives the JSON shape *and* the database — read, write, and
schema — with no drift between them.

## One codec, everything derived

```elm
import Std.Db as Db
import Std.Codec as Codec exposing (Codec)
import Std.Db.Store as Store exposing (Store)


type alias Todo =
    { id : Int
    , title : String
    , done : Int
    }


todos : Store Todo
todos =
    Store.fromCodec "todos" (Codec.auto { id = 0, title = "", done = 0 })
        |> Store.serial "id"
```

`Codec.auto` takes a **zero-value witness** (a blank record) and reflects its
fields into snake_case columns — `title` → `title`, a nested record or list → a
JSON blob column. `Store.serial "id"` marks the integer primary key as
auto-increment, so `insert` lets the database assign it.

That one definition now gives you the table schema, the insert/read mapping, and
`Codec.toJson` / `fromJson` — all consistent, because they come from the same
place.

## Opening, creating, writing, reading

```elm
run : Task Error ()
run =
    Db.connect ()
        |> Task.andThen
            (\conn ->
                Store.create conn todos                       -- ensure the table
                    |> Task.andThen (\_ -> Store.insert conn todos { id = 0, title = "Buy milk", done = 0 })
                    |> Task.andThen (\_ -> Store.query todos |> Store.orderAsc "id" |> Store.toList conn)
                    |> Task.map (\rows -> logCount rows)
            )
```

- `Store.insert conn store record` writes a row.
- `Store.query store |> … |> Store.toList conn` reads. The query builder is
  composable: `Store.where_ (Store.eq "done" (SqlInt 0))`, `Store.orderDesc "id"`,
  `Store.limit 20`, `Store.offset 40`. Values bind as parameters, so it's
  injection-safe.
- `Store.all conn store` reads the whole table; `Store.findBy` fetches one.

## SQLite now, Postgres later — same code

The database is a **tier decision**, not a code decision:

- **Prototype / pet / internal / single machine → SQLite.** It's a single file,
  embeds in your binary's world, zero ops. This is the right default for most of
  what you'll build first.
- **Production / multiple instances → PostgreSQL.**

The app code above doesn't change between them — only the driver (a connection
string / config) differs. The codec emits dialect-correct SQL for whichever
backend you connect to.

## When you outgrow the store

For joins, aggregates, and transactions, drop to raw `Std.Db`
(`query` / `exec` / `withTransaction`) — or use `Store.selectRaw` to run any SQL
and decode each row into a typed projection record via a codec. The store is the
default, not a cage. Schema changes ship as committed migration files:
`sky db migrate --gen` diffs your types and writes one; `sky db migrate` applies
it.

Full surface: the [Std.Db guide](../skydb/overview.md),
[Std.Db.Store](../m/Std.Db.Store.html), and [Std.Codec](../m/Std.Codec.html).

**[Next → Auth](16-auth.md)**
