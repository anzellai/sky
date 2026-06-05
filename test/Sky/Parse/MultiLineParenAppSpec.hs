module Sky.Parse.MultiLineParenAppSpec (spec) where

-- Regression fence for "multi-line function application inside grouping parens".
--
-- Pre-fix bug: `tryNextLineArgs` in src/Sky/Parse/Expression.hs (line
-- 138) anchored the next-line continuation indent against the inner
-- function's column (`funcCol`). Inside grouping parens that put the
-- inner func far from column 1, valid continuations on a less-indented
-- column got rejected, surfacing as the cryptic
--     `sky: Expected , or ) in expression`
-- with a Haskell call stack pointing at Expression.hs:223 (the paren
-- close-or-comma sentinel that fired because the inner expression's
-- continuation wasn't consumed). Surfaced from a real-world Std.Ui
-- port:
--
--     Ui.html (renderItems
--         [ "a", "b" ])
--
-- Fix: relax the next-line check to ALSO accept continuation when the
-- candidate column is past the surrounding block's `_indent`. The
-- block-indent rule still rejects sibling declarations (which sit at
-- column == _indent), so it's safe. Sister fix in `isExprStart`:
-- exclude Sky keywords (`then`, `else`, `in`, `of`, …) so the relaxed
-- rule doesn't accidentally consume them as continuation args (which
-- would break if/then/else and let/in / case/of parses).
--
-- Tier 1 (task #491): migrated from subprocess `sky check` to
-- in-process `Compile.compile` via Sky.Build.Helpers.InProcessCompile.
-- The pre-fix assertion that stdout contains "No errors found" is
-- replaced by checking compileInProcess returns CompileOk (success
-- gate is byte-identical); the negative assertion that stdout NOT
-- contain "Expected , or )" is preserved as a CompileErr text check
-- in the failing-path tests (where applicable).

import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


spec :: Spec
spec = do
    describe "Multi-line function application inside grouping parens" $ do

        it "outer (inner\\n    arg) — list-literal arg on next line" $ do
            -- Pre-fix: parser bails with "Expected , or )" because
            -- the inner func column was greater than the continuation
            -- column. Post-fix: the block-indent fallback accepts.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Ui as Ui"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "renderItems : List String -> any"
                    , "renderItems xs = xs"
                    , ""
                    , "view : any"
                    , "view ="
                    , "    Ui.html (renderItems"
                    , "        [ \"a\", \"b\" ])"
                    , ""
                    , "main = let _ = view in println \"ok\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()


        it "outer (inner\\n    \"x\") — string arg on next line" $ do
            -- Smaller variant of the above, no list/record involved.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "outer : String -> String"
                    , "outer s = \"[\" ++ s ++ \"]\""
                    , ""
                    , "inner : String -> String"
                    , "inner s = s ++ \"!\""
                    , ""
                    , "view : String"
                    , "view ="
                    , "    outer (inner"
                    , "        \"alpha\")"
                    , ""
                    , "main = println view"
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()


    describe "Keyword-aware exprStart: relaxed rule doesn't break if/then/else" $ do

        it "if/then/else inside let body — `else` not consumed as cont arg" $ do
            -- Sister-fix sanity: the relaxed continuation rule
            -- excludes keyword leading tokens. Without that, `else`
            -- after `Ui.text \"\"` was being absorbed as if it were
            -- another arg to `Ui.text`, which broke skyforum's
            -- View/Detail.sky parse. This test mirrors that shape.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "speak : Bool -> String"
                    , "speak isEmpty ="
                    , "    let"
                    , "        children = [ \"a\" ]"
                    , "    in"
                    , "        if isEmpty then"
                    , "            \"\""
                    , "        else"
                    , "            String.join \", \" children"
                    , ""
                    , "main = println (speak False)"
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()
