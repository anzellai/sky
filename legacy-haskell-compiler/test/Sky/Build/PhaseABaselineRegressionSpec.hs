{-# LANGUAGE OverloadedStrings #-}

-- | Sky.Build.PhaseABaselineRegressionSpec — v0.17 Phase A ratchet gate.
--
-- DESIGN DOC: docs/v0.17-roadmap/phase-A-cgenv-reshape.md
-- LOCKED DECISIONS (2026-06-24, user-confirmed):
--   * globalAnonRecords = Option (c) — bounded-monotonic IORef with
--     documented contract + spec verification.
--   * Phase C (runtime kernel monomorphisation) DROPPED — per
--     AUTONOMOUS_GOAL.md REVISED SCOPE 2026-06-21, Maybe/Result/Task
--     have zero rt.Coerce hits and already typed end-to-end.
--   * Phase A timeline accepted at 6-10 weeks (honest).
--   * Phase B independent of Phase A, parallel-trackable.
--   * Phase D = Phase 4 Stage 7+ continuation, not a separate phase.
--
-- WHAT THIS SPEC IS
--
-- Phase A reshapes the lowerer's per-scope state — currently held in
-- a single `scopeStateRef :: IORef LC.LowerCtx` CAF at
-- @src/Sky/Build/Compile.hs:509@ — into an explicit threaded
-- @cgEnv@ parameter, then DELETES the IORef.  Six-to-ten weeks of
-- compiler refactor work touching ~59 reader sites
-- (`getCgEnvFromScope`-shaped) and 1 writer.
--
-- The risk the grill identified: by iter 9 of Phase A the
-- `SKY_CGENV_DIFF=1` differential gate — which currently catches
-- writer/reader divergence by running BOTH the IORef-backed path
-- and the threaded path and comparing outputs — vanishes as a
-- safety net, because the IORef path itself is what we're deleting.
-- Iter 9 deletes the CAF.  Post-deletion the diff-gate cannot
-- run.  We need a SECONDARY post-deletion gate to catch regressions
-- introduced by post-deletion refactors.
--
-- This spec IS that secondary gate.  It pins iter-0 (baseline)
-- measurements of three structural invariants on the emitted Go +
-- the compiler source, then asserts AT EVERY BUILD that the
-- current values are MONOTONE NON-INCREASING (criterion #3 ratchet
-- semantics — matches the design doc + the user's verbatim goal
-- "rock solid + future proof").
--
-- HOW IT INTERACTS WITH `RtCoerceBudgetSpec`
--
-- `RtCoerceBudgetSpec` already pins per-cluster `rt.Coerce*` counts
-- on `26-ui-showcase`.  This spec is intentionally NARROWER in
-- structural scope (5 measurements vs 9 clusters) but BROADER in
-- coverage:
--
--   * Adds 00-standard-libs — RtCoerceBudgetSpec only covers
--     26-ui-showcase, so a regression introduced in stdlib HOF
--     emission that doesn't reach the showcase would slip past the
--     existing gate.  00-standard-libs exercises the bare stdlib
--     surface (no Std.Ui interaction).
--
--   * Adds compiler-source structural invariants — Phase A's whole
--     point is `IORef count :: 1 → 0` + `getCgEnvFromScope ::
--     59 → 0`.  No existing spec gates these counts.
--
-- The two specs are COMPLEMENTARY: RtCoerceBudgetSpec is the
-- per-cluster fine-grained ratchet on the ui surface;
-- PhaseABaselineRegressionSpec is the structural / cross-example /
-- compiler-source ratchet for Phase A.
--
-- HOW TO RATCHET DOWN (post-IORef-deletion future)
--
-- When Phase A iter 9 ships and the IORef count drops to 0, run
-- the spec.  It will report:
--
--   ✗ phase-A baseline: IORef count
--       baseline=1 actual=0 (under by 1 — RATCHET DOWN)
--
-- That's the signal to update `baselineCompileIORefCount = 0` IN
-- THIS FILE in the same commit so the gate forward-locks the win.
-- Going UP from 0 → 1 (someone re-introduces an IORef) trips the
-- spec.  Going DOWN further is impossible (0 is the floor).
--
-- The same ratchet semantics apply to every other measurement:
-- ANY drop is a forward-lockable win and the maintainer updates
-- the baseline in the same commit.
--
-- WHAT GETS PINNED
--
-- 1. `rt.Coerce` matching-line count in `examples/26-ui-showcase`.
-- 2. `rt.Coerce` matching-line count in `examples/00-standard-libs`.
-- 3. `rt.AsListT` matching-line count in `examples/26-ui-showcase`.
-- 4. IORef declaration count in `src/Sky/Build/Compile.hs`.
-- 5. `getCgEnvFromScope` reader site count in
--    `src/Sky/Build/Compile.hs`.
--
-- Pinned values reflect THIS REPO STATE (HEAD = f4848aba +
-- this commit) — measured at iter-0 by clean-rebuilding both
-- examples and grep-counting the compiler source.  See Bash
-- transcript at the bottom of this file's commit message.

module Sky.Build.PhaseABaselineRegressionSpec (spec) where

import qualified Data.List as List
import qualified System.Exit as Exit
import System.Directory (doesFileExist, getCurrentDirectory)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import Test.Hspec


-- ---------------------------------------------------------------------------
-- PINNED ITER-0 BASELINES (2026-06-24 — Phase A iter 0)
-- ---------------------------------------------------------------------------
--
-- Measurement protocol:
--
-- 1. From repo root, build the local sky binary
--    (`scripts/build.sh` or equivalent).
-- 2. cd into examples/26-ui-showcase ; rm -rf sky-out .skycache
--    .skydeps ; <sky-out/sky> build src/Main.sky
-- 3. Capture: grep -c 'rt\.Coerce'  examples/26-ui-showcase/sky-out/main.go
-- 4. Capture: grep -c 'rt\.AsListT' examples/26-ui-showcase/sky-out/main.go
-- 5. cd into examples/00-standard-libs ; same clean-rebuild
-- 6. Capture: grep -c 'rt\.Coerce'  examples/00-standard-libs/sky-out/main.go
-- 7. Compiler source counts: grep -cE '^[a-zA-Z_]+Ref :: IORef'
--    src/Sky/Build/Compile.hs   (declarations, not uses)
-- 8. Compiler source counts: grep -c 'getCgEnvFromScope'
--    src/Sky/Build/Compile.hs
--
-- The spec re-runs steps 2-6 inside @beforeAll@.  Steps 7-8 are
-- pure file reads.


-- | Baseline `rt.Coerce` matching-line count for
-- examples/26-ui-showcase post-iter-0.  CURRENT FLOOR (the ratchet
-- comparison is `actual <= baseline`).
--
-- Bumped 172 → 177 (2026-06-29): `ad7d7eec` routed Anon_R_* targets
-- through `rt.Coerce[T]` instead of nominal `any(x).(Anon_R)` direct
-- assertions.  +5 `rt.Coerce` calls is the price of removing a panic
-- class (struct→struct narrowing now goes through the rt.Coerce
-- reflect path which handles field-mismatch gracefully).  Future
-- compiler-level reductions (struct→struct elision when source +
-- target structural-equal) can ratchet this back down.
--
-- Bumped 177 → 229 (2026-07-01): v0.17 Gap 1 close — CLAUDE.md §8
-- non-regression rule ("no raw `.(T)` assertions on any-typed
-- thunks"). Added `CoerceSealedIface` classifier arm to
-- `classifyCoerceTarget` (Compile.hs:15165) plus parallel guards in
-- `coerceArg` (:17497), `coerceSubject` (:19343), and `legacyTcoCase`
-- (:19670) so sealed-iface targets ROUTE THROUGH `rt.Coerce[<iface>]`
-- instead of a raw `any(x).(SealedIface)` assertion. +52 rt.Coerce
-- calls is the price of removing 52 raw `.(T)` sites on Std_Ui_*
-- sealed-iface types (Std_Ui_Attribute, Std_Ui_Element, etc.) —
-- direct §8 compliance. All added sites are Class 1 residual per
-- `docs/v0.17/rt-coerce-residual-surface.md` (sealed-interface ctor
-- narrowing; sound by construction via `_cg_sealedIfaceNames`
-- registry). Future sealed-iface flip iterations (per iters 63-72
-- pattern in `sealedIfaceFlipAllowList`) elide these via
-- direct-ctor-body detection.
baseline26UiShowcaseRtCoerce :: Int
baseline26UiShowcaseRtCoerce = 229


-- | Baseline `rt.Coerce` matching-line count for
-- examples/00-standard-libs post-iter-0.  Diverges from the user-
-- stated 116 — actual clean-rebuild on f4848aba measures 124.
-- The 116 figure likely came from an earlier snapshot before
-- recent CPS / stdlib emission shifts; we pin THE TRUTHFUL count
-- so the gate isn't already in regression.  The ratchet semantics
-- mean any future drop to 116 (or lower) is a forward-lockable
-- win.
--
-- Bumped 124 → 125 (2026-06-30): commit `277ee217` paired the
-- struct-decl + record-literal cgEnv widening at five sites
-- (`generateAliasForDep`, `generateStruct`, `lowerRecordLiteralTo`,
-- + two `exprToGo` arms).  User record aliases referenced inside
-- Maybe/Result/Task wrappers now resolve to their typed
-- `<Mod>_<Name>_R` shape instead of falling through to the kernel
-- `rt.Sky<Name>` / `any` fallback.  The single +1 `rt.Coerce` is
-- the cost of one extra typed↔runtime bridge call where the
-- widened typed shape now hits an FFI any-boundary that
-- previously was reached with `any` directly.  This is the
-- correct direction for v0.17 (tighter type fidelity); a future
-- pass can elide the residual bridge once the bridged callee is
-- also widened to consume the typed shape.
--
-- Bumped 125 → 127 (2026-07-01): v0.17 Gap 1 close — same commit as
-- 26-ui-showcase 177 → 229 bump. Sky.Test.TestResult IS in
-- `sealedIfaceFlipAllowList` so the new `CoerceSealedIface` arm fires
-- on case-subject / coerceArg sites here too. +2 rt.Coerce calls
-- represent 2 raw `any(x).(Sky_Test_TestResult)` sites that now route
-- through `rt.Coerce[Sky_Test_TestResult]`. Class 1 residual (see doc
-- referenced above).
--
-- Bumped 127 → 128 (2026-07-01, v0.17.1 PR #136): Math.min/max
-- Float-truncation fix routes these kernels through the polymorphic
-- rt.Math_min / rt.Math_max path (skyLessThan comparator) instead of
-- the typed-int rt.Math_minT / rt.Math_maxT + rt.AsInt args path.
-- Same emission delta as the 26-ui-showcase CoerceFloat +1 bump —
-- one extra rt.Coerce site where the AsInt path had none.
baseline00StandardLibsRtCoerce :: Int
baseline00StandardLibsRtCoerce = 128


-- | Baseline `rt.AsListT` matching-line count for
-- examples/26-ui-showcase post-iter-0.
--
-- Ratcheted 191 → 189 (2026-06-29): typed-emit improvements
-- reduced 2 redundant AsListT wraps on cross-module HOF callbacks.
baseline26UiShowcaseRtAsListT :: Int
baseline26UiShowcaseRtAsListT = 189


-- | Baseline declared-IORef count in src/Sky/Build/Compile.hs.
-- Currently 1: `scopeStateRef`.  Phase A iter 9 deletes this CAF;
-- the post-deletion baseline becomes 0 (which then becomes the
-- monotone floor — re-introducing an IORef forever trips this
-- gate).
baselineCompileIORefCount :: Int
baselineCompileIORefCount = 1


-- | Baseline `getCgEnvFromScope` reader-site count in
-- src/Sky/Build/Compile.hs.  Phase A migrates each reader to receive
-- a threaded `cgEnv` parameter; the count drops monotonically to 0
-- over iters 1-8 before iter 9 deletes the helper entirely.
--
-- Ratcheted 37 → 3 (2026-06-29): the v0.17 PR-α + IORef-defusing
-- batch + Phase A iter 6a/6b/6c/6d work collectively migrated 34
-- reader sites to threaded-ctx form.  3 remaining are the residual
-- bridge sites still consulting the scopeStateRef IORef while the
-- final reader migration is in flight.
baselineGetCgEnvFromScopeCount :: Int
baselineGetCgEnvFromScopeCount = 3


-- ---------------------------------------------------------------------------
-- Build orchestration — clean-rebuild both examples in beforeAll
-- ---------------------------------------------------------------------------


-- | Locate the sky binary built by the harness.  Same shape as
-- RtCoerceBudgetSpec.
findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok
        then return c
        else fail ("missing: " ++ c ++ "\n"
                ++ "Run scripts/build.sh first to produce the local "
                ++ "sky binary that this spec exercises.")


-- | Clean-slate build of the named example.  Returns the emitted
-- main.go contents on success, an error string on failure.  The
-- `rm -rf sky-out .skycache .skydeps` wipe is essential — a
-- partial cached build would mask a coerce-count regression.
buildExample :: String -> IO (Either String String)
buildExample exDir = do
    sky <- findSky
    cwd <- getCurrentDirectory
    let dir = cwd </> "examples" </> exDir
        cmd = "cd " ++ dir
           ++ " && rm -rf sky-out .skycache .skydeps 2>/dev/null; "
           ++ sky ++ " build src/Main.sky 2>&1"
    (ec, out, err) <- readCreateProcessWithExitCode (shell cmd) ""
    case ec of
        Exit.ExitSuccess ->
            if "Compilation successful" `List.isInfixOf` out
                then do
                    src <- readFile (dir </> "sky-out" </> "main.go")
                    return (Right src)
                else return (Left ("build did not declare 'Compilation successful':\n"
                                ++ out ++ "\n" ++ err))
        Exit.ExitFailure n ->
            return (Left ("sky build " ++ exDir ++ " failed (exit "
                       ++ show n ++ "):\n" ++ out ++ err))


-- | Read the compiler source — the IORef + reader counts read
-- straight from the on-disk file (no compilation step needed).
readCompileHs :: IO String
readCompileHs = do
    cwd <- getCurrentDirectory
    readFile (cwd </> "src" </> "Sky" </> "Build" </> "Compile.hs")


-- | Count `grep -c`-style matching LINES of @needle@ in @src@.
-- A line containing the needle twice still counts as 1.
countMatchingLines :: String -> String -> Int
countMatchingLines needle src =
    length [() | l <- lines src, needle `List.isInfixOf` l]


-- | Count IORef declarations — match lines of shape
-- `<name>Ref :: IORef ...`.  Mirrors the regex
-- `grep -E '^[a-zA-Z_]+Ref :: IORef' src/Sky/Build/Compile.hs`.
countIORefDeclarations :: String -> Int
countIORefDeclarations src =
    length [() | l <- lines src, isIORefDecl l]
  where
    isIORefDecl l =
        case words l of
            (nm : "::" : "IORef" : _)
                | "Ref" `List.isSuffixOf` nm
                , not (null nm)
                , not (isComment nm) ->
                    True
            _ -> False
    -- Type signatures inside doc-comments (e.g. `-- > foo :: IORef
    -- ...`) start with `--`.  Filter those out so the count tracks
    -- ACTUAL declarations.
    isComment nm = "--" `List.isPrefixOf` nm


-- ---------------------------------------------------------------------------
-- Ratchet helpers — assert actual <= baseline; emit ratchet hint on drop
-- ---------------------------------------------------------------------------


data MeasurementResult = MeasurementResult
    { mrLabel    :: !String
    , mrBaseline :: !Int
    , mrActual   :: !Int
    }


-- | Format a single measurement for the human-readable summary.
formatMeasurement :: MeasurementResult -> String
formatMeasurement (MeasurementResult lbl b a) =
    "  " ++ pad 42 lbl
         ++ "  baseline=" ++ pad 4 (show b)
         ++ "  actual=" ++ pad 4 (show a)
         ++ mark
  where
    mark
      | a > b     = "  REGRESSION (over by " ++ show (a - b) ++ ")"
      | a < b     = "  (RATCHET DOWN — update baseline in spec!)"
      | otherwise = "  (at floor)"
    pad n s = s ++ replicate (max 0 (n - length s)) ' '


-- | Check a single measurement.  Returns Nothing on success
-- (actual <= baseline), Just <reason> on regression.
checkMeasurement :: MeasurementResult -> Maybe String
checkMeasurement (MeasurementResult lbl b a)
    | a > b = Just $
        "PHASE A REGRESSION: " ++ lbl
        ++ "\n  baseline = " ++ show b
        ++ "\n  actual   = " ++ show a
        ++ "\n  over by  = " ++ show (a - b)
        ++ "\nFix one of:\n"
        ++ "  (a) Fix the new regression site (preferred — that's "
        ++ "why this gate exists);\n"
        ++ "  (b) If the new count is PROVEN-CORRECT and intentional, "
        ++ "ratchet baseline UP in the same commit "
        ++ "in test/Sky/Build/PhaseABaselineRegressionSpec.hs with a "
        ++ "comment justifying the increase."
    | otherwise = Nothing


-- ---------------------------------------------------------------------------
-- Spec wiring
-- ---------------------------------------------------------------------------


-- | beforeAll context — gather every measurement upfront so each
-- @it@-clause is a pure assertion against a precomputed value.
-- The clean-rebuild + file-read cost is paid ONCE per spec
-- invocation.
data PhaseACtx = PhaseACtx
    { ctx26UiShowcase    :: Either String String  -- main.go OR build error
    , ctx00StandardLibs  :: Either String String
    , ctxCompileHsSrc    :: String
    }


gatherPhaseACtx :: IO PhaseACtx
gatherPhaseACtx = do
    showcase <- buildExample "26-ui-showcase"
    stdlibs  <- buildExample "00-standard-libs"
    compHs   <- readCompileHs
    return (PhaseACtx showcase stdlibs compHs)


spec :: Spec
spec = beforeAll gatherPhaseACtx $
    describe "Sky.Build.PhaseABaselineRegression — v0.17 Phase A ratchet gate" $ do

        it "26-ui-showcase clean build succeeds" $ \ctx ->
            case ctx26UiShowcase ctx of
                Right _  -> return ()
                Left err -> expectationFailure err

        it "00-standard-libs clean build succeeds" $ \ctx ->
            case ctx00StandardLibs ctx of
                Right _  -> return ()
                Left err -> expectationFailure err

        it "every Phase A baseline is monotone non-increasing" $ \ctx -> do
            let measurements = collectMeasurements ctx
            -- Emit the summary up-front so cabal-test logs surface
            -- every count even on PASS (useful when maintainer is
            -- ratcheting + wants to see all five at once).
            putStrLn ""
            putStrLn "Phase A baseline measurements:"
            mapM_ (putStrLn . formatMeasurement) measurements
            -- Then check each.  Concatenate failures so one run
            -- surfaces every regression, not just the first.
            case concatMap reportFailure measurements of
                []   -> return ()
                msgs -> expectationFailure (concat msgs)
  where
    reportFailure m = case checkMeasurement m of
        Nothing  -> []
        Just msg -> [msg ++ "\n\n"]

    collectMeasurements ctx =
        let showcaseSrc = either (const "") id (ctx26UiShowcase ctx)
            stdlibsSrc  = either (const "") id (ctx00StandardLibs ctx)
            compileSrc  = ctxCompileHsSrc ctx
        in  [ MeasurementResult
                "rt.Coerce  in 26-ui-showcase"
                baseline26UiShowcaseRtCoerce
                (countMatchingLines "rt.Coerce" showcaseSrc)
            , MeasurementResult
                "rt.Coerce  in 00-standard-libs"
                baseline00StandardLibsRtCoerce
                (countMatchingLines "rt.Coerce" stdlibsSrc)
            , MeasurementResult
                "rt.AsListT in 26-ui-showcase"
                baseline26UiShowcaseRtAsListT
                (countMatchingLines "rt.AsListT" showcaseSrc)
            , MeasurementResult
                "IORef declarations in Compile.hs"
                baselineCompileIORefCount
                (countIORefDeclarations compileSrc)
            , MeasurementResult
                "getCgEnvFromScope readers in Compile.hs"
                baselineGetCgEnvFromScopeCount
                (countMatchingLines "getCgEnvFromScope" compileSrc)
            ]
