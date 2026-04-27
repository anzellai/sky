{-# LANGUAGE OverloadedStrings #-}
module Sky.Lsp.DiagnosticsSpec (spec) where

-- LSP diagnostic-parity specs. The editor must see every error
-- `sky check` / `sky build` sees — anything less is a developer-
-- experience regression. This file asserts the signal flows through
-- `textDocument/publishDiagnostics` for:
--
--   * non-exhaustive case expressions (Gap 2a)
--   * undefined/unbound names (Gap 2b, depends on Gap 1)
--
-- Infrastructure note: `awaitNotification` in Sky.Lsp.Harness waits
-- for the server-pushed diagnostic (no request/response correlation).
-- Pre-harness, ProtocolSpec/CapabilitiesSpec discarded such
-- notifications while draining for a response.

import Test.Hspec
import qualified Data.Aeson as Aeson
import Data.Aeson (Value(..))
import qualified Data.Aeson.KeyMap as KM
import qualified Data.Text as T
import qualified Data.Vector as V
import System.Directory (createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)

import Sky.Lsp.Harness
    ( findSky, withLsp
    , initializeLsp, didOpen
    , awaitNotification
    )


setupProject :: FilePath -> String -> IO FilePath
setupProject dir src = do
    let srcDir = dir </> "src"
        fixture = srcDir </> "Main.sky"
        toml = dir </> "sky.toml"
    createDirectoryIfMissing True srcDir
    writeFile toml "name = \"lsp-diag\"\nentry = \"src/Main.sky\"\n"
    writeFile fixture src
    return fixture


-- | Extract the list of diagnostic message strings from a
-- publishDiagnostics notification payload.
diagnosticMessages :: Aeson.Value -> [T.Text]
diagnosticMessages v = case v of
    Object o -> case KM.lookup "params" o of
        Just (Object p) -> case KM.lookup "diagnostics" p of
            Just (Array arr) -> concatMap getMsg (V.toList arr)
            _ -> []
        _ -> []
    _ -> []
  where
    getMsg (Object d) = case KM.lookup "message" d of
        Just (String t) -> [t]
        _ -> []
    getMsg _ = []


anyMatch :: T.Text -> [T.Text] -> Bool
anyMatch needle = any (needle `T.isInfixOf`)


spec :: Spec
spec = do
    describe "LSP publishes diagnostics for every Sky-level error" $ do

        it "Gap 2a — non-exhaustive case surfaces as an editor diagnostic" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type Colour = Red | Green | Blue"
                    , ""
                    , "name c ="
                    , "    case c of"
                    , "        Red -> \"red\""
                    , "        Green -> \"green\""
                    , ""
                    , "main = println (name Red)"
                    ]
            withSystemTempDirectory "sky-lsp-diag-exhaust" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    -- Server pushes publishDiagnostics after didOpen.
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics notification within budget"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            anyMatch "Non-exhaustive" msgs `shouldBe` True
                            -- The missing constructor should be named.
                            anyMatch "Blue" msgs `shouldBe` True

        it "Gap 2b — undefined name surfaces as an editor diagnostic" $ do
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println messgae"
                    ]
            withSystemTempDirectory "sky-lsp-diag-unbound" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics notification within budget"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            anyMatch "Undefined name" msgs `shouldBe` True
                            anyMatch "messgae" msgs `shouldBe` True

        it "clean file produces a diagnostics notification with an empty array" $ do
            -- Positive control: a valid file should still trigger
            -- publishDiagnostics (empty), so editors that cache
            -- diagnostics clear stale state.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println \"hi\""
                    ]
            withSystemTempDirectory "sky-lsp-diag-clean" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on clean file"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            msgs `shouldBe` []

        it "user-defined monadic-do chain on Result produces no diagnostics" $ do
            -- Regression for Bug #1: previously `sky check` passed but
            -- `go build` failed on this pattern. LSP piggy-backs on the
            -- sky-check pipeline (Parse → Canonicalise → Constrain →
            -- Solve → Exhaustiveness), so a false-positive LSP
            -- diagnostic here would mean the type solver was spuriously
            -- rejecting a valid user-HOF chain. Empty diagnostics =>
            -- LSP agrees with the fixed codegen.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Result as Result"
                    , "import Sky.Core.Error as Error exposing (Error)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "do : Result Error a -> (a -> Result Error b) -> Result Error b"
                    , "do result fn ="
                    , "    Result.andThen fn result"
                    , ""
                    , "pipeline : String -> Result Error (String, String)"
                    , "pipeline key ="
                    , "    do (firstStep key) (\\a ->"
                    , "    do (secondStep a) (\\b ->"
                    , "    Ok (a, b)))"
                    , ""
                    , "firstStep : String -> Result Error String"
                    , "firstStep key ="
                    , "    if key == \"\" then"
                    , "        Err (Error.invalidInput \"empty key\")"
                    , "    else"
                    , "        Ok (\"first:\" ++ key)"
                    , ""
                    , "secondStep : String -> Result Error String"
                    , "secondStep a ="
                    , "    Ok (\"second:\" ++ a)"
                    , ""
                    , "main ="
                    , "    case pipeline \"hello\" of"
                    , "        Ok (a, b) ->"
                    , "            println (\"ok \" ++ a ++ \" \" ++ b)"
                    , ""
                    , "        Err e ->"
                    , "            println (errorToString e)"
                    ]
            withSystemTempDirectory "sky-lsp-diag-user-hof" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on user-HOF chain"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            msgs `shouldBe` []

        it "TEA with Live.app: LSP suppresses no-externals false-positive" $ do
            -- The LSP's runPipeline calls the no-externals variant
            -- of the constraint generator, so cross-module record
            -- kernel sigs (notably `Live.app`) false-positive with
            -- `Type mismatch: { ... } vs { ... }`. Until the proper
            -- externals helper lands, the LSP heuristically
            -- suppresses these (`isLikelyExternalsFalsePositive`)
            -- so users editing TEA apps don't see phantom errors
            -- that `sky check` doesn't report.
            sky <- findSky
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Cmd as Cmd"
                    , "import Std.Sub as Sub"
                    , "import Std.Live exposing (app)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Model = { count : Int }"
                    , "type Msg = Tick"
                    , ""
                    , "init : a -> ( Model, Cmd Msg )"
                    , "init _ ="
                    , "    ( { count = 0 }, Cmd.none )"
                    , ""
                    , "update : Msg -> Model -> ( Model, Cmd Msg )"
                    , "update msg model ="
                    , "    ( model, Cmd.none )"
                    , ""
                    , "view : Model -> any"
                    , "view _ = \"hi\""
                    , ""
                    , "subscriptions _ = Sub.none"
                    , ""
                    , "main ="
                    , "    app"
                    , "        { init = init"
                    , "        , update = update"
                    , "        , view = view"
                    , "        , subscriptions = subscriptions"
                    , "        , routes = []"
                    , "        , notFound = ()"
                    , "        }"
                    ]
            withSystemTempDirectory "sky-lsp-tea-app" $ \dir -> do
                fixture <- setupProject dir src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    result <- awaitNotification hout "textDocument/publishDiagnostics"
                    case result of
                        Nothing -> expectationFailure
                            "no publishDiagnostics on TEA-app file"
                        Just payload -> do
                            let msgs = diagnosticMessages payload
                            -- The `{ ... } vs { ... }` heuristic
                            -- catches this; LSP shows no errors.
                            -- (For authoritative diagnostics users
                            -- should run `sky check` — same as today.)
                            msgs `shouldBe` []
