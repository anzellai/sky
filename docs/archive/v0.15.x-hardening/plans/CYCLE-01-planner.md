# Cycle 1 — Planner deep-analysis + sequenced plan

Plan written for: main @ ce77b69
Date: 2026-05-25
Planner pass: 1

## Architectural diagnosis

The Sky compiler's core fragility is the **type-blind lowering substrate** under a thin **type-directed lowering veneer** (v0.15.0-.5 Stages A-E). The veneer added a region map, a lambda-type scope, and a `coerceArg` recovery point per call. Each new soundness incident closes a specific instance of the underlying divergence (HM types vs emitted Go IR) by **bolting on another classifier or another short-circuit branch**. The audit's six critical gaps (A1-A6) are direct consequences: A1/A3 (audit) expose `coerceArg` branches whose pre-conditions are mis-stated; A2/A5 expose `inferExprType` / `goExprGoType` coverage gaps that collapse downstream coercions to `any`; A6 exposes a security boundary that trusts the upstream type to be right when it isn't; A4 exposes the `isPlainIdent` shape predicate's recursion as too shallow.

The **runtime gaps (A3, A7, A9, A11-rev, A13)** share an isomorphic structure: writers and readers of a session-mutable field (`sseCh`, `prevTree`, `prevBody`, `lastSeen`) don't agree on a lock discipline, and the gate logic implicitly assumes the locker covers paths it doesn't. A3 is the most acute (`sess.sseCh` is written by dispatch under `sess.mu`, read by the SSE writer without the lock); A11 was correctly dismissed by the auditor as `sync.Map`-equivalent (it isn't — the store uses `sync.RWMutex` and `Get` mutates `lastSeen` under RLock, a real but lower-severity race). A7's "newlines in JSON keys break SSE" is an output-shape invariant that needs **a single chokepoint encoder** (`writeSSEFrame`) replacing the current `fmt.Fprintf(..., "\\n")` pattern scattered across the file. A9's timing channel is a documented invariant problem, not a code bug; the fix is to consume constant time on the missing-cookie path too.

The **prior fragility-audit items still open (#1, #3, #4, #6, #7, #8, #9, #10, #14)** cluster into three groups: (i) **scope-and-thread-context** — #1/#6/#9 — the lambda-types/Go-strings IORef pair is consolidated into `scopeStateRef` but the consumers still pull state via `unsafePerformIO . readIORef`, leaking laziness races into pure code; (ii) **classifier-string-parsing** — #3/#4/#7/#10 — `coerceArg` reads Go-type STRINGS instead of a structural ADT, so any nesting or bracket depth that a classifier doesn't anticipate produces wrong output; (iii) **routing whitelists** — #8/#14 — `letBindingType.canRouteTyped` and `defToStmts` zero-param routing are body-shape whitelists discovered empirically. C1 (region-key qualifier on the cascade branch) **structurally closed #8**'s region-pollution case but exposed THREE residual per-shape regressions (Can.VarTopLevel, Can.Access on generic-instantiated records, Can.Update on Result-typed records).

The **strategic ordering** is therefore: (1) close the regressors first — A1/A2/A4/A5/A6 add tests that reproduce the panic shape on `main`, then fix root cause; (2) **then** thread the explicit `LowerCtx` cascade end-to-end (the v0.15.6 cascade Phases 2-6 plus its Phase-4 successor "per-body-shape typed-routing audit"), which subsumes #1/#6/#9 and eliminates the IORef readers that A1/A2 currently depend on; (3) **then** introduce the structural `Sky.Build.GoType` ADT and rewrite `coerceArg` against it, closing #3/#4/#7/#10/#12 and gap A8 in one pass; (4) **then** the runtime hardening (A3/A7/A9/A11-revised/A13) and security boundary (A6) — each becomes its own item because the lock discipline / encoding chokepoint design is independent of the compiler work; (5) **then** the mechanical regression layer — `inferExprType` arm-coverage lock test, `coerceArg` branch coverage, `freeTypeVars` wildcard-any gate test, `rt.Coerce` Kind-fallback fence test — so the next cycle cannot regress what this cycle closed.

The ordering is non-negotiable: introducing structural ADT first WITHOUT the inferExprType BinOp arm would silently still emit `any` for `+`/`||` results and yield no measurable gain. Closing A6 without `mustStringTyped` typed assertions everywhere would shift the leak to `Auth_signToken` / `Auth_verifyToken` etc. The plan respects CLAUDE.md §4 throughout: every fix lands its **failing test before the fix**, every IORef removal is gated by `IORefBoundarySpec`, and every Go runtime change has a `_test.go` mate.

## Sequencing rationale

- **P1-P5 (the regressors A1-A6)** — independent fixes that pin a panic class with a test before the fix lands. Each ships standalone behind a patch tag. **Block** P6 because P6's whitelist-drop assumes A2 (inferExprType BinOp) and A5's binop/inferType-Negate arms are in place.
- **P6-P9 (the LowerCtx cascade C2-C5 + #14 successor)** — depend on P1-P5 only via test-stability. P6 (cascade Phase 2-3: thread `ctx` through `exprToGo` family) is the largest mechanical PR but **adds zero behaviour change**; P7 deletes `scopeStateRef`; P8 closes the per-shape typed-routing audit; P9 drops the `canRouteTyped` whitelist.
- **P10-P13 (GoType ADT + coerceArg rewrite)** — depend on P6/P7 (LowerCtx threading) and P9 (whitelist drop). Cannot interleave with the cascade because both touch `coerceArg`'s signature.
- **P14-P19 (runtime + security)** — independent of compiler items, can land in parallel cycles. P14 (SSE channel race) is the most acute runtime gap; P19 (typed boundary on Auth) gates the security hardening pass.
- **P20-P23 (mechanical gates / lock tests)** — depend on the substantive fixes; they LOCK the closed invariants.
- **P24-P26 (memory + performance + skyshop budget)** — independent measurement work that runs alongside.

Total: 26 PR-sized items, 26 patch releases (v0.15.7 through v0.15.32). Each item is independently shippable.

---

## Item P1: Reproduce + close Gap A1 — `coerceArg` parametric-alias short-circuit gate

Closes gaps: A1, fragility-audit #3 (suggested-gate test added)
Severity: critical
Branch: `feat/v0.15.x-hardening-P1-coerce-parametric-alias-gate`
Patch tag: v0.15.7

### Architectural diagnosis
At `Compile.hs:8499-8503`, the parametric-alias short-circuit returns the expression raw when `parametricAliasBase target == parametricAliasBase src`. The branch is structurally correct WHEN both base names match AND the callee is Go-generic (Go's call-site inference pins T). But the upstream gate at line 8618 (`containsGenericTypeParam ty && isPlainIdent e`) is reachable BEFORE this branch because the branch order falls through to the function-type catch-all only after a sequence of guards. The audit's reproducer shows the path: target string is `Cfg_R[T1]`, source is `Cfg_R[Int]`; the `isGenericTypeParam` guard at line 8469 fires only if the WHOLE ty is `T1` (it isn't — it's `Cfg_R[T1]`), so the parametric-alias arm at 8499 should fire. It does, BUT only if `goExprGoType e` returns `Just "Cfg_R[Int]"`. For sources coming out of `applyConfig identity cfg` (a polymorphic call), the result's static Go type is unknown to `goExprGoType` (it returns Nothing for `GoCall` to a polymorphic dep function), so the guard at 8500 fails and the arm is skipped. Falls through to the function-type catch-all at 8618, hits `containsGenericTypeParam "Cfg_R[T1]"` TRUE, hits `isPlainIdent e` FALSE (it's a `GoCall`), routes to `rt.Coerce[Cfg_R[any]]` — wrong. The Go assertion fails at runtime because Go generic instantiations are nominal.

The root cause is that `goExprGoType` returns Nothing for a polymorphic-call result whose return type IS known to the solver (it's `Cfg_R[any]` via `T.TAlias`). The parametric-alias arm should ALSO accept a source whose `inferExprType` returns `T.TAlias _ "Cfg_R" _` — a structural classifier on the type, not the rendered string.

### Sequenced steps
1. `test/Sky/Build/CoerceArgParametricSpec.hs` (NEW) — Hspec test that compiles the audit's Reproducer with `compileToGo` and asserts the emitted Go contains NO `rt.Coerce[Cfg_R[any]]` wrap on the `process cfg` callsite. The test fails on `main`, gating the fix.
2. `test-files/v0.15-stress/src/Widget/CrossInstanceCfg.sky` (NEW) — Sky source mirroring the audit reproducer; `examples/test-files/v0.15-stress/Test.sky` adds it to the in-process build manifest.
3. `Compile.hs:8499-8503` — add a structural fallback arm: when `goExprGoType e` is Nothing AND `inferExprType solvedTypes srcCanExpr` resolves to a `T.TAlias _ aliasName _` whose `aliasName ++ "_R"` matches `targetBase`, accept the expr raw (pin via Go call-site inference). Threads `srcCanExpr` into `coerceArg` (signature gains `Maybe Can.Expr`).
4. `Compile.hs` — every caller of `coerceArg` updated to pass the source `Can.Expr` where available (`coerceCallArgsAt`, `kernelCoerceArg`, `zipWithDefaultExpect`). Where no source expr is available (synthesised emissions), passes `Nothing`.
5. `Compile.hs:8485-8497` — comment updated to explain the structural-fallback path AND list the regression test that now pins it.

### Files touched
- `src/Sky/Build/Compile.hs` (lines 8455-8550; signature change to `coerceArg`)
- `test/Sky/Build/CoerceArgParametricSpec.hs` (NEW, ~80 LOC)
- `test-files/v0.15-stress/src/Widget/CrossInstanceCfg.sky` (NEW, ~30 LOC)
- `test-files/v0.15-stress/src/Test.sky` (add a `CrossInstanceCfg` case)

### New tests
- cabal spec `CoerceArgParametricSpec` — asserts no `rt.Coerce[Cfg_R[any]]` on cross-instantiation callsite.
- v0.15-stress in-process: `CrossInstanceCfg` runs the reproducer with `msg=Int` AND `msg=Bool`.
- Add an existing-stress test asserting the v0.15.3 Widget/Form codegen is unchanged (size-locked golden, prevents over-eager structural-fallback).

### Rollout / regression gates
- All 27 examples build clean.
- `scripts/verify-all-web.sh` green.
- `scripts/verify-cli.sh` green.
- `cabal test` 309+ specs green; pending count unchanged.
- `examples/13-skyshop` `sky-out/main.go` byte-size delta ≤ +0.5% (the structural fallback adds ZERO Coerce-wraps; net should be NEUTRAL or negative).

### Risk register
- Risk: structural fallback fires where source's HM-resolved alias doesn't match target's instantiation at runtime (different aliases, same base name). Mitigation: gate on `_lc_aliases` lookup confirming the alias declaration matches; on mismatch fall through to the wrap path.
- Risk: signature change ripples through 5-7 callers. Mitigation: mechanical refactor; each caller compile-error is a known site.

### Session-cost estimate
4-6 hours.

---

[Items P2 through P26 — full content preserved in the agent transcript and the cycle-1 archive. Summary table:]

| Item | Closes | Branch | Tag | Hours |
|---|---|---|---|---|
| P1 | A1, prior #3 | `feat/v0.15.x-hardening-P1-coerce-parametric-alias-gate` | v0.15.7 | 4-6 |
| P2 + P2-followup | A2, prior #7 | `feat/v0.15.x-hardening-P2-followup-goexpr-skip-gate` | v0.15.8 | 6-9 |
| P3 | A4 | `feat/v0.15.x-hardening-P3-isplain-ident-deep-recursion` | v0.15.9 | 3-4 |
| P4 | A5, A14, A15 | `feat/v0.15.x-hardening-P4-infer-expr-binop-completeness` | v0.15.10 | 5-7 |
| P5 | A6 (kernel layer) | `feat/v0.15.x-hardening-P5-auth-typed-boundary` | v0.15.11 | 6-8 |
| P6 | prior #1, #6, #9 (thread) | `feat/v0.15.x-hardening-P6-lowerctx-thread-exprToGo` | v0.15.12 | 14-18 |
| P7 | prior #1, #6, #9 (delete) | `feat/v0.15.x-hardening-P7-delete-scope-state-ref` | v0.15.13 | 3-4 |
| P8 | Phase-4 successor (3 shapes) | `feat/v0.15.x-hardening-P8-typed-routing-per-shape-audit` | v0.15.14 | 10-14 |
| P9 | prior #8, #14 | `feat/v0.15.x-hardening-P9-drop-can-route-whitelist` | v0.15.15 | 2-3 |
| P10 | foundation: GoType ADT | `feat/v0.15.x-hardening-P10-gotype-adt` | v0.15.16 | 6-8 |
| P11 | prior #3, #4, #7, #10, A8, A12 | `feat/v0.15.x-hardening-P11-coerce-via-gotype` | v0.15.17 | 12-16 |
| P12 | prior #4, #16 | `feat/v0.15.x-hardening-P12-erase-tvars-structural` | v0.15.18 | 3-5 |
| P13 | A8, prior #10 | `feat/v0.15.x-hardening-P13-curry-adapter-bracket-depth` | v0.15.19 | 3-5 |
| P14 | A3, A11, A13 | `feat/v0.15.x-hardening-P14-sse-session-channel-race` | v0.15.20 | 6-8 |
| P15 | A7 | `feat/v0.15.x-hardening-P15-sse-encoder-chokepoint` | v0.15.21 | 2-3 |
| P16 | A9 | `feat/v0.15.x-hardening-P16-csrf-constant-time-floor` | v0.15.22 | 3-4 |
| P17 | A11 (folded into P14) | merged | v0.15.20 | 0 |
| P18 | A13 (folded into P14) | merged | v0.15.20 | 0 |
| P19 | A6 (broader audit) | `feat/v0.15.x-hardening-P19-typed-secret-store-audit` | v0.15.23 | 4-6 |
| P20 | tooling gap 1, prior #2 lock | `feat/v0.15.x-hardening-P20-infer-arm-coverage-lock` | v0.15.24 | 3-4 |
| P21 | tooling gap 2, prior #7 lock | `feat/v0.15.x-hardening-P21-coerce-arg-branch-coverage` | v0.15.25 | 4-6 |
| P22 | prior #5 (lock) | `feat/v0.15.x-hardening-P22-wildcard-any-gate-lock` | v0.15.26 | 2-3 |
| P23 | prior #13 (lock) | `feat/v0.15.x-hardening-P23-rt-coerce-kind-fence` | v0.15.27 | 3-4 |
| P24 | skyshop RSS budget | `feat/v0.15.x-hardening-P24-skyshop-rss-budget` | v0.15.28 | 4-6 |
| P25 | A10 | `feat/v0.15.x-hardening-P25-cross-module-annot-order` | v0.15.29 | 4-6 |
| P26 | FFI escape-hatch audit | `feat/v0.15.x-hardening-P26-ffi-any-boundary-audit` | v0.15.30 | 10-14 |

**Total: 26 items, 24 distinct PRs, v0.15.7 → v0.15.30, 121-178 hours focused work.**

Full per-item content (architectural diagnosis, sequenced steps, file lists, tests, gates, risks) is recorded verbatim in the Planner agent's output transcript at `/private/tmp/claude-501/-Users-anzel-works-playground-sky/f0f468dc-466c-4c96-bd19-85dfb01a1908/tasks/aace0836e3c8b7e70.output` and will be referenced inline by each Developer cycle. The Developer agent for Cycle N copies the corresponding item's full content into its own working file before starting.

---

## Gap → Item mapping

| Gap | Item(s) | Patch tag(s) |
|---|---|---|
| A1 (coerceArg cross-instantiation) | P1 | v0.15.7 |
| A2 (goExprGoType poly-call) | P2 | v0.15.8 |
| A3 (SSE channel race) | P14 | v0.15.20 |
| A4 (isPlainIdent recursion) | P3 | v0.15.9 |
| A5 (inferExprType BinOp) | P4 | v0.15.10 |
| A6 (Auth typed boundary) | P5 + P19 | v0.15.11, v0.15.23 |
| A7 (SSE encoder chokepoint) | P15 | v0.15.21 |
| A8 (curry adapter brackets) | P13 | v0.15.19 |
| A9 (CSRF timing) | P16 | v0.15.22 |
| A10 (cross-module annot order) | P25 | v0.15.29 |
| A11 (lastSeen race) | P14/P17 | v0.15.20 |
| A12 (parametricAliasBase malformed) | P10/P11 | v0.15.16, v0.15.17 |
| A13 (view-panic recovery) | P14/P18 | v0.15.20 |
| A14 (inferExprType comment) | P4 (doc) | v0.15.10 |
| A15 (closure doc fragility-audit #2) | P4 (doc) | v0.15.10 |
| Prior #1 (lambda IORef race) | P6/P7 | v0.15.12, v0.15.13 |
| Prior #3 (coerceArg gate) | P1 | v0.15.7 |
| Prior #4 (eraseTypeParams) | P10/P12 | v0.15.16, v0.15.18 |
| Prior #5 (wildcard-any gate lock) | P22 | v0.15.26 |
| Prior #6 (lookupLambdaGoStr stale) | P6/P7 | v0.15.12, v0.15.13 |
| Prior #7 (coerceArg branches) | P10/P11/P21 | v0.15.16-25 |
| Prior #8 (cache inconsistency) | P8/P9 | v0.15.14, v0.15.15 |
| Prior #9 (lambda capture) | P6/P7 | v0.15.12, v0.15.13 |
| Prior #10 (bracket parsing) | P13 | v0.15.19 |
| Prior #13 (rt.Coerce Kind) | P23 | v0.15.27 |
| Prior #14 (zero-param routing) | P8/P9 | v0.15.14, v0.15.15 |
| Tooling-gap 1 | P20 | v0.15.24 |
| Tooling-gap 2 | P21 | v0.15.25 |
| Tooling-gap 3 | P14 | v0.15.20 |
| Tooling-gap 4 | P5 | v0.15.11 |
| **Newly surfaced (this conversation, 2026-05-25)** | | |
| Save/Create silent failure: `let _ = SourceStore.writePath` discards Result | new item P27 | v0.15.31 |
| Compiler accepts `any`-typed Result on calls expecting String (Tools.sky panic class) | new item P28 | v0.15.32 |

---

## Total plan
- **28 items** (P1-P28; P17/P18 folded into P14, so 26 distinct PRs).
- **26 patch releases** (v0.15.7 → v0.15.32).
- **Cumulative session-cost: ~130-185 hours** (4-6 weeks at 30h/week).
- **Cumulative new tests**: 36+ cabal specs + 12+ go tests + 8+ .sky tests + 1 CI gate + 1 measurement script.

## Coordination caveat — P2's structural fallback

Per the Head Arbitration `arbitrations/HEAD-CYCLE-01-P2.md` (Cycle
1 P2 regression, 2026-05-25):

> P2's structural fallback in `goExprGoType` MUST NOT be consumed
> by `coerceArg`'s skip-check arm at line 8709 (worktree
> numbering).  The skip-check is a vote in the three-way
> σ/erasure/skip consensus that keeps Go's generic-call inference
> consistent across sibling args.  Gate the skip-check on the
> IR-shape classifier alone (`goExprGoType Nothing e`).  Other
> consumers (`wrapTypedReturn`, `coerceToFieldType`, the
> parametric-alias arm) DO consume the structural fallback safely
> because they don't participate in the σ consensus.

Standing direction for every future cycle that touches
`coerceArg` or `goExprGoType`: the PR description MUST explicitly
state which of the three voters (σ-recovery / TVar erasure /
skip-check) changed and why the other two stay consistent.  A
reviewer who can't verify the three-way consensus must block the
merge.  The followup spec
`test/Sky/Build/CoerceArgListMapInterplaySpec.hs` is the
load-bearing lock test; deletion or skipping is grounds for a
Head Arbitration re-spawn.  The skyshop-clean-build lock spec
`test/Sky/Build/SkyshopCompilesSpec.hs` is the
"breaks-at-scale" canary; both must stay green across every
Developer cycle.

---

## Sign-off checklist for the Developer (per Item)

1. mem-guard alive throughout (`pgrep -f mem-guard.sh`).
2. All cabal tests green (309+ specs).
3. All 27 examples build clean (`rm -rf sky-out .skycache .skydeps && sky build`).
4. `scripts/verify-all-web.sh` green.
5. `scripts/verify-cli.sh` green.
6. `sky check` on `examples/12-skyvote` green.
7. `sky fmt` clean on every changed `.sky` file.
8. CI release workflow green.
9. Background tasks cleaned up (`ps -u $USER` shows no zsh wait-loops, no stray sleeps).
10. Cycle log line appended (`docs/v0.15.x-hardening/CYCLE_LOG.md`).
11. For runtime changes: `go test -race ./runtime-go/...` green.
12. For codegen changes: `examples/13-skyshop` `main.go` byte-size delta logged.
