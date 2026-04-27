module Sky.Type.RecordFieldExactnessSpec (spec) where

-- Regression fence for "closed records reject extra/missing fields".
--
-- Pre-fix bug: `unifyRecords` in src/Sky/Type/Unify.hs (lines 168-172
-- pre-fix) hit a fallback "create fresh extension and merge" branch
-- whenever the field sets differed — even when both sides were
-- closed records (no row-extension variable). This silently accepted
-- record literals with completely wrong field names against an
-- explicit record-typed annotation:
--
--     takesRecord : { name : String, count : Int } -> String
--     takesRecord { id = 1, label = "x" }            -- WAS accepted
--
-- The runtime then panicked with cryptic
-- `rt.AsInt: expected numeric value, got <nil>` when codegen tried
-- to read the missing field. Surfaced from a real-world Std.Ui
-- port: `Border.shadow { offset = 1, size = 2, blur = 4, color = ... }`
-- (wrong field names — actual is `{offsetX, offsetY, blur, spread,
-- color}`) passed sky check + sky build, then panicked at runtime.
--
-- Fix: respect each record's closed/open status. When EITHER side
-- is closed (extension bound to `EmptyRecord1`), the opposite side's
-- extra fields are illegal — fail unification. Open records (still
-- a FlexVar extension) keep the row-poly merge.

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


checkOnly :: [(FilePath, String)] -> IO (Int, String)
checkOnly files =
    withSystemTempDirectory "sky-record-exactness" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        mapM_ (\(p, c) -> do
            let full = tmp </> p
            writeFile full c) files
        writeFile (tmp </> "sky.toml") "name = \"record-exact-test\"\n"
        let cmd = "cd " ++ tmp ++ " && " ++ sky ++ " check src/Main.sky 2>&1"
        (ec, sout, serr) <- readCreateProcessWithExitCode (shell cmd) ""
        let combined = sout ++ serr
            ecInt = case ec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        return (ecInt, combined)


spec :: Spec
spec = do
    describe "Closed records reject mismatched field sets" $ do

        it "in-module: wrong-field-name record literal fails type-check" $ do
            -- Pre-fix: passes silently. Post-fix: rejected.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "takesRecord : { name : String, count : Int } -> String"
                    , "takesRecord r = r.name"
                    , ""
                    , "test : String"
                    , "test = takesRecord { id = 1, label = \"foo\" }"
                    , ""
                    , "main = println test"
                    ]
            (ec, out) <- checkOnly [("src/Main.sky", src)]
            ec `shouldBe` 1
            out `shouldSatisfy` ("Type mismatch" `isInfixOf`)

        it "cross-module: wrong-field-name record literal fails type-check" $ do
            -- Same shape but the function lives in a dep module.
            -- Pre-fix this passed even more silently because the
            -- externals path also dropped detail. Post-fix: rejected.
            let lib = unlines
                    [ "module Lib exposing (..)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , ""
                    , "takesRecord : { name : String, count : Int } -> String"
                    , "takesRecord r = r.name"
                    ]
                main_ = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Lib"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "test : String"
                    , "test = Lib.takesRecord { id = 1, label = \"foo\" }"
                    , ""
                    , "main = println test"
                    ]
            (ec, out) <- checkOnly [("src/Lib.sky", lib), ("src/Main.sky", main_)]
            ec `shouldBe` 1
            out `shouldSatisfy` ("Type mismatch" `isInfixOf`)

        it "correct-shape record literal still passes" $ do
            -- Sanity: closed-record exactness only rejects when fields
            -- actually disagree. Right shape must still type-check.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "takesRecord : { name : String, count : Int } -> String"
                    , "takesRecord r = r.name"
                    , ""
                    , "test : String"
                    , "test = takesRecord { name = \"alice\", count = 7 }"
                    , ""
                    , "main = println test"
                    ]
            (ec, out) <- checkOnly [("src/Main.sky", src)]
            ec `shouldBe` 0
            out `shouldSatisfy` ("No errors found" `isInfixOf`)


    describe "Cross-module externals register all top-level names (not only functions)" $ do

        it "applying a non-function value as if it were a function fails" $ do
            -- Pre-fix: `Ui.fill : Length` (bare value) was filtered out
            -- of the cross-module externals because `isFunctionType`
            -- only kept TLambda. Call sites then fell through to
            -- CLocal (treated as polymorphic) and `Ui.fill 1`
            -- type-checked silently. Post-fix: the externals filter
            -- is dropped, so the constraint correctly fails with
            -- "Length vs a -> b".
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Ui as Ui"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "test : any"
                    , "test = Ui.width (Ui.fill 1)"
                    , ""
                    , "main = let _ = test in println \"ok\""
                    ]
            (ec, out) <- checkOnly [("src/Main.sky", src)]
            ec `shouldBe` 1
            out `shouldSatisfy` ("Type" `isInfixOf`)
            -- The error names the offender so users know what to fix.
            out `shouldSatisfy` (\s -> "Std.Ui.fill" `isInfixOf` s
                                    || "fill" `isInfixOf` s)
