-- | Module header parsing for Sky.
-- Parses: module declaration, imports, and collects all top-level declarations
module Sky.Parse.Module where

import qualified Data.Text as T
import Sky.Parse.Primitives
import Sky.Parse.Space (spaces, freshLine)
import Sky.Parse.Variable (lower, upper)
import Sky.Parse.Declaration (declaration, DeclType(..), DeclPayload(..))
import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A


-- | Parse error for module level
data ModuleError
    = ModuleExpected Row Col
    | ModuleNameExpected Row Col
    | ImportExpected Row Col
    | DeclarationError Row Col
    deriving (Show)


-- | Parse a complete Sky module
parseModule :: T.Text -> Either ModuleError Src.Module
parseModule src =
    fromText moduleParser (\r c -> ModuleExpected r c) src


moduleParser :: Parser ModuleError Src.Module
moduleParser = do
    spaces
    -- Parse module header
    mHeader <- moduleHeader
    spaces
    -- Parse imports
    imports <- moduleImports
    spaces
    -- Parse declarations
    (values, unions, aliases, binops) <- moduleDeclarations
    spaces
    end (\r c -> ModuleExpected r c)
    return Src.Module
        { Src._name = mHeader
        , Src._exports = A.At A.one Src.ExposingAll  -- TODO: parse exposing clause
        , Src._docs = Src.NoDocs
        , Src._imports = imports
        , Src._values = values
        , Src._unions = unions
        , Src._aliases = aliases
        , Src._binops = binops
        }


-- | Parse module header: module Name.Space exposing (..)
moduleHeader :: Parser ModuleError (Maybe (A.Located [String]))
moduleHeader =
    oneOfWithFallback
        [ do
            keyword (\r c -> ModuleExpected r c) (T.pack "module")
            spaces
            name <- addLocation (moduleName (\r c -> ModuleNameExpected r c))
            spaces
            -- Parse exposing clause
            keyword (\r c -> ModuleExpected r c) (T.pack "exposing")
            spaces
            _ <- exposingClause (\r c -> ModuleExpected r c)
            return (Just name)
        ]
        Nothing


-- | Parse a dotted module name: Sky.Core.List
moduleName :: (Row -> Col -> ModuleError) -> Parser ModuleError [String]
moduleName mkError = do
    first <- upper mkError
    rest <- moduleNameParts mkError
    return (first : rest)


moduleNameParts :: (Row -> Col -> ModuleError) -> Parser ModuleError [String]
moduleNameParts mkError =
    oneOfWithFallback
        [ do
            char mkError '.'
            part <- upper mkError
            rest <- moduleNameParts mkError
            return (part : rest)
        ]
        []


-- | Parse exposing clause: (..) or (name1, name2, Type(..))
exposingClause :: (Row -> Col -> ModuleError) -> Parser ModuleError Src.Exposing
exposingClause mkError = do
    char mkError '('
    spaces
    mc <- peek
    case mc of
        Just '.' -> do
            string mkError (T.pack "..")
            spaces
            char mkError ')'
            return Src.ExposingAll
        _ -> do
            items <- exposedItems mkError
            spaces
            char mkError ')'
            return (Src.ExposingList items)


exposedItems :: (Row -> Col -> ModuleError) -> Parser ModuleError [A.Located Src.Exposed]
exposedItems mkError = do
    first <- addLocation (exposedItem mkError)
    rest <- moreExposedItems mkError
    return (first : rest)


moreExposedItems :: (Row -> Col -> ModuleError) -> Parser ModuleError [A.Located Src.Exposed]
moreExposedItems mkError =
    oneOfWithFallback
        [ do
            spaces
            char mkError ','
            spaces
            item <- addLocation (exposedItem mkError)
            rest <- moreExposedItems mkError
            return (item : rest)
        ]
        []


exposedItem :: (Row -> Col -> ModuleError) -> Parser ModuleError Src.Exposed
exposedItem mkError =
    oneOf mkError
        [ -- Type with constructors: Type(..)
          do name <- upper mkError
             mc <- peek
             case mc of
                 Just '(' -> do
                     char mkError '('
                     string mkError (T.pack "..")
                     char mkError ')'
                     return (Src.ExposedType name Src.Public)
                 _ -> return (Src.ExposedType name Src.Private)

        , -- Operator: (+)
          do char mkError '('
             op <- operatorStr mkError
             char mkError ')'
             return (Src.ExposedOperator op)

        , -- Value
          do name <- lower mkError
             return (Src.ExposedValue name)
        ]


operatorStr :: (Row -> Col -> ModuleError) -> Parser ModuleError String
operatorStr mkError = Parser $ \s cok _eok _cerr eerr ->
    let (op, rest) = T.span isOpChar (_src s)
    in if T.null op
        then eerr (_row s) (_col s) mkError
        else cok (T.unpack op) (s { _src = rest, _offset = _offset s + T.length op, _col = _col s + T.length op })
  where
    isOpChar c = c `elem` ("+-*/<>=!&|^~%?@#$:.\\" :: [Char])


-- | Parse imports
moduleImports :: Parser ModuleError [Src.Import]
moduleImports =
    oneOfWithFallback
        [ do
            imp <- moduleImport
            spaces
            rest <- moduleImports
            return (imp : rest)
        ]
        []


moduleImport :: Parser ModuleError Src.Import
moduleImport = do
    keyword (\r c -> ImportExpected r c) (T.pack "import")
    spaces
    name <- addLocation (moduleName (\r c -> ImportExpected r c))
    spaces
    alias <- importAlias
    spaces
    expo <- importExposing
    return Src.Import
        { Src._importName = name
        , Src._importAlias = alias
        , Src._importExposing = A.At A.one expo
        }


importAlias :: Parser ModuleError (Maybe String)
importAlias =
    oneOfWithFallback
        [ do
            keyword (\r c -> ImportExpected r c) (T.pack "as")
            spaces
            name <- upper (\r c -> ImportExpected r c)
            return (Just name)
        ]
        Nothing


importExposing :: Parser ModuleError Src.Exposing
importExposing =
    oneOfWithFallback
        [ do
            keyword (\r c -> ImportExpected r c) (T.pack "exposing")
            spaces
            exposingClause (\r c -> ImportExpected r c)
        ]
        (Src.ExposingList [])


-- | Parse all declarations
moduleDeclarations :: Parser ModuleError ([A.Located Src.Value], [A.Located Src.Union], [A.Located Src.Alias], [A.Located Src.Infix])
moduleDeclarations = go [] [] [] []
  where
    go vals unions aliases binops =
        oneOfWithFallback
            [ do
                (declType, payload) <- declaration (\r c -> DeclarationError r c)
                spaces
                case declType of
                    DeclValue ->
                        case A.toValue payload of
                            ValuePayload name params body ann ->
                                let v = Src.Value (A.At (A.toRegion payload) name) params body ann
                                in go (A.At (A.toRegion payload) v : vals) unions aliases binops
                            _ -> go vals unions aliases binops
                    DeclAnnotation ->
                        -- TODO: attach annotation to the next value declaration
                        go vals unions aliases binops
                    DeclUnion ->
                        case A.toValue payload of
                            UnionPayload name vars ctors ->
                                let u = Src.Union (A.At (A.toRegion payload) name) vars ctors
                                in go vals (A.At (A.toRegion payload) u : unions) aliases binops
                            _ -> go vals unions aliases binops
                    DeclAlias ->
                        case A.toValue payload of
                            AliasPayload name vars body ->
                                let a = Src.Alias (A.At (A.toRegion payload) name) vars body
                                in go vals unions (A.At (A.toRegion payload) a : aliases) binops
                            _ -> go vals unions aliases binops
                    DeclForeign ->
                        go vals unions aliases binops
            ]
            (reverse vals, reverse unions, reverse aliases, reverse binops)
