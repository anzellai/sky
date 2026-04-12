-- | Canonicalise type annotations — resolve type names.
module Sky.Canonicalise.Type
    ( canonicaliseTypeAnnotation
    , freeTypeVars
    )
    where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Sky.AST.Source as Src
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName


-- | Canonicalise a source type annotation to a canonical type
canonicaliseTypeAnnotation :: ModuleName.Canonical -> Src.TypeAnnotation -> Can.Type
canonicaliseTypeAnnotation home srcType = case srcType of
    Src.TLambda from to ->
        Can.TLambda
            (canonicaliseTypeAnnotation home from)
            (canonicaliseTypeAnnotation home to)

    Src.TVar name ->
        Can.TVar name

    Src.TType modStr segments args ->
        let
            canArgs = map (canonicaliseTypeAnnotation home) args
            typeName = last segments
            typeHome = resolveTypeName modStr segments
        in
        Can.TType typeHome typeName canArgs

    Src.TTypeQual qualifier name args ->
        let
            canArgs = map (canonicaliseTypeAnnotation home) args
            typeHome = resolveTypeQual qualifier
        in
        Can.TType typeHome name canArgs

    Src.TRecord fields mExt ->
        let
            canFields = Map.fromList $ zipWith (\i (A.At _ name, ty) ->
                (name, Can.FieldType i (canonicaliseTypeAnnotation home ty)))
                [0..] fields
        in
        Can.TRecord canFields mExt

    Src.TUnit ->
        Can.TUnit

    Src.TTuple a b rest ->
        Can.TTuple
            (canonicaliseTypeAnnotation home a)
            (canonicaliseTypeAnnotation home b)
            (case rest of
                [] -> Nothing
                (r:_) -> Just (canonicaliseTypeAnnotation home r))


-- | Resolve a type name to its home module
resolveTypeName :: String -> [String] -> ModuleName.Canonical
resolveTypeName modStr segments
    -- If modStr is empty, the type is either a builtin or in the current scope
    | null modStr = case segments of
        ["Int"]    -> ModuleName.basics
        ["Float"]  -> ModuleName.basics
        ["Bool"]   -> ModuleName.basics
        ["String"] -> ModuleName.basics
        ["Char"]   -> ModuleName.basics
        ["List"]   -> ModuleName.list
        ["Maybe"]  -> ModuleName.maybe_
        ["Result"] -> ModuleName.result_
        ["Task"]   -> ModuleName.task
        _          -> ModuleName.Canonical ""  -- local type
    -- Otherwise the type has a module qualifier
    | otherwise = ModuleName.Canonical modStr


-- | Resolve a qualified type name
resolveTypeQual :: String -> ModuleName.Canonical
resolveTypeQual qualifier = case qualifier of
    "List"   -> ModuleName.list
    "Maybe"  -> ModuleName.maybe_
    "Result" -> ModuleName.result_
    "Task"   -> ModuleName.task
    "Dict"   -> ModuleName.dict
    "Set"    -> ModuleName.set
    _        -> ModuleName.Canonical qualifier


-- | Extract free type variables from a source type annotation
freeTypeVars :: Src.TypeAnnotation -> [(String, ())]
freeTypeVars srcType =
    map (\v -> (v, ())) $ Set.toList $ collectVars srcType
  where
    collectVars :: Src.TypeAnnotation -> Set.Set String
    collectVars t = case t of
        Src.TVar name -> Set.singleton name
        Src.TLambda from to -> collectVars from `Set.union` collectVars to
        Src.TType _ _ args -> Set.unions (map collectVars args)
        Src.TTypeQual _ _ args -> Set.unions (map collectVars args)
        Src.TRecord fields _ -> Set.unions (map (\(_, ty) -> collectVars ty) fields)
        Src.TUnit -> Set.empty
        Src.TTuple a b rest -> collectVars a `Set.union` collectVars b `Set.union` Set.unions (map collectVars rest)
