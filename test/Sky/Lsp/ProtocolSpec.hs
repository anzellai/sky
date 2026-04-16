{-# LANGUAGE OverloadedStrings #-}
module Sky.Lsp.ProtocolSpec (spec) where

-- Audit P3-2: LSP integration harness. Spawns `sky lsp` as a
-- subprocess, speaks JSON-RPC with Content-Length framing, and
-- asserts hover responses. Pre-fix there was no LSP test at all —
-- hover regressions shipped silently. This spec locks the basic
-- protocol shape and hover cases mapped to the P2-2 local-types +
-- P2-3 stable-rename fixes.

import Test.Hspec
import qualified Data.Aeson as Aeson
import Data.Aeson ((.=), Value(..))
import qualified Data.Aeson.KeyMap as KM
import qualified Data.Text as T
import System.Directory (createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)

import Sky.Lsp.Harness
    ( findSky, withLsp
    , sendMsg, recvResponseFor
    , initializeLsp, didOpen
    , posRequest
    )


spec :: Spec
spec = do
    describe "LSP protocol integration (audit P3-2)" $ do

        it "initialize → hoverProvider: true" $ do
            sky <- findSky
            withLsp sky $ \hin hout -> do
                sendMsg hin $ Aeson.object
                    [ "jsonrpc" .= ("2.0" :: T.Text)
                    , "id"      .= (1 :: Int)
                    , "method"  .= ("initialize" :: T.Text)
                    , "params"  .= Aeson.object
                        [ "processId"    .= Aeson.Null
                        , "rootUri"      .= Aeson.Null
                        , "capabilities" .= Aeson.object []
                        ]
                    ]
                resp <- recvResponseFor hout 1
                let caps = case resp of
                        Object o -> case KM.lookup "result" o of
                            Just (Object r) -> KM.lookup "capabilities" r
                            _ -> Nothing
                        _ -> Nothing
                case caps of
                    Just (Object c) ->
                        case KM.lookup "hoverProvider" c of
                            Just (Bool True) -> return ()
                            other -> expectationFailure
                                $ "hoverProvider != true: " ++ show other
                    _ -> expectationFailure "no capabilities in initialize response"

        it "hover on a top-level value returns its type signature" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp" $ \dir -> do
                -- buildIndex needs a real sky.toml + entry on disk so
                -- the workspace typecheck runs and populates symbols.
                let src = unlines
                        [ "module Main exposing (answer)"
                        , ""
                        , "import Sky.Core.Prelude exposing (..)"
                        , ""
                        , "answer : Int"
                        , "answer = 42"
                        ]
                    srcDir = dir </> "src"
                    fixture = srcDir </> "Main.sky"
                    toml = dir </> "sky.toml"
                createDirectoryIfMissing True srcDir
                writeFile toml "name = \"lsp-fixture\"\nentry = \"src/Main.sky\"\n"
                writeFile fixture src
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture src
                    -- Hover on `answer` in its definition (0-indexed).
                    sendMsg hin $ posRequest "textDocument/hover" 2 fixture 5 0
                    resp <- recvResponseFor hout 2
                    let content = hoverContent resp
                    -- Accept either the annotation-preserved
                    -- "answer : Int" or any string containing "Int".
                    case content of
                        Just txt
                            | "Int" `T.isInfixOf` txt -> return ()
                            | otherwise -> expectationFailure
                                $ "hover content missing Int: " ++ T.unpack txt
                        Nothing -> expectationFailure "hover returned no content"


-- Extract the hover content string from a response. LSP supports
-- both { "contents": "…" } and { "contents": { "kind": "markdown",
-- "value": "…" } } shapes — Sky uses the MarkupContent form.
hoverContent :: Aeson.Value -> Maybe T.Text
hoverContent v = case v of
    Object o -> case KM.lookup "result" o of
        Just (Object r) -> case KM.lookup "contents" r of
            Just (Object c) -> case KM.lookup "value" c of
                Just (String t) -> Just t
                _ -> Nothing
            Just (String t) -> Just t
            _ -> Nothing
        _ -> Nothing
    _ -> Nothing
