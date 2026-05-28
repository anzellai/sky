module Sky.Build.PubSubPublishTaskSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- Cycle 4 PT — Std.PubSub.publish (Task-shaped, callable from any
-- context, complements Cmd.publish which only fires from a Sky.Live
-- update return).
--
-- This spec pins:
--
--   1. Std.PubSub typechecks + builds when imported by user code.
--
--   2. The call site routes to rt.PubSub_publish (the kernel entry
--      registered in src/Sky/Generate/Go/Kernel.hs:494) — NOT to
--      rt.Ffi_kernel (the panic stub) or any alias-body call.
--
--   3. A run with no Sky.Live app registered in this process
--      surfaces the Err(Unavailable) branch at runtime, not a
--      panic or silent zero.
--
-- The matching runtime contract (PubSub_publish reads
-- processBroker, returns Ok(deliveryCount) or Err(Unavailable))
-- is exercised in runtime-go/rt/live_pubsub_task_test.go.
spec :: Spec
spec = describe "Std.PubSub.publish (Cycle 4 PT)" $ do
    it "type-checks + builds + routes to rt.PubSub_publish + runs the Err branch when no Live.app" $ do
        sky <- findSky
        withSystemTempDirectory "sky-pubsub-task" $ \tmp -> do
            writeFixture tmp
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
                then expectationFailure $
                    "sky build failed.\n" ++ out ++ "\n" ++ errOut
                else do
                    built <- doesFileExist (tmp </> "sky-out" </> "app")
                    built `shouldBe` True
                    body <- readFile (tmp </> "sky-out" </> "main.go")
                    -- Call site lowers to rt.PubSub_publish (the
                    -- registered kernel). The rt.Ffi_kernel panic
                    -- stub may legitimately appear in the dead
                    -- alias body declaration — what matters is the
                    -- call site routes correctly AND the program
                    -- reaches the runtime branch (verified below).
                    let routesToKernel = "rt.PubSub_publish(" `isInfixOf` body
                    routesToKernel `shouldBe` True
                    -- Run with no Live.app — Err branch fires.
                    (rc, runOut, _) <- runApp tmp
                    rc `shouldBe` ExitSuccess
                    ("no-live-app" `isInfixOf` runOut) `shouldBe` True

  where
    fixture :: String
    fixture =
        "module Main exposing (main)\n\n\
        \import Sky.Core.Prelude exposing (..)\n\
        \import Sky.Core.Task as Task\n\
        \import Sky.Core.Dict as Dict\n\
        \import Std.PubSub as PubSub\n\
        \import Std.Log exposing (println)\n\n\n\
        \main =\n\
        \    let\n\
        \        payload = Dict.fromList [( \"k\", \"v\" )]\n\
        \        result = Task.run (PubSub.publish \"t\" payload)\n\
        \    in\n\
        \        case result of\n\
        \            Ok n ->\n\
        \                println (\"ok-\" ++ String.fromInt n)\n\
        \            Err _ ->\n\
        \                println \"no-live-app\"\n"

    writeFixture :: FilePath -> IO ()
    writeFixture tmp = do
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "sky.toml")
            ("name = \"pubsub-task\"\n"
                ++ "version = \"0.0.0\"\n"
                ++ "entry = \"src/Main.sky\"\n\n"
                ++ "[source]\nroot = \"src\"\n")
        writeFile (tmp </> "src" </> "Main.sky") fixture

    findSky :: IO FilePath
    findSky = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok
            then return candidate
            else return "sky"

    runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
    runSky sky args cwd =
        readCreateProcessWithExitCode
            ((proc sky args) { cwd = Just cwd })
            ""

    runApp :: FilePath -> IO (ExitCode, String, String)
    runApp tmp =
        readCreateProcessWithExitCode
            ((proc (tmp </> "sky-out" </> "app") []) { cwd = Just tmp })
            ""
