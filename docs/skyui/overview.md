# Std.Ui overview

**A typed layout DSL for Sky.Live, modelled on [mdgriffith/elm-ui](https://package.elm-lang.org/packages/mdgriffith/elm-ui/latest/).** Build a UI from typed primitives (`el`, `row`, `column`, `paragraph`, `textColumn`) and typed attributes (`Background.color`, `Border.rounded`, `Font.size`, `Region.heading`) — Std.Ui renders to inline-styled HTML on the server side and Sky.Live's wire ferries diffs to the browser. No CSS files. No template languages. No client framework.

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

Std.Ui borrows elm-ui's insight: model layout in terms the user actually wants (`row`, `column`, `el`, `padding`, `spacing`, alignment), and emit the right HTML+CSS automatically. No more "why is my flex child not centering" — `centerY` does centering and the underlying `align-self: center` is an implementation detail.

## The mental model

| Concept | Type | Examples |
|---|---|---|
| **Element** | `Element msg` | `Ui.text "hi"`, `Ui.row [...] [...]`, `Ui.button [...] cfg` |
| **Attribute** | `Attribute msg` | `Ui.padding 16`, `Background.color (Ui.rgb 0 0 0)`, `Ui.onClick MyMsg` |
| **Length** | `Length` | `Ui.px 200`, `Ui.fill 1`, `Ui.content`, `Ui.min 100 (Ui.fill 1)` |
| **Color** | `Color` | `Ui.rgb 255 102 0`, `Ui.rgba 0 0 0 0.5`, `Ui.white`, `Ui.black` |

Every `Element msg` has a `msg` parameter — the same `msg` you've defined for your TEA app. Attributes that carry events (`onClick`, `onSubmit`, `onInput`) tie into the same `msg` so the type checker catches mismatches at compile time.

The `Ui.layout` function takes the root element and produces an `any` that Sky.Live's `view` field accepts. Wrap your top-level view in it.

## Layout primitives

```elm
Ui.el      [Attr] (Element)            -- single element (renders as <div>)
Ui.row     [Attr] [Element]            -- horizontal flex container
Ui.column  [Attr] [Element]            -- vertical flex container
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

## Length

```elm
Ui.px : Int -> Length          -- absolute pixels
Ui.fill : Int -> Length        -- flex-grow weight (1 = single growing slot)
Ui.content                     -- shrink-to-fit
Ui.min : Int -> Length -> Length    -- minimum constraint on a length
Ui.max : Int -> Length -> Length    -- maximum constraint
```

Use with `Ui.width` / `Ui.height`:

```elm
Ui.row [ Ui.spacing 8 ]
    [ Ui.el [ Ui.width (Ui.px 80) ] (Ui.text "Label:")
    , Ui.el [ Ui.width (Ui.fill 1) ] (Ui.text fieldValue)   -- fills remaining
    , Ui.el [ Ui.width (Ui.px 32) ] (Ui.text "✓")
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

Lazy.lazy renderItem item               -- elm-ui-style memo wrapper
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

## Std.Ui vs elm-ui — surface coverage

| Surface | elm-ui | Std.Ui | Notes |
|---|:---:|:---:|---|
| **Layout**: `el / row / column / paragraph / textColumn` | ✅ | ✅ | |
| Layout: `none` | ✅ | ⚠️ | Cross-module type-param strip — workaround `Ui.text ""` |
| Layout: `link / image / button` | ✅ | ✅ | |
| Layout: `input` (real `<input>`) | n/a | ➕ | Sky-only — `Ui.el` renders as `<div>` so a dedicated helper exists |
| Layout: `form` (with `onSubmit`-into-typed-record) | n/a | ➕ | Sky-only — wire driver decodes formData into typed record |
| Layout: `html` escape hatch | ✅ | ⚠️ | Collapses to `Text ""` today |
| **Length**: `px / content / fill / min / max` | ✅ | ✅ | |
| Length: `fillPortion / shrink` | ✅ | ❌ | |
| **Alignment**: `centerX/Y / align*` | ✅ | ✅ | |
| **Padding**: uniform / `spacing` | ✅ | ✅ | |
| Padding: `paddingXY / paddingEach` | ✅ | ⚠️ | `AttrPadding` takes 4 ints, no helper yet |
| **Background**: `color` | ✅ | ✅ | |
| Background: gradient / image | ✅ | ❌ | |
| **Border**: `color / width / rounded` | ✅ | ✅ | |
| Border: `widthEach / dashed / dotted / shadow` | ✅ | ❌ | |
| **Font**: `color / family / size / bold` | ✅ | ✅ | |
| Font: `italic / underline / letterSpacing / wordSpacing` | ✅ | ❌ | |
| **Color**: `rgb / rgba / rgb255` | ✅ | ✅ | Sky stores 0-255 ints; HM friction with 0-1 floats |
| **Region**: `heading / footer` | ✅ | ✅ | |
| Region: `navigation / mainContent / aside / announce` | ✅ | ⚠️ | ADT variants exist; user-facing helpers partial |
| **Events**: `onClick / onMouseEnter/Leave / onFocus` | ✅ | ✅ | |
| Events: `onInput` (text input) | n/a | ➕ | Sky-only — typed `(String -> msg)` |
| Events: `onChange / onKeyDown / onSubmit` | n/a | ➕ | Sky.Live wire events |
| Events: `onFile / onImage` (with browser-side resize) | n/a | ➕ | Sky-only — base64 data URL + `fileMaxSize/Width/Height` |
| **Input controls**: `button / text / multiline / checkbox` | ✅ | ✅ | (`Std.Ui.Input`) |
| Input: `radio / radioRow / slider` | ✅ | ❌ | HM friction — deferred |
| Input: `username / email / newPassword / search` | ✅ | ❌ | Use generic `Ui.input` with `type="..."` |
| Input: `placeholder` | ✅ | ⚠️ | Constructor exists, render is TODO |
| Input: `labelAbove/Below/Left/Right/Hidden` | ✅ | ✅ | |
| **Lazy**: `lazy / lazy2..lazy5` | ✅ | ⚠️ | No-op wrappers; runtime memo deferred |
| **Keyed**: `keyed` | ✅ | ✅ | `sky-key` attribute |
| **Nearby**: `above / below / onLeft / onRight / inFront / behind` | ✅ | ⚠️ | `Location` ctors exist; user-facing helpers TBD |
| **Cursor**: `pointer` | ✅ | ✅ | |
| **Misc**: `transparent` / `htmlAttribute` | ✅ | ✅ | |
| Misc: `clip / scrollbars / focusStyle` | ✅ | ❌ | |
| Misc: `classifyDevice` | ✅ | ➕ | Via `Std.Ui.Responsive` |
| **Render target** | Browser-side Elm runtime | Server-side Sky.Live + ~2 KB browser JS | Different shape, same DSL |
| **Style emission** | CSS classes (memoised) | Inline styles | Sky's path is simpler, slightly heavier wire |

Legend: ✅ at parity · ⚠️ partial · ❌ not yet · ➕ Sky-only

## Known limitations

**#17 — HM type-checker heap exhaustion on Std.Ui-heavy single modules.** A single Main.sky that combines (`Std.Ui` + sub-modules) imports + ~25 polymorphic `Element Msg` helpers + `view` returning a deeply nested tree can blow the GHC heap during the `-- Type Checking` phase. Symptom: `sky check` allocates ~2.6 GB/s, GC consumes 80%+ of total time, peaks at 4–5 GB RSS in 10 s. The compiler-side fix is tracked; the canonical workaround that ships in `examples/19-skyforum` is **splitting the view layer across multiple modules** (`State.sky` / `Update.sky` / `View/Common.sky` / `View/Posts.sky` / `View/Detail.sky` / `View/Compose.sky` / `View/Login.sky` / `Main.sky` dispatcher). The split form delivers the *full* feature surface and type-checks in 1.11 s / 369 MB.

When iterating on Std.Ui-heavy code on macOS, run `scripts/mem-guard.sh` in the background first — it SIGKILLs runaway compiler processes before they OOM the machine. See CLAUDE.md "Memory Safety (Non-Negotiable)" for the standing rule.

**#18 — Typed-codegen monomorphises `(String -> Msg)` helper params to `(String -> any)`.** A helper like `textField : String -> String -> (String -> Msg) -> Element Msg` (with concrete `Msg`, not polymorphic `msg`) gets emitted with a `func(string) any` arg, which `go build` rejects: `cannot use Msg_LoginUserChanged (value of type func(v0 string) Msg) as func(string) any`. Workaround: inline the input element at the use site (no helper indirection — the typed codegen sees the constructor through). Most Std.Ui form patterns flatten naturally.

Same bug class also turns up as: empty list `[]` in a positional constructor's typed-slice arg position emits as `[]any{}` instead of `[]string{}`. Workaround: switch seed data from positional `Post 1 "..." ... [] []` form to record-literal `{ id = 1, ..., upvoters = [], ... }` — the field's type alias gives the codegen the target type.

**Cross-module type-parameter stripping.** `import Std.Ui exposing (Element)` gets the type alias but the canonicaliser may strip type parameters from cross-module references to certain values (notably `Std.Ui.none`). Workaround: use `Ui.text ""` instead of `Ui.none` when referencing across module boundaries.

## See also

* [`examples/19-skyforum`](../../examples/19-skyforum/) — the full demo
* [Sky.Live overview](../skylive/overview.md) — the runtime Std.Ui sits on top of
* [Standard library reference](../stdlib.md) — the rest of Sky's surface
* [mdgriffith/elm-ui](https://package.elm-lang.org/packages/mdgriffith/elm-ui/latest/) — the Elm package this is modelled on
