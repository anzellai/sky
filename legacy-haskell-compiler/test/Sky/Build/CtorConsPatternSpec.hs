module Sky.Build.CtorConsPatternSpec (spec) where

-- Regression fence for "cons pattern INSIDE a constructor arg" —
-- the inverse of ConsCtorPatternSpec (which guards against
-- "constructor pattern AS the cons head").
--
-- Pre-fix bug: `argPatternCondition` in src/Sky/Build/Compile.hs
-- only narrowed the outer ctor branch when the sub-pattern was a
-- ctor or literal (PCtor / PInt / PStr / PBool / PChr). PCons
-- (and PList) inside a ctor arg fell through to `_ -> Nothing`,
-- so no length check was emitted. The outer ctor branch then
-- swallowed the case and the binding code (`rt.AsList(.JustValue)
-- [0]`) panicked at runtime with "index out of range" when the
-- inner list was empty.
--
-- Symptom that surfaced this:
--
--     regionOf : List String -> String
--     regionOf parts =
--         case List.tail parts of
--             Just (r :: _) -> String.toUpper r
--             _ -> ""
--
-- For `regionOf ["en"]`, `List.tail` returns `Just []`. The
-- `Just (r :: _)` arm matched (only `Tag == 0` was checked), then
-- destructure panicked. Workaround was to nest the match —
-- `Just xs -> case xs of (r :: _) -> … | _ -> ""` — which DOES
-- check inner length via the standard PCons rule.
--
-- Fix: argPatternCondition handles PCons (>= 1 length check) and
-- PList (== N length check) explicitly, joined to the outer
-- ctor-tag check via && so both must hold for the branch to fire.

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
    withSystemTempDirectory "sky-ctor-cons" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"ctor-cons-test\"\n"
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
    describe "Cons-pattern inside a ctor arg narrows by inner length" $ do

        it "Just (h :: _) — exact bug-report repro for I18n.regionOf" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.List as List"
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "regionOf : List String -> String"
                    , "regionOf parts ="
                    , "    case List.tail parts of"
                    , "        Just (r :: _) -> String.toUpper r"
                    , "        _ -> \"\""
                    , ""
                    , "main ="
                    , "    let"
                    , "        _ = println (regionOf [\"en\", \"GB\"])"
                    , "        _ = println (regionOf [\"en\"])"
                    , "        _ = println (regionOf [])"
                    , "    in"
                    , "        println \"done\""
                    ]
            (ec, bout, rout) <- buildAndRun src
            ec `shouldBe` 0
            rout `shouldNotSatisfy` ("panic" `isInfixOf`)
            rout `shouldNotSatisfy` ("index out of range" `isInfixOf`)
            -- Behaviour: ["en", "GB"] → "GB", ["en"] → "" (Just []),
            -- [] → "" (Nothing). Order matters because println
            -- writes lines in sequence.
            rout `shouldSatisfy` ("GB" `isInfixOf`)
            rout `shouldSatisfy` ("done" `isInfixOf`)
            -- Also ignore the build-step output — only the run-step
            -- output is examined for panics. (`bout` is captured for
            -- forensics if the assert fires.)
            length bout `shouldSatisfy` (>= 0)


        it "Ok (x :: rest) — same hazard for Result.Ok over an empty list" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "headOf : Result String (List Int) -> String"
                    , "headOf r ="
                    , "    case r of"
                    , "        Ok (x :: _) -> String.fromInt x"
                    , "        _ -> \"none\""
                    , ""
                    , "main ="
                    , "    let"
                    , "        _ = println (headOf (Ok [42, 7]))"
                    , "        _ = println (headOf (Ok []))"
                    , "        _ = println (headOf (Err \"boom\"))"
                    , "    in"
                    , "        println \"done\""
                    ]
            (ec, _bout, rout) <- buildAndRun src
            ec `shouldBe` 0
            rout `shouldNotSatisfy` ("panic" `isInfixOf`)
            rout `shouldSatisfy` ("42" `isInfixOf`)
            -- Ok [] and Err "boom" both fall through to "none" — so
            -- "none" should appear at least once.
            rout `shouldSatisfy` ("none" `isInfixOf`)


        it "Just [a, b] — fixed-length list inside ctor narrows on size" $ do
            -- Sister case: a fixed-length PList sub-pattern. Same
            -- failure mode — pre-fix the outer Just match swallowed
            -- the case and the [a, b] destructure assumed wrong size.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "pair : Maybe (List Int) -> String"
                    , "pair m ="
                    , "    case m of"
                    , "        Just [a, b] ->"
                    , "            String.fromInt a ++ \"+\" ++ String.fromInt b"
                    , "        _ -> \"other\""
                    , ""
                    , "main ="
                    , "    let"
                    , "        _ = println (pair (Just [3, 4]))"
                    , "        _ = println (pair (Just [9]))"
                    , "        _ = println (pair (Just [1, 2, 3]))"
                    , "        _ = println (pair Nothing)"
                    , "    in"
                    , "        println \"done\""
                    ]
            (ec, _bout, rout) <- buildAndRun src
            ec `shouldBe` 0
            rout `shouldNotSatisfy` ("panic" `isInfixOf`)
            rout `shouldSatisfy` ("3+4" `isInfixOf`)
            rout `shouldSatisfy` ("other" `isInfixOf`)
