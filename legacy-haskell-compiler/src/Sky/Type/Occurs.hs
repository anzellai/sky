-- | Occurs check for type inference.
--
-- Derivative work adapted from elm/compiler's @Type.Occurs@
-- (Copyright © 2012–present Evan Czaplicki, BSD-3-Clause). See
-- NOTICE.md at the repo root for the full attribution and licence
-- text.
--
-- Prevents infinite types like `a = List a` by detecting cycles.
module Sky.Type.Occurs
    ( occurs
    )
    where

import qualified Data.Map.Strict as Map
import qualified Sky.Type.UnionFind as UF
import qualified Sky.Type.Type as T


-- | Check if a variable occurs in its own structure (cycle detection).
-- Returns True if a cycle was detected.
occurs :: T.Variable -> IO Bool
occurs var = occursHelp [] var


occursHelp :: [T.Variable] -> T.Variable -> IO Bool
occursHelp seen var = do
    -- Check if we've already visited this variable
    isSeen <- anyEquivalent seen var
    if isSeen
        then return True  -- cycle detected
        else do
            desc <- UF.get var
            case T._content desc of
                T.FlexVar _ ->
                    return False

                T.FlexSuper _ _ ->
                    return False

                T.RigidVar _ ->
                    return False

                T.RigidSuper _ _ ->
                    return False

                T.Structure flatType ->
                    occursInFlatType (var : seen) flatType

                T.Alias _ _ args realVar ->
                    do  argCycles <- mapM (occursHelp (var : seen) . snd) args
                        realCycle <- occursHelp (var : seen) realVar
                        return (or argCycles || realCycle)

                T.Error ->
                    return False


-- | Check if occurs in a flat type structure
occursInFlatType :: [T.Variable] -> T.FlatType -> IO Bool
occursInFlatType seen flatType = case flatType of
    T.App1 _ _ args ->
        anyM (occursHelp seen) args

    T.Fun1 arg result ->
        do  argCycle <- occursHelp seen arg
            resCycle <- occursHelp seen result
            return (argCycle || resCycle)

    T.EmptyRecord1 ->
        return False

    T.Record1 fields ext ->
        do  fieldCycles <- mapM (occursHelp seen) (Map.elems fields)
            extCycle <- occursHelp seen ext
            return (or fieldCycles || extCycle)

    T.Unit1 ->
        return False

    T.Tuple1 a b mc ->
        do  aCycle <- occursHelp seen a
            bCycle <- occursHelp seen b
            cCycle <- case mc of
                Nothing -> return False
                Just c -> occursHelp seen c
            return (aCycle || bCycle || cCycle)


-- | Check if any variable in the list is equivalent to the target
anyEquivalent :: [T.Variable] -> T.Variable -> IO Bool
anyEquivalent [] _ = return False
anyEquivalent (v:vs) target = do
    eq <- UF.equivalent v target
    if eq then return True else anyEquivalent vs target


-- | Monadic any
anyM :: Monad m => (a -> m Bool) -> [a] -> m Bool
anyM _ [] = return False
anyM f (x:xs) = do
    result <- f x
    if result then return True else anyM f xs
