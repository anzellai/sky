# v0.17 Full Architectural Close — Execution Roadmap

> **Scope**: full close per `.claude/AUTONOMOUS_GOAL.md` (10 criteria) PLUS user direction
> 2026-06-28: "known bugs fixed so all examples run correctly with LSP + sky compiler fully
> working."
>
> **Companion docs**:
> - `.claude/AUTONOMOUS_GOAL.md` — immutable mandate (verbatim goal + 10 criteria)
> - `docs/v0.17/stabilization-postmortem.md` — bottleneck analysis (the 3 linked problems)
> - This doc — session-by-session execution plan
>
> **Branch**: `feat/v0.17-pure-sound-codegen` @ `63207ed4` (postmortem committed)

---

## Definition of Done (DoD)

A tag of `v0.17.0` requires ALL of:

**A. The 10 criteria from `AUTONOMOUS_GOAL.md`** (verbatim):
1. Zero `rt.Coerce` in well-typed Sky emission (or documented FFI-boundary only)
2. `eraseUndeclaredTVarsInGoSource` band-aid DELETED
3. `globalCgEnv` + `globalGoSigMap` IORefs DELETED (not "load-bearing-but-pure")
4. `SKY_GOSIG_DIFF=1` zero `Anon_R_*` undefined on every example
5. 9 `GoTypeAdt` + `GoTypeRoundTrip` parity tests PASS
6. Every active CLAUDE.md limitation CLOSED or user-signed-off-open
7. Cycle 6 umbrella (#383) "If it compiles, it works" CLOSED
8. Property fuzzer ≥10k iter clean
9. All in-progress v0.17 umbrella tasks closed: #383 #595 #644 #659 #660 #656 #654 #661 #677
10. Independent Judge agent verdict: PASS with no "but/except/however"

**B. User direction 2026-06-28** (this session's expansion):
11. **All 39+ examples build + run clean** including 05/11/13 (currently broken)
12. **Bundled console regenerate green** (`scripts/regenerate-console.sh` no-op)
13. **LSP fully working** — completion / hover / references / call-hierarchy / signature
    help all functional against the modern compiler API
14. **Sky compiler "fully working"** — sweep + verify-all-web + verify-cli all green

**C. Stability gates** (mine, derived from this session's failure mode):
15. **No spec encodes bug-compatible behavior** — every cabal-test spec asserts the CORRECT
    output, not the currently-emitted-but-wrong output
16. **Architectural docstring at every IORef + scopeStateRef field** — single-writer /
    monotonic invariant stated + spec gate enforcing it (matches the
    `globalAnonRecords` precedent at `Compile.hs:Phase A iter 0`)

---

## Where we are right now (2026-06-28 state)

| Criterion | Status | Evidence |
|---|---|---|
| 1 (rt.Coerce = 0) | **154/317** on ui-showcase | Iter 66 measurement; floor 209 after sealed-iface flip |
| 2 (band-aid delete) | **NOT done** | `eraseUndeclaredTVarsInGoSource` still wired |
| 3 (globalCgEnv delete) | **Phase A iter 6 in-flight** | 17 Class A reader sites, partial migration |
| 4 (SKY_GOSIG_DIFF clean) | **NOT verified** since iter 30 | Unknown current state |
| 5 (GoTypeAdt + RoundTrip) | **9 pending** per task #653 | Spec backlog |
| 6 (limitations) | **All 10 closed or marked deferred** | CLAUDE.md "Active limitations" |
| 7 (Cycle 6 #383) | **in_progress** | Umbrella tracker |
| 8 (fuzzer) | **DONE** | `WellTypedFuzzerSpec` 10k clean |
| 9 (umbrella tasks) | **2 of 9 closed** | Tasks tracker |
| 10 (Judge PASS) | **NOT achieved** | Latest verdict: REFRAMED-CLOSE only |
| 11 (39 examples) | **36/39 pass** | 05/11/13 fail typed-FFI |
| 12 (console regen) | **FAIL** | Store → rt.SkyStore |
| 13 (LSP working) | **assumed working** | Not audited this session |
| 14 (full sweep) | **partial** | cabal test passes; example sweep 36/39 |
| 15 (no bug-encoding specs) | **NOT done** | 10 specs encode env-free behavior |
| 16 (IORef contracts) | **partial** | Only `globalAnonRecords` has spec gate |

**Honest read**: ~40% of the way through the close. Sessions of focused, sequenced work
required.

---

## Session-by-session plan

Each session ends with a verified gate before the next starts. Per CLAUDE.md §0.4: decide →
plan → execute → verify. Per §0.1 + §0.2: batch commits to milestone boundaries, one push +
CI run per session.

### Session 1 — Renderer + dispatch fragmentation map (NO CODE, just survey)

**Goal**: build the dispatch coverage matrix that PR-22 should have produced but didn't. Map
every `Can.VarKernel` arm across the 4 exprToGo* functions. Identify gap cells.

**Method**:
- Architecture-Consult agent reads `docs/architecture/sky-compiler-architecture.md` +
  `Compile.hs:lines 90-150 + 480-600` + every `exprToGo*` function. Output: a Markdown table
  with rows = call shape (VarKernel stdlib / VarKernel FFI / VarTopLevel / VarLocal / Call),
  cols = entry function (exprToGo / exprToGoTyped / exprToGoExpect / exprToGoExpectGo), cells
  = arm location or "GAP".
- 3-way diff agent run: same fixture (`Firestore.queryDocuments q ctx`) through each entry
  function, trace which arm fires, identify where they diverge.
- Deliverable: `docs/v0.17/dispatch-coverage-matrix.md` — the truth table.

**Gate**: the matrix is complete + reviewed. NO code changes yet.

**Why first**: the previous session's failure mode was guessing-then-breaking. This session
removes the guessing.

**Estimate**: 1 session.

---

### Session 2 — Fix the typed-FFI dispatch gap (Problem B → criteria 11)

**Goal**: examples/05, 11, 13 build clean. Bundled console drift check Stays Red (intentional —
that's Problem A, Session 3).

**Method**:
- Per the dispatch matrix from Session 1, identify the missing FFI arm in the wrong entry
  function (probable: `exprToGoTyped`'s VarKernel arm at line 20570, OR a dep-emit path).
- Add typed-FFI dispatch arms there, mirroring `exprToGo`'s 14116/14134 arms.
- Add `Sky.Build.TypedFfiDispatchSpec` — multi-module fixture per call shape (0-arg Unit,
  1-arg, 2-arg, N-arg, partial app, case-subject, let-binding RHS).
- Each spec uses an FFI fixture with a kernel that has only a `T` wrapper (no bare wrapper).
  Asserts emitted Go contains `rt.Go_<Pkg>_<method>T(` and NOT `rt.Go_<Pkg>_<method>(`.

**Gate**:
- examples/05/11/13 clean build + run
- Existing cabal-test green (no regression in other arms)
- New spec passes
- Push: ONE commit, ONE CI run

**Estimate**: 1-2 sessions depending on how many dispatch paths need the arm added.

---

### Session 3 — Env-thread `solvedTypeToGoViaPipelineFlat` + retarget specs (Problems A + C → criterion 12)

**Goal**: bundled console regenerate green. 10 cabal-test specs retargeted to assert
env-aware (correct) output.

**Method**:
- Migrate all 8-9 callers of `solvedTypeToGoViaPipelineFlat` to take an explicit `cgEnv`
  (NOT read from `scopeStateRef` — that was my Session 0 mistake; explicit threading is
  safer).
- For each caller, identify the correct env at the call site (entry-module env, dep-module
  env, or empty for bootstrap-phase).
- Per spec in {CrossModuleLambdaCollisionC, DepCurrentModuleHint, +8 others}:
  - Read the spec's fixture + current assertion
  - Build the CORRECT env-aware output by hand (or via legacy `safeReturnTypeFullBounded`
    which DOES env-thread correctly)
  - Update assertion + add migration log entry in `docs/v0.17/spec-retarget-log.md`

**Gate**:
- `scripts/regenerate-console.sh` produces clean go-build
- All 10 retargeted specs pass with new assertions
- Full cabal-test sweep green
- `docs/v0.17/spec-retarget-log.md` lists every spec change with reasoning
- Push: ONE batched commit (renderer migration + spec retarget atomic), ONE CI run

**Estimate**: 2 sessions (1 for the migration, 1 for the spec retargeting + verification).

---

### Session 4 — `globalCgEnv` + `globalGoSigMap` deletion (criterion #3)

**Goal**: the two surviving module-level IORefs DELETED. All ~20 call sites threaded.

**Method**:
- Continue Phase A iter 6 (`getCgEnvFromScope` migration). Remaining ~11 Class A reader
  sites + ~14 Class B reader sites. Plus `globalGoSigMap` migration to `LowerCtx._lc_goSig`
  channel.
- Per CLAUDE.md §0.3 architectural-mechanism citation: each batch cites the §7 lever
  (LowerCtx value-channel + scope-extension method) and §8 floor non-touching proof.
- Add the `scopeStateRef` audit spec gate (already partially exists in
  `Sky.Build.ScopeStateRefAuditSpec`). Extend to enforce: every writer is a single-writer,
  every reader reads-through, no IORef is read post-`importsForced` write.

**Gate**:
- `grep -n "globalCgEnv\|globalGoSigMap" src/Sky/Build/Compile.hs` returns ZERO matches in
  live code (only comments referencing the deletion)
- `Sky.Build.ScopeStateRefAuditSpec` passes with strict contract assertions
- 26-ui-showcase clean build (cgEnv lookups still resolve correctly through scopeStateRef)
- Cabal sweep + example sweep green
- Push: ONE batched commit

**Estimate**: 2-3 sessions (per CLAUDE.md §0.2 N-strikes — 3 prior swap attempts produced
regressions; needs careful architectural mechanism).

---

### Session 5 — Sealed-interface ADT emission (#677 → criterion 1)

**Goal**: rt.Coerce floor on 26-ui-showcase drops from current 154 to ≤30 (or 0 with documented
FFI boundary). The sealed-iface flip on Std.Ui.Element / Std.Html.Html ships.

**Method**:
- Per `.claude/AUTONOMOUS_GOAL.md` lines 68-128 (2026-06-21 user directive — "Authorized
  design (durable)"): replace `type Std_Ui_Element = rt.SkyADT` with real Go sealed
  interface `Std_Ui_Element[Msg any] interface { ... }`.
- ADT variant constructors emit as concrete struct types implementing the interface.
- Pattern matches emit as type-switch.
- Runtime helpers `rt.Field` / `rt.RecordUpdate` redirect through the interface dispatch.
- Per CLAUDE.md §0.2 — this is the multi-session work; do not attempt single-session.
- Run iteratively: flip one ADT per commit, verify ratchet drop, no regression.

**Gate**:
- rt.Coerce count on `examples/26-ui-showcase/sky-out/main.go` ≤ 30 (or documented residual)
- All 39+ examples clean build
- `Sky.Build.RtCoerceBudgetSpec` ratchet updated to the new floor
- Cabal sweep + Playwright + verify-cli all green
- Push: 1 commit per flipped ADT (Std.Ui.Element, Std.Ui.Attribute, Std.Html.Html, etc.)

**Estimate**: 3-4 sessions.

---

### Session 6 — Limitation closures + `eraseUndeclaredTVarsInGoSource` delete (criteria 2, 6, 7)

**Goal**: Limitations #7 (zero-arg call shape) + #8 (CPS list ops) FULLY closed
(StrictHmArityGate live, all CPS specs green). Defensive band-aid DELETED.

**Method**:
- Limitation #7: complete the StrictHmArityGate (4 pending arms — k-a / k-b / u-a / u-b
  wiring in `constrainCall`). Per spec `Sky.Type.StrictHmArityGateSpec` — 4 of 8 already
  live; flip the remaining 4 to live + add the actionable [E2007] diagnostic.
- Limitation #8: verify CPS rewrites on List/Maybe/Result are constant-stack on 1M-item
  inputs. Spec `Sky.Build.CpsStackConstantBoundSpec` covers; ensure ratchet locked.
- Delete `eraseUndeclaredTVarsInGoSource`: this band-aid (Compile.hs:~3154) erases unbound
  TVars to `any` defensively. If criteria 1 + 5 are met, NO unbound TVars should reach
  emission. Delete + verify zero regression via full sweep.

**Gate**:
- All CLAUDE.md "Active limitations" entries marked closed
- `grep eraseUndeclaredTVarsInGoSource src/Sky/Build/Compile.hs` returns 0 hits
- StrictHmArityGate spec all-live (no pending)
- Full sweep green
- Push: 3 commits (one per closure)

**Estimate**: 1-2 sessions.

---

### Session 7 — LSP audit + close (criterion 13)

**Goal**: LSP completion / hover / references / call-hierarchy / signature-help all
functional against the modern compiler.

**Method**:
- Run `scripts/lsp-test-nvim.sh` (per memory note) against a sample of examples.
- Per LSP feature (completion / hover / references / call-hierarchy / signature-help / code
  actions): test against 13-skyshop (largest), 26-ui-showcase (richest typed UI),
  19-skyforum (multi-module).
- Each broken feature filed as a spec gate (`test/Sky/Lsp/<Feature>Spec.hs`) + fixed.
- Per CLAUDE.md §9 — runtime verification on every push.

**Gate**:
- All 6 LSP features functional on sample examples
- New LSP specs cover the verified surface
- Push: 1 commit per fix

**Estimate**: 1-2 sessions (depends on how much regressed silently).

---

### Session 8 — `SKY_GOSIG_DIFF` + GoType parity (criteria 4, 5)

**Goal**: `SKY_GOSIG_DIFF=1` clean on full sweep. 9 GoTypeAdt + GoTypeRoundTrip parity tests
PASS.

**Method**:
- Run `SKY_GOSIG_DIFF=1` on every example. Identify Anon_R_* mismatches.
- Per task #653 — 9 GoTypeAdt / GoTypeRoundTrip tests pending. Audit each, fix the rendering
  divergence.
- Lock via spec gate.

**Gate**:
- `SKY_GOSIG_DIFF=1 scripts/example-sweep.sh` zero divergences
- `cabal test --match GoTypeAdt --match GoTypeRoundTrip` all pass
- Push: 1 batched commit

**Estimate**: 1 session.

---

### Session 9 — Judge verification + tag v0.17.0

**Goal**: Independent Judge agent verdict per CLAUDE.md §0. All 10 + 6 criteria PASS. Tag
shipped.

**Method**:
- 3-agent adversarial gate (per `.claude/AUTONOMOUS_GOAL.md` line 661):
  1. Verbatim-goal Judge: reads `.claude/AUTONOMOUS_GOAL.md` lines 8-13 + verifies the 10
     criteria
  2. User-direction Judge: reads `docs/v0.17/full-close-roadmap.md` DoD + verifies criteria
     11-16
  3. Adversarial Judge: tries to refute the close (per CLAUDE.md §0.4 forbidden-phrases
     check)
- All 3 PASS without "but/except/however/caveat" → tag.
- Per CLAUDE.md `feedback_no_auto_tag_release` — USER tags the release manually after local
  verification.
- I prepare the tag-ready state + CHANGELOG + release notes; user pulls the trigger.

**Gate**:
- 3 Judge verdicts: all PASS
- User manual verification: clean
- Tag `v0.17.0` shipped
- SkyDeploy redeploy per CLAUDE.md §5

**Estimate**: 1 session (mostly verification + waiting).

---

## Total estimate

| Session | Goal | Estimate |
|---|---|---|
| 1 | Dispatch coverage matrix | 1 session |
| 2 | Typed-FFI dispatch gap (Problem B) | 1-2 sessions |
| 3 | Env-thread pipeline + spec retarget (Problems A + C) | 2 sessions |
| 4 | globalCgEnv + globalGoSigMap deletion | 2-3 sessions |
| 5 | Sealed-interface ADT emission (#677) | 3-4 sessions |
| 6 | Limitation closures + band-aid delete | 1-2 sessions |
| 7 | LSP audit + close | 1-2 sessions |
| 8 | SKY_GOSIG_DIFF + GoType parity | 1 session |
| 9 | Judge verification + tag | 1 session |
| **TOTAL** | | **13-18 sessions** |

This matches CLAUDE.md `feedback_v017_FULL_PROMPT` and `feedback_never_settle_for_deferral`
— the user has explicitly said "multi-session/days/weeks is OK if the architecture is right."

---

## Non-negotiable execution discipline

Per CLAUDE.md §0 + §0.4, every session:

1. **Architecture-Consult agent FIRST.** Phase 0 of every workflow reads
   `docs/architecture/sky-compiler-architecture.md`. Cites §6 rt.Coerce origin / §7 lever
   / §8 floor for the proposed tactic. NO tactical work without architectural citation.

2. **Adversarial grill BEFORE implementing.** G1: false negatives? G2: false positives?
   G3: cost-bounded? G4: clean layering? G5: does it close criterion or document partial
   close?

3. **Three-leg soundness stool.** Runtime classification (Go-side test) + emission-time
   (Sky.Build spec) + real-world e2e (example sweep + verify). NO single-leg "proof".

4. **N-strikes circuit-breaker.** 3 failures on the same lever → reclassify, don't retry
   4th. Read `docs/architecture/sky-compiler-architecture.md` §6/§7/§8, escalate to user
   with floor citation if floor-touching.

5. **Spec-vs-fix incompatibility checks.** If a fix would regress N specs, audit which
   specs encode bug-compatible behavior (the 10-spec case this session). Update
   `docs/v0.17/spec-retarget-log.md` with reasoning per spec touched.

6. **Push policy.** ONE push per session, AT the milestone boundary (per CLAUDE.md §0.1).
   Not per-commit. Not per-PR. Reduces CI burn + makes the branch state legible.

7. **Honest reporting.** If a session's work doesn't close its criterion, the report says
   so. The "but/except/however/caveat" gate from CLAUDE.md §0 applies to me too, not just
   Judge agents.

---

## Risk register

| Risk | Mitigation |
|---|---|
| Renderer collapse breaks more specs than the current 10 | Session 1 dispatch matrix surfaces ALL specs touching renderer behavior |
| Sealed-iface flip cascades into runtime panics | Per-ADT flip + ratchet verification per commit |
| globalCgEnv deletion regresses cgEnv reads pre-importsForced | scopeStateRef value-channel single-writer contract spec gate |
| LSP audit surfaces unrelated regressions | Filed as new tasks, fixed in dedicated session |
| Judge agents drift on "but/except/however" | Multiple independent Judges + explicit forbidden-phrases check |
| User's available time runs out before close | Each session is self-contained: branch is shippable as v0.17-rc<N> at any session boundary |

---

## What I am NOT doing this session

Following CLAUDE.md §0.4 "Architecture + planning happen UP FRONT" + the previous session's
failure mode (guess → break → revert):

- **Not writing code this session.** Session 1 = matrix construction. Session 2 = fix.
  Plan first.
- **Not pushing more commits.** Branch is at `63207ed4` (postmortem doc). Wait for user
  green-light + Session 1 start.
- **Not running long-running tests.** No cabal-test + no example sweep. Those run in
  Sessions 2+ at milestone boundaries.
- **Not starting on multiple sessions at once.** N-strikes lesson: bound work,
  verify, then proceed.

---

## What I need from you to start Session 1

Three things:

1. **Confirm the roadmap shape.** Push back on any session ordering / scope you disagree
   with. I'm proposing Session 2 (typed-FFI fix) before Session 3 (env-thread pipeline)
   because Problem B is independent + smaller. Reverse them if you have a reason.

2. **Confirm session budget.** I estimate 13-18 sessions for the full close. That's
   weeks-to-months of focused work. If you need v0.17 shipped sooner with a partial close,
   say so — we'd rescope Sessions 5/7 to v0.17.x.

3. **Authorise the v0.17-roadmap branch protection.** Per CLAUDE.md §0.1 — don't push to
   main, don't force-push, don't tag without your sign-off. Confirm this still holds for
   v0.17.0.

When you green-light, Session 1 starts: I spawn the Architecture-Consult + dispatch-mapper
agents, they produce `docs/v0.17/dispatch-coverage-matrix.md`, then I report findings + we
move to Session 2.

---

*Written 2026-06-28 following user direction: "a [postmortem + plan]" + "full or close to
full arch close, we need known bugs fixed so all examples run correctly with LSP + sky
compiler fully working." Honest scope: 13-18 sessions. Honest first step: stop guessing,
build the dispatch coverage matrix.*
