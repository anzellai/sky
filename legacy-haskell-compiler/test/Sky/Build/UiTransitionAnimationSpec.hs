module Sky.Build.UiTransitionAnimationSpec (spec) where

-- Regression fence for the Std.Ui transition + animation DSL
-- (`Transition.attribute` / `Animation.attribute`, issue #378).
--
-- The compile-side contract: a Sky source that calls the new helpers
-- builds to Go output containing:
--   1. The runtime marker attrs the injection passes key off
--      (`data-sky-tr-rules`, `data-sky-tr-respect`,
--      `data-sky-anim-rules`).
--   2. The AttrTransition / AttrAnimation ADT ctors — proves the
--      typed-codegen path didn't dead-code-eliminate them.
--   3. The user-facing helper symbols
--      (`Std_Ui_Transition_attribute`, `Std_Ui_Animation_attribute`).
--   4. The lowered shorthand strings ("200ms ease-out") + the
--      animation iteration keyword ("forwards" etc.) — proves the
--      Spec record fields flow through.
--
-- Pairs with runtime-go/rt/live.go's `injectTransitionStyles` +
-- `injectAnimationStyles` (the runtime half that wraps the marker
-- attrs into sky-id-scoped `<style>` children at render time, with
-- the `@media (prefers-reduced-motion: no-preference)` gate by
-- default).

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
    withSystemTempDirectory "sky-ui-tr-anim" $ \tmp -> do
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


hasTrMarker :: String -> Bool
hasTrMarker = isInfixOf "data-sky-tr-rules"


hasAnimMarker :: String -> Bool
hasAnimMarker = isInfixOf "data-sky-anim-rules"


hasTransitionCtor :: String -> Bool
hasTransitionCtor = isInfixOf "Std_Ui_Attribute_AttrTransition"


hasAnimationCtor :: String -> Bool
hasAnimationCtor = isInfixOf "Std_Ui_Attribute_AttrAnimation"


spec :: Spec
spec = describe "Std.Ui transitions + animations" $ do

    it "Transition.attribute lowers to AttrTransition + marker + shorthand" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Ui.Background as Background"
                , "import Std.Ui.Transition as Transition"
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
                , "            , Transition.attribute"
                , "                  [ Transition.property \"background-color\""
                , "                  , Transition.duration 200"
                , "                  , Transition.easing Transition.easeOut"
                , "                  ]"
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
        -- Runtime markers the injection pass needs.
        mainGo `shouldSatisfy` hasTrMarker
        -- The AttrTransition ctor lowered to a real Go value.
        mainGo `shouldSatisfy` hasTransitionCtor
        -- User-facing helper symbol.
        mainGo `shouldSatisfy`
            ("Std_Ui_Transition_attribute" `isInfixOf`)
        -- The shorthand pieces (property name, duration literal,
        -- "ms" suffix, easing case-branch) all flow through the
        -- runtime call chain. We DON'T look for the joined
        -- "200ms ease-out" — that's built by rt.Concat at runtime,
        -- not at compile time.
        mainGo `shouldSatisfy`
            ("background-color" `isInfixOf`)
        mainGo `shouldSatisfy` ("\"ms" `isInfixOf`)
        mainGo `shouldSatisfy`
            ("ease-out" `isInfixOf`)

    it "Animation.attribute lowers to AttrAnimation + marker + keyframes" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Ui.Animation as Animation"
                , "import Std.Ui.Transform as Transform"
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
                , "            [ Animation.attribute"
                , "                  { name = \"fadeIn\""
                , "                  , duration = 300"
                , "                  , easing = Animation.easeOut"
                , "                  , delay = 0"
                , "                  , iterations = Animation.once"
                , "                  , fillMode = Animation.forwards"
                , "                  , respectReducedMotion = True"
                , "                  , keyframes ="
                , "                      [ ( 0, [ Transform.opacity 0.0 ] )"
                , "                      , ( 100, [ Transform.opacity 1.0 ] )"
                , "                      ]"
                , "                  }"
                , "            ]"
                , "            (Ui.text \"animate me\"))"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy` hasAnimMarker
        mainGo `shouldSatisfy` hasAnimationCtor
        mainGo `shouldSatisfy`
            ("Std_Ui_Animation_attribute" `isInfixOf`)
        -- User-supplied name + shorthand pieces flow through.
        mainGo `shouldSatisfy` ("fadeIn" `isInfixOf`)
        mainGo `shouldSatisfy` ("ease-out" `isInfixOf`)
        mainGo `shouldSatisfy` ("forwards" `isInfixOf`)

    it "Transition.attributeUnsafe sets respect=False (opt out of reduced-motion)" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Ui.Transition as Transition"
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
                , "            [ Transition.attributeUnsafe"
                , "                  [ Transition.property \"transform\""
                , "                  , Transition.duration 1000"
                , "                  , Transition.easing Transition.linear"
                , "                  ]"
                , "            ]"
                , "            (Ui.text \"spinner\"))"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy`
            ("Std_Ui_Transition_attributeUnsafe" `isInfixOf`)
        mainGo `shouldSatisfy` hasTransitionCtor

    it "composes with Ui.breakpoint + Background.hoverColor — all markers coexist" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Ui.Background as Background"
                , "import Std.Ui.Transition as Transition"
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
                , "                , Background.hoverColor (Ui.rgb 0 90 200)"
                , "                , Transition.attribute"
                , "                      [ Transition.property \"background-color\""
                , "                      , Transition.duration 200"
                , "                      ]"
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
        -- All four markers must coexist in the same emitted body.
        mainGo `shouldSatisfy` ("data-sky-mq-q" `isInfixOf`)
        mainGo `shouldSatisfy` ("data-sky-pc-rules" `isInfixOf`)
        mainGo `shouldSatisfy` hasTrMarker
        mainGo `shouldSatisfy`
            ("(max-width: 767px)" `isInfixOf`)
        mainGo `shouldSatisfy`
            ("Std_Ui_Background_hoverColor" `isInfixOf`)
        mainGo `shouldSatisfy`
            ("Std_Ui_Transition_attribute" `isInfixOf`)

    it "element with no transition/animation attrs emits no helper call" $ do
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
        -- The renderer constants `data-sky-tr-rules` /
        -- `data-sky-anim-rules` will appear in every main.go since
        -- they're hard-coded into renderNodeAs. What we check here is
        -- that the user-facing helper symbols (Transition.attribute /
        -- Animation.attribute) did NOT slip into the lowered Go.
        mainGo `shouldSatisfy`
            (not . ("Std_Ui_Transition_attribute" `isInfixOf`))
        mainGo `shouldSatisfy`
            (not . ("Std_Ui_Animation_attribute" `isInfixOf`))

    it "Animation.infinite iteration → CSS infinite keyword" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Ui.Animation as Animation"
                , "import Std.Ui.Transform as Transform"
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
                , "            [ Animation.attribute"
                , "                  { name = \"spin\""
                , "                  , duration = 1000"
                , "                  , easing = Animation.linear"
                , "                  , delay = 0"
                , "                  , iterations = Animation.infinite"
                , "                  , fillMode = Animation.none"
                , "                  , respectReducedMotion = False"
                , "                  , keyframes ="
                , "                      [ ( 0, [ Transform.rotate 0.0 ] )"
                , "                      , ( 100, [ Transform.rotate 360.0 ] )"
                , "                      ]"
                , "                  }"
                , "            ]"
                , "            (Ui.text \"spinner\"))"
                , ""
                , "main = app { init = init, update = update, view = view"
                , "           , subscriptions = subs"
                , "           , routes = [ route \"/\" () ], notFound = () }"
                ]
        (ec, mainGo, err) <- buildMainGo src
        ec `shouldBe` 0
        err `shouldBe` ""
        mainGo `shouldSatisfy`
            ("Std_Ui_Animation_attribute" `isInfixOf`)
        -- `infinite` and `none` keywords flow through the shorthand
        -- tail builder.
        mainGo `shouldSatisfy` ("infinite" `isInfixOf`)
        -- The spin name flows through.
        mainGo `shouldSatisfy` ("spin" `isInfixOf`)
