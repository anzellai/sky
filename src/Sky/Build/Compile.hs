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
import qualified System.Process
import qualified System.Exit
import Control.Monad (when)
import System.Directory (createDirectoryIfMissing, doesDirectoryExist, doesFileExist, copyFile, listDirectory)
import System.IO (hFlush, stdout, readFile', stderr, hPutStrLn)
import System.IO.Unsafe (unsafePerformIO)
import System.FilePath (takeDirectory, (</>))

import qualified Data.ByteString as BS
import Sky.Build.EmbeddedRuntime (embeddedRuntime, embeddedSkyStdlib)

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
import qualified Sky.Build.FfiRegistry as FfiReg
import qualified Sky.Build.SkyDeps as SkyDeps
import qualified Sky.Canonicalise.Environment as Env
import qualified System.Environment


-- | Global codegen environment (set once per compilation, read during codegen)
{-# NOINLINE globalCgEnv #-}
globalCgEnv :: IORef Rec.CodegenEnv
globalCgEnv = unsafePerformIO $ newIORef (Rec.CodegenEnv Map.empty Map.empty Map.empty Set.empty Set.empty Map.empty Map.empty Map.empty)

-- | Read the global codegen env (for use in pure codegen functions)
getCgEnv :: Rec.CodegenEnv
getCgEnv = unsafePerformIO $ readIORef globalCgEnv


-- | Read ffi/*.kernel.json and write the resulting module/function maps into
-- Env.ffiKernelModulesRef and Env.ffiKernelFunctionsRef. After this call the
-- pure kernelModules / kernelFunctions lookups include FFI entries.
loadAndSeedFfiRegistry :: IO ()
loadAndSeedFfiRegistry = do
    reg <- FfiReg.loadRegistry
    let mods = FfiReg._fr_modules reg
        moduleMap =
            Map.fromList [ (FfiReg._fm_moduleName m, FfiReg._fm_kernelName m) | m <- mods ]
        functionMap =
            Map.fromListWith (++)
                [ (FfiReg._fm_kernelName m,
                   map FfiReg._ffn_name (FfiReg._fm_functions m))
                | m <- mods
                ]
    writeIORef Env.ffiKernelModulesRef moduleMap
    writeIORef Env.ffiKernelFunctionsRef functionMap
    if null mods
        then return ()
        else putStrLn $ "-- Loaded " ++ show (length mods) ++ " FFI module(s)"


-- | Full compilation: parse → canonicalise → codegen → write Go
compile :: Toml.SkyConfig -> FilePath -> FilePath -> IO (Either String FilePath)
compile config entryPath outDir = do
    -- Compute source root relative to the entry file
    let entryDir = takeDirectory entryPath
        sourceRoot = if Toml._sourceRoot config == "src"
            then entryDir  -- entry IS in the source root
            else Toml._sourceRoot config

    -- Phase 0: Load FFI registry (ffi/*.kernel.json) and seed the kernel
    -- module/function IORefs so FFI packages resolve as first-class kernels.
    loadAndSeedFfiRegistry

    -- Phase 0b: Install Sky-source dependencies declared in [dependencies].
    -- Each dep contributes an extra source root that discovery will probe
    -- in order after the primary project source root.
    depRoots <- SkyDeps.installDeps (Toml._skyDeps config)

    -- Phase 0c: Materialise the embedded Sky stdlib (Std.IoError, etc.)
    -- into outDir/.sky-stdlib/ and add it as a discovery root so
    -- `import Std.IoError` resolves with no user setup. Stdlib lives
    -- LAST in the root list so a user's local Std/* override wins.
    stdlibRoot <- writeEmbeddedSkyStdlib outDir

    -- Phase 1: Discover all modules
    putStrLn "-- Discovering modules"
    modules <- Graph.discoverModulesMulti (sourceRoot : depRoots ++ [stdlibRoot]) entryPath
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
                -- Strict read so the handle closes before the later
                -- writeFile (line 259) tries to re-open the same file.
                -- Lazy readFile left the handle open, breaking `sky check`
                -- on CI runners where the next step invoked sky again.
                cached <- readFile' hashFile
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
                Right cm -> return (Right (n, cm))
                Left err -> return (Left (n, err))
        let validDeps = [x | Right x <- depCanMods]
            depErrors = [(n, err) | Left (n, err) <- depCanMods]

        -- If any dep failed to canonicalise, fail the build with the first
        -- error so users see actionable messages (e.g. ambiguous imports)
        -- rather than a downstream "undefined" Go error.
        case depErrors of
         ((n, err):_) ->
            return (Left $ "Canonicalise error in " ++ n ++ ":\n" ++ err)
         [] ->
          case Canonicalise.canonicaliseWithDeps depInfoMap entrySrcMod of
           Left err -> return (Left $ "Canonicalise error: " ++ err)
           Right canMod -> do
            putStrLn "   Names resolved"
            -- T2/T6: prime the global codegen env's function-type
            -- tables BEFORE dep-decl emission, so call-site codegen
            -- in dep bodies (Can.Call → coerceCallArgs) can also see
            -- typed param types for cross-module calls.
            let earlyAllRecAliases = Set.unions
                    [ Set.union
                        (Rec.collectRecordAliases (Can._aliases m))
                        (Set.map (\n -> p ++ "_" ++ n)
                                 (Rec.collectRecordAliases (Can._aliases m)))
                    | (mn, m) <- validDeps
                    , let p = map (\c -> if c == '.' then '_' else c) mn
                    ] `Set.union`
                    Rec.collectRecordAliases (Can._aliases canMod)
                earlyDepParamTypes = Map.unions
                    [ fst (collectFuncTypesWith earlyAllRecAliases prefix depMod)
                    | (modName, depMod) <- validDeps
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    ]
                earlyDepRetTypes = Map.unions
                    [ snd (collectFuncTypesWith earlyAllRecAliases prefix depMod)
                    | (modName, depMod) <- validDeps
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    ]
                (earlyEntryParams, earlyEntryRet) =
                    collectFuncTypesWith earlyAllRecAliases "" canMod
            modifyIORef globalCgEnv $ \e -> e
                { Rec._cg_funcParamTypes =
                    Map.union earlyEntryParams earlyDepParamTypes
                , Rec._cg_funcRetType =
                    Map.union earlyEntryRet earlyDepRetTypes
                }
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
                -- T2/T6: collect typed param + return signatures from
                -- every dep module's annotated declarations. Names are
                -- module-prefixed (Lib_Db_exec) to match the call-site
                -- emission convention. Uses the merged record-alias
                -- set so cross-module record types resolve.
                depParamTypes = Map.unions
                    [ fst (collectFuncTypesWith earlyAllRecAliases prefix depMod)
                    | (modName, depMod) <- validDeps
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    ]
                depRetTypes = Map.unions
                    [ snd (collectFuncTypesWith earlyAllRecAliases prefix depMod)
                    | (modName, depMod) <- validDeps
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    ]
            putStrLn "-- Type Checking"
            -- Run HM on each dep module so unannotated functions get
            -- inferred types for the typed-codegen tables. Errors in a
            -- dep don't block the entry — we degrade to `any` for that
            -- module's bindings.
            depSolved <- Async.forConcurrently validDeps $ \(modName, depMod) -> do
                cs <- Constrain.constrainModule depMod
                r  <- Solve.solve cs
                case r of
                    Solve.SolveOk t -> return (modName, t)
                    Solve.SolveError _ -> return (modName, Map.empty)
            constraints <- Constrain.constrainModule canMod
            solveResult <- Solve.solve constraints
            types <- case solveResult of
                Solve.SolveOk types -> do
                    putStrLn $ "   Types OK (" ++ show (length (Map.keys types)) ++ " bindings)"
                    return types
                Solve.SolveError err -> do
                    putStrLn $ "   TYPE WARNING: " ++ err
                    return Map.empty
            -- Merge inferred dep types into the param + return tables
            -- keyed by module-prefixed Go names. Annotation-derived
            -- entries already in the tables win over inferred ones
            -- (annotations represent the user's declared contract).
            let depInferredParams = Map.unions
                    [ Map.fromList
                        [ ( prefix ++ "_" ++ n
                          , splitInferredParams (countParamsFor n depMod) ty )
                        | (n, ty) <- Map.toList depTypes
                        ]
                    | (modName, depTypes) <- depSolved
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    , let depMod = head [ m | (mn, m) <- validDeps, mn == modName ]
                    ]
                depInferredRets = Map.unions
                    [ Map.fromList
                        [ ( prefix ++ "_" ++ n
                          , inferredReturnFor (countParamsFor n depMod) ty )
                        | (n, ty) <- Map.toList depTypes
                        ]
                    | (modName, depTypes) <- depSolved
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    , let depMod = head [ m | (mn, m) <- validDeps, mn == modName ]
                    ]
            putStrLn $ "   HM infer (deps): "
                ++ show (Map.size depInferredParams) ++ " functions typed"
            modifyIORef globalCgEnv $ \e -> e
                { Rec._cg_funcParamTypes =
                    Map.union (Rec._cg_funcParamTypes e) depInferredParams
                , Rec._cg_funcRetType =
                    Map.union (Rec._cg_funcRetType e) depInferredRets
                }
            putStrLn "-- Generating Go"
            let goCode = generateGoMulti canMod entrySrcMod config types depDecls depRecAliases depArities depParamTypes depRetTypes depInferredParams depInferredRets
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
            -- Pull in Go deps declared in sky.toml so generated ffi/*_bindings.go
            -- can resolve imports.
            seedGoDependencies outDir (Toml._goDeps config)
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
-- | Run `go get <pkg>[@<ver>]` for each Go dependency declared in sky.toml.
-- Runs after runtime + ffi copy so imports in generated ffi/*_bindings.go
-- resolve before the final `go build`. Skipped stdlib pkgs (no slash).
seedGoDependencies :: FilePath -> [(String, String)] -> IO ()
seedGoDependencies outDir deps = do
    hasMod <- doesFileExist (outDir </> "go.mod")
    if not hasMod || null deps
        then return ()
        else do
            let external = filter (\(p, _) -> '/' `elem` p || '.' `elem` p) deps
            when (not (null external)) $
                putStrLn $ "   resolving " ++ show (length external) ++ " Go dep(s)"
            mapM_ (goGet outDir) external
            _ <- System.Process.readProcessWithExitCode
                    "sh" ["-c", "cd " ++ outDir ++ " && go mod tidy 2>&1"] ""
            return ()
  where
    goGet dir (pkg, ver) =
        let target = if ver == "" || ver == "latest"
                        then pkg
                        else pkg ++ "@" ++ ver
            cmd = "cd " ++ dir ++ " && go get " ++ target ++ " 2>&1"
        in do
            (ec, out, _) <- System.Process.readProcessWithExitCode "sh" ["-c", cmd] ""
            case ec of
                System.Exit.ExitSuccess -> return ()
                _ -> putStrLn $ "      go get " ++ target ++ " FAILED: " ++ out


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
        Nothing -> writeEmbeddedRuntime outDir
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


-- ═══════════════════════════════════════════════════════════
-- WORKSPACE TYPECHECK (for LSP)
-- ═══════════════════════════════════════════════════════════

-- | Per-module workspace typecheck result. Keys are dotted module
-- names ("Lib.Db", "Std.IoError", "Main").
data WorkspaceTypecheck = WorkspaceTypecheck
    { _wt_modules :: Map.Map String WorkspaceModule
    , _wt_canonError :: Maybe (String, String)  -- (moduleName, error)
    }

data WorkspaceModule = WorkspaceModule
    { _wm_path   :: FilePath
    , _wm_src    :: Src.Module
    , _wm_canon  :: Can.Module
    , _wm_types  :: Map.Map String T.Type   -- binding name → inferred type
    , _wm_source :: T.Text                  -- raw text for doc-comment scanning
    }


-- | LSP entry point: discover, parse, canonicalise and type-check the
-- entire workspace without running codegen. Honours the Sky stdlib
-- discovery root + Sky-source deps. Errors in any single module are
-- isolated — others continue so partial results are still useful for
-- hover/definition.
typecheckWorkspace :: Toml.SkyConfig -> FilePath -> IO WorkspaceTypecheck
typecheckWorkspace config entryPath = do
    let entryDir = takeDirectory entryPath
        sourceRoot = if Toml._sourceRoot config == "src"
            then entryDir
            else Toml._sourceRoot config
    loadAndSeedFfiRegistry
    depRoots <- SkyDeps.installDeps (Toml._skyDeps config)
    -- Materialise stdlib into a temp side-dir so we don't disturb the
    -- user's sky-out/. We use the source root parent so the path stays
    -- predictable for goto-definition jumps.
    let stdlibSideDir = entryDir </> ".sky-stdlib"
    stdlibRoot <- writeStdlibTo stdlibSideDir
    modules <- Graph.discoverModulesMulti
        (sourceRoot : depRoots ++ [stdlibRoot]) entryPath
    let moduleOrder = Graph.compilationOrder modules

    -- Parse all
    parsed <- Async.forConcurrently moduleOrder $ \modInfo -> do
        src <- TIO.readFile (Graph._mi_path modInfo)
        return (modInfo, src, Parse.parseModule src)
    let okParsed =
            [ (Graph._mi_name mi, Graph._mi_path mi, src, m)
            | (mi, src, Right m) <- parsed
            ]

    -- First-pass canonicalise (per-module deps map)
    firstPass <- Async.forConcurrently okParsed $ \(n, _, _, srcMod) ->
        case Canonicalise.canonicalise srcMod of
            Right cm -> return (Just (n, cm))
            Left _   -> return Nothing
    let firstValid = [x | Just x <- firstPass]
        depInfoMap = Map.fromList
            [ (modName, Canonicalise.DepInfo
                { Canonicalise._dep_name = Can._name depMod
                , Canonicalise._dep_unions =
                    [ (typeName, Can._u_alts u)
                    | (typeName, u) <- Map.toList (Can._unions depMod)
                    ]
                , Canonicalise._dep_aliases = Map.keys (Can._aliases depMod)
                , Canonicalise._dep_values = Set.toList (collectDeclNames (Can._decls depMod))
                })
            | (modName, depMod) <- firstValid
            ]

    -- Second-pass canonicalise + per-module typecheck
    perMod <- Async.forConcurrently okParsed $ \(n, path, src, srcMod) ->
        case Canonicalise.canonicaliseWithDeps depInfoMap srcMod of
            Left err -> return (n, Left err, srcMod, path, src)
            Right canMod -> do
                cs <- Constrain.constrainModule canMod
                r  <- Solve.solve cs
                let types = case r of
                        Solve.SolveOk t -> t
                        _               -> Map.empty
                return (n, Right (canMod, types), srcMod, path, src)

    let modMap = Map.fromList
            [ (n, WorkspaceModule
                { _wm_path = path
                , _wm_src = srcMod
                , _wm_canon = canMod
                , _wm_types = types
                , _wm_source = src
                })
            | (n, Right (canMod, types), srcMod, path, src) <- perMod
            ]
        firstError = listToMaybeFirst
            [ (n, err) | (n, Left err, _, _, _) <- perMod ]

    return WorkspaceTypecheck
        { _wt_modules = modMap
        , _wt_canonError = firstError
        }
  where
    listToMaybeFirst []    = Nothing
    listToMaybeFirst (x:_) = Just x


-- | Variant of writeEmbeddedSkyStdlib that targets an arbitrary
-- destination, used by the LSP path which mirrors stdlib next to the
-- project source so jumps land on stable, user-visible paths.
writeStdlibTo :: FilePath -> IO FilePath
writeStdlibTo root = do
    createDirectoryIfMissing True root
    mapM_ (writeOne root) embeddedSkyStdlib
    return root
  where
    writeOne base (relPath, bytes) = do
        let dst = base </> relPath
        createDirectoryIfMissing True (takeDirectory dst)
        BS.writeFile dst bytes


-- | Materialise the embedded Sky stdlib (Std.IoError, Std.RemoteData,
-- etc.) into <outDir>/.sky-stdlib/ at build start. Returns the root
-- path so `discoverModulesMulti` can probe it. Always rewritten so a
-- compiler upgrade picks up the latest stdlib without `sky clean`.
writeEmbeddedSkyStdlib :: FilePath -> IO FilePath
writeEmbeddedSkyStdlib outDir = do
    let root = outDir </> ".sky-stdlib"
    createDirectoryIfMissing True root
    mapM_ (writeOne root) embeddedSkyStdlib
    return root
  where
    writeOne base (relPath, bytes) = do
        let dst = base </> relPath
        createDirectoryIfMissing True (takeDirectory dst)
        BS.writeFile dst bytes


-- | Write the embedded runtime (bundled into the sky binary at TH-time)
-- to the output directory. Released binaries hit this path because there
-- is no runtime-go/ on disk; everything they need is already in the exe.
writeEmbeddedRuntime :: FilePath -> IO ()
writeEmbeddedRuntime outDir = do
    let rtDir = outDir </> "rt"
    createDirectoryIfMissing True rtDir
    mapM_ (writeOne outDir rtDir) embeddedRuntime
  where
    writeOne base rtBase (relPath, bytes) = do
        let dst = case relPath of
                'r':'t':'/':rest -> rtBase </> rest
                _                -> base </> relPath
        createDirectoryIfMissing True (takeDirectory dst)
        BS.writeFile dst bytes


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

    mkDef def = case def of
        Can.DestructDef _ _ -> []
        _ ->
          let -- For TypedDef, the 5th field is the RETURN type only;
              -- per-pattern arg types live in `typedPats :: [(Pat, Type)]`.
              (name, params, body, mAnnotArgs, mAnnotRet) = case def of
                Can.Def (A.At _ n) pats expr ->
                    (n, pats, expr, Nothing, Nothing)
                Can.TypedDef (A.At _ n) _ typedPats expr retTy ->
                    ( n
                    , map fst typedPats
                    , expr
                    , Just (map snd typedPats)
                    , Just retTy
                    )
                Can.DestructDef{} -> error "unreachable: filtered above"
              goName = modPrefix ++ "_" ++ name
              (goParams', destructStmts) = destructureParams params
              -- T3 (dep path): annotated dep functions get typed return.
              -- T2/T6 (dep path): typed params too. When no annotation
              -- exists, fall back to HM-inferred types recorded in the
              -- global env by the per-dep solver run.
              env = getCgEnv
              qualLookupName = modPrefix ++ "_" ++ name
              inferredParams = Map.findWithDefault []
                                  qualLookupName
                                  (Rec._cg_funcParamTypes env)
              inferredRet = Map.findWithDefault "any"
                                qualLookupName
                                (Rec._cg_funcRetType env)
              _dbg = ()
              depParamGoTys = case mAnnotArgs of
                  Just argTys -> map safeReturnType argTys
                  Nothing     ->
                      if null inferredParams
                          then replicate (length params) "any"
                          else inferredParams ++
                               replicate (max 0 (length params - length inferredParams)) "any"
              depRetType = _dbg `seq` case mAnnotRet of
                  Just rt' -> safeReturnType rt'
                  Nothing  -> inferredRet
              -- Replace each param's Go type with the typed form
              -- (when not "any"). destructureParams gave us patterns
              -- already; we just rewrite the type slot.
              typedGoParams' = zipWith
                  (\(GoIr.GoParam pname _) ty -> GoIr.GoParam pname ty)
                  goParams'
                  (depParamGoTys ++ repeat "any")
              rawBody = exprToGo body
              bodyExpr = wrapTypedReturn depRetType rawBody
          in [ GoIr.GoDeclFunc GoIr.GoFuncDecl
                { GoIr._gf_name = goName
                , GoIr._gf_typeParams = []
                , GoIr._gf_params = typedGoParams'
                , GoIr._gf_returnType = depRetType
                , GoIr._gf_body = destructStmts ++ [GoIr.GoReturn bodyExpr]
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
        Can.DestructDef _ _ -> acc  -- destructure let-binding — no top-level name


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
            GoIr.GoDeclRaw ("type " ++ qualType ++ " struct { Tag int; SkyName string; Fields []any }")
            : [ if arity == 0
                  then GoIr.GoDeclVar (qualType ++ "_" ++ cname) qualType
                        (Just (GoIr.GoStructLit qualType
                            [ ("Tag", GoIr.GoIntLit idx)
                            , ("SkyName", GoIr.GoStringLit cname)
                            ]))
                  else GoIr.GoDeclFunc GoIr.GoFuncDecl
                        { GoIr._gf_name = qualType ++ "_" ++ cname
                        , GoIr._gf_typeParams = []
                        , GoIr._gf_params =
                            [ GoIr.GoParam ("v" ++ show i) (ctorArgGoTypeDep i argTys)
                            | i <- [0 .. arity - 1]
                            ]
                        , GoIr._gf_returnType = qualType
                        , GoIr._gf_body = [GoIr.GoReturn (GoIr.GoStructLit qualType
                            ([ ("Tag", GoIr.GoIntLit idx)
                             , ("SkyName", GoIr.GoStringLit cname)
                             ]
                            ++ [("Fields", GoIr.GoSliceLit "any"
                                    (map (\i -> GoIr.GoIdent ("v" ++ show i)) [0..arity-1]))]))]
                        }
              | Can.Ctor cname idx arity argTys <- ctors
              ]
  where
    -- T1: dep ctor params typed from declared union's arg types.
    ctorArgGoTypeDep i argTys
        | i < length argTys =
            let ty = argTys !! i
                goTy = safeReturnType ty
            in if goTy == "any" then "any" else goTy
        | otherwise = "any"


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
                -- T7 (record field typing): emit struct with typed
                -- fields when the alias's field types are concrete
                -- primitives or known runtime-safe types. Fall back to
                -- `any` per-field when the type can't be safely spelled.
                fieldGoType fty =
                    let goTy = solvedTypeToGo fty
                    in if goTy == "any" || null goTy || isPolymorphicRet goTy
                         then "any"
                         else goTy
                structDecl = GoIr.GoDeclRaw $ "type " ++ structName ++ " struct { "
                    ++ intercalate_ "; "
                        [ capitalise_ fn ++ " " ++ fieldGoType fty
                        | (fn, T.FieldType _ fty) <- fieldList
                        ]
                    ++ " }"
                hasUserCtor = Set.member aliasName userDefs
                paramList = zipWith (\i _ -> "p" ++ show i) [0::Int ..] fieldList
                paramDecls = intercalate_ ", " [p ++ " any" | p <- paramList]
                fieldInits =
                    [ let goTy = fieldGoType fty
                          src = "p" ++ show i
                          coerced = if goTy == "any"
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
generateGoMulti :: Can.Module -> Src.Module -> Toml.SkyConfig -> Solve.SolvedTypes -> [GoIr.GoDecl] -> Set.Set String -> Map.Map String Int -> Map.Map String [String] -> Map.Map String String -> Map.Map String [String] -> Map.Map String String -> String
generateGoMulti canMod srcMod config solvedTypes depDecls depRecAliases depArities depParamTypes depRetTypes extraInferredParamTypes extraInferredRetTypes =
    let
        imports = unsafePerformIO $ do
            -- T2/T6: register entry-module + dep-module typed function
            -- signatures so call-site codegen (`coerceCallArgs`) can
            -- emit `any(arg).(T)` coercions when passing args to
            -- typed-param functions across module boundaries.
            -- Rebuild the cgEnv fresh from ALL sources (annotations,
            -- HM-inferred, dep types) so the final env is deterministic
            -- regardless of when `imports` is forced relative to
            -- depDecls during goCode rendering.
            let (entryParamTys, entryRetTys) = collectFuncTypes "" canMod
                allParamTys = Map.unions
                    [ entryParamTys, depParamTypes, extraInferredParamTypes ]
                allRetTys   = Map.unions
                    [ entryRetTys, depRetTypes, extraInferredRetTypes ]
                cgEnv = Rec.withFuncTypes allParamTys allRetTys
                      $ Rec.withDepArities depArities
                      $ Rec.withRecordAliases depRecAliases
                      $ Rec.buildCodegenEnv solvedTypes canMod
            writeIORef globalCgEnv cgEnv
            return $ collectGoImports canMod srcMod
        -- Force `imports` before anything else so the env is set up
        -- before depDecls / decls are evaluated (they read getCgEnv).
        importsForced = imports `seq` imports
        unionDecls = generateUnionTypes canMod
        aliasDecls = generateAliasTypes canMod
        decls = generateDecls canMod solvedTypes
        mainDecl = generateMainFunc canMod srcMod solvedTypes
        -- Pin the rt import so Go doesn't error out with "imported and not used"
        -- when the user's program doesn't happen to reference rt.* directly
        -- (e.g. main = 42). The blank var reference is zero-cost at runtime.
        rtPin = [GoIr.GoDeclRaw "var _ = rt.AsInt"]
        -- Emit sky.toml's `port` as a SKY_LIVE_PORT default so Sky.Live /
        -- Sky.Http.Server pick it up. Shell env and .env still take
        -- precedence (we only Setenv when unset).
        -- Use reflect-free stdlib (`os` package) in a named-init to set the
        -- port fallback without requiring extra imports — we pipe through
        -- rt.SetPortDefault which lives in the runtime (always imported).
        -- Every runtime default derivable from sky.toml lands in this
        -- single init() so the generated binary reflects the project's
        -- configuration at zero runtime cost. All defaults are only
        -- applied when the corresponding env var is unset — that way
        -- CI / docker can override without a recompile.
        liveDefaults =
            [ GoIr.GoDeclRaw $
                "func init() {\n"
                ++ "\trt.SetPortDefault(\"" ++ show (Toml._livePort config) ++ "\")\n"
                ++ tomlLiveEnv "SKY_LIVE_STORE"      (Toml._liveStore     config)
                ++ tomlLiveEnv "SKY_LIVE_STORE_PATH" (Toml._liveStorePath config)
                ++ tomlLiveEnv "SKY_LIVE_TTL"        (intString           (Toml._liveTtl config))
                ++ tomlLiveEnv "SKY_STATIC_DIR"      (Toml._liveStatic    config)
                ++ tomlLiveEnv "SKY_AUTH_SECRET"     (Toml._authSecret    config)
                ++ tomlLiveEnv "SKY_AUTH_TOKEN_TTL"  (intString (Toml._authTokenTtl config))
                ++ tomlLiveEnv "SKY_AUTH_COOKIE"     (Toml._authCookie    config)
                ++ tomlLiveEnv "SKY_AUTH_DRIVER"     (Toml._authDriver    config)
                ++ tomlLiveEnv "SKY_DB_DRIVER"       (Toml._dbDriver      config)
                ++ tomlLiveEnv "SKY_DB_PATH"         (Toml._dbPath        config)
                ++ "}"
            ]
        portDefault = liveDefaults  -- preserve historical name for downstream splices
        pkg = GoIr.GoPackage
            { GoIr._pkg_name = "main"
            , GoIr._pkg_imports = imports
            , GoIr._pkg_decls = rtPin ++ portDefault ++ depDecls ++ unionDecls ++ aliasDecls ++ decls ++ mainDecl
            }
    in GoBuilder.renderPackage pkg


-- | Emit a Go if-not-already-set os.Setenv for a sky.toml-derived
-- runtime default. No-op when the value is empty (so we don't unset
-- actual env-var overrides).
tomlLiveEnv :: String -> String -> String
tomlLiveEnv _    ""    = ""
tomlLiveEnv name value =
       "\trt.SetEnvDefault(\"" ++ name ++ "\", " ++ escapeGoString value ++ ")\n"


intString :: Int -> String
intString n
    | n <= 0    = ""
    | otherwise = show n


escapeGoString :: String -> String
escapeGoString s = "\"" ++ concatMap esc s ++ "\""
  where
    esc '\\' = "\\\\"
    esc '"'  = "\\\""
    esc '\n' = "\\n"
    esc '\r' = "\\r"
    esc '\t' = "\\t"
    esc c    = [c]


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
generateUnionTypes canMod =
    concatMap generateUnion (Map.toList (Can._unions canMod))
  where
    -- This module's Go prefix ("Main", "State", ...) — used to rewrite
    -- local type refs that typeToGo would otherwise return as
    -- "Main_Page" into just "Page".
    localPrefix = map (\c -> if c == '.' then '_' else c)
                      (ModuleName.toString (Can._name canMod))

    -- Strip "<localPrefix>_" from the front of a Go type string when
    -- present, so ctor param types that reference local unions use
    -- the unprefixed name (matching how generateUnion declares them).
    stripLocalPrefix s =
        let pre = localPrefix ++ "_"
        in if take (length pre) s == pre then drop (length pre) s else s

    generateUnion (typeName, Can.Union vars ctors numAlts opts) = case opts of
        Can.Enum ->
            -- Enum: type Name int; const ( Name_Ctor = iota ... )
            [ GoIr.GoDeclType typeName (GoIr.GoEnumDef (map (ctorConstName typeName) ctors)) ]
        _ ->
            -- Tagged union: struct with Tag + SkyName + fields. The
            -- SkyName field is used by the Sky.Live runtime to derive
            -- the wire-format msg name (e.g. "Increment") from any
            -- ADT value without the compiler having to emit a separate
            -- MsgTagToName table.
            [ GoIr.GoDeclRaw $ "type " ++ typeName ++ " struct { Tag int; SkyName string; Fields []any }" ]
            ++ map (generateCtorFunc typeName) ctors

    ctorConstName typeName (Can.Ctor cname _ _ _) = typeName ++ "_" ++ cname

    generateCtorFunc typeName (Can.Ctor cname idx arity argTys) =
        if arity == 0
        then GoIr.GoDeclVar (typeName ++ "_" ++ cname) typeName
            (Just (GoIr.GoStructLit typeName
                [ ("Tag", GoIr.GoIntLit idx)
                , ("SkyName", GoIr.GoStringLit cname)
                ]))
        else GoIr.GoDeclFunc GoIr.GoFuncDecl
            { GoIr._gf_name = typeName ++ "_" ++ cname
            , GoIr._gf_typeParams = []
            -- T1: ctor params are typed from the union declaration, not `any`.
            -- `HttpError Int String` becomes `(v0 int, v1 string) IoError`
            -- so callers get Go-level type checking at construction sites.
            , GoIr._gf_params = ctorParamsTyped argTys arity
            , GoIr._gf_returnType = typeName
            , GoIr._gf_body = [GoIr.GoReturn (GoIr.GoStructLit typeName
                ([ ("Tag", GoIr.GoIntLit idx)
                 , ("SkyName", GoIr.GoStringLit cname)
                 ]
                 ++ [("Fields", GoIr.GoSliceLit "any" (map (\i -> GoIr.GoIdent ("v" ++ show i)) [0..arity-1]))]))]
            }

    -- Map Can.Ctor argument types to Go param types. If we have fewer
    -- types than arity (parser/canon gap), fall back to `any` for the
    -- missing slots — we never want to crash codegen on incomplete info.
    ctorParamsTyped argTys arity =
        [ GoIr.GoParam ("v" ++ show i) (ctorArgGoType i argTys)
        | i <- [0 .. arity - 1]
        ]

    -- T1: ctor params are typed from the union's declared arg types,
    -- degrading to `any` for polymorphic TVars (T4 territory). Call
    -- sites coerce via the VarCtor branch of exprToGo Can.Call.
    ctorArgGoType i argTys
        | i < length argTys =
            let ty = argTys !! i
                goTy = safeReturnType ty
            in if goTy == "any" then "any" else stripLocalPrefix goTy
        | otherwise = "any"

    hasTVar :: T.Type -> Bool
    hasTVar t = case t of
        T.TVar _        -> True
        T.TLambda a b   -> hasTVar a || hasTVar b
        T.TType _ _ xs  -> any hasTVar xs
        T.TTuple a b cs -> any hasTVar (a : b : cs)
        T.TAlias _ _ pairs (T.Filled inner)  -> any hasTVar (inner : map snd pairs)
        T.TAlias _ _ pairs (T.Hoisted inner) -> any hasTVar (inner : map snd pairs)
        T.TRecord _ _   -> False
        T.TUnit         -> False


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
            fieldGoType fty =
                let goTy = solvedTypeToGo fty
                in if goTy == "any" || null goTy || isPolymorphicRet goTy
                     then "any"
                     else goTy
            goFields = map (\(fname, T.FieldType _ ftype) ->
                (capitalise fname, fieldGoType ftype)) fields
            paramList = zipWith (\i _ -> "p" ++ show i) [0::Int ..] fields
            paramDecls = intercalate_ ", " [p ++ " any" | p <- paramList]
            fieldInits =
                [ let goTy = fieldGoType fty
                      src = "p" ++ show i
                      coerced = if goTy == "any"
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
generateDefMaybe reachable dceEnabled def solvedTypes = case def of
    Can.DestructDef{} -> []  -- destructure lets only live inside bodies
    _ ->
        let name = case def of
                Can.Def (A.At _ n) _ _           -> n
                Can.TypedDef (A.At _ n) _ _ _ _  -> n
                Can.DestructDef{} -> error "unreachable: filtered above"
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
            Can.DestructDef _ _ -> ("__destruct__", [], error "unreachable: destructdef has no toplevel codegen")

        -- T3 (narrow): prefer the user's annotation (carried by
        -- TypedDef) and fall back to HM-inferred type from the solver
        -- map. Both routed through safeReturnType which rejects types
        -- that can't be safely emitted yet (user ADT names, record
        -- aliases — need T4 for polymorphic, T7 for user structs).
        mSolvedType = Map.lookup name solvedTypes
        mAnnotTy = case def of
            Can.TypedDef _ _ _ _ ty -> Just ty
            _                       -> Nothing
        goParams = map patternToParam params
        goRetType = case (mAnnotTy, mSolvedType) of
            (Just funcType, _) ->
                let (_argTypes, retType) = splitFuncType (length params) funcType
                in safeReturnType retType
            (_, Just funcType) ->
                let (_argTypes, retType) = splitFuncType (length params) funcType
                in safeReturnType retType
            _ -> "any"
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
        let rawBody = if isTyped
                then exprToGoTypedWithRet solvedTypes goRetType body
                else exprToGo body
            -- Wrap typed returns in any()-coerced assertion so we match
            -- Go's return type even when the body expression produces
            -- `any` (common case) or a concrete typed value.
            bodyExpr = wrapTypedReturn goRetType rawBody
            (goParams', destructStmts) = destructureParams params
        in
        [ GoIr.GoDeclFunc GoIr.GoFuncDecl
            { GoIr._gf_name = goSafeName name
            , GoIr._gf_typeParams = []
            , GoIr._gf_params = goParams'
            , GoIr._gf_returnType = goRetType
            , GoIr._gf_body = destructStmts ++ [GoIr.GoReturn bodyExpr]
            }
        ]


-- | Generate function parameters and destructuring statements for any
-- non-PVar patterns. Returns (params, prelude stmts) where the prelude
-- binds names extracted from complex patterns in the function body.
destructureParams :: [Can.Pattern] -> ([GoIr.GoParam], [GoIr.GoStmt])
destructureParams pats =
    let (params, stmtLists) = unzip (zipWith oneParam [0::Int ..] pats)
    in (params, concat stmtLists)
  where
    oneParam idx (A.At _ pat) = case pat of
        Can.PVar name -> (GoIr.GoParam (goSafeName name) "any", [])
        Can.PAnything -> (GoIr.GoParam "_" "any", [])
        Can.PUnit     -> (GoIr.GoParam "_" "any", [])
        _ ->
            let tmp = "_p" ++ show idx
            in (GoIr.GoParam tmp "any", patternBindings tmp pat)


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
-- | True when an inferred Go type reference can't safely be emitted
-- as a function return yet. Reject:
--   * Bare type parameters ("A", "T_a")
--   * Runtime types that aren't actually defined
--     (SkyList/SkyDict/SkySet/SkyCmd/SkySub are conceptual — their
--     values flow as `any` at runtime)
--   * The literal string "any"
isPolymorphicRet :: String -> Bool
isPolymorphicRet s
    | s == "any" = True
    -- Reject anywhere-in-string references to runtime types that aren't
    -- actually defined (they flow as `any` at runtime so the type would
    -- be an undefined identifier at Go-build time).
    | any (`isInfixOfStr` s)
          ["rt.SkyList", "rt.SkyDict", "rt.SkySet", "rt.SkyCmd", "rt.SkySub"] = True
    -- Reject leading underscores (malformed — happens when typeToGo
    -- combines empty module prefix with type name) and known-unresolved
    -- kernel types we haven't mapped yet (VNode from Std.Html).
    | take 1 s == "_" = True
    | any (`isInfixOfStr` s) ["_VNode", "VNode"] = True
    | otherwise =
        let hasBareUpperWord = any isPolyWord (words (replaceBrackets s))
        in hasBareUpperWord
  where
    replaceBrackets = map (\c -> if c `elem` ("[],*" :: String) then ' ' else c)
    isPolyWord w = case w of
        [c] | c >= 'A' && c <= 'Z' -> True
        ('T':'_':_)                -> True
        _                          -> False
    isInfixOfStr needle hay = any (isPrefixOfStr needle) (tails hay)
    isPrefixOfStr p str = take (length p) str == p
    tails [] = [[]]
    tails xs@(_:rest) = xs : tails rest


-- | T4: wrap a function body's raw Go expression so it matches the
-- declared Go return type at runtime. For parametric types like
-- `rt.SkyResult[E, A]`, a plain `any(body).(T)` assertion fails when
-- the body is built via the default `rt.Ok[any, any]` and the target
-- has specific E/A — the generic instantiations are distinct Go types.
-- ResultCoerce/MaybeCoerce reconstruct the value with target params.
wrapTypedReturn :: String -> GoIr.GoExpr -> GoIr.GoExpr
wrapTypedReturn retType body
    | retType == "any" = body
    | Just params <- stripParametric "rt.SkyResult" retType =
        GoIr.GoCall
            (GoIr.GoIdent ("rt.ResultCoerce[" ++ params ++ "]"))
            [body]
    | Just inner <- stripParametric "rt.SkyMaybe" retType =
        GoIr.GoCall
            (GoIr.GoIdent ("rt.MaybeCoerce[" ++ inner ++ "]"))
            [body]
    | otherwise =
        GoIr.GoTypeAssert
            (GoIr.GoCall (GoIr.GoIdent "any") [body])
            retType


-- | If `s` is shaped like `<prefix>[params]`, return `params`;
-- otherwise Nothing. Handles nested brackets by counting depth.
stripParametric :: String -> String -> Maybe String
stripParametric prefix s
    | take (length prefix) s == prefix, drop (length prefix) s /= "" =
        let rest = drop (length prefix) s
        in case rest of
            '[':_ ->
                let inner = dropLast1 (drop 1 rest)
                in if not (null inner) then Just inner else Nothing
            _ -> Nothing
    | otherwise = Nothing
  where
    dropLast1 [] = []
    dropLast1 [_] = []
    dropLast1 (x:xs) = x : dropLast1 xs


-- | Decide whether a Sky type can be safely emitted as a Go return
-- type today (T3). Accepts primitives, parametric Sky runtime types
-- (SkyResult/SkyMaybe/SkyTask), and user-defined ADTs / record
-- aliases (looking up the record-alias set in the codegen env to
-- append `_R` when needed). Rejects polymorphic type variables and
-- unmapped kernel types. Returns "any" for anything not safely
-- expressible.
safeReturnType :: T.Type -> String
safeReturnType t = case t of
    -- T4: Unit returns safely typed now — rt.ResultCoerce handles the
    -- generic-instantiation mismatch at the return wrap.
    T.TUnit                       -> "struct{}"
    T.TType _ "Int" []            -> "int"
    T.TType _ "Float" []          -> "float64"
    T.TType _ "Bool" []           -> "bool"
    T.TType _ "String" []         -> "string"
    T.TType _ "Char" []           -> "rune"
    T.TType _ "Bytes" []          -> "[]byte"
    T.TType _ "Result" [e, a]     -> "rt.SkyResult[" ++ safeReturnType e
                                     ++ ", " ++ safeReturnType a ++ "]"
    T.TType _ "Maybe"  [x]        -> "rt.SkyMaybe[" ++ safeReturnType x ++ "]"
    T.TType _ "Task"   [e, a]     -> "rt.SkyTask[" ++ safeReturnType e
                                     ++ ", " ++ safeReturnType a ++ "]"
    -- T5: list/dict/set typed as concrete Go types. User-code audit
    -- required in parallel — when a function annotated to return
    -- `Dict String String` actually holds mixed-type values (e.g.
    -- SQL COUNT(*) columns), the annotation is wrong and needs
    -- fixing.
    T.TType _ "List"   _          -> "[]any"
    T.TType _ "Dict"   _          -> "map[string]any"
    T.TType _ "Set"    _          -> "map[any]bool"
    -- User-defined named type: only emit when it's a known record
    -- alias (then use `_R` suffix). Plain ADT unions stay `any` until
    -- we can guarantee every call site produces the exact struct type
    -- (not just `any(expr)`). Re-enable when T6 lands.
    T.TType home name [] ->
        let modStr = ModuleName.toString home
            prefix = if null modStr || modStr == "Main"
                       then ""
                       else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
            base = prefix ++ name
            env = getCgEnv
            isRecordAlias = Set.member base (Rec._cg_recordAliases env)
                         || Set.member name (Rec._cg_recordAliases env)
        in if isRecordAlias then base ++ "_R" else "any"
    T.TAlias _ _ _ (T.Filled inner)  -> safeReturnType inner
    T.TAlias _ _ _ (T.Hoisted inner) -> safeReturnType inner
    _ -> "any"


-- | Walk a canonical module's top-level declarations, collecting
-- per-function (paramTypes, returnType) for every TypedDef whose
-- annotation is concrete and safely expressible. The qualified-name
-- prefix lets dep-module callers reference functions as
-- "Lib_Db_exec" while entry-module callers see "exec".
--
-- Returns (paramTypes :: Map name [paramType], retType :: Map name retType).
-- Functions without annotations are absent; callers treat absence as
-- "fall back to `any`".
collectFuncTypes :: String -> Can.Module -> (Map.Map String [String], Map.Map String String)
collectFuncTypes prefix canMod =
    collectFuncTypesWith Set.empty prefix canMod

-- | Same as collectFuncTypes but takes an extra set of record-alias
-- names so safeReturnTypePure can promote them to `_R` Go names. The
-- set should contain BOTH bare alias names and module-prefixed ones
-- so cross-module record refs resolve too.
collectFuncTypesWith :: Set.Set String -> String -> Can.Module -> (Map.Map String [String], Map.Map String String)
collectFuncTypesWith extraRecAliases prefix canMod =
    let localRecAliases = Rec.collectRecordAliases (Can._aliases canMod)
        prefixed = if null prefix
                     then localRecAliases
                     else Set.map (\n -> prefix ++ "_" ++ n) localRecAliases
        knownRecAliases = Set.unions [extraRecAliases, localRecAliases, prefixed]
        qualName n = if null prefix then n else prefix ++ "_" ++ n
        goDecls Can.SaveTheEnvironment = []
        goDecls (Can.Declare d rest)        = d : goDecls rest
        goDecls (Can.DeclareRec d ds rest)  = d : ds ++ goDecls rest
        extract def = case def of
            Can.TypedDef (A.At _ n) _ typedPats _ retType ->
                let argTypes = map snd typedPats
                    argGoTys = map (safeReturnTypeWith knownRecAliases) argTypes
                    retGoTy  = safeReturnTypeWith knownRecAliases retType
                    hasAnyTyped = retGoTy /= "any" || any (/= "any") argGoTys
                in if hasAnyTyped
                     then Just (qualName n, argGoTys, retGoTy)
                     else Nothing
            _ -> Nothing
        bindings = goDecls (Can._decls canMod)
        results = mapMaybe extract bindings
        paramMap = Map.fromList [ (qual, ps) | (qual, ps, _) <- results ]
        retMap   = Map.fromList [ (qual, r)  | (qual, _, r) <- results ]
    in (paramMap, retMap)


-- | safeReturnType variant that takes an explicit record-alias set
-- instead of consulting the global env. Used by collectFuncTypes
-- during env bootstrap.
safeReturnTypeWith :: Set.Set String -> T.Type -> String
safeReturnTypeWith recAliases = go
  where
    -- Extract module prefixes that appear in the alias set (everything
    -- before the last "_"). Lets us find "State_Model_R" from a TType
    -- whose home is "" or "Main".
    aliasModulePrefixes =
        Set.fromList
            [ reverse (drop 1 (dropWhile (/= '_') (reverse a)))
            | a <- Set.toList recAliases
            , '_' `elem` a
            ]

    go t = case t of
        T.TUnit                       -> "struct{}"
        T.TType _ "Int" []            -> "int"
        T.TType _ "Float" []          -> "float64"
        T.TType _ "Bool" []           -> "bool"
        T.TType _ "String" []         -> "string"
        T.TType _ "Char" []           -> "rune"
        T.TType _ "Bytes" []          -> "[]byte"
        T.TType _ "Result" [e, a]     -> "rt.SkyResult[" ++ go e
                                         ++ ", " ++ go a ++ "]"
        T.TType _ "Maybe"  [x]        -> "rt.SkyMaybe[" ++ go x ++ "]"
        T.TType _ "Task"   [e, a]     -> "rt.SkyTask[" ++ go e
                                         ++ ", " ++ go a ++ "]"
        T.TType _ "List"   _          -> "[]any"
        T.TType _ "Dict"   _          -> "map[string]any"
        T.TType _ "Set"    _          -> "map[any]bool"
        T.TType home name [] ->
            let modStr = ModuleName.toString home
                prefix = if null modStr || modStr == "Main"
                           then ""
                           else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
                base = prefix ++ name
                -- Prefer prefixed forms over bare name. When home is
                -- "" / "Main" we still try every known module prefix
                -- so a record alias defined in another module still
                -- resolves correctly.
                qualifiedCandidates =
                    [ p ++ "_" ++ name | p <- Set.toList aliasModulePrefixes ]
                candidates = if null prefix
                               then qualifiedCandidates ++ [name]
                               else base : qualifiedCandidates ++ [name]
                matches = [ c | c <- candidates, Set.member c recAliases ]
            in case matches of
                (m:_) -> m ++ "_R"
                _     -> "any"
        T.TAlias home name _ aliasType ->
            let modStr = ModuleName.toString home
                prefix = if null modStr || modStr == "Main"
                           then ""
                           else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
                base = prefix ++ name
                qualifiedCandidates =
                    [ p ++ "_" ++ name | p <- Set.toList aliasModulePrefixes ]
                candidates = if null prefix
                               then qualifiedCandidates ++ [name]
                               else base : qualifiedCandidates ++ [name]
                matches = [ c | c <- candidates, Set.member c recAliases ]
                inner = case aliasType of
                    T.Filled i  -> i
                    T.Hoisted i -> i
            in case matches of
                (m:_) -> m ++ "_R"
                _     -> go inner
        _ -> "any"


-- | Count how many params a dep-module binding has. Used when we
-- need to split a solver-inferred function type (which chains
-- TLambdas) into the right number of arg types.
countParamsFor :: String -> Can.Module -> Int
countParamsFor name canMod = go (Can._decls canMod)
  where
    go Can.SaveTheEnvironment = 0
    go (Can.Declare d rest) = maybe (go rest) id (matchDef d)
    go (Can.DeclareRec d ds rest) =
        maybe (firstMatching (d : ds) (go rest)) id (matchDef d)
    matchDef d = case d of
        Can.Def (A.At _ n) pats _
            | n == name -> Just (length pats)
            | otherwise -> Nothing
        Can.TypedDef (A.At _ n) _ pats _ _
            | n == name -> Just (length pats)
            | otherwise -> Nothing
        _ -> Nothing
    firstMatching [] fallback = fallback
    firstMatching (d:ds) fallback = case matchDef d of
        Just k  -> k
        Nothing -> firstMatching ds fallback


-- | Extract Go param types for a function from a solver-inferred
-- function type, taking `arity` TLambdas from the head.
-- Env-free (uses safeReturnTypePure) so it can run before the
-- globalCgEnv is fully populated.
splitInferredParams :: Int -> T.Type -> [String]
splitInferredParams 0 _ = []
splitInferredParams n (T.TLambda from to) =
    safeReturnTypePure from : splitInferredParams (n - 1) to
splitInferredParams _ _ = []


-- | Extract the Go return type for a function from a solver-inferred
-- function type (dropping `arity` TLambdas from the head). Env-free.
inferredReturnFor :: Int -> T.Type -> String
inferredReturnFor 0 ty = safeReturnTypePure ty
inferredReturnFor n (T.TLambda _ to) = inferredReturnFor (n - 1) to
inferredReturnFor _ ty = safeReturnTypePure ty


-- | Env-free version of safeReturnType for use during env bootstrap.
-- Doesn't recognise user record aliases (so they degrade to `any` in
-- the param/return tables); the codegen of the function body will
-- still see them via the live env. This is acceptable because record
-- aliases as call-site argument types are rare and the degradation
-- only loses a typing opportunity, not correctness.
safeReturnTypePure :: T.Type -> String
safeReturnTypePure t = case t of
    -- T4: Unit returns safely typed now — rt.ResultCoerce handles the
    -- generic-instantiation mismatch at the return wrap.
    T.TUnit                       -> "struct{}"
    T.TType _ "Int" []            -> "int"
    T.TType _ "Float" []          -> "float64"
    T.TType _ "Bool" []           -> "bool"
    T.TType _ "String" []         -> "string"
    T.TType _ "Char" []           -> "rune"
    T.TType _ "Bytes" []          -> "[]byte"
    T.TType _ "Result" [e, a]     -> "rt.SkyResult[" ++ safeReturnTypePure e
                                     ++ ", " ++ safeReturnTypePure a ++ "]"
    T.TType _ "Maybe"  [x]        -> "rt.SkyMaybe[" ++ safeReturnTypePure x ++ "]"
    T.TType _ "Task"   [e, a]     -> "rt.SkyTask[" ++ safeReturnTypePure e
                                     ++ ", " ++ safeReturnTypePure a ++ "]"
    T.TType _ "List"   _          -> "[]any"
    T.TType _ "Dict"   _          -> "map[string]any"
    T.TType _ "Set"    _          -> "map[any]bool"
    T.TAlias _ _ _ (T.Filled inner)  -> safeReturnTypePure inner
    T.TAlias _ _ _ (T.Hoisted inner) -> safeReturnTypePure inner
    _ -> "any"


-- Used by Map.fromList where values must be unique; here keys come from
-- distinct top-level names so no conflicts arise.
mapMaybe :: (a -> Maybe b) -> [a] -> [b]
mapMaybe _ []     = []
mapMaybe f (x:xs) = case f x of
    Just y  -> y : mapMaybe f xs
    Nothing -> mapMaybe f xs


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
            -- Local module: check zeroArgs set. Cross-module: check funcArities
            -- which is populated with qualified names from deps.
            isZeroArg = Set.member name (Rec._cg_zeroArgs env)
                     || Map.lookup qualName (Rec._cg_funcArities env) == Just 0
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
        curryLambdaPat params (exprToGo body)

    Can.Call func args ->
        case A.toValue func of
            Can.VarCtor _opts _home _typeName _ctorName annot ->
                -- ADT constructor partial app: JobDone : Int -> Result -> Msg
                -- applied to just `jid` must close over jid.
                let declared = ctorArity annot
                    got = length args
                    paramTys = ctorParamTypes annot
                in if got < declared
                    then emitPartialCtor func args (declared - got)
                    -- T1: coerce each arg to the ctor's declared param type.
                    else GoIr.GoCall (exprToGo func)
                          (zipWithDefault coerceArg exprToGo paramTys args)
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
                    -- T2/T6: when the callee has typed params (recorded
                    -- in env._cg_funcParamTypes), coerce each `any`-arg
                    -- expression to the expected param type.
                    else GoIr.GoCall (exprToGo func)
                                     (coerceCallArgs qualName args)
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
        -- Bind the value to a fresh temp, then run the standard pattern-
        -- bindings machinery (same code used by case arms) so tuple/record/
        -- constructor destructuring produces real bindings for each field.
        let tmp = "__destruct__"
            (A.At _ p) = pat
            valStmt = GoIr.GoShortDecl tmp (exprToGo valExpr)
            sink    = GoIr.GoAssign "_" (GoIr.GoIdent tmp)
            bindStmts = patternBindings tmp p
        in GoIr.GoBlock (valStmt : sink : bindStmts) (exprToGo body)

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

    Can.Tuple a b more ->
        case length more of
            0 -> GoIr.GoStructLit "rt.SkyTuple2"
                    [("V0", exprToGo a), ("V1", exprToGo b)]
            1 -> GoIr.GoStructLit "rt.SkyTuple3"
                    [("V0", exprToGo a), ("V1", exprToGo b), ("V2", exprToGo (head more))]
            _ ->
                -- arity 4+: pack into SkyTupleN{Vs: []any{...}}
                let vs = a : b : more
                    vsInit = GoIr.GoSliceLit "any" (map exprToGo vs)
                in GoIr.GoStructLit "rt.SkyTupleN" [("Vs", vsInit)]


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


-- | Per-argument Go types for a constructor, derived from its
-- canonical annotation. Uses safeReturnType (env-aware so record
-- aliases resolve). Missing slots degrade to "any".
ctorParamTypes :: Can.Annotation -> [String]
ctorParamTypes (Can.Forall _ t) = go t
  where
    go (T.TLambda from to) = safeReturnType from : go to
    go _                   = []

ctorArity :: Can.Annotation -> Int
ctorArity (Can.Forall _ t) = countArrows t
  where
    countArrows (T.TLambda _ r) = 1 + countArrows r
    countArrows _ = 0


-- | Emit a lambda that supplies the already-collected args then takes the
-- remaining `missing` args one at a time and calls the constructor.
emitPartialCtor :: Can.Expr -> [Can.Expr] -> Int -> GoIr.GoExpr
emitPartialCtor func suppliedArgs missing =
    let -- T1 partial-app coercion: recover the ctor's declared param
        -- types from its annotation so both already-supplied args and
        -- the closure-captured extras coerce to the right Go types.
        paramTys = case A.toValue func of
            Can.VarCtor _ _ _ _ annot -> ctorParamTypes annot
            _                         -> []
        suppliedTys = take (length suppliedArgs) paramTys
        extraTys    = drop (length suppliedArgs) paramTys
                   ++ replicate missing "any"
        suppliedGo  = zipWithDefault coerceArg exprToGo suppliedTys suppliedArgs
        extraNames  = [ "__p" ++ show i | i <- [0 .. missing - 1] ]
        extraIdents = zipWith (\n ty -> coerceArg (GoIr.GoIdent n) ty)
                              extraNames extraTys
        finalCall = GoIr.GoCall (exprToGo func) (suppliedGo ++ extraIdents)
    in foldr wrapLambda finalCall extraNames
  where
    wrapLambda name body =
        GoIr.GoFuncLit [GoIr.GoParam name "any"] "any"
            [GoIr.GoReturn body]


-- | Partial application of a user-defined top-level function: wrap the
-- call in a chain of `func(x any) any { return callee(... , x, ...) }`
-- lambdas binding the remaining parameters.
-- | T2/T6 helper. For a known top-level callee, look up its expected
-- Go param types and emit each arg with the right coercion. When a
-- param type is not registered (callee is `any`-typed), pass the arg
-- through unchanged. The `any(arg).(T)` form works whether `arg` is
-- already typed `T` (redundant assertion) or `any` (real coercion).
coerceCallArgs :: String -> [Can.Expr] -> [GoIr.GoExpr]
coerceCallArgs qualName args =
    let env = getCgEnv
        paramTypes = Map.findWithDefault [] qualName (Rec._cg_funcParamTypes env)
    in if null paramTypes
         then map exprToGo args
         else zipWithDefault coerceArg exprToGo paramTypes args

-- | T4-aware coercion. For parametric Sky types whose generic
-- instantiation won't match via plain `.(T)` assertion
-- (e.g. `SkyResult[any,any]` vs `SkyResult[IoError,string]`), use the
-- runtime coerce helpers that reconstruct the value with target
-- generic params.
coerceArg :: GoIr.GoExpr -> String -> GoIr.GoExpr
coerceArg e ty
    | ty == "any" || null ty = e
    | Just params <- stripParametric "rt.SkyResult" ty =
        GoIr.GoCall (GoIr.GoIdent ("rt.ResultCoerce[" ++ params ++ "]")) [e]
    | Just inner <- stripParametric "rt.SkyMaybe" ty =
        GoIr.GoCall (GoIr.GoIdent ("rt.MaybeCoerce[" ++ inner ++ "]")) [e]
    | otherwise =
        GoIr.GoTypeAssert
            (GoIr.GoCall (GoIr.GoIdent "any") [e]) ty

-- | Like zipWith, but when the left list runs out we apply a fallback
-- function to the remaining right-list elements. Used so callers
-- passing more args than the registered param-type list have the extra
-- args still emitted (variadic-ish degradation).
zipWithDefault :: (b -> a -> b) -> (c -> b) -> [a] -> [c] -> [b]
zipWithDefault _ fb [] cs = map fb cs
zipWithDefault _ _  _ [] = []
zipWithDefault f fb (a:as) (c:cs) = f (fb c) a : zipWithDefault f fb as cs


emitPartialUserCall :: Can.Expr -> [Can.Expr] -> Int -> GoIr.GoExpr
emitPartialUserCall func suppliedArgs missing =
    let -- Resolve callee qualified name so we can look up its typed
        -- param signature and coerce both the supplied args and the
        -- closure-captured extras.
        qualName = case A.toValue func of
            Can.VarTopLevel home name ->
                let modStr = ModuleName.toString home
                in if null modStr || modStr == "Main"
                     then name
                     else map (\c -> if c == '.' then '_' else c) modStr
                          ++ "_" ++ name
            _ -> ""
        env = getCgEnv
        paramTypes = Map.findWithDefault [] qualName
                       (Rec._cg_funcParamTypes env)
        suppliedTypes = take (length suppliedArgs) paramTypes
        extraTypes    = drop (length suppliedArgs) paramTypes
                     ++ replicate missing "any"
        suppliedGo = zipWithDefault coerceArg exprToGo suppliedTypes suppliedArgs
        extraNames = [ "__pp" ++ show i | i <- [0 .. missing - 1] ]
        extraIdents = zipWith (\n ty -> coerceArg (GoIr.GoIdent n) ty)
                              extraNames extraTypes
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
    Can.DestructDef pat valExpr ->
        let tmp = "__destruct__"
            (A.At _ p) = pat
            valStmt   = GoIr.GoShortDecl tmp (exprToGo valExpr)
            sink      = GoIr.GoAssign "_" (GoIr.GoIdent tmp)
            bindStmts = patternBindings tmp p
        in valStmt : sink : bindStmts

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
        subjectType = detectSubjectType branches
        -- Wrap in `any(...)` before asserting so the assertion works
        -- whether the expression is already typed (e.g. a typed Sky
        -- function returning SkyResult[IoError, string]) or `any`
        -- (legacy `any`-returning helpers). Without the `any()` wrap,
        -- Go rejects type-asserting a concrete struct to another.
        anyWrapped e = GoIr.GoCall (GoIr.GoIdent "any") [e]
        -- T4: when the subject type is a parametric Sky container
        -- (SkyResult[any,any] / SkyMaybe[any]), use the ResultCoerce /
        -- MaybeCoerce runtime helpers instead of a plain type assertion.
        -- This handles the case where the source is already typed with
        -- different generic params (e.g. SkyResult[any, string]) — a
        -- plain `.(SkyResult[any, any])` runtime-fails because the
        -- generic instantiations are distinct Go types.
        coerceSubject typeName e
            | Just params <- stripParametric "rt.SkyResult" typeName =
                GoIr.GoCall (GoIr.GoIdent ("rt.ResultCoerce[" ++ params ++ "]")) [e]
            | Just inner <- stripParametric "rt.SkyMaybe" typeName =
                GoIr.GoCall (GoIr.GoIdent ("rt.MaybeCoerce[" ++ inner ++ "]")) [e]
            | otherwise =
                GoIr.GoTypeAssert (anyWrapped e) typeName
        subjectDecl = case subjectType of
            Just typeName ->
                GoIr.GoShortDecl "__subject" (coerceSubject typeName goSubject)
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
                [ GoIr.GoTypeAssert (GoIr.GoCall (GoIr.GoIdent "any") [GoIr.GoIdent subject]) "[]any" ])
            (GoIr.GoIntLit 1)

    -- Fixed-length list: match exact length; element conditions handled in
    -- bindings below (codegen over-matches conservatively — strict element
    -- matching would need nested if-cascades we don't model in a single cond).
    Can.PList xs ->
        Just $ GoIr.GoBinary "=="
            (GoIr.GoCall (GoIr.GoIdent "len")
                [ GoIr.GoTypeAssert (GoIr.GoCall (GoIr.GoIdent "any") [GoIr.GoIdent subject]) "[]any" ])
            (GoIr.GoIntLit (length xs))

    -- Tuples, records, aliases: structure is guaranteed by HM — bindings carry the work.
    Can.PTuple{} -> Nothing
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
        let asSlice = GoIr.GoTypeAssert (GoIr.GoCall (GoIr.GoIdent "any") [GoIr.GoIdent subject]) "[]any"
            (A.At _ hPat) = h
            (A.At _ tPat) = t
            headExpr = GoIr.GoIndex asSlice (GoIr.GoIntLit 0)
            -- Wrap in any() so nested patternBindings can re-assert `.([]any)`.
            -- Without this, the recursive case `1 :: 2 :: _` tries
            -- `__tail.([]any)[0]` on something already typed `[]any`, failing
            -- Go's "is not an interface" check.
            tailExpr = GoIr.GoRaw ("any(any(" ++ subject ++ ").([]any)[1:])")
            headName = "__sky_h_" ++ subject
            tailName = "__sky_t_" ++ subject
            headStmts = case hPat of
                Can.PVar name ->
                    if isDiscardName name
                        then [ GoIr.GoAssign "_" headExpr ]
                        else [ GoIr.GoShortDecl name headExpr
                             , GoIr.GoAssign "_" (GoIr.GoIdent name)
                             ]
                Can.PAnything -> [ GoIr.GoAssign "_" headExpr ]
                _ -> GoIr.GoShortDecl headName headExpr
                    : GoIr.GoAssign "_" (GoIr.GoIdent headName)
                    : patternBindings headName hPat
            tailStmts = case tPat of
                Can.PVar name ->
                    if isDiscardName name
                        then [ GoIr.GoAssign "_" tailExpr ]
                        else [ GoIr.GoShortDecl name tailExpr
                             , GoIr.GoAssign "_" (GoIr.GoIdent name)
                             ]
                Can.PAnything -> [ GoIr.GoAssign "_" tailExpr ]
                _ -> GoIr.GoShortDecl tailName tailExpr
                    : GoIr.GoAssign "_" (GoIr.GoIdent tailName)
                    : patternBindings tailName tPat
        in headStmts ++ tailStmts

    -- [a, b, c]  →  bind each element by index
    Can.PList xs ->
        let asSlice suf = GoIr.GoRaw ("any(" ++ subject ++ ").([]any)[" ++ show suf ++ "]")
            bindEl i (A.At _ p) = case p of
                Can.PVar name ->
                    if isDiscardName name
                        then [ GoIr.GoAssign "_" (asSlice i) ]
                        else [ GoIr.GoShortDecl name (asSlice i)
                             , GoIr.GoAssign "_" (GoIr.GoIdent name)
                             ]
                Can.PAnything -> [ GoIr.GoAssign "_" (asSlice i) ]
                _ ->
                    let sub = "__sky_li_" ++ show i ++ "_" ++ subject
                    in GoIr.GoShortDecl sub (asSlice i)
                        : GoIr.GoAssign "_" (GoIr.GoIdent sub)
                        : patternBindings sub p
        in concat (zipWith bindEl [0::Int ..] xs)

    -- (a, b[, c, ...])  →  bind V0/V1/V2 (SkyTuple2/3) or Vs[N] (SkyTupleN)
    Can.PTuple aPat bPat more ->
        let arity = 2 + length more
            allPats = aPat : bPat : more
            (tupleKind, accessor) = case arity of
                2 -> ("SkyTuple2", \i -> GoIr.GoSelector (asTup "SkyTuple2") ("V" ++ show i))
                3 -> ("SkyTuple3", \i -> GoIr.GoSelector (asTup "SkyTuple3") ("V" ++ show i))
                _ -> ("SkyTupleN", \i -> GoIr.GoIndex
                        (GoIr.GoSelector (asTup "SkyTupleN") "Vs")
                        (GoIr.GoIntLit i))
            asTup k = GoIr.GoTypeAssert (GoIr.GoIdent subject) ("rt." ++ k)
            _ = tupleKind  -- silences warning; kept for grep-ability
            bindField i (A.At _ p) = case p of
                Can.PVar name ->
                    if isDiscardName name
                        then [ GoIr.GoAssign "_" (accessor i) ]
                        else [ GoIr.GoShortDecl name (accessor i)
                             , GoIr.GoAssign "_" (GoIr.GoIdent name)
                             ]
                Can.PAnything -> [ GoIr.GoAssign "_" (accessor i) ]
                _ ->
                    let sub = "__sky_t_V" ++ show i ++ "_" ++ subject
                    in GoIr.GoShortDecl sub (accessor i)
                       : GoIr.GoAssign "_" (GoIr.GoIdent sub)
                       : patternBindings sub p
        in concat (zipWith bindField [0 :: Int ..] allPats)

    -- { name }  →  name := rt.Field(subject, "Name")
    Can.PRecord fields ->
        concat
        [ [ GoIr.GoShortDecl f
            (GoIr.GoCall (GoIr.GoQualified "rt" "Field")
                [ GoIr.GoIdent subject
                , GoIr.GoStringLit (capitalise_ f)
                ])
          , GoIr.GoAssign "_" (GoIr.GoIdent f)
          ]
        | f <- fields
        ]

    -- `(PCons h t) as whole`  →  bind whole := subject, then recurse into inner
    Can.PAlias inner name ->
        let (A.At _ innerPat) = inner
            aliasStmt = if isDiscardName name
                then [ GoIr.GoAssign "_" (GoIr.GoIdent subject) ]
                else [ GoIr.GoShortDecl name (GoIr.GoIdent subject) ]
        in aliasStmt ++ patternBindings subject innerPat


-- | Bind a constructor argument to a local variable.
-- For Ok/Err/Just (our special generic types) we need a type-assertion on
-- the subject first when the subject is any-typed (comes from an inner
-- destructure temp) — otherwise `.OkValue` / `.JustValue` on `any` fails
-- Go's type check. For user-defined Tag-based ADTs, the outer case already
-- asserted the subject to the struct type so `.Fields[i]` works directly.
bindCtorArg :: String -> String -> Can.PatternCtorArg -> [GoIr.GoStmt]
bindCtorArg subject ctorName (Can.PatternCtorArg idx _ty pat) =
    let (A.At _ innerPat) = pat
        -- Type-assert the subject for the special generic ctors. Wrap in
        -- any(...) first so this works both when the subject is already
        -- typed (outer case asserted it) and when it's a raw `any` from
        -- a nested destructure temp.
        --
        -- Go accepts: any(x).(SkyResult[any, any]).OkValue
        -- Works for x : any                → cast-then-assert
        --       and x : rt.SkyResult[...]  → to-any-then-assert-back (identity)
        anyWrap n = GoIr.GoCall (GoIr.GoIdent "any") [GoIr.GoIdent n]
        subjectAsStruct = case ctorName of
            "Ok"   -> GoIr.GoTypeAssert (anyWrap subject) "rt.SkyResult[any, any]"
            "Err"  -> GoIr.GoTypeAssert (anyWrap subject) "rt.SkyResult[any, any]"
            "Just" -> GoIr.GoTypeAssert (anyWrap subject) "rt.SkyMaybe[any]"
            _      -> GoIr.GoIdent subject
        fieldAccess = case ctorName of
            "Ok"   -> GoIr.GoSelector subjectAsStruct "OkValue"
            "Err"  -> GoIr.GoSelector subjectAsStruct "ErrValue"
            "Just" -> GoIr.GoSelector subjectAsStruct "JustValue"
            _      -> GoIr.GoIndex
                        (GoIr.GoSelector subjectAsStruct "Fields")
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
        Can.PAnything -> [ GoIr.GoAssign "_" fieldAccess ]
        _ ->
            let tmp = "__sky_cf_" ++ show idx ++ "_" ++ subject
            in GoIr.GoShortDecl tmp fieldAccess
               : GoIr.GoAssign "_" (GoIr.GoIdent tmp)
               : patternBindings tmp innerPat


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
defName (Can.DestructDef _ _) = "__destruct__"


-- | Get the body expression from a definition
defBody :: Can.Def -> Can.Expr
defBody (Can.Def _ _ body) = body
defBody (Can.TypedDef _ _ _ body _) = body
defBody (Can.DestructDef _ body) = body


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
        curryLambdaPat params (exprToGoTyped types retType body)

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


-- | Pattern-aware currying. Each param that is not a simple PVar is bound
-- to `_pN any` and destructured via patternBindings inside the innermost
-- lambda body. This lets `\(a, b) -> a + b` compile correctly.
curryLambdaPat :: [Can.Pattern] -> GoIr.GoExpr -> GoIr.GoExpr
curryLambdaPat [] body = body
curryLambdaPat pats body =
    let go _   []     = [GoIr.GoReturn body]
        go idx (p:ps) =
            let (param, stmts) = oneLambdaParam idx p
                inner          = case ps of
                    [] -> stmts ++ [GoIr.GoReturn body]
                    _  -> stmts ++ [GoIr.GoReturn (wrap (idx + 1) ps)]
            in [GoIr.GoReturn (GoIr.GoFuncLit [param] "any" inner)]
        wrap idx (p:ps) =
            let (param, stmts) = oneLambdaParam idx p
                tail_ = case ps of
                    [] -> stmts ++ [GoIr.GoReturn body]
                    _  -> stmts ++ [GoIr.GoReturn (wrap (idx + 1) ps)]
            in GoIr.GoFuncLit [param] "any" tail_
        wrap _ [] = body
    in case go 0 pats of
        [GoIr.GoReturn e] -> e
        _ -> body
  where
    oneLambdaParam :: Int -> Can.Pattern -> (GoIr.GoParam, [GoIr.GoStmt])
    oneLambdaParam idx (A.At _ pat) = case pat of
        Can.PVar name -> (GoIr.GoParam (goSafeName name) "any", [])
        Can.PAnything -> (GoIr.GoParam "_" "any", [])
        Can.PUnit     -> (GoIr.GoParam "_" "any", [])
        _ ->
            let tmp = "_lp" ++ show idx
            in (GoIr.GoParam tmp "any", patternBindings tmp pat)


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
