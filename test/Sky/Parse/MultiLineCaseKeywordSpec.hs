module Sky.Parse.MultiLineCaseKeywordSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


-- Cycle 4 D4 regression: a `case` keyword on its own line with
-- the subject on the next line failed to parse.  The companion
-- `MultiLineCaseSubjectSpec` only covered the `<subject>\n of`
-- transition; D4 is the OTHER half — the `case\n <subject>`
-- transition.  The fix mirrors `exprLet`: insert
-- `freshLine mkError` at the head of `exprCase` so the parser
-- skips the optional newline between the `case` keyword and the
-- subject expression.
spec :: Spec
spec = describe "parser accepts `case` keyword with subject on next line" $ do
    it "compiles a `case\\n    subject\\n    of` body" $ do
        sky <- findSky
        withSystemTempDirectory "sky-multiline-case-kw" $ \tmp -> do
            writeFixture tmp fixture
            (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
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
            ("name = \"multiline-case-kw\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") body


fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "classify : Int -> Int -> Int -> String"
    , "classify a b c ="
    , "    case"
    , "        (a, b, c)"
    , "    of"
    , "        (0, 0, 0) -> \"zeros\""
    , "        _ -> \"other\""
    , ""
    , "main ="
    , "    println (classify 1 2 3)"
    ]
