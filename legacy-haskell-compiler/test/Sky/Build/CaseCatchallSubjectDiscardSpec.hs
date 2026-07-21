module Sky.Build.CaseCatchallSubjectDiscardSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import qualified Data.List as List


-- Cycle 4 D2 regression: a `case foo of _ -> Ok ()` (where the
-- subject is otherwise unused) used to emit Go that declared
-- `__subject := foo` and never read it.  Go's `declared and not
-- used` build error rejected the binary.  The fix in
-- `Sky.Build.Compile.caseToGo` emits a blank-identifier discard
-- (`_ = __subject`) immediately after the subject declaration so
-- the catchall-only shape compiles.
--
-- This spec asserts BOTH:
--   1. the fixture builds clean (Go would have rejected the
--      pre-fix codegen), AND
--   2. the emitted Go contains the discard line
--      (`_ = __subject`) so a future refactor that drops the
--      discard regresses this test.
spec :: Spec
spec = describe "case _ -> Ok () compiles + emits the subject discard" $ do
    it "builds and emits `_ = __subject` for a catchall-only case" $ do
        sky <- findSky
        withSystemTempDirectory "sky-case-catchall" $ \tmp -> do
            writeFixture tmp fixture
            (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
            ec `shouldBe` ExitSuccess
            built <- doesFileExist (tmp </> "sky-out" </> "app")
            built `shouldBe` True
            goSrc <- readFile (tmp </> "sky-out" </> "main.go")
            (List.isInfixOf "_ = __subject" goSrc) `shouldBe` True

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
            ("name = \"case-catchall\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") body


fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "checkValue : Int -> Result Error ()"
    , "checkValue n ="
    , "    case n of"
    , "        _ -> Ok ()"
    , ""
    , "main ="
    , "    case checkValue 5 of"
    , "        Ok () -> println \"ok\""
    , "        Err _ -> println \"err\""
    ]
