# probe-E-poly-adt-maybe

Exercises **Cause E** — poly ADT type-arg propagation.

User-defined `type Box a = Box a | EmptyBox` with a polymorphic
`unwrap : a -> Box a -> a` function.

**Current state: GREEN** — `unwrap[T1 any]` IS emitted generic
today; v0.15's typed-directed lowering closes this for
consumption-side code.  Surface remains on the C4 watch list
because the dep-module emit path (`generateUnionForDep` in
Compile.hs:3399) still drops `_vars` — manifests when this Box
flows across module boundaries.

**Cross-module variant** (not yet captured by a fixture):
import a Box from another module, exercise the cross-module
emit path.  C4 closes that gap.
