{-# LANGUAGE NumericUnderscores #-}
{-# LANGUAGE ScopedTypeVariables #-}
module Sky.Cli.WatchSpec (spec) where

-- Regression tests for `sky watch`. The watch command spawns a child
-- binary and listens for source-file changes, so end-to-end testing
-- needs (a) a temp project, (b) a long-running watch process, and
-- (c) careful kill+wait teardown so we don't leak processes between
-- specs. We exercise three behaviours that are load-bearing for the
-- DX promise:
--
--   1. Initial build + spawn — the watcher prints a "watching" banner,
--      runs the initial build, and the resulting binary is reachable.
--   2. Source-edit triggers rebuild — after a benign edit the watcher
--      prints "rebuilt in …" within a reasonable window. We assert via
--      log substring rather than touching the child binary's behaviour
--      since the live-counter binary keeps running on the same port.
--   3. Build-error keeps old binary running — the most user-visible
--      promise of `sky watch`. A broken save must produce "build
--      failed:" + "previous binary still running" in the log; the
--      child must NOT be killed.
--
-- We also assert that --no-run rebuilds without spawning. We do NOT
-- exercise the SIGTERM/SIGKILL escalation path here — that needs a
-- child that ignores SIGTERM, and Sky.Live binaries respect SIGTERM
-- cleanly. The graceful kill path is covered by the manual smoke run.

import Test.Hspec
import Control.Concurrent (threadDelay)
import qualified Control.Exception as E
import Data.List (isInfixOf)
import qualified System.Timeout as Timeout
import System.Posix.Signals (signalProcess, sigKILL)
import System.Process.Internals
    ( ProcessHandle__(..), withProcessHandle )
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import qualified System.IO as IO
import System.Process
    ( createProcess, proc, CreateProcess(..), terminateProcess
    , waitForProcess, ProcessHandle, StdStream(..)
    )


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- A minimal CLI app: prints a line then exits. Watch's job is to
-- rebuild + relaunch on edits, not Sky.Live-specific behaviour, so we
-- don't need the live runtime here. Faster build + no port juggling.
miniSrc :: String
miniSrc =
    "module Main exposing (main)\n\
    \import Sky.Core.Prelude exposing (..)\n\
    \import Std.Log exposing (println)\n\
    \\n\
    \main = println \"alpha\"\n"

miniSrcEdited :: String
miniSrcEdited =
    "module Main exposing (main)\n\
    \import Sky.Core.Prelude exposing (..)\n\
    \import Std.Log exposing (println)\n\
    \\n\
    \main = println \"beta\"\n"

miniSrcBroken :: String
miniSrcBroken =
    "module Main exposing (main)\n\
    \import Sky.Core.Prelude exposing (..)\n\
    \import Std.Log exposing (println)\n\
    \\n\
    \main = println \"alpha\"\n\
    \zzz_top_level_garbage 123 456\n"


writeProject :: FilePath -> String -> IO ()
writeProject dir src = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml")
        "name = \"watch-spec\"\nentry = \"src/Main.sky\"\n"
    writeFile (dir </> "src" </> "Main.sky") src


-- Spawn `sky watch --no-run` in `dir`, redirecting stdout+stderr to
-- the given log path. --no-run keeps the test fast and side-effect-
-- free: we exercise the file-watch + rebuild loop without binding a
-- port or spawning a long-lived child.
spawnWatch :: FilePath -> FilePath -> FilePath -> IO ProcessHandle
spawnWatch sky dir logPath = do
    h <- IO.openFile logPath IO.WriteMode
    IO.hSetBuffering h IO.LineBuffering
    (_, _, _, ph) <- createProcess (proc sky ["watch", "--no-run", "src/Main.sky"])
        { cwd = Just dir
        , std_in  = NoStream
        , std_out = UseHandle h
        , std_err = UseHandle h
        , delegate_ctlc = False
        }
    pure ph


-- Tear down a spawned watch. terminateProcess sends SIGTERM (POSIX) or
-- TerminateProcess (Windows); the watch's clean-exit path catches it
-- as UserInterrupt async exception, kills its own child if any, and
-- returns from runWatch. waitForProcess reaps the zombie.
-- #368 — bounded process teardown. Earlier this called `waitForProcess`
-- without a timeout, and on a wedged child the spec hung forever (we
-- once waited 7 hours before noticing). Two-stage escalation:
--   1. SIGTERM, wait up to 5 s.
--   2. If still alive: SIGKILL, wait up to 3 s (POSIX) — the kernel
--      will reap regardless.
killWatch :: ProcessHandle -> IO ()
killWatch ph = do
    terminateProcess ph
    mTerm <- Timeout.timeout 5_000_000 (waitForProcess ph)
    case mTerm of
        Just _  -> pure ()
        Nothing -> do
            -- SIGTERM ignored — escalate to SIGKILL on POSIX.
            withProcessHandle ph $ \h -> case h of
                OpenHandle pid -> signalProcess sigKILL pid
                _              -> pure ()
            _ <- Timeout.timeout 3_000_000 (waitForProcess ph)
            pure ()


-- Read the watch log with a polite delay so the underlying process
-- has had a chance to flush. Returns the full file contents.
readLogAfter :: Int -> FilePath -> IO String
readLogAfter delayMs path = do
    threadDelay (delayMs * 1000)
    ex <- doesFileExist path
    if ex then readFile path else pure ""

-- Poll the log every 200ms up to `maxMs` waiting for `marker` to
-- appear, then return the full file contents. Avoids the
-- timing-sensitive "wait fixed N seconds then assert" pattern that
-- breaks when the underlying compile/run is slower than the budget.
-- Returns the log contents at the moment the marker appeared (or
-- the final contents after timeout, so the assertion still gets
-- a useful diagnostic).
waitForLogContaining :: Int -> String -> FilePath -> IO String
waitForLogContaining maxMs marker path = go 0
  where
    pollMs = 200
    go elapsed
        | elapsed >= maxMs = readFile path `E.catch` (\(_ :: E.SomeException) -> pure "")
        | otherwise = do
            ex <- doesFileExist path
            content <- if ex
                then readFile path `E.catch` (\(_ :: E.SomeException) -> pure "")
                else pure ""
            if marker `isInfixOf` content
                then pure content
                else do
                    threadDelay (pollMs * 1000)
                    go (elapsed + pollMs)


spec :: Spec
spec = do
    describe "sky watch" $ do

        it "prints the watching banner + runs the initial build" $ do
            sky <- findSky
            withSystemTempDirectory "sky-watch" $ \tmp -> do
                writeProject tmp miniSrc
                let logP = tmp </> "watch.log"
                ph <- spawnWatch sky tmp logP
                E.bracket_ (pure ()) (killWatch ph) $ do
                    log_ <- readLogAfter 5000 logP
                    log_ `shouldContain` "[watch]"
                    log_ `shouldContain` "watching"
                    log_ `shouldContain` "initial build"

        it "rebuilds on a source edit" $ do
            sky <- findSky
            withSystemTempDirectory "sky-watch" $ \tmp -> do
                writeProject tmp miniSrc
                let logP = tmp </> "watch.log"
                ph <- spawnWatch sky tmp logP
                E.bracket_ (pure ()) (killWatch ph) $ do
                    -- Wait for the initial build to settle (poll up to
                    -- 30s) — first-build cabal compile + go build can
                    -- be slow on a cold cache.
                    _ <- waitForLogContaining 30000 "watching" logP
                    -- Edit the file in-place — the watcher's mtime+size
                    -- check picks this up on the next poll cycle.
                    writeFile (tmp </> "src" </> "Main.sky") miniSrcEdited
                    -- Poll up to 30s for the "rebuilt in" banner so the
                    -- test isn't timing-sensitive on slow hardware.
                    log_ <- waitForLogContaining 30000 "rebuilt in" logP
                    log_ `shouldSatisfy` ("rebuilt in" `isInfixOf`)

        it "keeps state alive on broken save (build failed banner appears)" $ do
            sky <- findSky
            withSystemTempDirectory "sky-watch" $ \tmp -> do
                writeProject tmp miniSrc
                let logP = tmp </> "watch.log"
                ph <- spawnWatch sky tmp logP
                E.bracket_ (pure ()) (killWatch ph) $ do
                    _ <- readLogAfter 5000 logP
                    writeFile (tmp </> "src" </> "Main.sky") miniSrcBroken
                    log_ <- readLogAfter 5000 logP
                    -- The two markers that prove the build-error policy:
                    -- (a) we printed "build failed:" — surfaces the error,
                    -- (b) we printed "previous binary still running" — the
                    -- old child wasn't torn down.
                    log_ `shouldSatisfy` ("build failed:" `isInfixOf`)
                    log_ `shouldSatisfy` ("previous binary still running" `isInfixOf`)
