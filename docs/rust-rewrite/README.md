# Sky Compiler — Rust Rewrite (architecture-first)

> **Branch:** `rewrite/rust-compiler`
> **Status:** DESIGN / DOCUMENTATION. No compiler code until this blueprint is
> reviewed. This directory is the full implementation guide for rebuilding the
> Sky compiler + tooling + LSP in Rust, on the architecture the Haskell journey
> taught us we needed.

## Why a rewrite (not another patch)

The Sky toolchain has been through TS → Sky-self-hosted → Haskell, and hit a wall
each time. Every wall traced to **architecture foundations**, not to a missing
feature: a 24k-line `Compile.hs` monolith no context can hold, 791 IORef/global-
mutable sites clawed back toward purity over the entire v0.17 cycle, a batch
pipeline that forced the LSP to bolt on its own fixpoint + threads, non-
deterministic emission (the reproducibility CI killer), and an `any`-boxed self-
host that couldn't type-check its own invariants. Patching the Haskell tree was
attempted repeatedly; it fails on different issues each time because the
foundations are wrong. **We now know the ups and downs — the highest-value move
is to lay the foundations right, once.**

Rust is chosen because it is the only option that satisfies every axis at once:
enforced sum-type exhaustiveness (the soundness floor), rust-analyzer-grade
tooling for the compiler's own authors, a large AI-training corpus (the primary
motivation — AI-assisted velocity), a single distributable binary, and — the part
specific to *our* diagnosis — the **arena + integer-index + salsa** idioms are the
direct fix for the exact problems this journey surfaced (variable identity for
union-find, determinism by construction, incremental LSP). It is also where the
modern language-tooling wave landed (Roc, Gleam→Rust, rust-analyzer, ruff, biome).
It gives strong ADTs + real TCO/iteration + predictable memory without Haskell's
purity tax or laziness/space-leak surprises. Full rationale: [`00-goals-and-principles.md`](00-goals-and-principles.md).

## The two hard goals (acceptance gates — non-negotiable)

1. **Compatible-or-better than today's Sky compiler.** Same language, same
   stdlib surface, same `sky.toml`, same CLI. Better diagnostics, tooling,
   reproducibility, and maintainability. See [`03-language-reference.md`](03-language-reference.md).
2. **Every existing example builds AND runs correctly.** The 42 `examples/*`
   are the conformance suite. "Syntax is correct → it must run" — a program that
   `sky check`s must `go build` and must not panic under well-typed semantics.
   The Haskell compiler is the **differential oracle** during bring-up. See
   [`11-testing-and-verification.md`](11-testing-and-verification.md).

## Document map (reading order)

| # | Doc | What it covers | Author |
|---|---|---|---|
| — | [`README.md`](README.md) | This index | spine |
| 00 | [`00-goals-and-principles.md`](00-goals-and-principles.md) | Goals, the journey's learnings as design laws, non-negotiables | spine |
| 01 | [`01-architecture-overview.md`](01-architecture-overview.md) | Query-based (salsa) core, data flow, interning, determinism | spine |
| 02 | [`02-workspace-and-crates.md`](02-workspace-and-crates.md) | Cargo workspace, crate DAG, responsibilities, key deps | spine |
| 03 | [`03-language-reference.md`](03-language-reference.md) | The Sky language the compiler must implement (compat spec) | agent |
| 04 | [`04-syntax-lexer-parser.md`](04-syntax-lexer-parser.md) | Lossless CST (rowan), error recovery, layout/indentation | agent |
| 05 | [`05-name-resolution.md`](05-name-resolution.md) | Canonicalisation, imports, qualifier rules, scopes | agent |
| 06 | [`06-type-system.md`](06-type-system.md) | HM inference, arena union-find, exhaustiveness, invariants | agent |
| 07 | [`07-lowering-and-ir.md`](07-lowering-and-ir.md) | Typed lowering IR, type-directed lowering, minimal coercion | agent |
| 08 | [`08-go-codegen.md`](08-go-codegen.md) | Deterministic Go emission, the runtime interface | agent |
| 09 | [`09-runtime-and-ffi.md`](09-runtime-and-ffi.md) | Keep the Go runtime; reproducible FFI; stdlib embedding | agent |
| 10 | [`10-lsp-and-tooling.md`](10-lsp-and-tooling.md) | LSP on the query core; fmt/doc/test/build/watch | agent |
| 11 | [`11-testing-and-verification.md`](11-testing-and-verification.md) | Conformance corpus, differential + rejection testing, repro gate | agent |
| 12 | [`12-migration-and-milestones.md`](12-migration-and-milestones.md) | Phased bring-up with the Haskell compiler as oracle | agent |

## Status

DOCUMENTATION IN PROGRESS. Spine docs (00–02) authored inline; subsystem docs
(03–12) delegated to focused agents against the spine. No `Cargo.toml`, no Rust
source yet — the blueprint lands first.
