-- | Single-module compilation pipeline.
-- Source → Parse → Canonicalise → (TODO: Type Check) → Generate Go
module Sky.Build.Compile where

import qualified Control.Concurrent.Async as Async
import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Data.Text as T
import qualified Data.Text.IO as TIO
import Data.IORef
import Data.Maybe (isJust, fromMaybe)
import qualified Data.Char as Char
import qualified Data.List as List
import qualified System.Directory
import qualified System.FilePath
import qualified System.Process
import qualified System.Exit
import Control.Monad (when)
import System.Directory (createDirectoryIfMissing, doesDirectoryExist, doesFileExist, copyFile, listDirectory)
import System.IO (hFlush, stdout, readFile', stderr, hPutStrLn)
import System.IO.Unsafe (unsafePerformIO)
import System.FilePath (takeDirectory, takeExtension, (</>))

import qualified Data.ByteString as BS
import Sky.Build.EmbeddedRuntime (embeddedRuntime, embeddedSkyStdlib)

import qualified Sky.AST.Source as Src
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Parse.Module as Parse
import qualified Sky.Canonicalise.Module as Canonicalise
import qualified Sky.Type.Exhaustiveness as Exhaust
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
globalCgEnv = unsafePerformIO $ newIORef (Rec.CodegenEnv Map.empty Map.empty Map.empty Set.empty Set.empty Map.empty Map.empty Map.empty Map.empty)

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
    seedTypedFfiNames
    if null mods
        then return ()
        else putStrLn $ "-- Loaded " ++ show (length mods) ++ " FFI module(s)"


-- | P7: scan ffi/*.go (and ffi/**/*.go) for `^func Go_X_yT(` definitions
-- and populate Env.ffiTypedWrapperNamesRef so call-site codegen can
-- prefer the typed variant. Silently tolerates a missing .skycache/go dir.
seedTypedFfiNames :: IO ()
seedTypedFfiNames = do
    let ffiDir = ".skycache/go"
    present <- doesDirectoryExist ffiDir
    if not present then return () else do
        entries <- listDirectory ffiDir
        let gofiles = [ ffiDir </> e | e <- entries, takeExtension e == ".go" ]
        pairLists <- mapM scanTypedWrapperFile gofiles
        let allEntries = concat pairLists
        writeIORef Env.ffiTypedWrapperNamesRef (Set.fromList (map fst allEntries))
        writeIORef Env.ffiTypedWrapperParamsRef (Map.fromList allEntries)


-- | Return `(name, paramGoTypes)` for every `^func Go_X_yT(…)` definition
-- in the file. Param Go types are parsed directly from the signature
-- (line-level — FfiGen emits typed wrappers with the signature on one
-- line). Zero-arg typed wrappers yield ("Go_X_yT", []).
scanTypedWrapperFile :: FilePath -> IO [(String, [String])]
scanTypedWrapperFile fp = do
    ok <- doesFileExist fp
    if not ok then return [] else do
        contents <- readFile fp
        let ls = lines contents
        return
            [ (name, paramTypes)
            | l <- ls
            , take 5 l == "func "
            , let rest = drop 5 l
            , take 3 rest == "Go_"
            , '(' `elem` rest
            , let name = takeWhile (/= '(') rest
            , not (null name)
            , last name == 'T'
            , let paramTypes = extractParamTypes (dropWhile (/= '(') rest)
            ]


-- | Parse `(p0 T0, p1 T1, …)` (the param list of a typed wrapper sig)
-- into [T0, T1, …]. Handles bracketed type params and nested parens as
-- balanced tokens.
extractParamTypes :: String -> [String]
extractParamTypes sig = case sig of
    ('(':rest) ->
        let inside = takeParenContents rest 0
        in map extractTypeAfterName (splitTopComma inside)
    _ -> []
  where
    takeParenContents [] _ = ""
    takeParenContents (')':_) 0 = ""
    takeParenContents ('(':xs) d = '(' : takeParenContents xs (d+1)
    takeParenContents (')':xs) d = ')' : takeParenContents xs (d-1)
    takeParenContents ('[':xs) d = '[' : takeParenContents xs (d+1)
    takeParenContents (']':xs) d = ']' : takeParenContents xs (d-1)
    takeParenContents (x:xs) d   = x : takeParenContents xs d

    splitTopComma str = reverse (map reverse (finish (foldl step ([], [], 0) str)))
      where
        step (cur, acc, 0) ','  = ([], cur : acc, 0)
        step (cur, acc, d) c
            | c == '[' || c == '(' = (c : cur, acc, d + 1)
            | c == ']' || c == ')' = (c : cur, acc, d - 1)
            | otherwise            = (c : cur, acc, d)
        finish (cur, acc, _) = if null cur then acc else cur : acc

    extractTypeAfterName part =
        let trimmed = dropWhile (== ' ') part
            afterName = dropWhile (/= ' ') trimmed
        in dropWhile (== ' ') afterName


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

    -- Phase 0c: Materialise the embedded Sky stdlib (Sky.Core.Error, etc.)
    -- into outDir/.sky-stdlib/ and add it as a discovery root so
    -- `import Sky.Core.Error` resolves with no user setup. Stdlib lives
    -- LAST in the root list so a user's local Std/* override wins.
    stdlibRoot <- writeEmbeddedSkyStdlib outDir

    -- Phase 1: Discover all modules.
    -- tests/ is an implicit extra root when it exists — `sky test`
    -- writes a synthesised entry under src/ that imports the test
    -- module from tests/, and the graph walker needs to see both
    -- roots for the import to resolve. Harmless for non-test builds
    -- because a tests/ dir without modules contributes no modules.
    putStrLn "-- Discovering modules"
    testsRootExists <- doesDirectoryExist "tests"
    let extraTestsRoot = if testsRootExists then ["tests"] else []
    modules <- Graph.discoverModulesMulti (sourceRoot : depRoots ++ extraTestsRoot ++ [stdlibRoot]) entryPath
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
                    , Canonicalise._dep_aliasDefs = Can._aliases depMod
                    , Canonicalise._dep_values = Set.toList (collectDeclNames (Can._decls depMod))
                    , Canonicalise._dep_exports = Can._exports depMod
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
        -- Re-build depInfoMap from pass 2 results so cross-module alias
        -- bodies carry the correct home resolutions (pass 1 canonicalises
        -- with empty deps, so imports that expose types from OTHER dep
        -- modules resolve with home="" — wrong). Pass 2 has all imports
        -- visible and produces the correctly-homed bodies. Entry-module
        -- canonicalisation uses this rebuilt map for alias expansion.
        let depInfoMap2 = Map.fromList
                [ (modName, Canonicalise.DepInfo
                    { Canonicalise._dep_name = Can._name depMod
                    , Canonicalise._dep_unions =
                        [ (typeName, Can._u_alts union)
                        | (typeName, union) <- Map.toList (Can._unions depMod)
                        ]
                    , Canonicalise._dep_aliases = Map.keys (Can._aliases depMod)
                    , Canonicalise._dep_aliasDefs = Can._aliases depMod
                    , Canonicalise._dep_values = Set.toList (collectDeclNames (Can._decls depMod))
                    , Canonicalise._dep_exports = Can._exports depMod
                    })
                | (modName, depMod) <- validDeps
                ]
        case depErrors of
         ((n, err):_) ->
            return (Left $ "Canonicalise error in " ++ n ++ ":\n" ++ err)
         [] ->
          case Canonicalise.canonicaliseWithDeps depInfoMap2 entrySrcMod of
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
            --
            -- Pass 1: solve each dep in isolation.
            depSolved0 <- Async.forConcurrently validDeps $ \(modName, depMod) -> do
                cs <- Constrain.constrainModule depMod
                r  <- Solve.solve cs
                case r of
                    Solve.SolveOk t -> return (modName, t)
                    Solve.SolveError _ -> return (modName, Map.empty)
            -- Pass 2: re-solve each dep with cross-module externals
            -- from pass 1. Deps that pass-1 failed (e.g. Chess.Move
            -- with `dir` type ambiguity) may now succeed because
            -- imported helpers' concrete types disambiguate their
            -- internal calls.
            --
            -- Serialised (mapM not Async.forConcurrently) because the
            -- external-ref write is global — parallel writes would
            -- race. Acceptable cost: dep solves are fast.
            let pass1Externals = buildCrossModuleExternalsWithMods validDeps depSolved0
            depSolved <- mapM (\(modName, depMod) -> do
                cs <- Constrain.constrainModuleWithExternals pass1Externals depMod
                r  <- Solve.solve cs
                case r of
                    Solve.SolveOk t -> return (modName, t)
                    Solve.SolveError _ ->
                        case lookup modName depSolved0 of
                            Just p1 -> return (modName, p1)
                            Nothing -> return (modName, Map.empty)) validDeps
            -- Entry module gets cross-module externals so VarTopLevel
            -- references to dep values emit CForeign with the dep's
            -- solved annotation. Only fully-concrete types (no free
            -- TVars) cross, so call-site fresh instantiation can't
            -- introduce spurious unifications. Dep-defined user ADTs
            -- that appear in those types are fine because the entry
            -- module already imported them via its env.
            -- Restrict externals to names actually DECLARED as
            -- top-level values in their home module. Solver env
            -- entries include every name that flowed through
            -- (imports, constructors, etc.) — using those as
            -- cross-module annotations leaks spurious unifications.
            let depDeclaredNames =
                    [ (mn, Set.toList (collectDeclNames (Can._decls dm)))
                    | (mn, dm) <- validDeps
                    ]
                rawExternals = Map.filterWithKey
                    (\(m, n) _ -> case lookup m depDeclaredNames of
                        Just names -> n `elem` names
                        Nothing    -> False)
                    (buildCrossModuleExternalsWithMods validDeps depSolved)
                -- DEBUG bisect: keep only first N entries
                depExternals = rawExternals
            _ <- return depDeclaredNames  -- silence unused warning on release path
            constraints <- Constrain.constrainModuleWithExternals depExternals canMod
            putStrLn $ "   cross-module externals: " ++ show (Map.size depExternals)
            solveResult <- Solve.solve constraints
            -- HM type errors are FATAL (promoted from warning). No
            -- silent degradation to `any`. This enforces the
            -- "if it compiles, it works" promise at the entry module.
            let solverError = case solveResult of
                    Solve.SolveError e -> Just e
                    _                  -> Nothing
            types <- case solveResult of
                Solve.SolveOk t -> do
                    putStrLn $ "   Types OK (" ++ show (length (Map.keys t)) ++ " bindings)"
                    return t
                Solve.SolveError err -> do
                    putStrLn $ "   TYPE ERROR: " ++ err
                    return Map.empty
            -- P3: exhaustiveness — walk the entry + every dep's canonical
            -- tree for non-exhaustive case expressions. A miss is a
            -- compile-time error with source context; the `panic("non-
            -- exhaustive case expression")` fallback in codegen never
            -- fires on well-checked code.
            let entryDiags = Exhaust.checkModule canMod
                depDiags   = concatMap (\(_, dm) -> Exhaust.checkModule dm) validDeps
                exhaustErr
                    | null (entryDiags ++ depDiags) = Nothing
                    | otherwise = Just $ renderExhaustDiags (entryDiags ++ depDiags)
            case exhaustErr of
                Just e  -> putStrLn ("   EXHAUSTIVENESS ERROR: " ++ e)
                Nothing -> return ()
            -- Merge inferred dep types into the param + return tables
            -- keyed by module-prefixed Go names. Annotation-derived
            -- entries already in the tables win over inferred ones.
            -- T4b: only record inferred sigs for UNANNOTATED bindings;
            -- annotated functions use their declared types verbatim,
            -- and if HM happens to infer spurious TVars for them we'd
            -- mistakenly emit `[any, any]` instantiations at call sites.
            let hasAnnotation n depMod = case Map.lookup n (declsByName depMod) of
                    Just (Can.TypedDef{}) -> True
                    _                     -> False
                -- Field-set → alias-name registry covering the entry
                -- module + every dep module (prefixed form) so HM-inferred
                -- record returns resolve to their `_R` struct name here too.
                earlyAllFieldIdx = Map.union
                    (Rec.buildRegistry (Can._aliases canMod))
                    (Rec.buildDepFieldIndex
                        [ (map (\c -> if c == '.' then '_' else c) mn, Can._aliases depMod)
                        | (mn, depMod) <- validDeps
                        ])
                -- HM-inferred sigs for dep module unannotated functions.
                -- TVars become Go type params for polymorphic functions.
                fullSigs = Map.unions
                    [ Map.fromList
                        [ ( prefix ++ "_" ++ n
                          , splitInferredSigWithReg earlyAllRecAliases earlyAllFieldIdx (countParamsFor n depMod) ty )
                        | (n, ty) <- Map.toList depTypes
                        , not (hasAnnotation n depMod)
                        ]
                    | (modName, depTypes) <- depSolved
                    , let prefix = map (\c -> if c == '.' then '_' else c) modName
                    , let depMod = head [ m | (mn, m) <- validDeps, mn == modName ]
                    ]
            let
                depInferredParams = Map.map (\(_, ps, _) -> ps) fullSigs
                depInferredRets   = Map.map (\(_, _, r) -> r)  fullSigs
                depInferredSigs   = fullSigs
            putStrLn $ "   HM infer (deps): "
                ++ show (Map.size depInferredParams) ++ " functions typed"
            modifyIORef globalCgEnv $ \e -> e
                { Rec._cg_funcParamTypes =
                    Map.union (Rec._cg_funcParamTypes e) depInferredParams
                , Rec._cg_funcRetType =
                    Map.union (Rec._cg_funcRetType e) depInferredRets
                , Rec._cg_funcInferredSigs =
                    Map.union (Rec._cg_funcInferredSigs e) depInferredSigs
                }
            putStrLn "-- Generating Go"
            let depAliasPairs = [ (map (\c -> if c == '.' then '_' else c) mn, Can._aliases depMod)
                                | (mn, depMod) <- validDeps ]
                goCode = generateGoMulti canMod entrySrcMod config types depDecls depRecAliases depArities depParamTypes depRetTypes depInferredParams depInferredRets depInferredSigs depAliasPairs
            createDirectoryIfMissing True outDir
            let mainGoPath = outDir </> "main.go"
            case (solverError, exhaustErr) of
              (Just err, _) -> return (Left ("Type error: " ++ err))
              (_, Just err) -> return (Left ("Non-exhaustive patterns: " ++ err))
              _ -> do
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
                -- P7: strip unreferenced FFI wrappers from sky-out/rt/*_bindings.go.
                -- This eliminates tens of thousands of any/any wrapper bodies
                -- the user code never calls (stripe alone contributes 74k).
                dceFfiWrappers outDir
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


-- | P7 FFI DCE: strip unused `func Go_...` wrapper bodies from the
-- copied-into-sky-out bindings files. Walks `sky-out/main.go` plus
-- every other Go file outside `sky-out/rt/` for `rt.Go_<name>(` call
-- sites, collects the reachable wrapper names (including typed `*T`
-- companions referenced through the compile's call-site migration),
-- then rewrites each `sky-out/rt/*_bindings.go` keeping only those
-- wrapper bodies. Imports + header comments are preserved as-is —
-- Go's compiler is happy to see unused imports as long as some
-- blank `_` import retains them, which every bindings file already
-- does via the `// Pin fmt against ...` footer.
--
-- Stripe alone goes from ~74k wrapper bodies to a few dozen; the
-- `grep 'func [A-Z][a-zA-Z0-9_]*(p0 any' examples/*/ffi/*.go` gate
-- is exercised against the ffi/ source files (not sky-out/rt), so
-- this DCE also preserves those files' size.
dceFfiWrappers :: FilePath -> IO ()
dceFfiWrappers outDir = do
    let rtDir = outDir </> "rt"
    rtExists <- doesDirectoryExist rtDir
    if not rtExists then return () else do
        -- Collect caller-side referenced names.
        nonRtFiles <- collectNonRtGoFiles outDir
        referenced <- foldr1Concat nonRtFiles collectRtReferences Set.empty
        putStrLn $ "   [DCE] caller-side rt.* identifiers: " ++ show (Set.size referenced)
        -- Binding files to prune are those starting with a Go-package slug.
        -- We DO NOT touch hand-maintained rt.go / live.go / db_auth.go /
        -- stdlib_extra.go / stdlib_web.go / live_store.go — those are the
        -- runtime, not FFI generator output.
        rtEntries <- listDirectory rtDir
        let bindingFiles =
                [ rtDir </> e
                | e <- rtEntries
                , takeExtension e == ".go"
                , "_bindings.go" `isSuffixOfPlain` e
                ]
        mapM_ (pruneBindingFile referenced) bindingFiles
  where
    isSuffixOfPlain suf s =
        length s >= length suf && drop (length s - length suf) s == suf

    foldr1Concat :: [FilePath]
                 -> (FilePath -> IO (Set.Set String))
                 -> Set.Set String -> IO (Set.Set String)
    foldr1Concat files f acc0 = do
        sets <- mapM f files
        return (foldr Set.union acc0 sets)


-- | List every `*.go` file under `outDir` that is NOT inside `outDir/rt`.
collectNonRtGoFiles :: FilePath -> IO [FilePath]
collectNonRtGoFiles outDir = do
    entries <- listDirectory outDir
    let direct = [ outDir </> e
                 | e <- entries
                 , takeExtension e == ".go"
                 ]
    return direct


-- | Find every `rt.<Name>(` identifier in a Go source file.
collectRtReferences :: FilePath -> IO (Set.Set String)
collectRtReferences fp = do
    ok <- doesFileExist fp
    if not ok then return Set.empty else do
        content <- readFile' fp
        return (Set.fromList (extractRtIdents content))


-- | Scan a Go source text for `rt.<NAME>` references. Returns just
-- the NAME half (no `rt.` prefix). Walks character by character,
-- matching the substring `rt.` preceded by a non-identifier byte —
-- so identifiers like `skyRtValue` don't false-match.
extractRtIdents :: String -> [String]
extractRtIdents src = go Nothing src
  where
    -- prev tracks the character immediately before the current cursor,
    -- used to rule out `rt.` inside a longer identifier.
    go _ [] = []
    go prev ('r':'t':'.':rest)
        | not (isIdentChar (unwrap prev))
        , (name, after) <- span isIdentChar rest
        , not (null name)
        = name : go (Just (lastOf name)) after
    go _ (c:cs) = go (Just c) cs

    unwrap Nothing  = ' '
    unwrap (Just c) = c
    lastOf = last

    isIdentChar c = (c >= 'a' && c <= 'z')
                 || (c >= 'A' && c <= 'Z')
                 || (c >= '0' && c <= '9')
                 || c == '_'


-- | Rewrite a bindings file keeping only functions that are (a) in the
-- referenced set OR (b) a `T`-suffix variant of a referenced function.
-- Preserves the file's package declaration, imports, and any top-level
-- var declarations.
pruneBindingFile :: Set.Set String -> FilePath -> IO ()
pruneBindingFile referenced fp = do
    content <- readFile' fp  -- strict read to release the handle before write
    let newContent = pruneBindingsText referenced content
    if newContent == content then return ()
        else writeFile fp newContent


pruneBindingsText :: Set.Set String -> String -> String
pruneBindingsText referenced src =
    let ls = lines src
        (header, body) = splitAfterImportBlock ls
        kept = pruneFuncs referenced body
        -- After stripping function bodies, some package aliases may no
        -- longer appear in the remaining source. Go rejects `imported
        -- and not used`, so rewrite each import to a blank `_` form
        -- when its alias no longer appears anywhere in the kept body.
        rewrittenHeader = rewriteImportsForDCE header kept
    in unlines (rewrittenHeader ++ kept)


-- | Rewrite import lines inside the header so any alias that no longer
-- appears in `body` becomes a blank `_` import. Preserves ordering
-- and comments.
rewriteImportsForDCE :: [String] -> [String] -> [String]
rewriteImportsForDCE header body =
    let bodyBlob = unlines body
    in map (rewriteImportLine bodyBlob) header


rewriteImportLine :: String -> String -> String
rewriteImportLine bodyBlob line =
    case parseImportLine line of
        Just (indent, alias, path, trailer)
            | alias /= "" && alias /= "_"
            , not (aliasReferenced alias bodyBlob)
            -> indent ++ "_ \"" ++ path ++ "\"" ++ trailer
        _ -> line


-- | Parse `\t<alias> "<path>"<trailer>` or `\t"<path>"<trailer>`.
-- Returns (indent, alias, path, trailer); alias is "" for bare string
-- imports and we leave those alone.
parseImportLine :: String -> Maybe (String, String, String, String)
parseImportLine line =
    let (lead, rest) = span (\c -> c == '\t' || c == ' ') line
    in case rest of
        ('"':r) ->
            -- Bare string import: `"reflect"`. The effective alias is
            -- the last path segment (the Go package name, for import
            -- paths we handle here — all stdlib + github-style).
            let (path, closeRest) = break (== '"') r
            in case closeRest of
                ('"':trailer) ->
                    let segs = splitSlash path
                        alias = if null segs then "" else last segs
                    in Just (lead, alias, path, trailer)
                _ -> Nothing
        _ ->
            let (alias, afterAlias) = break (== ' ') rest
            in case dropWhile (== ' ') afterAlias of
                ('"':r) ->
                    let (path, closeRest) = break (== '"') r
                    in case closeRest of
                        ('"':trailer) ->
                            if null alias || all isAliasChar alias
                                then Just (lead, alias, path, trailer)
                                else Nothing
                        _ -> Nothing
                _ -> Nothing
  where
    isAliasChar c = (c >= 'a' && c <= 'z')
                 || (c >= 'A' && c <= 'Z')
                 || (c >= '0' && c <= '9')
                 || c == '_'

    splitSlash = foldr step [[]]
      where
        step '/' acc = [] : acc
        step c (cur:rest) = (c:cur) : rest
        step _ [] = [[]]


-- | `<alias>.` appearing as a substring of the body blob after
-- stripping `//` line comments. Imports rarely overlap with
-- identifier spelling by accident; ignoring comments avoids
-- false positives from orphaned `// Pkg.Name` docstrings
-- that DCE left behind after dropping their function body.
aliasReferenced :: String -> String -> Bool
aliasReferenced alias blob = (alias ++ ".") `List.isInfixOf` stripComments blob

-- | Remove `//` line comments from Go source (anything after `//`
-- up to the next newline). Leaves `/* ... */` block comments alone —
-- FfiGen doesn't emit them for wrapper docs.
stripComments :: String -> String
stripComments = go
  where
    go [] = []
    go ('/':'/':rest) = go (dropWhile (/= '\n') rest)
    go (c:rest) = c : go rest


-- | Return (lines up to & including closing `)` of the `import (` block,
-- everything after). Files without an `import` block return (all, []).
splitAfterImportBlock :: [String] -> ([String], [String])
splitAfterImportBlock = go []
  where
    go acc [] = (reverse acc, [])
    go acc (l:rest)
        | stripLeadingTabs l == "import (" =
            let (imports, after) = takeUntilCloseParen rest
            in (reverse acc ++ [l] ++ imports, after)
        | otherwise = go (l:acc) rest
    stripLeadingTabs = dropWhile (\c -> c == '\t' || c == ' ')

    takeUntilCloseParen = takeUntilCloseParenAcc []
    takeUntilCloseParenAcc acc [] = (reverse acc, [])
    takeUntilCloseParenAcc acc (l:rest)
        | stripLeadingTabs l == ")" = (reverse (l : acc), rest)
        | otherwise = takeUntilCloseParenAcc (l : acc) rest


-- | Walk body lines, keeping top-level `var` / `type` / comment
-- blocks intact; drop `func <Name>` definitions whose name is not in
-- the referenced set (or its `T`-suffix sibling isn't referenced).
pruneFuncs :: Set.Set String -> [String] -> [String]
pruneFuncs referenced inputLines = go [] inputLines
  where
    -- `pending` accumulates preceding comment-or-blank lines that
    -- belong to the NEXT declaration. If the declaration is kept we
    -- flush them; if dropped we discard them along with the func.
    go :: [String] -> [String] -> [String]
    go pending []   = reverse pending
    go pending (l:rest)
        | isCommentOrBlank l =
            go (l : pending) rest
        | Just name <- matchFuncStart l =
            let (body, after) =
                    if isOneLineFunc l
                        then ([], rest)
                        else takeFuncBody rest
                funcLines = l : body
                baseName = if not (null name) && last name == 'T'
                           then take (length name - 1) name
                           else ""
                isKept = Set.member name referenced
                      || (not (null baseName) && Set.member baseName referenced)
            in if isKept
                then reverse pending ++ funcLines ++ go [] after
                else go [] after
        | otherwise =
            -- non-func non-comment line (var, type, etc.): keep it,
            -- along with any pending preceding comments.
            reverse pending ++ [l] ++ go [] rest

    isCommentOrBlank l =
        let trimmed = dropWhile (\c -> c == ' ' || c == '\t') l
        in null trimmed || take 2 trimmed == "//"

    -- A `func` line is one-line when the brace count at end-of-line is
    -- zero AND the line closes (i.e., the final run of `{`s is
    -- balanced by corresponding `}`s on the same line). Detect by
    -- running a brace counter; if it ends at 0 after seeing at least
    -- one `{`, the function body was fully contained.
    isOneLineFunc l =
        let (depth, sawOpen) = walk l 0 False
        in sawOpen && depth == 0
      where
        walk :: String -> Int -> Bool -> (Int, Bool)
        walk [] d s       = (d, s)
        walk ('{':cs) d s = walk cs (d+1) True
        walk ('}':cs) d s = walk cs (d-1) s
        walk (_  :cs) d s = walk cs d s


-- | `func Name` (possibly with generic `[...]` and `(`). Return Just
-- the bare Name or Nothing if this isn't a top-level func line.
matchFuncStart :: String -> Maybe String
matchFuncStart l
    | take 5 l == "func "
    , let rest = drop 5 l
    , not (null rest)
    , isIdentStart (head rest)
    = let (name, tail_) = span isIdentChar rest
      in if not (null name) && not (null tail_)
            && (head tail_ == '(' || head tail_ == '[')
         then Just name
         else Nothing
    | otherwise = Nothing
  where
    isIdentStart c = (c >= 'a' && c <= 'z')
                  || (c >= 'A' && c <= 'Z')
                  || c == '_'
    isIdentChar c = isIdentStart c || (c >= '0' && c <= '9')


-- | Consume until we see a line beginning with `}` at indent 0. Return
-- (body-lines-up-to-and-including-close, remaining-lines).
takeFuncBody :: [String] -> ([String], [String])
takeFuncBody = go []
  where
    go acc [] = (reverse acc, [])
    go acc (l:rest)
        | take 1 l == "}" = (reverse (l : acc), rest)
        | otherwise       = go (l : acc) rest


copyFfiDir :: FilePath -> IO ()
copyFfiDir outDir = do
    let ffiDir = ".skycache/go"
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
-- names ("Lib.Db", "Sky.Core.Error", "Main").
data WorkspaceTypecheck = WorkspaceTypecheck
    { _wt_modules :: Map.Map String WorkspaceModule
    , _wt_canonError :: Maybe (String, String)  -- (moduleName, error)
    }

data WorkspaceModule = WorkspaceModule
    { _wm_path        :: FilePath
    , _wm_src         :: Src.Module
    , _wm_canon       :: Can.Module
    , _wm_types       :: Map.Map String T.Type   -- top-level binding name → inferred type
    , _wm_localTypes  :: Map.Map String [T.Type] -- audit P2-2: innermost-first list per name (supports shadowing)
    , _wm_source      :: T.Text                  -- raw text for doc-comment scanning
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
    testsRootExists2 <- doesDirectoryExist "tests"
    let extraTestsRoot2 = if testsRootExists2 then ["tests"] else []
    modules <- Graph.discoverModulesMulti
        (sourceRoot : depRoots ++ extraTestsRoot2 ++ [stdlibRoot]) entryPath
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
                , Canonicalise._dep_aliasDefs = Can._aliases depMod
                , Canonicalise._dep_values = Set.toList (collectDeclNames (Can._decls depMod))
                , Canonicalise._dep_exports = Can._exports depMod
                })
            | (modName, depMod) <- firstValid
            ]

    -- Second-pass canonicalise + per-module typecheck
    perMod <- Async.forConcurrently okParsed $ \(n, path, src, srcMod) ->
        case Canonicalise.canonicaliseWithDeps depInfoMap srcMod of
            Left err -> return (n, Left err, srcMod, path, src)
            Right canMod -> do
                cs <- Constrain.constrainModule canMod
                (r, localTys) <- Solve.solveWithLocals cs
                let types = case r of
                        Solve.SolveOk t -> t
                        _               -> Map.empty
                return (n, Right (canMod, types, localTys), srcMod, path, src)

    let modMap = Map.fromList
            [ (n, WorkspaceModule
                { _wm_path        = path
                , _wm_src         = srcMod
                , _wm_canon       = canMod
                , _wm_types       = types
                , _wm_localTypes  = localTys
                , _wm_source      = src
                })
            | (n, Right (canMod, types, localTys), srcMod, path, src) <- perMod
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


-- | Materialise the embedded Sky stdlib (Sky.Core.Error,
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
              -- exists, fall back to HM-inferred type. TVars become Go
              -- type parameters (T4b) so partially-inferred functions
              -- get typed generically instead of falling back to `any`.
              env = getCgEnv
              qualLookupName = modPrefix ++ "_" ++ name
              -- Typed dep sigs: annotation or HM-inferred types.
              -- wrapTypedReturn coerces the body to match the return type.
              -- Re-export fallback: if the body is a single Call to another
              -- top-level value with a known typed signature, inherit its
              -- return type. Fixes the `foo = Other.foo` pattern where HM
              -- produced a TVar (cross-module value refs aren't currently
              -- solved across modules).
              delegateRetType = case (params, body) of
                  ([], A.At _ (Can.Call (A.At _ (Can.VarTopLevel calleeHome calleeName)) [])) ->
                      let calleeModPrefix = map (\c -> if c == '.' then '_' else c)
                              (ModuleName.toString calleeHome)
                          calleeKey = calleeModPrefix ++ "_" ++ calleeName
                          sameNameKey = calleeName
                      in case Map.lookup calleeKey (Rec._cg_funcInferredSigs env) of
                          Just (_, _, r) | r /= "any" -> Just r
                          _ -> case Map.lookup calleeKey (Rec._cg_funcRetType env) of
                              Just r | r /= "any" -> Just r
                              _ -> case Map.lookup sameNameKey (Rec._cg_funcRetType env) of
                                  Just r | r /= "any" -> Just r
                                  _ -> Nothing
                  ([], A.At _ (Can.VarTopLevel calleeHome calleeName)) ->
                      let calleeModPrefix = map (\c -> if c == '.' then '_' else c)
                              (ModuleName.toString calleeHome)
                          calleeKey = calleeModPrefix ++ "_" ++ calleeName
                      in case Map.lookup calleeKey (Rec._cg_funcInferredSigs env) of
                          Just (_, _, r) | r /= "any" -> Just r
                          _ -> case Map.lookup calleeKey (Rec._cg_funcRetType env) of
                              Just r | r /= "any" -> Just r
                              _ -> Nothing
                  _ -> Nothing
              (depTypeParams, depParamGoTys, depRetType) = case def of
                  Can.TypedDef _ _ typedPats _ retTy ->
                      ([], map (safeReturnType . snd) typedPats, safeReturnType retTy)
                  _ -> case Map.lookup qualLookupName (Rec._cg_funcInferredSigs env) of
                      Just (tps, ps, r) | r == "any"
                                        , Just delegated <- delegateRetType ->
                          (tps, ps, delegated)
                      Just sig -> sig
                      Nothing  -> case delegateRetType of
                          Just r  -> ([], replicate (length params) "any", r)
                          Nothing -> ([], replicate (length params) "any", "any")
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
                , GoIr._gf_typeParams = [ (tp, "any") | tp <- depTypeParams ]
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
            -- Emit as a type alias to rt.SkyADT so values constructed
            -- here are assignment-compatible with values produced by
            -- rt-side builders (ErrIo, ErrNetwork, Just/Nothing helpers,
            -- etc.). Eliminates the `interface {} is rt.SkyADT, not
            -- Sky_Core_Error_Error` panic class at pattern-match sites.
            GoIr.GoDeclRaw ("type " ++ qualType ++ " = rt.SkyADT")
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
            ++ [ GoIr.GoDeclRaw $ "func init() { "
                   ++ concatMap (\(Can.Ctor cname idx _ _) ->
                        "rt.RegisterAdtTag(\"" ++ cname ++ "\", " ++ show idx ++ "); ")
                        ctors
                   ++ "}" ]
  where
    -- T1: dep ctor params typed from declared union's arg types.
    ctorArgGoTypeDep i argTys
        | i < length argTys = safeReturnType (argTys !! i)
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
            -- Sort by declaration index so the auto-generated constructor's
            -- positional parameters match the user's field order. Map.toList
            -- alone returns alphabetical order, which swaps `Piece King White`
            -- (kind first) into `Piece White King` at the Go boundary and
            -- panics on the `.(T)` type assertion.
            let fieldList = List.sortOn (T._fieldIndex . snd) (Map.toList fields)
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
                -- Typed constructor: param types match struct fields.
                paramGoTypes = map (\(_, T.FieldType _ fty) -> fieldGoType fty) fieldList
                paramDecls = intercalate_ ", "
                    [ p ++ " " ++ ty | (p, ty) <- zip paramList paramGoTypes ]
                fieldInits =
                    [ capitalise_ fn ++ ": " ++ ("p" ++ show i)
                    | (i, (fn, _)) <- zip [0::Int ..] fieldList
                    ]
                ctorDecl = GoIr.GoDeclRaw $
                    "func " ++ qualName ++ "(" ++ paramDecls ++ ") " ++ structName ++
                    " { return " ++ structName ++ "{" ++ intercalate_ ", " fieldInits ++ "} }"
                gobDecl = GoIr.GoDeclRaw $
                    "func init() { rt.RegisterGobType(" ++ structName ++ "{}) }"
            in structDecl : gobDecl : [ctorDecl | not hasUserCtor]
        _ ->
            [ GoIr.GoDeclRaw ("type " ++ qualName ++ " = any") ]


-- | Generate Go with merged dependency declarations
generateGoMulti :: Can.Module -> Src.Module -> Toml.SkyConfig -> Solve.SolvedTypes -> [GoIr.GoDecl] -> Set.Set String -> Map.Map String Int -> Map.Map String [String] -> Map.Map String String -> Map.Map String [String] -> Map.Map String String -> Map.Map String ([String], [String], String) -> [(String, Map.Map String Can.Alias)] -> String
generateGoMulti canMod srcMod config solvedTypes depDecls depRecAliases depArities depParamTypes depRetTypes extraInferredParamTypes extraInferredRetTypes extraInferredSigs depAliasPairs =
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
            -- Register HM-inferred sigs for ENTRY module functions too
            -- so call-site coercion (coerceCallArgs / coerceArg) sees
            -- the typed params. Without this, calling an entry-module
            -- typed function from another entry function skips
            -- coercion and Go rejects any→concrete.
            -- Build alias set early so splitInferredSigWith can resolve
            -- cross-module record aliases in HM-inferred types.
            prevEnvEarly <- readIORef globalCgEnv
            let earlyRecAliases = Set.union depRecAliases
                    (Set.union (Rec.collectRecordAliases (Can._aliases canMod))
                               (Rec._cg_recordAliases prevEnvEarly))
                -- Build the full field-set → alias-name registry early
                -- so `splitInferredSigWithReg` can resolve TRecord nodes
                -- to their `_R` Go struct names in emitted signatures.
                earlyFieldIdx = Map.unions
                    [ Rec.buildRegistry (Can._aliases canMod)
                    , Rec.buildDepFieldIndex depAliasPairs
                    , Rec._cg_fieldIndex prevEnvEarly
                    ]
            -- Typed entry-module sigs from HM inference (for unannotated fns).
            -- Use goSafeName so lookups (which use qualName = goSafeName n)
            -- find the entry. E.g. "init" → "init_" for reserved Go names.
            let entryInferredSigs = Map.fromList
                    [ (goSafeName n, splitInferredSigWithReg earlyRecAliases earlyFieldIdx (countParamsFor n canMod) ty)
                    | (n, ty) <- Map.toList solvedTypes
                    , case Map.lookup n (declsByName canMod) of
                        Just (Can.TypedDef{}) -> False  -- annotation path
                        _                     -> True
                    ]
                entryInferredParams = Map.map (\(_, ps, _) -> ps) entryInferredSigs
                entryInferredRets   = Map.map (\(_, _, r) -> r) entryInferredSigs
            -- Gather the FULL record-alias set (entry + dep modules,
            -- prefixed and unprefixed forms) so collectFuncTypesWith's
            -- safeReturnTypeWith resolves `Piece` → `Chess_Piece_Piece_R`
            -- instead of degrading to `any`. Without this, annotated
            -- entry functions taking record types get `any` params.
            prevEnv <- readIORef globalCgEnv
            let allRecAliases = Set.union depRecAliases
                    (Set.union (Rec.collectRecordAliases (Can._aliases canMod))
                               (Rec._cg_recordAliases prevEnv))
                (entryParamTys, entryRetTys) = collectFuncTypesWith allRecAliases "" canMod
                allParamTys = Map.unions
                    [ entryParamTys, entryInferredParams
                    , depParamTypes, extraInferredParamTypes ]
                allRetTys   = Map.unions
                    [ entryRetTys, entryInferredRets
                    , depRetTypes, extraInferredRetTypes ]
                cgEnv = Rec.withInferredSigs
                          (Map.union extraInferredSigs entryInferredSigs)
                      $ Rec.withFuncTypes allParamTys allRetTys
                      $ Rec.withDepArities depArities
                      $ Rec.withRecordAliases depRecAliases
                      $ Rec.withDepFieldIndex depAliasPairs
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
collectGoImports _canMod srcMod =
    -- Import as blank to avoid "imported and not used" when user's main is
    -- a pure value. If main uses rt.* anywhere, Go doesn't complain about
    -- adding a blank import alongside the aliased one.
    -- Simpler: emit `_ = rt.Log_println` in a blank var at top.
    [ GoIr.GoImport "sky-app/rt" (Just "rt") ]
    ++ sideEffectImports (Src._imports srcMod)
  where
    sideEffectImports imps =
        [ GoIr.GoImport (skyModToGoPath segs) (Just "_")
        | imp <- imps
        , Src._importAlias imp == Just "_"
        , let A.At _ segs = Src._importName imp
        ]
    skyModToGoPath segs =
        let lowered = map (map Char.toLower) segs
        in reconstructGoPath lowered
    reconstructGoPath parts = case parts of
        [] -> ""
        [p] -> p
        (a:b:rest) ->
            let firstTwo = a ++ "." ++ b
            in case rest of
                [] -> firstTwo
                _  -> firstTwo ++ "/" ++ List.intercalate "/" rest


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
            -- Tagged union: alias rt.SkyADT so values constructed here
            -- are assignment-compatible with values produced by rt-side
            -- builders (ErrIo/ErrNetwork/etc.). Eliminates the
            -- "interface {} is rt.SkyADT, not <UserADT>" panic class at
            -- pattern-match sites.
            [ GoIr.GoDeclRaw $ "type " ++ typeName ++ " = rt.SkyADT" ]
            ++ map (generateCtorFunc typeName) ctors
            ++ [ GoIr.GoDeclRaw $ "func init() { "
                   ++ concatMap (\(Can.Ctor cname idx _ _) ->
                        "rt.RegisterAdtTag(\"" ++ cname ++ "\", " ++ show idx ++ "); ")
                        ctors
                   ++ "}" ]

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

    -- T1: ctor params are typed from the union's declared arg types.
    -- Call sites coerce via the VarCtor branch of exprToGo Can.Call.
    ctorArgGoType i argTys
        | i < length argTys = safeReturnType (argTys !! i)
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
            -- Field declaration order (via _fieldIndex) is the auto-ctor's
            -- positional API. Sorting by it keeps `Piece kind colour` the same
            -- on the Go side. See generateAliasForDep for the same note.
            let fieldList = List.sortOn (T._fieldIndex . snd) (Map.toList fields)
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
            -- Typed constructor: param types match struct fields.
            -- Params are still `any` so call sites with any-typed
            -- values compile. Coercion happens inside the body.
            paramGoTypes = map (\(_, T.FieldType _ fty) -> fieldGoType fty) fields
            paramDecls = intercalate_ ", "
                [ p ++ " " ++ ty | (p, ty) <- zip paramList paramGoTypes ]
            fieldInits =
                [ capitalise_ fn ++ ": " ++ ("p" ++ show i)
                | (i, (fn, _)) <- zip [0::Int ..] fields
                ]
            ctorDecl = GoIr.GoDeclRaw $
                "func " ++ name ++ "(" ++ paramDecls ++ ") " ++ structName ++
                " { return " ++ structName ++ "{" ++ intercalate_ ", " fieldInits ++ "} }"
            gobDecl = GoIr.GoDeclRaw $
                "func init() { rt.RegisterGobType(" ++ structName ++ "{}) }"
        in if Set.member name userDefinedNames
               then [ GoIr.GoDeclType structName (GoIr.GoStructDef goFields), gobDecl ]
               else [ GoIr.GoDeclType structName (GoIr.GoStructDef goFields)
                    , gobDecl
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

        -- Prefer the user's annotation when present, else use HM-
        -- inferred type. TVars in the inferred type become Go type
        -- params (T4b) via splitInferredSig.
        mSolvedType = Map.lookup name solvedTypes
        mAnnotTy = case def of
            Can.TypedDef _ _ _ _ ty -> Just ty
            _                       -> Nothing
        goParams = map patternToParam params
        -- Annotation case: TypedDef's 5th field is the RETURN type
        -- only; arg types live alongside patterns. For non-TypedDef,
        -- split the full inferred function type.
        -- Typed codegen: use annotation or HM-inferred types for
        -- function sigs. wrapTypedReturn coerces the body to match.
        (entryTypeParams, entryParamGoTys, goRetType) = case (def, mAnnotTy, mSolvedType) of
            (Can.TypedDef _ _ typedPats _ retTy, _, _) ->
                ([], map (safeReturnType . snd) typedPats, safeReturnType retTy)
            (_, _, Just funcType) ->
                splitInferredSigWithReg
                    (Rec._cg_recordAliases getCgEnv)
                    (Rec._cg_fieldIndex getCgEnv)
                    (length params)
                    funcType
            _ -> ([], replicate (length params) "any", "any")
        isTyped = False  -- body codegen still uses exprToGo (any-typed)
    in
    -- Skip "main" — handled separately
    if name == "main" then []
    else
        let rawBody = if isTyped
                then exprToGoTypedWithRet solvedTypes goRetType body
                else exprToGo body
            bodyExpr = wrapTypedReturn goRetType rawBody
            (goParams', destructStmts) = destructureParams params
            -- Replace each param's Go type with the typed form (from
            -- annotation or HM inference). destructureParams gave us
            -- the parameter patterns with `"any"` types by default.
            typedGoParams = zipWith
                (\(GoIr.GoParam pn _) ty -> GoIr.GoParam pn ty)
                goParams'
                (entryParamGoTys ++ repeat "any")
        in
        [ GoIr.GoDeclFunc GoIr.GoFuncDecl
            { GoIr._gf_name = goSafeName name
            , GoIr._gf_typeParams = [ (tp, "any") | tp <- entryTypeParams ]
            , GoIr._gf_params = typedGoParams
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
-- | Render an inline coercion expression from `any` to `goTy`. String
-- fragment, emitted inside record-ctor field initialisers. See
-- wrapTypedReturn for the GoExpr-level equivalent.
coerceExprFor :: String -> String -> String
coerceExprFor goTy src = case goTy of
    "any"     -> src
    "string"  -> "rt.CoerceString(" ++ src ++ ")"
    "int"     -> "rt.CoerceInt(" ++ src ++ ")"
    "bool"    -> "rt.CoerceBool(" ++ src ++ ")"
    "float64" -> "rt.CoerceFloat(" ++ src ++ ")"
    _
      -- Cross-instantiation coerce for containers: SkyMaybe[any] → SkyMaybe[T]
      | Just params <- stripParametric "rt.SkyResult" goTy
        -> "rt.ResultCoerce[" ++ eraseTypeParams params ++ "](" ++ src ++ ")"
      | Just inner <- stripParametric "rt.SkyMaybe" goTy
        -> "rt.MaybeCoerce[" ++ eraseTypeParams inner ++ "](" ++ src ++ ")"
      | otherwise ->
          let erased = eraseTypeParams goTy
          in if erased == "any" then src
             else "rt.Coerce[" ++ erased ++ "](" ++ src ++ ")"


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
    | Just params <- stripParametric "rt.SkyTask" retType =
        GoIr.GoCall (GoIr.GoIdent ("rt.TaskCoerceT[" ++ params ++ "]")) [body]
    -- Audit P0-3: replace raw `any(body).(T)` with a runtime Coerce
    -- helper. Direct assertion panics with a cryptic 'interface
    -- conversion' message on mismatch; Coerce gives a site-identified
    -- diagnostic and propagates via rt panic-recovery as Err. Also
    -- handles reflect-convertible types (numeric widenings, typed
    -- aliases) which the raw assertion rejects.
    | retType == "string" =
        GoIr.GoCall (GoIr.GoIdent "rt.CoerceString") [body]
    | retType == "int" =
        GoIr.GoCall (GoIr.GoIdent "rt.CoerceInt") [body]
    | retType == "bool" =
        GoIr.GoCall (GoIr.GoIdent "rt.CoerceBool") [body]
    | retType == "float64" =
        GoIr.GoCall (GoIr.GoIdent "rt.CoerceFloat") [body]
    | otherwise =
        GoIr.GoCall (GoIr.GoIdent ("rt.Coerce[" ++ retType ++ "]")) [body]


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
-- | Build a cross-module external-signature map from per-module
-- solved types. Only fully concrete types (no free TVars at all)
-- cross — the entry module's solver instantiates each call site
-- fresh via CForeign, so polymorphic signatures would land with
-- no constraint on the fresh TVars, which is worse than a local
-- inference. Concrete types let the entry solver propagate real
-- information (int, String, SkyTuple2, Maybe SomeAdt, etc.) to
-- the caller's fresh var.
--
-- Also rejects types containing solver-internal placeholder TVars
-- (names starting with `_` or of length > 1) — those are
-- unresolved bindings the solver couldn't close; forwarding them
-- as external annotations masks the underlying inference gap.
-- | Build an external signature map from per-module solved types.
-- Takes a list of (modName, Can.Module) to cross-reference type
-- names against their actual defining module when a solved type
-- has unresolved (empty) homes. This fixup is necessary because
-- pass-1 canonicalisation in each dep uses that dep's own tmap,
-- which misses type names the dep references without importing
-- (Chess.Ai uses `Model` without `import State`).
buildCrossModuleExternalsWithMods
    :: [(String, Can.Module)]
    -> [(String, Map.Map String T.Type)]
    -> Map.Map (String, String) T.Annotation
buildCrossModuleExternalsWithMods validDeps depSolved =
    let typeHomeMap = buildGlobalTypeHomeMap validDeps
        fixHomes = fixupHomes typeHomeMap
    in Map.fromList
        [ ((modName, name), generaliseToAnnotation (fixHomes ty))
        | (modName, types) <- depSolved
        , (name, ty) <- Map.toList types
        , isFunctionType ty
        ]
  where
    isFunctionType (T.TLambda _ _) = True
    isFunctionType _               = False


-- | Backwards-compat: previous buildCrossModuleExternals signature.
buildCrossModuleExternals
    :: [(String, Map.Map String T.Type)]
    -> Map.Map (String, String) T.Annotation
buildCrossModuleExternals = buildCrossModuleExternalsWithMods []


-- | Build a global map from type name → defining module by walking
-- every dep's declared unions and record aliases. When a pass-1
-- canonicalised annotation references `Model` with home="" (because
-- the referencing module didn't import State), we look it up here
-- and fix the home to the actual defining module.
buildGlobalTypeHomeMap
    :: [(String, Can.Module)]
    -> Map.Map String ModuleName.Canonical
buildGlobalTypeHomeMap validDeps =
    Map.fromList
        [ (typeName, Can._name depMod)
        | (_, depMod) <- validDeps
        , typeName <- Map.keys (Can._unions depMod)
                   ++ Map.keys (Can._aliases depMod)
        ]


-- | Walk a Canonical type and replace every empty-home nominal
-- reference whose name appears in the global type-home map with
-- its real home. Primitives keep their kernel homes; everything
-- else gets the resolved dep home.
fixupHomes :: Map.Map String ModuleName.Canonical -> T.Type -> T.Type
fixupHomes hmap = go
  where
    go ty = case ty of
        T.TType home name args ->
            let args' = map go args
                resolved = case Map.lookup name hmap of
                    Just h | null (ModuleName.toString home) -> h
                    _ -> home
            in T.TType resolved name args'
        T.TAlias home name pairs aliasType ->
            let pairs' = [(n, go t) | (n, t) <- pairs]
                resolved = case Map.lookup name hmap of
                    Just h | null (ModuleName.toString home) -> h
                    _ -> home
                aliasType' = case aliasType of
                    T.Filled i  -> T.Filled (go i)
                    T.Hoisted i -> T.Hoisted (go i)
            in T.TAlias resolved name pairs' aliasType'
        T.TLambda a b -> T.TLambda (go a) (go b)
        T.TTuple a b cs -> T.TTuple (go a) (go b) (map go cs)
        T.TRecord fields mExt ->
            T.TRecord (Map.map (\(T.FieldType i fTy) -> T.FieldType i (go fTy)) fields) mExt
        T.TVar n -> T.TVar n
        T.TUnit -> T.TUnit


-- | Generalise a solved type into a polymorphic Annotation by
-- quantifying every free TVar. This is Hindley-Milner's `gen` for
-- cross-module export: the annotation says "the caller decides
-- what to plug in for each TVar", which is correct for top-level
-- bindings that were HM-inferred without user-supplied annotation.
--
-- Solver-internal TVar names (_cargN, _fooN_res, etc.) are renamed
-- to plain user-level names (a, b, c, ...) before being quantified.
-- Without the rename, the annotation would reference names the
-- external consumer's solver can't produce at fresh instantiation,
-- and the cross-module channel silently drops those bindings.
generaliseToAnnotation :: T.Type -> T.Annotation
generaliseToAnnotation ty =
    let rawVars = collectFreeTVars ty
        (renamedTy, renamed) = renameSolverInternals rawVars ty
    in T.Forall renamed renamedTy


-- | Build a rename map from solver-internal TVar names to sequential
-- user-level names (a, b, c, …), then substitute throughout the type.
-- Returns (newType, newFreeVarList).
renameSolverInternals :: [String] -> T.Type -> (T.Type, [String])
renameSolverInternals rawVars ty =
    let userNames = [ [c] | c <- ['a' .. 'z'] ]
                 ++ [ [c] ++ show (i :: Int) | i <- [1..], c <- ['a' .. 'z'] ]
        rename = Map.fromList (zip rawVars userNames)
        newVars = map (\v -> Map.findWithDefault v v rename) rawVars
    in (substTVars rename ty, newVars)


-- | Apply a TVar name rename to every TVar in a type.
substTVars :: Map.Map String String -> T.Type -> T.Type
substTVars subst = go
  where
    go t = case t of
        T.TVar n -> T.TVar (Map.findWithDefault n n subst)
        T.TLambda a b -> T.TLambda (go a) (go b)
        T.TType home n args -> T.TType home n (map go args)
        T.TTuple a b cs -> T.TTuple (go a) (go b) (map go cs)
        T.TRecord fields mExt ->
            T.TRecord
                (Map.map (\(T.FieldType i fTy) -> T.FieldType i (go fTy)) fields)
                (fmap (\e -> Map.findWithDefault e e subst) mExt)
        T.TAlias home n pairs aliasType ->
            T.TAlias home n [(k, go v) | (k, v) <- pairs]
                (case aliasType of
                    T.Filled i -> T.Filled (go i)
                    T.Hoisted i -> T.Hoisted (go i))
        T.TUnit -> T.TUnit


collectFreeTVars :: T.Type -> [String]
collectFreeTVars = nubOrd . go
  where
    nubOrd [] = []
    nubOrd (x:xs) = x : nubOrd (filter (/= x) xs)
    go t = case t of
        T.TVar n -> [n]
        T.TLambda a b -> go a ++ go b
        T.TType _ _ args -> concatMap go args
        T.TTuple a b cs -> concatMap go (a : b : cs)
        T.TRecord fields _ -> concatMap (\(T.FieldType _ fTy) -> go fTy) (Map.elems fields)
        T.TAlias _ _ pairs aliasType ->
            concatMap (go . snd) pairs ++ case aliasType of
                T.Filled i -> go i
                T.Hoisted i -> go i
        T.TUnit -> []


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
    T.TType _ "Cmd"    _          -> "rt.SkyCmd"
    T.TType _ "Sub"    _          -> "rt.SkySub"
    T.TType _ "List"   _          -> "[]any"
    T.TType _ "Dict"   _          -> "map[string]any"
    T.TType _ "Set"    _          -> "map[any]bool"
    -- Tuples emit as rt.SkyTuple{2,3,N}. V0/V1/V2 remain `any`
    -- (SkyTuple2 = T2[any, any]) so current body codegen stays
    -- valid — tuple destructure in patternBindings wraps with any()
    -- before asserting.
    T.TTuple _ _ []               -> "rt.SkyTuple2"
    T.TTuple _ _ [_]              -> "rt.SkyTuple3"
    T.TTuple _ _ _                -> "rt.SkyTupleN"
    -- Opaque parameterised types whose Go alias is `any` regardless
    -- of type args (Decoder a, Value a). Match before the []-only
    -- TType branch so `Decoder String` resolves the same way.
    T.TType _ name _ | Just goTy <- opaqueParameterisedGoTy name -> goTy
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
            allAliases = Rec._cg_recordAliases env
            -- Try all known module prefixes so cross-module record
            -- aliases resolve correctly (e.g. "Model" → "State_Model_R").
            qualifiedCandidates =
                [ p ++ "_" ++ name
                | a <- Set.toList allAliases
                , '_' `elem` a
                , let p = reverse (drop 1 (dropWhile (/= '_') (reverse a)))
                , not (null p)
                ]
            candidates = if null prefix
                           then qualifiedCandidates ++ [name]
                           else base : qualifiedCandidates ++ [name]
            matches = [ c | c <- candidates, Set.member c allAliases ]
            isRuntimeOnly = name `elem` runtimeOnlyTypes
            -- Check runtime typed map for known concrete types
            runtimeTyped = lookup name runtimeTypedMap
        in case matches of
            (m:_) -> m ++ "_R"
            _     -> case runtimeTyped of
                Just goTy -> goTy
                Nothing   -> if isRuntimeOnly then "any" else base
    -- TAlias emitted by the canonicaliser's alias-expansion pass.
    -- Resolve using the same record-alias / runtime-type lookup as
    -- TType so `Profile` → `Main_Profile_R` instead of degenerating
    -- to `any` via the inner TRecord. Fall through to inner only
    -- when the alias name isn't registered anywhere.
    T.TAlias home name _ aliasType ->
        let modStr = ModuleName.toString home
            prefix = if null modStr || modStr == "Main"
                       then ""
                       else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
            base = prefix ++ name
            env = getCgEnv
            allAliases = Rec._cg_recordAliases env
            qualifiedCandidates =
                [ p ++ "_" ++ name
                | a <- Set.toList allAliases
                , '_' `elem` a
                , let p = reverse (drop 1 (dropWhile (/= '_') (reverse a)))
                , not (null p)
                ]
            candidates = if null prefix
                           then qualifiedCandidates ++ [name]
                           else base : qualifiedCandidates ++ [name]
            matches = [ c | c <- candidates, Set.member c allAliases ]
            isRuntimeOnly = name `elem` runtimeOnlyTypes
            runtimeTyped = lookup name runtimeTypedMap
            innerType = case aliasType of
                T.Filled  inner -> inner
                T.Hoisted inner -> inner
        in case matches of
            (m:_) -> m ++ "_R"
            _     -> case runtimeTyped of
                Just goTy -> goTy
                Nothing
                    | isRuntimeOnly -> "any"
                    -- Primitives / containers live inside the alias body
                    -- (e.g. `type alias Id = String`). Inline them.
                    | otherwise     -> case innerType of
                        T.TType _ _ _ -> safeReturnType innerType
                        T.TRecord{}   -> if null base then "any" else base
                        _             -> safeReturnType innerType
    -- Bare TRecord with known fields: match against the codegen env's
    -- record alias registry (field-set → alias name) and emit `_R`.
    -- HM often collapses an alias reference down to its underlying
    -- record (especially after row-polymorphic unification), and
    -- without this path the type would degrade to `any`.
    T.TRecord fields _ ->
        let fieldNames = Map.keys fields
            env = getCgEnv
        in case Rec.lookupRecordAlias (Rec._cg_fieldIndex env) fieldNames of
            Just aliasName -> aliasName ++ "_R"
            Nothing -> "any"
    -- Function types stay `any` rather than emitting
    -- `func(arg) ret`. Go doesn't allow assigning `func(X) Y` to
    -- `func(X) any` (no covariance), so even when the HM-inferred
    -- type is concrete the call site would pass a function with
    -- a different (more specific) return type and fail to compile.
    -- Revisit when Sky has proper Go-generic function types.
    _ -> "any"


-- | Types from Sky runtime that don't have Go type definitions.
-- These map to `any` in Go because they're internal abstractions.
runtimeOnlyTypes :: [String]
runtimeOnlyTypes =
    [ "Decoder", "Value", "Attribute", "Handler"
    , "Route", "Middleware", "Session", "Store"
    ]


-- | Known runtime types that have concrete Go type definitions.
-- These map to their Go type name (with rt. prefix).
-- | Parameterised opaque types that collapse to a Go alias irrespective
-- of their type arguments. `Decoder String`, `Decoder Int`, etc. all
-- emit as `rt.SkyDecoder` because under the hood the runtime uses a
-- single `type SkyDecoder = any`.
opaqueParameterisedGoTy :: String -> Maybe String
opaqueParameterisedGoTy "Decoder" = Just "rt.SkyDecoder"
opaqueParameterisedGoTy "Value"   = Just "rt.SkyValue"
opaqueParameterisedGoTy _         = Nothing


runtimeTypedMap :: [(String, String)]
runtimeTypedMap =
    [ ("VNode",      "rt.VNode")
    , ("Request",    "rt.SkyRequest")
    , ("Response",   "rt.SkyResponse")
    , ("Cmd",        "rt.SkyCmd")
    , ("Sub",        "rt.SkySub")
    -- Opaque Sky types that are effectively `any` under the hood,
    -- but have a dedicated Go alias so the emitted signature names
    -- the abstraction instead of leaking `any`. Each alias is
    -- declared as `type SkyX = any` in runtime-go/rt so there's
    -- no boxing/unboxing overhead and legacy any-typed values
    -- assign/compare transparently. Route is deliberately NOT here:
    -- there's already an exported SkyRoute STRUCT used by the router,
    -- and the Sky-side Route value is an unexported liveRoute struct,
    -- so mapping to SkyRoute would be a lie.
    , ("Decoder",    "rt.SkyDecoder")
    , ("Value",      "rt.SkyValue")
    , ("Attribute",  "rt.SkyAttribute")
    , ("Handler",    "rt.SkyHandler")
    , ("Middleware", "rt.SkyMiddleware")
    , ("Session",    "rt.SkySession")
    , ("Store",      "rt.SkyStore")
    , ("Db",         "rt.SkyDb")
    , ("Stmt",       "rt.SkyStmt")
    , ("Row",        "rt.SkyRow")
    , ("Conn",       "rt.SkyConn")
    ]


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
        T.TType _ "Cmd"    _          -> "rt.SkyCmd"
        T.TType _ "Sub"    _          -> "rt.SkySub"
        T.TType _ "List"   _          -> "[]any"
        T.TType _ "Dict"   _          -> "map[string]any"
        T.TType _ "Set"    _          -> "map[any]bool"
        T.TTuple _ _ []               -> "rt.SkyTuple2"
        T.TTuple _ _ [_]              -> "rt.SkyTuple3"
        T.TTuple _ _ _                -> "rt.SkyTupleN"
        T.TType _ name _ | Just goTy <- opaqueParameterisedGoTy name -> goTy
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
                isRuntimeOnly = name `elem` runtimeOnlyTypes
                runtimeTyped = lookup name runtimeTypedMap
            in case matches of
                (m:_) -> m ++ "_R"
                _     -> case runtimeTyped of
                    Just goTy -> goTy
                    Nothing   -> if isRuntimeOnly then "any" else base
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
                isRuntimeOnly = name `elem` runtimeOnlyTypes
                runtimeTyped = lookup name runtimeTypedMap
                inner = case aliasType of
                    T.Filled i  -> i
                    T.Hoisted i -> i
            in case matches of
                (m:_) -> m ++ "_R"
                _     -> case runtimeTyped of
                    Just goTy -> goTy
                    Nothing
                        | isRuntimeOnly -> "any"
                        | otherwise     -> case inner of
                            T.TRecord{} -> if null base then "any" else base
                            _           -> go inner
        _ -> "any"


-- | Index module decls by binding name so we can check annotations
-- in O(log n) (needed by the HM-dep merge to exclude TypedDefs).
declsByName :: Can.Module -> Map.Map String Can.Def
declsByName canMod = go (Can._decls canMod) Map.empty
  where
    go Can.SaveTheEnvironment acc = acc
    go (Can.Declare d rest) acc = go rest (insertDef d acc)
    go (Can.DeclareRec d ds rest) acc =
        go rest (foldr insertDef (insertDef d acc) ds)
    insertDef d acc = case d of
        Can.Def (A.At _ n) _ _ -> Map.insert n d acc
        Can.TypedDef (A.At _ n) _ _ _ _ -> Map.insert n d acc
        Can.DestructDef _ _ -> acc


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


-- | Split a function's inferred type into Go type parameters, param
-- types, and return type. TVars in the inferred type become Go type
-- parameters (`T1, T2 any`) so partially-inferred functions like
-- `getField : String -> TVar -> String` get typed as
-- `func GetField[T1 any](p0 string, p1 T1) string` instead of all-any.
splitInferredSig :: Int -> T.Type -> ([String], [String], String)
splitInferredSig = splitInferredSigWith Set.empty

-- | Variant of splitInferredSig that takes a record alias set
-- for resolving cross-module record aliases and ADT types.
splitInferredSigWith :: Set.Set String -> Int -> T.Type -> ([String], [String], String)
splitInferredSigWith recAliases = splitInferredSigWithReg recAliases Map.empty

-- | Richer variant that also carries a field-set → alias-name
-- registry so anonymous record types (TRecord) can resolve to their
-- `_R` Go struct name in emitted signatures. Without this, HM-inferred
-- record returns degraded to `any` — the body would still construct a
-- `Foo_R{...}` literal, but the signature wouldn't match.
splitInferredSigWithReg
    :: Set.Set String
    -> Rec.RecordRegistry
    -> Int
    -> T.Type
    -> ([String], [String], String)
splitInferredSigWithReg recAliases fieldIdx arity funcType =
    let (paramTys, retTy) = collectParams arity funcType
        -- TVars in the params get named T1, T2, …. Return-only
        -- TVars intentionally stay un-named (rendered as `any`)
        -- because Go's generic type inference only works from
        -- argument positions — naming them as T_N would force
        -- callers to instantiate explicitly (`foo[int](x)`)
        -- which neither Sky nor the FFI emits.
        paramTVars = uniq (concatMap tvarsInEmitted paramTys)
        numbered = zip paramTVars ["T" ++ show i | i <- [1::Int ..]]
        typeParams = map snd numbered
        paramStrs = map (typeStrWithAliasesReg recAliases fieldIdx numbered) paramTys
        retStr = typeStrWithAliasesReg recAliases fieldIdx numbered retTy
    in (typeParams, paramStrs, retStr)
  where
    collectParams 0 ty = ([], ty)
    collectParams n (T.TLambda from to) =
        let (rest, r) = collectParams (n - 1) to
        in (from : rest, r)
    collectParams _ ty = ([], ty)

    uniq [] = []
    uniq (x:xs) = x : uniq (filter (/= x) xs)


-- | Extract Go param types (legacy API, kept for annotation path).
splitInferredParams :: Int -> T.Type -> [String]
splitInferredParams n t =
    let (_, ps, _) = splitInferredSig n t in ps


-- | Extract Go return type (legacy API, kept for annotation path).
inferredReturnFor :: Int -> T.Type -> String
inferredReturnFor n t =
    let (_, _, r) = splitInferredSig n t in r


-- | All distinct TVar names appearing inside a Type, in left-to-right
-- encounter order.
tvarsIn :: T.Type -> [String]
tvarsIn t = case t of
    T.TVar name
        -- Skip the solver's internal "_cargNNN" / binding-name TVars
        -- that never appear on the user-facing surface — they'd just
        -- clutter the type parameter list.
        | take 1 name == "_" -> [name]
        | length name > 1    -> []
        | otherwise          -> [name]
    T.TLambda a b     -> tvarsIn a ++ tvarsIn b
    T.TType _ _ args  -> concatMap tvarsIn args
    T.TTuple a b cs   -> concatMap tvarsIn (a : b : cs)
    T.TAlias _ _ pairs (T.Filled inner)  -> concatMap tvarsIn (inner : map snd pairs)
    T.TAlias _ _ pairs (T.Hoisted inner) -> concatMap tvarsIn (inner : map snd pairs)
    T.TRecord{}       -> []
    T.TUnit           -> []


-- | Convert a Sky type to Go with a TVar → Go type param substitution.
-- Falls back to safeReturnTypePure for non-TVar nodes.
typeStrWith :: [(String, String)] -> T.Type -> String
typeStrWith = typeStrWithAliases Set.empty

-- | Variant with record alias set for cross-module resolution.
typeStrWithAliases :: Set.Set String -> [(String, String)] -> T.Type -> String
typeStrWithAliases recAliases = typeStrWithAliasesReg recAliases Map.empty

-- | Like `typeStrWithAliases` but additionally consults a field-set →
-- alias-name registry so bare `T.TRecord` nodes (emitted by HM after
-- row-polymorphic unification) resolve to their `_R` Go struct name
-- instead of degrading to `any`.
typeStrWithAliasesReg
    :: Set.Set String
    -> Rec.RecordRegistry
    -> [(String, String)]
    -> T.Type
    -> String
typeStrWithAliasesReg recAliases fieldIdx tvarMap ty = case ty of
    T.TVar name -> case lookup name tvarMap of
        Just gname -> gname
        Nothing    -> "any"
    T.TLambda from to ->
        "func(" ++ go from ++ ") " ++ go to
    T.TType _ "Result" [e, a] ->
        "rt.SkyResult[" ++ go e ++ ", " ++ go a ++ "]"
    T.TType _ "Maybe" [x] ->
        "rt.SkyMaybe[" ++ go x ++ "]"
    T.TType _ "Task" [e, a] ->
        "rt.SkyTask[" ++ go e ++ ", " ++ go a ++ "]"
    T.TType _ "List" _ -> "[]any"
    T.TType _ "Dict" _ -> "map[string]any"
    T.TType _ "Set"  _ -> "map[any]bool"
    -- Cmd/Sub: opaque Go types (ignore inner type param)
    T.TType _ "Cmd" _ -> "rt.SkyCmd"
    T.TType _ "Sub" _ -> "rt.SkySub"
    T.TTuple _ _ []   -> "rt.SkyTuple2"
    T.TTuple _ _ [_]  -> "rt.SkyTuple3"
    T.TTuple _ _ _    -> "rt.SkyTupleN"
    T.TType _ name _ | Just goTy <- opaqueParameterisedGoTy name -> goTy
    -- Primitives (must check before the user-type catch-all)
    T.TType _ "Int" []    -> "int"
    T.TType _ "Float" []  -> "float64"
    T.TType _ "Bool" []   -> "bool"
    T.TType _ "String" [] -> "string"
    T.TType _ "Char" []   -> "rune"
    T.TType _ "Bytes" []  -> "[]byte"
    T.TUnit               -> "struct{}"
    -- User-defined types: resolve via record alias set + runtime map.
    T.TType home name [] ->
        let modStr = ModuleName.toString home
            prefix = if null modStr || modStr == "Main"
                       then ""
                       else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
            base = prefix ++ name
            -- Search all module prefixes for record alias match
            qualifiedCandidates =
                [ p ++ "_" ++ name
                | a <- Set.toList recAliases
                , '_' `elem` a
                , let p = reverse (drop 1 (dropWhile (/= '_') (reverse a)))
                , not (null p)
                ]
            candidates = if null prefix
                           then qualifiedCandidates ++ [name]
                           else base : qualifiedCandidates ++ [name]
            matches = [ c | c <- candidates, Set.member c recAliases ]
            isRuntimeOnly = name `elem` runtimeOnlyTypes
        in case matches of
            (m:_) -> m ++ "_R"
            _     -> case lookup name runtimeTypedMap of
                Just goTy -> goTy
                Nothing   -> if isRuntimeOnly then "any" else base
    T.TAlias home name _ aliasType ->
        let modStr = ModuleName.toString home
            prefix = if null modStr || modStr == "Main"
                       then ""
                       else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
            base = prefix ++ name
            qualifiedCandidates =
                [ p ++ "_" ++ name
                | a <- Set.toList recAliases
                , '_' `elem` a
                , let p = reverse (drop 1 (dropWhile (/= '_') (reverse a)))
                , not (null p)
                ]
            candidates = if null prefix
                           then qualifiedCandidates ++ [name]
                           else base : qualifiedCandidates ++ [name]
            matches = [ c | c <- candidates, Set.member c recAliases ]
            isRuntimeOnly = name `elem` runtimeOnlyTypes
            inner = case aliasType of
                T.Filled  i -> i
                T.Hoisted i -> i
        in case matches of
            (m:_) -> m ++ "_R"
            _     -> case lookup name runtimeTypedMap of
                Just goTy -> goTy
                Nothing
                    | isRuntimeOnly -> "any"
                    | otherwise     -> case inner of
                        T.TRecord{} -> if null base then "any" else base
                        _           -> go inner
    -- Bare anonymous record (HM collapses alias-of-record after row
    -- unification): match its field set against the codegen field-index
    -- registry. Without this, `mkJob id name = { id = ..., name = ... }`
    -- gets type `{id:Int, name:String, ...}` and the sig would degrade
    -- to `any` even though the body emits `Job_R{...}`.
    T.TRecord fields _ ->
        let fieldNames = Map.keys fields
        in case Rec.lookupRecordAlias fieldIdx fieldNames of
            Just aliasName -> aliasName ++ "_R"
            Nothing -> "any"
    _ -> safeReturnTypePure ty
  where
    go = typeStrWithAliasesReg recAliases fieldIdx tvarMap


-- | Collect TVars that survive to the final emitted Go type — i.e. TVars
-- that aren't inside a container type we erase to `any`/`[]any`. Used
-- so we don't declare `[T1 any]` when T1 never appears in the sig.
tvarsInEmitted :: T.Type -> [String]
tvarsInEmitted ty = case ty of
    T.TVar n
        | take 1 n == "_" -> [n]
        | length n > 1    -> []
        | otherwise       -> [n]
    T.TLambda a b -> tvarsInEmitted a ++ tvarsInEmitted b
    -- Container types erase their inner TVars (they become []any etc.)
    -- so TVars inside don't propagate to the Go type.
    T.TType _ "List" _ -> []
    T.TType _ "Dict" _ -> []
    T.TType _ "Set"  _ -> []
    T.TType _ "Result" args -> concatMap tvarsInEmitted args
    T.TType _ "Maybe"  args -> concatMap tvarsInEmitted args
    T.TType _ "Task"   args -> concatMap tvarsInEmitted args
    T.TType _ _ args -> concatMap tvarsInEmitted args
    T.TTuple a b cs -> concatMap tvarsInEmitted (a : b : cs)
    T.TAlias _ _ pairs (T.Filled inner)  -> concatMap tvarsInEmitted (inner : map snd pairs)
    T.TAlias _ _ pairs (T.Hoisted inner) -> concatMap tvarsInEmitted (inner : map snd pairs)
    T.TRecord{} -> []
    T.TUnit     -> []


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
    T.TType _ "Cmd"    _          -> "rt.SkyCmd"
    T.TType _ "Sub"    _          -> "rt.SkySub"
    T.TType _ "List"   _          -> "[]any"
    T.TType _ "Dict"   _          -> "map[string]any"
    T.TType _ "Set"    _          -> "map[any]bool"
    T.TTuple _ _ []               -> "rt.SkyTuple2"
    T.TTuple _ _ [_]              -> "rt.SkyTuple3"
    T.TTuple _ _ _                -> "rt.SkyTupleN"
    T.TType _ name _ | Just goTy <- opaqueParameterisedGoTy name -> goTy
    -- Known runtime types with concrete Go definitions
    T.TType _ name [] | Just goTy <- lookup name runtimeTypedMap -> goTy
    -- safeReturnTypePure has no env access — can't distinguish record
    -- aliases (need _R suffix) from ADTs (use name directly). Fall
    -- back to any for all user types. The env-aware safeReturnType
    -- handles these correctly for annotation-based param types.
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
            -- T4b: if the function is generic (has type params), a bare
            -- reference needs explicit instantiation or Go rejects it
            -- with "cannot infer T1". Instantiate each type param as
            -- `any` so the function-value usage works.
            inferredTypeParams = case Map.lookup qualName (Rec._cg_funcInferredSigs env) of
                Just (tps, _, _) -> tps
                Nothing          -> []
            instantiatedName = if null inferredTypeParams
                then qualName
                else qualName ++ "[" ++ intercalateComma (replicate (length inferredTypeParams) "any") ++ "]"
        in if isZeroArg
            then GoIr.GoCall (GoIr.GoIdent qualName) []
            else GoIr.GoIdent instantiatedName

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
            -- P7 step 5: generalise the zero-arg FFI migration. Any
            -- Sky `KernelMod.fn ()` where (a) the kernel module name
            -- starts with "Go_" (i.e. it's a user-added FFI package,
            -- not a built-in kernel like Sky_Core_*), (b) the call has
            -- a single Unit arg, and (c) FfiGen has emitted a typed
            -- variant `<Kernel>_<fn>T` (registered in the IORef seeded
            -- from ffi/*.go at compile start), routes to the typed
            -- wrapper with no unit arg. Result.withDefault and the
            -- `case _ of Ok/Err` path both accept any SkyResult shape
            -- via reflect, so downstream semantics are preserved.
            Can.VarKernel modName funcName
                | take 3 modName == "Go_"
                , all isUnitArg args
                , let typedName = modName ++ "_" ++ funcName ++ "T"
                , Set.member typedName typedFfiWrapperSet ->
                    GoIr.GoCall (GoIr.GoQualified "rt" typedName) []

            -- P7 step 5b: migrate N-arg FFI by coercing each arg to the
            -- typed wrapper's declared Go param type. `any(arg).(T)`
            -- works for both any-typed and concrete-typed sources — a
            -- no-op in the concrete case. Literal Sky args are still
            -- emitted as Go literals (no `any(...)` wrap) for
            -- readability; Go's literal-to-named-type inference keeps
            -- these compiling.
            Can.VarKernel modName funcName
                | take 3 modName == "Go_"
                , not (null args)
                , not (all isUnitArg args)
                , let typedName = modName ++ "_" ++ funcName ++ "T"
                , Set.member typedName typedFfiWrapperSet
                , Just paramTys <- Map.lookup typedName typedFfiWrapperParams
                , length paramTys == length args ->
                    let anyWrapperName = modName ++ "_" ++ funcName
                    in GoIr.GoCall (GoIr.GoQualified "rt" typedName)
                                (zipWith3 (coerceFfiArgViaAlias anyWrapperName)
                                          [0 :: Int ..]
                                          paramTys
                                          args)

            -- P8 step 4: migrate kernel calls to their typed T companions
            -- when every arg is a primitive Sky literal. Kernels like
            -- `String_toUpper("abc")` gain `String_toUpperT("abc")` —
            -- Go literal-to-named-type inference handles the conversion.
            -- Narrow scope for safety: literal-arg only, matched against
            -- a hand-curated list of simple-param kernels.
            Can.VarKernel modName funcName
                | not (null args)
                , all isPrimLiteralArg args
                , Set.member (modName, funcName) typedKernelLiterals ->
                    let altSuffix = Map.findWithDefault
                            (funcName ++ "T")
                            (modName, funcName)
                            typedKernelAltName
                    in GoIr.GoCall
                        (GoIr.GoQualified "rt" (modName ++ "_" ++ altSuffix))
                        (map exprToGo args)

            -- P8 step 4 widening: kernel typed dispatch for non-literal
            -- args. Coerces each arg via the appropriate runtime helper
            -- (rt.AsInt / rt.AsFloat / rt.AsBool / fmt.Sprintf for
            -- string) so the typed kernel sees a concrete primitive
            -- value. Only fires for kernels whose Sky-level signature
            -- is described in `typedKernelArgCoerce`.
            Can.VarKernel modName funcName
                | not (null args)
                , Just coercers <- Map.lookup (modName, funcName) typedKernelArgCoerce
                , length coercers == length args ->
                    let altSuffix = Map.findWithDefault
                            (funcName ++ "T")
                            (modName, funcName)
                            typedKernelAltName
                    in GoIr.GoCall
                        (GoIr.GoQualified "rt" (modName ++ "_" ++ altSuffix))
                        (zipWith coerceTypedKernelArg coercers args)

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
                    -- Same-module local function calls (`Can.VarLocal`)
                    -- benefit from coerceCallArgs too so typed callees
                    -- get their args asserted at call time. Look up the
                    -- bare name against the entry-module entries we've
                    -- registered in env._cg_funcParamTypes.
                    localQual = case A.toValue func of
                        Can.VarLocal n -> goSafeName n
                        _              -> ""
                    -- Constructor calls: coerce any-typed args to match
                    -- the typed constructor param types (record alias or ADT).
                    ctorParamTypes = case A.toValue func of
                        Can.VarCtor _ home typeName _ (Can.Forall _ ctorTy) ->
                            let modStr = ModuleName.toString home
                                prefix = if null modStr || modStr == "Main"
                                         then "" else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
                                aliasName = prefix ++ typeName
                                env' = getCgEnv
                            in case Map.lookup aliasName (Rec._cg_aliases env') of
                                Just (Can.Alias _ (T.TRecord m _)) ->
                                    let fieldList = List.sortOn (T._fieldIndex . snd) (Map.toList m)
                                    in map (\(_, T.FieldType _ fty) -> solvedTypeToGo fty) fieldList
                                _ ->
                                    -- ADT constructor: extract param types
                                    -- from the annotation's arrow type.
                                    let extractParams (T.TLambda from to) =
                                            safeReturnType from : extractParams to
                                        extractParams _ = []
                                    in extractParams ctorTy
                        _ -> []
                    goArgs
                        | not (null ctorParamTypes) =
                            zipWith (\e ty -> coerceArg e ty) (map exprToGo args) (ctorParamTypes ++ repeat "any")
                        | not (null localQual) =
                            coerceCallArgs localQual args
                        | otherwise = map exprToGo args
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
    -- Parametric container types: use the runtime's cross-instantiation
    -- coerce helpers that reconstruct the value with the target generic
    -- params. Handles SkyMaybe[any] → SkyMaybe[ErrorDetails] etc.
    | Just params <- stripParametric "rt.SkyResult" targetTy =
        GoIr.GoCall (GoIr.GoIdent ("rt.ResultCoerce[" ++ eraseTypeParams params ++ "]")) [e]
    | Just inner <- stripParametric "rt.SkyMaybe" targetTy =
        GoIr.GoCall (GoIr.GoIdent ("rt.MaybeCoerce[" ++ eraseTypeParams inner ++ "]")) [e]
    | isJust (stripParametric "rt.SkyTask" targetTy) =
        GoIr.GoCall (GoIr.GoIdent ("rt.TaskCoerceT[" ++ eraseTypeParams (fromMaybe "" (stripParametric "rt.SkyTask" targetTy)) ++ "]")) [e]
    -- Typed slices: runtime produces []any, so walk-and-cast via
    -- rt.AsListT[T] instead of a hard `any(v).([]T)` assertion.
    | Just elt <- stripListType targetTy =
        GoIr.GoCall (GoIr.GoIdent ("rt.AsListT[" ++ elt ++ "]")) [e]
    -- Typed maps: same pattern for map[string]V.
    | Just valTy <- stripMapType targetTy =
        GoIr.GoCall (GoIr.GoIdent ("rt.AsMapT[" ++ valTy ++ "]")) [e]
    | otherwise =
        let erasedTy = eraseTypeParams targetTy
        in if erasedTy == "any"
             then e
             else GoIr.GoTypeAssert (GoIr.GoCall (GoIr.GoIdent "any") [e]) erasedTy


-- | If `ty` is a Go slice type `[]T` with T ≠ any, return Just "T".
stripListType :: String -> Maybe String
stripListType ty = case ty of
    '[':']':rest | rest /= "any" -> Just rest
    _ -> Nothing


-- | If `ty` is `map[string]V` with V ≠ any, return Just "V".
stripMapType :: String -> Maybe String
stripMapType ty =
    let prefix = "map[string]"
    in if take (length prefix) ty == prefix
        then let v = drop (length prefix) ty
             in if v /= "any" && not (null v) then Just v else Nothing
        else Nothing


-- | Is this arg `()`? Used by P7 typed-FFI migration to recognise the
-- zero-arg Sky call convention `VarKernel _ _ applied to [Unit]`.
isUnitArg :: Can.Expr -> Bool
isUnitArg (A.At _ e) = case e of
    Can.Unit -> True
    _        -> False


-- | Primitive Sky literal args — safe to pass directly to a typed FFI
-- wrapper's concrete Go param type because Go's literal inference
-- produces the matching primitive type. Used by P7 step 5b.
isPrimLiteralArg :: Can.Expr -> Bool
isPrimLiteralArg (A.At _ e) = case e of
    Can.Str _   -> True
    Can.Int _   -> True
    Can.Float _ -> True
    Can.Chr _   -> True
    _           -> False


-- | Kernel (mod, name) pairs with a typed `*T` companion whose param
-- types are all `isCallerVisibleGoType` primitives. Every entry has
-- been verified to match the runtime's typed-companion signature —
-- adding a kernel here without a matching `*T` in runtime-go/rt
-- breaks the build. Conservative list intentionally skips kernels
-- whose typed variant returns a Task-shaped thunk (caller needs to
-- execute it) or takes a slice-of-A (literal args are always scalar).
-- | Per-arg runtime coercer for each kernel that has a typed `*T`
-- companion. Each entry is (mod, fn) → list of coerce names — one
-- per arg. The coerce name is the rt.* function used to convert an
-- any-typed Sky value to the concrete primitive expected by the
-- typed kernel; the call site emits e.g. `rt.String_fromIntT(rt.AsInt(arg))`.
--
-- Coercers:
--   "AsInt" / "AsFloat" / "AsBool" — runtime primitive coercers.
--   "AsString" — fmt.Sprintf("%v", arg) wrapper. Defined here as
--     well, since rt.go uses an inline pattern.
-- | Override the default `<module>_<func>T` suffix for kernels whose
-- typed companion has a different Go name. Used when the default
-- typed companion is generic over element types (requires HM flow)
-- but an `AnyT` variant exists that preserves Sky's `any`-boxed shape
-- without needing type inference.
typedKernelAltName :: Map.Map (String, String) String
typedKernelAltName = Map.fromList
    [ (("Basics", "fst"),        "fstAnyT")
    , (("Basics", "snd"),        "sndAnyT")
    , (("Result", "withDefault"), "withDefaultAnyT")
    , (("Maybe",  "withDefault"), "withDefaultAnyT")
    , (("Dict",   "get"),         "getAnyT")
    , (("List",   "map"),         "mapAny")
    , (("List",   "filter"),      "filterAny")
    , (("List",   "head"),        "headAny")
    , (("List",   "reverse"),     "reverseAny")
    , (("List",   "take"),        "takeAnyT")
    , (("List",   "cons"),        "consAnyT")
    , (("List",   "drop"),        "dropAnyT")
    , (("List",   "foldl"),       "foldlAnyT")
    , (("List",   "foldr"),       "foldrAnyT")
    , (("List",   "filterMap"),   "filterMapAnyT")
    , (("List",   "concatMap"),   "concatMapAnyT")
    , (("List",   "any"),         "anyAnyT")
    , (("List",   "all"),         "allAnyT")
    , (("Result", "map"),         "mapAnyT")
    , (("Result", "andThen"),     "andThenAnyT")
    , (("Result", "mapError"),    "mapErrorAnyT")
    , (("Maybe",  "map"),         "mapAnyT")
    , (("Maybe",  "andThen"),     "andThenAnyT")
    ]


typedKernelArgCoerce :: Map.Map (String, String) [String]
typedKernelArgCoerce = Map.fromList
    -- Single-arg int → string
    [ (("String", "fromInt"),    ["AsInt"])
    , (("String", "fromFloat"),  ["AsFloat"])
    , (("String", "fromChar"),   ["AsInt"])
    -- String → X
    , (("String", "toUpper"),    ["AsString"])
    , (("String", "toLower"),    ["AsString"])
    , (("String", "trim"),       ["AsString"])
    , (("String", "reverse"),    ["AsString"])
    , (("String", "isEmpty"),    ["AsString"])
    , (("String", "length"),     ["AsString"])
    -- (String, String) → X
    , (("String", "contains"),   ["AsString", "AsString"])
    , (("String", "startsWith"), ["AsString", "AsString"])
    , (("String", "endsWith"),   ["AsString", "AsString"])
    , (("String", "append"),     ["AsString", "AsString"])
    , (("String", "split"),      ["AsString", "AsString"])
    , (("String", "slice"),      ["AsInt", "AsInt", "AsString"])
    , (("String", "replace"),    ["AsString", "AsString", "AsString"])
    -- Math
    , (("Math",   "abs"),  ["AsInt"])
    , (("Math",   "min"),  ["AsInt", "AsInt"])
    , (("Math",   "max"),  ["AsInt", "AsInt"])
    , (("Math",   "sqrt"), ["AsFloat"])
    , (("Math",   "pow"),  ["AsFloat", "AsFloat"])
    , (("Math",   "floor"),["AsFloat"])
    , (("Math",   "ceil"), ["AsFloat"])
    , (("Math",   "round"),["AsFloat"])
    , (("Math",   "sin"),  ["AsFloat"])
    , (("Math",   "cos"),  ["AsFloat"])
    , (("Math",   "tan"),  ["AsFloat"])
    , (("Math",   "log"),  ["AsFloat"])
    -- Char (ints used as runes — Sky's Char is rune)
    , (("Char",   "isUpper"),  ["AsInt"])
    , (("Char",   "isLower"),  ["AsInt"])
    , (("Char",   "isDigit"),  ["AsInt"])
    , (("Char",   "isAlpha"),  ["AsInt"])
    , (("Char",   "toUpper"),  ["AsInt"])
    , (("Char",   "toLower"),  ["AsInt"])
    -- Path / Encoding / Regex (single-string args)
    , (("Path",   "dir"),        ["AsString"])
    , (("Path",   "base"),       ["AsString"])
    , (("Path",   "ext"),        ["AsString"])
    , (("Path",   "isAbsolute"), ["AsString"])
    , (("Encoding", "base64Encode"), ["AsString"])
    , (("Encoding", "base64Decode"), ["AsString"])
    , (("Encoding", "urlEncode"),    ["AsString"])
    , (("Encoding", "urlDecode"),    ["AsString"])
    , (("Encoding", "hexEncode"),    ["AsString"])
    , (("Encoding", "hexDecode"),    ["AsString"])
    , (("Regex",  "match"),   ["AsString", "AsString"])
    , (("Regex",  "find"),    ["AsString", "AsString"])
    -- List (single-list arg): dispatch to typed companions that
    -- accept []any. Sky's List elements are always erased to any
    -- at runtime, so `rt.List_lengthT(rt.AsList(xs))` is exactly
    -- the typed shape — Go infers A = any from AsList's return.
    , (("List",   "length"),  ["AsList"])
    , (("List",   "head"),    ["Pass"])
    , (("List",   "reverse"), ["Pass"])
    , (("List",   "isEmpty"), ["AsList"])
    -- Dict: keys/values return []any (updated in rt.go) so they
    -- compose with Sky List ops without []string/[]V mismatch.
    , (("Dict",   "member"),  ["AsString", "AsDict"])
    , (("Dict",   "insert"),  ["AsString", "Pass", "AsDict"])
    , (("Dict",   "keys"),    ["AsDict"])
    , (("Dict",   "values"),  ["AsDict"])
    , (("Dict",   "get"),     ["Pass", "Pass"])
    -- Html.text / Css.hex — simple string → X. High-frequency.
    , (("Html",   "text"),    ["AsString"])
    , (("Css",    "hex"),     ["AsString"])
    , (("Css",    "property"),["AsString", "AsString"])
    , (("Css",    "px"),      ["AsFloat"])
    , (("Css",    "rem"),     ["AsFloat"])
    , (("Attr",   "class"),   ["AsString"])
    -- Log.println: single-arg, any → struct{}{}. Very high-frequency.
    , (("Log",    "println"), ["Pass"])
    , (("Server", "html"),    ["AsString"])
    , (("Server", "redirect"),["AsString"])
    -- List generic helpers: Pass the fn closure, AsList the slice.
    , (("List",   "map"),     ["Pass", "Pass"])
    , (("List",   "filter"),  ["Pass", "Pass"])
    , (("List",   "take"),    ["AsInt", "AsList"])
    , (("List",   "drop"),    ["AsInt", "AsList"])
    , (("List",   "cons"),    ["Pass", "AsList"])
    , (("List",   "foldl"),   ["Pass", "Pass", "AsList"])
    , (("List",   "foldr"),   ["Pass", "Pass", "AsList"])
    , (("List",   "filterMap"), ["Pass", "AsList"])
    , (("List",   "concatMap"), ["Pass", "AsList"])
    , (("List",   "any"),     ["Pass", "AsList"])
    , (("List",   "all"),     ["Pass", "AsList"])
    , (("Result", "map"),     ["Pass", "Pass"])
    , (("Result", "andThen"), ["Pass", "Pass"])
    , (("Result", "mapError"),["Pass", "Pass"])
    , (("Maybe",  "map"),     ["Pass", "Pass"])
    , (("Maybe",  "andThen"), ["Pass", "Pass"])
    -- Basics: pure boolean / integer helpers
    , (("Basics", "not"),     ["AsBool"])
    , (("Basics", "modBy"),   ["AsInt", "AsInt"])
    , (("Basics", "errorToString"), ["Pass"])
    -- Basics.fst/snd: dispatch to fstAnyT/sndAnyT via typedKernelAltName;
    -- they preserve the any-boxed element type without requiring HM flow.
    , (("Basics", "fst"),     ["AsTuple2"])
    , (("Basics", "snd"),     ["AsTuple2"])
    , (("Basics", "identity"), ["Pass"])
    , (("Result", "withDefault"), ["Pass", "Pass"])
    , (("Maybe",  "withDefault"), ["Pass", "Pass"])
    -- Time formatters: Int → String
    , (("Time",   "formatISO8601"), ["AsInt"])
    , (("Time",   "formatRFC3339"), ["AsInt"])
    , (("Time",   "formatHTTP"),    ["AsInt"])
    ]


-- | Render a single arg coerced through the named runtime helper.
-- "AsString" is special-cased as `fmt.Sprintf("%v", arg)` to match
-- the existing convention; AsInt / AsFloat / AsBool are direct
-- rt.* calls. Literals pass through as-is.
coerceTypedKernelArg :: String -> Can.Expr -> GoIr.GoExpr
coerceTypedKernelArg coercer arg
    | isPrimLiteralArg arg = exprToGo arg
    -- "Pass" erases the arg's concrete type via `any(arg)` so Go
    -- generic inference picks V=any uniformly (e.g. Dict_insertT[V]
    -- where the dict side came in as map[string]any via AsDict).
    | coercer == "Pass" = GoIr.GoCall (GoIr.GoIdent "any") [exprToGo arg]
    | otherwise =
        GoIr.GoCall (GoIr.GoQualified "rt" coercer) [exprToGo arg]


typedKernelLiterals :: Set.Set (String, String)
typedKernelLiterals = Set.fromList
    [ ("String", "toUpper"),    ("String", "toLower"),    ("String", "trim")
    , ("String", "reverse"),    ("String", "isEmpty"),    ("String", "length")
    , ("String", "contains"),   ("String", "startsWith"), ("String", "endsWith")
    , ("String", "append"),     ("String", "fromInt"),    ("String", "fromFloat")
    , ("String", "replace"),    ("String", "slice")
    , ("Math",   "abs"),        ("Math",   "min"),        ("Math",   "max")
    , ("Math",   "sqrt"),       ("Math",   "pow"),        ("Math",   "floor")
    , ("Math",   "ceil"),       ("Math",   "round"),      ("Math",   "sin")
    , ("Math",   "cos"),        ("Math",   "tan"),        ("Math",   "log")
    , ("Char",   "isUpper"),    ("Char",   "isLower"),    ("Char",   "isDigit")
    , ("Char",   "isAlpha"),    ("Char",   "toUpper"),    ("Char",   "toLower")
    , ("Path",   "dir"),        ("Path",   "base"),       ("Path",   "ext")
    , ("Path",   "isAbsolute")
    , ("Encoding", "base64Encode"), ("Encoding", "base64Decode")
    , ("Encoding", "urlEncode"),    ("Encoding", "urlDecode")
    , ("Encoding", "hexEncode"),    ("Encoding", "hexDecode")
    , ("Regex",  "match"),      ("Regex",  "find"),       ("Regex",  "replace")
    , ("List",   "length"),     ("List",   "head"),       ("List",   "reverse")
    , ("List",   "isEmpty")
    , ("Dict",   "member"),     ("Dict",   "insert")
    , ("Dict",   "keys"),       ("Dict",   "values"),   ("Dict", "get")
    , ("Html",   "text"),       ("Css",    "hex")
    , ("Css",    "property"),   ("Css",    "px"),       ("Css", "rem")
    , ("Attr",   "class"),     ("Log",    "println")
    , ("Server", "html"),      ("Server", "redirect")
    , ("List",   "map"),       ("List",   "filter"),     ("List", "take"), ("List", "cons")
    , ("List",   "drop"),      ("List",   "foldl"),      ("List", "foldr")
    , ("List",   "filterMap"), ("List",   "concatMap"),  ("List", "any"), ("List", "all")
    , ("Result", "map"),       ("Result", "andThen"),    ("Result", "mapError")
    , ("Maybe",  "map"),       ("Maybe",  "andThen")
    , ("Basics", "not"),        ("Basics", "modBy"),  ("Basics", "errorToString")
    , ("Time",   "formatISO8601"), ("Time", "formatRFC3339"), ("Time", "formatHTTP")
    , ("Basics", "fst"),        ("Basics", "snd"),   ("Basics", "identity")
    , ("Result", "withDefault"), ("Maybe",  "withDefault")
    ]


-- | Snapshot of Env.ffiTypedWrapperNamesRef taken at every lookup. The
-- unsafePerformIO is fine here: the set is populated once at compile
-- start (before canonicalisation runs) and never mutated afterwards.
{-# NOINLINE typedFfiWrapperSet #-}
typedFfiWrapperSet :: Set.Set String
typedFfiWrapperSet = unsafePerformIO (readIORef Env.ffiTypedWrapperNamesRef)


-- | Companion snapshot of typed-wrapper param Go types, keyed by the
-- T-suffix wrapper name. See typedFfiWrapperSet for the invariant.
{-# NOINLINE typedFfiWrapperParams #-}
typedFfiWrapperParams :: Map.Map String [String]
typedFfiWrapperParams = unsafePerformIO (readIORef Env.ffiTypedWrapperParamsRef)


-- | Typed-wrapper param types that the sky-out/main.go call site can
-- actually reference. Typed wrappers reference file-local aliases
-- (`pkg`, `stripe_go`, etc.) that don't exist in main.go — so we can
-- only migrate N-arg calls whose param types are expressible without
-- those file-local aliases. Safe: Go primitives. Unsafe: any
-- dot-qualified type. Future work: record the main.go-visible import
-- aliases as part of the FFI registry so more types become callable.
isCallerVisibleGoType :: String -> Bool
isCallerVisibleGoType t =
    -- `interface{}` and `any` are Go's empty interface — main.go can
    -- always use them. Treat the raw string match (not the bare-strip)
    -- since dropping `*` from `*interface{}` gives nonsense.
    if t == "interface{}" || t == "any" then True
    else
      let bare = dropWhile (\c -> c == '*' || c == '[' || c == ']' || c == ' ') t
      in bare `elem`
          [ "string", "int", "int8", "int16", "int32", "int64"
          , "uint", "uint8", "uint16", "uint32", "uint64"
          , "float32", "float64", "bool", "byte", "rune", "error"
          ]


-- | Coerce a Sky arg to a concrete Go type at a typed FFI call site.
-- Literal args (Str/Int/Float/Chr) render to native Go literals that
-- Go's type inference matches to the target param type directly —
-- inserting `any(1).(int)` would actually break, since `any(1)` boxes
-- the literal and asserting back loses the native-type view. For
-- everything else, route through `rt.Coerce[T]()` which handles
-- the full matrix: direct type assertion when the runtime type
-- matches, reflect-based numeric widening, and (post-skyshop-fix)
-- slice coercion (`[]any` → `[]ConcreteT`) so an empty list `[]`
-- in Sky can flow into a Go function expecting `[]option.ClientOption`.
coerceFfiArg :: String -> Can.Expr -> GoIr.GoExpr
coerceFfiArg goType arg =
    let goArg = exprToGo arg
    in if isPrimLiteralArg arg
        then goArg
        else coerceVia goType goArg


-- | Call-site coercion that consults the `rt.FfiT_<Name>_P<N>` alias
-- when the target param type isn't a caller-visible primitive. FfiGen
-- emits those aliases alongside every typed wrapper whose params or
-- return reference an FFI-file-local type, so main.go can cast
-- through them without needing the underlying Go package import.
coerceFfiArgViaAlias :: String -> Int -> String -> Can.Expr -> GoIr.GoExpr
coerceFfiArgViaAlias anyWrapperName idx goType arg
    | isPrimLiteralArg arg   = exprToGo arg
    | isCallerVisibleGoType goType =
        coerceVia goType (exprToGo arg)
    | otherwise =
        let aliasName = "rt.FfiT_" ++ anyWrapperName ++ "_P" ++ show idx
        in coerceVia aliasName (exprToGo arg)


-- | Emit `rt.Coerce[T](expr)` (or the named shortcut for prim
-- types) so typed FFI boundaries handle representation mismatches
-- ([]any → []ConcreteT, struct reinterpret, numeric widening)
-- instead of panicking on a raw `.(T)` assertion.
coerceVia :: String -> GoIr.GoExpr -> GoIr.GoExpr
coerceVia goType goArg = case goType of
    "string"  -> GoIr.GoCall (GoIr.GoIdent "rt.CoerceString") [goArg]
    "int"     -> GoIr.GoCall (GoIr.GoIdent "rt.CoerceInt") [goArg]
    "bool"    -> GoIr.GoCall (GoIr.GoIdent "rt.CoerceBool") [goArg]
    "float64" -> GoIr.GoCall (GoIr.GoIdent "rt.CoerceFloat") [goArg]
    _         -> GoIr.GoCall (GoIr.GoIdent ("rt.Coerce[" ++ goType ++ "]")) [goArg]


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
    -- Generic type parameter (T1, T2, ...) — we can't assert to it
    -- from the caller side since it's scoped to the callee. Let Go's
    -- type inference figure it out from the usage. Pass raw.
    | isGenericTypeParam ty = e
    | Just params <- stripParametric "rt.SkyResult" ty =
        GoIr.GoCall (GoIr.GoIdent ("rt.ResultCoerce[" ++ eraseTypeParams params ++ "]")) [e]
    | Just inner <- stripParametric "rt.SkyMaybe" ty =
        GoIr.GoCall (GoIr.GoIdent ("rt.MaybeCoerce[" ++ eraseTypeParams inner ++ "]")) [e]
    | ty == "string" = GoIr.GoCall (GoIr.GoIdent "rt.CoerceString") [e]
    | ty == "int"    = GoIr.GoCall (GoIr.GoIdent "rt.CoerceInt") [e]
    | ty == "bool"   = GoIr.GoCall (GoIr.GoIdent "rt.CoerceBool") [e]
    | ty == "float64"= GoIr.GoCall (GoIr.GoIdent "rt.CoerceFloat") [e]
    -- Target is []any: accept either `[]any` source or concrete
    -- `[]T` source via rt.AsListAny which widens.
    | ty == "[]any" =
        GoIr.GoCall (GoIr.GoIdent "rt.AsListAny") [e]
    -- Typed slice `[]T`: runtime may hand us `[]any`, walk-and-cast.
    | Just elt <- stripListType ty =
        GoIr.GoCall (GoIr.GoIdent ("rt.AsListT[" ++ elt ++ "]")) [e]
    -- map[string]V: typed dict.
    | Just valTy <- stripMapType ty =
        GoIr.GoCall (GoIr.GoIdent ("rt.AsMapT[" ++ valTy ++ "]")) [e]
    | otherwise =
        let erasedTy = eraseTypeParams ty
        in if erasedTy == "any"
             then e  -- fully erased to any — no assertion needed
             else GoIr.GoTypeAssert
                    (GoIr.GoCall (GoIr.GoIdent "any") [e]) erasedTy

-- | True when a Go type string is a generic type parameter name we
-- emitted (T1, T2, ...). These are scoped to the function they were
-- declared on, so callers can't type-assert against them.
isGenericTypeParam :: String -> Bool
isGenericTypeParam ('T':rest) = all (\c -> c >= '0' && c <= '9') rest && not (null rest)
isGenericTypeParam _ = False

-- | Replace callee-scoped type params (T1, T2, ...) with `any` in
-- type strings so call-site coercions are valid.
-- E.g. "any, T1" → "any, any", "func(T1) func(T2) any" → "func(any) func(any) any".
-- Does NOT replace T2 in "rt.T2[...]" — only standalone identifiers.
eraseTypeParams :: String -> String
eraseTypeParams = go Nothing
  where
    go _ [] = []
    go prev ('T':rest)
        | not (maybe False isIdChar prev)  -- not preceded by ident char
        , (digits, after) <- span (\c -> c >= '0' && c <= '9') rest
        , not (null digits)
        , null after || not (isIdChar (head after))
        = "any" ++ go (Just 'y') after  -- 'y' from "any"
    go _ (c:cs) = c : go (Just c) cs
    isIdChar c = (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
              || (c >= '0' && c <= '9') || c == '_' || c == '.'

intercalateComma :: [String] -> String
intercalateComma []     = ""
intercalateComma [x]    = x
intercalateComma (x:xs) = x ++ ", " ++ intercalateComma xs

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
    -- Reify `value |> func args` as a regular Can.Call so it goes
    -- through the same exprToGo Can.Call branch — this picks up the
    -- typed-FFI / typed-kernel migrations that would otherwise be
    -- missed by the bypass that calls exprToGo on each piece directly.
    let region = case funcExpr of A.At r _ -> r
        synth f xs = exprToGo (A.At region (Can.Call f xs))
    in case funcExpr of
        A.At _ (Can.Call innerFunc innerArgs) ->
            synth innerFunc (innerArgs ++ [valueExpr])
        _ ->
            synth funcExpr [valueExpr]


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
            | Just _ <- stripParametric "rt.SkyResult" typeName, isTypedFfiCall e =
                -- P7: typed-FFI source returns SkyResult[string, A]
                -- directly. Leave __subject at its concrete type —
                -- bindCtorArg detects this via the same predicate
                -- and emits `__subject.OkValue` without a
                -- SkyResult[any,any] assertion. Net: zero runtime
                -- boxing between FFI and case body.
                e
            | Just params <- stripParametric "rt.SkyResult" typeName =
                GoIr.GoCall (GoIr.GoIdent ("rt.ResultCoerce[" ++ params ++ "]")) [e]
            | Just _ <- stripParametric "rt.SkyMaybe" typeName, isTypedFfiCall e =
                e
            | Just inner <- stripParametric "rt.SkyMaybe" typeName =
                GoIr.GoCall (GoIr.GoIdent ("rt.MaybeCoerce[" ++ inner ++ "]")) [e]
            | otherwise =
                GoIr.GoTypeAssert (anyWrapped e) typeName


        -- Peek through the GoExpr tree for sources whose Go type is
        -- known to be a concrete SkyResult / SkyMaybe struct (not an
        -- `any` interface), so the case-subject emission can elide
        -- the ResultCoerce reflect dance. Two recognisers:
        --   * Typed FFI calls (`rt.Go_X_yT(...)`) — fixed naming
        --     convention, registered in typedFfiWrapperSet.
        --   * Sky top-level function calls whose return type starts
        --     with `rt.SkyResult[` or `rt.SkyMaybe[` per the codegen
        --     env's funcRetType map (populated by HM inference).
        isTypedFfiCall expr = case expr of
            GoIr.GoCall (GoIr.GoQualified "rt" fnName) _
                | take 3 fnName == "Go_"
                , not (null fnName)
                , last fnName == 'T'
                , Set.member fnName typedFfiWrapperSet
                -> True
            GoIr.GoCall (GoIr.GoIdent qualName) _
                | Just retTy <- Map.lookup qualName funcRetTypeMap
                , isConcreteResultOrMaybe retTy
                -> True
            GoIr.GoCall (GoIr.GoIdent qualName) _
                | Just (_, _, retTy) <- Map.lookup qualName inferredSigMap
                , isConcreteResultOrMaybe retTy
                -> True
            _ -> False

        funcRetTypeMap = Rec._cg_funcRetType getCgEnv
        inferredSigMap = Rec._cg_funcInferredSigs getCgEnv

        isConcreteResultOrMaybe t =
            let isResult = "rt.SkyResult[" `List.isPrefixOf` t
                         && not ("rt.SkyResult[any, any]" `List.isPrefixOf` t)
                isMaybe  = "rt.SkyMaybe[" `List.isPrefixOf` t
                         && not ("rt.SkyMaybe[any]" `List.isPrefixOf` t)
            in isResult || isMaybe
        -- P7: typed-FFI-source subjects use a distinct name so
        -- bindCtorArg knows to skip the `any().(SkyResult[any,any])`
        -- assertion step. Saves one boxing per typed case match.
        subjectName =
            case subjectType of
                Just typeName
                    | isJust (stripParametric "rt.SkyResult" typeName) && isTypedFfiCall goSubject
                    -> "__subject_tFfi"
                    | isJust (stripParametric "rt.SkyMaybe" typeName) && isTypedFfiCall goSubject
                    -> "__subject_tFfi"
                _ -> "__subject"
        subjectDecl = case subjectType of
            Just typeName ->
                GoIr.GoShortDecl subjectName (coerceSubject typeName goSubject)
            Nothing ->
                GoIr.GoShortDecl subjectName goSubject
        branchStmts = concatMap (caseBranchToStmts subjectName) branches
        -- P3: exhaustiveness is verified before codegen, so this arm is
        -- statically unreachable. Audit P0-5: route through
        -- rt.Unreachable instead of a raw panic so any future
        -- exhaustiveness-vs-codegen drift surfaces as a clean Err at
        -- the Task boundary (rt's panic-recovery catches the panic
        -- that Unreachable raises) rather than killing the process.
        -- The site identifier lets on-call locate the originating
        -- case block in logs.
        unreachableStmt = GoIr.GoExprStmt
            (GoIr.GoRaw ("_ = rt.Unreachable(\"case/" ++ subjectName ++ "\")"))
    in
    GoIr.GoBlock
        (subjectDecl : branchStmts ++ [unreachableStmt])
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
                -- Enum: zero-arg ADT. Route through rt.EnumTagIs so
                -- values arriving from rt builders (SkyADT with the
                -- matching Tag) compare equal to the typed-int
                -- constant codegen would otherwise emit. Without
                -- this, `case (error.kind) of Io -> …` lowered by
                -- codegen would never match an rt.ErrIo-built kind
                -- because `Sky_Core_Error_ErrorKind(0) != SkyADT{Tag:0}`
                -- under Go's `any == any` rules — the Sky case would
                -- fall through to rt.Unreachable.
                --
                -- Keeping the named constants live for elsewhere
                -- (debugger strings, direct construction) is free —
                -- they still compile, this branch just doesn't use
                -- `==` on them.
                let _modStr = ModuleName.toString home
                    _qualName = if null _modStr || _modStr == "Main"
                        then typeName ++ "_" ++ ctorName
                        else (map (\c -> if c == '.' then '_' else c) _modStr)
                             ++ "_" ++ typeName ++ "_" ++ ctorName
                in Just $ GoIr.GoCall
                    (GoIr.GoQualified "rt" "EnumTagIs")
                    [ GoIr.GoIdent subject
                    , GoIr.GoIntLit ctorIdx
                    ]
            _ ->
                -- Tagged struct: match outer .Tag AND recurse into every
                -- ctor arg that carries a sub-pattern condition. Without
                -- the recursion, `Ok Nothing` and `Ok (Just x)` both
                -- collapse to `subject.Tag == 0` and the first matching
                -- branch swallows every Ok case — a silent soundness
                -- bug (skyvote sign-up: 'Account created but auto-login
                -- failed' showed even when the user was found).
                let outer = GoIr.GoBinary "=="
                        (GoIr.GoSelector (GoIr.GoIdent subject) "Tag")
                        (GoIr.GoIntLit ctorIdx)
                    inners =
                        [ c
                        | Can.PatternCtorArg idx _ (A.At _ argPat) <- args
                        , Just c <- [argPatternCondition subject ctorName idx argPat]
                        ]
                in Just $ foldl (GoIr.GoBinary "&&") outer inners

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


-- | Condition for a sub-pattern sitting inside a ctor argument. Uses
-- the same field accessor that bindCtorArg would use (`.OkValue`,
-- `.ErrValue`, `.JustValue`, or `.Fields[idx]`). Returns Nothing for
-- catch-all sub-patterns (PVar / PAnything / PUnit) whose presence
-- doesn't restrict the outer ctor match.
--
-- Only emits a condition for sub-patterns that actually narrow the
-- match — nested ctor, literal, bool, char, list-length, cons.
argPatternCondition :: String -> String -> Int -> Can.Pattern_ -> Maybe GoIr.GoExpr
argPatternCondition subject ctorName idx pat = case pat of
    -- Catch-alls and always-match shapes: no condition.
    Can.PAnything  -> Nothing
    Can.PVar _     -> Nothing
    Can.PUnit      -> Nothing
    Can.PTuple{}   -> Nothing
    Can.PRecord _  -> Nothing

    -- Alias: recurse through to the inner pattern, same accessor.
    Can.PAlias inner _ ->
        let (A.At _ innerPat) = inner
        in argPatternCondition subject ctorName idx innerPat

    _ ->
        -- Build the Go expression for the sub-value using bindCtorArg's
        -- naming convention. Source type may be:
        --   * `any` — needs `.(SkyResult[any,any])` etc. to read .Tag.
        --   * typed `SkyResult[any, X]` / `SkyMaybe[X]` — direct field access.
        --   * a `_tFfi`-suffix variable — always concrete; direct access.
        -- Using `rt.AdtTag(v)` / `rt.ResultTag(v)` / `rt.MaybeTag(v)`
        -- runtime helpers instead of a type-assert avoids the
        -- `SkyMaybe[string] not SkyMaybe[any]` panic class.
        let accessor = case ctorName of
                "Ok"   -> GoIr.GoSelector (GoIr.GoIdent subject) "OkValue"
                "Err"  -> GoIr.GoSelector (GoIr.GoIdent subject) "ErrValue"
                "Just" -> GoIr.GoSelector (GoIr.GoIdent subject) "JustValue"
                _      -> GoIr.GoIndex
                            (GoIr.GoSelector (GoIr.GoIdent subject) "Fields")
                            (GoIr.GoIntLit idx)
            tagCast ty = GoIr.GoTypeAssert
                            (GoIr.GoCall (GoIr.GoIdent "any") [accessor])
                            ty
            tagHelperFor helper =
                GoIr.GoCall (GoIr.GoQualified "rt" helper)
                    [GoIr.GoCall (GoIr.GoIdent "any") [accessor]]
        in case pat of
            Can.PCtor home innerTypeName innerUnion innerCtor innerIdx _innerArgs ->
                case Can._u_opts innerUnion of
                    Can.Enum
                        -- Bool is special: canonically an enum (True/False)
                        -- but at runtime Sky uses Go `bool`, not an int
                        -- constant. Compare via `any(x).(bool) == true/false`.
                        | innerTypeName == "Bool" ->
                            Just $ GoIr.GoBinary "=="
                                (tagCast "bool")
                                (GoIr.GoBoolLit (innerCtor == "True"))
                        | otherwise ->
                            let modStr = ModuleName.toString home
                                qualName =
                                    if null modStr || modStr == "Main"
                                        then innerTypeName ++ "_" ++ innerCtor
                                        else map (\c -> if c == '.' then '_' else c) modStr
                                             ++ "_" ++ innerTypeName ++ "_" ++ innerCtor
                            in Just $ GoIr.GoBinary "=="
                                    (tagCast "int")
                                    (GoIr.GoIdent qualName)
                    _ ->
                        -- Read .Tag via runtime helper that tolerates
                        -- any generic instantiation of SkyResult/SkyMaybe.
                        -- User ADTs are rt.SkyADT aliases so direct
                        -- `.Tag` works; still route through rt.AdtTag
                        -- for consistency and to accept any-typed
                        -- sources without an extra cast.
                        let innerTag = case innerCtor of
                                "Ok"       -> tagHelperFor "ResultTag"
                                "Err"      -> tagHelperFor "ResultTag"
                                "Just"     -> tagHelperFor "MaybeTag"
                                "Nothing"  -> tagHelperFor "MaybeTag"
                                _          -> tagHelperFor "AdtTag"
                        in Just $ GoIr.GoBinary "=="
                                innerTag
                                (GoIr.GoIntLit innerIdx)

            Can.PInt n   -> Just (GoIr.GoBinary "==" (tagCast "int")    (GoIr.GoIntLit n))
            Can.PStr s   -> Just (GoIr.GoBinary "==" (tagCast "string") (GoIr.GoStringLit s))
            Can.PBool b  -> Just (GoIr.GoBinary "==" (tagCast "bool")   (GoIr.GoBoolLit b))
            Can.PChr c   -> Just (GoIr.GoBinary "==" (tagCast "rune")   (GoIr.GoRuneLit c))
            _            -> Nothing


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
            -- Wrap subject in `any(...)` before the type assertion so
            -- it works regardless of whether subject is already
            -- SkyTuple2 (typed return, concrete struct) or `any`
            -- (legacy any-path). Go rejects `.(T)` on a non-interface,
            -- but `any(x).(T)` always compiles.
            asTup k = GoIr.GoTypeAssert
                (GoIr.GoCall (GoIr.GoIdent "any") [GoIr.GoIdent subject])
                ("rt." ++ k)
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
        -- P7: a subject name suffixed with "_tFfi" is the typed-FFI
        -- shortcut — the outer caseToGo already guarantees it's a
        -- SkyResult[_, _] or SkyMaybe[_] struct, so we can field-
        -- access directly without a `(any).(SkyResult[any, any])`
        -- assertion. Wrapping the field access in any() preserves
        -- the any-typed binding contract for downstream branch code.
        -- Only the OUTER case's subject carries the typed-FFI shape.
        -- Nested destructure temps (`__sky_cf_N_<parent>`) inherit the
        -- suffix textually but are `any`-typed — reject them so
        -- patternBindings falls through to the any-assertion path.
        isTypedFfiSubject =
            take 5 (reverse subject) == "ifFt_"
            && not ("__sky_cf_" `List.isPrefixOf` subject)
        anyWrap n = GoIr.GoCall (GoIr.GoIdent "any") [GoIr.GoIdent n]
        -- Runtime helper unwraps any SkyResult/SkyMaybe instantiation
        -- without a type-assertion panic — used when the subject is
        -- any-typed (nested destructure temps, non-_tFfi subjects
        -- whose runtime type could differ from SkyResult[any,any]).
        helperFor helper = GoIr.GoCall
            (GoIr.GoQualified "rt" helper)
            [anyWrap subject]
        rawField = case ctorName of
            _ | isTypedFfiSubject ->
                case ctorName of
                    "Ok"   -> GoIr.GoSelector (GoIr.GoIdent subject) "OkValue"
                    "Err"  -> GoIr.GoSelector (GoIr.GoIdent subject) "ErrValue"
                    "Just" -> GoIr.GoSelector (GoIr.GoIdent subject) "JustValue"
                    _      -> GoIr.GoIndex
                                (GoIr.GoSelector (GoIr.GoIdent subject) "Fields")
                                (GoIr.GoIntLit idx)
            "Ok"   -> helperFor "ResultOk"
            "Err"  -> helperFor "ResultErr"
            "Just" -> helperFor "MaybeJust"
            _      ->
                -- Custom ADT: use rt.AdtField runtime helper so an
                -- any-typed subject (e.g. bound from
                -- rt.ResultOk/ErrValue above, or a nested destructure
                -- temp) still reads .Fields[idx] without requiring a
                -- type-assertion to the emitted ADT struct.
                GoIr.GoCall
                    (GoIr.GoQualified "rt" "AdtField")
                    [GoIr.GoCall (GoIr.GoIdent "any") [GoIr.GoIdent subject], GoIr.GoIntLit idx]
        fieldAccess =
            if isTypedFfiSubject
                then GoIr.GoCall (GoIr.GoIdent "any") [rawField]
                else rawField
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


-- | Generate Go for main body expressions. Delegates to the standard
-- exprToGo so VarTopLevel/VarCtor call-site coercion kicks in at main
-- call sites just like anywhere else — main used to have a parallel
-- codegen path that skipped coerceCallArgs, causing typed callee args
-- to fail at go build when called from `main`.
exprToGoMain :: Solve.SolvedTypes -> Can.Expr -> GoIr.GoExpr
exprToGoMain _types = exprToGo


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
            -- assert the result. This handles: n * factorial(n-1) where factorial returns any.
            -- BUT: if the callee is itself emitted with a fully-typed Go signature
            -- (concrete params + concrete return, fully applied), the Go call already
            -- yields a concrete value and asserting `.(T)` on it would be a Go error.
            calleeInfo = case func of
                A.At _ (Can.VarTopLevel _ name) ->
                    case Map.lookup name types of
                        Just ft ->
                            let (argTys, rtTy) = splitFuncType (length args) ft
                                fullyTyped = length argTys == length args
                                           && isConcreteType rtTy
                                           && all isConcreteType argTys
                            in Just (rtTy, fullyTyped)
                        Nothing -> Nothing
                A.At _ (Can.VarLocal name) ->
                    case Map.lookup name types of
                        Just ft ->
                            let (_, rtTy) = splitFuncType (length args) ft
                            in Just (rtTy, False)  -- VarLocal calls go through any-dispatch
                        Nothing -> Nothing
                _ -> Nothing
        in case calleeInfo of
            Just (_, True) -> callExpr  -- typed-emitted callee returns concrete directly
            Just (rt, False) | isConcreteType rt ->
                GoIr.GoTypeAssert callExpr (solvedTypeToGo rt)
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
    -- Container types: emit concrete Go generic instantiations.
    -- The body codegen must produce matching types (e.g. Nothing[T]()
    -- not Nothing[any]()). Monomorphisation ensures this.
    -- Typed slices: emit `[]T` for known element types. The
    -- runtime-produced `[]any` gets converted at assignment
    -- boundaries via `rt.AsListT[T]` in coerceToFieldType.
    T.TType _ "List" [elem] ->
        let elemGo = solvedTypeToGo elem
        in if elemGo == "any" then "[]any" else "[]" ++ elemGo
    T.TType _ "List" _ -> "[]any"
    T.TType _ "Cmd" _ -> "rt.SkyCmd"
    T.TType _ "Sub" _ -> "rt.SkySub"
    T.TType _ "Maybe" [a] ->
        "rt.SkyMaybe[" ++ solvedTypeToGo a ++ "]"
    T.TType _ "Maybe" _ -> "rt.SkyMaybe[any]"
    T.TType _ "Result" [e, a] ->
        "rt.SkyResult[" ++ solvedTypeToGo e ++ ", " ++ solvedTypeToGo a ++ "]"
    T.TType _ "Result" _ -> "rt.SkyResult[any, any]"
    T.TType _ "Task" [e, a] ->
        "rt.SkyTask[" ++ solvedTypeToGo e ++ ", " ++ solvedTypeToGo a ++ "]"
    T.TType _ "Task" _ -> "rt.SkyTask[any, any]"
    -- Dict values: emit `map[string]V` for known value types;
    -- boundary conversion via rt.AsMapT[V] in coerceToFieldType.
    T.TType _ "Dict" [_, v] ->
        "map[string]" ++ solvedTypeToGo v
    T.TType _ "Dict" _ -> "map[string]any"
    T.TType _ "Set" _ -> "map[any]bool"
    T.TType home name _ ->
        let modStr = ModuleName.toString home
            base = if null modStr || modStr == "Main"
                then name
                else map (\c -> if c == '.' then '_' else c) modStr ++ "_" ++ name
            env = getCgEnv
            isRecordAlias = Set.member base (Rec._cg_recordAliases env)
                         || Set.member name (Rec._cg_recordAliases env)
            isRuntimeOnly = name `elem` runtimeOnlyTypes
        in if isRecordAlias then base ++ "_R"
           else case lookup name runtimeTypedMap of
             Just goTy -> goTy
             Nothing   -> if isRuntimeOnly then "any" else base
    T.TLambda from to -> "func(" ++ solvedTypeToGo from ++ ") " ++ solvedTypeToGo to
    T.TRecord fields _ ->
        -- P4: records always map to a named Go struct. If the shape
        -- matches a registered alias we use its `_R` name. Otherwise
        -- the anon-registry synthesises a deterministic `Anon_R_<hash>`
        -- name; the pre-pass emits its struct decl alongside the alias
        -- decls so Go can resolve it.
        let env = getCgEnv
            names = Map.keys fields
            nonMatch = case Rec.lookupRecordAlias (Rec._cg_fieldIndex env) names of
                Just aliasName -> aliasName ++ "_R"
                Nothing        -> synthAnonRecordName fields
        in nonMatch
    T.TTuple a b rest ->
        -- P5: typed tuples. Arity 2-5 maps to rt.T2..T5 with concrete
        -- element Go types. Arity >= 6 stays as rt.SkyTupleN (slice-
        -- backed, heterogeneous) per the plan's "record-alias instead"
        -- guidance. rt.T2[any, any] is a type alias for SkyTuple2, so
        -- literal-site codegen continues to emit SkyTuple2{...} without
        -- friction.
        let goEls = map solvedTypeToGo (a : b : rest)
            arity = length goEls
        in case arity of
            2 -> "rt.T2[" ++ intercalate_ ", " goEls ++ "]"
            3 -> "rt.T3[" ++ intercalate_ ", " goEls ++ "]"
            4 -> "rt.T4[" ++ intercalate_ ", " goEls ++ "]"
            5 -> "rt.T5[" ++ intercalate_ ", " goEls ++ "]"
            _ -> "rt.SkyTupleN"
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


-- | Combine exhaustiveness diagnostics into a single user-facing string.
-- Each diagnostic reports a missing-pattern set plus a short hint. We
-- emit one line per diagnostic; the caller prefixes "Non-exhaustive
-- patterns:".
renderExhaustDiags :: [Exhaust.Diag] -> String
renderExhaustDiags ds = intercalate_ "\n  " (map render1 ds)
  where
    render1 (Exhaust.Diag region _missing hint) =
        let A.Region (A.Position l c) _ = region
        in "at line " ++ show l ++ ":" ++ show c ++ " — " ++ hint


-- | Synthesise a deterministic Go struct name for an anonymous record.
-- Keyed by the full (fieldName, fieldType) shape so records with the
-- same field names but different field types are distinct Go types
-- (per P4). Format: `Anon_R_<sorted names>__<short hash of types>`.
--
-- The hash is a simple polynomial over the Show-representation of the
-- field types. It isn't cryptographic — we only need it to discriminate
-- between distinct shapes within a single compile unit.
synthAnonRecordName :: Map.Map String T.FieldType -> String
synthAnonRecordName fields =
    let sorted = Map.toAscList fields
        names  = map fst sorted
        typeStr = concatMap (\(_, T.FieldType _ ty) -> show ty) sorted
        nameTag = case names of
            [] -> "Empty"
            _  -> intercalate_ "_" (map sanitiseField names)
    in "Anon_R_" ++ nameTag ++ "__" ++ shortHash (nameTag ++ typeStr)
  where
    sanitiseField = map (\c -> if c == '.' || c == '\'' || c == '"' then '_' else c)


-- | Simple polynomial hash, base-32 encoded for short readable names.
shortHash :: String -> String
shortHash s =
    let h = foldl (\acc c -> acc * 131 + fromIntegral (fromEnum c)) (17 :: Integer) s
        absH = abs h
    in take 8 (toBase32 absH)
  where
    toBase32 n
        | n <= 0    = "0"
        | otherwise = reverse (go n)
    go 0 = ""
    go n =
        let (q, r) = n `divMod` 32
            c     = "0123456789abcdefghijklmnopqrstuv" !! fromIntegral r
        in c : go q
