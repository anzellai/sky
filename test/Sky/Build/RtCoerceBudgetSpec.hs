module Sky.Build.RtCoerceBudgetSpec (spec) where

-- v0.17 step-5 (#644) — rt.Coerce* per-cluster ratchet-down gate.
--
-- This spec is the user-visible "criterion #1 disqualifier" gate:
-- every `rt.Coerce*` call in 26-ui-showcase's emitted main.go must
-- not EXCEED its hardcoded baseline.  As future batches close more
-- coerceVia / typed-slot mismatches, the maintainer ratchets the
-- baseline DOWN in the same commit.  Going UP is a regression and
-- fails the spec.
--
-- Per adversary-2 #7: NO baseline file — the truth lives in the
-- spec source so the gate is impossible to ratchet without a code
-- review.
--
-- Counted clusters (per step description):
--
--   * `rt.Coerce[`     — generic single-type-arg narrowing
--                        (most common; record-narrow + ADT-narrow)
--   * `rt.CoerceInt`   — Int-specific narrowing (rt.AsInt fast path)
--   * `rt.CoerceString`
--   * `rt.CoerceBool`
--   * `rt.CoerceFloat`
--   * `rt.TaskCoerceT` — Task[Error, T] cross-instantiation widen
--   * `rt.ResultCoerce`— Result widen
--   * `rt.MaybeCoerce` — Maybe widen
--   * `rt.AsListT`     — typed list cast (per-element coerce)
--
-- Counts use `grep -c`-style semantics (matching LINES, not matches)
-- so the baseline numbers line up with the manual command a
-- maintainer runs when ratcheting:
--
--   grep -c 'rt\.Coerce\[' examples/26-ui-showcase/sky-out/main.go
--
-- Sibling: UiShowcaseRtCoerceClosedProofSpec asserts every call
-- carries a `// PROOF: ...` comment (qualitative — "we know why
-- every site exists").  This spec is the QUANTITATIVE gate —
-- "the count is bounded and monotonically decreasing".

import Test.Hspec
import qualified System.Exit as Exit
import System.Directory (getCurrentDirectory, doesFileExist)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import Data.List (isInfixOf)
import Data.Map.Strict (Map)
import qualified Data.Map.Strict as Map


-- | Hardcoded baseline captured post-step-3 (anon-record close).
--
-- HOW TO RATCHET: when a future batch closes coerceVia sites, run
-- the spec; failing comparisons report the new lower count; update
-- the Map entry IN THIS FILE in the same commit so the gate
-- forward-locks the win.
--
-- Initial values measured 2026-06-20 on feat/v0.17-fully-typed-codegen
-- @ post-step-3 HEAD via clean-slate `sky build src/Main.sky` against
-- examples/26-ui-showcase using:
--
--   grep -c 'rt\.Coerce\['      main.go   → 238
--   grep -c 'rt\.CoerceInt'     main.go   →  19
--   grep -c 'rt\.CoerceString'  main.go   →  82
--   grep -c 'rt\.CoerceBool'    main.go   →  17
--   grep -c 'rt\.CoerceFloat'   main.go   →  22
--   grep -c 'rt\.TaskCoerceT'   main.go   →   0
--   grep -c 'rt\.ResultCoerce'  main.go   →   0
--   grep -c 'rt\.MaybeCoerce'   main.go   →  24
--   grep -c 'rt\.AsListT'       main.go   → 171
--
-- The 5 active typed-fast-path clusters (Int/String/Bool/Float)
-- partition correctly from the bare `rt.Coerce[` cluster because
-- `rt.CoerceInt` does NOT contain the substring `rt.Coerce[`
-- (the typed-fast-path emission spells out the type suffix).
-- So the per-cluster grep partitioning is mutually exclusive and
-- additive.
--
-- Note on `rt.AsListT` (171) and `rt.MaybeCoerce` (24): both are
-- typed-coerce-list / typed-maybe-narrow helpers used heavily by
-- Std.Ui's element / attribute lowering paths.  They're not bugs
-- per se (the cluster names contain `Coerce`-shaped tokens but the
-- emission is type-CORRECT — a typed-slice / typed-maybe narrow
-- through a single generic dispatch).  Future batches that close
-- coerceVia entry sites should drop these counts in lockstep with
-- the bare-`rt.Coerce[` cluster.  Counted here because both share
-- the same "narrowing" semantic the ratchet is targeting.
rtCoerceBaseline :: Map String Int
rtCoerceBaseline = Map.fromList
    [ ("rt.Coerce["     , 238)
    , ("rt.CoerceInt"   , 19)
    , ("rt.CoerceString", 82)
    , ("rt.CoerceBool"  , 17)
    , ("rt.CoerceFloat" , 22)
    , ("rt.TaskCoerceT" , 0)
    , ("rt.ResultCoerce", 0)
    , ("rt.MaybeCoerce" , 24)
    , ("rt.AsListT"     , 171)
    ]


-- | Total overall `rt.Coerce`-mentioning-line budget.  Forward
-- regression ceiling: if some future change makes 26-ui-showcase
-- emit MORE matching lines, the gate fails.  Ratchets DOWN in
-- lockstep with the per-cluster numbers above.
--
-- The bare-`rt.Coerce` substring is a superset (every typed fast
-- path matches it too), so this is the headline number a
-- maintainer reports as "rt.Coerce sites in the showcase".
-- Measured 2026-06-20 post-step-3: 317.
rtCoerceTotalBudget :: Int
rtCoerceTotalBudget = 317


-- | Resolve the example's main.go path. Cabal-test runs with the
-- compiler repo root as cwd; the showcase example's main.go is at
-- 'examples/26-ui-showcase/sky-out/main.go'.
showcaseMainGoPath :: IO FilePath
showcaseMainGoPath = do
    cwd <- getCurrentDirectory
    return (cwd </> "examples" </> "26-ui-showcase" </> "sky-out" </> "main.go")


-- | Resolve the locally-built sky binary path.
findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- | Clean-slate build of the showcase example. Wipes sky-out /
-- .skycache / .skydeps so a partial previous build doesn't mask a
-- coerce-count regression.  ~7 s clean-build on M-series Macs.
buildShowcase :: IO (Either String String)
buildShowcase = do
    sky <- findSky
    cwd <- getCurrentDirectory
    let dir = cwd </> "examples" </> "26-ui-showcase"
        buildCmd = "cd " ++ dir
                ++ " && rm -rf sky-out .skycache .skydeps 2>/dev/null; "
                ++ sky ++ " build src/Main.sky 2>&1"
    (ec, out, err) <- readCreateProcessWithExitCode (shell buildCmd) ""
    case ec of
        Exit.ExitSuccess   -> return (Right out)
        Exit.ExitFailure n ->
            return (Left ("sky build failed (exit " ++ show n
                          ++ "):\n" ++ out ++ err))


-- | Read the emitted main.go after the build.
readShowcaseMainGo :: IO String
readShowcaseMainGo = showcaseMainGoPath >>= readFile


-- | Count lines in `src` that contain `needle` — `grep -c` semantics:
-- a line with the needle TWICE still contributes 1.  Matches the
-- manual `grep -c <needle> main.go` invocation a maintainer runs
-- when ratcheting.
countMatchingLines :: String -> String -> Int
countMatchingLines needle src =
    length [ () | l <- lines src, needle `isInfixOf` l ]


-- | Run the per-cluster comparison.  Returns Nothing on success,
-- Just <descriptive failure> on regression so the it-clause emits a
-- single clear failure message naming every cluster that exceeded.
checkClusters :: String -> Maybe String
checkClusters src =
    let pairs    = Map.toAscList rtCoerceBaseline
        actuals  = [ (cluster, baseline, countMatchingLines cluster src)
                   | (cluster, baseline) <- pairs ]
        regrs    = [ (cluster, baseline, actual)
                   | (cluster, baseline, actual) <- actuals
                   , actual > baseline ]
    in case regrs of
        []  -> Nothing
        rs  -> Just $
            "rt.Coerce* cluster count regression:\n"
            ++ concatMap formatRegression rs
            ++ "\nFix one of:\n"
            ++ "  (a) Fix the new emission site (preferred — that's "
            ++ "why this gate exists);\n"
            ++ "  (b) If the new sites are PROVEN-CORRECT and "
            ++ "intentional, ratchet baseline UP in the same commit "
            ++ "in test/Sky/Build/RtCoerceBudgetSpec.hs with a "
            ++ "comment justifying the increase."
  where
    formatRegression (c, b, a) =
        "  " ++ pad 18 c ++ " baseline=" ++ show b
        ++ " actual=" ++ show a
        ++ " (over by " ++ show (a - b) ++ ")\n"
    pad n s = s ++ replicate (max 0 (n - length s)) ' '


-- | One-line summary report — useful in cabal test logs so a
-- maintainer ratcheting can see all current counts even on success.
clusterSummary :: String -> String
clusterSummary src =
    "rt.Coerce* per-cluster counts (baseline / actual):\n"
    ++ concatMap row (Map.toAscList rtCoerceBaseline)
    ++ "  " ++ pad 18 "TOTAL rt.Coerce" ++ "  "
    ++ pad 4 (show rtCoerceTotalBudget) ++ " / "
    ++ pad 4 (show (countMatchingLines "rt.Coerce" src)) ++ totalMark src ++ "\n"
  where
    row (c, b) =
        let a = countMatchingLines c src
            mark | a > b     = "  REGRESSION"
                 | a < b     = "  (ratchet down — update baseline!)"
                 | otherwise = "  (at floor)"
        in "  " ++ pad 18 c ++ "  " ++ pad 4 (show b) ++ " / "
           ++ pad 4 (show a) ++ mark ++ "\n"
    totalMark s =
        let a = countMatchingLines "rt.Coerce" s
        in if a > rtCoerceTotalBudget
           then "  REGRESSION"
           else if a < rtCoerceTotalBudget
                then "  (ratchet down — update rtCoerceTotalBudget!)"
                else "  (at floor)"
    pad n s = s ++ replicate (max 0 (n - length s)) ' '


spec :: Spec
spec = beforeAll buildAndRead $
    describe "Sky.Build.RtCoerceBudget — per-cluster ratchet-down gate" $ do

    it "26-ui-showcase clean build succeeds" $ \(buildOut, _) ->
        case buildOut of
            Right s  -> s `shouldSatisfy`
                (\out -> "Compilation successful" `isInfixOf` out)
            Left err -> expectationFailure err

    it "no rt.Coerce* cluster exceeds its hardcoded baseline" $
        \(_, src) -> do
            -- Emit summary for ratchet visibility (cabal test
            -- propagates the it-name + free-form msgs).
            putStrLn ""
            putStrLn (clusterSummary src)
            case checkClusters src of
                Nothing  -> return ()
                Just msg -> expectationFailure msg

    -- Total-budget ceiling: bare `rt.Coerce` matching-line count
    -- must not exceed the hardcoded total budget.  Distinct from
    -- the per-cluster gate because some future emission shape may
    -- not fall into any of the explicit clusters above (escape-
    -- valve regression catch).
    it "total rt.Coerce matching-line count does not exceed budget" $
        \(_, src) -> do
            let total = countMatchingLines "rt.Coerce" src
            total `shouldSatisfy` (<= rtCoerceTotalBudget)

  where
    buildAndRead :: IO (Either String String, String)
    buildAndRead = do
        outOrErr <- buildShowcase
        case outOrErr of
            Left err -> return (Left err, "")
            Right out -> do
                src <- readShowcaseMainGo
                return (Right out, src)
