module Sky.Build.CryptoAeadSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- v0.15.44 — Sky-source declarations of AES-GCM / ChaCha20 /
-- Bytes / Task.retryWith must dispatch to the runtime kernels
-- and round-trip end-to-end.
spec :: Spec
spec = describe "v0.15.44 symmetric crypto + retry combinator" $ do
    it "aesGcm + chacha20 + retryWith build and run end-to-end" $ do
        sky <- findSky
        withSystemTempDirectory "sky-crypto-aead" $ \tmp -> do
            writeFixture tmp
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
              then expectationFailure $
                  "sky build failed.\n" ++ out ++ "\n" ++ errOut
              else do
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- The user binding must dispatch to the runtime AEAD kernels.
                ("Crypto_aesGcmEncrypt" `isInfixOf` body) `shouldBe` True
                ("Crypto_chacha20Encrypt" `isInfixOf` body) `shouldBe` True
                ("Task_retryWith" `isInfixOf` body) `shouldBe` True
                (rc, runOut, _) <- runApp tmp
                rc `shouldBe` ExitSuccess
                -- Round-trip succeeded if main prints the recovered text.
                ("hello-world" `isInfixOf` runOut) `shouldBe` True
                -- Retry combinator returns the success body.
                ("retry-ok" `isInfixOf` runOut) `shouldBe` True

  where
    findSky = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok then return candidate
              else fail ("sky binary missing at " ++ candidate)

    runSky sky args workDir = do
        let cp = (proc sky args) { cwd = Just workDir }
        readCreateProcessWithExitCode cp ""

    runApp dir = do
        let cp = (proc (dir </> "sky-out" </> "app") []) { cwd = Just dir }
        readCreateProcessWithExitCode cp ""

    writeFixture dir = do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml") $ unlines
            [ "[project]"
            , "name = \"crypto-aead-test\""
            , ""
            , "[bin]"
            , "name = \"app\""
            ]
        writeFile (dir </> "src" </> "Main.sky") $ unlines
            [ "module Main exposing (main)"
            , ""
            , "import Sky.Core.Prelude exposing (..)"
            , "import Sky.Core.Crypto as Crypto"
            , "import Sky.Core.Task as Task"
            , "import Std.Log exposing (println)"
            , ""
            , ""
            , "main ="
            , "    let"
            , "        key = Crypto.aesKeyFromPassword \"pw\" \"salt\""
            , "        encResult = Crypto.aesGcmEncrypt key \"hello-world\""
            , "        roundTrip ="
            , "            case encResult of"
            , "                Ok ct ->"
            , "                    case Crypto.aesGcmDecrypt key ct of"
            , "                        Ok pt -> pt"
            , "                        Err _ -> \"err-decrypt\""
            , "                Err _ -> \"err-encrypt\""
            , "        _ = println roundTrip"
            , "        -- chacha smoke"
            , "        ckey = Crypto.chachaKeyFromPassword \"pw\" \"salt\""
            , "        _ = Crypto.chacha20Encrypt ckey \"x\""
            , "        -- retryWith"
            , "        retryResult ="
            , "            Task.retryWith"
            , "                (Task.linearBackoff 2 1)"
            , "                (Task.succeed \"retry-ok\")"
            , "        _ ="
            , "            case Task.perform retryResult of"
            , "                Ok s -> println s"
            , "                Err _ -> println \"err-retry\""
            , "    in"
            , "        Task.succeed ()"
            ]
