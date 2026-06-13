module Sky.Build.GoTypeAdtSpec (spec) where

import qualified Data.Map.Strict as Map
import Test.Hspec
import Sky.Generate.Go.Type
    ( GoType(..)
    , RenderEnv(..)
    , MappingContext(..)
    , defaultRenderEnv
    , defaultMappingContext
    , mapSkyTypeToGo
    , renderGoType
    , typeToGo
    , goTypeArgs
    )
import qualified Sky.Type.Type as T
import Sky.Sky.ModuleName (Canonical(..))

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

    -- ========================================================================
    -- C2 — differential parity: typeToGo vs mapSkyTypeToGo
    -- ========================================================================
    --
    -- For every T.Type below the two paths MUST agree:
    --
    --     typeToGo ty
    --         ==
    --     renderGoType defaultRenderEnv
    --         (mapSkyTypeToGo defaultMappingContext ty)
    --
    -- This locks the C2 contract: structural mapper produces identical
    -- output to the legacy String-based path.  C8+ enriches
    -- MappingContext with env data; those paths get their own asserts
    -- (no env data today → C2 parity remains green by construction).
    describe "C2 differential parity — typeToGo vs renderGoType . mapSkyTypeToGo" $ do
        let parity ty =
                renderGoType defaultRenderEnv
                    (mapSkyTypeToGo defaultMappingContext ty)
                    `shouldBe` typeToGo ty

        let basicsHome = Canonical "Sky.Core.Basics"
        let bareHome   = Canonical ""
        let listHome   = Canonical "Sky.Core.List"
        let userHome   = Canonical "Acme.Widget"

        it "parity on TVar" $ do
            parity (T.TVar "a")
            parity (T.TVar "msg")
            parity (T.TVar "comparable")

        it "parity on TUnit" $
            parity T.TUnit

        it "parity on TLambda" $ do
            parity (T.TLambda (T.TType bareHome "Int" []) (T.TType bareHome "String" []))
            parity (T.TLambda (T.TVar "a") (T.TVar "b"))
            parity
                (T.TLambda
                    (T.TType bareHome "Int" [])
                    (T.TLambda (T.TType bareHome "String" []) (T.TType bareHome "Bool" [])))

        it "parity on TTuple arities 2/3/4" $ do
            -- 2-tuple — TTuple a b []
            parity (T.TTuple (T.TType bareHome "Int" []) (T.TType bareHome "String" []) [])
            -- 3-tuple — TTuple a b [c]
            parity
                (T.TTuple
                    (T.TType bareHome "Int" [])
                    (T.TType bareHome "String" [])
                    [T.TType bareHome "Bool" []])
            -- N-tuple — TTuple a b [c, d]
            parity
                (T.TTuple
                    (T.TType bareHome "Int" [])
                    (T.TType bareHome "String" [])
                    [T.TType bareHome "Bool" [], T.TType bareHome "Float" []])

        it "parity on TRecord (closed)" $ do
            let fields = Map.fromList
                    [ ("name", T.FieldType 0 (T.TType bareHome "String" []))
                    , ("age",  T.FieldType 1 (T.TType bareHome "Int" []))
                    ]
            parity (T.TRecord fields Nothing)

        it "parity on TRecord (extensible — fallback raw)" $ do
            let fields = Map.fromList
                    [ ("name", T.FieldType 0 (T.TType bareHome "String" [])) ]
            parity (T.TRecord fields (Just "rec"))

        it "parity on TType primitives (qualified + bare)" $ do
            parity (T.TType basicsHome "Int" [])
            parity (T.TType basicsHome "Float" [])
            parity (T.TType basicsHome "Bool" [])
            parity (T.TType basicsHome "String" [])
            parity (T.TType basicsHome "Char" [])
            parity (T.TType bareHome "Int" [])
            parity (T.TType bareHome "Float" [])
            parity (T.TType bareHome "Bool" [])
            parity (T.TType bareHome "String" [])
            parity (T.TType bareHome "Char" [])
            parity (T.TType bareHome "Bytes" [])

        it "parity on TType parameterised core types" $ do
            parity (T.TType listHome "List" [T.TType bareHome "Int" []])
            parity (T.TType bareHome "Maybe" [T.TType bareHome "String" []])
            parity
                (T.TType bareHome "Result"
                    [ T.TType bareHome "String" []
                    , T.TType bareHome "Int" []
                    ])
            parity
                (T.TType bareHome "Task"
                    [ T.TType bareHome "String" []
                    , T.TVar "a"
                    ])
            parity
                (T.TType bareHome "Dict"
                    [ T.TType bareHome "String" []
                    , T.TType bareHome "Int" []
                    ])
            parity (T.TType bareHome "Set" [T.TType bareHome "Int" []])
            parity (T.TType bareHome "Cmd" [T.TVar "msg"])
            parity (T.TType bareHome "Sub" [T.TVar "msg"])

        it "parity on TType Html (special-cased)" $
            parity (T.TType bareHome "Html" [T.TVar "msg"])

        it "parity on TType user-defined (nullary + parameterised)" $ do
            parity (T.TType userHome "Color" [])
            parity (T.TType userHome "Widget" [T.TVar "msg"])
            parity
                (T.TType userHome "Cfg"
                    [ T.TVar "msg"
                    , T.TType bareHome "Int" []
                    ])

        it "parity on TAlias (Hoisted + Filled — both pass through to inner)" $ do
            let inner = T.TType bareHome "Int" []
            parity (T.TAlias bareHome "Age" [] (T.Hoisted inner))
            parity (T.TAlias bareHome "Age" [] (T.Filled inner))

        it "parity on deeply nested composites" $ do
            -- List (Result Error (Maybe (Cfg msg)))
            let inner =
                    T.TType bareHome "List"
                        [ T.TType bareHome "Result"
                            [ T.TType bareHome "Error" []
                            , T.TType bareHome "Maybe"
                                [ T.TType userHome "Cfg" [T.TVar "msg"] ]
                            ]
                        ]
            parity inner

    -- ========================================================================
    -- PR 1 — GoTuple constructor + goTypeArgs accessor
    -- ========================================================================
    --
    -- 'GoTuple [GoType]' replaces the lossy 'GoBare "rt.SkyTuple2"'
    -- shape from C2.  'goTypeArgs' is the structural replacement for
    -- the String-parsing seam @parseTupleTypeArgs@ at
    -- @Sky.Build.Compile@.  Cause-H Step 4 (consumer migration) flips
    -- the 'renderTupleGeneric' policy gate per call site; until then
    -- the renderer ships the alias form for byte parity with C2 +
    -- 'typeToGo'.
    describe "PR 1 — GoTuple + goTypeArgs (structural)" $ do
        let env = defaultRenderEnv
            genericEnv =
                defaultRenderEnv { renderTupleGeneric = True }
            bareHome = Canonical ""

        it "renders 2-tuple as rt.SkyTuple2 under defaultRenderEnv" $
            renderGoType env
                (GoTuple [GoBare "int", GoBare "string"])
                `shouldBe` "rt.SkyTuple2"

        it "renders 3-tuple as rt.SkyTuple3 under defaultRenderEnv" $
            renderGoType env
                (GoTuple [GoBare "int", GoBare "string", GoBare "bool"])
                `shouldBe` "rt.SkyTuple3"

        it "renders N≥4 as rt.SkyTupleN under defaultRenderEnv" $
            renderGoType env
                (GoTuple
                    [ GoBare "int"
                    , GoBare "string"
                    , GoBare "bool"
                    , GoBare "float64"
                    ])
                `shouldBe` "rt.SkyTupleN"

        it "renders 2-tuple as rt.T2[A, B] when renderTupleGeneric=True" $
            renderGoType genericEnv
                (GoTuple [GoBare "int", GoBare "string"])
                `shouldBe` "rt.T2[int, string]"

        it "renders 3-tuple as rt.T3[A, B, C] when renderTupleGeneric=True" $
            renderGoType genericEnv
                (GoTuple
                    [GoBare "int", GoBare "string", GoBare "bool"])
                `shouldBe` "rt.T3[int, string, bool]"

        it "still emits SkyTupleN at arity≥4 regardless of policy gate" $
            -- No Go-side generic SkyTupleN exists; the slice-backed
            -- variant is the only emission for arity ≥ 4.
            renderGoType genericEnv
                (GoTuple
                    [ GoBare "int"
                    , GoBare "string"
                    , GoBare "bool"
                    , GoBare "float64"
                    ])
                `shouldBe` "rt.SkyTupleN"

        it "renders typed nested generics inside a tuple" $
            -- (List Int, rt.SkyMaybe[String]) is the Cause-H Step 4
            -- canary shape — the legacy primitive-only whitelist
            -- rejected both elements; the typed pipeline keeps them.
            renderGoType genericEnv
                (GoTuple
                    [ GoNamed "rt.SkyList" [GoBare "int"]
                    , GoNamed "rt.SkyMaybe" [GoBare "string"]
                    ])
                `shouldBe` "rt.T2[rt.SkyList[int], rt.SkyMaybe[string]]"

        it "goTypeArgs returns Just for GoNamed args" $ do
            goTypeArgs (GoNamed "rt.SkyList" [GoBare "int"])
                `shouldBe` Just [GoBare "int"]
            goTypeArgs (GoNamed "rt.SkyResult" [GoBare "Error", GoBare "int"])
                `shouldBe` Just [GoBare "Error", GoBare "int"]
            goTypeArgs (GoNamed "Std_Html_Html" [])
                `shouldBe` Just []

        it "goTypeArgs returns Just for GoTuple args" $ do
            goTypeArgs (GoTuple [GoBare "int", GoBare "string"])
                `shouldBe` Just [GoBare "int", GoBare "string"]
            goTypeArgs (GoTuple [])
                `shouldBe` Just []

        it "goTypeArgs returns Nothing for non-applicative shapes" $ do
            goTypeArgs (GoBare "int")          `shouldBe` Nothing
            goTypeArgs GoUnit                  `shouldBe` Nothing
            goTypeArgs GoAny                   `shouldBe` Nothing
            goTypeArgs (GoFunc (GoBare "int") (GoBare "string"))
                                               `shouldBe` Nothing
            goTypeArgs (GoStruct [("F", GoBare "int")])
                                               `shouldBe` Nothing
            goTypeArgs (GoTypeVar "T1")        `shouldBe` Nothing
            goTypeArgs (GoRaw "/* anything */") `shouldBe` Nothing

        it "mapSkyTypeToGo lifts TTuple straight into GoTuple" $ do
            -- Structural shape — assert the constructor, not the
            -- rendered string.  This is the new contract: consumers
            -- pattern-match on 'GoTuple' instead of prefix-sniffing
            -- the rendered form.
            mapSkyTypeToGo defaultMappingContext
                (T.TTuple (T.TType bareHome "Int" []) (T.TType bareHome "String" []) [])
                `shouldBe` GoTuple [GoBare "int", GoBare "string"]
            mapSkyTypeToGo defaultMappingContext
                (T.TTuple
                    (T.TType bareHome "Int" [])
                    (T.TType bareHome "String" [])
                    [T.TType bareHome "Bool" []])
                `shouldBe` GoTuple
                    [GoBare "int", GoBare "string", GoBare "bool"]
