module Sky.Canonicalise.UnboundSpec (spec) where

-- Tier 1 (task #491): in-process via compileInProcess. The
-- pre-Tier-1 spec copied the fixtures from test/fixtures/unbound
-- and test/fixtures/unbound-paren; the bodies are inlined here so
-- the spec stands alone.

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


unboundTypoSrc :: String
unboundTypoSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "main ="
    , "    println messgae"
    ]


unboundParenSrc :: String
unboundParenSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "-- Issue #52: unbound names inside parens (`(loadExample i)`) used"
    , "-- to slip past the canonicaliser because the Src.Paren case fell"
    , "-- through `_ -> []` in collectUnqualExprRegions. The user would then"
    , "-- see a Go-side `undefined: loadExample` error."
    , "main ="
    , "    println <|"
    , "        String.fromInt <|"
    , "            Maybe.withDefault 0 <|"
    , "                List.head (List.drop (loadExample i) [ 1, 2, 3, 4 ])"
    ]


spec :: Spec
spec = do
    describe "Canonicaliser rejects undefined names at the Sky layer" $ do
        it "rejects a typo (`messgae`) with a user-facing Sky error, not a Go error" $ do
            result <- compileInProcess unboundTypoSrc
            case result of
                CompileOk _ -> expectationFailure "expected unbound name to be rejected"
                CompileErr combined -> do
                    -- The user-facing signal: the typo is named + positioned.
                    combined `shouldSatisfy` \s -> "Undefined name" `isInfixOf` s
                    combined `shouldContain` "messgae"
                    -- And the error surface is Sky's, NOT Go's fallback message.
                    combined `shouldSatisfy` \s ->
                        not ("compiler-side bug" `isInfixOf` s)

        it "rejects unbound names inside parens (issue #52 regression)" $ do
            -- Pre-fix, the Src.Paren wrap on `(loadExample i)` caused
            -- collectUnqualExprRegions to fall through `_ -> []` and
            -- silently skip both `loadExample` and `i`. The user saw a
            -- Go-side `undefined: loadExample` error instead of a Sky
            -- diagnostic. Post-fix, Sky reports it at the canonicalise
            -- stage with line:col.
            result <- compileInProcess unboundParenSrc
            case result of
                CompileOk _ -> expectationFailure "expected unbound name in paren to be rejected"
                CompileErr combined -> do
                    combined `shouldSatisfy` \s -> "Undefined name" `isInfixOf` s
                    combined `shouldContain` "loadExample"
                    -- Must NOT be a Go-side error.
                    combined `shouldSatisfy` \s ->
                        not ("rt.List_dropT" `isInfixOf` s)
                    combined `shouldSatisfy` \s ->
                        not ("undefined: loadExample" `isInfixOf` s)
