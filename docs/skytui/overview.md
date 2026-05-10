# Sky.Tui overview

**Terminal-rendering TEA backend.** `Sky.Tui` runs an `init` / `update`
/ `view` / `subscriptions` app the same way Sky.Live runs a web app —
but the view function paints to ANSI terminal cells instead of HTML.
The same `Std.Ui` element tree renders in both backends, so a
counter, a stopwatch, or a small dashboard ports between the browser
and the terminal with no view rewrites.

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Task as Task
import Std.Tui as Tui
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Ui as Ui
import Std.Ui exposing (Element)
import Std.Ui.Background as Background
import Std.Ui.Font as Font


type alias Model = { count : Int }

type Msg = Increment | Decrement | Quit | NoOp

init : () -> ( Model, Cmd Msg )
init _ = ( { count = 0 }, Cmd.none )

update : Msg -> Model -> ( Model, Cmd Msg )
update msg model = case msg of
    Increment -> ( { model | count = model.count + 1 }, Cmd.none )
    Decrement -> ( { model | count = model.count - 1 }, Cmd.none )
    Quit      -> ( model, Cmd.perform (Sky.Core.System.exit 0) (\_ -> NoOp) )
    NoOp      -> ( model, Cmd.none )

view : Model -> Element Msg
view model =
    Ui.column
        [ Ui.padding 16, Ui.spacing 8 ]
        [ Ui.text ("Count: " ++ String.fromInt model.count)
        , Ui.row [ Ui.spacing 8 ]
            [ Ui.button [] { onPress = Just Decrement, label = Ui.text "−" }
            , Ui.button [] { onPress = Just Increment, label = Ui.text "+" }
            ]
        ]

subscriptions : Model -> Sub Msg
subscriptions _ = Sub.none

main =
    Tui.app
        { init = init, update = update, view = view
        , subscriptions = subscriptions
        }
        |> Task.run
```

`sky run src/Main.sky` builds and launches the binary. The terminal
switches to alt-screen, raw mode, and mouse tracking; teardown on
exit (Ctrl-C, `Quit`, panic, SIGTERM) restores the user's primary
screen.

## Status: experimental

Sky.Tui shipped on the `exp/tea-core` branch and is targeted at
v0.12. The surface is feature-stable but not yet covered by the same
backwards-compatibility promises as Sky.Live. Treat it as a way to
experiment with TEA-style terminal apps; expect ergonomic polish
between minor releases.

## What works

| Area | Details |
|---|---|
| Layout | row, column, wrappedRow, paragraph (word-wrap), textColumn, grid + gridColumns, el |
| Sized elements | text, link, image, button, input, form |
| Length | px, fill, fillPortion N, content, shrink, minimum N L, maximum N L, vh N, vw N |
| Padding / spacing | padding N, paddingXY x y, paddingEach { top, right, bottom, left }, spacing N |
| Alignment | centerX/Y, alignLeft/Right/Top/Bottom |
| Borders | solid / dashed / dotted with `widthEach`, rounded, color |
| Text styling | bold, italic, underline, lineThrough, fg/bg colour (truecolour SGR; suppressed under `NO_COLOR`) |
| Headings | h1 – h6 with distinct visual markers (`═ ─ ▌ ▎ ▏ ·`) |
| Inputs | text, password (masked), checkbox (☐/☑), radio (○/●), slider, multiline textarea |
| Events | onClick, onInput, onFocus, onSubmit (form record-decode), onKeyDown |
| Mouse | left-press, scroll wheel (3 cells/notch). Release / drag / middle / right-click deferred |
| Nearby overlays | above, below, onLeft, onRight, inFront, behind |
| Wide chars | CJK + emoji + ZWJ family — proper grapheme clusters via `github.com/rivo/uniseg` |
| Bracketed paste | up to 1 MiB; multi-line paste no longer fires phantom Enter |
| Modifier keys | Ctrl-Left/Right do word-jump; Shift/Alt/Ctrl flags reach user `onKey` |
| Resize | SIGWINCH triggers re-layout |

The runtime restores the terminal in every exit path: panic, signal
(SIGTERM/HUP/QUIT/INT), `System.exit`, normal `Quit` Msg. mosh
sessions don't end up with a corrupted readline.

## Logical-pixel canvas

`Tui.app` accepts optional `canvasWidth` / `canvasHeight` fields. The
runtime computes `pxPerCell` from the live terminal size and scales
every `Ui.padding 8`, `Ui.spacing 4`, `Ui.px N` to character cells.
Default 1280×720 matches a typical web canvas — Std.Ui apps written
for the browser look right in the terminal without re-tuning. Tweak
via `canvasWidth = 800` for denser layout.

```elm
main =
    Tui.app
        { init = init, update = update, view = view
        , subscriptions = subscriptions
        , canvasWidth = 1024
        , canvasHeight = 768
        }
        |> Task.run
```

## Auth guard middleware

`Tui.app`'s optional `guard` field has the same shape as
`Live.app`'s — `Msg -> Model -> Result Error ()`. Returning
`Err reason` skips the update and (if your model has
`notification` / `notificationType` fields) writes the rejection
into them for the view to render. The same guard function works
under both backends, so authentication logic stays portable.

## Sky.Cli — line-oriented TEA

For apps that DON'T want raw-mode and full-screen rendering,
`Sky.Cli` provides a line-oriented variant: the view returns a
`String`, `update` consumes lines from stdin, and the runtime runs
on a regular non-raw terminal. Useful for piped scripts and CI
diagnostics.

`Cli.readPassword : () -> Task Error String` reads a line from
stdin with terminal echo disabled — wraps `golang.org/x/term`'s
ReadPassword. Falls back gracefully on non-TTY stdin.

## Examples

| # | Name | Description |
|---|------|-------------|
| 20 | cli-counter | Sky.Cli — TEA on stdin lines |
| 21 | tui-stopwatch | Sky.Tui — bubbletea-style stopwatch |
| 22 | tui-stopwatch-ui | Sky.Tui — Std.Ui-driven stopwatch (same view function works under Sky.Live too) |
| 23 | tui-todo | Sky.Tui — todo CRUD demo |
| 24 | tui-kitchen-sink | Sky.Tui — every supported Std.Ui primitive in one screen |

## Environment variables

| Var | Purpose |
|---|---|
| `NO_COLOR` | Suppress colour SGR (bold / underline / reverse retained) |
| `TERM=dumb` | Refused with friendly error before raw mode (avoids corrupting non-TTY output) |
| `SKY_TUI_QUIET=1` | Suppress unsupported-attribute warnings on exit |
| `SKY_TUI_LOG=1` | Write a ledger of warnings to a log file |

## Reliability floor

These are enforced runtime invariants — every panic / signal /
malformed input path was audited before the experimental tag.

| Concern | Floor |
|---|---|
| Goroutine panic | `safeGo` wrapper restores TTY before exiting |
| External SIGTERM / SIGHUP / SIGQUIT / SIGINT | Trapped → tuiTeardown → exit 128+signum |
| Panic on main goroutine | Deferred tuiTeardown + DECSTR soft reset on exit |
| ANSI injection via user text | `sanitiseRune` strips control bytes (0x00-0x1F, 0x7F) |
| Wide-char column drift | uniseg-backed `displayWidth` / `iterGraphemes` |
| Resource exhaustion (runaway view height) | Hard cap `tuiMaxContentH = 50,000`; soft warn at 10,000 |
| `TERM=dumb` / non-TTY stdin | Refused before raw mode |
| Readline corruption after exit | DECSTR (`\x1b[!p`) + charset reset + scroll-region reset on every teardown path |

## Prior-art attribution

The TEA shape (`init` / `update` / `view` / `subscriptions`) is
adapted from elm-lang's `Browser.element`. `bubbletea`'s renderer
inspired the `safeGo` + alt-screen lifecycle. See `NOTICE.md`.

## See also

- `docs/skylive/overview.md` — the web-side TEA backend that shares the same `Std.Ui` element tree.
- `docs/skyui/overview.md` — the layout DSL itself.
- `examples/24-tui-kitchen-sink/src/Main.sky` — every supported primitive in one screen.
