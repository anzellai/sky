# 00 — Goals & Principles

## Goals

**Primary:** an AI-velocity-friendly, correct, maintainable Sky compiler +
tooling + LSP that is **at least on par with the current Haskell compiler** and
strictly better on diagnostics, tooling, reproducibility, and the ability of an
AI assistant to make safe, local changes.

**Success is defined by acceptance gates, not by "feels done":**

1. **Language compatibility.** Every construct in [`03-language-reference.md`](03-language-reference.md)
   parses, type-checks, and lowers with the same *accept/reject* behaviour as
   the Haskell compiler (differential-tested). Same `sky.toml`, same CLI verbs.
2. **The 42 examples build AND run.** `sky build` → `go build` → the program
   runs / serves / responds exactly as under the Haskell compiler. The web +
   CLI + TUI verify scripts (`scripts/verify-*.sh`) pass. "If it compiles, it
   works" is a hard invariant, not an aspiration.
3. **Reproducible builds.** The same source compiles to byte-identical Go on any
   platform, any run. (This was a historical CI killer; here it is designed in.)
4. **Sound by construction.** No runtime panic from well-typed Sky. The compiler
   cannot emit a raw `any`-assertion or a partial match on its own IR.
5. **Editable in bounded context.** No module a human or model cannot hold in
   working memory. (The 24k-line `Compile.hs` is the anti-goal.)

## The journey's learnings, as design laws

Each law is a scar. Violating it is how we got here.

| Law | The scar it prevents |
|---|---|
| **L1 — No global mutable state. Ever.** All state flows through an explicit query database / context handle. | 791 IORef/`unsafePerformIO` sites; the entire v0.17 `globalCgEnv`/`scopeStateRef`→`CompileCtx`/`EmitM` clawback. |
| **L2 — Demand-driven & incremental (query core), not a batch pipeline.** | LSP had to bolt on a 5-round fixpoint + 8 IORefs + background threads because the compiler was batch-only. |
| **L3 — Intern everything to integer IDs (names, types, spans, files).** | Union-find variable identity (pointer-eq → unique Int was "the one real design task"); comparison/alloc cost. |
| **L4 — Determinism is an invariant, tested.** Ordered maps/index-order iteration in every emission path; no hashmap order reaches output. | Go-map iteration nondeterminism + platform-variant FFI inspector → non-reproducible builds (`f6e3ecdd`). |
| **L5 — Enforced module boundaries; a size budget per module.** Crate DAG makes cycles impossible. | The 24k-line monolith; the "circular imports" wall that was really a wrong-seam split. |
| **L6 — The compiler's own invariants live in the type system.** `enum`s + exhaustive `match` make illegal states unrepresentable. | `any`-boxed ADTs → `getDeclName` panic → the self-host that "couldn't catch its own bugs" (33-gap audit). |
| **L7 — Errors are values, diagnostics are data.** No exceptions for control flow; every phase returns partial results + diagnostics. | Cryptic Go errors instead of Sky-level diagnostics; recovery-hostile parser. |
| **L8 — Lossless syntax tree + error recovery.** Parse always produces a tree; the LSP works on broken code. | Fragile CPS parser with rank-N types + manual column tracking; no recovery. |
| **L9 — A typed lowering IR; coercion is the exception.** | `rt.Coerce` residual surface + Go-generics-on-record-alias gymnastics from an untyped impedance layer. |
| **L10 — The Go backend + runtime is an asset; keep it.** Rewrite the *compiler*, not the runtime. | Losing goroutine-Tasks, the deploy story, the SkyDeploy moat would be self-inflicted. |

## Non-negotiables (checked by CI)

- **No `unsafe` in the compiler crates** except a documented, reviewed allowlist
  (there should be none in the frontend/middle-end).
- **No `HashMap`/`HashSet` iteration reaching emitted output.** Use `IndexMap`,
  `BTreeMap`, or interned-id order. A lint/test enforces it (L4).
- **Every IR/AST enum match is exhaustive** — no catch-all `_ =>` arms on the
  compiler's own types (L6). `#![deny(non_exhaustive_omitted_patterns)]` where
  available; otherwise a review rule + tests.
- **No panics on well-typed input.** Panics are compiler-bug asserts only,
  behind a `bug!()` macro that emits a "please report" diagnostic (L7).
- **Reproducibility gate:** compile the corpus N× across seeds + ≥2 platforms in
  CI, byte-diff the Go (L4).
- **The Haskell compiler is the oracle** until the Rust compiler passes 100% of
  the differential + example-run corpus (see [`11`](11-testing-and-verification.md)).
- **Compat first, cleverness second.** Where the Haskell compiler's observable
  behaviour is quirky-but-relied-upon (e.g. explicit-alias-wins import rule,
  `main` Task auto-force), the rewrite reproduces it, then improves it behind a
  documented change — never silently diverges.

## Explicitly out of scope for v1 (kept honest)

- **Self-hosting.** A v2 credibility milestone; needs a rejection corpus +
  invariants-in-types + reproducibility (all built here first). The Rust
  compiler becomes the reference oracle that makes self-hosting *later* viable.
- **New backends** (LLVM/Cranelift/WASM). Keep emitting Go (L10).
- **New language features.** v1 targets exact compatibility; the language grows
  after the foundation is proven.
