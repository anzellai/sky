module Sky.Build.UiShowcaseRtCoerceClosedProofSpec (spec) where

-- v0.17 step-8 (#644) — rt.Coerce closed-proof gate.
--
-- The 26-ui-showcase example exercises every Std.Ui primitive
-- (~317 rt.Coerce sites at first build).  This spec rebuilds
-- the example, runs 'RtCoerceScanner' over the emitted main.go,
-- and asserts that EVERY rt.Coerce site carries a proof comment
-- in one of three categories:
--
--   * @// PROOF: FFI: ...@ — the typed-vs-any FFI boundary
--     (kernel return narrowing, stdlib ADT constructor widening,
--     generic type-param widening, function-typed adapter,
--     SkyMaybe / SkyResult / SkyTask cross-instantiation).
--
--   * @// PROOF: JSON-narrow: ...@ — JSON decoder → typed value.
--     Doesn't fire in 26-ui-showcase but the classifier is
--     forward-compatible.
--
--   * @// PROOF: DB-narrow: ...@ — Db.query row → typed record.
--     Same forward-compat note.
--
-- The gate is a CLOSED proof, not a soft ≤20 floor: zero
-- unproven sites permitted.  If a new rt.Coerce shape escapes
-- 'annotateRtCoerceSites' classifier in Compile.hs (because a
-- new construction-site emits a previously-unrecognised shape),
-- the test fails with a clear regression hint pointing back to
-- the post-pass + scanner pair.

import Test.Hspec
import qualified System.Exit as Exit
import System.Directory (getCurrentDirectory, doesFileExist)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import Data.List (isInfixOf)

import Sky.Build.RtCoerceScanner
    ( Site (..)
    , scanCoerceSites
    , unprovenSites
    )


-- | Resolve the example's main.go path. The cabal test runs with
-- the compiler repo root as cwd, so 'examples/26-ui-showcase/
-- sky-out/main.go' is the lookup.
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


-- | Build the showcase example so the test isn't sensitive to a
-- stale main.go.  Uses the locally-installed sky binary in
-- sky-out/.  Idempotent in practice (sky build is incremental).
buildShowcase :: IO (Either String String)
buildShowcase = do
    sky <- findSky
    cwd <- getCurrentDirectory
    let dir = cwd </> "examples" </> "26-ui-showcase"
        -- Wipe stale caches so a partial previous build doesn't
        -- mask a regression. The example is ~7 s clean-build.
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


spec :: Spec
spec = beforeAll buildAndRead $
    describe "Sky.Build.UiShowcaseRtCoerceClosedProofSpec — rt.Coerce closed proof" $ do

    it "26-ui-showcase build succeeds" $ \(buildOut, _) ->
        case buildOut of
            Right s  -> s `shouldSatisfy`
                (\out -> "Compilation successful" `isInfixOf` out)
            Left err -> expectationFailure err

    it "every rt.Coerce site carries a proof comment (closed proof, zero floor)" $
        \(_, src) -> do
            let sites      = scanCoerceSites src
                unproven   = unprovenSites src
                totalSites = length sites
                bad        = length unproven
            -- Sanity floor: 26-ui-showcase MUST exercise at least
            -- 100 rt.Coerce sites (was 317 at v0.17 step-8 start).
            -- If this drops below 100 it likely means the example
            -- regressed (Std.Ui surface shrank) or the post-pass
            -- inadvertently rewrote the tokens.
            totalSites `shouldSatisfy` (>= 100)
            -- The closed-proof gate: bad must be 0.
            if bad == 0
                then return ()
                else expectationFailure $
                    "Found " ++ show bad ++ " unproven rt.Coerce sites "
                    ++ "(of " ++ show totalSites ++ " total). "
                    ++ "First 5:\n"
                    ++ unlines (map formatSite (take 5 unproven))
                    ++ "\nFix: extend annotateRtCoerceSites in "
                    ++ "src/Sky/Build/Compile.hs to classify the "
                    ++ "new construction-site, or add an inline "
                    ++ "/* PROOF: ... */ at the construction site."

    it "every proof comment uses one of three categories" $ \(_, src) -> do
        let allLines = lines src
            proofLines = filter ("PROOF:" `isInfixOf`) allLines
            valid l = any (`isInfixOf` l)
                [ "PROOF: FFI:"
                , "PROOF: JSON-narrow:"
                , "PROOF: DB-narrow:"
                ]
            invalid = filter (not . valid) proofLines
        invalid `shouldBe` []

  where
    buildAndRead :: IO (Either String String, String)
    buildAndRead = do
        outOrErr <- buildShowcase
        case outOrErr of
            Left err -> return (Left err, "")
            Right out -> do
                src <- readShowcaseMainGo
                return (Right out, src)


formatSite :: Site -> String
formatSite s =
    "  line " ++ show (siteLine s)
    ++ " col " ++ show (siteColumn s)
    ++ " token=" ++ siteToken s
    ++ "\n    line: " ++ truncated (siteLineText s)
    ++ "\n    prev: " ++ truncated (sitePrevText s)
  where
    truncated x
        | length x <= 120 = x
        | otherwise       = take 117 x ++ "..."
