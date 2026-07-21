module Sky.Build.JsonPipelinePanic372Spec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


-- v0.15.x #372 — user-defined Decoder pipeline runtime panic.
--
-- BEFORE THE FIX. A user-defined helper that chains
-- `Decode.andThen` + `Decode.map` over a curried record ctor
-- (the same shape as `Json.Decode.Pipeline.optional`, but inlined
-- by the user instead of routed through the stdlib's hand-coded
-- `stdlib_extra.go` path) panicked at the final accumulator stage
-- with:
--
--   rt.Coerce: expected func(interface {}) interface {},
--   got Spec_R ({x })
--
-- Root cause: `adaptFuncValue` in `runtime-go/rt/rt.go` zero-padded
-- the remaining args when the target was `func(any) any` (target's
-- return is `any` instead of another `func`).  A 2-arg Go ctor like
-- `Spec : func(string, string) Spec_R` fed one String produced
-- `Spec("x", "")` instead of a partially-applied closure.  At the
-- next pipeline stage, `f` was a fully-constructed `Spec_R` value
-- (not a function), and `rt.Coerce[func(any) any](f)` panicked.
--
-- AFTER THE FIX. When `nin > len(allArgs)` AND target's return is
-- an interface (e.g. `func(any) any`), `adaptFuncValue` now boxes a
-- `curryRemainingArgs` closure as the interface return — mirroring
-- `skyCallOne`'s already-shipped currying contract.  Sky semantics
-- say all functions are curried; this aligns the adapter boundary
-- with that invariant.
--
-- Regression guard: build the minimal repro, run the binary, and
-- assert it prints the decoded values without a panic.
spec :: Spec
spec = do
    describe "v0.15.x #372 — user Decoder pipeline curry/Coerce panic" $ do
        it "two-field user-defined optional helper round-trips at runtime" $ do
            sky <- findSky
            withSystemTempDirectory "sky-372" $ \tmp -> do
                writeFixture tmp
                (ec, stdoutBuild, errOut) <-
                    runSky sky ["build", "src/Main.sky"] tmp
                let _ = stdoutBuild
                let _ = errOut  -- silence unused
                ec `shouldBe` ExitSuccess
                let appPath = tmp </> "sky-out" </> "app"
                hasApp <- doesFileExist appPath
                hasApp `shouldBe` True
                (rc, stdout', stderr') <- readCreateProcessWithExitCode
                    (proc appPath []) { cwd = Just tmp } ""
                -- Pre-fix: rc was a panic exit (2) with the
                -- `rt.Coerce: expected func(interface {}) interface
                -- {}, got main.Spec_R` panic on stderr.  Post-fix:
                -- clean exit and the decoded values printed.
                rc `shouldBe` ExitSuccess
                ("got: a=x b=y" `isInfixOf` stdout') `shouldBe` True
                -- No panic trace on stderr.
                ("rt.Coerce: expected func" `isInfixOf` stderr')
                    `shouldBe` False

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
        writeFile (dir </> "sky.toml")
            (unlines
                [ "name = \"x372\""
                , "[bin]"
                , "main = \"src/Main.sky\""
                ])
        writeFile (dir </> "src" </> "Main.sky") mainFixture


mainFixture :: String
mainFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.Json.Decode as Decode"
    , "import Sky.Core.Error as Error exposing (Error)"
    , "import Std.Log exposing (println)"
    , ""
    , ""
    , "type alias Spec ="
    , "    { a : String, b : String }"
    , ""
    , ""
    , "orEmpty : String -> Decode.Decoder (String -> a) -> Decode.Decoder a"
    , "orEmpty field next ="
    , "    next"
    , "        |> Decode.andThen"
    , "            (\\f ->"
    , "                Decode.oneOf"
    , "                    [ Decode.field field Decode.string"
    , "                    , Decode.succeed \"\""
    , "                    ]"
    , "                    |> Decode.map f"
    , "            )"
    , ""
    , ""
    , "specDecoder : Decode.Decoder Spec"
    , "specDecoder ="
    , "    Decode.succeed Spec"
    , "        |> orEmpty \"a\""
    , "        |> orEmpty \"b\""
    , ""
    , ""
    , "main ="
    , "    case Decode.decodeString specDecoder \"{\\\"a\\\":\\\"x\\\",\\\"b\\\":\\\"y\\\"}\" of"
    , "        Ok spec ->"
    , "            println (\"got: a=\" ++ spec.a ++ \" b=\" ++ spec.b)"
    , ""
    , "        Err e ->"
    , "            println (\"err: \" ++ Error.toString e)"
    ]
