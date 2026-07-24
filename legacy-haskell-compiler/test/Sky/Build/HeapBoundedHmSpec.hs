module Sky.Build.HeapBoundedHmSpec (spec) where

-- Regression fence for Limitation #17 (HM heap exhaustion on
-- Std.Ui-heavy modules).
--
-- Root cause (discovered 2026-04-27 via bisect): the OOM was NOT a
-- compiler-internal quadratic — it was a single ill-typed line in
-- `sky-stdlib/Std/Ui/Input.sky`'s `inputBase` helper:
--
--     :: Ui.onInput (cfg.onChange cfg.text)   -- WRONG
--
-- This passes a `Msg` (the result of applying `cfg.onChange` to
-- `cfg.text`) where `Ui.onInput` expects a `(String -> Msg)`
-- callback. After the typed-event change to `Ui.onInput`, this
-- created a pathological constraint shape that HM had to thrash
-- through, allocating ~2.6 GB/s during dep-module type-checking.
--
-- The fix landed in commit `dc1359b` (early in the same session as
-- this spec): `Ui.onInput cfg.onChange` (pass the function directly).
-- Plus `Ui.input` was added so the helper doesn't fall back to
-- `Ui.el` (which renders as `<div>`, not `<input>`).
--
-- This spec re-runs `sky check` on the heap-fence fixture under a
-- tight heap cap (-M256M, well above the legitimate ~122 MB
-- allocation but well below the 4-5 GB pre-fix explosion). If a
-- future change re-introduces a similar Std.Ui-cascading
-- constraint pathology, the cap trips and the spec fails BEFORE
-- a developer hits an unbounded OOM in the wild.
--
-- The fixture lives at `test-fixtures/heap-bound-fence.sky` (was
-- `src/Main.sky.bak` pre-rename). The new path keeps it OUT of
-- the example's `src/` so module discovery doesn't pick it up
-- alongside the live `Main.sky`, and signals its purpose
-- explicitly — it's a test fixture, not a backup.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


spec :: Spec
spec = do
    describe "Limitation #17 — HM heap-bounded type-check on the heap-fence fixture" $ do
        it "type-checks examples/19-skyforum/test-fixtures/heap-bound-fence.sky under +RTS -M256M" $ do
            -- Pre-fix this allocated 2.6 GB/s and OOMed at 4-5 GB.
            -- Post-fix it allocates ~122 MB total with 1.6 MB max
            -- residency; 256 MB cap gives 2x headroom over the
            -- legitimate budget while catching any pathological
            -- regression early.
            sky <- findSky
            cwd <- getCurrentDirectory
            let exampleDir = cwd </> "examples" </> "19-skyforum"
                fenceRel  = "test-fixtures" </> "heap-bound-fence.sky"
                fencePath = exampleDir </> fenceRel
            fenceExists <- doesFileExist fencePath
            fenceExists `shouldBe` True
            -- The fixture IS valid Sky source — same shape as
            -- examples/19-skyforum/src/Main.sky's pre-split form
            -- (the original 689-line monolithic Reddit-style
            -- forum demo). Kept specifically as the #17 regression
            -- artefact. Passing it as the explicit entry to `sky
            -- check` exercises the same constraint-solving path
            -- that OOMed pre-fix.
            let cp = (proc sky
                        [ "+RTS", "-M256M", "-RTS"
                        , "check", fenceRel
                        ])
                        { cwd = Just exampleDir }
            (ec, out, err) <- readCreateProcessWithExitCode cp ""
            let combined = out ++ err
            ec `shouldBe` ExitSuccess
            -- "Heap exhausted" is the GHC RTS marker when the cap
            -- trips. Must NOT appear.
            ("Heap exhausted" `elem` lines combined) `shouldBe` False
