{-# LANGUAGE LambdaCase #-}
-- | Minimal Sky-type parser used by Sky.Build.FfiRegistry.
--
-- Parses the closed subset of Sky type-string syntax that the FFI
-- inspector emits into kernel.json's @skyType@ field — see
-- @Sky.Build.FfiGen.wrapperSkyType@ for the producer side. This is
-- not a general-purpose Sky parser; it is intentionally small so we
-- can canonicalise the FFI registry without depending on the full
-- Sky.Parse machinery (which threads source-position state, layout
-- filtering, and qualified-import resolution that don't apply to
-- a single-line type string).
--
-- Grammar (closed; matches the FfiGen producer):
--
-- > type   := arrow
-- > arrow  := app ('->' arrow)?
-- > app    := atom atom*
-- > atom   := '(' inner ')' | identifier
-- > inner  := type (',' type)*       -- tuple, including 'unit' when empty
-- > identifier := lowercase | uppercase
--
-- Application is left-associative (matches Elm/Sky surface syntax).
-- Arrow is right-associative.
--
-- The parser is total: invalid input returns 'Nothing' rather than
-- raising. Callers (FfiRegistry's JSON decoder) treat 'Nothing' as
-- "no Sky type known" and fall back to the legacy any-typed path.
module Sky.Build.FfiTypeParser
    ( FtyAst(..)
    , parseFty
    -- ^ exposed for the cabal test suite — the full parser lives
    -- as one entry point so callers don't reach into the lexer.
    ) where

import Data.Char (isAlpha, isAlphaNum, isLower, isSpace, isUpper)


-- | Mini Sky-type AST. Mirrors a useful subset of
-- @Sky.AST.Canonical.Type@; the conversion to a canonical 'Type'
-- happens at HM-wire time (Phase C) where the canonical module
-- map is in scope.
data FtyAst
    = FtyVar !String
        -- ^ Lower-case identifier — type variable. Includes the
        -- special 'any' which Sky.Type.Solve maps to a fresh
        -- unification var per occurrence (Limitation #18).
    | FtyApp !String ![FtyAst]
        -- ^ @Name args@. Empty arg list = bare type constructor,
        -- e.g. @String@ / @Int@ / @ActionCodeSettings@. Nonempty =
        -- applied, e.g. @List String@ / @Result Error Token@.
    | FtyArrow !FtyAst !FtyAst
        -- ^ Right-associative function arrow.
    | FtyUnit
        -- ^ The unit type @()@, distinct from 'FtyTuple []' so the
        -- conversion to T.Type can produce 'TUnit' directly.
    | FtyTuple ![FtyAst]
        -- ^ 2+-element tuple.
    deriving (Eq, Show)


-- | Parse a Sky type string. Returns 'Nothing' if the string isn't
-- a syntactically valid type within the closed subset above —
-- callers fall back to the no-type path.
parseFty :: String -> Maybe FtyAst
parseFty s = case lex' s of
    Nothing   -> Nothing
    Just toks -> case runP pType toks of
        Just (t, []) -> Just t
        _            -> Nothing


-- ──────────────────────────────────────────────────────────────────
-- Lexer
-- ──────────────────────────────────────────────────────────────────

data Tok
    = TLP        -- '('
    | TRP        -- ')'
    | TComma     -- ','
    | TArrow     -- '->'
    | TIdent !String
    deriving (Eq, Show)


-- | Tokenise. Whitespace is the only separator; identifiers are
-- @[A-Za-z_][A-Za-z0-9_]*@ (Sky / Elm syntax). Returns 'Nothing'
-- on the first unrecognised character — that's how channel-residue
-- strings (e.g. @<-chan struct{}@) are rejected en bloc, rather
-- than silently letting the parser stop early at the bad char and
-- treat the remainder as a clean parse.
lex' :: String -> Maybe [Tok]
lex' = go . dropWhile isSpace
  where
    go [] = Just []
    go ('(':rest) = (TLP :)    <$> go (dropWhile isSpace rest)
    go (')':rest) = (TRP :)    <$> go (dropWhile isSpace rest)
    go (',':rest) = (TComma :) <$> go (dropWhile isSpace rest)
    go ('-':'>':rest) = (TArrow :) <$> go (dropWhile isSpace rest)
    go cs@(c:_) | isAlpha c || c == '_' =
        let (ident, rest) = span identChar cs
        in (TIdent ident :) <$> go (dropWhile isSpace rest)
    go _ = Nothing

    identChar c = isAlphaNum c || c == '_'


-- ──────────────────────────────────────────────────────────────────
-- Parser combinator (single-token, monadic-state form)
-- ──────────────────────────────────────────────────────────────────

newtype P a = P { runP :: [Tok] -> Maybe (a, [Tok]) }

instance Functor P where
    fmap f (P g) = P $ \ts -> case g ts of
        Just (a, rest) -> Just (f a, rest)
        Nothing -> Nothing

instance Applicative P where
    pure x = P $ \ts -> Just (x, ts)
    (P pf) <*> (P pa) = P $ \ts -> case pf ts of
        Just (f, ts') -> case pa ts' of
            Just (a, ts'') -> Just (f a, ts'')
            Nothing -> Nothing
        Nothing -> Nothing

instance Monad P where
    return = pure
    (P p) >>= k = P $ \ts -> case p ts of
        Just (a, ts') -> runP (k a) ts'
        Nothing -> Nothing


peek :: P (Maybe Tok)
peek = P $ \ts -> Just (case ts of (t:_) -> Just t; [] -> Nothing, ts)


eat :: Tok -> P ()
eat want = P $ \case
    (t:rest) | t == want -> Just ((), rest)
    _ -> Nothing


-- ──────────────────────────────────────────────────────────────────
-- Grammar
-- ──────────────────────────────────────────────────────────────────

-- | type ::= arrow
pType :: P FtyAst
pType = pArrow


-- | arrow ::= app ('->' arrow)?
pArrow :: P FtyAst
pArrow = do
    lhs <- pApp
    next <- peek
    case next of
        Just TArrow -> eat TArrow >> FtyArrow lhs <$> pArrow
        _           -> pure lhs


-- | app ::= atom atom*
--
-- Application is constructor-headed: an atom that's a bare
-- identifier becomes the constructor; subsequent atoms are its
-- arguments. So the result is always a flat 'FtyApp head args'
-- when followed by atoms — matches Sky's left-associative
-- application semantics. If the head atom is a paren'd type, no
-- application is allowed (matches Sky too).
pApp :: P FtyAst
pApp = do
    head_ <- pAtom
    case head_ of
        FtyApp name [] -> do
            args <- pArgs
            pure $ if null args then head_ else FtyApp name args
        _ -> pure head_
  where
    pArgs :: P [FtyAst]
    pArgs = do
        next <- peek
        case next of
            Just TLP        -> (:) <$> pAtom <*> pArgs
            Just (TIdent _) -> (:) <$> pAtom <*> pArgs
            _               -> pure []


-- | atom ::= identifier | '(' inner ')'
-- inner ::= type (',' type)*  (zero items = unit, one = paren'd, 2+ = tuple)
pAtom :: P FtyAst
pAtom = do
    next <- peek
    case next of
        Just (TIdent n) -> do
            eat (TIdent n)
            pure $ if isVar n then FtyVar n else FtyApp n []
        Just TLP -> do
            eat TLP
            -- Zero items: unit literal '()'
            after <- peek
            case after of
                Just TRP -> eat TRP >> pure FtyUnit
                _ -> do
                    first <- pType
                    items <- pTupleTail
                    eat TRP
                    case items of
                        [] -> pure first
                        xs -> pure (FtyTuple (first : xs))
        _ -> P $ const Nothing
  where
    pTupleTail :: P [FtyAst]
    pTupleTail = do
        next <- peek
        case next of
            Just TComma -> eat TComma >> ((:) <$> pType <*> pTupleTail)
            _ -> pure []

    -- Type-variable identifiers in Sky surface syntax: lower-case
    -- first character, OR the wildcard 'any'.
    isVar :: String -> Bool
    isVar [] = False
    isVar (c:_) = isLower c || c == '_'
    -- 'any' lands as FtyVar via the lower-case rule above; the
    -- wildcard semantics live in Sky.Type.Solve.
    -- (Just adding this comment since it surprised me when reading
    -- back — uppercase identifiers go through FtyApp with empty
    -- args, lowercase through FtyVar.)
    _isUpper c = isUpper c
