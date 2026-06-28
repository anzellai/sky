module Sky.Build.UserAliasShadowsRuntimeTypedSpec (spec) where

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcessMulti)


-- v0.17 regression spec — user record alias whose bare name collides
-- with a 'RuntimeMaps.runtimeTypedMap' kernel entry (Store, Cmd, Sub,
-- Decoder, Value, Attribute, Handler, Middleware, Session, Error,
-- HttpResponse, Db, Stmt, Row, Conn, VNode, Request) must resolve to
-- the user's @<base>_R@ struct, NOT the kernel @rt.SkyX@ alias.
--
-- Root cause closed: 'solvedTypeToGoViaPipelineFlat' was constructed
-- with 'emptyCgEnv' at the entry CAF, so the populated
-- @_cg_recordAliases@ registry (built at C9/C10 from
-- 'Rec.collectRecordAliases' on every dep + entry module) was
-- inaccessible to the pipeline.  The 'mapAliasType' fallthrough then
-- hit 'mcRuntimeTypedMap' lookup (which has @"Store" → "rt.SkyStore"@)
-- and emitted @rt.SkyStore@ for the user's @type alias Store = { ... }@
-- struct field.  The downstream @go build@ then rejected
-- @model.Store.Field@ accesses with
-- @type rt.SkyStore has no field or method Field@.
--
-- The repro is the same shape as the bundled console (sky-bundled/
-- console/src/State.sky:253) where a typed @type alias Store@ holds
-- a record-of-closures used by the Cmd.perform handlers; the
-- regression first surfaced on the v0.17.0 CI drift check of
-- @scripts/regenerate-console.sh@.
--
-- The fix at 'Compile.solvedTypeToGoViaPipelineFlat' reads
-- @LC._lc_cgEnv@ from 'scopeStateRef' at call time (NOINLINE CAF,
-- same pattern as 'lookupAliasDecl') so the populated registry is
-- live when the renderer fires.  Pre-fix this spec exhibits
-- @Store rt.SkyStore@ in the entry-module struct's field list;
-- post-fix it emits @Store DepStoreModule_Store_R@.
spec :: Spec
spec = describe "user record alias collides with runtimeTypedMap kernel name" $ do
    it "resolves bare-name Store to <Mod>_Store_R when user declared it as a record alias" $ do
        result <- compileInProcessMulti
            [ ("src/DepStoreModule.sky", depModuleSrc)
            , ("src/Main.sky",           entryMainSrc)
            ]
        case result of
            CompileErr e -> expectationFailure ("compile failed:\n" ++ e)
            CompileOk body -> do
                -- The entry Main_Model_R struct holds a Store field
                -- typed as the dep's Store alias.  Pre-fix the field
                -- emitted as `Store rt.SkyStore` (kernel mapping won)
                -- and the downstream Go build failed:
                --   type rt.SkyStore has no field or method
                --     ReadOverview / ReadLogs / etc.
                -- Post-fix it must emit as
                -- `Store DepStoreModule_Store_R` — the registry-resolved
                -- typed struct.
                let modelStructLines =
                        filter ("type Main_Model_R struct" `isInfixOf`)
                            (lines body)
                modelStructLines `shouldNotSatisfy` null

                -- Locate the struct line + assert the Store field is
                -- typed.  This is the load-bearing assertion: the
                -- failure mode was Store erasing to `rt.SkyStore`,
                -- producing well-typed Sky but broken Go.
                let structLine = head modelStructLines
                structLine `shouldSatisfy`
                    ("Store DepStoreModule_Store_R" `isInfixOf`)
                structLine `shouldNotSatisfy`
                    ("Store rt.SkyStore" `isInfixOf`)


-- ─── Fixtures ──────────────────────────────────────────────────

-- | Dep module that exports a record alias named `Store`.  The
-- bare name COLLIDES with the kernel's runtimeTypedMap entry
-- @"Store" → "rt.SkyStore"@ at Sky.Generate.Go.RuntimeMaps:96.
-- The fix must prefer the user's structural declaration.
depModuleSrc :: String
depModuleSrc = unlines
    [ "module DepStoreModule exposing (Store, defaultStore)"
    , ""
    , "type alias Store ="
    , "    { readOverview : () -> String"
    , "    , readLogs     : Int -> String"
    , "    }"
    , ""
    , "defaultStore : Store"
    , "defaultStore ="
    , "    { readOverview = \\_ -> \"overview\""
    , "    , readLogs     = \\n -> \"logs:\" ++ String.fromInt n"
    , "    }"
    ]


-- | Entry module that uses DepStoreModule.Store as a field type.
-- The struct emission goes through `solvedTypeToGoViaPipelineFlat`
-- (the regression site) for the field type rendering.
entryMainSrc :: String
entryMainSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import DepStoreModule exposing (Store, defaultStore)"
    , "import Std.Log exposing (println)"
    , ""
    , "type alias Model ="
    , "    { store : Store"
    , "    , tag   : String"
    , "    }"
    , ""
    , "initialModel : Model"
    , "initialModel ="
    , "    { store = defaultStore"
    , "    , tag   = \"main\""
    , "    }"
    , ""
    , "main ="
    , "    println initialModel.tag"
    ]
