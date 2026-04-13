module Main where

import Options.Applicative
import System.Exit (exitFailure, exitSuccess)
import System.IO (hPutStrLn, stderr)
import qualified System.Directory
import System.Directory (createDirectoryIfMissing, doesFileExist)
import System.Process (callProcess)
import Control.Monad (when)

import qualified Data.Text.IO as TIO
import qualified Sky.Build.Compile as Compile
import qualified Sky.Sky.Toml as Toml
import qualified Sky.Parse.Module as ParseMod
import qualified Sky.Format.Format as Format
import qualified Sky.Lsp.Server as Lsp
import qualified Sky.Build.FfiGen as FfiGen
import qualified Sky.Build.SkyDeps as SkyDeps


-- | Sky compiler CLI
-- Commands: build, run, check, fmt, init, add, remove, install, lsp, upgrade, version
main :: IO ()
main = do
    cmd <- execParser opts
    result <- runCommand cmd
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
        -- Parse + typecheck only (no codegen, no go build)
        result <- Compile.compile config path "sky-out"
        case result of
            Left err -> return (Left err)
            Right _ -> do
                putStrLn "No errors found."
                return (Right ())

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
        -- Create project structure
        createDirectoryIfMissing True (name ++ "/src")
        writeFile (name ++ "/sky.toml") $ unlines
            [ "name = \"" ++ name ++ "\""
            , "version = \"0.1.0\""
            , "entry = \"src/Main.sky\""
            , ""
            , "[source]"
            , "root = \"src\""
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
            ]
        putStrLn $ "Created " ++ name ++ "/"
        putStrLn $ "  sky.toml"
        putStrLn $ "  src/Main.sky"
        putStrLn $ "  .gitignore"
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
        case Toml._skyDeps config of
            [] -> putStrLn "No [dependencies] entries in sky.toml."
            _  -> putStrLn "Sky dependencies installed."
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

    Upgrade -> do
        putStrLn "Upgrade not yet implemented"
        return (Right ())
