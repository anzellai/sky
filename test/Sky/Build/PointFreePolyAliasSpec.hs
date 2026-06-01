module Sky.Build.PointFreePolyAliasSpec (spec) where

-- Regression spec for #398 — point-free top-level alias of a
-- polymorphic / N-ary function.
--
-- Pre-fix, a binding like `tickle = String.toUpper` (where
-- `String.toUpper : String -> String`) lowered to a 0-arity Go
-- thunk wrapper:
--
--     func tickle() func(string) string {
--         return rt.Coerce[func(string) string](rt.String_toUpper)
--     }
--
-- Call sites then hit `too many arguments in call to tickle`
-- because the lowerer inferred arity from the syntactic param
-- count (= 0) instead of from the type sig's arrow chain (= 1).
--
-- Post-fix, `etaExpandPointFree` in `Sky.Build.Compile` detects the
-- mismatch and rewrites the def to a normal N-ary function with
-- synthetic `_skyEta_pN` parameters, so the emitted Go reads
--
--     func tickle(_skyEta_p0 string) string {
--         return rt.CoerceString(rt.String_toUpperT(rt.AsString(_skyEta_p0)))
--     }
--
-- and the call site `tickle "hello"` succeeds in `go build`.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- Compile a self-contained Main.sky inside a tempdir.  Each spec
-- writes its own source, runs `sky build` with `cwd = $tmp`, and
-- inspects $tmp/sky-out/main.go.  Tempdir isolation avoids the
-- shared-sky-out race that #381 fixed for ExampleSweepSpec and that
-- #396 closes for CheckIsBuild / RecordFieldOrder.
buildInTmp :: FilePath -> String -> (FilePath -> ExitCode -> String -> String -> IO ()) -> IO ()
buildInTmp slug src k = do
    sky <- findSky
    withSystemTempDirectory slug $ \tmp -> do
        writeFile (tmp </> "Main.sky") src
        let cp = (proc sky ["build", "Main.sky"]) { cwd = Just tmp }
        (ec, out, err) <- readCreateProcessWithExitCode cp ""
        k tmp ec out err


spec :: Spec
spec = describe "point-free top-level alias of a polymorphic function (#398)" $ do
    it "emits an N-ary function (not a 0-arity thunk) for annotated 1-arg alias" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "tickle : String -> String"
                , "tickle = String.toUpper"
                , ""
                , "main = println (tickle \"hi\")"
                ]
        buildInTmp "sky-pf-1arg" src $ \tmp ec out err -> do
            let combined = out ++ err
            ec `shouldBe` ExitSuccess
            ("Build complete" `isInfixOf` combined) `shouldBe` True
            body <- readFile (tmp </> "sky-out" </> "main.go")
            -- Eta-expanded form: `func tickle(_skyEta_p0 ...)`.
            ("func tickle(_skyEta_p0" `isInfixOf` body) `shouldBe` True
            -- The broken thunk form must NOT appear.
            ("func tickle() func(" `isInfixOf` body) `shouldBe` False

    it "emits an N-ary function for an unannotated 1-arg alias" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "-- No annotation — HM-solved type drives arity."
                , "tickle = String.toUpper"
                , ""
                , "main = println (tickle \"hi\")"
                ]
        buildInTmp "sky-pf-noannot" src $ \tmp ec _ _ -> do
            ec `shouldBe` ExitSuccess
            body <- readFile (tmp </> "sky-out" </> "main.go")
            ("func tickle(_skyEta_p0" `isInfixOf` body) `shouldBe` True
            ("func tickle() func(" `isInfixOf` body) `shouldBe` False

    it "expands a 2-arg point-free alias to both params" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "joinStr : String -> String -> String"
                , "joinStr = String.append"
                , ""
                , "main = println (joinStr \"hi\" \"lo\")"
                ]
        buildInTmp "sky-pf-2arg" src $ \tmp ec _ _ -> do
            ec `shouldBe` ExitSuccess
            body <- readFile (tmp </> "sky-out" </> "main.go")
            -- Both params present.
            ("func joinStr(_skyEta_p0" `isInfixOf` body) `shouldBe` True
            (", _skyEta_p1" `isInfixOf` body) `shouldBe` True

    it "leaves plain value bindings (no arrows) untouched" $ do
        -- Guard against over-application: a value binding like
        -- `greeting : String; greeting = \"Howdy\"` must NOT be
        -- eta-expanded — it has zero arrows in its type.
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , ""
                , "greeting : String"
                , "greeting = \"Howdy\""
                , ""
                , "main = println greeting"
                ]
        buildInTmp "sky-pf-value" src $ \tmp ec _ _ -> do
            ec `shouldBe` ExitSuccess
            body <- readFile (tmp </> "sky-out" </> "main.go")
            ("func greeting()" `isInfixOf` body) `shouldBe` True
            -- No eta param injected.
            ("_skyEta_p0" `isInfixOf` body) `shouldBe` False
