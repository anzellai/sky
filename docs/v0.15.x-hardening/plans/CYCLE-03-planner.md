# Cycle 3 — Planner synthesis (C-gaps + reframed P7 + carry-forward)

Plan written for: main @ post-v0.15.15 (PR #84 merged 38c50e4 / PR #85 merged 3de5810)
PR #86 (v0.15.16, SVG/MathML namespace) is OPEN in-flight; cycle 3 numbering starts v0.15.17.
Date: 2026-05-26
Planner pass: 3

## Architectural diagnosis (cycle-3 delta)

Cycle 2 closed the most acute compiler regressors (P1-P5, P6 partial) and surfaced the **LowerCtx cascade safe-ceiling** (2/4 backbone slots migrated; 3 deferred behind the `scopeStateRef` deferred-thunk seam). Cycle 3's Auditor identifies that the remaining cascade work cannot proceed mechanically — **C5 re-frames cycle-1's P7** from "delete `scopeStateRef`" to "make `letBindingType` pure end-to-end by extending `Solve.SolvedTypes` with the region map". This shifts P7 from a 3-4 hour lowerer-side deletion to an 8-12 hour solver-side surgery, split across **two PRs** (P37a Solver-side extension; P37b lowerer migration + IORef field deletion).

The runtime gaps cluster into four families:

1. **C1 residuals (high) + C4 TTL-deletion leak (high) + C6 lock-around-marshal contention (high).** All three are session-lifecycle / dispatch invariants. C1's residuals (dispatchBatched suppression mirror + Go-test mate + post-panic prevBody zero) are a single 1-2 hour cleanup PR that lands FIRST. C4's goroutine leak is one focused PR with a runtime regression test. C6 is a 3-call-site hoist of the JSON marshal outside `sess.mu`.

2. **C2 prevBody dual-meaning (high) + C7 commitRender 5-site fan-out (medium).** Both rename/refactor work to lock the prevTree/prevBody invariant explicitly. C2 introduces the `lastShippedBody` distinction; C7 extracts `commitRender(vn, body)` and routes all 5 write sites through it.

3. **C3 multi-session pub/sub prereqs (critical, strategic).** Phase 3g (#259) is blocked on **five** independent prereqs — none of which exist in `main`. Each prereq is its own item: P45 design doc (gates everything), P46 Store-level Subscribe API, P47 global+local seq split, P48 handler rebuild on broadcast-induced patch, P49 divergence detection (Msg-shape broadcast). Memory bound (prereq 5) folds into P46. The design doc P45 is the umbrella deliverable that must land BEFORE any code in P46-P49.

4. **C11 SSE diff-then-patch transport overhaul (critical, strategic).** Two PRs (P50a wire prevTree through producers + compute diffTrees; P50b add `event: patches` SSE event type + client adapter). Closes C1 fully (suppression becomes `patches.length === 0`), C2 fully (prevBody dual-meaning vanishes), and a part of A7 (single JSON marshalling chokepoint).

The **carry-forward** of OPEN cycle-1 items (P14/P15/P16/P19) and cycle-2 items (P29-P34) is preserved verbatim with updated estimates. None of them have been superseded by cycle-3 findings; they remain as previously-scoped items waiting for their dev slot.

## Sequencing rationale

- **P35 (C1 residuals)** lands FIRST — small cleanup, no dependencies, locks in v0.15.14's invariants.
- **P36 (C4 TTL leak)** is independent of compiler items; ships alongside the cycle-1 runtime backlog (P14).
- **P37a + P37b (C5 reframed P7)** is the **highest-value compiler item this cycle**. Replaces cycle-1's P7. Solver-side surgery (P37a) MUST land before lowerer migration (P37b); deletion of `_lc_regionTypes` from `scopeStateRef` rides P37b.
- **P38 (C10 snapshotCallerCtx helper)** rides P37 — unifies the three `unsafePerformIO (readIORef scopeStateRef)` sites with explicit snapshot semantics. **Blocks** any future cascade migration of the remaining 3 deferred slots (record/list/let-body).
- **P39 (C2 prevBody rename) + P40 (C7 commitRender)** are independent; can interleave with P35/P36.
- **P41 (C6 marshal hoist)** depends on P40 (the snapshot pattern fits the commitRender helper).
- **P42 (C14 SSE buffer env knob)** is a 1-hour standalone.
- **P43 (C13 letBindingType whitelist comment)** is a 1-hour standalone.
- **P44 (C9 LowerCtx cascade design doc)** is a doc-only PR; lands alongside P37.
- **P45 (Phase 3g design doc) → P46 → P47 → P48 → P49** is the C3 critical path. P45 is the umbrella; the others are gated on it.
- **P50a + P50b (C11 SSE diff-then-patch)** is the **largest strategic item this cycle**. Gated on P45 (design doc may inform the transport shape) and P40 (commitRender helper). Each is its own PR.

The carry-forward items (cycle-1 P14/P15/P16/P19, cycle-2 P29-P34) retain their cycle-1/2 tags (v0.15.20-v0.15.38). Cycle-3 NEW items take v0.15.17-v0.15.32 (the unused slots in the v0.15.16-v0.15.30s range freed by the v0.15.13-v0.15.16 reshuffle).

## Tag allocation

| Slot | Owner | Status |
|---|---|---|
| v0.15.13 | PR #82 attr order | SHIPPED |
| v0.15.14 | PR #85 diffNodes Events + runPerformBody suppress | SHIPPED |
| v0.15.15 | PR #84 LowerCtx cascade Phase 2 (P6) | SHIPPED |
| v0.15.16 | PR #86 SVG/MathML namespace | IN-FLIGHT (skip) |
| v0.15.17 | **P35 cycle-3 cleanup** | NEW |
| v0.15.18 | **P36 C4 TTL leak** | NEW |
| v0.15.19 | **P37a SolvedTypes carries region map** | NEW |
| v0.15.20 | **P14 (carry-forward, SSE channel race)** | OPEN (cycle 1) |
| v0.15.21 | **P15 (carry-forward, SSE encoder chokepoint)** | OPEN (cycle 1) |
| v0.15.22 | **P16 (carry-forward, CSRF timing floor)** | OPEN (cycle 1) |
| v0.15.23 | **P19 (carry-forward, typed secret-store audit)** | OPEN (cycle 1) |
| v0.15.24 | **P37b letBindingType pure + scopeStateRef field delete** | NEW |
| v0.15.25 | **P38 snapshotCallerCtx helper (C10)** | NEW |
| v0.15.26 | **P39 prevBody rename (C2)** | NEW |
| v0.15.27 | **P40 commitRender helper (C7)** | NEW |
| v0.15.28 | **P41 marshal-outside-lock (C6)** | NEW |
| v0.15.29 | **P42 SSE buffer env knob (C14)** | NEW |
| v0.15.30 | **P43 letBindingType whitelist cross-ref (C13)** | NEW |
| v0.15.31 | **P44 LowerCtx cascade design doc (C9)** | NEW |
| v0.15.32 | **P29 (carry-forward, B1 alias prefix helper)** | OPEN (cycle 2) |
| v0.15.33 | **P30 (carry-forward, B2 alias identity (mod,name))** | OPEN (cycle 2) |
| v0.15.34 | **P31 (carry-forward, B3 script-revive whitelist)** | OPEN (cycle 2) |
| v0.15.35 | **P32 (carry-forward, B4 form-submit submitter)** | OPEN (cycle 2) |
| v0.15.36 | **P33 (carry-forward, B5 form-data desync doc)** | OPEN (cycle 2) |
| v0.15.37 | **P34 (carry-forward, cabal-test RSS measurement)** | OPEN (cycle 2) |
| v0.15.38 | **P45 Phase 3g design doc (C3 umbrella)** | NEW |
| v0.15.39 | **P46 Store Subscribe API (C3 prereq 1+5)** | NEW |
| v0.15.40 | **P47 global+local seq split (C3 prereq 2)** | NEW |
| v0.15.41 | **P48 handler rebuild on broadcast (C3 prereq 3)** | NEW |
| v0.15.42 | **P49 Msg-shape broadcast (C3 prereq 4)** | NEW |
| v0.15.43 | **P50a SSE diff-then-patch producer (C11)** | NEW |
| v0.15.44 | **P50b event: patches transport + client adapter (C11)** | NEW |

Cycle-1 P7 (was v0.15.13) is **CANCELLED** — replaced by P37a/P37b in this cycle. Cycle-1 P8 (was v0.15.14, "typed-routing per-shape audit") is **SUPERSEDED** by P37b's correctness-from-construction approach (the whitelist drops naturally once `letBindingType` is pure). Cycle-1 P9 ("drop canRouteTyped whitelist", was v0.15.15) is **DEFERRED** to post-P37b — the whitelist is still load-bearing today per C13.

The cycle-1 tags v0.15.13, v0.15.14, v0.15.15 are reused as documented above (SHIPPED for unrelated content). v0.15.20-v0.15.23 are reserved for the carry-forward cycle-1 items so they keep their long-standing identities in the cycle-1 plan.

NEVER v0.16.

---

## NEW items P35-P50 (cycle 3)

### Item P35: Cycle-3 cleanup — C1 residuals (dispatchBatched suppression + Go test + post-panic prevBody)

Closes gaps: C1 residuals (3 sub-items per audit lines 92-103)
Severity: high
Branch: `feat/v0.15.x-hardening-P35-runperform-suppress-cleanup`
Patch tag: v0.15.17
Depends on: PR #85 (v0.15.14) merged — DONE.

#### Diagnosis
v0.15.14 closed the runPerformBody + Time.every suppression but skipped three small follow-ups: (1) `dispatchBatched` at live.go:2480-2486 does NOT mirror the byte-equality check; (2) no `_test.go` mate locks the suppression contract; (3) `sess.prevBody` is unconditionally written to `""` on dispatch panic recovery (live.go:2540-2549, line 2618), so the first post-panic dispatch's suppression check compares against `""` rather than the last-shipped body — harmless but the intent is obscured.

#### Sequenced steps
1. `runtime-go/rt/live_runperform_suppress_test.go` (NEW) — three Go test cases: (a) identical-view dispatch produces NO SSE frame; (b) view-change dispatch DOES produce a frame; (c) post-panic dispatch's first update still produces a frame even if it equals post-panic-zeroed prevBody. Test fails on `main` until step 2 lands.
2. `runtime-go/rt/live.go:2480-2486` — mirror `runPerformBody`'s `body != prevBody` suppression into `dispatchBatched`. 3-line patch capturing `prevBody := sess.prevBody` BEFORE the dispatch call, comparing AFTER.
3. `runtime-go/rt/live.go:2540-2549` (panic recover) — set `body = sess.prevBody` (keep last-known) instead of `body = ""`, so suppression on next dispatch behaves consistently. Comment notes that recover'd panics still emit a structured-error frame via the existing error-frame path.

#### Files touched
- `runtime-go/rt/live.go` (3 edit sites)
- `runtime-go/rt/live_runperform_suppress_test.go` (NEW, ~80 LOC)

#### New tests
- 3 Go test cases (above).

#### Rollout / regression gates
- `go test -race ./runtime-go/...` green.
- All 27 examples build clean.
- `scripts/verify-all-web.sh` green.
- Cabal test count unchanged.

#### Risk register
- Risk: changing post-panic prevBody from "" to last-known might mask a future panic that itself corrupted the body. Mitigation: structured-error frame path is unaffected; the change only affects suppression of the NEXT-cycle's view.

#### Session cost
2 hours, 1 PR.

---

### Item P36: Close Gap C4 — TTL-deletion goroutine leak

Closes gaps: C4
Severity: high
Branch: `feat/v0.15.x-hardening-P36-ttl-delete-cancels-subs`
Patch tag: v0.15.18

#### Diagnosis
`memoryStore.Delete(sid)` (live_store.go:300-330) removes the session from the map but never closes `sess.cancelSub` or `sess.sseCh`. Time.every subscription goroutines (and any in-flight `runPerformBody`) continue dispatching after the session is "gone"; they push to `sess.sseCh` until the buffered channel fills, then silently drop via `default:`. No goroutine ever exits — `cancelSub` is only closed on next-`setupSubscriptions`, not on session deletion. Under production churn, this leaks N goroutines per expired-with-active-subscription session.

#### Sequenced steps
1. `runtime-go/rt/live_store_delete_test.go` (NEW) — opens a session with `Sub.every 50ms Tick`, sets TTL=50ms, sleeps past cleanup interval, asserts `runtime.NumGoroutine()` returns to baseline (±2 for SSE/test framework).
2. `runtime-go/rt/live_store.go:300-330` — `Delete(sid)` first acquires `sess.mu`, sets `sess.done atomic.Bool = true`, closes `sess.cancelSub`, releases the lock, briefly waits (10ms grace) for subscription goroutines to drain, then closes `sess.sseCh`.
3. `runtime-go/rt/live.go` — every writer to `sess.sseCh` (runPerformBody, dispatchBatched, Time.every) checks `if sess.done.Load() { return }` before pushing.
4. `runtime-go/rt/live.go` (subscription goroutines) — each select arm includes `<-sess.cancelSub` to exit cleanly.

#### Files touched
- `runtime-go/rt/live_store.go` (Delete + struct field)
- `runtime-go/rt/live.go` (3 writer sites + subscription goroutines)
- `runtime-go/rt/live_store_delete_test.go` (NEW, ~120 LOC)

#### New tests
- Goroutine-count regression test (above).
- Go race-detector clean.

#### Rollout / regression gates
- `go test -race ./runtime-go/...` green.
- `scripts/verify-all-web.sh` green (no observable behaviour change for live sessions).
- Production-soak: run examples/14-skylive-counter for 30 minutes with rapid open/close (Playwright loop); assert `runtime.NumGoroutine()` stable.

#### Risk register
- Risk: closing `sseCh` while another goroutine writes panics "send on closed channel". Mitigation: the `sess.done` flag check before write; grace period covers in-flight writes; close happens after the grace.
- Risk: in-flight `runPerformBody` may be mid-marshal when `done` is set; the marshal completes but the push drops. Acceptable — the session is gone, no observer.

#### Session cost
6-8 hours, 1 PR.

---

### Item P37a: SolvedTypes carries region map (C5 step 1)

Closes gaps: C5 step 1 of 2 (replaces cycle-1 P7)
Severity: critical (foundation for P37b + future cascade work)
Branch: `feat/v0.15.x-hardening-P37a-solvedtypes-region-map`
Patch tag: v0.15.19

#### Diagnosis (from audit C5 reframe)
The `scopeStateRef` IORef is not just a race-prone scope state holder — it forms a deferred-thunk cycle with `letBindingType`. `letToGo`'s `defStmts` thunk runs `letBindingType ctx solved dn valExpr` which reads `_lc_regionTypes` from the IORef-snapshotted ctx; meanwhile `bodyGo = lowerBody body` may install a fresh ctx via the lowerExpr wrapper. The two thunks force in dependency order; if both depend on a ctx-shaped value observed at different evaluation points, GHC blackholes. v0.15.15 verified this on examples/16-skychess and CoerceArgParametricSpec.

The fix is to move the region-types map OUT of `scopeStateRef` and INTO `Solve.SolvedTypes` (which already carries `Map Var T.Type`). Once region-types live in a pure data structure that flows alongside SolvedTypes, `letBindingType`'s region lookup becomes pure — no IORef read — and the deferred-thunk cycle is broken.

This step ONLY extends SolvedTypes; it does not yet migrate `letBindingType`'s signature or delete the IORef field. That's P37b.

#### Sequenced steps
1. `src/Sky/Type/Solve.hs` — extend `data SolvedTypes` with a `solvedRegionTypes :: Map A.Region T.Type` field. The Solver writes to it after unification settles (mirrors the existing per-region IORef write that currently feeds `_lc_regionTypes`).
2. `src/Sky/Type/Solve.hs` (constructor + every record update) — populate the new field. Internal change; downstream readers continue using the IORef path until P37b.
3. `test/Sky/Type/SolvedTypesRegionMapSpec.hs` (NEW) — compiles a minimal multi-region fixture, asserts `solvedRegionTypes` contains every region key the existing `_lc_regionTypes` IORef-backed path would contain. Byte-equality assertion locks the data-shape.
4. `src/Sky/Build/Compile.hs` — NO consumer changes in this PR. The IORef remains the source of truth for region lookups; the new SolvedTypes field is populated but unused (verified by SolvedTypesRegionMapSpec).

#### Files touched
- `src/Sky/Type/Solve.hs` (data type + constructor + record update sites)
- `test/Sky/Type/SolvedTypesRegionMapSpec.hs` (NEW, ~100 LOC)

#### New tests
- `SolvedTypesRegionMapSpec` — locks the data-flow shape.

#### Rollout / regression gates
- All 27 examples build clean (zero behaviour change expected).
- `examples/13-skyshop` main.go byte-identical (md5 39b18368... or whatever the current locked value is).
- Cabal test +1 spec; no other change.
- `go test -race` clean (no runtime change).

#### Risk register
- Risk: SolvedTypes grows in memory size; affects cabal-test RSS (CLAUDE.md non-negotiable §1). Mitigation: measure before merge via `scripts/measure-cabal-test-rss.sh` (P34 prerequisite — if P34 not yet shipped, measure ad-hoc). The region map is the SAME data already held in the IORef; total memory is constant.
- Risk: a record-update site in Solve.hs is missed, leaving the new field unset. Mitigation: SolvedTypesRegionMapSpec asserts the field is populated for at least the test fixture; add 5-10 assertions across distinct multi-region shapes.

#### Session cost
4-6 hours, 1 PR.

---

### Item P37b: `letBindingType` pure + delete `_lc_regionTypes` from scopeStateRef (C5 step 2)

Closes gaps: C5 step 2 of 2; superseded cycle-1 P7
Severity: critical
Branch: `feat/v0.15.x-hardening-P37b-letBindingType-pure-delete-regionref`
Patch tag: v0.15.24
Depends on: P37a (v0.15.19) merged.

#### Diagnosis
Once SolvedTypes carries the region map (P37a), `letBindingType` can drop its `LC.LowerCtx` parameter — it only needs region-types and solved-types, both now available on SolvedTypes. The function becomes purely a (SolvedTypes, String, Can.Expr) -> Maybe T.Type — no IORef read. With `letBindingType` pure, `letToGo`'s `defStmts` thunk no longer participates in the ctx-aware wrapper write/force seam. `letToGo`'s body can then route through `lowerExprExpectGo` ctx-installer safely.

After the migration, `_lc_regionTypes` field on `LC.LowerCtx` (and the `lookupRegionType` IORef helper) become dead. Delete them. Other `scopeStateRef` users (lambda-types, lambda-Go-strings) stay — they have their own deferred-thunk seams to close independently (out of scope for this cycle).

#### Sequenced steps
1. `src/Sky/Build/Compile.hs` — change `letBindingType :: LC.LowerCtx -> Solve.SolvedTypes -> String -> Can.Expr -> Maybe T.Type` to `letBindingType :: Solve.SolvedTypes -> String -> Can.Expr -> Maybe T.Type`. Body reads region map from `solvedRegionTypes solved` instead of `LC.lookupRegionType ctx`.
2. `src/Sky/Build/Compile.hs` (every call site) — drop the `ctx` argument. Mechanical (~5 sites per `grep`).
3. `src/Sky/Build/LowerCtx.hs` — delete `_lc_regionTypes` field, `lookupRegionType`, and the `withRegionTypes` installer. Run `cabal build` to flush any latent reader.
4. `src/Sky/Build/Compile.hs` (3 sites of `unsafePerformIO (readIORef scopeStateRef)` at 9996/10357/10163) — these stay; they read OTHER scope-state fields (lambda-types, etc.). Verify via cabal-build no field reference to deleted region-types path.
5. `src/Sky/Build/Compile.hs` (`letToGo` body lowering, line 10054-10064) — REMOVE the in-line revert comment block. Route the let-body through `lowerExprExpectGo` ctx-installer (the migration deferred from P6). Validate with the CoerceArgParametricSpec + examples/16-skychess sweep (the two reproducers from P6's revert).
6. `test/Sky/Build/LetBodyCtxCascadeSpec.hs` (NEW) — locks the let-body migration: compiles a fixture where the let-body shape WOULD have blackholed under the IORef path, asserts the new pure path compiles + runs correctly.

#### Files touched
- `src/Sky/Build/Compile.hs` (letBindingType + ~5 call sites + letToGo body migration + revert-block deletion)
- `src/Sky/Build/LowerCtx.hs` (field delete + helper delete)
- `test/Sky/Build/LetBodyCtxCascadeSpec.hs` (NEW, ~100 LOC)

#### New tests
- `LetBodyCtxCascadeSpec` — the let-body migration's load-bearing lock.
- Existing `CoerceArgParametricSpec` + examples/16-skychess + skyshop sweep must remain green (the v0.15.15 P6 revert reproducers).

#### Rollout / regression gates
- `cabal test` 350+ specs green; 1 pending matches prior.
- All 27 examples build clean; 13-skyshop main.go byte-equality (or documented delta — the let-body migration may slightly change `letToGo`'s emission for the affected let-body slots).
- `scripts/verify-all-web.sh` green.
- `scripts/verify-cli.sh` green.
- σ-consensus voter audit included in PR description (per cycle-1 standing direction): no `coerceArg` / `goExprGoType` voter changed in this PR.

#### Risk register
- Risk: deleting `_lc_regionTypes` reveals a hidden consumer that the cabal build doesn't flag (e.g. `Text.show` reflection). Mitigation: full grep for "_lc_regionTypes" + "lookupRegionType" before merge.
- Risk: the let-body migration re-triggers the blackhole on a fixture not covered by the P6 reproducers. Mitigation: run the full self-test suite (`scripts/build.sh --self-tests`) BEFORE pushing; add any new blackhole reproducer to LetBodyCtxCascadeSpec.
- Risk: emission delta for let-body changes 13-skyshop main.go by >0.5%. Mitigation: log the delta; if material, document the cause (more typed routing, fewer Coerce wraps) in the PR.

#### Session cost
4-6 hours, 1 PR.

---

### Item P38: Unify scopeStateRef 3-site reads — `snapshotCallerCtx` helper (C10)

Closes gaps: C10
Severity: high
Branch: `feat/v0.15.x-hardening-P38-snapshot-caller-ctx-helper`
Patch tag: v0.15.25
Depends on: P37b (v0.15.24) merged — the helper documents the post-P37b seam shape.

#### Diagnosis (from audit C10)
Three SEPARATE `unsafePerformIO (readIORef scopeStateRef)` reads sit at the function head of `letToGo` (Compile.hs:9996), `defToStmts` (10357), and `registerMainLetBindingType` (10163). Each captures a snapshot ONCE per function entry and threads it via `ctx`. The pattern is good (single snapshot vs N implicit reads) but latent: `defToStmts` may run inside `letToGo`'s body-lowering, where the wrapper-installed ctx differs from the original caller's ctx. The current code happens to work because only lambda body + call arg slots install wrappers today. Post-P37b, more migrations land; the latency becomes a live hazard.

#### Sequenced steps
1. `src/Sky/Build/Compile.hs` — introduce `{-# NOINLINE snapshotCallerCtx #-}` `snapshotCallerCtx :: () -> LC.LowerCtx` with explicit Haddock documenting the snapshot semantics ("Returned ctx is the value installed at the CALL site, NOT any inner-wrapper installed value").
2. Replace the three `unsafePerformIO (readIORef scopeStateRef)` sites with `snapshotCallerCtx ()`.
3. `test/Sky/Build/SnapshotCallerCtxSpec.hs` (NEW) — locks the helper's NOINLINE shape via TemplateHaskell or `Reify` (or just a comment-doc test asserting the helper is referenced from exactly 3 sites).

#### Files touched
- `src/Sky/Build/Compile.hs` (helper + 3 call sites)
- `test/Sky/Build/SnapshotCallerCtxSpec.hs` (NEW, ~50 LOC)

#### New tests
- `SnapshotCallerCtxSpec`.

#### Rollout / regression gates
- Zero behaviour change (mechanical refactor).
- All 27 examples build clean; 13-skyshop main.go byte-identical.
- `cabal test` +1 spec.

#### Risk register
- Risk: NOINLINE-snapshot doesn't actually inline-prevent under -O2. Mitigation: pragma is a hint to GHC; the function's IO read forces re-evaluation regardless. The doc comment is the real lock.

#### Session cost
2-3 hours, 1 PR.

---

### Item P39: Rename `prevBody` → `lastShippedBody` + `lastComputedBody` (C2)

Closes gaps: C2
Severity: high
Branch: `feat/v0.15.x-hardening-P39-prevbody-rename-split`
Patch tag: v0.15.26

#### Diagnosis (from audit C2)
`sess.prevBody` is overloaded — both "last computed body" (dispatch's invariant) AND "last shipped body" (suppression's invariant). They coincide today but the dual meaning is undocumented. A future cleanup ("only update prevBody when shipping a frame") would silently break suppression on every byte-identical view. Split the field; document the contract.

#### Sequenced steps
1. `runtime-go/rt/live.go:1283` (or whichever struct line `prevBody` lives on) — rename `prevBody string` to TWO fields: `lastComputedBody string` (set by every dispatch / renderView) and `lastShippedBody string` (set only when a frame is enqueued to `sseCh`).
2. Suppression checks compare against `lastShippedBody` (the correct semantics).
3. dispatch's contract becomes "always update `lastComputedBody`; updating `lastShippedBody` is the SSE producer's job".
4. `runtime-go/rt/live_runperform_suppress_test.go` (extend P35's test) — add a case asserting that a dispatch which computes a new body but is suppressed (no frame shipped) leaves `lastShippedBody` unchanged.
5. Document the field-pair contract in a doc-comment on the struct.

#### Files touched
- `runtime-go/rt/live.go` (~10 sites)
- `runtime-go/rt/live_runperform_suppress_test.go` (extend)

#### New tests
- Extended P35 test (above).

#### Rollout / regression gates
- `go test -race` green.
- `scripts/verify-all-web.sh` green.
- All 27 examples build clean.

#### Risk register
- Risk: a missed call site continues writing the old `prevBody` field name → cabal build fails. Good — caught at compile time.
- Risk: external Go FFI references the field. Mitigation: grep external Go (none expected; `liveSession` is unexported).

#### Session cost
3-4 hours, 1 PR.

---

### Item P40: Extract `commitRender(vn, body)` — 5-site fan-out (C7)

Closes gaps: C7
Severity: medium
Branch: `feat/v0.15.x-hardening-P40-commit-render-helper`
Patch tag: v0.15.27
Depends on: P39 (v0.15.26) merged — the helper writes both new field names.

#### Diagnosis (from audit C7)
`sess.prevTree` and `sess.prevBody` (post-P39: `lastComputedBody`) are written at FIVE sites with subtly different invariants: dispatch (2594/2618), handleInitial (2090/2091), renderView (2629), dispatchBatched-handlers-rebuild (2438), handleSSE-reconnect-resync (2894/2895). Any new feature that re-renders needs to update both fields; missing one site silently corrupts subsequent diff/suppression decisions.

#### Sequenced steps
1. `runtime-go/rt/live.go` — add `func (sess *liveSession) commitRender(vn *VNode, body string)` that writes BOTH `prevTree` and `lastComputedBody` under `sess.mu`. Doc-comment documents the invariant.
2. Replace all 5 sites with `sess.commitRender(&vn, body)` calls.
3. `runtime-go/rt/live_commit_render_test.go` (NEW) — table test asserts each of the 5 sites updates BOTH fields atomically (use a parallel goroutine reading the fields while commitRender runs).

#### Files touched
- `runtime-go/rt/live.go` (helper + 5 call sites)
- `runtime-go/rt/live_commit_render_test.go` (NEW, ~100 LOC)

#### New tests
- `commit_render_test.go`.

#### Rollout / regression gates
- `go test -race` green.
- All 27 examples build clean.
- `scripts/verify-all-web.sh` green.

#### Risk register
- Risk: renderView previously wrote ONLY prevTree (line 2629); changing it to write both fields might shift the next-suppression-check's behaviour. Mitigation: investigate renderView's body source (likely just-rendered string); update test to cover the new semantics; document in PR.

#### Session cost
3-4 hours, 1 PR.

---

### Item P41: Hoist JSON marshal outside `sess.mu` (C6)

Closes gaps: C6
Severity: high (latency)
Branch: `feat/v0.15.x-hardening-P41-marshal-outside-lock`
Patch tag: v0.15.28
Depends on: P40 (v0.15.27) merged — uses the snapshot pattern.

#### Diagnosis (from audit C6)
`runPerformBody` holds `sess.mu` while calling `encodeSSEFrame(sess, body)` (live.go:2712). The marshal is CPU-bound (~200µs for 50KB body on M1). Under steady-state load, 100 sessions × 10 ticks/sec serialize through the mutex; ~2% of one core lost to lock contention on marshal alone. Same hazard at the 3 call sites: dispatchBatched, runPerformBody, Time.every setupSubscriptions.

#### Sequenced steps
1. `runtime-go/rt/live.go` — add `func (sess *liveSession) prepareFrameSnapshot() (snap frameSnapshot, err error)` that under `sess.mu` captures `respSeq`, `body`, `ackInputs`, returns. Marshal happens after unlock against the snapshot.
2. Three call sites (dispatchBatched, runPerformBody, Time.every) call `snap, err := sess.prepareFrameSnapshot()`, unlock implicitly, then `frame := encodeSSEFrameFromSnapshot(snap)` outside the lock.
3. `runtime-go/rt/live_lock_contention_test.go` (NEW) — benchmark/test asserting `sess.mu` is held for <50µs in steady state (vs the current >200µs).

#### Files touched
- `runtime-go/rt/live.go` (helper + 3 call sites)
- `runtime-go/rt/live_lock_contention_test.go` (NEW, ~120 LOC)

#### New tests
- Lock-contention benchmark (above).

#### Rollout / regression gates
- `go test -race` green.
- All 27 examples build clean; runtime behaviour unchanged.
- `scripts/verify-all-web.sh` green.
- Benchmark logs latency improvement.

#### Risk register
- Risk: the snapshot doesn't capture all mutable state; marshal sees stale data. Mitigation: explicit snapshot type; review every field encodeSSEFrame reads.
- Risk: lock contention test is flaky on CI runners. Mitigation: assert relative improvement (post / pre ratio < 0.5), not absolute time.

#### Session cost
4-6 hours, 1 PR.

---

### Item P42: SSE buffer env knob + drops metric (C14)

Closes gaps: C14
Severity: low
Branch: `feat/v0.15.x-hardening-P42-sse-buffer-env-knob`
Patch tag: v0.15.29

#### Diagnosis (from audit C14)
`sseCh` capacity is hardcoded at 16 (live.go:2066). A 5-second burst at 100ms/tick fills it; drops happen silently via the `default:` arm. Future broadcast scenarios (C3) make drops user-visible. Parameterize via env; add a drops counter so operators can observe rate.

#### Sequenced steps
1. `runtime-go/rt/live.go` — read `SKY_LIVE_SSE_BUF` env at startup; default 16; clamp to [1, 1024].
2. Add `sky_live_sse_drops_total{session}` counter; increment in each `default:` arm.
3. Document the env var in `CLAUDE.md` environment-variables section + `templates/CLAUDE.md` + `docs/skylive/overview.md`.
4. `runtime-go/rt/live_sse_buffer_test.go` (NEW) — asserts env override works; asserts drops counter increments.

#### Files touched
- `runtime-go/rt/live.go` (env read + counter + drop sites)
- `CLAUDE.md` + `templates/CLAUDE.md` + `docs/skylive/overview.md` (env var doc)
- `runtime-go/rt/live_sse_buffer_test.go` (NEW, ~80 LOC)

#### New tests
- `live_sse_buffer_test.go`.

#### Rollout / regression gates
- All 27 examples build clean.
- `go test -race` green.

#### Risk register
- Risk: clamp range too narrow. Mitigation: [1, 1024] is generous; can widen later if needed.

#### Session cost
1-2 hours, 1 PR.

---

### Item P43: Strengthen `letBindingType` whitelist cross-ref comment (C13)

Closes gaps: C13
Severity: low
Branch: `feat/v0.15.x-hardening-P43-let-binding-whitelist-comment`
Patch tag: v0.15.30

#### Diagnosis (from audit C13)
The body-shape whitelist for typed routing (Compile.hs:10259-10266) is currently CORRECT but its comment doesn't name the specific failure path (`coerceArg → coerceToFieldType → rt.AsListT` misbehaving on FFI results). Already partially documented at 10218-10230; strengthen by adding the regression-spec name.

#### Sequenced steps
1. `src/Sky/Build/Compile.hs:10218-10266` — append a comment block naming P10/P11 (GoType ADT structural fix) and the specific path `coerceArg → coerceToFieldType → rt.AsListT` plus a reference to the Money.allocate regression. Note that the whitelist becomes droppable once P11 lands.

#### Files touched
- `src/Sky/Build/Compile.hs` (comment only)

#### New tests
- None (comment-only).

#### Rollout / regression gates
- All 27 examples build clean (no code change).

#### Risk register
- None.

#### Session cost
0.5-1 hour, 1 PR.

---

### Item P44: LowerCtx cascade design doc (C9 tooling gap 4)

Closes gaps: cycle-3 tooling gap 4 ("scopeStateRef write/restore semantics undocumented")
Severity: low (documentation)
Branch: `feat/v0.15.x-hardening-P44-lowerctx-cascade-design-doc`
Patch tag: v0.15.31
Depends on: P37b (v0.15.24) merged — doc reflects post-migration shape.

#### Diagnosis
The `lowerExpr` / `lowerExprExpectGo` wrappers' contract ("force to WHNF before restore") is documented in-line but not in a separate design note. Future maintainers reading the wrapper code may miss the subtle force-then-restore ordering. Write a single design note summarizing the 3 Phase 2 wrappers + the 3 deferred-thunk reverts (P6 history) + the post-P37b status.

#### Sequenced steps
1. `docs/v0.15.x-hardening/lowerctx-cascade-design.md` (NEW) — sections: (a) the IORef-implicit problem; (b) the wrapper write/force/restore pattern; (c) the four backbone slots + their migration status; (d) the deferred-thunk blackhole class + reproducers; (e) post-P37b status (let-body migrated; record/list still deferred or migrated TBD); (f) what NOT to do (e.g. don't move IORef reads into thunks held by GoBlock continuations).

#### Files touched
- `docs/v0.15.x-hardening/lowerctx-cascade-design.md` (NEW, ~400-600 lines)

#### New tests
- None (doc-only).

#### Rollout / regression gates
- None (doc-only).

#### Risk register
- None.

#### Session cost
3-4 hours, 1 PR.

---

### Item P45: Phase 3g multi-session design doc (C3 umbrella — issue #259)

Closes gaps: C3 (umbrella — gates P46-P49)
Severity: critical (strategic)
Branch: `feat/v0.15.x-hardening-P45-phase-3g-design-doc`
Patch tag: v0.15.38

#### Diagnosis (from audit C3)
Phase 3g multi-session apps (chat, collaborative editor, presence) cannot ship without coordinated decisions on FIVE prereqs: (1) Store-level Subscribe API; (2) global+local seq split; (3) handler rebuild on broadcast-induced patch; (4) divergence detection (Msg-shape broadcast); (5) memory bound on subscription registries. The architectural decision needs to land BEFORE the API surface — today neither exists. Issue #259 needs a design doc PRIOR to any code.

#### Sequenced steps
1. `docs/phase-3g-multi-session-design.md` (NEW) — sections: (a) problem statement (chat / collab / presence); (b) Store interface for broadcast with backend-by-backend feasibility analysis (memory / sqlite / redis pubsub / postgres NOTIFY / firestore snapshots); (c) Msg-shape vs total-replacement broadcast; (d) seq numbering rule (per-app global counter alongside per-session local counter); (e) memory bound via ref-counted topic registry; (f) handler-rebuild contract (every view-changing event re-runs renderVNode-populates-handlers); (g) test plan (multi-session Playwright in `scripts/verify-all-web.sh` exercising N parallel sessions); (h) Sky API sketch (`Sub.subscribe topic decoder`, `Live.broadcast topic msg`); (i) failure modes (network partition mid-broadcast; subscriber crash; store backend down).
2. `docs/v0.15.x-hardening/CYCLE_LOG.md` — append cross-ref to the design doc.
3. Add a "BLOCKED ON DESIGN" label to issue #259 until this doc is reviewed; remove on merge.

#### Files touched
- `docs/phase-3g-multi-session-design.md` (NEW, ~800-1200 lines)
- `docs/v0.15.x-hardening/CYCLE_LOG.md` (cross-ref)

#### New tests
- None (doc-only; test plan is part of the doc).

#### Rollout / regression gates
- Reviewed by user (human gate).
- Cross-referenced from issue #259.
- ALL of P46-P49 must be re-validated against this doc; any disagreement triggers a re-spawn.

#### Risk register
- Risk: design decisions in the doc may need revision once implementation reveals constraints. Mitigation: the doc is versioned; revision is expected after P46 lands.

#### Session cost
6-8 hours (deep design work; not code).

---

### Item P46: Store-level Subscribe API + ref-counted topic registry (C3 prereqs 1+5)

Closes gaps: C3 prereqs 1 (Subscribe API) + 5 (memory bound)
Severity: critical
Branch: `feat/v0.15.x-hardening-P46-store-subscribe-api`
Patch tag: v0.15.39
Depends on: P45 (v0.15.38) design doc merged + reviewed.

#### Diagnosis (from audit C3 prereqs 1 + 5)
Each store backend (memory / sqlite / redis / postgres / firestore) needs a fanout primitive. Redis pub/sub is native; others need polling or notification triggers. The interface MUST be defined at the `liveStore` level (live_store.go:250-260) so the backend abstraction holds. A naive `map[topic][]chan` grows unbounded under churn; need ref-counted topics + cleanup on session.Delete.

#### Sequenced steps
1. Implement per design doc P45's "Store interface for broadcast" section.
2. Add `Subscribe(topic, filter) (<-chan SessionEvent, cancel func())` to `liveStore` interface.
3. Implement on memoryStore first (ref-counted topic registry, cleanup on session.Delete).
4. Add per-backend stubs for sqlite/redis/postgres/firestore returning `errors.New("Subscribe not supported on this backend yet")` for future PRs.
5. `runtime-go/rt/live_store_subscribe_test.go` (NEW) — table tests for memory backend.
6. `runtime-go/rt/live_store_subscribe_memory_bound_test.go` (NEW) — opens 1000 subscriptions, closes them, asserts topic registry map size returns to 0.

#### Files touched
- `runtime-go/rt/live_store.go` (interface + memory impl + stubs)
- `runtime-go/rt/live_store_subscribe_test.go` (NEW, ~200 LOC)
- `runtime-go/rt/live_store_subscribe_memory_bound_test.go` (NEW, ~100 LOC)

#### New tests
- Subscribe API table tests + memory-bound test.

#### Rollout / regression gates
- `go test -race` green.
- All 27 examples build clean (no observable change; new API unused).
- `scripts/verify-all-web.sh` green.

#### Risk register
- Risk: ref-count race under concurrent subscribe/unsubscribe. Mitigation: `sync.Mutex` on the topic registry; atomic ref-count.
- Risk: memory backend's polling overhead under heavy churn. Mitigation: design doc P45 specifies the polling model; revisit if benchmark shows >5% CPU at 1000 subscribers.

#### Session cost
10-14 hours, 1 PR.

---

### Item P47: Global + local seq split (C3 prereq 2)

Closes gaps: C3 prereq 2 (global+local seq split)
Severity: critical
Branch: `feat/v0.15.x-hardening-P47-global-local-seq-split`
Patch tag: v0.15.40
Depends on: P45 (v0.15.38) design doc; can ship before or after P46.

#### Diagnosis (from audit C3 prereq 2)
Today `sess.outSeq` is per-session (live.go:1289-1292). Broadcast events need a global monotonic counter so observers can detect drops. New field: `app.globalSeq atomic.Int64` (renamed `sess.outSeq → sess.localSeq` for clarity).

#### Sequenced steps
1. Implement per design doc P45's "Seq numbering rule" section.
2. Rename `sess.outSeq` → `sess.localSeq` everywhere.
3. Add `app.globalSeq atomic.Int64`.
4. SSE frames carry both `{localSeq, globalSeq}`; client side stores both.
5. `runtime-go/rt/live_seq_split_test.go` (NEW) — asserts both counters advance correctly under broadcast + local-dispatch interleaving.

#### Files touched
- `runtime-go/rt/live.go` (field rename + new field + SSE frame format)
- JS client side (live.go's embedded JS) — store + ack both seqs
- `runtime-go/rt/live_seq_split_test.go` (NEW, ~150 LOC)

#### New tests
- Seq split table tests.

#### Rollout / regression gates
- `go test -race` green.
- All 27 examples build clean.
- `scripts/verify-all-web.sh` green (single-session apps see globalSeq always = localSeq for first session).

#### Risk register
- Risk: SSE frame format change breaks JS client backward-compat. Mitigation: globalSeq is additive; old client ignores the field.
- Risk: rename ripples through 15+ sites. Mitigation: mechanical refactor.

#### Session cost
5-7 hours, 1 PR.

---

### Item P48: Handler rebuild on broadcast-induced patch (C3 prereq 3)

Closes gaps: C3 prereq 3 (handler rebuild contract)
Severity: critical
Branch: `feat/v0.15.x-hardening-P48-handler-rebuild-on-broadcast`
Patch tag: v0.15.41
Depends on: P46 (v0.15.39) Subscribe API; P47 (v0.15.40) seq split.

#### Diagnosis (from audit C3 prereq 3)
When session A's broadcast triggers session B's view to change, session B's `runCmd` did not run; its `sess.handlers` map is stale relative to the new render. The renderVNode-populates-handlers cycle must run on EVERY view-changing event, not just user-msg-driven dispatches.

#### Sequenced steps
1. Per design doc P45, decide: route broadcast through `dispatch` (Msg-shape) OR add a "broadcast-render" pathway that explicitly rebuilds handlers.
2. Implement chosen path. If Msg-shape (recommended): the broadcast just dispatches a Msg on each subscribing session; existing dispatch path re-renders + rebuilds handlers naturally.
3. `runtime-go/rt/live_broadcast_handler_rebuild_test.go` (NEW) — opens 2 sessions, broadcasts a Msg from one, asserts the other's `sess.handlers` map is rebuilt and the user's next click on the new view dispatches correctly.

#### Files touched
- `runtime-go/rt/live.go` (broadcast dispatch path)
- `runtime-go/rt/live_broadcast_handler_rebuild_test.go` (NEW, ~150 LOC)

#### New tests
- Handler-rebuild test (above).

#### Rollout / regression gates
- `go test -race` green.
- All 27 examples build clean.
- `scripts/verify-all-web.sh` green.
- Add new multi-session Playwright fixture (per tooling gap 2 from audit) — opens 2 tabs, broadcasts, asserts both update.

#### Risk register
- Risk: handler-rebuild on EVERY tick (combined with Time.every) doubles CPU cost. Mitigation: handlers already rebuild on dispatch; broadcast just routes through dispatch.

#### Session cost
6-8 hours, 1 PR.

---

### Item P49: Msg-shape broadcast (C3 prereq 4 — divergence detection)

Closes gaps: C3 prereq 4 (divergence detection)
Severity: critical
Branch: `feat/v0.15.x-hardening-P49-msg-shape-broadcast`
Patch tag: v0.15.42
Depends on: P48 (v0.15.41).

#### Diagnosis (from audit C3 prereq 4)
Two sessions can race on shared model state. Session A applies broadcast event 100; session B applies user event 99 then broadcast 100. If event 99 modifies a field broadcast 100 reads, A and B diverge. Two design options: (a) all broadcast events are total replacement (overwrite local state), or (b) broadcast events are message-shape (each session's `update` applies them deterministically). Elm-style answer is (b). Sky.Live's wire protocol has no precedent for "broadcast Msg" — adding it is this item.

#### Sequenced steps
1. Per design doc P45, finalize Msg-shape broadcast protocol.
2. Sky-source API: `Live.broadcast : Topic -> msg -> Cmd msg` (sends to all subscribers; subscriber receives the msg via `Sub.subscribe topic decoder`).
3. Runtime: subscribers' Msg dispatch path is the existing dispatch; broadcast just enqueues the Msg.
4. `runtime-go/rt/live_broadcast_divergence_test.go` (NEW) — table test exercising the race scenario in the audit; asserts both sessions converge.
5. `sky-stdlib/Std/Cmd.sky` + `sky-stdlib/Std/Sub.sky` — add `broadcast` / `subscribe` primitives wired to runtime FFI.
6. `examples/27-multi-session-chat` (NEW) — example demonstrating the API; verified via `scripts/verify-all-web.sh` multi-tab probe.
7. Update `docs/skylive/overview.md` + `templates/CLAUDE.md` to document the API.

#### Files touched
- `runtime-go/rt/live.go` (broadcast dispatch path)
- `runtime-go/rt/live_broadcast_divergence_test.go` (NEW, ~200 LOC)
- `sky-stdlib/Std/Cmd.sky` + `sky-stdlib/Std/Sub.sky` (new primitives)
- `examples/27-multi-session-chat/` (NEW example, ~300 LOC Sky source)
- `docs/skylive/overview.md` + `templates/CLAUDE.md`

#### New tests
- Divergence test + Playwright multi-tab fixture + example.

#### Rollout / regression gates
- `go test -race` green.
- All 28 examples build clean (now 28 with the new chat example).
- `scripts/verify-all-web.sh` green incl. new multi-tab probe.
- Cycle log entry confirms a 28-example sweep.

#### Risk register
- Risk: Msg-shape broadcast doesn't cover total-replacement use cases (e.g. "replace this single record"). Mitigation: total replacement is a special-case Msg; user writes the Msg shape.
- Risk: large per-session model duplication under broadcast load. Mitigation: design doc covers this — model is per-session, but shared persistent state lives in Std.Db.

#### Session cost
12-16 hours, 1 PR.

---

### Item P50a: SSE diff-then-patch producer (C11 step 1)

Closes gaps: C11 step 1 of 2
Severity: critical (strategic)
Branch: `feat/v0.15.x-hardening-P50a-sse-diff-then-patch-producer`
Patch tag: v0.15.43
Depends on: P40 (v0.15.27) commitRender helper; P45 (v0.15.38) design doc may inform shape.

#### Diagnosis (from audit C11 step 1)
The SSE channel `sess.sseCh` carries full RENDERED HTML BODIES, not structural patches. Every Cmd.perform completion + Time.every tick + reconnect-resync sends 10-50 KB. The HTTP /_sky/event POST path computes structural patches via `diffTrees`; the SSE path does not. Wire prevTree through runPerformBody + Time.every; compute diffTrees on each render; ship patches when small enough.

#### Sequenced steps
1. `runtime-go/rt/live.go` (runPerformBody + Time.every) — capture `prevTree := sess.prevTree` before dispatch; compute `patches := diffTrees(prevTree, newTree, ...)` after.
2. If `patches` is non-empty AND NOT a full-replace (`!patchesAreFullReplace(patches)`), encode patches into the SSE frame.
3. Else fall back to full-body SSE (first-render case; full-replace case).
4. NO client-side changes in this PR (P50b adds the client adapter). Encode patches as a NEW `event: patches` SSE event type that the client ignores until P50b lands; full-body SSE remains the operative path.
5. `runtime-go/rt/live_sse_diff_producer_test.go` (NEW) — asserts the producer correctly chooses patches-vs-full-body based on diff result + flag.

#### Files touched
- `runtime-go/rt/live.go` (3 producer sites)
- `runtime-go/rt/live_sse_diff_producer_test.go` (NEW, ~150 LOC)

#### New tests
- Producer test.

#### Rollout / regression gates
- `go test -race` green.
- All 27 examples build clean; behaviour unchanged (client ignores `event: patches` until P50b).
- `scripts/verify-all-web.sh` green.
- Optional: log SSE bytes/session (tooling gap 3 from audit) to demonstrate baseline.

#### Risk register
- Risk: diffTrees on prevTree==nil panics. Mitigation: guard explicitly.
- Risk: emitting `event: patches` confuses older clients. Mitigation: well-known event type; old client's `EventSource` ignores unhandled event types.

#### Session cost
6-10 hours, 1 PR.

---

### Item P50b: `event: patches` transport + client adapter (C11 step 2)

Closes gaps: C11 step 2 of 2
Severity: critical (strategic)
Branch: `feat/v0.15.x-hardening-P50b-event-patches-transport`
Patch tag: v0.15.44
Depends on: P50a (v0.15.43).

#### Diagnosis
Client adapter for the new `event: patches` SSE event type. JS-side: parse JSON patch list, call existing `__skyApplyPatches(patches)` (already at live.go:3989+). Reconnect-resync continues using full-body SSE (the only "no prevTree yet" case).

#### Sequenced steps
1. `runtime-go/rt/live.go` (embedded JS) — add `eventSource.addEventListener('patches', e => { const patches = JSON.parse(e.data); __skyApplyPatches(patches); ackSeq(...); });`.
2. Reconnect-resync path stays on full-body SSE (documented in-line).
3. `runtime-go/rt/live_sse_patches_client_test.go` (NEW) — Playwright fixture asserting patches-shape SSE drives the client correctly.
4. Update `docs/skylive/architecture.md` to document the patches-shape SSE transport.

#### Files touched
- `runtime-go/rt/live.go` (embedded JS)
- `runtime-go/rt/live_sse_patches_client_test.go` (NEW Playwright fixture, ~100 LOC)
- `docs/skylive/architecture.md`

#### New tests
- Playwright fixture (above).

#### Rollout / regression gates
- All 27 examples build clean.
- `scripts/verify-all-web.sh` green incl. new patches-shape fixture.
- Bandwidth measurement: re-run the optional logging from P50a; assert bytes/session reduction on a representative app (e.g. examples/14-skylive-counter — Time.every-driven; expect >80% reduction).
- C1 fully closed (suppression becomes `patches.length === 0`).
- C2 fully closed (prevBody dual-meaning vanishes — patches transport doesn't need it; can DEPRECATE post-P50b if all paths route through patches).

#### Risk register
- Risk: `__skyApplyPatches` has a latent bug when patches don't originate from the HTTP path. Mitigation: existing HTTP-path tests cover the function shape; new Playwright fixture exercises the SSE path.
- Risk: focus preservation regression (the function may have HTTP-path-specific assumptions). Mitigation: explicit focus-restoration test in the Playwright fixture.

#### Session cost
6-10 hours, 1 PR.

---

## Carry-forward items (still OPEN from cycles 1 + 2)

### Cycle 1 carry-forward (estimates UNCHANGED from cycle-1 plan unless noted)

| ID | Subject | Branch | Tag | Hours | Notes |
|---|---|---|---|---|---|
| P14 | A3 + A11 + A13 — SSE channel race + lastSeen race + view-panic recovery | `feat/v0.15.x-hardening-P14-sse-session-channel-race` | v0.15.20 | 6-8 | Folds P17/P18. **Cycle-3 dependency**: should ship before P36 (C4 TTL leak) if possible to reduce overlap. |
| P15 | A7 — SSE encoder chokepoint | `feat/v0.15.x-hardening-P15-sse-encoder-chokepoint` | v0.15.21 | 2-3 | C12 restates A7; resolved here. |
| P16 | A9 — CSRF constant-time floor | `feat/v0.15.x-hardening-P16-csrf-constant-time-floor` | v0.15.22 | 3-4 | Unchanged. |
| P19 | A6 broader — typed secret-store audit | `feat/v0.15.x-hardening-P19-typed-secret-store-audit` | v0.15.23 | 4-6 | Unchanged. |
| P10 | foundation: GoType ADT | `feat/v0.15.x-hardening-P10-gotype-adt` | TBD post-cycle-3 | 6-8 | DEFERRED beyond cycle-3 window. |
| P11 | prior #3,#4,#7,#10,A8,A12 — coerce via GoType | `feat/v0.15.x-hardening-P11-coerce-via-gotype` | TBD | 12-16 | DEFERRED. |
| P12 | prior #4,#16 — erase TVars structural | `feat/v0.15.x-hardening-P12-erase-tvars-structural` | TBD | 3-5 | DEFERRED. |
| P13 | A8, prior #10 — curry adapter bracket depth | `feat/v0.15.x-hardening-P13-curry-adapter-bracket-depth` | TBD | 3-5 | DEFERRED. |
| P20 | infer-arm coverage lock | `feat/v0.15.x-hardening-P20-infer-arm-coverage-lock` | TBD | 3-4 | DEFERRED. |
| P21 | coerce-arg branch coverage | `feat/v0.15.x-hardening-P21-coerce-arg-branch-coverage` | TBD | 4-6 | DEFERRED. |
| P22 | wildcard-any gate lock | `feat/v0.15.x-hardening-P22-wildcard-any-gate-lock` | TBD | 2-3 | DEFERRED. |
| P23 | rt.Coerce Kind fence | `feat/v0.15.x-hardening-P23-rt-coerce-kind-fence` | TBD | 3-4 | DEFERRED. |
| P25 | A10 — cross-module annot order | `feat/v0.15.x-hardening-P25-cross-module-annot-order` | TBD | 4-6 | DEFERRED. |
| P26 | FFI escape-hatch audit | `feat/v0.15.x-hardening-P26-ffi-any-boundary-audit` | TBD | 10-14 | DEFERRED. |
| P27 | save/create silent failure | `feat/v0.15.x-hardening-P27-save-create-silent-failure` | TBD | TBD | DEFERRED. |
| P28 | any-typed Result on call expecting String | `feat/v0.15.x-hardening-P28-any-typed-result-string` | TBD | TBD | DEFERRED. |

Note: cycle-1 P7 (was v0.15.13) is **CANCELLED — replaced by P37a + P37b in this cycle.** Cycle-1 P8/P9 are SUPERSEDED/DEFERRED (P37b handles let-body migration directly; whitelist drop awaits P11).

### Cycle 2 carry-forward (estimates UNCHANGED from cycle-2 plan)

| ID | Subject | Branch | Tag | Hours | Notes |
|---|---|---|---|---|---|
| P29 | B1 — module-prefix alignment | `feat/v0.15.x-hardening-P29-alias-base-prefix-shared-helper` | v0.15.32 | 3-4 | C8 restates B1; resolved here. |
| P30 | B2 — alias identity (mod,name) | `feat/v0.15.x-hardening-P30-alias-identity-module-qualified` | v0.15.33 | 5-7 | Unchanged. |
| P31 | B3 — script-revive whitelist | `feat/v0.15.x-hardening-P31-script-revive-attr-whitelist` | v0.15.34 | 3-4 | C9 restates B3; resolved here. |
| P32 | B4 — form-submitter mousedown capture | `feat/v0.15.x-hardening-P32-form-submitter-mousedown-capture` | v0.15.35 | 4-5 | Unchanged. |
| P33 | B5 — form-data desync doc | `feat/v0.15.x-hardening-P33-form-data-concurrent-patch-doc` | v0.15.36 | 3-4 | Unchanged. |
| P34 | cabal-test memory pathology | `feat/v0.15.x-hardening-P34-cabal-test-rss-measurement` | v0.15.37 | 6-8 | Unchanged; absorbs cycle-1 P24. |

---

## Updated total

- **Original 34 items (cycles 1+2) + 16 new (cycles 3 P35-P50, treating P37a/P37b + P50a/P50b as 4 items, plus P45-P49 = 5) = 50 items.**
  - Cycle 3 NEW items: P35, P36, P37a, P37b, P38, P39, P40, P41, P42, P43, P44, P45, P46, P47, P48, P49, P50a, P50b = 18 NEW items.
- **44 distinct PRs** across the three cycles (P17/P18 → P14; cycle-1 P24 → cycle-2 P34; cycle-1 P7 cancelled; cycle-1 P8 superseded).
- **Patch tags this cycle**: v0.15.17 → v0.15.44 (v0.15.16 reserved for PR #86 in-flight). Cycle 3 absorbs OPEN cycle-1 P14-P19 + cycle-2 P29-P34 tags v0.15.20-v0.15.37.
- **Cumulative session-cost**: ~250-345 hours (8-11 weeks at 30h/week).
- **Cumulative new tests vs cycle 2**: +16 Go test suites + 5 cabal specs + 2 Playwright fixtures + 1 new example (27-multi-session-chat) + 2 design docs.

## Standing directions added this cycle

1. **Cycle-1 P7 is CANCELLED.** Any reference in future plans to "P7 delete scopeStateRef" is wrong — the work is split into P37a (Solver-side SolvedTypes extension) + P37b (lowerer-side migration + IORef field delete).

2. **`scopeStateRef` field-by-field migration MUST follow C5's framing.** Each remaining field (lambda-types, lambda-Go-strings) requires the same pattern: extract the data into a pure container (Solve.SolvedTypes or a dedicated pure ADT), make consumers pure, THEN delete the IORef field. NEVER attempt mechanical deletion.

3. **C3 Phase 3g implementation gated on P45 design doc.** No `feat/v0.15.x-hardening-P46-...` etc. branch starts work until P45 is merged + reviewed. The 5 prereqs MUST land in dependency order (P45 → {P46, P47} → P48 → P49). Skipping the design doc and writing API speculatively is grounds for re-spawn.

4. **C11 SSE diff-then-patch (P50a/b) is the cycle-3 strategic flagship.** Once shipped, C1 and C2 become deprecatable; `prevBody` dual-meaning vanishes. Future cycles should NOT regress to full-body SSE without explicit justification.

5. **Cycle 1 standing direction (σ/erasure/skip consensus) continues to bind.** P37b's PR description MUST explicitly state no `coerceArg`/`goExprGoType` voter changed (the let-body migration is structural, not voter-touching).

6. **Cycle 2 standing direction (B3-B5 Playwright-mandatory) continues to bind.** Add: **P48-P49 multi-session work is Playwright-mandatory** in `scripts/verify-all-web.sh` (new multi-tab probe).

7. **Cycle-3 new tooling-gap follow-ups**: (a) every runtime-touching PR ships its `_test.go` mate; (b) Playwright multi-session probe required for P48-P49; (c) SSE bandwidth metric (P50a optional logging) becomes mandatory once P50b lands; (d) `docs/v0.15.x-hardening/lowerctx-cascade-design.md` (P44) becomes the canonical reference for any future cascade work.

## Sign-off checklist additions (cycle-3 only)

16. For C5 (P37a + P37b): SolvedTypes data-shape lock test green AND let-body migration green on the P6 reproducers (CoerceArgParametricSpec + examples/16-skychess).
17. For C3 (P45-P49): design doc reviewed + cross-referenced from issue #259; multi-tab Playwright probe added to `scripts/verify-all-web.sh`.
18. For C11 (P50a + P50b): bandwidth measurement before/after committed to `docs/v0.15.x-hardening/measurements/`; demonstrates the bandwidth-reduction claim.
19. For every runtime PR (P35, P36, P39, P40, P41, P42, P46, P47, P48): `go test -race ./runtime-go/...` green AND `_test.go` mate exists (per tooling gap 1 from audit).

---

## Cross-reference

- Cycle 3 audit: `docs/v0.15.x-hardening/audits/CYCLE-03-auditor.md`
- Cycle 1 plan: `docs/v0.15.x-hardening/plans/CYCLE-01-planner.md` (P1-P28; many SHIPPED; remainder DEFERRED beyond cycle-3 window per table above)
- Cycle 2 plan: `docs/v0.15.x-hardening/plans/CYCLE-02-planner.md` (P29-P34, all OPEN — carry-forward)
- Cycle log: `docs/v0.15.x-hardening/CYCLE_LOG.md`
- Implementation reports: `docs/v0.15.x-hardening/implementations/CYCLE-*.md`
- Head arbitration (cycle 1): `docs/v0.15.x-hardening/arbitrations/HEAD-CYCLE-01-P2.md` (σ-consensus standing direction)
- Tag history: v0.15.7 (P1) → v0.15.15 (P6) — see CYCLE_LOG.md
- In-flight PRs at cycle-3 start: PR #86 (v0.15.16, SVG/MathML namespace, OPEN)
- Future design docs to be created: `docs/v0.15.x-hardening/lowerctx-cascade-design.md` (P44); `docs/phase-3g-multi-session-design.md` (P45)
