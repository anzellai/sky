{-# LANGUAGE OverloadedStrings #-}

-- | Sky.Build.IORefBoundarySpec — invariant gate enforcing the
-- v0.15.5 (PR 2/6) consolidation of per-scope IORefs.
--
-- Before this PR, the lowerer carried two NOINLINE per-scope
-- IORefs (the lambda-type map + lambda-Go-string map).  Both held
-- disjoint maps that were ALWAYS pushed/popped together at the
-- same scope seams — a strong tell that they belonged in a single
-- ctx-shaped value.  PR 2 retired both IORefs in favour of a
-- single `scopeStateRef :: IORef LC.LowerCtx`, routing every read
-- and write through `Sky.Build.LowerCtx` helpers.
--
-- This spec is the mechanical regression gate: a literal string
-- match against `src/Sky/Build/Compile.hs` for the two retired
-- IORef names.  If a future change reintroduces them (the rolled-
-- back v0.13/v0.15 pair), this spec trips and the migration is
-- caught at `cabal test` time rather than via a subtle behaviour
-- regression.
--
-- The string-match approach is intentionally cheap and immune to
-- compiler-internal renames; the OLD names are stable historical
-- artefacts and the gate is "they don't come back".
module Sky.Build.IORefBoundarySpec (spec) where

import qualified Data.List as List
import Test.Hspec


spec :: Spec
spec = do
    describe "Compile.hs lowering-scope IORef boundary" $ do
        it "no longer references the retired globalLambdaTypes IORef" $ do
            src <- readFile "src/Sky/Build/Compile.hs"
            -- The retired name MUST NOT appear anywhere in Compile.hs —
            -- not as a binding, a `readIORef` argument, or even a
            -- back-reference in a comment.  The presence of the
            -- literal string anywhere is a regression flag.
            ("globalLambdaTypes" `List.isInfixOf` src) `shouldBe` False
        it "no longer references the retired globalLambdaGoStrings IORef" $ do
            src <- readFile "src/Sky/Build/Compile.hs"
            ("globalLambdaGoStrings" `List.isInfixOf` src) `shouldBe` False
        it "no longer references the retired globalRegionTypes IORef" $ do
            src <- readFile "src/Sky/Build/Compile.hs"
            -- v0.15.5 PR 3 — retired in favour of `scopeStateRef`'s
            -- `_lc_regionTypes` field.  Same gate-shape as the PR 2
            -- pair above: any reintroduction (even a back-reference
            -- in a comment) trips this spec.
            ("globalRegionTypes" `List.isInfixOf` src) `shouldBe` False
