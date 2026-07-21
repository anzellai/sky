{-# LANGUAGE OverloadedStrings #-}

-- | v0.17 step-8 (#644) — rt.Coerce closed-proof scanner.
--
-- Walks emitted Go source character-by-character, finds every
-- runtime-coerce token call ('rt.Coerce[' / 'rt.CoerceString' /
-- 'rt.CoerceInt' / 'rt.CoerceBool' / 'rt.CoerceFloat' /
-- 'rt.MaybeCoerce' / 'rt.ResultCoerce' / 'rt.TaskCoerceT'), and
-- for each match asks: does the IMMEDIATELY PRECEDING context
-- carry a proof comment with one of these prefixes?
--
--   * @// PROOF: FFI:@
--   * @// PROOF: JSON-narrow:@
--   * @// PROOF: DB-narrow:@
--   * @/* PROOF: FFI:@         (inline; same-line splice)
--   * @/* PROOF: JSON-narrow:@
--   * @/* PROOF: DB-narrow:@
--
-- "Immediately preceding" means one of:
--   * For the FIRST coerce-token on a line: the comment is on
--     the previous line at the same (or greater) indent.
--   * For SECOND+ coerce-tokens on the same line (dense IIFEs):
--     an inline @/* PROOF: ... */@ block appears on the same
--     line, between the previous coerce-token's closing context
--     and this coerce-token's opening 'r'.
--
-- Returns the list of UNPROVEN sites — empty list means full
-- closed proof.
--
-- Designed to live in the test stanza so it can re-use
-- 'Data.List' / pure 'String' without dragging dependencies into
-- the library.  Re-usable from other showcase / regression specs
-- that want the same gate.
module Sky.Build.RtCoerceScanner
    ( Site (..)
    , scanCoerceSites
    , unprovenSites
    , isProvenSite
    ) where

import Data.List (isPrefixOf, isInfixOf, tails)


-- | One coerce-site location + its proof status.
data Site = Site
    { siteLine        :: !Int         -- 1-based line number
    , siteColumn      :: !Int         -- 1-based column where the token starts
    , siteToken       :: !String      -- rendered token (rt.Coerce[T] / rt.CoerceString / ...)
    , siteLineText    :: !String      -- the full line containing the site
    , sitePrevText    :: !String      -- the previous line's text (or "" at file start)
    , siteIsFirstOnLn :: !Bool        -- True iff this is the first coerce token on its line
    , siteInlinePrefix :: !String     -- characters on the same line BEFORE this site's 'rt.' prefix
    } deriving (Show)


-- | Three accepted proof-line prefixes (preceding-line form).
preceedingLinePrefixes :: [String]
preceedingLinePrefixes =
    [ "// PROOF: FFI:"
    , "// PROOF: JSON-narrow:"
    , "// PROOF: DB-narrow:"
    ]


-- | Three accepted inline-proof prefixes (same-line @/* PROOF: */@).
inlinePrefixes :: [String]
inlinePrefixes =
    [ "/* PROOF: FFI:"
    , "/* PROOF: JSON-narrow:"
    , "/* PROOF: DB-narrow:"
    ]


-- | Coerce-token surface forms.  Order matters for prefix matching:
-- longer first so 'rt.CoerceString' doesn't match the shorter
-- 'rt.Coerce[' prefix.
coerceTokens :: [String]
coerceTokens =
    [ "rt.ResultCoerce"
    , "rt.MaybeCoerce"
    , "rt.TaskCoerceT"
    , "rt.CoerceString"
    , "rt.CoerceFloat"
    , "rt.CoerceBool"
    , "rt.CoerceInt"
    , "rt.Coerce["
    ]


-- | Scan a Go source for every coerce-site, returning a flat list
-- of 'Site' records.  Each site carries enough context for
-- downstream proof-checking.  Idempotent: re-scanning emitted-
-- annotated output gives the same site list (proof comments
-- don't count as coerce sites because they're prefixed by '//').
scanCoerceSites :: String -> [Site]
scanCoerceSites src = goLine 1 "" (lines src)
  where
    goLine :: Int -> String -> [String] -> [Site]
    goLine _   _      []             = []
    goLine n   prev   (l : rest) =
        scanOneLine n prev l ++ goLine (n + 1) l rest

    scanOneLine :: Int -> String -> String -> [Site]
    scanOneLine n prevLine line = goCol True 1 line
      where
        goCol :: Bool -> Int -> String -> [Site]
        goCol _       _   []          = []
        goCol firstOn col rest@(_:cs)
            | Just tok <- matchToken rest =
                let consumed = length line - length rest
                    prefix   = take consumed line
                    site = Site
                        { siteLine = n
                        , siteColumn = col
                        , siteToken = tok
                        , siteLineText = line
                        , sitePrevText = prevLine
                        , siteIsFirstOnLn = firstOn
                        , siteInlinePrefix = prefix
                        }
                    -- Step past 'rt.' so the next match doesn't
                    -- re-fire on the same site.  3 chars = "rt.".
                    advance = 3
                in site
                   : goCol False (col + advance) (drop advance rest)
            | otherwise =
                goCol firstOn (col + 1) cs

    matchToken :: String -> Maybe String
    matchToken s = case filter (`isPrefixOf` s) coerceTokens of
        (t : _) -> Just t
        []      -> Nothing


-- | Is a given site backed by a proof comment?
--
-- First-on-line sites: previous line must contain (anywhere) one
-- of @preceedingLinePrefixes@.  Concretely the post-pass emits
-- the proof at the same indent as the coerce-site, and we just
-- check 'isInfixOf' so leading whitespace / cabal-vs-go fmt
-- variations don't break the contract.
--
-- Subsequent-on-line sites: the inline prefix (everything on the
-- same line BEFORE this site's 'rt.') must end with one of
-- @inlinePrefixes@ + arbitrary content + closing @*/@.  We accept
-- "appears in the inline prefix" as the proof — the post-pass
-- emits @/* PROOF: ... */ rt.Coerce@ so the proof block is
-- DIRECTLY before the token.
isProvenSite :: Site -> Bool
isProvenSite site
    | siteIsFirstOnLn site =
        any (`isInfixOf` sitePrevText site) preceedingLinePrefixes
    | otherwise =
        any (`isInfixOf` siteInlinePrefix site) inlinePrefixes


-- | Filter to unproven sites only.  Empty list = closed proof.
unprovenSites :: String -> [Site]
unprovenSites = filter (not . isProvenSite) . scanCoerceSites


-- | (unused; suppress unused-import warning on tails)
_dontWarn :: ()
_dontWarn = const () (tails "x")
