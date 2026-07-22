# AUTONOMOUS MANDATE — LSP 100% reliable for EXTERNAL dependency resolution

**Set:** 2026-07-22. **Branch:** `rewrite/rust-compiler`. **Mode:** fully
autonomous. Directive: **"use agents + grilling + autonomous mode"**, don't stop
midway.

> Follows the v1-blocker closure mandate (complete, Judge-verified) + a series of
> LSP hover fixes: internal cross-module hover now works annotated + unannotated
> (`d5e02fbd`). This mandate closes EXTERNAL dependency resolution in the LSP.

## Verbatim user goal (the authority on "done")

> yes we need LSP 100% reliable, in real practical terms... so external deps
> resolution is a must.
>
> use agents + grilling + autonomous mode now

## Scope

Make the Rust LSP fully resolve + type + hover references to BOTH classes of
external dependency, so no external ref shows `?` or a spurious "unresolved"
diagnostic in-editor:

1. **External Sky modules** (`sky add --sky` → `.skydeps/<slug>/src/*.sky`) —
   currently excluded from the LSP's module load (lib.rs:2183), so refs don't
   resolve. Load them; the cross-module hover fix (`d5e02fbd`) then covers hover
   (annotated + unannotated).
2. **Go FFI** (`Uuid.newString`, Stripe, …) — `Res::Foreign` hover returns None
   (lib.rs:310) and the LSP never loads the FFI surface, so FFI imports don't
   typecheck in-editor. Load the FFI surface types (parse `sky-ffi/*.kernel.json`
   / `.skyi` via the `ffi` crate) so imports resolve + typecheck, and wire the
   `Res::Foreign` hover arm to the `(package, func) → skyType`.

## Definition of done — "100% reliable, real practical terms"

For a project that uses BOTH an external Sky dep and a Go FFI dep:
- Hover on an external-Sky-module ref (annotated + unannotated) → shows its type.
- Hover on a Go-FFI ref (`Uuid.newString`) → shows its FFI type (not `?`).
- Goto-def / completion behave sanely for both where applicable.
- NO spurious "unresolved name" diagnostics on external imports/refs in-editor
  (the LSP diagnostics must match what `sky build` accepts).
- All existing LSP guarantees intact: nvim 17/17 gate + full `cargo test -p
  sky-lsp` green; whole-repo gate unaffected (LSP is additive — zero
  compiler/codegen/runtime change).
- Regression tests for both external classes.
- Independent Judge verifies against this file before "done".

## Method

Agents + grilling per CLAUDE.md §0.3/§0.4: architecture-consult (how the BUILD
path loads FFI surfaces + skydeps + types `Res::Foreign`, so the LSP mirrors it)
→ adversarial grill (does the plan reach the 100%-reliable bar? spurious
diagnostics? offline?) → implement → Judge.

## Resume protocol

Read THIS file + `git log --oneline -15` on `rewrite/rust-compiler`. LSP code:
`rust/crates/sky-lsp/src/lib.rs` (`load_project`/`load_dir` ~192-215, dir-walk
skip ~2183, `ref_type_string` `Res::Foreign` ~310). FFI parsing: `rust/crates/ffi/`.
