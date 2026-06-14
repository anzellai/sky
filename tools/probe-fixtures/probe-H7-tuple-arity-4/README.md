# probe-H7 — 4-tuple `(Int, String, Bool, Float)`

Proves the runtime needs `rt.T4`-`rt.T9` aliases generated (the
existing runtime stops at `rt.T3`). PR-17 lands those aliases AND
flips the lowering gate so `rt.SkyTupleN` (slice-backed) goes
away for any well-typed concrete tuple.

**Closes by:** PR-17.
