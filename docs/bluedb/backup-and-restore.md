# BlueDB — hot backup + restore (Tier 4)

BlueDB stores a whole database in two local files: `<path>` (the write-ahead
log) and `<path>.snap` (the latest snapshot). You cannot back that up with a
naive `cp` of a *live* store: a checkpoint calls `Truncate(0)` on the WAL right
after installing a fresh snapshot, so a copy taken across that window can grab an
**inconsistent (snapshot, WAL) pair** — a snapshot from one moment and a WAL
from another.

`BlueDB.backup` fixes this. It reuses the checkpoint machinery to snapshot the
memtable to a **separate** destination, in the committer's single-threaded
context, and — unlike a checkpoint — it **never truncates the live WAL**. The
result is a consistent, point-in-time, immediately-openable copy of the store,
taken safely while the app keeps running.

## `BlueDB.backup` — consistent point-in-time hot backup

```elm
import Std.BlueDB as BlueDB

-- Back the live store up to a timestamped path; the app keeps serving.
BlueDB.backup store "/backups/app-2026-08-05.blue"
```

```
backup : Store -> String -> Task Error ()
```

The backup writes **two** files that together are a complete store:

| File | Contents |
|---|---|
| `<dest>.snap` | the memtable snapshot at the backup's committed `seq` |
| `<dest>` | a fresh, empty WAL (version header only) |

Because `Open` loads `<dest>.snap` and replays `<dest>`, `<dest>` +
`<dest>.snap` is a self-contained store you can open, verify, ship, or archive.

`dest` must differ from the live store's own `<path>` / `<path>.snap` (a `dest`
that would clobber the live files is rejected).

### The consistency guarantee

The snapshot is taken **in the committer goroutine**, the single writer. So the
backup is a clean point-in-time at the store's committed `seq`:

* It includes **every write committed before** the `backup` call.
* It includes **none after** it.
* A write racing concurrently is serialized *after* the backup — it lands in the
  live store and is simply not in this backup.

Nothing about the live store changes: the live WAL is neither truncated nor
written by a backup — `backup` is a pure copy **out** to `dest`. The live store
keeps serving reads and writes with no pause beyond the memtable copy.

## Restore

A restore is just opening the backup — there is no separate import step:

```elm
BlueDB.open "/backups/app-2026-08-05.blue"
```

or, offline / from a script, point the app's store path at the backup file.
Check a backup first with the read-only scanner:

```sh
sky bluedb /backups/app-2026-08-05.blue verify
```

`verify` never writes; it reports `OK` when `Open` would succeed. Pair a backup
with a `verify` in CI/cron so a bad copy is caught before you need it.

## Offline / scripted backups — the CLI

For a **stopped** store (or a snapshot of a copied file), the CLI takes a backup
without any app code:

```sh
sky bluedb data/app.blue backup /backups/app.blue
# backed up data/app.blue -> /backups/app.blue
```

The CLI **opens the store itself** (taking the engine's exclusive lock), so it
is for offline / scripted backups. A **live** store (running app) holds that
lock — for a live store, back up **through the app** with `BlueDB.backup` (its
console / an admin action), never with a second writer. The CLI writes only to
`<dest>` + `<dest>.snap`, exactly like the in-process API.

## Scope

This is a **single-node, local-file** backup — one consistent copy of one
store's two files. It pairs with `verify` (integrity) and is the operability
primitive for snapshots, pre-migration safety copies, and cron archival. It is
not (yet) a streaming / continuous-replication story: the Litestream-style
"ship the WAL as a segment stream" work is tracked as the remaining Tier-4
streaming item (see `docs/bluedb/roadmap.md`), because a checkpoint's
`Truncate(0)` destroys the WAL log today.
