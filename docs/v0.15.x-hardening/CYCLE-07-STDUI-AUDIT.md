# Cycle 7 — Std.Ui correctness sweep (audit)

> **Read me first.** This is the PLAN doc for cycle 7, not a fix. It
> measures Std.Ui across ~108 distinct combinations of (outer
> primitive × length × inner primitive × cross-cutting attr) on
> HEAD-of-main (post-#402, pre-cycle-7), groups the failures into
> a small set of families, proposes architectural fixes per family,
> and orders the work into bundled patch releases. No `sky-stdlib/`
> or `runtime-go/rt/` source is touched here.

## Method

Two harnesses, both in the audit branch:

1. **In-page matrix rig** — `/tmp/cycle07-rig/`. A single Sky.Live
   app stacking 102 measurement cases, each in a 320 px localised
   "frame". The frame's explicit pixel height makes layout
   resolution definite; the matrix-target primitive sits inside,
   with a `data-probe`-tagged child. Playwright walks the page,
   reads `getComputedStyle` + `getBoundingClientRect`, oracle-
   classifies as PASS / FAIL / PARTIAL / OBSERVE per category.
2. **Z-fixtures** — `/tmp/cycle07-rig-z1`, `-z2`, `-z3`. Single-
   purpose Sky.Live apps that reproduce the user's reported broken
   cases on the layout-root path (no fixed-height ancestor). The
   in-page rig CAN'T reach these — the matrix frame is always
   pixel-sized, which masks the cross-axis bug — so the Z fixtures
   are the bug exhibit.

Source binary: `cabal install exe:sky` rebuilt from HEAD at
`dc5c2d6a`, copied to `/tmp/audit-sky-bin/sky`. Chromium via
Playwright @ project-local install. Viewport 1280×800 unless
noted. Measurements are reproducible by re-running
`/tmp/cycle07-rig/build-rig.mjs && /tmp/audit-sky-bin/sky build
src/Main.sky && node measure.mjs && node analyze.mjs`.

## §1. Behavioral matrix

**In-page matrix (frame-stable):** 102 rows across 14 groups.
ALL 102 pass under the strict oracle defined in
`/tmp/cycle07-rig/analyze.mjs`. The high pass rate is not the
story — see §1.2 + §2: the cases that exhibit the headline bug
have flex-grow-derived ancestor heights, which the rig's
explicit-px frame fundamentally CANNOT reproduce. The matrix is
useful as a wide regression-coverage net; the Z fixtures are
the bug exhibits.

Group breakdown:

| Group | Rows | PASS | FAIL | PARTIAL | What it covers |
|---|---|---|---|---|---|
| A-length | 42 | 42 | 0 | 0 | 7 outer primitives × 6 length specs (px / fill / fillPortion / shrink / minimum / maximum / vh+vw). Probe = pixel-sized child. |
| B-flex-chain | 8 | 8 | 0 | 0 | Issue-#63 family: parent-leaves-axis-open + child has fill; deep-3 nest; row-in-row; col-row mixed. All pass IN-FRAME because frame is px-sized. |
| C-sized | 5 | 5 | 0 | 0 | `Ui.button` / `Ui.input` / `Ui.form` / `Ui.link` with width fill + height fill. |
| D-input | 8 | 8 | 0 | 0 | `Input.text` / `multiline` / `checkbox` / `slider` / `radioRow` / `email` / `search` / `multiline + labelAbove` all with `[width fill, height fill]`. **All pass IN-FRAME** — same caveat as B. (The user's reopen on #63 is a Z-fixture, not in-rig.) |
| E-align | 7 | 7 | 0 | 0 | `centerX` / `centerY` / `alignRight` / `alignTop` on row vs column parents + single-child centerX. |
| F-padding | 5 | 5 | 0 | 0 | `padding` / `paddingXY 30 10` (X horizontal, Y vertical — v0.11.x breaking shape verified) / `paddingEach` / row+column `spacing 18/24`. |
| G-nearby | 6 | 6 | 0 | 0 | `above` / `below` / `onLeft` / `onRight` / `inFront` / `behind`. Outer has `position: relative`, nearby children are `position: absolute`. |
| H-overflow | 6 | 6 | 0 | 0 | `clip` (emits `hidden hidden`) / `clipX` `clipY` (emit `clip visible` per CSS spec rationale) / `scrollbars` / `scrollbarX` / `scrollbarY`. Documented inconsistency between `clip` and `clipX/Y` keyword choice is intentional (`overflow: hidden` promotes the off-axis to `auto`; `overflow: clip` does not — see `Std.Ui.clipX` comment line 1599). |
| I-pointer | 1 | 1 | 0 | 0 | `cursor: pointer`. |
| J-pseudo | 4 | 4 | 0 | 0 | `Background.hoverColor` (`:hover` wrapped in `@media (hover: hover)` ✓) / `Background.focusVisibleColor` / `Font.hoverColor` / `Border.activeColor`. Sky-id-scoped `<style data-sky-pc=…>` child confirmed. |
| K-breakpoint | 3 | 3 | 0 | 0 | `Ui.breakpoint Ui.mobile` (`@media (max-width: 767px)`) / `Ui.darkMode` (`prefers-color-scheme: dark`) / `Ui.mediaQuery "(min-resolution: 2dppx)"`. Scoped `<style data-sky-mq=…>` confirmed. |
| L-anim | 2 | 2 | 0 | 0 | `Transition.attribute` background-color / `Animation.attribute` fadeIn keyframes. Both emit sky-id-scoped style children with `@media (prefers-reduced-motion: no-preference)` gating. |
| M-aspect | 4 | 4 | 0 | 0 | `Ui.widescreen` (16/9) / `Ui.square` (1/1) / `Ui.grid + Ui.gridColumns 100` (`repeat(auto-fill, minmax(100px, 1fr))`) / `Grid.columns [fr 1, px 100, fr 1]`. |
| N-file | 1 | 1 | 0 | 0 | `Ui.input [Ui.fileMaxSize, Ui.fileMaxWidth, Ui.fileMaxHeight]` data-attrs survive to DOM. |
| **In-page** | **102** | **102** | **0** | **0** |

Full row-by-row table: `/tmp/cycle07-rig/in-page-matrix.md`
(checked into the rig dir, not into the Sky repo — the rig is
ephemeral by design).

### §1.1 Z-fixtures (layout-root, no fixed-height ancestor)

The Z fixtures REPRODUCE the user's reported breakage on HEAD.
These are the bug exhibits.

| ID | Scaffold | Expected | Measured | Result |
|---|---|---|---|---|
| `Z1-row-fill-in-layout` | `Ui.layout [] (Ui.row [Ui.height Ui.fill] [Ui.text …])` | row fills viewport 768 px after body margin | row = 800 px ✓ | **PASS** — the user's report ("row ~22 px") does NOT reproduce on this exact shape at HEAD. Row at the layout root is a single flex child of the wrapper (column dir, min-height 100vh) — `flex-grow:1` resolves cleanly there. |
| `Z2-input-multiline-in-row-fill` | `Ui.layout [] (Ui.row [width fill, height fill, padding 16] [Input.multiline [width fill, height fill] cfg])` | textarea fills viewport minus padding ≈ 768 px | textarea = **51 px** | **FAIL** — Input.multiline's `wrapWithLabel` Ui.el wrapper is sized 51 px tall; textarea inside is 51 px. The row is correctly 800 px tall (flex-grow:1 in column-direction `r` wrapper). |
| `Z3-three-pane-app-shell` | classic header + sidebar + main app shell, `Ui.layout [] (Ui.column [w fill, h fill] [header (60 px), Ui.row [w fill, h fill] [sidebar (px 200, h fill), main (w fill, h fill) [content (w fill, h fill)]]])` | sidebar / main / content each fill the row's 740 px cross-axis | sidebar = **22 px**, main = **22 px**, content = **22 px** | **FAIL** — every child of the row that asks for `height fill` collapses to its text-content height. This is the user's "~22 px" report, located. |

**Live-fix probe (Z3):** stripping the inline `height: 100%` from
sidebar / main / content (leaving the existing `align-self:
stretch`) recovered 740 px on all three. The `height: 100%` rule
is actively HARMFUL when the row's height was itself derived
from `flex-grow: 1` in its own flex parent — CSS resolves `100%`
against the parent's USED size only when that size is "definite"
per the flex-layout spec; a flex-grow-derived height is
indefinite for the purpose of `%` resolution on cross-axis
children. See live probe output:
`/tmp/cycle07-rig/probe-z3.mjs` — before-after delta confirms
`align-self: stretch` alone is sufficient (and correct) for
cross-axis fill.

**Live-fix probe (Z2):** adding `flex-grow: 1; min-height: 0`
to the wrapWithLabel wrapper (and removing `height: 100%`)
recovered 768 px. Same root cause as Z3.

### §1.2 Confidence calibration

| Question | Status |
|---|---|
| **Did the rig find the user-reported bug?** | No — the rig's px-sized frame breaks the indefinite-parent precondition. The Z fixtures are the real exhibits. |
| **How representative is the rig of real apps?** | Real apps run with layout-root → flex-grow-derived inner heights (the Z-shape), not with explicit-px frames. The rig is a useful regression net for *every other* class of attr, but it falls short on the headline class. The next iteration of the rig should include a "naked frame" sibling (frame uses `Ui.height Ui.fill` itself) so cross-axis fill cascades face the real flex-derived heights. |
| **Coverage gaps known and accepted:** | (a) **No keyboard / mouse interactions** — pseudo-class :hover styling exists but I only verify emission, not interaction state. (b) **No viewport-resize verification** — `Ui.breakpoint Ui.mobile` triggers at < 768 px; the rig only checks the rules-string, not breakpoint-crossing behaviour. (c) **No Sky.Tui / Sky.Webview parity check** — the audit is web-only. Std.Ui ships three backends; cross-backend behaviour for the bug families is unmeasured here and is a §3 risk. (d) **`Ui.Lazy`, `Ui.Keyed`, `Std.Ui.Responsive` not exercised** — those affect diff identity / model-driven branching, orthogonal to layout. (e) **`Ui.html` (Raw escape hatch) not exercised** — bypasses Ui's render path by design. (f) **No `Border.*` cross-product** — Border.color / width / style / rounded / shadow / glow / innerShadow + per-side widths only spot-checked. (g) **`Ui.layout` rootAttrs not exercised separately** — they merge into the root's render context, not the wrapper's. The wrapper's hard-coded `min-height: 100vh; display: flex; flex-direction: column;` is a load-bearing assumption tracked under F1 fix. |

---

## §2. Root-cause taxonomy

Five families — each is a single architectural defect with
multiple symptoms, not a per-symptom patch.

### F1 — Cross-axis fill emits `height: 100%` / `width: 100%`, harmful when parent dimension is flex-grow-derived

**Symptom set.**
- Z2 — textarea inside row-of-fill collapses to 51 px.
- Z3 — sidebar / main / content in classic three-pane app shell
  collapse to 22 px each.
- (Any real Sky.Live app where a `Ui.row` is a flex child of a
  column ancestor AND that row's children ask for `Ui.height
  Ui.fill`.) The in-page rig HIDES this because the rig frame is
  explicitly `Ui.height (Ui.px 320)` — a definite ancestor height.
- (Symmetric case for `Ui.column` with `Ui.width Ui.fill`
  children when the column itself is flex-grow-sized: same bug
  on the width axis; not separately reproduced but mechanically
  identical.)

**Root cause.** `widthFillFor` / `heightFillFor` in
`sky-stdlib/Std/Ui.sky:2865-2930` emit `align-self: stretch;
width: 100%` (or `height: 100%`) for the cross-axis case. The
`align-self: stretch` part is sufficient and correct; the
`width: 100%` / `height: 100%` is REDUNDANT in standard flex
(`stretch` is the default `align-items`) and ACTIVELY HARMFUL
when the parent's cross-axis size was itself flex-grow-derived.
CSS Flexbox §9.8 ("Definite and Indefinite Sizes"): a flex
item's cross size is definite when the flex container's cross
size is definite; a flex container's main/cross size is
indefinite when it depends on flex-grow of its parent and the
parent's main/cross is also flex-grow-derived. Resolving
`height: 100%` against an indefinite parent falls back to
auto/content, defeating the intended stretch.

The `align-self: stretch` line alone produces correct cross-axis
fill in every standards-compliant browser. The explicit `width:
100%` / `height: 100%` was added as belt-and-braces in earlier
fix cycles (search `widthFillFor` git log) without anyone
realising the BRACES break.

**Why this family is distinct from F2/F3/F4/F5.** F1 is purely
CSS emission. The propagation pass (F2) addresses a DIFFERENT
problem (parent left axis OPEN; this family has parent's axis
SET to fill via a flex-grow chain). F1 stays inside `widthCssIn`
/ `heightCssIn`.

### F2 — One-level propagation insufficient when intermediate wrappers are introduced after the propagation pass

**Symptom set.**
- Z2 — even with F1 fixed, the wrapWithLabel wrapper still needs
  to carry the height attribute. The propagation in
  `propagateFillToContainer` (Ui.sky:1943) runs on
  user-constructed parents; if a user calls
  `Input.multiline [Ui.height Ui.fill] cfg`, the layout attrs
  end up on the INNER `<textarea>`, not on the wrapWithLabel
  Ui.el wrapper — so when render hits the wrapper, its
  `propagateFillToContainer` looks at its child (the textarea)
  and sees fill, synthesises a fill attr — but if its OWN
  parent (the user's row) doesn't propagate further, the chain
  ends one level too short.
- (Any `Input.*` primitive that's nested 2+ levels deep inside
  another container with implicit sizing.)
- (Any user-defined wrapper helper that emits an `Ui.el` between
  user attrs and the actual measured child.)

**Root cause.** Two coupled defects:
1. `Std.Ui.Input.*` routes user layout attrs to the inner form
   control, not to the wrapWithLabel `Ui.el` wrapper. Branch
   `fix/403-input-wrapwithlabel-attrs-split` already prototypes
   `splitLayoutAttrs` to hoist layout attrs to the wrapper —
   that's the right idea but mid-branch and not on main.
2. The propagation in Ui.sky:1943 is structurally a SINGLE-LEVEL
   pass per render (the comment claims "recursion isn't needed
   because the bottom container ultimately propagates a real
   AttrHeight"). That argument relies on the in-line user's
   wrapping pattern; for Input.* family the user's attrs and the
   internal wrapper are NOT in the same render call — they're
   in two different `renderNodeAs` invocations, and the
   propagation pass at each level only sees its own children.

**Why this family is distinct.** F1 is the wrong CSS at the leaf;
F2 is the WRONG ATTR DISTRIBUTION (and missing implicit pass-
through) at the intermediate wrapper. Either fix alone is
insufficient. The `splitLayoutAttrs` branch fixes 2.1 cleanly;
F1 then becomes the residual.

### F3 — Layout root wrapper hard-codes `min-height: 100vh; display: flex; flex-direction: column;` and ignores user-supplied root attrs for the wrapper itself

**Symptom set.**
- A user writing `Ui.layout [Background.color (Ui.rgb 18 18 24)]
  (...)` to dark-theme the page expects the wrapper itself (the
  page-tall surface) to take the background color. Today those
  attrs go to the ROOT child, not the wrapper, so the area
  outside the root (e.g. when content is shorter than 100 vh)
  shows bare `<body>`.
- A user wanting a row-direction root (LTR app shell on
  desktop, column on mobile via breakpoint) cannot — the
  wrapper hardcodes `flex-direction: column;` regardless of the
  root's own layout marker. So `Ui.layout [] (Ui.row [...])`
  forces an extra flex-column wrap that disagrees with the
  row's main-axis.
- (Confirmed in the rendered HTML of every example I inspected;
  every Sky.Live app today wraps a `Ui.column` so the mismatch
  is hidden by convention. But it's a real expressivity ceiling.)

**Root cause.** `layout` at Ui.sky:1676 inlines the
`min-height: 100vh; display: flex; flex-direction: column;`
attribute on a `Html.div`, ignoring `rootAttrs`. The intent (per
the inline comment) is to give the root a "real 100vh floor".
The implementation is correct for that goal but builds in a hard
column-direction assumption.

**Why distinct.** F1/F2 are inside Std.Ui's flex-chain pass; F3
is the LAYER above (the Std.Html wrapper). Different scope of
edit, different risk profile.

### F4 — Double `align-self` emission: `width fill` + `centerX` produces conflicting `align-self: stretch; align-self: center` (cascade-order-dependent)

**Symptom set.**
- E-col-centerX rendered HTML carries both `align-self: stretch`
  (from `Ui.width Ui.fill`) and `align-self: center` (from
  `Ui.centerX`). The later one wins per CSS cascade order, so
  `centerX` overrides the stretch — but `width: 100%` (also
  emitted by `width fill`) still forces full width, so the
  visible result is correct. The CSS is wrong-by-luck.
- More broadly: `alignSelfX` / `alignSelfY` at Ui.sky:3031+
  ALWAYS emit `align-self: <h>` regardless of whether the
  element also has a cross-axis fill. Mixing alignment + fill
  produces stylesheet noise that future cascade-aware fixes
  (e.g. F1 cleanup) might unmask as a real bug.
- (No live failure today; this is a robustness / code-smell
  finding that becomes a risk when F1 lands.)

**Root cause.** `widthFillFor`/`heightFillFor` AND `alignSelfX`/
`alignSelfY` independently emit `align-self`. The two passes
don't reconcile.

**Why distinct.** This is a CONFLICT between two separately-
correct subsystems. The fix is reconciliation (one or the other
emits `align-self`, not both); not a fix to either subsystem in
isolation.

### F5 — Cross-axis `Ui.spacing` + `wrappedRow` `flex-wrap` interaction not exercised end-to-end (LOW confidence)

**Symptom set.** None observed in the rig. The rig's `wrappedRow`
cases all use px-sized children, so wrap behaviour at the page
boundary isn't exercised. The Std.Html.render path treats
`flex-wrap: wrap;` correctly, but in interaction with
`spacing N` (`gap: Npx`) some browsers historically have had
gap-with-wrap row-spacing edge cases. UNVERIFIED — flagged for
explicit coverage in the next rig iteration.

**Root cause.** Unknown — speculation only.

**Why distinct.** Listed as F5 to surface the coverage gap, not
because the bug is confirmed. Drop it if §4 verification clears
it.

---

## §3. Architectural fix per family

### F1 fix — emit `align-self: stretch` ONLY for cross-axis fill

**`sky-stdlib/Std/Ui.sky` changes.**

```elm
widthFillFor : LayoutContext -> Int -> String
widthFillFor parentCtx n =
    case parentCtx of
        AsRow ->
            "flex-grow: " ++ String.fromInt n ++ "; min-width: 0;"
        -- Cross-axis: align-self: stretch is sufficient + correct
        -- across browsers regardless of parent's height
        -- definiteness. Dropping the explicit width: 100% closes
        -- the cross-axis-collapses-under-flex-grow bug (F1).
        AsColumn ->
            "align-self: stretch;"
        AsEl ->
            "align-self: stretch;"
        AsParagraph ->
            "width: 100%;"  -- block element; align-self irrelevant
        AsTextColumn ->
            "align-self: stretch;"


heightFillFor : LayoutContext -> Int -> String
heightFillFor parentCtx n =
    case parentCtx of
        AsRow ->
            "align-self: stretch;"  -- was: align-self: stretch; height: 100%
        AsColumn ->
            "flex-grow: " ++ String.fromInt n ++ "; min-height: 0;"
        AsEl ->
            "flex-grow: " ++ String.fromInt n ++ "; min-height: 0;"
        AsParagraph ->
            "height: 100%;"  -- block, no flex; bare height OK
        AsTextColumn ->
            "flex-grow: " ++ String.fromInt n ++ "; min-height: 0;"
```

**`runtime-go/rt/*.go` changes.** None.

**Invariants established.**
- A cross-axis fill child carries ONLY `align-self: stretch` —
  the spec-correct minimum for cross-axis stretching.
- A main-axis fill child carries `flex-grow: N; min-{w,h}: 0`
  — the spec-correct flex-grow form.
- No element carries both `align-self: stretch` AND `height|
  width: 100%` simultaneously.

**Risk.**
- **Backwards compat.** Apps that already render correctly under
  the current double-emission won't change — `align-self:
  stretch` alone is a superset of the working cases. The cases
  this UN-BREAKS (Z2 / Z3) are currently observably broken, so
  there's no user-visible regression surface.
- **Performance.** Marginally faster (shorter style strings;
  fewer style declarations per element).
- **Tui / Webview parity.** Sky.Tui's renderer reads layout
  attrs directly from the Sky-side ADT, not from emitted CSS, so
  this change is web-only. Sky.Webview shares the web renderer
  (Sky.Live's `renderVNode`) so parity is automatic.
- **One existing snapshot test in
  `runtime-go/rt/live_pseudo_class_test.go` or similar** may
  assert exact style strings — needs a one-pass update.

### F2 fix — split Input.* attrs + recursive propagation for synthetic wrappers

**`sky-stdlib/Std/Ui/Input.sky` changes.** Adopt the
`fix/403-input-wrapwithlabel-attrs-split` branch's
`splitLayoutAttrs` helper. Apply uniformly to `text` /
`multiline` / `email` / `username` / `search` /
`currentPassword` / `newPassword` / `slider` / `checkbox` /
`radio` / `radioRow`. Layout attrs (`AttrWidth` / `AttrHeight` /
`AttrAlignX` / `AttrAlignY` / `AttrPadding` / `AttrSpacing` /
`AttrNearby` / `AttrPointer` / `AttrOverflow`) hoist to the
wrapWithLabel wrapper. Form / event / visual-style attrs stay
on the inner form control. When at least one layout attr is
hoisted, the inner control gains implicit `Ui.width Ui.fill +
Ui.height Ui.fill` so the cascade flows through.

**`sky-stdlib/Std/Ui.sky` changes.** `propagateFillToContainer`
at line 1943 needs to handle the wrapWithLabel intermediate
case. Cleanest fix: the `splitLayoutAttrs` route makes F2.1 a
non-issue; F2.2 then disappears too because the wrapper now
carries the user's `Ui.height Ui.fill` directly.

**`runtime-go/rt/*.go` changes.** None.

**Invariants established.**
- Every `Input.*` primitive treats `Ui.width Ui.fill / Ui.height
  Ui.fill` on its attr list as if applied to the outer wrapper
  (consistent with `Ui.button`'s direct mapping).
- The inner form control always grows to fill the wrapper when
  the wrapper has layout attrs hoisted.

**Risk.**
- **Backwards compat.** The shape `splitLayoutAttrs` was
  prototyped on a branch and passed the cabal spec
  `InputAttrsSplitSpec.hs`. Apps that today wrote `Ui.width
  Ui.fill` on `Input.text` and saw it apply to the `<input>`
  control will still see fill behaviour, because the implicit
  fill-on-control is added when layout attrs were hoisted. Apps
  that used Ui.width on the inner control to make it WIDER than
  the wrapper would observe a behaviour change — there are no
  such call sites in the example sweep.
- The cabal spec already exists. Cherry-pick + rebase onto F1's
  base.
- **Tui / Webview parity.** Sky.Tui's Input renderer reads
  width/height attrs directly off the wrapper element (the
  textarea control is a child); after the split, the wrapper
  carries the attrs, so Tui sees them. Verify in Sky.Tui smoke.

### F3 fix — make `Ui.layout` honour rootAttrs on the wrapper itself + allow row-direction root

**`sky-stdlib/Std/Ui.sky` changes.**

```elm
layout : List (Attribute msg) -> Element msg -> any
layout rootAttrs root =
    let
        -- Default attrs that establish the viewport-tall flex
        -- floor.  User-supplied rootAttrs OVERRIDE defaults
        -- (later in the list wins via collectStyle's foldl).
        defaultAttrs =
            [ AttrStyle "min-height" "100vh"
            -- direction picked from the root's marker;
            -- defaults to column (legacy behaviour).
            ]
        rootCtx = layoutContextFor (rootMarkers root)  -- pick AsRow if root is row, else AsColumn
        wrapperDir =
            case rootCtx of
                AsRow -> "row"
                _     -> "column"
        wrapperStyle =
            "min-height: 100vh; display: flex; flex-direction: "
                ++ wrapperDir ++ ";"
                ++ extraStylesFrom rootAttrs   -- e.g. Background.color routes to wrapper bg
    in
        Html.div [ Attr.style wrapperStyle ] [ styleNode, renderElement rootCtx [] root ]
```

The implementation needs a helper `rootMarkers` that extracts
the root's own attribute list to detect a row marker. Cleanest
implementation: pattern-match on `root` directly (`Node _ attrs
_` / `TaggedNode _ _ attrs _`). Routing user-supplied
Background / Border / Font attrs onto the wrapper while keeping
layout attrs (`AttrWidth` / `AttrHeight` / `AttrPadding`) on the
root is the right split — same shape as the F2 layout/visual
partition.

**`runtime-go/rt/*.go` changes.** None.

**Invariants established.**
- `Ui.layout [Background.color (Ui.rgb 18 18 24)] (...)` makes
  the WRAPPER (the 100 vh floor) carry the dark background, not
  the root.
- `Ui.layout [] (Ui.row [Ui.height Ui.fill] [...])` renders with
  a row-direction wrapper, so the row's `height fill` resolves
  correctly without flex-axis mismatch.
- Layout attrs (`Ui.width` / `Ui.height` / `Ui.padding`) on the
  rootAttrs list continue to apply to the root child element,
  preserving the v0.15.x semantics.

**Risk.**
- **Backwards compat.** The wrapper direction was historically
  always column; row-rooted apps don't exist today. The visual-
  attrs hoist to wrapper is BRAND NEW. The old behaviour was to
  do nothing with rootAttrs at the wrapper level. Apps that
  passed `Ui.layout []` will be byte-identical. Apps that passed
  non-empty rootAttrs and were relying on attrs going to the
  root are very few — `examples/26-ui-showcase` is the only
  example using non-empty rootAttrs and explicitly wants the
  root (column) to receive `Background.color`. F3 needs to be
  CAREFUL about who gets what: visual attrs to wrapper +
  layout/sizing attrs to root, both reached via partition.

  Simpler alternative for v0.15.55: split into TWO entry points
  — `Ui.layout` (legacy, attrs to root, column-direction wrap)
  + `Ui.layoutWith { wrapperAttrs, rootAttrs }` (record-shaped,
  full control). Defer the breaking change until v0.16.
- **Tui / Webview parity.** Tui's layout entrypoint is
  separate (`Tui.app`'s render pipe); doesn't touch this. Webview
  shares Sky.Live's renderer, so updated wrapper styling carries.
- Performance: zero. Same DOM count, slightly different
  style-attr selection.

### F4 fix — reconcile align-self emission between fill and alignment passes

**`sky-stdlib/Std/Ui.sky` changes.** `collectStyle` currently
foldl-merges attribute CSS. The fix is to track per-element
whether `align-self` has already been emitted by a previous attr
and SUPPRESS subsequent emission, OR (cleaner) have the fill
path NOT emit `align-self: stretch` when an alignment attr is
also present, OR (cleanest) have `align-self` set ONCE at a
late post-pass.

Recommended: a late post-pass — collect all attrs in a fold,
then derive a single `align-self` value at the end with the
priority: explicit alignment ATTR > fill-implied stretch.
Implementation: add a final-pass function `resolveAlignSelf`
called after `collectStyle` returns.

**`runtime-go/rt/*.go` changes.** None.

**Invariants established.**
- Exactly one `align-self` declaration per element.
- Explicit `Ui.centerX` / `alignLeft` / `alignRight` /
  `alignTop` / `alignBottom` always wins over implicit fill-
  stretch.
- The fix is invisible when the explicit alignment matches the
  fill default (`stretch`).

**Risk.**
- **Backwards compat.** Today the cascade-last wins by accident;
  this makes the precedence explicit. The visible result of
  `[width fill, centerX]` already resolves to centerX-wins +
  width:100% — F4 keeps that exact rendered result. Apps that
  relied on "fill stretches across the row regardless of
  centerX" would change, but no such app exists in the example
  sweep, and the documented semantics are alignment-wins.
- **Performance.** Marginally faster.
- **Tui parity.** Tui's renderer reads alignment + fill
  independently; no impact.

### F5 fix — defer until reproduced

If verification (see §4) doesn't surface a real failure on
`wrappedRow` + `spacing` + multi-row wrap, drop F5 entirely.
If it does surface a bug, the fix is likely in the `flex-wrap`
+ `gap` interaction, which is purely CSS — a small
`buildStyleString` adjustment.

---

## §4. Plan + bundling

Order of operations + dependencies:

```
  F1 (CSS emission, leaf)
    │
    ├──> F2 (Input.* attrs + recursive propagation)
    │       depends on F1 — the implicit `Ui.width Ui.fill +
    │       Ui.height Ui.fill` injected by splitLayoutAttrs
    │       relies on F1's correct cross-axis emission to
    │       actually cascade.
    │
    └──> F4 (align-self reconciliation)
            depends on F1 — needs F1's reduced emission to
            avoid double-write in the same pass.

  F3 (layout wrapper rewrite)
            independent — no flex-chain coupling. Can ship
            before/after F1 either way. Higher risk-of-
            backwards-incompat though; defer to next minor.

  F5 (wrappedRow + spacing — UNVERIFIED)
            independent — ship if reproduced; drop otherwise.
```

**Bundling recommendation.**

| Patch | Includes | Risk | Effort |
|---|---|---|---|
| **v0.15.55** | F1 + F2. Closes the user's reopened #63 + the user's #403 follow-up canonical case in one go. Both fixes are tested-in-isolation green; F1 backed by the live-fix probe data (Z2 / Z3 recover to expected dimensions on `height: 100%` removal); F2 backed by the existing `fix/403-input-wrapwithlabel-attrs-split` branch's `InputAttrsSplitSpec.hs`. Add 3 new regression scripts (`scripts/verify-cycle07-{cross-axis,input-fill,app-shell}.mjs`) covering Z1 / Z2 / Z3. | LOW. F1 strict CSS subset; F2 already prototyped + spec'd. | 1 dev-day for F1; F2 mostly a cherry-pick + extend + spec sweep ≈ 1 dev-day. |
| **v0.15.56** | F4. Cleanup of double `align-self` emission. Stand-alone; ship after F1 is stable to avoid bundling cascade-order changes with semantic changes. | LOW. No user-visible change. Code-hygiene grade. | 0.5 dev-day. |
| **v0.15.57 OR v0.16.0-pre** | F3. Visual attrs hoist to wrapper + row-direction root support. Recommend the **`Ui.layoutWith { wrapperAttrs, rootAttrs }` SECOND entry-point** route — leaves `Ui.layout` byte-identical, lets users opt in. Less risk than a breaking redirect of rootAttrs semantics. | MEDIUM. User-visible API surface change (additive). | 1.5 dev-day. |

**Regression coverage built per family.**

Every matrix row with a clear PASS/FAIL signal gains an assertion
in EITHER a Playwright script (when the oracle is a DOM measurement)
OR a cabal spec (when the oracle is the emitted Sky-side CSS
string). Mapping:

| Family | Test type | New file(s) |
|---|---|---|
| F1 | Playwright (computed-style) | `scripts/verify-cycle07-cross-axis.mjs` — drives the Z2/Z3 fixtures + 3 derivatives. Cabal: extend `test/Sky/Build/Stdlib/UiFillCssSpec.hs` (or create it) to assert `heightFillFor AsRow` emits `align-self: stretch` only. |
| F2 | Cabal spec | `test/Sky/Build/InputAttrsSplitSpec.hs` (already exists on the fix branch — adopt it). Playwright: `scripts/verify-cycle07-input-fill.mjs`. |
| F3 | Playwright | `scripts/verify-cycle07-layout-rootattrs.mjs`. Cabal: stylesnap on the wrapper's emitted style string for a known rootAttrs input. |
| F4 | Cabal | `test/Sky/Build/Stdlib/UiAlignSelfSpec.hs` asserts only ONE `align-self` declaration in `collectStyle`'s output for a representative shape. |
| F5 | (only if reproduced) | TBD. |

**Sweep gates.**

The 26-example sweep + `scripts/verify-issue-63.mjs` +
`scripts/verify-ui-showcase.sh` must stay green at every step.
For F1: re-run the showcase sweep — every `Background.color`/
`Border.*` snapshot pixel-identical, every existing layout
identical, only the inline-style strings change. For F2: the 4
existing cabal specs on the branch (3 attribute-routing cases +
1 no-leak) plus the 26-ui-showcase's new input-multiline card
already verify the corner. For F3: add a new `examples/26-ui-
showcase` section "layout-wrapper-styling" to lock in the
expected visual.

**Out-of-scope for cycle 7.**
- Sky.Tui layout-attr propagation — likely affected by F2's
  attr hoist (Tui reads attrs off the wrapper now, fine — but
  worth a smoke).
- Sky.Webview — shares Sky.Live's renderer, so automatically
  fixed.
- `Std.Ui.Responsive.classifyDevice` — typed-Msg adapter,
  orthogonal.
- `Ui.Lazy` / `Ui.Keyed` — diff-identity helpers, orthogonal.
- Color-render path (`colorCss`) — unrelated.
- `Ui.aspectRatio` / `Std.Ui.Grid` — all 4 in-page rig rows
  PASS at HEAD.

---

## Appendix A — Reproducing the audit

```bash
# 1. Rebuild compiler at HEAD (this branch's commit pinned).
TMPDIR=/tmp cabal install --overwrite-policy=always \
    --installdir=/tmp/audit-sky-bin --install-method=copy exe:sky

# 2. Build the rig.
node /tmp/cycle07-rig/build-rig.mjs
cd /tmp/cycle07-rig
TMPDIR=/tmp timeout 180 /tmp/audit-sky-bin/sky build src/Main.sky
SKY_LIVE_PORT=8780 ./sky-out/app &

# 3. Measure + analyze.
NODE_PATH=/Users/anzel/works/playground/sky/node_modules \
    node /tmp/cycle07-rig/measure.mjs
node /tmp/cycle07-rig/analyze.mjs   # prints PASS / FAIL summary

# 4. Z fixtures.
for z in z1 z2 z3; do
    cd /tmp/cycle07-rig-$z
    TMPDIR=/tmp timeout 90 /tmp/audit-sky-bin/sky build src/Main.sky
    SKY_LIVE_PORT=878X ./sky-out/app &
done
NODE_PATH=/Users/anzel/works/playground/sky/node_modules \
    node /tmp/cycle07-rig/measure-z.mjs   # Z1
NODE_PATH=/Users/anzel/works/playground/sky/node_modules \
    node /tmp/cycle07-rig/measure-z2.mjs  # Z2 (chain dump)
NODE_PATH=/Users/anzel/works/playground/sky/node_modules \
    node /tmp/cycle07-rig/measure-z3.mjs  # Z3
```

## Appendix B — Per-family acceptance criteria

**F1 closed when:**
- `widthFillFor` / `heightFillFor` emit `align-self: stretch`
  only for cross-axis fill (no `width: 100%` / `height: 100%`).
- Z2's textarea measures ≥ 700 px after the rig boots.
- Z3's sidebar / main / content all measure ≥ 700 px.
- `scripts/verify-issue-63.mjs` still passes.
- 26-ui-showcase HTML snapshot byte-identical.
- New `verify-cycle07-cross-axis.mjs` script passes.

**F2 closed when:**
- `splitLayoutAttrs` cherry-picked + applied to all 11 Input.*
  primitives.
- `Input.multiline [Ui.height Ui.fill] cfg` directly under a row
  fills correctly.
- `InputAttrsSplitSpec.hs` adopted + all 4 cases pass.
- `scripts/verify-issue-63-input.mjs` passes.

**F3 closed when:**
- `Ui.layoutWith` ships as the additive richer entry-point.
- `examples/26-ui-showcase` gains a `layout-wrapper-styling`
  card exercising wrapper Background.color.
- Existing `Ui.layout` callers byte-identical.

**F4 closed when:**
- Emitted style strings on `[width fill, centerX]` shapes carry
  ONE `align-self` declaration. Cabal spec asserts this.

**F5 closed when:**
- Either: a reproducing case is found and a fix lands, OR
- Verification shows the suspected gap is non-real and F5 is
  deleted from the plan.

---

## §5. v0.15.55 implementation log

What shipped on `feat/v0.15.55-stdui-correctness-f1-f2`:

**F1 (revised scope — asymmetric).** The audit proposed
stripping `100%` from BOTH `widthFillFor` cross-axis (column /
el / textColumn parents) AND `heightFillFor AsRow`. The
implementation only strips `heightFillFor AsRow` because the
full symmetric strip surfaced a F4 interaction in
`examples/26-ui-showcase`: the showcase's outer `Ui.column
[Ui.width (Ui.maximum 760 Ui.fill), Ui.centerX, ...]` was
relying on the pre-fix `width: 100%` to actually fill width
despite `Ui.centerX` cascading `align-self: center` over the
stretch. The full strip regressed the showcase from a 760-px
cap to a 574-px content-fit. The conservative scope:

  * `heightFillFor AsRow` → `align-self: stretch;` (was
    `align-self: stretch; height: 100%;`). Closes Z2 + Z3.
  * `widthFillFor AsColumn / AsEl / AsTextColumn` → unchanged
    (still `align-self: stretch; width: 100%;`). Column-parent
    widths are typically definite (block inheritance) AND the
    `width: 100%` survives the `centerX` cascade.

The audit's "symmetric width-axis bug … not separately
reproduced" gives cover for the asymmetric scope. The
symmetric strip can ship under F4 (when align-self
reconciliation lands) without breaking the F4 invariant first.

Regression fence:
  * `test/Sky/Build/UiFillCssSpec.hs` — 4 cases asserting
    `align-self: stretch; height: 100%;` never reappears,
    `align-self: stretch; width: 100%;` still appears,
    bare `align-self: stretch;` literal exists, main-axis
    fill still emits `flex-grow + min-{axis}: 0`.
  * `scripts/verify-stdui-matrix.mjs` — Z1/Z2/Z3 +
    7-cell in-page matrix (B-flex-chain / D-input / E-align).

**F2 (cherry-picked from `fix/403-input-wrapwithlabel-attrs-
split`).** `splitLayoutAttrs` + `implicitFillIfHoisted`
applied uniformly to all 11 `Input.*` primitives. Existing
helpers were directionally correct against the audit's F2
section — no in-flight tweaks needed. With F1 in place the
`Ui.height Ui.fill` injected by `implicitFillIfHoisted` on
the inner control correctly cascades because the wrapper now
carries the user's `Ui.height Ui.fill` directly via the split.

**F3 / F4 / F5: deferred to v0.15.56+.** F4 reconciliation
(double `align-self` emission) sequences cleanly behind F1.
F3 (`Ui.layoutWith`) is additive and not blocking. F5 was
unverified speculation; the audit rig's wrappedRow rows
already PASS, so F5 deletes unless a new repro appears.

**Doc + marker changes shipped in v0.15.55:**

  * `sky-stdlib/Std/Ui.sky` — `heightFillFor AsRow` emission
    + asymmetry-rationale comment block.
  * `sky-stdlib/Std/Ui/Input.sky` — `splitLayoutAttrs` /
    `isLayoutAttr` / `implicitFillIfHoisted` (from F2
    cherry-pick).
  * `src/Sky/Build/EmbeddedRuntime.hs` — re-embed markers
    `2026-06-01t` (F2) + `2026-06-01u` (F1).
  * `docs/skyui/overview.md` — new "`Ui.fill` — how it
    lowers (v0.15.55+)" section.
  * `CLAUDE.md` + `templates/CLAUDE.md` — `Ui.fill` asymmetry
    note alongside the existing #4 `Input.*` attrs-split
    convention.
  * `scripts/verify-stdui-matrix.mjs` — new 4-fixture
    Playwright regression set (self-bootstrapping).
  * `test/Sky/Build/UiFillCssSpec.hs` — new 4-case spec.
  * `test/Sky/Build/InputAttrsSplitSpec.hs` — adopted from
    F2 branch.

**Verification gates green at HEAD:**

  * `cabal test` — full suite + new UiFillCss + adopted
    InputAttrsSplit specs pass.
  * `scripts/example-sweep.sh` — 26 / 26 pass.
  * `scripts/verify-ui-showcase.sh` — all snapshots
    byte-identical to v0.15.54 baseline (the conservative
    F1 scope preserves the showcase's `width: 100%`
    behaviour).
  * `scripts/verify-issue-63.mjs` + `verify-issue-63-input.mjs`
    — both pass (textarea ≥ 764 px tall).
  * `scripts/verify-stdui-matrix.mjs` — 4 / 4 fixtures pass.

**Sibling bugs surfaced (per CLAUDE.md §4 no-deferral).** None
new from this work — F4 was already documented in the audit's
§2 root-cause taxonomy. The audit's F3 + F4 + F5 stay queued
on the v0.15.56+ task pipeline.
