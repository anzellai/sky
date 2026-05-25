module Sky.Build.GoExprTypeInferenceSpec (spec) where

-- | Regression for Cycle-01 / Gap A2 — `goExprGoType` returns
-- Nothing for polymorphic-call results, so downstream coerceArg
-- branches that gate on `Just srcTy <- goExprGoType e` fall back
-- to redundant `rt.Coerce`/`rt.ResultCoerce`/`rt.Coerce[any]`
-- wraps around values whose static type IS recoverable via the
-- HM solver's per-name annotations.
--
-- This spec drives the build of `test/fixtures/goexpr-type-
-- inference/src/Main.sky` (a `Result.andThen` pipeline whose
-- typed result is passed straight to a statically-typed
-- consumer), then asserts:
--
-- 1. The build succeeds (no `go build` error from a botched
--    coercion).
-- 2. The runtime output is `ok:final` — proves the value flows
--    intact through the pipeline.
-- 3. The emitted `main.go` contains NO Coerce wrap around the
--    `pipeline(5)` call site at the `report(...)` arg slot —
--    the pre-fix shape was
--    `report(rt.Coerce[rt.SkyResult[…]](pipeline(5)))` or
--    `report(rt.ResultCoerce[…](pipeline(5)))`.

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         copyFile, doesFileExist, listDirectory,
                         doesDirectoryExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok
        then return c
        else fail ("missing: " ++ c ++ " (run `cabal install … exe:sky` first)")


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


-- | Build the fixture, return (combinedOutErr, mainGo, runOutput).
buildAndRun :: IO (String, String, String)
buildAndRun = do
    sky <- findSky
    cwd <- getCurrentDirectory
    let fixtureRoot = cwd </> "test" </> "fixtures" </> "goexpr-type-inference"
    withSystemTempDirectory "sky-goinfer" $ \tmp -> do
        copyTree fixtureRoot tmp
        let cpBuild = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
        (bec, bout, berr) <- readCreateProcessWithExitCode cpBuild ""
        let combined = bout ++ berr
        case bec of
            ExitFailure n ->
                fail $ "sky build failed (" ++ show n ++ "):\n" ++ combined
            ExitSuccess -> return ()
        mainGo <- readFile (tmp </> "sky-out" </> "main.go")
        -- Run the resulting binary to confirm runtime success.
        let appPath = tmp </> "sky-out" </> "app"
        let cpRun = (proc appPath []) { cwd = Just tmp }
        (rec', rout, rerr) <- readCreateProcessWithExitCode cpRun ""
        let runOut = rout ++ rerr
        case rec' of
            ExitFailure n ->
                fail $ "binary run failed (" ++ show n ++ "):\n" ++ runOut
            ExitSuccess -> return (combined, mainGo, runOut)


spec :: Spec
spec = describe "goExprGoType — polymorphic-call return type fallback" $ do
    it "compiles and runs the Result.andThen pipeline cleanly" $ do
        (_combined, _mainGo, runOut) <- buildAndRun
        -- Functional assertion: the pipeline produced "ok:final".
        ("ok:final" `isInfixOf` runOut) `shouldBe` True

    it "emits no redundant Coerce wrap on the pipeline call site" $ do
        (_combined, mainGo, _runOut) <- buildAndRun
        -- The static-type assertion: the Go body must NOT wrap
        -- `pipeline(...)` in any of the cross-instantiation
        -- coerce helpers.  Pre-fix, `coerceArg` saw
        -- `goExprGoType (GoCall (GoIdent "pipeline") [...])`
        -- return Nothing and routed the arg through one of:
        --
        --   - `rt.ResultCoerce[…](pipeline(…))`  — when the
        --     target was `rt.SkyResult[E, A]` (the target IS
        --     SkyResult, so it routes via the parametric
        --     SkyResult branch in coerceArg).
        --   - `rt.Coerce[rt.SkyResult[…]](pipeline(…))`  — when
        --     the slot fell through to the catch-all.
        --
        -- Post-fix, the `Maybe Can.Expr` fallback recovers the
        -- pipeline's `Result Error String` type, the wrap is
        -- elided, and the call site emits `report(pipeline(5))`
        -- raw.
        ("rt.ResultCoerce[Sky_Core_Error_Error, string](pipeline(5))"
            `isInfixOf` mainGo) `shouldBe` False
        ("rt.Coerce[rt.SkyResult[Sky_Core_Error_Error, string]](pipeline(5))"
            `isInfixOf` mainGo) `shouldBe` False
        -- And confirm the raw call shape is present.
        ("report(pipeline(5))" `isInfixOf` mainGo) `shouldBe` True
