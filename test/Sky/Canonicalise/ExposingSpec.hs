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


-- #576 — kernel-implicit Prelude types may appear in `exposing (...)` even
-- when the dep module doesn't declare them via `type alias`. `Decoder` is
-- defined as `runtimeOnlyTypes` in Compile.hs; the canonicaliser threads
-- it as kernel-implicit; the .sky source for `Std.Db.Decode` never has
-- `type alias Decoder a = ...` (the kernel is the type's source of
-- truth). Pre-fix this spec's import errored with "module Std.Db.Decode
-- does not expose type Decoder" — a misleading error since Decoder is
-- already globally available.
mainReExportsDecoderSrc :: String
mainReExportsDecoderSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Db.Decode exposing (Decoder, succeed)"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "myDec : Decoder Int"
    , "myDec ="
    , "    succeed 0"
    , ""
    , ""
    , "main ="
    , "    let _ = myDec in"
    , "    println \"ok\""
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

    describe "#576: kernel-implicit Prelude type re-exposure" $ do
        it "accepts `import Std.Db.Decode exposing (Decoder, ...)` (Decoder is kernel-implicit)" $ do
            result <- compileInProcessMulti
                [ ("src/Main.sky", mainReExportsDecoderSrc) ]
            case result of
                CompileOk _ -> pure ()
                CompileErr e -> expectationFailure
                    ( "expected re-exposure of kernel-implicit Decoder to succeed, "
                      ++ "got: " ++ e )
