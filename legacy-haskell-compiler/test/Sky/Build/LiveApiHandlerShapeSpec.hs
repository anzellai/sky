module Sky.Build.LiveApiHandlerShapeSpec (spec) where

-- Task #545 regression — Std.Live.api must reject wrongly-shaped
-- handlers at HM type-check.
--
-- Pre-fix Std.Live.api had no entry in
-- src/Sky/Type/Constrain/Expression.hs's kernel-type switch, so a
-- handler shaped `Request -> Task Error Response` (the
-- Sky.Http.Server shape) unified silently with what `api`
-- "expected".  The lowerer stored it as `any`, and at runtime
-- Sky.Live's reflective Call against the wrong shape fell through
-- — the closure value was %v-printed into the response body as a
-- Go pointer string (e.g. `0x105129450`).
--
-- The fix adds:
--
--   ("Live", "api") ->
--       Just $ T.Forall []
--           (T.TLambda stringType
--               (T.TLambda
--                   (T.TLambda
--                       (T.TType ModuleName.dict "Dict"
--                           [stringType, T.TVar "any"])
--                       (T.TType (ModuleName.Canonical "") "Response" []))
--                   (T.TType (ModuleName.Canonical "") "Route" [])))
--
-- so `Live.api` is typed `String -> (Dict String any -> Response)
-- -> Route`, matching the runtime contract that
-- Live.app dispatches API routes synchronously.
--
-- This spec asserts:
--   1. A Dict-shaped handler passes HM (positive control).
--   2. A Task-shaped handler is rejected at HM with a clear
--      type-mismatch error pointing at the handler.

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO (writeFile)
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
          else fail ("sky binary missing at " ++ candidate
                  ++ " — run cabal install --installdir=./sky-out first")


runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
runSky sky args workDir = do
    let cp = (proc sky args) { cwd = Just workDir }
    readCreateProcessWithExitCode cp ""


writeProject :: FilePath -> String -> IO ()
writeProject root mainBody = do
    createDirectoryIfMissing True (root </> "src")
    writeFile (root </> "sky.toml") $ unlines
        [ "name = \"api-shape-test\""
        , "version = \"0.1.0\""
        , "entry = \"src/Main.sky\""
        , ""
        , "[source]"
        , "root = \"src\""
        , ""
        , "[live]"
        , "port = 8765"
        ]
    writeFile (root </> "src" </> "Main.sky") mainBody


-- A minimal Sky.Live app shell.  The caller supplies the api-
-- registration line (which is the variable under test) and any
-- handler definitions it needs.
appShell :: String -> String -> String
appShell handlerDefs apiLine = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Task as Task"
    , "import Std.Cmd as Cmd"
    , "import Std.Sub as Sub"
    , "import Std.Live exposing (app, api, route)"
    , "import Sky.Http.Server as Server"
    , "import Sky.Http.Server exposing (Request, Response)"
    , "import Sky.Core.Error as Error exposing (Error)"
    , "import Std.Html as Html"
    , ""
    , "type Page = HomePage"
    , "type Msg = NoOp"
    , ""
    , "init : Dict String any -> ( String, Cmd.Cmd Msg )"
    , "init _ = ( \"\", Cmd.none )"
    , ""
    , "update : Msg -> String -> ( String, Cmd.Cmd Msg )"
    , "update _ m = ( m, Cmd.none )"
    , ""
    -- Live.app's kernel sig constrains view to `Model -> Html msg`.
    -- Earlier iteration of this fixture used `view : String -> any`
    -- — wildcard `any` is per-occurrence (not polymorphic) so it
    -- doesn't unify with `Html Msg` in the cfg record. Bind a real
    -- Html return so the positive test exercises ONLY the api
    -- handler shape under test, not an incidental view-type
    -- mismatch.
    , "view : String -> Html.Html Msg"
    , "view _ = Html.text \"\""
    , ""
    , "subscriptions : String -> Sub.Sub Msg"
    , "subscriptions _ = Sub.none"
    , ""
    , handlerDefs
    , ""
    , "main ="
    , "    app"
    , "        { init = init"
    , "        , update = update"
    , "        , view = view"
    , "        , subscriptions = subscriptions"
    , "        , routes = [ route \"/\" HomePage ]"
    , "        , notFound = HomePage"
    , "        , api = [ " ++ apiLine ++ " ]"
    , "        }"
    ]


spec :: Spec
spec = describe "Sky.Live.api handler shape (Task #545)" $ do

    it "accepts the canonical Dict String any -> Response shape" $ do
        sky <- findSky
        withSystemTempDirectory "sky-api-shape-positive" $ \dir -> do
            let goodHandler = unlines
                    [ "okHandler : Dict String any -> Response"
                    , "okHandler _ ="
                    , "    Server.json \"{}\""
                    ]
            writeProject dir (appShell goodHandler "api \"GET /ok\" okHandler")
            (code, _out, err) <-
                runSky sky ["check", "src/Main.sky"] dir
            case code of
                ExitSuccess   -> return ()
                ExitFailure _ ->
                    expectationFailure $
                        "Dict-shaped handler should have passed HM; stderr was:\n"
                            ++ err

    it "rejects a Request -> Task Error Response handler with a clear HM mismatch" $ do
        sky <- findSky
        withSystemTempDirectory "sky-api-shape-negative" $ \dir -> do
            let badHandler = unlines
                    [ "badHandler : Request -> Task Error Response"
                    , "badHandler _ ="
                    , "    Task.succeed (Server.json \"{}\")"
                    ]
            writeProject dir (appShell badHandler "api \"GET /bad\" badHandler")
            (code, out, err) <-
                runSky sky ["check", "src/Main.sky"] dir
            let combined = out ++ "\n" ++ err
            case code of
                ExitSuccess ->
                    expectationFailure $
                        "Task-shaped handler should have failed HM but the\n"
                            ++ "compiler accepted it (silent runtime %v leak\n"
                            ++ "would follow at runtime).\nOutput was:\n"
                            ++ combined
                ExitFailure _ -> do
                    -- The error message must call out the type mismatch
                    -- so the user knows which arg is at fault.  We match
                    -- on both the handler name and the conflicting types
                    -- — keeps the assertion stable across error-message
                    -- formatting tweaks but rejects accidental
                    -- "any unified with anything" silent passes.
                    combined `shouldSatisfy`
                        (\s -> "badHandler" `isInfixOf` s)
                    combined `shouldSatisfy`
                        (\s -> "Dict" `isInfixOf` s)
                    combined `shouldSatisfy`
                        (\s -> "Task" `isInfixOf` s)
