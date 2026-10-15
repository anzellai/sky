# Stream D: CAF initialiser outlining (branch perf/caf-outline)

Commit 29a89f46a on perf/caf-outline (base perf/build-speed 3feaa1c38).
Machine: 16 GB Apple Silicon Mac, go1.26.1. GOCACHE=$HOME/.sky/go-build.
base = sky-out/sky built at 3feaa1c38. new = sky-out/sky built from 29a89f46a.
Raw rows: results.tsv. Logs: logs-*, ex-*, fe-*.

## Cause, confirmed

`go build -gcflags='sky-app=-m=2'` on the coordinator's expA/b100 (m2-b100-before.txt):

- `can inline Shared_spaModelWireFields_ with cost 79` (budget 80). Closure
  bodies are not counted at their size, so a 200-field initialiser closure
  still fits.
- `inlining call to Shared_spaModelWireFields_`: 100 times.
- `(*LazyCaf[...]).Get` itself is NOT inlined (cost 91). Only the accessor is.

On the regenerated b100 with the base compiler (m2-base100-gen.txt) the same
class appears under another name: `Shared_save1ReqCodec`, 206 func literals,
inlined 100 times. audit.py weight: CAF class 3,132,523 vs other 483.

After the fix (m2-b100-after.txt): no CAF accessor holds a func literal, and
no `__cafInit` is ever inlined (it is only referenced by value). The only
inlinable function left that holds func literals is `Std_Codec_codecAndMap`
(9 literals, inlined once, weight 483).

## Generated Sky.Spa app (grill/genwhole.py on examples/62-app-notes)

Backend `go build` of package main (touch main.go, deps warm):

| N fields | compiler | -c=8 wall | -c=8 user | -c=8 peak RSS | -c=1 wall | -c=1 peak RSS | compiled funcs |
|---|---|---|---|---|---|---|---|
| 25 | base | 2.33 s | 5.7 s | 597 MB | 3.38 s | 597 MB | 5,079 |
| 25 | new | 1.99 s | 4.8 s | 595 MB | 2.81 s | 609 MB | 2,973 |
| 50 | base | 3.18 s | 10.2 s | 904 MB | 5.43 s | 610 MB | 10,979 |
| 50 | new | 2.33 s | 5.9 s | 599 MB | 3.43 s | 599 MB | 4,423 |
| 100 | base | 6.30 s | 24.3 s | 2,238 MB | 11.67 s | 1,306 MB | 30,273 |
| 100 | new | 2.80 s | 7.9 s | 639 MB | 4.40 s | 629 MB | 7,323 |
| 200 | base | 28.21 s | 99.6 s | 4,691 MB | 44.82 s | 4,118 MB | 98,873 |
| 200 | new | 4.62 s | 13.1 s | 1,111 MB | 7.00 s | 789 MB | 13,123 |

Growth per doubling of fields (compiled funcs): base 2.16x, 2.76x, 3.27x.
New 1.49x, 1.66x, 1.79x.

The coordinator's earlier b200 run read 62 s / 6.1 GB / 100,681 funcs on the
same class; this run read 28 s / 4.7 GB / 98,873 funcs for base (machine load
differs; the function count agrees within the generator difference).

Whole `sky build --target web:app` (one run each, Go cache warm for rt and deps):

| N | base wall | base user | base peak RSS | new wall | new user | new peak RSS |
|---|---|---|---|---|---|---|
| 50 | 41.9 s | 74.1 s | 1,336 MB | 39.7 s | 65.4 s | 1,316 MB |
| 100 | 66.2 s | 133.8 s | 2,215 MB | 62.1 s | 101.5 s | 2,410 MB |
| 200 | 149.7 s | 379.1 s | 4,617 MB | 115.1 s | 191.9 s | 4,095 MB |

(N=25 rows are not comparable: the new N=25 build ran after a probe build of
the same project, so its Go cache was warm.)

The whole build is now dominated by other stages. The wasm frontend compile is
unchanged by this fix (no CAF copying there):

| N | compiler | frontend go build wall | user | peak RSS | compiled funcs |
|---|---|---|---|---|---|
| 100 | base | 7.63 s | 25.7 s | 2,294 MB | 11,335 |
| 100 | new | 7.59 s | 26.2 s | 2,405 MB | 11,335 |
| 200 | base | 15.55 s | 50.3 s | 4,434 MB | 21,435 |
| 200 | new | 14.85 s | 51.8 s | 4,065 MB | 21,435 |

The frontend at 200 fields still peaks at about 4 GB. That is a separate cost
for another stream.

## Examples

Package main `go build` (touch main.go):

| example | compiler | -c=8 wall | -c=8 user | peak RSS | -c=1 wall | compiled funcs |
|---|---|---|---|---|---|---|
| 13-skyshop | base | 6.69 s | 15.3 s | 1,284 MB | 8.02 s | 5,269 |
| 13-skyshop | new | 4.61 s | 12.4 s | 1,278 MB | 6.87 s | 3,487 |
| 19-skyforum (Sky.Live) | base | 1.83 s | 4.2 s | 592 MB | 2.68 s | 1,485 |
| 19-skyforum (Sky.Live) | new | 1.86 s | 4.1 s | 585 MB | 2.67 s | 1,443 |

13-skyshop whole `sky build`: base 66.0 s, new 28.9 s. Not a fair pair: the
base run went first and paid cold FFI (Stripe SDK) package compiles that the
new run then reused. Use the package-main numbers above.

Audit (audit.py over -m=2): skyshop base had 4 CAF accessors copied with their
closures (`Main_appDef` 41 literals, `Lib_Products_listAllProducts`,
`Std_App_noDurableWiring`, `Lib_Cart_getAllOrders`); new has none. skyforum
base had 2 (`Main_appDef` 39 literals, `Std_App_noDurableWiring`); new has none.

## Tests (red = fix reverted, tree claimed with streamC/claim.sh)

| test | red | green |
|---|---|---|
| codegen/tests/caf_accessor_shape.rs | FAIL: accessor is `Main_table__caf.Get(func() int { ... })` | pass |
| sky/tests/caf_inline_scale_flow.rs (n=40 -> 80) | FAIL: 1,830 -> 6,790 funcs, 3.71x | pass: 190 -> 310, 1.63x |
| rt TestDemangleFrameKinds (new rows) | FAIL: `Main.table__cafInit` | pass |

Proof logs: proof-red.txt, proof-green.txt, t2-red.txt, t2-green.txt.

## Gates run (narrow)

- `cargo test -p lower -p codegen`: all pass.
- `cargo test --release -p sky --test go_compile_shape`: pass.
- `xtask build-run --golden` (CLI family + the 15 other golden examples):
  23 of 23 goldens match.
- `xtask build-run --run` for 09-live-counter, 19-skyforum, 26-ui-showcase,
  62-app-notes, 00-standard-libs: build 5/5, run 5/5, match 4/4.
- `xtask coerce-floor`: adapter 0 everywhere. My change moves no row. Four
  rows differ from the golden, and they differ identically with the fix
  reverted (cfbase run), so they come from the base branch:
  63-app-chat 427 -> 430, 60-spa-todos/client 260 -> 263,
  60-spa-todos/server 211 -> 214 (WIDENED), sky-bundled/console 292 -> 276
  (tightened). Four examples (03-tea-external and three others) did not emit
  for lack of `sky install` FFI surfaces on this machine.
- Console Go regenerated (scripts/regenerate-console.sh): 113 CAFs now use
  `__cafInit`; the package builds and vets.
