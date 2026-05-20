# Std.Db overview

> **v0.13 state**: typed Go output end-to-end. Whole-program Sky DCE
> prunes unused FFI bindings (Stripe-SDK scale: −82 % source). LSP 100 %
> coverage; runtime verification across all 26 examples. See
> [`../compiler/journey.md`](../compiler/journey.md) for the changelog.


**One database API, two backends.** `Std.Db` is a thin, parameter-safe wrapper over `database/sql` that works identically against SQLite and PostgreSQL. Pick the driver in `sky.toml`; never touch it again in your code.

```elm
module Main exposing (main)

import Std.Db as Db
import Sky.Core.Task as Task
import Std.Log exposing (println)


main =
    Db.open "todos.db"
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

## What's in the surface

Every operation that touches the disk returns `Task Error a` (per the [Task-everywhere doctrine](../../CLAUDE.md#effect-boundary-task-everywhere-v0100)). Parameter-supplied helpers (`Db.getString`, `Db.getInt`) return bare values because the default plugs the failure case at the call site.

### Connect / open / close

| Function | Type | Notes |
|---|---|---|
| `Db.open` | `String -> Task Error Db` | Auto-detects driver from `sky.toml` `[database] driver`; path is sqlite file OR Postgres URL |
| `Db.connect` | `String -> Task Error Db` | Explicit alias for `open` (compatibility) |
| `Db.close` | `Db -> Task Error ()` | Releases the connection pool |

### Statements

| Function | Type | Notes |
|---|---|---|
| `Db.exec` | `Db -> String -> List any -> Task Error Int` | Parameterised insert / update / delete; returns affected rows |
| `Db.execRaw` | `Db -> String -> Task Error ()` | DDL only — no parameters. Use for `CREATE TABLE`, `CREATE INDEX`. |
| `Db.query` | `Db -> String -> List any -> Task Error (List (Dict String any))` | Returns rows as `Dict String any` |
| `Db.queryDecode` | `Db -> String -> List any -> (Dict String any -> Result Error a) -> Task Error (List a)` | Decodes each row through your function; failures abort the whole query |

### Conventional CRUD (auto-generated SQL)

For any table with an `id` column, these save you from hand-writing SELECT/UPDATE/DELETE:

| Function | Type | Notes |
|---|---|---|
| `Db.insertRow` | `Db -> String -> Dict String any -> Task Error Int` | Returns new row id |
| `Db.getById` | `Db -> String -> Int -> Task Error (Dict String any)` | Single row by primary key |
| `Db.updateById` | `Db -> String -> Int -> Dict String any -> Task Error Int` | Returns affected rows |
| `Db.deleteById` | `Db -> String -> Int -> Task Error Int` | Returns affected rows |
| `Db.findWhere` | `Db -> String -> String -> List any -> Task Error (List (Dict String any))` | Parameterised WHERE; never string-concatenate user input into the clause |
| `Db.findOneByField` | `Db -> String -> String -> any -> Task Error (Maybe (Dict String any))` | Single-row lookup by indexed column |

### Transactions

| Function | Type | Notes |
|---|---|---|
| `Db.withTransaction` | `Db -> (Db -> Task Error a) -> Task Error a` | Commits on `Ok`, rolls back on `Err` automatically |

### Row accessors (default-supplied → bare return)

| Function | Type | Notes |
|---|---|---|
| `Db.getField` | `String -> Dict String any -> String` | Stringifies any value at the field |
| `Db.getFieldOr` | `any -> Dict String any -> String -> any` | Default value when field missing |
| `Db.getString` | `String -> Dict String any -> String` | Type-aware; empty string when missing |
| `Db.getInt` | `String -> Dict String any -> Int` | Type-aware; 0 when missing |
| `Db.getBool` | `String -> Dict String any -> Bool` | Type-aware; False when missing |

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
-- Row shape from the runtime is `Dict String any` — the typed
-- accessors (`Db.getInt` / `Db.getString` / `Db.getBool`) read
-- through the dict and apply the default-supplied fallback.
decodeTodo : Dict String any -> Result Error Todo
decodeTodo row =
    Ok
        (Todo
            (Db.getInt "id" row)
            (Db.getString "text" row)
            (Db.getBool "done" row)
        )


main =
    Db.open "todos.db"
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
- **Index columns you query**. The `findOneByField` / `findWhere` helpers don't add indexes — that's still a deliberate schema decision.
- **Migrations are your responsibility.** `Db.execRaw` runs DDL but there's no built-in migration runner. Most apps keep a `migrations/*.sql` directory and apply them in order at startup; example projects 07/08/16 show the pattern.

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

## See also

- [`examples/07-todo-cli`](../../examples/07-todo-cli/) — SQLite CLI todo app, showcases `withTransaction` and `queryDecode`
- [`examples/08-notes-app`](../../examples/08-notes-app/) — Full CRUD web app on SQLite, with auth
- [`examples/16-skychess`](../../examples/16-skychess/) — Sky.Live game with persistent move history
- [Sky.Auth overview](../skyauth/overview.md) — uses `Db` for `register` / `login` / `setRole`
- [Standard library reference](../stdlib.md) — full kernel surface
