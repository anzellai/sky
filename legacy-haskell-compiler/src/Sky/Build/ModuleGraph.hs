-- | Module graph discovery and topological sorting.
-- Discovers all .sky files from the source root, parses their imports,
-- builds a dependency graph, and returns compilation order.
module Sky.Build.ModuleGraph
    ( ModuleInfo(..)
    , discoverModules
    , discoverModulesMulti
    , discoverModulesFromSeeds
    , discoverModulesFromSeedsTolerant
    , listSkyFiles
    , compilationOrder
    )
    where

import Control.Exception (try, SomeException, ErrorCall, displayException, evaluate)
import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Data.Text as T
import qualified Data.Text.IO as TIO
import System.Directory (doesFileExist, doesDirectoryExist, listDirectory)
import System.Exit (exitWith, ExitCode(..))
import System.FilePath ((</>), takeDirectory, dropExtension, makeRelative, takeExtension)

import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Reporting.Render as Render
import qualified Sky.Parse.Module as Parse


-- | Information about a discovered module
data ModuleInfo = ModuleInfo
    { _mi_name     :: !String       -- module name (e.g., "Lib.Utils")
    , _mi_path     :: !FilePath     -- file path (e.g., "src/Lib/Utils.sky")
    , _mi_imports  :: [String]      -- imported module names
    , _mi_isLocal  :: !Bool         -- True if local (not stdlib)
    }
    deriving (Show)


-- | Safety wrapper around `Parse.parseModule` that catches any
-- Haskell-level exception escaping the parser (typically an
-- `ErrorCall` from a `error "Expected …"` site in
-- src/Sky/Parse/{Expression,Pattern,Type}.hs) and converts it to a
-- Sky-shaped `ModuleError` referencing the source file.
--
-- This is a SAFETY NET — the parser SHOULD never throw an
-- ErrorCall; every parse-failure path should return a `Left
-- ModuleError` with row+col. The pre-2026-05-18 parser had ~6
-- `error "…"` sites that bypassed the Either path and surfaced
-- as Haskell stack traces with GHC CallStack noise — terrible
-- DX. This wrapper guarantees the user sees an Elm-style Sky
-- error block regardless.
--
-- Layer 2 (eventually): migrate every `error` in the parser to
-- a proper position-tagged ModuleError variant so the catch
-- here becomes a true safety net (the catch-all message is
-- generic — a proper Layer 2 message would carry the actual
-- "expected , or ) here" context).
safeParseModule
    :: FilePath
    -> T.Text
    -> IO (Either Parse.ModuleError Src.Module)
safeParseModule path src = do
    result <- try (evaluate (Parse.parseModule src))
        :: IO (Either SomeException (Either Parse.ModuleError Src.Module))
    case result of
        Right ok -> return ok
        Left exc -> do
            -- Best-effort: anchor the error at line 1 col 1 since
            -- the escaping exception doesn't carry position. The
            -- diagnostic body includes the underlying message so a
            -- reader can grep the source.
            putStrLn ""
            putStrLn ("-- PARSE ERROR (uncaught) ── " ++ path)
            putStrLn ""
            putStrLn "The parser crashed while processing this file."
            putStrLn ("Underlying error: " ++ stripCallStack (displayException exc))
            putStrLn ""
            putStrLn "This is a Sky compiler bug — the parser should produce"
            putStrLn "a clean Sky error block instead of crashing. Please"
            putStrLn "report this case so we can improve the diagnostic. As"
            putStrLn "a workaround: look for unmatched brackets ( [ { in the"
            putStrLn "source above; that's the most common trigger."
            -- Fall through with a generic ModuleError so the rest
            -- of the build path exits cleanly.
            return (Left (Parse.ModuleExpected 1 1))
  where
    -- GHC's displayException for ErrorCall ends with a HasCallStack
    -- block — strip it for user readability.
    stripCallStack s =
        let lns = lines s
        in case break (\l -> l == "CallStack (from HasCallStack):") lns of
            (head_, _:_) -> unlines head_
            _            -> s


-- | Discover all modules starting from the entry file.
-- Recursively follows local imports to build the full module graph.
discoverModules :: String -> FilePath -> IO (Map.Map String ModuleInfo)
discoverModules sourceRoot = discoverModulesMulti [sourceRoot]


-- | Discover modules given multiple candidate source roots. The entry file
-- determines the primary module name via the first root it is relative to;
-- imports are resolved by probing each root in order (first match wins).
discoverModulesMulti :: [String] -> FilePath -> IO (Map.Map String ModuleInfo)
discoverModulesMulti roots entryPath = discoverModulesFromSeeds roots [entryPath]


-- | Like discoverModulesMulti but takes MULTIPLE entry seeds. Used by
-- the LSP's workspace typecheck so every .sky file in src/ + tests/
-- ends up in the index — not just modules transitively reachable from
-- the project's main entry.
--
-- Parse errors in seeds are FATAL (matches the build path). The LSP
-- should use `discoverModulesFromSeedsTolerant` instead, which skips
-- unparseable files and continues — the user is constantly editing
-- mid-parse code and an unrelated file's broken state shouldn't kill
-- the whole workspace index.
discoverModulesFromSeeds :: [String] -> [FilePath] -> IO (Map.Map String ModuleInfo)
discoverModulesFromSeeds roots seeds = do
    go Map.empty seeds
  where
    primaryRoot = case roots of
        (r:_) -> r
        []    -> "."

    go visited [] = return visited
    go visited (path:rest) = do
        exists <- doesFileExist path
        let alreadyByPath = any (\v -> _mi_path v == path) (Map.elems visited)
            modNameGuess = pathToModuleName (rootFor path) path
            alreadyByName = Map.member modNameGuess visited
        if not exists || alreadyByPath || alreadyByName
            then go visited rest
            else do
                source <- TIO.readFile path
                parsedR <- safeParseModule path source
                case parsedR of
                    Left err -> do
                        -- v0.13 Layer 1: render the Diagnostic and
                        -- exit cleanly.  The previous `error`-based
                        -- halt printed a Haskell CallStack to stderr
                        -- which leaked GHC internals to end users —
                        -- the Elm-style block above already tells
                        -- them everything they need (file, line:col,
                        -- source snippet, fix guidance), so we exit
                        -- with code 1 and let the shell see the
                        -- failure naturally.
                        let diag = Parse.moduleErrorToDiagnostic path err
                        rendered <- Render.renderCli diag
                        putStrLn rendered
                        exitWith (ExitFailure 1)
                    Right srcMod -> do
                        let declaredName = case Src._name srcMod of
                                Just (A.At _ segs) -> joinDots segs
                                Nothing -> modNameGuess
                            importNames = map getImportName (Src._imports srcMod)
                            localImports = filter isLocalImport importNames
                        localPaths <- mapM resolveImport localImports
                        let info = ModuleInfo
                                { _mi_name = declaredName
                                , _mi_path = path
                                , _mi_imports = importNames
                                , _mi_isLocal = True
                                }
                        go (Map.insert declaredName info visited)
                           (catMaybe localPaths ++ rest)

    rootFor path =
        case filter (\r -> take (length r) path == r) roots of
            (r:_) -> r
            []    -> primaryRoot

    resolveImport :: String -> IO (Maybe FilePath)
    resolveImport modName = do
        let candidates = map (\r -> moduleNameToPath r modName) roots
        firstExisting candidates

    firstExisting [] = return Nothing
    firstExisting (p:ps) = do
        ok <- doesFileExist p
        if ok then return (Just p) else firstExisting ps

    catMaybe = foldr (\m acc -> case m of Just x -> x:acc; Nothing -> acc) []


-- | Tolerant variant: parse errors are SKIPPED, not fatal. Returns
-- only the modules that successfully parsed. Use this for the LSP
-- workspace pass so an unrelated file's broken state doesn't kill
-- diagnostics, hover, completion etc. across the entire project.
discoverModulesFromSeedsTolerant :: [String] -> [FilePath] -> IO (Map.Map String ModuleInfo)
discoverModulesFromSeedsTolerant roots seeds = do
    goTolerant Map.empty seeds
  where
    primaryRoot = case roots of
        (r:_) -> r
        []    -> "."

    goTolerant visited [] = return visited
    goTolerant visited (path:rest) = do
        exists <- doesFileExist path
        let alreadyByPath = any (\v -> _mi_path v == path) (Map.elems visited)
            modNameGuess = pathToModuleName (rootFor path) path
            alreadyByName = Map.member modNameGuess visited
        if not exists || alreadyByPath || alreadyByName
            then goTolerant visited rest
            else do
                source <- TIO.readFile path
                case Parse.parseModule source of
                    Left _ ->
                        -- Skip parse-failed module and continue.
                        goTolerant visited rest
                    Right srcMod -> do
                        let declaredName = case Src._name srcMod of
                                Just (A.At _ segs) -> joinDots segs
                                Nothing -> modNameGuess
                            importNames = map getImportName (Src._imports srcMod)
                            localImports = filter isLocalImport importNames
                        localPaths <- mapM resolveImport localImports
                        let info = ModuleInfo
                                { _mi_name = declaredName
                                , _mi_path = path
                                , _mi_imports = importNames
                                , _mi_isLocal = True
                                }
                        goTolerant (Map.insert declaredName info visited)
                                   (catMaybe localPaths ++ rest)

    rootFor path =
        case filter (\r -> take (length r) path == r) roots of
            (r:_) -> r
            []    -> primaryRoot

    resolveImport :: String -> IO (Maybe FilePath)
    resolveImport modName = do
        let candidates = map (\r -> moduleNameToPath r modName) roots
        firstExisting candidates

    firstExisting [] = return Nothing
    firstExisting (p:ps) = do
        ok <- doesFileExist p
        if ok then return (Just p) else firstExisting ps

    catMaybe = foldr (\m acc -> case m of Just x -> x:acc; Nothing -> acc) []


-- | Return modules in compilation order (dependencies first).
compilationOrder :: Map.Map String ModuleInfo -> [ModuleInfo]
compilationOrder modules =
    let sorted = topoSort modules
    in map (\name -> modules Map.! name) sorted


-- | Topological sort of module names
topoSort :: Map.Map String ModuleInfo -> [String]
topoSort modules =
    let (_, result) = foldl (\(vis, acc) name -> visit vis name acc) (Set.empty, []) (Map.keys modules)
    in reverse result
  where
    visit visited name acc
        | Set.member name visited = (visited, acc)
        | otherwise =
            case Map.lookup name modules of
                Nothing -> (Set.insert name visited, name : acc)
                Just info ->
                    let localDeps = filter (\imp -> Map.member imp modules) (_mi_imports info)
                        (visited', acc') = foldl (\(v, a) dep -> visit v dep a)
                            (Set.insert name visited, acc) localDeps
                    in (visited', name : acc')


-- ═══════════════════════════════════════════════════════════
-- HELPERS
-- ═══════════════════════════════════════════════════════════

pathToModuleName :: String -> FilePath -> String
pathToModuleName sourceRoot path =
    let relative = makeRelative sourceRoot path
        withoutExt = dropExtension relative
    in map (\c -> if c == '/' then '.' else c) withoutExt


moduleNameToPath :: String -> String -> FilePath
moduleNameToPath sourceRoot modName =
    sourceRoot </> map (\c -> if c == '.' then '/' else c) modName ++ ".sky"


getImportName :: Src.Import -> String
getImportName imp =
    case Src._importName imp of
        A.At _ segs -> joinDots segs


-- | We attempt to resolve EVERY import against the configured roots
-- (project src + Sky-source deps + embedded Sky stdlib). `resolveImport`
-- returns Nothing for imports with no on-disk source — those are
-- assumed to be kernel modules (Sky.Core.*, Std.Db, etc. — implemented
-- in Go in runtime-go/rt/) and get resolved later by the canonicaliser
-- via the kernel registry. This lets new on-disk stdlib modules
-- (Sky.Core.Error, ...) participate in the module graph
-- without a per-module allowlist.
isLocalImport :: String -> Bool
isLocalImport _ = True


joinDots :: [String] -> String
joinDots [] = ""
joinDots [x] = x
joinDots (x:xs) = x ++ "." ++ joinDots xs


-- | Recursively list every .sky file under a directory. Used by the
-- LSP workspace pass to discover ALL source files, not just those
-- transitively imported from the project's main entry. Returns paths
-- relative to the cwd (or absolute, depending on input). Skips
-- common cache + build dirs.
listSkyFiles :: FilePath -> IO [FilePath]
listSkyFiles root = do
    isDir <- doesDirectoryExist root
    if not isDir
        then return []
        else do
            r <- try (listDirectory root) :: IO (Either SomeException [FilePath])
            case r of
                Left _ -> return []
                Right entries -> concat <$> mapM each entries
  where
    each name
        | name == ".skycache" = return []
        | name == "sky-out" = return []
        | name == "dist-newstyle" = return []
        | name == ".git" = return []
        | name == "node_modules" = return []
        | name == ".sky-stdlib" = return []
        | otherwise = do
            let path = root </> name
            isD <- doesDirectoryExist path
            if isD
                then listSkyFiles path
                else if takeExtension path == ".sky"
                    then return [path]
                    else return []
