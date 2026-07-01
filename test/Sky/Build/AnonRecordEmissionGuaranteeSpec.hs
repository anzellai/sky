module Sky.Build.AnonRecordEmissionGuaranteeSpec (spec) where

-- v0.17 step-1 gap-3 regression — anonymous-record emission
-- guarantee under SKY_GOSIG_DIFF differential mode.
--
-- BACKSTORY:
--
-- Pre-fix, the @imports@ thunk inside 'generateGoMulti' (Compile.hs
-- ~line 4978) issued
--   atomicWriteIORef globalAnonRecords Map.empty
-- as a defensive backup of the @resetCompileState@ wipe.  But the
-- thunk runs INSIDE @unsafePerformIO@ and its strictness is gated
-- by the @lowerCtx \`seq\` GoBuilder.renderPackage pkg@ chain.
--
-- Under @SKY_GOSIG_DIFF=1@ the probe block in 'solvePhase' forces
-- additional evaluation chains that (via Haskell laziness) can
-- end up forcing 'decls' rendering BEFORE the 'imports' thunk's
-- reset fires.  When that happens:
--
--   (1) decls render → 'synthAnonRecordName' fires
--       'atomicModifyIORef'' registering the Anon_R_… shape.
--   (2) 'imports' thunk runs late → 'atomicWriteIORef
--       globalAnonRecords Map.empty' WIPES the just-registered
--       shape.
--   (3) 'anonRecordDecls' = 'generateAnonRecordDecls' reads the
--       EMPTY registry → no @type Anon_R_… = struct{...}@ decl
--       is emitted.
--
-- Net effect: the cast token at the use-site (e.g. line 866 in
-- the iter18-debug fixture's emitted main.go) references
-- @Anon_R_rootAttrs_wrapperAttrs__5n085ahc@ but no declaration
-- is emitted, and @go build@ rejects with:
--   undefined: Anon_R_rootAttrs_wrapperAttrs__5n085ahc
--
-- FIX (Compile.hs ~line 4969):
--
-- Remove the redundant in-thunk reset.  'resetCompileState'
-- (Compile.hs:1287) already wipes 'globalAnonRecords' at
-- 'continueCompile' entry — strictly BEFORE any
-- 'synthAnonRecordName' registration site can fire.
-- Registrations from decl-rendering therefore survive to
-- 'generateAnonRecordDecls' read at module-end, regardless of
-- decl/imports thunk-force ordering.
--
-- THIS SPEC LOCKS THE FIX:
--
--   Mode-(i):  SKY_GOSIG_DIFF=1 build over the canonical
--              fixture succeeds (was the failing case).
--   Mode-(ii): Default-env build over the same fixture succeeds
--              AND its emitted main.go has the same anon-record
--              count as the SKY_GOSIG_DIFF=1 build (proof the
--              fix doesn't drop OR add decls — byte-stable
--              relative to environment toggle).
--   Mode-(iii): Render-order invariant — every Anon_R_<hash>
--              cast token in the emitted main.go has a matching
--              @type Anon_R_<hash> = struct{...}@ declaration.
--              Catches future regressions where someone adds a
--              lazy reset back into the imports thunk OR
--              introduces a hand-built Anon_R_ string that
--              bypasses 'synthAnonRecordName'.

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist, listDirectory)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..),
                      env)
import System.Environment (getEnvironment)
import System.Exit (ExitCode(..))
import Data.List (isInfixOf, nub, sort, isPrefixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
          else fail ("sky binary missing at " ++ candidate
                  ++ " — run cabal install --installdir=./sky-out first")


findFixtureDir :: IO FilePath
findFixtureDir = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "test" </> "fixtures" </> "anon-record-gosigdiff"
    ok <- doesFileExist (candidate </> "sky.toml")
    if ok then return candidate
          else fail ("anon-record-gosigdiff fixture missing at " ++ candidate)


runSky :: FilePath -> [String] -> FilePath -> [(String, String)] -> IO (ExitCode, String, String)
runSky sky args workDir extraEnv = do
    baseEnv <- getEnvironment
    -- Override / add SKY_GOSIG_DIFF (and any other extras) to the
    -- parent process env.  Later entries win on duplicate keys
    -- when System.Process passes the list to execve.
    let envList = filter (\(k, _) -> k `notElem` map fst extraEnv) baseEnv
                ++ extraEnv
    let cp = (proc sky args) { cwd = Just workDir, env = Just envList }
    readCreateProcessWithExitCode cp ""


prepareFixture :: FilePath -> FilePath -> IO ()
prepareFixture srcDir destDir = do
    createDirectoryIfMissing True (destDir </> "src")
    -- Top-level entries (sky.toml etc.)
    rootEntries <- listDirectory srcDir
    let rootFiles = [ e | e <- rootEntries
                    , e `notElem` [".", "..", "sky-out", ".skycache",
                                   ".skydeps", "src"] ]
    mapM_ (copyTop srcDir destDir) rootFiles
    -- src/ contents
    let srcSrc = srcDir </> "src"
    srcSrcExists <- doesFileExist (srcSrc </> "Main.sky")
    if srcSrcExists
      then do
        srcEntries <- listDirectory srcSrc
        mapM_ (copySrc srcSrc (destDir </> "src")) srcEntries
      else return ()
  where
    copyTop sd dd name = do
        contents <- readFile (sd </> name)
        writeFile (dd </> name) contents
    copySrc sd dd name
        | ".sky" `isInfixOf` name = do
            contents <- readFile (sd </> name)
            writeFile (dd </> name) contents
        | otherwise = return ()


-- Extract every "Anon_R_<hash>" token in the emitted Go source.
extractAnonTokens :: String -> [String]
extractAnonTokens = nub . sort . go
  where
    go [] = []
    go s
        | "Anon_R_" `isPrefixOf` s =
            let token = takeWhile isTokenChar s
                rest  = dropWhile isTokenChar s
            in token : go rest
        | otherwise = go (drop 1 s)
    isTokenChar c = c == '_' || (c >= 'a' && c <= 'z')
                              || (c >= 'A' && c <= 'Z')
                              || (c >= '0' && c <= '9')


-- Extract just the Anon_R_ names that have a `type Anon_R_… =` decl.
extractAnonDecls :: String -> [String]
extractAnonDecls src = nub . sort $
    [ takeWhile isTokenChar after
    | l <- lines src
    , let stripped = dropWhile (== ' ') l
    , "type Anon_R_" `isPrefixOf` stripped
    , let after = drop (length ("type " :: String)) stripped
    ]
  where
    isTokenChar c = c == '_' || (c >= 'a' && c <= 'z')
                              || (c >= 'A' && c <= 'Z')
                              || (c >= '0' && c <= '9')


spec :: Spec
spec = describe "gap-3 — anonymous-record emission survives SKY_GOSIG_DIFF" $ do

    it "mode-(i) SKY_GOSIG_DIFF=1 build succeeds (was failing pre-fix)" $
      withSystemTempDirectory "sky-gap3-diff1" $ \dir -> do
        fixture <- findFixtureDir
        prepareFixture fixture dir
        sky <- findSky
        (exit, stdout', stderr') <-
            runSky sky ["build", "src/Main.sky"] dir
                [("SKY_GOSIG_DIFF", "1")]
        let combined = stdout' ++ "\n" ++ stderr'
        -- Sanity: no "undefined: Anon_R_…" failure shape.
        (("undefined: Anon_R_") `isInfixOf` combined) `shouldBe` False
        ("Compilation successful" `isInfixOf` combined) `shouldBe` True
        exit `shouldBe` ExitSuccess

    it "mode-(ii) default-env build succeeds + emits same Anon_R_ shape count as DIFF=1" $
      withSystemTempDirectory "sky-gap3-mode2" $ \dir -> do
        fixture <- findFixtureDir
        prepareFixture fixture dir
        sky <- findSky

        -- Run #1: default env (SKY_GOSIG_DIFF unset).
        (exitA, stdoutA, stderrA) <-
            runSky sky ["build", "src/Main.sky"] dir []
        let combinedA = stdoutA ++ "\n" ++ stderrA
        ("Compilation successful" `isInfixOf` combinedA) `shouldBe` True
        exitA `shouldBe` ExitSuccess
        emittedA <- readFile (dir </> "sky-out" </> "main.go")
        let declCountA = length (extractAnonDecls emittedA)

        -- Wipe + re-run with SKY_GOSIG_DIFF=1.
        wipeBuild dir
        (exitB, stdoutB, stderrB) <-
            runSky sky ["build", "src/Main.sky"] dir
                [("SKY_GOSIG_DIFF", "1")]
        let combinedB = stdoutB ++ "\n" ++ stderrB
        ("Compilation successful" `isInfixOf` combinedB) `shouldBe` True
        exitB `shouldBe` ExitSuccess
        emittedB <- readFile (dir </> "sky-out" </> "main.go")
        let declCountB = length (extractAnonDecls emittedB)

        -- Mode-(ii) invariant: differential gate is non-destructive.
        declCountA `shouldBe` declCountB

    it "mode-(iii) render-order invariant — every Anon_R_ token has a matching type decl" $
      withSystemTempDirectory "sky-gap3-mode3" $ \dir -> do
        fixture <- findFixtureDir
        prepareFixture fixture dir
        sky <- findSky
        -- Run with the previously-problematic env so we exercise the
        -- worst-case ordering AND validate emission.
        (exit, _stdout, _stderr) <-
            runSky sky ["build", "src/Main.sky"] dir
                [("SKY_GOSIG_DIFF", "1")]
        exit `shouldBe` ExitSuccess
        emitted <- readFile (dir </> "sky-out" </> "main.go")
        let allTokens = extractAnonTokens emitted
            decls = extractAnonDecls emitted
            usedTokens = filter (`notElem` decls) allTokens
            -- Strip prefix of decls (e.g. tokens that ALSO appear
            -- on the declaration line — those are obviously OK).
            unmatched = [ t | t <- allTokens
                            , t `notElem` decls ]
        -- The invariant: every USED Anon_R_<hash> token must have
        -- a matching `type` declaration.  Otherwise go-build will
        -- reject with `undefined: Anon_R_…`.
        unmatched `shouldBe` []
        -- Sanity: the fixture genuinely exercises anon records
        -- (otherwise the spec would vacuously pass even if codegen
        -- removed all Anon_R_ shapes).
        (length allTokens >= 2) `shouldBe` True


wipeBuild :: FilePath -> IO ()
wipeBuild dir = do
    -- Best-effort wipe of generated dirs.  Use shell commands
    -- because System.Directory's recursive-remove doesn't
    -- support an "ignore-missing" mode and we want to be silent
    -- if nothing exists.
    _ <- readCreateProcessWithExitCode
            (proc "rm" ["-rf", dir </> "sky-out",
                              dir </> ".skycache",
                              dir </> ".skydeps"]) ""
    return ()
