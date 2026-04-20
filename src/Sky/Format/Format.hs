-- | Elm-format-style pretty printer for Sky source code.
--
-- Uses absolute column tracking: every function takes `col :: Int`
-- (the current indentation column) and produces strings with
-- newlines at the correct absolute position. The golden rule is
-- "one line or each on its own line" — never mix.
module Sky.Format.Format (formatModule) where

import Data.List (intercalate, sortOn)
import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A


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
fmtTopDecl :: TopDecl -> String
fmtTopDecl (DAlias a) = "\n" ++ fmtAlias (A.toValue a)
fmtTopDecl (DUnion u) = "\n" ++ fmtUnion (A.toValue u)
fmtTopDecl (DValue v) = "\n\n" ++ fmtValue (A.toValue v)

formatModule :: Src.Module -> String
formatModule m =
    let header = case Src._name m of
            Just (A.At _ segs) ->
                "module " ++ joinDots segs ++ " exposing " ++ fmtExposing (A.toValue (Src._exports m))
            Nothing -> ""
        imports = map fmtImport (Src._imports m)
        tagged = map DAlias (Src._aliases m)
              ++ map DUnion (Src._unions m)
              ++ map DValue (Src._values m)
        orderedDecls = map fmtTopDecl (sortOn topDeclLine tagged)
        sections = filter (not . null) [header] ++
                   (if null imports then [] else ["\n" ++ intercalate "\n" imports]) ++
                   orderedDecls
        _ = Src._comments m
    in intercalate "\n" sections ++ "\n"


joinDots :: [String] -> String
joinDots = intercalate "."

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
        exposingPart = case A.toValue (Src._importExposing imp) of
            Src.ExposingList [] -> ""
            exp_ -> " exposing " ++ fmtExposing exp_
    in "import " ++ name ++ aliasPart ++ exposingPart

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

fmtValue :: Src.Value -> String
fmtValue v =
    let name = A.toValue (Src._valueName v)
        annotStr = case Src._valueType v of
            Just (A.At _ t) -> name ++ " : " ++ fmtType t ++ "\n"
            Nothing -> ""
        params = map fmtPattern (Src._valuePatterns v)
        paramsStr = if null params then "" else " " ++ unwords params
        body = fmt 4 (A.toValue (Src._valueBody v))
    in annotStr ++ name ++ paramsStr ++ " =\n    " ++ body


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
-- `fmt col expr` formats an expression starting at column `col`.
-- Multi-line constructs break with newlines at `col` indentation.
-- ═══════════════════════════════════════════════════════════

-- | The indentation unit (4 spaces, matching Elm)
step :: Int
step = 4

-- | Produce `n` spaces
ind :: Int -> String
ind n = replicate n ' '

-- | Format an expression at absolute column `col`
fmt :: Int -> Src.Expr_ -> String
fmt _ (Src.Int n) = show n
fmt _ (Src.Float f) = show f
fmt _ (Src.Chr s) = "'" ++ s ++ "'"
fmt _ (Src.Str s) = "\"" ++ escapeStringLit s ++ "\""
fmt _ (Src.MultilineStr s) = "\"\"\"" ++ escapeMultilineLit s ++ "\"\"\""
fmt _ (Src.Var n) = n
fmt _ (Src.VarQual m n) = m ++ "." ++ n
fmt _ Src.Unit = "()"
fmt _ (Src.Op o) = "(" ++ o ++ ")"
fmt col (Src.Negate e) = "-" ++ fmt col (A.toValue e)
fmt _ (Src.Accessor f) = "." ++ f
fmt col (Src.Access e (A.At _ f)) = fmt col (A.toValue e) ++ "." ++ f

-- Lists
fmt col (Src.List []) = "[]"
fmt col (Src.List [x]) = "[" ++ fmt col (A.toValue x) ++ "]"
fmt col (Src.List xs) = fmtCollection col "[ " ", " "]" (map (\x -> fmt (col + 2) (A.toValue x)) xs)

-- Tuples
fmt col (Src.Tuple a b cs) =
    fmtCollection col "( " ", " ")" (map (\x -> fmt (col + 2) (A.toValue x)) (a:b:cs))

-- Records
fmt col (Src.Record []) = "{}"
fmt col (Src.Record fs) =
    fmtCollection col "{ " ", " "}" (map (\(A.At _ n, e) -> n ++ " = " ++ fmt (col + 2) (A.toValue e)) fs)

-- Record update
fmt col (Src.Update (A.At _ n) fs) =
    let items = map (\(A.At _ fn, e) -> fn ++ " = " ++ fmt (col + 2) (A.toValue e)) fs
        oneLine = "{ " ++ n ++ " | " ++ intercalate ", " items ++ " }"
    in if col + length oneLine <= 80
       then oneLine
       else "{ " ++ n ++ " | " ++ head items
            ++ concatMap (\i -> "\n" ++ ind col ++ ", " ++ i) (tail items)
            ++ "\n" ++ ind col ++ "}"

-- Function calls
fmt col (Src.Call f args) =
    let funcStr = fmt col (A.toValue f)
        argStrs = map (fmtArg col) args
        oneLine = funcStr ++ " " ++ unwords argStrs
        argCol = col + step
    in if col + length oneLine <= 80
       then oneLine
       else funcStr
            ++ concatMap (\a -> "\n" ++ ind argCol ++ fmtArg argCol a) args

-- Binary operators (pipelines break at each |>)
fmt col (Src.Binops segs tail_) =
    let opCol = col + step
        parts = map (\(e, A.At _ op) -> (e, op)) segs
        fmtPart (e, op) = fmt col (A.toValue e) ++ " " ++ op ++ " "
        oneLine = concatMap fmtPart parts ++ fmt col (A.toValue tail_)
    in if col + length oneLine <= 80
       then oneLine
       else let (firstE, _) = head parts
                rest = tail parts
                lastOp = snd (last parts)
                fmtFirst = fmt col (A.toValue firstE)
                fmtRest (e, op) =
                    let rhsCol = opCol + length op + 1
                    in "\n" ++ ind opCol ++ op ++ " " ++ fmt rhsCol (A.toValue e)
                fmtLast =
                    let rhsCol = opCol + length lastOp + 1
                    in "\n" ++ ind opCol ++ lastOp ++ " " ++ fmt rhsCol (A.toValue tail_)
            in fmtFirst ++ concatMap fmtRest rest ++ fmtLast

-- Lambda
fmt col (Src.Lambda pats body) =
    let paramsStr = unwords (map fmtPattern pats)
        bodyStr = fmt (col + step) (A.toValue body)
    in if not ('\n' `elem` bodyStr) && col + length ("\\" ++ paramsStr ++ " -> " ++ bodyStr) <= 80
       then "\\" ++ paramsStr ++ " -> " ++ bodyStr
       else "\\" ++ paramsStr ++ " ->\n" ++ ind (col + step) ++ bodyStr

-- If/then/else
fmt col (Src.If branches elseE) =
    let fmtBranch (cond, body) =
            "if " ++ fmt col (A.toValue cond) ++ " then\n"
            ++ ind (col + step) ++ fmt (col + step) (A.toValue body)
        branchStrs = map fmtBranch branches
        elseStr = "else\n" ++ ind (col + step) ++ fmt (col + step) (A.toValue elseE)
    in intercalate ("\n\n" ++ ind col ++ "else ") branchStrs
       ++ "\n\n" ++ ind col ++ elseStr

-- Let/in
fmt col (Src.Let defs body) =
    let defStrs = map (fmtDef (col + step) . A.toValue) defs
        bodyStr = fmt (col + step) (A.toValue body)
    in "let\n"
       ++ concatMap (\d -> ind (col + step) ++ d ++ "\n") defStrs
       ++ ind col ++ "in\n"
       ++ ind (col + step) ++ bodyStr

-- Case — subject formatted at col+5 (after "case "), "of" on same line
fmt col (Src.Case subj branches) =
    let subjCol = col + 5
        subjStr = fmt subjCol (A.toValue subj)
        branchStrs = map (fmtCaseBranch (col + step)) branches
    in "case " ++ subjStr ++ " of"
       ++ concatMap (\b -> "\n\n" ++ ind (col + step) ++ b) branchStrs


-- ═══════════════════════════════════════════════════════════
-- Helpers
-- ═══════════════════════════════════════════════════════════

-- | Format a collection (list, tuple, record) with leading commas.
-- Short: `[ a, b, c ]`  Long: `[ a\n, b\n, c\n]`
fmtCollection :: Int -> String -> String -> String -> [String] -> String
fmtCollection col open sep close items =
    let oneLine = open ++ intercalate sep items ++ " " ++ close
    in if col + length oneLine <= 80
       then oneLine
       else open ++ head items
            ++ concatMap (\i -> "\n" ++ ind col ++ sep ++ i) (tail items)
            ++ "\n" ++ ind col ++ close


-- | Format a function argument — parens around complex expressions
fmtArg :: Int -> Src.Expr -> String
fmtArg col e = case A.toValue e of
    Src.Call _ _   -> wrapParen col e
    Src.Binops _ _ -> wrapParen col e
    Src.If _ _     -> wrapParen col e
    Src.Let _ _    -> wrapParen col e
    Src.Case _ _   -> wrapParen col e
    Src.Lambda _ _ -> wrapParen col e
    Src.Negate _   -> wrapParen col e
    _              -> fmt col (A.toValue e)


-- | Wrap in parens — multi-line bodies get indented inside
wrapParen :: Int -> Src.Expr -> String
wrapParen col e =
    let body = fmt (col + 1) (A.toValue e)
    in if '\n' `elem` body
       then "(" ++ body ++ ")"
       else "(" ++ body ++ ")"


-- | Format a case branch
fmtCaseBranch :: Int -> (Src.Pattern, Src.Expr) -> String
fmtCaseBranch col (pat, body) =
    fmtPattern pat ++ " ->\n" ++ ind (col + step) ++ fmt (col + step) (A.toValue body)


-- | Format a let binding
fmtDef :: Int -> Src.Def -> String
fmtDef col d = case d of
    Src.Destruct pat body ->
        let patStr = fmtPattern pat
            bodyStr = fmt (col + step) (A.toValue body)
            oneLine = patStr ++ " = " ++ bodyStr
        in if not ('\n' `elem` bodyStr) && col + length oneLine <= 76
           then oneLine
           else patStr ++ " =\n" ++ ind (col + step) ++ bodyStr
    _ ->
        let name = A.toValue (Src._defName d)
            params = map fmtPattern (Src._defPatterns d)
            paramsStr = if null params then "" else " " ++ unwords params
            bodyStr = fmt (col + step) (A.toValue (Src._defBody d))
            oneLine = name ++ paramsStr ++ " = " ++ bodyStr
        in if not ('\n' `elem` bodyStr) && col + length oneLine <= 76
           then oneLine
           else name ++ paramsStr ++ " =\n" ++ ind (col + step) ++ bodyStr


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

escapeMultilineLit :: String -> String
escapeMultilineLit = go
  where
    go [] = []
    go ('"':'"':'"':rest) = "\\\"\\\"\\\"" ++ go rest
    go ('\\':rest)        = "\\\\" ++ go rest
    go (c:rest)           = c : go rest
