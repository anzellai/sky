module Sky.Build.LiveInitRequestSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import qualified Data.List as List


-- v0.16.8 #423 — Sky.Live init request shape widening.
--
-- Pre-v0.16.8, dispatchRoot's init `req` map carried only `Path` /
-- `Query` / `Params`.  Apps with auth had to either Cmd.perform a
-- /api/whoami fetch then re-render (extra round-trip + UX flash) or
-- pass auth via query params (fragile).  Now the runtime populates
-- `req` with `Method : String`, `Headers : Dict String String`,
-- `Cookies : Dict String String` too.  Session-bootstrap at first
-- render is a single Dict.get away.
spec :: Spec
spec = describe "v0.16.8 #423 — Sky.Live init request shape" $ do
    it "init can read method + cookies + headers from req" $ do
        sky <- findSky
        withSystemTempDirectory "sky-live-init-req" $ \tmp -> do
            writeFixture tmp
            (ec, _, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            case ec of
                ExitFailure _ ->
                    expectationFailure ("sky build failed:\n" ++ errOut)
                ExitSuccess -> return ()
            built <- doesFileExist (tmp </> "sky-out" </> "app")
            built `shouldBe` True

            goSrc <- readFile (tmp </> "sky-out" </> "main.go")
            -- All three new accessors must lower into Field reads.
            ("\"Cookies\"" `List.isInfixOf` goSrc) `shouldBe` True
            ("\"Headers\"" `List.isInfixOf` goSrc) `shouldBe` True
            ("\"Method\"" `List.isInfixOf` goSrc) `shouldBe` True

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
            ("name = \"live-init-req\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") fixture


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
    , "type alias Model ="
    , "    { method : String"
    , "    , sid : String"
    , "    , ua : String"
    , "    }"
    , ""
    , "init req ="
    , "    let"
    , "        sid = Maybe.withDefault \"anon\" (Dict.get \"sky_sid\" req.cookies)"
    , "        ua  = Maybe.withDefault \"none\" (Dict.get \"User-Agent\" req.headers)"
    , "        m   = req.method"
    , "    in"
    , "    ( { method = m, sid = sid, ua = ua }, Cmd.none )"
    , ""
    , "update msg model = case msg of"
    , "    NoOp -> ( model, Cmd.none )"
    , ""
    , "view model ="
    , "    div []"
    , "        [ p [] [ text (\"method=\" ++ model.method) ]"
    , "        , p [] [ text (\"sid=\" ++ model.sid) ]"
    , "        , p [] [ text (\"ua=\" ++ model.ua) ]"
    , "        ]"
    , ""
    , "subscriptions _ = Sub.none"
    , ""
    , "main ="
    , "    app"
    , "        { init = init"
    , "        , update = update"
    , "        , view = view"
    , "        , subscriptions = subscriptions"
    , "        , routes = [ route \"/\" HomePage ]"
    , "        , notFound = HomePage"
    , "        }"
    ]
