# ADR-001 — Sky.Live sessions: bolt-on gob store vs unified-engine collection

**Status:** accepted. Funnel done (main win). Sessions-as-collection = non-urgent roadmap; Redis decided non-breaking (kept as escape hatch). No open user decision.
**Date:** 2026-08-07. **Branch:** feat/bluedb.

## The question
"Is the architecture just wrong — did we make a foundational mistake again, like we
did on exp/bluedb before starting fresh here?"

## Verdict
**The core engine (Phase 1–4) is right and independently verified** — unified
Pebble/MVCC engine, real SSI serializable transactions, one Persist API across
embedded/sqlite/postgres, query-scoped commit-path reactivity. No change there.

**The Phase-5 *session* work was band-aided — a localised recurrence of the
exp/bluedb "bolt-on over separate backends" mistake.** The Sky.Live session Model
lives in a **gob-blob session store** (memory/sqlite/redis/postgres) that is
**separate from the unified BlueDB engine**. Because it isn't on a durable-by-default
data path, Phase 5 accreted workarounds:

| Mechanism | Verdict | Detail |
|---|---|---|
| **5d persist-at-N-sites** | **band-aid → FIXED** | No durable-by-default write path, so every ack site had to remember `store.Set`. The manual audit proved it error-prone (missed 4 paths, then a per-event window). Replaced by the single **persist-before-ack funnel** (commit e1f6eaf2). |
| **B3 tripwire** | **band-aid → FIXED** | Was a test to catch a forgotten persist because there was no structural funnel. Now the tripwire enforces the *funnel is the sole sender* — structural, not a fragile count. |
| **5c version envelope** | **reasonable, not purely a band-aid** | The session Model is app-specific `any`; persisting an opaque Model durably across schema changes genuinely needs EITHER version-safety (the envelope) OR a typed schema-codec. The envelope is a fine choice; the codec alternative (sessions-as-typed-collection via the app's Model codec) is a larger integration with only incremental ROI for sessions. Keep the envelope; revisit only if sessions become typed collections. |

Honest scope: **the real band-aids (5d/B3) are now fixed with the funnel — the main
correct-direction win.** 5c stays. Full sessions-as-collection is a worthwhile
*roadmap* item (config unification, engine-native durability), not an urgent fix.

## The correct architecture (goal #1's actual vision: "the session Model IS a collection")
- The session Model is a row in a `_sky_sessions` collection via the Persist API.
- **One dispatch persist point** (the funnel): mutate → persist (durable, before ack)
  → ship. The commit *is* the persist.
- Schema, migration, reactivity, ACID inherited from the engine. One `[data]` config.
  One code path. The 5c/5d/B3 band-aids dissolve.

## What's been done (this correct-direction step)
**The persist-before-ack FUNNEL** (`persistAndShipFrame`, commit e1f6eaf2). All async
ack paths ship through one helper that persists first; B3 is now structural (the
funnel is the sole raw sender, tripwire-enforced). This dissolved the 6-site band-aid
AND fixed a per-event window the manual audit had missed. Crucially, the funnel is
**backend-agnostic**: it is the exact seam to make sessions-a-collection — swap what
"persist" means *inside the one helper* (`store.Set` → a Persist commit on the
session row), touching nothing else.

## Blockers (grilled) for full sessions-as-collection
1. **Redis (the one genuine product decision).** BlueDB has no Redis backend. Redis
   is today's *fast cross-instance* session store. Sessions-as-collection covers
   single-instance (embedded) + shared (postgres); Redis-fast-shared would need
   either a BlueDB Redis backend (future) or Redis kept as a special-case session
   store. **Decision needed:** drop Redis-fast sessions in favour of postgres (simpler,
   one model) OR keep Redis as an escape-hatch session backend alongside the collection
   default. (Postgres covers correctness/scale; Redis is a latency optimisation.)
2. **Risk.** Replacing a working core subsystem. Mitigated by the funnel: swap only
   the persist implementation behind one helper; the in-memory session ownership,
   per-session mutex, and multi-tab fan-out invariants are untouched (the collection is
   the durable backing, exactly as the gob store is today).
3. **Arbitrary Model shapes.** gob accepts anything; a collection needs `Codec.auto`.
   TEA Models are typed records ⇒ derivable; session-unsafe shapes (func/chan) are
   already rejected by `validateSessionValue`. Not a blocker.

## Decision
- **Done (this is the main correct-direction win):** the persist-before-ack funnel —
  the 5d/B3 band-aids are gone, replaced by one structural persist point.
- **Roadmap (not urgent):** back the funnel's persist with a BlueDB `_sky_sessions`
  collection + fold session config into `[data]`. This is NON-BREAKING: add the
  collection-backed store as the new default; keep memory/sqlite/redis/postgres as
  legacy/escape-hatch backends (so no existing app breaks, and the Redis question
  below never forces a removal). ROI is incremental (config unification + engine-native
  durability); the funnel already delivered the durability correctness.
- **5c envelope stays** — it's legitimate version-safety for an opaque app Model.
- **Redis (decided, non-breaking):** do NOT drop it. Keep it as an escape-hatch
  session backend; the collection is the unified default. If Redis-latency at scale
  ever justifies it, add Redis as a first-class Persist backend (unified), not a
  session special-case. No user sign-off needed — nothing is removed.
