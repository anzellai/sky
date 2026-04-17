-- | Whitespace and comment handling for Sky parser.
-- Handles: spaces, newlines, line comments (--), block comments ({- -})
module Sky.Parse.Space where

import qualified Data.Text as T
import Sky.Parse.Primitives


-- | Skip zero or more spaces (not newlines)
spaces :: Parser x ()
spaces = Parser $ \s _ eok _ _ ->
    let (_, rest) = T.span (\c -> c == ' ' || c == '\t') (_src s)
        consumed = T.length (_src s) - T.length rest
    in eok () (s { _src = rest, _offset = _offset s + consumed, _col = _col s + consumed })


-- | Skip whitespace including newlines, line comments, block comments
freshLine :: (Row -> Col -> x) -> Parser x ()
freshLine _ = Parser $ \s _ eok _ _ ->
    let s' = skipWhitespace s
    in eok () s'


-- | Skip all whitespace and comments, returning new state
skipWhitespace :: State -> State
skipWhitespace s =
    case T.uncons (_src s) of
        Nothing -> s

        Just (' ', rest) ->
            skipWhitespace (s { _src = rest, _offset = _offset s + 1, _col = _col s + 1 })

        Just ('\t', rest) ->
            skipWhitespace (s { _src = rest, _offset = _offset s + 1, _col = _col s + 4 })

        Just ('\n', rest) ->
            skipWhitespace (s { _src = rest, _offset = _offset s + 1, _row = _row s + 1, _col = 1 })

        Just ('\r', rest) ->
            skipWhitespace (s { _src = rest, _offset = _offset s + 1 })

        Just ('-', rest1)
            | Just ('-', _) <- T.uncons rest1 ->
                -- Line comment: skip to end of line
                let afterComment = T.dropWhile (/= '\n') rest1
                    skipped = T.length (_src s) - T.length afterComment
                in skipWhitespace (s { _src = afterComment, _offset = _offset s + skipped, _col = _col s + skipped })

        Just ('{', rest1)
            | Just ('-', rest2) <- T.uncons rest1 ->
                -- Block comment: skip to -}
                let (afterBlock, newRow, newCol) = skipBlockComment rest2 (_row s) (_col s + 2) 1
                    skipped = T.length (_src s) - T.length afterBlock
                in skipWhitespace (s { _src = afterBlock, _offset = _offset s + skipped, _row = newRow, _col = newCol })

        _ -> s


-- | Skip a block comment {- ... -}, handling nesting
skipBlockComment :: T.Text -> Row -> Col -> Int -> (T.Text, Row, Col)
skipBlockComment txt row col depth
    | depth == 0 = (txt, row, col)
    | T.null txt = (txt, row, col)
    | otherwise =
        case T.uncons txt of
            Nothing -> (txt, row, col)

            Just ('-', rest)
                | Just ('}', rest2) <- T.uncons rest ->
                    skipBlockComment rest2 row (col + 2) (depth - 1)

            Just ('{', rest)
                | Just ('-', rest2) <- T.uncons rest ->
                    skipBlockComment rest2 row (col + 2) (depth + 1)

            Just ('\n', rest) ->
                skipBlockComment rest (row + 1) 1 depth

            Just (_, rest) ->
                skipBlockComment rest row (col + 1) depth


-- | Check indentation: current column must be > indent
checkIndent :: (Row -> Col -> x) -> Parser x ()
checkIndent mkError = Parser $ \s _ eok _ eerr ->
    if _col s > _indent s
        then eok () s
        else eerr (_row s) (_col s) mkError


-- | Check that we're at a specific column
checkCol :: (Row -> Col -> x) -> Col -> Parser x ()
checkCol mkError expected = Parser $ \s _ eok _ eerr ->
    if _col s == expected
        then eok () s
        else eerr (_row s) (_col s) mkError


-- | Check alignment: column must equal indent
checkAligned :: (Row -> Col -> x) -> Parser x ()
checkAligned mkError = Parser $ \s _ eok _ eerr ->
    if _col s == _indent s
        then eok () s
        else eerr (_row s) (_col s) mkError
