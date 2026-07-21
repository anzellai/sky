module Sky.Build.PartialKernelAppSpec (spec) where

-- Regression spec for #463 + #465 — partial application of a typed
-- FFI kernel.
--
-- Both bugs share a codegen pathology: when a kernel is called with
-- FEWER args than its declared arity, the lowerer used to route the
-- under-arity call to the typed companion (e.g. `rt.Regex_replaceT`,
-- `rt.JsonDec_decodeString`). The typed companion is strict about
-- arg count, so `go build` rejected the emitted Go with:
--
--     not enough arguments in call to rt.Regex_replaceT
--         have (string, string)
--         want (string, string, string)
--
-- #463 — 3-arg kernel called with 2 args:
--     List.map (Regex.replace "-" "_") xs
--
-- #465 — 2-arg kernel called with 1 arg:
--     List.map (JsonDec.decodeString decoder) inputs
--     -- or in pipe form:
--     JsonDec.decodeString decoder |> Result.andThen processFn
--
-- Post-fix, partial-app of a kernel emits a closure capturing the
-- supplied args and taking the remaining as `any`-typed lambda
-- params, then calls the DYNAMIC (any-typed) kernel form which
-- accepts `any` for every position:
--
--     func(__pk0 any) any { return rt.Regex_replace("-", "_", __pk0); }
--
-- The closure satisfies any-typed HOF slots (List.map's first param
-- routes through Coerce[func(any) any]) without further coercion.

import Test.Hspec
import System.Directory (getCurrentDirectory, doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))
import Data.List (isInfixOf)


findSky :: IO FilePath
findSky = do
    cwd <- getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- doesFileExist c
    if ok then return c else fail ("missing: " ++ c)


-- Compile a self-contained Main.sky inside a tempdir, then optionally
-- run the resulting binary. Mirrors PointFreePolyAliasSpec's helper
-- shape.
buildInTmp :: FilePath -> String -> (FilePath -> ExitCode -> String -> String -> IO ()) -> IO ()
buildInTmp slug src k = do
    sky <- findSky
    withSystemTempDirectory slug $ \tmp -> do
        writeFile (tmp </> "Main.sky") src
        let cp = (proc sky ["build", "Main.sky"]) { cwd = Just tmp }
        (ec, out, err) <- readCreateProcessWithExitCode cp ""
        k tmp ec out err


-- Run the produced sky-out/app binary.
runApp :: FilePath -> IO (ExitCode, String, String)
runApp tmp = do
    let bin = tmp </> "sky-out" </> "app"
    let cp = (proc bin []) { cwd = Just tmp }
    readCreateProcessWithExitCode cp ""


spec :: Spec
spec = describe "partial application of typed FFI kernels (#463 + #465)" $ do

    it "#463 — 3-arg kernel partial-applied to 2 args inside List.map builds + runs" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.List as List"
                , "import Sky.Core.Regex as Regex"
                , "import Sky.Core.String as String"
                , "import Std.Log exposing (println)"
                , ""
                , "main ="
                , "    let"
                , "        xs ="
                , "            [ \"a-b\", \"c-d\" ]"
                , ""
                , "        ys ="
                , "            List.map (Regex.replace \"-\" \"_\") xs"
                , "    in"
                , "    println (String.join \" \" ys)"
                ]
        buildInTmp "sky-pk-463" src $ \tmp ec out err -> do
            let combined = out ++ err
            -- The pre-fix error string is the bug's signature; assert
            -- it does NOT appear.
            ("not enough arguments in call to rt.Regex_replaceT"
                `isInfixOf` combined) `shouldBe` False
            ec `shouldBe` ExitSuccess
            ("Build complete" `isInfixOf` combined) `shouldBe` True
            -- Run the binary and check output.
            (runEc, runOut, _) <- runApp tmp
            runEc `shouldBe` ExitSuccess
            (filter (/= '\n') runOut) `shouldBe` "a_b c_d"

    it "#463 — emitted Go uses a closure, not a direct under-arity call" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.List as List"
                , "import Sky.Core.Regex as Regex"
                , ""
                , "main ="
                , "    let _ = List.map (Regex.replace \"-\" \"_\") [\"a-b\"]"
                , "    in ()"
                ]
        buildInTmp "sky-pk-463-shape" src $ \tmp ec _ _ -> do
            ec `shouldBe` ExitSuccess
            body <- readFile (tmp </> "sky-out" </> "main.go")
            -- The closure form: an `any` lambda body that calls the
            -- DYNAMIC kernel (Regex_replace, not Regex_replaceT).
            -- Note: the closure may appear wrapped in rt.Coerce[...]
            -- depending on the surrounding HOF; we just want the
            -- substring proof that the closure form is emitted.
            ("rt.Regex_replace(\"-\", \"_\", __pk0)" `isInfixOf` body)
                `shouldBe` True
            -- Pre-fix shape: bare typed-T call with 2 args. MUST be
            -- absent — if it slips back in, go build will reject it.
            ("rt.Regex_replaceT(\"-\", \"_\")" `isInfixOf` body)
                `shouldBe` False

    it "#465 — 2-arg kernel partial-applied to 1 arg inside List.map builds + runs" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.Json.Decode as JsonDec"
                , "import Sky.Core.List as List"
                , "import Sky.Core.String as String"
                , "import Std.Log exposing (println)"
                , ""
                , "decoder : JsonDec.Decoder String"
                , "decoder ="
                , "    JsonDec.string"
                , ""
                , "main ="
                , "    let"
                , "        inputs ="
                , "            [ \"\\\"a\\\"\", \"\\\"b\\\"\" ]"
                , ""
                , "        results ="
                , "            List.map (JsonDec.decodeString decoder) inputs"
                , "    in"
                , "    println (String.fromInt (List.length results))"
                ]
        buildInTmp "sky-pk-465" src $ \tmp ec out err -> do
            let combined = out ++ err
            -- The pre-fix error: `not enough arguments` on the typed
            -- companion. Must NOT appear.
            ("not enough arguments in call to rt.JsonDec_decodeString"
                `isInfixOf` combined) `shouldBe` False
            ec `shouldBe` ExitSuccess
            ("Build complete" `isInfixOf` combined) `shouldBe` True
            (runEc, runOut, _) <- runApp tmp
            runEc `shouldBe` ExitSuccess
            (filter (/= '\n') runOut) `shouldBe` "2"

    it "#465 — emitted Go uses a closure for the partial-applied decoder" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.Json.Decode as JsonDec"
                , "import Sky.Core.List as List"
                , ""
                , "decoder : JsonDec.Decoder String"
                , "decoder = JsonDec.string"
                , ""
                , "main ="
                , "    let _ = List.map (JsonDec.decodeString decoder) [\"\\\"a\\\"\"]"
                , "    in ()"
                ]
        buildInTmp "sky-pk-465-shape" src $ \tmp ec _ _ -> do
            ec `shouldBe` ExitSuccess
            body <- readFile (tmp </> "sky-out" </> "main.go")
            -- Closure body calls the dynamic kernel.
            ("rt.JsonDec_decodeString(decoder(), __pk0)" `isInfixOf` body)
                `shouldBe` True

    it "full-application of the same kernel still routes through the typed companion (no regression)" $ do
        -- Soundness check: the new partial-app arm fires ONLY when
        -- args < arity. Full-arg calls must continue to use the
        -- typed dispatch path (Regex_replaceT for literal args).
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Sky.Core.Regex as Regex"
                , "import Std.Log exposing (println)"
                , ""
                , "main ="
                , "    println (Regex.replace \"a\" \"b\" \"banana\")"
                ]
        buildInTmp "sky-pk-fullapp" src $ \tmp ec _ _ -> do
            ec `shouldBe` ExitSuccess
            body <- readFile (tmp </> "sky-out" </> "main.go")
            -- Full-app still uses the typed companion (literal-args
            -- arm at typedKernelLiterals).
            ("rt.Regex_replaceT" `isInfixOf` body) `shouldBe` True
            -- And no spurious closure for the full-app site.
            ("__pk0 any" `isInfixOf` body) `shouldBe` False
