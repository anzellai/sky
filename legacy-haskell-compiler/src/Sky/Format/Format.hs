-- | Opinionated, deterministic pretty printer for Sky source code
-- (output is Elm-format-compatible).
--
-- Uses absolute column tracking: every function takes `col :: Int`
-- (the current indentation column) and produces strings with
-- newlines at the correct absolute position. The golden rule is
-- "one line or each on its own line" — never mix.
--
-- v0.17.8 Phase 3 (#144): the fmt cascade threads a comment-stream
-- (`CS`) so per-node arms drain own-line comments above the node
-- they belong to. Phase 3 installed drain hooks at EVERY semantic
-- boundary — top-decl / value body / let-def / case-arm / record
-- field / list element / tuple element / record-update field / if
-- else — so the entire ~500 LOC string-anchor post-pass that used
-- to live in `app/Main.hs`'s `preserveTopLevelComments` is now
-- redundant and deleted.
--
-- Trailing comments (`x = y  -- inline`) are silently dropped —
-- the same behaviour as the pre-Phase 3 string-anchor path.  Only
-- own-line comments (whitespace-only preceding the `--` / `{-`)
-- round-trip through `sky fmt`.
module Sky.Format.Format (formatModule) where

import Data.List (intercalate, sortOn, partition)
import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A


-- ═══════════════════════════════════════════════════════════
-- AST-driven comment placement (v0.17.8 / issue #144 Phase 3)
--
-- `CS` is the pending-comment stream (sorted by source line at
-- entry). Every fmt* helper threads `CS -> a -> (CS, String)` so a
-- consumer arm can `drainBefore` at a semantic drain point.
--
-- Two draining shapes:
--   * `drainBefore indent nodeLine cs` — drain own-line comments
--     with `_line < nodeLine` AND `_commentCol == indent + 1`.
--     Used at header positions where a comment sits at the same
--     indent as the node it precedes.
--   * `drainInnerUntil nextLine cs` — drain remaining own-line
--     comments with `_line < nextLine` AND `_commentCol > 1`
--     (INNER-indent only, so top-level headers stay routed to
--     the next decl's start-of-decl drain). Each comment renders
--     at its OWN source column.
-- ═══════════════════════════════════════════════════════════

type CS = [A.Located Src.ParsedComment]


-- Peel own-line comments whose source line is strictly less than
-- `nodeLine` AND whose source column matches the node's indent
-- level. Trailing comments and mismatched-indent own-line comments
-- are left in place (the enclosing / next-decl drain picks them up).
--
-- Column semantics: `_commentCol` is 1-based (parser stores
-- `Region._start._col`). `indent` is a 0-based space count. A
-- comment matches this indent when `_commentCol == indent + 1`.
drainBefore :: Int -> Int -> CS -> (String, CS)
drainBefore indent nodeLine cs =
    let (drainable, kept) = partition drainCond cs
        rendered = concatMap (renderComment indent) drainable
    in (rendered, kept)
  where
    drainCond (A.At r pc) =
         Src._commentPos pc == Src.CommentOwnLine
      && A._line (A._start r) < nodeLine
      && Src._commentCol pc == indent + 1


-- Permissive variant: drains ANY own-line comment above `nodeLine`
-- regardless of source column. Used where the AST re-indents the
-- child (typically Lambda body — the lambda re-columns its body
-- to `col + step` which rarely matches the source column). CS
-- threading guarantees no cross-contamination: outer-scope drains
-- fire first, so this only sees comments that no outer scope
-- claimed.  Emit at target `indent`.
drainAnyBefore :: Int -> Int -> CS -> (String, CS)
drainAnyBefore indent nodeLine cs =
    let (drainable, kept) = partition drainCond cs
        rendered = concatMap (renderComment indent) drainable
    in (rendered, kept)
  where
    drainCond (A.At r pc) =
         Src._commentPos pc == Src.CommentOwnLine
      && A._line (A._start r) < nodeLine


-- End-of-decl mop-up: drain remaining OWN-LINE comments whose
-- source line falls before `nextLine`, using each comment's OWN
-- source column for indent. This catches "body-trailer" comments
-- that sit below the last emitted node but before the next top-
-- level decl (FormatSpec's "preserves trailing body comments in
-- tail position" regression). Filters `_commentCol > 1` so top-
-- level comments still route to the next decl's start-of-decl
-- drain (regression: "header comment stays above the definition,
-- not a call site").
drainInnerUntil :: Int -> CS -> (String, CS)
drainInnerUntil nextLine cs =
    let (drainable, kept) = partition drainCond cs
        rendered = concatMap renderAtOwnCol drainable
    in (rendered, kept)
  where
    drainCond (A.At r pc) =
         Src._commentPos pc == Src.CommentOwnLine
      && A._line (A._start r) < nextLine
      && Src._commentCol pc > 1
    renderAtOwnCol c@(A.At _ pc) =
        renderComment (Src._commentCol pc - 1) c


-- Drain ALL remaining own-line comments (line unbounded), each
-- rendered at its own source column. Called once at end of
-- formatModule to catch stragglers below the last decl.
drainRemaining :: CS -> (String, CS)
drainRemaining cs =
    let (drainable, kept) = partition drainCond cs
        rendered = concatMap renderAtOwnCol drainable
    in (rendered, kept)
  where
    drainCond (A.At _ pc) = Src._commentPos pc == Src.CommentOwnLine
    renderAtOwnCol c@(A.At _ pc) =
        renderComment (Src._commentCol pc - 1) c


-- Render a single comment at the given indent. Delimiters are
-- reattached (parser stored the raw body only). For line comments
-- the parser preserved leading body whitespace, so `--` + body
-- reconstructs the source form. Each rendered comment ends with a
-- newline; callers rely on this to concat cleanly.
renderComment :: Int -> A.Located Src.ParsedComment -> String
renderComment indent (A.At _ pc) =
    case Src._commentKind pc of
         Src.CommentLine  -> ind indent ++ "--" ++ Src._commentText pc ++ "\n"
         Src.CommentBlock -> ind indent ++ "{-" ++ Src._commentText pc ++ "-}\n"


-- | Strip trailing newlines from a String.
dropTrailingNL :: String -> String
dropTrailingNL s = case reverse s of
    '\n':rs -> dropTrailingNL (reverse rs)
    _       -> s


-- Threaded state-monad-in-disguise. Given a per-item renderer that
-- threads CS, fold across a list producing (finalCS, [rendered]).
mapAccumStr :: (CS -> a -> (CS, String)) -> CS -> [a] -> (CS, [String])
mapAccumStr = mapAccumCS

-- Polymorphic variant used by the collection rendering (list /
-- tuple / record / record-update): each element produces a pair
-- `(drainedString, itemString)` so the outer arm can decide multi-
-- line vs single-line based on whether ANY element has drained
-- comments.
mapAccumCS :: (CS -> a -> (CS, b)) -> CS -> [a] -> (CS, [b])
mapAccumCS _ cs []     = (cs, [])
mapAccumCS f cs (x:xs) =
    let (cs',  s)  = f cs x
        (cs'', ss) = mapAccumCS f cs' xs
    in (cs'', s : ss)


-- ═══════════════════════════════════════════════════════════
-- Module
-- ═══════════════════════════════════════════════════════════

-- | Tagged top-level declaration, keyed by original source position.
-- Tagging lets us merge aliases / unions / values into one list and
-- sort by line number so formatted output preserves the order the
-- user wrote — without this, the formatter always groups "all aliases,
-- then all unions, then all values", which silently rewrites files
-- like `type Page / type Msg / type alias Job / type alias Model`
-- into `type alias Job / type alias Model / type Page / type Msg`.
data TopDecl
    = DAlias (A.Located Src.Alias)
    | DUnion (A.Located Src.Union)
    | DValue (A.Located Src.Value)

topDeclLine :: TopDecl -> Int
topDeclLine (DAlias (A.At r _)) = A._line (A._start r)
topDeclLine (DUnion (A.At r _)) = A._line (A._start r)
topDeclLine (DValue (A.At r _)) = A._line (A._start r)

-- Values are separated by two blank lines in elm-format; type decls
-- (aliases + unions) by one. We emit a leading blank line per decl
-- that matches the *kind* of that decl, which is why each variant
-- carries its own leading-separator string.
--
-- Phase 3: drain own-line comments matching col 0 above this decl
-- at the start (header comments) AND drain any remaining inner-
-- indent own-line comments below the body but before `mNextLine`
-- (body-trailer comments) at their source column.
fmtTopDeclWithNext :: CS -> (TopDecl, Maybe Int) -> (CS, String)
fmtTopDeclWithNext cs (td, mNextLine) =
    let nodeLine       = topDeclLine td
        (drained, cs1) = drainBefore 0 nodeLine cs
        (cs2, sep, body) = fmtBody cs1 td
        (postDrained0, cs3) = case mNextLine of
            Just n  -> drainInnerUntil n cs2
            Nothing -> ("", cs2)
        postDrained    = dropTrailingNL postDrained0
        bodyPlus       = if null postDrained then body else body ++ "\n" ++ postDrained
    in (cs3, sep ++ drained ++ bodyPlus)
  where
    fmtBody cs' (DAlias a) = (cs', "\n",   fmtAlias (A.toValue a))
    fmtBody cs' (DUnion u) = (cs', "\n",   fmtUnion (A.toValue u))
    fmtBody cs' (DValue v) =
        let (cs'', vs) = fmtValue cs' (A.toValue v)
        in (cs'', "\n\n", vs)


formatModule :: Src.Module -> String
formatModule m =
    let header = case Src._name m of
            Just (A.At _ segs) ->
                let lhs = "module " ++ joinDots segs ++ " exposing"
                in lhs ++ fmtExposingClause (length lhs) (A.toValue (Src._exports m))
            Nothing -> ""
        imports = map fmtImport (Src._imports m)
        tagged = map DAlias (Src._aliases m)
              ++ map DUnion (Src._unions m)
              ++ map DValue (Src._values m)
        -- Comments enter sorted by source line so drainBefore's
        -- line-window filter is stable per node.
        cs0 = sortOn (A._line . A._start . A.toRegion) (Src._comments m)
        sortedDecls = sortOn topDeclLine tagged
        -- Pair each decl with the next decl's start line (Nothing
        -- for the last). Used by end-of-decl inner-indent drain
        -- to bound how far below-body comments should carry.
        nextLines = drop 1 (map (Just . topDeclLine) sortedDecls) ++ [Nothing]
        pairs = zip sortedDecls nextLines
        (csAfterDecls, orderedDecls) = mapAccumStr fmtTopDeclWithNext cs0 pairs
        -- Any comments left after the last decl land here — emit at
        -- their own source column so file-trailing comments survive.
        (trailingBlock0, _csFinal) = drainRemaining csAfterDecls
        trailingBlock = dropTrailingNL trailingBlock0
        trailingSection = if null trailingBlock then [] else ["\n" ++ trailingBlock]
        sections = filter (not . null) [header] ++
                   (if null imports then [] else ["\n" ++ intercalate "\n" imports]) ++
                   orderedDecls ++
                   trailingSection
    in intercalate "\n" sections ++ "\n"


joinDots :: [String] -> String
joinDots = intercalate "."


-- | Maximum single-line width before `fmtExposingAt` breaks the
-- exposing list across multiple lines. Matches the elm-format
-- convention (~100 chars). Anything past this threshold renders as
-- one-export-per-line with leading commas, indented 4 spaces.
maxLineWidth :: Int
maxLineWidth = 100


-- | Render the FULL exposing clause including the leading separator
-- (a single space for single-line, a newline + 4-space indent for
-- multi-line), choosing automatically based on the rendered length.
--
-- `lhsLen` is the column at which the "exposing" keyword ends —
-- i.e. `length "module Main exposing"` for module headers, or
-- `length "import Std.Log exposing"` for imports. The threshold is
-- `maxLineWidth`; if the single-line form (lhs + " " + "(...)")
-- would exceed it, switch to one-per-line with leading commas.
--
-- Multi-line shape (matches `sky fmt` convention for records/lists):
--
--     exposing
--         ( first
--         , second
--         , third
--         )
fmtExposingClause :: Int -> Src.Exposing -> String
fmtExposingClause _ Src.ExposingAll = " (..)"
fmtExposingClause lhsLen (Src.ExposingList items) =
    let rendered = map (fmtExposed . A.toValue) items
        single = "(" ++ intercalate ", " rendered ++ ")"
        totalLen = lhsLen + 1 + length single   -- +1 for the leading space
    in
        if totalLen <= maxLineWidth || length items <= 1 then
            " " ++ single
        else
            -- Multi-line: newline + 4-space indent, then one entry
            -- per line with leading commas. The opening "exposing"
            -- keyword stays on its own line above this output.
            "\n    ( " ++ intercalate "\n    , " rendered ++ "\n    )"


-- Single-line shim for the `formatTypeAnnotation`-style callers
-- that need the bare `(item, item)` form without leading space or
-- multi-line decision.
fmtExposing :: Src.Exposing -> String
fmtExposing Src.ExposingAll = "(..)"
fmtExposing (Src.ExposingList items) =
    "(" ++ intercalate ", " (map (fmtExposed . A.toValue) items) ++ ")"


fmtExposed :: Src.Exposed -> String
fmtExposed (Src.ExposedValue n) = n
fmtExposed (Src.ExposedType n Src.Public) = n ++ "(..)"
fmtExposed (Src.ExposedType n Src.Private) = n
fmtExposed (Src.ExposedType n (Src.PublicCtors cs)) = n ++ "(" ++ intercalate ", " cs ++ ")"
fmtExposed (Src.ExposedOperator n) = "(" ++ n ++ ")"

fmtImport :: Src.Import -> String
fmtImport imp =
    let name = joinDots (A.toValue (Src._importName imp))
        aliasPart = case Src._importAlias imp of
            Just a  -> " as " ++ a
            Nothing -> ""
        prefix = "import " ++ name ++ aliasPart
        exposingPart = case A.toValue (Src._importExposing imp) of
            Src.ExposingList [] -> ""
            exp_ ->
                let lhs = " exposing"
                    -- lhsLen is the column where "exposing" ENDS,
                    -- starting from column 0 (since each import is
                    -- on its own line, no outer indent).
                    lhsLen = length prefix + length lhs
                in lhs ++ fmtExposingClause lhsLen exp_
    in prefix ++ exposingPart

fmtAlias :: Src.Alias -> String
fmtAlias a =
    let name = A.toValue (Src._aliasName a)
        vars = map A.toValue (Src._aliasVars a)
        body = fmtTypeCol 4 (A.toValue (Src._aliasType a))
        varsStr = if null vars then "" else " " ++ unwords vars
    in "type alias " ++ name ++ varsStr ++ " =\n    " ++ body

fmtUnion :: Src.Union -> String
fmtUnion u =
    let name = A.toValue (Src._unionName u)
        vars = map A.toValue (Src._unionVars u)
        varsStr = if null vars then "" else " " ++ unwords vars
        ctors = map (fmtCtor . A.toValue) (Src._unionCtors u)
        body = case ctors of
            []     -> ""
            [c]    -> "\n    = " ++ c
            (c:cs) -> "\n    = " ++ c ++ concatMap (\c2 -> "\n    | " ++ c2) cs
    in "type " ++ name ++ varsStr ++ body

fmtCtor :: (String, [Src.TypeAnnotation]) -> String
fmtCtor (n, []) = n
fmtCtor (n, args) = n ++ " " ++ unwords (map fmtTypeParens args)

fmtValue :: CS -> Src.Value -> (CS, String)
fmtValue cs v =
    let name = A.toValue (Src._valueName v)
        annotStr = case Src._valueType v of
            Just (A.At _ t) -> name ++ " : " ++ fmtType t ++ "\n"
            Nothing -> ""
        params = map fmtPattern (Src._valuePatterns v)
        paramsStr = if null params then "" else " " ++ unwords params
        -- Phase 3: drain before body at indent 4 catches an own-line
        -- comment sitting between `foo x =` and the body expression.
        bodyLine = A._line (A._start (A.toRegion (Src._valueBody v)))
        (drained, cs1) = drainBefore 4 bodyLine cs
        (cs2, body) = fmtE 4 cs1 (Src._valueBody v)
        header = annotStr ++ name ++ paramsStr ++ " =\n"
        bodyBlock = if null drained
                    then ind 4 ++ body
                    else drained ++ ind 4 ++ body
    in (cs2, header ++ bodyBlock)


-- ═══════════════════════════════════════════════════════════
-- Types
-- ═══════════════════════════════════════════════════════════

-- | Column-aware type formatter. `col` is the current indent column
-- at which the rendered type starts; it informs the max-line-width
-- check and the continuation indent for multi-line records.
fmtType :: Src.TypeAnnotation -> String
fmtType = fmtTypeCol 0

fmtTypeCol :: Int -> Src.TypeAnnotation -> String
fmtTypeCol col t = case t of
    Src.TLambda a b -> fmtTypeAtomCol col a ++ " -> " ++ fmtTypeCol col b
    _ -> fmtTypeAtomCol col t

fmtTypeAtom :: Src.TypeAnnotation -> String
fmtTypeAtom = fmtTypeAtomCol 0

fmtTypeAtomCol :: Int -> Src.TypeAnnotation -> String
fmtTypeAtomCol _ (Src.TVar n) = n
fmtTypeAtomCol col (Src.TType _ segs args) =
    let n = joinDots segs
    in if null args then n
       else n ++ " " ++ unwords (map (fmtTypeParensCol col) args)
fmtTypeAtomCol col (Src.TTypeQual m n args) =
    let base = m ++ "." ++ n
    in if null args then base
       else base ++ " " ++ unwords (map (fmtTypeParensCol col) args)
fmtTypeAtomCol _ Src.TUnit = "()"
fmtTypeAtomCol col (Src.TTuple a b cs) =
    "( " ++ intercalate ", " (map (fmtTypeCol col) (a:b:cs)) ++ " )"
fmtTypeAtomCol col (Src.TRecord fs _) = fmtRecordType col fs
fmtTypeAtomCol col t@(Src.TLambda _ _) = "(" ++ fmtTypeCol col t ++ ")"

fmtTypeParens :: Src.TypeAnnotation -> String
fmtTypeParens = fmtTypeParensCol 0

fmtTypeParensCol :: Int -> Src.TypeAnnotation -> String
fmtTypeParensCol col t@(Src.TType _ _ (_:_)) =
    "(" ++ fmtTypeAtomCol col t ++ ")"
fmtTypeParensCol col t@(Src.TTypeQual _ _ (_:_)) =
    "(" ++ fmtTypeAtomCol col t ++ ")"
fmtTypeParensCol col t = fmtTypeAtomCol col t


-- | Record-type formatting with the same "one line or one-per-line"
-- rule the expression-level record literal formatter uses. Multi-
-- line breaks to leading commas at column `col`, matching elm-format.
fmtRecordType :: Int -> [(A.Located String, Src.TypeAnnotation)] -> String
fmtRecordType col fs =
    let oneField (A.At _ n, ty) = n ++ " : " ++ fmtTypeCol (col + 6) ty
        items = map oneField fs
        oneLine = "{ " ++ intercalate ", " items ++ " }"
    in if col + length oneLine <= 80 && length items <= 1
         then oneLine
         else case items of
            []     -> "{}"
            (i:is) -> "{ " ++ i
                  ++ concatMap (\it -> "\n" ++ ind col ++ ", " ++ it) is
                  ++ "\n" ++ ind col ++ "}"


-- ═══════════════════════════════════════════════════════════
-- Patterns
-- ═══════════════════════════════════════════════════════════

fmtPattern :: Src.Pattern -> String
fmtPattern (A.At _ p) = case p of
    Src.PAnything -> "_"
    Src.PVar n -> n
    Src.PUnit -> "()"
    Src.PInt n -> show n
    Src.PFloat f -> show f
    Src.PStr s -> "\"" ++ escapeStringLit s ++ "\""
    Src.PBool True -> "True"
    Src.PBool False -> "False"
    Src.PCtor n _ [] -> n
    Src.PCtor n _ args -> n ++ " " ++ unwords (map fmtPatternAtom args)
    Src.PCtorQual m n [] -> m ++ "." ++ n
    Src.PCtorQual m n args -> m ++ "." ++ n ++ " " ++ unwords (map fmtPatternAtom args)
    Src.PList ps -> "[" ++ intercalate ", " (map fmtPattern ps) ++ "]"
    Src.PCons hd tl -> fmtPatternAtom hd ++ " :: " ++ fmtPattern tl
    Src.PTuple a b cs -> "( " ++ intercalate ", " (map fmtPattern (a:b:cs)) ++ " )"
    Src.PRecord ns -> "{ " ++ intercalate ", " (map A.toValue ns) ++ " }"
    Src.PAlias inner (A.At _ n) -> fmtPattern inner ++ " as " ++ n
    Src.PChr s -> "'" ++ s ++ "'"

fmtPatternAtom :: Src.Pattern -> String
fmtPatternAtom p@(A.At _ p_) = case p_ of
    Src.PCtor _ _ (_:_)     -> "(" ++ fmtPattern p ++ ")"
    Src.PCtorQual _ _ (_:_) -> "(" ++ fmtPattern p ++ ")"
    Src.PCons _ _           -> "(" ++ fmtPattern p ++ ")"
    Src.PAlias _ _          -> "(" ++ fmtPattern p ++ ")"
    _                        -> fmtPattern p


-- ═══════════════════════════════════════════════════════════
-- Expressions — absolute column tracking
--
-- `fmt col cs expr_` formats an Expr_ starting at column `col`,
-- threading the comment stream `cs`. `fmtE col cs expr` unwraps
-- the Located wrapper for callers that carry regions.
-- ═══════════════════════════════════════════════════════════

-- | The indentation unit (4 spaces, matching Elm)
step :: Int
step = 4

-- | Produce `n` spaces
ind :: Int -> String
ind n = replicate n ' '


-- | Format a Located expression — CS-threading wrapper around `fmt`.
fmtE :: Int -> CS -> Src.Expr -> (CS, String)
fmtE col cs e = fmt col cs (A.toValue e)


-- | Format an expression at absolute column `col`, threading CS.
fmt :: Int -> CS -> Src.Expr_ -> (CS, String)
fmt _ cs (Src.Int n) = (cs, show n)
fmt _ cs (Src.Float f) = (cs, show f)
fmt _ cs (Src.Chr s) = (cs, "'" ++ s ++ "'")
fmt _ cs (Src.Str s) = (cs, "\"" ++ escapeStringLit s ++ "\"")
fmt _ cs (Src.MultilineStr s) = (cs, "\"\"\"" ++ escapeMultilineLit s ++ "\"\"\"")
fmt _ cs (Src.Var n) = (cs, n)
fmt _ cs (Src.VarQual m n) = (cs, m ++ "." ++ n)
fmt _ cs Src.Unit = (cs, "()")
fmt _ cs (Src.Op o) = (cs, "(" ++ o ++ ")")
fmt col cs (Src.Negate e) =
    let (cs', s) = fmtE col cs e in (cs', "-" ++ s)
-- Paren is the parser's way of keeping `(a - b) * c` grouped properly.
-- Emit the parens back out when formatting so the round-trip stays
-- stable. Without this the formatter drops them and subsequent builds
-- silently re-associate.
fmt col cs (Src.Paren e) =
    let (cs', s) = fmtE col cs e in (cs', "(" ++ s ++ ")")
fmt _ cs (Src.Accessor f) = (cs, "." ++ f)
fmt col cs (Src.Access e (A.At _ f)) =
    let (cs', s) = fmtE col cs e in (cs', s ++ "." ++ f)

-- Lists — Phase 3: drain BEFORE each element in multi-line mode so
-- own-line comments between list elements (#572) route to the AST
-- side instead of the deleted string-anchor path.
fmt _   cs (Src.List []) = (cs, "[]")
fmt col cs (Src.List xs) =
    let elemCol = col + 2
        renderElem c e =
            let eLine = A._line (A._start (A.toRegion e))
                (drained, c1) = drainBefore col eLine c
                (c2, s) = fmtE elemCol c1 e
            in (c2, (drained, s))
        (cs', pairs) = mapAccumCS renderElem cs xs
        anyDrained = any (not . null . fst) pairs
        items = map snd pairs
        oneLine = "[ " ++ intercalate ", " items ++ " ]"
        singleFits = not anyDrained
                  && col + length oneLine <= 80
                  && not (any (elem '\n') items)
    in if singleFits
       then (cs', oneLine)
       else (cs', renderCollectionWithDrains col "[ " ", " "]" pairs)

-- Tuples
fmt col cs (Src.Tuple a b rest) =
    let elemCol = col + 2
        allElems = a:b:rest
        renderElem c e =
            let eLine = A._line (A._start (A.toRegion e))
                (drained, c1) = drainBefore col eLine c
                (c2, s) = fmtE elemCol c1 e
            in (c2, (drained, s))
        (cs', pairs) = mapAccumCS renderElem cs allElems
        anyDrained = any (not . null . fst) pairs
        items = map snd pairs
        oneLine = "( " ++ intercalate ", " items ++ " )"
        singleFits = not anyDrained
                  && col + length oneLine <= 80
                  && not (any (elem '\n') items)
    in if singleFits
       then (cs', oneLine)
       else (cs', renderCollectionWithDrains col "( " ", " ")" pairs)

-- Records
fmt _   cs (Src.Record []) = (cs, "{}")
fmt col cs (Src.Record fs) =
    let elemCol = col + 2
        renderField c (A.At r n, e) =
            let fLine = A._line (A._start r)
                (drained, c1) = drainBefore col fLine c
                (c2, s) = fmtE elemCol c1 e
            in (c2, (drained, n ++ " = " ++ s))
        (cs', pairs) = mapAccumCS renderField cs fs
        anyDrained = any (not . null . fst) pairs
        items = map snd pairs
        oneLine = "{ " ++ intercalate ", " items ++ " }"
        singleFits = not anyDrained
                  && col + length oneLine <= 80
                  && not (any (elem '\n') items)
    in if singleFits
       then (cs', oneLine)
       else (cs', renderCollectionWithDrains col "{ " ", " "}" pairs)

-- Record update
fmt col cs (Src.Update (A.At _ n) fs) =
    let elemCol = col + 2
        renderField c (A.At r fn, e) =
            let fLine = A._line (A._start r)
                (drained, c1) = drainBefore col fLine c
                (c2, s) = fmtE elemCol c1 e
            in (c2, (drained, fn ++ " = " ++ s))
        (cs', pairs) = mapAccumCS renderField cs fs
        anyDrained = any (not . null . fst) pairs
        items = map snd pairs
        oneLine = "{ " ++ n ++ " | " ++ intercalate ", " items ++ " }"
        singleFits = not anyDrained && col + length oneLine <= 80
    in if singleFits
       then (cs', oneLine)
       else
         let (d0, i0) = head pairs
             openStr = "{ " ++ n ++ " | "
             fstBlock = if null d0
                         then openStr ++ i0
                         else stripLeadingIndent col d0
                              ++ ind col ++ openStr ++ i0
             restBlocks = concatMap
                 (\(d, i) -> "\n" ++ d ++ ind col ++ ", " ++ i)
                 (tail pairs)
             closer = "\n" ++ ind col ++ "}"
         in (cs', fstBlock ++ restBlocks ++ closer)

-- Function calls
fmt col cs (Src.Call f args) =
    let (cs1, funcStr) = fmtE col cs f
        argCol = col + step
        -- Speculative: render args at col to decide layout width.
        -- The state impact is DISCARDED; the real render below uses
        -- the same CS input (cs1) so the drained-comment output is
        -- identical to a single-shot rendering.
        (_,   flatArgs) = mapAccumStr (fmtArg col) cs1 args
        oneLine = funcStr ++ " " ++ unwords flatArgs
        fits = col + length oneLine <= 80 && not (any (elem '\n') flatArgs)
    in if fits
         then let (cs2, argStrs) = mapAccumStr (fmtArg col) cs1 args
              in (cs2, funcStr ++ " " ++ unwords argStrs)
         else let (cs2, argStrs) = mapAccumStr (fmtArg argCol) cs1 args
              in (cs2, funcStr ++ concatMap (\a -> "\n" ++ ind argCol ++ a) argStrs)

-- Binary operators (pipelines break at each |>)
fmt col cs (Src.Binops segs tail_) =
    let opCol = col + step
        -- Speculative one-line render at col.
        speculativeSeg c (e, A.At _ op) =
            let (c', s) = fmtE col c e in (c', s ++ " " ++ op ++ " ")
        (_, flatParts) = mapAccumStr speculativeSeg cs segs
        (_, flatTail)  = fmtE col cs tail_
        oneLine = concat flatParts ++ flatTail
        fits = col + length oneLine <= 80 && not ('\n' `elem` oneLine)
    in if fits
         then let (cs1, parts) = mapAccumStr speculativeSeg cs segs
                  (cs2, tailStr) = fmtE col cs1 tail_
              in (cs2, concat parts ++ tailStr)
         else let firstSeg = head segs
                  restSegs = tail segs
                  lastOp = case last segs of (_, A.At _ o) -> o
                  (firstE, _) = firstSeg
                  (cs1, fmtFirst) = fmtE col cs firstE
                  renderRest c (e, A.At _ op) =
                      let rhsCol = opCol + length op + 1
                          (c', s) = fmtE rhsCol c e
                      in (c', "\n" ++ ind opCol ++ op ++ " " ++ s)
                  (cs2, restStrs) = mapAccumStr renderRest cs1 restSegs
                  rhsColLast = opCol + length lastOp + 1
                  (cs3, tailStr) = fmtE rhsColLast cs2 tail_
                  fmtLast = "\n" ++ ind opCol ++ lastOp ++ " " ++ tailStr
              in (cs3, fmtFirst ++ concat restStrs ++ fmtLast)

-- Lambda — Phase 2 drain hook, Phase 3 permissive-column widening.
-- Own-line comments whose source line is above the body's start
-- line get emitted between `\... ->` and the body's rendered form.
-- If any drained, the output is forced to multi-line so the
-- comments have somewhere to sit.  Uses `drainAnyBefore` (no col
-- filter) because the lambda body may re-indent to a column that
-- doesn't match the source col (common when the lambda is nested
-- inside a call arg — `fmtArg` wraps in parens, shifting bodyCol).
fmt col cs (Src.Lambda pats body) =
    let paramsStr = unwords (map fmtPattern pats)
        bodyRegion = A.toRegion body
        bodyLine = A._line (A._start bodyRegion)
        bodyCol = col + step
        (drained, cs1) = drainAnyBefore bodyCol bodyLine cs
        (cs2, bodyStr) = fmtE bodyCol cs1 body
        oneLine = "\\" ++ paramsStr ++ " -> " ++ bodyStr
        multiLine = "\\" ++ paramsStr ++ " ->\n" ++ drained ++ ind bodyCol ++ bodyStr
    in (cs2, if null drained && not ('\n' `elem` bodyStr) && col + length oneLine <= 80
             then oneLine
             else multiLine)

-- If/then/else — Phase 3: drain before elseE at col+step.
fmt col cs (Src.If branches elseE) =
    let renderBranch c (cond, body) =
            let (c1, condStr) = fmtE col c cond
                (c2, bodyStr) = fmtE (col + step) c1 body
            in (c2, "if " ++ condStr ++ " then\n"
                 ++ ind (col + step) ++ bodyStr)
        (cs1, branchStrs) = mapAccumStr renderBranch cs branches
        elseLine = A._line (A._start (A.toRegion elseE))
        (elseDrained, cs1a) = drainBefore (col + step) elseLine cs1
        (cs2, elseBody) = fmtE (col + step) cs1a elseE
        elseStr = "else\n" ++ elseDrained ++ ind (col + step) ++ elseBody
    in (cs2, intercalate ("\n\n" ++ ind col ++ "else ") branchStrs
             ++ "\n\n" ++ ind col ++ elseStr)

-- Let/in — Phase 3: drain BEFORE each Def at col+step and BEFORE
-- the body. Handles the "body comment above let-binding survives
-- reflow" case (#353) purely on the AST side.
fmt col cs (Src.Let defs body) =
    let defCol = col + step
        renderDef c d =
            let defLine = A._line (A._start (A.toRegion d))
                (drained, c1) = drainBefore defCol defLine c
                (c2, s) = fmtDef defCol c1 (A.toValue d)
            in (c2, drained ++ ind defCol ++ s ++ "\n")
        (cs1, defStrs) = mapAccumStr renderDef cs defs
        bodyLine = A._line (A._start (A.toRegion body))
        (bodyDrained, cs1a) = drainBefore defCol bodyLine cs1
        (cs2, bodyStr) = fmtE defCol cs1a body
    in (cs2, "let\n"
             ++ concat defStrs
             ++ bodyDrained
             ++ ind col ++ "in\n"
             ++ ind defCol ++ bodyStr)

-- Case — Phase 3: drain BEFORE each branch's pattern at col+step.
-- Handles `case … of \n -- click branch \n Click -> 1` (#144).
fmt col cs (Src.Case subj branches) =
    let subjCol = col + 5
        branchCol = col + step
        (cs1, subjStr) = fmtE subjCol cs subj
        renderBranch c (pat, body) =
            let patLine = A._line (A._start (A.toRegion pat))
                (drained, c1) = drainBefore branchCol patLine c
                (c2, br) = fmtCaseBranch branchCol c1 (pat, body)
            in (c2, drained ++ ind branchCol ++ br)
        (cs2, branchStrs) = mapAccumStr renderBranch cs1 branches
    in (cs2, "case " ++ subjStr ++ " of"
             ++ concatMap (\b -> "\n\n" ++ b) branchStrs)


-- ═══════════════════════════════════════════════════════════
-- Helpers
-- ═══════════════════════════════════════════════════════════

-- | Multi-line collection render with per-element drain block.
-- Each `pair` is `(drained-string, item-string)`.  `drained` is
-- pre-rendered with `<indent col>` prefix + trailing newline per
-- line (see `renderComment`).
--
-- Convention: the caller has already positioned the cursor at
-- `col`. So the FIRST character of our output should be `open`
-- (or the first drain line MINUS its leading `<col>` prefix, since
-- the caller already emitted `<col>`). Subsequent lines in the
-- output carry their own `\n<col>` indent.
--
-- Layout when `d0` is empty:
--
--     <open><item0>
--     <col>[<d1>]<sep><item1>
--     ...
--     <col><close>
--
-- Layout when `d0` is non-empty (first-line-indent stripped so it
-- lands at the caller-provided `col`):
--
--     <d0-line-1-body>
--     <col><d0-line-2>...
--     <col><open><item0>
--     <col><d1><col><sep><item1>
--     ...
--     <col><close>
renderCollectionWithDrains
    :: Int -> String -> String -> String
    -> [(String, String)] -> String
renderCollectionWithDrains col open sep close pairs =
    let (d0, i0) = head pairs
        firstBlock =
            if null d0
              then open ++ i0
              else stripLeadingIndent col d0
                    ++ ind col ++ open ++ i0
        restBlocks = concatMap
            (\(d, i) -> "\n" ++ d ++ ind col ++ sep ++ i)
            (tail pairs)
        closer = "\n" ++ ind col ++ close
    in firstBlock ++ restBlocks ++ closer


-- | Strip `col` leading space characters from the string.  Used to
-- align a `renderComment`-emitted first line with the caller-
-- provided col prefix (see `renderCollectionWithDrains`).  If the
-- first `col` chars are not all spaces, returns the input unchanged
-- (defensive — should not fire under our own callers, but avoids
-- silent misalignment if a caller passes a string with a different
-- shape).
stripLeadingIndent :: Int -> String -> String
stripLeadingIndent col s =
    let (leader, rest) = splitAt col s
    in if all (== ' ') leader && length leader == col
         then rest
         else s


-- | Format a function argument — parens around complex expressions
fmtArg :: Int -> CS -> Src.Expr -> (CS, String)
fmtArg col cs e = case A.toValue e of
    Src.Call _ _   -> wrapParen col cs e
    Src.Binops _ _ -> wrapParen col cs e
    Src.If _ _     -> wrapParen col cs e
    Src.Let _ _    -> wrapParen col cs e
    Src.Case _ _   -> wrapParen col cs e
    Src.Lambda _ _ -> wrapParen col cs e
    Src.Negate _   -> wrapParen col cs e
    _              -> fmtE col cs e


-- | Wrap in parens — multi-line bodies get indented inside
wrapParen :: Int -> CS -> Src.Expr -> (CS, String)
wrapParen col cs e =
    let (cs', body) = fmtE (col + 1) cs e
    in (cs', "(" ++ body ++ ")")


-- | Format a case branch
fmtCaseBranch :: Int -> CS -> (Src.Pattern, Src.Expr) -> (CS, String)
fmtCaseBranch col cs (pat, body) =
    let (cs', bodyStr) = fmtE (col + step) cs body
    in (cs', fmtPattern pat ++ " ->\n" ++ ind (col + step) ++ bodyStr)


-- | Format a let binding
fmtDef :: Int -> CS -> Src.Def -> (CS, String)
fmtDef col cs d = case d of
    Src.Destruct pat body ->
        let patStr = fmtPattern pat
            (cs', bodyStr) = fmtE (col + step) cs body
            oneLine = patStr ++ " = " ++ bodyStr
        in (cs', if not ('\n' `elem` bodyStr) && col + length oneLine <= 76
                 then oneLine
                 else patStr ++ " =\n" ++ ind (col + step) ++ bodyStr)
    _ ->
        let name = A.toValue (Src._defName d)
            params = map fmtPattern (Src._defPatterns d)
            paramsStr = if null params then "" else " " ++ unwords params
            (cs', bodyStr) = fmtE (col + step) cs (Src._defBody d)
            oneLine = name ++ paramsStr ++ " = " ++ bodyStr
        in (cs', if not ('\n' `elem` bodyStr) && col + length oneLine <= 76
                 then oneLine
                 else name ++ paramsStr ++ " =\n" ++ ind (col + step) ++ bodyStr)


-- ═══════════════════════════════════════════════════════════
-- String escaping
-- ═══════════════════════════════════════════════════════════

escapeStringLit :: String -> String
escapeStringLit = concatMap esc
  where
    esc '\\' = "\\\\"
    esc '"'  = "\\\""
    esc '\n' = "\\n"
    esc '\t' = "\\t"
    esc '\r' = "\\r"
    esc c    = [c]

-- Multiline (`"""..."""`) strings are preserved VERBATIM by the
-- parser (see Sky.Parse.String — no unescapeString applied), so
-- the formatter MUST also preserve verbatim or the round-trip
-- breaks. This was the bug behind issue 2026-05-18: a single
-- `\test` in a multiline source became `\\test` after `sky fmt`
-- because escapeMultilineLit was applying single-line-string
-- backslash-doubling, which then re-parsed as a literal `\\test`.
--
-- The only character a multiline string CAN'T contain is the
-- closing `"""` sequence — but the parser has no escape syntax
-- for it either, so a source containing `"""` was already
-- malformed before reaching the formatter. We pass it through
-- as-is (lets the user spot the issue on the next compile)
-- rather than silently inserting `\` escapes the parser would
-- read as literal backslash-quote bytes.
--
-- Net: identity function. Whole purpose of multiline strings
-- (JavaScript / CSS / JSON / SQL embedding) requires it.
escapeMultilineLit :: String -> String
escapeMultilineLit = id
