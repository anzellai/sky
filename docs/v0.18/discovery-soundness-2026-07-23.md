# Discovery sweep — soundness/correctness (2026-07-23)

A 5-prober + adversarial-grill workflow probed the Rust compiler for closeable
v1 soundness/correctness gaps with REAL repros (differential vs the Haskell
oracle). 10 grill-confirmed findings; 5 closed this session, 2 deferred with
rationale, 3 were duplicates/probe-artifacts.

## Closed

| # | Finding | Class | Fix | Regression |
|---|---------|-------|-----|-----------|
| A | Negative-literal `case` patterns lowered to `== 0` (`-1`/`-5` never matched, `0` wrongly matched) | **miscompile** (type-checks + go-builds, wrong at runtime) | `resolve.rs` `Pattern::Negate` arm parses the negated literal (int → `Int(-val)`, float → E1006) instead of the `Int(0)` stub | `driver_negative_literal_patterns_match_at_runtime` (build+run+assert) |
| D | `modBy` negative numerator returned Go truncated remainder, not Elm floored modulo (`modBy 3 -1` = -1 not 2) | correctness (differential) | `Basics_modBy` delegates to the already-correct `Basics_modByT` | `rt.TestBasicsModByFloored` |
| B | `Maybe`/`Result` `map2..5`/`andMap`/`combine` didn't type-check 2nd+ container payloads cross-module (accepted + panicked; oracle rejects) | **soundness** (accept-wrong) | seed cross-module check-sigs in `sig.rs` (they're unannotated in kernel modules, which the app check-sig pass skips) | `maybe_andmap_illtyped` + `result_map2_illtyped` corpus |
| C | Ordering `< > <= >=` on tuples/lists type-checked + go-built but PANICKED (`rt.AsInt: got rt.T2`) | **soundness** ("if it compiles it works") | `rt.cmp` compares composites lexicographically via `cmpComposite` (lists element-wise + shorter-prefix-less; tuples field-by-field) | `rt.TestCmpCompositeLexicographic` |
| #10 | Multiline (triple-quoted) strings didn't collapse `\\` → `\` (diverged from oracle + doc 04 §9) | correctness (differential) | `resolve.rs` `decode_multiline_escapes` (left-to-right: `\\`→`\`, `\{{`→`{{`, other `\X` verbatim) | `resolve::tests::multiline_escapes_...` |

## Known residual on B (tracked, not closed)

`map2..5` with an **inline lambda** first arg (`Maybe.map2 (\a b -> a + b) (Just
1) (Just "x")`) still slips through — the lambda body's constraints (`b` numeric
from `+`) don't thread to the later container args before they're checked. The
named-function form (`map2 addI …`) IS caught. This is a deeper checker
constraint-ordering matter (lambda-arg bidirectional inference), separate from
the missing-sig hole this session closed.

## Deferred (with rationale)

- **#6 `any -> any` passthrough relabels a value's type** (Rust accepts
  `String.fromInt (passthrough "s")`; oracle rejects). This is the DELIBERATE
  wildcard-`any` escape-hatch semantics (per-occurrence-independent, documented
  in CLAUDE.md; the D1 `any_result_check_sigs` work tuned its result-position
  soundness). Tightening it risks the D1 gate + Std.Ui (which relies on
  wildcard-`any` throughout). A policy question for the user, not a bug-fix.
- **#9 `String.fromFloat` never uses scientific notation** for very large/small
  magnitudes. Ambiguous target: RUST `1000000`→"1000000" matches Elm/JS where
  the ORACLE gives "1e+06" (wrong); RUST misses "1e+21" for 1e21 where the oracle
  is right. Neither matches Elm's JS-`toString` semantics exactly, and matching
  it would DIVERGE from the oracle (differential gates). Low-value display nicety
  — not worth the risk/effort in this patch.

## Not findings (verified)

Composite-comparison duplicate (#8 == #2, folded into C); the `map2` "silently
prints wrong output" sub-claim (it panics, not silently-wrong — the primary
accept-wrong is real and closed under B).
