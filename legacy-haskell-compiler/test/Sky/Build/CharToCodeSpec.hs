module Sky.Build.CharToCodeSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import qualified Data.List as List


-- v0.16.7 #419 — `Sky.Core.Char.toCode : Char -> Int` and its
-- partner `fromCode : Int -> Char` were missing from the stdlib
-- surface, so any Sky program reaching for the code-point of a
-- character had to drop into a kernel FFI or hand-write the
-- conversion.  This spec asserts:
--
--   1. The module exposes both names (canonicaliser resolves them).
--   2. The runtime kernels exist (rt.Char_toCode / rt.Char_fromCode).
--   3. The compiled binary produces the right values at runtime:
--      `Char.toCode 'A'  = 65`
--      `Char.fromCode 65 = 'A'`
--      `Char.fromCode (Char.toCode 'é') = 'é'`   (Unicode round-trip)
--      `Char.fromCode 0x110000 = '\\ufffd'`       (clamp to U+FFFD)
spec :: Spec
spec = describe "v0.16.7 #419 — Sky.Core.Char.toCode / fromCode" $ do
    it "exposes toCode + fromCode and round-trips at runtime" $ do
        sky <- findSky
        withSystemTempDirectory "sky-char-tocode" $ \tmp -> do
            writeFixture tmp
            (ec, _, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            case ec of
                ExitFailure _ ->
                    expectationFailure ("sky build failed:\n" ++ errOut)
                ExitSuccess -> return ()
            built <- doesFileExist (tmp </> "sky-out" </> "app")
            built `shouldBe` True

            -- Run the binary and validate the round-trip values.
            (rc, stdoutS, _) <- readCreateProcessWithExitCode
                (proc (tmp </> "sky-out" </> "app") []) { cwd = Just tmp } ""
            rc `shouldBe` ExitSuccess
            ("toCode 'A' = 65" `List.isInfixOf` stdoutS) `shouldBe` True
            ("fromCode 65 = A" `List.isInfixOf` stdoutS) `shouldBe` True
            -- Round-trip on a multi-byte rune (U+00E9 = 233 = 'é').
            ("roundtrip 'é' = é" `List.isInfixOf` stdoutS) `shouldBe` True
            -- Out-of-range clamp.  U+110000 is the first code point
            -- above the Unicode maximum (U+10FFFF) — must yield the
            -- replacement character.
            ("clamp 0x110000 = \xfffd" `List.isInfixOf` stdoutS)
                `shouldBe` True

  where
    findSky :: IO FilePath
    findSky = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok then return candidate
              else fail ("sky binary missing at " ++ candidate)

    runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
    runSky sky args workDir =
        readCreateProcessWithExitCode
            (proc sky args) { cwd = Just workDir } ""

    writeFixture :: FilePath -> IO ()
    writeFixture dir = do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml")
            ("name = \"char-tocode\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") fixture


fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Char as Char"
    , "import Std.Log exposing (println)"
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            println"
    , "                (\"toCode 'A' = \" ++ String.fromInt (Char.toCode 'A'))"
    , ""
    , "        _ ="
    , "            println"
    , "                (\"fromCode 65 = \" ++ String.fromChar (Char.fromCode 65))"
    , ""
    , "        _ ="
    , "            println"
    , "                (\"roundtrip 'é' = \""
    , "                    ++ String.fromChar (Char.fromCode (Char.toCode 'é')))"
    , ""
    , "        _ ="
    , "            println"
    , "                (\"clamp 0x110000 = \""
    , "                    ++ String.fromChar (Char.fromCode 1114112))"
    , "    in"
    , "    println \"done\""
    ]
