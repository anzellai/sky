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
    , memberLambdaType
    , lookupAlias
    , lookupAnnotation
    , lookupSolved
    , withLambdaTypes
    , withLambdaGoStrs
    ) where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set

import qualified Sky.AST.Canonical as Can
import qualified Sky.Generate.Go.Record as Rec
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
    , _lc_aliasMap    :: !(Map.Map String String)
        -- ^ Reserved for a future module-prefix → unprefixed alias
        -- shortcut map.  Empty today; populated in PR 2 when the
        -- alias-lookup migration lands.  Kept in the record so PRs
        -- 2-6 don't need to grow the type again.
    , _lc_annotMap    :: !(Map.Map String T.Annotation)
        -- ^ Per-callee generalised annotation map.  Snapshotted
        -- from `globalAnnotMap`.  Read by σ-derivation at every
        -- reachable instance emission.
    }


-- | Empty `LowerCtx` for tests and bootstrap.  Real compilation
-- always goes through `buildLowerCtx`.
emptyLowerCtx :: ModuleName.Canonical -> LowerCtx
emptyLowerCtx home = LowerCtx
    { _lc_module      = home
    , _lc_solved      = Solve.emptySolvedTypes
    , _lc_lambdaTypes = Map.empty
    , _lc_lambdaGoStr = Map.empty
    , _lc_aliases     = Map.empty
    , _lc_fieldIdx    = Map.empty
    , _lc_unionNames  = Set.empty
    , _lc_aliasMap    = Map.empty
    , _lc_annotMap    = Map.empty
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
    -> LowerCtx
buildLowerCtx home solved aliases fieldIdx unions annots = LowerCtx
    { _lc_module      = home
    , _lc_solved      = solved
    , _lc_lambdaTypes = Map.empty
    , _lc_lambdaGoStr = Map.empty
    , _lc_aliases     = aliases
    , _lc_fieldIdx    = fieldIdx
    , _lc_unionNames  = unions
    , _lc_aliasMap    = Map.empty
    , _lc_annotMap    = annots
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
