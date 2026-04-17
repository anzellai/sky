-- | Elm-format-style pretty printer for Sky source code.
--
-- Uses absolute column tracking: every function takes `col :: Int`
-- (the current indentation column) and produces strings with
-- newlines at the correct absolute position. The golden rule is
-- "one line or each on its own line" — never mix.
module Sky.Format.Format (formatModule) where

import Data.List (intercalate)
import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A


-- ═══════════════════════════════════════════════════════════
-- Module
-- ═══════════════════════════════════════════════════════════

formatModule :: Src.Module -> String
formatModule m =
    let header = case Src._name m of
            Just (A.At _ segs) ->
                "module " ++ joinDots segs ++ " exposing " ++ fmtExposing (A.toValue (Src._exports m))
            Nothing -> ""
        imports = map fmtImport (Src._imports m)
        aliases = map (fmtAlias . A.toValue) (Src._aliases m)
        unions = map (fmtUnion . A.toValue) (Src._unions m)
        values = map (fmtValue . A.toValue) (Src._values m)
        sections = filter (not . null) [header] ++
                   (if null imports then [] else [intercalate "\n" imports]) ++
                   map (\a -> "\n" ++ a) aliases ++
                   map (\u -> "\n" ++ u) unions ++
                   map (\v -> "\n\n" ++ v) values
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
        body = fmtType (A.toValue (Src._aliasType a))
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

fmtType :: Src.TypeAnnotation -> String
fmtType t = case t of
    Src.TLambda a b -> fmtTypeAtom a ++ " -> " ++ fmtType b
    _ -> fmtTypeAtom t

fmtTypeAtom :: Src.TypeAnnotation -> String
fmtTypeAtom (Src.TVar n) = n
fmtTypeAtom (Src.TType _ segs args) =
    let n = joinDots segs
    in if null args then n else n ++ " " ++ unwords (map fmtTypeParens args)
fmtTypeAtom (Src.TTypeQual m n args) =
    let base = m ++ "." ++ n
    in if null args then base else base ++ " " ++ unwords (map fmtTypeParens args)
fmtTypeAtom Src.TUnit = "()"
fmtTypeAtom (Src.TTuple a b cs) = "( " ++ intercalate ", " (map fmtType (a:b:cs)) ++ " )"
fmtTypeAtom (Src.TRecord fs _) =
    "{ " ++ intercalate ", " (map (\(A.At _ n, ty) -> n ++ " : " ++ fmtType ty) fs) ++ " }"
fmtTypeAtom t@(Src.TLambda _ _) = "(" ++ fmtType t ++ ")"

fmtTypeParens :: Src.TypeAnnotation -> String
fmtTypeParens t@(Src.TType _ _ (_:_)) = "(" ++ fmtTypeAtom t ++ ")"
fmtTypeParens t@(Src.TTypeQual _ _ (_:_)) = "(" ++ fmtTypeAtom t ++ ")"
fmtTypeParens t = fmtTypeAtom t


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
    let parts = map (\(e, A.At _ op) -> (fmt col (A.toValue e), op)) segs
        lastStr = fmt col (A.toValue tail_)
        oneLine = concatMap (\(s, op) -> s ++ " " ++ op ++ " ") parts ++ lastStr
    in if col + length oneLine <= 80
       then oneLine
       else let (firstStr, firstOp) = head parts
                rest = tail parts
                lastOp = snd (last parts)
            in firstStr
               ++ concatMap (\(s, op) -> "\n" ++ ind (col + step) ++ op ++ " " ++ s) rest
               ++ "\n" ++ ind (col + step) ++ lastOp ++ " " ++ lastStr

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

-- Case
fmt col (Src.Case subj branches) =
    let subjStr = fmt col (A.toValue subj)
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
