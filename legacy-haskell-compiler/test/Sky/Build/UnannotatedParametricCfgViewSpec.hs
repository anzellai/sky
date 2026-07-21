module Sky.Build.UnannotatedParametricCfgViewSpec (spec) where

-- Issue #521 regression — unannotated parametric-Cfg view function
-- in a Sky.Live app.  The body uses Std.Ui kernel calls (Ui.column /
-- Ui.form / Ui.el / Ui.onSubmit / Ui.onClick) routing the cfg's
-- callback fields through typed event HOFs.
--
-- Pre-fix the codegen emitted `any(cfg).(Widget_Cfg_R[any])` casts
-- in the generic body; monomorphise's token-level substitution
-- couldn't rewrite `[any]` back to `[Msg]`, and the specialised
-- wrapper's `Widget_Cfg_R[Msg]` value panicked on the assertion
-- with `interface conversion: Widget_Cfg_R[SkyADT] vs
-- Widget_Cfg_R[interface{}]`.
--
-- This was the load-bearing trigger for skydeploy's Editor.sky
-- (#521 task description) — every tab click panicked at the
-- inline-state hydration.  Closing this spec also closes the
-- broader `Foo_R[any]`-cast-panic class for parametric record
-- aliases (sibling family: #261/#262/#263/#461/#463/#465/#467).
--
-- The fix lives in src/Sky/Build/{Compile.hs,LowerCtx.hs}:
--   - LowerCtx.withEnclosingTypeParams / lookupEnclosingTypeParam
--   - Compile.withScopedEnclosingTypeParams (the eager IORef push
--     around dep + entry function-body construction)
--   - Compile.enclosingTypeParamInScope (the lookup helper)
--   - Compile.eraseTypeParamsExceptScope (selective erasure that
--     pins in-scope TVars instead of widening them to `any`)
--   - substituteOnly at three call sites (VarKernel branch,
--     coerceCallArgs, coerceCallArgsAt) now partitions unbound
--     TVars by scope membership before erasing.
--
-- Source: docs/v0.16.x-console/parametric-cfg-repro/ — the
-- canonical fixture preserved as documentation alongside the
-- code change.
--
-- This spec asserts:
--   1. The fixture builds clean (Sky lowering + go build).
--   2. The emitted generic body for Widget_view carries
--      `Widget_Cfg_R[T<n>]`-typed casts somewhere — proof the
--      enclosing-scope guard fired.
--   3. The emitted body's helper calls do NOT carry the
--      `any(...).(Widget_Cfg_R[any])` panic pattern.

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist, listDirectory)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
          else fail ("sky binary missing at " ++ candidate
                  ++ " — run cabal install --installdir=./sky-out first")


findReproDir :: IO FilePath
findReproDir = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "docs" </> "v0.16.x-console" </> "parametric-cfg-repro"
    ok <- doesFileExist (candidate </> "sky.toml")
    if ok then return candidate
          else fail ("parametric-cfg-repro fixture missing at " ++ candidate)


runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
runSky sky args workDir = do
    let cp = (proc sky args) { cwd = Just workDir }
    readCreateProcessWithExitCode cp ""


-- Copy the canonical fixture from docs/ into a clean temp dir.
-- The fixture has Main.sky and Widget.sky at the top level; sky.toml
-- points at src/Main.sky, so we shuffle them into src/ on copy.
prepareFixture :: FilePath -> FilePath -> IO ()
prepareFixture srcDir destDir = do
    createDirectoryIfMissing True (destDir </> "src")
    entries <- listDirectory srcDir
    let files = [ e | e <- entries
                    , e `notElem` [".", "..", "sky-out", ".skycache",
                                   ".skydeps", "README.md"] ]
    mapM_ (copyOne srcDir destDir) files
  where
    copyOne sd dd name
        | ".sky" `isInfixOf` name = do
            contents <- readFile (sd </> name)
            writeFile (dd </> "src" </> name) contents
        | otherwise = do
            contents <- readFile (sd </> name)
            writeFile (dd </> name) contents


spec :: Spec
spec = describe "Issue #521 — unannotated parametric-Cfg view" $ do

    it "the canonical fixture builds clean end-to-end" $
      withSystemTempDirectory "sky-521-build" $ \dir -> do
        repro <- findReproDir
        prepareFixture repro dir
        sky <- findSky
        (exit, stdout', stderr') <- runSky sky ["build", "src/Main.sky"] dir
        let combined = stdout' ++ "\n" ++ stderr'
        ("Compilation successful" `isInfixOf` combined) `shouldBe` True
        exit `shouldBe` ExitSuccess

    it "the generic body preserves a TVar cast (proof the enclosing-scope guard fired)" $
      withSystemTempDirectory "sky-521-tvar" $ \dir -> do
        repro <- findReproDir
        prepareFixture repro dir
        sky <- findSky
        (exit, _stdout, _stderr) <- runSky sky ["build", "src/Main.sky"] dir
        exit `shouldBe` ExitSuccess
        emitted <- readFile (dir </> "sky-out" </> "main.go")
        -- Proof of fix: somewhere in the body, `Widget_Cfg_R[T<n>]`
        -- appears — meaning the lowerer kept the TVar instead of
        -- erasing to `any`.  Monomorphise then rewrites `T<n>` →
        -- the per-call-site concrete type.
        let preservedTVar tn = ("Widget_Cfg_R[" ++ tn ++ "]") `isInfixOf` emitted
        (preservedTVar "T1" || preservedTVar "T2") `shouldBe` True

    it "the generic body emits no panic-shape cast \
       \`any(cfg).(Widget_Cfg_R[any])`" $
      withSystemTempDirectory "sky-521-nopanic" $ \dir -> do
        repro <- findReproDir
        prepareFixture repro dir
        sky <- findSky
        (exit, _stdout, _stderr) <- runSky sky ["build", "src/Main.sky"] dir
        exit `shouldBe` ExitSuccess
        emitted <- readFile (dir </> "sky-out" </> "main.go")
        -- Pre-fix the body's helper-call args were lowered as
        -- `any(cfg).(Widget_Cfg_R[any])` — the panic shape.
        let panicShape = "any(cfg).(Widget_Cfg_R[any])"
        (panicShape `isInfixOf` emitted) `shouldBe` False
