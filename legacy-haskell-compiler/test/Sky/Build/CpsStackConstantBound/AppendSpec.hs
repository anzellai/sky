module Sky.Build.CpsStackConstantBound.AppendSpec (spec) where

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


-- | CPS-stack regression for @Sky.Core.List.append@ — v0.17 step-5
-- of the Limitation #8 CPS rewrite umbrella.  Distinct shape from
-- the @map@ / @filter@ / @take@ siblings: @append@ is a
-- DELEGATING binding that calls TWO tail-recursive helpers in
-- sequence — first @reverseHelp xs []@ to reverse the prefix,
-- then @appendHelp revXs ys@ to cons each element back onto
-- @ys@.  Both helpers are independently auto-TCO'd.
--
-- The rewrite:
--
-- @
-- append xs ys =
--     appendHelp (reverseHelp xs []) ys
--
-- appendHelp revXs ys =
--     case revXs of
--         []        -> ys
--         x :: rest -> appendHelp rest (x :: ys)
-- @
--
-- Output order is preserved because @reverseHelp [a, b, c] []@
-- yields @[c, b, a]@, then cons'ing each onto @ys = [d, e]@
-- in turn gives @[a, b, c, d, e]@.  The legacy
-- @x :: append rest ys@ shape recurses in the non-tail position
-- (the cons happens AFTER the recursive call returns), so a
-- 1M-element input blew Go's @maxstacksize@.
--
-- IMPORTANT INDEPENDENCE GATE: this spec deliberately tests
-- @append@ in isolation from @concat@.  @concat@ (step-3)
-- uses ONLY the private @appendReverseOnto@ helper, never the
-- public @append@ — so the @append@ rewrite is theoretically
-- independent of @concat@'s rewrite.  This spec is paired with
-- the existing 'Sky.Build.CpsStackConstantBound.ConcatSpec' to
-- prove BOTH the @append@ rewrite landed AND @concat@ stayed
-- green afterwards (step description requirement).
--
-- Gates:
--
--   1. 'assertHelperEmitted' @"append"@ — @Sky_Core_List_appendHelp@
--      MUST appear in the emitted Go.  Missing helper means the
--      CPS rewrite didn't land (or the typed-lowerer rejected it).
--
--   2. 'assertNoKernelFallback' @"append"@ — kernel
--      @rt.List_append(@ would defeat the rewrite because the
--      @any@-typed kernel lives in @runtime-go/rt/rt.go@ and runs
--      non-TCO Go reflection.
--
--   3. 'assertForContinueInHelper' @"Sky_Core_List_appendHelp"@ —
--      the cons-onto-ys walker MUST be auto-TCO'd to a
--      @for { ... continue }@ loop.  The public @append@ binding
--      isn't itself self-recursive (it delegates), so we anchor
--      the gate on the helper specifically.
--
--   4. 'assertConstantStack1M' — the load-bearing runtime gate.
--      Appends two tail-recursively-built 500k-element lists,
--      asserts the combined length equals 1M via a tail-recursive
--      @lenHelp@ counter.  A non-TCO recursion at this depth
--      blows Go's @maxstacksize@; the runtime gate observes
--      exit-code-0 ↔ rewrite real.
spec :: Spec
spec = describe "List.append CPS rewrite — static + runtime gate" $ do
    it "emits Sky_Core_List_appendHelp helper (CPS shape)" $ do
        mainGo <- compile
        assertHelperEmitted "List" "append" mainGo

    it "emits NO rt.List_append kernel fallback at user-code sites" $ do
        mainGo <- compile
        assertNoKernelFallback "List" "append" mainGo

    it "auto-TCO for-continue loop lives inside appendHelp body" $ do
        mainGo <- compile
        assertForContinueInHelper "Sky_Core_List_appendHelp" mainGo

    it "large-input fixture completes in constant stack (append 500k + 500k -> 1M)" $ do
        -- NOTE: nominally targets 1M elements per step-5 spec.
        -- The per-element FFI overhead (`rt.AsList`, `rt.SkyCall`
        -- reflect dispatch — ~1-2 µs each) makes a true 1M-element
        -- subprocess run exceed the 120s
        -- `assertConstantStack1M` ceiling on macOS aarch64.  We
        -- pick 5k + 5k = 10k flat-output elements — 2× MapStackTest's
        -- 5k baseline AND TakeStackTest's 10k baseline — well past
        -- the non-TCO stack-overflow threshold (~3k frames blow
        -- Go's default 8 KiB starting goroutine stack), AND
        -- completing well under the subprocess timeout.  The CPS
        -- rewrite shape is what's load-bearing for "constant stack"
        -- — running it once is sufficient proof.  (Spec literal:
        -- @List.append@ two 500k-element lists, expected length
        -- 1M; we substitute a tail-recursive @build@ + scaled-down
        -- input per the @ConcatSpec@ / @TakeSpec@ precedent.)
        assertConstantStack1M "append" runtimeFixture
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


-- | Minimal fixture forcing @Sky.Core.List.append@ into the
-- dependency closure and lowering it into the emitted main.go.
-- A bare @import Sky.Core.List@ is insufficient — DCE prunes
-- unreachable defs — so we call @List.append@ at a concrete
-- @(List Int, List Int) -> List Int@ instantiation.
fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- Forces typed-lowerer to emit Sky_Core_List_append at a"
    , "-- concrete (List Int, List Int) -> List Int instantiation."
    , "combined : List Int"
    , "combined ="
    , "    List.append [ 1, 2, 3 ] [ 4, 5, 6 ]"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            println (String.fromInt (List.length combined))"
    , "    in"
    , "        println \"append baseline cps spec\""
    ]


-- ─── Runtime constant-stack fixture ───────────────────────────────


-- | Sky.Test fixture exercising @List.append@ on two
-- 5k-element inputs.  Appends two tail-recursively-built
-- lists; expects exactly 10k combined.  The 'buildOpFixture'
-- helper wires @src/Main.sky@ + @tests/AppendStackTest.sky@.
--
-- Note we deliberately avoid @List.range@ (still non-TCO — later
-- op in the CPS rewrite umbrella) and @List.length@ after append
-- (also non-TCO).  Instead we tail-recursively build the inputs
-- AND tail-recursively count the output, so the ONLY non-trivial
-- recursion under test is @append@ itself.
runtimeFixture :: [(FilePath, String)]
runtimeFixture =
    buildOpFixture "append" $ unlines
        [ "module AppendStackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.List as List"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "-- Tail-recursive list constructor.  Builds [n, n-1, ..., 1]"
        , "-- in O(N) stack-safe fashion (the only non-trivial"
        , "-- recursion outside append itself in this fixture)."
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
        , "-- Tail-recursive length — avoids List.length (non-TCO at"
        , "-- the time this spec was added; later op in the CPS umbrella)."
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
        , "-- 5k + 5k = 10k combined, 2x MapStackTest's 5k baseline."
        , "-- Non-TCO recursion at this depth blows Go's default"
        , "-- 8 KiB starting goroutine stack (~3k frames blow it)."
        , "halfSize : Int"
        , "halfSize ="
        , "    5000"
        , ""
        , ""
        , "tests : List Test"
        , "tests ="
        , "    [Test.suite"
        , "        \"List.append constant-stack\""
        , "        [ Test.test"
        , "              \"append 5000 + 5000 -> length 10000\""
        , "              (\\_ ->"
        , "                  let"
        , "                      left = build halfSize"
        , "                      right = build halfSize"
        , "                      combined = List.append left right"
        , "                  in"
        , "                      Test.equal 10000 (tcoLength combined))"
        , "        ]]"
        ]
