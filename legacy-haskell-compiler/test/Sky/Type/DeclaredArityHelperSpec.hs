module Sky.Type.DeclaredArityHelperSpec (spec) where

import Test.Hspec

import qualified Sky.AST.Canonical as Can
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Type.Type as T
import Sky.Type.Constrain.Expression (declaredArity)


-- | PR-B regression for the v0.17 strict-HM arity gate.
--
-- 'declaredArity' is a PURE structural walk of a 'T.Annotation'
-- body — it peels the leading 'T.TLambda' chain and returns the
-- count.  No fresh UF vars; no solver interaction.  PR-C wires
-- it to 'constrainCall' (paired with the wildcard-`any` gate);
-- PR-D wires it to the 'Can.VarKernel' / 'Can.VarTopLevel' arms.
--
-- This spec is the load-bearing contract for the helper.  Every
-- shape that PR-C/PR-D depend on is locked here so a future
-- refactor of declaredArity cannot silently change behaviour at
-- the gate sites.
--
-- See @docs/v0.17-roadmap/strict-hm-arity-gate-design.md@ for
-- the full multi-PR plan.


-- | Synthetic module home used for fixture construction.  The
-- helper never looks at module identity — only at TLambda
-- structure — so the home string is irrelevant for the result.
dummyHome :: ModuleName.Canonical
dummyHome = ModuleName.Canonical ""


-- | Forall with no quantifiers — the monomorphic shape used by
-- kernel value bindings like 'Uuid.v4 : String'.
monoForall :: T.Type -> T.Annotation
monoForall = Can.Forall []


-- | Forall with one quantifier — a polymorphic shape like
-- 'identity : a -> a'.  Caller wiring (PR-C) MUST gate on
-- 'any (/= "any") freeVars' before calling 'declaredArity', so
-- real polymorphism stays on the per-call-site CForeign path.
polyForall :: [String] -> T.Type -> T.Annotation
polyForall = Can.Forall


-- | 'String' nominal — used as a leaf for both the strict-value
-- shape (Uuid.v4) and as the target of TLambda chains.
stringTy :: T.Type
stringTy = T.TType dummyHome "String" []


-- | 'Int' nominal.
intTy :: T.Type
intTy = T.TType dummyHome "Int" []


-- | '()' nominal — the unit-arg shape that Sky.Core.Pure surfaces
-- use ('Pure.uuidV4 : () -> Task Error String').
unitTy :: T.Type
unitTy = T.TUnit


-- | Build a 'Task Error a' type leaf for the kernel-arrow fixtures.
taskTy :: T.Type -> T.Type
taskTy inner = T.TType dummyHome "Task" [T.TType dummyHome "Error" [], inner]


-- | Synthetic 'Request' type — fixture for the cross-module
-- HeadAlias verification.
requestTy :: T.Type
requestTy = T.TType dummyHome "Request" []


-- | Synthetic 'Response' type — fixture for the cross-module
-- HeadAlias verification.
responseTy :: T.Type
responseTy = T.TType dummyHome "Response" []


spec :: Spec
spec = describe "v0.17 PR-B — declaredArity pure helper" $ do

    ----------------------------------------------------------------
    -- TIER 1 — bare value shapes (arity 0)
    ----------------------------------------------------------------

    it "returns 0 for monomorphic bare value (Uuid.v4 : String)" $
        declaredArity (monoForall stringTy) `shouldBe` 0

    it "returns 0 for nullary Forall over a leaf type" $
        declaredArity (monoForall intTy) `shouldBe` 0

    ----------------------------------------------------------------
    -- TIER 2 — single-arg shapes (arity 1)
    ----------------------------------------------------------------

    it "returns 1 for unit-arg arrow (Pure.uuidV4 : () -> Task Error String)" $
        declaredArity (monoForall (T.TLambda unitTy (taskTy stringTy)))
            `shouldBe` 1

    it "returns 1 for monomorphic single-arg (consume : String -> Int)" $
        declaredArity (monoForall (T.TLambda stringTy intTy)) `shouldBe` 1

    ----------------------------------------------------------------
    -- TIER 3 — multi-arg shapes
    ----------------------------------------------------------------

    it "returns 2 for two-arg arrow (over : Int -> Int -> Int)" $
        declaredArity (monoForall (T.TLambda intTy (T.TLambda intTy intTy)))
            `shouldBe` 2

    it "returns 3 for three-arg arrow" $
        declaredArity
            (monoForall
                (T.TLambda intTy
                    (T.TLambda stringTy
                        (T.TLambda intTy intTy))))
            `shouldBe` 3

    ----------------------------------------------------------------
    -- TIER 4 — polymorphic shapes (helper agnostic to Forall vars)
    ----------------------------------------------------------------

    -- The helper is structural.  Real-polymorphism gating happens
    -- AT THE CALL SITE (PR-C) — declaredArity itself just walks
    -- the body.  The caller MUST filter on
    -- 'any (/= "any") freeVars' before consulting the helper so
    -- v0.15.1's same-module polymorphic re-instantiation behaviour
    -- on CForeign stays intact.
    it "returns the same arity for polymorphic Forall as for monomorphic" $ do
        let mono = monoForall (T.TLambda intTy (T.TLambda intTy intTy))
        let poly = polyForall ["a"] (T.TLambda intTy (T.TLambda intTy intTy))
        declaredArity mono `shouldBe` declaredArity poly

    it "returns 1 for wildcard-only Forall (view : Model -> any)" $ do
        let modelTy = T.TType dummyHome "Model" []
        let body = T.TLambda modelTy (T.TVar "any")
        declaredArity (polyForall ["any"] body) `shouldBe` 1

    ----------------------------------------------------------------
    -- TIER 5 — HeadAlias verification anchor (PR-B step 2)
    ----------------------------------------------------------------

    -- 'myHandler : Handler' over
    -- 'type alias Handler = Request -> Task Error Response'
    -- is the v0.16.4 PR #123 shape.  Sky.Canonicalise.Module's
    -- 'arrowResultN' / 'arrowArgs' unfold the head TAlias before
    -- the TypedDef's annotation reaches the constrainer, so
    -- 'globalExternals' / 'globalSameModAnnots' store the UNFOLDED
    -- 'TLambda Request (Task Error Response)' shape.
    -- declaredArity on that unfolded shape returns 1 — matching
    -- the 1-arg user call shape 'myHandler req'.  No mis-classify.
    it "returns 1 on unfolded HeadAlias shape (TLambda Request (Task Error Response))" $
        declaredArity
            (monoForall (T.TLambda requestTy (taskTy responseTy)))
            `shouldBe` 1
