-- | Type instantiation — creates fresh copies of quantified types.
--
-- Derivative work adapted from elm/compiler's @Type.Instantiate@
-- (Copyright © 2012–present Evan Czaplicki, BSD-3-Clause). See
-- NOTICE.md at the repo root for the full attribution and licence
-- text.
--
-- When using a polymorphic value, its type variables are replaced
-- with fresh unification variables.
module Sky.Type.Instantiate
    ( fromCanType
    , fromAnnotation
    )
    where

import qualified Data.Map.Strict as Map
import qualified Sky.Type.UnionFind as UF
import qualified Sky.Type.Type as T
import qualified Sky.Sky.ModuleName as ModuleName


-- | Convert a Type.Type to a solver variable.
-- Creates fresh variables for all type vars.
fromCanType :: Int -> T.Type -> IO T.Variable
fromCanType rank canType = do
    env <- buildEnv rank canType Map.empty
    typeToVariable rank env canType


-- | Instantiate an annotation (Forall vars type) into a solver variable.
-- Each quantified variable gets a fresh flex variable.
fromAnnotation :: Int -> T.Annotation -> IO (T.Variable, [T.Variable])
fromAnnotation rank (T.Forall freeVars canType) = do
    -- Create fresh flex variables for each quantified variable
    freshVars <- mapM (\name -> do
        v <- UF.fresh (T.Descriptor (T.FlexVar (Just name)) rank T.noMark Nothing)
        return (name, v)) freeVars
    let env = Map.fromList freshVars
    var <- typeToVariable rank env canType
    return (var, map snd freshVars)


-- | Build an environment of fresh variables for all free type vars in a type
buildEnv :: Int -> T.Type -> Map.Map String T.Variable -> IO (Map.Map String T.Variable)
buildEnv rank canType env = case canType of
    T.TVar name ->
        case Map.lookup name env of
            Just _ -> return env
            Nothing -> do
                v <- UF.fresh (T.Descriptor (T.FlexVar (Just name)) rank T.noMark Nothing)
                return (Map.insert name v env)

    T.TLambda from to ->
        buildEnv rank from env >>= buildEnv rank to

    T.TType _ _ args ->
        foldlM (\e arg -> buildEnv rank arg e) env args

    T.TRecord fields _ ->
        foldlM (\e (T.FieldType _ ty) -> buildEnv rank ty e) env (Map.elems fields)

    T.TUnit -> return env

    T.TTuple a b more -> do
        e0 <- buildEnv rank a env
        e1 <- buildEnv rank b e0
        foldlM (\e ty -> buildEnv rank ty e) e1 more

    T.TAlias _ _ pairs _ ->
        foldlM (\e (_, ty) -> buildEnv rank ty e) env pairs


-- | Convert a canonical type to a solver variable using the environment
typeToVariable :: Int -> Map.Map String T.Variable -> T.Type -> IO T.Variable
typeToVariable rank env canType = case canType of
    T.TVar name ->
        case Map.lookup name env of
            Just v -> return v
            Nothing -> UF.fresh (T.Descriptor (T.FlexVar (Just name)) rank T.noMark Nothing)

    T.TLambda from to -> do
        fromVar <- typeToVariable rank env from
        toVar <- typeToVariable rank env to
        UF.fresh (T.Descriptor (T.Structure (T.Fun1 fromVar toVar)) rank T.noMark Nothing)

    T.TType home name args -> do
        argVars <- mapM (typeToVariable rank env) args
        UF.fresh (T.Descriptor (T.Structure (T.App1 home name argVars)) rank T.noMark Nothing)

    T.TRecord fields mExt -> do
        fieldVars <- Map.traverseWithKey (\_ (T.FieldType _ ty) ->
            typeToVariable rank env ty) fields
        extVar <- case mExt of
            Nothing -> UF.fresh (T.Descriptor (T.Structure T.EmptyRecord1) rank T.noMark Nothing)
            Just name ->
                case Map.lookup name env of
                    Just v -> return v
                    Nothing -> UF.fresh (T.Descriptor (T.FlexVar (Just name)) rank T.noMark Nothing)
        UF.fresh (T.Descriptor (T.Structure (T.Record1 fieldVars extVar)) rank T.noMark Nothing)

    T.TUnit ->
        UF.fresh (T.Descriptor (T.Structure T.Unit1) rank T.noMark Nothing)

    T.TTuple a b more -> do
        aVar <- typeToVariable rank env a
        bVar <- typeToVariable rank env b
        mcVar <- case more of
            []      -> return Nothing
            (c : _) -> Just <$> typeToVariable rank env c
        UF.fresh (T.Descriptor (T.Structure (T.Tuple1 aVar bVar mcVar)) rank T.noMark Nothing)

    T.TAlias home name pairs aliasType -> do
        pairVars <- mapM (\(n, ty) -> do
            v <- typeToVariable rank env ty
            return (n, v)) pairs
        innerVar <- case aliasType of
            T.Hoisted inner -> typeToVariable rank env inner
            T.Filled inner -> typeToVariable rank env inner
        UF.fresh (T.Descriptor (T.Alias home name pairVars innerVar) rank T.noMark Nothing)


-- Helpers

foldlM :: Monad m => (b -> a -> m b) -> b -> [a] -> m b
foldlM _ acc [] = return acc
foldlM f acc (x:xs) = f acc x >>= \acc' -> foldlM f acc' xs
