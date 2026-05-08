# Std.Ui cross-platform mapping

Status: Design reference for `exp/tea-core` spike onwards.
Companion doc: `tea-backends.md`.

This document tables every Std.Ui primitive against its target
representation in each backend (Sky.Live HTML, Sky.Webview HTML,
Sky.Tui ANSI cells, Sky.Gui gio operations) so we can see at a glance:

  - Where the abstraction is clean (typed primitive maps cleanly to
    every backend)
  - Where it's lossy but acceptable (lower fidelity in TUI for
    visual extras like shadows, but functional behaviour preserved)
  - Where it's an explicit escape hatch (raw HTML, raw CSS — opt-in
    portability loss)
  - What still needs design (focus management, animation)

## The logical-pixel canvas

Before the table: a foundational design call.

`Length.Px N` is **a logical pixel against a 1280×720 design canvas**,
not a physical pixel. Each renderer converts to its native unit:

| Backend | Conversion |
|---|---|
| Sky.Live, Sky.Webview | `Px N` → `Npx` CSS (CSS pixels are already logical via DPR) |
| Sky.Tui | `Px N` → `round(N / pxPerCellAxis)` character cells, where `pxPerCellX = 1280 / termCols`, `pxPerCellY = 720 / termRows`, recomputed on SIGWINCH |
| Sky.Gui (gio) | `Px N` → `N` device-independent units (gio's dp) |

This makes pixel layouts portable. A `column [width (px 320)]` design
takes ~25% of width in a 1280-wide browser, ~25% of width in a
120-column terminal, and ~25% of width on a 1280-wide gio window.
The 25%-of-width invariant holds across every backend.

The canvas dimensions are configurable per program (e.g. for
TUI-first apps designed at 80×24, set `canvas = (640, 384)` so the
ratio is right). Default: 1280×720 (HD baseline).

Aspect ratio: TUI cells are ~2:1 (height:width), so X and Y axes
get independent scalars. Layout *structure* is preserved; visual
shapes will differ (a `Px 100 × Px 100` square renders as ~9 cells
× ~4 rows). For layout-driven UIs this is the right tradeoff.

Sub-cell rounding is handled by carrying fractional cells through
the layout pass and quantising only at leaves, with floor + remainder-
distribution for `Fill`-style portion divisions. Same algorithm
flexbox uses.

---

## Element constructors

| Std.Ui | Live HTML | Webview | Tui | Gui (gio) |
|---|---|---|---|---|
| `Empty` | `<text></text>` | same | nothing | no-op |
| `Text "hello"` | `<text>hello</text>` | same | text in cells (UTF-8, grapheme-aware via uniseg) | gio paint.Text |
| `Node desc attrs children` | `<div>` (or semantic tag from Description) | same | box with children laid out in default direction | gio Stack/Flex |
| `TaggedNode "h1" …` | `<h1>` | same | bold + double-line border below | gio bigger font |
| `TaggedNode "img" …` | `<img>` | same | `[image]` placeholder OR sixel/iTerm img protocol if terminal supports | gio paint.NewImageOp |
| `TaggedNode "a" …` | `<a>` | same | underlined, focusable; Enter activates href as Msg dispatch | gio clickable |
| `TaggedNode "button" …` | `<button>` | same | `[ Label ]` rendering, focusable | gio clickable + style |
| `TaggedNode "input" …` | `<input>` | same | text-input widget with cursor + edit keys | gio editor |
| `TaggedNode "form" …` | `<form>` | same | container, focus group | gio container |
| `Raw vnode` | passthrough Std.Html | same | `[raw HTML omitted]` placeholder | skip / placeholder |

**Lossy-but-acceptable:** `TaggedNode "img"` in TUI — terminal-image protocols (sixel, iTerm2, kitty) work but aren't universal; `[image]` placeholder is the safe default. **Fully lossy:** `Raw any` is by design an escape hatch — opting out of portability is the user's choice.

---

## Length

| Std.Ui | Live HTML | Webview | Tui | Gui (gio) |
|---|---|---|---|---|
| `Px N` | `Npx` | same | `pxToCellsX(N)` or `pxToCellsY(N)` | `unit.Dp(N)` |
| `Content` | `width: auto` | same | measured from intrinsic child sizes | gio `Rigid` |
| `Fill 1` | `flex: 1 1 auto` | same | `1 / total_portions` of remaining cells | `layout.Flexed(1)` |
| `Fill N` | `flex: N 1 auto` | same | `N / total_portions` of remaining cells | `layout.Flexed(N)` |
| `Min N (Fill 1)` | `min-width: Npx; flex: 1` | same | `max(N_cells, computed)` | clamp |
| `Max N (Fill 1)` | `max-width: Npx; flex: 1` | same | `min(N_cells, computed)` | clamp |
| `Vh N` | `Nvh` | same | `(N * termRows) / 100` | `(N * windowH) / 100` |
| `Vw N` | `Nvw` | same | `(N * termCols) / 100` | `(N * windowW) / 100` |

**All lengths lift cleanly.** No fundamental loss — only the resolution differs across backends.

---

## Alignment

| Std.Ui | Live HTML | Tui | Gui |
|---|---|---|---|
| `AlignLeft` (HAlign) | `justify-content: flex-start` (in row) / `align-items: flex-start` (in column) | child placed at column 0 of its slot | `layout.W` |
| `CenterX` | `justify-content: center` / `align-items: center` | child centred in its slot (cell-rounded) | `layout.Center` |
| `AlignRight` | `justify-content: flex-end` / `align-items: flex-end` | child placed at right edge of slot | `layout.E` |
| `AlignTop` (VAlign) | analogous | child placed at top row of slot | `layout.N` |
| `CenterY` | analogous | child centred vertically | `layout.Center` |
| `AlignBottom` | analogous | child placed at bottom row | `layout.S` |

**Maps cleanly.** Cell-rounding for Tui means a centred odd-width child has a 1-cell preference (left). Document.

---

## Padding & spacing

| Std.Ui | Live HTML | Tui | Gui |
|---|---|---|---|
| `AttrPadding T R B L` | `padding: Tpx Rpx Bpx Lpx` | `pxToCells*` for each side; blank rows above/below, blank cols left/right | gio inset |
| `AttrSpacing N` | `gap: Npx` (CSS gap on flex containers) | `pxToCellsAxis(N)` blank cells between siblings | gio spacing |

**Clean.** TUI loses sub-cell padding (rounds to nearest cell) but proportions stay right.

---

## Colour

| Std.Ui | Live HTML | Tui | Gui |
|---|---|---|---|
| `Rgba 255 102 0 1.0` | `rgb(255, 102, 0)` | nearest of 256 ANSI colours (fast Lab-distance match) — falls back to 16-colour on legacy terms | `color.NRGBA{R: 255, G: 102, B: 0, A: 255}` |
| `Rgba ... 0.5` (alpha) | `rgba(...)` | IGNORED (terminals don't support alpha) — render at full opacity, document | gio handles natively |

**Lossy in Tui (alpha) but acceptable.** Terminal cells are opaque. Alpha-aware compositing isn't a thing. We render at full opacity and document the limit. The 256-colour mapping is good enough that designs look recognisable — Bubble Tea's lipgloss uses the same approach.

---

## Border

| Std.Ui | Live HTML | Tui | Gui |
|---|---|---|---|
| `AttrBorderWidth N` | `border-width: Npx` | unicode box drawing chars (─ │ ┌ ┐ └ ┘) — N>0 means draw, N=0 means don't | gio `clip.Stroke{Width: N}` |
| `AttrBorderWidthEach T R B L` | `border-(side)-width: Npx` | independent edges using box-drawing chars | gio per-side stroke |
| `AttrBorderColor` | `border-color: rgb(...)` | colour applied to box chars | gio stroke fill |
| `AttrBorderRounded R` | `border-radius: Rpx` | IGNORED (no rounded box-chars in standard Unicode); document | gio `clip.RRect` |
| `AttrBorderStyle "solid"` | `border-style: solid` | continuous box chars | gio solid |
| `AttrBorderStyle "dashed"` | `border-style: dashed` | dash chars (─ ─ ─) | gio dashed |
| `AttrBorderStyle "dotted"` | `border-style: dotted` | dot chars (· · ·) | gio dotted |
| `AttrBorderShadow ox oy blur spread color` | `box-shadow: ox oy blur spread rgba` | IGNORED (no terminal shadows); document | gio drop shadow |
| `AttrBorderInsetShadow …` | `box-shadow: inset …` | IGNORED | gio inset shadow |

**Borders work. Rounded corners + shadows lossy in Tui (silently dropped).** For users who want portable code, document: "rounded borders and shadows are visual decoration; layout is preserved without them."

---

## Font

| Std.Ui | Live HTML | Tui | Gui |
|---|---|---|---|
| `AttrFontSize N` | `font-size: Npx` | IGNORED (terminal has one cell size); document | gio `unit.Sp(N)` |
| `AttrFontWeight 700` | `font-weight: 700` | ANSI bold (SGR 1) when ≥ 600, normal otherwise | gio `text.Bold` |
| `AttrFontItalic` | `font-style: italic` | ANSI italic (SGR 3) | gio `text.Italic` |
| `AttrFontUnderline` | `text-decoration: underline` | ANSI underline (SGR 4) | gio underline |
| `AttrFontDecoration "line-through"` | `text-decoration: …` | ANSI strikethrough (SGR 9) | gio strikethrough |
| `AttrFontDecoration "none"` | `…` | strip ANSI styles | clear |
| `AttrFontDecoration "overline"` | `…` | ANSI overline (SGR 53), patchy support | gio overline |
| `AttrFontFamily "Inter, sans-serif"` | `font-family: …` | IGNORED (terminal font); document | gio `text.Font{Typeface: "Inter"}` |
| `AttrFontColor color` | `color: rgb(...)` | ANSI 256-colour foreground | gio paint.ColorOp |
| `AttrFontLetterSpacing N` | `letter-spacing: Nem` | IGNORED | gio letter-spacing |
| `AttrFontWordSpacing N` | `word-spacing: Nem` | IGNORED | gio word-spacing |
| `AttrFontAlign "center"` | `text-align: center` | text aligned in cell range | gio align |

**Font fidelity is the biggest TUI loss.** One cell size, one (mostly) font family, no spacing tweaks. But weight/italic/underline/colour all carry. **Functional invariant: text content is identical, just less typographically rich.**

---

## Background

| Std.Ui | Live HTML | Tui | Gui |
|---|---|---|---|
| `AttrBgColor color` | `background-color: rgb(...)` | ANSI 256-colour background per cell in the element's box | gio fill rect |
| `AttrBgImage "url(...)"` | `background-image: url(...)` | IGNORED | parse CSS-ish URL, gio image fill (limited) |
| `AttrBgGradient "linear-gradient(...)"` | `background-image: linear-gradient(...)` | IGNORED | parse → gio paint.LinearGradientOp (subset) |

**Solid colours portable. Images and gradients lossy in Tui, partial in Gui.** For portable code: stick to BgColor.

---

## Events

| Std.Ui | Live HTML | Tui | Gui |
|---|---|---|---|
| `Event.onClick Msg` | `data-sky-ev-click="…"` | element becomes focusable; Tab cycles to it; Enter dispatches Msg | gio `pointer.Filter{Kinds: pointer.Press}` → Msg |
| `Event.onSubmit Msg` (form) | `data-sky-ev-submit="…"` | Enter while inside a focused form-descendant dispatches Msg with collected input values | gio key.Filter{Name: key.NameEnter} on form |
| `Event.onInput (\v -> …)` | `data-sky-ev-input="…"` | keystrokes update focused input's buffer; Msg dispatched with current value | gio editor.Editor change events |
| `Event.onChange (\v -> …)` | `data-sky-ev-change="…"` | dispatched on focus loss / Enter | gio editor commit |
| `Event.onFocus Msg` | `data-sky-ev-focus="…"` | dispatched when Tab arrives | gio focus event |
| `Event.onBlur Msg` | `data-sky-ev-blur="…"` | dispatched when Tab leaves | gio focus loss |
| `Event.onKeyDown (\k -> …)` | `data-sky-ev-keydown` | direct mapping (raw key code) | gio key.Event |
| `Event.onMouseOver / onMouseOut` | `data-sky-ev-mouseover` etc | IGNORED (no mouse default) — could enable with terminal mouse mode | gio pointer hover |
| `Event.onFile (\path -> …)` | `data-sky-ev-file="…"` | IGNORED — no file picker in pure Tui | gio file picker (planned) |
| `Event.onImage` (image upload) | `data-sky-ev-image` | IGNORED | gio file picker + image decode |

**Click → Tab+Enter is the central translation.** This is the focus-management work I flagged. The runtime maintains a focus index, computes a tab-order from a tree walk (any element with a click/submit/input/change handler is focusable), and dispatches Tab/Shift-Tab to move it. Enter activates the focused element.

Mouse events in TUI are technically possible (xterm supports `\e[?1000h` for mouse tracking) — option for v2.

---

## Pointer / cursor

| Std.Ui | Live HTML | Tui | Gui |
|---|---|---|---|
| `AttrPointer` | `cursor: pointer` | underline on focus (visual cue that element is interactive) | gio `pointer.CursorPointer` |

---

## Overflow

| Std.Ui | Live HTML | Tui | Gui |
|---|---|---|---|
| `AttrOverflow "clip" "clip"` | `overflow: clip` | content past container box is truncated | gio clip rect |
| `AttrOverflow "scrollbars" "scrollbars"` | `overflow: auto` | scroll-key support: focused element accepts arrow keys to scroll | gio scrollable list |
| `AttrOverflow "clip" "scrollbars"` | mixed | mixed: x clipped, y scrollable | gio clip rect + scroll |

**Scrollable regions in TUI need keyboard hooks.** Not free, but the click→Tab→Enter focus story extends to "focused scroll region accepts Up/Down/PgUp/PgDown."

---

## Description (accessibility regions)

| Std.Ui | Live HTML | Tui | Gui |
|---|---|---|---|
| `DescMain` | `<main>` | no visual change; semantic role for screen readers via Mac VoiceOver-on-Terminal etc | gio a11y semantic role |
| `DescNavigation` | `<nav>` | same | a11y role |
| `DescContentInfo` | `<footer>` | same | a11y role |
| `DescComplementary` | `<aside>` | same | a11y role |
| `DescHeading 1..6` | `<h1>`..`<h6>` | bold + size visual treatment varying with level | gio styled text |
| `DescButton` | `<button>` | already covered by `[ Label ]` rendering for focusable elements | gio button role |
| `DescParagraph` | `<p>` | line break before/after | gio paragraph |
| `DescLabel "name"` | `aria-label="name"` | a11y exposure | a11y exposure |
| `DescLivePolite` / `DescLiveAssertive` | `aria-live="polite/assertive"` | a11y change announcements | a11y announcements |

**Accessibility semantics carry across.** Visual treatment varies (especially headings); accessibility role exposure is consistent.

---

## Nearby (overlays)

| Std.Ui | Live HTML | Tui | Gui |
|---|---|---|---|
| `Above el` | absolute-positioned above | rendered on the row above the parent (or skipped if parent is at top) | gio Stack with Y-offset |
| `Below el` | absolute-positioned below | rendered on the row below | same |
| `OnLeft el` | absolute-positioned left | rendered in cells to the left | same |
| `OnRight el` | absolute-positioned right | rendered in cells to the right | same |
| `InFront el` | z-index: above; same coords | layered render over the element's cells (loses underlying content for that frame) | gio higher z |
| `Behind el` | z-index: below | rendered first, then parent on top | gio lower z |

**Absolute positioning lifts via cell offsets.** Lossy when overlays exceed terminal bounds or overlap meaningfully — TUI fallback: clip to visible cells, document.

---

## Escape hatches (intentionally non-portable)

These exist to give users a way out when typed primitives don't suffice. Using them costs portability — that's the trade.

| Std.Ui | What it is | Behaviour in Tui / Gui |
|---|---|---|
| `AttrStyle "k" "v"` | Raw CSS prop+value | Pattern-match on known props (`opacity`, `transform: translate`); ignore unknown |
| `AttrAttribute "k" "v"` | Raw HTML attribute | Pattern-match on known (`href`, `name`, `for`); ignore unknown |
| `AttrClass "name"` | CSS class | IGNORED in non-HTML backends |
| `Raw vnode` | Embed raw Std.Html | placeholder text in Tui; skip in Gui |

**Document the rule:** "Reach for these only when typed primitives don't have what you need. Code using them won't render correctly outside Sky.Live / Sky.Webview."

---

## Summary: what's clean, what's lossy, what's intentional

**Cleanly portable (the typed core):**
- All Element constructors (Empty, Text, Node, semantic TaggedNodes)
- All Length variants under the logical-pixel canvas model
- Alignment (HAlign, VAlign)
- Padding, spacing
- Colour (Rgba) — lossy on alpha for Tui
- Borders — lossy on rounded + shadow for Tui
- Font weight/italic/underline/colour
- All events (with Tab+Enter focus translation in Tui)
- Description regions (semantic + a11y)
- Nearby overlays
- Overflow (with keyboard scroll support in Tui)

**Intentionally lossy in Tui (visual decoration):**
- Font size (one terminal cell size)
- Font family (terminal font only)
- Letter / word spacing
- Background images / gradients
- Border radius / shadow
- Alpha compositing

**Escape hatches (opt-out portability):**
- AttrStyle (raw CSS)
- AttrAttribute (raw HTML)
- AttrClass (raw CSS class)
- Raw any (raw Std.Html VNode)

**Functionally preserved everywhere:** content, layout structure, events, focus order, semantic meaning, accessibility roles.

The headline: **portability of behaviour, not pixel parity.** A user who sticks to typed primitives gets a UI that works on every backend with appropriate-fidelity rendering. Reaching for an escape hatch is an explicit choice to opt out for that one use.

---

## What this means for the spike

The spike on `exp/tea-core` will implement the **left half of every clean-portable row above** for Tui — enough to validate that the same `view : Model -> Element Msg` renders both as Sky.Live HTML and Sky.Tui character cells without changes.

Specifically, v1 spike scope:
- Element: Empty, Text, Node, TaggedNode (h1-h6, p, button, a, input)
- Length: Px, Content, Fill, Vh, Vw (Min/Max if time)
- Alignment: HAlign + VAlign
- Padding, Spacing
- AttrFontColor, AttrBgColor (Rgba mapping)
- AttrBorderWidth + AttrBorderColor (single line; defer rounded/shadow)
- AttrFontWeight, AttrFontItalic, AttrFontUnderline
- AttrEvent: onClick, onInput, onSubmit (with focus-tab management)

Out of v1 scope (deferred to follow-up commits):
- Images (placeholder only)
- Gradients (ignored)
- Shadows (ignored)
- Mouse events
- Animation transitions
- Sub-grapheme rendering polish

The stopwatch port will exercise: Text, Node, Px, Fill, Spacing, BgColor, FontColor, Border, Event.onClick. About a quarter of the surface — enough to know the architecture holds.

---

## Sources
- Std.Ui module: `sky-stdlib/Std/Ui.sky` (1361 lines, ADT lines 42-95, Length 97-105, Color 128-133)
- Sky.Live HTML renderer: `Std.Ui.renderElement` (line 860 onwards)
- Bubble Tea + lipgloss for prior-art on cell-grid layout in Go
- gio's layout package for prior-art on flex-style portion division
