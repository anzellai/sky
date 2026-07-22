# v1 Discovery Sweep — CLOSE-NOW queue (PR #154)

From the 17-agent discovery+grill sweep (2026-07-22). 14 grill-confirmed,
self-contained, achievable-in-patch items. **Bold = `check ≡ build` violation**
(highest v1 value). Work in ROI order; verify each repro before fixing.

| # | Item | Eff | Status |
|---|------|-----|--------|
| 1 | String.repeat negative → "" | S | ✅ |
| 2 | sky doctor auth-secret skips commented headers | S | ✅ |
| 3 | sky test uses assets_root_for (standalone works) | S | ✅ |
| 4 | arity-0 CAF call-site no longer double-forces | S | ✅ |
| 5 | call-arg type mismatch anchored at whole call, not the arg (infer.rs:587) | S | ⬜ |
| 6 | E1001 shows line/caret/excerpt (+drop redundant reason) | S | ✅ |
| 7 | pattern-ctor recorded as ref (hover/goto/refs/rename) | S | ✅ |
| 8 | `List.sum/product/maximum/minimum` missing from stdlib | S/M | ⬜ |
| 9 | Basics.abs/negate/sqrt repointed to rt.Math_*/rt.Negate | S | ✅ |
| 10 | `String.left/right`, `List.sort*/filterMap` exist but not exposed | S | ⬜ |
| 11 | **Char literals lowered as Go strings → panic / pattern build-fail** (lower.rs:1914/4428) | M | ⬜ |
| 12 | **`let` forward refs → non-compiling Go** (lower.rs:1713 topo-sort) | M | ⬜ |
| 13 | **entry module hardcoded to `Main`** — sky.toml entry/CLI file arg ignored (build.rs:188/348) | M | ⬜ |
| 14 | typo of kernel member → E4005 "please report" not name error (resolve.rs:1817) | S/M | ⬜ |

**Ordering constraint:** #8 → #10 → #14 as one chain (add real fns to exposing,
THEN tighten the gate, else #14 rejects the newly-exposed blind-mapped fns).

Deferred: `toString` custom-ADT constructor-name rendering (needs codegen tag→name
table threaded to runtime — scoped follow-on). Composite-shape stringify half is
close-able (STRETCH).

## Surfaced during #4 (new, separate — kernel partial application)

- **Partial application of a KERNEL emits an under-applied call** (check≡build).
  `let g = String.append "hi " in g "bob"` → emitted `rt.String_append("hi ")`
  (1 arg to a 2-arg kernel) → `go build: not enough arguments in call to
  rt.String_append`. Distinct from #4 (which was the arity-0 CAF call-site
  double-force, now fixed + verified via a user-fn point-free = 42). A kernel
  under-application should emit a partial closure. Effort M — next in queue.
