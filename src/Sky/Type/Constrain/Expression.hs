-- | Constraint generation from canonical expressions.
-- IO-based with a unique counter for type variable names.
-- Each call site gets unique placeholder names so the solver's
-- TVar cache shares variables WITHIN a definition but not ACROSS definitions.
module Sky.Type.Constrain.Expression
    ( constrainModule
    , Env
    )
    where

import Data.IORef
import qualified Data.Map.Strict as Map
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Type.Type as T
import qualified Sky.Sky.ModuleName as ModuleName


-- | Type environment: maps variable names to their type schemes
type Env = Map.Map String T.Annotation


-- | Fresh name counter
type Counter = IORef Int

freshName :: Counter -> String -> IO String
freshName counter prefix = do
    n <- readIORef counter
    modifyIORef' counter (+1)
    return (prefix ++ show n)


-- ═══════════════════════════════════════════════════════════
-- MODULE
-- ═══════════════════════════════════════════════════════════

-- | Generate constraints for an entire module (IO for fresh names)
constrainModule :: Can.Module -> IO T.Constraint
constrainModule canMod = do
    counter <- newIORef 0
    constrainDecls counter Map.empty (Can._decls canMod)


constrainDecls :: Counter -> Env -> Can.Decls -> IO T.Constraint
constrainDecls counter env decls = case decls of
    Can.SaveTheEnvironment ->
        return T.CTrue

    Can.Declare def rest -> do
        (defCon, name, defType) <- constrainDefWithType counter env def
        let env' = Map.insert name (T.Forall [] defType) env
        restCon <- constrainDecls counter env' rest
        -- Use CLet to introduce the def binding into the solver env for rest
        let defHeader = Map.singleton name (A.one, defType)
        return $ T.CLet [] [] defHeader defCon restCon

    Can.DeclareRec def defs rest -> do
        -- For recursive defs, we need the types first (for mutual references)
        -- Use defTypeInfoIO to pre-register, then constrainDef uses the SAME names
        let allDefs = def : defs
        -- Pre-generate type info and add to env
        defInfos <- mapM (defTypeInfoIO counter) allDefs
        let recEnv = foldr (\(n, t) e -> Map.insert n (T.Forall [] t) e) env defInfos
        -- Now constrain each def — constrainDef will generate its OWN type vars
        -- which are different from defInfos. We need them to share.
        -- Fix: pass the pre-generated type vars into constrainDef
        defCons <- zipWithM (\d (_, ty) -> constrainDefWithKnownType counter recEnv d ty) allDefs defInfos
        restCon <- constrainDecls counter recEnv rest
        return $ T.CAnd (defCons ++ [restCon])


-- ═══════════════════════════════════════════════════════════
-- EXPRESSIONS
-- ═══════════════════════════════════════════════════════════

-- | Generate constraints for an expression given an expected type.
constrain :: Counter -> Env -> Can.Expr -> T.Expected T.Type -> IO T.Constraint
constrain counter env (A.At region expr) expected = case expr of

    Can.VarLocal name ->
        return $ T.CLocal region name expected

    Can.VarTopLevel _home name ->
        return $ T.CLocal region name expected

    Can.VarKernel modName funcName ->
        case lookupKernelType modName funcName of
            Just annot -> return $ T.CForeign region (modName ++ "." ++ funcName) annot expected
            Nothing -> return T.CTrue

    Can.VarCtor _opts _home _typeName ctorName annot ->
        return $ T.CForeign region ctorName annot expected

    Can.Chr _ ->
        return $ T.CEqual region T.CChar charType expected

    Can.Str _ ->
        return $ T.CEqual region T.CString stringType expected

    Can.Int _ ->
        return $ T.CEqual region T.CNumber intType expected

    Can.Float _ ->
        return $ T.CEqual region T.CFloat floatType expected

    Can.Unit ->
        return $ T.CEqual region T.CRecord T.TUnit expected

    Can.List items ->
        constrainList counter env region items expected

    Can.Negate inner ->
        constrain counter env inner expected

    Can.Binop op _opHome _opName _annot left right ->
        constrainBinop counter env region op left right expected

    Can.Lambda params body ->
        constrainLambda counter env region params body expected

    Can.Call func args ->
        constrainCall counter env region func args expected

    Can.If branches elseExpr ->
        constrainIf counter env region branches elseExpr expected

    Can.Let def body ->
        constrainLet counter env def body expected

    Can.LetRec defs body ->
        constrainLetRec counter env defs body expected

    Can.LetDestruct pat valExpr body ->
        constrainLetDestruct counter env pat valExpr body expected

    Can.Case subject branches ->
        constrainCase counter env region subject branches expected

    Can.Accessor _field -> return T.CTrue
    Can.Access _target _ -> return T.CTrue
    Can.Update _ _ _ -> return T.CTrue
    Can.Record _ -> return T.CTrue
    Can.Tuple _ _ _ -> return T.CTrue


-- ═══════════════════════════════════════════════════════════
-- LIST
-- ═══════════════════════════════════════════════════════════

constrainList :: Counter -> Env -> T.Region -> [Can.Expr] -> T.Expected T.Type -> IO T.Constraint
constrainList counter env region items expected = do
    elemName <- freshName counter "_elem"
    let elemType = T.TVar elemName
        listType = T.TType ModuleName.list "List" [elemType]
    itemCons <- zipWithM (\i item ->
        constrain counter env item (T.FromContext region (T.ListEntry i) elemType))
        [0..] items
    return $ T.CAnd (itemCons ++ [T.CEqual region T.CList listType expected])


-- ═══════════════════════════════════════════════════════════
-- BINARY OPERATORS
-- ═══════════════════════════════════════════════════════════

constrainBinop :: Counter -> Env -> T.Region -> String -> Can.Expr -> Can.Expr -> T.Expected T.Type -> IO T.Constraint
constrainBinop counter env region op left right expected = do
    (leftType, rightType, resultType) <- binopTypes counter op
    leftCon <- constrain counter env left (T.NoExpectation leftType)
    rightCon <- constrain counter env right (T.NoExpectation rightType)
    return $ T.CAnd [leftCon, rightCon, T.CEqual region T.CApp resultType expected]


binopTypes :: Counter -> String -> IO (T.Type, T.Type, T.Type)
binopTypes counter op = case op of
    "+"  -> return (intType, intType, intType)
    "-"  -> return (intType, intType, intType)
    "*"  -> return (intType, intType, intType)
    "/"  -> return (floatType, floatType, floatType)
    "//" -> return (intType, intType, intType)
    "++" -> return (stringType, stringType, stringType)
    "==" -> do { n <- freshName counter "_cmp"; return (T.TVar n, T.TVar n, boolType) }
    "/=" -> do { n <- freshName counter "_cmp"; return (T.TVar n, T.TVar n, boolType) }
    "<"  -> return (intType, intType, boolType)
    ">"  -> return (intType, intType, boolType)
    "<=" -> return (intType, intType, boolType)
    ">=" -> return (intType, intType, boolType)
    "&&" -> return (boolType, boolType, boolType)
    "||" -> return (boolType, boolType, boolType)
    "|>" -> do { a <- freshName counter "_pa"; b <- freshName counter "_pb"; return (T.TVar a, T.TLambda (T.TVar a) (T.TVar b), T.TVar b) }
    "<|" -> do { a <- freshName counter "_pa"; b <- freshName counter "_pb"; return (T.TLambda (T.TVar a) (T.TVar b), T.TVar a, T.TVar b) }
    ">>" -> do { a <- freshName counter "_ca"; b <- freshName counter "_cb"; c <- freshName counter "_cc"; return (T.TLambda (T.TVar a) (T.TVar b), T.TLambda (T.TVar b) (T.TVar c), T.TLambda (T.TVar a) (T.TVar c)) }
    "<<" -> do { a <- freshName counter "_ca"; b <- freshName counter "_cb"; c <- freshName counter "_cc"; return (T.TLambda (T.TVar b) (T.TVar c), T.TLambda (T.TVar a) (T.TVar b), T.TLambda (T.TVar a) (T.TVar c)) }
    _    -> do
              a <- freshName counter "_opa"
              b <- freshName counter "_opb"
              r <- freshName counter "_opr"
              return (T.TVar a, T.TVar b, T.TVar r)


-- ═══════════════════════════════════════════════════════════
-- LAMBDA
-- ═══════════════════════════════════════════════════════════

constrainLambda :: Counter -> Env -> T.Region -> [Can.Pattern] -> Can.Expr -> T.Expected T.Type -> IO T.Constraint
constrainLambda counter env region params body expected = do
    paramTypes <- mapM (\_ -> do n <- freshName counter "_larg"; return (T.TVar n)) params
    resultName <- freshName counter "_lres"
    let resultType = T.TVar resultName
        paramBindings = concatMap patternBindings (zip params paramTypes)
        bodyEnv = foldr (\(n, ann) e -> Map.insert n ann e) env paramBindings
        funcType = foldr T.TLambda resultType paramTypes
    bodyCon <- constrain counter bodyEnv body (T.NoExpectation resultType)
    return $ T.CAnd [bodyCon, T.CEqual region T.CLambda funcType expected]


-- ═══════════════════════════════════════════════════════════
-- CALL
-- ═══════════════════════════════════════════════════════════

constrainCall :: Counter -> Env -> T.Region -> Can.Expr -> [Can.Expr] -> T.Expected T.Type -> IO T.Constraint
constrainCall counter env region func args expected = do
    resultName <- freshName counter "_cres"
    argNames <- mapM (\_ -> freshName counter "_carg") args
    let resultType = T.TVar resultName
        argTypes = map T.TVar argNames
        funcType = foldr T.TLambda resultType argTypes
    funcCon <- constrain counter env func (T.NoExpectation funcType)
    argCons <- zipWithM (\argType arg ->
        constrain counter env arg (T.FromContext region (T.CallArg "f" 0) argType))
        argTypes args
    return $ T.CAnd (funcCon : argCons ++ [T.CEqual region T.CApp resultType expected])


-- ═══════════════════════════════════════════════════════════
-- IF-THEN-ELSE
-- ═══════════════════════════════════════════════════════════

constrainIf :: Counter -> Env -> T.Region -> [(Can.Expr, Can.Expr)] -> Can.Expr -> T.Expected T.Type -> IO T.Constraint
constrainIf counter env region branches elseExpr expected = do
    branchName <- freshName counter "_ifres"
    let branchType = T.TVar branchName
    condCons <- mapM (\(cond, _) ->
        constrain counter env cond (T.FromContext region T.IfCondition boolType)) branches
    bodyCons <- zipWithM (\i (_, body) ->
        constrain counter env body (T.FromContext region (T.IfBranch i) branchType))
        [1..] branches
    elseCon <- constrain counter env elseExpr (T.FromContext region (T.IfBranch 0) branchType)
    return $ T.CAnd (condCons ++ bodyCons ++ [elseCon, T.CEqual region T.CIf branchType expected])


-- ═══════════════════════════════════════════════════════════
-- LET
-- ═══════════════════════════════════════════════════════════

constrainLet :: Counter -> Env -> Can.Def -> Can.Expr -> T.Expected T.Type -> IO T.Constraint
constrainLet counter env def body expected = do
    (defCon, name, defType) <- constrainDefWithType counter env def
    let bodyEnv = Map.insert name (T.Forall [] defType) env
    bodyCon <- constrain counter bodyEnv body expected
    return $ T.CAnd [defCon, bodyCon]


constrainLetRec :: Counter -> Env -> [Can.Def] -> Can.Expr -> T.Expected T.Type -> IO T.Constraint
constrainLetRec counter env defs body expected = do
    -- Pre-generate type info and add to env (for mutual references)
    defInfos <- mapM (defTypeInfoIO counter) defs
    let recEnv = foldr (\(n, t) e -> Map.insert n (T.Forall [] t) e) env defInfos
    -- Constrain each def using its pre-generated type
    defCons <- zipWithM (\d (_, ty) -> constrainDefWithKnownType counter recEnv d ty) defs defInfos
    bodyCon <- constrain counter recEnv body expected
    return $ T.CAnd (defCons ++ [bodyCon])


constrainLetDestruct :: Counter -> Env -> Can.Pattern -> Can.Expr -> Can.Expr -> T.Expected T.Type -> IO T.Constraint
constrainLetDestruct counter env pat valExpr body expected = do
    vName <- freshName counter "_dest"
    let valType = T.TVar vName
    valCon <- constrain counter env valExpr (T.NoExpectation valType)
    let bindings = patternBindings (pat, valType)
        bodyEnv = foldr (\(n, ann) e -> Map.insert n ann e) env bindings
    bodyCon <- constrain counter bodyEnv body expected
    return $ T.CAnd [valCon, bodyCon]


-- | Generate constraints for a definition, returning (constraint, name, funcType)
constrainDefWithType :: Counter -> Env -> Can.Def -> IO (T.Constraint, String, T.Type)
constrainDefWithType counter env def = case def of
    Can.Def (A.At region name) params body -> do
        paramNames <- mapM (\_ -> freshName counter ("_" ++ name ++ "_arg")) params
        resultName <- freshName counter ("_" ++ name ++ "_res")
        let paramTypes = map T.TVar paramNames
            resultType = T.TVar resultName
            paramBindings = concatMap patternBindings (zip params paramTypes)
            bodyEnv = foldr (\(n, ann) e -> Map.insert n ann e) env paramBindings
            funcType = foldr T.TLambda resultType paramTypes
        bodyCon <- constrain counter bodyEnv body (T.NoExpectation resultType)
        -- Wrap body in CLet that introduces parameter bindings into solver env
        -- CLet header maps param names to their type variables
        -- headerCon = CTrue (no extra constraint), bodyCon = the actual body constraint
        let paramHeader = Map.fromList $
                map (\(pname, T.Forall _ ptype) -> (pname, (A.one, ptype))) paramBindings
            wrappedCon = T.CLet [] [] paramHeader T.CTrue bodyCon
        return (wrappedCon, name, funcType)

    Can.TypedDef (A.At region name) _freeVars typedPats body retType -> do
        let paramBindings = concatMap (\(pat, ty) -> patternBindings (pat, ty)) typedPats
            bodyEnv = foldr (\(n, ann) e -> Map.insert n ann e) env paramBindings
            funcType = foldr (\(_, ty) acc -> T.TLambda ty acc) retType typedPats
        bodyCon <- constrain counter bodyEnv body (T.NoExpectation retType)
        return (bodyCon, name, funcType)

    -- Destructure binding — collect type-vars from the pattern so the body
    -- sees each bound name. We synthesise a placeholder "name" matching the
    -- _defName sentinel so downstream diagnostics stay intact.
    Can.DestructDef pat body -> do
        resultName <- freshName counter "_destruct_res"
        let resultType = T.TVar resultName
        bodyCon <- constrain counter env body (T.NoExpectation resultType)
        return (bodyCon, "__destruct__", resultType)


-- | Constrain a def with a pre-generated function type (for recursive defs)
-- ignored: type-check path for DestructDef — handled in constrainDefWithType.
constrainDefWithKnownType :: Counter -> Env -> Can.Def -> T.Type -> IO T.Constraint
constrainDefWithKnownType counter env def knownType = case def of
    Can.Def (A.At _region _name) params body -> do
        let (paramTypes, resultType) = splitFuncTypeN (length params) knownType
            paramBindings = concatMap patternBindings (zip params paramTypes)
            bodyEnv = foldr (\(n, ann) e -> Map.insert n ann e) env paramBindings
        constrain counter bodyEnv body (T.NoExpectation resultType)

    Can.TypedDef (A.At _region _name) _freeVars typedPats body retType -> do
        let paramBindings = concatMap (\(pat, ty) -> patternBindings (pat, ty)) typedPats
            bodyEnv = foldr (\(n, ann) e -> Map.insert n ann e) env paramBindings
        constrain counter bodyEnv body (T.NoExpectation retType)

    -- Destructure binding: constrain the value's body with no expectation.
    Can.DestructDef _ body ->
        constrain counter env body (T.NoExpectation knownType)


-- | Split a function type into N argument types and the result type
splitFuncTypeN :: Int -> T.Type -> ([T.Type], T.Type)
splitFuncTypeN 0 ty = ([], ty)
splitFuncTypeN n (T.TLambda from to) =
    let (rest, ret) = splitFuncTypeN (n - 1) to
    in (from : rest, ret)
splitFuncTypeN _ ty = ([], ty)


-- ═══════════════════════════════════════════════════════════
-- CASE
-- ═══════════════════════════════════════════════════════════

constrainCase :: Counter -> Env -> T.Region -> Can.Expr -> [Can.CaseBranch] -> T.Expected T.Type -> IO T.Constraint
constrainCase counter env region subject branches expected = do
    subjName <- freshName counter "_subj"
    resName <- freshName counter "_caseres"
    let subjectType = T.TVar subjName
        resultType = T.TVar resName
    subjectCon <- constrain counter env subject (T.NoExpectation subjectType)
    branchCons <- zipWithM (constrainBranch counter env region subjectType resultType) [1..] branches
    return $ T.CAnd (subjectCon : branchCons ++ [T.CEqual region T.CCase resultType expected])


constrainBranch :: Counter -> Env -> T.Region -> T.Type -> T.Type -> Int -> Can.CaseBranch -> IO T.Constraint
constrainBranch counter env region subjectType resultType branchIdx (Can.CaseBranch pat body) =
    let bindings = patternBindings (pat, subjectType)
        branchEnv = foldr (\(n, ann) e -> Map.insert n ann e) env bindings
    in constrain counter branchEnv body (T.FromContext region (T.CaseBranch branchIdx) resultType)


-- ═══════════════════════════════════════════════════════════
-- PATTERN BINDINGS
-- ═══════════════════════════════════════════════════════════

patternBindings :: (Can.Pattern, T.Type) -> [(String, T.Annotation)]
patternBindings (A.At _ pat, ty) = case pat of
    Can.PVar name -> [(name, T.Forall [] ty)]
    Can.PAnything -> []
    Can.PAlias inner name -> (name, T.Forall [] ty) : patternBindings (inner, ty)
    Can.PRecord fields -> map (\f -> (f, T.Forall [] (T.TVar ("_rec_" ++ f)))) fields
    Can.PUnit -> []
    Can.PTuple a b more ->
        concat $
            patternBindings (a, T.TVar "_tup_0")
            : patternBindings (b, T.TVar "_tup_1")
            : zipWith (\i p -> patternBindings (p, T.TVar ("_tup_" ++ show (i :: Int))))
                      [2 ..] more
    Can.PList items ->
        concatMap (\item -> patternBindings (item, T.TVar "_list_elem")) items
    Can.PCons h t ->
        let elemType = T.TVar "_cons_elem"
            listType = T.TType ModuleName.list "List" [elemType]
        in patternBindings (h, elemType) ++ patternBindings (t, listType)
    Can.PBool _ -> []
    Can.PChr _ -> []
    Can.PStr _ -> []
    Can.PInt _ -> []
    Can.PCtor _home _typeName _union ctorName _idx args ->
        concatMap (\(Can.PatternCtorArg _ argType argPat) ->
            patternBindings (argPat, argType)) args


-- ═══════════════════════════════════════════════════════════
-- HELPERS
-- ═══════════════════════════════════════════════════════════

defTypeInfoIO :: Counter -> Can.Def -> IO (String, T.Type)
defTypeInfoIO counter (Can.Def (A.At _ name) params _body) = do
    paramNames <- mapM (\_ -> freshName counter ("_" ++ name ++ "_arg")) params
    resultName <- freshName counter ("_" ++ name ++ "_res")
    let paramTypes = map T.TVar paramNames
        resultType = T.TVar resultName
    return (name, foldr T.TLambda resultType paramTypes)
defTypeInfoIO _counter (Can.TypedDef (A.At _ name) _freeVars typedPats _body retType) =
    let funcType = foldr (\(_, ty) acc -> T.TLambda ty acc) retType typedPats
    in return (name, funcType)
defTypeInfoIO counter (Can.DestructDef _ _) = do
    resultName <- freshName counter "_destruct_res"
    return ("__destruct__", T.TVar resultName)


zipWithM :: Monad m => (a -> b -> m c) -> [a] -> [b] -> m [c]
zipWithM f xs ys = sequence (zipWith f xs ys)


lookupKernelType :: String -> String -> Maybe T.Annotation
lookupKernelType modName funcName = case (modName, funcName) of
    ("Log", "println") ->
        Just $ T.Forall [] (T.TLambda stringType T.TUnit)
    ("Basics", "identity") ->
        Just $ T.Forall ["a"] (T.TLambda (T.TVar "a") (T.TVar "a"))
    ("Basics", "always") ->
        Just $ T.Forall ["a", "b"] (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "b") (T.TVar "a")))
    ("Basics", "not") ->
        Just $ T.Forall [] (T.TLambda boolType boolType)
    ("String", "fromInt") ->
        Just $ T.Forall [] (T.TLambda intType stringType)
    ("String", "fromFloat") ->
        Just $ T.Forall [] (T.TLambda floatType stringType)
    ("String", "length") ->
        Just $ T.Forall [] (T.TLambda stringType intType)
    ("String", "isEmpty") ->
        Just $ T.Forall [] (T.TLambda stringType boolType)
    ("String", "join") ->
        Just $ T.Forall [] (T.TLambda stringType (T.TLambda (T.TType ModuleName.list "List" [stringType]) stringType))
    ("String", "toInt") ->
        Just $ T.Forall [] (T.TLambda stringType
            (T.TType ModuleName.maybe_ "Maybe" [intType]))
    ("String", "toFloat") ->
        Just $ T.Forall [] (T.TLambda stringType
            (T.TType ModuleName.maybe_ "Maybe" [floatType]))
    ("String", "toUpper") ->
        Just $ T.Forall [] (T.TLambda stringType stringType)
    ("String", "toLower") ->
        Just $ T.Forall [] (T.TLambda stringType stringType)
    ("String", "trim") ->
        Just $ T.Forall [] (T.TLambda stringType stringType)
    ("String", "reverse") ->
        Just $ T.Forall [] (T.TLambda stringType stringType)
    ("String", "append") ->
        Just $ T.Forall [] (T.TLambda stringType (T.TLambda stringType stringType))
    ("String", "contains") ->
        Just $ T.Forall [] (T.TLambda stringType (T.TLambda stringType boolType))
    ("String", "startsWith") ->
        Just $ T.Forall [] (T.TLambda stringType (T.TLambda stringType boolType))
    ("String", "endsWith") ->
        Just $ T.Forall [] (T.TLambda stringType (T.TLambda stringType boolType))
    ("String", "split") ->
        Just $ T.Forall [] (T.TLambda stringType
            (T.TLambda stringType (T.TType ModuleName.list "List" [stringType])))
    ("String", "replace") ->
        Just $ T.Forall [] (T.TLambda stringType
            (T.TLambda stringType (T.TLambda stringType stringType)))
    ("String", "slice") ->
        Just $ T.Forall [] (T.TLambda intType
            (T.TLambda intType (T.TLambda stringType stringType)))
    ("Task", "succeed") ->
        Just $ T.Forall ["e", "a"] (T.TLambda (T.TVar "a")
            (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "a"]))
    ("Task", "fail") ->
        Just $ T.Forall ["e", "a"] (T.TLambda (T.TVar "e")
            (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "a"]))
    ("Task", "andThen") ->
        Just $ T.Forall ["e", "a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "b"]))
                (T.TLambda
                    (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "a"])
                    (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "b"])))
    ("Task", "run") ->
        Just $ T.Forall ["e", "a"]
            (T.TLambda
                (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "a"])
                (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"]))
    ("Task", "map") ->
        Just $ T.Forall ["e", "a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TVar "b"))
                (T.TLambda
                    (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "a"])
                    (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "b"])))
    ("Result", "withDefault") ->
        Just $ T.Forall ["e", "a"]
            (T.TLambda (T.TVar "a")
                (T.TLambda
                    (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"])
                    (T.TVar "a")))
    ("Maybe", "withDefault") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TVar "a")
                (T.TLambda
                    (T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"])
                    (T.TVar "a")))
    ("List", "map") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TVar "b"))
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.list "List" [T.TVar "b"])))
    ("List", "filter") ->
        Just $ T.Forall ["a"]
            (T.TLambda
                (T.TLambda (T.TVar "a") boolType)
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.list "List" [T.TVar "a"])))
    ("List", "foldl") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "b") (T.TVar "b")))
                (T.TLambda (T.TVar "b")
                    (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                        (T.TVar "b"))))
    _ -> Nothing


intType, floatType, stringType, boolType, charType :: T.Type
intType = T.TType ModuleName.basics "Int" []
floatType = T.TType ModuleName.basics "Float" []
stringType = T.TType ModuleName.basics "String" []
boolType = T.TType ModuleName.basics "Bool" []
charType = T.TType ModuleName.basics "Char" []
