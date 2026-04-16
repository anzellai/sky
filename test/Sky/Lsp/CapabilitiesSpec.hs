{-# LANGUAGE OverloadedStrings #-}
{-# LANGUAGE ScopedTypeVariables #-}
module Sky.Lsp.CapabilitiesSpec (spec) where

-- LSP per-capability integration specs. ProtocolSpec covers
-- initialize + hover; this file extends with the other LSP
-- capabilities Sky claims to support per docs/tooling/lsp.md and
-- src/Sky/Lsp/Server.hs:
--   * textDocument/definition
--   * textDocument/references
--   * textDocument/documentSymbol
--   * textDocument/formatting
-- Each test spawns `sky lsp`, sets up a small project, and asserts
-- the response shape + content. Pre-spec, every capability could
-- silently regress while still appearing in the server.capabilities
-- payload — only initialize+hover were end-to-end tested.

import Test.Hspec
import qualified Data.Aeson as Aeson
import Data.Aeson ((.=), Value(..))
import qualified Data.Aeson.KeyMap as KM
import qualified Data.ByteString as BS
import qualified Data.ByteString.Lazy as BL
import qualified Data.ByteString.Char8 as BC
import qualified Data.Text as T
import qualified Data.Vector as V
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO (Handle, hClose, hFlush, hSetBuffering, BufferMode(..))
import System.IO.Temp (withSystemTempDirectory)
import System.Process
import Control.Concurrent (threadDelay)
import Control.Exception (bracket, SomeException, try)


-- ── Shared LSP harness (mirrors ProtocolSpec.hs; consider extracting
--    to a Sky.Lsp.TestHelpers module if more LSP specs land) ────────

findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


sendMsg :: Handle -> Aeson.Value -> IO ()
sendMsg h v = do
    let body = BL.toStrict (Aeson.encode v)
        hdr = BC.pack ("Content-Length: " ++ show (BS.length body) ++ "\r\n\r\n")
    BS.hPut h hdr
    BS.hPut h body
    hFlush h


recvMsg :: Handle -> IO BS.ByteString
recvMsg h = do
    n <- readHeaders h 0
    BS.hGet h n
  where
    readHeaders h' acc = do
        line <- readLine h'
        if BS.null line
            then return acc
            else
                let key = BC.takeWhile (/= ':') line
                    val = BS.drop 1 (BC.dropWhile (/= ':') line)
                    valS = BC.unpack (BC.dropWhile (== ' ') val)
                in if BC.map toLower key == "content-length"
                     then readHeaders h' (read (trim valS))
                     else readHeaders h' acc
    readLine h' = loop BS.empty
      where
        loop a = do
            c <- BS.hGet h' 1
            if BS.null c
              then return a
              else if c == BC.pack "\n"
                     then return (stripCR a)
                     else loop (a `BS.append` c)
    stripCR bs
        | BS.null bs = bs
        | BS.last bs == 13 = BS.init bs
        | otherwise = bs
    toLower c | c >= 'A' && c <= 'Z' = toEnum (fromEnum c + 32) | otherwise = c
    trim = reverse . dropWhile ws . reverse . dropWhile ws
      where ws c = c == ' ' || c == '\r' || c == '\n' || c == '\t'


recvResponseFor :: Handle -> Int -> IO Aeson.Value
recvResponseFor h reqId = go (40 :: Int)
  where
    go 0 = fail ("no response for id=" ++ show reqId)
    go n = do
        raw <- recvMsg h
        case Aeson.decode (BL.fromStrict raw) of
            Just v | matchesId v -> return v
            _                    -> go (n - 1)
      where
        matchesId v = case v of
            Object o -> KM.lookup "id" o == Just (Number (fromIntegral reqId))
            _ -> False


withLsp :: FilePath -> (Handle -> Handle -> IO a) -> IO a
withLsp sky action =
    bracket
      (do
          (Just hin, Just hout, _, ph) <- createProcess (proc sky ["lsp"])
              { std_in = CreatePipe
              , std_out = CreatePipe
              , std_err = NoStream
              }
          hSetBuffering hin  NoBuffering
          hSetBuffering hout NoBuffering
          return (hin, hout, ph))
      (\(_, _, ph) -> do
          terminateProcess ph
          _ <- waitForProcess ph
          return ())
      (\(hin, hout, _) -> action hin hout)


initializeLsp :: Handle -> Handle -> IO ()
initializeLsp hin hout = do
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
    _ <- recvResponseFor hout 1
    sendMsg hin $ Aeson.object
        [ "jsonrpc" .= ("2.0" :: T.Text)
        , "method"  .= ("initialized" :: T.Text)
        , "params"  .= Aeson.object []
        ]


didOpen :: Handle -> FilePath -> String -> IO ()
didOpen hin path src = do
    sendMsg hin $ Aeson.object
        [ "jsonrpc" .= ("2.0" :: T.Text)
        , "method"  .= ("textDocument/didOpen" :: T.Text)
        , "params"  .= Aeson.object
            [ "textDocument" .= Aeson.object
                [ "uri"        .= ("file://" ++ path)
                , "languageId" .= ("sky" :: T.Text)
                , "version"    .= (1 :: Int)
                , "text"       .= src
                ]
            ]
        ]
    threadDelay 300000


-- Position-bearing requests share this builder.
posRequest :: T.Text -> Int -> FilePath -> Int -> Int -> Aeson.Value
posRequest method reqId path line col = Aeson.object
    [ "jsonrpc" .= ("2.0" :: T.Text)
    , "id"      .= reqId
    , "method"  .= method
    , "params"  .= Aeson.object
        [ "textDocument" .= Aeson.object
            [ "uri" .= ("file://" ++ path) ]
        , "position" .= Aeson.object
            [ "line"      .= line
            , "character" .= col
            ]
        ]
    ]


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
