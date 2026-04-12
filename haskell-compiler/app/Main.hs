module Main where

import Options.Applicative
import System.Exit (exitFailure, exitSuccess)
import System.IO (hPutStrLn, stderr)
import System.Directory (createDirectoryIfMissing, doesFileExist)
import System.Process (callProcess)

import qualified Sky.Build.Compile as Compile
import qualified Sky.Sky.Toml as Toml


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
        putStrLn $ "Checking " ++ path ++ "..."
        -- TODO: parse + typecheck only
        putStrLn "Check not yet implemented"
        return (Right ())

    Fmt path -> do
        putStrLn $ "Formatting " ++ path ++ "..."
        -- TODO: Format.Format.format
        putStrLn "Format not yet implemented"
        return (Right ())

    Init mName -> do
        let name = maybe "sky-project" id mName
        putStrLn $ "Initialising project: " ++ name
        -- TODO: create sky.toml, src/Main.sky, .gitignore, CLAUDE.md
        putStrLn "Init not yet implemented"
        return (Right ())

    Add pkg -> do
        putStrLn $ "Adding " ++ pkg ++ "..."
        putStrLn "Add not yet implemented"
        return (Right ())

    Remove pkg -> do
        putStrLn $ "Removing " ++ pkg ++ "..."
        putStrLn "Remove not yet implemented"
        return (Right ())

    Install -> do
        putStrLn "Installing dependencies..."
        putStrLn "Install not yet implemented"
        return (Right ())

    Lsp -> do
        putStrLn "Starting language server..."
        -- TODO: Lsp.Server.run
        putStrLn "LSP not yet implemented"
        return (Right ())

    Upgrade -> do
        putStrLn "Upgrade not yet implemented"
        return (Right ())
