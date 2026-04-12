-- | Single-module compilation pipeline.
-- Source → Parse → Canonicalise → (TODO: Type Check) → Generate Go
module Sky.Build.Compile where

import qualified Control.Concurrent.Async as Async
import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Data.Text as T
import qualified Data.Text.IO as TIO
import Data.IORef
import qualified System.Directory
import qualified System.FilePath
import System.Directory (createDirectoryIfMissing, doesDirectoryExist, doesFileExist, copyFile, listDirectory)
import System.IO (hFlush, stdout)
import System.IO.Unsafe (unsafePerformIO)
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
import qualified Sky.Type.Type as T
import qualified Sky.Generate.Go.Type as GoType
import qualified Sky.Generate.Go.Record as Rec
import qualified Sky.Build.ModuleGraph as Graph
import qualified Sky.Build.Dce as Dce
import qualified System.Environment


-- | Global codegen environment (set once per compilation, read during codegen)
{-# NOINLINE globalCgEnv #-}
globalCgEnv :: IORef Rec.CodegenEnv
globalCgEnv = unsafePerformIO $ newIORef (Rec.CodegenEnv Map.empty Map.empty Map.empty Set.empty Set.empty Map.empty)

-- | Read the global codegen env (for use in pure codegen functions)
getCgEnv :: Rec.CodegenEnv
getCgEnv = unsafePerformIO $ readIORef globalCgEnv


-- | Full compilation: parse → canonicalise → codegen → write Go
compile :: Toml.SkyConfig -> FilePath -> FilePath -> IO (Either String FilePath)
compile config entryPath outDir = do
    -- Compute source root relative to the entry file
    let entryDir = takeDirectory entryPath
        sourceRoot = if Toml._sourceRoot config == "src"
            then entryDir  -- entry IS in the source root
            else Toml._sourceRoot config

    -- Phase 1: Discover all modules
    putStrLn "-- Discovering modules"
    modules <- Graph.discoverModules sourceRoot entryPath
    let moduleOrder = Graph.compilationOrder modules
    putStrLn $ "   Found " ++ show (length moduleOrder) ++ " module(s)"

    -- Incremental build: if source hash matches cached, reuse output
    srcHash <- computeSourceHash (map Graph._mi_path moduleOrder)
    let cacheDir = ".skycache"
        hashFile = cacheDir </> "source.hash"
        existingMain = outDir </> "main.go"
    cacheHit <- do
        hasHash <- doesFileExist hashFile
        hasMain <- doesFileExist existingMain
        if hasHash && hasMain
            then do
                cached <- readFile hashFile
                return (cached == srcHash)
            else return False
    if cacheHit
        then do
            putStrLn "-- Incremental: source unchanged, reusing cached output"
            copyRuntime outDir
            return (Right existingMain)
        else continueCompile config entryPath outDir moduleOrder srcHash


-- | Compute a stable hash of all source file contents
computeSourceHash :: [FilePath] -> IO String
computeSourceHash paths = do
    contents <- mapM readFile paths
    -- Simple, not cryptographic: sum of SDBM-ish hashes keyed by path
    let combined = concat (zipWith (\p c -> p ++ ":" ++ c ++ "\n") paths contents)
    return (show (length combined) ++ "-" ++ show (foldl (\acc c -> acc * 31 + fromEnum c) (0 :: Int) combined))


continueCompile :: Toml.SkyConfig -> FilePath -> FilePath -> [Graph.ModuleInfo] -> String -> IO (Either String FilePath)
continueCompile config _entryPath outDir moduleOrder srcHash = do

    -- Phase 2: Parse all modules in parallel — parsing is pure text→AST
    -- with no cross-module dependencies, so it parallelises trivially.
    -- We preserve topo order in the result list so downstream phases see the
    -- same ordering as a sequential build.
    putStrLn "-- Parsing"
    parseResults <- Async.forConcurrently moduleOrder $ \modInfo -> do
        src <- TIO.readFile (Graph._mi_path modInfo)
        case Parse.parseModule src of
            Left err ->
                return (modInfo, Left err)
            Right srcMod ->
                return (modInfo, Right srcMod)
    let formatted = flip map parseResults $ \(modInfo, r) -> case r of
            Left err ->
                Left $ "Parse error in " ++ Graph._mi_name modInfo ++ ": " ++ show err
            Right srcMod ->
                Right (Graph._mi_name modInfo, srcMod)
    -- Print summaries in deterministic order
    mapM_ (\(modInfo, r) -> case r of
        Left err ->
            putStrLn $ "   PARSE FAILED: " ++ Graph._mi_name modInfo ++ " " ++ show err
        Right srcMod ->
            let declCount = length (Src._values srcMod)
            in putStrLn $ "   " ++ Graph._mi_name modInfo ++ ": " ++ show declCount ++ " declarations"
        ) parseResults
    let parseResults' = formatted

    let errors = [e | Left e <- parseResults']
        parsed = [(n, m) | Right (n, m) <- parseResults']

    if not (null errors) then return (Left $ head errors)
      else if null parsed then return (Left "No modules found")
      else do
        -- Phase 3: Canonicalise (entry module + merge deps)
        putStrLn "-- Canonicalising"
        let entrySrcMod = snd (last parsed)
            -- Dependency modules are all parsed modules except the entry.
            depModules = if length parsed > 1 then init parsed else []

        -- Two-pass canonicalisation so dep modules can reference each
        -- other's ADT constructors:
        --   1. Canonicalise each dep in isolation (only its own ADTs visible)
        --      to build a depInfoMap with every module's union constructors.
        --   2. Re-canonicalise every dep AND the entry with the full map.
        firstPassDeps <- Async.forConcurrently depModules $ \(n, srcMod) ->
            case Canonicalise.canonicalise srcMod of
                Right cm -> return (Just (n, cm))
                Left _   -> return Nothing
        let firstValid = [x | Just x <- firstPassDeps]
            depInfoMap = Map.fromList
                [ (modName, Canonicalise.DepInfo
                    { Canonicalise._dep_name = Can._name depMod
                    , Canonicalise._dep_unions =
                        [ (typeName, Can._u_alts union)
                        | (typeName, union) <- Map.toList (Can._unions depMod)
                        ]
                    , Canonicalise._dep_aliases = Map.keys (Can._aliases depMod)
                    , Canonicalise._dep_values = Set.toList (collectDeclNames (Can._decls depMod))
                    })
                | (modName, depMod) <- firstValid
                ]

        -- Pass 2: re-canonicalise deps with full cross-module info.
        depCanMods <- Async.forConcurrently depModules $ \(n, srcMod) ->
            case Canonicalise.canonicaliseWithDeps depInfoMap srcMod of
                Right cm -> return (Just (n, cm))
                Left _   -> return Nothing
        let validDeps = [x | Just x <- depCanMods]

        case Canonicalise.canonicaliseWithDeps depInfoMap entrySrcMod of
          Left err -> return (Left $ "Canonicalise error: " ++ err)
          Right canMod -> do
            putStrLn "   Names resolved"
            let depDecls = concatMap (\(modName, depMod) ->
                    let prefix = map (\c -> if c == '.' then '_' else c) modName
                    in generateDeclsForDep depMod prefix) validDeps
                depRecAliases = Set.unions
                    [ Set.map (\n -> prefix ++ "_" ++ n)
                             (Rec.collectRecordAliases (Can._aliases depMod))
                    | (modName, depMod) <- validDeps
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    ]
                depArities = Map.unions
                    [ Map.mapKeys (\n -> prefix ++ "_" ++ n)
                                  (Rec.collectFuncArities (Can._decls depMod))
                    | (modName, depMod) <- validDeps
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    ]
            putStrLn "-- Type Checking"
            constraints <- Constrain.constrainModule canMod
            solveResult <- Solve.solve constraints
            types <- case solveResult of
                Solve.SolveOk types -> do
                    putStrLn $ "   Types OK (" ++ show (length (Map.keys types)) ++ " bindings)"
                    return types
                Solve.SolveError err -> do
                    putStrLn $ "   TYPE WARNING: " ++ err
                    return Map.empty
            putStrLn "-- Generating Go"
            let goCode = generateGoMulti canMod entrySrcMod config types depDecls depRecAliases depArities
            createDirectoryIfMissing True outDir
            let mainGoPath = outDir </> "main.go"
            writeFile mainGoPath goCode
            putStrLn $ "   Wrote " ++ mainGoPath
            -- copyRuntime also copies runtime-go/go.mod + go.sum into outDir
            -- when it can locate the runtime. Only fall back to a minimal
            -- go.mod here if copyRuntime didn't write one (no runtime found).
            copyRuntime outDir
            hasOutMod <- doesFileExist (outDir </> "go.mod")
            if not hasOutMod
                then writeFile (outDir </> "go.mod") $ unlines ["module sky-app", "", "go 1.21"]
                else return ()
            -- Write cache hash to enable incremental rebuild skip
            let cacheDir = ".skycache"
            createDirectoryIfMissing True cacheDir
            writeFile (cacheDir </> "source.hash") srcHash
            putStrLn "Compilation successful"
            return (Right mainGoPath)


-- LEGACY: single-module parse entry (no longer used from compile)
parseSingle :: Toml.SkyConfig -> FilePath -> FilePath -> IO (Either String FilePath)
parseSingle config entryPath outDir = do
    source <- TIO.readFile entryPath
    putStrLn $ "-- Lexing " ++ entryPath
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
                    constraints <- Constrain.constrainModule canMod
                    solveResult <- Solve.solve constraints
                    let solvedTypes = case solveResult of
                            Solve.SolveOk types -> do
                                putStrLn $ "   Types OK (" ++ show (length (Map.keys types)) ++ " bindings)"
                                mapM_ (\(n, t) -> putStrLn $ "     " ++ n ++ " : " ++ Solve.showType t) (Map.toList types)
                                return types
                            Solve.SolveError err -> do
                                putStrLn $ "   TYPE WARNING: " ++ err
                                -- Still return empty types — codegen falls back to any
                                return Map.empty
                    types <- solvedTypes

                    -- Phase 5: Generate Go (using solved types)
                    putStrLn "-- Generating Go"
                    let goCode = generateGo canMod srcMod config types

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


-- | Copy user FFI files from ./ffi/*.go into sky-out/rt/ so they compile into
-- the same Go package as the runtime. Users call `rt.Register` from init() in
-- these files to expose Go functions to Sky via Ffi.call "name" args.
copyFfiDir :: FilePath -> IO ()
copyFfiDir outDir = do
    let ffiDir = "ffi"
        dstDir = outDir </> "rt"
    exists <- doesDirectoryExist ffiDir
    if not exists then return ()
        else do
            contents <- listDirectoryHs ffiDir
            let goFiles = filter isGoFile contents
            mapM_ (\f -> copyFile (ffiDir </> f) (dstDir </> f)) goFiles
  where
    isGoFile name = ".go" `isSuffixOfHs` name
    isSuffixOfHs suffix name =
        length name >= length suffix &&
        drop (length name - length suffix) name == suffix


listDirectoryHs :: FilePath -> IO [FilePath]
listDirectoryHs = listDirectory


-- | Copy the Go runtime package into the output directory.
-- Locates runtime-go/ via (in order):
--   1. SKY_RUNTIME_DIR env var (explicit override)
--   2. ./runtime-go (cwd-relative — for compiler dev)
--   3. <binary-dir>/../runtime-go (installed layout, binary in bin/)
--   4. <binary-dir>/../../runtime-go (cabal dist-newstyle layout)
--   5. Walk up from cwd looking for a haskell-compiler/runtime-go sibling
--   6. Fall back to inline runtimeGoSource string (hello-world only — misses
--      Live, DB, Auth, FFI, stdlib extras — most programs will fail at link)
copyRuntime :: FilePath -> IO ()
copyRuntime outDir = do
    let rtDir = outDir </> "rt"
    createDirectoryIfMissing True rtDir
    mRuntime <- locateRuntimeDir
    case mRuntime of
        Nothing -> do
            putStrLn "   Warning: runtime-go/ not found — using minimal fallback."
            putStrLn "   Set SKY_RUNTIME_DIR to point at your haskell-compiler/runtime-go."
            writeFile (rtDir </> "rt.go") runtimeGoSource
        Just runtimeDir -> do
            let mainRt = runtimeDir </> "rt" </> "rt.go"
            mainExists <- doesFileExist mainRt
            if mainExists
                then copyFile mainRt (rtDir </> "rt.go")
                else writeFile (rtDir </> "rt.go") runtimeGoSource
            -- Copy every *.go file in runtime-go/rt/ so new runtime modules
            -- are picked up automatically without hardcoding names.
            let rtSourceDir = runtimeDir </> "rt"
            hasRtDir <- doesDirectoryExist rtSourceDir
            if hasRtDir
                then do
                    files <- System.Directory.listDirectory rtSourceDir
                    let goFiles = filter (\f ->
                            let ext = reverse (take 3 (reverse f))
                            in ext == ".go" && f /= "rt.go"
                            ) files
                    mapM_ (\name -> copyFile (rtSourceDir </> name) (rtDir </> name)) goFiles
                else return ()
            -- Copy go.mod and go.sum to inherit runtime dep versions.
            let srcMod = runtimeDir </> "go.mod"
            hasSrcMod <- doesFileExist srcMod
            if hasSrcMod then copyFile srcMod (outDir </> "go.mod") else return ()
            let srcSum = runtimeDir </> "go.sum"
            hasSum <- doesFileExist srcSum
            if hasSum then copyFile srcSum (outDir </> "go.sum") else return ()
    -- User FFI: copy ./ffi/*.go into sky-out/rt/ regardless of runtime-go location.
    copyFfiDir outDir


-- | Locate the runtime-go directory by probing known locations.
locateRuntimeDir :: IO (Maybe FilePath)
locateRuntimeDir = do
    envVar <- System.Environment.lookupEnv "SKY_RUNTIME_DIR"
    case envVar of
        Just p -> do
            ok <- doesDirectoryExist p
            if ok then return (Just p) else probeLocations
        Nothing -> probeLocations
  where
    probeLocations = do
        cands <- candidates
        firstExisting cands

    candidates = do
        cwd <- System.Directory.getCurrentDirectory
        exeDir <- fmap System.FilePath.takeDirectory System.Environment.getExecutablePath
        -- Walk up from the binary's dir (cabal dist-newstyle nests ~8 deep)
        -- and from cwd looking for an ancestor containing runtime-go/rt/.
        let upN n base = iterate (</> "..") base !! n
        return $
            "runtime-go"
            : [ upN n exeDir </> "runtime-go" | n <- [0..12] ]
            ++ [ upN n cwd </> "runtime-go" | n <- [0..12] ]

    firstExisting [] = return Nothing
    firstExisting (p:ps) = do
        ok <- doesDirectoryExist (p </> "rt")
        if ok then return (Just p) else firstExisting ps


-- ═══════════════════════════════════════════════════════════
-- GO CODE GENERATION (from Canonical AST)
-- ═══════════════════════════════════════════════════════════

-- | Generate Go declarations for a dependency module's functions
generateDeclsForDep :: Can.Module -> String -> [GoIr.GoDecl]
generateDeclsForDep canMod modPrefix =
    let userDefs = collectDeclNames (Can._decls canMod)
    in concatMap (generateUnionForDep modPrefix) (Map.toList (Can._unions canMod))
    ++ concatMap (generateAliasForDep userDefs modPrefix) (Map.toList (Can._aliases canMod))
    ++ go (Can._decls canMod)
  where
    go Can.SaveTheEnvironment = []
    go (Can.Declare def rest) = mkDef def ++ go rest
    go (Can.DeclareRec def defs rest) = mkDef def ++ concatMap mkDef defs ++ go rest

    mkDef def =
        let (name, params, body) = case def of
                Can.Def (A.At _ n) pats expr -> (n, pats, expr)
                Can.TypedDef (A.At _ n) _ typedPats expr _ -> (n, map fst typedPats, expr)
            goName = modPrefix ++ "_" ++ name
        in [ GoIr.GoDeclFunc GoIr.GoFuncDecl
                { GoIr._gf_name = goName
                , GoIr._gf_typeParams = []
                , GoIr._gf_params = map patternToParam params
                , GoIr._gf_returnType = "any"
                , GoIr._gf_body = [GoIr.GoReturn (exprToGo body)]
                }
           ]


-- | Walk a Decls tree, collecting every value-level name
collectDeclNames :: Can.Decls -> Set.Set String
collectDeclNames = goNames Set.empty
  where
    goNames acc Can.SaveTheEnvironment = acc
    goNames acc (Can.Declare d rest) = goNames (addName acc d) rest
    goNames acc (Can.DeclareRec d ds rest) =
        goNames (foldr (flip addName) (addName acc d) ds) rest
    addName acc d = case d of
        Can.Def (A.At _ n) _ _ -> Set.insert n acc
        Can.TypedDef (A.At _ n) _ _ _ _ -> Set.insert n acc


-- | Emit a dep module's union type declaration + constructor value/func.
-- Type becomes `<ModPrefix>_<TypeName>` and each ctor becomes
-- `<ModPrefix>_<TypeName>_<CtorName>`.
generateUnionForDep :: String -> (String, Can.Union) -> [GoIr.GoDecl]
generateUnionForDep modPrefix (typeName, Can.Union _vars ctors _numAlts opts) =
    let qualType = modPrefix ++ "_" ++ typeName
    in case opts of
        Can.Enum ->
            [ GoIr.GoDeclType qualType (GoIr.GoEnumDef
                [ qualType ++ "_" ++ cname
                | Can.Ctor cname _ _ _ <- ctors
                ])
            ]
        _ ->
            GoIr.GoDeclRaw ("type " ++ qualType ++ " struct { Tag int; Fields []any }")
            : [ if arity == 0
                  then GoIr.GoDeclVar (qualType ++ "_" ++ cname) qualType
                        (Just (GoIr.GoStructLit qualType [("Tag", GoIr.GoIntLit idx)]))
                  else GoIr.GoDeclFunc GoIr.GoFuncDecl
                        { GoIr._gf_name = qualType ++ "_" ++ cname
                        , GoIr._gf_typeParams = []
                        , GoIr._gf_params = zipWith (\i _ -> GoIr.GoParam ("v" ++ show i) "any")
                                                [0::Int ..] [1..arity]
                        , GoIr._gf_returnType = qualType
                        , GoIr._gf_body = [GoIr.GoReturn (GoIr.GoStructLit qualType
                            ([("Tag", GoIr.GoIntLit idx)]
                            ++ [("Fields", GoIr.GoSliceLit "any"
                                    (map (\i -> GoIr.GoIdent ("v" ++ show i)) [0..arity-1]))]))]
                        }
              | Can.Ctor cname idx arity _ <- ctors
              ]


-- | Emit a dep module's type alias. Record aliases become Go named structs
-- so cross-module records type-check. Non-record aliases become Go type aliases.
-- Record aliases emit BOTH a struct type (suffixed "_R" to avoid collision
-- with user-defined constructor functions of the same name) AND an auto-
-- constructor function using the original alias name.
generateAliasForDep :: Set.Set String -> String -> (String, Can.Alias) -> [GoIr.GoDecl]
generateAliasForDep userDefs modPrefix (aliasName, Can.Alias _vars body) =
    let qualName = modPrefix ++ "_" ++ aliasName
        structName = qualName ++ "_R"
    in case body of
        T.TRecord fields _ ->
            let fieldList = Map.toList fields
                structDecl = GoIr.GoDeclRaw $ "type " ++ structName ++ " struct { "
                    ++ intercalate_ "; "
                        [ capitalise_ fn ++ " any"
                        | (fn, _) <- fieldList
                        ]
                    ++ " }"
                -- Auto-ctor (func <qualName>(...)) unless the user defined one.
                hasUserCtor = Set.member aliasName userDefs
                paramList = zipWith (\i _ -> "p" ++ show i) [0::Int ..] fieldList
                paramDecls = intercalate_ ", " [p ++ " any" | p <- paramList]
                fieldInits =
                    [ let goTy = solvedTypeToGo fty
                          src = "p" ++ show i
                          coerced = if goTy == "any" || null goTy
                                        then src
                                        else "any(" ++ src ++ ").(" ++ goTy ++ ")"
                      in capitalise_ fn ++ ": " ++ coerced
                    | (i, (fn, T.FieldType _ fty)) <- zip [0::Int ..] fieldList
                    ]
                ctorDecl = GoIr.GoDeclRaw $
                    "func " ++ qualName ++ "(" ++ paramDecls ++ ") " ++ structName ++
                    " { return " ++ structName ++ "{" ++ intercalate_ ", " fieldInits ++ "} }"
            in structDecl : [ctorDecl | not hasUserCtor]
        _ ->
            [ GoIr.GoDeclRaw ("type " ++ qualName ++ " = any") ]


-- | Generate Go with merged dependency declarations
generateGoMulti :: Can.Module -> Src.Module -> Toml.SkyConfig -> Solve.SolvedTypes -> [GoIr.GoDecl] -> Set.Set String -> Map.Map String Int -> String
generateGoMulti canMod srcMod config solvedTypes depDecls depRecAliases depArities =
    let
        imports = unsafePerformIO $ do
            let cgEnv = Rec.withDepArities depArities
                      $ Rec.withRecordAliases depRecAliases
                      $ Rec.buildCodegenEnv solvedTypes canMod
            writeIORef globalCgEnv cgEnv
            return $ collectGoImports canMod srcMod
        unionDecls = generateUnionTypes canMod
        aliasDecls = generateAliasTypes canMod
        decls = generateDecls canMod solvedTypes
        mainDecl = generateMainFunc canMod srcMod solvedTypes
        -- Pin the rt import so Go doesn't error out with "imported and not used"
        -- when the user's program doesn't happen to reference rt.* directly
        -- (e.g. main = 42). The blank var reference is zero-cost at runtime.
        rtPin = [GoIr.GoDeclRaw "var _ = rt.AsInt"]
        pkg = GoIr.GoPackage
            { GoIr._pkg_name = "main"
            , GoIr._pkg_imports = imports
            , GoIr._pkg_decls = rtPin ++ depDecls ++ unionDecls ++ aliasDecls ++ decls ++ mainDecl
            }
    in GoBuilder.renderPackage pkg


-- | Generate Go source from a canonical module with solved types (single module)
generateGo :: Can.Module -> Src.Module -> Toml.SkyConfig -> Solve.SolvedTypes -> String
generateGo canMod srcMod config solvedTypes =
    let
        imports = unsafePerformIO $ do
            let cgEnv = Rec.buildCodegenEnv solvedTypes canMod
            writeIORef globalCgEnv cgEnv
            return $ collectGoImports canMod srcMod
        unionDecls = generateUnionTypes canMod
        aliasDecls = generateAliasTypes canMod
        decls = generateDecls canMod solvedTypes
        mainDecl = generateMainFunc canMod srcMod solvedTypes
        pkg = GoIr.GoPackage
            { GoIr._pkg_name = "main"
            , GoIr._pkg_imports = imports
            , GoIr._pkg_decls = unionDecls ++ aliasDecls ++ decls ++ mainDecl
            }
    in GoBuilder.renderPackage pkg


-- | Collect Go imports needed
collectGoImports :: Can.Module -> Src.Module -> [GoIr.GoImport]
collectGoImports _canMod _srcMod =
    -- Import as blank to avoid "imported and not used" when user's main is
    -- a pure value. If main uses rt.* anywhere, Go doesn't complain about
    -- adding a blank import alongside the aliased one.
    -- Simpler: emit `_ = rt.Log_println` in a blank var at top.
    [ GoIr.GoImport "sky-app/rt" (Just "rt") ]


-- | Check if module imports Task
isTaskImport :: Src.Import -> Bool
isTaskImport imp =
    let segs = case Src._importName imp of A.At _ s -> s
    in segs == ["Sky", "Core", "Task"]


-- ═══════════════════════════════════════════════════════════
-- DECLARATIONS
-- ═══════════════════════════════════════════════════════════

-- | Generate Go type declarations for user-defined union types
generateUnionTypes :: Can.Module -> [GoIr.GoDecl]
generateUnionTypes canMod = concatMap generateUnion (Map.toList (Can._unions canMod))
  where
    generateUnion (typeName, Can.Union vars ctors numAlts opts) = case opts of
        Can.Enum ->
            -- Enum: type Name int; const ( Name_Ctor = iota ... )
            [ GoIr.GoDeclType typeName (GoIr.GoEnumDef (map (ctorConstName typeName) ctors)) ]
        _ ->
            -- Tagged union: struct with Tag + fields
            [ GoIr.GoDeclRaw $ "type " ++ typeName ++ " struct { Tag int; Fields []any }" ]
            ++ map (generateCtorFunc typeName) ctors

    ctorConstName typeName (Can.Ctor cname _ _ _) = typeName ++ "_" ++ cname

    generateCtorFunc typeName (Can.Ctor cname idx arity _) =
        if arity == 0
        then GoIr.GoDeclVar (typeName ++ "_" ++ cname) typeName
            (Just (GoIr.GoStructLit typeName [("Tag", GoIr.GoIntLit idx)]))
        else GoIr.GoDeclFunc GoIr.GoFuncDecl
            { GoIr._gf_name = typeName ++ "_" ++ cname
            , GoIr._gf_typeParams = []
            , GoIr._gf_params = zipWith (\i _ -> GoIr.GoParam ("v" ++ show i) "any") [0::Int ..] [1..arity]
            , GoIr._gf_returnType = typeName
            , GoIr._gf_body = [GoIr.GoReturn (GoIr.GoStructLit typeName
                ([("Tag", GoIr.GoIntLit idx)] ++ [("Fields", GoIr.GoSliceLit "any" (map (\i -> GoIr.GoIdent ("v" ++ show i)) [0..arity-1]))]))]
            }


-- | Generate Go type declarations for record type aliases.
-- Record aliases become Go structs; records with function fields become Go interfaces.
generateAliasTypes :: Can.Module -> [GoIr.GoDecl]
generateAliasTypes canMod =
    let userDefinedNames = collectDeclNames (Can._decls canMod)
    in concatMap (generateAlias userDefinedNames) (Map.toList (Can._aliases canMod))
  where
    generateAlias userDefinedNames (name, Can.Alias _vars body) = case body of
        T.TRecord fields _ ->
            let fieldList = Map.toList fields
                hasMethods = any (\(_, T.FieldType _ ty) -> isFuncType ty) fieldList
            in if hasMethods
                then generateInterface name fieldList
                else generateStruct userDefinedNames name fieldList
        _ ->
            [ GoIr.GoDeclRaw $ "type " ++ name ++ " = " ++ solvedTypeToGo body ]

    generateStruct userDefinedNames name fields =
        let structName = name ++ "_R"
            goFields = map (\(fname, T.FieldType _ ftype) ->
                (capitalise fname, solvedTypeToGo ftype)) fields
            paramList = zipWith (\i _ -> "p" ++ show i) [0::Int ..] fields
            paramDecls = intercalate_ ", " [p ++ " any" | p <- paramList]
            fieldInits =
                [ let goTy = solvedTypeToGo fty
                      src = "p" ++ show i
                      coerced = if goTy == "any" || null goTy
                                    then src
                                    else "any(" ++ src ++ ").(" ++ goTy ++ ")"
                  in capitalise_ fn ++ ": " ++ coerced
                | (i, (fn, T.FieldType _ fty)) <- zip [0::Int ..] fields
                ]
            ctorDecl = GoIr.GoDeclRaw $
                "func " ++ name ++ "(" ++ paramDecls ++ ") " ++ structName ++
                " { return " ++ structName ++ "{" ++ intercalate_ ", " fieldInits ++ "} }"
        in if Set.member name userDefinedNames
               then [ GoIr.GoDeclType structName (GoIr.GoStructDef goFields) ]
               else [ GoIr.GoDeclType structName (GoIr.GoStructDef goFields)
                    , ctorDecl
                    ]

    generateInterface name fields =
        let goMethods = map (\(fname, T.FieldType _ ftype) ->
                case ftype of
                    T.TLambda from to ->
                        let (params, ret) = collectFuncParams ftype
                            goParams = zipWith (\i p -> GoIr.GoParam ("p" ++ show i) (solvedTypeToGo p)) [0::Int ..] params
                        in (capitalise fname, goParams, solvedTypeToGo ret)
                    _ ->
                        -- Getter method
                        (capitalise fname, [], solvedTypeToGo ftype)
                ) fields
        in [ GoIr.GoDeclInterface name goMethods ]

    collectFuncParams (T.TLambda from to) =
        let (rest, ret) = collectFuncParams to
        in (from : rest, ret)
    collectFuncParams ty = ([], ty)

    isFuncType (T.TLambda _ _) = True
    isFuncType _ = False

    capitalise [] = []
    capitalise (c:cs) = toUpper c : cs
    toUpper c = if c >= 'a' && c <= 'z' then toEnum (fromEnum c - 32) else c


-- | Generate Go declarations from canonical decls
generateDecls :: Can.Module -> Solve.SolvedTypes -> [GoIr.GoDecl]
generateDecls canMod solvedTypes =
    -- DCE: compute transitive closure from main and only emit reachable defs.
    -- This shrinks binaries + speeds up `go build` for large projects.
    -- Disable with SKY_DCE=0 env var (checked at codegen time).
    let reachable = Dce.reachableTopLevel canMod
        dceEnabled = unsafePerformIO (fmap (/= "0") (lookupDceFlag))
    in declsToList reachable dceEnabled (Can._decls canMod) []
  where
    declsToList _ _ Can.SaveTheEnvironment acc = acc
    declsToList reachable dce (Can.Declare def rest) acc =
        declsToList reachable dce rest (acc ++ generateDefMaybe reachable dce def solvedTypes)
    declsToList reachable dce (Can.DeclareRec def defs rest) acc =
        let these = generateDefMaybe reachable dce def solvedTypes
                 ++ concatMap (\d -> generateDefMaybe reachable dce d solvedTypes) defs
        in declsToList reachable dce rest (acc ++ these)


-- | Emit def only if reachable (or DCE disabled).
generateDefMaybe :: Set.Set String -> Bool -> Can.Def -> Solve.SolvedTypes -> [GoIr.GoDecl]
generateDefMaybe reachable dceEnabled def solvedTypes =
    let name = case def of
            Can.Def (A.At _ n) _ _           -> n
            Can.TypedDef (A.At _ n) _ _ _ _  -> n
    in if not dceEnabled || Set.member name reachable || name == "main"
        then generateDef def solvedTypes
        else []


-- | Read SKY_DCE env var once. Default: enabled.
lookupDceFlag :: IO String
lookupDceFlag = do
    mv <- System.Environment.lookupEnv "SKY_DCE"
    return (maybe "1" id mv)


-- | Generate Go for a single definition, using solved types for signatures
generateDef :: Can.Def -> Solve.SolvedTypes -> [GoIr.GoDecl]
generateDef def solvedTypes =
    let (name, params, body) = case def of
            Can.Def (A.At _ n) pats expr -> (n, pats, expr)
            Can.TypedDef (A.At _ n) _ typedPats expr _ ->
                (n, map fst typedPats, expr)

        -- All functions use any params and any return for Go compatibility.
        -- Typed codegen uses type assertions internally for direct operators.
        -- The solved types drive the INTERNAL codegen, not the function signature.
        mSolvedType = Map.lookup name solvedTypes
        goParams = map patternToParam params
        goRetType = "any"
        isTyped = case mSolvedType of
            Just funcType ->
                let (argTypes, retType) = splitFuncType (length params) funcType
                in length argTypes == length params
                    && solvedTypeToGo retType /= "any"
                    && all (\t -> solvedTypeToGo t /= "any") argTypes
            Nothing -> False
    in
    -- Skip "main" — handled separately
    if name == "main" then []
    else
        let bodyExpr = if isTyped
                then exprToGoTypedWithRet solvedTypes goRetType body
                else exprToGo body
        in
        [ GoIr.GoDeclFunc GoIr.GoFuncDecl
            { GoIr._gf_name = goSafeName name
            , GoIr._gf_typeParams = []
            , GoIr._gf_params = goParams
            , GoIr._gf_returnType = goRetType
            , GoIr._gf_body = [GoIr.GoReturn bodyExpr]
            }
        ]


-- | Escape Sky identifiers that collide with Go reserved/builtin names.
-- Only applies to top-level Sky functions emitted as Go funcs.
goSafeName :: String -> String
goSafeName n
    | n `elem` reservedGoNames = n ++ "_"
    | otherwise = n


-- | Sky convention: identifiers starting with `_` mean the value is unused.
-- In Go this must be represented as the blank identifier to avoid "declared and not used".
isDiscardName :: String -> Bool
isDiscardName ('_':_) = True
isDiscardName _       = False


reservedGoNames :: [String]
reservedGoNames =
    [ "init"      -- Go's package init has special semantics
    , "new", "make", "len", "cap", "copy", "append", "delete"
    , "panic", "recover", "print", "println"
    , "type", "func", "var", "const", "interface", "struct"
    , "map", "chan", "go", "defer", "goto", "fallthrough"
    , "range", "return", "for", "switch", "case", "default"
    , "break", "continue", "import", "package", "select"
    ]


-- | Generate typed function parameters and return type from a solved type
typedFuncSig :: [Can.Pattern] -> T.Type -> ([GoIr.GoParam], String)
typedFuncSig params funcType =
    let (argTypes, retType) = splitFuncType (length params) funcType
        goParams = zipWith (\pat ty ->
            GoIr.GoParam (patternName pat) (GoType.typeToGo ty))
            params argTypes
    in (goParams, GoType.typeToGo retType)


-- | Split a function type into argument types and return type
splitFuncType :: Int -> T.Type -> ([T.Type], T.Type)
splitFuncType 0 ty = ([], ty)
splitFuncType n (T.TLambda from to) =
    let (rest, ret) = splitFuncType (n - 1) to
    in (from : rest, ret)
splitFuncType _ ty = ([], ty)  -- not enough arrows, return as-is


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
        -- For cross-module references, prefix with module name.
        -- Zero-arg top-level values are emitted as functions, so references must call them.
        let modStr = ModuleName.toString home
            qualName = if null modStr || modStr == "Main"
                then goSafeName name
                else map (\c -> if c == '.' then '_' else c) modStr ++ "_" ++ goSafeName name
            env = getCgEnv
            isZeroArg = Set.member name (Rec._cg_zeroArgs env)
        in if isZeroArg
            then GoIr.GoCall (GoIr.GoIdent qualName) []
            else GoIr.GoIdent qualName

    Can.VarKernel modName funcName ->
        kernelToGo modName funcName

    Can.VarCtor opts home typeName ctorName annot ->
        ctorToGo opts home typeName ctorName annot

    Can.List items ->
        GoIr.GoSliceLit "any" (map exprToGo items)

    Can.Negate inner ->
        -- For literal negation, use direct Go negative literal
        case inner of
            A.At _ (Can.Int n) -> GoIr.GoIntLit (-n)
            A.At _ (Can.Float f) -> GoIr.GoFloatLit (-f)
            _ -> GoIr.GoCall (GoIr.GoQualified "rt" "Negate") [exprToGo inner]

    Can.Binop op opHome opName _annot left right ->
        binopToGo op left right

    Can.Lambda params body ->
        -- Generate curried function: \a b -> body becomes func(a any) any { return func(b any) any { return body } }
        curryLambda (map patternToParam params) (exprToGo body)

    Can.Call func args ->
        case A.toValue func of
            Can.VarCtor _opts _home _typeName _ctorName annot ->
                -- ADT constructor partial app: JobDone : Int -> Result -> Msg
                -- applied to just `jid` must close over jid.
                let declared = ctorArity annot
                    got = length args
                in if got < declared
                    then emitPartialCtor func args (declared - got)
                    else GoIr.GoCall (exprToGo func) (map exprToGo args)
            Can.VarTopLevel home name ->
                -- Partial application of a top-level function:
                -- `canViewMonitor session` where canViewMonitor : Session -> Monitor -> Bool
                -- must yield a closure capturing session.
                let env = getCgEnv
                    modStr = ModuleName.toString home
                    qualName = if null modStr || modStr == "Main"
                        then name
                        else map (\c -> if c == '.' then '_' else c) modStr ++ "_" ++ name
                    declared = Map.findWithDefault (length args) qualName (Rec._cg_funcArities env)
                    got = length args
                in if got < declared && declared > 0
                    then emitPartialUserCall func args (declared - got)
                    else GoIr.GoCall (exprToGo func) (map exprToGo args)
            _ ->
                let goFunc = exprToGo func
                    goArgs = map exprToGo args
                in if isDirectCallable func
                    then GoIr.GoCall goFunc goArgs
                    else GoIr.GoCall (GoIr.GoQualified "rt" "SkyCall")
                                    (goFunc : goArgs)

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
        -- Record accessor function: .field → func(r any) any { return rt.Field(r, "Field") }
        GoIr.GoFuncLit [GoIr.GoParam "__r" "any"] "any"
            [GoIr.GoReturn (GoIr.GoCall (GoIr.GoQualified "rt" "Field") [GoIr.GoIdent "__r", GoIr.GoStringLit (capitalise_ field)])]

    Can.Access target (A.At _ field) ->
        -- Record field access via reflect-based runtime helper
        GoIr.GoCall (GoIr.GoQualified "rt" "Field") [exprToGo target, GoIr.GoStringLit (capitalise_ field)]

    Can.Update _name baseExpr fields ->
        -- Record update via reflect-based runtime helper (works on any + typed structs)
        let baseGo = GoBuilder.renderExpr (exprToGo baseExpr)
            fieldUpdates = Map.toList fields
            pairs = map (\(fname, Can.FieldUpdate _ fexpr) ->
                "\"" ++ capitalise_ fname ++ "\": " ++ GoBuilder.renderExpr (exprToGo fexpr))
                fieldUpdates
        in GoIr.GoRaw $ "rt.RecordUpdate(" ++ baseGo ++ ", map[string]any{" ++
            intercalate_ ", " pairs ++ "})"

    Can.Record fields ->
        -- Record literal: look up matching type alias → named struct, or anonymous
        let entries = Map.toList fields
            fieldNames = map fst entries
            env = getCgEnv
        in case Rec.lookupRecordAlias (Rec._cg_fieldIndex env) fieldNames of
            Just aliasName ->
                -- Named struct: Alias_R{Name: "Alice", Age: 30}
                let structName = aliasName ++ "_R"
                    fieldTypeMap = case Map.lookup aliasName (Rec._cg_aliases env) of
                        Just (Can.Alias _ (T.TRecord m _)) ->
                            Map.map (\(T.FieldType _ ty) -> solvedTypeToGo ty) m
                        _ -> Map.empty
                in GoIr.GoStructLit structName
                    [ (capitalise_ fn, coerceToFieldType (Map.findWithDefault "any" fn fieldTypeMap) (exprToGo fe))
                    | (fn, fe) <- entries
                    ]
            Nothing ->
                -- Anonymous struct
                let fieldDecls = intercalate_ "; " (map (\(fn, _) -> capitalise_ fn ++ " any") entries)
                    fieldInits = intercalate_ ", " (map (\(fn, fe) -> capitalise_ fn ++ ": " ++ GoBuilder.renderExpr (exprToGo fe)) entries)
                in GoIr.GoRaw $ "struct{ " ++ fieldDecls ++ " }{" ++ fieldInits ++ "}"

    Can.Tuple a b mC ->
        case mC of
            Nothing -> GoIr.GoStructLit "rt.SkyTuple2" [("V0", exprToGo a), ("V1", exprToGo b)]
            Just c -> GoIr.GoStructLit "rt.SkyTuple3" [("V0", exprToGo a), ("V1", exprToGo b), ("V2", exprToGo c)]


-- ═══════════════════════════════════════════════════════════
-- KERNEL FUNCTION RESOLUTION
-- ═══════════════════════════════════════════════════════════

-- | Map a kernel function to its Go equivalent
-- Zero-arity kernel functions are called immediately (Dict.empty → rt.Dict_empty())
kernelToGo :: String -> String -> GoIr.GoExpr
kernelToGo modName funcName =
    case Kernel.lookup modName funcName of
        Just ki ->
            let goExpr = if Kernel._ki_typed ki
                    then GoIr.GoIdent (Kernel._ki_goName ki ++ genericParams modName funcName)
                    else GoIr.GoIdent (Kernel._ki_goName ki)
            in if Kernel._ki_arity ki == 0
                then GoIr.GoCall goExpr []  -- zero-arity: call immediately
                else goExpr
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
-- | Count the number of `->` arrows in a Forall-wrapped type — that's the
-- arity of the constructor. For `Just : a -> Maybe a` this is 1. For
-- `JobDone : Int -> Result String String -> Msg` this is 2.
-- | Coerce an expression to a target Go type for struct-field assignment.
-- When the target is `any` (or unknown), pass through. Otherwise wrap as
-- `any(expr).(TargetType)` which is safe across concrete and any-typed sources.
coerceToFieldType :: String -> GoIr.GoExpr -> GoIr.GoExpr
coerceToFieldType targetTy e
    | targetTy == "any" || null targetTy = e
    | otherwise =
        GoIr.GoTypeAssert (GoIr.GoCall (GoIr.GoIdent "any") [e]) targetTy


-- | Can we emit a direct Go call for this callee expression?
-- Direct: kernel funcs, ADT constructors, top-level funcs (all are real Go funcs).
-- Indirect (wrap with rt.SkyCall): local vars, field accesses, expression results —
-- these are any-typed at runtime and Go forbids calling them directly.
isDirectCallable :: Can.Expr -> Bool
isDirectCallable (A.At _ e) = case e of
    Can.VarKernel _ _      -> True
    Can.VarCtor{}          -> True
    Can.VarTopLevel _ _    -> True
    Can.Lambda _ _         -> True
    _                      -> False


ctorArity :: Can.Annotation -> Int
ctorArity (Can.Forall _ t) = countArrows t
  where
    countArrows (T.TLambda _ r) = 1 + countArrows r
    countArrows _ = 0


-- | Emit a lambda that supplies the already-collected args then takes the
-- remaining `missing` args one at a time and calls the constructor.
emitPartialCtor :: Can.Expr -> [Can.Expr] -> Int -> GoIr.GoExpr
emitPartialCtor func suppliedArgs missing =
    let suppliedGo = map exprToGo suppliedArgs
        -- We need fresh parameter names for the missing args.
        extraNames = [ "__p" ++ show i | i <- [0 .. missing - 1] ]
        extraIdents = map GoIr.GoIdent extraNames
        finalCall = GoIr.GoCall (exprToGo func) (suppliedGo ++ extraIdents)
    in foldr wrapLambda finalCall extraNames
  where
    wrapLambda name body =
        GoIr.GoFuncLit [GoIr.GoParam name "any"] "any"
            [GoIr.GoReturn body]


-- | Partial application of a user-defined top-level function: wrap the
-- call in a chain of `func(x any) any { return callee(... , x, ...) }`
-- lambdas binding the remaining parameters.
emitPartialUserCall :: Can.Expr -> [Can.Expr] -> Int -> GoIr.GoExpr
emitPartialUserCall func suppliedArgs missing =
    let suppliedGo = map exprToGo suppliedArgs
        extraNames = [ "__pp" ++ show i | i <- [0 .. missing - 1] ]
        extraIdents = map GoIr.GoIdent extraNames
        finalCall = GoIr.GoCall (exprToGo func) (suppliedGo ++ extraIdents)
    in foldr wrapLambda finalCall extraNames
  where
    wrapLambda name body =
        GoIr.GoFuncLit [GoIr.GoParam name "any"] "any"
            [GoIr.GoReturn body]


ctorToGo :: Can.CtorOpts -> ModuleName.Canonical -> String -> String -> Can.Annotation -> GoIr.GoExpr
ctorToGo _opts home typeName ctorName _annot = case ctorName of
    "Ok"      -> GoIr.GoIdent "rt.Ok[any, any]"
    "Err"     -> GoIr.GoIdent "rt.Err[any, any]"
    "Just"    -> GoIr.GoIdent "rt.Just[any]"
    "Nothing" -> GoIr.GoCall (GoIr.GoIdent "rt.Nothing[any]") []
    "True"    -> GoIr.GoBoolLit True
    "False"   -> GoIr.GoBoolLit False
    -- User-defined constructor: prefix with module path for cross-module
    -- references. `generateDeclsForDep` emits ctors as `<ModPath>_<Type>_<Ctor>`
    -- so a constructor from State.sky for type Page becomes State_Page_BoardPage.
    _ ->
        let modStr = ModuleName.toString home
        in if null modStr || modStr == "Main"
            then GoIr.GoIdent (typeName ++ "_" ++ ctorName)
            else
                let modPrefix = map (\c -> if c == '.' then '_' else c) modStr
                in GoIr.GoIdent (modPrefix ++ "_" ++ typeName ++ "_" ++ ctorName)


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
    "//" -> GoIr.GoCall (GoIr.GoQualified "rt" "IntDiv") [exprToGo left, exprToGo right]

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
        if name == "_"
        then [GoIr.GoAssign "_" (exprToGo body)]
        else [ GoIr.GoShortDecl name (exprToGo body)
             , GoIr.GoAssign "_" (GoIr.GoIdent name)  -- suppress unused errors
             ]

    Can.Def (A.At _ name) params body ->
        let goParams = map patternToParam params
        in [ GoIr.GoShortDecl name
                (GoIr.GoFuncLit goParams "any" [GoIr.GoReturn (exprToGo body)])
           , GoIr.GoAssign "_" (GoIr.GoIdent name)
           ]

    Can.TypedDef (A.At _ name) _ [] body _ ->
        [ GoIr.GoShortDecl name (exprToGo body)
        , GoIr.GoAssign "_" (GoIr.GoIdent name)
        ]

    Can.TypedDef (A.At _ name) _ typedPats body _ ->
        let goParams = map (patternToParam . fst) typedPats
        in [ GoIr.GoShortDecl name
                (GoIr.GoFuncLit goParams "any" [GoIr.GoReturn (exprToGo body)])
           , GoIr.GoAssign "_" (GoIr.GoIdent name)
           ]


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
    patternGoType (Can.PCtor home typeName union ctorName _ _)
        | ctorName == "Ok" || ctorName == "Err" = Just "rt.SkyResult[any, any]"
        | ctorName == "Just" || ctorName == "Nothing" = Just "rt.SkyMaybe[any]"
        | Can._u_opts union == Can.Enum = Nothing  -- Enum: compare int directly
        | otherwise =
            -- Qualify with the home-module prefix so cross-module ADT
            -- assertions reference the dep-emitted struct type.
            let modStr = ModuleName.toString home
            in Just $ if null modStr || modStr == "Main"
                then typeName
                else map (\c -> if c == '.' then '_' else c) modStr ++ "_" ++ typeName
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

    Can.PCtor home typeName union ctorName ctorIdx args ->
        case Can._u_opts union of
            Can.Enum ->
                -- Enum: compare int value directly
                let modStr = ModuleName.toString home
                    qualName = if null modStr || modStr == "Main"
                        then typeName ++ "_" ++ ctorName
                        else (map (\c -> if c == '.' then '_' else c) modStr)
                             ++ "_" ++ typeName ++ "_" ++ ctorName
                in Just $ GoIr.GoBinary "=="
                    (GoIr.GoIdent subject)
                    (GoIr.GoIdent qualName)
            _ ->
                -- Tagged struct: match on .Tag field
                Just $ GoIr.GoBinary "=="
                    (GoIr.GoSelector (GoIr.GoIdent subject) "Tag")
                    (GoIr.GoIntLit ctorIdx)

    Can.PUnit -> Nothing  -- always matches

    -- Cons: match non-empty list, len(subject.([]any)) >= 1
    Can.PCons _ _ ->
        Just $ GoIr.GoBinary ">="
            (GoIr.GoCall (GoIr.GoIdent "len")
                [ GoIr.GoTypeAssert (GoIr.GoIdent subject) "[]any" ])
            (GoIr.GoIntLit 1)

    -- Fixed-length list: match exact length; element conditions handled in
    -- bindings below (codegen over-matches conservatively — strict element
    -- matching would need nested if-cascades we don't model in a single cond).
    Can.PList xs ->
        Just $ GoIr.GoBinary "=="
            (GoIr.GoCall (GoIr.GoIdent "len")
                [ GoIr.GoTypeAssert (GoIr.GoIdent subject) "[]any" ])
            (GoIr.GoIntLit (length xs))

    -- Tuples, records, aliases: structure is guaranteed by HM — bindings carry the work.
    Can.PTuple _ _ _ -> Nothing
    Can.PRecord _    -> Nothing
    Can.PAlias inner _ ->
        let (A.At _ innerPat) = inner
        in patternCondition subject innerPat


-- | Generate Go variable bindings from a pattern
patternBindings :: String -> Can.Pattern_ -> [GoIr.GoStmt]
patternBindings subject pat = case pat of
    Can.PVar name ->
        if isDiscardName name
            then [ GoIr.GoAssign "_" (GoIr.GoIdent subject) ]
            else [ GoIr.GoShortDecl name (GoIr.GoIdent subject)
                 , GoIr.GoAssign "_" (GoIr.GoIdent name)
                 ]

    Can.PAnything -> []
    Can.PUnit -> []
    Can.PInt _ -> []
    Can.PStr _ -> []
    Can.PBool _ -> []
    Can.PChr _ -> []

    Can.PCtor _home typeName _union ctorName _ctorIdx args ->
        -- Bind constructor arguments
        concatMap (bindCtorArg subject ctorName) args

    -- head :: tail  →  h := subject.([]any)[0]; t := subject.([]any)[1:]
    Can.PCons h t ->
        let asSlice = GoIr.GoTypeAssert (GoIr.GoIdent subject) "[]any"
            (A.At _ hPat) = h
            (A.At _ tPat) = t
            headExpr = GoIr.GoIndex asSlice (GoIr.GoIntLit 0)
            tailExpr = GoIr.GoRaw (subject ++ ".([]any)[1:]")
            headName = "__sky_h_" ++ subject
            tailName = "__sky_t_" ++ subject
            headStmts = case hPat of
                Can.PVar name ->
                    if isDiscardName name
                        then [ GoIr.GoAssign "_" headExpr ]
                        else [ GoIr.GoShortDecl name headExpr ]
                Can.PAnything -> []
                _ -> GoIr.GoShortDecl headName headExpr : patternBindings headName hPat
            tailStmts = case tPat of
                Can.PVar name ->
                    if isDiscardName name
                        then [ GoIr.GoAssign "_" tailExpr ]
                        else [ GoIr.GoShortDecl name tailExpr ]
                Can.PAnything -> []
                _ -> GoIr.GoShortDecl tailName tailExpr : patternBindings tailName tPat
        in headStmts ++ tailStmts

    -- [a, b, c]  →  bind each element by index
    Can.PList xs ->
        let asSlice suf = GoIr.GoRaw (subject ++ ".([]any)[" ++ show suf ++ "]")
            bindEl i (A.At _ p) = case p of
                Can.PVar name ->
                    if isDiscardName name
                        then [ GoIr.GoAssign "_" (asSlice i) ]
                        else [ GoIr.GoShortDecl name (asSlice i) ]
                Can.PAnything -> []
                _ ->
                    let sub = "__sky_li_" ++ show i ++ "_" ++ subject
                    in GoIr.GoShortDecl sub (asSlice i) : patternBindings sub p
        in concat (zipWith bindEl [0::Int ..] xs)

    -- (a, b) or (a, b, c)  →  bind V0, V1[, V2] off SkyTuple2/3
    Can.PTuple aPat bPat mcPat ->
        let tupleKind = case mcPat of Just _ -> "SkyTuple3"; Nothing -> "SkyTuple2"
            asTup = GoIr.GoTypeAssert (GoIr.GoIdent subject) ("rt." ++ tupleKind)
            bindField fld (A.At _ p) = case p of
                Can.PVar name ->
                    if isDiscardName name
                        then [ GoIr.GoAssign "_" (GoIr.GoSelector asTup fld) ]
                        else [ GoIr.GoShortDecl name (GoIr.GoSelector asTup fld) ]
                Can.PAnything -> []
                _ ->
                    let sub = "__sky_t_" ++ fld ++ "_" ++ subject
                    in GoIr.GoShortDecl sub (GoIr.GoSelector asTup fld)
                       : patternBindings sub p
            pieces = bindField "V0" aPat ++ bindField "V1" bPat
            extra = case mcPat of
                Just c -> bindField "V2" c
                Nothing -> []
        in pieces ++ extra

    -- { name }  →  name := rt.Field(subject, "Name")
    Can.PRecord fields ->
        [ GoIr.GoShortDecl f
            (GoIr.GoCall (GoIr.GoQualified "rt" "Field")
                [ GoIr.GoIdent subject
                , GoIr.GoStringLit (capitalise_ f)
                ])
        | f <- fields
        ]

    -- `(PCons h t) as whole`  →  bind whole := subject, then recurse into inner
    Can.PAlias inner name ->
        let (A.At _ innerPat) = inner
            aliasStmt = if isDiscardName name
                then [ GoIr.GoAssign "_" (GoIr.GoIdent subject) ]
                else [ GoIr.GoShortDecl name (GoIr.GoIdent subject) ]
        in aliasStmt ++ patternBindings subject innerPat


-- | Bind a constructor argument to a local variable
bindCtorArg :: String -> String -> Can.PatternCtorArg -> [GoIr.GoStmt]
bindCtorArg subject ctorName (Can.PatternCtorArg idx _ty pat) =
    let (A.At _ innerPat) = pat
        fieldAccess = case ctorName of
            "Ok"   -> GoIr.GoSelector (GoIr.GoIdent subject) "OkValue"
            "Err"  -> GoIr.GoSelector (GoIr.GoIdent subject) "ErrValue"
            "Just" -> GoIr.GoSelector (GoIr.GoIdent subject) "JustValue"
            _      -> GoIr.GoIndex
                        (GoIr.GoSelector (GoIr.GoIdent subject) "Fields")
                        (GoIr.GoIntLit idx)
    in case innerPat of
        Can.PVar name ->
            if isDiscardName name
                then [ GoIr.GoAssign "_" fieldAccess ]
                else
                    -- Bind + discard-sink so Go doesn't error on unused when
                    -- the case body doesn't reference the binding.
                    [ GoIr.GoShortDecl name fieldAccess
                    , GoIr.GoAssign "_" (GoIr.GoIdent name)
                    ]
        Can.PAnything -> []
        _ ->
            let tmp = "__sky_cf_" ++ show idx ++ "_" ++ subject
            in GoIr.GoShortDecl tmp fieldAccess : patternBindings tmp innerPat


-- ═══════════════════════════════════════════════════════════
-- MAIN FUNCTION
-- ═══════════════════════════════════════════════════════════

-- | Generate the main() function (uses solved types for typed codegen)
generateMainFunc :: Can.Module -> Src.Module -> Solve.SolvedTypes -> [GoIr.GoDecl]
generateMainFunc canMod srcMod solvedTypes =
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
                stmts = exprToMainStmtsTyped solvedTypes body
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


-- | Convert the main body to Go statements, using typed codegen where possible
exprToMainStmtsTyped :: Solve.SolvedTypes -> Can.Expr -> [GoIr.GoStmt]
exprToMainStmtsTyped types (A.At _ expr) = case expr of
    Can.Let def body ->
        defToStmts def ++ exprToMainStmtsTyped types body

    Can.LetRec defs body ->
        concatMap defToStmts defs ++ exprToMainStmtsTyped types body

    Can.LetDestruct _pat valExpr body ->
        [GoIr.GoExprStmt (exprToGoMain types valExpr)] ++ exprToMainStmtsTyped types body

    -- Calls are valid Go expression statements, emit bare
    Can.Call _ _ ->
        [GoIr.GoExprStmt (exprToGoMain types (A.At A.one expr))]

    -- Non-call values (e.g. literals, vars): Go rejects bare expression
    -- statements that aren't calls, so discard via blank assignment.
    _ ->
        [GoIr.GoAssign "_" (exprToGoMain types (A.At A.one expr))]


-- | Generate Go for main body expressions — uses typed path for function calls
-- that target typed functions, any-typed for everything else
exprToGoMain :: Solve.SolvedTypes -> Can.Expr -> GoIr.GoExpr
exprToGoMain types expr@(A.At _ inner) = case inner of
    -- For function calls: if the target function is fully typed,
    -- generate typed arguments
    Can.Call func args ->
        let goFunc = exprToGoMain types func
            goArgs = map (exprToGoMain types) args
        in GoIr.GoCall goFunc goArgs

    -- Negate: use direct Go negate if we can determine the type
    Can.Negate e -> GoIr.GoUnary "-" (exprToGoMain types e)

    -- Binop: use direct Go operators when possible
    Can.Binop op _ _ _ left right ->
        binopToGo op left right  -- reuse existing binop (still any-typed for main)

    -- Fall back to any-typed for everything else
    _ -> exprToGo expr


-- | Legacy untyped main stmts (kept for reference)
exprToMainStmts :: Can.Expr -> [GoIr.GoStmt]
exprToMainStmts = exprToMainStmtsTyped Map.empty


-- ═══════════════════════════════════════════════════════════
-- HELPERS
-- ═══════════════════════════════════════════════════════════

-- ═══════════════════════════════════════════════════════════
-- TYPED EXPRESSION CODEGEN
-- ═══════════════════════════════════════════════════════════

-- | Generate Go expression in typed context with known return type.
exprToGoTypedWithRet :: Solve.SolvedTypes -> String -> Can.Expr -> GoIr.GoExpr
exprToGoTypedWithRet types retType expr = exprToGoTyped types retType expr


-- | Generate Go expression in typed context — uses direct Go operators
-- instead of any-typed runtime wrappers.
exprToGoTyped :: Solve.SolvedTypes -> String -> Can.Expr -> GoIr.GoExpr
exprToGoTyped types retType (A.At _ expr) = case expr of
    Can.Int n -> GoIr.GoIntLit n
    Can.Float f -> GoIr.GoFloatLit f
    Can.Str s -> GoIr.GoStringLit s
    Can.Chr c -> GoIr.GoRuneLit c
    Can.Unit -> GoIr.GoRaw "struct{}{}"

    Can.VarLocal name ->
        -- If we have a solved type for this var and it's concrete, use type assertion
        case Map.lookup name types of
            Just ty | isConcreteType ty -> GoIr.GoTypeAssert (GoIr.GoIdent name) (solvedTypeToGo ty)
            _ -> GoIr.GoIdent name
    Can.VarTopLevel _ name -> GoIr.GoIdent (goSafeName name)
    Can.VarKernel modName funcName -> kernelToGo modName funcName

    Can.Binop op _ _ _ left right -> typedBinop types retType op left right
    Can.If branches elseExpr -> typedIf types retType branches elseExpr

    Can.Call func args ->
        let goFunc = exprToGoTyped types retType func
            goArgs = map (exprToGoTyped types retType) args
            callExpr = case func of
                A.At _ (Can.VarLocal name) ->
                    case Map.lookup name types of
                        Just (T.TLambda _ _) ->
                            GoIr.GoCall (GoIr.GoRaw (name ++ ".(func(any) any)")) goArgs
                        _ -> GoIr.GoCall goFunc goArgs
                _ -> GoIr.GoCall goFunc goArgs
            -- If the called function has a known return type and we need a primitive,
            -- assert the result. This handles: n * factorial(n-1) where factorial returns any
            funcRetType = case func of
                A.At _ (Can.VarLocal name) ->
                    case Map.lookup name types of
                        Just ft -> let (_, rt) = splitFuncType (length args) ft in Just rt
                        Nothing -> Nothing
                A.At _ (Can.VarTopLevel _ name) ->
                    case Map.lookup name types of
                        Just ft -> let (_, rt) = splitFuncType (length args) ft in Just rt
                        Nothing -> Nothing
                _ -> Nothing
        in case funcRetType of
            Just rt | isConcreteType rt -> GoIr.GoTypeAssert callExpr (solvedTypeToGo rt)
            _ -> callExpr

    Can.Negate inner -> GoIr.GoUnary "-" (exprToGoTyped types retType inner)

    Can.Lambda params body ->
        curryLambda (map patternToParam params) (exprToGoTyped types retType body)

    _ -> exprToGo (A.At A.one expr)


typedBinop :: Solve.SolvedTypes -> String -> String -> Can.Expr -> Can.Expr -> GoIr.GoExpr
typedBinop types retType op left right = case op of
    "|>" -> pipeApply left right
    "<|" -> pipeApply right left
    -- String concat: use rt.Concat which returns any, then assert to string if needed
    "++" -> let concatExpr = GoIr.GoCall (GoIr.GoQualified "rt" "Concat") [exprToGoTyped types retType left, exprToGoTyped types retType right]
            in if retType == "string"
               then GoIr.GoTypeAssert concatExpr "string"
               else concatExpr
    "/=" -> GoIr.GoBinary "!=" (exprToGoTyped types retType left) (exprToGoTyped types retType right)
    _ -> GoIr.GoBinary op (exprToGoTyped types retType left) (exprToGoTyped types retType right)


typedIf :: Solve.SolvedTypes -> String -> [(Can.Expr, Can.Expr)] -> Can.Expr -> GoIr.GoExpr
typedIf types retType branches elseExpr =
    let
        go [] = "return " ++ GoBuilder.renderExpr (exprToGoTyped types retType elseExpr)
        go ((cond, body):rest) =
            "if " ++ GoBuilder.renderExpr (exprToGoTyped types retType cond)
            ++ " { return " ++ GoBuilder.renderExpr (exprToGoTyped types retType body) ++ " }; "
            ++ go rest
    in
    GoIr.GoRaw $ "func() " ++ retType ++ " { " ++ go branches ++ " }()"


-- | Check if a type is assertable from any (has a known Go representation).
-- Only PRIMITIVE types can be safely asserted — function types can't because
-- the runtime representation is func(any) any, not func(int) int.
isConcreteType :: T.Type -> Bool
isConcreteType ty = case ty of
    T.TVar _ -> False
    T.TType _ name _ -> name `elem` ["Int", "Float", "Bool", "String", "Char"]
    T.TUnit -> True
    _ -> False  -- Functions, containers, etc. stay as any


-- | Convert a solved type to a Go type string.
-- Falls back to "any" for unresolved type variables.
solvedTypeToGo :: T.Type -> String
solvedTypeToGo ty = case ty of
    T.TVar name
        | head name == '_' -> "any"  -- unresolved internal variable
        | otherwise -> "any"         -- unresolved user variable (TODO: Go type param)
    T.TUnit -> "struct{}"
    T.TType _ "Int" [] -> "int"
    T.TType _ "Float" [] -> "float64"
    T.TType _ "Bool" [] -> "bool"
    T.TType _ "String" [] -> "string"
    T.TType _ "Char" [] -> "rune"
    -- Container types: stay as any at runtime (Go doesn't have covariant generics)
    -- The type checker validates element types but Go uses []any, rt.SkyResult[any,any] etc.
    T.TType _ "List" _ -> "any"  -- []any at runtime
    T.TType _ "Maybe" _ -> "any"  -- rt.SkyMaybe[any] at runtime
    T.TType _ "Result" _ -> "any"  -- rt.SkyResult[any,any] at runtime
    T.TType _ "Task" _ -> "any"  -- rt.SkyTask[any,any] at runtime
    T.TType _ "Dict" _ -> "any"  -- map[string]any at runtime
    T.TType _ "Set" _ -> "any"   -- map[any]bool at runtime
    T.TType home name _ ->
        let modStr = ModuleName.toString home
            base = if null modStr || modStr == "Main"
                then name
                else map (\c -> if c == '.' then '_' else c) modStr ++ "_" ++ name
            env = getCgEnv
            -- Record aliases live under "<base>_R" in Go (to avoid name
            -- collision with a user-defined constructor function).
            isRecordAlias = Set.member base (Rec._cg_recordAliases env)
                         || Set.member name (Rec._cg_recordAliases env)
        in if isRecordAlias then base ++ "_R" else base
    T.TLambda from to -> "func(" ++ solvedTypeToGo from ++ ") " ++ solvedTypeToGo to
    T.TRecord _ _ -> "any"  -- TODO: struct type
    T.TTuple _ _ _ -> "any"  -- TODO: tuple type
    T.TAlias home name _ aliasTy ->
        let modStr = ModuleName.toString home
            base = if null modStr || modStr == "Main"
                then name
                else map (\c -> if c == '.' then '_' else c) modStr ++ "_" ++ name
            isRecord = case aliasTy of
                T.Hoisted (T.TRecord _ _) -> True
                T.Filled  (T.TRecord _ _) -> True
                _ -> False
        in if isRecord then base ++ "_R" else base


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
    , "\t\"reflect\""
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
    , "func Log_println(args ...any) any {"
    , "\tfmt.Println(args...)"
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
    , "func List_take(n any, list any) any {"
    , "\tcount := AsInt(n)"
    , "\titems := list.([]any)"
    , "\tif count > len(items) { count = len(items) }"
    , "\treturn items[:count]"
    , "}"
    , ""
    , "func List_drop(n any, list any) any {"
    , "\tcount := AsInt(n)"
    , "\titems := list.([]any)"
    , "\tif count > len(items) { count = len(items) }"
    , "\treturn items[count:]"
    , "}"
    , ""
    , "func List_append(a any, b any) any {"
    , "\treturn append(a.([]any), b.([]any)...)"
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
    , "// Result operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Result_map(fn any, result any) any {"
    , "\tr := result.(SkyResult[any, any])"
    , "\tif r.Tag == 0 { return Ok[any, any](fn.(func(any) any)(r.OkValue)) }"
    , "\treturn result"
    , "}"
    , ""
    , "func Result_andThen(fn any, result any) any {"
    , "\tr := result.(SkyResult[any, any])"
    , "\tif r.Tag == 0 { return fn.(func(any) any)(r.OkValue) }"
    , "\treturn result"
    , "}"
    , ""
    , "func Result_withDefault(def any, result any) any {"
    , "\tr := result.(SkyResult[any, any])"
    , "\tif r.Tag == 0 { return r.OkValue }"
    , "\treturn def"
    , "}"
    , ""
    , "func Result_mapError(fn any, result any) any {"
    , "\tr := result.(SkyResult[any, any])"
    , "\tif r.Tag == 1 { return Err[any, any](fn.(func(any) any)(r.ErrValue)) }"
    , "\treturn result"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Maybe operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Maybe_withDefault(def any, maybe any) any {"
    , "\tm := maybe.(SkyMaybe[any])"
    , "\tif m.Tag == 0 { return m.JustValue }"
    , "\treturn def"
    , "}"
    , ""
    , "func Maybe_map(fn any, maybe any) any {"
    , "\tm := maybe.(SkyMaybe[any])"
    , "\tif m.Tag == 0 { return Just[any](fn.(func(any) any)(m.JustValue)) }"
    , "\treturn maybe"
    , "}"
    , ""
    , "func Maybe_andThen(fn any, maybe any) any {"
    , "\tm := maybe.(SkyMaybe[any])"
    , "\tif m.Tag == 0 { return fn.(func(any) any)(m.JustValue) }"
    , "\treturn maybe"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Record field access (reflect-based for any-typed params)"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Dict operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Dict_empty() any { return map[string]any{} }"
    , ""
    , "func Dict_insert(key any, val any, dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\tnew := make(map[string]any, len(m)+1)"
    , "\tfor k, v := range m { new[k] = v }"
    , "\tnew[fmt.Sprintf(\"%v\", key)] = val"
    , "\treturn new"
    , "}"
    , ""
    , "func Dict_get(key any, dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\tv, ok := m[fmt.Sprintf(\"%v\", key)]"
    , "\tif ok { return Just[any](v) }"
    , "\treturn Nothing[any]()"
    , "}"
    , ""
    , "func Dict_remove(key any, dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\tnew := make(map[string]any, len(m))"
    , "\tk := fmt.Sprintf(\"%v\", key)"
    , "\tfor kk, v := range m { if kk != k { new[kk] = v } }"
    , "\treturn new"
    , "}"
    , ""
    , "func Dict_member(key any, dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\t_, ok := m[fmt.Sprintf(\"%v\", key)]"
    , "\treturn ok"
    , "}"
    , ""
    , "func Dict_keys(dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\tresult := make([]any, 0, len(m))"
    , "\tfor k := range m { result = append(result, k) }"
    , "\treturn result"
    , "}"
    , ""
    , "func Dict_values(dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\tresult := make([]any, 0, len(m))"
    , "\tfor _, v := range m { result = append(result, v) }"
    , "\treturn result"
    , "}"
    , ""
    , "func Dict_toList(dict any) any {"
    , "\tm := dict.(map[string]any)"
    , "\tresult := make([]any, 0, len(m))"
    , "\tfor k, v := range m { result = append(result, SkyTuple2{V0: k, V1: v}) }"
    , "\treturn result"
    , "}"
    , ""
    , "func Dict_fromList(list any) any {"
    , "\titems := list.([]any)"
    , "\tresult := make(map[string]any, len(items))"
    , "\tfor _, item := range items {"
    , "\t\tt := item.(SkyTuple2)"
    , "\t\tresult[fmt.Sprintf(\"%v\", t.V0)] = t.V1"
    , "\t}"
    , "\treturn result"
    , "}"
    , ""
    , "func Dict_map(fn any, dict any) any {"
    , "\tf := fn.(func(any) any)"
    , "\tm := dict.(map[string]any)"
    , "\tresult := make(map[string]any, len(m))"
    , "\tfor k, v := range m { result[k] = f(v) }"
    , "\treturn result"
    , "}"
    , ""
    , "func Dict_foldl(fn any, acc any, dict any) any {"
    , "\tf := fn.(func(any) any)"
    , "\tm := dict.(map[string]any)"
    , "\tresult := acc"
    , "\tfor k, v := range m {"
    , "\t\tstep := f(k)"
    , "\t\tstep2 := step.(func(any) any)(v)"
    , "\t\tresult = step2.(func(any) any)(result)"
    , "\t}"
    , "\treturn result"
    , "}"
    , ""
    , "func Dict_union(a any, b any) any {"
    , "\tma := a.(map[string]any)"
    , "\tmb := b.(map[string]any)"
    , "\tresult := make(map[string]any, len(ma)+len(mb))"
    , "\tfor k, v := range mb { result[k] = v }"
    , "\tfor k, v := range ma { result[k] = v }"
    , "\treturn result"
    , "}"
    , ""
    , "// ═══════════════════════════════════════════════════════════"
    , "// Math operations"
    , "// ═══════════════════════════════════════════════════════════"
    , ""
    , "func Math_abs(n any) any { x := AsInt(n); if x < 0 { return -x }; return x }"
    , "func Math_min(a any, b any) any { if AsInt(a) < AsInt(b) { return a }; return b }"
    , "func Math_max(a any, b any) any { if AsInt(a) > AsInt(b) { return a }; return b }"
    , ""
    , "func Field(record any, field string) any {"
    , "\tv := reflect.ValueOf(record)"
    , "\tif v.Kind() == reflect.Ptr { v = v.Elem() }"
    , "\tif v.Kind() == reflect.Struct {"
    , "\t\tf := v.FieldByName(field)"
    , "\t\tif f.IsValid() { return f.Interface() }"
    , "\t}"
    , "\treturn nil"
    , "}"
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


-- | Capitalise a string (for Go export)
capitalise_ :: String -> String
capitalise_ [] = []
capitalise_ (c:cs) = (if c >= 'a' && c <= 'z' then toEnum (fromEnum c - 32) else c) : cs


-- | String intercalation helper
intercalate_ :: String -> [String] -> String
intercalate_ _ [] = ""
intercalate_ _ [x] = x
intercalate_ sep (x:xs) = x ++ sep ++ intercalate_ sep xs
