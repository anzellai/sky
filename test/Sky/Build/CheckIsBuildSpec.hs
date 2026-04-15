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

import Test.Hspec
import System.Process (readCreateProcessWithExitCode, shell)
import System.Exit (ExitCode(..))
import System.Directory (listDirectory, getCurrentDirectory, doesFileExist)
import System.FilePath ((</>), takeFileName)
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
            fixtureExists <- doesFileExist fixture
            fixtureExists `shouldBe` True
            (_ec, out, _err) <- readCreateProcessWithExitCode
                (shell ("cd " ++ cwd
                        ++ " && ./sky-out/sky check "
                        ++ fixture ++ " 2>&1"))
                ""
            ("Running go build..." `isInfixOf` out) `shouldBe` True

        it "agrees with sky build on every test-files/*.sky fixture" $ do
            cwd <- getCurrentDirectory
            let fixtureDir = cwd </> "test-files"
            skyBinary <- doesFileExist (cwd </> "sky-out" </> "sky")
            skyBinary `shouldBe` True
            names <- listDirectory fixtureDir
            let skyFiles = sort [ fixtureDir </> n | n <- names, ".sky" `isSuffixOf` n ]
            divergences <- traverse (runBoth cwd) skyFiles
            let disagreeing =
                    [ (takeFileName f, c, b)
                    | (f, c, b) <- divergences
                    , c /= b
                    ]
            disagreeing `shouldBe` []


-- Run both `sky check` and `sky build` against the fixture, scrubbing
-- the per-project sky-out/ cache between them so the two commands see
-- an identical starting state. Returns the verdicts side-by-side.
runBoth :: FilePath -> FilePath -> IO (FilePath, Verdict, Verdict)
runBoth cwd fixture = do
    let sky = cwd </> "sky-out" </> "sky"
        clean = "rm -rf " ++ (cwd </> ".skycache") ++ " " ++ (cwd </> "sky-out" </> "main.go")
    -- check
    _ <- readCreateProcessWithExitCode (shell clean) ""
    (cec, _, _) <- readCreateProcessWithExitCode
        (shell (sky ++ " check " ++ fixture ++ " > /dev/null 2>&1"))
        ""
    -- build
    _ <- readCreateProcessWithExitCode (shell clean) ""
    (bec, _, _) <- readCreateProcessWithExitCode
        (shell (sky ++ " build " ++ fixture ++ " > /dev/null 2>&1"))
        ""
    return (fixture, verdictOf cec, verdictOf bec)
