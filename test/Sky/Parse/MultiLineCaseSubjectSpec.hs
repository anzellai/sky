module Sky.Parse.MultiLineCaseSubjectSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


-- Regression: prior to the v0.14 parser fix, `case <multi-line
-- subject>` followed by `of` on a fresh line failed to parse
-- because `exprCase` only consumed horizontal whitespace between
-- the subject and the `of` keyword. The user-visible symptom was
-- a confusing "Top-level declaration expected" error pointing at
-- the case-following branch line.
--
-- Fix landed in `src/Sky/Parse/Expression.hs` — replaced `spaces`
-- with `freshLine` before the `of` keyword. Safe because `of` is
-- a reserved keyword that never starts a top-level declaration.
spec :: Spec
spec = describe "parser accepts multi-line case subject + `of` on fresh line" $ do
    it "compiles a `case Result.mapError ... \\n of`-shaped body" $ do
        sky <- findSky
        withSystemTempDirectory "sky-multiline-case" $ \tmp -> do
            writeFixture tmp fixture
            (ec, _, _err) <- runSky sky ["build", "src/Main.sky"] tmp
            ec `shouldBe` ExitSuccess
            built <- doesFileExist (tmp </> "sky-out" </> "app")
            built `shouldBe` True

  where
    findSky :: IO FilePath
    findSky = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok then return candidate
              else fail ("sky binary missing at " ++ candidate)

    runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
    runSky sky args workDir = do
        let cp = (proc sky args) { cwd = Just workDir }
        readCreateProcessWithExitCode cp ""

    writeFixture :: FilePath -> String -> IO ()
    writeFixture dir body = do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml")
            ("name = \"multiline-case\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") body


fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Result as Result"
    , "import Sky.Core.Error as Error"
    , "import Std.Log exposing (println)"
    , ""
    , "main ="
    , "    case Result.mapError"
    , "            (\\_ -> Error.unexpected \"wrapped\")"
    , "            (Err (Error.unexpected \"inner\"))"
    , "    of"
    , "        Err e ->"
    , "            println (\"Err: \" ++ Error.toString e)"
    , ""
    , "        Ok _ ->"
    , "            println \"Ok\""
    ]
