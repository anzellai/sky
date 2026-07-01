-- | v0.17 PR-5 — solver-emits-typed-go bridge.
--
-- The foundation refactor's load-bearing module. The solver computes
-- per-region T.Type as today; 'buildGoTypeRegions' walks that snapshot
-- once and produces a Map A.Region SolvedRegion where each entry
-- carries BOTH the canonical T.Type AND a structural GoType.
--
-- After PR-5 lands every downstream consumer that today re-runs a
-- String-based renderer ambient (the seven legacy renderers in
-- Compile.hs that PR-3's master plan enumerates) can be migrated
-- to a single lookup against this map. PRs 11-16 (Phase γ) do the
-- migration; PR-5 ships the producer alongside the parity test that
-- gates the contract.
--
-- The architectural property: when 'buildGoTypeRegions' has run,
-- the solver IS the single authority for both the Sky type and the
-- Go type at every region. No downstream renderer needs an
-- ambient CodegenEnv read; downstream needs the SolvedRegion only.
--
-- Lives in @Sky.Type.Solve.GoTypeBuild@ rather than @Sky.Type.Solve@
-- to avoid pulling 'Sky.Generate.Go.Type' into the solver's import
-- list — that would create a cycle through @Sky.Generate.Go.Record@.
-- This module is the bridge.
module Sky.Type.Solve.GoTypeBuild
    ( SolvedRegion(..)
    , buildGoTypeRegions
    , lookupSolvedGoType
    , lookupSolvedRegion
    ) where

import qualified Data.Map.Strict as Map
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Type.Type as T
import qualified Sky.Type.Solve as Solve
import qualified Sky.Generate.Go.Type as GoType
import qualified Sky.Generate.Go.Record as Rec


-- | One region's complete typed snapshot.
--
-- @srSkyType@ is exactly what the solver wrote (i.e. what's in
-- 'Solve.RegionTypes' today). @srGoType@ is the structural Go
-- representation 'GoType.mapSkyTypeToGo' produces under the active
-- 'GoType.MappingContext'.
--
-- Pre-foundation, downstream renderers re-derive @srGoType@ at
-- every emit site by re-running String-based mappers. PR-5 ships
-- the canonical producer; PRs 11-16 migrate the consumers.
data SolvedRegion = SolvedRegion
    { srSkyType :: !T.Type
    , srGoType  :: !GoType.GoType
    }
    deriving (Show)


-- | Build the typed-region map from a 'Solve.SolvedTypes' snapshot
-- + the codegen environment + the render policy.
--
-- The map walks @Solve._stRegions@ (the existing per-region T.Type
-- ledger) and produces one 'SolvedRegion' per entry by running
-- 'GoType.mapSkyTypeToGo' under the context derived from @cgEnv@
-- and @renderEnv@.
--
-- O(N) over the region count; one 'GoType.mapSkyTypeToGo' call per
-- region. The 'GoType.MappingContext' is computed once and reused.
--
-- This is the bridge function PRs 11+ consume. Today no caller
-- reaches into the result; the function exists so the parity test
-- can verify the structural-vs-legacy mapping agrees BEFORE any
-- consumer migrates.
buildGoTypeRegions
    :: GoType.RenderEnv
    -> Rec.CodegenEnv
    -> Solve.SolvedTypes
    -> Map.Map A.Region SolvedRegion
buildGoTypeRegions renderEnv cgEnv solved =
    Map.map mkSolvedRegion (Solve._stRegions solved)
  where
    mctx = GoType.buildMappingContext renderEnv cgEnv
    mkSolvedRegion skyTy = SolvedRegion
        { srSkyType = skyTy
        , srGoType  = GoType.mapSkyTypeToGo mctx skyTy
        }


-- | Look up just the 'GoType.GoType' for a region.
lookupSolvedGoType
    :: A.Region
    -> Map.Map A.Region SolvedRegion
    -> Maybe GoType.GoType
lookupSolvedGoType r m = fmap srGoType (Map.lookup r m)


-- | Look up the full 'SolvedRegion'.
lookupSolvedRegion
    :: A.Region
    -> Map.Map A.Region SolvedRegion
    -> Maybe SolvedRegion
lookupSolvedRegion = Map.lookup
