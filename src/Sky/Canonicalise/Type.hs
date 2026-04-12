-- | Canonicalise type annotations — resolve type names.
module Sky.Canonicalise.Type
    ( canonicaliseTypeAnnotation
    , canonicaliseTypeAnnotationWith
    , freeTypeVars
    )
    where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Sky.AST.Source as Src
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName


-- | Canonicalise a source type annotation to a canonical type.
-- Back-compat shim: no cross-module type-name resolution context.
canonicaliseTypeAnnotation :: ModuleName.Canonical -> Src.TypeAnnotation -> Can.Type
canonicaliseTypeAnnotation = canonicaliseTypeAnnotationWith Map.empty


-- | Canonicalise with a type-name → home map. The map lets unqualified
-- type references (e.g. `MyCounter : Counter` where Counter is imported)
-- resolve to the correct home module. Local type declarations should also
-- appear in this map pointing to the current module.
canonicaliseTypeAnnotationWith
    :: Map.Map String ModuleName.Canonical
    -> ModuleName.Canonical
    -> Src.TypeAnnotation
    -> Can.Type
canonicaliseTypeAnnotationWith tmap home srcType = case srcType of
    Src.TLambda from to ->
        Can.TLambda
            (canonicaliseTypeAnnotationWith tmap home from)
            (canonicaliseTypeAnnotationWith tmap home to)

    Src.TVar name ->
        Can.TVar name

    Src.TType modStr segments args ->
        let
            canArgs = map (canonicaliseTypeAnnotationWith tmap home) args
            typeName = last segments
            typeHome = resolveTypeName tmap modStr segments
        in
        Can.TType typeHome typeName canArgs

    Src.TTypeQual qualifier name args ->
        let
            canArgs = map (canonicaliseTypeAnnotationWith tmap home) args
            typeHome = resolveTypeQual qualifier
        in
        Can.TType typeHome name canArgs

    Src.TRecord fields mExt ->
        let
            canFields = Map.fromList $ zipWith (\i (A.At _ name, ty) ->
                (name, Can.FieldType i (canonicaliseTypeAnnotationWith tmap home ty)))
                [0..] fields
        in
        Can.TRecord canFields mExt

    Src.TUnit ->
        Can.TUnit

    Src.TTuple a b rest ->
        Can.TTuple
            (canonicaliseTypeAnnotationWith tmap home a)
            (canonicaliseTypeAnnotationWith tmap home b)
            (case rest of
                [] -> Nothing
                (r:_) -> Just (canonicaliseTypeAnnotationWith tmap home r))


-- | Resolve a type name to its home module.
-- Priority: builtins → qualified module → type-name map → empty (local).
resolveTypeName
    :: Map.Map String ModuleName.Canonical
    -> String
    -> [String]
    -> ModuleName.Canonical
resolveTypeName tmap modStr segments
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
        [name]     -> Map.findWithDefault (ModuleName.Canonical "") name tmap
        _          -> ModuleName.Canonical ""
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
