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
