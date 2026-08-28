# AUTONOMOUS GOAL — get main release-ready (2026-08-28)

## The user's verbatim mandate

> ok fully e2e unattended+autonomous + PIV
> until you get to main ready to release

(Follows the merge-readiness discussion: this branch — feat/unified-app-builder,
161 files, +10.3k/−2k, carrying unified Std.App + front-door deprecation + the
BREAKING Secret migration + Sky.Spa + embedded-Postgres + the erasure-collision
soundness fixes — is a clean fast-forward onto main. §0.2.1 says a merge to main
is gated by the FULL suite, not the per-commit subset.)

## Definition of done — "main ready to release"

1. The FULL §0.2.1 merge gate is GREEN (run to completion, nothing skipped that
   the environment can run):
   - `cargo test --workspace` WITH live tests (a real PostgreSQL cluster up — NOT
     SKY_LIVE_TESTS=skip).
   - `xtask harness --tier t2` (behaviour-corpus — the v0.21.0-class catcher).
   - `xtask harness --tier t3` (apps-postgres + multi-replica fleet).
   - `xtask harness --verify-falsifiers --tier t1` (falsifier proofs).
   - `scripts/example-sweep.sh` full build+run on a clean slate.
   - `scripts/conformance.sh`.
   - coerce-floor / denominators / coverage-ledger / config-* all `--check` green.
   If a tier genuinely cannot run in THIS environment (e.g. PG shmget limit), say
   so LOUDLY with the reason and confirm CI covers it — never silently skip.
2. Every red is fixed at root cause (PIV: architecture-consult for compiler
   changes, regression-test-first), re-verified. NO "deferred / pre-existing /
   out of scope" framing.
3. Release prepared: version bump (recommend v0.23.0 — breaking minor) with
   CHANGELOG.md entry + README banner + AGENTS.md "Current line" in sync (the
   docs_state_the_current_version gate must be green).
4. Fast-forward merge to main.

## Reserved for the user (durable rule — do NOT do autonomously)

- `git tag` + `gh release` publish (the actual RELEASE). Stop at merged+versioned
  main; hand the release button to the user.
- The final VERSION NUMBER is the user's call (I set a recommended one for gate
  consistency; they adjust before the tag).

## Standing constraints

- darraghstudio HARD HOLD — never touch/deploy/upgrade.
- No co-author wording in commits.
- Memory: the box is swap-constrained this session — run gates SERIALLY at LOW
  parallelism, clean orphan go/sky processes between phases, distinguish
  mem-guard-kill flakes (CodegenBug on a control/known-pass case, SIGKILL
  signature) from real failures (re-run to confirm before treating as a bug).

## Loop state

- Phase 0 — setup: goal captured; relieve memory; provision a real PG cluster.
- Phase 1 — workspace + live tests (PG up).
- Phase 2 — T2 behaviour-corpus.
- Phase 3 — T3 apps-postgres + fleet.
- Phase 4 — example-sweep (full) + conformance.
- Phase 5 — falsifiers + census --check.
- Phase 6 — version bump + CHANGELOG + doc sync; FF merge to main.
- Phase Z — Judge verifies "release-ready"; hand release to user.
