# 55 — Persist query (one query, both backends)

Demonstrates the **portable query builder** in `Std.Persist`: the SAME
`Persist.query |> where_ |> orderDesc` runs unchanged on the embedded KV backend
(BlueDB) and on SQL — the graduation story, now with filtering.

```elm
demoQuery =
    Persist.query users
        |> Persist.where_ (Persist.eq "status" (Persist.string "active"))
        |> Persist.where_ (Persist.gte "age" (Persist.int 18))
        |> Persist.orderDesc "age"

-- run on either connection:
Persist.toList conn demoQuery      -- List User
Persist.toCount conn demoQuery     -- Int
```

- **SQL** renders a `WHERE` clause via `Std.Db.Store`'s `Cond`.
- **KV** serializes the SAME resolved `Cond` to a plan JSON (`Store.planJson`)
  and evaluates it in Go over each decoded record (`BlueDB.collQuery`) — a
  full-scan predicate: an analytics/cold-path op, never the reactive hot path.
  For point lookups declare an `index` and use `findAllByIndex`.

The builder covers `eq/neq/gt/gte/lt/lte/like/isNull/notNull/inList` +
`and_/or_/not_`, `orderAsc/orderDesc`, `limit/offset`, and the
`toList/toMaybe/toCount` terminals — all re-exported from `Std.Persist`, so a KV
app queries from one import.

## Run

```bash
# KV runs embedded; the SQL leg needs a sqlite DB URL
DATABASE_URL="sqlite:data/q.db" sky run src/Main.sky
```

Both backends print byte-identical results across AND+order-desc, count,
OR-nesting, and LIKE.
