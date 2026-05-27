module Sky.Parse.RowPolyRecordAnnotationSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


-- Cycle 4 D6 regression: a row-polymorphic record annotation
--     greet : { r | name : String } -> String
-- previously failed to parse — the AST slot
-- `Src.TRecord [...] (Maybe String)`, canonicaliser,
-- `Instantiate.typeToVariable T.TRecord`, AND `Unify.unifyRecords`
-- all already supported row polymorphism, but the SURFACE PARSER
-- in `Sky.Parse.Type` had no `{ r | ... }` rule.
--
-- The fix adds a non-consuming lookahead (`peekRowPolyIntro`)
-- followed by a row-poly branch that emits
-- `Src.TRecord fields (Just rowVar)`.  This spec exercises BOTH:
--   1. the row-poly annotation parses cleanly, AND
--   2. the open-record semantics flow through HM correctly — a
--      caller passing a record with EXTRA fields beyond `name`
--      type-checks (closed-record would reject it).
spec :: Spec
spec = describe "parser accepts row-polymorphic record annotations" $ do
    it "type-checks `{ r | name : String }` and accepts extra fields" $ do
        sky <- findSky
        withSystemTempDirectory "sky-row-poly" $ \tmp -> do
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
            ("name = \"row-poly\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") body


fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "-- D6: row-polymorphic annotation accepts records carrying extras"
    , "greet : { r | name : String } -> String"
    , "greet rec ="
    , "    \"Hello, \" ++ rec.name"
    , ""
    , "main ="
    , "    println (greet { name = \"World\", age = 99 })"
    ]
