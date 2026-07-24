{-# LANGUAGE OverloadedStrings #-}

-- | Sky.Build.ScopeStateRefAuditSpec — discovery + invariant gate
-- enforcing the writer audit of @scopeStateRef@ documented in the
-- contract block above 'scopeStateRef' in "Sky.Build.Compile"
-- (~Compile.hs:496-595).
--
-- Background.  @scopeStateRef@ is the consolidated lowering-scope
-- state IORef holding an @LC.LowerCtx@.  Per the v0.17 release
-- contract (AUTONOMOUS_GOAL.md criterion #3), it has TWO write
-- classes:
--
--   * Class A — bracket-scoped (push/pop) writes implementing
--     `withLambdaTypes` / `withScopedLambdaTypes` /
--     `withLambdaGoStrs` / `withScopedLambdaGoStrs` /
--     `withScopedEnclosingTypeParams(Stmts)` /
--     `withScopedLowerCtx(Stmts)`. Each writer is lexically
--     paired with a restore step.  The audit verifies the
--     write-count matches the documented helper count + dep-body
--     bracket exchange.
--
--   * Class B — monotonic-accumulating pipeline writes
--     (`modifyIORef`/`atomicModifyIORef` / `modifyIORef'`) for
--     `_lc_cgEnv`, `_lc_kernelAlias`, `_lc_unionNames`,
--     `_lc_currentDepModule`, `_lc_solved`. Each writer reads
--     prior state via the modifier and produces a successor.
--     The audit verifies the modify-count matches the documented
--     accumulator-site count.
--
-- Audit policy.  This spec greps @Sky.Build.Compile@ for every
-- IORef-write primitive applied to @scopeStateRef@ and asserts
-- the counts match the documented expectations.  A new writer
-- MUST update both the contract block and this audit; an
-- unaccounted writer is a regression.
--
-- Pattern mirrors 'Sky.Build.AnonRecordWriterAuditSpec' — cheap
-- mechanical sweep, immune to import-path renames, aligned with
-- the existing 'IORefBoundarySpec' gate pattern.
module Sky.Build.ScopeStateRefAuditSpec (spec) where

import qualified Data.List as List
import Test.Hspec


-- | Documented Class A writer count (bracket-scoped, push/pop).
-- See the contract block above 'scopeStateRef' in
-- "Sky.Build.Compile" for the per-site rationale.
--
-- Each `withScopedX` helper writes TWICE (push + restore) per
-- bracket entry.  The `withLambdaTypes` (non-scoped) variant
-- writes once.  Counts the writeIORef + atomicWriteIORef
-- primitives.
--
--   * 'withLambdaTypes'                — 1 push (no restore)
--   * 'withScopedLambdaTypes'          — 2 (push + restore)
--   * 'withScopedLambdaGoStrs'         — 2 (push + restore)
--   * 'withScopedEnclosingTypeParams'  — 2 (push + restore)
--   * 'withScopedEnclosingTypeParamsStmts' — 2 (push + restore)
--   * 'withScopedLowerCtx'             — 2 (push + restore)
--   * 'withScopedLowerCtxStmts'        — 2 (push + restore)
--   * dep-body emission bracket (~6813-6815) — 2 (push + restore)
--   * compile-entry barrier reset (~3178)   — 1
-- + per-fn cgEnv finalisation writes (~7906/~7912 — Class B
--   adjacency)
--
-- Total expected writeIORef-form primitives: 25.
expectedClassAWriterCount :: Int
expectedClassAWriterCount = 25


-- | Documented Class B writer count (monotonic-accumulating
-- pipeline state).
--
-- Each `modifyIORef`-form primitive reads-prior, produces-successor.
-- Counts the modifyIORef + modifyIORef' + atomicModifyIORef +
-- atomicModifyIORef' primitives.
--
--   * cgEnv pipeline writers
--     (~3184, ~4240, ~4347, ~4733, ~4778, ~7486, ~7491)  — 7
--   * kernelAlias writers  (~3842, ~4873)                — 2
--   * unionNames / solved / currentDepModule
--     accumulators                                       — variable
--
-- Total expected modifyIORef-form primitives: 17.
expectedClassBWriterCount :: Int
expectedClassBWriterCount = 17


-- | Compile.hs path.  The audit deliberately scopes to this
-- single file — scopeStateRef is module-internal to
-- Sky.Build.Compile by design, so a write to it from any other
-- module would be a layering violation independent of this audit.
compileHsPath :: FilePath
compileHsPath = "src/Sky/Build/Compile.hs"


-- | Count occurrences of `<prim> scopeStateRef` for a list of
-- primitive names.  Substring match per line because Haskell
-- syntax around IORef writes is uniform.
countWritesIn :: [String] -> String -> Int
countWritesIn prims source =
    let needles = [p ++ " scopeStateRef" | p <- prims]
        match line = any (`List.isInfixOf` line) needles
    in length (filter match (lines source))


spec :: Spec
spec = describe "Sky.Build.ScopeStateRefAuditSpec — v0.17 criterion #3 contract gate" $ do
    it ("Class A (bracket-scoped) writer count = " ++ show expectedClassAWriterCount) $ do
        source <- readFile compileHsPath
        let classAPrims = ["writeIORef", "atomicWriteIORef"]
            count = countWritesIn classAPrims source
        count `shouldBe` expectedClassAWriterCount
    it ("Class B (monotonic-accumulating) writer count = " ++ show expectedClassBWriterCount) $ do
        source <- readFile compileHsPath
        let classBPrims =
                [ "modifyIORef"
                , "modifyIORef'"
                , "atomicModifyIORef"
                , "atomicModifyIORef'"
                ]
            -- modifyIORef matches both `modifyIORef` and the
            -- primed `modifyIORef'`, so deduplicate by counting
            -- only the longest non-overlapping match per line.
            -- For this audit the simple substring count is
            -- conservative: it COULD over-count if a line had
            -- both forms, but `Sky.Build.Compile` does not.
            count = countWritesIn classBPrims source
        count `shouldBe` expectedClassBWriterCount
    it "no scopeStateRef writes outside Sky.Build.Compile (non-comment lines only)" $ do
        -- Layering invariant: scopeStateRef is module-internal.
        -- A write from any other module would be a contract
        -- violation independent of the per-class accounting.
        --
        -- This is a spot-check on a representative leaf module.
        -- Comment-only mentions of the IORef name are ignored
        -- (those are documentation cross-references, not writes).
        source <- readFile "src/Sky/Build/LowerCtx.hs"
        let prims =
                [ "writeIORef scopeStateRef"
                , "atomicWriteIORef scopeStateRef"
                , "modifyIORef scopeStateRef"
                , "modifyIORef' scopeStateRef"
                , "atomicModifyIORef scopeStateRef"
                , "atomicModifyIORef' scopeStateRef"
                ]
            isCommentLine line = case dropWhile (== ' ') line of
                ('-':'-':_) -> True
                _           -> False
            nonCommentLines = filter (not . isCommentLine) (lines source)
            anyMatch = any (\line -> any (`List.isInfixOf` line) prims)
                          nonCommentLines
        anyMatch `shouldBe` False
