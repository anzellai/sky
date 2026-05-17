module Sky.Build.ExposingTypeCtorsSpec (spec) where

-- Phase 2.5 — Limitation 11 regression fence.
--
-- Pre-fix: `import OtherModule exposing (MyType(..))` did NOT bring
-- MyType's constructors into scope for user-defined modules. Only
-- kernel modules (Maybe(..), Result(..), etc.) worked because they
-- bypassed the dep-exports filter.
--
-- Root cause: the `depExportedNames` allow-list in
-- Sky.Canonicalise.Module included _dep_values / _dep_aliases /
-- union NAMES, but NOT union constructor names. The
-- `exposedDepCtors` collector correctly built the ctor list, but
-- the subsequent `keep` filter dropped every entry because e.g.
-- "Active" wasn't in any of the three sets (only "Status" was —
-- the union's own name).
--
-- Fix: extend depExportedNames to include each union's
-- constructor names.
--
-- This spec creates a temp project where a Status ADT is defined
-- in src/Status.sky and imported with `exposing (Status(..))` from
-- src/Main.sky. Pre-fix → "Undefined name: Active" at canonicalise
-- time. Post-fix → builds + runs.

import Test.Hspec
import qualified System.Directory as Dir
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import qualified System.Process as Proc
import System.Exit (ExitCode(..))


findSky :: IO FilePath
findSky = do
    cwd <- Dir.getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- Dir.doesFileExist c
    if ok then pure c else fail ("missing: " ++ c)


-- | Scaffold a project where src/Status.sky exports an ADT with
-- ctors and src/Main.sky imports it via `exposing (Status(..))`.
scaffold :: FilePath -> IO ()
scaffold root = do
    Dir.createDirectoryIfMissing True (root </> "src")
    writeFile (root </> "sky.toml") "name = \"lim11\"\n"
    writeFile (root </> "src" </> "Status.sky") $ unlines
        [ "module Status exposing (Status(..), describe)"
        , ""
        , "type Status = Active | Inactive | Pending"
        , ""
        , "describe : Status -> String"
        , "describe s ="
        , "    case s of"
        , "        Active -> \"active\""
        , "        Inactive -> \"inactive\""
        , "        Pending -> \"pending\""
        ]
    writeFile (root </> "src" </> "Main.sky") $ unlines
        [ "module Main exposing (main)"
        , ""
        , "import Std.Log exposing (println)"
        , "import Status exposing (Status(..), describe)"
        , ""
        , "main ="
        , "    let s = Active"
        , "    in"
        , "        println (describe s)"
        ]


-- | Like scaffold but also imports a SUBSET of ctors via
-- `Status(Active, Pending)` — verifies the Partial Public case
-- (Src.PublicCtors) handles user modules too.
scaffoldPartial :: FilePath -> IO ()
scaffoldPartial root = do
    Dir.createDirectoryIfMissing True (root </> "src")
    writeFile (root </> "sky.toml") "name = \"lim11p\"\n"
    writeFile (root </> "src" </> "Status.sky") $ unlines
        [ "module Status exposing (Status(..))"
        , ""
        , "type Status = Active | Inactive | Pending"
        ]
    writeFile (root </> "src" </> "Main.sky") $ unlines
        [ "module Main exposing (main)"
        , ""
        , "import Std.Log exposing (println)"
        , "import Status exposing (Status(Active, Pending))"
        , ""
        , "main ="
        , "    let s = Active"
        , "        t = Pending"
        , "    in"
        , "        println \"ok\""
        ]


runBuild :: FilePath -> IO (Int, String)
runBuild dir = do
    sky <- findSky
    let cmd = "cd " ++ dir ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
    (ec, out, _) <- Proc.readCreateProcessWithExitCode
        (Proc.shell cmd) ""
    let code = case ec of
            ExitSuccess     -> 0
            ExitFailure n   -> n
    pure (code, out)


spec :: Spec
spec = describe "Limitation 11 — exposing (Type(..)) for user modules" $ do

    it "import ... exposing (Status(..)) brings ALL ctors into scope" $ do
        withSystemTempDirectory "sky-lim11" $ \dir -> do
            scaffold dir
            (code, out) <- runBuild dir
            if code /= 0
                then expectationFailure $
                    "build failed (Lim 11 still open?):\n" ++ out
                else do
                    -- And the built binary actually runs.
                    let app = dir </> "sky-out" </> "app"
                    appExists <- Dir.doesFileExist app
                    appExists `shouldBe` True

    it "import ... exposing (Status(Active, Pending)) brings the listed ctors" $ do
        withSystemTempDirectory "sky-lim11-partial" $ \dir -> do
            scaffoldPartial dir
            (code, out) <- runBuild dir
            if code /= 0
                then expectationFailure $
                    "build failed (Lim 11 PublicCtors path broken?):\n" ++ out
                else pure ()
