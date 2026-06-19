# Active autonomous goal — v0.17 Sky compiler architectural close

**Status:** LIVE since 2026-06-19.
**Branch:** `feat/v0.17-fully-typed-codegen`.
**Mandate:** persists across compactions, new sessions, and assistant turn boundaries
until a Judge agent verifies 100% achievement OR the user explicitly revokes it.

## Verbatim goal (the user's words)

> 100% fully typed e2e, if valid sky code is consumed, the type sig
> is 100% correct through to emitted go code. no runtime panics,
> truly if it compiles it works. rock solid + future proof sky
> compiler + 100% soundness for v0.17.

## Concrete disqualification criteria (derived from verbatim goal)

A Judge verdict of "100% ACHIEVED" requires ALL of:

1. **Zero `rt.Coerce` calls in emitted Go for well-typed Sky code.**
   The 317 calls in `examples/26-ui-showcase/sky-out/main.go` must
   be 0 (or only at documented FFI boundaries with a closed proof).

2. **`eraseUndeclaredTVarsInGoSource` band-aid DELETED from
   `src/Sky/Build/Compile.hs`.** Currently still wired as a defensive
   floor at line ~3154. Must be gone.

3. **2 surviving module-level IORefs (`globalCgEnv` +
   `globalGoSigMap`) actually DELETED**, not documented as
   "load-bearing-but-pure". The `getCgEnv` CAF must be gone. All
   ~20 call sites must thread `LowerCtx` explicitly.

4. **`SKY_GOSIG_DIFF=1` produces zero `Anon_R_*` undefined errors**
   on every example in the sweep including the iter-20 fixture.

5. **9 `GoTypeAdt` + `GoTypeRoundTrip` parity tests PASS** (currently
   failing under "spec backlog" framing per task #653).

6. **Every active limitation in `CLAUDE.md ## Active limitations` is
   either CLOSED or has explicit user sign-off to remain open**.
   Specifically #7 (zero-arg call shape) and #8 (non-TCO O(N) stack).

7. **Cycle 6 umbrella (#383) "If it compiles, it works credibility
   close" CLOSED.** This is the user's literal phrase made into a
   task.

8. **A property-based fuzzer exists** that generates random
   well-typed Sky programs and asserts `sky build && ./sky-out/app`
   doesn't panic. Run for ≥10,000 iterations clean before close.

9. **All currently in_progress / pending v0.17 umbrella tasks
   CLOSED**: #383 #595 #644 #659 #660 #656 #654 (reopened) #661
   (reopened).

10. **An independent Judge agent (fresh context, no prior bias)
    confirms the above in a single verdict with no "but/except/
    however/caveat".**

## Stop conditions (per CLAUDE.md Non-Negotiable #0)

- Judge returns "100% ACHIEVED + VERIFIED" → final report → stop.
- Implementation blocker → describe concretely + PushNotification
  user describing what direction is needed → wait → resume on
  user response.
- User explicitly revokes the mandate.

## What CANNOT close this

- "My narrow lens 3-agent verification passed" — that's not the
  goal verifier.
- "Iter N criteria all green" — those are my criteria, not the
  goal.
- "Documented as load-bearing-but-pure" — that's not deletion.
- "Spec backlog" / "technical debt" / "pre-existing" — disqualified.
- "Cabal test + example sweep green" — gates, not the goal.

## Workflow (per CLAUDE.md Non-Negotiables #0 / #0.1 / #0.2)

Each iteration:
1. Spawn fresh **Judge agent** → get verdict + ordered gap list.
2. **Architect agent** plans the closure batch for top gaps.
3. **≥2 adversarial grillers** in parallel attack the plan BEFORE code touched.
4. Plan refined if grillers flag blocking concerns.
5. **Executor agents** implement (parallel where independent).
6. Targeted spec only during execution — NO full suite mid-batch.
7. ONE full milestone verification at end of batch.
8. Re-spawn Judge → re-verdict → loop if NOT 100%.

NO `ScheduleWakeup` between iterations — workflow runs to completion
in one invocation, I re-invoke immediately on result.

## Push policy

- Local commits liberally on `feat/v0.17-fully-typed-codegen`.
- Push to `origin` ONLY at meaningful milestones:
  - Judge-verified phase close (e.g. T1-leak architectural close done)
  - Umbrella task closed (#383, #595, #644, #660, ...)
  - User-requested checkpoint
  - 100% achieved (final close)
- This file's commit IS such a milestone (it's the discipline
  foundation for everything that follows).
