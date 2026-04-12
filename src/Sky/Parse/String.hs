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
                            in cok (SingleLine (unescapeString (T.unpack content)))
                                   (s { _src = rest2, _offset = _offset s + len, _col = _col s + len })
                        Nothing ->
                            eerr (_row s) (_col s) mkError

        _ -> eerr (_row s) (_col s) mkError


-- | Unescape a string's escape sequences so the parsed value is the actual
-- runtime string. Supports: \n \t \r \\ \" \' \0 and Unicode escapes:
--   \xHH       — two-digit hex byte (ASCII only)
--   \uHHHH     — four-digit hex, BMP Unicode code point
--   \u{H...}   — variable-length hex, full Unicode code point up to U+10FFFF
-- Any unknown escape is left as-is (raw backslash + char) so the user can tell
-- something is wrong at compile time rather than silently losing data.
unescapeString :: String -> String
unescapeString [] = []
unescapeString ('\\':c:rest) =
    case c of
        'n'  -> '\n' : unescapeString rest
        't'  -> '\t' : unescapeString rest
        'r'  -> '\r' : unescapeString rest
        '\\' -> '\\' : unescapeString rest
        '"'  -> '"'  : unescapeString rest
        '\'' -> '\'' : unescapeString rest
        '0'  -> '\0' : unescapeString rest
        'a'  -> '\a' : unescapeString rest
        'b'  -> '\b' : unescapeString rest
        'f'  -> '\f' : unescapeString rest
        'v'  -> '\v' : unescapeString rest
        'x'  -> case parseHex 2 rest of
            Just (n, r2) -> toEnum n : unescapeString r2
            Nothing      -> '\\' : 'x' : unescapeString rest
        'u'  -> case rest of
            '{':r1 -> case parseHexBraced r1 of
                Just (n, r2) | validCodepoint n -> toEnum n : unescapeString r2
                _ -> '\\' : 'u' : unescapeString rest
            _ -> case parseHex 4 rest of
                Just (n, r2) | validCodepoint n -> toEnum n : unescapeString r2
                _            -> '\\' : 'u' : unescapeString rest
        other -> '\\' : other : unescapeString rest
unescapeString (c:rest) = c : unescapeString rest


-- | Parse up to n hex digits (require exactly n)
parseHex :: Int -> String -> Maybe (Int, String)
parseHex n xs =
    let (digs, rest) = splitAt n xs
    in if length digs == n && all isHexDigit digs
        then Just (foldl (\acc c -> acc * 16 + hexVal c) 0 digs, rest)
        else Nothing


-- | Parse hex digits until '}'
parseHexBraced :: String -> Maybe (Int, String)
parseHexBraced xs =
    let (digs, rest) = span isHexDigit xs
    in case rest of
        '}':r2 | not (null digs) && length digs <= 8 ->
            Just (foldl (\acc c -> acc * 16 + hexVal c) 0 digs, r2)
        _ -> Nothing


isHexDigit :: Char -> Bool
isHexDigit c =
    (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')


hexVal :: Char -> Int
hexVal c
    | c >= '0' && c <= '9' = fromEnum c - fromEnum '0'
    | c >= 'a' && c <= 'f' = 10 + fromEnum c - fromEnum 'a'
    | c >= 'A' && c <= 'F' = 10 + fromEnum c - fromEnum 'A'
    | otherwise = 0


-- | A code point is valid if within Unicode range and not a surrogate.
validCodepoint :: Int -> Bool
validCodepoint n = n >= 0 && n <= 0x10FFFF && not (n >= 0xD800 && n <= 0xDFFF)


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
