# probe-D-record-update

Exercises **Cause D** — record update over typed base.  Today's
`{ p | age = p.age + 1 }` lowers to `rt.RecordUpdate(p, map[string]any{...})`
(reflect-backed map narrowing).  C5 emits a typed closure
`func() Person_R { _u := p; _u.Age = p.Age + 1; return _u }()`
when baseGo's static type is known, falling back to RecordUpdate
when it's `any`.

**Current state: GREEN** (rt.RecordUpdate path works).
**Tighten when C5 lands** — replace `MUST_CONTAIN rt.RecordUpdate`
with `MUST_NOT_CONTAIN rt.RecordUpdate` AND
`MUST_CONTAIN "_u.Age = "` to lock the closure shape.
