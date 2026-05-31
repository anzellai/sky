module Sky.Build.UiMediaQuerySpec (spec) where

-- Regression fence for the Std.Ui media-query primitive
-- (`Ui.breakpoint` / `Ui.mediaQuery`, issue #376).
--
-- The compile-side contract: a Sky source that calls Ui.breakpoint
-- builds to Go output containing the literal marker strings the
-- runtime expects to find on the wrapper element
-- (`data-sky-mq-q` + `data-sky-mq-rules`) AND the breakpoint
-- expansion (`(max-width: 767px)` etc.) lowered into the emitted
-- Go body. If the v0.15.x typed-codegen path stops lowering the
-- Std.Ui Sky-source case branches, this spec fires.
--
-- Pairs with runtime-go/rt/live.go's `injectMediaQueryStyles`
-- (the half that actually wraps the marker attrs into a scoped
-- `<style>` child at render time).

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


buildMainGo :: String -> IO (Int, String, String)
buildMainGo src =
    withSystemTempDirectory "sky-ui-mq" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"tmp\"\nversion = \"0.0.0\"\n"
        let buildCmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (bec, bout, berr) <- readCreateProcessWithExitCode (shell buildCmd) ""
        case bec of
            Exit.ExitFailure n -> return (n, "", "build failed: " ++ bout ++ berr)
            Exit.ExitSuccess -> do
                main_go <- readFile (tmp </> "sky-out" </> "main.go")
                return (0, main_go, "")


spec :: Spec
spec = describe "Std.Ui media-query primitive" $ do

    it "compiles Ui.breakpoint Ui.mobile and embeds the marker + CSS" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Ui.Background as Background"
                , "import Std.Live exposing (app, route)"
                , ""
                , "type Msg = Tick"
                , "type alias Model = { x : Int }"
                , ""
                , "init _ = ({x = 0}, Cmd.none)"
                , "update _ m = (m, Cmd.none)"
                , "subs _ = Sub.none"
                , ""
                , "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Ui.breakpoint Ui.mobile"
                , "            [ Ui.padding 8"
                , "            , Background.color (Ui.rgb 240 0 0)"
                , "            ]"
                , "            (Ui.el [] (Ui.text \"phone-only red border\")))"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- Sky's Std.Ui.breakpoint expands to mediaQuery, which sets
        -- `AttrAttribute "data-sky-mq-q" query` + `AttrAttribute
        -- "data-sky-mq-rules" rulesCss` on the wrapper. The
        -- compile-time evaluation of `breakpointToQuery Mobile`
        -- produces `(max-width: 767px)`, which lands as a literal in
        -- the lowered Go.
        mainGo `shouldSatisfy` ("data-sky-mq-q" `isInfixOf`)
        mainGo `shouldSatisfy` ("data-sky-mq-rules" `isInfixOf`)
        mainGo `shouldSatisfy` ("(max-width: 767px)" `isInfixOf`)

    it "Ui.breakpoint Ui.darkMode lowers to prefers-color-scheme" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Live exposing (app, route)"
                , ""
                , "type Msg = Tick"
                , "type alias Model = { x : Int }"
                , ""
                , "init _ = ({x = 0}, Cmd.none)"
                , "update _ m = (m, Cmd.none)"
                , "subs _ = Sub.none"
                , ""
                , "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Ui.breakpoint Ui.darkMode [] (Ui.text \"dark mode\"))"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy` ("(prefers-color-scheme: dark)" `isInfixOf`)

    it "Ui.mediaQuery escape hatch passes a raw query string through" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Live exposing (app, route)"
                , ""
                , "type Msg = Tick"
                , "type alias Model = { x : Int }"
                , ""
                , "init _ = ({x = 0}, Cmd.none)"
                , "update _ m = (m, Cmd.none)"
                , "subs _ = Sub.none"
                , ""
                , "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Ui.mediaQuery \"(orientation: portrait)\""
                , "            [ Ui.padding 4 ]"
                , "            (Ui.text \"portrait only\"))"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy` ("(orientation: portrait)" `isInfixOf`)
