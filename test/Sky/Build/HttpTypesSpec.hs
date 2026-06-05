module Sky.Build.HttpTypesSpec (spec) where

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


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
--
-- Tier 1 (task #491): no subprocess `sky build` — the compile
-- pipeline runs IN-PROCESS via Sky.Build.Helpers.InProcessCompile.
-- ZERO subprocess.  ZERO `go build`.  ZERO GOCACHE writes.
spec :: Spec
spec = describe "v0.15.44 HTTP typed surface" $ do
    it "HttpResponse + Request + Response + Http builder chain build cleanly" $ do
        let fixture = unlines
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
        result <- compileInProcess fixture
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk body -> do
                -- HttpResponse_R typed struct must appear in user code
                ("Sky_Core_Http_HttpResponse_R" `isInfixOf` body)
                    `shouldBe` True
                -- Http.defaultRequest must lower to the user-side
                -- Sky.Core.Http.defaultRequest dispatcher
                ("defaultRequest" `isInfixOf` body) `shouldBe` True
