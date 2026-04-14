module Sky.Type.ExhaustivenessSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing, doesFileExist)
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

writeProject :: FilePath -> String -> IO ()
writeProject dir body = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml")
        "name = \"ex\"\nversion = \"0.0.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n"
    writeFile (dir </> "src" </> "Main.sky") body

spec :: Spec
spec = do
    describe "P3: non-exhaustive case is a build error" $ do
        it "reports the missing constructor (Blue) for a user ADT" $ do
            sky <- findSky
            cwd <- getCurrentDirectory
            body <- readFile (cwd </> "test" </> "fixtures" </> "exhaustiveness" </> "missing.sky")
            withSystemTempDirectory "sky-p3" $ \tmp -> do
                writeProject tmp body
                (ec, out, err) <- readCreateProcessWithExitCode
                    ((proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }) ""
                ec `shouldNotBe` ExitSuccess
                let combined = out ++ err
                combined `shouldSatisfy` \s ->
                    ("does not cover" `isInfixOf` s) && ("Blue" `isInfixOf` s)

        it "accepts a wildcard-covered case" $ do
            sky <- findSky
            withSystemTempDirectory "sky-p3-ok" $ \tmp -> do
                writeProject tmp $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type Colour = Red | Green | Blue"
                    , ""
                    , "describe : Colour -> String"
                    , "describe c ="
                    , "    case c of"
                    , "        Red -> \"red\""
                    , "        _ -> \"other\""
                    , ""
                    , "main = println (describe Red)"
                    ]
                (ec, _o, _e) <- readCreateProcessWithExitCode
                    ((proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }) ""
                ec `shouldBe` ExitSuccess
