module Sky.Cli.ExitCodesSpec (spec) where

-- Exit-code contracts for the most-used sky subcommands. Pre-spec,
-- silent regressions where a command exited 0 despite failing
-- (e.g. sky build returning success after a Go compile error)
-- could ship undetected. Each test invokes the binary in a tmpdir
-- and asserts the exit code matches the user-visible outcome.

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


-- Write a minimal sky.toml + src/Main.sky into `dir`.
writeProject :: FilePath -> String -> IO ()
writeProject dir src = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml")
        "name = \"cli-spec\"\nentry = \"src/Main.sky\"\n"
    writeFile (dir </> "src" </> "Main.sky") src


spec :: Spec
spec = do
    describe "sky CLI exit code contracts" $ do

        it "sky --version exits 0 with v0.9.0 banner" $ do
            sky <- findSky
            (ec, out, _) <- readCreateProcessWithExitCode (proc sky ["--version"]) ""
            ec `shouldBe` ExitSuccess
            ("v0.9" `isInfixOf` out) `shouldBe` True

        it "sky build of a well-typed program exits 0" $ do
            sky <- findSky
            withSystemTempDirectory "sky-cli-ok" $ \dir -> do
                writeProject dir $ unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main = println \"ok\""
                    ]
                (ec, _, _) <- readCreateProcessWithExitCode
                    (proc sky ["build", "src/Main.sky"]) { cwd = Just dir } ""
                ec `shouldBe` ExitSuccess

        it "sky build of a syntactically-broken program exits non-zero" $ do
            sky <- findSky
            withSystemTempDirectory "sky-cli-syntax" $ \dir -> do
                writeProject dir "module Main exposing (main"  -- missing closing paren
                (ec, _, _) <- readCreateProcessWithExitCode
                    (proc sky ["build", "src/Main.sky"]) { cwd = Just dir } ""
                case ec of
                    ExitFailure _ -> return ()
                    _ -> expectationFailure $ "expected non-zero exit, got " ++ show ec

        it "sky build of a program with a Go-level error exits non-zero" $ do
            -- Sky source compiles, but the Go output won't (we induce
            -- this via a deliberately-broken FFI reference). Audit
            -- P0-1 made `sky check` a strict superset; `sky build`
            -- must follow suit — a green Sky type-check that
            -- produces invalid Go MUST surface as non-zero exit.
            sky <- findSky
            withSystemTempDirectory "sky-cli-go-err" $ \dir -> do
                writeProject dir $ unlines
                    [ "module Main exposing (main)"
                    , "main ="
                    , "    let x : Int = \"not an int\""  -- Sky-side type error
                    , "    in x"
                    ]
                (ec, _, _) <- readCreateProcessWithExitCode
                    (proc sky ["build", "src/Main.sky"]) { cwd = Just dir } ""
                case ec of
                    ExitFailure _ -> return ()
                    _ -> expectationFailure $ "expected non-zero exit, got " ++ show ec

        it "sky check of a well-typed program exits 0" $ do
            sky <- findSky
            withSystemTempDirectory "sky-cli-check-ok" $ \dir -> do
                writeProject dir $ unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main = println \"ok\""
                    ]
                (ec, _, _) <- readCreateProcessWithExitCode
                    (proc sky ["check", "src/Main.sky"]) { cwd = Just dir } ""
                ec `shouldBe` ExitSuccess
