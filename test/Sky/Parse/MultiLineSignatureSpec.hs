module Sky.Parse.MultiLineSignatureSpec (spec) where

import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


-- Regression: prior to the 2026-05-23 parser fix, a top-level
-- declaration whose `:` lived on a continuation line failed to
-- parse with a "Top-level declaration expected" error. The user
-- pattern that hit this was a wide signature where the type
-- spilled onto its own line for readability:
--
--   greeting
--       : String -> String
--   greeting name = "hello, " ++ name
--
-- Fix landed in `src/Sky/Parse/Declaration.hs` — the value /
-- annotation branch now tries three alternatives via `oneOf`:
-- same-line `:`, continuation-line `:` (freshLine + checkIndent
-- + `:`), then value def. Multi-line `->` continuation INSIDE
-- the type body is still not supported (workaround: extract a
-- type alias).
--
-- Tier 1 (task #491): migrated from subprocess `sky build` to
-- in-process `Compile.compile` via Sky.Build.Helpers.InProcessCompile.
spec :: Spec
spec = describe "parser accepts multi-line declaration signatures" $ do
    it "compiles `name\\n    : T` (lower-cased declaration)" $ do
        result <- compileInProcess lowerFixture
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk _  -> return ()

    it "compiles `Name\\n    : T` (upper-cased declaration)" $ do
        result <- compileInProcess upperFixture
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk _  -> return ()

    it "compiles an inline record in a same-line signature" $ do
        result <- compileInProcess inlineRecordFixture
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk _  -> return ()


lowerFixture :: String
lowerFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "greeting"
    , "    : String -> String"
    , "greeting name ="
    , "    \"hello, \" ++ name"
    , ""
    , "main ="
    , "    let _ = println (greeting \"world\")"
    , "    in ()"
    ]


upperFixture :: String
upperFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "type alias Profile = { name : String, age : Int }"
    , ""
    , "Profile"
    , "    : String -> Int -> Profile"
    , "Profile n a = { name = n, age = a }"
    , ""
    , "main ="
    , "    let _ = println (Profile \"Alice\" 30).name"
    , "    in ()"
    ]


inlineRecordFixture :: String
inlineRecordFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.String as String"
    , "import Std.Log exposing (println)"
    , ""
    , "processReq : Int -> { name : String, age : Int } -> String"
    , "processReq i r = String.fromInt i ++ \" \" ++ r.name"
    , ""
    , "main ="
    , "    let _ = println (processReq 42 { name = \"Alice\", age = 30 })"
    , "    in ()"
    ]
