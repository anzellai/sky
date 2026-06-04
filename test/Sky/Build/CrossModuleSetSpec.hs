module Sky.Build.CrossModuleSetSpec (spec) where

-- v0.16.3 fix(#461) — cross-module Set panic LOCK test.
--
-- Pre-fix: a function exported from one module returning `Set a`
-- panicked at the cross-module call site:
--
--   rt.Coerce: expected map[interface {}]bool, got rt.SkySet
--
-- Sky's typed Go form for `Set a` is `map[any]bool`, but the rt.Set_*
-- kernels return an opaque `SkySet` struct ({items: map[string]any}).
-- The typed-codegen wrapper at the cross-module return-narrow site
-- emitted `rt.Coerce[map[any]bool](Set_fromList(...))` which had no
-- SkySet → map[any]V bridge.
--
-- Fix lives in:
--   * runtime-go/rt/stdlib_extra.go — new `skySetToMap` helper; new
--     `toSkySet` arm accepting `map[K]V`-shaped inputs.
--   * runtime-go/rt/rt.go — `rt.Coerce[T]` + `narrowReflectValue`
--     consult `skySetToMap` before panicking.
--
-- The fixture exercises four representative shapes — all 4 panic
-- pre-fix; all 4 print `2/3/3/2` post-fix. The spec is the regression
-- gate.

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         copyFile, doesFileExist, listDirectory,
                         doesDirectoryExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import System.Environment (getEnvironment)
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


copyTree :: FilePath -> FilePath -> IO ()
copyTree src dst = do
    createDirectoryIfMissing True dst
    entries <- listDirectory src
    mapM_
        (\e -> do
            let s = src </> e
                d = dst </> e
            isF <- doesFileExist s
            if isF
                then copyFile s d
                else do
                    isD <- doesDirectoryExist s
                    if isD then copyTree s d else return ())
        entries


-- Build the spec's env: keep the parent's PATH / HOME / SHELL but
-- pin SKY_RUNTIME_DIR to the current-cwd's runtime-go (i.e. the
-- runtime the binary was built from, which has our fix). Without
-- this pin a nix-shell-wrapped session may inherit an export like
-- `SKY_RUNTIME_DIR=$PWD/runtime-go` set in the shell hook —
-- evaluated against the SHELL's PWD, NOT the test's tmp cwd —
-- pointing the spec at a sibling repo's stale runtime-go.
specEnv :: FilePath -> IO [(String, String)]
specEnv cwd = do
    parentEnv <- getEnvironment
    let stripped = filter (\(k, _) -> k /= "SKY_RUNTIME_DIR") parentEnv
        rtDir    = cwd </> "runtime-go"
    return (("SKY_RUNTIME_DIR", rtDir) : stripped)


spec :: Spec
spec = describe "#461 cross-module Set returns must not panic" $ do
    it "clean-builds the 3-module Helper/Helper2/Main fixture" $ do
        sky <- findSky
        cwd <- getCurrentDirectory
        env <- specEnv cwd
        let fixtureRoot = cwd </> "test" </> "fixtures" </> "cross-module-set"
        withSystemTempDirectory "sky-xmod-set" $ \tmp -> do
            copyTree fixtureRoot tmp
            (ec, bout, berr) <- readCreateProcessWithExitCode
                (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp, env = Just env } ""
            let combined = bout ++ berr
            case ec of
                ExitSuccess -> return ()
                ExitFailure n ->
                    expectationFailure $
                        "sky build failed (" ++ show n ++ "):\n"
                        ++ combined
            ("Build complete" `isInfixOf` combined) `shouldBe` True

    it "runs without panic and prints 2/3/3/2 across all four shapes" $ do
        sky <- findSky
        cwd <- getCurrentDirectory
        env <- specEnv cwd
        let fixtureRoot = cwd </> "test" </> "fixtures" </> "cross-module-set"
        withSystemTempDirectory "sky-xmod-set-run" $ \tmp -> do
            copyTree fixtureRoot tmp
            (bec, bout, berr) <- readCreateProcessWithExitCode
                (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp, env = Just env } ""
            let bcombined = bout ++ berr
            case bec of
                ExitSuccess -> return ()
                ExitFailure n ->
                    expectationFailure $
                        "sky build failed (" ++ show n ++ "):\n"
                        ++ bcombined
            let appPath = tmp </> "sky-out" </> "app"
            (rec, rout, rerr) <- readCreateProcessWithExitCode
                (proc appPath []) { cwd = Just tmp } ""
            let rcombined = rout ++ rerr
            case rec of
                ExitSuccess -> return ()
                ExitFailure n ->
                    expectationFailure $
                        "binary exited (" ++ show n ++ "):\n"
                        ++ rcombined
            -- Four sizes printed `crossOne/crossTwo/chain/inlineSize`:
            --   * Helper.uniqueOf ["a","b","a"]            → 2
            --   * Helper2.passthrough ["a","b","a","c"]    → 3
            --   * Set.insert "c" (Helper.uniqueOf [a,b,a]) → 3
            --   * uniqueInline ["a","b","a"]               → 2
            ("2/3/3/2" `isInfixOf` rout) `shouldBe` True
            -- Hard fence: the pre-fix panic string MUST NOT appear.
            ("CoerceFailure" `isInfixOf` rcombined) `shouldBe` False
            ("rt.SkySet" `isInfixOf` rcombined) `shouldBe` False
