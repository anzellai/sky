module Sky.Build.WebviewLoopbackAssetsSpec (spec) where

import Test.Hspec
import Data.List (isInfixOf)
import System.FilePath ((</>))

import Sky.Build.Helpers.InProcessCompile
    ( CompileOpts(..)
    , CompileOkRich(..)
    , compileInProcessWith
    , defaultCompileOpts
    )


-- Bug #370 — Sky.Webview can't load relative-path assets.
--
-- Design (locked): reuse the existing sky.toml `[live].static` key.
-- When set, the webview runtime spawns a 127.0.0.1 loopback http
-- server so the embedded webview can resolve `/static/foo.vrm` /
-- `/voice.js` etc. When unset, the runtime stays on the original
-- `w.SetHtml(webviewPageWrap(body))` path — no regression for
-- Sky.Ui-only apps (examples/31-webview-stopwatch-ui).
--
-- This spec is a 2-route fixture pinning BOTH cases:
--
--   1. With `[live].static = "public"` configured — the generated
--      init() registers SKY_LIVE_STATIC_DIR via SetSkyDefault, and
--      the materialised runtime contains the loopback server
--      helper (`startWebviewLoopback`). At runtime that env var is
--      what webviewStaticDir() reads to decide the Navigate path.
--
--   2. With NO static configured — the generated init() does NOT
--      register SKY_LIVE_STATIC_DIR, so webviewStaticDir() returns
--      "" and the runtime falls through to SetHtml. No-regression
--      guarantee for stopwatch / counter / hello-world style
--      desktop apps.
--
-- The runtime side (loopback server actually spawns + serves
-- /static/*) is pinned by Go unit tests in
-- runtime-go/rt/webview_test.go (TestWebviewLoopbackServesStaticAndBody
-- + TestWebviewLoopbackBindsLoopbackOnly).
--
-- Tier 1 (task #491): no subprocess `sky build` — the compile
-- pipeline runs IN-PROCESS via Sky.Build.Helpers.InProcessCompile.
-- The runtime materialisation (`copyRuntime`) STILL happens —
-- `compileInProcess`'s tempdir gets a real `sky-out/rt/webview.go`
-- so the loopback-helper assertion against that file's content
-- carries through unchanged.
spec :: Spec
spec = describe "Sky.Webview loopback assets (bug #370)" $ do

    it "[live].static = \"public\" → generated init emits SetSkyDefault(LIVE_STATIC_DIR)" $ do
        let opts = defaultCompileOpts
                { coToml = tomlWithStatic
                  -- Add a real asset under public/ so the build doesn't
                  -- have to invent one — keeps the fixture honest.
                , coExtraFiles =
                    [ ("public/model.vrm", "<binary>")
                    ]
                }
        compileInProcessWith opts webviewFixture $ \r ->
            case r of
                Left e -> expectationFailure ("compile failed: " ++ e)
                Right (CompileOkRich body outDir) -> do
                    -- The TOML key must be projected into the
                    -- runtime env-default. SetSkyDefault is no-op
                    -- when the process env already has the var, so
                    -- shell / .env still win — this just ships the
                    -- toml-configured default.
                    let setsStaticDefault =
                          "SetSkyDefault(\"LIVE_STATIC_DIR\", \"public\")" `isInfixOf` body
                    setsStaticDefault `shouldBe` True
                    -- And the call site still routes to rt.Webview_app
                    -- (regression sanity — we did not accidentally
                    -- break the Webview.app codegen path).
                    let routesToWebviewApp = "rt.Webview_app(" `isInfixOf` body
                    routesToWebviewApp `shouldBe` True
                    -- The materialised runtime under sky-out/rt/
                    -- must contain the loopback server helper —
                    -- without it the env var would have no effect
                    -- on the produced binary.
                    rtSrc <- readFile (outDir </> "rt" </> "webview.go")
                    let runtimeHasLoopback =
                          "startWebviewLoopback" `isInfixOf` rtSrc
                          && "webviewStaticDir" `isInfixOf` rtSrc
                          && "127.0.0.1:0" `isInfixOf` rtSrc
                    runtimeHasLoopback `shouldBe` True

    it "no [live].static → no SetSkyDefault(LIVE_STATIC_DIR); SetHtml path preserved" $ do
        let opts = defaultCompileOpts { coToml = tomlNoStatic }
        compileInProcessWith opts webviewFixture $ \r ->
            case r of
                Left e -> expectationFailure ("compile failed: " ++ e)
                Right (CompileOkRich body outDir) -> do
                    -- No-regression gate: an app without [live].static
                    -- must NOT register LIVE_STATIC_DIR (the env var
                    -- is the gate webviewStaticDir reads — registering
                    -- it would silently switch every stopwatch /
                    -- counter desktop app to Navigate mode).
                    let setsStaticDefault =
                          "SetSkyDefault(\"LIVE_STATIC_DIR\"" `isInfixOf` body
                    setsStaticDefault `shouldBe` False
                    -- Belt-and-braces: confirm rt.Webview_app is
                    -- still wired, AND the materialised runtime
                    -- still carries the original SetHtml-fallback
                    -- branch. We don't WANT to silently delete the
                    -- non-loopback path.
                    rtSrc <- readFile (outDir </> "rt" </> "webview.go")
                    let runtimeKeepsSetHtmlFallback =
                          "w.SetHtml(webviewPageWrap(body))" `isInfixOf` rtSrc
                    runtimeKeepsSetHtmlFallback `shouldBe` True

  where
    -- A minimal valid Webview.app program. Reuses the same shape
    -- as WebviewAppSpec's validFixture so a future stdlib API
    -- change touches one file.
    webviewFixture :: String
    webviewFixture =
        "module Main exposing (main)\n\n\
        \import Sky.Core.Prelude exposing (..)\n\
        \import Sky.Core.Task as Task\n\
        \import Std.Webview as Webview\n\
        \import Std.Cmd as Cmd\n\
        \import Std.Sub as Sub\n\
        \import Std.Ui as Ui\n\
        \import Std.Ui exposing (Element)\n\n\n\
        \type alias Model = { count : Int }\n\n\
        \type Msg = Inc | NoOp\n\n\
        \init : () -> ( Model, Cmd Msg )\n\
        \init _ = ( { count = 0 }, Cmd.none )\n\n\
        \update : Msg -> Model -> ( Model, Cmd Msg )\n\
        \update msg model =\n\
        \    case msg of\n\
        \        Inc -> ( { model | count = model.count + 1 }, Cmd.none )\n\
        \        NoOp -> ( model, Cmd.none )\n\n\
        \subscriptions : Model -> Sub Msg\n\
        \subscriptions _ = Sub.none\n\n\
        \view : Model -> Element Msg\n\
        \view model =\n\
        \    Ui.column [] [ Ui.text (String.fromInt model.count) ]\n\n\
        \main =\n\
        \    Webview.app\n\
        \        { init = init\n\
        \        , update = update\n\
        \        , view = view\n\
        \        , subscriptions = subscriptions\n\
        \        , window = { title = \"Test\", size = ( 800, 600 ) }\n\
        \        }\n\
        \        |> Task.run\n"

    tomlWithStatic :: String
    tomlWithStatic =
        "name = \"webview-loopback-test\"\n\
        \version = \"0.0.0\"\n\
        \entry = \"src/Main.sky\"\n\n\
        \[source]\n\
        \root = \"src\"\n\n\
        \[live]\n\
        \static = \"public\"\n"

    tomlNoStatic :: String
    tomlNoStatic =
        "name = \"webview-loopback-no-static\"\n\
        \version = \"0.0.0\"\n\
        \entry = \"src/Main.sky\"\n\n\
        \[source]\n\
        \root = \"src\"\n"
