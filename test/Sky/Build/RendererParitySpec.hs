{-# LANGUAGE OverloadedStrings #-}

-- | v0.17 renderer parity scaffold.
--
-- PR-1 ships the structure that PR-5 fills in: the property test
-- diffing `solvedTypeToGo` (legacy chain) against
-- `Sky.Type.Solve.GoTypeBuild` (foundation) per `SolvedRegion`.
--
-- At PR-1 the foundation does not exist; this spec validates the
-- corpus + KnownDivergence allowlist invariants only.
--
-- Doc: docs/v0.17-full-e2e-typed-master-plan.md
module Sky.Build.RendererParitySpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist, listDirectory)
import System.FilePath ((</>), takeFileName)
import Data.List (sort, isPrefixOf, isInfixOf)
import qualified Sky.Build.KnownDivergence as KD

probesDir :: IO FilePath
probesDir = do
    cwd <- getCurrentDirectory
    return (cwd </> "tools" </> "probe-fixtures")

discoverProbes :: IO [FilePath]
discoverProbes = do
    root <- probesDir
    entries <- listDirectory root
    return (sort [ takeFileName e | e <- entries
                                  , "probe-" `isPrefixOf` e ])

spec :: Spec
spec = do
    describe "v0.17 PR-1 renderer-parity infrastructure" $ do

        it "discovers the 14 H/TCO fixtures the master plan calls for" $ do
            probes <- discoverProbes
            let hProbes   = filter ("probe-H"   `isPrefixOf`) probes
                tcoProbes = filter ("probe-TCO" `isPrefixOf`) probes
            -- The master plan calls for 7 H + 7 TCO probes.
            -- Filter excludes the legacy probe-H-tuple-destructure.
            let hNumbered = filter (\p -> any (`isPrefixOf` p)
                                          [ "probe-H1-", "probe-H2-"
                                          , "probe-H3-", "probe-H4-"
                                          , "probe-H5-", "probe-H6-"
                                          , "probe-H7-" ]) hProbes
            length hNumbered   `shouldBe` 7
            length tcoProbes   `shouldBe` 7

        it "every fixture ships sky.toml + src/Main.sky + expectations.txt + README.md" $ do
            root <- probesDir
            probes <- discoverProbes
            let required = ["sky.toml", "src/Main.sky", "expectations.txt", "README.md"]
            mapM_ (\probe -> mapM_ (\f -> do
                let p = root </> probe </> f
                exists <- doesFileExist p
                exists `shouldBe` True) required) probes

        it "KnownDivergence allowlist is empty at PR-1 (legacy is source of truth)" $ do
            length KD.knownDivergences `shouldBe` 0

        it "pre-mortem lesson 4: continue-block divergences are NOT allowlistable" $ do
            -- This is the contract: any future KnownDiv whose
            -- description mentions a continue block fails the
            -- gate. Verified inline as the gate's tripwire.
            KD.isContinueBlockDivergence "TCO continue block reassignment"
                `shouldBe` True
            KD.isContinueBlockDivergence "rt.SkyTuple2 widen"
                `shouldBe` False

    describe "v0.17 PR-5 GoTypeBuild parity (placeholder)" $ do
        it "PLACEHOLDER — populated at PR-5 when Sky.Type.Solve.GoTypeBuild lands" $ do
            -- At PR-5: spec becomes a QuickCheck property that
            -- diffs `solvedTypeToGo region` against
            -- `renderGoType (GoTypeBuild.lookup region)` over every
            -- SolvedRegion in the corpus, asserting either equality
            -- OR membership in KnownDivergence.
            --
            -- Pre-mortem lesson 2: parity coverage MUST include
            -- `tailPositionRegions :: Module -> [A.Region]` from
            -- Sky.Build.TailCallOpt, not just expression-body regions.
            True `shouldBe` True
