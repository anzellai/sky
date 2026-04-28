# Std.Ui overview

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
Ui.none                                -- empty placeholder (workaround:
                                       --   use Ui.text "" today — see
                                       --   Limitations below)
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

Lazy.lazy renderItem item               -- memo wrapper (no-op today; see Limitations)
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
| Layout: `none` | ✅ | Use `import Std.Ui exposing (Element)` and bare `Element Msg` in annotations (not `Ui.Element Msg`) |
| Layout: `link / image / button` | ✅ | |
| Layout: `input` (real `<input>`) | ✅ | `Ui.el` renders as `<div>`, so a dedicated helper exists |
| Layout: `form` (with `onSubmit`-into-typed-record) | ✅ | Wire driver decodes formData into a typed record |
| Layout: `html` escape hatch | ✅ | `Ui.html node : any -> Element msg` wraps a Std.Html VNode |
| **Length**: `px / content / fill / fillPortion / minimum / maximum / shrink / vh / vw` | ✅ | `fill : Length` is bare; use `fillPortion n` for proportional weights; `vh n` / `vw n` are viewport-relative |
| **Alignment**: `centerX/Y / align*` | ✅ | |
| **Padding**: `padding / paddingXY / paddingEach` / `spacing` | ✅ | `paddingEach` is record-shaped: `{ top, right, bottom, left }` (matches `Border.widthEach` and elm-ui) |
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
| **Lazy**: `lazy / lazy2..lazy5` | ⚠️ | No-op wrappers (type-correct passthrough); runtime memoisation deferred — needs a runtime VNode cache keyed on function-pointer + serialised args |
| **Keyed**: `keyed` | ✅ | `sky-key` attribute |
| **Nearby**: `above / below / onLeft / onRight / inFront / behind` | ✅ | Renderer wraps the parent with `position: relative` and the nearby Element with `position: absolute` + matching offsets |
| **Cursor**: `pointer` | ✅ | |
| **Overflow**: `clip / clipX / clipY / scrollbars / scrollbarX / scrollbarY` | ✅ | `overflow-x` / `overflow-y` |
| **Misc**: `transparent` / `htmlAttribute` / `style` / `class` / `name` | ✅ | |
| Misc: `classifyDevice` | ✅ | Via `Std.Ui.Responsive` |
| **Render target** | — | Server-side Sky.Live + ~2 KB browser JS |
| **Style emission** | — | Inline styles per element |

Legend: ✅ ships · ⚠️ partial

## Known limitations

**#17 — HM type-checker heap exhaustion on Std.Ui-heavy single modules.** A single Main.sky that combines (`Std.Ui` + sub-modules) imports + ~25 polymorphic `Element Msg` helpers + `view` returning a deeply nested tree can blow the GHC heap during the `-- Type Checking` phase. Symptom: `sky check` allocates ~2.6 GB/s, GC consumes 80%+ of total time, peaks at 4–5 GB RSS in 10 s. The compiler-side fix is tracked; the canonical workaround that ships in `examples/19-skyforum` is **splitting the view layer across multiple modules** (`State.sky` / `Update.sky` / `View/Common.sky` / `View/Posts.sky` / `View/Detail.sky` / `View/Compose.sky` / `View/Login.sky` / `Main.sky` dispatcher). The split form delivers the *full* feature surface and type-checks in 1.11 s / 369 MB.

When iterating on Std.Ui-heavy code on macOS, run `scripts/mem-guard.sh` in the background first — it SIGKILLs runaway compiler processes before they OOM the machine. See CLAUDE.md "Memory Safety (Non-Negotiable)" for the standing rule.

**#18 — Typed-codegen monomorphises `(String -> Msg)` helper params to `(String -> any)`.** A helper like `textField : String -> String -> (String -> Msg) -> Element Msg` (with concrete `Msg`, not polymorphic `msg`) gets emitted with a `func(string) any` arg, which `go build` rejects: `cannot use Msg_LoginUserChanged (value of type func(v0 string) Msg) as func(string) any`. Workaround: inline the input element at the use site (no helper indirection — the typed codegen sees the constructor through). Most Std.Ui form patterns flatten naturally.

Same bug class also turns up as: empty list `[]` in a positional constructor's typed-slice arg position emits as `[]any{}` instead of `[]string{}`. Workaround: switch seed data from positional `Post 1 "..." ... [] []` form to record-literal `{ id = 1, ..., upvoters = [], ... }` — the field's type alias gives the codegen the target type.

**Cross-module qualified type references.** Annotations using a *qualified-with-alias* type reference (`view : ... -> Ui.Element Msg`) can fail with `Type mismatch: Element a vs Element Msg` because Sky's canonicaliser strips type parameters from qualified-alias references. **Workaround**: import the type unqualified and use the bare name in annotations. The canonical pattern (used by every Sky.Ui example) is:

```elm
import Std.Ui as Ui
import Std.Ui exposing (Element)        -- bring the bare type name in scope

view : Model -> Element Msg              -- bare `Element`, not `Ui.Element`
view model = Ui.row [...] [...]          -- bare `Element` lets `Ui.row` instantiate cleanly
```

With this pattern, `Ui.none`, `Ui.text`, `Ui.row`, `Ui.column` and the rest unify against `Element Msg` correctly. The compiler-side fix (proper qualified-alias type-param resolution) is tracked separately and is not specific to Std.Ui.

## See also

* [`examples/19-skyforum`](../../examples/19-skyforum/) — the full demo
* [Sky.Live overview](../skylive/overview.md) — the runtime Std.Ui sits on top of
* [Standard library reference](../stdlib.md) — the rest of Sky's surface
* [NOTICE.md](../../NOTICE.md) — prior-art attribution for Std.Ui's API conventions
