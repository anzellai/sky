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
-- continuation wasn't consumed). Surfaced from the sendcrafts Std.Ui
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


checkOnly :: String -> IO (Int, String)
checkOnly src =
    withSystemTempDirectory "sky-multiline-paren" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"multiline-paren-test\"\n"
        let cmd = "cd " ++ tmp ++ " && " ++ sky ++ " check src/Main.sky 2>&1"
        (ec, sout, serr) <- readCreateProcessWithExitCode (shell cmd) ""
        let combined = sout ++ serr
            ecInt = case ec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        return (ecInt, combined)


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
            (ec, out) <- checkOnly src
            ec `shouldBe` 0
            out `shouldNotSatisfy` ("Expected , or )" `isInfixOf`)
            out `shouldSatisfy` ("No errors found" `isInfixOf`)


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
            (ec, out) <- checkOnly src
            ec `shouldBe` 0
            out `shouldSatisfy` ("No errors found" `isInfixOf`)


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
            (ec, out) <- checkOnly src
            ec `shouldBe` 0
            out `shouldSatisfy` ("No errors found" `isInfixOf`)
