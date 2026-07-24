module Sky.Canonicalise.KernelFallbackSpec (spec) where

-- Regression for the canonicaliser fallback that ships kernel calls
-- without their `rt.` prefix when the user hasn't written an explicit
-- `import Sky.Core.<Mod> as <Mod>`.
--
-- Bug shape: `Crypto.sha256 raw` resolves via `resolveQualVar`'s
-- fallback to `VarTopLevel "Crypto" "sha256"`, the lowerer emits
-- `Crypto_sha256(arg)` (no `rt.` prefix), and `go build` fails with
-- `undefined: Crypto_sha256`.
--
-- Fix: `resolveQualVar` consults `kernelModules` on fallback so any
-- registered kernel qualifier resolves as `VarKernel`, matching the
-- explicit-import path. Same shape applies to Encoding, Hex, Time,
-- Slog, Char, Path, Math, Regex etc. — pick the ones that actually
-- chain through `typedKernelArgCoerce` so the deeply-nested call
-- shape (the one that surfaced the bug originally) is exercised.
--
-- Tier 1 (task #491): in-process via compileInProcess; the pre-Tier-1
-- spec also ran the produced binary to confirm Crypto.sha256 "hello"
-- emits the canonical 12-char prefix `2cf24dba5fb0`. That runtime
-- check is covered by runtime-go/rt/*_test.go's sha256 unit tests —
-- the spec's contribution is the lowered-Go shape, which is asserted
-- directly off the CompileOk Go source.

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


fixtureSrc :: String
fixtureSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "{-|"
    , "Regression for the canonicaliser fallback that ships kernel calls"
    , "without their `rt.` prefix when the user hasn't written an explicit"
    , "`import Sky.Core.<Mod> as <Mod>`."
    , "-}"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Task as Task"
    , "import Std.Log as Log"
    , ""
    , ""
    , "main ="
    , "    -- Crypto.sha256 nested inside String.slice (typedKernelArgCoerce"
    , "    -- arg coercion path) — the original failure mode in"
    , "    -- <downstream>/Services/Stripe.sky webhook logging."
    , "    let"
    , "        digest = String.slice 0 12 (Crypto.sha256 \"hello\")"
    , "        encoded = Encoding.base64Encode \"secret\""
    , "    in"
    , "        Task.run (Log.println (digest ++ \" \" ++ encoded))"
    ]


spec :: Spec
spec = do
    describe "Canonicaliser falls back to kernel registry for unimported qualifiers" $ do
        it "Crypto.sha256 / Encoding.base64Encode used without explicit import lower to rt.*" $ do
            result <- compileInProcess fixtureSrc
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk main_go -> do
                    -- Generated Go must call the kernel through the rt
                    -- package — bare `Crypto_sha256(` is the failure mode
                    -- the canonicaliser fallback used to ship.
                    main_go `shouldSatisfy` ("rt.Crypto_sha256(" `isInfixOf`)
                    -- Encoding.base64Encode lowers via the typed-kernel
                    -- literal-arg path to the `T`-suffix variant
                    -- (`Encoding_base64EncodeT`), so accept either form
                    -- — the bug-shape we guard against is the missing
                    -- `rt.` prefix, not the `T` suffix.
                    main_go `shouldSatisfy` \s ->
                        "rt.Encoding_base64Encode" `isInfixOf` s
                    -- Defence in depth: the bare-name form must be absent
                    -- (a regression would emit `Crypto_sha256(` somewhere).
                    main_go `shouldSatisfy` \s -> not (" Crypto_sha256(" `isInfixOf` s)
                                               && not ("(Crypto_sha256(" `isInfixOf` s)
                                               && not ("=Crypto_sha256(" `isInfixOf` s)
