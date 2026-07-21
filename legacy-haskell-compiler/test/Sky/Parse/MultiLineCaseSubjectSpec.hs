module Sky.Parse.MultiLineCaseSubjectSpec (spec) where

import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


-- Regression: prior to the v0.14 parser fix, `case <multi-line
-- subject>` followed by `of` on a fresh line failed to parse
-- because `exprCase` only consumed horizontal whitespace between
-- the subject and the `of` keyword. The user-visible symptom was
-- a confusing "Top-level declaration expected" error pointing at
-- the case-following branch line.
--
-- Fix landed in `src/Sky/Parse/Expression.hs` — replaced `spaces`
-- with `freshLine` before the `of` keyword. Safe because `of` is
-- a reserved keyword that never starts a top-level declaration.
--
-- Tier 1 (task #491): migrated from subprocess `sky build` to
-- in-process `Compile.compile` via Sky.Build.Helpers.InProcessCompile.
spec :: Spec
spec = describe "parser accepts multi-line case subject + `of` on fresh line" $ do
    it "compiles a `case Result.mapError ... \\n of`-shaped body" $ do
        result <- compileInProcess fixture
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk _  -> return ()


fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Result as Result"
    , "import Sky.Core.Error as Error"
    , "import Std.Log exposing (println)"
    , ""
    , "main ="
    , "    case Result.mapError"
    , "            (\\_ -> Error.unexpected \"wrapped\")"
    , "            (Err (Error.unexpected \"inner\"))"
    , "    of"
    , "        Err e ->"
    , "            println (\"Err: \" ++ Error.toString e)"
    , ""
    , "        Ok _ ->"
    , "            println \"Ok\""
    ]
