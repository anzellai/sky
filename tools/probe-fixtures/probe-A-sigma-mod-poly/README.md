# probe-A-sigma-mod-poly

Exercises **Cause A** — σ-projection name-space drift on same-module
polymorphic re-instantiation.  `wrap : a -> List a` called with
`String` and `Int` in the same module forces α-renaming per call
site.

**Current state: GREEN** — v0.15's CForeign re-instantiation
closed the soundness gap.  C8-C12 (Phase γ — σ-projection rework)
cleans up the IORef-backed implementation but should not change
output.
