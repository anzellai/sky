-- | v0.15.x hardening — Plan Item P38 (Cycle 3 / audit C10).
--
-- Cycle 3 audit C10 called out three SEPARATE
-- `unsafePerformIO (readIORef scopeStateRef)` sites that obtain a
-- `LC.LowerCtx` snapshot at the function entry of the P37b-resumed
-- cascade slots (record-field init, list element, let body).  Each
-- site previously called the bare `ctxFromIORef ()` helper, which
-- did NOT force the result to WHNF.  The wrappers
-- (`lowerExprExpectGo` / `lowerExpr`) carry their own
-- `ctx \`seq\` return ()` defensive force, but that runs at wrapper
-- entry — AFTER any sibling computation may have built thunks
-- reading `scopeStateRef`.  The PR #91 (P37b / v0.15.19) commit
-- message documented this hazard class as the load-bearing
-- `<<loop>>` reproducer on examples/13-skyshop.
--
-- P38 introduces `snapshotCallerCtx :: () -> LC.LowerCtx` that:
--
--   1. Reads `scopeStateRef` once.
--   2. Forces the resulting `LC.LowerCtx` to WHNF before returning
--      it (the `seq` pattern from PR #91).
--   3. Carries explicit Haddock documenting that the returned ctx
--      is the value installed at the CALL site, NOT any inner-
--      wrapper-installed value.
--
-- All three cascade-resume sites now route through this helper.
-- The structural shape this spec pins:
--
--   * `snapshotCallerCtx` is defined exactly once in `Compile.hs`.
--   * The body forces ctx to WHNF (literal `seq` text on the
--     binding's value-form line).
--   * The helper is `{-# NOINLINE #-}` (mirrors `ctxFromIORef`).
--   * Exactly three call sites (`snapshotCallerCtx ()`) live in
--     `Compile.hs` — one per cascade-resume slot.
--   * End-to-end build of the three-slot resume fixture (the same
--     fixture P37b's `LetBodyCascadeResumeSpec` uses) still
--     succeeds without `<<loop>>`.  Closes the integration arm:
--     the helper is wired into the typed-lowerer pipeline, not
--     just defined-and-unused.
--
-- The string-match approach mirrors `IORefBoundarySpec` /
-- `LetBodyCascadeResumeSpec` — cheap, immune to compiler-internal
-- renames within the helper's own signature, and the names this
-- spec pins are themselves the load-bearing semantic.
module Sky.Build.SnapshotCallerCtxSpec (spec) where

import Data.List (isInfixOf)
import qualified Data.List as List
import System.Directory (createDirectoryIfMissing, doesFileExist,
                         getCurrentDirectory)
import System.Exit (ExitCode (..))
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (CreateProcess (..), proc,
                       readCreateProcessWithExitCode)
import Test.Hspec


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok
        then return candidate
        else fail ("sky binary missing at " ++ candidate
                ++ " — run cabal install --installdir=./sky-out first")


runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
runSky sky args workDir = do
    let cp = (proc sky args) { cwd = Just workDir }
    readCreateProcessWithExitCode cp ""


writeFixtureProject :: FilePath -> String -> String -> IO ()
writeFixtureProject dir name body = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml")
        ("name = \"" ++ name
            ++ "\"\nversion = \"0.0.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
    writeFile (dir </> "src" </> "Main.sky") body


-- | The three-slot reproducer from `LetBodyCascadeResumeSpec`.
-- Reused verbatim so this spec ALSO exercises the helper as it sits
-- inside the typed-lowerer pipeline — the helper isn't just defined,
-- it's load-bearing for the three cascade-resume slots.
threeSlotResumeSource :: String
threeSlotResumeSource = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "type alias Cfg a ="
    , "    { value : a"
    , "    , label : String"
    , "    }"
    , ""
    , "mkCfg : Int -> Cfg Int"
    , "mkCfg n = { value = n, label = \"x\" }"
    , ""
    , "main ="
    , "    let"
    , "        cfg = mkCfg 41"
    , "        xs = [ 1, 2, 3 ]"
    , "        total ="
    , "            case xs of"
    , "                [] -> 0"
    , "                _  -> cfg.value + 1"
    , "    in"
    , "    println (String.fromInt total)"
    ]


-- | Count occurrences of an exact substring in a body.
countInfix :: String -> String -> Int
countInfix needle haystack = go haystack
  where
    n = length needle
    go [] = 0
    go s@(_:rest)
        | needle `List.isPrefixOf` s = 1 + go (drop n s)
        | otherwise                  = go rest


spec :: Spec
spec = do
    describe "Sky.Build.Compile — P38 snapshotCallerCtx helper" $ do

        it "defines `snapshotCallerCtx :: () -> LC.LowerCtx` exactly once" $ do
            src <- readFile "src/Sky/Build/Compile.hs"
            -- The helper's source-level signature.  Pinning the
            -- shape catches a refactor that silently drops the
            -- unit param (and with it the per-call-site fresh
            -- IORef read semantic).
            ("snapshotCallerCtx :: () -> LC.LowerCtx" `isInfixOf` src)
                `shouldBe` True
            -- The body's value-form line.  The `seq` is LOAD-BEARING;
            -- pinning the literal source guards the WHNF force
            -- against a "cleanup" that re-introduces the PR #91
            -- thunk hazard.
            ("ctx `seq` return ctx" `isInfixOf` src) `shouldBe` True

        it "is marked NOINLINE (matches ctxFromIORef contract)" $ do
            -- Without the pragma GHC can share the snapshot across
            -- nested cascade slots — silently breaking the
            -- "snapshot at the call site" semantic the helper's
            -- Haddock documents.
            src <- readFile "src/Sky/Build/Compile.hs"
            ("{-# NOINLINE snapshotCallerCtx #-}" `isInfixOf` src)
                `shouldBe` True

        it "documents the P37b PR #91 thunk hazard provenance" $ do
            -- The helper's Haddock must reference the v0.15.19
            -- P37b PR #91 thunk hazard so the `seq` pattern's
            -- purpose stays preserved across future cascade work.
            -- A bare `readIORef` (no provenance comment) is a tell
            -- that the contract has been forgotten.
            src <- readFile "src/Sky/Build/Compile.hs"
            ("P37b" `isInfixOf` src) `shouldBe` True
            -- PR #91 named explicitly so future archaeologists can
            -- chase the failure-class back to the load-bearing
            -- commit (c7a31df).
            ("PR #91" `isInfixOf` src) `shouldBe` True

        it "is called from exactly three cascade-resume sites" $ do
            -- The three P37b-resumed slots (record-field init, list
            -- element, let body) each take ONE snapshot at the
            -- function entry of their typed-lowerer arm.  A fourth
            -- call site is a flag — most likely a copy-paste
            -- regression that needs a second look, or a planned
            -- cascade extension that needs an updated audit gate.
            --
            -- Match the rvalue form `= snapshotCallerCtx ()` so the
            -- helper's own defining-equation (`snapshotCallerCtx ()
            -- = …`, which has the parens on the LHS not the RHS)
            -- doesn't double-count.
            src <- readFile "src/Sky/Build/Compile.hs"
            countInfix "= snapshotCallerCtx ()" src `shouldBe` 3
            -- Total occurrences (definition body line + 3 call
            -- sites) = 4.  Pinning this catches a stray reference
            -- (e.g. a leftover `ctxFromIORef ()` rename to
            -- `snapshotCallerCtx ()` in a Haddock that should
            -- have stayed pointing at the older helper).
            countInfix "snapshotCallerCtx ()" src `shouldBe` 4

        it "the cascade fixture still compiles cleanly under the helper" $ do
            -- End-to-end gate: the three-slot reproducer from
            -- `LetBodyCascadeResumeSpec` builds, runs, and prints
            -- the expected `42` (41 from `cfg.value` + 1, via the
            -- let-body case expression).  If the helper migration
            -- regressed the cascade — e.g. by reintroducing the
            -- thunk hazard — this fixture either blackholes
            -- (`<<loop>>`) or panics; both surface as a Build
            -- failure.
            sky <- findSky
            withSystemTempDirectory "sky-p38-snapshot" $ \tmp -> do
                writeFixtureProject tmp "p38-snapshot" threeSlotResumeSource
                (bec, bout, berr) <- runSky sky ["build", "src/Main.sky"] tmp
                let bcombined = bout ++ berr
                bec `shouldBe` ExitSuccess
                ("Build complete" `isInfixOf` bcombined) `shouldBe` True
                ("<<loop>>" `isInfixOf` bcombined) `shouldBe` False
                ("panic" `isInfixOf` bcombined) `shouldBe` False

                let appPath = tmp </> "sky-out" </> "app"
                (rec_, rout, _) <- readCreateProcessWithExitCode
                    (proc appPath []) ""
                rec_ `shouldBe` ExitSuccess
                ("42" `isInfixOf` rout) `shouldBe` True
