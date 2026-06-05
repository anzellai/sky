module Sky.Build.WebviewAppSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


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
spec :: Spec
spec = describe "Std.Webview.app (issue #356, v0.1 MVP)" $ do
    it "type-checks + builds a minimal Webview.app program with all required fields" $ do
        sky <- findSky
        withSystemTempDirectory "sky-webview-app" $ \tmp -> do
            writeFixture tmp validFixture
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
                then expectationFailure $
                    "sky build failed.\n" ++ out ++ "\n" ++ errOut
                else do
                    built <- doesFileExist (tmp </> "sky-out" </> "app")
                    built `shouldBe` True
                    body <- readFile (tmp </> "sky-out" </> "main.go")
                    -- The Webview.app call site lowers to rt.Webview_app
                    -- via the default Mod_Func kernelToGo fallback (no
                    -- explicit Kernel.hs entry needed, same as
                    -- rt.Tui_app / rt.Cli_program).
                    let routesToWebviewApp = "rt.Webview_app(" `isInfixOf` body
                    routesToWebviewApp `shouldBe` True

    it "rejects a Webview.app call missing the required window field" $ do
        sky <- findSky
        withSystemTempDirectory "sky-webview-app-miss" $ \tmp -> do
            writeFixture tmp missingWindowFixture
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            -- HM-level rejection — closed record sig should error,
            -- NOT a runtime "cfg must define" panic. The build
            -- terminates non-zero with a TYPE ERROR diagnostic
            -- mentioning AppCfg (the inferred required shape).
            ec `shouldNotBe` ExitSuccess
            let combined = out ++ "\n" ++ errOut
            -- sky's diagnostic surfaces "TYPE ERROR" + the expected
            -- AppCfg shape with the window field present and the
            -- actual shape missing it. Either substring indicates
            -- a clean compile-time rejection (vs the runtime panic
            -- "cfg must define" path).
            let isTypeError =
                    "TYPE ERROR" `isInfixOf` combined
                    || "AppCfg" `isInfixOf` combined
                    || "Type mismatch" `isInfixOf` combined
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

    writeFixture :: FilePath -> String -> IO ()
    writeFixture tmp src = do
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "sky.toml")
            ("name = \"webview-app-test\"\n"
                ++ "version = \"0.0.0\"\n"
                ++ "entry = \"src/Main.sky\"\n\n"
                ++ "[source]\nroot = \"src\"\n")
        writeFile (tmp </> "src" </> "Main.sky") src

    findSky :: IO FilePath
    findSky = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok
            then return candidate
            else return "sky"

    runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
    runSky sky args cwd =
        readCreateProcessWithExitCode
            ((proc sky args) { cwd = Just cwd })
            ""
