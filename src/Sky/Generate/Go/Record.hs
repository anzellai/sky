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
    , withFuncTypes
    , withInferredSigs
    , withDepFieldIndex
    , withRecordAliases
    , collectRecordAliases
    , withDepArities
    , collectFuncArities
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
    , _cg_recordAliases :: !(Set.Set String)  -- names of all known record aliases
                                              --   (current module + deps) so
                                              --   solvedTypeToGo can suffix "_R"
    , _cg_funcArities :: !(Map.Map String Int)  -- top-level function arities
                                                -- used for partial-application
                                                -- closure synthesis
    , _cg_funcParamTypes :: !(Map.Map String [String])
      -- Per-function Go param types (qualified Go name → [paramType]).
      -- Populated for annotated top-level functions so call-site codegen
      -- can emit `any(arg).(T)` coercions where needed. Functions not
      -- in this map have `any` params and need no coercion.
    , _cg_funcRetType :: !(Map.Map String String)
      -- Per-function Go return type (qualified Go name → retType).
      -- Used by call-site codegen to decide whether further coercion
      -- is needed when the call feeds into a typed context.
    , _cg_funcInferredSigs :: !(Map.Map String ([String], [String], String))
      -- T4b: full HM-inferred signature including Go type parameters
      -- for TVars. Value is (typeParams, paramTypes, returnType).
      -- Populated per-dep from the solver, used by mkDef to emit
      -- generic Go signatures for unannotated polymorphic functions.
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
    , _cg_recordAliases = collectRecordAliases (Can._aliases canMod)
    , _cg_funcArities = collectFuncArities (Can._decls canMod)
    , _cg_funcParamTypes = Map.empty
    , _cg_funcRetType = Map.empty
    , _cg_funcInferredSigs = Map.empty
    }


-- | Collect top-level function arities (param count) so the codegen can
-- synthesize closures for partial applications (`List.filter f xs` where
-- f is a 2-arg function applied to one arg).
collectFuncArities :: Can.Decls -> Map.Map String Int
collectFuncArities = go Map.empty
  where
    go acc Can.SaveTheEnvironment = acc
    go acc (Can.Declare def rest) = go (addDef acc def) rest
    go acc (Can.DeclareRec def defs rest) =
        go (foldr (flip addDef) (addDef acc def) defs) rest

    addDef acc d = case d of
        Can.Def (A.At _ n) ps _          -> Map.insert n (length ps) acc
        Can.TypedDef (A.At _ n) _ ps _ _ -> Map.insert n (length ps) acc
        Can.DestructDef _ _              -> acc


-- | Build a fresh CodegenEnv but with the record-alias set extended.
-- Used by the multi-module path so dep-module record aliases also get the
-- "_R" struct-name suffix in solvedTypeToGo.
withRecordAliases :: Set.Set String -> CodegenEnv -> CodegenEnv
withRecordAliases extra env =
    env { _cg_recordAliases = Set.union extra (_cg_recordAliases env) }


-- | Extend the function-arity map with dep-module qualified names.
withDepArities :: Map.Map String Int -> CodegenEnv -> CodegenEnv
withDepArities extra env =
    env { _cg_funcArities = Map.union extra (_cg_funcArities env) }


-- | Merge per-function param+return type tables into the env. Used
-- after typed dep + entry signatures have been determined so call-site
-- codegen can emit coercions.
withFuncTypes :: Map.Map String [String] -> Map.Map String String -> CodegenEnv -> CodegenEnv
withFuncTypes paramTys retTys env = env
    { _cg_funcParamTypes = Map.union paramTys (_cg_funcParamTypes env)
    , _cg_funcRetType    = Map.union retTys   (_cg_funcRetType env)
    }

-- | Merge per-function inferred signatures (type params + param types
-- + return type) into the env. Used for T4b Go-generics emission.
withInferredSigs :: Map.Map String ([String], [String], String) -> CodegenEnv -> CodegenEnv
withInferredSigs sigs env = env
    { _cg_funcInferredSigs = Map.union sigs (_cg_funcInferredSigs env)
    }


-- | Merge dep-module aliases into the field registry so record
-- literals in the entry module whose field set matches a dep alias
-- resolve to the typed `<Prefix>_<Name>_R` struct rather than
-- degrading to an anonymous struct.
--
-- The caller passes (modPrefix, aliases) pairs. We key the extra
-- registry entries by the module-prefixed name so the struct-literal
-- codegen emits `State_Model_R{...}` instead of `Model_R{...}`.
withDepFieldIndex :: [(String, Map.Map String Can.Alias)] -> CodegenEnv -> CodegenEnv
withDepFieldIndex pairs env =
    let extra = Map.fromList
            [ (Set.fromList fieldNames, prefix ++ "_" ++ aliasName)
            | (prefix, aliases) <- pairs
            , (aliasName, Can.Alias _ body) <- Map.toList aliases
            , Just fieldNames <- [recordFieldNames body]
            ]
    in env { _cg_fieldIndex = Map.union (_cg_fieldIndex env) extra
           -- Also the dep aliases so lookupAlias inside codegen finds
           -- field types for `any(p0).(string)` coercion.
           , _cg_aliases = Map.union
                (Map.fromList
                    [ (prefix ++ "_" ++ aliasName, alias)
                    | (prefix, aliases) <- pairs
                    , (aliasName, alias) <- Map.toList aliases
                    ])
                (_cg_aliases env)
           }


collectRecordAliases :: Map.Map String Can.Alias -> Set.Set String
collectRecordAliases aliases =
    Set.fromList
        [ name
        | (name, Can.Alias _ body) <- Map.toList aliases
        , case body of { T.TRecord _ _ -> True; _ -> False }
        ]


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
        Can.DestructDef _ _           -> acc
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
