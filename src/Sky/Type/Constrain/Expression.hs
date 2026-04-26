-- | Constraint generation from canonical expressions.
-- IO-based with a unique counter for type variable names.
-- Each call site gets unique placeholder names so the solver's
-- TVar cache shares variables WITHIN a definition but not ACROSS definitions.
module Sky.Type.Constrain.Expression
    ( constrainModule
    , constrainModuleWithExternals
    , Env
    )
    where

import Data.IORef
import qualified Data.Map.Strict as Map
import System.IO.Unsafe (unsafePerformIO)
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
constrainModule = constrainModuleWithExternals Map.empty

-- | Constrain a module with a pre-populated external type environment
-- keyed by (home-module, binding-name). VarTopLevel lookups with a
-- non-local home emit CForeign against the external annotation so
-- fresh var instantiations let each call site unify independently.
constrainModuleWithExternals
    :: Map.Map (String, String) T.Annotation
    -> Can.Module
    -> IO T.Constraint
constrainModuleWithExternals externals canMod = do
    counter <- newIORef 0
    writeIORef globalExternals externals
    constrainDecls counter Map.empty (Can._decls canMod)


-- | Thread the external signature map through a global IORef so the
-- VarTopLevel handler in constrain can reach it without extending
-- every helper's signature.
--
-- NOT THREAD-SAFE for concurrent calls to constrainModuleWithExternals
-- with different externals. Compile.hs must either serialise those
-- calls or ensure all concurrent modules share the same externals.
globalExternals :: IORef (Map.Map (String, String) T.Annotation)
{-# NOINLINE globalExternals #-}
globalExternals = unsafePerformIO (newIORef Map.empty)


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

    Can.VarTopLevel home name -> do
        -- Cross-module channel: if we have an externally-solved
        -- annotation for (home, name), emit CForeign so the solver
        -- instantiates fresh vars at this call site. Falls back to
        -- CLocal for same-module references or when no external
        -- annotation is registered.
        externals <- readIORef globalExternals
        let homeStr = ModuleName.toString home
        case Map.lookup (homeStr, name) externals of
            Just annot ->
                return $ T.CForeign region (homeStr ++ "." ++ name) annot expected
            Nothing ->
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

    Can.Record fields -> do
        -- Build a TRecord actualType with fresh TVars per field, constrain
        -- each field expression to its TVar, then emit CEqual so the solver
        -- unifies the record literal with whatever the expected type is.
        -- Thanks to the alias-expansion pass in canonicaliser, a reference
        -- like `: Profile` on an annotation appears as TAlias, and the
        -- solver unfolds it to the underlying TRecord for unification.
        --
        -- Skip the CEqual when expected is a bare nominal TType (a union
        -- or non-alias type): unifying TRecord with TType would fail with
        -- no benefit, so we fall back to per-field constraints only.
        fieldPairs <- mapM (\(fname, expr) -> do
            tvName <- freshName counter ("_rfld_" ++ fname)
            let tv = T.TVar tvName
            fieldCon <- constrain counter env expr (T.NoExpectation tv)
            return (fname, tv, fieldCon))
            (Map.toList fields)
        let fieldMap = Map.fromList
                [ (n, T.FieldType i tv)
                | (i, (n, tv, _)) <- zip [0..] fieldPairs
                ]
            recType = T.TRecord fieldMap Nothing
            fieldCons = [ c | (_, _, c) <- fieldPairs ]
            expectedIsUnifiable = case expected of
                T.NoExpectation t         -> isRecordUnifiable t
                T.FromContext _ _ t       -> isRecordUnifiable t
                T.FromAnnotation _ _ _ t  -> isRecordUnifiable t
            isRecordUnifiable ty = case ty of
                T.TVar{}    -> True
                T.TRecord{} -> True
                T.TAlias _ _ _ _ -> True
                _           -> False
        if expectedIsUnifiable
            then return $ T.CAnd (fieldCons ++ [T.CEqual region T.CRecord recType expected])
            else return $ T.CAnd fieldCons

    Can.Tuple a b rest -> do
        aName <- freshName counter "_t0"
        bName <- freshName counter "_t1"
        restNames <- mapM (\i -> freshName counter ("_t" ++ show i)) [2 .. length rest + 1]
        let aType = T.TVar aName
            bType = T.TVar bName
            restTypes = map T.TVar restNames
            tupleType = T.TTuple aType bType restTypes
        aCon <- constrain counter env a (T.NoExpectation aType)
        bCon <- constrain counter env b (T.NoExpectation bType)
        restCons <- zipWithM (\ty expr ->
            constrain counter env expr (T.NoExpectation ty))
            restTypes rest
        return $ T.CAnd (aCon : bCon : restCons ++ [T.CEqual region T.CRecord tupleType expected])


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
    -- `++` is polymorphic: works on both strings and lists. Emit a fresh
    -- type variable and require (left == right == result). The enclosing
    -- context unifies `a` with String or `List e` as appropriate.
    "++" -> do
              a <- freshName counter "_app"
              let ty = T.TVar a
              return (ty, ty, ty)
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
    -- Wrap body in CLet so param names are scoped. Without this the solver's
    -- runtime _env leaks lambda params (or pattern names) into whatever
    -- declaration is solved next, and a totally-unrelated `Just n -> ...`
    -- can pick up a stale `n` from a previous `\n -> ...` in the module.
    let paramHeader = Map.fromList
            [ (pname, (A.one, ptype))
            | (pname, T.Forall _ ptype) <- paramBindings
            ]
        bodyScoped = T.CLet [] [] paramHeader T.CTrue bodyCon
    return $ T.CAnd [bodyScoped, T.CEqual region T.CLambda funcType expected]


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
    -- Wrap with CLet so the bound name has proper lexical scope in the
    -- solver's runtime env — otherwise `let x = ... in ...` leaks `x`
    -- into the next top-level declaration.
    let header = Map.singleton name (A.one, defType)
    return $ T.CAnd [defCon, T.CLet [] [] header T.CTrue bodyCon]


constrainLetRec :: Counter -> Env -> [Can.Def] -> Can.Expr -> T.Expected T.Type -> IO T.Constraint
constrainLetRec counter env defs body expected = do
    -- Pre-generate type info and add to env (for mutual references)
    defInfos <- mapM (defTypeInfoIO counter) defs
    let recEnv = foldr (\(n, t) e -> Map.insert n (T.Forall [] t) e) env defInfos
    -- Constrain each def using its pre-generated type
    defCons <- zipWithM (\d (_, ty) -> constrainDefWithKnownType counter recEnv d ty) defs defInfos
    bodyCon <- constrain counter recEnv body expected
    let header = Map.fromList [(n, (A.one, t)) | (n, t) <- defInfos]
    return $ T.CAnd (defCons ++ [T.CLet [] [] header T.CTrue bodyCon])


constrainLetDestruct :: Counter -> Env -> Can.Pattern -> Can.Expr -> Can.Expr -> T.Expected T.Type -> IO T.Constraint
constrainLetDestruct counter env pat valExpr body expected = do
    vName <- freshName counter "_dest"
    let valType = T.TVar vName
    valCon <- constrain counter env valExpr (T.NoExpectation valType)
    let bindings = patternBindings (pat, valType)
        bodyEnv = foldr (\(n, ann) e -> Map.insert n ann e) env bindings
    bodyCon <- constrain counter bodyEnv body expected
    let header = Map.fromList
            [ (n, (A.one, t))
            | (n, T.Forall _ t) <- bindings
            ]
    return $ T.CAnd [valCon, T.CLet [] [] header T.CTrue bodyCon]


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

    Can.TypedDef (A.At _region name) freeVars typedPats body retType -> do
        -- Alpha-rename free TVars in the annotation so the polymorphic
        -- variable `a` in `boolVal : Bool -> a` doesn't collide with
        -- the `a` in `intVal : Int -> a` (the solver's TVar cache
        -- shares vars by name, so same-letter annotations across
        -- sibling definitions would otherwise unify their `a`s).
        renameMap <- Map.fromList <$>
            mapM (\(v, _) -> do
                fresh <- freshName counter ("_" ++ name ++ "_" ++ v)
                return (v, fresh)) freeVars
        let renameT = substTypeVarNames renameMap
            typedPats' = [ (pat, renameT ty) | (pat, ty) <- typedPats ]
            retType' = renameT retType
            paramBindings = concatMap (\(pat, ty) -> patternBindings (pat, ty)) typedPats'
            bodyEnv = foldr (\(n, ann) e -> Map.insert n ann e) env paramBindings
            funcType = foldr (\(_, ty) acc -> T.TLambda ty acc) retType' typedPats'
        bodyCon <- constrain counter bodyEnv body (T.NoExpectation retType')
        -- Wrap body in CLet so param bindings flow into the solver's
        -- _env. Without this, CLocal "param" lookups hit an empty
        -- env, create fresh unconstrained TVars, and downstream
        -- unifications fail even though the annotation gave the
        -- params concrete types. Matches the Can.Def path.
        let paramHeader = Map.fromList
                [ (pname, (A.one, ptype))
                | (pname, T.Forall _ ptype) <- paramBindings
                ]
            wrappedCon = T.CLet [] [] paramHeader T.CTrue bodyCon
        return (wrappedCon, name, funcType)

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
constrainBranch counter env region subjectType resultType branchIdx (Can.CaseBranch pat body) = do
    -- Fresh-instantiate any ADT type parameters this pattern references,
    -- emit a CEqual to unify the scrutinee with the instantiated pattern
    -- type (so e.g. `case m of Just n -> …` forces m's type to be
    -- `Maybe <fresh>` and binds n to that same fresh var). Without this,
    -- ADT argTypes fall back to raw `TVar "a"` from the union definition,
    -- and multiple pattern matches end up sharing the same stale "a".
    (bindings, ctorEqs) <- instantiatePattern counter pat subjectType
    let branchEnv = foldr (\(n, ann) e -> Map.insert n ann e) env bindings
    bodyCon <- constrain counter branchEnv body (T.FromContext region (T.CaseBranch branchIdx) resultType)
    let patHeader = Map.fromList
            [ (pname, (A.one, ptype))
            | (pname, T.Forall _ ptype) <- bindings
            ]
    return (T.CAnd (ctorEqs ++ [T.CLet [] [] patHeader T.CTrue bodyCon]))


-- | Walk the pattern; for every ADT constructor, fresh-alpha-rename its
-- type parameters, collect the instantiation constraint (scrutineeType
-- must unify with `TType home typeName [fresh_vars...]`), and accumulate
-- variable bindings with the instantiated arg types.
--
-- Returns (name-bindings, unification-constraints).
instantiatePattern
    :: Counter
    -> Can.Pattern
    -> T.Type
    -> IO ([(String, T.Annotation)], [T.Constraint])
instantiatePattern counter (A.At reg p) scrutTy = case p of
    Can.PVar name        -> return ([(name, T.Forall [] scrutTy)], [])
    Can.PAnything        -> return ([], [])
    Can.PUnit            ->
        return ([], [T.CEqual reg T.CRecord T.TUnit (T.NoExpectation scrutTy)])
    Can.PBool _          ->
        return ([], [T.CEqual reg (T.CCustom "bool pattern") boolType (T.NoExpectation scrutTy)])
    Can.PChr _           ->
        return ([], [T.CEqual reg T.CChar charType (T.NoExpectation scrutTy)])
    Can.PStr _           ->
        return ([], [T.CEqual reg T.CString stringType (T.NoExpectation scrutTy)])
    Can.PInt _           ->
        return ([], [T.CEqual reg T.CNumber intType (T.NoExpectation scrutTy)])

    Can.PAlias inner name -> do
        (innerBinds, innerCons) <- instantiatePattern counter inner scrutTy
        return ((name, T.Forall [] scrutTy) : innerBinds, innerCons)

    Can.PRecord fields ->
        -- Record patterns bind each field name to a fresh var (solver unifies
        -- with the scrutinee on access). Keep the historical behaviour.
        let bindings =
                [ (f, T.Forall [] (T.TVar ("_rec_" ++ f)))
                | f <- fields
                ]
        in return (bindings, [])

    Can.PTuple a b more -> do
        tvarNames <- mapM (\i -> freshName counter ("_tup_" ++ show i))
                          [0 .. 1 + length more]
        let (firstName : secondName : restNames) = tvarNames
            tupleTy = T.TTuple (T.TVar firstName) (T.TVar secondName)
                               (map T.TVar restNames)
            eq = T.CEqual reg T.CCase scrutTy (T.NoExpectation tupleTy)
        (binds, cons) <- fmap combine $ mapM (\(pat', name) ->
            instantiatePattern counter pat' (T.TVar name))
            (zip (a : b : more) tvarNames)
        return (binds, eq : cons)

    Can.PList items -> do
        elemName <- freshName counter "_list_elem"
        let elemTy = T.TVar elemName
            listTy = T.TType ModuleName.list "List" [elemTy]
            eq     = T.CEqual reg T.CCase scrutTy (T.NoExpectation listTy)
        (binds, cons) <- fmap combine $ mapM (\item ->
            instantiatePattern counter item elemTy) items
        return (binds, eq : cons)

    Can.PCons h t -> do
        elemName <- freshName counter "_cons_elem"
        let elemTy = T.TVar elemName
            listTy = T.TType ModuleName.list "List" [elemTy]
            eq     = T.CEqual reg T.CCase scrutTy (T.NoExpectation listTy)
        (hBinds, hCons) <- instantiatePattern counter h elemTy
        (tBinds, tCons) <- instantiatePattern counter t listTy
        return (hBinds ++ tBinds, eq : hCons ++ tCons)

    Can.PCtor home typeName union _ctorName _idx args -> do
        -- Fresh-alpha-rename the ADT's type parameters. The result is:
        --   - pattern's expected scrutinee type  = TType home typeName [freshVars]
        --   - each arg's type = argType with the ADT's TVar substituted
        --     for the fresh var on the same position.
        let tyParams = Can._u_vars union
        freshVarNames <- mapM (\v -> freshName counter ("_" ++ v ++ "_inst")) tyParams
        let subst = Map.fromList (zip tyParams (map T.TVar freshVarNames))
            instantiatedOuter =
                T.TType home typeName (map T.TVar freshVarNames)
            eq = T.CEqual reg T.CCase scrutTy
                    (T.NoExpectation instantiatedOuter)
        (binds, cons) <- fmap combine $ mapM (\(Can.PatternCtorArg _ argTy argPat) ->
            let argTy' = substTypeVars subst argTy
            in instantiatePattern counter argPat argTy') args
        return (binds, eq : cons)
  where
    combine xs = (concatMap fst xs, concatMap snd xs)


-- | Rename a set of TVar names within a Can.Type without otherwise
-- changing the structure. Used by TypedDef processing to alpha-
-- rename the annotation's free TVars so each function's `a`/`b`
-- binders don't accidentally unify with each other through the
-- solver's shared TVar cache.
substTypeVarNames :: Map.Map String String -> Can.Type -> Can.Type
substTypeVarNames subst = go
  where
    go t = case t of
        Can.TVar n -> Can.TVar (Map.findWithDefault n n subst)
        Can.TLambda a b -> Can.TLambda (go a) (go b)
        Can.TType h n args -> Can.TType h n (map go args)
        Can.TTuple a b cs -> Can.TTuple (go a) (go b) (map go cs)
        Can.TRecord fields mExt ->
            Can.TRecord
                (Map.map (\(Can.FieldType i fTy) -> Can.FieldType i (go fTy)) fields)
                mExt
        Can.TAlias h n pairs aliasType ->
            Can.TAlias h n [(k, go v) | (k, v) <- pairs]
                (case aliasType of
                    Can.Filled i -> Can.Filled (go i)
                    Can.Hoisted i -> Can.Hoisted (go i))
        Can.TUnit -> Can.TUnit


-- | Substitute named type variables in a Canonical.Type.
substTypeVars :: Map.Map String T.Type -> Can.Type -> T.Type
substTypeVars subst ct = case ct of
    Can.TVar n -> case Map.lookup n subst of
        Just t  -> t
        Nothing -> T.TVar n
    Can.TLambda a b  -> T.TLambda (substTypeVars subst a) (substTypeVars subst b)
    Can.TType h n args -> T.TType h n (map (substTypeVars subst) args)
    Can.TUnit        -> T.TUnit
    Can.TTuple a b cs -> T.TTuple (substTypeVars subst a) (substTypeVars subst b)
                                  (map (substTypeVars subst) cs)
    Can.TRecord _ _ -> T.TVar "_rec"  -- records at pattern level not supported
    Can.TAlias h n pairs aliasType ->
        T.TAlias h n
            [(k, substTypeVars subst t) | (k, t) <- pairs]
            (case aliasType of
                Can.Filled  inner -> T.Filled  (substTypeVars subst inner)
                Can.Hoisted inner -> T.Hoisted (substTypeVars subst inner))


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
    -- Log.println : String -> Task Error () — observable side
    -- effect (writes to stdout). Task-shaped per the Task-everywhere
    -- doctrine (2026-04-24+); the lowerer's auto-force on `let _ =`
    -- discards keeps the pervasive `let _ = println "step"` debug-
    -- trace pattern working unchanged.
    ("Log", "println") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TUnit]))
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
    ("Task", "fromResult") ->
        -- Task.fromResult : Result e a -> Task e a
        -- Bridge helper so a Result-returning FFI call can be lifted
        -- into a Task pipeline without `case` boilerplate.
        Just $ T.Forall ["e", "a"]
            (T.TLambda
                (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"])
                (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "a"]))
    ("Task", "andThenResult") ->
        -- Task.andThenResult : (a -> Result e b) -> Task e a -> Task e b
        -- Chain a Result-returning step after a Task; flattens what
        -- would otherwise be Task.andThen (\a -> Task.fromResult (f a)).
        Just $ T.Forall ["e", "a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "b"]))
                (T.TLambda
                    (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "a"])
                    (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "b"])))
    ("Task", "mapError") ->
        -- Task.mapError : (e -> e2) -> Task e a -> Task e2 a
        -- Mirrors Result.mapError. Useful for adding context to an
        -- error before it propagates further up the chain.
        Just $ T.Forall ["e", "e2", "a"]
            (T.TLambda
                (T.TLambda (T.TVar "e") (T.TVar "e2"))
                (T.TLambda
                    (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "a"])
                    (T.TType ModuleName.task "Task" [T.TVar "e2", T.TVar "a"])))
    ("Task", "onError") ->
        -- Task.onError : (e -> Task e2 a) -> Task e a -> Task e2 a
        -- Recover from a Task error by producing a new Task. The
        -- canonical "convert error to graceful response" pattern at
        -- HTTP handler boundaries and Sky.Live update branches.
        Just $ T.Forall ["e", "e2", "a"]
            (T.TLambda
                (T.TLambda (T.TVar "e")
                    (T.TType ModuleName.task "Task" [T.TVar "e2", T.TVar "a"]))
                (T.TLambda
                    (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "a"])
                    (T.TType ModuleName.task "Task" [T.TVar "e2", T.TVar "a"])))
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
    ("Maybe", "map") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda (T.TLambda (T.TVar "a") (T.TVar "b"))
                (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"])
                           (T.TType ModuleName.maybe_ "Maybe" [T.TVar "b"])))
    ("Maybe", "andThen") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TType ModuleName.maybe_ "Maybe" [T.TVar "b"]))
                (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"])
                           (T.TType ModuleName.maybe_ "Maybe" [T.TVar "b"])))
    ("Result", "combine") ->
        Just $ T.Forall ["e", "a"]
            (T.TLambda
                (T.TType ModuleName.list "List"
                    [T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"]])
                (T.TType ModuleName.result_ "Result"
                    [T.TVar "e", T.TType ModuleName.list "List" [T.TVar "a"]]))
    ("Result", "map") ->
        Just $ T.Forall ["e", "a", "b"]
            (T.TLambda (T.TLambda (T.TVar "a") (T.TVar "b"))
                (T.TLambda
                    (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"])
                    (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "b"])))
    ("Result", "andThen") ->
        Just $ T.Forall ["e", "a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "a")
                    (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "b"]))
                (T.TLambda
                    (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"])
                    (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "b"])))
    ("Result", "andThenTask") ->
        -- Result.andThenTask : (a -> Task e b) -> Result e a -> Task e b
        -- Chain a Task-returning step after a Result. The Result→Task
        -- bridge that lets sync FFI feed into effectful pipelines
        -- without an intermediate `case` on the Result.
        Just $ T.Forall ["e", "a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "a")
                    (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "b"]))
                (T.TLambda
                    (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"])
                    (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "b"])))
    ("Result", "mapError") ->
        Just $ T.Forall ["e", "e2", "a"]
            (T.TLambda (T.TLambda (T.TVar "e") (T.TVar "e2"))
                (T.TLambda
                    (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"])
                    (T.TType ModuleName.result_ "Result" [T.TVar "e2", T.TVar "a"])))
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
    -- Html kernel functions — catch-all for all element builders.
    -- 1-arg elements: text (String → VNode), img/input (attrs → VNode)
    ("Html", "text") ->
        Just $ T.Forall [] (T.TLambda stringType vnodeType)
    ("Html", "img") ->
        Just $ T.Forall [] (T.TLambda attrListType vnodeType)
    ("Html", "input") ->
        Just $ T.Forall [] (T.TLambda attrListType vnodeType)
    ("Html", "raw") ->
        -- Html.raw : String → VNode (embed raw HTML)
        Just $ T.Forall [] (T.TLambda stringType vnodeType)
    ("Html", "styleNode") ->
        -- styleNode : List Attribute -> String -> VNode (CSS body is a String)
        Just $ T.Forall [] (T.TLambda attrListType (T.TLambda stringType vnodeType))
    ("Html", "render") ->
        -- Html.render : VNode -> String
        Just $ T.Forall [] (T.TLambda vnodeType stringType)
    ("Html", "doctype") ->
        -- Html.doctype : () -> String (emits the DOCTYPE prefix)
        Just $ T.Forall [] (T.TLambda T.TUnit stringType)
    ("Html", "br") ->
        -- Html.br : List Attribute -> VNode (void element)
        Just $ T.Forall [] (T.TLambda attrListType vnodeType)
    ("Html", "hr") ->
        -- Html.hr : List Attribute -> VNode
        Just $ T.Forall [] (T.TLambda attrListType vnodeType)
    ("Html", "titleNode") ->
        -- Html.titleNode : String -> VNode
        Just $ T.Forall [] (T.TLambda stringType vnodeType)
    ("Html", "script") ->
        -- Html.script : List Attribute -> body -> VNode
        -- The body parameter stays polymorphic so callers can pass
        -- either a String (inline JS) or a List VNode (child nodes).
        Just $ T.Forall ["b"]
            (T.TLambda attrListType
                (T.TLambda (T.TVar "b") vnodeType))
    -- Void HTML elements (no children): attrs -> VNode
    ("Html", "meta") ->
        Just $ T.Forall [] (T.TLambda attrListType vnodeType)
    ("Html", "link") ->
        Just $ T.Forall [] (T.TLambda attrListType vnodeType)
    ("Html", "area") ->
        Just $ T.Forall [] (T.TLambda attrListType vnodeType)
    ("Html", "base") ->
        Just $ T.Forall [] (T.TLambda attrListType vnodeType)
    ("Html", "col") ->
        Just $ T.Forall [] (T.TLambda attrListType vnodeType)
    ("Html", "embed") ->
        Just $ T.Forall [] (T.TLambda attrListType vnodeType)
    ("Html", "source") ->
        Just $ T.Forall [] (T.TLambda attrListType vnodeType)
    ("Html", "track") ->
        Just $ T.Forall [] (T.TLambda attrListType vnodeType)
    ("Html", "wbr") ->
        Just $ T.Forall [] (T.TLambda attrListType vnodeType)
    -- 3-arg: node (String → attrs → children → VNode)
    ("Html", "node") ->
        Just $ T.Forall [] (T.TLambda stringType (T.TLambda attrListType (T.TLambda vnodeListType vnodeType)))
    -- All other Html.* functions: 2-arg (attrs → children → VNode)
    ("Html", _) ->
        Just $ T.Forall [] (T.TLambda attrListType (T.TLambda vnodeListType vnodeType))
    -- Attr kernel functions
    ("Attr", "class") ->
        Just $ T.Forall [] (T.TLambda stringType attrType)
    ("Attr", "attribute") ->
        Just $ T.Forall [] (T.TLambda stringType (T.TLambda stringType attrType))
    ("Attr", "dataAttribute") ->
        Just $ T.Forall [] (T.TLambda stringType (T.TLambda stringType attrType))
    ("Attr", "boolAttribute") ->
        Just $ T.Forall [] (T.TLambda stringType attrType)
    ("Attr", "id") ->
        Just $ T.Forall [] (T.TLambda stringType attrType)
    ("Attr", "href") ->
        Just $ T.Forall [] (T.TLambda stringType attrType)
    ("Attr", "src") ->
        Just $ T.Forall [] (T.TLambda stringType attrType)
    ("Attr", "type_") ->
        Just $ T.Forall [] (T.TLambda stringType attrType)
    ("Attr", "type") ->
        Just $ T.Forall [] (T.TLambda stringType attrType)
    ("Attr", "value") ->
        Just $ T.Forall [] (T.TLambda stringType attrType)
    ("Attr", "placeholder") ->
        Just $ T.Forall [] (T.TLambda stringType attrType)
    ("Attr", "style") ->
        Just $ T.Forall [] (T.TLambda stringType attrType)
    ("Attr", "attribute") ->
        Just $ T.Forall [] (T.TLambda stringType (T.TLambda stringType attrType))
    -- Catch-all for boolean attrs (checked, disabled, required, etc.)
    -- Runtime helpers ignore the arg (it exists only to let Sky
    -- callers use `Attr.required ()` or any other value as a
    -- present/absent marker), so accept any type at the kernel
    -- level rather than forcing String.
    ("Attr", _) ->
        Just $ T.Forall ["a"] (T.TLambda (T.TVar "a") attrType)
    -- Event handlers
    ("Event", "onClick") ->
        Just $ T.Forall ["msg"] (T.TLambda (T.TVar "msg") attrType)
    ("Event", "onInput") ->
        Just $ T.Forall ["msg"] (T.TLambda (T.TLambda stringType (T.TVar "msg")) attrType)
    ("Event", "onSubmit") ->
        Just $ T.Forall ["msg"] (T.TLambda (T.TVar "msg") attrType)
    ("Event", "onCheck") ->
        Just $ T.Forall ["msg"] (T.TLambda (T.TLambda boolType (T.TVar "msg")) attrType)
    -- Cmd kernel functions
    ("Cmd", "none") ->
        Just $ T.Forall ["msg"] cmdType
    ("Cmd", "batch") ->
        Just $ T.Forall ["msg"] (T.TLambda (T.TType ModuleName.list "List" [cmdType]) cmdType)
    ("Cmd", "perform") ->
        Just $ T.Forall ["err", "a", "msg"]
            (T.TLambda (T.TType ModuleName.task "Task" [T.TVar "err", T.TVar "a"])
                (T.TLambda (T.TLambda (T.TType ModuleName.result_ "Result" [T.TVar "err", T.TVar "a"]) (T.TVar "msg"))
                    cmdType))
    -- Sub kernel functions
    ("Sub", "none") ->
        Just $ T.Forall ["msg"] subType
    ("Sub", "every") ->
        Just $ T.Forall ["msg"] (T.TLambda intType (T.TLambda (T.TVar "msg") subType))
    ("Time", "every") ->
        Just $ T.Forall ["msg"] (T.TLambda intType (T.TLambda (T.TVar "msg") subType))
    -- More Task kernel functions
    ("Task", "perform") ->
        Just $ T.Forall ["e", "a"]
            (T.TLambda
                (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "a"])
                (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"]))
    ("Task", "sequence") ->
        Just $ T.Forall ["e", "a"]
            (T.TLambda
                (T.TType ModuleName.list "List"
                    [T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "a"]])
                (T.TType ModuleName.task "Task"
                    [T.TVar "e", T.TType ModuleName.list "List" [T.TVar "a"]]))
    ("Task", "parallel") ->
        Just $ T.Forall ["e", "a"]
            (T.TLambda
                (T.TType ModuleName.list "List"
                    [T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "a"]])
                (T.TType ModuleName.task "Task"
                    [T.TVar "e", T.TType ModuleName.list "List" [T.TVar "a"]]))
    ("Task", "lazy") ->
        Just $ T.Forall ["e", "a"]
            (T.TLambda
                (T.TLambda T.TUnit (T.TVar "a"))
                (T.TType ModuleName.task "Task" [T.TVar "e", T.TVar "a"]))
    -- Result kernel
    ("Result", "traverse") ->
        Just $ T.Forall ["e", "a", "b"]
            (T.TLambda (T.TLambda (T.TVar "a")
                (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "b"]))
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.result_ "Result"
                        [T.TVar "e", T.TType ModuleName.list "List" [T.TVar "b"]])))
    -- Maybe — more kernels
    ("Maybe", "isJust") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"]) boolType)
    ("Maybe", "isNothing") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"]) boolType)
    -- List kernel functions
    ("List", "head") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                (T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"]))
    ("List", "tail") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                (T.TType ModuleName.maybe_ "Maybe"
                    [T.TType ModuleName.list "List" [T.TVar "a"]]))
    ("List", "length") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"]) intType)
    ("List", "isEmpty") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"]) boolType)
    ("List", "reverse") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                (T.TType ModuleName.list "List" [T.TVar "a"]))
    ("List", "take") ->
        Just $ T.Forall ["a"]
            (T.TLambda intType
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.list "List" [T.TVar "a"])))
    ("List", "drop") ->
        Just $ T.Forall ["a"]
            (T.TLambda intType
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.list "List" [T.TVar "a"])))
    ("List", "append") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.list "List" [T.TVar "a"])))
    ("List", "concat") ->
        Just $ T.Forall ["a"]
            (T.TLambda
                (T.TType ModuleName.list "List"
                    [T.TType ModuleName.list "List" [T.TVar "a"]])
                (T.TType ModuleName.list "List" [T.TVar "a"]))
    ("List", "member") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TVar "a")
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"]) boolType))
    ("List", "any") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TLambda (T.TVar "a") boolType)
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"]) boolType))
    ("List", "all") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TLambda (T.TVar "a") boolType)
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"]) boolType))
    ("List", "indexedMap") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda
                (T.TLambda intType (T.TLambda (T.TVar "a") (T.TVar "b")))
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.list "List" [T.TVar "b"])))
    ("List", "filterMap") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "a")
                    (T.TType ModuleName.maybe_ "Maybe" [T.TVar "b"]))
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.list "List" [T.TVar "b"])))
    ("List", "range") ->
        Just $ T.Forall []
            (T.TLambda intType
                (T.TLambda intType
                    (T.TType ModuleName.list "List" [intType])))
    ("List", "foldr") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "b") (T.TVar "b")))
                (T.TLambda (T.TVar "b")
                    (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                        (T.TVar "b"))))
    ("List", "sum") ->
        Just $ T.Forall []
            (T.TLambda (T.TType ModuleName.list "List" [intType]) intType)
    ("List", "product") ->
        Just $ T.Forall []
            (T.TLambda (T.TType ModuleName.list "List" [intType]) intType)
    ("List", "sort") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                (T.TType ModuleName.list "List" [T.TVar "a"]))
    ("List", "sortBy") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda (T.TLambda (T.TVar "a") (T.TVar "b"))
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.list "List" [T.TVar "a"])))
    ("List", "sortWith") ->
        Just $ T.Forall ["a"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "a") intType))
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.list "List" [T.TVar "a"])))
    ("List", "singleton") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TVar "a")
                (T.TType ModuleName.list "List" [T.TVar "a"]))
    ("List", "repeat") ->
        Just $ T.Forall ["a"]
            (T.TLambda intType
                (T.TLambda (T.TVar "a")
                    (T.TType ModuleName.list "List" [T.TVar "a"])))
    ("List", "zip") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "b"])
                    (T.TType ModuleName.list "List"
                        [T.TTuple (T.TVar "a") (T.TVar "b") []])))
    ("List", "unzip") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda
                (T.TType ModuleName.list "List"
                    [T.TTuple (T.TVar "a") (T.TVar "b") []])
                (T.TTuple
                    (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.list "List" [T.TVar "b"])
                    []))
    ("List", "parallelMap") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TVar "b"))
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.list "List" [T.TVar "b"])))
    -- Dict kernel functions (keys are always String in Sky's Dict)
    ("Dict", "empty") ->
        Just $ T.Forall ["k", "v"]
            (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"])
    ("Dict", "fromList") ->
        Just $ T.Forall ["k", "v"]
            (T.TLambda
                (T.TType ModuleName.list "List"
                    [T.TTuple (T.TVar "k") (T.TVar "v") []])
                (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"]))
    ("Dict", "toList") ->
        Just $ T.Forall ["k", "v"]
            (T.TLambda (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"])
                (T.TType ModuleName.list "List"
                    [T.TTuple (T.TVar "k") (T.TVar "v") []]))
    ("Dict", "insert") ->
        Just $ T.Forall ["k", "v"]
            (T.TLambda (T.TVar "k")
                (T.TLambda (T.TVar "v")
                    (T.TLambda (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"])
                        (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"]))))
    ("Dict", "get") ->
        Just $ T.Forall ["k", "v"]
            (T.TLambda (T.TVar "k")
                (T.TLambda (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"])
                    (T.TType ModuleName.maybe_ "Maybe" [T.TVar "v"])))
    ("Dict", "remove") ->
        Just $ T.Forall ["k", "v"]
            (T.TLambda (T.TVar "k")
                (T.TLambda (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"])
                    (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"])))
    ("Dict", "member") ->
        Just $ T.Forall ["k", "v"]
            (T.TLambda (T.TVar "k")
                (T.TLambda (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"]) boolType))
    ("Dict", "size") ->
        Just $ T.Forall ["k", "v"]
            (T.TLambda (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"]) intType)
    ("Dict", "isEmpty") ->
        Just $ T.Forall ["k", "v"]
            (T.TLambda (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"]) boolType)
    ("Dict", "keys") ->
        Just $ T.Forall ["k", "v"]
            (T.TLambda (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"])
                (T.TType ModuleName.list "List" [T.TVar "k"]))
    ("Dict", "values") ->
        Just $ T.Forall ["k", "v"]
            (T.TLambda (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"])
                (T.TType ModuleName.list "List" [T.TVar "v"]))
    ("Dict", "map") ->
        Just $ T.Forall ["k", "a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "k") (T.TLambda (T.TVar "a") (T.TVar "b")))
                (T.TLambda (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "a"])
                    (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "b"])))
    ("Dict", "foldl") ->
        Just $ T.Forall ["k", "a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "k")
                    (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "b") (T.TVar "b"))))
                (T.TLambda (T.TVar "b")
                    (T.TLambda (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "a"])
                        (T.TVar "b"))))
    ("Dict", "union") ->
        Just $ T.Forall ["k", "v"]
            (T.TLambda (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"])
                (T.TLambda (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"])
                    (T.TType ModuleName.dict "Dict" [T.TVar "k", T.TVar "v"])))
    -- Set kernel functions
    ("Set", "empty") ->
        Just $ T.Forall ["a"] (T.TType ModuleName.set "Set" [T.TVar "a"])
    ("Set", "insert") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TVar "a")
                (T.TLambda (T.TType ModuleName.set "Set" [T.TVar "a"])
                    (T.TType ModuleName.set "Set" [T.TVar "a"])))
    ("Set", "member") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TVar "a")
                (T.TLambda (T.TType ModuleName.set "Set" [T.TVar "a"]) boolType))
    ("Set", "remove") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TVar "a")
                (T.TLambda (T.TType ModuleName.set "Set" [T.TVar "a"])
                    (T.TType ModuleName.set "Set" [T.TVar "a"])))
    ("Set", "toList") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.set "Set" [T.TVar "a"])
                (T.TType ModuleName.list "List" [T.TVar "a"]))
    ("Set", "fromList") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                (T.TType ModuleName.set "Set" [T.TVar "a"]))
    ("Set", "size") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.set "Set" [T.TVar "a"]) intType)
    -- Context (Go stdlib) — background/todo return an opaque
    -- context.Context; Sky exposes these as `rt.SkyValue` so user
    -- wrappers like `ctx = Context.background ()` don't degrade to
    -- `any` in the emitted Go sig.
    ("Context", "background") ->
        Just $ T.Forall []
            (T.TLambda T.TUnit
                (T.TType (ModuleName.Canonical "") "Value" []))
    ("Context", "todo") ->
        Just $ T.Forall []
            (T.TLambda T.TUnit
                (T.TType (ModuleName.Canonical "") "Value" []))
    -- Fmt.sprint / Fmt.sprintln (Go stdlib fmt) both take a list of
    -- values and return the formatted String.
    ("Fmt", "sprint") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"]) stringType)
    ("Fmt", "sprintln") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"]) stringType)
    ("Fmt", "sprintf") ->
        Just $ T.Forall ["a"]
            (T.TLambda stringType
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"]) stringType))
    -- System.* — process-level I/O (CLI args, environment reads,
    -- cwd, termination). Task-everywhere doctrine: every observable
    -- side effect returns Task Error a. Lowerer's auto-force on
    -- `let _ =` discards keeps the eager pattern usable.
    --
    -- Renamed from Sky kernel `Os` (2026-04-24) so the `Os`
    -- qualifier is free for the Go FFI `os` package — sky-log et al.
    -- need stdin / stderr / fileWriteString from Go's std library
    -- and previously hit a kernel-vs-FFI namespace collision.
    ("System", "args") ->
        Just $ T.Forall []
            (T.TLambda T.TUnit
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TType ModuleName.list "List" [stringType]]))
    ("System", "getenv") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))
    -- System.exit: stays polymorphic `Int -> a` rather than
    -- migrating to Task. The function never returns (process
    -- terminates), so it's naturally polymorphic in the return type;
    -- Task-wrapping it would force every case branch that uses
    -- System.exit as a fatal-error escape to also return Task, which
    -- is invasive without adding type information (the Task would
    -- never actually flow).
    ("System", "exit") ->
        Just $ T.Forall ["a"]
            (T.TLambda intType (T.TVar "a"))
    ("System", "getcwd") ->
        Just $ T.Forall []
            (T.TLambda T.TUnit
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))
    -- System.cwd : () -> Task Error String — runtime returns a Task
    -- thunk per the v0.10.0 Task-everywhere migration. Pre-fix this
    -- sig was Result Error String (declared duplicate further down)
    -- and the mismatch was silently swallowed at the foreign-unify
    -- step.
    ("System", "cwd") ->
        Just $ T.Forall []
            (T.TLambda T.TUnit
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))
    -- Time.sleep / Time.now / Time.unixMillis return Task Error <T>
    -- per the Task-everywhere doctrine: clock reads observe a non-
    -- deterministic real-world resource so they get the same Task
    -- treatment as File / Http / Db. The lowerer's auto-force on
    -- `let _ = Time.now ()` discard sites means the user-facing
    -- ergonomics stay close to the eager pattern.
    --
    -- Time.timeString is the exception: it's a pure deterministic
    -- formatter (Int -> String, just calls strftime equivalent).
    -- Demoted to bare String — no wrapper buys anything.
    ("Time", "sleep") ->
        Just $ T.Forall ["e"]
            (T.TLambda intType
                (T.TType ModuleName.task "Task" [T.TVar "e", T.TUnit]))
    ("Time", "now") ->
        Just $ T.Forall []
            (T.TLambda T.TUnit
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , intType]))
    ("Time", "unixMillis") ->
        Just $ T.Forall []
            (T.TLambda T.TUnit
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , intType]))
    ("Time", "timeString") ->
        Just $ T.Forall [] (T.TLambda intType stringType)
    -- Random — returns a Task in Sky stdlib
    ("Random", "int") ->
        Just $ T.Forall ["e"]
            (T.TLambda intType
                (T.TLambda intType
                    (T.TType ModuleName.task "Task" [T.TVar "e", intType])))
    ("Random", "float") ->
        Just $ T.Forall ["e"]
            (T.TLambda floatType
                (T.TLambda floatType
                    (T.TType ModuleName.task "Task" [T.TVar "e", floatType])))
    -- Math
    ("Math", "abs") ->
        Just $ T.Forall [] (T.TLambda intType intType)
    ("Math", "min") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "a") (T.TVar "a")))
    ("Math", "max") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "a") (T.TVar "a")))
    ("Math", "sqrt") ->
        Just $ T.Forall [] (T.TLambda floatType floatType)
    ("Math", "pow") ->
        Just $ T.Forall [] (T.TLambda floatType (T.TLambda floatType floatType))
    ("Math", "floor") ->
        Just $ T.Forall [] (T.TLambda floatType intType)
    ("Math", "ceil") ->
        Just $ T.Forall [] (T.TLambda floatType intType)
    ("Math", "round") ->
        Just $ T.Forall [] (T.TLambda floatType intType)
    ("Math", "pi") ->
        Just $ T.Forall [] floatType
    -- String — additional
    ("String", "concat") ->
        Just $ T.Forall []
            (T.TLambda (T.TType ModuleName.list "List" [stringType]) stringType)
    ("String", "words") ->
        Just $ T.Forall []
            (T.TLambda stringType (T.TType ModuleName.list "List" [stringType]))
    ("String", "lines") ->
        Just $ T.Forall []
            (T.TLambda stringType (T.TType ModuleName.list "List" [stringType]))
    ("String", "fromChar") ->
        Just $ T.Forall [] (T.TLambda charType stringType)
    ("String", "toList") ->
        Just $ T.Forall []
            (T.TLambda stringType (T.TType ModuleName.list "List" [charType]))
    ("String", "fromList") ->
        Just $ T.Forall []
            (T.TLambda (T.TType ModuleName.list "List" [charType]) stringType)
    ("String", "repeat") ->
        Just $ T.Forall []
            (T.TLambda intType (T.TLambda stringType stringType))
    ("String", "padLeft") ->
        Just $ T.Forall []
            (T.TLambda intType (T.TLambda charType (T.TLambda stringType stringType)))
    ("String", "padRight") ->
        Just $ T.Forall []
            (T.TLambda intType (T.TLambda charType (T.TLambda stringType stringType)))
    -- Basics
    ("Basics", "compare") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TVar "a")
                (T.TLambda (T.TVar "a") intType))
    ("Basics", "fst") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda (T.TTuple (T.TVar "a") (T.TVar "b") []) (T.TVar "a"))
    ("Basics", "snd") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda (T.TTuple (T.TVar "a") (T.TVar "b") []) (T.TVar "b"))
    ("Basics", "clamp") ->
        Just $ T.Forall []
            (T.TLambda intType
                (T.TLambda intType
                    (T.TLambda intType intType)))
    ("Basics", "modBy") ->
        Just $ T.Forall []
            (T.TLambda intType (T.TLambda intType intType))
    ("Basics", "toFloat") ->
        Just $ T.Forall [] (T.TLambda intType floatType)
    ("Basics", "round") ->
        Just $ T.Forall [] (T.TLambda floatType intType)
    ("Basics", "floor") ->
        Just $ T.Forall [] (T.TLambda floatType intType)
    ("Basics", "ceiling") ->
        Just $ T.Forall [] (T.TLambda floatType intType)
    ("Basics", "truncate") ->
        Just $ T.Forall [] (T.TLambda floatType intType)
    ("Basics", "identity") ->
        Just $ T.Forall ["a"] (T.TLambda (T.TVar "a") (T.TVar "a"))
    ("Basics", "errorToString") ->
        Just $ T.Forall []
            (T.TLambda (T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []) stringType)
    -- Log
    -- Css — most helpers return a String (CSS textual fragment).
    -- Opaque rule/property returns stay as `any` via runtimeOnlyTypes.
    ("Css", "hex") ->
        Just $ T.Forall [] (T.TLambda stringType stringType)
    ("Css", "px") ->
        Just $ T.Forall ["a"] (T.TLambda (T.TVar "a") stringType)
    ("Css", "rem") ->
        Just $ T.Forall ["a"] (T.TLambda (T.TVar "a") stringType)
    ("Css", "em") ->
        Just $ T.Forall ["a"] (T.TLambda (T.TVar "a") stringType)
    ("Css", "pct") ->
        Just $ T.Forall ["a"] (T.TLambda (T.TVar "a") stringType)
    ("Css", "stylesheet") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"]) stringType)
    -- Css.rule / Css.property / Css.margin / … deliberately un-kernelled.
    -- The runtime returns opaque `cssRule` / `cssProp` structs (not String),
    -- so a "returns String" kernel sig would cause `rt.Coerce[[]string]` to
    -- panic at the list boundary in Tailwind. Only helpers the runtime
    -- confirms return `string` get kernel entries below.
    ("Css", "shadow") ->
        -- Runtime: Sprintf("%v %v %v %v", …) → String.
        Just $ T.Forall ["a", "b", "c"]
            (T.TLambda (T.TVar "a")
                (T.TLambda (T.TVar "b")
                    (T.TLambda (T.TVar "c")
                        (T.TLambda stringType stringType))))
    ("Css", "rgb") ->
        Just $ T.Forall []
            (T.TLambda intType
                (T.TLambda intType (T.TLambda intType stringType)))
    ("Css", "rgba") ->
        Just $ T.Forall []
            (T.TLambda intType
                (T.TLambda intType
                    (T.TLambda intType (T.TLambda floatType stringType))))
    ("Css", "hsl") ->
        Just $ T.Forall []
            (T.TLambda intType
                (T.TLambda intType (T.TLambda intType stringType)))
    ("Css", "hsla") ->
        Just $ T.Forall []
            (T.TLambda intType
                (T.TLambda intType
                    (T.TLambda intType (T.TLambda floatType stringType))))
    ("Log", "printlnT") ->
        Just $ T.Forall ["a", "e"]
            (T.TLambda (T.TVar "a")
                (T.TType ModuleName.task "Task" [T.TVar "e", T.TUnit]))
    -- Live.app: the signature carries ALL user-code-facing field types
    -- so the record constraint propagates Model/Msg into user init/update/view/
    -- subscriptions. This is the big TEA-typing lever.
    ("Live", "app") ->
        -- init receives the per-request context as a plain polymorphic
        -- `req`. Earlier this slot was typed `Dict String v` to pin
        -- the outer shape, but user init bodies (skyshop) narrow `v`
        -- via nested Dict.get so HM reached `Dict String (Dict ? ?)`
        -- and the runtime's `map[string]any{"path":…}` fails the
        -- reflect Call at init time. Leaving req free keeps the
        -- runtime's generic map compatible with any inferred shape;
        -- return-only TVar defaulting collapses it to `rt.SkyValue`
        -- in the emitted sig for examples that don't touch req.
        Just $ T.Forall ["model", "msg", "page", "e", "req"]
            (T.TLambda
                (T.TRecord
                    (Map.fromList
                        [ ("init", T.FieldType 0
                            (T.TLambda (T.TVar "req")
                                (T.TTuple (T.TVar "model") cmdTypeOfMsg [])))
                        , ("update", T.FieldType 1
                            (T.TLambda (T.TVar "msg")
                                (T.TLambda (T.TVar "model")
                                    (T.TTuple (T.TVar "model") cmdTypeOfMsg []))))
                        , ("view", T.FieldType 2
                            (T.TLambda (T.TVar "model") vnodeType))
                        , ("subscriptions", T.FieldType 3
                            (T.TLambda (T.TVar "model") subTypeOfMsg))
                        , ("routes", T.FieldType 4
                            (T.TType ModuleName.list "List"
                                [T.TType (ModuleName.Canonical "") "Route" []]))
                        , ("notFound", T.FieldType 5 (T.TVar "page"))
                        ])
                    Nothing)
                (T.TType ModuleName.task "Task" [T.TVar "e", T.TUnit]))
    -- Live.route: String -> page -> Route
    ("Live", "route") ->
        Just $ T.Forall ["page"]
            (T.TLambda stringType
                (T.TLambda (T.TVar "page")
                    (T.TType (ModuleName.Canonical "") "Route" [])))
    -- Json.Decode (kernel mod "JsonDec") — signatures carry the
    -- opaque Sky `Decoder a` as TType "Decoder" [a]; the codegen
    -- resolves Decoder to rt.SkyDecoder via runtimeTypedMap.
    ("JsonDec", "string") ->
        Just $ T.Forall [] (decoderOf stringType)
    ("JsonDec", "int") ->
        Just $ T.Forall [] (decoderOf intType)
    ("JsonDec", "float") ->
        Just $ T.Forall [] (decoderOf floatType)
    ("JsonDec", "bool") ->
        Just $ T.Forall [] (decoderOf boolType)
    ("JsonDec", "decodeString") ->
        Just $ T.Forall ["a"]
            (T.TLambda (decoderOf (T.TVar "a"))
                (T.TLambda stringType
                    (T.TType ModuleName.result_ "Result"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" [], T.TVar "a"])))
    ("JsonDec", "field") ->
        Just $ T.Forall ["a"]
            (T.TLambda stringType
                (T.TLambda (decoderOf (T.TVar "a")) (decoderOf (T.TVar "a"))))
    ("JsonDec", "at") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType ModuleName.list "List" [stringType])
                (T.TLambda (decoderOf (T.TVar "a")) (decoderOf (T.TVar "a"))))
    -- Decode.index : Int -> Decoder a -> Decoder a
    ("JsonDec", "index") ->
        Just $ T.Forall ["a"]
            (T.TLambda intType
                (T.TLambda (decoderOf (T.TVar "a")) (decoderOf (T.TVar "a"))))
    ("JsonDec", "list") ->
        Just $ T.Forall ["a"]
            (T.TLambda (decoderOf (T.TVar "a"))
                (decoderOf (T.TType ModuleName.list "List" [T.TVar "a"])))
    ("JsonDec", "map") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda (T.TLambda (T.TVar "a") (T.TVar "b"))
                (T.TLambda (decoderOf (T.TVar "a")) (decoderOf (T.TVar "b"))))
    ("JsonDec", "andThen") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda (T.TLambda (T.TVar "a") (decoderOf (T.TVar "b")))
                (T.TLambda (decoderOf (T.TVar "a")) (decoderOf (T.TVar "b"))))
    ("JsonDec", "succeed") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TVar "a") (decoderOf (T.TVar "a")))
    ("JsonDec", "fail") ->
        Just $ T.Forall ["a"]
            (T.TLambda stringType (decoderOf (T.TVar "a")))
    ("JsonDec", "oneOf") ->
        Just $ T.Forall ["a"]
            (T.TLambda
                (T.TType ModuleName.list "List" [decoderOf (T.TVar "a")])
                (decoderOf (T.TVar "a")))
    ("JsonDec", "map2") ->
        Just $ T.Forall ["a", "b", "c"]
            (T.TLambda (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "b") (T.TVar "c")))
                (T.TLambda (decoderOf (T.TVar "a"))
                    (T.TLambda (decoderOf (T.TVar "b"))
                        (decoderOf (T.TVar "c")))))
    ("JsonDec", "map3") ->
        Just $ T.Forall ["a", "b", "c", "d"]
            (T.TLambda (T.TLambda (T.TVar "a")
                (T.TLambda (T.TVar "b")
                    (T.TLambda (T.TVar "c") (T.TVar "d"))))
                (T.TLambda (decoderOf (T.TVar "a"))
                    (T.TLambda (decoderOf (T.TVar "b"))
                        (T.TLambda (decoderOf (T.TVar "c"))
                            (decoderOf (T.TVar "d"))))))
    -- Json.Decode.Pipeline (kernel mod "JsonDecP")
    ("JsonDecP", "required") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda stringType
                (T.TLambda (decoderOf (T.TVar "a"))
                    (T.TLambda (decoderOf (T.TLambda (T.TVar "a") (T.TVar "b")))
                        (decoderOf (T.TVar "b")))))
    ("JsonDecP", "optional") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda stringType
                (T.TLambda (decoderOf (T.TVar "a"))
                    (T.TLambda (T.TVar "a")
                        (T.TLambda (decoderOf (T.TLambda (T.TVar "a") (T.TVar "b")))
                            (decoderOf (T.TVar "b"))))))
    -- Std.Db kernel types: row accessors return primitives; the
    -- heavier functions (open/exec/execRaw/query) are typed at the
    -- Sky level even though the runtime takes/returns `any`, because
    -- user wrappers benefit from HM propagating `String`/`List a`/
    -- `Result Error …` through the call graph. The kernel carries
    -- `Db` as an opaque nominal type — mapped to `rt.SkyDb` in
    -- codegen via runtimeTypedMap — so wrappers that thread `conn`
    -- through get a non-`any` param in their emitted sig.
    -- Std.Db — user wrappers now use `Os.exit 1` (polymorphic return)
    -- in the Err branch instead of `identity ""` so typed kernel sigs
    -- don't reject the fatal fallback.
    -- Db kernel sigs migrated to Task per the effect-boundary-audit
    -- branch: every call touches the database (disk + maybe network),
    -- has observable side effects, and must compose with Cmd.perform
    -- without blocking Sky.Live's update() call. Runtime helpers
    -- (Db_connect, Db_exec, Db_query in db_auth.go) wrap their bodies
    -- in `func() any { ... }` thunks so the I/O actually defers.
    --
    -- Migration impact: every Lib/Db.sky wrapper changes from
    -- `... -> Result Error a` to `... -> Task Error a`, and consumers
    -- chain via Task.andThen / Task.map / Task.onError instead of
    -- case-on-Result. See examples/{07-todo-cli, 08-notes-app, 12-skyvote,
    -- 13-skyshop, 16-skychess, 17-skymon, 18-job-queue} for the
    -- canonical migrations per app shape (CLI / HTTP / Sky.Live).
    ("Db", "open") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda stringType
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , T.TType (ModuleName.Canonical "") "Db" []])))
    ("Db", "connect") ->
        Just $ T.Forall []
            (T.TLambda T.TUnit
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TType (ModuleName.Canonical "") "Db" []]))
    ("Db", "exec") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                        (T.TType ModuleName.task "Task"
                            [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                            , intType]))))
    ("Db", "execRaw") ->
        Just $ T.Forall []
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , intType])))
    ("Db", "query") ->
        -- Runtime returns List (Dict String any); user code reads
        -- strings via getField so typing as Dict String String matches
        -- every example's usage pattern.
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                        (T.TType ModuleName.task "Task"
                            [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                            , T.TType ModuleName.list "List"
                                [T.TType ModuleName.dict "Dict"
                                    [stringType, stringType]]]))))
    ("Db", "getField") ->
        Just $ T.Forall ["row"]
            (T.TLambda stringType
                (T.TLambda (T.TVar "row") stringType))
    ("Db", "getString") ->
        Just $ T.Forall ["row"]
            (T.TLambda stringType
                (T.TLambda (T.TVar "row") stringType))
    ("Db", "getInt") ->
        Just $ T.Forall ["row"]
            (T.TLambda stringType
                (T.TLambda (T.TVar "row") intType))
    ("Db", "getBool") ->
        Just $ T.Forall ["row"]
            (T.TLambda stringType
                (T.TLambda (T.TVar "row") boolType))
    -- Log.{info,warn,error,debug} : String -> Task Error ()
    -- (single-arg, plain msg). Slog's (msg, attrs) shape lives on
    -- the new Log.{infoWith,warnWith,errorWith,debugWith} variants
    -- below. Slog itself dropped — migrate `Slog.info msg attrs`
    -- to `Log.infoWith msg attrs`.
    ("Log", "info") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TUnit]))
    ("Log", "warn") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TUnit]))
    ("Log", "error") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TUnit]))
    ("Log", "debug") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TUnit]))
    ("Log", "infoWith") ->
        Just $ T.Forall ["a"]
            (T.TLambda stringType
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , T.TUnit])))
    ("Log", "warnWith") ->
        Just $ T.Forall ["a"]
            (T.TLambda stringType
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , T.TUnit])))
    ("Log", "errorWith") ->
        Just $ T.Forall ["a"]
            (T.TLambda stringType
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , T.TUnit])))
    ("Log", "debugWith") ->
        Just $ T.Forall ["a"]
            (T.TLambda stringType
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , T.TUnit])))

    -- ═══════════════════════════════════════════════════════════
    -- Effect Boundary additions (effect-boundary-audit branch).
    --
    -- Per CLAUDE.md "Effect Boundary: Task" doctrine, every operation
    -- that touches the filesystem, network, process state, or external
    -- entropy returns Task Error a. The runtime helpers for these
    -- already wrap their bodies in `func() any { ... }` thunks so the
    -- I/O is deferred until Task.run / Cmd.perform / Task.perform
    -- forces the chain. Adding the kernel sigs lets HM enforce what
    -- the runtime + docs + stdlib tables already promise.
    --
    -- Result-typed wrappers in user code (e.g. `Lib.Db.exec : ... -> Result Error ()`)
    -- are migrated separately in the Db / Auth / Os / Time steps of
    -- this branch.
    -- ═══════════════════════════════════════════════════════════

    -- File: Sky.Core.File. All paths I/O.
    ("File", "readFile") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))
    ("File", "readFileLimit") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda intType
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , stringType])))
    ("File", "readFileBytes") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TType ModuleName.list "List" [intType]]))
    ("File", "writeFile") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda stringType
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , T.TUnit])))
    ("File", "append") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda stringType
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , T.TUnit])))
    ("File", "exists") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , boolType]))
    ("File", "remove") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TUnit]))
    ("File", "mkdirAll") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TUnit]))
    ("File", "readDir") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TType ModuleName.list "List" [stringType]]))
    ("File", "isDir") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , boolType]))
    ("File", "tempFile") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))
    ("File", "copy") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda stringType
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , T.TUnit])))
    ("File", "rename") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda stringType
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , T.TUnit])))

    -- Process: Sky.Core.Process. Process state + child execution.
    ("Process", "run") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda (T.TType ModuleName.list "List" [stringType])
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , stringType])))
    ("Process", "getEnv") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))
    ("Process", "getCwd") ->
        Just $ T.Forall []
            (T.TLambda T.TUnit
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))
    ("Process", "loadEnv") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TUnit]))

    -- Io: Sky.Core.Io. Standard I/O.
    ("Io", "readLine") ->
        Just $ T.Forall []
            (T.TLambda T.TUnit
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))
    ("Io", "writeStdout") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TUnit]))
    ("Io", "writeStderr") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TUnit]))
    -- Io.writeString is intentionally NOT registered here. Its runtime
    -- helper is variadic — `Io_writeString(args...)` accepts both the
    -- 1-arg form (write string to stdout, equivalent to writeStdout)
    -- AND the 2-arg form (writer + string, used by 05-mux-server's
    -- HTTP handlers to write to *http.ResponseWriter). Pinning a
    -- single-arg `String -> Task Error ()` kernel sig forces the
    -- 2-arg call sites to type their writer as `String`, which then
    -- panics at runtime when MakeFunc tries to bridge a real
    -- *http.response into the Sky-side `string` param. Leaving Io.writeString
    -- polymorphic-defaulted preserves both call shapes; users who want
    -- the 1-arg stdout form should use Io.writeStdout instead.

    -- Crypto: Sky.Core.Crypto. The two entropy-consuming helpers.
    -- sha256/sha512/md5/hmacSha256/constantTimeEqual stay pure.
    --
    -- Crypto.randomBytes : Int -> Task Error String
    -- Returns n cryptographically-secure random bytes hex-encoded
    -- (matches the runtime's actual return + the docstring; the
    -- pre-2026-04-24 sig declared `List Int` but no caller ever got
    -- a list — runtime always returned hex strings).
    ("Crypto", "randomBytes") ->
        Just $ T.Forall []
            (T.TLambda intType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))
    ("Crypto", "randomToken") ->
        Just $ T.Forall []
            (T.TLambda intType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))

    -- ═══════════════════════════════════════════════════════════
    -- Dangerous-class missing sigs added by the effect-boundary-audit
    -- branch. These all return wrapped types (Result/Maybe/Task) so
    -- without explicit kernel sigs the type checker can't enforce
    -- shape on case-pattern matches, leading to runtime panics like
    -- the sendcrafts `rt.AsInt: got rt.SkyMaybe[interface{}]` class.
    --
    -- Bare-type returns (String/Int/Bool/VNode/Attribute) are still
    -- left polymorphic-defaulted — they can't admit bad pattern
    -- destructuring because there's no wrapper to pattern-match on.
    -- Tracked as a separate known-gap; see Limitation #18 in the
    -- Known Limitations section.
    -- ═══════════════════════════════════════════════════════════

    -- Result combinators (map2..5, andMap) — same shape family as
    -- the existing Result.combine / Result.traverse sigs.
    ("Result", "map2") ->
        Just $ T.Forall ["e", "a", "b", "c"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "b") (T.TVar "c")))
                (T.TLambda
                    (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"])
                    (T.TLambda
                        (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "b"])
                        (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "c"]))))
    ("Result", "map3") ->
        Just $ T.Forall ["e", "a", "b", "c", "d"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "b") (T.TLambda (T.TVar "c") (T.TVar "d"))))
                (T.TLambda (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"])
                    (T.TLambda (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "b"])
                        (T.TLambda (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "c"])
                            (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "d"])))))
    ("Result", "map4") ->
        Just $ T.Forall ["e", "a", "b", "c", "d", "f"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "b") (T.TLambda (T.TVar "c") (T.TLambda (T.TVar "d") (T.TVar "f")))))
                (T.TLambda (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"])
                    (T.TLambda (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "b"])
                        (T.TLambda (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "c"])
                            (T.TLambda (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "d"])
                                (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "f"]))))))
    ("Result", "map5") ->
        Just $ T.Forall ["e", "a", "b", "c", "d", "f", "g"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "b") (T.TLambda (T.TVar "c") (T.TLambda (T.TVar "d") (T.TLambda (T.TVar "f") (T.TVar "g"))))))
                (T.TLambda (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"])
                    (T.TLambda (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "b"])
                        (T.TLambda (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "c"])
                            (T.TLambda (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "d"])
                                (T.TLambda (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "f"])
                                    (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "g"])))))))
    ("Result", "andMap") ->
        -- andMap : Result e a -> Result e (a -> b) -> Result e b
        Just $ T.Forall ["e", "a", "b"]
            (T.TLambda
                (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "a"])
                (T.TLambda
                    (T.TType ModuleName.result_ "Result"
                        [T.TVar "e", T.TLambda (T.TVar "a") (T.TVar "b")])
                    (T.TType ModuleName.result_ "Result" [T.TVar "e", T.TVar "b"])))

    -- Maybe combinators (map2..5, andMap, combine, traverse) — same
    -- shape family as Maybe.map / Maybe.andThen.
    ("Maybe", "map2") ->
        Just $ T.Forall ["a", "b", "c"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "b") (T.TVar "c")))
                (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"])
                    (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "b"])
                        (T.TType ModuleName.maybe_ "Maybe" [T.TVar "c"]))))
    ("Maybe", "map3") ->
        Just $ T.Forall ["a", "b", "c", "d"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "b") (T.TLambda (T.TVar "c") (T.TVar "d"))))
                (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"])
                    (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "b"])
                        (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "c"])
                            (T.TType ModuleName.maybe_ "Maybe" [T.TVar "d"])))))
    ("Maybe", "map4") ->
        Just $ T.Forall ["a", "b", "c", "d", "e"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "b") (T.TLambda (T.TVar "c") (T.TLambda (T.TVar "d") (T.TVar "e")))))
                (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"])
                    (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "b"])
                        (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "c"])
                            (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "d"])
                                (T.TType ModuleName.maybe_ "Maybe" [T.TVar "e"]))))))
    ("Maybe", "map5") ->
        Just $ T.Forall ["a", "b", "c", "d", "e", "f"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TLambda (T.TVar "b") (T.TLambda (T.TVar "c") (T.TLambda (T.TVar "d") (T.TLambda (T.TVar "e") (T.TVar "f"))))))
                (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"])
                    (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "b"])
                        (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "c"])
                            (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "d"])
                                (T.TLambda (T.TType ModuleName.maybe_ "Maybe" [T.TVar "e"])
                                    (T.TType ModuleName.maybe_ "Maybe" [T.TVar "f"])))))))
    ("Maybe", "andMap") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda
                (T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"])
                (T.TLambda
                    (T.TType ModuleName.maybe_ "Maybe" [T.TLambda (T.TVar "a") (T.TVar "b")])
                    (T.TType ModuleName.maybe_ "Maybe" [T.TVar "b"])))
    ("Maybe", "combine") ->
        Just $ T.Forall ["a"]
            (T.TLambda
                (T.TType ModuleName.list "List"
                    [T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"]])
                (T.TType ModuleName.maybe_ "Maybe"
                    [T.TType ModuleName.list "List" [T.TVar "a"]]))
    ("Maybe", "traverse") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda
                (T.TLambda (T.TVar "a") (T.TType ModuleName.maybe_ "Maybe" [T.TVar "b"]))
                (T.TLambda
                    (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.maybe_ "Maybe"
                        [T.TType ModuleName.list "List" [T.TVar "b"]])))

    -- Db higher-level helpers. All return Task Error a (the runtime
    -- helpers are still eager Result-returning — TaskCoerce bridges
    -- the call site). Migrating their runtime to thunks is tracked
    -- as future work alongside the rest of the Bucket A2 sweep.
    ("Db", "close") ->
        Just $ T.Forall []
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TUnit]))
    ("Db", "insertRow") ->
        Just $ T.Forall []
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TLambda
                        (T.TType ModuleName.dict "Dict" [stringType, stringType])
                        (T.TType ModuleName.task "Task"
                            [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                            , intType]))))
    ("Db", "getById") ->
        Just $ T.Forall []
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TLambda stringType
                        (T.TType ModuleName.task "Task"
                            [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                            , T.TType ModuleName.maybe_ "Maybe"
                                [T.TType ModuleName.dict "Dict"
                                    [stringType, stringType]]]))))
    ("Db", "updateById") ->
        Just $ T.Forall []
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TLambda stringType
                        (T.TLambda
                            (T.TType ModuleName.dict "Dict" [stringType, stringType])
                            (T.TType ModuleName.task "Task"
                                [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                                , intType])))))
    ("Db", "deleteById") ->
        Just $ T.Forall []
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TLambda stringType
                        (T.TType ModuleName.task "Task"
                            [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                            , intType]))))

    -- Auth: split per the effect-boundary doctrine.
    -- Task: register / login / setRole touch the DB (writes, reads).
    -- Result: hashPassword / verifyPassword / signToken / verifyToken /
    -- hashPasswordCost / passwordStrength are pure crypto / time-stamp
    -- read; bcrypt is CPU-bound but observably "fire and return" with
    -- no I/O effect that warrants Task ceremony.
    ("Auth", "register") ->
        Just $ T.Forall []
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TLambda stringType
                        (T.TType ModuleName.task "Task"
                            [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                            , intType]))))
    ("Auth", "login") ->
        Just $ T.Forall []
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TLambda stringType
                        (T.TType ModuleName.task "Task"
                            [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                            , intType]))))
    ("Auth", "setRole") ->
        Just $ T.Forall []
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda intType
                    (T.TLambda stringType
                        (T.TType ModuleName.task "Task"
                            [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                            , T.TUnit]))))
    ("Auth", "hashPassword") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.result_ "Result"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))
    ("Auth", "hashPasswordCost") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda intType
                    (T.TType ModuleName.result_ "Result"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , stringType])))
    ("Auth", "verifyPassword") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda stringType
                    (T.TType ModuleName.result_ "Result"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , boolType])))
    ("Auth", "passwordStrength") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.result_ "Result"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))
    ("Auth", "signToken") ->
        -- signToken : Secret -> claims -> expirySeconds -> Result Error Token
        -- claims is left polymorphic — typically a Dict / opaque map.
        Just $ T.Forall ["a"]
            (T.TLambda stringType
                (T.TLambda (T.TVar "a")
                    (T.TLambda intType
                        (T.TType ModuleName.result_ "Result"
                            [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                            , stringType]))))
    ("Auth", "verifyToken") ->
        -- verifyToken : Secret -> Token -> Result Error claims
        Just $ T.Forall ["a"]
            (T.TLambda stringType
                (T.TLambda stringType
                    (T.TType ModuleName.result_ "Result"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , T.TVar "a"])))

    -- Db query helpers (composed wrappers around Db.query).
    ("Db", "findOneByField") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TLambda stringType
                        (T.TLambda (T.TVar "a")
                            (T.TType ModuleName.task "Task"
                                [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                                , T.TType ModuleName.maybe_ "Maybe"
                                    [T.TType ModuleName.dict "Dict"
                                        [stringType, stringType]]])))))
    ("Db", "findManyByField") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TLambda stringType
                        (T.TLambda (T.TVar "a")
                            (T.TType ModuleName.task "Task"
                                [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                                , T.TType ModuleName.list "List"
                                    [T.TType ModuleName.dict "Dict"
                                        [stringType, stringType]]])))))
    ("Db", "findByConditions") ->
        Just $ T.Forall []
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TLambda
                        (T.TType ModuleName.dict "Dict" [stringType, stringType])
                        (T.TType ModuleName.task "Task"
                            [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                            , T.TType ModuleName.list "List"
                                [T.TType ModuleName.dict "Dict"
                                    [stringType, stringType]]]))))
    ("Db", "unsafeFindWhere") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TLambda stringType
                        (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                            (T.TType ModuleName.task "Task"
                                [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                                , T.TType ModuleName.list "List"
                                    [T.TType ModuleName.dict "Dict"
                                        [stringType, stringType]]])))))
    ("Db", "queryDecode") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda stringType
                    (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                        (T.TLambda (T.TVar "b")
                            (T.TType ModuleName.task "Task"
                                [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                                , T.TType ModuleName.list "List" [T.TVar "b"]])))))
    ("Db", "withTransaction") ->
        -- withTransaction : Db -> (Db -> Task Error a) -> Task Error a
        Just $ T.Forall ["a"]
            (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                (T.TLambda
                    (T.TLambda (T.TType (ModuleName.Canonical "") "Db" [])
                        (T.TType ModuleName.task "Task"
                            [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                            , T.TVar "a"]))
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , T.TVar "a"])))

    -- List.find — Maybe-returning lookup.
    ("List", "find") ->
        Just $ T.Forall ["a"]
            (T.TLambda (T.TLambda (T.TVar "a") boolType)
                (T.TLambda (T.TType ModuleName.list "List" [T.TVar "a"])
                    (T.TType ModuleName.maybe_ "Maybe" [T.TVar "a"])))

    -- Args.* and Env.* dropped in v0.10.0 — folded into System.*.
    -- New System sigs:
    --   System.getArg     : Int -> Task Error (Maybe String)
    --   System.getenvOr   : String -> String -> Task Error String
    --   System.getenvInt  : String -> Task Error Int
    --   System.getenvBool : String -> Task Error Bool
    --   System.loadEnv    : () -> Task Error ()
    -- (System.args / getenv / cwd / exit are declared above with
    -- the rest of the System block.)
    ("System", "getArg") ->
        Just $ T.Forall []
            (T.TLambda intType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TType ModuleName.maybe_ "Maybe" [stringType]]))
    -- System.getenvOr key default : String
    -- Bare-String return — when a default is supplied the call CAN'T
    -- fail, so Task-wrapping it would force every config helper at
    -- module top-level into the `Task.run … |> Result.withDefault def`
    -- pattern this helper exists to avoid. Fallible variants
    -- (getenv / getenvInt / getenvBool) stay Task.
    ("System", "getenvOr") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda stringType stringType))
    ("System", "getenvInt") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , intType]))
    ("System", "getenvBool") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , boolType]))
    ("System", "loadEnv") ->
        Just $ T.Forall []
            (T.TLambda T.TUnit
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TUnit]))

    -- Encoding encoders never fail — base64 / URL-encode / hex-encode
    -- are total functions over any input string. Kernel sig is
    -- `String -> String` to match the runtime + typed companion which
    -- have always returned a bare string (the codegen consumes them
    -- as bare strings via `rt.Concat("…", rt.Encoding_urlEncodeT(x))`).
    -- Pre-2026-04-24 the kernel sig wrapped these in Result Error String
    -- which never fired the Err arm — broken pattern matches were
    -- silently impossible to trigger. Decoders correctly stay Result
    -- (they CAN fail on malformed input).
    ("Encoding", "base64Encode") ->
        Just $ T.Forall [] (T.TLambda stringType stringType)
    ("Encoding", "base64Decode") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.result_ "Result"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))
    ("Encoding", "urlEncode") ->
        Just $ T.Forall [] (T.TLambda stringType stringType)
    ("Encoding", "urlDecode") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.result_ "Result"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))
    ("Encoding", "hexEncode") ->
        Just $ T.Forall [] (T.TLambda stringType stringType)
    ("Encoding", "hexDecode") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.result_ "Result"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , stringType]))

    -- Hex.* dropped in v0.10.0 — Encoding.hexEncode (bare String)
    -- and Encoding.hexDecode (Result Error String) are the
    -- consolidated surface, declared above.

    -- Time.now / Time.unixMillis / Time.timeString / Time.parse:
    -- intentionally NOT registered. Runtime returns SkyResult eagerly,
    -- but existing user code relies on context-dependent unification
    -- (used as both `ts = Time.now ()` for an eager Int and
    -- `Task.andThen (\_ -> Time.now)` inside a Task chain). Pinning a
    -- single sig — Result Error Int OR Task Error Int — breaks the
    -- other call shape. See Limitation #18 + doctrine carve-out for
    -- the broader "sync convenience effects stay polymorphic" position.

    -- Uuid.v4 / v7 / parse: also intentionally NOT registered.
    -- v4/v7 runtime returns a thunk (Task-shaped) but is commonly
    -- called eagerly as `id = Uuid.v4 ()` and expected to bind a
    -- String directly. Polymorphic-defaulted lets both shapes work.
    -- parse is eager Result-returning but kept polymorphic for the
    -- same reason — wrappers that then thread it into a Task chain.

    -- Random.choice / shuffle: runtime returns thunks (Task) but
    -- existing examples use them context-dependently. Same reason
    -- as Time / Uuid above. Random.int / Random.float ARE typed Task
    -- (added earlier) because their explicit-arg form forces the
    -- caller to commit to a shape.

    -- System.cwd duplicate sig — the canonical Task version is
    -- declared higher up in the System block (line ~1349). Leaving
    -- a no-op here because the registry is first-match-wins; an
    -- earlier Result-shape declaration silently shipped before
    -- foreign-fatal landed (v0.10.0). The runtime returns a Task
    -- thunk and the upper sig matches.

    -- Server: extractors return Maybe String for things that may be
    -- absent (cookies, query params, headers, route params).
    ("Server", "param") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda (T.TType (ModuleName.Canonical "") "Request" [])
                    (T.TType ModuleName.maybe_ "Maybe" [stringType])))
    ("Server", "queryParam") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda (T.TType (ModuleName.Canonical "") "Request" [])
                    (T.TType ModuleName.maybe_ "Maybe" [stringType])))
    ("Server", "header") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda (T.TType (ModuleName.Canonical "") "Request" [])
                    (T.TType ModuleName.maybe_ "Maybe" [stringType])))
    ("Server", "getCookie") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda (T.TType (ModuleName.Canonical "") "Request" [])
                    (T.TType ModuleName.maybe_ "Maybe" [stringType])))

    -- ─── Limitation #16 dangerous-class kernel sigs ───────────────
    -- Closing the 9 (now 10 after v0.10.0 renames) gaps from
    -- CLAUDE.md "Some kernel functions still missing HM type
    -- signatures". These return Maybe/Result/Task wrappers OR
    -- opaque FFI types (Route, Handler, HttpResponse, Decoder); the
    -- gap caused user code that pattern-matched the wrapper to
    -- silently degrade to `any`, which downstream surfaced as
    -- runtime panics like `rt.AsBool: expected bool, got
    -- rt.SkyResult[…]`. Each entry below mirrors the exact return
    -- shape of the matching `runtime-go/rt/*.go` helper.

    -- Server.static : String -> String -> Route
    -- Runtime: returns SkyRoute (struct). Same opaque-Route encoding
    -- as Server.get / Live.route.
    ("Server", "static") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda stringType
                    (T.TType (ModuleName.Canonical "") "Route" [])))

    -- Sky.Http.Middleware.* — all take a Handler and decorate it
    -- with extra behaviour (CORS preflight, request logging, basic
    -- auth, rate limit). All return Handler so they compose via
    -- `withCors origins (withLogging baseHandler)`.
    ("Middleware", "withCors") ->
        Just $ T.Forall []
            (T.TLambda (T.TType ModuleName.list "List" [stringType])
                (T.TLambda (T.TType (ModuleName.Canonical "") "Handler" [])
                    (T.TType (ModuleName.Canonical "") "Handler" [])))
    ("Middleware", "withLogging") ->
        Just $ T.Forall []
            (T.TLambda (T.TType (ModuleName.Canonical "") "Handler" [])
                (T.TType (ModuleName.Canonical "") "Handler" []))
    ("Middleware", "withBasicAuth") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda stringType
                    (T.TLambda (T.TType (ModuleName.Canonical "") "Handler" [])
                        (T.TType (ModuleName.Canonical "") "Handler" []))))
    ("Middleware", "withRateLimit") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda intType
                    (T.TLambda intType
                        (T.TLambda (T.TType (ModuleName.Canonical "") "Handler" [])
                            (T.TType (ModuleName.Canonical "") "Handler" [])))))

    -- Sky.Http (kernel mod "Http") — both return Task Error
    -- HttpResponse (Task-everywhere doctrine since v0.10.0).
    -- HttpResponse is opaque; users read its fields via Std.Http
    -- helpers (statusCode/body/header) — those land in #26 batch.
    ("Http", "get") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TType ModuleName.task "Task"
                    [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                    , T.TType (ModuleName.Canonical "") "HttpResponse" []]))
    ("Http", "post") ->
        Just $ T.Forall []
            (T.TLambda stringType
                (T.TLambda stringType
                    (T.TType ModuleName.task "Task"
                        [T.TType (ModuleName.Canonical "Sky.Core.Error") "Error" []
                        , T.TType (ModuleName.Canonical "") "HttpResponse" []])))

    -- Json.Decode.map4 extends the existing map2/map3 series.
    ("JsonDec", "map4") ->
        Just $ T.Forall ["a", "b", "c", "d", "e"]
            (T.TLambda (T.TLambda (T.TVar "a")
                (T.TLambda (T.TVar "b")
                    (T.TLambda (T.TVar "c")
                        (T.TLambda (T.TVar "d") (T.TVar "e")))))
                (T.TLambda (decoderOf (T.TVar "a"))
                    (T.TLambda (decoderOf (T.TVar "b"))
                        (T.TLambda (decoderOf (T.TVar "c"))
                            (T.TLambda (decoderOf (T.TVar "d"))
                                (decoderOf (T.TVar "e")))))))

    -- Json.Decode.Pipeline.custom : JsonDecoder a -> JsonDecoder
    -- (a -> b) -> JsonDecoder b. Same shape as JsonDecP.required
    -- but the first arg is a Decoder rather than a field name.
    ("JsonDecP", "custom") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda (decoderOf (T.TVar "a"))
                (T.TLambda (decoderOf (T.TLambda (T.TVar "a") (T.TVar "b")))
                    (decoderOf (T.TVar "b"))))

    -- Json.Decode.Pipeline.requiredAt : List String -> JsonDecoder
    -- a -> JsonDecoder (a -> b) -> JsonDecoder b. Like `required`
    -- but takes a path (list of nested keys).
    ("JsonDecP", "requiredAt") ->
        Just $ T.Forall ["a", "b"]
            (T.TLambda (T.TType ModuleName.list "List" [stringType])
                (T.TLambda (decoderOf (T.TVar "a"))
                    (T.TLambda (decoderOf (T.TLambda (T.TVar "a") (T.TVar "b")))
                        (decoderOf (T.TVar "b")))))

    _ -> Nothing


intType, floatType, stringType, boolType, charType :: T.Type
intType = T.TType ModuleName.basics "Int" []
floatType = T.TType ModuleName.basics "Float" []
stringType = T.TType ModuleName.basics "String" []
boolType = T.TType ModuleName.basics "Bool" []
charType = T.TType ModuleName.basics "Char" []

vnodeType, attrType, attrListType, vnodeListType, cmdType, subType :: T.Type
-- Use empty Canonical so VNode/Attribute unify with user annotations
-- that resolve VNode to Canonical "" (implicitly imported).
vnodeType = T.TType (ModuleName.Canonical "") "VNode" []
-- Attribute is a transparent alias for (String, String). See
-- actuallyUnify's Alias case.
attrType =
    T.TAlias (ModuleName.Canonical "") "Attribute" []
        (T.Filled (T.TTuple stringType stringType []))
attrListType = T.TType ModuleName.list "List" [attrType]
vnodeListType = T.TType ModuleName.list "List" [vnodeType]
-- Use Canonical "" so Cmd/Sub unify with user annotations that
-- resolve to empty-home module names (same as VNode/Attribute).
cmdType = T.TType (ModuleName.Canonical "") "Cmd" [T.TVar "msg"]
subType = T.TType (ModuleName.Canonical "") "Sub" [T.TVar "msg"]

-- Inside Live.app's record type, the TVar "msg" is shared across
-- init/update/subscriptions so the three coordinate. cmdTypeOfMsg /
-- subTypeOfMsg reference the same "msg" var as the top-level Forall
-- binder does.
cmdTypeOfMsg, subTypeOfMsg :: T.Type
cmdTypeOfMsg = T.TType (ModuleName.Canonical "") "Cmd" [T.TVar "msg"]
subTypeOfMsg = T.TType (ModuleName.Canonical "") "Sub" [T.TVar "msg"]

-- Decoder wrapper. Home is empty so it unifies with runtimeTypedMap's
-- "Decoder" lookup (which picks up rt.SkyDecoder) regardless of where
-- the user imports from.
decoderOf :: T.Type -> T.Type
decoderOf inner = T.TType (ModuleName.Canonical "") "Decoder" [inner]
