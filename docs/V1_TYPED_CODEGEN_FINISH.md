# V1 Typed Codegen Finish + Full Sky-Source Stdlib Migration

> Multi-session plan tracked in repo per the user-feedback rule. Commit
> checkpoints land on `feat/v1-roadmap` branch.

## Goal — the FINAL contract

**Every USED Sky function, value, lambda, and partial application
emits as fully-typed Go.** No `any` in USED code except:
1. **Genuinely-dynamic FFI returns** (raw JSON, reflect dispatch) — at
   the immediate boundary only, before the `rt.Coerce[T]` step
2. **Polymorphic context** — Go generics `[A, B any]` parameters
   (typed parametric, NOT untyped)
3. **Multi-arg ADT constructor fields** with HETEROGENEOUS types where
   no per-ctor typed Go struct exists yet (Stage 3 closes this last gap)

After this work: every byte of `any` in emitted Go is provably necessary.

## Architectural deliverables (in dependency order)

### Stage 1 — Typed lambda lowering (Gap 4 complete)

Currently `Gap 4` is "substantially closed" per CLAUDE.md — input types
flow when `curryLambdaPatTyped` fires, but output types stay `any`.

**Target:** for every lambda `\x -> body` where HM has inferred both
input AND output types:

```go
// Today:
func(x any) any { return rt.AsInt(x) * 2 }

// Target:
func(x int) int { return x * 2 }
```

**Sites to touch:**
- `curryLambdaPat` in Compile.hs — accept optional output Go type
- `kernelTypedCall` — pass HM-inferred output type to lambda lowering
- `coerceCallArgsAt` — same
- All HOF kernel signatures in `lookupKernelType` — capture output
  TVar's resolution at call site

**Regression fence:** every example's main.go has zero unjustified
`func(any) any` shapes. Measure via grep + diff vs baseline.

### Stage 2 — Typed partial application

Currently `Decimal.add five` emits `func(any) any` (Sky's curry shape).
With known types, should emit `func(Decimal) Decimal`.

**Target:**
```elm
let inc = Decimal.add (Decimal.fromInt 1)
    result = inc someDecimal
```
emits:
```go
inc := func(b decimal.Decimal) decimal.Decimal {
    return rt.Decimal_add(rt.Decimal_fromInt(1), b)
}
result := inc(someDecimal)
```

**Sites to touch:**
- `emitPartialUserCall` / `emitPartialCtor` in Compile.hs
- Cross-module function-value emission (let-binding holds a typed
  closure type from imported module)
- The let-binding codegen — declare the binding with its full HM type

### Stage 3 — Per-ADT-ctor typed Go structs

Currently all multi-arg constructors use `SkyADT{Tag, SkyName,
Fields []any}` for uniformity. Heterogeneous fields are stored as
`[]any` — the last legitimate `any` source in USED code.

**Target:**
```elm
type Person = Person String Int
let p = Person "Alice" 30
```
emits:
```go
type Sky_Person_Person struct {
    Tag     int
    SkyName string
    V0      string   // typed instead of any
    V1      int
}
var Sky_Person_Person_Person = func(v0 string, v1 int) Sky_Person_Person {
    return Sky_Person_Person{Tag: 0, SkyName: "Person", V0: v0, V1: v1}
}
p := Sky_Person_Person_Person("Alice", 30)
```

Pattern destructure path:
```elm
case p of Person n a -> ...
```
emits:
```go
if __subject.Tag == 0 {
    n := __subject.V0   // typed string access — no any.(string)
    a := __subject.V1   // typed int access
    ...
}
```

Polymorphic ADTs continue to use the existing `SkyADT` for the generic
case; concrete instantiations get typed structs.

**Sites to touch:**
- `generateCtorFunc` in Compile.hs — emit typed struct + constructor
- `pattern-match codegen` — emit typed field access for typed ctors
- Cross-module ctor reference — preserve typed struct name through imports

### Stage 4 — Ffi.kernel mechanism

The Sky-source declaration layer that routes to existing kernel
dispatch transparently.

```elm
-- sky-stdlib/Sky/Core/List.sky
map : (a -> b) -> List a -> List b
map = Ffi.kernel "List_map"
```

**Codegen behaviour:**
- At codegen-init, scan all Sky-source modules. Build registry
  `<SkyFnName> → <KernelName>` for every binding whose body is exactly
  `Ffi.kernel "NAME"`.
- At every `Can.Call` site, if `func` resolves to a registered
  Sky-source kernel-alias, rewrite the callee to
  `Can.VarKernel kernelMod kernelName` and fall through to existing
  dispatch (kernelTypedCall, typedKernelLiterals, etc.).
- For partial app / HOF pass: emit a typed Sky-source trampoline
  (typed thanks to Stage 1 + 2).

**Sites to touch:**
- `lookupKernelType` — register `Ffi.kernel : String -> a`
- `Module.hs` — add `"kernel"` to the `Ffi` whitelist
- Compile.hs — registry build + call-site rewrite (~50 LOC)
- rt.go — register `Ffi_kernel` (runtime identity that panics —
  Ffi.kernel should never reach the runtime; codegen inlines all uses)

### Stage 5 — Full Sky-source stdlib migration

Move ALL ~25 kernel-registered modules to Sky source using `Ffi.kernel`.

Modules to migrate:
- `Sky.Core.String`, `Sky.Core.List`, `Sky.Core.Dict`, `Sky.Core.Set`
- `Sky.Core.Char`, `Sky.Core.Math`, `Sky.Core.Regex`, `Sky.Core.Path`
- `Sky.Core.Crypto`, `Sky.Core.Encoding`, `Sky.Core.Uuid`
- `Sky.Core.Json.{Encode, Decode, Decode.Pipeline}`
- `Sky.Core.{Time, Random, Http, File, Io, System, Process, Task}`
- `Std.{Cmd, Sub, Log, Db, Auth, Live, Jobs, Cli, Tui}`
- `Sky.Http.{Server, RateLimit, Middleware}`

For each module:
1. Write `sky-stdlib/<path>/<Module>.sky` with type sigs +
   `Ffi.kernel "NAME"` bodies
2. Remove kernel-registration entries from `lookupKernelType` and
   `Canonicalise/Module.hs`
3. Verify all examples that use the module still build + run identically

**Per-module commit cadence:** one commit per module. Each commit
verified by example sweep + cabal test before moving to the next.

### Stage 6 — Documentation

- Update `CLAUDE.md` standard-library section to reflect Sky-source
  status of every module
- Update `templates/CLAUDE.md` to teach AI tooling to write
  `Ffi.kernel`-style declarations for new stdlib additions
- Update `docs/stdlib.md` to point users at the Sky source files as
  the canonical reference
- Note in the language ref: "every Sky stdlib module is Sky source;
  Go runtime functions are accessed via the `Ffi.kernel` declaration
  layer"

## Progress tracker

- [x] Phase 2.4 Std.Decimal + Std.Money + Std.Time landed (e6039ab)
- [ ] Stage 1 — Typed lambda lowering (Gap 4 complete)
- [ ] Stage 2 — Typed partial application
- [ ] Stage 3 — Per-ADT-ctor typed Go structs
- [ ] Stage 4 — Ffi.kernel mechanism
- [ ] Stage 5 — Full stdlib migration (per-module)
- [ ] Stage 6 — Documentation sync

## Risk management

- Each stage MUST pass cabal test + 26-example sweep before next begins
- Memory guard (`scripts/mem-guard.sh`) must run during compiler dev
- Background-task hygiene checklist before every checkpoint commit
- Per-module migration commits are individually revertable
- No `--no-verify` skip of hooks
- Never tag release without explicit user ask

## Estimated effort

3-4 focused dev weeks total:
- Stage 1: 5-7 days
- Stage 2: 3-5 days
- Stage 3: 5-7 days
- Stage 4: 1-2 days
- Stage 5: 3-5 days (mechanical once foundation is solid)
- Stage 6: 1 day

Single Claude session is ~3-4 hours of focused work. So expect
8-15 sessions across the work.
