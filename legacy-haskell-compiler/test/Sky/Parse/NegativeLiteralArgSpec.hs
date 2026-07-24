module Sky.Parse.NegativeLiteralArgSpec (spec) where

-- v0.17 Limitation #4 regression spec — negative literal in
-- application-argument position.
--
-- Pre-fix Sky's `appArgs` parser stopped on any operator character,
-- so `add 10 -5` was tokenised as `(add 10) - 5`.  Since `add 10`
-- has type `Int -> Int`, the HM solver rejected with "Type
-- mismatch: expected Int -> Int, actual Int" — never reaching the
-- intended `add 10 (-5)` shape.  Required workaround: explicit
-- parens `(-5)` at every numeric-negative argument site (per CLAUDE.md
-- Limitation #4 documentation).
--
-- Post-fix `appArgs` peeks two chars ahead.  When the next char is
-- `-` AND the char after is a digit (no intervening whitespace —
-- the `spaces` consumer has already eaten inter-token whitespace
-- before `appArgs` runs), the `-` is admitted as a unary-negate
-- prefix introducing a negative-literal argument.  Binary
-- subtraction (`f - 1` with spaces around `-`) remains binary
-- because in that case the `peekNextIsNegativeDigit` check sees
-- whitespace after `-` and returns False.
--
-- This spec exercises the canonical user-visible cases:
--   * single negative-literal arg
--   * multiple negative-literal args back-to-back
--   * mixed positional + negative-literal
--   * binary subtraction with spaces remains binary (no regression)
--   * legacy paren form `(-n)` still works (no regression)

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


spec :: Spec
spec = describe "negative literal in application argument position (#632 / Limitation #4)" $ do

    it "accepts `add 10 -5` as `add 10 (-5)`" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "add : Int -> Int -> Int"
                , "add a b = a + b"
                , ""
                , "main = println (String.fromInt (add 10 -5))"
                ]
        result <- compileInProcess src
        case result of
            CompileOk _ -> return ()
            CompileErr e ->
                expectationFailure ("expected build to succeed, got error:\n" ++ e)

    it "accepts multiple negative-literal args back-to-back: `sub3 100 -50 -1`" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "sub3 : Int -> Int -> Int -> Int"
                , "sub3 a b c = a + b + c"
                , ""
                , "main = println (String.fromInt (sub3 100 -50 -1))"
                ]
        result <- compileInProcess src
        case result of
            CompileOk _ -> return ()
            CompileErr e ->
                expectationFailure ("expected build to succeed, got error:\n" ++ e)

    it "preserves binary subtraction with spaces: `n - m` stays binary" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "main ="
                , "    let"
                , "        n = 10"
                , "        m = 3"
                , "        diff = n - m"
                , "    in"
                , "    println (String.fromInt diff)"
                ]
        result <- compileInProcess src
        case result of
            CompileOk _ -> return ()
            CompileErr e ->
                expectationFailure ("expected build to succeed, got error:\n" ++ e)

    it "still accepts the legacy paren form `add 10 (-5)`" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "add : Int -> Int -> Int"
                , "add a b = a + b"
                , ""
                , "main = println (String.fromInt (add 10 (-5)))"
                ]
        result <- compileInProcess src
        case result of
            CompileOk _ -> return ()
            CompileErr e ->
                expectationFailure ("expected build to succeed, got error:\n" ++ e)

    it "rejects `add 10 - 5` with explicit subtraction-as-application via type error" $ do
        -- `add 10 - 5` parses as `(add 10) - 5`.  Since `add 10 :
        -- Int -> Int`, the binary subtraction is ill-typed.  HM
        -- correctly rejects with a type mismatch.
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "add : Int -> Int -> Int"
                , "add a b = a + b"
                , ""
                , "main = println (String.fromInt (add 10 - 5))"
                ]
        result <- compileInProcess src
        case result of
            CompileOk _ ->
                expectationFailure
                    "expected `add 10 - 5` to be rejected as a binary-subtract type error"
            CompileErr e ->
                e `shouldSatisfy` ("Type" `isInfixOf`)
