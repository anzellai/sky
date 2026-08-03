# Autonomous mandate — BlueDB Phase 1 (steps 1–2)

Set: 2026-08-03. Branch: `exp/bluedb`.
(Supersedes the COMPLETE v0.19.8 coverage-hardening mandate — see memory
[[coverage_hardening_mandate]].)

## User's goal (verbatim — the authority on "done")

> "ok please follow your suggested plan, in fully autonomous mode"
> "remember to grill the design + implementation each phase"

The suggested plan the user approved (verbatim from the assistant turn):

1. **Finish the storage engine** — snapshot + WAL truncation (bounds recovery
   time & disk growth; today it replays the whole WAL). Pure Go, self-contained.
2. **Runtime backend** — make BlueDB a Sky.Live session-store driver
   (`SKY_LIVE_STORE=bluedb`) — the smallest real integration, proves persistence
   end-to-end with zero new API.

## Hard rules

1. **Grill design + implementation EACH phase** (user, explicit). Before/after
   implementing a phase, run a fresh-context adversarial grill against the design
   AND the code — attack crash-window correctness, races, torn writes, seq skip,
   TTL/expiry, interface conformance, prod fallback semantics. Fix what it finds
   before committing.
2. Engine unit tests green incl. `-race`. The session-store driver exercised by
   an ACTUAL Sky.Live example running with `SKY_LIVE_STORE=bluedb` (persist across
   a restart), not just a unit test.
3. **Do NOT touch the compiler kernel registry** this pass (that's step #3,
   deferred). Runtime-only + a store-driver wiring.
4. No-deferral (§4): a real bug the grill/tests surface is fixed at root cause.
5. Commit at each phase boundary; push to `exp/bluedb`.

## Definition of done

Both steps implemented, grilled, tested (engine `-race` green; a real Sky.Live app
persists sessions through a restart via BlueDB), committed, pushed. One end
summary.

## Progress ledger

- [x] Phase 1 — snapshot + WAL truncation (engine core `runtime-go/bluedb/`).
      GRILLED (fresh-context): CRITICAL mid-batch-write-error rollback bug found
      + fixed (F1) + F2/F4/F5 hardening. 18 tests green incl. -race. Commit
      2f6fc9ec.
- [x] Phase 2 — BlueDB Sky.Live session-store driver (`SKY_LIVE_STORE=bluedb`).
      GRILLED (fresh-context): 5 real regressions vs sqlite found + fixed —
      Ping-lies-on-sealed-engine (readyz, HIGH), reap-TOCTOU + dead read-slide
      (active session lost on restart, MEDIUM), Get-returns-torn-down-session
      (F4), Close-doesn't-join-cleanup (F5). E2E VERIFIED: 09-live-counter with
      SKY_LIVE_STORE=bluedb — 30 increments persisted through a full server
      restart (fresh session=0), readyz reflects health.

MANDATE COMPLETE — both phases implemented, grilled, fixed, tested/e2e-verified.

## Extension (user, 2026-08-03): "keep going fully autonomous until e2e integration works"

App-level BlueDB integration end-to-end (persistence first; reactive is a later
phase). Grill + e2e-verify each step.
- [x] A: `[bluedb]` sky.toml section → selects the bluedb session store
      (section-level: presence selects unless `embedded = false`). E2E: persists
      across restart, no env. Also `[live] store = "bluedb"` already worked.
- [x] B: `Live.autoBlueDB` — PURE SKY over the existing `withStore "bluedb"`
      builder, NO compiler-kernel work. E2E: `config {...} |> Live.autoBlueDB`
      persists across restart, no env.
- GRILLED (fresh-context): fixed F1/F3 (code-set store silently beat
      `SKY_LIVE_STORE` env → deploy-override broken; now env > cfg, verified: env
      override → memory, no persist) + F2/F4 ([bluedb] edge cases → section-level
      mapping + unit test). Docs: sky-toml.md `[bluedb]`, README roadmap.

INTEGRATION WORKS: three opt-ins (`[live] store`, `[bluedb]`, `autoBlueDB`), all
e2e-verified persisting across a restart. Reactive scope-sync (multiplayer) is
the next phase — the seam is `autoBlueDB`'s call site.
