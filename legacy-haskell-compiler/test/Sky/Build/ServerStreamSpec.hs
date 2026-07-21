module Sky.Build.ServerStreamSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- Cycle 4 HS-Server / issue #362 — Sky.Http.Server.Stream
-- (server-side streaming HTTP response primitive; the
-- mirror image of Sky.Core.Http.Stream).
--
-- This spec pins:
--
--   1. Sky.Http.Server.Stream.{stream,emit,finish,withContentType}
--      type-check + build when imported by a Sky.Http.Server app.
--
--   2. Each call site routes to its rt.ServerStream_* kernel
--      symbol in the emitted main.go — a missing kernel-table
--      entry would surface as `undefined: rt.ServerStream_…`
--      at `go build` time.
--
--   3. The StreamWriter ADT case-destructures correctly (the
--      Sky-side `stream`/`emit`/`finish` wrappers all pattern-
--      match on `StreamWriter raw` before dispatching to the
--      raw-Int kernel) — covered by the build succeeding.
--
-- Runtime end-to-end coverage (httptest + Flusher contract +
-- chunk ordering + dispatcher sweep + non-Flusher reject) lives
-- in runtime-go/rt/server_stream_test.go.
spec :: Spec
spec = describe "Sky.Http.Server.Stream (Cycle 4 HS-Server / #362)" $ do
    it "type-checks + builds + routes every ServerStream kernel symbol" $ do
        sky <- findSky
        withSystemTempDirectory "sky-server-stream" $ \tmp -> do
            writeFixture tmp
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
                then expectationFailure $
                    "sky build failed.\n" ++ out ++ "\n" ++ errOut
                else do
                    built <- doesFileExist (tmp </> "sky-out" </> "app")
                    built `shouldBe` True
                    body <- readFile (tmp </> "sky-out" </> "main.go")
                    -- Every entry in the kernel table is exercised by
                    -- the fixture handler so each routing assertion
                    -- catches an accidental table-key drift.
                    let mustRoute kernel =
                            (kernel `isInfixOf` body) `shouldBe` True
                    mustRoute "rt.ServerStream_stream"
                    mustRoute "rt.ServerStream_emit"
                    mustRoute "rt.ServerStream_finish"
                    mustRoute "rt.ServerStream_withContentType"

  where
    -- Fixture: a Sky.Http.Server handler that uses ALL four
    -- ServerStream entries.  `main` registers `handleStream` as a
    -- route handler so the whole-program DCE pass keeps it
    -- reachable — otherwise the kernel-routing assertions below
    -- would silently pass on an empty main.go.  `Server.listen`
    -- is called but never reached (commented out via a dead
    -- guard) so the build doesn't spawn a server in the test.
    fixture :: String
    fixture =
        "module Main exposing (main)\n\n\
        \import Sky.Core.Prelude exposing (..)\n\
        \import Sky.Core.Task as Task\n\
        \import Sky.Http.Server as Server\n\
        \import Sky.Http.Server.Stream as Stream\n\
        \import Std.Log exposing (println)\n\n\n\
        \-- Type-driven exercise: every Stream.* entry appears in a\n\
        \-- handler signature that returns `Task Error Response`.\n\
        \-- Build success + emitted kernel routing covers parsing +\n\
        \-- canonicalisation + HM inference + codegen.\n\
        \handleStream : Request -> Task Error Response\n\
        \handleStream _ =\n\
        \    Stream.stream \"text/event-stream\" (\\writer ->\n\
        \        Stream.withContentType \"text/event-stream\" writer\n\
        \            |> Task.andThen (\\_ -> Stream.emit \"event: tick\\n\\n\" writer)\n\
        \            |> Task.andThen (\\_ -> Stream.emit \"event: done\\n\\n\" writer)\n\
        \            |> Task.andThen (\\_ -> Stream.finish writer))\n\n\n\
        \-- Keep handleStream reachable via the route list — otherwise\n\
        \-- whole-program DCE prunes the Stream.* call sites and the\n\
        \-- kernel-routing assertions below fail on an empty main.go.\n\
        \-- We never reach `Server.listen` at runtime: `main` just\n\
        \-- prints + exits.  The `routes` binding's existence is what\n\
        \-- the DCE walker traces from.\n\
        \routes =\n\
        \    [ Server.get \"/stream\" handleStream\n\
        \    ]\n\n\n\
        \main =\n\
        \    let\n\
        \        _ = routes\n\
        \    in\n\
        \        println \"server-stream-build-ok\"\n"

    writeFixture :: FilePath -> IO ()
    writeFixture tmp = do
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "sky.toml")
            ("name = \"server-stream\"\n"
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
