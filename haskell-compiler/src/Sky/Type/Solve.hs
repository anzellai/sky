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


-- | Result of solving constraints
data SolveResult
    = SolveOk
    | SolveError String  -- error message
    deriving (Show)


-- | Solver state: maps variable names to their UF variables
type SolverEnv = Map.Map String T.Variable


-- | Solve a constraint tree. Returns Ok or an error.
solve :: T.Constraint -> IO SolveResult
solve constraint = do
    result <- solveHelp Map.empty 0 constraint
    return result


-- | Solve a constraint in the given environment
solveHelp :: SolverEnv -> Int -> T.Constraint -> IO SolveResult
solveHelp env rank constraint = case constraint of

    T.CTrue ->
        return SolveOk

    T.CSaveTheEnvironment ->
        return SolveOk

    T.CAnd constraints ->
        solveAll env rank constraints

    T.CEqual _region _category actualType expected -> do
        -- Convert both types to variables and unify
        actualVar <- Instantiate.fromCanType rank actualType
        expectedVar <- expectedToVar env rank expected
        ok <- Unify.unify actualVar expectedVar
        if ok
            then return SolveOk
            else return $ SolveError $ "Type mismatch: " ++ showType actualType ++ " vs " ++ showExpected expected

    T.CLocal _region name expected -> do
        -- Look up the variable in the environment
        case Map.lookup name env of
            Just var -> do
                -- Copy the variable (instantiate if polymorphic)
                expectedVar <- expectedToVar env rank expected
                ok <- Unify.unify var expectedVar
                if ok
                    then return SolveOk
                    else return $ SolveError $ "Variable '" ++ name ++ "' type mismatch"
            Nothing ->
                -- Unknown variable — create a fresh flex var
                -- This allows forward references and unresolved names to proceed
                return SolveOk

    T.CForeign _region name annot expected -> do
        -- Instantiate the annotation (creates fresh vars for quantified names)
        (instVar, _freshVars) <- Instantiate.fromAnnotation rank annot
        expectedVar <- expectedToVar env rank expected
        ok <- Unify.unify instVar expectedVar
        if ok
            then return SolveOk
            else return $ SolveError $ "Foreign '" ++ name ++ "' type mismatch"

    T.CPattern _region _category _actualType _expected ->
        -- Pattern constraints: simplified for now
        return SolveOk

    T.CLet rigids flexVars header headerCon bodyCon -> do
        -- Simplified let solving (no generalization yet)
        -- 1. Solve header constraint
        headerResult <- solveHelp env rank headerCon
        case headerResult of
            SolveError _ -> return headerResult
            SolveOk -> do
                -- 2. Add header bindings to environment
                headerVars <- mapM (\(_, (_, ty)) -> Instantiate.fromCanType rank ty) (Map.toList header)
                let headerEntries = zipWith (\(name, _) var -> (name, var))
                        (Map.toList header) headerVars
                    env' = foldr (\(name, var) e -> Map.insert name var e) env headerEntries
                -- 3. Solve body constraint with extended environment
                solveHelp env' rank bodyCon


-- | Solve a list of constraints sequentially
solveAll :: SolverEnv -> Int -> [T.Constraint] -> IO SolveResult
solveAll _env _rank [] = return SolveOk
solveAll env rank (c:cs) = do
    result <- solveHelp env rank c
    case result of
        SolveError _ -> return result
        SolveOk -> solveAll env rank cs


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
