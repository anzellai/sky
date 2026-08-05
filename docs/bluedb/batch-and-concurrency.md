# BlueDB — batch writes + multi-writer concurrency (Tier 2)

BlueDB's Tier-2 story is **multi-writer ergonomics**. The engine already scales
concurrent in-process writers via group-commit; this page documents the three
surfacing points: the multi-key atomic `batch`, the "open once, share the
handle" idiom, and the relaxed (`Sync=false`) durability tier.

## `BlueDB.batch` — multi-key atomic write, one fsync

`batch` commits several key writes/deletes **atomically** — all-or-nothing —
with a **single fsync**, via the engine's group-commit `WriteBatch`. Either
every op in the list lands or none does; a crash mid-commit recovers to the
pre-batch state (the WAL record is written and fsync'd as one unit). It's the
right primitive for a set of keys that must move together.

```elm
import Std.BlueDB as BlueDB exposing (Put, Del)

-- Move three keys as one durable unit.
BlueDB.batch store
    [ Put "user:1" aliceJson
    , Put "user:2" bobJson
    , Del "user:old"
    ]
```

The op list is a declarative `List BatchOp`:

```elm
type BatchOp
    = Put String String   -- write value under key
    | Del String          -- remove key
```

Ops apply in list order (last-writer-wins for a key repeated in the same batch),
exactly as on crash recovery. An **empty list is a no-op** — nothing is
committed.

### Guarantees

| Property | `batch` | N separate `put`/`delete` calls |
|---|---|---|
| Atomicity | all-or-nothing across every op | each op independent |
| fsync count | **1** for the whole batch | 1 per call (sequential) |
| Crash recovery | pre-batch or post-batch, never a subset | any prefix may be durable |

The one-fsync amortization is the whole point: writing five keys in one `batch`
is one durable commit instead of five, so it's both faster (one fsync) and safe
(the five keys can't be observed half-applied after a crash).

### Raw-layer caveat — no secondary-index maintenance

`batch` is the **raw kv layer**, exactly like `put` / `delete`: it writes keys
and values and does **not** maintain any secondary indexes, unique constraints,
serial sequences, or collection namespacing. If you write records into an
**indexed collection**, use `BlueDB.collPut` (per-collection, schema-enforced,
index-maintaining — itself one atomic `WriteBatch`) or `BlueDB.putIndexed` (raw
record + declared index entries in one atomic batch). Reach for `batch` when you
own the key layout directly and just need several raw keys to move together.

> The declarative `batch : Store -> List BatchOp` list form is the idiomatic
> pure-Sky shape and maps to exactly one kernel call. There is deliberately no
> imperative builder-callback (`withBatch`) — the list is the builder.

## Open once, share the handle

`BlueDB.open` is a **memoised connection**, like a database pool — open it once
and share the returned `Store` across every writer. Opening the same path twice
within a process returns the *same* handle (never a second engine on one WAL,
which would corrupt it); across processes the engine's advisory file lock refuses
the second open. So the idiom is a single top-level binding (a memoised CAF):

```elm
-- ONE handle for the whole program; every writer shares it.
db : BlueDB.Store
db =
    BlueDB.open "data/app.blue"
        |> Task.run
        |> Result.withDefault (-- handle open failure for your app --)
```

Do **not** open per request / per handler. Concurrency scales through the
engine's group-commit committer: many in-process goroutines can `put` / `batch`
concurrently and the committer amortizes their fsyncs into shared group commits
(~51k durable writes/sec at high concurrency on an SSD laptop — see
[capacity.md](capacity.md)). One committer per open file is the correct,
permanent design; "scaling writers" means more concurrency *into* that committer,
which is already done. Multi-*process* write on one file is the irreducible floor
— never build it (open the store from one process and expose it over your app).

## `Sync=false` — relaxed durability tier

The engine supports a relaxed tier (`Options.Sync = false`) that skips the
per-commit fsync: it still survives a **process crash** (the WAL is written), but
**not power loss / OS crash** (the un-fsync'd page can be lost). It trades that
durability for throughput — roughly **~319k writes/sec** vs ~51k for the durable
default (see [capacity.md](capacity.md)).

**Sky-surfaced via `BlueDB.openWith`.** `BlueDB.open` stays the fully-durable
default (`Sync: true` — one fsync per commit / per `batch`). To pick the relaxed
tier from Sky, open with explicit `OpenOptions`:

```elm
import Std.BlueDB as BlueDB

main =
    -- Relaxed durability: skips the per-commit fsync for throughput.
    BlueDB.openWith (BlueDB.withSync False BlueDB.defaultOptions) "data/app.blue"
        |> Task.andThen (\store -> BlueDB.put store "k" "v")
        |> Task.run
```

`OpenOptions` is composed from `BlueDB.defaultOptions` (the exact defaults `open`
uses) via the `with*` builders — `withSync`, `withCheckpointEvery`,
`withMaxValueBytes`, `withMaxKeys` — so future fields never break call sites. The
honest durability caveat still stands: `sync = False` survives a **process crash**
(the WAL is written) but **NOT power loss / OS crash** (an un-fsync'd page can be
lost). Use it only where the data can be regenerated or a lost tail is acceptable.

**Reused handle ignores options.** `openWith` is idempotent per path just like
`open`: a path already open returns the SAME handle and the options passed to the
second `openWith` are IGNORED (the live handle keeps its original options). That
reuse is logged (`bluedb.open.options-ignored`) so a second, differing `openWith`
isn't silently a no-op. Open a store once with the options you want, then share
the handle (the memoised-connection contract).

## See also

- [durability.md](durability.md) — the WAL + memtable substrate and the
  durability tiers.
- [capacity.md](capacity.md) — throughput/latency numbers, the group-commit
  amortization, and RAM/key-count bounds.
- [roadmap.md](roadmap.md) — Tier 2 (multi-writer ergonomics) in context.
