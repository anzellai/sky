-- | Minimal elm-format-compatible pretty printer for Sky.
-- Takes a parsed Src.Module and emits canonical formatted source.
module Sky.Format.Format (formatModule) where

import Data.List (intercalate)
import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A


-- | Escape a string literal's contents for re-emission. The lexer
-- stores string values unescaped (interprets \n, \t, \", \\, etc.
-- into their literal chars); to round-trip we must put the escapes
-- back or the next parse will misread the content as code. Matches
-- the escape set accepted by Sky.Parse.Lexer.
escapeStringLit :: String -> String
escapeStringLit = concatMap esc
  where
    esc '\\' = "\\\\"
    esc '"'  = "\\\""
    esc '\n' = "\\n"
    esc '\t' = "\\t"
    esc '\r' = "\\r"
    esc c    = [c]


-- | Triple-quoted strings: escape embedded `"""` runs but leave
-- single / double quotes intact (multiline strings don't need them
-- escaped — the terminator is `"""`). Escape backslashes so the
-- interpolation token `{{expr}}` is preserved verbatim and the raw
-- content round-trips through the lexer.
escapeMultilineLit :: String -> String
escapeMultilineLit = go
  where
    go [] = []
    go ('"':'"':'"':rest) = "\\\"\\\"\\\"" ++ go rest
    go ('\\':rest)        = "\\\\" ++ go rest
    go (c:rest)           = c : go rest


-- | Format a full module
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
fmtExposed (Src.ExposedType n (Src.PublicCtors cs)) =
    n ++ "(" ++ fmtIntercalate ", " cs ++ ")"
fmtExposed (Src.ExposedOperator n) = "(" ++ n ++ ")"

fmtIntercalate :: String -> [String] -> String
fmtIntercalate _   []     = ""
fmtIntercalate _   [x]    = x
fmtIntercalate sep (x:xs) = x ++ sep ++ fmtIntercalate sep xs


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
fmtCtor (n, args) = n ++ " " ++ unwords (map fmtTypeAtom args)


fmtValue :: Src.Value -> String
fmtValue v =
    let name = A.toValue (Src._valueName v)
        annotStr = case Src._valueType v of
            Just (A.At _ t) -> name ++ " : " ++ fmtType t ++ "\n"
            Nothing -> ""
        params = map fmtPattern (Src._valuePatterns v)
        paramsStr = if null params then "" else " " ++ unwords params
        body = fmtExpr 1 (A.toValue (Src._valueBody v))
    in annotStr ++ name ++ paramsStr ++ " =\n    " ++ body


fmtType :: Src.TypeAnnotation -> String
fmtType t = case t of
    Src.TLambda a b -> fmtTypeAtom a ++ " -> " ++ fmtType b
    _ -> fmtTypeAtom t


fmtTypeAtom :: Src.TypeAnnotation -> String
fmtTypeAtom (Src.TVar n) = n
fmtTypeAtom (Src.TType _ segs args) =
    let n = joinDots segs
    in if null args then n
       else n ++ " " ++ unwords (map fmtTypeAtom args)
fmtTypeAtom (Src.TTypeQual m n args) =
    let base = m ++ "." ++ n
    in if null args then base
       else base ++ " " ++ unwords (map fmtTypeAtom args)
fmtTypeAtom Src.TUnit = "()"
fmtTypeAtom (Src.TTuple a b cs) =
    "(" ++ intercalate ", " (map fmtType (a:b:cs)) ++ ")"
fmtTypeAtom (Src.TRecord fs _) =
    "{ " ++ intercalate ", " (map (\(A.At _ n, ty) -> n ++ " : " ++ fmtType ty) fs) ++ " }"
fmtTypeAtom t@(Src.TLambda _ _) = "(" ++ fmtType t ++ ")"


-- Top-level pattern (no outer parens needed for constructor application)
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
    Src.PTuple a b cs -> "(" ++ intercalate ", " (map fmtPattern (a:b:cs)) ++ ")"
    Src.PRecord ns -> "{ " ++ intercalate ", " (map A.toValue ns) ++ " }"
    Src.PAlias inner (A.At _ n) -> fmtPattern inner ++ " as " ++ n
    Src.PChr s -> "'" ++ s ++ "'"


-- Atomic pattern (wrap in parens if it could contain whitespace-separated parts)
fmtPatternAtom :: Src.Pattern -> String
fmtPatternAtom p@(A.At _ p_) = case p_ of
    Src.PCtor _ _ (_:_)     -> "(" ++ fmtPattern p ++ ")"
    Src.PCtorQual _ _ (_:_) -> "(" ++ fmtPattern p ++ ")"
    Src.PCons _ _           -> "(" ++ fmtPattern p ++ ")"
    Src.PAlias _ _          -> "(" ++ fmtPattern p ++ ")"
    _                        -> fmtPattern p


fmtExpr :: Int -> Src.Expr_ -> String
fmtExpr _ (Src.Int n) = show n
fmtExpr _ (Src.Float f) = show f
fmtExpr _ (Src.Chr s) = "'" ++ s ++ "'"
fmtExpr _ (Src.Str s) = "\"" ++ escapeStringLit s ++ "\""
fmtExpr _ (Src.MultilineStr s) = "\"\"\"" ++ escapeMultilineLit s ++ "\"\"\""
fmtExpr _ (Src.Var n) = n
fmtExpr _ (Src.VarQual m n) = m ++ "." ++ n
fmtExpr _ Src.Unit = "()"
fmtExpr _ (Src.Op o) = "(" ++ o ++ ")"
fmtExpr lvl (Src.Negate e) = "-" ++ fmtExpr lvl (A.toValue e)
fmtExpr lvl (Src.List xs) =
    "[" ++ intercalate ", " (map (fmtExpr lvl . A.toValue) xs) ++ "]"
fmtExpr lvl (Src.Tuple a b cs) =
    "(" ++ intercalate ", " (map (fmtExpr lvl . A.toValue) (a:b:cs)) ++ ")"
fmtExpr _ (Src.Accessor f) = "." ++ f
fmtExpr lvl (Src.Access e (A.At _ f)) = fmtExpr lvl (A.toValue e) ++ "." ++ f
fmtExpr lvl (Src.Call f args) =
    fmtExpr lvl (A.toValue f) ++ " " ++ unwords (map (fmtArg lvl) args)
fmtExpr lvl (Src.Binops segs tail_) =
    concatMap (\(e, A.At _ op) -> fmtExpr lvl (A.toValue e) ++ " " ++ op ++ " ") segs
    ++ fmtExpr lvl (A.toValue tail_)
fmtExpr lvl (Src.Lambda pats body) =
    "\\" ++ unwords (map fmtPattern pats) ++ " -> " ++ fmtExpr lvl (A.toValue body)
fmtExpr lvl (Src.If branches elseE) = fmtIf lvl branches elseE
fmtExpr lvl (Src.Let defs body) =
    let ind = indent lvl
        defsStr = intercalate ("\n" ++ indent (lvl+1)) (map (fmtDef (lvl+1) . A.toValue) defs)
    in "let\n" ++ indent (lvl+1) ++ defsStr ++ "\n" ++ ind ++ "in\n" ++ indent (lvl+1) ++ fmtExpr (lvl+1) (A.toValue body)
fmtExpr lvl (Src.Case subj branches) =
    let ind = indent (lvl+1)
        branchStr (pat, e) = ind ++ fmtPattern pat ++ " ->\n" ++ indent (lvl+2) ++ fmtExpr (lvl+2) (A.toValue e)
    in "case " ++ fmtExpr lvl (A.toValue subj) ++ " of\n" ++
       intercalate "\n\n" (map branchStr branches)
fmtExpr lvl (Src.Record fs) =
    "{ " ++ intercalate ", " (map (\(A.At _ n, e) -> n ++ " = " ++ fmtExpr lvl (A.toValue e)) fs) ++ " }"
fmtExpr lvl (Src.Update (A.At _ n) fs) =
    "{ " ++ n ++ " | " ++ intercalate ", " (map (\(A.At _ fn, e) -> fn ++ " = " ++ fmtExpr lvl (A.toValue e)) fs) ++ " }"


fmtArg :: Int -> Src.Expr -> String
fmtArg lvl e = case A.toValue e of
    Src.Call _ _ -> "(" ++ fmtExpr lvl (A.toValue e) ++ ")"
    Src.Binops _ _ -> "(" ++ fmtExpr lvl (A.toValue e) ++ ")"
    Src.If _ _ -> "(" ++ fmtExpr lvl (A.toValue e) ++ ")"
    Src.Let _ _ -> "(" ++ fmtExpr lvl (A.toValue e) ++ ")"
    Src.Case _ _ -> "(" ++ fmtExpr lvl (A.toValue e) ++ ")"
    Src.Lambda _ _ -> "(" ++ fmtExpr lvl (A.toValue e) ++ ")"
    Src.Negate _ -> "(" ++ fmtExpr lvl (A.toValue e) ++ ")"
    _ -> fmtExpr lvl (A.toValue e)


fmtIf :: Int -> [(Src.Expr, Src.Expr)] -> Src.Expr -> String
fmtIf lvl branches elseE =
    let ind = indent lvl
        fmtBranch (cond, body) =
            "if " ++ fmtExpr lvl (A.toValue cond) ++ " then\n" ++
            indent (lvl+1) ++ fmtExpr (lvl+1) (A.toValue body)
    in intercalate ("\n" ++ ind ++ "else ") (map fmtBranch branches) ++
       "\n" ++ ind ++ "else\n" ++ indent (lvl+1) ++ fmtExpr (lvl+1) (A.toValue elseE)


fmtDef :: Int -> Src.Def -> String
fmtDef lvl d =
    let name = A.toValue (Src._defName d)
        params = map fmtPattern (Src._defPatterns d)
        paramsStr = if null params then "" else " " ++ unwords params
        body = fmtExpr lvl (A.toValue (Src._defBody d))
    in name ++ paramsStr ++ " = " ++ body


indent :: Int -> String
indent n = replicate (n * 4) ' '
