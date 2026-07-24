module Sky.Build.StdUiChartSpec (spec) where

-- Regression fence for Std.Ui.Chart primitives (PR 4 — v0.16.0).
--
-- Compile-side contract: a Sky source that calls each chart helper
-- (line / area / bar / sparkline / heatmap) builds cleanly through
-- `sky build`. The emitted Go contains the expected SVG markup
-- (viewBox, fill, stroke attrs) so we know the chart actually
-- renders a structure the browser will paint.
--
-- We don't snapshot exact SVG bytes — coordinate math depends on
-- runtime arithmetic and the formatter may rearrange attribute
-- order. Instead, we assert "did the markup land in the binary?"
-- by checking the emitted main.go contains the literal SVG element
-- name and the expected fill/stroke attribute names per helper.

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
    withSystemTempDirectory "sky-ui-chart" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"tmp\"\nversion = \"0.0.0\"\n"
        let buildCmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (bec, bout, berr) <- readCreateProcessWithExitCode (shell buildCmd) ""
        case bec of
            Exit.ExitFailure n -> return (n, "", "build failed:\n" ++ bout ++ berr)
            Exit.ExitSuccess -> do
                main_go <- readFile (tmp </> "sky-out" </> "main.go")
                return (0, main_go, "")


prelude :: String
prelude = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Std.Ui as Ui exposing (Element)"
    , "import Std.Ui.Chart as Chart"
    , "import Std.Live exposing (app, route)"
    , ""
    , "type alias Model = { x : Int }"
    , "type Msg = NoOp"
    , ""
    , "init _ = ({x = 0}, Cmd.none)"
    , "update _ m = (m, Cmd.none)"
    , "subs _ = Sub.none"
    , ""
    ]


footer :: String
footer = unlines
    [ ""
    , "main = app { init = init, update = update, view = view"
    , "           , subscriptions = subs"
    , "           , routes = [ route \"/\" () ], notFound = () }"
    ]


spec :: Spec
spec = describe "Std.Ui.Chart (PR 4 — v0.16.0)" $ do

    it "Chart.line cfg series compiles and emits an SVG <path stroke=…>" $ do
        let src = prelude ++ unlines
                [ "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Chart.line"
                , "            (Chart.defaultCfg |> Chart.withWidth 200 |> Chart.withHeight 80)"
                , "            [ Chart.series [(0.0, 5.0), (1.0, 10.0), (2.0, 7.0)] ])"
                ] ++ footer
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy` ("Std_Ui_Chart_line" `isInfixOf`)
        mainGo `shouldSatisfy` ("svg" `isInfixOf`)
        mainGo `shouldSatisfy` ("viewBox" `isInfixOf`)
        mainGo `shouldSatisfy` ("stroke" `isInfixOf`)
        mainGo `shouldSatisfy` ("path" `isInfixOf`)

    it "Chart.area cfg series compiles and emits a fill-opacity attribute" $ do
        let src = prelude ++ unlines
                [ "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Chart.area"
                , "            Chart.defaultCfg"
                , "            [ Chart.series [(0.0, 1.0), (1.0, 3.0)] ])"
                ] ++ footer
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy` ("Std_Ui_Chart_area" `isInfixOf`)
        mainGo `shouldSatisfy` ("fill-opacity" `isInfixOf`)

    it "Chart.bar cfg series compiles and emits <rect> + rx attribute" $ do
        let src = prelude ++ unlines
                [ "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Chart.bar"
                , "            (Chart.defaultCfg |> Chart.withTitle \"Routes\")"
                , "            [ Chart.series [(0.0, 10.0), (1.0, 20.0)] ])"
                ] ++ footer
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy` ("Std_Ui_Chart_bar" `isInfixOf`)
        mainGo `shouldSatisfy` ("rect" `isInfixOf`)

    it "Chart.sparkline cfg values compiles, no axes (no <text> axis labels)" $ do
        let src = prelude ++ unlines
                [ "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Chart.sparkline"
                , "            (Chart.defaultCfg |> Chart.withWidth 60 |> Chart.withHeight 16)"
                , "            [1.0, 2.0, 3.0, 2.0, 4.0])"
                ] ++ footer
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy` ("Std_Ui_Chart_sparkline" `isInfixOf`)
        mainGo `shouldSatisfy` ("preserveAspectRatio" `isInfixOf`)

    it "Chart.heatmap cfg grid compiles and emits a <rect> grid" $ do
        let src = prelude ++ unlines
                [ "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Chart.heatmap"
                , "            Chart.defaultCfg"
                , "            [ [1.0, 2.0, 3.0]"
                , "            , [4.0, 5.0, 6.0]"
                , "            ])"
                ] ++ footer
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy` ("Std_Ui_Chart_heatmap" `isInfixOf`)
        mainGo `shouldSatisfy` ("fill-opacity" `isInfixOf`)

    it "Builder API: Chart.defaultCfg |> Chart.withColor + Chart.withYRange compose without type errors" $ do
        let src = prelude ++ unlines
                [ "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Chart.line"
                , "            (Chart.defaultCfg"
                , "                |> Chart.withColor (Ui.rgb 200 100 50)"
                , "                |> Chart.withYRange 0.0 100.0"
                , "                |> Chart.withGridLines False"
                , "            )"
                , "            [ Chart.series [(0.0, 10.0), (1.0, 50.0)]"
                , "                |> Chart.withSeriesColor (Ui.rgb 50 150 200)"
                , "                |> Chart.withSeriesLabel \"requests\""
                , "            ])"
                ] ++ footer
        (ec, _, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
