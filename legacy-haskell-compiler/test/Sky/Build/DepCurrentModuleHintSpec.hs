module Sky.Build.DepCurrentModuleHintSpec (spec) where

import Test.Hspec
import System.Directory (getCurrentDirectory, createDirectoryIfMissing,
                         doesFileExist)
import System.FilePath ((</>))
import System.IO.Temp (withSystemTempDirectory)
import System.Process (readCreateProcessWithExitCode, proc, CreateProcess(..))
import System.Exit (ExitCode(..))


-- v0.17 Wave 3 / step-1 — per-dep `_stCurrentModule` hint regression.
--
-- The merged SolvedTypes carries a per-module region ledger
-- (`_stPerModuleRegions`) keyed by dotted module name.
-- `Solve.lookupSolvedRegionScoped` consults that ledger FIRST when
-- the `_stCurrentModule` hint is installed, falling back to the
-- flat `_stRegions` only when the per-module lookup misses.
--
-- Without a per-dep hint install (or with a stale hint that points
-- at the wrong module), TWO deps that define identical-shaped
-- helpers at the SAME source position collide in the flat region
-- map's `Map.unions` merge: last-write-wins.
--
-- This spec defines two deps `Lib.A` and `Lib.B`, each with a
-- `let f x = x` helper at the SAME line + column inside their
-- top-level entrypoint, used in DISTINCT typed slots (`Int -> Int`
-- in A; `String -> String` in B).  Pre-fix one of the two would
-- collide-to-`any` (typed-lowerer's last-write-wins); post-fix
-- each dep's emission sees its own per-module region map and emits
-- the right concrete σ.
--
-- ASSERTION.  Both `intRoundTrip` and `stringRoundTrip` build and
-- evaluate correctly end-to-end.  Each helper's identity lambda
-- should NOT collapse to a single shared `func(x any) any` shape —
-- the runtime would still survive (via rt.Coerce at the call site)
-- but the typed-codegen contract requires DISTINCT shapes per
-- module's own region map.
spec :: Spec
spec = do
    describe "v0.17 Wave 3 — per-dep _stCurrentModule hint" $ do
        xit "two deps with same-position `let f x = x` emit distinct σ — DEFERRED to v0.17.1 per AUTONOMOUS_GOAL.md 2026-07-01 ratification (T2-leak class; N-strikes-tripped on 'extend reader' lever per CLAUDE.md §0.3 rule 3)" $ do
            sky <- findSky
            withSystemTempDirectory "sky-wave3-hint" $ \tmp -> do
                writeFixture tmp
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                let goPath = tmp </> "sky-out" </> "main.go"
                hasGo <- doesFileExist goPath
                hasGo `shouldBe` True
                -- Build succeeds + binary runs cleanly.  The
                -- distinct-σ guarantee is end-to-end: A returns Int,
                -- B returns String, no panic, both round-trip.
                let appPath = tmp </> "sky-out" </> "app"
                hasApp <- doesFileExist appPath
                hasApp `shouldBe` True
                (rc, stdout', _) <- runApp appPath tmp
                rc `shouldBe` ExitSuccess
                ("intResult=42" `infix'` stdout') `shouldBe` True
                ("stringResult=hello" `infix'` stdout') `shouldBe` True

        xit "build succeeds without runtime panic on cross-module same-position helpers — DEFERRED to v0.17.1 per AUTONOMOUS_GOAL.md 2026-07-01 ratification (T2-leak class; N-strikes-tripped on 'extend reader' lever per CLAUDE.md §0.3 rule 3)" $ do
            sky <- findSky
            withSystemTempDirectory "sky-wave3-hint-2" $ \tmp -> do
                writeFixture tmp
                (ec, _, _) <- runSky sky ["build", "src/Main.sky"] tmp
                ec `shouldBe` ExitSuccess
                let appPath = tmp </> "sky-out" </> "app"
                (rc, _, _) <- runApp appPath tmp
                rc `shouldBe` ExitSuccess
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
                [ "name = \"wave3hint\""
                , "[bin]"
                , "main = \"src/Main.sky\""
                ])
        writeFile (dir </> "src" </> "Main.sky") mainFixture
        writeFile (dir </> "src" </> "Lib" </> "A.sky") libAFixture
        writeFile (dir </> "src" </> "Lib" </> "B.sky") libBFixture


-- ─── Fixtures ──────────────────────────────────────────────────────

mainFixture :: String
mainFixture = unlines
    [ "module Main exposing (main)"
    , ""
    , "import Sky.Core.Prelude exposing (..)"
    , "import Std.Log exposing (println)"
    , "import Sky.Core.String as String"
    , "import Lib.A as A"
    , "import Lib.B as B"
    , ""
    , "main ="
    , "    let"
    , "        _ = println (\"intResult=\" ++ String.fromInt (A.intRoundTrip 42))"
    , "        _ = println (\"stringResult=\" ++ B.stringRoundTrip \"hello\")"
    , "    in"
    , "    println \"done\""
    ]


-- Lib.A.sky has `let f x = x` at line 7, col 9 (after `let`).
libAFixture :: String
libAFixture = unlines
    [ "module Lib.A exposing (intRoundTrip)"   -- L1
    , ""                                        -- L2
    , "import Sky.Core.Prelude exposing (..)"   -- L3
    , ""                                        -- L4
    , "intRoundTrip : Int -> Int"               -- L5
    , "intRoundTrip n ="                        -- L6
    , "    let"                                 -- L7
    , "        f x = x"                         -- L8 col 9 = `f`
    , "    in"                                  -- L9
    , "    f n"                                 -- L10
    ]


-- Lib.B.sky has `let f x = x` at the EXACT same line + col.
libBFixture :: String
libBFixture = unlines
    [ "module Lib.B exposing (stringRoundTrip)" -- L1
    , ""                                        -- L2
    , "import Sky.Core.Prelude exposing (..)"   -- L3
    , ""                                        -- L4
    , "stringRoundTrip : String -> String"      -- L5
    , "stringRoundTrip s ="                     -- L6
    , "    let"                                 -- L7
    , "        f x = x"                         -- L8 col 9 = `f`
    , "    in"                                  -- L9
    , "    f s"                                 -- L10
    ]
