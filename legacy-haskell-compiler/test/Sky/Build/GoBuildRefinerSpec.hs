-- | v0.13 Layer 2 — go-build error refiner integration spec.
--
-- Exercises the end-to-end path:
--   1. Build a well-formed Sky source (succeeds, emits main.go
--      with SKY-ORIGIN comments).
--   2. Hand-corrupt main.go to introduce a Go-build error.
--   3. Re-invoke `sky build`.  The incremental cache short-
--      circuits codegen but `go build` runs and fails.
--   4. The refiner in `runGoBuildWithDiagnostics` parses the Go
--      error, looks up the nearest preceding SKY-ORIGIN comment,
--      and emits a `GO BUILD ERROR [E5001]` Diagnostic pointing
--      at Sky source.
--
-- The unit-level pieces are covered by `ValidatorSpec`; this is
-- the integration-level fence.
module Sky.Build.GoBuildRefinerSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)
import qualified Data.Text as T
import qualified Data.Text.IO as TIO


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- | Build the project, returns (exit code, combined output).
runSkyBuild :: FilePath -> FilePath -> IO (ExitCode, String)
runSkyBuild sky tmp = do
    let cp = (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp }
    (ec, out, err) <- readCreateProcessWithExitCode cp ""
    return (ec, out ++ err)


-- | Inject a known-bad Go expression into main.go that go-build
-- will reject (`undefined: NotAName`).  The injection happens
-- immediately after the `func main()` line so the surrounding
-- SKY-ORIGIN comment maps the resulting error back to Sky source.
-- Uses strict Text IO so the readFile handle is closed before
-- writeFile opens the same path.
corruptMainGo :: FilePath -> IO ()
corruptMainGo mainGoPath = do
    src <- TIO.readFile mainGoPath
    let lns = T.lines src
        injected = concatMap inject lns
        inject l
            | T.pack "func main()" `T.isInfixOf` l = [l, T.pack "    _ = NotAName"]
            | otherwise = [l]
    TIO.writeFile mainGoPath (T.unlines injected)


spec :: Spec
spec = do
    describe "go-build error refiner — end-to-end" $ do

        it "remaps a corrupted main.go's Go error to Sky source via [E5001]" $ do
            sky <- findSky
            withSystemTempDirectory "sky-refiner" $ \tmp -> do
                createDirectoryIfMissing True (tmp </> "src")
                writeFile (tmp </> "sky.toml")
                    "[project]\nname = \"refiner\"\nbin = \"app\"\n"
                writeFile (tmp </> "src" </> "Main.sky") $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println \"refiner test\""
                    ]
                -- First build succeeds + emits SKY-ORIGIN.
                (ec1, _) <- runSkyBuild sky tmp
                ec1 `shouldBe` ExitSuccess
                mainGoPath <- return (tmp </> "sky-out" </> "main.go")
                hasMain <- doesFileExist mainGoPath
                hasMain `shouldBe` True
                -- Verify SKY-ORIGIN comment landed.  Strict text IO
                -- so we can re-open the same file for writing below.
                goSrc <- T.unpack <$> TIO.readFile mainGoPath
                goSrc `shouldSatisfy` ("SKY-ORIGIN: src/Main.sky" `isInfixOf`)
                -- Corrupt main.go to trigger a go-build error.
                corruptMainGo mainGoPath
                -- Second build: codegen short-circuits (incremental
                -- cache; source.hash unchanged) but `go build` fails.
                -- The refiner should detect the failure, map it back
                -- to Sky source, and emit the [E5001] Diagnostic.
                (ec2, out) <- runSkyBuild sky tmp
                ec2 `shouldNotBe` ExitSuccess
                out `shouldSatisfy` ("[E5001]" `isInfixOf`)
                out `shouldSatisfy` ("GO BUILD ERROR" `isInfixOf`)
                -- The Sky source path should appear in the diagnostic
                -- header.
                out `shouldSatisfy` ("src/Main.sky" `isInfixOf`)
                -- The raw Go error must still be visible for
                -- contributor debugging.
                out `shouldSatisfy` ("undefined: NotAName" `isInfixOf`)
