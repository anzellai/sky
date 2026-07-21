module Sky.Type.UfCycleGuardSpec (spec) where

-- Regression fence for the v0.13.0 dep-fixpoint OOM fix: a missed
-- occurs check in `Sky.Type.Unify.actuallyUnify`'s FlexVar↔Structure
-- merge could splice a self-referential cycle into the union-find
-- graph. The downstream `Sky.Type.Solve.variableToType` walk then
-- recursed forever through the cyclic `App1` args, allocating GB
-- of heap before mem-guard / RTS killed the host.
--
-- Symptom triggered by importing `Std.Ui.Events` (or any sky-source
-- stdlib module that re-exports a polymorphic helper across the
-- recursive `Element msg` / `Attribute msg` ADT). Under +RTS -M256M
-- the pre-fix compiler dies with "Heap exhausted". Post-fix it
-- finishes in a few hundred MB with the legitimate type error
-- (or success when the source is well-typed).
--
-- Fix landed in two layers:
--   1. `Unify.actuallyUnify` now occurs-checks every FlexVar
--      ↔ Structure / FlexVar ↔ Alias merge — prevents new cycles
--      from being introduced.
--   2. `Solve.variableToType` carries a path-tracking `seen` set
--      so any pre-existing cycle reads back as a `TVar "_cycle"`
--      sentinel instead of looping forever.
--
-- Tier 1 (task #491): the pre-Tier-1 spec spawned `sky build … +RTS
-- -M256M -RTS` to assert the compiler completed without OOM. In-
-- process we can't inject per-call RTS heap flags, so the migrated
-- assertion runs the same source through compileInProcess and
-- asserts the compile terminates cleanly (exit 0 or a legitimate
-- type error). A reintroduced UF cycle would now blow out the test
-- runner's heap, surfaced as a hard crash with the host's `+RTS
-- -M…` setting — still observable, just at the suite level rather
-- than the subprocess level.

import Test.Hspec

import Sky.Build.Helpers.InProcessCompile (CompileResult(..), compileInProcess)


spec :: Spec
spec = describe "UF cycle guard" $ do

    it "trivial Std.Ui.Events importer terminates without UF cycle" $ do
        -- Minimal reproducer extracted from mini-notion. Pre-fix
        -- this allocates >3 GB during the dep-fixpoint round-1
        -- solve of Std.Ui.Events; post-fix it completes promptly.
        -- We accept EITHER success (well-typed) OR a clean compile
        -- error (some unrelated typed-codegen issue) — the test's
        -- contract is "compiler terminates", not "source is correct".
        let src = unlines
                [ "module Main exposing (main)"
                , ""
                , "import Std.Ui as Ui"
                , "import Std.Ui exposing (Element)"
                , "import Std.Ui.Events as Events"
                , "import Std.Log exposing (println)"
                , ""
                , ""
                , "type Msg = Click"
                , ""
                , "view : Element Msg"
                , "view = Ui.el [ Events.onClick Click ] (Ui.text \"hi\")"
                , ""
                , "main = println \"compiled\""
                ]
        -- The call returns CompileOk or CompileErr — both are
        -- acceptable. The implicit contract is that this expression
        -- evaluates AT ALL (a reintroduced cycle would crash the
        -- test runner with "Heap exhausted").
        result <- compileInProcess src
        case result of
            CompileOk _ -> return ()
            CompileErr _ -> return ()
