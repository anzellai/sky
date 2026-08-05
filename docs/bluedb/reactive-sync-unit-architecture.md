# Persist reactive — the sync-unit architecture

Status: **APPROVED** (2026-08-05, user-confirmed after two 4-adversary grill
rounds). Replaces the collection-topic reactive model. Supersedes the change-feed
→ overlap → re-query approach for the security/scoping layer; that machinery
survives only for the non-session-write edge case (below).

**Locked decisions:** default = persist whole Model + revalidate derived fields
on reopen (SWR); `Persist.ephemeral` = opt-in per field for huge Models.
Reactivity and ephemeral are separate features. Marker = `Persist.liveInto db
.field query` (bare accessor → replace-fold + `sky:` `,noPersist` tag; no
wrapper, no closure inference). Build order: (1) verified session identity on the
Std.Auth path, (2) reactivity core (identity-on-goroutine + startReactive-on-
reconnect + tenant-scoped topic-with-body + `liveInto`), (3) ephemeral opt-in.

## The one idea

**Reactivity = keeping shared state consistent across the sessions that should
see it. Key the broadcast by the *sync unit* (who shares the state), not by the
*collection* (what table changed).** The sync unit comes from *verified*
`SessionIdentity`, so the tenant boundary IS the topic — cross-tenant leak,
activity oracle, and subscription IDOR all dissolve by construction.

## Three tiers — only the middle needs new machinery

| Tier | Who shares state | Sync unit | Mechanism | New work |
|---|---|---|---|---|
| Unauth / one browser | tabs of one cookie jar | `session_id` | per-session fan-out (`fanOutFrame` → `sseConns`) | **none — shipped v0.18** |
| Auth, tenant-scoped | a tenant's sessions (1 user multi-device, or N users in a tenant) | the **tenant** (verified claim) | nudge → re-query, topic = `reactive:<tenant>` | the whole design |
| Public collab | a room's participants | the **room id** | same, topic = `reactive:room:<id>` | reuses the same path |

### Why per-session fan-out already covers tier 1 (and multi-tab everywhere)

Same `sky_sid` = one server Model + one mutex; every committed frame fans out to
all of that session's live SSE connections. Multiple **tabs/windows of one
browser** are always in sync with **zero** broadcast. The only gap is
**different `sky_sid`s** (a user's phone vs laptop — cookies don't cross
devices), which is exactly what tier 2 fills.

## The mechanism (tier 2/3)

1. **Write** — `Persist.put/insert/update/delete`, called from `update` in a
   **session context**, does three things atomically-in-spirit:
   - write to the durable store (BlueDB / SQL) — durability + the query source,
   - the app updates its Model (the write's local effect),
   - publish a **nudge** `{op, coll, pk}` to the *writer's verified sync-unit
     topic*. **Record body is never broadcast** (kept from the last grill).
2. **Subscribe** — each session subscribes to **its own** sync-unit topic,
   derived from verified `SessionIdentity` at subscribe time. **Fail-closed**:
   no verified identity ⇒ no cross-session topic (tier-1 per-session fan-out
   only). A session physically cannot subscribe to another tenant's topic.
3. **On nudge** — the receiving session **re-queries its declared reactive
   fields** (`Persist.live` bindings) and folds the fresh result into its Model.
   Re-query (not value-copy) because each session's filter/view differs and the
   store is authoritative + self-healing (any nudge heals all prior misses).
4. **Fan-out within a session** — the re-query's Model change fans out to that
   session's tabs via the shipped path.
5. **Durability** — the Model is persisted by the state-sync foundation
   (R1/R2/P1, already landed); the data by the store.

### Why "sync the Model" is NOT "copy the Model"

Even one user's own devices hold **different** Models (laptop on page X with a
half-typed draft; phone on page Y). Private state (draft/page/selection) must
stay per-connection. So the nudge re-derives only the **declared reactive
fields** — the `Persist.live` declaration stays and is what separates *shared
data (re-query it)* from *private state (leave it alone)*. The declaration
answers "which fields"; the sync-unit topic answers "who + is it allowed" — two
orthogonal concerns, cleanly split.

## Security — solved at the topic layer

- **Topic = verified sync unit.** A session only ever receives its own tenant's
  nudges. No collection topic that every tenant subscribes to.
- **No record body on the wire** — nudge only; even a mis-scoped subscriber
  learns nothing but "something in your own tenant changed."
- **Fail-closed** — missing/invalid identity degrades to per-session isolation,
  never to a shared topic.
- **No trust in Model** — the scope value is read from `SessionIdentity`
  (`identityValid`-gated), never from `model.*`. Forging `model.tenantId` buys
  nothing.
- This retires F1 (cross-tenant record leak — already nudge-only), F2 (activity
  oracle — you only see your own tenant's activity, which you're entitled to),
  F3 (subscription IDOR — you can't name another topic).

## API — small surface, magic default

- **Default (99% — tenant-isolated SaaS): zero per-query code.** The framework
  derives the sync-unit topic from a configured identity claim:
  ```elm
  Live.config { … }
      |> Live.withReactive reactiveQueries      -- which fields are live
      |> Live.withReactiveScope "tenantId"      -- the verified claim = sync unit
  ```
  Unauth sessions (no identity) transparently fall to per-session fan-out.
- **Opt-in (collab/rooms): per-binding scope** when the sync unit isn't the
  tenant claim:
  ```elm
  Persist.live conn (query …) fold
      |> Persist.scopedBy (Persist.room model.roomId)   -- authorised room join
  ```
  A `room` scope must be gated by an app authorisation check (the app only binds
  a room the session may join); the framework still refuses to publish/subscribe
  a room topic without that gate returning ok.
- **Low-level escape hatch (0.0001%)**: `Persist.watchCollection` stays for
  hand-rolled control, but is documented as advanced + "you own the scoping."

## What changes vs. the current build

| Current | New |
|---|---|
| Publish from the process-global change-feed pump (no identity → can't scope) | Publish from the **write layer in session context** (has verified identity) |
| Topic = `__bluedb:<collection>` (every tenant subscribes) | Topic = `reactive:<verified sync unit>` |
| Overlap engine is security-load-bearing | Overlap engine is a **pure perf option** (skip re-query that can't matter) |
| Fan-out = O(all viewers of a collection) | Fan-out = O(one sync unit's sessions) |
| Change-feed is the reactive hot path | Change-feed survives ONLY for non-session writes (below) |

## The surviving edge case — non-session writes

Writes that do **not** happen in a session context — a background `Sky.Cli`
job, a scheduled task, a raw `Std.BlueDB` write, an external process writing the
SQL DB — have no session ⇒ no verified sync unit. Options, to be decided in the
grill:
- **(E1)** Require such writes to pass an explicit scope
  (`Persist.putScoped scope …`) so they can publish to the right sync-unit topic.
- **(E2)** Keep the process-global change-feed for these, publishing a
  *scope-derivable* nudge (the record carries the tenant column ⇒ derive the
  topic from the row, not the collection). Needs the tenant column declared.
- **(E3)** Non-session writes don't publish; sessions pick them up on their next
  natural nudge or a periodic re-query. Simplest, weakest freshness.

## Performance / scale / reliability

- **Per write:** work = (this sync unit's sessions) × (nudge + 1 re-query + 1
  render). Tenant-isolated ⇒ a handful of sessions, not all viewers.
- **Scale:** topics bounded by *active tenants*, not users×collections. Redis
  channel per active tenant; sticky sessions cluster a tenant per instance so
  most fan-out stays in-process.
- **Reliability:** re-query authoritative + self-healing; Model durability =
  R1/R2/P1; per-session fan-out in-process + hardened. A dropped nudge self-heals
  on the next one; a periodic safety re-query (opt-in) bounds worst-case
  staleness.
- **Perf options (non-security):** wire the overlap engine to skip re-queries a
  change can't affect; keyed/subtree render to cut the paint. Both optional.

## GRILL OUTCOME (4 fresh-context adversaries) + REFINED DESIGN

Verdicts: security **BROKEN**, correctness **NOT CONVERGENT**, perf **HAS A
CLIFF**, DX **TOO MUCH SURFACE**. All four agreed the *sync-unit direction is
correct*; every break is a prerequisite or a refinement, not a rejection.

### The foundation the whole thing stands on (must build FIRST)

**Framework-verified session identity on the standard `Std.Auth` path.**
Security-F1: `sess.identity` is populated *only* by the sub-app mount gate
(`live.go:4113`, `IdentityFromContext`); the normal bcrypt+JWT-cookie login that
sets `model.session` never sets it. So for the exact tenant-isolated SaaS the
"magic default" targets, `identityValid=false` → tier-2 never fires, and the
only way to make it "work" is to read scope from Model — which reopens the leak.
Prerequisite work:
- Bridge `Std.Auth` login → `sess.identity` from the *verified* JWT/session
  claims (server-derived, never client Model).
- **Re-scope on login/logout** (Security-F5): identity change tears down + re-subscribes reactive loops.
- **Expiry re-validation** (Security-F4): identity is a mint-time gob snapshot
  with no TTL check; "expired ⇒ fail-closed" is currently not implemented.
This foundation is independently valuable (console auth, the v0.16.6 tenant SQL
gate, analytics identify all want a verified session identity).

### The security ⇄ perf resolution (both grills, same knot)

Perf-Cliff-1: nudge-only (record body stripped) *neuters* the overlap engine —
with no body, every insert/update re-queries all N sessions → O(N×M). But
nudge-only was a mitigation for the **shared collection topic**. On a **verified
per-tenant topic**, carrying the body is *safe* (tenant-mates are entitled to
it). So the resolved payload is `{op, coll, pk, commitToken, record}` on a
tenant-scoped topic → the overlap engine works → **O(writes)** for disjoint-data
SaaS. The two models stop fighting once the topic is the tenant.

### Convergence fixes (correctness grill — all default-on)

- **Freshness token** (F1): nudge carries a per-tenant monotonic commit token;
  receiver retries the re-query until `read_token ≥ nudge_token` (feed the
  existing retry loop a freshness predicate). Closes replica-lag / cross-instance
  stale-read → permanent-stale on the last write.
- **Periodic safety re-query DEFAULT-ON** (F1/F7): bounds worst-case staleness
  from a lost/dropped publish to one interval. (Was opt-in = stale-by-default.)
- **Re-query on own dispatch when binding inputs change** (F2): a
  model-dependent filter (`WHERE status = model.filter`) that changes via a plain
  dispatch must re-run its binding — nudge the session's own loop on every
  committed dispatch, dirty-check suppresses the no-ops.
- **Pre-render dirty-check** (F4 + Perf-Cliff-3): hoist the DeepEqual check
  BEFORE render — no-op the whole refresh (no render/diff/persist) when the fold
  result is unchanged. Today the check only gates the SSE frame *after* a full
  render, so N−1 tenant-mates pay a render storm per write (and fill `sseCh` →
  self-inflicted resyncs).
- **TOCTOU guard** (F3): capture binding inputs at snapshot, re-check under the
  lock at fold time, discard (re-arm) if they changed.
- **Loop keyed by sync-unit / binding-set, not per-collection** (F6): a
  multi-collection view refreshes atomically under one lock — no torn cross-field
  frame. Also collapses today's goroutine-per-(session×collection) to one loop
  per session (Perf-Cliff-6 win).
- **`startReactive` on SSE reconnect + re-hydrate** (F5): not only on full GET,
  so an SPA session after a server restart re-establishes its subscriptions.
- **Shorten `sess.mu`** (Perf-Cliff-4): run the re-query OUTSIDE the lock; only
  fold+render under it, so a write-heavy tenant doesn't starve that session's
  own input.

### Scale posture (perf grill)

- **Drop "sticky-per-tenant"** (Cliff-2): it concentrates a big tenant on one
  instance — a regression. Keep per-session sticky + a tenant-scoped Redis
  channel; each instance re-queries only its local subscribers → load spreads.
- **Honest limit:** a genuine shared-feed/broadcast tenant is **O(N²)** and
  fundamental (same wall as any LiveView-style system). The answer is overlap +
  keyed render + horizontal spread, not re-keying. Say so; don't pretend.
- **Overlap engine + keyed render are NOT optional** above a few hundred
  sessions/tenant — reclassify as load-bearing.

### API shrink (DX grill) — the whole 99% surface

- **`Live.withReactive`** — attach once. Scope **auto-derived from verified
  identity** (auth → identity unit; unauth → per-session). **No dev-named claim
  string.** If ever overridden, the claim is **boot-validated against the
  identity schema and fails LOUD** — never the silent per-session no-op that
  a mistyped `withReactiveScope "tenantId"` causes today (DX-#1, the worst DX
  class).
- **`Persist.live`** — mark a query reactive *at the site it already runs* (a
  `liveInto`-style terminal), so there's no parallel `reactiveQueries` list and
  no fold that duplicates the app's own initial load (DX-#4).
- **Room reactivity rides a verified membership set** established at authorized
  join — reuses the identity-derived path, **zero new vocabulary, zero Model
  trust** (kills DX-#3 *and* Security-F2 together). Drop `withReactiveScope`,
  `scopedBy`, `room`.
- **`Persist.watchCollection`** stays as the documented 0.0001% escape hatch.
- **Dev-mode assertion:** `withReactive` attached but the running session has no
  cross-device sync unit → dev banner/log ("reactive is per-session only — no
  verified identity; cross-device inactive"), so the auth/unauth split (DX-#2)
  can't ship silently broken.

### Non-session writes — decided

**E1 only** (explicit scope passed by the trusted caller). E2 (derive topic from
the row's tenant column) is writer-controlled → cross-tenant misdelivery
(Security-F3). E3 (don't publish) is the safe fallback when no scope is given.

### Revised build sequence

1. **Verified session identity foundation** — `Std.Auth` login → `sess.identity`
   (server-verified), re-scope on login/logout, expiry re-validation. *Gate:
   security F1/F4/F5 closed; standalone value.*
2. **Tenant-scoped reactive topic** carrying `{op, coll, pk, commitToken,
   record}`; publish from the write layer in session context; subscribe
   fail-closed from verified identity.
3. **Convergence fixes** — freshness-token retry, pre-render dirty-check,
   sync-unit-keyed loop, re-query-on-own-dispatch, TOCTOU guard, reconnect
   re-subscribe, default-on safety re-query.
4. **Perf** — wire the overlap engine (now body-enabled), keyed render; drop
   sticky-per-tenant; document the O(N²) broadcast floor.
5. **API** — `withReactive` (auto-scope, loud override) + `Persist.live`
   at-site; verified-membership rooms; dev-mode assertion.

## FOUNDATION REFINEMENT — "persist the inputs, re-derive the data" (TO GRILL)

Motivating case (user): an internal admin app, many concurrent users, a huge
Model (thousands of records). Today the whole Model is gob-encoded per change
(`encodeSession`, `live_store.go`; no diff) — so a big Model is expensive on
THREE axes at once: (1) persistence — multi-MB blob written per change; (2) live
RAM — the Model sits in server memory per session (LiveView-style); (3)
render/diff — CPU scales with Model size. Gob-diffing only chips at (1) and is
hard (opaque struct). The right lever hits all three: **stop persisting the big
query-derived data; re-derive it.**

### Principle

The Model conceptually splits into:
- **Input / UI state** — page, filters, auth, scroll, draft, selection. Small.
  Persisted in the session blob.
- **Query-derived data** — the rows a `Persist.live` binding produces. Large.
  **NOT persisted**; re-derived by re-running the query on load (recovery) and on
  NOTIFY (reactivity) — the SAME operation.

Blob size becomes dataset-independent. Live RAM + render stay bounded IF the
query is paged (hold a page, not the table). Recovery = reactivity = refetch.

### The mechanism candidates (the core design fork)

- **A — `Reactive a` field wrapper.** `todos : Reactive (List Todo)`.
  Custom `GobEncode` emits nothing (persists empty); the framework refetches on
  load via the field's bound query. Explicit + typed, but every read/update site
  unwraps (`Reactive.get` / re-wrap on `{m | todos = ...}`), and it must compose
  with `view`, record-update, `Codec`, HM inference.
- **B — infer the derived field from the `Persist.live` binding.** The binding's
  fold `\rows m -> { m | todos = rows }` names the target field — but it's an
  opaque closure; extracting "which field" at compile or runtime is hard.
- **C — a designated sub-record.** `model.data : DataView` — a known field the
  persister zeroes before encode and the framework rebuilds. Coarse (all-or-
  nothing), simple, no per-field machinery, no wrapper at read sites.

### On-load sequence (no stale snapshot for data)

Restore input blob → paint (data fields at their zero/`loading`) → re-derive
(re-run bindings) → fold → repaint. Unlike whole-Model restore, the data half
has no stale-snapshot window because it was never stored — it's loading-then-
fresh. Input half is instant.

### GRILL RESOLUTION (4 adversaries: DX / feasibility / runtime / correctness)

Verdicts: DX **HYBRID-B IS MAGIC**, feasibility **D IS BUILDABLE**, runtime **5
HAZARDS**, correctness **6 CONTRACT HOLES**. None rejected the direction; taken
together they force ONE reframe that dissolves the conflicts:

**Reactivity and not-persisting-the-data are TWO features, not one.**

- **Reactivity** (live NOTIFY → re-derive → fold) — clean, all four bless it.
- **Ephemeral (don't persist the derived data, re-derive on load)** — the
  blob-size optimization. It ALONE introduces the empty-flash, actionable-empty
  state, lost-edit, Loading-state, and recovery≠reactivity problems.

So: **default = persist the whole Model + revalidate derived fields on reopen
(SWR); ephemeral = opt-in per field, for huge Models only.**

#### The marker (reconciled across DX + feasibility + correctness)

`Persist.liveInto db .todos (query …)` — a **bare field-accessor** `.todos`:
- **DX:** the field stays a plain `List Todo` — `view`/`update`/`Codec` untouched,
  zero read tax (kills the `Reactive a` wrapper = RemoteData-redux, DX grill).
- **Feasibility:** `.todos` is a *syntactic* accessor literal at the call site
  (statically known like record-update — NOT the opaque fold closure that blocked
  candidate B). The compiler stamps the field for the persister via the existing
  `sky:"name,type"` struct tag → `sky:"todos,…,noPersist"` (mechanism D — reuses
  `codec_auto.go` tag reflection; no HM change, no GobEncode codegen).
- **Correctness:** the framework OWNS the assignment (`{m | todos = rows}`,
  replace-only) — an arbitrary `model→model` fold is inexpressible, so the
  accumulating-fold break (Hole 3a) and recovery≠reactivity vanish.

#### Default vs opt-in

| | Default (99.9999%) | `|> Persist.ephemeral` (huge Model, opt-in) |
|---|---|---|
| Persist | whole Model (incl. derived data) | derived field zeroed out of the blob |
| Reopen | instant paint (last-known) → revalidate (re-derive) | paint skeleton/empty → re-derive fills |
| Blob size | grows with data | dataset-independent |
| Loading state | none needed (data always present) | field is a tri-state; dev handles it |
| Lost-edit / empty-flash | none | dev accepts the documented window |

Default kills the empty-flash + actionable-empty (Holes 1/4) + lost-edit (Hole
2) + the "loading-flash is worse UX than SWR" finding (Hole 7) for the common
app. Ephemeral is the escape hatch for the admin grid, with its trade-offs
documented — exactly "low-level control if devs choose it."

#### FATAL prerequisites (needed for reactivity itself, not just ephemeral)

1. **Stamp identity on the re-derive goroutine** (runtime 3a): `reactiveRefreshOnce`
   calls `sky_call(b.run, nil)` at `live_reactive.go:229` WITHOUT
   `setGoroutineLiveSession(sess)` → a tenant-scoped re-derive reads a nil session
   → fail-closed to empty (or unscoped). Wrap the query body in
   `setGoroutineLiveSession`/`defer clear`.
2. **`startReactive` on SSE reconnect + re-hydrate** (runtime 3b, = the earlier
   F5): today it fires only from the full-GET `handleInitial` (`live.go:4231`); an
   SPA that reconnects via SSE after a restart gets no loops → stale (default) or
   permanently-blank (ephemeral). Wire it to the reconnect/re-hydrate path.

#### Other required fixes (from the grills)

- **Zero on a shallow copy at the encode boundary, never in place** (runtime 1):
  in-place zeroing empties the live session — and `reap` (`live_store_bluedb.go:231`)
  makes it silent background loss. `encodeSession` shallow-copies the top-level
  Model + zeroes each `,noPersist` field header (multiple top-level fields OK —
  no forced sub-record).
- **Dirty-check over the zeroed projection** (runtime 2 + correctness cross-cut):
  `lastPersistedModel` holds the zeroed Model so a pure re-derive is a no-op
  persist (else every ephemeral refresh persists).
- **Restart admission control** (runtime 5): a global weighted semaphore + jitter
  on re-derive, else 1000 reconnecting sessions stampede the DB at cold start.
- **`liveInto` fields are server-owned — don't optimistically mutate them**
  (correctness 2b/5): mutate via a write; the nudge reflects it. Local-only state
  (drag-reorder overlay) lives in a separate INPUT field, not the live field.
- **Ban derive-of-derive as a stored field** (correctness 3b): a value computed
  from a live field is a `view`-time pure function, never a persisted field.
- **Keyset (cursor) pagination for reactive paged bindings** (correctness 6):
  `WHERE id > :last ORDER BY id LIMIT n`, not OFFSET, to avoid row-shift anomalies
  under live inserts. Document until offered.

#### Implementation progress (feat: exp/bluedb)

- **Phase 1 — verified session identity** (`5767765a`): `Live.withIdentify`
  callback populates `sess.identity` from a verified login; also fixed a
  fail-OPEN in the shared console-auth invoke (Err → empty-but-valid identity).
- **Phase 2a/2b** (`9e6c8c7b`): identity stamped on the reactive-loop goroutine
  (3a); `startReactive` on SSE reconnect (3b) + a build-then-claim-or-discard
  concurrency guard (no leak on concurrent reconnect).
- **Phase 2c — tenant-scoped topic** (`01055470`, SHIPPED): sync unit =
  verified `Claims["tenant"]` (aligns with `tenantPrefixForSession`,
  `hub_bridge.go:539`). Topic = `reactive:<tenant>:<coll>` when a verified
  tenant exists; else `bluedbCollTopic(coll)` (unauth/dev — byte-identical).
  Subscribe derives it from `SessionIdentity(sess)` (fail-closed); publish moves
  to the write layer (`Persist_publishChange` → `reactivePublishScoped`, deriving
  the topic from the WRITER's own verified identity — forgery-safe, never from
  record data); KV `Persist` arms gain that write-layer publish; the pump stays
  on the collection topic for unauth/background. DEFERRED to follow-ups:
  record-body-on-tenant-topic + overlap wiring; the freshness/commit token
  (multi-instance read-replica only).

#### Phase 3 recon (liveInto marker + ephemeral) — feasibility LOCKED

A bare `.todos` accessor is a distinct node `Expr::Accessor(Name("todos"))`
(`hir.rs:59`), preserved through resolve/infer, desugared to an opaque
`func(_r any) any { return rt.Field(_r,"Todos") }` only at `lower.rs:2506`.

- **3a `liveInto db .field query` — SHIPPED** (`3bcd27e7`): lowerer special-cases
  the `Persist_liveInto` kernel, extracts the field name from the bare
  `Expr::Accessor`, and rewrites to `Std_Persist_live` with a synthesized
  `rt.RecordUpdate` replace-fold (DCE-safe; non-accessor arg → hard error).
  Example 58 rewritten to `liveInto .todos`; reactive e2e identical; corpus gates
  + sweep 29/0 green.
- (recon) The field name IS statically
  recoverable at the call site: args reach `lower_call` as `&[ExprId]` HIR nodes
  (`lower.rs:3186`) and `lower_call` already pattern-matches an argument's HIR
  before lowering it (precedent `lower.rs:3346` for `Expr::Lambda`). A `liveInto`
  arm reads `Name("todos")` exactly as `lower_update` recovers a static field
  write (`lower.rs:4785`). The framework-owned replace-fold synthesizes via the
  existing `rt.RecordUpdate(m, {field: rows})` kernel (precedent `lower.rs:4747`).
  No new metadata channel needed. Parts: a `liveInto` lower arm + a Sky binding
  in Persist.sky + reuse RecordUpdate. Medium size, self-contained.
  NOTE: no existing Sky feature mines a name from an accessor at compile time
  (`Codec.field "id" .id` passes the name as a SEPARATE string; the accessor is a
  runtime getter). `liveInto` deriving the name from `.field` is a NEW capability
  — cheap, but genuinely new.
- **3b `Persist.ephemeral` (`,noPersist` tag) — needs a NEW lower→codegen
  channel.** Appending a 3rd `sky:` tag segment is trivial at emit
  (`codegen/src/lib.rs:246`) + needs `skyTagType` fixed to cut at the 2nd comma
  (`codec_auto.go:70`) + a flag reader. The HARD part: codegen's `emit_type_def`
  sees only `(name, GoTy)` per field and has no knowledge of which
  `(record, field)` pairs a `liveInto` marked — a side registry populated during
  the 3a lower arm must be threaded into codegen. Plus the runtime side (shallow-
  copy-zero persister at the encode boundary, projected dirty-check, restart
  admission control). This is the substantial, compiler-invasive half.

#### Build order (folds into the earlier revised sequence)

The two FATAL prerequisites (identity-on-goroutine, startReactive-on-reconnect)
+ the tenant-scoped topic-with-body (earlier grill) are the reactivity core.
`liveInto .field` (replace-fold + tag) is the marker. `Persist.ephemeral` +
shallow-copy-zero + projected dirty-check + admission control are the opt-in
blob-size layer, built last.

### Open questions for THIS grill

1. Which mechanism (A/B/C) is actually implementable given the Sky→Go compiler +
   gob + HM inference, and which has the least DX tax? Is there a D nobody's seen?
2. First-paint gap: data fields empty until re-derive completes — is that a blank
   flash / layout jump / an actionable-empty-state the user mis-reads as "no
   data"? How to distinguish "loading" from "genuinely empty"?
3. A field that is PART input, PART derived (a list the user reorders locally AND
   that comes from a query) — optimistic local edits vs re-derive clobbering
   them. Who wins, and is it expressible?
4. Interaction with R1/R2/P1: the persist path now must zero/skip the derived
   fields before encode — where, and does it break the DeepEqual dirty-check
   (the derived field is always "changed" to empty)?
5. Re-derive needs the session's query context (conn + verified identity) OUTSIDE
   a request — does `currentLiveSession()` / the reactive loop provide it on the
   reopen path AND the restart path?
6. Failure: re-derive fails (DB down) on reopen — does the user get a broken
   Model (inputs but no data), a retry, or a hard error? What's the contract?
7. Does this force every Sky.Live app to adopt the split, or is it opt-in per
   field with a zero-cost default for apps that DON'T have huge Models?
8. Paging: to bound RAM/render the query must be paged, but the reactive re-derive
   must then re-fetch the CURRENT page — does a change on page 1 while viewing
   page 5 still notify correctly, and does "hold a page" break offset-based
   reactivity?

## Original open questions (now answered by the grill above)

1. Is "sync unit = verified claim" airtight, or is there a scope the app needs
   that identity can't express (and does `scopedBy` cover it without reopening
   the IDOR)?
2. Non-session writes — E1 / E2 / E3?
3. Re-query storm: N sessions in one tenant all re-query on every write — is the
   overlap engine *needed* (not optional) at some tenant size, and where's the
   knee?
4. Ordering: two writes in one tenant → two nudges → two re-queries; is
   last-write-wins on the re-query always convergent, or can a re-query race a
   later write and persist a stale fold?
5. Does folding a re-query result into the Model interact badly with the P1
   dirty-check or the per-session mutex (deadlock / lost update)?
6. DX: is `withReactiveScope "tenantId"` + `scopedBy` genuinely the whole
   surface a 99% app sees, or does collab force more concepts?
