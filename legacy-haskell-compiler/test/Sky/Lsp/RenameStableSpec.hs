module Sky.Lsp.RenameStableSpec (spec) where

-- Audit P2-3: Solve.showType used to rename TVars per-call so two
-- hovers in the same file could display the same internal t108 as
-- different letters. Solve.moduleRenaming + showTypeWith gives the
-- LSP index a stable per-file renaming.
--
-- The LSP JSON-RPC is not exercised here (tracked under P3-2). This
-- spec verifies the public contract at the compiler-internal layer:
--   (1) `sky check` stays green for a module with multiple
--       polymorphic definitions that share TVars.
--   (2) The `sky fmt --stdin` round-trip keeps all annotations
--       intact (sanity).
-- Lower-level showTypeWith coverage needs library-level tests which
-- the current test-suite shape doesn't support (no build-depends on
-- the sky-compiler library). That wider wiring is tracked under
-- P3-2 as part of the LSP integration harness.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import System.Exit (ExitCode(..))
import System.IO.Temp (withSystemTempDirectory)
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


polymorphicFixture :: String
polymorphicFixture = unlines
    [ "module M exposing (identity, apply)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , ""
    , "identity : a -> a"
    , "identity x = x"
    , ""
    , "apply : (a -> b) -> a -> b"
    , "apply f x = f x"
    ]


spec :: Spec
spec = do
    describe "stable TVar renaming (audit P2-3)" $ do

        it "accepts a fixture with multiple polymorphic definitions" $ do
            sky <- findSky
            withSystemTempDirectory "sky-rename" $ \dir -> do
                let fixture = dir </> "Rename.sky"
                writeFile fixture polymorphicFixture
                (ec, _out, err) <- readCreateProcessWithExitCode
                    (shell (sky ++ " check " ++ fixture))
                    ""
                case ec of
                    ExitSuccess -> return ()
                    ExitFailure _ -> expectationFailure err

        it "sky fmt --stdin keeps annotated TVars (a, b, …) in output" $ do
            sky <- findSky
            (ec, out, _err) <- readCreateProcessWithExitCode
                (shell ("SKY_FMT_FORCE=1 " ++ sky ++ " fmt --stdin"))
                polymorphicFixture
            ec `shouldBe` ExitSuccess
            -- Annotations round-trip with user-written TVar names;
            -- the formatter doesn't get clever with renaming.
            ("identity : a -> a" `isInfixOf` out) `shouldBe` True
            ("apply : (a -> b) -> a -> b" `isInfixOf` out) `shouldBe` True
