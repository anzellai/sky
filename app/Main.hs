{-# LANGUAGE TemplateHaskell #-}
module Main where

import Options.Applicative
import System.Exit (exitFailure, exitSuccess)
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

import qualified Data.Text.IO as TIO
import qualified Sky.Build.Compile as Compile
import qualified Sky.Sky.Toml as Toml
import qualified Sky.Parse.Module as ParseMod
import qualified Sky.Format.Format as Format
import qualified Sky.Lsp.Server as Lsp
import qualified Sky.Build.FfiGen as FfiGen
import qualified Sky.Build.SkyDeps as SkyDeps


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
    | Fmt FilePath
    | Test FilePath
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


commandParser :: Parser Command
commandParser = subparser
    ( command "build"
        (info (Build <$> fileArg) (progDesc "Compile to binary"))
    <> command "run"
        (info (Run <$> fileArg) (progDesc "Build and run"))
    <> command "check"
        (info (Check <$> fileArg) (progDesc "Type-check only"))
    <> command "fmt"
        (info (Fmt <$> fileArg) (progDesc "Format source file"))
    <> command "test"
        (info (Test <$> fileArg) (progDesc "Run a Sky test module (exposing `tests : List Test`)"))
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
        -- Parse + typecheck only (no codegen, no go build)
        result <- Compile.compile config path "sky-out"
        case result of
            Left err -> return (Left err)
            Right _ -> do
                putStrLn "No errors found."
                return (Right ())

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

    Fmt path -> do
        src <- TIO.readFile path
        case ParseMod.parseModule src of
            Left err -> return (Left $ "Parse error: " ++ show err)
            Right srcMod -> do
                let formatted = Format.formatModule srcMod
                writeFile path formatted
                putStrLn $ "Formatted " ++ path
                return (Right ())

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
