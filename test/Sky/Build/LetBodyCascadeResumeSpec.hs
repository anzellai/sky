-- | v0.15.x hardening — Plan Item P37b (LowerCtx cascade Phase 3).
--
-- This spec is the lock for the three deferred-slot migrations
-- that P6 (v0.15.15) reverted because `letBindingType`'s
-- IORef-backed region lookup formed a deferred-thunk cycle with
-- the ctx-aware wrapper's write/restore.  P37b broke the cycle:
--
--   1. P37a put the per-region HM type map on `Solve.SolvedTypes`
--      as a pure record field (`_stRegions`).
--   2. P37b makes `letBindingType` PURE — it no longer takes a
--      `LC.LowerCtx`; its region lookup is a pure projection over
--      `Solve.SolvedTypes._stRegions` via `Solve.lookupSolvedRegion`.
--   3. The `_lc_regionTypes` field on `LC.LowerCtx` was deleted
--      together with the `LC.lookupRegionType` helper that read it.
--   4. The three deferred slots (record-field init, list element,
--      let body) now route through the `lowerExprExpectGo` /
--      `lowerExpr` wrappers — the same explicit-ctx mechanism the
--      lambda body + typed call arg slots used since P6.
--
-- The end-to-end contract the spec pins:
--
--   * `letBindingType :: Solve.SolvedTypes -> String -> Can.Expr
--                       -> Maybe T.Type` is pure-callable from a
--     plain `IO` smoke harness — no `scopeStateRef` setup, no
--     `unsafePerformIO` ladder.
--   * The three previously-blackholing fixture shapes (parametric-
--     alias record literal + typed list element + nested
--     control-flow let-body) compile cleanly and produce real
--     typed Go output.
--
-- The structural check on the cascade-resume output is the
-- "regression flag" — a P37b revert that drops the wrapper
-- routing back to bare `exprToGoExpectGo` would lose the typed
-- coerce wrappers (`rt.CoerceInt`, `rt.CoerceString`, …) that
-- the let-body slot ships under the cascade.

module Sky.Build.LetBodyCascadeResumeSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
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


-- | The P6 blackhole reproducer in its smallest deterministic
-- form: a parametric-alias record literal whose field carries a
-- typed lambda body, plus a list element carrying the same shape,
-- plus a let-body whose RHS is a control-flow case expression
-- (the precise let-body shape that triggered GHC's `<<loop>>`
-- under skyshop in May 2026).
--
-- Each construct exercises one of the three deferred slots P37b
-- re-migrated:
--
--   * `Cfg Int` record literal in `mkCfg`        — record-field-init slot.
--   * `[ 1, 2, 3 ]` list literal in `xs`         — list-element slot.
--   * `let total = case xs of …` body in `main`  — let-body slot.
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


spec :: Spec
spec = do
    describe "Sky.Build.Compile — P37b LowerCtx cascade Phase 3 resume" $ do
        it "compiles the three-slot resume fixture cleanly" $ do
            -- The fixture is a downstream-deterministic shrink of
            -- the P6 skyshop / CoerceArgParametricSpec blackholes.
            -- Pre-P37b this either tripped `<<loop>>` (under the
            -- mechanical wrapper migration) or fell back to the
            -- legacy push/pop path (under P6's revert).  Post-P37b
            -- the wrapper routes through the ctx-aware path AND
            -- the build completes successfully — that combination
            -- is the load-bearing acceptance criterion.
            sky <- findSky
            withSystemTempDirectory "sky-p37b-resume" $ \tmp -> do
                writeFixtureProject tmp "p37b-resume" threeSlotResumeSource
                (bec, bout, berr) <- runSky sky ["build", "src/Main.sky"] tmp
                let bcombined = bout ++ berr
                bec `shouldBe` ExitSuccess
                ("Build complete" `isInfixOf` bcombined) `shouldBe` True
                -- And the no-blackhole guarantee:
                ("<<loop>>" `isInfixOf` bcombined) `shouldBe` False
                ("panic" `isInfixOf` bcombined) `shouldBe` False

                let appPath = tmp </> "sky-out" </> "app"
                (rec_, rout, _) <- readCreateProcessWithExitCode
                    (proc appPath []) ""
                rec_ `shouldBe` ExitSuccess
                -- 41 + 1 = 42, via the let-body case expression.
                ("42" `isInfixOf` rout) `shouldBe` True

        it "emits typed-coerce wrappers on the let-body IIFE" $ do
            -- Cascade-resume signature: when `letToGo` knows the
            -- body's expected Go type, the body's IIFE now routes
            -- through `lowerExprExpectGo` and the resulting Go
            -- coerces the IIFE's return value (`rt.CoerceInt`,
            -- `rt.CoerceString`, …) to the typed slot.  Pre-cascade
            -- the IIFE returned `any` and skipped the coerce.  A
            -- regression that reverted the let-body migration would
            -- drop the wrap.
            sky <- findSky
            withSystemTempDirectory "sky-p37b-coerce" $ \tmp -> do
                writeFixtureProject tmp "p37b-coerce" threeSlotResumeSource
                (bec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                bec `shouldBe` ExitSuccess
                let mainGo = tmp </> "sky-out" </> "main.go"
                generated <- readFile mainGo
                -- At least one rt.Coerce* wrap appears in the
                -- emitted Go — the fixture's `total = case …` body
                -- forces the typed-IIFE path.  Without P37b's
                -- cascade resume the let-body emitted `func() any`
                -- with no surrounding Coerce.
                let hasCoerce =
                        "rt.Coerce" `isInfixOf` generated
                hasCoerce `shouldBe` True

        it "letBindingType is callable as a pure function over SolvedTypes" $ do
            -- The function's public surface is the P37b contract.
            -- The IORefBoundarySpec already pins the signature via
            -- a literal string match; this spec verifies the
            -- behavioural surface — `Compile.hs` exports nothing
            -- from this module (it's all internal) so we can't
            -- import the function directly.  Instead we lock the
            -- signature via the same source-file string match plus
            -- the build-time effect (the fixture compiles).
            src <- readFile "src/Sky/Build/Compile.hs"
            -- Pure shape post-P37b:
            ("letBindingType :: Solve.SolvedTypes -> String -> Can.Expr"
                `isInfixOf` src) `shouldBe` True
            -- The function calls Solve.lookupSolvedRegion (pure
            -- projection over SolvedTypes) NOT the deleted
            -- IORef-backed Compile.lookupRegionType.
            ("Solve.lookupSolvedRegion r solvedTypes"
                `isInfixOf` src) `shouldBe` True
