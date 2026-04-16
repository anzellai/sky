module Sky.Cli.TestSpec (spec) where

-- `sky test <file>` contract: exit code reflects pass/fail. A
-- passing test module exits 0; a failing test module exits
-- non-zero. This locks the invariant so CI wrappers can depend
-- on the exit code.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


setupTestProject :: FilePath -> String -> IO ()
setupTestProject dir testModBody = do
    -- sky test infers the test module name from the file path
    -- relative to src/ or tests/. We place the fixture under
    -- tests/ and configure sky.toml so the entry writer knows
    -- where src/ lives.
    createDirectoryIfMissing True (dir </> "src")
    createDirectoryIfMissing True (dir </> "tests")
    writeFile (dir </> "sky.toml") $ unlines
        [ "name = \"test-spec\""
        , "entry = \"src/Main.sky\""
        ]
    -- Minimal dummy main so the project is valid.
    writeFile (dir </> "src" </> "Main.sky") $ unlines
        [ "module Main exposing (main)"
        , "import Std.Log exposing (println)"
        , "main = println \"placeholder\""
        ]
    writeFile (dir </> "tests" </> "SampleTest.sky") testModBody


passingBody :: String
passingBody = unlines
    [ "module SampleTest exposing (tests)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Test as Test exposing (Test)"
    , ""
    , "tests : List Test"
    , "tests ="
    , "    [ Test.test \"trivially passes\" (\\_ -> Test.pass)"
    , "    ]"
    ]


failingBody :: String
failingBody = unlines
    [ "module SampleTest exposing (tests)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Test as Test exposing (Test)"
    , ""
    , "tests : List Test"
    , "tests ="
    , "    [ Test.test \"intentionally fails\" (\\_ -> Test.fail \"nope\")"
    , "    ]"
    ]


spec :: Spec
spec = do
    describe "sky test" $ do

        it "passing test module exits 0" $ do
            sky <- findSky
            withSystemTempDirectory "sky-test-pass" $ \dir -> do
                setupTestProject dir passingBody
                (ec, _out, _err) <- readCreateProcessWithExitCode
                    (proc sky ["test", "tests/SampleTest.sky"]) { cwd = Just dir } ""
                ec `shouldBe` ExitSuccess

        it "failing test module exits non-zero" $ do
            sky <- findSky
            withSystemTempDirectory "sky-test-fail" $ \dir -> do
                setupTestProject dir failingBody
                (ec, _out, _err) <- readCreateProcessWithExitCode
                    (proc sky ["test", "tests/SampleTest.sky"]) { cwd = Just dir } ""
                case ec of
                    ExitFailure _ -> return ()
                    _ -> expectationFailure $
                        "failing test should exit non-zero, got " ++ show ec
