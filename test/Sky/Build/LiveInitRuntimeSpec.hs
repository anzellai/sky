module Sky.Build.LiveInitRuntimeSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing, findExecutable)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..),
                      createProcess, terminateProcess, waitForProcess)
import System.Exit (ExitCode(..))
import qualified Data.List as List
import Control.Concurrent (threadDelay)
import Control.Exception (bracket)


-- v0.16.10 — runtime regression fence for the Sky.Live `init req`
-- lookup contract.  v0.16.7 #417 capitalized the runtime req-map
-- keys without breaking ANY codegen contract spec (because the
-- codegen for `req.path` correctly emits `rt.Field(req, "Path")`),
-- but silently broke every app reading req via Sky-source
-- `Dict.get "path" req` (lowercase literal, case-sensitive Dict_get
-- kernel) — SkyDeploy's OAuth + magic-link sign-in flows hit this
-- exact pattern.
--
-- The existing v0.16.8 codegen contract spec (LiveInitRequestSpec)
-- only asserts the EMITTED Go contains "Cookies" / "Headers" /
-- "Method" string literals; it doesn't catch this class because
-- the codegen WAS correct — only the runtime values flowing
-- through were wrong.  This spec actually builds + runs the
-- binary + curls localhost + parses the rendered HTML — the FULL
-- loop that production runs.  Future runtime changes that break
-- the lookup contract fail here immediately.
spec :: Spec
spec = describe "v0.16.10 — Sky.Live init runtime lookup contract" $ do
    it "Dict.get \"path\" / \"query\" / \"method\" req resolve at runtime" $ do
        skipUnlessCurl $ do
            sky <- findSky
            withSystemTempDirectory "sky-live-init-runtime" $ \tmp -> do
                writeFixture tmp
                (ec, _, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
                case ec of
                    ExitFailure _ ->
                        expectationFailure ("sky build failed:\n" ++ errOut)
                    ExitSuccess -> return ()
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True
                let port = 18099 :: Int
                withRunningBinary (tmp </> "sky-out" </> "app") port $ do
                    threadDelay 3000000  -- 3s warmup for boot
                    body <- curlGet port "/probe/route?token=abc"
                    -- Lowercase Dict.get reads must resolve — exactly
                    -- the pattern SkyDeploy's SsoApp uses for OAuth +
                    -- magic-link sign-in.  If the runtime req-map keys
                    -- change back to capitalized (or anything other
                    -- than lowercase) without an rt.Field fallback,
                    -- these isInfixOf checks fail.
                    ("RUNTIME p=/probe/route" `List.isInfixOf` body)
                        `shouldBe` True
                    ("q=token=abc" `List.isInfixOf` body) `shouldBe` True
                    ("m=GET" `List.isInfixOf` body) `shouldBe` True

  where
    findSky :: IO FilePath
    findSky = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok then return candidate
              else fail ("sky binary missing at " ++ candidate)

    runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
    runSky sky args workDir =
        readCreateProcessWithExitCode
            (proc sky args) { cwd = Just workDir } ""

    writeFixture :: FilePath -> IO ()
    writeFixture dir = do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml")
            ("name = \"live-init-runtime\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") fixture


-- The SkyDeploy SsoApp shape: explicit `Dict String any` annotation
-- on init, then Sky-source `Dict.get "path"` / `Dict.get "query"` /
-- `Dict.get "method"` against req.  This compiles to runtime
-- `Dict_get(key, req)` with case-sensitive map lookup.  The Sky.Live
-- runtime req-map keys MUST stay lowercase for this contract to
-- hold.
fixture :: String
fixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Dict as Dict"
    , "import Std.Cmd as Cmd"
    , "import Std.Html exposing (..)"
    , "import Std.Live exposing (app, route)"
    , "import Std.Sub as Sub"
    , ""
    , "type Page = HomePage"
    , "type Msg = NoOp"
    , ""
    , "type alias Model = { p : String, q : String, m : String }"
    , ""
    , "readField : String -> Dict String any -> String"
    , "readField k req ="
    , "    case Dict.get k req of"
    , "        Just v -> \"\" ++ v"
    , "        Nothing -> \"MISS\""
    , ""
    , "init : Dict String any -> ( Model, Cmd Msg )"
    , "init req ="
    , "    ( { p = readField \"path\" req"
    , "      , q = readField \"query\" req"
    , "      , m = readField \"method\" req"
    , "      }"
    , "    , Cmd.none"
    , "    )"
    , ""
    , "update msg model = case msg of"
    , "    NoOp -> ( model, Cmd.none )"
    , ""
    , "view model ="
    , "    div [] [ text (\"RUNTIME p=\" ++ model.p ++ \" q=\" ++ model.q ++ \" m=\" ++ model.m) ]"
    , ""
    , "subscriptions _ = Sub.none"
    , ""
    , "main ="
    , "    app"
    , "        { init = init"
    , "        , view = view"
    , "        , update = update"
    , "        , subscriptions = subscriptions"
    , "        , routes = [ route \"/\" HomePage ]"
    , "        , notFound = HomePage"
    , "        }"
    ]


-- ─── helpers ──────────────────────────────────────────────────


-- Spawn the binary with SKY_LIVE_PORT set, run the inner action,
-- then SIGTERM the binary.  Any exception in the inner action
-- propagates after cleanup.
withRunningBinary :: FilePath -> Int -> IO a -> IO a
withRunningBinary exe port action = bracket
    (createProcess (proc exe []) { env = Just env })
    (\(_, _, _, ph) -> do
        terminateProcess ph
        _ <- waitForProcess ph
        return ())
    (\_ -> action)
  where
    env =
        [ ("SKY_LIVE_PORT", show port)
        , ("PATH", "/usr/bin:/bin:/usr/local/bin")
        , ("HOME", "/tmp")
        ]


-- Curl-based HTTP GET.  Tied to system curl so we don't have to
-- pull in network-simple / http-client; every dev box has curl
-- and CI runners on macOS / Ubuntu / Windows all ship it.
curlGet :: Int -> String -> IO String
curlGet port path = do
    let url = "http://127.0.0.1:" ++ show port ++ path
    (_, sout, _) <- readCreateProcessWithExitCode
        (proc "curl" ["-s", "--max-time", "5", url]) ""
    return sout


-- Skip the spec when curl is missing (defensive; never expected in
-- normal dev/CI).  Uses pendingWith so the runner reports it as a
-- known skip instead of a silent pass.
skipUnlessCurl :: IO () -> IO ()
skipUnlessCurl body = do
    mCurl <- findExecutable "curl"
    case mCurl of
        Nothing -> pendingWith "curl missing — skipping runtime probe"
        Just _  -> body
