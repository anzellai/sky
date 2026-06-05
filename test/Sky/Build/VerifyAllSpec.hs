module Sky.Build.VerifyAllSpec (spec) where

-- Audit P3-1: `sky verify` is now CI's canonical runtime check.
-- Invoked with no args it must iterate every example in examples/,
-- honour per-example verify.json scenarios (P2-4), and skip GUI
-- examples cleanly when SKY_SKIP_GUI is set on a headless runner.
--
-- v0.16.5: the second `it` block (full `sky verify` over EVERY
-- example) was duplicating Sky.Build.ExampleSweep — both build
-- every example from scratch, costing ~10 min of redundant compile
-- time per cabal test run. Per the two-suite architecture (#494):
--
--   * Local dev gate: scripts/test-local.sh runs ExampleSweep
--     (parallel build + HTTP probe via scripts/example-sweep.sh).
--   * CI gate: scripts/test-ci.sh runs VerifyAll's full-sweep
--     block via SKY_RUN_FULL_VERIFY=1.
--
-- Default cabal test only runs the GUI-skip smoke (covers the
-- `sky verify` CLI surface in ~2 s). Setting SKY_RUN_FULL_VERIFY=1
-- re-enables the full per-example iteration when the operator
-- explicitly wants it.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
import System.Environment (lookupEnv)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


spec :: Spec
spec = do
    describe "sky verify all-examples (audit P3-1)" $ do

        it "SKY_SKIP_GUI=1 skips 11-fyne-stopwatch with a clear marker" $ do
            -- Prevents headless CI runners without GTK / Cocoa libs
            -- from failing on the Fyne example. Marker is "[skip]
            -- 11-fyne-stopwatch" so CI logs show the deliberate
            -- skip, not a silent no-op.
            sky <- findSky
            (_ec, out, _err) <- readCreateProcessWithExitCode
                (shell ("SKY_SKIP_GUI=1 " ++ sky ++ " verify 11-fyne-stopwatch"))
                ""
            -- On Linux: full "[skip] 11-fyne-stopwatch: GUI example on Linux …"
            -- On Darwin: the native "gui skipped runtime" message.
            -- Both are acceptable — the skip marker appears either way.
            let hasLinuxSkip = "[skip] 11-fyne-stopwatch" `isInfixOf` out
                hasDarwinSkip = "gui skipped runtime: 11-fyne-stopwatch" `isInfixOf` out
            (hasLinuxSkip || hasDarwinSkip) `shouldBe` True

        it "sky verify with no arg iterates >= 10 examples (CI gate; set SKY_RUN_FULL_VERIFY=1)" $ do
            -- Replaces CI's hand-picked 6-example list. If a future
            -- example-addition doesn't get wired into CI explicitly,
            -- it's still covered here by virtue of living in
            -- examples/. 10 is a conservative floor — the repo
            -- currently has 18+.
            --
            -- v0.16.5: gated behind SKY_RUN_FULL_VERIFY=1 to remove
            -- the ~10 min duplication with Sky.Build.ExampleSweep.
            -- Pendings out in default `cabal test` runs.
            -- scripts/test-ci.sh sets the env var; scripts/
            -- test-local.sh does not (ExampleSweep covers the local
            -- dev flow with HTTP probing).
            runFull <- lookupEnv "SKY_RUN_FULL_VERIFY"
            case runFull of
                Just v | v /= "" && v /= "0" -> do
                    sky <- findSky
                    (_ec, out, _err) <- readCreateProcessWithExitCode
                        (shell ("SKY_SKIP_GUI=1 " ++ sky ++ " verify"))
                        ""
                    -- Each example line has "runtime ok:", "FAIL …",
                    -- or "[skip]" / "gui skipped runtime" prefix.
                    -- Count the total.
                    let lines' = lines out
                        exampleLines =
                            [ l | l <- lines'
                                , any (`isInfixOf` l)
                                      [ "runtime ok:"
                                      , "FAIL build:"
                                      , "FAIL panic:"
                                      , "FAIL scenario"
                                      , "FAIL http"
                                      , "FAIL exit "
                                      , "[skip]"
                                      , "gui skipped runtime:"
                                      ]
                            ]
                    length exampleLines `shouldSatisfy` (>= 10)
                _ -> pendingWith "SKY_RUN_FULL_VERIFY unset — full sky-verify covered by ExampleSweep (local) / scripts/test-ci.sh (CI)"
