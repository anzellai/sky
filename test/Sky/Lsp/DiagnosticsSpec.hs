{-# LANGUAGE OverloadedStrings #-}
module Sky.Lsp.DiagnosticsSpec (spec) where

-- LSP diagnostic-parity specs. The editor must see every error
-- `sky check` / `sky build` sees — anything less is a developer-
-- experience regression. This file asserts the signal flows through
-- `textDocument/publishDiagnostics` for:
--
--   * non-exhaustive case expressions (Gap 2a)
--   * undefined/unbound names (Gap 2b, depends on Gap 1)
--
-- Infrastructure note: `awaitNotification` in Sky.Lsp.Harness waits
-- for the server-pushed diagnostic (no request/response correlation).
-- Pre-harness, ProtocolSpec/CapabilitiesSpec discarded such
-- notifications while draining for a response.

import Test.Hspec
import qualified Data.Aeson as Aeson
import Data.Aeson (Value(..))
import qualified Data.Aeson.KeyMap as KM
import qualified Data.Text as T
import qualified Data.Vector as V
import System.Directory (createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)

import Sky.Lsp.Harness
    ( findSky, withLsp
    , initializeLsp, didOpen
    , awaitNotification
    )


setupProject :: FilePath -> String -> IO FilePath
setupProject dir src = do
    let srcDir = dir </> "src"
        fixture = srcDir </> "Main.sky"
        toml = dir </> "sky.toml"
    createDirectoryIfMissing True srcDir
    writeFile toml "name = \"lsp-diag\"\nentry = \"src/Main.sky\"\n"
    writeFile fixture src
    return fixture


-- | Extract the list of diagnostic message strings from a
-- publishDiagnostics notification payload.
diagnosticMessages :: Aeson.Value -> [T.Text]
diagnosticMessages v = case v of
    Object o -> case KM.lookup "params" o of
        Just (Object p) -> case KM.lookup "diagnostics" p of
            Just (Array arr) -> concatMap getMsg (V.toList arr)
            _ -> []
        _ -> []
    _ -> []
  where
    getMsg (Object d) = case KM.lookup "message" d of
        Just (String t) -> [t]
        _ -> []
    getMsg _ = []


anyMatch :: T.Text -> [T.Text] -> Bool
anyMatch needle = any (needle `T.isInfixOf`)


spec :: Spec
spec = do
    describe "LSP publishes diagnostics for every Sky-level error" $ do

        it "Gap 2a — non-exhaustive case surfaces as an editor diagnostic" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type Colour = Red | Green | Blue"
                    , ""
                    , "name c ="
                    , "    case c of"
                    , "        Red -> \"red\""
                    , "        Green -> \"green\""
                    , ""
                    , "main = println (name Red)"
                    ]
            withSystemTempDirectory "sky-lsp-diag-exhaust" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    -- Server pushes publishDiagnostics after didOpen.
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics notification within budget"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            anyMatch "Non-exhaustive" msgs `shouldBe` True
                            -- The missing constructor should be named.
                            anyMatch "Blue" msgs `shouldBe` True

        it "Gap 2b — undefined name surfaces as an editor diagnostic" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println messgae"
                    ]
            withSystemTempDirectory "sky-lsp-diag-unbound" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics notification within budget"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            anyMatch "Undefined name" msgs `shouldBe` True
                            anyMatch "messgae" msgs `shouldBe` True

        it "issue #52 — partial application at stdlib boundary produces a diagnostic" $ do
            -- Regression for issue #52
            -- (https://github.com/anzellai/sky/issues/52): a Sky.Live
            -- view function calling `Ui.layout [] (codeSection)` —
            -- where codeSection : Model -> Element — should be flagged
            -- by the LSP. Pre-fix, this compiled (and crashed at
            -- runtime) AND the LSP showed no red squiggles. The fix
            -- (cross-module externals threaded into runPipelineSt)
            -- makes HM see Ui.layout's signature, so the partial
            -- application surfaces as a real type-error diagnostic.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , "import Std.Ui as Ui"
                    , "import Std.Ui exposing (Element)"
                    , ""
                    , "type alias Model = { count : Int }"
                    , ""
                    , "codeSection : Model -> Element msg"
                    , "codeSection model ="
                    , "    Ui.text (String.fromInt model.count)"
                    , ""
                    , "viewBuggy : Model -> any"
                    , "viewBuggy model ="
                    , "    Ui.layout [] codeSection"
                    , ""
                    , "main = println (toString (viewBuggy { count = 0 }))"
                    ]
            withSystemTempDirectory "sky-lsp-issue52" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics notification within budget"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            -- The diagnostic should mention the
                            -- mis-applied identifier and the type
                            -- mismatch. Phrasing: "Foreign 'Main.codeSection':
                            -- (Model) -> Element a vs Element a"
                            anyMatch "codeSection" msgs `shouldBe` True
                            anyMatch "Element" msgs `shouldBe` True

        it "Limitation #19 — Tui.app missing required field surfaces a diagnostic" $ do
            -- Closed-record HM kernel sig (commit follows this test)
            -- enforces presence of init / update / view / subscriptions
            -- on Tui.app's cfg. A missing field used to compile silently
            -- and panic at runtime — now both `sky check` and the LSP
            -- flag it at type-check time. This test omits
            -- `subscriptions` to assert the diagnostic surfaces.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Task as Task"
                    , "import Std.Cmd as Cmd"
                    , "import Std.Tui as Tui"
                    , "import Std.Ui as Ui"
                    , "import Std.Ui exposing (Element)"
                    , ""
                    , "type alias Model = { count : Int }"
                    , "type Msg = NoOp"
                    , ""
                    , "init : a -> ( Model, Cmd Msg )"
                    , "init _ = ( { count = 0 }, Cmd.none )"
                    , ""
                    , "update : Msg -> Model -> ( Model, Cmd Msg )"
                    , "update _ model = ( model, Cmd.none )"
                    , ""
                    , "view : Model -> Element Msg"
                    , "view _ = Ui.text \"hi\""
                    , ""
                    , "main ="
                    , "    Tui.app"
                    , "        { init = init"
                    , "        , update = update"
                    , "        , view = view"
                    , "        }"
                    , "        |> Task.run"
                    ]
            withSystemTempDirectory "sky-lsp-tui-missing" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on missing-field file"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            -- Diagnostic should mention the missing
                            -- field explicitly. Sky's record-mismatch
                            -- renderer lists every field the user
                            -- gave AND every field expected, so
                            -- "subscriptions" appears in the
                            -- expected-shape list.
                            anyMatch "Type" msgs `shouldBe` True
                            anyMatch "subscriptions" msgs `shouldBe` True

        it "issue #52 corrected — applying the missing arg clears the diagnostic" $ do
            -- Positive control for the fix above: change `codeSection`
            -- to `codeSection model` and the LSP should report empty
            -- diagnostics. Without this, a false-positive could leave
            -- the squiggle on every save indefinitely.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , "import Std.Ui as Ui"
                    , "import Std.Ui exposing (Element)"
                    , ""
                    , "type alias Model = { count : Int }"
                    , ""
                    , "codeSection : Model -> Element msg"
                    , "codeSection model ="
                    , "    Ui.text (String.fromInt model.count)"
                    , ""
                    , "viewCorrect : Model -> any"
                    , "viewCorrect model ="
                    , "    Ui.layout [] (codeSection model)"
                    , ""
                    , "main = println (toString (viewCorrect { count = 0 }))"
                    ]
            withSystemTempDirectory "sky-lsp-issue52-fix" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics notification within budget"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            msgs `shouldBe` []

        it "clean file produces a diagnostics notification with an empty array" $ do
            -- Positive control: a valid file should still trigger
            -- publishDiagnostics (empty), so editors that cache
            -- diagnostics clear stale state.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println \"hi\""
                    ]
            withSystemTempDirectory "sky-lsp-diag-clean" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on clean file"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            msgs `shouldBe` []

        it "user-defined monadic-do chain on Result produces no diagnostics" $ do
            -- Regression for Bug #1: previously `sky check` passed but
            -- `go build` failed on this pattern. LSP piggy-backs on the
            -- sky-check pipeline (Parse → Canonicalise → Constrain →
            -- Solve → Exhaustiveness), so a false-positive LSP
            -- diagnostic here would mean the type solver was spuriously
            -- rejecting a valid user-HOF chain. Empty diagnostics =>
            -- LSP agrees with the fixed codegen.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Result as Result"
                    , "import Sky.Core.Error as Error exposing (Error)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "do : Result Error a -> (a -> Result Error b) -> Result Error b"
                    , "do result fn ="
                    , "    Result.andThen fn result"
                    , ""
                    , "pipeline : String -> Result Error (String, String)"
                    , "pipeline key ="
                    , "    do (firstStep key) (\\a ->"
                    , "    do (secondStep a) (\\b ->"
                    , "    Ok (a, b)))"
                    , ""
                    , "firstStep : String -> Result Error String"
                    , "firstStep key ="
                    , "    if key == \"\" then"
                    , "        Err (Error.invalidInput \"empty key\")"
                    , "    else"
                    , "        Ok (\"first:\" ++ key)"
                    , ""
                    , "secondStep : String -> Result Error String"
                    , "secondStep a ="
                    , "    Ok (\"second:\" ++ a)"
                    , ""
                    , "main ="
                    , "    case pipeline \"hello\" of"
                    , "        Ok (a, b) ->"
                    , "            println (\"ok \" ++ a ++ \" \" ++ b)"
                    , ""
                    , "        Err e ->"
                    , "            println (errorToString e)"
                    ]
            withSystemTempDirectory "sky-lsp-diag-user-hof" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on user-HOF chain"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            msgs `shouldBe` []

        it "TEA with Live.app: wrong view return type surfaces as a real diagnostic" $ do
            -- Pre-Limitation-#19-fix this was a "the LSP suppresses
            -- false positives" test. With the closed-record HM kernel
            -- sigs landed (Live.app / Tui.app / Cli.program) PLUS
            -- the field-name-aware error rendering, a wrong-typed
            -- view function is now both:
            --   1. Rejected by `sky check` (always was — the user
            --      kernel sig had this; the LSP was hiding it).
            --   2. Rejected by the LSP with the SAME informative
            --      diagnostic, listing the expected view return
            --      type (VNode) vs what the user wrote (String).
            -- This test fixture deliberately writes `view _ = "hi"`
            -- to assert the diagnostic surfaces.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Cmd as Cmd"
                    , "import Std.Sub as Sub"
                    , "import Std.Live exposing (app)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Model = { count : Int }"
                    , "type Msg = Tick"
                    , ""
                    , "init : a -> ( Model, Cmd Msg )"
                    , "init _ ="
                    , "    ( { count = 0 }, Cmd.none )"
                    , ""
                    , "update : Msg -> Model -> ( Model, Cmd Msg )"
                    , "update msg model ="
                    , "    ( model, Cmd.none )"
                    , ""
                    , "view : Model -> any"
                    , "view _ = \"hi\""
                    , ""
                    , "subscriptions _ = Sub.none"
                    , ""
                    , "main ="
                    , "    app"
                    , "        { init = init"
                    , "        , update = update"
                    , "        , view = view"
                    , "        , subscriptions = subscriptions"
                    , "        , routes = []"
                    , "        , notFound = ()"
                    , "        }"
                    ]
            withSystemTempDirectory "sky-lsp-tea-app" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on TEA-app file"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            -- The diagnostic surfaces with field-
                            -- aware rendering — `view : (Model) ->
                            -- String` vs `view : (Model) -> VNode`.
                            anyMatch "Type" msgs `shouldBe` True
                            anyMatch "view" msgs `shouldBe` True
                            anyMatch "VNode" msgs `shouldBe` True
