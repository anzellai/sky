{-# LANGUAGE OverloadedStrings #-}

-- | Sky.Build.SealedIfaceFlipAllowListSpec — gate for v0.17 P3.4d
-- per-ADT opt-in allowlist for sealed-iface ADT emission.
--
-- The allowlist ('sealedIfaceFlipAllowList' in Compile.hs) is the
-- production switch that turns sealed-iface emission on for a
-- specific qualified ADT name.  Empty under P3.3 default — every
-- ADT keeps the legacy @rt.SkyADT@ shape.  Each populated entry
-- moves the gate's 4th arm from dead code to live, replacing the
-- ADT's legacy emission with the @emitSealedIfaceUnion@ shape.
--
-- This spec locks five invariants:
--
-- 1. The allowlist is currently EMPTY (P3.4d scaffolding ship —
--    byte-identity contract on every example's main.go).
-- 2. 'shouldEmitSealedIface' returns False for the same hand-built
--    fixtures the carve-out spec covers, with the allowlist empty.
-- 3. Carve-out precedence: an entry that's BOTH in 'rtBuilderShadowList'
--    AND would be in the allowlist still returns False.  Shadow list
--    is checked before the allowlist (Compile.hs lines 922-923).
-- 4. Other guards (Enum / Unbox / polymorphic) still short-circuit
--    BEFORE the allowlist check.
-- 5. (Not asserted at the production-set level — the production set
--    is empty.  The wiring is verified end-to-end via the
--    'SealedIfaceEmissionSpec' which already covers
--    @emitSealedIfaceUnion@'s output for hand-built ctors.)
--
-- The allowlist + the gate's True arm exist in production code
-- under @src/Sky/Build/Compile.hs@.  Populating an entry triggers
-- the live True arm at the production callers; populating the
-- WRONG entry can blast across every project with that bare entry-
-- module name (e.g. @"Main.Msg"@ matches every example + every test
-- fixture's entry module — see Compile.hs comment block on the
-- definition).
module Sky.Build.SealedIfaceFlipAllowListSpec where

import qualified Data.Set as Set
import           Test.Hspec

import qualified Sky.AST.Canonical    as Can
import           Sky.Build.Compile
                     ( rtBuilderShadowList
                     , sealedIfaceFlipAllowList
                     , shouldEmitSealedIface
                     )
import qualified Sky.Sky.ModuleName   as ModuleName


spec :: Spec
spec = do
    let modMain    = ModuleName.Canonical "Main"
    let modColor   = ModuleName.Canonical "Mod.Color"
    let modSqlVal  = ModuleName.Canonical "Std.Db"

    describe "sealedIfaceFlipAllowList — 6 entries post iter 72" $ do

        it "contains Sky.Test.TestResult (first ADT flip)" $
            "Sky.Test.TestResult" `Set.member` sealedIfaceFlipAllowList
                `shouldBe` True

        it "contains Sky.Core.Jwt.Algorithm (second ADT flip)" $
            "Sky.Core.Jwt.Algorithm" `Set.member` sealedIfaceFlipAllowList
                `shouldBe` True

        it "contains Std.Ui.Animation.Iterations (third ADT flip)" $
            "Std.Ui.Animation.Iterations" `Set.member` sealedIfaceFlipAllowList
                `shouldBe` True

        it "contains Std.Ui.Grid.Track (sixth flip — iter 72)" $
            -- v0.17 iter 72 — 9 variants incl recursive
            -- @Minmax Track Track@ + @Repeat Int Track@.  Strongest
            -- test of iter 70 cross-ADT fix: Track is the ADT that
            -- originally surfaced the bug in iter 68; flipping it
            -- post-iter-70 proves the dep-module region scoping
            -- correctly handles self-referencing iface fields.
            "Std.Ui.Grid.Track" `Set.member` sealedIfaceFlipAllowList
                `shouldBe` True

        it "contains Std.Ui.Transition.Step (fifth flip — iter 71)" $
            -- v0.17 iter 71 — 4 1-arg variants including
            -- @StepEasing Easing@ carrying the iter-70-flipped
            -- Easing sealed iface.  Proves iface-in-variant-struct
            -- round-trips correctly post-iter-70.
            "Std.Ui.Transition.Step" `Set.member` sealedIfaceFlipAllowList
                `shouldBe` True

        it "contains Std.Ui.Transition.Easing (fourth ADT flip — iter 70)" $
            -- v0.17 iter 68 attempted Easing first but the flip
            -- exposed a cross-ADT name-prefix pollution bug.
            -- v0.17 iter 70 root-caused it: 'LC.withCurrentDepModule'
            -- was defined but never installed, so dep-module case-of
            -- regions fell into the flat '_stRegions' map with
            -- last-write-wins collisions across ADTs.  Installing
            -- the missing wire at 'depBodyCtx' (Compile.hs:~5604)
            -- closed the class; iter 70 re-attempted Easing and it
            -- ships clean.
            "Std.Ui.Transition.Easing" `Set.member` sealedIfaceFlipAllowList
                `shouldBe` True

        it "does NOT contain Std.Ui.Animation.FillMode (removed iter 68 — Can.Enum NOP)" $
            "Std.Ui.Animation.FillMode" `Set.member` sealedIfaceFlipAllowList
                `shouldBe` False

        it "Set.size is 6 (catches accidental population without spec update)" $
            Set.size sealedIfaceFlipAllowList `shouldBe` 6

        it "Sky.Test.TestResult triggers sealed-iface gate (Can.Normal)" $
            shouldEmitSealedIface
                (ModuleName.Canonical "Sky.Test")
                "TestResult" [] Can.Normal
                `shouldBe` True

        it "Sky.Core.Jwt.Algorithm triggers sealed-iface gate (Can.Normal)" $
            shouldEmitSealedIface
                (ModuleName.Canonical "Sky.Core.Jwt")
                "Algorithm" [] Can.Normal
                `shouldBe` True

        it "Std.Ui.Animation.Iterations triggers sealed-iface gate (Can.Normal)" $
            shouldEmitSealedIface
                (ModuleName.Canonical "Std.Ui.Animation")
                "Iterations" [] Can.Normal
                `shouldBe` True

        it "Std.Ui.Transition.Easing triggers sealed-iface gate (Can.Normal)" $
            shouldEmitSealedIface
                (ModuleName.Canonical "Std.Ui.Transition")
                "Easing" [] Can.Normal
                `shouldBe` True

        it "Std.Ui.Transition.Step triggers sealed-iface gate (Can.Normal)" $
            shouldEmitSealedIface
                (ModuleName.Canonical "Std.Ui.Transition")
                "Step" [] Can.Normal
                `shouldBe` True

        it "Std.Ui.Grid.Track triggers sealed-iface gate (Can.Normal)" $
            shouldEmitSealedIface
                (ModuleName.Canonical "Std.Ui.Grid")
                "Track" [] Can.Normal
                `shouldBe` True

        it "iter-67/68 lesson: Can.Enum input rejects regardless of allowlist" $
            -- Even WITH the qualified name in the allowlist, an ADT
            -- canonicalised as Can.Enum is still rejected by guard #1
            -- (line 919) — the allowlist NEVER runs.  Locking this
            -- down so a future "but X is in the allowlist!" claim
            -- gets explicitly refuted by the gate behaviour.
            shouldEmitSealedIface
                (ModuleName.Canonical "Std.Ui.Animation")
                "Iterations" [] Can.Enum
                `shouldBe` False

    describe "Sky.Build.shouldEmitSealedIface — gate behaviour" $ do

        it "returns False for Main.Msg (not in allowlist)" $
            -- This input would, if we populated "Main.Msg", catch
            -- ~14 examples + ~6 test fixtures via the bare entry-
            -- module name collision documented in Compile.hs.  With
            -- the empty allowlist it correctly stays False.
            shouldEmitSealedIface modMain "Msg" [] Can.Normal `shouldBe` False

        it "returns False for arbitrary non-carve-out monomorphic ADT" $
            shouldEmitSealedIface modColor "Color" [] Can.Normal `shouldBe` False

    describe "Guard ordering — carve-out wins over allowlist" $ do

        it "rtBuilderShadowList Sky.Core.Error.Error returns False" $
            -- Even if Set.union'd with the allowlist, line 922's
            -- @rtBuilderShadowList@ check fires FIRST (Compile.hs:922
            -- before :923).  This invariant matters when we
            -- populate the allowlist later — a hypothetical entry
            -- that collides with shadow-list MUST still emit legacy.
            shouldEmitSealedIface
                (ModuleName.Canonical "Sky.Core.Error")
                "Error" [] Can.Normal
                `shouldBe` False

        it "rtBuilderShadowList Std.Db.SqlValue returns False" $
            shouldEmitSealedIface modSqlVal "SqlValue" [] Can.Normal
                `shouldBe` False

    describe "Other guards still short-circuit before allowlist" $ do

        it "Can.Enum input returns False regardless of any future allowlist match" $
            shouldEmitSealedIface modMain "Page" [] Can.Enum `shouldBe` False

        it "Can.Unbox input returns False regardless of any future allowlist match" $
            shouldEmitSealedIface modMain "Wrap" [] Can.Unbox `shouldBe` False

        it "Polymorphic (TVars present) input returns False" $
            shouldEmitSealedIface modMain "Box" ["a"] Can.Normal `shouldBe` False

    describe "Co-existence with carve-out (Set disjointness)" $ do

        it "no entry in rtBuilderShadowList appears in the allowlist" $
            -- Currently vacuous because the allowlist is empty, but
            -- this invariant ENFORCES disjointness when future
            -- entries land.  A double-listed entry would be a
            -- gate-ordering tripwire.
            Set.intersection rtBuilderShadowList sealedIfaceFlipAllowList
                `shouldBe` Set.empty
