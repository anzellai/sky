module Sky.Build.UiMultilineTextareaSpec (spec) where

-- Regression fence for the `Std.Ui.Input.multiline` <textarea> bug.
--
-- Pre-fix shape: `multiline` called `inputBase "textarea"` which
-- built a `Ui.input` element with `type="textarea"` set as an HTML
-- attribute. But:
--
--   1. `<input type="textarea">` is NOT a valid HTML form control.
--      Every browser silently falls back to a single-line text
--      input, so multi-line editing was broken in the browser.
--   2. `Std.Ui`'s `dispatchTag` had no `"textarea"` case — the
--      `TaggedNode "textarea"` shape would have fallen through to
--      `Html.div`, hiding any future fix attempt.
--
-- User-facing symptom (mini-notion page-body editor): typing
-- newlines submitted the form instead of inserting a line break;
-- the editor was visually a one-line field that swallowed any
-- attempt at multi-line content.
--
-- Fix (two halves, both required):
--
--   * `Std.Ui.dispatchTag`: added a `"textarea"` branch routing to
--     `Html.textarea attrs children`.
--   * `Std.Ui.Input.multiline`: now emits `Ui.TaggedNode "textarea"`
--     directly instead of routing through `inputBase "textarea"`.
--     The `value` attr is preserved (Sky.Live's renderer strips it
--     and splices it as text content — see
--     `runtime-go/rt/live.go`'s textarea special case); the
--     `type="textarea"` attr is kept too because Sky.Tui's
--     `walkAttrs` reads it to identify multi-line editors.
--
-- This spec compiles a tiny Std.Ui project that uses `Input.multiline`
-- and asserts the lowered Go routes through `Std_Html_textarea`. If
-- the bug regresses (multiline falls back to `Html.input` or the
-- dispatchTag textarea branch goes missing), the lowered code stops
-- referencing `Std_Html_textarea` and the spec fails.

import Test.Hspec
import qualified System.Exit as Exit
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import System.IO.Temp (withSystemTempDirectory)
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


buildAndReadMain :: String -> IO (Int, String, String)
buildAndReadMain src =
    withSystemTempDirectory "sky-ui-multiline" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml")
            "name = \"tmp\"\nversion = \"0.0.0\"\n"
        let buildCmd = "cd " ++ tmp ++ " && " ++ sky
                       ++ " build src/Main.sky 2>&1"
        (bec, bout, berr) <- readCreateProcessWithExitCode
                              (shell buildCmd) ""
        case bec of
            Exit.ExitFailure n -> return (n, "",
                "build failed: " ++ bout ++ berr)
            Exit.ExitSuccess -> do
                main_go <- readFile (tmp </> "sky-out" </> "main.go")
                return (0, main_go, "")


spec :: Spec
spec = describe "Std.Ui.Input.multiline emits a real <textarea>" $ do

    it "lowered code routes the multiline element through Std_Html_textarea" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Cmd as Cmd"
                , "import Std.Sub as Sub"
                , "import Std.Live exposing (app, route)"
                , "import Std.Ui as Ui"
                , "import Std.Ui exposing (Element)"
                , "import Std.Ui.Input as Input"
                , ""
                , "type Msg = EditDraft String"
                , "type alias Model = { draft : String }"
                , ""
                , "init _ = ({ draft = \"hello\" }, Cmd.none)"
                , ""
                , "update msg model ="
                , "    case msg of"
                , "        EditDraft s -> ({ model | draft = s }, Cmd.none)"
                , ""
                , "subs _ = Sub.none"
                , ""
                , "view : Model -> any"
                , "view model ="
                , "    Ui.layout []"
                , "        (Input.multiline"
                , "            [ Ui.width Ui.fill ]"
                , "            { onChange = EditDraft"
                , "            , text = model.draft"
                , "            , placeholder = Just (Input.placeholder [] (Ui.text \"x\"))"
                , "            , label = Input.labelHidden \"Body\""
                , "            , spellcheck = True"
                , "            })"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildAndReadMain src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- POSITIVE: the lowered code must route a "textarea" tag
        -- through `Std_Html_textarea`. Without the dispatchTag
        -- branch, the lowered dispatch falls through to
        -- `Std_Html_div` and the `<textarea>` never appears in the
        -- rendered HTML.
        mainGo `shouldSatisfy` ("Std_Html_textarea" `isInfixOf`)
        -- POSITIVE: the multiline body must build a TaggedNode with
        -- the literal "textarea" tag string. Pre-fix it built a
        -- TaggedNode "input" via inputBase, so the "textarea"
        -- literal only appeared as the type-attr argument — never
        -- as the TaggedNode constructor tag.
        mainGo `shouldSatisfy`
            ("Std_Ui_Element_TaggedNode(\"textarea\"" `isInfixOf`)
