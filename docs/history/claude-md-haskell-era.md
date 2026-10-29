# CLAUDE.md: rules from the Haskell compiler era

This page keeps the parts of the repository `CLAUDE.md` that applied only to
the retired Haskell compiler (`Compile.hs`, its `IORef`s, the `cabal test`
suite and its `hspec` specs) and to the v0.17 close. They were removed from
`CLAUDE.md` in v0.29.0, when the Haskell compiler left the tree
([`legacy-compilers.md`](legacy-compilers.md)). The rules that still apply
stay in `CLAUDE.md` in a Rust form: the architecture-consult phase, the
four-citation rule against `docs/rust-rewrite/14-runtime-narrowing-taxonomy.md`,
the N-strikes circuit-breaker, the three-leg soundness stool and the
floor-authorisation rule.

The text below is copied from `CLAUDE.md` as it stood at tag `v0.28.1`.

## §0.3 rule 1: criterion #3 deletion-target wording (locked 2026-06-24)

**Criterion #3 deletion-target wording (locked 2026-06-24).**
Earlier framings of `.claude/AUTONOMOUS_GOAL.md` criterion #3
read "`globalCgEnv` + `globalGoSigMap` IORefs DELETED". That
wording UNDER-SPECIFIED the bridge IORefs (`scopeStateRef`)
and the successor CAFs (`getCgEnvFromScope`, env-CAFs) that
surfaced during the iter 17 / 37 / 42 / Class-A swap attempts.
The locked wording is:

> Criterion #3 = `{globalCgEnv, globalGoSigMap, scopeStateRef,
> env-CAFs}` DELETED **AND** any residual IORef in `Compile.hs`
> carries a machine-verified single-writer / single-reader
> monotonic contract (see
> `docs/history/v0.17-roadmap/phase-A-iter-0-anonrecords-contract.md`
> for the `globalAnonRecords` precedent and the
> `Sky.Build.AnonRecordWriterAuditSpec` verification gate).

**This is NOT a relaxation** — it is a precise specification
of the substantive purity guarantee. The original "DELETE"
wording is satisfied by deleting the named IORefs; the
"machine-verified contract" clause closes the loophole that
would otherwise let an unnamed bridge IORef survive under a
"load-bearing-but-pure" reframe (forbidden per §0 hard rule
3). The contract has TWO parts:
- Source-level contract docstring naming the writer site +
  reader sites + monotonic invariant (e.g. "register-on-
  first-mention; never overwrites; end-of-module barrier").
- Spec gate (cabal-test) that builds a multi-module fixture
  and asserts the invariant programmatically — a write that
  overwrites OR reads a stale value MUST fail the gate.

Any "close" claim against criterion #3 cites BOTH the named-
IORef deletions AND the surviving-IORef contract+spec
pair. Judge verdicts that PASS without the second citation
are rejected.

## §0.3 rule 5: the v0.17 floor authorisation

This authorisation was scoped to the v0.17 close. It does not carry to a new
floor-touching tactic, which still needs the user's authorisation
(`CLAUDE.md` §0.3 rule 5).

**AUTHORIZED 2026-06-23**: user has explicitly authorised
floor-touching tactics for v0.17 close (literal-zero
rt.Coerce via runtime rewrite — see
`docs/history/v0.17-roadmap/literal-zero-close-plan.md`).
That authorisation is scoped to the v0.17 close and does not
carry to a new floor-touching tactic.

## §0.4: v0.17 history cited as precedent

- The phase pattern (decide, plan, execute, verify) "is the pattern that
  shipped v0.17.0 (typed-emit fix + documented rt.Coerce surface +
  scopeStateRef contract + panic-class gate locks) in additive phases with
  zero regression."
- The agent and grilling pattern was the default "for any work where solo
  execution carries cascade risk (Compile.hs surgery, multi-system changes,
  broad audits)". Sessions that skipped it "historically produced cascade
  regressions (iter 17 / 37 / 42 / Class-A swap attempts)".
- The three-leg soundness stool named the Haskell gates: the emission-time leg
  was `Sky.Build.PanicClassGateSpec`, and the fuzzer leg was
  `Sky.Build.WellTypedFuzzerSpec` (10k iterations).
- The N-strikes circuit-breaker was written after three failed attempts on
  one lever: iter 17 / 37 / 42 / the Class-A swap.
- Reframed goals: the example was v0.17 "100% fully typed" reframed to "rock
  solid + ~100% sound with documented surface".
- Push discipline: "v0.17.0 closure shipped this way (5 commits in 2 pushes vs
  the 6 individual pushes that preceded the correction)."
- Context discipline: "For `Compile.hs` (23k lines), know the line number
  before reading." The section folded in `docs/session-protocol.md`.
- A checkpoint file example was `docs/history/v0.17/session-N-checkpoint.md`.

## §0.2: cabal wording

§0.2 said "Full cabal test suite + full example sweep + verify scripts run
ONLY at milestone boundaries", and forbade waiting "for cabal-test for 25min
via ScheduleWakeup". The Rust form is the same rule with `cargo test` and the
xtask gates.

## §1: mem-guard process list

§1 listed "legacy `cabal` / `ghc` / `ghc-iserv` / `haskell-language-server`
still covered" by `scripts/mem-guard.sh`. The script no longer watches them.
