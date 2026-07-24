module Sky.Build.NestedPatternSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing, doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


writeMain :: FilePath -> String -> IO ()
writeMain dir body = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml") $ unlines
        [ "name = \"nested-pattern-regress\""
        , "version = \"0.0.0\""
        , "entry = \"src/Main.sky\""
        , ""
        , "[source]"
        , "root = \"src\""
        ]
    writeFile (dir </> "src" </> "Main.sky") body


runProject :: FilePath -> FilePath -> IO (ExitCode, String, String)
runProject sky dir = do
    -- Build
    (bc, bOut, bErr) <- readCreateProcessWithExitCode
        ((proc sky ["build", "src/Main.sky"]) { cwd = Just dir }) ""
    case bc of
        ExitFailure _ -> return (bc, bOut, bErr)
        ExitSuccess -> do
            readCreateProcessWithExitCode
                ((proc (dir </> "sky-out" </> "app") []) { cwd = Just dir }) ""


spec :: Spec
spec = do
    describe "nested constructor patterns (skyvote signup regression)" $ do
        it "discriminates Ok Nothing vs Ok (Just x) correctly" $ do
            sky <- findSky
            withSystemTempDirectory "sky-nest-" $ \dir -> do
                writeMain dir $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , "import Sky.Core.Error as Error exposing (Error)"
                    , ""
                    , ""
                    , "lookup : Int -> Result Error (Maybe String)"
                    , "lookup n ="
                    , "    if n == 0 then"
                    , "        Err (Error.io \"no key\")"
                    , "    else if n == 1 then"
                    , "        Ok Nothing"
                    , "    else"
                    , "        Ok (Just \"found\")"
                    , ""
                    , ""
                    , "describe : Int -> String"
                    , "describe n ="
                    , "    case lookup n of"
                    , "        Err _ ->"
                    , "            \"err\""
                    , "        Ok Nothing ->"
                    , "            \"nothing\""
                    , "        Ok (Just s) ->"
                    , "            \"just:\" ++ s"
                    , ""
                    , ""
                    , "main ="
                    , "    let"
                    , "        _ = println (describe 0)"
                    , "        _ = println (describe 1)"
                    , "        _ = println (describe 2)"
                    , "    in"
                    , "        ()"
                    ]
                (ec, out, err) <- runProject sky dir
                case ec of
                    ExitSuccess -> do
                        let want = unlines ["err", "nothing", "just:found"]
                        out `shouldBe` want
                    ExitFailure n ->
                        expectationFailure $
                            "build/run failed with exit " ++ show n ++
                            "\nstdout: " ++ out ++
                            "\nstderr: " ++ err

        it "discriminates nested ADT constructors (Ok True / Ok False)" $ do
            sky <- findSky
            withSystemTempDirectory "sky-nest-bool-" $ \dir -> do
                writeMain dir $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , "import Sky.Core.Error as Error exposing (Error)"
                    , ""
                    , ""
                    , "check : Int -> Result Error Bool"
                    , "check n ="
                    , "    if n == 0 then"
                    , "        Err (Error.io \"zero\")"
                    , "    else if n == 1 then"
                    , "        Ok True"
                    , "    else"
                    , "        Ok False"
                    , ""
                    , ""
                    , "describe : Int -> String"
                    , "describe n ="
                    , "    case check n of"
                    , "        Err _ -> \"err\""
                    , "        Ok True -> \"true\""
                    , "        Ok False -> \"false\""
                    , ""
                    , ""
                    , "main ="
                    , "    let"
                    , "        _ = println (describe 0)"
                    , "        _ = println (describe 1)"
                    , "        _ = println (describe 2)"
                    , "    in"
                    , "        ()"
                    ]
                (ec, out, err) <- runProject sky dir
                case ec of
                    ExitSuccess ->
                        out `shouldBe` unlines ["err", "true", "false"]
                    ExitFailure n ->
                        expectationFailure $
                            "build/run failed with exit " ++ show n ++
                            "\nstdout: " ++ out ++
                            "\nstderr: " ++ err
