# `Module.value.field` on a qualified value

Pinned repro, v0.27.0 downstream round 3 (a downstream project's reduction).

`Shape.origin.x` is the field `x` of the value `Shape.origin`. The parser read
the whole run as one qualified name, and resolution failed with
`[E1001] Undefined name: Shape.origin.x`. A qualified path now ends at its first
lower-case segment; the rest is field access.

coordinate: `qual_path=alias qual_use=arg` (stratum `qualified_field`)

Expect: accept, stdout `3`.
