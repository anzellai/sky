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
    -- v0.17 close: the C2 byte-identity contract has been INTENTIONALLY
    -- invalidated for 6 arms where the pipeline now carries policies
    -- the legacy 'typeToGo' doesn't.  Surface-area changes:
    --
    -- * TTuple of primitives — pipeline emits typed @rt.T2[A, B]@ via
    --   PR-17 SHIP POINT B; legacy emits @rt.SkyTuple2@.
    -- * TRecord (closed/extensible) — pipeline emits @Anon_R_<hash>@
    --   via 'synthAnonRecordName'; legacy emits inline @struct{...}@.
    -- * TType parameterised core types — pipeline routes Error,
    --   Decoder, Db, Cmd, Sub through 'mcRuntimeTypedMap'; legacy
    --   emits the bare name.
    -- * TType user-defined parameterised — pipeline strips type args
    --   for non-record-alias ADTs (the runtime alias is
    --   @type X = rt.SkyADT@); legacy preserves them.
    -- * Deeply nested composites — combination of the above.
    --
    -- These divergences are CORRECT (pipeline is strictly more typed
    -- + env-aware).  Parity tests that survived are documented below;
    -- failing arms were retired or restated to assert the post-v0.17
    -- pipeline shape directly.
    describe "C2 differential parity — typeToGo vs renderGoType . mapSkyTypeToGo" $ do
        let parity ty =
                renderGoType defaultRenderEnv
                    (mapSkyTypeToGo defaultMappingContext ty)
                    `shouldBe` typeToGo ty

        let pipelineEmits ty expected =
                renderGoType defaultRenderEnv
                    (mapSkyTypeToGo defaultMappingContext ty)
                    `shouldBe` expected

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

        it "TTuple arities 2/3/4 emit post-PR-17 typed forms" $ do
            -- v0.17 PR-17 SHIP POINT B — primitive-only tuples emit
            -- typed @rt.T{2,3}[...]@; arity 4+ stays on rt.SkyTupleN
            -- alias (no Go-side generic SkyTupleN).
            pipelineEmits
                (T.TTuple (T.TType bareHome "Int" []) (T.TType bareHome "String" []) [])
                "rt.T2[int, string]"
            pipelineEmits
                (T.TTuple
                    (T.TType bareHome "Int" [])
                    (T.TType bareHome "String" [])
                    [T.TType bareHome "Bool" []])
                "rt.T3[int, string, bool]"
            pipelineEmits
                (T.TTuple
                    (T.TType bareHome "Int" [])
                    (T.TType bareHome "String" [])
                    [T.TType bareHome "Bool" [], T.TType bareHome "Float" []])
                "rt.SkyTupleN"

        it "TRecord (closed) emits synthAnonRecordName-derived Anon_R_… alias" $ do
            -- v0.17 PR-22 S2 — bare anonymous records register a
            -- deterministic Anon_R_<fieldsHash>__<typesHash> name via
            -- 'synthAnonRecordName' so the codegen pass can emit the
            -- backing Go struct decl.  Legacy 'typeToGo' emitted an
            -- inline @struct{...}@ literal that broke @go build@ at
            -- type-position uses.
            let fields = Map.fromList
                    [ ("name", T.FieldType 0 (T.TType bareHome "String" []))
                    , ("age",  T.FieldType 1 (T.TType bareHome "Int" []))
                    ]
                rendered = renderGoType defaultRenderEnv
                    (mapSkyTypeToGo defaultMappingContext (T.TRecord fields Nothing))
            -- Deterministic prefix + hash suffix; can't assert exact hash
            -- (depends on Show of FieldType), but shape is stable.
            take 7 rendered `shouldBe` "Anon_R_"

        it "TRecord (extensible) routes through the same record mapper" $ do
            -- v0.17 PR-22 S4 — extensible records now route through
            -- 'mapRecordType' instead of the pre-S4 'any /* extensible
            -- record */' fallback.  Output is the same Anon_R_ alias.
            let fields = Map.fromList
                    [ ("name", T.FieldType 0 (T.TType bareHome "String" [])) ]
                rendered = renderGoType defaultRenderEnv
                    (mapSkyTypeToGo defaultMappingContext (T.TRecord fields (Just "rec")))
            take 7 rendered `shouldBe` "Anon_R_"

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

        it "TType parameterised core types — pipeline-shape asserts" $ do
            -- v0.17 — pipeline routes List/Dict/Set through native Go
            -- collections (slice / map[string]V / map[any]bool) instead
            -- of @rt.SkyList[X]@ aliases that don't exist in the runtime.
            -- Maybe/Result/Task keep their typed-named form.
            pipelineEmits
                (T.TType listHome "List" [T.TType bareHome "Int" []])
                "[]int"
            pipelineEmits
                (T.TType bareHome "Maybe" [T.TType bareHome "String" []])
                "rt.SkyMaybe[string]"
            pipelineEmits
                (T.TType bareHome "Result"
                    [ T.TType bareHome "String" []
                    , T.TType bareHome "Int" []
                    ])
                "rt.SkyResult[string, int]"
            -- TVar in Task position → GoTypeVar "A" (default policy
            -- preserves the type-param name; mcTVarsToAny=True policy
            -- would map to GoAny).
            pipelineEmits
                (T.TType bareHome "Task"
                    [ T.TType bareHome "String" []
                    , T.TVar "a"
                    ])
                "rt.SkyTask[string, A]"
            pipelineEmits
                (T.TType bareHome "Dict"
                    [ T.TType bareHome "String" []
                    , T.TType bareHome "Int" []
                    ])
                "map[string]int"
            pipelineEmits
                (T.TType bareHome "Set" [T.TType bareHome "Int" []])
                "map[any]bool"
            -- v0.17 PR-18 — Cmd/Sub stay on bare alias for TVar args
            -- because the typed sibling @rt.SkyCmd_T[X]@ requires the
            -- concrete X to be in Go scope.
            pipelineEmits
                (T.TType bareHome "Cmd" [T.TVar "msg"])
                "rt.SkyCmd"
            pipelineEmits
                (T.TType bareHome "Sub" [T.TVar "msg"])
                "rt.SkySub"

        it "parity on TType Html (special-cased)" $
            parity (T.TType bareHome "Html" [T.TVar "msg"])

        it "TType user-defined under default ctx falls through to any" $ do
            -- v0.17 PR-22 S6 — without env data the user-ADT fallback
            -- can't classify the type as a record-alias / union /
            -- runtime-typed shape, so 'mapNamedType' emits 'GoAny'.
            -- Real codegen uses 'buildMappingContext getCgEnv' which
            -- populates the registries — this default-ctx behaviour is
            -- intentional (a TVar fallback under empty env).
            pipelineEmits (T.TType userHome "Color" []) "any"
            pipelineEmits
                (T.TType userHome "Widget" [T.TVar "msg"])
                "any"
            pipelineEmits
                (T.TType userHome "Cfg"
                    [ T.TVar "msg"
                    , T.TType bareHome "Int" []
                    ])
                "any"

        it "parity on TAlias (Hoisted + Filled — both pass through to inner)" $ do
            let inner = T.TType bareHome "Int" []
            parity (T.TAlias bareHome "Age" [] (T.Hoisted inner))
            parity (T.TAlias bareHome "Age" [] (T.Filled inner))

        it "deeply nested composites under default ctx — pipeline-shape assert" $ do
            -- List (Result Error (Maybe (Cfg msg)))
            -- v0.17 pipeline emits:
            --   * List X → []X (native Go slice)
            --   * Error → Sky_Core_Error_Error (mcRuntimeTypedMap)
            --   * Cfg msg → any (user ADT with TVar arg under default ctx
            --     — no record-alias match, no union match, falls through
            --     to GoAny via the unknown-name fallback)
            let inner =
                    T.TType bareHome "List"
                        [ T.TType bareHome "Result"
                            [ T.TType bareHome "Error" []
                            , T.TType bareHome "Maybe"
                                [ T.TType userHome "Cfg" [T.TVar "msg"] ]
                            ]
                        ]
            pipelineEmits inner
                "[]rt.SkyResult[Sky_Core_Error_Error, rt.SkyMaybe[any]]"

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

        -- v0.17 PR-17 SHIP POINT B widening: when every element is a
        -- primitive (int / string / bool / rune / float64 / []byte) or
        -- already-typed (rt.T2..rt.T9 / _R-suffix alias), the renderer
        -- emits the typed form even under defaultRenderEnv.  Tests below
        -- assert the post-SHIP-POINT-B output.  The pre-widening "always
        -- alias under defaultRenderEnv" contract was a one-step gate
        -- the PR-17 widening retired.
        it "renders 2-tuple of primitives as rt.T2[A, B] under defaultRenderEnv" $
            renderGoType env
                (GoTuple [GoBare "int", GoBare "string"])
                `shouldBe` "rt.T2[int, string]"

        it "renders 3-tuple of primitives as rt.T3[A, B, C] under defaultRenderEnv" $
            renderGoType env
                (GoTuple [GoBare "int", GoBare "string", GoBare "bool"])
                `shouldBe` "rt.T3[int, string, bool]"

        it "renders 2-tuple with a non-typed element as rt.SkyTuple2 under defaultRenderEnv" $
            -- GoAny element fails the allTyped check, so the renderer
            -- falls back to the SkyTuple alias form.  This is the
            -- back-compat path for parametric/uncertain shapes.
            renderGoType env
                (GoTuple [GoBare "int", GoAny])
                `shouldBe` "rt.SkyTuple2"

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
