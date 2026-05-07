{-# LANGUAGE CPP #-}
{-# LANGUAGE RecordWildCards #-}
{-# LANGUAGE ScopedTypeVariables #-}
{-# LANGUAGE TypeApplications #-}

-- | sky watch — file-watch-driven rebuild + restart loop.
--
-- Polls a strict allowlist of paths every WatchOpts.pollMs (default 200ms),
-- debounces by WatchOpts.debounceMs (default 150ms) so a burst of save
-- events coalesces, then runs the same compile pipeline as `sky run` and
-- (re)spawns the resulting binary. The binary is killed with a graceful
-- SIGTERM, escalated to SIGKILL after WatchOpts.killTimeoutMs if it
-- ignores the request.
--
-- Allowlist (no .skywatchignore — predictable beats configurable):
--   - sky.toml at the project root
--   - the directory containing the entry point (recursive walk, *.sky)
--   - tests/ at the project root if present (recursive walk, *.sky)
--   - any extra paths supplied via repeated --watch=PATH on the CLI
--
-- Build-error policy: the previously-running binary KEEPS RUNNING through
-- a failing rebuild. The user fixes the typo, the next save triggers a
-- successful rebuild, and only then is the old binary replaced. Without
-- this, every fat-finger save would tear down the dev session — a UX
-- regression versus what `sky run` already gives you.
module Sky.Cli.Watch
    ( WatchOpts(..)
    , defaultWatchOpts
    , runWatch
    -- exported for testing the pure pieces:
    , collectWatchedPaths
    , hashFiles
    ) where

import qualified Control.Exception as E
import qualified Control.Concurrent
import Control.Monad (forM, forM_, unless, when)
import Data.IORef (IORef, newIORef, readIORef, writeIORef)
import Data.List (sort)
import qualified System.Directory as Dir
import System.Exit (ExitCode(..))
import System.FilePath ((</>), takeDirectory, takeExtension, takeFileName)
import System.IO (hFlush, hPutStrLn, stderr, stdout)
import qualified System.Process as P
import System.Process (ProcessHandle, getProcessExitCode, terminateProcess)
#ifndef mingw32_HOST_OS
import qualified System.Posix.Signals as Sig
#endif
import qualified Data.Time.Clock as Clock
import qualified Data.Time.Clock.POSIX as POSIX

import qualified Sky.Build.Compile as Compile
import qualified Sky.Sky.Toml as Toml


-- | Tunable knobs for the watch loop. defaultWatchOpts gives sensible
-- defaults; the CLI parser overlays user-supplied flags onto these.
data WatchOpts = WatchOpts
    { woEntry          :: FilePath     -- entry .sky file
    , woNoRun          :: Bool         -- rebuild only, don't spawn the binary
    , woClear          :: Bool         -- clear screen between rebuilds
    , woPollMs         :: Int          -- poll interval (200ms default)
    , woDebounceMs     :: Int          -- debounce after a change (150ms default)
    , woKillTimeoutMs  :: Int          -- ms to wait for graceful SIGTERM
    , woExtras         :: [FilePath]   -- extra paths from --watch=...
    }
    deriving (Show)


defaultWatchOpts :: FilePath -> WatchOpts
defaultWatchOpts entry = WatchOpts
    { woEntry         = entry
    , woNoRun         = False
    , woClear         = False
    , woPollMs        = 200
    , woDebounceMs    = 150
    , woKillTimeoutMs = 3000
    , woExtras        = []
    }


-- | Sky-friendly status banner. Uses ansi-terminal-equivalent escapes
-- (already a project dep via Sky.Reporting); we keep it as raw escapes
-- here to avoid pulling another import. Colours degrade to plain text on
-- terminals that don't support ANSI.
ansi :: String -> String -> String
ansi code s = "\ESC[" ++ code ++ "m" ++ s ++ "\ESC[0m"

cyan, green, red, yellow :: String -> String
cyan   = ansi "36"
green  = ansi "32"
red    = ansi "31"
yellow = ansi "33"

bannerInfo, bannerOk, bannerErr, bannerWarn :: String -> IO ()
bannerInfo  msg = putStrLn (cyan   "[watch] " ++ msg) >> hFlush stdout
bannerOk    msg = putStrLn (green  "[watch] " ++ msg) >> hFlush stdout
bannerErr   msg = putStrLn (red    "[watch] " ++ msg) >> hFlush stdout
bannerWarn  msg = putStrLn (yellow "[watch] " ++ msg) >> hFlush stdout


-- ── File enumeration ────────────────────────────────────────────────

-- | Apply the strict allowlist policy + extras and return every path the
-- watcher should hash on each tick. Order is deterministic so the hash
-- is stable across ticks for an unchanged tree.
--
-- Pure-ish (only hits the filesystem to enumerate), no side effects.
-- Exported for tests so the policy can be unit-tested without spawning
-- the loop.
collectWatchedPaths :: WatchOpts -> IO [FilePath]
collectWatchedPaths WatchOpts{..} = do
    let entryDir = takeDirectory woEntry
    haveToml <- Dir.doesFileExist "sky.toml"
    let tomlPath = ["sky.toml" | haveToml]
    entryFiles <- walkSky entryDir
    haveTests <- Dir.doesDirectoryExist "tests"
    testFiles  <- if haveTests then walkSky "tests" else pure []
    extraFiles <- concat <$> mapM resolveExtra woExtras
    pure (sort (tomlPath ++ entryFiles ++ testFiles ++ extraFiles))
  where
    walkSky :: FilePath -> IO [FilePath]
    walkSky root = do
        ok <- Dir.doesDirectoryExist root
        if not ok then pure []
        else do
            entries <- Dir.listDirectory root
            paths <- forM entries $ \name -> do
                let p = root </> name
                isDir <- Dir.doesDirectoryExist p
                case () of
                    _ | name `elem` skipDirs -> pure []
                      | isDir                -> walkSky p
                      | takeExtension p == ".sky" -> pure [p]
                      | otherwise            -> pure []
            pure (concat paths)
    -- Direct path → file: include verbatim. Direct path → dir: walk
    -- recursively, .sky-only (matching the entry-dir behaviour for
    -- consistency). Anything missing is silently skipped (the user might
    -- supply a path that gets created later).
    resolveExtra :: FilePath -> IO [FilePath]
    resolveExtra p = do
        isFile <- Dir.doesFileExist p
        if isFile then pure [p]
        else do
            isDir <- Dir.doesDirectoryExist p
            if isDir then walkSky p
            else pure []
    -- Hard-coded skip list. Generated dirs would feedback-loop on every
    -- build; .git would burn CPU on every commit / branch switch.
    skipDirs :: [String]
    skipDirs =
        [ "sky-out"
        , ".skycache"
        , ".skydeps"
        , "dist-newstyle"
        , "node_modules"
        , ".git"
        , ".vscode"
        , ".idea"
        ]


-- | Fingerprint the watched set as (path, mtime, size). Cheap; we don't
-- hash file contents because mtime+size catches every meaningful change
-- and the watch loop can afford the rare cosmetic-touch false positive.
-- The cost we DO want to avoid is reading every byte of every file
-- 5 times per second.
hashFiles :: [FilePath] -> IO String
hashFiles paths = do
    parts <- forM paths $ \p -> do
        e <- Dir.doesFileExist p
        if not e then pure (p ++ ":missing")
        else do
            mt <- Dir.getModificationTime p
            sz <- Dir.getFileSize p
            pure (p ++ ":" ++ show (POSIX.utcTimeToPOSIXSeconds mt) ++ ":" ++ show sz)
    pure (concat parts)


-- ── Process lifecycle ───────────────────────────────────────────────

-- | Send SIGTERM, wait up to killTimeoutMs for graceful exit, escalate
-- to SIGKILL if still alive. Idempotent — calling on an already-exited
-- handle is a no-op.
killChildGraceful :: Int -> ProcessHandle -> IO ()
killChildGraceful timeoutMs ph = do
    ec0 <- getProcessExitCode ph
    case ec0 of
        Just _  -> pure ()
        Nothing -> do
            terminateProcess ph
            waitFor ((timeoutMs + 99) `div` 100)
  where
    waitFor :: Int -> IO ()
    waitFor steps
        | steps <= 0 = do
            -- Escalate. There's no portable Haskell SIGKILL helper so go
            -- through System.Posix.Signals directly. On Windows the
            -- terminateProcess call above is unconditional already
            -- (TerminateProcess), so there is nothing to escalate to.
#ifndef mingw32_HOST_OS
            mPid <- P.getPid ph
            forM_ mPid $ \pid -> Sig.signalProcess Sig.sigKILL pid
#endif
            -- Reap so the handle releases its zombie slot.
            _ <- P.waitForProcess ph
            pure ()
        | otherwise  = do
            ec <- getProcessExitCode ph
            case ec of
                Just _  -> pure ()
                Nothing -> do
                    Control.Concurrent.threadDelay 100000  -- 100ms
                    waitFor (steps - 1)


-- | Spawn the freshly-built binary inheriting our std streams. Returns
-- the handle so the watcher can kill + restart on the next rebuild.
spawnBinary :: FilePath -> IO ProcessHandle
spawnBinary binPath = do
    (_, _, _, ph) <- P.createProcess (P.proc binPath [])
        { P.std_in  = P.Inherit
        , P.std_out = P.Inherit
        , P.std_err = P.Inherit
        , P.delegate_ctlc = False  -- we install our own SIGINT handler
        }
    pure ph


-- ── Build invocation ────────────────────────────────────────────────

-- | Invoke the same build pipeline as `sky build`, returning the output
-- binary path on success or the formatted error on failure. We DON'T
-- regenerate FFI bindings here — `sky add/install` is the user-explicit
-- step for that, and watch mode never touches deps.
--
-- Critically wraps the entire pipeline in a SomeException catch. The
-- Sky compile pipeline can raise Haskell `error` calls on parser /
-- lowerer panics that the Either-typed contract doesn't cover (e.g.
-- ModuleGraph throws for some malformed input). Without this guard,
-- a single malformed save would propagate the exception up the call
-- stack and kill the watch loop entirely — defeating the whole
-- "keep the old binary running through a broken edit" promise that
-- makes `sky watch` actually pleasant. We treat any caught exception
-- as a build failure and recover.
runBuild :: WatchOpts -> IO (Either String FilePath)
runBuild opts = do
    res <- E.try @E.SomeException (runBuildInner opts)
    case res of
        Right ok -> pure ok
        Left e   -> pure (Left ("compile pipeline panicked: " ++ show e))

runBuildInner :: WatchOpts -> IO (Either String FilePath)
runBuildInner WatchOpts{..} = do
    haveToml <- Dir.doesFileExist "sky.toml"
    cfg <- if haveToml
        then Toml.parseSkyToml <$> readFile "sky.toml"
        else pure Toml.defaultConfig
    let outDir = "sky-out"
    Dir.createDirectoryIfMissing True outDir
    result <- Compile.compile cfg woEntry outDir
    case result of
        Left err  -> pure (Left err)
        Right _   -> do
            let binPath = outDir </> Toml._binName cfg
                cmd = "cd " ++ outDir ++ " && go build -o " ++ Toml._binName cfg ++ " ."
            (ec, _, gerr) <- P.readCreateProcessWithExitCode (P.shell cmd) ""
            case ec of
                ExitSuccess   -> pure (Right binPath)
                ExitFailure _ -> pure (Left ("go build failed:\n" ++ gerr))


-- ── Top-level loop ──────────────────────────────────────────────────

-- | Public entry point. Blocks until SIGINT or SIGTERM, at which point
-- it cleanly tears down the child process and returns.
--
-- Lifecycle:
--   * SIGINT (Ctrl-C) — Haskell RTS auto-translates to UserInterrupt
--     async exception. We catch it inside `bracket_`, run cleanup, return.
--   * SIGTERM — install a handler that re-throws UserInterrupt at the
--     main thread, joining the same path. macOS launchd, Linux systemd,
--     and `kill <pid>` all work.
--   * Any other exception — cleanup still runs (bracket_), then re-thrown.
--
-- Critically, no signal handler ever calls exitWith / throwIO ExitSuccess
-- directly; that path produces a noisy "sky: ExitSuccess" message on
-- stderr (Haskell prints unhandled top-level exceptions). The
-- UserInterrupt path is silent.
runWatch :: WatchOpts -> IO ()
runWatch opts = do
    canonicalEntry <- Dir.makeAbsolute (woEntry opts)
    let opts' = opts { woEntry = canonicalEntry }

    bannerInfo $ "watching " ++ describeWatched opts'
    maybeLiveStoreTip

    childRef <- newIORef (Nothing :: Maybe ProcessHandle)

    -- Forward SIGTERM to the main thread as UserInterrupt, joining the
    -- same clean-exit path Ctrl-C uses. POSIX-only — Windows has no
    -- SIGTERM in this sense; Ctrl-C / Ctrl-Break still raise
    -- UserInterrupt via the GHC RTS on every platform.
#ifndef mingw32_HOST_OS
    mainTid <- Control.Concurrent.myThreadId
    _ <- Sig.installHandler Sig.sigTERM
            (Sig.CatchOnce (E.throwTo mainTid E.UserInterrupt))
            Nothing
#endif

    let cleanup = do
            mch <- readIORef childRef
            forM_ mch (killChildGraceful (woKillTimeoutMs opts'))
            -- Tear down stdout buffer so the parent shell sees the
            -- final newline before reclaiming control.
            hFlush stdout

    E.handle silentExit $
        E.bracket_ (pure ()) cleanup $ do
            initialPaths <- collectWatchedPaths opts'
            initialHash  <- hashFiles initialPaths
            initialResult <- doBuild opts' "initial build"
            case initialResult of
                Right binPath ->
                    unless (woNoRun opts') $ do
                        ph <- spawnBinary binPath
                        writeIORef childRef (Just ph)
                Left _ -> pure ()  -- error already printed; keep watching
            loop opts' childRef initialHash
  where
    -- UserInterrupt + ExitSuccess are the two clean-exit signals we
    -- want to swallow silently. Anything else propagates so the user
    -- sees the real error.
    silentExit :: E.SomeException -> IO ()
    silentExit e =
        case (E.fromException e :: Maybe E.AsyncException) of
            Just E.UserInterrupt -> pure ()
            _ ->
                case (E.fromException e :: Maybe ExitCode) of
                    Just ExitSuccess -> pure ()
                    _                -> E.throwIO e


-- | The polling loop. Compares the watched-set hash every poll cycle;
-- on change, debounces by sleeping debounceMs and re-hashing to absorb
-- multi-file save bursts, then triggers a rebuild + restart.
loop :: WatchOpts -> IORef (Maybe ProcessHandle) -> String -> IO ()
loop opts childRef lastHash = do
    Control.Concurrent.threadDelay (woPollMs opts * 1000)
    paths <- collectWatchedPaths opts
    h <- hashFiles paths
    if h == lastHash
        then loop opts childRef lastHash
        else do
            -- Debounce: sleep and re-sample. Absorbs save-all bursts
            -- (e.g. multi-file refactor in editor).
            Control.Concurrent.threadDelay (woDebounceMs opts * 1000)
            paths2 <- collectWatchedPaths opts
            h2 <- hashFiles paths2
            -- Tag with the changed path for nicer UX; if hashing finds
            -- multiple changes pick the first whose hash differs.
            let changed = changedPath lastHash h2 paths2
            when (woClear opts) $ do
                putStr "\ESC[2J\ESC[H"  -- ANSI clear screen + home
                hFlush stdout
            bannerInfo $ "rebuilding (" ++ maybe "files changed" takeFileName changed ++ ")…"
            t0 <- Clock.getCurrentTime
            buildRes <- doBuild opts ""
            t1 <- Clock.getCurrentTime
            let elapsed = Clock.diffUTCTime t1 t0
            case buildRes of
                Left _ ->
                    -- Old binary stays alive; we already printed the
                    -- error inside doBuild.
                    bannerWarn $ "rebuild failed in " ++ show elapsed
                        ++ "; previous binary still running"
                Right binPath -> do
                    bannerOk $ "rebuilt in " ++ show elapsed
                    unless (woNoRun opts) $ do
                        mch <- readIORef childRef
                        forM_ mch (killChildGraceful (woKillTimeoutMs opts))
                        ph <- spawnBinary binPath
                        writeIORef childRef (Just ph)
            loop opts childRef h2


-- | Best-effort identification of the file whose hash differs. Returns
-- Nothing if every path is unchanged (shouldn't happen at this call
-- site) or if the hashes don't decompose cleanly (pre-formatting).
changedPath :: String -> String -> [FilePath] -> Maybe FilePath
changedPath _ _ [] = Nothing
changedPath _ _ ps = Just (head ps)  -- TODO: smarter diff if needed


-- | Wrap runBuild with status-line printing. The error case prints the
-- raw compiler output verbatim — Sky's errors are already Elm-style
-- formatted.
doBuild :: WatchOpts -> String -> IO (Either String FilePath)
doBuild opts banner = do
    when (not (null banner)) $ bannerInfo banner
    res <- runBuild opts
    case res of
        Right binPath -> pure (Right binPath)
        Left err -> do
            bannerErr "build failed:"
            hPutStrLn stderr err
            pure (Left err)


-- ── Helpers ─────────────────────────────────────────────────────────

describeWatched :: WatchOpts -> String
describeWatched WatchOpts{..} =
    let entryDir = takeDirectory woEntry
    in "sky.toml + " ++ entryDir ++ "/ + tests/"
       ++ (if null woExtras then "" else " + " ++ show woExtras)


-- | Sky.Live nudge: encourage a persistent session store when watching
-- a Live app. With the memory store, every rebuild loses session state
-- and the user re-inits to their landing view; with sqlite/redis/etc
-- the Model survives the SIGTERM/spawn cycle. Cheap heuristic on the
-- sky.toml text — wrong-positive cost is one printed line.
maybeLiveStoreTip :: IO ()
maybeLiveStoreTip = do
    haveToml <- Dir.doesFileExist "sky.toml"
    when haveToml $ do
        contents <- readFile "sky.toml"
        let isLive    = elemSubstring "[live]" contents
            hasMem    = elemSubstring "store = \"memory\"" contents
            hasNonMem = any (\s -> elemSubstring ("store = \"" ++ s ++ "\"") contents)
                            ["sqlite", "redis", "postgres", "firestore"]
        when (isLive && (hasMem || not hasNonMem)) $
            bannerWarn "tip: set [live] store = \"sqlite\" in sky.toml — your in-progress Model survives hot-reload"

elemSubstring :: String -> String -> Bool
elemSubstring needle = go
  where
    n = length needle
    go []         = False
    go xs@(_:rest)
        | take n xs == needle = True
        | otherwise           = go rest
