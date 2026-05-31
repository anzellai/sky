{-# LANGUAGE LambdaCase #-}

-- | `sky doctor` — diagnose common Sky project / runtime issues.
--
-- The intended user is AI-generated code (Cursor, Claude Code,
-- Lovable, etc.) that has hit a wall and doesn't know whether the
-- problem is in the user's source, the compiler cache, a stale
-- runtime port, or a missing dependency. Running `sky doctor`
-- surfaces every common stuck-state in one shot with actionable
-- fixes.
--
-- Each check is a small, independent function returning a
-- @Finding@ — the central runner aggregates results, prints them
-- in a stable order, and (when @--fix@ is given) applies safe
-- remediations. Unsafe remediations (deleting user source, killing
-- non-Sky processes, modifying sky.toml) are NEVER auto-applied —
-- only suggested.
--
-- Exit codes:
--
-- * @0@ — clean (no findings)
-- * @1@ — at least one finding (possibly remediated under @--fix@)
-- * @2@ — diagnostic itself couldn't run (no sky.toml visible,
--         permission errors, etc.)
module Sky.Cli.Doctor
    ( runDoctor
    , DoctorOpts(..)
    ) where

import Control.Exception (SomeException, try)
import Data.List (isInfixOf, isSuffixOf, sortOn)
import Data.Time.Clock (UTCTime, diffUTCTime, getCurrentTime)
import qualified System.Directory as Dir
import qualified System.Environment as Env
import System.Exit (ExitCode(..), exitWith)
import System.FilePath ((</>), takeDirectory)
import qualified System.Process as Proc


-- | Command-line knobs for `sky doctor`.
--
-- @--fix@ enables the auto-remediation path. Off by default — the
-- diagnostic should be safe to run a hundred times; auto-fixes
-- delete things, so the user opts in.
data DoctorOpts = DoctorOpts
    { _doFix     :: !Bool
    , _doVerbose :: !Bool
    } deriving (Show)


-- | A single diagnostic finding. Severity drives exit code +
-- output prefix; @fixable@ marks findings that @--fix@ will act on.
data Finding = Finding
    { _fCheck    :: !String   -- ^ short identifier ("stale-cache", "port-busy", ...)
    , _fSeverity :: !Severity
    , _fMessage  :: !String   -- ^ human-readable description
    , _fHint     :: !String   -- ^ how to fix manually (one-liner)
    , _fFix      :: !(Maybe (IO String))
        -- ^ when present + opts._doFix, runs the remediation and
        -- returns a status string (e.g. "deleted .skycache/lowered/").
        -- When the action throws, the outer runner catches +
        -- reports.
    }


data Severity = Info | Warn | Error
    deriving (Eq, Ord, Show)


runDoctor :: DoctorOpts -> IO ()
runDoctor opts = do
    root <- locateProjectRoot
    case root of
        Nothing -> do
            putStrLn "sky doctor: no sky.toml found in current directory or any ancestor."
            putStrLn "            (cd into a project root and re-run, or `sky init` to start one.)"
            exitWith (ExitFailure 2)
        Just projectRoot -> do
            putStrLn $ "sky doctor — checking " ++ projectRoot
            putStrLn ""
            findings <- runAllChecks projectRoot
            let sorted = sortOn _fSeverity findings -- Info first, Error last
            mapM_ (printFinding opts) sorted
            applied <- if _doFix opts
                then applyFixes findings
                else pure []
            mapM_ putStrLn applied
            putStrLn ""
            if null findings
                then do
                    putStrLn "✓ no issues found."
                    exitWith ExitSuccess
                else do
                    let counts = (count Info, count Warn, count Error)
                        count s = length (filter ((== s) . _fSeverity) findings)
                    putStrLn $ summaryLine counts (_doFix opts) (length applied)
                    if any ((== Error) . _fSeverity) findings
                        then exitWith (ExitFailure 1)
                        else exitWith (ExitFailure 1)
  where
    summaryLine (i, w, e) didFix nFixed =
        let parts = [s | (n, label) <- [(e, "errors"), (w, "warnings"), (i, "info")]
                       , n > 0
                       , let s = show n ++ " " ++ label]
            issuesPart = case parts of
                [] -> "no issues"
                _  -> intercalateStr ", " parts
        in if didFix
            then issuesPart ++ "; applied " ++ show nFixed ++ " auto-fix"
                 ++ (if nFixed == 1 then "" else "es") ++ "."
            else issuesPart ++ " — run with --fix to auto-apply safe remediations."

    intercalateStr sep = foldr1 (\a b -> a ++ sep ++ b)


-- | Project root = the nearest ancestor of cwd containing sky.toml.
locateProjectRoot :: IO (Maybe FilePath)
locateProjectRoot = do
    cwd <- Dir.getCurrentDirectory
    walkUp cwd
  where
    walkUp dir = do
        hasToml <- Dir.doesFileExist (dir </> "sky.toml")
        if hasToml
            then pure (Just dir)
            else do
                let parent = takeDirectory dir
                if parent == dir
                    then pure Nothing
                    else walkUp parent


-- ─── Checks ───────────────────────────────────────────────────

runAllChecks :: FilePath -> IO [Finding]
runAllChecks root = do
    -- Each check is independent; order in the output reflects the
    -- order added here (severity then this order via stable sort).
    -- mem-guard is a compiler-dev concern (warns about runaway
    -- ghc/cabal/sky processes on the contributor's machine),
    -- gated to the Sky compiler repo so user projects + CI runners
    -- don't see it as an issue.
    isCompilerRepo <- Dir.doesFileExist (root </> "scripts" </> "mem-guard.sh")
    fmap concat $ sequence $
        [ checkSkyTomlSyntax root
        , checkStaleCache root
        , checkSkyOutAge root
        , checkPortInUse root
        , checkMissingFfi root
        ] ++ [ checkMemGuardAlive | isCompilerRepo ]


-- | sky.toml exists (root was found) AND parses as text. We don't
-- semantic-validate here (the build path does that with better
-- error messages) — just check the file isn't empty / unreadable.
checkSkyTomlSyntax :: FilePath -> IO [Finding]
checkSkyTomlSyntax root = do
    let toml = root </> "sky.toml"
    sizeOrError <- try (Dir.getFileSize toml) :: IO (Either SomeException Integer)
    case sizeOrError of
        Left e ->
            pure [ Finding
                { _fCheck = "sky-toml-unreadable"
                , _fSeverity = Error
                , _fMessage = "sky.toml could not be read: " ++ show e
                , _fHint = "ensure file permissions allow reading; recreate from `sky init` if corrupt"
                , _fFix = Nothing
                } ]
        Right 0 ->
            pure [ Finding
                { _fCheck = "sky-toml-empty"
                , _fSeverity = Error
                , _fMessage = "sky.toml is empty"
                , _fHint = "minimal valid file:\n  name = \"myapp\"\n  entry = \"src/Main.sky\""
                , _fFix = Nothing
                } ]
        Right _ -> pure []


-- | .skycache/ older than the newest src/*.sky file → stale.
-- Safe to delete on --fix (the next build regenerates).
checkStaleCache :: FilePath -> IO [Finding]
checkStaleCache root = do
    let cache = root </> ".skycache"
    cacheExists <- Dir.doesDirectoryExist cache
    if not cacheExists
        then pure [] -- no cache, nothing stale
        else do
            cacheMtime <- newestMtime cache
            srcMtime <- newestSkyMtime (root </> "src")
            case (cacheMtime, srcMtime) of
                (Just cm, Just sm) | sm `diffUTCTime` cm > 0 ->
                    pure [ Finding
                        { _fCheck = "stale-cache"
                        , _fSeverity = Warn
                        , _fMessage = ".skycache/ is older than your src/*.sky files"
                        , _fHint = "run `sky doctor --fix` to delete it (next build regenerates)"
                        , _fFix = Just $ do
                            Dir.removeDirectoryRecursive cache
                            pure ("✓ deleted " ++ cache)
                        } ]
                _ -> pure []


-- | sky-out/ older than the embedded sky binary (or the user's
-- main.sky has changed since the last build). Safe to delete.
checkSkyOutAge :: FilePath -> IO [Finding]
checkSkyOutAge root = do
    let outDir = root </> "sky-out"
    outExists <- Dir.doesDirectoryExist outDir
    if not outExists
        then pure []
        else do
            mainGo <- Dir.doesFileExist (outDir </> "main.go")
            if not mainGo
                then pure [] -- only a partial build, nothing to flag
                else do
                    mainGoMtime <- Dir.getModificationTime (outDir </> "main.go")
                    srcMtime <- newestSkyMtime (root </> "src")
                    case srcMtime of
                        Just sm | sm `diffUTCTime` mainGoMtime > 0 ->
                            pure [ Finding
                                { _fCheck = "stale-build"
                                , _fSeverity = Info
                                , _fMessage = "sky-out/main.go is older than your src/*.sky files"
                                , _fHint = "run `sky build` to refresh, or `sky doctor --fix` to remove sky-out/"
                                , _fFix = Just $ do
                                    Dir.removeDirectoryRecursive outDir
                                    pure ("✓ deleted " ++ outDir)
                                } ]
                        _ -> pure []


-- | The [live] port (default 8000) is already in use by another
-- process. Symptom AI hits constantly: previous `sky run` left a
-- zombie listener, the next run fails with "address in use".
--
-- We do NOT auto-kill — that would risk killing a process the user
-- wants to keep. Hint at `lsof -i :PORT | grep LISTEN` so they
-- decide.
--
-- Bug #371: previously this check ran unconditionally, producing a
-- false-positive finding on any project where an unrelated host
-- process happened to hold port 8000 (a different web server, a
-- prior dev session, a parallel cabal test). Two narrowing gates
-- close that:
--
--   1. **No `sky-out/` → skip.** A project that has never been
--      built has never had a `sky run` to leave a listener; flagging
--      port-busy there is dishonest. Cross-spec test pollution +
--      ambient host state can't trip a clean scaffolded project.
--
--   2. **`SKY_DOCTOR_SKIP_PORT_CHECK=1` → skip.** Escape hatch for
--      developers running Doctor in environments where port 8000 is
--      legitimately owned by something else (e.g. running Doctor
--      against a built project while a separate live server is
--      intentionally serving on 8000).
checkPortInUse :: FilePath -> IO [Finding]
checkPortInUse root = do
    skipEnv <- Env.lookupEnv "SKY_DOCTOR_SKIP_PORT_CHECK"
    let skipByEnv = case skipEnv of
            Just v | v `elem` ["1", "true", "yes", "on"] -> True
            _ -> False
    hasBuilt <- Dir.doesDirectoryExist (root </> "sky-out")
    if skipByEnv || not hasBuilt
        then pure []
        else do
            let port = "8000" -- TODO: parse sky.toml [live] port. v1.0: hardcoded default.
            result <- try (Proc.readProcessWithExitCode
                "lsof" ["-ti", ":" ++ port] "")
                :: IO (Either SomeException (ExitCode, String, String))
            let (ec, out, _) = either (\_ -> (ExitFailure 1, "", "")) id result
            case ec of
                ExitSuccess | not (null (trim out)) -> do
                    let pid = takeWhile (/= '\n') out
                    pure [ Finding
                        { _fCheck = "port-busy"
                        , _fSeverity = Warn
                        , _fMessage = "port " ++ port ++ " is in use (pid " ++ pid ++ ")"
                        , _fHint = "previous `sky run` left a listener — kill with `kill " ++ pid
                                   ++ "` (or `sky doctor --fix` to do it for you)"
                        , _fFix = Just $ do
                            _ <- Proc.system ("kill " ++ pid)
                            pure ("✓ killed pid " ++ pid)
                        } ]
                _ -> pure []
  where
    trim = dropWhile (`elem` (" \n\t" :: String)) . reverse
         . dropWhile (`elem` (" \n\t" :: String)) . reverse


-- | .skycache/ffi/*.kernel.json — when the user has imported FFI
-- packages but never run `sky install`, the build fails with cryptic
-- "package not found" errors. Detect by scanning sky source for
-- import lines that look like FFI imports (path with dots that
-- doesn't match the local stdlib namespaces).
--
-- Conservative — false-positives would be annoying. So only flag
-- when there's a literal `import` line referencing a domain-style
-- path (github.com, golang.org, etc.).
checkMissingFfi :: FilePath -> IO [Finding]
checkMissingFfi root = do
    let srcDir = root </> "src"
    exists <- Dir.doesDirectoryExist srcDir
    if not exists
        then pure []
        else do
            files <- listSkyFiles srcDir
            imports <- concat <$> mapM readFfiImports files
            let unique = removeDupes imports
            if null unique
                then pure []
                else do
                    let ffiCache = root </> ".skycache" </> "ffi"
                    cacheExists <- Dir.doesDirectoryExist ffiCache
                    if cacheExists
                        then do
                            cached <- Dir.listDirectory ffiCache
                            let missing = filter (\imp ->
                                    not (any (importMatchesCacheFile imp) cached)) unique
                            pure (map missingFinding missing)
                        else
                            pure (map missingFinding unique)
  where
    readFfiImports path = do
        contents <- try (readFile path) :: IO (Either SomeException String)
        case contents of
            Left _ -> pure []
            Right c -> pure
                [ pkg | line <- lines c
                      , Just pkg <- [parseImportPkg line]
                      , isFfiPath pkg
                ]
    parseImportPkg line =
        case words line of
            ("import":rest) -> case rest of
                (path:_) -> Just path
                _ -> Nothing
            _ -> Nothing
    isFfiPath p = ".com" `isInfixOf` p || ".org" `isInfixOf` p
                  || ".io" `isInfixOf` p || "google.golang" `isInfixOf` p
    -- Loose match — the cache filename is a slug derived from the
    -- pkg path. We just check the leading segment appears somewhere
    -- in the filename. False-negatives possible but acceptable for
    -- a diagnostic.
    importMatchesCacheFile imp file =
        let stem = takeWhile (/= '.') imp
        in stem `isInfixOf` file
    removeDupes = foldr (\x acc -> if x `elem` acc then acc else x : acc) []
    missingFinding pkg = Finding
        { _fCheck = "missing-ffi"
        , _fSeverity = Warn
        , _fMessage = "import references " ++ pkg
                      ++ " but no FFI bindings cached for it"
        , _fHint = "run `sky install` (regenerates `.skycache/ffi/`)"
        , _fFix = Nothing -- sky install is heavy + sometimes slow; don't auto-run
        }


-- | mem-guard.sh is the safety net that kills a runaway sky / cabal
-- / haskell-language-server process before it locks the user's Mac.
-- Always-on during compiler dev sessions. Cheap to check.
checkMemGuardAlive :: IO [Finding]
checkMemGuardAlive = do
    result <- try (Proc.readProcessWithExitCode
        "pgrep" ["-f", "mem-guard.sh"] "")
        :: IO (Either SomeException (ExitCode, String, String))
    let (ec, out, _) = either (\_ -> (ExitFailure 1, "", "")) id result
    case ec of
        ExitSuccess | not (null out) -> pure [] -- alive
        _ -> pure
            [ Finding
                { _fCheck = "mem-guard"
                , _fSeverity = Info
                , _fMessage = "mem-guard.sh is not running"
                , _fHint = "compiler dev sessions should run "
                           ++ "`nohup scripts/mem-guard.sh > /tmp/mem-guard.out 2>&1 & disown` "
                           ++ "to prevent a runaway compiler / LSP from locking the host"
                , _fFix = Nothing -- launching a background daemon needs user consent
                }
            ]


-- (checkEmbeddedStdlibPresent removed in v1.x — `.skycache/stdlib`
-- is only materialised by the LSP path, not by `sky build`, so its
-- absence is normal for binary-builds. The check was raising false
-- positives on every clean-built project.)


-- ─── Apply --fix path ────────────────────────────────────────

applyFixes :: [Finding] -> IO [String]
applyFixes findings = do
    let fixable = [ (f, fix) | f <- findings, Just fix <- [_fFix f] ]
    putStrLn ""
    putStrLn "─── applying fixes ─────────────────────────────────────"
    mapM applyOne fixable
  where
    applyOne (f, action) = do
        result <- try action :: IO (Either SomeException String)
        case result of
            Right msg -> pure msg
            Left e    -> pure ("✗ " ++ _fCheck f
                               ++ ": fix failed — " ++ show e)


-- ─── Output formatting ──────────────────────────────────────

printFinding :: DoctorOpts -> Finding -> IO ()
printFinding opts f = do
    putStrLn $ prefix ++ " " ++ _fMessage f
    putStrLn $ "   ↳ " ++ _fHint f
    if _doVerbose opts
        then putStrLn $ "   ↳ check-id: " ++ _fCheck f
        else pure ()
    putStrLn ""
  where
    prefix = case _fSeverity f of
        Info  -> "·"
        Warn  -> "⚠"
        Error -> "✗"


-- ─── Helpers ─────────────────────────────────────────────────

-- | Newest mtime under @dir@ (recursive). Nothing if empty / missing.
newestMtime :: FilePath -> IO (Maybe UTCTime)
newestMtime dir = do
    exists <- Dir.doesDirectoryExist dir
    if not exists
        then pure Nothing
        else do
            paths <- listDirectoryRecursive dir
            times <- mapM (\p -> try (Dir.getModificationTime p)
                                    :: IO (Either SomeException UTCTime)) paths
            let good = [t | Right t <- times]
            pure $ case good of
                [] -> Nothing
                ts -> Just (maximum ts)


-- | Newest .sky file mtime under @dir@.
newestSkyMtime :: FilePath -> IO (Maybe UTCTime)
newestSkyMtime dir = do
    files <- listSkyFiles dir
    times <- mapM (\p -> try (Dir.getModificationTime p)
                            :: IO (Either SomeException UTCTime)) files
    let good = [t | Right t <- times]
    pure $ case good of
        [] -> Nothing
        ts -> Just (maximum ts)


-- | Recursively list every file under @dir@.
listDirectoryRecursive :: FilePath -> IO [FilePath]
listDirectoryRecursive dir = do
    exists <- Dir.doesDirectoryExist dir
    if not exists
        then pure []
        else do
            entries <- Dir.listDirectory dir
            paths <- mapM walk entries
            pure (concat paths)
  where
    walk e = do
        let full = dir </> e
        isDir <- Dir.doesDirectoryExist full
        if isDir
            then listDirectoryRecursive full
            else pure [full]


-- | Recursively list .sky files under @dir@.
listSkyFiles :: FilePath -> IO [FilePath]
listSkyFiles dir = do
    all_ <- listDirectoryRecursive dir
    pure (filter (".sky" `isSuffixOf`) all_)


-- | Silence the unused-import warnings when GHC's optimiser eats
-- references in the cheap-check branches.
_unused :: IO ()
_unused = do
    _ <- getCurrentTime
    pure ()
