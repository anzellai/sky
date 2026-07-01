# P3 design: sealed-iface emission for monomorphic user ADTs

## Scope (this iter)

- **In:** monomorphic user-defined ADTs (`type Color = Red | Green | RGB Int Int Int`). No type vars.
- **Out (later P4):** parametric user ADTs (`type Maybe a = ...`) + Element/Attribute/Html stdlib.
- **Carve-out:** `Can.Enum` (all-nullary) stays on `type X int + iota` — unchanged.

## Trigger

Environment variable `SKY_VARIANT_ADT=1` at compile time. Default off → byte-identical
emission to today. When set → both the ADT declaration AND the case-of pattern
matching emit the new shape. Both must flip atomically; pattern-match codegen
cannot read SkyADT.Fields against a sealed-iface variant.

## New emission for non-Enum monomorphic union

For `type Mod_X = A | B Int String`:

```go
// Sealed interface — sealing method is the SkyVariant pair already in rt.go (P1).
type Mod_X interface {
    SkyVariantTag() int
    SkyVariantName() string
}

// Per-variant concrete structs with TYPED payload fields (no Fields []any).
type Mod_X_A_V struct{}
func (Mod_X_A_V) SkyVariantTag()  int    { return 0 }
func (Mod_X_A_V) SkyVariantName() string { return "A" }

type Mod_X_B_V struct {
    V0 int
    V1 string
}
func (Mod_X_B_V) SkyVariantTag()  int    { return 1 }
func (Mod_X_B_V) SkyVariantName() string { return "B" }

// Constructors return the variant struct directly.
// Go's structural subtyping auto-widens to Mod_X at use sites (interface
// satisfaction is by method set — no explicit cast or rt.Coerce).
var Mod_X_A = Mod_X_A_V{}
func Mod_X_B(v0 int, v1 string) Mod_X_B_V {
    return Mod_X_B_V{V0: v0, V1: v1}
}

// Factory for the wire-dispatch path (__sky_send), registered via the
// P2 BuildAdtFromWire registry.
func init() {
    rt.RegisterAdtVariant("A", func(raw []json.RawMessage) any { return Mod_X_A_V{} })
    rt.RegisterAdtVariant("B", func(raw []json.RawMessage) any {
        var v0 int;    if len(raw) >= 1 { _ = json.Unmarshal(raw[0], &v0) }
        var v1 string; if len(raw) >= 2 { _ = json.Unmarshal(raw[1], &v1) }
        return Mod_X_B_V{V0: v0, V1: v1}
    })
    // Legacy compat: keep RegisterAdtTag so any code still going through
    // LookupAdtTag (msg_logging.go, EnumTagIs paths) keeps working.
    rt.RegisterAdtTag("A", 0)
    rt.RegisterAdtTag("B", 1)
}
```

## Pattern-match emission (new shape)

Current (legacy SkyADT):
```go
__subject := msg.(Mod_X)  // = rt.SkyADT alias
switch __subject.Tag {
case 0: ...                       // nullary A
case 1:
    v0 := __subject.Fields[0].(int)
    v1 := __subject.Fields[1].(string)
    ...
}
```

New (sealed-iface):
```go
switch __subject := msg.(type) {
case Mod_X_A_V:
    ...
case Mod_X_B_V:
    v0 := __subject.V0   // typed int — no Fields[i].(int)
    v1 := __subject.V1   // typed string
    ...
default:
    panic(rt.Unreachable("case/Mod_X"))
}
```

## Compile.hs touch points

| Site | Change |
|---|---|
| `generateUnion` ~5792 (entry-module) | Gate Can.Enum-or-Non-Enum: if SKY_VARIANT_ADT set AND vars=[] AND opts≠Can.Enum, emit new shape; else legacy. |
| `generateUnionForDep` ~4849 (dep-module) | Same gate. |
| `coerceSubject` ~15606 + caseToGo ~15529 | Gate: if subject type is a sealed-iface (per ADT-shape registry), emit `switch __subject := e.(type)` instead of `__subject.(typeName)`. |
| `bindCtorArg` ~16695 | Gate: if in new-shape branch, emit `__subject.V0` instead of `__subject.Fields[0].(int)`. |
| New helper: `generateVariantStructs` — emits the variant struct + methods + factory init. |

## Verification fixture

`test/fixtures/p3-sealed-iface-monomorphic/`:
- `type Color = Red | Green | RGB Int Int Int`
- `type Shape = Circle Float | Square Float Float | Triangle Float Float Float`
- Bottom-line: 26-ui-showcase floor unchanged when env unset; focused fixture builds clean + runs with sealed shape when env set.

## Risks identified (for the grill to attack)

1. **Atomic emission contract**: emission + pattern-match must both flip together. Are there any code paths where one fires without the other?
2. **Constructor widening**: `var c Mod_X = Mod_X_A` — interface satisfaction is structural in Go. Should work. Verify in a small Go test.
3. **Cross-module ADT references**: ModB does `case x of Foo y -> ...` where Foo is ModA's ctor. ModB's pattern-match needs to see ModA's variant struct type. Are they exported (capital? Codegen uses `Mod_X_Foo_V` — already capital). Single-package emission means visibility is fine.
4. **Comparison**: msg-equality via `==` on variants. Variants with all-comparable fields work. User ADTs containing closures (`type Cmd msg = Perform (Task ...)`) are uncomparable structs — but those are parametric (P4 scope), not in P3.
5. **gob round-trip**: variants are new concrete types; gob needs registration. User decided "accept break" so no MarshalBinary needed. But `gobRegisterAll` walks values reachable from Model. New variants must register transparently. Per workflow runtime grill, `isSkyWrapperType` extension to recognise `_V` suffix needed.
6. **RegisterAdtTag dual-write**: keeping `RegisterAdtTag` calls in the init block as compat with legacy callers (msg_logging.go reads .SkyName field; on variants that's now a method — does legacy `LookupAdtTag` still need both? Yes, for SkyADT-shaped FFI builders.)
7. **Sky.Live handler closure**: when the renderer encounters `onClick (RGB 255 0 0)`, sess.handlers[hid] = ... the value of `RGB 255 0 0` which is now `Mod_X_RGB_V{V0:255, V1:0, V2:0}` (a SkyVariant). applyMsgArgs's IsFinalisedAdt guard (P2) skips reflect-applying — correct.
8. **Curried Msg ctor in handler map**: `onInput UpdateEmail` — UpdateEmail is now `func(string) Main_Msg_UpdateEmail_V`. Renderer stores the function. applyMsgArgs reflects it. Type signature changed from `func(any) any` to `func(string) Main_Msg_UpdateEmail_V` — does applyMsgArgs's reflect.Value.Call handle this? Should, because Call accepts reflect.Value args matching the typed parameters. Verify.
9. **Code paths that construct SkyADT from rt-side Go FFI**: e.g. Sky.Core.Error uses `makeError` to build SkyADT. The user's case-of on Error becomes type-switch on Mod_Sky_Core_Error_* variants. But makeError still emits SkyADT — type-switch misses → default panic. Resolution: Error stays on legacy SkyADT in P3 (carve-out, like Can.Enum).
