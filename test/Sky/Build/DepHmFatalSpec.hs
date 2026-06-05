module Sky.Build.DepHmFatalSpec (spec) where

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcessMulti)


-- Regression: dep module HM errors are fatal (v0.10.0+).  Pre-fix
-- the dep silently degraded to `any`-typed bindings and the entry
-- consumed broken values at runtime.  Symptom in a downstream app:
-- `[AUTH] Admin ensured: 0x102…` — the func-pointer of an unforced
-- Task thunk being string-split.
--
-- Pinned in v0.13 Layer 1 against the structured Diagnostic
-- renderer: output shape includes the stable `[E2001]` code, the
-- `TYPE ERROR` header, and the failing dep's Sky source path
-- (`Lib/Config.sky`).
--
-- Tier 1 (task #491): migrated from subprocess `sky build` of an
-- on-disk fixture to in-process `compileInProcessMulti` with the
-- fixture sources inlined.  Sources are byte-identical to
-- `test/fixtures/dep-hm-fatal/src/{Main,Lib/Config}.sky`.
spec :: Spec
spec = do
    describe "Dep module HM errors are fatal (v0.10.0+)" $ do
        it "blocks the build with TYPE ERROR (Mod): … when a dep module fails HM in pass 2" $ do
            result <- compileInProcessMulti
                [ ("src/Main.sky",        mainSrc)
                , ("src/Lib/Config.sky",  libConfigSrc)
                ]
            case result of
                CompileOk _ ->
                    expectationFailure "expected dep HM failure but compile succeeded"
                CompileErr combined -> do
                    -- v0.13 Layer 1: dep-module type errors now flow
                    -- through the structured Diagnostic renderer.  Output
                    -- shape: `-- TYPE ERROR ── src/Lib/Config.sky:N:M [E2001]`
                    -- (where the source path comes from moduleOrder) plus
                    -- the Type mismatch body.  We pin the [E2001] code
                    -- and the Sky source path as the stable markers.
                    combined `shouldSatisfy` ("TYPE ERROR" `isInfixOf`)
                    combined `shouldSatisfy` ("[E2001]" `isInfixOf`)
                    combined `shouldSatisfy` ("Lib/Config.sky" `isInfixOf`)
                    combined `shouldSatisfy` \s ->
                        "Task Error String" `isInfixOf` s
                        || "Type mismatch" `isInfixOf` s


mainSrc :: String
mainSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.System as System"
    , "import Lib.Config as Config"
    , ""
    , ""
    , "main ="
    , "    let _ = Config.admins ()"
    , "    in System.exit 0"
    ]


libConfigSrc :: String
libConfigSrc = unlines
    [ "module Lib.Config exposing (admins)"
    , ""
    , "{-|"
    , "Regression for \"dep HM errors silently degrade to any\" (v0.10.0)."
    , ""
    , "Pre-fix: this body type-errored under HM (System.getenv returns"
    , "Task Error String, not String — so `|> String.split` was wrong),"
    , "but the dep was tolerated and `admins` was inferred as `any`-typed."
    , "The entry module then silently consumed the broken value."
    , ""
    , "Post-fix: pass-2 dep HM error is fatal. `sky build` aborts with"
    , "`TYPE ERROR (Lib.Config): …`."
    , "-}"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.String as String"
    , "import Sky.Core.System as System"
    , ""
    , ""
    , "admins : () -> List String"
    , "admins _ ="
    , "    System.getenv \"ADMINS\""
    , "        |> String.split \",\""
    ]
