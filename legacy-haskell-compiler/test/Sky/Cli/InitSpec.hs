module Sky.Cli.InitSpec (spec) where

-- `sky init <name>` scaffolds a project and the scaffold MUST build
-- clean — otherwise new users hit a wall on their very first command.
-- This spec runs init in a tmpdir, asserts the expected files
-- materialise, and runs `sky build` on the scaffolded source.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
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


spec :: Spec
spec = do
    describe "sky init" $ do

        it "scaffolds a project with sky.toml + src/Main.sky" $ do
            sky <- findSky
            withSystemTempDirectory "sky-init" $ \tmp -> do
                (ec, _, _) <- readCreateProcessWithExitCode
                    (proc sky ["init", "myapp"]) { cwd = Just tmp } ""
                ec `shouldBe` ExitSuccess
                doesFileExist (tmp </> "myapp" </> "sky.toml") `shouldReturn` True
                doesFileExist (tmp </> "myapp" </> "src" </> "Main.sky") `shouldReturn` True

        it "scaffolded project builds clean" $ do
            -- The end-to-end contract: a fresh `sky init` produces
            -- a project that `sky build` accepts. If init's template
            -- ever drifts (e.g. references a stdlib symbol that no
            -- longer exists), this fails immediately.
            sky <- findSky
            withSystemTempDirectory "sky-init-build" $ \tmp -> do
                (ecInit, _, _) <- readCreateProcessWithExitCode
                    (proc sky ["init", "buildable"]) { cwd = Just tmp } ""
                ecInit `shouldBe` ExitSuccess
                let projectDir = tmp </> "buildable"
                (ecBuild, _, errBuild) <- readCreateProcessWithExitCode
                    (proc sky ["build", "src/Main.sky"]) { cwd = Just projectDir } ""
                case ecBuild of
                    ExitSuccess -> return ()
                    _ -> expectationFailure $
                        "scaffolded project failed to build:\n" ++ errBuild
