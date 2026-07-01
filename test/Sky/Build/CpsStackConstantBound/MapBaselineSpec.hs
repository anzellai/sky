module Sky.Build.CpsStackConstantBound.MapBaselineSpec (spec) where

import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..),
                                           compileInProcess)
import Sky.Build.CpsStackConstantBound.Shared
    ( assertHelperEmitted
    , assertNoKernelFallback
    , assertForContinueInHelper
    )


-- | Baseline CPS-stack regression for @Sky.Core.List.map@ —
-- re-encodes the v0.17 step-8 (commit 8e5dbd4f) assertions as a
-- cabal-test regression gate using the four Shared.hs combinators.
--
-- The step-8 commit shipped:
--
--   1. Sky-source rewrite of @map fn list = mapHelp fn list []@
--      with @mapHelp@ accumulating reversed + @reverseHelp@ flip,
--      both auto-TCO'd by @Sky.Build.TailCallOpt@.
--   2. The emitted Go retains typed @func(T1) T2@ lambda lowering
--      (no @func(any) any@ fallback) — proved by the typed
--      @[T1 any, T2 any]@ signature on @Sky_Core_List_map_@.
--   3. The auto-TCO loop @for { ... continue }@ lives inside
--      @Sky_Core_List_mapHelp@'s Go body, not the public
--      @Sky_Core_List_map_@ shim's.
--
-- This spec gates regression for criteria (1)+(2)+(3). The runtime
-- 5k-element fixture (asserted via @sky test@ in step-8) lives in
-- @examples/00-standard-libs/tests/MapStackTest.sky@ and is
-- exercised by the @ExampleSweepSpec@; gating it here too would
-- duplicate the subprocess cost. Future CPS rewrites
-- (@filter@ / @foldr@ / @length@ / …) each get their own
-- @<Op>Spec.hs@ module under this directory; the runtime gate
-- via @assertConstantStack1M@ runs once per op there.
spec :: Spec
spec = describe "List.map CPS rewrite — static codegen contract" $ do
    it "emits Sky_Core_List_mapHelp helper (CPS shape)" $ do
        mainGo <- compile
        assertHelperEmitted "List" "map" mainGo

    it "emits NO rt.List_map kernel fallback at user-code sites" $ do
        mainGo <- compile
        assertNoKernelFallback "List" "map" mainGo

    it "auto-TCO for-continue loop lives inside mapHelp body" $ do
        mainGo <- compile
        assertForContinueInHelper "Sky_Core_List_mapHelp" mainGo

    it "auto-TCO for-continue loop lives inside reverseHelp body" $ do
        -- map calls reverseHelp on completion to flip the
        -- accumulator. reverseHelp must also be auto-TCO'd, or
        -- the final reverse step is O(N) stack — defeating the
        -- whole rewrite.
        mainGo <- compile
        assertForContinueInHelper "Sky_Core_List_reverseHelp" mainGo


-- | Compile the small fixture and return the emitted main.go.
-- Fails the spec on any compile error (the fixture is well-typed
-- by construction — a failure here indicates a compiler
-- regression unrelated to the CPS rewrite).
compile :: IO String
compile = do
    result <- compileInProcess fixture
    case result of
        CompileErr err -> do
            expectationFailure ("Fixture compile failed:\n" ++ err)
            return ""
        CompileOk body -> return body


-- ─── Fixture ──────────────────────────────────────────────────────


-- | Minimal fixture that forces @Sky.Core.List.map@ to be reached
-- by the dependency closer and lowered into the emitted main.go.
-- A bare @import Sky.Core.List@ alone is insufficient because
-- DCE prunes unreachable defs — we call @List.map@ to keep the
-- closure live.
fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- Forces typed-lowerer to emit Sky_Core_List_map_ at a"
    , "-- concrete (Int -> Int) instantiation.  This is the same"
    , "-- shape every user-written `List.map` call takes."
    , "doubled : List Int"
    , "doubled ="
    , "    List.map (\\x -> x * 2) [ 1, 2, 3 ]"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            println (String.fromInt (List.head doubled |> Maybe.withDefault 0))"
    , "    in"
    , "        println \"map baseline cps spec\""
    ]
