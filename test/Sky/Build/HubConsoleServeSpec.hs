{-# LANGUAGE ScopedTypeVariables #-}
-- | Regression spec for `sky console-serve` — the v0.16.4 hub
-- daemon. Asserts:
--
--   * @--help@ surface lists every flag (no silent drift).
--   * Materialise + build + exec path lands on a daemon that
--     accepts an OTLP/JSON push and persists the row to the
--     SQLite hot store.
--
-- The spec shells out to the actual built `sky` binary (mirrors
-- @Sky.Cli.DoctorSpec@'s pattern). Daemon spawn is bounded via a
-- 90 s `timeout` so a hung child can't poison the cabal test run
-- (CLAUDE.md §3 — test/build timeout gate).
module Sky.Build.HubConsoleServeSpec (spec) where

import Test.Hspec
import Control.Concurrent (threadDelay)
import Control.Exception (try, SomeException)
import qualified Data.List as List
import qualified System.Directory as Dir
import System.FilePath ((</>))
import System.Environment (lookupEnv)
import System.IO (hGetContents)
import System.IO.Temp (withSystemTempDirectory)
import qualified System.Process as Proc
import System.Exit (ExitCode(..))
import qualified Data.Time.Clock.POSIX as POSIX


-- | Inherit the parent process's PATH so subprocess `sky` invocations
-- can find `go` (which `sky console-serve` shells out to for building
-- the hub daemon binary). The CI runner places Go under
-- `/opt/hostedtoolcache/go/<v>/x64/bin` on Linux +
-- `/Users/runner/hostedtoolcache/go/<v>/arm64/bin` on macOS via
-- `actions/setup-go@v5`; neither path is in the fallback. v0.16.13:
-- the prior `/usr/bin:/bin:/usr/local/bin` hardcode silently passed
-- on Linux (Ubuntu image leaves go in /usr/local/bin) but failed on
-- macOS with `sky: go: createProcess: exec: does not exist`.
inheritedPath :: IO String
inheritedPath = do
    mp <- lookupEnv "PATH"
    case mp of
        Just p | not (null p) -> pure p
        _                     -> pure "/usr/bin:/bin:/usr/local/bin"

-- | Locate the built `sky` binary at the repo's `sky-out/sky`.
-- Mirrors @Sky.Cli.DoctorSpec@'s pattern.
findSky :: IO FilePath
findSky = do
    cwd <- Dir.getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- Dir.doesFileExist candidate
    if ok then pure candidate
          else fail ("missing: " ++ candidate)


-- | Pick a "probably free" TCP port for the daemon. We derive
-- one from the current POSIX-microsecond clock into the dynamic
-- 49152-65000 range; a real collision causes the daemon to fail
-- to bind and the spec's waitForReady poll times out so we fail
-- fast rather than flake.
freePort :: IO Int
freePort = do
    t <- POSIX.getPOSIXTime
    let micros = floor (t * 1000000) :: Int
    pure (49152 + (micros `mod` 15849))


-- | Try a curl until it succeeds or @retries@ exhaust. Stops the
-- spec from waiting indefinitely on a still-building daemon.
waitForReady :: Int -> Int -> IO Bool
waitForReady _    0  = pure False
waitForReady port n = do
    (ec, _, _) <- Proc.readCreateProcessWithExitCode
        (Proc.shell ("curl -sf http://localhost:" ++ show port ++ "/_hub/healthz")) ""
    case ec of
        ExitSuccess -> pure True
        _ -> do
            threadDelay 1000000
            waitForReady port (n - 1)


spec :: Spec
spec = describe "Sky.Build.HubConsoleServe" $ do

    -- Top-level `--help` lists the console-serve subcommand. The
    -- subcommand's own flag surface is asserted indirectly via the
    -- rejection test below + the working-daemon test below it (an
    -- unwired flag would either reach the daemon as garbage or
    -- the parser would error out on `unrecognized option`).
    it "console-serve is listed in the top-level command surface" $ do
        sky <- findSky
        (ec, out, _err) <- Proc.readCreateProcessWithExitCode
            (Proc.proc sky ["--help"]) ""
        ec `shouldBe` ExitSuccess
        out `shouldSatisfy` ("console-serve" `List.isInfixOf`)
        out `shouldSatisfy` ("hub daemon" `List.isInfixOf`)

    it "console-serve rejects --auth with an unknown mode" $ do
        sky <- findSky
        (ec, _, _) <- Proc.readCreateProcessWithExitCode
            (Proc.proc sky ["console-serve", "--auth", "ghosts"]) ""
        case ec of
            ExitFailure 2 -> pure ()
            other -> expectationFailure ("expected exit 2, got " ++ show other)

    it "console-serve --auth token requires SKY_CONSOLE_HUB_TOKEN" $ do
        -- The child's `hub.Validate` should refuse to start in token
        -- mode without a token. We don't care that go build runs —
        -- the validation fires inside the child once it's exec'd.
        sky <- findSky
        path <- inheritedPath
        withSystemTempDirectory "sky-hub-spec" $ \tmp -> do
            port <- freePort
            (ec, _, _) <- Proc.readCreateProcessWithExitCode
                ((Proc.proc sky
                    [ "console-serve"
                    , "--port", show port
                    , "--data-dir", tmp </> "data"
                    , "--auth", "token"
                    ]) { Proc.env = Just [("PATH", path)] })
                ""
            case ec of
                ExitFailure _ -> pure ()
                ExitSuccess -> expectationFailure
                    "expected non-zero exit on token-mode without token"

    -- The full daemon spawn test takes a few seconds because the
    -- child has to `go build ./cmd/sky-hub` the first time. It uses
    -- a bounded `timeout` (90 s) so a regression that hangs the
    -- daemon can't wedge the cabal test run.
    it "console-serve daemon accepts an OTLP/JSON push and persists to SQLite" $ do
        sky <- findSky
        path <- inheritedPath
        withSystemTempDirectory "sky-hub-spec" $ \tmp -> do
            let dataDir = tmp </> "data"
                dbFile = dataDir </> "console-hot.db"
            port <- freePort
            -- Spawn daemon under `timeout` so a wedged child gets
            -- SIGKILL'd at 180 s — cabal test can't hang on this
            -- subprocess (CLAUDE.md §3). Must exceed `waitForReady`
            -- below (120 s) so the daemon isn't killed mid-boot
            -- under Linux CI's cold-cache go-build delay (v0.16.13:
            -- prior 90 s ceiling killed the daemon before the
            -- 120 s wait could observe it ready).
            let bp = (Proc.proc "timeout"
                        [ "180"
                        , sky, "console-serve"
                        , "--port", show port
                        , "--data-dir", dataDir
                        , "--auth", "off"
                        ])
                        { Proc.env = Just [("PATH", path), ("SKY_CONSOLE_HUB_QUIET", "1")]
                        , Proc.std_out = Proc.CreatePipe
                        , Proc.std_err = Proc.CreatePipe
                        }
            (_, mStdout, mStderr, ph) <- Proc.createProcess bp
            -- Wait up to 120 s for daemon ready. First boot includes
            -- the go build step; warm runs are <1 s. Linux CI's
            -- cold-cache go build of the console daemon can exceed
            -- 60 s under load (v0.16.13: observed timeouts on
            -- ubuntu-latest at 60 s); 120 s gives generous headroom.
            ready <- waitForReady port 120
            -- v0.16.13 #530-CI: on failure, drain captured stdout +
            -- stderr from the daemon so the real error surfaces in
            -- CI output instead of the bare `expected True got
            -- False`. Without this the test failed silently on
            -- Linux CI for 3 consecutive runs with no diagnostic.
            if ready
                then return ()
                else do
                    out <- maybe (return "") hGetContents mStdout
                    err <- maybe (return "") hGetContents mStderr
                    expectationFailure $
                        "console-serve daemon never bound port " ++ show port
                        ++ " within 120s.\n"
                        ++ "--- daemon stdout ---\n" ++ out
                        ++ "--- daemon stderr ---\n" ++ err

            -- Push a Sky-shaped JSON log batch.
            let payload =
                    "{\"resourceLogs\":[{\"resource\":{\"attributes\":\
                    \[{\"key\":\"service.name\",\"value\":{\"stringValue\":\"spec-svc\"}}]\
                    \},\"scopeLogs\":[{\"logRecords\":[{\
                    \\"timeUnixNano\":\"1750000000000000000\",\
                    \\"severityText\":\"INFO\",\
                    \\"body\":{\"stringValue\":\"hello-from-spec\"}}]}]}]}"
            (ec, out, _) <- Proc.readCreateProcessWithExitCode
                (Proc.shell
                    ("curl -s -o /dev/null -w '%{http_code}' "
                  ++ "-X POST -H 'Content-Type: application/json' "
                  ++ "--data " ++ show payload ++ " "
                  ++ "http://localhost:" ++ show port ++ "/v1/logs"))
                ""
            ec `shouldBe` ExitSuccess
            out `shouldBe` "200"

            -- Give the batcher's 200 ms tick + an extra buffer to
            -- commit before we close the daemon.
            threadDelay 1000000

            -- Tear down. The SIGINT handler triggers the drain +
            -- close path inside the hub.
            (_ :: Either SomeException ()) <- try $ Proc.terminateProcess ph
            _ <- Proc.waitForProcess ph

            -- Persisted? Open the SQLite directly and count rows.
            (ec2, sqlOut, _) <- Proc.readCreateProcessWithExitCode
                (Proc.shell
                    ("sqlite3 " ++ dbFile
                  ++ " 'SELECT service_name || \"|\" || level || \"|\" || message FROM telemetry_log'"))
                ""
            ec2 `shouldBe` ExitSuccess
            sqlOut `shouldSatisfy` ("spec-svc|info|hello-from-spec" `List.isInfixOf`)
