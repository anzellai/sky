module Sky.Build.ExampleSweepSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, proc)
import System.Exit (ExitCode(..))

-- Delegates to scripts/example-sweep.sh. The shell script is the canonical
-- sweep; wrapping it here makes `cabal test` cover it alongside unit tests.
--
-- Use SKY_SKIP_SWEEP=1 to skip when iterating locally on unit-only changes.
spec :: Spec
spec = do
    describe "scripts/example-sweep.sh --build-only" $ do
        it "succeeds across all examples" $ do
            cwd <- getCurrentDirectory
            let script = cwd </> "scripts" </> "example-sweep.sh"
            haveScript <- doesFileExist script
            haveScript `shouldBe` True
            (ec, _out, _err) <- readCreateProcessWithExitCode
                (proc "bash" [script, "--build-only"]) ""
            ec `shouldBe` ExitSuccess
