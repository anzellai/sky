-- | Type unification for Sky's Hindley-Milner type inference.
-- CPS-based unifier adapted from Elm's Type.Unify.
-- Handles: type variables, structures, records, aliases, super types.
module Sky.Type.Unify
    ( unify
    )
    where

import qualified Data.Map.Strict as Map
import qualified Sky.Type.UnionFind as UF
import qualified Sky.Type.Type as T
import qualified Sky.Sky.ModuleName as ModuleName


-- | Unify two type variables. Returns True on success, False on failure.
unify :: T.Variable -> T.Variable -> IO Bool
unify v1 v2 = do
    eq <- UF.equivalent v1 v2
    if eq
        then return True  -- already unified
        else actuallyUnify v1 v2


-- | Perform actual unification between two non-equivalent variables
actuallyUnify :: T.Variable -> T.Variable -> IO Bool
actuallyUnify v1 v2 = do
    d1 <- UF.get v1
    d2 <- UF.get v2
    case (T._content d1, T._content d2) of

        -- FlexVar unifies with anything
        (T.FlexVar _, _) -> do
            merge v1 v2 (T._content d2)
            return True

        (_, T.FlexVar _) -> do
            merge v1 v2 (T._content d1)
            return True

        -- Error suppresses cascading errors
        (T.Error, _) -> do
            merge v1 v2 T.Error
            return True

        (_, T.Error) -> do
            merge v1 v2 T.Error
            return True

        -- FlexSuper unifies with compatible types
        (T.FlexSuper super1 _, T.FlexSuper super2 _) ->
            case combineSuper super1 super2 of
                Just combined -> do
                    merge v1 v2 (T.FlexSuper combined Nothing)
                    return True
                Nothing -> return False

        (T.FlexSuper super _, T.Structure flat) ->
            if superMatches super flat
                then do merge v1 v2 (T._content d2); return True
                else return False

        (T.Structure flat, T.FlexSuper super _) ->
            if superMatches super flat
                then do merge v1 v2 (T._content d1); return True
                else return False

        -- RigidVar can only unify with FlexVar (handled above) or Error
        (T.RigidVar _, _) -> return False
        (_, T.RigidVar _) -> return False

        (T.RigidSuper _ _, _) -> return False
        (_, T.RigidSuper _ _) -> return False

        -- Structure-Structure: structural unification
        (T.Structure flat1, T.Structure flat2) ->
            unifyStructure v1 v2 flat1 flat2

        -- Alias: unwrap and unify
        (T.Alias _ _ _ realVar, _) ->
            unify realVar v2

        (_, T.Alias _ _ _ realVar) ->
            unify v1 realVar


-- | Unify two type structures
unifyStructure :: T.Variable -> T.Variable -> T.FlatType -> T.FlatType -> IO Bool
unifyStructure v1 v2 flat1 flat2 = case (flat1, flat2) of

    (T.App1 home1 name1 args1, T.App1 home2 name2 args2) ->
        -- Homes agree when they match exactly, OR when either side is
        -- the sentinel `Canonical ""`. The empty-home form is used by
        -- kernel type signatures for types that have no real Sky module
        -- (e.g. `Db.Db`, `VNode`, `Route`) — the canonicaliser resolves
        -- the user's short alias (`Db.Db` under `import Std.Db as Db`)
        -- to `Canonical "Db"` via `resolveTypeQual`, so without this
        -- relaxation a `Maybe Db.Db` field fails to unify with the
        -- `Db` that `Db.connect` actually returns (empty home).
        -- Same-name short-circuit: if the name already equals a kernel
        -- type name, prefer compatibility over strict equality.
        let emptyCan = ModuleName.Canonical ""
            homesAgree = home1 == home2 || home1 == emptyCan || home2 == emptyCan
        in if homesAgree && name1 == name2 && length args1 == length args2
            then do
                results <- mapM (uncurry unify) (zip args1 args2)
                if and results
                    then do merge v1 v2 (T.Structure flat1); return True
                    else return False
            else return False

    (T.Fun1 arg1 res1, T.Fun1 arg2 res2) ->
        do  argOk <- unify arg1 arg2
            resOk <- unify res1 res2
            if argOk && resOk
                then do merge v1 v2 (T.Structure flat1); return True
                else return False

    (T.Unit1, T.Unit1) ->
        do merge v1 v2 (T.Structure T.Unit1); return True

    (T.Tuple1 a1 b1 mc1, T.Tuple1 a2 b2 mc2) ->
        do  aOk <- unify a1 a2
            bOk <- unify b1 b2
            cOk <- case (mc1, mc2) of
                (Nothing, Nothing) -> return True
                (Just c1, Just c2) -> unify c1 c2
                _ -> return False
            if aOk && bOk && cOk
                then do merge v1 v2 (T.Structure flat1); return True
                else return False

    (T.EmptyRecord1, T.EmptyRecord1) ->
        do merge v1 v2 (T.Structure T.EmptyRecord1); return True

    (T.Record1 fields1 ext1, T.Record1 fields2 ext2) ->
        unifyRecords v1 v2 fields1 ext1 fields2 ext2

    _ -> return False  -- incompatible structures


-- | Unify record types
unifyRecords :: T.Variable -> T.Variable
    -> Map.Map String T.Variable -> T.Variable
    -> Map.Map String T.Variable -> T.Variable
    -> IO Bool
unifyRecords v1 v2 fields1 ext1 fields2 ext2 = do
    let shared = Map.intersectionWith (,) fields1 fields2
        only1 = Map.difference fields1 fields2
        only2 = Map.difference fields2 fields1

    -- Unify shared fields
    sharedOk <- mapM (uncurry unify) (Map.elems shared)
    if not (and sharedOk)
        then return False
        else do
            if Map.null only1 && Map.null only2
                then do
                    extOk <- unify ext1 ext2
                    if extOk
                        then do merge v1 v2 (T.Structure (T.Record1 fields1 ext1)); return True
                        else return False
                else do
                    -- Create fresh extension and unify
                    newExt <- UF.fresh (T.Descriptor (T.FlexVar Nothing) 0 T.noMark Nothing)
                    merge v1 v2 (T.Structure (T.Record1 (Map.union fields1 fields2) newExt))
                    return True


-- ═══════════════════════════════════════════════════════════
-- HELPERS
-- ═══════════════════════════════════════════════════════════

-- | Merge two variables under a single representative with new content
merge :: T.Variable -> T.Variable -> T.Content -> IO ()
merge v1 v2 content = do
    d1 <- UF.get v1
    d2 <- UF.get v2
    let newRank = min (T._rank d1) (T._rank d2)
    UF.union v1 v2 (T.Descriptor content newRank T.noMark Nothing)


-- | Check if a super type constraint is satisfied by a flat type
superMatches :: T.SuperType -> T.FlatType -> Bool
superMatches super flat = case (super, flat) of
    (T.Number, T.App1 home "Int" [])   | isBasics home -> True
    (T.Number, T.App1 home "Float" []) | isBasics home -> True
    (T.Comparable, T.App1 home "Int" [])    | isBasics home -> True
    (T.Comparable, T.App1 home "Float" [])  | isBasics home -> True
    (T.Comparable, T.App1 home "String" []) | isBasics home -> True
    (T.Comparable, T.App1 home "Char" [])   | isBasics home -> True
    (T.Appendable, T.App1 home "String" []) | isBasics home -> True
    (T.Appendable, T.App1 _ "List" _)  -> True
    (T.CompAppend, T.App1 home "String" []) | isBasics home -> True
    _ -> False
  where
    isBasics = ModuleName.isSkyCore


-- | Combine two super type constraints
combineSuper :: T.SuperType -> T.SuperType -> Maybe T.SuperType
combineSuper s1 s2
    | s1 == s2 = Just s1
    | otherwise = case (s1, s2) of
        (T.Number, T.Comparable) -> Just T.Number
        (T.Comparable, T.Number) -> Just T.Number
        (T.Appendable, T.Comparable) -> Just T.CompAppend
        (T.Comparable, T.Appendable) -> Just T.CompAppend
        _ -> Nothing
