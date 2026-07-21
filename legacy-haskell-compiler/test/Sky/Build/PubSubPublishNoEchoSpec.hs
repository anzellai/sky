module Sky.Build.PubSubPublishNoEchoSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- Cycle 4 NE / issue #359 — Cmd.publishNoEcho + Std.PubSub.publishNoEcho.
--
-- This spec pins:
--
--   1. Std.Cmd.publishNoEcho + Std.PubSub.publishNoEcho both
--      type-check + build when imported by user code.
--
--   2. The PubSub.publishNoEcho call site routes to the
--      rt.PubSub_publishNoEcho kernel (NOT to the panic-stub
--      Ffi_kernel route) and reaches the runtime branch
--      end-to-end — the Err(Unavailable) path fires when no
--      Sky.Live app is registered.
--
--   3. The Cmd.publishNoEcho alias body lowers to its kernel
--      stub — checked indirectly by the build succeeding with
--      the import in scope (a missing kernel entry would surface
--      as `undefined: rt.Cmd_publishNoEcho` at go build time,
--      mirroring the Ffi-kernel-table validation that lights up
--      missing entries).
--
-- The matching SkipOrigin runtime contract is exercised in
-- runtime-go/rt/live_pubsub_no_echo_test.go.
spec :: Spec
spec = describe "Std.Cmd.publishNoEcho + Std.PubSub.publishNoEcho (Cycle 4 NE / #359)" $ do
    it "type-checks + builds + routes PubSub kernel + runs the Err branch when no Live.app" $ do
        sky <- findSky
        withSystemTempDirectory "sky-pubsub-noecho" $ \tmp -> do
            writeFixture tmp
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
                then expectationFailure $
                    "sky build failed.\n" ++ out ++ "\n" ++ errOut
                else do
                    built <- doesFileExist (tmp </> "sky-out" </> "app")
                    built `shouldBe` True
                    body <- readFile (tmp </> "sky-out" </> "main.go")
                    -- PubSub.publishNoEcho lowers to its kernel
                    -- symbol at the call site. Cmd.publishNoEcho's
                    -- kernel-table entry is exercised by the build
                    -- itself — Std.Cmd is in the import list, so
                    -- the alias's rt.Ffi_kernel("Cmd_publishNoEcho")
                    -- body must resolve; an unregistered name would
                    -- surface as a build-time error in go build via
                    -- `undefined: rt.Cmd_publishNoEcho` at any
                    -- reachable call site.
                    let pubSubRoutes = "rt.PubSub_publishNoEcho(" `isInfixOf` body
                    pubSubRoutes `shouldBe` True
                    -- Run with no Live.app — the PubSub Task fires
                    -- its Err branch.
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
        \import Std.Cmd as Cmd\n\
        \import Std.PubSub as PubSub\n\
        \import Std.Log exposing (println)\n\n\n\
        \-- Std.Cmd.publishNoEcho is in scope; an unregistered\n\
        \-- kernel binding would surface as a build error when\n\
        \-- the alias body's Ffi_kernel route fails to resolve\n\
        \-- against the typed Cmd shape.\n\
        \_cmdImported : String -> any -> Cmd ()\n\
        \_cmdImported =\n\
        \    Cmd.publishNoEcho\n\n\n\
        \main =\n\
        \    let\n\
        \        payload = Dict.fromList [( \"k\", \"v\" )]\n\
        \        result = Task.run (PubSub.publishNoEcho \"t\" payload)\n\
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
            ("name = \"pubsub-noecho\"\n"
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
