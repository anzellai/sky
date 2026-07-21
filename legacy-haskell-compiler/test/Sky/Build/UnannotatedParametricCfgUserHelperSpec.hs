module Sky.Build.UnannotatedParametricCfgUserHelperSpec (spec) where

-- Issue #521 corner-case sibling — same root cause (enclosing-scope
-- TVar erasure) but a different call shape: an unannotated `view`
-- calls a user-defined helper passing `cfg` and `cfg.<callback>` as
-- positional args.  Pre-fix the helper-call's 2nd arg was lowered
-- through coerceCallArgsAt, which erased the enclosing T1 to `any`
-- and emitted the `any(cfg).(Widget_Cfg_R[any])` panic shape.
--
-- This shape was not exercised by UnannotatedParametricCfgViewSpec
-- (which routes through Std.Ui kernel calls).  Adding it as a
-- regression locks down the user-defined-helper path independently.

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
          else fail ("sky binary missing at " ++ candidate
                  ++ " — run cabal install --installdir=./sky-out first")


runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
runSky sky args workDir = do
    let cp = (proc sky args) { cwd = Just workDir }
    readCreateProcessWithExitCode cp ""


writeFixture :: FilePath -> IO ()
writeFixture dir = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml") $ unlines
        [ "name = \"corner521\""
        , "version = \"0.1.0\""
        , "entry = \"src/Main.sky\""
        ]
    writeFile (dir </> "src" </> "Widget.sky") $ unlines
        [ "module Widget exposing (Cfg, view)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , ""
        , "type alias Cfg msg ="
        , "    { onCheck : msg, onDismiss : msg, label : String }"
        , ""
        , "-- 2-arg user-defined helper; 2nd arg flows in as bare TVar."
        , "toolbar cfg onCheck ="
        , "    if String.isEmpty cfg.label then onCheck else cfg.onDismiss"
        , ""
        , "-- DELIBERATELY UNANNOTATED.  HM should infer"
        , "-- view : Cfg msg -> msg.  The call below passes cfg + cfg.onCheck"
        , "-- positionally — pre-#521 fix this lowered to"
        , "-- `Widget_toolbar(any(cfg).(Widget_Cfg_R[any]), …)`."
        , "view cfg ="
        , "    let check = toolbar cfg cfg.onCheck"
        , "    in check"
        ]
    writeFile (dir </> "src" </> "Main.sky") $ unlines
        [ "module Main exposing (main)"
        , ""
        , "import Sky.Core.Prelude exposing (..)"
        , "import Std.Log exposing (println)"
        , "import Widget"
        , ""
        , "type Msg = ClickIt | DismissIt"
        , ""
        , "demo : Widget.Cfg Msg"
        , "demo = { onCheck = ClickIt, onDismiss = DismissIt, label = \"\" }"
        , ""
        , "main ="
        , "    let _ = Widget.view demo"
        , "    in println \"ok\""
        ]


spec :: Spec
spec = describe "Issue #521 corner case — user-defined helper" $ do

    it "the user-helper fixture builds clean" $
      withSystemTempDirectory "sky-521-userhelper-build" $ \dir -> do
        writeFixture dir
        sky <- findSky
        (exit, stdout', stderr') <- runSky sky ["build", "src/Main.sky"] dir
        let combined = stdout' ++ "\n" ++ stderr'
        ("Compilation successful" `isInfixOf` combined) `shouldBe` True
        exit `shouldBe` ExitSuccess

    it "the generic view body preserves a TVar cast at the helper-call \
       \arg (proof the enclosing-scope guard fired through coerceCallArgsAt)" $
      withSystemTempDirectory "sky-521-userhelper-tvar" $ \dir -> do
        writeFixture dir
        sky <- findSky
        (exit, _stdout, _stderr) <- runSky sky ["build", "src/Main.sky"] dir
        exit `shouldBe` ExitSuccess
        emitted <- readFile (dir </> "sky-out" </> "main.go")
        let preservedTVar tn =
              ("Widget_toolbar(cfg, rt.Coerce[" ++ tn ++ "](cfg.OnCheck))")
                `isInfixOf` emitted
        (preservedTVar "T1" || preservedTVar "T2") `shouldBe` True

    it "the emitted body carries no `any(cfg).(Widget_Cfg_R[any])` \
       \panic shape" $
      withSystemTempDirectory "sky-521-userhelper-nopanic" $ \dir -> do
        writeFixture dir
        sky <- findSky
        (exit, _stdout, _stderr) <- runSky sky ["build", "src/Main.sky"] dir
        exit `shouldBe` ExitSuccess
        emitted <- readFile (dir </> "sky-out" </> "main.go")
        let panicShape = "any(cfg).(Widget_Cfg_R[any])"
        (panicShape `isInfixOf` emitted) `shouldBe` False

    it "the specialised wrapper substitutes T1 → Msg cleanly \
       \at the helper-call site (end-to-end runtime safety)" $
      withSystemTempDirectory "sky-521-userhelper-spec" $ \dir -> do
        writeFixture dir
        sky <- findSky
        (exit, _stdout, _stderr) <- runSky sky ["build", "src/Main.sky"] dir
        exit `shouldBe` ExitSuccess
        emitted <- readFile (dir </> "sky-out" </> "main.go")
        let specialisedShape =
              "Widget_toolbar(cfg, rt.Coerce[Msg](cfg.OnCheck))"
        (specialisedShape `isInfixOf` emitted) `shouldBe` True
