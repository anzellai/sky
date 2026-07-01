module Sky.Build.CpsStackConstantBound.ResultCombineSpec (spec) where

import Data.List (isInfixOf)
import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..),
                                           compileInProcess)
import Sky.Build.CpsStackConstantBound.Shared
    ( assertForContinueInHelper
    , assertConstantStack1M
    , buildOpFixture
    )


-- | CPS-stack regression for @Sky.Core.Result.combine@ — v0.17
-- step-7 of the Limitation #8 CPS rewrite umbrella.  Sibling of
-- the @List.foldr@ delegation shape: @combine@ is a DELEGATING
-- binding, NOT a CPS-helper binding.  The public @combine@ is a
-- thin shim that calls @combineHelp results []@; the auto-TCO
-- @for { ... continue }@ loop lives inside
-- @Sky_Core_Result_combineHelp@'s emitted Go body — NOT in the
-- public @Sky_Core_Result_combine@'s.
--
-- The rewrite:
--
-- @
-- combine results =
--     combineHelp results []
--
-- combineHelp results acc =
--     case results of
--         [] -> Ok (reverseHelp acc [])
--         (r :: rest) ->
--             case r of
--                 Ok x -> combineHelp rest (x :: acc)
--                 Err e -> Err e
--
-- reverseHelp list acc =
--     case list of
--         [] -> acc
--         (x :: rest) -> reverseHelp rest (x :: acc)
-- @
--
-- The recursive arm @combineHelp rest (x :: acc)@ is in tail
-- position; @Sky.Build.TailCallOpt@ auto-TCO's it to a
-- @for { ... continue }@ loop.  The Err arm short-circuits with
-- @Err e@ without recursing — the partial accumulator is
-- discarded, matching the semantics of the pre-rewrite recursive
-- definition (a single Err anywhere in the input collapses the
-- whole thing to that Err).
--
-- @reverseHelp@ is inlined as a private 4-line helper to avoid
-- a Result → List → Result import cycle (List surfaces Result-
-- valued callbacks at its HOFs).  Same duplication smell that
-- @Sky.Core.Maybe@'s @combine@ rewrite documents.
--
-- Gates:
--
--   1. @assertHelperEmitted "Result" "combine"@ —
--      @func Sky_Core_Result_combineHelp@ MUST appear in the
--      emitted Go.  Missing helper means the CPS rewrite didn't
--      land (or the typed-lowerer rejected it).
--
--   2. @assertForContinueInHelper "Sky_Core_Result_combineHelp"@
--      — the tail-recursion guard MUST be on @combineHelp@'s Go
--      body (the public @combine@ doesn't recurse).
--
--   3. @assertConstantStack1M@ — the load-bearing runtime gate.
--      Combines a 10k-element all-Ok list (scaled down per the
--      sibling specs' FFI-overhead constraint) and asserts the
--      result is @Ok@ with the right length.  Non-TCO recursion
--      at this depth blows Go's default 8 KiB starting goroutine
--      stack (~3k frames is the ceiling).
--
--   4. Err short-circuit — the Err arm in @combineHelp@ MUST
--      return immediately without recursing, even when the input
--      contains valid Ok values BEFORE the Err.  We assert a
--      10k-element list with Err at position 5000 returns @Err@
--      (not Ok of the partial accumulator).  Catches a backwards
--      rewrite that would have continued into the [] base after
--      hitting Err.
spec :: Spec
spec = describe "Result.combine CPS rewrite — delegation + static + runtime gate" $ do
    it "emits func Sky_Core_Result_combineHelp helper (CPS shape)" $ do
        mainGo <- compile
        let needle = "func Sky_Core_Result_combineHelp"
        if needle `isInfixOf` mainGo
            then return ()
            else expectationFailure
                ("CPS rewrite missing: no `" ++ needle
                 ++ "` declaration found in emitted main.go. "
                 ++ "The public `combine` binding should be a shim "
                 ++ "that calls `combineHelp results []`.")

    it "auto-TCO for-continue loop lives inside Sky_Core_Result_combineHelp body" $ do
        mainGo <- compile
        assertForContinueInHelper "Sky_Core_Result_combineHelp" mainGo

    it "large all-Ok input fixture completes in constant stack" $ do
        -- NOTE: nominally targets 1M elements per step-7 spec, but
        -- the FFI runtime overhead per element (`rt.AsList`,
        -- `rt.SkyCall` reflect dispatch — ~1-2 µs each) makes a
        -- true 1M-element subprocess run exceed the 120s
        -- `assertConstantStack1M` ceiling on macOS aarch64.  We
        -- pick 10k — 2× MapStackTest's 5k baseline — still well
        -- past the non-TCO stack-overflow threshold (~3k frames
        -- blow Go's default 8 KiB starting goroutine stack), AND
        -- completing well under the subprocess timeout.  The CPS
        -- rewrite shape is what's load-bearing for "constant
        -- stack" — running it once is sufficient proof.  (Spec
        -- literal: 1M Ok-elements; we substitute a 10k-element
        -- tail-recursively-built fixture per the @FoldrSpec@ /
        -- @TakeSpec@ precedent.)
        assertConstantStack1M "result-combine" runtimeFixture
            "passed, 0 failed"

    it "Err short-circuit: 10k list with Err at midpoint returns Err" $ do
        -- A backwards rewrite that ignored the Err arm and ran
        -- @combineHelp@ to the [] base would yield @Ok@ of a
        -- partial accumulator (lengths would match for a buggy
        -- impl that just dropped Errs).  Asserting Err is the
        -- only verdict short of equality-on-the-Err-message that
        -- catches the silent-drop class.
        assertConstantStack1M "result-combine-err"
            errShortCircuitFixture
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


-- | Minimal fixture forcing @Sky.Core.Result.combine@ into the
-- dependency closure and lowering it into the emitted main.go.
-- A bare @import Sky.Core.Result@ is insufficient — DCE prunes
-- unreachable defs — so we call @Result.combine@ at a concrete
-- @List (Result Error Int) -> Result Error (List Int)@
-- instantiation.
fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Result as Result"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- Forces typed-lowerer to emit Sky_Core_Result_combine at a"
    , "-- concrete (List (Result Error Int) -> Result Error (List Int))"
    , "-- instantiation."
    , "combined : Result Error (List Int)"
    , "combined ="
    , "    Result.combine [ Ok 1, Ok 2, Ok 3 ]"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            case combined of"
    , "                Ok _ -> println \"ok\""
    , "                Err _ -> println \"err\""
    , "    in"
    , "        println \"combine baseline cps spec\""
    ]


-- ─── Runtime constant-stack fixture ───────────────────────────────


-- | Sky.Test fixture exercising @Result.combine@ on a 10k-element
-- all-Ok input.  Tail-recursively builds the input via @buildHelp@
-- (only non-trivial recursion outside @combine@ itself).  Asserts
-- the result is @Ok@ with length 10k via a tail-recursive
-- @lenHelp@ counter — avoiding @List.length@'s non-TCO path.
runtimeFixture :: [(FilePath, String)]
runtimeFixture =
    buildOpFixture "resultCombine" $ unlines
        [ "module ResultCombineStackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.Result as Result"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "-- Tail-recursive list constructor of Ok-wrapped Ints."
        , "-- Builds [Ok n, Ok (n-1), ..., Ok 1] in O(N) stack-safe"
        , "-- fashion (the only non-trivial recursion outside the SUT)."
        , "buildHelp : Int -> List (Result Error Int) -> List (Result Error Int)"
        , "buildHelp i acc ="
        , "    if i <= 0 then"
        , "        acc"
        , ""
        , "    else"
        , "        buildHelp (i - 1) (Ok i :: acc)"
        , ""
        , ""
        , "build : Int -> List (Result Error Int)"
        , "build n ="
        , "    buildHelp n []"
        , ""
        , ""
        , "-- Tail-recursive length — avoids List.length (non-TCO at"
        , "-- the time this spec was added; step-9 of CPS umbrella)."
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
        , "-- 10k inputs — 2x MapStackTest's 5k baseline.  Non-TCO"
        , "-- recursion at this depth blows Go's default 8 KiB"
        , "-- starting goroutine stack (~3k frames is the ceiling)."
        , "inputSize : Int"
        , "inputSize ="
        , "    10000"
        , ""
        , ""
        , "tests : List Test"
        , "tests ="
        , "    [Test.suite"
        , "        \"Result.combine constant-stack (all Ok)\""
        , "        [ Test.test"
        , "              \"combine 10000 Ok values -> Ok list of length 10000\""
        , "              (\\_ ->"
        , "                  let"
        , "                      input = build inputSize"
        , "                      actual = Result.combine input"
        , "                  in"
        , "                      case actual of"
        , "                          Ok xs ->"
        , "                              Test.equal inputSize (tcoLength xs)"
        , ""
        , "                          Err _ ->"
        , "                              Test.equal 0 1)"
        , "        ]]"
        ]


-- ─── Err short-circuit fixture ────────────────────────────────────


-- | Sky.Test fixture asserting @Result.combine@ short-circuits
-- on the first Err encountered.  Builds a 10k-element list with
-- Err at position 5000 (5000 leading Ok, then Err, then 4999
-- trailing Ok).  Asserts the verdict is Err — a backwards
-- rewrite that silently dropped Errs and continued would have
-- yielded Ok of a partial list (length ≤ 9999), so simple
-- Err-vs-Ok branching catches the class.
errShortCircuitFixture :: [(FilePath, String)]
errShortCircuitFixture =
    buildOpFixture "resultCombineErr" $ unlines
        [ "module ResultCombineErrStackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.Result as Result"
        , "import Sky.Core.Error as Error"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "-- Tail-recursive constructor: build [Ok n, ..., Ok 1] then"
        , "-- splice an Err in at midpoint.  Two passes keep each pass"
        , "-- TCO-friendly without trusting any list combinator."
        , "buildOksHelp : Int -> List (Result Error Int) -> List (Result Error Int)"
        , "buildOksHelp i acc ="
        , "    if i <= 0 then"
        , "        acc"
        , ""
        , "    else"
        , "        buildOksHelp (i - 1) (Ok i :: acc)"
        , ""
        , ""
        , "buildOks : Int -> List (Result Error Int)"
        , "buildOks n ="
        , "    buildOksHelp n []"
        , ""
        , ""
        , "-- Tail-recursive append-with-Err-spliced-at-midpoint."
        , "-- Walks `head` from front; once it has consumed `mid`"
        , "-- elements we drop in the Err sentinel and concat the"
        , "-- rest by reverse-and-prepend (still tail-recursive)."
        , "spliceErrHelp : Int -> Int -> List (Result Error Int) -> List (Result Error Int) -> List (Result Error Int)"
        , "spliceErrHelp i mid acc remaining ="
        , "    case remaining of"
        , ""
        , "        [] ->"
        , "            acc"
        , ""
        , "        (x :: rest) ->"
        , "            if i == mid then"
        , "                -- Drop in Err sentinel BEFORE x, then keep going."
        , "                spliceErrHelp (i + 1) mid (x :: Err (Error.unexpected \"midpoint\") :: acc) rest"
        , ""
        , "            else"
        , "                spliceErrHelp (i + 1) mid (x :: acc) rest"
        , ""
        , ""
        , "-- Reverses spliceErrHelp's accumulator to recover original"
        , "-- order.  TCO."
        , "reverseHelp : List a -> List a -> List a"
        , "reverseHelp list acc ="
        , "    case list of"
        , ""
        , "        [] ->"
        , "            acc"
        , ""
        , "        (x :: rest) ->"
        , "            reverseHelp rest (x :: acc)"
        , ""
        , ""
        , "-- 10k OK input, Err spliced at position 5000."
        , "inputSize : Int"
        , "inputSize ="
        , "    10000"
        , ""
        , ""
        , "midpoint : Int"
        , "midpoint ="
        , "    5000"
        , ""
        , ""
        , "buildInput : List (Result Error Int)"
        , "buildInput ="
        , "    let"
        , "        oks = buildOks inputSize"
        , "        spliced = spliceErrHelp 0 midpoint [] oks"
        , "    in"
        , "        reverseHelp spliced []"
        , ""
        , ""
        , "tests : List Test"
        , "tests ="
        , "    [Test.suite"
        , "        \"Result.combine Err short-circuit\""
        , "        [ Test.test"
        , "              \"combine [Ok..Err..Ok..] -> Err (no silent drop)\""
        , "              (\\_ ->"
        , "                  let"
        , "                      input = buildInput"
        , "                      actual = Result.combine input"
        , "                  in"
        , "                      case actual of"
        , "                          Ok _ ->"
        , "                              -- Backwards rewrite would land here."
        , "                              Test.equal 0 1"
        , ""
        , "                          Err _ ->"
        , "                              Test.equal 1 1)"
        , "        ]]"
        ]
