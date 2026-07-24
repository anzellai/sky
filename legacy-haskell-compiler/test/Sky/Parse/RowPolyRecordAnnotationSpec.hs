module Sky.Parse.RowPolyRecordAnnotationSpec (spec) where

import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


-- Cycle 4 D6 regression: a row-polymorphic record annotation
--     greet : { r | name : String } -> String
-- previously failed to parse — the AST slot
-- `Src.TRecord [...] (Maybe String)`, canonicaliser,
-- `Instantiate.typeToVariable T.TRecord`, AND `Unify.unifyRecords`
-- all already supported row polymorphism, but the SURFACE PARSER
-- in `Sky.Parse.Type` had no `{ r | ... }` rule.
--
-- The fix adds a non-consuming lookahead (`peekRowPolyIntro`)
-- followed by a row-poly branch that emits
-- `Src.TRecord fields (Just rowVar)`.  This spec exercises BOTH:
--   1. the row-poly annotation parses cleanly, AND
--   2. the open-record semantics flow through HM correctly — a
--      caller passing a record with EXTRA fields beyond `name`
--      type-checks (closed-record would reject it).
--
-- Tier 1 (task #491): migrated from subprocess `sky build` to
-- in-process `Compile.compile` via Sky.Build.Helpers.InProcessCompile.
spec :: Spec
spec = describe "parser accepts row-polymorphic record annotations" $ do
    it "type-checks `{ r | name : String }` and accepts extra fields" $ do
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
    , "-- D6: row-polymorphic annotation accepts records carrying extras"
    , "greet : { r | name : String } -> String"
    , "greet rec ="
    , "    \"Hello, \" ++ rec.name"
    , ""
    , "main ="
    , "    println (greet { name = \"World\", age = 99 })"
    ]
