module Sky.Build.IsPlainIdentSpec (spec) where

-- v0.15.x hardening — Gap A4 / Plan Item P3 closure.
--
-- `isPlainIdent` classifies a `GoExpr` as a "plain user identifier
-- chain" that the typed-routing arm of `coerceArg` (`Compile.hs`
-- line ~8660) trusts to pass raw into a generic-parameter slot.
-- Go's call-site type inference can pin the callee's TVar from
-- the source's STATIC Go type — provided the static type is
-- actually concrete at the call site.
--
-- The audit (Gap A4) + fragility-audit #7 residual flagged two
-- shape classes the original three-arm classifier mis-judged:
--
--   (a) Deep-selector chains rooted in a kernel call —
--       `rt.SkyCall(...).Field.Nested`.  The old recursion saw
--       the outermost `GoSelector` and recursed once on its
--       inner `GoSelector base = …`, which itself recursed
--       into `GoCall` and returned False — so the OUTERMOST
--       result was already False.  Recursion-correctness is
--       preserved by THIS spec's `kernel call → field → field`
--       case, which still asserts False.
--
--   (b) Selector chains whose intermediate base resolves to
--       `any` — `someValue.field.nested` where `someValue.field`
--       is statically `any` in the emitted Go.  Go's call-site
--       inference cannot pin a TVar from an `any`-typed base,
--       so passing the chain raw at a typed slot silently
--       routes a runtime panic.  The old `isPlainIdent` had no
--       arm for this — it accepted any selector chain whose
--       leaf was a plain ident, regardless of intermediate
--       types.  Closed by the new `isPlainIdentForTypedRouting`
--       wrapper (see `Compile.hs` for the typed companion).
--
-- This spec is a STRUCTURAL unit-table — it locks the recursion
-- invariants of `isPlainIdent` (the purely-syntactic classifier).
-- The companion typed gate is exercised by the runtime-shape
-- regression tests already in place (CoerceArgParametricSpec).

import Test.Hspec
import qualified Sky.Build.Compile as C
import qualified Sky.Build.LowerCtx as LC
import qualified Sky.Generate.Go.Ir as GoIr
import qualified Sky.Sky.ModuleName as ModuleName


-- | Empty `LowerCtx` for the typed-routing tests.  Mirrors the
-- pre-iter-15 empty-codegen-env behaviour (`scopeStateRef` empty,
-- `globalCgEnv` empty) — `goExprGoType` returns Nothing for every
-- ident/selector base, so the typed gate's "Nothing → False" rule
-- rejects EVERY selector chain.  Bare idents still pass.
emptyCtx :: LC.LowerCtx
emptyCtx = LC.emptyLowerCtx (ModuleName.Canonical "Main")


-- | Build a `someName` ident.
ident :: String -> GoIr.GoExpr
ident = GoIr.GoIdent


-- | Build a `rt.Name` ident (kernel callee shape).
rtIdent :: String -> GoIr.GoExpr
rtIdent name = GoIr.GoIdent ("rt." ++ name)


-- | Build a kernel call: `rt.Name(arg1, arg2, ...)`.
rtCall :: String -> [GoIr.GoExpr] -> GoIr.GoExpr
rtCall name args = GoIr.GoCall (rtIdent name) args


-- | Build a selector chain `base.f1.f2...`.
dot :: GoIr.GoExpr -> String -> GoIr.GoExpr
dot base field = GoIr.GoSelector base field


spec :: Spec
spec = describe "isPlainIdent (Gap A4 / P3 structural classifier)" $ do

    describe "leaf cases" $ do
        it "accepts a bare user ident" $
            C.isPlainIdent (ident "cfg") `shouldBe` True

        it "rejects an `rt.*` ident (kernel callee leaked into ref position)" $
            C.isPlainIdent (rtIdent "SkyCall") `shouldBe` False

        it "rejects a literal" $
            C.isPlainIdent (GoIr.GoIntLit 42) `shouldBe` False

        it "rejects a string literal" $
            C.isPlainIdent (GoIr.GoStringLit "hi") `shouldBe` False

        it "rejects a function literal" $
            C.isPlainIdent (GoIr.GoFuncLit [] "any" []) `shouldBe` False

        it "rejects a struct literal" $
            C.isPlainIdent (GoIr.GoStructLit "Foo_R" []) `shouldBe` False

        it "rejects a kernel call result directly" $
            C.isPlainIdent (rtCall "SkyCall" [ident "f", ident "x"])
                `shouldBe` False

        it "rejects a typed block (IIFE) result" $
            C.isPlainIdent (GoIr.GoTypedBlock "int" [] (GoIr.GoIntLit 1))
                `shouldBe` False

    describe "single-level selectors (ident.field)" $ do
        it "accepts `cfg.WfSubmit` (plain ident base)" $
            C.isPlainIdent (dot (ident "cfg") "WfSubmit") `shouldBe` True

        it "rejects `rt.X.Field` (rt.* base)" $
            C.isPlainIdent (dot (rtIdent "X") "Field") `shouldBe` False

        it "rejects `(rt.SkyCall(...)).Field` (kernel-call base)" $
            C.isPlainIdent (dot (rtCall "SkyCall" [ident "f"]) "Field")
                `shouldBe` False

        it "rejects `(42).Field` (literal base)" $
            C.isPlainIdent (dot (GoIr.GoIntLit 42) "Field") `shouldBe` False

    describe "deep selector chains (ident.field.field...)" $ do
        it "accepts `cfg.f1.f2` (ident root, two selectors)" $
            C.isPlainIdent (dot (dot (ident "cfg") "f1") "f2")
                `shouldBe` True

        it "accepts `cfg.f1.f2.f3` (ident root, three selectors)" $
            C.isPlainIdent (dot (dot (dot (ident "cfg") "f1") "f2") "f3")
                `shouldBe` True

        it "rejects `(rt.SkyCall(...)).Field.Nested`\
           \ (kernel-call root, two selectors)" $
            -- The audit's specific A4 reproducer shape: a
            -- malformed chain whose OUTERMOST construct is a
            -- selector AND whose deepest base is a kernel call.
            -- Recursion-correctness invariant — the classifier
            -- must walk to the leaf, not stop at the direct
            -- parent.
            let inner = rtCall "SkyCall" [ident "f", ident "x"]
                chain = dot (dot inner "Field") "Nested"
            in C.isPlainIdent chain `shouldBe` False

        it "rejects `(rt.SkyCall(...)).a.b.c.d`\
           \ (kernel-call root, four selectors)" $
            let inner = rtCall "SkyCall" [ident "f"]
                chain = dot (dot (dot (dot inner "a") "b") "c") "d"
            in C.isPlainIdent chain `shouldBe` False

        it "rejects `(rt.Coerce[T](x)).Field`\
           \ (Coerce wrapper base)" $
            let inner = GoIr.GoCall
                            (GoIr.GoIdent "rt.Coerce[Foo_R[any]]")
                            [ident "x"]
                chain = dot inner "Field"
            in C.isPlainIdent chain `shouldBe` False

        it "rejects `cfg.Field.(rt.Whatever(y))`\
           \ — IIFE / call in the chain is non-selector / non-ident\
           \ so the classifier never recurses into it" $
            -- This crafted shape (GoSelector wrapping a GoCall as a
            -- field name does not exist syntactically), so we model
            -- the realistic case where the OUTER base is a call —
            -- already covered above; here we verify the inverse: a
            -- selector wrapping a TypedBlock is rejected.
            let inner = GoIr.GoTypedBlock "int" [] (GoIr.GoIntLit 1)
                chain = dot inner "Field"
            in C.isPlainIdent chain `shouldBe` False

    describe "regression invariants (existing v0.15.3 acceptance set)" $ do
        it "accepts the `cfg.WfSubmit` shape that v0.15.3 closed"
            $ C.isPlainIdent (dot (ident "cfg") "WfSubmit")
                `shouldBe` True

        it "accepts a deep user-only chain (e.g. record-of-records)"
            $ C.isPlainIdent (dot (dot (ident "outer") "inner") "leaf")
                `shouldBe` True

    -- The typed-routing companion is the load-bearing P3 fix: it
    -- layers an `goExprGoType`-on-every-selector-base check on
    -- top of `isPlainIdent`.  In an empty codegen-env (no
    -- `scopeStateRef` push, no `globalCgEnv` registrations),
    -- `goExprGoType` returns Nothing for every ident/selector
    -- base, so the typed gate's "Nothing → False" rule rejects
    -- EVERY selector chain.  Bare idents still pass — the typed
    -- gate doesn't check the leaf's type (the leaf check belongs
    -- to `isParametricCompatibleSource` upstream).
    describe "isPlainIdentForTypedRouting (Gap A4 / P3 typed gate)" $ do
        it "accepts a bare user ident (leaf — type check\
           \ doesn't apply at the leaf)" $
            C.isPlainIdentForTypedRouting emptyCtx (ident "cfg") `shouldBe` True

        it "rejects a bare `rt.*` ident (structural-classifier\
           \ rejects first)" $
            C.isPlainIdentForTypedRouting emptyCtx (rtIdent "Whatever")
                `shouldBe` False

        it "rejects `cfg.WfSubmit` in an empty env (base's\
           \ `goExprGoType` returns Nothing → strict reject;\
           \ wrap path runs)" $
            -- In CODEGEN context with a populated lambda-types
            -- scope, `goExprGoType (GoIdent "cfg")` returns Just
            -- for parametric-record-alias-typed params (see
            -- `goExprGoType` parametric-alias arm) and the typed
            -- gate accepts.  Under the test harness — no env push
            -- — the base looks untyped and we conservatively wrap.
            -- The soundness floor.
            C.isPlainIdentForTypedRouting emptyCtx (dot (ident "cfg") "WfSubmit")
                `shouldBe` False

        it "rejects `(rt.SkyCall(...)).Field` (structural reject\
           \ — kernel-call base)" $
            C.isPlainIdentForTypedRouting emptyCtx
                (dot (rtCall "SkyCall" [ident "f"]) "Field")
                `shouldBe` False

        it "rejects `(rt.SkyCall(...)).Field.Nested` (the audit's\
           \ A4 deep-recursion case — structural reject)" $
            let inner = rtCall "SkyCall" [ident "f", ident "x"]
                chain = dot (dot inner "Field") "Nested"
            in C.isPlainIdentForTypedRouting emptyCtx chain `shouldBe` False

        it "rejects deep user-only chains in empty env (every\
           \ intermediate base resolves to Nothing)" $
            -- `outer.inner.leaf` — structural classifier accepts
            -- (all-plain-idents), typed gate rejects (no env).
            -- This is the WIDER test surface vs the structural
            -- classifier: P3 adds the type-aware soundness check
            -- on top of the structural classifier, and over-
            -- rejection here is the BENIGN side of the trade.
            C.isPlainIdentForTypedRouting emptyCtx
                (dot (dot (ident "outer") "inner") "leaf")
                `shouldBe` False

        it "rejects a literal (structural reject)" $
            C.isPlainIdentForTypedRouting emptyCtx (GoIr.GoIntLit 42)
                `shouldBe` False

        it "rejects a kernel call directly (structural reject)" $
            C.isPlainIdentForTypedRouting emptyCtx (rtCall "SkyCall" [ident "f"])
                `shouldBe` False
