module Sky.Build.CompileSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist, removeDirectoryRecursive)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)

-- | Find the built sky compiler binary. Prefers $SKY_BIN, else sky-out/sky
-- relative to the repo root (derived from cwd at test run).
findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    -- cabal test runs from repo root
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
          else fail ("sky binary missing at " ++ candidate
                  ++ " — run scripts/build.sh first")

runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
runSky sky args workDir = do
    let cp = (proc sky args) { cwd = Just workDir }
    readCreateProcessWithExitCode cp ""

writeFixtureProject :: FilePath -> String -> String -> IO ()
writeFixtureProject dir name body = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml")
        ("name = \"" ++ name ++ "\"\nversion = \"0.0.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
    writeFile (dir </> "src" </> "Main.sky") body

spec :: Spec
spec = do
    describe "type errors are fatal" $ do
        it "aborts the build when types don't unify" $ do
            sky <- findSky
            withSystemTempDirectory "sky-type-err" $ \tmp -> do
                writeFixtureProject tmp "type-err" $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "-- Int + String must not unify"
                    , "bad : Int"
                    , "bad = 1 + \"not-an-int\""
                    , ""
                    , "main = println (String.fromInt bad)"
                    ]
                (ec, _out, err) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldNotBe` ExitSuccess
                -- Expect a message mentioning a type error or unification.
                let combined = err
                (any (`isInfixOf` combined)
                    ["TYPE ERROR", "Type error", "Cannot unify",
                     "type error", "cannot unify"])
                    `shouldBe` True

    describe "well-typed programs build" $ do
        it "compiles a trivial hello-world clean-slate" $ do
            sky <- findSky
            withSystemTempDirectory "sky-ok" $ \tmp -> do
                writeFixtureProject tmp "hello" $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println \"ok\""
                    ]
                (ec, _out, _err) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True
