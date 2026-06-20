{-# LANGUAGE TemplateHaskell #-}
{-# LANGUAGE OverloadedStrings #-}
{-# LANGUAGE CPP #-}
module Main where

import Options.Applicative
import System.Exit (exitFailure, exitSuccess, ExitCode(..))
import qualified Data.Version
import qualified Paths_sky_compiler
import System.IO (hPutStr, hPutStrLn, stderr)

import qualified System.Directory
import qualified System.Environment
import qualified Language.Haskell.TH.Syntax
import System.Directory (createDirectoryIfMissing, doesFileExist, removeFile, renameFile, getCurrentDirectory)
import System.IO.Error (catchIOError)
import qualified Control.Concurrent
import qualified Control.Exception
import qualified GHC.IO.Encoding
#ifndef mingw32_HOST_OS
import qualified System.Posix.Signals as Signals
#endif
import System.FilePath ((</>), takeExtension, takeDirectory, takeFileName, dropExtension, splitDirectories)
import System.Exit (exitWith)
import Data.List (isPrefixOf, isInfixOf, stripPrefix, tails)
import System.Process (callProcess, rawSystem)
import qualified System.Process
import qualified System.IO.Temp
import qualified System.Timeout
import qualified System.Exit
import Control.Monad (when)
import Data.FileEmbed (embedStringFile)
import qualified System.Info

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Data.Aeson as Aeson
import Data.Aeson ((.:), (.:?), (.!=))
import qualified Data.ByteString as B
import qualified Data.ByteString.Lazy as BL
import qualified Data.Text as T
import qualified Data.Text.IO as TIO
import qualified Sky.Build.Compile as Compile
import qualified Sky.Sky.Toml as Toml
import qualified Sky.Parse.Module as ParseMod
import qualified Sky.Format.Format as Format
import qualified Sky.Lsp.Server as Lsp
import qualified Sky.Build.FfiGen as FfiGen
import qualified Sky.Build.SkyDeps as SkyDeps
import qualified Sky.Build.Validator as Validator
import qualified Sky.Reporting.Render as Render
import qualified Sky.Cli.Watch as Watch
import qualified Sky.Cli.Doctor as Doctor
import qualified Sky.Doc.Index as DocIdx
import qualified Sky.Doc.Terminal as DocTerm
import qualified Sky.Doc.Render as DocRender
import qualified Sky.Build.EmbeddedDocServer

import qualified Control.Concurrent.Async as Async
import qualified Control.Concurrent.QSem as QSem
import qualified GHC.Conc as GHC


-- | End-to-end verification (replaces scripts/verify-examples.sh +
-- scripts/check-forbidden.sh). Returns True iff everything passed.
--
-- Stages:
--   1. Forbidden-pattern gate across src\/, sky-stdlib\/, and every
--      examples\/\*\/src\/ tree (rejects Result String, Task String,
--      Std.IoError, RemoteData).
--   2. Build + run every example (or the one named via `target`).
--      Panics in stderr / non-zero exit / non-2xx HTTP → fail.
runVerify :: Maybe String -> IO Bool
runVerify target = do
    cwd <- System.Directory.getCurrentDirectory
    forbiddenOk <- case target of
        Just _  -> return True   -- per-example run skips the global gate
        Nothing -> checkForbidden cwd
    when (not forbiddenOk) $
        hPutStrLn stderr "verify: forbidden-pattern gate failed"
    exampleOk <- runExampleVerify cwd target
    return (forbiddenOk && exampleOk)


-- | Grep gate for pre-v1 error-surface patterns. Fails the verify
-- run if any non-comment line in the Sky sources matches. Mirrors
-- test/Sky/ErrorUnificationSpec.hs for quick local runs.
checkForbidden :: FilePath -> IO Bool
checkForbidden cwd = do
    let patterns =
            [ ("Result String",  "Result[[:space:]]+String[[:space:]]")
            , ("Task String",    "Task[[:space:]]+String[[:space:]]")
            , ("Std.IoError",    "Std\\.IoError")
            , ("RemoteData",     "\\bRemoteData\\b")
            ]
        roots = ["src", "sky-stdlib"] ++ [cwd ++ "/examples"]
    results <- mapM (checkOne roots) patterns
    let fails = [ label | (label, False) <- zip (map fst patterns) results ]
    mapM_ (\l -> hPutStrLn stderr $ "  FORBIDDEN " ++ l) fails
    return (null fails)
  where
    checkOne _roots (_label, pat) = do
        (_ec, out, _) <- System.Process.readProcessWithExitCode "sh"
            [ "-c"
            , unwords
                [ "grep -rn --include='*.sky'"
                , "--exclude-dir=.skycache --exclude-dir=.skydeps --exclude-dir=sky-out"
                , shellQuote pat
                , shellQuote cwd ++ "/src"
                , shellQuote cwd ++ "/sky-stdlib"
                , shellQuote cwd ++ "/examples"
                , "2>/dev/null | grep -vE '^[^:]*:[0-9]+:[[:space:]]*--' | head -5"
                ]
            ] ""
        -- `out` is the filtered grep output (excluding comment-only lines).
        -- True = no matches = pass.
        return (null (filter (not . null) (lines out)))


shellQuote :: String -> String
shellQuote s = "'" ++ concatMap esc s ++ "'"
  where esc '\'' = "'\\''"; esc c = [c]

-- | Run a command in an explicit working dir with an ARGUMENT VECTOR (no shell),
-- mirroring callProcess's "throw on non-zero exit" semantics. Used instead of
-- @callProcess "sh" ["-c", "cd <dir> && <cmd> " ++ value]@ so a caller-supplied
-- package / dependency name can never be interpreted by a shell.
callProcessIn :: FilePath -> FilePath -> [String] -> IO ()
callProcessIn dir cmd args = do
    (_, _, _, ph) <- System.Process.createProcess
        (System.Process.proc cmd args) { System.Process.cwd = Just dir }
    ec <- System.Process.waitForProcess ph
    case ec of
        ExitSuccess   -> return ()
        ExitFailure n -> ioError (userError (cmd ++ " (in " ++ dir ++ ") exited " ++ show n))


-- | Build + runtime-probe each example. Classification mirrors the
-- original scripts/example-sweep.sh: server / gui / cli. Failure
-- modes: build-fail, non-zero exit, panic in log, non-2xx HTTP.
runExampleVerify :: FilePath -> Maybe String -> IO Bool
runExampleVerify cwd target = do
    let examplesDir = cwd ++ "/examples"
    hasDir <- System.Directory.doesDirectoryExist examplesDir
    if not hasDir
        then do
            hPutStrLn stderr "verify: no examples/ directory"
            return True
        else do
            entries <- System.Directory.listDirectory examplesDir
            let dirs = case target of
                    Just t  -> filter (== t) entries
                    Nothing -> entries
                exampleDirs = [examplesDir ++ "/" ++ d | d <- dirs]
            -- Clean the failure log before running so stale entries from
            -- a prior invocation (e.g. cabal test → sky verify in the
            -- same CI job) don't cause a false exit-code-1.
            removeFile "/tmp/sky-verify-fails.txt"
                `catchIOError` (\_ -> return ())
            mapM_ (verifyOne cwd) exampleDirs
            hasFailures <- readFile "/tmp/sky-verify-fails.txt"
                `catchIOError` (\_ -> return "")
            return (null (filter (not . null) (lines hasFailures)))


-- | Verify one example. Writes any failure reason to
-- /tmp/sky-verify-fails.txt (append). Uses the same shell primitives
-- the prior scripts/verify-examples.sh relied on — sky build, exec,
-- curl probe — now orchestrated from Haskell so the one-binary
-- contract holds.
verifyOne :: FilePath -> FilePath -> IO ()
verifyOne cwd dir = do
    let name = takeFileName dir
        tomlPath = dir </> "sky.toml"
        logPath  = "/tmp/sky-verify-" ++ name ++ ".log"
    hasToml <- doesFileExist tomlPath
    if not hasToml then return () else do
        -- Audit P3-1: Fyne GUI example needs GTK / Cocoa dev libs
        -- at link time. Headless Linux CI (GitHub Actions ubuntu-latest)
        -- doesn't ship them, so the `go build` step fails even if
        -- the Sky-level code compiles cleanly. Skip the whole verify
        -- step on Linux for any GUI example by default; the SKY_SKIP_GUI=0
        -- override lets a GUI-capable runner still exercise it.
        skipGui <- shouldSkipGui name
        if skipGui
            then putStrLn $ "  [skip] " ++ name ++ ": GUI example on Linux (set SKY_SKIP_GUI=0 to run)"
            else do
                -- Clean build. Preserve `.skycache/ffi/` (FFI bindings —
                -- regenerating skyshop's Stripe + Firebase takes 15+ min
                -- of `sky-ffi-inspect` per run) and `.skydeps/` (Sky
                -- package lockfile). Compiler invalidates `ffi/`
                -- entries on upstream Go module change via content hash,
                -- so keeping them between sweeps is safe.
                _ <- System.Process.readProcessWithExitCode "sh"
                    [ "-c"
                    , unwords
                        [ "cd", shellQuote dir, "&&"
                        , "rm -rf sky-out .skycache/lowered .skycache/go", "&&"
                        , shellQuote (cwd ++ "/sky-out/sky"), "build src/Main.sky"
                        , ">", shellQuote logPath, "2>&1"
                        ]
                    ] ""
                let bin = dir </> "sky-out" </> "app"
                hasBin <- doesFileExist bin
                if not hasBin
                    then do
                        putStrLn $ "  FAIL build: " ++ name
                        appendFile "/tmp/sky-verify-fails.txt" (name ++ ":build\n")
                    else classifyAndRun cwd name dir bin logPath


-- shouldSkipGui: true only when this is a GUI example AND
-- SKY_SKIP_GUI is unset or "1" AND we're on Linux (darwin has
-- Cocoa so Fyne builds there).
shouldSkipGui :: String -> IO Bool
shouldSkipGui name
    | not (isGui name) = return False
    | otherwise = do
        skipEnv <- System.Environment.lookupEnv "SKY_SKIP_GUI"
        case skipEnv of
            Just "0" -> return False
            _        -> do
                (_, uname, _) <- System.Process.readProcessWithExitCode
                    "uname" ["-s"] ""
                let sys = takeWhile (/= '\n') uname
                return (sys == "Linux")


classifyAndRun :: FilePath -> String -> FilePath -> FilePath -> FilePath -> IO ()
classifyAndRun _cwd name dir bin logPath = do
    isSrv <- isServerExample dir
    if isGui name
      then putStrLn $ "  gui skipped runtime: " ++ name
      else if isSrv
        then do
            port <- readPortFromToml (dir </> "sky.toml")
            -- Audit P2-4: per-example scenario file. If
            -- examples/<n>/verify.json exists, run each listed request
            -- and assert status + body-substring. Otherwise fall back
            -- to the single GET / probe.
            let scenarioPath = dir </> "verify.json"
            hasScenario <- doesFileExist scenarioPath
            if hasScenario
                then runScenario name dir logPath port scenarioPath
                else runDefaultProbe name dir logPath port
        else do
            -- CLI example: run; panic / non-zero exit / no-exit = fail.
            -- The wait is TIMEOUT-BOUNDED (60s): a CLI example that
            -- doesn't terminate is itself a bug — an infinite loop, or
            -- a server example `isServerExample` somehow missed. Before
            -- this bound the wait was an unbounded
            -- `readProcessWithExitCode`; when the hardcoded `isServer`
            -- list rotted (19-skyforum was never added to it) verify
            -- spawned skyforum's never-exiting Sky.Live server here and
            -- waited forever, wedging the whole verify run AND the
            -- `cabal test` ExampleSweep/VerifyAll specs with it.
            mResult <- System.Timeout.timeout (60 * 1000000) $
                System.Process.readProcessWithExitCode "sh"
                    [ "-c"
                    , "cd " ++ shellQuote dir ++ " && ./sky-out/app > "
                        ++ shellQuote logPath ++ " 2>&1"
                    ] ""
            case mResult of
                Nothing -> do
                    -- The timed-out `sh` is torn down by readProcess's
                    -- exception cleanup, but its `./sky-out/app` child
                    -- was a separate fork — kill that orphan explicitly.
                    _ <- System.Process.readProcessWithExitCode "sh"
                        [ "-c"
                        , "pkill -f " ++ shellQuote (dir </> "sky-out" </> "app")
                            ++ " 2>/dev/null; true"
                        ] ""
                    putStrLn $ "  FAIL timeout (>60s, did not exit): " ++ name
                    appendFile "/tmp/sky-verify-fails.txt" (name ++ ":timeout\n")
                Just (ec, _, _) -> do
                    hasPanic <- hasPanicIn logPath
                    case (ec, hasPanic) of
                        (_, True) -> do
                            putStrLn $ "  FAIL panic: " ++ name
                            appendFile "/tmp/sky-verify-fails.txt" (name ++ ":panic\n")
                        (System.Exit.ExitFailure n, _) -> do
                            putStrLn $ "  FAIL exit " ++ show n ++ ": " ++ name
                            appendFile "/tmp/sky-verify-fails.txt" (name ++ ":exit\n")
                        _ -> do
                            -- expected.txt comparison if the file exists.
                            let expected = dir </> "expected.txt"
                            hasExpected <- doesFileExist expected
                            if hasExpected
                                then do
                                    want <- readFile expected
                                    got  <- readFile logPath
                                    if want == got
                                        then putStrLn $ "  runtime ok: " ++ name
                                        else do
                                            putStrLn $ "  FAIL expected.txt mismatch: " ++ name
                                            appendFile "/tmp/sky-verify-fails.txt" (name ++ ":expected\n")
                                else putStrLn $ "  runtime ok: " ++ name


-- Audit P2-4: scenario-driven server example verification.
--
-- verify.json shape:
--   { "requests":
--       [ { "method": "GET",  "path": "/",           "expectStatus": 200,
--           "expectBody": ["Welcome"]                                  }
--       , { "method": "POST", "path": "/api/echo",   "body": "hi",
--           "expectStatus": 200, "expectBody": ["hi"]                  }
--       ]
--   }
--
-- Any failing request (status mismatch, missing body substring,
-- panic log line) fails the whole example and appends an entry to
-- /tmp/sky-verify-fails.txt. This replaces the "just check HTTP
-- 200 on /" smoke test which would pass a handler that returns
-- an empty 200 response — surfacing the bug class the audit
-- identified as M6.
runScenario :: String -> FilePath -> FilePath -> Int -> FilePath -> IO ()
runScenario name dir logPath port scenarioPath = do
    -- Pre-flight: kill any process holding our port. Without this,
    -- a stale `sky-out/app` from a prior session (or another
    -- example running in the background) silently steals the
    -- bind, our spawn no-ops, and the scenario probes hit the
    -- WRONG server's responses. Verify reports "FAIL scenario:
    -- body missing X" while the actual app under test never
    -- started. (See CLAUDE.md "verify port-collision hardening".)
    killPortHolder port
    raw <- B.readFile scenarioPath
    case Aeson.eitherDecode (BL.fromStrict raw) of
        Left err -> do
            putStrLn $ "  FAIL scenario parse: " ++ name ++ " (" ++ err ++ ")"
            appendFile "/tmp/sky-verify-fails.txt" (name ++ ":scenario-parse\n")
        Right scenario -> do
            -- Start the server with a dedicated spawn so we can
            -- run multiple requests against it. Matches the
            -- existing default-probe shell shape so the panic
            -- detector below keeps finding its markers.
            let serverCmd = unwords
                    [ "(cd", shellQuote dir, "&& exec ./sky-out/app) >"
                    , shellQuote logPath, "2>&1 &"
                    , "echo $!"
                    ]
            (_, pidTxt, _) <- System.Process.readProcessWithExitCode "sh"
                ["-c", serverCmd] ""
            let pid = takeWhile (\c -> c /= '\n' && c /= ' ') pidTxt
            -- Wait for the server to come up. Probes the port AND
            -- confirms the listener PID matches OUR spawn — guards
            -- against a stale process from a different example
            -- having the port and serving unrelated responses.
            _ <- System.Process.readProcessWithExitCode "sh"
                ["-c", unwords
                    [ "for i in $(seq 1 20); do"
                    , "  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 1"
                    , "    'http://localhost:" ++ show port ++ "/' 2>/dev/null);"
                    , "  case \"$code\" in 2??|3??|4??) break;; esac;"
                    , "  sleep 0.5;"
                    , "done"
                    ]] ""
            -- Identity check: lsof -ti :PORT should return our pid.
            -- If it returns a different pid, kill that imposter and
            -- retry our spawn. Catches the rare race where a
            -- pre-existing server bound the port between our pre-
            -- flight kill and our spawn (or where a parallel test
            -- in the same process beat us to bind).
            (_, ownerTxt, _) <- System.Process.readProcessWithExitCode "sh"
                ["-c", "lsof -ti :" ++ show port ++ " 2>/dev/null | head -1"] ""
            let owner = takeWhile (\c -> c /= '\n' && c /= ' ') ownerTxt
            when (not (null owner) && owner /= pid) $ do
                -- Imposter on our port. Kill + respawn.
                _ <- System.Process.readProcessWithExitCode "sh"
                    ["-c", "kill " ++ owner ++ " 2>/dev/null; sleep 0.3;"
                            ++ "kill -9 " ++ owner ++ " 2>/dev/null; sleep 0.2"]
                    ""
                -- Re-spawn (server we started already noticed bind
                -- failure; spawn a fresh one).
                _ <- System.Process.readProcessWithExitCode "sh"
                    ["-c", serverCmd] ""
                _ <- System.Process.readProcessWithExitCode "sh"
                    ["-c", unwords
                        [ "for i in $(seq 1 20); do"
                        , "  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 1"
                        , "    'http://localhost:" ++ show port ++ "/' 2>/dev/null);"
                        , "  case \"$code\" in 2??|3??|4??) break;; esac;"
                        , "  sleep 0.5;"
                        , "done"
                        ]] ""
                return ()
            -- Run each scenario request, collecting failures.
            failures <- mapM (runScenarioRequest port) (scenarioRequests scenario)
            -- Stop the server.
            _ <- System.Process.readProcessWithExitCode "sh"
                ["-c", "kill " ++ pid ++ " 2>/dev/null; wait " ++ pid ++ " 2>/dev/null"]
                ""
            panicked <- hasPanicIn logPath
            case (panicked, concat failures) of
                (True, _) -> do
                    putStrLn $ "  FAIL panic: " ++ name
                    appendFile "/tmp/sky-verify-fails.txt" (name ++ ":panic\n")
                (False, []) ->
                    putStrLn $ "  runtime ok: " ++ name ++ " (scenario: "
                        ++ show (length (scenarioRequests scenario)) ++ " requests)"
                (False, reasons) -> do
                    mapM_ (\r -> putStrLn $ "  FAIL scenario: " ++ name ++ ": " ++ r) reasons
                    appendFile "/tmp/sky-verify-fails.txt" (name ++ ":scenario\n")


runDefaultProbe :: String -> FilePath -> FilePath -> Int -> IO ()
runDefaultProbe name dir logPath port = do
    -- Same port-collision pre-flight as runScenario.
    killPortHolder port
    (_, stdoutTxt, _) <- System.Process.readProcessWithExitCode "sh"
        [ "-c"
        , unwords
            [ "(cd", shellQuote dir, "&& exec ./sky-out/app) >", shellQuote logPath, "2>&1 &"
            , "pid=$!;"
            , "tries=0; code=000;"
            , "while [ $tries -lt 20 ]; do"
            , "  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 1 'http://localhost:" ++ show port ++ "/' 2>/dev/null);"
            , "  case \"$code\" in 2??|3??) break;; esac;"
            , "  sleep 0.5; tries=$((tries+1));"
            , "done;"
            , "kill $pid 2>/dev/null; wait $pid 2>/dev/null;"
            , "if grep -Eq 'panic:|runtime error:|\\[sky\\.live\\] panic|\\[sky\\.http\\] panic' " ++ shellQuote logPath ++ "; then"
            , "  printf '%s\\n' '  FAIL panic: " ++ name ++ "'; echo " ++ shellQuote (name ++ ":panic") ++ " >> /tmp/sky-verify-fails.txt;"
            , "elif echo \"$code\" | grep -Eq '^(2|3)[0-9][0-9]$'; then"
            , "  printf '%s\\n' \"  runtime ok: " ++ name ++ " (http $code)\";"
            , "else"
            , "  printf '%s\\n' \"  FAIL http$code: " ++ name ++ "\"; echo " ++ shellQuote (name ++ ":http") ++ " >> /tmp/sky-verify-fails.txt;"
            , "fi"
            ]
        ] ""
    putStr stdoutTxt


-- | Kill anything listening on `port`, then briefly wait for the
-- socket to drain. Used by `sky verify` before launching the
-- example's server so a stale process from a prior session can't
-- steal the port and serve unrelated responses to our probes.
-- macOS uses `lsof -ti :PORT`; Linux uses the same with
-- `-w` to suppress warnings. Either way: best-effort, silent.
killPortHolder :: Int -> IO ()
killPortHolder port = do
    let cmd = "pids=$(lsof -ti :" ++ show port ++ " 2>/dev/null);"
           ++ " if [ -n \"$pids\" ]; then"
           ++ "   kill $pids 2>/dev/null; sleep 0.3;"
           ++ "   pids=$(lsof -ti :" ++ show port ++ " 2>/dev/null);"
           ++ "   [ -n \"$pids\" ] && kill -9 $pids 2>/dev/null;"
           ++ "   sleep 0.2;"
           ++ " fi; true"
    _ <- System.Process.readProcessWithExitCode "sh" ["-c", cmd] ""
    return ()


-- One scenario request; returns [] on success, [reason] on failure.
runScenarioRequest :: Int -> ScenarioRequest -> IO [String]
runScenarioRequest port req = do
    let url = "http://localhost:" ++ show port ++ srPath req
        method = srMethod req
        -- Write response body to a per-request temp file so
        -- subsequent reads can't be clobbered by a background
        -- process. /tmp is fine — we're bounded to the verify run.
        respFile = "/tmp/sky-verify-resp-"
                   ++ filter (\c -> c /= '/' && c /= ' ') (method ++ srPath req)
        bodyArgs = case srBody req of
            Just b  -> ["--data", b]
            Nothing -> []
        -- Argument vector — no shell, so method / url / body are never
        -- shell-parsed (a scenario field can't inject a command).
        curlArgs = [ "-s", "-o", respFile, "-w", "%{http_code}", "--max-time", "5"
                   , "-X", method ] ++ bodyArgs ++ [url]
    (_, codeOut, _) <- System.Process.readProcessWithExitCode "curl" curlArgs ""
    let code = takeWhile (/= '\n') (dropWhile (== ' ') codeOut)
    body <- readFile respFile
    let statusReasons = case srExpectStatus req of
            Just expected | show expected /= code ->
                [method ++ " " ++ srPath req ++ ": got status " ++ code
                    ++ ", expected " ++ show expected]
            _ -> []
        bodyReasons =
            [ method ++ " " ++ srPath req ++ ": body missing substring " ++ show sub
            | sub <- srExpectBody req
            , not (sub `isSubstringOf` body)
            ]
    return (statusReasons ++ bodyReasons)


data Scenario = Scenario { scenarioRequests :: [ScenarioRequest] }
data ScenarioRequest = ScenarioRequest
    { srMethod       :: String
    , srPath         :: String
    , srBody         :: Maybe String
    , srExpectStatus :: Maybe Int
    , srExpectBody   :: [String]
    }


instance Aeson.FromJSON Scenario where
    parseJSON = Aeson.withObject "Scenario" $ \o ->
        Scenario <$> o .: "requests"

instance Aeson.FromJSON ScenarioRequest where
    parseJSON = Aeson.withObject "ScenarioRequest" $ \o -> do
        m  <- o .:? "method"      .!= ("GET" :: String)
        p  <- o .:  "path"
        b  <- o .:? "body"
        es <- o .:? "expectStatus"
        eb <- o .:? "expectBody"  .!= ([] :: [String])
        return (ScenarioRequest m p b es eb)


hasPanicIn :: FilePath -> IO Bool
hasPanicIn path = do
    exists <- doesFileExist path
    if not exists then return False else do
        content <- readFile path
        return ("panic:" `isPrefixOf` dropWhile (/= '\n') content
                || "panic:" `isSubstringOf` content)


isSubstringOf :: String -> String -> Bool
isSubstringOf needle hay = any (isPrefixOf needle) (tails hay)


readPortFromToml :: FilePath -> IO Int
readPortFromToml path = do
    src <- readFile path
    let ls = [ dropWhile (\c -> c == ' ' || c == '=') (drop 4 l)
             | l <- lines src
             , "port" `isPrefixOf` l
             ]
        digits = filter (`elem` ['0'..'9']) (concat ls)
    return (if null digits then 8000 else read digits)


-- | Classify an example as a long-lived "server" (Sky.Live,
-- Sky.Http.Server, or a raw gorilla/mux + net/http listener) by
-- SOURCE CONTENT — not a hardcoded name list. The previous
-- hardcoded `isServer` list silently rotted when 19-skyforum was
-- added: `sky verify` misclassified it as a CLI example and did an
-- unbounded process-wait on the never-exiting Sky.Live server,
-- wedging the whole verify run (and `cabal test`) forever. A
-- content scan can't rot — a new server example is detected the
-- moment its source lands.
isServerExample :: FilePath -> IO Bool
isServerExample dir = do
    let srcDir = dir </> "src"
    hasSrc <- System.Directory.doesDirectoryExist srcDir
    if not hasSrc
        then return False
        else do
            files <- listSkyFilesRec srcDir
            blobs <- mapM readFileSafe files
            let blob = concat blobs
            return $ any (`isInfixOf` blob)
                [ "Live.app", "Server.listen"
                , "import Sky.Http.Server", "import Std.Live"
                , "listenAndServe", "ListenAndServe"
                , "Gorilla.Mux", "gorilla/mux"
                ]
  where
    readFileSafe f = readFile f `catchIOError` (\_ -> return "")


-- | Recursively list every `.sky` file under a directory.
listSkyFilesRec :: FilePath -> IO [FilePath]
listSkyFilesRec dir = do
    entries <- System.Directory.listDirectory dir
    nested <- mapM
        (\e -> do
            let p = dir </> e
            isDir <- System.Directory.doesDirectoryExist p
            if isDir
                then listSkyFilesRec p
                else return [ p | takeExtension p == ".sky" ])
        entries
    return (concat nested)


isGui :: String -> Bool
isGui n = n == "11-fyne-stopwatch"


-- | Derive a dotted Sky module name from a source file path. The
-- path is expected to be absolute; we peel off the source root
-- (`<cwd>/src/` or `<cwd>/tests/`) and translate `/` → `.`, dropping
-- the `.sky` extension. Returns Nothing for files outside those
-- roots so the caller can emit a user-friendly error.
moduleNameFromPath :: FilePath -> FilePath -> Maybe String
moduleNameFromPath = moduleNameFromPathWithRoots ["src", "tests"]


moduleNameFromPathWithRoots :: [FilePath] -> FilePath -> FilePath -> Maybe String
moduleNameFromPathWithRoots roots cwd absPath
    | takeExtension absPath /= ".sky" = Nothing
    | otherwise =
        let normaliseRoot r = if r == "." || null r
                then cwd
                else cwd </> r
            candidates = map normaliseRoot roots
            stripRoot root = stripPrefix (root ++ "/") absPath
            relative = foldr
                (\root acc -> case acc of
                    Just _  -> acc
                    Nothing -> stripRoot root)
                Nothing
                candidates
        in case relative of
            Nothing -> Nothing
            Just rel ->
                let stem  = dropExtension rel
                    parts = splitDirectories stem
                    -- Sky module segments must begin with an uppercase
                    -- letter. Test directory path segments are often
                    -- lowercase (tests/core/FooTest.sky → core is
                    -- `core` on disk, `Core` in Sky). Capitalise the
                    -- first letter of every segment when it isn't
                    -- already uppercase.
                    capFirst (c:cs) | c >= 'a' && c <= 'z' = toEnum (fromEnum c - 32) : cs
                    capFirst s = s
                    rewritten = map capFirst parts
                in Just (foldr (\a b -> if null b then a else a ++ "." ++ b) "" rewritten)


-- | Append a Go dependency to sky.toml's `[go.dependencies]` table
-- so subsequent `sky build` / `sky install` round-trips see it.
-- Idempotent: if the package is already listed under any key, the
-- file is left untouched (we don't bump versions).
--
-- Hand-rolled because Sky.Sky.Toml only has a parser, not a writer
-- (the v0.7 self-hosted compiler used a Sky-side toml writer that
-- never made it across to the Haskell rewrite). Keeps the manifest
-- formatting friendly: appends to the existing `[go.dependencies]`
-- section if present, otherwise creates one at the end of the file.
appendGoDependency :: String -> IO ()
appendGoDependency pkg = do
    hasToml <- doesFileExist "sky.toml"
    if not hasToml
        then putStrLn "   (no sky.toml — skipping dep registration; create one with `sky init`)"
        else do
            content <- readFile "sky.toml"
            length content `seq` return ()  -- force read so writeFile is safe
            let lns = lines content
                quoted = "\"" ++ pkg ++ "\""
                alreadyListed = any (\l ->
                        let trimmed = dropWhile (== ' ') l
                        in startsWith quoted trimmed
                            || startsWith ("\"" ++ pkg ++ "\"") trimmed)
                    lns
            if alreadyListed
                then putStrLn $ "   (already listed in sky.toml — left as-is)"
                else do
                    let entry = quoted ++ " = \"latest\""
                        sectionHeader = "[\"go.dependencies\"]"
                        legacyHeader  = "[go.dependencies]"
                        hasSection = any (\l ->
                                let t = dropWhile (== ' ') l
                                in t == sectionHeader || t == legacyHeader)
                            lns
                        newLines =
                            if hasSection
                                then insertAfterSection lns
                                else lns ++ ["", sectionHeader, entry]
                    writeFile "sky.toml" (unlines newLines)
                    putStrLn $ "   Added to sky.toml [go.dependencies]"
  where
    startsWith p s = take (length p) s == p
    -- Append `entry` immediately after the `[go.dependencies]` (or
    -- legacy `["go.dependencies"]`) section header so deps cluster
    -- together in source order. If the section is the last thing in
    -- the file, append to the end.
    insertAfterSection ls =
        let isHeader l =
                let t = dropWhile (== ' ') l
                in t == "[\"go.dependencies\"]" || t == "[go.dependencies]"
            (before, rest) = break isHeader ls
        in case rest of
            (header:after) -> before ++ [header, "\"" ++ pkg ++ "\" = \"latest\""] ++ after
            []             -> ls  -- shouldn't reach (hasSection was True)


-- | For each declared go dep, regenerate the FFI bindings when its
-- `.skycache/ffi/<slug>.kernel.json` file is absent. Used by `sky
-- install` and the `sky build` auto-regen fallback. Silently skips
-- inspector failures — user can still run `sky add <pkg>` manually.
--
-- Performance shape (skyshop benchmark, 18 Go deps including Stripe
-- SDK + Firebase + Firestore):
--   * `go get pkg` per dep: warm-cache near-instant, cold-cache
--     dominated by network. Batched here into ONE `go get pkg1 pkg2
--     ...` invocation so the module graph is updated atomically and
--     transitive deps shared between Stripe and Firestore (e.g.
--     golang.org/x/oauth2) are resolved once.
--   * `sky-ffi-inspect pkg` per dep: CPU-heavy go/types load. This
--     is the bottleneck. Bounded-parallel via QSem to cap memory
--     pressure (each inspector holds 1-2 GB for big SDKs); user can
--     override the cap via SKY_INSTALL_PARALLEL.
--   * `generateBindings`: <1s per dep, parallel-safe (writes to
--     distinct .skycache/ffi/<slug>.* files per dep).
-- | Build the project at `path` and exec the resulting binary.
-- Shared by `sky run` and `sky db <status|migrate>` (the latter
-- pre-sets SKY_DB_OP so the app runs in DB-ops mode).
runProject :: FilePath -> IO (Either String ())
runProject path = do
    hasToml <- doesFileExist "sky.toml"
    config <- if hasToml
        then Toml.parseSkyToml <$> readFile "sky.toml"
        else return Toml.defaultConfig
    let outDir = "sky-out"
    createDirectoryIfMissing True outDir
    let goDeps = Toml._goDeps config
    when (not (null goDeps)) $ do
        hasGoMod <- doesFileExist "sky-out/go.mod"
        when (not hasGoMod) $ do
            hasRt <- doesFileExist "runtime-go/go.mod"
            if hasRt
                then callProcess "cp" ["runtime-go/go.mod", "sky-out/go.mod"]
                else writeFile "sky-out/go.mod" $ unlines ["module sky-app", "", "go 1.21"]
        regenMissingBindings goDeps
    result <- Compile.compile config path outDir
    case result of
        Left err -> return (Left err)
        Right goPath -> do
            putStrLn "Running go build..."
            runGoBuildWithDiagnostics outDir (Toml._binName config) goPath
            -- v0.15.42 (audit §3.4): "Compilation successful" prints
            -- only after `go build` returns 0. Before this fix the
            -- banner appeared at the end of Sky lowering, misleading
            -- users when Go subsequently rejected the emitted code.
            putStrLn "Compilation successful"
            putStrLn "Build complete, running..."
            -- Propagate the child's exit code verbatim rather than
            -- surfacing a `callProcess … failed` exception — a
            -- non-zero exit (e.g. `sky db status` finding drift) is
            -- a legitimate, scriptable outcome.
            ec <- rawSystem (outDir ++ "/" ++ Toml._binName config) []
            case ec of
                ExitSuccess   -> return (Right ())
                ExitFailure _ -> exitWith ec


regenMissingBindings :: [(String, String)] -> IO ()
regenMissingBindings deps = do
    createDirectoryIfMissing True ".skycache/ffi"
    -- Filter once: only keep deps whose kernel.json is missing.
    -- Subsequent `sky install` runs see this empty after a successful
    -- first run, so the parallel machinery only kicks in on cold
    -- caches and after `sky add`.
    missing <- filterM (\(pkg, _) -> do
        let slug = FfiGen.slugify pkg
        cached <- doesFileExist (".skycache/ffi/" ++ slug ++ ".kernel.json")
        return (not cached)) deps
    case missing of
        [] -> return ()
        _  -> do
            -- Batch `go get` for all missing deps in a single
            -- invocation. Go's module resolver is faster than running
            -- it N times because the dep graph is computed once.
            -- best-effort `go get` (the old `|| true`): run the deps as argv,
            -- ignore a non-zero exit. (Drops the cosmetic `grep -v '^go:'`
            -- stderr noise-filter; download lines print verbatim now.)
            do (_, _, _, ph) <- System.Process.createProcess
                   (System.Process.proc "go" ("get" : map fst missing))
                       { System.Process.cwd = Just "sky-out" }
               _ <- System.Process.waitForProcess ph
               return ()
            -- Chunked multi-inspector strategy:
            --   * Split missing deps into K chunks (K = parallelism cap).
            --   * Run K inspector subprocesses in parallel, each in
            --     multi-mode over its chunk.
            -- Why chunked rather than (a) one combined call or (b) N
            -- separate calls:
            --   Combined-only: single subprocess, internal Go-loader
            --     goroutines saturate ~2 cores. Dedup benefit but no
            --     cross-process parallelism — wall-clock-bound by the
            --     slowest single load.
            --   Separate-N: N subprocesses each loading one root,
            --     each internally using ~2 cores. Saturates cores at
            --     N=4, but every shared transitive dep is type-
            --     checked N times (no dedup).
            --   Chunked-K: K subprocesses each loading C/K deps in
            --     multi-mode. Each chunk gets dedup within itself;
            --     chunks run in parallel for wall speedup. Sweet
            --     spot when K matches numProcessors/2 (so total
            --     thread count matches core count).
            -- For skyshop's 18 deps with K=4: 4-5 deps/chunk, 4
            -- chunks running concurrently, ~halves wall vs combined-
            -- only and saves ~10% CPU vs separate-N.
            n <- resolveInstallParallelism
            let pkgs   = map fst missing
                chunks = chunkInto n pkgs
            chunkResults <- mapConcurrentlyN n FfiGen.runInspectorMulti chunks
            -- Concat back into a per-input results list, preserving
            -- order. Each chunk's results are aligned to its input
            -- subset (runInspectorMulti's contract).
            let allResults = concat chunkResults
            -- generateBindings is parallel-safe (each pkg writes to
            -- distinct files). Sub-second per call so this loop is
            -- fast either way; keep the parallel scaffolding so
            -- future heavier post-processing scales for free.
            _ <- mapConcurrentlyN n emit (zip pkgs allResults)
            return ()
  where
    emit (_, Left _)     = return ()
    emit (_, Right info) = do
        _ <- FfiGen.generateBindings info
        return ()


-- | Split a list into N roughly-equal chunks. Used by the install
-- chunked-multi strategy. Filters out empty chunks so callers don't
-- spawn no-op subprocesses.
chunkInto :: Int -> [a] -> [[a]]
chunkInto n xs
    | n <= 1    = [xs]
    | null xs   = []
    | otherwise =
        let total       = length xs
            chunkSize   = (total + n - 1) `div` n
        in  filter (not . null) (chunkOf chunkSize xs)
  where
    chunkOf _ [] = []
    chunkOf k ys = let (h, t) = splitAt k ys in h : chunkOf k t


-- | Resolve the inspector concurrency cap. Honours
-- SKY_INSTALL_PARALLEL (clamped to 1..16). Defaults to
-- min(numProcessors, 4): more than 4 risks RAM exhaustion on
-- Stripe-sized SDKs (each loader holds ~1.5 GB). Caps at 16 so a
-- typo doesn't accidentally launch hundreds of workers.
--
-- We use GHC.getNumProcessors (physical/logical core count from
-- the OS) rather than GHC.numCapabilities (RTS capability count,
-- always 1 unless +RTS -N is passed). The async/QSem-based
-- machinery doesn't need multiple capabilities — the inspector
-- runs as N separate OS processes and our Haskell side is mostly
-- IO-blocked waiting on them, which the runtime handles fine on
-- a single capability via cooperative scheduling.
resolveInstallParallelism :: IO Int
resolveInstallParallelism = do
    override <- System.Environment.lookupEnv "SKY_INSTALL_PARALLEL"
    cores <- GHC.getNumProcessors
    let defaultN = max 1 (min 4 cores)
    case override >>= readMaybeInt of
        Just n | n >= 1 && n <= 16 -> return n
        _                          -> return defaultN
  where
    readMaybeInt s = case reads s of
        [(n, "")] -> Just (n :: Int)
        _         -> Nothing


-- | Bounded-concurrency map: at most `n` workers in flight at once.
-- Built on async + QSem so we don't take a new dep. Returns results
-- in input order.
mapConcurrentlyN :: Int -> (a -> IO b) -> [a] -> IO [b]
mapConcurrentlyN n action xs = do
    sem <- QSem.newQSem n
    Async.mapConcurrently (\x -> Control.Exception.bracket_
        (QSem.waitQSem sem)
        (QSem.signalQSem sem)
        (action x)) xs


-- Local filterM (avoid pulling Control.Monad just for this).
filterM :: Monad m => (a -> m Bool) -> [a] -> m [a]
filterM _ []     = return []
filterM p (x:xs) = do
    keep <- p x
    rest <- filterM p xs
    return (if keep then x : rest else rest)


-- | Sky compiler CLI
-- Commands: build, run, check, fmt, init, add, remove, install, lsp, upgrade, version
main :: IO ()
main = do
    -- Force UTF-8 for all file IO regardless of the host locale.
    -- Sky stdlib + user source contain non-ASCII bytes (…, box-
    -- drawing, emoji in multiline strings). Under a C/POSIX locale
    -- — common in minimal Docker images + CI — the default
    -- locale encoding is ASCII and `readFile` aborts with
    -- "hGetContents: invalid argument (cannot decode byte …)".
    -- Setting the locale encoding here fixes every read + write.
    GHC.IO.Encoding.setLocaleEncoding GHC.IO.Encoding.utf8
    -- `sky` with no arguments should print the help screen and exit 0
    -- instead of a bare "Missing: (COMMAND)" error. Inject `--help`
    -- into argv when none is present.
    args <- System.Environment.getArgs
    result <- if null args
        then do
            _ <- handleParseResult $ execParserPure defaultPrefs opts ["--help"]
            return (Right ())
        else do
            cmd <- execParser opts
            runCommand cmd
    case result of
        Right () -> exitSuccess
        Left err -> do
            hPutStrLn stderr err
            exitFailure
  where
    opts = info (commandParser <**> helper)
        ( fullDesc
        <> header "sky — the Sky programming language compiler"
        <> progDesc "Compile Sky to typed Go"
        )


data Command
    = Build FilePath
    | Run FilePath
    | Watch Watch.WatchOpts      -- file-watch-driven rebuild + restart
    | Check FilePath
    | Fmt FmtTarget
    | Test FilePath
    | Verify (Maybe String)      -- Nothing = all examples; Just name = one
    | Init (Maybe String)
    | Add String
    | Remove String
    | Install
    | Update
    | Clean
    | Lsp
    | Upgrade
    | UpgradeClaude              -- refresh ./CLAUDE.md from embedded template
    | Console ConsoleOpts        -- run bundled Sky Console mini-app
    | ConsoleServe ConsoleServeOpts
                                 -- run the standalone Sky Console hub daemon
    | Doc DocOpts                -- print / serve API documentation
    | Doctor Doctor.DoctorOpts   -- diagnose project / runtime issues
    | Db DbAction FilePath       -- sky db status / sky db migrate
    | Version
    deriving (Show)


-- | `sky db` sub-actions — drive the runtime's SKY_DB_OP mode.
data DbAction
    = DbStatus   -- report applied / pending / drifted migrations
    | DbMigrate  -- apply pending migrations, then exit
    deriving (Show)


-- | Options for `sky console`.
data ConsoleOpts = ConsoleOpts
    { _consolePort :: Int        -- Sky.Live port (default 8025)
    , _consoleTui  :: Bool       -- --tui: run via Sky.Tui instead
    } deriving (Show)


-- | Options for `sky console serve` — the standalone Hub daemon.
-- v0.16.4 Chunk 1. See docs/v0.16.x-console/v0.16.4-IMPLEMENTATION-PLAN.md
-- for the full chunked scope.
data ConsoleServeOpts = ConsoleServeOpts
    { _hubPort    :: !Int             -- HTTP listen port (default 4000)
    , _hubDataDir :: !FilePath        -- SQLite + future DuckDB live here
    , _hubAuth    :: !String          -- "token" | "app" | "off"
    , _hubTlsCert :: !(Maybe FilePath)
    , _hubTlsKey  :: !(Maybe FilePath)
    } deriving (Show)


-- | Options for `sky doc`.
data DocOpts = DocOpts
    { _docTarget :: !(Maybe String)  -- Module name to print (terminal mode)
    , _docList   :: !Bool            -- --list: print all module names
    , _docServe  :: !Bool            -- --serve: start the doc HTTP server
    , _docTui    :: !Bool            -- --tui: launch the Sky.Tui doc browser
    , _docPort   :: !Int             -- --port (default 8030)
    } deriving (Show)


data FmtTarget
    = FmtFile FilePath
    | FmtStdin
    deriving (Show)


commandParser :: Parser Command
commandParser = subparser
    ( command "build"
        (info (Build <$> fileArg) (progDesc "Compile to binary"))
    <> command "run"
        (info (Run <$> fileArg) (progDesc "Build and run"))
    <> command "watch"
        (info (Watch <$> watchOptsParser)
            (progDesc "Watch source files; rebuild + restart on change"))
    <> command "check"
        (info (Check <$> fileArg) (progDesc "Type-check only"))
    <> command "fmt"
        (info (Fmt <$> fmtTargetArg)
            (progDesc "Format source file (or stdin with --stdin / -)"))
    <> command "test"
        (info (Test <$> fileArg) (progDesc "Run a Sky test module (exposing `tests : List Test`)"))
    <> command "verify"
        (info (Verify <$> optional (argument str (metavar "EXAMPLE")))
            (progDesc "Build + run + panic-check every example; enforce forbidden-pattern gate"))
    <> command "init"
        (info (Init <$> optional (argument str (metavar "NAME")))
            (progDesc "Create new project"))
    <> command "add"
        (info (Add <$> argument str (metavar "PACKAGE"))
            (progDesc "Add Go dependency"))
    <> command "remove"
        (info (Remove <$> argument str (metavar "PACKAGE"))
            (progDesc "Remove Go dependency"))
    <> command "install"
        (info (pure Install) (progDesc "Install dependencies"))
    <> command "update"
        (info (pure Update) (progDesc "Update Go dependencies to latest"))
    <> command "clean"
        (info (pure Clean) (progDesc "Remove build artifacts (sky-out/, .skycache/)"))
    <> command "lsp"
        (info (pure Lsp) (progDesc "Start language server"))
    <> command "upgrade"
        (info (pure Upgrade) (progDesc "Self-upgrade"))
    <> command "upgrade-claude"
        (info (pure UpgradeClaude)
            (progDesc "Refresh ./CLAUDE.md from this binary's embedded template"))
    <> command "console"
        (info (Console <$> consoleOptsParser)
            (progDesc "Run the bundled Sky Console dashboard (Std.Ui Sky.Live; --tui for terminal)"))
    <> command "console-serve"
        (info (ConsoleServe <$> consoleServeOptsParser)
            (progDesc "Run as a Sky Console hub daemon — OTLP receiver + multi-service dashboard"))
    <> command "doc"
        (info (Doc <$> docOptsParser)
            (progDesc "Print or browse API docs (--serve for HTTP server)"))
    <> command "doctor"
        (info (Doctor <$> doctorOptsParser)
            (progDesc "Diagnose common project / runtime stuck-states (stale cache, port in use, missing FFI)"))
    <> command "db"
        (info dbParser
            (progDesc "Database migrations — `sky db status` / `sky db migrate`"))
    <> command "version"
        (info (pure Version) (progDesc "Show version"))
    )
  <|> flag' Version
        ( long "version"
        <> short 'v'
        <> help "Show version"
        )


fileArg :: Parser FilePath
fileArg = argument str (metavar "FILE" <> value "src/Main.sky")


-- Parser for `sky db <status|migrate> [FILE]`.
dbParser :: Parser Command
dbParser = subparser
    ( command "status"
        (info (Db DbStatus <$> fileArg)
            (progDesc "Show applied / pending / drifted migrations, then exit"))
    <> command "migrate"
        (info (Db DbMigrate <$> fileArg)
            (progDesc "Apply all pending migrations in order, then exit"))
    )


-- Parser for `sky console` flags.
consoleOptsParser :: Parser ConsoleOpts
consoleOptsParser = ConsoleOpts
    <$> option auto
        ( long "port"
       <> short 'p'
       <> metavar "PORT"
       <> value 8025
       <> help "Listen port for the Sky.Live console (default 8025)"
        )
    <*> switch
        ( long "tui"
       <> help "Run via Sky.Tui in the terminal instead of the browser"
        )


-- Parser for `sky console-serve` flags (Hub daemon, v0.16.4).
consoleServeOptsParser :: Parser ConsoleServeOpts
consoleServeOptsParser = ConsoleServeOpts
    <$> option auto
        ( long "port"
       <> short 'p'
       <> metavar "PORT"
       <> value 4000
       <> help "HTTP listen port (default 4000)"
        )
    <*> strOption
        ( long "data-dir"
       <> metavar "DIR"
       <> value "./skyhub-data"
       <> help "Directory for the hub's SQLite database (default ./skyhub-data)"
        )
    <*> strOption
        ( long "auth"
       <> metavar "MODE"
       <> value "token"
       <> help "Auth mode: token | app | off (default token; SKY_CONSOLE_HUB_TOKEN required for token)"
        )
    <*> optional (strOption
        ( long "tls-cert"
       <> metavar "FILE"
       <> help "Path to TLS certificate (enables HTTPS when paired with --tls-key)"
        ))
    <*> optional (strOption
        ( long "tls-key"
       <> metavar "FILE"
       <> help "Path to TLS key (required when --tls-cert is set)"
        ))


-- Parser for `sky doc` flags.
docOptsParser :: Parser DocOpts
docOptsParser = DocOpts
    <$> optional (argument str
        ( metavar "MODULE"
       <> help "Module name to print (e.g. Std.Money)"
        ))
    <*> switch
        ( long "list"
       <> help "List every indexed module name"
        )
    <*> switch
        ( long "serve"
       <> help "Start the browsable doc HTTP server"
        )
    <*> switch
        ( long "tui"
       <> help "Launch the Sky.Tui terminal doc browser"
        )
    <*> option auto
        ( long "port"
       <> short 'p'
       <> metavar "PORT"
       <> value 8030
       <> help "Listen port for `--serve` (default 8030)"
        )


-- Parser for `sky doctor` flags.
doctorOptsParser :: Parser Doctor.DoctorOpts
doctorOptsParser = Doctor.DoctorOpts
    <$> switch
        ( long "fix"
       <> help "Auto-apply safe remediations (delete stale caches, kill port holder)"
        )
    <*> switch
        ( long "verbose"
       <> short 'v'
       <> help "Print check ids alongside each finding"
        )


-- Parser for `sky watch` flags. Defaults match
-- Sky.Cli.Watch.defaultWatchOpts; CLI flags overlay onto them.
watchOptsParser :: Parser Watch.WatchOpts
watchOptsParser = build
    <$> argument str (metavar "FILE" <> value "src/Main.sky")
    <*> switch (long "no-run" <> help "Rebuild only; do not spawn the binary")
    <*> switch (long "clear" <> help "Clear the screen between rebuilds")
    <*> option auto
            ( long "interval"
           <> metavar "MS"
           <> value 200
           <> help "Poll interval in ms (default 200)"
            )
    <*> option auto
            ( long "debounce"
           <> metavar "MS"
           <> value 150
           <> help "Debounce window after a change in ms (default 150)"
            )
    <*> option auto
            ( long "kill-timeout"
           <> metavar "MS"
           <> value 3000
           <> help "Graceful SIGTERM timeout before SIGKILL (default 3000)"
            )
    <*> many (strOption
            ( long "watch"
           <> metavar "PATH"
           <> help "Extra file or directory to watch (repeatable). Directories are walked recursively for .sky files."
            ))
  where
    build entry noRun clearScreen interval debounce killTimeout extras =
        (Watch.defaultWatchOpts entry)
            { Watch.woNoRun         = noRun
            , Watch.woClear         = clearScreen
            , Watch.woPollMs        = interval
            , Watch.woDebounceMs    = debounce
            , Watch.woKillTimeoutMs = killTimeout
            , Watch.woExtras        = extras
            }


-- Accept either `--stdin` / `-` / positional FILE. Used by `sky fmt`
-- so editors (helix, neovim, vscode) can pipe buffers directly.
fmtTargetArg :: Parser FmtTarget
fmtTargetArg =
    flag' FmtStdin (long "stdin" <> help "Read source from stdin, write formatted output to stdout")
  <|> (toTarget <$> argument str (metavar "FILE" <> value "src/Main.sky"))
  where
    toTarget "-"  = FmtStdin
    toTarget path = FmtFile path


-- | CLAUDE.md contents are embedded into the sky binary at build time
-- via Template Haskell, so `sky init` works from any release artefact
-- without needing a templates/ directory beside the binary.
embeddedClaudeMd :: String
embeddedClaudeMd = $(embedStringFile "templates/CLAUDE.md")


-- | Copy a named template into the new project. For CLAUDE.md we use
-- the embedded copy; other templates fall through to disk lookup so
-- future project-scaffolding additions don't require a compiler rebuild.
copyTemplate :: FilePath -> FilePath -> IO ()
copyTemplate destProject "CLAUDE.md" =
    writeFile (destProject ++ "/CLAUDE.md") embeddedClaudeMd
copyTemplate destProject filename = do
    -- Disk-template fallback for names other than CLAUDE.md.
    candidates <- templateSearchPaths filename
    mSrc <- firstExisting candidates
    case mSrc of
        Nothing  -> return ()
        Just src -> do
            content <- readFile src
            writeFile (destProject ++ "/" ++ filename) content
  where
    firstExisting [] = return Nothing
    firstExisting (p:ps) = do
        ok <- doesFileExist p
        if ok then return (Just p) else firstExisting ps


templateSearchPaths :: FilePath -> IO [FilePath]
templateSearchPaths filename = do
    env <- System.Environment.lookupEnv "SKY_TEMPLATES_DIR"
    exe <- System.Environment.getExecutablePath
    let exeDir = dirOf exe
    cwd <- System.Directory.getCurrentDirectory
    return $ concat
        [ maybe [] (\d -> [d </> filename]) env
        , [ exeDir </> "templates" </> filename
          , exeDir </> ".." </> "templates" </> filename
          , cwd </> "templates" </> filename
          ]
        ]
  where
    dirOf = reverse . dropWhile (/= '/') . reverse
    (</>) a b = a ++ "/" ++ b


-- | Version string.
--
-- * Local / contributor builds: `app/VERSION` contains the literal
--   "dev" (kept in git), so `sky --version` reports "sky dev (0.9.0)".
-- * CI release builds: before `cabal install`, CI overwrites
--   `app/VERSION` with the tagged version (e.g. `1.2.3`) so the
--   released binary reports "sky v1.2.3".
--
-- `qAddDependentFile` registers app/VERSION as a TH dependency so
-- GHC re-runs this splice (and recompiles Main.hs) whenever the
-- file contents change — enough to survive cabal's object cache.
skyBuildVersion :: String
skyBuildVersion =
    $(do
        let versionFile = "app/VERSION"
            isWs c = c == ' ' || c == '\n' || c == '\r' || c == '\t'
            trim = reverse . dropWhile isWs . reverse . dropWhile isWs
        Language.Haskell.TH.Syntax.qAddDependentFile versionFile
        raw <- Language.Haskell.TH.Syntax.runIO (readFile versionFile)
        Language.Haskell.TH.Syntax.lift (trim raw))


skyVersionString :: String
skyVersionString
    | skyBuildVersion == "dev" = "sky dev"
    | otherwise                = "sky v" ++ skyBuildVersion


runCommand :: Command -> IO (Either String ())
runCommand cmd = case cmd of
    Version -> do
        putStrLn skyVersionString
        return (Right ())

    Build path -> do
        -- Read sky.toml if it exists
        hasToml <- doesFileExist "sky.toml"
        config <- if hasToml
            then Toml.parseSkyToml <$> readFile "sky.toml"
            else return Toml.defaultConfig
        let outDir = "sky-out"
        createDirectoryIfMissing True outDir
        -- Auto-regen missing Go FFI bindings before compile. Idempotent:
        -- skips deps whose .kernel.json is already present.
        let goDeps = Toml._goDeps config
        when (not (null goDeps)) $ do
            hasGoMod <- doesFileExist "sky-out/go.mod"
            when (not hasGoMod) $ do
                hasRt <- doesFileExist "runtime-go/go.mod"
                if hasRt
                    then callProcess "cp" ["runtime-go/go.mod", "sky-out/go.mod"]
                    else writeFile "sky-out/go.mod" $ unlines ["module sky-app", "", "go 1.21"]
            regenMissingBindings goDeps
        result <- Compile.compile config path outDir
        case result of
            Left err -> return (Left err)
            Right goPath -> do
                putStrLn "Running go build..."
                runGoBuildWithDiagnostics outDir (Toml._binName config) goPath
                -- v0.15.42 (audit §3.4): see Run-path comment.
                putStrLn "Compilation successful"
                putStrLn $ "Build complete: " ++ outDir ++ "/" ++ Toml._binName config
                return (Right ())

    Run path -> runProject path

    Db action path -> do
        -- Drive the runtime's SKY_DB_OP mode: the built app's
        -- Db.migrate call detects the env var, prints a report /
        -- applies migrations, and exits before serving.
        System.Environment.setEnv "SKY_DB_OP" $ case action of
            DbStatus  -> "status"
            DbMigrate -> "migrate"
        runProject path

    Watch opts -> do
        Watch.runWatch opts
        return (Right ())

    Check path -> do
        hasToml <- doesFileExist "sky.toml"
        config <- if hasToml
            then Toml.parseSkyToml <$> readFile "sky.toml"
            else return Toml.defaultConfig
        -- Regen missing FFI bindings so type-check sees up-to-date .skyi
        -- signatures without needing the user to run `sky build` first.
        let outDir = "sky-out"
            goDeps = Toml._goDeps config
        when (not (null goDeps)) $ do
            createDirectoryIfMissing True outDir
            hasGoMod <- doesFileExist "sky-out/go.mod"
            when (not hasGoMod) $ do
                hasRt <- doesFileExist "runtime-go/go.mod"
                if hasRt
                    then callProcess "cp" ["runtime-go/go.mod", "sky-out/go.mod"]
                    else writeFile "sky-out/go.mod" $ unlines ["module sky-app", "", "go 1.21"]
            regenMissingBindings goDeps
        -- P0-1 (audit): sky check must be a superset of sky build. Run
        -- the full emit + `go build` so codegen-stage failures surface
        -- here instead of only when the user runs `sky build`. Without
        -- this gate the checker accepted programs that panicked at
        -- runtime (typed-callee .(T) assertions, record-ctor field
        -- swaps, Task-return coercion holes) because the Sky type
        -- system was satisfied but codegen produced invalid Go.
        result <- Compile.compile config path outDir
        case result of
            Left err -> return (Left err)
            Right _ -> do
                putStrLn "Running go build..."
                -- #569: `sky check` discards the binary, so we don't
                -- need an optimised build.  `-gcflags=all=-l` disables
                -- inlining, which is the *only* way to stop Go's
                -- closure-name composition pathology in the
                -- Std.Ui.renderElement / renderNodeAs mutual-recursion
                -- pair — every inlining decision through a `func1`
                -- closure prepends another `OuterFn.func1.` to the
                -- nested closure's symbol name, and the static-call
                -- graph here makes the prefix grow exponentially.
                -- We measured 20.5 MB symbols on a Std.Ui-importing
                -- fixture; Apple's ld64 in Sequoia (used by GitHub
                -- Actions `macos-latest`) tightened the symbol-name
                -- cap and rejects the object file with the assertion
                -- `(name.size() <= maxLength)` during input parsing.
                -- Disabling inlining caps the symbol at ~300 bytes
                -- and lets the linker complete.  Free on `sky check`
                -- (throwaway binary); `sky build` keeps full
                -- optimisation for production.
                (ec, _, berr) <- System.Process.readCreateProcessWithExitCode
                    (System.Process.shell
                        ("cd " ++ outDir
                            ++ " && go build -gcflags=all=-l -o /dev/null ."))
                    ""
                case ec of
                    System.Exit.ExitSuccess -> do
                        putStrLn "No errors found."
                        return (Right ())
                    System.Exit.ExitFailure _ -> do
                        let msg = "Codegen produced Go that `go build` rejects.\n"
                                ++ "This is a compiler-side bug — the Sky type system\n"
                                ++ "accepted the program but Go did not.\n\n"
                                ++ "Go errors:\n"
                                ++ berr
                        return (Left msg)

    Test path -> do
        -- Synthesise a temporary Main.sky that imports the user's test
        -- module and calls `Sky.Test.runMain tests`. Build + run via the
        -- same pipeline as `sky build`; exit code is propagated so CI
        -- picks up failures. The synthesis keeps user test modules
        -- minimal: `module FooTest exposing (tests); tests = [...]`.
        hasToml <- doesFileExist "sky.toml"
        config <- if hasToml
            then Toml.parseSkyToml <$> readFile "sky.toml"
            else return Toml.defaultConfig
        absPath <- System.Directory.canonicalizePath path
        cwd <- System.Directory.getCurrentDirectory
        -- Honour the configured source root (default src/) and the
        -- common tests/ convention.
        let sourceRoots = [Toml._sourceRoot config, "src", "tests"]
        testModName <- case moduleNameFromPathWithRoots sourceRoots cwd absPath of
            Just n  -> return n
            Nothing -> do
                hPutStrLn stderr $
                    "sky test: " ++ path ++ " must live under src/ or tests/ so its module name can be derived"
                exitFailure
        -- Write the synthesised entry into the project's configured
        -- source root (defaults to `src/`; test projects commonly use
        -- `tests/`). Placing it anywhere else would leave it outside
        -- the module-graph walker's scan.
        let entryDir  = Toml._sourceRoot config
            entryFile = entryDir </> "SkyTestEntry__.sky"
            entryBody = unlines
                [ "module SkyTestEntry__ exposing (main)"
                , ""
                , "import Sky.Test as Test"
                , "import " ++ testModName ++ " as Suite"
                , ""
                , "main ="
                , "    Test.runMain Suite.tests"
                ]
        createDirectoryIfMissing True entryDir
        writeFile entryFile entryBody
        let outDir = "sky-out"
        createDirectoryIfMissing True outDir
        let goDeps = Toml._goDeps config
        when (not (null goDeps)) $ do
            hasGoMod <- doesFileExist "sky-out/go.mod"
            when (not hasGoMod) $ do
                hasRt <- doesFileExist "runtime-go/go.mod"
                if hasRt
                    then callProcess "cp" ["runtime-go/go.mod", "sky-out/go.mod"]
                    else writeFile "sky-out/go.mod" $ unlines ["module sky-app", "", "go 1.21"]
            regenMissingBindings goDeps
        result <- Compile.compile config entryFile outDir
        -- Clean up the entry regardless of compile outcome. Pre-fix,
        -- a go-build exception skipped the cleanup line, leaving
        -- SkyTestEntry__.sky in src/ across sessions.
        let cleanup = do
                System.Directory.removeFile entryFile
                    `catchIOError` (\_ -> return ())
        case result of
            Left err -> do
                cleanup
                return (Left err)
            Right _ -> do
                let binName = Toml._binName config
                -- go build may fail (undefined references etc.);
                -- wrap in try so cleanup always runs.
                buildRc <- Control.Exception.try
                    (callProcess "sh"
                        ["-c", "cd " ++ outDir ++ " && go build -o " ++ binName ++ " ."])
                    :: IO (Either Control.Exception.SomeException ())
                cleanup
                case buildRc of
                    Left e -> do
                        hPutStrLn stderr $
                            "sky test: go build failed: " ++ show e
                        exitWith (System.Exit.ExitFailure 1)
                    Right () -> do
                        -- Run with inherited stdout/stderr so test
                        -- output is visible; propagate exit code.
                        (_, _, _, ph) <- System.Process.createProcess
                            (System.Process.proc (outDir ++ "/" ++ binName) [])
                        ec <- System.Process.waitForProcess ph
                        case ec of
                            System.Exit.ExitSuccess   -> return (Right ())
                            System.Exit.ExitFailure n ->
                                exitWith (System.Exit.ExitFailure n)

    Verify target -> do
        ok <- runVerify target
        if ok then return (Right ()) else exitWith (System.Exit.ExitFailure 1)

    Fmt target -> do
        case target of
            FmtFile path -> do
                src <- TIO.readFile path
                case ParseMod.parseModule src of
                    Left err -> return (Left $ "Parse error: " ++ show err)
                    Right srcMod -> do
                        let baseOut = T.pack (Format.formatModule srcMod)
                            withComments = preserveTopLevelComments src baseOut
                        case fmtSafetyCheck src withComments of
                            Just msg -> return (Left msg)
                            Nothing -> do
                                TIO.writeFile path withComments
                                putStrLn $ "Formatted " ++ path
                                return (Right ())
            FmtStdin -> do
                src <- TIO.getContents
                case ParseMod.parseModule src of
                    Left err -> do
                        TIO.putStr src
                        return (Left $ "Parse error: " ++ show err)
                    Right srcMod -> do
                        let baseOut = T.pack (Format.formatModule srcMod)
                            withComments = preserveTopLevelComments src baseOut
                        force <- System.Environment.lookupEnv "SKY_FMT_FORCE"
                        debug <- System.Environment.lookupEnv "SKY_FMT_DEBUG"
                        case debug of
                            Just _ -> do
                                hPutStrLn stderr "=== baseOut (pre-preserver) ==="
                                TIO.hPutStr stderr baseOut
                                hPutStrLn stderr "=== withComments (post-preserver) ==="
                            _ -> return ()
                        case (force, fmtSafetyCheck src withComments) of
                            (Just _, _)        -> TIO.putStr withComments >> return (Right ())
                            (Nothing, Just m)  -> TIO.putStr src >> return (Left m)
                            (Nothing, Nothing) -> TIO.putStr withComments >> return (Right ())

    Init mName -> do
        let name = maybe "sky-project" id mName
        putStrLn $ "Initialising project: " ++ name
        createDirectoryIfMissing True (name ++ "/src")
        writeFile (name ++ "/sky.toml") $ unlines
            [ "# sky.toml — project configuration."
            , "# Full reference: https://github.com/anzellai/sky#skytoml"
            , ""
            , "name    = \"" ++ name ++ "\""
            , "version = \"0.1.0\""
            , "entry   = \"src/Main.sky\""
            , "bin     = \"app\""
            , ""
            , "[source]"
            , "root = \"src\""
            , ""
            , "# [live]            # Sky.Live runtime (uncomment to configure)"
            , "# port         = 8000"
            , "# store        = \"memory\"   # memory | sqlite | postgres | redis"
            , "# storePath    = \"sky.db\"   # sqlite file or postgres / redis conn str"
            , "# ttl          = 1800         # session TTL in seconds"
            , "# static       = \"public\"   # static asset directory"
            , "# maxBodyBytes = 5242880      # cap for /_sky/event (5 MiB default; bump for onFile/onImage uploads)"
            , ""
            , "# [auth]            # Std.Auth configuration (uncomment to use)"
            , "# driver     = \"jwt\"         # jwt | session | oauth"
            , "# secret     = \"change-me\"   # JWT signing secret (use env var in prod)"
            , "# tokenTtl   = 86400           # token lifetime in seconds"
            , "# cookieName = \"sky_auth\""
            , ""
            , "# [database]        # Std.Db configuration (uncomment to use)"
            , "# driver = \"sqlite\"          # sqlite | postgres"
            , "# path   = \"app.db\"          # sqlite file or postgres conn str"
            , ""
            , "# [\"go.dependencies\"]        # `sky add <pkg>` records these here"
            , ""
            , "# [dependencies]              # Sky-source dependencies (from git)"
            , "# \"github.com/anzellai/sky-tailwind\" = \"latest\""
            ]
        writeFile (name ++ "/src/Main.sky") $ unlines
            [ "module Main exposing (main)"
            , ""
            , "import Sky.Core.Prelude exposing (..)"
            , "import Std.Log exposing (println)"
            , ""
            , ""
            , "main ="
            , "    println \"Hello from " ++ name ++ "!\""
            ]
        writeFile (name ++ "/.gitignore") $ unlines
            [ "sky-out/"
            , ".skycache/"
            , ".skydeps/"
            , ".env"
            , "*.db"
            , "*.db-shm"
            , "*.db-wal"
            ]
        -- Copy the Sky coding guide so AI assistants operating in this
        -- project have context on stdlib / idioms. Template lives next
        -- to the installed binary; also probe the dev-tree path.
        copyTemplate name "CLAUDE.md"
        putStrLn $ "Created " ++ name ++ "/"
        putStrLn $ "  sky.toml"
        putStrLn $ "  src/Main.sky"
        putStrLn $ "  .gitignore"
        putStrLn $ "  CLAUDE.md"
        putStrLn $ ""
        putStrLn $ "Next: cd " ++ name ++ " && sky build src/Main.sky"
        return (Right ())

    Add pkg -> do
        putStrLn $ "Adding " ++ pkg ++ "..."
        -- Ensure sky-out exists with go.mod (copy from runtime-go to inherit deps)
        createDirectoryIfMissing True "sky-out"
        hasGoMod <- doesFileExist "sky-out/go.mod"
        if not hasGoMod
            then do
                hasRuntimeMod <- doesFileExist "runtime-go/go.mod"
                if hasRuntimeMod
                    then callProcess "cp" ["runtime-go/go.mod", "sky-out/go.mod"]
                    else writeFile "sky-out/go.mod" $ unlines ["module sky-app", "", "go 1.21"]
            else return ()
        -- Fetch the package
        callProcessIn "sky-out" "go" ["get", pkg]
        -- Generate bindings via the Go inspector
        do
                putStrLn $ "Inspecting " ++ pkg ++ "..."
                r <- FfiGen.runInspector pkg
                case r of
                    Left err -> do
                        putStrLn $ "   FFI inspector warning: " ++ err
                        putStrLn $ "   (You can still write hand-written bindings in ffi/.)"
                        return (Right ())
                    Right info -> do
                        names <- FfiGen.generateBindings info
                        putStrLn $ "Generated " ++ show (length names) ++ " bindings in ffi/"
                        mapM_ (\n -> putStrLn $ "   " ++ n) (take 10 names)
                        if length names > 10
                            then putStrLn $ "   ... and " ++ show (length names - 10) ++ " more"
                            else return ()
                        -- Persist the dep into sky.toml so subsequent
                        -- `sky build` / `sky install` round-trips see it.
                        -- Idempotent: if the package is already present
                        -- (any version), the file is left untouched.
                        appendGoDependency pkg
                        putStrLn "Call from Sky via: Ffi.callPure \"<name>\" [args]  (or callTask for effectful)"
                        return (Right ())

    Remove pkg -> do
        putStrLn $ "Removing " ++ pkg ++ "..."
        hasGoMod <- doesFileExist "sky-out/go.mod"
        if hasGoMod
            then do
                callProcessIn "sky-out" "go" ["mod", "edit", "-droprequire", pkg]
                callProcessIn "sky-out" "go" ["mod", "tidy"]
                putStrLn $ "Removed " ++ pkg
            else putStrLn "No sky-out/go.mod found. Run sky build first."
        return (Right ())

    Install -> do
        hasToml <- doesFileExist "sky.toml"
        config <- if hasToml
            then Toml.parseSkyToml <$> readFile "sky.toml"
            else return Toml.defaultConfig
        _ <- SkyDeps.installDeps (Toml._skyDeps config)
        -- Auto-regen Go FFI bindings for every declared go dep whose
        -- `.skycache/ffi/<slug>.kernel.json` is absent. This replaces
        -- the old workflow where bindings were checked-in under ffi/.
        let goDeps = Toml._goDeps config
        when (not (null goDeps)) $ do
            putStrLn $ "Installing " ++ show (length goDeps) ++ " Go dependency(ies)"
            createDirectoryIfMissing True "sky-out"
            hasGoMod <- doesFileExist "sky-out/go.mod"
            when (not hasGoMod) $ do
                hasRt <- doesFileExist "runtime-go/go.mod"
                if hasRt
                    then callProcess "cp" ["runtime-go/go.mod", "sky-out/go.mod"]
                    else writeFile "sky-out/go.mod" $ unlines ["module sky-app", "", "go 1.21"]
            regenMissingBindings goDeps
            putStrLn $ "Go dependencies installed."
        case Toml._skyDeps config of
            [] -> return ()
            _  -> putStrLn "Sky dependencies installed."
        when (null (Toml._skyDeps config) && null goDeps) $
            putStrLn "No [dependencies] or [go.dependencies] entries in sky.toml."
        return (Right ())

    Update -> do
        hasGoMod <- doesFileExist "sky-out/go.mod"
        if not hasGoMod
            then do
                putStrLn "No sky-out/go.mod found. Run `sky build` first."
                return (Right ())
            else do
                putStrLn "Updating Go dependencies..."
                callProcess "sh" ["-c", "cd sky-out && go get -u ./... && go mod tidy"]
                putStrLn "Go dependencies updated."
                return (Right ())

    Clean -> do
        let removeIfExists p = do
                isDir  <- System.Directory.doesDirectoryExist p
                isFile <- doesFileExist p
                when isDir  (System.Directory.removeDirectoryRecursive p)
                when isFile (System.Directory.removeFile p)
        mapM_ removeIfExists ["sky-out", ".skycache", ".skydeps", "dist"]
        putStrLn "Removed sky-out/ .skycache/ .skydeps/ dist/"
        return (Right ())

    Lsp -> do
        -- LSP talks JSON-RPC on stdin/stdout; don't print anything to stdout
        -- after this point (it would corrupt the protocol framing).
        Lsp.runLsp
        return (Right ())

    Upgrade -> runUpgrade

    UpgradeClaude -> runUpgradeClaude

    Console opts -> runConsole opts

    ConsoleServe opts -> runConsoleServe opts

    Doc opts -> runDoc opts

    Doctor opts -> do
        Doctor.runDoctor opts
        -- runDoctor exits the process directly with the proper
        -- code; the Right () here is only reached when no exit
        -- happens (shouldn't, but keeps the return-type happy).
        pure (Right ())


-- | P11a: `sky upgrade` — fetch latest release from GitHub and swap the
-- running binary in place. Shells out to `curl` + `tar` so we pull in no
-- new Haskell dependencies and stay portable across macOS/Linux.
--
-- Pipeline:
--   1. Detect current platform (darwin-arm64 / linux-x64 etc).
--   2. GET https://api.github.com/repos/anzellai/sky/releases/latest
--   3. Parse tag_name (raw grep — the endpoint is stable).
--   4. Download the matching tarball into a temp dir.
--   5. `tar -xzf` then atomically rename(new, old).
--
-- Exit 1 with a clear message on any failure; never corrupt the existing
-- binary.
runUpgrade :: IO (Either String ())
runUpgrade = do
    putStrLn "sky upgrade: detecting platform..."
    (osName, arch) <- detectPlatform
    let platform = osName ++ "-" ++ arch
    putStrLn $ "   platform: " ++ platform
    putStrLn "   fetching latest release metadata..."
    releaseJson <- System.Process.readProcess "curl"
        [ "-sSL"
        , "-H", "Accept: application/vnd.github+json"
        , "https://api.github.com/repos/anzellai/sky/releases/latest"
        ] ""
    case extractTagName releaseJson of
        Nothing ->
            return (Left "sky upgrade: could not parse release metadata — is the repo reachable?")
        Just tag -> do
            putStrLn $ "   latest tag: " ++ tag
            currentBin <- System.Environment.getExecutablePath
            let assetName = "sky-" ++ platform ++ ".tar.gz"
                dlUrl = "https://github.com/anzellai/sky/releases/download/"
                            ++ tag ++ "/" ++ assetName
            tmpDir <- System.IO.Temp.getCanonicalTemporaryDirectory
            let stageDir = tmpDir ++ "/sky-upgrade-" ++ tag
            System.Directory.createDirectoryIfMissing True stageDir
            putStrLn $ "   downloading " ++ dlUrl
            (curlEC, _, curlErr) <- System.Process.readProcessWithExitCode "curl"
                [ "-sSLfo", stageDir ++ "/sky.tar.gz", dlUrl ] ""
            case curlEC of
                System.Exit.ExitFailure _ ->
                    return $ Left $ "sky upgrade: download failed — " ++ curlErr
                System.Exit.ExitSuccess -> do
                    putStrLn "   extracting..."
                    (tarEC, _, tarErr) <- System.Process.readProcessWithExitCode "tar"
                        [ "-xzf", stageDir ++ "/sky.tar.gz", "-C", stageDir ] ""
                    case tarEC of
                        System.Exit.ExitFailure _ ->
                            return $ Left $ "sky upgrade: extract failed — " ++ tarErr
                        System.Exit.ExitSuccess -> do
                            let candidate = stageDir ++ "/sky-" ++ platform
                            haveCandidate <- doesFileExist candidate
                            let newBin = if haveCandidate then candidate
                                         else stageDir ++ "/sky"
                            haveNewBin <- doesFileExist newBin
                            if not haveNewBin
                                then return $ Left $
                                    "sky upgrade: archive did not contain a `sky` binary"
                                else do
                                    putStrLn $ "   swapping " ++ currentBin
                                    System.Directory.copyFile newBin (currentBin ++ ".new")
                                    System.Directory.renameFile (currentBin ++ ".new") currentBin
                                    _ <- System.Process.readProcessWithExitCode
                                        "chmod" ["+x", currentBin] ""
                                    putStrLn $ "sky upgrade: upgraded to " ++ tag
                                    return (Right ())


-- | `sky upgrade-claude` — refresh the cwd's CLAUDE.md from the
-- template embedded in this binary at build time. Solves the
-- staleness problem when a user upgrades the sky compiler but
-- their existing project's CLAUDE.md (a snapshot taken at
-- `sky init` time) still references old API names like `Ui.max`
-- (now `Ui.maximum`) or missing surface that landed since.
--
-- Behaviour:
--   * Always overwrites ./CLAUDE.md (the template is what AI
--     assistants consume; users shouldn't be hand-editing it).
--   * Backs up any existing file to CLAUDE.md.bak so accidental
--     local edits aren't lost.
--   * Prints the bytes-changed delta so the user can tell at a
--     glance whether the template actually moved.
runUpgradeClaude :: IO (Either String ())
runUpgradeClaude = do
    let target = "CLAUDE.md"
    existed <- doesFileExist target
    oldSize <- if existed
        then do
            old <- readFile target
            -- Force the read so the rename below sees a consistent file.
            length old `seq` return (length old)
        else return 0
    when existed $ do
        -- Backup. Overwrite any prior .bak so repeated invocations
        -- don't accumulate cruft. The user can recover from a single
        -- mistake; older history belongs in git.
        renameFile target (target ++ ".bak")
    writeFile target embeddedClaudeMd
    let newSize = length embeddedClaudeMd
        verb    = if existed then "Refreshed" else "Created"
    putStrLn $ verb ++ " " ++ target
        ++ " (" ++ show oldSize ++ " → " ++ show newSize ++ " bytes"
        ++ ", from " ++ skyVersionString ++ ")"
    when existed $
        putStrLn $ "  previous version saved as " ++ target ++ ".bak"
    return (Right ())


-- | `sky console [--port N] [--tui]` — materialise the bundled
-- Std.Ui console mini-app, build it (cached per-version) and run.
--
-- Cache layout: @$XDG_CACHE_HOME/sky/console-<version>/@. We use the
-- compiler version string in the dir name so `sky upgrade`
-- auto-invalidates without manual cleanup. Subsequent runs that find
-- @sky-out/app@ already present skip the rebuild step.
--
-- ─── `sky doc` — print or serve API documentation ─────────────
--
-- MVP scope (v0.14.x):
--   * `sky doc`                — summary of the project's index
--   * `sky doc <Module>`       — print one module's surface
--   * `sky doc --list`         — list every module name
--   * `sky doc --serve [-p N]` — TODO: Sky-app spawn (not in MVP)
--
-- The index is built via `Sky.Doc.Index.buildDocIndex` which
-- re-projects the LSP index into a JSON-serializable catalog.
-- For v0.14.x.MVP we ship the terminal subset only; `--serve`
-- is a follow-up commit that adds the bundled Sky.Http.Server
-- app under `sky-bundled/doc/`.
runDoc :: DocOpts -> IO (Either String ())
runDoc opts = do
    cwd <- getCurrentDirectory
    -- Walk up to find sky.toml (project root). If we never find
    -- one, the index will be empty — surface a clear message
    -- and exit with code 2 rather than producing an empty page.
    mRoot <- findProjectRootUpward cwd
    case mRoot of
        Nothing -> do
            hPutStrLn stderr
                "sky doc: no sky.toml found in current directory or any ancestor."
            hPutStrLn stderr
                "         cd into a project (e.g. examples/01-hello-world) and re-run."
            exitWith (ExitFailure 2)
        Just root -> do
            idx <- DocIdx.buildDocIndex skyBuildVersion root
            case (_docServe opts, _docTui opts, _docList opts, _docTarget opts) of
                (True, True, _, _) -> do
                    hPutStrLn stderr
                        "sky doc: --serve and --tui are incompatible (pick one)."
                    exitWith (ExitFailure 2)
                (True, _, _, _) -> runDocServe idx (_docPort opts)
                (_, True, _, _) -> runDocTui idx
                (_, _, True, _) -> do
                    DocTerm.printAllModules idx
                    return (Right ())
                (_, _, _, Just modName) -> do
                    DocTerm.printModule idx modName
                    return (Right ())
                (_, _, _, Nothing) -> do
                    DocTerm.printIndexSummary idx
                    return (Right ())


-- | Render the doc site to a per-version cache directory, then
-- materialise the bundled Sky.Http.Server app and run it pointed
-- at that directory. Mirrors `runConsole`'s spawn pattern.
-- | Install SIGTERM / SIGHUP / SIGINT handlers that forward the
-- signal to a spawned child process: terminate (gentle), wait 1 s,
-- SIGKILL if still alive.  Used by `sky doc --serve|--tui` and
-- `sky console` so an external supervisor's tear-down doesn't
-- orphan the long-lived Sky.Live / Sky.Tui child.
--
-- Windows has no SIGTERM/SIGHUP and `unix` is unavailable, so the
-- helper degrades to a no-op there — the child still dies when its
-- parent's main loop exits (Haskell runtime closes handles on
-- normal shutdown), just without the explicit graceful path.
forwardChildSignals :: System.Process.ProcessHandle -> IO ()
#ifdef mingw32_HOST_OS
forwardChildSignals _ = return ()
#else
forwardChildSignals rph = do
    let install sig = Signals.installHandler sig
            (Signals.Catch $ do
                System.Process.terminateProcess rph
                _ <- Control.Concurrent.forkIO $ do
                    Control.Concurrent.threadDelay 1000000
                    mec <- System.Process.getProcessExitCode rph
                    case mec of
                        Just _  -> return ()
                        Nothing -> do
                            ph <- System.Process.getPid rph
                            case ph of
                                Just pid -> Signals.signalProcess Signals.sigKILL pid
                                Nothing  -> return ()
                return ())
            Nothing
    _ <- install Signals.sigTERM
    _ <- install Signals.sigHUP
    _ <- install Signals.sigINT
    return ()
#endif


runDocServe :: DocIdx.DocIndex -> Int -> IO (Either String ())
runDocServe idx port = do
    -- Render HTML + JSON to .skycache/doc-out/ under the project
    -- root. Re-render every invocation so the docs reflect the
    -- current source state.
    let docOut = DocIdx.diRoot idx </> ".skycache" </> "doc-out"
    DocRender.renderToDir docOut idx

    -- Materialise the bundled doc-server Sky app to a cache dir
    -- (mirrors `runConsole`).
    cache <- System.Directory.getXdgDirectory System.Directory.XdgCache "sky"
    let root   = cache </> ("doc-" ++ skyBuildVersion)
        srcDir = root </> "src"
        binPath = root </> "sky-out" </> "app"
    createDirectoryIfMissing True srcDir
    let writeFileBytes p bytes = do
            createDirectoryIfMissing True (takeDirectory p)
            B.writeFile p bytes
    mapM_ (\(rel, bytes) -> writeFileBytes (root </> rel) bytes)
          Sky.Build.EmbeddedDocServer.embeddedDocServerApp

    skyBin <- System.Environment.getExecutablePath
    haveBin <- doesFileExist binPath
    when (not haveBin) $ do
        putStrLn $ "sky doc: building doc-server (one-time per version, into "
                   ++ root ++ ")..."
        let bp = (System.Process.proc skyBin ["build", "src/Main.sky"])
                    { System.Process.cwd = Just root
                    , System.Process.std_out = System.Process.Inherit
                    , System.Process.std_err = System.Process.Inherit
                    }
        (_, _, _, ph) <- System.Process.createProcess bp
        bec <- System.Process.waitForProcess ph
        case bec of
            ExitSuccess -> return ()
            ExitFailure n -> do
                hPutStrLn stderr $ "sky doc: doc-server build failed (exit "
                                   ++ show n ++ ")"
                exitWith (ExitFailure n)

    env0 <- System.Environment.getEnvironment
    let env1 = filter (\(k, _) -> k `notElem`
                          ["SKY_LIVE_PORT", "SKY_DOC_DIR"]) env0
              ++ [ ("SKY_LIVE_PORT", show port)
                 , ("SKY_DOC_DIR", docOut)
                 ]
        rp = (System.Process.proc binPath [])
                { System.Process.env = Just env1
                , System.Process.std_out = System.Process.Inherit
                , System.Process.std_err = System.Process.Inherit
                , System.Process.std_in  = System.Process.Inherit
                }
    putStrLn $ "sky doc: serving " ++ docOut
            ++ " on http://127.0.0.1:" ++ show port ++ " (Ctrl-C to stop)"
    (_, _, _, rph) <- System.Process.createProcess rp
    -- Reuse runConsole's signal-forwarding so SIGTERM/SIGHUP from
    -- a parent (or external supervisor) tears down the child
    -- cleanly instead of orphaning it to PID 1.
    forwardChildSignals rph
    rec' <- Control.Exception.bracket_ (return ())
        (do
            mec <- System.Process.getProcessExitCode rph
            case mec of
                Just _  -> return ()
                Nothing -> System.Process.terminateProcess rph)
        (System.Process.waitForProcess rph)
    case rec' of
        ExitSuccess     -> return (Right ())
        ExitFailure 130 -> return (Right ())
        ExitFailure 143 -> return (Right ())
        ExitFailure n   -> exitWith (ExitFailure n)


-- | Render the doc site to a cache directory, then build + run the
-- bundled Sky.Tui doc browser pointed at that directory.  Mirrors
-- @runDocServe@'s spawn pattern but uses @src/MainTui.sky@ as the
-- entry point so the cached binary is `app-tui` rather than `app`.
runDocTui :: DocIdx.DocIndex -> IO (Either String ())
runDocTui idx = do
    let docOut = DocIdx.diRoot idx </> ".skycache" </> "doc-out"
    DocRender.renderToDir docOut idx

    cache <- System.Directory.getXdgDirectory System.Directory.XdgCache "sky"
    let root    = cache </> ("doc-" ++ skyBuildVersion)
        srcDir  = root </> "src"
        binPath = root </> "sky-out" </> "app-tui"
    createDirectoryIfMissing True srcDir
    let writeFileBytes p bytes = do
            createDirectoryIfMissing True (takeDirectory p)
            B.writeFile p bytes
    mapM_ (\(rel, bytes) -> writeFileBytes (root </> rel) bytes)
          Sky.Build.EmbeddedDocServer.embeddedDocServerApp

    let haveTui = any (\(p, _) -> p == "src/MainTui.sky")
                       Sky.Build.EmbeddedDocServer.embeddedDocServerApp
    when (not haveTui) $ do
        hPutStrLn stderr
            "sky doc --tui: bundled MainTui.sky missing (rebuild the sky binary)."
        exitWith (ExitFailure 1)

    skyBin  <- System.Environment.getExecutablePath
    haveBin <- doesFileExist binPath
    when (not haveBin) $ do
        putStrLn $ "sky doc: building doc-tui (one-time per version, into "
                   ++ root ++ ")..."
        let bp = (System.Process.proc skyBin ["build", "src/MainTui.sky"])
                    { System.Process.cwd      = Just root
                    , System.Process.std_out  = System.Process.Inherit
                    , System.Process.std_err  = System.Process.Inherit
                    }
        (_, _, _, ph) <- System.Process.createProcess bp
        bec <- System.Process.waitForProcess ph
        case bec of
            ExitSuccess -> do
                -- `sky build` always writes sky-out/app — rename to
                -- the TUI-specific name so HTTP + TUI binaries can
                -- coexist in the same cache dir.
                let built = root </> "sky-out" </> "app"
                ok <- doesFileExist built
                if ok
                    then renameFile built binPath
                    else do
                        hPutStrLn stderr
                            "sky doc: build succeeded but sky-out/app is missing"
                        exitWith (ExitFailure 1)
            ExitFailure n -> do
                hPutStrLn stderr $ "sky doc: doc-tui build failed (exit "
                                   ++ show n ++ ")"
                exitWith (ExitFailure n)

    env0 <- System.Environment.getEnvironment
    let env1 = filter (\(k, _) -> k /= "SKY_DOC_DIR") env0
              ++ [ ("SKY_DOC_DIR", docOut) ]
        rp = (System.Process.proc binPath [])
                { System.Process.env      = Just env1
                , System.Process.std_out  = System.Process.Inherit
                , System.Process.std_err  = System.Process.Inherit
                , System.Process.std_in   = System.Process.Inherit
                }
    putStrLn "sky doc: starting terminal browser (Ctrl-C to exit)..."
    (_, _, _, rph) <- System.Process.createProcess rp
    forwardChildSignals rph
    rec' <- Control.Exception.bracket_ (return ())
        (do
            mec <- System.Process.getProcessExitCode rph
            case mec of
                Just _  -> return ()
                Nothing -> System.Process.terminateProcess rph)
        (System.Process.waitForProcess rph)
    case rec' of
        ExitSuccess     -> return (Right ())
        ExitFailure 130 -> return (Right ())
        ExitFailure 143 -> return (Right ())
        ExitFailure n   -> exitWith (ExitFailure n)


-- | Walk up the directory tree looking for `sky.toml`. Returns
-- the absolute path of the directory containing it, or Nothing
-- at the filesystem root.
findProjectRootUpward :: FilePath -> IO (Maybe FilePath)
findProjectRootUpward dir = do
    let toml = dir </> "sky.toml"
    ok <- doesFileExist toml
    if ok
        then return (Just dir)
        else do
            let parent = takeDirectory dir
            if parent == dir || null parent
                then return Nothing
                else findProjectRootUpward parent


-- --tui swaps the entrypoint (@src/Main.sky@) for the bundled Tui
-- variant (@src/MainTui.sky@) before building. Both share the same
-- @State.sky@ + @View.sky@ — only the app-runner module changes.
-- | `sky console` — v0.16.0 PR 2 deprecation surface.
--
-- The pre-v0.16.0 implementation materialised an embedded Sky source
-- tree (via Sky.Build.EmbeddedConsole), shelled out to itself to
-- `sky build` it (a recursive Go build inside the user's terminal),
-- and ran the resulting binary as a foreground console process.
-- That recursive Go build was the e2-micro OOM cause v0.16.0 is
-- closing — it has been deleted alongside Sky.Build.EmbeddedConsole
-- and `sky-bundled/console/`.
--
-- The replacement is the IN-PROCESS inline console: every Sky.Live
-- and Sky.Http.Server binary now auto-mounts the Sky Console at
-- `/_sky/console` (gated by the production / SKY_CONSOLE_EMBED
-- rules in runtime-go/rt/console.go's MountEmbeddedConsole). Users
-- reach it from any running app's URL — no standalone command
-- needed.
--
-- This stub prints a deprecation notice and exits 0. It is kept
-- (rather than deleted from the CLI grammar) so existing scripts /
-- shell aliases that invoke `sky console` don't fail loudly during
-- the v0.15 → v0.16 transition; they just learn about the new
-- shape. v0.17 may drop the subcommand entirely.
runConsole :: ConsoleOpts -> IO (Either String ())
runConsole _opts = do
    putStrLn ""
    putStrLn "sky console: the standalone command is deprecated in v0.16.0."
    putStrLn ""
    putStrLn "The Sky Console is now embedded in every Sky.Live + Sky.Http.Server"
    putStrLn "binary — reach it at <your-app-origin>/_sky/console."
    putStrLn ""
    putStrLn "Quick start:"
    putStrLn "  cd <your project>"
    putStrLn "  sky run src/Main.sky"
    putStrLn "  # browse http://localhost:<port>/_sky/console"
    putStrLn ""
    putStrLn "See docs/v0.16.x-console/EMBEDDED.md for the migration notes."
    return (Right ())


-- | `sky console-serve` — the Sky Console hub daemon. v0.16.4
-- Chunks 2 + 3 wire the OTLP/HTTP receiver + SQLite hot store.
--
-- Strategy: materialise the TH-embedded runtime-go tree into a
-- version-scoped cache dir under ~/.cache/sky/hub-<version>/, run
-- `go build ./cmd/sky-hub` to produce a standalone hub binary,
-- then exec it with the resolved flag values forwarded. The build
-- is one-shot per Sky version (subsequent invocations short-
-- circuit on a present binary), so steady-state startup is a
-- single process spawn.
--
-- Mirrors the materialise-then-spawn pattern in `runDocServe`
-- (above), but the child here is pure Go — we don't invoke
-- `sky build` on Sky source (there is none for the hub daemon).
--
-- See docs/v0.16.x-console/v0.16.4-IMPLEMENTATION-PLAN.md.
runConsoleServe :: ConsoleServeOpts -> IO (Either String ())
runConsoleServe opts = do
    -- Validate flag combinations up front (fail fast before
    -- materialising anything).
    case (_hubTlsCert opts, _hubTlsKey opts) of
        (Just _, Nothing) -> do
            hPutStrLn stderr "sky console-serve: --tls-cert set but --tls-key missing"
            exitWith (ExitFailure 2)
        (Nothing, Just _) -> do
            hPutStrLn stderr "sky console-serve: --tls-key set but --tls-cert missing"
            exitWith (ExitFailure 2)
        _ -> return ()
    let auth = _hubAuth opts
    when (auth /= "token" && auth /= "off" && auth /= "app") $ do
        hPutStrLn stderr $
            "sky console-serve: unknown --auth mode " ++ show auth
            ++ " (want token|off|app)"
        exitWith (ExitFailure 2)

    -- Materialise the embedded runtime-go tree into the version-
    -- scoped cache root.  Idempotent: an existing cache from a
    -- previous invocation is left in place (writeOne overwrites
    -- per-file, which is cheap on a warm cache).
    cache <- System.Directory.getXdgDirectory System.Directory.XdgCache "sky"
    let root    = cache </> ("hub-" ++ skyBuildVersion)
        binPath = root </> "sky-hub"
    createDirectoryIfMissing True root
    Compile.writeEmbeddedRuntime root

    -- One-shot build per Sky version. Reusing the binary on
    -- repeat invocations keeps the steady-state startup latency
    -- to a single process spawn.
    haveBin <- doesFileExist binPath
    when (not haveBin) $ do
        putStrLn $ "sky console-serve: building hub daemon (one-time per "
                ++ "version, into " ++ root ++ ")..."
        -- CGO_ENABLED=0: the hub doesn't need cgo (modernc.org/sqlite is
        -- pure Go) AND `rt/hub/hub.go` transitively imports `sky-app/rt`
        -- which contains `webview.go` (cgo + WebKit framework on darwin).
        -- The Apple ld_prime linker (Xcode 15+) hits an internal
        -- assertion on the resulting long Go cgo symbol names — see the
        -- 2026-06-08 v0.16.13 macOS CI failure (`ld: Assertion failed:
        -- (name.size() <= maxLength)` in SymbolString.cpp:74). Disabling
        -- cgo routes through `webview_stub.go` (no WebKit linkage), the
        -- ld_prime assertion path goes away, and the resulting sky-hub
        -- binary is functionally identical (the stub is unreachable
        -- because sky-hub never calls webview symbols).
        baseEnv <- System.Environment.getEnvironment
        let buildEnv = ("CGO_ENABLED", "0") : filter ((/= "CGO_ENABLED") . fst) baseEnv
        let bp = (System.Process.proc "go" [ "build", "-o", "sky-hub"
                                            , "./cmd/sky-hub"
                                            ])
                    { System.Process.cwd     = Just root
                    , System.Process.env     = Just buildEnv
                    , System.Process.std_out = System.Process.Inherit
                    , System.Process.std_err = System.Process.Inherit
                    }
        (_, _, _, ph) <- System.Process.createProcess bp
        bec <- System.Process.waitForProcess ph
        case bec of
            ExitSuccess -> return ()
            ExitFailure n -> do
                hPutStrLn stderr $ "sky console-serve: go build sky-hub failed (exit "
                                   ++ show n ++ ")"
                exitWith (ExitFailure n)

    -- Forward flags to the child.  Env carries SKY_CONSOLE_HUB_TOKEN
    -- + SKY_CONSOLE_HUB_MAX_PAYLOAD + SKY_CONSOLE_HUB_RETENTION_HOURS
    -- transparently via the inherited environment.
    let baseArgs =
            [ "--port", show (_hubPort opts)
            , "--data-dir", _hubDataDir opts
            , "--auth", auth
            ]
        tlsArgs = case (_hubTlsCert opts, _hubTlsKey opts) of
            (Just c, Just k) -> [ "--tls-cert", c, "--tls-key", k ]
            _                -> []
        rp = (System.Process.proc binPath (baseArgs ++ tlsArgs))
                { System.Process.std_out = System.Process.Inherit
                , System.Process.std_err = System.Process.Inherit
                , System.Process.std_in  = System.Process.Inherit
                }
    (_, _, _, rph) <- System.Process.createProcess rp
    forwardChildSignals rph
    rec' <- Control.Exception.bracket_ (return ())
        (do
            mec <- System.Process.getProcessExitCode rph
            case mec of
                Just _  -> return ()
                Nothing -> System.Process.terminateProcess rph)
        (System.Process.waitForProcess rph)
    case rec' of
        ExitSuccess     -> return (Right ())
        ExitFailure 130 -> return (Right ())
        ExitFailure 143 -> return (Right ())
        ExitFailure n   -> exitWith (ExitFailure n)


-- | Pull the `"tag_name"` field out of a GitHub release JSON blob. We
-- don't want to depend on aeson here for the upgrade path (keeps the
-- critical self-update code path minimal). Robust to whitespace and
-- surrounding fields — we look for the literal key.
extractTagName :: String -> Maybe String
extractTagName s = go s
  where
    needle = "\"tag_name\""
    go [] = Nothing
    go t@(_:rest)
        | take (length needle) t == needle =
            let afterKey = drop (length needle) t
                afterColon = dropWhile (\c -> c == ':' || c == ' ' || c == '\t') afterKey
            in case afterColon of
                ('"' : rest') -> Just (takeWhile (/= '"') rest')
                _             -> Nothing
        | otherwise = go rest


-- | Identify the current OS + arch in a form that matches our release
-- artefact naming (e.g. `darwin-arm64`, `linux-x64`).
detectPlatform :: IO (String, String)
detectPlatform = do
    (_, unameOs, _) <- System.Process.readProcessWithExitCode "uname" ["-s"] ""
    (_, unameArch, _) <- System.Process.readProcessWithExitCode "uname" ["-m"] ""
    let os = case trim unameOs of
            "Darwin"   -> "darwin"
            "Linux"    -> "linux"
            other      -> map toLowerChar other
        arch = case trim unameArch of
            "arm64"    -> "arm64"
            "aarch64"  -> "arm64"
            "x86_64"   -> "x64"
            "amd64"    -> "x64"
            other      -> other
    return (os, arch)
  where
    trim = dropWhile isSpace . reverse . dropWhile isSpace . reverse
    isSpace c = c == ' ' || c == '\n' || c == '\t' || c == '\r'
    toLowerChar c
        | c >= 'A' && c <= 'Z' = toEnum (fromEnum c + 32)
        | otherwise = c


-- ─── Formatter safety guard (defence in depth, #353) ─────────────────
-- The formatter's post-pass (`preserveTopLevelComments`) now reanchors
-- body comments by EITHER the preceding code line OR the next code
-- line, so the round-trip is lossless for the shapes users actually
-- write. This guard stays as a last-resort net: if a future
-- formatter change drops a substantial fraction of input comments
-- (>5%), refuse to overwrite the file so the user notices instead
-- of silently losing comments.
--
-- A small (< 5%) reduction is allowed because indentation-only
-- restacking can collapse a trailing comment onto an earlier line
-- (e.g. two-line `case ... of\n    -- doc` becoming `case ... of  -- doc`)
-- which legitimately reduces the per-line count.
fmtSafetyCheck :: T.Text -> T.Text -> Maybe String
fmtSafetyCheck srcIn srcOut =
    let commentsBefore = countComments srcIn
        commentsAfter  = countComments srcOut
        threshold      = max 1 (commentsBefore `div` 20)  -- ~5% slack
    in if commentsBefore > commentsAfter + threshold
         then Just $ unlines
             [ "refusing to format: " ++ show commentsBefore
                 ++ " comment line(s) in input but only "
                 ++ show commentsAfter ++ " in output (dropped "
                 ++ show (commentsBefore - commentsAfter) ++ ", threshold "
                 ++ show threshold ++ ")."
             , "This is a sky fmt bug — please report at"
             , "https://github.com/anzellai/sky/issues with the source file."
             , "To format anyway, re-run with SKY_FMT_FORCE=1."
             ]
       else Nothing
  where
    countComments t =
        length [l | l <- map T.strip (T.lines t)
                  , T.pack "--" `T.isPrefixOf` l || T.pack "{-" `T.isPrefixOf` l]

-- ─── Comment preservation across sky fmt ─────────────────────────
-- The parser discards comments before they reach the AST, so
-- Format.formatModule emits output without them. This post-pass
-- scans the original source for comment blocks and re-inserts them
-- into the formatted output, keyed by either:
--   * the next top-level declaration header (for module-level comments)
--   * the preceding code line's stripped text (for body comments inside
--     let / case / etc.).
--
-- Declaration header keys:
--   * `name =` / `name :` / `name arg =`       → "val:name"
--   * `type alias Name = ...`                  → "alias:Name"
--   * `type Name = ...`                        → "type:Name"
--   * `import A.B.C ...`                       → "import:A.B.C"
--   * `module M exposing (...)`                → "module"
--
-- Body-comment anchors use "after:<stripped preceding line>" and are
-- matched on first-occurrence in the output. This gives correct
-- placement for the common case (comments inside let bodies) without
-- needing per-node AST position tracking.
preserveTopLevelComments :: T.Text -> T.Text -> T.Text
preserveTopLevelComments source formatted =
    let -- Assign each body block a stable id; the maps then carry
        -- the id only, and a shared blocks-by-id table holds the
        -- actual comment text. Consumption by either prev- or
        -- next-anchor deletes the id from the table so the other
        -- anchor can't re-emit the same block (#353).
        srcBlocks      = collectCommentBlocks source
        bodyBlocks     = [(i, cs)
                         | (i, (cs, _, _, isH)) <- zip [0::Int ..] srcBlocks
                         , not isH]
        headerMap      = foldl addHeaderBlock Map.empty srcBlocks
        prevAnchorMap  = foldl addPrevAnchorBlock Map.empty
                              (zip [0::Int ..] srcBlocks)
        nextAnchorMap  = foldl addNextAnchorBlock Map.empty
                              (zip [0::Int ..] srcBlocks)
        bodyTable      = Map.fromList bodyBlocks
        trailingMap    = collectTrailingComments source
        outLines       = T.lines formatted
        withTrailing   = map (reattachTrailing trailingMap) outLines
        injected       = injectComments headerMap prevAnchorMap nextAnchorMap
                                        bodyTable withTrailing
    in T.unlines injected
  where
    -- Walk source; for each run of comment/blank lines, produce a
    -- block carrying THREE keys:
    --   * a header-anchor (the line right AFTER the block) when that
    --     line is a top-level decl — comments float above the decl
    --     header in output
    --   * a prev-anchor (the line right BEFORE the block) for body
    --     comments — used when the previous code line survives `sky
    --     fmt` unchanged (typical for short single-line expressions)
    --   * a next-anchor (the line right AFTER the block) for body
    --     comments — used when the previous expression got reflowed
    --     so its text key no longer matches (e.g. a multi-segment
    --     string concat collapsed onto fewer lines under `sky fmt`).
    --     The next anchor is typically a let-binding name or branch
    --     pattern, both of which survive reformatting.
    collectCommentBlocks :: T.Text -> [([T.Text], T.Text, Maybe T.Text, Bool)]
    -- each entry: (commentLines, prevOrHeaderAnchor, maybeNextAnchor, isHeader)
    --   * when isHeader=True the second field is the next code line
    --     (a top-level decl) — `declKey` derives a structural key
    --   * when isHeader=False the second field is the previous code
    --     line (preserves the pre-#353 behaviour); the third field
    --     is the next code line for the fallback path
    collectCommentBlocks t = walk Nothing False False [] (T.lines t)
      where
        walk _prev _prevIsBody _inStr _acc [] = []
        walk prev prevIsBody inStr acc (l:ls)
            -- Inside a `"""` multiline string the line is string
            -- content — never a comment or a declaration. Skipping
            -- it here is what stops a `--`-prefixed string line
            -- from being collected as a comment and re-injected
            -- (duplicated) on every `sky fmt` round-trip.
            | inStr = walk prev prevIsBody (nextInStr inStr l) acc ls
            | isCommentOrBlank l = walk prev prevIsBody inStr (acc ++ [l]) ls
            | isTopLevelDecl l =
                let trimmed = trimBlanks acc
                    anchorKey = stripTrailingComment (T.strip l)
                    rest = walk (Just anchorKey) False (nextInStr False l) [] ls
                    -- #587-followup / regression fence for the "header
                    -- comment stays above the definition" spec.  The
                    -- `prevIsBody` quirk-fix below assumes the comment
                    -- block is IMMEDIATELY adjacent to its previous-body
                    -- line.  But when blank lines separate them, the
                    -- comment is semantically a HEADER for the next
                    -- top-level decl — not a trailer for the previous
                    -- body.  Falling through to the body-anchor path
                    -- under that shape stuck the comment after the
                    -- prev-body call-site line (e.g. `helper x`) instead
                    -- of above the `helper y =` definition.
                    leadingBlanks =
                        length acc -
                        length (dropWhile (T.null . T.strip) acc)
                in if null trimmed
                     then rest
                     -- Quirk fix: when the comment block's
                     -- immediately preceding code line was BODY
                     -- (`prevIsBody`) AND there are NO blank lines
                     -- between prev-body and the comment block, the
                     -- comments belong to that body, not to the next
                     -- decl. The formatter's decl-end blank lines
                     -- made the walker mis-attribute these to the
                     -- next decl's header group, which then printed
                     -- them between the two decls (visually attached
                     -- to the wrong one). Emit as a body block keyed
                     -- to prev (body line) with the upcoming decl
                     -- line as fallback next-anchor.
                     else case prev of
                         Just p | prevIsBody, leadingBlanks == 0 ->
                             (trimmed, p, Just anchorKey, False) : rest
                         _ ->
                             (trimmed, T.strip l, Nothing, True) : rest
            | otherwise =
                let trimmed = trimBlanks acc
                    nextAnchor = stripTrailingComment (T.strip l)
                    rest = walk (Just nextAnchor) True (nextInStr False l) [] ls
                in case (trimmed, prev) of
                    ([], _) -> rest
                    (_, Just p) -> (trimmed, p, Just nextAnchor, False) : rest
                    -- No previous code line (e.g. comments at the
                    -- very top of a `let` body): fall back to using
                    -- the next-anchor as the primary key.
                    (_, Nothing) -> (trimmed, nextAnchor, Just nextAnchor, False) : rest

        -- A line flips the in-multiline-string state when it
        -- contains an odd number of `"""` delimiters.
        nextInStr cur l =
            if odd (T.count (T.pack "\"\"\"") l) then not cur else cur

    -- Strip a trailing "-- comment" from a stripped code line so the
    -- anchor key stays stable across fmt (which drops trailing comments).
    -- Approximate: splits on first "  --" (two-or-more spaces before --)
    -- or "--" at end-of-expression context.
    stripTrailingComment :: T.Text -> T.Text
    stripTrailingComment s =
        case T.breakOn (T.pack "--") s of
            (before, after)
                | T.null after -> s
                | otherwise    ->
                    -- Only treat as comment if preceded by whitespace or at BOL.
                    let rev = T.reverse before
                    in case T.uncons rev of
                        Just (c, _) | c == ' ' || c == '\t' -> T.stripEnd before
                        _ -> s

    -- Build: stripped-code-before-`--`  →  "  -- comment text"
    -- (preserving the exact leading whitespace before the `--` so
    -- reattachment is byte-identical).
    collectTrailingComments :: T.Text -> Map.Map T.Text T.Text
    collectTrailingComments t = foldl step Map.empty (T.lines t)
      where
        step acc fullLine =
            case splitTrailingComment fullLine of
                Nothing -> acc
                Just (codePart, trailingPart) ->
                    let key = T.strip codePart
                    in if T.null key then acc
                       else Map.insertWith (\_ old -> old) key trailingPart acc

    -- Return (codeUpToButNotIncluding "--", "  -- rest of line")
    -- only when the line is not a whole-line comment/blank/block-comment.
    -- Ignores `--` that appears inside a string literal (simple state machine).
    splitTrailingComment :: T.Text -> Maybe (T.Text, T.Text)
    splitTrailingComment fullLine =
        let s = T.strip fullLine
        in if T.null s
              || T.pack "--" `T.isPrefixOf` s
              || T.pack "{-" `T.isPrefixOf` s
             then Nothing
             else scan 0 False (T.unpack fullLine)
      where
        scan _ _ [] = Nothing
        scan i inStr (c:rest)
            | inStr =
                if c == '\\' && not (null rest)
                  then scan (i+2) True (drop 1 rest)
                  else if c == '"' then scan (i+1) False rest
                       else scan (i+1) True rest
            | c == '"' = scan (i+1) True rest
            | c == '-', '-':_ <- rest
            , i > 0
            , precedingIsSpace i fullLine
                = let (code, after) = T.splitAt i fullLine
                  in Just (code, after)
            | otherwise = scan (i+1) False rest

        precedingIsSpace i line =
            case T.uncons (T.reverse (T.take i line)) of
                Just (c, _) -> c == ' ' || c == '\t'
                Nothing     -> False

    reattachTrailing :: Map.Map T.Text T.Text -> T.Text -> T.Text
    reattachTrailing tm l =
        let code = T.stripEnd l
            key  = T.strip code
        in case Map.lookup key tm of
            Just trailing ->
                if T.pack "--" `T.isInfixOf` code
                  then l
                  else T.append code trailing
            Nothing -> l

    trimBlanks = reverse . dropWhile (T.null . T.strip)
               . reverse . dropWhile (T.null . T.strip)

    isCommentOrBlank :: T.Text -> Bool
    isCommentOrBlank l =
        let s = T.strip l
        in T.null s
           || T.pack "--" `T.isPrefixOf` s
           || T.pack "{-" `T.isPrefixOf` s

    -- Top-level decl: starts at col 1 with a keyword or lowercase ident.
    isTopLevelDecl :: T.Text -> Bool
    isTopLevelDecl l =
        case T.uncons l of
            Nothing -> False
            Just (c, _)
                | c == ' ' || c == '\t' -> False
                | otherwise ->
                    let s = T.strip l
                    in T.pack "module " `T.isPrefixOf` s
                       || T.pack "import " `T.isPrefixOf` s
                       || T.pack "type " `T.isPrefixOf` s
                       || T.pack "type alias " `T.isPrefixOf` s
                       || lowercaseHead s

    lowercaseHead :: T.Text -> Bool
    lowercaseHead s = case T.uncons s of
        Just (c, _) -> c >= 'a' && c <= 'z'
        Nothing     -> False

    declKey :: T.Text -> Maybe T.Text
    declKey l =
        let s = T.strip l
        in if T.pack "module " `T.isPrefixOf` s then Just (T.pack "module")
           else if T.pack "type alias " `T.isPrefixOf` s
               then Just (T.append (T.pack "alias:") (firstIdent (T.drop 11 s)))
           else if T.pack "type " `T.isPrefixOf` s
               then Just (T.append (T.pack "type:") (firstIdent (T.drop 5 s)))
           else if T.pack "import " `T.isPrefixOf` s
               then Just (T.append (T.pack "import:") (firstIdent (T.drop 7 s)))
           else if lowercaseHead s
               then Just (T.append (T.pack "val:") (firstIdent s))
           else Nothing

    firstIdent :: T.Text -> T.Text
    firstIdent =
        T.takeWhile (\c -> (c >= 'a' && c <= 'z')
                        || (c >= 'A' && c <= 'Z')
                        || (c >= '0' && c <= '9')
                        || c == '_' || c == '.')
        . T.dropWhile (== ' ')

    -- Header map: decl key → queue of comment blocks (source order).
    addHeaderBlock
        :: Map.Map T.Text [[T.Text]]
        -> ([T.Text], T.Text, Maybe T.Text, Bool)
        -> Map.Map T.Text [[T.Text]]
    addHeaderBlock acc (cs, anchor, _next, isHeader) =
        if not isHeader then acc
        else case declKey anchor of
            Nothing -> acc
            Just k  -> Map.insertWith (\new existing -> existing ++ new) k [cs] acc

    -- Prev-anchor map: stripped preceding-code line → queue of body
    -- block IDs (most-recent-last). This is the original (pre-#353)
    -- anchor; preserved as the primary key for the common case
    -- where the previous code line survives the formatter unchanged.
    -- Storing IDs (rather than text) lets the injector consume an
    -- ID, which then also removes it from the next-anchor queue
    -- via the shared `bodyTable` lookup — preventing double-emit.
    addPrevAnchorBlock
        :: Map.Map T.Text [Int]
        -> (Int, ([T.Text], T.Text, Maybe T.Text, Bool))
        -> Map.Map T.Text [Int]
    addPrevAnchorBlock acc (i, (_cs, prevA, _next, isHeader)) =
        if isHeader then acc
        else Map.insertWith (\new existing -> existing ++ new) prevA [i] acc

    -- Next-anchor map: NORMALISED next-code line → queue of body
    -- block IDs. #353: fallback used when the previous expression
    -- got reflowed under `sky fmt` (e.g. a multi-segment string
    -- concat collapsed) so the prev-anchor key never matches in
    -- the output. The next-anchor (typically a let-binding name or
    -- a type signature like `name : T`) survives reformatting and
    -- lands the comment above the right line.
    --
    -- ONLY binding-shaped next-lines are kept — `name =`, `name :`,
    -- `name args =` etc. — where the leading identifier is the
    -- binding name. Expression-shaped lines (`Process.run ...`,
    -- `case x of`, function applications) are SKIPPED because their
    -- first identifier (`Process`, `case`, `List`, …) is too generic
    -- — matching the first occurrence in the formatted output would
    -- mis-place the comment when the same identifier is used at
    -- multiple call sites earlier in the file.
    addNextAnchorBlock
        :: Map.Map T.Text [Int]
        -> (Int, ([T.Text], T.Text, Maybe T.Text, Bool))
        -> Map.Map T.Text [Int]
    addNextAnchorBlock acc (i, (_cs, _prevA, mNext, isHeader)) =
        case (isHeader, mNext) of
            (False, Just nextA) ->
                case nextAnchorKey nextA of
                    Just k -> Map.insertWith (\new existing -> existing ++ new) k [i] acc
                    Nothing -> acc
            _ -> acc

    -- Compute a next-anchor key from a candidate line.
    -- Returns `Just <ident>` for binding-shaped lines:
    --   * `name =`             → `Just "name"`
    --   * `name : Type`        → `Just "name"`
    --   * `name arg1 arg2 =`   → `Just "name"`
    -- Returns `Just <full-line-strip>` for list-element continuations
    --   * `, "FOO"`            → `Just ", \"FOO\""`
    --   * `, expr ++ x`        → `Just ", expr ++ x"`
    -- List-element lines are common landing slots for #572-style stranded
    -- comments (a doc block between two list elements). The leading
    -- identifier is `,` itself — too generic — so we key on the FULL
    -- stripped line, which is specific (string literals / field-shaped
    -- assignments are nearly unique within their enclosing list).
    -- Returns `Nothing` for OTHER expression-shaped lines (call sites,
    -- `case … of`, operator-led continuations) — those would
    -- mis-match because the identifier is too generic.
    nextAnchorKey :: T.Text -> Maybe T.Text
    nextAnchorKey t =
        let stripped = T.stripStart t
            isIdentCh c = (c >= 'a' && c <= 'z')
                       || (c >= 'A' && c <= 'Z')
                       || (c >= '0' && c <= '9')
                       || c == '_'
            ident = T.takeWhile isIdentCh stripped
            startsWithComma = case T.uncons stripped of
                Just (',', _) -> True
                _             -> False
        in if startsWithComma && T.length stripped > 1
             then Just (T.strip stripped)
             else if T.null ident
                    then Nothing
                    else if isBindingShape stripped
                           then Just ident
                           else Nothing

    -- A binding-shape line has a `=` (after the binder) or a `:`
    -- (top-level type annotation) at the top level. Crude but
    -- effective: scan for ` = ` / ` : ` while tracking nesting in
    -- (), [], {} and skipping string contents.
    isBindingShape :: T.Text -> Bool
    isBindingShape t = scan 0 False (T.unpack t)
      where
        scan _ _ [] = False
        scan d inStr (c:rest)
            | inStr = case c of
                '"'  -> scan d False rest
                '\\' | (_:rest') <- rest -> scan d True rest'
                _    -> scan d True rest
            | otherwise = case c of
                '"' -> scan d True rest
                '(' -> scan (d+1) False rest
                '[' -> scan (d+1) False rest
                '{' -> scan (d+1) False rest
                ')' -> scan (max 0 (d-1)) False rest
                ']' -> scan (max 0 (d-1)) False rest
                '}' -> scan (max 0 (d-1)) False rest
                '=' | d == 0
                    , take 1 rest /= "="           -- not `==`
                    , take 1 rest /= ">"           -- not `=>`
                    -> True
                ':' | d == 0
                    , take 1 rest /= ":"           -- not `::`
                    -> True
                _ -> scan d inStr rest

    -- Walk output lines, splicing comments in at header/anchor matches.
    --
    -- Header map keys → comment text directly (top-level header
    -- comments aren't ambiguous, they only ever fire on the matched
    -- top-level decl).
    --
    -- Prev/Next anchor maps key → block ID; the `bodyTable` resolves
    -- the ID to the comment text. Once an ID is consumed by either
    -- anchor we drop it from the body table so the other anchor can
    -- still POP its queue (head-of-line pointers stay correct) but
    -- skips emitting empty content. This is what stops the same
    -- block from being emitted twice when both anchors match (#353).
    injectComments
        :: Map.Map T.Text [[T.Text]]
        -> Map.Map T.Text [Int]
        -> Map.Map T.Text [Int]
        -> Map.Map Int [T.Text]
        -> [T.Text]
        -> [T.Text]
    injectComments hm0 am0 nm0 bt0 = go hm0 am0 nm0 bt0
      where
        go _  _  _  _  [] = []
        go hm am nm bt (l:ls) =
            -- Header injection fires BEFORE the line — but only when
            -- `l` is a genuine top-level declaration (col 1). Without
            -- the `isTopLevelDecl` guard, `declKey` strips
            -- indentation and would also match an indented *use* of
            -- the name (e.g. `cmdSecret opts rest` inside a `case`),
            -- splicing the decl's header comment in before the first
            -- call site instead of its definition.
            let stripped = T.strip l
                headerHit = case (if isTopLevelDecl l then declKey l else Nothing) of
                    Just k | Just (cs:rest) <- Map.lookup k hm ->
                        let hm' = if null rest then Map.delete k hm
                                               else Map.insert k rest hm
                        in Just (cs, hm')
                    _ -> Nothing
                -- Pop the head ID of a queue and resolve it through
                -- bodyTable. Empty result → block already consumed.
                popQueue mp key bt' = case Map.lookup key mp of
                    Just (i:rest) ->
                        let mp' = if null rest then Map.delete key mp
                                               else Map.insert key rest mp
                        in case Map.lookup i bt' of
                            Just cs -> Just (cs, mp', Map.delete i bt')
                            Nothing -> Nothing   -- already consumed via the other anchor
                    _ -> Nothing
                prevAnchorHit = popQueue am stripped bt
                (prevAnchorRes, bt1, am') = case prevAnchorHit of
                    Just (cs, m, b) -> (Just cs, b, m)
                    Nothing         -> (Nothing, bt, am)
                -- Next-anchor fires INDEPENDENTLY of prev-anchor.
                -- The two anchor BLOCKS are different — bodyTable's
                -- ID-based dedup (Map.delete on consume) prevents
                -- double-emission if the same block somehow ended
                -- up keyed to both anchors. The case where two
                -- DIFFERENT blocks anchor to the same line is real
                -- and load-bearing (#572): inside a list literal,
                -- block N's next-anchor and block N+1's prev-anchor
                -- are the SAME list-element line — without firing
                -- both we starve one of the two blocks.
                nextAnchorHit = case nextAnchorKey l of
                    Just nextK -> popQueue nm nextK bt1
                    Nothing    -> Nothing
                (nextAnchorRes, bt2, nm') = case nextAnchorHit of
                    Just (cs, m, b) -> (Just cs, b, m)
                    Nothing         -> (Nothing, bt1, nm)
            in case (headerHit, prevAnchorRes, nextAnchorRes) of
                -- Header hit + prev-anchor on the same line:
                -- header above, body below. Header comments float
                -- to column 1 (a top-level decl's leading comment).
                -- Body comments keep their source indentation.
                (Just (hcs, hm'), Just acs, Just ncs) ->
                    hcs ++ ncs ++ [l] ++ acs ++ go hm' am' nm' bt2 ls
                (Just (hcs, hm'), Just acs, Nothing) ->
                    hcs ++ [l] ++ acs ++ go hm' am' nm' bt2 ls
                (Just (hcs, hm'), Nothing, Just ncs) ->
                    hcs ++ ncs ++ [l] ++ go hm' am' nm' bt2 ls
                (Just (hcs, hm'), Nothing, Nothing) ->
                    hcs ++ [l] ++ go hm' am' nm' bt2 ls
                -- No header. Next-anchor block emits BEFORE the
                -- line; prev-anchor block emits AFTER the line.
                -- Both can fire simultaneously — see #572.
                (Nothing, Just acs, Just ncs) ->
                    ncs ++ l : acs ++ go hm am' nm' bt2 ls
                (Nothing, Just acs, Nothing) ->
                    l : acs ++ go hm am' nm' bt2 ls
                (Nothing, Nothing, Just ncs) ->
                    -- Place comments ABOVE the matched line, also
                    -- with their source indentation.
                    ncs ++ l : go hm am' nm' bt2 ls
                (Nothing, Nothing, Nothing) ->
                    l : go hm am' nm' bt2 ls

    -- (Removed in #353: comment blocks are now emitted with their
    -- source-original indentation rather than being re-indented to
    -- the anchor line. The previous behaviour broke idempotency
    -- when the anchor line lived at a different indent level from
    -- the comment — common for body comments above a let-binding
    -- where the previous expression's continuation line is deeper
    -- than the binding-level indent.)


-- | Wrap `go build` with stderr capture + Sky-shaped diagnostics.
--
-- The default `callProcess "sh" ["-c", "... go build ..."]` streams Go's
-- raw error to the user and throws on failure. Go's errors for our
-- typed-generic dispatch are particularly cryptic ("cannot use X (any)
-- as int value in argument to rt.List_dropT[int]: need type assertion"),
-- and crucially they indicate a SKY COMPILER BUG — Sky's type system
-- accepted the program, but codegen produced incompatible Go. We:
--   1. Run go build via readCreateProcessWithExitCode (captures stderr)
--   2. Print stderr unchanged (user still sees the raw Go diagnostic)
--   3. Detect known compiler-bug patterns and prepend a Sky-shaped note
--      so the user knows where to file an issue
--   4. Re-raise (via exitWith) so callers see the same failure
runGoBuildWithDiagnostics :: FilePath -> String -> FilePath -> IO ()
runGoBuildWithDiagnostics outDir binName _goPath = do
    -- Prefer a fully static binary: CGO_ENABLED=0 yields an
    -- executable with no libc dependency, so it runs on a
    -- distroless / scratch / Alpine base — smaller, portable
    -- deploy images. Sky's default deps (modernc.org/sqlite, pgx)
    -- are pure Go, so this is the right default.
    --
    -- A few apps DO need cgo — notably Fyne GUI apps (OpenGL
    -- bindings). If the static build fails, transparently retry
    -- with cgo enabled and note it; the resulting binary then
    -- needs a glibc base.
    -- v0.15.2: inject the Sky compiler version into the emitted binary's
    -- rt.skyVersion via -ldflags. Pre-v0.15.2 every sky-built app's
    -- `/_sky/buildinfo` reported `skyVersion: "dev"` regardless of which
    -- Sky compiler had built it — the variable defaults to "dev" and was
    -- only populated by the release CI's `cabal install -ldflags="-X
    -- sky-app/rt.skyVersion=..."` (which only the Sky compiler binary
    -- itself got, not the apps it builds). Propagating the compiler's
    -- own version through `go build` makes any sky-built app report the
    -- truth without per-project deploy-script ceremony.
    --
    -- Quoting: skyBuildVersion is a tag name like `0.15.2` (alphanumerics
    -- + dot only — no shell metacharacters), so single-quoting is enough
    -- to defend against an accidental space without breaking under sh -c.
    let versionLdflag =
            "-ldflags '-X sky-app/rt.skyVersion=" ++ skyBuildVersion ++ "'"
        -- #569: Apple ld64 in macOS Sequoia (Xcode 16+) tightened
        -- the symbol-name length cap to ~16 KB.  Go's inliner
        -- composes nested closure names recursively in mutually-
        -- recursive functions: the Std.Ui.renderElement /
        -- renderNodeAs pair produces symbols like
        -- `…func1.…func1.1.…func1.…`, with each inlining decision
        -- prepending another prefix.  Measured 20.5 MB symbols on
        -- a Std.Ui-importing test fixture before ld64 rejected the
        -- object file (`Assertion failed: name.size() <= maxLength`,
        -- ObjectFileParser::addAtomsForSection → makeNamedAtom →
        -- makeSymbolStringInPlace).  Disabling inlining on darwin
        -- caps the longest symbol at ~300 bytes and unblocks linking.
        -- Linux's GNU ld doesn't have this cap, so we keep the
        -- inliner on for production-perf there.  Cost on darwin:
        -- ~5–10% runtime perf hit from missed inlining — acceptable
        -- given the alternative is an unlinkable binary.  This is
        -- a workaround; the principled fix is to teach Go's symbol
        -- mangler not to compose nested closure names.
        gcflagsForOs
            | System.Info.os == "darwin" = " -gcflags=all=-l"
            | otherwise = ""
        buildCmd cgo =
            "cd " ++ outDir ++ " && CGO_ENABLED=" ++ (if cgo then "1" else "0")
            ++ " go build " ++ versionLdflag ++ gcflagsForOs
            ++ " -o " ++ binName ++ " ."
    -- Some kernels exist as a real `cgo && darwin` implementation +
    -- a `!cgo || !darwin` stub that returns Err Error at runtime.
    -- A static-first build picks up the stub and "succeeds" even
    -- though the resulting binary instantly exits at runtime. For
    -- those kernels we must start with cgo on. The signal is the
    -- presence of `rt.Webview_app` (the only kernel of this shape
    -- today) in the emitted main.go.
    let mainGoPath0 = outDir System.FilePath.</> "main.go"
    mainGoExists0 <- doesFileExist mainGoPath0
    needsCgo <- if mainGoExists0
        then ("rt.Webview_app" `isInfixOf`) <$> readFile mainGoPath0
        else return False
    (ec0, _o0, e0) <- System.Process.readCreateProcessWithExitCode
        (System.Process.shell (buildCmd needsCgo)) ""
    (ec, berr) <- case ec0 of
        System.Exit.ExitSuccess -> do
            when needsCgo $
                putStrLn "  (built with cgo — Sky.Webview requires it; deploy targets must be macOS for v0.1)"
            return (ec0, e0)
        System.Exit.ExitFailure _ | needsCgo ->
            -- Already used cgo on first attempt; the failure is the
            -- real one — don't double-attempt.
            return (ec0, e0)
        System.Exit.ExitFailure _ -> do
            (ec1, _o1, e1) <- System.Process.readCreateProcessWithExitCode
                (System.Process.shell (buildCmd True)) ""
            case ec1 of
                System.Exit.ExitSuccess -> do
                    putStrLn "  (built with cgo — a dependency requires it; deploy images must use a glibc base)"
                    return (ec1, e1)
                -- Both attempts failed → the cgo error is the real
                -- one (the static attempt may just have hit the
                -- cgo gap); diagnose against it.
                System.Exit.ExitFailure _ -> return (ec1, e1)
    case ec of
        System.Exit.ExitSuccess -> return ()
        System.Exit.ExitFailure _ -> do
            -- v0.15.42 (audit §3.4): Sky's own lowering succeeded
            -- but Go rejected the emitted code. Distinguish this
            -- from a successful build so users / CI parsing the
            -- log can tell the two states apart.
            hPutStrLn stderr "Sky lowering succeeded but `go build` failed:"
            hPutStrLn stderr ""
            -- v0.13 Layer 2: when go build fails, parse the Go
            -- error and try to map it back to Sky source via the
            -- SKY-ORIGIN comments in main.go.  If we can resolve,
            -- render a structured Diagnostic with code [E5001]
            -- (go build rejected codegen output) instead of just
            -- dumping the raw Go error to stderr.
            let mainGoPath = outDir System.FilePath.</> "main.go"
            mainGoExists <- doesFileExist mainGoPath
            rendered <- if mainGoExists
                then do
                    goSrc <- readFile mainGoPath
                    let originMap = Validator.parseOriginComments goSrc
                        loc = Validator.parseGoBuildError berr
                    case loc >>= Validator.resolveGoErrorToSky originMap of
                        Just diag -> do
                            r <- Render.renderCli diag
                            return (Just r)
                        Nothing -> return Nothing
                else return Nothing
            case rendered of
                Just r -> do
                    putStrLn r
                    -- Keep the raw Go error too so contributors
                    -- can copy-paste it when filing an issue.
                    hPutStrLn stderr ""
                    hPutStrLn stderr "Raw `go build` output for reference:"
                    hPutStr stderr berr
                Nothing -> do
                    -- Fallback: pre-v0.13 behaviour.
                    hPutStr stderr berr
                    when (isCompilerBugPattern berr) $ do
                        hPutStrLn stderr ""
                        hPutStrLn stderr "─────────────────────────────────────────────────"
                        hPutStrLn stderr "Sky compiler bug detected (go build rejected our"
                        hPutStrLn stderr "generated code). The Sky type system accepted this"
                        hPutStrLn stderr "program; codegen produced incompatible Go. Please"
                        hPutStrLn stderr "file an issue at https://github.com/anzellai/sky/issues"
                        hPutStrLn stderr "with the source + the Go error above."
                        hPutStrLn stderr "─────────────────────────────────────────────────"
            System.Exit.exitWith ec


-- | Recognise the canonical compiler-bug shape: `cannot use X (any) ...
-- in argument to rt.<helper>T[<type>]`. These are typed-codegen
-- monomorphisation gaps — the kernel expects a typed primitive but the
-- caller hands in an any-typed value without coercion. Issue #52 is
-- the case study: List.drop with a record-field-derived Int arg.
isCompilerBugPattern :: String -> Bool
isCompilerBugPattern s =
       ("need type assertion" `isInfixOf` s && "rt." `isInfixOf` s)
    || ("cannot use" `isInfixOf` s && "rt." `isInfixOf` s)
    || ("interface conversion" `isInfixOf` s && "rt." `isInfixOf` s)
