-- | Dead-code elimination for Sky.
--
-- Two layers:
--
--   1. Per-module DCE — `reachableTopLevel` walks one module's call graph
--      starting from `main` and returns the set of top-level names that
--      are transitively used. Unreachable decls inside the entry module
--      can be dropped from the generated Go output.
--
--   2. Whole-program DCE — `reachableWholeProgram` walks every module
--      in the project, tracking refs across module boundaries. Returns
--      the set of typed `Ref`s reachable from a set of entry roots
--      (typically `(entryModule, "main")`, optionally plus test-module
--      `tests` lists when running `sky test`).
--
-- The typed `Ref` ADT distinguishes:
--
--   * 'TopRef' — a user-defined top-level binding in some module.
--   * 'FfiRef' — an FFI kernel function (kernel module + function name).
--                Used to prune `Env.ffiKernel*Ref` maps so unreferenced
--                FFI sigs don't bloat codegen / `seedTypedFfiNames`.
--   * 'CtorRef' — an ADT constructor in some module. Pattern matches
--                 against a ctor keep the ctor (and by implication the
--                 sister ctors of the same ADT — see 'expandCtorClosure')
--                 alive even when no source-side `VarCtor` reaches them.
--
-- Conventions:
--
--   * Module names use the canonical dotted form ("Sky.Core.List"). FFI
--     kernel names are the short form ("Uuid", "Stripe") matching
--     `_fm_kernelName` in `Sky.Build.FfiRegistry`.
--   * The `__destruct__` sentinel name is preserved for top-level let-
--     destructure bindings (module-init side effects) — those have no
--     name to reference, so they're treated as roots.
--   * Top-level discards (`let _ = …` at module init scope) cannot be
--     reached by name either, so the walker emits them as implicit
--     sinks: any module containing a `DestructDef` or a value whose
--     body forces a `Task` discard counts as a side-effect root and
--     stays alive. See 'sideEffectRoots'.
module Sky.Build.Dce
    ( -- * Per-module DCE (legacy entry, used by emitter today)
      reachableTopLevel
    , buildCallGraph

      -- * Whole-program DCE
    , Ref(..)
    , reachableWholeProgram
    , ModuleName
    , isReachableTop
    , isReachableFfi
    , isReachableCtor
    )
    where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName


-- | Whole-program ref kind. See module header.
data Ref
    = TopRef !ModuleName !String
    | FfiRef !String !String           -- (kernelModule, kernelFn)
    | CtorRef !ModuleName !String      -- (home module, ctor name)
    deriving (Eq, Ord, Show)


-- | Dotted-canonical module name, e.g. "Sky.Core.List" or "Main".
type ModuleName = String


-- | Reachable closure from `main` over a single module's call graph.
-- Always includes "main". Unreachable names can be pruned safely.
--
-- Per-module only — does not see cross-module refs. Kept for backwards
-- compatibility with the legacy emitter path that DCEs entry-module
-- decls independently of the whole-program pass.
reachableTopLevel :: Can.Module -> Set.Set String
reachableTopLevel canMod =
    let graph = buildCallGraph canMod
        roots = Set.singleton "main"
    in closure graph roots


-- | A map from a top-level definition name to the set of top-level names
-- it references (directly). We ignore kernel/ctor refs here — kernel functions
-- are always present, and ADT constructors are handled separately via the
-- type-alias / union machinery.
buildCallGraph :: Can.Module -> Map.Map String (Set.Set String)
buildCallGraph canMod =
    let pairs = collectDefs (Can._decls canMod)
    in Map.fromList pairs
  where
    collectDefs Can.SaveTheEnvironment = []
    collectDefs (Can.Declare def rest) = defPair def : collectDefs rest
    collectDefs (Can.DeclareRec def defs rest) =
        map defPair (def : defs) ++ collectDefs rest

    defPair d = case d of
        Can.Def (A.At _ n) _ body      -> (n, collectTopRefs body)
        Can.TypedDef (A.At _ n) _ _ body _ -> (n, collectTopRefs body)
        Can.DestructDef _ body         -> ("__destruct__", collectTopRefs body)


-- | Top-level names referenced by an expression. Used by the legacy
-- per-module DCE — drops kernel and ctor info.
collectTopRefs :: Can.Expr -> Set.Set String
collectTopRefs e =
    Set.fromList [ n | TopRef _ n <- Set.toList (collectRefs Nothing e) ]


-- | Every typed `Ref` referenced by an expression.
--
-- The optional `Maybe ModuleName` argument records the HOME module the
-- expression lives in — used to scope `VarTopLevel m n` refs that
-- target the same module (canonical form may carry an empty home for
-- self-refs in some pipelines; this defends against that).
--
-- Does not descend into local bound variables — they're not top-level
-- refs. Does descend into every nested expression form. New AST nodes
-- in `Can.Expr_` MUST get an explicit arm here (CLAUDE.md
-- "No `_ -> []` catchalls" rule).
collectRefs :: Maybe ModuleName -> Can.Expr -> Set.Set Ref
collectRefs home (A.At _ e) = case e of
    Can.VarLocal _            -> Set.empty
    Can.VarTopLevel m n       -> Set.singleton (TopRef (ModuleName.toString m) n)
    Can.VarKernel km fn       -> Set.singleton (FfiRef km fn)
    Can.VarCtor _ m _typeName cname _  ->
        Set.singleton (CtorRef (ModuleName.toString m) cname)
    Can.Chr _                 -> Set.empty
    Can.Str _                 -> Set.empty
    Can.Int _                 -> Set.empty
    Can.Float _               -> Set.empty
    Can.Unit                  -> Set.empty
    Can.Accessor _            -> Set.empty
    Can.List xs               -> unionMap (collectRefs home) xs
    Can.Negate x              -> collectRefs home x
    Can.Binop _ _ _ _ a b     ->
        collectRefs home a `Set.union` collectRefs home b
    Can.Lambda _ body         -> collectRefs home body
    Can.Call f args           ->
        collectRefs home f `Set.union` unionMap (collectRefs home) args
    Can.If branches elseE     ->
        unionMap (\(c, t) -> collectRefs home c `Set.union` collectRefs home t) branches
            `Set.union` collectRefs home elseE
    Can.Let def body          ->
        defRefs home def `Set.union` collectRefs home body
    Can.LetRec defs body      ->
        unionMap (defRefs home) defs `Set.union` collectRefs home body
    Can.LetDestruct _ rhs body ->
        collectRefs home rhs `Set.union` collectRefs home body
    Can.Case subj branches    ->
        collectRefs home subj
            `Set.union` unionMap (branchRefs home) branches
    Can.Access target _       -> collectRefs home target
    Can.Update _ base fields  ->
        collectRefs home base
            `Set.union` unionMap (\(Can.FieldUpdate _ fe) -> collectRefs home fe)
                                 (Map.elems fields)
    Can.Record m              -> unionMap (collectRefs home) (Map.elems m)
    Can.Tuple a b more        ->
        collectRefs home a `Set.union` collectRefs home b
            `Set.union` unionMap (collectRefs home) more


-- | A case branch's pattern can reference constructors (CtorRef) and its
-- body is walked for the usual ref set.
branchRefs :: Maybe ModuleName -> Can.CaseBranch -> Set.Set Ref
branchRefs home (Can.CaseBranch pat body) =
    patternRefs pat `Set.union` collectRefs home body


-- | Constructor refs introduced by patterns. `PCtor` matches keep the
-- constructor (and so the union it belongs to) alive.
patternRefs :: Can.Pattern -> Set.Set Ref
patternRefs (A.At _ p) = case p of
    Can.PAnything       -> Set.empty
    Can.PVar _          -> Set.empty
    Can.PRecord _       -> Set.empty
    Can.PAlias inner _  -> patternRefs inner
    Can.PUnit           -> Set.empty
    Can.PTuple a b more ->
        patternRefs a `Set.union` patternRefs b
            `Set.union` unionMap patternRefs more
    Can.PList xs        -> unionMap patternRefs xs
    Can.PCons hd tl     -> patternRefs hd `Set.union` patternRefs tl
    Can.PBool _         -> Set.empty
    Can.PChr _          -> Set.empty
    Can.PStr _          -> Set.empty
    Can.PInt _          -> Set.empty
    Can.PCtor home _ty _u cname _idx args ->
        Set.insert (CtorRef (ModuleName.toString home) cname)
            (unionMap (patternRefs . Can._pca_pat) args)


defRefs :: Maybe ModuleName -> Can.Def -> Set.Set Ref
defRefs home (Can.Def _ _ body)          = collectRefs home body
defRefs home (Can.TypedDef _ _ _ body _) = collectRefs home body
defRefs home (Can.DestructDef _ body)    = collectRefs home body


unionMap :: Ord b => (a -> Set.Set b) -> [a] -> Set.Set b
unionMap f = foldr (Set.union . f) Set.empty


-- ═══════════════════════════════════════════════════════════
-- Whole-program reachability
-- ═══════════════════════════════════════════════════════════

-- | Walk the entire program's call graph starting from `(entryMod, "main")`
-- plus any extra roots (e.g. `tests` lists for `sky test`). Returns every
-- transitively-reached `Ref` across all modules.
--
-- Cross-module: a TopRef in module A references TopRef/FfiRef/CtorRef in
-- any module; the closure walks them all.
--
-- Side-effect roots: any module-level `DestructDef` or `let _ = X`
-- discarded value is a sink — its body's refs are kept alive even
-- without a name to reach them from. These are added to the root set
-- upfront via `sideEffectRoots`.
--
-- CtorRef closure: matching one ctor of a union pulls in EVERY sister
-- ctor of the same union (the codegen emits whole-union ADT structs
-- and the type integrity needs all alternates present). See
-- 'expandCtorClosure'.
reachableWholeProgram
    :: ModuleName                        -- ^ entry module canonical name
    -> Map.Map ModuleName Can.Module     -- ^ every module in the project, keyed by canonical name
    -> Set.Set String                    -- ^ extra entry-module roots (e.g. `tests` for `sky test`)
    -> Set.Set Ref
reachableWholeProgram entryMod allMods extraRoots =
    let graph = buildWholeProgramGraph allMods
        seeds = Set.fromList
            ( TopRef entryMod "main"
            : [ TopRef entryMod r | r <- Set.toList extraRoots ]
            )
            `Set.union` sideEffectRoots allMods
            `Set.union` initRoots allMods
        reached = closureRefs graph seeds
    in expandCtorClosure allMods reached


-- | Side-effect roots: every module-init discard (`DestructDef` at top
-- level OR a same-named top-level binding `__destruct__`). These have
-- no callable name so they'd never be reached otherwise.
sideEffectRoots :: Map.Map ModuleName Can.Module -> Set.Set Ref
sideEffectRoots allMods = Set.fromList
    [ TopRef mn name
    | (mn, m) <- Map.toList allMods
    , name <- destructNames (Can._decls m)
    ]
  where
    destructNames Can.SaveTheEnvironment = []
    destructNames (Can.Declare def rest) = destructName def ++ destructNames rest
    destructNames (Can.DeclareRec def defs rest) =
        concatMap destructName (def : defs) ++ destructNames rest

    destructName (Can.DestructDef _ _) = ["__destruct__"]
    destructName _                     = []


-- | A handful of well-known top-level names that ARE callable but
-- might not be referenced by `main` directly — e.g. dep modules with
-- their own free-standing logic the user expects to run.
--
-- Today: nothing. Reserved for future cases (e.g. `register`
-- annotations on test modules).
initRoots :: Map.Map ModuleName Can.Module -> Set.Set Ref
initRoots _ = Set.empty


-- | Whole-program call graph: every (Ref → set of Refs it references).
-- Only TopRefs appear as KEYS (only top-level decls have bodies); FfiRef
-- and CtorRef are LEAVES (no body to walk).
buildWholeProgramGraph
    :: Map.Map ModuleName Can.Module
    -> Map.Map Ref (Set.Set Ref)
buildWholeProgramGraph allMods = Map.unions
    [ Map.fromList (declRefs mn (Can._decls m))
    | (mn, m) <- Map.toList allMods
    ]
  where
    declRefs _ Can.SaveTheEnvironment = []
    declRefs mn (Can.Declare def rest) = defEntry mn def ++ declRefs mn rest
    declRefs mn (Can.DeclareRec def defs rest) =
        map (defEntry' mn) (def : defs) ++ declRefs mn rest

    defEntry mn d = [defEntry' mn d]

    defEntry' mn d = case d of
        Can.Def (A.At _ n) _ body          -> (TopRef mn n, collectRefs (Just mn) body)
        Can.TypedDef (A.At _ n) _ _ body _ -> (TopRef mn n, collectRefs (Just mn) body)
        Can.DestructDef _ body             -> (TopRef mn "__destruct__", collectRefs (Just mn) body)


-- | Transitive closure over a Ref → Ref-set graph.
closureRefs
    :: Map.Map Ref (Set.Set Ref)
    -> Set.Set Ref
    -> Set.Set Ref
closureRefs graph = go
  where
    go visited =
        let frontier = Set.unions
                [ Map.findWithDefault Set.empty r graph
                | r <- Set.toList visited
                ]
            next = visited `Set.union` frontier
        in if next == visited then visited else go next


-- | If any constructor of a union is reachable, every sister constructor
-- of the same union becomes reachable too. Required because the codegen
-- emits a single Go ADT type per union (all alternates share the type
-- declaration), and the pattern-match `Tag` integer indexes ALL ctors.
-- Dropping a sister ctor would shift indices or panic on integrity
-- checks.
expandCtorClosure
    :: Map.Map ModuleName Can.Module
    -> Set.Set Ref
    -> Set.Set Ref
expandCtorClosure allMods reached =
    let extra = Set.unions
            [ siblings mn cname
            | CtorRef mn cname <- Set.toList reached
            ]
    in reached `Set.union` extra
  where
    siblings :: ModuleName -> String -> Set.Set Ref
    siblings mn cname =
        case Map.lookup mn allMods of
            Nothing -> Set.empty
            Just m  -> case findUnionFor cname (Can._unions m) of
                Nothing    -> Set.empty
                Just union -> Set.fromList
                    [ CtorRef mn other
                    | Can.Ctor other _ _ _ <- Can._u_alts union
                    ]

    findUnionFor cname = goU . Map.elems
      where
        goU [] = Nothing
        goU (u:us)
            | any (\(Can.Ctor n _ _ _) -> n == cname) (Can._u_alts u) = Just u
            | otherwise = goU us


-- ═══════════════════════════════════════════════════════════
-- Lookup helpers
-- ═══════════════════════════════════════════════════════════

isReachableTop :: Set.Set Ref -> ModuleName -> String -> Bool
isReachableTop s mn n = Set.member (TopRef mn n) s


isReachableFfi :: Set.Set Ref -> String -> String -> Bool
isReachableFfi s km fn = Set.member (FfiRef km fn) s


isReachableCtor :: Set.Set Ref -> ModuleName -> String -> Bool
isReachableCtor s mn cname = Set.member (CtorRef mn cname) s


-- | Transitive closure over the call graph (legacy, string-keyed).
-- Starts from `roots` and expands until fixed point.
--
-- Kept exported for backward compat — used by `reachableTopLevel`.
closure :: Map.Map String (Set.Set String) -> Set.Set String -> Set.Set String
closure graph = go
  where
    go visited =
        let frontier = Set.unions
                [ Map.findWithDefault Set.empty n graph
                | n <- Set.toList visited
                ]
            next = visited `Set.union` frontier
        in if next == visited then visited else go next
