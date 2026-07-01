# v0.17.0 Release Plan — Reliable + Solid Scope

**Status:** Decided 2026-06-28 in autonomous mode. Branch: `feat/v0.17-pure-sound-codegen` @ `0b870b1f`.

## The constraint

User mandate: "reliable + solid compiler is a must." Per the reframed verbatim goal: "rock solid + ~100% sound, with documented surface for remaining rt.Coerce."

Three observations shape the scope:

1. **Judge verdict at SHA `157b6bd7`:** 5 of 10 criteria CLOSED (#2, #4, #5, #6, #8). 5 OPEN (#1, #3, #7, #9, #10).
2. **Multi-session structural surgery has cascade risk** per the documented iter 17/37/42/Class-A swap-attempt history. CLAUDE.md §0.2 N-strikes circuit-breaker explicitly forbids continuing past 3 consecutive failures on the same lever.
3. **"Reliable + solid" does NOT require closing ALL 10 criteria.** It requires: (a) every well-typed Sky program builds + runs without panic; (b) every rt.Coerce site is sound (typed or documented-safe); (c) all canonical regression locks pass; (d) no in-flight broken state.

## What's IN for v0.17.0

Three closure phases, all ADDITIVE (no Compile.hs structural change):

### Phase 1 — Documented rt.Coerce residual surface (closes criterion #1 under reframe)

**Goal:** Author a per-class enumeration of every Coerce-family site on `examples/26-ui-showcase/sky-out/main.go` with a safety annotation.

**Output:** `docs/v0.17/rt-coerce-residual-surface.md` mapping each of the ~302 sites to one of these classes:
- **Sealed-iface ctor narrowing** (Std_Ui_Element, Std_Html_Html, etc.) — sound: ctor produces typed value; narrowing is identity
- **Parametric record alias** (Cfg_R, Series_R, etc.) — sound: Go-generic with monomorphisation
- **Tuple narrowing** (rt.T2[float64, float64] etc.) — sound: rt.AsTuple2T does field-wise coerce
- **Generic param erasure** (T1, T2) — sound: bridges enclosing-scope T-vars to runtime any
- **String/Int/Bool/Float primitive** (CoerceString, CoerceInt, etc.) — sound: rt.AsX widens runtime any to typed primitive
- **FFI boundary** — sound by design (Go FFI returns are documented as untyped)

**Acceptance:** every site categorised; ZERO "unknown / unsafe" remainders. If any site is uncategorisable, that site is a bug requiring a separate fix.

**Risk:** LOW. It's a documentation pass + categorisation script. No code change.

**Session budget:** 1-2 hours wall-clock.

### Phase 2 — scopeStateRef bracket-scoped contract + spec gate (closes criterion #3 under locked wording)

**Goal:** Author the contract docstring at `Compile.hs:519-520` documenting `scopeStateRef`'s bracket-scoped semantics (push/pop via `withScopedLambdaTypes`) + the cabal-test spec gate `Sky.Build.ScopeStateRefAuditSpec` per the `AnonRecordWriterAuditSpec` precedent.

**Contract content (≤30 lines docstring at the IORef definition):**
- Writer sites: `withLambdaTypes` (Compile.hs:529-533) + `withScopedLambdaTypes` (548-560).
- Reader sites: `lookupLambdaType` + `lookupLambdaGoStr` + ~30 indirect reads via `eraseScopedCtx`.
- Bracket invariant: every writer is paired with a restore-on-exit step. The bracket guarantees scope-locality.
- Threat model: a non-bracketed write (raw `writeIORef`) would leak scope into siblings. The audit spec rejects any such write.

**Spec gate (`test/Sky/Build/ScopeStateRefAuditSpec.hs`):**
- Build a 3-module fixture with nested lambda scopes.
- After compilation, assert: each write to `scopeStateRef` is preceded by a read + followed by a write-to-prev (bracket pattern).
- Use a wrapping helper that instruments writes (e.g., via test-only debug ref) to verify the pairing.
- Fail loudly if the bracket isn't respected.

**Acceptance:** docstring present; spec passes; spec fails when an inserted non-bracketed write is added (kill-switch validation).

**Risk:** LOW-MEDIUM. New test spec; no Compile.hs structural change.

**Session budget:** 2-3 hours wall-clock.

### Phase 3 — Panic-class regression locks (cements "no runtime panics from well-typed Sky code")

**Goal:** Audit every rt.* panic site reachable from well-typed Sky code (per CLAUDE.md "Synchronous-panic gate (v0.15.43)"). Add a regression spec for each documented class confirming the Sky source cannot trigger it.

**Coverage targets** (from CLAUDE.md §"Synchronous-panic gate"):
- `rt.IntDiv` / `rt.Rem` / `rt.Div` (div-by-zero)
- `rt.AsInt` / `AsFloat` / `AsBool` (heterogeneous slice / untyped FFI return)
- `rt.cmp` (comparison mismatch)
- `rt.Coerce` (3 variants)
- `rt.skyCallDirect`
- Go-runtime index out of range / nil-deref

**Output:** `test/Sky/Runtime/PanicClassGateSpec.hs` with one regression case per class. Each case: a Sky source that the type checker accepts AND would have triggered the panic on a pre-v0.17 compiler; assertion that the current compiler emits Go that returns Err instead of panicking (or refuses to compile).

**Acceptance:** spec passes on `feat/v0.17-pure-sound-codegen` HEAD; each case proves the synchronous-panic gate + recover discipline is intact.

**Risk:** LOW. Pure test additions.

**Session budget:** 2-3 hours wall-clock.

### Phase 4 — Release ceremony

**After Phases 1-3 ship:**

1. Re-spawn independent Judge agent on the post-Phase-3 SHA. If LITERAL still NOT ACHIEVED but REFRAMED ACHIEVED, that closes criterion #10 under the reframed goal.
2. Bump version: edit any cabal-file version stamps; `bash scripts/build.sh` to regenerate the binary.
3. Run the full release gate sequence per CLAUDE.md §"Release checklist":
   - cabal test
   - example sweep (expect 36/39 — 3 pre-existing FFI-gen failures don't block)
   - verify-cli.sh
   - verify-all-web.sh
4. Author `docs/release-notes/v0.17.0.md` summarising:
   - The architectural typed-emit fix (4571da08)
   - 5 closed criteria
   - 2 newly closed criteria (#1 reframe + #3 contract) from Phase 1-2
   - Documented limitations (open umbrellas as known-issues with v0.17.x/v0.18.0 roadmap)
5. Tag `v0.17.0` (USER-EXECUTED — per CLAUDE.md `feedback_no_auto_tag_release`, I never tag without explicit ask).
6. SkyDeploy redeploy per CLAUDE.md §5.

**Session budget:** 1 hour wall-clock + verify-script wait.

## What's OUT for v0.17.0 (deferred)

These items are explicitly OUT of v0.17.0 scope. They ship in v0.17.x patches or v0.18.0:

- **Sealed-interface ADT emission (#677)** — multi-session, cascade risk per iter 17/37/42 history. Targets criterion #1 LITERAL close but the reframe makes it optional.
- **scopeStateRef FULL DELETION (Phase A iter 7+, #678)** — multi-session reader-migration. Phase 2's contract+spec gate closes criterion #3 under the locked wording without deletion.
- **Cycle 6 umbrella #383 close** — broad scope (multiple sub-tasks).
- **Other v0.17 umbrellas (#644 / #654 / #659 / #660 / #664 / #672 / #677 / #678)** — multi-session structural surgery.
- **Literal-zero rt.Coerce close** — explicitly accepted as deferred under the reframe.
- **Extended fuzzer iter count (>10k)** — already at 10k per AUTONOMOUS_GOAL.md commit `b6c9be6e`.

These deferred items remain tracked in the task list. v0.17.1+ patch series and v0.18.0 design will pick them up.

## Why this is the right scope

- **Honors the reframe.** "Rock solid + ~100% sound" is achievable WITHOUT closing all 10 criteria. The remaining criteria improve architectural cleanness, not soundness.
- **Avoids cascade risk.** Phases 1-3 are all ADDITIVE (docs + specs). No Compile.hs structural change.
- **Closes 2 more criteria.** Post-Phases 1-3: 7 of 10 criteria CLOSED (5 already + #1 reframe + #3 contract). That's a defensible release position.
- **Releases the typed-emit fix.** The architectural improvement from commit `4571da08` (8 errors → 0 on 00-standard-libs, 131/131 runtime) deserves user-visible v0.17.0 status.
- **Per CLAUDE.md §0 hard rule 4**, the only stop condition is a genuine implementation blocker. Deferred items don't block; they're scope choices.

## Execution order

Phase 1 → Phase 2 → Phase 3 → Phase 4. Each phase ships its own commit. After each, the verify gates re-run (background cabal-test + example sweep). If any phase regresses something, halt + investigate before continuing.

**Starting Phase 1 immediately in this session if budget supports.**
