# Sky.Spa de-reflection (prod web) — progress tracker

> Resume: read this + `.claude/AUTONOMOUS_GOAL.md`. Goal: de-reflect the Sky.Spa
> CLIENT core so a real Sky-emitted client compiles under TinyGo to a web-viable
> bundle, ISOLATED in a client runtime so Sky.Live's reflect path is untouched.
> Branch `exp/spa`. TinyGo at /opt/homebrew/bin (0.41.1).

## Measured baseline (surrogates, prod-web.md)
- reflection-free counter: Go 579KB gz / TinyGo 65KB gz
- reflection-free todos-scale: Go 600KB gz / TinyGo 103KB gz
- current reflect-heavy real client: ~2.4MB gz
=> target: real de-reflected client ~100KB gz under TinyGo.

## Phases
| Phase | State | Notes |
|---|---|---|
| D0 — Architecture-Consult | ✅ | map spa-counter client reflection surface + de-reflection mechanism + TinyGo constraints + phased plan |
| D1 — dispatch de-reflection | ✅ | typed-closure emission (rt.SpaFns) — the 4 dispatch sites, client-only |
| D2 — spa-counter → TinyGo | ✅ | real Sky-emitted counter compiles+renders under TinyGo; 1.59 MB raw / 521 KB gz |
| D3 — codec + ADT de-reflection | ⏳ | reflection-free client codec/adt for data-decoding (todos) |
| D4 — todos → TinyGo + e2e | ⏳ | real todos client TinyGo build + render + e2e; measure |
| D5 — Judge + isolation proof + full sweep | ⏳ | Sky.Live untouched; §0.2.1 green; DONE list |

## Decisions / findings
- (D0) isolation is a hard requirement: de-reflection in a `//go:build js`/spa
  client runtime; server reflect path byte-unchanged.

## D0 findings (consult, verified on-machine)
- **Isolation is the dominant blocker, NOT dispatch.** `tinygo build -target wasm`
  on the emitted counter fails FIRST on `net/http` (roundtrip_js.go gap) — an
  IMPORT typecheck failure (not reachability), via untagged `rt.go` importing
  net/http(:45)/os(:47)/os/exec(:48)/crypto/rsa(:31)/crypto/x509(:37) +
  database/sql (rt_core_shims_js.go). DCE can't save it. Client must stop
  importing the server stdlib.
- **Dispatch: typed-closure emission (confirmed), NOT Stage-6.** Codegen has the
  concrete types at the `Spa_config` site (`lower.rs:5901 lower_record`, all-`any`
  anon-struct branch ~5972/6079) → emit `rt.SpaFns{Init,Update,View,Subs}` adapters
  that call `Main_update(m.(Msg),md.(int))` directly + unpack `T2.V0/.V1` at
  concrete type → removes `reflect.Value.Call` (live_core.go:2425/2436/2516) + T2
  tuple reflect. Stage 6 is the server wire-decode problem the client lacks + still
  routes through reflect.Call — wrong lever.
- Counter's reflect surface = ONLY the 4 sky_call sites + T2. HtmlToVNode/adt_shape
  (SkyADT fast path) + codec_auto NOT reached by the counter — those are D3 (todos).
- Isolation options: (A) tag-split rt.go server funcs `//go:build !js` (no dup,
  extends the P1 live_core/rt_server carve, driven by the tinygo build oracle) vs
  (B) separate `rt_spa` client package (perfect isolation, duplication tax).
  Default A (server files stay, just tagged → non-js server build byte-unchanged);
  fall to B only if rt.go won't split cleanly.
- D-plan: D1 typed-closure dispatch → D2 isolation carve + counter TinyGo → D3
  codec/adt → D4 todos TinyGo+e2e.

## D1 findings (implemented)
- **Codegen** (`rust/crates/lower/src/lower.rs`, `kernel_call` hook +
  `try_lower_spa_fns`/`spa_adapter`): the `rt.Spa_config` kernel call now emits
  `rt.SpaFns{Init,Update,View,Subs}` of typed adapter closures
  (`func(a0 any, a1 any) rt.SkyTuple2 { p := Main_update(a0.(Main_Msg), a1.(int)); return rt.SkyTuple2{V0: p.V0, V1: p.V1} }`)
  instead of the all-`any` anon struct. Type assertions `.(T)`, no reflect.
  Gated on `rt.Spa_config` only ⇒ Sky.Live/Tui/Webview (`Live_config`, a
  different kernel) is byte-unchanged and still reflect-dispatches.
- **Runtime**: `rt.SpaFns` + `asSpaFns` (`spa_core.go`); `Spa_config` stores the
  SpaFns under "Fns"; the wasm driver (`live_wasm.go`
  spaRun/step/renderCurrent/reconcileSubs) invokes the closures directly, reads
  `.V0`/`.V1` — no `sky_call`/`sky_call2`/`tupleFirst` on the counter path.
- Verified: standard-Go `GOOS=js` build + `run_headless.cjs` pass
  (0→+1×3→3→Reset→0→−1); `cargo test -p sky/lower/codegen` + `go test ./rt/...`
  green.

## D2 findings (implemented — option A, tag-split, one `rt` package)
- **os + net/url COMPILE under TinyGo 0.41.1** (verified with a probe) ⇒ only
  net/http, os/exec, crypto/rsa, crypto/x509, encoding/pem were true blockers.
  Much smaller carve than feared.
- Split to `//go:build !js`: `rt_server_kernels.go` (Process_run, RSA/PKCS
  Crypto), `stdlib_http_server.go` (net/http outbound client); tagged
  console_inline / console_internal_token / email_kernel / email_mime `!js`;
  dropped net/http from rt_core_shims_js.go; inlined `http.TimeFormat` literal
  in rt.go. Client HTTP stays browser-`fetch` (http_wasm.go).
- **Task entry reflect**: `AnyTaskRun(TaskCoerceT[Error,()](Spa_app(cfg)))`
  hit `reflect.Type.NumIn()` (unimplemented in TinyGo → runtime panic). Fixed
  reflect-free with `SkyTask[E,A].RunAny()` + an interface assertion in
  `anyTaskInvoke` (portable; identical result to the old reflect fallback,
  faster on the server too).
- **Result**: `tinygo build -no-debug -opt=z -target wasm` on the REAL emitted
  spa-counter SUCCEEDS and RENDERS/runs the TEA loop headless on TinyGo's
  wasm_exec.js. **Bundle 1,623,321 B raw / 534,039 B gzip (1.59 MB / 521 KB).**
  Larger than the hand-written surrogate (191 KB / 65 KB) because the real
  client links the whole `rt` package (all pure kernels + reflect-based
  codec/ADT machinery DCE can't yet strip). Shrinking toward the surrogate is
  D3+ (codec/ADT de-reflection + DCE tuning).
- **Reflect status**: the 4 DISPATCH sites are reflect-free (proven by render).
  `reflect.Value.Call`/`MakeFunc` remain in js-compiled files
  (`sky_call`/`sky_call2` live_core.go, `pipelineApply`/`SkyCall`/MakeFunc
  rt.go) on COLD client paths the counter never executes (Cmd.perform,
  onNavigate, Sub.every timers, JSON-pipeline decode). TinyGo compiles them as
  panic-stubs; de-reflecting them (so a perform/router/timer Spa app also
  TinyGo-runs) is D3+.
- **Sky.Live untouched**: `go test ./rt/...` green; 09-live-counter +
  19-skyforum build + serve HTTP 200; standard-Go spa-counter + spa-input
  still render. `gates_measure_a_fresh_compiler` (21), project suite,
  `denominators --check`, `coverage-ledger --check` all PASS.

## D1+D2 DONE (@e825d428, pushed) + D3 plan (consult)
- D1: typed-closure SPA dispatch (lower.rs try_lower_spa_fns; rt.SpaFns) — server byte-unchanged.
- D2: TinyGo-clean client carve (option A tag-split; rt_server_kernels.go + stdlib_http_server.go). REAL spa-counter TinyGo-compiles+renders. Bundle 521 KB gz. Sky.Live 09/19 HTTP 200.
- D3 consult: hand-written codecs DON'T touch codec_auto (no codec rewrite). D3 = ONE mechanism ("apply a boxed Sky func value without reflect" = func(any)any wrap-at-emission + client driver `.(func(any)any)`), at dispatch cold-paths (perform/onInput/onNavigate/Sub.every/param-routes; live_wasm.go:430/431, dom_render_wasm.go:144) AND the codec applicative (JsonDec_map2→pipelineApply; stdlib_extra.go:1190).
- SIZE IS A CLIFF: TinyGo keeps reflect metadata until ZERO reachable reflect.Value.Call/MakeFunc. Need dispatch (D3a)+codec applicative (D3b)+HtmlToVNode unwrapADTShape ref removed + codec_auto/adt_shape/adaptFuncValue tagged !js (D3c). Measure at D3c.
- Phases: D3a cold-path dispatch · D3b codec applicative · D3c render+reflect isolation (size unlock, "zero reachable reflect" gate) · D3d(=D4) todos TinyGo + e2e + measure.

## D3 progress (coordinator, hands-on foreground)
- D3a DONE (@..): performTask + dispatchEvent reflection-free (typed asserts on
  SkyADT-aliased boundary: SkyTask[SkyADT,any], toMsg func(SkyResult[SkyADT,any])any,
  onInput func(string)any). Pure runtime, client-only, no codegen. Counter renders
  under TinyGo, no regression.
- D3b DONE (@ec930641): pipelineApply 2-arg fast-path (codec applicative
  func(func(any)any,any)any). Todos client TinyGo-COMPILES.
- D3c-partial (@..): ResultCoerce SkyResult[E,any] fast-path.
- **BLOCKER MAPPED (honest): full functional todos-under-TinyGo needs the any→typed
  COERCION MACHINERY de-reflected, not just dispatch/codec.** The runtime panic
  walks forward as each site is fixed: FieldByName (ResultCoerce ✓) → now
  (reflect.Type).ConvertibleTo() in coerceInner/Coerce/AsListT narrowing the
  decoded list to []Todo_R. Root cause: the client decode yields values that are
  then reflect-narrowed to typed structs/slices; the clean fix is either (a) make
  the client codec decode produce already-typed values (no post-decode coerce), or
  (b) client-only reflection-free variants of coerceInner/Coerce/AsListT/
  narrowReflectValue/mapToRecordStruct. Multi-site, iterative — a bounded-but-
  substantial remaining effort (the "reflection-free coercion" piece).
- STATE: dispatch + codec-applicative + result-coerce de-reflected + committed;
  spa-counter FULLY works under TinyGo (D1+D2, CI-green); todos COMPILES under
  TinyGo (644 KB gz, size cliff not yet tripped) but panics at runtime on the next
  coercion-reflect site. Size (zero-reflect DCE) awaits the coercion de-reflection.

## D3 DEFINITIVE finding — the coercion narrow needs CODEGEN, not runtime fast-paths
Chasing the runtime panic forward (FieldByName→ResultCoerce ✓ → ConvertibleTo→
coerceInner/Coerce) reaches a FUNDAMENTAL wall: `coerceInner[[]Todo_R]` /
`Coerce[[]Todo_R]` must narrow `[]any` (each element a BOXED Todo_R) into a typed
`[]Todo_R`. In the generic runtime helper the element type is erased, so the narrow
is inherently reflect (`ConvertibleTo`/`reflect.Convert`) — TinyGo-unimplemented.
Narrowing it reflection-free requires the STATIC element type, which exists only at
CODEGEN (the emit site knows `Todo_R`). So the emit must either (a) produce
already-typed decode results (no post-decode coerce), or (b) emit the narrow using
the known element type (e.g. `AsListT[Todo_R]` directly on an `A=any` result rather
than `coerceInner[[]Todo_R]`). This is the pervasive "reflection-free coercion"
codegen work (prod-web.md's large lever), NOT a bounded runtime patch.

### Achieved (committed, verified)
- **spa-counter FULLY works under TinyGo** (D1+D2, CI-green, 521 KB gz) — a real
  Sky-emitted client running client-side under TinyGo. SHIPPED.
- Dispatch (performTask/dispatchEvent), codec-applicative (pipelineApply), and
  ResultCoerce de-reflected (client-only/safe short-circuits); todos COMPILES
  under TinyGo (644 KB gz). Server byte-unchanged; `go build ./...`=0.

### Remaining for functional todos (record-app) under TinyGo + the size cliff
1. Codegen: emit the client's any→typed coercion reflection-free using the static
   element/field types (route slice narrows through `AsListT[Elem]` on `A=any`
   results; struct narrows via typed field assigns) — gated on the SPA client
   target so the server reflect coercion is unchanged.
2. Then D3c isolation (client-only reflection-free variants of the residual
   reflect refs: sky_call cold-fallbacks, unwrapADTShape in HtmlToVNode,
   adaptFuncValue) so the client graph reaches ZERO reflect → DCE trips the size
   cliff (644 KB → target ~100 KB).
This is a bounded-but-substantial codegen effort (multi-session), now precisely
scoped. Resume here.
