module Sky.Build.PureModuleSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- v0.15.50 regression — Sky.Core.Pure additive mirror module.
--
-- Every Pure.* wrapper must:
--
--   1. Type-check against its declared `() -> Task Error a` signature
--      (no upstream Sky-source breakage).
--
--   2. Emit typed-Go that preserves the typed `SkyTask[Error, T]` shape
--      end-to-end (no `any` widening from the Pure wrappers — the
--      wrapper itself must lower with `rt.SkyTask[Sky_Core_Error_Error,
--      <T>]` return type, not `rt.SkyTask[any, any]`).
--
--   3. Resolve to the existing canonical kernel symbol underneath:
--      Pure.timeNow ⇒ rt.Time_now, Pure.systemCwd ⇒ rt.System_cwd, etc.
--      The wrappers must not bring in new runtime entry points.
--
-- This spec compiles a tiny fixture that exercises six of the nine
-- Pure.* surfaces and asserts the emitted Go matches the typed
-- shape + reuses the canonical kernel names.
spec :: Spec
spec = describe "Sky.Core.Pure (v0.15.50 additive mirror)" $ do
    it "lowers each wrapper as a typed SkyTask[Error, T] re-routing to the canonical kernel" $ do
        sky <- findSky
        withSystemTempDirectory "sky-pure-module" $ \tmp -> do
            writeFixture tmp
            (ec, out, errOut) <- runSky sky ["build", "src/Main.sky"] tmp
            if ec /= ExitSuccess
              then expectationFailure $
                  "sky build failed.\n" ++ out ++ "\n" ++ errOut
              else do
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True
                body <- readFile (tmp </> "sky-out" </> "main.go")

                -- Typed-Go gate: every wrapper preserves the typed
                -- SkyTask[Sky_Core_Error_Error, <T>] return shape.
                let typedUuid =
                        "Sky_Core_Pure_uuidV4(_ struct{}) rt.SkyTask[Sky_Core_Error_Error, string]"
                            `isInfixOf` body
                    typedTime =
                        "Sky_Core_Pure_timeNow(_ struct{}) rt.SkyTask[Sky_Core_Error_Error, int]"
                            `isInfixOf` body
                    typedCwd =
                        "Sky_Core_Pure_systemCwd(_ struct{}) rt.SkyTask[Sky_Core_Error_Error, string]"
                            `isInfixOf` body
                    typedArgs =
                        "Sky_Core_Pure_systemArgs(_ struct{}) rt.SkyTask[Sky_Core_Error_Error, []string]"
                            `isInfixOf` body

                typedUuid  `shouldBe` True
                typedTime  `shouldBe` True
                typedCwd   `shouldBe` True
                typedArgs  `shouldBe` True

                -- Kernel-reuse gate: the wrapper bodies route through
                -- the canonical kernel symbols (not via Ffi_kernel
                -- runtime panic stub).
                let reusesTimeNow   = "rt.Time_now(struct{}{})"     `isInfixOf` body
                    reusesSystemCwd = "rt.System_cwd(struct{}{})"   `isInfixOf` body
                    reusesUuidV4    = "rt.Uuid_v4()"                `isInfixOf` body

                reusesTimeNow   `shouldBe` True
                reusesSystemCwd `shouldBe` True
                reusesUuidV4    `shouldBe` True

                -- Anti-`any` gate: no Pure wrapper emits the wildcard
                -- `SkyTask[any, any]` shape (would indicate the typed
                -- lowering lost the slot's expected return type).
                let widenedTaskAny =
                        "Sky_Core_Pure_timeNow(_ struct{}) rt.SkyTask[any, any]"
                            `isInfixOf` body
                widenedTaskAny `shouldBe` False

                -- Smoke-run: the fixture prints "ok" iff Pure.timeNow
                -- + Pure.systemCwd ran cleanly.
                (rc, runOut, _) <- runApp tmp
                rc `shouldBe` ExitSuccess
                ("ok" `isInfixOf` runOut) `shouldBe` True

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

    runApp :: FilePath -> IO (ExitCode, String, String)
    runApp dir = do
        let cp = (proc (dir </> "sky-out" </> "app") []) { cwd = Just dir }
        readCreateProcessWithExitCode cp ""

    writeFixture :: FilePath -> IO ()
    writeFixture dir = do
        createDirectoryIfMissing True (dir </> "src")
        writeFile (dir </> "sky.toml") $ unlines
            [ "[project]"
            , "name = \"pure-module-test\""
            , ""
            , "[bin]"
            , "name = \"app\""
            ]
        writeFile (dir </> "src" </> "Main.sky") $ unlines
            [ "module Main exposing (main)"
            , ""
            , "import Sky.Core.Prelude exposing (..)"
            , "import Sky.Core.Pure as Pure"
            , "import Sky.Core.Task as Task"
            , "import Std.Log exposing (println)"
            , ""
            , ""
            , "main ="
            , "    Pure.timeNow ()"
            , "        |> Task.andThen (\\_ -> Pure.systemCwd ())"
            , "        |> Task.andThen (\\_ -> Pure.uuidV4 ())"
            , "        |> Task.andThen (\\_ -> Pure.systemArgs ())"
            , "        |> Task.andThen (\\_ -> println \"ok\")"
            ]
