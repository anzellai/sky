module Sky.Build.Helpers.InProcessCompile
    ( compileInProcess
    , CompileResult(..)
    , withSilencedStdout
    ) where

-- | Tier 1 test infrastructure (task #491) — call the compiler
-- in-process from a spec instead of spawning a `sky build`
-- subprocess.
--
-- Pre-Tier-1: each Sky.Build.* spec wrote a fixture to a tempdir,
-- `withSystemTempDirectory` + `readCreateProcessWithExitCode` to
-- spawn `sky build`, then read the resulting main.go and asserted
-- on the bytes.  Each subprocess ran a full `go build` against the
-- emitted Go, generating unique generic-instance entries in the
-- shared GOCACHE.  Across 100+ fixture builds the cache ballooned
-- to 30+ GB (forced the cabal-test.sh watcher in commit
-- cdf9c6bb as a stop-gap).
--
-- This helper bypasses BOTH the subprocess fork AND the
-- `go build` invocation.  It calls `Sky.Build.Compile.compile`
-- directly: source.sky → main.go (Sky lowering only).  ZERO
-- subprocesses, ZERO GOCACHE writes, ZERO `go build` runs.
--
-- Disk footprint per call: ~MB (stdlib materialisation + main.go
-- write to tempdir).  GOCACHE footprint: ZERO.  Wall-clock per
-- call: ~1-3 s (vs 5-15 s for the subprocess pattern).
--
-- IORef safety: `Compile.compile` resets its global IORef state
-- at the start of `continueCompile` (see writeIORef calls in
-- src/Sky/Build/Compile.hs around lines 731, 738, 2697).  Multiple
-- calls from the same Haskell test process are independent.

import qualified Sky.Build.Compile as Compile
import qualified Sky.Sky.Toml as Toml
import System.Directory (createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO (hClose, stdout, openFile, IOMode (..))
import GHC.IO.Handle (hDuplicate, hDuplicateTo)
import System.IO.Temp (withSystemTempDirectory)
import Control.Exception (bracket, catch, SomeException)


-- | Outcome of an in-process compile call.
data CompileResult
    = CompileOk
        { mainGo :: String
        -- ^ Contents of the emitted sky-out/main.go (the same file
        -- the subprocess spec helpers read post-`sky build`).
        }
    | CompileErr
        { errMsg :: String
        -- ^ The compiler's error string — same format as the
        -- subprocess error path.
        }
    deriving (Show)


-- | Compile a single-module Sky fixture in-process.  The fixture
-- is written to a fresh tempdir as `src/Main.sky`, a minimal
-- sky.toml is materialised next to it, then `Compile.compile`
-- runs the full Sky lowering pipeline (parse → canonicalise →
-- HM → lower) into `sky-out/main.go`.
--
-- The function silences the compiler's `putStrLn`-style progress
-- output via stdout redirection so Hspec's test runner doesn't
-- interleave compiler chatter with spec results.
--
-- NOTE: stdlib + runtime-go materialisation still hits disk
-- (Compile.writeEmbeddedSkyStdlib + copyRuntime).  These are
-- one-time costs per tempdir, NOT per-call inside the same
-- tempdir, but each call here uses a fresh tempdir.  A future
-- refinement could thread a cached tempdir across calls in a
-- beforeAll hook for further savings.
compileInProcess :: String -> IO CompileResult
compileInProcess skySrc = withSystemTempDirectory "sky-inproc" $ \tmp -> do
    let srcDir = tmp </> "src"
        outDir = tmp </> "sky-out"
        entry  = srcDir </> "Main.sky"
        tomlSrc = unlines
            [ "name = \"tmp\""
            , "version = \"0.0.0\""
            ]
    createDirectoryIfMissing True srcDir
    createDirectoryIfMissing True outDir
    writeFile entry skySrc
    let config = Toml.parseSkyToml tomlSrc
    result <- withSilencedStdout (Compile.compile config entry outDir)
        `catch` (\e -> return (Left (show (e :: SomeException))))
    case result of
        Left err -> return (CompileErr err)
        Right _path -> do
            mainGoText <- readFile (outDir </> "main.go")
            length mainGoText `seq` return (CompileOk mainGoText)


-- | Run an IO action with stdout redirected to /dev/null.
-- Restores the original stdout on the way out (success or
-- exception). Used to hush `Compile.compile`'s progress output
-- so it doesn't pollute test runner output.
withSilencedStdout :: IO a -> IO a
withSilencedStdout action = do
    bracket
        (do
            saved <- hDuplicate stdout
            devNull <- openFile "/dev/null" WriteMode
            hDuplicateTo devNull stdout
            hClose devNull
            return saved)
        (\saved -> do
            hDuplicateTo saved stdout
            hClose saved)
        (const action)
