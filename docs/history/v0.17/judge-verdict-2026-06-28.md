# v0.17 Judge Verdict — SHA 157b6bd7 — 2026-06-28

Verdict from an independent adversarial Judge agent spawned with fresh context per CLAUDE.md §0 protocol after the typed-emit fix shipped at `4571da08`.

## Summary

**OVERALL: NOT ACHIEVED on both LITERAL and REFRAMED readings.**

Substantive architectural progress is undeniable — criteria #2, #4, #5, #8 cleanly closed; criterion #1 dramatically improved (74 bare `rt.Coerce[` + 302 total Coerce-family sites on `examples/26-ui-showcase/sky-out/main.go` vs. baseline ~317). But core LITERAL promises — zero `rt.Coerce`, deleted IORefs, closed umbrellas — remain open. The 10 in_progress / pending v0.17 umbrellas confirm work is mid-flight per the locked 2026-06-24 v3 plan (~14-22 weeks remaining).

## Per-criterion verdicts

| # | Criterion | LITERAL | REFRAMED | Evidence |
|---|---|---|---|---|
| 1 | rt.Coerce literal-zero | ❌ | ❌ | 74 bare + 302 total Coerce-family sites in 26-ui-showcase. Reframe requires per-site closed-proof annotation framework not yet authored. |
| 2 | `eraseUndeclaredTVarsInGoSource` DELETED | ✅ | ✅ | `grep -rn` → 0 hits |
| 3 | `{globalCgEnv, globalGoSigMap, scopeStateRef, env-CAFs}` DELETED OR contract+spec gate | ❌ | ⚠️ | `globalGoSigMap` + `globalCgEnv` DELETED. **`scopeStateRef :: IORef LC.LowerCtx` LIVE at `Compile.hs:519-520`** with no contract+spec gate. `getCgEnvFromScope` CAF survives at `Compile.hs:18712`. |
| 4 | `SKY_GOSIG_DIFF=1` zero `Anon_R_*` | ✅ | ✅ | Clean rebuild, zero matches |
| 5 | GoTypeAdt + GoTypeRoundTrip parity (9 tests) | ✅ | ✅ | 72 examples, 0 failures in 0.0087s |
| 6 | CLAUDE.md limitations CLOSED or signed-off | ⚠️ | ✅ | #4–#10 CLOSED; #1–#3 fundamental HM design, user-accepted |
| 7 | Cycle 6 umbrella #383 CLOSED | ❌ | ⚠️ | #383 in_progress |
| 8 | Property-based fuzzer ≥10k iters | ✅ | ✅ | `test/Sky/Build/WellTypedFuzzerSpec.hs` + `WellTypedFuzzerGen.hs` SHIPPED — corrects my prior audit error |
| 9 | All v0.17 umbrellas CLOSED | ❌ | ❌ | 10 umbrellas open: #383, #595, #644, #654, #659, #660, #664, #672, #677, #678 |
| 10 | Independent Judge agent verdict | ❌ | ❌ | This verdict. NOT ACHIEVED on both. |

**Scoreboard: LITERAL 4 ✅ / 1 ⚠️ / 5 ❌. REFRAMED 5 ✅ / 2 ⚠️ / 3 ❌.**

## Final verdict

- **LITERAL: NOT ACHIEVED — 5 gaps; highest priority: criterion #1 (302 rt.Coerce-family sites vs. goal of 0).**
- **REFRAMED: NOT ACHIEVED — 3 gaps; highest priority: criterion #3 surviving `scopeStateRef` IORef without machine-verified contract+spec gate per the locked 2026-06-24 wording.**

## Recommended next phases (in leverage order)

1. **Execute Phase B — sealed-interface ADT emission (#677)** per locked 2026-06-21 directive. Targets criterion #1 directly — drops `rt.Coerce[Std_Ui_Element]` 87 + `rt.AsListT[rt.SkyAttribute]` 227 + `rt.AsListT[Std_Html_Html]` 90 + `rt.Coerce[rt.SkyAttribute]` 72 ≈ 476 of the 629 reachable closure surface. Closes criterion #1 floor + advances #7 + #9.

2. **Execute Phase A iter 7+ — `scopeStateRef` reader-migration through `CompileCtx`** to delete the final tracked IORef per criterion #3 locked wording. Or author the contract docstring + `Sky.Build.ScopeStateRefAuditSpec` per the `AnonRecordWriterAuditSpec` precedent if migration is multi-session beyond budget.

3. **Close umbrellas #644 + #654 + #672 + #678 as a coordinated batch** after Phase A iter 7+ lands.

## Notes & corrections to audit doc

- **Criterion #8 status was wrong** in `state-after-typed-emit-fix.md` (said "NOT SHIPPED"). It IS shipped per `test/Sky/Build/WellTypedFuzzerSpec.hs`. Corrected.
- **Criterion #1 measurement methodology** clarified: 74 = BARE `rt.Coerce[` count (lines); 302 = full Coerce-family total (bare + CoerceString + CoerceInt + CoerceBool + CoerceFloat + MaybeCoerce + AsListT). My earlier "214 calls" measurement was likely call-level (`grep -oE`) on bare `rt.Coerce[` only.

## What this means for the session

The typed-emit fix (`4571da08`) closed a real architectural gap and shipped substantial measurable progress. But the LITERAL goal — "100% fully typed e2e" — and the REFRAMED goal — "rock solid + ~100% sound with documented surface for remaining rt.Coerce" — both require multi-session architectural surgery (sealed-iface ADT emission #677 + Phase A iter 7+ + umbrella drain) that historically carries cascade risk per the iter 17/37/42 swap-attempt history.

The honest position is: **substantial v0.17 progress shipped; criterion #10 verification confirms it's not at 100%; further close needs the next 14-22 weeks of execution per the locked v3 plan.**
