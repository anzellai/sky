{-# LANGUAGE OverloadedStrings #-}

-- | Sky.Build.LowerPhaseGoSigMapDeletedSpec — invariant gate for
-- v0.17 PR-α step-5 (task #654): the 'globalGoSigMap' IORef has
-- been DELETED.  The map now flows as a pure value via
-- 'SolveOutputs._so_goSigMap' (built in 'solvePhase') →
-- 'generateGoMulti's 'goSigMapFromSolve' parameter → top-level
-- pure 'finalGoSigMap' let binding (which applies the C10
-- typeParams refresh purely after 'importsForced' sequences the
-- final cgEnv write) → consumed by 'specDecls'.
--
-- The previous architecture bridged the C9 → C10 → spec-decl flow
-- through a NOINLINE CAF IORef ('globalGoSigMap').  Re-introducing
-- the IORef defeats the v0.17 IORef-defusing programme and risks
-- the same cross-compile leak class that 'globalCgEnv' has been
-- triaged for under #625.
--
-- This spec is the mechanical regression gate.  It enforces three
-- properties on 'src/Sky/Build/Compile.hs':
--
--   1. The IORef definition is gone (no top-level
--      'globalGoSigMap :: IORef ...').
--   2. No live 'readIORef globalGoSigMap' / 'writeIORef
--      globalGoSigMap' / 'modifyIORef* globalGoSigMap' calls
--      exist.  Comments referring to the deleted IORef by name
--      are allowed (they document the migration) — only LIVE
--      Haskell expressions trip this gate.
--   3. The sentinel '_globalGoSigMap_SHOULD_NOT_EXIST' is present
--      so future greppers find the deletion rationale and don't
--      re-introduce the global.
module Sky.Build.LowerPhaseGoSigMapDeletedSpec (spec) where

import qualified Data.List as List
import Test.Hspec


spec :: Spec
spec = do
    describe "Compile.hs globalGoSigMap IORef DELETED (v0.17 PR-α step-5)" $ do
        it "no top-level 'globalGoSigMap ::' definition remains" $ do
            src <- readFile "src/Sky/Build/Compile.hs"
            -- Walk every line; flag any non-comment line that
            -- begins with the legacy binding name.  Comments
            -- referring to the deletion rationale start with '--'
            -- and are exempt.
            let definitionLines =
                    [ ln
                    | ln <- lines src
                    , "globalGoSigMap ::" `List.isPrefixOf` ln
                    ]
            definitionLines `shouldBe` []
        it "no live 'readIORef globalGoSigMap' calls remain" $ do
            src <- readFile "src/Sky/Build/Compile.hs"
            let badLines =
                    [ ln
                    | ln <- lines src
                    , "readIORef globalGoSigMap" `List.isInfixOf` ln
                    , not (isCommentLine ln)
                    ]
            badLines `shouldBe` []
        it "no live 'writeIORef globalGoSigMap' calls remain" $ do
            src <- readFile "src/Sky/Build/Compile.hs"
            let badLines =
                    [ ln
                    | ln <- lines src
                    , "writeIORef globalGoSigMap" `List.isInfixOf` ln
                    , not (isCommentLine ln)
                    ]
            badLines `shouldBe` []
        it "no live 'modifyIORef* globalGoSigMap' calls remain" $ do
            src <- readFile "src/Sky/Build/Compile.hs"
            let badLines =
                    [ ln
                    | ln <- lines src
                    , ("modifyIORef globalGoSigMap" `List.isInfixOf` ln
                        || "modifyIORef' globalGoSigMap" `List.isInfixOf` ln)
                    , not (isCommentLine ln)
                    ]
            badLines `shouldBe` []
        it "sentinel '_globalGoSigMap_SHOULD_NOT_EXIST' is present" $ do
            src <- readFile "src/Sky/Build/Compile.hs"
            ("_globalGoSigMap_SHOULD_NOT_EXIST" `List.isInfixOf` src)
                `shouldBe` True
  where
    -- | A line is treated as a comment when its first
    -- non-whitespace characters are '--'.  Multi-line
    -- '{- ... -}' blocks are not detected — globalGoSigMap
    -- mentions in Compile.hs are all single-line '--' comments
    -- after the migration.
    isCommentLine :: String -> Bool
    isCommentLine ln =
        case dropWhile (\c -> c == ' ' || c == '\t') ln of
            '-':'-':_ -> True
            _         -> False
