{-# LANGUAGE RankNTypes #-}
-- | Parser combinator library for Sky.
-- Inspired by Elm's Parse.Primitives but using Text for safety.
-- CPS-based for performance with explicit position tracking.
module Sky.Parse.Primitives
    ( Parser
    , State(..)
    , Row, Col
    -- Entry
    , fromText
    -- Combinators
    , oneOf
    , oneOfWithFallback
    -- Positioning
    , getPosition
    , getCol
    , getIndent
    , setIndent
    , withIndent
    , addLocation
    -- Primitives
    , satisfy
    , char
    , string
    , keyword
    , peek
    , end
    )
    where

import qualified Data.Text as T
import Data.Char (isSpace)
import qualified Sky.Reporting.Annotation as A


-- | Row and column (1-based)
type Row = Int
type Col = Int


-- | Parser state
data State = State
    { _src    :: !T.Text     -- remaining source text
    , _offset :: !Int        -- absolute offset into original source
    , _indent :: !Int        -- current indentation level
    , _row    :: !Row        -- current line number
    , _col    :: !Col        -- current column number
    }
    deriving (Show)


-- | CPS parser with 4 continuations:
-- consumed-ok, empty-ok, consumed-err, empty-err
newtype Parser x a = Parser
    { unParser :: forall b.
        State                                   -- input state
        -> (a -> State -> b)                    -- consumed ok
        -> (a -> State -> b)                    -- empty ok
        -> (Row -> Col -> (Row -> Col -> x) -> b) -- consumed err
        -> (Row -> Col -> (Row -> Col -> x) -> b) -- empty err
        -> b
    }


instance Functor (Parser x) where
    fmap f (Parser p) = Parser $ \s cok eok cerr eerr ->
        p s (cok . f) (eok . f) cerr eerr


instance Applicative (Parser x) where
    pure a = Parser $ \s _ eok _ _ -> eok a s

    (Parser pf) <*> (Parser pa) = Parser $ \s cok eok cerr eerr ->
        let
            applyOk f s' = pa s' (cok . f) (cok . f) cerr cerr
            applyEok f s' = pa s' (cok . f) (eok . f) cerr eerr
        in
            pf s applyOk applyEok cerr eerr


instance Monad (Parser x) where
    return = pure

    (Parser pa) >>= f = Parser $ \s cok eok cerr eerr ->
        let
            bindOk a s' =
                let (Parser pb) = f a
                in pb s' cok cok cerr cerr
            bindEok a s' =
                let (Parser pb) = f a
                in pb s' cok eok cerr eerr
        in
            pa s bindOk bindEok cerr eerr


-- ENTRY POINT

-- | Parse a Text value
fromText :: Parser x a -> (Row -> Col -> x) -> T.Text -> Either x a
fromText (Parser p) mkError src =
    p (State src 0 0 1 1)
        (\a _ -> Right a)
        (\a _ -> Right a)
        (\r c toErr -> Left (toErr r c))
        (\r c toErr -> Left (toErr r c))


-- COMBINATORS

-- | Try each parser in order; if all fail with empty, report custom error
oneOf :: (Row -> Col -> x) -> [Parser x a] -> Parser x a
oneOf mkError parsers = Parser $ \s cok eok cerr eerr ->
    go parsers s cok eok cerr eerr mkError
  where
    go [] s _ _ _ eerr mkErr = eerr (_row s) (_col s) mkErr
    go (Parser p : rest) s cok eok cerr eerr mkErr =
        p s cok eok cerr (\_ _ _ -> go rest s cok eok cerr eerr mkErr)


-- | Try parsers; if all fail, return fallback value
oneOfWithFallback :: [Parser x a] -> a -> Parser x a
oneOfWithFallback parsers fallback = Parser $ \s cok eok cerr _eerr ->
    go parsers s cok eok cerr
  where
    go [] s _ eok _ = eok fallback s
    go (Parser p : rest) s cok eok cerr =
        p s cok eok cerr (\_ _ _ -> go rest s cok eok cerr)


-- POSITION

-- | Get current position as (Row, Col)
getPosition :: Parser x (Row, Col)
getPosition = Parser $ \s _ eok _ _ ->
    eok (_row s, _col s) s


-- | Get current column
getCol :: Parser x Col
getCol = Parser $ \s _ eok _ _ ->
    eok (_col s) s


-- | Get current indent level
getIndent :: Parser x Int
getIndent = Parser $ \s _ eok _ _ ->
    eok (_indent s) s


-- | Set indent level
setIndent :: Int -> Parser x ()
setIndent n = Parser $ \s _ eok _ _ ->
    eok () (s { _indent = n })


-- | Run parser with a given indent level, restoring afterwards
withIndent :: Int -> Parser x a -> Parser x a
withIndent n (Parser p) = Parser $ \s cok eok cerr eerr ->
    let
        oldIndent = _indent s
        restoreOk a s' = cok a (s' { _indent = oldIndent })
        restoreEok a s' = eok a (s' { _indent = oldIndent })
    in
        p (s { _indent = n }) restoreOk restoreEok cerr eerr


-- | Wrap a parser result with source location
addLocation :: Parser x a -> Parser x (A.Located a)
addLocation (Parser p) = Parser $ \s cok eok cerr eerr ->
    let
        start = A.Position (_row s) (_col s)
        okWith a s' =
            let end_ = A.Position (_row s') (_col s')
                region = A.Region start end_
            in cok (A.At region a) s'
        eokWith a s' =
            let end_ = A.Position (_row s') (_col s')
                region = A.Region start end_
            in eok (A.At region a) s'
    in
        p s okWith eokWith cerr eerr


-- PRIMITIVES

-- | Consume one character satisfying a predicate
satisfy :: (Row -> Col -> x) -> (Char -> Bool) -> Parser x Char
satisfy mkError predicate = Parser $ \s cok _eok _cerr eerr ->
    case T.uncons (_src s) of
        Just (c, rest)
            | predicate c ->
                let
                    (newRow, newCol) =
                        if c == '\n'
                            then (_row s + 1, 1)
                            else (_row s, _col s + 1)
                in
                    cok c (State rest (_offset s + 1) (_indent s) newRow newCol)

        _ -> eerr (_row s) (_col s) mkError


-- | Match a specific character
char :: (Row -> Col -> x) -> Char -> Parser x ()
char mkError expected = Parser $ \s cok _eok _cerr eerr ->
    case T.uncons (_src s) of
        Just (c, rest)
            | c == expected ->
                let
                    (newRow, newCol) =
                        if c == '\n'
                            then (_row s + 1, 1)
                            else (_row s, _col s + 1)
                in
                    cok () (State rest (_offset s + 1) (_indent s) newRow newCol)

        _ -> eerr (_row s) (_col s) mkError


-- | Match a specific string
string :: (Row -> Col -> x) -> T.Text -> Parser x ()
string mkError expected = Parser $ \s cok _eok _cerr eerr ->
    if T.isPrefixOf expected (_src s)
        then
            let
                len = T.length expected
                consumed = T.take len (_src s)
                rest = T.drop len (_src s)
                newlines = T.count (T.pack "\n") consumed
                newRow = _row s + newlines
                newCol = if newlines > 0
                    then T.length (snd (T.breakOnEnd (T.pack "\n") consumed)) + 1
                    else _col s + len
            in
                cok () (State rest (_offset s + len) (_indent s) newRow newCol)
        else
            eerr (_row s) (_col s) mkError


-- | Match a keyword (specific string NOT followed by alphanumeric or _)
keyword :: (Row -> Col -> x) -> T.Text -> Parser x ()
keyword mkError kw = Parser $ \s cok _eok _cerr eerr ->
    if T.isPrefixOf kw (_src s)
        then
            let
                rest = T.drop (T.length kw) (_src s)
                nextChar = case T.uncons rest of
                    Just (c, _) -> c
                    Nothing     -> ' '
                isWordChar c = c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
            in
                if isWordChar nextChar
                    then eerr (_row s) (_col s) mkError
                    else
                        let len = T.length kw
                        in cok () (State rest (_offset s + len) (_indent s) (_row s) (_col s + len))
        else
            eerr (_row s) (_col s) mkError


-- | Peek at next character without consuming
peek :: Parser x (Maybe Char)
peek = Parser $ \s _ eok _ _ ->
    eok (fmap fst (T.uncons (_src s))) s


-- | Succeed only at end of input
end :: (Row -> Col -> x) -> Parser x ()
end mkError = Parser $ \s _ eok _ eerr ->
    if T.null (_src s)
        then eok () s
        else eerr (_row s) (_col s) mkError
