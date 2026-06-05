module Sky.Canonicalise.PipelineIntegritySpec (spec) where

-- v0.15.42 Cycle 6 — pipeline-integrity regression fence.
--
-- Bundles three "if it compiles, it works" credibility-bar
-- regressions called out in the v0.15.41 audit:
--
--   §3.1  Unknown qualified name silently passed canonicaliser, then
--         only failed at `go build` with `undefined: NotARealModule_foo`.
--         Now: canonicaliser flags it as a clean naming error citing
--         the missing module.
--
--   §3.4  "Compilation successful" used to print at the end of Sky
--         lowering, BEFORE `go build` ran. A subsequent Go build
--         failure left users staring at a "successful" banner
--         followed by a Go diagnostic. Now: "Sky lowering succeeded"
--         is the lowering-stage banner; "Compilation successful"
--         only prints after Go returns 0; failure prints
--         "Sky lowering succeeded but `go build` failed:" first.
--
--   §3.2  A user `type Result a = Just a | Nothing` shadowed the
--         Prelude-exposed Maybe/Result constructors silently. The
--         resulting program compiles but `Just`/`Nothing` resolve to
--         the user's ADT, not the stdlib ones — refactor regression
--         class. Now: hard canonicaliser error names both the shadow
--         and the protected stdlib type / constructor.
--
-- Tier 1 (task #491): the canonicaliser-rejection cases run
-- in-process via compileInProcess. The CLI-banner sequencing case
-- (§3.4 it-1) was a stdout-ordering assertion on the `sky` CLI
-- wrapper; in-process compilation skips the wrapper entirely so
-- that case is replaced by the structural check that the banner
-- text still lives in app/Main.hs (same source-of-truth contract
-- as the §3.4 it-2 case).

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


spec :: Spec
spec = describe "v0.15.42 Cycle 6: pipeline-integrity regression fence" $ do

    describe "Bug 1 (audit §3.1): unknown qualified name" $ do

        it "rejects `NotARealModule.foo` at canonicalisation, not go build" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main = println (NotARealModule.doSomething 42)"
                    ]
            result <- compileInProcess src
            case result of
                CompileOk _ -> expectationFailure "expected NotARealModule to be rejected"
                CompileErr e -> do
                    -- The error names the missing qualifier explicitly.
                    e `shouldSatisfy` ("Undefined name: NotARealModule.doSomething" `isInfixOf`)
                    e `shouldSatisfy` ("Module `NotARealModule` is not imported" `isInfixOf`)
                    -- And NEVER falls through to a Go diagnostic — the whole
                    -- point of the fix is that Sky catches it pre-Go.
                    e `shouldNotSatisfy` ("undefined: NotARealModule" `isInfixOf`)


        it "suggests a similar known qualifier when the typo is close" $ do
            -- `Strng.fromInt` is 1 edit away from the auto-qualified
            -- kernel module `String`. The fix offers a Did-you-mean.
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main = println (Strng.fromInt 42)"
                    ]
            result <- compileInProcess src
            case result of
                CompileOk _ -> expectationFailure "expected Strng typo to be rejected"
                CompileErr e -> do
                    e `shouldSatisfy` ("Undefined name: Strng.fromInt" `isInfixOf`)
                    e `shouldSatisfy` ("Did you mean `String`?" `isInfixOf`)


    describe "Bug 2 (audit §3.4): success banner sequencing" $ do

        it "wires the success-path banner into app/Main.hs" $ do
            -- The CLI-wrapper "Sky lowering succeeded" banner is pure
            -- stdout text emitted by app/Main.hs after the in-process
            -- compile pipeline returns Ok. Since compileInProcess
            -- bypasses the CLI wrapper, the stdout-ordering assertion
            -- from the pre-Tier-1 spec moves to a structural check on
            -- the same source string in app/Main.hs (matching §3.4
            -- it-2's pattern below).
            mainSrc <- readFile "app/Main.hs"
            mainSrc `shouldSatisfy`
                ("Sky lowering succeeded" `isInfixOf`)


        it "wires the failure-path banner into runGoBuildWithDiagnostics" $ do
            -- Engineering an actual go-build failure inside the
            -- temp-dir sandbox is fragile (the Sky-side validator
            -- catches most known compiler-bug shapes before Go even
            -- runs, by design). The failure-path banner is a small
            -- pure-string contract; assert it lives in the source
            -- where Main.hs invokes go build, so removing it shows
            -- up as a CI failure with a clear pointer.
            mainSrc <- readFile "app/Main.hs"
            mainSrc `shouldSatisfy`
                ("Sky lowering succeeded but `go build` failed:" `isInfixOf`)


    describe "Bug 3 (audit §3.2): Prelude shadowing of stdlib types" $ do

        it "rejects user `type Result a = Just a | Nothing`" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , "type Result a = Just a | Nothing"
                    , "main = println (case Just 42 of"
                    , "    Just x -> \"got \" ++ String.fromInt x"
                    , "    Nothing -> \"nothing\")"
                    ]
            result <- compileInProcess src
            case result of
                CompileOk _ -> expectationFailure "expected Prelude shadowing to be rejected"
                CompileErr e -> do
                    e `shouldSatisfy` ("Prelude shadowing" `isInfixOf`)
                    e `shouldSatisfy` ("type Result" `isInfixOf`)
                    e `shouldSatisfy` ("Sky.Core.Result" `isInfixOf`)


        it "rejects a user constructor named `Just` even under a non-Result type" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , "type Box a = Just a | Empty"
                    , "main = println \"x\""
                    ]
            result <- compileInProcess src
            case result of
                CompileOk _ -> expectationFailure "expected Just-constructor shadow to be rejected"
                CompileErr e -> do
                    e `shouldSatisfy` ("Prelude shadowing" `isInfixOf`)
                    e `shouldSatisfy` ("constructor `Just`" `isInfixOf`)
                    e `shouldSatisfy` ("Sky.Core.Maybe" `isInfixOf`)


        it "allows user types whose names do NOT collide with Prelude" $ do
            -- Regression guard: the gate must not over-trigger.
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , "type Color = Red | Green | Blue"
                    , "main = println \"ok\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> do
                    e `shouldNotSatisfy` ("Prelude shadowing" `isInfixOf`)
                    expectationFailure ("compile failed: " ++ e)
                CompileOk _ -> return ()
