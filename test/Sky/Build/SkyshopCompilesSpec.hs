module Sky.Build.SkyshopCompilesSpec (spec) where

-- v0.15.x hardening — Cycle 1 / Plan Item P2-followup STANDING lock.
--
-- This spec is the project-scale canary that catches "looks fine
-- in 26 small examples, breaks at scale" regressions in compiler
-- edits.  The original P2 worktree's σ-recovery / TVar-erasure /
-- coerceArg three-way consensus break only surfaced under skyshop
-- because skyshop has the densest concentration of
-- `List.map / List.take / List.filter` chains over typed
-- (`List String`, `List Tup2`, etc.) sources.
--
-- Per the Head Arbitration
-- (`docs/v0.15.x-hardening/arbitrations/HEAD-CYCLE-01-P2.md`),
-- ANY Compile.hs edit by ANY Developer cycle must keep
-- `examples/13-skyshop` building.  Deletion or skipping of THIS
-- spec is grounds for a Head Arbitration re-spawn.
--
-- Behaviour:
--   * If `examples/13-skyshop/sky.toml` exists → build it,
--     assert `sky build` exit 0 (which itself drives `go build`
--     on the emitted Go, so any codegen-side regression trips).
--   * If the example dir is absent (isolated cabal-test runs,
--     stripped-down CI envs) → skip with a pending().  This lets
--     the spec be a no-op when run outside the repo tree.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesDirectoryExist,
                         doesFileExist)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok
        then return candidate
        else fail ("sky binary missing at " ++ candidate
                ++ " — run cabal install --installdir=./sky-out first")


spec :: Spec
spec = describe "examples/13-skyshop clean-build lock" $ do
    it "compiles the Stripe-SDK-scale benchmark cleanly" $ do
        cwd <- getCurrentDirectory
        let exampleDir = cwd </> "examples" </> "13-skyshop"
        exists <- doesDirectoryExist exampleDir
        if not exists
            then pendingWith $
                "examples/13-skyshop not present at " ++ exampleDir
                ++ " — skipped (isolated test run without repo tree)."
            else do
                sky <- findSky
                let cp = (proc sky ["build", "src/Main.sky"])
                            { cwd = Just exampleDir }
                (ec, bout, berr) <- readCreateProcessWithExitCode cp ""
                let combined = bout ++ berr
                case ec of
                    ExitSuccess -> return ()
                    ExitFailure n ->
                        expectationFailure $
                            "skyshop sky build failed (" ++ show n ++ "):\n"
                            ++ combined
                ("Build complete" `isInfixOf` combined) `shouldBe` True
