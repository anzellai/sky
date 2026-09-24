# Durable execution — `Std.Durable`

> Status: design for v1. `Std.Durable` is a general server primitive (payment and
> checkout sagas, order fulfilment, onboarding and approval flows, any multi-step
> process that must survive a restart). It is NOT AI-specific; an agent run is
> just one kind of durable workflow, and `Std.Ai.Agent` is built on top of it.

> **Two levels of durability.** `Std.Durable` (this doc) makes a multi-step EFFECT
> workflow exactly-once and resumable — the right tool when a step must not re-run
> (a charge, an outbound message). For the simpler property "an app's MODEL should
> survive a restart", `Std.App` offers a zero-annotation layer:
> `App.withDurable db modelCodec` snapshots the TEA Model after each update and
> restores it on start, with no change to `model` / `msg` / `update`. It is built on
> the same `Durable.saveSnapshot` / `loadSnapshot` substrate. See
> `docs/skyapp/overview.md`. The Model snapshot is at-most-once for an in-flight
> effect; reach for `Durable.step` when an effect needs the exactly-once guarantee.

## The problem

A workflow runs across many steps and, sometimes, hours or days — a checkout that
waits on a payment webhook, an approval that waits on a human, an onboarding that
sends a mail then waits a day then sends another. It must:

- survive a process restart, a crash, or a deploy, and resume where it left off;
- never re-run a side effect it already ran (exactly-once steps);
- wait passively — a run blocked on an approval for three days must not hold a
  process or a thread;
- scale horizontally — any worker instance can advance any run;
- record what it did, for audit and for later replay.

## The v1 model: step journalling

A workflow is an ordinary `Task` that marks its side-effecting boundaries with
`step`. Each step's result is journalled in Postgres. On resume the workflow body
re-runs from the top, but a `step` whose result is already journalled returns the
recorded value instead of re-executing. This is the DBOS / Restate model, chosen
for v1 because it is achievable in pure Sky over `Std.Db` and it is clear: the
durable boundaries are visible in the code.

```elm
checkout : Durable.Ctx -> CheckoutInput -> Task Error OrderId
checkout ctx input =
    Durable.step ctx orderIdCodec "reserve"
        (Inventory.reserve input.items)
        |> Task.andThen (\reservation ->
            Durable.step ctx chargeCodec "charge"
                (Payments.charge input.card input.amount)
            |> Task.andThen (\charge ->
                Durable.awaitSignal ctx webhookCodec "payment.settled"
                |> Task.andThen (\settled ->
                    Durable.step ctx orderCodec "finalise"
                        (Orders.finalise reservation charge settled))))
```

If the process dies after `charge`, the resumed run replays `reserve` and
`charge` from the journal (no double charge) and blocks again at
`awaitSignal` until the webhook delivers `payment.settled`.

### The determinism contract

The body re-runs from the top on every resume, so the code **between** steps must
be deterministic: no `Time.now`, `Uuid.v4`, `Random`, or direct I/O outside a
`step`. Every effect and every nondeterministic value goes inside a `step` (or
`stepValue` for a pure-but-nondeterministic value like a fresh id), whose result
is journalled and replayed. This is the same contract every durable engine
imposes; Sky can partly enforce it later (the effect boundary already separates
pure code from `Task`, and the `sky fuzz` / mock seam can flag a between-step
effect), but v1 states it as a documented rule.

## The suspend / resume mechanism

`sleep` and `awaitSignal` suspend the run when their condition is not yet met.
In a pure `Task` chain the only way to stop the chain is to fail it, so:

1. `sleep ctx ms` / `awaitSignal ctx name`: if the wake condition is already
   satisfied (a journalled "timer fired" row, or a delivered signal), it returns
   and the body continues. Otherwise it persists the wake condition to the run
   row (`wake_at`, or `waiting_signal`) and fails with a recognisable **suspend
   sentinel** `Error`.
2. The suspend sentinel short-circuits the rest of the body via `Task.andThen`'s
   error propagation.
3. The runner runs the body and inspects the result:
   - `Ok output` → the run is complete; record the output, mark `done`.
   - `Err e` where `Durable.isSuspend e` → the run suspended; the wake condition
     is already persisted; release the lease, leave the run `waiting`.
   - `Err e` otherwise → a real failure; retry with backoff, then dead-letter.

A run that is `waiting` holds no process. A scheduler tick (or a delivered
signal) makes it claimable again when `wake_at <= now` or the signal arrives.

## Storage (Postgres)

Three tables, created by `Durable.setup db` (idempotent), mirroring the
`Std.Jobs` Postgres store's claim pattern.

```
_sky_durable_runs
  id            TEXT PRIMARY KEY        -- the caller's workflow id (dedup key)
  workflow      TEXT NOT NULL           -- registered name
  status        TEXT NOT NULL           -- running | waiting | done | failed | dead
  input         TEXT NOT NULL           -- JSON, the start input
  output        TEXT                    -- JSON, set on done
  attempts      INT  NOT NULL DEFAULT 0
  wake_at       TIMESTAMPTZ NOT NULL    -- when the run is next due (now for runnable)
  waiting_signal TEXT                   -- the signal name it is blocked on, or NULL
  last_error    TEXT NOT NULL DEFAULT ''
  claimed_at    TIMESTAMPTZ             -- lease; NULL = free
  created_at    TIMESTAMPTZ NOT NULL
  updated_at    TIMESTAMPTZ NOT NULL

_sky_durable_journal
  run_id        TEXT NOT NULL
  step_id       TEXT NOT NULL
  result        TEXT NOT NULL           -- JSON, the recorded step result
  created_at    TIMESTAMPTZ NOT NULL
  PRIMARY KEY (run_id, step_id)

_sky_durable_signals
  run_id        TEXT NOT NULL
  name          TEXT NOT NULL
  payload       TEXT NOT NULL           -- JSON
  delivered     BOOLEAN NOT NULL DEFAULT FALSE
  created_at    TIMESTAMPTZ NOT NULL
  PRIMARY KEY (run_id, name)
```

### The claim (lease / SKIP LOCKED)

The runner claims one due run atomically, mirroring `_sky_jobs`:

```sql
WITH picked AS (
  SELECT id FROM _sky_durable_runs
  WHERE status IN ('running','waiting')
    AND wake_at <= $now
    AND (claimed_at IS NULL OR claimed_at < $leaseExpiry)
  ORDER BY wake_at ASC
  FOR UPDATE SKIP LOCKED
  LIMIT 1)
UPDATE _sky_durable_runs SET claimed_at = $now, status='running' FROM picked
WHERE _sky_durable_runs.id = picked.id
RETURNING _sky_durable_runs.id, workflow, input;
```

Lease is a fixed visibility timeout (30 min, as Jobs): a crashed worker's run is
reclaimable by a peer after the lease expires. `step` writes the journal row and
its effect result in one `withTransaction`, so a crash between the effect and the
journal write leaves the effect un-committed to the journal and it re-runs — the
contract is at-least-once execution + idempotent apply = effectively-once, and for
a non-idempotent external effect the step passes a deterministic idempotency key
(`run_id:step_id`) the external service dedupes on.

## The runner

`Durable.poll db defs` claims one due run and advances it until it suspends or
completes, returning whether it did work. It is Sky-driven: the app mounts it on a
tick (a `Sub.every` in a Sky.Live/`Std.App` app, or a loop in a `Sky.Cli` daemon),
so v1 needs no Go goroutine and stays pure Sky. A `poll` that returns `True` is
re-fired immediately (drain), a `False` waits for the next tick. Horizontal scale
is free: run more app instances; each claims independently via SKIP LOCKED.

Because each registered workflow has its own input/output types, the runner holds
them type-erased: `register` closes over the codecs and exposes a
`Ctx -> String -> Task Error String` body; `Durable.erase` yields the homogeneous
`RegisteredWorkflow` the runner dispatches by name. `start` keeps the typed handle
so it can encode the input.

## Public API (v1)

```elm
type Ctx                       -- threaded through a workflow body (db + run id + cursor)
type WorkflowDef i o           -- a typed, registered workflow
type RegisteredWorkflow        -- its type-erased form, for the runner

register  : String -> Codec i -> Codec o -> (Ctx -> i -> Task Error o) -> WorkflowDef i o
erase     : WorkflowDef i o -> RegisteredWorkflow

setup     : Db -> Task Error ()                                   -- create the tables (idempotent)
start     : Db -> WorkflowDef i o -> String -> i -> Task Error () -- idempotent on the id
signal    : Db -> String -> String -> Codec a -> a -> Task Error ()  -- deliver a signal (runId, name, payload)
poll      : Db -> List RegisteredWorkflow -> Task Error Bool      -- claim + advance one due run

-- inside a workflow body (all take Ctx):
step        : Ctx -> Codec a -> String -> Task Error a -> Task Error a
stepValue   : Ctx -> Codec a -> String -> (() -> a) -> Task Error a   -- journal a fresh value (uuid/time)
sleep       : Ctx -> Int -> Task Error ()                             -- passive, ms from now
awaitSignal : Ctx -> Codec a -> String -> Task Error a               -- passive, until delivered
```

## Reliability + scale summary

- **Exactly-once effects** — journal + `withTransaction` + a deterministic
  idempotency key for non-idempotent externals.
- **Crash recovery** — the lease expires and a peer resumes from the journal.
- **Passive waits** — a waiting run is rows, not a process; woken by `wake_at` or
  a delivered signal, so hours-and-days runs are cheap.
- **Horizontal** — stateless workers claim via `FOR UPDATE SKIP LOCKED`.
- **Poison runs** — after N attempts a run dead-letters and stops looping.

## The v1 / v2 boundary

- **v1 (this doc):** explicit `step` boundaries, single worker version, Postgres.
- **v2 — shipped:**
  - **worker versioning + migration** — a run is stamped with its workflow version
    at `start`; `registerVersioned` / `pollWith` reconcile a resumed run's version
    against the registered defs (`FailSafe` fails a mismatch, `PinToStart` parks it
    for a worker on the old version — the rolling-deploy case). A `Migrate` policy
    (rewrite a run's version/journal in place) is the remaining follow-up.
  - **history compaction** — `Durable.compact db retentionMs` collapses the journal
    of terminal runs while keeping each run's summary (output + status). The
    continue-as-new form (deleting the run rows themselves) builds on it.
- **v2 — durable TEA replay — shipped (restore-on-first-tick), pure Sky:**
  - **model snapshot** — `Durable.saveSnapshot` / `saveSnapshotAuto` / `loadSnapshot`
    serialise a Model with `Std.Codec` to `_sky_durable_snapshot` and restore it as a
    restart would; write-if-newer by `seq`. No kernel (the earlier "snapshot kernel"
    premise was refuted — see [`durable-transparent-replay.md`](durable-transparent-replay.md)).
  - **TEA wiring** — a durable `Std.App` app adds one `Msg` variant and wires
    `Durable.restoreCmd` (init loads the snapshot, model swaps in on tick one),
    `Durable.snapshotCmd` (update persists the new model), `Durable.applyRestore`.
    `SnapshotEvent` carries `RestoreFailed` / `PersistFailed` too (v0.25.17): a
    snapshot that no longer decodes, or a write that failed, is never silent.
    `Std.App`'s `init` stays synchronous; restore is a first tick, not a framework
    change.
  - The model must be a plain data value with a `Codec` (no function fields) — the
    same rule Elm's ports impose.
- **v2 — the one optional kernel:** FULLY annotation-free capture (durability for an
  effect written as a plain `Cmd.perform`) still needs a runtime hook in the `Cmd`
  dispatcher; it is optional, since `Durable.perform`/`snapshotCmd` give the same
  guarantee with one annotation. Scoped in
  [`durable-transparent-replay.md`](durable-transparent-replay.md).
- **v2 — later:** the eval-replay pipeline reusing the same journal.
