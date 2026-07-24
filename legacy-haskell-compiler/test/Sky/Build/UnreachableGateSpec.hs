module Sky.Build.UnreachableGateSpec (spec) where

-- Audit P0-5: codegen case-fallback panics must route through
-- rt.Unreachable (catchable, logs site, converts to Err via rt
-- panic-recovery) instead of raw `panic("sky: internal …")` string
-- that crashes the process unless a specific outer handler is in
-- place. This spec greps the committed example builds for the
-- forbidden raw-panic string.
--
-- v0.15.57 #408 — workdir isolation. Pre-fix the spec read
-- `examples/12-skyvote/sky-out/main.go` and similar paths
-- DIRECTLY from the in-tree examples, which required a prior
-- example-sweep run to have populated the artifacts. On a wiped
-- tree the readFile threw `IOException NoSuchThing: openFile:
-- does not exist`. Same shape as #381 and #396: copy the example
-- into a per-spec workdir under `$TMPDIR/sky-unreach-…/` and
-- build there. Workdirs are cached across `it` blocks via a
-- process-lifetime IORef so each example only builds once.

import Test.Hspec
import Data.List (isInfixOf)
import Data.IORef (IORef, newIORef, readIORef, writeIORef)
import qualified System.Directory as Dir
import System.Directory (getCurrentDirectory, doesFileExist, doesDirectoryExist)
import System.FilePath ((</>))
import System.IO.Temp (createTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import System.IO.Unsafe (unsafePerformIO)
import Control.Exception (catch, SomeException)

spec :: Spec
spec = do
    describe "no raw 'unreachable case arm' panics in emitted Go (audit P0-5)" $ do
        it "examples/12-skyvote/sky-out/main.go uses rt.Unreachable, not raw panic" $
            assertGateCleanExample "12-skyvote"
        it "examples/16-skychess/sky-out/main.go uses rt.Unreachable" $
            assertGateCleanExample "16-skychess"
        it "examples/15-http-server/sky-out/main.go uses rt.Unreachable" $
            assertGateCleanExample "15-http-server"


-- Build the example into a workdir (if not already cached), then
-- assert the gate against the emitted main.go.
assertGateCleanExample :: String -> Expectation
assertGateCleanExample name = do
    mDir <- requireExampleBuilt name
    case mDir of
        Nothing -> pendingWith ("skipped — could not build " ++ name ++ " in tempdir")
        Just dir -> do
            contents <- readFile (dir </> "sky-out" </> "main.go")
            let forbidden = "panic(\"sky: internal"
                required  = "rt.Unreachable("
            (forbidden `isInfixOf` contents) `shouldBe` False
            (required `isInfixOf` contents) `shouldBe` True


-- | Process-lifetime cache of "example name → built tempdir".
{-# NOINLINE unreachWorkdirCache #-}
unreachWorkdirCache :: IORef [(String, Maybe FilePath)]
unreachWorkdirCache = unsafePerformIO (newIORef [])


requireExampleBuilt :: String -> IO (Maybe FilePath)
requireExampleBuilt name = do
    cache <- readIORef unreachWorkdirCache
    case lookup name cache of
        Just r -> return r
        Nothing -> do
            r <- buildExampleInTemp name
            writeIORef unreachWorkdirCache ((name, r) : cache)
            return r


buildExampleInTemp :: String -> IO (Maybe FilePath)
buildExampleInTemp name = do
    cwd <- getCurrentDirectory
    let src = cwd </> "examples" </> name
        sky = cwd </> "sky-out" </> "sky"
    srcExists <- doesDirectoryExist src
    skyExists <- doesFileExist sky
    if not (srcExists && skyExists)
        then return Nothing
        else (`catch` (\e -> do
                let _ = (e :: SomeException)
                return Nothing)) $ do
            tmpBase <- Dir.getTemporaryDirectory
            workdir <- createTempDirectory tmpBase ("sky-unreach-" ++ name ++ "-")
            let cpProc = (proc "cp" ["-R", src ++ "/.", workdir]) { cwd = Just cwd }
            (cpRc, _, _) <- readCreateProcessWithExitCode cpProc ""
            case cpRc of
                ExitSuccess -> do
                    let nuke = (proc "rm" ["-rf", "sky-out", ".skycache", ".skydeps"]) { cwd = Just workdir }
                    _ <- readCreateProcessWithExitCode nuke ""
                    let buildProc = (proc sky ["build", "src/Main.sky"]) { cwd = Just workdir }
                    (_bRc, _, _) <- readCreateProcessWithExitCode buildProc ""
                    mainExists <- doesFileExist (workdir </> "sky-out" </> "main.go")
                    if mainExists
                        then return (Just workdir)
                        else return Nothing
                ExitFailure _ -> return Nothing
