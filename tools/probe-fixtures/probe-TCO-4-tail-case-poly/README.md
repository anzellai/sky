# probe-TCO-4 — case-arm tail-position polymorphic

`choose : Int -> Maybe a -> a -> Maybe a` has its tail in a
`case` body. One arm passes through the polymorphic `m : Maybe a`;
the other recurses with a `Just fallback` lift. Exercises
`coerceReturnExprT`'s interaction with the case-arm tail tag at
PR-13/14.

**Closes by:** PR-13 + PR-14.
