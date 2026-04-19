-- | Renders Go IR to Go source code.
-- Produces well-formatted, idiomatic Go output.
module Sky.Generate.Go.Builder where

import Sky.Generate.Go.Ir


-- ═══════════════════════════════════════════════════════════
-- PACKAGE
-- ═══════════════════════════════════════════════════════════

renderPackage :: GoPackage -> String
renderPackage pkg =
    unlines $
        [ "package " ++ _pkg_name pkg
        , ""
        ]
        ++ renderImports (_pkg_imports pkg)
        ++ [""]
        ++ concatMap renderDecl (_pkg_decls pkg)


renderImports :: [GoImport] -> [String]
renderImports [] = []
renderImports [imp] = ["import " ++ renderImport imp]
renderImports imps =
    ["import ("]
    ++ map (\i -> "\t" ++ renderImport i) imps
    ++ [")"]


renderImport :: GoImport -> String
renderImport (GoImport path Nothing) = "\"" ++ path ++ "\""
renderImport (GoImport path (Just alias)) = alias ++ " \"" ++ path ++ "\""


-- ═══════════════════════════════════════════════════════════
-- DECLARATIONS
-- ═══════════════════════════════════════════════════════════

renderDecl :: GoDecl -> [String]
renderDecl decl = case decl of
    GoDeclFunc func ->
        renderFuncDecl func ++ [""]

    GoDeclVar name typ mExpr ->
        case mExpr of
            Just expr -> ["var " ++ name ++ " " ++ typ ++ " = " ++ renderExpr expr, ""]
            Nothing   -> ["var " ++ name ++ " " ++ typ, ""]

    GoDeclConst name typ expr ->
        ["const " ++ name ++ " " ++ typ ++ " = " ++ renderExpr expr, ""]

    GoDeclType name def ->
        renderTypeDef name def ++ [""]

    GoDeclInterface name methods ->
        ["type " ++ name ++ " interface {"]
        ++ map renderInterfaceMethod methods
        ++ ["}", ""]

    GoDeclMethod recv recvType func ->
        renderMethodDecl recv recvType func ++ [""]

    GoDeclRaw code ->
        [code, ""]


renderFuncDecl :: GoFuncDecl -> [String]
renderFuncDecl func =
    let typeParams = renderTypeParams (_gf_typeParams func)
        params = renderParams (_gf_params func)
        retType = if null (_gf_returnType func) then "" else " " ++ _gf_returnType func
        header = "func " ++ _gf_name func ++ typeParams ++ "(" ++ params ++ ")" ++ retType ++ " {"
        body = concatMap (map ("\t" ++) . renderStmt) (_gf_body func)
    in header : body ++ ["}"]


renderMethodDecl :: String -> String -> GoFuncDecl -> [String]
renderMethodDecl recv recvType func =
    let typeParams = renderTypeParams (_gf_typeParams func)
        params = renderParams (_gf_params func)
        retType = if null (_gf_returnType func) then "" else " " ++ _gf_returnType func
        header = "func (" ++ recv ++ " " ++ recvType ++ ") " ++ _gf_name func ++ typeParams ++ "(" ++ params ++ ")" ++ retType ++ " {"
        body = concatMap (map ("\t" ++) . renderStmt) (_gf_body func)
    in header : body ++ ["}"]


renderTypeParams :: [(String, String)] -> String
renderTypeParams [] = ""
renderTypeParams params =
    "[" ++ intercalate ", " (map (\(n, c) -> n ++ " " ++ c) params) ++ "]"


renderParams :: [GoParam] -> String
renderParams = intercalate ", " . map renderParam


renderParam :: GoParam -> String
renderParam (GoParam name typ) = name ++ " " ++ typ


renderTypeDef :: String -> GoTypeDef -> [String]
renderTypeDef name def = case def of
    GoStructDef fields ->
        ["type " ++ name ++ " struct {"]
        ++ map (\(fn, ft) -> "\t" ++ fn ++ " " ++ ft) fields
        ++ ["}"]

    GoAliasDef target ->
        ["type " ++ name ++ " = " ++ target]

    GoEnumDef values ->
        -- `type X = int` (alias, not a distinct type) so values
        -- flowing through `any` and then type-asserted to X succeed
        -- — the runtime boxes plain `int` for all integer ADT tags,
        -- and Go requires the runtime and static types to match
        -- exactly for `.(T)` unless T is an interface. Alias makes
        -- int and X the same static type.
        ["type " ++ name ++ " = int", "", "const ("]
        ++ zipWith (\v i -> if i == 0 then "\t" ++ v ++ " " ++ name ++ " = iota" else "\t" ++ v)
            values [0::Int ..]
        ++ [")"]


renderInterfaceMethod :: (String, [GoParam], String) -> String
renderInterfaceMethod (name, params, ret) =
    "\t" ++ name ++ "(" ++ renderParams params ++ ") " ++ ret


-- ═══════════════════════════════════════════════════════════
-- STATEMENTS
-- ═══════════════════════════════════════════════════════════

renderStmt :: GoStmt -> [String]
renderStmt stmt = case stmt of
    GoExprStmt expr ->
        [renderExpr expr]

    GoAssign name expr ->
        [name ++ " = " ++ renderExpr expr]

    GoShortDecl name expr ->
        [name ++ " := " ++ renderExpr expr]

    GoVarDecl name typ mExpr ->
        case mExpr of
            Just expr -> ["var " ++ name ++ " " ++ typ ++ " = " ++ renderExpr expr]
            Nothing   -> ["var " ++ name ++ " " ++ typ]

    GoReturn expr ->
        ["return " ++ renderExpr expr]

    GoReturnVoid ->
        ["return"]

    GoIf cond thenStmts elseStmts ->
        ["if " ++ renderExpr cond ++ " {"]
        ++ concatMap (map ("\t" ++) . renderStmt) thenStmts
        ++ if null elseStmts then ["}"]
           else ["} else {"]
                ++ concatMap (map ("\t" ++) . renderStmt) elseStmts
                ++ ["}"]

    GoSwitch expr cases ->
        ["switch " ++ renderExpr expr ++ " {"]
        ++ concatMap renderSwitchCase cases
        ++ ["}"]

    GoTypeSwitch name expr cases ->
        ["switch " ++ name ++ " := " ++ renderExpr expr ++ ".(type) {"]
        ++ concatMap renderTypeSwitchCase cases
        ++ ["}"]

    GoFor name expr body ->
        ["for _, " ++ name ++ " := range " ++ renderExpr expr ++ " {"]
        ++ concatMap (map ("\t" ++) . renderStmt) body
        ++ ["}"]

    GoBlock_ stmts ->
        ["{"]
        ++ concatMap (map ("\t" ++) . renderStmt) stmts
        ++ ["}"]

    GoComment text ->
        ["// " ++ text]

    GoBlank ->
        [""]


renderSwitchCase :: (GoExpr, [GoStmt]) -> [String]
renderSwitchCase (val, stmts) =
    ["case " ++ renderExpr val ++ ":"]
    ++ concatMap (map ("\t" ++) . renderStmt) stmts


renderTypeSwitchCase :: (String, [GoStmt]) -> [String]
renderTypeSwitchCase (typ, stmts) =
    ["case " ++ typ ++ ":"]
    ++ concatMap (map ("\t" ++) . renderStmt) stmts


-- ═══════════════════════════════════════════════════════════
-- EXPRESSIONS
-- ═══════════════════════════════════════════════════════════

renderExpr :: GoExpr -> String
renderExpr expr = case expr of
    GoIdent name -> name
    GoQualified pkg name -> pkg ++ "." ++ name
    GoIntLit n -> show n
    GoFloatLit f -> show f
    GoStringLit s -> "\"" ++ escapeGo s ++ "\""
    GoRuneLit s -> "'" ++ s ++ "'"
    GoBoolLit True -> "true"
    GoBoolLit False -> "false"
    GoNil -> "nil"

    GoCall func args ->
        renderExpr func ++ "(" ++ intercalate ", " (map renderExpr args) ++ ")"

    GoGenericCall name typeArgs args ->
        name ++ "[" ++ intercalate ", " typeArgs ++ "](" ++ intercalate ", " (map renderExpr args) ++ ")"

    GoSelector expr field ->
        renderExpr expr ++ "." ++ field

    GoIndex expr idx ->
        renderExpr expr ++ "[" ++ renderExpr idx ++ "]"

    GoSliceLit typ elems ->
        "[]" ++ typ ++ "{" ++ intercalate ", " (map renderExpr elems) ++ "}"

    GoMapLit keyType valType entries ->
        "map[" ++ keyType ++ "]" ++ valType ++ "{" ++ intercalate ", " (map renderMapEntry entries) ++ "}"

    GoStructLit typ fields ->
        typ ++ "{" ++ intercalate ", " (map renderStructField fields) ++ "}"

    GoFuncLit params retType body ->
        "func(" ++ renderParams params ++ ") " ++ retType ++ " { "
        ++ concatMap (\s -> head (renderStmt s) ++ "; ") body
        ++ "}"

    GoBinary op left right ->
        renderExpr left ++ " " ++ op ++ " " ++ renderExpr right

    GoUnary op operand ->
        op ++ renderExpr operand

    GoTypeAssert expr typ ->
        renderExpr expr ++ ".(" ++ typ ++ ")"

    GoBlock stmts result ->
        "func() any { "
        ++ concatMap (\s -> unlines' (renderStmt s) ++ "; ") stmts
        ++ "return " ++ renderExpr result ++ " }()"

    GoRaw code -> code


renderMapEntry :: (GoExpr, GoExpr) -> String
renderMapEntry (k, v) = renderExpr k ++ ": " ++ renderExpr v

renderStructField :: (String, GoExpr) -> String
renderStructField (name, val) = name ++ ": " ++ renderExpr val


-- HELPERS

unlines' :: [String] -> String
unlines' [] = ""
unlines' [x] = x
unlines' (x:xs) = x ++ "; " ++ unlines' xs


-- | Escape a Haskell String for emission as a Go double-quoted string literal.
-- Go strings are UTF-8; printable Unicode characters can be emitted as-is.
-- Control characters and bytes that would terminate the literal are escaped.
-- Non-ASCII characters are emitted as their UTF-8 bytes (Go's default).
escapeGo :: String -> String
escapeGo = concatMap escapeChar
  where
    escapeChar '"'  = "\\\""
    escapeChar '\\' = "\\\\"
    escapeChar '\n' = "\\n"
    escapeChar '\t' = "\\t"
    escapeChar '\r' = "\\r"
    escapeChar '\0' = "\\x00"
    escapeChar c
        | fromEnum c < 0x20 =
            -- Other C0 control characters: hex-escape
            let n = fromEnum c
                h hi = "0123456789abcdef" !! hi
            in ['\\', 'x', h (n `div` 16), h (n `mod` 16)]
        | fromEnum c > 0x10FFFF = [c]  -- invalid, leave as-is
        | otherwise = [c]  -- printable ASCII or Unicode; Go handles UTF-8 natively


intercalate :: String -> [String] -> String
intercalate _ [] = ""
intercalate _ [x] = x
intercalate sep (x:xs) = x ++ sep ++ intercalate sep xs
