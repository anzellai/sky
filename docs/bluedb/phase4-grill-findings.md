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
1. **Phase-2 correctness patch (now):** blindPut emits OldIndex + failing→passing regression +
   re-run the Phase-2 conformance suite. Ship as its own commit (fixes shipped code).
2. **Revise the Phase-4 design** folding A#1/A#2/B#1/B#2/B#3 + NB-1/2/3 — especially the rt-layer
   fan-out re-architecture (changefeed + identity-stamped publish). Re-grill or Judge the revised
   design.
3. Only then implement Phase 4a (with the two-tenant isolation test in its `-race` gate).
