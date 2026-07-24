module Sky.Build.DepNarrowEnvSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


-- v0.17 Wave 3 / step-1 — narrowed dep SolvedTypes env regression.
--
-- DOC.  An earlier attempt (memory note v017_wave3_scope_install_too_aggressive)
-- installed the FULL merged SolvedTypes — including the entry
-- module's `_stEnv` — onto every dep's render-time ctx.  That
-- regressed `02-go-stdlib`-class consumers: the entry module's
-- Task-generic call sites bled into the dep's HM-inferred
-- specialisation, defaulting `Task a` slots that the dep never
-- imports to whatever generic the entry pinned.
--
-- The corrective discipline: filter the entry's `_stEnv` so the
-- dep ctx sees ONLY the bindings the dep actually references
-- (transitively) via its own imports.  The per-module ledger
-- already preserves each dep's own `_stEnv` under
-- `_stPerModuleEnv`; the merged top-level `_stEnv` should NOT
-- leak entry-only bindings into dep emission.
--
-- This spec exercises the regression class: a 2-module fixture
-- where the entry module uses Task-typed callees (Time.now,
-- Process.run, etc) but the dep does NOT import any Task surface.
-- Build must succeed without the dep's compilation getting
-- polluted by entry's Task-generic specialisation.
--
-- ASSERTION.  End-to-end build + run completes; the dep's emitted
-- Go is free of any unexpected `SkyTask` / `func() any` references
-- that would only appear if the entry's env leaked.
spec :: Spec
spec = do
    describe "v0.17 Wave 3 — dep ctx env narrowing (Task-generic guard)" $ do
        it "dep that doesn't import Task surface is unaffected by entry's Task usage" $ do
            sky <- findSky
            withSystemTempDirectory "sky-wave3-narrow" $ \tmp -> do
                writeFixture tmp
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                let goPath = tmp </> "sky-out" </> "main.go"
                hasGo <- doesFileExist goPath
                hasGo `shouldBe` True
                body <- readFile goPath
                -- The dep `Lib.Pure` defines a totally pure
                -- `doubleIt : Int -> Int` — its emission should
                -- NOT contain any `SkyTask` reference (the entry's
                -- Task-generic surface should be invisible to it).
                let depFnStart = "func Lib_Pure_doubleIt"
                case findInfix depFnStart body of
                    Nothing -> fail $
                        "Expected `" ++ depFnStart ++ "` in emitted Go"
                    Just afterStart -> do
                        let (depFnBody, _rest) = scopeFnBody afterStart
                            taskTokens =
                                countSubstring "SkyTask" depFnBody
                                  + countSubstring "func() any" depFnBody
                        -- Pre-fix: dep ctx with leaked entry Task
                        -- generics can route `doubleIt` through a
                        -- SkyTask-shaped wrapper.
                        -- Post-fix: dep emission stays pure
                        -- Int -> Int, no Task scaffolding.
                        taskTokens `shouldBe` 0

        it "binary runs without runtime panic" $ do
            sky <- findSky
            withSystemTempDirectory "sky-wave3-narrow-2" $ \tmp -> do
                writeFixture tmp
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                let appPath = tmp </> "sky-out" </> "app"
                (rc, stdout', _) <- runApp appPath tmp
                rc `shouldBe` ExitSuccess
                ("doubled=84" `infix'` stdout') `shouldBe` True
  where
    findSky :: IO FilePath
    findSky = do
        cwd <- getCurrentDirectory
        let candidate = cwd </> "sky-out" </> "sky"
        ok <- doesFileExist candidate
        if ok then return candidate
              else fail ("sky binary missing at " ++ candidate)

    runSky :: FilePath -> [String] -> FilePath -> IO (ExitCode, String, String)
    runSky sky args workDir = do
        let cp = (proc sky args) { cwd = Just workDir }
        readCreateProcessWithExitCode cp ""

    runApp :: FilePath -> FilePath -> IO (ExitCode, String, String)
    runApp appPath workDir = do
        let cp = (proc appPath []) { cwd = Just workDir }
        readCreateProcessWithExitCode cp ""

    infix' :: String -> String -> Bool
    infix' needle hay = go hay
      where
        nlen = length needle
        go [] = False
        go s@(_:rest)
            | take nlen s == needle = True
            | otherwise = go rest

    writeFixture :: FilePath -> IO ()
    writeFixture dir = do
        createDirectoryIfMissing True (dir </> "src" </> "Lib")
        writeFile (dir </> "sky.toml")
            (unlines
                [ "name = \"wave3narrow\""
                , "[bin]"
                , "main = \"src/Main.sky\""
                ])
        writeFile (dir </> "src" </> "Main.sky") mainFixture
        writeFile (dir </> "src" </> "Lib" </> "Pure.sky") libPureFixture

    findInfix :: String -> String -> Maybe String
    findInfix needle = go
      where
        nlen = length needle
        go [] = Nothing
        go s@(_:rest)
            | take nlen s == needle = Just (drop nlen s)
            | otherwise = go rest

    scopeFnBody :: String -> (String, String)
    scopeFnBody = go []
      where
        go acc [] = (reverse acc, [])
        go acc s@(_:rest)
            | take 6 s == "\nfunc " = (reverse acc, s)
            | otherwise = go (head s : acc) rest

    countSubstring :: String -> String -> Int
    countSubstring needle = go 0
      where
        nlen = length needle
        go n [] = n
        go n s@(_:rest)
            | take nlen s == needle = go (n+1) (drop nlen s)
            | otherwise = go n rest


-- ─── Fixtures ──────────────────────────────────────────────────────

-- Entry uses Task-typed callees (Time.now is Task Error Int).
mainFixture :: String
mainFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Sky.Core.String as String"
    , "import Sky.Core.Time as Time"
    , "import Std.Log exposing (println)"
    , "import Lib.Pure as Pure"
    , ""
    , "main ="
    , "    let"
    , "        n = Pure.doubleIt 42"
    , "        _ = println (\"doubled=\" ++ String.fromInt n)"
    , "        _ = Time.unixMillis ()"
    , "    in"
    , "    println \"done\""
    ]


-- Dep is totally pure — does not import Task surface.
libPureFixture :: String
libPureFixture = unlines
    [ "module Lib.Pure exposing (doubleIt)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , ""
    , "doubleIt : Int -> Int"
    , "doubleIt n ="
    , "    n + n"
    ]
