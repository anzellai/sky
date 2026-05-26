-- | Constraint solver for Sky's Hindley-Milner type inference.
--
-- Derivative work adapted from elm/compiler's @Type.Solve@
-- (Copyright © 2012–present Evan Czaplicki, BSD-3-Clause). See
-- NOTICE.md at the repo root for the full attribution and licence
-- text.
--
-- Walks the constraint tree, unifying types via UnionFind. Uses a
-- TVar name cache to share UF variables for the same type variable
-- name. The defensive solver-step bound (`SKY_SOLVER_BUDGET`) is a
-- Sky-specific addition not present upstream.
module Sky.Type.Solve
    ( solve
    , solveWithLocals
    , solveWithInstances
    , solveWithInstancesAndRegions   -- v0.15 Stage A
    , SolveResult(..)
    , SolvedTypes(..)
    , emptySolvedTypes               -- v0.15.x P37a
    , lookupSolvedVar                -- v0.15.x P37a
    , lookupSolvedRegion             -- v0.15.x P37a
    , insertSolvedVar                -- v0.15.x P37a
    , unionSolvedEnv                 -- v0.15.x P37a
    , RegionTypes                    -- v0.15 Stage A
    , CallInstance(..)
    , CallSiteInstance(..)
    , showType
    , showTypeWith
    , moduleRenaming
    , solveErrorToDiagnostic
    )
    where

import Control.Monad (unless)
import Data.IORef
import qualified Data.List as List
import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Sky.Type.Type as T
import qualified Sky.Type.UnionFind as UF
import qualified Sky.Type.Unify as Unify
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Reporting.Diagnostic as Diag
import System.Environment (lookupEnv)
import Text.Read (readMaybe)


-- | Emit a "LINE:COL:" prefix for error messages so downstream consumers
-- (LSP, CLI) can parse a source location out of the plain-text payload.
-- An all-zero region (synthetic) prints nothing.
posPrefix :: A.Region -> String
posPrefix (A.Region (A.Position r c) _)
    | r <= 0 || c <= 0 = ""
    | otherwise        = show r ++ ":" ++ show c ++ ": "


-- | Result of solving constraints
data SolveResult
    = SolveOk !SolvedTypes
    | SolveError String
    deriving (Show)


-- | v0.15.x P37a — `SolvedTypes` is now a record that carries BOTH
-- the resolved per-name HM-type environment AND the per-region HM
-- type map.  Pre-P37a this was a bare `Map.Map String T.Type` and the
-- region map was returned alongside via a 4-tuple from
-- `solveWithInstancesAndRegions`; the lowerer snapshotted the region
-- map into `scopeStateRef`'s `_lc_regionTypes` slot and consulted it
-- through `lookupRegionType :: A.Region -> Maybe T.Type` (an
-- `unsafePerformIO`-wrapped IORef read).  That IORef read sits at the
-- heart of `letBindingType`'s shared seam with `letToGo` (cycle-3
-- audit gap C5) and is the load-bearing reason the LowerCtx cascade
-- can't migrate the remaining record-field / list-element / let-body
-- slots without GHC blackholing on deferred thunks.
--
-- P37a's job is purely SOLVER-SIDE: extend `SolvedTypes` to also
-- carry the region map (same shape as the pre-P37a `RegionTypes`
-- value the IORef holds), and populate it at solve time.  P37b will
-- migrate `letBindingType` to read from this pure value instead of
-- the IORef and then delete the `_lc_regionTypes` IORef field.  This
-- PR is a zero-behaviour-change extension: the IORef remains the
-- source of truth for `lookupRegionType` reads; the new field is
-- populated but not yet consumed by `letBindingType` /
-- `lookupRegionType`.
--
-- Field naming convention (`_st<thing>`) matches the project's
-- record-field style (cf. `_lc_*` on `LowerCtx`).
data SolvedTypes = SolvedTypes
    { _stEnv     :: !(Map.Map String T.Type)
        -- ^ Pre-P37a's plain map: maps variable names to their
        -- resolved HM types.  Every consumer that did
        -- `Map.lookup name solvedTypes` now goes through this field
        -- (or the convenience helper `lookupSolvedVar`).
    , _stRegions :: !RegionTypes
        -- ^ v0.15 Stage A's per-region type map, now stored as
        -- pure data alongside the env.  Populated by every solve
        -- entry point (`solve` / `solveWithLocals` /
        -- `solveWithInstancesAndRegions`) so the SolvedTypes value
        -- always carries the full pure snapshot.  P37b consumes
        -- this to make `letBindingType`'s region lookup pure.
    }
    deriving (Eq, Show)


-- | An empty `SolvedTypes`.  Used at LSP boundaries where the solver
-- errored before producing a usable map AND in tests that need a
-- placeholder.
emptySolvedTypes :: SolvedTypes
emptySolvedTypes = SolvedTypes Map.empty Map.empty


-- | Look up a top-level / let-bound name's HM-resolved type.
-- Convenience for the common `Map.lookup name (_stEnv st)` idiom.
lookupSolvedVar :: String -> SolvedTypes -> Maybe T.Type
lookupSolvedVar n = Map.lookup n . _stEnv


-- | Look up the HM type recorded at a given source region.
-- Pure substitute for the `lookupRegionType` IORef read that P37b
-- will migrate `letBindingType` over to.  Today this is consulted
-- only by the new P37a spec; the lowerer still reads via the
-- IORef-backed path.
lookupSolvedRegion :: A.Region -> SolvedTypes -> Maybe T.Type
lookupSolvedRegion r = Map.lookup r . _stRegions


-- | Insert a binding into the env map, leaving the region map
-- untouched.  Convenience for callers that previously did
-- `Map.insert n t solved` on the bare-Map shape.
insertSolvedVar :: String -> T.Type -> SolvedTypes -> SolvedTypes
insertSolvedVar n t st = st { _stEnv = Map.insert n t (_stEnv st) }


-- | Extend the env map with the entries from another map.  The
-- argument map's entries take precedence on key clash (matches
-- `Map.union` semantics on `Map.union additions existing`).
unionSolvedEnv :: Map.Map String T.Type -> SolvedTypes -> SolvedTypes
unionSolvedEnv additions st =
    st { _stEnv = Map.union additions (_stEnv st) }


-- | v0.15 Stage A — per-region type map.  Every solved constraint's
-- actual type, keyed by the constraint's source region.  Consumed
-- by the typed-directed lowerer in v0.15 Stage B+ to look up the
-- concrete HM type of any sub-expression by its source position.
type RegionTypes = Map.Map A.Region T.Type


-- | Solver state
data SolverState = SolverState
    { _env      :: !(Map.Map String T.Variable)  -- variable name → UF variable
    , _varCache :: !(IORef (Map.Map String T.Variable))  -- TVar name → shared UF variable
    , _rank     :: !Int
    , _locals   :: !(IORef (Map.Map String [T.Variable]))
      -- Audit P2-2: store the FULL list of resolved types per
      -- binding name (one entry per CLet-capture firing). Inner
      -- scopes fire first (their body-solve completes while outer
      -- body-solve is still in flight), so with front-of-list
      -- prepend the innermost binding ends up at index 0. LSP's
      -- lookupLocal picks the smallest enclosing scope's binding
      -- which is also innermost; both agree on "index 0" for the
      -- shadowed case. For a single binding (no shadowing) the
      -- list has one element regardless.
    , _solverSteps :: !(IORef Int)
      -- Solver-step counter for the defensive bound (Limitation #17
      -- hardening). Incremented by `bumpSolverStep` at the top of
      -- every `solveHelp` invocation. When the count exceeds
      -- `_solverBudget`, the solver short-circuits with a clear
      -- TYPE ERROR rather than letting the constraint explosion
      -- consume unbounded heap. The counter is global per `solve`
      -- call (reset to 0 on each entry); legitimate large modules
      -- consume well under 100K steps; the explosion path consumes
      -- millions before OOMing.
    , _solverBudget :: !Int
      -- Hard cap on _solverSteps. Defaults to defaultSolverBudget
      -- (5,000,000), overridable via the `SKY_SOLVER_BUDGET` env
      -- var. Set to 0 to disable the bound entirely (debug only —
      -- DO NOT ship without the bound; that's what Limitation #17
      -- showed can OOM the host).
    , _callInstances :: !(IORef [CallInstanceRecord])
      -- v0.13 Phase A1: every CForeign solver step records the
      -- (region, callee-name, fresh-TVars) triple here.  After
      -- solve completes, the post-pass evaluates each fresh-TVar
      -- through union-find to derive the concrete type instance
      -- at that call site.  Front-of-list prepend; order doesn't
      -- matter because the downstream consumer deduplicates by
      -- (callee, type-args) anyway.
    , _regionVars :: !(IORef (Map.Map A.Region T.Variable))
      -- v0.15 Stage A — per-region UF-variable map.  Every solved
      -- CEqual / CLocal / CForeign records its actualVar against
      -- the constraint's region here.  After solve completes,
      -- variables are walked to concrete `T.Type`s, producing the
      -- region-keyed `RegionTypes` map the typed-directed lowerer
      -- queries in Stage B+.  Behaviour-preserving: existing
      -- consumers (LSP, codegen, monomorphisation) don't read this
      -- map yet.  Front-of-write: last-write-wins per region (a
      -- constraint may fire multiple times during fixpoint
      -- iteration; final entry is the converged type).
    }


-- | A single call-site polymorphic-reference record captured during
-- solve.  Post-solve evaluation turns each `_ci_freshVars` UF
-- variable into a concrete type via `variableToType`.
data CallInstanceRecord = CallInstanceRecord
    { _ci_region      :: !A.Region        -- where the reference appears
    , _ci_callee      :: !String          -- qualified callee name
    , _ci_freshVars   :: ![T.Variable]    -- per quantified type var
    , _ci_quantifiers :: ![String]        -- Sky-source Forall var names
                                          -- in the same order as freshVars
                                          -- (excludes the wildcard `any`)
                                          -- so downstream codegen can map
                                          -- each concrete type back to its
                                          -- annotation TVar by name even
                                          -- when codegen-time defaulting
                                          -- collapses some positions.
    }


-- | Public view of a resolved call-site instance.  Used by
-- monomorphisation as the (callee, type-args) key.
data CallInstance = CallInstance
    { _instance_callee      :: !String
    , _instance_types       :: ![T.Type]
    , _instance_quantifiers :: ![String]  -- annotation Forall names in
                                          -- positional alignment with
                                          -- `_instance_types`.  Used by
                                          -- call-site coercion to build
                                          -- σ in Sky-name space so it
                                          -- doesn't depend on Go-side
                                          -- defaulting collapsing some
                                          -- positions away.
    }
    deriving (Eq, Ord, Show)


-- | A call-site annotated with its resolved instance.  The compile
-- pipeline keeps a Map keyed by (file, region) so codegen can pick
-- the right mangled name when emitting each `Can.Call` node.
--
-- The CallInstance inside is the same key the monomorphisation
-- registry uses, so a region-keyed lookup and a Set-of-instances
-- table stay consistent: every CallSiteInstance's instance appears
-- in the deduped instance set.
data CallSiteInstance = CallSiteInstance
    { _cs_region   :: !A.Region
    , _cs_instance :: !CallInstance
    }
    deriving (Show)


-- | Default cap on solver steps before bailing with a defensive
-- TYPE ERROR. 5,000,000 is ~50x the largest legitimate Std.Ui-
-- heavy module measured (heap-bound-fence.sky post-fix uses well under
-- 100K steps); the broken-stdlib reproducer hit millions before
-- exhausting 4-5 GB of heap.
defaultSolverBudget :: Int
defaultSolverBudget = 5000000


-- | Per-constraint multiplier for the structural-budget computation.
-- Effective budget = max(defaultSolverBudget, constraint_count *
-- defaultSolverBudgetFactor). The default 200 means: a 100-constraint
-- module gets the floor (5,000,000); a 1M-constraint module gets
-- 200M. This scales with input size while still catching pathological
-- expansion (where N constraints generate >> N×factor solver steps).
defaultSolverBudgetFactor :: Int
defaultSolverBudgetFactor = 200


-- | Three-mode env var resolution for the solver budget:
--
--   * SKY_SOLVER_BUDGET unset  → STRUCTURAL mode (the v0.12+ default).
--     Effective cap = max(defaultSolverBudget, constraint_count * factor).
--     Scales with input size, so legitimately-large generated
--     codebases don't trip a wall-clock-shaped constant cap, while
--     pathological constraint expansion (which generates >> N×factor
--     solver steps from N constraints) is still caught.
--
--   * SKY_SOLVER_BUDGET=0      → DISABLED (escape hatch).
--     No cap at all. Debug only — risk of unbounded heap consumption.
--
--   * SKY_SOLVER_BUDGET=N (>0) → ABSOLUTE mode (legacy behaviour).
--     Effective cap is exactly N. Backwards compatible with the
--     pre-v0.12 wall-clock-shaped budget. Useful for regression
--     tests that want to deterministically trip the bound.
--
-- SKY_SOLVER_BUDGET_FACTOR overrides the structural-mode multiplier
-- (default 200). Ignored in DISABLED / ABSOLUTE modes.
data SolverBudgetMode
    = BudgetStructural  -- env unset
    | BudgetDisabled    -- SKY_SOLVER_BUDGET=0
    | BudgetAbsolute !Int  -- SKY_SOLVER_BUDGET=N>0


readBudgetMode :: IO SolverBudgetMode
readBudgetMode = do
    s <- lookupEnv "SKY_SOLVER_BUDGET"
    return $ case s of
        Nothing -> BudgetStructural
        Just str -> case readMaybe str of
            Just n | n == 0 -> BudgetDisabled
                   | n > 0  -> BudgetAbsolute n
            _               -> BudgetStructural  -- malformed → fallback


readSolverBudgetFactor :: IO Int
readSolverBudgetFactor = do
    s <- lookupEnv "SKY_SOLVER_BUDGET_FACTOR"
    return $ case s >>= readMaybe of
        Just n | n > 0 -> n
        _              -> defaultSolverBudgetFactor


-- | Count constraints in the input tree. Treats every leaf
-- constraint (CEqual, CLocal, CForeign, CPattern, CTrue,
-- CSaveTheEnvironment) as a single unit, plus recursively counting
-- through CAnd / CLet branches. Used to derive a structural budget
-- that scales with input size — catches pathological cases (which
-- expand FAR beyond N×factor steps) while letting legitimate
-- large-codebase compiles run to completion.
countConstraints :: T.Constraint -> Int
countConstraints = go
  where
    go c = case c of
        T.CTrue                  -> 1
        T.CSaveTheEnvironment    -> 1
        T.CEqual _ _ _ _         -> 1
        T.CLocal _ _ _           -> 1
        T.CForeign _ _ _ _       -> 1
        T.CPattern _ _ _ _       -> 1
        T.CAnd cs                -> 1 + sum (map go cs)
        T.CLet { T._headerCon = h, T._bodyCon = b } ->
            1 + go h + go b


effectiveSolverBudget :: T.Constraint -> IO Int
effectiveSolverBudget root = do
    mode <- readBudgetMode
    case mode of
        BudgetDisabled    -> return 0
        BudgetAbsolute n  -> return n
        BudgetStructural  -> do
            factor <- readSolverBudgetFactor
            let n = countConstraints root
            return (max defaultSolverBudget (n * factor))


-- Back-compat shim: callers that import readSolverBudget still get
-- a number out, but the NEW caller path (via solve / solveWithLocals)
-- routes through effectiveSolverBudget so the structural mode kicks
-- in by default.
readSolverBudget :: IO Int
readSolverBudget = do
    mode <- readBudgetMode
    case mode of
        BudgetDisabled   -> return 0
        BudgetAbsolute n -> return n
        BudgetStructural -> return defaultSolverBudget


-- | Increment the solver-step counter. Returns Just errMsg when
-- the budget is exceeded (caller MUST short-circuit and propagate
-- the error up the constraint tree); returns Nothing when there's
-- still budget. Bound = 0 disables the check.
bumpSolverStep :: SolverState -> IO (Maybe String)
bumpSolverStep state
    | _solverBudget state == 0 = return Nothing  -- disabled
    | otherwise = do
        n <- readIORef (_solverSteps state)
        let n' = n + 1
        writeIORef (_solverSteps state) n'
        if n' > _solverBudget state
            then return (Just (budgetExceededMsg (_solverBudget state)))
            else return Nothing


-- | v0.15 Stage A — record a region → UF-variable mapping.
--
-- Called from every solved CEqual / CLocal / CForeign once unify has
-- succeeded.  Variables (not types) are stored because subsequent
-- constraints can further unify the variable's transitive structure.
-- Post-solve, callers (`solveWithRegions` / wrappers) walk each var
-- to a concrete `T.Type`.
--
-- The skip on `A.zero`-position regions filters synthetic constraints
-- the constraint generator emits with `A.one` / `A.zero` sentinels for
-- bookkeeping (let-binding header, lambda-param scope, etc.).  Real
-- source positions carry non-zero line:col, so this filter loses no
-- user-facing type info.
recordRegionVar :: SolverState -> A.Region -> T.Variable -> IO ()
recordRegionVar state region var =
    case A._start region of
        A.Position 0 0 -> return ()
        _ -> modifyIORef' (_regionVars state) (Map.insert region var)


-- | v0.15 Stage A — convert the per-region UF-variable map to typed
-- form.  Walked at solve-end so every variable's transitive structure
-- is fully unified.
freezeRegionTypes :: IORef (Map.Map A.Region T.Variable) -> IO RegionTypes
freezeRegionTypes ref = do
    raw <- readIORef ref
    Map.traverseWithKey (\_ v -> variableToType v) raw


budgetExceededMsg :: Int -> String
budgetExceededMsg budget = unlines
    [ "TYPE ERROR: constraint solver exceeded budget (" ++ show budget ++ " operations)."
    , ""
    , "This usually indicates an ill-typed Sky source — most commonly:"
    , "  * Passing a value where a function is expected (or vice-versa)"
    , "    inside a polymorphic helper. The HM type checker tries to"
    , "    unify the polymorphic type variable with the wrong shape and"
    , "    propagates the constraint through every call site, hitting"
    , "    combinatorial explosion."
    , "  * A recursive type-alias definition that doesn't have a base case."
    , "  * A pattern that creates an infinite type via the occurs check."
    , ""
    , "Look at the most recently edited polymorphic helper (a function"
    , "with a type variable like `a` or `msg` in its annotation) — the"
    , "issue is almost certainly there."
    , ""
    , "If you're sure the source is well-typed and the cap is too low,"
    , "set `SKY_SOLVER_BUDGET=N` to a larger value (default 5,000,000)."
    , "Set to 0 to disable the bound entirely (NOT recommended — risk"
    , "of unbounded heap consumption)."
    ]


-- | Solve a constraint tree.
solve :: T.Constraint -> IO SolveResult
solve constraint = do
    cache <- newIORef Map.empty
    locals <- newIORef Map.empty
    steps <- newIORef 0
    budget <- effectiveSolverBudget constraint
    instances <- newIORef []
    regions <- newIORef Map.empty
    let state0 = SolverState Map.empty cache 0 locals steps budget instances regions
    (result, finalState) <- solveHelp state0 constraint
    case result of
        Nothing -> do
            envTypes <- readSolvedTypes (_env finalState)
            localVars <- readIORef (_locals finalState)
            -- Resolve the stored UF variables NOW — all constraints
            -- have been solved, so vars should be fully determined.
            localTys <- Map.traverseWithKey (\_ vars ->
                mapM variableToType (filter (const True) vars)) localVars
            -- Merge: _locals captures every CLet-bound name including
            -- top-level declarations that _env loses after CLet restore.
            -- Take the first (innermost) type for each name — UNLESS
            -- the name was bound MULTIPLE TIMES with structurally-
            -- different types (intra-module shadowing). In that case,
            -- collapse to a sentinel TVar "_ambig" so downstream
            -- codegen knows the lookup is ambiguous and falls back
            -- to safe any-routing rather than picking the wrong one.
            --
            -- Concrete bug class this fixes: a module with multiple
            -- `let result = ...` bindings (different types per
            -- function — see examples/06-json/Main.sky) used to
            -- collapse to one head-type, breaking typed-codegen at
            -- the OTHER scopes' lookup sites.
            let pickType tys = case List.nub (filter (not . isUnboundTVar) tys) of
                    []  -> head tys  -- all unbound — keep first as-is
                    [t] -> t          -- all resolved types agree
                    _   -> T.TVar "_ambig"  -- distinct concrete types — ambiguous
                isUnboundTVar (T.TVar n) = "_" `List.isPrefixOf` n || null n
                isUnboundTVar _ = False
                localFirst = Map.map pickType (Map.filter (not . null) localTys)
            let merged = Map.union localFirst envTypes
            -- v0.15.x P37a — freeze the per-region var map alongside
            -- the per-name env so the SolvedTypes record carries
            -- both as pure data.  Same data the IORef-backed
            -- `lookupRegionType` reads via the
            -- `solveWithInstancesAndRegions` path; populating it
            -- here too means LSP / single-module entry points emit
            -- a complete SolvedTypes even though they don't yet
            -- consume the regions field.
            regionTys <- freezeRegionTypes (_regionVars finalState)
            return (SolveOk (SolvedTypes merged regionTys))
        Just err -> return (SolveError err)


-- | Like `solve`, but also returns the accumulated local-binding types.
-- LSP hover uses this so it can surface `file : Int` instead of
-- "(local binding)" without the types leaking into codegen. Audit
-- P2-2: the returned map is `name → [types]` (ordered innermost-first)
-- so shadowed locals don't collapse on hover.
solveWithLocals :: T.Constraint -> IO (SolveResult, Map.Map String [T.Type])
solveWithLocals constraint = do
    cache <- newIORef Map.empty
    locals <- newIORef Map.empty
    steps <- newIORef 0
    budget <- effectiveSolverBudget constraint
    instances <- newIORef []
    regions <- newIORef Map.empty
    let state0 = SolverState Map.empty cache 0 locals steps budget instances regions
    (result, finalState) <- solveHelp state0 constraint
    localVars <- readIORef (_locals finalState)
    localTypes <- Map.traverseWithKey (\_ vars ->
        mapM variableToType vars) localVars
    case result of
        Nothing -> do
            envTypes <- readSolvedTypes (_env finalState)
            -- v0.15.x P37a — same freeze pattern as `solve` above.
            regionTys <- freezeRegionTypes (_regionVars finalState)
            return (SolveOk (SolvedTypes envTypes regionTys), localTypes)
        Just err ->
            return (SolveError err, localTypes)


-- | v0.13 Phase A1: solve a constraint set and ALSO return the
-- call-site instance map needed by the monomorphisation pass.
--
-- Every `T.CForeign` constraint hit during solve records a
-- `CallInstanceRecord` carrying the call-site region, the callee's
-- qualified name, and the list of fresh UF variables created for the
-- callee's quantified type parameters.  After the solver successfully
-- unifies all constraints, this function walks each record and
-- evaluates each fresh var via `variableToType` to derive the
-- concrete type the call site instantiated to.
--
-- Result: a list of `CallInstance` values, one per polymorphic
-- reference, deduplicated by `(callee, type-args)`.  Concrete-typed
-- pre-monomorphisation instances form the table that downstream
-- codegen iterates to emit per-instance Go functions.
--
-- Skips records where any fresh var resolves to an unbound TVar
-- (e.g. a polymorphic helper that's never called with a concrete
-- type at this module's `main` boundary).  Those instances need
-- the `_Any` fallback at emission time.
solveWithInstances :: T.Constraint -> IO (SolveResult, [CallInstance], [CallSiteInstance])
solveWithInstances constraint = do
    (result, ci, csi, _) <- solveWithInstancesAndRegions constraint
    return (result, ci, csi)


-- | v0.15 Stage A — superset of `solveWithInstances` that ALSO
-- returns the per-region type map.  The lowerer (Stage B+) calls
-- this and threads `RegionTypes` into `LowerCtx` so any
-- sub-expression's HM type can be queried by its source region.
-- The 4-tuple shape preserves backward compatibility with
-- `solveWithInstances` (which now delegates here and discards the
-- regions component).
solveWithInstancesAndRegions
    :: T.Constraint
    -> IO (SolveResult, [CallInstance], [CallSiteInstance], RegionTypes)
solveWithInstancesAndRegions constraint = do
    cache <- newIORef Map.empty
    locals <- newIORef Map.empty
    steps <- newIORef 0
    budget <- effectiveSolverBudget constraint
    instances <- newIORef []
    regions <- newIORef Map.empty
    let state0 = SolverState Map.empty cache 0 locals steps budget instances regions
    (result, finalState) <- solveHelp state0 constraint
    case result of
        Nothing -> do
            envTypes <- readSolvedTypes (_env finalState)
            localVars <- readIORef (_locals finalState)
            localTys <- Map.traverseWithKey (\_ vars ->
                mapM variableToType vars) localVars
            let pickType tys = case List.nub (filter (not . isUnboundTVar) tys) of
                    []  -> head tys
                    [t] -> t
                    _   -> T.TVar "_ambig"
                isUnboundTVar (T.TVar n) = "_" `List.isPrefixOf` n || null n
                isUnboundTVar _ = False
                localFirst = Map.map pickType (Map.filter (not . null) localTys)
                merged = Map.union localFirst envTypes
            records <- readIORef (_callInstances finalState)
            (ci, csi) <- extractInstancesAndSites records
            regionTys <- freezeRegionTypes (_regionVars finalState)
            -- v0.15.x P37a — the new pure `SolvedTypes` record
            -- carries the region map alongside the env.  Existing
            -- consumers (compile.hs, lsp) keep reading `_stEnv`;
            -- the IORef-backed `lookupRegionType` retains its
            -- previous semantics until P37b's migration.  The
            -- fourth tuple slot (`regionTys`) stays for backward
            -- compatibility — `Compile.hs` still threads it into
            -- `scopeStateRef._lc_regionTypes` directly today.
            return (SolveOk (SolvedTypes merged regionTys), ci, csi, regionTys)
        Just err -> do
            -- v0.13 Phase A4: even on error, return whatever
            -- call-site instances were captured BEFORE the error
            -- bubbled up.  The CSIs are useful for spec emission +
            -- mangled-name routing even when the dep had a non-
            -- foreign type error.  Without this, dep modules that
            -- pass-2 falls back to pass-1 lose their CSI capture,
            -- and call sites inside them fall back to generic names
            -- — breaking the drop-generics pass.
            records <- readIORef (_callInstances finalState)
            (ci, csi) <- extractInstancesAndSites records
            regionTys <- freezeRegionTypes (_regionVars finalState)
            return (SolveError err, ci, csi, regionTys)


-- | Walk the captured call-instance records, resolve each fresh
-- UF var to its concrete type, deduplicate by (callee, type-args).
-- Records where any fresh var doesn't resolve to a concrete type
-- are skipped (the monomorphisation `_Any` fallback handles them).
extractInstances :: [CallInstanceRecord] -> IO [CallInstance]
extractInstances records = do
    (ci, _) <- extractInstancesAndSites records
    return ci


-- | Same as `extractInstances` but ALSO returns the per-call-site
-- registry — one `CallSiteInstance` per resolved record, indexable
-- by `(file, region)` downstream.  The first return value is the
-- deduplicated set the monomorphiser emits one Go function for;
-- the second is the call-site map codegen uses to pick the right
-- mangled name at each emission site.
--
-- v0.13 Phase A5 update: instances with PARTIALLY-resolved type
-- args (e.g. `Result Error <free-a>`) are captured with the free
-- positions normalised to `T.TVar "any"`.  This is the `_Any`
-- fallback at the type-arg level — it lets the monomorphisation
-- substitution map say `T1 → any` instead of skipping the call
-- entirely.  Without this normalisation, partial instances drop
-- and the codegen falls back to the pre-A5 erase-to-any path
-- (which mismatches Go's type inference at typed call sites).
extractInstancesAndSites
    :: [CallInstanceRecord] -> IO ([CallInstance], [CallSiteInstance])
extractInstancesAndSites records = do
    raw <- mapM resolveOne records
    let sites = [ CallSiteInstance (_ci_region r) ci
                | (r, Just ci) <- zip records raw ]
        deduped = Set.toList $ Set.fromList [ci | Just ci <- raw]
    return (deduped, sites)
  where
    resolveOne rec_ = do
        tys <- mapM variableToType (_ci_freshVars rec_)
        let normalised = map (normaliseUnresolved isConcreteType) tys
        return (Just (CallInstance (_ci_callee rec_) normalised
                                   (_ci_quantifiers rec_)))


-- | Replace unresolved TVars (FlexVar with internal `_` prefix or
-- raw user-TVar names left polymorphic) with the wildcard
-- `T.TVar "any"`.  Keeps concrete types and aliases intact.  Used
-- by the A5 instance extractor so partial-resolution call sites
-- still produce a usable monomorphisation key.
normaliseUnresolved :: (T.Type -> Bool) -> T.Type -> T.Type
normaliseUnresolved isConc = go
  where
    go t
        | isConc t = t
        | otherwise = case t of
            T.TVar _ -> T.TVar "any"
            T.TLambda f r -> T.TLambda (go f) (go r)
            T.TType h n args -> T.TType h n (map go args)
            T.TRecord fields ext ->
                T.TRecord (Map.map (\(T.FieldType i ty) ->
                                       T.FieldType i (go ty)) fields) ext
            T.TUnit -> T.TUnit
            T.TTuple a b cs -> T.TTuple (go a) (go b) (map go cs)
            T.TAlias h n pairs aty ->
                T.TAlias h n (map (\(k, v) -> (k, go v)) pairs) aty

    isConcrete = isConcreteType


-- | Concrete-type predicate.  A type is concrete iff it has no
-- unresolved TVar (FlexVar with `_` prefix or empty name) or
-- user-declared lowercase TVar (`a`, `msg`, …).  Uppercase TVar
-- names are accepted because they correspond to nominal types
-- (e.g. `Maybe`, `String`, …) in Sky's HM output.
isConcreteType :: T.Type -> Bool
isConcreteType (T.TVar n) =
       not (null n) && head n /= '_' && n /= "any"
    && all (\c -> c >= 'A' && c <= 'Z'
                || c >= 'a' && c <= 'z'
                || c == '_'
                || c >= '0' && c <= '9') n
    && (head n >= 'A' && head n <= 'Z')
isConcreteType (T.TLambda f t) = isConcreteType f && isConcreteType t
isConcreteType (T.TType _ _ args) = all isConcreteType args
isConcreteType (T.TRecord fields _) =
    all (\(T.FieldType _ ty) -> isConcreteType ty) (Map.elems fields)
isConcreteType T.TUnit = True
isConcreteType (T.TTuple a b cs) =
    isConcreteType a && isConcreteType b && all isConcreteType cs
isConcreteType (T.TAlias _ _ pairs _) =
    all (isConcreteType . snd) pairs


-- | Convert a Type to a UF Variable, SHARING variables for the same TVar name.
-- This is the critical function: when two constraints reference TVar "_arg0",
-- they get the SAME UF variable, so unification propagates between them.
typeToVar :: SolverState -> T.Type -> IO T.Variable
typeToVar state ty = case ty of
    T.TVar "any" ->
        -- Wildcard semantics: every occurrence of `any` in source
        -- types gets its OWN fresh unification variable, never shared
        -- via the cache. Without this, distinct `any` occurrences
        -- in the same definition collapse to a single variable —
        -- so `case x of AttrA s -> Just s | AttrB v -> Just v` (where
        -- AttrA holds String and AttrB holds `any`) unifies `String`
        -- through the AttrA branch into the shared `any` slot, then
        -- the construction site `AttrB 42` rejects the Int because
        -- the slot is already pinned to String. The wildcard split
        -- restores the "any unifies with anything, independently"
        -- semantics users expect.
        UF.fresh (T.Descriptor (T.FlexVar (Just "any")) (_rank state) T.noMark Nothing)

    T.TVar name -> do
        -- Share UF variables for the same TVar name via cache.
        -- With unique names per call site (from IO-based constraint generation),
        -- this correctly shares within a definition but not across call sites.
        cache <- readIORef (_varCache state)
        case Map.lookup name cache of
            Just var -> return var  -- SHARED: return existing variable
            Nothing -> do
                var <- UF.fresh (T.Descriptor (T.FlexVar (Just name)) (_rank state) T.noMark Nothing)
                modifyIORef' (_varCache state) (Map.insert name var)
                return var

    T.TLambda from to -> do
        fromVar <- typeToVar state from
        toVar <- typeToVar state to
        UF.fresh (T.Descriptor (T.Structure (T.Fun1 fromVar toVar)) (_rank state) T.noMark Nothing)

    T.TType home name args -> do
        argVars <- mapM (typeToVar state) args
        UF.fresh (T.Descriptor (T.Structure (T.App1 home name argVars)) (_rank state) T.noMark Nothing)

    T.TRecord fields mExt -> do
        fieldVars <- Map.traverseWithKey (\_ (T.FieldType _ t) -> typeToVar state t) fields
        extVar <- case mExt of
            Nothing -> UF.fresh (T.Descriptor (T.Structure T.EmptyRecord1) (_rank state) T.noMark Nothing)
            Just name -> typeToVar state (T.TVar name)
        UF.fresh (T.Descriptor (T.Structure (T.Record1 fieldVars extVar)) (_rank state) T.noMark Nothing)

    T.TUnit ->
        UF.fresh (T.Descriptor (T.Structure T.Unit1) (_rank state) T.noMark Nothing)

    T.TTuple a b more -> do
        aVar <- typeToVar state a
        bVar <- typeToVar state b
        -- The unifier's Tuple1 is hard-coded to 2- or 3-tuples (Maybe third).
        -- 4+ element tuples collapse to 3-tuple for unification purposes —
        -- elements past the third flow as any at runtime. Catches structural
        -- mismatches on first three, loses precision beyond.
        mcVar <- case more of
            []      -> return Nothing
            (c : _) -> Just <$> typeToVar state c
        UF.fresh (T.Descriptor (T.Structure (T.Tuple1 aVar bVar mcVar)) (_rank state) T.noMark Nothing)

    T.TAlias home name pairs aliasType -> do
        pairVars <- mapM (\(n, t) -> do v <- typeToVar state t; return (n, v)) pairs
        innerVar <- case aliasType of
            T.Hoisted inner -> typeToVar state inner
            T.Filled inner -> typeToVar state inner
        UF.fresh (T.Descriptor (T.Alias home name pairVars innerVar) (_rank state) T.noMark Nothing)


-- | Convert Expected Type to a UF Variable (using shared cache)
expectedToVar :: SolverState -> T.Expected T.Type -> IO T.Variable
expectedToVar state (T.NoExpectation ty) = typeToVar state ty
expectedToVar state (T.FromContext _ _ ty) = typeToVar state ty
expectedToVar state (T.FromAnnotation _ _ _ ty) = typeToVar state ty


-- | Instantiate an Annotation into a UF Variable (fresh vars for quantified names).
-- Returns the instantiated variable plus the list of fresh UF vars created
-- for the NON-WILDCARD quantified type parameters, in declaration order.
-- v0.13 Phase A1 uses the fresh-var list to capture call-site
-- instantiation: after solve completes, evaluating each fresh var via
-- `variableToType` gives the concrete type at the call site.
--
-- Behaviour-preserving wrt pre-A1: every name in `freeVars` (including
-- the `any` wildcard) gets a fresh UF var stored in the local cache as
-- before — typeToVar's wildcard case bypasses the cache for `any` so the
-- entry is dead-write but the HM-side state shape is identical.  Only
-- the RETURNED fresh-var list omits `any` (the monomorphiser doesn't
-- specialise wildcards).
instantiateAnnotation :: SolverState -> T.Annotation -> IO (T.Variable, [T.Variable], [String])
instantiateAnnotation state (T.Forall freeVars canType)
    -- No quantification: use the GLOBAL cache for sharing.  No fresh
    -- vars to track — the call site is not polymorphic.
    | null freeVars = do
        v <- typeToVar state canType
        return (v, [], [])
    -- With quantification: create a LOCAL cache so each use gets fresh
    -- vars (identical to pre-A1).  The fresh-var list returned to the
    -- caller excludes the `any` wildcard so the monomorphisation pass
    -- doesn't try to specialise it.  The third return component is the
    -- list of quantifier names IN THE SAME ORDER as the fresh-var
    -- list — downstream codegen uses these names to map each concrete
    -- type back to its annotation TVar (load-bearing for partial-
    -- instance substitution when codegen-time defaulting collapses
    -- some positions, e.g. `Result e a` → `Result Error a` where `e`
    -- no longer appears as a Go generic but the call instance still
    -- has 2 entries).
    | otherwise = do
        localCache <- newIORef Map.empty
        named <- mapM (\name -> do
            v <- UF.fresh (T.Descriptor (T.FlexVar (Just name)) (_rank state) T.noMark Nothing)
            modifyIORef' localCache (Map.insert name v)
            return (name, v)) freeVars
        let instState = state { _varCache = localCache }
        v <- typeToVar instState canType
        let kept = [ (n, uv) | (n, uv) <- named, n /= "any" ]
            freshVars = map snd kept
            quantNames = map fst kept
        return (v, freshVars, quantNames)


-- ═══════════════════════════════════════════════════════════
-- SOLVER
-- ═══════════════════════════════════════════════════════════

solveHelp :: SolverState -> T.Constraint -> IO (Maybe String, SolverState)
solveHelp state constraint = do
    -- Defensive bound (Limitation #17 hardening). Every solveHelp
    -- entry costs one budget unit; combinatorial constraint
    -- explosions trip this and short-circuit with a clear TYPE
    -- ERROR rather than letting unbounded heap consumption OOM
    -- the host. CTrue / CSaveTheEnvironment are no-op cases but
    -- still count — they're cheap and counting them keeps the
    -- budget logic uniform.
    bumped <- bumpSolverStep state
    case bumped of
        Just errMsg -> return (Just errMsg, state)
        Nothing     -> solveHelpBody state constraint


solveHelpBody :: SolverState -> T.Constraint -> IO (Maybe String, SolverState)
solveHelpBody state constraint = case constraint of

    T.CTrue ->
        return (Nothing, state)

    T.CSaveTheEnvironment ->
        return (Nothing, state)

    T.CAnd constraints ->
        solveAll state constraints

    T.CEqual region _category actualType expected -> do
        actualVar <- typeToVar state actualType
        expectedVar <- expectedToVar state expected
        ok <- Unify.unify actualVar expectedVar
        if ok
            then do
                recordRegionVar state region actualVar
                return (Nothing, state)
            else do
                -- Debug: read back actual resolved types
                at <- variableToType actualVar
                et <- variableToType expectedVar
                let hint = resultMismatchHint at et
                    -- Smart diff for record-vs-record mismatches: if
                    -- both sides are records that share most fields,
                    -- show only the DIFFERING ones. Without this,
                    -- error messages on TEA cfg shapes (Live.app's
                    -- 6-field cfg) were unreadable walls of text.
                    (diffSummary, isDiff) = renderTypeMismatchTagged at et
                    -- Drop the `(from: ... vs ...)` trailer when we
                    -- already gave a field-level diff — it adds noise
                    -- without helping the user.
                    trailer
                        | isDiff = ""
                        | otherwise = " (from: " ++ showType actualType
                                   ++ " vs " ++ showExpected expected ++ ")"
                -- The diff summary either begins with `\n     expected: …`
                -- (leaf form) or `in <path>:\n     expected: …` (path
                -- form).  In the leaf case the `"Type mismatch: "`
                -- prefix would leave a trailing space line before the
                -- newline; trim that to keep the body tight.
                let sep = case diffSummary of
                        ('\n':_) -> "Type mismatch:"
                        _        -> "Type mismatch: "
                return (Just $ posPrefix region ++ sep ++ diffSummary ++ trailer ++ hint, state)

    T.CLocal region name expected -> do
        case Map.lookup name (_env state) of
            Just var -> do
                expectedVar <- expectedToVar state expected
                ok <- Unify.unify var expectedVar
                if ok
                    then do
                        recordRegionVar state region var
                        return (Nothing, state)
                    else do
                        vt <- variableToType var
                        et <- variableToType expectedVar
                        let hint = resultMismatchHint vt et
                        return (Just $ posPrefix region ++ "Variable '" ++ name
                                ++ "' type mismatch: " ++ showType vt
                                ++ " vs " ++ showType et ++ hint, state)
            Nothing -> do
                -- Unknown variable — create a fresh flex var and add to env
                freshVar <- UF.fresh (T.Descriptor (T.FlexVar (Just name)) (_rank state) T.noMark Nothing)
                let state' = state { _env = Map.insert name freshVar (_env state) }
                return (Nothing, state')

    T.CForeign region name annot expected -> do
        (instVar, freshVars, quants) <- instantiateAnnotation state annot
        expectedVar <- expectedToVar state expected
        ok <- Unify.unify instVar expectedVar
        if ok
            then do
                -- v0.13 Phase A1: capture this polymorphic reference for
                -- monomorphisation.  Skip when there are no quantified
                -- vars (the call site is already concrete).
                unless (null freshVars) $
                    modifyIORef' (_callInstances state) $
                        \xs -> CallInstanceRecord region name freshVars quants : xs
                recordRegionVar state region instVar
                return (Nothing, state)
            else do
                instType <- variableToType instVar
                expType <- variableToType expectedVar
                -- v0.10.0: foreign mismatches are now FATAL. Was
                -- silently swallowed (`Continue past foreign mismatch
                -- for now`) which let FFI return-shape bugs through —
                -- e.g. `Regexp.regexpMatchString re s : Result Error
                -- Bool` being used as bare Bool ran fine at HM time
                -- and panicked at runtime with `rt.AsBool: expected
                -- bool, got rt.SkyResult[interface {},bool]`. Same
                -- pattern as the dep-HM-fatal change.
                let hint = resultMismatchHint instType expType
                return (Just $ "Foreign '" ++ name ++ "': "
                        ++ showType instType ++ " vs " ++ showType expType
                        ++ hint
                       , state)

    T.CPattern _region _category _actualType _expected ->
        return (Nothing, state)

    T.CLet _rigids _flexVars header headerCon bodyCon -> do
        -- Solve header constraint first
        (headerErr, state1) <- solveHelp state headerCon
        case headerErr of
            Just _ -> return (headerErr, state1)
            Nothing -> do
                -- Convert header types to UF variables (using shared cache!)
                headerVars <- mapM (\(name, (_, ty)) -> do
                    var <- typeToVar state1 ty
                    return (name, var)) (Map.toList header)
                -- Save the current bindings for the names we're about to
                -- shadow. After solving the body we restore them — without
                -- this, lambda / let / case-arm names leak into the global
                -- env and the next declaration's `Just n ->` pattern binds
                -- to a stale `n` from an unrelated scope.
                let savedBindings =
                        [ (name, Map.lookup name (_env state1))
                        | (name, _) <- headerVars
                        ]
                    state2 = state1
                        { _env = foldr (\(name, var) e -> Map.insert name var e)
                                       (_env state1) headerVars
                        }
                (bodyErr, state3) <- solveHelp state2 bodyCon
                -- Capture each header name's resolved type BEFORE we
                -- restore the outer env. This is the only hook where
                -- local-binding types are known to the solver; LSP
                -- hover relies on this for let / lambda param hovers.
                -- Store the UF variables (not resolved types) so they
                -- can be re-read at the end of solving when all
                -- constraints have been processed and vars are fully
                -- resolved. Storing types eagerly here loses solutions
                -- from constraints solved after the CLet scope exits.
                mapM_ (\(name, var) ->
                    modifyIORef' (_locals state3)
                        (Map.insertWith (\new old -> new ++ old) name [var]))
                    headerVars
                -- Restore outer scope's env for the shadowed names.
                let restoredEnv = foldr restoreBinding (_env state3) savedBindings
                    restoreBinding (name, Just old) e = Map.insert name old e
                    restoreBinding (name, Nothing)  e = Map.delete name e
                return (bodyErr, state3 { _env = restoredEnv })


-- | Solve a list of constraints sequentially
solveAll :: SolverState -> [T.Constraint] -> IO (Maybe String, SolverState)
solveAll state [] = return (Nothing, state)
solveAll state (c:cs) = do
    (err, state') <- solveHelp state c
    case err of
        Just _ -> return (err, state')
        Nothing -> solveAll state' cs


-- ═══════════════════════════════════════════════════════════
-- READ SOLVED TYPES
-- ═══════════════════════════════════════════════════════════

-- | v0.15.x P37a: returns the bare env map.  The full `SolvedTypes`
-- record is constructed at the solver-entry callers (`solve` etc.)
-- which know whether to attach the region map (only
-- `solveWithInstancesAndRegions` does — `solve`/`solveWithLocals`
-- leave it empty).
readSolvedTypes :: Map.Map String T.Variable -> IO (Map.Map String T.Type)
readSolvedTypes env =
    Map.traverseWithKey (\_ var -> variableToType var) env


variableToType :: T.Variable -> IO T.Type
variableToType = variableToTypeSeen []


-- | Convert a UF variable to T.Type, defending against cycles in
-- the union-find graph. When `unify` merges a FlexVar with a
-- Structure that transitively contains the same FlexVar (a missed
-- occurs check), the UF graph develops a self-loop. Without this
-- guard, recursive descent through App1/Fun1/Tuple1 args loops
-- forever and OOMs the host — the symptom is heap-exhaustion in
-- the v0.13 dep-fixpoint round-1 solve when mutually-recursive
-- ADT types (Element/Attribute) leak through cross-module
-- externals.
--
-- Cycle detection uses representative equivalence (`UF.equivalent`)
-- along the depth-first path. When we re-encounter a var on the
-- current path, return a `TVar "_cycle"` sentinel — downstream
-- consumers (codegen, generaliseToAnnotation) treat this as an
-- opaque any-typed slot and emit safe fall-back routing.
variableToTypeSeen :: [T.Variable] -> T.Variable -> IO T.Type
variableToTypeSeen seen var = do
    cyc <- anyEquivSeen seen var
    if cyc
        then return (T.TVar "_cycle")
        else do
            desc <- UF.get var
            case T._content desc of
                T.FlexVar (Just name) -> return (T.TVar name)
                T.FlexVar Nothing -> return (T.TVar "_")
                T.FlexSuper T.Number _ -> return (T.TType ModuleName.basics "Int" [])
                T.FlexSuper _ _ -> return (T.TVar "_super")
                T.RigidVar name -> return (T.TVar name)
                T.RigidSuper _ name -> return (T.TVar name)
                T.Structure flat -> flatTypeToTypeSeen (var : seen) flat
                T.Alias home name pairVars realVar -> do
                    inner <- variableToTypeSeen (var : seen) realVar
                    -- Read back the type-arg pair vars too — parametric
                    -- aliases (post Surface 1 / Canonicalise expansion)
                    -- carry their bindings here.  Pre-fix this was `[]`
                    -- which made `Cfg Msg` and `Cfg Int` look identical
                    -- in error messages ("Cfg vs Cfg") and made
                    -- `typeStructEq`'s arg comparison meaningless.
                    pairTys <- mapM (\(n, pv) -> do
                                        pty <- variableToTypeSeen (var : seen) pv
                                        return (n, pty)) pairVars
                    return (T.TAlias home name pairTys (T.Filled inner))
                T.Error -> return (T.TVar "_error")


anyEquivSeen :: [T.Variable] -> T.Variable -> IO Bool
anyEquivSeen [] _ = return False
anyEquivSeen (v : vs) target = do
    eq <- UF.equivalent v target
    if eq then return True else anyEquivSeen vs target


flatTypeToType :: T.FlatType -> IO T.Type
flatTypeToType = flatTypeToTypeSeen []


flatTypeToTypeSeen :: [T.Variable] -> T.FlatType -> IO T.Type
flatTypeToTypeSeen seen flat = case flat of
    T.App1 home name argVars -> do
        argTypes <- mapM (variableToTypeSeen seen) argVars
        return (T.TType home name argTypes)
    T.Fun1 argVar resVar -> do
        argType <- variableToTypeSeen seen argVar
        resType <- variableToTypeSeen seen resVar
        return (T.TLambda argType resType)
    T.EmptyRecord1 ->
        return (T.TRecord Map.empty Nothing)
    T.Record1 fieldVars extVar -> do
        fieldTypes <- Map.traverseWithKey (\_ fVar -> do
            ty <- variableToTypeSeen seen fVar
            return (T.FieldType 0 ty)) fieldVars
        -- Preserve the row-extension variable so an OPEN record reads
        -- back open (not CLOSED) across the module boundary — without
        -- it, a `Can.Access`-derived open record param closes on the
        -- export boundary and rejects callers with extra fields.
        --
        -- NON-RECURSIVE on purpose: we look at the extension var's
        -- content one level deep ONLY. Recursing into a `Record1`
        -- extension (to flatten merged rows) risks an infinite loop
        -- when the union-find graph is cyclic — a record extension
        -- can transitively point back at itself after a chain of
        -- merges. A `Record1` (or any non-flex) extension is read
        -- back as CLOSED; that loses a little precision for the rare
        -- merged-extension case but can never hang. Only a still-free
        -- named `FlexVar` extension keeps the record OPEN.
        extDesc <- UF.get extVar
        case T._content extDesc of
            T.FlexVar (Just n)      -> return (T.TRecord fieldTypes (Just n))
            T.FlexSuper _ (Just n)  -> return (T.TRecord fieldTypes (Just n))
            _                       -> return (T.TRecord fieldTypes Nothing)
    T.Unit1 ->
        return T.TUnit
    T.Tuple1 aVar bVar mcVar -> do
        aType <- variableToTypeSeen seen aVar
        bType <- variableToTypeSeen seen bVar
        moreType <- case mcVar of
            Nothing -> return []
            Just cVar -> (:[]) <$> variableToTypeSeen seen cVar
        return (T.TTuple aType bType moreType)


-- ═══════════════════════════════════════════════════════════
-- TYPE DISPLAY
-- ═══════════════════════════════════════════════════════════


-- | When a unification failure has @Result Error _@ on one side and
-- a bare type on the other, append a one-line hint pointing the
-- user at the trust-boundary unwrap pattern. Per CLAUDE.md "every
-- FFI call returns Result Error T" — so this mismatch shape
-- usually means the user forgot to unwrap.
--
-- Returns @""@ when the hint isn't applicable (e.g. both sides
-- are Result-typed, or neither is). Empty string composes
-- harmlessly with the existing error string.
--
-- Detection is structural: look for @TType (Canonical "Sky.Core.
-- Result") "Result" [_, _]@ at the top of one side. Lambda /
-- arrow positions are skipped through to the result, since the
-- common builder shape is @(Builder -> Builder)@ vs
-- @(Result Error Builder -> Result Error Builder)@ — the
-- mismatch lives at the param position there.
resultMismatchHint :: T.Type -> T.Type -> String
resultMismatchHint a b
    | isResult a && not (isResult b)
        = "\nHint: " ++ showType a ++ " is the Sky shape of a "
       ++ "Go FFI call. Unwrap with `case x of Ok v -> ...` or "
       ++ "compose via `Result.andThen` / `Result.withDefault`."
    | not (isResult a) && isResult b
        = "\nHint: " ++ showType b ++ " is the Sky shape of a "
       ++ "Go FFI call. Unwrap with `case x of Ok v -> ...` or "
       ++ "compose via `Result.andThen` / `Result.withDefault`."
    -- Lambda / arrow pair: chase through the param position. The
    -- common pattern that surfaces here is a setter being piped
    -- via |> from a Result-producing builder — e.g.
    --   Stripe.newX () |> Stripe.xSetMode "..."
    -- where xSetMode : X -> Result Error X but |> hands it
    -- Result Error X. Unification then sees X (the param) vs
    -- Result Error X. We follow the lambdas to find the deepest
    -- Result vs bare clash.
    | T.TLambda ap ar <- a, T.TLambda bp br <- b
        = let pHint = resultMismatchHint ap bp
              rHint = resultMismatchHint ar br
          in if not (null pHint) then pHint
             else rHint
    | otherwise = ""
  where
    isResult :: T.Type -> Bool
    isResult (T.TType (ModuleName.Canonical mn) "Result" _) =
        mn == "Sky.Core.Result"
    isResult _ = False


-- | Public entry point: rename fresh type variables to a, b, c, ...
-- before rendering so hover/error messages don't leak solver-internal
-- names (t47, _carg61) that confuse users.
--
-- Renames are per-call, so two independent `showType` invocations
-- both start from `a`. For cross-hover consistency within a single
-- module use `showTypeWith` / `moduleRenaming` (audit P2-3).
showType :: T.Type -> String
showType ty = showTypeR (renameVars ty)

-- | Render a type using a pre-computed module-level renaming.
-- The same solver-level TVar (e.g. t108) keeps the same human
-- letter across every hover in the module, so a user reading
-- two hovers doesn't see t108 as 'a' here and 'b' there.
showTypeWith :: Map.Map String String -> T.Type -> String
showTypeWith rename ty = showTypeR (substVar (\n -> Map.findWithDefault n n rename) ty)


-- | Render a type-mismatch summary. When both sides are records that
-- share most fields (typical of TEA cfg shapes like Live.app's
-- `{init, update, view, subscriptions, routes, notFound}`), show
-- only the DIFFERING fields so the user can see the bug at a glance:
--
--   Type mismatch in field `update`:
--     expected: Msg -> { i : Int, n : Int } -> ...
--     actual:   Msg -> { i : Int, n : String } -> ...
--
-- Falls back to "<actual> vs <expected>" for non-record or single-
-- field mismatches.
renderTypeMismatch :: T.Type -> T.Type -> String
renderTypeMismatch a e = fst (renderTypeMismatchTagged a e)


-- | Like renderTypeMismatch but also reports whether the result is a
-- field-level diff (True) or a full type pair (False). Callers use
-- the tag to decide whether to suppress the `(from: ...)` trailer.
--
-- Recursively walks INTO the mismatching structure to surface the
-- leaf mismatch. For nested cases like
--   `Msg -> { i : Int, n : String } -> ...` vs
--   `Msg -> { i : Int, n : Int } -> ...`
-- the renderer walks: TLambda matches on the FROM (Msg = Msg),
-- recurses into the TO; that's another TLambda, matches its TO,
-- recurses into its FROM; that's a record with one differing field
-- `n`; emits a path like "param 2 → field `n`: String vs Int".
-- The user sees the exact leaf instead of two near-identical types.
renderTypeMismatchTagged :: T.Type -> T.Type -> (String, Bool)
renderTypeMismatchTagged actual expected =
    let renamed = renameVars (T.TTuple actual expected [])
    in case renamed of
        T.TTuple a e _ ->
            case findLeafMismatch [] a e of
                Just (path, leafA, leafE) ->
                    ( renderPathDiff path leafA leafE, True )
                Nothing -> shallowDiff a e
        _ -> (showTypeR actual ++ " vs " ++ showTypeR expected, False)
  where
    -- Fallback: same record-level diff as before (no recursive walk).
    shallowDiff a e = case (a, e) of
        (T.TRecord af _, T.TRecord ef _) ->
            let diffs = recordFieldDiff af ef
            in case diffs of
                [(name, aty, ety)] ->
                    ( "field `" ++ name ++ "`:\n" ++
                      "     expected: " ++ showTypeR ety ++ "\n" ++
                      "     actual:   " ++ showTypeR aty
                    , True )
                ds | not (null ds) && length ds <= 4 ->
                    ( "in " ++ show (length ds) ++ " field(s):\n" ++
                      concatMap (\(name, aty, ety) ->
                          "     " ++ name ++ ":\n" ++
                          "       expected: " ++ showTypeR ety ++ "\n" ++
                          "       actual:   " ++ showTypeR aty ++ "\n")
                          ds
                    , True )
                _ -> (showTypeR a ++ " vs " ++ showTypeR e, False)
        _ -> (showTypeR a ++ " vs " ++ showTypeR e, False)


-- | A path segment from the outer type down to the leaf mismatch.
data PathSeg
    = PsField !String       -- recordType -> field
    | PsParam !Int          -- lambda arg position (1-based)
    | PsReturn              -- lambda result
    | PsTupleElem !Int      -- tuple element (1-based)
    | PsArg !String !Int    -- type-constructor arg (e.g. `List Int` -> arg 1)
    deriving (Show)


-- | Walk two types in parallel until either finding a leaf where the
-- two differ structurally OR confirming they're equivalent. Returns
-- Just (path, leafActual, leafExpected) on mismatch.
findLeafMismatch :: [PathSeg] -> T.Type -> T.Type -> Maybe ([PathSeg], T.Type, T.Type)
findLeafMismatch path a e =
    case (a, e) of
        -- TVars always unify in HM; not a real mismatch leaf.
        (T.TVar _, _) -> Nothing
        (_, T.TVar _) -> Nothing
        -- Identical primitives — no mismatch.
        _ | typeStructEq a e -> Nothing
        -- Lambdas: recurse into FROM first (positional). If FROM
        -- matches, recurse into TO.
        (T.TLambda f1 t1, T.TLambda f2 t2) ->
            case findLeafMismatch (path ++ [PsParam (countParam path + 1)]) f1 f2 of
                Just r -> Just r
                Nothing -> findLeafMismatch (path ++ [PsReturn]) t1 t2
        -- Records: walk the field diff.
        (T.TRecord af _, T.TRecord ef _) ->
            let diffs = recordFieldDiff af ef
            in case diffs of
                [(name, aty, ety)] ->
                    -- Single differing field — recurse into it.
                    findLeafMismatch (path ++ [PsField name]) aty ety
                _ -> Just (path, a, e) -- multiple field diffs — stop here
        -- TType: same head + same arity → recurse into args.
        (T.TType _ n1 a1, T.TType _ n2 a2)
          | n1 == n2 && length a1 == length a2 ->
            firstDiffAt n1 (zip [1..] (zip a1 a2)) path
        -- Tuples.
        (T.TTuple x1 y1 zs1, T.TTuple x2 y2 zs2) | length zs1 == length zs2 ->
            firstDiffTuple (zip [1..] (zip (x1:y1:zs1) (x2:y2:zs2))) path
        -- Alias: unfold names — same name treated as equal at this
        -- layer; differing names fall through to the leaf.
        (T.TAlias _ n1 _ _, T.TAlias _ n2 _ _) | n1 == n2 -> Nothing
        -- Anything else is a leaf mismatch at the current path.
        _ -> Just (path, a, e)
  where
    countParam :: [PathSeg] -> Int
    countParam = length . filter (\seg -> case seg of PsParam _ -> True; _ -> False)

    firstDiffAt typeName parts pth = go parts
      where
        go [] = Nothing
        go ((i, (t1, t2)) : rest) =
            case findLeafMismatch (pth ++ [PsArg typeName i]) t1 t2 of
                Just r -> Just r
                Nothing -> go rest

    firstDiffTuple parts pth = go parts
      where
        go [] = Nothing
        go ((i, (t1, t2)) : rest) =
            case findLeafMismatch (pth ++ [PsTupleElem i]) t1 t2 of
                Just r -> Just r
                Nothing -> go rest


-- | Render a path + leaf-types diff.
--
-- Example output for a 3-segment path:
--   in `update` → param 2 → field `n`:
--        expected: Int
--        actual:   String
renderPathDiff :: [PathSeg] -> T.Type -> T.Type -> String
renderPathDiff path actual expected =
    -- The caller prepends `"Type mismatch: "`. For a top-level
    -- mismatch (no path), skip straight to the expected/actual
    -- pair so we never produce a stray `Type mismatch: :` line.
    -- For a deeper mismatch, lead with `in <path>:` so the user
    -- sees where in the structure the divergence is.
    let header = case path of
            [] -> ""
            _  -> "in " ++ joinPath path ++ ":"
        renderedLeaf =
              "\n     expected: " ++ showTypeR expected
           ++ "\n     actual:   " ++ showTypeR actual
    in header ++ renderedLeaf
  where
    joinPath = List.intercalate " → " . map segText

    segText (PsField n)     = "field `" ++ n ++ "`"
    segText (PsParam i)     = "param " ++ show i
    segText PsReturn        = "return"
    segText (PsTupleElem i) = "tuple element " ++ show i
    segText (PsArg ty i)    = "`" ++ ty ++ "` arg " ++ show i


-- | Diff two record field maps. Returns (name, actualType,
-- expectedType) tuples for fields that DIFFER (present on both sides
-- with different types) or are MISSING from one side. Same-typed
-- fields are dropped — they don't help the user understand the bug.
recordFieldDiff :: Map.Map String T.FieldType -> Map.Map String T.FieldType
                -> [(String, T.Type, T.Type)]
recordFieldDiff af ef =
    let allKeys = List.sort (List.nub (Map.keys af ++ Map.keys ef))
        diffOne k = case (Map.lookup k af, Map.lookup k ef) of
            (Just (T.FieldType _ at), Just (T.FieldType _ et))
                | typeStructEq at et -> Nothing
                | otherwise          -> Just (k, at, et)
            (Just (T.FieldType _ at), Nothing) -> Just (k, at, T.TVar "<missing>")
            (Nothing, Just (T.FieldType _ et)) -> Just (k, T.TVar "<missing>", et)
            (Nothing, Nothing) -> Nothing
    in [d | Just d <- map diffOne allKeys]


-- | Structural type equality at the rendering layer (ignores TVar
-- naming differences after our renaming pass — bare TVars unify
-- with anything in HM, so they're not a useful "difference" signal
-- for users reading error messages).
typeStructEq :: T.Type -> T.Type -> Bool
typeStructEq (T.TVar _) (T.TVar _) = True
typeStructEq T.TUnit T.TUnit = True
typeStructEq (T.TType _ n1 a1) (T.TType _ n2 a2) =
    n1 == n2 && length a1 == length a2 && and (zipWith typeStructEq a1 a2)
typeStructEq (T.TLambda f1 t1) (T.TLambda f2 t2) =
    typeStructEq f1 f2 && typeStructEq t1 t2
typeStructEq (T.TRecord f1 _) (T.TRecord f2 _) =
    Map.keysSet f1 == Map.keysSet f2
    && and [ case (Map.lookup k f1, Map.lookup k f2) of
                (Just (T.FieldType _ t1), Just (T.FieldType _ t2)) ->
                    typeStructEq t1 t2
                _ -> False
           | k <- Map.keys f1 ]
typeStructEq (T.TTuple a1 b1 cs1) (T.TTuple a2 b2 cs2) =
    typeStructEq a1 a2 && typeStructEq b1 b2
    && length cs1 == length cs2 && and (zipWith typeStructEq cs1 cs2)
-- Parametric aliases must compare type-args too: `Cfg Msg` and
-- `Cfg Int` share the alias name but are distinct types. Pre-fix
-- this returned True for any matching name, so error-message diff
-- generation skipped the actual type-arg mismatch.
typeStructEq (T.TAlias _ n1 p1 _) (T.TAlias _ n2 p2 _) =
    n1 == n2 && length p1 == length p2
    && and (zipWith typeStructEq (map snd p1) (map snd p2))
typeStructEq _ _ = False

-- | Build a stable per-module renaming from all types that will be
-- displayed (top-level function signatures + local binding types
-- + anything else the LSP index wants to cache). The returned map
-- assigns consecutive human-readable names in first-occurrence
-- order. Call sites pass the result to `showTypeWith`.
moduleRenaming :: [T.Type] -> Map.Map String String
moduleRenaming tys =
    let allNames = nubOrdered (concatMap collectVarNames tys)
    in Map.fromList (zip allNames humanNames)
  where
    nubOrdered = foldr (\x acc -> if x `elem` acc then acc else x : acc) []

-- | Render with already-renamed vars (used internally so lambda bodies
-- reuse the parent's renaming).
showTypeR :: T.Type -> String
showTypeR ty = case ty of
    T.TVar name -> name
    T.TUnit -> "()"
    T.TType _ name [] -> name
    T.TType _ name args -> name ++ " " ++ unwords (map showTypeAtomR args)
    T.TLambda from to -> showTypeAtomR from ++ " -> " ++ showTypeR to
    -- Render records with their field names + types up to a sane
    -- limit. Pre-fix this was `{ ... }`, which made every record-
    -- vs-record mismatch indistinguishable in error messages —
    -- particularly painful for TEA cfg shapes (Live.app, Tui.app,
    -- Cli.program) where the closed-record kernel sigs surface
    -- exactly this kind of error when a required field is missing
    -- or wrong-shaped. Limitation #19. Caps fields at 6 to keep
    -- diagnostics readable; longer records get a trailing ", ...".
    T.TRecord fields ext -> showRecord fields ext
    T.TTuple a b _ -> "( " ++ showTypeR a ++ ", " ++ showTypeR b ++ " )"
    -- Parametric aliases (post Surface 1 in Canonicalise/Module.hs)
    -- carry their type-arg bindings in `pairs`. Render them so the
    -- error message stays informative: `Cfg Msg` not just `Cfg`.
    T.TAlias _ name pairs _
        | null pairs -> name
        | otherwise  -> name ++ " " ++ unwords (map (showTypeAtomR . snd) pairs)


-- Render the fields of a record. Extension marker (`| r`) shown
-- when the record is row-polymorphic.
showRecord :: Map.Map String T.FieldType -> Maybe String -> String
showRecord fields ext =
    let pairs = Map.toAscList fields
        renderField (n, T.FieldType _ t) = n ++ " : " ++ showTypeR t
        keep   = take 6 pairs
        more   = length pairs - length keep
        body   = if null pairs
                 then ""
                 else " " ++ unwords (intersperseCommas (map renderField keep))
                       ++ (if more > 0 then ", ..." else "")
                       ++ " "
        extStr = case ext of
                   Just _  -> "| ..."
                   Nothing -> ""
    in "{" ++ body ++ extStr ++ "}"
  where
    intersperseCommas []     = []
    intersperseCommas [x]    = [x]
    intersperseCommas (x:xs) = (x ++ ",") : intersperseCommas xs


showTypeAtomR :: T.Type -> String
showTypeAtomR ty = case ty of
    T.TVar name -> name
    T.TType _ name [] -> name
    T.TUnit -> "()"
    _ -> "(" ++ showTypeR ty ++ ")"


-- | Collect TVar names in left-to-right occurrence order and rewrite
-- them to a, b, c, ... This gives stable user-facing type sigs that
-- don't expose solver-internal fresh-name counters.
renameVars :: T.Type -> T.Type
renameVars ty =
    let names = collectVarNames ty
        rename = Map.fromList (zip names humanNames)
        sub n = Map.findWithDefault n n rename
    in substVar sub ty

collectVarNames :: T.Type -> [String]
collectVarNames = nubOrdered . go
  where
    go (T.TVar n) = [n]
    go (T.TType _ _ args) = concatMap go args
    go (T.TLambda a b) = go a ++ go b
    go (T.TTuple a b cs) = concatMap go (a : b : cs)
    go (T.TAlias _ _ subs b) = concatMap (go . snd) subs ++ goAlias b
    -- TRecord previously fell through `_ -> []` so record field
    -- types' TVars (notably the solver-generated `_rfld_*` names
    -- that emerge from constraintRecord) were never collected for
    -- renaming. Result: error messages and hover signatures that
    -- showed `{ count : _rfld_count12 }` instead of `{ count : Int }`
    -- (or `{ count : a }` for legitimately polymorphic fields).
    -- Walk into every field's type AND the row-extension variable.
    go (T.TRecord fields ext) =
        concatMap (\(_, T.FieldType _ t) -> go t) (Map.toList fields)
        ++ maybe [] (\n -> [n]) ext
    go _ = []
    goAlias (T.Hoisted t) = go t
    goAlias (T.Filled t)  = go t
    nubOrdered = foldr (\x acc -> if x `elem` acc then acc else x : acc) []

substVar :: (String -> String) -> T.Type -> T.Type
substVar f ty = case ty of
    T.TVar n -> T.TVar (f n)
    T.TType m n args -> T.TType m n (map (substVar f) args)
    T.TLambda a b -> T.TLambda (substVar f a) (substVar f b)
    T.TTuple a b cs -> T.TTuple (substVar f a) (substVar f b) (map (substVar f) cs)
    T.TAlias m n subs body ->
        T.TAlias m n [(k, substVar f v) | (k, v) <- subs] (substAlias f body)
    -- TRecord — substitute into every field's type AND the row-
    -- extension variable. Same bug as collectVarNames above.
    T.TRecord fields ext ->
        let fields' = Map.map (\(T.FieldType i t) -> T.FieldType i (substVar f t)) fields
            ext' = fmap f ext
        in T.TRecord fields' ext'
    other -> other
  where
    substAlias g (T.Hoisted t) = T.Hoisted (substVar g t)
    substAlias g (T.Filled t)  = T.Filled  (substVar g t)

-- | Infinite "a, b, c, ..., z, a1, b1, ..." sequence.
humanNames :: [String]
humanNames =
    [[c] | c <- ['a' .. 'z']]
    ++ [ [c] ++ show (n :: Int) | n <- [1..], c <- ['a' .. 'z'] ]


showExpected :: T.Expected T.Type -> String
showExpected (T.NoExpectation ty) = showType ty
showExpected (T.FromContext _ _ ty) = showType ty
showExpected (T.FromAnnotation _ _ _ ty) = showType ty


-- ═══════════════════════════════════════════════════════════
-- v0.13 LAYER 1 — Diagnostic conversion
-- ═══════════════════════════════════════════════════════════

-- | Convert a legacy String solver error to a structured Diagnostic.
--
-- The solver currently emits errors as strings with a `LINE:COL:`
-- prefix and a "Type mismatch: ..." body. This converter parses the
-- prefix to extract a Region and wraps the rest as a Diagnostic with
-- code E2001.
--
-- Future Layer 1 work moves the solver itself to produce Diagnostic
-- values directly (eliminating the parse-then-rewrap step). For now
-- this lets Compile.hs render type errors via the new Sky.Reporting
-- pipeline without changing every error-emission site at once.
solveErrorToDiagnostic :: FilePath -> String -> Diag.Diagnostic
solveErrorToDiagnostic path err =
    let (region, body) = parsePrefix err
    in Diag.mkError path region Diag.CatType Diag.typeE_Mismatch body


parsePrefix :: String -> (A.Region, String)
parsePrefix s =
    case break (== ':') s of
        (lineStr, ':':rest)
          | not (null lineStr), all isDigit lineStr ->
            case break (== ':') rest of
                (colStr, ':':body)
                  | not (null colStr), all isDigit colStr ->
                    let l = read lineStr
                        c = read colStr
                        region = A.Region (A.Position l c) (A.Position l c)
                    in (region, dropWhile (== ' ') body)
                _ -> (synthetic, s)
        _ -> (synthetic, s)
  where
    isDigit c = c >= '0' && c <= '9'
    synthetic = A.Region (A.Position 1 1) (A.Position 1 1)
