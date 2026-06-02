{-# LANGUAGE TemplateHaskell #-}

-- | Recursive directory-walk TH helper for `Sky.Build.EmbeddedRuntime`.
-- v0.16.0 PR 2 fix-up: added isEmbeddableRuntimeFile/Dir filter so
-- *_test.go, testdata/, .skycache/, editor backups, .DS_Store are
-- never embedded. Filter is reused by `copyRuntimeRecursive` in
-- `Sky.Build.Compile` so the on-disk + TH paths agree.
--
-- Workaround for issue #58: `Data.FileEmbed.embedDir` in
-- `file-embed-0.0.16.0` does NOT recurse into subdirectories
-- when TH-spliced into a cabal-installed binary (works correctly
-- in `runghc`). This module's `embedDirRecursive` does the
-- recursion ourselves at TH compile time and registers each file
-- via `qAddDependentFile` so cabal invalidates the splice on
-- modification.
--
-- Lives in a separate module because GHC's TH stage restriction
-- forbids using a locally-defined function in a top-level splice.
module Sky.Build.EmbedDirTH
    ( embedDirRecursive
    , isEmbeddableRuntimeFile
    , isEmbeddableRuntimeDir
    ) where

import qualified Data.ByteString as BS
import Language.Haskell.TH
import Language.Haskell.TH.Syntax (qAddDependentFile, runIO)
import qualified Data.FileEmbed as FE
import qualified System.Directory as Dir
import System.FilePath ((</>), takeFileName, splitDirectories)
import Control.Monad (forM, forM_)
import Data.List (isSuffixOf)


-- | Recursive directory walk done at TH compile time. Returns
-- every file under `root` (relative to the project root, which
-- is where cabal runs the splice) as `[(relativePath, bytes)]`.
--
-- Files / directories that should never ship in the compiled sky
-- binary or be materialised into a user's `sky-out/` (Go test
-- files, testdata fixtures, editor backups, OS junk) are
-- filtered out at the walk step.  Filtering here — rather than
-- at every consumer site — keeps the `embeddedRuntime` /
-- `embeddedSkyStdlib` lists lean (smaller `sky` binary) AND
-- prevents `writeEmbeddedRuntime` / `writeEmbeddedSkyStdlib` /
-- `copyRuntimeRecursive` from leaking those files into the
-- user-facing build tree.
--
-- Predicates `isEmbeddableRuntimeFile` + `isEmbeddableRuntimeDir`
-- are exported so the on-disk dev path (`copyRuntimeRecursive`
-- in `Sky.Build.Compile`) applies the same filter — single
-- source of truth.
embedDirRecursive :: FilePath -> Q Exp
embedDirRecursive root = do
    entries <- runIO (walk root "")
    forM_ entries $ \(_, p) -> qAddDependentFile p
    let mkPair (rel, abs_) = do
            -- Reuse file-embed's `bsToExp` to turn ByteString
            -- bytes into a TH expression. This is the same path
            -- file-embed's own `embedFile` uses internally.
            bs <- runIO (BS.readFile abs_)
            bsExp <- FE.bsToExp bs
            [| (rel, $(return bsExp)) |]
    let pairs = map mkPair entries
    listE pairs
  where
    walk :: FilePath -> FilePath -> IO [(FilePath, FilePath)]
    walk base sub = do
        let dirPath = base </> sub
        items <- Dir.listDirectory dirPath
        fmap concat $ forM items $ \name -> do
            let rel = if null sub then name else sub </> name
                abs_ = base </> rel
            isDir <- Dir.doesDirectoryExist abs_
            if isDir
                then if isEmbeddableRuntimeDir name
                        then walk base rel
                        else return []
                else if isEmbeddableRuntimeFile rel
                        then return [(rel, abs_)]
                        else return []


-- | True for files that belong in the embedded runtime / stdlib
-- payload.  Excludes:
--
--   * Go test files (`*_test.go`) — useless to user binaries
--     (Go's `go build` already filters them, but they bloat the
--     `sky` binary AND the materialised on-disk `rt/` tree).
--   * Editor / VCS junk (`.bak` / `.swp` / `.DS_Store` / `~`).
--   * The runtime-go module's own scratch dirs (`.skycache` —
--     re-created by every user build, never embed source's copy).
--
-- Kept: `*.go` non-test, `go.mod`, `go.sum`, embedded SQL/HTML/
-- SVG/JSON data files the runtime imports via `//go:embed`.
--
-- The `rel` path is relative to the embed root (`runtime-go/` or
-- `sky-stdlib/`), so we can match against full path components
-- like `.skycache/anything`.
isEmbeddableRuntimeFile :: FilePath -> Bool
isEmbeddableRuntimeFile rel =
    let name = takeFileName rel
        parts = splitDirectories rel
    in not (any (`isSuffixOf` name) excludedSuffixes)
       && not (any (`elem` excludedDirComponents) parts)
       && not (name `elem` excludedExactNames)
  where
    excludedSuffixes =
        [ "_test.go"   -- Go's standard test-file convention
        , ".bak"       -- editor backups
        , ".swp"       -- vim swap
        , ".swo"       -- vim swap
        , "~"          -- emacs/general backup
        , ".orig"      -- patch / merge leftover
        , ".rej"       -- patch reject leftover
        ]
    excludedDirComponents =
        [ ".skycache"  -- per-project build cache, never embed source's
        , "testdata"   -- Go convention for test fixtures
        , ".git"       -- defence in depth (shouldn't be there anyway)
        ]
    excludedExactNames =
        [ ".DS_Store"  -- macOS Finder junk
        , "Thumbs.db"  -- Windows shell junk
        ]


-- | Directory-level filter used by the recursive walk.  Skipping
-- whole directories is faster than walking them and rejecting
-- every contained file; also prevents `qAddDependentFile` calls
-- for files we'd discard anyway.
isEmbeddableRuntimeDir :: FilePath -> Bool
isEmbeddableRuntimeDir name =
    name `notElem`
        [ ".skycache"
        , "testdata"
        , ".git"
        , "node_modules"
        ]
