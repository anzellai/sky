# Phase 4 design grill — findings (+ a Phase-2 regression it exposed)

Grill of `docs/bluedb/phase4-reactivity-design.md` @ `19044eb9`. Two adversaries.
Design-only; **not implemented**. Design does NOT proceed to code until every blocking
finding is folded and re-verified.

## ⚠️ EXPOSED: shipped Phase-2 SSI under-reject (fix-first — highest priority)
Grill A's cross-cutting alarm, **confirmed in code**:
- `blindPut` (`embedded.go:156-165`, the autocommit upsert for collections with no unique
  columns) emits `NewIndex` only, **no `OldIndex`** — unlike `blindDelete` (`embedded.go:169-190`,
  which reads the pre-image precisely so a scanner sees the row LEAVE).
- `validate.go:47` detects range departure via `coordHit(New) || coordHit(Old)`.
- `txn.go:563` records a scanned row as a point read **only when `fallback`** — a range-optimized
  (precise-tier) scan records the range, not the returned PKs.
- ⇒ A txn scans `where status='open'` (range `[open,open]`, no point for r1); a concurrent
  autocommit `put r1 {status:closed}` commits `{NewIndex:[closed], OldIndex:nil}`; validator sees
  no point hit and no coord hit → **commits a stale read = non-serializable UNDER-REJECT.**
- Phase-2 conformance missed it: its write-skew tests drove updates through `Txn.Put`
  (→ `ensurePreimage` → OldIndex populated), never the autocommit blind path.
- **FIX (closes Phase-2 hole AND Phase-4 A#1):** `blindPut` reads the pre-image + emits `OldIndex`
  (mirror `blindDelete`). Regression: an autocommit blind upsert-update moving an indexed row out
  of a concurrently-scanned range, vs a txn that scanned it → MUST be REJECTED (currently commits).

## Phase-4 design blocking findings

### Grill A — under-notification (2 blocking)
- **A#1** — same `blindPut` `OldIndex=nil`: an autocommit update moving a row out of a watched
  range fires NO `Leave` in the precise tier (which matches on coordHit(New/Old), not `resultPks`)
  → silent stale. Corollary: on the blind path `OldIndex` is always nil → every blind update
  classifies Enter (never Stay) → the §11 "re-sort on Stay" order-churn mitigation never fires →
  stale order + duplicate rows. **Closed by the blindPut OldIndex fix** + the precise-tier matcher
  must also consult `resultPks` (belt) and witness order-columns (order-churn).
- **A#2** — subscription setup race: the design registers into the live registry (`byColl`) AFTER
  backfill, so a commit durable between the `Changelog.Tail(readTs)` scan and registration is in
  NEITHER → missed. **Fix:** register live (start buffering) FIRST, then baseline + backfill, then
  dedup by `lastTs` ("subscribe before you snapshot").

### Grill B — fan-out / capability / tenant (3 blocking) — shared root cause
The design moved fan-out from the ref's identity-stamped rt/session layer DOWN to the commit-path
`bluedb` layer, which has neither verified identity nor a legal path to the rt broker.
- **B#1** — cross-tenant reactive LEAK: the commit-path fan-out goroutine is not identity-stamped,
  so `currentLiveSession()`→nil → `SessionIdentity`→!ok → tenant resolves to the UNSCOPED shared
  `bluedbCollTopic`. Combined with §5's decision to carry the full record body on the topic
  (the ref keeps `Record=''` precisely to avoid this) → **cross-tenant row-body leak.**
- **B#2** — R6 fails OPEN, not closed: no reactive equivalent of the v0.16.6 SQL-`WHERE` tenant
  gate. When identity is absent both pub+sub fall to the unscoped topic → every subscriber gets
  every tenant's delta. Design misclassifies its own weak point (calls it "empty rows"/liveness;
  §5's body-carry makes it confidentiality).
- **B#3** — cross-instance broker bridge is a layering inversion: `bluedb` cannot import `rt`
  (cycle; `rt` imports `bluedb`), so the commit-path fan-out can't reach the rt `Broker`. Embedded
  multi-replica reports `CrossInstanceReactive=true` → boots green → but the bridge doesn't
  typecheck → tenant-A's session on replica 2 silently never sees replica 1's write = the exact
  silent-stale the matrix claims impossible.
- **ROOT FIX (B#1/B#2/B#3):** keep delta-match + registry in `bluedb`, but perform identity
  resolution + tenant-scoped publish at the **rt/session layer** — re-add a `bluedb` changefeed
  (`(ref) bluedb/changefeed.go` `DB.Subscribe`) that an rt pump drains and publishes UP to the
  broker on the verified per-tenant topic (the ref's proven architecture). Add an enforced
  reactive tenant gate (fail-CLOSED). Keep `Record` off the cross-tenant topic.

### Non-blocking (fold, don't defer)
- **NB-1** — 4c must REQUIRE the realistic-N fan-out bench; the "OR Phase-6 scope decision"
  proof-deferral is a no-deferral-rule loophole (a Phase-6 *optimization* is fine; a Phase-6
  *proof deferral* is not).
- **NB-2** — move a two-tenant commit-path isolation `-race` test into 4a (today isolation is only
  exercised in 4c; the B#1/B#2 leak would look shippable through 4a+4b).
- **NB-3** — characterize/bound the single-instance resync thundering-herd (high write × high N →
  single matcher overflow → all affected subs re-query). The per-session mutex convoy IS bounded
  (last-writer-wins); the resync storm is not.

## Plan
1. **Phase-2 correctness patch (DONE + pushed @aba0611a):** blindPut emits OldIndex + regression.
2. **Design v2 (DONE @d965b682):** changefeed + rt pump + write-time tenant tag. Re-grilled below.
3. Implement Phase 4a with the v3 fixes baked in (two-tenant isolation + cross-instance fail-closed
   + tenant-not-durable tests in its `-race` gate).

---

## Re-grill of v2 (`d965b682`) — 2 blocking + 4 NB → v3 fixes (locked)

Orchestrator independently verified the v2 load-bearing assumption: `Cmd.perform` writes run on an
identity-stamped goroutine (`live.go:5298` `runWithLiveSession`), and enumerated every write path —
all intra-Live paths stamp; unstamped paths (raw `Http.Server` handler, background/CLI, subscriber
`toMsg` decoder) fail-closed to tenant `""`. No LOCAL leak. But:

- **RG#1 (BLOCKING) — cross-instance `""`-tag leak.** v2's strict-partition proof holds only on the
  LOCAL `byCollTenant` lookup. The cross-instance publish uses `reactiveTenantTopic`
  (`(ref) bluedb_reactive.go:42-49`) which FALLS BACK to the shared `__bluedb:<coll>` topic when
  tenant is `""`. A no-session write (Stripe webhook via `Sky.Http.Server`, background job) tags `""`
  → pk-nudge on the shared cross-tenant topic → every unauth reactive session across replicas learns
  tenant-A's row pk (`Record=""` holds, so body doesn't cross — pk-oracle, not body-leak).
  **v3 FIX:** empty `batch.Tenant` SKIPS the cross-instance broker publish entirely (fail-closed) —
  NO `reactiveTenantTopic` fallback. A cross-instance reactive write MUST carry a real tenant tag
  from an identity-stamped writer; out-of-session writes (webhooks) that need reactive propagation
  use an explicit tenant-attach escape hatch (`Persist.withTenant` / stamp the handler goroutine) —
  documented liveness boundary (closes NB-B too).
- **RG#2 (BLOCKING) — embedded multi-replica boot-fatal is not runtime-detectable.** A process has no
  intrinsic "replica count" signal; N replicas each with their own local pebble dir is
  indistinguishable from single-instance (pebble's dir-lock only catches SAME-dir). So a
  non-SkyDeploy multi-replica embedded deploy boots green → silent stale — the exact B#3 hole,
  relocated to boot-detection-impossibility.
  **v3 FIX:** replace "runtime detects topology" with an EXPLICIT fail-closed operator assertion.
  `watch`/reactive on embedded (or sqlite) requires `[data] reactiveScope = "single-instance"`
  (`SKY_DATA_REACTIVE_SCOPE=single-instance`). In a non-dev env, `watch` on embedded/sqlite WITHOUT
  the assertion is a boot HARD-FATAL with a clear message (assert single-instance, or use
  postgres data + redis broker for multi-replica reactive). Dev: warn+allow. Detectable (signal =
  the explicit assertion), fail-closed by default, parallels the `SKY_CONSOLE_AUTH`-must-be-set prod
  gate. The assertion is about the DATA backend's replica scope — independent of the session store,
  so a single-instance app using postgres SESSIONS for durability is not false-positive-fatal'd.

- **NB-A (over-notify) — setup-race is AT-LEAST-ONCE, not exactly-once.** `EmbeddedBackend.Query`'s
  `eng.Snapshot()` picks a FRESH readTs (`pebble_engine.go:334-350`); the pinned-readTs read
  (`snapshotAt`) is off the `Engine` iface, so baseline can't pin step-2's readTs → a commit in the
  overlap is double-delivered. **v3:** relax §7's "exactly once" to "AT-LEAST-once; `liveInto`/`watch`
  apply is IDEMPOTENT (pk-keyed), so a double-delivery is a no-op." Still no miss (union covers all).
- **NB-C — the changefeed is NEW work, not a ref port.** `(ref) changefeed.go` carries no coords/no
  tenant; v2's coord+tenant `ChangeBatch` is materially new. **v3:** drop the "proven/ported" framing;
  it gets its own tests.
- **NB-D — `CommitReq.Tenant` must never enter `EncodeChangelogPayload`** (transient routing only).
  **v3:** add an explicit test asserting Tenant never appears in the durable changelog payload.

**Verified-clean by the re-grill:** durable-before-notify (both emit sites strictly post-`Apply(Sync)`
+ `advanceDurableHi`); drop→resync can't permanently under-notify; the blindPut OldIndex fix is real.

### Phase-4a gate (updated with v3)
`-race` tests: (a) two-tenant LOCAL commit-path isolation (no cross-tenant delivery); (b) cross-instance
empty-tenant fail-closed (empty tag → NO broker publish); (c) `CommitReq.Tenant` never in the durable
changelog payload; (d) precise-tier Enter/Leave/Stay incl. autocommit-blind update-out (the A#1 path);
(e) register-live-first setup with no missed commit (at-least-once). No realistic-N deferral (NB-1).
