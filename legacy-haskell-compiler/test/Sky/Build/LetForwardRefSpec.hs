module Sky.Build.LetForwardRefSpec (spec) where

-- Phase 2.5 — Limitation 15 regression fence.
--
-- Pre-fix: let-bindings in source order produced lowered Go in
-- source order. A forward reference (using a name defined later)
-- type-checked fine (HM treats let as recursive) but failed at
-- `go build` time with "undefined: NAME".
--
-- Symptom:
--
--   main =
--       let
--           callFirst = useSecond 5        -- forward ref
--           useSecond n = n * 2
--       in
--           println (String.fromInt callFirst)
--
--   → Code generation produced Go that `go build` rejected:
--     ./main.go:17:26: undefined: useSecond
--
-- Fix: canonicaliseLet topologically sorts the bindings before
-- folding into nested Can.Let. Each binding ends up emitted AFTER
-- its dependencies, so the lowered Go is in dependency order
-- regardless of source order. Cycles (mutual recursion between
-- value bindings) fall back to source order — the existing
-- codegen error is a useful nudge to refactor to top-level
-- (which DOES support mutual recursion).

import Test.Hspec
import qualified System.Directory as Dir
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import qualified System.Process as Proc
import System.Exit (ExitCode(..))


findSky :: IO FilePath
findSky = do
    cwd <- Dir.getCurrentDirectory
    let c = cwd </> "sky-out" </> "sky"
    ok <- Dir.doesFileExist c
    if ok then pure c else fail ("missing: " ++ c)


-- | Run `sky build` on a temp project and return (exit code,
-- combined stdout+stderr, path-to-binary-if-built).
buildAndRun :: FilePath -> IO (Int, String, Maybe String)
buildAndRun dir = do
    sky <- findSky
    let buildCmd = "cd " ++ dir ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
    (bec, bout, _) <- Proc.readCreateProcessWithExitCode (Proc.shell buildCmd) ""
    case bec of
        ExitFailure n -> pure (n, bout, Nothing)
        ExitSuccess -> do
            let app = dir </> "sky-out" </> "app"
            ok <- Dir.doesFileExist app
            if not ok
                then pure (0, bout, Nothing)
                else do
                    (rec_, rout, _) <- Proc.readCreateProcessWithExitCode
                        (Proc.proc app []) ""
                    case rec_ of
                        ExitSuccess     -> pure (0, rout, Just rout)
                        ExitFailure n   -> pure (n, rout, Nothing)


scaffold :: FilePath -> String -> IO ()
scaffold root mainSrc = do
    Dir.createDirectoryIfMissing True (root </> "src")
    writeFile (root </> "sky.toml") "name = \"lim15\"\n"
    writeFile (root </> "src" </> "Main.sky") mainSrc


spec :: Spec
spec = describe "Limitation 15 — let forward references" $ do

    it "callFirst = useSecond 5 ; useSecond n = n * 2 — builds + runs" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.String as String"
                , "import Std.Log exposing (println)"
                , ""
                , "main ="
                , "    let"
                , "        callFirst = useSecond 5"
                , "        useSecond n = n * 2"
                , "    in"
                , "        println (String.fromInt callFirst)"
                ]
        withSystemTempDirectory "sky-lim15-fwd" $ \dir -> do
            scaffold dir src
            (code, output, runOut) <- buildAndRun dir
            if code /= 0
                then expectationFailure $
                    "build/run failed (Lim 15 still open?):\n" ++ output
                else case runOut of
                    Just out -> out `shouldBe` "10\n"
                    Nothing  -> expectationFailure "binary didn't produce output"

    it "deeper forward chain — a → b → c (defined in reverse order)" $ do
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.String as String"
                , "import Std.Log exposing (println)"
                , ""
                , "main ="
                , "    let"
                , "        a = b 1"
                , "        b n = c (n + 10)"
                , "        c n = n * 2"
                , "    in"
                , "        println (String.fromInt a)"
                ]
        withSystemTempDirectory "sky-lim15-deep" $ \dir -> do
            scaffold dir src
            (code, output, runOut) <- buildAndRun dir
            if code /= 0
                then expectationFailure $
                    "deep-forward build failed:\n" ++ output
                else case runOut of
                    Just out -> out `shouldBe` "22\n"  -- (1+10)*2
                    Nothing  -> expectationFailure "no output"

    it "non-forward order still works (no regression)" $ do
        -- Sanity: when source order ALREADY matches dependency
        -- order, the topo sort must be a no-op.
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.String as String"
                , "import Std.Log exposing (println)"
                , ""
                , "main ="
                , "    let"
                , "        helper n = n * 2"
                , "        result = helper 7"
                , "    in"
                , "        println (String.fromInt result)"
                ]
        withSystemTempDirectory "sky-lim15-noregr" $ \dir -> do
            scaffold dir src
            (code, output, runOut) <- buildAndRun dir
            if code /= 0
                then expectationFailure $
                    "sane order build broken:\n" ++ output
                else case runOut of
                    Just out -> out `shouldBe` "14\n"
                    Nothing  -> expectationFailure "no output"
