module Sky.Cli.RunSpec (spec) where

-- `sky run` builds + executes the resulting binary. The contract:
-- the wrapper's exit code MUST be the underlying app's exit code,
-- so `case Process.exit 42` in user code surfaces as `sky run`
-- exiting 42. Without this, scripts wrapping `sky run` lose
-- meaningful exit codes.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


writeProject :: FilePath -> String -> IO ()
writeProject dir src = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml")
        "name = \"run-spec\"\nentry = \"src/Main.sky\"\n"
    writeFile (dir </> "src" </> "Main.sky") src


spec :: Spec
spec = do
    describe "sky run" $ do

        it "executes the built binary and emits its stdout" $ do
            sky <- findSky
            withSystemTempDirectory "sky-run" $ \tmp -> do
                writeProject tmp $ unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main = println \"hello-from-run\""
                    ]
                (ec, out, _) <- readCreateProcessWithExitCode
                    (proc sky ["run", "src/Main.sky"]) { cwd = Just tmp } ""
                ec `shouldBe` ExitSuccess
                ("hello-from-run" `isInfixOf` out) `shouldBe` True
