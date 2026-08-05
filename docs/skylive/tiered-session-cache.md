# Tiered session cache — bounded RAM working set + disk backing

Status: **PROPOSED** (to grill before implementing). Motivated by the
sky-lang.org OOM (2026-08-05): the durable session stores hold EVERY session
within the TTL window (30m) in an in-RAM `memCache`, so sustained cookie-less
bot/crawler traffic (each request mints a ~37 KB `liveSession`) piles up
(rate × 30m) sessions in RAM → OOM on a small VM. Locally reproduced: RAM tracks
all-within-TTL; sessions DO reap (not a leak); sqlite doesn't help today because
its memCache holds the live pointers too.

## Idea (user's)

Keep a **bounded in-RAM working set**: evict a session's live pointer from
memCache after a short **idle-evict window** (~5m), but keep its blob on disk
(sqlite/bluedb) until the full TTL; **fall back to disk on a memCache miss** and
resurrect. RAM then tracks *active* sessions (SSE-connected + touched-within-5m),
not *all-within-30m-TTL* — a near-fixed memory cap, with unchanged 30m
durability. Only durable stores (sqlite/bluedb/postgres/redis) can do this;
memory-only has no backing.

## The building blocks already exist (recon, exp/bluedb)

- `memCache map[string]*liveSession` + `memMu` RWMutex on every durable store
  (`live_store_bluedb.go:33`, `live_store.go:440`, postgres/redis same).
- **`markDone()`** (`live.go:2369`) = pure goroutine/handle teardown (close done,
  cancel subs, teardownReactive, close streams/sockets) — **does NOT touch the
  disk blob.** So "stop goroutines, keep blob" = `markDone()` + evict from
  memCache WITHOUT `db.Delete`. Clean split.
- **SSE-connected gate:** `hasSSEConnOtherThan("")` (`live.go:6836`) → true iff
  `len(sseConns)>0`. Never evict an SSE-connected session.
- **lastSeen** (`live.go:2151`, atomic) touched on Get-hit (under the RLock),
  Set, SSE heartbeat (`live.go:6415`), dispatch. SSE-connected → never idle.
- **Reload → re-establish IS wired:** `decodeSession` (`live_store.go:1158`)
  rebuilds model+identity, leaves prevTree=nil / handlers={} / fresh channels /
  no goroutines. `handleSSE` re-establishes via `startReactive` (Phase 2b,
  `live.go:6197`, idempotent) + `ensureSSERelay` (sync.Once). So a disk-reloaded
  session is safe for a fresh request + re-spins its loops on SSE connect.
- reap runs every 60s (hardcoded, all stores).

## The design

1. **New knob** `SKY_LIVE_IDLE_EVICT` (default ~5m), mirrors `SKY_LIVE_TTL`
   plumbing; `idleEvict time.Duration` field on each durable store + a
   `Live_withIdleEvict` kernel. `0`/unset-in-dev may disable (keep current
   behaviour) — decide in grill.
2. **Idle-evict pass in `reap`** (runs each 60s tick, alongside the existing TTL
   reap): for a memCache entry where `now - lastSeenTime() > idleEvict` AND
   `!hasSSEConnOtherThan("")` AND still within the full TTL (not reap-expired):
   - encode+persist the current model under `sess.mu` (reap already does this,
     `live_store_bluedb.go:231`),
   - `sess.markDone()` (tear down goroutines),
   - `delete(memCache, sid)` **without** `db.Delete` (blob stays until TTL).
3. **Fix the Get-miss re-insert gap (the hard part).** Today Get-miss decodes
   from disk but returns an ORPHAN (does not re-cache) — fine for the current
   "miss == reaped/gone" world, WRONG for eviction (an evicted session that's
   accessed must become the ONE shared live pointer again). Change Get-miss to,
   under `memMu.Lock` with double-checked locking, decode + insert into memCache
   (single-flight): the first caller decodes + inserts; a concurrent caller that
   arrives finds it already inserted and uses that — so two concurrent requests
   for an evicted session share ONE resurrected liveSession, not two split-brain
   copies.

## GRILL OUTCOME (2 adversaries) + MANDATORY FIXES

Verdicts: concurrency **RACY (5)**, RAM-bound **DOESN'T DELIVER for bluedb**.
Design direction holds; these fixes are non-negotiable.

**Concurrency (all must ship):**
1. **Re-validate the gate atomically at the delete.** The evict decision
   (lastSeen idle + no-SSE + still-cached) MUST be re-checked under the SAME
   `memMu.Lock` that deletes from memCache — taking `sseConnMu` there
   (`memMu → sseConnMu`, acyclic). Never decide under RLock and delete later.
   Closes: reap `markDone`-ing a session in the `Get→registerSSEConn` window
   (stranded-SSE-on-dead-session) + evicting a just-touched session.
2. **Generation-safe eviction.** Add `evicted atomic.Bool` to `liveSession`; set
   it under `memMu.Lock` at evict (before releasing). `Set` checks it under
   `memMu` and SKIPS the memCache re-insert for an evicted pointer; async
   producers (`runPerformBody`, `Time.every` tick, subscriber dispatch, reactive
   refresh) test `evicted`/`done` before `persistSession`. Closes: async `Set`
   resurrecting the `markDone`'d corpse → split-brain / lost updates.
3. **Lock-order invariant: reap holds at most ONE of {`memMu`, `sess.mu`}.**
   `encodeSession` (needs `sess.mu`) and `markDone` (takes `sess.mu` via
   teardownReactive) run ONLY with `memMu` released. Order: persist the fresh
   blob under `sess.mu` (no memMu) → `memMu.Lock` re-check + set `evicted` +
   `delete` → Unlock → `markDone`. Closes the `memMu→sess.mu` vs `sess.mu→memMu`
   deadlock.
4. **sqlite Get-hit touches lastSeen OFF the RLock** (`live_store.go:514` RUnlock
   before touch) — move the touch back inside the RLock for parity; fix #1's
   re-check-under-Lock is what actually protects it.
5. **Stale-blob overwrite:** reap `db.Put`s outside `sess.mu`; gate the evict
   persist on the generation/no-change-since-encode so a concurrent dispatch's
   newer blob isn't clobbered.

**RAM-bound (must ship):**
6. **Skip encode-fail sessions.** If `encodeSession` errors (non-gob-encodable
   Model — the memCache-only fallback), do NOT evict: keep it in memCache to the
   full TTL (else idle-evict destroys the only copy at 5m — a durability
   regression). Evict ONLY a session with a confirmed fresh persisted blob.
7. **`touchLastSeen()` to NOW on resurrect** (Get-miss re-insert) — `decodeSession`
   seeds lastSeen from the blob (stale), so a resurrect that forgets to touch is
   born-idle → re-evicted next tick (thrash).
8. **Persist fresh lastSeen at evict** so the 60s TTL reap doesn't delete the
   disk blob under a resurrectable session.

**bluedb caveat (documented limitation, not a blocker):** BlueDB is a
RAM-resident engine (`bluedb/db.go:88` `mem map`), so an evicted session's blob
stays in `db.mem` until TTL — bluedb-as-session-store gets only the ~3.4×
liveSession-object win, not the fixed cap. **sqlite / postgres / redis get the
real near-fixed cap** (blob on disk / external). Recommendation: memory-
constrained deployments use `SKY_LIVE_STORE=sqlite` (or redis/postgres) for
sessions. Target apps (sky-lang.org, darraghstudio) already do.

**New cap (sqlite/pg/redis), given the fixes:** RAM ≈ `L × (SSE-connected ∪
touched-within-idleEvict)` — decoupled from TTL. ~5.5× less than all-within-30m
for the bot case.

## Correctness surface (the grill targets)

- **G1 — split-brain on concurrent Get-miss.** Two requests for an evicted
  session must resurrect ONE shared liveSession, not two (else each spawns its
  own goroutines + persists over the other = lost updates). The double-checked
  `memMu.Lock` re-insert is the fix — is it airtight across all durable stores,
  and does it deadlock against anything (encode under sess.mu inside memMu)?
- **G2 — eviction vs. in-flight request.** A request that took the live pointer
  (Get-hit, touched lastSeen under RLock) then uses it outside the lock. Can reap
  evict + markDone it mid-use? The "touch under RLock" + eviction-needs-Lock
  serialization means a completed Get bumps lastSeen so eviction skips — but is
  there a window (long non-SSE request > idleEvict) where markDone fires under an
  active dispatch? Consequence + guard.
- **G3 — markDone-then-resurrect.** After eviction, the next Get decodes a FRESH
  liveSession (new done channel). Confirm we never reuse the markDone'd pointer,
  and nothing holds a stale reference that would resurrect-with-dead-goroutines.
- **G4 — SSE connect racing eviction.** handleSSE registers an SSE conn +
  startReactive; reap evicts on "no SSE". If an SSE registers just as reap
  evaluates "no SSE", could we evict a just-connected session (killing its SSE)?
  Ordering of registerSSEConn vs the eviction's SSE check.
- **G5 — thrash.** A session that flaps just under/over the 5m window on each
  request → repeated decode/markDone/re-decode. Bounded? Cost of a resurrect
  (decode + gob) per flap.
- **G6 — does it actually bound RAM** for the bot case, and what's the new cap
  formula (SSE-connected + touched-within-idleEvict)? Any path that still holds
  all-within-TTL?
- **G7 — memory store** (no disk): must be a NO-OP (can't evict-with-fallback).
  Confirm the change is gated to durable stores only.
