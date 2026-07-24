{-# LANGUAGE OverloadedStrings #-}

-- | Sky.Build.MsgDispatchSpec — unit tests for
-- 'Sky.Build.MsgDispatch.collectMsgVariants' and its emission
-- helpers.  Pure-Haskell harness, no compiler subprocess, no
-- example-sweep dependencies.
--
-- v0.17 Phase 4, Stage 1.  Locks the variant-enumeration shape
-- + the Stage 1 observable emission ('rt.RegisterMsgUpdate' +
-- 'rt.RegisterMsgVariant') against the Can.Module surface so
-- downstream Phase 4 stages (typed update arms, dispatch
-- tables, wire decoders) can rely on the helper's output
-- without re-walking @Can.Union@ values.
module Sky.Build.MsgDispatchSpec (spec) where

import qualified Data.Map.Strict     as Map
import           Test.Hspec

import qualified Sky.AST.Canonical   as Can
import qualified Sky.Build.MsgDispatch as MD
import qualified Sky.Sky.ModuleName  as ModuleName


-- ---------------------------------------------------------------
-- Fixture helpers — small ADT module values for the unit specs.
-- ---------------------------------------------------------------

-- | Build a 'Can.Module' with the given (typeName, Union) entries.
mkModule :: ModuleName.Canonical -> [(String, Can.Union)] -> Can.Module
mkModule mn unions = Can.Module
    { Can._name    = mn
    , Can._exports = Can.ExportEverything
    , Can._decls   = Can.SaveTheEnvironment
    , Can._unions  = Map.fromList unions
    , Can._aliases = Map.empty
    }


-- | @Can.Union@ shortcut: vars, ctors, opts.  numAlts derived
-- from the constructor list length so we don't have to count.
mkUnion :: [String] -> [Can.Ctor] -> Can.CtorOpts -> Can.Union
mkUnion vars ctors opts = Can.Union
    { Can._u_vars    = vars
    , Can._u_alts    = ctors
    , Can._u_numAlts = length ctors
    , Can._u_opts    = opts
    }


-- ---------------------------------------------------------------
-- Specs.
-- ---------------------------------------------------------------

spec :: Spec
spec = describe "Sky.Build.MsgDispatch (v0.17 Phase 4 Stage 1)" $ do

    -- A monomorphic TEA-shape ADT:
    -- @type Msg = Increment | Decrement | SetValue Int | Mix Int String@
    let msgUnion = mkUnion []
            [ Can.Ctor "Increment" 0 0 []
            , Can.Ctor "Decrement" 1 0 []
            , Can.Ctor "SetValue"  2 1 [Can.TType (ModuleName.Canonical "Sky.Core.Basics") "Int" []]
            , Can.Ctor "Mix"       3 2
                [ Can.TType (ModuleName.Canonical "Sky.Core.Basics") "Int"    []
                , Can.TType (ModuleName.Canonical "Sky.Core.String") "String" []
                ]
            ]
            Can.Normal
        msgModule = mkModule (ModuleName.Canonical "Main")
                        [("Msg", msgUnion)]

    describe "variantsFromUnion" $ do

        it "enumerates every constructor in declaration order" $ do
            let vs = MD.variantsFromUnion msgUnion
            map MD._mv_name vs `shouldBe`
                ["Increment", "Decrement", "SetValue", "Mix"]

        it "preserves the declared tag indices" $ do
            let vs = MD.variantsFromUnion msgUnion
            map MD._mv_tag vs `shouldBe` [0, 1, 2, 3]

        it "preserves per-variant arity" $ do
            let vs = MD.variantsFromUnion msgUnion
            map MD._mv_arity vs `shouldBe` [0, 0, 1, 2]

        it "preserves typed payload parameters" $ do
            let vs = MD.variantsFromUnion msgUnion
                lengths = map (length . MD._mv_argTys) vs
            lengths `shouldBe` [0, 0, 1, 2]

        it "is deterministic — repeat call yields the same shape" $ do
            let vs1 = MD.variantsFromUnion msgUnion
                vs2 = MD.variantsFromUnion msgUnion
            map MD._mv_tag  vs1 `shouldBe` map MD._mv_tag  vs2
            map MD._mv_name vs1 `shouldBe` map MD._mv_name vs2

    describe "collectMsgVariants — module-level" $ do

        it "returns one MsgUnion per declared ADT" $ do
            let mus = MD.collectMsgVariants msgModule
            map MD._mu_typeName mus `shouldBe` ["Msg"]

        it "preserves type-variable list (monomorphic = empty)" $ do
            let mus = MD.collectMsgVariants msgModule
            map MD._mu_vars mus `shouldBe` [[]]

        it "preserves CtorOpts" $ do
            let mus = MD.collectMsgVariants msgModule
            map MD._mu_opts mus `shouldBe` [Can.Normal]

        it "alphabetically orders multiple ADTs by bare type name" $ do
            let m = mkModule (ModuleName.Canonical "Main")
                        [ ("Zebra", mkUnion [] [Can.Ctor "Z" 0 0 []] Can.Normal)
                        , ("Alpha", mkUnion [] [Can.Ctor "A" 0 0 []] Can.Normal)
                        , ("Mango", mkUnion [] [Can.Ctor "M" 0 0 []] Can.Normal)
                        ]
            map MD._mu_typeName (MD.collectMsgVariants m)
                `shouldBe` ["Alpha", "Mango", "Zebra"]

        it "returns empty list for a module with no ADTs" $ do
            let m = mkModule (ModuleName.Canonical "Main") []
            length (MD.collectMsgVariants m) `shouldBe` 0

    describe "isMsgShapedUnion — Phase 4 emission gate" $ do

        let normalU = head (MD.collectMsgVariants msgModule)

        it "accepts a Normal ADT with 4+ ctors" $ do
            MD.isMsgShapedUnion normalU `shouldBe` True

        it "rejects an Enum ADT (all nullary)" $ do
            let m = mkModule (ModuleName.Canonical "Main")
                        [ ("Direction"
                          , mkUnion []
                              [ Can.Ctor "North" 0 0 []
                              , Can.Ctor "South" 1 0 []
                              , Can.Ctor "East"  2 0 []
                              , Can.Ctor "West"  3 0 []
                              ] Can.Enum
                          )
                        ]
                [mu] = MD.collectMsgVariants m
            MD.isMsgShapedUnion mu `shouldBe` False

        it "rejects an Unbox ADT (single-ctor wrapper)" $ do
            let m = mkModule (ModuleName.Canonical "Main")
                        [ ("Wrapper"
                          , mkUnion []
                              [ Can.Ctor "Wrap" 0 1
                                    [Can.TType (ModuleName.Canonical "Sky.Core.Basics") "Int" []]
                              ] Can.Unbox
                          )
                        ]
                [mu] = MD.collectMsgVariants m
            MD.isMsgShapedUnion mu `shouldBe` False

        it "rejects a degenerate zero-variant ADT" $ do
            let m = mkModule (ModuleName.Canonical "Main")
                        [ ("Empty", mkUnion [] [] Can.Normal) ]
                [mu] = MD.collectMsgVariants m
            MD.isMsgShapedUnion mu `shouldBe` False

    describe "emitRegisterUpdateLine — Stage 1 observable" $ do

        it "emits the rt.RegisterMsgUpdate(qual, nil) shape" $ do
            MD.emitRegisterUpdateLine "Main_Msg"
                `shouldBe` "rt.RegisterMsgUpdate(\"Main_Msg\", nil)"

        it "preserves the qualified Go name verbatim" $ do
            MD.emitRegisterUpdateLine "Sky_Core_Error_Error"
                `shouldBe` "rt.RegisterMsgUpdate(\"Sky_Core_Error_Error\", nil)"

        it "is byte-identical for the same input (deterministic)" $ do
            MD.emitRegisterUpdateLine "X" `shouldBe` MD.emitRegisterUpdateLine "X"

    describe "emitRegisterMsgVariantLine — Stage 1 observable" $ do

        let vs = MD.variantsFromUnion msgUnion

        it "emits the (qual, ctor, tag, arity) shape for a nullary variant" $ do
            MD.emitRegisterMsgVariantLine "Main_Msg" (head vs)
                `shouldBe`
                "rt.RegisterMsgVariant(\"Main_Msg\", \"Increment\", 0, 0)"

        it "emits the correct arity for a unary variant" $ do
            MD.emitRegisterMsgVariantLine "Main_Msg" (vs !! 2)
                `shouldBe`
                "rt.RegisterMsgVariant(\"Main_Msg\", \"SetValue\", 2, 1)"

        it "emits the correct arity for a binary variant" $ do
            MD.emitRegisterMsgVariantLine "Main_Msg" (vs !! 3)
                `shouldBe`
                "rt.RegisterMsgVariant(\"Main_Msg\", \"Mix\", 3, 2)"

    describe "Parametric ADT (Maybe-like) — type-var preservation" $ do

        -- @type Box a = Empty | Filled a@
        let boxUnion = mkUnion ["a"]
                [ Can.Ctor "Empty"  0 0 []
                , Can.Ctor "Filled" 1 1 [Can.TVar "a"]
                ] Can.Normal
            boxModule = mkModule (ModuleName.Canonical "Main")
                            [("Box", boxUnion)]

        it "preserves type-variable list on parametric ADTs" $ do
            let [mu] = MD.collectMsgVariants boxModule
            MD._mu_vars mu `shouldBe` ["a"]

        it "passes the Msg-shape gate (Normal + non-empty variants)" $ do
            let [mu] = MD.collectMsgVariants boxModule
            MD.isMsgShapedUnion mu `shouldBe` True

        it "the second variant carries the TVar argument" $ do
            let [mu] = MD.collectMsgVariants boxModule
                vs   = MD._mu_variants mu
            MD._mv_argTys (vs !! 1) `shouldBe` [Can.TVar "a"]
