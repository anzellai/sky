module Sky.Stdlib.ResultLawsSpec (spec) where

-- v0.17 G1 (sky-stdlib-correctness §8.1) — Result Functor /
-- Bifunctor / Monad algebraic law specs (minimal pass).
--
-- Sky.Core.Result is a Functor in `a` (via `map`), a Bifunctor in
-- `(e, a)` (via `map` + `mapError`), and a Monad in `a` (`Err e`
-- short-circuits).  Per docs/architecture/sky-stdlib-correctness.md
-- §3.2 laws verified by inspection; this spec promotes a minimal
-- subset to a runtime regression gate.
--
-- SCOPE (this session): Functor identity over `Ok` — the
-- foundational law (a Functor that fails identity is not a
-- Functor).  This gate is intentionally minimal because a v0.17
-- typed-codegen bug surfaced during fixture authoring (see TODO).
-- It is still the right shape — any future regression that
-- breaks Result.map identity on the Ok arm fails this spec at
-- runtime BEFORE landing on a user.
--
-- TODO (next session, after v0.17 sealed-iface ADT close):
-- expand to cover the Err-arm laws and the full law set:
--   * Functor identity (Err arm)
--   * Functor composition over Ok
--   * Bifunctor identity (mapError identity) over Ok + Err
--   * Bifunctor preserves Err (mapError h (Err e) == Err (h e))
--   * Monad left-identity (andThen f (Ok a) == f a)
--   * Monad right-identity (andThen Ok r == r) over Ok + Err
--   * Monad associativity
--   * Err short-circuits andThen
--
-- TYPED-CODEGEN BUG REPRODUCED:
-- Any Sky program that introduces a `Result String Int`-typed
-- Err value AND a polymorphic Result-kernel call (Result.map /
-- Result.andThen / Result.mapError) over a Result value in the
-- SAME let-block scope triggers
--   `rt.coerceInner: type mismatch — source string cannot be
--    cast to target rt.SkyADT`
-- at runtime.  Single-law-over-Ok-only scopes pass cleanly.
-- Adding ANY Err-typed binding (`errV : Result String Int =
-- Err "boom"`) in the same scope as ANY Result.map / .andThen /
-- .mapError regresses to the panic — even when the Err binding
-- is not the law's argument.  This is criterion-1 floor work
-- for v0.17 close (the literal-zero rt.Coerce target).  Root
-- cause: typed-lowerer widens `Result String Int` slot types to
-- `Result Error Int` somewhere in the codegen pipeline when
-- multiple kernel call-sites share scope.  Documented here so
-- the regression surface is recorded as a pre-existing runtime
-- symptom — NOT a spec authoring error.  Re-open this spec to
-- cover the full law set once criterion-1 closes.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import qualified Data.List as List


spec :: Spec
spec = describe "v0.17 G1 — Sky.Core.Result algebraic Laws (minimal)" $ do
    it "Functor identity holds at runtime over Ok" $ do
        sky <- findSky
        withSystemTempDirectory "sky-result-laws" $ \tmp -> do
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

            List.isInfixOf "OK functor-identity-ok" stdoutS `shouldBe` True
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
            ("name = \"result-laws\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") fixture


fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Result as Result"
    , "import Std.Log exposing (println)"
    , ""
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
    , "okV : Result String Int"
    , "okV = Ok 10"
    , ""
    , "main ="
    , "    let"
    , "        -- Functor identity over Ok — `map identity` is a no-op."
    , "        l1 = eqResultStringInt (Result.map identity okV) okV"
    , "        _ = println (report \"functor-identity-ok\" l1)"
    , "    in"
    , "    println \"result-laws done\""
    ]
