# probe-H5 — `(Int, List Item)` list-of-records element

Tuple element is `List Item` where `Item` is a user record alias.
Forces the typed-slot lowering all the way through to list-of-
record element type. PR-15 (`lowerRecordLiteralTo` + pattern-side
migration) is the value-side seam that has to land before this
probe will emit typed lists.

**Closes by:** PR-17 + PR-15.
