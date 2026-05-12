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
    , SolveResult(..)
    , SolvedTypes
    , showType
    , showTypeWith
    , moduleRenaming
    )
    where

import Data.IORef
import qualified Data.List as List
import qualified Data.Map.Strict as Map
import qualified Sky.Type.Type as T
import qualified Sky.Type.UnionFind as UF
import qualified Sky.Type.Unify as Unify
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Reporting.Annotation as A
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


-- | Solved type environment: maps variable names to their resolved types
type SolvedTypes = Map.Map String T.Type


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
    }


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
    let state0 = SolverState Map.empty cache 0 locals steps budget
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
            return (SolveOk merged)
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
    let state0 = SolverState Map.empty cache 0 locals steps budget
    (result, finalState) <- solveHelp state0 constraint
    localVars <- readIORef (_locals finalState)
    localTypes <- Map.traverseWithKey (\_ vars ->
        mapM variableToType vars) localVars
    case result of
        Nothing -> do
            solvedTypes <- readSolvedTypes (_env finalState)
            return (SolveOk solvedTypes, localTypes)
        Just err ->
            return (SolveError err, localTypes)


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


-- | Instantiate an Annotation into a UF Variable (fresh vars for quantified names)
instantiateAnnotation :: SolverState -> T.Annotation -> IO T.Variable
instantiateAnnotation state (T.Forall freeVars canType)
    -- No quantification: use the GLOBAL cache for sharing
    | null freeVars = typeToVar state canType
    -- With quantification: create a LOCAL cache so each use gets fresh vars
    | otherwise = do
        localCache <- newIORef Map.empty
        mapM_ (\name -> do
            var <- UF.fresh (T.Descriptor (T.FlexVar (Just name)) (_rank state) T.noMark Nothing)
            modifyIORef' localCache (Map.insert name var)) freeVars
        let instState = state { _varCache = localCache }
        typeToVar instState canType


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
            then return (Nothing, state)
            else do
                -- Debug: read back actual resolved types
                at <- variableToType actualVar
                et <- variableToType expectedVar
                let hint = resultMismatchHint at et
                return (Just $ posPrefix region ++ "Type mismatch: " ++ showType at ++ " vs " ++ showType et ++ " (from: " ++ showType actualType ++ " vs " ++ showExpected expected ++ ")" ++ hint, state)

    T.CLocal region name expected -> do
        case Map.lookup name (_env state) of
            Just var -> do
                expectedVar <- expectedToVar state expected
                ok <- Unify.unify var expectedVar
                if ok
                    then return (Nothing, state)
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

    T.CForeign _region name annot expected -> do
        instVar <- instantiateAnnotation state annot
        expectedVar <- expectedToVar state expected
        ok <- Unify.unify instVar expectedVar
        if ok
            then return (Nothing, state)
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

readSolvedTypes :: Map.Map String T.Variable -> IO SolvedTypes
readSolvedTypes env =
    Map.traverseWithKey (\_ var -> variableToType var) env


variableToType :: T.Variable -> IO T.Type
variableToType var = do
    desc <- UF.get var
    case T._content desc of
        T.FlexVar (Just name) -> return (T.TVar name)
        T.FlexVar Nothing -> return (T.TVar "_")
        T.FlexSuper T.Number _ -> return (T.TType ModuleName.basics "Int" [])
        T.FlexSuper _ _ -> return (T.TVar "_super")
        T.RigidVar name -> return (T.TVar name)
        T.RigidSuper _ name -> return (T.TVar name)
        T.Structure flat -> flatTypeToType flat
        T.Alias home name _ realVar -> do
            inner <- variableToType realVar
            return (T.TAlias home name [] (T.Filled inner))
        T.Error -> return (T.TVar "_error")


flatTypeToType :: T.FlatType -> IO T.Type
flatTypeToType flat = case flat of
    T.App1 home name argVars -> do
        argTypes <- mapM variableToType argVars
        return (T.TType home name argTypes)
    T.Fun1 argVar resVar -> do
        argType <- variableToType argVar
        resType <- variableToType resVar
        return (T.TLambda argType resType)
    T.EmptyRecord1 ->
        return (T.TRecord Map.empty Nothing)
    T.Record1 fieldVars extVar -> do
        fieldTypes <- Map.traverseWithKey (\_ fVar -> do
            ty <- variableToType fVar
            return (T.FieldType 0 ty)) fieldVars
        return (T.TRecord fieldTypes Nothing)
    T.Unit1 ->
        return T.TUnit
    T.Tuple1 aVar bVar mcVar -> do
        aType <- variableToType aVar
        bType <- variableToType bVar
        moreType <- case mcVar of
            Nothing -> return []
            Just cVar -> (:[]) <$> variableToType cVar
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
    T.TAlias _ name _ _ -> name


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
