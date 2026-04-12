-- | Dead-code elimination for Sky.
-- Walks the call graph starting from `main` (and anything main directly or
-- transitively references). Returns the set of reachable top-level names.
-- Unreachable decls can be dropped from the generated Go output, shrinking
-- binaries and speeding up the downstream Go compile step.
module Sky.Build.Dce
    ( reachableTopLevel
    , buildCallGraph
    )
    where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A


-- | Reachable closure from `main` over the top-level call graph.
-- Always includes "main". Unreachable names can be pruned safely.
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
        Can.Def (A.At _ n) _ body      -> (n, collectRefs body)
        Can.TypedDef (A.At _ n) _ _ body _ -> (n, collectRefs body)
        Can.DestructDef _ body         -> ("__destruct__", collectRefs body)


-- | All top-level names referenced in an expression.
-- Does not descend into nested lambdas' local vars — those are bound, not
-- referenced. Conservatively traverses all sub-expressions.
collectRefs :: Can.Expr -> Set.Set String
collectRefs (A.At _ e) = case e of
    Can.VarTopLevel _ n       -> Set.singleton n
    Can.VarLocal _            -> Set.empty
    Can.VarKernel _ _         -> Set.empty
    Can.VarCtor _ _ _ _ _     -> Set.empty
    Can.Chr _                 -> Set.empty
    Can.Str _                 -> Set.empty
    Can.Int _                 -> Set.empty
    Can.Float _               -> Set.empty
    Can.Unit                  -> Set.empty
    Can.Accessor _            -> Set.empty
    Can.List xs               -> unionMap collectRefs xs
    Can.Negate x              -> collectRefs x
    Can.Binop _ _ _ _ a b     -> collectRefs a `Set.union` collectRefs b
    Can.Lambda _ body         -> collectRefs body
    Can.Call f args           -> collectRefs f `Set.union` unionMap collectRefs args
    Can.If branches elseE     ->
        unionMap (\(c, t) -> collectRefs c `Set.union` collectRefs t) branches
            `Set.union` collectRefs elseE
    Can.Let def body          -> defRefs def `Set.union` collectRefs body
    Can.LetRec defs body      ->
        unionMap defRefs defs `Set.union` collectRefs body
    Can.LetDestruct _ rhs body -> collectRefs rhs `Set.union` collectRefs body
    Can.Case subj branches    ->
        collectRefs subj
            `Set.union` unionMap (\(Can.CaseBranch _ b) -> collectRefs b) branches
    Can.Access target _       -> collectRefs target
    Can.Update _ base fields  ->
        collectRefs base
            `Set.union` unionMap (\(Can.FieldUpdate _ fe) -> collectRefs fe)
                                 (Map.elems fields)
    Can.Record m              -> unionMap collectRefs (Map.elems m)
    Can.Tuple a b mc          ->
        collectRefs a `Set.union` collectRefs b
            `Set.union` maybe Set.empty collectRefs mc


defRefs :: Can.Def -> Set.Set String
defRefs (Can.Def _ _ body)          = collectRefs body
defRefs (Can.TypedDef _ _ _ body _) = collectRefs body
defRefs (Can.DestructDef _ body)    = collectRefs body


unionMap :: (a -> Set.Set String) -> [a] -> Set.Set String
unionMap f = foldr (Set.union . f) Set.empty


-- | Transitive closure over the call graph.
-- Starts from `roots` and expands until fixed point.
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
