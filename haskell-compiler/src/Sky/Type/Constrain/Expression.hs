-- | Constraint generation from canonical expressions.
-- Walks the AST and produces constraints for the solver.
-- Adapted from Elm's Type.Constrain.Expression.
module Sky.Type.Constrain.Expression
    ( constrain
    , constrainDef
    , Env
    )
    where

import qualified Data.Map.Strict as Map
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Type.Type as T
import qualified Sky.Sky.ModuleName as ModuleName


-- | Type environment: maps variable names to their type schemes
type Env = Map.Map String T.Annotation


-- | Generate constraints for an expression given an expected type.
-- Returns a Constraint tree that the solver will walk.
constrain :: Env -> Can.Expr -> T.Expected T.Type -> T.Constraint
constrain env (A.At region expr) expected = case expr of

    Can.VarLocal name ->
        T.CLocal region name expected

    Can.VarTopLevel home name ->
        T.CLocal region name expected

    Can.VarKernel modName funcName ->
        case lookupKernelType modName funcName of
            Just annot -> T.CForeign region (modName ++ "." ++ funcName) annot expected
            Nothing -> T.CTrue  -- unknown kernel function, skip

    Can.VarCtor _opts _home _typeName ctorName annot ->
        T.CForeign region ctorName annot expected

    Can.Chr _ ->
        T.CEqual region T.CChar charType expected

    Can.Str _ ->
        T.CEqual region T.CString stringType expected

    Can.Int _ ->
        -- Int literals are polymorphic: could be Int or number
        T.CEqual region T.CNumber intType expected

    Can.Float _ ->
        T.CEqual region T.CFloat floatType expected

    Can.Unit ->
        T.CEqual region T.CRecord T.TUnit expected

    Can.List items ->
        constrainList env region items expected

    Can.Negate inner ->
        constrain env inner expected

    Can.Binop op _opHome _opName _annot left right ->
        constrainBinop env region op left right expected

    Can.Lambda params body ->
        constrainLambda env region params body expected

    Can.Call func args ->
        constrainCall env region func args expected

    Can.If branches elseExpr ->
        constrainIf env region branches elseExpr expected

    Can.Let def body ->
        constrainLet env def body expected

    Can.LetRec defs body ->
        constrainLetRec env defs body expected

    Can.LetDestruct pat valExpr body ->
        constrainLetDestruct env pat valExpr body expected

    Can.Case subject branches ->
        constrainCase env region subject branches expected

    Can.Accessor _field ->
        T.CTrue  -- TODO: record accessor constraint

    Can.Access target (A.At _ _field) ->
        T.CTrue  -- TODO: record access constraint

    Can.Update _name _base _fields ->
        T.CTrue  -- TODO: record update constraint

    Can.Record _fields ->
        T.CTrue  -- TODO: record literal constraint

    Can.Tuple a b mc ->
        T.CTrue  -- TODO: tuple constraint


-- ═══════════════════════════════════════════════════════════
-- LIST
-- ═══════════════════════════════════════════════════════════

constrainList :: Env -> T.Region -> [Can.Expr] -> T.Expected T.Type -> T.Constraint
constrainList env region items expected =
    let elemType = T.TVar "_list_elem"
        listType = T.TType ModuleName.list "List" [elemType]
        itemCons = zipWith (\i item ->
            constrain env item (T.FromContext region (T.ListEntry i) elemType))
            [0..] items
    in T.CAnd (itemCons ++ [T.CEqual region T.CList listType expected])


-- ═══════════════════════════════════════════════════════════
-- BINARY OPERATORS
-- ═══════════════════════════════════════════════════════════

constrainBinop :: Env -> T.Region -> String -> Can.Expr -> Can.Expr -> T.Expected T.Type -> T.Constraint
constrainBinop env region op left right expected =
    let (leftType, rightType, resultType) = binopTypes op
        leftCon = constrain env left (T.NoExpectation leftType)
        rightCon = constrain env right (T.NoExpectation rightType)
        resultCon = T.CEqual region T.CApp resultType expected
    in T.CAnd [leftCon, rightCon, resultCon]


-- | Get types for a binary operator
binopTypes :: String -> (T.Type, T.Type, T.Type)
binopTypes op = case op of
    "+"  -> (intType, intType, intType)  -- simplified; should be number
    "-"  -> (intType, intType, intType)
    "*"  -> (intType, intType, intType)
    "/"  -> (floatType, floatType, floatType)
    "//" -> (intType, intType, intType)
    "++" -> (stringType, stringType, stringType)  -- simplified; should be appendable
    "==" -> (T.TVar "_cmp", T.TVar "_cmp", boolType)
    "/=" -> (T.TVar "_cmp", T.TVar "_cmp", boolType)
    "<"  -> (T.TVar "_cmp", T.TVar "_cmp", boolType)
    ">"  -> (T.TVar "_cmp", T.TVar "_cmp", boolType)
    "<=" -> (T.TVar "_cmp", T.TVar "_cmp", boolType)
    ">=" -> (T.TVar "_cmp", T.TVar "_cmp", boolType)
    "&&" -> (boolType, boolType, boolType)
    "||" -> (boolType, boolType, boolType)
    "|>" -> (T.TVar "_pipe_a", T.TLambda (T.TVar "_pipe_a") (T.TVar "_pipe_b"), T.TVar "_pipe_b")
    "<|" -> (T.TLambda (T.TVar "_pipe_a") (T.TVar "_pipe_b"), T.TVar "_pipe_a", T.TVar "_pipe_b")
    ">>" -> (T.TLambda (T.TVar "_a") (T.TVar "_b"), T.TLambda (T.TVar "_b") (T.TVar "_c"), T.TLambda (T.TVar "_a") (T.TVar "_c"))
    "<<" -> (T.TLambda (T.TVar "_b") (T.TVar "_c"), T.TLambda (T.TVar "_a") (T.TVar "_b"), T.TLambda (T.TVar "_a") (T.TVar "_c"))
    _    -> (T.TVar "_op_a", T.TVar "_op_b", T.TVar "_op_r")


-- ═══════════════════════════════════════════════════════════
-- LAMBDA
-- ═══════════════════════════════════════════════════════════

constrainLambda :: Env -> T.Region -> [Can.Pattern] -> Can.Expr -> T.Expected T.Type -> T.Constraint
constrainLambda env region params body expected =
    let
        -- Create type variables for each parameter
        paramTypes = zipWith (\i _ -> T.TVar ("_arg" ++ show i)) [0::Int ..] params
        -- Add params to environment
        paramBindings = concatMap (patternBindings) (zip params paramTypes)
        bodyEnv = foldr (\(n, ann) e -> Map.insert n ann e) env paramBindings
        -- Constrain body
        resultType = T.TVar "_result"
        bodyCon = constrain bodyEnv body (T.NoExpectation resultType)
        -- Build function type
        funcType = foldr T.TLambda resultType paramTypes
    in
    T.CAnd [bodyCon, T.CEqual region T.CLambda funcType expected]


-- ═══════════════════════════════════════════════════════════
-- CALL
-- ═══════════════════════════════════════════════════════════

constrainCall :: Env -> T.Region -> Can.Expr -> [Can.Expr] -> T.Expected T.Type -> T.Constraint
constrainCall env region func args expected =
    let
        -- Constrain the function
        resultType = T.TVar "_call_result"
        argTypes = zipWith (\i _ -> T.TVar ("_call_arg" ++ show i)) [0::Int ..] args
        funcType = foldr T.TLambda resultType argTypes
        funcCon = constrain env func (T.NoExpectation funcType)
        -- Constrain each argument
        argCons = zipWith (\argType arg ->
            constrain env arg (T.FromContext region (T.CallArg "f" 0) argType))
            argTypes args
        -- Result must match expected
        resultCon = T.CEqual region T.CApp resultType expected
    in
    T.CAnd (funcCon : argCons ++ [resultCon])


-- ═══════════════════════════════════════════════════════════
-- IF-THEN-ELSE
-- ═══════════════════════════════════════════════════════════

constrainIf :: Env -> T.Region -> [(Can.Expr, Can.Expr)] -> Can.Expr -> T.Expected T.Type -> T.Constraint
constrainIf env region branches elseExpr expected =
    let
        branchType = T.TVar "_if_result"
        condCons = map (\(cond, _) ->
            constrain env cond (T.FromContext region T.IfCondition boolType)) branches
        bodyCons = zipWith (\i (_, body) ->
            constrain env body (T.FromContext region (T.IfBranch i) branchType))
            [1..] branches
        elseCon = constrain env elseExpr (T.FromContext region (T.IfBranch 0) branchType)
        resultCon = T.CEqual region T.CIf branchType expected
    in
    T.CAnd (condCons ++ bodyCons ++ [elseCon, resultCon])


-- ═══════════════════════════════════════════════════════════
-- LET
-- ═══════════════════════════════════════════════════════════

constrainLet :: Env -> Can.Def -> Can.Expr -> T.Expected T.Type -> T.Constraint
constrainLet env def body expected =
    let
        (name, defType) = defTypeInfo def
        bodyEnv = Map.insert name (T.Forall [] defType) env
        defCon = constrainDef env def
        bodyCon = constrain bodyEnv body expected
    in
    T.CAnd [defCon, bodyCon]


constrainLetRec :: Env -> [Can.Def] -> Can.Expr -> T.Expected T.Type -> T.Constraint
constrainLetRec env defs body expected =
    let
        -- Add all def names to env first (mutual recursion)
        defInfos = map defTypeInfo defs
        recEnv = foldr (\(n, t) e -> Map.insert n (T.Forall [] t) e) env defInfos
        -- Constrain each definition
        defCons = map (constrainDef recEnv) defs
        -- Constrain body
        bodyCon = constrain recEnv body expected
    in
    T.CAnd (defCons ++ [bodyCon])


constrainLetDestruct :: Env -> Can.Pattern -> Can.Expr -> Can.Expr -> T.Expected T.Type -> T.Constraint
constrainLetDestruct env pat valExpr body expected =
    let
        valType = T.TVar "_destruct"
        valCon = constrain env valExpr (T.NoExpectation valType)
        bindings = patternBindings (pat, valType)
        bodyEnv = foldr (\(n, ann) e -> Map.insert n ann e) env bindings
        bodyCon = constrain bodyEnv body expected
    in
    T.CAnd [valCon, bodyCon]


-- | Generate constraints for a definition
constrainDef :: Env -> Can.Def -> T.Constraint
constrainDef env def = case def of
    Can.Def (A.At region name) params body ->
        let
            paramTypes = zipWith (\i _ -> T.TVar ("_def_arg" ++ show i)) [0::Int ..] params
            paramBindings = concatMap patternBindings (zip params paramTypes)
            bodyEnv = foldr (\(n, ann) e -> Map.insert n ann e) env paramBindings
            resultType = T.TVar ("_def_result_" ++ name)
            bodyCon = constrain bodyEnv body (T.NoExpectation resultType)
            funcType = foldr T.TLambda resultType paramTypes
        in
        T.CAnd [bodyCon, T.CEqual region T.CApp funcType (T.NoExpectation funcType)]

    Can.TypedDef (A.At region name) _freeVars typedPats body retType ->
        let
            paramBindings = concatMap (\(pat, ty) ->
                patternBindings (pat, ty)) typedPats
            bodyEnv = foldr (\(n, ann) e -> Map.insert n ann e) env paramBindings
            bodyCon = constrain bodyEnv body (T.NoExpectation retType)
        in
        bodyCon


-- ═══════════════════════════════════════════════════════════
-- CASE
-- ═══════════════════════════════════════════════════════════

constrainCase :: Env -> T.Region -> Can.Expr -> [Can.CaseBranch] -> T.Expected T.Type -> T.Constraint
constrainCase env region subject branches expected =
    let
        subjectType = T.TVar "_case_subject"
        resultType = T.TVar "_case_result"
        subjectCon = constrain env subject (T.NoExpectation subjectType)
        branchCons = zipWith (constrainBranch env region subjectType resultType) [1..] branches
        resultCon = T.CEqual region T.CCase resultType expected
    in
    T.CAnd (subjectCon : branchCons ++ [resultCon])


constrainBranch :: Env -> T.Region -> T.Type -> T.Type -> Int -> Can.CaseBranch -> T.Constraint
constrainBranch env region subjectType resultType branchIdx (Can.CaseBranch pat body) =
    let
        bindings = patternBindings (pat, subjectType)
        branchEnv = foldr (\(n, ann) e -> Map.insert n ann e) env bindings
        bodyCon = constrain branchEnv body (T.FromContext region (T.CaseBranch branchIdx) resultType)
    in
    bodyCon


-- ═══════════════════════════════════════════════════════════
-- PATTERN BINDINGS
-- ═══════════════════════════════════════════════════════════

-- | Extract variable bindings from a pattern with expected type
patternBindings :: (Can.Pattern, T.Type) -> [(String, T.Annotation)]
patternBindings (A.At _ pat, ty) = case pat of
    Can.PVar name -> [(name, T.Forall [] ty)]
    Can.PAnything -> []
    Can.PAlias inner name -> (name, T.Forall [] ty) : patternBindings (inner, ty)
    Can.PRecord fields -> map (\f -> (f, T.Forall [] (T.TVar ("_rec_" ++ f)))) fields
    Can.PUnit -> []
    Can.PTuple a b mc ->
        let aType = T.TVar "_tup_0"
            bType = T.TVar "_tup_1"
        in patternBindings (a, aType) ++ patternBindings (b, bType)
            ++ maybe [] (\c -> patternBindings (c, T.TVar "_tup_2")) mc
    Can.PList items ->
        let elemType = T.TVar "_list_elem"
        in concatMap (\item -> patternBindings (item, elemType)) items
    Can.PCons h t ->
        let elemType = T.TVar "_cons_elem"
            listType = T.TType ModuleName.list "List" [elemType]
        in patternBindings (h, elemType) ++ patternBindings (t, listType)
    Can.PBool _ -> []
    Can.PChr _ -> []
    Can.PStr _ -> []
    Can.PInt _ -> []
    Can.PCtor home typeName _union ctorName _idx args ->
        concatMap (\(Can.PatternCtorArg _ argType argPat) ->
            patternBindings (argPat, argType)) args


-- ═══════════════════════════════════════════════════════════
-- HELPERS
-- ═══════════════════════════════════════════════════════════

-- | Get name and inferred type from a definition
defTypeInfo :: Can.Def -> (String, T.Type)
defTypeInfo (Can.Def (A.At _ name) params _body) =
    let paramTypes = zipWith (\i _ -> T.TVar ("_def_arg" ++ show i)) [0::Int ..] params
        resultType = T.TVar ("_def_result_" ++ name)
    in (name, foldr T.TLambda resultType paramTypes)
defTypeInfo (Can.TypedDef (A.At _ name) _freeVars typedPats _body retType) =
    let funcType = foldr (\(_, ty) acc -> T.TLambda ty acc) retType typedPats
    in (name, funcType)


-- | Known kernel function types
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
    ("Result", "map") ->
        Just $ T.Forall ["e", "a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TVar "b"))
                (T.TLambda
                    (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"])
                    (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "b"])))
    ("Maybe", "withDefault") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TVar "a")
                (T.TLambda
                    (T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"])
                    (T.TVar "a")))
    _ -> Nothing


-- ═══════════════════════════════════════════════════════════
-- BUILT-IN TYPES
-- ═══════════════════════════════════════════════════════════

intType :: T.Type
intType = T.TType ModuleName.basics "Int" []

floatType :: T.Type
floatType = T.TType ModuleName.basics "Float" []

stringType :: T.Type
stringType = T.TType ModuleName.basics "String" []

boolType :: T.Type
boolType = T.TType ModuleName.basics "Bool" []

charType :: T.Type
charType = T.TType ModuleName.basics "Char" []
