module Sky.Build.UiAlignSelfSpec (spec) where

-- Regression fence for the v0.15.56 F4 fix.
--
-- F4 root cause: cross-axis `widthFillFor` / `heightFillFor`
-- previously emitted `align-self: stretch;` explicitly. Combined
-- with an explicit alignment attr (`Ui.centerX` / `Ui.centerY` /
-- `Ui.alignLeft/Right/Top/Bottom`) — which also emits an
-- `align-self` declaration via `alignSelfX/Y` — this produced
-- TWO `align-self` declarations on the same element:
--
--     align-self: stretch; ... align-self: center;
--
-- Cascade-last wins, so `center` overrode `stretch` and the
-- visible result was correct — but it was correct BY LUCK: future
-- attr re-ordering, a CSS engine change, or any added pass that
-- read `align-self` from the inline style could surface the
-- conflict as a real bug.
--
-- F4 fix: strip `align-self: stretch;` from both cross-axis
-- emitters (`widthFillFor AsColumn/AsEl/AsTextColumn` AND
-- `heightFillFor AsRow`). `stretch` is the default `align-items`
-- value, so emitting it explicitly was redundant — and the
-- default still applies when no other `align-self` is emitted.
-- Post-F4 invariant: at most ONE `align-self` declaration per
-- element, sourced from `alignSelfX/Y` only.
--
-- Combiner #1 (task #489): both axis fixtures (width-axis under a
-- column parent, height-axis under a row parent) now share ONE
-- multi-fixture Main.sky compiled by a SINGLE `sky build` →
-- SINGLE `go build` invocation.  Pre-combiner this spec ran two
-- subprocess sky-builds; post-combiner it runs one, halving the
-- GOCACHE footprint with no regression-coverage loss.  In fact
-- the combined assertion is STRICTER — a partial F4 fix that
-- re-emitted `stretch` on only ONE axis trips the global
-- "stretch nowhere" check as cleanly as it would have tripped a
-- per-axis check, AND the count-based "center appears ≥ 2 times"
-- assertion proves both alignment attrs still emit their
-- explicit `align-self: center;` declaration.

import Test.Hspec
import qualified System.Exit as Exit
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import System.IO.Temp (withSystemTempDirectory)
import Data.List (isInfixOf, tails)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- | Single multi-fixture project: both axes' views live as
-- separate top-level functions in Main.sky; ONE sky build covers
-- both.  Returns the combined emitted main.go (or an error string
-- + exit code on build failure).
buildCombinedMain :: IO (Int, String, String)
buildCombinedMain =
    withSystemTempDirectory "sky-ui-alignself" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") combinedFixture
        writeFile (tmp </> "sky.toml") "name = \"tmp\"\nversion = \"0.0.0\"\n"
        let buildCmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (bec, bout, berr) <- readCreateProcessWithExitCode (shell buildCmd) ""
        case bec of
            Exit.ExitFailure n -> return (n, "", "build failed: " ++ bout ++ berr)
            Exit.ExitSuccess -> do
                main_go <- readFile (tmp </> "sky-out" </> "main.go")
                return (0, main_go, "")


-- | Combined fixture: width-axis + height-axis views in ONE
-- Main.sky.  The combined view nests both shapes so the lowerer
-- emits BOTH `widthFillFor AsColumn` (under outer column) AND
-- `heightFillFor AsRow` (under inner row) into the same main.go
-- on a single go build.
combinedFixture :: String
combinedFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Std.Ui as Ui"
    , "import Std.Live exposing (app, route)"
    , ""
    , "type Msg = Noop"
    , "type alias Model = { x : Int }"
    , ""
    , "init _ = ({x = 0}, Cmd.none)"
    , "update _ m = (m, Cmd.none)"
    , "subs _ = Sub.none"
    , ""
    , "-- Width axis: [Ui.width fill, Ui.centerX] under a column"
    , "-- parent → widthFillFor AsColumn (cross-axis) + centerX's"
    , "-- alignSelfX AsColumn CenterX.  Pre-F4 emitted both"
    , "-- align-self: stretch AND align-self: center.  Post-F4 only"
    , "-- the center declaration remains."
    , "widthView : Ui.Element Msg"
    , "widthView ="
    , "    Ui.column"
    , "        [ Ui.width Ui.fill, Ui.centerX ]"
    , "        [ Ui.text \"width\" ]"
    , ""
    , "-- Height axis: [Ui.height fill, Ui.centerY] under a row"
    , "-- parent → heightFillFor AsRow (cross-axis) + centerY's"
    , "-- alignSelfY AsRow CenterY.  Same single-emission contract"
    , "-- on the height axis."
    , "heightView : Ui.Element Msg"
    , "heightView ="
    , "    Ui.row"
    , "        [ Ui.width Ui.fill, Ui.height Ui.fill ]"
    , "        [ Ui.el [ Ui.height Ui.fill, Ui.centerY ] (Ui.text \"height\") ]"
    , ""
    , "view : Model -> any"
    , "view _ ="
    , "    Ui.layout []"
    , "        (Ui.column []"
    , "            [ widthView"
    , "            , heightView"
    , "            ])"
    , ""
    , "main = app { init = init, update = update, view = view"
    , "           , subscriptions = subs"
    , "           , routes = [ route \"/\" () ], notFound = () }"
    ]


-- | Count occurrences of `needle` in `haystack`.  Uses the
-- standard `tails`/`isPrefixOf` idiom — linear in haystack length.
countOccurrences :: String -> String -> Int
countOccurrences needle haystack
    | null needle = 0
    | otherwise = length [ () | t <- tails haystack, needle `isInfixOf` take (length needle) t ]


spec :: Spec
spec = describe "Std.Ui align-self single-emission contract (F4)" $ do

    it "neither width-axis (column parent) nor height-axis (row parent) emits align-self: stretch; both alignment attrs still emit align-self: center" $ do
        (ec, mainGo, err) <- buildCombinedMain
        ec `shouldBe` 0
        err `shouldBe` ""
        -- F4 invariant: NO `align-self: stretch` ANYWHERE in the
        -- emitted main.go.  A partial fix that left it on only ONE
        -- axis would still trip this assertion — strictly more
        -- coverage than the pre-combiner per-axis split.
        mainGo `shouldNotSatisfy`
            ("align-self: stretch" `isInfixOf`)
        -- Both fixtures' alignment attrs MUST still produce their
        -- explicit `align-self: center;` declaration — at LEAST
        -- two of them in the combined output (one per fixture).
        countOccurrences "align-self: center;" mainGo `shouldSatisfy` (>= 2)
        -- The width-fill emitter must keep `width: 100%` so the
        -- canonical `[Ui.width (Ui.maximum 760 Ui.fill), Ui.centerX]`
        -- shape still fills width before centring (showcase shell).
        mainGo `shouldSatisfy`
            ("width: 100%;" `isInfixOf`)
