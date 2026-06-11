module Sky.Build.ListLiteralPatternSpec (spec) where

-- Regression fence for #587 — list-literal pattern element
-- discriminator codegen.
--
-- Pre-fix bug: the `Can.PList` arm of `patternCondition` checked
-- only the list length, then deferred element pattern matching to
-- the body bindings.  But bindings run AFTER the arm gate, so
-- `case xs of [Star Nothing] -> "M"; _ -> "F"` accepted ANY
-- 1-element list — both `[Col "id"]` and `[Star (Just "x")]`
-- silently matched the first arm and ran its body on a wrong-
-- shaped element.  Same defect class as #583 (cons-pattern non-
-- head tag check) and #402 (cons-pattern length guard).
--
-- Fix: PList in both `patternCondition` and the nested
-- `patternConditionForExpr` now AND-in `patternConditionForExpr`
-- per element against `rt.AsList(subj)[i]`.  PCtor inside
-- `patternConditionForExpr` additionally recurses into its arg
-- patterns via `rt.ResultOk` / `rt.ResultErr` / `rt.MaybeJust`
-- (for Result/Maybe) or `rt.AdtField(subj, idx)` (for user ADTs).

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


buildAndRun :: String -> IO (Int, String, String)
buildAndRun src =
    withSystemTempDirectory "sky-list-lit" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"list-lit-test\"\n"
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
    describe "List-literal pattern element discriminator (#587)" $ do

        it "single-element list pattern [Ctor Nothing] gates BOTH outer ctor tag AND inner arg" $ do
            -- The exact user-supplied repro from the sky-sqlgen
            -- session.  Pre-fix every line printed MATCHED; post-
            -- fix only `[Star Nothing]` prints MATCHED.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Task as Task"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type Item"
                    , "    = Star (Maybe String)"
                    , "    | Col String"
                    , ""
                    , "matchStar : List Item -> String"
                    , "matchStar items ="
                    , "    case items of"
                    , "        [ Star Nothing ] -> \"MATCHED\""
                    , "        _                -> \"fallthrough\""
                    , ""
                    , "main ="
                    , "    Task.run"
                    , "        (let"
                    , "            _ = println (\"a: \" ++ matchStar [ Star Nothing ])"
                    , "            _ = println (\"b: \" ++ matchStar [ Star (Just \"x\") ])"
                    , "            _ = println (\"c: \" ++ matchStar [ Col \"id\" ])"
                    , "            _ = println (\"d: \" ++ matchStar [ Star Nothing, Col \"a\" ])"
                    , "            _ = println (\"e: \" ++ matchStar [])"
                    , "         in"
                    , "         Task.succeed ()"
                    , "        )"
                    ]
            (ec, build, runOut) <- buildAndRun src
            ec `shouldBe` 0
            build `shouldSatisfy` ("Compilation successful" `isInfixOf`)
            -- [Star Nothing] is the only line that should match.
            runOut `shouldSatisfy` ("a: MATCHED"     `isInfixOf`)
            runOut `shouldSatisfy` ("b: fallthrough" `isInfixOf`)
            runOut `shouldSatisfy` ("c: fallthrough" `isInfixOf`)
            runOut `shouldSatisfy` ("d: fallthrough" `isInfixOf`)
            runOut `shouldSatisfy` ("e: fallthrough" `isInfixOf`)

        it "list-literal pattern [Just 1, Just 2] gates each ctor + each inner Int" $ do
            -- Two-element shape exercises the index-stepping in the
            -- new PList per-element discriminator AND PCtor
            -- arg-recursion for a Maybe Int payload.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Task as Task"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "matchPair : List (Maybe Int) -> String"
                    , "matchPair items ="
                    , "    case items of"
                    , "        [ Just 1, Just 2 ] -> \"MATCHED\""
                    , "        _                  -> \"fallthrough\""
                    , ""
                    , "main ="
                    , "    Task.run"
                    , "        (let"
                    , "            _ = println (\"a: \" ++ matchPair [ Just 1, Just 2 ])"
                    , "            _ = println (\"b: \" ++ matchPair [ Just 1, Just 3 ])"
                    , "            _ = println (\"c: \" ++ matchPair [ Just 1, Nothing ])"
                    , "            _ = println (\"d: \" ++ matchPair [ Nothing, Just 2 ])"
                    , "            _ = println (\"e: \" ++ matchPair [ Just 1 ])"
                    , "         in"
                    , "         Task.succeed ()"
                    , "        )"
                    ]
            (ec, build, runOut) <- buildAndRun src
            ec `shouldBe` 0
            build `shouldSatisfy` ("Compilation successful" `isInfixOf`)
            runOut `shouldSatisfy` ("a: MATCHED"     `isInfixOf`)
            runOut `shouldSatisfy` ("b: fallthrough" `isInfixOf`)
            runOut `shouldSatisfy` ("c: fallthrough" `isInfixOf`)
            runOut `shouldSatisfy` ("d: fallthrough" `isInfixOf`)
            runOut `shouldSatisfy` ("e: fallthrough" `isInfixOf`)

        it "list-literal pattern [Ok x] vs [Err _] correctly discriminates Ok/Err" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Task as Task"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "classify : List (Result String Int) -> String"
                    , "classify xs ="
                    , "    case xs of"
                    , "        [ Ok _ ]  -> \"one-ok\""
                    , "        [ Err _ ] -> \"one-err\""
                    , "        _         -> \"other\""
                    , ""
                    , "main ="
                    , "    Task.run"
                    , "        (let"
                    , "            _ = println (\"a: \" ++ classify [ Ok 7 ])"
                    , "            _ = println (\"b: \" ++ classify [ Err \"oops\" ])"
                    , "            _ = println (\"c: \" ++ classify [ Ok 1, Ok 2 ])"
                    , "            _ = println (\"d: \" ++ classify [])"
                    , "         in"
                    , "         Task.succeed ()"
                    , "        )"
                    ]
            (ec, build, runOut) <- buildAndRun src
            ec `shouldBe` 0
            build `shouldSatisfy` ("Compilation successful" `isInfixOf`)
            runOut `shouldSatisfy` ("a: one-ok"  `isInfixOf`)
            runOut `shouldSatisfy` ("b: one-err" `isInfixOf`)
            runOut `shouldSatisfy` ("c: other"   `isInfixOf`)
            runOut `shouldSatisfy` ("d: other"   `isInfixOf`)
