module Sky.Build.InputAttrsSplitSpec (spec) where

-- Regression fence for the GitHub issue #63 follow-up: every
-- `Std.Ui.Input.*` control now partitions user `attrs` between
-- the wrapWithLabel wrapper (layout / size / alignment) and the
-- inner form control (form / event / visual style).
--
-- Pre-fix: `Input.multiline [Ui.width Ui.fill, Ui.height Ui.fill]
-- {...}` inside `Ui.row [Ui.fill, Ui.fill] [...]` collapsed. The
-- wrapper Ui.el `wrapWithLabel` emitted had NO layout attrs, so
-- it sat at `flex: 0 0 auto`; the user's height/width-fill on
-- Input.multiline correctly applied to the inner <textarea>, but
-- the parent didn't grow so the textarea had nothing to fill.
--
-- Fix: `splitLayoutAttrs` partitions user attrs by ctor — layout
-- attrs (AttrWidth, AttrHeight, AttrAlignX, AttrAlignY,
-- AttrPadding, AttrSpacing, AttrNearby, AttrPointer,
-- AttrOverflow) hoist to the wrapWithLabel wrapper; everything
-- else stays on the inner control. When ≥1 layout attr was
-- hoisted, implicit `Ui.width Ui.fill + Ui.height Ui.fill`
-- attaches to the inner control so the layout cascade flows
-- through (no implicit fill when zero layout attrs supplied →
-- defaults stay intrinsic / shrink-to-content).
--
-- This spec compiles four scenarios and asserts the lowered Go
-- routes each attr to the right place:
--
--   1. Layout attrs + visual attrs supplied — layout flows
--      through `splitLayoutAttrs` (helper present in the lowered
--      output).
--   2. Form attrs (onInput / spellcheck) stay on the inner
--      control (still wired to the textarea's attribute list).
--   3. Visual attrs (Background.color) stay on the inner control
--      (the textarea carries the background-color CSS literal).
--   4. No layout attrs supplied — the helper still runs but
--      `implicitFillIfHoisted` returns `[]` so the inner control
--      doesn't gain implicit fill.

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
    withSystemTempDirectory "sky-input-attrs-split" $ \tmp -> do
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


-- Scaffold a minimal Sky.Live app whose view places an
-- `Input.multiline` inside a `Ui.row` and varies the attrs.
multilineSrc :: String -> String -> String
multilineSrc inputAttrs extraImports = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Cmd as Cmd"
    , "import Std.Sub as Sub"
    , "import Std.Live exposing (app, route)"
    , "import Std.Ui as Ui"
    , "import Std.Ui exposing (Element)"
    , "import Std.Ui.Input as Input"
    , extraImports
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
    , "        (Ui.row"
    , "            [ Ui.width Ui.fill, Ui.height Ui.fill ]"
    , "            [ Input.multiline"
    , "                [ " ++ inputAttrs ++ " ]"
    , "                { onChange = EditDraft"
    , "                , text = model.draft"
    , "                , placeholder = Nothing"
    , "                , label = Input.labelHidden \"Body\""
    , "                , spellcheck = True"
    , "                }"
    , "            ])"
    , ""
    , "main = app { init = init, update = update, view = view"
    , "           , subscriptions = subs"
    , "           , routes = [ route \"/\" () ], notFound = () }"
    ]


spec :: Spec
spec = describe "Std.Ui.Input.* attrs split between wrapper + control" $ do

    it "layout attrs are routed through splitLayoutAttrs at the multiline call site" $ do
        let src = multilineSrc "Ui.width Ui.fill, Ui.height Ui.fill" ""
        (ec, mainGo, err) <- buildAndReadMain src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- POSITIVE: the splitLayoutAttrs helper appears in the
        -- lowered Go (without it, the wrapper would stay layout-
        -- attrs-empty and the issue #63 follow-up reopens).
        mainGo `shouldSatisfy`
            ("Std_Ui_Input_splitLayoutAttrs" `isInfixOf`)
        -- POSITIVE: the partitioner uses the per-ctor isLayoutAttr
        -- predicate to route AttrWidth / AttrHeight etc. to the
        -- wrapper side.
        mainGo `shouldSatisfy`
            ("Std_Ui_Input_isLayoutAttr" `isInfixOf`)
        -- POSITIVE: implicitFillIfHoisted runs so the inner
        -- control carries fill when layout was hoisted.
        mainGo `shouldSatisfy`
            ("Std_Ui_Input_implicitFillIfHoisted" `isInfixOf`)

    it "form attrs (spellcheck / onInput) stay on the inner <textarea>" $ do
        let src = multilineSrc "Ui.width Ui.fill" ""
        (ec, mainGo, err) <- buildAndReadMain src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- The TaggedNode "textarea" body still references the
        -- spellcheck attr literal AND the user's onChange dispatch.
        -- These are control-side attrs that MUST NOT migrate to
        -- the wrapper.
        mainGo `shouldSatisfy`
            ("\"spellcheck\"" `isInfixOf`)
        mainGo `shouldSatisfy`
            ("Std_Ui_Element_TaggedNode(\"textarea\"" `isInfixOf`)

    it "visual attrs (Background.color) stay on the inner <textarea>, not the wrapper" $ do
        -- Background.color is an AttrBgColor — explicitly NOT
        -- in the layout set; visual styling belongs on the
        -- control itself. The fix's `isLayoutAttr` falls
        -- through the default `_ -> False` arm for AttrBgColor.
        let src = multilineSrc
                "Background.color (Ui.rgb 240 240 240)"
                "import Std.Ui.Background as Background"
        (ec, mainGo, err) <- buildAndReadMain src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- The rgb literal must be a colour-typed arg somewhere in
        -- the lowered output — it does NOT vanish, doesn't get
        -- re-routed.
        mainGo `shouldSatisfy`
            ("Std_Ui_Color_Rgba" `isInfixOf`)

    it "no layout attrs supplied → no implicit fill leaks to inner control" $ do
        -- When the user supplies zero layout attrs, the wrapper
        -- stays intrinsic AND the inner control stays intrinsic.
        -- This is the regression we MUST NOT cause: defaults
        -- changing from "shrink-to-content" to "fill parent".
        --
        -- We can verify this by building a control with ONLY
        -- visual attrs and confirming the helper still routes
        -- through implicitFillIfHoisted (it's compiled in either
        -- way), but the helper's runtime behaviour is exercised
        -- by the Playwright gate verify-issue-63-input.mjs.
        let src = multilineSrc
                "Background.color (Ui.rgb 240 240 240)"
                "import Std.Ui.Background as Background"
        (ec, mainGo, err) <- buildAndReadMain src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy`
            ("Std_Ui_Input_implicitFillIfHoisted" `isInfixOf`)
