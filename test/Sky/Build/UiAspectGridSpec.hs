module Sky.Build.UiAspectGridSpec (spec) where

-- Regression fence for the Std.Ui aspect-ratio + content-aware
-- grid track primitives (`Ui.aspectRatio` / `Ui.aspectRatioWH`
-- + `Std.Ui.Grid.tracks`/`columns`/`rows`, issue #379).
--
-- The compile-side contract: a Sky source that calls these
-- helpers builds to Go output containing the literal CSS the
-- runtime will emit on the wire (`aspect-ratio: 16 / 9;` literal,
-- `grid-template-columns: 1fr 200px 1fr;` literal, etc.). If the
-- v0.15.x typed-codegen path stops lowering the new Std.Ui.Grid
-- ADT branches, or the literal strings drift, this spec fires.
--
-- Pairs with `scripts/verify-ui-showcase.mjs` (the visual / DOM
-- snapshot half that confirms the browser actually applies the
-- styles at multiple viewport widths). Compile-time + run-time
-- gates together — neither alone catches everything.

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
    withSystemTempDirectory "sky-ui-aspect-grid" $ \tmp -> do
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
spec = describe "Std.Ui aspect-ratio + grid track primitives (#379)" $ do

    it "Ui.aspectRatioWH lowers to literal `aspect-ratio: <w> / <h>` CSS key + value" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Live exposing (app, route)"
                , ""
                , "type alias Model = { x : Int }"
                , ""
                , "init _ = ({x = 0}, Cmd.none)"
                , "update _ m = (m, Cmd.none)"
                , "subs _ = Sub.none"
                , ""
                , "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Ui.el [ Ui.aspectRatioWH 16 9, Ui.width Ui.fill ] Ui.none)"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- The compile-time evaluation of `aspectRatioWH 16 9` produces
        -- `aspect-ratio` + `16 / 9` String literals that end up in the
        -- emitted Go's CSS-emission path.
        mainGo `shouldSatisfy` ("aspect-ratio" `isInfixOf`)
        mainGo `shouldSatisfy` ("16 / 9" `isInfixOf`)

    it "Ui.aspectRatio Float lowers to literal decimal aspect-ratio value" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Live exposing (app, route)"
                , ""
                , "type alias Model = { x : Int }"
                , ""
                , "init _ = ({x = 0}, Cmd.none)"
                , "update _ m = (m, Cmd.none)"
                , "subs _ = Sub.none"
                , ""
                , "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Ui.el [ Ui.aspectRatio 2.35, Ui.width Ui.fill ] Ui.none)"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy` ("aspect-ratio" `isInfixOf`)
        mainGo `shouldSatisfy` ("2.35" `isInfixOf`)

    it "Std.Ui.Grid.columns sidebar layout lowers to literal `1fr 200px 1fr`" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Ui.Grid as Grid"
                , "import Std.Live exposing (app, route)"
                , ""
                , "type alias Model = { x : Int }"
                , ""
                , "init _ = ({x = 0}, Cmd.none)"
                , "update _ m = (m, Cmd.none)"
                , "subs _ = Sub.none"
                , ""
                , "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Ui.grid"
                , "            [ Ui.width Ui.fill"
                , "            , Grid.columns [ Grid.fr 1, Grid.px 200, Grid.fr 1 ]"
                , "            ]"
                , "            [ Ui.text \"sidebar\", Ui.text \"main\", Ui.text \"aside\" ])"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- The compile-time-evaluated Grid.tracksToCss for
        -- [fr 1, px 200, fr 1] is "1fr 200px 1fr" — verbatim in Go.
        mainGo `shouldSatisfy` ("1fr" `isInfixOf`)
        mainGo `shouldSatisfy` ("200px" `isInfixOf`)
        -- The marker key is the contract with `findGridTemplate`.
        mainGo `shouldSatisfy` ("__gridTracks" `isInfixOf`)

    it "Std.Ui.Grid.repeatAutoFit lowers to repeat(auto-fit, minmax(...)) literal" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Ui.Grid as Grid"
                , "import Std.Live exposing (app, route)"
                , ""
                , "type alias Model = { x : Int }"
                , ""
                , "init _ = ({x = 0}, Cmd.none)"
                , "update _ m = (m, Cmd.none)"
                , "subs _ = Sub.none"
                , ""
                , "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Ui.grid"
                , "            [ Ui.width Ui.fill"
                , "            , Grid.columns"
                , "                [ Grid.repeatAutoFit (Grid.minmax (Grid.px 240) (Grid.fr 1)) ]"
                , "            ]"
                , "            [ Ui.text \"card1\", Ui.text \"card2\" ])"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy` ("auto-fit" `isInfixOf`)
        mainGo `shouldSatisfy` ("minmax(240px, 1fr)" `isInfixOf`)

    it "Std.Ui.Grid.tracks accepts both columns + rows axes" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Ui.Grid as Grid"
                , "import Std.Live exposing (app, route)"
                , ""
                , "type alias Model = { x : Int }"
                , ""
                , "init _ = ({x = 0}, Cmd.none)"
                , "update _ m = (m, Cmd.none)"
                , "subs _ = Sub.none"
                , ""
                , "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Ui.grid"
                , "            [ Ui.width Ui.fill"
                , "            , Grid.tracks"
                , "                [ Grid.auto, Grid.fr 1 ]"
                , "                [ Grid.px 60, Grid.fr 1, Grid.px 40 ]"
                , "            ]"
                , "            [ Ui.text \"hdr\", Ui.text \"body\" ])"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- Both axes lowered.
        mainGo `shouldSatisfy` ("auto 1fr" `isInfixOf`)
        mainGo `shouldSatisfy` ("60px 1fr 40px" `isInfixOf`)

    it "Ui.gridColumns N stays back-compat with the legacy auto-fill default" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Live exposing (app, route)"
                , ""
                , "type alias Model = { x : Int }"
                , ""
                , "init _ = ({x = 0}, Cmd.none)"
                , "update _ m = (m, Cmd.none)"
                , "subs _ = Sub.none"
                , ""
                , "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Ui.grid"
                , "            [ Ui.width Ui.fill, Ui.gridColumns 240 ]"
                , "            [ Ui.text \"a\", Ui.text \"b\" ])"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- Legacy __gridMin marker still produced when Grid.tracks is
        -- not in play.
        mainGo `shouldSatisfy` ("__gridMin" `isInfixOf`)
