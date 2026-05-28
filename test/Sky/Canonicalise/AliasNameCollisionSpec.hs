module Sky.Canonicalise.AliasNameCollisionSpec (spec) where

-- Cycle 4 — task #350 regression fence (follow-up to D5 / PR #105).
--
-- Pre-fix bug: when TWO dependency modules each expose a type alias
-- with the same NAME (e.g. `App.State.Model` and `Lib.State.Model`),
-- the dep-alias map flattens to a single `String → Can.Alias` keyed on
-- just the alias name. `Map.unions` is left-biased, so whichever dep
-- comes first wins — the other dep's `Model` collapses into the same
-- entry. At alias-expansion time, BOTH `Can.TType "App.State" "Model"`
-- and `Can.TType "Lib.State" "Model"` look up the SAME body and the
-- HM solver later prints the dishonest "Model vs Model" type error.
--
-- D5 closed the qualifier-collision class (two imports sharing the
-- same default qualifier — `import State` + `import App.State` both
-- becoming `State.X`). This spec pins the DEEPER class that survived:
-- even with disambiguating `as Alias` clauses, the alias-name
-- collision still tripped because `collectDepAliases` discarded the
-- home-module info before forming its map key.
--
-- Fix (Approach B from #350's brief): key the dep alias map by
-- `(ModuleName.Canonical, String)` instead of `String` alone, and
-- thread the home through alias-lookup in `expandTypeAliases`. Both
-- aliases now coexist; each lookup hits its own home's body.

import Test.Hspec
import qualified System.Exit as Exit
import System.Directory (getCurrentDirectory, doesFileExist,
                         createDirectoryIfMissing)
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


-- | Build a fixture with multiple source files keyed by relative path
-- (always under `src/`) plus an empty sky.toml. Returns the build's
-- exit code + combined stdout/stderr.
buildFixture :: [(FilePath, String)] -> IO (Int, String)
buildFixture files =
    withSystemTempDirectory "sky-350" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "sky.toml") "name = \"alias-name-test\"\n"
        mapM_ (\(p, c) -> do
            let dst = tmp </> p
                dir = reverse (dropWhile (/= '/') (reverse dst))
            createDirectoryIfMissing True dir
            writeFile dst c) files
        let cmd = "cd " ++ tmp ++ " && " ++ sky ++ " build src/Main.sky 2>&1"
        (ec, sout, serr) <- readCreateProcessWithExitCode (shell cmd) ""
        let combined = sout ++ serr
            ecInt = case ec of
                Exit.ExitSuccess -> 0
                Exit.ExitFailure n -> n
        return (ecInt, combined)


-- An `App.State` module with its own Model alias + initial helper.
appStateModule :: String
appStateModule = unlines
    [ "module App.State exposing (Model, initial)"
    , ""
    , "type alias Model = { count : Int }"
    , ""
    , "initial : Model"
    , "initial = { count = 0 }"
    ]


-- A `Lib.State` module with a DIFFERENT Model alias + a settings
-- helper. Last-segment matches App.State so it would have tripped D5,
-- BUT users disambiguate with explicit `as` aliases — D5 lets it
-- through. The deeper bug then surfaces inside the canonicaliser's
-- depAliasMap, where the two `Model` entries collapse.
libStateModule :: String
libStateModule = unlines
    [ "module Lib.State exposing (Model, settings)"
    , ""
    , "type alias Model = { name : String }"
    , ""
    , "settings : Model"
    , "settings = { name = \"lib\" }"
    ]


spec :: Spec
spec =
    describe "Cycle 4 #350: cross-module type-alias name collision" $ do

        it "lets two deps each expose `Model` under disambiguating `as` aliases" $ do
            -- Pre-fix: build emitted `Foreign 'Lib.State.settings':
            -- Model vs Model`. Post-fix: clean compile. The two
            -- imports use explicit `as AppS` / `as LibS` so D5 does
            -- not trip; the bug under test is the dep-alias map
            -- collapsing on NAME.
            let mainSrc = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , "import App.State as AppS"
                    , "import Lib.State as LibS"
                    , ""
                    , "useApp : AppS.Model"
                    , "useApp = AppS.initial"
                    , ""
                    , "useLib : LibS.Model"
                    , "useLib = LibS.settings"
                    , ""
                    , "main = println (toString useApp.count ++ \"/\" ++ useLib.name)"
                    ]
            (ec, out) <- buildFixture
                [ ("src/Main.sky",       mainSrc)
                , ("src/App/State.sky",  appStateModule)
                , ("src/Lib/State.sky",  libStateModule)
                ]
            -- The dishonest "Model vs Model" must not surface.
            out `shouldNotSatisfy` ("Model vs Model" `isInfixOf`)
            ec `shouldBe` 0
            out `shouldSatisfy` ("Compilation successful" `isInfixOf`)


        it "preserves alias-body identity per home module" $ do
            -- Stronger: the two Model bodies have INCOMPATIBLE shapes
            -- (App.State.Model has `count : Int`; Lib.State.Model has
            -- `name : String`). If the dep-alias map still collapsed
            -- (whichever order Map.unions visited), one site would see
            -- the wrong body's fields and the program would either
            -- fail to type-check OR mis-emit. Picking different fields
            -- on each side ensures no accidental name overlap masks
            -- the bug.
            let mainSrc = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , "import App.State as AppS"
                    , "import Lib.State as LibS"
                    , ""
                    , "-- field access on each side proves the body's"
                    , "-- not been swapped for the OTHER home's alias"
                    , "appCount : Int"
                    , "appCount = AppS.initial.count"
                    , ""
                    , "libName : String"
                    , "libName = LibS.settings.name"
                    , ""
                    , "main = println (toString appCount ++ \"|\" ++ libName)"
                    ]
            (ec, out) <- buildFixture
                [ ("src/Main.sky",       mainSrc)
                , ("src/App/State.sky",  appStateModule)
                , ("src/Lib/State.sky",  libStateModule)
                ]
            ec `shouldBe` 0
            out `shouldSatisfy` ("Compilation successful" `isInfixOf`)
