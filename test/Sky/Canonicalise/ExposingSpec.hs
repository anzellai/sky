module Sky.Canonicalise.ExposingSpec (spec) where

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
    describe "P2: importing an unexposed name is a canonicalise error" $ do
        it "rejects `import Lib.Hidden exposing (secret)` when secret is package-private" $ do
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "hiding"
            withSystemTempDirectory "sky-p2" $ \tmp -> do
                copyTree fixtureRoot tmp
                let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (ec, out, err) <- readCreateProcessWithExitCode cp ""
                ec `shouldNotBe` ExitSuccess
                let combined = out ++ err
                combined `shouldSatisfy` \s ->
                    ("does not expose" `isInfixOf` s) &&
                    ("secret" `isInfixOf` s)
