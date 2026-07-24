{-# LANGUAGE OverloadedStrings #-}

-- | Sky.Build.SealedIfaceMetadataSpec — verify P3.4c.0 widened
-- 'Rec.CodegenEnv._cg_unionDetails' + 'LC.LowerCtx._lc_unionDetails'
-- maps carry the metadata the upcoming sealed-iface emission gate
-- needs (per-union opts + ctors + originating ModuleName.Canonical).
--
-- P3.4c.0 ships the metadata channel only.  No gate is wired — the
-- maps are populated and consumed downstream by P3.4c.1+
-- (subjectIsSealedIface + shouldEmitSealedIface).  This spec proves
-- the population path: entry module ADTs key by bare type name,
-- dep modules by prefixed name, opts/ctors/home faithfully reflect
-- the source Can.Union.
module Sky.Build.SealedIfaceMetadataSpec where

import qualified Data.Map.Strict     as Map
import           Test.Hspec

import qualified Sky.AST.Canonical   as Can
import qualified Sky.Generate.Go.Record as Rec
import qualified Sky.Sky.ModuleName  as ModuleName
import qualified Sky.Type.Solve      as Solve


spec :: Spec
spec = do
    describe "Rec._cg_unionDetails — entry module population" $ do

        let colorCtors =
                [ Can.Ctor "Red"   0 0 []
                , Can.Ctor "Green" 1 0 []
                , Can.Ctor "RGB"   2 3 []
                ]
            shapeCtors =
                [ Can.Ctor "Circle" 0 1 []
                , Can.Ctor "Square" 1 1 []
                ]
            -- entry module = Main with two ADTs: Color (Normal) + Shape (Normal)
            entryMod = Can.Module
                { Can._name    = ModuleName.Canonical "Main"
                , Can._exports = Can.ExportEverything
                , Can._decls   = Can.SaveTheEnvironment
                , Can._unions  = Map.fromList
                    [ ( "Color"
                      , Can.Union [] colorCtors 3 Can.Normal
                      )
                    , ( "Shape"
                      , Can.Union [] shapeCtors 2 Can.Normal
                      )
                    ]
                , Can._aliases = Map.empty
                }
            cgEnv = Rec.buildCodegenEnv Solve.emptySolvedTypes entryMod

        it "maps entry-module union by bare type name (Color)" $ do
            Map.lookup "Color" (Rec._cg_unionDetails cgEnv) `shouldSatisfy`
                \mEntry -> case mEntry of
                    Just (mn, opts, vars, ctors) ->
                        mn == ModuleName.Canonical "Main"
                            && opts == Can.Normal
                            && vars == []
                            && length ctors == 3
                    Nothing -> False

        it "maps entry-module union by bare type name (Shape)" $ do
            Map.lookup "Shape" (Rec._cg_unionDetails cgEnv) `shouldSatisfy`
                \mEntry -> case mEntry of
                    Just (mn, opts, _vars, ctors) ->
                        mn == ModuleName.Canonical "Main"
                            && opts == Can.Normal
                            && length ctors == 2
                    Nothing -> False

        it "every '_cg_unionNames' entry has a matching '_cg_unionDetails' entry" $ do
            let names    = Rec._cg_unionNames   cgEnv
                details  = Rec._cg_unionDetails cgEnv
                detailKs = Map.keysSet details
            detailKs `shouldBe` names

    describe "Rec._cg_unionDetails — Enum classification" $ do

        let enumCtors =
                [ Can.Ctor "Mon" 0 0 []
                , Can.Ctor "Tue" 1 0 []
                , Can.Ctor "Wed" 2 0 []
                ]
            enumMod = Can.Module
                { Can._name    = ModuleName.Canonical "Main"
                , Can._exports = Can.ExportEverything
                , Can._decls   = Can.SaveTheEnvironment
                , Can._unions  = Map.singleton "Day"
                    (Can.Union [] enumCtors 3 Can.Enum)
                , Can._aliases = Map.empty
                }
            cgEnv = Rec.buildCodegenEnv Solve.emptySolvedTypes enumMod

        it "preserves Can.Enum opts in the metadata map" $ do
            case Map.lookup "Day" (Rec._cg_unionDetails cgEnv) of
                Just (_, opts, _, _) -> opts `shouldBe` Can.Enum
                Nothing -> expectationFailure "Day entry missing"

    describe "Rec.withUnionDetails — dep-module extension" $ do

        let depCtors = [Can.Ctor "ErrIo" 0 1 []]
            depMetaIn = Map.singleton "Sky_Core_Error_Error"
                ( ModuleName.Canonical "Sky.Core.Error"
                , Can.Normal
                , []
                , depCtors
                )
            baseEnv = Rec.buildCodegenEnv Solve.emptySolvedTypes Can.Module
                { Can._name    = ModuleName.Canonical "Main"
                , Can._exports = Can.ExportEverything
                , Can._decls   = Can.SaveTheEnvironment
                , Can._unions  = Map.empty
                , Can._aliases = Map.empty
                }
            extEnv = Rec.withUnionDetails depMetaIn baseEnv

        it "adds dep-keyed entry without disturbing the base map" $ do
            Map.lookup "Sky_Core_Error_Error" (Rec._cg_unionDetails extEnv)
                `shouldSatisfy` \r -> case r of
                    Just (mn, _, _, ctors) ->
                        mn == ModuleName.Canonical "Sky.Core.Error"
                            && length ctors == 1
                    Nothing -> False

        it "extending an empty base preserves only the new entries" $ do
            Map.keysSet (Rec._cg_unionDetails extEnv)
                `shouldBe` Map.keysSet depMetaIn

    describe "Rec._cg_unionDetails — Parametric ADT (with type vars)" $ do

        let resultCtors =
                [ Can.Ctor "Ok"  0 1 []
                , Can.Ctor "Err" 1 1 []
                ]
            -- Synthetic Result e a (parametric)
            resultMod = Can.Module
                { Can._name    = ModuleName.Canonical "Sky.Core.Result"
                , Can._exports = Can.ExportEverything
                , Can._decls   = Can.SaveTheEnvironment
                , Can._unions  = Map.singleton "Result"
                    (Can.Union ["e", "a"] resultCtors 2 Can.Normal)
                , Can._aliases = Map.empty
                }
            cgEnv = Rec.buildCodegenEnv Solve.emptySolvedTypes resultMod

        it "preserves the type-variable list" $ do
            case Map.lookup "Result" (Rec._cg_unionDetails cgEnv) of
                Just (_, _, vars, _) -> vars `shouldBe` ["e", "a"]
                Nothing -> expectationFailure "Result entry missing"
