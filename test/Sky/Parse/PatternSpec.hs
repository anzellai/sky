module Sky.Parse.PatternSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing, doesFileExist)
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

-- P1 regression: the fixture exercises negative-int patterns in case arms
-- and let-bindings-with-params after multi-line case (CLAUDE.md KL #5 + #9).
-- If either parser hole regresses, this test flips red.
spec :: Spec
spec = do
    describe "parser P1 regression" $ do
        it "compiles and runs the P1 fixture" $ do
            sky <- findSky
            cwd <- getCurrentDirectory
            fixture <- readFile (cwd </> "test" </> "fixtures" </> "parser" </> "P1_regression.sky")
            withSystemTempDirectory "sky-p1" $ \tmp -> do
                createDirectoryIfMissing True (tmp </> "src")
                writeFile (tmp </> "sky.toml")
                    "name = \"p1\"\nversion = \"0.0.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n"
                writeFile (tmp </> "src" </> "Main.sky") fixture
                let build = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (ec, _o, _e) <- readCreateProcessWithExitCode build ""
                ec `shouldBe` ExitSuccess
                -- and it should run
                let run = (proc (tmp </> "sky-out" </> "app") []) { cwd = Just tmp }
                (ec2, out2, _e2) <- readCreateProcessWithExitCode run ""
                ec2 `shouldBe` ExitSuccess
                out2 `shouldContain` "minus-one"
                out2 `shouldContain` "other-3"
