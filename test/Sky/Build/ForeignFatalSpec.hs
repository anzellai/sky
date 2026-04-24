module Sky.Build.ForeignFatalSpec (spec) where

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
    describe "Foreign-call mismatches are fatal (v0.10.0+)" $ do
        it "build aborts with a Type mismatch when an FFI / kernel return-shape doesn't unify" $ do
            -- Pre-fix the solver swallowed CForeign mismatches with
            -- the comment `Continue past foreign mismatch for now`.
            -- The bug shipped as `rt.AsBool: expected bool, got
            -- rt.SkyResult[…]` runtime panics in sky-log's
            -- Webhook.matchesFilter — `Regexp.regexpMatchString`
            -- returned `Result Error Bool` but was used as bare
            -- `Bool`. Now the build aborts with a TYPE ERROR.
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "foreign-fatal"
            withSystemTempDirectory "sky-ff" $ \tmp -> do
                copyTree fixtureRoot tmp
                let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (ec, out, err) <- readCreateProcessWithExitCode cp ""
                let combined = out ++ err
                ec `shouldNotBe` ExitSuccess
                combined `shouldSatisfy` ("TYPE ERROR" `isInfixOf`)
                -- One of these phrases must appear — either the
                -- direct Foreign mismatch report, or the equivalent
                -- Type mismatch surfaced via the CEqual constraint
                -- on the same expression.
                combined `shouldSatisfy` \s ->
                    "Foreign" `isInfixOf` s
                    || "Type mismatch" `isInfixOf` s
                appExists <- doesFileExist (tmp </> "sky-out" </> "app")
                appExists `shouldBe` False
