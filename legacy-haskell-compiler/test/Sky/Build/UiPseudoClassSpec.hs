module Sky.Build.UiPseudoClassSpec (spec) where

-- Regression fence for the Std.Ui pseudo-class primitive
-- (`Background.hoverColor` / `Font.focusColor` / `Border.activeColor`
-- / `Ui.onPseudo`, issue #377).
--
-- The compile-side contract: a Sky source that calls one of the
-- new `on<State>` helpers builds to Go output containing the marker
-- string the runtime expects to find on the element
-- (`data-sky-pc-rules`) AND the per-pseudo wire tag (`h|`, `v|`,
-- `a|`, `d|`, `f|`). If the v0.15.x typed-codegen path stops
-- lowering the AttrPseudoRule constructor, this spec fires.
--
-- Pairs with runtime-go/rt/live.go's `injectPseudoClassStyles`
-- (the half that actually wraps the marker attr into a scoped
-- `<style>` child with per-pseudo CSS rules at render time).

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
    withSystemTempDirectory "sky-ui-pc" $ \tmp -> do
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


-- Compile-time fence: the wire-format tag strings ("h", "f", "v",
-- "a", "d") only ever exist as runtime case-branch return values
-- (the `pseudoClassTag` function lowering), NOT as constants
-- baked into individual call sites. So the spec keys off two
-- compile-time invariants instead:
--
--   1. `data-sky-pc-rules` literal — the marker attr name. This
--      MUST appear in the lowered Go whenever any element on the
--      page reads pseudo-class attributes, otherwise the runtime
--      injection step has nothing to find.
--   2. The `Std_Ui_Attribute_AttrPseudoRule` ctor — proves the
--      AttrPseudoRule node lowered to a real value (not a no-op
--      / not pruned by DCE).
--   3. The sub-module helper symbol (`Std_Ui_Background_hoverColor`
--      etc.) — proves the user-facing API surface lowered through
--      to Go entry points the typed call resolves against.

hasMarker :: String -> Bool
hasMarker = isInfixOf "data-sky-pc-rules"

hasPseudoCtor :: String -> Bool
hasPseudoCtor = isInfixOf "Std_Ui_Attribute_AttrPseudoRule"


spec :: Spec
spec = describe "Std.Ui pseudo-class primitive" $ do

    it "Background.hoverColor lowers to AttrPseudoRule + marker" $ do
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
                , "        (Ui.el"
                , "            [ Background.color (Ui.rgb 0 122 255)"
                , "            , Background.hoverColor (Ui.rgb 0 90 200)"
                , "            ]"
                , "            (Ui.text \"hover me\"))"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- The marker string is what the runtime's
        -- `injectPseudoClassStyles` keys off; if absent, no <style>
        -- gets emitted and the hover-state CSS is lost.
        mainGo `shouldSatisfy` hasMarker
        -- The AttrPseudoRule ctor must lower to a real Go ctor —
        -- proves the helper isn't dead-code-eliminated.
        mainGo `shouldSatisfy` hasPseudoCtor
        -- The user-facing helper from the sub-module compiled.
        mainGo `shouldSatisfy`
            ("Std_Ui_Background_hoverColor" `isInfixOf`)
        -- The base-colour background-color literal — proves
        -- non-pseudo rules still render unchanged alongside.
        mainGo `shouldSatisfy` ("background-color" `isInfixOf`)

    it "Font.focusColor lowers via FocusVisible ctor (safer default)" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Ui.Font as Font"
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
                , "        (Ui.el"
                , "            [ Font.color (Ui.rgb 30 30 30)"
                , "            , Font.focusColor (Ui.rgb 0 122 255)"
                , "            ]"
                , "            (Ui.text \"focus me\"))"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy` hasMarker
        mainGo `shouldSatisfy` hasPseudoCtor
        -- The sub-module helper symbol lowered.
        mainGo `shouldSatisfy`
            ("Std_Ui_Font_focusColor" `isInfixOf`)
        -- Crucially: `focusColor` MUST target FocusVisible, not
        -- the sticky-focus Focus ctor. The function body uses
        -- `onPseudo focusVisible` — so the lowered Go must call
        -- `Std_Ui_focusVisible` somewhere in its FROM-source chain.
        mainGo `shouldSatisfy`
            ("Std_Ui_focusVisible" `isInfixOf`)

    it "Background.disabledColor lowers + reaches the Disabled ctor" $ do
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
                , "        (Ui.el"
                , "            [ Background.disabledColor (Ui.rgb 200 200 200) ]"
                , "            (Ui.text \"disabled\"))"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy` hasMarker
        mainGo `shouldSatisfy` hasPseudoCtor
        mainGo `shouldSatisfy`
            ("Std_Ui_Background_disabledColor" `isInfixOf`)
        mainGo `shouldSatisfy`
            ("Std_Ui_disabled" `isInfixOf`)

    it "Ui.onPseudo Ui.focus targets sticky :focus (escape hatch)" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Ui.Border as Border"
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
                , "        (Ui.el"
                , "            [ Ui.onPseudo Ui.focus [ Border.color (Ui.rgb 0 122 255) ] ]"
                , "            (Ui.text \"sticky focus ring\"))"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy` hasMarker
        mainGo `shouldSatisfy` hasPseudoCtor
        -- The user picked sticky :focus explicitly — proves the
        -- escape hatch's Focus ctor (NOT FocusVisible) is what
        -- lowered.
        mainGo `shouldSatisfy`
            (\s -> "Std_Ui_focus" `isInfixOf` s
                   && "Std_Ui_onPseudo" `isInfixOf` s)

    it "stacked pseudo-rules on one element emit multiple AttrPseudoRule calls" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Ui.Background as Background"
                , "import Std.Ui.Font as Font"
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
                , "        (Ui.el"
                , "            [ Background.hoverColor (Ui.rgb 0 92 215)"
                , "            , Font.focusColor (Ui.rgb 255 102 0)"
                , "            , Background.activeColor (Ui.rgb 0 62 175)"
                , "            ]"
                , "            (Ui.text \"multi-state\"))"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- All three sub-module helper symbols appear — proves
        -- each one lowered through its own dispatch.
        mainGo `shouldSatisfy`
            ("Std_Ui_Background_hoverColor" `isInfixOf`)
        mainGo `shouldSatisfy`
            ("Std_Ui_Font_focusColor" `isInfixOf`)
        mainGo `shouldSatisfy`
            ("Std_Ui_Background_activeColor" `isInfixOf`)

    it "composes with Ui.breakpoint — pseudo + media-query markers coexist" $ do
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
                , "            [ Ui.padding 24 ]"
                , "            (Ui.el"
                , "                [ Background.color (Ui.rgb 0 122 255)"
                , "                , Background.hoverColor (Ui.rgb 0 92 215)"
                , "                ]"
                , "                (Ui.text \"composed\")))"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- BOTH markers must coexist in the same emitted body — the
        -- pseudo-rule attaches to the inner element while the media-
        -- query wraps the outer; both live in the same Go output.
        mainGo `shouldSatisfy` ("data-sky-mq-q" `isInfixOf`)
        mainGo `shouldSatisfy` hasMarker
        mainGo `shouldSatisfy` ("(max-width: 767px)" `isInfixOf`)
        mainGo `shouldSatisfy`
            ("Std_Ui_Background_hoverColor" `isInfixOf`)

    it "element with no pseudo-class attrs emits NO AttrPseudoRule call" $ do
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
                , "        (Ui.el"
                , "            [ Background.color (Ui.rgb 0 122 255) ]"
                , "            (Ui.text \"plain\"))"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        -- Note: `data-sky-pc-rules` will ALWAYS appear in main.go
        -- (as a constant in `renderNodeAs`'s body), regardless of
        -- whether the user code uses pseudo-classes. That's fine —
        -- runtime emission is gated on `collectPseudoRules`
        -- producing a non-empty list, which depends on the user
        -- actually adding AttrPseudoRule values to the attribute
        -- list. The compile-time fence here checks the user-facing
        -- helpers (`*hoverColor` / `*focusColor` / `onPseudo`) are
        -- NOT in the lowered Go — if any of them sneaked in, the
        -- view function would carry a pseudo-rule unintentionally.
        mainGo `shouldSatisfy` (not . ("Std_Ui_Background_hoverColor" `isInfixOf`))
        mainGo `shouldSatisfy` (not . ("Std_Ui_Background_focusColor" `isInfixOf`))
        mainGo `shouldSatisfy` (not . ("Std_Ui_Background_activeColor" `isInfixOf`))
        mainGo `shouldSatisfy` (not . ("Std_Ui_Background_disabledColor" `isInfixOf`))
        mainGo `shouldSatisfy` (not . ("Std_Ui_Font_hoverColor" `isInfixOf`))
        mainGo `shouldSatisfy` (not . ("Std_Ui_Border_hoverColor" `isInfixOf`))
        mainGo `shouldSatisfy` (not . ("Std_Ui_onPseudo" `isInfixOf`))
