module Sky.Canonicalise.HeadAliasFunctionSigSpec (spec) where

-- Regression fence for "a value def whose entire signature is a type
-- alias expanding to a function compiles". Pre-fix
-- (canonicaliseValue in src/Sky/Canonicalise/Module.hs), the
-- param/return split (`arrowArgs` / `arrowResultN`) only peeled
-- `TLambda`. An alias reference at canonicalisation time is a
-- nominal `TType`, so when a user wrote
--
--     type alias Handler = Request -> Task Error Response
--
--     myHandler : Handler
--     myHandler req = Task.succeed (Server.text "ok")
--
-- the def's single param was dropped and the body was checked
-- against the unpeeled `Handler` alias — surfacing as a confusing
-- "expected Handler, got ..." mismatch. This is canonical Elm
-- syntax (e.g. `type alias Renderer msg = Model -> Html msg`;
-- `view : Renderer Msg`); Sky should match.
--
-- Fix (cherry-picked from contributor PR #123, src/Sky/Canonicalise/
-- Module.hs portion only): `unfoldHeadAlias` peels a `TAlias` at the
-- HEAD of a value annotation before the split, with a visited-set
-- guarding mutual recursion. Argument / return leaf types keep
-- their nominal form, so existing typed lowering of ordinary
-- `f : Rec -> String` signatures is byte-for-byte unchanged.

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


-- v0.16.8 #499 — single retry on non-zero exit.  Closes the
-- full-sweep flake class where running this spec after 519 other
-- subprocess-heavy specs hits transient OS pressure (FD limit,
-- go-build cache contention, IO scheduler stalls).  Each spec
-- already creates its own withSystemTempDirectory, so the retry
-- gets a fresh tmp dir.  If the second attempt also fails the
-- captured sky-check output is surfaced via `out` for diagnosis
-- — the assertion paths below print it on `shouldBe` failure.
checkOnly :: String -> IO (Int, String)
checkOnly src = do
    (ec1, out1) <- checkOnlyOnce src
    if ec1 == 0
        then return (ec1, out1)
        else do
            (ec2, out2) <- checkOnlyOnce src
            return (ec2, out2 ++ "\n--- previous attempt (ec=" ++ show ec1 ++ ") ---\n" ++ out1)


checkOnlyOnce :: String -> IO (Int, String)
checkOnlyOnce src =
    withSystemTempDirectory "sky-head-alias" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "src" </> "Main.sky") src
        writeFile (tmp </> "sky.toml") "name = \"head-alias-test\"\n"
        let cmd = "cd " ++ tmp ++ " && " ++ sky ++ " check src/Main.sky 2>&1"
        (ec, sout, serr) <- readCreateProcessWithExitCode (shell cmd) ""
        let combined = sout ++ serr
            ecInt = case ec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        return (ecInt, combined)


-- v0.16.8 #499 — assert exit code, surfacing captured sky-check
-- output on failure so flakes are actionable not opaque.
assertCleanExit :: HasCallStack => Int -> String -> Expectation
assertCleanExit ec out =
    if ec == 0
        then return ()
        else expectationFailure $
            "sky check exited " ++ show ec ++ ", captured output:\n" ++ out


spec :: Spec
spec = do
    describe "Head-position type alias of a function signature compiles" $ do

        it "concrete function-typed alias at head with matching params" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Adder ="
                    , "    Int -> Int -> Int"
                    , ""
                    , "add : Adder"
                    , "add a b = a + b"
                    , ""
                    , "main = println (String.fromInt (add 2 3))"
                    ]
            (ec, out) <- checkOnly src
            assertCleanExit ec out
            out `shouldSatisfy` ("No errors found" `isInfixOf`)


        it "generic function-typed alias at head with type-var instantiation" $ do
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Pred a ="
                    , "    a -> Bool"
                    , ""
                    , "isPositive : Pred Int"
                    , "isPositive n = n > 0"
                    , ""
                    , "main ="
                    , "    if isPositive 5 then"
                    , "        println \"ok\""
                    , "    else"
                    , "        println \"no\""
                    ]
            (ec, out) <- checkOnly src
            assertCleanExit ec out
            out `shouldSatisfy` ("No errors found" `isInfixOf`)


        it "head non-function alias (Int wrapper) still compiles unchanged" $ do
            -- Sanity that the unfold doesn't disturb the existing
            -- non-function head-alias path.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Count ="
                    , "    Int"
                    , ""
                    , "n : Count"
                    , "n = 42"
                    , ""
                    , "main = println (String.fromInt n)"
                    ]
            (ec, out) <- checkOnly src
            assertCleanExit ec out
            out `shouldSatisfy` ("No errors found" `isInfixOf`)


        it "alias at leaf position (existing path) unchanged" $ do
            -- Pre-fix already worked: alias as RETURN type or PARAM
            -- type (not head of whole annotation) doesn't need
            -- peeling because the surrounding TLambda is split fine.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Tag ="
                    , "    String"
                    , ""
                    , "wrap : Int -> Tag"
                    , "wrap n = \"#\" ++ String.fromInt n"
                    , ""
                    , "main = println (wrap 7)"
                    ]
            (ec, out) <- checkOnly src
            assertCleanExit ec out
            out `shouldSatisfy` ("No errors found" `isInfixOf`)


        it "function-typed alias as both head AND leaf (middleware shape)" $ do
            -- The composer pattern: a head alias whose body
            -- contains another arrow that itself returns an alias.
            -- Tests that head-only unfolding is sufficient — the
            -- nested return-position alias stays nominal and HM
            -- still unifies through it.
            let src = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , ""
                    , "type alias Op ="
                    , "    Int -> Int"
                    , ""
                    , "type alias Decorator ="
                    , "    Op -> Op"
                    , ""
                    , "double : Decorator"
                    , "double f = \\n -> f n * 2"
                    , ""
                    , "main ="
                    , "    let"
                    , "        inc = \\n -> n + 1"
                    , "        decorated = double inc"
                    , "    in"
                    , "        println (String.fromInt (decorated 3))"
                    ]
            (ec, out) <- checkOnly src
            assertCleanExit ec out
            out `shouldSatisfy` ("No errors found" `isInfixOf`)
