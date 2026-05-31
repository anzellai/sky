module Sky.Build.ExampleSweepSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist, removePathForcibly,
                         createDirectoryIfMissing, getTemporaryDirectory)
import System.IO.Temp (createTempDirectory)
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
--
-- Bug #381 fix: the spec now invokes the script with `--workdir` so
-- the sweep operates on copies of `examples/*` under
-- `$TMPDIR/sky-example-sweep-<pid>/`. Without this, the script's
-- `rm -rf sky-out .skycache/lowered .skycache/go` races the TypedFfi
-- and UnreachableGate specs, which read the in-tree `examples/*/sky-out/`
-- + `.skycache/go/` artefacts. Sequential hspec ordering puts
-- ExampleSweep first, but a subsequent spec re-running `sky build` on
-- the same example (SkyshopCompiles → examples/13-skyshop) could still
-- partially overwrite + corrupt those reads in the abstract; copying
-- to $TMPDIR removes the shared-mutable surface entirely.
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
                    -- $TMPDIR/sky-example-sweep-XXXXXX isolates each
                    -- test run. `createTempDirectory` returns a
                    -- guaranteed-unique path that survives parallel
                    -- cabal-test invocations.
                    tmpRoot <- getTemporaryDirectory
                    createDirectoryIfMissing True tmpRoot
                    workdir <- createTempDirectory tmpRoot "sky-example-sweep-"
                    (ec, out, err) <- readCreateProcessWithExitCode
                        (proc "bash" [script, "--build-only", "--workdir", workdir]) ""
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
                    -- Belt-and-braces cleanup: script's own EXIT trap
                    -- already removed $workdir/examples, but if the
                    -- shell was killed before the trap ran the workdir
                    -- can leak. Idempotent.
                    removePathForcibly workdir
                    ec `shouldBe` ExitSuccess
