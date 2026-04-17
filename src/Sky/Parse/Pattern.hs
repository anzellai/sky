-- | Pattern parsing for Sky.
-- Patterns appear in: case branches, function parameters, let destructuring
module Sky.Parse.Pattern where

import qualified Data.Char
import qualified Data.Text as T
import Sky.Parse.Primitives
import Sky.Parse.Space (spaces, freshLine)
import Sky.Parse.Variable (lower, upper)
import Sky.Parse.Number (number, Number(..))
import Sky.Parse.String (stringLiteral, charLiteral, StringResult(..))
import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A


-- | Parse error context
data PError
    = PExpectedPattern Row Col
    | PExpectedCloseParen Row Col
    | PExpectedCloseBracket Row Col
    | PExpectedComma Row Col
    deriving (Show)


-- | Parse a pattern
pattern_ :: (Row -> Col -> x) -> Parser x Src.Pattern
pattern_ mkError = addLocation $ do
    pat <- patternAtom mkError
    spaces
    -- Check for :: (cons)
    mc <- peek
    case mc of
        Just ':' -> do
            -- Could be :: cons pattern
            oneOfWithFallback
                [ do
                    string mkError (T.pack "::")
                    spaces
                    rest <- pattern_ mkError
                    return (Src.PCons (A.At A.one pat) rest)
                ]
                pat
        Just 'a' -> do
            -- Could be `as` alias
            oneOfWithFallback
                [ do
                    keyword mkError (T.pack "as")
                    spaces
                    name <- addLocation (lower mkError)
                    return (Src.PAlias (A.At A.one pat) name)
                ]
                pat
        _ -> return pat


-- | Parse an atomic pattern (no cons or alias)
patternAtom :: (Row -> Col -> x) -> Parser x Src.Pattern_
patternAtom mkError =
    oneOf mkError
        [ -- Wildcard: _
          do char mkError '_'
             mc <- peek
             case mc of
                 Just c | isIdentContinue c -> do
                     -- It's actually a variable starting with _
                     name <- collectIdent "_"
                     return (Src.PVar name)
                 _ -> return Src.PAnything

        , -- Unit () | Parenthesised (pat) | Tuple (p1, p2, ...).
          -- Single `(` consume then branch on the next non-space char to
          -- avoid the cerr-commit bug where alternative branches each
          -- re-attempt to consume `(` and fail mid-stream.
          do char mkError '('
             spaces
             mc0 <- peek
             case mc0 of
                 Just ')' -> do
                     char mkError ')'
                     return Src.PUnit
                 _ -> do
                     p1 <- pattern_ mkError
                     spaces
                     mc <- peek
                     case mc of
                         Just ',' -> do
                             char mkError ','
                             spaces
                             p2 <- pattern_ mkError
                             more <- patternTupleRest mkError
                             spaces
                             char mkError ')'
                             return (Src.PTuple p1 p2 more)
                         Just ')' -> do
                             char mkError ')'
                             return (A.toValue p1)
                         _ -> error "Expected , or )"

        , -- List: [pat, pat, ...]
          do char mkError '['
             spaces
             mc <- peek
             case mc of
                 Just ']' -> do
                     char mkError ']'
                     return (Src.PList [])
                 _ -> do
                     first <- pattern_ mkError
                     rest <- patternListRest mkError
                     spaces
                     char mkError ']'
                     return (Src.PList (first : rest))

        , -- Record: { a, b, c }
          do char mkError '{'
             spaces
             fields <- patternRecordFields mkError
             spaces
             char mkError '}'
             return (Src.PRecord fields)

        , -- Constructor: Name or Name pat pat ...
          do name <- upper mkError
             spaces
             args <- patternCtorArgs mkError
             return (Src.PCtor name [] args)

        , -- Negative number literal: `-3`, `-3.14`. Peek two chars so we
          -- don't consume `-` unless the next char is a digit — otherwise
          -- the pattern parser commits on `-` and breaks when the `-` was
          -- actually a separator (e.g. `->` after a ctor pattern).
          do ok <- peekNextIsNegativeDigit
             if not ok
                 then Parser $ \s _ _ _ eerr -> eerr (_row s) (_col s) mkError
                 else do
                     char mkError '-'
                     n <- number mkError
                     return $ case n of
                         IntNum i   -> Src.PInt (negate i)
                         FloatNum f -> Src.PFloat (negate f)

        , -- Number literal
          do n <- number mkError
             return $ case n of
                 IntNum i  -> Src.PInt i
                 FloatNum f -> Src.PFloat f

        , -- String literal
          do s <- stringLiteral mkError
             return $ case s of
                 SingleLine str -> Src.PStr str
                 MultiLine str  -> Src.PStr str
                 CharLit _      -> error "char in pattern context"

        , -- Char literal
          do s <- charLiteral mkError
             return $ case s of
                 CharLit c -> Src.PChr c
                 _         -> error "expected char"

        , -- Boolean: True / False
          oneOf mkError
            [ keyword mkError (T.pack "True") >> return (Src.PBool True)
            , keyword mkError (T.pack "False") >> return (Src.PBool False)
            ]

        , -- Variable
          do name <- lower mkError
             return (Src.PVar name)
        ]


-- | Parse constructor arguments (zero or more atomic patterns)
patternCtorArgs :: (Row -> Col -> x) -> Parser x [Src.Pattern]
patternCtorArgs mkError =
    oneOfWithFallback
        [ do
            arg <- addLocation (patternAtom mkError)
            spaces
            rest <- patternCtorArgs mkError
            return (arg : rest)
        ]
        []


-- | Parse remaining tuple elements after the second
patternTupleRest :: (Row -> Col -> x) -> Parser x [Src.Pattern]
patternTupleRest mkError =
    oneOfWithFallback
        [ do
            spaces
            char mkError ','
            spaces
            p <- pattern_ mkError
            rest <- patternTupleRest mkError
            return (p : rest)
        ]
        []


-- | Parse remaining list elements
patternListRest :: (Row -> Col -> x) -> Parser x [Src.Pattern]
patternListRest mkError =
    oneOfWithFallback
        [ do
            spaces
            char mkError ','
            spaces
            p <- pattern_ mkError
            rest <- patternListRest mkError
            return (p : rest)
        ]
        []


-- | Parse record pattern fields
patternRecordFields :: (Row -> Col -> x) -> Parser x [A.Located String]
patternRecordFields mkError =
    oneOfWithFallback
        [ do
            name <- addLocation (lower mkError)
            rest <- patternRecordFieldsRest mkError
            return (name : rest)
        ]
        []


patternRecordFieldsRest :: (Row -> Col -> x) -> Parser x [A.Located String]
patternRecordFieldsRest mkError =
    oneOfWithFallback
        [ do
            spaces
            char mkError ','
            spaces
            name <- addLocation (lower mkError)
            rest <- patternRecordFieldsRest mkError
            return (name : rest)
        ]
        []


-- Helpers

-- | Peek two chars without consuming; return True iff the next char is `-`
-- and the one after is an ASCII digit. Used to gate the negative-number
-- branch in patternAtom so `-` inside `->` doesn't get misconsumed.
peekNextIsNegativeDigit :: Parser x Bool
peekNextIsNegativeDigit = Parser $ \s _ eok _ _ ->
    let src = _src s
    in case T.unpack (T.take 2 src) of
           ('-' : c : _) -> eok (c >= '0' && c <= '9') s
           _             -> eok False s


-- | Accepts any Unicode letter, digit, or underscore. Matches Variable.isIdentChar
-- so identifiers use the same alphabet in value, type, and pattern positions.
isIdentContinue :: Char -> Bool
isIdentContinue c = Data.Char.isAlphaNum c || c == '_'

collectIdent :: String -> Parser x String
collectIdent prefix = Parser $ \s _ eok _ _ ->
    let (more, rest) = T.span isIdentContinue (_src s)
        name = prefix ++ T.unpack more
        len = T.length more
    in eok name (s { _src = rest, _offset = _offset s + len, _col = _col s + len })
