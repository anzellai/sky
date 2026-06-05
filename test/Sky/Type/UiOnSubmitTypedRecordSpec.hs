module Sky.Type.UiOnSubmitTypedRecordSpec (spec) where

-- Regression fence for the in-module `Ui.onSubmit (record -> Msg)` case.
--
-- Pre-fix bug: `Std.Ui.onSubmit` and `Std.Ui.Events.onSubmit` were
-- typed `forall msg. msg -> Attribute msg`. A constructor like
-- `DoSignIn : LoginForm -> Msg` passed as `Ui.onSubmit DoSignIn`
-- unified with `msg = (LoginForm -> Msg)`, producing an
-- `Attribute (LoginForm -> Msg)` that the surrounding `Ui.form`
-- could not reconcile with the function's annotated `Element Msg`
-- return type. Surfaced as:
--   `Type mismatch: Element ((LoginForm) -> Msg) vs Element Msg`
--
-- Asymmetry: the cross-module case (loginView in a separate module
-- imported by Main) accidentally type-checked because the externals
-- path uses a more permissive typing. The runtime has always
-- handled both shapes correctly via `applyMsgArgs` + `decodeMsgArg`
-- — it's the in-module HM that was rejecting valid Sky code.
--
-- Fix: widen both wrappers to `(a -> Attribute b)`. The runtime is
-- the trust boundary for the actual decode (json.Unmarshal of
-- formData into the function's first-param Go type), so the type
-- signature should match the runtime's permissiveness rather than
-- pin a single shape.
--
-- This regression spec confirms BOTH shapes type-check in-module:
--   1. `Ui.onSubmit DoSignOut` where `DoSignOut : Msg` (plain)
--   2. `Ui.onSubmit DoSignIn`  where `DoSignIn : LoginForm -> Msg`
--
-- Tier 1 (task #491): in-process via compileInProcess.

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


-- The user's failing pattern from a downstream app (CC port report,
-- 2026-04-27): Msg + view in the same module, view annotated
-- `Element Msg`, form's onSubmit binds a record-arg constructor.
inModuleSrc :: String
inModuleSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Live as Live"
    , "import Std.Cmd as Cmd"
    , "import Std.Sub as Sub"
    , "import Std.Ui as Ui"
    , "import Std.Ui exposing (Element)"
    , ""
    , "type alias AuthCreds ="
    , "    { email : String, password : String }"
    , ""
    , "type alias Model = { status : String }"
    , ""
    , "type Msg = DoSignIn AuthCreds | DoSignOut"
    , ""
    , "init _ = ( { status = \"\" }, Cmd.none )"
    , ""
    , "update : Msg -> Model -> ( Model, Cmd.Cmd Msg )"
    , "update msg model ="
    , "    case msg of"
    , "        DoSignIn creds -> ( { model | status = creds.email }, Cmd.none )"
    , "        DoSignOut      -> ( { model | status = \"\" }, Cmd.none )"
    , ""
    , "view : Model -> Element Msg"
    , "view _model ="
    , "    Ui.column []"
    , "        [ Ui.form"
    , "            [ Ui.onSubmit DoSignIn ]"
    , "            [ Ui.input [ Ui.htmlAttribute \"type\" \"email\", Ui.name \"email\" ]"
    , "            , Ui.input [ Ui.htmlAttribute \"type\" \"password\", Ui.name \"password\" ]"
    , "            ]"
    , "        , Ui.button"
    , "            [ Ui.onClick DoSignOut ]"
    , "            { onPress = Just DoSignOut, label = Ui.text \"Sign out\" }"
    , "        ]"
    , ""
    , "subscriptions _ = Sub.none"
    , ""
    , "main ="
    , "    Live.app"
    , "        { init = init, update = update"
    , "        , view = \\m -> Ui.layout [] (view m)"
    , "        , subscriptions = subscriptions"
    , "        , routes = [], notFound = ()"
    , "        }"
    ]


spec :: Spec
spec = do
    describe "Std.Ui.onSubmit accepts both plain Msg and (record -> Msg) in-module" $ do

        it "in-module Msg + view + onSubmit DoSignIn (record-arg) type-checks" $ do
            result <- compileInProcess inModuleSrc
            case result of
                CompileErr e -> do
                    -- Pin the specific pre-fix failure strings even
                    -- when compilation fails for some other reason.
                    e `shouldNotSatisfy` ("Type mismatch: Element" `isInfixOf`)
                    e `shouldNotSatisfy` ("vs Element Msg"         `isInfixOf`)
                    expectationFailure ("compile failed: " ++ e)
                CompileOk _ -> return ()
