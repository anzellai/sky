module Sky.Build.SolverBudgetSpec (spec) where

-- Tests the defensive bound on the HM constraint solver
-- (Limitation #17 hardening, 2026-04-27).
--
-- The bound caps total `solveHelp` invocations per `solve` call;
-- when exceeded, the solver short-circuits with a clear TYPE ERROR
-- rather than allowing unbounded heap consumption (the failure
-- mode that previously OOMed the host machine).
--
-- Three properties to pin:
--   1. Default budget (5,000,000) doesn't trip on any legitimate
--      program — verified by the full ExampleSweep + HeapBoundedHm
--      specs passing.
--   2. SKY_SOLVER_BUDGET=N caps the bound to N steps.
--   3. When the cap is exceeded, sky check exits non-zero with a
--      clear TYPE ERROR message that points the user at the most
--      likely source (mistyped polymorphic helper).

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         copyFile, doesFileExist, listDirectory, doesDirectoryExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Environment (getEnvironment)
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


copyTree :: FilePath -> FilePath -> IO ()
copyTree src dst = do
    createDirectoryIfMissing True dst
    entries <- listDirectory src
    mapM_ (\e -> do
        let s = src </> e
            d = dst </> e
        isF <- doesFileExist s
        if isF
            then copyFile s d
            else do
                isD <- doesDirectoryExist s
                if isD then copyTree s d else return ()) entries


-- | Run sky with an extra env var (here: SKY_SOLVER_BUDGET=N).
runSkyWithEnv :: FilePath -> [(String, String)] -> [String] -> FilePath
              -> IO (ExitCode, String, String)
runSkyWithEnv sky extra args wd = do
    base <- getEnvironment
    let cp = (proc sky args) { cwd = Just wd, env = Just (extra ++ base) }
    readCreateProcessWithExitCode cp ""


spec :: Spec
spec = do
    describe "Limitation #17 hardening — HM solver defensive bound" $ do
        it "default budget compiles a tiny well-typed program cleanly" $ do
            -- Sanity: the bound doesn't false-positive on legitimate code.
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "solver-budget"
            withSystemTempDirectory "sky-sb-default" $ \tmp -> do
                copyTree fixtureRoot tmp
                (ec, _, _) <- runSkyWithEnv sky [] ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess

        it "SKY_SOLVER_BUDGET=2 trips with a clear TYPE ERROR (not an OOM, not silent success)" $ do
            -- An unreasonably tight cap forces the bound to fire on any
            -- non-trivial program. The error message must mention the
            -- magic phrase so the user knows what happened and can
            -- raise the cap if intentional.
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "solver-budget"
            withSystemTempDirectory "sky-sb-low" $ \tmp -> do
                copyTree fixtureRoot tmp
                (ec, out, err) <- runSkyWithEnv sky
                    [("SKY_SOLVER_BUDGET", "2")]
                    ["check", "src/Main.sky"]
                    tmp
                let combined = out ++ err
                -- Must NOT be a clean success.
                ec `shouldNotBe` ExitSuccess
                -- Must contain the budget-exceeded marker.
                ("TYPE ERROR: constraint solver exceeded budget"
                    `isInfixOf` combined) `shouldBe` True
                -- Must mention the env-var override path so the user
                -- has an actionable next step.
                ("SKY_SOLVER_BUDGET" `isInfixOf` combined) `shouldBe` True

        it "SKY_SOLVER_BUDGET=0 disables the bound (escape hatch)" $ do
            -- Setting budget to 0 must let the program through. This is
            -- the documented escape hatch — disables the bound entirely
            -- (NOT recommended for shipping but useful for debugging
            -- when the bound is suspected of false-positiving).
            sky <- findSky
            cwd <- getCurrentDirectory
            let fixtureRoot = cwd </> "test" </> "fixtures" </> "solver-budget"
            withSystemTempDirectory "sky-sb-zero" $ \tmp -> do
                copyTree fixtureRoot tmp
                (ec, _, _) <- runSkyWithEnv sky
                    [("SKY_SOLVER_BUDGET", "0")]
                    ["build", "src/Main.sky"]
                    tmp
                ec `shouldBe` ExitSuccess
