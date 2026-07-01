-- | Record type registry and classification.
-- Maps Sky record type aliases to Go struct/interface declarations.
-- Provides field-set lookup for matching record literals to their alias names.
module Sky.Generate.Go.Record
    ( RecordRegistry
    , CodegenEnv(..)
    , AliasKind(..)
    , buildRegistry
    , buildDepFieldIndex
    , lookupRecordAlias
    , classifyAlias
    , buildCodegenEnv
    , withFuncTypes
    , withFuncUltimateRetTypes
    , withInferredSigs
    , withDepFieldIndex
    , withRecordAliases
    , withUnionNames
    , withUnionDetails
    , withSealedIfaceNames
    , withEnumNames
    , withCallSiteInstances
    , withFuncSkyToGoTVars
    , collectRecordAliases
    , withDepArities
    , collectFuncArities
    )
    where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Data.List as List
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName
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
    , _cg_unionNames    :: !(Set.Set String)  -- module-prefixed union/ADT names
                                              --   that have a `type X = rt.SkyADT`
                                              --   alias emitted somewhere. Used
                                              --   by safeReturnType to fall back
                                              --   to `any` for FFI-opaque type
                                              --   refs that don't correspond to
                                              --   any emitted Go type alias.
    , _cg_unionDetails  :: !(Map.Map String (ModuleName.Canonical, Can.CtorOpts, [String], [Can.Ctor]))
      -- ^ v0.17 P3.4c.0 — per-union metadata for the sealed-iface
      -- emission gate.  Keyed by the SAME convention as
      -- '_cg_unionNames': entry-module unions are keyed by the bare
      -- type name ("Color"); dep-module unions by the prefixed name
      -- ("Sky_Core_Error_Error").  Value: the originating
      -- 'ModuleName.Canonical' + 'Can.CtorOpts' + type-variable
      -- names + constructor list.  Consumed by the carve-out
      -- predicate 'shouldEmitSealedIface' and the subject-inference
      -- helper 'subjectIsSealedIface' (both NOT WIRED yet — P3.4c.1).
      --
      -- Population: entry module populated from 'Can._unions canMod'
      -- in 'buildCodegenEnv'.  Dep modules populated by 'withUnionDetails'
      -- at the per-dep emission point (mirrors 'withUnionNames').
      -- Cross-spec invariant: every entry in '_cg_unionNames' has a
      -- matching entry here, and vice versa.
    , _cg_enumNames     :: !(Set.Set String)  -- v0.13 typed lowerer: union
                                              --   names whose Sky declaration is
                                              --   a pure enum (all nullary
                                              --   constructors).  These emit as
                                              --   `type X = int` (NOT `= rt.SkyADT`),
                                              --   so their zero value is `0`, not
                                              --   `X{}`.  `goZeroValue` needs this
                                              --   to type an enum-returning IIFE.
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
      --
      -- IMPORTANT semantic: this map stores the AFTER-ONE-ARG-APPLIED
      -- type (single TLambda strip via `safeReturnType`). For
      -- partial-app wrapper codegen that needs the ULTIMATE scalar
      -- return type (after ALL args applied), use
      -- `_cg_funcUltimateRetType` instead.
    , _cg_funcUltimateRetType :: !(Map.Map String String)
      -- v0.13 Stage 2 — typed partial application. Per-function
      -- Go return type after ALL args applied (recursive TLambda
      -- strip). Used by `emitPartialUserCall` to type the innermost
      -- wrapper's return value correctly.
      --
      -- Populated everywhere `_cg_funcRetType` is populated, using
      -- `ultimateReturnType` (strips every nested TLambda level
      -- before solvedTypeToGo runs).
    , _cg_funcInferredSigs :: !(Map.Map String ([String], [String], String))
      -- T4b: full HM-inferred signature including Go type parameters
      -- for TVars. Value is (typeParams, paramTypes, returnType).
      -- Populated per-dep from the solver, used by mkDef to emit
      -- generic Go signatures for unannotated polymorphic functions.
    , _cg_callSiteInstances :: !(Map.Map (String, Int, Int) (Map.Map String Solve.CallInstance))
      -- v0.13 Phase A5: at each polymorphic call site, the captured
      -- instance gives the call's concrete type-args.  Codegen at
      -- `Can.VarTopLevel` / `Can.VarKernel` consults this map to
      -- pick the right generic instantiation.
      --
      -- v0.17 C24 — key widened from (line, col) to
      -- (modName, line, col) where modName is the qualified Sky
      -- module that hosts the call ("" for the entry module).
      -- Closes cross-file collision: two distinct dep modules with
      -- calls at the same (line, col) used to collide and silently
      -- drop instances → `any`-widened args; now they're keyed
      -- separately and each emits the right specialised name.
      -- Read sites derive `modName` from `globalCurrentDepModule`
      -- (Just m → m, Nothing → "" for entry).
    , _cg_funcSkyToGoTVars :: !(Map.Map String [(String, String)])
      -- v0.13 Phase A5+: per-function mapping from annotation Sky-
      -- TVar names (e.g. "a", "e") to the emitted Go-generic names
      -- (e.g. "T1", "T2") that survived codegen-time defaulting.
      -- Used by `coerceCallArgsAt` to build the substitution map in
      -- Sky-name space — necessary because `CallInstance` records
      -- carry one entry per annotation Forall var (e in `Result e
      -- a` included) but `_cg_funcInferredSigs.tps` only carries
      -- the survivors (e collapses to `Sky_Core_Error_Error` at
      -- codegen).  Annotation positions absent from this map have
      -- already been baked into the Go sig as concrete types.
    , _cg_sealedIfaceNames :: !(Set.Set String)
      -- ^ v0.17 iter 60 — qualified Go names of unions whose
      -- 'shouldEmitSealedIface' gate returns True.  Keyed by the
      -- SAME convention as '_cg_unionNames' (entry-module = bare
      -- type name; dep-module = @<prefix>_<typeName>@).  Consumed
      -- by 'goZeroValue' (Compile.hs ~line 7796): the union arm's
      -- @T{}@ literal is invalid Go on an interface type, so a
      -- name in this set MUST emit @nil@ instead.
      --
      -- Population: derived at 'buildCodegenEnv' from the entry
      -- module's '_cg_unionDetails' via 'shouldEmitSealedIface'
      -- filter; extended per-dep at every 'withUnionDetails' call
      -- site via 'withSealedIfaceNames'.  Empty under P3.3 default
      -- ('shouldEmitSealedIface' returns False everywhere) — byte
      -- identity preserved.
      --
      -- Closes the architectural pre-condition for the first
      -- sealed-iface flip identified by dual-grill iter 59 (Griller
      -- A vector V5 / V7) on examples/00-standard-libs's 14 sites
      -- of @return Sky_Test_TestResult{}@.
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


-- | Build a dep-module field-index registry keyed by the prefixed alias
-- name (e.g. "Lib_Db" + "Config" → "Lib_Db_Config") so signature codegen
-- can resolve record literals from imported modules to their `_R` struct.
buildDepFieldIndex :: [(String, Map.Map String Can.Alias)] -> RecordRegistry
buildDepFieldIndex pairs =
    Map.fromList
        [ (Set.fromList fieldNames, prefix ++ "_" ++ aliasName)
        | (prefix, aliases) <- pairs
        , (aliasName, Can.Alias _ body) <- Map.toList aliases
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
    , _cg_unionNames = Set.fromList (Map.keys (Can._unions canMod))
    , _cg_unionDetails = Map.fromList
        [ ( uname
          , ( Can._name canMod
            , Can._u_opts u
            , Can._u_vars u
            , Can._u_alts u
            )
          )
        | (uname, u) <- Map.toList (Can._unions canMod)
        ]
    , _cg_enumNames = Set.fromList
        [ uname
        | (uname, u) <- Map.toList (Can._unions canMod)
        , Can._u_opts u == Can.Enum
        ]
    , _cg_funcArities = collectFuncArities (Can._decls canMod)
    , _cg_funcParamTypes = Map.empty
    , _cg_funcRetType = Map.empty
    , _cg_funcUltimateRetType = Map.empty
    , _cg_funcInferredSigs = Map.empty
    , _cg_callSiteInstances = Map.empty
    , _cg_funcSkyToGoTVars = Map.empty
    , _cg_sealedIfaceNames = Set.empty
      -- ^ v0.17 iter 60 — left empty here; caller (Compile.hs)
      -- augments via 'withSealedIfaceNames' after consulting
      -- 'shouldEmitSealedIface' (which lives in Compile.hs to avoid
      -- a Record→Compile import cycle).  Same pattern as the empty
      -- '_cg_funcInferredSigs' etc. that Compile.hs populates
      -- post-construction.
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


-- | Extend the union-name set with dep-module qualified union names.
-- Used so safeReturnType can recognise types like "Sky_Core_Error_Error"
-- as Sky-defined ADTs (which have `type X = rt.SkyADT` aliases emitted)
-- vs FFI-opaque names like "Bufio_Scanner" (no Go alias, falls back to any).
withUnionNames :: Set.Set String -> CodegenEnv -> CodegenEnv
withUnionNames extra env =
    env { _cg_unionNames = Set.union extra (_cg_unionNames env) }


-- | v0.13 typed lowerer: extend the enum-name set with dep-module
-- qualified enum-union names.  Mirrors `withUnionNames` — kept
-- separate because enum-vs-tagged is a distinct property
-- (`type X = int` vs `type X = rt.SkyADT`).
withEnumNames :: Set.Set String -> CodegenEnv -> CodegenEnv
withEnumNames extra env =
    env { _cg_enumNames = Set.union extra (_cg_enumNames env) }


-- | v0.17 P3.4c.0 — extend the per-union metadata map with dep-module
-- entries.  Mirror of 'withUnionNames' for the sealed-iface gate's
-- metadata channel.  Caller derives the keyed entries (entry-keyed
-- by bare type name; dep-keyed by @\<prefix\>_\<typeName\>@) so the
-- key convention matches '_cg_unionNames' / '_cg_enumNames' exactly.
--
-- Population at the same per-dep call site as 'withUnionNames'.
-- Value carries the originating 'ModuleName.Canonical' so consumers
-- can rebuild the full qualified Go name without round-tripping
-- through a name parser.  Pure no-op until 'shouldEmitSealedIface'
-- / 'subjectIsSealedIface' read it (P3.4c.1 onward).
withUnionDetails
    :: Map.Map String (ModuleName.Canonical, Can.CtorOpts, [String], [Can.Ctor])
    -> CodegenEnv
    -> CodegenEnv
withUnionDetails extra env =
    env { _cg_unionDetails = Map.union extra (_cg_unionDetails env) }


-- | v0.17 iter 60 — extend the sealed-iface name set with dep-module
-- qualified names whose 'shouldEmitSealedIface' gate returns True.
-- Mirror of 'withUnionDetails' / 'withUnionNames' — called at the
-- same per-dep emission install site so 'goZeroValue' sees the
-- updated set when emitting IIFE fall-throughs for code that
-- references a dep-module sealed-iface ADT.
withSealedIfaceNames :: Set.Set String -> CodegenEnv -> CodegenEnv
withSealedIfaceNames extra env =
    env { _cg_sealedIfaceNames = Set.union extra (_cg_sealedIfaceNames env) }


-- | v0.13 Phase A5: install the captured call-site instance map.
-- Each entry maps a source `(file, line, col)` triple to the
-- `CallInstance` recorded by the solver at that site.  Codegen
-- consults this map when emitting `Can.Call` nodes to pick the
-- right generic instantiation (concrete types vs `any`).
withCallSiteInstances
    :: Map.Map (String, Int, Int) (Map.Map String Solve.CallInstance)
    -> CodegenEnv -> CodegenEnv
withCallSiteInstances csi env =
    env { _cg_callSiteInstances = csi }


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

-- | v0.13 Stage 2 — merge the ultimate (after-all-args-applied)
-- return-type table into the env. Called alongside `withFuncTypes`.
withFuncUltimateRetTypes :: Map.Map String String -> CodegenEnv -> CodegenEnv
withFuncUltimateRetTypes m env = env
    { _cg_funcUltimateRetType = Map.union m (_cg_funcUltimateRetType env) }

-- | Merge per-function inferred signatures (type params + param types
-- + return type) into the env. Used for T4b Go-generics emission.
withInferredSigs :: Map.Map String ([String], [String], String) -> CodegenEnv -> CodegenEnv
withInferredSigs sigs env = env
    { _cg_funcInferredSigs = Map.union sigs (_cg_funcInferredSigs env)
    }


-- | v0.13 Phase A5+: register the Sky-TVar → Go-TVar mapping for
-- each polymorphic function in the env.  Drives `coerceCallArgsAt`
-- so the call-site substitution map is keyed by SKY names (matching
-- the `CallInstance.quantifiers` carried by the solver) and projected
-- to Go names that the param-type strings actually use.
withFuncSkyToGoTVars :: Map.Map String [(String, String)] -> CodegenEnv -> CodegenEnv
withFuncSkyToGoTVars m env = env
    { _cg_funcSkyToGoTVars = Map.union m (_cg_funcSkyToGoTVars env)
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


-- | Look up a record alias name by field names.
--
-- v0.13 A1: superset match for open records. The HM solver emits
-- `T.TRecord fields (Just rowExt)` for any function that only
-- accesses a SUBSET of a record's fields (e.g. `\m -> m.count`
-- constrains `m : {count : Int | ρ}`). The exact-match registry
-- lookup (pre-A1) returned Nothing for these, so the renderer
-- fell back to `any`. With superset match: if the target is a
-- strict subset of EXACTLY one alias's field set, return that
-- alias name. Multiple distinct sizes → smallest superset wins.
-- Tied sizes → ambiguous, fall back to Nothing (renderer emits
-- `any`; correctness preserved at the cost of typing precision).
lookupRecordAlias :: RecordRegistry -> [String] -> Maybe String
lookupRecordAlias registry fieldNames =
    let target = Set.fromList fieldNames
    in case Map.lookup target registry of
        Just aliasName -> Just aliasName
        Nothing
          | Set.null target -> Nothing
          | otherwise       ->
              let supersets =
                      [ (Set.size fs, name)
                      | (fs, name) <- Map.toList registry
                      , target `Set.isSubsetOf` fs
                      , target /= fs
                      ]
              in case List.sortOn fst supersets of
                  []                          -> Nothing
                  [(_, n)]                    -> Just n
                  ((s1, n1) : (s2, _) : _)
                      | s1 < s2   -> Just n1
                      | otherwise -> Nothing


-- | Classify a type alias as data record, behaviour record, or non-record.
-- Fields must be returned in *declaration order* (via FieldType's
-- _fieldIndex), not in Map key order (which is alphabetical). The
-- auto-generated record constructor `Foo : T1 -> T2 -> ... -> Foo`
-- uses this ordering as its positional API; if we reorder, user code
-- like `Piece King White` passes args into a constructor that expects
-- `(Colour, Kind)` and panics at runtime on the type assertion.
classifyAlias :: Can.Alias -> AliasKind
classifyAlias (Can.Alias _ body) = case body of
    T.TRecord fields _ ->
        let sorted = List.sortOn (T._fieldIndex . snd) (Map.toList fields)
            fieldList = map (\(name, T.FieldType _ ty) -> (name, ty)) sorted
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
