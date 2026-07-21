module Sky.Build.TaskResultBridgesSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


-- The three Result/Task bridge helpers (Task.fromResult,
-- Task.andThenResult, Result.andThenTask) flatten what would otherwise
-- be nested case-on-Result inside a Task.andThen lambda. This spec
-- builds a Sky program that exercises every shape — Ok lift, Err lift,
-- Result-step after Task, Task-step after Result, and the four-link
-- pipeline used in the docs — and asserts the printed output. Catches
-- regressions across the canonicaliser, kernel type sigs, kernel
-- registry, and runtime helpers in one shot.
findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
          else fail ("sky binary missing at " ++ candidate
                  ++ " — run scripts/build.sh first")

runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
runSky sky args workDir = do
    let cp = (proc sky args) { cwd = Just workDir }
    readCreateProcessWithExitCode cp ""

writeFixtureProject :: FilePath -> String -> String -> IO ()
writeFixtureProject dir name body = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml")
        ("name = \"" ++ name ++ "\"\nversion = \"0.0.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
    writeFile (dir </> "src" </> "Main.sky") body

spec :: Spec
spec = do
    describe "Task.fromResult / Task.andThenResult / Result.andThenTask" $ do
        it "compiles, runs, and prints the expected outputs" $ do
            sky <- findSky
            withSystemTempDirectory "sky-task-result-bridges" $ \tmp -> do
                writeFixtureProject tmp "task-result-bridges" $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Result as Result"
                    , "import Sky.Core.Task as Task"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "showInt : Result String Int -> String"
                    , "showInt r ="
                    , "    case r of"
                    , "        Ok n -> \"Ok \" ++ String.fromInt n"
                    , "        Err e -> \"Err \" ++ e"
                    , ""
                    , "showStr : Result String String -> String"
                    , "showStr r ="
                    , "    case r of"
                    , "        Ok s -> \"Ok \" ++ s"
                    , "        Err e -> \"Err \" ++ e"
                    , ""
                    , "main ="
                    , "    let"
                    , "        t1 = Task.run (Task.fromResult (Ok 42))"
                    , "        t2 = Task.run (Task.fromResult (Err \"boom\"))"
                    , "        t3 ="
                    , "            Task.run"
                    , "                (Task.andThenResult"
                    , "                    (\\n -> Ok (n + 1))"
                    , "                    (Task.succeed 10))"
                    , "        t4 ="
                    , "            Task.run"
                    , "                (Task.andThenResult"
                    , "                    (\\_ -> Err \"result-failed\")"
                    , "                    (Task.succeed 10))"
                    , "        t5 ="
                    , "            Task.run"
                    , "                (Result.andThenTask"
                    , "                    (\\n -> Task.succeed (n * 2))"
                    , "                    (Ok 10))"
                    , "        t6 ="
                    , "            Task.run"
                    , "                (Result.andThenTask"
                    , "                    (\\n -> Task.succeed (n * 2))"
                    , "                    (Err \"no\"))"
                    , "        t7 ="
                    , "            Ok 5"
                    , "                |> Result.andThenTask (\\n -> Task.succeed (n + 1))"
                    , "                |> Task.andThenResult (\\n -> Ok (n * 2))"
                    , "                |> Task.andThen (\\n -> Task.succeed (String.fromInt n))"
                    , "                |> Task.run"
                    , "    in"
                    , "    println"
                    , "        (String.join \"|\""
                    , "            [ showInt t1"
                    , "            , showInt t2"
                    , "            , showInt t3"
                    , "            , showInt t4"
                    , "            , showInt t5"
                    , "            , showInt t6"
                    , "            , showStr t7"
                    , "            ])"
                    ]
                (ec, _bOut, bErr) <- runSky sky ["build", "src/Main.sky"] tmp
                if ec /= ExitSuccess
                    then expectationFailure ("sky build failed: " ++ bErr)
                    else return ()
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True
                (rec, rOut, rErr) <-
                    readCreateProcessWithExitCode
                        ((proc (tmp </> "sky-out" </> "app") []) { cwd = Just tmp })
                        ""
                if rec /= ExitSuccess
                    then expectationFailure ("app exited non-zero: " ++ rErr)
                    else return ()
                let expected = unlines
                        [ "Ok 42|Err boom|Ok 11|Err result-failed|Ok 20|Err no|Ok 12"
                        ]
                rOut `shouldBe` expected
