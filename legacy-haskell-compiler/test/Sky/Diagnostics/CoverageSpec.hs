-- | v0.13 overall guarantee — diagnostic coverage spec.
--
-- One regression test per error category.  Each fixture inlined
-- below represents a canonical instance of an error class.  The
-- spec asserts that:
--
--   1. The Sky CLI reports the matching diagnostic code + region.
--   2. The build fails — the program never reaches the runtime.
--
-- LSP wire-format validation is covered separately by the
-- `Sky.Lsp.DiagnosticsSpec` suite, which exercises the JSON
-- shape end-to-end through a real LSP harness.  The CLI codes
-- + LSP codes share the same `Diagnostic` AST, so any drift in
-- one surface is caught by the other.
--
-- Adding a new error category:
--   * Add a new fixture binding alongside the four below.
--   * Add a new `describe ... it` block here asserting the code.
--   * If the category has a unique severity / category prefix,
--     also assert the header wording.
--
-- Tier 1 (task #491): migrated from subprocess `sky check` to
-- in-process `compileInProcess` via Sky.Build.Helpers.InProcessCompile.
-- Fixture text inlined from test/fixtures/diagnostics/*.sky (those
-- files remain on disk for `sky check` smoke-testing from the
-- CLI; this spec no longer reads them).
module Sky.Diagnostics.CoverageSpec (spec) where

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


-- | Compile a fixture in-process and return its combined output
-- (errMsg for failure path, mainGo for success path).  All
-- diagnostic-coverage cases below expect failure, so they pattern
-- match on `CompileErr`.
runFixture :: String -> IO String
runFixture src = do
    result <- compileInProcess src
    case result of
        CompileOk _   -> return ""   -- caller flips this into a failure via isInfixOf negative
        CompileErr e  -> return e


spec :: Spec
spec = do
    describe "Diagnostic coverage — one fixture per error class" $ do

        it "parse error fixture surfaces [E0001] PARSE ERROR" $ do
            out <- runFixture parseErrorSrc
            out `shouldNotBe` ""
            out `shouldSatisfy` ("[E0001]" `isInfixOf`)
            out `shouldSatisfy` ("PARSE ERROR" `isInfixOf`)

        it "unbound-name fixture surfaces [E1001] NAMING ERROR" $ do
            out <- runFixture unboundNameSrc
            out `shouldNotBe` ""
            out `shouldSatisfy` ("[E1001]" `isInfixOf`)
            out `shouldSatisfy` ("NAMING ERROR" `isInfixOf`)
            -- The diagnostic body should include the offending name.
            out `shouldSatisfy` ("frobnicate" `isInfixOf`)

        it "type-mismatch fixture surfaces [E2001] TYPE ERROR" $ do
            out <- runFixture typeMismatchSrc
            out `shouldNotBe` ""
            out `shouldSatisfy` ("[E2001]" `isInfixOf`)
            out `shouldSatisfy` ("TYPE ERROR" `isInfixOf`)
            out `shouldSatisfy` ("Type mismatch" `isInfixOf`)
            -- Both expected + actual types should appear in the body.
            out `shouldSatisfy` ("expected: Int" `isInfixOf`)
            out `shouldSatisfy` ("actual:   String" `isInfixOf`)

        it "non-exhaustive fixture surfaces [E3001] EXHAUSTIVENESS ERROR" $ do
            out <- runFixture nonExhaustiveSrc
            out `shouldNotBe` ""
            out `shouldSatisfy` ("[E3001]" `isInfixOf`)
            out `shouldSatisfy` ("EXHAUSTIVENESS ERROR" `isInfixOf`)
            -- The missing constructor name should appear.
            out `shouldSatisfy` ("Blue" `isInfixOf`)

    describe "Diagnostic invariants" $ do

        it "every fixture exits non-zero — the runtime never sees the program" $ do
            mapM_ (\src -> do
                result <- compileInProcess src
                case result of
                    CompileErr _ -> return ()
                    CompileOk _ -> expectationFailure
                        "expected compile failure but the fixture compiled cleanly")
                [ parseErrorSrc
                , unboundNameSrc
                , typeMismatchSrc
                , nonExhaustiveSrc
                ]

        it "every fixture's diagnostic carries a stable [Ennnn] code" $ do
            -- Loop-asserts: any code in the documented ranges (E0001-E5999)
            -- counts.  Catches new diagnostics that forget to register
            -- a code via `mkError`.
            results <- mapM runFixture
                [ parseErrorSrc
                , unboundNameSrc
                , typeMismatchSrc
                , nonExhaustiveSrc
                ]
            mapM_ (\out -> out `shouldSatisfy` (\s ->
                any (`isInfixOf` s)
                    [ "[E0", "[E1", "[E2", "[E3", "[E4", "[E5" ])) results


-- ─── Inlined fixtures (mirror test/fixtures/diagnostics/*.sky) ───

parseErrorSrc :: String
parseErrorSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Std.Log exposing (println)"
    , ""
    , "-- Misspelled `type`: should surface as a [E0001] PARSE ERROR."
    , "tyep alias Foo = Int"
    , ""
    , "main = println \"x\""
    ]


unboundNameSrc :: String
unboundNameSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Std.Log exposing (println)"
    , ""
    , "-- `frobnicate` is not defined anywhere: should surface as"
    , "-- a [E1001] NAMING ERROR."
    , "main ="
    , "    println (frobnicate 42)"
    ]


typeMismatchSrc :: String
typeMismatchSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Std.Log exposing (println)"
    , ""
    , "-- Int + String can't unify: should surface as a [E2001] TYPE ERROR."
    , "add : Int -> Int -> Int"
    , "add x y = x + y"
    , ""
    , "main ="
    , "    println (String.fromInt (add \"hello\" 1))"
    ]


nonExhaustiveSrc :: String
nonExhaustiveSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Std.Log exposing (println)"
    , ""
    , "type Color = Red | Green | Blue"
    , ""
    , "-- Blue branch missing: should surface as a [E3001]"
    , "-- EXHAUSTIVENESS ERROR."
    , "describe : Color -> String"
    , "describe c ="
    , "    case c of"
    , "        Red -> \"red\""
    , "        Green -> \"green\""
    , ""
    , "main = println (describe Red)"
    ]
