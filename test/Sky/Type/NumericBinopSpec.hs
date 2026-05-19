module Sky.Type.NumericBinopSpec (spec) where

-- Regression fence for the polymorphic-numeric-binop fix (2026-05-18).
--
-- Pre-fix: `+`, `-`, `*` were hardcoded `Int -> Int -> Int` in
-- src/Sky/Type/Constrain/Expression.hs's `binopTypes`. Float
-- arithmetic was effectively broken — `1.5 - 0.5` failed with
-- "Variable 'a' type mismatch: Float vs Int". Workarounds spread
-- through user code (e.g. sky-bundled/console/src/View.sky's
-- formatPercent had to spell `f / 0.0001` instead of `f * 10000`).
--
-- Fix: `+ - *` are now polymorphic over a single TVar (`a -> a ->
-- a`), same shape as the existing polymorphic `++`. The runtime
-- helpers (`rt.Add`, `rt.Sub`, `rt.Mul`) already handle both Int
-- and Float via reflect dispatch; the codegen drops to native Go
-- binops when both operand types resolve concretely.
--
-- These tests pin three invariants:
--   1. Float arithmetic compiles + runs (was: type-check rejected).
--   2. Int arithmetic compiles + runs unchanged (no regression).
--   3. Mixed Int + Float STILL rejected at compile time (the
--      polymorphism unifies both operands; mixed types fail
--      unification, which is exactly what users want — silent
--      Float ↔ Int coercion is a numeric-precision footgun).

import Test.Hspec
import qualified System.Exit as Exit
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import System.IO.Temp (withSystemTempDirectory)
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


buildAndRun :: String -> IO (Int, String)
buildAndRun src =
    withSystemTempDirectory "sky-numeric-binop" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"binop-test\"\n"
        let cmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1 && ./sky-out/app 2>&1"
        (ec, sout, serr) <- readCreateProcessWithExitCode (shell cmd) ""
        let combined = sout ++ serr
            ecInt = case ec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        return (ecInt, combined)


buildOnly :: String -> IO (Int, String)
buildOnly src =
    withSystemTempDirectory "sky-numeric-binop" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"binop-test\"\n"
        let cmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (ec, sout, serr) <- readCreateProcessWithExitCode (shell cmd) ""
        let combined = sout ++ serr
            ecInt = case ec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        return (ecInt, combined)


spec :: Spec
spec = do
    describe "Numeric binops (`+`, `-`, `*`) are polymorphic over Int / Float" $ do

        it "Float subtraction compiles + runs" $ do
            -- The minimum-reproducer for the original bug.
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main ="
                    , "    println (String.fromFloat (3.14 - 1.5))"
                    ]
            (ec, out) <- buildAndRun src
            ec `shouldBe` 0
            -- Result: 1.64 (subject to float repr — accept any 1.64*)
            ("1.64" `isInfixOf` out) `shouldBe` True

        it "Float multiplication compiles + runs" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main ="
                    , "    println (String.fromFloat (3.0 * 2.5))"
                    ]
            (ec, out) <- buildAndRun src
            ec `shouldBe` 0
            ("7.5" `isInfixOf` out) `shouldBe` True

        it "Float addition compiles + runs" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main ="
                    , "    println (String.fromFloat (1.5 + 2.25))"
                    ]
            (ec, out) <- buildAndRun src
            ec `shouldBe` 0
            ("3.75" `isInfixOf` out) `shouldBe` True

        it "Int arithmetic still compiles + runs (no regression)" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main ="
                    , "    let"
                    , "        a = 10 - 3"
                    , "        b = 4 * 5"
                    , "        c = a + b"
                    , "    in"
                    , "        println (String.fromInt c)"
                    ]
            (ec, out) <- buildAndRun src
            ec `shouldBe` 0
            -- 10-3 + 4*5 = 7 + 20 = 27
            ("27" `isInfixOf` out) `shouldBe` True

        it "Mixed Int + Float subtraction is REJECTED at compile time" $ do
            -- Polymorphic typing unifies both operands; Int ≠ Float so
            -- the constraint fails. We DON'T silently coerce; the
            -- user must spell `Basics.toFloat n - f` (or similar)
            -- when they want Float arithmetic on an Int operand.
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main ="
                    , "    let z = 10 - 3.5"
                    , "    in println (String.fromFloat z)"
                    ]
            (ec, out) <- buildOnly src
            ec `shouldNotBe` 0
            -- Either "Type mismatch" or "Float vs Int" — both
            -- accepted depending on which arm of unification
            -- fires first.
            ("Type mismatch" `isInfixOf` out
                || "Float vs Int" `isInfixOf` out
                || "Int vs Float" `isInfixOf` out) `shouldBe` True

        it "Float division (`/`) still works (was already Float-only)" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main ="
                    , "    println (String.fromFloat (10.0 / 4.0))"
                    ]
            (ec, out) <- buildAndRun src
            ec `shouldBe` 0
            ("2.5" `isInfixOf` out) `shouldBe` True

        it "Int integer division (`//`) still works (was already Int-only)" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main ="
                    , "    println (String.fromInt (10 // 3))"
                    ]
            (ec, out) <- buildAndRun src
            ec `shouldBe` 0
            ("3" `isInfixOf` out) `shouldBe` True
