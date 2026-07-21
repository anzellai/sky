module Sky.Parse.MultiLineExposingSpec (spec) where

-- Regression fence for the multi-line `module ... exposing (…)` and
-- `import X exposing (…)` parser fix.
--
-- Pre-fix bug: the exposing-list parser used `spaces` (which only
-- skips ` ` and `\t`) between items, so source like
--
--     module Foo exposing
--         ( a
--         , b
--         )
--
-- silently failed inside `exposingClause`. Worse, the failure was
-- swallowed by `oneOfWithFallback` for imports (returning an empty
-- exposing list, silently dropping all imports) and downgraded to
-- a `Warning: could not parse …` for the module header (silently
-- dropping the module from the build graph entirely).
--
-- Fix: replace `spaces` with `freshLine` inside the exposing list
-- (newlines are layout-irrelevant inside parens), and turn parser
-- errors at the module-graph stage into FATAL errors.
--
-- Both invariants pinned here: the multi-line shape compiles + runs;
-- a real parse error fails the build (not a warning).
--
-- Tier 1 (task #491): migrated from subprocess `sky build` to
-- in-process `Compile.compile` via Sky.Build.Helpers.InProcessCompile.
-- The "Build complete" stdout assertion (which the subprocess
-- captured from stdout via 2>&1) becomes the byte-identical
-- CompileOk success check; the failure-path assertion that
-- stdout contains "[E0001]" is preserved by scanning the
-- captured-stdout text routed into CompileErr.errMsg.

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


spec :: Spec
spec = do
    describe "Multi-line `exposing (…)` parses + works end-to-end" $ do

        it "module header with multi-line exposing list compiles + runs" $ do
            let src = unlines
                    [ "module Main exposing"
                    , "    ( main"
                    , "    , double"
                    , "    )"
                    , ""
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "double : Int -> Int"
                    , "double n = n * 2"
                    , ""
                    , "main ="
                    , "    println (String.fromInt (double 21))"
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()

        it "import with multi-line exposing list works" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing"
                    , "    ( println"
                    , "    )"
                    , ""
                    , "main ="
                    , "    println \"hello multi-line\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()

        it "one-export-per-line with leading commas (the canonical sky fmt shape)" $ do
            let src = unlines
                    [ "module Main exposing"
                    , "    ( main"
                    , "    , a"
                    , "    , b"
                    , "    , c"
                    , "    )"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "a : Int"
                    , "a = 1"
                    , ""
                    , "b : Int"
                    , "b = 2"
                    , ""
                    , "c : Int"
                    , "c = 3"
                    , ""
                    , "main = println \"ok\""
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _  -> return ()

    describe "Parse errors are now FATAL (not silently downgraded to warnings)" $ do

        it "an unparseable module fails the build hard, not just a warning" $ do
            -- Genuinely broken syntax: missing closing paren on the
            -- exposing list. Pre-fix this would emit
            -- `Warning: could not parse src/Main.sky: …` and proceed
            -- with 0 modules (then fail in some inscrutable way later).
            -- Post-fix it must abort the build with a non-zero exit.
            let src = unlines
                    [ "module Main exposing (main"     -- missing )
                    , ""
                    , "main = 1"
                    ]
            result <- compileInProcess src
            case result of
                CompileOk _ ->
                    expectationFailure "expected a hard parse failure but compile succeeded"
                CompileErr e ->
                    -- v0.13 Layer 1: parser failures emit a structured
                    -- Diagnostic with the stable code [E0001] (parse-error
                    -- category).  Pre-v0.13 the test looked for the raw
                    -- `PARSE ERROR: <path>: <ctor>` message that surfaced
                    -- the Haskell constructor name to end users.
                    e `shouldSatisfy` ("[E0001]" `isInfixOf`)
