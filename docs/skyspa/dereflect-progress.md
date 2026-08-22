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
