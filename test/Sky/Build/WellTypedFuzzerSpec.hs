{-# LANGUAGE ScopedTypeVariables #-}

-- v0.17 step-2 — Subprocess-isolated well-typed Sky fuzzer.
--
-- Closes gap-4 of the v0.17 close umbrella:
--
--   "A property-based fuzzer exists that generates random
--    well-typed Sky programs and asserts `sky build && ./sky-out/app`
--    doesn't panic. Run for ≥10,000 iterations clean before close."
--
-- Why subprocess-isolated, not in-process:
--   bug #492 (globalReachableProgram unsafePerformIO CSE) means
--   in-process iterations contaminate each other via the very
--   IORefs gap-1 leaves alive. Subprocess isolation gives every
--   fuzz iteration a clean compiler-process state — so a soundness
--   regression cannot be masked by stale module-level state from
--   a previous iteration.
--
-- Two run tiers:
--   1. Default (`cabal test` without env): qcMaxSuccess = 100,
--      cheap dev gate. Each iter: write fixture → sky build → run.
--   2. Milestone (`SKY_FUZZ_FULL=1`): qcMaxSuccess = 10000 (the
--      gap-4 target). Run before tagging a release.
--
-- The default tier of 100 iterations is calibrated to keep total
-- spec wall-clock under ~3 minutes on a warm cabal cache (each
-- `sky build` + run is 1-2 s; serialised at 100 iters that's
-- 100-200 s). The full 10k-iter tier is opt-in via env var and
-- intended to run overnight or in the milestone-gated CI step.
--
-- Per-iteration assertions:
--   * `sky build` exits 0 within 10 s.
--   * `./sky-out/app` exits 0 within 10 s.
--   * Emitted `sky-out/main.go` contains no bare `T1`/`T2`/`T3`
--     identifier outside `[T<N> any]` brackets (the type-param
--     declaration form). Bare T1 in a value position is the
--     v0.17 T1-leak signature.
--   * No `Anon_R_<hash>` token appears without a matching
--     `type Anon_R_<hash>` declaration in the same file.
--   * No `// PROOF:` annotation that classifies as `Unknown`
--     (typed-codegen contract — a Sky value at a Go slot whose
--     proof reduces to Unknown means the typed pipeline gave up).

module Sky.Build.WellTypedFuzzerSpec (spec) where

import Test.Hspec
import Test.QuickCheck
    ( Property
    , Args(..)
    , forAllShrinkShow
    , ioProperty
    , stdArgs
    , quickCheckWithResult
    , Result(Success)
    , generate
    )
import qualified Test.QuickCheck.Property as QCProp
import System.Directory
    ( getCurrentDirectory
    , createDirectoryIfMissing
    , doesFileExist
    )
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Environment (lookupEnv)
import System.Process
    ( proc
    , CreateProcess(..)
    , ProcessHandle
    , createProcess
    , waitForProcess
    , terminateProcess
    , interruptProcessGroupOf
    , StdStream(..)
    )
import System.Exit (ExitCode(..))
import qualified System.IO as IO
import Control.Exception (SomeException, try)
import Control.Concurrent (forkIO, threadDelay, killThread)
import Control.Concurrent.MVar (newEmptyMVar, putMVar, takeMVar)
import Data.List (isInfixOf, isPrefixOf)
import Data.Maybe (isJust)
import qualified Data.Char as Char

import Sky.Build.WellTypedFuzzerGen
    ( Program(..)
    , ShapeCategory(..)
    , genProgram
    , genProgramOfCategory
    , renderProgram
    , categoryLabel
    )


-- ─── Spec entry point ──────────────────────────────────────────────


spec :: Spec
spec = describe "Sky.Build.WellTypedFuzzer (subprocess-isolated)" $ do
    it "well-typed generated programs build + run without panic" $ do
        sampleDir <- lookupEnv "SKY_FUZZ_SAMPLE_DIR"
        case sampleDir of
            Just d  -> writeSamples d 50
            Nothing -> do
                fullMode <- isJust <$> lookupEnv "SKY_FUZZ_FULL"
                let n | fullMode  = 10000
                      | otherwise = 100
                runFuzzer n


-- v0.17 step-2 — pre-flight sampler.  When SKY_FUZZ_SAMPLE_DIR is
-- set, the spec writes N representative programs to that directory
-- (rotating across the four categories) and exits successfully
-- without invoking `sky build`.  The `make fuzz-sample-50` target
-- consumes this hook for the 50-program human-review checkpoint
-- (step-2.d in the v0.17 plan).
writeSamples :: FilePath -> Int -> IO ()
writeSamples dir n = do
    createDirectoryIfMissing True dir
    let cats  = [minBound :: ShapeCategory .. maxBound]
        nCats = length cats
        per   = n `div` nCats
        leftover = n `mod` nCats
        counts = zipWith
            (\idx _ -> per + if idx < leftover then 1 else 0)
            [0 :: Int ..]
            cats
    sequence_
        [ writeOneSample dir cat i
        | (cat, count) <- zip cats counts
        , i <- [0 .. count - 1]
        ]
    putStrLn ("wrote " ++ show n ++ " sample programs to " ++ dir)
  where
    writeOneSample d cat i = do
        prog <- generate (genProgramOfCategory cat)
        let fname = d </> (categoryLabel cat ++ "-" ++ pad2 i ++ ".sky")
        writeFile fname (renderProgram prog)
    pad2 i = if i < 10 then '0' : show i else show i


-- ─── Driver ────────────────────────────────────────────────────────


runFuzzer :: Int -> IO ()
runFuzzer n = do
    sky <- findSky
    let args = stdArgs { maxSuccess = n
                       , maxSize    = 16
                       , maxShrinks = 0      -- shrinking spawns more
                                             -- subprocesses; not worth it
                       }
    res <- quickCheckWithResult args (fuzzerProperty sky)
    case res of
        Success {} -> return ()
        other      -> expectationFailure
            ("WellTypedFuzzer failed: " ++ show other)


fuzzerProperty :: FilePath -> Property
fuzzerProperty sky =
    forAllShrinkShow genProgram (const []) showProgram $ \prog ->
        ioProperty (runOneIteration sky prog)


showProgram :: Program -> String
showProgram p =
    "[" ++ categoryLabel (pCategory p) ++ "]\n" ++ renderProgram p


-- ─── Per-iteration check ──────────────────────────────────────────


runOneIteration :: FilePath -> Program -> IO QCProp.Result
runOneIteration sky prog = withSystemTempDirectory "sky-fuzz" $ \tmp -> do
    writeFixture tmp prog
    buildRes <- runWithTimeout 10 (proc sky ["build", "src/Main.sky"])
                                  { cwd = Just tmp }
    case buildRes of
        BuildOk -> do
            let appPath = tmp </> "sky-out" </> "app"
            exists <- doesFileExist appPath
            if not exists
                then return (qcFail prog
                       "sky build returned 0 but sky-out/app is missing")
                else do
                    runRes <- runWithTimeout 10 (proc appPath [])
                                                { cwd = Just tmp }
                    case runRes of
                        BuildOk -> goodCheckCodegen tmp prog
                        BuildFailed code out ->
                            return (qcFail prog
                              ("app exited " ++ show code ++ ":\n"
                                ++ truncated out))
                        BuildTimedOut ->
                            return (qcFail prog
                              "app exceeded 10s timeout")
                        BuildIoError e ->
                            return (qcFail prog
                              ("io error running app: " ++ e))
        BuildFailed code out ->
            return (qcFail prog
              ("sky build exited " ++ show code ++ ":\n"
                ++ truncated out))
        BuildTimedOut ->
            return (qcFail prog
              "sky build exceeded 10s timeout")
        BuildIoError e ->
            return (qcFail prog
              ("io error running sky build: " ++ e))


-- After a successful build+run we also inspect the emitted Go.
-- See module header for the three structural assertions.
goodCheckCodegen :: FilePath -> Program -> IO QCProp.Result
goodCheckCodegen tmp prog = do
    let mainGoPath = tmp </> "sky-out" </> "main.go"
    exists <- doesFileExist mainGoPath
    if not exists
        then return qcOk      -- no main.go to inspect = nothing to check.
        else do
            src <- readFile mainGoPath
            case classifyMainGo src of
                Nothing  -> return qcOk
                Just msg -> return (qcFail prog
                    ("codegen check: " ++ msg))


-- ─── Codegen assertions (operate on emitted main.go text) ──────────


classifyMainGo :: String -> Maybe String
classifyMainGo src
    | Just leak <- bareTNLeak src
        = Just ("bare T-var leak: " ++ leak)
    | Just orphan <- orphanAnonRecord src
        = Just ("orphan Anon_R_ reference: " ++ orphan)
    | unknownProofPresent src
        = Just "// PROOF: ... = Unknown annotation present"
    | otherwise = Nothing


-- v0.17 T1-leak signature: bare `T1` / `T2` / `T3` token appearing
-- in a value position. We approximate by:
--   * Splitting into whitespace-delimited tokens.
--   * Allowing T<N> only when it appears immediately after `[`
--     (Go generic param decl `[T1 any]`) or `,` inside a
--     bracketed list (`[T1 any, T2 any]`).
-- This is intentionally conservative — the canonical leak shape
-- (`var x T1` / `func(T1)` / `[]T1` / `T1{}`) trips here, while
-- the typed-decl shape (`[T1 any]`) is admitted.
bareTNLeak :: String -> Maybe String
bareTNLeak src =
    let needles = ["T1", "T2", "T3"]
        bads    = [ ln
                  | ln <- lines src
                  , any (lineHasBareTN ln) needles
                  ]
    in case bads of
        []    -> Nothing
        (b:_) -> Just (truncated b)


-- A line "has bare TN" iff TN appears as a standalone identifier
-- AND the surrounding context doesn't look like a generic
-- parameter declaration (`[T1 any]` / `[T1 any, T2 any]`).
lineHasBareTN :: String -> String -> Bool
lineHasBareTN ln tn =
    let occs = findOccurrences tn ln
    in any (isBareOccurrence ln tn) occs


findOccurrences :: String -> String -> [Int]
findOccurrences needle haystack = go 0 haystack
  where
    go _ [] = []
    go i s@(_:rest)
      | needle `isPrefixOf` s
          && atIdentBoundary s
              = i : go (i + length needle) (drop (length needle) s)
      | otherwise = go (i + 1) rest
    atIdentBoundary s =
        let nextCh = drop (length needle) s
        in case nextCh of
            []      -> True
            (c:_)   -> not (isIdentChar c)
    isIdentChar c = Char.isAlphaNum c || c == '_'


-- Is a T<N> occurrence at offset i in `ln` part of `[TN any]` or
-- `, TN any]` / `, TN any,`? If so it's a typed-decl, not a leak.
isBareOccurrence :: String -> String -> Int -> Bool
isBareOccurrence ln tn i =
    let pre  = take i ln
        post = drop (i + length tn) ln
        -- Trim trailing whitespace from `pre` then look at the
        -- final char.
        preTrim = reverse (dropWhile (== ' ') (reverse pre))
        -- Trim leading whitespace from `post`.
        postTrim = dropWhile (== ' ') post
        -- A "decl" context: `[T1 any` or `, T1 any`.
        prevCh = if null preTrim then '\0' else last preTrim
        followsBracketOrComma = prevCh == '[' || prevCh == ','
        followedByAny = "any" `isPrefixOf` postTrim
    in not (followsBracketOrComma && followedByAny)


-- Anonymous record: every `Anon_R_<hash>` token must be backed by a
-- matching `type Anon_R_<hash> ` declaration somewhere in the file.
orphanAnonRecord :: String -> Maybe String
orphanAnonRecord src =
    let allTokens = collectAnonTokens src
        decls     = collectAnonDecls src
        orphans   = [ t | t <- allTokens, t `notElem` decls ]
    in case orphans of
        []    -> Nothing
        (o:_) -> Just o


collectAnonTokens :: String -> [String]
collectAnonTokens =
    foldr collectLine [] . lines
  where
    collectLine ln acc = scan ln ++ acc
    scan [] = []
    scan s@(c:rest)
      | "Anon_R_" `isPrefixOf` s =
          let token = takeWhile (\ch -> Char.isAlphaNum ch || ch == '_') s
          in token : scan (drop (length token) s)
      | otherwise = scan rest


collectAnonDecls :: String -> [String]
collectAnonDecls src =
    [ token
    | ln <- lines src
    , "type Anon_R_" `isInfixOf` ln
    , let rest = drop (length ("type " :: String))
                      (dropWhile (/= 'A') ln)
          token = takeWhile (\ch -> Char.isAlphaNum ch || ch == '_') rest
    , not (null token)
    ]


-- Look for `// PROOF: ... = Unknown` style annotations.
unknownProofPresent :: String -> Bool
unknownProofPresent src =
    any (\ln -> "// PROOF:" `isInfixOf` ln && "Unknown" `isInfixOf` ln)
        (lines src)


-- ─── Subprocess runner with timeout ───────────────────────────────


data BuildResult
    = BuildOk
    | BuildFailed Int String
    | BuildTimedOut
    | BuildIoError String


runWithTimeout :: Int -> CreateProcess -> IO BuildResult
runWithTimeout secs cp = do
    let cp' = cp { std_out = CreatePipe, std_err = CreatePipe
                 , create_group = True
                 }
    result <- try (createProcess cp')
    let typed :: Either SomeException
                    ( Maybe IO.Handle
                    , Maybe IO.Handle
                    , Maybe IO.Handle
                    , ProcessHandle )
        typed = result
    case typed of
        Left e  -> return (BuildIoError (show e))
        Right (_, mOut, mErr, ph) -> do
            mv <- newEmptyMVar
            tid <- forkIO $ do
                exit <- waitForProcess ph
                putMVar mv (Right exit)
            timer <- forkIO $ do
                threadDelay (secs * 1000000)
                putMVar mv (Left ())
            outcome <- takeMVar mv
            case outcome of
                Right ec -> do
                    killThread timer
                    stdo <- maybe (return "") (\h -> readHandleSafe h) mOut
                    stde <- maybe (return "") (\h -> readHandleSafe h) mErr
                    case ec of
                        ExitSuccess   -> return BuildOk
                        ExitFailure c -> return (BuildFailed c (stdo ++ "\n" ++ stde))
                Left () -> do
                    -- Kill the process group so any
                    -- runtime-spawned children also die.
                    _ <- try (interruptProcessGroupOf ph)
                            :: IO (Either SomeException ())
                    _ <- try (terminateProcess ph)
                            :: IO (Either SomeException ())
                    killThread tid
                    return BuildTimedOut


readHandleSafe :: IO.Handle -> IO String
readHandleSafe h = do
    r <- try (readAndClose h) :: IO (Either SomeException String)
    case r of
        Left _  -> return ""
        Right s -> return s
  where
    readAndClose hdl = do
        s <- IO.hGetContents hdl
        length s `seq` return s


-- ─── Fixture writer ───────────────────────────────────────────────


writeFixture :: FilePath -> Program -> IO ()
writeFixture dir prog = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml") tomlBody
    writeFile (dir </> "src" </> "Main.sky") (renderProgram prog)
  where
    tomlBody = unlines
        [ "name = \"sky-fuzz\""
        , "version = \"0.0.0\""
        , "entry = \"src/Main.sky\""
        , ""
        , "[source]"
        , "root = \"src\""
        ]


-- ─── Sky binary locator ───────────────────────────────────────────


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
          else fail ("sky binary missing at " ++ candidate)


-- ─── QC result helpers ────────────────────────────────────────────


qcOk :: QCProp.Result
qcOk = QCProp.succeeded


qcFail :: Program -> String -> QCProp.Result
qcFail prog msg = QCProp.failed { QCProp.reason = labelled }
  where
    labelled = "[" ++ categoryLabel (pCategory prog) ++ "] " ++ msg
        ++ "\n--- program ---\n" ++ renderProgram prog


-- Truncate long subprocess output so failure messages stay
-- readable.
truncated :: String -> String
truncated s
    | length s <= 800 = s
    | otherwise       = take 800 s ++ "\n…[truncated]"
