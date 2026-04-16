module Sky.Canonicalise.UnboundSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         copyFile, doesFileExist, listDirectory)
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
    mapM_ copyEntry entries
  where
    copyEntry e = do
        let s = src </> e
            d = dst </> e
        isF <- doesFileExist s
        if isF
            then copyFile s d
            else copyTree s d


spec :: Spec
spec = do
    describe "Canonicaliser rejects undefined names at the Sky layer" $ do
        it "rejects a typo (`messgae`) with a user-facing Sky error, not a Go error" $ do
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "unbound"
            withSystemTempDirectory "sky-unbound" $ \tmp -> do
                copyTree fixtureRoot tmp
                let cp = (proc sky ["check", "src/Main.sky"]) { cwd = Just tmp }
                (ec, out, err) <- readCreateProcessWithExitCode cp ""
                ec `shouldNotBe` ExitSuccess
                let combined = out ++ err
                -- The user-facing signal: the typo is named + positioned.
                combined `shouldSatisfy` \s -> "Undefined name" `isInfixOf` s
                combined `shouldContain` "messgae"
                -- And the error surface is Sky's, NOT Go's fallback message.
                combined `shouldSatisfy` \s ->
                    not ("compiler-side bug" `isInfixOf` s)
