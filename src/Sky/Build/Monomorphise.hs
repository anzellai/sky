-- | v0.13 Phase A2 — Monomorphisation type-level building blocks.
--
-- Given the call-instance table from `Sky.Type.Solve.solveWithInstances`,
-- the monomorphisation pass needs to:
--
--   1. Mangle each `(callee, type-args)` instance into a deterministic
--      Go identifier so emission can declare one function per instance
--      and call sites can reference the right one.
--   2. Build a substitution map (`a |-> Int`, `b |-> String`, …) from
--      the callee's polymorphic scheme + the call site's concrete
--      type-args.
--   3. Apply the substitution to a Sky-source function body — every
--      `TVar` in type annotations / pattern types / inferred shapes
--      is replaced with the concrete type at this instance.
--
-- This module ships the pure type-level pieces.  The downstream
-- `Sky.Build.Compile` integration (collecting call sites, threading
-- the substitution, rewriting call-site names to the mangled instance)
-- lands in a later phase.
--
-- Correctness property: `mangleType` is deterministic — equal types
-- map to equal names regardless of how they were constructed.
-- Equal-types-different-names would create duplicate emissions
-- (wasted code) and fragment the LSP's symbol index.
--
-- v0.13 Phase A2 (this file) — replaces an earlier stub that walked
-- the AST without using the solver's instance capture.  The new
-- architecture: collection happens AT SOLVE TIME via
-- `Solve.solveWithInstances`; this module just mangles + substitutes.
module Sky.Build.Monomorphise
    ( mangleType
    , mangleInstance
    , mangleQualName
    , buildSubstitution
    , substituteType
    , typesEquiv
    , collectCallSites
    , collectCallSitesDef
    , reachableInstances
    , ReachableSet
    , Instance
    , substTypeParamsInString
    , specialiseFuncDecl
    ) where

import qualified Data.Char as Char
import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Data.List as List
import Data.Maybe (mapMaybe)
import qualified Sky.AST.Canonical as Can
import qualified Sky.Generate.Go.Ir as Ir
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Type.Solve as Solve
import qualified Sky.Type.Type as T


-- ─── mangling ────────────────────────────────────────────────────────


-- | Encode a Sky type as a Go-identifier-safe string.
--
-- Encoding rules (deterministic, reversible-by-inspection):
--
--   * Primitive types collapse to their bare names: `Int`, `String`,
--     `Bool`, `Float`, `Char`, `Unit`.
--   * Parametric types use `<Ctor>Of` then their args separated by
--     `_`: `MaybeOf_Int`, `ListOf_String`, `ResultOf_Error_Int`.
--     Nested parametrics nest:
--       `Maybe (List Int)` → `MaybeOf_ListOf_Int`
--       `Dict String (List Int)` → `DictOf_String_ListOf_Int`
--   * Function types use `FnOf`: `Int -> String` → `FnOf_Int_String`.
--   * Tuples use `Tup<N>Of`: `(Int, String)` → `Tup2Of_Int_String`.
--   * Records collapse to `RecOf_<sorted-field-keys>_<short-hash>`.
--     The hash discriminates structurally-different records that
--     share field names but not types.
--   * Type variables (should not survive monomorphisation but kept
--     for completeness) encode as `TV_<name>`.
--   * Aliases encode the alias's canonical name; the underlying
--     type isn't re-mangled because two aliases of the same shape
--     are still distinct user-facing types.
mangleType :: Can.Type -> String
mangleType ty = case ty of
    Can.TVar name -> "TV_" ++ sanitise name
    Can.TUnit     -> "Unit"
    Can.TLambda from to ->
        "FnOf_" ++ mangleType from ++ "_" ++ mangleType to
    Can.TType _ name [] ->
        -- Primitive / nullary type constructor: bare name.
        sanitise name
    Can.TType _ name args ->
        sanitise name ++ "Of_" ++ List.intercalate "_" (map mangleType args)
    Can.TTuple a b cs ->
        let n = 2 + length cs
        in "Tup" ++ show n ++ "Of_"
        ++ List.intercalate "_" (map mangleType (a : b : cs))
    Can.TRecord fields _ext ->
        -- Sort field keys for determinism; hash the FULL field-and-type
        -- shape so two records with the same keys but different types
        -- get different names.
        let sortedFields = List.sortOn fst (Map.toList fields)
            keyNames = map fst sortedFields
            shapeStr = concatMap
                (\(k, Can.FieldType _ t) -> k ++ ":" ++ mangleType t ++ ";")
                sortedFields
        in "RecOf_" ++ List.intercalate "_" (map sanitise keyNames)
                    ++ "__" ++ shortHash shapeStr
    Can.TAlias _ name _pairs _underlying ->
        -- Alias keeps its name.  Two aliases that expand to the same
        -- type are still distinct user-facing types — the LSP/docs
        -- treat them as named entities.
        sanitise name


-- | Mangle a (callee, type-args) instance into the Go function name
-- the monomorphiser will emit.  Examples:
--
--   @CallInstance "Sky.Core.Maybe.withDefault" [Int]@
--     → @Sky_Core_Maybe_withDefault__Int@
--
--   @CallInstance "Sky.Core.List.map" [Int, String]@
--     → @Sky_Core_List_map__Int_String@
--
--   @CallInstance "Sky.Core.Maybe.withDefault" [List Int]@
--     → @Sky_Core_Maybe_withDefault__ListOf_Int@
--
-- The double-underscore separator distinguishes the callee suffix
-- from the type-arg list, so future renames (e.g. a Sky function
-- gaining an underscore in its name) don't collide with type-arg
-- separators.
mangleInstance :: Solve.CallInstance -> String
mangleInstance (Solve.CallInstance callee tys _) =
    mangleQualName callee
    ++ (if null tys
          then ""
          else "__" ++ List.intercalate "_" (map mangleType tys))


-- | Convert a Sky-source qualified name (`Sky.Core.Maybe.withDefault`)
-- to a Go-identifier-safe form (`Sky_Core_Maybe_withDefault`).  Dots
-- become underscores; everything else is left untouched (callers feed
-- already-validated Sky names).
mangleQualName :: String -> String
mangleQualName = map (\c -> if c == '.' then '_' else c)


-- ─── substitution ────────────────────────────────────────────────────


-- | Zip a function's `Forall` quantifier list with the call site's
-- concrete type-args to produce the substitution `σ : TVar → Type`.
--
-- The caller must ensure the lengths match — they do by construction
-- because `solveWithInstances` only records call sites whose fresh
-- vars all resolved to concrete types, and the fresh-var list has the
-- same length as `Forall vars` (minus the wildcard `any`).
buildSubstitution :: Can.Annotation -> [Can.Type] -> Map.Map String Can.Type
buildSubstitution (Can.Forall vars _) tys =
    -- The solver filters out the wildcard `any` from the fresh-var
    -- list (`fromAnnotation` in Instantiate.hs); align by doing the
    -- same here so the zip semantics match.
    let realVars = filter (/= "any") vars
    in Map.fromList (zip realVars tys)


-- | Walk a Sky `Can.Type` replacing every `TVar n` in the substitution
-- map with its concrete type.  Anything else recurses structurally.
substituteType :: Map.Map String Can.Type -> Can.Type -> Can.Type
substituteType σ ty = case ty of
    Can.TVar name -> Map.findWithDefault ty name σ
    Can.TUnit -> ty
    Can.TLambda from to ->
        Can.TLambda (substituteType σ from) (substituteType σ to)
    Can.TType home name args ->
        Can.TType home name (map (substituteType σ) args)
    Can.TRecord fields ext ->
        Can.TRecord (Map.map (substituteFieldType σ) fields) ext
    Can.TTuple a b cs ->
        Can.TTuple (substituteType σ a) (substituteType σ b)
                   (map (substituteType σ) cs)
    Can.TAlias home name pairs aliasTy ->
        Can.TAlias home name
            (map (\(n, t) -> (n, substituteType σ t)) pairs)
            (substituteAliasType σ aliasTy)


substituteFieldType :: Map.Map String Can.Type -> Can.FieldType -> Can.FieldType
substituteFieldType σ (Can.FieldType idx ty) =
    Can.FieldType idx (substituteType σ ty)


substituteAliasType :: Map.Map String Can.Type -> Can.AliasType -> Can.AliasType
substituteAliasType σ at = case at of
    Can.Hoisted t -> Can.Hoisted (substituteType σ t)
    Can.Filled t  -> Can.Filled (substituteType σ t)


-- ─── equivalence ────────────────────────────────────────────────────


-- | Structural type equality for deduplication.  Two types are
-- equivalent when they have identical canonical shape modulo TVar-
-- name aliasing (i.e. `a → a` and `b → b` are equivalent).  Used by
-- the monomorphisation pass to collapse instances that look
-- different syntactically but represent the same Go-emitted
-- function.
typesEquiv :: Can.Type -> Can.Type -> Bool
typesEquiv = go Map.empty
  where
    go env t1 t2 = case (t1, t2) of
        (Can.TVar a, Can.TVar b) ->
            case Map.lookup a env of
                Just b' -> b' == b
                Nothing -> True  -- first occurrence; alias a ~ b
        (Can.TUnit, Can.TUnit) -> True
        (Can.TLambda f1 t1', Can.TLambda f2 t2') ->
            go env f1 f2 && go env t1' t2'
        (Can.TType _ n1 a1, Can.TType _ n2 a2) ->
            n1 == n2 && length a1 == length a2
                     && and (zipWith (go env) a1 a2)
        (Can.TRecord f1 _, Can.TRecord f2 _) ->
            Map.keysSet f1 == Map.keysSet f2
            && all (\k -> case (Map.lookup k f1, Map.lookup k f2) of
                    (Just (Can.FieldType _ x), Just (Can.FieldType _ y)) ->
                        go env x y
                    _ -> False) (Map.keys f1)
        (Can.TTuple a1 b1 c1, Can.TTuple a2 b2 c2) ->
            go env a1 a2 && go env b1 b2
            && length c1 == length c2
            && and (zipWith (go env) c1 c2)
        (Can.TAlias _ n1 _ _, Can.TAlias _ n2 _ _) ->
            n1 == n2
        _ -> False


-- ─── helpers ─────────────────────────────────────────────────────────


-- | Sanitise a name for use as a Go identifier.  Replaces characters
-- not in `[A-Za-z0-9_]` with `_`.  The callers feed already-sensible
-- Sky names — this is defence in depth, not the primary check.
sanitise :: String -> String
sanitise = map (\c ->
    if (c >= 'A' && c <= 'Z')
       || (c >= 'a' && c <= 'z')
       || (c >= '0' && c <= '9')
       || c == '_'
    then c
    else '_')


-- | Short deterministic hash for record-shape disambiguation.
-- 8 hex digits is plenty — anonymous-record collision probability
-- at scale is negligible.  SDBM-ish; identical to the existing
-- `synthAnonRecordName` in Compile.hs so we stay consistent.
shortHash :: String -> String
shortHash = padHash . hex . foldl step (0 :: Int)
  where
    step h c = (h * 33 + fromEnum c) `mod` 0x100000000

    hex 0 = "0"
    hex n = toHex n

    toHex 0 = ""
    toHex n =
        let (q, r) = n `divMod` 16
        in toHex q ++ [digit r]

    digit n
        | n < 10    = toEnum (fromEnum '0' + n)
        | otherwise = toEnum (fromEnum 'a' + n - 10)

    padHash s = replicate (max 0 (8 - length s)) '0' ++ s


-- ─── Sky-level DCE: reachability + transitive closure ──────────────


-- | A specialised instance: the callee's qualified Sky name plus the
-- list of concrete `Can.Type`s its quantified `Forall` vars resolved
-- to at this usage.
type Instance = (String, [Can.Type])


-- | The reachable-instance set: every instance that needs to be
-- emitted as a specialised Go function.  Computed by transitive
-- closure from `main` (or any entry-point list) over the captured
-- CallInstance map.
type ReachableSet = Set.Set Instance


-- | Walk a `Can.Expr`, collecting every USAGE site annotated with
-- `(usage-region, callee-qualified-name)`.  "Usage" covers both
-- direct calls (`Can.Call`) AND value references (`Can.VarTopLevel`
-- / `Can.VarKernel` / `Can.VarCtor` appearing OUTSIDE the func slot
-- of a `Can.Call`).  Used by the reachability walker to find what
-- each function's body invokes OR passes as a value.
--
-- Why value references count: Sky's runtime invokes user functions
-- dynamically through records (Sky.Live's `Live.app { update,
-- view, … }`), through commands (`Cmd.perform task ToMsg`), and
-- through Msg dispatch.  These references don't appear as
-- `Can.Call` in the source AST but ARE reachable usages — DCE
-- must keep them.
--
-- `Can.VarLocal` references are NOT included — locals are
-- per-function-scope bindings (lambda args, let-binders) and don't
-- need separate emission.
--
-- Recurses into every sub-expression so nested usages (inside
-- lambdas, let bodies, case branches, binop operands, record
-- field updates, etc.) all surface in the output.
collectCallSites :: Can.Expr -> [(A.Region, String)]
collectCallSites e@(A.At region expr) = case expr of
    Can.Call func args ->
        let funcSite = case A.toValue func of
                Can.VarTopLevel home name ->
                    [(A.toRegion func,
                      ModuleName.toString home ++ "." ++ name)]
                Can.VarKernel modName name ->
                    [(A.toRegion func, modName ++ "." ++ name)]
                Can.VarCtor _ home _ ctorName _ ->
                    [(A.toRegion func,
                      ModuleName.toString home ++ "." ++ ctorName)]
                _ -> []
        in funcSite
           ++ collectCallSitesNonHead func  -- exclude the head ref
                                            -- (already captured above)
           ++ concatMap collectCallSites args
    -- VALUE reference: a top-level / kernel / ctor name appearing
    -- bare (not as the head of a Call).  Counts as a usage so DCE
    -- doesn't drop the function.
    Can.VarTopLevel home name ->
        [(region, ModuleName.toString home ++ "." ++ name)]
    Can.VarKernel modName name ->
        [(region, modName ++ "." ++ name)]
    Can.VarCtor _ home _ ctorName _ ->
        [(region, ModuleName.toString home ++ "." ++ ctorName)]

    Can.Lambda _ body -> collectCallSites body
    Can.If branches elseE ->
        concatMap (\(c, b) -> collectCallSites c ++ collectCallSites b) branches
        ++ collectCallSites elseE
    Can.Let def body -> collectCallSitesDef def ++ collectCallSites body
    Can.LetRec defs body ->
        concatMap collectCallSitesDef defs ++ collectCallSites body
    Can.LetDestruct _ val body ->
        collectCallSites val ++ collectCallSites body
    Can.Case subj branches ->
        collectCallSites subj
        ++ concatMap (\(Can.CaseBranch _ b) -> collectCallSites b) branches
    Can.Access tgt _ -> collectCallSites tgt
    Can.Update _ baseE fields ->
        collectCallSites baseE
        ++ concatMap (\(_, Can.FieldUpdate _ fe) -> collectCallSites fe)
                     (Map.toList fields)
    Can.Record fields ->
        concatMap collectCallSites (Map.elems fields)
    Can.Negate inner -> collectCallSites inner
    Can.Binop _ _ _ _ left right ->
        collectCallSites left ++ collectCallSites right
    Can.List items -> concatMap collectCallSites items
    Can.Tuple a b cs ->
        collectCallSites a ++ collectCallSites b
        ++ concatMap collectCallSites cs
    -- All other Expr_ constructors are leaves (no sub-exprs).
    _ -> case e of _ -> []


-- | Like `collectCallSites` but treats the head as an
-- already-captured call site (i.e., does not re-emit a VALUE
-- reference for the head of a `Can.Call`).
collectCallSitesNonHead :: Can.Expr -> [(A.Region, String)]
collectCallSitesNonHead (A.At _ expr) = case expr of
    Can.VarTopLevel _ _ -> []
    Can.VarKernel _ _ -> []
    Can.VarCtor{} -> []
    _ -> collectCallSites (A.At A.one expr)


-- | Walk a `Can.Def`, descending into its body to collect call sites.
collectCallSitesDef :: Can.Def -> [(A.Region, String)]
collectCallSitesDef def = case def of
    Can.Def _ _ body -> collectCallSites body
    Can.TypedDef _ _ _ body _ -> collectCallSites body
    Can.DestructDef _ body -> collectCallSites body


-- | Compute the reachable instance set by transitive closure from a
-- list of entry points (typically just `[(main-qualName, [])]`).
--
-- At each entry: look up the def, build σ from the instance's
-- type-args via the annotation, walk the body's call sites,
-- substitute the inner CallInstance type-args by σ, enqueue.
-- Continue until fixpoint.
--
-- `defMap` is keyed by full Sky-qualified names (e.g.
-- `"Sky.Core.List.foldl"`).  Names not present are leaves
-- (kernel / FFI calls — emitted via separate paths).
--
-- `annotMap` carries each callee's GENERALISED annotation (post-
-- `generaliseToAnnotation`), so the Forall var names align with what
-- the solver captured as `_instance_quantifiers`.
--
-- `csiMap` is keyed by `(line, col)` of the call's func-expression
-- region — same shape as `_cg_callSiteInstances`.
--
-- Returns: the set of reachable `(qualName, [concrete-types])`
-- pairs.  Each is a function that must be emitted as a specialised
-- instance.
reachableInstances
    :: Map.Map String Can.Def
    -> Map.Map String Can.Annotation
    -> Map.Map (String, Int, Int) Solve.CallInstance
    -> [Instance]
    -> ReachableSet
reachableInstances defMap annotMap csiMap entries = go Set.empty entries
  where
    go reached [] = reached
    go reached (inst : rest)
        | Set.member inst reached = go reached rest
        | otherwise =
            let reached' = Set.insert inst reached
                (qName, ts) = inst
                σ = case Map.lookup qName annotMap of
                        Just annot -> buildSubstitution annot ts
                        Nothing    -> Map.empty
                bodyCalls = case Map.lookup qName defMap of
                        Just d  -> collectCallSitesDef d
                        Nothing -> []
                -- v0.17 C24 — derive the (modName, line, col) key.
                -- The csiMap key is "" for write-side when
                -- SKY_CSI_WIDEN_KEY is unset.  We can't read env
                -- vars from this pure function — instead probe the
                -- csiMap's first key to decide whether to use "".
                -- This keeps Mono in sync with Compile.hs without
                -- threading the flag through every call.
                firstKey = case Map.keys csiMap of
                    (k:_) -> k
                    _ -> ("", 0, 0)
                wideningOn = case firstKey of (m, _, _) -> not (null m)
                hostMod = if not wideningOn then "" else
                    case reverse (List.findIndices (== '.') qName) of
                        (i : _) -> take i qName
                        []      -> ""
                newCalls = mapMaybe
                    (\(region, callee) ->
                        let key = ( hostMod
                                  , A._line (A._start region)
                                  , A._col  (A._start region) )
                            siteTypes = case Map.lookup key csiMap of
                                Just (Solve.CallInstance _ tys _) -> tys
                                Nothing -> []
                            substituted = map (substituteType σ) siteTypes
                        in if Map.member callee defMap
                            then Just (callee, substituted)
                            else
                                -- Non-Sky-source callee (kernel /
                                -- FFI / Ctor) — terminal node, no
                                -- transitive instances to expand.
                                -- Still record for the typed-call-
                                -- site rewriting layer.
                                Just (callee, substituted))
                    bodyCalls
            in go reached' (newCalls ++ rest)


-- ─── Per-instance specialisation: GoIr substitution ─────────────────


-- | Substitute generic Go type-parameter names (`T1`, `T2`, …) in a
-- rendered Go type-string with concrete Go types.  Identifier-aware:
-- only replaces whole-word matches so `T1` in `rt.SkyMaybe[T1]`
-- becomes `rt.SkyMaybe[string]` but a hypothetical
-- `T1_field_name` isn't touched.
--
-- Mirrors `substTVarsInGoType` in Compile.hs.  Kept duplicated here
-- so the monomorphisation pass doesn't depend on Compile.hs (which
-- depends on this module).
substTypeParamsInString :: Map.Map String String -> String -> String
substTypeParamsInString σ = goSubst
  where
    goSubst [] = []
    goSubst rest@(c:cs)
        | isIdentStart c =
            let (word, after) = span isIdentChar rest
            in case Map.lookup word σ of
                Just replacement -> replacement ++ goSubst after
                Nothing          -> word ++ goSubst after
        | otherwise = c : goSubst cs

    -- Unicode-aware Go identifier predicates so a Sky source name
    -- with non-ASCII letters (allowed by Go's spec — see
    -- `Sky.Parse.Variable.isIdentChar` for the parser side) is
    -- treated as one token here too. ASCII-only walks would slice
    -- the identifier and apply σ-substitution to the ASCII prefix.
    isIdentStart c = Char.isLetter c || c == '_'
    isIdentChar c  = Char.isAlphaNum c || c == '_'


-- | v0.13 Phase A4: specialise a generic `GoFuncDecl` per a captured
-- instance.  Substitutes every `T1`, `T2`, … in the function's
-- parameters, return type, and body with the instance's concrete
-- Go types.  Drops the generic type-param list so the emitted
-- function is non-generic.
--
-- The new name is mangled per `mangleInstance` and the function's
-- name gets the `__<types>` suffix.
--
-- The body is recursively traversed; every `GoIdent`,
-- `GoGenericCall`, `GoSliceLit`, `GoMapLit`, `GoStructLit`,
-- `GoFuncLit`, `GoTypeAssert`, and the inline type strings get the
-- substitution applied.
--
-- Recursive self-calls in the body are renamed: a call to
-- `<originalName>[T1, ...]` (or `<originalName>(...)` if Go inferred
-- the type params) becomes `<originalName>__<types>(...)` — the
-- specialised name.  This makes the recursion resolve to the SAME
-- specialised instance.
specialiseFuncDecl
    :: String                          -- mangled instance name
    -> Map.Map String String           -- σ: T1 → "int" etc.
    -> Maybe String                    -- original generic name to
                                       -- rewrite recursive calls
    -> Ir.GoFuncDecl                   -- generic source
    -> Ir.GoFuncDecl                   -- specialised result
specialiseFuncDecl mangledName σ originalName func =
    Ir.GoFuncDecl
        { Ir._gf_name       = mangledName
        -- Retain any type params the instance's σ did NOT substitute.
        -- A CallInstance can be partial — it captures concrete types
        -- for the type params it could resolve at the call site, but
        -- a param whose type stayed polymorphic at the call site
        -- (e.g. the call lives inside another generic function) has
        -- no entry in σ.  Dropping the ENTIRE type-param list while
        -- those `Tn` still appear in the params / body / return
        -- emits undeclared-identifier Go (`undefined: T2`).  Keeping
        -- the unsubstituted ones produces a PARTIALLY-specialised
        -- generic function — `func F[T2 any](field T2, …)` — which
        -- is valid Go and still monomorphic in the resolved params.
        , Ir._gf_typeParams =
            [ tp
            | tp@(tpName, _) <- Ir._gf_typeParams func
            , not (Map.member tpName σ)
            ]
        , Ir._gf_params     = map substParam (Ir._gf_params func)
        , Ir._gf_returnType = substTypeParamsInString σ (Ir._gf_returnType func)
        , Ir._gf_body       = map substStmt (Ir._gf_body func)
        }
  where
    substParam (Ir.GoParam name ty) =
        Ir.GoParam name (substTypeParamsInString σ ty)

    substStmt stmt = case stmt of
        Ir.GoExprStmt e -> Ir.GoExprStmt (substExpr e)
        Ir.GoAssign n e -> Ir.GoAssign n (substExpr e)
        Ir.GoShortDecl n e -> Ir.GoShortDecl n (substExpr e)
        Ir.GoVarDecl n ty me ->
            Ir.GoVarDecl n (substTypeParamsInString σ ty) (fmap substExpr me)
        Ir.GoReturn e -> Ir.GoReturn (substExpr e)
        Ir.GoReturnVoid -> stmt
        Ir.GoIf cond thn els ->
            Ir.GoIf (substExpr cond) (map substStmt thn) (map substStmt els)
        Ir.GoSwitch e branches ->
            Ir.GoSwitch (substExpr e)
                [(substExpr v, map substStmt body) | (v, body) <- branches]
        Ir.GoTypeSwitch n e branches mDefault ->
            Ir.GoTypeSwitch n (substExpr e)
                [(substTypeParamsInString σ t, map substStmt body)
                | (t, body) <- branches]
                (fmap (map substStmt) mDefault)
        Ir.GoFor n e body ->
            Ir.GoFor n (substExpr e) (map substStmt body)
        Ir.GoForever body ->
            Ir.GoForever (map substStmt body)
        Ir.GoContinue -> stmt
        Ir.GoBlock_ stmts -> Ir.GoBlock_ (map substStmt stmts)
        Ir.GoComment _ -> stmt
        Ir.GoBlank -> stmt

    substExpr e = case e of
        Ir.GoIdent name ->
            -- Identifier could be a TYPE name reference embedded in
            -- a function-name path (rare) or a value identifier.
            -- Apply the substitution to be safe (it's a no-op for
            -- non-Tn identifiers).
            Ir.GoIdent (substTypeParamsInString σ name)
        Ir.GoQualified pkg n -> Ir.GoQualified pkg n
        Ir.GoIntLit _ -> e
        Ir.GoFloatLit _ -> e
        Ir.GoStringLit _ -> e
        Ir.GoRuneLit _ -> e
        Ir.GoBoolLit _ -> e
        Ir.GoNil -> e
        Ir.GoCall f args -> Ir.GoCall (substExpr f) (map substExpr args)
        Ir.GoGenericCall name typeArgs args ->
            -- If the call is to the function we're specialising,
            -- rewrite to the specialised mangled name and drop the
            -- type args (specialised version is non-generic).
            case originalName of
                Just orig | orig == name ->
                    Ir.GoCall (Ir.GoIdent mangledName) (map substExpr args)
                _ ->
                    Ir.GoGenericCall name
                        (map (substTypeParamsInString σ) typeArgs)
                        (map substExpr args)
        Ir.GoSelector tgt n -> Ir.GoSelector (substExpr tgt) n
        Ir.GoIndex tgt i -> Ir.GoIndex (substExpr tgt) (substExpr i)
        Ir.GoSliceLit ty items ->
            Ir.GoSliceLit (substTypeParamsInString σ ty) (map substExpr items)
        Ir.GoMapLit k v entries ->
            Ir.GoMapLit (substTypeParamsInString σ k)
                        (substTypeParamsInString σ v)
                        [(substExpr ke, substExpr ve) | (ke, ve) <- entries]
        Ir.GoStructLit ty fields ->
            Ir.GoStructLit (substTypeParamsInString σ ty)
                [(n, substExpr fv) | (n, fv) <- fields]
        Ir.GoFuncLit params retTy body ->
            Ir.GoFuncLit
                (map substParam params)
                (substTypeParamsInString σ retTy)
                (map substStmt body)
        Ir.GoBinary op l r -> Ir.GoBinary op (substExpr l) (substExpr r)
        Ir.GoUnary op x -> Ir.GoUnary op (substExpr x)
        Ir.GoTypeAssert v ty ->
            Ir.GoTypeAssert (substExpr v) (substTypeParamsInString σ ty)
        Ir.GoBlock stmts result ->
            Ir.GoBlock (map substStmt stmts) (substExpr result)
        Ir.GoTypedBlock retTy stmts result ->
            Ir.GoTypedBlock (substTypeParamsInString σ retTy)
                (map substStmt stmts) (substExpr result)
        Ir.GoRaw s ->
            -- Raw Go code may embed type-param tokens.  Apply
            -- string-level substitution defensively.
            Ir.GoRaw (substTypeParamsInString σ s)
