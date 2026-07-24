{-# LANGUAGE ScopedTypeVariables #-}
-- v0.16.5 — per-describe timing instrumentation.
--
-- Wraps Test.Hspec.describe with begin/end timestamp capture. Each
-- completed describe block appends one CSV row to the file pointed at
-- by SKY_TIMINGS_FILE (default /tmp/sky-cabal-timings.csv):
--
--     name,start_unix,end_unix,duration_seconds
--
-- The CSV is APPENDED to, never truncated — multiple test runs
-- accumulate. Truncate explicitly via `rm /tmp/sky-cabal-timings.csv`
-- between runs if you want a clean read.
--
-- Why this helper exists: hspec's default formatter doesn't print
-- per-describe wall-clock timing. Without numbers we can't make
-- informed decisions about which specs to parallelize, merge, or
-- demote to the opt-in slow tier. ONE annotated test run produces a
-- machine-readable timing log for downstream tooling.
--
-- Drop-in usage in Spec.hs:
--
--     import SkyTiming (describeT)
--     ...
--     describeT "Sky.Build.Compile" Sky.Build.CompileSpec.spec
--
-- Identical semantics to `describe` modulo the timing side-channel.

module SkyTiming
    ( describeT
    ) where

import Test.Hspec (Spec, describe, beforeAll_, afterAll_)
import Data.Time.Clock.POSIX (getPOSIXTime)
import Data.IORef (IORef, newIORef, writeIORef, readIORef, atomicModifyIORef')
import Data.Maybe (fromMaybe)
import qualified Data.Map.Strict as Map
import System.Environment (lookupEnv)
import System.IO (openFile, IOMode (AppendMode), hPutStrLn, hClose)
import System.IO.Unsafe (unsafePerformIO)
import Control.Exception (catch, SomeException)
import Text.Printf (printf)

-- Per-describe start times. Keyed by name because Hspec runs
-- beforeAll_ before the describe block begins; we read the start time
-- back in afterAll_ to compute duration. A Map (not a stack) tolerates
-- nested describeT calls — each level keys by its own unique name.
startTimes :: IORef (Map.Map String Double)
{-# NOINLINE startTimes #-}
startTimes = unsafePerformIO (newIORef Map.empty)

-- describeT — drop-in replacement for `describe` with timing.
--
-- Semantically equivalent to `describe`; the only side-effect is one
-- appended CSV line per invocation.
describeT :: String -> Spec -> Spec
describeT name body =
    beforeAll_ (recordStart name) $
    afterAll_  (recordEnd   name) $
    describe   name body

recordStart :: String -> IO ()
recordStart name = do
    now <- realToFrac <$> getPOSIXTime
    atomicModifyIORef' startTimes (\m -> (Map.insert name now m, ()))

recordEnd :: String -> IO ()
recordEnd name = (do
    endTs <- realToFrac <$> getPOSIXTime
    m <- readIORef startTimes
    case Map.lookup name m of
        Nothing -> return ()
        Just startTs -> do
            let durSecs = endTs - startTs :: Double
            file <- timingsFile
            h <- openFile file AppendMode
            hPutStrLn h (printf "%s,%.3f,%.3f,%.3f" name startTs endTs durSecs)
            hClose h)
    `catch` \(_ :: SomeException) -> return ()
    -- Never let timing instrumentation kill a test run. The catch
    -- swallows fs/permission errors silently — losing one row is fine.

timingsFile :: IO FilePath
timingsFile = fromMaybe "/tmp/sky-cabal-timings.csv" <$> lookupEnv "SKY_TIMINGS_FILE"
