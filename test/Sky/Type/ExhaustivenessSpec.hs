module Sky.Type.ExhaustivenessSpec (spec) where

-- Tier 1 (task #491): in-process via compileInProcess. The
-- pre-Tier-1 spec read the fixture from disk (test/fixtures/
-- exhaustiveness/missing.sky); the body is inlined here so the
-- spec stands alone.

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


missingBlueSrc :: String
missingBlueSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "type Colour"
    , "    = Red"
    , "    | Green"
    , "    | Blue"
    , ""
    , ""
    , "describe : Colour -> String"
    , "describe c ="
    , "    case c of"
    , ""
    , "        Red ->"
    , "            \"red\""
    , ""
    , "        Green ->"
    , "            \"green\""
    , ""
    , ""
    , "main ="
    , "    println (describe Red)"
    ]


wildcardCoveredSrc :: String
wildcardCoveredSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "type Colour = Red | Green | Blue"
    , ""
    , "describe : Colour -> String"
    , "describe c ="
    , "    case c of"
    , "        Red -> \"red\""
    , "        _ -> \"other\""
    , ""
    , "main = println (describe Red)"
    ]


spec :: Spec
spec = do
    describe "P3: non-exhaustive case is a build error" $ do
        it "reports the missing constructor (Blue) for a user ADT" $ do
            result <- compileInProcess missingBlueSrc
            case result of
                CompileOk _ -> expectationFailure "expected non-exhaustive case to be rejected"
                CompileErr e ->
                    e `shouldSatisfy` \s ->
                        ("does not cover" `isInfixOf` s) && ("Blue" `isInfixOf` s)

        it "accepts a wildcard-covered case" $ do
            result <- compileInProcess wildcardCoveredSrc
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _ -> return ()
