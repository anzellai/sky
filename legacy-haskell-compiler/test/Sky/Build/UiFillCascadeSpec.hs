module Sky.Build.UiFillCascadeSpec (spec) where

-- Regression fence for the Std.Ui Fill cross-axis cascade bug.
--
-- Pre-fix bug: both `widthCss (Fill _)` and `heightCss (Fill _)`
-- emitted the CSS string `"flex-grow: 1; align-self: stretch;"`
-- unconditionally — regardless of the parent's flex-direction.
--
-- The problem is that CSS flexbox interprets `flex-grow` along the
-- parent's MAIN axis only:
--   * In a `flex-direction: column` parent, every child marked
--     `width: fill` got `flex-grow: 1` (vertical grow), causing
--     header/main/footer to compete for vertical space and split
--     1/3 each instead of header/footer sizing to content and
--     main filling the rest.
--   * Symmetric breakage for `height: fill` in row parents.
--
-- User-facing symptom (mini-notion YT video repro): adding
-- `Ui.height (Ui.vh 100)` to the root column to make it fill the
-- viewport caused EVERY interior block (headers, footers, buttons)
-- to stretch to 1/3 viewport each, breaking the layout.
--
-- Fix: parent-direction-aware emission via `widthCssIn parentCtx` /
-- `heightCssIn parentCtx`:
--   * Column parent: `width fill` = `align-self: stretch; width:
--     100%;` (cross-axis stretch, no flex-grow); `height fill` =
--     `flex-grow: 1; min-height: 0;` (main-axis grow).
--   * Row parent: the inverse.
--   * El / Paragraph: plain `width: 100%` / `height: 100%` (no
--     flex on parent).
--
-- This spec compiles a tiny Std.Ui-using project + greps the
-- rendered Go output for the OLD bad CSS combo. If the combo
-- reappears anywhere in the typed-codegen / runtime path, the
-- test fails.

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


buildAndReadServer :: String -> IO (Int, String, String)
buildAndReadServer src =
    withSystemTempDirectory "sky-ui-fill" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"tmp\"\nversion = \"0.0.0\"\n"
        -- `cd $TMP` is load-bearing: sky build resolves sky-out/
        -- relative to CWD, not the source file's project root, so
        -- without the cd the output lands in the test runner's cwd
        -- (the sky repo root) instead of the temp dir, and the
        -- subsequent readFile misses.
        let buildCmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (bec, bout, berr) <- readCreateProcessWithExitCode (shell buildCmd) ""
        case bec of
            Exit.ExitFailure n -> return (n, "", "build failed: " ++ bout ++ berr)
            Exit.ExitSuccess -> do
                main_go <- readFile (tmp </> "sky-out" </> "main.go")
                return (0, main_go, "")


spec :: Spec
spec = describe "Std.Ui Fill cross-axis cascade" $ do

    it "Fill emission no longer hard-codes the broken CSS combo" $ do
        -- The CSS string `flex-grow: 1; align-self: stretch;` is
        -- the pre-fix signature. It MUST NOT appear in the lowered
        -- Sky code for any width / height Fill emission, regardless
        -- of parent context.
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
                -- `Model -> any` matches the mini-notion / skyforum
                -- pattern: Live.app expects Html, the Ui.layout
                -- boundary returns Std.Html.Html, the `any` return
                -- annotation makes them unify. A concrete `Model`
                -- input is required — pure `any -> any` defaults the
                -- arg pattern to unit, breaking the call shape.
                , "view : Model -> any"
                , "view _ ="
                , "    Ui.layout []"
                , "        (Ui.column"
                , "            [ Ui.width Ui.fill, Ui.height Ui.fill ]"
                , "            [ Ui.el [ Ui.width Ui.fill ] (Ui.text \"top\")"
                , "            , Ui.el [ Ui.width Ui.fill, Ui.height Ui.fill ] (Ui.text \"middle\")"
                , "            , Ui.el [ Ui.width Ui.fill ] (Ui.text \"bottom\")"
                , "            ])"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildAndReadServer src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- The string lives in Std.Ui's source (widthCssIn /
        -- heightCssIn branches) — the v0.13 typed-codegen lowers
        -- those branches directly into the emitted Go body as
        -- string literals. So the lowered main.go is a faithful
        -- search target for the bad combo's absence.
        mainGo `shouldNotSatisfy`
            ("flex-grow: 1; align-self: stretch;" `isInfixOf`)
