module Sky.Build.CheckIsBuildSpec (spec) where

-- Audit P0-1: `sky check` must be a superset of `sky build`. If the
-- Sky type system accepts a program but the Go emitter produces code
-- that `go build` rejects, the checker is lying. Before this fix,
-- `sky check` stopped after codegen and never exercised `go build` —
-- that's exactly how the fibonacci .(int) bug and the http-server
-- Task-coerce bug reached users.
--
-- The spec runs both commands on every test-files/*.sky fixture and
-- asserts they agree on accept/reject. A divergence means either
-- codegen is broken for that fixture (bug to fix) or the checker is
-- silently tolerant (bug to fix).
--
-- v0.15.52 #396 — workdir isolation. Pre-fix the spec ran `sky
-- {check,build}` from the repo cwd, so the emitted `sky-out/main.go`
-- and `.skycache/` raced RecordFieldOrderSpec (which also writes
-- there) and any other spec that reads them under parallel sweep.
-- The fix is the same shape as #381: this spec now runs ALL
-- invocations under one dedicated `withSystemTempDirectory "sky-cib-"`
-- workdir per spec invocation — shared across fixtures (so the
-- Sky-side `.skycache/` + Go build cache stay warm between
-- fixtures, keeping the sweep wall-time the same as the pre-fix
-- shared-cwd run) but completely walled off from any other spec's
-- view of the in-tree `sky-out/` directory.

import Test.Hspec
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import System.Directory (listDirectory, getCurrentDirectory, doesFileExist,
                         removePathForcibly)
import System.FilePath ((</>), takeFileName)
import System.IO.Temp (withSystemTempDirectory)
import Data.List (isSuffixOf, isInfixOf, sort)


-- Per-file accept/reject pair. We accept exit code only, not stderr
-- content — the point is structural agreement, not message matching.
data Verdict = VAccept | VReject deriving (Eq, Show)

verdictOf :: ExitCode -> Verdict
verdictOf ExitSuccess     = VAccept
verdictOf (ExitFailure _) = VReject


spec :: Spec
spec = do
    describe "sky check ≥ sky build (audit P0-1)" $ do
        it "sky check invokes `go build` as part of checking" $ do
            -- Demonstrates the fix is active. Pre-fix, sky check
            -- stopped after Compile.compile and never wrote a
            -- 'Running go build...' line. Post-fix it does. A future
            -- implementation that replaces the shell-out with an
            -- in-process Go parse must update this spec.
            cwd <- getCurrentDirectory
            let fixture = cwd </> "test-files" </> "add-test.sky"
                sky = cwd </> "sky-out" </> "sky"
            fixtureExists <- doesFileExist fixture
            fixtureExists `shouldBe` True
            withSystemTempDirectory "sky-cib-hello" $ \tmp -> do
                let cp = (proc sky ["check", fixture]) { cwd = Just tmp }
                (_ec, out, _err) <- readCreateProcessWithExitCode cp ""
                ("Running go build..." `isInfixOf` out) `shouldBe` True

        it "agrees with sky build on every test-files/*.sky fixture" $ do
            cwd <- getCurrentDirectory
            let fixtureDir = cwd </> "test-files"
                sky = cwd </> "sky-out" </> "sky"
            skyBinary <- doesFileExist sky
            skyBinary `shouldBe` True
            names <- listDirectory fixtureDir
            let skyFiles = sort [ fixtureDir </> n | n <- names, ".sky" `isSuffixOf` n ]
            -- One shared workdir for ALL fixtures in this spec —
            -- keeps the Sky `.skycache/` + Go build cache warm
            -- (so the sweep stays as fast as the pre-fix in-tree
            -- shared-cwd run) but isolates the artefacts from any
            -- OTHER spec that also pokes the repo's `sky-out/`.
            divergences <- withSystemTempDirectory "sky-cib-sweep" $ \tmp ->
                traverse (runBoth sky tmp) skyFiles
            let disagreeing =
                    [ (takeFileName f, c, b)
                    | (f, c, b) <- divergences
                    , c /= b
                    ]
            disagreeing `shouldBe` []


-- Run both `sky check` and `sky build` against the fixture inside
-- the given workdir. Scrubs the per-workdir `sky-out/main.go` +
-- `.skycache/` between the two invocations so check and build see
-- an identical starting state. Returns the verdicts side-by-side.
runBoth :: FilePath -> FilePath -> FilePath -> IO (FilePath, Verdict, Verdict)
runBoth sky workdir fixture = do
    let cleanArtefacts = do
            removePathForcibly (workdir </> ".skycache")
            removePathForcibly (workdir </> "sky-out" </> "main.go")
    -- check
    cleanArtefacts
    let cpCheck = (proc sky ["check", fixture]) { cwd = Just workdir }
    (cec, _, _) <- readCreateProcessWithExitCode cpCheck ""
    -- build
    cleanArtefacts
    let cpBuild = (proc sky ["build", fixture]) { cwd = Just workdir }
    (bec, _, _) <- readCreateProcessWithExitCode cpBuild ""
    return (fixture, verdictOf cec, verdictOf bec)
