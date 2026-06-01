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
-- This spec greps the lowered Go output for two shapes:
--   (a) [Ui.width fill, Ui.centerX] under a column parent — must
--       emit `align-self: center;` exactly once, NOT alongside
--       `align-self: stretch;`.
--   (b) [Ui.height fill, Ui.centerY] under a row parent — same
--       single-emission contract for the height axis.

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
    withSystemTempDirectory "sky-ui-alignself" $ \tmp -> do
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


-- [Ui.width fill, Ui.centerX] under a column parent → cross-axis
-- width fill AND cross-axis horizontal alignment. Two align-self
-- emitters in the pre-F4 world.
fixtureWidthFillCenterX :: String
fixtureWidthFillCenterX = unlines
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
    , "view : Model -> any"
    , "view _ ="
    , "    Ui.layout []"
    , "        (Ui.column"
    , "            [ Ui.width Ui.fill, Ui.centerX ]"
    , "            [ Ui.text \"hi\" ])"
    , ""
    , "main = app { init = init, update = update, view = view"
    , "           , subscriptions = subs"
    , "           , routes = [ route \"/\" () ], notFound = () }"
    ]


-- [Ui.height fill, Ui.centerY] under a row parent → cross-axis
-- height fill AND cross-axis vertical alignment. Two align-self
-- emitters in the pre-F4 world.
fixtureHeightFillCenterY :: String
fixtureHeightFillCenterY = unlines
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
    , "view : Model -> any"
    , "view _ ="
    , "    Ui.layout []"
    , "        (Ui.row"
    , "            [ Ui.width Ui.fill, Ui.height Ui.fill ]"
    , "            [ Ui.el [ Ui.height Ui.fill, Ui.centerY ] (Ui.text \"hi\") ])"
    , ""
    , "main = app { init = init, update = update, view = view"
    , "           , subscriptions = subs"
    , "           , routes = [ route \"/\" () ], notFound = () }"
    ]


spec :: Spec
spec = describe "Std.Ui align-self single-emission contract (F4)" $ do

    it "[Ui.width fill, Ui.centerX]: emits align-self: center, NOT align-self: stretch" $ do
        (ec, mainGo, err) <- buildAndReadMain fixtureWidthFillCenterX
        ec `shouldBe` 0
        err `shouldBe` ""
        -- The widthFillFor AsColumn branch must NOT emit a redundant
        -- `align-self: stretch` (stretch is the default; emitting it
        -- explicitly created the cascade conflict with the alignment
        -- attr's `align-self: center`).
        mainGo `shouldNotSatisfy`
            ("align-self: stretch" `isInfixOf`)
        -- The user's centerX MUST still produce `align-self: center`
        -- (it cascades over the default stretch to give the column
        -- horizontal centring — see alignSelfX AsColumn CenterX).
        mainGo `shouldSatisfy`
            ("align-self: center;" `isInfixOf`)
        -- The width-fill emitter must keep `width: 100%` so a
        -- `[Ui.width (Ui.maximum 760 Ui.fill), Ui.centerX]` shape
        -- still fills width before centring (showcase outer column).
        mainGo `shouldSatisfy`
            ("width: 100%;" `isInfixOf`)

    it "[Ui.height fill, Ui.centerY] under row parent: emits align-self: center, NOT align-self: stretch" $ do
        (ec, mainGo, err) <- buildAndReadMain fixtureHeightFillCenterY
        ec `shouldBe` 0
        err `shouldBe` ""
        -- Same contract on the vertical axis. heightFillFor AsRow
        -- must not emit `align-self: stretch`; centerY's
        -- `align-self: center` is the sole declaration.
        mainGo `shouldNotSatisfy`
            ("align-self: stretch" `isInfixOf`)
        mainGo `shouldSatisfy`
            ("align-self: center;" `isInfixOf`)
