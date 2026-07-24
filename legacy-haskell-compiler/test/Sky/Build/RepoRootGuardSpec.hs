module Sky.Build.RepoRootGuardSpec (spec) where

-- v0.17 task #662 — `sky build` repo-root guard regression fence.
--
-- CLAUDE.md states as a hard contract: "Never run `sky build`
-- from the repo root — overwrites the compiler binary in
-- sky-out/".  app/Main.hs:1271-1288 enforces this by refusing
-- when the cwd contains a `sky-compiler.cabal` file (a unique
-- marker of the compiler-source repo root; user projects never
-- have one).
--
-- This spec pins two failure modes:
--
--   1. From the compiler repo root: `sky build src/Main.sky`
--      MUST refuse with a clear diagnostic mentioning
--      "compiler repo root" and "sky-compiler.cabal".  Exit
--      code MUST be non-zero so CI scripts catch the misuse.
--
--   2. From a directory WITHOUT `sky-compiler.cabal`
--      (simulating a user project): the guard MUST NOT fire.
--      We don't actually build (slow) — we just confirm the
--      guard's specific diagnostic doesn't appear in stderr
--      when run from a temp dir.  Build success/failure is
--      out of scope for THIS spec; it's covered by the
--      example sweep.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
    createDirectoryIfMissing, removeDirectoryRecursive,
    getTemporaryDirectory)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


spec :: Spec
spec = do
    describe "task #662 — sky build repo-root guard" $ do
        it "refuses `sky build` from the Sky compiler repo root" $ do
            -- The test process's cwd IS the compiler repo root
            -- (sky-tests runs from there).  So invoking `sky
            -- build` with cwd unset hits the guard naturally.
            sky <- findSky
            cwd <- getCurrentDirectory
            -- Confirm we're in the right place — guard's
            -- precondition is sky-compiler.cabal at cwd.
            cabalExists <- doesFileExist (cwd </> "sky-compiler.cabal")
            cabalExists `shouldBe` True
            let cp = (proc sky ["build", "src/Main.sky"])
                        { cwd = Just cwd }
            (ec, out, err) <- readCreateProcessWithExitCode cp ""
            let combined = out ++ err
            ec `shouldNotBe` ExitSuccess
            ("refusing to run from the Sky compiler" `isInfixOf` combined)
                `shouldBe` True
            ("sky-compiler.cabal" `isInfixOf` combined) `shouldBe` True

        it "guard does NOT fire from a directory without sky-compiler.cabal" $ do
            -- Create a sibling temp dir with no cabal marker.
            -- Build will fail (no Main.sky), but the FAILURE
            -- mode must NOT be the repo-root guard.
            sky <- findSky
            tmp <- getTemporaryDirectory
            let userProj = tmp </> "sky-repo-root-guard-spec"
            createDirectoryIfMissing True userProj
            -- Make sure no stray sky-compiler.cabal leaked in.
            cabalExists <- doesFileExist (userProj </> "sky-compiler.cabal")
            cabalExists `shouldBe` False
            let cp = (proc sky ["build", "src/Main.sky"])
                        { cwd = Just userProj }
            (_, out, err) <- readCreateProcessWithExitCode cp ""
            let combined = out ++ err
            ("refusing to run from the Sky compiler" `isInfixOf` combined)
                `shouldBe` False
            -- Cleanup.
            removeDirectoryRecursive userProj
