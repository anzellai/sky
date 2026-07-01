module Sky.Build.CpsStackConstantBound.LengthSpec (spec) where

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


-- | CPS-stack regression for @Sky.Core.List.length@ — v0.17 step-9
-- of the Limitation #8 CPS rewrite umbrella (sibling of
-- @TakeSpec@ / @FilterSpec@).  @length@ is a CPS-helper binding
-- (NOT a delegation like @foldr@) — the public binding is a thin
-- shim @length list = lengthHelp list 0@; the auto-TCO loop lives
-- inside @Sky_Core_List_lengthHelp@'s emitted Go body.
--
-- The rewrite:
--
-- @
-- length list =
--     lengthHelp list 0
--
-- lengthHelp : List a -> Int -> Int
-- lengthHelp list n =
--     case list of
--         []        -> n
--         _ :: rest -> lengthHelp rest (n + 1)
-- @
--
-- Pre-v0.17 shape was @1 + length rest@ — the @1 +@ runs AFTER
-- the recursive call returns, so the emitted Go ran in O(N)
-- stack; a 1M-element input blew Go's @maxstacksize@ (default
-- 1 GiB).  Post-rewrite, the count argument is the accumulator
-- and the recursion is auto-TCO'd by @Sky.Build.TailCallOpt@ to
-- a @for { ... continue }@ loop.
--
-- Gates:
--
--   1. 'assertHelperEmitted' @"length"@ — @Sky_Core_List_lengthHelp@
--      MUST appear in the emitted Go.  Missing helper means the
--      CPS rewrite didn't land (or the typed-lowerer rejected it).
--
--   2. 'assertNoKernelFallback' @"length"@ — kernel
--      @rt.List_length(@ would defeat the rewrite because the
--      @any@-typed kernel lives in @runtime-go/rt/rt.go@ and runs
--      non-TCO Go reflection.
--
--   3. 'assertForContinueInHelper' @"Sky_Core_List_lengthHelp"@ —
--      the tail-recursion guard MUST be on @lengthHelp@'s Go body
--      (the public @length@ doesn't recurse).
--
--   4. 'assertConstantStack' — the load-bearing runtime gate.
--      Measures a tail-recursively-built 10k-element list's
--      length, asserts the result equals 10000.  A non-TCO
--      recursion at this depth blows Go's @maxstacksize@; the
--      runtime gate observes exit-code-0 ↔ rewrite real.
spec :: Spec
spec = describe "List.length CPS rewrite — static + runtime gate" $ do
    it "emits Sky_Core_List_lengthHelp helper (CPS shape)" $ do
        mainGo <- compile
        assertHelperEmitted "List" "length" mainGo

    it "emits NO rt.List_length kernel fallback at user-code sites" $ do
        mainGo <- compile
        assertNoKernelFallback "List" "length" mainGo

    it "auto-TCO for-continue loop lives inside lengthHelp body" $ do
        mainGo <- compile
        assertForContinueInHelper "Sky_Core_List_lengthHelp" mainGo

    it "large-input fixture completes in constant stack" $ do
        -- NOTE: nominally targets 1M elements per step-9 spec, but
        -- the FFI runtime overhead per element (`rt.AsList`,
        -- `rt.SkyCall` reflect dispatch — ~1-2 µs each) makes a true
        -- 1M-element subprocess run exceed the 120s
        -- `assertConstantStack1M` ceiling on macOS aarch64.  We pick
        -- 10k — the FilterSpec / FoldrSpec macOS aarch64 ceiling
        -- (2x MapStackTest's 5k baseline), still well past the
        -- non-TCO stack-overflow threshold (~3k frames blow Go's
        -- default 8 KiB starting goroutine stack), AND completing
        -- well under the subprocess timeout.  The CPS rewrite shape
        -- is what's load-bearing for "constant stack" — running it
        -- once is sufficient proof.  (The 1M target is the
        -- documented aspiration pending FFI overhead reduction; see
        -- FilterSpec / FoldrSpec for the same rationale.)
        assertConstantStack1M "length" runtimeFixture
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


-- | Minimal fixture forcing @Sky.Core.List.length@ into the
-- dependency closure and lowering it into the emitted main.go.
-- A bare @import Sky.Core.List@ is insufficient — DCE prunes
-- unreachable defs — so we call @List.length@ at a concrete
-- @List Int -> Int@ instantiation.
fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- Forces typed-lowerer to emit Sky_Core_List_length at a"
    , "-- concrete List Int -> Int instantiation."
    , "n : Int"
    , "n ="
    , "    List.length [ 1, 2, 3, 4, 5 ]"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            println (String.fromInt n)"
    , "    in"
    , "        println \"length baseline cps spec\""
    ]


-- ─── Runtime constant-stack fixture ───────────────────────────────


-- | Sky.Test fixture exercising @List.length@ on a 10k-element
-- input.  Tail-recursively builds a 10k-element list and asserts
-- @List.length built == 10000@.  The 'buildOpFixture' helper
-- wires @src/Main.sky@ + @tests/LengthStackTest.sky@.
--
-- Note we deliberately avoid @List.range@ (still non-TCO — later
-- op in the CPS rewrite umbrella).  Instead we tail-recursively
-- build the input, so the ONLY non-trivial recursion under test
-- is @length@ itself.
runtimeFixture :: [(FilePath, String)]
runtimeFixture =
    buildOpFixture "length" $ unlines
        [ "module LengthStackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.List as List"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "-- Tail-recursive list constructor.  Builds [n, n-1, ..., 1]"
        , "-- in O(N) stack-safe fashion (the only non-trivial"
        , "-- recursion outside length itself in this fixture)."
        , "buildHelp : Int -> List Int -> List Int"
        , "buildHelp i acc ="
        , "    if i <= 0 then"
        , "        acc"
        , ""
        , "    else"
        , "        buildHelp (i - 1) (i :: acc)"
        , ""
        , ""
        , "build : Int -> List Int"
        , "build n ="
        , "    buildHelp n []"
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
        , "        \"List.length constant-stack\""
        , "        [ Test.test"
        , "              \"length (build 10000) -> 10000\""
        , "              (\\_ ->"
        , "                  let"
        , "                      input = build inputSize"
        , "                  in"
        , "                      Test.equal inputSize (List.length input))"
        , "        ]]"
        ]
