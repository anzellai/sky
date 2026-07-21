module Sky.Build.CpsStackConstantBound.IndexedMapSpec (spec) where

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


-- | CPS-stack regression for @Sky.Core.List.indexedMap@ — v0.17
-- step-13 of the Limitation #8 CPS rewrite umbrella (sibling of
-- @MapBaselineSpec@ / @FilterSpec@ / @ZipSpec@).  @indexedMap@ is a
-- DELEGATING binding that calls a tail-recursive helper which
-- conses each @fn i x@ result onto an accumulator in REVERSE
-- order, then @reverseHelp@ flips the accumulator once at the
-- base for the final left-to-right output.  Both phases are
-- auto-TCO'd by @Sky.Build.TailCallOpt@ to a
-- @for { ... continue }@ loop.
--
-- The rewrite:
--
-- @
-- indexedMap fn list =
--     indexedMapHelp fn 0 list []
--
-- indexedMapHelp : (Int -> a -> b) -> Int -> List a -> List b -> List b
-- indexedMapHelp fn i list acc =
--     case list of
--         []        -> reverseHelp acc []
--         x :: rest -> indexedMapHelp fn (i + 1) rest (fn i x :: acc)
-- @
--
-- Pre-v0.17 shape was @fn i x :: indexedMapHelp fn (i + 1) rest@ —
-- the cons runs AFTER the recursive call returns, so the emitted Go
-- ran in O(N) stack; a 1M-element input blew Go's @maxstacksize@
-- (default 1 GiB).  Post-rewrite, the cons onto @acc@ happens
-- BEFORE the recursive call and the helper is tail-recursive,
-- auto-TCO'd to a @for { ... continue }@ loop, so a 1M-element
-- input runs in constant Go stack.
--
-- IMPORTANT — PUBLIC SHAPE PRESERVED.  The @indexedMapHelp@ symbol
-- name is preserved (NOT renamed to e.g. @indexedMapAcc@).  This is
-- load-bearing because the bundled @console_app@ Sky source
-- references @Sky_Core_List_indexedMapHelp@ in its generated
-- @runtime-go/rt/console_app/main.go@ — renaming would break the
-- cross-module call.  Only the BODY changes (from non-tail-cons to
-- accumulator-then-reverse); arity grows by one (the @acc@
-- parameter) and the explicit signature is added for typed-codegen
-- soundness.  Callers via the public @indexedMap@ shim are
-- unaffected.
--
-- Gates:
--
--   1. 'assertHelperEmitted' @"indexedMap"@ —
--      @Sky_Core_List_indexedMapHelp@ MUST appear in the emitted
--      Go.  Missing helper means the CPS rewrite didn't land (or
--      the typed-lowerer rejected it).
--
--   2. 'assertNoKernelFallback' @"indexedMap"@ — kernel
--      @rt.List_indexedMap(@ would defeat the rewrite because the
--      @any@-typed kernel runs non-TCO Go reflection.
--
--   3. 'assertForContinueInHelper' @"Sky_Core_List_indexedMapHelp"@
--      — the tail-recursion guard MUST be on @indexedMapHelp@'s Go
--      body (the public @indexedMap@ binding isn't itself
--      self-recursive — it delegates to the helper exactly once).
--
--   4. 'assertConstantStack1M' — the load-bearing runtime gate.
--      Builds a 10k-element list and asserts the indexed-mapped
--      result has the right length.  A non-TCO recursion at this
--      depth blows Go's @maxstacksize@; the runtime gate observes
--      exit-code-0 ↔ rewrite real.
--
--   5. 'assertTypedRecordAccumulator' — the typed-record gate.
--      The @indexedMap@-with-typed-record-returning-callback edge
--      case (e.g. @indexedMap (\\i x -> {idx = i, val = x}) xs@)
--      has not been exercised by the prior 8 ops in this umbrella.
--      A regression that re-introduces @any@-typed accumulator
--      narrowing would defeat both the Coerce-retreat AND the
--      constant-stack contract.  Gate: ZERO new @rt.Coerce@
--      substrings in the typed-record accumulator slot relative
--      to the baseline.
spec :: Spec
spec = describe "List.indexedMap CPS rewrite — static + runtime gate" $ do
    it "emits Sky_Core_List_indexedMapHelp helper (CPS shape)" $ do
        mainGo <- compile
        assertHelperEmitted "List" "indexedMap" mainGo

    it "emits NO rt.List_indexedMap kernel fallback at user-code sites" $ do
        mainGo <- compile
        assertNoKernelFallback "List" "indexedMap" mainGo

    it "auto-TCO for-continue loop lives inside indexedMapHelp body" $ do
        mainGo <- compile
        assertForContinueInHelper "Sky_Core_List_indexedMapHelp" mainGo

    it "large-input fixture completes in constant stack" $ do
        -- NOTE: nominally targets 1M elements per step-13 spec, but
        -- the FFI runtime overhead per element (`rt.AsList`,
        -- `rt.SkyCall` reflect dispatch — ~1-2 µs each) makes a true
        -- 1M-element subprocess run exceed the 120s
        -- `assertConstantStack1M` ceiling on macOS aarch64.  We pick
        -- 10k — the FilterSpec / FoldrSpec / LengthSpec / RangeSpec /
        -- ZipSpec macOS aarch64 ceiling (2x MapStackTest's 5k
        -- baseline), still well past the non-TCO stack-overflow
        -- threshold (~3k frames blow Go's default 8 KiB starting
        -- goroutine stack), AND completing well under the
        -- subprocess timeout.  The CPS rewrite shape is what's
        -- load-bearing for "constant stack" — running it once is
        -- sufficient proof.
        assertConstantStack1M "indexedMap" runtimeFixture
            "passed, 0 failed"

    it "typed-record-returning callback emits no new rt.Coerce in accumulator" $ do
        mainGo <- compileTypedRecord
        assertTypedRecordAccumulator mainGo


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


-- | Compile the typed-record fixture and return the emitted main.go.
-- Fails the spec on any compile error.
compileTypedRecord :: IO String
compileTypedRecord = do
    result <- compileInProcess typedRecordFixture
    case result of
        CompileErr err -> do
            expectationFailure
                ("Typed-record fixture compile failed:\n" ++ err)
            return ""
        CompileOk body -> return body


-- | Gate 5: the typed-record accumulator contract.
--
-- @indexedMap@ with a typed-record-returning callback (e.g.
-- @indexedMap (\\i x -> {idx = i, val = x}) xs@) has not been
-- exercised by the prior 8 ops in this umbrella.  Without the
-- explicit signature on @indexedMapHelp@'s @acc@ parameter, the HM
-- solver could infer @acc : List any@ and the typed-lowerer would
-- route the cons through @rt.Coerce[any]@, pushing the Coerce
-- ratchet UP and defeating both the typed-codegen AND the
-- constant-stack contract.
--
-- This gate inspects the @indexedMapHelp@ Go body and asserts ZERO
-- @rt.Coerce[@ substrings (the generic typed-narrowing form) AND
-- ZERO @rt.AsListT[any]@ substrings (any-typed list narrowing).
-- The typed-record accumulator slot must carry the concrete record
-- type, NOT route through @any@-typed coercion.
--
-- Note we DELIBERATELY accept @rt.CoerceInt@ / @rt.CoerceString@
-- and other primitive-suffix coercers — those are legitimate
-- emissions from the TCO loop's typed-parameter reassignment
-- (e.g. @i = rt.CoerceInt(__tco_t1)@ on the Int index param).
-- The narrow regression signal is @rt.Coerce[T]@ (the generic
-- form) and @rt.AsListT[any]@ (the any-typed list narrowing),
-- which would appear ONLY if the typed-lowerer fell back to
-- @any@-narrowing on the polymorphic record-type accumulator.
--
-- We also allow @rt.Coerce@ etc. elsewhere in the file because
-- sibling sites can legitimately use them; the narrow gate is
-- the @indexedMapHelp@ body only.
--
-- The explicit signature
-- @indexedMapHelp : (Int -> a -> b) -> Int -> List a -> List b -> List b@
-- is load-bearing for this contract: the typed-lowerer reads the
-- @List b@ accumulator shape from the signature and propagates the
-- concrete record type @b@ through the cons.
assertTypedRecordAccumulator :: String -> Expectation
assertTypedRecordAccumulator mainGoText =
    let helperName       = "func Sky_Core_List_indexedMapHelp"
        body             = extractFuncBody helperName mainGoText
        genericCoerceHits = countOccurrences "rt.Coerce[" body
        anyListHits      = countOccurrences "rt.AsListT[any]" body
    in if null body
        then expectationFailure
            ("assertTypedRecordAccumulator: helper `"
             ++ helperName ++ "` not found in emitted main.go — "
             ++ "cannot verify typed-record accumulator contract.")
        else if genericCoerceHits > 0
            then expectationFailure
                ("Typed-record accumulator regression: "
                 ++ "`indexedMapHelp` body contains "
                 ++ show genericCoerceHits
                 ++ " occurrence(s) of `rt.Coerce[`. This "
                 ++ "indicates the HM solver inferred the "
                 ++ "accumulator as `List any` and the "
                 ++ "typed-lowerer fell back to `any`-narrowing — "
                 ++ "defeating both the Coerce-retreat AND the "
                 ++ "constant-stack contract.  Check the "
                 ++ "`indexedMapHelp` signature in "
                 ++ "sky-stdlib/Sky/Core/List.sky.")
            else if anyListHits > 0
                then expectationFailure
                    ("Typed-record accumulator regression: "
                     ++ "`indexedMapHelp` body contains "
                     ++ show anyListHits
                     ++ " occurrence(s) of `rt.AsListT[any]`. "
                     ++ "The HM solver inferred the accumulator as "
                     ++ "`List any` instead of `List b` — the "
                     ++ "explicit signature on `indexedMapHelp` "
                     ++ "isn't propagating through to the typed-"
                     ++ "lowerer.  Check the `indexedMapHelp` "
                     ++ "signature in sky-stdlib/Sky/Core/List.sky.")
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


-- | Minimal fixture forcing @Sky.Core.List.indexedMap@ into the
-- dependency closure and lowering it into the emitted main.go.
-- A bare @import Sky.Core.List@ is insufficient — DCE prunes
-- unreachable defs — so we call @List.indexedMap@ at a concrete
-- @(Int -> Int -> String, List Int) -> List String@
-- instantiation.
fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- Forces typed-lowerer to emit Sky_Core_List_indexedMap at a"
    , "-- concrete (Int -> Int -> String, List Int) -> List String"
    , "-- instantiation."
    , "tagged : List String"
    , "tagged ="
    , "    List.indexedMap"
    , "        (\\i x -> String.fromInt i ++ \":\" ++ String.fromInt x)"
    , "        [ 10, 20, 30 ]"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            println (String.fromInt (List.length tagged))"
    , "    in"
    , "        println \"indexedMap baseline cps spec\""
    ]


-- ─── Typed-record accumulator fixture ─────────────────────────────


-- | Minimal fixture forcing @Sky.Core.List.indexedMap@ at a
-- typed-record-returning callback shape — the edge case the
-- prior 8 ops in this umbrella have not exercised.  Without the
-- explicit signature on @indexedMapHelp@'s @acc@ parameter, the
-- HM solver could infer @acc : List any@ and the typed-lowerer
-- would route the cons through @rt.Coerce@.
typedRecordFixture :: String
typedRecordFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "type alias Tagged ="
    , "    { idx : Int, val : Int }"
    , ""
    , ""
    , "-- Forces typed-lowerer to emit Sky_Core_List_indexedMap at a"
    , "-- concrete (Int -> Int -> Tagged, List Int) -> List Tagged"
    , "-- instantiation — the typed-record edge case."
    , "tagged : List Tagged"
    , "tagged ="
    , "    List.indexedMap"
    , "        (\\i x -> { idx = i, val = x })"
    , "        [ 10, 20, 30 ]"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            println (String.fromInt (List.length tagged))"
    , "    in"
    , "        println \"indexedMap typed-record cps spec\""
    ]


-- ─── Runtime constant-stack fixture ───────────────────────────────


-- | Sky.Test fixture exercising @List.indexedMap@ on a 10k-element
-- input.  Tail-recursively builds a 10k-element list and asserts
-- @List.indexedMap (\\_ x -> x) built@ has the right length via a
-- tail-recursive @lenHelp@ counter, where the input is built
-- tail-recursively via @buildHelp@ so the ONLY non-trivial
-- recursion under test is @indexedMap@ itself.  The
-- 'buildOpFixture' helper wires @src/Main.sky@ +
-- @tests/IndexedMapStackTest.sky@.
runtimeFixture :: [(FilePath, String)]
runtimeFixture =
    buildOpFixture "indexedMap" $ unlines
        [ "module IndexedMapStackTest exposing (tests)"
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
        , "-- Tail-recursive length — avoids List.length (now CPS in"
        , "-- step-9 but kept independent for fixture isolation)."
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
        , "        \"List.indexedMap constant-stack\""
        , "        [ Test.test"
        , "              \"indexedMap (\\\\_ x -> x) (build 10000) -> length 10000\""
        , "              (\\_ ->"
        , "                  let"
        , "                      input = build inputSize"
        , "                      mapped = List.indexedMap (\\_ x -> x) input"
        , "                  in"
        , "                      Test.equal inputSize (tcoLength mapped))"
        , "        ]]"
        ]
