# Conformance-suite findings (v0.19.x)

The Layer-1 stdlib conformance suite (`tests/conformance/`, adversarial `sky test`
assertions) exists to catch the "compiles-clean-behaves-wrong" class the corpus
gates + differential oracle miss. This file tracks bugs it surfaces. Per the
no-deferral principle each is fixed (test flips from "documents bug" → "asserts
fix"); until fixed, the suite documents it in-file as a `KNOWN BUG` block so the
suite stays green rather than aborting on a panic.

## Open

### C1 — `Codec.fromJson` on an ADT (enum / taggedUnion) PANICS on decode failure  [SEVERE]

A decode FAILURE against any `Codec.enum` / `Codec.taggedUnion` codec panics with
`CoerceFailure` in `rt.ResultCoerce` / `coerceInner` ("source string cannot be
cast to target rt.SkyADT") instead of returning `Err`. The success path works.
Compiler typed-codegen routing bug in `Std_Codec_fromJson`'s `ResultCoerce[<ADT>]`.
Repro:
```elm
Codec.fromJson (Codec.enum [ ( Stickers, "stickers" ) ]) "\"nope\""   -- panics
Codec.fromJson shipmentTaggedUnionCodec "[\"GhostTag\"]"              -- panics
```
Violates the "no runtime panic from well-typed Sky" + "if it compiles it works"
non-negotiables. **Highest priority.**

### C2 — reflective `auto`/`autoCamel`/`autoWith` record decoder is PERMISSIVE

A missing required field OR a wrong-typed field silently decodes to the
zero-value default and returns `Ok` instead of `Err` (silent data corruption):
```elm
Codec.fromJson (Codec.auto { name = "", count = 0 }) "{\"name\":\"x\"}"
  -- Ok { name = "x", count = 0 }   (count invented)
Codec.fromJson (Codec.auto { name = "", count = 0 }) "{\"name\":\"x\",\"count\":\"z\"}"
  -- Ok { name = "x", count = 0 }   (type mismatch ignored)
```
The explicit `object`/`field`/`buildObject` decoder is strict + correct on the
same inputs — leniency is isolated to the reflective `Codec_autoDecoder` kernel.

**Shared root of C1 + C2 (analysis):** the reflective decoders are LENIENT.
`Codec_autoDecoder` (runtime-go/rt/codec_auto.go:439) returns `Err` when
`codecAutoDecodeVal` errors — so C2's leniency lives INSIDE `codecAutoDecodeVal`
(it must reject a missing required field / a type mismatch instead of filling the
zero value). C1 is the enum/taggedUnion reflective decoder returning
`Ok "<rawstring>"` on an unknown tag; that non-ADT `Ok` value then hits
`rt.ResultCoerce[Error, <ADT>]` (rt.go:329, the `fromJson`-result wrap), which
calls `coerceInner[<ADT>]("nope")` and PANICS (rt.go:655). Making the enum decoder
`Err` on an unknown name fixes both the leniency AND the panic (the `Err` path of
ResultCoerce is sound). Fix = strictness in the reflective decoders; add a
belt-and-suspenders in `coerceInner` to never panic from a decode path.

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

This is not a quick fix — the sound remedy is a cons-cell (or O(1)-prepend) list
representation instead of `[]any`, which is a runtime + codegen + interop change
touching the whole list surface. **Escalate to user**: whether to undertake the
list-rep rewrite now (multi-session) or accept documented O(n²) with a corrected
doc + a guardrail on large-list ops. Per no-deferral this is a "start the correct
fix / get direction", not "ignore".

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

- **S1 — `Std.Db.Store` multi-column `ORDER BY` reversed** — `orderAsc`/`orderDesc`
  prepended, so the last call became the primary key. Fixed (`orderTail` reverses).
  Guarded by `StoreConformanceTest` (proven red-on-bug).

## Clean (no bug found; suite is a regression guard)

- **Sky.Core.Json** (Encode/Decode/Pipeline) — 30 adversarial assertions incl. the
  full escaping fixture (`"`, `\`, `\n`, `\t`, control char, emoji, CJK); malformed
  input → `Err`; fractional-int rejection; oneOf/at/index/optional. All correct.
