# probe-G-ffi-opaque-collapse

Placeholder for **Cause G** — FFI opaque collapse to "Value".

Today every opaque FFI type from `go-pkg` collapses to a single
sentinel `(Canonical "") "Value"`, so Sky annotations
`stripe.Customer` and `aws.Account` unify even though they're
distinct Go types.

C17 closes this via a 6-commit umbrella documented at
`docs/v0.17-c17-revised-design.md`. When C17 ships, replace this
fixture's Main.sky with a cross-package opaque shape that demands
`MUST_FAIL_BUILD` — the Sky compiler should reject the unsound
cross-type assignment.

Today the fixture is a no-FFI baseline (probe sweep framework
handles missing-FFI cleanly).
