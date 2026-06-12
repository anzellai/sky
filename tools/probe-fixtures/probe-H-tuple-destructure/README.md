# probe-H-tuple-destructure

Exercises **Cause H** — tuple type erasure.  Today
`(String, Int)` lowers via `rt.SkyTuple2 = T2[any, any]` — element
types erased to `any`.  C6a emits typed `T2[string, int]` at call
sites; C6b drops the alias entirely so the runtime carries the
real element types.

**Current state: GREEN** (build + run works; types lossy at boundary).
**Tighten when C6a lands** — `MUST_CONTAIN "T2[string, int]"`.
**Tighten when C6b lands** — `MUST_NOT_CONTAIN "rt.SkyTuple2"`.
