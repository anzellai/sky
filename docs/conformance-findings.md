# Conformance-suite findings (v0.19.x)

The Layer-1 stdlib conformance suite (`tests/conformance/`, adversarial `sky test`
assertions) exists to catch the "compiles-clean-behaves-wrong" class the corpus
gates + differential oracle miss. This file tracks bugs it surfaces. Per the
no-deferral principle each is fixed (test flips from "documents bug" → "asserts
fix"); until fixed, the suite documents it in-file as a `KNOWN BUG` block so the
suite stays green rather than aborting on a panic.

## Open

_None — every finding below is fixed + verified._

## Fixed (test now asserts the fix)

- **L2 — `String.toInt` did not trim surrounding whitespace** — the any-typed
  `String_toInt` (`runtime-go/rt/rt.go`) did `Atoi(Sprintf(...))` with no
  `TrimSpace`, so `String.toInt "  42  " == Nothing` — but `String.toFloat` trims
  (`"  2.5  " == Just 2.5`) and the typed companion `String_toIntT` trims. So
  trimming silently depended on which codegen path was chosen. **Fix:**
  `String_toInt` now `TrimSpace`s the input before `Atoi`, matching
  `toFloat`/`toIntT` (the additive, least-surprising choice). Guarded by
  `StringConformanceTest` (`"  42  " → Just 42`) and the Go regression
  `runtime-go/rt/string_toint_test.go`.
- **C3 — cross-module same-named-type collision in `case` pattern emission**
  [SEVERE] — two modules that each declare a same-named ADT with the same
  variant names (`type Prim = Leaf String | Node Int` in both `Alpha` and
  `Beta`) miscompiled every `case` on one module's value: the pattern lowerer
  resolved the bare constructor name (`Leaf`) through a **last-writer-wins**
  `ctor_owner` map, so a `case alphaVal of Alpha.Leaf …` emitted its variant
  type-assertions against `Beta_Prim_Leaf_V` (Beta interned last). The Alpha
  value never matched the Beta variant struct, so the exhaustiveness-checked
  case fell through to `panic(rt.Unreachable("case"))`; through the reflective
  codec `taggedUnion` decode path the same collision surfaced as
  `interface conversion: main.Alpha_Prim_Leaf_V is not main.Beta_Prim_Leaf_V`.
  (The finding's original `reflect: Call using <Mod>_Prim_R as func(interface{})
  interface{}` symptom was the pre-C1/C2 manifestation; the underlying cause is
  this pattern-resolution collision, NOT the auto-codec kernel — `Codec.auto`
  /`autoWith`/`enum`/`taggedUnion` all just drive user `case`s / constructors.)
  **Root cause:** the pattern-lowering paths (`pattern_nominal`,
  `sealed_adt_union`, `ctor_pattern`, `pattern_nominal_ty` in
  `rust/crates/lower/src/lower.rs`) keyed off the bare constructor NAME even
  though the resolved `Pattern::Ctor { ctor: Some(CtorRef) }` already carries
  `type_` — the module-correct owning-union `DefId`. Constructor *construction*
  already honoured it (`pinned_union_go` at the ctor-call site); only the
  pattern side didn't. **Fix (2026-08-01):** new `ctor_union_owner` helper
  resolves the owning-union Go name from `CtorRef.type_` via `pinned_union_go`
  first (falling back to the subject's pinned nominal, then the bare-name map
  for unresolved/builtin ctors), and every pattern path routes through it. So a
  `case` arm now asserts against its own module's `_R`/`_V` structs. Guarded by
  the Rust regression `crates/project/tests/xmodule_same_variant.rs` (drives the
  real emit pipeline; asserts each module's `case` pins its OWN variant struct,
  red-on-bug) — plus the corpus `49-xmodule-adt` build-run gate. Verified e2e
  across every reflective codec path (auto / autoCamel / autoWith / enum /
  taggedUnion / nested-record-field / list / maybe / `Store.selectRaw`
  projection) with same-named types across two modules.


- **L1 — every consing `Sky.Core.List` op was O(n²) in TIME** [SEVERE] — Sky
  lists are Go `[]any` slices, so `rt.List_cons` is an O(n) immutable prepend
  (`make([]any, 0, len+1)` + copy). The v0.17 pure-Sky CPS/accumulator loops
  `::`-cons per element, so every list-BUILDING op (`map`/`filter`/`reverse`/
  `append`/`concat`/`concatMap`/`range`/`zip`/`indexedMap`/`take`/`drop`/`foldr`)
  was **O(n²) in time** (constant stack, but `List.range 1 40000` took ~18s).
  **Fix (2026-08-01):** each building op is now an `Ffi.kernel "List_<name>"`
  alias (same as `Sky.Core.Dict`'s HOF kernels) backed by an O(n) Go loop that
  grows the result with `append` in forward order — NO per-element cons. This
  KEEPS the `[]any` representation (all FFI/interop/`rt.AsListT` widening
  unchanged), stays constant Go stack (a plain loop), and drops the ops to O(n).
  The `[]any` runtime kernels already existed (`rt.List_mapAny`/`List_filterAny`/
  `List_range`/…, `runtime-go/rt/rt.go`); wiring them via the `.sky` bodies was
  the change, plus three latent edge-case bugs fixed so the newly-reachable
  kernels match the Sky-source semantics they replace: `List_range` (hi<lo →
  panicked on a negative `make` cap; now `[]`), `List_take`/`List_drop` (n<0 →
  panicked on a negative slice bound; now clamp to 0). The SCALAR ops (`foldl`/
  `length`/`member`/`any`/`all`/`find`/`isEmpty`) stay pure Sky — already O(n)
  and auto-TCO'd. Doubling benchmark (full pipeline, wall-clock) proves LINEAR
  scaling: 100k=0.16s, 200k=0.29s, 400k=0.57s, 800k=1.14s, 1.6M=2.26s (each
  doubling ~2×, not the old ~4×). Guarded by `ListConformanceTest` (its SCALE
  sweep now runs at 1_000_000 and completes in well under a second) and the Go
  regression `runtime-go/rt/list_edge_parity_test.go` (range/take/drop edges).
- **C1 — `Codec.fromJson` on an ADT (enum / taggedUnion) PANICKED on decode
  failure** [SEVERE] — a decode FAILURE against a `Codec.enum` / `Codec.taggedUnion`
  codec panicked with `CoerceFailure` in `rt.ResultCoerce` / `coerceInner`
  ("source string cannot be cast to target rt.SkyADT") instead of returning `Err`.
  **Root cause:** the enum/taggedUnion decoders DO fail correctly (via `D.fail` in
  `Std.Codec`), but `JsonDec_fail` (runtime-go/rt/stdlib_extra.go) returned an Err
  carrying a **bare string** rather than a proper Error ADT like every other
  decoder (`ErrDecode`). `fromJson : Codec a -> String -> Result Error a` has its
  result wrapped by codegen in `ResultCoerce[Error, a]`, whose Err path calls
  `coerceInner[Error](errValue)` — a bare string cannot narrow to the Error SkyADT
  and panicked (rt.go:coerceInner). **Fix:** `JsonDec_fail` now returns
  `Err(ErrDecode(msg))`; plus a defensive guard in `coerceInner` wraps a bare
  string → Error ADT (target `rt.SkyADT`) instead of aborting, so no decode path
  can panic the runtime. Guarded by `CodecConformanceTest` (enum + taggedUnion
  decode-failure → `Test.err`, proven red-on-bug) and the Go regression
  `runtime-go/rt/codec_enum_decode_fail_test.go`.
- **C2 — reflective `auto`/`autoCamel`/`autoWith` record decoder was PERMISSIVE**
  — a missing required field OR a wrong-typed field silently decoded to the
  zero-value default and returned `Ok` (silent data corruption). **Fix:** the
  reflective decoder (`codecAutoDecodeVal` / `codecAutoDecodeStruct` /
  `Codec_autoDecoderOverrides`, runtime-go/rt/codec_auto.go) is now STRICT — it
  errors on (a) a required (non-Maybe) field absent from the object, (b) a field
  whose JSON value has the wrong type for the target (string↔int↔bool↔float↔
  array), (c) a fractional number where an Int is expected, and (d) an unknown
  registered-enum value — matching the explicit `object`/`field`/`buildObject`
  decoder. **Nuance preserved:** a `Maybe` field that is absent or null still
  decodes to `Nothing` (only non-optional fields are required). Guarded by
  `CodecConformanceTest` ("auto strict → Err" section + Maybe-absent positives)
  and the Go regressions `TestAutoDecoder*` in
  `runtime-go/rt/codec_enum_decode_fail_test.go`.
- **S1 — `Std.Db.Store` multi-column `ORDER BY` reversed** — `orderAsc`/`orderDesc`
  prepended, so the last call became the primary key. Fixed (`orderTail` reverses).
  Guarded by `StoreConformanceTest` (proven red-on-bug).

## Clean (no bug found; suite is a regression guard)

- **Sky.Core.Json** (Encode/Decode/Pipeline) — 30 adversarial assertions incl. the
  full escaping fixture (`"`, `\`, `\n`, `\t`, control char, emoji, CJK); malformed
  input → `Err`; fractional-int rejection; oneOf/at/index/optional. All correct.
