{-# LANGUAGE OverloadedStrings #-}

-- | Sky.Build.AnonRecordWriterAuditSpec — discovery + invariant
-- gate enforcing the writer audit of @globalAnonRecords@ documented
-- at "Sky.Build.Compile" above 'generateAnonRecordDecls'.
--
-- Background.  @globalAnonRecords@ is a process-wide IORef
-- (canonical definition in 'Sky.Generate.Go.AnonRecords').  The
-- typed renderer @generateAnonRecordDecls@ snapshots it at
-- emission time to materialise @type Anon_R_…@ struct decls; any
-- write after that read point silently loses shapes, causing
-- @go build@ to fail with "undefined: Anon_R_…".
--
-- Audit policy.  The legitimate writers are documented at the
-- top of @generateAnonRecordDecls@ in "Sky.Build.Compile" (one
-- 'writeIORef' reset in 'resetCompileState', one
-- 'atomicModifyIORef'' registration in 'synthAnonRecordName',
-- and one 'atomicWriteIORef' reset helper 'resetAnonRecords').
-- This spec greps every @.hs@ file under @src/@ for the union of
-- those four IORef-write primitives applied to
-- @globalAnonRecords@, counts them, and asserts the count is
-- EXACTLY the documented number.  A new writer must update the
-- audit AND prove it fires before the emission-time read.
--
-- Pattern mirrors 'Sky.Build.EraseBandAidAbsentSpec' — cheap
-- mechanical sweep over @src/@, immune to import-path renames,
-- aligned with the existing 'IORefBoundarySpec' gate pattern.
module Sky.Build.AnonRecordWriterAuditSpec (spec) where

import Control.Monad (foldM)
import qualified Data.List as List
import System.Directory (doesDirectoryExist, listDirectory)
import System.FilePath ((</>), takeExtension)
import Test.Hspec


-- | Documented writer count.  See the comment block above
-- 'generateAnonRecordDecls' in "Sky.Build.Compile" for the
-- per-site rationale.
--
--   * 'resetCompileState'    @ Compile.hs        — 1 writer
--   * 'synthAnonRecordName'  @ AnonRecords.hs    — 1 writer
--   * 'resetAnonRecords'     @ AnonRecords.hs    — 1 writer
--
-- = 3 total.
expectedWriterCount :: Int
expectedWriterCount = 3


-- | Recursively walk @src/@ and return every @.hs@ file path.
--
-- We sweep the whole tree, not just the canonical owner module,
-- because a future writer could appear under any module name —
-- that's the whole point of an invariant gate.
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


-- | Token-level needles for the four IORef-write primitives
-- applied to @globalAnonRecords@.  We match the LITERAL phrase
-- @\<prim\> globalAnonRecords@ so import-path renames don't
-- silently change the count.
--
-- Both the plain and atomic variants are listed because the
-- live codebase uses 'writeIORef' (Compile.hs reset path) and
-- the atomic combinators 'atomicModifyIORef'' /
-- 'atomicWriteIORef' (AnonRecords.hs paths).  Any future writer
-- under any of these names is counted.
writeNeedles :: [String]
writeNeedles =
    [ "writeIORef globalAnonRecords"
    , "modifyIORef globalAnonRecords"
    , "atomicWriteIORef globalAnonRecords"
    , "atomicModifyIORef' globalAnonRecords"
    , "atomicModifyIORef globalAnonRecords"
    , "modifyIORef' globalAnonRecords"
    ]


-- | True when a line is a Haskell line-comment (after leading
-- whitespace).  Block comments and pragmas are not relevant — the
-- needles don't appear there.  Skipping comment lines is what
-- prevents the audit's own documentation block from counting
-- itself: the comment above 'generateAnonRecordDecls' enumerates
-- the writer call sites as Haddock prose, and we want the gate to
-- track ACTUAL code, not its own annotation.
isCommentLine :: String -> Bool
isCommentLine s = case dropWhile (\c -> c == ' ' || c == '\t') s of
    ('-':'-':_) -> True
    _           -> False


-- | Count how many writer occurrences a single file contains.
-- Each non-comment line is checked against every needle; we sum
-- the per-line matches so multi-write files contribute correctly.
countWritersInFile :: FilePath -> IO Int
countWritersInFile p = do
    contents <- readFile p
    let ls = filter (not . isCommentLine) (lines contents)
        hits = [ () | line <- ls, n <- writeNeedles, n `List.isInfixOf` line ]
    pure (length hits)


-- | Aggregate over every @.hs@ file under @src/@.
totalWriterOccurrences :: IO Int
totalWriterOccurrences = do
    files <- walkHaskellSources "src"
    counts <- mapM countWritersInFile files
    pure (sum counts)


-- | The list of @(path, count)@ pairs for any file that contains
-- at least one writer occurrence.  Used only to produce a
-- helpful diagnostic on failure — the contract is the total
-- count below.
writerSites :: IO [(FilePath, Int)]
writerSites = do
    files <- walkHaskellSources "src"
    pairs <- mapM (\f -> do n <- countWritersInFile f; pure (f, n)) files
    pure [p | p@(_, n) <- pairs, n > 0]


spec :: Spec
spec = describe "v0.17 IORef audit — globalAnonRecords writers" $ do

    it "writer count matches the documented audit" $ do
        total <- totalWriterOccurrences
        sites <- writerSites
        let msg =
                "Expected " ++ show expectedWriterCount ++ " writer(s); " ++
                "found " ++ show total ++ ".  Writer sites:\n  " ++
                List.intercalate "\n  "
                    [ p ++ "  (×" ++ show n ++ ")" | (p, n) <- sites ] ++
                "\nIf you added a writer, update the audit comment " ++
                "above generateAnonRecordDecls in Compile.hs AND " ++
                "bump expectedWriterCount in this spec — and prove " ++
                "the new write fires BEFORE the readIORef in " ++
                "generateAnonRecordDecls.  If you removed a writer, " ++
                "drop it from the audit comment and bump down."
        if total == expectedWriterCount
            then total `shouldBe` expectedWriterCount
            else expectationFailure msg

    -- Sanity / methodology gate.  Mirrors the 'walks a non-empty
    -- set' guard in 'EraseBandAidAbsentSpec' — if the walk silently
    -- returns [] (e.g. someone moved @src/@ or broke directory
    -- walking) the count check above would pass trivially at 0.
    -- This second example pins the walk to a non-trivial
    -- population so a "expected count" verdict is meaningful.
    it "walks a non-empty set of .hs files under src/" $ do
        srcFiles <- walkHaskellSources "src"
        -- The compiler tree currently has ~70 Haskell modules.  A
        -- lower-bound of 10 is conservative enough to survive a
        -- big-module merge without becoming a flake, while
        -- catching the "walk silently returned []" failure mode.
        length srcFiles >= 10 `shouldBe` True
