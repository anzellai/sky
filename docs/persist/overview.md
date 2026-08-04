# Std.Persist — the unified data front door

`Std.Persist` is the **one obvious way** to persist typed records in Sky. It sits
over the two record backends — SQL (`Std.Db.Store` → sqlite/postgres) and the
embedded KV (`Std.BlueDB`) — on the shared schema primitive (`Std.Codec`). You
define a record **once**, connect to a backend, and run the **same universal
verbs**; the compiler refuses any verb the backend can't do — as a **compile
error**, never a runtime failure ("if it compiles, it works").

```elm
import Std.Persist as Persist
import Std.Db.Store as Store
import Std.Codec as Codec
import Sky.Core.Task as Task

type alias User = { id : String, name : String, age : Int }

users : Persist.Collection User
users =
    Persist.collection
        (Store.fromCodec "users" (Codec.auto { id = "", name = "", age = 0 })
            |> Store.primaryKey "id"
        )

-- ONE function, ANY backend — only universal verbs, so the phantom tag is free.
seed : Persist.Conn cap -> Task Error ()
seed conn =
    Persist.create conn users
        |> Task.andThen (\_ -> Persist.put conn users { id = "u1", name = "Ada", age = 30 })
        |> Task.andThen (\_ -> Persist.get conn users "u1")
        |> Task.andThen (\_ -> Task.succeed ())

main =
    Persist.connectKeyValue "data/app.blue"   -- or: Persist.connectRelational ()
        |> Task.andThen seed
        |> Task.run
```

## The mental model

A **`Collection a`** is a named, codec-typed record set — backend-agnostic. It
wraps a full `Store` (so every schema builder — `serial` / `unique` /
`defaultNow` / `touchOnUpdate` — is reachable). A **`Conn cap`** is an open
connection tagged by its backend capability (`Relational` | `KeyValue`). The tag
is a **phantom type**: universal verbs are polymorphic in it (work on any
backend), capability-gated verbs pin it (so misuse is a type error).

## Verbs

| Verb | Backends | Notes |
|---|---|---|
| `create conn coll` | any | SQL: `CREATE TABLE IF NOT EXISTS`; KV: no-op (schemaless) |
| `put conn coll record` | any | Upsert by the record's self-assigned key |
| `get conn coll key` | any | Read by primary key (SQL binds the key **typed** — Int PKs work on Postgres) |
| `delete conn coll key` | any | Delete by primary key |
| `count conn coll` | any | SQL `COUNT(*)`; KV key scan (O(n) — an analytics act) |
| `all conn coll` | any | Every record |
| `scan conn coll prefix limit` | **KV only** | Prefix scan → `(key, record)` pairs |
| `sql conn` | **Relational only** | Escape hatch → raw `Std.Db` (joins, query builder, `selectRaw`, transactions) |
| `kv conn` | **KeyValue only** | Escape hatch → raw `Std.BlueDB.Store` |

Calling a KV-only verb on a relational connection (or vice-versa) is a **compile
error**:

```elm
Persist.scan sqlConn users "u:" 10
-- ✗ TYPE ERROR: `KeyValue` vs `Relational`
```

## Why it exists — the graduation story

The payoff is **start embedded, scale out with almost no rewrite**. Begin on
BlueDB (single-node, fast small writes, zero ops); when you outgrow it, move the
same CRUD to SQL (multi-instance) by changing **only** the `connect` call:

```elm
-- before: Persist.connectKeyValue "data/app.blue"
-- after:  Persist.connectRelational ()      -- driver from sky.toml [database]
```

Every universal-verb site survives the swap. Anything backend-specific (a prefix
`scan`, a raw SQL join via `sql`) **stops compiling** at exactly the call sites
that need attention — the compiler shows you the port, it isn't a runtime
surprise.

## Escape hatches — you never leave the type world

The universal verbs are deliberately the CRUD subset. For backend-specific power,
drop one rung:

```elm
Persist.sql conn                       -- : Task Error Db  → the raw Std.Db handle
    |> Task.andThen (\db -> Store.toList db (Store.query userStore |> Store.where_ (Store.gt "age" (Db.SqlInt 18))))

Persist.kv conn                        -- : Task Error BlueDB.Store
    |> Task.andThen (\store -> BlueDB.scanValues codec store "session:" 100)
```

The escape hatches are `Task`-typed (their impossible-backend arm is a
`Task.fail`, never a panic — so the no-runtime-panic guarantee holds).

## Secondary indexes (KV backend)

On BlueDB, declare an index on a record field to look records up by that field
(not just by primary key). Indexes are **opt-in** (a collection with none is
byte-identical to before) and **maintained atomically** with the record — the
index entry and the record commit together in one write, so they never diverge
across a crash.

```elm
users : Persist.Collection User
users =
    Persist.collection (Store.fromCodec "users" (Codec.auto blank) |> Store.primaryKey "id")
        |> Persist.index "email"          -- declare a secondary index (pure)

-- at startup, backfill existing records (idempotent, O(1) when nothing changed):
Persist.reindex conn users

Persist.findByIndex conn users "email" "ada@x"      -- : Task Error (Maybe User)
Persist.findAllByIndex conn users "status" "active" -- : Task Error (List User) — non-unique
Persist.countByIndex conn users "status" "active"   -- : Task Error Int (no record fetch)
```

### Ordered range

An index over a numeric/date/text field also supports an **ordered range** — the
half-open interval `[lo, hi)`, with either bound `Nothing` for unbounded:

```elm
events : Persist.Collection Event
events =
    Persist.collection (Store.fromCodec "events" (Codec.auto blank) |> Store.primaryKey "id")
        |> Persist.index "ts"             -- an Int field

-- events with 1000 <= ts < 2000:
Persist.findAllByIndexRange conn events "ts" (Just "1000") (Just "2000")  -- : Task Error (List Event)
-- everything from 1700 onward (unbounded upper):
Persist.findAllByIndexRange conn events "ts" (Just "1700") Nothing
-- count only, no record fetch:
Persist.countByIndexRange conn events "ts" (Just "1000") (Just "2000")    -- : Task Error Int
```

The range is **correct for the field's type** — an `Int` field compares
numerically, not lexically (so `5 < 18 < 100`, never the string order
`"100" < "18" < "5"`). The index stores an **order-preserving** encoding derived
from the codec's field type, so this holds for `int`, `bool`, and `text`
(text/date range by byte order = code-point / ISO-8601 order). `real`/`money`/
`float` fields have **no** KV range (they'd need scale/IEEE normalisation) — the
call returns an error; use the SQL backend's typed query builder (`Persist.sql`)
for those. Bounds are the field's key-string form (`"1700"`, `"2026-01-01"`).

> **Format note.** The index encoding is versioned. `Persist.reindex` at startup
> migrates an older index to the current format (a one-time full rebuild) — so
> always call `reindex` once at startup before serving.

- **Non-unique** — several records may share a value; `findByIndex` returns the
  first, `findAllByIndex` returns all. BlueDB does **not enforce** uniqueness (use
  a serial/UUID PK, or the SQL backend, for a real constraint).
- `findByIndex`/`findAllByIndex`/`findAllByIndexRange`/`countByIndex`/
  `countByIndexRange`/`reindex` are **KV-tag verbs** (`Conn KeyValue`) — the
  phantom-tag verb set that the KV backend supports.
- `countByIndex`/`countByIndexRange` are exact but **O(N)-scan analytics ops**
  (cold path), not hot-path point reads.

## How it relates to the other surfaces

| You want… | Reach for |
|---|---|
| Typed records, one obvious API, backend-portable CRUD | **`Std.Persist`** |
| SQL-specialist power (query builder, joins, migrations) | `Std.Db.Store` (via `Persist.sql`, or directly) |
| Embedded KV specifics (prefix scan, raw keys) | `Std.BlueDB` (via `Persist.kv`, or directly) |
| The schema itself (columns + JSON, one definition) | `Std.Codec` (foundational; drives all of the above) |
| Raw SQL / raw KV | `Std.Db` / `Std.BlueDB` |

`Std.Db.Table` is **deprecated** — `Std.Db.Store` (codec-driven) subsumes it;
new code uses `Std.Persist` or `Std.Db.Store`.

Analytics, tracing, and the Sky.Live session store keep their own specialized
modules (they are not record-CRUD); only their backend/driver selection is shared
configuration.
