module Sky.Build.LiveNavigationSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import qualified Data.List as List


-- v0.16.7 #417 + #418 — Sky.Live navigation contract widening.
--
-- #417: `init`'s `req` arg gains a `params : Dict String String`
-- field keyed by the route pattern's `:name` segments.  No-arg
-- page constructors can now access URL params directly from
-- `req.params` without needing to switch to function-typed
-- constructors.  Routes with no `:name` segments get an empty
-- Dict.
--
-- #418: A new optional `onNavigate : Page -> msg` field on the
-- `Live.app` cfg lets apps react to URL-driven route changes.
-- The framework dispatches the resulting Msg through `update`
-- after every `applyRoute` call (initial mount, sky-nav clicks,
-- popstate Back/Forward).
--
-- This spec asserts BOTH compile cleanly when used together — the
-- row-poly extension pattern preserves the typed shape, and a
-- Sky.Live app with the new fields builds + lowers + go-builds
-- successfully.  Behavioural verification would require a running
-- server + browser; the build-and-emit gate here is the codegen
-- contract test.
spec :: Spec
spec = describe "v0.16.7 #417 + #418 — Sky.Live navigation contract" $ do
    it "Sky.Live app using req.params + onNavigate compiles cleanly" $ do
        sky <- findSky
        withSystemTempDirectory "sky-live-nav" $ \tmp -> do
            writeFixture tmp
            (ec, _, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            case ec of
                ExitFailure _ ->
                    expectationFailure ("sky build failed:\n" ++ errOut)
                ExitSuccess -> return ()
            built <- doesFileExist (tmp </> "sky-out" </> "app")
            built `shouldBe` True

            -- The emitted Go must reference the new runtime fields
            -- + dispatch site so the runtime cgo / link path is
            -- exercised.  Catches a future field-rename or
            -- accidental-skip regression.
            goSrc <- readFile (tmp </> "sky-out" </> "main.go")
            -- onNavigate field threaded into the cfg literal.
            ("OnNavigate:" `List.isInfixOf` goSrc) `shouldBe` True

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
            ("name = \"live-nav\"\nversion = \"0.0.0\"\n"
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
    , "type Page"
    , "    = HomePage"
    , "    | ItemPage              -- no-arg ctor; reads slug from req.params"
    , ""
    , "type Msg"
    , "    = NavigatedTo Page"
    , ""
    , "type alias Model ="
    , "    { page : Page"
    , "    , slug : String"
    , "    }"
    , ""
    , "init : a -> ( Model, Cmd Msg )"
    , "init req ="
    , "    let"
    , "        -- #417: read the route param from the new req.params"
    , "        -- Dict.  Empty Dict.get returns Nothing; default to \"\"."
    , "        slug ="
    , "            Maybe.withDefault \"\" (Dict.get \"slug\" req.params)"
    , "    in"
    , "    ( { page = HomePage, slug = slug }, Cmd.none )"
    , ""
    , "update : Msg -> Model -> ( Model, Cmd Msg )"
    , "update msg model ="
    , "    case msg of"
    , "        NavigatedTo p ->"
    , "            ( { model | page = p }, Cmd.none )"
    , ""
    , "view : Model -> Html Msg"
    , "view model ="
    , "    div [] [ text (\"slug=\" ++ model.slug) ]"
    , ""
    , "subscriptions : Model -> Sub Msg"
    , "subscriptions _model ="
    , "    Sub.none"
    , ""
    , "-- #418: onNavigate fires after every URL-driven applyRoute"
    , "-- so the app can react with a typed Msg.  Page -> Msg here."
    , "onNavigate : Page -> Msg"
    , "onNavigate p ="
    , "    NavigatedTo p"
    , ""
    , "main ="
    , "    app"
    , "        { init = init"
    , "        , update = update"
    , "        , view = view"
    , "        , subscriptions = subscriptions"
    , "        , routes = ["
    , "              route \"/\" HomePage"
    , "            , route \"/item/:slug\" ItemPage"
    , "            ]"
    , "        , notFound = HomePage"
    , "        , onNavigate = onNavigate"
    , "        }"
    ]
