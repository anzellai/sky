module Sky.Build.VerifyAllSpec (spec) where

-- Audit P3-1: `sky verify` is now CI's canonical runtime check.
-- Invoked with no args it must iterate every example in examples/,
-- honour per-example verify.json scenarios (P2-4), and skip GUI
-- examples cleanly when SKY_SKIP_GUI is set on a headless runner.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
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

        it "sky verify with no arg iterates >= 10 examples" $ do
            -- Replaces CI's hand-picked 6-example list. If a future
            -- example-addition doesn't get wired into CI explicitly,
            -- it's still covered here by virtue of living in
            -- examples/. 10 is a conservative floor — the repo
            -- currently has 18.
            sky <- findSky
            (_ec, out, _err) <- readCreateProcessWithExitCode
                (shell ("SKY_SKIP_GUI=1 " ++ sky ++ " verify"))
                ""
            -- Each example line has "runtime ok:", "FAIL …", or
            -- "[skip]" / "gui skipped runtime" prefix. Count the
            -- total.
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
