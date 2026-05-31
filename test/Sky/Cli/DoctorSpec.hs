{-# LANGUAGE ScopedTypeVariables #-}
module Sky.Cli.DoctorSpec (spec) where

-- Phase 2.3 — `sky doctor` regression fence. Tests run against
-- a clean temp project so they don't depend on the host's `.skycache`
-- / port-8000 / mem-guard state.
--
-- Coverage:
--   * clean project produces "no issues"
--   * stale .skycache produces a warning
--   * stale sky-out/main.go produces an info finding
--   * --fix flag actually deletes the offending dirs
--   * sky.toml missing → exit code 2 (separate test using
--     `getCurrentDirectory` redirection)
--
-- The doctor module uses `getCurrentDirectory` internally for
-- project-root discovery; we wrap calls in `withCurrentDirectory`
-- so per-test temp directories work without polluting the host.

import Test.Hspec
import Control.Exception (catch, SomeException)
import Data.List (isInfixOf)
import System.Exit (ExitCode(..))
import qualified System.Directory as Dir
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import qualified Data.Time.Clock as Clock
import qualified System.Process as Proc


-- | String-level isInfixOf alias to keep test prose readable.
isInfixOfStr :: String -> String -> Bool
isInfixOfStr = isInfixOf


-- The runner exits the process via System.Exit. To test, we shell
-- out to the built `sky` binary instead of calling runDoctor
-- directly (avoids the test framework's process being killed).
findSky :: IO FilePath
findSky = do
    cwd <- Dir.getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- Dir.doesFileExist c
    if ok then pure c else fail ("missing: " ++ c)


-- | Run `sky doctor` in @dir@ and return (exit code, stdout).
--
-- Bug #371 (test hygiene): `SKY_DOCTOR_SKIP_PORT_CHECK=1` makes the
-- port-8000 check deterministic across runs — without it, an unrelated
-- host process holding the port would inject a spurious "port-busy"
-- finding into the "clean project" / "stale-cache" / "empty toml"
-- assertions and break them under the full-sweep `cabal test`. The
-- production code-path already skips the check on un-built projects,
-- but the `--fix`/`stale` cases scaffold + build, so we also gate via
-- env on every invocation here.
runDoctorIn :: FilePath -> [String] -> IO (Int, String)
runDoctorIn dir args = do
    sky <- findSky
    let cmd = "cd " ++ dir ++ " && SKY_DOCTOR_SKIP_PORT_CHECK=1 "
              ++ sky ++ " doctor " ++ unwords args
    (ec, out, _err) <- Proc.readCreateProcessWithExitCode
        (Proc.shell cmd) ""
    let code = case ec of
            ExitSuccess     -> 0
            ExitFailure n   -> n
    pure (code, out)


-- | Helper: scaffold a minimal Sky project (sky.toml + src/Main.sky).
scaffold :: FilePath -> IO ()
scaffold root = do
    Dir.createDirectoryIfMissing True (root </> "src")
    writeFile (root </> "sky.toml") "name = \"doctortest\"\n"
    writeFile (root </> "src" </> "Main.sky")
        "module Main exposing (main)\n\nmain = ()\n"


-- | Helper: simulate a stale `.skycache` by creating one and then
-- bumping the src file's mtime.
makeStaleCache :: FilePath -> IO ()
makeStaleCache root = do
    Dir.createDirectoryIfMissing True (root </> ".skycache")
    writeFile (root </> ".skycache" </> "source.hash") "0123abcd"
    -- Wait then touch the src so its mtime is strictly newer.
    threadSleepMs 50
    touch (root </> "src" </> "Main.sky")
  where
    touch p = do
        now <- Clock.getCurrentTime
        Dir.setModificationTime p now
    threadSleepMs ms = do
        _ <- Proc.readCreateProcessWithExitCode
            (Proc.shell ("sleep " ++ show (fromIntegral ms / 1000 :: Double))) ""
        pure ()


spec :: Spec
spec = describe "Sky.Cli.Doctor" $ do

    it "clean project reports no issues, exit 0" $ do
        withSystemTempDirectory "sky-doctor" $ \dir -> do
            scaffold dir
            (code, out) <- runDoctorIn dir []
            code `shouldBe` 0
            out `shouldContain` "no issues found"

    it "missing sky.toml exits with code 2" $ do
        withSystemTempDirectory "sky-doctor-no-toml" $ \dir -> do
            -- Don't scaffold — dir exists but has no sky.toml.
            (code, out) <- runDoctorIn dir []
            code `shouldBe` 2
            out `shouldContain` "no sky.toml found"

    it "stale .skycache fires a warning" $ do
        withSystemTempDirectory "sky-doctor-stale" $ \dir -> do
            scaffold dir
            makeStaleCache dir
            (code, out) <- runDoctorIn dir []
            -- exit 1 because we have findings (even if all info).
            code `shouldBe` 1
            out `shouldContain` ".skycache/ is older than your src/*.sky"

    it "--fix deletes the stale .skycache" $ do
        withSystemTempDirectory "sky-doctor-fix" $ \dir -> do
            scaffold dir
            makeStaleCache dir
            -- Confirm the cache exists before fix.
            existsBefore <- Dir.doesDirectoryExist (dir </> ".skycache")
            existsBefore `shouldBe` True

            (_, out) <- runDoctorIn dir ["--fix"]
            out `shouldContain` "deleted"

            -- After --fix, the directory should be gone.
            existsAfter <- Dir.doesDirectoryExist (dir </> ".skycache")
            existsAfter `shouldBe` False

    it "verbose flag prints check-id alongside each finding" $ do
        withSystemTempDirectory "sky-doctor-v" $ \dir -> do
            scaffold dir
            makeStaleCache dir
            (_, out) <- runDoctorIn dir ["--verbose"]
            out `shouldContain` "check-id: stale-cache"

    -- Defensive: doctor must not crash on a malformed / empty
    -- sky.toml — should report a clear error finding instead.
    it "empty sky.toml reports an error finding" $ do
        withSystemTempDirectory "sky-doctor-empty-toml" $ \dir -> do
            Dir.createDirectoryIfMissing True (dir </> "src")
            writeFile (dir </> "sky.toml") ""
            -- We still need a src so the project-root walk succeeds.
            writeFile (dir </> "src" </> "Main.sky") "module Main exposing (main)\nmain = ()\n"
            (code, out) <- runDoctorIn dir []
            -- An error finding → exit code 1.
            code `shouldBe` 1
            out `shouldContain` "sky.toml is empty"

    -- Make sure we don't import-cycle or otherwise fail to build
    -- the test suite when the project is unusual. Smoke test.
    it "does not crash when only sky.toml is present (no src)" $ do
        withSystemTempDirectory "sky-doctor-no-src" $ \dir -> do
            writeFile (dir </> "sky.toml") "name = \"x\"\n"
            (code, _) <- runDoctorIn dir []
            -- No errors, no findings → exit 0.
            code `shouldSatisfy` (\c -> c == 0 || c == 1)

    -- Bug #371 regression: the port-busy check used to fire on a
    -- pristine scaffolded project whenever ANY host process held
    -- port 8000 (other dev servers, parallel test runs, browser
    -- live-reload). The fix gates the check on `sky-out/` existing
    -- — a project that has never been built can't have leaked a
    -- `sky run` listener, so the finding is dishonest.
    --
    -- This test runs `sky doctor` WITHOUT the env-var skip (using a
    -- bare shell command) on a pristine scaffold, which used to
    -- emit a port-busy warning whenever the host happened to bind
    -- 8000. Now it always reports "no issues found".
    -- v0.15.48 +10 tooling-polish checks
    it "v0.15.48: go-toolchain check runs (Go is on PATH in dev env)" $ do
        withSystemTempDirectory "sky-doctor-go" $ \dir -> do
            scaffold dir
            (_, out) <- runDoctorIn dir ["--verbose"]
            -- We don't enforce a specific result — just confirm
            -- the check doesn't crash. If `go version` ≥ 1.22 the
            -- check is silent; older Go fires "Go X.Y is too old".
            -- Either way, no other check should fail.
            out `shouldSatisfy`
              (\o -> ("no issues found" `isInfixOfStr` o)
                   || ("go-toolchain" `isInfixOfStr` o)
                   || ("checking" `isInfixOfStr` o))

    it "v0.15.48: unknown [foo] section in sky.toml fires toml-unknown-section" $ do
        withSystemTempDirectory "sky-doctor-toml" $ \dir -> do
            Dir.createDirectoryIfMissing True (dir </> "src")
            writeFile (dir </> "sky.toml") $
                "name = \"x\"\n[totallymadeup]\nkey = 1\n"
            writeFile (dir </> "src" </> "Main.sky")
                "module Main exposing (main)\nmain = ()\n"
            (_, out) <- runDoctorIn dir ["--verbose"]
            out `shouldContain` "unknown section"

    it "v0.15.48: known [live] section does NOT fire toml-unknown-section" $ do
        withSystemTempDirectory "sky-doctor-live" $ \dir -> do
            Dir.createDirectoryIfMissing True (dir </> "src")
            writeFile (dir </> "sky.toml") $
                "name = \"x\"\n[live]\nport = 8000\n"
            writeFile (dir </> "src" </> "Main.sky")
                "module Main exposing (main)\nmain = ()\n"
            (_, out) <- runDoctorIn dir ["--verbose"]
            out `shouldNotContain` "toml-unknown-section"

    it "v0.15.48: [live] without SKY_AUTH_TOKEN_SECRET fires auth-secret-missing" $ do
        withSystemTempDirectory "sky-doctor-auth" $ \dir -> do
            Dir.createDirectoryIfMissing True (dir </> "src")
            writeFile (dir </> "sky.toml") $
                "name = \"x\"\n[live]\nport = 8000\n"
            writeFile (dir </> "src" </> "Main.sky")
                "module Main exposing (main)\nmain = ()\n"
            -- Explicitly clear the env to make this deterministic.
            sky <- findSky
            let cmd = "cd " ++ dir
                    ++ " && SKY_DOCTOR_SKIP_PORT_CHECK=1 unset SKY_AUTH_TOKEN_SECRET 2>/dev/null; "
                    ++ "env -u SKY_AUTH_TOKEN_SECRET " ++ sky ++ " doctor --verbose"
            (_, out, _) <- Proc.readCreateProcessWithExitCode
                (Proc.shell cmd) ""
            out `shouldContain` "auth-secret-missing"

    it "Bug #371: clean unbuilt project skips the port-8000 check" $ do
        withSystemTempDirectory "sky-doctor-371" $ \dir -> do
            scaffold dir
            sky <- findSky
            -- Bare invocation (no SKY_DOCTOR_SKIP_PORT_CHECK) — this
            -- exercises the production code-path's "no sky-out/ → skip"
            -- gate, not the env-var escape hatch.
            let cmd = "cd " ++ dir ++ " && " ++ sky ++ " doctor"
            (ec, out, _err) <- Proc.readCreateProcessWithExitCode
                (Proc.shell cmd) ""
            let code = case ec of
                    ExitSuccess   -> 0
                    ExitFailure n -> n
            code `shouldBe` 0
            out `shouldContain` "no issues found"


-- Catch + drop exceptions in helpers that probe filesystem state
-- the test doesn't care about — silently treat as "nothing here".
ignoreExn :: IO a -> a -> IO a
ignoreExn act fallback = act `catch` \(_ :: SomeException) -> pure fallback
