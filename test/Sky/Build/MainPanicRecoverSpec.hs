-- MainPanicRecoverSpec — Cycle 6 PC (v0.15.43) verifies the emitted
-- Go `func main()` starts with `defer rt.LogPanicAndExit()`.
--
-- This is the codegen anchor that wires the top-level panic→Err
-- recovery. Regressions here would silently re-expose the
-- synchronous-panic class (CLI / Tui / batch process crashes with
-- a Go stack dump instead of a structured-log line).
--
-- The runtime-side classifyPanic / compressStack / newErrId
-- behaviour is covered by runtime-go/rt/panic_recover_test.go.
-- This spec pins the COMPILER side: a vanilla Sky main builds
-- a main.go whose func main()'s first statement is the deferred
-- call. We also verify a div-by-zero binary exits 1 with the
-- structured-log shape, end-to-end.
module Sky.Build.MainPanicRecoverSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)

findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
          else fail ("sky binary missing at " ++ candidate
                  ++ " — run scripts/build.sh first")

runProc :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
runProc cmd args workDir = do
    let cp = (proc cmd args) { cwd = Just workDir }
    readCreateProcessWithExitCode cp ""

writeProject :: FilePath -> String -> String -> IO ()
writeProject dir name body = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml")
        ("name = \"" ++ name ++ "\"\nversion = \"0.0.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
    writeFile (dir </> "src" </> "Main.sky") body

spec :: Spec
spec = do
    describe "func main() panic-recover wrapper" $ do
        it "emits `defer rt.LogPanicAndExit()` as the first stmt of func main()" $ do
            sky <- findSky
            withSystemTempDirectory "sky-mpr-shape" $ \tmp -> do
                writeProject tmp "shape-test" $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println \"hello\""
                    ]
                (ec, _out, err) <- runProc sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                let mainGo = tmp </> "sky-out" </> "main.go"
                exists <- doesFileExist mainGo
                exists `shouldBe` True
                contents <- readFile mainGo
                -- The defer is injected as the very first statement
                -- of the emitted func main(). Order matters — a deferred
                -- LogPanicAndExit after any other panicking call would
                -- miss panics fired before the defer is registered.
                let lns = lines contents
                let mainStart =
                        dropWhile (\l -> not ("func main()" `isInfixOf` l)) lns
                case mainStart of
                    (_funcLine : firstStmt : _) ->
                        ("defer rt.LogPanicAndExit()" `isInfixOf` firstStmt)
                            `shouldBe` True
                    _ -> expectationFailure ("no func main() found in:\n" ++ contents
                                          ++ "\nsky stderr:\n" ++ err)

    describe "div-by-zero at top level" $ do
        it "exits 1 with a structured-log line (not a Go stack dump)" $ do
            sky <- findSky
            withSystemTempDirectory "sky-mpr-divzero" $ \tmp -> do
                writeProject tmp "divzero-test" $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main ="
                    , "    let"
                    , "        n = 10"
                    , "        d = 0"
                    , "        result = n // d"
                    , "    in"
                    , "    println (String.fromInt result)"
                    ]
                (ecB, _outB, errB) <- runProc sky ["build", "src/Main.sky"] tmp
                ecB `shouldBe` ExitSuccess

                -- Run the binary. It must NOT exit 0 (the div by zero
                -- triggered) and the stderr must contain the
                -- structured-log markers we promised: "Sky panic:",
                -- "DivisionByZero", "ref ", and an errId hint.
                let appPath = tmp </> "sky-out" </> "app"
                (ecR, outR, errR) <- runProc appPath [] tmp
                ecR `shouldNotBe` ExitSuccess
                let combined = outR ++ "\n" ++ errR
                ("Sky panic" `isInfixOf` combined) `shouldBe` True
                ("DivisionByZero" `isInfixOf` combined) `shouldBe` True
                ("ref " `isInfixOf` combined) `shouldBe` True
                -- And critically: NO `panic: rt.IntDiv` raw Go stack
                -- dump as the first thing the user sees. The recover
                -- catches it.
                ("goroutine 1 [running]" `isInfixOf` combined)
                    `shouldBe` False
                errB `seq` return ()
