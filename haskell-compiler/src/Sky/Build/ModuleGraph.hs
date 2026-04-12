-- | Module graph discovery and topological sorting.
-- Discovers all .sky files from the source root, parses their imports,
-- builds a dependency graph, and returns compilation order.
module Sky.Build.ModuleGraph
    ( ModuleInfo(..)
    , discoverModules
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
discoverModules sourceRoot entryPath = do
    go Map.empty [entryPath]
  where
    go visited [] = return visited
    go visited (path:rest) = do
        exists <- doesFileExist path
        let modName = pathToModuleName sourceRoot path
        if not exists || Map.member modName visited
            then go visited rest
            else do
                source <- TIO.readFile path
                case Parse.parseModule source of
                    Left _ -> do
                        putStrLn $ "   Warning: could not parse " ++ path
                        go visited rest
                    Right srcMod -> do
                        let declaredName = case Src._name srcMod of
                                Just (A.At _ segs) -> joinDots segs
                                Nothing -> modName
                            importNames = map getImportName (Src._imports srcMod)
                            localImports = filter isLocalImport importNames
                            localPaths = map (moduleNameToPath sourceRoot) localImports
                            info = ModuleInfo
                                { _mi_name = declaredName
                                , _mi_path = path
                                , _mi_imports = importNames
                                , _mi_isLocal = True
                                }
                        go (Map.insert declaredName info visited) (localPaths ++ rest)


-- | Return modules in compilation order (dependencies first).
compilationOrder :: Map.Map String ModuleInfo -> [ModuleInfo]
compilationOrder modules =
    let sorted = topoSort modules
    in map (\name -> modules Map.! name) sorted


-- | Topological sort of module names
topoSort :: Map.Map String ModuleInfo -> [String]
topoSort modules = reverse $ foldl (\acc name -> snd (visit Set.empty name acc)) [] (Map.keys modules)
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
