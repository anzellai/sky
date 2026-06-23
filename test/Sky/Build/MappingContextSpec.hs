{-# LANGUAGE OverloadedStrings #-}

-- | v0.17 PR-4 — MappingContext + buildMappingContext.
--
-- Asserts:
--   1. Field accessors return the expected env-derived data.
--   2. buildMappingContext is TOTAL — every CodegenEnv shape produces
--      a valid MappingContext (no partial pattern matches inside).
--   3. The C2 differential parity property STILL holds with the widened
--      context: when mapSkyTypeToGo doesn't yet consult the new fields
--      (PR-4 state), buildMappingContext-derived contexts produce the
--      same output as defaultMappingContext.
--
-- Doc: docs/v0.17-full-e2e-typed-master-plan.md §"Phase α"
module Sky.Build.MappingContextSpec (spec) where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import Test.Hspec
import Sky.Generate.Go.Type
    ( MappingContext(..)
    , buildMappingContext
    , defaultMappingContext
    , defaultRenderEnv
    , mapSkyTypeToGo
    , renderGoType
    , typeToGo
    )
import qualified Sky.Generate.Go.Record as Rec
import qualified Sky.AST.Canonical as Can
import qualified Sky.Type.Solve as Solve
import qualified Sky.Type.Type as T

-- | A maximally-empty CodegenEnv — every map / set is empty.
emptyCgEnv :: Rec.CodegenEnv
emptyCgEnv = Rec.CodegenEnv
    { Rec._cg_solvedTypes        = Solve.emptySolvedTypes
    , Rec._cg_aliases            = Map.empty
    , Rec._cg_fieldIndex         = Map.empty
    , Rec._cg_zeroArgs           = Set.empty
    , Rec._cg_recordAliases      = Set.empty
    , Rec._cg_unionNames         = Set.empty
    , Rec._cg_unionDetails       = Map.empty
    , Rec._cg_enumNames          = Set.empty
    , Rec._cg_funcArities        = Map.empty
    , Rec._cg_funcParamTypes     = Map.empty
    , Rec._cg_funcRetType        = Map.empty
    , Rec._cg_funcUltimateRetType = Map.empty
    , Rec._cg_funcInferredSigs   = Map.empty
    , Rec._cg_callSiteInstances  = Map.empty
    , Rec._cg_funcSkyToGoTVars   = Map.empty
    , Rec._cg_sealedIfaceNames   = Set.empty
    }

-- | A populated CodegenEnv — exercises every PR-4 field channel.
populatedCgEnv :: Rec.CodegenEnv
populatedCgEnv = emptyCgEnv
    { Rec._cg_recordAliases = Set.fromList ["Model_R", "Cfg_R", "Lib_Db_Config"]
    , Rec._cg_unionNames    = Set.fromList ["Msg", "Lib_Db_Connection"]
    , Rec._cg_enumNames     = Set.fromList ["Lib_Db_Connection"]
    , Rec._cg_aliases       = Map.fromList
        [ ("Item",  Can.Alias [] T.TUnit)
        , ("Model", Can.Alias [] T.TUnit)
        ]
    }

spec :: Spec
spec = describe "v0.17 PR-4 — MappingContext + buildMappingContext" $ do

    describe "buildMappingContext on emptyCgEnv" $ do
        let ctx = buildMappingContext defaultRenderEnv emptyCgEnv
        it "mcRenderEnv is defaultRenderEnv"
            $ mcRenderEnv ctx `shouldBe` defaultRenderEnv
        it "mcRecordAliases is empty"
            $ mcRecordAliases ctx `shouldBe` Set.empty
        it "mcUnionNames is empty"
            $ mcUnionNames ctx `shouldBe` Set.empty
        it "mcEnumNames is empty"
            $ mcEnumNames ctx `shouldBe` Set.empty
        it "mcAliases is empty"
            $ Map.size (mcAliases ctx) `shouldBe` 0

    describe "buildMappingContext on populatedCgEnv" $ do
        let ctx = buildMappingContext defaultRenderEnv populatedCgEnv
        it "passes through mcRecordAliases verbatim"
            $ mcRecordAliases ctx `shouldBe`
                Set.fromList ["Model_R", "Cfg_R", "Lib_Db_Config"]
        it "passes through mcUnionNames verbatim"
            $ mcUnionNames ctx `shouldBe`
                Set.fromList ["Msg", "Lib_Db_Connection"]
        it "passes through mcEnumNames verbatim"
            $ mcEnumNames ctx `shouldBe`
                Set.fromList ["Lib_Db_Connection"]
        it "passes through mcAliases keys verbatim"
            $ Map.keys (mcAliases ctx) `shouldBe` ["Item", "Model"]

    describe "buildMappingContext is total" $ do
        it "doesn't error on emptyCgEnv"
            $ length (show (buildMappingContext defaultRenderEnv emptyCgEnv))
                `shouldSatisfy` (> 0)
        it "doesn't error on populatedCgEnv"
            $ length (show (buildMappingContext defaultRenderEnv populatedCgEnv))
                `shouldSatisfy` (> 0)

    describe "PR-4 parity contract — built context = default context (no consumer yet)" $ do
        -- The PR-4 invariant: mapSkyTypeToGo doesn't yet consult the new
        -- PR-4 fields (PRs 5-10 wire them up). So renderGoType output
        -- under defaultMappingContext == under buildMappingContext.
        --
        -- This test gates Phase α correctness: if a PR adds a field
        -- consumer without flipping the parity contract appropriately,
        -- this assertion catches it.
        let sampleTypes =
                [ T.TUnit
                , T.TVar "a"
                , T.TVar "msg"
                , T.TLambda (T.TVar "a") (T.TVar "b")
                , T.TTuple T.TUnit (T.TVar "a") []
                ]
            built = buildMappingContext defaultRenderEnv populatedCgEnv
        it "mapSkyTypeToGo identical under default vs. populated context (every sample type)" $ do
            mapM_ (\ty ->
                renderGoType defaultRenderEnv (mapSkyTypeToGo built ty)
                    `shouldBe`
                    renderGoType defaultRenderEnv (mapSkyTypeToGo defaultMappingContext ty))
                sampleTypes

    describe "legacy parity — typeToGo == renderGoType after buildMappingContext" $ do
        -- Same C2 differential parity property, but verified through
        -- buildMappingContext's output rather than defaultMappingContext.
        -- Tracks that buildMappingContext doesn't introduce any drift.
        let built = buildMappingContext defaultRenderEnv populatedCgEnv
            check ty = renderGoType defaultRenderEnv (mapSkyTypeToGo built ty)
                        `shouldBe` typeToGo ty
        it "TUnit"     $ check T.TUnit
        it "TVar a"    $ check (T.TVar "a")
        it "TLambda"   $ check (T.TLambda (T.TVar "a") (T.TVar "b"))
        it "TTuple"    $ check (T.TTuple (T.TVar "a") (T.TVar "b") [])
