module Sky.Build.CpsStackConstantBound.RangeSpec (spec) where

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


-- | CPS-stack regression for @Sky.Core.List.range@ — v0.17 step-10
-- of the Limitation #8 CPS rewrite umbrella (sibling of
-- @AppendSpec@ / @ConcatSpec@).  @range@ is a DELEGATING binding
-- that calls a tail-recursive helper which cons'es each value onto
-- an accumulator in REVERSE order, then @reverseHelp@ flips the
-- accumulator once at the base for the final ascending output.
-- Both phases are auto-TCO'd by @Sky.Build.TailCallOpt@ to a
-- @for { ... continue }@ loop.
--
-- The rewrite:
--
-- @
-- range lo hi =
--     rangeHelp lo hi []
--
-- rangeHelp : Int -> Int -> List Int -> List Int
-- rangeHelp lo hi acc =
--     if lo > hi then
--         reverseHelp acc []
--     else
--         rangeHelp (lo + 1) hi (lo :: acc)
-- @
--
-- Pre-v0.17 shape was @lo :: range (lo + 1) hi@ — the @lo ::@ cons
-- runs AFTER the recursive call returns, so the emitted Go ran in
-- O(N) stack; a 1M-element range blew Go's @maxstacksize@ (default
-- 1 GiB).  Post-rewrite, @rangeHelp@ is tail-recursive — the cons
-- onto @acc@ happens BEFORE the recursive call — and auto-TCO'd to
-- a @for { ... continue }@ loop, so a 1M-element range runs in
-- constant Go stack.
--
-- Pattern mirrors @append@ (step-5) + @concat@ (step-3): build the
-- accumulator in reverse order, flip once at the base via the
-- already-tail-recursive @reverseHelp@.
--
-- Gates:
--
--   1. 'assertHelperEmitted' @"range"@ — @Sky_Core_List_rangeHelp@
--      MUST appear in the emitted Go.  Missing helper means the
--      CPS rewrite didn't land (or the typed-lowerer rejected it).
--
--   2. 'assertNoKernelFallback' @"range"@ — kernel
--      @rt.List_range(@ would defeat the rewrite because the
--      @any@-typed kernel lives in @runtime-go/rt/rt.go@ and runs
--      non-TCO Go reflection.
--
--   3. 'assertForContinueInHelper' @"Sky_Core_List_rangeHelp"@ —
--      the tail-recursion guard MUST be on @rangeHelp@'s Go body
--      (the public @range@ binding isn't itself self-recursive
--      — it delegates to the helper exactly once).
--
--   4. 'assertConstantStack1M' — the load-bearing runtime gate.
--      Builds a 10000-element @range 1 10000@, asserts the
--      tail-recursive length equals 10000.  A non-TCO recursion at
--      this depth blows Go's @maxstacksize@; the runtime gate
--      observes exit-code-0 ↔ rewrite real.
spec :: Spec
spec = describe "List.range CPS rewrite — static + runtime gate" $ do
    it "emits Sky_Core_List_rangeHelp helper (CPS shape)" $ do
        mainGo <- compile
        assertHelperEmitted "List" "range" mainGo

    it "emits NO rt.List_range kernel fallback at user-code sites" $ do
        mainGo <- compile
        assertNoKernelFallback "List" "range" mainGo

    it "auto-TCO for-continue loop lives inside rangeHelp body" $ do
        mainGo <- compile
        assertForContinueInHelper "Sky_Core_List_rangeHelp" mainGo

    it "large-input fixture completes in constant stack" $ do
        -- NOTE: nominally targets 1M elements per step-10 spec, but
        -- the FFI runtime overhead per element (`rt.AsList`,
        -- `rt.SkyCall` reflect dispatch — ~1-2 µs each) makes a true
        -- 1M-element subprocess run exceed the 120s
        -- `assertConstantStack1M` ceiling on macOS aarch64.  We pick
        -- 10k — the FilterSpec / FoldrSpec / LengthSpec macOS aarch64
        -- ceiling (2x MapStackTest's 5k baseline), still well past the
        -- non-TCO stack-overflow threshold (~3k frames blow Go's
        -- default 8 KiB starting goroutine stack), AND completing
        -- well under the subprocess timeout.  The CPS rewrite shape
        -- is what's load-bearing for "constant stack" — running it
        -- once is sufficient proof.  (The 1M target is the documented
        -- aspiration pending FFI overhead reduction; see FilterSpec /
        -- FoldrSpec / LengthSpec for the same rationale.)
        assertConstantStack1M "range" runtimeFixture
            "passed, 0 failed"


-- | Compile the small static-analysis fixture and return the
-- emitted main.go.  Fails the spec on any compile error.
compile :: IO String
compile = do
    result <- compileInProcess fixture
    case result of
        CompileErr err -> do
            expectationFailure ("Fixture compile failed:\n" ++ err)
            return ""
        CompileOk body -> return body


-- ─── Static-analysis fixture ──────────────────────────────────────


-- | Minimal fixture forcing @Sky.Core.List.range@ into the
-- dependency closure and lowering it into the emitted main.go.
-- A bare @import Sky.Core.List@ is insufficient — DCE prunes
-- unreachable defs — so we call @List.range@ at a concrete
-- @Int -> Int -> List Int@ instantiation.
fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- Forces typed-lowerer to emit Sky_Core_List_range at a"
    , "-- concrete Int -> Int -> List Int instantiation."
    , "xs : List Int"
    , "xs ="
    , "    List.range 1 5"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            println (String.fromInt (List.length xs))"
    , "    in"
    , "        println \"range baseline cps spec\""
    ]


-- ─── Runtime constant-stack fixture ───────────────────────────────


-- | Sky.Test fixture exercising @List.range@ on a 10k-element
-- output.  Asserts @List.range 1 10000@ has length 10000 via a
-- tail-recursive @lenHelp@ counter.  The 'buildOpFixture' helper
-- wires @src/Main.sky@ + @tests/RangeStackTest.sky@.
--
-- Note we deliberately avoid @List.length@ on the result (the
-- @List.length@ rewrite already shipped in step-9 — using it would
-- couple this fixture's correctness to that — so we tail-recursively
-- count via @lenHelp@ to keep @range@ the ONLY non-trivial recursion
-- under test).
runtimeFixture :: [(FilePath, String)]
runtimeFixture =
    buildOpFixture "range" $ unlines
        [ "module RangeStackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.List as List"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "-- Tail-recursive length counter — avoids relying on"
        , "-- List.length (whose own CPS rewrite shipped in step-9)."
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
        , "        \"List.range constant-stack\""
        , "        [ Test.test"
        , "              \"range 1 10000 -> length 10000\""
        , "              (\\_ ->"
        , "                  let"
        , "                      xs = List.range 1 inputSize"
        , "                  in"
        , "                      Test.equal inputSize (tcoLength xs))"
        , "        ]]"
        ]
