-- | Top-level declaration parsing for Sky.
-- Handles: function defs, type annotations, type/alias/union declarations, foreign imports
module Sky.Parse.Declaration where

import Data.List (intercalate)
import qualified Data.Text as T
import Sky.Parse.Primitives
import Sky.Parse.Space (spaces, freshLine, skipWhitespace)
import Sky.Parse.Variable (lower, upper)
import Sky.Parse.Expression (expression)
import Sky.Parse.Pattern (pattern_)
import Sky.Parse.Type (typeAnnotation)
import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A


-- | Parse a top-level declaration
declaration :: (Row -> Col -> x) -> Parser x (DeclType, A.Located DeclPayload)
declaration mkError =
    oneOf mkError
        [ -- type declaration (alias or union)
          do keyword mkError (T.pack "type")
             spaces
             -- Peek for "alias" keyword without consuming
             oneOf mkError
                 [ do keyword mkError (T.pack "alias")
                      spaces
                      parseTypeAlias mkError
                 , parseUnionType mkError
                 ]

        , -- foreign import
          do keyword mkError (T.pack "foreign")
             spaces
             keyword mkError (T.pack "import")
             spaces
             parseForeignImport mkError

        , -- value definition or type annotation
          do name <- addLocation (lower mkError)
             spaces
             mc <- peek
             case mc of
                 Just ':' -> do
                     -- Type annotation: name : Type
                     char mkError ':'
                     spaces
                     ann <- addLocation (typeAnnotation mkError)
                     return (DeclAnnotation, A.At (A.toRegion name) (AnnotPayload (A.toValue name) ann))
                 _ -> do
                     -- Value definition: name params = body
                     params <- functionParams mkError
                     spaces
                     char mkError '='
                     freshLine mkError
                     bodyCol <- getCol
                     body <- withIndent bodyCol (expression mkError)
                     return (DeclValue, A.At (A.toRegion name) (ValuePayload (A.toValue name) params body Nothing))

        , -- Uppercase value (constructor used as function — from auto-generated record constructors)
          do name <- addLocation (upper mkError)
             spaces
             params <- functionParams mkError
             spaces
             char mkError '='
             freshLine mkError
             bodyCol <- getCol
             body <- withIndent bodyCol (expression mkError)
             return (DeclValue, A.At (A.toRegion name) (ValuePayload (A.toValue name) params body Nothing))
        ]


-- Declaration types
data DeclType
    = DeclValue
    | DeclAnnotation
    | DeclUnion
    | DeclAlias
    | DeclForeign
    deriving (Show, Eq)


data DeclPayload
    = ValuePayload String [Src.Pattern] Src.Expr (Maybe (A.Located Src.TypeAnnotation))
    | AnnotPayload String (A.Located Src.TypeAnnotation)
    | UnionPayload String [A.Located String] [A.Located (String, [Src.TypeAnnotation])]
    | AliasPayload String [A.Located String] (A.Located Src.TypeAnnotation)
    | ForeignPayload String String  -- Sky name, Go package
    deriving (Show)


-- | Parse function parameters (zero or more patterns)
functionParams :: (Row -> Col -> x) -> Parser x [Src.Pattern]
functionParams mkError =
    oneOfWithFallback
        [ do
            p <- pattern_ mkError
            spaces
            rest <- functionParams mkError
            return (p : rest)
        ]
        []


-- TYPE ALIAS

parseTypeAlias :: (Row -> Col -> x) -> Parser x (DeclType, A.Located DeclPayload)
parseTypeAlias mkError = do
    name <- addLocation (upper mkError)
    spaces
    vars <- typeVars mkError
    spaces
    char mkError '='
    freshLine mkError  -- type body may be on next line
    body <- addLocation (typeAnnotation mkError)
    return (DeclAlias, A.At (A.toRegion name) (AliasPayload (A.toValue name) vars body))


-- UNION TYPE

parseUnionType :: (Row -> Col -> x) -> Parser x (DeclType, A.Located DeclPayload)
parseUnionType mkError = do
    name <- addLocation (upper mkError)
    spaces
    vars <- typeVars mkError
    freshLine mkError  -- = may be on next line
    ctors <- unionConstructors mkError
    return (DeclUnion, A.At (A.toRegion name) (UnionPayload (A.toValue name) vars ctors))


typeVars :: (Row -> Col -> x) -> Parser x [A.Located String]
typeVars mkError =
    oneOfWithFallback
        [ do
            v <- addLocation (lower mkError)
            spaces
            rest <- typeVars mkError
            return (v : rest)
        ]
        []


unionConstructors :: (Row -> Col -> x) -> Parser x [A.Located (String, [Src.TypeAnnotation])]
unionConstructors mkError = do
    mc <- peek
    case mc of
        Just '=' -> do
            char mkError '='
            freshLine mkError  -- first constructor may be on next line
            first <- addLocation (unionConstructor mkError)
            rest <- moreUnionConstructors mkError
            return (first : rest)
        _ -> return []


-- | Parse more union constructors. Uses peek to check for | safely.
moreUnionConstructors :: (Row -> Col -> x) -> Parser x [A.Located (String, [Src.TypeAnnotation])]
moreUnionConstructors mkError = Parser $ \s cok eok cerr eerr ->
    let s' = skipWhitespace s
    in case T.uncons (_src s') of
        Just ('|', _) ->
            -- Found |, parse the constructor
            let (Parser p) = do
                    freshLine mkError
                    char mkError '|'
                    freshLine mkError
                    ctor <- addLocation (unionConstructor mkError)
                    rest <- moreUnionConstructors mkError
                    return (ctor : rest)
            in p s cok eok cerr eerr
        _ ->
            -- No more constructors
            eok [] s


unionConstructor :: (Row -> Col -> x) -> Parser x (String, [Src.TypeAnnotation])
unionConstructor mkError = do
    name <- upper mkError
    spaces
    args <- ctorTypeArgs mkError
    return (name, args)


ctorTypeArgs :: (Row -> Col -> x) -> Parser x [Src.TypeAnnotation]
ctorTypeArgs mkError =
    oneOfWithFallback
        [ do
            -- Only parse atomic types as constructor args (no arrows)
            arg <- typeAtomForCtor mkError
            spaces
            rest <- ctorTypeArgs mkError
            return (arg : rest)
        ]
        []


-- | Parse an atomic type suitable for constructor args
-- (no arrows, no application — just variables, names, parens, records)
typeAtomForCtor :: (Row -> Col -> x) -> Parser x Src.TypeAnnotation
typeAtomForCtor mkError =
    oneOf mkError
        [ -- Parenthesised
          do char mkError '('
             spaces
             t <- typeAnnotation mkError
             spaces
             char mkError ')'
             return t

        , -- Qualified type name:  Counter.Msg, Json.Value, …
          -- Parse a dotted chain of uppercase segments. The final segment
          -- is the type name; the rest form the module path.
          do first <- upper mkError
             rest <- dottedUpperSegments mkError
             case rest of
                 [] -> return (Src.TType "" [first] [])
                 _  ->
                     -- reconstruct the module path (everything except last)
                     let all_ = first : rest
                         modPath = intercalate "." (init all_)
                         nm = last all_
                     in return (Src.TTypeQual modPath nm [])

        , -- Type variable
          do name <- lower mkError
             return (Src.TVar name)
        ]


-- | Parse zero or more `.UpperIdent` segments. Used for qualified type
-- references like `Counter.Msg` or `Json.Decode.Value`.
dottedUpperSegments :: (Row -> Col -> x) -> Parser x [String]
dottedUpperSegments mkError = Parser $ \s cok eok cerr eerr ->
    case T.uncons (_src s) of
        Just ('.', rest1) ->
            case T.uncons rest1 of
                Just (c, _) | c >= 'A' && c <= 'Z' ->
                    let (Parser p) = do
                            char mkError '.'
                            name <- upper mkError
                            more <- dottedUpperSegments mkError
                            return (name : more)
                    in p s cok eok cerr eerr
                _ -> eok [] s
        _ -> eok [] s


-- FOREIGN IMPORT

parseForeignImport :: (Row -> Col -> x) -> Parser x (DeclType, A.Located DeclPayload)
parseForeignImport mkError = do
    -- foreign import "go/package" exposing (func1, func2)
    pkg <- stringLiteralSimple mkError
    return (DeclForeign, A.At A.one (ForeignPayload "" pkg))


-- | Simple string parser for foreign import paths
stringLiteralSimple :: (Row -> Col -> x) -> Parser x String
stringLiteralSimple mkError = Parser $ \s cok _eok _cerr eerr ->
    case T.uncons (_src s) of
        Just ('"', rest1) ->
            let (content, rest2) = T.break (== '"') rest1
            in case T.uncons rest2 of
                Just ('"', rest3) ->
                    let len = 1 + T.length content + 1
                    in cok (T.unpack content) (s { _src = rest3, _offset = _offset s + len, _col = _col s + len })
                _ -> eerr (_row s) (_col s) mkError
        _ -> eerr (_row s) (_col s) mkError
