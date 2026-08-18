# Render-pipeline optimization — Html→VNode reflect de-boxing

Removes the single largest allocation site in the Sky.Live render pipeline:
`asList` reflect-boxing every attribute and child of every element inside
`HtmlToVNode` (`runtime-go/rt/live.go`). Output is **byte-identical** — a
runtime allocation/CPU change, not a behaviour change.

## What the profile said (HEAD, before)

Allocation attribution of the full first-paint render of `26-ui-showcase`
(384 sky-id elements), `Main_view → rt.HtmlRenderWithHandlers`, `-memprofile`
+ `pprof -alloc_objects` (`before-alloc-profile-showcase.txt`). Allocation is
the stable observable (reproduces to 0.0–0.2%); CPU self-time is quoted with
its spread.

| pass | allocs/op | % of render objects |
|---|---|---|
| **full pipeline** | 17,098 | 100% |
| Sky Element→Html (`Main_view`: renderElement / buildStyleStringWith / collectStyle / collectHtmlAttrs) | 12,517 | 73.2% |
| **Html→VNode (`HtmlToVNode`)** | 3,849 | 22.5% |
| renderVNode + assignSkyIDs + applyStyleInjections | ~732 | 4.3% |

Flat allocation sites, sorted — the #1 site was **`reflect.unsafe_New` at
23.6%** (11.06 M objects), and `asList` accounted for **22.24% cum** of all
objects:

```
11061458 23.62%  reflect.unsafe_New
 2142093  4.58%  rt.asList                         (make([]any, n))
 ...
        cum 22.24%  rt.asList  →  8.27M of the reflect.unsafe_New via rv.Index(i).Interface()
```

**Root cause.** `Std_Html_Attributes_Attribute` and `Std_Html_Html` are type
aliases for `rt.SkyADT`, so an `HElement`'s attribute and child lists arrive as
`[]SkyADT` boxed in `Fields []any`. The old `HtmlToVNode` routed both through
`asList`, whose typed-slice branch does `reflect.ValueOf(v)` +
`rv.Index(i).Interface()` per element — one `reflect.unsafe_New` heap box per
attribute and per child, plus a throwaway `[]any` per list.

## The change

`runtime-go/rt/live.go`. `HtmlToVNode` gains a `[]SkyADT` fast path
(`appendHtmlAttrs` / `appendHtmlChildren`) that iterates the typed slice
directly: no `[]any` copy, no per-element reflect box, and `Children` is
pre-sized (each child yields exactly one VNode). The `[]any` and reflect
branches are retained unchanged for the erased/mixed case (e.g. a `Raw`/VNode
passthrough child). Shared shape functions (`htmlShapeToVNode`,
`applyHtmlAttrShape`) keep the fast and slow paths behaviourally identical.

## The win (allocation, grounded; CPU secondary with spread)

### Html→VNode pass, ref-vs-new, one binary, at the re-baseline's view sizes

`BenchmarkHtmlToVNode_{Before,After}` in
`runtime-go/rt/html_to_vnode_diff_test.go` runs the frozen pre-change converter
(`htmlToVNodeRef`) and the new `HtmlToVNode` over a forum-shaped Html ADT
(~16 elements/post). 3 repeats, allocation identical to the object across all
(`pass-ref-vs-new.txt`):

| view | allocs before | allocs after | Δ allocs | bytes before | bytes after | Δ bytes |
|---|---|---|---|---|---|---|
| **101 vnodes (≈94 sky-id)** | 552 | 197 | **−64.3%** | 50,928 | 32,128 | −36.9% |
| **965 vnodes (≈974 sky-id)** | 5,253 | 1,871 | **−64.4%** | 489,728 | 308,016 | −37.1% |

CPU on this shared M1 moved −40% to −66% across runs (±37% swing — do not quote
a point value); the allocation figure is the stable one.

### Full first-paint interaction, same-binary controlled (26-ui-showcase, 384 el)

Rebuilt the app with only `runtime-go/rt/live.go` changed (compiler re-embeds
the runtime), same benchmark:

| pass | before allocs/op | after allocs/op | Δ |
|---|---|---|---|
| **full pipeline** | **17,098** | **14,163** | **−2,935 (−17.2%)** |
| Sky Element→Html (unchanged) | 12,517 | 12,517 | 0 |
| Html→VNode | 3,849 | 914 | −2,935 (−76.3%) |

Bytes 1,366,970 → 1,150,632 (−15.8%). The full-pipeline reduction equals the
Html→VNode reduction exactly — the Sky-side pass is untouched. The after profile
(`after-alloc-profile-showcase.txt`) confirms `asList` is gone from the render
path and `reflect.unsafe_New` halved (11.06M → 6.27M); the residue is
`reflect.Value.call` HOF dispatch **inside the Sky-side pass**, a different
origin (the next lever, below).

The share of the interaction the win removes tracks how attribute-heavy the view
is: `26-ui-showcase` (charts, many styled elements) carried 22.5% of its render
allocations in Html→VNode; the lighter `19-skyforum` signed-in view is
Sky-Element→Html-dominated (94% of its 7,815 render allocs/op), so the same pass
is a smaller slice there. The pass itself is −64% everywhere it runs, on every
first paint and full-replace patch.

## Byte-identical correctness gate

- `runtime-go/rt/html_to_vnode_diff_test.go` — `TestHtmlToVNodeDiff` renders a
  corpus (every Html/Attribute ADT shape, `[]SkyADT` **and** `[]any` slices, a
  VNode passthrough child, class/style multi-value joining, URL-scheme
  neutralisation, empty/nested) through the frozen reference and the live
  converter and asserts the VNode tree is `DeepEqual` **and** the rendered HTML
  is byte-identical. `TestHtmlToVNodeDiffGateIsFalsifiable` proves the gate
  bites. Mutation-proven: injecting a dropped-child bug into the live
  `appendHtmlChildren` turned `TestHtmlToVNodeDiff` red on 4 corpus cases.
- App-level oracle: the full first-paint HTML of `26-ui-showcase` (77,258 B)
  and `19-skyforum` logged-out (18,099 B) + signed-in (18,354 B), captured
  before the change and `diff`ed after the rebuild — **byte-identical, all
  three**.
- Full `go test ./rt/` (incl. the render goldens, `live_alloc_gate`,
  `live_render_deterministic`) green; `go test -race` on the render/live subset
  green. No emit change → `coerce-floor` unaffected.

## Next lever (identified, not taken here)

The Sky Element→Html pass (`Main_view`, 73% of render allocations) is the
remaining prize, and its flat sites are now the profile top: the
`collectStyle` accumulator-concat fold inside `buildStyleStringWith` (`.7.17`,
6.99% flat), `String_fromInt`, `toAttrAttribute.func1` boxing, and `List_cons`.
Closing those means editing `sky-stdlib/Std/Ui.sky` (compiled Sky) under the
same byte-identical constraint — a higher-risk change than this runtime-Go one.

## Conditions

| | |
|---|---|
| Host | Apple M1, 8 core, 16 GB, macOS 26.x — arm64 (shared machine) |
| Commit | `feat/config-perf-followup` @ `6c43720f` + this change |
| Go | 1.26.1 |
| Method | in-package `testing.B` `-benchmem` on the real compiled pipeline + ref-vs-new in one binary; allocation is the grounded metric, CPU quoted with spread |
