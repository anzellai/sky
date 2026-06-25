{-# LANGUAGE OverloadedStrings #-}

-- | Sky.Build.SealedIfaceEmissionSpec — verify the pure
-- 'emitSealedIfaceUnion' helper emits the structurally-correct
-- GoIr.GoDecl shape per the iter-50 dual-grill design.
--
-- P3.4a ships the helper. NOT WIRED — 'generateUnion' +
-- 'generateUnionForDep' still emit the legacy @type X = rt.SkyADT@
-- shape until P3.4b/c flip them per-ADT via 'shouldEmitSealedIface'.
--
-- These specs call the helper directly with hand-built
-- @[Can.Ctor]@ lists and pattern-match on the returned decls.
-- GoDecl has no Eq instance (per Griller 2 R5); we use structural
-- pattern matching + 'show' assertions.
module Sky.Build.SealedIfaceEmissionSpec where

import           Data.List (isInfixOf)
import           Test.Hspec

import qualified Sky.AST.Canonical    as Can
import           Sky.Build.Compile    (emitSealedIfaceUnion)
import           Sky.Build.CompileCtx (emptyEmitCompileCtx)
import qualified Sky.Sky.ModuleName   as ModuleName
import qualified Sky.Generate.Go.Ir   as GoIr


spec :: Spec
spec = do
    describe "emitSealedIfaceUnion — sealed-iface emission for monomorphic ADT" $ do

        let colorCtors =
                [ Can.Ctor "Red"   0 0 []
                , Can.Ctor "Green" 1 0 []
                , Can.Ctor "RGB"   2 3 []  -- argTys empty; ctorFieldGoType defaults to "any"
                ]

        let dummyCtx = emptyEmitCompileCtx (ModuleName.Canonical "Main")
        let decls = emitSealedIfaceUnion dummyCtx "Mod_Color" [] colorCtors

        it "first decl is the sealed interface Mod_Color" $ do
            case decls of
                (GoIr.GoDeclInterface name methods : _) -> do
                    name `shouldBe` "Mod_Color"
                    map (\(n, _, t) -> (n, t)) methods `shouldBe`
                        [ ("SkyVariantTag",  "int")
                        , ("SkyVariantName", "string")
                        ]
                _ -> expectationFailure "expected GoDeclInterface as first decl"

        it "emits 3 type decls (one struct per variant)" $ do
            let typeDecls = [d | d@(GoIr.GoDeclType _ _) <- decls]
            length typeDecls `shouldBe` 3

        it "nullary variant has SkyVariant_ uint8 dummy field" $ do
            let typeDecls = [d | d@(GoIr.GoDeclType _ _) <- decls]
            case typeDecls of
                (GoIr.GoDeclType n (GoIr.GoStructDef fields) : _) -> do
                    n `shouldBe` "Mod_Color_Red_V"
                    fields `shouldBe` [("SkyVariant_", "uint8")]
                _ -> expectationFailure "first variant struct shape wrong"

        it "N-ary variant has V0/V1/V2 typed fields" $ do
            let typeDecls = [d | d@(GoIr.GoDeclType _ _) <- decls]
            case last typeDecls of
                GoIr.GoDeclType n (GoIr.GoStructDef fields) -> do
                    n `shouldBe` "Mod_Color_RGB_V"
                    map fst fields `shouldBe` ["V0", "V1", "V2"]
                _ -> expectationFailure "RGB variant struct shape wrong"

        it "emits 6 method decls (Tag + Name per variant × 3 variants)" $ do
            let methodDecls = [d | d@(GoIr.GoDeclMethod _ _ _) <- decls]
            length methodDecls `shouldBe` 6

        it "nullary variants emit GoDeclVar bindings" $ do
            -- Red + Green nullary → 2 var decls
            let varDecls = [d | d@(GoIr.GoDeclVar _ _ _) <- decls]
            length varDecls `shouldBe` 2
            case varDecls of
                [GoIr.GoDeclVar n1 t1 _, GoIr.GoDeclVar n2 t2 _] -> do
                    (n1, t1) `shouldBe` ("Mod_Color_Red",   "Mod_Color_Red_V")
                    (n2, t2) `shouldBe` ("Mod_Color_Green", "Mod_Color_Green_V")
                _ -> expectationFailure "var decls shape wrong"

        it "N-ary variant emits GoDeclFunc constructor returning variant struct" $ do
            let funcDecls = [d | d@(GoIr.GoDeclFunc _) <- decls]
            case funcDecls of
                [GoIr.GoDeclFunc func] -> do
                    GoIr._gf_name func       `shouldBe` "Mod_Color_RGB"
                    GoIr._gf_returnType func `shouldBe` "Mod_Color_RGB_V"
                    length (GoIr._gf_params func) `shouldBe` 3
                _ -> expectationFailure "expected 1 GoDeclFunc for RGB"

        it "init block GoDeclRaw contains RegisterAdtTag + RegisterAdtVariant + gob.Register per ctor" $ do
            let rawDecls = [raw | GoIr.GoDeclRaw raw <- decls]
            case rawDecls of
                [initBody] -> do
                    -- RegisterAdtTag — legacy compat (3 lines)
                    initBody `shouldContain` "rt.RegisterAdtTag(\"Red\", 0)"
                    initBody `shouldContain` "rt.RegisterAdtTag(\"Green\", 1)"
                    initBody `shouldContain` "rt.RegisterAdtTag(\"RGB\", 2)"
                    -- RegisterAdtVariant factories
                    initBody `shouldContain` "rt.RegisterAdtVariant(\"Red\""
                    initBody `shouldContain` "rt.RegisterAdtVariant(\"Green\""
                    initBody `shouldContain` "rt.RegisterAdtVariant(\"RGB\""
                    -- N-ary factory has rt.JsonUnmarshal steps
                    -- (v0.17 iter 63 — re-routed via rt.* re-exports
                    -- to keep emitted main.go's import list to
                    -- "sky-app/rt" only)
                    initBody `shouldContain` "rt.JsonUnmarshal(raw[0]"
                    initBody `shouldContain` "rt.JsonUnmarshal(raw[1]"
                    initBody `shouldContain` "rt.JsonUnmarshal(raw[2]"
                    -- rt.GobRegister per variant
                    initBody `shouldContain` "rt.GobRegister(Mod_Color_Red_V{})"
                    initBody `shouldContain` "rt.GobRegister(Mod_Color_Green_V{})"
                    initBody `shouldContain` "rt.GobRegister(Mod_Color_RGB_V{})"
                _ -> expectationFailure "expected exactly 1 GoDeclRaw (init block)"

        it "factory closure body for nullary variant is short-circuit (no Unmarshal)" $ do
            let rawDecls = [raw | GoIr.GoDeclRaw raw <- decls]
            case rawDecls of
                [initBody] -> do
                    -- Red is nullary — its factory line should NOT mention raw[0]
                    let redLineLines = [ln | ln <- lines initBody, "Red\"" `isInfixOf` ln, "RegisterAdtVariant" `isInfixOf` ln]
                    case redLineLines of
                        [ln] -> ln `shouldNotContain` "raw[0]"
                        _ -> expectationFailure "expected exactly one Red RegisterAdtVariant line"
                _ -> expectationFailure "expected exactly 1 GoDeclRaw"

    describe "emitSealedIfaceUnion — minimal single-ctor ADT" $ do
        let oneCtor = [Can.Ctor "Wrap" 0 0 []]
        let dummyCtx = emptyEmitCompileCtx (ModuleName.Canonical "Main")
        let decls = emitSealedIfaceUnion dummyCtx "Mod_Wrapper" [] oneCtor

        it "emits interface + 1 struct + 2 methods + 1 var + 1 init for nullary single ctor" $ do
            let counts =
                    ( length [() | GoIr.GoDeclInterface _ _ <- decls]
                    , length [() | GoIr.GoDeclType _ _ <- decls]
                    , length [() | GoIr.GoDeclMethod _ _ _ <- decls]
                    , length [() | GoIr.GoDeclVar _ _ _ <- decls]
                    , length [() | GoIr.GoDeclFunc _ <- decls]
                    , length [() | GoIr.GoDeclRaw _ <- decls]
                    )
            counts `shouldBe` (1, 1, 2, 1, 0, 1)
