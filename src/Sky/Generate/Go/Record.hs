-- | Record type registry and classification.
-- Maps Sky record type aliases to Go struct/interface declarations.
-- Provides field-set lookup for matching record literals to their alias names.
module Sky.Generate.Go.Record
    ( RecordRegistry
    , CodegenEnv(..)
    , AliasKind(..)
    , buildRegistry
    , lookupRecordAlias
    , classifyAlias
    , buildCodegenEnv
    )
    where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Type.Type as T
import qualified Sky.Type.Solve as Solve


-- | Maps a sorted set of field names to the alias name
type RecordRegistry = Map.Map (Set.Set String) String


-- | Codegen environment threaded through all expression codegen
data CodegenEnv = CodegenEnv
    { _cg_solvedTypes :: !Solve.SolvedTypes
    , _cg_aliases     :: !(Map.Map String Can.Alias)
    , _cg_fieldIndex  :: !RecordRegistry
    , _cg_zeroArgs    :: !(Set.Set String)  -- top-level names defined with zero params
    }


-- | Classification of a type alias
data AliasKind
    = DataRecord [(String, T.Type)]       -- all fields are data → Go struct
    | BehaviourRecord [(String, T.Type)]  -- has function fields → Go interface
    | NonRecordAlias T.Type               -- not a record type
    deriving (Show)


-- | Build the record registry from module aliases
buildRegistry :: Map.Map String Can.Alias -> RecordRegistry
buildRegistry aliases =
    Map.fromList
        [ (Set.fromList fieldNames, aliasName)
        | (aliasName, Can.Alias _ body) <- Map.toList aliases
        , Just fieldNames <- [recordFieldNames body]
        ]


-- | Build a CodegenEnv from solved types and module info
buildCodegenEnv :: Solve.SolvedTypes -> Can.Module -> CodegenEnv
buildCodegenEnv solvedTypes canMod = CodegenEnv
    { _cg_solvedTypes = solvedTypes
    , _cg_aliases = Can._aliases canMod
    , _cg_fieldIndex = buildRegistry (Can._aliases canMod)
    , _cg_zeroArgs = collectZeroArgs (Can._decls canMod)
    }


-- | Collect names of zero-parameter top-level definitions.
-- These must be called with () at reference sites in Go, since we codegen them as `func name() any`.
collectZeroArgs :: Can.Decls -> Set.Set String
collectZeroArgs = go Set.empty
  where
    go acc Can.SaveTheEnvironment = acc
    go acc (Can.Declare def rest) = go (addDef acc def) rest
    go acc (Can.DeclareRec def defs rest) =
        go (foldr (flip addDef) (addDef acc def) defs) rest

    addDef acc d = case d of
        Can.Def locName [] _          -> Set.insert (A.toValue locName) acc
        Can.TypedDef locName _ [] _ _ -> Set.insert (A.toValue locName) acc
        _                             -> acc


-- | Look up a record alias name by field names
lookupRecordAlias :: RecordRegistry -> [String] -> Maybe String
lookupRecordAlias registry fieldNames =
    Map.lookup (Set.fromList fieldNames) registry


-- | Classify a type alias as data record, behaviour record, or non-record
classifyAlias :: Can.Alias -> AliasKind
classifyAlias (Can.Alias _ body) = case body of
    T.TRecord fields _ ->
        let fieldList = map (\(name, T.FieldType _ ty) -> (name, ty)) (Map.toList fields)
            hasFuncField = any (\(_, ty) -> isFuncType ty) fieldList
        in if hasFuncField
            then BehaviourRecord fieldList
            else DataRecord fieldList
    other ->
        NonRecordAlias other


-- | Extract field names from a record type (Nothing if not a record)
recordFieldNames :: T.Type -> Maybe [String]
recordFieldNames (T.TRecord fields _) = Just (Map.keys fields)
recordFieldNames _ = Nothing


-- | Check if a type is a function type
isFuncType :: T.Type -> Bool
isFuncType (T.TLambda _ _) = True
isFuncType _ = False
