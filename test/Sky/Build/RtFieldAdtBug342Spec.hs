module Sky.Build.RtFieldAdtBug342Spec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


-- Regression for bug #342: `rt.Field record "PageField"` reading a
-- user-defined ADT-typed model field through a polymorphic dep
-- function (`Sky.Test.equal : a -> a -> TestResult`) emitted as
-- `Sky_Test_equal(Live_CounterTest_Page_CounterPage, rt.Field(...))`
-- with NO coerce wrap around `rt.Field`.  `rt.Field` returns `any`;
-- Go's call-site inference pinned `T1 = Live_CounterTest_Page` from
-- the first arg and then rejected the second with `type any of
-- rt.Field(...) does not match inferred type Live_CounterTest_Page
-- for T1`.
--
-- Root cause: `betterTypeStr` in src/Sky/Build/Compile.hs preferred
-- `"any"` over a bare TVar `"T1"`.  When the param-types merger
-- compared the HM-inferred dep entry `["T1","T1"]` against the
-- early collector's TVar-erased entry `["any","any"]`, it picked
-- `["any","any"]` — collapsing the polymorphic Sky_Test_equal
-- signature to `(any, any)`.  Downstream `coerceCallArgsAt`
-- fallback then never emitted the needed `rt.Coerce[T1]` around
-- `rt.Field(...)`.
--
-- Fix: `betterTypeStr` now treats a bare TVar as carrying STRICTLY
-- MORE info than `any` (preserves polymorphism for call-site σ-
-- recovery); concrete > TVar > any.
spec :: Spec
spec = describe "bug #342 — rt.Field on ADT model field via polymorphic dep" $ do
    it "compiles + runs Sky_Test_equal(AdtCtor, rt.Field(model, \"Page\"))" $ do
        sky <- findSky
        withSystemTempDirectory "sky-bug-342" $ \tmp -> do
            writeFixture tmp
            (ec, _, errOut) <- runSky sky ["test", "tests/Live/CounterTest.sky"] tmp
            if ec /= ExitSuccess
              then expectationFailure ("sky test failed:\n" ++ errOut)
              else do
                built <- doesFileExist (tmp </> "sky-out" </> "main.go")
                built `shouldBe` True

  where
    findSky :: IO FilePath
    findSky = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok then return candidate
              else fail ("sky binary missing at " ++ candidate)

    runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
    runSky sky args workDir = do
        let cp = (proc sky args) { cwd = Just workDir }
        readCreateProcessWithExitCode cp ""

    writeFixture :: FilePath -> IO ()
    writeFixture dir = do
        createDirectoryIfMissing True (dir </> "src")
        createDirectoryIfMissing True (dir </> "tests" </> "Live")
        writeFile (dir </> "sky.toml")
            ("name = \"bug-342-fixture\"\nversion = \"0.0.0\"\n"
             ++ "entry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
        writeFile (dir </> "src" </> "Main.sky") mainSrc
        writeFile (dir </> "tests" </> "Live" </> "CounterTest.sky") testSrc


-- ─── Fixtures ──────────────────────────────────────────────────

-- A trivial entry — keeps the project shape valid for `sky test`.
mainSrc :: String
mainSrc = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "main ="
    , "    println \"bug-342 fixture\""
    ]

-- Minimal reproducer of bug #342: a model record with an ADT field
-- read via `model.page` (Sky), then compared via the polymorphic
-- `Sky.Test.equal`.  Pre-fix the compiled Go failed to build with
-- `type any of rt.Field(...) does not match inferred type ... for T1`.
testSrc :: String
testSrc = unlines
    [ "module Live.CounterTest exposing (tests)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Test as Test exposing (Test)"
    , ""
    , "type Page = HomePage | AboutPage"
    , ""
    , "type alias Model = { page : Page, count : Int }"
    , ""
    , "initial : Model"
    , "initial = { page = HomePage, count = 0 }"
    , ""
    , "tests : List Test"
    , "tests ="
    , "    [ Test.test \"adt field reads typed\" (\\_ ->"
    , "        Test.equal HomePage initial.page)"
    , "    , Test.test \"adt field after let\" (\\_ ->"
    , "        let model = { initial | page = AboutPage } in"
    , "        Test.equal AboutPage model.page)"
    , "    ]"
    ]
