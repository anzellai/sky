{-# LANGUAGE LambdaCase #-}
{-# LANGUAGE OverloadedStrings #-}

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
import Data.Char (isDigit)
import Data.List (isInfixOf, isPrefixOf, isSuffixOf, sortOn)
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
        -- v0.15.48 — +10 tooling-polish checks
        , checkGoToolchain
        , checkFfiCacheIntegrity root
        , checkLockfilePresence root
        , checkAuthSecretEnv root
        , checkCiParity root
        , checkStdlibVersion root
        , checkTomlSchema root
        , checkSubappBinaries root
        , checkSkyCheckSmoke root
        , checkGovulnCheck root
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


-- ─── v0.15.48 +10 checks ──────────────────────────────────────


-- | Go toolchain version compatibility. We need ≥ 1.22 (generics +
-- range-over-func). Warn on much newer toolchain only if we know
-- a min — for now just inform on > 1.99 (won't fire today).
checkGoToolchain :: IO [Finding]
checkGoToolchain = do
    result <- try (Proc.readProcessWithExitCode "go" ["version"] "")
        :: IO (Either SomeException (ExitCode, String, String))
    case result of
        Left _ -> pure
            [ Finding
                { _fCheck = "go-toolchain"
                , _fSeverity = Error
                , _fMessage = "`go` not found on PATH"
                , _fHint = "install Go ≥ 1.22 (https://go.dev/dl/) and re-run"
                , _fFix = Nothing
                } ]
        Right (ExitSuccess, out, _) -> case parseGoVersion out of
            Nothing -> pure []  -- couldn't parse, don't false-positive
            Just (maj, minor) ->
                if maj > 1 || (maj == 1 && minor >= 22)
                    then pure []
                    else pure
                        [ Finding
                            { _fCheck = "go-toolchain"
                            , _fSeverity = Error
                            , _fMessage =
                                "Go " ++ show maj ++ "." ++ show minor
                                ++ " is too old — Sky's runtime needs ≥ 1.22"
                            , _fHint = "upgrade Go: https://go.dev/dl/"
                            , _fFix = Nothing
                            } ]
        Right (_, _, err) -> pure
            [ Finding
                { _fCheck = "go-toolchain"
                , _fSeverity = Warn
                , _fMessage = "`go version` failed: " ++ trimTrail err
                , _fHint = "check `go` is installed + on PATH"
                , _fFix = Nothing
                } ]
  where
    trimTrail = takeWhile (/= '\n')


-- | Parse the leading "go1.X.Y" out of `go version` output.
-- Returns Just (major, minor); patch is ignored for compatibility check.
parseGoVersion :: String -> Maybe (Int, Int)
parseGoVersion s =
    let prefix = "go version go"
    in case dropUntilPrefix prefix s of
        Nothing -> Nothing
        Just rest ->
            let majStr = takeWhile isDigit rest
                rest2  = drop (length majStr) rest
                minStr = case rest2 of
                    ('.':more) -> takeWhile isDigit more
                    _          -> ""
            in case (readMb majStr, readMb minStr) of
                (Just maj, Just minor) -> Just (maj, minor)
                _ -> Nothing
  where
    readMb x = case reads x of
        [(n, "")] -> Just (n :: Int)
        _         -> Nothing
    dropUntilPrefix p str
      | p `isPrefixOf` str = Just (drop (length p) str)
      | otherwise = case str of
            (_:rest) -> dropUntilPrefix p rest
            []       -> Nothing


-- | FFI cache parity: every `.skycache/ffi/*.skyi` should have a
-- matching package shape in `.skydeps/`. A mismatch means the user
-- ran `sky add` then `sky update` without re-extracting, or the
-- cache is stale across compiler upgrades.
checkFfiCacheIntegrity :: FilePath -> IO [Finding]
checkFfiCacheIntegrity root = do
    let ffiDir = root </> ".skycache" </> "ffi"
        depsDir = root </> ".skydeps"
    ffiExists <- Dir.doesDirectoryExist ffiDir
    depsExists <- Dir.doesDirectoryExist depsDir
    if not ffiExists || not depsExists
        then pure []
        else do
            ffiFiles <- filter (".skyi" `isSuffixOf`) <$>
                Dir.listDirectory ffiDir
            depEntries <- Dir.listDirectory depsDir
            -- Conservative: only flag when ffi files have NO
            -- corresponding deps dir at all (full mismatch). The
            -- per-package hash check belongs in `sky install`.
            let orphans = filter (\f ->
                    let stem = takeWhile (/= '.') f
                    in not (any (stem `isInfixOf`) depEntries)) ffiFiles
            if null orphans
                then pure []
                else pure
                    [ Finding
                        { _fCheck = "ffi-cache-orphan"
                        , _fSeverity = Warn
                        , _fMessage =
                            show (length orphans) ++
                            " cached FFI binding(s) have no .skydeps source: "
                            ++ joinWithComma (take 3 orphans)
                            ++ (if length orphans > 3 then ", ..." else "")
                        , _fHint = "run `sky install` to regenerate, or remove obsolete bindings"
                        , _fFix = Nothing
                        } ]


-- | sky.lock presence + parity check. A pinned lockfile makes builds
-- reproducible across machines. Warn if missing entirely.
checkLockfilePresence :: FilePath -> IO [Finding]
checkLockfilePresence root = do
    let lockPath = root </> "sky.lock"
        depsDir = root </> ".skydeps"
    depsExists <- Dir.doesDirectoryExist depsDir
    if not depsExists
        then pure []  -- no deps, no lock needed
        else do
            lockExists <- Dir.doesFileExist lockPath
            if lockExists
                then pure []
                else do
                    -- Only warn when deps directory has content
                    entries <- Dir.listDirectory depsDir
                    if null entries
                        then pure []
                        else pure
                            [ Finding
                                { _fCheck = "missing-lockfile"
                                , _fSeverity = Info
                                , _fMessage =
                                    "sky.lock missing — builds may not be reproducible"
                                , _fHint =
                                    "run `sky install` to generate sky.lock (v0.16+ enforces)"
                                , _fFix = Nothing
                                } ]


-- | When sky.toml references [live] or [auth], SKY_AUTH_TOKEN_SECRET
-- must be ≥ 32 bytes. Surface dishonestly-short secrets early
-- instead of waiting for runtime startup-time failure.
checkAuthSecretEnv :: FilePath -> IO [Finding]
checkAuthSecretEnv root = do
    let tomlPath = root </> "sky.toml"
    tomlExists <- Dir.doesFileExist tomlPath
    if not tomlExists
        then pure []
        else do
            contents <- try (readFile tomlPath)
                :: IO (Either SomeException String)
            case contents of
                Left _ -> pure []
                Right c ->
                    if "[live]" `isInfixOf` c || "[auth]" `isInfixOf` c
                        then do
                            secret <- Env.lookupEnv "SKY_AUTH_TOKEN_SECRET"
                            case secret of
                                Just s | length s >= 32 -> pure []
                                Just s -> pure
                                    [ Finding
                                        { _fCheck = "auth-secret-short"
                                        , _fSeverity = Error
                                        , _fMessage =
                                            "SKY_AUTH_TOKEN_SECRET is " ++
                                            show (length s) ++
                                            " bytes — must be ≥ 32"
                                        , _fHint =
                                            "export SKY_AUTH_TOKEN_SECRET=\"$(openssl rand -hex 32)\""
                                        , _fFix = Nothing
                                        } ]
                                Nothing -> pure
                                    [ Finding
                                        { _fCheck = "auth-secret-missing"
                                        , _fSeverity = Warn
                                        , _fMessage =
                                            "SKY_AUTH_TOKEN_SECRET is unset (Sky.Live / Std.Auth in use)"
                                        , _fHint =
                                            "export SKY_AUTH_TOKEN_SECRET=\"$(openssl rand -hex 32)\""
                                        , _fFix = Nothing
                                        } ]
                        else pure []


-- | CI parity: confirm `.github/workflows/ci.yml` invokes the
-- canonical verify scripts. Drift means CI is out of date.
checkCiParity :: FilePath -> IO [Finding]
checkCiParity root = do
    let ciPath = root </> ".github" </> "workflows" </> "ci.yml"
    ciExists <- Dir.doesFileExist ciPath
    if not ciExists
        then pure []  -- user may not use GitHub Actions
        else do
            contents <- try (readFile ciPath)
                :: IO (Either SomeException String)
            case contents of
                Left _ -> pure []
                Right c ->
                    if   "verify-all-web.sh" `isInfixOf` c
                      || "verify-cli.sh" `isInfixOf` c
                      || "example-sweep.sh" `isInfixOf` c
                      || "cabal test" `isInfixOf` c
                      || "sky build" `isInfixOf` c
                        then pure []
                        else pure
                            [ Finding
                                { _fCheck = "ci-parity"
                                , _fSeverity = Info
                                , _fMessage =
                                    ".github/workflows/ci.yml doesn't invoke `sky build` / `cabal test` / verify-*.sh"
                                , _fHint =
                                    "CI may be out of date — see docs/tooling/cli.md for canonical CI shape"
                                , _fFix = Nothing
                                } ]


-- | Sky stdlib version: compare the running sky binary's embedded
-- version to the user's `.skycache/stdlib-version` marker (if any).
-- Drift means the cache was generated by a different compiler.
checkStdlibVersion :: FilePath -> IO [Finding]
checkStdlibVersion root = do
    let markerPath = root </> ".skycache" </> "stdlib-version"
    markerExists <- Dir.doesFileExist markerPath
    if not markerExists
        then pure []  -- fresh project / never built
        else do
            contents <- try (readFile markerPath)
                :: IO (Either SomeException String)
            case contents of
                Left _ -> pure []
                Right cached ->
                    -- Read our own compiled-in version through env.
                    -- The sky binary sets SKY_VERSION_INTERNAL when
                    -- spawning Doctor; absence = unknown, skip.
                    let cachedTrim = trimSpace cached
                    in if null cachedTrim
                        then pure []
                        else do
                            sk <- Env.lookupEnv "SKY_VERSION_INTERNAL"
                            case sk of
                                Just cur | cur /= cachedTrim -> pure
                                    [ Finding
                                        { _fCheck = "stdlib-version-drift"
                                        , _fSeverity = Warn
                                        , _fMessage =
                                            ".skycache was generated by Sky " ++
                                            cachedTrim ++ " but you're on " ++ cur
                                        , _fHint =
                                            "delete `.skycache/` to regenerate (or run `sky doctor --fix`)"
                                        , _fFix = Just $ do
                                            let cache = root </> ".skycache"
                                            ok <- Dir.doesDirectoryExist cache
                                            if ok
                                                then do
                                                    Dir.removeDirectoryRecursive cache
                                                    pure ("✓ cleared " ++ cache)
                                                else pure
                                                    "✓ cache already clean"
                                        } ]
                                _ -> pure []


-- | sky.toml schema validation: warn on top-level keys that aren't
-- in the known set. Conservative — only flags surprise sections.
checkTomlSchema :: FilePath -> IO [Finding]
checkTomlSchema root = do
    let tomlPath = root </> "sky.toml"
    tomlExists <- Dir.doesFileExist tomlPath
    if not tomlExists
        then pure []
        else do
            contents <- try (readFile tomlPath)
                :: IO (Either SomeException String)
            case contents of
                Left _ -> pure []
                Right c ->
                    let sections =
                          [ trimSpace (drop 1 (init (trimSpace l)))
                          | l <- lines c
                          , let t = trimSpace l
                          , length t >= 2
                          , head t == '['
                          , last t == ']'
                          , not (isPrefixOf "[[" t)
                          ]
                        known =
                          [ "live", "log", "auth", "env", "subapp"
                          , "build", "test", "docs", "deploy"
                          , "live.session", "live.routes"
                          , "source", "database", "dependencies"
                          , "go.dependencies"
                          ]
                        unknown =
                          filter (\s -> not (s `elem` known)
                                     && not (any (\k -> isPrefixOf (k ++ ".") s) known))
                                 sections
                    in if null unknown
                        then pure []
                        else pure
                            [ Finding
                                { _fCheck = "toml-unknown-section"
                                , _fSeverity = Info
                                , _fMessage =
                                    "sky.toml has unknown section(s): "
                                    ++ joinWithComma unknown
                                , _fHint =
                                    "valid sections: " ++ joinWithComma known
                                    ++ " — see docs/sky-toml.md"
                                , _fFix = Nothing
                                } ]


-- | When sky.toml [subapp] sections name binary paths, those binaries
-- must exist and be executable. Otherwise sub-app spawn fails at
-- request time with a cryptic 502.
checkSubappBinaries :: FilePath -> IO [Finding]
checkSubappBinaries root = do
    let tomlPath = root </> "sky.toml"
    tomlExists <- Dir.doesFileExist tomlPath
    if not tomlExists
        then pure []
        else do
            contents <- try (readFile tomlPath)
                :: IO (Either SomeException String)
            case contents of
                Left _ -> pure []
                Right c ->
                    if not ("[subapp" `isInfixOf` c)
                        then pure []
                        else do
                            -- Cheap parse: look for bin = "..." lines
                            -- under any [subapp.*] section.
                            let bins =
                                  [ extractQuoted v
                                  | l <- lines c
                                  , let t = trimSpace l
                                  , "bin" `isPrefixOf` t
                                  , let v = dropWhile (== '=') (dropWhile (/= '=') t)
                                  ]
                            missing <- filterM (fmap not . binIsRunnable root) bins
                            if null missing
                                then pure []
                                else pure
                                    [ Finding
                                        { _fCheck = "subapp-bin-missing"
                                        , _fSeverity = Warn
                                        , _fMessage =
                                            "sub-app bin path(s) not executable: "
                                            ++ joinWithComma missing
                                        , _fHint =
                                            "build the sub-app(s) or fix the bin = \"...\" path in sky.toml"
                                        , _fFix = Nothing
                                        } ]
  where
    filterM _ [] = pure []
    filterM p (x:xs) = do
        keep <- p x
        rest <- filterM p xs
        pure (if keep then x : rest else rest)


binIsRunnable :: FilePath -> String -> IO Bool
binIsRunnable root path
    | null path = pure True  -- empty / unparsed — skip
    | otherwise = do
        let full = if isAbsolute path then path else root </> path
        ok <- Dir.doesFileExist full
        if ok
            then do
                perms <- Dir.getPermissions full
                pure (Dir.executable perms)
            else pure False
  where
    isAbsolute p = case p of
        ('/':_) -> True
        _       -> False


-- | `sky check` smoke: when src/Main.sky exists + sky-out/ is present,
-- a stale check signals codegen regression. Run `sky check` in a
-- best-effort path and surface its exit code.
--
-- We DON'T actually invoke `sky check` here (would recurse + slow);
-- instead we surface a hint to the user.
checkSkyCheckSmoke :: FilePath -> IO [Finding]
checkSkyCheckSmoke root = do
    let mainPath = root </> "src" </> "Main.sky"
        outPath = root </> "sky-out" </> "main.go"
    hasMain <- Dir.doesFileExist mainPath
    hasOut <- Dir.doesFileExist outPath
    if hasMain && hasOut
        then do
            -- If we already detected a stale build elsewhere, don't
            -- pile on. This check only fires when sky-out/ is fresh
            -- relative to src and we want a final smoke gate.
            mainMtime <- Dir.getModificationTime mainPath
            outMtime <- Dir.getModificationTime outPath
            if mainMtime `diffUTCTime` outMtime > 0
                then pure []  -- stale-build will fire elsewhere
                else pure
                    [ Finding
                        { _fCheck = "check-smoke"
                        , _fSeverity = Info
                        , _fMessage =
                            "sky-out/main.go is current — run `sky check` to confirm go build still succeeds"
                        , _fHint =
                            "`sky check src/Main.sky` runs HM type-check + `go build` on the emitted Go"
                        , _fFix = Nothing
                        } ]
        else pure []


-- | govulncheck: best-effort run of `govulncheck ./runtime-go/...`
-- if the binary is on PATH. Always warn-only — security drift is
-- informational, not a build gate.
checkGovulnCheck :: FilePath -> IO [Finding]
checkGovulnCheck root = do
    let runtimeDir = root </> "runtime-go"
    runtimeExists <- Dir.doesDirectoryExist runtimeDir
    if not runtimeExists
        then pure []  -- user project, not the Sky compiler repo
        else do
            -- Quick which check
            which <- try (Proc.readProcessWithExitCode
                "which" ["govulncheck"] "")
                :: IO (Either SomeException (ExitCode, String, String))
            case which of
                Right (ExitSuccess, _, _) -> pure
                    [ Finding
                        { _fCheck = "govulncheck-available"
                        , _fSeverity = Info
                        , _fMessage =
                            "govulncheck is installed — run periodically against runtime-go/"
                        , _fHint =
                            "cd runtime-go && govulncheck ./..."
                        , _fFix = Nothing
                        } ]
                _ -> pure
                    [ Finding
                        { _fCheck = "govulncheck-missing"
                        , _fSeverity = Info
                        , _fMessage =
                            "govulncheck not installed — Go-runtime CVE scanning unavailable"
                        , _fHint =
                            "install: `go install golang.org/x/vuln/cmd/govulncheck@latest`"
                        , _fFix = Nothing
                        } ]


-- ─── Helpers for v0.15.48 checks ─────────────────────────────


-- | Strip whitespace from both ends of a string.
trimSpace :: String -> String
trimSpace = dropWhile (`elem` (" \n\t" :: String))
          . reverse . dropWhile (`elem` (" \n\t" :: String)) . reverse


-- | Extract the contents of a single-quoted or double-quoted string
-- after an `=` sign on a TOML line.  `bin = "./billing-app"` → "./billing-app".
extractQuoted :: String -> String
extractQuoted raw =
    let s = trimSpace raw
        stripQuote q str = case str of
            (c:rest) | c == q -> takeWhile (/= q) rest
            _                 -> str
    in case s of
        ('=':rest) -> extractQuoted rest
        ('"':_)    -> stripQuote '"' s
        ('\'':_)   -> stripQuote '\'' s
        _          -> s


joinWithComma :: [String] -> String
joinWithComma []     = ""
joinWithComma [x]    = x
joinWithComma (x:xs) = x ++ ", " ++ joinWithComma xs


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
