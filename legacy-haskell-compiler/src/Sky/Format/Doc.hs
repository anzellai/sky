-- | Wadler-Lindig pretty printer for Sky source code.
--
-- Two-phase approach:
--   1. Build a Doc tree (width-agnostic)
--   2. Layout the Doc to a String given maxWidth=80
--
-- The key combinator is `group`: it tries to render its
-- children on one line (replacing Line with space). If the
-- result exceeds maxWidth, it breaks at every Line.
module Sky.Format.Doc
    ( Doc(..)
    , nil
    , text
    , line
    , lineOrSpace
    , lineOrEmpty
    , indent
    , group
    , concat_
    , intercalateDoc
    , layout
    , render
    , parens
    , brackets
    , braces
    , sep
    , commaSep
    , leadingComma
    ) where


-- | The document algebra.
data Doc
    = DText !String
    | DLine                -- hard line break (always breaks)
    | DSoftLine            -- break if group is too wide, else space
    | DSoftEmpty           -- break if group is too wide, else nothing
    | DIndent !Int !Doc
    | DGroup !Doc          -- try one-line; if > maxWidth, break
    | DConcat ![Doc]
    | DNil
    deriving (Show)


maxWidth :: Int
maxWidth = 80


nil :: Doc
nil = DNil

text :: String -> Doc
text "" = DNil
text s  = DText s

line :: Doc
line = DLine

lineOrSpace :: Doc
lineOrSpace = DSoftLine

lineOrEmpty :: Doc
lineOrEmpty = DSoftEmpty

indent :: Int -> Doc -> Doc
indent n d = DIndent n d

group :: Doc -> Doc
group d = DGroup d

concat_ :: [Doc] -> Doc
concat_ [] = DNil
concat_ [d] = d
concat_ ds = DConcat ds

intercalateDoc :: Doc -> [Doc] -> Doc
intercalateDoc _   []     = DNil
intercalateDoc _   [d]    = d
intercalateDoc sep' (d:ds) = concat_ (d : concatMap (\x -> [sep', x]) ds)

sep :: [Doc] -> Doc
sep = intercalateDoc (text " ")

commaSep :: [Doc] -> Doc
commaSep = intercalateDoc (concat_ [text ",", lineOrSpace])

leadingComma :: [Doc] -> Doc
leadingComma []     = DNil
leadingComma [d]    = d
leadingComma (d:ds) = concat_ (d : map (\x -> concat_ [DLine, text ", ", x]) ds)


parens :: Doc -> Doc
parens d = group (concat_ [text "(", d, text ")"])

brackets :: Doc -> Doc
brackets d = group (concat_ [text "[ ", d, lineOrEmpty, text "]"])

braces :: Doc -> Doc
braces d = group (concat_ [text "{ ", d, lineOrEmpty, text "}"])


-- | Render a Doc to a String.
render :: Doc -> String
render = layout 0


-- | Core layout engine. Processes the Doc tree and produces a
-- string with proper indentation and line breaks.
layout :: Int -> Doc -> String
layout ind doc = case doc of
    DNil        -> ""
    DText s     -> s
    DLine       -> "\n" ++ replicate ind ' '
    DSoftLine   -> " "   -- in non-group context, soft = space
    DSoftEmpty  -> ""
    DIndent n d -> layout (ind + n) d
    DConcat ds  -> concatMap (layout ind) ds
    DGroup d    ->
        let flat = layoutFlat d
        in if length flat + ind <= maxWidth
           then flat
           else layoutBreak ind d


-- | Render a Doc with all soft breaks as spaces (one-line mode).
layoutFlat :: Doc -> String
layoutFlat doc = case doc of
    DNil        -> ""
    DText s     -> s
    DLine       -> " "
    DSoftLine   -> " "
    DSoftEmpty  -> ""
    DIndent _ d -> layoutFlat d
    DConcat ds  -> concatMap layoutFlat ds
    DGroup d    -> layoutFlat d


-- | Render a Doc with all soft breaks as newlines (multi-line mode).
layoutBreak :: Int -> Doc -> String
layoutBreak ind doc = case doc of
    DNil        -> ""
    DText s     -> s
    DLine       -> "\n" ++ replicate ind ' '
    DSoftLine   -> "\n" ++ replicate ind ' '
    DSoftEmpty  -> "\n" ++ replicate ind ' '
    DIndent n d -> layoutBreak (ind + n) d
    DConcat ds  -> concatMap (layoutBreak ind) ds
    DGroup d    ->
        let flat = layoutFlat d
        in if length flat + ind <= maxWidth
           then flat
           else layoutBreak ind d
