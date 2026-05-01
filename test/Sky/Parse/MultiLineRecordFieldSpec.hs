module Sky.Parse.MultiLineRecordFieldSpec (spec) where

-- Regression fence for "first record-literal field's value on a new
-- line".
--
-- Pre-fix bug: `exprAtom_` in src/Sky/Parse/Expression.hs handled
-- the FIRST field of a record literal (`{ field = …`) with `spaces`
-- after the `=`, while every SUBSEQUENT field went through
-- `recordField` which uses `freshLine` after the `=`. So:
--
--     call
--         { system =
--             "value"
--         , user = "..."
--         }
--
-- failed at `row=line-of-=, col=just-past-=` with
-- `PARSE ERROR: DeclarationError`, while the same shape on the
-- SECOND field parsed cleanly. Workaround documented in the bug
-- report was lifting the value into a `let` or using the
-- positional auto-constructor.
--
-- Fix: switch the first-field path to `freshLine` so the rule is
-- uniform across fields.

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


checkOnly :: String -> IO (Int, String)
checkOnly src =
    withSystemTempDirectory "sky-multiline-record" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"multiline-record-test\"\n"
        let cmd = "cd " ++ tmp ++ " && " ++ sky ++ " check src/Main.sky 2>&1"
        (ec, sout, serr) <- readCreateProcessWithExitCode (shell cmd) ""
        let combined = sout ++ serr
            ecInt = case ec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        return (ecInt, combined)


spec :: Spec
spec = do
    describe "Multi-line first-field record-literal value" $ do

        it "first field's RHS on next line — minimal" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Pair ="
                    , "    { a : String"
                    , "    , b : String"
                    , "    }"
                    , ""
                    , "use : Pair -> String"
                    , "use p = p.a ++ p.b"
                    , ""
                    , "view : String"
                    , "view ="
                    , "    use"
                    , "        { a ="
                    , "            \"hello \""
                    , "        , b = \"world\""
                    , "        }"
                    , ""
                    , "main = println view"
                    ]
            (ec, out) <- checkOnly src
            ec `shouldBe` 0
            out `shouldNotSatisfy` ("DeclarationError" `isInfixOf`)
            out `shouldSatisfy` ("No errors found" `isInfixOf`)


        it "first field's RHS on next line, with `++` continuation" $ do
            -- The original bug-report reproducer: every field's value
            -- starts on a new line and the value itself is a `++`
            -- chain that wraps onto further continuation lines.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Sky.Core.Error as Error exposing (Error)"
                    , "import Sky.Core.Task as Task"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Prompt ="
                    , "    { system : String"
                    , "    , user : String"
                    , "    , maxTokens : Int"
                    , "    }"
                    , ""
                    , "draftA : String -> String -> String -> Task Error String"
                    , "draftA title category notes ="
                    , "    call"
                    , "        { system ="
                    , "            \"You help a guardian describe a product. \""
                    , "            ++ \"Write 60-90 words.\""
                    , "        , user ="
                    , "            \"Title: \" ++ title"
                    , "            ++ \"\\nCategory: \" ++ category"
                    , "            ++ \"\\nNotes: \" ++ notes"
                    , "        , maxTokens = 300"
                    , "        }"
                    , ""
                    , "call : Prompt -> Task Error String"
                    , "call _ = Task.succeed \"ok\""
                    , ""
                    , "main = println \"ok\""
                    ]
            (ec, out) <- checkOnly src
            ec `shouldBe` 0
            out `shouldNotSatisfy` ("DeclarationError" `isInfixOf`)
            out `shouldSatisfy` ("No errors found" `isInfixOf`)


        it "all fields same-line — sanity" $ do
            -- Already worked pre-fix; locks the existing shape in.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Pair ="
                    , "    { a : String"
                    , "    , b : String"
                    , "    }"
                    , ""
                    , "use : Pair -> String"
                    , "use p = p.a ++ p.b"
                    , ""
                    , "view : String"
                    , "view = use { a = \"hello \", b = \"world\" }"
                    , ""
                    , "main = println view"
                    ]
            (ec, out) <- checkOnly src
            ec `shouldBe` 0
            out `shouldSatisfy` ("No errors found" `isInfixOf`)
