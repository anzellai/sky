module Sky.Parse.MultiLineCaseKeywordSpec (spec) where

import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


-- Cycle 4 D4 regression: a `case` keyword on its own line with
-- the subject on the next line failed to parse.  The companion
-- `MultiLineCaseSubjectSpec` only covered the `<subject>\n of`
-- transition; D4 is the OTHER half — the `case\n <subject>`
-- transition.  The fix mirrors `exprLet`: insert
-- `freshLine mkError` at the head of `exprCase` so the parser
-- skips the optional newline between the `case` keyword and the
-- subject expression.
--
-- Tier 1 (task #491): migrated from subprocess `sky build` to
-- in-process `Compile.compile` via Sky.Build.Helpers.InProcessCompile.
spec :: Spec
spec = describe "parser accepts `case` keyword with subject on next line" $ do
    it "compiles a `case\\n    subject\\n    of` body" $ do
        result <- compileInProcess fixture
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk _  -> return ()


fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "classify : Int -> Int -> Int -> String"
    , "classify a b c ="
    , "    case"
    , "        (a, b, c)"
    , "    of"
    , "        (0, 0, 0) -> \"zeros\""
    , "        _ -> \"other\""
    , ""
    , "main ="
    , "    println (classify 1 2 3)"
    ]
