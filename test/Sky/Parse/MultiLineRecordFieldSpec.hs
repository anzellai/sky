module Sky.Parse.MultiLineRecordFieldSpec (spec) where

-- Regression fence for "first record-literal field's value on a new
-- line".
--
-- Pre-fix bug: `exprAtom_` in src/Sky/Parse/Expression.hs handled
-- the FIRST field of a record literal (`{ field = …`) with `spaces`
-- after the `=`, while every SUBSEQUENT field went through
-- `recordField` which uses `freshLine` after the `=`. So:
--
--     call
--         { system =
--             "value"
--         , user = "..."
--         }
--
-- failed at `row=line-of-=, col=just-past-=` with
-- `PARSE ERROR: DeclarationError`, while the same shape on the
-- SECOND field parsed cleanly. Workaround documented in the bug
-- report was lifting the value into a `let` or using the
-- positional auto-constructor.
--
-- Fix: switch the first-field path to `freshLine` so the rule is
-- uniform across fields.
--
-- Tier 1 (task #491): migrated from subprocess `sky check` to
-- in-process `Compile.compile` via Sky.Build.Helpers.InProcessCompile.

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


spec :: Spec
spec = do
    describe "Multi-line first-field record-literal value" $ do

        it "first field's RHS on next line — minimal" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Pair ="
                    , "    { a : String"
                    , "    , b : String"
                    , "    }"
                    , ""
                    , "use : Pair -> String"
                    , "use p = p.a ++ p.b"
                    , ""
                    , "view : String"
                    , "view ="
                    , "    use"
                    , "        { a ="
                    , "            \"hello \""
                    , "        , b = \"world\""
                    , "        }"
                    , ""
                    , "main = println view"
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> do
                    -- Pre-fix bug surfaced as a parser DeclarationError;
                    -- any non-empty CompileErr is itself a regression here.
                    e `shouldNotSatisfy` ("DeclarationError" `isInfixOf`)
                    expectationFailure ("compile failed: " ++ e)
                CompileOk _ -> return ()


        it "first field's RHS on next line, with `++` continuation" $ do
            -- The original bug-report reproducer: every field's value
            -- starts on a new line and the value itself is a `++`
            -- chain that wraps onto further continuation lines.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Error as Error exposing (Error)"
                    , "import Sky.Core.Task as Task"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Prompt ="
                    , "    { system : String"
                    , "    , user : String"
                    , "    , maxTokens : Int"
                    , "    }"
                    , ""
                    , "draftA : String -> String -> String -> Task Error String"
                    , "draftA title category notes ="
                    , "    call"
                    , "        { system ="
                    , "            \"You help a guardian describe a product. \""
                    , "            ++ \"Write 60-90 words.\""
                    , "        , user ="
                    , "            \"Title: \" ++ title"
                    , "            ++ \"\\nCategory: \" ++ category"
                    , "            ++ \"\\nNotes: \" ++ notes"
                    , "        , maxTokens = 300"
                    , "        }"
                    , ""
                    , "call : Prompt -> Task Error String"
                    , "call _ = Task.succeed \"ok\""
                    , ""
                    , "main = println \"ok\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> do
                    e `shouldNotSatisfy` ("DeclarationError" `isInfixOf`)
                    expectationFailure ("compile failed: " ++ e)
                CompileOk _ -> return ()


        it "all fields same-line — sanity" $ do
            -- Already worked pre-fix; locks the existing shape in.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Pair ="
                    , "    { a : String"
                    , "    , b : String"
                    , "    }"
                    , ""
                    , "use : Pair -> String"
                    , "use p = p.a ++ p.b"
                    , ""
                    , "view : String"
                    , "view = use { a = \"hello \", b = \"world\" }"
                    , ""
                    , "main = println view"
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()
