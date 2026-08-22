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
| D0 — Architecture-Consult | 🔨 | map spa-counter client reflection surface + de-reflection mechanism + TinyGo constraints + phased plan |
| D1 — dispatch de-reflection | ⏳ | typed dispatch (perMsgTypedDispatch Stage 6 or equiv), client-only |
| D2 — spa-counter → TinyGo | ⏳ | real Sky-emitted counter compiles+renders under TinyGo; measure |
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
