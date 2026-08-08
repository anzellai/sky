# Phase 4 — Per-Msg dispatch codegen lever

**Status**: architectural design (iter 14, no code change)
**Branch base**: 35178163 (iter 13 floor: rt.Coerce 26-ui-showcase=172, 00-standard-libs=124)
**Predecessor**: `phase3-kernel-routing-typed-prop.md`
**Successor**: dedicated implementation track post-P5 saturation

## Executive summary

The Sky.Live wire dispatch path (and every TEA-shaped backend — Sky.Tui,
Sky.Cli, Sky.Webview) routes every user event through a reflection-driven
adapter (`sky_call` / `sky_call2` / `adaptFuncValue` / `reflect.MakeFunc`).
That adapter is correct + complete, but pays per-dispatch costs that are
**already statically knowable** from the Msg ADT shape:

* `reflect.ValueOf(fn)` per call
* `reflect.Type.NumIn` + `reflect.Type.In(i)` lookups
* `reflect.MakeFunc` allocation for curried Sky lambdas flowing through
  Go-typed FFI slots
* per-arg `narrowReflectValue` walk
* per-call `runtime.FuncForPC` for `msgDisplayName`

The compiler already knows the full Msg union shape — `type Msg = …` at
canonicalisation gives us tag, constructor name, constructor arity, and
**typed** constructor parameters. We can codegen a per-Msg typed dispatch
table and call into it without touching the reflect machinery on the hot
path.

This is the "static-dispatch lever" that closes the **last big runtime
reflect chunk** after the rt.Coerce elision floor (iter 13). Together
they deliver: well-typed Sky code → well-typed Go code → no reflection
in the steady-state event loop.

## Survey of reflect sites (runtime-go/rt)

### Site 1 — `adaptFuncValue` at `rt.go:128` (reflect.MakeFunc)

Single site uses `reflect.MakeFunc`. Called from `Coerce[T]` when a Sky
`func(any) any` lambda must satisfy a Go-typed callback (HTTP handlers,
Fyne callbacks, json decoders, kernel callback fields).

```go
func adaptFuncValueWithCapture(skyFn reflect.Value, targetTy reflect.Type, captured []reflect.Value) reflect.Value {
    return reflect.MakeFunc(targetTy, func(inArgs []reflect.Value) []reflect.Value {
        // boxes args to any, calls skyFn, unwraps return
    })
}
```

Hot-path callers:
* every `rt.Coerce[func(http.ResponseWriter, *http.Request)](handler)` at boot
* every record-update of a function-typed field at runtime
* Decoder pipelines that flow a partially-applied ctor through `Decode.andThen`

This site is **boundary code** (Sky→Go FFI shim). Not Phase 4 scope.
Tracked separately under "FFI-adapter audit" because eliminating it
requires emitting typed Go closures at every Coerce[func(...)] site,
which is a larger codegen surface.

### Site 2 — `sky_call` / `sky_call2` at `live.go:7895` / `7922` (reflect.ValueOf + reflect.Call)

```go
func sky_call(f any, arg any) any {
    rv := reflect.ValueOf(f)
    if rv.Kind() != reflect.Func { return f }
    // ... reflect.Call
}
```

Called from **every Msg dispatch** in:
* `dispatchMsgImpl` @ line 4400 (`sky_call2(app.update, msg, sess.model)`)
* `dispatchMsgImpl` @ line 4366 (`sky_call2(app.guard, msg, sess.model)`)
* `Cmd.perform` completion @ line 4702 (`sky_call(toMsg, result)`)
* `Sub.every` tick @ line 4920 (`sky_call(toMsg, t.UnixMilli())`)
* `Sub.subscribeTopic` delivery @ line 5169 (`sky_call(toMsg, ev.Payload)`)
* `Http.Stream` chunk delivery @ line 5388 (`sky_call(toMsg, chunkVal)`)
* `applyMsgArgs` @ line 1711 (curried-ctor Msg construction from wire args)
* `__sky_send` direct-construct @ line 4012 / 4185

These ARE Phase 4 scope. Each call site pays a per-dispatch reflect
cost in the steady-state user-input → update → view loop.

### Site 3 — `msgDisplayName` at `live.go:481` (runtime.FuncForPC + reflect)

```go
if rv.Kind() == reflect.Func {
    name := runtime.FuncForPC(rv.Pointer()).Name()
    // ...
}
```

Called for every dispatched Msg to label the trace span + log entry. With
the v0.17 `SkyVariant` interface, struct-shaped Msgs already short-circuit
via `sv.SkyVariantName()` — only function-typed ctors (partial-application)
fall through. Phase 4 narrows this further by emitting **per-binding-site
typed dispatchers** that carry the Msg name in their closure.

### Site 4 — `applyMsgArgs` / `decodeMsgArg` at `live.go:1692` / `1744`

```go
ptr := reflect.New(paramT)
if err := json.Unmarshal(raw, ptr.Interface()); err == nil {
    return ptr.Elem().Interface()
}
```

Wire-arg decode into the typed ctor parameter shape. Currently uses
reflect to find the parameter type. Phase 4: codegen a per-Msg
`__skyMsgDecode_<MsgName>(raw []json.RawMessage) any` dispatcher that
calls into typed `json.Unmarshal` directly — no reflect.New, no
reflect.Type.In(0) lookup.

## Compile-time Msg knowledge

The compiler knows the Msg union completely at code-emission time. From
`examples/19-skyforum/sky-out/main.go`:

```go
type State_Msg = rt.SkyADT

func State_Msg_Navigate(v0 State_Page) State_Msg { ... Tag: 0, SkyName: "Navigate" }
func State_Msg_UpvotePost(v0 int) State_Msg     { ... Tag: 1, SkyName: "UpvotePost" }
// ... constructors with typed parameters ...

func init() {
    rt.RegisterAdtTag("Navigate", 0)
    rt.RegisterAdtTag("UpvotePost", 1)
    // ...
}
```

The pieces needed for static dispatch are ALREADY present in the
emitted code:

* The Msg type name (`State_Msg`)
* Each variant's tag (0, 1, ...) and SkyName
* Each variant's typed parameter list (`v0 State_Page`, `v0 int`, ...)
* The `init()` block that runs at package load

## The dispatch lever

### What we emit today

```go
// adapt closures + Coerce dispatch + reflect.Call per Msg
sky_call2(app.update, msg, sess.model)
```

Even with the V-shape sealed iface (post-PR3), the `update` function
takes `(any, any) -> (any, any)` so we still hop through reflect.

### What we emit post-Phase 4

```go
// Per-app generated typed dispatch table
var stateUpdateDispatch = map[int]func(payload any, model State_Model_R) (State_Model_R, SkyCmd){
    0: func(p any, m State_Model_R) (State_Model_R, SkyCmd) {
        return Main_update_Navigate(p.(State_Page), m)
    },
    1: func(p any, m State_Model_R) (State_Model_R, SkyCmd) {
        return Main_update_UpvotePost(p.(int), m)
    },
    // ...
}

// Hot path: 1 map lookup + 1 typed function call. No reflect.
func dispatchMsg(msg any, model State_Model_R) (State_Model_R, SkyCmd) {
    if v, ok := msg.(SkyVariant); ok {
        if fn, ok := stateUpdateDispatch[v.SkyVariantTag()]; ok {
            return fn(v.SkyVariantPayload(), model)  // typed-payload accessor
        }
    }
    // Fallback to legacy reflect path for func-typed Msgs (currying)
    return reflectDispatch(msg, model)
}
```

The dispatch table:
* lives in package-level data, built in `init()`
* is keyed by integer tag (constant per Msg ADT)
* each entry is a fully-typed Go function — no `func(any) any`
* falls back to the reflect path when the Msg is a partially-applied
  ctor (which Sky uses for `\arg -> Increment arg`-style point-free
  shapes that don't construct the ADT struct yet)

### Compiler changes

#### Change 1 — emit per-Msg typed update arms

The compiler already knows that `update : Msg -> Model -> (Model, Cmd Msg)`
breaks down into one Sky case-arm per Msg variant. We codegen each arm
as a SEPARATE typed Go function:

```go
// Sky:  Increment -> ({ model | count = model.count + 1 }, Cmd.none)
func Main_update_Increment(model Main_Model_R) (Main_Model_R, rt.SkyCmd) {
    return Main_Model_R{Count: model.Count + 1, /* ... */}, rt.Cmd_none()
}
```

The full `update` function stays in the emitted Go file (for FFI
boundary use — `Live.app cfg` still passes `update : any` through
typed-codegen). But each arm gets a typed extracted form.

#### Change 2 — emit per-app dispatch table at boot

```go
var Main_update_dispatch = map[int]func(payload any, model Main_Model_R) (Main_Model_R, rt.SkyCmd){
    /* one entry per Msg variant */
}

func init() {
    Main_update_dispatch[0] = func(p any, m Main_Model_R) (Main_Model_R, rt.SkyCmd) { return Main_update_Increment(m) }
    Main_update_dispatch[1] = func(p any, m Main_Model_R) (Main_Model_R, rt.SkyCmd) { return Main_update_DoSignIn(p.(Main_AuthCreds_R), m) }
    // ...
    rt.RegisterMsgDispatch("Main_Msg", Main_update_dispatch)
}
```

The runtime API `rt.RegisterMsgDispatch(adtTypeName string, table any)`
allows the runtime to fast-path future dispatches via the typed map
when the Msg's SkyVariant interface yields a tag this table knows.

#### Change 3 — emit per-Msg wire decoders

For wire-driven dispatch (the user clicked a button labelled with a
typed Msg), we already have `applyMsgArgs` deciding how to decode the
JSON args into the typed ctor parameter. Phase 4 emits a typed
decoder per Msg:

```go
func Main_Msg_decode_DoSignIn(raw json.RawMessage) (any, error) {
    var v0 Main_AuthCreds_R
    if err := json.Unmarshal(raw, &v0); err != nil {
        return nil, err
    }
    return Main_Msg_DoSignIn(v0), nil
}
```

Registered analogously: `rt.RegisterMsgDecoder("DoSignIn", Main_Msg_decode_DoSignIn)`.

The wire path replaces:

```go
ptr := reflect.New(paramT)
json.Unmarshal(raw, ptr.Interface())
return ptr.Elem().Interface()
```

with:

```go
if dec, ok := rt.LookupMsgDecoder(msgName); ok {
    return dec(raw)
}
// fallback to reflect path for func-typed Msg ctors
```

### Runtime changes

Add three runtime functions and three registries:

```go
// runtime-go/rt/msg_dispatch.go (NEW)

var msgUpdateDispatch = make(map[string]any)        // adt name -> typed update map
var msgUpdateDispatchMu sync.RWMutex

func RegisterMsgUpdate(adtName string, table any) {
    msgUpdateDispatchMu.Lock()
    msgUpdateDispatch[adtName] = table
    msgUpdateDispatchMu.Unlock()
}

var msgDecoders = make(map[string]MsgDecoder)       // ctor name -> wire decoder
var msgDecodersMu sync.RWMutex

type MsgDecoder func(raw json.RawMessage) (any, error)

func RegisterMsgDecoder(ctorName string, dec MsgDecoder) {
    msgDecodersMu.Lock()
    msgDecoders[ctorName] = dec
    msgDecodersMu.Unlock()
}

// Per-binding-site dispatcher: a Sub.every emits ToMsg = Tick. We register
// Tick → typed-constructor at boot, so the steady-state tick path is one
// map lookup + one typed ctor call.
var msgToMsgDispatch = make(map[string]any)         // ctor name -> typed ctor fn
var msgToMsgDispatchMu sync.RWMutex
```

The runtime `sky_call` family adds a fast-path FIRST and only falls
back to reflect when the lookup misses:

```go
func sky_call(f any, arg any) any {
    if name := msgFunctionName(f); name != "" {
        if dec, ok := lookupMsgDispatch(name); ok {
            return dec(arg)
        }
    }
    // existing reflect path unchanged
    rv := reflect.ValueOf(f)
    // ...
}
```

### Closure capture for partial application

Sky idiom: `\arg -> Increment arg`. Lowered today as `func(arg any) any { return Increment_(arg.(int)) }`.
Phase 4 still emits the same wrapper — but if the compiler can
**statically** see that the lambda body is a SINGLE ctor application
with no extra computation, it adds a registration:

```go
// Sky: Sub.subscribeTopic "count" Increment
// Emit: rt.RegisterMsgToMsg("Sub_subscribeTopic@line42", Main_Msg_Increment)
```

The runtime's `sky_call(toMsg, payload)` checks the typed registry
first; on hit, calls the typed ctor with a typed `.(int)` arg. Miss
→ legacy path. This handles the curried-shape Msg ctors that today
push through reflect.MakeFunc when crossing the `func(any) any` slot.

## Iteration plan

| Iter | What | Verify |
|---|---|---|
| 1 | New module `Sky.Build.MsgDispatch` — pure helper that enumerates ADT variants + typed parameter shape from Solve.SolvedTypes; smoke tests | unit specs in `test/Sky/Build/MsgDispatchSpec.hs` |
| 2 | Runtime additions: `RegisterMsgUpdate` / `RegisterMsgDecoder` / `RegisterMsgToMsg` + lookup helpers; no caller wired yet | `runtime-go/rt/msg_dispatch_test.go` covering register + lookup + concurrency |
| 3 | Codegen change 1: for each `app.update : Msg -> Model -> (Model, Cmd)` definition, emit per-variant typed Go functions alongside the unified `Main_update` (keep the legacy function — typed callers can choose) | golden-file regression in `test/Sky/Build/PerMsgUpdateSpec.hs` |
| 4 | Codegen change 2: emit the dispatch table + init() registration for every Msg ADT that's the type-arg of `Live.app cfg` | end-to-end build of `examples/19-skyforum` + assert dispatch map populated at boot |
| 5 | Codegen change 3: per-Msg wire decoders for variants with non-`any` ctor parameters | examples/13-skyshop POST flow (sign-in form) via integration test |
| 6 | Runtime change: `sky_call` / `sky_call2` consult the typed dispatch table FIRST; reflect path unchanged | Benchmark `BenchmarkMsgDispatch` shows 5-10× speedup on typed paths |
| 7 | Migrate Cmd.perform completion / Sub.every tick / Sub.subscribeTopic / Http.Stream chunk callers to register their `toMsg` ctor at boot | Each scenario gains the typed-dispatch coverage |
| 8 | Architectural verify: run example sweep + 410+ cabal specs + Playwright + `scripts/verify-all-web.sh` + `scripts/verify-cli.sh` | All green; no regressions; no new IORef leak |
| 9 | Bench gates: capture before/after per-dispatch CPU + allocations across 5 representative apps | Document in `docs/v0.17-roadmap/phase4-bench-results.md` |
| 10 | Doc + release: update CLAUDE.md ("Phase 4 closed"); CHANGELOG entry | Tag preparation only — no auto-tag per CLAUDE.md §3.5 |

Estimated total: **10 iterations**, ~3 sessions, ~80 commits.

## Risks + mitigations

### Risk R1 — Partial-application breakage

Sky lets users write `\arg -> Increment arg` and pass that as a `toMsg`.
The compiler today doesn't always statically reduce these to a clean
ctor reference. If Phase 4 routes those onto a registry path that
expects a clean ctor name, we silently miss the registration and fall
back to reflect — which is harmless BUT defeats the speedup.

**Mitigation**: Phase 4 keeps the reflect path as fallback. Coverage
becomes a quality metric (not a correctness metric). The compiler
emits a `__sky_dispatch_coverage_<App>` map at boot that tooling can
inspect to see which dispatch sites short-circuited and which didn't.

### Risk R2 — Cross-module ctor reference

Sky has `type Msg = Foo | Bar | Submodule.SubMsg`. Cross-module ctors
need their qualified name resolved through pkgAlias. Inspector's
`pkgAlias` registry (PR-9) supplies this — Phase 4 reuses it for the
dispatch table emission.

**Mitigation**: spec covered in iter 1 — `MsgDispatchSpec` includes a
multi-module fixture; the helper resolves the canonical qualified
ctor name via `pkgAlias`, not the literal Sky name.

### Risk R3 — Field-by-field typed ctor parameter accessors

Phase 4's typed-update-arm change 1 needs to call `Main_update_DoSignIn(p.(Main_AuthCreds_R), m)`
— but `p any` is the payload, not the original Msg struct. The dispatch
table needs a `SkyVariantPayload()` accessor on the SkyVariant interface
that exposes the typed V0/V1/V2 fields as `[]any`. We have
`SkyVariantTag()` + `SkyVariantName()`; we need the payload accessor.

**Mitigation**: add `SkyVariantPayload() []any` to the SkyVariant
interface (BC-safe — sealed iface, only codegen + runtime implement
it). Each `_V` variant struct returns its `V0`, `V1`, ... fields as
typed `any`. The dispatch arm narrows back with `.(Type)` per param —
type-asserted, not reflected.

### Risk R4 — Init ordering

Go's `init()` order is by file declaration order WITHIN a package, and
unspecified ACROSS packages. The runtime registries must be safe for
concurrent `init()` writes (we already use sync.RWMutex for the
existing adt registries — same pattern).

**Mitigation**: registries are sync.RWMutex-guarded; no read happens
before `main()` (which is after all `init()` blocks finish).

### Risk R5 — Source-map / trace span fidelity

`msgDisplayName` today returns the ctor name. Phase 4's dispatcher
calls into typed arms that don't carry the SkyName field — but the
SkyVariant interface still exposes `SkyVariantName()`, so trace spans
remain labelled correctly.

**Mitigation**: trace span instrumentation moves UP the call stack to
the new dispatch entry point (`dispatchMsgFast`), which reads
`sv.SkyVariantName()` at the top before delegating to the typed arm.
Same fidelity as today.

### Risk R6 — IORef discipline

Per CLAUDE.md §0 hard rule 3, the implementation must NOT introduce
load-bearing-but-pure module-level state. Three options for compile-
time state needed (per-module variant tables):

* **Option A** — `Sky.Build.MsgDispatch` builds the variant table as a
  pure function from `Sky.Type.Solve.SolvedTypes`. Threading is the
  existing `LowerCtx` reader. NO new IORef.
* **Option B** — extend `LowerCtx.SolvedTypes` with a precomputed
  `_perMsgVariants :: Map Qualified [(Tag, Ctor, [GoType])]` field.
  Reader still pure. PREFERRED.
* **Option C** — add a new IORef. FORBIDDEN per §0.

**Mitigation**: Option B is the architecturally-clean route. iter 1
implements it on the SolvedTypes already-existing field set; no new
IORef.

## Proposed lever (one-line summary)

**Compile-time-known Msg ADT → per-Msg typed dispatch table → registered
at init() → consulted by `sky_call` BEFORE the reflect path → reflect
stays as fallback for partial-application shapes.**

The "lever" name: `perMsgTypedDispatch`.

Switch position in the compiler: A new `Sky.Build.MsgDispatch` module
emits the table + decoders when LowerCtx.SolvedTypes shows an ADT used
as the type-arg of `Live.app cfg` / `Tui.app cfg` / `Webview.app cfg`
(detected via the `appMsgType` Inspector entry — already known per
v0.17 PR-9 `pkgAlias`-style registry pattern).

## What this does NOT close

* **adaptFuncValue / reflect.MakeFunc** — needs a separate codegen
  surface ("typed Coerce[func(...)]" — eliminate the FFI shim by
  emitting Go-shaped closures at every Coerce[func(...)] site). Out of
  scope here; tracked under `phase5-typed-ffi-shim.md` (future).
* **Generic `SkyCall` in HOFs (`map`/`filter`/`foldr`)** — these route
  through reflect because the HOF receives a `func(any) any` Sky lambda
  whose typed shape isn't known at HOF dispatch time. The typed-kernel
  variant (`Sky_Core_List_mapT[A, B]`) already exists per PR-22 — needs
  the lowerer to PICK the typed variant when LowerCtx knows both A and B.
  That's the rt.Coerce elision work (P5), not Phase 4.

## File layout

Implementation files (no new dirs):

| File | Status | What |
|---|---|---|
| `src/Sky/Build/MsgDispatch.hs` | NEW | Pure helper: SolvedTypes → variant table → emit specs |
| `src/Sky/Build/Compile.hs` | edit | Wire MsgDispatch.emit into the codegen pipeline at the same site as `init()` blocks for ADT tag registration |
| `runtime-go/rt/msg_dispatch.go` | NEW | Registries + lookup + sync.RWMutex + fast-path entries |
| `runtime-go/rt/live.go` | edit | `sky_call` consults `msg_dispatch.go` fast-path FIRST; reflect unchanged |
| `test/Sky/Build/MsgDispatchSpec.hs` | NEW | Pure unit specs over the helper |
| `test/Sky/Build/PerMsgUpdateSpec.hs` | NEW | Golden-file regression for emit output |
| `runtime-go/rt/msg_dispatch_test.go` | NEW | Register + lookup + concurrency tests |

## Decision gate before implementation

The Track A P5 Stage 4 work (wrap elision for SkyMaybe/SkyResult/[]T)
should complete BEFORE Phase 4 implementation begins. Reason: Phase 4
benchmarks against rt.Coerce floor — moving the floor mid-Phase-4
makes the speedup numbers noisy. Phase 4 starts when the rt.Coerce floor
is in the iter 13 range (172 / 124) AND P5 Stage 4 has either landed
OR been declared an explicit limitation.

This document captures the design; implementation is a separate track.
