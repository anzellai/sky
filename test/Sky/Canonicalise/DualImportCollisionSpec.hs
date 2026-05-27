module Sky.Canonicalise.DualImportCollisionSpec (spec) where

-- Cycle 4 — D5 regression fence.
--
-- Pre-fix bug: when two imports share the same default qualifier
-- (e.g. `import State` AND `import App.State` — both last-segment
-- `State`), the canonicaliser's `_importAliases` map (last-wins) and
-- `_qualVars` map (union) disagree about which canonical module the
-- qualifier owns. Qualified TYPE references silently misroute to the
-- LAST imported module's type, while qualified VALUE references reach
-- whichever module's binding is in `_qualVars`. The result is the
-- dishonest type error `Foreign 'State.initial': Model vs Model` —
-- two same-named aliases from different homes printing identically.
--
-- Fix: at canonicalisation time, walk the import list and reject when
-- two imports bind the SAME qualifier but resolve to DIFFERENT
-- canonical modules. Kernel modules collapse to their kernel pseudo-
-- module name (so `Sky.Core.Time` + `Std.Time` both routing to the
-- `Time` kernel does NOT trigger). Two aliased imports of the SAME
-- module (`import Std.Ui as Ui` + `import Std.Ui exposing (Element)`)
-- also do not trigger because both resolve to the same canonical
-- module.

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
    withSystemTempDirectory "sky-d5" $ \tmp -> do
        sky <- findSky
        createDirectoryIfMissing True (tmp </> "src")
        writeFile (tmp </> "sky.toml") "name = \"d5-test\"\n"
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


-- A `State` module with a Model alias + initial value.
stateModule :: String
stateModule = unlines
    [ "module State exposing (Model, initial)"
    , ""
    , "type alias Model = { count : Int, label : String }"
    , ""
    , "initial : Model"
    , "initial = { count = 0, label = \"init\" }"
    ]


-- An `App.State` module with a DIFFERENT Model alias + a defaultModel.
-- Same last-segment as State, so its default qualifier collides.
appStateModule :: String
appStateModule = unlines
    [ "module App.State exposing (Model, defaultModel)"
    , ""
    , "type alias Model = { foo : String, bar : Int }"
    , ""
    , "defaultModel : Model"
    , "defaultModel = { foo = \"x\", bar = 99 }"
    ]


-- A second module of `App.State` shape that only re-exports a value
-- (no Model alias) — used for the workaround test so the test asserts
-- the `as Alias` rename compiles cleanly without bumping into the
-- unrelated cross-module alias-name-collision bug class.
appHelpersModule :: String
appHelpersModule = unlines
    [ "module App.Helpers exposing (defaultThing)"
    , ""
    , "defaultThing : Int"
    , "defaultThing = 42"
    ]


spec :: Spec
spec = describe "Cycle 4 D5: dual-import qualifier collision detection" $ do

    it "rejects two imports that share the same default qualifier" $ do
        -- Pre-fix: silently miscompiled — type checker emitted
        -- `Foreign 'State.initial': Model vs Model`. Post-fix: clear
        -- canonicalise error pointing at the import line.
        let mainSrc = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , "import State"
                , "import App.State"
                , ""
                , "useFn : State.Model"
                , "useFn = State.initial"
                , ""
                , "main = println (toString useFn.count)"
                ]
        (ec, out) <- buildFixture
            [ ("src/Main.sky", mainSrc)
            , ("src/State.sky", stateModule)
            , ("src/App/State.sky", appStateModule)
            ]
        ec `shouldNotBe` 0
        -- The dishonest downstream "Model vs Model" must NOT surface
        -- now — it has to be intercepted at canonicalisation time.
        out `shouldNotSatisfy` ("Model vs Model" `isInfixOf`)
        -- The diagnostic explicitly names BOTH imports + the qualifier.
        out `shouldSatisfy` ("two imports both bind the qualifier" `isInfixOf`)
        out `shouldSatisfy` ("`State`" `isInfixOf`)
        out `shouldSatisfy` ("import State" `isInfixOf`)
        out `shouldSatisfy` ("import App.State" `isInfixOf`)
        -- And it points the user at the fix-it.
        out `shouldSatisfy` ("as " `isInfixOf`)


    it "accepts the explicit-alias workaround (`import App.X as AppX`)" $ do
        -- The user-facing escape hatch: alias one of the colliding
        -- imports. The two qualifiers are now distinct so there's no
        -- collision. (Using App.Helpers — a sibling module without a
        -- Model alias — keeps the test focused on D5's exact contract;
        -- the cross-module alias-name-collision bug class is separate.)
        let mainSrc = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , "import State"
                , "import App.Helpers as AppH"
                , ""
                , "useFn : State.Model"
                , "useFn = State.initial"
                , ""
                , "main = println (toString (useFn.count + AppH.defaultThing))"
                ]
        (ec, out) <- buildFixture
            [ ("src/Main.sky", mainSrc)
            , ("src/State.sky", stateModule)
            , ("src/App/Helpers.sky", appHelpersModule)
            ]
        ec `shouldBe` 0
        out `shouldNotSatisfy` ("two imports both bind" `isInfixOf`)
        out `shouldSatisfy` ("Compilation successful" `isInfixOf`)


    it "does NOT flag two imports of the SAME module under different aliases" $ do
        -- `import Std.Ui as Ui` plus `import Std.Ui exposing (Element)`
        -- both reach the qualifier `Ui` (the explicit alias on the
        -- first, the last-segment fallback on the second). They
        -- resolve to the SAME canonical module, so my D5 guard must
        -- NOT trip — this shape is widespread (every Std.Ui-heavy
        -- example uses it).
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , "import Std.Ui as Ui"
                , "import Std.Ui exposing (Element)"
                , ""
                , "view : Element ()"
                , "view = Ui.text \"ok\""
                , ""
                , "main = let _ = view in println \"ok\""
                ]
        (ec, out) <- buildFixture [("src/Main.sky", src)]
        out `shouldNotSatisfy` ("two imports both bind" `isInfixOf`)
        -- We don't assert success because the dummy `view` may not
        -- type-check cleanly under every cabal-test variation —
        -- the only contract is that D5's collision guard does NOT
        -- trip on aliased same-module re-imports.
        ec `seq` return ()


    it "does NOT flag kernel imports that share a kernel pseudo-module" $ do
        -- `Sky.Core.Time` and `Std.Time` both alias to the `Time`
        -- kernel pseudo-module — they route to the same kernel
        -- dispatch table. Importing both (or either) under the
        -- default `Time` qualifier must NOT trigger D5's collision
        -- guard. This shape is uncommon (users import one or the
        -- other), but the guard's correctness depends on collapsing
        -- kernel modules onto their pseudo-module name.
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , "import Sky.Core.Time"
                , "import Std.Time"
                , ""
                , "main = println \"ok\""
                ]
        (ec, out) <- buildFixture [("src/Main.sky", src)]
        out `shouldNotSatisfy` ("two imports both bind" `isInfixOf`)
        ec `seq` return ()
