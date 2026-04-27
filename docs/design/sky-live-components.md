# Sky.Live — Component Protocol & Package Ecosystem

## The Core Idea

Every embeddable component exposes a type with **the same name as the module**.
This is the standard ML-family convention also used by Elm (`Html.Html`,
`Json.Decoder`, `Task.Task`). When that type appears in your Model, the
compiler knows it's a component.

```elm
-- Package:
module DatePicker exposing (DatePicker, Msg, ...)
type alias DatePicker = { ... }

-- Consumer:
type alias Model =
    { datePicker : DatePicker.DatePicker
    }
```

Clear. Explicit. Compatible with the wider ML-family idiom. No new keywords.

---

## 1. The Component Protocol

A Sky.Live component package MUST export this shape:

```
Module: Foo

Required exports:
  Foo     : type           -- the component state (same name as module)
  Msg     : type           -- internal message type
  init    : Foo            -- initial state (a value, not a function)
  update  : Msg -> Foo -> ( Foo, Cmd Msg )
  view    : Config msg -> Foo -> Html msg

Optional exports:
  Config  : type alias     -- view configuration with toMsg + callbacks
  subscriptions : Foo -> Sub Msg
  (any query/command functions)
```

That's the contract. If a module follows this shape, it's a component.
The compiler can verify it and enable auto-wiring.

---

## 2. What Package Authors Write

### DatePicker

```elm
module DatePicker exposing
    ( DatePicker, Msg, Config
    , init, update, view
    , selected, isOpen, open, close
    )

{-| A date picker component for Sky.Live applications.

    import DatePicker exposing (DatePicker)

    type alias Model =
        { datePicker : DatePicker }

    type Msg
        = DatePickerMsg DatePicker.Msg

-}

-- ── The Component Type ───────────────────────────────
-- Same name as module. This IS the component.

type alias DatePicker =
    { open : Bool
    , selected : Maybe Date
    , viewing : Date
    , highlighted : Maybe Date
    }

-- ── Internal Msg ─────────────────────────────────────

type Msg
    = Open
    | Close
    | SelectDate Date
    | NextMonth
    | PrevMonth
    | Highlight Date
    | KeyDown String

-- ── Config (how parent communicates with this component) ──

type alias Config msg =
    { toMsg : Msg -> msg
    , onSelect : Date -> msg
    , minDate : Maybe Date
    , maxDate : Maybe Date
    , format : String
    }

-- ── Lifecycle ────────────────────────────────────────

init : DatePicker
init =
    { open = False
    , selected = Nothing
    , viewing = Date.today ()
    , highlighted = Nothing
    }

update : Msg -> DatePicker -> ( DatePicker, Cmd Msg )
update msg picker =
    case msg of
        Open ->
            ( { picker | open = True }, Cmd.none )

        Close ->
            ( { picker | open = False }, Cmd.none )

        SelectDate date ->
            ( { picker | selected = Just date, open = False }, Cmd.none )

        NextMonth ->
            ( { picker | viewing = Date.addMonths 1 picker.viewing }, Cmd.none )

        PrevMonth ->
            ( { picker | viewing = Date.addMonths -1 picker.viewing }, Cmd.none )

        Highlight date ->
            ( { picker | highlighted = Just date }, Cmd.none )

        KeyDown key ->
            handleKeyboard key picker

view : Config msg -> DatePicker -> Html msg
view config picker =
    div [ class "datepicker" ]
        [ viewInput config picker
        , if picker.open then
            viewCalendar config picker
          else
            text ""
        ]

viewInput : Config msg -> DatePicker -> Html msg
viewInput config picker =
    input
        [ readonly True
        , value (formatSelected config.format picker)
        , onClick (config.toMsg Open)
        , class "datepicker-input"
        ]

viewCalendar : Config msg -> DatePicker -> Html msg
viewCalendar config picker =
    div [ class "datepicker-calendar" ]
        [ div [ class "datepicker-nav" ]
            [ button [ onClick (config.toMsg PrevMonth) ] [ text "‹" ]
            , span [] [ text (Date.formatMonth picker.viewing) ]
            , button [ onClick (config.toMsg NextMonth) ] [ text "›" ]
            ]
        , div [ class "datepicker-days" ]
            (List.map (viewDay config picker) (Date.daysInMonth picker.viewing))
        ]

viewDay : Config msg -> DatePicker -> Date -> Html msg
viewDay config picker date =
    let
        isSelected =
            picker.selected == Just date

        isDisabled =
            isBeforeMin config.minDate date || isAfterMax config.maxDate date
    in
    button
        [ onClick
            (if isDisabled then
                config.toMsg (Highlight date)
             else
                config.onSelect date        -- fires parent callback
            )
        , classList
            [ ( "selected", isSelected )
            , ( "disabled", isDisabled )
            , ( "highlighted", picker.highlighted == Just date )
            ]
        ]
        [ text (String.fromInt (Date.day date)) ]

-- ── Query Functions ──────────────────────────────────

selected : DatePicker -> Maybe Date
selected picker =
    picker.selected

isOpen : DatePicker -> Bool
isOpen picker =
    picker.open

-- ── State Transforms ─────────────────────────────────

open : DatePicker -> DatePicker
open picker =
    { picker | open = True }

close : DatePicker -> DatePicker
close picker =
    { picker | open = False }
```

### RichEditor

```elm
module RichEditor exposing
    ( RichEditor, Msg, Config
    , init, update, view
    , getContent, setContent, isEmpty
    )

type alias RichEditor =
    { content : String
    , cursorPos : Int
    , selection : Maybe ( Int, Int )
    , history : List String
    , historyIndex : Int
    }

type Msg
    = Insert String
    | Delete
    | Bold
    | Italic
    | Undo
    | Redo
    | SetCursor Int
    | SetSelection Int Int

type alias Config msg =
    { toMsg : Msg -> msg
    , onChange : String -> msg
    , placeholder : String
    }

init : RichEditor
init =
    { content = ""
    , cursorPos = 0
    , selection = Nothing
    , history = [ "" ]
    , historyIndex = 0
    }

update : Msg -> RichEditor -> ( RichEditor, Cmd Msg )
update msg editor =
    case msg of
        Insert text ->
            ( insertAt editor.cursorPos text editor, Cmd.none )
        Bold ->
            ( wrapSelection "**" editor, Cmd.none )
        -- ...

view : Config msg -> RichEditor -> Html msg
view config editor =
    div [ class "rich-editor" ]
        [ viewToolbar config
        , viewContent config editor
        ]

-- Queries
getContent : RichEditor -> String
getContent editor = editor.content

isEmpty : RichEditor -> Bool
isEmpty editor = editor.content == ""

-- Transforms
setContent : String -> RichEditor -> RichEditor
setContent content editor = { editor | content = content, cursorPos = String.length content }
```

### Autocomplete

```elm
module Autocomplete exposing
    ( Autocomplete, Msg, Config
    , init, update, view
    , selected, query, clear, setValue
    )

type alias Autocomplete =
    { query : String
    , results : List String
    , highlighted : Int
    , isOpen : Bool
    , selected : Maybe String
    , loading : Bool
    }

type Msg
    = SetQuery String
    | GotResults (Result String (List String))
    | Highlight Int
    | Select String
    | Close
    | KeyDown String

type alias Config msg =
    { toMsg : Msg -> msg
    , onSelect : String -> msg
    , fetch : String -> Cmd Msg
    , placeholder : String
    }

init : Autocomplete
init =
    { query = ""
    , results = []
    , highlighted = -1
    , isOpen = False
    , selected = Nothing
    , loading = False
    }

update : Msg -> Autocomplete -> ( Autocomplete, Cmd Msg )
update msg ac =
    case msg of
        SetQuery q ->
            ( { ac | query = q, isOpen = String.length q >= 2 }
            , Cmd.none
            )
        GotResults (Ok results) ->
            ( { ac | results = results, loading = False }, Cmd.none )
        Select value ->
            ( { ac | selected = Just value, query = value, isOpen = False }, Cmd.none )
        -- ...

view : Config msg -> Autocomplete -> Html msg
view config ac =
    div [ class "autocomplete" ]
        [ input
            [ value ac.query
            , placeholder config.placeholder
            , onInput (\v -> config.toMsg (SetQuery v))
            , onKeyDown (\k -> config.toMsg (KeyDown k))
            ]
        , if ac.isOpen then
            viewDropdown config ac
          else
            text ""
        ]

-- Queries
selected : Autocomplete -> Maybe String
selected ac = ac.selected

query : Autocomplete -> String
query ac = ac.query

-- Transforms
clear : Autocomplete -> Autocomplete
clear ac = { ac | query = "", selected = Nothing, results = [], isOpen = False }

setValue : String -> Autocomplete -> Autocomplete
setValue val ac = { ac | query = val, selected = Just val }
```

---

## 3. The Pattern: Module Name = Type Name

This is already idiomatic in ML-family languages (Elm uses it too):

```
Module              Type                In Model
─────────────────   ──────────────────  ──────────────────────────
DatePicker          DatePicker          datePicker : DatePicker
RichEditor          RichEditor          editor : RichEditor
Autocomplete        Autocomplete        tagSearch : Autocomplete
Html                Html                (not a component, but same pattern)
Task                Task                (same pattern)
```

When you see `datePicker : DatePicker.DatePicker` in a Model, it's
immediately clear: this field holds a DatePicker component.

With `import DatePicker exposing (DatePicker)`, it becomes even cleaner:

```elm
import DatePicker exposing (DatePicker)
import RichEditor exposing (RichEditor)
import Autocomplete exposing (Autocomplete)

type alias Model =
    { title : String
    , datePicker : DatePicker
    , editor : RichEditor
    , tagSearch : Autocomplete
    }
```

---

## 4. What App Developers Write

### Full Example

```elm
module Main exposing (main)

import Std.Html exposing (..)
import Std.Html.Attributes exposing (..)
import Std.Live exposing (app, route)
import Std.Live.Events exposing (onClick, onInput, onSubmit)
import DatePicker exposing (DatePicker)
import RichEditor exposing (RichEditor)
import Autocomplete exposing (Autocomplete)

-- ══════════════════════════════════════════════════════
-- MODEL
-- ══════════════════════════════════════════════════════

type Page
    = EditorPage
    | PreviewPage

type alias Model =
    { page : Page
    , title : String
    , publishDate : Maybe Date
    , tags : List String
    -- Components: type makes it obvious what these are
    , datePicker : DatePicker
    , editor : RichEditor
    , tagSearch : Autocomplete
    }

-- ══════════════════════════════════════════════════════
-- MSG
-- ══════════════════════════════════════════════════════

type Msg
    = Navigate Page
    | UpdateTitle String
    | DateSelected Date
    | TagSelected String
    | RemoveTag String
    | Publish
    | GotPublishResult (Result String Post)
    -- Component Msg wrappers — declare them, don't handle them
    | DatePickerMsg DatePicker.Msg
    | EditorMsg RichEditor.Msg
    | TagSearchMsg Autocomplete.Msg

-- ══════════════════════════════════════════════════════
-- INIT
-- ══════════════════════════════════════════════════════

init : Request -> ( Model, Cmd Msg )
init _req =
    ( { page = EditorPage
      , title = ""
      , publishDate = Nothing
      , tags = []
      -- Components init to their default state
      , datePicker = DatePicker.init
      , editor = RichEditor.init
      , tagSearch = Autocomplete.init
      }
    , Cmd.none
    )

-- ══════════════════════════════════════════════════════
-- UPDATE — only your app logic
-- ══════════════════════════════════════════════════════

update : Msg -> Model -> ( Model, Cmd Msg )
update msg model =
    case msg of
        Navigate page ->
            ( { model | page = page }, Cmd.none )

        UpdateTitle t ->
            ( { model | title = t }, Cmd.none )

        DateSelected date ->
            ( { model | publishDate = Just date }, Cmd.none )

        TagSelected tag ->
            if List.member tag model.tags then
                ( model, Cmd.none )
            else
                ( { model | tags = model.tags ++ [ tag ] }, Cmd.none )

        RemoveTag tag ->
            ( { model | tags = List.filter (\t -> t /= tag) model.tags }
            , Cmd.none
            )

        Publish ->
            ( model
            , publishPost
                { title = model.title
                , body = RichEditor.getContent model.editor
                , date = model.publishDate
                , tags = model.tags
                }
                GotPublishResult
            )

        GotPublishResult (Ok _) ->
            ( { model | page = PreviewPage }, Cmd.none )

        GotPublishResult (Err _) ->
            ( model, Cmd.none )

        -- DatePickerMsg, EditorMsg, TagSearchMsg:
        -- no case needed — compiler auto-wires

-- ══════════════════════════════════════════════════════
-- VIEW
-- ══════════════════════════════════════════════════════

view : Model -> Html Msg
view model =
    div [ class "app" ]
        [ viewNav model.page
        , case model.page of
            EditorPage ->
                viewEditor model
            PreviewPage ->
                viewPreview model
        ]

viewEditor : Model -> Html Msg
viewEditor model =
    div [ class "editor" ]
        [ h1 [] [ text "New Post" ]

        , div [ class "field" ]
            [ label [] [ text "Title" ]
            , input [ type_ "text", value model.title, onInput UpdateTitle ]
            ]

        , div [ class "field" ]
            [ label [] [ text "Publish Date" ]
            -- Just call the component's view.
            -- toMsg is auto-injected because:
            --   model.datePicker : DatePicker  (component type)
            --   DatePickerMsg : DatePicker.Msg  (matching wrapper)
            , DatePicker.view
                { onSelect = DateSelected
                , minDate = Just (Date.today ())
                , maxDate = Nothing
                , format = "YYYY-MM-DD"
                }
                model.datePicker
            ]

        , div [ class "field" ]
            [ label [] [ text "Content" ]
            , RichEditor.view
                { onChange = \_ -> NoOp
                , placeholder = "Write your post..."
                }
                model.editor
            ]

        , div [ class "field" ]
            [ label [] [ text "Tags" ]
            , div [ class "tags" ] (List.map viewTag model.tags)
            , Autocomplete.view
                { onSelect = TagSelected
                , fetch = searchTags
                , placeholder = "Search tags..."
                }
                model.tagSearch
            ]

        , button [ onClick Publish ] [ text "Publish" ]
        ]

viewTag : String -> Html Msg
viewTag tag =
    span [ class "tag" ]
        [ text tag
        , button [ onClick (RemoveTag tag) ] [ text "×" ]
        ]

viewNav : Page -> Html Msg
viewNav current =
    nav []
        [ button [ onClick (Navigate EditorPage) ] [ text "Edit" ]
        , button [ onClick (Navigate PreviewPage) ] [ text "Preview" ]
        ]

viewPreview : Model -> Html Msg
viewPreview model =
    div [ class "preview" ]
        [ h1 [] [ text model.title ]
        , p [] [ text (Maybe.withDefault "No date" (Maybe.map Date.toString model.publishDate)) ]
        , div [ class "tags" ] (List.map (\t -> span [ class "tag" ] [ text t ]) model.tags)
        , div [ class "body" ] [ raw (RichEditor.getContent model.editor) ]
        ]

main =
    app
        { init = init
        , update = update
        , view = view
        , subscriptions = \_ -> Sub.none
        , routes = [ route "/" EditorPage ]
        , notFound = EditorPage
        }
```

---

## 5. How the Compiler Detects Components

### Step 1: Identify component types in Model

The compiler scans Model fields and checks if the field type is a
component — a module that exports the required protocol shape:

```
Field: datePicker : DatePicker.DatePicker
  → Module DatePicker exports:
    DatePicker  ✓  (type, same name as module)
    Msg         ✓  (type)
    init        ✓  (DatePicker)
    update      ✓  (Msg -> DatePicker -> (DatePicker, Cmd Msg))
    view        ✓  (Config msg -> DatePicker -> Html msg)
  → This is a component. ✓

Field: title : String
  → String is not a component module. Skip.
```

### Step 2: Match Msg wrappers

```
DatePickerMsg DatePicker.Msg
  → wraps Msg type from DatePicker module
  → field datePicker : DatePicker in Model
  → match confirmed ✓

EditorMsg RichEditor.Msg
  → wraps Msg type from RichEditor module
  → field editor : RichEditor in Model
  → match confirmed ✓
```

The matching is by **type**, not just by name. The compiler checks that
`DatePicker.Msg` in the variant payload is the `Msg` type from the same
module that defines `DatePicker` (the component type). This is unambiguous.

### Step 3: Auto-generate missing update cases

If the developer's `update` function has no branch for `DatePickerMsg`,
the compiler generates:

```elm
-- Auto-generated:
DatePickerMsg subMsg ->
    let
        ( newState, subCmd ) =
            DatePicker.update subMsg model.datePicker
    in
    ( { model | datePicker = newState }
    , Cmd.map DatePickerMsg subCmd
    )
```

### Step 4: Auto-inject toMsg in view calls

When the compiler sees:

```elm
DatePicker.view { onSelect = DateSelected, ... } model.datePicker
```

It checks:
1. `DatePicker.view` expects a `Config msg` with a `toMsg` field
2. `model.datePicker` has type `DatePicker` (a component)
3. `DatePickerMsg` wraps `DatePicker.Msg` in the app's `Msg` type
4. The Config record literal is missing `toMsg`

→ Inject `toMsg = DatePickerMsg` into the record.

If `toMsg` is already provided, use the developer's value. No conflict.

---

## 6. The Compiler Verification

When a module is used as a component, the compiler checks the full protocol:

```
✓ DatePicker.DatePicker exists (type, same name as module)
✓ DatePicker.Msg exists (type)
✓ DatePicker.init : DatePicker (returns the component type)
✓ DatePicker.update : Msg -> DatePicker -> (DatePicker, Cmd Msg)
✓ DatePicker.view : Config msg -> DatePicker -> Html msg (with toMsg in Config)
```

If any piece is missing, the compiler gives a clear error:

```
Error: Module `DatePicker` is used as a component in Model
       but does not export the required interface.

       Missing: `update : Msg -> DatePicker -> (DatePicker, Cmd Msg)`

       A component module must export:
         TypeName  — the component state (same name as module)
         Msg       — internal message type
         init      — initial state value
         update    — Msg -> TypeName -> (TypeName, Cmd Msg)
         view      — Config msg -> TypeName -> Html msg
```

This turns the convention into a **verified protocol** with helpful
error messages.

---

## 7. Overriding Auto-Wiring

### Handle a component's messages explicitly

Just write the case. Compiler skips auto-generation for that component:

```elm
update msg model =
    case msg of
        -- Explicit: intercept DatePicker to also update publishDate
        DatePickerMsg subMsg ->
            let
                ( newPicker, subCmd ) =
                    DatePicker.update subMsg model.datePicker

                newPublishDate =
                    DatePicker.selected newPicker
            in
            ( { model
                | datePicker = newPicker
                , publishDate = newPublishDate
              }
            , Cmd.map DatePickerMsg subCmd
            )

        -- EditorMsg, TagSearchMsg: still auto-wired (no explicit case)
        ...
```

### Provide toMsg explicitly in view

```elm
DatePicker.view
    { toMsg = DatePickerMsg       -- explicit, compiler won't inject
    , onSelect = DateSelected
    , minDate = Nothing
    , maxDate = Nothing
    , format = "YYYY-MM-DD"
    }
    model.datePicker
```

### Use non-standard naming

If you don't want auto-wiring for a component, use a different Msg variant
name. The compiler only auto-wires when it finds a match:

```elm
type alias Model =
    { startDate : DatePicker          -- non-standard field name
    , endDate : DatePicker            -- two instances of same component
    }

type Msg
    = StartPickerMsg DatePicker.Msg   -- non-standard variant name
    | EndPickerMsg DatePicker.Msg
```

No auto-wiring here — the naming doesn't follow the convention.
You write the forwarding explicitly:

```elm
update msg model =
    case msg of
        StartPickerMsg subMsg ->
            let ( s, c ) = DatePicker.update subMsg model.startDate
            in ( { model | startDate = s }, Cmd.map StartPickerMsg c )

        EndPickerMsg subMsg ->
            let ( s, c ) = DatePicker.update subMsg model.endDate
            in ( { model | endDate = s }, Cmd.map EndPickerMsg c )
```

This is natural — if you have two instances of the same component,
you need explicit control anyway.

---

## 8. Multiple Instances of the Same Component

A common case: a form with two date pickers (start/end date).

```elm
type alias Model =
    { startDate : DatePicker
    , endDate : DatePicker
    }

type Msg
    = StartDateMsg DatePicker.Msg
    | EndDateMsg DatePicker.Msg
    | StartDateSelected Date
    | EndDateSelected Date
```

No auto-wiring kicks in (names don't match convention). Explicit forwarding:

```elm
update msg model =
    case msg of
        StartDateMsg subMsg ->
            let ( s, c ) = DatePicker.update subMsg model.startDate
            in ( { model | startDate = s }, Cmd.map StartDateMsg c )

        EndDateMsg subMsg ->
            let ( s, c ) = DatePicker.update subMsg model.endDate
            in ( { model | endDate = s }, Cmd.map EndDateMsg c )

        StartDateSelected date -> ...
        EndDateSelected date -> ...

view model =
    div []
        [ DatePicker.view
            { toMsg = StartDateMsg, onSelect = StartDateSelected, ... }
            model.startDate
        , DatePicker.view
            { toMsg = EndDateMsg, onSelect = EndDateSelected, ... }
            model.endDate
        ]
```

Explicit, clear, type-safe. The convention is a shortcut for the common
case (one instance). Multiple instances naturally require explicit wiring.

---

## 9. Component Diff Analysis

The compiler traces through component view functions just like any
other function call. Components don't create opaque boundaries:

```
Msg: DatePickerMsg NextMonth
  → auto-wired update runs DatePicker.update NextMonth model.datePicker
  → DatePicker.update changes: datePicker.viewing
  → view calls DatePicker.view with model.datePicker
  → DatePicker.view reads datePicker.viewing in viewCalendar
  → page guard: viewEditor only renders when model.page == EditorPage
  → result: if on EditorPage, re-render calendar grid only
  → result: if on PreviewPage, zero patches
```

Event sourcing on the wire:

```json
{ "msg": "DatePickerMsg.NextMonth", "args": [] }
{ "msg": "DatePickerMsg.SelectDate", "args": ["2026-04-15"] }
{ "msg": "UpdateTitle", "args": ["Hello World"] }
```

Component Msgs are namespaced with the variant name prefix.

---

## 10. Package Documentation Convention

Every component package should document usage like this:

```elm
{-| DatePicker — A date picker component for Sky.Live

## Quick Start

    import DatePicker exposing (DatePicker)

    -- 1. Add to your Model:
    type alias Model =
        { datePicker : DatePicker
        , selectedDate : Maybe Date
        }

    -- 2. Add to your Msg:
    type Msg
        = DatePickerMsg DatePicker.Msg
        | DateSelected Date

    -- 3. Init:
    init = { datePicker = DatePicker.init, selectedDate = Nothing }

    -- 4. View:
    DatePicker.view
        { onSelect = DateSelected
        , minDate = Nothing
        , maxDate = Nothing
        , format = "YYYY-MM-DD"
        }
        model.datePicker

    Steps 2-4 are auto-wired by the compiler when using
    the standard naming convention.

-}
```

---

## 11. Summary: The Component Protocol

### Package side (what you export):

```elm
module Foo exposing (Foo, Msg, Config, init, update, view, ...)

type alias Foo = { ... }                              -- state
type Msg = ...                                        -- internal messages
type alias Config msg = { toMsg : Msg -> msg, ... }   -- callbacks + options
init : Foo                                            -- initial state
update : Msg -> Foo -> ( Foo, Cmd Msg )               -- state machine
view : Config msg -> Foo -> Html msg                  -- render
```

### Consumer side (how you use it):

```elm
import Foo exposing (Foo)

type alias Model = { foo : Foo }                      -- store state
type Msg = FooMsg Foo.Msg | ...                       -- declare wrapper
init = { foo = Foo.init }                             -- init
-- update: auto-wired (or explicit)
-- view: Foo.view { onX = MyCallback } model.foo      -- toMsg auto-injected
```

### Compiler does:

| What | When |
|------|------|
| Verify component protocol | Field type exports `Foo`, `Msg`, `init`, `update`, `view` |
| Auto-generate update forwarding | Msg variant exists, no explicit case in update |
| Auto-inject `toMsg` in view | Config missing `toMsg`, component type matched in Model |
| Skip auto-wiring | Explicit case written, or naming doesn't follow convention |
| Error with guidance | Module used as component but missing required exports |
