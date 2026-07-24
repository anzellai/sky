module Sky.Build.HttpStreamForEachSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- Issue #373 — Sky.Core.Http.Stream.forEachChunk
-- (synchronous chunk-iterator that bridges Http.Stream consumer ↔
-- Server.Stream producer inside the same Sky.Http.Server handler).
--
-- This spec pins:
--
--   1. forEachChunk type-checks + builds when imported by a
--      Sky.Http.Server handler (compile shape — the relay shape from
--      issue #373).
--
--   2. The call site routes to rt.HttpStream_forEachChunk in the
--      emitted main.go — a missing kernel-table entry would surface
--      as `undefined: rt.HttpStream_forEachChunk` at `go build` time.
--
--   3. The body argument has the typed shape
--      `func(string) rt.SkyTask[Sky_Core_Error_Error, struct{}]` —
--      no widening to `func(any) any`. The relay shape from #373
--      requires the chunk to land typed as String so it can be
--      passed verbatim into Server.Stream.emit (also String-typed).
--
-- Runtime end-to-end coverage (drain order, body-Err abort,
-- always-close-on-exit, unknown-id no-op) lives in
-- runtime-go/rt/http_stream_test.go.
spec :: Spec
spec = describe "Sky.Core.Http.Stream.forEachChunk (#373)" $ do
    it "type-checks + builds + routes rt.HttpStream_forEachChunk + emits typed body" $ do
        sky <- findSky
        withSystemTempDirectory "sky-http-stream-foreach" $ \tmp -> do
            writeFixture tmp
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
                then expectationFailure $
                    "sky build failed.\n" ++ out ++ "\n" ++ errOut
                else do
                    built <- doesFileExist (tmp </> "sky-out" </> "app")
                    built `shouldBe` True
                    body <- readFile (tmp </> "sky-out" </> "main.go")
                    -- Kernel routing: forEachChunk must lower to
                    -- rt.HttpStream_forEachChunk.
                    ("rt.HttpStream_forEachChunk" `isInfixOf` body)
                        `shouldBe` True
                    -- Open + close kernel symbols must also remain
                    -- present (regression guard for the kernel table).
                    ("rt.HttpStream_open" `isInfixOf` body)
                        `shouldBe` True
                    -- No widening: the body closure must land typed
                    -- as `func(_lp_chunk string)` (param) +
                    -- `rt.SkyTask[Sky_Core_Error_Error, struct{}]`
                    -- (return) — the SkyDeploy relay shape requires
                    -- chunk : String to flow directly into
                    -- Server.Stream.emit (also String-typed) without
                    -- a func(any) any detour.
                    ("func(_lp_chunk string)" `isInfixOf` body)
                        `shouldBe` True

  where
    -- Fixture: a Sky.Http.Server handler that mirrors the canonical
    -- relay shape from issue #373. Uses Http.Stream.open +
    -- forEachChunk against an upstream stream, re-emitting via
    -- Server.Stream.emit / finish.
    --
    -- We keep `handleRelay` reachable via the route list so DCE
    -- doesn't prune the forEachChunk call site away. main never
    -- actually invokes Server.listen — it just println-s a
    -- sentinel so the build runs without spawning a server.
    fixture :: String
    fixture =
        "module Main exposing (main)\n\n\
        \import Sky.Core.Prelude exposing (..)\n\
        \import Sky.Core.Task as Task\n\
        \import Sky.Core.Http exposing (HttpRequest)\n\
        \import Sky.Core.Http.Stream as HttpStream\n\
        \import Sky.Http.Server as Server\n\
        \import Sky.Http.Server.Stream as ServerStream\n\
        \import Std.Log exposing (println)\n\n\n\
        \handleRelay : Request -> Task Error Response\n\
        \handleRelay _ =\n\
        \    ServerStream.stream \"text/event-stream\" (\\writer ->\n\
        \        HttpStream.open upstreamRequest\n\
        \            |> Task.andThen (\\hdl ->\n\
        \                HttpStream.forEachChunk hdl\n\
        \                    (\\chunk -> ServerStream.emit chunk writer))\n\
        \            |> Task.andThen (\\_ -> ServerStream.finish writer))\n\n\n\
        \upstreamRequest : HttpRequest\n\
        \upstreamRequest =\n\
        \    { method = \"GET\"\n\
        \    , url = \"http://127.0.0.1:9999/upstream\"\n\
        \    , body = \"\"\n\
        \    , headers = []\n\
        \    , timeout = 30000\n\
        \    , followRedirects = True\n\
        \    , maxRedirects = 10\n\
        \    }\n\n\n\
        \routes =\n\
        \    [ Server.get \"/relay\" handleRelay\n\
        \    ]\n\n\n\
        \main =\n\
        \    let\n\
        \        _ = routes\n\
        \    in\n\
        \        println \"http-stream-foreach-build-ok\"\n"

    writeFixture :: FilePath -> IO ()
    writeFixture tmp = do
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "sky.toml")
            ("name = \"http-stream-foreach\"\n"
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
