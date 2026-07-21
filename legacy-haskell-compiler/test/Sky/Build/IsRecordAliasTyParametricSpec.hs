module Sky.Build.IsRecordAliasTyParametricSpec (spec) where

-- v0.17 #631 regression spec — isRecordAliasTy must accept
-- parametric instantiations of record aliases (`Foo_R[T1]`,
-- `RetryPolicy_R[Sky_Core_Error_Error]`, etc.).
--
-- Pre-fix `isRecordAliasTy` required the type string to END in
-- the literal `_R` suffix.  Parametric instantiations end in `]`
-- so the function returned False — silently routing
-- `coerceArg`'s parametric-record-alias coercion through the
-- final `else GoTypeAssert (any e) target` arm at Compile.hs:
-- 11922 instead of `rt.Coerce[Target](e)`.  The resulting
-- `any(X).(RetryPolicy_R[Error])` cast panics at runtime when
-- the source's static Go type is `RetryPolicy_R[any]` (which
-- happens for every kernel-return TVar that appears only in the
-- return position, e.g. `Task.linearBackoff : Int -> Int ->
-- RetryPolicy e`).
--
-- The fix at Compile.hs:isRecordAliasTy extends the predicate
-- to also accept the parametric shape `<Module_>Name_R[…]`.
-- Routing through `rt.Coerce[Target]` bridges the
-- `[any]→[Error]` instantiation gap via the reflect-backed
-- field-walk path that `rt.RecordUpdate` already uses for
-- cross-instantiation coercion.

import Test.Hspec

import qualified Sky.Build.Compile as Compile


spec :: Spec
spec = describe "isRecordAliasTy parametric instantiations (#631)" $ do
    it "accepts bare record alias" $
        Compile.isRecordAliasTy "Sky_Core_Task_RetryPolicy_R"
            `shouldBe` True

    it "accepts module-qualified parametric record alias" $
        Compile.isRecordAliasTy
            "Sky_Core_Task_RetryPolicy_R[Sky_Core_Error_Error]"
            `shouldBe` True

    it "accepts single-TVar parametric record alias" $
        Compile.isRecordAliasTy "Cfg_R[T1]" `shouldBe` True

    it "accepts multi-arg parametric record alias" $
        Compile.isRecordAliasTy "Pair_R[T1, T2]" `shouldBe` True

    it "accepts nested-bracket parametric record alias" $
        Compile.isRecordAliasTy
            "Slot_R[Sky_Core_Task_RetryPolicy_R[any]]"
            `shouldBe` True

    it "rejects bare `any`" $
        Compile.isRecordAliasTy "any" `shouldBe` False

    it "rejects primitive `string`" $
        Compile.isRecordAliasTy "string" `shouldBe` False

    it "rejects slice of record alias (caller-side strip)" $
        Compile.isRecordAliasTy "[]Cfg_R" `shouldBe` False

    it "rejects empty string" $
        Compile.isRecordAliasTy "" `shouldBe` False

    it "rejects non-alias identifier" $
        Compile.isRecordAliasTy "Foo" `shouldBe` False
