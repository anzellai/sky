{-# LANGUAGE ScopedTypeVariables #-}
module Sky.Build.SkyLiveHeadSpec (spec) where

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


-- v0.15.58 — Sky.Live per-page <head> injection.
--
-- Pins:
--   1. An app WITH `head = headFor` builds, runs, and emits the
--      app-supplied tags into the served <head>.
--   2. An app WITHOUT the `head` field still builds + runs
--      (the field is optional via the row-open Live.app cfg
--      record — `appExt` row var on the HM sig in
--      Sky.Type.Constrain.Expression).
--   3. The baseline runtime tags (<meta charset>, <meta sky-base>,
--      <style>) are present in both — head injection is purely
--      ADDITIVE on top of the runtime's required boilerplate.
spec :: Spec
spec = describe "Sky.Live cfg.head injection (v0.15.58)" $ do
    it "an app with a `head` callback emits the supplied tags into <head>" $ do
        sky <- findSky
        withSystemTempDirectory "sky-live-head-with" $ \tmp -> do
            writeFixture tmp fixtureWithHead
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
                then expectationFailure $
                    "sky build (with head) failed.\n" ++ out ++ "\n" ++ errOut
                else do
                    built <- doesFileExist (tmp </> "sky-out" </> "app")
                    built `shouldBe` True
                    body <- fetchPage tmp 18741
                    -- App-supplied tags present
                    ("<title>Test Page Title</title>" `isInfixOf` body) `shouldBe` True
                    ("name=\"description\"" `isInfixOf` body) `shouldBe` True
                    ("a sky.live test page" `isInfixOf` body) `shouldBe` True
                    ("rel=\"canonical\"" `isInfixOf` body) `shouldBe` True
                    ("property=\"og:title\"" `isInfixOf` body) `shouldBe` True
                    ("application/ld+json" `isInfixOf` body) `shouldBe` True
                    -- Baseline tags still present
                    ("charset=\"utf-8\"" `isInfixOf` body) `shouldBe` True
                    ("name=\"sky-base\"" `isInfixOf` body) `shouldBe` True

    it "an app WITHOUT a `head` callback still builds and serves a clean baseline <head>" $ do
        sky <- findSky
        withSystemTempDirectory "sky-live-head-without" $ \tmp -> do
            writeFixture tmp fixtureWithoutHead
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
                then expectationFailure $
                    "sky build (without head) failed.\n" ++ out ++ "\n" ++ errOut
                else do
                    built <- doesFileExist (tmp </> "sky-out" </> "app")
                    built `shouldBe` True
                    body <- fetchPage tmp 18742
                    -- App-supplied tags absent
                    ("Test Page Title" `isInfixOf` body) `shouldBe` False
                    ("og:title" `isInfixOf` body) `shouldBe` False
                    ("application/ld+json" `isInfixOf` body) `shouldBe` False
                    -- Baseline tags still present
                    ("charset=\"utf-8\"" `isInfixOf` body) `shouldBe` True
                    ("name=\"sky-base\"" `isInfixOf` body) `shouldBe` True

  where
    fixtureWithHead :: String
    fixtureWithHead =
        "module Main exposing (main)\n\n\
        \import Sky.Core.Prelude exposing (..)\n\
        \import Std.Cmd as Cmd\n\
        \import Std.Sub as Sub\n\
        \import Std.Live exposing (app)\n\
        \import Std.Live.Head as Head\n\
        \import Std.Html as Html\n\
        \import Std.Html exposing (Html)\n\n\n\
        \type alias Model = { count : Int }\n\
        \type Msg = NoOp\n\n\n\
        \init _ = ({ count = 0 }, Cmd.none)\n\n\
        \update _ model = (model, Cmd.none)\n\n\
        \subscriptions _ = Sub.none\n\n\
        \view _ =\n\
        \    Html.node \"div\" [] [ Html.text \"hi\" ]\n\n\n\
        \headFor : Model -> List (Html Msg)\n\
        \headFor _ =\n\
        \    [ Head.title \"Test Page Title\"\n\
        \    , Head.meta \"description\" \"a sky.live test page\"\n\
        \    , Head.canonical \"https://example.com/test\"\n\
        \    , Head.metaProperty \"og:title\" \"Test Page Title\"\n\
        \    , Head.jsonLd \"{\\\"@type\\\":\\\"WebPage\\\"}\"\n\
        \    ]\n\n\n\
        \main =\n\
        \    app\n\
        \        { init = init\n\
        \        , update = update\n\
        \        , view = view\n\
        \        , subscriptions = subscriptions\n\
        \        , routes = []\n\
        \        , notFound = ()\n\
        \        , head = headFor\n\
        \        }\n"

    fixtureWithoutHead :: String
    fixtureWithoutHead =
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
            ("name = \"sky-live-head-spec\"\n"
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

    -- Spawn the built binary on the given port, poll until it
    -- starts serving, GET / via `curl`, kill it, return the body.
    -- Bounded waits everywhere per CLAUDE.md §3.
    fetchPage :: FilePath -> Int -> IO String
    fetchPage tmp port = do
        currentEnv <- getEnvironment
        let env' = ("SKY_LIVE_PORT", show port) : currentEnv
        (_, _, _, ph) <- createProcess
            ((proc (tmp </> "sky-out" </> "app") [])
                { cwd = Just tmp
                , env = Just env'
                , std_out = CreatePipe
                , std_err = CreatePipe })
        body <- pollUntilServingOrFail port `catch` \(_ :: SomeException) -> return ""
        terminateProcess ph
        _ <- waitForProcess ph
        return body

    -- Poll for up to 10 s, return the body on first 200 OK.
    pollUntilServingOrFail :: Int -> IO String
    pollUntilServingOrFail port = go (50 :: Int)
      where
        url = "http://127.0.0.1:" ++ show port ++ "/"
        go n
          | n <= 0 = return ""
          | otherwise = do
              threadDelay 200000  -- 200 ms
              (ec, out, _) <- readCreateProcessWithExitCode
                  (proc "curl" ["-s", "--max-time", "2", url]) ""
              if ec == ExitSuccess && not (null out)
                then return out
                else go (n - 1)
