-- | Constraint solver for Sky's Hindley-Milner type inference.
-- Walks the constraint tree, unifying types via UnionFind.
-- Adapted from Elm's Type.Solve (simplified — no rank-based generalization yet).
module Sky.Type.Solve
    ( solve
    , SolveResult(..)
    )
    where

import qualified Data.Map.Strict as Map
import qualified Sky.Type.Type as T
import qualified Sky.Type.UnionFind as UF
import qualified Sky.Type.Unify as Unify
import qualified Sky.Type.Instantiate as Instantiate
import qualified Sky.Sky.ModuleName as ModuleName


-- | Result of solving constraints
data SolveResult
    = SolveOk !SolvedTypes
    | SolveError String  -- error message
    deriving (Show)


-- | Solved type environment: maps variable names to their resolved types
type SolvedTypes = Map.Map String T.Type


-- | Solver state: maps variable names to their UF variables
type SolverEnv = Map.Map String T.Variable


-- | Solve a constraint tree. Returns Ok with solved types, or an error.
solve :: T.Constraint -> IO SolveResult
solve constraint = do
    (result, env) <- solveHelp Map.empty 0 constraint
    case result of
        Nothing -> do
            -- Read solved types from UF variables
            solvedTypes <- readSolvedTypes env
            return (SolveOk solvedTypes)
        Just err -> return (SolveError err)


-- | Solve a constraint in the given environment.
-- Returns (Nothing, env) on success or (Just error, env) on failure.
solveHelp :: SolverEnv -> Int -> T.Constraint -> IO (Maybe String, SolverEnv)
solveHelp env rank constraint = case constraint of

    T.CTrue ->
        return (Nothing, env)

    T.CSaveTheEnvironment ->
        return (Nothing, env)

    T.CAnd constraints ->
        solveAll env rank constraints

    T.CEqual _region _category actualType expected -> do
        actualVar <- Instantiate.fromCanType rank actualType
        expectedVar <- expectedToVar env rank expected
        ok <- Unify.unify actualVar expectedVar
        if ok
            then return (Nothing, env)
            else return (Just $ "Type mismatch: " ++ showType actualType ++ " vs " ++ showExpected expected, env)

    T.CLocal _region name expected -> do
        case Map.lookup name env of
            Just var -> do
                expectedVar <- expectedToVar env rank expected
                ok <- Unify.unify var expectedVar
                if ok
                    then return (Nothing, env)
                    else return (Just $ "Variable '" ++ name ++ "' type mismatch", env)
            Nothing ->
                -- Unknown variable — create a fresh flex var and add to env
                do  freshVar <- UF.fresh (T.Descriptor (T.FlexVar (Just name)) rank T.noMark Nothing)
                    return (Nothing, Map.insert name freshVar env)

    T.CForeign _region name annot expected -> do
        (instVar, _freshVars) <- Instantiate.fromAnnotation rank annot
        expectedVar <- expectedToVar env rank expected
        ok <- Unify.unify instVar expectedVar
        if ok
            then return (Nothing, env)
            else return (Just $ "Foreign '" ++ name ++ "' type mismatch", env)

    T.CPattern _region _category _actualType _expected ->
        return (Nothing, env)

    T.CLet rigids flexVars header headerCon bodyCon -> do
        (headerErr, env1) <- solveHelp env rank headerCon
        case headerErr of
            Just _ -> return (headerErr, env1)
            Nothing -> do
                headerVars <- mapM (\(_, (_, ty)) -> Instantiate.fromCanType rank ty) (Map.toList header)
                let headerEntries = zipWith (\(name, _) var -> (name, var))
                        (Map.toList header) headerVars
                    env' = foldr (\(name, var) e -> Map.insert name var e) env1 headerEntries
                solveHelp env' rank bodyCon


-- | Solve a list of constraints sequentially
solveAll :: SolverEnv -> Int -> [T.Constraint] -> IO (Maybe String, SolverEnv)
solveAll env _rank [] = return (Nothing, env)
solveAll env rank (c:cs) = do
    (err, env') <- solveHelp env rank c
    case err of
        Just _ -> return (err, env')
        Nothing -> solveAll env' rank cs


-- | Convert an Expected type to a UF variable
expectedToVar :: SolverEnv -> Int -> T.Expected T.Type -> IO T.Variable
expectedToVar _env rank expected = case expected of
    T.NoExpectation ty ->
        Instantiate.fromCanType rank ty
    T.FromContext _region _context ty ->
        Instantiate.fromCanType rank ty
    T.FromAnnotation _name _arity _subCtx ty ->
        Instantiate.fromCanType rank ty


-- ═══════════════════════════════════════════════════════════
-- TYPE DISPLAY (for error messages)
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


-- ═══════════════════════════════════════════════════════════
-- READ SOLVED TYPES
-- ═══════════════════════════════════════════════════════════

-- | Read solved types from UF variables back to canonical types
readSolvedTypes :: SolverEnv -> IO SolvedTypes
readSolvedTypes env =
    Map.traverseWithKey (\_ var -> variableToType var) env


-- | Convert a UF variable back to a canonical type (reading its resolved content)
variableToType :: T.Variable -> IO T.Type
variableToType var = do
    desc <- UF.get var
    case T._content desc of
        T.FlexVar (Just name) -> return (T.TVar name)
        T.FlexVar Nothing -> return (T.TVar "_")
        T.FlexSuper T.Number _ -> return (T.TType (ModuleName.Canonical "Sky.Core.Basics") "Int" [])
        T.FlexSuper _ _ -> return (T.TVar "_super")
        T.RigidVar name -> return (T.TVar name)
        T.RigidSuper _ name -> return (T.TVar name)
        T.Structure flat -> flatTypeToType flat
        T.Alias home name _ realVar -> do
            inner <- variableToType realVar
            return (T.TAlias home name [] (T.Filled inner))
        T.Error -> return (T.TVar "_error")


-- | Convert a FlatType back to a canonical type
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
        fieldTypes <- Map.traverseWithKey (\name fVar -> do
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
