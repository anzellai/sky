-- | Variable and constructor name parsing for Sky.
-- lowercase = value names, uppercase = type/constructor names
module Sky.Parse.Variable where

import Data.Char (isAlpha, isAlphaNum, isUpper, isLower, isLetter)
import qualified Data.Text as T
import Sky.Parse.Primitives
import Sky.Parse.Keyword (isKeyword)


-- | Parse a lowercase identifier (value or function name).
-- Accepts: ASCII lowercase letters, Unicode lowercase letters, `_`, and
-- caseless letters (CJK ideographs, Arabic, Hebrew, etc.) which have neither
-- an upper nor a lower form. Caseless letters are treated as value-level names
-- so Chinese/Japanese/Korean identifiers work for values; user-defined types
-- must still start with an ASCII (or explicitly uppercase Unicode) letter.
lower :: (Row -> Col -> x) -> Parser x String
lower mkError = Parser $ \s cok _eok _cerr eerr ->
    case T.uncons (_src s) of
        Just (c, _)
            | isLowerLike c ->
                let (name, rest) = T.span isIdentChar (_src s)
                    nameStr = T.unpack name
                    len = T.length name
                in
                    if isKeyword nameStr
                        then eerr (_row s) (_col s) mkError
                        else cok nameStr (s { _src = rest, _offset = _offset s + len, _col = _col s + len })

        _ -> eerr (_row s) (_col s) mkError


-- | Matches lowercase letters, `_`, or letters with no case distinction.
isLowerLike :: Char -> Bool
isLowerLike c = c == '_' || isLower c || (isLetter c && not (isUpper c))


-- | Parse an uppercase identifier (type or constructor name)
upper :: (Row -> Col -> x) -> Parser x String
upper mkError = Parser $ \s cok _eok _cerr eerr ->
    case T.uncons (_src s) of
        Just (c, _)
            | isUpper c ->
                let (name, rest) = T.span isIdentChar (_src s)
                    nameStr = T.unpack name
                    len = T.length name
                in cok nameStr (s { _src = rest, _offset = _offset s + len, _col = _col s + len })

        _ -> eerr (_row s) (_col s) mkError


-- | Check if a character can appear in an identifier
isIdentChar :: Char -> Bool
isIdentChar c = isAlphaNum c || c == '_'


-- | Parse a dotted qualified name: Module.Name or Module.Sub.Name
-- Returns (module segments, final name)
qualifiedVar :: (Row -> Col -> x) -> Parser x ([String], String)
qualifiedVar mkError = do
    first <- upper mkError
    rest <- dotParts mkError
    case rest of
        [] -> return ([], first)
        _  ->
            let parts = first : init rest
                name  = last rest
            in return (parts, name)


-- | Parse zero or more .Name segments
dotParts :: (Row -> Col -> x) -> Parser x [String]
dotParts mkError =
    oneOfWithFallback
        [ do
            char mkError '.'
            name <- oneOf mkError [upper mkError, lower mkError]
            rest <- dotParts mkError
            return (name : rest)
        ]
        []
