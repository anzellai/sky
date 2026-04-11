-- | Expression parsing for Sky.
-- Handles all Sky expression forms including multiline strings,
-- let-in, case-of, if-then-else, lambdas, operators, function application.
module Sky.Parse.Expression where

import qualified Data.Text as T
import Sky.Parse.Primitives
import Sky.Parse.Space (spaces, freshLine, checkIndent, skipWhitespace)
import Sky.Parse.Variable (lower, upper)
import Sky.Parse.Number (number, Number(..))
import Sky.Parse.String (stringLiteral, charLiteral, StringResult(..))
import Sky.Parse.Symbol (operator)
import Sky.Parse.Pattern (pattern_)
import Sky.Parse.Type (typeAnnotation)
import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A


-- | Parse an expression (top-level, handles binary operators)
expression :: (Row -> Col -> x) -> Parser x Src.Expr
expression mkError = do
    expr1 <- addLocation (exprApp mkError)
    spaces
    -- Check for binary operators
    binopRest mkError expr1


-- | Parse binary operator continuation
binopRest :: (Row -> Col -> x) -> Src.Expr -> Parser x Src.Expr
binopRest mkError left =
    oneOfWithFallback
        [ do
            op <- addLocation (operator mkError)
            spaces
            right <- addLocation (exprApp mkError)
            spaces
            -- Build up the binop chain
            let chain = Src.Binops [(left, op)] right
            -- Continue looking for more operators
            addLocation (return chain) >>= \located ->
                binopRest mkError located
        ]
        left


-- | Parse function application: f a b c
exprApp :: (Row -> Col -> x) -> Parser x Src.Expr_
exprApp mkError = do
    func <- exprAtom mkError
    spaces
    args <- appArgs mkError
    case args of
        [] -> return (A.toValue func)
        _  -> return (Src.Call func args)


-- | Parse zero or more application arguments
appArgs :: (Row -> Col -> x) -> Parser x [Src.Expr]
appArgs mkError =
    oneOfWithFallback
        [ do
            col <- getCol
            indent <- getIndent
            if col > indent
                then do
                    arg <- addLocation (exprAtom_ mkError)
                    spaces
                    rest <- appArgs mkError
                    return (arg : rest)
                else return []
        ]
        []


-- | Parse an atomic expression (no application or operators)
exprAtom :: (Row -> Col -> x) -> Parser x Src.Expr
exprAtom mkError = addLocation (exprAtom_ mkError)


exprAtom_ :: (Row -> Col -> x) -> Parser x Src.Expr_
exprAtom_ mkError =
    oneOf mkError
        [ -- Parenthesised / tuple / unit
          do char mkError '('
             spaces
             mc <- peek
             case mc of
                 Just ')' -> do
                     char mkError ')'
                     return Src.Unit
                 _ -> do
                     e1 <- expression mkError
                     spaces
                     mc2 <- peek
                     case mc2 of
                         Just ',' -> do
                             char mkError ','
                             spaces
                             e2 <- expression mkError
                             more <- tupleRest mkError
                             spaces
                             char mkError ')'
                             return (Src.Tuple e1 e2 more)
                         Just ')' -> do
                             char mkError ')'
                             return (A.toValue e1)
                         _ -> error "Expected , or ) in expression"

        , -- List literal: [a, b, c]
          do char mkError '['
             spaces
             mc <- peek
             case mc of
                 Just ']' -> do
                     char mkError ']'
                     return (Src.List [])
                 _ -> do
                     first <- expression mkError
                     rest <- listRest mkError
                     spaces
                     char mkError ']'
                     return (Src.List (first : rest))

        , -- Record literal or update: { field = val } or { r | field = val }
          do char mkError '{'
             spaces
             mc <- peek
             case mc of
                 Just '}' -> do
                     char mkError '}'
                     return (Src.Record [])
                 _ -> do
                     -- Could be record literal or record update
                     name <- addLocation (lower mkError)
                     spaces
                     mc2 <- peek
                     case mc2 of
                         Just '|' -> do
                             -- Record update: { name | field = val, ... }
                             char mkError '|'
                             spaces
                             fields <- recordFields mkError
                             spaces
                             char mkError '}'
                             return (Src.Update name fields)
                         Just '=' -> do
                             -- Record literal starting with name = val
                             char mkError '='
                             spaces
                             val <- expression mkError
                             rest <- recordFieldsRest mkError
                             spaces
                             char mkError '}'
                             return (Src.Record ((name, val) : rest))
                         _ -> error "Expected | or = after record field name"

        , -- Negate: -expr
          do char mkError '-'
             e <- addLocation (exprAtom_ mkError)
             return (Src.Negate e)

        , -- Lambda: \x y -> body
          do char mkError '\\'
             spaces
             params <- lambdaParams mkError
             spaces
             string mkError (T.pack "->")
             spaces
             body <- expression mkError
             return (Src.Lambda params body)

        , -- If-then-else
          do keyword mkError (T.pack "if")
             spaces
             exprIf mkError

        , -- Let-in
          do keyword mkError (T.pack "let")
             spaces
             exprLet mkError

        , -- Case-of
          do keyword mkError (T.pack "case")
             spaces
             exprCase mkError

        , -- String literals (single-line and multiline)
          do s <- stringLiteral mkError
             return $ case s of
                 SingleLine str -> Src.Str str
                 MultiLine str  -> Src.MultilineStr str
                 CharLit _      -> error "char in string context"

        , -- Char literal
          do s <- charLiteral mkError
             return $ case s of
                 CharLit c -> Src.Chr c
                 _         -> error "expected char"

        , -- Number
          do n <- number mkError
             return $ case n of
                 IntNum i   -> Src.Int i
                 FloatNum f -> Src.Float f

        , -- Qualified variable or constructor: Module.name
          do first <- upper mkError
             mc <- peek
             case mc of
                 Just '.' -> do
                     char mkError '.'
                     name <- oneOf mkError [lower mkError, upper mkError]
                     return (Src.VarQual first name)
                 _ -> return (Src.Var first)  -- Bare constructor

        , -- Variable or accessor
          do name <- lower mkError
             return (Src.Var name)

        , -- Record accessor: .field
          do char mkError '.'
             name <- lower mkError
             return (Src.Accessor name)
        ]


-- IF-THEN-ELSE

exprIf :: (Row -> Col -> x) -> Parser x Src.Expr_
exprIf mkError = do
    cond <- expression mkError
    spaces
    keyword mkError (T.pack "then")
    spaces
    thenBranch <- expression mkError
    spaces
    elseIfs <- elseIfChain mkError
    keyword mkError (T.pack "else")
    spaces
    elseBranch <- expression mkError
    return (Src.If ((cond, thenBranch) : elseIfs) elseBranch)


elseIfChain :: (Row -> Col -> x) -> Parser x [(Src.Expr, Src.Expr)]
elseIfChain mkError =
    oneOfWithFallback
        [ do
            keyword mkError (T.pack "else")
            spaces
            keyword mkError (T.pack "if")
            spaces
            cond <- expression mkError
            spaces
            keyword mkError (T.pack "then")
            spaces
            body <- expression mkError
            spaces
            rest <- elseIfChain mkError
            return ((cond, body) : rest)
        ]
        []


-- LET-IN

exprLet :: (Row -> Col -> x) -> Parser x Src.Expr_
exprLet mkError = do
    freshLine mkError
    bindingCol <- getCol
    bindings <- letBindings mkError bindingCol
    freshLine mkError
    keyword mkError (T.pack "in")
    freshLine mkError
    body <- expression mkError
    return (Src.Let bindings body)


-- | Parse let bindings with column tracking.
-- All bindings must start at the SAME column (bindingCol).
-- This is the fix for the parser bug in the self-hosted compiler.
letBindings :: (Row -> Col -> x) -> Col -> Parser x [A.Located Src.Def]
letBindings mkError bindingCol = do
    first <- addLocation (letBinding mkError)
    spaces
    rest <- moreLetBindings mkError bindingCol
    return (first : rest)


moreLetBindings :: (Row -> Col -> x) -> Col -> Parser x [A.Located Src.Def]
moreLetBindings mkError bindingCol = do
    col <- getCol
    src <- peekSrc
    if col == bindingCol && not (isInKeyword src)
        then do
            b <- addLocation (letBinding mkError)
            spaces
            rest <- moreLetBindings mkError bindingCol
            return (b : rest)
        else return []
  where
    isInKeyword src =
        T.length src >= 2
            && T.take 2 src == T.pack "in"
            && (T.length src < 3 || not (isIdentContinue (T.index src 2)))
    isIdentContinue c = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_'


letBinding :: (Row -> Col -> x) -> Parser x Src.Def
letBinding mkError = do
    name <- addLocation (lower mkError)
    spaces
    params <- lambdaParams_ mkError
    spaces
    char mkError '='
    freshLine mkError
    body <- expression mkError
    return (Src.Def name params body Nothing)


-- | Parse zero or more lambda/function parameters
lambdaParams :: (Row -> Col -> x) -> Parser x [Src.Pattern]
lambdaParams mkError = do
    first <- pattern_ mkError
    rest <- lambdaParams_ mkError
    return (first : rest)


lambdaParams_ :: (Row -> Col -> x) -> Parser x [Src.Pattern]
lambdaParams_ mkError =
    oneOfWithFallback
        [ do
            p <- pattern_ mkError
            spaces
            rest <- lambdaParams_ mkError
            return (p : rest)
        ]
        []


-- CASE-OF

exprCase :: (Row -> Col -> x) -> Parser x Src.Expr_
exprCase mkError = do
    subject <- expression mkError
    spaces
    keyword mkError (T.pack "of")
    spaces
    branchCol <- getCol
    branches <- caseBranches mkError branchCol
    return (Src.Case subject branches)


-- | Parse case branches. Each branch must start at branchCol.
-- This is the column-tracked version that avoids the self-hosted compiler's bug.
caseBranches :: (Row -> Col -> x) -> Col -> Parser x [(Src.Pattern, Src.Expr)]
caseBranches mkError branchCol = do
    first <- caseBranch mkError
    spaces
    rest <- moreCaseBranches mkError branchCol
    return (first : rest)


moreCaseBranches :: (Row -> Col -> x) -> Col -> Parser x [(Src.Pattern, Src.Expr)]
moreCaseBranches mkError branchCol = do
    col <- getCol
    if col == branchCol
        then oneOfWithFallback
            [ do
                b <- caseBranch mkError
                spaces
                rest <- moreCaseBranches mkError branchCol
                return (b : rest)
            ]
            []
        else return []


caseBranch :: (Row -> Col -> x) -> Parser x (Src.Pattern, Src.Expr)
caseBranch mkError = do
    pat <- pattern_ mkError
    spaces
    string mkError (T.pack "->")
    spaces
    body <- expression mkError
    return (pat, body)


-- HELPERS

tupleRest :: (Row -> Col -> x) -> Parser x [Src.Expr]
tupleRest mkError =
    oneOfWithFallback
        [ do
            spaces
            char mkError ','
            spaces
            e <- expression mkError
            rest <- tupleRest mkError
            return (e : rest)
        ]
        []


listRest :: (Row -> Col -> x) -> Parser x [Src.Expr]
listRest mkError =
    oneOfWithFallback
        [ do
            spaces
            char mkError ','
            spaces
            e <- expression mkError
            rest <- listRest mkError
            return (e : rest)
        ]
        []


recordFields :: (Row -> Col -> x) -> Parser x [(A.Located String, Src.Expr)]
recordFields mkError = do
    first <- recordField mkError
    rest <- recordFieldsRest mkError
    return (first : rest)


recordField :: (Row -> Col -> x) -> Parser x (A.Located String, Src.Expr)
recordField mkError = do
    name <- addLocation (lower mkError)
    spaces
    char mkError '='
    spaces
    val <- expression mkError
    return (name, val)


recordFieldsRest :: (Row -> Col -> x) -> Parser x [(A.Located String, Src.Expr)]
recordFieldsRest mkError =
    oneOfWithFallback
        [ do
            spaces
            char mkError ','
            spaces
            field <- recordField mkError
            rest <- recordFieldsRest mkError
            return (field : rest)
        ]
        []
