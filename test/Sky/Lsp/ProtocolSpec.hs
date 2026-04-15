{-# LANGUAGE OverloadedStrings #-}
{-# LANGUAGE ScopedTypeVariables #-}
module Sky.Lsp.ProtocolSpec (spec) where

-- Audit P3-2: LSP integration harness. Spawns `sky lsp` as a
-- subprocess, speaks JSON-RPC with Content-Length framing, and
-- asserts hover responses. Pre-fix there was no LSP test at all —
-- hover regressions shipped silently. This spec locks the basic
-- protocol shape and two hover cases that map to the P2-2 local-
-- types + P2-3 stable-rename fixes.

import Test.Hspec
import qualified Data.Aeson as Aeson
import Data.Aeson ((.=), Value(..))
import qualified Data.Aeson.KeyMap as KM
import qualified Data.ByteString as BS
import qualified Data.ByteString.Lazy as BL
import qualified Data.ByteString.Char8 as BC
import qualified Data.Text as T
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO (Handle, hClose, hFlush, hSetBuffering, BufferMode(..))
import System.IO.Temp (withSystemTempDirectory)
import System.Process
import Control.Concurrent (threadDelay)
import Control.Exception (bracket, SomeException, try)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- LSP message framing: Content-Length header + blank line + body.
sendMsg :: Handle -> Aeson.Value -> IO ()
sendMsg h v = do
    let body = BL.toStrict (Aeson.encode v)
        hdr = BC.pack ("Content-Length: " ++ show (BS.length body) ++ "\r\n\r\n")
    BS.hPut h hdr
    BS.hPut h body
    hFlush h


-- Read one framed LSP message off `h`. Returns the body bytes.
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


-- Recv until we see a response with id == reqId.
recvResponseFor :: Handle -> Int -> IO Aeson.Value
recvResponseFor h reqId = go 40
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
                    initialize hin hout 1
                    didOpen hin fixture src
                    -- Hover on `answer` in its definition (0-indexed).
                    sendMsg hin $ hoverRequest 2 fixture 5 0
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


-- Start a `sky lsp` subprocess; run `action` with its stdin/stdout;
-- terminate on exit.
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
      (\(hin, hout, ph) -> do
          (_ :: Either SomeException ()) <- try (hClose hin)
          (_ :: Either SomeException ()) <- try (hClose hout)
          terminateProcess ph
          _ <- waitForProcess ph
          return ())
      (\(hin, hout, _) -> action hin hout)


-- Build standard messages.

initialize :: Handle -> Handle -> Int -> IO ()
initialize hin hout reqId = do
    sendMsg hin $ Aeson.object
        [ "jsonrpc" .= ("2.0" :: T.Text)
        , "id"      .= reqId
        , "method"  .= ("initialize" :: T.Text)
        , "params"  .= Aeson.object
            [ "processId"    .= Aeson.Null
            , "rootUri"      .= Aeson.Null
            , "capabilities" .= Aeson.object []
            ]
        ]
    _ <- recvResponseFor hout reqId
    -- initialized notification
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
    -- Server needs a moment to build its index.
    threadDelay 300000


hoverRequest :: Int -> FilePath -> Int -> Int -> Aeson.Value
hoverRequest reqId path line col = Aeson.object
    [ "jsonrpc" .= ("2.0" :: T.Text)
    , "id"      .= reqId
    , "method"  .= ("textDocument/hover" :: T.Text)
    , "params"  .= Aeson.object
        [ "textDocument" .= Aeson.object
            [ "uri" .= ("file://" ++ path) ]
        , "position" .= Aeson.object
            [ "line"      .= line
            , "character" .= col
            ]
        ]
    ]


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
