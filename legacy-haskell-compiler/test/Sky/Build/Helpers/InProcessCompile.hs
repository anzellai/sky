module Sky.Build.Helpers.InProcessCompile
    ( compileInProcess
    , compileInProcessMulti
    , CompileResult(..)
    , withSilencedStdout
    , withCapturedStdout
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
import System.FilePath ((</>), takeDirectory)
import System.IO (hClose, stdout, openFile, IOMode (..), hFlush)
import GHC.IO.Handle (hDuplicate, hDuplicateTo)
import System.IO.Temp (withSystemTempDirectory, withSystemTempFile)
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
        -- ^ The compiler's error string — concatenation of the
        -- structured diagnostic text that the CLI prints to
        -- stdout (TYPE ERROR / PARSE ERROR / NAMING ERROR /
        -- EXHAUSTIVENESS ERROR blocks, [E0001]…[E5999] codes,
        -- source snippets, etc.) and the `Left` returned by
        -- `Compile.compile`.  Same bytes the subprocess error path
        -- captures via stdout+stderr.
        }
    deriving (Show)


-- | Compile a single-module Sky fixture in-process.  The fixture
-- is written to a fresh tempdir as `src/Main.sky`, a minimal
-- sky.toml is materialised next to it, then `Compile.compile`
-- runs the full Sky lowering pipeline (parse → canonicalise →
-- HM → lower) into `sky-out/main.go`.
--
-- The function captures the compiler's stdout (where structured
-- diagnostics are emitted via `putStrLn`) and routes it into the
-- `errMsg` field on failure so tests can assert on the diagnostic
-- text (e.g. `out `shouldSatisfy` ("[E2001]" `isInfixOf`)`).
--
-- NOTE: stdlib + runtime-go materialisation still hits disk
-- (Compile.writeEmbeddedSkyStdlib + copyRuntime).  These are
-- one-time costs per tempdir, NOT per-call inside the same
-- tempdir, but each call here uses a fresh tempdir.  A future
-- refinement could thread a cached tempdir across calls in a
-- beforeAll hook for further savings.
compileInProcess :: String -> IO CompileResult
compileInProcess skySrc =
    compileInProcessMulti [("src/Main.sky", skySrc)]


-- | Compile a multi-file Sky project in-process.  Each tuple is
-- @(relativePath, sourceText)@ where the relative path is taken
-- from the project root (so `"src/Main.sky"`, `"src/View.sky"`,
-- `"tests/MyTest.sky"` all live alongside one another).  The
-- entry point is conventionally `"src/Main.sky"` — every spec in
-- this codebase uses that name.
--
-- The function materialises a minimal `sky.toml`, writes each
-- file (creating any necessary intermediate directories), then
-- compiles `src/Main.sky` exactly like `compileInProcess`.
--
-- Use this for specs that need dep modules (`src/State.sky` +
-- `src/View.sky` + `src/Main.sky`), test files (`tests/*.sky`
-- alongside `src/Main.sky`), or any other multi-file shape.
compileInProcessMulti :: [(FilePath, String)] -> IO CompileResult
compileInProcessMulti files = withSystemTempDirectory "sky-inproc" $ \tmp -> do
    let outDir = tmp </> "sky-out"
        entry  = tmp </> "src" </> "Main.sky"
        tomlSrc = unlines
            [ "name = \"tmp\""
            , "version = \"0.0.0\""
            , "entry = \"src/Main.sky\""
            , ""
            , "[source]"
            , "root = \"src\""
            ]
    createDirectoryIfMissing True outDir
    writeFile (tmp </> "sky.toml") tomlSrc
    -- Materialise every file, creating intermediate directories.
    mapM_ (\(relPath, content) -> do
            let fullPath = tmp </> relPath
            createDirectoryIfMissing True (takeDirectory fullPath)
            writeFile fullPath content)
        files
    let config = Toml.parseSkyToml tomlSrc
    (captured, result) <- withCapturedStdout
        (Compile.compile config entry outDir
            `catch` (\e -> return (Left (show (e :: SomeException)))))
    case result of
        Left err ->
            -- Combine: structured stdout diagnostics (TYPE ERROR
            -- block, [E2001] code, etc.) FIRST so isInfixOf scans
            -- find them; the bare `Left` summary after.
            return (CompileErr (captured ++ "\n" ++ err))
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


-- | Run an IO action with stdout redirected to a fresh temp file,
-- returning both the captured output and the action's result.
-- Restores the original stdout on the way out (success or
-- exception).
--
-- This is the variant `compileInProcessMulti` uses so that
-- structured diagnostic blocks (which `Sky.Build.Compile` writes
-- via `putStrLn`) survive into `CompileErr.errMsg`.
withCapturedStdout :: IO a -> IO (String, a)
withCapturedStdout action = do
    withSystemTempFile "sky-inproc-stdout" $ \tmpPath tmpHandle -> do
        hClose tmpHandle  -- close the handle System.IO.Temp gave us;
                          -- we'll re-open via dup2 ourselves.
        bracket
            (do
                saved <- hDuplicate stdout
                redir <- openFile tmpPath WriteMode
                hDuplicateTo redir stdout
                hClose redir
                return saved)
            (\saved -> do
                hFlush stdout
                hDuplicateTo saved stdout
                hClose saved)
            (\_ -> do
                r <- action
                hFlush stdout
                captured <- readFile tmpPath
                length captured `seq` return (captured, r))
