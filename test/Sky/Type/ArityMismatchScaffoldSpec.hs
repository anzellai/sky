module Sky.Type.ArityMismatchScaffoldSpec (spec) where

import Data.List (isInfixOf)
import Test.Hspec

import qualified Sky.Reporting.Annotation as A
import qualified Sky.Type.Type as T
import qualified Sky.Type.Solve as Solve


-- | PR-A scaffolding regression for the v0.17 strict-HM arity gate
-- (Limitation #7 close path).  This spec proves the new
-- 'T.CArityMismatch' constructor + 'Sky.Type.Solve.solveHelpBody'
-- arm + 'countConstraints' arm are wired end-to-end at the
-- solver layer — without depending on any caller emitting one.
--
-- PR-A is purely additive scaffolding.  Callers wire the gate at
-- 'constrainCall' (PR-C) + 'Can.VarKernel' / 'Can.VarTopLevel' arms
-- (PR-D).  This spec is the load-bearing assertion that the
-- constructor is REACHABLE end-to-end: build a constraint, hand it
-- to 'solve', verify the rendered diagnostic carries the binding
-- name + declared/supplied arities + the typed-error code prefix.
--
-- See @docs/v0.17-roadmap/strict-hm-arity-gate-design.md@ for the
-- full multi-PR plan.


-- | Synthetic region used for fixture construction.  The solver
-- prefixes the rendered string with a position marker derived from
-- this — the gate's diagnostic depth is verified at the call-site
-- arm in PR-C, not here.
dummyRegion :: A.Region
dummyRegion = A.Region (A.Position 1 1) (A.Position 1 1)


spec :: Spec
spec = describe "v0.17 PR-A — CArityMismatch scaffolding" $ do

    it "solver emits SolveError when handed a CArityMismatch" $ do
        result <- Solve.solve (T.CArityMismatch dummyRegion "Uuid.v4" 0 1)
        case result of
            Solve.SolveOk _ ->
                expectationFailure
                    ("Expected SolveError on CArityMismatch — solver returned"
                     ++ " SolveOk, meaning the new arm did not fire.")
            Solve.SolveError _ -> return ()

    it "diagnostic carries the binding name" $ do
        result <- Solve.solve (T.CArityMismatch dummyRegion "Uuid.v4" 0 1)
        case result of
            Solve.SolveOk _ ->
                expectationFailure "Expected SolveError, got SolveOk"
            Solve.SolveError msg ->
                msg `shouldSatisfy` \m -> "Uuid.v4" `isInfixOf` m

    it "diagnostic carries the declared arity D" $ do
        result <- Solve.solve (T.CArityMismatch dummyRegion "f" 3 1)
        case result of
            Solve.SolveOk _ ->
                expectationFailure "Expected SolveError, got SolveOk"
            Solve.SolveError msg ->
                msg `shouldSatisfy` \m -> "3-arg" `isInfixOf` m

    it "diagnostic carries the supplied arity S" $ do
        result <- Solve.solve (T.CArityMismatch dummyRegion "f" 0 2)
        case result of
            Solve.SolveOk _ ->
                expectationFailure "Expected SolveError, got SolveOk"
            Solve.SolveError msg ->
                msg `shouldSatisfy` \m -> "2 args" `isInfixOf` m

    it "diagnostic carries the [E2007] code prefix" $ do
        result <- Solve.solve (T.CArityMismatch dummyRegion "g" 1 0)
        case result of
            Solve.SolveOk _ ->
                expectationFailure "Expected SolveError, got SolveOk"
            Solve.SolveError msg ->
                msg `shouldSatisfy` \m -> "[E2007]" `isInfixOf` m

    it "solver does NOT halt on CArityMismatch wrapped in CAnd before later constraints" $ do
        -- Verifies the arm's short-circuit semantics: a CAnd
        -- chain with CArityMismatch as the first element returns
        -- the CArityMismatch error (first-error-wins per
        -- 'solveAll'), confirming the arm participates in the
        -- normal solver flow rather than throwing.
        result <- Solve.solve
            (T.CAnd
                [ T.CArityMismatch dummyRegion "h" 0 1
                , T.CTrue
                ])
        case result of
            Solve.SolveOk _ ->
                expectationFailure
                    ("Expected SolveError from CArityMismatch — solver"
                     ++ " returned SolveOk, so the arm was skipped or"
                     ++ " the chain mis-ordered.")
            Solve.SolveError msg ->
                msg `shouldSatisfy` \m -> "h" `isInfixOf` m
