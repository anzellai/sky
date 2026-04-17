# Compiler journey — TS → Go → Sky → Haskell

The Sky compiler has lived in four implementations. Each rewrite addressed a concrete limitation of its predecessor. This document captures the history so future maintainers understand why the current codebase looks the way it does.

## 1. TypeScript bootstrap (`legacy-ts-compiler/`)

**When:** project inception.

**Why TypeScript:** rapid prototyping. The language semantics were still moving; a dynamic ecosystem (npm, ts-node) made iteration fast. TypeScript's structural types gave enough safety without HM-level plumbing.

**What it did:** parser, canonicaliser, and a naïve Go emitter. No type inference — all types were erased to `any` in the Go output. Good enough to bootstrap runnable programs.

**Why it had to go:**

- Node.js dependency for every run.
- Slow: 5–15s startup for even trivial programs.
- No real type checking, so Sky's guarantees were vibes-based.
- JS ecosystem drift made reproducible builds painful.

## 2. Go rewrite

**When:** once Sky programs could run, the TS compiler was ported to Go.

**Why Go:** single static binary. No Node. Fast startup. The same language Sky compiles to — reducing cognitive load when debugging codegen.

**What changed:** same pipeline, reimplemented idiomatically in Go. Type checker was still basic.

**Why it had to go:**

- Writing a functional type checker in Go is unpleasant. The implementation fought the language at every step.
- Large feature landings (pattern exhaustiveness, HM inference) kept stalling because the imperative code made invariants hard to maintain.

## 3. Self-hosted Sky (`legacy-sky-compiler/`)

**When:** once Sky had enough features (HM inference, ADTs, pattern matching, FFI) to implement itself.

**Why self-hosted:** the classic demonstration that a language is real. Writing the compiler in Sky validated the language's ergonomics for production code.

**What worked:**

- Proved Sky could handle serious programs (the compiler was ~30k lines of Sky).
- Exercised every corner of the language, catching ergonomic bugs early.
- Self-hosted builds were around 6 MB native binaries with no external runtime.

**Why it had to go:**

- Sky's type system was Hindley-Milner, but writing HM itself in Sky hit the same expressiveness limits that made it hard to describe in Go. No higher-kinded types, no type classes, no row polymorphism — all intentional language omissions that made the compiler's own invariants brittle.
- Parser error recovery and LSP latency suffered because Sky's runtime model (single-threaded, `any`-boxed by default pre-v1) added cost the compiler couldn't optimise away.
- Debugging was circular: a compiler bug affecting inference made the compiler itself misbuild.

## 4. Haskell (current — `src/` tree)

**When:** 2026 Q1 — after P4/P5/P6 of the production-readiness plan landed.

**Why Haskell:**

- Hindley-Milner is Haskell's native idiom. The type checker is a few hundred lines of clear constraint-solving code rather than the thousands of lines of imperative state management it took in Go.
- ADTs and pattern matching in Haskell map 1:1 to Sky's AST, so the parser and canonicaliser are almost transliterations.
- GHC's optimiser produces a binary that's faster than the self-hosted Sky implementation without any hand-tuning.
- Type-level invariants (`data Canonical` vs `data Lowered` AST phases) catch compiler bugs at compile time.

**What moved:**

- Parser, canonicaliser, type checker, lowerer, Go emitter — all in `src/Sky/**`.
- FFI generator (`src/Sky/Build/FfiGen.hs`) inspects Go packages via a Go-side tool (`tools/sky-ffi-inspect/`) and emits typed wrappers.
- LSP (`src/Sky/Lsp/`) — same module graph reuse as the compiler, no duplicated parsing.
- The Sky runtime (`runtime-go/rt/`) stayed in Go — it's shipped as `//go:embed` data and copied into every project's `sky-out/rt/` at build time.

**Trade-offs made:**

- Sky is no longer self-hosted. For the foreseeable future, a Haskell toolchain (GHC 9.4+ via `cabal install`) is required to build the compiler. Users of Sky only need the `sky` binary + Go, not Haskell.
- Contributors to the compiler need to learn Haskell. The existing codebase is conventional Haskell — no advanced type-level hackery.

## Why this sequence

Each implementation pushed the language far enough that the next one became feasible. TS let us prove the shape of the language. Go let us ship a single binary. Sky self-hosted let us ensure the language was usable for real work. Haskell let us make the compiler *good*.

## What's next

No compiler rewrite is planned. The Haskell implementation is the long-term home. Future work is:

- Typed Go output end-to-end (v1 shipped this; v2 aims to eliminate the remaining reflect-based dispatch).
- Formal exhaustiveness for nested patterns.
- Multi-file incremental compilation.

See [versions.md](versions.md) for the feature-level changelog.
