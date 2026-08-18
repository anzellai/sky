# Render-pipeline optimization — Std.Ui `renderNodeAs` append-on-empty

Cuts per-element allocations in the **Sky-side Element→Html pass**
(`sky-stdlib/Std/Ui.sky`), the largest remaining render allocation share
(73% of render objects per the HEAD re-baseline in
`render-opt-htmltovnode-20260818/`, after typed twins and the HtmlToVNode
fast path landed). Output is **byte-identical** — an allocation/CPU change,
not a behaviour change.

## What the profile said (HEAD, before)

`renderNodeAs` traverses each element's attribute list and builds its
attribute + child lists with `++`. `List.append` (`rt.List_appendT`) always
`make`s a fresh slice and copies both operands — even when one operand is
`[]`. The common element carries no extra attrs, no nearby children, and no
pseudo/transition/animation attrs, so several of those `++` are appends onto
`[]` that copy the other operand for nothing. In the HEAD flat profile
(`render-opt-htmltovnode-20260818/after-alloc-profile-showcase.txt`),
`rt.List_appendT` is a top-12 site at **3.67% of all render objects** and
`rt.List_cons` a further 2.92%.

Per common element, `renderNodeAs` paid:

| site | `++`-on-`[]` in the common case |
|---|---|
| `allAttrs = extraAttrs ++ attrs` | `extraAttrs` is `[]` for every non-root element (the recursive `renderElement renderCtx []`) → 1 wasted copy of `attrs` |
| `attrList = … :: (pseudoAttrs ++ transitionAttrs ++ animationAttrs ++ htmlAttrs)` | all three special lists empty → 3 wasted copies of `htmlAttrs` |
| `renderedChildren = renderedMain ++ renderedNearby` | no nearby children → 1 wasted copy of the whole main-child list |
| `collectTransitions` | builds `String.join ", " []` (boxes its args + allocates) + a second `List.any` scan on every element, transition or not |

## The change (`sky-stdlib/Std/Ui.sky`)

Four guards, each preserving the emitted bytes exactly:

- **`allAttrs`** — `if List.isEmpty extraAttrs then attrs else extraAttrs ++ attrs`. `[] ++ attrs == attrs`.
- **`attrList`** — when `pseudoAttrs`/`transitionAttrs`/`animationAttrs` are all empty, the tail is `htmlAttrs` directly. `[] ++ [] ++ [] ++ htmlAttrs == htmlAttrs`.
- **`renderedChildren`** — `if List.isEmpty renderedNearby then renderedMain else renderedMain ++ renderedNearby`. `xs ++ [] == xs`.
- **`collectTransitions`** — `case rules of [] -> ("", False); _ -> (String.join …, List.any …)`. `renderNodeAs` matches `("", _)` and discards the Bool when the string is empty, so the empty branch is byte-identical in the emitted HTML.

The 2nd `markerFlags propagatedAttrs` was **left in place**: `markerFlagStep`
lowers to a value-struct accumulator (`_u := v_1; _u.Row = true`), so it heap-
allocates ~nothing, and removing it would be a CPU-only change against a
deliberately-cautious comment.

Attribute *order* in `attrList` is **not** output-sensitive — `Std.Html`'s
renderer sorts an element's attributes alphabetically before emit (verified:
a reordered-`attrList` mutant produced byte-identical HTML). The guards
therefore preserve order trivially; the falsification below uses a
content-dropping mutant instead.

## The win (allocation, grounded; CPU secondary with spread)

Forum-shaped Element tree (`Main_forumView`, ~16 Std.Ui elements/post) at the
re-baseline's view sizes, rendered through the full Std.Ui pass
(`Ui.layout [] el |> Html.render`). `BenchmarkRenderForum` (`render_bench_test.go`),
3 repeats, one binary each for before/after (compiler re-embeds the stdlib);
allocation reproduces to the object across all repeats.

| view | allocs before | allocs after | Δ allocs | bytes before | bytes after | Δ bytes |
|---|---|---|---|---|---|---|
| **~94 sky-id el (6 posts)** | 3,526 | 3,174 | **−352 (−9.98%)** | 179,477 | 162,836 | −16,641 (−9.27%) |
| **~974 sky-id el (60 posts)** | 34,206 | 30,722 | **−3,484 (−10.19%)** | 1,779,609 | 1,615,015 | −164,594 (−9.25%) |

The saving is a per-element fixed cost, so it scales with element count
(−352 at 94 el, −3,484 at 974 el ≈ **3.6 allocs/element**). CPU on this shared
M1 moved −3.0% to −3.5% across these runs; treat it as secondary against the
±37% interaction-CPU swing the re-baseline documents — the allocation figure
is the stable one.

### Relating to the HEAD interaction

This is a **−10% reduction of the Element→Html pass** at both re-baseline view
sizes. The re-baseline established that pass as 73% of render objects, so this
is on the order of **~7% of total render objects per interaction** — an
estimate, because the harness isolates Element→Html (its `Html.render`
serialisation stands in for the downstream `HtmlToVNode`, which this change
does not touch). The absolute per-element saving (3.6 allocs/el) is the
measured, app-independent quantity; it lands on every element on every first
paint and every full-replace patch.

## Byte-identical correctness gate

- **Differential build diff (the primary gate).** A corpus harness
  (`harness-Main.sky`, 11 elements exercising empty/one/many-style, html
  attrs, containers, fill-propagation, nearby, pseudo, transition, animation,
  a kitchen-sink combining all) rendered through `Ui.layout [] el |>
  Html.render`, built with the pre-change compiler and the post-change
  compiler, diffed: **byte-identical, all 11 cases.**
- **Mutation-proven the gate bites.** Injecting a dropped-attribute bug
  (`attrList` empty-extras branch → `[]` instead of `htmlAttrs`) turned the
  diff red on the html-attrs and container cases (the `class` attribute
  vanished). A reordering mutant was correctly *not* flagged — the renderer
  sorts attributes — confirming the gate keys on content, not incidental order.
- **Committed regression.** `tests/Std/UiRenderConcatTest.sky` (5 cases) pins
  the exact HTML of all four guarded code paths; discovered by
  `scripts/sky-suites.sh` and counted in `SKY_SUITES_EXPECTED` (393 → 398).
- **Existing Std.Ui HTML suites** (`UiTransitionAnimationTest`,
  `UiPseudoClassTest`, `UiInputCheckboxTest`, `UiAspectGridTest`,
  `UiMediaQueryTest` — 81 assertions) all green, including
  `collectTransitions joins multiple AttrTransitions with ', '`.
- **`coerce-floor` unchanged** — 00-standard-libs 186, 19-skyforum 256,
  26-ui-showcase 365 (exact, no widening); adapter exact at 17.
- `go test ./rt/...` green; both examples build clean-slate.

## Conditions

| | |
|---|---|
| Host | Apple M1, 8 core, 16 GB, macOS 26.x — arm64 (shared machine) |
| Commit | `feat/config-perf-followup` @ `7fdd73e3` + this change |
| Go | 1.26.1 |
| Method | in-package `testing.B` `-benchmem` on the real compiled Std.Ui pass; forum-shaped tree at the re-baseline's 94/974-element view sizes; allocation is the grounded metric, CPU quoted with spread |
