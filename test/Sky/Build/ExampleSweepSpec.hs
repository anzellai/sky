module Sky.Build.ExampleSweepSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
import System.Environment (lookupEnv)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, proc)
import System.Exit (ExitCode(..))

-- Delegates to scripts/example-sweep.sh. The shell script is the canonical
-- sweep; wrapping it here makes `cabal test` cover it alongside unit tests.
--
-- Set SKY_SKIP_SWEEP=1 to skip — CI uses this because the workflow
-- runs `sky verify` in a later step (the sweep duplicates every build),
-- and building skyshop's Stripe + Firebase FFI bindings from cold takes
-- 15+ min per run. Local devs can also set it to keep the unit-spec
-- loop tight.
spec :: Spec
spec = do
    describe "scripts/example-sweep.sh --build-only" $ do
        it "succeeds across all examples" $ do
            skip <- lookupEnv "SKY_SKIP_SWEEP"
            case skip of
                Just v | v /= "" && v /= "0" ->
                    pendingWith "SKY_SKIP_SWEEP set — sweep covered by `sky verify`"
                _ -> do
                    cwd <- getCurrentDirectory
                    let script = cwd </> "scripts" </> "example-sweep.sh"
                    haveScript <- doesFileExist script
                    haveScript `shouldBe` True
                    (ec, out, err) <- readCreateProcessWithExitCode
                        (proc "bash" [script, "--build-only"]) ""
                    -- Surface the sweep's output when it fails so CI
                    -- logs show which example failed and why. Without
                    -- this the hspec failure is just "ExitFailure 1"
                    -- with no diagnosis.
                    case ec of
                        ExitSuccess -> return ()
                        _ -> do
                            putStrLn "─── example-sweep.sh stdout ───"
                            putStrLn out
                            putStrLn "─── example-sweep.sh stderr ───"
                            putStrLn err
                    ec `shouldBe` ExitSuccess
