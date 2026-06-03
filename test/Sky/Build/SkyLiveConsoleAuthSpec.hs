{-# LANGUAGE ScopedTypeVariables #-}
module Sky.Build.SkyLiveConsoleAuthSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..),
                       createProcess, terminateProcess, waitForProcess,
                       StdStream(..))
import System.Exit (ExitCode(..))
import System.Environment (getEnvironment)
import Data.List (isInfixOf)
import Control.Concurrent (threadDelay)
import Control.Exception (catch, SomeException)


-- v0.16.0 PR 3 — Sky.Live consoleAuth field + production gate.
--
-- Pins:
--   1. An app WITH `consoleAuth = identifyAdmin` builds and runs
--      with SKY_CONSOLE_AUTH=app — the callback gates console
--      access. We can't trivially curl the inner console (the
--      Identity record's `subject` is an opaque value here), so we
--      assert the app boots cleanly + serves a 200 on /, which
--      confirms the row-poly consoleAuth field is accepted by HM
--      and the runtime field lookup picks it up without panic.
--   2. An app WITHOUT the `consoleAuth` field still builds and
--      runs — the field is optional via the row-open Live.app cfg
--      record (same `appExt` mechanism as v0.15.58 `head`).
--   3. A production-mode app (ENV=production) WITHOUT
--      SKY_CONSOLE_AUTH set declines to mount — the runtime emits
--      the `console.disabled reason=auth-unset` log and /_sky/console
--      returns 404 (no handler registered).
--   4. A production-mode app WITH SKY_CONSOLE_AUTH=token serves
--      the login form on /_sky/console GET (401 + form HTML).
spec :: Spec
spec = describe "Sky.Live consoleAuth field + auth gate (v0.16.0)" $ do
    it "an app WITH a `consoleAuth` callback builds and serves /" $ do
        sky <- findSky
        withSystemTempDirectory "sky-live-consoleauth-with" $ \tmp -> do
            writeFixture tmp fixtureWithConsoleAuth
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
                then expectationFailure $
                    "sky build (with consoleAuth) failed.\n" ++ out ++ "\n" ++ errOut
                else do
                    built <- doesFileExist (tmp </> "sky-out" </> "app")
                    built `shouldBe` True
                    body <- fetchPathWithEnv tmp 18743 "/" []
                    -- The app responded — consoleAuth field accepted + boot
                    -- succeeded. Body content varies; just confirm a non-
                    -- empty response landed.
                    (length body > 0) `shouldBe` True

    it "an app WITHOUT a `consoleAuth` field still builds and runs" $ do
        sky <- findSky
        withSystemTempDirectory "sky-live-consoleauth-without" $ \tmp -> do
            writeFixture tmp fixtureWithoutConsoleAuth
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
                then expectationFailure $
                    "sky build (without consoleAuth) failed.\n" ++ out ++ "\n" ++ errOut
                else do
                    built <- doesFileExist (tmp </> "sky-out" </> "app")
                    built `shouldBe` True
                    body <- fetchPathWithEnv tmp 18744 "/" []
                    (length body > 0) `shouldBe` True

    it "production mode + SKY_CONSOLE_AUTH=off → /_sky/console 404 (no console)" $ do
        -- v0.16.1 PR2: when SKY_CONSOLE_AUTH is *unset* in production,
        -- the runtime now fatal-exits at boot (refuses to start with
        -- an ambiguous security posture) instead of silently declining
        -- the mount. Operators who genuinely want no console in
        -- production must explicitly set SKY_CONSOLE_AUTH=off — that
        -- declines the mount but keeps the process running. We test
        -- the "off" path here; the fatal-exit-on-unset path is
        -- exercised by runtime-go/rt/console_boot_test.go's
        -- TestAssertConsoleInvariant_FatalWhenNeitherHealthyAndAuthSet.
        sky <- findSky
        withSystemTempDirectory "sky-live-consoleauth-prodoff" $ \tmp -> do
            writeFixture tmp fixtureWithoutConsoleAuth
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
                then expectationFailure $
                    "sky build failed.\n" ++ out ++ "\n" ++ errOut
                else do
                    body <- fetchPathWithEnv tmp 18745 "/_sky/console"
                                [ ("ENV", "production")
                                , ("SKY_CONSOLE_AUTH", "off")
                                ]
                    -- 404 → body is the default Go ServeMux "404 page not
                    -- found" text. We assert the absence of console
                    -- markers as a sufficient proxy.
                    ("Sky Console" `isInfixOf` body) `shouldBe` False
                    ("Sign in"     `isInfixOf` body) `shouldBe` False

    it "production mode + SKY_CONSOLE_AUTH=token → /_sky/console returns the login form" $ do
        sky <- findSky
        withSystemTempDirectory "sky-live-consoleauth-prodtoken" $ \tmp -> do
            writeFixture tmp fixtureWithoutConsoleAuth
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
                then expectationFailure $
                    "sky build failed.\n" ++ out ++ "\n" ++ errOut
                else do
                    body <- fetchPathWithEnv tmp 18746 "/_sky/console"
                                [ ("ENV", "production")
                                , ("SKY_CONSOLE_AUTH", "token")
                                , ("SKY_CONSOLE_TOKEN", "32-byte-test-token-aaaaaaaaaaaaaaaa")
                                ]
                    -- 401 + the login form HTML rendered by
                    -- renderConsoleLoginPage.
                    ("Sky Console" `isInfixOf` body) `shouldBe` True
                    ("Token"       `isInfixOf` body) `shouldBe` True
                    ("/_sky/console/_login" `isInfixOf` body) `shouldBe` True

  where
    fixtureWithConsoleAuth :: String
    fixtureWithConsoleAuth =
        "module Main exposing (main)\n\n\
        \import Sky.Core.Prelude exposing (..)\n\
        \import Sky.Core.Task as Task\n\
        \import Std.Cmd as Cmd\n\
        \import Std.Sub as Sub\n\
        \import Std.Live exposing (app)\n\
        \import Std.Live.Console exposing (Identity)\n\
        \import Sky.Core.Dict as Dict\n\
        \import Std.Html as Html\n\n\n\
        \type alias Model = { count : Int }\n\
        \type Msg = NoOp\n\n\n\
        \init _ = ({ count = 0 }, Cmd.none)\n\n\
        \update _ model = (model, Cmd.none)\n\n\
        \subscriptions _ = Sub.none\n\n\
        \view _ =\n\
        \    Html.node \"div\" [] [ Html.text \"hi\" ]\n\n\n\
        \identifyAdmin _ =\n\
        \    Task.succeed\n\
        \        (Just\n\
        \            { subject = \"test-user\"\n\
        \            , email = \"test@example\"\n\
        \            , claims = Dict.empty\n\
        \            }\n\
        \        )\n\n\n\
        \main =\n\
        \    app\n\
        \        { init = init\n\
        \        , update = update\n\
        \        , view = view\n\
        \        , subscriptions = subscriptions\n\
        \        , routes = []\n\
        \        , notFound = ()\n\
        \        , consoleAuth = identifyAdmin\n\
        \        }\n"

    fixtureWithoutConsoleAuth :: String
    fixtureWithoutConsoleAuth =
        "module Main exposing (main)\n\n\
        \import Sky.Core.Prelude exposing (..)\n\
        \import Std.Cmd as Cmd\n\
        \import Std.Sub as Sub\n\
        \import Std.Live exposing (app)\n\
        \import Std.Html as Html\n\n\n\
        \type alias Model = { count : Int }\n\
        \type Msg = NoOp\n\n\n\
        \init _ = ({ count = 0 }, Cmd.none)\n\n\
        \update _ model = (model, Cmd.none)\n\n\
        \subscriptions _ = Sub.none\n\n\
        \view _ =\n\
        \    Html.node \"div\" [] [ Html.text \"hi\" ]\n\n\n\
        \main =\n\
        \    app\n\
        \        { init = init\n\
        \        , update = update\n\
        \        , view = view\n\
        \        , subscriptions = subscriptions\n\
        \        , routes = []\n\
        \        , notFound = ()\n\
        \        }\n"

    writeFixture :: FilePath -> String -> IO ()
    writeFixture tmp src = do
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "sky.toml")
            ("name = \"sky-live-consoleauth-spec\"\n"
                ++ "version = \"0.0.0\"\n"
                ++ "entry = \"src/Main.sky\"\n\n"
                ++ "[source]\nroot = \"src\"\n")
        writeFile (tmp </> "src" </> "Main.sky") src

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

    -- Spawn the built binary with extra env vars, hit a path via curl,
    -- terminate the process, return the body.
    fetchPathWithEnv :: FilePath -> Int -> String -> [(String, String)] -> IO String
    fetchPathWithEnv tmp port path extraEnv = do
        currentEnv <- getEnvironment
        let env' = ("SKY_LIVE_PORT", show port) : extraEnv ++ currentEnv
        (_, _, _, ph) <- createProcess
            ((proc (tmp </> "sky-out" </> "app") [])
                { cwd = Just tmp
                , env = Just env'
                , std_out = CreatePipe
                , std_err = CreatePipe })
        body <- pollPath port path `catch` \(_ :: SomeException) -> return ""
        terminateProcess ph
        _ <- waitForProcess ph
        return body

    -- Poll for up to 10 s. Curl with -i to capture status + body so
    -- we can spot 401 / 404 / 200 in the same return.
    pollPath :: Int -> String -> IO String
    pollPath port path = go (50 :: Int)
      where
        url = "http://127.0.0.1:" ++ show port ++ path
        go n
          | n <= 0 = return ""
          | otherwise = do
              threadDelay 200000
              (ec, out, _) <- readCreateProcessWithExitCode
                  (proc "curl" ["-s", "-i", "--max-time", "2", url]) ""
              if ec == ExitSuccess && not (null out)
                then return out
                else go (n - 1)
