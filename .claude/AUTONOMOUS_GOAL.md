# AUTONOMOUS MANDATE — sweep for ALL achievable v1 improvements/fixes in this patch

**Set:** 2026-07-22. **Branch:** `rewrite/rust-compiler` (PR #154). **Mode:**
fully autonomous, agents + grilling. Directive: don't stop midway.

> Follows a chain of shipped v1-closure work (v1-blockers, `_subj` hole,
> pseudo-class runtime fix, cross-module hover, external-deps LSP resolution).
> The pattern: we keep finding small gaps + missed things one at a time. This
> mandate SYSTEMATICALLY discovers everything achievable, so we stop trickling.

## Verbatim user goal (the authority on "done")

> you see, we keep finding small things we could improve on, and something we've
> missed. could you again run agents + grilling + autonomous mode to find out
> what can be done for this patch to improve/fix as much as we can for our v1
> goals?

## Scope

DISCOVER every issue/improvement that is ACHIEVABLE IN THIS PATCH (small→medium,
self-contained, low-risk, LSP/tooling/diagnostics/edge-case-correctness) and
advances v1, then FIX as many as verified-safe. Explicitly OUT: the deep §8
irreducible floor (FFI-return / wire-decode / TEA reflect-dispatch), server-shape
CI verification, known-divergences enforcement gate — those are post-merge.

Surfaces to audit (real repros via the pre-built `~/.cargo/bin/debug/sky` +
differential vs oracle `sky-out/sky`, NOT speculation):
LSP completeness (every feature × symbol class × edge case) · diagnostics
quality (message + location + missing/wrong) · parser/syntax edge cases ·
type-checker accept-wrong / reject-valid / bad-inference · codegen/runtime
well-typed-miscompile or panic (closable) · tooling papercuts (fmt/doc/test/db/
add/install/watch/doctor) · stdlib correctness/completeness · DX / "if it
compiles it works" residual closable gaps.

## Method

Workflow: parallel discovery agents (each a surface, with REAL repros) → grill
each finding (real? achievable-in-patch? genuine v1 improvement? not deep floor?)
→ synthesize a prioritized actionable list (close-now / stretch / defer) with
effort + repro. Then implement the close-now bucket (verify each empirically,
commit per fix), grill, Judge.

## ADDED 2026-07-23 (user, mid-sweep) — expanded scope

1. **`sky upgrade` not wired** — implement real self-update (was STRETCH).
2. **`sky run`/`install` progress logging** — not Haskell-compatible; log
   progress info to the console like the oracle does.
3. **Full agents scan of the `sky` CLI** — discover every verb/flag/behaviour
   NOT implemented (or stubbed / diverging from the oracle) and implement
   properly.
4. **Docs/comments tidy-up (DO LAST, after all code settles)** — README (must
   reference v0.18.x), CLAUDE.md + `templates/CLAUDE.md`, docs/*: streamline
   outdated info, ensure everything reflects the shipped state.

## Definition of done

Every close-now-bucket item implemented + verified (empirical repro fixed) +
regression test + committed. Whole-repo gate green (cargo test --workspace + all
xtask gates + golden + build-run oracle parity + nvim 17/17). Independent Judge
verifies the discovery was thorough + the shipped fixes are real. A written list
of what was found, fixed, and honestly deferred (with why).

## Resume protocol

Read THIS file + `git log --oneline -20`. Prior mandates' work is committed;
this sweep is additive. Discovery findings tracked in docs/v0.18/.
