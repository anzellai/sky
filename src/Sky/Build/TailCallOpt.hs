-- | Sky.Build.TailCallOpt — compile-time TCO for tail-recursive Sky
-- functions.
--
-- Sky source like
--
--     foldl fn acc list =
--         case list of
--             [] -> acc
--             (x :: rest) -> foldl fn (fn x acc) rest
--
-- compiles by default to a non-tail-recursive Go function (Go has
-- no TCO).  For long lists this grows the goroutine stack
-- linearly and can hit the 1 GB default cap.  This module detects
-- the "every recursive call is in tail position" pattern and
-- rewrites the emitted Go body as a `for {}` loop where each tail
-- call becomes a parameter-reassignment + `continue` — constant
-- stack regardless of list length.
--
-- Scope: top-level function declarations whose body is either
-- `Can.Case`, `Can.If`, `Can.Let`, or a direct recursive call,
-- and where every self-reference appears in tail position.  Out
-- of scope (for now): non-tail recursion (`map`, `filter`,
-- `foldr`, `length`), mutual recursion, let-rec.
module Sky.Build.TailCallOpt
    ( isTailRecursive
    , countTailSelfCalls
    , countNonTailSelfCalls
    , rewriteTailCalls
    , tcoMarker
    , tcoMarkerParams
    -- v0.17 PR-5 — region enumeration at tail positions.  Required
    -- by 'Sky.Build.RendererParitySpec' (pre-mortem lesson 2) so the
    -- foundation parity property gates against the exact regions
    -- the lowerer touches when emitting the @continue@-block
    -- reassignment.  Without this coverage, PR-13's structural
    -- σ-migration silently breaks tail-recursive functions and the
    -- bisection lands at PR-13 instead of here.
    , tailPositionRegions
    ) where

import qualified Data.Map.Strict as Map
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName


-- | True iff `body` references `(home, name)` at some position AND
-- every such reference appears in tail position with the matching
-- arity.  Mutual recursion + indirect self-reference (via a closure
-- captured higher up) are deliberately NOT recognised — out of
-- scope for the v0.14.x TCO drop.
isTailRecursive
    :: ModuleName.Canonical
    -> String
    -> Int
    -> Can.Expr
    -> Bool
isTailRecursive home name arity body =
    let tailN = countTailSelfCalls home name arity body
        nonTailN = countNonTailSelfCalls home name arity body
    in tailN > 0 && nonTailN == 0


-- | Count self-references in tail position.  Mirrors `walk`'s
-- recursion shape but tracks the count rather than rewriting.
countTailSelfCalls
    :: ModuleName.Canonical
    -> String
    -> Int
    -> Can.Expr
    -> Int
countTailSelfCalls home name arity = walk True
  where
    walk :: Bool -> Can.Expr -> Int
    walk inTail (A.At _ e) = case e of
        -- Tail position structural propagators
        Can.Case _ branches | inTail ->
            sum [walk True b | Can.CaseBranch _ b <- branches]
        Can.If branches elseExpr | inTail ->
            sum [walk True b | (_, b) <- branches] + walk True elseExpr
        Can.Let _ body | inTail ->
            walk True body
        Can.LetRec _ body | inTail ->
            walk True body
        Can.LetDestruct _ _ body | inTail ->
            walk True body

        -- Tail-position self call: count it (and DO NOT recurse
        -- into args, which themselves are in non-tail position).
        Can.Call (A.At _ (Can.VarTopLevel h n)) args
            | inTail, h == home, n == name, length args == arity ->
                1 + sum (map (walk False) args)
        Can.Call (A.At _ (Can.VarLocal n)) args
            | inTail, n == name, length args == arity ->
                1 + sum (map (walk False) args)

        -- Anything else: walk subterms in NON-tail context.
        _ -> walkChildren e

    walkChildren :: Can.Expr_ -> Int
    walkChildren e = case e of
        Can.VarLocal _ -> 0
        Can.VarTopLevel _ _ -> 0
        Can.VarKernel _ _ -> 0
        Can.VarCtor{} -> 0
        Can.Chr _ -> 0
        Can.Str _ -> 0
        Can.Int _ -> 0
        Can.Float _ -> 0
        Can.List items -> sum (map (walk False) items)
        Can.Negate e' -> walk False e'
        Can.Binop _ _ _ _ l r -> walk False l + walk False r
        Can.Lambda _ body -> walk False body
        Can.Call f args -> walk False f + sum (map (walk False) args)
        Can.If branches elseExpr ->
            sum [walk False c + walk False b | (c, b) <- branches]
                + walk False elseExpr
        Can.Let def body -> walkDef def + walk False body
        Can.LetRec defs body -> sum (map walkDef defs) + walk False body
        Can.LetDestruct _ val body -> walk False val + walk False body
        Can.Case subj branches ->
            walk False subj
                + sum [walk False b | Can.CaseBranch _ b <- branches]
        Can.Accessor _ -> 0
        Can.Access target _ -> walk False target
        Can.Update _ target fields ->
            walk False target + sum (map walkFieldUpdate (Map.elems fields))
        Can.Record fields ->
            sum [walk False v | (_, v) <- Map.toList fields]
        Can.Unit -> 0
        Can.Tuple a b mc ->
            walk False a + walk False b + sum (map (walk False) mc)

    walkDef (Can.Def _ _ body) = walk False body
    walkDef (Can.TypedDef _ _ _ body _) = walk False body
    walkDef (Can.DestructDef _ body) = walk False body

    walkFieldUpdate (Can.FieldUpdate _ e) = walk False e

    walkFieldUpdate (Can.FieldUpdate _ e) = walk False e


-- | The synthetic kernel-module name we use to mark a Can.Call as
-- a "tail-call jump" rather than a real function call.  Picked to
-- be impossible in any real Sky source so codegen can recognise it
-- unambiguously.
tcoMarker :: String
tcoMarker = "__tco_jump__"


-- | Build the VarKernel function-name slot from the param-name
-- list.  The lowerer recovers the param-name list by splitting on
-- this delimiter.
tcoMarkerParams :: [String] -> String
tcoMarkerParams = foldr (\p acc -> if null acc then p else p ++ "\x1F" ++ acc) ""


-- | Rewrite a body so every TAIL-position self-call to `(home,
-- name)` with the right arity becomes a sentinel Can.Call to
-- `VarKernel tcoMarker "<param-name-list>"`.  Non-tail
-- references stay untouched (and `isTailRecursive` should have
-- ruled them out before this is called).
rewriteTailCalls
    :: ModuleName.Canonical
    -> String
    -> Int
    -> [String]           -- param names in their function-signature order
    -> Can.Expr
    -> Can.Expr
rewriteTailCalls home name arity paramNames = walk True
  where
    marker = A.At A.one (Can.VarKernel tcoMarker (tcoMarkerParams paramNames))

    walk inTail e@(A.At r inner) = case inner of
        Can.Case subj branches | inTail ->
            A.At r (Can.Case subj [Can.CaseBranch p (walk True b) | Can.CaseBranch p b <- branches])
        Can.If branches elseExpr | inTail ->
            A.At r (Can.If [(c, walk True b) | (c, b) <- branches] (walk True elseExpr))
        Can.Let def body | inTail ->
            A.At r (Can.Let def (walk True body))
        Can.LetRec defs body | inTail ->
            A.At r (Can.LetRec defs (walk True body))
        Can.LetDestruct pat val body | inTail ->
            A.At r (Can.LetDestruct pat val (walk True body))

        Can.Call (A.At _ (Can.VarTopLevel h n)) args
            | inTail, h == home, n == name, length args == arity ->
                A.At r (Can.Call marker args)
        Can.Call (A.At _ (Can.VarLocal n)) args
            | inTail, n == name, length args == arity ->
                A.At r (Can.Call marker args)

        _ -> e


-- | Count self-references that appear in NON-tail position.  A
-- function with `countNonTailSelfCalls = 0 && countTailSelfCalls
-- > 0` is fully tail-recursive and safe to TCO-transform.
countNonTailSelfCalls
    :: ModuleName.Canonical
    -> String
    -> Int
    -> Can.Expr
    -> Int
countNonTailSelfCalls home name arity = walk True
  where
    walk :: Bool -> Can.Expr -> Int
    walk inTail (A.At _ e) = case e of
        Can.Case _ branches | inTail ->
            sum [walk True b | Can.CaseBranch _ b <- branches]
        Can.If branches elseExpr | inTail ->
            sum [walk True b | (_, b) <- branches] + walk True elseExpr
        Can.Let _ body | inTail ->
            walk True body
        Can.LetRec _ body | inTail ->
            walk True body
        Can.LetDestruct _ _ body | inTail ->
            walk True body

        -- Tail-position self call: don't count, but recurse args
        -- in NON-tail context (in case they ALSO contain self-
        -- references — those would be non-tail).
        Can.Call (A.At _ (Can.VarTopLevel h n)) args
            | inTail, h == home, n == name, length args == arity ->
                sum (map (walk False) args)
        Can.Call (A.At _ (Can.VarLocal n)) args
            | inTail, n == name, length args == arity ->
                sum (map (walk False) args)

        -- Non-tail position self-call: count.
        Can.Call (A.At _ (Can.VarTopLevel h n)) args
            | h == home, n == name ->
                1 + sum (map (walk False) args)
        Can.Call (A.At _ (Can.VarLocal n)) args
            | n == name ->
                1 + sum (map (walk False) args)

        -- Anything else: walk subterms in NON-tail context.
        _ -> walkChildren e

    walkChildren :: Can.Expr_ -> Int
    walkChildren e = case e of
        Can.VarLocal _ -> 0
        Can.VarTopLevel _ _ -> 0
        Can.VarKernel _ _ -> 0
        Can.VarCtor{} -> 0
        Can.Chr _ -> 0
        Can.Str _ -> 0
        Can.Int _ -> 0
        Can.Float _ -> 0
        Can.List items -> sum (map (walk False) items)
        Can.Negate e' -> walk False e'
        Can.Binop _ _ _ _ l r -> walk False l + walk False r
        Can.Lambda _ body -> walk False body
        Can.Call f args -> walk False f + sum (map (walk False) args)
        Can.If branches elseExpr ->
            sum [walk False c + walk False b | (c, b) <- branches]
                + walk False elseExpr
        Can.Let def body -> walkDef def + walk False body
        Can.LetRec defs body -> sum (map walkDef defs) + walk False body
        Can.LetDestruct _ val body -> walk False val + walk False body
        Can.Case subj branches ->
            walk False subj
                + sum [walk False b | Can.CaseBranch _ b <- branches]
        Can.Accessor _ -> 0
        Can.Access target _ -> walk False target
        Can.Update _ target fields ->
            walk False target + sum (map walkFieldUpdate (Map.elems fields))
        Can.Record fields ->
            sum [walk False v | (_, v) <- Map.toList fields]
        Can.Unit -> 0
        Can.Tuple a b mc ->
            walk False a + walk False b + sum (map (walk False) mc)

    walkDef (Can.Def _ _ body) = walk False body
    walkDef (Can.TypedDef _ _ _ body _) = walk False body
    walkDef (Can.DestructDef _ body) = walk False body

    walkFieldUpdate (Can.FieldUpdate _ e) = walk False e


-- | Enumerate every 'A.Region' that sits at a tail position in @body@.
--
-- Tail-position propagators (walking with @inTail = True@):
--
--   * 'Can.Case' branches
--   * 'Can.If' branches AND else expression
--   * 'Can.Let' / 'Can.LetRec' / 'Can.LetDestruct' bodies
--
-- Every other expression breaks tail context — its sub-expressions are
-- walked with @inTail = False@ and their regions are NOT collected.
--
-- Used by 'test/Sky/Build/RendererParitySpec.hs' (pre-mortem lesson 2)
-- to gate the foundation parity property on the EXACT regions
-- 'rewriteTailCalls' will later rewrite into @continue@-block
-- reassignments. Without this coverage, PR-13's structural σ
-- migration silently breaks TCO and the bisection trail leads back
-- here, not there.
--
-- Returns regions in source-order (left-to-right, depth-first).
tailPositionRegions :: Can.Expr -> [A.Region]
tailPositionRegions = walk True
  where
    walk :: Bool -> Can.Expr -> [A.Region]
    walk inTail (A.At r e) =
        let here = if inTail then [r] else []
        in case e of
            Can.Case _ branches | inTail ->
                here ++ concat [walk True b | Can.CaseBranch _ b <- branches]
            Can.If branches elseExpr | inTail ->
                here
                    ++ concat [walk True b | (_, b) <- branches]
                    ++ walk True elseExpr
            Can.Let _ body | inTail ->
                here ++ walk True body
            Can.LetRec _ body | inTail ->
                here ++ walk True body
            Can.LetDestruct _ _ body | inTail ->
                here ++ walk True body
            -- Leaf / non-propagator at tail position — record just here.
            _ | inTail -> here
            -- Non-tail — don't collect, don't recurse (the tail set is
            -- purely about regions the TCO rewriter rewrites; arg
            -- positions of a non-tail call site are out of scope).
            _ -> []
