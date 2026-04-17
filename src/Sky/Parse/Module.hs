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


-- | Parse a complete Sky module.
--
-- Audit P2-1: also does a raw-text scan of the source for comments
-- and attaches them to `Src._comments`. The combinator-level parser
-- stays comment-free (comments are whitespace as far as layout /
-- indentation are concerned); the formatter uses this list to
-- round-trip comments through `sky fmt`.
parseModule :: T.Text -> Either ModuleError Src.Module
parseModule src =
    case fromText moduleParser (\r c -> ModuleExpected r c) src of
        Left e  -> Left e
        Right m -> Right m { Src._comments = collectComments src }


-- Walk the source text row-by-row, emitting one A.Located String per
-- line/block comment. Block comments span multiple rows and are
-- emitted with a Region covering the full start..end span. Contents
-- are stored WITHOUT the `--`, `{-`, `-}` delimiters — the formatter
-- re-inserts them on emit.
collectComments :: T.Text -> [A.Located String]
collectComments src = go 1 1 (T.unpack src) []
  where
    go _ _ []            acc = reverse acc
    go r _ ('\n':xs)     acc = go (r + 1) 1 xs acc
    go r c ('-':'-':xs)  acc =
        let (body, rest) = span (/= '\n') xs
            endCol       = c + 2 + length body
            region       = A.Region (A.Position r c) (A.Position r endCol)
        in go r endCol rest (A.At region (trimStart body) : acc)
    go r c ('{':'-':xs)  acc =
        let (body, rest, r', c', consumed) = takeBlockBody xs r (c + 2) 1
            region = A.Region (A.Position r c) (A.Position r' c')
        in if consumed
             then go r' c' rest (A.At region body : acc)
             else reverse acc   -- unclosed block; stop scanning to avoid run-on
    go r c ('"':xs)      acc = skipString r (c + 1) xs acc
    go r c (_:xs)        acc = go r (c + 1) xs acc

    -- Skip a Sky double-quoted string literal so a `--` inside a
    -- string isn't treated as a line comment. Triple-quoted strings
    -- also supported (scanner just treats them as nested quotes).
    skipString r c []            acc = reverse acc
    skipString r _ ('\n':xs)     acc = go (r + 1) 1 xs acc
    skipString r c ('\\':_:xs)   acc = skipString r (c + 2) xs acc
    skipString r c ('"':xs)      acc = go r (c + 1) xs acc
    skipString r c (_:xs)        acc = skipString r (c + 1) xs acc

    -- Consume block body; returns (body, rest, endRow, endCol, closed).
    -- Handles nested `{- -}` per Sky's documented lexer rules.
    takeBlockBody :: String -> Int -> Int -> Int -> (String, String, Int, Int, Bool)
    takeBlockBody []             r c _    = ("", "", r, c, False)
    takeBlockBody ('-':'}':rest) r c 1    = ("", rest, r, c + 2, True)
    takeBlockBody ('-':'}':rest) r c d    =
        let (b, rr, rrr, rc, ok) = takeBlockBody rest r (c + 2) (d - 1)
        in ("-}" ++ b, rr, rrr, rc, ok)
    takeBlockBody ('{':'-':rest) r c d    =
        let (b, rr, rrr, rc, ok) = takeBlockBody rest r (c + 2) (d + 1)
        in ("{-" ++ b, rr, rrr, rc, ok)
    takeBlockBody ('\n':rest)    r _ d    =
        let (b, rr, rrr, rc, ok) = takeBlockBody rest (r + 1) 1 d
        in ('\n':b, rr, rrr, rc, ok)
    takeBlockBody (x:rest)       r c d    =
        let (b, rr, rrr, rc, ok) = takeBlockBody rest r (c + 1) d
        in (x:b, rr, rrr, rc, ok)

    trimStart = dropWhile (== ' ')


moduleParser :: Parser ModuleError Src.Module
moduleParser = do
    freshLine (\r c -> ModuleExpected r c)
    -- Parse module header
    (mHeader, mExports) <- moduleHeader
    freshLine (\r c -> ModuleExpected r c)
    -- Parse imports
    imports <- moduleImports
    freshLine (\r c -> ModuleExpected r c)
    -- Parse declarations
    (values, unions, aliases, binops) <- moduleDeclarations
    freshLine (\r c -> ModuleExpected r c)
    -- Allow trailing whitespace before EOF
    end (\r c -> ModuleExpected r c)
    -- A module without a header exposes everything (legacy behaviour for
    -- fixtures and REPL-style inputs). An explicit header carries its own
    -- exposing list.
    let exports = case mExports of
            Just e  -> e
            Nothing -> A.At A.one Src.ExposingAll
    return Src.Module
        { Src._name = mHeader
        , Src._exports = exports
        , Src._docs = Src.NoDocs
        , Src._imports = imports
        , Src._values = values
        , Src._unions = unions
        , Src._aliases = aliases
        , Src._binops = binops
        , Src._comments = []   -- populated by parseModule's post-scan
        }


-- | Parse module header: module Name.Space exposing (..)
-- Returns both the module name and the located exposing clause so the
-- module builder can honour it (P2). Prior to this the parsed exposing
-- was discarded and every module behaved as `exposing (..)`.
moduleHeader :: Parser ModuleError (Maybe (A.Located [String]), Maybe (A.Located Src.Exposing))
moduleHeader = do
    mc <- peek
    case mc of
        Just 'm' -> do
            keyword (\r c -> ModuleExpected r c) (T.pack "module")
            spaces
            name <- addLocation (moduleName (\r c -> ModuleNameExpected r c))
            spaces
            keyword (\r c -> ModuleExpected r c) (T.pack "exposing")
            spaces
            expo <- addLocation (exposingClause (\r c -> ModuleExpected r c))
            return (Just name, Just expo)
        _ -> return (Nothing, Nothing)


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


-- | Parse remaining ctor names in `Type(A, B, C)` — called AFTER the first name.
exposedCtorRest :: (Row -> Col -> ModuleError) -> Parser ModuleError [String]
exposedCtorRest mkError =
    oneOfWithFallback
        [ do
            spaces
            char mkError ','
            spaces
            c <- upper mkError
            rest <- exposedCtorRest mkError
            return (c : rest)
        ]
        []


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
        [ -- Type with constructors: Type(..) | Type(CtorA, CtorB) | Type
          do name <- upper mkError
             mc <- peek
             case mc of
                 Just '(' -> do
                     char mkError '('
                     spaces
                     -- Either `..)` (all) or comma-separated ctor names.
                     mc2 <- peek
                     privacy <- case mc2 of
                         Just '.' -> do
                             string mkError (T.pack "..")
                             spaces
                             char mkError ')'
                             return Src.Public
                         _ -> do
                             c0 <- upper mkError
                             rest <- exposedCtorRest mkError
                             spaces
                             char mkError ')'
                             return (Src.PublicCtors (c0 : rest))
                     return (Src.ExposedType name privacy)
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
            freshLine (\r c -> ImportExpected r c)
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


-- | Parse all declarations.
--
-- Value-level type annotations (the `foo : T` line that precedes
-- `foo args = body`) are parsed as a separate DeclAnnotation payload
-- but belong logically to the next DeclValue of the same name. We
-- carry them through as `pendingAnns` (keyed by name) and splice them
-- into the matching Src.Value. Unmatched annotations are dropped — the
-- parser allows them for forward-declared type signatures, which the
-- rest of the pipeline does not yet consume.
moduleDeclarations :: Parser ModuleError ([A.Located Src.Value], [A.Located Src.Union], [A.Located Src.Alias], [A.Located Src.Infix])
moduleDeclarations = go [] [] [] [] []
  where
    -- pendingAnns :: [(name, annot)]  most-recent first
    go vals unions aliases binops pendingAnns =
        oneOfWithFallback
            [ do
                (declType, payload) <- declaration (\r c -> DeclarationError r c)
                freshLine (\r c -> DeclarationError r c)
                case declType of
                    DeclValue ->
                        case A.toValue payload of
                            ValuePayload name params body inlineAnn ->
                                let
                                    -- Use any inline annotation first; otherwise
                                    -- the most recent matching pending one.
                                    (ann, pendingAnns') = case inlineAnn of
                                        Just a  -> (Just a, pendingAnns)
                                        Nothing -> popAnnotation name pendingAnns
                                    v = Src.Value (A.At (A.toRegion payload) name) params body ann
                                in go (A.At (A.toRegion payload) v : vals) unions aliases binops pendingAnns'
                            _ -> go vals unions aliases binops pendingAnns
                    DeclAnnotation ->
                        case A.toValue payload of
                            AnnotPayload name annot ->
                                go vals unions aliases binops ((name, annot) : pendingAnns)
                            _ -> go vals unions aliases binops pendingAnns
                    DeclUnion ->
                        case A.toValue payload of
                            UnionPayload name vars ctors ->
                                let u = Src.Union (A.At (A.toRegion payload) name) vars ctors
                                in go vals (A.At (A.toRegion payload) u : unions) aliases binops pendingAnns
                            _ -> go vals unions aliases binops pendingAnns
                    DeclAlias ->
                        case A.toValue payload of
                            AliasPayload name vars body ->
                                let a = Src.Alias (A.At (A.toRegion payload) name) vars body
                                in go vals unions (A.At (A.toRegion payload) a : aliases) binops pendingAnns
                            _ -> go vals unions aliases binops pendingAnns
                    DeclForeign ->
                        go vals unions aliases binops pendingAnns
            ]
            (reverse vals, reverse unions, reverse aliases, reverse binops)

    -- Pop the (most recent) annotation whose name matches; return the
    -- remaining pending list.
    popAnnotation
        :: String
        -> [(String, A.Located Src.TypeAnnotation)]
        -> (Maybe (A.Located Src.TypeAnnotation), [(String, A.Located Src.TypeAnnotation)])
    popAnnotation target = go' []
      where
        go' acc []                              = (Nothing, reverse acc)
        go' acc ((n, a):rest)
            | n == target                       = (Just a, reverse acc ++ rest)
            | otherwise                         = go' ((n, a) : acc) rest
