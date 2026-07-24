module Sky.Build.GoKeywordCollisionSpec (spec) where

-- Regression fence for "Sky function name collides with Go reserved
-- word at the call site".
--
-- Pre-fix bug: the `Can.Call (VarTopLevel _ name)` codegen branch in
-- `Sky.Build.Compile` (around line 3633) used the raw Sky `name` for
-- the qualified call-site identifier whenever the callee lived in
-- the entry (`Main`) module — bypassing the `goSafeName` escape that
-- the function-definition path (line ~2048) already applies. So a
-- Sky function named `go`, `defer`, `chan`, `make`, `len`, etc.
-- defined in Main would emit:
--   func go_(n int) int { ... }   -- definition: sanitised
--   ... go(rt.CoerceInt(41)) ...  -- call: NOT sanitised
-- and `go build` rejected the call site with:
--   syntax error: unexpected keyword go, expected expression
--   expression in go must not be parenthesized
-- (Go's parser interprets `go(...)` as a goroutine launch.)
--
-- Surfaced from a real-world Std.Ui port report (2026-04-27, CC
-- session) where the AI mistakenly attributed the failure to a
-- parser bug — the source actually parsed and type-checked fine; it
-- was the emitted Go that broke. See the fix comment at the
-- corresponding line in Sky.Build.Compile.
--
-- Fix: apply `goSafeName` to `name` in the Main-module call-site
-- branch (and to `name` in the cross-module branch too, defensively).

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


buildOnly :: String -> IO (Int, String)
buildOnly src =
    withSystemTempDirectory "sky-go-keyword" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"go-keyword-test\"\n"
        let cmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (ec, sout, serr) <- readCreateProcessWithExitCode (shell cmd) ""
        let combined = sout ++ serr
            ecInt = case ec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        return (ecInt, combined)


spec :: Spec
spec = do
    describe "Sky function names that match Go reserved words sanitise at call sites too" $ do

        it "Sky function `go` defined in Main builds + the emitted call site uses go_, not go" $ do
            -- Pre-fix: `go build` fails with
            --   "syntax error: unexpected keyword go, expected expression"
            -- Post-fix: definition AND call site emit `go_`.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "go : Int -> Int"
                    , "go n = n + 1"
                    , ""
                    , "main ="
                    , "    println (String.fromInt (go 41))"
                    ]
            (ec, out) <- buildOnly src
            -- The pre-fix failure mode emits the Go-parser error verbatim.
            out `shouldNotSatisfy` ("unexpected keyword go" `isInfixOf`)
            out `shouldNotSatisfy` ("expression in go must not" `isInfixOf`)
            ec `shouldBe` 0
            out `shouldSatisfy` ("Build complete" `isInfixOf`)


        it "Sky function `defer` defined in Main builds (same root cause)" $ do
            -- Same class as `go`. `defer` in Go is also a statement
            -- keyword. Confirms the fix is general (not a one-off
            -- special case for `go`).
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "defer : Int -> Int"
                    , "defer n = n * 2"
                    , ""
                    , "main ="
                    , "    println (String.fromInt (defer 21))"
                    ]
            (ec, out) <- buildOnly src
            out `shouldNotSatisfy` ("unexpected keyword defer" `isInfixOf`)
            ec `shouldBe` 0
            out `shouldSatisfy` ("Build complete" `isInfixOf`)
