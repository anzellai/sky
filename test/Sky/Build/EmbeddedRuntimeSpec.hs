{-# LANGUAGE OverloadedStrings #-}
module Sky.Build.EmbeddedRuntimeSpec (spec) where

-- Audit P3-3: the embedded runtime-go tree (baked into the sky
-- binary via Template Haskell) must match the on-disk tree after
-- a plain `cabal build`. Pre-fix, `scripts/build.sh` touched the
-- embedder source to force a TH rebuild when runtime files were
-- added or removed. That dance has been removed — `embedDir`
-- registers every file it walks via `qAddDependentFile`, so cabal
-- re-embeds whenever a tracked file changes. This test locks the
-- invariant by running `sky build` on a trivial project and
-- diffing the materialised `sky-out/rt/` tree against disk.

import Test.Hspec
import qualified Data.ByteString as BS
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing,
                         listDirectory, doesDirectoryExist)
import System.FilePath ((</>), takeFileName)
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (sort)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


walkFiles :: FilePath -> IO [FilePath]
walkFiles root = do
    isDir <- doesDirectoryExist root
    if not isDir
        then return [root]
        else do
            entries <- listDirectory root
            concat <$> mapM (\e -> walkFiles (root </> e)) entries


spec :: Spec
spec = do
    describe "embedded runtime tracks disk tree (audit P3-3)" $ do

        it "sky build materialises rt/*.go whose bytes match runtime-go/rt/" $ do
            sky <- findSky
            cwd <- getCurrentDirectory
            let diskRtDir = cwd </> "runtime-go" </> "rt"
            diskFiles <- filter (\p -> ".go" `suffixOf` p) <$> walkFiles diskRtDir
            -- The binary ships with the embedded tree; materialise
            -- it by building a throwaway project.
            withSystemTempDirectory "sky-p3-3" $ \dir -> do
                createDirectoryIfMissing True (dir </> "src")
                writeFile (dir </> "sky.toml")
                    "name = \"p3-3\"\nentry = \"src/Main.sky\"\n"
                writeFile (dir </> "src" </> "Main.sky") $ unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main = println \"hi\""
                    ]
                (ec, _out, _err) <- readCreateProcessWithExitCode
                    (proc sky ["build", "src/Main.sky"])
                        { cwd = Just dir } ""
                ec `shouldBe` ExitSuccess
                let matRtDir = dir </> "sky-out" </> "rt"
                matFiles <- filter (\p -> ".go" `suffixOf` p) <$> walkFiles matRtDir
                -- File set by basename must match exactly.
                let diskNames = sort (map takeFileName diskFiles)
                    matNames  = sort (map takeFileName matFiles)
                matNames `shouldBe` diskNames
                -- Content must match byte-for-byte for every file.
                mapM_ (\name -> do
                        disk <- BS.readFile (diskRtDir </> name)
                        mat  <- BS.readFile (matRtDir </> name)
                        mat `shouldBe` disk)
                    diskNames
  where
    suffixOf suf s = drop (length s - length suf) s == suf
