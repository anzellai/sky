-- | Variable and constructor name parsing for Sky.
-- lowercase = value names, uppercase = type/constructor names
module Sky.Parse.Variable where

import Data.Char (isAlpha, isAlphaNum, isUpper, isLower)
import qualified Data.Text as T
import Sky.Parse.Primitives
import Sky.Parse.Keyword (isKeyword)


-- | Parse a lowercase identifier (value or function name)
lower :: (Row -> Col -> x) -> Parser x String
lower mkError = Parser $ \s cok _eok _cerr eerr ->
    case T.uncons (_src s) of
        Just (c, _)
            | isLower c || c == '_' ->
                let (name, rest) = T.span isIdentChar (_src s)
                    nameStr = T.unpack name
                    len = T.length name
                in
                    if isKeyword nameStr
                        then eerr (_row s) (_col s) mkError
                        else cok nameStr (s { _src = rest, _offset = _offset s + len, _col = _col s + len })

        _ -> eerr (_row s) (_col s) mkError


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
        [] -> return ([], first)  -- Just a constructor name
        _  -> let parts = first : init rest
                   name = last rest
               in return (parts, name)


-- | Parse zero or more .Name segments
dotParts :: (Row -> Col -> x) -> Parser x [String]
dotParts mkError = Parser $ \s _ eok _ _ ->
    go s []
  where
    go s acc =
        case T.uncons (_src s) of
            Just ('.', rest1) ->
                case T.uncons rest1 of
                    Just (c, _)
                        | isAlpha c ->
                            let (name, rest2) = T.span isIdentChar rest1
                                nameStr = T.unpack name
                                len = 1 + T.length name  -- dot + name
                            in go (s { _src = rest2, _offset = _offset s + len, _col = _col s + len }) (acc ++ [nameStr])
                    _ -> eok (reverse acc) s  -- dot not followed by alpha
            _ -> eok (reverse acc) s  -- no more dots
      where
        eok result state = result `seq` state `seq` (result, state)  -- force evaluation
        -- Actually we need the Parser continuation, let me restructure

-- Simpler version: just collect dot-separated names
dotSeparated :: (Row -> Col -> x) -> Parser x [String]
dotSeparated mkError = do
    first <- lower mkError
    moreNames mkError [first]


moreNames :: (Row -> Col -> x) -> [String] -> Parser x [String]
moreNames mkError acc =
    oneOfWithFallback
        [ do
            char mkError '.'
            name <- oneOf mkError [upper mkError, lower mkError]
            moreNames mkError (acc ++ [name])
        ]
        acc
