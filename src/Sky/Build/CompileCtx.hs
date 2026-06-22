{-# LANGUAGE BangPatterns #-}
-- |
-- v0.17 close P2 — pure 'CompileCtx' record + accessors.
--
-- This module is the value-channel scaffold that downstream phases
-- (P3+P4) consume to replace the surviving FFI + same-module-annot
-- IORefs in 'Sky.Type.Constrain.Expression', 'Sky.Canonicalise.Module',
-- 'Sky.Canonicalise.Environment', 'Sky.Type.Unify', and the
-- 'Sky.Build.Compile' codegen hot paths.
--
-- Purely additive — no callers yet.  Build is clean if the import
-- closes; behaviour is unchanged.  Once the readers migrate (one PR
-- per source file), the @LoadedFfiTables@ shim writes alongside
-- 'CompileCtx' can be deleted and the underlying 'IORef's can come
-- out (P1's full closure).
--
-- Why not extend an existing module?
--
--   * 'Sky.Build.LowerCtx' is the codegen-stage context (per-region
--     types, scoped enclosing TypeParams, …).  It carries the
--     /post-solve/ shape used by the lowerer.  Adding the
--     pre-canonicalisation FFI registry would smear two different
--     compile-stage abstractions across one type.
--   * 'Sky.Build.Compile' is the orchestration site.  Defining the
--     ctx record there would force every reader (in
--     'Sky.Canonicalise.*' / 'Sky.Type.*') to import 'Compile',
--     bringing the entire compiler closure into modules that today
--     have a tight, well-defined dependency set.
--
-- Living in 'Sky.Build.CompileCtx' keeps the new scaffold small,
-- import-clean, and reusable.  It only depends on 'Data.Map' /
-- 'Data.Set' / 'Sky.AST.Canonical' (for the @Annotation@ stored in
-- the kernel-types map) — the same surface 'LoadedFfiTables'
-- already exposes today.

module Sky.Build.CompileCtx
    ( CompileCtx(..)
    , emptyCtx
    -- Accessors (additive — no IORef, no @Maybe-with-default-empty@).
    , ctxKernelModules
    , ctxKernelFunctions
    , ctxKernelArity
    , ctxKernelTypes
    , ctxImplements
    , ctxPkgAlias
    , ctxTypedWrapperNames
    , ctxTypedWrapperParams
    -- v0.17 close criterion 3 — globalCgEnv migration (S0).
    , ctxCgEnv
    , withCgEnv
    ) where

import qualified Data.Map.Strict as Map
import qualified Data.Set        as Set

import qualified Sky.AST.Canonical    as Can
import qualified Sky.Generate.Go.Record as Rec
import qualified Sky.Type.Solve       as Solve


-- | v0.17 close P2 — the pure compile-time context bundle.  Every
-- field mirrors a 'IORef' that 'Sky.Build.Compile.loadAndSeedFfiRegistry'
-- currently writes.  Future phases thread @CompileCtx@ through the
-- entry points listed in @docs/v0.17-roadmap/architectural-close-plan.json@
-- and replace the @readIORef@ at each reader.
--
-- Strict in every field so a partial bundle can't sneak past via
-- thunks.  The intent is for callers to pattern-bind once at the
-- entry point and re-read fields cheaply (Map / Set are already
-- shared structurally).
data CompileCtx = CompileCtx
    { _ctx_kernelModules      :: !(Map.Map String String)
        -- ^ Sky import path → kernel-module name.  Mirrors
        -- 'Sky.Canonicalise.Environment.ffiKernelModulesRef'.
    , _ctx_kernelFunctions    :: !(Map.Map String [String])
        -- ^ Kernel name → exposed function names.  Mirrors
        -- 'Sky.Canonicalise.Environment.ffiKernelFunctionsRef'.
    , _ctx_kernelArity        :: !(Map.Map (String, String) Int)
        -- ^ @(kernelName, funcName) → arity@.  Mirrors
        -- 'Sky.Canonicalise.Environment.ffiKernelArityRef'.
    , _ctx_kernelTypes        :: !(Map.Map (String, String) Can.Annotation)
        -- ^ @(kernelName, funcName) → Sky annotation@.  Mirrors
        -- 'Sky.Canonicalise.Environment.ffiKernelTypeRef'.
    , _ctx_implements         :: !(Map.Map String [String])
        -- ^ Qualified-type → satisfied interfaces.  Mirrors
        -- 'Sky.Canonicalise.Environment.ffiImplementsRef' AND
        -- 'Sky.Type.Unify.ffiImplementsRef' (one source of truth at
        -- the ctx layer, vs the two-IORef duplication today).
    , _ctx_pkgAlias           :: !(Map.Map String String)
        -- ^ Go import path → canonical alias.  Mirrors
        -- 'Sky.Canonicalise.Environment.ffiPkgAliasRef'.
    , _ctx_typedWrapperNames  :: !(Set.Set String)
        -- ^ Typed FFI wrapper names (@Go_X_yT@).  Mirrors
        -- 'Sky.Canonicalise.Environment.ffiTypedWrapperNamesRef'.
    , _ctx_typedWrapperParams :: !(Map.Map String [String])
        -- ^ Typed wrapper name → param Go types.  Mirrors
        -- 'Sky.Canonicalise.Environment.ffiTypedWrapperParamsRef'.
    , _ctx_cgEnv              :: !Rec.CodegenEnv
        -- ^ v0.17 close criterion 3 — staging S0.  Mirrors
        -- 'Sky.Build.Compile.globalCgEnv' (an 'IORef'
        -- 'Rec.CodegenEnv' that today holds the per-compile codegen
        -- environment populated by 'seedEarlyCgEnv' / 'solvePhase'
        -- C9 / per-dep sig merge / C10 final rebuild — see
        -- 'docs/v0.17-roadmap/globalCgEnv-close-plan.md' for the
        -- full pre-implementation grill + 6-stage close path).
        --
        -- This field is the pure value-channel target.  Readers
        -- migrate to consult 'ctx._ctx_cgEnv' instead of the
        -- 'getCgEnv' CAF; writers migrate to thread an updated
        -- 'CompileCtx' instead of 'modifyIORef'.  S5 deletes the
        -- 'globalCgEnv' top-level IORef + 'getCgEnv' CAF.
        --
        -- INVARIANT (preserved from the legacy
        -- 'globalCgEnv' contract): readers MUST consult the
        -- POST-C10 value.  Reading before the per-dep sig merge or
        -- before the C10 rebuild observes a partial 'CodegenEnv'
        -- and re-triggers the iter-20 'Anon_R_*' undefined failure
        -- class.  S2-S4 wiring threads through 'LowerCtx' which is
        -- only available POST-C10.
    }


-- | An empty 'CompileCtx'.  Used at test entry points and at
-- canonicaliser fixture sites where no FFI has been loaded; matches
-- the empty-IORef behaviour the current code paths already exhibit
-- when @loadAndSeedFfiRegistry@ has not been called (LSP, isolated
-- spec runs, in-process compile harnesses).
emptyCtx :: CompileCtx
emptyCtx = CompileCtx
    { _ctx_kernelModules      = Map.empty
    , _ctx_kernelFunctions    = Map.empty
    , _ctx_kernelArity        = Map.empty
    , _ctx_kernelTypes        = Map.empty
    , _ctx_implements         = Map.empty
    , _ctx_pkgAlias           = Map.empty
    , _ctx_typedWrapperNames  = Set.empty
    , _ctx_typedWrapperParams = Map.empty
    , _ctx_cgEnv              = emptyCgEnv
    }


-- | Initial 'Rec.CodegenEnv' matching the value the legacy
-- 'globalCgEnv' IORef is seeded with at 'Sky.Build.Compile':149.
-- Lifted here so 'emptyCtx' stays import-clean and the field
-- initialiser is shared between the pure ctx + the legacy IORef
-- shim during the S0-S5 migration.
emptyCgEnv :: Rec.CodegenEnv
emptyCgEnv = Rec.CodegenEnv
    Solve.emptySolvedTypes
    Map.empty   -- _cg_aliases
    Map.empty   -- _cg_fieldIndex
    Set.empty   -- _cg_zeroArgs
    Set.empty   -- _cg_recordAliases
    Set.empty   -- _cg_unionNames
    Map.empty   -- _cg_unionDetails  (v0.17 P3.4c.0)
    Set.empty   -- _cg_enumNames
    Map.empty   -- _cg_funcArities
    Map.empty   -- _cg_funcParamTypes
    Map.empty   -- _cg_funcRetType
    Map.empty   -- _cg_funcUltimateRetType
    Map.empty   -- _cg_funcInferredSigs
    Map.empty   -- _cg_callSiteInstances
    Map.empty   -- _cg_funcSkyToGoTVars


-- | Field accessor (additive).  Equivalent to '_ctx_kernelModules'
-- but exported under the @ctx<Field>@ naming convention the future
-- consumers will adopt.  Once readers migrate, exporting only the
-- accessors (not the record selectors) lets us evolve the record
-- shape without source-breaking the call sites.
ctxKernelModules :: CompileCtx -> Map.Map String String
ctxKernelModules = _ctx_kernelModules


-- | See 'ctxKernelModules'.
ctxKernelFunctions :: CompileCtx -> Map.Map String [String]
ctxKernelFunctions = _ctx_kernelFunctions


-- | See 'ctxKernelModules'.
ctxKernelArity :: CompileCtx -> Map.Map (String, String) Int
ctxKernelArity = _ctx_kernelArity


-- | See 'ctxKernelModules'.
ctxKernelTypes :: CompileCtx -> Map.Map (String, String) Can.Annotation
ctxKernelTypes = _ctx_kernelTypes


-- | See 'ctxKernelModules'.
ctxImplements :: CompileCtx -> Map.Map String [String]
ctxImplements = _ctx_implements


-- | See 'ctxKernelModules'.
ctxPkgAlias :: CompileCtx -> Map.Map String String
ctxPkgAlias = _ctx_pkgAlias


-- | See 'ctxKernelModules'.
ctxTypedWrapperNames :: CompileCtx -> Set.Set String
ctxTypedWrapperNames = _ctx_typedWrapperNames


-- | See 'ctxKernelModules'.
ctxTypedWrapperParams :: CompileCtx -> Map.Map String [String]
ctxTypedWrapperParams = _ctx_typedWrapperParams


-- | v0.17 close criterion 3 — globalCgEnv migration (S0).
-- Accessor for the staged 'Rec.CodegenEnv' field.  Future reader
-- migration (S4) replaces 'Sky.Build.Compile.getCgEnv' CAF reads
-- with 'ctxCgEnv ctx' (where 'ctx' comes from the threaded
-- 'CompileCtx' or 'LowerCtx').  See
-- 'docs/v0.17-roadmap/globalCgEnv-close-plan.md'.
ctxCgEnv :: CompileCtx -> Rec.CodegenEnv
ctxCgEnv = _ctx_cgEnv


-- | v0.17 close criterion 3 — globalCgEnv migration (S0).  Setter
-- for the staged 'Rec.CodegenEnv' field.  Future writer migration
-- (S2-S3) replaces 'Sky.Build.Compile.modifyIORef globalCgEnv' /
-- 'writeIORef globalCgEnv' sites with
-- 'updatedCtx = withCgEnv newCgEnv ctx' threaded through
-- 'LowerCtx'.  See
-- 'docs/v0.17-roadmap/globalCgEnv-close-plan.md'.
withCgEnv :: Rec.CodegenEnv -> CompileCtx -> CompileCtx
withCgEnv newEnv ctx = ctx { _ctx_cgEnv = newEnv }
