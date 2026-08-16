# Stage 2 — the erased list-helper round trip, removed where it is provable

`../forum-rebaseline-20260816/` measured what an interaction costs on an
application and found the single largest structural fact in the profile:
**89–91% of all allocation happens inside a `reflect.Value.Call`**, two thirds
of CPU samples have a reflective higher-order call on the stack, and **one
fifth of every object allocated is the erased list-helper round trip's own
bookkeeping**. Allocation was ~250 objects per rendered element with a fixed
term indistinguishable from zero: every object is allocated on behalf of an
element.

This run measures what removing that round trip — where the compiler can prove
it is safe to — is worth on the same app, with the same harness, at the same
two view sizes.

## The change, in one emitted line

`src/View/Posts.sky:17`, the line 90.6% of the forum's home page hangs off,
emitted this before:

```go
rt.AsListT[Std_Ui_Element](rt.List_indexedMap(
    any(func(_p0 any, _p1 any) Std_Ui_Element { … }), any(v_1)))
```

and emits this after:

```go
rt.List_indexedMapT[State_Post_R, Std_Ui_Element](
    func(_e2 int, _e3 State_Post_R) Std_Ui_Element { … }, v_1)
```

Per call on a list of n, the first costs one box for the slice header, an
`asList` reflect walk that boxes **every element**, a `reflect.Value.Call` per
element (twice here — `indexedMap` applies the index first, so each element also
builds a curried closure), a `[]any` result, and an `AsListT` walk back. The
second is a Go `for` over a typed slice.

Compiler site: `Ctx::list_hof_typed` in `rust/crates/lower/src/lower.rs`,
reached from `kernel_call`. Architecture: doc 08 §6 category 6 ("polymorphic
kernel-fn arg"), §7.4 lever, explicitly **not** §8.3 floor — §8.3 as rescoped at
`50c8dcee` names these five helpers and says so.

## Conditions

| | |
|---|---|
| Host | Apple M1, 8 cores, 16 GB, macOS 26.5.2 — arm64 |
| Branch | `perf/stage2-typed-hof-loop`, off `feat/embedded-postgres` @ `40402294` |
| Go | 1.26.1 |
| App | `forumbench` — `examples/19-skyforum` plus the `init`-only view-size lever, byte-identical to `../forum-rebaseline-20260816/`'s |
| Arms | the **same app source**, compiled by two `sky` binaries differing by exactly one thing: `Ctx::list_hof_typed` and the two runtime helpers it needs |
| Interaction | signed-in upvote toggle — 2 patches, every press |
| Session store | `memory` |
| Load | `tools/skyliveload`, loopback, 25 sessions, closed loop (`-think 0`), 45 s window, 3 s ramp, 3 s warmup |
| `GOMAXPROCS` | **1** on the app |
| Run order | before, after × 3, **alternating**, within each view size |
| Repeats | 3, ranges reported |

Verified before measuring: the two app binaries differ (`cmp`), the after arm's
emitted Go carries 18 typed dispatch sites and the before arm carries 0, and the
after arm's `sky-out/rt/rt.go` contains the shipped helper bodies (`grep`, not
assumed — an aliased `cp` silently declined to overwrite the compiler binary
once during this run, and the only thing that caught it was grepping the built
artefact).

Every run asserts patch production as a **precondition** and again over the
window: `patch_rate` is `1` and `patches_naming_absent_ids` is `0` on all 12.

## The numbers

<!-- TABLES -->

## What did NOT move — the control

## What this run did NOT measure
