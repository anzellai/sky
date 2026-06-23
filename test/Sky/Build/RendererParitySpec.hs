{-# LANGUAGE OverloadedStrings #-}

-- | v0.17 renderer parity property.
--
-- PR-1 shipped the corpus + structural scaffold.
-- PR-5 fills it in: 'Sky.Type.Solve.GoTypeBuild' is the bridge that
-- runs 'mapSkyTypeToGo' once per region in the solver's snapshot.
-- The parity property asserts: for every region in a 'SolvedTypes',
-- the legacy 'typeToGo' agrees with 'renderGoType defaultEnv' applied
-- to the 'srGoType' the bridge produced.
--
-- Pre-mortem lesson 2: coverage MUST include 'tailPositionRegions'
-- from 'Sky.Build.TailCallOpt'. We exercise this explicitly by
-- building a synthetic 'Can.Expr' with tail positions, collecting
-- the region set, and asserting the parity holds over both
-- the expression-body coverage AND the tail-position subset.
--
-- Doc: docs/v0.17-full-e2e-typed-master-plan.md
module Sky.Build.RendererParitySpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist, listDirectory)
import System.FilePath ((</>), takeFileName)
import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import Data.List (sort, isPrefixOf, isInfixOf)
import qualified Sky.Build.KnownDivergence as KD
import qualified Sky.Build.TailCallOpt as TCO
import qualified Sky.Generate.Go.Type as GoType
import qualified Sky.Generate.Go.Record as Rec
import qualified Sky.Type.Solve as Solve
import qualified Sky.Type.Solve.GoTypeBuild as GTB
import qualified Sky.Type.Type as T
import qualified Sky.Reporting.Annotation as A
import qualified Sky.AST.Canonical as Can
import qualified Sky.Sky.ModuleName as ModuleName

probesDir :: IO FilePath
probesDir = do
    cwd <- getCurrentDirectory
    return (cwd </> "tools" </> "probe-fixtures")

discoverProbes :: IO [FilePath]
discoverProbes = do
    root <- probesDir
    entries <- listDirectory root
    return (sort [ takeFileName e | e <- entries
                                  , "probe-" `isPrefixOf` e ])

-- | The minimal CodegenEnv every PR-5 parity test consumes.
-- Per-test populated maps go on top of this base.
baseCgEnv :: Rec.CodegenEnv
baseCgEnv = Rec.CodegenEnv
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

-- | A synthetic region — A.Region is just (start, end) positions.
mkRegion :: Int -> Int -> A.Region
mkRegion startCol endCol =
    A.Region (A.Position 1 startCol) (A.Position 1 endCol)

-- | A representative Sky type corpus for the parity property.
-- Covers every constructor 'mapSkyTypeToGo' / 'typeToGo' handle.
sampleSkyTypes :: [T.Type]
sampleSkyTypes =
    [ T.TUnit
    , T.TVar "a"
    , T.TVar "msg"
    , T.TLambda (T.TVar "a") (T.TVar "b")
    , T.TLambda T.TUnit (T.TVar "result")
    , T.TTuple T.TUnit (T.TVar "a") []
    , T.TTuple (T.TVar "a") (T.TVar "b") [T.TVar "c"]
    ]

-- | A synthetic 'Can.Expr' with multiple tail positions — Case +
-- branches + Let body. Used to drive 'tailPositionRegions' and
-- assert the resulting regions still parity-render.
syntheticTailExpr :: Can.Expr
syntheticTailExpr =
    A.At (mkRegion 1 50) $
        Can.Case
            (A.At (mkRegion 6 9) (Can.VarLocal "n"))
            [ Can.CaseBranch (A.At (mkRegion 14 15) Can.PAnything)
                (A.At (mkRegion 19 20) (Can.Int 0))
            , Can.CaseBranch (A.At (mkRegion 25 26) Can.PAnything)
                (A.At (mkRegion 30 45) $
                    Can.Let
                        (Can.Def (A.At (mkRegion 30 35) "k") []
                            (A.At (mkRegion 37 38) (Can.Int 1)))
                        (A.At (mkRegion 40 45)
                            (Can.VarLocal "k")))
            ]

spec :: Spec
spec = describe "v0.17 PR-5 — renderer parity" $ do

    describe "PR-1 infrastructure invariants" $ do
        it "discovers the 14 H/TCO fixtures the master plan calls for" $ do
            probes <- discoverProbes
            let hProbes   = filter ("probe-H"   `isPrefixOf`) probes
                tcoProbes = filter ("probe-TCO" `isPrefixOf`) probes
            let hNumbered = filter (\p -> any (`isPrefixOf` p)
                                          [ "probe-H1-", "probe-H2-"
                                          , "probe-H3-", "probe-H4-"
                                          , "probe-H5-", "probe-H6-"
                                          , "probe-H7-" ]) hProbes
            length hNumbered   `shouldBe` 7
            length tcoProbes   `shouldBe` 7

        it "every fixture ships sky.toml + src/Main.sky + expectations.txt + README.md" $ do
            root <- probesDir
            probes <- discoverProbes
            let required = ["sky.toml", "src/Main.sky", "expectations.txt", "README.md"]
            mapM_ (\probe -> mapM_ (\f -> do
                let p = root </> probe </> f
                exists <- doesFileExist p
                exists `shouldBe` True) required) probes

        it "KnownDivergence allowlist is empty at PR-1 (legacy is source of truth)" $ do
            length KD.knownDivergences `shouldBe` 0

        it "pre-mortem lesson 4: continue-block divergences are NOT allowlistable" $ do
            KD.isContinueBlockDivergence "TCO continue block reassignment"
                `shouldBe` True
            KD.isContinueBlockDivergence "rt.SkyTuple2 widen"
                `shouldBe` False

    describe "PR-5 GoTypeBuild parity property" $ do
        -- The foundation contract: for every region's Sky type in
        -- the SolvedTypes, the legacy String renderer (typeToGo) and
        -- the structural typed pipeline (renderGoType + mapSkyTypeToGo
        -- via buildGoTypeRegions) produce the same Go source string.
        --
        -- This is the property PRs 11-16 (Phase γ) rely on when they
        -- migrate consumers off the seven legacy renderers in
        -- Compile.hs. If this assertion breaks, the foundation has
        -- drifted and the migration is unsafe.
        let renderEnv = GoType.defaultRenderEnv
            buildSolvedFromTypes types =
                let regions = Map.fromList
                        [ (mkRegion i (i + 1), ty)
                        | (i, ty) <- zip [1..] types
                        ]
                in Solve.emptySolvedTypes
                    { Solve._stRegions = regions }
            built solved = GTB.buildGoTypeRegions renderEnv baseCgEnv solved

        it "every region in the sample Sky type corpus parity-renders" $ do
            let solved = buildSolvedFromTypes sampleSkyTypes
                regionMap = built solved
                check region sr =
                    let legacy = GoType.typeToGo (GTB.srSkyType sr)
                        typed  = GoType.renderGoType renderEnv (GTB.srGoType sr)
                    in (region, legacy) `shouldBe` (region, typed)
            mapM_ (uncurry check) (Map.toList regionMap)

        it "every region preserves its Sky type (srSkyType round-trips)" $ do
            let solved = buildSolvedFromTypes sampleSkyTypes
                regionMap = built solved
                origRegions = Solve._stRegions solved
            -- For each region: SolvedRegion.srSkyType must equal the
            -- original T.Type the solver wrote.
            mapM_ (\(r, ty) ->
                case Map.lookup r regionMap of
                    Just sr -> GTB.srSkyType sr `shouldBe` ty
                    Nothing -> expectationFailure ("missing region in built map: " ++ show r))
                (Map.toList origRegions)

        it "lookupSolvedGoType returns the structural GoType for known regions" $ do
            let solved = buildSolvedFromTypes [T.TUnit]
                regionMap = built solved
                onlyRegion = head (Map.keys regionMap)
            GTB.lookupSolvedGoType onlyRegion regionMap
                `shouldBe` Just GoType.GoUnit

        it "lookupSolvedRegion returns the whole record" $ do
            let solved = buildSolvedFromTypes [T.TUnit]
                regionMap = built solved
                onlyRegion = head (Map.keys regionMap)
            case GTB.lookupSolvedRegion onlyRegion regionMap of
                Just sr -> do
                    GTB.srSkyType sr `shouldBe` T.TUnit
                    GTB.srGoType  sr `shouldBe` GoType.GoUnit
                Nothing -> expectationFailure "lookupSolvedRegion missed"

        it "buildGoTypeRegions on an empty SolvedTypes produces an empty map" $ do
            Map.null (built Solve.emptySolvedTypes) `shouldBe` True

    describe "pre-mortem lesson 2 — tail-position regions" $ do
        -- The tail-position coverage gate. If the parity test
        -- excludes tail-position regions, PR-13's structural σ
        -- migration silently breaks TCO. tailPositionRegions must
        -- both EXIST and surface every region the TCO rewriter
        -- touches.

        it "tailPositionRegions enumerates the Case + branches + Let body" $ do
            let regions = TCO.tailPositionRegions syntheticTailExpr
            -- 1 (outer Case) + 2 (branch RHSs) + 1 (Let body)
            length regions `shouldSatisfy` (>= 4)

        it "non-tail subexpressions are NOT collected" $ do
            -- The Case subject (VarLocal "n" at (6, 9)) is NOT a
            -- tail position — it's the scrutinee. The collector
            -- must not include it.
            let regions = TCO.tailPositionRegions syntheticTailExpr
                subjectRegion = mkRegion 6 9
            elem subjectRegion regions `shouldBe` False

        it "parity holds on a synthetic SolvedTypes keyed by tail-position regions" $ do
            -- This is the gate. Build a SolvedTypes whose keys are
            -- exactly the tail-position regions from the synthetic
            -- expr; assert parity holds for each.
            let tailRegions = TCO.tailPositionRegions syntheticTailExpr
                solved = Solve.emptySolvedTypes
                    { Solve._stRegions = Map.fromList
                        [ (r, ty)
                        | (r, ty) <- zip tailRegions (cycle sampleSkyTypes)
                        ]
                    }
                regionMap = GTB.buildGoTypeRegions
                    GoType.defaultRenderEnv baseCgEnv solved
            mapM_ (\sr ->
                GoType.renderGoType GoType.defaultRenderEnv (GTB.srGoType sr)
                    `shouldBe` GoType.typeToGo (GTB.srSkyType sr))
                (Map.elems regionMap)

    describe "PR-22 flatMappingContext — TVar→any policy" $ do
        -- v0.17 Phase ε PR-22 — the 'mcTVarsToAny' policy gate.
        -- 'defaultMappingContext' renders TVars as 'GoTypeVar' idents;
        -- 'flatMappingContext' renders them as 'GoAny'.  This is the
        -- ONLY difference between the two contexts; all other arms
        -- recurse via 'mapSkyTypeToGo ctx', so the policy propagates
        -- into containers (Cmd, Maybe, Result, Task, List, Set, Dict,
        -- tuple elements, lambda from/to).
        --
        -- The flat variant unlocks migration of call sites that
        -- previously consumed 'solvedTypeToGoBounded' (TVar→"any"
        -- semantics) without forcing the entire MappingContext to
        -- carry that policy globally.

        let renderEnv = GoType.defaultRenderEnv

        it "default policy: T.TVar lowers to GoTypeVar (ident)" $ do
            let go = GoType.mapSkyTypeToGo GoType.defaultMappingContext (T.TVar "a")
            GoType.renderGoType renderEnv go `shouldBe` "A"

        it "flat policy: T.TVar lowers to GoAny" $ do
            let go = GoType.mapSkyTypeToGo GoType.flatMappingContext (T.TVar "a")
            GoType.renderGoType renderEnv go `shouldBe` "any"

        it "flat policy propagates into containers (Maybe a → rt.SkyMaybe[any])" $ do
            let maybeA = T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"]
            let go = GoType.mapSkyTypeToGo GoType.flatMappingContext maybeA
            GoType.renderGoType renderEnv go `shouldBe` "rt.SkyMaybe[any]"

        it "flat policy is structural over TLambda (a -> b → func(any) any)" $ do
            let lam = T.TLambda (T.TVar "a") (T.TVar "b")
            let go = GoType.mapSkyTypeToGo GoType.flatMappingContext lam
            GoType.renderGoType renderEnv go `shouldBe` "func(any) any"

        it "flat policy on env-free concrete types is byte-identical to default" $ do
            -- Concrete primitives don't contain TVars, so the two
            -- policies agree.  This is the migration-safety lemma —
            -- swapping a call site from default to flat is byte-
            -- identical for any concrete-input call.
            let intTy    = T.TType ModuleName.basics "Int"    []
                stringTy = T.TType ModuleName.basics "String" []
                boolTy   = T.TType ModuleName.basics "Bool"   []
                concreteCorpus =
                    [ T.TUnit
                    , intTy
                    , stringTy
                    , T.TLambda intTy boolTy
                    ]
            mapM_ (\ty ->
                let defaultGo = GoType.mapSkyTypeToGo GoType.defaultMappingContext ty
                    flatGo    = GoType.mapSkyTypeToGo GoType.flatMappingContext   ty
                in GoType.renderGoType renderEnv flatGo
                    `shouldBe` GoType.renderGoType renderEnv defaultGo)
                concreteCorpus
