-- | Canonicalise patterns — resolve constructor references, extract bindings.
module Sky.Canonicalise.Pattern
    ( canonicalisePattern
    , patternNames
    )
    where

import qualified Sky.AST.Source as Src
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Canonicalise.Environment as Env


-- | Extract all variable names bound by a pattern
patternNames :: Src.Pattern -> [String]
patternNames (A.At _ pat) = case pat of
    Src.PVar name      -> [name]
    Src.PAnything       -> []
    Src.PRecord fields  -> map (\(A.At _ n) -> n) fields
    Src.PAlias inner (A.At _ name) -> name : patternNames inner
    Src.PUnit           -> []
    Src.PTuple a b rest -> patternNames a ++ patternNames b ++ concatMap patternNames rest
    Src.PCtor _ _ args    -> concatMap patternNames args
    Src.PCtorQual _ _ args-> concatMap patternNames args
    Src.PList items     -> concatMap patternNames items
    Src.PCons h t       -> patternNames h ++ patternNames t
    Src.PChr _          -> []
    Src.PStr _          -> []
    Src.PInt _          -> []
    Src.PFloat _        -> []
    Src.PBool _         -> []


-- | Canonicalise a source pattern
canonicalisePattern :: Env.Env -> Src.Pattern -> Can.Pattern
canonicalisePattern env (A.At region pat) =
    A.At region $ canonicalisePattern_ env pat


canonicalisePattern_ :: Env.Env -> Src.Pattern_ -> Can.Pattern_
canonicalisePattern_ env pat = case pat of
    Src.PAnything ->
        Can.PAnything

    Src.PVar name ->
        Can.PVar name

    Src.PRecord fields ->
        Can.PRecord (map (\(A.At _ n) -> n) fields)

    Src.PAlias inner (A.At _ name) ->
        Can.PAlias (canonicalisePattern env inner) name

    Src.PUnit ->
        Can.PUnit

    Src.PTuple a b rest ->
        Can.PTuple
            (canonicalisePattern env a)
            (canonicalisePattern env b)
            (case rest of
                [] -> Nothing
                (r:_) -> Just (canonicalisePattern env r))

    Src.PCtor ctorName modSegments args ->
        resolveCtorPattern env modSegments ctorName args

    Src.PCtorQual qualifier ctorName args ->
        resolveQualCtorPattern env qualifier ctorName args

    Src.PList items ->
        Can.PList (map (canonicalisePattern env) items)

    Src.PCons h t ->
        Can.PCons (canonicalisePattern env h) (canonicalisePattern env t)

    Src.PChr c ->
        Can.PChr c

    Src.PStr s ->
        Can.PStr s

    Src.PInt n ->
        Can.PInt n

    Src.PFloat f ->
        error $ "Float patterns not supported: " ++ show f

    Src.PBool b ->
        Can.PBool b


-- | Resolve a constructor pattern (e.g., Ok, Err, Just, Nothing)
-- The parser gives us PCtor with segments being module path parts + name
resolveCtorPattern :: Env.Env -> [String] -> String -> [Src.Pattern] -> Can.Pattern_
resolveCtorPattern env _segments ctorName args =
    case Env.lookupCtor ctorName env of
        Just ctor ->
            let canArgs = map (canonicalisePattern env) args
                union = Env._ch_union ctor
                argTypes = case lookupCtorArgs ctorName union of
                    Just ts -> ts
                    Nothing -> replicate (length args) (Can.TVar "a")
                ctorArgs = zipWith (\i (pat, ty) -> Can.PatternCtorArg i ty pat)
                    [0..] (zip canArgs argTypes)
            in Can.PCtor
                (Env._ch_home ctor)
                (Env._ch_type ctor)
                union
                (Env._ch_name ctor)
                (Env._ch_index ctor)
                ctorArgs
        Nothing ->
            -- Unknown constructor — proceed as an anonymous variable
            -- binding so downstream stages can at least produce a type
            -- error rather than crashing the whole compiler.
            Can.PVar ctorName


-- | Resolve a qualified constructor pattern (e.g., Maybe.Just)
resolveQualCtorPattern :: Env.Env -> String -> String -> [Src.Pattern] -> Can.Pattern_
resolveQualCtorPattern env qualifier ctorName args =
    case Env.lookupQualCtor qualifier ctorName env of
        Just ctor ->
            let canArgs = map (canonicalisePattern env) args
                union = Env._ch_union ctor
                argTypes = case lookupCtorArgs ctorName union of
                    Just ts -> ts
                    Nothing -> replicate (length args) (Can.TVar "a")
                ctorArgs = zipWith (\i (pat, ty) -> Can.PatternCtorArg i ty pat)
                    [0..] (zip canArgs argTypes)
            in Can.PCtor
                (Env._ch_home ctor)
                (Env._ch_type ctor)
                union
                (Env._ch_name ctor)
                (Env._ch_index ctor)
                ctorArgs
        Nothing ->
            error $ "Unknown qualified constructor in pattern: " ++ qualifier ++ "." ++ ctorName


-- | Look up constructor argument types from a Union
lookupCtorArgs :: String -> Can.Union -> Maybe [Can.Type]
lookupCtorArgs name (Can.Union _ ctors _ _) =
    case filter (\(Can.Ctor n _ _ _) -> n == name) ctors of
        (Can.Ctor _ _ _ argTypes : _) -> Just argTypes
        _ -> Nothing
