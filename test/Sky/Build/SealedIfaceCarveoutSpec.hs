{-# LANGUAGE OverloadedStrings #-}

-- | Sky.Build.SealedIfaceCarveoutSpec — audit gate for the v0.17
-- sealed-iface ADT carve-out decision function.
--
-- P3.3 ships the per-ADT decision @shouldEmitSealedIface@ +
-- hardcoded @rtBuilderShadowList@. The function returns False by
-- default (legacy emission) until P3.4 wires the real True-returning
-- branch at the call sites.
--
-- This spec locks four invariants:
--
-- 1. P3.3 default: every input returns False (no codegen change
--    until P3.4).
-- 2. Can.Enum input always returns False regardless of carve-out
--    (rule 1).
-- 3. Polymorphic input (non-empty vars) always returns False (rule
--    2) — P4 scope.
-- 4. 'rtBuilderShadowList' EXACTLY matches the explicit enumeration
--    below. Adding/removing entries silently fails this spec —
--    forces a future contributor to update both the production set
--    AND the spec (Griller 2 R3 — audit gate must IMPORT not
--    re-define).
--
-- Per the iter 48-49 dual-griller findings: every entry's
-- qualified name has been EMPIRICALLY verified against
-- @sky-stdlib/*.sky@ (type declarations) + @runtime-go/rt/*.go@
-- (SkyADT{...} construction sites). The original draft listed
-- @Sky.Db.Sql.SqlValue@ etc. which are fictional module paths.
module Sky.Build.SealedIfaceCarveoutSpec where

import qualified Data.Set as Set
import           Test.Hspec

import qualified Sky.AST.Canonical    as Can
import           Sky.Build.Compile
                     (rtBuilderShadowList, shouldEmitSealedIface)
import qualified Sky.Sky.ModuleName   as ModuleName


spec :: Spec
spec = do
    let modColor   = ModuleName.Canonical "Mod.Color"
    let modError   = ModuleName.Canonical "Sky.Core.Error"
    let modSqlVal  = ModuleName.Canonical "Std.Db"
    let modDecimal = ModuleName.Canonical "Std.Decimal"
    let modWsServer = ModuleName.Canonical "Sky.Http.Server.WebSocket"

    describe "Sky.Build.shouldEmitSealedIface (P3.3 default)" $ do

        it "returns False for a non-carve-out monomorphic ADT (P3.3 default)" $
            shouldEmitSealedIface modColor "Color" [] Can.Normal `shouldBe` False

        it "returns False for Can.Enum even when not in shadow list (rule 1)" $
            shouldEmitSealedIface modColor "Color" [] Can.Enum `shouldBe` False

        it "returns False for Can.Unbox (rule 1b: newtype wrapper)" $
            -- v0.17 P3.4c.0a — dual-grill iter 53 Griller #2 NF6 close.
            -- Unbox semantically wants the unwrapped representation;
            -- sealed-iface defeats the optimisation point.
            shouldEmitSealedIface modColor "Wrapper" [] Can.Unbox `shouldBe` False

        it "returns False for polymorphic ADT (rule 2: P4 scope)" $
            shouldEmitSealedIface modColor "Box" ["a"] Can.Normal `shouldBe` False

        it "returns False for Sky.Core.Error.Error (rule 3: rt-builder shadow)" $
            shouldEmitSealedIface modError "Error" [] Can.Normal `shouldBe` False

        it "returns False for Std.Db.SqlValue (rule 3: rt-builder shadow)" $
            shouldEmitSealedIface modSqlVal "SqlValue" [] Can.Normal `shouldBe` False

        it "returns False for Std.Db.SqlField (rule 3: nested SqlValue invariant)" $
            shouldEmitSealedIface modSqlVal "SqlField" [] Can.Normal `shouldBe` False

        it "returns False for Std.Decimal.Decimal (rule 3: Decimal__Internal ctor)" $
            shouldEmitSealedIface modDecimal "Decimal" [] Can.Normal `shouldBe` False

        it "returns False for Sky.Http.Server.WebSocket.WebSocketServer" $
            shouldEmitSealedIface modWsServer "WebSocketServer" [] Can.Normal
                `shouldBe` False

    describe "rtBuilderShadowList — audit gate" $ do

        it "matches the explicit empirically-verified enumeration" $
            Set.toAscList rtBuilderShadowList `shouldBe`
                [ "Sky.Core.Error.Error"
                , "Sky.Core.Http.Stream.ChunkEvent"
                , "Sky.Core.WebSocket.CloseCode"
                , "Sky.Core.WebSocket.WebSocketMessage"
                , "Sky.Http.Server.Stream.StreamWriter"
                , "Sky.Http.Server.WebSocket.WebSocketServer"
                , "Std.Db.SqlField"
                , "Std.Db.SqlValue"
                , "Std.Decimal.Decimal"
                ]

        it "contains 9 entries (catches accidental list growth)" $
            Set.size rtBuilderShadowList `shouldBe` 9

        it "Sky.Core.WebSocket.CloseCode is shadowed (iter 58 audit close)" $ do
            -- runtime-go/rt/websocket.go:676-693 buildCloseCodeValue ships
            -- SkyADT-shape CloseCode values to user (CloseCode -> msg)
            -- handlers via sky_call. Flipping to sealed-iface without
            -- migrating the rt builder → guaranteed runtime panic at the
            -- user's case-of dispatch. iter 58 dual-grill (A + B) caught
            -- this hole in the iter 49 audit.
            "Sky.Core.WebSocket.CloseCode" `Set.member` rtBuilderShadowList
                `shouldBe` True

        it "co-dependency: SqlValue + SqlField are BOTH present" $ do
            -- Griller 2 B1: SqlField carries SqlValue in SetField SqlValue.
            -- If one moves to sealed-iface and the other stays legacy,
            -- runtime panic. Both must move together.
            "Std.Db.SqlValue" `Set.member` rtBuilderShadowList `shouldBe` True
            "Std.Db.SqlField" `Set.member` rtBuilderShadowList `shouldBe` True
