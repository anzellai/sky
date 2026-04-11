-- | String and character literal parsing for Sky.
-- Supports: "regular strings", 'c' chars, """multiline strings with {{interpolation}}"""
module Sky.Parse.String where

import qualified Data.Text as T
import Sky.Parse.Primitives


-- | A parsed string value
data StringResult
    = SingleLine !String        -- "regular string"
    | MultiLine !String         -- """multiline with {{expr}}"""
    | CharLit !String           -- 'c'
    deriving (Show)


-- | Parse a string literal (single-line or multiline)
stringLiteral :: (Row -> Col -> x) -> Parser x StringResult
stringLiteral mkError = Parser $ \s cok _eok _cerr eerr ->
    case T.uncons (_src s) of
        Just ('"', rest1) ->
            case T.take 2 rest1 of
                t | t == T.pack "\"\"" ->
                    -- Triple-quoted: """..."""
                    let rest2 = T.drop 2 rest1  -- skip the other two quotes
                    in case findTripleClose rest2 of
                        Just (content, rest3) ->
                            let len = 3 + T.length content + 3  -- """ + content + """
                                newlines = T.count (T.pack "\n") content
                                newRow = _row s + newlines
                                newCol = if newlines > 0
                                    then T.length (lastLine content) + 4  -- +3 for closing """  + 1
                                    else _col s + len
                            in cok (MultiLine (T.unpack content))
                                   (s { _src = rest3, _offset = _offset s + len, _row = newRow, _col = newCol })
                        Nothing ->
                            eerr (_row s) (_col s) mkError

                _ ->
                    -- Single-quoted: "..."
                    case findSingleClose rest1 of
                        Just (content, rest2) ->
                            let len = 1 + T.length content + 1
                            in cok (SingleLine (T.unpack content))
                                   (s { _src = rest2, _offset = _offset s + len, _col = _col s + len })
                        Nothing ->
                            eerr (_row s) (_col s) mkError

        _ -> eerr (_row s) (_col s) mkError


-- | Parse a character literal: 'c'
charLiteral :: (Row -> Col -> x) -> Parser x StringResult
charLiteral mkError = Parser $ \s cok _eok _cerr eerr ->
    case T.uncons (_src s) of
        Just ('\'', rest1) ->
            case T.uncons rest1 of
                Just ('\\', rest2) ->
                    -- Escape sequence
                    case T.uncons rest2 of
                        Just (esc, rest3) ->
                            case T.uncons rest3 of
                                Just ('\'', rest4) ->
                                    let c = case esc of
                                            'n'  -> "\\n"
                                            't'  -> "\\t"
                                            'r'  -> "\\r"
                                            '\\' -> "\\\\"
                                            '\'' -> "\\'"
                                            _    -> ['\\', esc]
                                    in cok (CharLit c) (s { _src = rest4, _offset = _offset s + 4, _col = _col s + 4 })
                                _ -> eerr (_row s) (_col s) mkError
                        _ -> eerr (_row s) (_col s) mkError

                Just (c, rest2) ->
                    case T.uncons rest2 of
                        Just ('\'', rest3) ->
                            cok (CharLit [c]) (s { _src = rest3, _offset = _offset s + 3, _col = _col s + 3 })
                        _ -> eerr (_row s) (_col s) mkError

                _ -> eerr (_row s) (_col s) mkError

        _ -> eerr (_row s) (_col s) mkError


-- HELPERS

-- | Find closing " for a single-line string, handling escapes
findSingleClose :: T.Text -> Maybe (T.Text, T.Text)
findSingleClose = go T.empty
  where
    go acc txt =
        case T.uncons txt of
            Nothing -> Nothing
            Just ('"', rest) -> Just (acc, rest)
            Just ('\\', rest1) ->
                case T.uncons rest1 of
                    Just (c, rest2) -> go (acc `T.append` T.pack ['\\', c]) rest2
                    Nothing -> Nothing
            Just (c, rest) -> go (acc `T.snoc` c) rest


-- | Find closing """ for a multiline string
findTripleClose :: T.Text -> Maybe (T.Text, T.Text)
findTripleClose = go T.empty
  where
    go acc txt =
        case T.uncons txt of
            Nothing -> Nothing
            Just ('"', rest1) ->
                case T.take 2 rest1 of
                    t | t == T.pack "\"\"" ->
                        Just (acc, T.drop 2 rest1)
                    _ -> go (acc `T.snoc` '"') rest1
            Just (c, rest) -> go (acc `T.snoc` c) rest


-- | Get the last line of text (after last newline)
lastLine :: T.Text -> T.Text
lastLine txt =
    case T.breakOnEnd (T.pack "\n") txt of
        (_, after) -> after
