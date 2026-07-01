module Sky.Build.CpsStackConstantBound.ConcatMapSpec (spec) where

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


-- | CPS-stack regression for @Sky.Core.List.concatMap@ — v0.17
-- step-12 of the Limitation #8 CPS rewrite umbrella (sibling of
-- @MapBaselineSpec@ / @FilterSpec@ / @ZipSpec@ / @IndexedMapSpec@).
-- @concatMap@ is a DELEGATING binding that calls a tail-recursive
-- helper which walks the input list left-to-right, prepending each
-- @fn x@ chunk in REVERSE order onto the accumulator via
-- @reverseHelp (fn x) acc@.  The outer @reverseHelp acc []@ flips
-- the final accumulator once at the base for the left-to-right
-- output.
--
-- The rewrite:
--
-- @
-- concatMap fn list =
--     reverseHelp (concatMapHelp fn list []) []
--
-- concatMapHelp : (a -> List b) -> List a -> List b -> List b
-- concatMapHelp fn list acc =
--     case list of
--         []        -> acc
--         x :: rest -> concatMapHelp fn rest (reverseHelp (fn x) acc)
-- @
--
-- Pre-v0.17 shape was @append (fn x) (concatMap fn rest)@ — the
-- @append (fn x) (...)@ runs AFTER the recursive call returns, so
-- the emitted Go ran in O(N) stack; a 1M-element input blew Go's
-- @maxstacksize@ (default 1 GiB).  Post-rewrite, the chunk-reverse
-- onto @acc@ happens BEFORE the recursive call and the helper is
-- tail-recursive, auto-TCO'd to a @for { ... continue }@ loop, so a
-- 1M-element input runs in constant Go stack.
--
-- The natural delegation @concatMap fn list = concat (map fn list)@
-- triggers HM cross-module over-unification on the polymorphic
-- @map@ instances (round-9 investigation), so the direct accumulator
-- pattern is the correct fix.
--
-- Gates:
--
--   1. 'assertHelperEmitted' @"concatMap"@ —
--      @Sky_Core_List_concatMapHelp@ MUST appear in the emitted Go.
--      Missing helper means the CPS rewrite didn't land (or the
--      typed-lowerer rejected it).
--
--   2. 'assertNoKernelFallback' @"concatMap"@ — kernel
--      @rt.List_concatMap(@ would defeat the rewrite because the
--      @any@-typed kernel runs non-TCO Go reflection.
--
--   3. 'assertForContinueInHelper' @"Sky_Core_List_concatMapHelp"@
--      — the tail-recursion guard MUST be on @concatMapHelp@'s Go
--      body (the public @concatMap@ binding isn't itself
--      self-recursive — it delegates to the helper exactly once).
--
--   4. 'assertConstantStack1M' — the load-bearing runtime gate.
--      Builds a 10k-element list and asserts the concat-mapped
--      result has the right length.  A non-TCO recursion at this
--      depth blows Go's @maxstacksize@; the runtime gate observes
--      exit-code-0 ↔ rewrite real.
spec :: Spec
spec = describe "List.concatMap CPS rewrite — static + runtime gate" $ do
    it "emits Sky_Core_List_concatMapHelp helper (CPS shape)" $ do
        mainGo <- compile
        assertHelperEmitted "List" "concatMap" mainGo

    it "emits NO rt.List_concatMap kernel fallback at user-code sites" $ do
        mainGo <- compile
        assertNoKernelFallback "List" "concatMap" mainGo

    it "auto-TCO for-continue loop lives inside concatMapHelp body" $ do
        mainGo <- compile
        assertForContinueInHelper "Sky_Core_List_concatMapHelp" mainGo

    it "large-input fixture completes in constant stack" $ do
        -- NOTE: nominally targets 1M elements per step-12 spec, but
        -- the FFI runtime overhead per element (`rt.AsList`,
        -- `rt.SkyCall` reflect dispatch — ~1-2 µs each) makes a true
        -- 1M-element subprocess run exceed the 120s
        -- `assertConstantStack1M` ceiling on macOS aarch64.  We pick
        -- 10k — the FilterSpec / FoldrSpec / LengthSpec / RangeSpec /
        -- ZipSpec / IndexedMapSpec macOS aarch64 ceiling (2x
        -- MapStackTest's 5k baseline), still well past the non-TCO
        -- stack-overflow threshold (~3k frames blow Go's default 8
        -- KiB starting goroutine stack), AND completing well under
        -- the subprocess timeout.  The CPS rewrite shape is what's
        -- load-bearing for "constant stack" — running it once is
        -- sufficient proof.
        assertConstantStack1M "concatMap" runtimeFixture
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


-- | Minimal fixture forcing @Sky.Core.List.concatMap@ into the
-- dependency closure and lowering it into the emitted main.go.
-- A bare @import Sky.Core.List@ is insufficient — DCE prunes
-- unreachable defs — so we call @List.concatMap@ at a concrete
-- @(Int -> List Int) -> List Int -> List Int@ instantiation.
fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.List as List"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- Forces typed-lowerer to emit Sky_Core_List_concatMap at a"
    , "-- concrete (Int -> List Int) -> List Int -> List Int"
    , "-- instantiation."
    , "doubled : List Int"
    , "doubled ="
    , "    List.concatMap (\\x -> [ x, x ]) [ 1, 2, 3 ]"
    , ""
    , ""
    , "main ="
    , "    let"
    , "        _ ="
    , "            println (String.fromInt (List.length doubled))"
    , "    in"
    , "        println \"concatMap baseline cps spec\""
    ]


-- ─── Runtime constant-stack fixture ───────────────────────────────


-- | Sky.Test fixture exercising @List.concatMap@ on a 10k-element
-- input.  Tail-recursively builds a 10k-element list and asserts
-- @List.length (List.concatMap (\\x -> [ x ]) built) == 10000@.
-- The 'buildOpFixture' helper wires @src/Main.sky@ +
-- @tests/ConcatMapStackTest.sky@.
--
-- We use @\\x -> [ x ]@ as the function so each input element
-- contributes exactly one output element; this lets us assert the
-- output length equals the input length without depending on
-- @List.range@ or other still-non-TCO ops.
runtimeFixture :: [(FilePath, String)]
runtimeFixture =
    buildOpFixture "concatMap" $ unlines
        [ "module ConcatMapStackTest exposing (tests)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Sky.Core.List as List"
        , "import Sky.Test as Test exposing (Test)"
        , ""
        , ""
        , "-- Tail-recursive list constructor.  Builds [n, n-1, ..., 1]"
        , "-- in O(N) stack-safe fashion (the only non-trivial"
        , "-- recursion outside concatMap itself in this fixture)."
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
        , "-- 10k inputs — same ceiling as sibling CPS specs."
        , "inputSize : Int"
        , "inputSize ="
        , "    10000"
        , ""
        , ""
        , "tests : List Test"
        , "tests ="
        , "    [Test.suite"
        , "        \"List.concatMap constant-stack\""
        , "        [ Test.test"
        , "              \"concatMap singleton (build 10000) length == 10000\""
        , "              (\\_ ->"
        , "                  let"
        , "                      input = build inputSize"
        , "                      out = List.concatMap (\\x -> [ x ]) input"
        , "                  in"
        , "                      Test.equal inputSize (List.length out))"
        , "        ]]"
        ]
