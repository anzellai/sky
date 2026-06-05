module Sky.Canonicalise.ExposingSpec (spec) where

-- Tier 1 (task #491): in-process via compileInProcessMulti. The
-- pre-Tier-1 spec copied test/fixtures/hiding/ into a tempdir;
-- the two-module fixture is inlined here so the spec stands alone.

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcessMulti)


-- A `Lib.Hidden` that exposes `visible` but NOT `secret`.
libHiddenSrc :: String
libHiddenSrc = unlines
    [ "module Lib.Hidden exposing (visible)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , ""
    , ""
    , "visible : Int"
    , "visible ="
    , "    1"
    , ""
    , ""
    , "secret : Int"
    , "secret ="
    , "    99"
    ]


-- A Main that ALSO imports `secret` — should be rejected.
mainImportsSecretSrc :: String
mainImportsSecretSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Lib.Hidden exposing (visible, secret)"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "main ="
    , "    println (String.fromInt (visible + secret))"
    ]


spec :: Spec
spec = do
    describe "P2: importing an unexposed name is a canonicalise error" $ do
        it "rejects `import Lib.Hidden exposing (secret)` when secret is package-private" $ do
            result <- compileInProcessMulti
                [ ("src/Main.sky",       mainImportsSecretSrc)
                , ("src/Lib/Hidden.sky", libHiddenSrc)
                ]
            case result of
                CompileOk _ -> expectationFailure "expected unexposed `secret` import to be rejected"
                CompileErr e ->
                    e `shouldSatisfy` \s ->
                        ("does not expose" `isInfixOf` s) &&
                        ("secret" `isInfixOf` s)
