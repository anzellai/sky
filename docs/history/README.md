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
  nine defects are present AT ONCE, or only one at a time? Regenerate with
  `scripts/grill-mutation-matrix.sh`.
- `archive/` — earlier audits, remediation notes, and legacy READMEs.
