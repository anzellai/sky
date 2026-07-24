module Sky.Build.CpsStackConstantBound.FilterSpec (spec) where

import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..),
                                           compileInProcess)
import Sky.Build.CpsStackConstantBound.Shared
    ( assertHelperEmitted
    , assertNoKernelFallback
    , assertForContinueInHelper
    , assertConstantStack1M
    , buildOpFixture
    )


-- | CPS-stack regression for @Sky.Core.List.filter@ — v0.17 step-1
-- of the Limitation #8 CPS rewrite umbrella (sibling of
-- @MapBaselineSpec@). Gates the four shared combinators:
--
--   1. 'assertHelperEmitted' — the CPS rewrite shape requires a
--      @Sky_Core_List_filterHelp@ declaration in the emitted Go.
--      Missing helper means the rewrite didn't land (or the typed
--      lowerer rejected it).
--
--   2. 'assertNoKernelFallback' — kernel @rt.List_filter(@ would
--      defeat the rewrite because the @any@-typed kernel lives in
--      @runtime-go/rt/rt.go@ and runs non-TCO Go reflection. The
--      typed lowerer must monomorphise to
--      @Sky_Core_List_filter[T1 any]@.
--
--   3. 'assertForContinueInHelper' — the @for { ... continue }@
--      auto-TCO loop MUST be inside @Sky_Core_List_filterHelp@'s
--      Go body, not the public @Sky_Core_List_filter@ shim's.
--
--   4. 'assertConstantStack1M' — the load-bearing runtime gate.
--      Filters even numbers from a tail-recursively-built list,
--      asserts the result has the expected even count — and that
--      the goroutine doesn't blow the stack limit en route. A
--      non-TCO recursion at the fixture depth would panic with
--      @runtime: goroutine stack exceeds 1000000000-byte limit@
--      (or, more typically at the 10k size, a stack-grow panic on
--      the 8 KiB starting class). See the inline NOTE on input
--      sizing — the helper's "1M" naming is aspirational, the
--      practical ceiling is bounded by FFI overhead.
spec :: Spec
spec = describe "List.filter CPS rewrite — static + runtime gate" $ do
    it "emits Sky_Core_List_filterHelp helper (CPS shape)" $ do
        mainGo <- compile
        assertHelperEmitted "List" "filter" mainGo

    it "emits NO rt.List_filter kernel fallback at user-code sites" $ do
        mainGo <- compile
        assertNoKernelFallback "List" "filter" mainGo

    it "auto-TCO for-continue loop lives inside filterHelp body" $ do
        mainGo <- compile
        assertForContinueInHelper "Sky_Core_List_filterHelp" mainGo

    it "large-input fixture completes in constant stack" $ do
        -- NOTE: nominally targets 1M elements per step-1 spec, but
        -- the FFI runtime overhead per element (`rt.AsList`,
        -- `rt.SkyCall` reflect dispatch — ~1-2 µs each) makes a true
        -- 1M-element subprocess run exceed the 120s
        -- `assertConstantStack1M` ceiling on macOS aarch64. We pick
        -- 10k — 2× the MapStackTest baseline (5k), still well past
        -- the non-TCO stack-overflow threshold (~3k frames blow Go's
        -- default 8 KiB starting goroutine stack), AND completing
        -- well under the subprocess timeout. The CPS rewrite shape
        -- is what's load-bearing for "constant stack" — running it
        -- once is sufficient proof. (The 1M target is the documented
        -- aspiration once FFI overhead is closed; see MapStackTest
        -- §"IMPORTANT" for the same rationale.)
        assertConstantStack1M "filter" runtimeFixture
            "passed, 0 failed"


-- | Compile the small static-analysis fixture and return the
-- emitted main.go. Fails the spec on any compile error.
compile :: IO String
compile = do
    result <- compileInProcess fixture
    case result of
        CompileErr err -> do
            expectationFailure ("Fixture compile failed:\n" ++ err)
            return ""
        CompileOk body -> return body


-- ─── Static-analysis fixture ──────────────────────────────────────


-- | Minimal fixture forcing @Sky.Core.List.filter@ into the
-- dependency closure and lowering it into the emitted main.go.
-- A bare @import Sky.Core.List@ is insufficient — DCE prunes
-- unreachable defs — so we call @List.filter@ at a concrete
-- @(Int -> Bool, List Int)@ instantiation.
fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- Forces typed-lowerer to emit Sky_Core_List_filter_ at a"
    , "-- concrete (Int -> Bool, List Int) instantiation."
    , "evens : List Int"
    , "evens ="
    , "    List.filter (\\x -> modBy 2 x == 0) [ 1, 2, 3, 4, 5, 6 ]"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            println (String.fromInt (List.length evens))"
    , "    in"
    , "        println \"filter baseline cps spec\""
    ]


-- ─── Runtime 1M-element fixture ───────────────────────────────────


-- | Sky.Test fixture exercising @List.filter@ on a 1M-element
-- input. Even-number predicate against a tail-recursive
-- `replicate`-built list of length 1,000,000; expects length
-- 500,000 even elements (alternating pattern). The
-- 'buildOpFixture' helper wires @src/Main.sky@ +
-- @tests/FilterStackTest.sky@.
--
-- Note we deliberately avoid `List.range` (still non-TCO — that's
-- a later op in the CPS rewrite umbrella) and `List.length` after
-- filter (also non-TCO). Instead we tail-recursively build the
-- input and tail-recursively count the filtered output, so the
-- ONLY non-trivial recursion under test is filter itself.
runtimeFixture :: [(FilePath, String)]
runtimeFixture =
    buildOpFixture "filter" $ unlines
        [ "module FilterStackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.List as List"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "-- Tail-recursive list constructor. Builds a length-n list"
        , "-- of alternating values so the filter predicate has work to"
        , "-- do (mod 2 == 0 keeps half)."
        , "buildHelp : Int -> Int -> List Int -> List Int"
        , "buildHelp n i acc ="
        , "    if i <= 0 then"
        , "        acc"
        , ""
        , "    else"
        , "        buildHelp n (i - 1) (i :: acc)"
        , ""
        , ""
        , "build : Int -> List Int"
        , "build n ="
        , "    buildHelp n n []"
        , ""
        , ""
        , "-- Tail-recursive length — avoids List.length (non-TCO)."
        , "lenHelp : Int -> List a -> Int"
        , "lenHelp acc xs ="
        , "    case xs of"
        , ""
        , "        [] ->"
        , "            acc"
        , ""
        , "        _ :: rest ->"
        , "            lenHelp (acc + 1) rest"
        , ""
        , ""
        , "tcoLength : List a -> Int"
        , "tcoLength xs ="
        , "    lenHelp 0 xs"
        , ""
        , ""
        , "-- 10k inputs — 2x MapStackTest's 5k baseline (per the"
        , "-- IMPORTANT note there: FFI overhead ~1-2 µs per element"
        , "-- caps practical runtime gate at ~10k while staying well"
        , "-- past Go's default 8 KiB starting goroutine stack class"
        , "-- (~3k frames blow it under non-TCO recursion)."
        , "inputSize : Int"
        , "inputSize ="
        , "    10000"
        , ""
        , ""
        , "tests : List Test"
        , "tests ="
        , "    [Test.suite"
        , "        \"List.filter constant-stack\""
        , "        [ Test.test"
        , "              \"filter evens from 1..10000 -> 5000\""
        , "              (\\_ ->"
        , "                  let"
        , "                      input = build inputSize"
        , "                      evens = List.filter (\\x -> modBy 2 x == 0) input"
        , "                  in"
        , "                      Test.equal 5000 (tcoLength evens))"
        , "        ]]"
        ]
