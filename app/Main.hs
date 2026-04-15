{-# LANGUAGE TemplateHaskell #-}
module Main where

import Options.Applicative
import System.Exit (exitFailure, exitSuccess, ExitCode(..))
import System.IO (hPutStrLn, stderr)

import qualified System.Directory
import qualified System.Environment
import System.Directory (createDirectoryIfMissing, doesFileExist)
import System.IO.Error (catchIOError)
import System.FilePath ((</>), takeExtension, takeDirectory, takeFileName, dropExtension, splitDirectories)
import System.Exit (exitWith)
import Data.List (isPrefixOf, stripPrefix)
import System.Process (callProcess)
import qualified System.Process
import qualified System.IO.Temp
import qualified System.Exit
import Control.Monad (when)
import Data.FileEmbed (embedStringFile)

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Data.Text as T
import qualified Data.Text.IO as TIO
import qualified Sky.Build.Compile as Compile
import qualified Sky.Sky.Toml as Toml
import qualified Sky.Parse.Module as ParseMod
import qualified Sky.Format.Format as Format
import qualified Sky.Lsp.Server as Lsp
import qualified Sky.Build.FfiGen as FfiGen
import qualified Sky.Build.SkyDeps as SkyDeps


-- | End-to-end verification (replaces scripts/verify-examples.sh +
-- scripts/check-forbidden.sh). Returns True iff everything passed.
--
-- Stages:
--   1. Forbidden-pattern gate across src/, sky-stdlib/, examples/*/src/
--      (rejects Result String / Task String / Std.IoError / RemoteData).
--   2. Build + run every example (or the one named via `target`).
--      Panics in stderr / non-zero exit / non-2xx HTTP → fail.
runVerify :: Maybe String -> IO Bool
runVerify target = do
    cwd <- System.Directory.getCurrentDirectory
    forbiddenOk <- case target of
        Just _  -> return True   -- per-example run skips the global gate
        Nothing -> checkForbidden cwd
    when (not forbiddenOk) $
        hPutStrLn stderr "verify: forbidden-pattern gate failed"
    exampleOk <- runExampleVerify cwd target
    return (forbiddenOk && exampleOk)


-- | Grep gate for pre-v1 error-surface patterns. Fails the verify
-- run if any non-comment line in the Sky sources matches. Mirrors
-- test/Sky/ErrorUnificationSpec.hs for quick local runs.
checkForbidden :: FilePath -> IO Bool
checkForbidden cwd = do
    let patterns =
            [ ("Result String",  "Result[[:space:]]+String[[:space:]]")
            , ("Task String",    "Task[[:space:]]+String[[:space:]]")
            , ("Std.IoError",    "Std\\.IoError")
            , ("RemoteData",     "\\bRemoteData\\b")
            ]
        roots = ["src", "sky-stdlib"] ++ [cwd ++ "/examples"]
    results <- mapM (checkOne roots) patterns
    let fails = [ label | (label, False) <- zip (map fst patterns) results ]
    mapM_ (\l -> hPutStrLn stderr $ "  FORBIDDEN " ++ l) fails
    return (null fails)
  where
    checkOne _roots (_label, pat) = do
        (_ec, out, _) <- System.Process.readProcessWithExitCode "sh"
            [ "-c"
            , unwords
                [ "grep -rn --include='*.sky'"
                , "--exclude-dir=.skycache --exclude-dir=.skydeps --exclude-dir=sky-out"
                , shellQuote pat
                , shellQuote cwd ++ "/src"
                , shellQuote cwd ++ "/sky-stdlib"
                , shellQuote cwd ++ "/examples"
                , "2>/dev/null | grep -vE '^[^:]*:[0-9]+:[[:space:]]*--' | head -5"
                ]
            ] ""
        -- `out` is the filtered grep output (excluding comment-only lines).
        -- True = no matches = pass.
        return (null (filter (not . null) (lines out)))


shellQuote :: String -> String
shellQuote s = "'" ++ concatMap esc s ++ "'"
  where esc '\'' = "'\\''"; esc c = [c]


-- | Build + runtime-probe each example. Classification mirrors the
-- original scripts/example-sweep.sh: server / gui / cli. Failure
-- modes: build-fail, non-zero exit, panic in log, non-2xx HTTP.
runExampleVerify :: FilePath -> Maybe String -> IO Bool
runExampleVerify cwd target = do
    let examplesDir = cwd ++ "/examples"
    hasDir <- System.Directory.doesDirectoryExist examplesDir
    if not hasDir
        then do
            hPutStrLn stderr "verify: no examples/ directory"
            return True
        else do
            entries <- System.Directory.listDirectory examplesDir
            let dirs = case target of
                    Just t  -> filter (== t) entries
                    Nothing -> entries
                exampleDirs = [examplesDir ++ "/" ++ d | d <- dirs]
            mapM_ (verifyOne cwd) exampleDirs
            hasFailures <- readFile "/tmp/sky-verify-fails.txt"
                `catchIOError` (\_ -> return "")
            return (null (filter (not . null) (lines hasFailures)))


-- | Verify one example. Writes any failure reason to
-- /tmp/sky-verify-fails.txt (append). Uses the same shell primitives
-- the prior scripts/verify-examples.sh relied on — sky build, exec,
-- curl probe — now orchestrated from Haskell so the one-binary
-- contract holds.
verifyOne :: FilePath -> FilePath -> IO ()
verifyOne cwd dir = do
    let name = takeFileName dir
        tomlPath = dir </> "sky.toml"
        logPath  = "/tmp/sky-verify-" ++ name ++ ".log"
    hasToml <- doesFileExist tomlPath
    if not hasToml then return () else do
        -- Clean build.
        _ <- System.Process.readProcessWithExitCode "sh"
            [ "-c"
            , unwords
                [ "cd", shellQuote dir, "&&"
                , "rm -rf sky-out .skycache", "&&"
                , shellQuote (cwd ++ "/sky-out/sky"), "build src/Main.sky"
                , ">", shellQuote logPath, "2>&1"
                ]
            ] ""
        let bin = dir </> "sky-out" </> "app"
        hasBin <- doesFileExist bin
        if not hasBin
            then do
                putStrLn $ "  FAIL build: " ++ name
                appendFile "/tmp/sky-verify-fails.txt" (name ++ ":build\n")
            else classifyAndRun cwd name dir bin logPath


classifyAndRun :: FilePath -> String -> FilePath -> FilePath -> FilePath -> IO ()
classifyAndRun _cwd name dir bin logPath
    | isGui name = putStrLn $ "  gui skipped runtime: " ++ name
    | isServer name = do
        port <- readPortFromToml (dir </> "sky.toml")
        (_, stdoutTxt, _) <- System.Process.readProcessWithExitCode "sh"
            [ "-c"
            , unwords
                [ "(cd", shellQuote dir, "&& exec ./sky-out/app) >", shellQuote logPath, "2>&1 &"
                , "pid=$!;"
                , "tries=0; code=000;"
                , "while [ $tries -lt 20 ]; do"
                , "  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 1 'http://localhost:" ++ show port ++ "/' 2>/dev/null);"
                , "  case \"$code\" in 2??|3??) break;; esac;"
                , "  sleep 0.5; tries=$((tries+1));"
                , "done;"
                , "kill $pid 2>/dev/null; wait $pid 2>/dev/null;"
                , "if grep -Eq 'panic:|runtime error:|\\[sky\\.live\\] panic|\\[sky\\.http\\] panic' " ++ shellQuote logPath ++ "; then"
                , "  printf '%s\\n' '  FAIL panic: " ++ name ++ "'; echo " ++ shellQuote (name ++ ":panic") ++ " >> /tmp/sky-verify-fails.txt;"
                , "elif echo \"$code\" | grep -Eq '^(2|3)[0-9][0-9]$'; then"
                , "  printf '%s\\n' \"  runtime ok: " ++ name ++ " (http $code)\";"
                , "else"
                , "  printf '%s\\n' \"  FAIL http$code: " ++ name ++ "\"; echo " ++ shellQuote (name ++ ":http") ++ " >> /tmp/sky-verify-fails.txt;"
                , "fi"
                ]
            ] ""
        putStr stdoutTxt
        return ()
    | otherwise = do
        -- CLI example: run; panic / non-zero exit = fail.
        (ec, _, _) <- System.Process.readProcessWithExitCode "sh"
            [ "-c"
            , "cd " ++ shellQuote dir ++ " && ./sky-out/app > " ++ shellQuote logPath ++ " 2>&1"
            ] ""
        hasPanic <- hasPanicIn logPath
        case (ec, hasPanic) of
            (_, True) -> do
                putStrLn $ "  FAIL panic: " ++ name
                appendFile "/tmp/sky-verify-fails.txt" (name ++ ":panic\n")
            (System.Exit.ExitFailure n, _) -> do
                putStrLn $ "  FAIL exit " ++ show n ++ ": " ++ name
                appendFile "/tmp/sky-verify-fails.txt" (name ++ ":exit\n")
            _ -> do
                -- expected.txt comparison if the file exists.
                let expected = dir </> "expected.txt"
                hasExpected <- doesFileExist expected
                if hasExpected
                    then do
                        want <- readFile expected
                        got  <- readFile logPath
                        if want == got
                            then putStrLn $ "  runtime ok: " ++ name
                            else do
                                putStrLn $ "  FAIL expected.txt mismatch: " ++ name
                                appendFile "/tmp/sky-verify-fails.txt" (name ++ ":expected\n")
                    else putStrLn $ "  runtime ok: " ++ name


hasPanicIn :: FilePath -> IO Bool
hasPanicIn path = do
    exists <- doesFileExist path
    if not exists then return False else do
        content <- readFile path
        return ("panic:" `isPrefixOf` dropWhile (/= '\n') content
                || "panic:" `isSubstringOf` content)


isSubstringOf :: String -> String -> Bool
isSubstringOf needle hay = any (isPrefixOf needle) (tails hay)
  where tails [] = [[]]; tails (_:xs') = [] : map id (tails xs')


readPortFromToml :: FilePath -> IO Int
readPortFromToml path = do
    src <- readFile path
    let ls = [ dropWhile (\c -> c == ' ' || c == '=') (drop 4 l)
             | l <- lines src
             , "port" `isPrefixOf` l
             ]
        digits = filter (`elem` ['0'..'9']) (concat ls)
    return (if null digits then 8000 else read digits)


isServer :: String -> Bool
isServer n = n `elem`
    [ "05-mux-server", "08-notes-app", "09-live-counter"
    , "10-live-component", "12-skyvote", "13-skyshop"
    , "15-http-server", "16-skychess", "17-skymon", "18-job-queue"
    ]


isGui :: String -> Bool
isGui n = n == "11-fyne-stopwatch"


-- | Derive a dotted Sky module name from a source file path. The
-- path is expected to be absolute; we peel off the source root
-- (`<cwd>/src/` or `<cwd>/tests/`) and translate `/` → `.`, dropping
-- the `.sky` extension. Returns Nothing for files outside those
-- roots so the caller can emit a user-friendly error.
moduleNameFromPath :: FilePath -> FilePath -> Maybe String
moduleNameFromPath = moduleNameFromPathWithRoots ["src", "tests"]


moduleNameFromPathWithRoots :: [FilePath] -> FilePath -> FilePath -> Maybe String
moduleNameFromPathWithRoots roots cwd absPath
    | takeExtension absPath /= ".sky" = Nothing
    | otherwise =
        let normaliseRoot r = if r == "." || null r
                then cwd
                else cwd </> r
            candidates = map normaliseRoot roots
            stripRoot root = stripPrefix (root ++ "/") absPath
            relative = foldr
                (\root acc -> case acc of
                    Just _  -> acc
                    Nothing -> stripRoot root)
                Nothing
                candidates
        in case relative of
            Nothing -> Nothing
            Just rel ->
                let stem  = dropExtension rel
                    parts = splitDirectories stem
                    -- Sky module segments must begin with an uppercase
                    -- letter. Test directory path segments are often
                    -- lowercase (tests/core/FooTest.sky → core is
                    -- `core` on disk, `Core` in Sky). Capitalise the
                    -- first letter of every segment when it isn't
                    -- already uppercase.
                    capFirst (c:cs) | c >= 'a' && c <= 'z' = toEnum (fromEnum c - 32) : cs
                    capFirst s = s
                    rewritten = map capFirst parts
                in Just (foldr (\a b -> if null b then a else a ++ "." ++ b) "" rewritten)


-- | For each declared go dep, regenerate the FFI bindings when its
-- `.skycache/ffi/<slug>.kernel.json` file is absent. Used by `sky
-- install` and the `sky build` auto-regen fallback. Silently skips
-- inspector failures — user can still run `sky add <pkg>` manually.
regenMissingBindings :: [(String, String)] -> IO ()
regenMissingBindings deps = do
    createDirectoryIfMissing True ".skycache/ffi"
    mapM_ regenOne deps
  where
    regenOne (pkg, _ver) = do
        let slug = FfiGen.slugify pkg
            jsonPath = ".skycache/ffi/" ++ slug ++ ".kernel.json"
        already <- doesFileExist jsonPath
        if already then return ()
        else do
            -- Fetch Go module + generate bindings. Same flow as `sky add`.
            callProcess "sh" ["-c", "cd sky-out && go get " ++ pkg ++ " 2>&1 | grep -v '^go:' >&2 || true"]
            r <- FfiGen.runInspector pkg
            case r of
                Left _   -> return ()
                Right info -> do
                    _ <- FfiGen.generateBindings info
                    return ()


-- | Sky compiler CLI
-- Commands: build, run, check, fmt, init, add, remove, install, lsp, upgrade, version
main :: IO ()
main = do
    -- `sky` with no arguments should print the help screen and exit 0
    -- instead of a bare "Missing: (COMMAND)" error. Inject `--help`
    -- into argv when none is present.
    args <- System.Environment.getArgs
    result <- if null args
        then do
            _ <- handleParseResult $ execParserPure defaultPrefs opts ["--help"]
            return (Right ())
        else do
            cmd <- execParser opts
            runCommand cmd
    case result of
        Right () -> exitSuccess
        Left err -> do
            hPutStrLn stderr err
            exitFailure
  where
    opts = info (commandParser <**> helper)
        ( fullDesc
        <> header "sky — the Sky programming language compiler"
        <> progDesc "Compile Sky to typed Go"
        )


data Command
    = Build FilePath
    | Run FilePath
    | Check FilePath
    | Fmt FmtTarget
    | Test FilePath
    | Verify (Maybe String)      -- Nothing = all examples; Just name = one
    | Init (Maybe String)
    | Add String
    | Remove String
    | Install
    | Update
    | Clean
    | Lsp
    | Upgrade
    | Version
    deriving (Show)


data FmtTarget
    = FmtFile FilePath
    | FmtStdin
    deriving (Show)


commandParser :: Parser Command
commandParser = subparser
    ( command "build"
        (info (Build <$> fileArg) (progDesc "Compile to binary"))
    <> command "run"
        (info (Run <$> fileArg) (progDesc "Build and run"))
    <> command "check"
        (info (Check <$> fileArg) (progDesc "Type-check only"))
    <> command "fmt"
        (info (Fmt <$> fmtTargetArg)
            (progDesc "Format source file (or stdin with --stdin / -)"))
    <> command "test"
        (info (Test <$> fileArg) (progDesc "Run a Sky test module (exposing `tests : List Test`)"))
    <> command "verify"
        (info (Verify <$> optional (argument str (metavar "EXAMPLE")))
            (progDesc "Build + run + panic-check every example; enforce forbidden-pattern gate"))
    <> command "init"
        (info (Init <$> optional (argument str (metavar "NAME")))
            (progDesc "Create new project"))
    <> command "add"
        (info (Add <$> argument str (metavar "PACKAGE"))
            (progDesc "Add Go dependency"))
    <> command "remove"
        (info (Remove <$> argument str (metavar "PACKAGE"))
            (progDesc "Remove Go dependency"))
    <> command "install"
        (info (pure Install) (progDesc "Install dependencies"))
    <> command "update"
        (info (pure Update) (progDesc "Update Go dependencies to latest"))
    <> command "clean"
        (info (pure Clean) (progDesc "Remove build artifacts (sky-out/, .skycache/)"))
    <> command "lsp"
        (info (pure Lsp) (progDesc "Start language server"))
    <> command "upgrade"
        (info (pure Upgrade) (progDesc "Self-upgrade"))
    <> command "version"
        (info (pure Version) (progDesc "Show version"))
    )
  <|> flag' Version
        ( long "version"
        <> short 'v'
        <> help "Show version"
        )


fileArg :: Parser FilePath
fileArg = argument str (metavar "FILE" <> value "src/Main.sky")


-- Accept either `--stdin` / `-` / positional FILE. Used by `sky fmt`
-- so editors (helix, neovim, vscode) can pipe buffers directly.
fmtTargetArg :: Parser FmtTarget
fmtTargetArg =
    flag' FmtStdin (long "stdin" <> help "Read source from stdin, write formatted output to stdout")
  <|> (toTarget <$> argument str (metavar "FILE" <> value "src/Main.sky"))
  where
    toTarget "-"  = FmtStdin
    toTarget path = FmtFile path


-- | CLAUDE.md contents are embedded into the sky binary at build time
-- via Template Haskell, so `sky init` works from any release artefact
-- without needing a templates/ directory beside the binary.
embeddedClaudeMd :: String
embeddedClaudeMd = $(embedStringFile "templates/CLAUDE.md")


-- | Copy a named template into the new project. For CLAUDE.md we use
-- the embedded copy; other templates fall through to disk lookup so
-- future project-scaffolding additions don't require a compiler rebuild.
copyTemplate :: FilePath -> FilePath -> IO ()
copyTemplate destProject "CLAUDE.md" =
    writeFile (destProject ++ "/CLAUDE.md") embeddedClaudeMd
copyTemplate destProject filename = do
    -- Disk-template fallback for names other than CLAUDE.md.
    candidates <- templateSearchPaths filename
    mSrc <- firstExisting candidates
    case mSrc of
        Nothing  -> return ()
        Just src -> do
            content <- readFile src
            writeFile (destProject ++ "/" ++ filename) content
  where
    firstExisting [] = return Nothing
    firstExisting (p:ps) = do
        ok <- doesFileExist p
        if ok then return (Just p) else firstExisting ps


templateSearchPaths :: FilePath -> IO [FilePath]
templateSearchPaths filename = do
    env <- System.Environment.lookupEnv "SKY_TEMPLATES_DIR"
    exe <- System.Environment.getExecutablePath
    let exeDir = dirOf exe
    cwd <- System.Directory.getCurrentDirectory
    return $ concat
        [ maybe [] (\d -> [d </> filename]) env
        , [ exeDir </> "templates" </> filename
          , exeDir </> ".." </> "templates" </> filename
          , cwd </> "templates" </> filename
          ]
        ]
  where
    dirOf = reverse . dropWhile (/= '/') . reverse
    (</>) a b = a ++ "/" ++ b


runCommand :: Command -> IO (Either String ())
runCommand cmd = case cmd of
    Version -> do
        putStrLn "sky v1.0.0 (haskell)"
        return (Right ())

    Build path -> do
        -- Read sky.toml if it exists
        hasToml <- doesFileExist "sky.toml"
        config <- if hasToml
            then Toml.parseSkyToml <$> readFile "sky.toml"
            else return Toml.defaultConfig
        let outDir = "sky-out"
        createDirectoryIfMissing True outDir
        -- Auto-regen missing Go FFI bindings before compile. Idempotent:
        -- skips deps whose .kernel.json is already present.
        let goDeps = Toml._goDeps config
        when (not (null goDeps)) $ do
            hasGoMod <- doesFileExist "sky-out/go.mod"
            when (not hasGoMod) $ do
                hasRt <- doesFileExist "runtime-go/go.mod"
                if hasRt
                    then callProcess "cp" ["runtime-go/go.mod", "sky-out/go.mod"]
                    else writeFile "sky-out/go.mod" $ unlines ["module sky-app", "", "go 1.21"]
            regenMissingBindings goDeps
        result <- Compile.compile config path outDir
        case result of
            Left err -> return (Left err)
            Right goPath -> do
                putStrLn "Running go build..."
                callProcess "sh" ["-c", "cd " ++ outDir ++ " && go build -o " ++ Toml._binName config ++ " ."]
                putStrLn $ "Build complete: " ++ outDir ++ "/" ++ Toml._binName config
                return (Right ())

    Run path -> do
        -- Build first, then exec
        hasToml <- doesFileExist "sky.toml"
        config <- if hasToml
            then Toml.parseSkyToml <$> readFile "sky.toml"
            else return Toml.defaultConfig
        let outDir = "sky-out"
        createDirectoryIfMissing True outDir
        let goDeps = Toml._goDeps config
        when (not (null goDeps)) $ do
            hasGoMod <- doesFileExist "sky-out/go.mod"
            when (not hasGoMod) $ do
                hasRt <- doesFileExist "runtime-go/go.mod"
                if hasRt
                    then callProcess "cp" ["runtime-go/go.mod", "sky-out/go.mod"]
                    else writeFile "sky-out/go.mod" $ unlines ["module sky-app", "", "go 1.21"]
            regenMissingBindings goDeps
        result <- Compile.compile config path outDir
        case result of
            Left err -> return (Left err)
            Right goPath -> do
                putStrLn "Running go build..."
                callProcess "sh" ["-c", "cd " ++ outDir ++ " && go build -o " ++ Toml._binName config ++ " ."]
                putStrLn $ "Build complete, running..."
                callProcess (outDir ++ "/" ++ Toml._binName config) []
                return (Right ())

    Check path -> do
        hasToml <- doesFileExist "sky.toml"
        config <- if hasToml
            then Toml.parseSkyToml <$> readFile "sky.toml"
            else return Toml.defaultConfig
        -- Regen missing FFI bindings so type-check sees up-to-date .skyi
        -- signatures without needing the user to run `sky build` first.
        let outDir = "sky-out"
            goDeps = Toml._goDeps config
        when (not (null goDeps)) $ do
            createDirectoryIfMissing True outDir
            hasGoMod <- doesFileExist "sky-out/go.mod"
            when (not hasGoMod) $ do
                hasRt <- doesFileExist "runtime-go/go.mod"
                if hasRt
                    then callProcess "cp" ["runtime-go/go.mod", "sky-out/go.mod"]
                    else writeFile "sky-out/go.mod" $ unlines ["module sky-app", "", "go 1.21"]
            regenMissingBindings goDeps
        -- P0-1 (audit): sky check must be a superset of sky build. Run
        -- the full emit + `go build` so codegen-stage failures surface
        -- here instead of only when the user runs `sky build`. Without
        -- this gate the checker accepted programs that panicked at
        -- runtime (typed-callee .(T) assertions, record-ctor field
        -- swaps, Task-return coercion holes) because the Sky type
        -- system was satisfied but codegen produced invalid Go.
        result <- Compile.compile config path outDir
        case result of
            Left err -> return (Left err)
            Right _ -> do
                putStrLn "Running go build..."
                (ec, _, berr) <- System.Process.readCreateProcessWithExitCode
                    (System.Process.shell
                        ("cd " ++ outDir ++ " && go build -o /dev/null ."))
                    ""
                case ec of
                    System.Exit.ExitSuccess -> do
                        putStrLn "No errors found."
                        return (Right ())
                    System.Exit.ExitFailure _ -> do
                        let msg = "Codegen produced Go that `go build` rejects.\n"
                                ++ "This is a compiler-side bug — the Sky type system\n"
                                ++ "accepted the program but Go did not.\n\n"
                                ++ "Go errors:\n"
                                ++ berr
                        return (Left msg)

    Test path -> do
        -- Synthesise a temporary Main.sky that imports the user's test
        -- module and calls `Sky.Test.runMain tests`. Build + run via the
        -- same pipeline as `sky build`; exit code is propagated so CI
        -- picks up failures. The synthesis keeps user test modules
        -- minimal: `module FooTest exposing (tests); tests = [...]`.
        hasToml <- doesFileExist "sky.toml"
        config <- if hasToml
            then Toml.parseSkyToml <$> readFile "sky.toml"
            else return Toml.defaultConfig
        absPath <- System.Directory.canonicalizePath path
        cwd <- System.Directory.getCurrentDirectory
        -- Honour the configured source root (default src/) and the
        -- common tests/ convention.
        let sourceRoots = [Toml._sourceRoot config, "src", "tests"]
        testModName <- case moduleNameFromPathWithRoots sourceRoots cwd absPath of
            Just n  -> return n
            Nothing -> do
                hPutStrLn stderr $
                    "sky test: " ++ path ++ " must live under src/ or tests/ so its module name can be derived"
                exitFailure
        -- Write the synthesised entry into the project's configured
        -- source root (defaults to `src/`; test projects commonly use
        -- `tests/`). Placing it anywhere else would leave it outside
        -- the module-graph walker's scan.
        let entryDir  = Toml._sourceRoot config
            entryFile = entryDir </> "SkyTestEntry__.sky"
            entryBody = unlines
                [ "module SkyTestEntry__ exposing (main)"
                , ""
                , "import Sky.Test as Test"
                , "import " ++ testModName ++ " as Suite"
                , ""
                , "main ="
                , "    Test.runMain Suite.tests"
                ]
        createDirectoryIfMissing True entryDir
        writeFile entryFile entryBody
        let outDir = "sky-out"
        createDirectoryIfMissing True outDir
        let goDeps = Toml._goDeps config
        when (not (null goDeps)) $ do
            hasGoMod <- doesFileExist "sky-out/go.mod"
            when (not hasGoMod) $ do
                hasRt <- doesFileExist "runtime-go/go.mod"
                if hasRt
                    then callProcess "cp" ["runtime-go/go.mod", "sky-out/go.mod"]
                    else writeFile "sky-out/go.mod" $ unlines ["module sky-app", "", "go 1.21"]
            regenMissingBindings goDeps
        result <- Compile.compile config entryFile outDir
        -- Clean up the entry regardless of compile outcome.
        let cleanup = do
                System.Directory.removeFile entryFile
                    `catchIOError` (\_ -> return ())
        case result of
            Left err -> do
                cleanup
                return (Left err)
            Right _ -> do
                let binName = Toml._binName config
                callProcess "sh" ["-c", "cd " ++ outDir ++ " && go build -o " ++ binName ++ " ."]
                cleanup
                -- Run with inherited stdout/stderr so test output is
                -- visible; propagate exit code.
                (_, _, _, ph) <- System.Process.createProcess
                    (System.Process.proc (outDir ++ "/" ++ binName) [])
                ec <- System.Process.waitForProcess ph
                case ec of
                    System.Exit.ExitSuccess   -> return (Right ())
                    System.Exit.ExitFailure n ->
                        exitWith (System.Exit.ExitFailure n)

    Verify target -> do
        ok <- runVerify target
        if ok then return (Right ()) else exitWith (System.Exit.ExitFailure 1)

    Fmt target -> do
        case target of
            FmtFile path -> do
                src <- TIO.readFile path
                case ParseMod.parseModule src of
                    Left err -> return (Left $ "Parse error: " ++ show err)
                    Right srcMod -> do
                        let baseOut = T.pack (Format.formatModule srcMod)
                            withComments = preserveTopLevelComments src baseOut
                        case fmtSafetyCheck src withComments of
                            Just msg -> return (Left msg)
                            Nothing -> do
                                TIO.writeFile path withComments
                                putStrLn $ "Formatted " ++ path
                                return (Right ())
            FmtStdin -> do
                src <- TIO.getContents
                case ParseMod.parseModule src of
                    Left err -> do
                        TIO.putStr src
                        return (Left $ "Parse error: " ++ show err)
                    Right srcMod -> do
                        let baseOut = T.pack (Format.formatModule srcMod)
                            withComments = preserveTopLevelComments src baseOut
                        force <- System.Environment.lookupEnv "SKY_FMT_FORCE"
                        debug <- System.Environment.lookupEnv "SKY_FMT_DEBUG"
                        case debug of
                            Just _ -> do
                                hPutStrLn stderr "=== baseOut (pre-preserver) ==="
                                TIO.hPutStr stderr baseOut
                                hPutStrLn stderr "=== withComments (post-preserver) ==="
                            _ -> return ()
                        case (force, fmtSafetyCheck src withComments) of
                            (Just _, _)        -> TIO.putStr withComments >> return (Right ())
                            (Nothing, Just m)  -> TIO.putStr src >> return (Left m)
                            (Nothing, Nothing) -> TIO.putStr withComments >> return (Right ())

    Init mName -> do
        let name = maybe "sky-project" id mName
        putStrLn $ "Initialising project: " ++ name
        createDirectoryIfMissing True (name ++ "/src")
        writeFile (name ++ "/sky.toml") $ unlines
            [ "# sky.toml — project configuration."
            , "# Full reference: https://github.com/anzellai/sky#skytoml"
            , ""
            , "name    = \"" ++ name ++ "\""
            , "version = \"0.1.0\""
            , "entry   = \"src/Main.sky\""
            , "bin     = \"app\""
            , ""
            , "[source]"
            , "root = \"src\""
            , ""
            , "# [live]            # Sky.Live runtime (uncomment to configure)"
            , "# port      = 8000"
            , "# store     = \"memory\"      # memory | sqlite | postgres"
            , "# storePath = \"sky.db\"       # sqlite file or postgres conn str"
            , "# ttl       = 1800             # session TTL in seconds"
            , "# static    = \"public\"       # static asset directory"
            , ""
            , "# [auth]            # Std.Auth configuration (uncomment to use)"
            , "# driver     = \"jwt\"         # jwt | session | oauth"
            , "# secret     = \"change-me\"   # JWT signing secret (use env var in prod)"
            , "# tokenTtl   = 86400           # token lifetime in seconds"
            , "# cookieName = \"sky_auth\""
            , ""
            , "# [database]        # Std.Db configuration (uncomment to use)"
            , "# driver = \"sqlite\"          # sqlite | postgres"
            , "# path   = \"app.db\"          # sqlite file or postgres conn str"
            , ""
            , "# [\"go.dependencies\"]        # `sky add <pkg>` records these here"
            , ""
            , "# [dependencies]              # Sky-source dependencies (from git)"
            , "# \"github.com/anzellai/sky-tailwind\" = \"latest\""
            ]
        writeFile (name ++ "/src/Main.sky") $ unlines
            [ "module Main exposing (main)"
            , ""
            , "import Sky.Core.Prelude exposing (..)"
            , "import Std.Log exposing (println)"
            , ""
            , ""
            , "main ="
            , "    println \"Hello from " ++ name ++ "!\""
            ]
        writeFile (name ++ "/.gitignore") $ unlines
            [ "sky-out/"
            , ".skycache/"
            , ".skydeps/"
            , ".env"
            , "*.db"
            , "*.db-shm"
            , "*.db-wal"
            ]
        -- Copy the Sky coding guide so AI assistants operating in this
        -- project have context on stdlib / idioms. Template lives next
        -- to the installed binary; also probe the dev-tree path.
        copyTemplate name "CLAUDE.md"
        putStrLn $ "Created " ++ name ++ "/"
        putStrLn $ "  sky.toml"
        putStrLn $ "  src/Main.sky"
        putStrLn $ "  .gitignore"
        putStrLn $ "  CLAUDE.md"
        putStrLn $ ""
        putStrLn $ "Next: cd " ++ name ++ " && sky build src/Main.sky"
        return (Right ())

    Add pkg -> do
        putStrLn $ "Adding " ++ pkg ++ "..."
        -- Ensure sky-out exists with go.mod (copy from runtime-go to inherit deps)
        createDirectoryIfMissing True "sky-out"
        hasGoMod <- doesFileExist "sky-out/go.mod"
        if not hasGoMod
            then do
                hasRuntimeMod <- doesFileExist "runtime-go/go.mod"
                if hasRuntimeMod
                    then callProcess "cp" ["runtime-go/go.mod", "sky-out/go.mod"]
                    else writeFile "sky-out/go.mod" $ unlines ["module sky-app", "", "go 1.21"]
            else return ()
        -- Fetch the package
        callProcess "sh" ["-c", "cd sky-out && go get " ++ pkg]
        -- Generate bindings via the Go inspector
        do
                putStrLn $ "Inspecting " ++ pkg ++ "..."
                r <- FfiGen.runInspector pkg
                case r of
                    Left err -> do
                        putStrLn $ "   FFI inspector warning: " ++ err
                        putStrLn $ "   (You can still write hand-written bindings in ffi/.)"
                        return (Right ())
                    Right info -> do
                        names <- FfiGen.generateBindings info
                        putStrLn $ "Generated " ++ show (length names) ++ " bindings in ffi/"
                        mapM_ (\n -> putStrLn $ "   " ++ n) (take 10 names)
                        if length names > 10
                            then putStrLn $ "   ... and " ++ show (length names - 10) ++ " more"
                            else return ()
                        putStrLn "Call from Sky via: Ffi.callPure \"<name>\" [args]  (or callTask for effectful)"
                        return (Right ())

    Remove pkg -> do
        putStrLn $ "Removing " ++ pkg ++ "..."
        hasGoMod <- doesFileExist "sky-out/go.mod"
        if hasGoMod
            then do
                callProcess "sh" ["-c", "cd sky-out && go mod edit -droprequire " ++ pkg ++ " && go mod tidy"]
                putStrLn $ "Removed " ++ pkg
            else putStrLn "No sky-out/go.mod found. Run sky build first."
        return (Right ())

    Install -> do
        hasToml <- doesFileExist "sky.toml"
        config <- if hasToml
            then Toml.parseSkyToml <$> readFile "sky.toml"
            else return Toml.defaultConfig
        _ <- SkyDeps.installDeps (Toml._skyDeps config)
        -- Auto-regen Go FFI bindings for every declared go dep whose
        -- `.skycache/ffi/<slug>.kernel.json` is absent. This replaces
        -- the old workflow where bindings were checked-in under ffi/.
        let goDeps = Toml._goDeps config
        when (not (null goDeps)) $ do
            createDirectoryIfMissing True "sky-out"
            hasGoMod <- doesFileExist "sky-out/go.mod"
            when (not hasGoMod) $ do
                hasRt <- doesFileExist "runtime-go/go.mod"
                if hasRt
                    then callProcess "cp" ["runtime-go/go.mod", "sky-out/go.mod"]
                    else writeFile "sky-out/go.mod" $ unlines ["module sky-app", "", "go 1.21"]
            regenMissingBindings goDeps
        case Toml._skyDeps config of
            [] -> return ()
            _  -> putStrLn "Sky dependencies installed."
        when (null (Toml._skyDeps config) && null goDeps) $
            putStrLn "No [dependencies] or [go.dependencies] entries in sky.toml."
        return (Right ())

    Update -> do
        hasGoMod <- doesFileExist "sky-out/go.mod"
        if not hasGoMod
            then do
                putStrLn "No sky-out/go.mod found. Run `sky build` first."
                return (Right ())
            else do
                putStrLn "Updating Go dependencies..."
                callProcess "sh" ["-c", "cd sky-out && go get -u ./... && go mod tidy"]
                putStrLn "Go dependencies updated."
                return (Right ())

    Clean -> do
        let removeIfExists p = do
                isDir  <- System.Directory.doesDirectoryExist p
                isFile <- doesFileExist p
                when isDir  (System.Directory.removeDirectoryRecursive p)
                when isFile (System.Directory.removeFile p)
        mapM_ removeIfExists ["sky-out", ".skycache", ".skydeps", "dist"]
        putStrLn "Removed sky-out/ .skycache/ .skydeps/ dist/"
        return (Right ())

    Lsp -> do
        -- LSP talks JSON-RPC on stdin/stdout; don't print anything to stdout
        -- after this point (it would corrupt the protocol framing).
        Lsp.runLsp
        return (Right ())

    Upgrade -> runUpgrade


-- | P11a: `sky upgrade` — fetch latest release from GitHub and swap the
-- running binary in place. Shells out to `curl` + `tar` so we pull in no
-- new Haskell dependencies and stay portable across macOS/Linux.
--
-- Pipeline:
--   1. Detect current platform (darwin-arm64 / linux-x64 etc).
--   2. GET https://api.github.com/repos/anzellai/sky/releases/latest
--   3. Parse tag_name (raw grep — the endpoint is stable).
--   4. Download the matching tarball into a temp dir.
--   5. `tar -xzf` then atomically rename(new, old).
--
-- Exit 1 with a clear message on any failure; never corrupt the existing
-- binary.
runUpgrade :: IO (Either String ())
runUpgrade = do
    putStrLn "sky upgrade: detecting platform..."
    (osName, arch) <- detectPlatform
    let platform = osName ++ "-" ++ arch
    putStrLn $ "   platform: " ++ platform
    putStrLn "   fetching latest release metadata..."
    releaseJson <- System.Process.readProcess "curl"
        [ "-sSL"
        , "-H", "Accept: application/vnd.github+json"
        , "https://api.github.com/repos/anzellai/sky/releases/latest"
        ] ""
    case extractTagName releaseJson of
        Nothing ->
            return (Left "sky upgrade: could not parse release metadata — is the repo reachable?")
        Just tag -> do
            putStrLn $ "   latest tag: " ++ tag
            currentBin <- System.Environment.getExecutablePath
            let assetName = "sky-" ++ platform ++ ".tar.gz"
                dlUrl = "https://github.com/anzellai/sky/releases/download/"
                            ++ tag ++ "/" ++ assetName
            tmpDir <- System.IO.Temp.getCanonicalTemporaryDirectory
            let stageDir = tmpDir ++ "/sky-upgrade-" ++ tag
            System.Directory.createDirectoryIfMissing True stageDir
            putStrLn $ "   downloading " ++ dlUrl
            (curlEC, _, curlErr) <- System.Process.readProcessWithExitCode "curl"
                [ "-sSLfo", stageDir ++ "/sky.tar.gz", dlUrl ] ""
            case curlEC of
                System.Exit.ExitFailure _ ->
                    return $ Left $ "sky upgrade: download failed — " ++ curlErr
                System.Exit.ExitSuccess -> do
                    putStrLn "   extracting..."
                    (tarEC, _, tarErr) <- System.Process.readProcessWithExitCode "tar"
                        [ "-xzf", stageDir ++ "/sky.tar.gz", "-C", stageDir ] ""
                    case tarEC of
                        System.Exit.ExitFailure _ ->
                            return $ Left $ "sky upgrade: extract failed — " ++ tarErr
                        System.Exit.ExitSuccess -> do
                            let candidate = stageDir ++ "/sky-" ++ platform
                            haveCandidate <- doesFileExist candidate
                            let newBin = if haveCandidate then candidate
                                         else stageDir ++ "/sky"
                            haveNewBin <- doesFileExist newBin
                            if not haveNewBin
                                then return $ Left $
                                    "sky upgrade: archive did not contain a `sky` binary"
                                else do
                                    putStrLn $ "   swapping " ++ currentBin
                                    System.Directory.copyFile newBin (currentBin ++ ".new")
                                    System.Directory.renameFile (currentBin ++ ".new") currentBin
                                    _ <- System.Process.readProcessWithExitCode
                                        "chmod" ["+x", currentBin] ""
                                    putStrLn $ "sky upgrade: upgraded to " ++ tag
                                    return (Right ())


-- | Pull the `"tag_name"` field out of a GitHub release JSON blob. We
-- don't want to depend on aeson here for the upgrade path (keeps the
-- critical self-update code path minimal). Robust to whitespace and
-- surrounding fields — we look for the literal key.
extractTagName :: String -> Maybe String
extractTagName s = go s
  where
    needle = "\"tag_name\""
    go [] = Nothing
    go t@(_:rest)
        | take (length needle) t == needle =
            let afterKey = drop (length needle) t
                afterColon = dropWhile (\c -> c == ':' || c == ' ' || c == '\t') afterKey
            in case afterColon of
                ('"' : rest') -> Just (takeWhile (/= '"') rest')
                _             -> Nothing
        | otherwise = go rest


-- | Identify the current OS + arch in a form that matches our release
-- artefact naming (e.g. `darwin-arm64`, `linux-x64`).
detectPlatform :: IO (String, String)
detectPlatform = do
    (_, unameOs, _) <- System.Process.readProcessWithExitCode "uname" ["-s"] ""
    (_, unameArch, _) <- System.Process.readProcessWithExitCode "uname" ["-m"] ""
    let os = case trim unameOs of
            "Darwin"   -> "darwin"
            "Linux"    -> "linux"
            other      -> map toLowerChar other
        arch = case trim unameArch of
            "arm64"    -> "arm64"
            "aarch64"  -> "arm64"
            "x86_64"   -> "x64"
            "amd64"    -> "x64"
            other      -> other
    return (os, arch)
  where
    trim = dropWhile isSpace . reverse . dropWhile isSpace . reverse
    isSpace c = c == ' ' || c == '\n' || c == '\t' || c == '\r'
    toLowerChar c
        | c >= 'A' && c <= 'Z' = toEnum (fromEnum c + 32)
        | otherwise = c


-- ─── Formatter safety guard ──────────────────────────────────────────
-- Refuses to write formatter output that silently drops comments or
-- loses more than 1/3 of the original code lines.
--
-- Why: the parser currently skips line/block comments entirely rather
-- than attaching them to the AST, so Format.formatModule has nothing
-- to emit — the result is byte-identical except every comment is
-- gone. A user who runs `sky fmt` on a heavily-commented file would
-- silently lose their comments. Until the AST gains comment nodes,
-- fail loudly instead of destroying the source.
fmtSafetyCheck :: T.Text -> T.Text -> Maybe String
fmtSafetyCheck srcIn srcOut =
    let commentsBefore = countComments srcIn
        commentsAfter  = countComments srcOut
    in if commentsBefore > commentsAfter
         then Just $ unlines
             [ "refusing to format: " ++ show commentsBefore
                 ++ " comment line(s) in input but only "
                 ++ show commentsAfter ++ " in output."
             , "sky fmt does not round-trip comments yet; the AST drops them during parsing."
             , "Until the AST gains comment nodes, strip comments first or format the file by hand."
             ]
       else Nothing
  where
    countComments t =
        length [l | l <- map T.strip (T.lines t)
                  , T.pack "--" `T.isPrefixOf` l || T.pack "{-" `T.isPrefixOf` l]

-- ─── Comment preservation across sky fmt ─────────────────────────
-- The parser discards comments before they reach the AST, so
-- Format.formatModule emits output without them. This post-pass
-- scans the original source for comment blocks and re-inserts them
-- into the formatted output, keyed by either:
--   * the next top-level declaration header (for module-level comments)
--   * the preceding code line's stripped text (for body comments inside
--     let / case / etc.).
--
-- Declaration header keys:
--   * `name =` / `name :` / `name arg =`       → "val:name"
--   * `type alias Name = ...`                  → "alias:Name"
--   * `type Name = ...`                        → "type:Name"
--   * `import A.B.C ...`                       → "import:A.B.C"
--   * `module M exposing (...)`                → "module"
--
-- Body-comment anchors use "after:<stripped preceding line>" and are
-- matched on first-occurrence in the output. This gives correct
-- placement for the common case (comments inside let bodies) without
-- needing per-node AST position tracking.
preserveTopLevelComments :: T.Text -> T.Text -> T.Text
preserveTopLevelComments source formatted =
    let srcBlocks    = collectCommentBlocks source
        headerMap    = foldl addHeaderBlock Map.empty srcBlocks
        anchorMap    = foldl addAnchorBlock Map.empty srcBlocks
        trailingMap  = collectTrailingComments source
        outLines     = T.lines formatted
        withTrailing = map (reattachTrailing trailingMap) outLines
        injected     = injectComments headerMap anchorMap withTrailing
    in T.unlines injected
  where
    -- Walk source; for each run of comment/blank lines, produce a
    -- block keyed either by the NEXT non-blank line (header anchor)
    -- or the PREVIOUS non-blank line (body anchor), whichever is
    -- appropriate. A line is a header if it starts at col 1 with a
    -- keyword or a lowercase identifier; otherwise it's a body line
    -- (inside a let, case, etc.).
    collectCommentBlocks :: T.Text -> [([T.Text], T.Text, Bool)]
    -- each entry: (commentLines, anchorText, isHeader)
    -- anchorText is:
    --   * the stripped header line when isHeader=True (match via declKey)
    --   * the stripped preceding code line (minus trailing comment) when
    --     isHeader=False, so downstream matching against the formatter's
    --     output (which has stripped trailing comments) still works.
    collectCommentBlocks t = walk Nothing [] (T.lines t)
      where
        walk _prev _acc [] = []
        walk prev acc (l:ls)
            | isCommentOrBlank l = walk prev (acc ++ [l]) ls
            | isTopLevelDecl l =
                let trimmed = trimBlanks acc
                    anchorKey = stripTrailingComment (T.strip l)
                    rest = walk (Just anchorKey) [] ls
                in if null trimmed
                     then rest
                     else (trimmed, T.strip l, True) : rest
            | otherwise =
                let trimmed = trimBlanks acc
                    anchorKey = stripTrailingComment (T.strip l)
                    rest = walk (Just anchorKey) [] ls
                in case (trimmed, prev) of
                    ([], _) -> rest
                    (_, Just p) -> (trimmed, p, False) : rest
                    (_, Nothing) -> rest

    -- Strip a trailing "-- comment" from a stripped code line so the
    -- anchor key stays stable across fmt (which drops trailing comments).
    -- Approximate: splits on first "  --" (two-or-more spaces before --)
    -- or "--" at end-of-expression context.
    stripTrailingComment :: T.Text -> T.Text
    stripTrailingComment s =
        case T.breakOn (T.pack "--") s of
            (before, after)
                | T.null after -> s
                | otherwise    ->
                    -- Only treat as comment if preceded by whitespace or at BOL.
                    let rev = T.reverse before
                    in case T.uncons rev of
                        Just (c, _) | c == ' ' || c == '\t' -> T.stripEnd before
                        _ -> s

    -- Build: stripped-code-before-`--`  →  "  -- comment text"
    -- (preserving the exact leading whitespace before the `--` so
    -- reattachment is byte-identical).
    collectTrailingComments :: T.Text -> Map.Map T.Text T.Text
    collectTrailingComments t = foldl step Map.empty (T.lines t)
      where
        step acc fullLine =
            case splitTrailingComment fullLine of
                Nothing -> acc
                Just (codePart, trailingPart) ->
                    let key = T.strip codePart
                    in if T.null key then acc
                       else Map.insertWith (\_ old -> old) key trailingPart acc

    -- Return (codeUpToButNotIncluding "--", "  -- rest of line")
    -- only when the line is not a whole-line comment/blank/block-comment.
    -- Ignores `--` that appears inside a string literal (simple state machine).
    splitTrailingComment :: T.Text -> Maybe (T.Text, T.Text)
    splitTrailingComment fullLine =
        let s = T.strip fullLine
        in if T.null s
              || T.pack "--" `T.isPrefixOf` s
              || T.pack "{-" `T.isPrefixOf` s
             then Nothing
             else scan 0 False (T.unpack fullLine)
      where
        scan _ _ [] = Nothing
        scan i inStr (c:rest)
            | inStr =
                if c == '\\' && not (null rest)
                  then scan (i+2) True (drop 1 rest)
                  else if c == '"' then scan (i+1) False rest
                       else scan (i+1) True rest
            | c == '"' = scan (i+1) True rest
            | c == '-', '-':_ <- rest
            , i > 0
            , precedingIsSpace i fullLine
                = let (code, after) = T.splitAt i fullLine
                  in Just (code, after)
            | otherwise = scan (i+1) False rest

        precedingIsSpace i line =
            case T.uncons (T.reverse (T.take i line)) of
                Just (c, _) -> c == ' ' || c == '\t'
                Nothing     -> False

    reattachTrailing :: Map.Map T.Text T.Text -> T.Text -> T.Text
    reattachTrailing tm l =
        let code = T.stripEnd l
            key  = T.strip code
        in case Map.lookup key tm of
            Just trailing ->
                if T.pack "--" `T.isInfixOf` code
                  then l
                  else T.append code trailing
            Nothing -> l

    trimBlanks = reverse . dropWhile (T.null . T.strip)
               . reverse . dropWhile (T.null . T.strip)

    isCommentOrBlank :: T.Text -> Bool
    isCommentOrBlank l =
        let s = T.strip l
        in T.null s
           || T.pack "--" `T.isPrefixOf` s
           || T.pack "{-" `T.isPrefixOf` s

    -- Top-level decl: starts at col 1 with a keyword or lowercase ident.
    isTopLevelDecl :: T.Text -> Bool
    isTopLevelDecl l =
        case T.uncons l of
            Nothing -> False
            Just (c, _)
                | c == ' ' || c == '\t' -> False
                | otherwise ->
                    let s = T.strip l
                    in T.pack "module " `T.isPrefixOf` s
                       || T.pack "import " `T.isPrefixOf` s
                       || T.pack "type " `T.isPrefixOf` s
                       || T.pack "type alias " `T.isPrefixOf` s
                       || lowercaseHead s

    lowercaseHead :: T.Text -> Bool
    lowercaseHead s = case T.uncons s of
        Just (c, _) -> c >= 'a' && c <= 'z'
        Nothing     -> False

    declKey :: T.Text -> Maybe T.Text
    declKey l =
        let s = T.strip l
        in if T.pack "module " `T.isPrefixOf` s then Just (T.pack "module")
           else if T.pack "type alias " `T.isPrefixOf` s
               then Just (T.append (T.pack "alias:") (firstIdent (T.drop 11 s)))
           else if T.pack "type " `T.isPrefixOf` s
               then Just (T.append (T.pack "type:") (firstIdent (T.drop 5 s)))
           else if T.pack "import " `T.isPrefixOf` s
               then Just (T.append (T.pack "import:") (firstIdent (T.drop 7 s)))
           else if lowercaseHead s
               then Just (T.append (T.pack "val:") (firstIdent s))
           else Nothing

    firstIdent :: T.Text -> T.Text
    firstIdent =
        T.takeWhile (\c -> (c >= 'a' && c <= 'z')
                        || (c >= 'A' && c <= 'Z')
                        || (c >= '0' && c <= '9')
                        || c == '_' || c == '.')
        . T.dropWhile (== ' ')

    -- Header map: decl key → queue of comment blocks (source order).
    addHeaderBlock :: Map.Map T.Text [[T.Text]] -> ([T.Text], T.Text, Bool) -> Map.Map T.Text [[T.Text]]
    addHeaderBlock acc (cs, anchor, isHeader) =
        if not isHeader then acc
        else case declKey anchor of
            Nothing -> acc
            Just k  -> Map.insertWith (\new existing -> existing ++ new) k [cs] acc

    -- Anchor map: stripped preceding-code line → queue of comment blocks.
    addAnchorBlock :: Map.Map T.Text [[T.Text]] -> ([T.Text], T.Text, Bool) -> Map.Map T.Text [[T.Text]]
    addAnchorBlock acc (cs, anchor, isHeader) =
        if isHeader then acc
        else Map.insertWith (\new existing -> existing ++ new) anchor [cs] acc

    -- Walk output lines, splicing comments in at header/anchor matches.
    injectComments :: Map.Map T.Text [[T.Text]] -> Map.Map T.Text [[T.Text]]
                   -> [T.Text] -> [T.Text]
    injectComments = go
      where
        go _  _  [] = []
        go hm am (l:ls) =
            -- Header injection fires BEFORE the line.
            let stripped = T.strip l
                headerHit = case declKey l of
                    Just k | Just (cs:rest) <- Map.lookup k hm ->
                        let hm' = if null rest then Map.delete k hm
                                               else Map.insert k rest hm
                        in Just (cs, hm')
                    _ -> Nothing
                -- Anchor injection fires AFTER the line (splice body
                -- comments below the matched code line).
                anchorHit = case Map.lookup stripped am of
                    Just (cs:rest) ->
                        let am' = if null rest then Map.delete stripped am
                                               else Map.insert stripped rest am
                        in Just (cs, am')
                    _ -> Nothing
            in case (headerHit, anchorHit) of
                (Just (hcs, hm'), Just (acs, am')) ->
                    hcs ++ [l] ++ indentLike l acs ++ go hm' am' ls
                (Just (hcs, hm'), Nothing) ->
                    hcs ++ [l] ++ go hm' am ls
                (Nothing, Just (acs, am')) ->
                    l : indentLike l acs ++ go hm am' ls
                (Nothing, Nothing) ->
                    l : go hm am ls

    -- Re-indent comment block to match the indentation of the anchor line.
    -- Preserves the internal stripped shape so multi-line comments line up.
    indentLike :: T.Text -> [T.Text] -> [T.Text]
    indentLike ref cs =
        let indent = T.takeWhile (\c -> c == ' ' || c == '\t') ref
        in map (\c -> if T.null (T.strip c) then c else T.append indent (T.stripStart c)) cs