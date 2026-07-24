{-# LANGUAGE OverloadedStrings #-}

-- | v0.17 PR-3 — round-trip property test for GoType ↔ Go-string.
--
-- Asserts:  parseGoType (renderGoType genericEnv x) == Just (canonicaliseGoType x)
-- for every constructor of 'GoType' the foundation is required to round-trip.
--
-- Lossy cases (documented exceptions, NOT round-tripped here):
--
--   * @GoTuple@ under @renderTupleGeneric = False@ (defaultRenderEnv) —
--     the alias form @rt.SkyTuple2@ loses element types. Tested separately:
--     parsing the alias form gives @GoNamed "rt.SkyTuple2" []@.
--   * @GoRaw@ — escape hatch; only round-trips when its content happens
--     to be a canonical structural form. Tested separately.
--
-- Doc: docs/v0.17-full-e2e-typed-master-plan.md §"Phase α"
module Sky.Build.GoTypeRoundTripSpec (spec) where

import Test.Hspec
import Sky.Generate.Go.Type
    ( GoType(..)
    , canonicaliseGoType
    , defaultRenderEnv
    , genericRenderEnv
    , parseGoType
    , renderGoType
    )

-- | Helper: the round-trip we want to hold.
roundTrips :: GoType -> Expectation
roundTrips g =
    let rendered = renderGoType genericRenderEnv g
        parsed   = parseGoType rendered
        expected = Just (canonicaliseGoType g)
    in parsed `shouldBe` expected

spec :: Spec
spec = describe "v0.17 PR-3 — GoType ↔ Go-string round trip" $ do

    describe "primitives + GoBare ↔ identity" $ do
        it "GoBare \"int\"" $ roundTrips (GoBare "int")
        it "GoBare \"string\"" $ roundTrips (GoBare "string")
        it "GoBare \"bool\"" $ roundTrips (GoBare "bool")
        it "GoBare \"float64\"" $ roundTrips (GoBare "float64")
        it "GoBare \"rune\"" $ roundTrips (GoBare "rune")
        it "GoBare \"byte\"" $ roundTrips (GoBare "byte")
        it "GoBare \"error\"" $ roundTrips (GoBare "error")

    describe "trivial constructors" $ do
        it "GoUnit ↔ struct{}" $ roundTrips GoUnit
        it "GoAny ↔ any"        $ roundTrips GoAny

    describe "GoTypeVar" $ do
        it "T1"  $ roundTrips (GoTypeVar "T1")
        it "T2"  $ roundTrips (GoTypeVar "T2")
        it "T17" $ roundTrips (GoTypeVar "T17")

    describe "GoFunc" $ do
        it "func(int) string"
            $ roundTrips (GoFunc (GoBare "int") (GoBare "string"))
        it "curried — func(int) func(string) bool"
            $ roundTrips
                (GoFunc (GoBare "int")
                    (GoFunc (GoBare "string") (GoBare "bool")))

    describe "GoNamed" $ do
        it "nullary user type: Std_Html_Html ↔ GoNamed _ []"
            $ roundTrips (GoNamed "Std_Html_Html" [])
        it "nullary stdlib type: rt.SkyADT ↔ GoNamed _ []"
            $ roundTrips (GoNamed "rt.SkyADT" [])
        it "single-arg generic: rt.SkyList[int]"
            $ roundTrips (GoNamed "rt.SkyList" [GoBare "int"])
        it "two-arg generic: rt.SkyDict[string, int]"
            $ roundTrips (GoNamed "rt.SkyDict" [GoBare "string", GoBare "int"])
        it "nested generic: rt.SkyTask[Error, rt.SkyList[int]]"
            $ roundTrips
                (GoNamed "rt.SkyTask"
                    [ GoBare "error"
                    , GoNamed "rt.SkyList" [GoBare "int"]
                    ])

    describe "GoTuple under genericEnv" $ do
        it "2-tuple of primitives: rt.T2[string, int]"
            $ roundTrips (GoTuple [GoBare "string", GoBare "int"])
        it "3-tuple of primitives: rt.T3[int, string, bool]"
            $ roundTrips (GoTuple [GoBare "int", GoBare "string", GoBare "bool"])
        it "2-tuple of named types: rt.T2[Cfg_R, Model_R]"
            $ roundTrips (GoTuple [GoNamed "Cfg_R" [], GoNamed "Model_R" []])
        it "tuple of generic named: rt.T2[rt.SkyList[int], Model_R]"
            $ roundTrips
                (GoTuple
                    [ GoNamed "rt.SkyList" [GoBare "int"]
                    , GoNamed "Model_R" []
                    ])

    describe "GoStruct" $ do
        it "{ Name string; Age int; }"
            $ roundTrips (GoStruct [("Name", GoBare "string"), ("Age", GoBare "int")])
        it "single-field struct"
            $ roundTrips (GoStruct [("X", GoBare "int")])

    describe "lossy cases (documented exceptions)" $ do
        it "GoTuple under defaultEnv with a non-typed element collapses to rt.SkyTuple2 alias" $ do
            -- Default env emits the back-compat alias form only when at
            -- least one element fails the 'isTypedTupleElem' check (e.g.
            -- 'GoAny' from an unresolved TVar).  The parser can recover
            -- only the alias name, not the element types.
            --
            -- v0.17 PR-17 SHIP POINT B widened defaultEnv emission: when
            -- every element is primitive / typed, the renderer emits the
            -- typed form even under defaultEnv (see GoTypeAdtSpec).
            let rendered = renderGoType defaultRenderEnv
                              (GoTuple [GoBare "int", GoAny])
            rendered `shouldBe` "rt.SkyTuple2"
            parseGoType rendered `shouldBe` Just (GoNamed "rt.SkyTuple2" [])

        it "GoTuple arity ≥ 4 always collapses to rt.SkyTupleN" $ do
            let rendered = renderGoType genericRenderEnv
                              (GoTuple
                                  [GoBare "int", GoBare "string",
                                   GoBare "bool", GoBare "float64"])
            rendered `shouldBe` "rt.SkyTupleN"
            parseGoType rendered `shouldBe` Just (GoNamed "rt.SkyTupleN" [])

    describe "parser hardening" $ do
        it "empty input → Nothing" $
            parseGoType "" `shouldBe` Nothing

        it "unterminated func(...)  → Nothing" $
            parseGoType "func(int" `shouldBe` Nothing

        it "unbalanced brackets → Nothing" $
            parseGoType "rt.SkyList[int" `shouldBe` Nothing

        it "non-primitive bare name parses as nullary GoNamed (canonical)" $
            parseGoType "MyType" `shouldBe` Just (GoNamed "MyType" [])

        it "primitive name parses as GoBare" $
            parseGoType "int" `shouldBe` Just (GoBare "int")

        it "trims leading + trailing whitespace" $
            parseGoType "  int  " `shouldBe` Just (GoBare "int")

    describe "canonicaliseGoType is idempotent" $ do
        it "twice == once on a nested shape" $ do
            let g = GoFunc (GoBare "MyType")
                          (GoNamed "rt.SkyList" [GoBare "OtherType"])
                c1 = canonicaliseGoType g
                c2 = canonicaliseGoType c1
            c2 `shouldBe` c1

        it "primitives stay GoBare" $ do
            canonicaliseGoType (GoBare "int") `shouldBe` GoBare "int"

        it "non-primitive GoBare becomes GoNamed _ []" $ do
            canonicaliseGoType (GoBare "Foo") `shouldBe` GoNamed "Foo" []
