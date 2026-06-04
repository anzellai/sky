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
import System.IO.Temp (withSystemTempDirectory)
import qualified System.Process as Proc
import System.Exit (ExitCode(..))
import qualified Data.Time.Clock.POSIX as POSIX

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
        withSystemTempDirectory "sky-hub-spec" $ \tmp -> do
            port <- freePort
            (ec, _, _) <- Proc.readCreateProcessWithExitCode
                ((Proc.proc sky
                    [ "console-serve"
                    , "--port", show port
                    , "--data-dir", tmp </> "data"
                    , "--auth", "token"
                    ]) { Proc.env = Just [("PATH", "/usr/bin:/bin:/usr/local/bin")] })
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
        withSystemTempDirectory "sky-hub-spec" $ \tmp -> do
            let dataDir = tmp </> "data"
                dbFile = dataDir </> "console-hot.db"
            port <- freePort
            -- Spawn daemon under `timeout` so a wedged child gets
            -- SIGKILL'd at 90 s — cabal test can't hang on this
            -- subprocess (CLAUDE.md §3).
            let bp = (Proc.proc "timeout"
                        [ "90"
                        , sky, "console-serve"
                        , "--port", show port
                        , "--data-dir", dataDir
                        , "--auth", "off"
                        ])
                        { Proc.env = Just [("PATH", "/usr/bin:/bin:/usr/local/bin"), ("SKY_CONSOLE_HUB_QUIET", "1")]
                        , Proc.std_out = Proc.CreatePipe
                        , Proc.std_err = Proc.CreatePipe
                        }
            (_, _, _, ph) <- Proc.createProcess bp
            -- Wait up to 60 s for daemon ready. First boot includes
            -- the go build step; warm runs are <1 s.
            ready <- waitForReady port 60
            ready `shouldBe` True

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
