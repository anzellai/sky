-- | Single-module compilation pipeline.
-- Source → Parse → Canonicalise → (TODO: Type Check) → Generate Go
module Sky.Build.Compile where

import qualified Data.Map.Strict as Map
import qualified Data.Text as T
import qualified Data.Text.IO as TIO
import System.Directory (createDirectoryIfMissing, doesDirectoryExist, copyFile)
import System.FilePath (takeDirectory, (</>))

import qualified Sky.AST.Source as Src
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Parse.Module as Parse
import qualified Sky.Canonicalise.Module as Canonicalise
import qualified Sky.Generate.Go.Ir as GoIr
import qualified Sky.Generate.Go.Builder as GoBuilder
import qualified Sky.Generate.Go.Kernel as Kernel
import qualified Sky.Sky.Toml as Toml
import qualified Sky.Type.Constrain.Module as Constrain
import qualified Sky.Type.Solve as Solve


-- | Full compilation: parse → canonicalise → codegen → write Go
compile :: Toml.SkyConfig -> FilePath -> FilePath -> IO (Either String FilePath)
compile config entryPath outDir = do
    -- Phase 1: Read source
    source <- TIO.readFile entryPath
    putStrLn $ "-- Lexing " ++ entryPath

    -- Phase 2: Parse
    putStrLn "-- Parsing"
    case Parse.parseModule source of
        Left err -> do
            putStrLn $ "   PARSE FAILED: " ++ show err
            return (Left $ "Parse error: " ++ show err)
        Right srcMod -> do
            let modName = case Src._name srcMod of
                    Just (A.At _ names) -> concatMap id names
                    Nothing -> "Main"
                declCount = length (Src._values srcMod) + length (Src._unions srcMod) + length (Src._aliases srcMod)
            putStrLn $ "   Module: " ++ modName
            putStrLn $ "   " ++ show declCount ++ " declarations"

            -- Phase 3: Canonicalise
            putStrLn "-- Canonicalising"
            case Canonicalise.canonicalise srcMod of
                Left err -> do
                    putStrLn $ "   CANONICALISE FAILED: " ++ err
                    return (Left $ "Canonicalise error: " ++ err)
                Right canMod -> do
                    putStrLn "   Names resolved"

                    -- Phase 4: Type Check
                    putStrLn "-- Type Checking"
                    let constraints = Constrain.constrainModule canMod
                    solveResult <- Solve.solve constraints
                    let solvedTypes = case solveResult of
                            Solve.SolveOk types -> do
                                putStrLn $ "   Types OK (" ++ show (length (Map.keys types)) ++ " bindings resolved)"
                                return types
                            Solve.SolveError err -> do
                                putStrLn $ "   TYPE WARNING: " ++ err
                                return Map.empty
                    types <- solvedTypes

                    -- Phase 5: Generate Go (using solved types)
                    putStrLn "-- Generating Go"
                    let goCode = generateGo canMod srcMod config

                    -- Phase 6: Write output
                    createDirectoryIfMissing True outDir
                    let mainGoPath = outDir </> "main.go"
                    writeFile mainGoPath goCode
                    putStrLn $ "   Wrote " ++ mainGoPath

                    -- Copy runtime package
                    copyRuntime outDir

                    -- Write go.mod
                    let goModPath = outDir </> "go.mod"
                    writeFile goModPath $ unlines
                        [ "module sky-app"
                        , ""
                        , "go 1.21"
                        ]

                    putStrLn "Compilation successful"
                    return (Right mainGoPath)


-- | Copy the Go runtime package into the output directory
copyRuntime :: FilePath -> IO ()
copyRuntime outDir = do
    let rtDir = outDir </> "rt"
    createDirectoryIfMissing True rtDir
    -- Write rt package inline (for now, until we have a separate runtime-go dir)
    writeFile (rtDir </> "rt.go") runtimeGoSource


-- ═══════════════════════════════════════════════════════════
-- GO CODE GENERATION (from Canonical AST)
-- ═══════════════════════════════════════════════════════════

-- | Generate Go source from a canonical module
generateGo :: Can.Module -> Src.Module -> Toml.SkyConfig -> String
generateGo canMod srcMod config =
    let
        imports = collectGoImports canMod srcMod
        decls = generateDecls canMod
        mainDecl = generateMainFunc canMod srcMod
        pkg = GoIr.GoPackage
            { GoIr._pkg_name = "main"
            , GoIr._pkg_imports = imports
            , GoIr._pkg_decls = decls ++ mainDecl
            }
    in GoBuilder.renderPackage pkg


-- | Collect Go imports needed
collectGoImports :: Can.Module -> Src.Module -> [GoIr.GoImport]
collectGoImports _canMod _srcMod =
    -- Only import rt for now. fmt is used inside rt, not in main.go directly.
    -- TODO: scan generated code for fmt.Println usage and add "fmt" if needed
    [ GoIr.GoImport "sky-app/rt" (Just "rt") ]


-- | Check if module imports Task
isTaskImport :: Src.Import -> Bool
isTaskImport imp =
    let segs = case Src._importName imp of A.At _ s -> s
    in segs == ["Sky", "Core", "Task"]


-- ═══════════════════════════════════════════════════════════
-- DECLARATIONS
-- ═══════════════════════════════════════════════════════════

-- | Generate Go declarations from canonical decls
generateDecls :: Can.Module -> [GoIr.GoDecl]
generateDecls canMod = declsToList (Can._decls canMod) []
  where
    declsToList Can.SaveTheEnvironment acc = acc
    declsToList (Can.Declare def rest) acc =
        declsToList rest (acc ++ generateDef def)
    declsToList (Can.DeclareRec def defs rest) acc =
        declsToList rest (acc ++ generateDef def ++ concatMap generateDef defs)


-- | Generate Go for a single definition
generateDef :: Can.Def -> [GoIr.GoDecl]
generateDef def =
    let (name, params, body) = case def of
            Can.Def (A.At _ n) pats expr -> (n, pats, expr)
            Can.TypedDef (A.At _ n) _ typedPats expr _ ->
                (n, map fst typedPats, expr)
    in
    -- Skip "main" — handled separately
    if name == "main" then []
    else
        [ GoIr.GoDeclFunc GoIr.GoFuncDecl
            { GoIr._gf_name = name
            , GoIr._gf_typeParams = []
            , GoIr._gf_params = map patternToParam params
            , GoIr._gf_returnType = "any"
            , GoIr._gf_body = [GoIr.GoReturn (exprToGo body)]
            }
        ]


-- ═══════════════════════════════════════════════════════════
-- EXPRESSION CODE GENERATION
-- ═══════════════════════════════════════════════════════════

-- | Convert a canonical expression to Go IR
exprToGo :: Can.Expr -> GoIr.GoExpr
exprToGo (A.At _ expr) = case expr of

    Can.Str s ->
        GoIr.GoStringLit s

    Can.Int n ->
        GoIr.GoIntLit n

    Can.Float f ->
        GoIr.GoFloatLit f

    Can.Chr c ->
        GoIr.GoRuneLit c

    Can.Unit ->
        GoIr.GoRaw "struct{}{}"

    Can.VarLocal name ->
        GoIr.GoIdent name

    Can.VarTopLevel home name ->
        GoIr.GoIdent name

    Can.VarKernel modName funcName ->
        kernelToGo modName funcName

    Can.VarCtor opts home typeName ctorName annot ->
        ctorToGo opts home typeName ctorName annot

    Can.List items ->
        GoIr.GoSliceLit "any" (map exprToGo items)

    Can.Negate inner ->
        GoIr.GoCall (GoIr.GoQualified "rt" "Negate") [exprToGo inner]

    Can.Binop op opHome opName _annot left right ->
        binopToGo op left right

    Can.Lambda params body ->
        -- Generate curried function: \a b -> body becomes func(a any) any { return func(b any) any { return body } }
        curryLambda (map patternToParam params) (exprToGo body)

    Can.Call func args ->
        let goFunc = exprToGo func
            goArgs = map exprToGo args
        in GoIr.GoCall goFunc goArgs

    Can.If branches elseExpr ->
        ifToGo branches elseExpr

    Can.Let def body ->
        letToGo def body

    Can.LetRec defs body ->
        let stmts = concatMap defToStmts defs
        in GoIr.GoBlock stmts (exprToGo body)

    Can.LetDestruct pat valExpr body ->
        GoIr.GoBlock
            [GoIr.GoShortDecl (patternName pat) (exprToGo valExpr)]
            (exprToGo body)

    Can.Case subject branches ->
        caseToGo subject branches

    Can.Accessor field ->
        -- Record accessor function: .field → func(r any) any { return r.(map[string]any)["field"] }
        GoIr.GoFuncLit [GoIr.GoParam "__r" "any"] "any"
            [GoIr.GoReturn (GoIr.GoRaw ("__r.(map[string]any)[\"" ++ field ++ "\"]"))]

    Can.Access target (A.At _ field) ->
        -- Record field access: use runtime helper that handles both map and any types
        GoIr.GoCall (GoIr.GoQualified "rt" "RecordGet") [exprToGo target, GoIr.GoStringLit field]

    Can.Update _name baseExpr fields ->
        -- Record update: copy base record, override fields
        let baseGo = exprToGo baseExpr
            fieldUpdates = Map.toList fields
            updateCalls = map (\(name, Can.FieldUpdate _ expr) ->
                GoIr.GoRaw ("\"" ++ name ++ "\": " ++ GoBuilder.renderExpr (exprToGo expr)))
                fieldUpdates
        in GoIr.GoCall (GoIr.GoQualified "rt" "RecordUpdate")
            [baseGo, GoIr.GoRaw ("map[string]any{" ++ intercalate_ ", " (map (\(n, Can.FieldUpdate _ e) -> "\"" ++ n ++ "\": " ++ GoBuilder.renderExpr (exprToGo e)) fieldUpdates) ++ "}")]

    Can.Record fields ->
        -- Record literal: { name = "Alice", age = 30 }
        let entries = Map.toList fields
            goEntries = map (\(name, expr) ->
                (GoIr.GoStringLit name, exprToGo expr)) entries
        in GoIr.GoMapLit "string" "any" goEntries

    Can.Tuple a b mC ->
        case mC of
            Nothing -> GoIr.GoStructLit "rt.SkyTuple2" [("V0", exprToGo a), ("V1", exprToGo b)]
            Just c -> GoIr.GoStructLit "rt.SkyTuple3" [("V0", exprToGo a), ("V1", exprToGo b), ("V2", exprToGo c)]


-- ═══════════════════════════════════════════════════════════
-- KERNEL FUNCTION RESOLUTION
-- ═══════════════════════════════════════════════════════════

-- | Map a kernel function to its Go equivalent
-- For generic functions, append [any, ...] type params until type checker provides real types
kernelToGo :: String -> String -> GoIr.GoExpr
kernelToGo modName funcName =
    case Kernel.lookup modName funcName of
        Just ki ->
            if Kernel._ki_typed ki
            then GoIr.GoIdent (Kernel._ki_goName ki ++ genericParams modName funcName)
            else GoIr.GoIdent (Kernel._ki_goName ki)
        Nothing ->
            case (modName, funcName) of
                ("Log", "println") -> GoIr.GoQualified "rt" "Log_println"
                ("Basics", "add")  -> GoIr.GoIdent "+"
                ("Basics", "sub")  -> GoIr.GoIdent "-"
                ("Basics", "not")  -> GoIr.GoQualified "rt" "Basics_not"
                _ -> GoIr.GoQualified "rt" (modName ++ "_" ++ funcName)


-- | Get generic type parameters for a kernel function.
-- Until the type checker provides real types, use any-typed wrappers for Task functions
-- and [any, ...] type params for other generics.
genericParams :: String -> String -> String
genericParams modName funcName = case (modName, funcName) of
    -- Task functions use any-typed wrappers (don't need generic params)
    ("Task", _)  -> ""
    -- Other generic functions
    ("Result", "map")    -> "[any, any, any]"
    ("Result", "andThen") -> "[any, any, any]"
    ("Result", "withDefault") -> "[any, any]"
    ("Maybe", "map")     -> "[any, any]"
    ("Maybe", "andThen") -> "[any, any]"
    ("Maybe", "withDefault") -> "[any]"
    ("List", "map")      -> "[any, any]"
    ("List", "filter")   -> "[any]"
    ("List", "foldl")    -> "[any, any]"
    _                    -> ""


-- | Map a constructor to Go
-- Without type info, use [any, any] as type params for generic constructors
ctorToGo :: Can.CtorOpts -> ModuleName.Canonical -> String -> String -> Can.Annotation -> GoIr.GoExpr
ctorToGo opts home typeName ctorName _annot = case ctorName of
    "Ok"      -> GoIr.GoIdent "rt.Ok[any, any]"
    "Err"     -> GoIr.GoIdent "rt.Err[any, any]"
    "Just"    -> GoIr.GoIdent "rt.Just[any]"
    "Nothing" -> GoIr.GoCall (GoIr.GoIdent "rt.Nothing[any]") []
    "True"    -> GoIr.GoBoolLit True
    "False"   -> GoIr.GoBoolLit False
    _         -> GoIr.GoQualified "rt" ctorName


-- ═══════════════════════════════════════════════════════════
-- BINARY OPERATORS
-- ═══════════════════════════════════════════════════════════

-- | Convert a binary operator application to Go
binopToGo :: String -> Can.Expr -> Can.Expr -> GoIr.GoExpr
binopToGo op left right = case op of
    -- Pipe operators — desugar to function application
    -- a |> f becomes f(a), but if f is already a call f(x), becomes f(x, a)
    "|>" -> pipeApply left right
    "<|" -> pipeApply right left

    -- Composition operators (>> and <<)
    ">>" -> GoIr.GoCall (GoIr.GoQualified "rt" "ComposeL") [exprToGo left, exprToGo right]
    "<<" -> GoIr.GoCall (GoIr.GoQualified "rt" "ComposeR") [exprToGo left, exprToGo right]

    -- String/list concat — use runtime helper until type checker provides types
    "++" -> GoIr.GoCall (GoIr.GoQualified "rt" "Concat") [exprToGo left, exprToGo right]

    -- Cons operator
    "::" -> GoIr.GoCall (GoIr.GoQualified "rt" "List_cons") [exprToGo left, exprToGo right]

    -- Not-equal
    "/=" -> GoIr.GoBinary "!=" (exprToGo left) (exprToGo right)

    -- Arithmetic operators — use runtime helpers for any-typed values
    "+"  -> GoIr.GoCall (GoIr.GoQualified "rt" "Add") [exprToGo left, exprToGo right]
    "-"  -> GoIr.GoCall (GoIr.GoQualified "rt" "Sub") [exprToGo left, exprToGo right]
    "*"  -> GoIr.GoCall (GoIr.GoQualified "rt" "Mul") [exprToGo left, exprToGo right]
    "/"  -> GoIr.GoCall (GoIr.GoQualified "rt" "Div") [exprToGo left, exprToGo right]

    -- Comparison operators
    "==" -> GoIr.GoCall (GoIr.GoQualified "rt" "Eq") [exprToGo left, exprToGo right]
    ">"  -> GoIr.GoCall (GoIr.GoQualified "rt" "Gt") [exprToGo left, exprToGo right]
    "<"  -> GoIr.GoCall (GoIr.GoQualified "rt" "Lt") [exprToGo left, exprToGo right]
    ">=" -> GoIr.GoCall (GoIr.GoQualified "rt" "Gte") [exprToGo left, exprToGo right]
    "<=" -> GoIr.GoCall (GoIr.GoQualified "rt" "Lte") [exprToGo left, exprToGo right]

    -- Logic
    "&&" -> GoIr.GoCall (GoIr.GoQualified "rt" "And") [exprToGo left, exprToGo right]
    "||" -> GoIr.GoCall (GoIr.GoQualified "rt" "Or") [exprToGo left, exprToGo right]

    -- Other operators
    _ -> GoIr.GoBinary op (exprToGo left) (exprToGo right)


-- | Apply a pipe: `value |> func` becomes `func(value)`
-- If func is already a call `f(args...)`, append value as additional arg: `f(args..., value)`
pipeApply :: Can.Expr -> Can.Expr -> GoIr.GoExpr
pipeApply valueExpr funcExpr =
    let goValue = exprToGo valueExpr
    in case funcExpr of
        -- If the RHS is a function call with args: f(a) |> g(b) → g(b, f(a))
        A.At _ (Can.Call innerFunc innerArgs) ->
            GoIr.GoCall (exprToGo innerFunc) (map exprToGo innerArgs ++ [goValue])
        -- Otherwise: a |> f → f(a)
        _ ->
            GoIr.GoCall (exprToGo funcExpr) [goValue]


-- ═══════════════════════════════════════════════════════════
-- IF-THEN-ELSE
-- ═══════════════════════════════════════════════════════════

-- | Convert if-then-else to Go (IIFE with if-else chain)
ifToGo :: [(Can.Expr, Can.Expr)] -> Can.Expr -> GoIr.GoExpr
ifToGo branches elseExpr =
    let
        buildIf [] = [GoIr.GoReturn (exprToGo elseExpr)]
        buildIf ((cond, body):rest) =
            [GoIr.GoIf (toBoolExpr (exprToGo cond)) [GoIr.GoReturn (exprToGo body)] (buildIf rest)]
    in
    GoIr.GoBlock (buildIf branches) (GoIr.GoRaw "nil")


-- | Ensure an expression is a Go bool (cast from any if needed)
toBoolExpr :: GoIr.GoExpr -> GoIr.GoExpr
toBoolExpr expr = case expr of
    GoIr.GoBoolLit _ -> expr  -- already bool
    GoIr.GoCall (GoIr.GoQualified "rt" name) _
        | name `elem` ["Eq", "Gt", "Lt", "Gte", "Lte", "And", "Or"] ->
            GoIr.GoCall (GoIr.GoQualified "rt" "AsBool") [expr]
    _ -> GoIr.GoCall (GoIr.GoQualified "rt" "AsBool") [expr]


-- ═══════════════════════════════════════════════════════════
-- LET-IN
-- ═══════════════════════════════════════════════════════════

-- | Convert let-in to Go (IIFE with local declarations)
letToGo :: Can.Def -> Can.Expr -> GoIr.GoExpr
letToGo def body =
    GoIr.GoBlock (defToStmts def) (exprToGo body)


-- | Convert a definition to Go statements
defToStmts :: Can.Def -> [GoIr.GoStmt]
defToStmts def = case def of
    Can.Def (A.At _ name) [] body ->
        -- Simple binding: name := expr (use = for _, := for named)
        if name == "_"
        then [GoIr.GoExprStmt (exprToGo body)]
        else [GoIr.GoShortDecl name (exprToGo body)]

    Can.Def (A.At _ name) params body ->
        -- Function binding: name := func(params) { return body }
        let goParams = map patternToParam params
        in [GoIr.GoShortDecl name
            (GoIr.GoFuncLit goParams "any" [GoIr.GoReturn (exprToGo body)])]

    Can.TypedDef (A.At _ name) _ [] body _ ->
        [GoIr.GoShortDecl name (exprToGo body)]

    Can.TypedDef (A.At _ name) _ typedPats body _ ->
        let goParams = map (patternToParam . fst) typedPats
        in [GoIr.GoShortDecl name
            (GoIr.GoFuncLit goParams "any" [GoIr.GoReturn (exprToGo body)])]


-- ═══════════════════════════════════════════════════════════
-- CASE-OF
-- ═══════════════════════════════════════════════════════════

-- | Convert case-of to Go (IIFE with switch or if-chain)
caseToGo :: Can.Expr -> [Can.CaseBranch] -> GoIr.GoExpr
caseToGo subject branches =
    let
        goSubject = exprToGo subject
        -- Detect the type from patterns to know how to type-assert
        subjectType = detectSubjectType branches
        -- For typed subjects, use type assertion; for any, use directly
        subjectDecl = case subjectType of
            Just typeName ->
                GoIr.GoShortDecl "__subject"
                    (GoIr.GoTypeAssert goSubject typeName)
            Nothing ->
                GoIr.GoShortDecl "__subject" goSubject
        branchStmts = concatMap (caseBranchToStmts "__subject") branches
        panicStmt = GoIr.GoExprStmt (GoIr.GoRaw "panic(\"non-exhaustive case expression\")")
    in
    GoIr.GoBlock
        (subjectDecl : branchStmts ++ [panicStmt])
        (GoIr.GoRaw "nil")  -- unreachable, branches return


-- | Detect the Go type of the case subject from the patterns
detectSubjectType :: [Can.CaseBranch] -> Maybe String
detectSubjectType branches =
    case branches of
        (Can.CaseBranch (A.At _ pat) _ : _) -> patternGoType pat
        _ -> Nothing
  where
    patternGoType (Can.PCtor home typeName _ ctorName _ _)
        | ctorName == "Ok" || ctorName == "Err" = Just "rt.SkyResult[any, any]"
        | ctorName == "Just" || ctorName == "Nothing" = Just "rt.SkyMaybe[any]"
        | otherwise = Just "rt.SkyADT"
    patternGoType (Can.PBool _) = Nothing  -- bool doesn't need assertion
    patternGoType (Can.PInt _) = Nothing
    patternGoType (Can.PStr _) = Nothing
    patternGoType _ = Nothing


-- | Convert a case branch to Go if-statement
caseBranchToStmts :: String -> Can.CaseBranch -> [GoIr.GoStmt]
caseBranchToStmts subject (Can.CaseBranch pat body) =
    let
        (A.At _ patInner) = pat
        cond = patternCondition subject patInner
        bindings = patternBindings subject patInner
        bodyStmts = bindings ++ [GoIr.GoReturn (exprToGo body)]
    in
    case cond of
        Nothing -> bodyStmts  -- always matches (PVar, PAnything)
        Just condExpr -> [GoIr.GoIf condExpr bodyStmts []]


-- | Generate a Go condition for pattern matching
patternCondition :: String -> Can.Pattern_ -> Maybe GoIr.GoExpr
patternCondition subject pat = case pat of
    Can.PAnything -> Nothing  -- always matches
    Can.PVar _ -> Nothing     -- always matches

    Can.PInt n ->
        Just $ GoIr.GoBinary "==" (GoIr.GoIdent subject) (GoIr.GoIntLit n)

    Can.PStr s ->
        Just $ GoIr.GoBinary "==" (GoIr.GoIdent subject) (GoIr.GoStringLit s)

    Can.PBool True ->
        Just $ GoIr.GoBinary "==" (GoIr.GoIdent subject) (GoIr.GoBoolLit True)

    Can.PBool False ->
        Just $ GoIr.GoBinary "==" (GoIr.GoIdent subject) (GoIr.GoBoolLit False)

    Can.PChr c ->
        Just $ GoIr.GoBinary "==" (GoIr.GoIdent subject) (GoIr.GoRuneLit c)

    Can.PCtor home typeName _union ctorName ctorIdx args ->
        -- Match on .Tag field
        Just $ GoIr.GoBinary "=="
            (GoIr.GoSelector (GoIr.GoIdent subject) "Tag")
            (GoIr.GoIntLit ctorIdx)

    Can.PUnit -> Nothing  -- always matches

    _ -> Nothing  -- fallback: always match (TODO: handle more patterns)


-- | Generate Go variable bindings from a pattern
patternBindings :: String -> Can.Pattern_ -> [GoIr.GoStmt]
patternBindings subject pat = case pat of
    Can.PVar name ->
        [ GoIr.GoShortDecl name (GoIr.GoIdent subject) ]

    Can.PAnything -> []
    Can.PUnit -> []
    Can.PInt _ -> []
    Can.PStr _ -> []
    Can.PBool _ -> []
    Can.PChr _ -> []

    Can.PCtor _home typeName _union ctorName _ctorIdx args ->
        -- Bind constructor arguments
        concatMap (bindCtorArg subject ctorName) args

    _ -> []


-- | Bind a constructor argument to a local variable
bindCtorArg :: String -> String -> Can.PatternCtorArg -> [GoIr.GoStmt]
bindCtorArg subject ctorName (Can.PatternCtorArg idx _ty pat) =
    let (A.At _ innerPat) = pat
    in case innerPat of
        Can.PVar name ->
            let fieldAccess = case ctorName of
                    "Ok"   -> GoIr.GoSelector (GoIr.GoIdent subject) "OkValue"
                    "Err"  -> GoIr.GoSelector (GoIr.GoIdent subject) "ErrValue"
                    "Just" -> GoIr.GoSelector (GoIr.GoIdent subject) "JustValue"
                    _      -> GoIr.GoIndex
                                (GoIr.GoSelector (GoIr.GoIdent subject) "Fields")
                                (GoIr.GoIntLit idx)
            in [ GoIr.GoShortDecl name fieldAccess ]
        Can.PAnything -> []
        _ -> []  -- TODO: nested pattern matching


-- ═══════════════════════════════════════════════════════════
-- MAIN FUNCTION
-- ═══════════════════════════════════════════════════════════

-- | Generate the main() function
generateMainFunc :: Can.Module -> Src.Module -> [GoIr.GoDecl]
generateMainFunc canMod srcMod =
    case findMain canMod of
        Nothing ->
            [ GoIr.GoDeclFunc GoIr.GoFuncDecl
                { GoIr._gf_name = "main"
                , GoIr._gf_typeParams = []
                , GoIr._gf_params = []
                , GoIr._gf_returnType = ""
                , GoIr._gf_body = [GoIr.GoExprStmt (GoIr.GoCall (GoIr.GoQualified "rt" "Log_println") [GoIr.GoStringLit "No main function"])]
                }
            ]
        Just def ->
            let body = defBody def
                hasTask = any isTaskImport (Src._imports srcMod)
                stmts = exprToMainStmts body
                wrappedStmts = if hasTask
                    then stmts  -- TODO: wrap in rt.RunMainTask
                    else stmts
            in
            [ GoIr.GoDeclFunc GoIr.GoFuncDecl
                { GoIr._gf_name = "main"
                , GoIr._gf_typeParams = []
                , GoIr._gf_params = []
                , GoIr._gf_returnType = ""
                , GoIr._gf_body = wrappedStmts
                }
            ]


-- | Find the main definition
findMain :: Can.Module -> Maybe Can.Def
findMain canMod = findMainInDecls (Can._decls canMod)
  where
    findMainInDecls Can.SaveTheEnvironment = Nothing
    findMainInDecls (Can.Declare def rest) =
        if defName def == "main" then Just def else findMainInDecls rest
    findMainInDecls (Can.DeclareRec def defs rest) =
        if defName def == "main" then Just def
        else case filter (\d -> defName d == "main") defs of
            (d:_) -> Just d
            [] -> findMainInDecls rest


-- | Get the name from a definition
defName :: Can.Def -> String
defName (Can.Def (A.At _ n) _ _) = n
defName (Can.TypedDef (A.At _ n) _ _ _ _) = n


-- | Get the body expression from a definition
defBody :: Can.Def -> Can.Expr
defBody (Can.Def _ _ body) = body
defBody (Can.TypedDef _ _ _ body _) = body


-- | Convert the main body to Go statements (not a return value)
exprToMainStmts :: Can.Expr -> [GoIr.GoStmt]
exprToMainStmts (A.At _ expr) = case expr of
    Can.Let def body ->
        defToStmts def ++ exprToMainStmts body

    Can.LetRec defs body ->
        concatMap defToStmts defs ++ exprToMainStmts body

    Can.LetDestruct _pat valExpr body ->
        [GoIr.GoExprStmt (exprToGo valExpr)] ++ exprToMainStmts body

    Can.Call _ _ ->
        [GoIr.GoExprStmt (exprToGo (A.At A.one expr))]

    _ ->
        [GoIr.GoExprStmt (exprToGo (A.At A.one expr))]


-- ═══════════════════════════════════════════════════════════
-- HELPERS
-- ═══════════════════════════════════════════════════════════

-- | Generate a curried lambda: \a b -> body → func(a) { return func(b) { return body } }
curryLambda :: [GoIr.GoParam] -> GoIr.GoExpr -> GoIr.GoExpr
curryLambda [] body = body
curryLambda [p] body = GoIr.GoFuncLit [p] "any" [GoIr.GoReturn body]
curryLambda (p:ps) body =
    GoIr.GoFuncLit [p] "any" [GoIr.GoReturn (curryLambda ps body)]


-- | Convert a pattern to a Go function parameter
patternToParam :: Can.Pattern -> GoIr.GoParam
patternToParam (A.At _ pat) = case pat of
    Can.PVar name -> GoIr.GoParam name "any"
    _ -> GoIr.GoParam "_" "any"


-- | Extract a single name from a pattern (for destructuring)
patternName :: Can.Pattern -> String
patternName (A.At _ pat) = case pat of
    Can.PVar name -> name
    _ -> "_"


-- ═══════════════════════════════════════════════════════════
-- GO RUNTIME SOURCE (embedded)
-- ═══════════════════════════════════════════════════════════

-- | The Go runtime package source — typed with generics
runtimeGoSource :: String
runtimeGoSource = unlines
    [ "package rt"
    , ""
    , "import ("
    , "\t\"fmt\""
    , "\t\"strconv\""
    , "\t\"strings\""
    , ")"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Result"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "type SkyResult[E any, A any] struct {"
    , "\tTag      int"
    , "\tOkValue  A"
    , "\tErrValue E"
    , "}"
    , ""
    , "func Ok[E any, A any](v A) SkyResult[E, A] {"
    , "\treturn SkyResult[E, A]{Tag: 0, OkValue: v}"
    , "}"
    , ""
    , "func Err[E any, A any](e E) SkyResult[E, A] {"
    , "\treturn SkyResult[E, A]{Tag: 1, ErrValue: e}"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Maybe"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "type SkyMaybe[A any] struct {"
    , "\tTag       int"
    , "\tJustValue A"
    , "}"
    , ""
    , "func Just[A any](v A) SkyMaybe[A] {"
    , "\treturn SkyMaybe[A]{Tag: 0, JustValue: v}"
    , "}"
    , ""
    , "func Nothing[A any]() SkyMaybe[A] {"
    , "\treturn SkyMaybe[A]{Tag: 1}"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Task"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "type SkyTask[E any, A any] func() SkyResult[E, A]"
    , ""
    , "func Task_succeed[E any, A any](v A) SkyTask[E, A] {"
    , "\treturn func() SkyResult[E, A] { return Ok[E, A](v) }"
    , "}"
    , ""
    , "func Task_fail[E any, A any](e E) SkyTask[E, A] {"
    , "\treturn func() SkyResult[E, A] { return Err[E, A](e) }"
    , "}"
    , ""
    , "func Task_andThen[E any, A any, B any](fn func(A) SkyTask[E, B], task SkyTask[E, A]) SkyTask[E, B] {"
    , "\treturn func() SkyResult[E, B] {"
    , "\t\tr := task()"
    , "\t\tif r.Tag == 0 {"
    , "\t\t\treturn fn(r.OkValue)()"
    , "\t\t}"
    , "\t\treturn Err[E, B](r.ErrValue)"
    , "\t}"
    , "}"
    , ""
    , "func Task_run[E any, A any](task SkyTask[E, A]) SkyResult[E, A] {"
    , "\treturn task()"
    , "}"
    , ""
    , "func RunMainTask[E any, A any](task SkyTask[E, A]) {"
    , "\tr := task()"
    , "\tif r.Tag == 1 {"
    , "\t\tfmt.Println(\"Error:\", r.ErrValue)"
    , "\t}"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Composition"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func ComposeL[A any, B any, C any](f func(A) B, g func(B) C) func(A) C {"
    , "\treturn func(a A) C { return g(f(a)) }"
    , "}"
    , ""
    , "func ComposeR[A any, B any, C any](g func(B) C, f func(A) B) func(A) C {"
    , "\treturn func(a A) C { return g(f(a)) }"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Log"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Log_println(s any) any {"
    , "\tfmt.Println(s)"
    , "\treturn struct{}{}"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// String"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func String_fromInt(n any) any {"
    , "\treturn strconv.Itoa(AsInt(n))"
    , "}"
    , ""
    , "func String_fromFloat(f any) any {"
    , "\treturn strconv.FormatFloat(AsFloat(f), 'f', -1, 64)"
    , "}"
    , ""
    , "func String_length(s any) any {"
    , "\treturn len(fmt.Sprintf(\"%v\", s))"
    , "}"
    , ""
    , "func String_isEmpty(s any) any {"
    , "\treturn len(fmt.Sprintf(\"%v\", s)) == 0"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Basics"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Basics_identity[A any](a A) A {"
    , "\treturn a"
    , "}"
    , ""
    , "func Basics_always[A any, B any](a A, _ B) A {"
    , "\treturn a"
    , "}"
    , ""
    , "func Basics_not(b bool) bool {"
    , "\treturn !b"
    , "}"
    , ""
    , "func Basics_toString(v any) string {"
    , "\treturn fmt.Sprintf(\"%v\", v)"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Concat (temporary — will use + when types are known)"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Concat(a, b any) any {"
    , "\treturn fmt.Sprintf(\"%v%v\", a, b)"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Arithmetic and comparison (any-typed, until type checker)"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func AsInt(v any) int { if n, ok := v.(int); ok { return n }; return 0 }"
    , "func AsFloat(v any) float64 { if f, ok := v.(float64); ok { return f }; if n, ok := v.(int); ok { return float64(n) }; return 0 }"
    , "func AsBool(v any) bool { if b, ok := v.(bool); ok { return b }; return false }"
    , ""
    , "func Add(a, b any) any { return AsInt(a) + AsInt(b) }"
    , "func Sub(a, b any) any { return AsInt(a) - AsInt(b) }"
    , "func Mul(a, b any) any { return AsInt(a) * AsInt(b) }"
    , "func Div(a, b any) any { if AsInt(b) == 0 { return 0 }; return AsInt(a) / AsInt(b) }"
    , ""
    , "func Eq(a, b any) any { return a == b }"
    , "func Gt(a, b any) any { return AsInt(a) > AsInt(b) }"
    , "func Lt(a, b any) any { return AsInt(a) < AsInt(b) }"
    , "func Gte(a, b any) any { return AsInt(a) >= AsInt(b) }"
    , "func Lte(a, b any) any { return AsInt(a) <= AsInt(b) }"
    , ""
    , "func And(a, b any) any { return AsBool(a) && AsBool(b) }"
    , "func Or(a, b any) any { return AsBool(a) || AsBool(b) }"
    , ""
    , "func Negate(a any) any { return -AsInt(a) }"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// List operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func List_map(fn any, list any) any {"
    , "\tf := fn.(func(any) any)"
    , "\titems := list.([]any)"
    , "\tresult := make([]any, len(items))"
    , "\tfor i, item := range items { result[i] = f(item) }"
    , "\treturn result"
    , "}"
    , ""
    , "func List_filter(fn any, list any) any {"
    , "\tf := fn.(func(any) any)"
    , "\titems := list.([]any)"
    , "\tvar result []any"
    , "\tfor _, item := range items {"
    , "\t\tif AsBool(f(item)) { result = append(result, item) }"
    , "\t}"
    , "\treturn result"
    , "}"
    , ""
    , "func List_foldl(fn any, acc any, list any) any {"
    , "\tf := fn.(func(any) any)"
    , "\titems := list.([]any)"
    , "\tresult := acc"
    , "\tfor _, item := range items {"
    , "\t\tstep := f(item)"
    , "\t\tresult = step.(func(any) any)(result)"
    , "\t}"
    , "\treturn result"
    , "}"
    , ""
    , "func List_length(list any) any {"
    , "\titems := list.([]any)"
    , "\treturn len(items)"
    , "}"
    , ""
    , "func List_head(list any) any {"
    , "\titems := list.([]any)"
    , "\tif len(items) == 0 { return Nothing[any]() }"
    , "\treturn Just[any](items[0])"
    , "}"
    , ""
    , "func List_reverse(list any) any {"
    , "\titems := list.([]any)"
    , "\tresult := make([]any, len(items))"
    , "\tfor i, item := range items { result[len(items)-1-i] = item }"
    , "\treturn result"
    , "}"
    , ""
    , "func List_range(lo any, hi any) any {"
    , "\tl, h := AsInt(lo), AsInt(hi)"
    , "\tresult := make([]any, 0, h-l+1)"
    , "\tfor i := l; i <= h; i++ { result = append(result, i) }"
    , "\treturn result"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// More String operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func String_join(sep any, list any) any {"
    , "\ts := fmt.Sprintf(\"%v\", sep)"
    , "\titems := list.([]any)"
    , "\tparts := make([]string, len(items))"
    , "\tfor i, item := range items { parts[i] = fmt.Sprintf(\"%v\", item) }"
    , "\treturn strings.Join(parts, s)"
    , "}"
    , ""
    , "func String_split(sep any, s any) any {"
    , "\tparts := strings.Split(fmt.Sprintf(\"%v\", s), fmt.Sprintf(\"%v\", sep))"
    , "\tresult := make([]any, len(parts))"
    , "\tfor i, p := range parts { result[i] = p }"
    , "\treturn result"
    , "}"
    , ""
    , "func String_toInt(s any) any {"
    , "\tn, err := strconv.Atoi(fmt.Sprintf(\"%v\", s))"
    , "\tif err != nil { return Nothing[any]() }"
    , "\treturn Just[any](n)"
    , "}"
    , ""
    , "func String_toUpper(s any) any { return strings.ToUpper(fmt.Sprintf(\"%v\", s)) }"
    , "func String_toLower(s any) any { return strings.ToLower(fmt.Sprintf(\"%v\", s)) }"
    , "func String_trim(s any) any { return strings.TrimSpace(fmt.Sprintf(\"%v\", s)) }"
    , "func String_contains(sub any, s any) any { return strings.Contains(fmt.Sprintf(\"%v\", s), fmt.Sprintf(\"%v\", sub)) }"
    , "func String_startsWith(prefix any, s any) any { return strings.HasPrefix(fmt.Sprintf(\"%v\", s), fmt.Sprintf(\"%v\", prefix)) }"
    , "func String_reverse(s any) any { runes := []rune(fmt.Sprintf(\"%v\", s)); for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 { runes[i], runes[j] = runes[j], runes[i] }; return string(runes) }"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Record operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func RecordGet(record any, field string) any {"
    , "\tif m, ok := record.(map[string]any); ok { return m[field] }"
    , "\treturn nil"
    , "}"
    , ""
    , "func RecordUpdate(base any, updates map[string]any) any {"
    , "\toriginal := base.(map[string]any)"
    , "\tresult := make(map[string]any, len(original))"
    , "\tfor k, v := range original { result[k] = v }"
    , "\tfor k, v := range updates { result[k] = v }"
    , "\treturn result"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Tuple types"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "type SkyTuple2 struct { V0, V1 any }"
    , "type SkyTuple3 struct { V0, V1, V2 any }"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Any-typed Task wrappers (until type checker provides types)"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func AnyTaskSucceed(v any) any {"
    , "\treturn func() any { return Ok[any, any](v) }"
    , "}"
    , ""
    , "func AnyTaskFail(e any) any {"
    , "\treturn func() any { return Err[any, any](e) }"
    , "}"
    , ""
    , "func AnyTaskAndThen(fn any, task any) any {"
    , "\treturn func() any {"
    , "\t\tt := task.(func() any)"
    , "\t\tr := t().(SkyResult[any, any])"
    , "\t\tif r.Tag == 0 {"
    , "\t\t\tnext := fn.(func(any) any)(r.OkValue).(func() any)"
    , "\t\t\treturn next()"
    , "\t\t}"
    , "\t\treturn Err[any, any](r.ErrValue)"
    , "\t}"
    , "}"
    , ""
    , "func AnyTaskRun(task any) any {"
    , "\tt := task.(func() any)"
    , "\treturn t()"
    , "}"
    ]


-- | String intercalation helper
intercalate_ :: String -> [String] -> String
intercalate_ _ [] = ""
intercalate_ _ [x] = x
intercalate_ sep (x:xs) = x ++ sep ++ intercalate_ sep xs
