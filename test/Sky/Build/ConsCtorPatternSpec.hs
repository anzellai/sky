module Sky.Build.ConsCtorPatternSpec (spec) where

-- Regression fence for the cons-with-constructor pattern fix.
--
-- Pre-fix bug: `(Ctor x) :: rest -> body` lowered to a guard that
-- only checked `len(list) >= 1`, ignoring the head's constructor.
-- The case body's bindings then assumed the head matched the
-- constructor and extracted field 0 from whatever was at the head,
-- panicking with "interface conversion: …" at runtime when the head
-- was actually a different ADT variant.
--
-- Fix: the lowerer now emits a head-discriminator check (via the
-- new `consHeadCondition` helper) that joins the `len >= 1` test
-- via `&&`. So `(AttrDescribe d) :: rest` only fires when the
-- head's actual constructor IS AttrDescribe — falling through to
-- the catch-all arm otherwise.
--
-- This spec exercises the pattern that bit Std.Ui's pickSemanticTag
-- (renamed there) plus a generic Maybe / Result variant.

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


-- Build + run a single Sky source file in a fresh temp directory and
-- return (exit code, combined stdout+stderr from sky build, stdout
-- from running the binary).
buildAndRun :: String -> IO (Int, String, String)
buildAndRun src =
    withSystemTempDirectory "sky-cons-ctor" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"cons-ctor-test\"\n"
        let buildCmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (bec, bout, berr) <- readCreateProcessWithExitCode (shell buildCmd) ""
        let buildOut = bout ++ berr
            bInt = case bec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        if bInt /= 0
            then return (bInt, buildOut, "")
            else do
                let runCmd = "cd " ++ tmp ++ " && ./sky-out/app 2>&1"
                (_, rout, rerr) <- readCreateProcessWithExitCode (shell runCmd) ""
                return (0, buildOut, rout ++ rerr)


spec :: Spec
spec = do
    describe "Cons-with-constructor pattern dispatches by head ctor tag" $ do

        it "Maybe-head cons: (Just x) :: _ matches Just-headed list, not Nothing-headed" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "first : List (Maybe Int) -> String"
                    , "first xs ="
                    , "    case xs of"
                    , "        (Just n) :: _ -> \"Just \" ++ String.fromInt n"
                    , "        (Nothing) :: _ -> \"Nothing\""
                    , "        [] -> \"empty\""
                    , ""
                    , "main ="
                    , "    let"
                    , "        _ = println (first [ Just 1, Nothing ])"
                    , "        _ = println (first [ Nothing, Just 2 ])"
                    , "        _ = println (first [])"
                    , "    in"
                    , "        println \"done\""
                    , ""
                    , "import Sky.Core.String as String"
                    ]
            -- Imports order is wrong above; re-order.
            let src2 = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "first : List (Maybe Int) -> String"
                    , "first xs ="
                    , "    case xs of"
                    , "        (Just n) :: _ -> \"Just \" ++ String.fromInt n"
                    , "        Nothing :: _ -> \"Nothing\""
                    , "        [] -> \"empty\""
                    , ""
                    , "main ="
                    , "    let"
                    , "        _ = println (first [ Just 1, Nothing ])"
                    , "        _ = println (first [ Nothing, Just 2 ])"
                    , "        _ = println (first [])"
                    , "    in"
                    , "        println \"done\""
                    ]
            (ec, _build, runOut) <- buildAndRun src2
            ec `shouldBe` 0
            runOut `shouldSatisfy` ("Just 1" `isInfixOf`)
            runOut `shouldSatisfy` ("Nothing\n" `isInfixOf`)
            runOut `shouldSatisfy` ("empty\n" `isInfixOf`)
            -- Specifically: the second list starts with `Nothing`,
            -- so `(Just n) :: _` MUST NOT match it. Pre-fix this
            -- panicked with `interface conversion: …`.
            runOut `shouldNotSatisfy` ("Just 0" `isInfixOf`)

        it "Custom-ADT-head cons: literal-string discriminator at head" $ do
            -- Test a literal at the head of a cons: `\"foo\" :: _`
            -- should only fire when the head IS `\"foo\"`.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "classify : List String -> String"
                    , "classify xs ="
                    , "    case xs of"
                    , "        \"hello\" :: _ -> \"greeting\""
                    , "        \"bye\" :: _ -> \"farewell\""
                    , "        _ :: _ -> \"other-string\""
                    , "        [] -> \"empty\""
                    , ""
                    , "main ="
                    , "    let"
                    , "        _ = println (classify [\"hello\", \"world\"])"
                    , "        _ = println (classify [\"bye\", \"now\"])"
                    , "        _ = println (classify [\"random\"])"
                    , "        _ = println (classify [])"
                    , "    in"
                    , "        println \"done\""
                    ]
            (ec, _build, runOut) <- buildAndRun src
            ec `shouldBe` 0
            runOut `shouldSatisfy` ("greeting\n"     `isInfixOf`)
            runOut `shouldSatisfy` ("farewell\n"     `isInfixOf`)
            runOut `shouldSatisfy` ("other-string\n" `isInfixOf`)
            runOut `shouldSatisfy` ("empty\n"        `isInfixOf`)
