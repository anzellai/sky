module Sky.Build.DepHmFatalSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         copyFile, doesFileExist, listDirectory, doesDirectoryExist)
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


copyTree :: FilePath -> FilePath -> IO ()
copyTree src dst = do
    createDirectoryIfMissing True dst
    entries <- listDirectory src
    mapM_ (\e -> do
        let s = src </> e
            d = dst </> e
        isF <- doesFileExist s
        if isF
            then copyFile s d
            else do
                isD <- doesDirectoryExist s
                if isD then copyTree s d else return ()) entries


spec :: Spec
spec = do
    describe "Dep module HM errors are fatal (v0.10.0+)" $ do
        it "blocks the build with TYPE ERROR (Mod): … when a dep module fails HM in pass 2" $ do
            -- Pre-fix the dep silently degraded to `any`-typed bindings
            -- and the entry consumed broken values at runtime.
            -- Symptom in sendcrafts: `[AUTH] Admin ensured: 0x102…`
            -- — the func-pointer of an unforced Task thunk being
            -- string-split. This guards the regression.
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "dep-hm-fatal"
            withSystemTempDirectory "sky-dep-hm" $ \tmp -> do
                copyTree fixtureRoot tmp
                let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (ec, out, err) <- readCreateProcessWithExitCode cp ""
                let combined = out ++ err
                ec `shouldNotBe` ExitSuccess
                combined `shouldSatisfy` ("TYPE ERROR (Lib.Config)" `isInfixOf`)
                combined `shouldSatisfy` \s ->
                    "Task Error String" `isInfixOf` s
                    || "Type mismatch" `isInfixOf` s
                -- The fatal label tells the user this isn't a warning.
                combined `shouldSatisfy` ("fatal" `isInfixOf`)
                -- And no `app` binary should have been produced.
                appExists <- doesFileExist (tmp </> "sky-out" </> "app")
                appExists `shouldBe` False
