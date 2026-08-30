# AUTONOMOUS MANDATE — kernel-metadata unification to fully close reviewer item 2

## Verbatim goal (user, 2026-08-30)

> ok please do, as in fully unattended+autonomous+PIV mode
> no more inputs from me as I'm away now

(In reply to my offer: "I can take on the kernel-metadata unification next to
fully close item 2.")

## Definition of done (Judge verifies the LITERAL claim)

Reviewer **item 2** — "unknown qualified members (e.g. `List.sum`) resolve to
`any` and are rejected only at codegen `[E4005]`; the checker resolving unknown
qualified members to `any` is a smell" — is FULLY CLOSED:

1. An unknown qualified kernel member (`List.sum`, `Basics.remainderBy`, any
   `Mod.fn` where `Mod` is a kernel module and `fn` is not a real member) is
   **rejected at type-check** with a clear naming/type error — NOT deferred to a
   codegen `[E4005]`, and NOT silently resolved to `any`.
2. **ZERO false positives**: every REAL kernel function still resolves and
   compiles, both QUALIFIED (`List.sortWith`) and via `exposing` (`import
   Sky.Core.List exposing (sortWith)`), and every example / corpus / conformance
   / real app still builds+runs.
3. A **single source of truth** for kernel-function membership, synced across the
   compiler tables (`KERNEL_MODULES` / `PRELUDE_QUALIFIERS` / `KERNEL_FUNCTIONS`
   in `rust/crates/hir/src/kernel.rs`), the stdlib `.sky` `exposing` lists, and
   the runtime `rt.*` exports — with a **drift gate** that FAILS CI if they
   diverge (so this class cannot silently recur).
4. The confirmed drift is fixed: `sortWith` (real, in runtime, missing from
   source/exposing) becomes importable; `parallelMap` (in the prelude list, NO
   runtime impl) is reconciled (implemented or removed).

## Verification (Judge, fresh context, adversarial)
- `List.sum` / `Basics.remainderBy` → rejected at `sky check` (type/naming error,
  not codegen E4005).
- `List.sortWith` works qualified AND via `exposing`.
- `cargo test --workspace` + full example sweep + conformance + corpus + the new
  drift gate all green. coerce-floor unchanged (this is resolution, not emission).
- No forbidden framings ("but/except/mostly/…") in the PASS verdict.

## Constraints
- PIV: architecture-consult (`docs/rust-rewrite/`) BEFORE tactics; grill; Judge at
  close. Root-cause fixes only. No co-author line. Batch pushes at milestones.
- Related: memory `flagged_items_and_kernel_metadata`,
  `v0_19_kernel_metadata_unification`. The wildcard-`any` mechanism is
  load-bearing (`sky_wildcard_any_soundness`) — do not destabilise it; this adds
  a NAME-EXISTENCE check, distinct from type inference.

## PROGRESS
- P0 architecture-consult: DONE (PROCEED). Hole = resolve.rs:2746/2753 kernel-pseudo
  fallback returns Res::Kernel unvalidated. SSOT = .sky (v0.19 direction). Reject
  validates against kernel_functions(pseudo); zero-false-positive invariant =
  KERNEL_FUNCTIONS ⊇ every real callable member (member REAL iff kernel_go_name in
  runtime_exports).
- P1 drift gate: DONE (worktree agent, commit 3b71229b). `xtask kernel-members`.
  Drift is repo-wide: 18 modules, ~78 ambient members missing from KERNEL_FUNCTIONS
  (all KERNEL_TABLE-backed + real → add), 2 phantoms (List.parallelMap, Io.readBytes
  → remove), 4 List.sky bindings missing (sort/sortBy/sortWith/filterMap → add),
  17 Db runtime-only symbols (add getFloat; mark other 16 internal — sweep arbitrates).
- P2-P4: IN PROGRESS (agent resumed with the include-all policy; example-sweep is the
  false-positive arbiter — promote any now-rejected symbol an app actually uses).
