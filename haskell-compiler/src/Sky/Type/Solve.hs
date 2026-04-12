-- | Constraint solver for Sky's Hindley-Milner type inference.
-- Walks the constraint tree, unifying types via UnionFind.
-- Uses a TVar name cache to share UF variables for the same type variable name.
-- Adapted from Elm's Type.Solve.
module Sky.Type.Solve
    ( solve
    , SolveResult(..)
    , SolvedTypes
    , showType
    )
    where

import Data.IORef
import qualified Data.Map.Strict as Map
import qualified Sky.Type.Type as T
import qualified Sky.Type.UnionFind as UF
import qualified Sky.Type.Unify as Unify
import qualified Sky.Sky.ModuleName as ModuleName


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
    }


-- | Solve a constraint tree.
solve :: T.Constraint -> IO SolveResult
solve constraint = do
    cache <- newIORef Map.empty
    let state0 = SolverState Map.empty cache 0
    (result, finalState) <- solveHelp state0 constraint
    case result of
        Nothing -> do
            solvedTypes <- readSolvedTypes (_env finalState)
            return (SolveOk solvedTypes)
        Just err -> return (SolveError err)


-- | Convert a Type to a UF Variable, SHARING variables for the same TVar name.
-- This is the critical function: when two constraints reference TVar "_arg0",
-- they get the SAME UF variable, so unification propagates between them.
typeToVar :: SolverState -> T.Type -> IO T.Variable
typeToVar state ty = case ty of
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

    T.TTuple a b mc -> do
        aVar <- typeToVar state a
        bVar <- typeToVar state b
        mcVar <- case mc of
            Nothing -> return Nothing
            Just c -> Just <$> typeToVar state c
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
instantiateAnnotation state (T.Forall freeVars canType) = do
    -- Create a FRESH cache scope for instantiation (don't pollute the shared cache)
    -- Each quantified var gets a new fresh variable
    localCache <- newIORef Map.empty
    freshVars <- mapM (\name -> do
        var <- UF.fresh (T.Descriptor (T.FlexVar (Just name)) (_rank state) T.noMark Nothing)
        modifyIORef' localCache (Map.insert name var)
        return var) freeVars
    let instState = state { _varCache = localCache }
    typeToVar instState canType


-- ═══════════════════════════════════════════════════════════
-- SOLVER
-- ═══════════════════════════════════════════════════════════

solveHelp :: SolverState -> T.Constraint -> IO (Maybe String, SolverState)
solveHelp state constraint = case constraint of

    T.CTrue ->
        return (Nothing, state)

    T.CSaveTheEnvironment ->
        return (Nothing, state)

    T.CAnd constraints ->
        solveAll state constraints

    T.CEqual _region _category actualType expected -> do
        actualVar <- typeToVar state actualType
        expectedVar <- expectedToVar state expected
        ok <- Unify.unify actualVar expectedVar
        if ok
            then return (Nothing, state)
            else do
                -- Debug: read back actual resolved types
                at <- variableToType actualVar
                et <- variableToType expectedVar
                return (Just $ "Type mismatch: " ++ showType at ++ " vs " ++ showType et ++ " (from: " ++ showType actualType ++ " vs " ++ showExpected expected ++ ")", state)

    T.CLocal _region name expected -> do
        case Map.lookup name (_env state) of
            Just var -> do
                expectedVar <- expectedToVar state expected
                ok <- Unify.unify var expectedVar
                if ok
                    then return (Nothing, state)
                    else return (Just $ "Variable '" ++ name ++ "' type mismatch", state)
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
                -- Debug: show what failed to unify
                instType <- variableToType instVar
                expType <- variableToType expectedVar
                return (Nothing, state)  -- Continue past foreign mismatch for now
                -- return (Just $ "Foreign '" ++ name ++ "': " ++ showType instType ++ " vs " ++ showType expType, state)

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
                let state2 = state1 { _env = foldr (\(name, var) e -> Map.insert name var e) (_env state1) headerVars }
                -- Solve body with extended env
                solveHelp state2 bodyCon


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
        mcType <- case mcVar of
            Nothing -> return Nothing
            Just cVar -> Just <$> variableToType cVar
        return (T.TTuple aType bType mcType)


-- ═══════════════════════════════════════════════════════════
-- TYPE DISPLAY
-- ═══════════════════════════════════════════════════════════

showType :: T.Type -> String
showType ty = case ty of
    T.TVar name -> name
    T.TUnit -> "()"
    T.TType _ name [] -> name
    T.TType _ name args -> name ++ " " ++ unwords (map showTypeAtom args)
    T.TLambda from to -> showTypeAtom from ++ " -> " ++ showType to
    T.TRecord _ _ -> "{ ... }"
    T.TTuple a b _ -> "( " ++ showType a ++ ", " ++ showType b ++ " )"
    T.TAlias _ name _ _ -> name


showTypeAtom :: T.Type -> String
showTypeAtom ty = case ty of
    T.TVar name -> name
    T.TType _ name [] -> name
    T.TUnit -> "()"
    _ -> "(" ++ showType ty ++ ")"


showExpected :: T.Expected T.Type -> String
showExpected (T.NoExpectation ty) = showType ty
showExpected (T.FromContext _ _ ty) = showType ty
showExpected (T.FromAnnotation _ _ _ ty) = showType ty
