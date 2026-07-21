module Sky.Build.ConsPatternLengthSpec (spec) where

-- Regression fence for #402 — cons-pattern length-guard codegen.
--
-- Pre-fix bug: every PCons emitted only `len(subj) >= 1`, and the
-- recursive `consTailCondition` added another `>= 1` for each
-- nested cons.  So `a :: b :: c :: _` came out as
-- `>= 1 && >= 1` (== `len >= 2`) — IDENTICAL to `a :: b :: _`.
-- A 2-element list could enter the longer arm, then the body's
-- binding code read `tail[1]` of a 1-element tail and panicked
-- with `IndexOutOfRange`.
--
-- Fix: `consChainLength` walks the cons-chain to compute the
-- correct `(minLen, isExact)`.  PList terminator pins exact
-- length; PVar / PAnything / catch-all keeps `>=`.

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


-- Build + run a single Sky source file in a fresh temp directory.
buildAndRun :: String -> IO (Int, String, String)
buildAndRun src =
    withSystemTempDirectory "sky-cons-len" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"cons-len-test\"\n"
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


-- | Search the emitted Go for the cons-length guard line.  Used to
-- assert structurally that the chain-length walk produced the right
-- `>= N` / `== N` constant.
buildOnlyMain :: String -> IO (Int, String, String)
buildOnlyMain src =
    withSystemTempDirectory "sky-cons-len-build" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"cons-len-build\"\n"
        let buildCmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (bec, bout, berr) <- readCreateProcessWithExitCode (shell buildCmd) ""
        let buildOut = bout ++ berr
            bInt = case bec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        if bInt /= 0
            then return (bInt, buildOut, "")
            else do
                emitted <- readFile (tmp </> "sky-out" </> "main.go")
                return (0, buildOut, emitted)


spec :: Spec
spec = do
    describe "Cons-pattern length guard (#402)" $ do

        it "a :: b :: c :: _ requires len >= 3 (2-element list MUST fall through)" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "classify : List Int -> String"
                    , "classify parts ="
                    , "    case parts of"
                    , "        a :: b :: c :: _ ->"
                    , "            \"three: \" ++ String.fromInt a"
                    , "                ++ \" \" ++ String.fromInt b"
                    , "                ++ \" \" ++ String.fromInt c"
                    , ""
                    , "        a :: b :: [] ->"
                    , "            \"two: \" ++ String.fromInt a ++ \" \" ++ String.fromInt b"
                    , ""
                    , "        _ ->"
                    , "            \"zero or one\""
                    , ""
                    , "main ="
                    , "    let"
                    , "        _ = println (classify [ 1, 2 ])"
                    , "        _ = println (classify [ 1, 2, 3 ])"
                    , "        _ = println (classify [ 1, 2, 3, 4 ])"
                    , "        _ = println (classify [ 1 ])"
                    , "        _ = println (classify [])"
                    , "    in"
                    , "        println \"done\""
                    ]
            (ec, build, runOut) <- buildAndRun src
            ec `shouldBe` 0
            -- A 2-element list MUST take the `a :: b :: []` arm,
            -- not the `a :: b :: c :: _` arm (would panic).
            runOut `shouldSatisfy` ("two: 1 2"   `isInfixOf`)
            runOut `shouldSatisfy` ("three: 1 2 3" `isInfixOf`)
            -- Pre-fix this panicked with IndexOutOfRange — surface
            -- the regression clearly.
            runOut `shouldNotSatisfy` ("IndexOutOfRange" `isInfixOf`)
            runOut `shouldNotSatisfy` ("panic"           `isInfixOf`)
            -- The build log should report a clean compile.
            build   `shouldSatisfy` ("Compilation successful" `isInfixOf`)

        it "emitted Go uses len >= 3 for triple-cons arm and len == 2 for fixed two-cons arm" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "classify : List Int -> String"
                    , "classify parts ="
                    , "    case parts of"
                    , "        _ :: _ :: _ :: _ -> \"three+\""
                    , "        _ :: _ :: []     -> \"two\""
                    , "        _                -> \"other\""
                    , ""
                    , "main = println (classify [ 1, 2, 3 ])"
                    ]
            (ec, _, emitted) <- buildOnlyMain src
            ec `shouldBe` 0
            -- Three-cons arm: prefix length, `>= 3`.
            emitted `shouldSatisfy` (">= 3"  `isInfixOf`)
            -- Fixed two-cons arm: exact length, `== 2`.
            emitted `shouldSatisfy` ("== 2"  `isInfixOf`)
            -- AND the buggy duplicate guard pre-fix used a chained
            -- `>= 1 && len(rt.AsList(any(rt.AsList(...)` shape — the
            -- new chain-folding code MUST NOT emit that any more.
            emitted `shouldNotSatisfy` (">= 1 && len(rt.AsList(any(rt.AsList" `isInfixOf`)

        it "a :: [] matches exactly 1 element, not 1+" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "label : List Int -> String"
                    , "label xs ="
                    , "    case xs of"
                    , "        a :: [] -> \"one: \" ++ String.fromInt a"
                    , "        _       -> \"other\""
                    , ""
                    , "main ="
                    , "    let"
                    , "        _ = println (label [ 5 ])"
                    , "        _ = println (label [ 5, 6 ])"
                    , "        _ = println (label [])"
                    , "    in"
                    , "        println \"done\""
                    ]
            (ec, _, runOut) <- buildAndRun src
            ec `shouldBe` 0
            runOut `shouldSatisfy` ("one: 5\n" `isInfixOf`)
            -- A 2-element list MUST land on "other" — pre-fix the
            -- `a :: []` arm fired for any non-empty list because the
            -- terminal `[]` was ignored by the chain walker.
            runOut `shouldSatisfy` ("other\n" `isInfixOf`)
