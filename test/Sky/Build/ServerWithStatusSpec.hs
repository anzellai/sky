module Sky.Build.ServerWithStatusSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import System.Environment (getEnvironment)
import Data.List (isInfixOf)


-- v0.16.3 #467 — `Server.json body |> Server.withStatus 201`
-- regression spec.  Documented in CLAUDE.md + docs/stdlib.md as
-- the canonical idiom for building a typed `Response`.  Pre-fix
-- the chain panicked at runtime with
--
--   rt.Coerce: expected main.Sky_Http_Server_Response_R, got rt.SkyResponse
--
-- because `Server.withStatus` returns the runtime FFI struct
-- (`rt.SkyResponse`) while typed-codegen wraps the call result in
-- `rt.Coerce[Sky_Http_Server_Response_R](...)` and that branch had
-- no struct→struct narrow path (only `coerceInner` did).  Fix:
-- mirror the existing `narrowStructToStruct` call from `coerceInner`
-- into the user-facing `Coerce[T]` entry point.
--
-- Spec shape mirrors ServerStreamSpec:
--   1. Type-check + build a tiny fixture that exercises the panic
--      path via a typed `Response` slot.
--   2. Run the built binary and assert exit 0 (no runtime panic).
--
-- Runtime-side narrowing test lives in
-- `runtime-go/rt/server_withstatus_narrow_test.go`.
spec :: Spec
spec = describe "Sky.Http.Server.withStatus typed-Response narrow (#467)" $ do
    it "Server.json |> Server.withStatus chain builds + runs without panic" $ do
        sky <- findSky
        withSystemTempDirectory "sky-server-withstatus" $ \tmp -> do
            writeFixture tmp
            (bec, bout, berr) <- runSky sky ["build", "src/Main.sky"] tmp
            if bec /= ExitSuccess
                then expectationFailure $
                    "sky build failed.\n" ++ bout ++ "\n" ++ berr
                else do
                    built <- doesFileExist (tmp </> "sky-out" </> "app")
                    built `shouldBe` True
                    body <- readFile (tmp </> "sky-out" </> "main.go")
                    -- Codegen sanity: confirm the call shape that
                    -- triggers the typed-Response coerce slot is in
                    -- fact present.  Pre-fix this same generated code
                    -- panicked at runtime; the test passes because
                    -- the narrow now bridges the shapes.
                    let mustContain needle =
                            (needle `isInfixOf` body) `shouldBe` True
                    mustContain "Server_withStatus"
                    mustContain "Sky_Http_Server_Response_R"
                    -- Run the binary, assert exit 0.  Pre-fix the
                    -- process exited 1 via the v0.15.43 panic gate
                    -- with `CoerceFailure`.  Post-fix the chain
                    -- resolves cleanly and `main` prints + exits 0.
                    (rec, rout, rerr) <- runApp (tmp </> "sky-out" </> "app") tmp
                    if rec /= ExitSuccess
                        then expectationFailure $
                            "binary exited " ++ show rec
                            ++ "\nstdout:\n" ++ rout
                            ++ "\nstderr:\n" ++ rerr
                        else do
                            -- Sanity: assert the print landed (the
                            -- panic would short-circuit before this).
                            ("server-with-status-ok" `isInfixOf` rout)
                                `shouldBe` True

  where
    -- Fixture: builds the canonical chain inside a function whose
    -- declared return type is `Response`.  The annotation forces
    -- typed codegen to wrap the chain's result in
    -- `rt.Coerce[Sky_Http_Server_Response_R]` — exactly the slot
    -- that pre-fix panicked.  `main` calls the function so the
    -- DCE pass keeps it AND the runtime executes the path.
    fixture :: String
    fixture =
        "module Main exposing (main)\n\n\
        \import Sky.Core.Prelude exposing (..)\n\
        \import Sky.Http.Server as Server\n\
        \import Sky.Http.Server exposing (Response)\n\
        \import Sky.Core.Task as Task\n\
        \import Sky.Core.Error as Error exposing (Error)\n\
        \import Std.Log exposing (println)\n\n\n\
        \buildResp : String -> Response\n\
        \buildResp body =\n\
        \    Server.json body |> Server.withStatus 201\n\n\n\
        \main =\n\
        \    let\n\
        \        resp = buildResp \"{\\\"ok\\\": true}\"\n\
        \    in\n\
        \    case resp of\n\
        \        _ ->\n\
        \            println \"server-with-status-ok\"\n"

    writeFixture :: FilePath -> IO ()
    writeFixture tmp = do
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "sky.toml")
            ("name = \"server-withstatus\"\n"
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

    -- Strip SKY_RUNTIME_DIR so an agent-worktree run isn't hijacked
    -- by the parent repo's `.envrc` shellHook (the env var pins
    -- runtime-go to the parent repo, NOT this worktree).  Same
    -- treatment as EmbeddedRuntimeSpec.scrubRuntimeEnv.
    scrubRuntimeEnv :: IO [(String, String)]
    scrubRuntimeEnv = do
        env <- getEnvironment
        return [ (k, v) | (k, v) <- env, k /= "SKY_RUNTIME_DIR" ]

    runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
    runSky sky args cwd = do
        env <- scrubRuntimeEnv
        readCreateProcessWithExitCode
            ((proc sky args) { cwd = Just cwd, env = Just env })
            ""

    -- Bounded run with a finite timeout via `timeout 30 <bin>` so a
    -- regressed build that hangs doesn't poison the test runner.
    runApp :: FilePath -> FilePath -> IO (ExitCode, String, String)
    runApp bin cwd = do
        env <- scrubRuntimeEnv
        readCreateProcessWithExitCode
            ((proc "timeout" ["30", bin]) { cwd = Just cwd, env = Just env })
            ""
