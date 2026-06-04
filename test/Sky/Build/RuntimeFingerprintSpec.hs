module Sky.Build.RuntimeFingerprintSpec (spec) where

-- v0.16.2 #460 regression — copyRuntime must wipe stale files under
-- sky-out/rt/ when the sky binary's embedded runtime has changed
-- since the last build. Pre-fix, PR10-G's deleted console_loop.go /
-- subapp.go lingered in downstream apps' sky-out/rt/ and produced
-- `adminTokenSecret redeclared in this block` go-build failures
-- after `sky upgrade`. Closes the SkyDeploy 0.15.59 → 0.16.1 bump
-- regression.

import Test.Hspec
import System.Directory
    ( getCurrentDirectory, createDirectoryIfMissing, doesFileExist
    , removeFile
    )
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- Minimal Sky project skeleton — just enough that `sky build` lays
-- down sky-out/rt/ + .sky-runtime-fingerprint. The .sky source must
-- compile cleanly so we exercise the cache-hit + cache-miss paths.
writeMinimalProject :: FilePath -> IO ()
writeMinimalProject tmp = do
    writeFile (tmp </> "sky.toml") $ unlines
        [ "name = \"runtime-fingerprint-spec\""
        , "version = \"0.0.1\""
        , "entry = \"src/Main.sky\""
        ]
    createDirectoryIfMissing True (tmp </> "src")
    writeFile (tmp </> "src" </> "Main.sky") $ unlines
        [ "module Main exposing (main)"
        , "import Sky.Core.Prelude exposing (..)"
        , "import Std.Log exposing (println)"
        , "main = println \"runtime-fingerprint-spec\""
        ]


runSky :: FilePath -> FilePath -> [String] -> IO (ExitCode, String, String)
runSky sky workdir args = do
    let cp = (proc sky args) { cwd = Just workdir }
    readCreateProcessWithExitCode cp ""


spec :: Spec
spec = do
    describe "copyRuntime wipes stale sky-out/rt/ on fingerprint mismatch (#460)" $ do
        it "removes a stale .go file when the runtime fingerprint differs" $ do
            sky <- findSky
            withSystemTempDirectory "sky-rt-fingerprint" $ \tmp -> do
                writeMinimalProject tmp
                -- First build: lays down rt/ + writes fingerprint.
                (ec1, _, err1) <- runSky sky tmp ["build", "src/Main.sky"]
                ec1 `shouldBe` ExitSuccess
                err1 `shouldSatisfy` \_ -> True
                let fpFile = tmp </> "sky-out" </> "rt" </> ".sky-runtime-fingerprint"
                doesFileExist fpFile >>= (`shouldBe` True)
                -- Inject a stale file mimicking PR10-G's deleted
                -- console_loop.go / subapp.go. The filename collides
                -- with nothing in the current runtime — so the only
                -- way for `sky build` to remove it is via the
                -- fingerprint-driven wipe.
                let stale = tmp </> "sky-out" </> "rt" </> "STALE_FROM_OLD_SKY.go"
                writeFile stale $ unlines
                    [ "package rt"
                    , "func staleFromOldSky() string { return \"STALE\" }"
                    ]
                -- Corrupt the fingerprint so the next build sees drift.
                removeFile fpFile
                writeFile fpFile "stale-from-pre-v0.16.2\n"
                -- Second build: must wipe rtDir + re-materialise.
                (ec2, _, _) <- runSky sky tmp ["build", "src/Main.sky"]
                ec2 `shouldBe` ExitSuccess
                staleStillThere <- doesFileExist stale
                staleStillThere `shouldBe` False
                -- And the fingerprint should have been replaced with
                -- the canonical value, not left at the corruption.
                replaced <- readFile fpFile
                replaced `shouldSatisfy`
                    (\s -> take 26 s == "sky-runtime-fingerprint-v1")

        it "is a no-op (fingerprint match) when the runtime hasn't changed" $ do
            sky <- findSky
            withSystemTempDirectory "sky-rt-fingerprint-noop" $ \tmp -> do
                writeMinimalProject tmp
                (ec1, _, _) <- runSky sky tmp ["build", "src/Main.sky"]
                ec1 `shouldBe` ExitSuccess
                -- A user-FFI-shaped file in rt/ should survive a
                -- second build when the fingerprint still matches —
                -- otherwise we'd wipe user FFI on every incremental
                -- rebuild. (User FFI lives under rt/ via copyFfiDir;
                -- this is the no-regression check.)
                let userFile = tmp </> "sky-out" </> "rt" </> "user_ffi_marker.go"
                writeFile userFile $ unlines
                    [ "package rt"
                    , "// pretend user-FFI file; copyFfiDir would put"
                    , "// this here on every build, but the fingerprint"
                    , "// match means no wipe runs at all."
                    , "func userFfiMarker() {}"
                    ]
                (ec2, _, _) <- runSky sky tmp ["build", "src/Main.sky"]
                ec2 `shouldBe` ExitSuccess
                -- Same-fingerprint path keeps user_ffi_marker.go
                -- around because no wipe runs. (NOTE: a real user
                -- FFI file would be re-copied by copyFfiDir on every
                -- build, so this only matters for the wipe-vs-no-wipe
                -- branch — not the steady-state user-FFI lifecycle.)
                stillThere <- doesFileExist userFile
                stillThere `shouldBe` True
