# Std.Ui Portability Audit

Status: Living document — updated as backends evolve
Started: 2026-05-12

## Purpose

For every primitive in `Std.Ui` (and sub-modules `Background`, `Border`,
`Font`, `Region`, `Input`, `Lazy`, `Keyed`, `Responsive`, `Events`),
classify its behaviour across the three planned backends:

- **Live** — Sky.Live (HTML/CSS, browser)
- **Tui** — Sky.Tui v1 (ANSI cells, terminal)
- **Gui** — Sky.Gui on gio (native window, planned for v0.13)

Three tiers:

- ✅ **Portable** — works fully on all three; renders the same intent
- 🟡 **Degradable** — renders fully on Live; Tui and/or Gui
  approximate or warn but never break the layout
- 🔴 **Web-only** — Live only; non-web backends warn (`tuiWarn` style)
  and render a placeholder. User code keeps compiling and running.

The classification is what we DESIGN the backends to honour, not
what they currently do. A 🟡 marked "Tui: warn" is a contract Sky.Tui
must honour; today Tui may either implement or warn — but it will
NOT silently render wrong output.

## Marker plan

Add a runtime-level taxonomy (not a type-system marker — too invasive
for v0.13). Each backend imports the audit table at codegen / runtime
and emits warnings for tier-mismatch primitives, same pattern as
`tuiWarn(category, detail)` today.

A future `Std.Ui.Portability` module could expose the tier as a
queryable value if user code wants to branch on it.

---

## Core (`Std.Ui`)

### Layout primitives

| Primitive | Live | Tui | Gui (gio) | Tier |
|---|---|---|---|---|
| `none` | empty inline | empty cell | empty layout | ✅ Portable |
| `text` | `<span>` text | terminal text run | `material.Label` | ✅ Portable |
| `el` | `<div>` wrapper | bordered/padded cell box | `layout.UniformInset` + bg/border ops | ✅ Portable |
| `row` | flexbox-row | horizontal cell layout | `layout.Flex{Axis: Horizontal}` | ✅ Portable |
| `column` | flexbox-column | vertical cell layout | `layout.Flex{Axis: Vertical}` | ✅ Portable |
| `wrappedRow` | flex-wrap row | wraps cells | **needs custom layout helper** (gio has no wrap) | 🟡 Degradable |
| `grid` (CSS-Grid auto-fit) | grid template | n-column terminal grid | **custom column-count compute + Flex** | 🟡 Degradable |
| `paragraph` | inline text run | word-wrap text | `material.Body` with shaper | ✅ Portable |
| `textColumn` | block text | vertical text run | column of `material.Label` | ✅ Portable |
| `gridColumns N` (attribute) | sets minmax | sets column count | sets target column count | 🟡 Degradable |
| `html` (VNode escape hatch) | renders VNode | warn + placeholder | warn + placeholder | 🔴 Web-only |

### Length

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `px N` | `Npx` CSS | cells (rounded via pxToCells) | `unit.Dp(N)` | ✅ Portable |
| `shrink` | `width: auto` | shrink-to-content | `layout.Rigid` | ✅ Portable |
| `fill` | `flex: 1` | fill remaining cells | `layout.Flexed{Weight: 1}` | ✅ Portable |
| `fillPortion N` | `flex: N` | proportional fill | `layout.Flexed{Weight: float32(N)}` | ✅ Portable |
| `minimum N L` | `min-width/min-height: Npx` | constrain ≥ N | clamp constraints lower bound | ✅ Portable |
| `maximum N L` | `max-width/max-height: Npx` | constrain ≤ N | clamp constraints upper bound | ✅ Portable |
| `vh N` | `Nvh` CSS (viewport-relative) | cells via viewport height | window-height percent | ✅ Portable |
| `vw N` | `Nvw` CSS (viewport-relative) | cells via viewport width | window-width percent | ✅ Portable |
| `content` | `width: max-content` | content size | `layout.Rigid` | ✅ Portable |

### Padding / spacing

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `padding N` | `padding: Npx` | N-cell pad | `layout.UniformInset{Top:N, Right:N, ...}` | ✅ Portable |
| `paddingXY x y` | `padding: y x` | x/y cell pad | `layout.Inset{Top:y, Right:x, Bottom:y, Left:x}` | ✅ Portable |
| `paddingEach {top,right,bottom,left}` | per-side CSS | per-side cells | `layout.Inset{...}` | ✅ Portable |
| `spacing N` (row/column gap) | `gap: Npx` | inter-child cells | **interleave `layout.Spacer{Width:N}` between children** | 🟡 Degradable |
| `width L` / `height L` | CSS w/h | cell w/h | `gtx.Constraints` | ✅ Portable |

### Alignment

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `centerX` | `align-items` / `text-align: center` | center horizontally | flex `Center` alignment | ✅ Portable |
| `centerY` | `align-items: center` / `align-self` | center vertically | flex `Center` alignment | ✅ Portable |
| `alignLeft` / `alignRight` | flex / text-align | left/right cells | flex `Start`/`End` | ✅ Portable |
| `alignTop` / `alignBottom` | flex | top/bottom cells | flex `Start`/`End` | ✅ Portable |
| `pointer` | `cursor: pointer` | warn — no cursor in terminal | gio pointer cursor | 🟡 Degradable |

### Overflow

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `clip` / `clipX` / `clipY` | `overflow: hidden` | clip to bounds | `clip.Rect` | ✅ Portable |
| `scrollbars` / `scrollbarX` / `scrollbarY` | `overflow: scroll` | terminal scroll region | `layout.List` | ✅ Portable |

### Nearby positioning (overlays)

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `inFront e` | absolute on top | overlay cell | `layout.Stack` top layer | ✅ Portable |
| `behind e` | absolute behind | underlay cell | `layout.Stack` bottom layer | ✅ Portable |
| `above e` | absolute above parent | row above | **custom positioning** (gio has no above) | 🟡 Degradable |
| `below e` | absolute below | row below | **custom positioning** | 🟡 Degradable |
| `onLeft e` | absolute left | column left | **custom positioning** | 🟡 Degradable |
| `onRight e` | absolute right | column right | **custom positioning** | 🟡 Degradable |

### Color

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `rgb r g b` / `rgb255 r g b` | CSS rgb | nearest ANSI 256 | `color.NRGBA{R,G,B,A:255}` | ✅ Portable |
| `rgba r g b a` | CSS rgba | nearest ANSI + warn alpha unsupported | `color.NRGBA{R,G,B,A: byte(a*255)}` | 🟡 Degradable (Tui loses alpha) |
| `white` / `black` / `transparent` | CSS | terminal colours | gio constants | ✅ Portable |

### Style escape hatches

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `style key value` | inline CSS | warn — CSS not honoured | warn — CSS not honoured | 🔴 Web-only |
| `htmlAttribute key value` | HTML attr | warn — HTML not rendered | warn — HTML not rendered | 🔴 Web-only |
| `class name` | CSS class | warn | warn | 🔴 Web-only |
| `name n` (form field name) | `name=` attr | not used | not used | 🟡 Degradable (no-op on non-web) |

### Events

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `onClick msg` | click/touch | enter/space on focus | `pointer.InputOp{Types: pointer.Press}` | ✅ Portable |
| `onSubmit msg` | form submit | terminal Enter on focused form | enter on focused form-shape | ✅ Portable |
| `onInput (s -> msg)` | input event | char input on focused field | `widget.Editor` text events | ✅ Portable |
| `onChange (s -> msg)` | change event | enter on field | edit-blur events | ✅ Portable |
| `onFocus msg` | focus | tab-cycle to | focus events | ✅ Portable |
| `onMouseOver` / `onMouseOut` | mouse enter/leave | warn — no mouse | pointer enter/leave | 🟡 Degradable (Tui warns) |
| `onKeyDown msg` | keydown | keypress | `key.InputOp` | ✅ Portable |
| `onFile (url -> msg)` | file input + data URL | warn — no file dialog | warn — no file dialog (deferrable) | 🟡 Degradable (file path is Web-only for now) |
| `onImage` | same as file | warn | warn | 🟡 Degradable |

### Sized elements

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `button {onPress, label}` | `<button>` | bordered + focusable cell | `widget.Clickable` (or custom via pointer ops) | ✅ Portable |
| `input` | `<input>` | text-input cell with editor | `widget.Editor` (or custom) | ✅ Portable |
| `form onSubmit` | `<form>` | form-shape with enter | container with submit handler | ✅ Portable |
| `link {url, label}` | `<a href>` | bracketed `[text]` | OS exec `open url` | 🟡 Degradable (Tui shows URL, Gui opens system browser) |
| `image {src, description}` | `<img src>` | warn or ASCII art | `widget.Image` (loads via `image` package) | 🟡 Degradable (Tui shows description) |

---

## `Std.Ui.Background`

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `color c` | `background: c` | cell bg colour | `paint.FillShape` | ✅ Portable |
| `image url` | `background-image: url(...)` | warn — no images | `paint.NewImageOp` + draw | 🟡 Degradable (Tui warns) |
| `linearGradient angle stops` | CSS gradient | warn | **custom shader** — defer | 🔴 Web-only (initially); Gui can add later |
| `gradient css` | CSS raw | warn | warn | 🔴 Web-only |

## `Std.Ui.Border`

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `color c` | `border-color: c` | border cells colour | `widget.Border{Color: c}` | ✅ Portable |
| `width n` | `border-width: npx` | border thickness | `widget.Border{Width: unit.Dp(n)}` | ✅ Portable |
| `widthEach {top,right,bottom,left}` | per-side CSS | per-side cells | per-side border (gio supports unequal) | ✅ Portable |
| `rounded n` | `border-radius: npx` | terminal Unicode rounded corners | `clip.RRect{SE,SW,NE,NW: n}` | ✅ Portable |
| `solid` / `dashed` / `dotted` | CSS border-style | corner chars for solid; warn for dashed/dotted | solid OK; dashed/dotted need **custom paint** | 🟡 Degradable |
| `shadow {ox,oy,blur,spread,color}` | CSS box-shadow | warn | **custom paint** (multi-pass blur) | 🟡 Degradable |
| `glow blur color` | CSS box-shadow | warn | custom paint | 🟡 Degradable |
| `innerShadow {...}` | CSS inset shadow | warn | custom paint | 🟡 Degradable |

## `Std.Ui.Font`

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `color c` | CSS color | text fg colour | `material.Label` color | ✅ Portable |
| `size n` | CSS font-size | warn — fixed cell height | `unit.Sp(n)` | 🟡 Degradable (Tui uses single size) |
| `family s` | CSS font-family | warn — terminal font | gio font registry | 🟡 Degradable (Tui ignores) |
| `weight n` | CSS font-weight | bold or regular only | gio font weight | 🟡 Degradable (Tui bins to bold/regular) |
| `bold` / `semiBold` / `regular` / `light` / `extraBold` / `black` | CSS keywords | bold for ≥600, regular else | gio font weights | 🟡 Degradable (Tui bins) |
| `italic` | `font-style: italic` | terminal italic SGR (often unsupported) | gio italic style | 🟡 Degradable (depends on terminal) |
| `underline` / `lineThrough` / `overline` | text-decoration | SGR underline; warn lineThrough/overline | gio decoration (underline OK; strikethrough custom) | 🟡 Degradable |
| `noDecoration` | none | none | none | ✅ Portable |
| `letterSpacing em` | CSS letter-spacing | warn | **gio text.Shaper doesn't expose** | 🔴 Web-only (initially) |
| `wordSpacing em` | CSS word-spacing | warn | same as letterSpacing | 🔴 Web-only |
| `alignLeft` / `alignRight` / `alignCenter` / `center` / `justify` | text-align | text alignment | `text.Alignment` | ✅ Portable |
| `sansSerif` / `serif` / `monospace` | family constants | terminal font | gio family selection | 🟡 Degradable |

## `Std.Ui.Region` (accessibility)

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `heading n` | `<h1>..<h6>` | terminal banner (`═`/`─`) | gio `text.Semantics` + size emphasis | 🟡 Degradable |
| `mainContent` | `<main>` | no-op | gio aria region | 🟡 Degradable (Tui no-op) |
| `navigation` | `<nav>` | no-op | gio nav region | 🟡 Degradable |
| `footer` | `<footer>` | no-op | gio footer | 🟡 Degradable |
| `aside` | `<aside>` | no-op | gio aside | 🟡 Degradable |
| `label text` | `aria-label` | no-op | gio label | 🟡 Degradable |
| `announce` | `aria-live="polite"` | warn | gio announce | 🟡 Degradable |
| `announceUrgently` | `aria-live="assertive"` | warn | gio announce | 🟡 Degradable |

## `Std.Ui.Input`

All `Input.*` primitives map cleanly to the three backends in
PRINCIPLE — the widget shape is the same (label + field + onChange).
Implementation specifics:

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `button` | `<button>` | focusable button cell | `widget.Clickable` | ✅ Portable |
| `text` | `<input type="text">` | text-input editor cell | `widget.Editor` | ✅ Portable |
| `multiline` | `<textarea>` | multiline editor | `widget.Editor{SingleLine: false}` | ✅ Portable |
| `email` / `username` / `search` | typed `<input>` | same as text + inputmode | `widget.Editor` with filter | ✅ Portable |
| `currentPassword {show}` | `<input type="password">` | masked cell editor | `widget.Editor` with `Mask` | ✅ Portable |
| `newPassword {show}` | same as currentPassword | same as currentPassword | same | ✅ Portable |
| `checkbox` | `<input type="checkbox">` | `☐` / `☑` | `widget.Bool` | ✅ Portable |
| `radio` / `radioRow` | `<input type="radio">` | `○` / `●` | `widget.Enum` | ✅ Portable |
| `slider {min,max,step}` | `<input type="range">` | warn — no slider in TUI | `widget.Float` | 🟡 Degradable (Tui takes value via arrow keys) |
| `option` / `labelAbove/Below/Left/Right/Hidden` / `placeholder` | helper records | helper records | helper records | ✅ Portable |

## `Std.Ui.Keyed` / `Std.Ui.Lazy` / `Std.Ui.Responsive`

| Primitive | Live | Tui | Gui | Tier |
|---|---|---|---|---|
| `Keyed.el` / `row` / `column` | `sky-key` diff identity | sky-key | gio frame identity | ✅ Portable |
| `Keyed.applyKey` / `keyAttr` | sky-key attribute | sky-key | gio key | ✅ Portable |
| `Lazy.lazy` / `lazy2..5` | memoised subtree | currently no-op; v0.12+ LRU | gio's `op.Record` memo | ✅ Portable |
| `Responsive.classifyDevice` / `adapt` | window size CSS | terminal cols/rows | gio constraints | ✅ Portable |

## `Std.Ui.Events`

Sub-module re-exports — same classification as the parent `Events` row above.

---

## Counts

| Tier | Count |
|---|---|
| ✅ Portable | ~60 primitives |
| 🟡 Degradable | ~30 primitives |
| 🔴 Web-only | ~7 primitives (`Ui.html`, `style`, `htmlAttribute`, `class`, gradients, letterSpacing, wordSpacing) |

**~85% of Std.Ui is portable or degradable cleanly.** The Web-only set
is small and identifiable. The user can write portable apps by avoiding
the 🔴 set and accepting graceful degradation on 🟡.

---

## What Sky.Gui v0.13 ships

**MVP (Stage 2 in the plan)**: all ✅ Portable primitives + the
Degradable subset for layout/input. Defer:
- 🔴 Web-only primitives → emit `guiWarn` + render placeholder
- Custom-paint primitives (gradients, shadows, dashed/dotted, glow,
  innerShadow) → render fallback (solid color / single-pass border) +
  `guiWarn` for now; revisit in v0.13.1

**The contract**: a Sky.Live app using only ✅+🟡 primitives renders
identically on Sky.Gui at the layout level, with degradable
primitives best-effort. Same code, three targets, predictable
fallbacks.

---

## How the audit is enforced

1. **Runtime**: each backend has a `*_warn.go` (e.g.
   `runtime-go/rt/tui_warn.go` pattern) that emits a deduplicated
   warning when it encounters a non-portable primitive.
2. **Code review**: this document is the source of truth. New
   primitives MUST be added here before being accepted in any
   stdlib PR.
3. **Tests** (future): a portable-subset linter in `sky check` could
   flag 🔴 use when a `--target=gui` or `--target=tui` flag is set.

---

## Open questions

- **Lazy/Keyed Gui impl**: gio's `op.Record` / `op.CallOp` is the
  right primitive but needs careful interaction with `Cmd.perform`
  triggered re-renders.
- **Image loading on Gui**: blocking HTTP fetch in `widget.Image`?
  Async via Cmd.perform with a cache key?
- **`Lazy.lazy` cache key**: function-pointer + args fingerprint
  works on Live; gio needs the same key + invalidation strategy.
- **iOS-specific event handling** (touch + swipe + pinch): out of
  scope for Sky.Gui v0.13, in scope for v0.14 mobile.
