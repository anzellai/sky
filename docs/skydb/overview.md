# Std.Db overview

> **v0.15 state**: type-directed lowering across callback fields,
> record-field inits, list elements, and call args; Go generics on
> parametric record aliases. Whole-program Sky DCE prunes unused FFI
> bindings. LSP 100 % coverage; runtime verification across all 27
> examples. See [`../compiler/journey.md`](../compiler/journey.md)
> for the changelog.


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
| `Db.exec` | `Db -> String -> List a -> Task Error Int` | Parameterised insert / update / delete; returns affected rows |
| `Db.execRaw` | `Db -> String -> Task Error Int` | DDL or multi-statement script — **no** parameter binding (vulnerable to injection if `sql` is built from user input). Use for `CREATE TABLE`, `CREATE INDEX`. |
| `Db.query` | `Db -> String -> List a -> Task Error (List (Dict String String))` | Returns rows as `Dict String String` (every column stringified at the boundary) |
| `Db.queryDecode` | `Db -> String -> List a -> b -> Task Error (List b)` | Decoder is parametric — typically a `Dict String String -> Result Error a` function; failures abort the whole query |

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
