-- | Type unification for Sky's Hindley-Milner type inference.
--
-- Derivative work adapted from elm/compiler's @Type.Unify@
-- (Copyright © 2012–present Evan Czaplicki, BSD-3-Clause). See
-- NOTICE.md at the repo root for the full attribution and licence
-- text.
--
-- CPS-based unifier; handles type variables, structures, records,
-- aliases, super types.
module Sky.Type.Unify
    ( unify
    )
    where

import Data.IORef
import qualified Data.Map.Strict as Map
import System.IO.Unsafe (unsafePerformIO)
import qualified Sky.Type.UnionFind as UF
import qualified Sky.Type.Type as T
import qualified Sky.Type.Occurs as Occurs
import qualified Sky.Sky.ModuleName as ModuleName


-- | Monotonic counter for naming the fresh row-extension variable a
-- record merge introduces — must be unique so the merged record reads
-- back OPEN and never collides with an unrelated record's row.
rowExtCounter :: IORef Int
{-# NOINLINE rowExtCounter #-}
rowExtCounter = unsafePerformIO (newIORef 0)

freshRowExtName :: IO String
freshRowExtName = do
    n <- readIORef rowExtCounter
    writeIORef rowExtCounter (n + 1)
    return ("_rowext" ++ show n)


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

        -- FlexVar unifies with anything — but if the OTHER side is a
        -- Structure / Alias whose transitive UF graph contains this
        -- FlexVar's representative, merging would create a self-
        -- referential cycle (infinite type). Reject via occurs check
        -- to prevent the unify / variableToType walk from looping
        -- forever and OOMing the host. Pre-fix bug class: mini-notion
        -- (Sky.Live + Std.Ui + Std.Ui.Events) saw the v0.13 dep-
        -- fixpoint round-1 solve of Std.Ui.Events explode to >3 GB
        -- heap; a structural mistake in Std.Ui's round-0 externals
        -- fed an annotation back into Std.Ui.Events's body and the
        -- FlexVar↔Structure merge built a cycle that later unifies
        -- walked forever. Pair with the path-tracking guard in
        -- `Solve.variableToType` so existing cyclic graphs (built
        -- before this check landed) still read back as a finite
        -- `TVar "_cycle"` sentinel without looping.
        (T.FlexVar _, T.Structure _) -> do
            cyc <- Occurs.occurs v2
            if cyc
                then return False
                else do
                    merge v1 v2 (T._content d2)
                    return True

        (T.Structure _, T.FlexVar _) -> do
            cyc <- Occurs.occurs v1
            if cyc
                then return False
                else do
                    merge v1 v2 (T._content d1)
                    return True

        (T.FlexVar _, T.Alias _ _ _ _) -> do
            cyc <- Occurs.occurs v2
            if cyc
                then return False
                else do
                    merge v1 v2 (T._content d2)
                    return True

        (T.Alias _ _ _ _, T.FlexVar _) -> do
            cyc <- Occurs.occurs v1
            if cyc
                then return False
                else do
                    merge v1 v2 (T._content d1)
                    return True

        -- FlexVar ↔ FlexVar / FlexSuper / RigidVar / RigidSuper /
        -- Error — no cycle possible from a bare variable merge
        -- (neither side carries transitive structure).
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

        -- App1 ↔ Alias with same name + arity: unfold and match.
        -- This case arises with recursive parametric aliases: the
        -- alias body's self-reference is preserved as `Can.TType
        -- name args` (the visited-set guard prevents loop), but
        -- references from OUTSIDE the alias's expansion are wrapped
        -- as `Can.TAlias`.  Unify must bridge these representations
        -- by name, otherwise `mkBranch n cs = { kids = cs }` with
        -- `cs : List (Tree Int)` fails to unify against the field's
        -- declared `List (Tree a)` (a := Int) shape.
        (T.Structure (T.App1 home1 name1 args1),
         T.Alias home2 name2 pairs2 realVar2)
            | (home1 == home2 || nullHome home1 || nullHome home2)
            , name1 == name2
            , length args1 == length pairs2 -> do
                results <- mapM (uncurry unify)
                    (zip args1 (map snd pairs2))
                if and results
                    then do
                        merge v1 v2
                            (T.Alias home2 name2 pairs2 realVar2)
                        return True
                    else return False

        (T.Alias home1 name1 pairs1 realVar1,
         T.Structure (T.App1 home2 name2 args2))
            | (home1 == home2 || nullHome home1 || nullHome home2)
            , name1 == name2
            , length pairs1 == length args2 -> do
                results <- mapM (uncurry unify)
                    (zip (map snd pairs1) args2)
                if and results
                    then do
                        merge v1 v2
                            (T.Alias home1 name1 pairs1 realVar1)
                        return True
                    else return False

        -- Alias: unwrap and unify
        (T.Alias _ _ _ realVar, _) ->
            unify realVar v2

        (_, T.Alias _ _ _ realVar) ->
            unify v1 realVar


-- | Empty-home sentinel test used by both the App1-vs-App1 same-name
-- short-circuit and the App1-vs-Alias same-name path above.  Empty
-- home appears for kernel pseudo-types and TAlias instances whose
-- home was lost during a constraint round trip.
nullHome :: ModuleName.Canonical -> Bool
nullHome h = null (ModuleName.toString h)


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

    -- An OPEN record pattern unifies with an FFI-opaque nominal type.
    -- `Can.Access` lowers `expr.field` to an open-row record
    -- constraint `{ field : a | ρ }`. When `expr` is an FFI-opaque
    -- type (`HttpResponse`, `Route`, `Db`, … — carried with the
    -- empty-home sentinel `Canonical ""`), the field is read via a
    -- generated Go getter, not an HM record projection — so the
    -- opaque type satisfies the open-row pattern structurally without
    -- the unifier needing its field list. The record must be OPEN: a
    -- CLOSED record literal still cannot be passed where an opaque
    -- FFI value is expected. Builtins (Int/String/…) carry a real
    -- `Sky.Core.Basics` home, so `(5).field` is still rejected.
    (T.Record1 _ ext1, T.App1 home _ _)
        | null (ModuleName.toString home) -> do
            closed <- isClosedRecordExt ext1
            if closed
                then return False
                else do merge v1 v2 (T.Structure flat2); return True

    (T.App1 home _ _, T.Record1 _ ext2)
        | null (ModuleName.toString home) -> do
            closed <- isClosedRecordExt ext2
            if closed
                then return False
                else do merge v1 v2 (T.Structure flat1); return True

    _ -> return False  -- incompatible structures


-- | Unify record types.
--
-- Row-polymorphic semantics: a record carries a "row extension"
-- variable. When the extension is bound to `EmptyRecord1` the record
-- is CLOSED — it has exactly the fields listed and rejects any extra.
-- When it's still a FlexVar, the record is OPEN — additional fields
-- are allowed and absorb into the extension.
--
-- Pre-fix bug: the mismatched-fields branch unconditionally merged
-- both field sets under a fresh extension and returned True, even
-- when both records were closed. This silently accepted record
-- literals with completely wrong field names against an explicit
-- record-typed annotation:
--
--     takesRecord : { name : String, count : Int } -> String
--     takesRecord { id = 1, label = "x" }            -- WAS accepted
--
-- The mismatch only surfaced as a runtime panic later
-- (`rt.AsInt: expected numeric value, got <nil>`) when codegen
-- emitted field-by-position access against the annotated record
-- shape. Surfaced from a real-world Std.Ui port (Border.shadow
-- with the wrong record shape passed sky check + sky build, then
-- panicked at runtime).
--
-- Fix: respect the closed/open status of each side. A closed record
-- must NOT have extra fields on the other side; if it does, fail.
-- Both closed → exact field-set match required (already covered by
-- the `Map.null only1 && Map.null only2` branch). One side closed
-- → other side's extra fields are illegal. Both open → row-poly
-- merge as before.
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
            closed1 <- isClosedRecordExt ext1
            closed2 <- isClosedRecordExt ext2
            -- Side N closed disallows extras present only on the
            -- opposite side. Catches `takesRecord { wrong fields }`.
            let extras1Illegal = closed2 && not (Map.null only1)
                extras2Illegal = closed1 && not (Map.null only2)
            if extras1Illegal || extras2Illegal
                then return False
                else if Map.null only1 && Map.null only2
                    then do
                        extOk <- unify ext1 ext2
                        if extOk
                            then do merge v1 v2 (T.Structure (T.Record1 fields1 ext1)); return True
                            else return False
                    else do
                        -- Both open — row-poly merge under a fresh,
                        -- UNIQUELY-NAMED extension.
                        extName <- freshRowExtName
                        newExt <- UF.fresh (T.Descriptor (T.FlexVar (Just extName)) 0 T.noMark Nothing)
                        merge v1 v2 (T.Structure (T.Record1 (Map.union fields1 fields2) newExt))
                        return True


-- | Check whether a row-extension variable is bound to EmptyRecord1
-- (i.e. the record is closed and has no row extension).
isClosedRecordExt :: T.Variable -> IO Bool
isClosedRecordExt v = do
    d <- UF.get v
    case T._content d of
        T.Structure T.EmptyRecord1 -> return True
        _                          -> return False


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
