module Sky.Parse.MultiLineSignatureSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


-- Regression: prior to the 2026-05-23 parser fix, a top-level
-- declaration whose `:` lived on a continuation line failed to
-- parse with a "Top-level declaration expected" error. The user
-- pattern that hit this was a wide signature where the type
-- spilled onto its own line for readability:
--
--   greeting
--       : String -> String
--   greeting name = "hello, " ++ name
--
-- Fix landed in `src/Sky/Parse/Declaration.hs` — the value /
-- annotation branch now tries three alternatives via `oneOf`:
-- same-line `:`, continuation-line `:` (freshLine + checkIndent
-- + `:`), then value def. Multi-line `->` continuation INSIDE
-- the type body is still not supported (workaround: extract a
-- type alias).
spec :: Spec
spec = describe "parser accepts multi-line declaration signatures" $ do
    it "compiles `name\\n    : T` (lower-cased declaration)" $ do
        sky <- findSky
        withSystemTempDirectory "sky-multiline-sig-lower" $ \tmp -> do
            writeFixture tmp lowerFixture
            (ec, _, _err) <- runSky sky ["build", "src/Main.sky"] tmp
            ec `shouldBe` ExitSuccess
            built <- doesFileExist (tmp </> "sky-out" </> "app")
            built `shouldBe` True

    it "compiles `Name\\n    : T` (upper-cased declaration)" $ do
        sky <- findSky
        withSystemTempDirectory "sky-multiline-sig-upper" $ \tmp -> do
            writeFixture tmp upperFixture
            (ec, _, _err) <- runSky sky ["build", "src/Main.sky"] tmp
            ec `shouldBe` ExitSuccess

    it "compiles an inline record in a same-line signature" $ do
        sky <- findSky
        withSystemTempDirectory "sky-inline-record-sig" $ \tmp -> do
            writeFixture tmp inlineRecordFixture
            (ec, _, _err) <- runSky sky ["build", "src/Main.sky"] tmp
            ec `shouldBe` ExitSuccess


lowerFixture :: String
lowerFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "greeting"
    , "    : String -> String"
    , "greeting name ="
    , "    \"hello, \" ++ name"
    , ""
    , "main ="
    , "    let _ = println (greeting \"world\")"
    , "    in ()"
    ]


upperFixture :: String
upperFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "type alias Profile = { name : String, age : Int }"
    , ""
    , "Profile"
    , "    : String -> Int -> Profile"
    , "Profile n a = { name = n, age = a }"
    , ""
    , "main ="
    , "    let _ = println (Profile \"Alice\" 30).name"
    , "    in ()"
    ]


inlineRecordFixture :: String
inlineRecordFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.String as String"
    , "import Std.Log exposing (println)"
    , ""
    , "processReq : Int -> { name : String, age : Int } -> String"
    , "processReq i r = String.fromInt i ++ \" \" ++ r.name"
    , ""
    , "main ="
    , "    let _ = println (processReq 42 { name = \"Alice\", age = 30 })"
    , "    in ()"
    ]


writeFixture :: FilePath -> String -> IO ()
writeFixture tmp src = do
    createDirectoryIfMissing True (tmp </> "src")
    writeFile (tmp </> "src" </> "Main.sky") src
    writeFile (tmp </> "sky.toml") $ unlines
        [ "name = \"test\""
        , "version = \"0.1.0\""
        , "entry = \"src/Main.sky\""
        , ""
        , "[source]"
        , "root = \"src\""
        ]


runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
runSky sky args cwd =
    readCreateProcessWithExitCode (proc sky args) { cwd = Just cwd } ""


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    return (cwd </> "sky-out" </> "sky")
