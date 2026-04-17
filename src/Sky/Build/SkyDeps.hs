-- | Sky-source dependency installer.
-- Handles the [dependencies] section of sky.toml by cloning each declared
-- git repository into .skydeps/<flattened-name>/, then returning the extra
-- source roots (usually <dep>/src) that the module graph should search.
module Sky.Build.SkyDeps
    ( installDeps
    , depSourceRoots
    , flattenPkg
    )
    where

import Control.Exception (SomeException, try)
import System.Directory (createDirectoryIfMissing, doesDirectoryExist)
import System.FilePath ((</>))
import System.Process (callProcess)


-- | Install every Sky-source dependency declared in sky.toml and return
-- the source roots to prepend to the module search path.
-- Idempotent: if a dep is already cloned, skip clone but still return its root.
installDeps :: [(String, String)] -> IO [FilePath]
installDeps [] = return []
installDeps deps = do
    putStrLn $ "-- Installing " ++ show (length deps) ++ " Sky dependency(ies)"
    createDirectoryIfMissing True ".skydeps"
    mapM ensureDep deps


-- | Ensure one dependency is checked out. Returns the dep's source root
-- (<.skydeps>/<flat>/src if that directory exists, otherwise <.skydeps>/<flat>).
ensureDep :: (String, String) -> IO FilePath
ensureDep (pkg, version) = do
    let dest = ".skydeps" </> flattenPkg pkg
    already <- doesDirectoryExist dest
    if already
        then putStrLn $ "   " ++ pkg ++ " (cached)"
        else do
            putStrLn $ "   " ++ pkg ++ " @ " ++ version
            let url = "https://" ++ pkg ++ ".git"
            -- Shallow clone; if a non-"latest" version is pinned, try checkout after.
            cloneRes <- try (callProcess "git"
                ["clone", "--quiet", "--depth", "1", url, dest]) :: IO (Either SomeException ())
            case cloneRes of
                Left e -> putStrLn $ "   WARN: clone failed for " ++ pkg ++ ": " ++ show e
                Right () -> return ()
            case version of
                "latest" -> return ()
                "" -> return ()
                ver -> do
                    _ <- try (callProcess "sh"
                        ["-c", "cd " ++ dest ++ " && git fetch --quiet --depth 1 origin "
                               ++ ver ++ " && git checkout --quiet FETCH_HEAD"])
                        :: IO (Either SomeException ())
                    return ()
    depSourceRoot dest


-- | Resolve a dep's source root: prefer <dest>/src when present.
depSourceRoot :: FilePath -> IO FilePath
depSourceRoot dest = do
    hasSrc <- doesDirectoryExist (dest </> "src")
    return (if hasSrc then dest </> "src" else dest)


-- | Return source roots for already-installed deps without cloning.
-- Used by discovery paths that don't want to trigger network I/O.
depSourceRoots :: [(String, String)] -> IO [FilePath]
depSourceRoots deps = mapM (depSourceRoot . (".skydeps" </>) . flattenPkg . fst) deps


-- | github.com/anzellai/sky-tailwind → github.com_anzellai_sky-tailwind
flattenPkg :: String -> String
flattenPkg = map (\c -> if c == '/' then '_' else c)
