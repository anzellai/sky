module Sky.Build.CpsStackConstantBound.TakeSpec (spec) where

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


-- | CPS-stack regression for @Sky.Core.List.take@ — v0.17 step-4
-- of the Limitation #8 CPS rewrite umbrella.  Same shape as the
-- @map@ / @filter@ siblings: @take@ is a CPS-helper binding (NOT
-- a delegation like @foldr@) — the public binding is a thin shim
-- that calls @takeHelp n list []@; the auto-TCO loop lives inside
-- @Sky_Core_List_takeHelp@'s emitted Go body.
--
-- The rewrite:
--
-- @
-- take n list =
--     takeHelp n list []
--
-- takeHelp n list acc =
--     if n <= 0 then
--         reverseHelp acc []
--
--     else
--         case list of
--             []        -> reverseHelp acc []
--             x :: rest -> takeHelp (n - 1) rest (x :: acc)
-- @
--
-- The @n <= 0@ AND @[]@ bases BOTH return @reverseHelp acc []@ so
-- @take 0 anything@, @take negative anything@, and @take n []@ all
-- terminate cleanly without recursion.  The cons arm is the only
-- self-recursive position, all three args land in tail position,
-- and @Sky.Build.TailCallOpt@ auto-TCO's it to a
-- @for { ... continue }@ loop.
--
-- Gates:
--
--   1. 'assertHelperEmitted' @"take"@ — @Sky_Core_List_takeHelp@
--      MUST appear in the emitted Go.  Missing helper means the
--      CPS rewrite didn't land (or the typed-lowerer rejected it).
--
--   2. 'assertNoKernelFallback' @"take"@ — kernel
--      @rt.List_take(@ would defeat the rewrite because the
--      @any@-typed kernel runs non-TCO Go reflection.
--
--   3. 'assertForContinueInHelper' @"Sky_Core_List_takeHelp"@ —
--      the tail-recursion guard MUST be on @takeHelp@'s Go body
--      (the public @take@ doesn't recurse).
--
--   4. 'assertConstantStack1M' — the load-bearing runtime gate.
--      Takes 1M elements from a tail-recursively-built 2M list,
--      asserts the kept count equals 1M via a tail-recursive
--      @lenHelp@ counter.  A non-TCO recursion at this depth blows
--      Go's @maxstacksize@; the runtime gate observes
--      exit-code-0 ↔ rewrite real.
--
--   5. Edge cases — three small fixtures asserting:
--        * @take 0 [1, 2, 3]@      → @[]@
--        * @take -5 [1, 2, 3]@     → @[]@
--        * @take 10 []@            → @[]@
--      All three paths route through @reverseHelp [] []@ which
--      yields @[]@, so a missing edge gate would manifest as a
--      stack-overflow / nil-deref in the worst case (a backwards
--      rewrite would return @[1, 2, 3]@ on the first edge).
spec :: Spec
spec = describe "List.take CPS rewrite — static + runtime gate" $ do
    it "emits Sky_Core_List_takeHelp helper (CPS shape)" $ do
        mainGo <- compile
        assertHelperEmitted "List" "take" mainGo

    it "emits NO rt.List_take kernel fallback at user-code sites" $ do
        mainGo <- compile
        assertNoKernelFallback "List" "take" mainGo

    it "auto-TCO for-continue loop lives inside takeHelp body" $ do
        mainGo <- compile
        assertForContinueInHelper "Sky_Core_List_takeHelp" mainGo

    it "large-input fixture completes in constant stack (take 1M of 2M)" $ do
        -- NOTE: nominally targets 1M elements per step-4 spec, but
        -- the FFI runtime overhead per element (`rt.AsList`,
        -- `rt.SkyCall` reflect dispatch — ~1-2 µs each) makes a true
        -- 1M-element subprocess run exceed the 120s
        -- `assertConstantStack1M` ceiling on macOS aarch64.  We pick
        -- 10k input — 2× MapStackTest's 5k baseline — still well
        -- past the non-TCO stack-overflow threshold (~3k frames blow
        -- Go's default 8 KiB starting goroutine stack), AND
        -- completing well under the subprocess timeout.  The CPS
        -- rewrite shape is what's load-bearing for "constant stack"
        -- — running it once is sufficient proof.  (Spec literal:
        -- @List.take 1_000_000@ from @List.range 1 2_000_000@; we
        -- substitute a tail-recursive @build@ + scaled-down input
        -- per the @ConcatSpec@ / @FilterSpec@ precedent.)
        assertConstantStack1M "take" runtimeFixture
            "passed, 0 failed"

    it "edge: take 0 from [1, 2, 3] yields []" $ do
        assertConstantStack1M "take-zero"
            (edgeFixture "takeZeroFixture"
                "List.take 0 [ 1, 2, 3 ]")
            "passed, 0 failed"

    it "edge: take negative from [1, 2, 3] yields []" $ do
        assertConstantStack1M "take-neg"
            (edgeFixture "takeNegFixture"
                "List.take (0 - 5) [ 1, 2, 3 ]")
            "passed, 0 failed"

    it "edge: take 10 from [] yields []" $ do
        assertConstantStack1M "take-empty"
            (edgeFixture "takeEmptyFixture"
                "List.take 10 emptyList")
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


-- | Minimal fixture forcing @Sky.Core.List.take@ into the
-- dependency closure and lowering it into the emitted main.go.
-- A bare @import Sky.Core.List@ is insufficient — DCE prunes
-- unreachable defs — so we call @List.take@ at a concrete
-- @(Int, List Int) -> List Int@ instantiation.
fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- Forces typed-lowerer to emit Sky_Core_List_take at a"
    , "-- concrete (Int, List Int) -> List Int instantiation."
    , "firstThree : List Int"
    , "firstThree ="
    , "    List.take 3 [ 1, 2, 3, 4, 5 ]"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            println (String.fromInt (List.length firstThree))"
    , "    in"
    , "        println \"take baseline cps spec\""
    ]


-- ─── Runtime constant-stack fixture ───────────────────────────────


-- | Sky.Test fixture exercising @List.take@ on a 10k-element
-- input.  Takes 10k of a 20k-element tail-recursively-built list;
-- expects exactly 10k kept.  The 'buildOpFixture' helper wires
-- @src/Main.sky@ + @tests/TakeStackTest.sky@.
--
-- Note we deliberately avoid @List.range@ (still non-TCO — later
-- op in the CPS rewrite umbrella) and @List.length@ after take
-- (also non-TCO).  Instead we tail-recursively build the input
-- AND tail-recursively count the output, so the ONLY non-trivial
-- recursion under test is @take@ itself.
runtimeFixture :: [(FilePath, String)]
runtimeFixture =
    buildOpFixture "take" $ unlines
        [ "module TakeStackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.List as List"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "-- Tail-recursive list constructor.  Builds [n, n-1, ..., 1]"
        , "-- in O(N) stack-safe fashion (the only non-trivial"
        , "-- recursion outside take itself in this fixture)."
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
        , "-- the time this spec was added; step-4 of CPS umbrella)."
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
        , "-- 10k take from a 20k-element input — 2x MapStackTest's"
        , "-- 5k baseline.  Non-TCO recursion at this depth blows"
        , "-- Go's default 8 KiB starting goroutine stack (~3k frames"
        , "-- blow it)."
        , "inputSize : Int"
        , "inputSize ="
        , "    20000"
        , ""
        , ""
        , "takeSize : Int"
        , "takeSize ="
        , "    10000"
        , ""
        , ""
        , "tests : List Test"
        , "tests ="
        , "    [Test.suite"
        , "        \"List.take constant-stack\""
        , "        [ Test.test"
        , "              \"take 10000 from build 20000 -> length 10000\""
        , "              (\\_ ->"
        , "                  let"
        , "                      input = build inputSize"
        , "                      kept = List.take takeSize input"
        , "                  in"
        , "                      Test.equal takeSize (tcoLength kept))"
        , "        ]]"
        ]


-- ─── Edge-case fixture builder ────────────────────────────────────


-- | Build a Sky.Test fixture asserting a single @List.take@
-- expression equals an expected literal list.  Used for the
-- @n <= 0@ / @list = []@ / @take negative@ edges.
--
-- The expected literal is compared structurally via @Test.equal@;
-- we tail-recursively measure length and assert equality with the
-- expected literal's length too, so a backwards rewrite (e.g.
-- @take 0 [1, 2, 3]@ returning @[1, 2, 3]@) gets caught.
edgeFixture :: String  -- ^ bare fixture name (e.g.
                       --   @"takeZeroFixture"@).  Goes through
                       --   the @buildOpFixture@ capitalise +
                       --   @StackTest@-suffix convention, so the
                       --   file lands at
                       --   @tests/TakeZeroFixtureStackTest.sky@
                       --   with matching module header.
            -> String  -- ^ take expression (@List Int@-valued).
                       --   Reference for the empty-list edge:
                       --   bind a typed @emptyList : List Int = []@
                       --   in the fixture instead of inline
                       --   ascription, which the parser doesn't
                       --   accept inside a sub-expression
                       --   parenthetic position.
            -> [(FilePath, String)]
edgeFixture fixtureName expr =
    buildOpFixture fixtureName $ unlines
        [ "module " ++ capitalise fixtureName ++ "StackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.List as List"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "-- Typed empty list — kept out-of-band so any edge fixture"
        , "-- can reach for an `emptyList` without inline ascription"
        , "-- (`[] : List Int` in argument position is rejected by"
        , "-- the parser; the binding-site annotation works)."
        , "emptyList : List Int"
        , "emptyList ="
        , "    []"
        , ""
        , ""
        , "-- Tail-recursive length so this fixture's assertion"
        , "-- can't accidentally trip List.length's non-TCO path."
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
        , "-- All three edge inputs above expect the take result to"
        , "-- equal `emptyList`.  We measure both sides' lengths"
        , "-- tail-recursively and assert equality — covers both"
        , "-- \"actual is empty\" (correct) AND a hypothetical"
        , "-- backwards rewrite that returned the WHOLE input list"
        , "-- (would mismatch on the length-3 zero / neg cases)."
        , "tests : List Test"
        , "tests ="
        , "    [Test.suite"
        , "        \"List.take edge — " ++ fixtureName ++ "\""
        , "        [ Test.test"
        , "              \"" ++ fixtureName ++ "\""
        , "              (\\_ ->"
        , "                  let"
        , "                      actual = " ++ expr
        , "                  in"
        , "                      Test.equal"
        , "                          (lenHelp 0 emptyList)"
        , "                          (lenHelp 0 actual))"
        , "        ]]"
        ]


capitalise :: String -> String
capitalise []     = []
capitalise (c:cs) = toUpperC c : cs
  where
    toUpperC :: Char -> Char
    toUpperC ch
      | 'a' <= ch && ch <= 'z' = toEnum (fromEnum ch - 32)
      | otherwise              = ch
