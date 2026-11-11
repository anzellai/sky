# Generated typed Codec.auto codecs vs the reflective walker (2026-11-10)

What: the v0.29.9 generated codecs (`rust/crates/lower/src/lower/auto_codec.rs`,
`runtime-go/rt/codec_auto_gen.go`) against the reflective walker they replace,
on the large-model research fixture (a list of records, a Dict of records,
one large String) compiled as a Sky program (`app/`). "Reference" is the same
binary with `rt.AutoCodecUseReference(true)`: the walker plus the v0.29.6
single-pass JSON writer, which is the v0.29.8 behaviour. "Encode" is
`Spa_modelToJson` (`Codec.toJson (Codec.auto blank) m`), "decode" is
`Spa_modelFromJson` (parse plus build). Both modes are checked to give the
same text before timing.

How: `./run.sh results` (then `./summarise.sh results`), one process per
target, mode, shape and size, `-benchtime 2s -count 3`, median shown. Native:
Go 1.26.1 darwin/arm64, Apple M1. Wasm: the same test binary built for
js/wasm, run with Node (`wasm_exec_node.js`). The machine was under memory
pressure from other work (swap about 93% used), so treat single numbers as
indicative; the ratios held in every repeat.

| target | op | shape | MB | reference ms | generated ms | speed-up | reference allocs | generated allocs |
|---|---|---|---|---|---|---|---|---|
| native | encode | list | 1 | 6.79 | 1.08 | 6.3x | 67306 | 33 |
| native | encode | list | 3 | 20.32 | 3.27 | 6.2x | 202859 | 38 |
| native | encode | dict | 1 | 8.19 | 2.23 | 3.7x | 67338 | 38 |
| native | encode | dict | 3 | 25.16 | 7.16 | 3.5x | 202901 | 43 |
| native | encode | blob | 1 | 0.95 | 0.95 | 1.0x | 18 | 7 |
| native | encode | blob | 3 | 2.74 | 2.82 | 1.0x | 18 | 7 |
| native | decode | list | 1 | 20.95 | 9.47 | 2.2x | 250190 | 145825 |
| native | decode | list | 3 | 62.75 | 27.93 | 2.2x | 751469 | 437389 |
| native | decode | dict | 1 | 30.18 | 11.08 | 2.7x | 262637 | 153183 |
| native | decode | dict | 3 | 69.76 | 34.53 | 2.0x | 788174 | 458765 |
| native | decode | blob | 1 | 4.81 | 4.80 | 1.0x | 71 | 52 |
| native | decode | blob | 3 | 14.04 | 14.19 | 1.0x | 73 | 54 |
| wasm | encode | list | 1 | 46.97 | 9.06 | 5.2x | 94165 | 33 |
| wasm | encode | list | 3 | 138.17 | 22.04 | 6.3x | 283426 | 38 |
| wasm | encode | dict | 1 | 52.36 | 12.60 | 4.2x | 94197 | 38 |
| wasm | encode | dict | 3 | 159.47 | 37.17 | 4.3x | 283468 | 43 |
| wasm | encode | blob | 1 | 2.37 | 2.33 | 1.0x | 23 | 7 |
| wasm | encode | blob | 3 | 6.97 | 6.89 | 1.0x | 23 | 7 |
| wasm | decode | list | 1 | 113.24 | 57.04 | 2.0x | 334647 | 145865 |
| wasm | decode | list | 3 | 368.92 | 165.91 | 2.2x | 1004721 | 437429 |
| wasm | decode | dict | 1 | 127.22 | 62.03 | 2.1x | 347112 | 153197 |
| wasm | decode | dict | 3 | 404.90 | 202.42 | 2.0x | 1041445 | 458711 |
| wasm | decode | blob | 1 | 21.34 | 21.32 | 1.0x | 129 | 93 |
| wasm | decode | blob | 3 | 66.23 | 65.15 | 1.0x | 132 | 96 |

Targets from the plan: 1 MB Wasm list encode under 10 ms (9.06 ms; the
research measured 34.7 ms for the reference path on a quieter machine, 46.97 ms
here), boot decode at least 1.8 times faster (2.0 to 2.7 times, native and
Wasm). A blob is one String: the walker already writes it in one pass, so it
does not change. Decode still parses the whole document into a tree first
(`jsonParseDocument`, unchanged), which is now most of the decode time and
most of its allocations.

## Wasm size (gzip -9 of the client .wasm)

| client | v0.29.8 | v0.29.9 | change |
|---|---|---|---|
| `examples/60-spa-todos` client (`sky build --wasm`, no generated codec) | 2,889,025 | 2,893,622 | +4,597 (+0.16%) |
| `examples/61-app-kanban` (`--target web:app`, one generated codec for its Model) | 2,896,270 | 2,916,380 | +20,110 (+0.69%) |

`examples/19-skyforum` is a Sky.Live app, so it has no Wasm client.
`--target web:app` does not build it with either release (the split's
first-paint codec names an undefined `seedComments`).

Two size findings were fixed while measuring: `reflect.Type.MethodByName` in
the registration check made the linker keep every exported method (about
28 KB gzip on kanban; now `Implements`), and `errors.As` plus `fmt.Sprintf` in
the quota warning cost about 10 KB gzip (now a type assertion and strconv).
