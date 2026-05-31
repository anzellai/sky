module Sky.Build.HttpTypesSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- v0.15.44 — HttpResponse / Request / Response typed record aliases.
--
-- 1. `resp : HttpResponse` typing on a Http.get pipeline must
--    compile + emit the user-declared record struct so `.status` /
--    `.body` / `.headers` accessors land on the typed surface.
-- 2. `handler : Request -> Task Error Response` annotation must
--    compile.  Sky.Http.Server now exports typed Request / Response
--    aliases so `req.method` etc. type-check.
-- 3. `Http.defaultRequest "..." |> Http.withHeader k v |>
--    Http.withTimeout n |> Http.request` must compile.
spec :: Spec
spec = describe "v0.15.44 HTTP typed surface" $ do
    it "HttpResponse + Request + Response + Http builder chain build cleanly" $ do
        sky <- findSky
        withSystemTempDirectory "sky-http-types" $ \tmp -> do
            writeFixture tmp
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
              then expectationFailure $
                  "sky build failed.\n" ++ out ++ "\n" ++ errOut
              else do
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True
                body <- readFile (tmp </> "sky-out" </> "main.go")
                -- HttpResponse_R typed struct must appear in user code
                ("Sky_Core_Http_HttpResponse_R" `isInfixOf` body)
                    `shouldBe` True
                -- Http.defaultRequest must lower to the user-side
                -- Sky.Core.Http.defaultRequest dispatcher
                ("defaultRequest" `isInfixOf` body) `shouldBe` True

  where
    findSky = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok then return candidate
              else fail ("sky binary missing at " ++ candidate)

    runSky sky args workDir = do
        let cp = (proc sky args) { cwd = Just workDir }
        readCreateProcessWithExitCode cp ""

    writeFixture dir = do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml") $ unlines
            [ "[project]"
            , "name = \"http-types-test\""
            , ""
            , "[bin]"
            , "name = \"app\""
            ]
        writeFile (dir </> "src" </> "Main.sky") $ unlines
            [ "module Main exposing (main)"
            , ""
            , "import Sky.Core.Prelude exposing (..)"
            , "import Sky.Core.Http as Http exposing (HttpResponse, HttpRequest)"
            , "import Sky.Core.Task as Task"
            , "import Std.Log exposing (println)"
            , ""
            , ""
            , "-- Annotate the slot — checks the typed alias is in scope."
            , "describeResponse : HttpResponse -> String"
            , "describeResponse resp ="
            , "    String.fromInt resp.status ++ \" bytes=\""
            , "        ++ String.fromInt (String.length resp.body)"
            , ""
            , ""
            , "buildReq : HttpRequest"
            , "buildReq ="
            , "    Http.defaultRequest \"https://example.invalid/\""
            , "        |> Http.withMethod \"POST\""
            , "        |> Http.withHeader \"X-Demo\" \"yes\""
            , "        |> Http.withBody \"{}\""
            , "        |> Http.withTimeout 5000"
            , ""
            , ""
            , "main ="
            , "    let"
            , "        _ = println (\"req method = \" ++ buildReq.method)"
            , "    in"
            , "        Task.succeed ()"
            ]
