-- | Canonicalise type annotations — resolve type names.
module Sky.Canonicalise.Type
    ( canonicaliseTypeAnnotation
    , canonicaliseTypeAnnotationWith
    , canonicaliseTypeAnnotationWithAliases
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
canonicaliseTypeAnnotationWith tmap home srcType =
    canonicaliseTypeAnnotationWithAliases tmap Map.empty home srcType


-- | Like `canonicaliseTypeAnnotationWith` but also takes an
-- `aliasMap : alias-segment → full module name` so qualified type
-- annotations like `Ui.Color` (under `import Std.Ui as Ui`) resolve
-- to the dep's full home (`Std.Ui`) rather than a literal `Canonical
-- "Ui"`. Without this, `Ui.Color` and bare `Color` (resolved via the
-- `tmap` import-exposing path) get different homes and HM rejects
-- them as different types with the cryptic
-- `Type mismatch: Color vs Color` (same display, different identity).
canonicaliseTypeAnnotationWithAliases
    :: Map.Map String ModuleName.Canonical
    -> Map.Map String ModuleName.Canonical
    -> ModuleName.Canonical
    -> Src.TypeAnnotation
    -> Can.Type
canonicaliseTypeAnnotationWithAliases tmap aliasMap home srcType = case srcType of
    Src.TLambda from to ->
        Can.TLambda
            (recur from)
            (recur to)

    Src.TVar name ->
        Can.TVar name

    Src.TType modStr segments args ->
        let
            canArgs = map recur args
            typeName = last segments
            typeHome = resolveTypeName tmap modStr segments
        in
        Can.TType typeHome typeName canArgs

    Src.TTypeQual qualifier name args ->
        let
            canArgs = map recur args
            typeHome = resolveTypeQualWith aliasMap qualifier
        in
        Can.TType typeHome name canArgs

    Src.TRecord fields mExt ->
        let
            canFields = Map.fromList $ zipWith (\i (A.At _ n, ty) ->
                (n, Can.FieldType i (recur ty)))
                [0..] fields
        in
        Can.TRecord canFields mExt

    Src.TUnit ->
        Can.TUnit

    Src.TTuple a b rest ->
        Can.TTuple
            (recur a)
            (recur b)
            (map recur rest)
  where
    recur = canonicaliseTypeAnnotationWithAliases tmap aliasMap home


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
        ["Dict"]   -> ModuleName.dict
        ["Set"]    -> ModuleName.set
        [name]     -> Map.findWithDefault (ModuleName.Canonical "") name tmap
        _          -> ModuleName.Canonical ""
    | otherwise = ModuleName.Canonical modStr


-- | Resolve a qualified type name
resolveTypeQual :: String -> ModuleName.Canonical
resolveTypeQual = resolveTypeQualWith Map.empty


-- | Resolve a qualified type name, consulting an `import M as Alias`
-- alias map first. Built-in module qualifiers (List/Maybe/Result/Task/
-- Dict/Set) keep their hardcoded canonical forms; user `import M as A`
-- aliases resolve A → M; everything else falls through to the literal
-- qualifier as a Canonical name (back-compat for kernel modules
-- referenced via their canonical short name).
resolveTypeQualWith
    :: Map.Map String ModuleName.Canonical
    -> String
    -> ModuleName.Canonical
resolveTypeQualWith aliasMap qualifier = case qualifier of
    "List"   -> ModuleName.list
    "Maybe"  -> ModuleName.maybe_
    "Result" -> ModuleName.result_
    "Task"   -> ModuleName.task
    "Dict"   -> ModuleName.dict
    "Set"    -> ModuleName.set
    _        -> Map.findWithDefault
                    (ModuleName.Canonical qualifier)
                    qualifier
                    aliasMap


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
