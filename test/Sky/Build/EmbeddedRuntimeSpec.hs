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
import System.Environment (getEnvironment)
import System.FilePath ((</>), takeFileName)
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (sort)


-- | Strip SKY_RUNTIME_DIR from a process env so the test's `sky build`
-- doesn't get hijacked by a parent-repo runtime-go pinned by a nix
-- shellHook (`export SKY_RUNTIME_DIR="$PWD/runtime-go"` from the
-- parent repo's .envrc). v0.15.57 #409 verification surfaced this:
-- when an agent runs in a worktree, the shellHook still points
-- SKY_RUNTIME_DIR at the parent repo's runtime-go, so `sky build`
-- inside the spec's tempdir copies the WRONG runtime-go from the
-- parent — making the disk-tree comparison spuriously fail.
scrubRuntimeEnv :: IO [(String, String)]
scrubRuntimeEnv = do
    env <- getEnvironment
    return [ (k, v) | (k, v) <- env, k /= "SKY_RUNTIME_DIR" ]


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
            -- Use TOP-LEVEL files only (no subdirectory descent).
            -- The embedded runtime tree is flat by design; the
            -- `walkFiles` recursion previously assumed a flat
            -- structure too, but `runtime-go/rt/telemetry/` was
            -- added later, breaking the basename-keyed comparison
            -- because `disk[telemetry/atomic_float.go]` would be
            -- looked up by bare name in the materialised flat dir.
            -- Restrict comparison to top-level .go files which is
            -- what the embedded runtime tracks.
            diskEntries <- listDirectory diskRtDir
            -- v0.16.0 PR 2d added embedDirRecursive filtering so
            -- _test.go files don't ship in user binaries (saves
            -- ~1 MB per binary). The disk-vs-materialised compare
            -- must mirror that filter or we get a phantom diff.
            -- Also skip directories (subpackages like console_app/,
            -- telemetry/) since the embed comparison is flat-top-level.
            let diskFiles =
                    [ diskRtDir </> e
                    | e <- diskEntries
                    , ".go" `suffixOf` e
                    , not ("_test.go" `suffixOf` e)
                    ]
            withSystemTempDirectory "sky-p3-3" $ \dir -> do
                createDirectoryIfMissing True (dir </> "src")
                writeFile (dir </> "sky.toml")
                    "name = \"p3-3\"\nentry = \"src/Main.sky\"\n"
                writeFile (dir </> "src" </> "Main.sky") $ unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main = println \"hi\""
                    ]
                env <- scrubRuntimeEnv
                (ec, _out, _err) <- readCreateProcessWithExitCode
                    (proc sky ["build", "src/Main.sky"])
                        { cwd = Just dir, env = Just env } ""
                ec `shouldBe` ExitSuccess
                let matRtDir = dir </> "sky-out" </> "rt"
                matEntries <- listDirectory matRtDir
                let matFiles =
                        [ matRtDir </> e
                        | e <- matEntries, ".go" `suffixOf` e ]
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

        it "rt/jobs/ + rt/telemetry/ subpackages land in sky-out (issue #58)" $ do
            -- Regression for issue #58: `cabal install`'s sdist only
            -- bundles files declared in `extra-source-files`. If a
            -- runtime subdirectory is added (e.g. `rt/jobs/`,
            -- `rt/telemetry/`) but NOT declared, the binary's
            -- TH-embedded `embeddedRuntime` silently drops those
            -- files. User `go build` then fails with
            -- `package sky-app/rt/jobs is not in std`.
            --
            -- This test materialises a trivial app and asserts both
            -- `sky-out/rt/jobs/jobs.go` and
            -- `sky-out/rt/telemetry/atomic_float.go` exist post-build.
            -- Failure means either (a) the cabal file's
            -- `extra-source-files` is missing the subpackage glob,
            -- or (b) the embedded-write path lost the subdir
            -- copy logic.
            sky <- findSky
            withSystemTempDirectory "sky-issue-58" $ \dir -> do
                createDirectoryIfMissing True (dir </> "src")
                writeFile (dir </> "sky.toml")
                    "name = \"issue-58\"\nentry = \"src/Main.sky\"\n"
                writeFile (dir </> "src" </> "Main.sky") $ unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main = println \"hi\""
                    ]
                env <- scrubRuntimeEnv
                (ec, _out, _err) <- readCreateProcessWithExitCode
                    (proc sky ["build", "src/Main.sky"])
                        { cwd = Just dir, env = Just env } ""
                ec `shouldBe` ExitSuccess
                let jobsFile  = dir </> "sky-out" </> "rt" </> "jobs" </> "jobs.go"
                    telemFile = dir </> "sky-out" </> "rt" </> "telemetry"
                                    </> "atomic_float.go"
                jobsExists  <- doesFileExist jobsFile
                telemExists <- doesFileExist telemFile
                jobsExists  `shouldBe` True
                telemExists `shouldBe` True
  where
    suffixOf suf s = drop (length s - length suf) s == suf
