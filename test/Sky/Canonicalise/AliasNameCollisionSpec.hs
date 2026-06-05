module Sky.Canonicalise.AliasNameCollisionSpec (spec) where

-- Cycle 4 — task #350 / #361 regression fence (follow-up to D5 / PR #105).
--
-- This spec is the v2 follow-up to PR #111 (commit 5551053, reverted at
-- 0f453bd). It pins TWO bug classes simultaneously:
--
-- 1. #350 — cross-module alias-NAME collision (the original Cycle 4
--    follow-up gap). When TWO dependency modules each expose a type
--    alias with the same NAME (e.g. `App.State.Model` and
--    `Lib.State.Model`), the dep-alias map flattens to a single
--    `String → Can.Alias` keyed on just the alias name. `Map.unions`
--    is left-biased, so whichever dep comes first wins — the other
--    dep's `Model` collapses into the same entry. At alias-expansion
--    time, BOTH `Can.TType "App.State" "Model"` and
--    `Can.TType "Lib.State" "Model"` look up the SAME body and the
--    HM solver later prints the dishonest "Model vs Model" type
--    error.
--
-- 2. #361 — the Dashboard-regression class (introduced by PR #111's
--    pure (home, name) rekeying and the reason it was reverted).
--    When a module references a qualified type WITHOUT a
--    corresponding `import Mod as Q` (typically because the type
--    transits through a re-exporting intermediate module), the
--    canonicaliser's qualifier resolver falls back to
--    `Canonical "<qualifier>"` (literal short segment). Under pure
--    (home, name) keying that lookup misses and the alias body never
--    unfolds — downstream the type checker reports "<Alias> vs
--    { field : a | ... }" against any `target.field` access on a
--    value typed by the alias. The skydeploy/control-plane case was
--    `repo : Github.RepoInfo` used in `View/Dashboard.sky` where
--    Dashboard does NOT directly `import Github.Api as Github`; the
--    qualifier `Github` resolved to `Canonical "Github"` but the
--    alias was registered as `(Canonical "Github.Api", "RepoInfo")`.
--
-- Fix shape (v2):
--   * Primary lookup uses `(home, name)` — closes #350 by letting
--     two distinct same-named aliases coexist.
--   * Fallback by bare name when the primary misses AND there is a
--     SINGLE unique body across all (home, name) entries with that
--     name — closes #361 (the Dashboard regression). The resulting
--     `Can.TAlias` carries the RESOLVED home (not the typo'd one),
--     so downstream unification stays consistent.
--   * Multiple distinct bodies under the same name with NO matching
--     home is a true ambiguity — but in practice this is gated by
--     D5 (PR #105) at the qualifier level so we don't reach this
--     branch with conflicting bodies.
--
-- Tier 1 (task #491): runs the multi-module compile in-process via
-- Sky.Build.Helpers.InProcessCompile.compileInProcessMulti. ZERO
-- subprocess, ZERO `go build`, ZERO GOCACHE writes.

import Test.Hspec
import Data.List (isInfixOf)

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcessMulti)


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


-- A library module that exposes a record alias used in a typed sig.
-- Mirrors `packages/sky-github/src/Github/Api.sky` from the
-- skydeploy/control-plane regression.
githubApiModule :: String
githubApiModule = unlines
    [ "module Github.Api exposing (RepoInfo, makeRepo)"
    , ""
    , "type alias RepoInfo = { fullName : String }"
    , ""
    , "makeRepo : String -> RepoInfo"
    , "makeRepo name = { fullName = name }"
    ]


-- An intermediate module that imports Github.Api directly and
-- re-exposes both the alias and a constructor that USES it.
-- View modules (Dashboard) import State, not Github.Api.
stateReexportModule :: String
stateReexportModule = unlines
    [ "module State exposing (..)"
    , ""
    , "import Github.Api as Github"
    , ""
    , "type alias Model = { repo : Github.RepoInfo }"
    , ""
    , "emptyModel : Model"
    , "emptyModel = { repo = { fullName = \"\" } }"
    ]


spec :: Spec
spec =
    describe "Cycle 4 #350 / #361: cross-module type-alias name collision (v2)" $ do

        it "lets two deps each expose `Model` under disambiguating `as` aliases (#350)" $ do
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
            result <- compileInProcessMulti
                [ ("src/Main.sky",       mainSrc)
                , ("src/App/State.sky",  appStateModule)
                , ("src/Lib/State.sky",  libStateModule)
                ]
            case result of
                CompileErr e -> do
                    -- The dishonest "Model vs Model" must not surface.
                    e `shouldNotSatisfy` ("Model vs Model" `isInfixOf`)
                    expectationFailure ("compile failed: " ++ e)
                CompileOk _ -> return ()


        it "preserves alias-body identity per home module (#350)" $ do
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
            result <- compileInProcessMulti
                [ ("src/Main.sky",       mainSrc)
                , ("src/App/State.sky",  appStateModule)
                , ("src/Lib/State.sky",  libStateModule)
                ]
            case result of
                CompileErr e -> expectationFailure ("compile failed: " ++ e)
                CompileOk _ -> return ()


        it "qualified type referenced via a re-exporting transit module (#361)" $ do
            -- The skydeploy/control-plane regression mirrored. The
            -- consumer module (Main here, View/Dashboard.sky in the
            -- real bug) writes `Github.RepoInfo` qualified but does
            -- NOT directly `import Github.Api as Github` — the alias
            -- transits through State (`exposing (..)`).
            --
            -- Under pure (home, name) keying the lookup uses the
            -- typo'd `Canonical "Github"` and misses. Result: the
            -- alias body never unfolds and a downstream `repo.fullName`
            -- access surfaces as
            --   `Variable 'repo' type mismatch: RepoInfo vs { fullName : a | ... }`
            --
            -- The v2 fix's bare-name fallback finds the unique
            -- `RepoInfo` body and wraps the type with the correct
            -- resolved home (`Canonical "Github.Api"`).
            let mainSrc = unlines
                    [ "module Main exposing (main)"
                    , ""
                    , "import Sky.Core.Prelude exposing (..)"
                    , "import Std.Log exposing (println)"
                    , "import State exposing (..)"
                    , ""
                    , "-- `Github` is NOT imported here. The qualifier"
                    , "-- resolves only because State re-exposes everything"
                    , "-- and the bare-name fallback finds the unique body."
                    , "showRepo : Github.RepoInfo -> String"
                    , "showRepo repo = repo.fullName"
                    , ""
                    , "main = println (showRepo emptyModel.repo)"
                    ]
            result <- compileInProcessMulti
                [ ("src/Main.sky",          mainSrc)
                , ("src/State.sky",         stateReexportModule)
                , ("src/Github/Api.sky",    githubApiModule)
                ]
            case result of
                CompileErr e -> do
                    e `shouldNotSatisfy` ("RepoInfo vs {" `isInfixOf`)
                    e `shouldNotSatisfy` ("type mismatch" `isInfixOf`)
                    expectationFailure ("compile failed: " ++ e)
                CompileOk _ -> return ()
