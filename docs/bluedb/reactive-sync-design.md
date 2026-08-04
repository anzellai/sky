# BlueDB reactive scope-sync — design

> **Status:** design (awaiting decisions A/B/C below), 2026-08-04. The flagship
> "the Model is the DB" feature: a write in one session automatically updates the
> UI of every other session viewing that data — no polling, no manual pub/sub.
>
> Grounded in two reconnaissance passes (BlueDB change-observation + Sky.Live
> fan-out). The headline finding: **this rides existing machinery end-to-end** —
> no parallel system (per the reuse-don't-parallel rule).

## The one-paragraph architecture

A committed BlueDB write flows through a single choke point. We emit a change
event there, decode which `(collection, pk)` it touched, map that to one or more
**scope topics**, and publish to the **existing Sky.Live pub/sub broker**. Any
session subscribed to that scope receives the event through the **already-shipped**
external-update path (`sess.mu → app.dispatch → sseCh → fanOutFrame`), updates its
Model, and the SSE diff paints every one of its tabs. Because Sky.Live is
server-driven (the Model lives on the server; the browser is a thin diff target),
there is **no client-side database and therefore no optimistic-rebase problem** —
a local write is ordinary TEA `update`, and cross-session propagation is the new
part.

## The pipeline (with the reuse seams)

```
Session A update:  Persist.put conn todos t
        │
        ▼
BlueDB commit  ── choke point: bluedb/db.go:417-420 (post-fsync apply loop)
        │        emit (op, key, value, seq) AFTER db.mu.Unlock → async buffered chan
        ▼
Change decoder ── skip non-record keys; record key \x00x\x00d\x00<coll>\x00<pk>
        │        → (coll, pk, op, recordJSON)         [rt/bluedb_*_kernel.go helpers]
        ▼
Scope mapper  ── (coll, pk[, indexed fields]) → scope topic string(s)   [DECISION A]
        │
        ▼
app.Publish(scopeTopic, changePayload)   ── existing broker: live.go:2973
        │        in-process topicRegistry OR redisBroker (cross-replica) — unchanged
        ▼
Session B (subscribed to scopeTopic via Sub.subscribeTopic / Persist.watch*)
        │        runSubscriberLoop → runSubscriberDispatch: live.go:5766   [DECISION B/C]
        │        sess.mu.Lock → app.dispatch(sess, OnChange payload) → sseCh push
        ▼
fanOutFrame → every one of B's tabs repaints (SSE diff)   ── live.go:6688
```

Every box except the two new ones (**change-feed emit** + **scope mapper**) already
exists and ships today.

## New components (small, localized)

1. **Engine change-feed** (`runtime-go/bluedb/`): a `Subscribe(fn func(ChangeEvent))`
   / change channel on `*DB`. Emit site is `db.go:420` — right after
   `db.mu.Unlock()` in `process`, handing `(op, key, value, seq, batchID)` per
   mutation to a **buffered channel drained by a separate goroutine** (never call
   subscriber code on the single committer goroutine or under `db.mu` — a slow
   consumer must never stall commits; reuse the `ForEach`/`reap` snapshot-then-act
   idiom). Bounded buffer with a documented drop/coalesce policy on overflow.

2. **Change decoder + scope mapper** (`runtime-go/rt/bluedb_reactive.go`, new):
   filter to reserved-prefix record keys (discriminator byte `d`), strip
   `\x00x\x00d\x00`, split on first `\x00` → `(coll, pk)`; ignore the sibling
   `i/u/s/m` keys in the same batch (one record change, not five). Map to scope
   topic(s) per DECISION A. Publish via the app broker.

3. **Sky subscription surface** (`Std/Persist.sky` + `Std/Live.sky`): per DECISION B.

## Why there is no optimistic-rebase decision

In client-DB reactive systems (Convex, Zero, Firebase) the client holds a local
replica, applies writes optimistically, and must rebase when the server's
authoritative version arrives. **Sky.Live has no client replica** — the browser
receives DOM diffs, the Model is server-side, and a write already round-trips
through the server `update`. So:

- **Intra-session** freshness is automatic today (write → `update` → diff).
- **Cross-session** is the new capability, and it is a *notify + re-render*, not a
  *merge conflict*. The serializing per-session mutex (`live.go:2146`) already
  gives last-writer-wins ordering within a session; the change-feed's monotonic
  `db.seq` gives a global order across sessions.

This removes the single hardest piece of the classic design. (A future *offline*
or *client-authoritative* mode would reintroduce it — explicitly out of scope.)

---

## Decisions — LOCKED (2026-08-04): A2 + B2 + C1

The user chose the maximum-magic path: **full query-subscriptions (A2)**,
**fully-declarative auto-refresh (B2)**, **change payload + re-query helper (C1)**.
The end state is a reactive query whose result lives in the Model and re-computes
itself whenever a write could have changed it — no subscription code.

### Refined architecture for A2 + B2

**A2 query-overlap engine (the "could this change affect this query?" filter).**
A query subscription registers `(collection, Cond)`. On a committed change to that
collection carrying the new record JSON, we reuse the **P5 KV `Cond` evaluator**
(`bluedbEvalCond`) to test whether the changed record matches the query's `Cond`:

- **insert / update** → the change-feed carries the new record; if it matches the
  `Cond`, the query result may have gained/changed a row → re-run.
- **delete** (and the "row *left* the result set" case on update) → we may not hold
  the *old* record, so matching only the new value is not sufficient. **Correct-by-
  construction rule:** a query re-runs if the changed record matches its `Cond`
  **OR** the change is a delete **OR** the pk is already in the query's last result
  set (the framework caches each subscription's result pks). This never misses a
  transition (in, out, or reorder) and avoids re-running unrelated queries. The
  collection-level fallback (re-run every query on the collection) is always
  available and always correct — the overlap filter is a safe *narrowing*.

**B2 declarative reactive-query binding.** "Zero subscription code" in TEA is
realised as a **declared binding** the framework owns end-to-end — the app declares,
once, `(query-of-model, apply-result-to-model)`; the framework subscribes, re-runs
on a relevant change, and folds the result into the Model, then the normal diff
paints the tabs. Shape (final naming in P-R4):

```elm
-- in the Live config (or a `Persist.reactive` list):
reactiveQueries model =
    [ Persist.live
        (Persist.query todos |> Persist.where_ (Persist.eq "userId" model.me))
        (\rows model2 -> { model2 | todos = rows })   -- fold result into Model
    ]
```

The framework: evaluates each binding's query at mount (fills the Model), registers
`(collection, Cond, resultPks)`, and on every relevant change re-runs the query and
applies the fold — the user writes no `subscriptions` line and no Msg arm. (True
*compiler-introspected* zero-declaration binding — detecting query-backed Model
fields automatically — is a later compiler concern; this declared-binding form is
the maximum magic achievable at the stdlib layer and is what "auto-refresh" means
here.)

**C1 payload.** The change event carries `{ op, collection, pk, record }`; the
overlap engine uses `record` for the `Cond` match, and the re-run produces the fresh
result the binding folds in. A lower-level `Persist.watch*` Sub (the change payload
directly) is also exposed for apps that want to merge manually instead of re-query.

### Revised phasing (builds A2+B2+C1 bottom-up, each phase shippable)

- **P-R1** Engine change-feed — `DB.Subscribe`, buffered async fan-out, overflow
  policy, slow-consumer-never-stalls-commits fault test.
- **P-R2** Decoder + broker publish — `rt/bluedb_reactive.go`: key→(coll,pk,op),
  one-record-not-five batching, publish `{op,coll,pk,record}` to a per-collection
  broker topic (`__bluedb:<coll>`). Collection-scoped invalidation working e2e.
- **P-R3** Query-overlap engine (A2) — subscription registry `(coll, Cond,
  resultPks)`; `bluedbEvalCond`-based narrowing (match ∨ delete ∨ pk∈lastResult);
  differential test vs the always-re-run fallback.
- **P-R4** Declarative reactive bindings (B2) + C1 — `Persist.live` binding surface
  + framework wiring at `setupSubscriptions` + the `store.Set`/`autoBlueDB` seam;
  plus the low-level `Persist.watch*` Sub.
- **P-R5** autoBlueDB integration — the change-feed attaches at the `autoBlueDB`
  seam so a plain Model-persist write is observable with no app code.
- **P-R6** two-session e2e (in-process + Redis broker) — A writes, B's reactive
  list updates with no poll; cross-replica proven.

---

## Build status (2026-08-04)

**Foundation SHIPPED + tested (`exp/bluedb`):**
- **P-R1** engine change-feed — `runtime-go/bluedb/changefeed.go` (`DB.Subscribe`,
  non-blocking, slow-consumer-never-stalls, `-race`).
- **P-R2** record-change decoder + pump — `runtime-go/rt/bluedb_reactive.go`
  (`bluedbDecodeRecordKey`, `bluedbStartReactivePump`, `bluedbCollTopic`; one
  record change per write, siblings filtered).
- **P-R3** query-overlap engine — same file (`bluedbQuerySub`,
  `bluedbChangeAffectsQuery`; reuses P5 `bluedbEvalCond`).

**Remaining: P-R4/5/6 — the Sky.Live runtime integration** (the delicate part —
touches the `live.go` session loop; do this as a focused, fresh effort).

## P-R4 implementation plan (next — precise spec)

Two slices, smaller first:

### P-R4a — watch-Sub level (reuses ALL existing subscription machinery)

The reactive magic at the `Sub` level: a session gets a Msg when a watched
collection changes, then re-queries in its `update`.

1. **Store→broker pump linkage** (the one genuinely new wiring). When a BlueDB
   DATA store is used by a Live app, start `bluedbStartReactivePump` on it,
   publishing each decoded change to the app broker via `liveApp.Publish(
   bluedbCollTopic(coll), payload)` (broker at `live.go:2973`; payload =
   `{op, coll, pk, record}` JSON). Design question to settle: how the app's data
   store (opened by `Persist.connectKeyValue`, often a top-level CAF) reaches the
   running app's `*liveApp`/broker. Candidate: a process-global registry keyed by
   store path that the Live app bridges its broker to at boot (mirrors how
   `bluedbStore` already owns an in-process broker at `live_store_bluedb.go:66`).
   Confirm single-writer/one-pump-per-store.
2. **`Persist.watchCollection coll toMsg : Sub msg`** — thin Sky over
   `Sub.subscribeTopic (bluedbCollTopic coll) toMsg` (NO new kernel — rides
   `setupSubscriptions`/`runSubscriberDispatch` verbatim). `watchKey` variant
   filters to a pk. The payload decodes to a typed change via a codec (C1).
3. e2e: two sessions, A `Persist.put`s, B's `watchCollection` Msg fires → B
   re-queries → B's SSE repaints. Both in-process and (P-R6) Redis brokers.

### P-R4b — declarative auto-refresh (B2) on top of P-R4a

4. **`Persist.live query applyFn`** binding + a config list
   (`reactiveQueries model`). Framework, per session: run each query at mount
   (fill Model via `applyFn`), register a `bluedbQuerySub` (collection + Cond via
   `Store.condPlanJson` + result pks), subscribe to the collection topic, and on a
   change where `bluedbChangeAffectsQuery` is true, re-run + `applyFn` fold + update
   result pks — via the existing `sess.mu → dispatch → sseCh` path
   (`runSubscriberDispatch`, `live.go:5766`). Zero subscription code for the app.
5. **P-R5** attach at the `autoBlueDB` seam (`Std/Live.sky:197`) so a Model-persist
   write is observable with no app code.
6. **P-R6** two-session e2e on in-process + Redis brokers (cross-replica).

Each slice: three-leg verified, committed at the boundary. The `live.go` changes
are best isolated (worktree) given the core-loop surface.

---

## Original decision menu (superseded by the LOCKED section above)

### DECISION A — scope-key granularity (v1)

How a committed change maps to "which sessions care."

| Option | Topic shape | Pro | Con |
|---|---|---|---|
| **A1 collection + record + indexed-field scope (recommended)** | `coll:<name>`, `coll:<name>:pk:<pk>`, `coll:<name>:<field>:<value>` (field must be a declared `index`) | Cheap (string topics, no query engine); covers "watch this list", "watch this row", "watch MY rows" (`todos:userId:<me>`); rides the R1 index metadata | Over-notifies within a scope (a session re-queries/filters); not arbitrary predicates |
| **A2 full query-subscriptions (Convex/Zero-style)** | subscribe a `Persist.Query`; a change notifies iff it *could* change that query's result | Most precise; the true "reactive query" magic | Needs a change→query-overlap engine (does this pk match the query's `Cond`?), query-result caching + diffing — a large build, correctness-heavy |

Recommendation: **A1 now** (ships the magic in days, is the foundation), **A2 as a
later phase built on A1** (a query subscribes to its collection/field scope, then
re-evaluates its `Cond` on the change — we already have the KV `Cond` evaluator
from P5, so A2 is reachable incrementally).

### DECISION B — Sky subscription API shape

| Option | Looks like | Pro | Con |
|---|---|---|---|
| **B1 explicit `Persist.watch*` Subs (recommended)** | `subscriptions model = Persist.watchCollection todos OnTodosChanged` (or `watchKey` / `watchScope`), returns `Sub msg`; user handles the Msg (re-query or merge) | Thin layer over `Sub.subscribeTopic`; composes with existing TEA; ships fast; explicit + debuggable | User writes one `subscriptions` line + one Msg arm |
| **B2 fully-declarative auto-refresh** | a query bound in the Model auto-subscribes and auto-re-runs on any relevant change; zero subscription code | Maximum magic | Needs query-result caching, auto-diff into Model, lifecycle tracking; much larger; couples to A2 |

Recommendation: **B1 now**; B2 later on top of B1 + A2. (B1 *is* the "magic" from
the user's POV — the UI updates itself; they just declare what to watch.)

### DECISION C — delivery payload

| Option | Subscriber receives | Pro | Con |
|---|---|---|---|
| **C1 change payload (recommended)** | `{ op, collection, pk, record }` — the actual changed row | No re-query; subscriber merges directly into Model; the change-feed already carries it | Subscriber does its own scope filtering/merge |
| **C2 invalidation nudge** | "collection X changed" only | Dead simple; always correct via re-query | One extra query per change; coarser |
| **C3 both** | payload + a `re-query` helper | Flexibility | Slightly bigger surface |

Recommendation: **C1 with a re-query helper** — deliver the payload (cheap, already
in hand) and ship a `Persist.watch*` variant that re-queries for the user who
prefers correctness-by-reconstruction.

---

## Phasing (once A/B/C are set)

- **P-R1 Engine change-feed** — `DB.Subscribe` + buffered async fan-out + overflow
  policy; Go `-race` + a fault test (slow consumer never stalls commits).
- **P-R2 Decoder + scope mapper + broker publish** — `rt/bluedb_reactive.go`;
  unit-test key→(coll,pk) + scope-topic derivation + one-record-not-five batching.
- **P-R3 Sky surface** — `Persist.watchCollection/watchKey/watchScope` → `Sub msg`;
  wire through `setupSubscriptions`; the change payload type + codec.
- **P-R4 autoBlueDB integration** — attach the change-feed at the `autoBlueDB`
  seam so a Model-persist write is observable without app code (the "Model is the
  DB" endpoint).
- **P-R5 e2e** — two-session demo (session A writes a todo, session B's list
  updates with no poll), on both in-process and Redis brokers (cross-replica).

Each phase: three-leg verified (Go `-race` + emission/Sky spec + e2e), committed at
the boundary, per the standing methodology.

## Reuse map (nothing here is greenfield except items 1–2)

| Concern | Existing seam |
|---|---|
| Commit choke point | `bluedb/db.go:417-420` (`process`, post-fsync apply) |
| Snapshot-then-act idiom | `db.go` `ForEach`/`Scan`; `live_store_bluedb.go` `reap` |
| Record-key decode | `bluedbReserved`/`bluedbCollRecordPrefix` (`rt/bluedb_*_kernel.go`) |
| Pub/sub broker (in-proc + Redis) | `live_topics.go` `topicRegistry` / `live_redis_broker.go` |
| External-update-into-session | `runSubscriberDispatch` (`live.go:5766`) |
| Per-session serialization | `liveSession.mu` (`live.go:2146`) |
| Fan-out to a session's tabs | `fanOutFrame` (`live.go:6688`) |
| Scope identity | `SessionIdentity` claims (`session_identity.go:76`) |
| Attach seam | `autoBlueDB` (`Std/Live.sky:197`) / `store.Set` (`live.go:4553`) |

## P-R4b GRILL OUTCOME (2026-08-04) — design revised, decision pending

Two adversarial grills (typing/soundness + integration/concurrency) found the
sketched `Persist.live` design UNSOUND as specified. Verdicts split
(don't-build-keep-watch vs build-as-hybrid); substance agreed:

- **BLOCKING — pk erasure:** `Task Error (model -> model)` throws away the result
  pks, so the P-R3 overlap engine runs with an empty result set → a delete of an
  on-screen row never re-runs (permanent stale row). Fix: the RUNTIME runs the
  query and applies a `List a -> model -> model` fold (sees pks → feeds
  `setResultPks`).
- **BLOCKING — SQL silent staleness:** the change-feed is KV-only; `live` on
  `connectRelational` mounts once and never refreshes/errors. Must gate `live`
  to `Conn KeyValue` (compile error) → reactivity does NOT survive SQL graduation
  without a Postgres LISTEN/NOTIFY bridge (separate large piece).
- **BLOCKING — resync hole:** `bluedbPublishChange` drops the overflow/resync
  signal (bluedb_reactive.go:142) → under a write burst reactive views go stale
  with no self-correction. Must propagate a "re-run all bindings" signal.
- **HIGH — lock discipline:** run `refresh` OUTSIDE `sess.mu` (match
  runPerformBody:5306 / runSubscriberDispatch:5777), fold-apply INSIDE.
- **HIGH — thundering herd:** per-record publish → need per-binding coalescing
  (dirty flag + single in-flight refresh); a bulk write else storms N×sessions.
- **HIGH — shared collection:** per-topic registration is last-write-wins → two
  bindings on one collection collapse; registration must carry a SLICE of bindings.
- **HIGH — model-dependent Cond lifecycle:** `setupSubscriptions` keys on topic
  string only → a stale `where_ (eq "userId" model.me)` never re-registers.
  Re-derive the query from live `sess.model` at delivery, or re-bind on change.
- **INVARIANT:** reactive folds cannot emit Cmds (no Cmd slot) — this is what
  keeps the write→refresh loop finite; enforce it in the binding type.

Griller-2 hybrid blueprint (if BUILD): ride
setupSubscriptions/applyTopicSubsDiff/runSubscriberLoop/markDone; add a reactive
leaf kind carrying `[]reactiveBinding{refresh, fold, *bluedbQuerySub}`; branch
once in runSubscriberDispatch to a lean fold-apply+render helper (mirrors
dispatch tail 4921-4936 inside the panic-rollback 4837-4879, no
guard/msgTags/lifecycle/runCmd); `Live_withReactive` config kernel; post-mount
paint-then-fill initial refresh. ~6 new pieces + 3 blocking fixes.

The shipped `watchCollection` (browser-verified) already delivers the reactivity
through the hardened update/dispatch path; P-R4b's marginal win is removing the
`subscriptions` line + the re-query Msg arm.

## UNIFIED reactive architecture (2026-08-04) — backend-agnostic change source

The change SOURCE is unified per backend; the FAN-OUT is always the existing
Sky.Live broker (in-process single-instance; Redis multi-instance):

- **KV (BlueDB):** the engine change-feed (P-R1) — catches ALL writes at the
  storage source (incl. raw BlueDB.put).
- **SQL (SQLite + Postgres):** the Persist WRITE layer publishes each
  put/insert/delete to the same broker topic (`reactivePublish` /
  `Persist_publishChange`). SQLite has no LISTEN/NOTIFY and is single-process, so
  the write-layer publish is the right (and only) source; Postgres uses it too for
  app writes. Multi-instance Postgres crosses replicas via the existing Redis
  broker — app writes drive reactivity across instances with NO per-backend push.
- **Postgres LISTEN/NOTIFY:** OPTIONAL later add-on, only to catch EXTERNAL
  (non-app) writers. Not needed for app-originated reactivity.

`watchCollection` (and the future `Persist.live`) are backend-agnostic — they
subscribe to the collection topic; whoever publishes drives them.

**Browser-verified (scripts/verify-reactive-todos.{mjs,sh}, two independent
sessions):** KV `examples/56-reactive-todos` 6/6 and SQLite
`examples/57-reactive-todos-sql` 5/5, both ~70ms. The test uses a warmup
handshake (pub/sub has no replay, so it proves both subscriptions are live before
measuring). Demos coalesce re-queries (single in-flight + dirty flag) to avoid the
stale-overwrite race the grill flagged.

## Remaining: fully-declarative Persist.live (P-R4b) — sound design

The grill's SOUND redesign (supersedes the pk-erasing sketch above):
- `live : Conn cap -> Query a -> (List a -> model -> model) -> Live model`,
  carrying `run : Task Error (model->model, List String)` — the RUNTIME gets BOTH
  the fold AND the result pks (`List.filterMap (persistKeyString keyField) rows`),
  so it CAN feed `setResultPks` and actually use the P3 overlap engine. (The
  `(fold, pks)` tuple is still `a`-free → homogeneous `List (Live model)`.)
- Backend-agnostic (NOT KV-gated) now that SQL has a change source.
- Runtime = griller-2 HYBRID: ride setupSubscriptions/applyTopicSubsDiff/markDone
  (free teardown); reactive leaf carries a per-topic SLICE of bindings; a
  fold-apply variant of runSubscriberDispatch (run refresh OUTSIDE sess.mu, apply
  fold INSIDE, render tail inside the panic-rollback contract — NOT the fat
  dispatch); per-binding coalescing; re-derive query from live model at delivery
  (model-dependent Cond); post-mount paint-then-fill; NO-Cmd fold invariant keeps
  it loop-free. ~6 pieces + the resync-broadcast (already fixed at the pump).

## GRILL: "should all Model data be reactive by default?" (2026-08-04)

Four adversarial grills (DX · scalability/perf · reliability · security), each
ranking the design OPTIONS — A opt-in (shipped `Persist.live`) · B provenance/
unified store · C reactive-by-default · D local-first/CRDT.

**Unanimous verdict: A > B > C > D on EVERY axis. Reactive-by-default is rejected.**

- **The premise is a category error.** "Model persisted to the session DB"
  (`autoBlueDB`) is DURABILITY — single-owner, Model→DB, per-session. "Reactive"
  is cross-session SHARING — multi-owner, collection→Model. Opposite directions,
  opposite ownership; they merely share a storage engine. Most Model fields
  (draft, page, selection, error banners, optimistic UI) are single-owner and
  must NEVER broadcast. So "persisted ⇒ reactive" mis-categorizes durability as
  sharing.
- **DX:** opt-in keeps the framework-owned surface a single enumerable list (grep
  `reactiveQueries`); default-reactive dissolves that boundary and arms the
  erase-your-typing footgun permanently. C wins only for read-heavy dashboards.
- **Perf:** a write to an UNWATCHED collection is O(0) past commit (broker returns
  0 with no subscribers). Opt-in = "cost scales with declared-live viewers";
  default = "cost scales with total viewers × all backed fields" — taxes the OLTP
  hot path the north star protects. Dominant cost = full re-render + full-tree
  diff per affected session (V+D).
- **Reliability:** reactivity is a silently-fallible surface (stale-but-plausible
  under a green banner). Opt-in keeps it exactly as large as declared; default
  gives a clean-compiling app staleness + clobber semantics its author never
  reasoned about — the inverse of "if it compiles it works".
- **Security:** per-collection-topic fan-out breaks tenant isolation regardless of
  option; default-reactive multiplies the leak across the whole schema.

The "less wiring" instinct is best served later by INFERRED provenance (a compiler
concern — detect query-backed fields), NOT by flipping the default.

### Confirmed bugs in the shipped design → FIXED this pass
- **SECURITY (critical): cross-tenant record leak.** The change payload carried the
  full record JSON on the shared `__bluedb:<coll>` topic → every tenant's session
  received every other tenant's row (and plaintext across Redis replicas).
  **Fixed:** the broadcast is now a NUDGE (op/coll/pk only, `record` always "");
  subscribers re-query with their OWN filter. `watchCollection`'s `record` is now
  documented as always "". (bluedb_reactive.go `reactivePublish`; Persist.sky.)
- **RELIABILITY: silent-stale-on-query-error.** A transient DB error dropped a
  refresh with no retry (the query layer had no resync while every other layer
  does). **Fixed:** the loop re-arms with capped exponential backoff, a newer
  change supersedes/resets (live_reactive.go `reactiveRefreshWithRetry`).
- **RELIABILITY: panic-rollback cleared `sess.handlers`** before the panic-prone
  render → clicks became silent no-ops. **Fixed:** rollback restores handlers.
- **RELIABILITY: missed-first-change race.** `startReactive` was async, leaving a
  gap between page-load and subscribe where the first write was missed (flaky).
  **Fixed:** the SUBSCRIPTION is now synchronous (before the HTTP response); the
  initial fill stays async on the loop goroutine. Demo 6/6.

### Roadmap (levers, priority order — under-the-hood, API unchanged)
1. **Tenant-scoped topics** `__bluedb:<coll>:<field>:<value>` from the record's
   indexed field (the specced-but-unshipped DECISION A1) + subscriber scoping via
   verified `SessionIdentity` — the real fix for the activity oracle (F3) + the
   IDOR risk (F4). Needs an API to declare the scope field. THE security priority.
2. **Wire the built overlap engine** (`bluedbChangeAffectsQuery`) into `Persist.live`
   — cuts the re-run session COUNT (skip sessions the change can't affect).
3. **Incremental / keyed rendering** — cut V+D from O(tree) to O(change); the
   biggest perf lever, helps every refresh incl. the hot-shared-row case overlap
   can't.
4. Per-tenant fan-out rate-limit (DoS / thundering-herd).
