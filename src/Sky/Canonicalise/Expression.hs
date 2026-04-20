-- | Canonicalise expressions — resolve all variable references.
module Sky.Canonicalise.Expression
    ( canonicaliseExpr
    )
    where

import qualified Data.Char as Char
import qualified Data.Map.Strict as Map
import qualified Sky.AST.Source as Src
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Canonicalise.Environment as Env
import qualified Sky.Canonicalise.Pattern as CanPat
import qualified Sky.Canonicalise.Type as CanType
import qualified Sky.Parse.Symbol as Sym


-- | Canonicalise a source expression
canonicaliseExpr :: Env.Env -> Src.Expr -> Can.Expr
canonicaliseExpr env (A.At region expr) =
    A.At region $ canonicaliseExpr_ env region expr


canonicaliseExpr_ :: Env.Env -> A.Region -> Src.Expr_ -> Can.Expr_
canonicaliseExpr_ env region expr = case expr of

    Src.Var name ->
        resolveVar env name

    Src.VarQual qualifier name ->
        resolveQualVar env qualifier name

    Src.Chr c ->
        Can.Chr c

    Src.Str s ->
        Can.Str s

    Src.MultilineStr s ->
        -- Desugar `{{expr}}` interpolation at canonicalise time by splitting
        -- the raw string into (literal, expr) chunks and emitting a left-
        -- associated `++` chain. Expressions are parsed on the fly — we
        -- re-invoke the expression parser against the {{…}} body text.
        desugarMultiline env s

    Src.Int n ->
        Can.Int n

    Src.Float f ->
        Can.Float f

    Src.List items ->
        Can.List (map (canonicaliseExpr env) items)

    Src.Negate inner ->
        Can.Negate (canonicaliseExpr env inner)

    -- Parens are transparent at the canonical level — same type, same
    -- value. The wrapping only matters at parse time so the
    -- precedence-climber in canonicaliseBinops doesn't re-associate
    -- through a parenthesised sub-chain. See flattenBinops'.
    Src.Paren inner ->
        A.toValue (canonicaliseExpr env inner)

    Src.Binops pairs final ->
        canonicaliseBinops env pairs final

    Src.Lambda params body ->
        let paramNames = concatMap CanPat.patternNames params
            bodyEnv = Env.addLocals paramNames env
            canParams = map (CanPat.canonicalisePattern env) params
            canBody = canonicaliseExpr bodyEnv body
        in Can.Lambda canParams canBody

    Src.Call func args ->
        Can.Call (canonicaliseExpr env func) (map (canonicaliseExpr env) args)

    Src.If branches elseExpr ->
        Can.If
            (map (\(c, b) -> (canonicaliseExpr env c, canonicaliseExpr env b)) branches)
            (canonicaliseExpr env elseExpr)

    Src.Let defs body ->
        canonicaliseLet env defs body

    Src.Case subject branches ->
        let canSubject = canonicaliseExpr env subject
            canBranches = map (canonicaliseCaseBranch env) branches
        in Can.Case canSubject canBranches

    Src.Accessor field ->
        Can.Accessor field

    Src.Access target (A.At r field) ->
        Can.Access (canonicaliseExpr env target) (A.At r field)

    Src.Update (A.At r name) fields ->
        Can.Update (A.At r name) (canonicaliseExpr env (A.At region (Src.Var name)))
            (Map.fromList $ map (\(A.At fr fn, fe) ->
                (fn, Can.FieldUpdate fr (canonicaliseExpr env fe))) fields)

    Src.Record fields ->
        Can.Record $ Map.fromList $
            map (\(A.At _ name, val) -> (name, canonicaliseExpr env val)) fields

    Src.Unit ->
        Can.Unit

    Src.Tuple a b rest ->
        Can.Tuple
            (canonicaliseExpr env a)
            (canonicaliseExpr env b)
            (map (canonicaliseExpr env) rest)

    Src.Op op ->
        -- Standalone operator reference (e.g., passed as function)
        resolveOperator env op


-- ═══════════════════════════════════════════════════════════
-- VARIABLE RESOLUTION
-- ═══════════════════════════════════════════════════════════

-- | Resolve a bare variable name
resolveVar :: Env.Env -> String -> Can.Expr_
resolveVar env name =
    -- Check constructors first (uppercase)
    case Env.lookupCtor name env of
        Just ctor ->
            Can.VarCtor (Can._u_opts (Env._ch_union ctor))
                (Env._ch_home ctor)
                (Env._ch_type ctor)
                (Env._ch_name ctor)
                (Env._ch_annot ctor)
        Nothing ->
            -- Check variables
            case Env.lookupVar name env of
                Just Env.VarLocal ->
                    Can.VarLocal name
                Just (Env.VarTopLevel home) ->
                    Can.VarTopLevel home name
                Just (Env.VarKernel modName funcName) ->
                    Can.VarKernel modName funcName
                Nothing ->
                    -- Unknown variable — emit as local (will be caught by type checker)
                    Can.VarLocal name


-- | Resolve a qualified variable (e.g., Task.succeed, String.fromInt)
resolveQualVar :: Env.Env -> String -> String -> Can.Expr_
resolveQualVar env qualifier name =
    -- Check qualified constructors first
    case Env.lookupQualCtor qualifier name env of
        Just ctor ->
            Can.VarCtor (Can._u_opts (Env._ch_union ctor))
                (Env._ch_home ctor)
                (Env._ch_type ctor)
                (Env._ch_name ctor)
                (Env._ch_annot ctor)
        Nothing ->
            -- Check qualified variables
            case Env.lookupQualVar qualifier name env of
                Just (Env.VarKernel modName funcName) ->
                    Can.VarKernel modName funcName
                Just (Env.VarTopLevel home) ->
                    Can.VarTopLevel home name
                Just Env.VarLocal ->
                    Can.VarLocal name
                Nothing ->
                    -- Unknown qualified name — resolve alias to full module name
                    case Env.lookupImportAlias qualifier env of
                        Just fullMod -> Can.VarTopLevel fullMod name
                        Nothing -> Can.VarTopLevel (ModuleName.Canonical qualifier) name


-- | Resolve an operator to its canonical form
resolveOperator :: Env.Env -> String -> Can.Expr_
resolveOperator _env op = case op of
    "+"  -> Can.VarKernel "Basics" "add"
    "-"  -> Can.VarKernel "Basics" "sub"
    "*"  -> Can.VarKernel "Basics" "mul"
    "/"  -> Can.VarKernel "Basics" "fdiv"
    "//" -> Can.VarKernel "Basics" "idiv"
    "==" -> Can.VarKernel "Basics" "eq"
    "/=" -> Can.VarKernel "Basics" "neq"
    "<"  -> Can.VarKernel "Basics" "lt"
    ">"  -> Can.VarKernel "Basics" "gt"
    "<=" -> Can.VarKernel "Basics" "le"
    ">=" -> Can.VarKernel "Basics" "ge"
    "&&" -> Can.VarKernel "Basics" "and"
    "||" -> Can.VarKernel "Basics" "or"
    "++" -> Can.VarKernel "Basics" "append"
    "::" -> Can.VarKernel "List" "cons"
    _    -> Can.VarKernel "Basics" op


-- ═══════════════════════════════════════════════════════════
-- BINARY OPERATORS
-- ═══════════════════════════════════════════════════════════

-- | Canonicalise a binary operator chain using precedence climbing.
--
-- The parser emits `Binops [(e1, op1), (e2, op2), (e3, op3)] final` as a
-- flat parse of `e1 op1 e2 op2 e3 op3 final` without consulting operator
-- precedence. We build the correct tree here, reading per-op precedence
-- and associativity from Sky.Parse.Symbol.precedence.
canonicaliseBinops :: Env.Env -> [(Src.Expr, A.Located String)] -> Src.Expr -> Can.Expr_
canonicaliseBinops env pairs final =
    -- The parser emits Src.Binops as a *nested* structure: the first
    -- operand of an outer chain may itself be a Src.Binops node. We
    -- flatten into one long stream of operands+operators before applying
    -- precedence climbing. Without this, `n >= 0 && n <= 150` parses as
    -- `Binops [(Binops [(Binops [(n, >=)] 0), &&)] n), <=] 150` and only
    -- the outermost level gets its precedence recomputed — leaving the
    -- inner `((n >= 0) && n)` mis-associated.
    let (firstSrc, tailPairs) = flattenBinops pairs final
    in case tailPairs of
        [] -> A.toValue (canonicaliseExpr env firstSrc)
        _ ->
            let firstOperand = canonicaliseExpr env firstSrc
                restOperands = map (canonicaliseExpr env . fst) tailPairs
                ops          = map snd tailPairs
                (tree, _, _) = climb 0 firstOperand restOperands ops
            in A.toValue tree
  where
    -- Flatten a nested Src.Binops tree into (firstOperand, [(rightOperand, op)])
    -- in left-to-right source order. The `tailPairs` tuple is
    -- `(operand_i, op_{i-1})` — the operand that follows op_{i-1}.
    flattenBinops :: [(Src.Expr, A.Located String)] -> Src.Expr -> (Src.Expr, [(Src.Expr, String)])
    flattenBinops srcPairs srcFinal =
        let -- First recurse into the leftmost sub-operand.
            (leftmost, leadPairs) = case srcPairs of
                []     -> (srcFinal, [])
                (e0, A.At _ op0):restPairs ->
                    let (subFirst, subTail) = flattenBinops' e0
                        -- After flattening e0, the remaining chain is:
                        -- subTail ++ [(middle_operand, op0) ...] ++ restPairs ++ [(final, _)]
                        (middleFirst, middleTail) = flattenBinops restPairs srcFinal
                    in (subFirst, subTail ++ [(middleFirst, op0)] ++ middleTail)
        in (leftmost, leadPairs)

    -- Same as flattenBinops but recursing on a single operand expression.
    -- If it's itself a Src.Binops, flatten that; otherwise it's a leaf.
    -- Src.Paren is ALWAYS a leaf — parentheses are the user's explicit
    -- signal that this sub-chain should not be flattened into the outer
    -- precedence climb. Without this stop-point, `(a - b) * c` flattens
    -- as `[a, b, c]` with ops `[-, *]`, then precedence reassociates as
    -- `a - (b * c)`, silently violating the user's grouping.
    flattenBinops' :: Src.Expr -> (Src.Expr, [(Src.Expr, String)])
    flattenBinops' expr@(A.At _ e) = case e of
        Src.Paren _     -> (expr, [])
        Src.Binops ps f -> flattenBinops ps f
        _               -> (expr, [])

    -- climb minPrec left operands operators
    -- Consumes ops of precedence >= minPrec, pairing each with next operand.
    -- climb minPrec left operands operators
    -- Consumes ops of precedence >= minPrec, pairing each with next operand.
    climb :: Int -> Can.Expr -> [Can.Expr] -> [String]
          -> (Can.Expr, [Can.Expr], [String])
    climb _ left operands []        = (left, operands, [])
    climb minPrec left operands (op:opsTail)
        | opPrec op < minPrec = (left, operands, op:opsTail)
        | otherwise = case operands of
            [] -> (left, [], op:opsTail)
            (nextOperand:restOps) ->
                let Sym.Precedence p assoc = Sym.precedence op
                    nextMin = case assoc of
                        Sym.L -> p + 1
                        Sym.R -> p
                        Sym.N -> p + 1
                    (right, remOperands, remOps) = climb nextMin nextOperand restOps opsTail
                    merged = combine op left right
                in climb minPrec merged remOperands remOps

    opPrec op = case Sym.precedence op of Sym.Precedence p _ -> p

    combine op (A.At lr l) (A.At rr r) =
        let (opHome, opName) = resolveOpName op
            opAnnot = operatorAnnotation op
            mergedReg = A.merge lr rr
        in A.At mergedReg $
            Can.Binop op opHome opName opAnnot (A.At lr l) (A.At rr r)


-- | Resolve operator to its home module and canonical name
resolveOpName :: String -> (ModuleName.Canonical, String)
resolveOpName op = case op of
    "++"  -> (ModuleName.basics, "append")
    "::"  -> (ModuleName.list, "cons")
    "|>"  -> (ModuleName.basics, "apR")
    "<|"  -> (ModuleName.basics, "apL")
    ">>"  -> (ModuleName.basics, "composeL")
    "<<"  -> (ModuleName.basics, "composeR")
    "+"   -> (ModuleName.basics, "add")
    "-"   -> (ModuleName.basics, "sub")
    "*"   -> (ModuleName.basics, "mul")
    "/"   -> (ModuleName.basics, "fdiv")
    "//"  -> (ModuleName.basics, "idiv")
    "=="  -> (ModuleName.basics, "eq")
    "/="  -> (ModuleName.basics, "neq")
    "<"   -> (ModuleName.basics, "lt")
    ">"   -> (ModuleName.basics, "gt")
    "<="  -> (ModuleName.basics, "le")
    ">="  -> (ModuleName.basics, "ge")
    "&&"  -> (ModuleName.basics, "and")
    "||"  -> (ModuleName.basics, "or")
    _     -> (ModuleName.basics, op)


-- | Placeholder annotation for operators (will be filled by type checker)
operatorAnnotation :: String -> Can.Annotation
operatorAnnotation _ = Can.Forall [] Can.TUnit  -- placeholder


-- ═══════════════════════════════════════════════════════════
-- LET EXPRESSIONS
-- ═══════════════════════════════════════════════════════════

-- | Canonicalise let-in expressions
canonicaliseLet :: Env.Env -> [A.Located Src.Def] -> Src.Expr -> Can.Expr_
canonicaliseLet env defs body =
    let
        -- Collect all binding names first (for mutual visibility).
        -- Destructure bindings contribute the names embedded in the pattern.
        nameFromDef (A.At _ d) = case d of
            Src.Destruct pat _ -> CanPat.patternNames pat
            Src.Define{}       -> case Src._defName d of A.At _ n -> [n]
        allNames = concatMap nameFromDef defs
        letEnv = Env.addLocals allNames env

        -- Canonicalise each definition
        canDefs = map (canonicaliseDef letEnv) defs
        canBody = canonicaliseExpr letEnv body
    in
    -- Fold defs into nested Can.Let (wrapping each in a Located)
    let wrapLet d bodyExpr = A.At A.one (Can.Let d bodyExpr)
    in A.toValue (foldr wrapLet canBody canDefs)


-- | Canonicalise a let binding. Destructure bindings (`let (a, b) = e`)
-- take a distinct Can.Def variant so the lowerer can emit real field
-- bindings for a/b rather than just a single __destruct__ name.
canonicaliseDef :: Env.Env -> A.Located Src.Def -> Can.Def
canonicaliseDef env (A.At _ def) = case def of
    Src.Destruct srcPat srcBody ->
        let canPat = CanPat.canonicalisePattern env srcPat
            canBody = canonicaliseExpr env srcBody
        in Can.DestructDef canPat canBody
    Src.Define{} ->
        let
            name = Src._defName def
            params = Src._defPatterns def
            paramNames = concatMap CanPat.patternNames params
            bodyEnv = Env.addLocals paramNames env
            canParams = map (CanPat.canonicalisePattern env) params
            canBody = canonicaliseExpr bodyEnv (Src._defBody def)
        in
        case Src._defType def of
            Nothing ->
                Can.Def name canParams canBody
            Just (A.At _ srcType) ->
                let home = Env._home env
                    canType = CanType.canonicaliseTypeAnnotation home srcType
                    freeVars = CanType.freeTypeVars srcType
                    typedPatterns = zip canParams (arrowArgs canType)
                in Can.TypedDef name freeVars typedPatterns canBody (arrowResult canType)
  where
    arrowArgs (Can.TLambda from to) = from : arrowArgs to
    arrowArgs _ = []
    arrowResult (Can.TLambda _ to) = arrowResult to
    arrowResult t = t


-- ═══════════════════════════════════════════════════════════
-- CASE EXPRESSIONS
-- ═══════════════════════════════════════════════════════════

-- | Canonicalise a case branch
canonicaliseCaseBranch :: Env.Env -> (Src.Pattern, Src.Expr) -> Can.CaseBranch
canonicaliseCaseBranch env (pat, body) =
    let patNames = CanPat.patternNames pat
        branchEnv = Env.addLocals patNames env
        canPat = CanPat.canonicalisePattern env pat
        canBody = canonicaliseExpr branchEnv body
    in Can.CaseBranch canPat canBody


-- ══════════════════════════════════════════════════════════════════════
-- Multiline string interpolation desugaring
--
-- `"""hello {{name}}! you are {{age}} years old"""` becomes:
--   "hello " ++ name ++ "! you are " ++ Debug.toString age ++ " years old"
--
-- Non-string interpolation arguments are wrapped in a stringify call at
-- canonicalise time. We parse the interpolation expression by invoking the
-- expression parser on the {{…}} body text; the parser is shared with the
-- rest of the compiler so syntax works identically inside the braces.
-- ══════════════════════════════════════════════════════════════════════

desugarMultiline :: Env.Env -> String -> Can.Expr_
desugarMultiline env raw =
    let chunks = splitInterpolation raw
        parts = map (chunkToExpr env) chunks
    in case parts of
        [] -> Can.Str ""
        [p] -> A.toValue p
        (p:rest) -> A.toValue (foldl concatAppend p rest)
  where
    concatAppend :: Can.Expr -> Can.Expr -> Can.Expr
    concatAppend a b =
        A.At A.one (Can.Binop "++" ModuleName.basics "append" appendAnnot a b)

    appendAnnot =
        Can.Forall ["a"] (Can.TLambda (Can.TVar "a") (Can.TLambda (Can.TVar "a") (Can.TVar "a")))


-- A chunk is either a literal piece or an expression piece.
data Chunk = Lit String | ExprChunk String deriving (Show)


-- Split a raw multiline string into alternating Lit / ExprChunk parts.
splitInterpolation :: String -> [Chunk]
splitInterpolation = go ""
  where
    go acc [] = emit acc []
    go acc ('{':'{':rest) =
        let (inside, after) = span (/= '}') rest
        in case after of
            ('}':'}':after') ->
                emit acc (ExprChunk inside : go "" after')
            _ -> go (acc ++ "{{") rest  -- unclosed {{; treat as literal
    go acc (c:rest) = go (acc ++ [c]) rest

    emit "" rest = rest
    emit lit rest = Lit lit : rest


chunkToExpr :: Env.Env -> Chunk -> Can.Expr
chunkToExpr _env (Lit s) = A.At A.one (Can.Str s)
chunkToExpr env (ExprChunk body) =
    -- Body is a single identifier, qualified name, or field access —
    -- resolve as a variable reference, then wrap in a stringify call.
    let trimmed = dropWhile (== ' ') (reverse (dropWhile (== ' ') (reverse body)))
        resolved = resolveInterpolationRef env trimmed
    in A.At A.one (Can.Call stringifyFn [resolved])
  where
    stringifyFn =
        A.At A.one (Can.VarKernel "Debug" "toString")


-- Parse a simple interpolation expression: one of
--   foo            — bare lowercase identifier
--   record.field   — field access
--   Module.func    — qualified value
--   func arg       — function call (e.g. errorToString e, String.fromInt n)
-- Anything more complex: fall back to a string of the literal {{...}} so
-- the developer sees their code in output (clear signal to simplify).
resolveInterpolationRef :: Env.Env -> String -> Can.Expr
resolveInterpolationRef env s =
    -- Check for function call (contains space)
    case break (== ' ') s of
        (func, ' ':argStr) | not (null func) && not (null argStr) ->
            let arg = dropWhile (== ' ') argStr
                funcExpr = resolveInterpolationRef env func
                argExpr  = resolveInterpolationRef env arg
            in A.At A.one (Can.Call funcExpr [argExpr])
        _ -> resolveSimpleRef env s
  where
    resolveSimpleRef env' s' = case break (== '.') s' of
        (name, "") ->
            case Env.lookupVar name env' of
                Just (Env.VarTopLevel home) ->
                    A.At A.one (Can.VarTopLevel home name)
                Just (Env.VarKernel modName fn) ->
                    A.At A.one (Can.VarKernel modName fn)
                _ ->
                    A.At A.one (Can.VarLocal name)
        (first, '.':rest) ->
            if not (null first) && Char.isUpper (head first)
                then
                    case Env.lookupImportAlias first env' of
                        Just canonical ->
                            let kernelMod = ModuleName.toString canonical
                            in A.At A.one (Can.VarKernel kernelMod rest)
                        Nothing ->
                            A.At A.one (Can.Str ("{{" ++ s' ++ "}}"))
                else
                    A.At A.one
                        (Can.Access
                            (A.At A.one (Can.VarLocal first))
                            (A.At A.one rest))
        _ -> A.At A.one (Can.Str ("{{" ++ s' ++ "}}"))
