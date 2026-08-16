# Typed list destructuring — `rt.SkyLenT` / `SkyElemT` / `SkyTailSliceT`

**2026-08-15.** Follow-on to `../hof-dispatch-20260815/`. That run removed the
`reflect.MakeFunc` adapter from HOF callbacks and left **126 allocations per
`Std.Ui` marker scan** standing. This is where a large share of them were.

## What was wrong

The Rust codegen's cons/list pattern binder (`Pattern::Cons`, `Pattern::List` in
`rust/crates/lower/src/lower.rs`) emitted `rt.SkyLen` / `rt.SkyElem` /
`rt.SkyTailSlice`. All three take `x any` and route through `rt.AsList`.

`AsList` fast-paths a `[]any`. A `[]T` for **any other T misses that assertion**
and falls to the reflect arm, which allocates a fresh `[]any` of length n and
boxes every element into it (`runtime-go/rt/rt.go`, `AsList`).

So on a typed list each of those calls is **O(n) allocation**, not the single
boxed slice header the signature suggests — and a cons loop rebuilt the entire
list on every iteration, making an O(n) walk quadratic.

Measured directly (`runtime-go/rt/sky_destructure_typed_test.go`), on a
16-element `[]struct{string; int}`:

| helper | allocations/op |
|---|---|
| `SkyLen` | 18 |
| `SkyElem` | 18 |
| `SkyTailSlice` | 18 |
| `SkyLenT` / `SkyElemT` / `SkyTailSliceT` | **0** |

18 = n + 2, i.e. the whole list re-boxed, per call.

## The fix

When the subject's Go type is already a slice, emit the typed helpers. They
compile to `len(xs)`, `xs[i]` and `xs[1:]`; Go infers the type argument from the
slice, so no call site carries one. The ABI is unchanged — no monomorphisation,
one emit per definition, as with the eta-expansion.

The decision keys on `subj.ty` (the Go type of the expression being emitted),
not on the `subj_ty` type-context threaded alongside it. Those can disagree, and
it is `subj.ty` that decides whether the emitted Go type-checks: an
over-conservative fall back to the `any` path is a missed optimisation, while
the reverse is a `go build` failure.

Bounds guards are **kept**, though the pattern's length test has already run.
That test is a claim about the caller; a helper that panics when the claim is
wrong would convert a lowering bug into a runtime panic, and "no runtime panic
from well-typed Sky" is not a property to rest on an invariant asserted in a
comment. Out of range yields `T`'s zero, mirroring the `any` versions' nil.

## Measurement

Same harness, same app, same method as `../hof-dispatch-20260815/`:
`examples/26-ui-showcase` (384 elements), `GOMAXPROCS=1`, closed loop, 25
sessions, 20 s, warmup 5 s, ramp 3 s. Arms alternate per rep so thermal drift
and burst-credit decay fall on both equally. The two binaries differ **only** by
this change — verified by `strings`: `SkyElemT` appears 11× in the after binary
and 0× in the before.

`bench.sh` is the script as run. The "before" arm here is the *after* arm of the
HOF-dispatch run, i.e. eta-expansion already landed.

| run | before (interactions/sec) | after |
|---|---|---|
| 1 | 185.05 | 245.03 |
| 2 | 184.65 | 244.13 |
| 3 | 177.84 | 243.68 |
| **range** | **177.84 – 185.05** | **243.68 – 245.03** |

**1.34× throughput.** Ranges do not overlap. p50 latency 129.3 ms → 89.8 ms
(−31%), p95 326.6 → 273.7 ms.

Both arms: `error_rate: 0`, 25/25 sessions established, generator CPU under
0.3% of the machine (`generator_possibly_saturated: false`), `valid: true` — so
the faster arm is not winning by shedding work. Raw JSON for all six runs is
committed beside this file.

Cumulative over both changes, against the original 135.47 interactions/sec
baseline in `../hof-dispatch-20260815/`: **1.80×**.

Secondary effect, mechanically measured by `xtask coerce-floor`: because
`SkyElemT` returns `T` and `SkyTailSliceT` returns `[]T`, the narrowings that
used to bridge their `any` / `[]any` results disappear. The `narrow` class fell
**9,986 → 9,479 (−507) across 26 projects**, with the `adapter` class unchanged
at 35 — which is the classified ratchet doing its job: it reported a tightening
to be blessed rather than a widening to be investigated.

## What these numbers do NOT say

* **The per-call allocation ratio does not extrapolate to a request.** 18 → 0 on
  one helper is not 1.34× because a large share of an interaction is network
  syscall and rendering no compiler change touches. The end-to-end number is the
  one to quote.
* **The gain scales with list length and with how much destructuring an app
  does.** 26-ui-showcase walks attribute lists constantly. An app that pattern-
  matches lists rarely will see less; this is not a uniform 1.34× for all Sky.
* **Closed loop is the sensitive regime, not the typical one.** `-think 0` keeps
  the server saturated; with realistic think time the difference compresses.
* **One host, one toolchain.** Macmini, `gomaxprocs: 8` reported but the app
  pinned to `GOMAXPROCS=1`. Not a cross-platform claim.
* **`[]any` subjects gain nothing measurable.** They already hit `AsList`'s fast
  path; the win is specifically on typed slices.
