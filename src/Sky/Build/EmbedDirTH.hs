{-# LANGUAGE TemplateHaskell #-}

-- | Recursive directory-walk TH helper for `Sky.Build.EmbeddedRuntime`.
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
    ) where

import qualified Data.ByteString as BS
import Language.Haskell.TH
import Language.Haskell.TH.Syntax (qAddDependentFile, runIO)
import qualified Data.FileEmbed as FE
import qualified System.Directory as Dir
import System.FilePath ((</>))
import Control.Monad (forM, forM_)


-- | Recursive directory walk done at TH compile time. Returns
-- every file under `root` (relative to the project root, which
-- is where cabal runs the splice) as `[(relativePath, bytes)]`.
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
                then walk base rel
                else return [(rel, abs_)]
