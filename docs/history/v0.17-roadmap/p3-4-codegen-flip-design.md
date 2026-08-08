# P3.4 design: sealed-iface codegen flip — decomposition

P3.4 is the biggest single piece of v0.17 architectural close. Per
the iter 46 grill + iter 48 P3.2 supersession + iter 49 P3.3
findings, it must coordinate:

1. **Type declaration** — `type Mod_X = rt.SkyADT` → sealed
   interface + per-variant struct
2. **Constructor emission** — Tag/SkyName/Fields literal → variant
   struct literal
3. **init() block** — `RegisterAdtTag` only → `RegisterAdtTag` +
   `RegisterAdtVariant` + `gob.Register`
4. **Pattern match** — `__subject.Tag` int switch → Go type switch
5. **Field extraction** — `__subject.Fields[i].(T)` → typed `__subject.V0`
6. **Nullary handling** — `type X_V struct{}` gob-encode-fails →
   dummy exported field
7. **Parametric handling** — `Mod_X_Foo_V[T1]` instantiations
   gob.Register per-instantiation
8. **Carve-out respect** — use `shouldEmitSealedIface` from P3.3

This is ~1500-2000 LOC of coordinated change. Decomposing into
shippable sub-phases:

## P3.4a — emitSealedIfaceUnion helper (this commit)

Pure function. NOT WIRED. Spec-testable in isolation.

```haskell
-- | v0.17 sealed-iface emission for a monomorphic non-Enum
-- non-carve-out ADT. Returns the GoDecl list that would replace
-- the legacy @type X = rt.SkyADT@ + ctor-funcs + RegisterAdtTag
-- init block.
--
-- NOT WIRED in P3.4a — the generateUnion + generateUnionForDep
-- callers still emit the legacy shape. P3.4b wires this behind
-- 'shouldEmitSealedIface' (which still returns False at the call
-- sites, so no behavior change).
--
-- Shape produced for @type Color = Red | Green | RGB Int Int Int@:
--
--   type Mod_Color interface {
--       SkyVariantTag()  int
--       SkyVariantName() string
--   }
--
--   type Mod_Color_Red_V struct { SkyVariant_ uint8 }  -- dummy for gob
--   func (Mod_Color_Red_V) SkyVariantTag()  int    { return 0 }
--   func (Mod_Color_Red_V) SkyVariantName() string { return "Red" }
--
--   type Mod_Color_Green_V struct { SkyVariant_ uint8 }
--   func (Mod_Color_Green_V) SkyVariantTag()  int    { return 1 }
--   func (Mod_Color_Green_V) SkyVariantName() string { return "Green" }
--
--   type Mod_Color_RGB_V struct { V0 int; V1 int; V2 int }
--   func (Mod_Color_RGB_V) SkyVariantTag()  int    { return 2 }
--   func (Mod_Color_RGB_V) SkyVariantName() string { return "RGB" }
--
--   var Mod_Color_Red    = Mod_Color_Red_V{}
--   var Mod_Color_Green  = Mod_Color_Green_V{}
--   func Mod_Color_RGB(v0 int, v1 int, v2 int) Mod_Color_RGB_V {
--       return Mod_Color_RGB_V{V0: v0, V1: v1, V2: v2}
--   }
--
--   func init() {
--       rt.RegisterAdtTag("Red",   0)  -- legacy compat (kept)
--       rt.RegisterAdtTag("Green", 1)
--       rt.RegisterAdtTag("RGB",   2)
--
--       rt.RegisterAdtVariant("Red", func(raw []json.RawMessage) any {
--           return Mod_Color_Red_V{}
--       })
--       rt.RegisterAdtVariant("Green", func(raw []json.RawMessage) any {
--           return Mod_Color_Green_V{}
--       })
--       rt.RegisterAdtVariant("RGB", func(raw []json.RawMessage) any {
--           var v0 int; if len(raw) >= 1 { _ = json.Unmarshal(raw[0], &v0) }
--           var v1 int; if len(raw) >= 2 { _ = json.Unmarshal(raw[1], &v1) }
--           var v2 int; if len(raw) >= 3 { _ = json.Unmarshal(raw[2], &v2) }
--           return Mod_Color_RGB_V{V0: v0, V1: v1, V2: v2}
--       })
--
--       gob.Register(Mod_Color_Red_V{})    -- per Griller findings
--       gob.Register(Mod_Color_Green_V{})
--       gob.Register(Mod_Color_RGB_V{})
--   }
emitSealedIfaceUnion
    :: String                    -- ^ qualified type name (Mod_X)
    -> [Can.Ctor]                 -- ^ ADT ctors in declaration order
    -> [GoIr.GoDecl]
```

### Key emission rules

1. **Nullary variant gets `SkyVariant_ uint8` dummy field** —
   solves Griller 1 blocker #1 (`gob.Encode` fails on struct{}
   "no exported fields"). Underscore prefix communicates "ignore
   me" to readers while keeping the field exported. uint8 = 1 byte
   so encode/decode cost is minimal.
2. **N-ary variant uses `V0`, `V1`, ... named fields** typed from
   the ctor's `argTys` (same `safeReturnTypeFull` path as legacy
   ctor emission).
3. **Nullary ctor → top-level `var` of variant struct zero
   value** so `Mod_Color_Red` is usable as a value, matching the
   legacy emission's bare-ident calling shape.
4. **N-ary ctor → function returning the VARIANT STRUCT** (not
   the sealed interface). Go's structural subtyping widens at the
   use site — caller-side `var c Mod_Color = Mod_Color_RGB(...)`
   compiles cleanly because `Mod_Color_RGB_V` satisfies
   `Mod_Color`.
5. **gob.Register per variant** — the empirical fix Griller 1
   demanded. Idempotent at register time; encode-time tail call
   has the dummy field needed by Griller 1.
6. **Both registries written in init()** — legacy `RegisterAdtTag`
   stays so any rt-side code still consulting `LookupAdtTag` (e.g.
   `EnumTagIs`'s legacy branch) finds the ctor. `RegisterAdtVariant`
   adds the typed factory.

### Parametric ADT handling — DEFERRED to P4

P3.4 emits new shape only for **monomorphic non-Enum non-carve-out**
ADTs (per P3.3 rule 2 + rule 3). Parametric ADTs (Element / Maybe /
Result / etc.) stay on legacy SkyADT until P4 designs the Go-generic
variant shape coherently. This sidesteps Griller 2's `_V[T1]`
parametric-instantiation gob enumeration concern entirely for now.

## P3.4b — wire emitSealedIfaceUnion at generateUnion +
## generateUnionForDep (gated, False at runtime)

Replace the existing GoIr.GoDeclRaw "type qualType = rt.SkyADT" +
ctor-funcs path with:

```haskell
if shouldEmitSealedIface modName typeName vars opts
    then emitSealedIfaceUnion qualType ctors
    else <existing legacy emission>
```

`shouldEmitSealedIface` still returns False everywhere (P3.3
default), so this commit emits BYTE-IDENTICAL Go output to today.
Pure plumbing.

## P3.4c — pattern-match codegen type-switch

`caseToGo` / `coerceSubject` / `bindCtorArg` (Compile.hs:15529-16860)
gate on the same `shouldEmitSealedIface` check. When True, emit
type switch:

```go
switch __subject := <expr>.(type) {
case Mod_Color_Red_V:    <arm body — no field reads>
case Mod_Color_Green_V:  <arm body>
case Mod_Color_RGB_V:    -- bind via __subject.V0 / __subject.V1 / ...
                         <arm body>
default:
    panic(rt.Unreachable("case/Mod_Color"))
}
```

Coordinated with P3.4b — both must be in the same commit (or the
True branch's emission breaks at consumption time).

## P3.4d — flip carve-out for a single fixture ADT

Add ONE user ADT to the True path via a deliberate test-only
mechanism: a `SKY_VARIANT_ADT_DEBUG_ALLOW` env var or
similar (per-build, not per-call CAF — solved by reading it ONCE
into `scopeStateRef` at `resetCompileState`). Build + run focused
fixture. Verify floor reduces measurably on the fixture.

## P3.4e — flip default for all non-carve-out monomorphic ADTs

`shouldEmitSealedIface` rule 4 changes from `False` to `True`.
Run full example sweep. Measure floor on 26-ui-showcase. Expect
significant reduction.

## P3.4f — RtCoerceBudget gate

Lower the budget cap on 26-ui-showcase to reflect new floor.

## Implementation surface for P3.4a (THIS commit)

- `src/Sky/Build/Compile.hs` — new `emitSealedIfaceUnion` function
  (~200 LOC including factory + gob.Register emission)
- `test/Sky/Build/SealedIfaceEmissionSpec.hs` — call helper
  directly with hand-built `[Can.Ctor]` lists; assert GoDecl list
  shape (nullary variant has dummy field, N-ary variant has typed
  V0/V1/..., factory + gob.Register present)

P3.4a is purely additive — no callers yet. Risk to existing build:
zero. Risk that the helper's output is wrong: caught at the wire
in P3.4d. Per the per-commit-grill discipline, P3.4b and P3.4c
will each be designed + grilled in their own iters.

## Grill checklist (for the 2 parallel grillers)

1. Dummy field name `SkyVariant_` — collision risk with a user-
   defined ADT containing a constructor named `SkyVariant_`?
   (Unlikely; Sky parser banishes leading underscores AND user
   ADT ctor names get the `Mod_X_Ctor_V` mangle.)
2. Factory shape: `func(raw []json.RawMessage) any` — does the
   helper assume the codegen has access to `encoding/json` import?
   Verify the import is auto-added on first emission.
3. gob.Register at init time — order of Sky.Core.Error vs user
   modules. User-module init() runs after rt's. Confirmed via Go
   spec: imports init in dependency order.
4. Variant struct's method receiver is value, not pointer
   (`func (Mod_Color_Red_V) SkyVariantTag() int`). Reflect.Type
   for value vs pointer receivers differ; verify gob.Register
   accepts value-receiver-method structs (it does — `SkyADT`
   itself uses value receivers).
5. `safeReturnTypeFull` invocation — same path as today's ctor
   arg type emission. No new lowering surface introduced.
6. Naming: `Mod_X_Foo_V` for the variant struct, `Mod_X_Foo`
   stays as the constructor binding (var or func) so caller code
   doesn't change. Consistent with how today's `Mod_X_Foo` resolves.
7. Does the helper handle `Can.Unbox` (single-ctor single-arg
   ADT)? Today's emission has a special path. Verify P3.3's
   carve-out rule covers it — should rule 1 expand to include
   `Can.Unbox`?
