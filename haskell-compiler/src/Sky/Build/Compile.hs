-- | Single-module compilation pipeline.
-- Source → Parse → (TODO: Canonicalise → Constrain → Solve → Optimise) → Generate Go
module Sky.Build.Compile where

import qualified Data.Text as T
import qualified Data.Text.IO as TIO
import qualified Data.Map.Strict as Map
import System.Directory (createDirectoryIfMissing)

import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Parse.Module as Parse
import qualified Sky.Generate.Go.Ir as GoIr
import qualified Sky.Generate.Go.Builder as GoBuilder
import qualified Sky.Sky.Toml as Toml


-- | Full compilation: parse → codegen → write Go
compile :: Toml.SkyConfig -> FilePath -> FilePath -> IO (Either String FilePath)
compile config entryPath outDir = do
    -- Phase 1: Read source
    source <- TIO.readFile entryPath
    putStrLn $ "-- Lexing " ++ entryPath

    -- Phase 2: Parse
    putStrLn "-- Parsing"
    putStrLn $ "   Source: " ++ show (T.length source) ++ " chars"
    case Parse.parseModule source of
        Left err -> do
            putStrLn $ "   PARSE FAILED: " ++ show err
            return (Left $ "Parse error: " ++ show err)
        Right modul -> do
            let modName = case Src._name modul of
                    Just (A.At _ names) -> concatMap id names
                    Nothing -> "Main"
                declCount = length (Src._values modul) + length (Src._unions modul) + length (Src._aliases modul)
                importCount = length (Src._imports modul)

            putStrLn $ "   Module: " ++ modName
            putStrLn $ "   " ++ show declCount ++ " declarations, " ++ show importCount ++ " imports"

            -- Phase 3: Type Check (TODO — skip for now)
            putStrLn "-- Type Checking (skipped — not yet implemented)"

            -- Phase 4: Generate Go
            putStrLn "-- Generating Go"
            let goCode = generateGo modul config

            -- Phase 5: Write output
            let mainGoPath = outDir ++ "/main.go"
            writeFile mainGoPath goCode
            putStrLn $ "   Wrote " ++ mainGoPath

            -- Write go.mod if missing
            let goModPath = outDir ++ "/go.mod"
            writeFile goModPath $ unlines
                [ "module sky-app"
                , ""
                , "go 1.21"
                ]

            putStrLn "Compilation successful"
            return (Right mainGoPath)


-- | Generate Go source from a parsed module.
-- For now, does a simple direct translation without type checking.
-- Each top-level value becomes a Go function.
generateGo :: Src.Module -> Toml.SkyConfig -> String
generateGo modul config =
    let
        -- Collect imports needed
        imports = collectGoImports modul

        -- Generate declarations for each value
        decls = concatMap generateValueDecl (Src._values modul)

        -- Generate main function
        mainDecl = generateMainFunc modul

        pkg = GoIr.GoPackage
            { GoIr._pkg_name = "main"
            , GoIr._pkg_imports = imports
            , GoIr._pkg_decls = decls ++ mainDecl
            }
    in GoBuilder.renderPackage pkg


-- | Determine Go imports from Sky imports
collectGoImports :: Src.Module -> [GoIr.GoImport]
collectGoImports modul =
    [ GoIr.GoImport "fmt" Nothing
    ]


-- | Generate Go code for a top-level value declaration
generateValueDecl :: A.Located Src.Value -> [GoIr.GoDecl]
generateValueDecl (A.At _ val) =
    let name = case Src._valueName val of
            A.At _ n -> n
    in
        -- Skip "main" — handled separately
        if name == "main"
            then []
            else
                [ GoIr.GoDeclFunc GoIr.GoFuncDecl
                    { GoIr._gf_name = name
                    , GoIr._gf_typeParams = []
                    , GoIr._gf_params = map patternToParam (Src._valuePatterns val)
                    , GoIr._gf_returnType = "any"
                    , GoIr._gf_body = [GoIr.GoReturn (exprToGo (Src._valueBody val))]
                    }
                ]


-- | Convert a pattern to a Go parameter
patternToParam :: Src.Pattern -> GoIr.GoParam
patternToParam (A.At _ pat) = case pat of
    Src.PVar name -> GoIr.GoParam name "any"
    _             -> GoIr.GoParam "_" "any"


-- | Convert a Sky expression to a Go expression (simple direct translation)
exprToGo :: Src.Expr -> GoIr.GoExpr
exprToGo (A.At _ expr) = case expr of
    Src.Str s ->
        GoIr.GoStringLit s

    Src.MultilineStr s ->
        GoIr.GoStringLit s  -- TODO: handle interpolation

    Src.Int n ->
        GoIr.GoIntLit n

    Src.Float f ->
        GoIr.GoFloatLit f

    Src.Chr c ->
        GoIr.GoRuneLit c

    Src.Var name ->
        GoIr.GoIdent (goName name)

    Src.VarQual modName funcName ->
        GoIr.GoIdent (goQualName modName funcName)

    Src.Call func args ->
        -- Direct function call
        let goFunc = exprToGo func
            goArgs = map exprToGo args
        in case goFunc of
            GoIr.GoIdent "fmt_Println" ->
                GoIr.GoCall (GoIr.GoQualified "fmt" "Println") goArgs
            GoIr.GoIdent "String_fromInt" ->
                GoIr.GoCall (GoIr.GoQualified "fmt" "Sprint") goArgs
            GoIr.GoIdent "String_fromFloat" ->
                GoIr.GoCall (GoIr.GoQualified "fmt" "Sprint") goArgs
            _ ->
                GoIr.GoCall goFunc goArgs

    Src.If branches elseExpr ->
        -- If-then-else chain → Go if/else IIFE
        let goElse = exprToGo elseExpr
            goIfs = map (\(cond, body) -> (exprToGo cond, exprToGo body)) branches
        in case goIfs of
            [(cond, body)] ->
                GoIr.GoBlock
                    [GoIr.GoIf cond [GoIr.GoReturn body] [GoIr.GoReturn goElse]]
                    (GoIr.GoRaw "")  -- unreachable, returns above
            _ ->
                GoIr.GoRaw "/* multi-branch if TODO */"

    Src.Let defs body ->
        -- Let-in becomes Go IIFE: func() T { stmts; return body }()
        GoIr.GoBlock
            (concatMap defToStmts defs)
            (exprToGo body)

    Src.Case subject branches ->
        -- Case-of → Go switch IIFE
        let goSubject = exprToGo subject
            goBranches = map (\(pat, body) -> (patternToGoExpr pat, [GoIr.GoReturn (exprToGo body)])) branches
        in GoIr.GoBlock
            []
            (GoIr.GoRaw "/* case TODO */")

    Src.Lambda params body ->
        GoIr.GoFuncLit
            (map patternToParam params)
            "any"
            [GoIr.GoReturn (exprToGo body)]

    Src.List items ->
        GoIr.GoSliceLit "any" (map exprToGo items)

    Src.Tuple a b rest ->
        GoIr.GoRaw "/* tuple TODO */"

    Src.Record fields ->
        GoIr.GoRaw "/* record TODO */"

    Src.Unit ->
        GoIr.GoRaw "struct{}{}"

    Src.Negate inner ->
        GoIr.GoUnary "-" (exprToGo inner)

    Src.Binops pairs final ->
        -- Simple binary chain
        foldl (\acc (e, A.At _ op) -> GoIr.GoBinary (goOp op) acc (exprToGo e))
            (exprToGo final)
            (reverse pairs)

    Src.Access target (A.At _ field) ->
        GoIr.GoSelector (exprToGo target) field

    Src.Accessor field ->
        GoIr.GoRaw ("/* .accessor " ++ field ++ " */")

    Src.Update (A.At _ name) fields ->
        GoIr.GoRaw "/* record update TODO */"

    Src.Op op ->
        GoIr.GoIdent op


-- | Convert a let binding to Go statements
defToStmts :: A.Located Src.Def -> [GoIr.GoStmt]
defToStmts (A.At _ def) =
    let name = case Src._defName def of A.At _ n -> n
    in [GoIr.GoShortDecl name (exprToGo (Src._defBody def))]


-- | Generate the main() function
generateMainFunc :: Src.Module -> [GoIr.GoDecl]
generateMainFunc modul =
    case findMain modul of
        Nothing ->
            [ GoIr.GoDeclFunc GoIr.GoFuncDecl
                { GoIr._gf_name = "main"
                , GoIr._gf_typeParams = []
                , GoIr._gf_params = []
                , GoIr._gf_returnType = ""
                , GoIr._gf_body = [GoIr.GoExprStmt (GoIr.GoCall (GoIr.GoQualified "fmt" "Println") [GoIr.GoStringLit "No main function"])]
                }
            ]
        Just val ->
            [ GoIr.GoDeclFunc GoIr.GoFuncDecl
                { GoIr._gf_name = "main"
                , GoIr._gf_typeParams = []
                , GoIr._gf_params = []
                , GoIr._gf_returnType = ""
                , GoIr._gf_body = exprToMainStmts (Src._valueBody val)
                }
            ]


-- | Find the main function in a module
findMain :: Src.Module -> Maybe Src.Value
findMain modul =
    case filter isMain (Src._values modul) of
        (A.At _ val : _) -> Just val
        _ -> Nothing
  where
    isMain (A.At _ val) = case Src._valueName val of
        A.At _ "main" -> True
        _ -> False


-- NAME MAPPING

-- | Convert the main body to Go statements (not a return value)
exprToMainStmts :: Src.Expr -> [GoIr.GoStmt]
exprToMainStmts (A.At _ expr) = case expr of
    Src.Let defs body ->
        concatMap defToStmts defs ++ exprToMainStmts body
    Src.Call _ _ ->
        [GoIr.GoExprStmt (exprToGo (A.At A.one expr))]
    _ ->
        [GoIr.GoExprStmt (exprToGo (A.At A.one expr))]


-- | Convert a pattern to a Go expression (for switch case values)
patternToGoExpr :: Src.Pattern -> GoIr.GoExpr
patternToGoExpr (A.At _ pat) = case pat of
    Src.PInt n     -> GoIr.GoIntLit n
    Src.PStr s     -> GoIr.GoStringLit s
    Src.PBool b    -> GoIr.GoBoolLit b
    Src.PVar name  -> GoIr.GoIdent name
    Src.PAnything  -> GoIr.GoIdent "_"
    _              -> GoIr.GoRaw "/* pattern */"


-- | Map a Sky variable name to Go
goName :: String -> String
goName "println" = "fmt_Println"
goName name = name


-- | Map a qualified Sky name to Go
goQualName :: String -> String -> String
goQualName "Std.Log" "println"     = "fmt_Println"
goQualName "String"  "fromInt"     = "String_fromInt"
goQualName "String"  "fromFloat"   = "String_fromFloat"
goQualName "String"  "length"      = "String_length"
goQualName "String"  "toUpper"     = "String_toUpper"
goQualName "String"  "toLower"     = "String_toLower"
goQualName "String"  "join"        = "String_join"
goQualName "String"  "split"       = "String_split"
goQualName "List"    "map"         = "List_map"
goQualName "List"    "filter"      = "List_filter"
goQualName "List"    "foldl"       = "List_foldl"
goQualName "List"    "length"      = "List_length"
goQualName "List"    "append"      = "List_append"
goQualName "List"    "reverse"     = "List_reverse"
goQualName "Maybe"   "withDefault" = "Maybe_withDefault"
goQualName "Result"  "withDefault" = "Result_withDefault"
goQualName "Task"    "succeed"     = "Task_succeed"
goQualName "Task"    "fail"        = "Task_fail"
goQualName "Task"    "andThen"     = "Task_andThen"
goQualName "Task"    "perform"     = "Task_perform"
goQualName "Task"    "lazy"        = "Task_lazy"
goQualName "Cmd"     "none"        = "Cmd_none"
goQualName "Cmd"     "batch"       = "Cmd_batch"
goQualName "Cmd"     "perform"     = "Cmd_perform"
goQualName modName funcName = modName ++ "_" ++ funcName


-- | Map a Sky operator to Go
goOp :: String -> String
goOp "++" = "+"  -- string concat in Go
goOp "/=" = "!=" -- not equal
goOp "|>" = "|>" -- needs special handling (pipe)
goOp "<|" = "<|" -- needs special handling (reverse pipe)
goOp op   = op
