-- | v0.13 Phase A1 — call-instance capture regression fence.
--
-- Locks the solver's CForeign call-site capture mechanism.  Every
-- polymorphic-value reference (cross-module function call, kernel
-- function, ADT constructor) must produce one `CallInstance` entry
-- carrying the concrete type-arg list inferred at that site.
--
-- Downstream monomorphisation iterates the captured instance table
-- to emit one Go function per (callee, type-args) pair.  Regression
-- here means missed instances → unspecialised code → typed-codegen
-- coercion gap re-emerges.
module Sky.Type.InstanceCaptureSpec (spec) where

import Test.Hspec
import qualified Data.Map.Strict as Map
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Type.Type as T
import qualified Sky.Type.Solve as Solve


tyInt :: T.Type
tyInt = T.TType ModuleName.basics "Int" []


tyString :: T.Type
tyString = T.TType ModuleName.basics "String" []


-- ─── helpers ─────────────────────────────────────────────────────


-- | Synthesise an A.Region.  Specifics don't matter for capture
-- semantics — only the existence of distinct call sites.
mkRegion :: Int -> A.Region
mkRegion line =
    A.Region (A.Position line 1) (A.Position line 10)


-- | A simple polymorphic scheme: `forall a. a -> Maybe a -> a`.
-- Mirrors `Sky.Core.Maybe.withDefault`.
schemeMaybeWithDefault :: T.Annotation
schemeMaybeWithDefault =
    let maybeA = T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"]
    in T.Forall ["a"]
         (T.TLambda (T.TVar "a") (T.TLambda maybeA (T.TVar "a")))


-- | A polymorphic scheme: `forall a b. (a -> b) -> List a -> List b`.
-- Mirrors `List.map`.
schemeListMap :: T.Annotation
schemeListMap =
    let listA = T.TType ModuleName.list "List" [T.TVar "a"]
        listB = T.TType ModuleName.list "List" [T.TVar "b"]
        fnTy  = T.TLambda (T.TVar "a") (T.TVar "b")
    in T.Forall ["a", "b"]
         (T.TLambda fnTy (T.TLambda listA listB))


spec :: Spec
spec = do
    describe "Sky.Type.Solve.solveWithInstances" $ do

        it "captures a single concrete instantiation of Maybe.withDefault" $ do
            -- Constraint shape: a CForeign for Maybe.withDefault where
            -- the expected type unifies the `a` type variable with Int.
            -- That's the simplest possible polymorphic call site.
            let expected = T.NoExpectation
                    (T.TLambda tyInt
                        (T.TLambda
                            (T.TType ModuleName.maybe_ "Maybe" [tyInt])
                            tyInt))
                c = T.CForeign (mkRegion 5) "Sky.Core.Maybe.withDefault"
                        schemeMaybeWithDefault expected
            (res, ci, _) <- Solve.solveWithInstances c
            case res of
                Solve.SolveOk _ -> return ()
                Solve.SolveError e -> expectationFailure ("solve failed: " ++ e)
            length ci `shouldBe` 1
            case ci of
                [Solve.CallInstance callee tyArgs _quants] -> do
                    callee `shouldBe` "Sky.Core.Maybe.withDefault"
                    -- One quantified TVar (`a`); one concrete type arg.
                    length tyArgs `shouldBe` 1
                _ -> expectationFailure "expected exactly one instance"

        it "captures two distinct instantiations of the same function" $ do
            -- Two separate call sites, one Int-typed and one String-typed,
            -- both anding into a single CForeign-bundle.  Each must
            -- produce a distinct instance entry.
            let mkCall ty line = T.CForeign (mkRegion line)
                    "Sky.Core.Maybe.withDefault" schemeMaybeWithDefault
                    (T.NoExpectation
                        (T.TLambda ty
                            (T.TLambda
                                (T.TType ModuleName.maybe_ "Maybe" [ty])
                                ty)))
                c = T.CAnd
                        [ mkCall tyInt 5
                        , mkCall tyString 10
                        ]
            (res, ci, _) <- Solve.solveWithInstances c
            case res of
                Solve.SolveOk _ -> return ()
                Solve.SolveError e -> expectationFailure ("solve failed: " ++ e)
            -- Distinct concrete-type-args → distinct instances.
            length ci `shouldBe` 2

        it "captures a multi-tyvar instantiation of List.map" $ do
            let expected = T.NoExpectation
                    (T.TLambda
                        (T.TLambda tyInt tyString)
                        (T.TLambda
                            (T.TType ModuleName.list "List" [tyInt])
                            (T.TType ModuleName.list "List" [tyString])))
                c = T.CForeign (mkRegion 7) "Sky.Core.List.map"
                        schemeListMap expected
            (res, ci, _) <- Solve.solveWithInstances c
            case res of
                Solve.SolveOk _ -> return ()
                Solve.SolveError e -> expectationFailure ("solve failed: " ++ e)
            length ci `shouldBe` 1
            case ci of
                [Solve.CallInstance _ tyArgs _quants] -> do
                    -- Two quantified TVars (`a`, `b`).
                    length tyArgs `shouldBe` 2
                _ -> expectationFailure "expected exactly one instance"

        it "deduplicates structurally-identical instantiations" $ do
            -- Same call site logically (same callee, same type args)
            -- appearing twice in the constraint tree should collapse
            -- to one instance entry post-dedup.
            let mkCall line = T.CForeign (mkRegion line)
                    "Sky.Core.Maybe.withDefault" schemeMaybeWithDefault
                    (T.NoExpectation
                        (T.TLambda tyInt
                            (T.TLambda
                                (T.TType ModuleName.maybe_ "Maybe" [tyInt])
                                tyInt)))
                c = T.CAnd [mkCall 5, mkCall 10, mkCall 15]
            (res, ci, _) <- Solve.solveWithInstances c
            case res of
                Solve.SolveOk _ -> return ()
                Solve.SolveError e -> expectationFailure ("solve failed: " ++ e)
            -- Three call sites, all resolving to (Int) — one instance.
            length ci `shouldBe` 1

        it "returns an empty instance list when no polymorphic refs appear" $ do
            -- CTrue triggers no CForeign at all.
            (res, ci, _) <- Solve.solveWithInstances T.CTrue
            case res of
                Solve.SolveOk _ -> return ()
                Solve.SolveError e -> expectationFailure ("solve failed: " ++ e)
            ci `shouldBe` []

        it "returns no instances when solve fails (partial-capture safe)" $ do
            -- An impossible constraint: Int unified with String fails.
            -- The instances captured so far are discarded so downstream
            -- consumers don't see partial data on a broken build.
            let c = T.CEqual (mkRegion 1) T.CString tyInt
                        (T.NoExpectation tyString)
            (res, ci, _) <- Solve.solveWithInstances c
            case res of
                Solve.SolveError _ -> ci `shouldBe` []
                Solve.SolveOk _ ->
                    expectationFailure "expected solve to fail"
