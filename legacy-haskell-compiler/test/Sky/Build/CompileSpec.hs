module Sky.Build.CompileSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist, removeDirectoryRecursive)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)

-- | Find the built sky compiler binary. Prefers $SKY_BIN, else sky-out/sky
-- relative to the repo root (derived from cwd at test run).
findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    -- cabal test runs from repo root
    let candidate = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist candidate
    if ok then return candidate
          else fail ("sky binary missing at " ++ candidate
                  ++ " — run scripts/build.sh first")

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

spec :: Spec
spec = do
    describe "type errors are fatal" $ do
        it "aborts the build when types don't unify" $ do
            sky <- findSky
            withSystemTempDirectory "sky-type-err" $ \tmp -> do
                writeFixtureProject tmp "type-err" $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "-- Int + String must not unify"
                    , "bad : Int"
                    , "bad = 1 + \"not-an-int\""
                    , ""
                    , "main = println (String.fromInt bad)"
                    ]
                (ec, _out, err) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldNotBe` ExitSuccess
                -- Expect a message mentioning a type error or unification.
                let combined = err
                (any (`isInfixOf` combined)
                    ["TYPE ERROR", "Type error", "Cannot unify",
                     "type error", "cannot unify"])
                    `shouldBe` True

    describe "well-typed programs build" $ do
        it "compiles a trivial hello-world clean-slate" $ do
            sky <- findSky
            withSystemTempDirectory "sky-ok" $ \tmp -> do
                writeFixtureProject tmp "hello" $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "main = println \"ok\""
                    ]
                (ec, _out, _err) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True

    describe "user-defined polymorphic HOFs with Result-typed lambda params" $ do
        -- Regression for Bug #1 (surfaced by sky-chat ep07 on
        -- 2026-04-22). A user-defined `do : Result Error a ->
        -- (a -> Result Error b) -> Result Error b` chain passed
        -- `sky check` but `go build` rejected the emitted lambda:
        --
        --   cannot use func(a any) any as func(any) rt.SkyResult[
        --     Sky_Core_Error_Error, rt.SkyValue]
        --
        -- Root cause: HOF param sigs emitted the lambda's return as
        -- the defaulted `rt.SkyResult[E, rt.SkyValue]`, but Sky
        -- lambdas always lower to `func(any) any`. Even though
        -- `SkyValue = any`, the wrapper is a distinct generic
        -- instantiation with no Go covariance, so the function types
        -- were unassignable. Fix narrows lambda-typed HOF params'
        -- innermost return to `any` unless the return is itself a
        -- bare TVar (which Go then infers from the call-site arg's
        -- type — e.g. `Counter.view`'s `Msg -> parentMsg`).
        it "compiles a user-defined monadic-do chain on Result" $ do
            sky <- findSky
            withSystemTempDirectory "sky-user-hof-result" $ \tmp -> do
                writeFixtureProject tmp "user-hof-result" $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Result as Result"
                    , "import Sky.Core.Error as Error exposing (Error)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "do : Result Error a -> (a -> Result Error b) -> Result Error b"
                    , "do result fn ="
                    , "    Result.andThen fn result"
                    , ""
                    , "pipeline : String -> Result Error (String, String)"
                    , "pipeline key ="
                    , "    do (firstStep key) (\\a ->"
                    , "    do (secondStep a) (\\b ->"
                    , "    Ok (a, b)))"
                    , ""
                    , "firstStep : String -> Result Error String"
                    , "firstStep key ="
                    , "    if key == \"\" then"
                    , "        Err (Error.invalidInput \"empty key\")"
                    , "    else"
                    , "        Ok (\"first:\" ++ key)"
                    , ""
                    , "secondStep : String -> Result Error String"
                    , "secondStep a ="
                    , "    Ok (\"second:\" ++ a)"
                    , ""
                    , "main ="
                    , "    case pipeline \"hello\" of"
                    , "        Ok (a, b) ->"
                    , "            println (\"ok \" ++ a ++ \" \" ++ b)"
                    , ""
                    , "        Err e ->"
                    , "            println (errorToString e)"
                    ]
                (ec, _out, err) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                -- Extra safety: the specific Go-level error the bug used
                -- to produce must not reappear.
                ("cannot use func" `isInfixOf` err) `shouldBe` False
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True

        it "compiles a user-defined HOF whose callback return is a bare TVar" $ do
            -- Counter.view-shaped pattern on the entry module:
            -- `lift : (Inner -> outerMsg) -> Inner -> outerMsg`. The
            -- TVar appears only in the callback's return position AND
            -- the outer return. When the user passes a NAMED function
            -- (a constructor here, `Wrap : func(Inner) Outer`), Go
            -- should be able to infer `outerMsg = Outer` through the
            -- callback's return type.
            --
            -- Pre-fix: the entry-module call-site emitter always
            -- instantiated generic functions as `lift[any](...)`,
            -- forcing T1=any, which rejected `Wrap`'s concrete return.
            -- Fix skips the explicit `[any]` at direct-call sites for
            -- user-defined HOFs and lets Go infer type params from
            -- args. Bare references (function used as a value) still
            -- emit `[any]` to satisfy Go's "cannot infer T1" rule.
            sky <- findSky
            withSystemTempDirectory "sky-user-hof-tvar" $ \tmp -> do
                writeFixtureProject tmp "user-hof-tvar" $ unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type Inner = A | B"
                    , "type Outer = Wrap Inner"
                    , ""
                    , "lift : (Inner -> outerMsg) -> Inner -> outerMsg"
                    , "lift toOuter inner ="
                    , "    toOuter inner"
                    , ""
                    , "main ="
                    , "    case lift Wrap A of"
                    , "        Wrap A -> println \"A\""
                    , "        Wrap B -> println \"B\""
                    ]
                (ec, _out, _err) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                built <- doesFileExist (tmp </> "sky-out" </> "app")
                built `shouldBe` True
