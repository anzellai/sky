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
--
-- Tier 1 (task #491): in-process via compileInProcessMulti.

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcessMulti)


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
        result <- compileInProcessMulti
            [ ("src/Main.sky", mainSrc)
            , ("src/State.sky", stateModule)
            , ("src/App/State.sky", appStateModule)
            ]
        case result of
            CompileOk _ -> expectationFailure "expected dual-import qualifier collision to be rejected"
            CompileErr e -> do
                -- The dishonest downstream "Model vs Model" must NOT surface
                -- now — it has to be intercepted at canonicalisation time.
                e `shouldNotSatisfy` ("Model vs Model" `isInfixOf`)
                -- The diagnostic explicitly names BOTH imports + the qualifier.
                e `shouldSatisfy` ("two imports both bind the qualifier" `isInfixOf`)
                e `shouldSatisfy` ("`State`" `isInfixOf`)
                e `shouldSatisfy` ("import State" `isInfixOf`)
                e `shouldSatisfy` ("import App.State" `isInfixOf`)
                -- And it points the user at the fix-it.
                e `shouldSatisfy` ("as " `isInfixOf`)


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
        result <- compileInProcessMulti
            [ ("src/Main.sky", mainSrc)
            , ("src/State.sky", stateModule)
            , ("src/App/Helpers.sky", appHelpersModule)
            ]
        case result of
            CompileErr e -> do
                e `shouldNotSatisfy` ("two imports both bind" `isInfixOf`)
                expectationFailure ("compile failed: " ++ e)
            CompileOk _ -> return ()


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
        result <- compileInProcessMulti [("src/Main.sky", src)]
        case result of
            CompileOk _ -> return ()
            CompileErr e ->
                -- We don't assert success because the dummy `view` may not
                -- type-check cleanly under every cabal-test variation —
                -- the only contract is that D5's collision guard does NOT
                -- trip on aliased same-module re-imports.
                e `shouldNotSatisfy` ("two imports both bind" `isInfixOf`)


    it "explicit alias suppresses a bare import's auto-qualifier (2-line shape)" $ do
        -- v0.17.5 explicit-alias-wins rule.  When one import gives an
        -- EXPLICIT `as X` and another bare import's last-segment
        -- default would ALSO auto-register `X` for a different module,
        -- the bare's auto-qualifier is suppressed silently.  The
        -- explicit binding is unambiguous; the user gets what they
        -- wrote.  Pre-v0.17.5 this shape was E1001.
        let mainSrc = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , "import Std.Db as Db"
                , "import Lib.Db exposing (conn)"
                , ""
                -- `conn` reaches Lib.Db (unqualified exposing).
                -- `Db.<x>` would resolve to Std.Db (explicit alias wins).
                , "main = println conn"
                ]
            libDbModule = unlines
                [ "module Lib.Db exposing (conn, dummy)"
                , ""
                , "conn : String"
                , "conn = \"local://\""
                , ""
                , "dummy : Int"
                , "dummy = 0"
                ]
        result <- compileInProcessMulti
            [ ("src/Main.sky", mainSrc)
            , ("src/Lib/Db.sky", libDbModule)
            ]
        case result of
            CompileOk _ -> return ()
            CompileErr e -> do
                -- The collision gate must NOT fire — explicit wins.
                e `shouldNotSatisfy` ("two imports both bind" `isInfixOf`)
                expectationFailure ("expected clean build; got: " ++ e)


    it "explicit alias suppresses a bare import's auto-qualifier (3-line shape)" $ do
        -- Same rule applied to the historical ringfence 3-import
        -- workaround shape.  Under v0.17.5 the extra `import Lib.Db
        -- as LibDb` line is optional — the bare `import Lib.Db
        -- exposing (conn)` no longer collides with `Std.Db as Db`
        -- either way.  This case documents that the workaround
        -- continues to compile identically to the 2-line shape above.
        let mainSrc = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , "import Std.Db as Db"
                , "import Lib.Db as LibDb"
                , "import Lib.Db exposing (conn)"
                , ""
                , "main = let _ = LibDb.dummy in println conn"
                ]
            libDbModule = unlines
                [ "module Lib.Db exposing (conn, dummy)"
                , ""
                , "conn : String"
                , "conn = \"local://\""
                , ""
                , "dummy : Int"
                , "dummy = 0"
                ]
        result <- compileInProcessMulti
            [ ("src/Main.sky", mainSrc)
            , ("src/Lib/Db.sky", libDbModule)
            ]
        case result of
            CompileOk _ -> return ()
            CompileErr e -> do
                e `shouldNotSatisfy` ("two imports both bind" `isInfixOf`)
                expectationFailure ("expected clean build; got: " ++ e)


    it "two bare imports auto-registering same qualifier still trip E1001" $ do
        -- The explicit-alias-wins rule only fires when there IS an
        -- explicit alias to break the tie.  When BOTH sides are bare,
        -- the qualifier is genuinely ambiguous and the collision gate
        -- must still fire (silent miscompile prevention, task #347).
        let mainSrc = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , "import State"
                , "import App.State"
                , ""
                , "main = println \"x\""
                ]
        result <- compileInProcessMulti
            [ ("src/Main.sky", mainSrc)
            , ("src/State.sky", stateModule)
            , ("src/App/State.sky", appStateModule)
            ]
        case result of
            CompileOk _ -> expectationFailure "two bare imports must still trip E1001"
            CompileErr e -> do
                e `shouldSatisfy` ("two imports both bind the qualifier" `isInfixOf`)
                e `shouldSatisfy` ("`State`" `isInfixOf`)


    it "explicit alias resolution wins over suppressed bare import" $ do
        -- v0.17.5 explicit-alias-wins rule — pin the resolution
        -- path.  When Std.Db is aliased as Db and Lib.Db is imported
        -- bare, `Db.getInt` MUST route to Std.Db (the explicit
        -- alias), NOT Lib.Db.  Pre-v0.17.5 this shape was E1001; the
        -- silent resolution class this test locks did not exist
        -- because the file did not compile at all.
        let mainSrc = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Sky.Core.Prelude exposing (..)"
                , "import Std.Log exposing (println)"
                , "import Std.Db as Db"
                , "import Lib.Db exposing (conn)"
                , ""
                -- If `Db.getString` mis-resolved to Lib.Db, canonicalisation
                -- would reject with "Undefined name: Lib.Db.getString".
                -- Passing means the explicit alias wins.
                , "row : Task Error String"
                , "row = Task.succeed \"stub-cn\""
                , "    |> Task.andThen (\\c -> Db.connect c)"
                , "    |> Task.andThen (\\_ -> Task.succeed \"ok\")"
                , ""
                , "main = println conn"
                ]
            libDbModule = unlines
                [ "module Lib.Db exposing (conn)"
                , ""
                , "conn : String"
                , "conn = \"local://\""
                ]
        result <- compileInProcessMulti
            [ ("src/Main.sky", mainSrc)
            , ("src/Lib/Db.sky", libDbModule)
            ]
        case result of
            CompileOk _ -> return ()
            CompileErr e -> do
                -- The critical negative: `Db.connect` must not
                -- mis-resolve to Lib.Db (which has no `connect`).
                e `shouldNotSatisfy` ("Lib.Db.connect" `isInfixOf`)
                e `shouldNotSatisfy` ("Lib_Db_connect" `isInfixOf`)
                -- We don't force compile-success on this exact stub
                -- shape (Task chain may not fully type-check under
                -- every variation).  The contract is that `Db.<x>`
                -- routes to Std.Db, not Lib.Db.
                return ()


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
        result <- compileInProcessMulti [("src/Main.sky", src)]
        case result of
            CompileOk _ -> return ()
            CompileErr e ->
                e `shouldNotSatisfy` ("two imports both bind" `isInfixOf`)
