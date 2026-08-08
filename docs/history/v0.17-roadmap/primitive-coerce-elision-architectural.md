# Primitive coerce elision — architectural lever

**Status**: documented (iter 81); implementation deferred to dedicated mini-project
**Sites at risk**: ~527 occurrences of `rt.CoerceString/Int/Bool/Float` across
the example sweep post-iter-80

## Problem statement

The Sky compiler emits `rt.CoerceString(body)` / `rt.CoerceInt(body)` /
`rt.CoerceBool(body)` / `rt.CoerceFloat(body)` wraps when the target Go
type is a primitive and the body's static type is `any`. This is
correct + necessary at the FFI runtime boundary (where `rt.SkyCall` and
`rt.Ffi_callPure` return `any`).

However, MANY wraps are around bodies that ALREADY return the target
primitive type — typed kernel calls like `rt.String_fromIntT(...)`
(typed-T convention, returns `string`), `rt.JsonEnc_encode(...)`
(returns `string`), `rt.String_containsT(...)` (returns `bool`), etc.

These wraps are **structurally redundant** but the compiler doesn't
elide them because `goExprGoType` (the static-type-of-GoExpr predicate)
doesn't know the kernel functions' declared return types.

## Site survey (iter 81, post-iter-80)

| Wrap | 26-ui-showcase | 00-standard-libs | Total |
|---|---|---|---|
| `rt.CoerceString` | 202 | 86 | 288 |
| `rt.CoerceInt` | 44 | 73 | 117 |
| `rt.CoerceBool` | 17 | 62 | 79 |
| `rt.CoerceFloat` | 39 | 4 | 43 |
| **Total** | **302** | **225** | **527** |

A subset of these (estimated 30-50%) are genuinely redundant.

## Existing elision (line 14950 of `src/Sky/Build/Compile.hs`)

```haskell
| Just shapeTy <- goExprGoType ctx Nothing e
, shapeTy == ty
    = e
```

This DELIBERATELY passes `Nothing` instead of `mSrc` (the HM source)
because the v0.15.8 P2 arbitration ("HEAD-CYCLE-01-P2.md Step 3")
showed that pinning a typed TVar from one sibling arg while another
erases to `any` breaks Go's call-site inference uniformity and trips
`go build` on 13-skyshop with errors like:

```
type []string of ... does not match inferred type []any for []T1
```

The gate is locked by `test/Sky/Build/CoerceArgListMapInterplaySpec.hs`
+ `test/Sky/Build/SkyshopCompilesSpec.hs` — any future change here
re-trips both.

## Architectural approach (deferred)

The fix requires a NEW codegen channel that's separate from the
σ-consensus path:

1. **Build a kernel-return-type registry** — for every `rt.<name>`
   function (~80 entries in `runtime-go/rt/`), record its declared
   primitive return type (string / int / bool / float64 / any).
2. **Extend `goExprGoType`** to consult that registry when the body is
   `GoCall (GoIdent "rt.X") _`. Return `Just <prim>` when the registry
   says so.
3. **Add a NEW elision arm** in the primitive-narrow emit site (lines
   8791-8798 of `wrapTypedReturn`) that DOES pass `mSrc` — but ONLY
   when the body is a `GoCall (GoIdent "rt.X") _` shape (not a typed
   T-var sibling — avoiding the documented P2 hazard).
4. **Verification**: byte-diff against 13-skyshop main.go BEFORE and
   AFTER. Any change to skyshop's wrap distribution requires the
   `SkyshopCompilesSpec` to still pass.

## Estimated impact

- 30-50% of the 527 sites would elide → 150-260 wraps eliminated
- This is the largest single remaining lever for driving rt.Coerce to zero

## Implementation gates

1. Registry source: hand-curated CSV mapping `rt.X` → return type, or
   parse the rt/*.go files via TH or a build-time script
2. Spec coverage: new `KernelReturnTypeRegistrySpec` + extend
   `CoerceArgListMapInterplaySpec` to lock the σ-consensus invariant
3. Cross-example byte-diff sweep: 13-skyshop, 19-skyforum,
   26-ui-showcase, 30-sse-server-demo
4. Cabal full test gate

## Why deferred

Iter 79's runtime-any-alias elision (10 hardcoded entries → −69 sites)
shipped in one safe iter because:
- The elision target was UNAMBIGUOUSLY `any` (transparent alias)
- No registry needed
- No σ-consensus interaction
- Zero risk of breaking sibling-arg uniformity

The primitive-coerce elision lacks all four properties. It needs the
mini-project shape outlined above. Iter 79's quick win has been
extracted; iter 81+ would need ~1-2 hours focused on a coherent
multi-edit landing.

## Owner

Tracked under task #677 (v0.17 architectural close — sealed-interface
ADT emission). The flip pipeline + iter 70 region-scoping fix +
iter 79 alias elision close out the SAFE elisions. This documented
work is the next major architectural lever — to be picked up in a
fresh session with the full kernel-return-type-registry design done
upfront.
