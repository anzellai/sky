{-# LANGUAGE TemplateHaskell #-}
{-# LANGUAGE OverloadedStrings #-}

-- | Bundle the `sky-ffi-inspect` Go helper into the sky binary so
-- releases ship a single executable rather than a pair. The source
-- tree (`tools/sky-ffi-inspect/` — main.go + go.mod + go.sum) is
-- embedded via Template Haskell at build time. On first use,
-- `ensureInspector` materialises the tree into a content-hashed
-- cache dir under `$XDG_CACHE_HOME/sky/tools/`, runs `go build`, and
-- returns the path to the compiled binary. Subsequent calls are
-- O(stat) — the hash changes only when sky is rebuilt with new
-- inspector source, so `sky upgrade` auto-invalidates without
-- manual cleanup.
--
-- Trust model: Go is already a hard requirement of `sky build`, so
-- compiling a ~500-line helper on first `sky add` is a no-new-
-- dependency cost. Builds are reproducible and local — no network
-- access (go.sum is embedded, Go's module cache handles offline
-- reuse).
module Sky.Build.EmbeddedInspector
    ( ensureInspector
    , embeddedInspectorBytes
    ) where

import Control.Monad (unless, forM_)
import qualified Crypto.Hash.SHA256 as SHA256
import Data.ByteString (ByteString)
import qualified Data.ByteString as BS
import Data.FileEmbed (embedDir)
import Data.List (sortOn)
import Numeric (showHex)
import System.Directory (createDirectoryIfMissing, doesFileExist,
                         getPermissions, setPermissions, setOwnerExecutable,
                         getXdgDirectory, XdgDirectory(..))
import System.FilePath ((</>), takeDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


-- | The `tools/sky-ffi-inspect/` source tree, keyed by relative
-- path. Re-embedded whenever any file changes (file-embed registers
-- each file via qAddDependentFile).
embeddedInspectorBytes :: [(FilePath, ByteString)]
embeddedInspectorBytes = $(embedDir "tools/sky-ffi-inspect")


-- | Content hash of the embedded tree. Entries are sorted by path
-- so the hash is independent of embed-order. First 12 hex chars
-- are plenty to disambiguate across sky versions.
inspectorHash :: String
inspectorHash =
    let sorted = sortOn fst embeddedInspectorBytes
        combined = BS.concat [BS.concat [BS.pack (map (fromIntegral . fromEnum) p), b]
                              | (p, b) <- sorted]
        digest = SHA256.hash combined
    in take 12 (concatMap (pad2 . (`showHex` "")) (BS.unpack digest))
  where
    pad2 [c] = ['0', c]
    pad2 s   = s


-- | Return the path to a ready-to-run `sky-ffi-inspect`. Builds
-- into `$XDG_CACHE_HOME/sky/tools/sky-ffi-inspect-<hash>/` on first
-- use; reuses the cached binary thereafter.
ensureInspector :: IO (Either String FilePath)
ensureInspector = do
    cache <- getXdgDirectory XdgCache "sky"
    let root = cache </> "tools" </> ("sky-ffi-inspect-" ++ inspectorHash)
        bin  = root </> "sky-ffi-inspect"
    ready <- doesFileExist bin
    if ready
        then return (Right bin)
        else buildInspector root bin


buildInspector :: FilePath -> FilePath -> IO (Either String FilePath)
buildInspector root bin = do
    createDirectoryIfMissing True root
    -- Materialise source.
    forM_ embeddedInspectorBytes $ \(rel, bytes) -> do
        let dst = root </> rel
        createDirectoryIfMissing True (takeDirectory dst)
        BS.writeFile dst bytes
    -- go build .
    let gobuild = (proc "go" ["build", "-ldflags=-s -w", "-o", bin, "."])
                    { cwd = Just root }
    (ec, _out, err) <- readCreateProcessWithExitCode gobuild ""
    case ec of
        ExitSuccess -> do
            perms <- getPermissions bin
            setPermissions bin (setOwnerExecutable True perms)
            exists <- doesFileExist bin
            unless exists $
                return ()  -- fall through to Right; Left handled below
            return (Right bin)
        _ ->
            return (Left $ "sky-ffi-inspect: go build failed:\n" ++ err)
