-- | Module graph discovery and topological sorting.
-- Discovers all .sky files from the source root, parses their imports,
-- builds a dependency graph, and returns compilation order.
module Sky.Build.ModuleGraph
    ( ModuleInfo(..)
    , discoverModules
    , discoverModulesMulti
    , compilationOrder
    )
    where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Data.Text as T
import qualified Data.Text.IO as TIO
import System.Directory (doesFileExist)
import System.FilePath ((</>), takeDirectory, dropExtension, makeRelative)

import qualified Sky.AST.Source as Src
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Parse.Module as Parse


-- | Information about a discovered module
data ModuleInfo = ModuleInfo
    { _mi_name     :: !String       -- module name (e.g., "Lib.Utils")
    , _mi_path     :: !FilePath     -- file path (e.g., "src/Lib/Utils.sky")
    , _mi_imports  :: [String]      -- imported module names
    , _mi_isLocal  :: !Bool         -- True if local (not stdlib)
    }
    deriving (Show)


-- | Discover all modules starting from the entry file.
-- Recursively follows local imports to build the full module graph.
discoverModules :: String -> FilePath -> IO (Map.Map String ModuleInfo)
discoverModules sourceRoot = discoverModulesMulti [sourceRoot]


-- | Discover modules given multiple candidate source roots. The entry file
-- determines the primary module name via the first root it is relative to;
-- imports are resolved by probing each root in order (first match wins).
discoverModulesMulti :: [String] -> FilePath -> IO (Map.Map String ModuleInfo)
discoverModulesMulti roots entryPath = do
    go Map.empty [entryPath]
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
                case Parse.parseModule source of
                    Left err -> do
                        putStrLn $ "   Warning: could not parse " ++ path ++ ": " ++ show err
                        go visited rest
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

    -- Choose the best root for naming a given file path.
    rootFor path =
        case filter (\r -> take (length r) path == r) roots of
            (r:_) -> r
            []    -> primaryRoot

    -- Probe each root in order; return the first existing candidate path,
    -- falling back to the primary root (so a missing-module error still
    -- reports a sensible path).
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


isLocalImport :: String -> Bool
isLocalImport name
    | take 9 name == "Sky.Core." = False
    | take 9 name == "Sky.Http." = False
    | take 4 name == "Std."      = False
    | otherwise = True


joinDots :: [String] -> String
joinDots [] = ""
joinDots [x] = x
joinDots (x:xs) = x ++ "." ++ joinDots xs
