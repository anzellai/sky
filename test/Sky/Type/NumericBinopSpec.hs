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
--   1. Float arithmetic compiles (was: type-check rejected).
--   2. Int arithmetic compiles unchanged (no regression).
--   3. Mixed Int + Float STILL rejected at compile time (the
--      polymorphism unifies both operands; mixed types fail
--      unification, which is exactly what users want — silent
--      Float ↔ Int coercion is a numeric-precision footgun).
--
-- Tier 1 (task #491): in-process via compileInProcess. The
-- pre-Tier-1 spec also ran the produced binary to confirm the
-- numeric RESULT — `runtime-go/rt/*_test.go` already cover
-- `rt.Add`/`rt.Sub`/`rt.Mul` shape correctness, so the spec's
-- contribution is the HM type-check pass/fail boundary that is
-- now asserted directly off CompileOk / CompileErr.

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


spec :: Spec
spec = do
    describe "Numeric binops (`+`, `-`, `*`) are polymorphic over Int / Float" $ do

        it "Float subtraction compiles" $ do
            -- The minimum-reproducer for the original bug.
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main ="
                    , "    println (String.fromFloat (3.14 - 1.5))"
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()

        it "Float multiplication compiles" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main ="
                    , "    println (String.fromFloat (3.0 * 2.5))"
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()

        it "Float addition compiles" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main ="
                    , "    println (String.fromFloat (1.5 + 2.25))"
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()

        it "Int arithmetic still compiles (no regression)" $ do
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
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()

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
            result <- compileInProcess src
            case result of
                CompileOk _ -> expectationFailure "expected mixed Int/Float to be rejected"
                CompileErr e ->
                    -- Either "Type mismatch" or "Float vs Int" — both
                    -- accepted depending on which arm of unification
                    -- fires first.
                    ("Type mismatch" `isInfixOf` e
                        || "Float vs Int" `isInfixOf` e
                        || "Int vs Float" `isInfixOf` e) `shouldBe` True

        it "Float division (`/`) still compiles (was already Float-only)" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main ="
                    , "    println (String.fromFloat (10.0 / 4.0))"
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()

        it "Int integer division (`//`) still compiles (was already Int-only)" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main ="
                    , "    println (String.fromInt (10 // 3))"
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()
