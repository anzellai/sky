# Sky.Tui — Std.Ui coverage audit

Status: Honest gap analysis after `exp/tea-core` Phases A–D landed.
Goal: understand what fraction of Std.Ui actually renders correctly
under Sky.Tui today, versus what still falls back to nothing or
silent ignore.

The headline: **the typed core (Element + commonly-used Attributes)
is solid; the long tail of helpers, layout primitives, and Input.*
widgets is mostly unimplemented.** A real "extensive lib" pass
needs to cover them before we sell the cross-target story.

## Element constructors

| Std.Ui | Status | Notes |
|---|---|---|
| `none` | ✓ | Renders as Empty (nothing) |
| `text` | ✓ | UTF-8, rune-counted; uniseg grapheme polish deferred |
| `el` | ✓ | Single-element wrapper |
| `row` | ✓ | Flex-style horizontal; `Fill` portions distribute |
| `column` | ✓ | Flex-style vertical; same |
| `wrappedRow` | ✗ | wraps to next line on overflow — falls back to plain row |
| `grid` | ✗ | renders children stacked column-style; gridColumns ignored |
| `paragraph` | ✗ | should auto-wrap text within width — falls back to `el` |
| `textColumn` | ✗ | reading-width column — falls back to `column` |
| `link` | ◐ | renders the label; `url` is captured in attrs but not actionable in TUI (no browser to follow); pointer-style underline on focus |
| `image` | ◐ | renders `[image]` placeholder text; no sixel/iTerm/kitty protocols |
| `button` | ✓ | Including the focus ▸ ◂ markers |
| `input` | ✓ | Buffer + cursor + edit keys |
| `form` | ✗ | No special form-level handling; renders as `column` |
| `html` (raw) | ◐ | Renders `[raw]` placeholder — by design |

## Length

| Std.Ui | Status | Notes |
|---|---|---|
| `px` | ✓ | Logical-pixel canvas conversion |
| `shrink` (alias for `Content`) | ✓ | Shrink-to-fit children |
| `fill` | ✓ | Single-portion fill |
| `fillPortion` | ✓ | Multi-portion via `Fill N` |
| `minimum` | ✓ | Lower bound |
| `maximum` | ✓ | Upper bound |
| `vh` | ✓ | Viewport-height percent |
| `vw` | ✓ | Viewport-width percent |

## Layout attributes

| Std.Ui | Status | Notes |
|---|---|---|
| `width`, `height` | ✓ | |
| `padding`, `paddingXY`, `paddingEach` | ✓ | All map to cell counts via the canvas |
| `spacing` | ✓ | Cells between siblings |
| `centerX`, `centerY`, `alignLeft/Right/Top/Bottom` | ✗ | **Not yet implemented in cell layout.** ADT tags 3+4 are read but ignored — child placement is "stack from top-left" only |
| `pointer` | ✗ | No cursor concept in TUI; ignored (acceptable) |

## Style attributes

| Std.Ui | Status | Notes |
|---|---|---|
| `style "k" "v"` | ✗ | Raw CSS — ignored in TUI by design |
| `htmlAttribute "k" "v"` | ◐ | Reads `value`, `placeholder`, `name`; everything else ignored |
| `class` | ✗ | CSS class — ignored in TUI |

## Background

| Std.Ui | Status | Notes |
|---|---|---|
| `bgColor` | ✓ | ANSI 24-bit background |
| `Background.color` | ✓ | Same |
| `bgImage` | ✗ | No image fills in TUI |
| `bgGradient` | ✗ | No gradient fills |
| `Background.linearGradient` | ✗ | Generates a CSS string; TUI ignores |

## Border

| Std.Ui | Status | Notes |
|---|---|---|
| `borderWidth` | ✓ | Single-width all-sides |
| `borderWidthEach` (record) | ✓ | Per-side |
| `borderColor` | ✓ | Coloured box-drawing chars |
| `borderRounded` | ✗ | Documented IGNORED — no rounded box-drawing in standard Unicode |
| `borderStyle "solid/dashed/dotted"` | ✓ | All three styles |
| `Border.shadow` | ✗ | No shadows in TUI |
| `Border.glow` | ✗ | Same |
| `Border.innerShadow` | ✗ | Same |

## Font

| Std.Ui | Status | Notes |
|---|---|---|
| `fontColor` / `Font.color` | ✓ | ANSI 24-bit fg |
| `fontSize` / `Font.size` | ✗ | Documented IGNORED — terminal has one cell size |
| `fontFamily` / `Font.family` | ✗ | Documented IGNORED — terminal font |
| `fontWeight` / `Font.weight 700+` | ✓ | ANSI bold |
| `Font.bold/semiBold/regular/light/extraBold/black` | ◐ | Only bold (>=600) renders distinctly; others ignored |
| `fontItalic` / `Font.italic` | ✓ | ANSI italic (terminal-dependent) |
| `fontUnderline` / `Font.underline` | ✓ | ANSI underline |
| `Font.noDecoration` | ✗ | Doesn't strip earlier decorations |
| `Font.lineThrough` | ✗ | ANSI strikethrough (SGR 9) — not yet wired |
| `Font.overline` | ✗ | ANSI 53 — not wired |
| `fontDecoration "value"` | ✗ | Generic dispatch missing |
| `fontLetterSpacing` / `Font.letterSpacing` | ✗ | No fractional cell spacing |
| `fontWordSpacing` / `Font.wordSpacing` | ✗ | Same |
| `fontAlign "value"` / `Font.alignLeft/Right/Center` | ✗ | Text alignment not implemented |

## Overflow / scrolling

| Std.Ui | Status | Notes |
|---|---|---|
| `clip` / `clipX` / `clipY` | ✗ | All paint everything; no clipping |
| `scrollbars` / `scrollbarX` / `scrollbarY` | ✗ | No scroll-region support |
| `overflow "x" "y"` | ✗ | Same |

## Nearby (overlays)

| Std.Ui | Status | Notes |
|---|---|---|
| `above`, `below`, `onLeft`, `onRight`, `inFront`, `behind` | ✗ | Not implemented; the wrapped Element is dropped silently |

## Region (accessibility)

| Std.Ui | Status | Notes |
|---|---|---|
| `Region.heading 1..6` (= `descHeading`) | ◐ | h1/h2 underline; h3-h6 just bold |
| `Region.mainContent`, `Region.navigation`, `Region.footer`, `Region.aside` | ✗ | Read but not rendered differently (acceptable — no semantic markup in TUI) |
| `Region.label` (aria-label) | ✗ | Read but unused |
| `Region.announce`, `Region.announceUrgently` (aria-live) | ✗ | No screen-reader bridge |

## Events

| Std.Ui | Status | Notes |
|---|---|---|
| `onClick` | ✓ | Enter on focused button + mouse click |
| `onSubmit` | ✗ | No form-level event handling yet |
| `onInput` | ✓ | Per-keystroke in focused inputs |
| `onChange` | ✓ | Enter on focused input fires it |
| `onFocus` | ✗ | Not dispatched on Tab arrival |
| `onMouseOver` / `onMouseOut` | ✗ | Mouse hover (motion tracking) not enabled |
| `onKeyDown` | ✗ | Keystroke-level event not surfaced |
| `onFile` / `onImage` | ✗ | No file picker in TUI |
| `fileMaxSize/Width/Height` | ✗ | Ignored (no file picker) |

## Input.* (richer typed inputs)

| Std.Ui.Input | Status | Notes |
|---|---|---|
| `Input.button` | ✓ | Same as `Ui.button` |
| `Input.text` | ✗ | Custom record-shaped API; doesn't currently route |
| `Input.email`, `Input.username`, `Input.search` | ✗ | Same — type-validation is HTML-side |
| `Input.currentPassword`, `Input.newPassword` | ✗ | Same; password masking not wired |
| `Input.multiline` | ✗ | No multiline editor |
| `Input.checkbox` | ✗ | No native rendering |
| `Input.radio`, `Input.radioRow` | ✗ | Same |
| `Input.slider` | ✗ | Same |
| `Input.labelAbove/Below/Left/Right/Hidden` | ✗ | Layout helpers around inputs |
| `Input.placeholder` | ✗ | Currently we read the bare `placeholder` HTML attr; the `Input.placeholder` record type isn't routed |
| `Input.option` | ✗ | RadioOption type — no support |

## Keyed (diff identity)

| Std.Ui.Keyed | Status | Notes |
|---|---|---|
| `Keyed.el / row / column` | ✗ | Falls back to plain `el / row / column`; no key-based diff (we re-render the whole tree anyway) |
| `Keyed.keyAttr` | ✗ | Same |

## Lazy (memoisation)

| Std.Ui.Lazy | Status | Notes |
|---|---|---|
| `Lazy.lazy / lazy2..5` | ✓ trivial | Std.Ui already implements these as no-op wrappers; works automatically |

## Responsive

| Std.Ui.Responsive | Status | Notes |
|---|---|---|
| `Responsive.classifyDevice` | ✓ | Pure function — works regardless of backend |
| Backend-aware breakpoint adaptation | ✗ | User code calls classify and decides; no extra TUI hook needed |

## Summary

**Coverage by surface area (rough percentages):**

| Category | Implemented | Partial | Not implemented |
|---|---|---|---|
| Element constructors | 8/15 (53%) | 3/15 | 4/15 |
| Length | 8/8 (100%) | – | – |
| Layout attrs | 6/9 (67%) | – | 3/9 |
| Style/HTML attrs | 0/3 | 1/3 | 2/3 |
| Background | 1/4 (25%) | – | 3/4 |
| Border | 4/9 (44%) | – | 5/9 |
| Font | 5/16 (31%) | 1/16 | 10/16 |
| Overflow | 0/7 | – | 7/7 |
| Nearby | 0/6 | – | 6/6 |
| Region | 1/8 (13%) | 1/8 | 6/8 |
| Events | 4/11 (36%) | – | 7/11 |
| Input.* | 1/16 (6%) | – | 15/16 |
| Keyed | 0/4 | – | 4/4 |
| **Total** | **~38%** | **~6%** | **~56%** |

That's a fair characterisation: the typed core works, the demo
subset works, but a real "write the same view for Live and Tui and
it'll just work" claim needs ~60% more surface area.

## Where to invest next

Priority ranking by impact-vs-cost:

### Tier 1 — high impact, low cost
1. **Alignment** (`centerX`, `centerY`, `alignLeft/Right/Top/Bottom`)
   — straightforward layout pass change, ~half day
2. **Font.lineThrough / overline / noDecoration / fontDecoration**
   — direct ANSI mappings; ~half day
3. **Clip / scrollbars (clipping)** — bound child paint to parent
   box; ~1 day
4. **Region semantics for h3-h6** — distinct visual treatment
   (e.g. preceding markers); ~half day
5. **Onfocus / onBlur dispatch** — fire when Tab arrives/leaves
   focusables; ~half day

### Tier 2 — high impact, medium cost
6. **paragraph** with text wrap — proper word-wrapping inside a
   width constraint. Needs Unicode line-break-rule (uniseg already
   bundled). ~2 days
7. **wrappedRow** — same wrapping logic but for arbitrary children.
   ~1 day
8. **textColumn** — reading-width column. ~half day after paragraph
9. **Nearby overlays** (`above`, `below`, `onLeft`, `onRight`,
   `inFront`, `behind`) — render the secondary element at offsets
   from the primary's box. ~1-2 days
10. **Scroll regions** — per-element scroll-offset state, arrow
    keys when focused. ~2 days
11. **grid + gridColumns** — auto-layout grid. ~2 days

### Tier 3 — medium impact, higher cost
12. **Input.checkbox / radio / radioRow** — custom widgets with
    keyboard nav (Space toggles, arrows for radio groups). ~3 days
13. **Input.slider** — keyboard nav, visual track. ~1-2 days
14. **Input.multiline** — multi-line editor with vertical cursor
    movement. ~3 days
15. **Input.password** masking — display ●●● in TUI. ~half day
16. **Input.* label helpers** (labelAbove/Below/Left/Right/Hidden)
    — render the label appropriately around the input. ~1 day
17. **Form onSubmit collection** — form-level event that gathers
    all child input values into a record Msg. ~2 days

### Tier 4 — long tail
18. **Keyed diff** — useful for performance with many items, but
    diff-render already does cell-level diffing so this is more a
    correctness aid than a perf win. Defer.
19. **Border.shadow / glow / innerShadow** — TUI doesn't render
    shadows. Document as IGNORED.
20. **Background.bgImage / bgGradient** — same.
21. **Terminal-image protocols** for `image` (sixel/iTerm/kitty) —
    detection is per-terminal, fragile. ~1-2 days when needed.
22. **fontFamily / fontSize** — IGNORED by TUI design. Don't try.

## Honest delivery estimate

To go from 38% → ~85% (full typed-core + most Input.* widgets):

- Tier 1: ~3 days
- Tier 2: ~10 days
- Tier 3: ~10 days
- Tier 4: skip / document

That's roughly **~3 weeks of focused work** on top of what's already
shipped. After Tier 1+2 (~2 weeks) we'd be at ~70% with all major
layout primitives covered — that's the realistic milestone for "you
can write a non-trivial Sky.Live app and have it render under
Sky.Tui without changes." Tier 3 (Input.* widgets) is the bigger
investment but each widget is independent so it ships incrementally.

## What I would NOT do

- **Don't try to reach 100%.** ~15% of Std.Ui is HTML/CSS-specific
  by design (raw `style`, `class`, `bgImage`, gradients, font-
  family/size, shadows, scroll bars as visual elements). Document
  as opt-out and move on.
- **Don't block Sky.Webview on Tui completeness.** The Webview
  embeds the existing Sky.Live HTML renderer — independent of
  Sky.Tui's coverage.
- **Don't auto-translate `bgGradient` to ANSI gradient runs.**
  Tempting but breaks more than it helps; users can opt in
  explicitly later.
