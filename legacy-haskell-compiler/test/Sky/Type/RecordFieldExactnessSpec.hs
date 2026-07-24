module Sky.Type.RecordFieldExactnessSpec (spec) where

-- Regression fence for "closed records reject extra/missing fields".
--
-- Pre-fix bug: `unifyRecords` in src/Sky/Type/Unify.hs (lines 168-172
-- pre-fix) hit a fallback "create fresh extension and merge" branch
-- whenever the field sets differed — even when both sides were
-- closed records (no row-extension variable). This silently accepted
-- record literals with completely wrong field names against an
-- explicit record-typed annotation:
--
--     takesRecord : { name : String, count : Int } -> String
--     takesRecord { id = 1, label = "x" }            -- WAS accepted
--
-- The runtime then panicked with cryptic
-- `rt.AsInt: expected numeric value, got <nil>` when codegen tried
-- to read the missing field. Surfaced from a real-world Std.Ui
-- port: `Border.shadow { offset = 1, size = 2, blur = 4, color = ... }`
-- (wrong field names — actual is `{offsetX, offsetY, blur, spread,
-- color}`) passed sky check + sky build, then panicked at runtime.
--
-- Fix: respect each record's closed/open status. When EITHER side
-- is closed (extension bound to `EmptyRecord1`), the opposite side's
-- extra fields are illegal — fail unification. Open records (still
-- a FlexVar extension) keep the row-poly merge.
--
-- Tier 1 (task #491): in-process via compileInProcess(Multi).

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile
    ( CompileResult(..)
    , compileInProcess
    , compileInProcessMulti
    )


spec :: Spec
spec = do
    describe "Closed records reject mismatched field sets" $ do

        it "in-module: wrong-field-name record literal fails type-check" $ do
            -- Pre-fix: passes silently. Post-fix: rejected.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "takesRecord : { name : String, count : Int } -> String"
                    , "takesRecord r = r.name"
                    , ""
                    , "test : String"
                    , "test = takesRecord { id = 1, label = \"foo\" }"
                    , ""
                    , "main = println test"
                    ]
            result <- compileInProcess src
            case result of
                CompileOk _ -> expectationFailure "expected wrong-field-name literal to be rejected"
                CompileErr e ->
                    e `shouldSatisfy` ("Type mismatch" `isInfixOf`)

        it "cross-module: wrong-field-name record literal fails type-check" $ do
            -- Same shape but the function lives in a dep module.
            -- Pre-fix this passed even more silently because the
            -- externals path also dropped detail. Post-fix: rejected.
            let lib = unlines
                    [ "module Lib exposing (..)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , ""
                    , "takesRecord : { name : String, count : Int } -> String"
                    , "takesRecord r = r.name"
                    ]
                main_ = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Lib"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "test : String"
                    , "test = Lib.takesRecord { id = 1, label = \"foo\" }"
                    , ""
                    , "main = println test"
                    ]
            result <- compileInProcessMulti
                [ ("src/Lib.sky", lib)
                , ("src/Main.sky", main_)
                ]
            case result of
                CompileOk _ -> expectationFailure "expected cross-module wrong-field literal to be rejected"
                CompileErr e ->
                    e `shouldSatisfy` ("Type mismatch" `isInfixOf`)

        it "correct-shape record literal still passes" $ do
            -- Sanity: closed-record exactness only rejects when fields
            -- actually disagree. Right shape must still type-check.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "takesRecord : { name : String, count : Int } -> String"
                    , "takesRecord r = r.name"
                    , ""
                    , "test : String"
                    , "test = takesRecord { name = \"alice\", count = 7 }"
                    , ""
                    , "main = println test"
                    ]
            result <- compileInProcess src
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _ -> return ()


    describe "Cross-module externals register all top-level names (not only functions)" $ do

        it "applying a non-function value as if it were a function fails" $ do
            -- Pre-fix: `Ui.fill : Length` (bare value) was filtered out
            -- of the cross-module externals because `isFunctionType`
            -- only kept TLambda. Call sites then fell through to
            -- CLocal (treated as polymorphic) and `Ui.fill 1`
            -- type-checked silently. Post-fix: the externals filter
            -- is dropped, so the constraint correctly fails with
            -- "Length vs a -> b".
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Ui as Ui"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "test : any"
                    , "test = Ui.width (Ui.fill 1)"
                    , ""
                    , "main = let _ = test in println \"ok\""
                    ]
            result <- compileInProcess src
            case result of
                CompileOk _ -> expectationFailure "expected `Ui.fill 1` to be rejected"
                CompileErr e -> do
                    e `shouldSatisfy` ("Type" `isInfixOf`)
                    -- The error names the offender so users know what to fix.
                    e `shouldSatisfy` (\s -> "Std.Ui.fill" `isInfixOf` s
                                          || "fill" `isInfixOf` s)
