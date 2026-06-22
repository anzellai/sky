{-# LANGUAGE OverloadedStrings #-}

-- | Sky.Build.CaseToGoSealedIfaceSpec — verify the P3.4c.2
-- @caseToGoSealedIface@ helper:
--
--   * Returns @Nothing@ (caller falls back to legacy) for patterns
--     outside the initial scope (PAlias, nested PCtor in args,
--     literal patterns, multiple catchalls, trailing-PCtor-after-
--     catchall).
--
--   * Returns @Just GoExpr@ for the bulk shapes (top-level PCtor
--     with PVar/PAnything args, optional PAnything/PVar catchall
--     as LAST arm).
--
--   * Renders to valid Go via the Builder when the IR is fed
--     through.  Verifies the @default:@ arm renders correctly
--     (per P3.4c.2a IR extension).
--
-- Spec strategy is the same as 'SealedIfaceEmissionSpec' —
-- hand-built Can.CaseBranch fixtures + pattern-match on the
-- returned 'GoIr.GoExpr' value.  Pure unit-testable.
module Sky.Build.CaseToGoSealedIfaceSpec where

import           Data.List           (isInfixOf)
import           Data.Maybe          (isJust, isNothing)
import qualified Data.Map.Strict     as Map
import           Test.Hspec

import qualified Sky.AST.Canonical   as Can
import           Sky.Build.Compile   (caseToGoSealedIface)
import qualified Sky.Build.LowerCtx  as LC
import qualified Sky.Generate.Go.Builder as Builder
import qualified Sky.Generate.Go.Ir  as GoIr
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName  as ModuleName
import qualified Sky.Type.Solve      as Solve


-- ═══════════════════════════════════════════════════════════
-- Fixtures
-- ═══════════════════════════════════════════════════════════

modMain :: ModuleName.Canonical
modMain = ModuleName.Canonical "Main"


regionFor :: Int -> A.Region
regionFor n =
    A.Region (A.Position n 1) (A.Position n 10)


-- | Synthetic subject expression (any Can.Expr is fine — the
-- helper only uses it via exprToGo + region).
fixtureSubject :: Can.Expr
fixtureSubject =
    A.At (regionFor 1) (Can.VarLocal "msg")


-- | A trivial body expression — just a literal int.
litBody :: Int -> Can.Expr
litBody n = A.At (regionFor 99) (Can.Int n)


-- | Build a PCtor pattern with the given ctor name + index +
-- arg patterns (no Sky.Type.Type computation needed — we only
-- read pattern shape).
mkPCtor :: String -> Int -> [Can.Pattern] -> Can.Pattern_
mkPCtor name idx args =
    Can.PCtor modMain "Color" syntheticUnion name idx
        [ Can.PatternCtorArg i Can.TUnit p
        | (i, p) <- zip [0..] args
        ]
  where
    syntheticUnion :: Can.Union
    syntheticUnion = Can.Union [] [] 0 Can.Normal


mkCtx :: LC.LowerCtx
mkCtx = LC.emptyLowerCtx modMain


-- ═══════════════════════════════════════════════════════════
-- Spec
-- ═══════════════════════════════════════════════════════════

spec :: Spec
spec = do
    describe "caseToGoSealedIface — bail to Nothing (legacy fallback)" $ do

        it "bails on empty branches list" $ do
            let result = caseToGoSealedIface
                    mkCtx Solve.emptySolvedTypes Nothing
                    fixtureSubject [] "Mod_Color"
            -- Empty branches → enforceCatchallTrailing returns
            -- ([], Nothing) → Just (no ctor arms, no default) which
            -- the helper then synthesises an rt.Unreachable for.
            -- Empty is actually handled, not bailed.  (No real Sky
            -- source produces this; exhaustiveness rejects upstream.)
            result `shouldSatisfy` isJust

        -- v0.17 P3.4c.2b — PAlias now HANDLED (peeled to inner +
        -- alias binding stmt prepended).  Test moved to happy-path
        -- block below.  Bail-on-literal still applies — literals
        -- against an ADT subject are HM-rejected upstream.
        it "bails on literal pattern (e.g. PInt) against ADT subject" $ do
            let branch = Can.CaseBranch
                    (A.At (regionFor 4) (Can.PInt 0))
                    (litBody 1)
            let result = caseToGoSealedIface
                    mkCtx Solve.emptySolvedTypes Nothing
                    fixtureSubject [branch] "Mod_Color"
            result `shouldSatisfy` isNothing

        it "bails on catchall NOT as last arm (dead-code preservation)" $ do
            -- case x of Red -> 1 ; _ -> 2 ; Green -> 3
            -- The Green arm is dead under legacy; sealed-iface dispatch
            -- would silently make it reachable → bail.
            let branches =
                    [ Can.CaseBranch
                        (A.At (regionFor 8) (mkPCtor "Red" 0 []))
                        (litBody 1)
                    , Can.CaseBranch
                        (A.At (regionFor 9) Can.PAnything)
                        (litBody 2)
                    , Can.CaseBranch
                        (A.At (regionFor 10) (mkPCtor "Green" 1 []))
                        (litBody 3)
                    ]
            let result = caseToGoSealedIface
                    mkCtx Solve.emptySolvedTypes Nothing
                    fixtureSubject branches "Mod_Color"
            result `shouldSatisfy` isNothing

        it "bails on multiple catchalls" $ do
            let branches =
                    [ Can.CaseBranch
                        (A.At (regionFor 11) Can.PAnything)
                        (litBody 1)
                    , Can.CaseBranch
                        (A.At (regionFor 12) (Can.PVar "v"))
                        (litBody 2)
                    ]
            let result = caseToGoSealedIface
                    mkCtx Solve.emptySolvedTypes Nothing
                    fixtureSubject branches "Mod_Color"
            result `shouldSatisfy` isNothing

    describe "caseToGoSealedIface — happy path (Just GoExpr returned)" $ do

        let redBranch = Can.CaseBranch
                (A.At (regionFor 20) (mkPCtor "Red" 0 []))
                (litBody 1)
            greenBranch = Can.CaseBranch
                (A.At (regionFor 21) (mkPCtor "Green" 1 []))
                (litBody 2)
            -- N-ary with PVar args.
            rgbBranch = Can.CaseBranch
                (A.At (regionFor 22)
                    (mkPCtor "RGB" 2
                        [ A.At (regionFor 23) (Can.PVar "r")
                        , A.At (regionFor 24) (Can.PVar "g")
                        , A.At (regionFor 25) (Can.PVar "b")
                        ]))
                (litBody 3)

        it "returns Just for all-PCtor branches (exhaustive)" $ do
            let result = caseToGoSealedIface
                    mkCtx Solve.emptySolvedTypes Nothing
                    fixtureSubject [redBranch, greenBranch] "Mod_Color"
            result `shouldSatisfy` isJust

        it "returns Just for N-ary PCtor with PVar args" $ do
            let result = caseToGoSealedIface
                    mkCtx Solve.emptySolvedTypes Nothing
                    fixtureSubject [redBranch, greenBranch, rgbBranch]
                    "Mod_Color"
            result `shouldSatisfy` isJust

        it "returns Just with PAnything trailing catchall" $ do
            let catchall = Can.CaseBranch
                    (A.At (regionFor 30) Can.PAnything)
                    (litBody 99)
            let result = caseToGoSealedIface
                    mkCtx Solve.emptySolvedTypes Nothing
                    fixtureSubject [redBranch, catchall] "Mod_Color"
            result `shouldSatisfy` isJust

        it "returns Just with PVar trailing catchall (binds subject)" $ do
            let catchall = Can.CaseBranch
                    (A.At (regionFor 31) (Can.PVar "v"))
                    (litBody 99)
            let result = caseToGoSealedIface
                    mkCtx Solve.emptySolvedTypes Nothing
                    fixtureSubject [redBranch, catchall] "Mod_Color"
            result `shouldSatisfy` isJust

        -- v0.17 P3.4c.2b — PAlias now handled (peeled to inner +
        -- alias binding stmt prepended).
        it "returns Just for PAlias on top-level PCtor: (Red) as binding" $ do
            let aliasedBranch = Can.CaseBranch
                    (A.At (regionFor 32)
                        (Can.PAlias
                            (A.At (regionFor 33) (mkPCtor "Red" 0 []))
                            "binding"))
                    (litBody 1)
            let result = caseToGoSealedIface
                    mkCtx Solve.emptySolvedTypes Nothing
                    fixtureSubject [aliasedBranch, greenBranch] "Mod_Color"
            result `shouldSatisfy` isJust

        it "returns Just for PAlias on PAnything catchall: _ as fallback" $ do
            let aliasedCatchall = Can.CaseBranch
                    (A.At (regionFor 34)
                        (Can.PAlias
                            (A.At (regionFor 35) Can.PAnything)
                            "fallback"))
                    (litBody 99)
            let result = caseToGoSealedIface
                    mkCtx Solve.emptySolvedTypes Nothing
                    fixtureSubject [redBranch, aliasedCatchall] "Mod_Color"
            result `shouldSatisfy` isJust

        -- v0.17 P3.4c.2b — non-PCtor inner arg patterns now route
        -- through legacy patternBindings recursion.  Bail only on
        -- inner PCtor (deferred to P3.4c.2d).
        it "returns Just for N-ary PCtor with PAnything args" $ do
            let wildArgsBranch = Can.CaseBranch
                    (A.At (regionFor 36)
                        (mkPCtor "RGB" 2
                            [ A.At (regionFor 37) Can.PAnything
                            , A.At (regionFor 38) Can.PAnything
                            , A.At (regionFor 39) Can.PAnything
                            ]))
                    (litBody 99)
            let result = caseToGoSealedIface
                    mkCtx Solve.emptySolvedTypes Nothing
                    fixtureSubject [redBranch, greenBranch, wildArgsBranch]
                    "Mod_Color"
            result `shouldSatisfy` isJust

        it "still bails on nested PCtor inside ctor arg (deferred to P3.4c.2d)" $ do
            -- This case stays bailed in P3.4c.2b.  Nested sealed-iface
            -- dispatch is non-trivial — requires a nested GoTypeSwitch
            -- on the inner ADT.  The first flipped ADT in P3.4d MUST
            -- NOT have ctor args carrying other sealed-iface-flipped
            -- ADTs (documented precondition).
            let innerPCtor =
                    A.At (regionFor 40) (mkPCtor "Just" 1
                        [A.At (regionFor 41) (Can.PVar "user")])
                outerBranch = Can.CaseBranch
                    (A.At (regionFor 42)
                        (mkPCtor "Joined" 0 [innerPCtor]))
                    (litBody 1)
            let result = caseToGoSealedIface
                    mkCtx Solve.emptySolvedTypes Nothing
                    fixtureSubject [outerBranch] "Mod_Color"
            result `shouldSatisfy` isNothing

    describe "caseToGoSealedIface — Builder round-trip emits valid Go" $ do

        let redBranch = Can.CaseBranch
                (A.At (regionFor 40) (mkPCtor "Red" 0 []))
                (litBody 1)
            greenBranch = Can.CaseBranch
                (A.At (regionFor 41) (mkPCtor "Green" 1 []))
                (litBody 2)
        let result = caseToGoSealedIface
                mkCtx Solve.emptySolvedTypes Nothing
                fixtureSubject [redBranch, greenBranch] "Mod_Color"
        let renderedLines = case result of
                Just expr ->
                    -- Render via GoExprStmt wrapper to drive Builder.
                    Builder.renderStmt (GoIr.GoExprStmt expr)
                Nothing -> ["(no emission)"]
        let rendered = unlines renderedLines

        it "includes the switch/type/(type) guard" $ do
            rendered `shouldContain` "switch __subject"
            rendered `shouldContain` ".(type)"

        it "emits a case for the Red variant struct" $ do
            rendered `shouldContain` "case Mod_Color_Red_V:"

        it "emits a case for the Green variant struct" $ do
            rendered `shouldContain` "case Mod_Color_Green_V:"

        it "emits a default arm (NOT a `case default:` syntax error)" $ do
            rendered `shouldContain` "default:"
            -- Defensive: the literal `case default:` was the bug
            -- pattern Griller #1 flagged — assert it never appears.
            rendered `shouldNotContain` "case default:"

        it "default arm carries the rt.Unreachable safety net" $ do
            rendered `shouldContain` "rt.Unreachable"
            rendered `shouldContain` "case/Mod_Color"
