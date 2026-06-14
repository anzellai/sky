# probe-H6 — `(Int, String, Bool)` 3-tuple

Verifies arity-3 typed-tuple dispatch. The runtime already has
`rt.T3` from earlier work; this probe asserts the lowerer
actually routes through it.

**Closes by:** PR-17.
