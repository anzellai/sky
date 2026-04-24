module Sky.Canonicalise.KernelFallbackSpec (spec) where

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
    mapM_ copyEntry entries
  where
    copyEntry e = do
        let s = src </> e
            d = dst </> e
        isF <- doesFileExist s
        if isF
            then copyFile s d
            else do
                isD <- doesDirectoryExist s
                if isD then copyTree s d else return ()


spec :: Spec
spec = do
    describe "Canonicaliser falls back to kernel registry for unimported qualifiers" $ do
        it "Crypto.sha256 / Encoding.base64Encode used without explicit import compile and run" $ do
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "kernel-fallback"
            withSystemTempDirectory "sky-kfb" $ \tmp -> do
                copyTree fixtureRoot tmp
                let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
                (ec, out, err) <- readCreateProcessWithExitCode cp ""
                let combined = out ++ err
                ec `shouldBe` ExitSuccess
                -- Generated Go must call the kernel through the rt
                -- package — bare `Crypto_sha256(` is the failure mode
                -- the canonicaliser fallback used to ship.
                main_go <- readFile (tmp </> "sky-out" </> "main.go")
                main_go `shouldSatisfy` ("rt.Crypto_sha256(" `isInfixOf`)
                -- Encoding.base64Encode lowers via the typed-kernel
                -- literal-arg path to the `T`-suffix variant
                -- (`Encoding_base64EncodeT`), so accept either form
                -- — the bug-shape we guard against is the missing
                -- `rt.` prefix, not the `T` suffix.
                main_go `shouldSatisfy` \s ->
                    "rt.Encoding_base64Encode" `isInfixOf` s
                -- Defence in depth: the bare-name form must be absent
                -- (a regression would emit `Crypto_sha256(` somewhere).
                main_go `shouldSatisfy` \s -> not (" Crypto_sha256(" `isInfixOf` s)
                                           && not ("(Crypto_sha256(" `isInfixOf` s)
                                           && not ("=Crypto_sha256(" `isInfixOf` s)
                -- And the binary actually runs.
                let runApp = (proc (tmp </> "sky-out" </> "app") []) { cwd = Just tmp }
                (rec_, rout, rerr) <- readCreateProcessWithExitCode runApp ""
                let rcombined = rout ++ rerr
                rec_ `shouldBe` ExitSuccess
                -- Crypto.sha256 "hello" → 2cf24dba5fb0a30e... (12-char prefix).
                rcombined `shouldSatisfy` ("2cf24dba5fb0" `isInfixOf`)
