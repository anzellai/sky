module Sky.Type.TupleLambdaSpec (spec) where

-- Regression fence for the tuple-pattern-in-lambda-arg fix.
--
-- Pre-fix bug: `Sky.Type.Constrain.Expression.patternBindings` for
-- `Can.PTuple a b more` bound element types to STATIC names
-- (`_tup_0`, `_tup_1`, ...). These names collapsed via the solver's
-- `_varCache` so multiple tuple destructures in the SAME definition
-- shared element-type variables — e.g.:
--
--     let
--         _ = List.filterMap (\(name, r) -> ...) results
--         _ = List.map (\(name, msg) -> ...) failures
--     in ...
--
-- The two `name`s would share the `_tup_0` slot AND the two second-
-- elements would share the `_tup_1` slot, so HM unified types
-- across-the-lambdas that should have been independent. Surfaced as
-- `Type mismatch: String vs R (from: a vs R)` or
-- `Variable 'msg' type mismatch`.
--
-- Fix: introduce `patternBindingsIO` that mints FRESH type-var
-- names per pattern occurrence via the IO Counter and emits a
-- structural `T.CEqual` constraint tying the outer `ty` to the
-- pattern's structure (tuple/cons/list). Used by
-- `constrainLambda` for lambda parameters.
--
-- Tier 1 (task #491): in-process via compileInProcess.

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


spec :: Spec
spec = do
    describe "Tuple-pattern in lambda arg binds fresh element types per occurrence" $ do

        it "filterMap then map with two `(name, _)` lambdas (Sky.Test pattern)" $ do
            -- The exact shape that caused Sky.Test.summarise to fail
            -- HM type-check pre-fix.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.List as List"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type R = Failed String | Passed"
                    , ""
                    , "go : List ( String, R ) -> Bool"
                    , "go results ="
                    , "    let"
                    , "        failures ="
                    , "            List.filterMap"
                    , "                (\\( name, r ) ->"
                    , "                    case r of"
                    , "                        Failed msg -> Just ( name, msg )"
                    , "                        Passed -> Nothing)"
                    , "                results"
                    , "        _ ="
                    , "            List.map"
                    , "                (\\( name, msg ) -> println (name ++ \" \" ++ msg))"
                    , "                failures"
                    , "    in"
                    , "        True"
                    , ""
                    , "main ="
                    , "    let _ = go [ ( \"t1\", Failed \"oops\" ) ]"
                    , "    in println \"ok\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> do
                    -- Pin the pre-fix failure strings even on a
                    -- compile failure (so a real regression of the
                    -- tuple-pattern fix surfaces immediately).
                    e `shouldNotSatisfy` ("Variable 'msg' type mismatch" `isInfixOf`)
                    e `shouldNotSatisfy` ("String vs R"                 `isInfixOf`)
                    expectationFailure ("compile failed: " ++ e)
                CompileOk _ -> return ()


    describe "`/=` operator works on polymorphic generic params" $ do

        it "Sky.Test.notEqual : a -> a -> TestResult compiles" $ do
            -- Pre-fix: `expected /= actual` lowered to Go-native
            -- `expected != actual` which fails with
            -- `incomparable types in type set` for `T any` generics.
            -- Post-fix: lowers to `rt.NotEq` which uses deepEq
            -- internally and works for any value shape.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "neq : a -> a -> Bool"
                    , "neq x y = x /= y"
                    , ""
                    , "main ="
                    , "    if neq 1 2 then"
                    , "        println \"different values are unequal (correct)\""
                    , "    else"
                    , "        println \"oops\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _ -> return ()
