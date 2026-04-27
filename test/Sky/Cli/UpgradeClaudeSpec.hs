module Sky.Cli.UpgradeClaudeSpec (spec) where

-- `sky upgrade-claude` refreshes the cwd's CLAUDE.md from the
-- template embedded in the running `sky` binary at build time.
--
-- Problem solved: a user runs `sky init` at some sky version,
-- ships, then later runs `sky upgrade` to get a newer compiler.
-- The compiler's embedded template moves with new releases (new
-- stdlib APIs, deprecation notes, current limitations) but the
-- project's CLAUDE.md stays at the snapshot from init time. AI
-- assistants reading that stale CLAUDE.md hallucinate API names
-- (e.g. `Ui.max` vs the current `Ui.maximum`).
--
-- Contract:
--   1. With no existing ./CLAUDE.md → creates one from the
--      embedded template; exit 0.
--   2. With an existing ./CLAUDE.md → backs it up to ./CLAUDE.md.bak
--      and overwrites; exit 0.
--   3. The written file is byte-identical to templates/CLAUDE.md
--      that was bundled into this binary at build time (verified
--      structurally by checking the file contains a Sky-specific
--      anchor string the template is known to carry).

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


spec :: Spec
spec = do
    describe "sky upgrade-claude" $ do

        it "creates ./CLAUDE.md from the embedded template (no prior file)" $ do
            sky <- findSky
            withSystemTempDirectory "sky-upgrade-claude" $ \tmp -> do
                (ec, sout, _) <- readCreateProcessWithExitCode
                    (proc sky ["upgrade-claude"]) { cwd = Just tmp } ""
                ec `shouldBe` ExitSuccess
                doesFileExist (tmp </> "CLAUDE.md") `shouldReturn` True
                -- The output should clearly say "Created" (not
                -- "Refreshed") on the no-prior-file path.
                sout `shouldSatisfy` ("Created CLAUDE.md" `isInfixOf`)
                -- And no .bak should exist when there was nothing
                -- to back up.
                doesFileExist (tmp </> "CLAUDE.md.bak") `shouldReturn` False

        it "refreshes an existing ./CLAUDE.md and backs the prior copy to .bak" $ do
            sky <- findSky
            withSystemTempDirectory "sky-upgrade-claude-refresh" $ \tmp -> do
                let claudeMd = tmp </> "CLAUDE.md"
                writeFile claudeMd "# Old custom CLAUDE.md\nstale content here\n"
                (ec, sout, _) <- readCreateProcessWithExitCode
                    (proc sky ["upgrade-claude"]) { cwd = Just tmp } ""
                ec `shouldBe` ExitSuccess
                sout `shouldSatisfy` ("Refreshed CLAUDE.md" `isInfixOf`)
                -- The prior content lives on at .bak so accidental
                -- runs are recoverable.
                doesFileExist (tmp </> "CLAUDE.md.bak") `shouldReturn` True
                bakBody <- readFile (tmp </> "CLAUDE.md.bak")
                bakBody `shouldSatisfy` ("Old custom CLAUDE.md" `isInfixOf`)

        it "writes the current embedded template (anchor: contains a Sky-specific string)" $ do
            -- Verifies the binary actually serves the project-template
            -- variant of CLAUDE.md, not some other file. The anchor
            -- string is one templates/CLAUDE.md is known to carry.
            sky <- findSky
            withSystemTempDirectory "sky-upgrade-claude-anchor" $ \tmp -> do
                _ <- readCreateProcessWithExitCode
                    (proc sky ["upgrade-claude"]) { cwd = Just tmp } ""
                body <- readFile (tmp </> "CLAUDE.md")
                -- Anchor: every templates/CLAUDE.md release has carried
                -- this header at the top since the file was created.
                body `shouldSatisfy` ("Sky" `isInfixOf`)
                -- Sanity: the written file is non-trivial in size
                -- (the embedded template is many KB; an empty or
                -- tiny file would mean the splice broke).
                length body `shouldSatisfy` (> 1000)
