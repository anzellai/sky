# BlueDB — the reactive data layer for Sky (experimental)

> **Status:** `exp/bluedb`. This is a **design + API spec**, not an engine yet.
> Phase 1 (embedded reactive auto-sync over a fast local store) is the first
> thing to build and is buildable now.

## The one-liner

```elm
main =
    Live.app
        (Live.config { init = init, update = update, view = view
                     , subscriptions = subscriptions, routes = routes
                     , notFound = HomePage }
            |> Live.autoBlueDB)          -- ← that's it.
```

Your **Model is the database.** Persistent, reactive, multiplayer. No queries,
no schema drift, no store wiring, no migrations to hand-write. Every `update`
is durably persisted; every change in a session's *scope* pushes live to every
other session in that scope through the SSE channel Sky.Live already owns.

## North star — fast, frequent, small read + writes

BlueDB is an **OLTP hot-path engine first, everything else second.** The
workload we optimize for is a firehose of *small* point reads and writes at high
frequency and low latency — exactly the shape of a reactive Model that mutates
on every keystroke, click, tick, and multiplayer edit. We do **not** optimize
first for big analytical scans; that's the read/analytics surface (below), and
it must never slow the hot path.

Every design decision is judged against: **does this keep a single point
read/write cheap, low-latency (p99), and horizontally throughput-scalable?**

Concrete consequences (these are load-bearing, not aspirational):

| Pressure | Decision |
|---|---|
| Per-write `fsync` is the latency killer | **Group commit** — amortize one `fsync` across all concurrent writes in a ~1ms window. Durable *and* fast. |
| Network hop dominates a sub-ms op | **Embedded-first** — single-instance apps run BlueDB in-process (like SQLite), zero RPC. Distributed mode is opt-in for scale. |
| Point reads must be RAM-speed | Write-optimized LSM (Pebble) + large block cache + bloom filters; keep the **working set memory-resident**. |
| Cross-shard coordination kills p99 | Keep the hot path **single-key / single-range** — it's served by the leaseholder with no 2PC, no quorum round-trip. Data-locality (`colocate`) so related data shares a range. |
| A single hot key can't be split | **Sharded aggregates** (Firestore-style distributed counters / write-combining) for append/counter hotspots. |
| Frequent writes × reactive fan-out = amplification | **Coalesced change-feed** — per-scope debounce/batch so a hot key doesn't melt its subscribers; ship diffs, not snapshots. |

## Strategy — why this exact shape

Decided in the design discussion (see `strategy.md` when written):

- **Magic-first, compat-second, one engine.** The Sky-native reactive auto-sync
  experience is the flagship, the moat, and the reason to choose Sky. It's the
  thing no SQL-first design can copy, because SQL's request/response shape is
  wrong for reactivity.
- **SQL is a bridge, not the driver.** A Postgres-wire **read** surface (later)
  lets BI tools, `psql`, and skeptics connect over the same data — the reach —
  **without** letting SQL's worldview constrain the hot write path.
- **One substrate, two surfaces:** reactive-sync for app state (the 90% case,
  the magic), read/SQL for analytics/interop (the escape hatch). They serve
  different jobs, so they don't fight.

## The API

```elm
-- Zero-config: persist the whole Model + keep the scope live-synced.
-- Reads everything from [bluedb] in sky.toml.
Live.autoBlueDB : AppConfig -> AppConfig

-- Control when you need it (all optional, sensible defaults):
Live.withBlueDB : BlueConfig -> AppConfig -> AppConfig
```

`BlueConfig` (built with `Blue.config |> withX`, per the v0.19 builder style):

| Field | Type | Default | Meaning |
|---|---|---|---|
| `scope` | `Session \| User \| Tenant String \| Global \| Keyed (Model -> String)` | `Session` | Who shares live state. `User` = same person's devices; `Tenant` = a workspace; `Keyed` = a room/doc id. |
| `persist` | `Whole \| Fields (List String) \| Project (Model -> Doc)` | `Whole` | What gets stored. `Whole` = the entire Model; `Fields`/`Project` = a subset (secrets, transient UI state stay out). |
| `sync` | `Reactive \| PersistOnly` | `Reactive` | Push live changes, or just durably persist without fan-out. |
| `consistency` | `Strong \| Snapshot \| BoundedStaleness Int \| Eventual` | `Strong` | Per-app default; can be overridden per read later. |
| `merge` | `Model -> Model -> Model` | last-writer-wins per field | Conflict resolution when two sessions in a scope race. |

## Semantics — what "auto" actually does

1. **Persist.** After every `update`, the persisted projection of the Model is
   written to BlueDB, transactionally, keyed by the scope. Small diffs, group-
   committed — this is the hot write path.
2. **Hydrate.** On session start, the Model is loaded from BlueDB for its scope.
   `init` only fills what isn't already there (it becomes a *default*, not an
   *overwrite*).
3. **React.** BlueDB's change-feed pushes any change within a scope to every
   live session in that scope; the runtime folds it into their Model and
   re-renders over the **existing SSE channel**. (Sky.Live already fans out to a
   session's tabs — `autoBlueDB` widens that to the whole scope.)
4. **Resolve.** Concurrent writers in a scope reconcile via `merge`
   (last-writer-wins per field by default; supply a function for CRDT-ish
   fields like sets/counters).
5. **Isolate.** A session only ever sees its scope's data. `User`/`Tenant`
   scoping is enforced **server-side** (the v0.16.6 multi-tenant SQL-WHERE gate,
   generalized to a scope key) — a client cannot read across scopes.

## sky.toml — one section for everything (the "just put it all in BlueDB")

Today an app juggles `[database]` (app data) + `[live].store` (sessions) +
`[analytics].dbPath`. BlueDB collapses them:

```toml
name  = "myapp"
entry = "src/Main.sky"

[bluedb]
mode        = "auto"        # auto = whole-Model magic; "manual" = explicit Store API
embedded    = true          # in-process, single file — fastest; no network hop
# url       = "BLUEDB_URL"  # set instead of `embedded` to point at a BlueDB cluster
scope       = "user"        # session | user | tenant | global
sync        = "reactive"
consistency = "strong"
path        = "data/app.blue"   # embedded file location

# sessions + app data + analytics ALL live here. One store. Reactive.
```

**Migration from today:** delete `[database]` and `[live].store`/`storePath`;
add `[bluedb]`. Sessions, app data, and analytics unify into one reactive store.
`embedded = true` is the single-instance default (fast, zero-ops, like SQLite);
set `url` to move to a BlueDB cluster when you need horizontal scale — **the app
code doesn't change**, only sky.toml (same progression as
sqlite→postgres today, but you keep the magic).

## Target app the developer writes (aspirational — phase 1 goal)

A live multiplayer counter, complete, no DB code:

```elm
module Main exposing (main)

import Std.Live as Live exposing (app, config, route)

type alias Model = { count : Int }
type Msg = Inc | Dec

init _   = ( { count = 0 }, Cmd.none )
update m model = case m of
    Inc -> ( { model | count = model.count + 1 }, Cmd.none )
    Dec -> ( { model | count = model.count - 1 }, Cmd.none )

main =
    app (config { init = init, update = update, view = view
                , subscriptions = \_ -> Sub.none
                , routes = [ route "/" Home ], notFound = Home }
            |> Live.autoBlueDB)     -- count is now shared + live across everyone in scope
```

Two browsers, `scope = global`: click `+` in one, the other's number moves.
No query, no schema, no socket code. That's the phase-1 demo.

## Architecture (how it's built)

```
┌ Sky app — autoBlueDB, or the typed Store API for manual control ─────┐
│ Reactive layer:  change-feed → query-subscription → scoped-sync      │
│                  → optimistic rebase                                 │
│ Transaction:     MVCC + HLC; single-key fast path (no 2PC);          │
│                  deterministic Sky txns for multi-key                │
│ Durability:      group-commit WAL + memtable (hot set in RAM)        │
│ Storage:         Pebble (Go LSM) — embedded, or per-shard in cluster │
│ Scale-out (opt): range shards + multi-Raft + leaseholders            │
└──────────────────────────────────────────────────────────────────────┘
```

The reactive layer is the same whether the substrate is embedded (phase 1) or
the distributed engine (phase 3). **Build the DX over a fast local store first;
swap the substrate underneath later without touching app code.**

## Roadmap

- **Phase 0 — this branch.** Design + API/config spec. ← *you are here*
- **Phase 1 — embedded reactive magic (in progress).** `autoBlueDB` working over
  an embedded fast store: whole-Model persist (group-commit) + scope-keyed
  reactive fan-out via the existing SSE + pub/sub. Single node, zero-ops, sub-ms
  hot path. Delivers the demo and de-risks the whole bet.
  - **[landed]** Engine core — `runtime-go/bluedb/`: group-commit WAL +
    in-memory keyspace + crash/torn-tail recovery. Group commit proven (writes
    amortized across fsyncs). The durability substrate from `durability.md`.
  - **[landed]** Snapshot + WAL truncation — checkpoints (manual + auto-every-N)
    bound recovery time and disk; recovery loads snapshot + replays the WAL tail,
    skipping records ≤ coveredSeq (crash-window guard). **Adversarially grilled**
    (fresh-context review): fixed a CRITICAL bug — a mid-batch write error
    (ENOSPC) could leave a torn record behind good ones and silently drop acked
    writes on recovery; now the whole batch rolls back to a clean boundary (or the
    engine seals if it can't). Fault-injection tests prove no acked write is ever
    lost. 18 tests green incl. `-race`.
  - **[landed]** Sky.Live session-store driver — `SKY_LIVE_STORE=bluedb`
    (`rt/live_store_bluedb.go`): the embedded, zero-ops durable session store.
  - **[next]** The `Std.Live.autoBlueDB` stdlib surface + `[bluedb]` sky.toml
    parsing → the reactive fan-out layer (compiler-kernel work, deferred).
- **Phase 2 — partial sync.** Change-feed + query-subscriptions so big Models
  sync diffs, not snapshots; hot-key sharded aggregates.
- **Phase 3 — distributed substrate.** Native transactional ordered-KV (Pebble +
  multi-Raft + HLC) + deterministic Sky transactions. Horizontal write scale.
- **Phase 4 — read/compat surface.** Postgres-wire **read** endpoint for
  analytics/BI/interop.
- **Phase 5 — elasticity.** Load-based auto-split, follower reads, disaggregated
  storage (log-is-the-database; branching/PITR).

## Honesty

This branch is design + API. **Phase 1 is real work but buildable now** over an
embedded store, and is the next step — it delivers the reactive magic and the
fast hot path on a single node. The distributed engine (phase 3+) is a
multi-quarter effort; leading with phase 1 proves the DX and the workload fit
before we spend a line on Raft.
