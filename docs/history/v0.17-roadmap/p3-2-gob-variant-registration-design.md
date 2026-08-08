# P3.2 design: gob registration for sealed-iface variants — SUPERSEDED

**STATUS**: SUPERSEDED iter 48 by dual-griller findings. P3.2 as a
standalone runtime-side phase added the risk surface that codegen-
side gob.Register (P3.4) eliminates entirely. Findings preserved
below for the P3.4 design checklist.

### Why superseded

Two parallel adversarial grillers (iter 48, agentId
a26400e375ae64e7e + ad4476edf1fec9a23) BOTH returned needs-revision
on this design. Each found a DIFFERENT fatal flaw that one grill
alone would have missed — validating the AUTONOMOUS protocol's 2+
parallel grillers rule.

**Griller 1 (factory invocation + gob behaviour)**:
- Nullary variants (`struct{}` — the canonical `type Msg = Increment | Decrement`
  shape) gob.Register fine but FAIL at encode time with "no exported
  fields". Counter-app fails at first session-persist.
- `defer recover()` silently masks codegen bugs.
- factory(nil) returns a non-nil interface even for nil factories;
  semantic nil-check wrong.

**Griller 2 (suffix + parametric)**:
- `strings.HasSuffix(name, "_V")` MISSES `Mod_Msg_Bar_V[T1]` —
  Go generics put brackets AFTER `_V`. The Sky v0.15-baseline emits
  parametric variants in this exact shape. Silent miss.
- `walkGobType` has no `reflect.Interface` arm; can't reach
  variants through interface-typed fields anyway.
- `SkyMaybe[Mod_Msg]` (specific generic instantiation) needs
  registration distinct from `SkyMaybe[any]`.

### Decision: defer all gob handling to P3.4 codegen emission

Codegen knows the EXACT concrete type for each variant at lowering
time. The P3.4 codegen design checklist must:

1. Emit a dummy exported field on nullary variants (`type Foo_V struct { _SkyVariantTag uint8 }`)
   so gob.Encode of `Maybe Foo` succeeds at the interface boundary.
2. Emit `gob.Register(Mod_Msg_Foo_V{})` per ctor in the same init()
   block that calls `RegisterAdtVariant` — codegen handles
   parametric instantiations naturally by emitting each ground
   instantiation it monomorphises.
3. Pre-register `SkyMaybe[Mod_Msg]` / `SkyResult[Error, Mod_Msg]`
   / `SkyTuple2[X, Mod_Msg]` for every ADT that flows through a
   Sky.Live Model field — codegen-side type-graph walk
   (Sky.Build.Compile already has the surface map).
4. Skip `RegisterAdtVariant` + `gob.Register` emission for ADTs in
   the rt-builder-shadow-list (Sky.Core.Error / Sky.Db.Sql.SqlValue
   / Sky.Decimal.Internal / Sky.Http.StreamMsg / WebSocket). Those
   stay on legacy SkyADT shape; the SkyADT shape is already
   gob-registered at live_store.go:121.

P3.4 design (next iter) must address all 4 points BEFORE the
codegen flip ships. The dual-griller findings are the test fixtures
for that design's grill round.

---

# Original P3.2 design (PRESERVED for grill-checklist context)


## Problem

Sealed-iface ADTs (P3.4+) make user-defined `type Msg = Foo | Bar | Baz`
lower as:

```go
type Mod_Msg interface { SkyVariantTag() int; SkyVariantName() string }
type Mod_Msg_Foo_V struct {}
type Mod_Msg_Bar_V struct { V0 int }
```

The Sky.Live Model holds `currentMsg : Maybe Msg` → Go-side
`SkyMaybe[Mod_Msg]`, which is an interface field. gob CANNOT encode
values at an interface boundary unless the concrete type is registered.

Today's `gobRegisterAll` (live_store.go:53) walks VALUES — sees only
the variants present in the init Model. If init has `Just (Foo)` but a
later Cmd produces `Bar`, the second SSE-persisted snapshot fails:
`gob: type not registered for interface: Mod_Msg_Bar_V`.

`walkGobType` (live_store.go:70) walks TYPES — but an interface type
has zero concrete impls discoverable structurally. Walking
`Mod_Msg` (interface) does not yield `Mod_Msg_Foo_V`.

## Fix (additive, no behaviour change until codegen flips)

Extend `RegisterAdtVariant` (rt/adt_variant_factory.go) to ALSO
`gob.Register` the zero-value of the factory's output. Codegen P3.4+
already calls `RegisterAdtVariant("Foo", factoryFn)` in init() for
every ctor — invoking the factory with `nil` raw args yields the
variant's zero value (`Mod_Msg_Foo_V{}`), which is exactly what gob
needs registered:

```go
func RegisterAdtVariant(skyName string, factory AdtVariantFactory) {
    adtVariantRegistryMu.Lock()
    adtVariantRegistry[skyName] = factory
    adtVariantRegistryMu.Unlock()

    // Also register the variant's zero value with gob so Sky.Live's
    // session walker can encode it at interface boundaries even when
    // the init Model contains no instance. The factory invoked with
    // a nil rawArgs slice returns the variant's zero value because
    // every codegen-emitted factory decodes from json.RawMessage and
    // leaves fields at Go-zero when raw is shorter than the variant's
    // arity. gob.Register is idempotent (panics → defer recover so
    // duplicate registration via init+RegisterAdtVariant is safe).
    if zero := factory(nil); zero != nil {
        defer func() { recover() }()
        gob.Register(zero)
    }
}
```

Plus extend `isSkyWrapperType` to recognise the `_V` suffix
structurally — defence in depth for `GobRegisterTypeGraph` callers
walking type graphs that include variant struct types as fields:

```go
func isSkyWrapperType(t reflect.Type) bool {
    name := t.Name()
    return strings.HasPrefix(name, "SkyMaybe[") ||
        strings.HasPrefix(name, "SkyResult[") ||
        strings.HasPrefix(name, "SkyTuple2[") ||
        strings.HasPrefix(name, "SkyTuple3[") ||
        strings.HasPrefix(name, "SkyTask[") ||
        strings.HasSuffix(name, "_V")  // v0.17 sealed-iface variant
}
```

## Why factory-zero, not codegen-emitted gob.Register

Two paths considered:

(A) Codegen emits per-variant `gob.Register` in the same init() that
    calls `RegisterAdtVariant`. Pros: explicit + auditable. Cons:
    adds N more codegen IR nodes per ADT (doubles emission per ctor).
(B) `RegisterAdtVariant` itself registers — single source of truth.
    Pros: codegen IR doesn't change. Cons: invokes the factory at
    boot to get the zero value (extra alloc per ctor at init).

(B) chosen because codegen flip (P3.4) is the riskiest commit class —
keeping its IR change as small as possible reduces grill surface.
The boot-time alloc is per-init, single-pass, microseconds.

## Risks for grillers

1. **factory(nil) safety**: when raw=nil, the factory's
   `if len(raw) >= 1 { _ = json.Unmarshal(...) }` guards leave fields
   at Go-zero. For payload types Go-zero is meaningful (int=0,
   string=""). For complex payloads (record alias), Go-zero is the
   record's zero. None of these should panic at boot.
2. **gob.Register on a value containing a func field**: closures /
   func payloads in variants (Lazy with `func() Element`) panic gob.
   But the codegen-emitted factory for such variants would return a
   zero with nil func — gob may or may not accept that. Verify.
3. **Idempotency**: `gob.Register` panics on duplicate or shadow.
   `defer recover()` catches it. Confirm no leaked state on recover.
4. **Carve-out coordination**: rt-side-built ADTs (Sky.Core.Error,
   Sql.SqlValue, etc.) stay on legacy SkyADT path and DON'T call
   RegisterAdtVariant. Their legacy SkyADT registration at
   live_store.go:118-125 already handles them. No double-register.
5. **`_V` suffix collision**: any user type named `Foo_V` that's NOT
   a sealed-iface variant would be picked up by isSkyWrapperType.
   Mitigation: codegen mints variants as `Mod_X_Ctor_V` — the
   `Mod_` prefix is module-qualified. A user-defined `type Foo_V`
   would lower as `Mod_Foo_V`. Same suffix match — but isSkyWrapperType
   only widens what gets walked for registration; false-positives
   here register a struct gob can handle anyway (any pure-data
   user struct). Cost: zero correctness risk, small allocation cost.

## Verification

- rt regression suite still 7.5s green
- New test: RegisterAdtVariant for a variant struct → gob.Encode
  succeeds at an interface boundary
- Floor anchor unchanged (codegen flip not yet shipped)

## Implementation surface

`runtime-go/rt/adt_variant_factory.go` — ~15 LOC extension to
RegisterAdtVariant + import "encoding/gob".

`runtime-go/rt/live_store.go` — 1 line to isSkyWrapperType.

Test file: `runtime-go/rt/adt_variant_gob_test.go` — gob encode/decode
round-trip through a Sky.Live session-style interface boundary.
