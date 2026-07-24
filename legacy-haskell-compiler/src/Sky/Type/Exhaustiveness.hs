-- | Pattern-match exhaustiveness checking.
--
-- Minimal, deterministic checker that flags the common "missing
-- constructor" class of bugs at compile time. For ADT case expressions
-- it verifies that every constructor of the subject's union appears in
-- at least one branch (or that a wildcard/variable pattern exists).
--
-- Limitations (conscious, documented):
--   * Only top-level case arm heads are analysed. A branch whose pattern
--     is `Just Nothing -> ...` covers the `Just` head; any second-level
--     case within that branch carries its own check. This catches the
--     common "forgot `Nothing` arm" bug (P3 acceptance) and is strictly
--     conservative (no false positives).
--   * Int/String/Float/Char have infinite value spaces; we require a
--     wildcard arm and otherwise warn.
--
-- Returns a list of diagnostics — one per non-exhaustive case expression
-- found, in source order. An empty list means "all cases exhaustive".
module Sky.Type.Exhaustiveness
    ( Diag (..)
    , checkModule
    , checkBranches
    , Coverage (..)
    ) where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A


data Diag = Diag
    { _diag_location :: !A.Region
    , _diag_missing  :: ![String]
    , _diag_hint     :: !String
    }
    deriving (Show)


checkModule :: Can.Module -> [Diag]
checkModule m = concatMap checkDef (collectDefs (Can._decls m))
  where
    collectDefs :: Can.Decls -> [Can.Def]
    collectDefs (Can.Declare d rest)     = d : collectDefs rest
    collectDefs (Can.DeclareRec d ds rest) = d : ds ++ collectDefs rest
    collectDefs Can.SaveTheEnvironment   = []


checkDef :: Can.Def -> [Diag]
checkDef (Can.Def _ _ body)          = checkLocated body
checkDef (Can.TypedDef _ _ _ body _) = checkLocated body
checkDef (Can.DestructDef _ body)    = checkLocated body


checkLocated :: Can.Expr -> [Diag]
checkLocated (A.At _ e) = checkExpr e


checkExpr :: Can.Expr_ -> [Diag]
checkExpr e = case e of
    Can.Case subject branches ->
        let heads = [ pat | Can.CaseBranch (A.At _ pat) _ <- branches ]
            hereDiag = case checkBranches heads of
                Exhaustive    -> []
                Missing names ->
                    let A.At reg _ = subject
                        hint = "This `case` does not cover: " ++
                               renderMissing names ++ ". Add the missing "
                               ++ (if length names == 1 then "branch" else "branches")
                               ++ " or use `_ -> ...` explicitly."
                    in [Diag reg names hint]
            bodyDiags    = concatMap (\(Can.CaseBranch _ b) -> checkLocated b) branches
            subjectDiags = checkLocated subject
        in hereDiag ++ subjectDiags ++ bodyDiags
    Can.Lambda _args body               -> checkLocated body
    Can.Call f args                     -> checkLocated f ++ concatMap checkLocated args
    Can.Binop _ _ _ _ l r               -> checkLocated l ++ checkLocated r
    Can.If conds els                    ->
        concatMap (\(c, t) -> checkLocated c ++ checkLocated t) conds
            ++ checkLocated els
    Can.Let def body                    -> checkDef def ++ checkLocated body
    Can.LetRec defs body                -> concatMap checkDef defs ++ checkLocated body
    Can.LetDestruct _ rhs body          -> checkLocated rhs ++ checkLocated body
    Can.Access r _                      -> checkLocated r
    Can.Update _ r fields               ->
        checkLocated r
            ++ concatMap (\(_, Can.FieldUpdate _ v) -> checkLocated v) (Map.toList fields)
    Can.Record fields                   -> concatMap (checkLocated . snd) (Map.toList fields)
    Can.List xs                         -> concatMap checkLocated xs
    Can.Tuple a b rest                  ->
        checkLocated a ++ checkLocated b ++ concatMap checkLocated rest
    Can.Negate x                        -> checkLocated x
    _                                   -> []


data Coverage = Exhaustive | Missing [String]
    deriving (Show, Eq)


checkBranches :: [Can.Pattern_] -> Coverage
checkBranches pats
    | any isWildcard pats = Exhaustive
    | otherwise = case classify pats of
        Nothing -> Exhaustive
        Just (AdtArms covered allCtors) ->
            let missing = [ c | c <- allCtors, not (Set.member c covered) ]
            in if null missing then Exhaustive else Missing missing
        Just BoolArms ->
            let present = Set.fromList [ b | Can.PBool b <- pats ]
                all2    = Set.fromList [True, False]
                missing = [ show b | b <- Set.toList (Set.difference all2 present) ]
            in if null missing then Exhaustive else Missing missing
        Just UnitArms -> Exhaustive
        Just LitArms  -> Missing ["_"]
  where
    isWildcard Can.PAnything = True
    isWildcard (Can.PVar _)  = True
    isWildcard (Can.PAlias _ _) = True  -- alias at head behaves like wildcard
    isWildcard _             = False


data ShapeKind
    = AdtArms (Set.Set String) [String]
    | BoolArms
    | UnitArms
    | LitArms


classify :: [Can.Pattern_] -> Maybe ShapeKind
classify = go Nothing Set.empty
  where
    go acc names [] = case acc of
        Just (AdtArms _ allCtors) -> Just (AdtArms names allCtors)
        other                     -> other
    go acc names (p:ps) = case p of
        Can.PCtor _ _ union cname _ _ ->
            let allCtors = [ n | Can.Ctor n _ _ _ <- Can._u_alts union ]
                acc'     = case acc of
                    Just (AdtArms _ _) -> acc
                    _                  -> Just (AdtArms Set.empty allCtors)
                names'   = Set.insert cname names
            in go acc' names' ps
        Can.PBool _ -> go (chooseBool acc) names ps
        Can.PUnit   -> go (chooseUnit acc) names ps
        Can.PInt _  -> go (chooseLit acc) names ps
        Can.PStr _  -> go (chooseLit acc) names ps
        Can.PChr _  -> go (chooseLit acc) names ps
        _           -> go acc names ps

    chooseBool (Just (AdtArms _ _)) = Just BoolArms  -- shouldn't mix; boolean wins
    chooseBool Nothing              = Just BoolArms
    chooseBool other                = other

    chooseUnit Nothing = Just UnitArms
    chooseUnit other   = other

    chooseLit (Just _) = Just LitArms
    chooseLit Nothing  = Just LitArms


renderMissing :: [String] -> String
renderMissing []  = "<none>"
renderMissing [a] = "`" ++ a ++ "`"
renderMissing xs  = commaAnd (map (\x -> "`" ++ x ++ "`") xs)
  where
    commaAnd [a,b]   = a ++ " and " ++ b
    commaAnd (a:rs)  = a ++ ", " ++ commaAnd rs
    commaAnd []      = ""
