module Sky.Stdlib.TaskLawsSpec (spec) where

-- v0.17 G1 (sky-stdlib-correctness §8.1) — Task Functor / Monad
-- algebraic law specs (minimal pass).
--
-- Sky.Core.Task is a Monad over `a` with `succeed` as `pure` and
-- `andThen` as `>>=`.  Per docs/architecture/sky-stdlib-correctness.md
-- §6 laws verified by inspection; this spec promotes a minimal
-- subset to a runtime regression gate.
--
-- SCOPE (this session): Functor identity over `succeed`-tier
-- Task values.  Tasks aren't directly observable for equality,
-- so the law projects through `Task.run : Task e a -> Result e a`
-- then compares the resulting Results.  This is the foundational
-- law (a Functor that fails identity is not a Functor).
--
-- TODO (next session, after v0.17 sealed-iface ADT close):
--   * Functor identity over `fromResult (Err e)`-tier values
--     (matches Maybe-Nothing / Result-Err arm in MaybeLaws /
--     ResultLaws).
--   * Functor composition
--   * Monad left-identity:    `andThen f (succeed a) == f a`
--   * Monad right-identity:   `andThen succeed t == t`
--   * Monad associativity
--   * Err short-circuits andThen
--   * fromResult round-trip: `Task.run (Task.fromResult r) == r`
--
-- Same v0.17 typed-codegen regression class as ResultLawsSpec
-- applies — `Task String a` shapes hit the same Result-Err
-- multi-call narrowing bug because Task.run lowers to a
-- Result.  Re-open this spec to cover the full law set once
-- criterion-1 closes.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import qualified Data.List as List


spec :: Spec
spec = describe "v0.17 G1 — Sky.Core.Task algebraic Laws (minimal)" $ do
    it "Functor identity holds at runtime over Task.succeed" $ do
        sky <- findSky
        withSystemTempDirectory "sky-task-laws" $ \tmp -> do
            writeFixture tmp
            (ec, _, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            case ec of
                ExitFailure _ ->
                    expectationFailure ("sky build failed:\n" ++ errOut)
                ExitSuccess -> return ()
            built <- doesFileExist (tmp </> "sky-out" </> "app")
            built `shouldBe` True

            (rc, stdoutS, _) <- readCreateProcessWithExitCode
                (proc (tmp </> "sky-out" </> "app") []) { cwd = Just tmp } ""
            rc `shouldBe` ExitSuccess

            List.isInfixOf "OK functor-identity-succeed" stdoutS `shouldBe` True
            List.isInfixOf "FAIL" stdoutS `shouldBe` False
  where
    findSky :: IO FilePath
    findSky = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok then return candidate
              else fail ("sky binary missing at " ++ candidate)

    runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
    runSky sky args workDir =
        readCreateProcessWithExitCode
            (proc sky args) { cwd = Just workDir } ""

    writeFixture :: FilePath -> IO ()
    writeFixture dir = do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml")
            ("name = \"task-laws\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") fixture


fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Task as Task"
    , "import Std.Log exposing (println)"
    , ""
    , "-- Project Task back to Result for equality."
    , "eqResultStringInt : Result String Int -> Result String Int -> Bool"
    , "eqResultStringInt a b ="
    , "    case (a, b) of"
    , "        (Ok x, Ok y) -> x == y"
    , "        (Err e1, Err e2) -> e1 == e2"
    , "        _ -> False"
    , ""
    , "report : String -> Bool -> String"
    , "report name ok ="
    , "    if ok then \"OK \" ++ name else \"FAIL \" ++ name"
    , ""
    , "tOk : Task String Int"
    , "tOk = Task.succeed 10"
    , ""
    , "main ="
    , "    let"
    , "        -- Functor identity over Task.succeed — `map identity`"
    , "        -- preserves Task semantics through Task.run."
    , "        l1 ="
    , "            eqResultStringInt"
    , "                (Task.run (Task.map identity tOk))"
    , "                (Task.run tOk)"
    , ""
    , "        _ = println (report \"functor-identity-succeed\" l1)"
    , "    in"
    , "    println \"task-laws done\""
    ]
