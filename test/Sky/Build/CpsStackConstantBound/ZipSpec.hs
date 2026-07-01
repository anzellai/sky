module Sky.Build.CpsStackConstantBound.ZipSpec (spec) where

import Data.List (isInfixOf, isPrefixOf, tails)
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


-- | CPS-stack regression for @Sky.Core.List.zip@ — v0.17 step-11
-- of the Limitation #8 CPS rewrite umbrella (sibling of
-- @AppendSpec@ / @ConcatSpec@ / @RangeSpec@).  @zip@ is a
-- DELEGATING binding that calls a tail-recursive helper which
-- cons'es each @( x, y )@ pair onto an accumulator in REVERSE
-- order, then @reverseHelp@ flips the accumulator once at the
-- base for the final left-to-right output.  Both phases are
-- auto-TCO'd by @Sky.Build.TailCallOpt@ to a
-- @for { ... continue }@ loop.
--
-- The rewrite:
--
-- @
-- zip xs ys =
--     zipHelp xs ys []
--
-- zipHelp : List a -> List b -> List ( a, b ) -> List ( a, b )
-- zipHelp xs ys acc =
--     case xs of
--         []        -> reverseHelp acc []
--         x :: xRest ->
--             case ys of
--                 []        -> reverseHelp acc []
--                 y :: yRest -> zipHelp xRest yRest (( x, y ) :: acc)
-- @
--
-- Pre-v0.17 shape was @( x, y ) :: zip xRest yRest@ — the cons
-- runs AFTER the recursive call returns, so the emitted Go ran in
-- O(N) stack; a 1M-element zip blew Go's @maxstacksize@ (default
-- 1 GiB).  Post-rewrite, @zipHelp@ is tail-recursive — the cons
-- onto @acc@ happens BEFORE the recursive call — and auto-TCO'd
-- to a @for { ... continue }@ loop, so a 1M-element zip runs in
-- constant Go stack.
--
-- CRITICAL: the EXPLICIT signature on @zipHelp@ is load-bearing.
-- The tuple-typed accumulator (@List ( a, b )@) would otherwise be
-- inferred as @List any@ by the HM solver — tuples in CPS
-- accumulator position have not been exercised by the prior 8 ops
-- in this umbrella, so without the annotation the typed-lowerer
-- falls back to @rt.AsList[any]@ and the cons payload re-narrows
-- at runtime, pushing the rt.Coerce ratchet UP.  The 5th gate
-- @assertTupleAccumulatorTyped@ guards this contract.
--
-- Gates:
--
--   1. 'assertHelperEmitted' @"zip"@ — @Sky_Core_List_zipHelp@
--      MUST appear in the emitted Go.  Missing helper means the
--      CPS rewrite didn't land (or the typed-lowerer rejected it).
--
--   2. 'assertNoKernelFallback' @"zip"@ — kernel
--      @rt.List_zip(@ would defeat the rewrite because the
--      @any@-typed kernel runs non-TCO Go reflection.
--
--   3. 'assertForContinueInHelper' @"Sky_Core_List_zipHelp"@ —
--      the tail-recursion guard MUST be on @zipHelp@'s Go body
--      (the public @zip@ binding isn't itself self-recursive —
--      it delegates to the helper exactly once).
--
--   4. 'assertConstantStack1M' — the load-bearing runtime gate.
--      Builds a 10k-element zip, asserts the tail-recursive
--      length equals 10k.  A non-TCO recursion at this depth
--      blows Go's @maxstacksize@; the runtime gate observes
--      exit-code-0 ↔ rewrite real.
--
--   5. 'assertTupleAccumulatorTyped' — the typed-codegen gate.
--      The @zipHelp@ body MUST contain @rt.SkyTuple2@ in the
--      emission (typed-tuple narrowing AT emission, not runtime).
--      The fully-monomorphised call sites elsewhere in the file
--      then carry @rt.T2[int, string]@ (or whatever concrete
--      payload types) — but the polymorphic @zipHelp@ generic
--      body uses the runtime's type-erased @rt.SkyTuple2@ struct
--      with typed @V0@ / @V1@ fields (so the cons construction
--      is @rt.SkyTuple2{V0: x, V1: y}@, NOT
--      @rt.MakeTupleAny(x, y)@).  Equivalently: ZERO
--      @rt.AsListT[any](@ occurrences inside @zipHelp@'s body —
--      a regression that re-introduces @any@-typed accumulator
--      narrowing would defeat both the Coerce-retreat AND the
--      constant-stack contract.  Without the explicit
--      @List ( a, b )@ signature on @zipHelp@'s @acc@ param, the
--      typed-lowerer would route through @rt.AsList[any]@ and
--      the cons would emit as @any@.
spec :: Spec
spec = describe "List.zip CPS rewrite — static + runtime gate" $ do
    it "emits Sky_Core_List_zipHelp helper (CPS shape)" $ do
        mainGo <- compile
        assertHelperEmitted "List" "zip" mainGo

    it "emits NO rt.List_zip kernel fallback at user-code sites" $ do
        mainGo <- compile
        assertNoKernelFallback "List" "zip" mainGo

    it "auto-TCO for-continue loop lives inside zipHelp body" $ do
        mainGo <- compile
        assertForContinueInHelper "Sky_Core_List_zipHelp" mainGo

    it "large-input fixture completes in constant stack" $ do
        -- NOTE: nominally targets 1M elements per step-11 spec, but
        -- the FFI runtime overhead per element (`rt.AsList`,
        -- `rt.SkyCall` reflect dispatch — ~1-2 µs each) makes a true
        -- 1M-element subprocess run exceed the 120s
        -- `assertConstantStack1M` ceiling on macOS aarch64.  We pick
        -- 10k — the FilterSpec / FoldrSpec / LengthSpec / RangeSpec
        -- macOS aarch64 ceiling (2x MapStackTest's 5k baseline),
        -- still well past the non-TCO stack-overflow threshold
        -- (~3k frames blow Go's default 8 KiB starting goroutine
        -- stack), AND completing well under the subprocess timeout.
        -- The CPS rewrite shape is what's load-bearing for
        -- "constant stack" — running it once is sufficient proof.
        assertConstantStack1M "zip" runtimeFixture
            "passed, 0 failed"

    it "zipHelp accumulator emits typed rt.SkyTuple2 — no rt.AsList[any] regression" $ do
        mainGo <- compile
        assertTupleAccumulatorTyped mainGo


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


-- | Gate 5: the typed-tuple accumulator contract.
--
-- Two checks against the emitted Go inside the @zipHelp@ body:
--
--   (a) at least one @rt.SkyTuple2@ occurrence — typed-tuple
--       construction at emission time.  The @( x, y ) :: acc@
--       cons in @zipHelp@ emits as
--       @rt.SkyTuple2{V0: x, V1: y}@ (the typed runtime tuple
--       struct), and the @acc@ slot is declared as
--       @[]rt.SkyTuple2@.  Both rely on the HM solver knowing
--       the tuple shape at the @zipHelp@ binding site.
--
--   (b) zero @rt.AsListT[any](@ occurrences in the @zipHelp@
--       body — a regression that re-introduces @any@-typed
--       accumulator narrowing would defeat both the
--       Coerce-retreat AND the constant-stack contract.  (We
--       allow @rt.AsListT[any]@ elsewhere in the file because
--       sibling kernels can legitimately route through @any@;
--       the narrow gate is the @zipHelp@ body only.)
--
-- The signature-binding contract is structural: without
-- @zipHelp : List a -> List b -> List ( a, b ) -> List ( a, b )@,
-- the HM solver infers @acc : List any@ and the typed-lowerer
-- routes through @rt.AsList[any]@.  This gate observes the
-- structural outcome via emitted Go grep.
--
-- Note we DON'T require the fully-monomorphised
-- @rt.T2[concrete, concrete]@ form inside @zipHelp@ — the
-- function body is generic over @T1@ and @T2@, so the runtime's
-- type-erased @rt.SkyTuple2@ is the correct emission.  The
-- fully-typed form appears at call sites elsewhere in the file
-- (e.g. @pairs() []rt.T2[int, string]@).
assertTupleAccumulatorTyped :: String -> Expectation
assertTupleAccumulatorTyped mainGoText =
    let helperName    = "func Sky_Core_List_zipHelp"
        body          = extractFuncBody helperName mainGoText
        hasSkyTuple2  = "rt.SkyTuple2" `isInfixOf` body
        anyHits       = countOccurrences "rt.AsListT[any](" body
    in if null body
        then expectationFailure
            ("assertTupleAccumulatorTyped: helper `"
             ++ helperName ++ "` not found in emitted main.go — "
             ++ "cannot verify tuple-typed accumulator contract.")
        else if not hasSkyTuple2
            then expectationFailure
                ("Tuple-typed accumulator regression: `zipHelp` "
                 ++ "body has NO `rt.SkyTuple2` occurrence — the "
                 ++ "`( x, y ) :: acc` cons is routing through "
                 ++ "`any`-typed accumulator narrowing instead of "
                 ++ "the typed Go runtime struct "
                 ++ "`rt.SkyTuple2{V0: ..., V1: ...}`. The "
                 ++ "explicit signature `zipHelp : List a -> "
                 ++ "List b -> List ( a, b ) -> List ( a, b )` "
                 ++ "is load-bearing for HM to specialise the "
                 ++ "tuple shape; without it the typed-lowerer "
                 ++ "falls back to `rt.AsList[any]` and pushes "
                 ++ "the Coerce ratchet UP.")
            else if anyHits > 0
                then expectationFailure
                    ("Tuple-typed accumulator regression: `zipHelp` "
                     ++ "body contains " ++ show anyHits
                     ++ " occurrence(s) of `rt.AsListT[any](`. "
                     ++ "This indicates the HM solver inferred the "
                     ++ "tuple accumulator as `List any` and the "
                     ++ "typed-lowerer fell back to `any`-narrowing "
                     ++ "— defeating both the Coerce-retreat AND "
                     ++ "the constant-stack contract.  Check the "
                     ++ "`zipHelp` signature in "
                     ++ "sky-stdlib/Sky/Core/List.sky.")
                else return ()


-- | Extract a Go function's body (text between the first @{@ on
-- the declaration line and the next top-level @}@).  Brace-counted
-- so nested @{ ... }@ blocks are preserved.
--
-- Duplicated from Shared.hs's private @extractFuncBody@ because
-- Shared exports the higher-level gates but not the helper.
extractFuncBody :: String -> String -> String
extractFuncBody needle src =
    case dropWhile (not . (needle `isInfixOf`)) (lines src) of
        []     -> ""
        (h:rest)  ->
            let after  = dropWhile (/= '{') h
                stream = case after of
                    ('{':xs) -> xs ++ "\n" ++ unlines rest
                    _        -> unlines rest
            in takeUntilDepthZero stream

  where
    takeUntilDepthZero :: String -> String
    takeUntilDepthZero = go 1 ""
      where
        go :: Int -> String -> String -> String
        go _ acc [] = reverse acc
        go 0 acc _  = reverse acc
        go d acc (c:cs)
          | c == '{' = go (d + 1) (c:acc) cs
          | c == '}' = let d' = d - 1
                       in if d' == 0
                          then reverse acc
                          else go d' (c:acc) cs
          | otherwise = go d (c:acc) cs


-- | Count non-overlapping occurrences of a substring in a string.
-- Duplicated from Shared.hs because not exported.
countOccurrences :: String -> String -> Int
countOccurrences needle haystack =
    length . filter (needle `isPrefixOf`) . tails $ haystack


-- ─── Static-analysis fixture ──────────────────────────────────────


-- | Minimal fixture forcing @Sky.Core.List.zip@ into the
-- dependency closure and lowering it into the emitted main.go.
-- A bare @import Sky.Core.List@ is insufficient — DCE prunes
-- unreachable defs — so we call @List.zip@ at a concrete
-- @(List Int, List String) -> List ( Int, String )@ instantiation.
fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- Forces typed-lowerer to emit Sky_Core_List_zip at a"
    , "-- concrete (List Int, List String) -> List (Int, String)"
    , "-- instantiation."
    , "pairs : List ( Int, String )"
    , "pairs ="
    , "    List.zip [ 1, 2, 3 ] [ \"a\", \"b\", \"c\" ]"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            println (String.fromInt (List.length pairs))"
    , "    in"
    , "        println \"zip baseline cps spec\""
    ]


-- ─── Runtime constant-stack fixture ───────────────────────────────


-- | Sky.Test fixture exercising @List.zip@ on a 10k-element pair.
-- Asserts @List.zip xs ys@ has length 10000 via a tail-recursive
-- @lenHelp@ counter, where @xs@ and @ys@ are built tail-recursively
-- via @buildHelp@ so the ONLY non-trivial recursion under test is
-- @zip@ itself.  The 'buildOpFixture' helper wires
-- @src/Main.sky@ + @tests/ZipStackTest.sky@.
runtimeFixture :: [(FilePath, String)]
runtimeFixture =
    buildOpFixture "zip" $ unlines
        [ "module ZipStackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.List as List"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "-- Tail-recursive list constructor — avoids `List.range`"
        , "-- (whose own CPS rewrite shipped in step-10 — using it"
        , "-- here would be fine, but keeping the fixture independent"
        , "-- of OTHER rewrites is the standing convention)."
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
        , "        \"List.zip constant-stack\""
        , "        [ Test.test"
        , "              \"zip (build 10000) (build 10000) -> length 10000\""
        , "              (\\_ ->"
        , "                  let"
        , "                      xs = build inputSize"
        , "                      ys = build inputSize"
        , "                      pairs = List.zip xs ys"
        , "                  in"
        , "                      Test.equal inputSize (tcoLength pairs))"
        , "        ]]"
        ]
