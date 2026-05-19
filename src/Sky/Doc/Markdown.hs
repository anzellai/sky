{-# LANGUAGE OverloadedStrings #-}
-- | Sky.Doc.Markdown — a minimal Markdown → HTML renderer for
-- the doc-comment blocks above each Sky binding.
--
-- We support the subset that Sky-source `-- |` comments actually
-- use in the stdlib + examples today:
--
--   * Paragraphs (blank-line-separated)
--   * Inline code `like-this`
--   * Inline links [text](url)
--   * Emphasis *like this* and **like this**
--   * Fenced code blocks (```sky / ```elm / ``` ...)
--   * Unordered lists (- foo / * foo)
--   * Headings (## Foo) — for module-level doc blocks
--
-- Anything fancier (tables, definition lists, nested blockquotes,
-- HTML passthrough) is rendered as plain text. The goal is to
-- make stdlib doc comments LEGIBLE, not to be a full CommonMark
-- engine. The output is intentionally minimal HTML — works with
-- the doc server's stylesheet without extra CSS.
--
-- Pure-string transformation, no deps.
module Sky.Doc.Markdown
    ( renderMarkdown
    ) where

import qualified Data.Char as Char
import qualified Data.List as List


-- | Convert a doc-comment string to escaped HTML. The string is
-- the content of one or more `-- |` lines (with the leading
-- `-- |` already stripped — the LSP extractor does that).
renderMarkdown :: String -> String
renderMarkdown input =
    let
        blocks = splitBlocks (normaliseLines input)
    in
        concatMap renderBlock blocks


-- ─── Block-level parsing ────────────────────────────────────────

data Block
    = Paragraph [String]   -- non-empty, plain text lines (post inline-pass)
    | CodeFence String [String]  -- lang, lines
    | UnorderedList [[String]]   -- items, each a list of lines
    | Heading Int String         -- level, text
    deriving (Show)


-- | Strip trailing whitespace + collapse multi-blank-line runs to
-- a single blank.
normaliseLines :: String -> [String]
normaliseLines = map dropTrailingSpace . lines
  where
    dropTrailingSpace = reverse . dropWhile (== ' ') . reverse


-- | Walk lines, emit blocks. Stateful: blank lines close the
-- current block.
splitBlocks :: [String] -> [Block]
splitBlocks [] = []
splitBlocks (l:ls)
    | "```" `List.isPrefixOf` l =
        let lang = dropWhile (== '`') l
            (body, rest) = span (not . ("```" `List.isPrefixOf`)) ls
            -- Drop the closing fence line from rest if present.
            rest' = case rest of { (_:xs) -> xs ; [] -> [] }
        in CodeFence lang body : splitBlocks rest'
    | "## " `List.isPrefixOf` l =
        Heading 2 (drop 3 l) : splitBlocks ls
    | "# " `List.isPrefixOf` l =
        Heading 1 (drop 2 l) : splitBlocks ls
    | isListMarker l =
        let (items, rest) = collectListItems (l:ls)
        in UnorderedList items : splitBlocks rest
    | null l =
        splitBlocks ls
    | otherwise =
        let (paraLines, rest) = span paraLine ls
        in Paragraph (l : paraLines) : splitBlocks rest


paraLine :: String -> Bool
paraLine s =
    not (null s)
    && not ("```" `List.isPrefixOf` s)
    && not ("## " `List.isPrefixOf` s)
    && not ("# " `List.isPrefixOf` s)
    && not (isListMarker s)


isListMarker :: String -> Bool
isListMarker s =
    let t = dropWhile (== ' ') s
    in "- " `List.isPrefixOf` t || "* " `List.isPrefixOf` t


-- | Collect consecutive `- item` / `* item` lines into a list,
-- folding continuation lines (indented by 2+ spaces) into the
-- current item. Stops at the first non-list, non-continuation
-- line.
collectListItems :: [String] -> ([[String]], [String])
collectListItems = go []
  where
    go acc [] = (reverse acc, [])
    go acc (l:ls)
        | isListMarker l =
            let item = dropListMarker l
                (continuation, rest) = span isContinuation ls
                fullItem = item : map (dropWhile (== ' ')) continuation
            in go (fullItem : acc) rest
        | otherwise =
            (reverse acc, l:ls)
    dropListMarker s =
        let t = dropWhile (== ' ') s
        in dropWhile (== ' ') (drop 2 t)
    isContinuation s =
        not (null s)
        && not (isListMarker s)
        && (head s == ' ' || head s == '\t')


-- ─── Block rendering ────────────────────────────────────────────

renderBlock :: Block -> String
renderBlock (Paragraph ls) =
    "<p>" ++ inline (List.intercalate " " ls) ++ "</p>"
renderBlock (Heading lvl text) =
    let tag = "h" ++ show (lvl + 3)  -- h4/h5 — doc-comment headings nest INSIDE the symbol's h3
    in "<" ++ tag ++ ">" ++ inline text ++ "</" ++ tag ++ ">"
renderBlock (CodeFence _lang body) =
    "<pre class='code'>" ++ esc (List.intercalate "\n" body) ++ "</pre>"
renderBlock (UnorderedList items) =
    "<ul>" ++ concatMap renderItem items ++ "</ul>"
  where
    renderItem ls = "<li>" ++ inline (List.intercalate " " ls) ++ "</li>"


-- ─── Inline transformations ─────────────────────────────────────

-- | Apply inline transformations in order. Each pass operates on
-- plain text; sequential application is safe because each
-- transform only matches its own delimiters.
inline :: String -> String
inline =
    inlineLinks      -- [text](url)         — must run before code so [...] doesn't get escaped
    . inlineBoldEm   -- **bold** / *em*     — before code likewise
    . inlineCode     -- `code`              — escapes innards
    . escNonCode     -- final escape pass


-- | Escape angle brackets / ampersands / quotes EXCEPT inside the
-- placeholder spans the inline transforms already produced.
escNonCode :: String -> String
escNonCode = concatMap one
  where
    one '<'  = "&lt;"
    one '>'  = "&gt;"
    one '&'  = "&amp;"
    one '"'  = "&quot;"
    one '\'' = "&#39;"
    one c    = [c]


esc :: String -> String
esc = escNonCode


-- | Replace `…` runs with `<code>…</code>` placeholders. The
-- placeholder uses HTML escape codes the subsequent passes won't
-- touch (the back-ticks are gone by then).
inlineCode :: String -> String
inlineCode "" = ""
inlineCode ('\\':'`':rest) = '`' : inlineCode rest  -- escaped backtick
inlineCode ('`':rest) =
    case break (== '`') rest of
        (code, '`':rest') ->
            "<code>" ++ escNonCode code ++ "</code>" ++ inlineCode rest'
        _ ->
            '`' : inlineCode rest
inlineCode (c:rest) = c : inlineCode rest


-- | `[text](url)` → `<a href="url">text</a>`. URL is rendered as
-- the raw value (escaped) so it works for relative + absolute
-- links; no special handling for fancy syntaxes.
inlineLinks :: String -> String
inlineLinks "" = ""
inlineLinks ('[':rest) =
    case break (== ']') rest of
        (txt, ']':'(':rest') ->
            case break (== ')') rest' of
                (url, ')':rest'') ->
                    "<a href='" ++ escAttr url ++ "'>" ++ inlineLinks txt ++ "</a>"
                        ++ inlineLinks rest''
                _ ->
                    '[' : inlineLinks rest
        _ ->
            '[' : inlineLinks rest
inlineLinks (c:rest) = c : inlineLinks rest


escAttr :: String -> String
escAttr = concatMap one
  where
    one '"'  = "&quot;"
    one '\'' = "&#39;"
    one '<'  = "&lt;"
    one '>'  = "&gt;"
    one '&'  = "&amp;"
    one c    = [c]


-- | `**bold**` → `<strong>bold</strong>` and `*em*` →
-- `<em>em</em>`. Bold parses first so `**foo**` doesn't get
-- consumed by the em rule.
inlineBoldEm :: String -> String
inlineBoldEm = pass '*' "**" "strong" . pass '*' "*" "em"
  where
    pass :: Char -> String -> String -> String -> String
    pass _ delim tag s0 = go s0
      where
        go "" = ""
        go s
            | delim `List.isPrefixOf` s =
                let body = drop (length delim) s
                in case findClose body of
                    Just (inner, after) ->
                        "<" ++ tag ++ ">" ++ go inner ++ "</" ++ tag ++ ">"
                            ++ go after
                    Nothing -> head s : go (tail s)
            | otherwise = head s : go (tail s)
        findClose s =
            case List.stripPrefix delim s of
                Just _  -> Nothing  -- empty content, skip
                Nothing -> step "" s
        step acc s
            | delim `List.isPrefixOf` s =
                Just (reverse acc, drop (length delim) s)
            | null s = Nothing
            | otherwise =
                step (head s : acc) (tail s)


-- Avoid unused-warning churn.
_unusedIsSpace :: Char -> Bool
_unusedIsSpace = Char.isSpace
