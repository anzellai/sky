-- | Type annotation parsing for Sky.
-- Syntax: Int, String, a -> b, List Int, { name : String, age : Int }
module Sky.Parse.Type where

import qualified Data.Text as T
import Sky.Parse.Primitives
import Sky.Parse.Space (spaces, freshLine, skipWhitespace)
import Sky.Parse.Variable (lower, upper, isLowerLike, isIdentChar)
import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A


-- | Parse a type annotation (top level — handles ->)
typeAnnotation :: (Row -> Col -> x) -> Parser x Src.TypeAnnotation
typeAnnotation mkError = do
    t <- typeApply mkError
    spaces
    -- Check for ->
    mc <- peek
    case mc of
        Just '-' ->
            oneOfWithFallback
                [ do
                    string mkError (T.pack "->")
                    spaces
                    ret <- typeAnnotation mkError
                    return (Src.TLambda t ret)
                ]
                t
        _ -> return t


-- | Parse a type application: Maybe Int, Result String Int, etc.
typeApply :: (Row -> Col -> x) -> Parser x Src.TypeAnnotation
typeApply mkError = do
    base <- typeAtom mkError
    spaces
    args <- typeArgs mkError
    case args of
        [] -> return base
        _  -> case base of
            Src.TType mod name [] -> return (Src.TType mod name args)
            Src.TTypeQual mod name [] -> return (Src.TTypeQual mod name args)
            _ -> return base  -- shouldn't apply args to non-named type


-- | Parse zero or more type arguments (atomic types)
typeArgs :: (Row -> Col -> x) -> Parser x [Src.TypeAnnotation]
typeArgs mkError =
    oneOfWithFallback
        [ do
            arg <- typeAtom mkError
            spaces
            rest <- typeArgs mkError
            return (arg : rest)
        ]
        []


-- | Parse an atomic type (no application or arrow)
typeAtom :: (Row -> Col -> x) -> Parser x Src.TypeAnnotation
typeAtom mkError =
    oneOf mkError
        [ -- Unit type: ()
          do char mkError '('
             spaces
             mc <- peek
             case mc of
                 Just ')' -> do
                     char mkError ')'
                     return Src.TUnit
                 _ -> do
                     -- Parenthesised type or tuple
                     t1 <- typeAnnotation mkError
                     spaces
                     mc2 <- peek
                     case mc2 of
                         Just ',' -> do
                             char mkError ','
                             spaces
                             t2 <- typeAnnotation mkError
                             more <- typeTupleRest mkError
                             spaces
                             char mkError ')'
                             return (Src.TTuple t1 t2 more)
                         Just ')' -> do
                             char mkError ')'
                             return t1
                         _ -> failParse mkError

        , -- Record type: { field : Type, ... }
          -- Cycle 4 D6: also accept the row-polymorphic form
          --     { r | field : Type, ... }
          -- where `r` is a lowercase row variable.  The AST slot
          -- (`Src.TRecord fields (Maybe String)`), canonicaliser,
          -- HM Instantiate, and Unify all already support open
          -- records — the parser was the only missing surface.
          do char mkError '{'
             freshLine mkError  -- field may be on next line
             mc <- peek
             case mc of
                 Just '}' -> do
                     char mkError '}'
                     return (Src.TRecord [] Nothing)
                 _ -> do
                     -- Use a non-consuming lookahead to decide
                     -- between the row-poly form (`<lower> |`) and
                     -- the closed form (`<lower> :`).  `lower`
                     -- consumes on success, and once a parser has
                     -- consumed input the surrounding bind converts
                     -- any later empty-fail into a consumed-fail
                     -- (Primitives.hs:93-102 `>>=` definition) which
                     -- `oneOfWithFallback` won't catch — so we must
                     -- branch BEFORE consuming.
                     isRowPoly <- peekRowPolyIntro
                     if isRowPoly
                         then do
                             rowVar <- lower mkError
                             spaces
                             char mkError '|'
                             freshLine mkError
                             fields <- typeRecordFields mkError
                             freshLine mkError
                             char mkError '}'
                             return (Src.TRecord fields (Just rowVar))
                         else do
                             fields <- typeRecordFields mkError
                             freshLine mkError
                             char mkError '}'
                             return (Src.TRecord fields Nothing)

        , -- Type constructor: Maybe, List, MyType, or qualified: Set.Set
          do name <- upper mkError
             spaces
             mc <- peek
             case mc of
                 Just '.' ->
                     oneOfWithFallback
                         [ do
                             char mkError '.'
                             qname <- upper mkError
                             return (Src.TTypeQual name qname [])
                         ]
                         (Src.TType "" [name] [])
                 _ -> return (Src.TType "" [name] [])

        , -- Type variable: a, b, comparable
          do name <- lower mkError
             return (Src.TVar name)
        ]


-- | Parse remaining tuple type elements
typeTupleRest :: (Row -> Col -> x) -> Parser x [Src.TypeAnnotation]
typeTupleRest mkError =
    oneOfWithFallback
        [ do
            spaces
            char mkError ','
            spaces
            t <- typeAnnotation mkError
            rest <- typeTupleRest mkError
            return (t : rest)
        ]
        []


-- | Parse record type fields: name : Type, name2 : Type2
typeRecordFields :: (Row -> Col -> x) -> Parser x [(A.Located String, Src.TypeAnnotation)]
typeRecordFields mkError = do
    first <- typeRecordField mkError
    rest <- typeRecordFieldsRest mkError
    return (first : rest)


typeRecordField :: (Row -> Col -> x) -> Parser x (A.Located String, Src.TypeAnnotation)
typeRecordField mkError = do
    name <- addLocation (lower mkError)
    spaces
    char mkError ':'
    spaces
    t <- typeAnnotation mkError
    return (name, t)


-- | Parse more record fields. Uses peek to check for , safely.
typeRecordFieldsRest :: (Row -> Col -> x) -> Parser x [(A.Located String, Src.TypeAnnotation)]
typeRecordFieldsRest mkError = Parser $ \s _ eok _ _ ->
    let s' = skipWhitespace s
    in case T.uncons (_src s') of
        Just (',', _) ->
            let (Parser p) = do
                    freshLine mkError
                    char mkError ','
                    freshLine mkError
                    field <- typeRecordField mkError
                    rest <- typeRecordFieldsRest mkError
                    return (field : rest)
            in p s (\a s2 -> eok a s2) eok (\r c m -> eok [] s) (\r c m -> eok [] s)
        _ ->
            eok [] s


-- | Non-consuming lookahead for the row-polymorphic record opener
-- `<lowerIdent> [whitespace] |`. Used by the record-type parser to
-- decide between the closed form (`{ field : Type, … }`) and the
-- row-poly form (`{ r | field : Type, … }`) WITHOUT first consuming
-- the identifier (consumed input cannot be backtracked through
-- `oneOfWithFallback`).
peekRowPolyIntro :: Parser x Bool
peekRowPolyIntro = Parser $ \s _cok eok _cerr _eerr ->
    let src = _src s
    in case T.uncons src of
        Just (c, _)
            | isLowerLike c ->
                let (name, rest) = T.span isIdentChar src
                    afterName    = T.dropWhile (\ch -> ch == ' ' || ch == '\t') rest
                in case T.uncons afterName of
                    -- A '|' immediately (mod inline whitespace) after
                    -- the lowercase identifier is the row-poly
                    -- separator.  A '|' from a record-update sigil
                    -- (`{ rec | field = v }`) is in EXPRESSION land,
                    -- never TYPE land, so we never see one here for
                    -- the field-assignment-update meaning.
                    Just ('|', _) | not (T.null name) -> eok True s
                    _                                 -> eok False s
        _ -> eok False s
