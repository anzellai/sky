{-# LANGUAGE OverloadedStrings #-}
module Sky.Build.PolyKernelCrossTVarRenameSpec (spec) where

import Test.Hspec
import qualified Data.List as List

import Sky.Build.Helpers.InProcessCompile (CompileResult(..),
                                           compileInProcess)


-- | v0.17 iter 27 — regression gate for the cross-fn TVar-name
-- collision class in @coerceCallArgsAt@'s FALLBACK arm (Compile.hs:
-- ~16395+).
--
-- THE BUG (HIGH-confidence locator trace, runtime-reproduced on
-- examples/19-skyforum pre-fix):
--
-- Sky kernel signatures share Go-typevar names (@T1@, @T2@, …)
-- across helper functions.  When a helper at @[T1, T2]@ scope
-- calls a sibling helper at @[T1]@ scope WITHOUT a captured CSI
-- (Solve.CallInstance), the fallback path consults the callee's
-- mangled paramTypes verbatim.  σ-recovery may yield empty
-- (acc's GoType isn't statically derivable; @[]@ lowers to
-- @[]any@), substituteOnly's enclosing-tvar scope check then
-- reports the bare @T1@ as IN scope — the CALLER has its own T1.
-- The result: @rt.AsListT[T1](acc)@ is emitted; at runtime T1 is
-- the caller's @a@ TVar (e.g. @int@), acc holds elements typed
-- for the callee's domain — panic via type-assertion mismatch.
--
-- THE FIX: α-rename the callee's TVars to a high-numbered space
-- (@T9001@, @T9002@, …) BEFORE σ-recovery + substituteOnly run.
-- Post-rename, the scope check returns FALSE for every unbound
-- callee TVar (the caller doesn't have @T9NNN@ in scope), so
-- @eraseScopedCtx@ fires and the cast widens to @rt.AsListT[any]@.
-- The outer caller-T-pinned narrowing (e.g. @rt.AsListT[T2]@)
-- then handles per-element coercion via the runtime helper.
--
-- This fixture exercises the bug shape DIRECTLY: a user-defined
-- helper sharing @indexedMapHelp@'s shape (tail-recursive worker
-- with [T2] accumulator that calls @reverseHelp@ at the base case).
-- The stdlib's @Sky_Core_List_indexedMapHelp@ is also affected and
-- emits the bug class in EVERY Sky.Live app via @indexedMap@.
spec :: Spec
spec = describe "Cross-fn TVar collision α-rename (iter 27)" $ do
    it "indexedMap call site widens reverseHelp acc cast to any (not T1)" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.List as List"
                , "import Std.Log exposing (println)"
                , ""
                , "indexedItems : List String"
                , "indexedItems ="
                , "    List.indexedMap (\\i s -> String.fromInt i ++ \" \" ++ s) [ \"a\", \"b\", \"c\" ]"
                , ""
                , "main ="
                , "    println (String.join \", \" indexedItems)"
                ]
        result <- compileInProcess src
        case result of
            CompileErr e -> expectationFailure $ "compile failed:\n" ++ e
            CompileOk go ->
                -- Scope the assertion to `indexedMapHelp`'s body only
                -- (`reverseHelp` ITSELF emits `rt.AsListT[T1](acc)` in
                -- its OWN body's `[]` arm — that T1 is reverseHelp's
                -- own TVar IN-SCOPE so the cast is correct;
                -- `enclosingTypeParamInScopeCtx` returns True for the
                -- self-recursive case).  The bug class is about
                -- DIFFERENT functions sharing the T1 name.
                --
                -- Bound the slice from `indexedMapHelp[`'s `func` line
                -- up to the next `func ` boundary so we look ONLY at
                -- indexedMapHelp's body.  Pre-fix that body called
                -- `reverseHelp` with `rt.AsListT[T1](acc)` where T1
                -- was indexedMapHelp's OWN first TVar in scope —
                -- runtime T1 = int (caller's `a`); acc holds
                -- elements typed for the callee's domain → panic.
                let bodyAfter =
                        drop 1
                          (dropWhile
                            (not . List.isInfixOf "func Sky_Core_List_indexedMapHelp")
                            (lines go))
                    indexedMapHelpBody = unlines
                        (takeWhile
                          (not . List.isPrefixOf "func ")
                          bodyAfter)
                    bad      = "rt.AsListT[T1](acc)"
                    goodTAny = "rt.AsListT[any](acc)"
                    goodAny  = "rt.AsListAny(acc)"
                in do
                    (bad `List.isInfixOf` indexedMapHelpBody) `shouldBe` False
                    ( (goodTAny `List.isInfixOf` indexedMapHelpBody)
                      || (goodAny  `List.isInfixOf` indexedMapHelpBody) )
                        `shouldBe` True

    it "no T9NNN α-renamed token leaks into emitted Go" $ do
        -- The α-rename moves callee TVars into a high-numbered
        -- private space (T9001, T9002, …) inside the FALLBACK
        -- arm's σ-recovery + substitute machinery.  Every rename
        -- target MUST either be pinned by σ to a concrete Go type
        -- OR erased to `any` by `eraseScopedCtx`.  A leaked
        -- `T9NNN` token in emitted Go is an emission-level bug
        -- (Go's compiler would reject with `undefined: T9NNN`).
        let src = unlines
                [ "module Main exposing (main)"
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.List as List"
                , "import Std.Log exposing (println)"
                , ""
                , "main ="
                , "    println"
                , "        (String.join \", \""
                , "            (List.indexedMap"
                , "                (\\i s -> String.fromInt i ++ \" \" ++ s)"
                , "                [ \"a\", \"b\", \"c\" ]))"
                ]
        result <- compileInProcess src
        case result of
            CompileErr e -> expectationFailure $ "compile failed:\n" ++ e
            CompileOk go ->
                -- Spot-check every T9NNN in 9001..9020 — covers any
                -- mangled paramTypes with up to 20 distinct TVars
                -- per callee (real kernel sigs use 2-3).
                let leak = any
                        (\n -> ("T" ++ show n) `List.isInfixOf` go)
                        ([9001..9020] :: [Int])
                in leak `shouldBe` False
