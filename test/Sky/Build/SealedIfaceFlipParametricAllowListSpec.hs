{-# LANGUAGE OverloadedStrings #-}

-- | Sky.Build.SealedIfaceFlipParametricAllowListSpec — gate for
-- v0.17 iter 88 PARAMETRIC sealed-iface ADT emission allowlist.
--
-- Companion to 'Sky.Build.SealedIfaceFlipAllowListSpec' which covers
-- the MONOMORPHIC entries (currently 22 ADTs shipped).  This spec
-- locks the invariants of the SEPARATE allowlist for PARAMETRIC
-- ADTs (vars non-empty in their canonical declaration).
--
-- == What the parametric allowlist gates
--
-- 'shouldEmitSealedIface' has six arms (see Compile.hs).  Arm (3) is
-- the parametric admit:
--
--   | not (null vars)
--     && qualifiedName `Set.member` sealedIfaceFlipParametricAllowList
--                                                            = True
--
-- It MUST run BEFORE arm (4) which is the parametric default reject:
--
--   | not (null vars)                                        = False
--
-- It MUST run AFTER arm (2) which is the rt-side carve-out:
--
--   | qualifiedName `Set.member` rtBuilderShadowList         = False
--
-- == Empty at scaffolding ship
--
-- The allowlist is currently 'Set.empty' because Phase-0 dual-grill
-- (iter 88) identified a runtime-shim blocker for the canonical
-- targets (@Std.Html.Html@, @Std.Ui.Element msg@, @Std.Ui.Attribute
-- msg@) — see the Haddock on 'sealedIfaceFlipParametricAllowList'
-- in Compile.hs for the full analysis.
--
-- == Invariants this spec locks
--
-- 1. The allowlist is empty (byte-identity contract on every
--    example's main.go; the new arm at line (3) is dead code).
-- 2. The parametric + monomorphic allowlists are DISJOINT (a name
--    in both is a programming-error tripwire).
-- 3. 'shouldEmitSealedIface' rejects a parametric input when its
--    qualified name is NOT in the parametric allowlist.
-- 4. 'shouldEmitSealedIface' rejects a parametric input even when
--    its qualified name is in the MONOMORPHIC allowlist (the two
--    allowlists are not interchangeable — arm (5) only fires when
--    @null vars@).
-- 5. 'rtBuilderShadowList' still wins over the parametric admit
--    (arm 2 fires before arm 3).
module Sky.Build.SealedIfaceFlipParametricAllowListSpec where

import qualified Data.Set as Set
import           Test.Hspec

import qualified Sky.AST.Canonical    as Can
import           Sky.Build.Compile
                     ( rtBuilderShadowList
                     , sealedIfaceFlipAllowList
                     , sealedIfaceFlipParametricAllowList
                     , shouldEmitSealedIface
                     )
import qualified Sky.Sky.ModuleName   as ModuleName


spec :: Spec
spec = do
    describe "v0.17 iter 88 — parametric sealed-iface allowlist" $ do

        it "sealedIfaceFlipParametricAllowList is empty at scaffolding ship" $
            -- Byte-identity contract for iter 88: every example's
            -- main.go must be byte-identical to baseline b3973468.
            -- Populating this set requires the iter 89+ rt-side
            -- compatibility shim per the Compile.hs Haddock.
            Set.null sealedIfaceFlipParametricAllowList `shouldBe` True

        it "parametric + monomorphic allowlists are disjoint" $
            -- A name in both would be a gate-ordering tripwire: arm
            -- (3) would fire for parametric inputs while arm (5) would
            -- fire for monomorphic inputs of the same qualified name.
            -- The split-allowlist design assumes a name belongs to
            -- exactly one tier.
            Set.intersection sealedIfaceFlipAllowList
                             sealedIfaceFlipParametricAllowList
                `shouldBe` Set.empty

        it "shouldEmitSealedIface returns False on a parametric ADT NOT in the allowlist" $ do
            -- The canonical regression case: a parametric Mod.Foo
            -- with vars=[\"msg\"] and Can.Normal opts.  With empty
            -- allowlist, this falls through arm (3) to arm (4)
            -- which is the parametric default reject.
            let modName = ModuleName.Canonical "Test.Mod"
            shouldEmitSealedIface modName "Foo" ["msg"] Can.Normal
                `shouldBe` False

        it "shouldEmitSealedIface returns False on a parametric ADT in the MONOMORPHIC allowlist (wrong list)" $ do
            -- Std.Test.TestResult IS in the monomorphic allowlist
            -- (the first flip from iter 63).  But if we accidentally
            -- pass it WITH vars (e.g. a future code path mistakes a
            -- type-alias for a polymorphic ADT), arm (5) does NOT
            -- fire because arm (4) rejects first.
            let modName = ModuleName.Canonical "Sky.Test"
            shouldEmitSealedIface modName "TestResult" ["msg"] Can.Normal
                `shouldBe` False

        it "shouldEmitSealedIface returns False on a monomorphic ADT in the PARAMETRIC allowlist (wrong list)" $ do
            -- Symmetric to the above: even when (someday) the
            -- parametric allowlist contains an entry, passing it
            -- with null vars routes through arm (5) which checks the
            -- MONOMORPHIC list.  Currently both lists are empty so
            -- this is testing the gate's arm-ordering shape, not a
            -- live mismatch.
            let modName = ModuleName.Canonical "Test.Mod"
            shouldEmitSealedIface modName "FooParametric" [] Can.Normal
                `shouldBe` False

        it "rtBuilderShadowList wins over parametric admit (arm 2 before arm 3)" $ do
            -- Hypothetical: if a future parametric entry collides
            -- with a name in rtBuilderShadowList, the shadow-list
            -- check at arm (2) MUST fire first.  We assert this by
            -- using an existing rtBuilderShadowList entry
            -- (Sky.Core.Error.Error) and verifying it remains
            -- rejected EVEN when called with vars (simulating a
            -- hypothetical parametric form).
            let modName = ModuleName.Canonical "Sky.Core.Error"
            shouldEmitSealedIface modName "Error" ["a"] Can.Normal
                `shouldBe` False

        it "Can.Enum input rejected even for parametric ADTs" $ do
            -- Arm (1) is unconditional — Can.Enum ADTs never flip,
            -- regardless of vars or any allowlist match.
            let modName = ModuleName.Canonical "Test.Mod"
            shouldEmitSealedIface modName "Foo" ["msg"] Can.Enum
                `shouldBe` False

        it "Can.Unbox input rejected even for parametric ADTs" $ do
            -- Arm (1b) is unconditional — Can.Unbox ADTs never flip.
            let modName = ModuleName.Canonical "Test.Mod"
            shouldEmitSealedIface modName "Foo" ["msg"] Can.Unbox
                `shouldBe` False

        it "rtBuilderShadowList ∩ parametric allowlist is empty (invariant)" $
            -- Same shape as the disjointness check vs the monomorphic
            -- allowlist.  Defends against a future entry being added
            -- to the parametric list while shadow-list already
            -- carries the same qualified name.
            Set.intersection rtBuilderShadowList
                             sealedIfaceFlipParametricAllowList
                `shouldBe` Set.empty
