# probe-H1 — typed primitive `(String, Int)` tuple

The headline shape for v0.17 Cause-H. A let-destructure of a
constant tuple of primitives.

**Today:** `solvedTypeToGo.TTuple` checks a 10-primitive whitelist;
non-whitelist elements bring the whole tuple down to `rt.SkyTuple2`
(`{A,B any}`).  Even with both elements primitive, the existing
emission still uses `rt.SkyTuple2` because the primitive-allowed
path didn't land in v0.17 PR-1..3.

**Target (PR-17 / Ship Point B):** `rt.T2[string, int]{A: "hello", B: 42}`
with destructure as `pair_var.A` / `pair_var.B`. The
`rt.AsInt`-style "any → int" panic class for destructured primitives
closes at this moment.

**Closes by:** PR-17 (Cause H Step 4 widen + `rt.T4`-`rt.T9` aliases).

Used by `RendererParitySpec` to assert the value-side seam at this
fixture matches `solvedTypeToGo` against
`mapSkyTypeToGo`-via-foundation post-PR-5.
