module Sky.Build.CoerceArgParametricSpec (spec) where

-- v0.15.x hardening — Gap A1 / Plan Item P1 closure.
--
-- The fragility audit (docs/fragility-audit-v0.15.3.md item #3) flagged
-- `coerceArg`'s parametric-alias short-circuit as a backward-gated
-- branch.  The arm gates on `Just srcTy <- goExprGoType e`, but for
-- expression shapes whose Go-static type isn't tracked in the
-- lambda-types registry (let-bindings cross polymorphic-call result,
-- VarLocal references to outer-scope bindings, …) `goExprGoType`
-- returns Nothing and the arm doesn't fire.  Codegen falls through
-- to `any(arg).(Foo_R[any])` — a nominal type assertion that panics
-- at runtime when the source's actual Go-static type is a different
-- generic instantiation (`Foo_R[int]`, `Foo_R[Msg]`, …).
--
-- Reproducer (mirrors test-files/v0.15-stress/src/Widget/CrossInstanceCfg.sky):
--
--   type alias Cfg msg = { onSubmit : msg, label : String }
--   forwardCfg : Cfg msg -> Cfg msg
--   forwardCfg cfg = cfg
--   main =
--     let cfg0 = { onSubmit = 42, label = "i" }
--         cfg1 = forwardCfg cfg0
--     in ...
--
-- Pre-fix codegen:
--   cfg1 := forwardCfg(any(cfg0).(Cfg_R[any]))
--   -- panic: interface {} is main.Cfg_R[int], not main.Cfg_R[interface {}]
--
-- Post-fix (structural-fallback arm):
--   cfg1 := forwardCfg(cfg0)
--   -- Go infers T1 = int from cfg0's static type; binary runs clean.
--
-- This spec asserts both:
--   1. The emitted Go does NOT contain the panic-inducing
--      `any(...).(Cfg_R[any])` pattern.
--   2. The binary runs to completion without panicking.

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


writeFixtureProject :: FilePath -> String -> String -> IO ()
writeFixtureProject dir name body = do
    createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml")
        ("name = \"" ++ name ++ "\"\nversion = \"0.0.0\"\nentry = \"src/Main.sky\"\n\n[source]\nroot = \"src\"\n")
    writeFile (dir </> "src" </> "Main.sky") body


reproducerSource :: String
reproducerSource = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , ""
    , "type alias Cfg msg ="
    , "    { onSubmit : msg"
    , "    , label : String"
    , "    }"
    , ""
    , "-- Polymorphic identity-shaped passthrough."
    , "forwardCfg : Cfg msg -> Cfg msg"
    , "forwardCfg cfg ="
    , "    cfg"
    , ""
    , "makeIntCfg : Int -> Cfg Int"
    , "makeIntCfg x ="
    , "    { onSubmit = x, label = \"i\" }"
    , ""
    , "consumeIntCfg : Cfg Int -> Int"
    , "consumeIntCfg cfg ="
    , "    cfg.onSubmit"
    , ""
    , "main ="
    , "    let"
    , "        cfg0 = makeIntCfg 42"
    , "        cfg1 = forwardCfg cfg0"
    , "        n = consumeIntCfg cfg1"
    , "    in"
    , "    println (String.fromInt n)"
    ]


spec :: Spec
spec = do
    describe "coerceArg structural fallback for parametric-alias cross-instantiation" $ do
        it "emits no `any(...).(Cfg_R[any])` nominal assertion" $ do
            sky <- findSky
            withSystemTempDirectory "sky-coerce-arg-param" $ \tmp -> do
                writeFixtureProject tmp "coerce-arg-param" reproducerSource
                (ec, out, err) <- runSky sky ["build", "src/Main.sky"] tmp
                let combined = out ++ err
                ec `shouldBe` ExitSuccess
                ("Build complete" `isInfixOf` combined) `shouldBe` True
                let mainGo = tmp </> "sky-out" </> "main.go"
                generated <- readFile mainGo
                -- Pre-fix codegen emitted `any(cfg0).(Cfg_R[any])` at
                -- the call site, which panics at runtime under Go's
                -- nominal generic-type rules.  The structural fallback
                -- closes this — the arg flows raw.
                (".(Cfg_R[any])" `isInfixOf` generated) `shouldBe` False
                -- Defensive: also forbid `rt.Coerce[Cfg_R[any]]` —
                -- another shape the legacy wrap path could emit when
                -- the target slot's TVars were erased.
                ("rt.Coerce[Cfg_R[any]]" `isInfixOf` generated) `shouldBe` False

        it "runs to completion without panicking" $ do
            sky <- findSky
            withSystemTempDirectory "sky-coerce-arg-param-run" $ \tmp -> do
                writeFixtureProject tmp "coerce-arg-param-run" reproducerSource
                (bec, bout, berr) <- runSky sky ["build", "src/Main.sky"] tmp
                let bcombined = bout ++ berr
                bec `shouldBe` ExitSuccess
                ("Build complete" `isInfixOf` bcombined) `shouldBe` True
                let appPath = tmp </> "sky-out" </> "app"
                (rec, rout, rerr) <- readCreateProcessWithExitCode
                    (proc appPath []) ""
                let rcombined = rout ++ rerr
                rec `shouldBe` ExitSuccess
                ("42" `isInfixOf` rout) `shouldBe` True
                ("panic" `isInfixOf` rcombined) `shouldBe` False
