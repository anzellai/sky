module Sky.Build.GoTypeAdtSpec (spec) where

import Test.Hspec
import Sky.Generate.Go.Type
    ( GoType(..)
    , RenderEnv(..)
    , defaultRenderEnv
    , renderGoType
    )

-- | v0.17 C1 — Sky.Generate.Go.Type GoType ADT smoke tests.
--
-- These exercise 'renderGoType' on every constructor of 'GoType'.
-- The point is foundation-level: prove the new pipeline compiles and
-- renders deterministic Go strings before any caller is migrated off
-- 'solvedTypeToGo' (C2-C25).
spec :: Spec
spec = describe "v0.17 C1 — Sky.Generate.Go.Type" $ do
    let env = defaultRenderEnv

    describe "renderGoType" $ do
        it "renders bare primitives verbatim" $ do
            renderGoType env (GoBare "int") `shouldBe` "int"
            renderGoType env (GoBare "string") `shouldBe` "string"
            renderGoType env (GoBare "rune") `shouldBe` "rune"
            renderGoType env (GoBare "bool") `shouldBe` "bool"
            renderGoType env (GoBare "float64") `shouldBe` "float64"
            renderGoType env (GoBare "[]byte") `shouldBe` "[]byte"

        it "renders GoUnit as struct{}" $
            renderGoType env GoUnit `shouldBe` "struct{}"

        it "renders GoAny" $
            renderGoType env GoAny `shouldBe` "any"

        it "renders function types" $ do
            renderGoType env (GoFunc (GoBare "int") (GoBare "string"))
                `shouldBe` "func(int) string"
            renderGoType env
                (GoFunc (GoBare "int") (GoFunc (GoBare "string") (GoBare "bool")))
                `shouldBe` "func(int) func(string) bool"

        it "renders nullary named types without brackets" $ do
            renderGoType env (GoNamed "Std_Html_Html" [])
                `shouldBe` "Std_Html_Html"
            renderGoType env (GoNamed "rt.SkyADT" [])
                `shouldBe` "rt.SkyADT"

        it "renders parameterised named types with bracketed type args" $ do
            renderGoType env (GoNamed "rt.SkyList" [GoBare "int"])
                `shouldBe` "rt.SkyList[int]"
            renderGoType env (GoNamed "rt.SkyMaybe" [GoBare "string"])
                `shouldBe` "rt.SkyMaybe[string]"

        it "renders multi-arg named types comma-separated" $ do
            renderGoType env
                (GoNamed "rt.SkyResult" [GoBare "Error", GoBare "int"])
                `shouldBe` "rt.SkyResult[Error, int]"
            renderGoType env
                (GoNamed "rt.SkyDict" [GoBare "string", GoBare "int"])
                `shouldBe` "rt.SkyDict[string, int]"

        it "renders nested generics" $
            renderGoType env
                (GoNamed "rt.SkyTask"
                    [ GoBare "Error"
                    , GoNamed "rt.SkyList" [GoBare "int"]
                    ])
                `shouldBe` "rt.SkyTask[Error, rt.SkyList[int]]"

        it "renders anonymous struct types preserving field order" $ do
            renderGoType env
                (GoStruct [("Name", GoBare "string"), ("Age", GoBare "int")])
                `shouldBe` "struct{ Name string; Age int; }"

            -- Reverse field order — must be preserved verbatim (caller
            -- pre-sorts by _fieldIndex).
            renderGoType env
                (GoStruct [("Age", GoBare "int"), ("Name", GoBare "string")])
                `shouldBe` "struct{ Age int; Name string; }"

        it "renders type-variable identifiers verbatim" $ do
            renderGoType env (GoTypeVar "T1") `shouldBe` "T1"
            renderGoType env (GoTypeVar "Msg") `shouldBe` "Msg"

        it "renders GoRaw escape hatch verbatim" $
            renderGoType env (GoRaw "any /* extensible record */")
                `shouldBe` "any /* extensible record */"

    describe "defaultRenderEnv" $ do
        it "ships every policy gate in today's-runtime shape" $ do
            renderCmdGeneric defaultRenderEnv `shouldBe` False
            renderSubGeneric defaultRenderEnv `shouldBe` False
            renderTupleGeneric defaultRenderEnv `shouldBe` False
