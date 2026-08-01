# Conformance-suite findings (v0.19.x)

The Layer-1 stdlib conformance suite (`tests/conformance/`, adversarial `sky test`
assertions) exists to catch the "compiles-clean-behaves-wrong" class the corpus
gates + differential oracle miss. This file tracks bugs it surfaces. Per the
no-deferral principle each is fixed (test flips from "documents bug" → "asserts
fix"); until fixed, the suite documents it in-file as a `KNOWN BUG` block so the
suite stays green rather than aborting on a panic.

## Open

### L1 — every consing `Sky.Core.List` op is O(n²) in TIME  [SEVERE / ARCHITECTURAL — needs user decision]

Sky lists are Go `[]any` slices, and `rt.List_cons` (runtime-go/rt/rt.go:1881-1893)
rebuilds the ENTIRE accumulator on every prepend (`make([]any, 0, len(xs)+1)` +
copy of all `xs`). So prepend is O(n), not the O(1) a cons-list gives — and every
CPS/accumulator op (`range`/`map`/`filter`/`reverse`/`append`/`concat`/`zip`/
`indexedMap`) is **O(n²) in time**. The v0.17 CPS rewrites fixed constant *stack*
but not *time*; the docs' headline "1M-element input runs in constant Go stack"
is misleading — 1M elements would take hours. Measured (`List.range 1 n`, ~4× per
doubling → quadratic): 5k=291ms, 10k=1.2s, 20k=4.7s, 40k=18s; 200k ≈ 7.5 min.
(The List conformance suite caps its stack test at 20k for this reason.)

**USER DECISION (2026-08-01): must fix L1.**

**Chosen approach — O(n) runtime kernels, KEEP the `[]any` representation.** The
hot ops (`map`/`filter`/`foldl`/`foldr`/`reverse`/`append`/`concat`/`range`/`zip`/
`indexedMap`) are all Sky-source CPS/accumulator loops that `::`-cons per element;
`List_cons` (immutable prepend to `[]any`) is O(n), so each op is O(n²). Reimplement
each as an `Ffi.kernel` backed by an O(n) Go loop that builds the result with
`append` in forward order (no per-element cons) — this keeps lists as `[]any` (so
ALL FFI/interop/`rt.AsListT` typed-widening is unchanged), stays constant Go stack
(a plain loop), and drops the ops to O(n). A cons-cell rep was rejected: O(1) cons
but O(n) index + a whole-surface interop rewrite. Correctness bar: EXACT semantic
parity (foldl/foldr direction, zip truncation, range bounds, indexedMap indices,
empty/singleton edges) — the List conformance suite + example sweep are the gate,
and the large-list test can then go to 200k+/1M and run in well under a second
(prove O(n) with a doubling benchmark). Golden fixtures re-blessed where a map/etc.
call site's emitted Go changes.

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
