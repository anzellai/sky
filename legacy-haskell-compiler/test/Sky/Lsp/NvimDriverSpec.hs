module Sky.Lsp.NvimDriverSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist, findExecutable)
import System.Environment (lookupEnv)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, proc)
import System.Exit (ExitCode(..))

-- v0.13 G: wraps scripts/lsp-test-nvim.sh as a cabal test case so
-- the LSP-100% coverage requirement is enforced by `cabal test`.
-- The Lua driver exercises hover / completion / goto-definition
-- end-to-end through Neovim's real LSP client against a synthetic
-- fixture — catches editor-level bugs the synthetic JSON-RPC tests
-- in CapabilitiesSpec / HoverTypesSpec / RenameStableSpec miss
-- (label-vs-insertText, filterText, scope-handling, case-pattern
-- binder scope, etc.).
--
-- Skipped if neovim isn't installed (SKY_SKIP_LSP_NVIM=1 also
-- skips, for CI environments where headless nvim setup is too much
-- ceremony). When skipped the rest of the LSP test surface
-- (CapabilitiesSpec, HoverTypesSpec, RenameStableSpec, ScaleSpec,
-- DiagnosticsSpec, etc.) still runs.
spec :: Spec
spec = do
    describe "scripts/lsp-test-nvim.sh" $ do
        it "every USED symbol class — hover + goto-def" $ do
            skip <- lookupEnv "SKY_SKIP_LSP_NVIM"
            mNvim <- findExecutable "nvim"
            case (skip, mNvim) of
                (Just v, _) | v /= "" && v /= "0" ->
                    pendingWith "SKY_SKIP_LSP_NVIM set"
                (_, Nothing) ->
                    pendingWith "nvim not in PATH — LSP driver requires headless Neovim"
                _ -> do
                    cwd <- getCurrentDirectory
                    let script = cwd </> "scripts" </> "lsp-test-nvim.sh"
                    haveScript <- doesFileExist script
                    haveScript `shouldBe` True
                    (ec, out, err) <- readCreateProcessWithExitCode
                        (proc "bash" [script]) ""
                    case ec of
                        ExitSuccess -> return ()
                        _ -> do
                            putStrLn "─── lsp-test-nvim.sh stdout ───"
                            putStrLn out
                            putStrLn "─── lsp-test-nvim.sh stderr ───"
                            putStrLn err
                    ec `shouldBe` ExitSuccess
