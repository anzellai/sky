# AUTONOMOUS MANDATE — secure `main` to truly hold "if it compiles it works"

## Verbatim goal (user, 2026-08-29)

> yes please all the remaining gaps, holes, bugs, ci improvements etc. to make
> our main branch, which our next big release will be based on, as secured and
> as completed as possible. meaning truly holds our if it compiles it works
> status
>
> you can proceed with fully unattended+autonomous+piv mode.
>
> if you need my inputs say it now otherwise you can proceed until fully e2e
> delivered, ready for me to review + tag release

## Definition of done (Judge verifies the LITERAL claim)

`main` truly holds **"if it compiles it works"**: no known program that passes
`sky check` but fails `go build` OR panics at runtime OR returns wrong values
under well-typed semantics — AND the gates that prove this are WIRED so the
class cannot silently recur. The user reviews + tags; I do NOT tag/release.

## Scope (from this session's 5 audits + 2 confirmed+fixed breaks)

1. **Find + fix remaining soundness holes** — adversarial sweep across the hard
   classes (type-system/aliases/row-poly, FFI/rt.Coerce boundary, effects/Task,
   stdlib semantics). Every hole → fix (root cause) + regression.
2. **Enforcement wiring** (so the class can't recur):
   - erasure-fuzz into CI (runs NOWHERE today) + templates for the found breaks.
   - release.yml full-tier meta-gate (§0.2.1 unenforced prose).
   - release-gate holes: config-migration + verify-cli in no workflow; release
     runs a weaker run-set than per-commit; coerce-floor masks 5 FFI rows at
     release.
3. **Robustness**: HM solver budget (pathological module OOMs host); reconcile
   `//` by zero (panics) vs `modBy 0` (total) — make total, matching Elm + the
   no-panic promise.
4. **Polish** (non-blocking, do if time): missing List combinators, sky doc
   drift (kernel-lowered ops not surfaced), stale `--help`.

## Protocol
- Root-cause fixes only; regression-test-first; full verify (coerce-floor +
  example-sweep + erasure-fuzz + cargo test workspace + corpus) before each
  landing; batch pushes at milestones; watch CI green after each push.
- Fresh-context adversarial Judge at close on the LITERAL claim.
- NO tag/release (user's). darraghstudio HARD HOLD.
