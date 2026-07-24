-- | v0.13 Phase A2 — monomorphise type-level pieces regression fence.
--
-- Locks the mangling encoding + substitution semantics that the
-- downstream emission pass relies on.  Regression here means
-- mangled Go names drift (collision risk) or type substitution
-- corrupts function bodies (silent miscompilation).
module Sky.Build.MonomorphiseSpec (spec) where

import Test.Hspec
import qualified Data.Map.Strict as Map
import qualified Sky.AST.Canonical as Can
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Type.Solve as Solve
import Sky.Build.Monomorphise


tyInt, tyString, tyBool, tyUnit :: Can.Type
tyInt    = Can.TType ModuleName.basics "Int" []
tyString = Can.TType ModuleName.basics "String" []
tyBool   = Can.TType ModuleName.basics "Bool" []
tyUnit   = Can.TUnit


maybeOf :: Can.Type -> Can.Type
maybeOf a = Can.TType ModuleName.maybe_ "Maybe" [a]


listOf :: Can.Type -> Can.Type
listOf a = Can.TType ModuleName.list "List" [a]


spec :: Spec
spec = do
    describe "mangleType — encoding rules" $ do

        it "encodes primitives as their bare names" $ do
            mangleType tyInt    `shouldBe` "Int"
            mangleType tyString `shouldBe` "String"
            mangleType tyBool   `shouldBe` "Bool"
            mangleType tyUnit   `shouldBe` "Unit"

        it "encodes Maybe Int as MaybeOf_Int" $ do
            mangleType (maybeOf tyInt) `shouldBe` "MaybeOf_Int"

        it "encodes nested generics" $ do
            mangleType (maybeOf (listOf tyInt)) `shouldBe` "MaybeOf_ListOf_Int"

        it "encodes function types with FnOf" $ do
            mangleType (Can.TLambda tyInt tyString) `shouldBe` "FnOf_Int_String"

        it "encodes tuples with Tup<N>Of" $ do
            mangleType (Can.TTuple tyInt tyString [])
                `shouldBe` "Tup2Of_Int_String"
            mangleType (Can.TTuple tyInt tyString [tyBool])
                `shouldBe` "Tup3Of_Int_String_Bool"

        it "encodes records with sorted field keys + shape hash" $ do
            let recA = Can.TRecord (Map.fromList
                    [ ("name", Can.FieldType 0 tyString)
                    , ("age",  Can.FieldType 1 tyInt) ]) Nothing
                recB = Can.TRecord (Map.fromList
                    [ ("age",  Can.FieldType 0 tyInt)
                    , ("name", Can.FieldType 1 tyString) ]) Nothing
            -- Same fields & types, different declaration order →
            -- same mangle (because we sort keys).
            mangleType recA `shouldBe` mangleType recB
            mangleType recA `shouldSatisfy` (\s ->
                take 6 s == "RecOf_")

        it "differentiates records with same keys but different types" $ do
            let recA = Can.TRecord (Map.fromList
                    [ ("v", Can.FieldType 0 tyInt) ]) Nothing
                recB = Can.TRecord (Map.fromList
                    [ ("v", Can.FieldType 0 tyString) ]) Nothing
            mangleType recA `shouldNotBe` mangleType recB

    describe "mangleInstance" $ do

        it "produces deterministic Go names from CallInstance" $ do
            let inst = Solve.CallInstance "Sky.Core.Maybe.withDefault" [tyInt] ["a"]
            mangleInstance inst `shouldBe` "Sky_Core_Maybe_withDefault__Int"

        it "preserves multi-type-arg order" $ do
            let inst = Solve.CallInstance "Sky.Core.List.map" [tyInt, tyString] ["a", "b"]
            mangleInstance inst `shouldBe` "Sky_Core_List_map__Int_String"

        it "encodes nested generics in the type-args" $ do
            let inst = Solve.CallInstance "f" [maybeOf tyInt, listOf tyString] ["a", "b"]
            mangleInstance inst `shouldBe` "f__MaybeOf_Int_ListOf_String"

        it "omits the suffix when no type args (concrete instance)" $ do
            let inst = Solve.CallInstance "Sky.Core.System.cwd" [] []
            mangleInstance inst `shouldBe` "Sky_Core_System_cwd"

    describe "mangleQualName" $ do

        it "converts dots to underscores" $ do
            mangleQualName "Sky.Core.Maybe.withDefault"
                `shouldBe` "Sky_Core_Maybe_withDefault"

        it "leaves Sky-safe identifiers untouched otherwise" $ do
            mangleQualName "myFunc_123" `shouldBe` "myFunc_123"

    describe "buildSubstitution" $ do

        it "zips Forall vars with concrete type-args" $ do
            let ann = Can.Forall ["a"] (Can.TVar "a")
                σ = buildSubstitution ann [tyInt]
            Map.lookup "a" σ `shouldBe` Just tyInt
            Map.size σ `shouldBe` 1

        it "skips the `any` wildcard like the solver does" $ do
            let ann = Can.Forall ["a", "any", "b"] (Can.TVar "a")
                σ = buildSubstitution ann [tyInt, tyString]
            Map.lookup "a" σ `shouldBe` Just tyInt
            Map.lookup "b" σ `shouldBe` Just tyString
            Map.lookup "any" σ `shouldBe` Nothing

    describe "substituteType" $ do

        it "replaces a TVar with its concrete substitution" $ do
            let σ = Map.fromList [("a", tyInt)]
            substituteType σ (Can.TVar "a") `shouldBe` tyInt

        it "leaves unmentioned TVars untouched" $ do
            let σ = Map.fromList [("a", tyInt)]
            substituteType σ (Can.TVar "b") `shouldBe` Can.TVar "b"

        it "recurses into function types" $ do
            let σ = Map.fromList [("a", tyInt), ("b", tyString)]
                input  = Can.TLambda (Can.TVar "a") (Can.TVar "b")
                output = Can.TLambda tyInt tyString
            substituteType σ input `shouldBe` output

        it "recurses into parametric types" $ do
            let σ = Map.fromList [("a", tyInt)]
                input  = maybeOf (Can.TVar "a")
                output = maybeOf tyInt
            substituteType σ input `shouldBe` output

        it "recurses into nested generics" $ do
            let σ = Map.fromList [("a", tyInt)]
                input  = maybeOf (listOf (Can.TVar "a"))
                output = maybeOf (listOf tyInt)
            substituteType σ input `shouldBe` output

        it "recurses into records" $ do
            let σ = Map.fromList [("a", tyInt)]
                input = Can.TRecord (Map.fromList
                    [ ("v", Can.FieldType 0 (Can.TVar "a")) ]) Nothing
                output = Can.TRecord (Map.fromList
                    [ ("v", Can.FieldType 0 tyInt) ]) Nothing
            substituteType σ input `shouldBe` output

        it "recurses into tuples" $ do
            let σ = Map.fromList [("a", tyInt), ("b", tyString)]
                input  = Can.TTuple (Can.TVar "a") (Can.TVar "b") []
                output = Can.TTuple tyInt tyString []
            substituteType σ input `shouldBe` output

    describe "typesEquiv" $ do

        it "equates identical concrete types" $ do
            typesEquiv tyInt tyInt `shouldBe` True

        it "rejects distinct concrete types" $ do
            typesEquiv tyInt tyString `shouldBe` False

        it "alpha-equates TVars" $ do
            -- `a → a` ≡ `b → b` under alpha-renaming
            let t1 = Can.TLambda (Can.TVar "a") (Can.TVar "a")
                t2 = Can.TLambda (Can.TVar "b") (Can.TVar "b")
            typesEquiv t1 t2 `shouldBe` True

        it "recurses through parametric types" $ do
            typesEquiv (maybeOf tyInt) (maybeOf tyInt) `shouldBe` True
            typesEquiv (maybeOf tyInt) (maybeOf tyString) `shouldBe` False
