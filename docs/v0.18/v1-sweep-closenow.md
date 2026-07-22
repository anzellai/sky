# v1 Discovery Sweep — CLOSE-NOW queue (PR #154)

From the 17-agent discovery+grill sweep (2026-07-22). 14 grill-confirmed,
self-contained, achievable-in-patch items. **Bold = `check ≡ build` violation**
(highest v1 value). Work in ROI order; verify each repro before fixing.

| # | Item | Eff | Status |
|---|------|-----|--------|
| 1 | String.repeat negative → "" | S | ✅ |
| 2 | `sky doctor` false-positives auth-secret on pristine init (comment lines) | S | ⬜ |
| 3 | `sky test` can't run in a standalone project (repo_root_for → assets_root_for) | S | ⬜ |
| 4 | **arity-0 fn-valued def → double-call `Main_x()()(arg)` go-build fail** (lower.rs:2762) | S | ⬜ |
| 5 | call-arg type mismatch anchored at whole call, not the arg (infer.rs:587) | S | ⬜ |
| 6 | E1001/E1002 diagnostics carry no span/caret/excerpt (resolve.rs:1908/621) | S | ⬜ |
| 7 | ctor in case-pattern not recorded → hover/goto/refs/**rename corrupts** (resolve.rs:1534/1549) | S | ⬜ |
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
