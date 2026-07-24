{-# LANGUAGE OverloadedStrings #-}
-- | Lightweight per-project debug log for the LSP. Writes to
-- `<projectRoot>/.skycache/lsp-error.log` so the artefact lives next
-- to `sky build`'s other outputs and is gitignored.
--
-- Used by hover / completion / index code paths to surface why a
-- request returned nil — without this, all failure modes look
-- identical from the editor's perspective ("LSP didn't say anything").
module Sky.Lsp.Diag
    ( logHoverDiag
    , logRaw
    , logRaw_uri
    , findProjectRootForFile
    ) where

import qualified Data.Text as T

import qualified System.Directory as Dir
import System.FilePath ((</>), takeDirectory)
import qualified Data.Time.Clock as Clock
import qualified Data.Time.Format as Tfmt
import System.IO (IOMode(AppendMode), hPutStrLn, hClose, openFile)
import Control.Exception (try, SomeException)


-- | Log a hover-path diagnostic. Resolves the project root from the
-- given file path by walking up until we find a sky.toml.
logHoverDiag :: FilePath -> Int -> Int -> String -> IO ()
logHoverDiag file line col msg = do
    mRoot <- try (findProjectRootForFile file) :: IO (Either SomeException FilePath)
    case mRoot of
        Right root -> logRaw root
            ("hover " ++ file ++ ":" ++ show (line + 1) ++ ":" ++
             show (col + 1) ++ " — " ++ msg)
        Left _ -> return ()


-- | Append a raw line to <projectRoot>/.skycache/lsp-error.log.
-- Silently swallows IO errors (logging must never break the LSP).
logRaw :: FilePath -> String -> IO ()
logRaw projectRoot msg = do
    e <- try inner :: IO (Either SomeException ())
    case e of
        Right () -> return ()
        Left _ -> return ()
  where
    inner = do
        let logDir = projectRoot </> ".skycache"
        Dir.createDirectoryIfMissing True logDir
        let logPath = logDir </> "lsp-error.log"
        now <- Clock.getCurrentTime
        let stamp = Tfmt.formatTime Tfmt.defaultTimeLocale
                "%Y-%m-%d %H:%M:%S" now
        h <- openFile logPath AppendMode
        hPutStrLn h ("[" ++ stamp ++ "] " ++ msg)
        hClose h


-- | Log a raw line, given a `file://` URI. Resolves the project root
-- from the file path encoded in the URI.
logRaw_uri :: T.Text -> String -> IO ()
logRaw_uri uri msg = do
    let path = T.unpack (T.replace "file://" "" uri)
    mRoot <- try (findProjectRootForFile path) :: IO (Either SomeException FilePath)
    case mRoot of
        Right root -> logRaw root msg
        Left _ -> return ()


-- | Walk parent dirs until a `sky.toml` is found. Falls back to the
-- file's parent directory if no sky.toml is reachable (e.g. one-off
-- files outside any project).
findProjectRootForFile :: FilePath -> IO FilePath
findProjectRootForFile file = go (takeDirectory file)
  where
    go d = do
        let toml = d </> "sky.toml"
        e <- Dir.doesFileExist toml
        if e
            then return d
            else
                let parent = takeDirectory d
                in if parent == d
                    then return (takeDirectory file)
                    else go parent
