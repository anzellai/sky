module Sky.Type.Limitation7CurrentLooseAcceptanceSpec (spec) where

-- v0.17 Limitation #7 closure — "red-then-green" reproduction gate.
--
-- BACKGROUND
-- ──────────
-- CLAUDE.md Limitation #7 documents the historical inconsistency in
-- Sky's handling of zero-arg / arity-0 stdlib calls:
--
--   * `Uuid.v4` (declared `: Task Error String`) — Sky lowering
--     ACCEPTS the call shape `Uuid.v4 ()`, even though the value is
--     not a function and the trailing `()` is therefore a spurious
--     application.  Go build then rejects with "cannot call
--     rt.Uuid_v4() (value of interface type any): any is not a
--     function".  This is the "Sky says OK, Go says NO" pattern —
--     classic loose acceptance.
--   * `Time.now ()` (declared `: () -> Task Error Int`) — bare
--     references to the function value (`Time.now`) without the `()`
--     are similarly accepted by Sky lowering today, even when the
--     value-slot expects the wrapped result.
--   * User TypedDefs where a `: String` binding is called with `()`,
--     or a `: () -> String` function is used as a plain `String`,
--     are also loosely accepted by current Sky lowering.
--
-- PURPOSE OF THIS SPEC
-- ────────────────────
-- This file is the FIRST gate in the four-step Limitation #7
-- closure plan:
--
--   step-1 (this file) — Capture the loose-shape ACCEPTANCE in the
--          current compiler.  Six fixtures: four negative cases that
--          assert COMPILE-OK today (proving the loose-shape is
--          accepted), plus two positive-control cases that MUST stay
--          compiling clean throughout.
--   step-2 — Diagnose root cause inside the canonicaliser / HM.
--   step-3 — Land the fix.
--   step-4 — INVERT the four negative cases in this file: each
--           `shouldSatisfy isCompileOk` flips to
--           `shouldSatisfy isCompileErr`, with a diagnostic-text
--           assertion ("expected X, got Y").  The two positive
--           controls stay green (gate against regression).
--
-- The "red-then-green" idiom: a failing test that documents the bug
-- is the discovery artefact.  Once step-4 inverts, the assertions
-- become the rock-solid future-proof gate against any reintroduction
-- of the loose-shape acceptance.
--
-- CURRENT (step-1) STATUS — EMPIRICAL OBSERVATION
-- ────────────────────────────────────────────────
-- Probing the in-process pipeline reveals an asymmetry that the
-- step plan's "4-negative-cases-all-loose-accepted" premise
-- partially missed:
--
--   * (k-a) `Uuid.v4 ()` — CURRENTLY ACCEPTED by Sky HM + lowering
--     (CompileOk); Go build rejects.  Genuine loose-shape.
--   * (k-b) bare `Time.now` — CURRENTLY ACCEPTED (CompileOk).
--     Genuine loose-shape on the kernel/FFI side.
--   * (u-a) user TypedDef `foo : String` called with `()` —
--     ALREADY REJECTED by HM today
--     (`Variable 'foo' type mismatch: String vs a -> b`).
--     The user-side strictness is already in place.
--   * (u-b) user TypedDef `bar : () -> String` as `String` value —
--     ALREADY REJECTED by HM today
--     (`Variable 'bar' type mismatch: () -> String vs String`).
--     Same story: user-side already strict.
--
-- The asymmetry is informative: Limitation #7's residual scope is
-- the kernel/FFI side specifically.  HM correctly rejects the
-- equivalent user-TypedDef shapes; the kernel call shape slips past
-- because the canonicaliser / kernel-dispatch path doesn't currently
-- propagate the `: Task Error String` arity-0 declaration into the
-- same HM unification gate.  Step-2/3 must close this kernel-side
-- specifically; step-4 inverts ONLY (k-a) + (k-b).
--
-- Spec design:
--   * (k-a), (k-b) assert CompileOk today (genuine loose-shape;
--     step-4 inverts to CompileErr).
--   * (u-a), (u-b) assert CompileErr today (the user-side gate that
--     the kernel-side fix must MATCH).  These act as POSITIVE
--     controls for the existing HM strictness — they must STAY
--     rejected after step-4 (regression guard).
--   * (h-a), (p-a) stay CompileOk throughout (canonical-shape
--     positive controls).
--
-- After step-4 the spec contract becomes:
--   * (k-a), (k-b): CompileErr (newly tightened).
--   * (u-a), (u-b): CompileErr (was-already-strict guard).
--   * (h-a), (p-a): CompileOk (canonical-shape guard).
--
-- Run via:
--   cabal test --test-options='--match "Sky.Type.Limitation7CurrentLooseAcceptance"'

import Data.List (isInfixOf)
import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


-- | True when the in-process compile pipeline (parse → canonicalise
-- → HM → lower) succeeds.  Equivalent to "Sky lowering succeeded"
-- in the CLI output.  Does NOT include `go build`, so a CompileOk
-- here can still produce Go that the Go compiler rejects.
isCompileOk :: CompileResult -> Bool
isCompileOk (CompileOk _) = True
isCompileOk _             = False


-- | True when the in-process compile pipeline fails.  Used for
-- the user-TypedDef negative cases which are already strict today
-- (HM rejects with `Variable 'X' type mismatch`).  This pair of
-- helpers makes the spec read symmetrically with respect to step-4
-- inversion.
isCompileErr :: CompileResult -> Bool
isCompileErr (CompileErr _) = True
isCompileErr _              = False


-- | Helper for the user-TypedDef cases: asserts CompileErr AND
-- that the error text contains the expected HM diagnostic.  This
-- pins the test to a specific reason for rejection so step-4
-- cannot accidentally "fix" the rejection in a wrong way.
hasTypeMismatch :: String -> CompileResult -> Bool
hasTypeMismatch needle (CompileErr msg) = needle `isInfixOf` msg
hasTypeMismatch _      _                = False


spec :: Spec
spec = do
    describe "Limitation #7 — loose-shape acceptance (red-then-green gate)" $ do

        ---------------------------------------------------------------
        -- NEGATIVE FIXTURES — currently accepted (step-4 will invert)
        ---------------------------------------------------------------

        it "(k-a) kernel: `Uuid.v4 ()` against `Uuid.v4 : Task Error String` — TODO step-4 invert" $ do
            -- `Uuid.v4` is declared `: Task Error String` in
            -- sky-stdlib/Sky/Core/Uuid.sky.  Applying `()` to a
            -- non-function value is the textbook loose-shape that
            -- step-4 must reject.  Sky lowering currently allows it;
            -- the Go compiler then rejects with "cannot call
            -- rt.Uuid_v4() (value of interface type any)".
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main ="
                    , "    let _ = Uuid.v4 () in println \"hi\""
                    ]
            result <- compileInProcess src
            result `shouldSatisfy` isCompileOk

        it "(k-b) kernel: bare `Time.now` reference against `Time.now : () -> Task Error Int` — TODO step-4 invert" $ do
            -- `Time.now` is declared `: () -> Task Error Int`.  A
            -- bare reference in a discarded `let _ =` slot is
            -- currently accepted as a value (function-typed); step-4
            -- should require either `Time.now ()` (apply) or
            -- documented bare-value semantics.  For the discard-slot
            -- specifically, the loose-shape lets a function value
            -- silently slip past as a discarded effect, which is
            -- exactly the soundness gap we want to close.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , "import Sky.Core.Time as Time"
                    , ""
                    , "main ="
                    , "    let _ = Time.now in println \"hi\""
                    ]
            result <- compileInProcess src
            result `shouldSatisfy` isCompileOk

        it "(u-a) user TypedDef `foo : String` called with `()` — ALREADY STRICT (was-strict guard)" $ do
            -- The user-level mirror of (k-a).  Empirical observation
            -- (probe at step-1 design): HM ALREADY rejects this
            -- shape today with
            -- `Variable 'foo' type mismatch: String vs a -> b`.
            -- The user-side strictness was in place pre-PR-C; the
            -- kernel-side (k-a) was the remaining gap.
            --
            -- v0.17 PR-C (iter 31) wired the strict-HM gate at
            -- 'constrainCall' for k-a + u-a.  The gate fires
            -- FIRST in the CAnd, so the user-side diagnostic
            -- short-circuited from the legacy CEqual "Variable foo
            -- type mismatch" to the targeted CArityMismatch
            -- "[E2007] Arity mismatch — Main.foo declared as
            -- 0-arg, called with 1 args."  The strictness contract
            -- is preserved (the shape is still rejected); the
            -- diagnostic upgrade is the point of the gate.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "foo : String"
                    , "foo = \"hi\""
                    , ""
                    , "main ="
                    , "    println (foo ())"
                    ]
            result <- compileInProcess src
            result `shouldSatisfy` hasTypeMismatch "[E2007] Arity mismatch"

        it "(u-b) user TypedDef `bar : () -> String` used as a `String` value — ALREADY STRICT (was-strict guard)" $ do
            -- The mirror of (k-b).  Empirical: HM ALREADY rejects
            -- with `Variable 'bar' type mismatch: () -> String vs String`.
            -- Same was-already-strict story — step-4 keeps this
            -- assertion as a regression guard.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "bar : () -> String"
                    , "bar = \\_ -> \"hi\""
                    , ""
                    , "main ="
                    , "    println bar"
                    ]
            result <- compileInProcess src
            result `shouldSatisfy` hasTypeMismatch "Variable 'bar' type mismatch"

        ---------------------------------------------------------------
        -- POSITIVE CONTROLS — must STAY green after step-4
        ---------------------------------------------------------------

        it "(h-a) HeadAlias positive control: `myHandler : Handler` compiles clean" $ do
            -- v0.16.4 contributor PR #123 closure (Limitation #5 /
            -- HeadAliasFunctionSig).  Canonical Elm-style annotation
            -- where the entire signature is a function-typed alias
            -- (`Handler = Request -> Task Error Response`) must keep
            -- compiling clean throughout step-2/3/4 — proves that
            -- tightening Limitation #7 does NOT regress the
            -- HeadAlias path.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , "import Sky.Http.Server as Server exposing (Handler)"
                    , "import Sky.Core.Task as Task"
                    , ""
                    , "myHandler : Handler"
                    , "myHandler _req ="
                    , "    Task.succeed (Server.text \"hi\")"
                    , ""
                    , "main ="
                    , "    println \"control: myHandler typechecks\""
                    ]
            result <- compileInProcess src
            result `shouldSatisfy` isCompileOk

        it "(p-a) Pure.* positive control: `Pure.uuidV4 ()` compiles clean" $ do
            -- v0.15.50 Limitation #7 mitigation.  Sky.Core.Pure
            -- ships the canonical `() -> Task Error a` companion
            -- surface; `Pure.uuidV4 ()` is the documented arity-1
            -- call shape.  Must stay compiling clean throughout —
            -- proves that step-4's tightening does NOT break the
            -- Pure.* migration path the user code is supposed to
            -- adopt.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , "import Sky.Core.Pure as Pure"
                    , "import Sky.Core.Task as Task"
                    , ""
                    , "main ="
                    , "    let _ = Pure.uuidV4 () in println \"hi\""
                    ]
            result <- compileInProcess src
            result `shouldSatisfy` isCompileOk
