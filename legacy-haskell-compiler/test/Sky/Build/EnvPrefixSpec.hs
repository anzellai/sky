module Sky.Build.EnvPrefixSpec (spec) where

-- Regression fence for sky.toml `[env] prefix = "..."` namespacing.
--
-- Pre-fix bug class: every Sky binary read SKY_LIVE_PORT,
-- SKY_AUTH_TOKEN_TTL, SKY_LOG_FORMAT, etc. — meaning two Sky apps on
-- the same host shared the same env-var namespace. Setting
-- SKY_LIVE_PORT for one app affected every other Sky app on the
-- same shell. Workaround was Go FFI to set per-app env vars before
-- Live.app ran.
--
-- Fix: `[env] prefix = "FENCE"` in sky.toml seeds a runtime call
-- `rt.SetEnvPrefix("FENCE")` at the top of the generated init().
-- Subsequent `rt.SetSkyDefault("LIVE_PORT", "8000")` then sets
-- `FENCE_LIVE_PORT=8000`, and the runtime reads via the same
-- prefix. Default unchanged ("SKY") when the key is absent —
-- backwards compatible for every existing Sky project.

import Test.Hspec
import qualified System.Exit as Exit
import System.Directory (getCurrentDirectory, doesFileExist, createDirectoryIfMissing)
import System.FilePath ((</>))
import System.Process (readCreateProcessWithExitCode, shell)
import System.IO.Temp (withSystemTempDirectory)
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- Build a minimal Sky project with the supplied sky.toml +
-- src/Main.sky and return (build exit code, build stdout+stderr,
-- generated main.go contents).
buildAndReadGenerated :: String -> String -> IO (Int, String, String)
buildAndReadGenerated tomlBody srcBody =
    withSystemTempDirectory "sky-env-prefix" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "sky.toml") tomlBody
        writeFile (tmp </> "src" </> "Main.sky") srcBody
        let buildCmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (bec, bout, berr) <- readCreateProcessWithExitCode (shell buildCmd) ""
        let buildOut = bout ++ berr
            bInt = case bec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        if bInt /= 0
            then return (bInt, buildOut, "")
            else do
                gen <- readFile (tmp </> "sky-out" </> "main.go")
                _ <- length gen `seq` return ()
                return (0, buildOut, gen)


-- Build + run, capturing both build output and run output. Useful
-- when the test checks runtime behaviour (env-var reads).
buildAndRunWithEnv :: String -> String -> [(String, String)] -> IO (Int, String, String)
buildAndRunWithEnv tomlBody srcBody envVars =
    withSystemTempDirectory "sky-env-prefix-run" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "sky.toml") tomlBody
        writeFile (tmp </> "src" </> "Main.sky") srcBody
        let buildCmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (bec, bout, berr) <- readCreateProcessWithExitCode (shell buildCmd) ""
        let buildOut = bout ++ berr
            bInt = case bec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        if bInt /= 0
            then return (bInt, buildOut, "")
            else do
                let envPrefix = unwords [k ++ "=" ++ v | (k, v) <- envVars]
                    runCmd = "cd " ++ tmp ++ " && " ++ envPrefix ++ " ./sky-out/app 2>&1"
                (_, rout, rerr) <- readCreateProcessWithExitCode (shell runCmd) ""
                return (0, buildOut, rout ++ rerr)


spec :: Spec
spec = do
    describe "sky.toml [env] prefix codegen" $ do

        it "no [env] section → SKY_ namespace, SetEnvPrefix omitted" $ do
            let toml = unlines
                    [ "[project]"
                    , "name = \"defaults\""
                    , ""
                    , "[live]"
                    , "ttl = 600"
                    ]
                src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main = println \"hi\""
                    ]
            (ec, _bout, gen) <- buildAndReadGenerated toml src
            ec `shouldBe` 0
            -- Default behaviour: no prefix override.
            gen `shouldNotSatisfy` ("rt.SetEnvPrefix" `isInfixOf`)
            -- SetSkyDefault is still emitted (it just defaults to SKY_*).
            gen `shouldSatisfy` ("rt.SetSkyDefault(\"LIVE_TTL\", \"600\")" `isInfixOf`)


        it "[env] prefix = \"FENCE\" → emits SetEnvPrefix + SetSkyDefault" $ do
            let toml = unlines
                    [ "[project]"
                    , "name = \"prefixed\""
                    , ""
                    , "[env]"
                    , "prefix = \"FENCE\""
                    , ""
                    , "[live]"
                    , "port = 8765"
                    , "ttl = 1200"
                    ]
                src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main = println \"hi\""
                    ]
            (ec, _bout, gen) <- buildAndReadGenerated toml src
            ec `shouldBe` 0
            -- Prefix call is emitted FIRST (before SetSkyDefault) so
            -- the runtime hooks fire under the right namespace.
            gen `shouldSatisfy` ("rt.SetEnvPrefix(\"FENCE\")" `isInfixOf`)
            gen `shouldSatisfy` ("rt.SetPortDefault(\"8765\")" `isInfixOf`)
            gen `shouldSatisfy` ("rt.SetSkyDefault(\"LIVE_TTL\", \"1200\")" `isInfixOf`)
            -- Order: SetEnvPrefix must come before any SetSkyDefault
            -- in the same init().
            let prefixIdx = findInfix "rt.SetEnvPrefix(" gen
                portIdx   = findInfix "rt.SetPortDefault(" gen
            (prefixIdx < portIdx) `shouldBe` True


    describe "sky.toml [env] prefix runtime behaviour" $ do

        it "FENCE_LIVE_PORT overrides default; SKY_LIVE_PORT does NOT" $ do
            -- Build an app that prints the LIVE_PORT it would use.
            -- We can't test Live.app directly without a network
            -- bind, so use System.getenv against the prefixed name —
            -- this confirms the runtime SET the prefixed default.
            let toml = unlines
                    [ "[project]"
                    , "name = \"prefix-runtime\""
                    , ""
                    , "[env]"
                    , "prefix = \"FENCE\""
                    , ""
                    , "[live]"
                    , "port = 7777"
                    ]
                src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Task as Task"
                    , "import Sky.Core.System as System"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main ="
                    , "    let"
                    , "        fence = System.getenvOr \"FENCE_LIVE_PORT\" \"<unset>\""
                    , "        sky   = System.getenvOr \"SKY_LIVE_PORT\" \"<unset>\""
                    , "    in"
                    , "        let"
                    , "            _ = println (\"FENCE_LIVE_PORT=\" ++ fence)"
                    , "        in"
                    , "            println (\"SKY_LIVE_PORT=\" ++ sky)"
                    ]
            (ec, _bout, rout) <- buildAndRunWithEnv toml src []
            ec `shouldBe` 0
            -- The compiler-generated SetSkyDefault("LIVE_PORT", "7777")
            -- under prefix FENCE should set FENCE_LIVE_PORT=7777.
            rout `shouldSatisfy` ("FENCE_LIVE_PORT=7777" `isInfixOf`)
            -- SKY_LIVE_PORT should NOT be set (different namespace).
            rout `shouldSatisfy` ("SKY_LIVE_PORT=<unset>" `isInfixOf`)


        it "no [env] prefix → SKY_LIVE_PORT is set, FENCE_LIVE_PORT is not" $ do
            -- Mirror image: with no prefix override, the default
            -- "SKY" namespace is used. Confirms the absence of an
            -- [env] section is fully backwards-compatible.
            let toml = unlines
                    [ "[project]"
                    , "name = \"no-prefix\""
                    , ""
                    , "[live]"
                    , "port = 6543"
                    ]
                src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.System as System"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main ="
                    , "    let"
                    , "        sky   = System.getenvOr \"SKY_LIVE_PORT\" \"<unset>\""
                    , "        fence = System.getenvOr \"FENCE_LIVE_PORT\" \"<unset>\""
                    , "    in"
                    , "        let"
                    , "            _ = println (\"SKY_LIVE_PORT=\" ++ sky)"
                    , "        in"
                    , "            println (\"FENCE_LIVE_PORT=\" ++ fence)"
                    ]
            (ec, _bout, rout) <- buildAndRunWithEnv toml src []
            ec `shouldBe` 0
            rout `shouldSatisfy` ("SKY_LIVE_PORT=6543" `isInfixOf`)
            rout `shouldSatisfy` ("FENCE_LIVE_PORT=<unset>" `isInfixOf`)


    describe "System.setenv / System.unsetenv stdlib" $ do

        it "setenv writes a var; getenv reads it back" $ do
            let toml = unlines
                    [ "[project]"
                    , "name = \"setenv-test\""
                    ]
                src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Error exposing (Error)"
                    , "import Sky.Core.Task as Task"
                    , "import Sky.Core.System as System"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "step : Task Error ()"
                    , "step ="
                    , "    System.setenv \"MY_DYNAMIC_VAR\" \"hello\""
                    , "        |> Task.andThen (\\_ -> System.getenv \"MY_DYNAMIC_VAR\")"
                    , "        |> Task.andThen (\\v -> println (\"got: \" ++ v))"
                    , ""
                    , "main = Task.run step"
                    ]
            (ec, _bout, rout) <- buildAndRunWithEnv toml src []
            ec `shouldBe` 0
            rout `shouldSatisfy` ("got: hello" `isInfixOf`)


        it "unsetenv removes a var; subsequent getenv returns Err" $ do
            let toml = unlines
                    [ "[project]"
                    , "name = \"unsetenv-test\""
                    ]
                src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Error exposing (Error)"
                    , "import Sky.Core.Task as Task"
                    , "import Sky.Core.System as System"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "step : Task Error String"
                    , "step ="
                    , "    System.setenv \"TEMP_VAR\" \"x\""
                    , "        |> Task.andThen (\\_ -> System.unsetenv \"TEMP_VAR\")"
                    , "        |> Task.andThen (\\_ -> Task.succeed (System.getenvOr \"TEMP_VAR\" \"<unset>\"))"
                    , ""
                    , "main ="
                    , "    step"
                    , "        |> Task.andThen (\\v -> println (\"after unset: \" ++ v))"
                    , "        |> Task.run"
                    ]
            (ec, _bout, rout) <- buildAndRunWithEnv toml src []
            ec `shouldBe` 0
            rout `shouldSatisfy` ("after unset: <unset>" `isInfixOf`)


-- | Find first index of a substring in a string. Returns length+1
-- (a value greater than any valid index) if not found, so the
-- (a < b) ordering check fails sensibly for both-missing.
findInfix :: String -> String -> Int
findInfix needle hay = go 0 hay
  where
    nLen = length needle
    go i s
        | length s < nLen = length hay + 1
        | take nLen s == needle = i
        | otherwise = go (i + 1) (drop 1 s)
