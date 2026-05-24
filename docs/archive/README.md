# `docs/archive/`

Historical session notes, design proposals that landed (or were
superseded), and version-specific PR / audit summaries. Kept for
context — not maintained.

For the current state of Sky see:

- `../compiler/journey.md` — the running compiler changelog
  (TS → Go → Sky → Haskell, then v0.7 → v0.15 milestones).
- `../KNOWN_LIMITATIONS.md` — active limitations as of v0.15.
- `../../CLAUDE.md` — compiler-developer guide (v0.15 state + agreed contract).
- `../../templates/CLAUDE.md` — AI-assistant template for new Sky projects.
- `../../CHANGELOG.md` — per-version user-visible changes.

If a doc here describes a feature you're trying to use, double-check the
current state under `docs/` proper — the archived version may describe
the design before it shipped, or a workaround a later release removed.

Recently archived (after the feature shipped):

- `V1_TYPED_CODEGEN_FINISH.md` — multi-session plan for typed
  codegen + full Sky-source stdlib migration. All stages completed
  in v0.13–v0.15; the v0.15 type-soundness deep analysis at
  `docs/v1-rfc/type-soundness-deep-analysis.md` is the
  architectural successor.
- `parametric-record-aliases-bugs.md` — three parametric-record-
  alias bug surfaces (Surface 1 / Surface 2 / Surface 3). All
  closed by v0.15 (Stages A–F + Go generics on parametric records).
