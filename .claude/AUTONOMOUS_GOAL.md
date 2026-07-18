# AUTONOMOUS MANDATE — Rust rewrite of the Sky compiler

**Set:** 2026-07-18. **Branch:** `rewrite/rust-compiler`. **Mode:** fully
autonomous, user away. Directive: **"don't stop midway."**

> NOTE: This supersedes the previous v0.17 Haskell-close mandate on THIS branch.
> That mandate's history lives on `feat/v0.17-*` branches + `main`; not active here.

## Verbatim user goal (the authority on "done")

> document fully how to implement the Rust compiler + tooling + LSP properly in
> the desired architecture — goals are to be at least compatible or better than
> current sky's compiler, also examples must be run correctly (syntax is
> correct, so should be able to run no problem)
>
> go fully autonomous mode … don't stop midway

**Scope (user-selected 2026-07-18):** *"Push through milestones (as far as it
verifies)."* Finish the blueprint, then implement M0→M1→M2→M3, going as far as
each milestone **verifies clean against the Haskell differential oracle**. Stop
ONLY at a genuine blocker or a milestone that won't verify — document + notify,
never fake-pass.

## Non-negotiables

- Compatible-or-better than the Haskell compiler; the **42 `examples/*` build
  AND run** (not build-only). The Haskell compiler (`exe:sky`) is the ORACLE.
- Laws L1–L10 (`docs/rust-rewrite/00-goals-and-principles.md`): no global mutable
  state; salsa query core; intern to ids; determinism as a tested invariant;
  enforced crate DAG; no panics on well-typed input; typed lowering IR; keep Go.
- No fake greens. Verify each milestone with a REAL command against the
  oracle/corpus; read logs not exit codes. Env gotchas already learned: zsh
  `noclobber` blocks `>` on existing files (use `>|`); commands containing
  `python3` are blocked (use perl); Haskell build target is `exe:sky` (no `lib:`).
- Commit at every gate; **push at milestone boundaries** (user reviews remotely);
  PushNotification at majors + at any genuine blocker.

## Milestone gates (each = commit + push + notify)

- **BLUEPRINT** ✅ docs/rust-rewrite/README + 00–12 authored (14 docs, ~5.8k lines).
- **M0** — `rust/` Cargo workspace, all crates per doc 02, `cargo build` green
  (stubs + minimal salsa db skeleton compiles).
- **M1** — `syntax` crate: lexer + layout + rowan CST + parser + AST view.
  GATE: reprint(parse(f)) == f for **all 42 examples' .sky files** + tests green.
- **M2** — `hir` crate: name resolution (doc 05). GATE: resolves corpus with no
  unresolved-name errors the oracle doesn't also emit.
- **M3** — `ty` crate: HM inference (doc 06). GATE: inferred top-level types
  match the Haskell oracle on the corpus (differential). Hardest gate.

(M4 lower+emit byte-compat, M5 full build+run, M6 LSP 17/17, M7 repro gate,
M8 cutover — beyond this run unless it keeps verifying.)

## Resume protocol (if compacted / new session)

1. Read THIS file + `docs/rust-rewrite/12-migration-and-milestones.md`.
2. `git log --oneline -20` on `rewrite/rust-compiler` — last committed gate = the
   resume point.
3. Continue the next milestone. Do NOT restart from scratch; do NOT narrow the
   goal. The oracle + the 42 examples are the acceptance truth. Keep laws L1–L10.
