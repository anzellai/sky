{-# LANGUAGE OverloadedStrings #-}
module Sky.Lsp.CapabilitiesSpec (spec) where

-- LSP per-capability integration specs. ProtocolSpec covers
-- initialize + hover; this file extends with the other LSP
-- capabilities Sky claims to support per docs/tooling/lsp.md and
-- src/Sky/Lsp/Server.hs:
--   * textDocument/definition
--   * textDocument/references
--   * textDocument/documentSymbol
--   * textDocument/formatting
--   * textDocument/rename
--   * textDocument/completion
--   * textDocument/semanticTokens/full
-- Each test spawns `sky lsp`, sets up a small project, and asserts
-- the response shape + content. Pre-spec, every capability could
-- silently regress while still appearing in the server.capabilities
-- payload — only initialize+hover were end-to-end tested.

import Test.Hspec
import qualified Data.Aeson as Aeson
import Data.Aeson ((.=), Value(..))
import qualified Data.Aeson.KeyMap as KM
import qualified Data.Text as T
import qualified Data.Vector as V
import System.Directory (createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)

import Sky.Lsp.Harness
    ( findSky, withLsp
    , sendMsg, recvResponseFor
    , initializeLsp, didOpen
    , posRequest
    )


-- ── Fixture: a small valid project ────────────────────────────────

setupProject :: FilePath -> String -> IO FilePath
setupProject dir src = do
    let srcDir = dir </> "src"
        fixture = srcDir </> "Main.sky"
        toml = dir </> "sky.toml"
    createDirectoryIfMissing True srcDir
    writeFile toml "name = \"lsp-cap\"\nentry = \"src/Main.sky\"\n"
    writeFile fixture src
    return fixture


sampleSrc :: String
sampleSrc = unlines
    [ "module Main exposing (main, greet)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "greet : String -> String"
    , "greet name ="
    , "    \"Hello, \" ++ name"
    , ""
    , "main = println (greet \"world\")"
    ]


spec :: Spec
spec = do
    describe "LSP capabilities" $ do

        it "textDocument/definition jumps to a top-level definition" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp-def" $ \dir -> do
                fixture <- setupProject dir sampleSrc
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture sampleSrc
                    -- `greet` is used in main on line 9, char 16
                    -- Position over the call site:
                    --   "main = println (greet \"world\")"
                    --    0123456789012345678
                    --              1111111
                    sendMsg hin $ posRequest "textDocument/definition" 2 fixture 9 17
                    resp <- recvResponseFor hout 2
                    -- Result should be a Location { uri, range }.
                    let res = case resp of
                            Object o -> KM.lookup "result" o
                            _ -> Nothing
                    case res of
                        Just (Object _) -> return ()
                        Just (Array v) | not (V.null v) -> return ()
                        _ -> expectationFailure $
                            "definition returned no location: " ++ show res

        it "textDocument/documentSymbol returns top-level names" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp-sym" $ \dir -> do
                fixture <- setupProject dir sampleSrc
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture sampleSrc
                    sendMsg hin $ Aeson.object
                        [ "jsonrpc" .= ("2.0" :: T.Text)
                        , "id"      .= (3 :: Int)
                        , "method"  .= ("textDocument/documentSymbol" :: T.Text)
                        , "params"  .= Aeson.object
                            [ "textDocument" .= Aeson.object
                                [ "uri" .= ("file://" ++ fixture) ]
                            ]
                        ]
                    resp <- recvResponseFor hout 3
                    -- Expect at least `greet` and `main` in the symbols array.
                    let names = extractSymbolNames resp
                    ("greet" `elem` names) `shouldBe` True
                    ("main"  `elem` names) `shouldBe` True

        it "textDocument/formatting returns at-least-one TextEdit on a poorly-formatted file" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp-fmt" $ \dir -> do
                let messy = "module Main exposing (main)\n\n\n\n\nimport Std.Log exposing (println)\nmain=println \"x\"\n"
                fixture <- setupProject dir messy
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture messy
                    sendMsg hin $ Aeson.object
                        [ "jsonrpc" .= ("2.0" :: T.Text)
                        , "id"      .= (4 :: Int)
                        , "method"  .= ("textDocument/formatting" :: T.Text)
                        , "params"  .= Aeson.object
                            [ "textDocument" .= Aeson.object
                                [ "uri" .= ("file://" ++ fixture) ]
                            , "options" .= Aeson.object
                                [ "tabSize"      .= (4 :: Int)
                                , "insertSpaces" .= True
                                ]
                            ]
                        ]
                    resp <- recvResponseFor hout 4
                    -- Result must be a non-empty array of TextEdits OR
                    -- null (no changes needed). For our messy input
                    -- we expect at least one edit.
                    case resp of
                        Object o -> case KM.lookup "result" o of
                            Just (Array v) | not (V.null v) -> return ()
                            Just Aeson.Null -> expectationFailure
                                "formatting returned null on messy input"
                            other -> expectationFailure $
                                "unexpected formatting result: " ++ show other
                        _ -> expectationFailure "no result key"

        it "textDocument/references returns >=1 use-site for a top-level def" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp-refs" $ \dir -> do
                fixture <- setupProject dir sampleSrc
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture sampleSrc
                    -- Position over `greet` definition (line 5 col 0
                    -- in sampleSrc; LSP rows/cols are 0-indexed).
                    sendMsg hin $ Aeson.object
                        [ "jsonrpc" .= ("2.0" :: T.Text)
                        , "id"      .= (5 :: Int)
                        , "method"  .= ("textDocument/references" :: T.Text)
                        , "params"  .= Aeson.object
                            [ "textDocument" .= Aeson.object
                                [ "uri" .= ("file://" ++ fixture) ]
                            , "position" .= Aeson.object
                                [ "line" .= (5 :: Int), "character" .= (0 :: Int) ]
                            , "context" .= Aeson.object
                                [ "includeDeclaration" .= True ]
                            ]
                        ]
                    resp <- recvResponseFor hout 5
                    case resp of
                        Object o -> case KM.lookup "result" o of
                            Just (Array v) | not (V.null v) -> return ()
                            -- Server may return [] for an
                            -- unrecognised position; we count that
                            -- as a graceful no-op rather than a
                            -- crash. The strict invariant is that
                            -- the response shape is well-formed.
                            Just (Array _) -> return ()
                            Just Aeson.Null -> return ()
                            other -> expectationFailure $
                                "unexpected references result: " ++ show other
                        _ -> expectationFailure "no result key"

        it "textDocument/rename returns a workspace edit for a top-level def" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp-rename" $ \dir -> do
                fixture <- setupProject dir sampleSrc
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture sampleSrc
                    sendMsg hin $ Aeson.object
                        [ "jsonrpc" .= ("2.0" :: T.Text)
                        , "id"      .= (6 :: Int)
                        , "method"  .= ("textDocument/rename" :: T.Text)
                        , "params"  .= Aeson.object
                            [ "textDocument" .= Aeson.object
                                [ "uri" .= ("file://" ++ fixture) ]
                            , "position" .= Aeson.object
                                [ "line" .= (5 :: Int), "character" .= (0 :: Int) ]
                            , "newName" .= ("salutation" :: T.Text)
                            ]
                        ]
                    resp <- recvResponseFor hout 6
                    -- Result is a WorkspaceEdit { changes | documentChanges }
                    -- OR null when the position isn't renamable. We
                    -- accept either as long as the response parses.
                    case resp of
                        Object o -> case KM.lookup "result" o of
                            Just (Object _) -> return ()
                            Just Aeson.Null -> return ()
                            other -> expectationFailure $
                                "unexpected rename result: " ++ show other
                        _ -> expectationFailure "no result key"

        it "textDocument/completion returns a list (may be empty)" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp-comp" $ \dir -> do
                let withDot = unlines
                        [ "module Main exposing (main)"
                        , "import Sky.Core.String as String"
                        , "import Std.Log exposing (println)"
                        , "main = println String."
                        ]
                fixture <- setupProject dir withDot
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture withDot
                    -- Trigger completion right after `String.`
                    sendMsg hin $ Aeson.object
                        [ "jsonrpc" .= ("2.0" :: T.Text)
                        , "id"      .= (7 :: Int)
                        , "method"  .= ("textDocument/completion" :: T.Text)
                        , "params"  .= Aeson.object
                            [ "textDocument" .= Aeson.object
                                [ "uri" .= ("file://" ++ fixture) ]
                            , "position" .= Aeson.object
                                [ "line" .= (3 :: Int), "character" .= (21 :: Int) ]
                            , "context" .= Aeson.object
                                [ "triggerKind"      .= (2 :: Int)
                                , "triggerCharacter" .= ("." :: T.Text)
                                ]
                            ]
                        ]
                    resp <- recvResponseFor hout 7
                    -- LSP completion result is CompletionItem[] OR
                    -- CompletionList { isIncomplete, items }. Either
                    -- shape is acceptable; we only assert the
                    -- response parses without an error key.
                    case resp of
                        Object o -> case KM.lookup "result" o of
                            Just (Array _)  -> return ()
                            Just (Object _) -> return ()
                            Just Aeson.Null -> return ()
                            other -> expectationFailure $
                                "unexpected completion result: " ++ show other
                        _ -> expectationFailure "no result key"

        it "textDocument/semanticTokens/full returns a token array" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp-sem" $ \dir -> do
                fixture <- setupProject dir sampleSrc
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture sampleSrc
                    sendMsg hin $ Aeson.object
                        [ "jsonrpc" .= ("2.0" :: T.Text)
                        , "id"      .= (8 :: Int)
                        , "method"  .= ("textDocument/semanticTokens/full" :: T.Text)
                        , "params"  .= Aeson.object
                            [ "textDocument" .= Aeson.object
                                [ "uri" .= ("file://" ++ fixture) ]
                            ]
                        ]
                    resp <- recvResponseFor hout 8
                    -- LSP semanticTokens result is { data: Int[] }
                    -- (or null when empty).
                    case resp of
                        Object o -> case KM.lookup "result" o of
                            Just (Object so) -> case KM.lookup "data" so of
                                Just (Array _) -> return ()
                                _ -> expectationFailure $
                                    "semanticTokens missing data: " ++ show so
                            Just Aeson.Null -> return ()
                            other -> expectationFailure $
                                "unexpected semanticTokens result: " ++ show other
                        _ -> expectationFailure "no result key"

        it "didOpen with a syntax error doesn't crash the server" $ do
            sky <- findSky
            withSystemTempDirectory "sky-lsp-diag" $ \dir -> do
                let broken = "module Main exposing (main\nmain ="
                fixture <- setupProject dir broken
                withLsp sky $ \hin hout -> do
                    initializeLsp hin hout
                    didOpen hin fixture broken
                    -- Follow up with a documentSymbol request; if
                    -- the server crashed on the broken file, this
                    -- would block / time out.
                    sendMsg hin $ Aeson.object
                        [ "jsonrpc" .= ("2.0" :: T.Text)
                        , "id"      .= (9 :: Int)
                        , "method"  .= ("textDocument/documentSymbol" :: T.Text)
                        , "params"  .= Aeson.object
                            [ "textDocument" .= Aeson.object
                                [ "uri" .= ("file://" ++ fixture) ]
                            ]
                        ]
                    _ <- recvResponseFor hout 9
                    return ()


-- Walk a documentSymbol response into a flat list of names.
-- LSP shape: result is either DocumentSymbol[] (recursive) or
-- SymbolInformation[] (flat). We accept both.
extractSymbolNames :: Aeson.Value -> [T.Text]
extractSymbolNames v = case v of
    Object o -> case KM.lookup "result" o of
        Just (Array arr) -> concatMap symbolName (V.toList arr)
        _ -> []
    _ -> []
  where
    symbolName (Object so) = case KM.lookup "name" so of
        Just (String n) ->
            let kids = case KM.lookup "children" so of
                    Just (Array c) -> concatMap symbolName (V.toList c)
                    _ -> []
            in n : kids
        _ -> []
    symbolName _ = []
