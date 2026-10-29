# docs/history — frozen material

Point-in-time documents: per-version roadmaps, session checkpoints, superseded
design plans, closure write-ups, and the legacy (Haskell) compiler docs. They
are **frozen** — kept for provenance and archaeology, not maintained as current
reference, and deliberately **excluded from the live-docs gate**.

If you want how Sky works *today*, do not read here. Use:

- **`sky doc <Module>`** — the live stdlib API (generated from source).
- **`docs/`** (the parent tree) — the maintained reference docs.
- **`AGENTS.md`** — the agent-agnostic entry guide.

Contents:

- `compiler/` — the legacy Haskell-compiler architecture (the primary compiler
  is now the Rust rewrite; see `docs/rust-rewrite/`).
- `v0.16.x-console/`, `v0.17*/`, `v0.18/`, `v1-rfc/`, `self-host/` — per-version
  roadmaps, closure plans, and design explorations.
- `embedded-postgres/` — the simultaneous-mutation experiment for the
  Std.Analytics / Std.Db pool work: does the gate suite still discriminate when
  nine defects are present AT ONCE, or only one at a time? It was produced by
  `scripts/grill-mutation-matrix.sh`, a one-off tool removed in v0.29.0 (in git
  history at tag `v0.28.1`).
- `legacy-compilers.md` — the TypeScript, Sky-in-Sky and Haskell compilers,
  removed from the tree in v0.29.0; tag `v0.28.1` is the last that has them.
- `ci-redesign-2026-08/` — the superseded CI test-architecture and corpus
  proposals, their phase results, the Layer-2 member record, the v0.19
  conformance findings and the coverage-hardening plan. The live design is
  `docs/ci-test-architecture-v2.md`.
- `skyspa/spike/` — the Sky.Spa Phase-1 Go→wasm spike.
- `archive/` — earlier audits, remediation notes, legacy READMEs, the
  Sky.Webview plan and the long-form road-to-v1 document.
