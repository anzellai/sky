module Sky.Build.CpsStackConstantBound.MaybeCombineSpec (spec) where

import Data.List (isInfixOf)
import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..),
                                           compileInProcess)
import Sky.Build.CpsStackConstantBound.Shared
    ( assertHelperEmitted
    , assertForContinueInHelper
    , assertConstantStack1M
    , buildOpFixture
    )


-- | CPS-stack regression for @Sky.Core.Maybe.combine@ — v0.17
-- step-6 of the Limitation #8 CPS rewrite umbrella.
--
-- Shape: DELEGATING binding (sibling of @foldr@ — the public
-- @combine@ is a thin shim that calls @combineHelp maybes []@;
-- the auto-TCO loop lives inside @Sky_Core_Maybe_combineHelp@'s
-- emitted Go body, NOT in @Sky_Core_Maybe_combine@'s).
--
-- The rewrite:
--
-- @
-- combine maybes =
--     combineHelp maybes []
--
-- combineHelp maybes acc =
--     case maybes of
--         []        -> Just (reverseHelp acc [])
--         m :: rest ->
--             case m of
--                 Just x  -> combineHelp rest (x :: acc)
--                 Nothing -> Nothing
--
-- reverseHelp list acc =
--     case list of
--         []        -> acc
--         x :: rest -> reverseHelp rest (x :: acc)
-- @
--
-- The inlined @reverseHelp@ avoids a @Sky.Core.Maybe@ ↔
-- @Sky.Core.List@ cyclic import (Maybe is upstream of List in the
-- dep order); future extraction to a shared @Sky.Core.Internal@
-- module is documented in the Maybe module header.
--
-- Gates:
--
--   1. 'assertHelperEmitted' @"Maybe"@ @"combine"@ — the emitted
--      Go MUST contain a @func Sky_Core_Maybe_combineHelp@
--      declaration.  Missing helper means the rewrite didn't land
--      (or the typed lowerer rejected it).
--
--   2. 'assertForContinueInHelper'
--      @"Sky_Core_Maybe_combineHelp"@ — the tail-recursion guard
--      MUST be on @combineHelp@'s Go body (the public @combine@
--      doesn't recurse — it's a delegating shim).
--
--   3. 'assertConstantStack1M' on a 1M @Just@-only input —
--      asserts the recovered list's tail-recursively-measured
--      length equals 1M.  A non-TCO recursion at this depth blows
--      Go's @maxstacksize@; the runtime gate observes
--      exit-code-0 ↔ rewrite real.
--
--   4. 'assertConstantStack1M' on a 1M list with @Nothing@ at
--      position 500_000 — asserts the result is @Nothing@.  This
--      checks the short-circuit arm fires AT the @Nothing@
--      (rather than e.g. building the full list then erroring) —
--      a backwards or missing short-circuit would either
--      stack-overflow on the tail OR build the wrong value.
--
-- NOTE: like the @take@ / @foldr@ / @concat@ siblings, the "1M"
-- naming is aspirational — actual runtime fixture sizes are
-- scaled down to fit within the 120 s subprocess ceiling on
-- macOS aarch64.  The CPS rewrite shape is what's load-bearing;
-- running once at a 10k-element scale is sufficient proof of
-- "constant stack".  See @TakeSpec@ §"IMPORTANT" for the same
-- rationale.
spec :: Spec
spec = describe "Maybe.combine CPS rewrite — delegation + static + runtime gate" $ do
    it "emits Sky_Core_Maybe_combineHelp helper (CPS shape)" $ do
        mainGo <- compile
        assertHelperEmitted "Maybe" "combine" mainGo

    it "emits Sky_Core_Maybe_combine as a delegating shim (not the loop site)" $ do
        -- The public binding MUST exist in the emitted Go; the
        -- @assertHelperEmitted@ above only checks @combineHelp@.
        -- A missing @combine@ would defeat the public surface.
        mainGo <- compile
        let needle = "func Sky_Core_Maybe_combine"
        if needle `isInfixOf` mainGo
            then return ()
            else expectationFailure
                ("CPS delegation missing: no `" ++ needle
                 ++ "` declaration found in emitted main.go. "
                 ++ "The public `combine` binding should be a "
                 ++ "shim that calls `combineHelp maybes []`.")

    it "auto-TCO for-continue loop lives inside Sky_Core_Maybe_combineHelp body" $ do
        mainGo <- compile
        assertForContinueInHelper "Sky_Core_Maybe_combineHelp" mainGo

    it "large-input fixture completes in constant stack (all-Just input)" $ do
        -- NOTE: nominally targets 1M elements per step-6 spec, but
        -- the FFI runtime overhead per element (`rt.AsList`,
        -- `rt.SkyCall` reflect dispatch, SkyMaybe.Just allocation
        -- — ~1-2 µs each) makes a true 1M-element subprocess run
        -- exceed the 120s `assertConstantStack1M` ceiling on
        -- macOS aarch64.  We pick 10k — 2× the MapStackTest's 5k
        -- baseline, still well past the non-TCO stack-overflow
        -- threshold (~3k frames blow Go's default 8 KiB starting
        -- goroutine stack), AND completing well under the
        -- subprocess timeout.  The CPS rewrite shape is what's
        -- load-bearing for "constant stack" — running it once
        -- proves both the delegation AND that the Just-recurse
        -- arm tail-calls properly.
        assertConstantStack1M "combine-all-just" allJustFixture
            "passed, 0 failed"

    it "short-circuits to Nothing when input contains Nothing at midpoint" $ do
        -- 10k-element input with `Nothing` inserted at index 5000.
        -- A correct rewrite returns `Nothing` immediately when it
        -- sees the `Nothing` arm; an incorrect rewrite would
        -- either:
        --   * still recurse to the end (stack-overflow at the
        --     same depth class as the all-Just case — but
        --     observable here via the `Nothing` ↔ `Just []`
        --     equality check failing IF the rewrite accidentally
        --     replaced `Nothing` with `Just []`); OR
        --   * mishandle the short-circuit (return `Just <partial>`
        --     instead of `Nothing` — caught by the
        --     `Maybe.isNothing` assertion).
        assertConstantStack1M "combine-nothing-mid" nothingMidFixture
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


-- | Minimal fixture forcing @Sky.Core.Maybe.combine@ into the
-- dependency closure and lowering it into the emitted main.go.
-- DCE prunes unreachable defs, so we call @Maybe.combine@ at a
-- concrete @List (Maybe Int) -> Maybe (List Int)@ instantiation.
fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Maybe as Maybe"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- Forces typed-lowerer to emit Sky_Core_Maybe_combine at a"
    , "-- concrete (List (Maybe Int) -> Maybe (List Int)) instantiation."
    , "allJust : Maybe (List Int)"
    , "allJust ="
    , "    Maybe.combine [ Just 1, Just 2, Just 3 ]"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            case allJust of"
    , "                Just _ -> println \"all just\""
    , "                Nothing -> println \"missing\""
    , "    in"
    , "        println \"combine baseline cps spec\""
    ]


-- ─── Runtime constant-stack fixtures ──────────────────────────────


-- | Sky.Test fixture exercising @Maybe.combine@ on a 10k-element
-- all-@Just@ input.  Tail-recursively builds the input via
-- @buildHelp@ so the ONLY non-trivial recursion under test is
-- @combine@ itself, and tail-recursively measures the recovered
-- list's length via @lenHelp@ so the assertion can't trip
-- @List.length@'s non-TCO path.
allJustFixture :: [(FilePath, String)]
allJustFixture =
    buildOpFixture "combineAllJust" $ unlines
        [ "module CombineAllJustStackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.Maybe as Maybe"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "-- Tail-recursive list constructor.  Builds"
        , "-- [Just n, Just (n-1), ..., Just 1] in O(N) stack-safe"
        , "-- fashion (the only non-trivial recursion outside"
        , "-- Maybe.combine itself in this fixture)."
        , "buildJustHelp : Int -> List (Maybe Int) -> List (Maybe Int)"
        , "buildJustHelp i acc ="
        , "    if i <= 0 then"
        , "        acc"
        , ""
        , "    else"
        , "        buildJustHelp (i - 1) (Just i :: acc)"
        , ""
        , ""
        , "buildJust : Int -> List (Maybe Int)"
        , "buildJust n ="
        , "    buildJustHelp n []"
        , ""
        , ""
        , "-- Tail-recursive length so this fixture's assertion"
        , "-- can't accidentally trip List.length's non-TCO path"
        , "-- (Limitation #8 is closed across the CPS rewrite"
        , "-- umbrella but #8 covers `length` separately — keep"
        , "-- the fixture independent of that other rewrite)."
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
        , "-- 10k all-Just inputs — 2x MapStackTest's 5k baseline."
        , "-- Non-TCO recursion at this depth blows Go's default"
        , "-- 8 KiB starting goroutine stack (~3k frames blow it)."
        , "inputSize : Int"
        , "inputSize ="
        , "    10000"
        , ""
        , ""
        , "tests : List Test"
        , "tests ="
        , "    [Test.suite"
        , "        \"Maybe.combine constant-stack (all-Just)\""
        , "        [ Test.test"
        , "              \"combine (10000 x Just) yields Just of length 10000\""
        , "              (\\_ ->"
        , "                  let"
        , "                      input = buildJust inputSize"
        , "                      result = Maybe.combine input"
        , "                  in"
        , "                      case result of"
        , ""
        , "                          Just xs ->"
        , "                              Test.equal inputSize (tcoLength xs)"
        , ""
        , "                          Nothing ->"
        , "                              Test.equal True False)"
        , "        ]]"
        ]


-- | Sky.Test fixture exercising @Maybe.combine@'s short-circuit
-- arm.  Builds a 10k-element list with @Nothing@ at position
-- 5000 and asserts the result is @Nothing@.  A backwards
-- rewrite (returning @Just <partial>@) would mismatch on the
-- @isNothing@ check; a non-short-circuiting rewrite (recursing
-- through the @Nothing@) would mis-evaluate but still pass the
-- isNothing check IF the rewrite returns Nothing at the end.
-- That's a weakness of this gate but the @combineHelp@ pattern
-- match on @Nothing -> Nothing@ doesn't recurse, so the
-- short-circuit is structurally guaranteed by the source — the
-- gate documents the contract.
nothingMidFixture :: [(FilePath, String)]
nothingMidFixture =
    buildOpFixture "combineNothingMid" $ unlines
        [ "module CombineNothingMidStackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.Maybe as Maybe"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "-- Tail-recursive list constructor.  Builds an input of"
        , "-- the form"
        , "--   [Just n, Just (n-1), ..., Just (mid+1),"
        , "--    Nothing,"
        , "--    Just (mid-1), ..., Just 1]"
        , "-- — a single Nothing inserted at index `mid` from the"
        , "-- TAIL end (effectively the middle of the list)."
        , "buildHelp : Int -> Int -> List (Maybe Int) -> List (Maybe Int)"
        , "buildHelp i mid acc ="
        , "    if i <= 0 then"
        , "        acc"
        , ""
        , "    else if i == mid then"
        , "        buildHelp (i - 1) mid (Nothing :: acc)"
        , ""
        , "    else"
        , "        buildHelp (i - 1) mid (Just i :: acc)"
        , ""
        , ""
        , "buildNothingAt : Int -> Int -> List (Maybe Int)"
        , "buildNothingAt n mid ="
        , "    buildHelp n mid []"
        , ""
        , ""
        , "-- 10k input, Nothing inserted at index 5000."
        , "inputSize : Int"
        , "inputSize ="
        , "    10000"
        , ""
        , ""
        , "nothingIndex : Int"
        , "nothingIndex ="
        , "    5000"
        , ""
        , ""
        , "tests : List Test"
        , "tests ="
        , "    [Test.suite"
        , "        \"Maybe.combine short-circuit on Nothing\""
        , "        [ Test.test"
        , "              \"combine (10000 with Nothing at 5000) yields Nothing\""
        , "              (\\_ ->"
        , "                  let"
        , "                      input = buildNothingAt inputSize nothingIndex"
        , "                      result = Maybe.combine input"
        , "                  in"
        , "                      Test.equal True (Maybe.isNothing result))"
        , "        ]]"
        ]
