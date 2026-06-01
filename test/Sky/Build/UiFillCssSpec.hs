module Sky.Build.UiFillCssSpec (spec) where

-- Regression fence for the v0.15.55 F1 + v0.15.56 F4 fixes.
--
-- F1 root cause: `widthFillFor` / `heightFillFor` in
-- `sky-stdlib/Std/Ui.sky` emitted `align-self: stretch; width: 100%`
-- (or `height: 100%`) for the cross-axis case.  The explicit
-- `width: 100%` / `height: 100%` is ACTIVELY HARMFUL when the
-- parent's cross-axis size was itself flex-grow-derived — CSS
-- Flexbox §9.8 resolves `%` against the parent's USED size only
-- when that size is "definite"; a flex-grow-derived height is
-- indefinite for the purpose of `%` resolution on cross-axis
-- children.
--
-- Symptom: every Sky.Live app where a `Ui.row` is a flex child of a
-- column ancestor AND that row's children ask for `Ui.height Ui.fill`
-- saw the children collapse to text-content height (~22 px on the
-- user's three-pane app shell; 51 px on Input.multiline).
--
-- v0.15.55 F1: strip `height: 100%` from `heightFillFor AsRow`.
-- v0.15.56 F4: strip the redundant `align-self: stretch` from BOTH
-- cross-axis width AND cross-axis height fill emitters. `stretch`
-- is the default `align-items` value, so emitting it explicitly was
-- redundant AND collided with explicit `Ui.centerX/Y` /
-- `Ui.alignLeft/Right/Top/Bottom` alignment attrs (which emit
-- their own `align-self`). Post-F4 the cascade has exactly ONE
-- `align-self` declaration per element — emitted from
-- `alignSelfX/Y` only.
--
-- Width-axis asymmetry (intentional): `widthFillFor AsColumn /
-- AsEl / AsTextColumn` STILL emits `width: 100%` because column-
-- parent widths are typically definite (block inheritance) AND the
-- `width: 100%` survives the `[Ui.width fill, Ui.centerX]` cascade
-- (alignment overrides the implicit stretch default; width: 100%
-- stays put so the column still fills before centring within a
-- `max-width` cap). See the showcase outer column pattern.
--
-- This spec greps the lowered Go output for the OLD bad combos and
-- the EXPECTED new combo to fence the contract.

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
    withSystemTempDirectory "sky-ui-fillcss" $ \tmp -> do
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


-- A minimal Sky.Live app that exercises width-fill in a column (cross
-- axis) and height-fill in a row (cross axis) so the lowerer emits
-- both `widthFillFor AsColumn` and `heightFillFor AsRow` branches.
fixture :: String
fixture = unlines
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
    , "            [ Ui.width Ui.fill, Ui.height Ui.fill ]"
    , "            [ Ui.row [ Ui.width Ui.fill, Ui.height Ui.fill ]"
    , "                [ Ui.el [ Ui.width Ui.fill, Ui.height Ui.fill ] (Ui.text \"x\") ]"
    , "            ])"
    , ""
    , "main = app { init = init, update = update, view = view"
    , "           , subscriptions = subs"
    , "           , routes = [ route \"/\" () ], notFound = () }"
    ]


spec :: Spec
spec = describe "Std.Ui cross-axis fill emission (F1 + F4)" $ do

    it "heightFillFor AsRow does NOT emit `height: 100%`" $ do
        (ec, mainGo, err) <- buildAndReadMain fixture
        ec `shouldBe` 0
        err `shouldBe` ""
        -- F1 fix signature. heightFillFor AsRow previously emitted
        -- `align-self: stretch; height: 100%;` — the `100%` resolved
        -- to auto/content when the row's cross-axis (height) was
        -- indefinite (flex-grow-derived or content-derived),
        -- collapsing every fill child to text-content height. That
        -- was the Z2 (Input.multiline → 51 px) + Z3 (three-pane shell
        -- → 22 px each) bug class. Must NEVER come back.
        mainGo `shouldNotSatisfy`
            ("align-self: stretch; height: 100%;" `isInfixOf`)
        -- F4: the bare `align-self: stretch;` SHOULD ALSO be gone
        -- from cross-axis height fill — `stretch` is the default
        -- `align-items` value so emitting it explicitly was redundant
        -- AND collided with explicit alignment attrs. Post-F4 the
        -- cross-axis height branch emits NOTHING; alignment is
        -- emitted from `alignSelfX/Y` only.
        mainGo `shouldNotSatisfy`
            ("align-self: stretch; height:" `isInfixOf`)

    it "widthFillFor cross-axis keeps `width: 100%` (column parents have definite widths)" $ do
        (ec, mainGo, err) <- buildAndReadMain fixture
        ec `shouldBe` 0
        err `shouldBe` ""
        -- F1+F4 asymmetry: width cross-axis fill KEEPS the explicit
        -- `width: 100%` because column / el / textColumn parents have
        -- DEFINITE block widths (inherited from <body>) AND because
        -- `[Ui.width fill, Ui.centerX]` needs the explicit `width:
        -- 100%` to survive the alignment cascade (alignment overrides
        -- the implicit stretch; width: 100% stays put, so the column
        -- still fills before centring within a max-width cap). The
        -- fixture's column-of-row-of-el chain forces two
        -- widthFillFor AsColumn emissions (the row + the el inside)
        -- so the byte string must appear.
        mainGo `shouldSatisfy`
            ("width: 100%;" `isInfixOf`)

    it "F4: widthFillFor cross-axis does NOT emit `align-self: stretch`" $ do
        (ec, mainGo, err) <- buildAndReadMain fixture
        ec `shouldBe` 0
        err `shouldBe` ""
        -- F4 fix signature. `widthFillFor AsColumn/AsEl/AsTextColumn`
        -- previously emitted `align-self: stretch; width: 100%;`. The
        -- `align-self: stretch` was redundant (default flex behaviour)
        -- and conflicted with explicit alignment attrs (cascade-
        -- order-dependent). Post-F4 it emits only `width: 100%;`.
        mainGo `shouldNotSatisfy`
            ("align-self: stretch; width: 100%;" `isInfixOf`)

    it "main-axis fill still emits flex-grow + min-{axis}: 0" $ do
        (ec, mainGo, err) <- buildAndReadMain fixture
        ec `shouldBe` 0
        err `shouldBe` ""
        -- Main-axis emission unchanged. The fixture's column->row
        -- height fill is main-axis (column main = height), so
        -- `min-height: 0` must still appear from heightFillFor
        -- AsColumn/AsEl/AsTextColumn.
        mainGo `shouldSatisfy`
            ("min-height: 0;" `isInfixOf`)
        -- Symmetric: the row->el width fill is main-axis (row main =
        -- width), so `min-width: 0` must still appear from
        -- widthFillFor AsRow.
        mainGo `shouldSatisfy`
            ("min-width: 0;" `isInfixOf`)
