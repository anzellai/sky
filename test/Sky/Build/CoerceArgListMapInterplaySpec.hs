module Sky.Build.CoerceArgListMapInterplaySpec (spec) where

-- v0.15.x hardening — Cycle 1 / Plan Item P2-followup LOCK test.
--
-- This spec is the load-bearing regression test for the three-way
-- σ consensus invariant called out in
-- `docs/v0.15.x-hardening/arbitrations/HEAD-CYCLE-01-P2.md`.
--
-- Three mechanisms vote on the typed TVar instantiation of a
-- polymorphic Sky kernel call (e.g. `Sky_Core_List_map_[T1, T2]`):
--
--   1. σ-recovery in `coerceCallArgs(At)` — reads typed arg shapes
--      and pins TVars when source/target agree.
--   2. TVar erasure (`eraseTypeParams`) — when σ leaves a TVar
--      unbound, the substituted param type collapses to `any`.
--   3. `coerceArg`'s skip-check arm — when source already matches
--      the (post-erasure) target, the coercion wrap is elided.
--
-- The canonical reproducer below mixes a typed kernel call
-- (`List.take 6 categories : List String`) feeding into a
-- polymorphic kernel (`List.map`) with a lambda whose closure
-- type erases to `func(any) any`:
--
--   List.map (\cat -> cat ++ "!") (List.take 6 categories)
--
-- Pre-P2 behaviour (sound, lossy):
--   * σ-recovery leaves T1 unbound (lambda is `any`-typed → no pin
--     from the lambda side; type-recovery for the typed list-arg
--     would conflict with the lambda's `any`).
--   * TVar erasure: T1 → `any`, target slot becomes `[]any`.
--   * coerceArg skip-check sees source = `[]string`, target =
--     `[]any` (different) → DOES NOT elide → wraps with
--     `rt.AsListAny`.  Lambda emits as `func(any) any`.
--   * Go's call-site inference: T1 = any uniformly across both
--     sibling args.  `go build` accepts.
--
-- P2-broken behaviour (the regression this test locks down):
--   * `goExprGoType`'s structural fallback (audit-A2 fix) reports
--     `Sky_Core_List_take__String(...)` as `[]string`.
--   * σ-recovery still stays strict (deliberately passes Nothing).
--   * coerceArg's skip-check, with `mSrc=Just`, sees source
--     `[]string` matches target `[]string` (the recovery σ has
--     also pinned T1=string elsewhere via the fallback) → elides
--     the `rt.AsListAny` wrap.
--   * But the LAMBDA arg's recovery σ still saw `any` → lambda
--     emits `func(any) any`.  T1 has TWO different instantiations
--     from the two sibling args.  Go rejects:
--       "type []string of Sky_Core_List_take__String(6, ...)
--        does not match inferred type []any for []T1"
--
-- Post-followup (P2-followup):
--   * coerceArg's skip-check is gated to use the IR-SHAPE
--     classifier ALONE (`goExprGoType Nothing e`), no structural
--     fallback.  Pre-P2 behaviour restored at this voting site.
--   * Structural fallback still consumed by other sites that DO
--     benefit (`coerceArg`'s parametric-alias arm, the `wrapTyped
--     Return` consumers that already pass Nothing, etc.).
--   * The three-way consensus stays consistent across sibling args.
--
-- The spec's invariant is "the fixture clean-builds" — the SHAPE
-- of the wrap (which arg widens) is irrelevant.  Any future change
-- that breaks `Sky_Core_List_map_(λ, List.take 6 xs)` re-trips it.

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         copyFile, doesFileExist, listDirectory,
                         doesDirectoryExist)
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
    if ok
        then return candidate
        else fail ("sky binary missing at " ++ candidate
                ++ " — run cabal install --installdir=./sky-out first")


copyTree :: FilePath -> FilePath -> IO ()
copyTree src dst = do
    createDirectoryIfMissing True dst
    entries <- listDirectory src
    mapM_
        (\e -> do
            let s = src </> e
                d = dst </> e
            isF <- doesFileExist s
            if isF
                then copyFile s d
                else do
                    isD <- doesDirectoryExist s
                    if isD then copyTree s d else return ())
        entries


spec :: Spec
spec = describe "coerceArg <-> goExprGoType three-way σ consensus (List.map / List.take interplay)" $ do
    it "clean-builds the List.map (\\cat -> cat ++ \"!\") (List.take 6 xs) shape" $ do
        sky <- findSky
        cwd <- getCurrentDirectory
        let fixtureRoot = cwd </> "test" </> "fixtures"
                              </> "coerce-arg-list-map-interplay"
        withSystemTempDirectory "sky-coerce-listmap" $ \tmp -> do
            copyTree fixtureRoot tmp
            let cpBuild = (proc sky ["build", "src/Main.sky"])
                            { cwd = Just tmp }
            (ec, bout, berr) <- readCreateProcessWithExitCode cpBuild ""
            let combined = bout ++ berr
            case ec of
                ExitSuccess -> return ()
                ExitFailure n ->
                    expectationFailure $
                        "sky build failed (" ++ show n ++ "):\n"
                        ++ combined
            -- Defensive: also confirm the go-build leg, not just
            -- sky compile, made it through.  `Build complete`
            -- appears on stdout AFTER go build succeeds.
            ("Build complete" `isInfixOf` combined) `shouldBe` True

    it "runtime output is the 6-element concat" $ do
        sky <- findSky
        cwd <- getCurrentDirectory
        let fixtureRoot = cwd </> "test" </> "fixtures"
                              </> "coerce-arg-list-map-interplay"
        withSystemTempDirectory "sky-coerce-listmap-run" $ \tmp -> do
            copyTree fixtureRoot tmp
            (bec, bout, berr) <- readCreateProcessWithExitCode
                (proc sky ["build", "src/Main.sky"]) { cwd = Just tmp } ""
            let bcombined = bout ++ berr
            case bec of
                ExitSuccess -> return ()
                ExitFailure n ->
                    expectationFailure $
                        "sky build failed (" ++ show n ++ "):\n"
                        ++ bcombined
            let appPath = tmp </> "sky-out" </> "app"
            (rec, rout, rerr) <- readCreateProcessWithExitCode
                (proc appPath []) { cwd = Just tmp } ""
            let rcombined = rout ++ rerr
            case rec of
                ExitSuccess -> return ()
                ExitFailure n ->
                    expectationFailure $
                        "binary exited (" ++ show n ++ "):\n"
                        ++ rcombined
            -- First 6 elements of categories: a..f with "!" appended,
            -- concatenated → "a!b!c!d!e!f!"
            ("a!b!c!d!e!f!" `isInfixOf` rout) `shouldBe` True
            ("panic" `isInfixOf` rcombined) `shouldBe` False
