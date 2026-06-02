# Std.Ui overview

> **v0.15 state**: type-directed lowering across callback fields,
> record-field inits, list elements, and call args; Go generics on
> parametric record aliases. The codegen pain in **Limitation #18**
> (helper params with concrete-Msg `(String -> Msg)` previously
> mono-mised to `func(string) any`) is **closed in v0.15** — extract
> helpers freely. Whole-program Sky DCE prunes unused FFI bindings.
> LSP 100 % coverage; runtime verification across all 27 examples.
> See [`../compiler/journey.md`](../compiler/journey.md) for the
> changelog.


**A typed, no-CSS layout DSL for Sky.Live.** Build a UI from typed primitives (`el`, `row`, `column`, `paragraph`, `textColumn`) and typed attributes (`Background.color`, `Border.rounded`, `Font.size`, `Region.heading`) — Std.Ui renders to inline-styled HTML on the server side and Sky.Live's wire ferries diffs to the browser. No CSS files. No template languages. No client framework.

> Std.Ui's API surface adopts conventions from prior typed-layout DSLs in the Elm community. Implementation, runtime, and code generator are independent Sky / Haskell work — see [NOTICE.md](../../NOTICE.md) for full attribution.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Live exposing (app, route)
import Std.Ui as Ui
import Std.Ui exposing (Element)
import Std.Ui.Background as Background
import Std.Ui.Border as Border
import Std.Ui.Font as Font


type alias Model = { count : Int }
type Msg = Increment | Decrement


init : a -> ( Model, Cmd Msg )
init _ = ( { count = 0 }, Cmd.none )


update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Increment -> ( { model | count = model.count + 1 }, Cmd.none )
        Decrement -> ( { model | count = model.count - 1 }, Cmd.none )


view : Model -> any
view model =
    Ui.layout []
        (Ui.row
            [ Ui.spacing 12
            , Ui.padding 16
            , Background.color (Ui.rgb 255 102 0)
            , Font.color (Ui.rgb 255 255 255)
            , Border.rounded 4
            ]
            [ Ui.button [] { onPress = Just Decrement, label = Ui.text "−" }
            , Ui.el [ Font.size 24, Font.bold ] (Ui.text (String.fromInt model.count))
            , Ui.button [] { onPress = Just Increment, label = Ui.text "+" }
            ])


subscriptions _ = Sub.none

main = app { init = init, update = update, view = view, subscriptions = subscriptions, routes = [], notFound = () }
```

That's the whole picture: every visual element is an `Element msg`, every styling/layout decision is an `Attribute msg`, and the layout function `Ui.layout` produces the value Sky.Live's `view` field expects.

## Why it exists

The default Sky.Live view layer (`Std.Html` + `Std.Css`) is a near-1:1 binding to HTML elements and CSS properties. That's the right primitive — but most apps don't *want* to think about HTML semantics, BFC quirks, flexbox direction inheritance, or whether a particular tag is block/inline by default. They want to say "two things side by side with 12px gap" and have it work.

Std.Ui takes a different cut: model layout in terms the user actually wants (`row`, `column`, `el`, `padding`, `spacing`, alignment), and emit the right HTML+CSS automatically. No more "why is my flex child not centering" — `centerY` does centering and the underlying `align-self: center` is an implementation detail.

## The mental model

| Concept | Type | Examples |
|---|---|---|
| **Element** | `Element msg` | `Ui.text "hi"`, `Ui.row [...] [...]`, `Ui.button [...] cfg` |
| **Attribute** | `Attribute msg` | `Ui.padding 16`, `Background.color (Ui.rgb 0 0 0)`, `Ui.onClick MyMsg` |
| **Length** | `Length` | `Ui.px 200`, `Ui.fill`, `Ui.fillPortion 2`, `Ui.content`, `Ui.minimum 100 Ui.fill`, `Ui.maximum 600 Ui.fill` |
| **Color** | `Color` | `Ui.rgb 255 102 0`, `Ui.rgba 0 0 0 0.5`, `Ui.white`, `Ui.black` |

Every `Element msg` has a `msg` parameter — the same `msg` you've defined for your TEA app. Attributes that carry events (`onClick`, `onSubmit`, `onInput`) tie into the same `msg` so the type checker catches mismatches at compile time.

The `Ui.layout` function takes the root element and produces an `any` that Sky.Live's `view` field accepts. Wrap your top-level view in it.

## Layout primitives

```elm
Ui.el      [Attr] (Element)            -- single element (renders as <div>)
Ui.row     [Attr] [Element]            -- horizontal flex container
Ui.column  [Attr] [Element]            -- vertical flex container
Ui.wrappedRow [Attr] [Element]         -- like row, but children that don't
                                       --   fit wrap to a new line
                                       --   (CSS flex-wrap: wrap)
Ui.grid       [Attr] [Element]         -- CSS-Grid auto-fit container.
                                       --   Set min column width with
                                       --   `Ui.gridColumns N`. Use this
                                       --   (NOT wrappedRow) for product
                                       --   grids / image galleries —
                                       --   wrappedRow's flex-basis: auto
                                       --   collapses to 1-per-row when
                                       --   children contain <img>.
Ui.paragraph [Attr] [Element]          -- inline text flow with wrapping
Ui.textColumn [Attr] [Element]         -- vertical text-flow column
Ui.text   String                       -- bare text (no wrapping element)
Ui.none                                -- empty placeholder (`Element msg`)
```

`row` and `column` use flexbox under the hood, with `gap` driven by `Ui.spacing`. The default flex direction matches the helper name. Mix freely:

```elm
Ui.column [ Ui.spacing 16, Ui.padding 24 ]
    [ Ui.row [ Ui.spacing 8 ]
        [ Ui.text "Name:", Ui.text userName ]
    , Ui.row [ Ui.spacing 8 ]
        [ Ui.text "Score:", Ui.text (String.fromInt score) ]
    ]
```

### `Ui.grid` — CSS-Grid auto-fit (product cards, dashboards, galleries)

`Ui.wrappedRow` (CSS flexbox `flex-wrap: wrap`) is fine for flowing
text-sized children. But for **card-like children that contain `<img
width:100%>`**, flexbox's `flex-basis: auto` collapses each child to
100% of the container — every card ends up alone on its row regardless
of viewport width. That's the classic "flex vs intrinsic-sized image"
problem.

`Ui.grid` is the right primitive for that shape. It compiles to:

```css
display: grid;
grid-template-columns: repeat(auto-fill, minmax(<minWidth>px, 1fr));
gap: <Ui.spacing>px;
```

Children become grid items. Drop `Ui.width` from card-style children
and let the grid handle sizing — `minmax(<minWidth>, 1fr)` guarantees
each cell is at least `<minWidth>` and at most `1fr` of the remaining
space, with the row count adapting to viewport width automatically.

```elm
Ui.grid
    [ Ui.gridColumns 240   -- minmax(240px, 1fr)
    , Ui.spacing 16        -- gap: 16px
    , Ui.padding 24
    ]
    (List.map productCard products)
```

`Ui.gridColumns N` sets the minimum column width in pixels. Defaults
to `240px` if omitted (sensible product-card default — prevents a
totally-broken single-column fallback when the attribute is forgotten).

`Ui.spacing N` works as the gap (CSS Grid honours the `gap` property
natively, same as flexbox).

### `Std.Ui.Grid` — typed track lists (sidebars, content-aware columns)

`Ui.gridColumns` is great for product-card grids where every track
has the same minimum width. For **sidebar layouts** (`1fr 200px 1fr`),
**content-aware columns** (`auto 1fr`), or **`repeat(auto-fit,
minmax(<px>, 1fr))` card grids that re-flow on resize**, reach for
`Std.Ui.Grid`. The typed `Track` ADT spells out the exact CSS Grid
track-list, then `Grid.columns` / `Grid.rows` / `Grid.tracks` attach
it to a `Ui.grid` container.

```elm
import Std.Ui as Ui
import Std.Ui.Grid as Grid

Ui.grid
    [ Ui.width Ui.fill
    , Grid.columns
        [ Grid.repeatAutoFit (Grid.minmax (Grid.px 240) (Grid.fr 1)) ]
    , Ui.spacing 16
    ]
    (List.map productCard products)

-- Sidebar layout:
Ui.grid
    [ Ui.width Ui.fill
    , Grid.columns [ Grid.fr 1, Grid.px 200, Grid.fr 1 ]
    ]
    [ leftPane, mainPane, rightPane ]

-- Header + body + footer rows:
Ui.grid
    [ Ui.width Ui.fill
    , Grid.tracks
        [ Grid.auto, Grid.fr 1 ]
        [ Grid.px 60, Grid.fr 1, Grid.px 40 ]
    ]
    [ header, body, footer ]
```

**Track constructors** (every variant lowers to its idiomatic CSS):

| Sky | CSS | Use case |
|---|---|---|
| `Grid.fr N` | `Nfr` | Flexible track, proportional to other `fr` |
| `Grid.px N` | `Npx` | Fixed-width pixel track |
| `Grid.auto` | `auto` | Track hugs its content |
| `Grid.minContent` | `min-content` | Track shrinks to smallest non-overflowing size |
| `Grid.maxContent` | `max-content` | Track grows to content's preferred width |
| `Grid.minmax lo hi` | `minmax(lo, hi)` | Bounded — e.g. `minmax (px 240) (fr 1)` |
| `Grid.repeat N t` | `repeat(N, t)` | Repeat a track N times |
| `Grid.repeatAutoFit t` | `repeat(auto-fit, t)` | Re-flowing card grid (empty tracks collapse) |
| `Grid.repeatAutoFill t` | `repeat(auto-fill, t)` | Re-flowing grid that keeps ghost slots |

**`gridColumns` vs `Grid.columns` — when to pick which**

| Need | Reach for |
|---|---|
| Product-card grid (all tracks same min-width) | `Ui.gridColumns N` (lighter, default) |
| Sidebar shells, header rows, mixed track types | `Grid.tracks` / `Grid.columns` |
| Content-aware tracks (`auto` / `min-content`) | `Grid.columns` |
| Both column + row axes set explicitly | `Grid.tracks cols rows` |
| Responsive card grids that must `auto-fit minmax` | `Grid.columns [ Grid.repeatAutoFit … ]` |

Both compile to inline `grid-template-*` declarations — no runtime
injection pass, no model state. Sky.Tui falls back to column stacking
(it can't draw a 2-D grid in ANSI cells); Sky.Webview honours the
grid identically to Sky.Live.

### `Ui.aspectRatio` — proportional sizing (16:9, 1:1, 2.35:1)

Lock an element to a fixed width-to-height ratio. Pair with
`Ui.width Ui.fill` (or a fixed pixel width) — the browser's
`aspect-ratio` solver fills in the unset axis. Indispensable for
video embeds, image galleries, hero banners, avatar tiles, square
product images.

```elm
import Std.Ui as Ui

-- Decimal form — `aspect-ratio: 1.777`
Ui.el [ Ui.width Ui.fill, Ui.aspectRatio 1.777 ] videoPlaceholder

-- Integer-pair form — `aspect-ratio: 16 / 9` (more readable)
Ui.el [ Ui.width Ui.fill, Ui.aspectRatioWH 16 9 ] videoPlaceholder

-- Convenience aliases for common ratios:
Ui.el [ Ui.width (Ui.px 100), Ui.square ] avatar           -- 1:1
Ui.el [ Ui.width Ui.fill, Ui.widescreen ] heroBanner       -- 16:9
Ui.el [ Ui.width Ui.fill, Ui.fullHd ] heroBanner           -- 16:9 (alias)
Ui.el [ Ui.width Ui.fill, Ui.cinemascope ] cinemaBanner    -- 2.35:1
```

| Helper | CSS emitted | Common case |
|---|---|---|
| `Ui.aspectRatio Float` | `aspect-ratio: <r>` | Custom decimal ratio |
| `Ui.aspectRatioWH Int Int` | `aspect-ratio: <w> / <h>` | Standard ratios (4:3, 16:9, 2:3, …) |
| `Ui.square` | `aspect-ratio: 1 / 1` | Avatars, product tiles |
| `Ui.widescreen` / `Ui.fullHd` | `aspect-ratio: 16 / 9` | Video embeds, HDTV |
| `Ui.cinemascope` | `aspect-ratio: 2.35` | Hero banners, cinema |

The browser resizes the unset axis on every viewport change — no
re-render needed, no observer to wire up. Sky.Tui ignores the
property (ANSI cells don't have an aspect-ratio concept); Sky.Webview
honours it via the embedded WebKit/Chromium engine.

## Length

```elm
Ui.px : Int -> Length                   -- absolute pixels
Ui.fill : Length                        -- single growing slot (no arg)
Ui.fillPortion : Int -> Length          -- proportional flex-grow weight
Ui.content : Length                     -- shrink-to-fit
Ui.shrink : Length                      -- shrink to content size
Ui.minimum : Int -> Length -> Length    -- minimum constraint on a length
Ui.maximum : Int -> Length -> Length    -- maximum constraint on a length
Ui.vh : Int -> Length                   -- viewport-height percent (1..100)
Ui.vw : Int -> Length                   -- viewport-width percent  (1..100)
```

Use with `Ui.width` / `Ui.height`:

```elm
Ui.row [ Ui.spacing 8 ]
    [ Ui.el [ Ui.width (Ui.px 80) ] (Ui.text "Label:")
    , Ui.el [ Ui.width Ui.fill ] (Ui.text fieldValue)            -- fills remaining
    , Ui.el [ Ui.width (Ui.fillPortion 2) ] (Ui.text "double")   -- 2× fillPortion sibling
    , Ui.el [ Ui.width (Ui.maximum 320 Ui.fill) ] (Ui.text "capped")
    , Ui.el [ Ui.width (Ui.px 32) ] (Ui.text "✓")
    ]

-- Viewport-relative: full-page shells, hero sections, modals
Ui.column
    [ Ui.height (Ui.vh 100)             -- min-height: 100vh shell
    , Ui.width (Ui.vw 100)
    ]
    [ heroSection
    , content
    , footer
    ]
```

### `Ui.fill` — how it lowers (v0.15.55+, refined in v0.15.56)

`Ui.fill` lowers asymmetrically per the parent's flex direction:

| Position | Emitted CSS |
|---|---|
| Main-axis fill (e.g. `Ui.width Ui.fill` on a `Ui.row` child, `Ui.height Ui.fill` on a `Ui.column` child) | `flex-grow: N; min-{w,h}: 0;` |
| Cross-axis HEIGHT fill (`Ui.height Ui.fill` on a `Ui.row` child) | nothing — relies on flex's default `align-items: stretch` |
| Cross-axis WIDTH fill (`Ui.width Ui.fill` on a `Ui.column` / `Ui.el` / `Ui.textColumn` child) | `width: 100%;` |

The asymmetry isn't sloppy — it closes a real bug class. CSS
Flexbox §9.8 resolves `%` lengths against a parent's USED size
only when that size is "definite"; a flex-grow-derived size is
indefinite for the purpose of `%` resolution on cross-axis
children. Row parents commonly have indefinite heights (no
`Ui.height` attr or a grown-via-flex parent), so emitting
`height: 100%` on the cross-axis previously collapsed every
fill-height child to text-content height (issue #63 — three-pane
app shell, Input.multiline). With the explicit `100%` stripped,
the flex default `align-items: stretch` handles cross-axis fill
correctly under both definite and indefinite parents.

The width axis keeps its explicit `100%` because column-parent
widths are typically definite (block elements inherit width from
`<body>` / viewport), AND `width: 100%` survives the `centerX`
cascade so `[Ui.width fill, Ui.centerX]` and `[Ui.width (Ui.maximum
N Ui.fill), Ui.centerX]` (the canonical centred-page-content
shape) still fill width before centring within the max-width cap.

### `align-self` — single-emission contract (v0.15.56 F4)

Before v0.15.56 the cross-axis fill emitters AND the alignment
emitters (`alignSelfX/Y`) both wrote `align-self` declarations,
producing two declarations on the same element when both attrs
were present (`[Ui.width Ui.fill, Ui.centerX]`). Cascade-last
gave the visible-correct result but the rendering was order-
dependent — fragile against attr re-ordering or future CSS
engine work.

v0.15.56 F4 strips the redundant `align-self: stretch` from the
cross-axis fill emitters. `stretch` is the default `align-items`
value, so emitting it explicitly was a no-op; default behaviour
still applies when no other `align-self` is emitted. Post-F4
contract: at most ONE `align-self` declaration per element,
sourced from `alignSelfX/Y` only.

User-visible effect: identical rendering to v0.15.55. The change
is code-hygiene: clean cascade, explicit precedence (alignment
attrs always win over implicit fill-stretch), no ordering
ambiguity.

### `Ui.layoutWith` — wrapper customisation (v0.15.56)

`Ui.layout` builds an outer `<div>` wrapper around your root —
viewport-tall (`min-height: 100vh`), flex column. Apps that want
to reach the wrapper itself (page-wide `Background.color` for
dark mode, `Font.color` / `Font.family` cascading to every
descendant, raw style overrides for the wrapper's flex
direction) use the additive `Ui.layoutWith` entry point:

```elm
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Ui.Font as Font

view model =
    Ui.layoutWith
        { wrapperAttrs =
            [ Background.color (Ui.rgb 18 18 24)
            , Font.color (Ui.rgb 240 240 240)
            , Font.family "system-ui, -apple-system, sans-serif"
            ]
        , rootAttrs =
            [ Ui.padding 16
            , Ui.width Ui.fill
            ]
        }
        (Ui.column
            [ Ui.spacing 16 ]
            [ header, mainBody, footer ])
```

| Attr list | Reaches |
|---|---|
| `wrapperAttrs` | The outer 100 vh `<div>` (the page-tall flex floor). Background colours paint the whole viewport; Font.color / Font.family cascade to every descendant; Border / class / aria-* / data-* attach to the wrapper directly. |
| `rootAttrs` | The root element rendered under the wrapper (same as `Ui.layout`'s arg). `Ui.width` / `Ui.height` / `Ui.padding` / `Ui.spacing` etc. apply here. |

`Ui.layout attrs el` is equivalent to `Ui.layoutWith {
wrapperAttrs = [], rootAttrs = attrs } el` — byte-identical for
existing call sites.

## Alignment + spacing + padding

```elm
Ui.alignLeft / alignRight                -- horizontal alignment within parent
Ui.alignTop / alignBottom                -- vertical alignment within parent
Ui.centerX / centerY                     -- centering within parent
Ui.spacing : Int -> Attribute msg        -- gap between children of row/column
Ui.padding : Int -> Attribute msg        -- uniform padding (all four sides)
Ui.pointer                                -- cursor: pointer (use on clickable els)
```

## Colours

```elm
Ui.rgb 255 102 0                          -- 0-255 integer channels
Ui.rgba 255 102 0 0.5                     -- 0-255 RGB + 0-1 alpha
Ui.white / Ui.black / Ui.transparent     -- handy constants
```

Sky.Ui's `Color` stores 0-255 integers internally (Sky's HM has friction with [0,1] floats round-tripping through CSS). The `rgb`/`rgb255` helpers both use the integer form; the alpha channel stays a Float.

## Background, Border, Font, Region

Modular attribute helpers, all in their own sub-module so the import surface is explicit:

```elm
import Std.Ui.Background as Background
import Std.Ui.Border as Border
import Std.Ui.Font as Font
import Std.Ui.Region as Region

Background.color (Ui.rgb 246 246 240)
Border.color (Ui.rgb 230 230 230)
Border.width 1
Border.rounded 4
Font.color (Ui.rgb 33 33 33)
Font.family "Verdana, Geneva, sans-serif"
Font.size 14
Font.bold
Font.alignCenter                         -- text-align: center (also Font.center)
Region.heading 2                         -- semantic <h2> for screen readers
Region.footer
```

These are all `Attribute msg` — they go in the attribute list of any element.

## Buttons + form inputs

```elm
Ui.button : List (Attribute msg) -> { onPress : Maybe msg, label : Element msg } -> Element msg
Ui.input  : List (Attribute msg) -> Element msg     -- void <input> element
Ui.form   : List (Attribute msg) -> List (Element msg) -> Element msg
```

A button:
```elm
Ui.button
    [ Background.color (Ui.rgb 255 102 0)
    , Font.color (Ui.rgb 255 255 255)
    , Border.rounded 3
    , Ui.padding 6
    ]
    { onPress = Just LoginSubmit, label = Ui.text "sign in" }
```

`onPress = Nothing` renders the button with `disabled="true"`.

A free-standing text input (real `<input>`, not a `<div>` with bogus type/value attrs — that's what `Ui.el` would produce):
```elm
Ui.input
    [ Ui.htmlAttribute "type" "text"
    , Ui.htmlAttribute "value" model.draft
    , Ui.onInput DraftChanged          -- DraftChanged : String -> Msg
    , Border.width 1
    , Ui.padding 6
    ]
```

## Typed events

Event handlers are typed:

```elm
Ui.onClick    : msg -> Attribute msg
Ui.onSubmit   : msg -> Attribute msg
Ui.onInput    : (String -> msg) -> Attribute msg     -- typed callback
Ui.onChange   : (String -> msg) -> Attribute msg
Ui.onFocus / onMouseOver / onMouseOut / onKeyDown   : msg -> Attribute msg
Ui.onFile     : (String -> msg) -> Attribute msg     -- file upload (data URL)
Ui.onImage    : (String -> msg) -> Attribute msg     -- image upload + browser-side resize
```

The `(String -> msg)` shape on `onInput` etc. is important: at the wire layer Sky.Live ships the typed input value, and the typed callback shape lets the HM type-checker verify the wrapper at the call site. Pass a Msg constructor that takes a String (`type Msg = ... | DraftChanged String | ...`).

## Forms — the "password best-practice" pattern

For password fields (and any sensitive input — API keys, credit cards, tokens), wrap inputs in a `Ui.form` and dispatch on `onSubmit` with a typed record. **Do not** wire `onInput` on a password field — every keystroke would dispatch the secret to the server, where it ends up in the session store on every render.

```elm
type alias LoginForm =
    { username : String
    , password : String
    }


type Msg = ... | DoSignIn LoginForm | ...


loginView : Model -> Element Msg
loginView model =
    Ui.form [ Ui.onSubmit DoSignIn ]
        [ Ui.column [ Ui.spacing 12 ]
            [ Ui.input
                [ Ui.htmlAttribute "type" "text"
                , Ui.name "username"            -- formData key
                ]
            , Ui.input
                -- Password field — no `value` attr (don't round-trip the
                -- secret through DOM), no `onInput` (don't dispatch per
                -- keystroke). Submit-only.
                [ Ui.htmlAttribute "type" "password"
                , Ui.name "password"
                ]
            , Ui.input
                [ Ui.htmlAttribute "type" "submit"
                , Ui.htmlAttribute "value" "sign in"
                ]
            ]
        ]
```

When the form submits, Sky.Live ships the formData `{"username": "...", "password": "..."}` as the args to `DoSignIn`. The wire driver decodes the JSON directly into `LoginForm` via case-insensitive `json.Unmarshal` — Sky's lowercase field names land in the matching Go fields without per-Msg decoder boilerplate.

Three concrete wins from this pattern over per-keystroke `onInput`:

1. **Password manager extensions** (1Password, Bitwarden, browser autofill) stop seeing DOM mutation re-prompts on every render.
2. **The secret stays out of Model** — it lives only in the browser DOM until form submit, then briefly in the Msg's record arg until `update` consumes it. Without this pattern it would round-trip through every Sky.Live session-store write (Redis / Postgres / Firestore).
3. **Race-free submit** — reads the live DOM value, not a debounced keystroke. No possibility of dropping the last character if the user hits Enter before the 150 ms debounce settles.

## File / image upload

Same wire shape as `onInput`, but the JS driver reads a file from `<input type="file">` and ships a base64 data URL as the typed callback's `String` argument.

```elm
type Msg = ... | AvatarSelected String | DocSelected String | ...


view model =
    Ui.column [ Ui.spacing 12 ]
        [ -- Image upload — auto-resizes to fileMaxWidth × Height before
          -- upload. Re-encodes as JPEG @ 0.85 quality. Saves bandwidth on
          -- large camera-roll photos.
          Ui.input
            [ Ui.htmlAttribute "type" "file"
            , Ui.htmlAttribute "accept" "image/*"
            , Ui.onImage AvatarSelected
            , Ui.fileMaxSize   2_000_000      -- 2MB browser-side cap
            , Ui.fileMaxWidth  800
            , Ui.fileMaxHeight 800
            ]

        , -- Generic file upload — sends raw data URL, no resize.
          Ui.input
            [ Ui.htmlAttribute "type" "file"
            , Ui.htmlAttribute "accept" ".pdf,.txt"
            , Ui.onFile DocSelected
            , Ui.fileMaxSize 5_000_000
            ]
        ]
```

The data URL carries the MIME type (`data:image/jpeg;base64,...` or `data:application/pdf;base64,...`). Decode with `Std.Encoding.base64Decode` if you need raw bytes; route to `Http.post` for upload to a backend. Note: `Ui.fileMaxSize` is a UX guard, not a security boundary — Sky.Live caps the wire payload at `[live] maxBodyBytes` (default 5 MiB) and your server should still validate.

## Lazy + Keyed

```elm
import Std.Ui.Lazy as Lazy
import Std.Ui.Keyed as Keyed

Lazy.lazy renderItem item               -- LRU-cached subtree (function-pointer + args fingerprint)
Lazy.lazy2 renderRow username item      -- 2-arg variant; lazy3..lazy5 too
Keyed.column [ Ui.spacing 8 ]
    [ ( "row-" ++ String.fromInt item.id, renderRow item )
    , ...
    ]
```

`Lazy` currently no-ops (the wrapper is in place; runtime memoisation is deferred). `Keyed.*` emits the `sky-key` attribute so Sky.Live's diff algorithm can identify children across re-renders.

## Responsive

```elm
import Std.Ui.Responsive as Responsive

Responsive.classifyDevice viewportWidth     -- Phone | Tablet | Desktop | BigDesktop
Responsive.adapt viewport
    { phone   = mobileLayout
    , tablet  = tabletLayout
    , desktop = desktopLayout
    }
```

`Std.Ui.Responsive` is the **Model-driven** path: feed the viewport size in via `Sub.windowSize`, branch in your `view` function, dispatch a Msg when the layout changes. Useful when the layout transition needs to fire a typed event (e.g. close a tray, refit a canvas).

For **CSS-driven** viewport-conditional styling — instant, no JS, no Model field, no re-render — use the **media-query primitive** below.

## Media queries + breakpoints

`Ui.mediaQuery` + `Ui.breakpoint` express viewport-conditional styling in pure typed Sky. The CSS engine handles reactivity natively — instant, no JS round-trip, no model field, no re-render when the viewport crosses the breakpoint.

```elm
import Std.Ui as Ui
import Std.Ui.Background as Background


view : Model -> any
view _ =
    Ui.layout []
        (Ui.row
            [ Ui.spacing 16, Ui.padding 16 ]
            [ -- Typed-constant breakpoint: stacks vertically + red bg
              -- ONLY when viewport ≤ 767 px wide. Above the breakpoint
              -- the wrapper keeps its base layout (none here).
              Ui.breakpoint Ui.mobile
                  [ Ui.htmlAttribute "style" "flex-direction: column;"
                  , Background.color (Ui.rgb 240 0 0)
                  ]
                  sidebar

              -- Escape hatch: any raw CSS media-query string. The
              -- caller owns query correctness.
            , Ui.mediaQuery "(prefers-color-scheme: dark)"
                  [ Background.color (Ui.rgb 18 18 24) ]
                  main
            ])
```

### `Ui.breakpoint : Breakpoint -> List (Attribute msg) -> Element msg -> Element msg`

Typed constants covering 95 % of cases. Defaults follow Tailwind cuts so AI-generated Sky lines up with the most-common mental model.

| Breakpoint | CSS query | Typical use |
|---|---|---|
| `Ui.mobile` | `(max-width: 767px)` | phone-only overrides |
| `Ui.tablet` | `(min-width: 768px) and (max-width: 1023px)` | mid-size styling |
| `Ui.desktop` | `(min-width: 1024px)` | desktop-only |
| `Ui.smAndUp` | `(min-width: 640px)` | Tailwind `sm` cut |
| `Ui.mdAndUp` | `(min-width: 768px)` | Tailwind `md` cut |
| `Ui.lgAndUp` | `(min-width: 1024px)` | Tailwind `lg` cut |
| `Ui.xlAndUp` | `(min-width: 1280px)` | Tailwind `xl` cut |
| `Ui.darkMode` | `(prefers-color-scheme: dark)` | dark-theme overrides |
| `Ui.lightMode` | `(prefers-color-scheme: light)` | light-theme overrides |
| `Ui.reducedMotion` | `(prefers-reduced-motion: reduce)` | suppress transitions |
| `Ui.touchDevice` | `(hover: none) and (pointer: coarse)` | touch-first UI |
| `Ui.portrait` | `(orientation: portrait)` | tall layouts |
| `Ui.landscape` | `(orientation: landscape)` | wide layouts |
| `Ui.Custom minPx maxPx` | `(min-width: <minPx>px) and (max-width: <maxPx>px)` (or one bound when the other is `0`) | custom ranges |

### `Ui.mediaQuery : String -> List (Attribute msg) -> Element msg -> Element msg`

Escape hatch — any raw CSS media-query string. Use when no typed `Breakpoint` covers the case (`(orientation: portrait)`, `(min-resolution: 2dppx)`, `(forced-colors: active)`, etc.). The string is emitted verbatim inside `@media <q> { ... }`; the caller owns correctness.

### Composition

Nested breakpoints stack — each call wraps a fresh `<div>` with its own scoped `<style>` block. CSS rules match independently.

```elm
-- Stacks two media-query overrides on the same content.
Ui.breakpoint Ui.mobile
    [ Background.color (Ui.rgb 240 0 0) ]
    (Ui.breakpoint Ui.darkMode
        [ Background.color (Ui.rgb 18 18 24) ]
        content)
```

### What renders on the wire

`Ui.breakpoint Ui.mobile [ Ui.padding 8 ] child` lowers to:

```html
<div sky-id="r.0.2#div" style="display: flex; flex-direction: column;">
    <style data-sky-mq="r.0.2#div">
        @media (max-width: 767px) {
            [sky-id="r.0.2#div"] { padding: 8px 8px 8px 8px; }
        }
    </style>
    <!-- child content -->
</div>
```

The selector keys off the wrapper's runtime-assigned `sky-id` — so two breakpoints on the same page cannot cross-contaminate each other's rules. Sky.Tui silently ignores the injected `<style>` (terminal renders the base layer only); Sky.Webview honours media queries identically to Sky.Live because they share the runtime VNode pipeline.

### When to pick `Ui.breakpoint` vs `Std.Ui.Responsive`

| Use case | Pick |
|---|---|
| Layout differs by viewport, no Msg needed | `Ui.breakpoint` (no Model field, no re-render) |
| Layout-transition fires a typed Msg (close tray on mobile, refit canvas, re-fetch tile grid) | `Std.Ui.Responsive` (Model-driven via `Sub.windowSize`) |
| Both — visual override + Msg | Combine: `Ui.breakpoint` for the styling, `Sub.windowSize` for the Msg |

## Pseudo-classes (hover, focus, active, disabled)

`Background.hoverColor` / `Font.focusColor` / `Border.activeColor` (and friends) attach `:hover` / `:focus-visible` / `:active` / `:disabled` styling directly on an element — no `onMouseOver` Msg, no Model field, no re-render. The CSS engine handles the state transition natively.

```elm
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Ui.Border as Border
import Std.Ui.Font as Font

view : Model -> Element Msg
view _ =
    Ui.layout []
        (Ui.button
            [ Ui.padding 12
            , Background.color (Ui.rgb 0 122 255)
            , Background.hoverColor (Ui.rgb 0 92 215)     -- pointer over
            , Background.activeColor (Ui.rgb 0 62 175)    -- click down
            , Border.rounded 6
            , Border.hoverRounded 12                       -- morph corners on hover
            , Font.color Ui.white
            ]
            { onPress = Just Save, label = Ui.text "Save" })
```

### Per-sub-module helpers

| Module | Helpers |
|---|---|
| `Std.Ui.Background` | `hoverColor`, `focusColor`, `focusVisibleColor`, `activeColor`, `disabledColor` |
| `Std.Ui.Border` | `hoverColor`, `focusColor`, `focusVisibleColor`, `activeColor`, `hoverWidth`, `hoverRounded` |
| `Std.Ui.Font` | `hoverColor`, `focusColor`, `focusVisibleColor`, `activeColor`, `disabledColor`, `hoverSize` |

### Generic escape hatch — `Ui.onPseudo`

For selector combinations no sub-module helper covers:

```elm
Ui.button
    [ Ui.onPseudo Ui.hover [ Background.color red, Font.size 18 ]
    , Ui.onPseudo Ui.focusVisible [ Border.color blue, Border.width 2 ]
    ]
    { onPress = Just Save, label = Ui.text "Save" }
```

`Ui.PseudoClass` constructors: `Ui.hover`, `Ui.focus`, `Ui.focusVisible`, `Ui.active`, `Ui.disabled`.

### `:focus-visible` vs `:focus` — the safer default

`focusColor` in every sub-module targets **`:focus-visible`** (not `:focus`). Why: `:focus` fires on every click as well as keyboard nav, so click-induced focus rings paint on every interaction — visual noise users perceive as "broken". `:focus-visible` only fires when the browser thinks the user is navigating via keyboard, so the ring appears for accessibility users + disappears for pointer users.

Explicit alternatives:

* `Background.focusVisibleColor c` — spelled-out form (same behaviour as `focusColor`).
* `Ui.onPseudo Ui.focus [...]` — opt into the sticky-focus variant when you explicitly want click-induced rings (rare).

### Touch-device safety — `@media (hover: hover)` auto-gating

`:hover` rules are automatically wrapped in `@media (hover: hover)` by the runtime so they don't fire as sticky-hover on touch devices (the classic mobile bug where a tap leaves a button stuck in the hover colour until the next tap elsewhere). User code never needs to think about this — the runtime handles it.

```css
/* What the runtime emits for Background.hoverColor: */
@media (hover: hover) {
    [sky-id="r.0.2#button"]:hover { background-color: rgba(0, 92, 215, 1); }
}

/* But :focus-visible / :active / :disabled are NOT gated — they apply on every device: */
[sky-id="r.0.2#button"]:focus-visible { border-color: rgba(0, 122, 255, 1); }
[sky-id="r.0.2#button"]:active { background-color: rgba(0, 62, 175, 1); }
```

### Void-element pseudo-class hoist (v0.15.57+ — #409)

Pseudo-class rules attached to a VOID HTML element (`<input>`, `<img>`, `<br>`, `<hr>`, etc.) now render correctly. Pre-v0.15.57 the runtime prepended the `<style>` block as a first CHILD of the element carrying the rule — fine for `<div>` / `<button>`, but silently dropped on void tags because `renderVNode` skips children for void elements (the self-closing `/>` ends the tag).

Post-v0.15.57: the runtime hoists the `<style>` block to a SIBLING slot immediately AFTER the void element. The CSS selector still keys off the void element's `sky-id`, so the rule applies correctly.

```elm
-- Both styles work identically post-v0.15.57:
Input.text
    [ Background.color (Ui.rgb 240 240 240)
    , Background.activeColor (Ui.rgb 200 100 50)   -- :active works on <input>
    , Background.hoverColor (Ui.rgb 50 50 200)     -- @media (hover: hover) gate
    ]
    { onChange = UpdateText, text = m.text, ... }

Ui.image
    [ Border.activeColor (Ui.rgb 0 122 255) ]      -- :active works on <img>
    { src = "logo.png", description = "logo" }
```

The fix applies uniformly to all four style-injection passes (pseudo-class, animation, transition, media-query), so `Std.Ui.Animation.attribute` / `Std.Ui.Transition.attribute` / `Ui.breakpoint` all work on void elements too.

### Composition with `Ui.breakpoint`

`Background.hoverColor` inside `Ui.breakpoint Ui.mobile [...]` works as expected — the breakpoint wraps the element and the pseudo-rule attaches to the element itself; both layers stack via CSS inheritance. Each layer gets its own scoped `<style>` block, so neither cross-contaminates.

```elm
Ui.breakpoint Ui.mobile
    [ Ui.padding 24 ]
    (Ui.button
        [ Background.color (Ui.rgb 0 122 255)
        , Background.hoverColor (Ui.rgb 0 92 215)
        ]
        { onPress = Just Save, label = Ui.text "Save" })
```

### What renders on the wire

`Background.hoverColor` attaches a `data-sky-pc-rules` marker to the element. The runtime injects a sky-id-scoped `<style>` child:

```html
<button sky-id="r.0.2#button" style="...base styles...">
    <style data-sky-pc="r.0.2#button">
        @media (hover: hover) {
            [sky-id="r.0.2#button"]:hover { background-color: rgba(0, 92, 215, 1); }
        }
        [sky-id="r.0.2#button"]:active { background-color: rgba(0, 62, 175, 1); }
    </style>
    Save
</button>
```

The selector keys off the runtime-assigned `sky-id`, so multiple pseudo-rules on the same page cannot cross-contaminate. Sky.Tui silently ignores the injected `<style>` (terminal renders the base layer only); Sky.Webview honours pseudo-classes identically to Sky.Live because they share the runtime VNode pipeline.

## Transitions + animations

`Transition.attribute` + `Animation.attribute` (in `Std.Ui.Transition` / `Std.Ui.Animation`) declare CSS transitions and keyframe animations on a Sky.Ui element. Both are CSS-driven — the browser handles the frame timing, no JS round-trip, no model field, no re-render.

**`prefers-reduced-motion` is respected by default.** Every transition + animation rule is auto-wrapped in `@media (prefers-reduced-motion: no-preference) { ... }` by the runtime, so users who've opted out of motion in their OS get a static UI. This is non-negotiable for a11y. Opt OUT explicitly via `Transition.attributeUnsafe` or `respectReducedMotion = False` on an `Animation.Spec` ONLY when motion is semantically required (loading spinner, progress indicator).

### Transitions

```elm
import Std.Ui as Ui
import Std.Ui.Background as Background
import Std.Ui.Transition as Transition

view : Model -> Element Msg
view _ =
    Ui.layout []
        (Ui.button
            [ Background.color (Ui.rgb 0 122 255)
            , Background.hoverColor (Ui.rgb 0 92 215)
            , Transition.attribute
                  [ Transition.property "background-color"
                  , Transition.duration 200
                  , Transition.easing Transition.easeOut
                  ]
            ]
            { onPress = Just Save, label = Ui.text "Save" })
```

Build the transition by composing typed `Step`s. The renderer joins them into the CSS `transition: <prop> <dur>ms <easing> <delay>ms` shorthand.

| Step | Type | Default | Notes |
|---|---|---|---|
| `property` | `String -> Step` | `"all"` | CSS property name. Common: `"background-color"`, `"color"`, `"transform"`, `"opacity"`. Pass `"all"` to transition every animatable property. |
| `duration` | `Int -> Step` | `200` | Milliseconds. |
| `delay` | `Int -> Step` | `0` | Milliseconds. Only emitted in the shorthand when non-zero. |
| `easing` | `Easing -> Step` | `easeOut` | One of `Transition.linear`, `easeIn`, `easeOut`, `easeInOut`, `cubicBezier x1 y1 x2 y2`. |

### Animations (keyframes)

```elm
import Std.Ui.Animation as Animation
import Std.Ui.Transform as Transform

fadeInUp : Ui.Attribute msg
fadeInUp =
    Animation.attribute
        { name = "fadeInUp"
        , duration = 300
        , easing = Animation.easeOut
        , delay = 0
        , iterations = Animation.once
        , fillMode = Animation.forwards
        , respectReducedMotion = True
        , keyframes =
            [ ( 0, [ Transform.opacity 0.0, Transform.translateY 10 ] )
            , ( 100, [ Transform.opacity 1.0, Transform.translateY 0 ] )
            ]
        }
```

`Animation.Spec` fields:

| Field | Type | Notes |
|---|---|---|
| `name` | `String` | User-visible name. **Auto-suffixed with the element's sky-id by the runtime** — two `name = "fadeIn"` elements with different keyframes don't collide. |
| `duration` | `Int` | Milliseconds. |
| `easing` | `Easing` | Same constants as `Transition`. |
| `delay` | `Int` | Milliseconds. |
| `iterations` | `Iterations` | `Animation.once` / `Animation.infinite` / `Animation.times N`. |
| `fillMode` | `FillMode` | `Animation.none` / `Animation.forwards` / `Animation.backwards` / `Animation.both`. `forwards` is the most common — hold the final keyframe after the animation ends. |
| `respectReducedMotion` | `Bool` | `True` wraps the animation in `@media (prefers-reduced-motion: no-preference)` (default + recommended). `False` ignores the user's preference. |
| `keyframes` | `List (Int, List Transform.Prop)` | `(percent, props)` pairs. Percent in `[0, 100]`. Order doesn't matter; renderer sorts. |

### Transform / opacity properties (for keyframes)

`Std.Ui.Transform` exposes the typed keyframe properties:

| Helper | CSS |
|---|---|
| `Transform.translateX n` / `translateY n` / `translate x y` | `transform: translateX(Npx)` etc. |
| `Transform.scale s` / `scaleXY sx sy` | `transform: scale(s)` |
| `Transform.rotate deg` | `transform: rotate(<deg>deg)` |
| `Transform.skewX deg` / `skewY deg` | `transform: skew*(deg)` |
| `Transform.opacity a` | `opacity: a` (NOT a transform — emitted as standalone) |

Multiple `transform`-typed props on the same keyframe join into a single `transform:` shorthand (`transform: translateY(10px) scale(0.95)`). Mixed `transform` + `opacity` props emit two rules.

### Composition

- **Pseudo-class + transition.** `Background.hoverColor` defines the target state; `Transition.attribute` declares how to interpolate the change. Most natural way to build interactive buttons / cards / nav links.
- **Breakpoint + animation.** `Ui.breakpoint Ui.mobile [ ... ] child` wraps the element; the animation attaches to `child`. Both layers stack via CSS — the `@keyframes` lives in the inner scoped `<style>` while the `@media` wrapper from the breakpoint applies to the wrapper layout.
- **Multiple animations.** Stacking two `Animation.attribute` calls joins them in the `animation:` shorthand with commas.

### Reduced-motion sample

A loading spinner uses `respectReducedMotion = False` because a static circle defeats the purpose of "indicate the page is busy":

```elm
spinner : Element msg
spinner =
    Ui.el
        [ Ui.width (Ui.px 24)
        , Ui.height (Ui.px 24)
        , Background.color (Ui.rgb 60 120 200)
        , Border.rounded 12
        , Animation.attribute
              { name = "spin"
              , duration = 1000
              , easing = Animation.linear
              , delay = 0
              , iterations = Animation.infinite
              , fillMode = Animation.none
              , respectReducedMotion = False
              , keyframes =
                    [ ( 0, [Transform.rotate 0.0] )
                    , ( 100, [Transform.rotate 360.0] )
                    ]
              }
        ]
        Ui.none
```

For every other case — hover/focus transitions, page-load fades, slide-in panels — keep `respectReducedMotion = True` (the default).

### What renders on the wire

```html
<button sky-id="r.0#button" style="...base styles...">
    <style data-sky-tr="r.0#button">
        @media (prefers-reduced-motion: no-preference) {
            [sky-id="r.0#button"] { transition: background-color 200ms ease-out; }
        }
    </style>
    <style data-sky-pc="r.0#button">
        @media (hover: hover) {
            [sky-id="r.0#button"]:hover { background-color: rgba(0, 92, 215, 1); }
        }
    </style>
    Save
</button>
```

For an animated element:

```html
<div sky-id="r.1#div" style="...base styles...">
    <style data-sky-anim="r.1#div">
        @keyframes fadeInUp__r_1_div {
            0% { transform: translateY(10px); opacity: 0; }
            100% { transform: translateY(0px); opacity: 1; }
        }
        @media (prefers-reduced-motion: no-preference) {
            [sky-id="r.1#div"] { animation: fadeInUp__r_1_div 300ms ease-out 0ms 1 forwards; }
        }
    </style>
    ...content...
</div>
```

The `@keyframes` name is auto-suffixed with `__<sky-id>` (CSS-sanitised) so two unrelated elements declaring `name = "fadeInUp"` with different keyframes never collide globally.

## Putting it all together — a non-trivial example

`examples/19-skyforum` is the canonical Sky.Ui demo: a Reddit/HackerNews-style forum split across 8 modules. Highlights:

* **Posts list with per-post upvote/downvote.** Each user gets one vote per post; clicking the same direction removes the vote, clicking the opposite swaps. Vote button colours track active state (▲ orange when upvoted, ▼ blue when downvoted).
* **Post detail with recursive threaded comments.** Per-comment vote labels flip "upvote" → "upvoted" (orange) and "downvote" → "downvoted" (blue) based on the user's vote.
* **Reply compose with parent-thread context** via the form pattern.
* **Sign in via `<form onSubmit=DoSignIn>`** — password never enters the Model.
* **Anonymous users redirect to LoginPage** on any vote / comment attempt.

The 8-module split (`State.sky` / `Update.sky` / `View/{Common,Posts,Detail,Compose,Login}.sky` / `Main.sky`) is the canonical workaround for [Limitation #17](#known-limitations) — see below.

## Surface coverage

| Surface | Status | Notes |
|---|:---:|---|
| **Layout**: `el / row / column / wrappedRow / grid / paragraph / textColumn` | ✅ | `wrappedRow` adds `flex-wrap: wrap`; `grid` is CSS-Grid auto-fit (`Ui.gridColumns N` for the minmax floor) |
| Layout: `none` | ✅ | Bare `Ui.none : Element msg`. Use `import Std.Ui exposing (Element)` so annotations read `Element Msg` (not `Ui.Element Msg`). |
| Layout: `link / image / button` | ✅ | |
| Layout: `input` (real `<input>`) | ✅ | `Ui.el` renders as `<div>`, so a dedicated helper exists |
| Layout: `form` (with `onSubmit`-into-typed-record) | ✅ | Wire driver decodes formData into a typed record |
| Layout: `html` escape hatch | ✅ | `Ui.html node : any -> Element msg` wraps a Std.Html `Html msg` node |
| **Length**: `px / content / fill / fillPortion / minimum / maximum / shrink / vh / vw` | ✅ | `fill : Length` is bare; use `fillPortion n` for proportional weights; `vh n` / `vw n` are viewport-relative |
| **Alignment**: `centerX/Y / align*` | ✅ | |
| **Padding**: `padding / paddingXY / paddingEach` / `spacing` | ✅ | `paddingXY x y` is X-first/Y-second (matches elm-ui — `paddingXY 24 16` = 24px horizontal, 16px vertical). `paddingEach` is record-shaped: `{ top, right, bottom, left }` (matches `Border.widthEach` and elm-ui). |
| **Background**: `color / image / linearGradient / gradient` | ✅ | `Std.Ui.Background` |
| **Border**: `color / width / widthEach / rounded / solid / dashed / dotted / shadow / glow / innerShadow` | ✅ | `Std.Ui.Border` |
| **Font**: `color / family / size / weight / bold / semiBold / regular / light / extraBold / black / italic / underline / noDecoration / lineThrough / overline / letterSpacing / wordSpacing / alignLeft / alignRight / alignCenter / center / justify / sansSerif / serif / monospace` | ✅ | `Std.Ui.Font` |
| **Color**: `rgb / rgba / white / black / transparent` | ✅ | Sky stores 0-255 ints; HM friction with 0-1 floats |
| **Region**: `heading n / mainContent / navigation / footer / aside / label / announce / announceUrgently` | ✅ | Renderer dispatches `<h1>`..`<h6>` / `<main>` / `<nav>` / `<footer>` / `<aside>` from the Description; aria-label / aria-live for the rest |
| **Events**: `onClick / onMouseOver/Out / onFocus` | ✅ | |
| Events: `onInput` (text input) | ✅ | Typed `(String -> msg)` |
| Events: `onChange / onKeyDown / onSubmit` | ✅ | Sky.Live wire events |
| Events: `onFile / onImage` (with browser-side resize) | ✅ | Base64 data URL + `fileMaxSize/Width/Height` |
| **Input controls**: `button / text / multiline / checkbox` | ✅ | `Std.Ui.Input` |
| Input: `email / username / search / currentPassword / newPassword` | ✅ | Typed wrappers with the matching HTML5 input type + `autocomplete=` for password-manager UX |
| Input: `radio / radioRow / slider` | ✅ | `RadioOption` uses string values (Sky-side trade-off vs elm-ui's polymorphic option type to sidestep deeply-nested-polymorphic-record HM friction) |
| Input: `placeholder` | ✅ | Renders as the HTML `placeholder=` attribute on the input |
| Input: `labelAbove/Below/Left/Right/Hidden` | ✅ | LabelHidden emits `aria-label` on the wrapper |
| Input: attrs split between wrapper + control | ✅ | v0.15.55+. Layout / size / alignment attrs on `Input.*` (`Ui.width`/`Ui.height`/`Ui.padding`/`Ui.spacing`/`Ui.alignX`/`Ui.alignY`/`Ui.nearby`/`Ui.pointer`/`Ui.overflow`) hoist to the outer wrapper so the flex chain stays intact; form / event / visual attrs (`Ui.htmlAttribute`, `Ui.onInput`, `Background.color`, `Font.color`, …) stay on the inner `<input>` / `<textarea>`. The inner control gains implicit `Ui.width Ui.fill + Ui.height Ui.fill` when ≥1 layout attr was hoisted (no implicit fill when zero layout attrs → defaults stay intrinsic). |
| **Lazy**: `lazy / lazy2..lazy5` | ✅ | LRU-cached subtree, keyed on `(function-pointer, args fingerprint)`. Default cap 1024 entries; override via `SKY_UI_LAZY_CAP=N`. |
| **Keyed**: `keyed` | ✅ | `sky-key` attribute |
| **Nearby**: `above / below / onLeft / onRight / inFront / behind` | ✅ | Renderer wraps the parent with `position: relative` and the nearby Element with `position: absolute` + matching offsets |
| **Cursor**: `pointer` | ✅ | |
| **Overflow**: `clip / clipX / clipY / scrollbars / scrollbarX / scrollbarY` | ✅ | `overflow-x` / `overflow-y` |
| **Misc**: `transparent` / `htmlAttribute` / `style` / `class` / `name` | ✅ | |
| Misc: `classifyDevice` | ✅ | Via `Std.Ui.Responsive` (Model-driven) |
| **Media queries**: `mediaQuery / breakpoint / Breakpoint` | ✅ | CSS-driven viewport-conditional styling — instant, no JS round-trip. Typed `Mobile / Tablet / Desktop / SmAndUp / MdAndUp / LgAndUp / XlAndUp / DarkMode / LightMode / ReducedMotion / TouchDevice / Portrait / Landscape / Custom`. See §"Media queries + breakpoints". |
| **Pseudo-classes**: `Background.hoverColor / Font.focusColor / Border.activeColor / ... / Ui.onPseudo` | ✅ | `:hover` / `:focus-visible` / `:focus` / `:active` / `:disabled` typed helpers on every sub-module + generic escape hatch. `:hover` auto-gated behind `@media (hover: hover)` for touch-device safety. See §"Pseudo-classes (hover, focus, active, disabled)". |
| **Transitions + animations**: `Transition.attribute / Animation.attribute / Std.Ui.Transform` | ✅ | Typed CSS transition Steps + typed keyframe Spec with Iterations + FillMode. Auto-wrapped in `@media (prefers-reduced-motion: no-preference)` by default; opt out via `attributeUnsafe` / `respectReducedMotion = False`. `@keyframes` names auto-suffixed with sky-id. See §"Transitions + animations". |
| **Aspect ratio**: `Ui.aspectRatio / Ui.aspectRatioWH / Ui.square / Ui.widescreen / Ui.fullHd / Ui.cinemascope` | ✅ | Inline `aspect-ratio:` CSS; pairs with `Ui.width Ui.fill` so the unset axis auto-scales via the browser's aspect-ratio solver. See §"`Ui.aspectRatio` — proportional sizing". |
| **Grid tracks**: `Std.Ui.Grid.{tracks,columns,rows}` + `Track` ADT (`fr / px / auto / minContent / maxContent / minmax / repeat / repeatAutoFit / repeatAutoFill`) | ✅ | Typed CSS-grid track-list — sidebar layouts (`1fr 200px 1fr`), content-aware tracks (`auto 1fr`), responsive card grids (`repeat(auto-fit, minmax(240px, 1fr))`). Lighter `Ui.gridColumns N` (auto-fill default) stays for the common-case product-card grid. See §"`Std.Ui.Grid` — typed track lists". |
| **Render target** | — | Server-side Sky.Live + ~2 KB browser JS |
| **Style emission** | — | Inline styles per element |

Legend: ✅ ships · ⚠️ partial

## Known limitations

**#17 — HM type-checker heap exhaustion on Std.Ui-heavy single modules.** A single Main.sky that combines (`Std.Ui` + sub-modules) imports + ~25 polymorphic `Element Msg` helpers + `view` returning a deeply nested tree can blow the GHC heap during the `-- Type Checking` phase. Symptom: `sky check` allocates ~2.6 GB/s, GC consumes 80%+ of total time, peaks at 4–5 GB RSS in 10 s. The compiler-side fix is tracked; the canonical workaround that ships in `examples/19-skyforum` is **splitting the view layer across multiple modules** (`State.sky` / `Update.sky` / `View/Common.sky` / `View/Posts.sky` / `View/Detail.sky` / `View/Compose.sky` / `View/Login.sky` / `Main.sky` dispatcher). The split form delivers the *full* feature surface and type-checks in 1.11 s / 369 MB.

When iterating on Std.Ui-heavy code on macOS, run `scripts/mem-guard.sh` in the background first — it SIGKILLs runaway compiler processes before they OOM the machine. See CLAUDE.md "Memory Safety (Non-Negotiable)" for the standing rule.

**#18 — Typed-codegen monomorphised `(String -> Msg)` helper params to `(String -> any)`.** **Closed in v0.15.** Type-directed lowering now threads the typed callee param through call-site arg coercion and record-field lambda lowering, so a helper `textField : String -> (String -> Msg) -> Element Msg` emits with `func(string) Msg` and `go build` accepts the typed constructor directly. The empty-list-in-positional-constructor variant is closed by the same lowering.

**Cross-module qualified type references.** Annotations using a *qualified-with-alias* type reference (`view : ... -> Ui.Element Msg`) can fail with `Type mismatch: Element a vs Element Msg` because Sky's canonicaliser strips type parameters from qualified-alias references. **Workaround**: import the type unqualified and use the bare name in annotations. The canonical pattern (used by every Sky.Ui example) is:

```elm
import Std.Ui as Ui
import Std.Ui exposing (Element)        -- bring the bare type name in scope

view : Model -> Element Msg              -- bare `Element`, not `Ui.Element`
view model = Ui.row [...] [...]          -- bare `Element` lets `Ui.row` instantiate cleanly
```

With this pattern, `Ui.none`, `Ui.text`, `Ui.row`, `Ui.column` and the rest unify against `Element Msg` correctly. The compiler-side fix (proper qualified-alias type-param resolution) is tracked separately and is not specific to Std.Ui.

## See also

* [`examples/19-skyforum`](../../examples/19-skyforum/) — the full feature demo (forum)
* [`examples/26-ui-showcase`](../../examples/26-ui-showcase/) — every Std.Ui layout primitive on one page (visual-regression reference)
* [Sky.Live overview](../skylive/overview.md) — the runtime Std.Ui sits on top of
* [Standard library reference](../stdlib.md) — the rest of Sky's surface
* [NOTICE.md](../../NOTICE.md) — prior-art attribution for Std.Ui's API conventions
