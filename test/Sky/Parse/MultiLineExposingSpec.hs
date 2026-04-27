module Sky.Parse.MultiLineExposingSpec (spec) where

-- Regression fence for the multi-line `module ... exposing (…)` and
-- `import X exposing (…)` parser fix.
--
-- Pre-fix bug: the exposing-list parser used `spaces` (which only
-- skips ` ` and `\t`) between items, so source like
--
--     module Foo exposing
--         ( a
--         , b
--         )
--
-- silently failed inside `exposingClause`. Worse, the failure was
-- swallowed by `oneOfWithFallback` for imports (returning an empty
-- exposing list, silently dropping all imports) and downgraded to
-- a `Warning: could not parse …` for the module header (silently
-- dropping the module from the build graph entirely).
--
-- Fix: replace `spaces` with `freshLine` inside the exposing list
-- (newlines are layout-irrelevant inside parens), and turn parser
-- errors at the module-graph stage into FATAL errors.
--
-- Both invariants pinned here: the multi-line shape compiles + runs;
-- a real parse error fails the build (not a warning).

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


-- Build a tiny one-file Sky project from a literal source string,
-- run `sky build src/Main.sky`, and return (exitCode, stdout++stderr).
buildLiteral :: String -> IO (Int, String)
buildLiteral src =
    withSystemTempDirectory "sky-multiline" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"multiline-test\"\n"
        let cmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (ec, sout, serr) <- readCreateProcessWithExitCode (shell cmd) ""
        let combined = sout ++ serr
            ecInt = case ec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        return (ecInt, combined)


spec :: Spec
spec = do
    describe "Multi-line `exposing (…)` parses + works end-to-end" $ do

        it "module header with multi-line exposing list compiles + runs" $ do
            let src = unlines
                    [ "module Main exposing"
                    , "    ( main"
                    , "    , double"
                    , "    )"
                    , ""
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "double : Int -> Int"
                    , "double n = n * 2"
                    , ""
                    , "main ="
                    , "    println (String.fromInt (double 21))"
                    ]
            (ec, out) <- buildLiteral src
            ec `shouldBe` 0
            out `shouldSatisfy` ("Build complete" `isInfixOf`)

        it "import with multi-line exposing list works" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.String as String"
                    , "import Std.Log exposing"
                    , "    ( println"
                    , "    )"
                    , ""
                    , "main ="
                    , "    println \"hello multi-line\""
                    ]
            (ec, out) <- buildLiteral src
            ec `shouldBe` 0
            out `shouldSatisfy` ("Build complete" `isInfixOf`)

        it "one-export-per-line with leading commas (the canonical sky fmt shape)" $ do
            let src = unlines
                    [ "module Main exposing"
                    , "    ( main"
                    , "    , a"
                    , "    , b"
                    , "    , c"
                    , "    )"
                    , ""
                    , "import Std.Log exposing (println)"
                    , ""
                    , "a : Int"
                    , "a = 1"
                    , ""
                    , "b : Int"
                    , "b = 2"
                    , ""
                    , "c : Int"
                    , "c = 3"
                    , ""
                    , "main = println \"ok\""
                    ]
            (ec, _) <- buildLiteral src
            ec `shouldBe` 0

    describe "Parse errors are now FATAL (not silently downgraded to warnings)" $ do

        it "an unparseable module fails the build hard, not just a warning" $ do
            -- Genuinely broken syntax: missing closing paren on the
            -- exposing list. Pre-fix this would emit
            -- `Warning: could not parse src/Main.sky: …` and proceed
            -- with 0 modules (then fail in some inscrutable way later).
            -- Post-fix it must abort the build with a non-zero exit.
            let src = unlines
                    [ "module Main exposing (main"     -- missing )
                    , ""
                    , "main = 1"
                    ]
            (ec, out) <- buildLiteral src
            ec `shouldNotBe` 0
            out `shouldSatisfy` ("PARSE ERROR" `isInfixOf`)
