module Sky.Build.DictSourceSpec (spec) where

-- v0.15.45 — Layer 3 contract closure for Dict / Set + typed-key
-- Dict.toList soundness fix.
--
-- This spec pins:
--
--   1. Sky.Core.Dict and Sky.Core.Set are now Sky-source modules
--      (sky-stdlib/Sky/Core/Dict.sky + Set.sky exist) — backs the
--      `sky doc Sky.Core.Dict.get` discoverability story.
--
--   2. Every binding declared in those modules is wired as an
--      `Ffi.kernel "Dict_<name>"` alias (per the v0.13 Layer 3
--      convention) — so the call sites route to the existing typed
--      kernel dispatch unchanged.
--
--   3. `Dict.fromList [(1, "a")] |> Dict.toList` emits typed-key
--      routing (`rt.Dict_toListIntKey`) when the Sky type is
--      `Dict Int v`, NOT the legacy `rt.Dict_toList` String-key
--      path — closes Limitation #10's soundness hole.
--
-- Tier 1 (task #491): the two compile-and-inspect `it` blocks use
-- Sky.Build.Helpers.InProcessCompile instead of subprocess `sky
-- build`.  ZERO subprocess.  ZERO `go build`.  ZERO GOCACHE.
-- The stdlib-file-existence cases stay file-only.

import Test.Hspec
import qualified Data.ByteString as BS
import qualified Data.ByteString.Char8 as BS8
import Data.List (isInfixOf)
import System.Directory (doesFileExist)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


spec :: Spec
spec = describe "v0.15.45 — Dict + Set Layer 3 + typed-key routing" $ do

    it "Sky.Core.Dict.sky exists in sky-stdlib/" $ do
        exists <- doesFileExist "sky-stdlib/Sky/Core/Dict.sky"
        exists `shouldBe` True

    it "Sky.Core.Set.sky exists in sky-stdlib/" $ do
        exists <- doesFileExist "sky-stdlib/Sky/Core/Set.sky"
        exists `shouldBe` True

    it "Dict.sky declares each entry via Ffi.kernel" $ do
        body <- BS8.unpack <$> BS.readFile "sky-stdlib/Sky/Core/Dict.sky"
        -- Each documented binding routes through Ffi.kernel.
        let mustHave =
                [ "Ffi.kernel \"Dict_empty\""
                , "Ffi.kernel \"Dict_get\""
                , "Ffi.kernel \"Dict_insert\""
                , "Ffi.kernel \"Dict_toList\""
                , "Ffi.kernel \"Dict_fromList\""
                , "Ffi.kernel \"Dict_map\""
                ]
        mapM_ (\needle ->
            (needle `isInfixOf` body) `shouldBe` True) mustHave

    it "Set.sky declares each entry via Ffi.kernel" $ do
        body <- BS8.unpack <$> BS.readFile "sky-stdlib/Sky/Core/Set.sky"
        let mustHave =
                [ "Ffi.kernel \"Set_empty\""
                , "Ffi.kernel \"Set_insert\""
                , "Ffi.kernel \"Set_member\""
                , "Ffi.kernel \"Set_union\""
                , "Ffi.kernel \"Set_intersect\""
                , "Ffi.kernel \"Set_diff\""
                ]
        mapM_ (\needle ->
            (needle `isInfixOf` body) `shouldBe` True) mustHave

    it "Dict.toList on Dict Int v emits rt.Dict_toListIntKey" $ do
        result <- compileInProcess intKeyFixture
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk body -> do
                -- Typed routing fires for the inlined Int-key
                -- Dict.fromList → Dict.toList chain.
                ("rt.Dict_toListIntKey" `isInfixOf` body) `shouldBe` True
                -- Legacy String-key Dict.toList should still
                -- emit the unchanged rt.Dict_toList route — the
                -- typed-key routing is additive, not a wholesale
                -- replacement.

    it "Dict.toList on Dict String v keeps the legacy route" $ do
        result <- compileInProcess stringKeyFixture
        case result of
            CompileErr e -> expectationFailure ("compile failed: " ++ e)
            CompileOk body -> do
                -- Legacy path — no Int-key typed route emitted.
                ("rt.Dict_toListIntKey" `isInfixOf` body) `shouldBe` False
                ("rt.Dict_toListFloatKey" `isInfixOf` body) `shouldBe` False


-- ── Fixtures ──────────────────────────────────────────────────────


intKeyFixture :: String
intKeyFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Dict as Dict"
    , "import Sky.Core.List as List"
    , "import Std.Log exposing (println)"
    , ""
    , "sumIntKeys : Int"
    , "sumIntKeys ="
    , "    List.foldl (\\( k, _ ) acc -> acc + k) 0"
    , "        (Dict.toList (Dict.fromList [ ( 1, \"a\" ), ( 2, \"b\" ) ]))"
    , ""
    , "main : Int"
    , "main ="
    , "    let _ = println (\"sum:\" ++ String.fromInt sumIntKeys)"
    , "    in 0"
    ]


stringKeyFixture :: String
stringKeyFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Dict as Dict"
    , "import Sky.Core.List as List"
    , "import Std.Log exposing (println)"
    , ""
    , "countStringKeys : Int"
    , "countStringKeys ="
    , "    List.length (Dict.toList (Dict.fromList [ ( \"a\", 1 ), ( \"b\", 2 ) ]))"
    , ""
    , "main : Int"
    , "main ="
    , "    let _ = println (\"count:\" ++ String.fromInt countStringKeys)"
    , "    in 0"
    ]
