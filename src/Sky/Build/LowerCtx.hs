-- | Sky.Build.LowerCtx — explicit, immutable lowering context.
--
-- Background. The codegen pipeline today reads ~7 NOINLINE IORefs
-- (`globalLambdaTypes`, `globalLambdaGoStrings`, `globalRegionTypes`,
-- `globalAllAliases`, `globalAllFieldIdx`, `globalUnionNames`,
-- `globalAnnotMap`) during pure lowering.  That setup races with
-- lazy GoIR evaluation (the v0.15.3 editor panic) and makes scoping
-- accidental rather than structural.  The principled fix — named in
-- `docs/v1-rfc/type-soundness-deep-analysis.md` §5.1 and tracked by
-- `docs/improvement-plan-v0.16.md` Priority 1 — is to thread an
-- explicit `LowerCtx` reader value through every `exprToGo` /
-- `exprToGoExpectGo` / `coerceArg` / `letBindingType` call.
--
-- This module is the scaffolding step (PR 1 of 6).  It introduces:
--
--   * The `LowerCtx` record (10 fields, matching the plan).
--   * A `buildLowerCtx` constructor that snapshots the IORef state
--     once at codegen entry (`generateGoMulti`).
--   * Pure lookup helpers (`lookupLambdaType`, `lookupLambdaGoStr`,
--     `lookupRegionType`, …) that read from the snapshot.
--   * `withLambdaTypes` / `withLambdaGoStrs` helpers for nested
--     scopes — these return a NEW `LowerCtx` rather than mutating a
--     global, which is the whole point.
--
-- This PR does NOT migrate any existing call sites.  The IORef-
-- backed helpers in `Compile.hs` stay live; the new helpers
-- delegate to the same `Map.lookup` over the snapshot.  PRs 2-6
-- migrate the call sites one bottom-up batch at a time, until the
-- IORefs can be deleted.
--
-- See `docs/improvement-plan-v0.16.md` §2 for the staging.
module Sky.Build.LowerCtx
    ( LowerCtx (..)
    , emptyLowerCtx
    , buildLowerCtx
    , lookupLambdaType
    , lookupLambdaGoStr
    , lookupLambdaGoType
    , memberLambdaType
    , lookupAlias
    , lookupAnnotation
    , lookupSolved
    , lookupEnclosingTypeParam
    , withLambdaTypes
    , withLambdaGoStrs
    , withLambdaGoTypes
    , withEnclosingTypeParams
    , withCurrentDepModule
    , lookupCurrentDepModule
    , withKernelAlias
    , lookupKernelAlias
    , withAliases
    , withFieldIdx
    , withUnionNames
    , withUnionDetails
    , withFfiTypedWrapperNames
    , withFfiTypedWrapperParams
    , withCgEnv
    , lookupCgEnv
    , modifyCgEnv
    ) where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set

import qualified Sky.AST.Canonical as Can
import qualified Sky.Build.Dce as Dce
import qualified Sky.Build.Monomorphise as Mono
import qualified Sky.Generate.Go.Record as Rec
import qualified Sky.Generate.Go.Type as GoType
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Type.Solve as Solve
import qualified Sky.Type.Type as T


-- | Snapshot of every piece of state the lowerer needs.  Constructed
-- once at codegen entry (`generateGoMulti`), passed by value down
-- through the call tree.  Scope-nested updates (e.g. entering a
-- typed-lambda body) return a NEW `LowerCtx` via `withLambdaTypes`
-- — the parent ctx is unaffected, so the race-with-laziness class
-- of bug that `withScopedLambdaTypes` band-aided in v0.15.3 is
-- structurally impossible here.
data LowerCtx = LowerCtx
    { _lc_module      :: !ModuleName.Canonical
        -- ^ The module currently being lowered.  Used for
        -- module-prefix qualification of unqualified names.
    , _lc_solved      :: !Solve.SolvedTypes
        -- ^ HM-solved types for every top-level binding in the
        -- current module.  Snapshotted from the entry-point.
        -- v0.15.x P37b — `SolvedTypes` also carries the per-source-
        -- region HM type map in `_stRegions` (since P37a's solver
        -- surgery).  The previously-separate `_lc_regionTypes`
        -- field was deleted as part of P37b; consumers that need
        -- a region lookup call `Solve.lookupSolvedRegion r solved`
        -- against the SolvedTypes value flowing through their
        -- arguments, not against a snapshot installed via
        -- `scopeStateRef`.  This breaks the deferred-thunk cycle
        -- that blackholed the P6 record-field / list-element /
        -- let-body cascade migrations.
    , _lc_lambdaTypes :: !(Map.Map String T.Type)
        -- ^ Lambda-scope local-variable type bindings.  Replaces
        -- `globalLambdaTypes`.  Nested scopes update this field
        -- via `withLambdaTypes`.
    , _lc_lambdaGoStr :: !(Map.Map String String)
        -- ^ Lambda-scope Go-type strings for function-typed
        -- parameters in scope.  Replaces `globalLambdaGoStrings`.
        -- Updated via `withLambdaGoStrs`.
    , _lc_lambdaGoTypes :: !(Map.Map String GoType.GoType)
        -- ^ v0.17 PR-11 — structural Go-type registry for typed
        -- lambda params.  Populated at `lowerTypedLambda`'s writer
        -- via `parseGoType` directly (no lossy String→T.Type round
        -- trip via the now-deleted `inferTypeFromGoString`).  Readers
        -- that previously consumed the wildcard `TVar "_"` from the
        -- T.Type registry (sites 1, 3, 5 in `goExprGoType` /
        -- `operandIsStaticallyTyped` / `isMaybeOrResultIdent`) now
        -- consult this map directly and use `renderGoType` for the
        -- Go-string they want.  Sibling to `_lc_lambdaTypes` — both
        -- coexist until every consumer migrates off the T.Type
        -- channel.
    , _lc_aliases     :: !(Map.Map String Can.Alias)
        -- ^ Entry + dep merged alias map.  Snapshotted from
        -- `globalAllAliases`.  Read by parametric-alias generic-args
        -- renderer (`aliasGenericArgs`).
    , _lc_fieldIdx    :: !Rec.RecordRegistry
        -- ^ Field-set → alias-name registry.  Snapshotted from
        -- `globalAllFieldIdx`.  Read by `tvarsInEmitted` and
        -- friends to resolve `TRecord` nodes to `_R` Go struct
        -- names without forcing the env build.
    , _lc_unionNames  :: !(Set.Set String)
        -- ^ Union-name set.  Snapshotted from `globalUnionNames`.
        -- Read by `typeStrWithAliasesReg` while emitting dep-function
        -- sigs to discriminate union-typed args.
    , _lc_unionDetails :: !(Map.Map String (ModuleName.Canonical, Can.CtorOpts, [String], [Can.Ctor]))
        -- ^ v0.17 P3.4c.0 — per-union metadata for the sealed-iface
        -- emission gate.  Mirror of 'Rec._cg_unionDetails'; threaded
        -- so the @scopeStateRef@ cascade path reads the same map the
        -- 'Rec.CodegenEnv' carries.  Keys: same convention as
        -- '_lc_unionNames' (entry-keyed by bare type name; dep-keyed
        -- by prefixed name).  Value carries the originating
        -- 'ModuleName.Canonical' so consumers can rebuild the
        -- qualified Go name without parsing it back from the key.
        -- Read by 'subjectIsSealedIface' (P3.4c.1; NOT WIRED yet).
    , _lc_aliasMap    :: !(Map.Map String String)
        -- ^ Reserved for a future module-prefix → unprefixed alias
        -- shortcut map.  Empty today; populated in PR 2 when the
        -- alias-lookup migration lands.  Kept in the record so PRs
        -- 2-6 don't need to grow the type again.
    , _lc_annotMap    :: !(Map.Map String T.Annotation)
        -- ^ Per-callee generalised annotation map.  Snapshotted
        -- from `globalAnnotMap`.  Read by σ-derivation at every
        -- reachable instance emission.
    , _lc_enclosingTypeParams :: !(Set.Set String)
        -- ^ Generic type-parameter names declared on the
        -- currently-emitting Go function (e.g. `T1`, `T2` from
        -- `func Widget_view[T2 any](...)`).  Populated by
        -- `withEnclosingTypeParams` at dep + entry emission, BEFORE
        -- the body's `GoExpr` tree is constructed.  Read by the
        -- arg-coercion `eraseTypeParams` guard in `Compile.hs` to
        -- pin in-scope TVars instead of widening them to `any`.
        -- Closes Issue #521 and forecloses the entire
        -- `Cfg_R[any]`-cast-panic class for parametric record
        -- aliases (#261/#262/#263/#461/#463/#465/#467).
    , _lc_currentDepModule :: !(Maybe String)
        -- ^ v0.17 PR-α — dep-vs-entry emission mode hint.  Set to
        -- `Just modName` while @generateDeclsForDepScoped@'s dep is
        -- being rendered; `Nothing` while the entry module is
        -- being rendered.  Replaces the `globalCurrentDepModule`
        -- IORef whose lazy-CAF caching pathology required the
        -- sentinel-bracket pattern at @Compile.hs:3371-3376@.
        -- See @docs/v0.17-pr-alpha-renderer-state-threading-design.md@
        -- for the full migration plan.  Migration sites (read at
        -- @Compile.hs:7730@; write at @generateDeclsForDepScoped@)
        -- thread through PR-α subsequent sessions.  Today: scaffolding
        -- only — `Nothing` matches the IORef's default; no behavior
        -- change.
    , _lc_reachableSet :: !Mono.ReachableSet
        -- ^ v0.17 PR-α S2 — whole-program reachable-instance set
        -- (snapshot of @globalReachableSet@).  Populated at
        -- 'buildLowerCtx' after the solver runs.  Read by
        -- 'instanceMangledName' via the @scopeStateRef@ channel
        -- (which carries the entire @LowerCtx@), replacing the
        -- direct IORef read.  Written ONCE per compile; never
        -- mutates after.
    , _lc_reachableProgram :: !(Set.Set Dce.Ref)
        -- ^ v0.17 PR-α S2 — whole-program DCE reachable-ref set
        -- (snapshot of @globalReachableProgram@).  Populated at
        -- 'buildLowerCtx'.  Threaded explicitly to
        -- 'generateDeclsForDep' (which previously read the IORef)
        -- + read inline from the LC value at sites that already
        -- accept a 'LowerCtx'.  Same write-once-at-solver-done
        -- semantics as '_lc_reachableSet'.
    , _lc_kernelAlias :: !(Map.Map (ModuleName.Canonical, String) (String, String))
        -- ^ v0.17 iter 17 (task #654) — kernel-alias registry
        -- (snapshot of @globalKernelAlias@).  Maps a Sky-source
        -- (home, name) pair to the matching (kernelMod, kernelName)
        -- so codegen rewrites @Can.VarTopLevel home name@ to
        -- @Can.VarKernel kMod kFn@ when the source binding is a
        -- @Ffi.kernel "K_n"@ alias.  Written once after solvePhase
        -- (via @LC.withKernelAlias@ on 'scopeStateRef') so the
        -- transitional @ctxFromIORef ()@ bridges see it on the
        -- next read.  Replaces the @lookupKernelAlias@ IORef hop
        -- inside @exprToGo@'s @Can.VarTopLevel@ + @Can.Call@ arms.
    , _lc_ffiTypedWrapperNames :: !(Set.Set String)
        -- ^ Set of typed-FFI wrapper Go-function names emitted by
        -- @FfiGen@ (each named @<Kernel>_<fn>T@).  Read at @exprToGo@'s
        -- @Can.VarKernel@ arms (zero-arg + N-arg FFI dispatch) +
        -- @caseToGo@'s @isTypedFfiCall@ recogniser to decide whether
        -- the typed wrapper exists for a given call site.  Populated
        -- by @loadAndSeedFfiRegistry@ → @LoadedFfiTables@ →
        -- 'generateGoMulti' at codegen entry; default 'Set.empty' for
        -- bootstrap.  v0.17 close iter 5 (Phase 7 IORef defusing):
        -- single source of truth — the legacy
        -- @Env.ffiTypedWrapperNamesRef@ IORef has been deleted.
    , _lc_ffiTypedWrapperParams :: !(Map.Map String [String])
        -- ^ Mapping each @<Kernel>_<fn>T@ wrapper to its declared Go
        -- param-type strings.  Read at @exprToGo@'s @Can.VarKernel@
        -- N-arg arm to coerce arguments to the wrapper's declared
        -- param types.  Same population path as
        -- '_lc_ffiTypedWrapperNames'; default 'Map.empty'.  v0.17 close
        -- iter 5: single source of truth — the legacy
        -- @Env.ffiTypedWrapperParamsRef@ IORef has been deleted.
    , _lc_cgEnv :: !(Maybe Rec.CodegenEnv)
        -- ^ v0.17 close criterion 3 — globalCgEnv migration
        -- (staged S1, iter 34).  Bridge field that future reader
        -- migration (S4) consults instead of the
        -- 'Sky.Build.Compile.getCgEnv' CAF.  'Nothing' means the
        -- LowerCtx was built before the C10 cgEnv finalisation;
        -- consumers fall through to the legacy CAF in that case
        -- (during S2-S3 transitional staging).  Populated by S3 at
        -- the post-@importsForced \`seq\`@ install site.
        --
        -- See @docs/v0.17-roadmap/globalCgEnv-close-plan.md@ §S1.
    }


-- | Empty `LowerCtx` for tests and bootstrap.  Real compilation
-- always goes through `buildLowerCtx`.
emptyLowerCtx :: ModuleName.Canonical -> LowerCtx
emptyLowerCtx home = LowerCtx
    { _lc_module      = home
    , _lc_solved      = Solve.emptySolvedTypes
    , _lc_lambdaTypes = Map.empty
    , _lc_lambdaGoStr = Map.empty
    , _lc_lambdaGoTypes = Map.empty
    , _lc_aliases     = Map.empty
    , _lc_fieldIdx    = Map.empty
    , _lc_unionNames  = Set.empty
    , _lc_unionDetails = Map.empty
    , _lc_aliasMap    = Map.empty
    , _lc_annotMap    = Map.empty
    , _lc_enclosingTypeParams = Set.empty
    , _lc_currentDepModule = Nothing
    , _lc_reachableSet = Set.empty
    , _lc_reachableProgram = Set.empty
    , _lc_kernelAlias = Map.empty
    , _lc_ffiTypedWrapperNames = Set.empty
    , _lc_ffiTypedWrapperParams = Map.empty
    , _lc_cgEnv      = Nothing
    }


-- | Construct a `LowerCtx` from the values the IORefs hold at
-- codegen entry.  Pure — callers (`generateGoMulti`) read the
-- IORefs once in IO and pass the snapshots here.  Decoupling the
-- snapshot from the read site means tests can build a `LowerCtx`
-- directly without touching any global state.
--
-- v0.15.x P37b — the per-region HM type map is no longer a
-- separate parameter: it lives on `Solve.SolvedTypes._stRegions`
-- (populated by P37a) and is read directly via
-- `Solve.lookupSolvedRegion`.  The `LC._lc_regionTypes` field +
-- its dedicated lookup helper were deleted together with the
-- corresponding `scopeStateRef` write in `Compile.hs`.
buildLowerCtx
    :: ModuleName.Canonical
    -> Solve.SolvedTypes
    -> Map.Map String Can.Alias
    -> Rec.RecordRegistry
    -> Set.Set String
    -> Map.Map String T.Annotation
    -> Mono.ReachableSet
    -> Set.Set Dce.Ref
    -> LowerCtx
buildLowerCtx home solved aliases fieldIdx unions annots reached reachedProg = LowerCtx
    { _lc_module      = home
    , _lc_solved      = solved
    , _lc_lambdaTypes = Map.empty
    , _lc_lambdaGoStr = Map.empty
    , _lc_lambdaGoTypes = Map.empty
    , _lc_aliases     = aliases
    , _lc_fieldIdx    = fieldIdx
    , _lc_unionNames  = unions
    , _lc_unionDetails = Map.empty
    , _lc_aliasMap    = Map.empty
    , _lc_annotMap    = annots
    , _lc_enclosingTypeParams = Set.empty
    , _lc_currentDepModule = Nothing
    , _lc_reachableSet = reached
    , _lc_reachableProgram = reachedProg
    , _lc_kernelAlias = Map.empty
    , _lc_ffiTypedWrapperNames = Set.empty
    , _lc_ffiTypedWrapperParams = Map.empty
    , _lc_cgEnv      = Nothing
    }


-- | Look up a variable in the current lambda-types scope.  Pure
-- substitute for `Compile.lookupLambdaType` (which reads
-- `globalLambdaTypes` via `unsafePerformIO`).
lookupLambdaType :: LowerCtx -> String -> Maybe T.Type
lookupLambdaType ctx k = Map.lookup k (_lc_lambdaTypes ctx)


-- | Look up a Go-type string for a function-typed variable in
-- the current scope.  Pure substitute for
-- `Compile.lookupLambdaGoStr`.
lookupLambdaGoStr :: LowerCtx -> String -> Maybe String
lookupLambdaGoStr ctx k = Map.lookup k (_lc_lambdaGoStr ctx)


-- | v0.17 PR-11 — structural Go-type lookup for typed lambda params.
-- Returns the 'GoType' registered at @lowerTypedLambda@'s writer
-- without any String → T.Type round trip (the deleted
-- @inferTypeFromGoString@'s job).  Consumers route through
-- 'renderGoType' when they need the legacy Go-string they previously
-- got via @solvedTypeToGo@.
lookupLambdaGoType :: LowerCtx -> String -> Maybe GoType.GoType
lookupLambdaGoType ctx k = Map.lookup k (_lc_lambdaGoTypes ctx)


-- | Membership-only sister of `lookupLambdaType`.  Pure substitute
-- for `Compile.memberLambdaType`.
memberLambdaType :: LowerCtx -> String -> Bool
memberLambdaType ctx k = Map.member k (_lc_lambdaTypes ctx)


-- v0.15.x P37b — `lookupRegionType` was deleted.  The region
-- map now lives on `Solve.SolvedTypes._stRegions` (populated by
-- P37a's solver surgery) and is read via
-- `Solve.lookupSolvedRegion`.  Consumers in `Compile.hs`
-- (`letBindingType`, `inferExprType`'s `Can.Lambda` arm) consume
-- the SolvedTypes value flowing through their arguments instead
-- of routing through a per-call IORef snapshot — pure data,
-- pure lookups, no scope-state seam.


-- | Look up an alias by name.  No module-prefix fallback yet — that
-- ships with the PR 2 migration of `Compile.lookupAliasDecl`.
lookupAlias :: LowerCtx -> String -> Maybe Can.Alias
lookupAlias ctx aliasName = Map.lookup aliasName (_lc_aliases ctx)


-- | Look up a callee's generalised annotation.  Pure substitute
-- for reads of `globalAnnotMap`.
lookupAnnotation :: LowerCtx -> String -> Maybe T.Annotation
lookupAnnotation ctx name = Map.lookup name (_lc_annotMap ctx)


-- | Look up a top-level binding's HM-solved type.  Pure substitute
-- for `Map.lookup name (_lc_solved ctx)`.  PR 3 (`inferExprType` /
-- `letBindingType` migration) will use this to retire the
-- `Solve.SolvedTypes` thread now passed alongside `LowerCtx` —
-- one source of truth for solved types.
lookupSolved :: LowerCtx -> String -> Maybe T.Type
lookupSolved ctx name = Solve.lookupSolvedVar name (_lc_solved ctx)


-- | Extend the lambda-types scope.  Returns a NEW ctx — the parent
-- ctx is unchanged, so nested scopes obey lexical structure
-- automatically.  Replaces `withScopedLambdaTypes`'s push/pop +
-- forced-rendering trick (the trick exists because the previous
-- design had no scoping; this design has scoping for free).
withLambdaTypes :: Map.Map String T.Type -> LowerCtx -> LowerCtx
withLambdaTypes additions ctx =
    ctx { _lc_lambdaTypes = Map.union additions (_lc_lambdaTypes ctx) }


-- | Extend the lambda-Go-string scope.  Mirror of `withLambdaTypes`
-- for the Go-type-string registry.
withLambdaGoStrs :: Map.Map String String -> LowerCtx -> LowerCtx
withLambdaGoStrs additions ctx =
    ctx { _lc_lambdaGoStr = Map.union additions (_lc_lambdaGoStr ctx) }


-- | v0.17 PR-11 — extend the structural Go-type lambda-scope.
-- Mirror of 'withLambdaTypes' for the new 'GoType'-typed registry.
-- Populated at @lowerTypedLambda@'s writer; consumed by
-- @goExprGoType@ / @isMaybeOrResultIdent@ /
-- @operandIsStaticallyTyped@.
withLambdaGoTypes :: Map.Map String GoType.GoType -> LowerCtx -> LowerCtx
withLambdaGoTypes additions ctx =
    ctx { _lc_lambdaGoTypes = Map.union additions (_lc_lambdaGoTypes ctx) }


-- | Membership test against the enclosing-Go-function's type-param
-- scope.  Used by the arg-coercion `eraseTypeParams` guard in
-- `Compile.hs` to pin in-scope TVars (`T2`) instead of erasing to
-- `any`.  Monomorphise's token-level substitution then rewrites the
-- preserved `T2` to the concrete instance type — the codegen-time
-- decision is no longer load-bearing.
lookupEnclosingTypeParam :: LowerCtx -> String -> Bool
lookupEnclosingTypeParam ctx k = Set.member k (_lc_enclosingTypeParams ctx)


-- | Extend the enclosing-type-param scope.  Returns a NEW ctx —
-- the parent ctx is unaffected, so nested generic functions obey
-- lexical structure automatically (child wins on shadowing via
-- `Set.union additions prev`).
withEnclosingTypeParams :: [String] -> LowerCtx -> LowerCtx
withEnclosingTypeParams additions ctx =
    ctx { _lc_enclosingTypeParams =
            Set.union (Set.fromList additions) (_lc_enclosingTypeParams ctx) }


-- | v0.17 PR-α — set the dep-mode hint.  Returns a NEW ctx so the
-- entry-mode parent stays untouched while dep emission threads its
-- own copy down.  @Nothing@ marks entry-mode (the @generateMainGo@
-- path); @Just modName@ marks dep-mode (the
-- @generateDeclsForDepScoped@ path).
withCurrentDepModule :: Maybe String -> LowerCtx -> LowerCtx
withCurrentDepModule modHint ctx =
    ctx { _lc_currentDepModule = modHint }


-- | v0.17 PR-α — read the dep-mode hint.  Pure substitute for
-- @readIORef globalCurrentDepModule@ at the consumer (renderer)
-- side once threading reaches the consumer.  Today: scaffolding
-- only — no consumer migrated to call this yet.
lookupCurrentDepModule :: LowerCtx -> Maybe String
lookupCurrentDepModule ctx = _lc_currentDepModule ctx


-- | v0.17 iter 17 (task #654) — replace 'globalKernelAlias' IORef
-- with a 'LowerCtx'-threaded map.  Caller (continueCompile) sets
-- this once on 'scopeStateRef' immediately after the solve-phase
-- destructures @kernelAliasMap@ from 'SolveOutputs'; subsequent
-- transitional 'ctxFromIORef ()' bridges in
-- 'Sky.Build.Compile' read the populated map without a separate
-- IORef hop.  Direct 'LowerCtx' callers (e.g. @exprToGo@) read
-- via 'lookupKernelAlias' below.
withKernelAlias
    :: Map.Map (ModuleName.Canonical, String) (String, String)
    -> LowerCtx
    -> LowerCtx
withKernelAlias aliases ctx =
    ctx { _lc_kernelAlias = aliases }


-- | v0.17 iter 17 (task #654) — pure kernel-alias lookup against
-- the threaded 'LowerCtx'.  Replaces the @lookupKernelAlias :: ...
-- -> Maybe (String, String)@ IORef-reading helper formerly at
-- 'Sky.Build.Compile.lookupKernelAlias'.  Returns @Nothing@ when
-- the (home, name) pair is not a Sky-source @Ffi.kernel "K_n"@
-- alias — codegen continues with the original Sky-source binding.
lookupKernelAlias
    :: LowerCtx
    -> ModuleName.Canonical
    -> String
    -> Maybe (String, String)
lookupKernelAlias ctx home name =
    Map.lookup (home, name) (_lc_kernelAlias ctx)


-- | v0.17 close criterion 3 — globalCgEnv migration (S2).  Setter
-- for the LowerCtx-threaded 'Rec.CodegenEnv'.  Writers
-- (resetCompileState / seedEarlyCgEnv / generateDeclsForDep C10 /
-- solvePhase C9 / generateGoMulti imports thunk / generateGo
-- entry C10) install via @modifyIORef scopeStateRef
-- (LC.withCgEnv newEnv)@ immediately after the corresponding
-- legacy @writeIORef globalCgEnv@ / @modifyIORef globalCgEnv@.
-- Shadow path during S2/S3 transitional; readers fall through to
-- the legacy 'getCgEnv' CAF until S4.
withCgEnv :: Rec.CodegenEnv -> LowerCtx -> LowerCtx
withCgEnv newEnv ctx = ctx { _lc_cgEnv = Just newEnv }


-- | v0.17 close criterion 3 — globalCgEnv migration (S2).  Pure
-- lookup of the threaded 'Rec.CodegenEnv'.  Returns 'Nothing' when
-- the LowerCtx pre-dates the S2 writer install (or in the
-- 'emptyLowerCtx' / 'buildLowerCtx' bootstrap shapes); S4 readers
-- fall through to the legacy 'getCgEnv' CAF on 'Nothing'.
lookupCgEnv :: LowerCtx -> Maybe Rec.CodegenEnv
lookupCgEnv = _lc_cgEnv


-- | v0.17 iter 44 S5 v3 — pure-functional cgEnv mutation. Reads the
-- current @_lc_cgEnv@ (must be installed by a prior writer), applies
-- f, and writes the updated env back. Use with @modifyIORef
-- scopeStateRef (LC.modifyCgEnv f)@ to replace the legacy
-- @modifyIORef globalCgEnv f@ pattern (the globalCgEnv IORef was
-- deleted at S5).
modifyCgEnv :: (Rec.CodegenEnv -> Rec.CodegenEnv) -> LowerCtx -> LowerCtx
modifyCgEnv f ctx = case _lc_cgEnv ctx of
    Just env -> ctx { _lc_cgEnv = Just (f env) }
    Nothing  -> error "BUG: LC.modifyCgEnv on ctx with uninstalled _lc_cgEnv"


-- | v0.17 iter 18 (task #654) — install the merged record-alias
-- map.  Replaces 'globalAllAliases' IORef write at codegen entry.
-- continueCompile calls this once on 'scopeStateRef' after
-- 'seedEarlyCgEnv' returns the map; subsequent transitional
-- 'ctxFromIORef ()' bridges (used by @lookupAliasDecl@) see the
-- populated map without a separate IORef hop.
withAliases
    :: Map.Map String Can.Alias
    -> LowerCtx
    -> LowerCtx
withAliases aliases ctx =
    ctx { _lc_aliases = aliases }


-- | v0.17 iter 18 (task #654) — install the merged record-field
-- index.  Replaces 'globalAllFieldIdx' IORef write at codegen
-- entry.  Same write-once-via-scopeStateRef contract as
-- 'withAliases'; read by @tvarsInEmitted@ via 'ctxFromIORef ()'.
withFieldIdx
    :: Rec.RecordRegistry
    -> LowerCtx
    -> LowerCtx
withFieldIdx fieldIdx ctx =
    ctx { _lc_fieldIdx = fieldIdx }


-- | v0.17 iter 19 (task #654) — install the merged union-names
-- registry.  Replaces 'globalUnionNames' IORef writes.  Unlike
-- 'withAliases'/'withFieldIdx' (single-write at codegen entry),
-- this is called at THREE cascade points in 'continueCompile':
-- C9 entry-mod seed (writes 'depUnionNames'), C10 dep-mod update
-- (writes 'cgEnv._cg_unionNames' after dep-imports finish), and
-- the post-sig-emit refresh.  Each replaces a 'writeIORef
-- globalUnionNames $!' call.  Read by the renderer chains via
-- 'readIORefNoCse scopeStateRef' + '_lc_unionNames' projection.
withUnionNames
    :: Set.Set String
    -> LowerCtx
    -> LowerCtx
withUnionNames unions ctx =
    ctx { _lc_unionNames = unions }


-- | v0.17 P3.4c.0 — install the merged per-union metadata map.
-- Mirror of 'Rec.withUnionDetails'; sibling of 'withUnionNames' on
-- the LowerCtx-threaded path.  Called at the same cascade points
-- so '_lc_unionDetails' stays in lock-step with '_lc_unionNames'.
-- Pure no-op until 'subjectIsSealedIface' / 'shouldEmitSealedIface'
-- read it (P3.4c.1 onward).
withUnionDetails
    :: Map.Map String (ModuleName.Canonical, Can.CtorOpts, [String], [Can.Ctor])
    -> LowerCtx
    -> LowerCtx
withUnionDetails details ctx =
    ctx { _lc_unionDetails = details }


-- | Install the typed-FFI wrapper name set on the LowerCtx so the
-- lowerer's @Can.VarKernel@ arms read it structurally.  Populated from
-- 'LoadedFfiTables._lft_typedWrapperNames' at codegen entry
-- ('generateGoMulti').  v0.17 close iter 5 (Phase 7 IORef defusing):
-- the legacy @Env.ffiTypedWrapperNamesRef@ IORef has been deleted; this
-- is the only path the registry can flow through.
withFfiTypedWrapperNames
    :: Set.Set String
    -> LowerCtx
    -> LowerCtx
withFfiTypedWrapperNames names ctx =
    ctx { _lc_ffiTypedWrapperNames = names }


-- | v0.17 close P1 step 2/8 — install the typed-FFI wrapper param-type
-- map on the LowerCtx.  Sibling of 'withFfiTypedWrapperNames';
-- read by the N-arg FFI dispatch arm to coerce arguments.
withFfiTypedWrapperParams
    :: Map.Map String [String]
    -> LowerCtx
    -> LowerCtx
withFfiTypedWrapperParams params ctx =
    ctx { _lc_ffiTypedWrapperParams = params }
