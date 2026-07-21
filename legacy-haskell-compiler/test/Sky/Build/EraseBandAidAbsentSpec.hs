{-# LANGUAGE OverloadedStrings #-}

-- | Sky.Build.EraseBandAidAbsentSpec — permanent forward-regression
-- gate enforcing v0.17 close criterion #2: the legacy
-- @eraseUndeclaredTVarsInGoSource@ Go-source band-aid MUST NOT
-- reappear anywhere in @src/@.
--
-- Background.  Through Phase α / Phase ε the typed-codegen pipeline
-- shipped a textual sweep over emitted Go source that rewrote
-- undeclared type-variable identifiers (e.g. a stray @T1@ leaking
-- out of a TCO scope) back to @any@.  That band-aid masked the
-- real bug class (Reader-style typed-scope threading, closed by
-- PR-17b + Wave 3 SolvedTypes wiring) and is now ARCHITECTURALLY
-- absent — the only correct place to fix a T1 leak is at the
-- emission-time call site, never at a post-pass string sweep.
--
-- This spec is the mechanical regression gate: if a future
-- contributor reintroduces the helper (under any module / under
-- any signature / even as a stubbed @undefined@), the spec trips
-- at @cabal test@ time and the PR cannot land.  The string match
-- is intentionally cheap, immune to import-path renames, and
-- aligned with the existing 'IORefBoundarySpec' gate pattern.
--
-- Pairing — this gate is the forward-regression half of v0.17
-- criterion #2.  The backward-regression half (the legacy helper
-- has actually been deleted) is verified by the gate ITSELF: it
-- fails iff the legacy name appears anywhere in @src/@.  Combined
-- with the live 'Sky.Build.GoTypeAdtSpec' (36 ex) +
-- 'Sky.Build.GoTypeRoundTripSpec' (36 ex) parity gates this spec
-- closes criteria #2 + #5 of the v0.17 close goal as MANDATORY
-- cabal-test items.
module Sky.Build.EraseBandAidAbsentSpec (spec) where

import Control.Monad (filterM, foldM)
import qualified Data.List as List
import System.Directory (doesDirectoryExist, listDirectory)
import System.FilePath ((</>), takeExtension)
import Test.Hspec


-- | Recursively walk @src/@ and return every @.hs@ file path.
--
-- We sweep the whole tree, not just @Compile.hs@, because the band-aid
-- could be reintroduced under any module name — that's the whole
-- point of a forward-regression gate.
walkHaskellSources :: FilePath -> IO [FilePath]
walkHaskellSources root = do
    rootIsDir <- doesDirectoryExist root
    if not rootIsDir
        then pure []
        else do
            entries <- listDirectory root
            foldM step [] entries
  where
    step acc name = do
        let path = root </> name
        isDir <- doesDirectoryExist path
        if isDir
            then do
                more <- walkHaskellSources path
                pure (acc ++ more)
            else if takeExtension path == ".hs"
                then pure (path : acc)
                else pure acc


-- | True iff the legacy band-aid name appears in the given file.
fileMentionsBandAid :: FilePath -> IO Bool
fileMentionsBandAid p = do
    contents <- readFile p
    pure ("eraseUndeclaredTVarsInGoSource" `List.isInfixOf` contents)


spec :: Spec
spec = describe "v0.17 criterion #2 — eraseUndeclaredTVarsInGoSource band-aid absent" $ do

    it "no .hs file under src/ references the legacy band-aid name" $ do
        srcFiles <- walkHaskellSources "src"
        offenders <- filterM fileMentionsBandAid srcFiles
        -- Empty list is the contract.  On failure hspec prints the
        -- list of offending paths so the author knows exactly where
        -- the band-aid sneaked back in.
        offenders `shouldBe` []

    -- Sanity / methodology gate.  If the walk silently returns []
    -- (e.g. because someone moved src/ or broke directory walking)
    -- the offenders check above would pass trivially without
    -- actually verifying anything.  This second example pins the
    -- walk to a non-trivial population so a "0 offenders" verdict
    -- is meaningful.
    it "walks a non-empty set of .hs files under src/" $ do
        srcFiles <- walkHaskellSources "src"
        -- The compiler tree currently has ~70 Haskell modules.  A
        -- lower-bound of 10 is conservative enough to survive a
        -- big-module merge without becoming a flake, while catching
        -- the "walk silently returned []" failure mode.
        length srcFiles >= 10 `shouldBe` True
