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

    describe "Compile.hs LowerCtx-integration positive surface" $ do
        -- v0.15.5 PR 3 (iteration 3) — symmetric to the retired-IORef
        -- gate above: assert that the explicit LowerCtx integration is
        -- still wired in.  Catches the inverse regression — someone
        -- deletes the `LC.lookupRegionType` /
        -- `LC.withLambdaTypes` reads in a refactor and the scope
        -- state silently regresses to the IORef-only era.
        --
        -- These literal-string checks pin the CURRENT integration
        -- shape (v0.15.5 head).  If a future refactor renames
        -- `LC.lookupRegionType` / `LC.withLambdaTypes`, this spec
        -- needs an update too — that's intentional, because such a
        -- rename is exactly the kind of structural change worth a
        -- second look during code review.
        it "uses LC.lookupRegionType (pure region lookup)" $ do
            src <- readFile "src/Sky/Build/Compile.hs"
            ("LC.lookupRegionType" `List.isInfixOf` src) `shouldBe` True
        it "uses LC.withLambdaTypes (scoped lambda-type extension)" $ do
            src <- readFile "src/Sky/Build/Compile.hs"
            ("LC.withLambdaTypes" `List.isInfixOf` src) `shouldBe` True
        it "letBindingType accepts an explicit LC.LowerCtx parameter" $ do
            -- v0.15.5 PR 3 (iteration 3) — POC for the v0.15.6
            -- cascade.  This pins the signature so a future refactor
            -- that drops the ctx parameter (which would force the
            -- region lookup back through the IORef-backed
            -- `lookupRegionType`) fails the gate.
            src <- readFile "src/Sky/Build/Compile.hs"
            ("letBindingType :: LC.LowerCtx" `List.isInfixOf` src) `shouldBe` True
