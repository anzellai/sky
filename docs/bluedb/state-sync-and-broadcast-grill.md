# Grill: state-model-sync + optimal broadcast (2026-08-04)

Three adversarial agents: (A) state-model-sync reliability/perf/security, (B) optimal
broadcast architect, (C) broadcast design-space attacker. Reactive query is settled
opt-in (prior grill). This grill: is the STATE-SYNC foundation solid, and what's the
OPTIMAL broadcast?

## PART 1 — State-model-sync (session Model persistence) — NOT solid; CRITICAL bugs

The layer exists (SessionStore, 5 backends, gob blob, fsync'd WAL) and the SYNCHRONOUS
request path is crash-consistent (mount/event: dispatch→render→Set→reply). But:

- **R1 CRITICAL — async Model mutations are NEVER persisted.** Only 3 `store.Set` sites
  (mount live.go:4212, user-event :4576, SSE-resync :6239). Cmd.perform completion,
  Time.every tick, pub-sub delivery, WebSocket delivery, AND the reactive
  refresh (live_reactive.go) all mutate sess.model + ship an acked SSE frame WITHOUT
  persisting → guaranteed loss on restart for async/real-time apps. Contradicts
  durability.md ("acked updates never vanish"). CONFIRMED by grep.
- **R2 HIGH — off-lock encodeSession races async dispatch → unrecoverable crash.** Set at
  :4576 runs AFTER sess.mu.Unlock (:4573); encodeSession reads sess.model (+ reflect
  walks) off-lock while an async goroutine reassigns sess.model under sess.mu → Go data
  race → `fatal error: concurrent map read and map write` (not recoverable by
  dispatch's recover). Same at SSE-resync (:6239) + bluedbStore.reap. An app with a
  Time.every ticker + any click is one scheduling window from crashing the SERVER.
- **R4 MED — breaking schema change → silent full-session RESET.** gob type-change →
  decode err → treated as new session → init wipes Model; only a server log; withMigrate
  runs ONLY on successful resume (can't help). Field RENAME → silent zero, no error/log.
  No version tag on the blob.
- **R5 — multi-replica.** bluedb (flock LOCK_EX) + sqlite are single-writer → 2nd replica
  FATAL crash-loop. Only redis/postgres multi-replica-safe. memory fails-loud in prod (good).
- **P1 HIGH — blind whole-Model write-through per event, no dirty-check/debounce.** gob
  encode + 2 reflect walks + fsync per keystroke (onInput). Inside app.locker (serializes).
- **P2 — reap re-encodes every fresh session per 60s sweep (O(N), off-lock → feeds R2).**
- **S1 HIGH — plaintext gob at rest, no encryption.** Auth tokens/PII in the Model sit
  cleartext in the store file/Redis/PG. No encryption knob.
- **S3 MED — no tenant scoping on session keys** (sid-only isolation; a stolen sid → full Model).
- **S4 — cookie Secure only in iframe mode** (not __Host-, plain HTTP if no upstream TLS).

**Verdict: NOT solid enough to build reactivity on. Fix R1 + R2 + P1 first.**
Priority: R2 (crash) ≥ R1 (data loss) > P1 (amplification) > S1 (at-rest) > S3.

## PART 2 — Optimal broadcast — the RE-QUERY is the asset, not the cost

Architect proposed "Sharded-Scope Reactive" (D1 tenant-topics → D2 overlap → D3 cache →
D4 incremental). Attacker REJECTED the fancy layers, kept the narrowing ones. Synthesis:

**Ground truth constraining every design:** the change-feed carries only the NEW value
(not old); publish is process-global (no session, no old value); subscribe is NOT
authorized against identity today; the broker is fire-and-forget drop-on-full (no
replay). Self-healing today = "any nudge → full re-query heals all misses."

Candidate verdicts:
- **T1 tenant-scoped topics `coll:field:value`:** viable-with-caveats, an OPTIMIZATION not
  a boundary. Holes: (Q1) tenant-field CHANGE → publishes to NEW scope only (feed lacks
  old value) → old-tenant session shows a stale row until an unrelated write. (Q2)
  non-indexed/compound/or_/range predicate → no single scope key → falls back to
  collection topic → oracle returns. So T1 narrows fan-out + closes the oracle for
  equality-on-indexed queries ONLY, with a mandatory tenant-safe re-query fallback.
- **T2 record delta on scoped topic:** BROKEN security — every scoping imperfection →
  content leak; tenant id in Redis channel name; plaintext across Redis. (This is the
  exact leak already fixed by the nudge.) DON'T carry bodies on shared topics.
- **T3 incremental result-set maintenance:** BROKEN — a dropped/reordered delta silently
  diverges the list (loses self-healing). The RIGHT slice is keyed/subtree RENDERING of
  the re-query result (cut V+D O(tree)→O(change)), NOT incremental set maintenance.
- **T4 shared result cache:** BROKEN for tenant-scoped OLTP (illusory sharing since bound
  filters differ per session; amplified-leak on mis-key; cold-when-hot). Dashboards only.
- **T5 overlap narrowing:** viable, safest (pure narrowing → degrades to over-refresh
  never under-skip). Reliably skips DELETE/UPDATE of offscreen rows; CANNOT skip
  potential-inserts without a body. Needs resultPks maintained (currently DISCARDED at
  reactiveFoldFromResult live_reactive.go — returns fold only, drops the pk list).
- **Cross-instance:** keep nudge + best-effort Redis pub/sub (correct BECAUSE nudge
  self-heals); Postgres LISTEN/NOTIFY only as optional external-writer bridge (8KB cap →
  nudge-only). Redis Streams / log-poll only justified for deltas — which are rejected.

**Attacker's ranked build order (max security+scale, least DX+risk):**
1. **Identity-derived re-query filter, framework-enforced (SECURITY, first).** The real
   hole isn't topic granularity — it's that the tenant boundary lives in app query code
   (model.me) with NO framework enforcement + NO identity check at subscribe. Add a
   `scope` declaration; framework injects the value from verified SessionIdentity
   (identityValid-gated) into the re-query so a session PHYSICALLY can't query outside its
   identity. Closes the subscription IDOR + forged-model.me. No topic change. One optional
   arg — two-name surface intact.
2. **Wire T5 overlap (delete/update-of-offscreen skip).** Stop discarding pks; maintain
   resultPks; branch on bluedbChangeAffectsQuery. Pure narrowing, safe degrade, no API.
3. **Keyed/subtree rendering of the re-query result.** Cut V+D. "T3 done right." No API.
4. **T1 tenant-scoped topics — equality-on-indexed only, mandatory collection fallback
   (optional, after #1).** Scope from SessionIdentity. Publish old-scope too (or
   belt-and-suspenders collection nudge) for Q1. One optional scope/index declaration.

**DON'T build: T2, T3-incremental, T4.** Thesis: narrow WHO re-queries (T5) / cheapen the
PAINT (keyed render) / harden WHO's allowed to re-query (identity scope) — additive+safe.
Deleting the re-query (T2/T3/T4) trades self-healing safety for marginal savings.

## The scope decision this forces
The FOUNDATION (session store) has critical PRE-EXISTING bugs (R1 data loss, R2 server
crash) affecting ALL Sky.Live apps — arguably higher priority than the reactive/broadcast
layer on top. Recommended sequence: (1) fix R2 crash + R1 async-persist + P1 dirty-check
(state-sync foundation), (2) identity-derived reactive scope (broadcast security), (3) T5
overlap + keyed render (perf), (4) T1 topics (optional).
