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
    -- v0.17 Phase A iter 3 — emit-phase CompileCtx record (NEW, distinct from
    -- the iter-S0 cgEnv-bridge CompileCtx above; uses @_cc_*@ field names).
    , EmitCompileCtx(..)
    , emptyEmitCompileCtx
    , buildEmitCompileCtx
    , EmitM
    , runEmitM
    , askEmitCtx
    , lookupCgEnvFromCtx
    , lookupSolvedTypesFromCtx
    , lookupKernelAliasFromCtx
    , lookupUnionDetailsFromCtx
    , lookupAnonRecordsFromCtx
    , lookupModuleFromCtx
    , lookupAliasesFromCtx
    , lookupFieldIdxFromCtx
    , lookupUnionNamesFromCtx
    , lookupFfiTypedWrappersFromCtx
    , lookupFfiTypedWrapperParamsFromCtx
    , withCurrentModuleInCtx
    ) where

import qualified Data.Map.Strict as Map
import qualified Data.Set        as Set

import Control.Monad.Reader (ReaderT, runReaderT, ask)

import qualified Sky.AST.Canonical    as Can
import qualified Sky.Generate.Go.Record as Rec
import qualified Sky.Sky.ModuleName   as ModuleName
import qualified Sky.Type.Solve       as Solve
import qualified Sky.Type.Type        as T


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
        -- ^ Typed FFI wrapper names (@Go_X_yT@).  v0.17 close iter 5
        -- (Phase 7 IORef defusing) — single source of truth; the
        -- legacy @Env.ffiTypedWrapperNamesRef@ has been deleted.
    , _ctx_typedWrapperParams :: !(Map.Map String [String])
        -- ^ Typed wrapper name → param Go types.  v0.17 close iter 5
        -- — single source of truth; the legacy
        -- @Env.ffiTypedWrapperParamsRef@ has been deleted.
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
    Set.empty   -- _cg_sealedIfaceNames (v0.17 iter 60)


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


-- =====================================================================
-- v0.17 Phase A iter 3 — emit-phase CompileCtx scaffold
-- ---------------------------------------------------------------------
-- The record + helpers below are the iter-3 deliverable per
-- @docs/v0.17-roadmap/phase-A-cgenv-reshape.md@ §"Iter 3 — Introduce
-- CompileCtx record + ReaderT scaffold".  THIS iter is purely
-- additive — the record type ships, a thin ReaderT alias ('EmitM')
-- exists, lookup helpers exist, but NO existing reader site has been
-- migrated yet.  Iters 4+ thread the ctx through entry/dep emission;
-- iter 6-8 swap reader sites; iter 9-10 delete the IORef channel.
--
-- Naming convention: the new record uses @_cc_*@ field prefixes
-- (Compile-Ctx).  This is intentional — the existing 'CompileCtx'
-- above (the iter-S0 cgEnv-bridge record with @_ctx_*@ fields)
-- mirrors the FFI/registry IORefs and is consumed by canonicaliser
-- + solver code BEFORE emit.  The new 'EmitCompileCtx' record is
-- consumed by emit AFTER solve — distinct lifecycle, distinct
-- consumers, distinct prefix.  Keeping both in one module avoids a
-- new file + cabal entry while making the lifecycle separation
-- visible at every read site.
-- =====================================================================


-- | v0.17 Phase A iter 3 — emit-phase compile context.
--
-- Holds every value the post-solve emit pass needs to consume.  The
-- record is constructed ONCE in 'continueCompile' after 'solvePhase'
-- returns its 'SolveOutputs' bundle, at the boundary BEFORE
-- 'emitPhase' fires.  Iters 4-5 thread this ctx through emission
-- helpers via a ReaderT; iters 6-8 migrate reader sites from
-- 'getCgEnvFromScope' / 'scopeStateRef' reads to @asks (..._cc_*)@
-- reads.  Iter 9 deletes the CAF, iter 10 deletes the IORef.
--
-- Strict in every field — a partial bundle can't sneak past via
-- thunks.  Note that the @_cc_anonRecords@ field is a SNAPSHOT, not
-- a live IORef proxy: the iter-11 anon-records reshape (locked
-- Option (c)) keeps the underlying IORef as the writer channel; the
-- snapshot here lets emit-time readers consult the at-build-entry
-- value when needed.  See
-- @docs/v0.17-roadmap/phase-A-iter-0-anonrecords-contract.md@.
data EmitCompileCtx = EmitCompileCtx
    { _cc_cgEnv :: !Rec.CodegenEnv
        -- ^ Post-C10 codegen env — the deletion target of criterion #3.
        -- Constructed AT emit boundary (after every writer in
        -- 'continueCompile' has settled).  Iter 4-5 readers read this
        -- field via @asks _cc_cgEnv@ instead of 'getCgEnvFromScope'.
    , _cc_solvedTypes :: !Solve.SolvedTypes
        -- ^ Merged entry + dep solved-types map — what
        -- 'generateGoMulti' / 'lowerCtx' consume for HM type lookups.
        -- Mirrors 'LowerCtx._lc_solved' AFTER the @typesWithDeps@
        -- conflict-detection merge fires.  Iter 6+ readers in
        -- emit-time helpers consult this via @asks _cc_solvedTypes@.
    , _cc_kernelAlias :: !(Map.Map (ModuleName.Canonical, String) (String, String))
        -- ^ Kernel-alias registry — Sky-source @(home, name)@ pair to
        -- @(kernelMod, kernelName)@ for @Ffi.kernel "K_n"@ aliases.
        -- Mirrors 'SolveOutputs._so_kernelAliasMap' threaded into emit.
        -- Iter 6+ replaces the @scopeStateRef._lc_kernelAlias@ reads
        -- in 'exprToGo' / 'rewriteAliasHead' with @asks
        -- _cc_kernelAlias@.
    , _cc_unionDetails :: !(Map.Map String (ModuleName.Canonical, Can.CtorOpts, [String], [Can.Ctor]))
        -- ^ Per-union metadata for the sealed-iface emission gate.
        -- Mirror of @Rec._cg_unionDetails@ AND
        -- @LowerCtx._lc_unionDetails@.  Iter 6+ consolidates the two
        -- channels into a single @asks _cc_unionDetails@ read.
    , _cc_anonRecords :: !(Map.Map String (Map.Map String T.FieldType))
        -- ^ Snapshot of @globalAnonRecords@ at emit entry.  Iter 11
        -- (locked Option (c) per the contract doc) keeps the IORef as
        -- writer channel; readers that need build-entry state can
        -- consult this snapshot.  Live readers consume the IORef
        -- directly per the contract.  This field exists so future
        -- iters can DELETE the IORef channel WITHOUT a new field; the
        -- migration path is "promote snapshot → live", not "add new
        -- field".
    , _cc_module :: !ModuleName.Canonical
        -- ^ The module currently being emitted (entry-module).  Iter
        -- 5 dep emission wraps this via @ReaderT.local (\\ctx -> ctx
        -- { _cc_module = depMod })@ — same trick the existing
        -- @LowerCtx._lc_module@ uses.
    , _cc_aliases :: !(Map.Map String Can.Alias)
        -- ^ Entry + dep merged alias map.  Mirrors
        -- 'LowerCtx._lc_aliases'.  Iter 6+ readers in emit-time
        -- helpers consult this via @asks _cc_aliases@ instead of
        -- @LC._lc_aliases (readIORefNoCse scopeStateRef)@.
    , _cc_fieldIdx :: !Rec.RecordRegistry
        -- ^ Field-set → alias-name registry.  Mirrors
        -- 'LowerCtx._lc_fieldIdx'.  Same iter-6+ migration shape as
        -- 'aliases'.
    , _cc_unionNames :: !(Set.Set String)
        -- ^ Union-name set.  Mirrors 'LowerCtx._lc_unionNames'.  Same
        -- iter-6+ migration shape.
    , _cc_ffiTypedWrappers :: !(Set.Set String)
        -- ^ Typed-FFI wrapper Go-function names (@<Kernel>_<fn>T@).
        -- Mirrors 'LowerCtx._lc_ffiTypedWrapperNames'.  Iter 6+
        -- readers in 'exprToGo' (Can.VarKernel arms) +
        -- 'caseToGo' (isTypedFfiCall) consult this via @asks
        -- _cc_ffiTypedWrappers@.
    , _cc_ffiTypedWrapperParams :: !(Map.Map String [String])
        -- ^ Typed-FFI wrapper name → param Go types.  Companion to
        -- '_cc_ffiTypedWrappers'.  Mirrors
        -- 'LowerCtx._lc_ffiTypedWrapperParams'.  v0.17 Session 3c
        -- restored — the IORef→ctx migration moved names but missed
        -- this companion field, causing the A5 typed-FFI dispatch gate
        -- to always fail on Map lookup → bare-name emission →
        -- "undefined: rt.Go_<Pkg>_<method>" build failures.
    }


-- | Empty 'EmitCompileCtx' for tests + bootstrap.  Real emit goes
-- through 'buildEmitCompileCtx' which takes the post-solve bundle +
-- the C10 final cgEnv.
emptyEmitCompileCtx :: ModuleName.Canonical -> EmitCompileCtx
emptyEmitCompileCtx home = EmitCompileCtx
    { _cc_cgEnv             = emptyCgEnv
    , _cc_solvedTypes       = Solve.emptySolvedTypes
    , _cc_kernelAlias       = Map.empty
    , _cc_unionDetails      = Map.empty
    , _cc_anonRecords       = Map.empty
    , _cc_module            = home
    , _cc_aliases           = Map.empty
    , _cc_fieldIdx          = Map.empty
    , _cc_unionNames        = Set.empty
    , _cc_ffiTypedWrappers  = Set.empty
    , _cc_ffiTypedWrapperParams = Map.empty
    }


-- | Construct an 'EmitCompileCtx' from the values the emit phase
-- consumes.  Caller in 'continueCompile' destructures 'SolveOutputs'
-- + reads the C10-finalised cgEnv from 'scopeStateRef' (transitional
-- — iter 5 hoists C10 construction so the read disappears) and
-- passes the snapshots here.
--
-- Pure — decoupling the snapshot from the read site means tests can
-- build an 'EmitCompileCtx' directly without touching any global
-- state.  Same pattern as 'Sky.Build.LowerCtx.buildLowerCtx'.
buildEmitCompileCtx
    :: ModuleName.Canonical
    -> Rec.CodegenEnv
    -> Solve.SolvedTypes
    -> Map.Map (ModuleName.Canonical, String) (String, String)
    -> Map.Map String (ModuleName.Canonical, Can.CtorOpts, [String], [Can.Ctor])
    -> Map.Map String (Map.Map String T.FieldType)
    -> Map.Map String Can.Alias
    -> Rec.RecordRegistry
    -> Set.Set String
    -> Set.Set String
    -> Map.Map String [String]
    -> EmitCompileCtx
buildEmitCompileCtx home cgEnv solved kernelAlias unionDetails
                    anonRecs aliases fieldIdx unionNames ffiWrappers
                    ffiWrapperParams =
    EmitCompileCtx
        { _cc_cgEnv             = cgEnv
        , _cc_solvedTypes       = solved
        , _cc_kernelAlias       = kernelAlias
        , _cc_unionDetails      = unionDetails
        , _cc_anonRecords       = anonRecs
        , _cc_module            = home
        , _cc_aliases           = aliases
        , _cc_fieldIdx          = fieldIdx
        , _cc_unionNames        = unionNames
        , _cc_ffiTypedWrappers  = ffiWrappers
        , _cc_ffiTypedWrapperParams = ffiWrapperParams
        }


-- | v0.17 Phase A iter 3 — emit monad alias.  ReaderT over 'IO' with
-- 'EmitCompileCtx' as the environment.  THIS iter ships the alias +
-- the 'runEmitM' helper but does NOT migrate any existing function
-- to use it; existing IORef code continues to work unchanged.
--
-- Iter 4 will wrap entry-module emission in 'runEmitM'.  Iter 5
-- wraps dep emission.  Iters 6-8 migrate the reader sites.  See the
-- design doc § "Iter 4-8".
type EmitM = ReaderT EmitCompileCtx IO


-- | Run an 'EmitM' action against an explicit ctx.
runEmitM :: EmitCompileCtx -> EmitM a -> IO a
runEmitM ctx action = runReaderT action ctx


-- | Convenience — fetch the ctx inside an 'EmitM' computation.
-- Identical to mtl's 'ask' but re-exported here so call sites only
-- need to import 'Sky.Build.CompileCtx'.
askEmitCtx :: EmitM EmitCompileCtx
askEmitCtx = ask


-- | Read the codegen env from a threaded 'EmitCompileCtx'.  Pure
-- substitute for @unsafePerformIO (readIORef scopeStateRef) >>=
-- return . LC._lc_cgEnv@ that the 'getCgEnvFromScope' CAF performs.
-- Iter 6+ reader migration calls this at every site that previously
-- consulted the CAF.
lookupCgEnvFromCtx :: EmitCompileCtx -> Rec.CodegenEnv
lookupCgEnvFromCtx = _cc_cgEnv


-- | Read the merged solved-types map.  Same migration shape as
-- 'lookupCgEnvFromCtx'.
lookupSolvedTypesFromCtx :: EmitCompileCtx -> Solve.SolvedTypes
lookupSolvedTypesFromCtx = _cc_solvedTypes


-- | Read the kernel-alias registry.
lookupKernelAliasFromCtx :: EmitCompileCtx -> Map.Map (ModuleName.Canonical, String) (String, String)
lookupKernelAliasFromCtx = _cc_kernelAlias


-- | Read the per-union metadata map.
lookupUnionDetailsFromCtx :: EmitCompileCtx -> Map.Map String (ModuleName.Canonical, Can.CtorOpts, [String], [Can.Ctor])
lookupUnionDetailsFromCtx = _cc_unionDetails


-- | Read the anon-records snapshot.  Note: live readers still
-- consult the IORef per the iter-11 Option (c) contract; this
-- helper is for emit-time consumers that want the build-entry
-- state.
lookupAnonRecordsFromCtx :: EmitCompileCtx -> Map.Map String (Map.Map String T.FieldType)
lookupAnonRecordsFromCtx = _cc_anonRecords


-- | Read the currently-emitting module name.
lookupModuleFromCtx :: EmitCompileCtx -> ModuleName.Canonical
lookupModuleFromCtx = _cc_module


-- | Read the merged record-alias map.
lookupAliasesFromCtx :: EmitCompileCtx -> Map.Map String Can.Alias
lookupAliasesFromCtx = _cc_aliases


-- | Read the field-set → alias-name registry.
lookupFieldIdxFromCtx :: EmitCompileCtx -> Rec.RecordRegistry
lookupFieldIdxFromCtx = _cc_fieldIdx


-- | Read the union-names set.
lookupUnionNamesFromCtx :: EmitCompileCtx -> Set.Set String
lookupUnionNamesFromCtx = _cc_unionNames


-- | Read the typed-FFI wrapper names set.
lookupFfiTypedWrappersFromCtx :: EmitCompileCtx -> Set.Set String
lookupFfiTypedWrappersFromCtx = _cc_ffiTypedWrappers


-- | Read the typed-FFI wrapper param Go types map.  v0.17 Session 3c
-- — companion to 'lookupFfiTypedWrappersFromCtx'.  Closes the A5 gate
-- failure where the names Set was populated but the params Map was
-- left empty by the IORef→ctx migration.
lookupFfiTypedWrapperParamsFromCtx :: EmitCompileCtx -> Map.Map String [String]
lookupFfiTypedWrapperParamsFromCtx = _cc_ffiTypedWrapperParams


-- | Install a scoped current module hint for the SolvedTypes in the context.
withCurrentModuleInCtx :: Maybe String -> EmitCompileCtx -> EmitCompileCtx
withCurrentModuleInCtx mModName ctx =
    ctx { _cc_solvedTypes = Solve.withCurrentModule mModName (_cc_solvedTypes ctx) }
