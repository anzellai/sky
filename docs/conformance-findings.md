# Conformance-suite findings (v0.19.x)

The Layer-1 stdlib conformance suite (`tests/conformance/`, adversarial `sky test`
assertions) exists to catch the "compiles-clean-behaves-wrong" class the corpus
gates + differential oracle miss. This file tracks bugs it surfaces. Per the
no-deferral principle each is fixed (test flips from "documents bug" → "asserts
fix"); until fixed, the suite documents it in-file as a `KNOWN BUG` block so the
suite stays green rather than aborting on a panic.

## Open

### L2 — `String.toInt` does not trim whitespace (inconsistent with `toFloat`/`toIntT`)

The any-typed `String_toInt` (rt.go:3433) does `Atoi(Sprintf(...))` with no
`TrimSpace`, so `String.toInt "  42  " == Nothing` — but `String.toFloat` trims
(`"  2.5  " == Just 2.5`) and the typed companion `String_toIntT` (rt.go:3520)
trims. So trimming silently depends on the codegen path chosen. Fix: make
`String_toInt` consistent (trim, matching `toFloat`/`toIntT` — the additive,
least-surprising choice).

### C3 — cross-module reflective-codec type collision

Two modules that each define a same-named type (e.g. both a `Prim`) trigger a
reflect collision in the auto-codec kernel when compiled together:
`reflect: Call using <Mod>_Prim_R as type func(interface{}) interface{}`. Worth a
closer look (same family as the record-fieldset name-collision class).

## Fixed (test now asserts the fix)

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
