module Sky.Build.WebviewAppSpec (spec) where

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


-- Issue #356 / v0.1 MVP — Sky.Webview backend.
--
-- This spec pins:
--
--   1. `import Std.Webview` resolves; `Webview.app cfg` type-checks
--      against the closed-record signature
--      (init / update / view / subscriptions / window).
--
--   2. The codegen routes `Webview.app` to `rt.Webview_app` (the
--      default Mod_Func fallback in kernelToGo).
--
--   3. A program missing the `window` field FAILS to compile (the
--      closed-record sig surfaces a clean HM type error rather than
--      the runtime "cfg must define …" panic).
--
-- The runtime smoke test (`go test ./rt -run Webview` + interactive
-- `sky build && ./sky-out/app`) live under runtime-go/rt/webview_test.go
-- and examples/31-webview-stopwatch-ui respectively.
--
-- Tier 1 (task #491): no subprocess `sky build` — the compile
-- pipeline runs IN-PROCESS via Sky.Build.Helpers.InProcessCompile.
spec :: Spec
spec = describe "Std.Webview.app (issue #356, v0.1 MVP)" $ do
    it "type-checks + builds a minimal Webview.app program with all required fields" $ do
        result <- compileInProcess validFixture
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk body -> do
                -- The Webview.app call site lowers to rt.Webview_app
                -- via the default Mod_Func kernelToGo fallback (no
                -- explicit Kernel.hs entry needed, same as
                -- rt.Tui_app / rt.Cli_program).
                let routesToWebviewApp = "rt.Webview_app(" `isInfixOf` body
                routesToWebviewApp `shouldBe` True

    it "rejects a Webview.app call missing the required window field" $ do
        result <- compileInProcess missingWindowFixture
        case result of
            CompileOk _ ->
                expectationFailure
                    "expected compile failure for Webview.app missing required `window` field"
            CompileErr combined -> do
                -- HM-level rejection — closed record sig should error,
                -- NOT a runtime "cfg must define" panic. The compile
                -- pipeline's Left return value is the one-line marker
                -- `"Type error: <path>"` (see Compile.hs:1613); the
                -- full rendered TYPE ERROR block streams via stdout
                -- which the in-process helper silences.  The marker
                -- substring is enough to fence the regression.
                let isTypeError = "Type error" `isInfixOf` combined
                isTypeError `shouldBe` True

  where
    validFixture :: String
    validFixture =
        "module Main exposing (main)\n\n\
        \import Sky.Core.Prelude exposing (..)\n\
        \import Sky.Core.Task as Task\n\
        \import Std.Webview as Webview\n\
        \import Std.Cmd as Cmd\n\
        \import Std.Sub as Sub\n\
        \import Std.Ui as Ui\n\
        \import Std.Ui exposing (Element)\n\n\n\
        \type alias Model = { count : Int }\n\n\
        \type Msg = Inc | Dec | NoOp\n\n\
        \init : () -> ( Model, Cmd Msg )\n\
        \init _ = ( { count = 0 }, Cmd.none )\n\n\
        \update : Msg -> Model -> ( Model, Cmd Msg )\n\
        \update msg model =\n\
        \    case msg of\n\
        \        Inc -> ( { model | count = model.count + 1 }, Cmd.none )\n\
        \        Dec -> ( { model | count = model.count - 1 }, Cmd.none )\n\
        \        NoOp -> ( model, Cmd.none )\n\n\
        \subscriptions : Model -> Sub Msg\n\
        \subscriptions _ = Sub.none\n\n\
        \view : Model -> Element Msg\n\
        \view model =\n\
        \    Ui.column [] [ Ui.text (String.fromInt model.count) ]\n\n\
        \main =\n\
        \    Webview.app\n\
        \        { init = init\n\
        \        , update = update\n\
        \        , view = view\n\
        \        , subscriptions = subscriptions\n\
        \        , window = { title = \"Test\", size = ( 800, 600 ) }\n\
        \        }\n\
        \        |> Task.run\n"

    -- Missing the `window` field. The closed-record signature on the
    -- type-checker arm should reject this at compile time.
    missingWindowFixture :: String
    missingWindowFixture =
        "module Main exposing (main)\n\n\
        \import Sky.Core.Prelude exposing (..)\n\
        \import Sky.Core.Task as Task\n\
        \import Std.Webview as Webview\n\
        \import Std.Cmd as Cmd\n\
        \import Std.Sub as Sub\n\
        \import Std.Ui as Ui\n\
        \import Std.Ui exposing (Element)\n\n\n\
        \type alias Model = { count : Int }\n\
        \type Msg = NoOp\n\n\
        \init : () -> ( Model, Cmd Msg )\n\
        \init _ = ( { count = 0 }, Cmd.none )\n\n\
        \update : Msg -> Model -> ( Model, Cmd Msg )\n\
        \update _ model = ( model, Cmd.none )\n\n\
        \subscriptions : Model -> Sub Msg\n\
        \subscriptions _ = Sub.none\n\n\
        \view : Model -> Element Msg\n\
        \view model = Ui.text (String.fromInt model.count)\n\n\
        \main =\n\
        \    Webview.app\n\
        \        { init = init\n\
        \        , update = update\n\
        \        , view = view\n\
        \        , subscriptions = subscriptions\n\
        \        }\n\
        \        |> Task.run\n"
