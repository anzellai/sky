# Sky in two programs

This page holds the full programs that `README.md` shows. Each one is a complete
`module Main`, and `scripts/doc-examples.sh` type-checks it and runs `go build`
on it in CI, so the samples in the README cannot rot. A README sample is a copy
of a part of a program on this page. The test
`rust/crates/xtask/tests/readme_samples_are_built.rs` fails when the two drift.

## A full-stack todo app with a database

One file. The server owns the model, each click runs `update` on the server, and
Sky.Live patches the page over one SSE connection. There is no JavaScript to
write, no ORM and no hand-written SQL: one `Codec` drives the table, the reads
and the writes. `Store.migrate` creates the table, and adds a column when the
record gains a field.

Run it with `sky run src/Main.sky` and open http://localhost:8000. To move to
PostgreSQL, change `Sqlite "todos.db"` to `Postgres "postgres://..."`. Nothing
else changes.

```elm
module Main exposing (main, config)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Sky.Core.List as List
import Sky.Core.Task as Task exposing (Task)
import Sky.Config as Config exposing (Database(..))
import Std.App as App
import Std.Cmd as Cmd
import Std.Sub as Sub
import Std.Codec as Codec
import Std.Db as Db exposing (Db)
import Std.Db.Store as Store exposing (Store)
import Std.Ui as Ui exposing (Element)
import Std.Ui.Input as Input


config : Config.Config
config =
    Config.default |> Config.withDatabase (Sqlite "todos.db")

type alias Todo =
    { id : Int
    , title : String
    }

type alias Model =
    { todos : List Todo
    , draft : String
    , problem : String
    }

type Msg
    = Draft String
    | Add
    | Loaded (Result Error (List Todo))


todos : Store Todo
todos =
    Store.fromCodec "todos" (Codec.auto { id = 0, title = "" })
        |> Store.serial "id"


addTodo : Db -> String -> Task Error (List Todo)
addTodo db title =
    Store.insert db todos { id = 0, title = title }
        |> Task.andThen (\_ -> Store.all db todos)


update : Db -> Msg -> Model -> ( Model, Cmd Msg )
update db msg model =
    case msg of

        Draft text ->
            ( { model | draft = text }, Cmd.none )

        Add ->
            ( { model | draft = "" }
            , Cmd.perform (addTodo db model.draft) Loaded
            )

        Loaded (Ok rows) ->
            ( { model | todos = rows, problem = "" }, Cmd.none )

        Loaded (Err e) ->
            ( { model | problem = errorToString e }, Cmd.none )


view : Model -> Element Msg
view model =
    Ui.column
        [ Ui.padding 24, Ui.spacing 8 ]
        ([ Input.text
              []
              { onChange = Draft
              , text = model.draft
              , placeholder = Nothing
              , label = Input.labelHidden "New todo"
              }
        , Ui.button [] { onPress = Just Add, label = Ui.text "Add" }
        , Ui.text model.problem
        ]
            ++ List.map (\todo -> Ui.text todo.title) model.todos)


app db =
    App.app
        { init =
            \_ ->
                ( { todos = [], draft = "", problem = "" }
                , Cmd.perform (Store.all db todos) Loaded
                )
        , update = update db
        , view = view
        , subscriptions = \_ -> Sub.none
        }
        |> App.withNotFound ()


start : Db -> Task Error ()
start db =
    Store.migrate db todos |> Task.andThen (\_ -> App.run (app db))


main : Task Error ()
main =
    Db.connect () |> Task.andThen start
```

## A durable AI agent that the model cannot overrule

The model drafts a reply. A pure `firewall` function, not the model, decides
whether the reply may go out. Each `Durable.step` is written to a journal in the
database, so when the process crashes and starts again, the run continues from
its last step and does not pay for the same model call twice.

`deliver` is where your own mailer goes. Set `OPENAI_API_KEY`, or give
`Provider.compatible` the URL of any OpenAI-compatible endpoint, a local model
included. The full Slack bot is
[`examples/66-slack-agent`](../examples/66-slack-agent).

```elm
module Main exposing (main)

import Sky.Core.Prelude exposing (..)
import Sky.Core.Error exposing (Error)
import Sky.Core.Secret as Secret
import Sky.Core.Task as Task exposing (Task)
import Std.Ai.Policy as Policy
import Std.Ai.Provider as Provider exposing (Provider)
import Std.Codec as Codec exposing (Codec)
import Std.Db as Db exposing (Db)
import Std.Durable as Durable exposing (Ctx)
import Std.Log exposing (println)

type alias Ticket =
    { customer : String
    , text : String
    }


ticketCodec : Codec Ticket
ticketCodec =
    Codec.auto { customer = "", text = "" }


chatCodec : Codec Provider.ChatResponse
chatCodec =
    Codec.auto
        { content = ""
        , usage = { promptTokens = 0, completionTokens = 0, totalTokens = 0 }
        }


provider : Provider
provider =
    Provider.openai (Secret.fromEnv "OPENAI_API_KEY") "gpt-4o-mini"


firewall : Policy.Action -> Policy.Decision
firewall act =
    Policy.requireApprovalAbove Policy.Low act


replyBody : Ctx -> Ticket -> Task Error String
replyBody ctx ticket =
    Durable.step
        ctx
        chatCodec
        "draft"
        (Provider.chat
            provider
            [ Provider.system "Draft a short, kind reply."
            , Provider.user ticket.text
            ])
        |> Task.andThen
               (\draft ->
                   Durable.step
                       ctx
                       Codec.string
                       "send"
                       (send ticket draft.content))


send : Ticket -> String -> Task Error String
send ticket reply =
    Policy.gate
        firewall
        (Policy.action "email.send" ticket.customer Policy.Low)
        (deliver ticket reply)
        |> Task.map outcome


deliver : Ticket -> String -> Task Error String
deliver ticket reply =
    println ("To " ++ ticket.customer ++ ": " ++ reply)
        |> Task.map (\_ -> "sent")


outcome : Policy.Outcome String -> String
outcome result =
    case result of

        Policy.Ran sent ->
            sent

        Policy.Denied reason ->
            "denied: " ++ reason

        Policy.Pending reason ->
            "waiting for a human: " ++ reason


replyDef : Durable.WorkflowDef Ticket String
replyDef =
    Durable.register "support-reply" ticketCodec Codec.string replyBody


drain : Db -> Task Error ()
drain db =
    Durable.poll db [ Durable.erase replyDef ]
        |> Task.andThen
               (\didWork ->
                   if didWork then
                       drain db

                   else
                       Task.succeed ())


ticket : Ticket
ticket =
    { customer = "ada@example.com", text = "My invoice is wrong." }


run : Db -> Task Error ()
run db =
    Durable.setup db
        |> Task.andThen (\_ -> Durable.start db replyDef "ticket-42" ticket)
        |> Task.andThen (\_ -> drain db)


main : Task Error ()
main =
    Db.connect () |> Task.andThen run
```
