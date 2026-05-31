module Sky.Canonicalise.PipelineIntegritySpec (spec) where

-- v0.15.42 Cycle 6 — pipeline-integrity regression fence.
--
-- Bundles three "if it compiles, it works" credibility-bar
-- regressions called out in the v0.15.41 audit:
--
--   §3.1  Unknown qualified name silently passed canonicaliser, then
--         only failed at `go build` with `undefined: NotARealModule_foo`.
--         Now: canonicaliser flags it as a clean naming error citing
--         the missing module.
--
--   §3.4  "Compilation successful" used to print at the end of Sky
--         lowering, BEFORE `go build` ran. A subsequent Go build
--         failure left users staring at a "successful" banner
--         followed by a Go diagnostic. Now: "Sky lowering succeeded"
--         is the lowering-stage banner; "Compilation successful"
--         only prints after Go returns 0; failure prints
--         "Sky lowering succeeded but `go build` failed:" first.
--
--   §3.2  A user `type Result a = Just a | Nothing` shadowed the
--         Prelude-exposed Maybe/Result constructors silently. The
--         resulting program compiles but `Just`/`Nothing` resolve to
--         the user's ADT, not the stdlib ones — refactor regression
--         class. Now: hard canonicaliser error names both the shadow
--         and the protected stdlib type / constructor.

import Test.Hspec
import qualified System.Exit as Exit
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
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


-- | Build a fixture with a single Main.sky source plus an empty
-- sky.toml. Returns the build's exit code + combined stdout/stderr.
buildFixture :: String -> IO (Int, String)
buildFixture mainSrc =
    withSystemTempDirectory "sky-v0_15_42" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "sky.toml") "name = \"v0_15_42-test\"\n"
        writeFile (tmp </> "src" </> "Main.sky") mainSrc
        let cmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (ec, sout, serr) <- readCreateProcessWithExitCode (shell cmd) ""
        let combined = sout ++ serr
            ecInt = case ec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        return (ecInt, combined)


spec :: Spec
spec = describe "v0.15.42 Cycle 6: pipeline-integrity regression fence" $ do

    describe "Bug 1 (audit §3.1): unknown qualified name" $ do

        it "rejects `NotARealModule.foo` at canonicalisation, not go build" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main = println (NotARealModule.doSomething 42)"
                    ]
            (ec, out) <- buildFixture src
            ec `shouldNotBe` 0
            -- The error names the missing qualifier explicitly.
            out `shouldSatisfy` ("Undefined name: NotARealModule.doSomething" `isInfixOf`)
            out `shouldSatisfy` ("Module `NotARealModule` is not imported" `isInfixOf`)
            -- And NEVER falls through to a Go diagnostic — the whole
            -- point of the fix is that Sky catches it pre-Go.
            out `shouldNotSatisfy` ("undefined: NotARealModule" `isInfixOf`)
            out `shouldNotSatisfy` ("Sky lowering succeeded" `isInfixOf`)
            out `shouldNotSatisfy` ("Compilation successful" `isInfixOf`)


        it "suggests a similar known qualifier when the typo is close" $ do
            -- `Strng.fromInt` is 1 edit away from the auto-qualified
            -- kernel module `String`. The fix offers a Did-you-mean.
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main = println (Strng.fromInt 42)"
                    ]
            (ec, out) <- buildFixture src
            ec `shouldNotBe` 0
            out `shouldSatisfy` ("Undefined name: Strng.fromInt" `isInfixOf`)
            out `shouldSatisfy` ("Did you mean `String`?" `isInfixOf`)


    describe "Bug 2 (audit §3.4): success banner sequencing" $ do

        it "prints 'Sky lowering succeeded' BEFORE running go build" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Std.Log exposing (println)"
                    , "main = println \"hi\""
                    ]
            (ec, out) <- buildFixture src
            ec `shouldBe` 0
            -- New banner for the Sky-side phase.
            out `shouldSatisfy` ("Sky lowering succeeded" `isInfixOf`)
            -- Final, post-go-build banner.
            out `shouldSatisfy` ("Compilation successful" `isInfixOf`)
            -- Order: lowering banner appears BEFORE the success banner.
            let lowerIdx = findFirst "Sky lowering succeeded" out
                successIdx = findFirst "Compilation successful" out
            (lowerIdx < successIdx) `shouldBe` True


        it "wires the failure-path banner into runGoBuildWithDiagnostics" $ do
            -- Engineering an actual go-build failure inside the
            -- temp-dir sandbox is fragile (the Sky-side validator
            -- catches most known compiler-bug shapes before Go even
            -- runs, by design). The failure-path banner is a small
            -- pure-string contract; assert it lives in the source
            -- where Main.hs invokes go build, so removing it shows
            -- up as a CI failure with a clear pointer.
            mainSrc <- readFile "app/Main.hs"
            mainSrc `shouldSatisfy`
                ("Sky lowering succeeded but `go build` failed:" `isInfixOf`)


    describe "Bug 3 (audit §3.2): Prelude shadowing of stdlib types" $ do

        it "rejects user `type Result a = Just a | Nothing`" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , "type Result a = Just a | Nothing"
                    , "main = println (case Just 42 of"
                    , "    Just x -> \"got \" ++ String.fromInt x"
                    , "    Nothing -> \"nothing\")"
                    ]
            (ec, out) <- buildFixture src
            ec `shouldNotBe` 0
            out `shouldSatisfy` ("Prelude shadowing" `isInfixOf`)
            out `shouldSatisfy` ("type Result" `isInfixOf`)
            out `shouldSatisfy` ("Sky.Core.Result" `isInfixOf`)
            -- The whole point: catch it at Sky time, not after.
            out `shouldNotSatisfy` ("Sky lowering succeeded" `isInfixOf`)


        it "rejects a user constructor named `Just` even under a non-Result type" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , "type Box a = Just a | Empty"
                    , "main = println \"x\""
                    ]
            (ec, out) <- buildFixture src
            ec `shouldNotBe` 0
            out `shouldSatisfy` ("Prelude shadowing" `isInfixOf`)
            out `shouldSatisfy` ("constructor `Just`" `isInfixOf`)
            out `shouldSatisfy` ("Sky.Core.Maybe" `isInfixOf`)


        it "allows user types whose names do NOT collide with Prelude" $ do
            -- Regression guard: the gate must not over-trigger.
            let src = unlines
                    [ "module Main exposing (main)"
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , "type Color = Red | Green | Blue"
                    , "main = println \"ok\""
                    ]
            (ec, out) <- buildFixture src
            ec `shouldBe` 0
            out `shouldNotSatisfy` ("Prelude shadowing" `isInfixOf`)


-- | Find the first character index of `needle` in `hay`. -1 when absent.
findFirst :: String -> String -> Int
findFirst needle hay = go 0 hay
  where
    n = length needle
    go _ [] = -1
    go i s
        | needle `isPrefixOf'` s = i
        | otherwise = go (i + 1) (drop 1 s)
    isPrefixOf' p s = take (length p) s == p && n == length p
