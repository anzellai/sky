-- | v0.15.x P37a — SolvedTypes carries region map as pure data.
--
-- Locks the solver-side surgery shipped in
-- `feat/v0.15.x-hardening-P37a-solved-types-region-map`:
-- `Solve.SolvedTypes` is now a record carrying BOTH the per-name
-- HM-type environment AND the per-region HM-type map (the latter
-- previously returned only as a 4-tuple slot from
-- `solveWithInstancesAndRegions`).  This spec is the regression
-- gate so a future edit cannot silently drop the region field or
-- mis-populate it from a stale IORef snapshot.
--
-- The follow-up PR (P37b) consumes `_stRegions` to make
-- `letBindingType`'s region lookup pure, eliminating the
-- IORef-backed reader that today still services `lookupRegionType`.
-- This spec only locks the populate-time contract — the consume-
-- time migration ships separately.
module Sky.Type.SolvedTypesRegionMapSpec (spec) where

import Test.Hspec
import qualified Data.Map.Strict as Map
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Type.Type as T
import qualified Sky.Type.Solve as Solve


-- ─── helpers ─────────────────────────────────────────────────────


tyInt :: T.Type
tyInt = T.TType ModuleName.basics "Int" []


tyString :: T.Type
tyString = T.TType ModuleName.basics "String" []


tyBool :: T.Type
tyBool = T.TType ModuleName.basics "Bool" []


-- | Synthesise an A.Region at a given (line, col).  Non-zero
-- positions are required: the solver filters synthetic (0, 0)
-- regions out of `_regionVars` (see `Solve.recordRegionVar`).
mkRegion :: Int -> Int -> A.Region
mkRegion line col =
    A.Region (A.Position line col) (A.Position line (col + 4))


-- | Build a `CEqual` constraint at `region`, asserting `actual`
-- unifies with `expected` (both bare types — no FromAnnotation /
-- FromContext wrappers).  The `Category` doesn't affect solve
-- semantics; it only flavours diagnostic output, so we pick the
-- closest-fitting category for each fixture.
eq :: A.Region -> T.Category -> T.Type -> T.Type -> T.Constraint
eq region cat actual expected =
    T.CEqual region cat actual (T.NoExpectation expected)


-- ─── tests ───────────────────────────────────────────────────────


spec :: Spec
spec = do

    describe "Solve.emptySolvedTypes" $ do
        it "has both env and region maps empty" $ do
            Solve._stEnv     Solve.emptySolvedTypes `shouldBe` Map.empty
            Solve._stRegions Solve.emptySolvedTypes `shouldBe` Map.empty

        it "lookupSolvedVar on the empty value returns Nothing" $ do
            Solve.lookupSolvedVar "anything"
                Solve.emptySolvedTypes `shouldBe` Nothing

        it "lookupSolvedRegion on the empty value returns Nothing" $ do
            Solve.lookupSolvedRegion (mkRegion 1 1)
                Solve.emptySolvedTypes `shouldBe` Nothing

    describe "Solve.SolvedTypes record helpers" $ do
        it "insertSolvedVar leaves the region map untouched" $ do
            let st0 = Solve.SolvedTypes
                        Map.empty
                        (Map.singleton (mkRegion 5 5) tyInt)
                        Map.empty
                        Map.empty
                        Nothing
                        Map.empty
                st1 = Solve.insertSolvedVar "x" tyString st0
            Solve.lookupSolvedVar "x" st1 `shouldBe` Just tyString
            -- Region map preserved bit-for-bit.
            Solve._stRegions st1 `shouldBe` Solve._stRegions st0

        it "unionSolvedEnv merges with the additions winning on clash" $ do
            let st0 = Solve.SolvedTypes
                        (Map.fromList [("a", tyInt), ("b", tyString)])
                        Map.empty
                        Map.empty
                        Map.empty
                        Nothing
                        Map.empty
                additions = Map.fromList [("b", tyBool), ("c", tyInt)]
                st1 = Solve.unionSolvedEnv additions st0
            Solve.lookupSolvedVar "a" st1 `shouldBe` Just tyInt
            -- 'b' overridden by the additions map.
            Solve.lookupSolvedVar "b" st1 `shouldBe` Just tyBool
            Solve.lookupSolvedVar "c" st1 `shouldBe` Just tyInt

    describe "Solve.solveWithInstancesAndRegions" $ do
        it "populates _stRegions with the same map as the 4-tuple slot" $ do
            -- Two CEqual constraints at distinct non-zero regions
            -- exercise the `recordRegionVar` path.  This is the
            -- shape the solver writes for any non-synthetic
            -- expression — `let x = 42` and `let y = "abc"` in a
            -- real module would produce constraints of exactly
            -- this shape, keyed by the source region of each RHS.
            let r1 = mkRegion 10 5
                r2 = mkRegion 20 5
                c1 = eq r1 T.CNumber tyInt    tyInt
                c2 = eq r2 T.CString tyString tyString
                cs = T.CAnd [c1, c2]
            (res, _, _, regionTysFromTuple)
                <- Solve.solveWithInstancesAndRegions cs
            case res of
                Solve.SolveOk solved -> do
                    -- The acceptance contract: the SolvedTypes
                    -- record carries the SAME data as the 4-tuple's
                    -- regionTys slot.  P37b consumes the record
                    -- field; today the 4-tuple slot is still the
                    -- source of truth for `Compile.hs`'s IORef
                    -- write.  Locking byte-equality here keys both
                    -- consumers off one populate-time write.
                    Solve._stRegions solved `shouldBe` regionTysFromTuple
                    -- And the data isn't empty — the test fixture
                    -- has two recorded regions.
                    Map.size (Solve._stRegions solved) `shouldBe` 2
                Solve.SolveError e ->
                    expectationFailure ("solve failed: " ++ e)

        it "exposes the per-region types via lookupSolvedRegion" $ do
            let r1 = mkRegion 7 1
                r2 = mkRegion 8 1
                c1 = eq r1 T.CNumber       tyInt  tyInt
                c2 = eq r2 (T.CCustom "Bool") tyBool tyBool
                cs = T.CAnd [c1, c2]
            (res, _, _, _) <- Solve.solveWithInstancesAndRegions cs
            case res of
                Solve.SolveOk solved -> do
                    Solve.lookupSolvedRegion r1 solved `shouldBe` Just tyInt
                    Solve.lookupSolvedRegion r2 solved `shouldBe` Just tyBool
                    -- A non-recorded region returns Nothing.
                    Solve.lookupSolvedRegion (mkRegion 99 99) solved
                        `shouldBe` Nothing
                Solve.SolveError e ->
                    expectationFailure ("solve failed: " ++ e)

        it "filters synthetic (0, 0) regions out of the region map" $ do
            -- This mirrors the existing filter in
            -- `Solve.recordRegionVar`: constraint generators emit
            -- `A.zero` / `A.one` sentinels for let-binding headers,
            -- lambda-param scopes, etc.  Those should NOT appear in
            -- the region map (would leak bookkeeping noise into
            -- `letBindingType`'s lookup once P37b switches to the
            -- pure path).
            let zeroR = A.Region (A.Position 0 0) (A.Position 0 0)
                c     = eq zeroR T.CNumber tyInt tyInt
            (res, _, _, _) <- Solve.solveWithInstancesAndRegions c
            case res of
                Solve.SolveOk solved ->
                    Map.member zeroR (Solve._stRegions solved)
                        `shouldBe` False
                Solve.SolveError e ->
                    expectationFailure ("solve failed: " ++ e)

    describe "Solve.solve" $ do
        it "populates _stRegions even when not threading through the regions API" $ do
            -- AC#1 contract: every solver entry point produces a
            -- SolvedTypes value whose region map is the same pure
            -- snapshot that `_regionVars` carries.  The LSP /
            -- single-binding entry points (`solve` / `solveWithLocals`)
            -- get it for free since `_regionVars` is populated as
            -- the constraints unify, independent of the entry
            -- point.  This keeps P37b's consumer story uniform —
            -- the consumer doesn't have to ask "which path did this
            -- SolvedTypes come from?".
            let r  = mkRegion 3 3
                c  = eq r T.CString tyString tyString
            res <- Solve.solve c
            case res of
                Solve.SolveOk solved ->
                    Solve.lookupSolvedRegion r solved
                        `shouldBe` Just tyString
                Solve.SolveError e ->
                    expectationFailure ("solve failed: " ++ e)

    describe "P37b drop-in shape" $ do
        it "letBindingType-shaped lookup can be expressed as pure data" $ do
            -- P37b's job is to migrate `letBindingType` from
            -- `LC.lookupRegionType ctx region` (IORef-backed) to
            -- a pure projection over `Solve.SolvedTypes`.  This
            -- assertion is the smoke test for that migration:
            -- the let-binding's RHS-region lookup, derived purely
            -- from a `SolvedTypes` value, returns the type the
            -- solver actually recorded.  No `unsafePerformIO`
            -- in sight.
            let rhsRegion = mkRegion 15 9
                c         = eq rhsRegion T.CNumber tyInt tyInt
            res <- Solve.solve c
            case res of
                Solve.SolveOk solved -> do
                    let pureLookup = Solve.lookupSolvedRegion rhsRegion solved
                    pureLookup `shouldBe` Just tyInt
                Solve.SolveError e ->
                    expectationFailure ("solve failed: " ++ e)
