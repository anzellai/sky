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

import Control.Exception (SomeException, try, catch)
import Control.Monad (when)
import System.Directory (createDirectoryIfMissing, doesDirectoryExist, listDirectory)
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
--
-- v0.15.57 #415: validate the cache before short-circuiting. The old
-- "directory exists → assume cached" check let a corrupt or partial
-- checkout (empty `.git/`, no `src/*.sky` files) pass for valid,
-- which surfaced as `Undefined name: tw` at canonicalise time because
-- the dep import resolved to a phantom module. We now require at
-- least one `.sky` file under `<dest>/src/` (or `<dest>/`) before
-- accepting the cache; an empty / corrupt dir gets wiped + re-cloned.
ensureDep :: (String, String) -> IO FilePath
ensureDep (pkg, version) = do
    let dest = ".skydeps" </> flattenPkg pkg
    already <- doesDirectoryExist dest
    cacheValid <- if already then hasSkyFile dest else return False
    if cacheValid
        then putStrLn $ "   " ++ pkg ++ " (cached)"
        else do
            -- Corrupt cache: wipe before re-clone (git clone fails when
            -- the dest already exists; rm -rf is the simplest scrub).
            when already $ do
                putStrLn $ "   " ++ pkg ++ " (cache invalid — re-cloning)"
                _ <- try (callProcess "rm" ["-rf", dest]) :: IO (Either SomeException ())
                return ()
            when (not already) $
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


-- | True when <dest> (or <dest>/src/) contains at least one `.sky`
-- file. Walks the immediate top of <src>/<module>/* — sky-tailwind
-- nests under src/Tailwind/, so a depth-2 walk catches the common
-- "src + nested modules" layout while staying cheap.
hasSkyFile :: FilePath -> IO Bool
hasSkyFile dest = do
    let probe = dest </> "src"
    hasSrc <- doesDirectoryExist probe
    if hasSrc
        then anySkyUnder probe
        else anySkyUnder dest
  where
    anySkyUnder :: FilePath -> IO Bool
    anySkyUnder root = do
        ok <- doesDirectoryExist root
        if not ok
            then return False
            else do
                entries <- safeListDir root
                anyMatch root entries

    anyMatch _ [] = return False
    anyMatch root (e : rest) = do
        let path = root </> e
        if ".sky" `suffixOf` e
            then return True
            else do
                isDir <- doesDirectoryExist path
                if isDir
                    then do
                        nested <- safeListDir path
                        deeper <- anyMatch path nested
                        if deeper then return True else anyMatch root rest
                    else anyMatch root rest

    safeListDir :: FilePath -> IO [FilePath]
    safeListDir root =
        listDirectory root `catch` (\e -> let _ = (e :: SomeException) in return [])

    suffixOf :: String -> String -> Bool
    suffixOf suf s = drop (length s - length suf) s == suf


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
