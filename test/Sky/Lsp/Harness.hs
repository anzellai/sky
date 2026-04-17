{-# LANGUAGE OverloadedStrings #-}
{-# LANGUAGE ScopedTypeVariables #-}

-- | Shared test harness for `sky lsp` integration specs.
--
-- The LSP protocol speaks JSON-RPC over stdio with Content-Length
-- framing. This module bundles the framing, the subprocess
-- lifecycle, the initialize/didOpen helpers, and — new — a
-- server-pushed notification queue.
--
-- The notification queue lets specs wait for `publishDiagnostics`
-- and similar server→client messages that aren't responses to a
-- particular request id. Before this, the request-response helper
-- would discard notifications silently, so specs couldn't assert
-- anything about editor-visible diagnostics.
module Sky.Lsp.Harness
    ( -- * Subprocess
      findSky
    , withLsp

      -- * Framing
    , sendMsg
    , recvMsg

      -- * Request / response
    , recvResponseFor

      -- * Notification queue
    , awaitNotification

      -- * Lifecycle
    , initializeLsp
    , didOpen

      -- * Request builders
    , posRequest
    ) where

import qualified Data.Aeson as Aeson
import Data.Aeson ((.=), Value(..))
import qualified Data.Aeson.KeyMap as KM
import qualified Data.ByteString as BS
import qualified Data.ByteString.Lazy as BL
import qualified Data.ByteString.Char8 as BC
import qualified Data.Text as T
import System.Directory (getCurrentDirectory, doesFileExist)
import System.FilePath ((</>))
import System.IO (Handle, hClose, hFlush, hSetBuffering, BufferMode(..))
import System.Process
import Control.Concurrent (threadDelay)
import Control.Exception (bracket, SomeException, try)


-- ─── Subprocess ────────────────────────────────────────────────

findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- | Spawn `sky lsp`, hand the action the stdin/stdout handles,
-- terminate on exit (regardless of exception path).
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


-- ─── Framing ───────────────────────────────────────────────────

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


-- ─── Request / response ────────────────────────────────────────

-- | Read until we see a response with id == reqId, skipping any
-- notifications that arrive in between. Bounded (40 messages) to
-- avoid hanging on a misconfigured server.
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


-- ─── Notification queue ───────────────────────────────────────

-- | Wait for a server-pushed notification matching the given
-- method name (e.g. @textDocument/publishDiagnostics@). Returns
-- @Just value@ on match, or @Nothing@ after the bounded retry
-- budget is exhausted so specs can assert explicitly on absence.
--
-- Notifications have no @id@ field; only a @method@ and @params@.
-- This helper distinguishes them from request responses by the
-- presence of @method@.
--
-- Bounded because the server won't always push a notification —
-- a spec asserting absence must time out cleanly instead of
-- hanging the test run.
awaitNotification :: Handle -> T.Text -> IO (Maybe Aeson.Value)
awaitNotification h wantedMethod = go (40 :: Int)
  where
    go 0 = return Nothing
    go n = do
        raw <- recvMsg h
        case Aeson.decode (BL.fromStrict raw) of
            Just v | isMatching v -> return (Just v)
            _                     -> go (n - 1)
      where
        isMatching v = case v of
            Object o ->
                   KM.lookup "method" o == Just (String wantedMethod)
                && not (KM.member "id" o)
            _ -> False


-- ─── Lifecycle ─────────────────────────────────────────────────

-- | Send initialize (with a fresh reqId of 1 by convention),
-- consume the response, then send the initialized notification.
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


-- | Send a @textDocument/didOpen@ for @path@ with @src@ as its
-- text. Sleeps briefly so the server has time to build its
-- index before the caller's first follow-up request.
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


-- ─── Request builders ──────────────────────────────────────────

-- | Build a position-bearing request (hover, definition,
-- references, etc.). Shared because the shape is identical;
-- only the @method@ changes.
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
