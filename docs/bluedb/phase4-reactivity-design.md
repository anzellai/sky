# BlueDB / Std.Persist — Phase 4: query-scoped reactivity in the commit path (L4)

> **Status:** design, `feat/bluedb` @ `004cbb95`. Design only — no code specified for
> merge. This doc is written to be **grilled** by ≥2 adversarial reviewers before any
> Phase-4 code is written; the "Grill attacks pre-empted" (§10) and "Open questions /
> weakest points" (§11) sections exist to be attacked.
>
> **Citation convention.** Paths under `runtime-go/bluedb/…`, `runtime-go/rt/…`,
> `sky-stdlib/…`, `docs/…` are in **this** repo (`feat/bluedb`) unless prefixed
> **`(ref)`**, which cites the read-only prior-art worktree
> `.claude/worktrees/ref-exp-bluedb/` (the proven `exp/bluedb` reactive foundation we
> port FROM). The Phase-1/2/3 clean-slate substrate is in THIS repo; the reactive
> surface being ported is in the `(ref)` worktree.

---

## 0. TL;DR — the one-paragraph thesis

**Query-scoped reactivity is the exact dual of the SSI commit-time validation Phase 2/3
already ships.** A serializable transaction records a *read-set footprint*
(`ReadSet{points, ranges, collWitness, indexWitness}` — `engine.go:141-146`,
`readset.go`) and the committer asks, over the `(readTs, commitTs]` window, *"did any
committed `KeyChange` fall into my footprint → **abort**?"* (`validate.go:27-53`). A
**subscription is the same footprint**, derived from the same resolved `Cond` by the
same classifier (`classifyIndexable`, `cond.go:260`), and the commit path asks the
same question with the opposite consequence: *"did any committed `KeyChange` fall into
my footprint → **notify**?"* The membership transition (row **enters**, **leaves**, or
**stays** in a query's result) falls directly out of the `KeyChange.NewIndex` /
`KeyChange.OldIndex` coordinate pair the substrate **already computes and durably
logs** for every committed change (`keychange.go:22-30`, `txn.go:193-244`) — and
because a **delete/update-out** carries its vacated positions in `OldIndex` (derived
from the pre-image at `ensurePreimage`, `txn.go:234-244`), **the classic "deletes
silently drop" reactive bug is structurally impossible: a leaving row fires on the
OldIndex hit exactly as a phantom-disappearance fires an SSI conflict**
(`validate.go:47`, `coordHit` over `ch.OldIndex`). Phase 4 promotes this into the
commit path: a per-process subscriber registry indexed by collection, matched
in-committer against each durable `KeyChange` after `Apply(Sync)`, fanning **precise
transitions** to affected Sky.Live sessions — degrading to a conservative
**re-run nudge** (over-notify, never under-notify) exactly where the SSI layer already
degrades to its `WitnessCollection`/`indexWitness` fallback (`txn.go:183-191`,
`validate.go:41-49`). Cross-instance fan-out re-uses the existing verified-tenant
broker; the capability leak (a multi-replica SQL app can't do cross-process reactivity)
is made **runtime-loud** (compiler WARN + hard-fatal boot check + deploy preflight),
never a silent stale — because compile-time backend-capability gating is impossible by
theorem (clean-slate-architecture.md Decision 5).

---

## 1. What Phase 4 consumes (the substrate is already built)

Phase 4 writes **no new engine format**. Every primitive it needs already exists and is
frozen by the Phase-1 comparer commitment. This section pins the exact substrate seams.

### 1.1 The changelog already carries per-row transitions

`KeyChange` (`keychange.go:22-30`) is one committed row-level change:

```go
type KeyChange struct {
	Coll     CollID       // owning collection (per-change stamped, txn.go:270-282)
	Pk       []byte       // the user-key — point-read / row-scoped match
	Op       Op           // OpPut | OpDelete
	Record   []byte       // put: row bytes (L4 body); delete: nil. Validation ignores it.
	NewIndex []IndexCoord // positions the row NOW occupies (put); nil for delete
	OldIndex []IndexCoord // positions the row VACATED (update/delete); nil for insert
}
```

The `NewIndex`/`OldIndex` pair is **the reactive membership signal**, and it is
populated for the delete case: `Txn.Put` sets `newIndex = indexer(userKey, value)` and
derives `oldIndex` from the pre-image; `Txn.Delete` sets `newIndex = nil` and keeps the
pre-image `oldIndex` (`txn.go:193-218`, `ensurePreimage` `txn.go:234-244`). The list is
serialized into the **opaque, versioned changelog payload** (`keychange.go:42-63`
`EncodeChangelogPayload`; the format tag `payloadFmtV1` lets the shape evolve without a
store rewrite — `keychange.go:39-42`).

### 1.2 The commit path already exposes exactly two durable-emit points

The single committer (`committer.go:33-51`) group-commits and, **after** `Apply(Sync)`
returns durable, appends each commit's decoded changes to the in-RAM recent ring
(`committer.go:141-150` blind path; `committer.go:305-310` transactional path). These
**two post-`Apply` sites are the Phase-4 fan-out hook** — durable-before-notify by
construction (see §7, the ordering grill). The changes are *already decoded there* for
the ring append, so the fan-out reuses that decode, not a second one.

### 1.3 The validation window machinery is the setup-race machinery

`recentRing.after(readTs)` (`recent_changes.go:40-51`) returns every `KeyChange` with
`commitTs > readTs` in O(commits-since-readTs) — a bounded tail walk — with a
`spilled=true` fallback to the durable `Changelog.Tail(readTs)` (`changelog.go:17-47`)
when a reader lags below the ring floor. This is **exactly** the "subscribe pins a
`readTs`, backfill the gap, then go live" discipline Phase 4 needs (§7). The
`WatermarkRegistry` (`engine.go:163-178`) pins a reader token so GC never drops a
version or changelog entry a live subscription still needs, and `Advance(tok, readTs)`
(`engine.go:171`) lets a subscription move its floor forward as it consumes commits
(the R3 advancing-watermark, clean-slate-architecture.md R3).

### 1.4 The predicate evaluator + range classifier already exist

- `bluedbEvalCond(cols, *CondNode) bool` (`cond.go:52-105`) — the row predicate,
  SQL-3-valued-logic-collapsed. **The residual filter** Phase 4 applies to a `NewIndex`
  hit to confirm true membership (the same role the query executor gives it —
  cond.go header, "the exact-filter applied to rows a scan returns").
- `classifyIndexable(*CollSchema, *CondNode) (indexHit, bool)` (`cond.go:260-285`) —
  decides whether a `Cond` is a clean single-column range/equality on a declared,
  range-optimized index (→ a precise `[lo,hi]` footprint) or must fall through to the
  conservative collection witness. **This is the exact function that decides a
  subscription's footprint tier** (§3).
- `encodeScanRange` / `encodeIndexKey` (`index_key.go:57-116`) — the ONE canonical
  order-preserving encoder both the scan bound AND the change coord go through, so a
  subscription's range bound and a `KeyChange` coord byte-match by construction
  (`index_key.go:42-45`, the "no second encoder" invariant).
- `validate` / `coordHit` (`validate.go:27-71`) — **the matcher to generalize** (§2).

### 1.5 The Go seam Phase 4 fills

`EmbeddedBackend` (`embedded.go:23-35`) already declares
`_ CrossInstanceReactive = (*EmbeddedBackend)(nil)` and `Capabilities()` returns
`InProcessReactive: true, CrossInstanceReactive: true` (`embedded.go:386-396`).
`Watch(CollSchema, QueryPlan) (Subscription, error)` currently returns
`ErrReactiveSeamPhase4` (`embedded.go:403-405`). **Phase 4 = implement `Watch` +
the commit-path evaluator + the fan-out.** The `Subscription`/`Change` shapes
(`backend.go:92-105`) are the frozen seam; §6 extends `Change` with the transition tag.

---

## 2. Delta-match: the SSI-validation dual (the core)

### 2.1 The symmetry, stated precisely

| | SSI validation (Phase 2, shipped) | Reactive delta-match (Phase 4) |
|---|---|---|
| Footprint | `ReadSet{points,ranges,collWitness,indexWitness}` recorded by a txn's reads (`txn.go:51-56,162-191`) | The **same** struct, derived once from a subscription's resolved `Cond` (§3) |
| Window | `ring.after(readTs)` = committed changes in `(readTs, commitTs]` (`recent_changes.go:40`) | The **one** just-durable commit's `[]KeyChange` (`committer.go:305-310`) |
| Question | Did any change hit the footprint? (`validate.go:27`) | Which subscriptions' footprints does this change hit? |
| On hit | **Abort** the committing txn (`committer.go:257-266`) | **Notify** the subscription (§4) |
| Fallback | `WitnessCollection`/`indexWitness` → over-reject (`validate.go:41-49`) | Same witnesses → over-**notify** (§3.3) |

The matcher is a **refinement** of `coordHit` (`validate.go:57-71`). Validation ORs
`NewIndex` and `OldIndex` because it only cares *whether* a conflict exists
(`validate.go:47`: `coordHit(rs, ch.NewIndex) || coordHit(rs, ch.OldIndex)`). Reactivity
must **distinguish which side hit** to classify the transition:

```
enteredRange := coordHit(subFootprint, ch.NewIndex)   // row now occupies a watched coord
leftRange    := coordHit(subFootprint, ch.OldIndex)   // row vacated a watched coord
```

### 2.2 Membership transition — the full truth table

For a subscription with an **indexable footprint** (a precise range on the query's
leading indexed predicate column), and a committed `KeyChange ch`:

| `ch.Op` | `enteredRange` | `leftRange` | Residual `bluedbEvalCond(ch.Record)` | Transition | Justification |
|---|---|---|---|---|---|
| Put (insert) | true | false | matches | **Enter** | row appeared inside the range and satisfies the full predicate |
| Put (insert) | true | false | **no match** | **none** | inside the index range but a residual clause (e.g. a second AND term) excludes it — the index range is an over-approximation, residual is authoritative (`cond.go:257-259`) |
| Put (update) | true | true | matches | **Stay** (value changed) | still in range; row body updated |
| Put (update) | true | false | matches | **Enter** | moved INTO the range from outside |
| Put (update-out) | false | true | — | **Leave** | moved OUT of the range → **must fire** (see §2.3) |
| Delete | false | true | — (`Record=nil`) | **Leave** | vacated its position; the delete-re-run gate (§2.3) |
| any | false | false | — | **none** | change is irrelevant to this subscription |

The residual re-eval is applied **only on a `NewIndex` hit** (Enter/Stay candidates),
where `ch.Record` is present. A `Leave` never re-evals (a delete has `Record=nil`;
an update-out's new row is by definition outside the range) — it fires purely on the
`OldIndex` hit. This is why **Leave cannot be lost**: it does not depend on decoding a
body, only on the durably-logged vacated coordinate.

### 2.3 Delete-re-run correctness — the proof

**Claim:** a row leaving a query's result set ALWAYS fires a `Leave`, for both an
outright delete and an update that moves the row out of the predicate range.

**Proof.** Consider a subscription watching `WHERE status = 'open'` (a range
`[open,open]` on the `status` index) currently showing row `R`.

1. *R is deleted.* `Txn.Delete(R)` runs `ensurePreimage` (`txn.go:234-244`), which reads
   `R`'s pre-image at `readTs` and sets `bw.oldIndex = indexCoords(userKey, pre)` — i.e.
   the `status=open` coordinate. `buildReq` emits `KeyChange{Op:OpDelete, NewIndex:nil,
   OldIndex:[open-coord], Record:nil}` (`txn.go:275-282`). At match time
   `enteredRange=false`, `leftRange = coordHit(sub, [open-coord]) = true` → **Leave**. ∎
2. *R is updated `status: open → closed`.* `Txn.Put(R)` sets `newIndex =
   [closed-coord]`, and `ensurePreimage` gives `oldIndex = [open-coord]`
   (`txn.go:193-204`, `:234-244`). At match time `enteredRange = coordHit(sub,
   [closed-coord]) = false` (closed ∉ `[open,open]`), `leftRange = coordHit(sub,
   [open-coord]) = true` → **Leave**. ∎

The bug this closes ((ref) `docs/bluedb/reactive-sync-design.md:296-301`, "the
pk-erasing bug") is the failure mode where a change feed carries only the *new* row
(or, for a delete, nothing) and the subscriber has no way to learn a row it currently
displays is gone. Here the substrate **durably logs the vacated coordinate for every
delete and update** as a side effect of the pre-image read the SSI layer already does
for lost-update protection (`txn.go:233` docstring). Reactivity gets delete-safety
**for free** from a mechanism that exists for isolation.

**Belt-and-braces (the prior-art `resultPks` guard).** For the conservative tier (§3.3)
and cross-instance path (§5) where a subscription re-runs rather than reads coords, the
ported `bluedbQuerySub.resultPks` set ((ref) `bluedb_reactive.go:94-98`) makes Leave
detection independent of `OldIndex`: `if s.resultPks[rc.Pk] { return true }` ((ref)
`bluedb_reactive.go:129-131`) fires on any change to a pk currently displayed, delete or
not. The two mechanisms (coord-precise + resultPks-membership) are redundant on purpose:
the indexable tier proves Leave from the log; the conservative tier proves it from the
tracked result set. Neither can silently drop a leaving row.

### 2.4 Why not the prior-art re-eval-the-record approach alone

The `(ref)` engine had no `OldIndex` — `bluedbChangeAffectsQuery` re-decodes `rc.Record`
and re-runs `bluedbEvalCond` ((ref) `bluedb_reactive.go:124-142`), tracking `resultPks`
in RAM to catch deletes (`rc.Record=""` for a delete → the `resultPks` membership test is
the *only* delete signal). That works but (a) needs the record body on every change (a
cross-tenant-leak risk the prior art mitigates by never broadcasting the body — (ref)
`bluedb_reactive.go:157-162`), and (b) makes Leave detection depend on per-subscription
RAM state that must be kept exactly in sync. The clean-slate substrate's `OldIndex`
makes the **embedded, single-instance** Leave **structural and body-free**. Phase 4 uses
the coord approach as the primary embedded mechanism and keeps `resultPks` as the
conservative/cross-instance backstop (§3.3, §5). Both ship; §2.3's proof relies on the
coord path, §5's cross-instance relies on the `resultPks` path.

---

## 3. Subscription model

### 3.1 What a subscription IS

A subscription is a **footprint + a delivery target + a scope key**:

```go
// registered in the per-process reactive registry (§4.1)
type subscription struct {
	id        subID
	coll      CollID          // the ONE collection it watches (per-collection index key)
	tenant    string          // verified sync-unit scope (§3.4); "" = process-global
	footprint *ReadSet        // the SAME struct SSI uses — points/ranges/collWitness/indexWitness
	plan      QueryPlan       // for residual bluedbEvalCond + re-run on the conservative tier
	resultPks map[string]bool // tracked result set (Leave backstop + cross-instance, §2.3)
	lastTs    HLC             // highest commitTs applied — monotonic apply + dedup (§7)
	deliver   chan Change     // non-blocking; overflow → resync (§4.3)
}
```

Three scope shapes, all expressed as one footprint:

- **Whole collection** — `WitnessCollection(coll)` → `collWitness[coll]=true`
  (`txn.go:188-191`). Every change to the collection notifies. `plan.Where = CondTrue`.
- **Single row / PK** — a `points` entry keyed by the row's user-key. A `KeyChange` with
  `Pk == that key` notifies (`validate.go:35-39`). This is `watch this one row`;
  a delete of it fires (its `Pk` matches, `Op=OpDelete`, `Record=nil` → the caller sees
  a Leave).
- **Query predicate** — `classifyIndexable(schema, resolvedCond)` (`cond.go:260`):
  a clean single-column range → a `ranges` entry (`indexRange{index,lo,hi}`,
  `readset.go:20-24`); anything else → `collWitness`/`indexWitness` (the conservative
  tier, §3.3). The resolved `Cond` is the **already-shared `Cond`/`Query` algebra**
  from `Std.Db.Store` (clean-slate-architecture.md §L3), lowered to a `QueryPlan`
  (`backend.go:174-182`) exactly as a query is.

**The footprint is derived once, at `Watch`, by reusing `classifyIndexable`** — the
identical code path a Phase-2 txn-`Query` uses to record its read-set ((see
`embedded.go`'s `Transaction`→`embeddedTx.Query` read-set contract, `backend.go:52-54`).
So a subscription's watched range and a committed change's coord are guaranteed to be in
the same encoding — no drift class.

### 3.2 Lifecycle — bound to a Sky.Live session

A subscription's life is the session's life. Ported from `(ref)` `live_reactive.go`:

1. **Create on mount.** When a Sky.Live session with reactive bindings starts,
   `startReactive(sess)` ((ref) `live_reactive.go:82-147`) reads the bindings from the
   Model, resolves the tenant topic per binding, and registers. Phase-4 change: instead
   of subscribing a whole-collection broker topic and re-querying on any change, it calls
   `Backend.Watch(coll, plan)` and threads the returned `Subscription.Changes()` channel
   into the session loop.
2. **Live.** The session loop (`reactiveLoop`, (ref) `live_reactive.go:170-194`) selects
   on the subscription channel; each `Change` is coalesced (§4.4) and applied.
3. **Drop on session end.** `teardownReactive()` ((ref) `live_reactive.go:151-163`) —
   called from `markDone` — closes the subscription, which unregisters it from the
   registry AND releases its `WatermarkRegistry` token (`engine.go:172` `Release`), so a
   closed tab stops pinning the GC floor. Idempotent.

The session goroutine stays identity-stamped (`setGoroutineLiveSession(sess)`, (ref)
`live_reactive.go:178`) so the tenant scope re-derivation is fail-closed to empty on a
missing identity — never a cross-tenant leak.

### 3.3 Non-indexable predicates — the conservative tier

`classifyIndexable` returns `ok=false` for: an OR/nested/NOT predicate, a predicate on a
non-declared column, an `IS NULL`/`IS NOT NULL` leaf, or any predicate on a
NOT-order-preserving column (`Real`/`Money`/`Blob`/`Codec.map` — `cond.go:243-249`,
`index_key.go:34-40`). For these, the subscription's footprint degrades to a **witness**,
mirroring `Txn.ScanFallback`/`WitnessCollection` (`txn.go:183-191`):

- **Index-level witness** (`indexWitness[idx]=true`) — a fallback-typed indexed column:
  any `KeyChange` with a coord on that index (New OR Old) matches (`validate.go:60-62`).
- **Collection-level witness** (`collWitness[coll]=true`) — the coarsest: any change to
  the collection matches (`validate.go:41-44`).

**On a witness match, Phase 4 does NOT compute a precise transition. It marks the
subscription dirty and re-runs its query** (the self-healing nudge — (ref)
`unit-architecture.md:54-60`), coalesced (§4.4). The re-run produces a fresh result set;
the subscriber diffs it against `resultPks` to derive Enter/Leave/Stay, and updates
`resultPks`. This is **over-notify, never under-notify**: a witness fires on changes that
may not actually affect the query, costing a re-run, but it can never miss one.

**The exact boundary + cost.** Precise-delta (a `ranges` hit, no re-run) is available iff
the query is a **single-column range/equality on a declared range-optimized (int/text/
bool ascending) index** — the `classifyIndexable` v1 envelope (`cond.go:251-259`). Cost of
a precise match: an O(coords) `coordHit` over the change's index coords (typically 1-few),
plus one `bluedbEvalCond` residual on an Enter/Stay candidate — no query, no scan.
Cost of a conservative match: one full `bluedbEvalCond`-filtered re-query
(`bluedbRunQuery`, (ref) `bluedb_query_kernel.go:482-566`) per coalesced burst. The
conservative tier is thus **correct-but-O(query) per relevant commit**; the design goal
is that the common Sky.Live list view (`WHERE tenant=… AND status=…` on an indexed
column) lands in the precise tier. **We do NOT over-claim precise deltas for arbitrary
`Cond` — the honest envelope is single-column-ascending-indexed, everything else re-runs.**

### 3.4 Tenant scoping — the verified sync unit

The scope key is **who shares state** (the tenant), not **what table changed** — a
security model, not a storage mechanism (clean-slate-architecture.md §L4; (ref)
`unit-architecture.md:16-23`). The tenant is read from the **verified**
`SessionIdentity(sess)` `Claims["tenant"]` (`runtime-go/rt/session_identity.go:76`, which
returns `ok=false` unless a gate stamped `sess.identityValid` — `live.go:2089-2090`,
distinguishing "no gate ran" from "anonymous"; (ref) `bluedb_reactive.go:42-49`
`reactiveTenantTopic`), read via `SessionIdentity(currentLiveSession())`
(`live_session_ctx.go:34`) on the identity-stamped session goroutine — never from record
data (forgery-safe). This gives:

- **Per-process:** the registry buckets subscriptions by `(coll, tenant)` so a change is
  matched only against subscriptions whose tenant is entitled to see it. A change's
  tenant is derived from its own row/collection scope; a subscription on tenant A never
  sees tenant B's change even under a coarse collection witness (the witness is scoped
  within the tenant bucket).
- **Cross-instance:** the broker topic is `reactive:<tenant>:<coll>` ((ref)
  `bluedb_reactive.go:42-49`) — a Redis SUBSCRIBE per tenant, so only instances hosting a
  tenant-A session receive tenant-A changes (§5).

**Prerequisite (R6, open).** The whole tenant-scoping is inert without a
framework-verified `SessionIdentity` on the *standard* `Std.Auth` login path. Historically
`sess.identity` was populated only by the sub-app mount gate (clean-slate-architecture.md
R6). Phase 4 depends on `Live.withIdentify` populating it on the standard path; §11 flags
this as a hard dependency, not a Phase-4 deliverable.

---

## 4. Fan-out, coalescing, back-pressure

### 4.1 The registry + the commit-path match

The registry lives on the `EmbeddedBackend` (`embedded.go:23-29`, alongside the existing
`byName`/`serials` maps) — **per-process**, one per open engine:

```go
type reactiveRegistry struct {
	mu    sync.RWMutex
	// primary index: only subscriptions on the CHANGED collection are ever visited.
	byColl map[CollID]map[subID]*subscription
}
```

The commit path, at the two post-`Apply` sites (`committer.go:141-150`, `:305-310`),
hands the just-durable `(commitTs, []KeyChange)` to a **non-blocking dispatcher** (a
buffered channel to a separate fan-out goroutine — the changefeed pattern, (ref)
`changefeed.go:112-122` `emitChanges`: `select { case ch <- changes: default:
overflow=1 }`). **The committer never blocks on fan-out** — a slow dispatcher drops its
batch and latches an overflow flag that forces subscriber resync. This preserves the R1
committer-never-stalls contract ((ref) `changefeed.go:5-9`).

The fan-out goroutine, per change `ch`:

1. Look up `byColl[ch.Coll]` — **O(1); a change on collection X never visits a
   subscription on collection Y.** If empty, drop the change (the `hasSubs()`
   short-circuit, (ref) `changefeed.go:80-85`).
2. For each subscription in that bucket whose tenant matches the change's scope:
   compute the transition via §2.2 (`coordHit` New/Old + residual) for the indexable
   tier, or mark-dirty for the witness tier.
3. On a real transition, non-blockingly enqueue a `Change` on `sub.deliver` (§4.3).

### 4.2 Shared predicate evaluation (the amplification bound)

Naively this is O(changes × subs-on-collection). The **honest** bounds:

- **Collection-partitioned:** already only visits subs on the changed collection.
- **Shared-predicate coalescing:** many subscriptions in a tenant watch the **identical**
  `(coll, resolvedCond)` (every tenant-mate viewing the same list). The registry
  **de-dups footprints**: identical resolved plans evaluate the predicate **once** and
  fan the single result to all N sharing subscriptions. This drops match *detection* from
  O(changes × subs) toward **O(changes × distinct-predicates-on-collection)**.
- **Range-index bucketing (scaling lever, may defer to 4c):** within a collection, index
  the `ranges` footprints by `IndexID` in a sorted structure so a coord lookup is
  O(log P + hits) instead of O(P) over all P predicates. v1 (4a) may do a linear walk over
  distinct predicates and be honest it is O(distinct-predicates); the interval index is the
  documented lever if the bench (4c) shows it's needed.

### 4.3 Reaching the subscriber — a typed Msg into the session loop

A matched `Change` is **not** applied to any Model directly. It is delivered as an event on
`sub.deliver`, which the session's `reactiveLoop` ((ref) `live_reactive.go:170-194`)
selects on and turns into a **Model fold** run under the **per-session serializing mutex**
(§8). This is byte-for-byte the path a broker broadcast already takes today:
`runSubscriberDispatch` (`runtime-go/rt/live.go:5767`) decodes an event → `msg`
(`sky_call(toMsg, payload)`), takes `sess.mu.Lock()`, runs `app.dispatch(sess, msg)` (the
same update loop as `Cmd.perform`, `live.go:5787`), snapshots a frame under the lock, and
non-blockingly sends it to `sess.sseCh` (`live.go:5809-5811`). Everything downstream — the
fold, the `view` re-render, `diffTrees`, the multi-tab fan-out (§8), the SSE frame — is
**unchanged Sky.Live machinery** ((ref) `reactiveRefreshOnce` `live_reactive.go:277-366`;
current-repo `dispatch` `live.go:4792`, `chooseSSEFrame` `live.go:2795`). Phase 4
**composes with**, does not replace, the TEA render path. The SSE send stays non-blocking
with drop→resync (`live.go:5370-5371` drop → `recordSseDrop` + `markAllConnsOutOfSync`;
CLAUDE.md `SKY_LIVE_SSE_BUFFER` → `sky_live_sse_drops_total` + inline resync).

### 4.4 Coalescing + back-pressure

- **Burst coalescing:** `drainChangeBurst(ch)` ((ref) `live_reactive.go:198-209`)
  non-blockingly drains all queued changes before a single re-render, so a bulk write
  (or a rapid tick stream) produces ONE frame, not one-per-row.
- **Precise-delta coalescing:** multiple precise Enter/Leave/Stay in one burst fold into
  the Model list in arrival order, then one render.
- **Monotonic apply + dedup:** each subscription carries `lastTs`; a change with
  `commitTs <= lastTs` is dropped (guards the setup-race backfill/live overlap, §7, and a
  conservative re-run racing a precise delta).
- **Overflow → resync:** if `sub.deliver` overflows (a wedged session), the flag forces a
  full re-query on next drain ((ref) `changefeed.go:41-43` `Overflowed()`), which
  self-heals all prior misses ((ref) `unit-architecture.md:54-60`).

### 4.5 Realistic-N honesty (grill fix #9 / R7 — NOT a rigged N=2 gate)

**The O(writes) win is query RE-EVALUATION, not SSE fan-out.** Query-scoped delta-match
gets match *detection* to O(writes × distinct-predicates). But **delivery to the N live
sessions in a tenant is irreducibly O(N)** — the same wall as any LiveView/Phoenix system
(clean-slate-architecture.md R7). The N=2 two-browser demo **hides** this. Phase 4's gate
(§9, 4c) therefore requires, beyond the 2-browser demo, **EITHER**:

- **(a)** a **realistic-N shared-feed benchmark** — hundreds of sessions per tenant on one
  shared query — measuring (i) per-commit match-detection cost (must be
  ~O(distinct-predicates), independent of N), (ii) per-commit fan-out delivery cost
  (expected O(N), characterized and bounded), (iii) memory per subscription; **OR**
- **(b)** an explicit **scope decision** deferring high-N shared feeds to Phase 6 (keyed
  render + horizontal spread), stated as a first-class criterion — not buried in prose.

The headline states the distinction plainly: **re-evaluation is O(writes); delivery is
O(N).** The design's job is to make (i) provably N-independent; (ii) is the honest,
LiveView-shaped floor.

---

## 5. Cross-instance (multi-replica)

**Precise delta-match is LOCAL to each instance.** The commit-path evaluator on instance
A holds only instance A's subscription registry. A tenant-A session may live on instance
B. Therefore cross-instance reactivity is **broadcast-the-change, match-locally**:

1. Instance A commits a change. Its fan-out goroutine, in addition to matching local subs,
   publishes the **tenant-scoped raw change** on the broker topic `reactive:<tenant>:<coll>`
   ((ref) `bluedb_reactive.go:217-221` `reactivePublishScoped`) — via the existing
   `Broker` interface (`runtime-go/rt/live_topics.go:108`:
   `Subscribe/SubscribeWithOwner/Publish/Close`). The broker itself is tenant-agnostic — it
   keys only on the topic STRING and knows `Origin`/`ownerSid`, not tenant — so tenant
   scoping is **encoded in the topic key** (`reactive:<tenant>:<coll>`, exact-match), which
   is exactly what the existing registry supports. In-process tier: `topicRegistry`
   (`live_topics.go:130`), non-blocking `Publish` (`live_topics.go:270,303-312`). Redis
   tier: `redisBroker` (`live_redis_broker.go:102`), selected by `store=redis` (→
   `brokerForRedisStore`, `live_store.go:1068`) or `SKY_LIVE_BROKER_URL`
   (`maybeOverrideBroker`, `live_redis_broker.go:480`); `SKY_LIVE_BROKER=inprocess` forces
   the in-process tier (`live_redis_broker.go:496`).
2. Every instance hosting a tenant-A session is SUBSCRIBED to `reactive:<tenant>:<coll>`
   (the topic is set up at `startReactive`, (ref) `live_reactive.go:111-122`
   `app.topics.Subscribe(topic)`; current-repo `SubscribeWithOwner` at
   `live.go:5670`/`live_topics.go:214`). It receives the change (Redis `receiveLoop` drops
   its own echo via `InstanceID`, `live_redis_broker.go:266`) and runs its OWN local
   delta-match against its OWN subscriptions.
3. The tenant scoping bounds *which* instances receive the change: only instances hosting a
   tenant-A session ever get it. Match compute happens once per receiving instance — fine,
   each instance only matches its own subs.

**The body-safety nuance.** The prior art broadcasts a **nudge only** — `Record` always
`""` ((ref) `bluedb_reactive.go:157-162`) — to prevent cross-tenant body leaks over the
broker, and the receiving instance re-queries with its own tenant filter. With a
**verified per-tenant topic** the change body MAY be carried safely ("tenant-mates are
entitled to it", clean-slate-architecture.md §L4) → the receiving instance can compute a
**precise** transition from the broadcast `KeyChange` (New/Old coords + Record) rather than
re-querying → O(writes) cross-instance too. **Design decision:** carry the full
tenant-scoped `KeyChange` (coords + record) on the verified per-tenant topic; fall back to
the nudge-only + re-query where the identity is unverified (fail-closed). This is the
`resultPks`-membership Leave path (§2.3 belt-and-braces) doing the work when the receiving
instance didn't originate the pre-image.

**Capability consequence:** cross-instance precise reactivity requires the broker
(Redis). A multi-replica app whose backend/broker can't carry it must fail loud (§6) —
never silently show one replica's users a stale list.

---

## 6. Capability check (runtime-loud) + the Sky surface

### 6.1 The three-part safety net (NOT a compile-time gate)

Compile-time backend-capability gating is **impossible by theorem**
(clean-slate-architecture.md Decision 5 / R5): a capability UNION isn't expressible in
Sky's HM (no type classes, no HKT); the Postgres NOTIFY tag is un-mintable (dialect is a
runtime property, `connectRelational` returns `Relational` for both sqlite and pg); and
the backend axis is irreducibly RUNTIME (the image is built once, the backend injected at
boot via env — HM types cannot depend on a runtime value). So safety is:

1. **Compiler WARN (compile-visible).** Whether an app *uses* `watch`/`live`/
   `withReactive` is a static fact. If it does, the compiler emits a build-time WARN:
   *"this app requires a reactive-capable backend."* (Emitted from the Rust HIR pass that
   sees the `Ffi.kernel "Persist_watch"` / `withReactive` reference.)
2. **Runtime-loud HARD-FATAL boot check.** At startup the runtime probes
   `Backend.Capabilities()` (`backend.go:107-115`) and matches it against the declared
   reactive requirement AND the replica topology:
   - `InProcessReactive` is **always true** (`embedded.go:388`) → **single-instance
     `watch` never fails** on any backend (KV / sqlite / pg). ~99% of apps.
   - **Multi-replica** (replica count > 1, from the deploy/env — `SKY_LIVE_STORE` shared +
     replica hint) **AND** the app declares reactive bindings **AND**
     `Capabilities().CrossInstanceReactive == false` (sqlite/pg in v1) → **HARD FATAL at
     boot** with a concrete message: *"app uses reactive `watch` but backend=sqlite can't
     do cross-process reactivity across N replicas — use the embedded engine, or add the
     Postgres LISTEN/NOTIFY bridge, or run single-instance."* **NEVER a silent stale read.**
     This is the seam Phase 3 explicitly deferred here (phase3-status.md:272-279).
3. **CI / deploy preflight.** `sky doctor` (and the SkyDeploy preflight) boots with the
   **target** `[data]` config + replica count and asserts capabilities BEFORE production
   traffic — catching the mismatch at deploy, not at 2am.

The **embedded default is always cross-instance-reactive** (`embedded.go:389`), so the
fatal only fires for the deliberate SQL-backend + multi-replica + reactive combination —
where a silent stale would be a correctness disaster and a loud fatal is the only honest
outcome.

**Completeness (the grill target).** The backend × deployment matrix, every cell:

| Backend | Single-instance | Multi-replica |
|---|---|---|
| **embedded (BlueDB)** | ✅ commit-path (InProcess) | ✅ commit-path + broker (CrossInstance=true) |
| **sqlite** | ✅ in-process pub/sub (InProcess=true) | ❌ **HARD FATAL** (CrossInstance=false; no cross-process notify) |
| **postgres** | ✅ in-process pub/sub | ❌ **HARD FATAL** in v1 (CrossInstance=false until the LISTEN/NOTIFY bridge, R10) → ✅ when the bridge ships |

There is **no cell that silently degrades**: every ❌ is a boot fatal, every ✅ is wired.
R10 (external SQL writers bypassing the commit path) is documented honestly as
"reactivity covers Sky-originated writes; external SQL writes need the NOTIFY bridge"
(clean-slate-architecture.md R10) — a known scope line, surfaced, not hidden.

### 6.2 The Sky surface

**Raw tier (the 0.001%)** — `Std.Persist`:

```elm
-- opaque handle; closed when the Sub is torn down
type Subscription a

type ChangeOp = Enter | Leave | Update

type alias Change a =
    { op  : ChangeOp     -- membership transition (§2.2)
    , key : String       -- primary key of the affected row
    , row : Maybe a      -- Just for Enter/Update; Nothing for Leave (delete carries no body)
    }

watch   : Conn cap -> Collection a -> Query a -> Task Error (Subscription a)
changes : Subscription a -> Sub (Change a)     -- typed Sub-tier delivery
unwatch : Subscription a -> Task Error ()
```

The Go `Change` (`backend.go:99-105`) is extended with a `Transition` tag
(`ChangeEnter|ChangeStay|ChangeLeave`) computed in the commit path (§2.2); `Op` stays for
the row/collection tiers. `row = Nothing` on Leave is load-bearing: a delete's
`Record=nil` (`keychange.go:27`) means the deleted body is genuinely unavailable
(§11 weak point).

**Easy tier (the 99.99% — magic-first, goal #3)** — Sky.Live builder integration, matching
the v0.19 `Live.config |> withX` convention (CLAUDE.md; modeled on the shipped
`Live.withAnalytics`/`withAnalyticsIdentify`):

```elm
import Std.Live exposing (app, config, withReactive)

-- (a) explicit: each Change becomes a typed Msg the app folds into Model
Live.watchCollection : Collection a -> Query a -> (Change a -> msg)
                    -> AppConfig msg -> AppConfig msg

-- (b) magic: bind a query to a Model list field; the runtime MAINTAINS the field
--     (applies Enter/Leave/Update to the list, re-sorts, re-renders) — no Msg wiring
Live.liveInto : Collection a -> Query a -> (model -> List a) -> (List a -> model -> model)
             -> AppConfig msg -> AppConfig msg

main =
    app
        ( config { init = …, update = …, view = …, subscriptions = …, routes = …, notFound = … }
            |> Live.liveInto users (Persist.query users |> where_ (eq "status" "open"))
                 .openUsers (\rows m -> { m | openUsers = rows })
        )
```

`liveInto` re-homes the `(ref)` `Persist.liveInto`/`liveNamed` builders (which already
carry the `condPlan` the runtime ignored — (ref) `Persist.sky:968`, "carried but
unused"; `live_reactive.go:10-12`, "v1 refreshes at COLLECTION scope"). **Phase 4 wires
`condPlan` → the subscription footprint** — that promotion IS L4. The **`Live.autoBlueDB`
whole-Model-is-a-collection** magic (clean-slate-architecture.md §L0) is the ceiling: the
entire Model is one scope-keyed reactive row (deferred to Phase 5's DX collapse, but the
Phase-4 engine is what makes it live).

---

## 7. Ordering & consistency — the setup race

**Two failure modes to close: (1) a subscriber must never see a change before its commit
is durable; (2) a subscriber must never miss a change committed during subscription
setup.** Both are closed by mirroring the Phase-2 window-boundary discipline.

**(1) Durable-before-notify.** Fan-out is dispatched **only from the two post-`Apply(Sync)`
sites** (`committer.go:139-150` blind, `:303-310` txn) — *after* `advanceDurableHi` and
the ring append, which run only when `err == nil` from `Apply(b, pebble.Sync)`
(`committer.go:135-138`, `:300-304`). A subscriber therefore cannot observe a change that
isn't durable; on a durability fault the engine seals and no fan-out fires
(`committer.go:301-302`).

**(2) No-miss setup race.** `Watch` uses the same begin-snapshot boundary a txn uses
(`txn.go:89-104`, R-2.8):

1. **Pin.** `tok, readTs := engine.Readers().Register()` (`engine.go:169`) — atomically
   picks `readTs = durableHi` AND registers a reader token in one critical section (closes
   the 2a TOCTOU) so GC won't drop versions/changelog below `readTs`.
2. **Baseline.** Run the initial query at the `readTs` snapshot → the starting result set →
   seed `resultPks`.
3. **Backfill the gap.** Drain `Changelog.Tail(readTs)` (`changelog.go:17-47`) — every
   change committed *during* steps 1-2 — and apply them (dedup by `commitTs > lastTs`).
   This is the exact `recent_changes.go:40` "validate against `(readTs, now]`" window, read
   from the durable changelog.
4. **Go live.** Register in `byColl` and consume live changes; drop any with
   `commitTs <= lastTs` (the backfill/live overlap can double-deliver a boundary commit —
   `lastTs` dedup makes it idempotent).
5. **Advance.** As the subscription consumes commits, `engine.Readers().Advance(tok,
   lastTs)` (`engine.go:171`) moves its GC floor forward (R3 advancing watermark) so a
   long-lived tab doesn't pin the floor at its start-of-session `readTs` forever.

There is **no window** in which a commit is neither in the baseline (≤ readTs) nor in the
backfill/live stream (> readTs): `readTs` is a clean cut, and the ring/`Tail` spill
fallback (`recent_changes.go:40-51`, `committer.go:230-252`) guarantees the > readTs half is
never lost even if the in-RAM ring trimmed under a slow setup (it falls back to the durable
changelog, fail-CLOSED — `committer.go:245-250`).

---

## 8. Interaction with the per-session mutex + multi-tab fan-out

CLAUDE.md: **one session = one Model, serialized by a per-session mutex; multi-tab of the
same session mirrors one shared view.** Phase-4 reactivity composes cleanly:

- A matched `Change` becomes a **Model fold** applied **under `sess.mu`**
  (`runtime-go/rt/live.go:2146` the per-session mutex; (ref) `live_reactive.go:284-334`) —
  serialized against user-driven dispatches (last-writer-wins, exactly the existing
  `Cmd.perform`/`runSubscriberDispatch` discipline, `live.go:5767-5811`). A reactive fold
  and a click never race; the mutex orders them.
- The resulting frame is written to the single per-session ingress channel `sess.sseCh`
  (`live.go:2157`); the `ensureSSERelay` goroutine (`live.go:6596`) drains it and
  `fanOutFrame` (`live.go:6689`) fans each frame to **all** the session's live SSE
  connections (`sess.sseConns`, `live.go:2181`) — non-blocking per-conn, drop→resync on a
  full buffer (`live.go:6699-6710`). Reactivity produces the frame; the multi-tab fan-out is
  unchanged (CLAUDE.md "Per-session fan-out"). Every tab of the session converges to the
  same list.
- **Panic-rollback discipline ports verbatim** ((ref) `live_reactive.go:313-334`): a
  reactive fold that panics restores `sess.model`, `sess.lastComputedBody`, AND
  `sess.handlers` (without the handler restore, `prevTree`'s handler IDs dangle → silent
  no-op clicks) — the same invariant the existing reactive loop already enforces.
- **Two people browsing INDEPENDENTLY are two sessions, not two tabs** — they get two
  subscriptions with two tenant/identity scopes; cross-session sync is the tenant-topic
  broker (§5), not the per-session fan-out. This is the existing model, unchanged.

**Model-dependent-filter re-register (convergence hazard, (ref)
`unit-architecture.md:182-197`).** A subscription whose `Query` depends on a Model field
(`where owner = model.currentUser`) must **re-register** (unwatch + re-watch with the new
footprint) when that field changes, else it watches a stale predicate. `liveInto`/
`watchCollection` re-evaluate the binding's `reactiveQueries(model)` on Model change
((ref) `live_reactive.go:56-78`) and re-register when the resolved plan differs. §11 flags
this as a correctness obligation the builder must enforce by default.

---

## 9. Phasing — three independently verifiable sub-milestones

### Phase 4a — delta-match engine + registry (Go, commit-path)

- **Build:** the `reactiveRegistry` on `EmbeddedBackend`; the footprint derivation
  (`classifyIndexable` reuse → `subscription.footprint`); the commit-path dispatcher at the
  two post-`Apply` sites; the transition matcher (§2.2, `coordHit` New/Old + residual +
  witness tier); the setup-race Register/baseline/backfill/advance (§7); implement
  `EmbeddedBackend.Watch` (replace `ErrReactiveSeamPhase4`, `embedded.go:403`).
- **Gate (Go tests, `-race`):** insert→Enter; update-in-range→Stay; update-into-range→Enter;
  **update-out-of-range→Leave**; **delete→Leave (the delete-re-run gate)**; residual
  excludes an in-range-but-predicate-failing row; non-indexable predicate → conservative
  re-run fires, never misses; **setup-race: a commit landing during Watch setup is delivered
  exactly once** (no miss, no dup); committer never blocks under a wedged subscriber
  (overflow→resync); a closed subscription releases its watermark token (GC floor advances).
- **Independently verifiable:** pure Go, no Sky surface — provable with `go test`.

### Phase 4b — Sky surface + Sky.Live integration

- **Build:** `Std.Persist` `watch`/`changes`/`unwatch` + the typed `Change a`/`ChangeOp`;
  the `Live.watchCollection`/`liveInto`/`withReactive` builders (re-home `(ref)`
  `Persist.sky:947-1089` + `live_reactive.go`); wire `condPlan` → footprint; re-home the
  SSE-frame/panic-rollback tail.
- **Gate:** an example app (a live list); the **2-browser live demo**
  (clean-slate-architecture.md README:179-204) — two browsers, one tenant, a write in one
  reflects in the other; delete an on-screen row → it disappears in both.
- **Independently verifiable:** the 2-browser demo + a Playwright script.

### Phase 4c — capability check + cross-instance + realistic-N bench

- **Build:** the boot HARD-FATAL capability check (§6.1) + the compiler WARN + the
  `sky doctor`/deploy preflight; the cross-instance tenant-topic broadcast + local
  match (§5); the realistic-N fan-out benchmark harness.
- **Gate:** a **multi-replica sqlite/pg app with reactive bindings FATALS at boot** (every
  matrix ❌ cell, §6.1) — and the embedded/single-instance cells DON'T; a **2-replica
  cross-instance demo** (tenant-A write on replica 1 reaches a tenant-A session on
  replica 2, tenant-B unaffected); the **realistic-N bench** (§4.5) — hundreds of subs on
  one shared query, proving match-detection is N-independent and characterizing the O(N)
  delivery floor — OR the explicit Phase-6 scope decision.
- **Independently verifiable:** the boot-fatal test + the 2-replica demo + the bench numbers.

---

## 10. Grill attacks pre-empted

- **A1 — "Deletes silently drop" (the classic reactive bug).** Closed structurally: every
  delete/update logs its vacated coordinate in `KeyChange.OldIndex` (from `ensurePreimage`,
  `txn.go:234-244`), and Leave fires on the `OldIndex` `coordHit` (§2.3 proof) with the
  `resultPks`-membership backstop for the conservative/cross-instance path (§2.3
  belt-and-braces). A Leave never depends on decoding a body. **Both the coord path and the
  resultPks path independently catch a leaving row.**
- **A2 — "Index-coord matching isn't sound for arbitrary `Cond`."** Correct — and we don't
  claim it is. Precise deltas are bounded to `classifyIndexable`'s v1 envelope
  (single-column-ascending-indexed range/equality, `cond.go:251-259`); **everything else
  degrades to a conservative witness + re-run** (§3.3), over-notify never under-notify,
  identical to the SSI layer's own `WitnessCollection` fallback (`validate.go:41-49`). The
  boundary is stated, the cost is stated, the fallback is proven safe by the same argument
  that proves SSI's fallback safe.
- **A3 — "Fan-out amplification is rigged by an N=2 gate."** §4.5 states the O(writes)
  (re-evaluation) vs O(N) (delivery) distinction plainly and makes the 4c gate REQUIRE
  either a realistic-N (hundreds/tenant) bench or an explicit Phase-6 scope decision.
  Match-detection is bounded to O(changes × distinct-predicates) via collection
  partitioning + shared-predicate coalescing; delivery O(N) is the honest LiveView floor,
  not hidden.
- **A4 — "Some backend × deployment cell silently goes stale."** §6.1's matrix enumerates
  every cell; every non-reactive cell is a **boot HARD-FATAL**, never a silent degrade;
  single-instance `watch` works everywhere (`InProcessReactive` always true). R10 (external
  SQL writers) is documented as a known scope line with the NOTIFY-bridge answer.
- **A5 — "Setup race: a change during Watch setup is missed or double-counted."** §7:
  Register pins `readTs=durableHi` + token; baseline query at `readTs`; backfill
  `Changelog.Tail(readTs)`; live with `commitTs > lastTs` dedup. `readTs` is a clean cut
  with no gap; the ring/Tail spill fallback (fail-CLOSED, `committer.go:245-250`) guarantees
  the > readTs half is never lost.
- **A6 — "Subscriber sees a change before it's durable."** §7(1): fan-out dispatches only
  from the post-`Apply(Sync)` sites; a sealed engine fires nothing.
- **A7 — "Reactivity fights the per-session mutex / multi-tab model."** §8: a Change is a
  Model fold under `sess.mu`, serialized with clicks (last-writer-wins); the frame uses the
  existing multi-tab fan-out unchanged; panic-rollback restores handlers.
- **A8 — "A long-lived tab pins the GC floor forever → version bloat."** §7(5): the
  subscription advances its watermark token to each consumed `commitTs`
  (`engine.Readers().Advance`, `engine.go:171`), so the floor tracks the slowest *live*
  subscription's consumed position (R3), not its start; a closed tab releases the token
  entirely (§3.2).

---

## 11. Open questions / weakest points (what I'm least sure survives a grill)

1. **Order-only churn on a non-predicate index column.** A subscription ordered by column
   `C` but filtered on column `S`: a `Put` that changes only `C` (row stays in the `S`
   range) produces a `NewIndex`/`OldIndex` hit on the **`S` index** (Stay) but the row's
   **sort position** changed. Unless the footprint also witnesses the order column, the
   subscriber's *ordered* list is stale (right rows, wrong order). Mitigation options: (a)
   on any Stay, re-sort the maintained list (cheap for small N); (b) also record an
   `orderWitness` on order columns. **Leaning to (a) for `liveInto` (it owns the list) —
   but this needs to be explicit and tested, and it's the most likely correctness gap a
   griller finds.**
2. **Delivery O(N) is irreducible.** §4.5 is honest that match-detection is N-independent
   but delivery to N sessions is O(N). Shared-predicate eval reduces *detection* cost, not
   *delivery* cost. If a griller demands sub-O(N) delivery for a 10k-session tenant, the
   only answer is Phase-6 keyed-render + horizontal spread — i.e. **the honest answer is a
   scope deferral, and the bench must show the O(N) constant is small enough to be
   acceptable up to a stated N.** This is a real ceiling, not a bug, but it will be
   attacked.
3. **Leave carries no row body.** A delete's `Record=nil` (`keychange.go:27`), so a `Leave`
   delivers only the pk (`row = Nothing`, §6.2). A subscriber that wants to render "X was
   removed" with X's fields can't get them from the change — it must have cached the row (it
   usually has, since the row was on-screen). Documented limitation; a griller may want an
   opt-in "carry the tombstoned body" mode (costs a wider changelog payload).
4. **Cross-instance recomputes the delta N times + the conservative broadcast is coarse.**
   §5: each receiving instance re-matches. Fine for the precise tier, but a **collection
   witness** (non-indexable predicate) broadcast forces *every* tenant instance to re-query
   — the O(writes)-per-tenant claim degrades to O(writes × instances × query-cost) for
   conservative-tier subscriptions on a multi-replica deployment. Honest, but the bench (4c)
   must include a conservative-tier cross-instance case, not just the precise tier.
5. **The `classifyIndexable` v1 envelope is narrow (single-column ascending).** Composite
   and OR predicates — common in real list views (`status IN (…) AND tenant = …`) — fall to
   the conservative tier today (`cond.go:281-318` handles only a two-bound AND on ONE
   column). Correct (re-run), but a busy multi-tenant collection with many OR-predicate
   subscriptions re-runs a lot. Widening the precise envelope (composite index footprints)
   is real work; v1 honesty is "OR/composite → re-run." A griller may argue the *common*
   case isn't in the precise tier, undercutting the O(writes) headline for realistic queries.
6. **R6 verified-identity dependency is a hard external prerequisite.** §3.4: tenant
   scoping is inert without framework-verified `SessionIdentity` on the standard `Std.Auth`
   login path. If `Live.withIdentify` doesn't populate it by default (R6 open,
   clean-slate-architecture.md), the whole tenant-scoped fan-out fail-closes to empty rows —
   a silent "reactivity does nothing" for exactly the SaaS the magic targets. **This is a
   Phase-4 blocker that lives outside Phase 4** — it must be confirmed, not assumed.
7. **Model-dependent-filter re-register races.** §8: re-registering a subscription when a
   Model field changes its predicate is a correctness obligation with a race window (old
   sub torn down, new sub's baseline query + backfill running) during which a change could
   be missed if the re-register isn't itself boundary-disciplined (§7). Needs the same
   Register-pin-then-backfill discipline on every re-register, and that's easy to get subtly
   wrong. Likely the second-most-likely correctness gap after #1.
