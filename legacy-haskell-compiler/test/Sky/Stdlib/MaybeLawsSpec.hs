module Sky.Stdlib.MaybeLawsSpec (spec) where

-- v0.17 G1 (sky-stdlib-correctness §8.1) — Maybe Functor / Monad
-- algebraic law specs.
--
-- Per docs/architecture/sky-stdlib-correctness.md §8.1 G1, the
-- algebraic laws for Sky.Core.Maybe were only verified by
-- inspection.  A regression that breaks Functor identity, monad
-- left-identity, etc. would compile cleanly and the bug would
-- surface only via user-reported behaviour differences.
--
-- This spec ships a Sky fixture that exercises the laws against
-- concrete representative values and asserts equality at runtime.
-- Each test prints a single line and the Haskell harness asserts
-- the output literal.  Subprocess-isolated for the same reason as
-- AnonRecordSubprocessFixtureSpec (in-process IORef state can
-- mask divergence).
--
-- Coverage:
--   * Functor identity:    `Maybe.map identity m == m`
--   * Functor composition: `Maybe.map (f . g) m == Maybe.map f (Maybe.map g m)`
--   * Monad left-identity: `Maybe.andThen f (Just a) == f a`
--   * Monad right-identity:`Maybe.andThen Just m == m`
--   * Monad associativity: `(m >>= f) >>= g == m >>= (\x -> f x >>= g)`
--
-- Tested over both `Just x` and `Nothing` representative values.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import qualified Data.List as List


spec :: Spec
spec = describe "v0.17 G1 — Sky.Core.Maybe algebraic Laws" $ do
    it "Functor + Monad laws hold at runtime over Just / Nothing" $ do
        sky <- findSky
        withSystemTempDirectory "sky-maybe-laws" $ \tmp -> do
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

            -- Each law assertion prints `OK <name>` on success;
            -- the Sky fixture short-circuits to `FAIL <name>` on
            -- equality mismatch.  We assert every OK appears AND
            -- no FAIL appears.
            mapM_ (\name ->
                List.isInfixOf ("OK " ++ name) stdoutS `shouldBe` True)
                [ "functor-identity-just"
                , "functor-identity-nothing"
                , "functor-composition-just"
                , "functor-composition-nothing"
                , "monad-left-identity-just"
                , "monad-left-identity-nothing"
                , "monad-right-identity-just"
                , "monad-right-identity-nothing"
                , "monad-associativity-just"
                , "monad-associativity-nothing"
                ]
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
            ("name = \"maybe-laws\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") fixture


fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Maybe as Maybe"
    , "import Std.Log exposing (println)"
    , ""
    , "-- Same-shape comparison helper.  We can't `==` Maybes"
    , "-- directly without structural equality on payload."
    , "-- For Int payloads (the test domain), pattern-match it."
    , "eqMaybeInt : Maybe Int -> Maybe Int -> Bool"
    , "eqMaybeInt a b ="
    , "    case (a, b) of"
    , "        (Nothing, Nothing) -> True"
    , "        (Just x, Just y) -> x == y"
    , "        _ -> False"
    , ""
    , "report : String -> Bool -> String"
    , "report name ok ="
    , "    if ok then \"OK \" ++ name else \"FAIL \" ++ name"
    , ""
    , "-- Test domain values."
    , "j10 : Maybe Int"
    , "j10 = Just 10"
    , ""
    , "nn : Maybe Int"
    , "nn = Nothing"
    , ""
    , "f : Int -> Int"
    , "f x = x + 1"
    , ""
    , "g : Int -> Int"
    , "g x = x * 2"
    , ""
    , "kf : Int -> Maybe Int"
    , "kf x = Just (x + 100)"
    , ""
    , "kg : Int -> Maybe Int"
    , "kg x = Just (x * 3)"
    , ""
    , "main ="
    , "    let"
    , "        -- Functor identity: map identity m == m"
    , "        l1j = eqMaybeInt (Maybe.map identity j10) j10"
    , "        l1n = eqMaybeInt (Maybe.map identity nn) nn"
    , ""
    , "        -- Functor composition: map (f . g) m == map f (map g m)"
    , "        fg x = f (g x)"
    , "        l2j = eqMaybeInt (Maybe.map fg j10) (Maybe.map f (Maybe.map g j10))"
    , "        l2n = eqMaybeInt (Maybe.map fg nn)  (Maybe.map f (Maybe.map g nn))"
    , ""
    , "        -- Monad left-identity: andThen f (Just a) == f a"
    , "        l3j = eqMaybeInt (Maybe.andThen kf (Just 5)) (kf 5)"
    , "        -- Vacuous on Nothing — still a law: andThen anything Nothing == Nothing."
    , "        l3n = eqMaybeInt (Maybe.andThen kf nn) Nothing"
    , ""
    , "        -- Monad right-identity: andThen Just m == m"
    , "        l4j = eqMaybeInt (Maybe.andThen Just j10) j10"
    , "        l4n = eqMaybeInt (Maybe.andThen Just nn)  nn"
    , ""
    , "        -- Monad associativity:"
    , "        --   andThen kg (andThen kf m) == andThen (\\x -> andThen kg (kf x)) m"
    , "        lhsJ = Maybe.andThen kg (Maybe.andThen kf j10)"
    , "        rhsJ = Maybe.andThen (\\x -> Maybe.andThen kg (kf x)) j10"
    , "        l5j  = eqMaybeInt lhsJ rhsJ"
    , "        lhsN = Maybe.andThen kg (Maybe.andThen kf nn)"
    , "        rhsN = Maybe.andThen (\\x -> Maybe.andThen kg (kf x)) nn"
    , "        l5n  = eqMaybeInt lhsN rhsN"
    , ""
    , "        _ = println (report \"functor-identity-just\" l1j)"
    , "        _ = println (report \"functor-identity-nothing\" l1n)"
    , "        _ = println (report \"functor-composition-just\" l2j)"
    , "        _ = println (report \"functor-composition-nothing\" l2n)"
    , "        _ = println (report \"monad-left-identity-just\" l3j)"
    , "        _ = println (report \"monad-left-identity-nothing\" l3n)"
    , "        _ = println (report \"monad-right-identity-just\" l4j)"
    , "        _ = println (report \"monad-right-identity-nothing\" l4n)"
    , "        _ = println (report \"monad-associativity-just\" l5j)"
    , "        _ = println (report \"monad-associativity-nothing\" l5n)"
    , "    in"
    , "    println \"maybe-laws done\""
    ]
