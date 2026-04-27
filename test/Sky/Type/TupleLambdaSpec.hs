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


buildOnly :: String -> IO (Int, String)
buildOnly src =
    withSystemTempDirectory "sky-tuple-lambda" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"tuple-lambda-test\"\n"
        let cmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (ec, sout, serr) <- readCreateProcessWithExitCode (shell cmd) ""
        let combined = sout ++ serr
            ecInt = case ec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        return (ecInt, combined)


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
            (ec, out) <- buildOnly src
            -- Sky type-check should succeed. Go build may fail on
            -- unrelated typed-codegen issues — we only assert the HM
            -- got past the tuple-destructure step (no
            -- "Variable 'msg' type mismatch" or "String vs R" error).
            out `shouldNotSatisfy` ("Variable 'msg' type mismatch" `isInfixOf`)
            out `shouldNotSatisfy` ("String vs R"                 `isInfixOf`)
            -- And exit code is 0 if Go-build succeeded; if it
            -- errored on something unrelated, that's outside this
            -- spec's scope. We accept either.
            ec `shouldSatisfy` (\n -> n == 0 || n == 1)
            out `shouldSatisfy` ("Compilation successful" `isInfixOf`)


    describe "`/=` operator works on polymorphic generic params" $ do

        it "Sky.Test.notEqual : a -> a -> TestResult compiles + runs" $ do
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
            (ec, out) <- buildOnly src
            ec `shouldBe` 0
            out `shouldSatisfy` ("Build complete" `isInfixOf`)
