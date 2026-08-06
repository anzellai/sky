# BlueDB / Std.Persist — Phase 4: query-scoped reactivity (L4) — v2 (grill-folded)

> **Status:** design, `feat/bluedb` @ `aba0611a`. Design only — no code specified for
> merge. This is the **v2** rewrite that folds every finding from
> `docs/bluedb/phase4-grill-findings.md` (A#1, A#2, B#1, B#2, B#3, NB-1, NB-2, NB-3).
> It is written to be **re-grilled** by ≥2 adversarial reviewers before any Phase-4
> code is written. §12 maps each finding → how it is now closed; §11 is the honest
> remaining-weakest-points list to attack.
>
> **The v1 → v2 re-architecture in one line.** v1 put the fan-out on the commit-path
> `bluedb`-layer goroutine, which had **no verified session identity** (→ cross-tenant
> leak, B#1/B#2) and **no legal path to the rt `Broker`** (`bluedb` can't import `rt` —
> B#3). v2 keeps the delta-match + subscription registry in `bluedb` but moves
> **identity resolution + tenant-scoped publish to the rt/session layer**, bridged by a
> `bluedb` **changefeed** (`DB.Subscribe`) that `rt` drains (the legal
> `rt → bluedb` direction), with the tenant carried as a **write-time-verified tag**
> stamped by the writing session on its identity-stamped goroutine — never re-derived on
> the pump goroutine, never read from record data. This is the ref's proven architecture
> (`(ref) changefeed.go` + `(ref) bluedb_reactive.go`), ported onto the clean-slate
> engine's `OldIndex`-precise substrate.
>
> **Citation convention.** Paths under `runtime-go/bluedb/…`, `runtime-go/rt/…`,
> `sky-stdlib/…`, `docs/…` are in **this** repo (`feat/bluedb` @ `aba0611a`) unless
> prefixed **`(ref)`**, which cites the read-only prior-art worktree
> `.claude/worktrees/ref-exp-bluedb/` (the proven `exp/bluedb` reactive foundation we
> port FROM). Line numbers marked **✅re-read** were verified against HEAD while writing
> v2; the rest are inherited from v1's grilled citation set (same tree, undisputed).

---

## 0. TL;DR — the one-paragraph thesis

**Query-scoped reactivity is the exact dual of the SSI commit-time validation Phase 2/3
already ships.** A serializable transaction records a *read-set footprint*
(`ReadSet{points, ranges, collWitness, indexWitness}` — `engine.go:141-146` ✅re-read)
and the committer asks, over the `(readTs, commitTs]` window, *"did any committed
`KeyChange` fall into my footprint → **abort**?"* (`validate.go:27-53`). A
**subscription is the same footprint**, derived from the same resolved `Cond` by the
same classifier (`classifyIndexable`, `embedded.go:374` ✅re-read), and the commit path
asks the same question with the opposite consequence: *"did any committed `KeyChange`
fall into my footprint → **notify**?"* The membership transition (row **enters**,
**leaves**, or **stays**) falls out of the `KeyChange.NewIndex`/`OldIndex` coordinate
pair the substrate already computes and durably logs for every committed change
(`keychange.go:23-30` ✅re-read) — and because a delete/update-out carries its vacated
positions in `OldIndex` (from the pre-image, now emitted on **both** the transactional
path (`ensurePreimage`) **and the autocommit blind path** (`blindPut`,
`embedded.go:164-184` ✅re-read — the Phase-2 under-reject fix landed at `aba0611a`)),
**the classic "deletes silently drop" reactive bug is structurally impossible.** v2
promotes this into the commit path via a **`bluedb` changefeed** (`DB.Subscribe`, ported
from `(ref) changefeed.go:52`) that an **rt-layer pump** drains; the pump does the
**tenant-scoped publish** for cross-instance and hands each delta to the **`bluedb`
subscription registry** for local precise fan-out. The tenant is a **write-time-verified
tag** (§3.4), so the fan-out is scoped to the entitled tenant with an **enforced
fail-closed gate** (§4.5) — the reactive analogue of the v0.16.6 SQL-`WHERE` gate.
Cross-instance re-uses the existing verified-tenant broker (§5); the capability matrix
(§6) is corrected so **every cell is proven or boots HARD-FATAL** — an unshared local
store multi-replica is impossible and boots fatal, not a silent stale.

---

## 1. What Phase 4 consumes (the substrate is already built)

Phase 4 writes **no new durable engine format** (the Phase-1 comparer commitment
holds). The one **transient, non-persisted** field it adds (`CommitReq.Tenant`, §3.4) is
never serialized to the changelog. This section pins the exact substrate seams.

### 1.1 The changelog already carries per-row transitions

`KeyChange` (`keychange.go:23-30` ✅re-read) is one committed row-level change:

```go
type KeyChange struct {
	Coll     CollID       // owning collection (per-change stamped)
	Pk       []byte       // the user-key — point-read / row-scoped match
	Op       Op           // OpPut | OpDelete
	Record   []byte       // put: row bytes (L4 body); delete: nil. Validation ignores it.
	NewIndex []IndexCoord // positions the row NOW occupies (put); nil for delete
	OldIndex []IndexCoord // positions the row VACATED (update/delete); nil for insert
}
```

The `NewIndex`/`OldIndex` pair is the reactive membership signal. **It is now populated
for the delete case AND the autocommit blind-update case** — the fix landed at
`aba0611a`:

- `Txn.Put`/`Txn.Delete` (transactional path) set `oldIndex` from the pre-image via
  `ensurePreimage`.
- `blindPut` (`embedded.go:164-184` ✅re-read, the autocommit upsert) now reads the
  pre-image Snapshot and emits `OldIndex` coords when the collection has indexes — the
  same short-circuit `buildIndexer` uses (`if len(cs.Indexes) > 0`). **This is the fix
  the grill's "⚠️ EXPOSED" section demanded**, and it is what makes the precise-tier
  `Leave` fire on the blind path (A#1). `blindDelete` (`embedded.go:186-209` ✅re-read)
  emits `OldIndex` from the pre-image for outright deletes.

### 1.2 The commit path already exposes exactly two durable-emit points

The single committer (`committer.go:33-51` ✅re-read) group-commits and, **after**
`Apply(pebble.Sync)` returns durable, appends each commit's decoded changes to the
in-RAM recent ring at **two sites**:

- **Blind path** — `processBlindPhase1`, `committer.go:139-150` ✅re-read (after
  `advanceDurableHi`, decode `DecodeChangelogPayload`, `e.recent.append`).
- **Transactional path** — `processTxn`, `committer.go:304-310` ✅re-read (after
  `advanceDurableHi`, iterate `applied[]`, `e.recent.append(a.commitTs, a.changes)`).

These two post-`Apply` sites are the Phase-4 **changefeed emit hook** — durable-before-
notify by construction (§7). The changes are **already decoded there** for the ring
append (`a.changes` on the txn path; `chg`/`DecodeChangelogPayload` on the blind path),
so the changefeed reuses that decode, not a second one.

### 1.3 The validation-window machinery IS the setup-race machinery

`recent.after(readTs)` (`committer.go:230` ✅re-read, `recent_changes.go`) returns every
`KeyChange` with `commitTs > readTs` in O(commits-since-readTs), with a `spilled=true`
fallback to the durable `changelogTailChanges` → `Changelog.Tail` (`committer.go:245-252`
✅re-read, `changelog.go`) when a reader lags below the ring floor — and that fallback is
**fail-CLOSED** on a read error (`committer.go:247` aborts rather than validating against
a nil window). This is exactly the "subscribe pins a `readTs`, backfill the gap, then go
live" discipline Phase 4 needs (§7). The `WatermarkRegistry` (`engine.go:166-175`
✅re-read) pins a reader token so GC never drops a version/changelog entry a live
subscription still needs, and `Advance(tok, readTs)` (`engine.go:171` ✅re-read) lets a
subscription move its floor forward as it consumes.

### 1.4 The predicate evaluator + range classifier already exist

- `bluedbEvalCond(cols, *CondNode) bool` (`cond.go`, used at `embedded.go:311,386`
  ✅re-read) — the row predicate; the residual filter Phase 4 applies to a `NewIndex` hit
  to confirm true membership.
- `classifyIndexable(*CollSchema, *CondNode) (indexHit, bool)` (`embedded.go:374`
  ✅re-read, `cond.go`) — decides whether a `Cond` is a clean single-column
  range/equality on a declared range-optimized index (→ a precise `[lo,hi]` footprint) or
  falls to the conservative witness. **The exact function that decides a subscription's
  footprint tier** (§3).
- `encodeScanRange`/`encodeIndexKey` (`index_key.go`) — the ONE canonical
  order-preserving encoder both the scan bound AND the change coord go through, so a
  subscription's range bound and a `KeyChange` coord byte-match by construction.
- `validate`/`coordHit` (`validate.go:27-71`) — **the matcher to generalize** (§2).

### 1.5 The Go seams Phase 4 fills

`EmbeddedBackend` (`embedded.go:23-35` ✅re-read) declares
`_ CrossInstanceReactive = (*EmbeddedBackend)(nil)` and `Capabilities()` returns
`InProcessReactive: true, CrossInstanceReactive: true` (`embedded.go:405-413` ✅re-read —
**the `CrossInstanceReactive: true` cell is corrected in §6**).
`Watch(CollSchema, QueryPlan) (Subscription, error)` currently returns
`ErrReactiveSeamPhase4` (`embedded.go:422-424` ✅re-read). The `Subscription`/`Change`
shapes (`backend.go:92-105` ✅re-read) are the frozen seam; §6.2 extends `Change` with
the transition tag. **`CommitReq` (`engine.go:107-122` ✅re-read) gains one transient
`Tenant string` field** (§3.4) — not part of `ChangelogPayload`, never durably written.
**`DB.Subscribe`/`emitChanges` (the changefeed) is NEW code on the engine**, ported from
`(ref) changefeed.go` and wired at the two §1.2 sites.

---

## 2. Delta-match: the SSI-validation dual (the core — unchanged from v1, hardened)

### 2.1 The symmetry

| | SSI validation (shipped) | Reactive delta-match (Phase 4) |
|---|---|---|
| Footprint | `ReadSet{points,ranges,collWitness,indexWitness}` (`engine.go:141-146`) | The **same** struct, derived once from a subscription's resolved `Cond` (§3) |
| Window | `recent.after(readTs)` (`committer.go:230`) | The **one** just-durable commit's `[]KeyChange` (§1.2) |
| Question | Did any change hit the footprint? (`validate.go:27`) | Which subscriptions' footprints does this change hit? |
| On hit | **Abort** the committing txn (`committer.go:257-266`) | **Notify** the subscription (§4) |
| Fallback | `collWitness`/`indexWitness` → over-reject (`validate.go:41-49`) | Same witnesses → over-**notify** (§3.3) |

Validation ORs `NewIndex` and `OldIndex` (`validate.go:47`) because it only cares
*whether* a conflict exists. Reactivity must **distinguish which side hit**:

```
enteredRange := coordHit(subFootprint, ch.NewIndex)   // row now occupies a watched coord
leftRange    := coordHit(subFootprint, ch.OldIndex)   // row vacated a watched coord
wasDisplayed := sub.resultPks[string(ch.Pk)]          // A#1 belt: is this pk currently shown?
```

### 2.2 Membership transition — the full truth table (with the A#1 belt clause)

For a subscription with an **indexable footprint** and a committed `KeyChange ch`:

| `ch.Op` | `enteredRange` | `leftRange` | `wasDisplayed` | Residual `bluedbEvalCond` | Transition |
|---|---|---|---|---|---|
| Put (insert) | true | false | false | matches | **Enter** |
| Put (insert) | true | false | false | **no match** | **none** (residual excludes) |
| Put (update) | true | true | true | matches | **Stay** — **re-sort if an order-column coord differs** (§2.5, A#1) |
| Put (update-in) | true | false | false | matches | **Enter** (moved INTO range) |
| Put (update-out) | false | true | true | — | **Leave** |
| Put (update) | false | false | **true** | — | **Leave** (A#1 belt — displayed pk no longer hits the footprint) |
| Delete | false | true | true/false | — (`Record=nil`) | **Leave** |
| any | false | false | false | — | **none** |

The residual re-eval is applied **only on a `NewIndex` hit** (`ch.Record` present). A
`Leave` never re-evals — it fires purely on the `OldIndex` hit **or** the `wasDisplayed`
belt clause. This is why Leave cannot be lost.

### 2.3 Delete/leave correctness — the proof (three independent legs)

**Claim.** A row leaving a query's result set ALWAYS fires a `Leave`, for an outright
delete AND an update that moves the row out of range — on the blind path AND the
transactional path.

**Leg 1 — `OldIndex` coord (transactional + blind, post-`aba0611a`).** A delete/update
carries its vacated coordinate in `OldIndex` (from the pre-image: `ensurePreimage` on the
txn path; the `blindPut`/`blindDelete` Snapshot read on the autocommit path,
`embedded.go:164-209` ✅re-read). `leftRange = coordHit(sub, ch.OldIndex) = true` →
**Leave**. **This leg was BROKEN on the blind path in v1** (`blindPut` emitted no
`OldIndex`) — the grill's ⚠️EXPOSED finding. It is fixed at `aba0611a` and re-proven
here.

**Leg 2 — `resultPks` membership belt (A#1).** Independent of `OldIndex`: the precise
matcher also checks `wasDisplayed := sub.resultPks[pk]`. Any change to a displayed pk that
does NOT re-enter the range → **Leave** (truth-table row 6). Ported from
`(ref) bluedb_reactive.go:129-131` (`if s.resultPks[rc.Pk] { return true }`). So even if
`OldIndex` were ever absent (an index-less collection, or a future coord-encoding gap),
a leaving displayed row still fires.

**Leg 3 — conservative witness (§3.3).** A non-indexable predicate marks the sub dirty
and re-queries; the diff against `resultPks` derives the Leave. Over-notify, never under.

Three legs, each sufficient alone. The 4a `-race` gate exercises all three
(delete→Leave, blind-update-out→Leave via `OldIndex`, and a displayed-pk update that
misses the range→Leave via the belt).

### 2.4 Why coords AND `resultPks` (both ship)

The coord path proves Leave from the durable log (body-free, structural); the `resultPks`
path proves it from the tracked result set. v2 uses **coords as the primary embedded
mechanism** and **`resultPks` as the belt + the sole cross-instance mechanism** (§5, where
the receiving instance has no coords). Both ship.

### 2.5 Order-only churn — the maintained-list staleness proof (A#1)

**The hazard.** A subscription filtered on column `S`, ordered by column `C ≠ S`. A `Put`
changing only `C` (row stays in the `S` range) hits the same `S` coord in `NewIndex` and
`OldIndex` → classified **Stay** by the filter footprint alone. But the row's sort
position changed → an *ordered* maintained list would be stale (right rows, wrong order).

**The fix (two mechanisms, belt-and-braces):**

1. **Order-column witness (precise tier).** When a subscription has `Orders` on
   column(s) not already in its filter footprint, and those columns are **declared
   indexes**, v2 adds them to the footprint as an `orderWitness` (an extra `ranges`/index
   entry spanning the whole index, so any coord on the order index is "in"). On a `Stay`,
   the matcher compares the order-column coord in `NewIndex` vs `OldIndex`: **if they
   differ → the Stay is order-affecting → the delivered `Change` carries `Transition =
   ChangeStay` with an `orderChanged` flag**. `buildIndexer(cs)` already emits coords for
   **every** declared index on the collection (`embedded.go:83-89` ✅re-read), so the
   order-column coord is present in the change whenever the order column is a declared
   index — no new engine work.

2. **Key-by-pk re-sort (maintained-list tier — the general belt).** `liveInto` owns its
   maintained list and keys it **by pk**. On ANY delivered `Change` for a displayed pk
   (Enter/Stay/Leave), the maintainer (a) replaces the row at that pk (never inserts a
   duplicate — the pk key is the anti-dup invariant), then (b) **re-sorts the list by the
   query's `Orders`** from the maintained rows themselves (no re-query — the rows are in
   hand). Cost is O(k log k) for the bounded `k` a live list shows.

**Staleness/dup proof.** *No stale order:* every `Change` to a displayed pk triggers a
re-sort of the full maintained list against `Orders`; an order-only `Put` is a `Change`
for a displayed pk (it hits the filter footprint as a Stay AND the pk is in `resultPks`),
so it triggers the re-sort. *No duplicate:* the maintained list is a pk-keyed map rendered
in sorted order; re-inserting a pk replaces, and a `Leave` removes the pk. A row can
therefore never appear twice, and its position is always the re-sorted position. ∎

For the **explicit `watchCollection`/`Change`-as-Msg** tier (§6.2) the app owns list
maintenance; the `Change` carries `Transition` + `orderChanged` so the app's fold can
re-sort. The 4a gate includes an order-only churn case (Stay with `orderChanged=true` on
an ordered maintained list; assert the emitted list order matches a fresh baseline query).

---

## 3. Subscription model

### 3.1 What a subscription IS

A subscription is a **footprint + a delivery channel + a scope key**, held in the
**`bluedb` registry** (§4.1 — `bluedb` owns the match; the delivery channel is a plain Go
channel, so `bluedb` never imports `rt`):

```go
// in the bluedb reactiveRegistry (§4.1). rt constructs one via Backend.Watch.
type subscription struct {
	id           subID
	coll         CollID
	tenant       string          // the OPAQUE scope key rt passes in (§3.4); "" = process-global bucket
	footprint    *ReadSet        // the SAME struct SSI uses (points/ranges/collWitness/indexWitness)
	orderWitness []IndexID       // declared order-column indexes (A#1, §2.5)
	plan         QueryPlan       // for the residual bluedbEvalCond + conservative re-run
	resultPks    map[string]bool // tracked result set (Leave belt + cross-instance, §2.3)
	lastTs       HLC             // highest applied commitTs — monotonic apply + dedup (§7)
	deliver      chan Change     // non-blocking; overflow → resync (§4.4). A plain channel: no rt type.
}
```

`bluedb` treats `tenant` as an opaque string it **matches** (delta tag vs sub tenant, §4)
— it never **resolves** it (that is rt's job, §3.4). This is the layering that dissolves
B#1/B#3: identity resolution lives where identity lives (rt), matching lives with the
engine (`bluedb`).

Three scope shapes, one footprint: **whole collection** (`collWitness`), **single row/PK**
(a `points` entry), **query predicate** (`classifyIndexable` → a `ranges` entry, else a
witness). The resolved `Cond` is the shared `Std.Db.Store` `Cond`/`Query` algebra lowered
to a `QueryPlan` exactly as a query is.

### 3.2 Lifecycle — bound to a Sky.Live session (rt)

Ported from `(ref) live_reactive.go`:

1. **Create on mount.** `startReactive(sess)` (`(ref) live_reactive.go:82-147`) reads the
   Model's reactive bindings, resolves the tenant per binding (§3.4), and for each calls
   `Backend.Watch(coll, plan)` — threading the returned `Subscription.Changes()` channel
   into the session loop (v1 subscribed a whole-collection broker topic; v2 gets a precise
   channel from the registry).
2. **Live.** `reactiveLoop` (`(ref) live_reactive.go:170-194`) selects on the channel;
   each `Change` is coalesced (§4.3) and folded into Model under `sess.mu` (§8). The loop
   goroutine is **identity-stamped** (`setGoroutineLiveSession(sess)`,
   `(ref) live_reactive.go:178`; `live_session_ctx.go:47` ✅re-read).
3. **Drop on session end.** `teardownReactive()` (`(ref) live_reactive.go:151-163`, from
   `markDone`) closes the subscription → unregisters it from the `bluedb` registry AND
   releases its `WatermarkRegistry` token (`engine.go:172` `Release`). Idempotent.

### 3.3 Non-indexable predicates — the conservative tier

`classifyIndexable` returns `ok=false` for OR/nested/NOT, a non-declared column, an
`IS NULL`, or a not-order-preserving column (`Real`/`Money`/`Blob`/`Codec.map`). The
footprint degrades to a witness (`indexWitness`/`collWitness`), mirroring
`Txn.ScanCollection` (`embedded.go:377` ✅re-read). **On a witness match Phase 4 marks the
subscription dirty and re-runs its query** (the self-healing nudge), coalesced (§4.3); the
re-run diffs the fresh result set against `resultPks` to derive Enter/Leave/Stay and
updates `resultPks`. **Over-notify, never under-notify** — a witness fires on changes that
may not affect the query (costing a re-run) but can never miss one.

**Precise-delta available iff** the query is a single-column range/equality on a declared
range-optimized ascending index (`IndexSpec` v1 scope is SINGLE-COLUMN ASCENDING,
`backend.go:146-155` ✅re-read). Everything else re-runs. **We do NOT over-claim precise
deltas for arbitrary `Cond`.**

### 3.4 Tenant scoping — the WRITE-TIME-VERIFIED tag (B#1/B#2 root fix)

The scope key is **who shares state** (the tenant), a security model, not a storage
mechanism. v2's central change: **the delta carries a tenant tag stamped by the WRITING
session on its identity-stamped goroutine, BEFORE the commit** — never re-derived on the
pump goroutine (which has no session), never read from record data (forgeable).

**Write side (stamp).** A Sky.Live `Persist` write runs on the session goroutine (update,
or a `Cmd.perform` task — both identity-stamped, `(ref) live_reactive.go:176` comment;
`live_session_ctx.go:34` ✅re-read `currentLiveSession`). The rt `Persist` write kernel
reads the **verified** tenant `SessionIdentity(currentLiveSession()).Claims["tenant"]`
(`(ref) bluedb_reactive.go:42-49` `reactiveTenantTopic`; the same verified claim
`tenantPrefixForSession` uses — forgery-safe, returns `ok=false` unless a gate stamped
`identityValid`) and threads it into the backend write as `CommitReq.Tenant`
(`engine.go:107` ✅re-read — the new transient field). `blindPut`/`txWrite`
(`embedded.go:164,215` ✅re-read) set `CommitReq.Tenant` from the value rt passed; the
committer copies it onto the **in-RAM changefeed event** at the §1.2 emit sites. A write
with no verified tenant (background job, CLI, unauth) stamps `""`.

> **Why a field, not a re-derive.** The pump goroutine (§4) is NOT a session goroutine —
> `currentLiveSession()` there is nil → a re-derive would fail-closed to `""` for EVERY
> delta (v1's B#1 leak: nil identity → unscoped shared topic). Carrying the tag as
> commit-time data means the trustworthy tenant travels WITH the delta to the pump. It is
> **not read from record columns** (a tenant column is app data an attacker could forge);
> it is the framework-verified claim of the goroutine that performed the write.

> **Why not a `bluedb` goroutine-local (rejected alt).** rt could stamp a `bluedb`-side
> goroutine-local the committer reads. Rejected: it duplicates the identity mechanism in
> two layers and is untestable without a live session. Threading `CommitReq.Tenant`
> explicitly is testable in the 4a two-tenant `-race` gate (NB-2) with a bare
> `Backend.Put(..., tenant)` call, no session harness.

**Match side (enforce, fail-closed — B#2).** The `bluedb` registry buckets subscriptions
by `(coll, tenant)`. The fan-out visits **only** `byCollTenant[(ch.Coll, ch.Tenant)]` —
where `ch.Tenant` is the delta's write-time tag. A subscription on tenant A is **never**
visited for a tenant-B (or `""`) delta, even under a coarse collection witness (the
witness is scoped inside the tenant bucket). A delta tagged `""` visits **only** the `""`
bucket (single-tenant/dev), **never the union of all tenants** — this is the enforced
reactive tenant gate: **absent verified identity ⇒ the subscription receives NOTHING from
other tenants, never the unscoped firehose.** It is the reactive analogue of the v0.16.6
SQL-`WHERE` gate: no verified tenant ⇒ scoped to the empty bucket, not the whole table.

**Cross-instance (§5)** encodes the tenant in the broker topic
`reactive:<tenant>:<coll>` (`(ref) bluedb_reactive.go:42-49`) — a per-tenant Redis
SUBSCRIBE — and a verified session subscribes ONLY to its own tenant's topic. Both sides
fail-closed.

**R6 dependency (unchanged, external).** The whole scheme is inert without a
framework-verified `SessionIdentity` on the **standard** `Std.Auth` login path. Phase 4
depends on `Live.withIdentify` (or equivalent) populating it there; §11 #6 flags this as a
hard external prerequisite, not a Phase-4 deliverable. **Fail-closed behaviour makes the
missing-identity case SAFE (empty, not leaky)** — it is a liveness gap ("reactivity does
nothing for authed multi-tenant"), not a confidentiality gap.

---

## 4. The changefeed + rt pump + fan-out (the re-architecture — B#1/B#2/B#3)

### 4.1 The `bluedb` changefeed (NEW; ported from `(ref) changefeed.go`)

The engine grows a change-feed exactly like the ref's (`(ref) changefeed.go:52-122`), but
carrying **decoded record-level `KeyChange`s** (the current tree already decodes them at
the ring-append sites) plus the write-time tenant tag:

```go
// on the engine. Ported from (ref) changefeed.go:52 (DB.Subscribe / emitChanges).
type ChangeBatch struct {
	CommitTs HLC
	Tenant   string        // the CommitReq.Tenant write-time tag (§3.4)
	Changes  []KeyChange   // already decoded at the §1.2 ring-append site
}
func (e *pebbleEngine) Subscribe(buf int) (*ChangeSub, func()) { … } // (ref) changefeed.go:52
func (e *pebbleEngine) hasSubs() bool { … }                          // (ref) changefeed.go:80 — skip when nobody listens
func (e *pebbleEngine) emitChanges(b ChangeBatch) { … }              // (ref) changefeed.go:108 — NON-BLOCKING; full buffer → overflow latch
```

**Emit is NON-BLOCKING** (`(ref) changefeed.go:112-122`: `select { case ch <- batch:
default: overflow=1 }`) at the two §1.2 post-`Apply` sites. **The committer never blocks
on a subscriber** (the R1 committer-never-stalls contract) — a slow drain drops its batch
and latches `overflow`, forcing subscriber resync (§4.4). `hasSubs()` short-circuits the
emit entirely in the common no-reactive process. The `bluedb` **subscription registry**
(§3.1) also lives here — a plain-channel structure with no rt types.

### 4.2 The rt pump (the ONE drain; ported from `(ref) bluedb_reactive.go:241`)

rt owns a single pump goroutine per engine that drains the changefeed and does the two
jobs `bluedb` legally cannot:

```go
// rt. Ported from (ref) bluedb_reactive.go:241 bluedbStartReactivePump.
func bluedbStartReactivePump(eng bluedb.Engine, reg *bluedb.ReactiveRegistry) func() {
	sub, cancel := eng.Subscribe(0)
	go func() {
		for batch := range sub.C {           // one committed group commit
			overflowed := sub.Overflowed()   // (ref) changefeed.go:41
			// (a) LOCAL precise fan-out — bluedb owns the match + registry (§3.1, §4.5).
			//     Tenant-gated by comparing batch.Tenant to each sub's tenant bucket.
			reg.DispatchLocal(batch)         // pushes precise Changes onto matched subs' channels
			// (b) CROSS-INSTANCE — rt owns the broker (bluedb cannot import rt). Publish a
			//     tenant-scoped NUDGE (Record=""), skip-origin, ONLY on a shared broker (§5).
			if crossInstanceTopology() {
				for _, ch := range batch.Changes {
					reactivePublishTenantNudge(batch.Tenant, ch) // reactive:<tenant>:<coll>, no body
				}
			}
			if overflowed { reg.MarkResyncAll(batch.Tenant) } // (ref) bluedb_reactive.go:261-265
		}
	}()
	return func() { cancel() }
}
```

- **The match is in `bluedb`** (`reg.DispatchLocal`) — it visits only
  `byCollTenant[(ch.Coll, batch.Tenant)]` (§4.5) and computes the §2.2 transition via
  `coordHit`(New/Old) + residual + the A#1 belt. It pushes precise `Change`s onto each
  matched sub's plain channel. **No rt import, no broker, no session pointer in `bluedb`.**
- **The publish is in `rt`** (`reactivePublishTenantNudge`) — the ONLY thing that touches
  the broker (`(ref) reactivePublishScoped`, but here the tenant comes from the delta tag,
  not `currentLiveSession()`, because the pump is not a session goroutine). **`Record` is
  always `""` on the broker** (§5, the ref invariant `(ref) bluedb_reactive.go:149-162`).
- **One drain goroutine** = one ordering point. No two goroutines race the changefeed.

**SQL backends have no engine changefeed** — sqlite/postgres reactivity is fed by the
**write-layer publish** (`(ref) bluedb_reactive.go:188 reactivePublish`, backend-agnostic),
which runs on the session goroutine and therefore uses `reactivePublishScoped`
(`(ref) bluedb_reactive.go:217`, tenant from the writer's verified identity). The engine
changefeed is the **embedded** precise mechanism; the write-layer publish is the **SQL**
mechanism; both converge on the same rt subscription/broker plumbing.

### 4.3 Coalescing

- **Burst coalescing:** `drainChangeBurst` (`(ref) live_reactive.go:198-209`) non-blockingly
  drains all queued changes before one re-render — a bulk write → ONE frame.
- **Precise-delta coalescing:** multiple Enter/Leave/Stay in one burst fold into the Model
  list in arrival order, then one render (+ one re-sort, §2.5).
- **Monotonic apply + dedup:** each sub drops `commitTs <= lastTs` (guards the setup-race
  overlap §7 and a conservative re-run racing a precise delta).

### 4.4 Back-pressure + overflow

- The changefeed emit is non-blocking (§4.1); a full engine→pump buffer latches
  `overflow` → the pump `MarkResyncAll` for the affected tenant → each affected sub
  re-queries on next drain (self-heals all misses).
- A full per-sub `deliver` channel latches the sub's own overflow → the sub re-queries.
- The SSE send stays non-blocking with drop→resync
  (`live.go` `recordSseDrop` + `markAllConnsOutOfSync`; `SKY_LIVE_SSE_BUFFER` inline
  resync per CLAUDE.md) — unchanged Sky.Live machinery.

### 4.5 The enforced reactive tenant gate (B#2) + the amplification bound

**The gate (fail-closed).** `reg.DispatchLocal(batch)` computes
`bucket := reg.byCollTenant[collTenantKey(ch.Coll, batch.Tenant)]` and matches ONLY within
`bucket`. There is **no code path** that iterates all tenants' subscriptions for one
change. A `batch.Tenant == ""` delta matches only the `""` bucket. This is the structural
gate: a leak would require a `bluedb` bug that reads the wrong bucket, which the 4a
two-tenant `-race` gate (NB-2) catches in the first sub-phase.

**The amplification bound (honest).** Within a `(coll, tenant)` bucket, naive match is
O(changes × subs). Bounds:
- **Collection + tenant partitioned:** a change visits only subs on its collection AND its
  tenant.
- **Shared-predicate coalescing:** identical resolved `(coll, cond)` plans in a tenant
  evaluate the predicate **once** and fan the single transition to all N sharing subs —
  match *detection* is **O(changes × distinct-predicates-per-(coll,tenant))**, N-independent.
- **Range-index bucketing (scaling lever, may defer as an OPTIMIZATION to Phase 6):** index
  the `ranges` footprints by `IndexID` in a sorted structure → O(log P + hits). 4a may do a
  linear walk over distinct predicates and be honest it is O(distinct-predicates); this is
  an optimization, not a proof deferral (§9, NB-1).

---

## 5. Cross-instance (multi-replica) — corrected + nudge-only (B#3)

**Precise delta-match is LOCAL to each instance** (the commit-path registry holds only its
own instance's subs). A tenant-A session may live on another instance. Cross-instance is
therefore **broadcast-a-nudge, re-query-locally** — the ref's proven model:

1. Instance A commits. Its rt pump (§4.2), **only when a shared broker is configured**
   (`crossInstanceTopology()`), publishes a **tenant-scoped NUDGE** (`op/coll/pk`,
   **`Record=""`**, `(ref) bluedb_reactive.go:149-162,203`) on `reactive:<tenant>:<coll>`
   with **skip-origin** (`publishNoEcho`/`SkipOrigin` — so instance A does NOT receive its
   own nudge; it already did the precise LOCAL fan-out). Redis drops the own-echo by
   `InstanceID` (`live_redis_broker.go:266`); skip-origin covers the in-process tier.
2. Every OTHER instance hosting a tenant-A session is SUBSCRIBED to `reactive:<tenant>:<coll>`
   (set at `startReactive`, `(ref) live_reactive.go:111-122`). It receives the nudge and
   runs a **conservative re-query** (`reactiveLoop` → `reactiveRefreshOnce`,
   `(ref) live_reactive.go:277-366`) against **its own view of the shared backend**, using
   `resultPks` membership (§2.3 Leg 2) to derive the transition — the nudge carries the pk,
   so a delete-of-an-unshown-row is skipped, but any other nudge on a watched collection
   re-queries.
3. Tenant scoping bounds *which* instances receive: only instances hosting a tenant-A
   session subscribe to `reactive:<tenant>:<coll>`. `Record=""` means **no body ever
   crosses the broker** — even the per-tenant topic carries only a nudge, so a broker
   misconfiguration or a future shared-topic reuse cannot leak a body. **This is stricter
   than v1**, which proposed carrying the full record on the topic (the B#1 body-leak
   vector). v2 keeps `Record` off the wire unconditionally.

**Why cross-instance is conservative (re-query), not precise.** The receiving instance has
no `OldIndex`/coords (nudge-only, by the body-safety invariant) → it cannot compute a coord
transition → it re-queries. Correct (over-notify never under), body-free, and it queries
the **shared** backend (which has the write). Precise cross-instance (carrying coords) is a
documented future optimization, gated behind the same verified-tenant topic — **NOT v1**,
because the grill flagged body-carry as the leak vector.

**The critical correction (B#3 — a green cell that was never proven).** Cross-instance
re-query is only sound when every instance queries the **same shared data**. **Embedded
BlueDB is a single-writer LOCAL pebble store** — N replicas each hold an INDEPENDENT store,
so instance B's re-query would NOT contain instance A's write. Therefore **embedded +
multi-replica cannot do cross-instance reactivity at all** (the data is not shared), and v1
asserting `CrossInstanceReactive=true` for that cell was the exact "boots green but the
bridge doesn't work" B#3 failure. **v2 makes embedded + multi-replica a boot HARD-FATAL**
(§6). The pump→broker→other-instances→re-query chain is proven ONLY for a **shared** backend
(Postgres) + a **shared** broker (Redis).

---

## 6. Capability check (runtime-loud) + the Sky surface — corrected matrix

### 6.1 The three-part safety net (NOT a compile-time gate)

Compile-time backend-capability gating is impossible by theorem (clean-slate Decision 5:
no type classes/HKT; the backend axis is a runtime property injected at boot). So safety
is runtime-loud:

1. **Compiler WARN (compile-visible).** Whether an app *uses* `watch`/`liveInto`/
   `withReactive` is a static fact → a build-time WARN "this app requires a
   reactive-capable backend" (emitted from the Rust HIR pass on the
   `Persist_watch`/`withReactive` reference).
2. **Runtime HARD-FATAL boot check.** At startup the runtime probes
   `Backend.Capabilities()` (`backend.go:107-115` ✅re-read) and matches it against the
   declared reactive requirement AND the replica topology:
   - `InProcessReactive` is **always true** (`embedded.go:407` ✅re-read; every backend has
     in-process pub/sub) → **single-instance `watch` never fails** on any backend (~99% of
     apps).
   - **Multi-replica AND reactive bindings AND the store is NOT a shared store** (embedded
     local pebble, or sqlite local file) → **HARD FATAL**: *"reactive `watch` across N
     replicas needs a SHARED backend — embedded/sqlite are single-writer local stores.
     Run single-instance, or use Postgres + a Redis broker."*
   - **Multi-replica AND reactive bindings AND store=postgres AND no shared broker
     (`SKY_LIVE_BROKER`/Redis absent)** → **HARD FATAL**: *"reactive across replicas needs a
     Redis broker to carry change nudges — set SKY_LIVE_BROKER_URL, or run single-instance."*
   - **NEVER a silent stale read.**
3. **CI / deploy preflight.** `sky doctor` + the SkyDeploy preflight boot with the *target*
   config + replica count and assert capabilities BEFORE production traffic.

### 6.2 The corrected backend × deployment matrix (every cell PROVEN or FATAL)

| Backend | Single-instance | Multi-replica |
|---|---|---|
| **embedded (BlueDB, local pebble)** | ✅ commit-path changefeed → precise local fan-out (multi-tenant safe via §3.4) | ❌ **HARD FATAL** — single-writer local store; data not shared across replicas (B#3 correction — was a false ✅ in v1) |
| **sqlite (local file)** | ✅ write-layer publish → in-process re-query | ❌ **HARD FATAL** — local file, not shareable |
| **postgres (shared)** | ✅ write-layer publish → in-process re-query | ✅ **iff Redis broker** (pump/publish → Redis nudge → each instance re-queries shared PG); ❌ **HARD FATAL** without a shared broker |

There is **no cell that silently degrades**: every ❌ is a boot fatal, every ✅ is wired
end-to-end. The v1 matrix's `embedded multi-replica = ✅` cell is corrected to ❌ FATAL —
it was the "asserted, not proven" cell B#3 attacked. **R10** (external SQL writers bypassing
the commit path — a write to Postgres NOT via a Sky instance fires no publish) is documented
as a known scope line with the LISTEN/NOTIFY-bridge answer, surfaced not hidden.

### 6.3 The Sky surface

**Raw tier** — `Std.Persist`:

```elm
type Subscription a
type ChangeOp = Enter | Leave | Update
type alias Change a =
    { op          : ChangeOp     -- membership transition (§2.2)
    , key         : String       -- primary key of the affected row
    , row         : Maybe a      -- Just for Enter/Update; Nothing for Leave (delete carries no body)
    , orderChanged : Bool        -- (A#1) a Stay whose sort-key column moved — re-sort
    }
watch   : Conn cap -> Collection a -> Query a -> Task Error (Subscription a)
changes : Subscription a -> Sub (Change a)
unwatch : Subscription a -> Task Error ()
```

The Go `Change` (`backend.go:99-105` ✅re-read) gains `Transition
(ChangeEnter|ChangeStay|ChangeLeave)` + an `OrderChanged bool`, computed in the commit path
(§2.2/§2.5). `row = Nothing` on Leave is load-bearing (`Record=nil`, `keychange.go:27`).

**Easy tier (magic-first)** — Sky.Live builder integration (v0.19 `config |> withX`):

```elm
Live.watchCollection : Collection a -> Query a -> (Change a -> msg) -> AppConfig msg -> AppConfig msg
Live.liveInto        : Collection a -> Query a -> (model -> List a) -> (List a -> model -> model)
                    -> AppConfig msg -> AppConfig msg
```

`liveInto` re-homes the `(ref) Persist.liveInto`/`liveNamed` builders (which already carry
the `condPlan` the ref runtime ignored — `(ref) live_reactive.go:10-12` "v1 refreshes at
COLLECTION scope"). **Phase 4 wires `condPlan` → the subscription footprint** (that
promotion IS L4) AND owns the pk-keyed maintained list + re-sort (§2.5). `Live.autoBlueDB`
whole-Model-is-a-collection is Phase 5's DX ceiling; the Phase-4 engine makes it live.

---

## 7. Ordering & consistency — "subscribe before you snapshot" (A#2)

Two failure modes: (1) never see a change before it is durable; (2) never miss a change
committed during subscription setup.

**(1) Durable-before-notify.** The changefeed emits **only from the two post-`Apply(Sync)`
sites** (`committer.go:139-150`, `:304-310` ✅re-read) — after `advanceDurableHi`. A
subscriber cannot observe a non-durable change; a sealed engine (durability fault) fires
nothing (`committer.go:301-302` ✅re-read).

**(2) No-miss setup race — REGISTER-LIVE-FIRST (the A#2 reorder).** v1 registered into the
live registry AFTER backfill, leaving a window where a commit between the `Tail(readTs)`
scan and registration was in neither. v2 registers live FIRST:

1. **Register-live (start buffering).** Register the sub into the `bluedb` registry
   `byCollTenant` **with an empty `resultPks`** and a `buffering=true` flag. From this
   instant, the fan-out captures every matching delta into the sub's `deliver` buffer (it
   does not yet compute precise transitions — it just buffers the raw `Change`s and their
   `commitTs`).
2. **Pin `readTs`.** `tok, readTs := engine.Readers().Register()` (`engine.go:169`
   ✅re-read — atomically `readTs = durableHi` + token, closing the TOCTOU). **`readTs` is
   pinned AFTER buffering starts**, so `readTs ≤ (any commitTs the buffer could still be
   missing)` — the buffer already covers `(buffer-start, ∞) ⊇ (readTs, ∞)`.
3. **Baseline.** Run the initial query at the `readTs` snapshot → seed `resultPks`. This
   sees every commit `≤ readTs`.
4. **Drain the buffer + go live.** Set `buffering=false`; process buffered + new deltas,
   dropping `commitTs ≤ readTs` (already in baseline) and setting `lastTs`. Dedup is by
   `lastTs`.
5. **Backfill ONLY on buffer overflow.** If the setup buffer overflowed (slow setup, high
   write rate), fall back to `changelogTailChanges(readTs)` (`committer.go:245-252`
   ✅re-read — the durable, fail-CLOSED spill path) to reconstruct the `(readTs, now]`
   window. In the common case the buffer is a superset and NO durable backfill is needed.
6. **Advance.** As the sub consumes, `engine.Readers().Advance(tok, lastTs)` (`engine.go:171`
   ✅re-read) moves its GC floor forward so a long-lived tab doesn't pin the floor forever.

**No-window proof.** Buffering starts at `t_reg`; `readTs` is pinned at `t_readTs > t_reg`.
Every commit with `commitTs ≤ readTs` is in the baseline. Every commit with
`commitTs > readTs` committed at wall-time `> t_reg` (since commitTs is monotonic in commit
order and `readTs = durableHi` at `t_readTs > t_reg`), so it is in the buffer (which
captures from `t_reg`). The only escape is a buffer overflow → the durable `Tail(readTs)`
fallback covers exactly `(readTs, now]`, fail-CLOSED. **Every commit is in the baseline OR
the buffer OR the durable backfill — never in none.** ∎ (v1 could drop a commit between the
`Tail` scan and a later registration; v2's register-first removes that gap.)

---

## 8. Interaction with the per-session mutex + multi-tab fan-out (unchanged)

- A matched `Change` becomes a **Model fold under `sess.mu`** (`(ref) live_reactive.go:284-334`)
  — serialized against user dispatches (last-writer-wins, the existing
  `runSubscriberDispatch` discipline). A reactive fold and a click never race.
- The frame goes to `sess.sseCh`; `fanOutFrame` fans it to all the session's SSE
  connections (non-blocking, drop→resync). Every tab converges to the same list. The
  multi-tab fan-out is unchanged Sky.Live machinery.
- **Panic-rollback ports verbatim** (`(ref) live_reactive.go:313-334`): a fold that panics
  restores `sess.model`, `sess.lastComputedBody`, AND `sess.handlers` (without the handler
  restore, `prevTree`'s handler IDs dangle → silent no-op clicks).
- **Two people browsing independently are two sessions** → two subscriptions with two
  tenant scopes; cross-session sync is the tenant-topic broker (§5), not the per-session
  fan-out.
- **Model-dependent-filter re-register (§11 #7).** A subscription whose `Query` depends on a
  Model field must re-register (unwatch + re-watch) when that field changes, using the SAME
  register-live-first discipline (§7) so the re-register has no miss window.

---

## 9. Phasing — three independently verifiable sub-milestones

### Phase 4a — changefeed + delta-match engine + registry (Go, commit-path)

- **Build:** the `bluedb` changefeed (`Subscribe`/`emitChanges`, `(ref) changefeed.go`) at
  the two §1.2 sites carrying `ChangeBatch{CommitTs, Tenant, Changes}`; the `CommitReq.Tenant`
  transient field + `blindPut`/`txWrite` stamp; the `bluedb` `reactiveRegistry` (byCollTenant)
  + `DispatchLocal`; the transition matcher (§2.2, `coordHit` New/Old + residual + the A#1
  belt + order-witness §2.5); the register-live-first setup (§7); implement `Watch` (replace
  `ErrReactiveSeamPhase4`, `embedded.go:422-424`).
- **Gate (Go `-race`):** insert→Enter; update-in-range→Stay; update-into-range→Enter;
  **blind update-out-of-range→Leave** (Leg 1, the `aba0611a` fix); **delete→Leave**;
  **displayed-pk update missing the range→Leave** (Leg 2 belt); **order-only churn on an
  ordered maintained list → re-sort, no stale order, no dup** (§2.5); residual excludes an
  in-range-but-predicate-failing row; non-indexable predicate → conservative re-run fires,
  never misses; **setup-race: a commit landing during Watch setup is delivered exactly once**
  (§7); committer never blocks under a wedged subscriber (overflow→resync); a closed
  subscription releases its watermark token.
- **★ NB-2 — TWO-TENANT COMMIT-PATH ISOLATION `-race` TEST (in 4a, not 4c):** register a
  tenant-A sub and a tenant-B sub on the SAME collection; a tenant-A-tagged write MUST
  deliver ONLY to the tenant-A sub, and a `""`-tagged write ONLY to a `""` sub — under
  `go test -race`, with concurrent writers on both tenants. This catches the B#1/B#2 leak
  class in the first sub-phase (v1 would have looked shippable through 4a+4b).
- **Independently verifiable:** pure Go, no Sky surface.

### Phase 4b — Sky surface + Sky.Live integration

- **Build:** `Std.Persist` `watch`/`changes`/`unwatch` + typed `Change a`; the rt pump
  (`(ref) bluedbStartReactivePump`) doing LOCAL `DispatchLocal` + (shared-broker-only)
  cross-instance nudge; the rt Persist write kernel stamping `CommitReq.Tenant` from the
  verified identity (§3.4); `Live.watchCollection`/`liveInto` (re-home
  `(ref) Persist.sky` + `live_reactive.go`); wire `condPlan` → footprint; the pk-keyed
  maintained list + re-sort (§2.5); re-home the SSE-frame/panic-rollback tail.
- **Gate:** a live-list example; the 2-browser live demo (one tenant, a write in one browser
  reflects in the other; delete an on-screen row → gone in both); **a single-instance
  two-tenant browser demo** — tenant-A's write is invisible to a tenant-B session on the same
  process (the multi-tenant-on-one-box case that §3.4 protects).
- **Independently verifiable:** the demos + Playwright.

### Phase 4c — capability check + cross-instance + realistic-N bench

- **Build:** the boot HARD-FATAL capability check (§6.1, incl. the corrected embedded/sqlite
  multi-replica FATAL + postgres-needs-broker FATAL) + the compiler WARN + `sky doctor`/deploy
  preflight; the cross-instance tenant-topic nudge + local re-query (§5) on Postgres+Redis;
  the realistic-N bench harness.
- **Gate:** every matrix ❌ cell FATALS at boot AND the ✅ cells DON'T; a **2-replica
  Postgres+Redis cross-instance demo** (tenant-A write on replica 1 reaches a tenant-A session
  on replica 2; tenant-B unaffected; **`Record` never on the wire** — assert the broker payload
  is nudge-only); **an embedded multi-replica config FATALS at boot** (B#3 correction, proven
  not asserted).
- **★ NB-1 — REALISTIC-N BENCH IS REQUIRED (no proof-deferral loophole):** a
  hundreds-of-sessions-per-tenant shared-feed bench measuring (i) per-commit match-detection
  cost — **must be provably N-independent** (~O(distinct-predicates)); (ii) per-commit fan-out
  delivery cost — **must be characterized AND BOUNDED linear up to a STATED N** (e.g. "≤ X
  ms/commit to N=1000 sessions/tenant"); (iii) memory per subscription. The bench **must
  close**; a Phase-6 *optimization* (keyed render + horizontal spread above the stated N) is
  allowed, a Phase-6 *proof deferral* is not (per the no-deferral rule). The old "OR an
  explicit Phase-6 scope decision" escape is REMOVED.
- **★ NB-3 — RESYNC THUNDERING-HERD BOUND:** the bench includes a high-write × high-N
  conservative-tier case that forces the resync path (buffer overflow → all affected subs
  re-query). The design BOUNDS the herd: (a) **per-sub coalescing** collapses a burst to ONE
  re-query regardless of how many deltas dropped; (b) a **global reactive re-query semaphore**
  (default `K = min(GOMAXPROCS, 8)`) caps concurrent re-queries so an N-way synchronized
  stampede runs at most K at a time (the rest queue) — the engine sees ≤ K concurrent scans,
  not N; (c) resync is per-sub debounced. The gate asserts concurrent re-query count stays ≤ K
  and total re-queries ≤ (affected subs) under a 10k-write burst on 500 subs. The bound is
  **stated and enforced**, not left unbounded.
- **Independently verifiable:** the boot-fatal tests + the 2-replica demo + the bench numbers.

---

## 10. Grill attacks pre-empted

- **A1 — "Deletes silently drop."** Closed by THREE independent legs (§2.3): `OldIndex`
  coord (now on the blind path too, `aba0611a`), `resultPks` membership belt, and the
  conservative witness. All three in the 4a `-race` gate.
- **A2 — "A change during Watch setup is missed."** Closed by register-live-FIRST (§7): buffer
  before pinning `readTs`; baseline; drain-with-dedup; durable `Tail` only on overflow. The
  no-window proof is airtight (v2 removes v1's register-after-backfill gap).
- **A3 — "Index-coord matching isn't sound for arbitrary `Cond`."** We don't claim it is.
  Precise deltas are bounded to `classifyIndexable`'s single-column-ascending envelope;
  everything else re-runs (§3.3), over-notify never under.
- **A4 — "Fan-out is rigged by N=2."** §4.5 + NB-1: match-detection is O(changes ×
  distinct-predicates-per-(coll,tenant)), N-independent; delivery is the honest O(N) LiveView
  floor, and the 4c bench REQUIRES characterizing + bounding it to a stated N (no deferral).
- **B1 — "Cross-tenant reactive leak."** Closed by the write-time-verified tenant tag (§3.4):
  the delta carries the writer's verified tenant; the local match visits ONLY that tenant's
  bucket; the cross-instance topic is per-tenant AND nudge-only (`Record=""`). No nil-identity
  path reaches an unscoped topic — the tag is data on the delta, not a pump-goroutine re-derive.
- **B2 — "Fails OPEN, not closed."** Closed by the enforced gate (§4.5): a `""`-tagged delta
  matches ONLY the `""` bucket; there is no code path iterating all tenants. The reactive
  analogue of the v0.16.6 SQL-`WHERE` gate. NB-2 puts the two-tenant `-race` test in 4a.
- **B3 — "Cross-instance bridge is a layering inversion / a false-green cell."** Closed: (1)
  the bridge is `rt → bluedb` (legal) via the changefeed + rt pump (§4); (2) the matrix is
  corrected (§6.2) so **embedded/sqlite multi-replica boots HARD-FATAL** (unshared local
  store — the write isn't in the other replica's store), and **postgres multi-replica requires
  a Redis broker or boots FATAL**. No green cell that isn't proven end-to-end.
- **A5/A6 (durability + before-durable)** — §7(1): emit only post-`Apply(Sync)`; sealed engine
  fires nothing.
- **A7 (mutex/multi-tab)** — §8: fold under `sess.mu`, existing multi-tab fan-out,
  panic-rollback restores handlers.
- **A8 (GC floor bloat)** — §7(6): `Advance` per consumed `commitTs`; a closed tab `Release`s.

---

## 11. Remaining weakest points (what I'm least sure survives a re-grill)

1. **The `Cmd.perform` write goroutine's identity stamp (write-time tag correctness).** §3.4
   assumes every `Persist` write runs on an identity-stamped goroutine so
   `SessionIdentity(currentLiveSession())` yields the writer's verified tenant. `update` and
   `handleInitial` stamp their goroutines; the ref asserts `Cmd.perform` tasks do too
   (`(ref) live_reactive.go:176` comment), but I have **not re-verified the current tree's
   `Cmd.perform` stamps**. If a `Persist` write can run on an UNSTAMPED goroutine, its tag is
   `""` → its delta fan-outs only to `""`-bucket subs → **a real tenant's write silently
   fails to notify that tenant** (fail-closed, so SAFE — not a leak — but a liveness bug). The
   4b two-tenant demo must include a write issued from `Cmd.perform`, not just `update`, to
   prove the stamp holds. **Most likely re-grill target.**

2. **Cross-instance is conservative-only (re-query), so the O(writes) headline is
   single-instance.** §5: a multi-replica tenant re-queries the shared Postgres on every nudge
   → cross-instance cost is O(nudges × instances-hosting-the-tenant × query-cost), not
   O(writes). Honest, but a griller will note the O(writes) win evaporates the moment a tenant
   spans replicas. The 4c bench must include a cross-instance conservative case, and the doc
   must not headline O(writes) for multi-replica.

3. **Order-witness only works when the order column is a DECLARED index.** §2.5 leg 1 needs
   the order column's coord in `NewIndex`/`OldIndex`, which `buildIndexer` emits only for
   declared indexes. For an order column that is NOT indexed, only leg 2 (pk-keyed re-sort on
   any displayed-pk change) fires — which requires the change to be delivered at all. If the
   filter footprint is a precise range and an order-only `Put` produces a Stay that the belt
   catches (pk ∈ resultPks), the re-sort fires; but a griller may construct a case where an
   order-only change to a displayed row does NOT hit the filter footprint's coord (e.g. the
   filter column also changed in a compensating way) — needs an explicit 4a test to rule out.

4. **`classifyIndexable`'s v1 envelope is narrow (single-column ascending).** Composite/OR
   list-view predicates (`status IN (…) AND tenant = …`) fall to the conservative tier
   (re-run). Correct, but the *common* multi-tenant list view may NOT be in the precise tier,
   undercutting the O(writes) headline for realistic queries. Widening to composite footprints
   is real work deferred as an optimization (not a proof) — but the 4c bench should report what
   fraction of realistic queries land precise vs conservative.

5. **R6 verified-identity is a hard external prerequisite (§3.4).** Tenant scoping (and hence
   ALL multi-tenant reactivity) is inert without framework-verified `SessionIdentity` on the
   standard `Std.Auth` path. Fail-closed makes the gap SAFE (empty, not leaky), but "reactivity
   does nothing for exactly the authed SaaS the magic targets" is a Phase-4 blocker living
   OUTSIDE Phase 4. It must be confirmed shipped, not assumed.

6. **The `CommitReq.Tenant` threading touches the write path breadth.** Adding a transient
   `Tenant` to `CommitReq` + stamping it in `blindPut`/`txWrite` + reading it in the committer
   is a small change, but it crosses the L1/L2 boundary the engine keeps clean
   (`engine.go:112` "OPAQUE to L1"). `Tenant` is NOT opaque-L1-payload (it is a transient
   routing tag, never durably written), so it does not break the changelog format — but a
   griller may argue it muddies the layering contract. The alternative (a `bluedb`
   goroutine-local, §3.4 rejected-alt) trades layer-purity for mechanism-duplication; the
   choice should be defended explicitly at implementation.

7. **Model-dependent-filter re-register race (§8).** Re-registering on a Model-field change
   uses the §7 register-live-first discipline, but the OLD sub must be torn down and the NEW
   sub's baseline+buffer stood up without a miss window. It is easy to get subtly wrong (a
   change during the swap). Needs a dedicated 4a re-register-race test, and it is the
   second-most-likely correctness gap after #1.

---

## 12. Changes from the grilled v1 (per finding)

| Finding | v1 flaw | v2 closure |
|---|---|---|
| **⚠️ EXPOSED (Phase-2)** | `blindPut` emitted no `OldIndex` → precise `Leave` never fired on the blind path | Fixed at `aba0611a` (`embedded.go:164-184`); §1.1/§2.3 Leg 1 re-prove it; 4a gate has blind-update-out→Leave |
| **A#1** | precise matcher used only coord hits; order-only churn → stale order + dup | §2.2 belt clause (`wasDisplayed`) + §2.3 Leg 2 (`resultPks`) + §2.5 order-witness + pk-keyed re-sort with a no-stale/no-dup proof; 4a order-churn gate |
| **A#2** | registered live AFTER backfill → miss window between `Tail` scan and registration | §7 REGISTER-LIVE-FIRST: buffer → pin `readTs` → baseline → drain-dedup → durable `Tail` only on overflow; airtight no-window proof |
| **B#1** | commit-path fan-out goroutine had no identity → nil → unscoped shared topic + body-carry → cross-tenant leak | §3.4 WRITE-TIME-VERIFIED tenant tag (stamped by the writer, carried on the delta); §4.5 local match visits only the delta's tenant bucket; §5 nudge-only (`Record=""`) cross-instance |
| **B#2** | failed OPEN (nil identity → firehose); mislabeled as liveness | §4.5 enforced fail-CLOSED gate — `""`-tagged delta matches ONLY the `""` bucket, no all-tenants code path; the reactive analogue of the v0.16.6 SQL-`WHERE` gate; NB-2 test in 4a |
| **B#3** | `bluedb`→`rt` broker call is a cycle; embedded multi-replica claimed ✅ but the bridge didn't typecheck / the store isn't shared | §4 changefeed + rt pump (legal `rt→bluedb`); §6.2 corrected matrix — **embedded/sqlite multi-replica boots HARD-FATAL** (unshared local store), postgres multi-replica needs a Redis broker or FATALs; §5 chain proven for shared backend only |
| **NB-1** | 4c allowed "realistic-N bench OR a Phase-6 scope decision" (proof-deferral loophole) | §9 4c: the realistic-N bench is REQUIRED; must bound O(N) delivery to a STATED N; only a Phase-6 *optimization* (not proof) may be deferred |
| **NB-2** | two-tenant isolation exercised only in 4c | §9 4a gate now includes the two-tenant commit-path isolation `-race` test |
| **NB-3** | resync thundering-herd unbounded | §9 NB-3: per-sub coalescing + a global re-query semaphore (`K = min(GOMAXPROCS, 8)`) + per-sub debounce; the bound is stated and gated (≤ K concurrent re-queries) |

---

*End of v2. Re-grill this before any Phase-4 code. The two most likely re-grill targets are
§11 #1 (the `Cmd.perform` identity stamp — a liveness, not a leak, but it undermines the
magic) and §11 #7 (the re-register race).*
