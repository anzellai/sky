-- | Canonicalise a parsed module — resolve all names, qualify variables.
-- Source AST → Canonical AST
module Sky.Canonicalise.Module
    ( canonicalise
    , canonicaliseWithDeps
    , DepInfo(..)
    )
    where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import Data.IORef (readIORef)
import System.IO.Unsafe (unsafePerformIO)
import qualified Sky.AST.Source as Src
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Canonicalise.Environment as Env
import qualified Sky.Canonicalise.Expression as CanExpr
import qualified Sky.Canonicalise.Pattern as CanPat
import qualified Sky.Canonicalise.Type as CanType


-- | Information about a dependency module extracted by a prior canonicalisation
-- pass. We only need the union-constructor info to resolve cross-module ADT
-- constructors when another module imports this one with `exposing (..)`.
data DepInfo = DepInfo
    { _dep_name    :: !ModuleName.Canonical
    , _dep_unions  :: ![(String, [Can.Ctor])]   -- (type name, constructors)
    , _dep_aliases :: ![String]                 -- exported alias names
    , _dep_aliasDefs :: !(Map.Map String Can.Alias)  -- alias bodies (for type-expansion)
    , _dep_values  :: ![String]                 -- exported top-level value names
    , _dep_exports :: !Can.Exports              -- dep's own exposing clause (P2)
    }


-- | Filter a DepInfo by its own `exposing` clause. `ExportEverything` is
-- the no-op fast path (preserves legacy behaviour for `exposing (..)`).
-- When the dep declares an explicit list, the importer only sees names
-- in that list — names defined but not exposed stay package-private.
filterDepByExports :: DepInfo -> DepInfo
filterDepByExports d = case _dep_exports d of
    Can.ExportEverything -> d
    Can.ExportExplicit namesMap ->
        let keep = namesMap `Map.union` Map.empty
            isExposed n = Map.member n keep
        in d { _dep_unions  = filter (isExposed . fst) (_dep_unions d)
             , _dep_aliases = filter isExposed (_dep_aliases d)
             , _dep_aliasDefs = Map.filterWithKey (\k _ -> isExposed k) (_dep_aliasDefs d)
             , _dep_values  = filter isExposed (_dep_values d)
             }


-- | Back-compat: canonicalise with no cross-module info.
canonicalise :: Src.Module -> Either String Can.Module
canonicalise = canonicaliseWithDeps Map.empty


-- | Canonicalise a source module given a map of known dependency modules
-- (by module path string). The deps contribute their exported constructors
-- to the importer's environment when the importer uses `exposing (..)` or
-- `exposing (Type(..))`.
canonicaliseWithDeps :: Map.Map String DepInfo -> Src.Module -> Either String Can.Module
canonicaliseWithDeps deps srcMod =
    let
        modName = case Src._name srcMod of
            Just (A.At _ segs) -> ModuleName.fromRaw segs
            Nothing -> ModuleName.Canonical "Main"

        -- Build type-name → home map so unqualified cross-module type
        -- references resolve correctly (e.g. `MyCounter : Counter` where
        -- Counter is imported from another module).
        tmap = buildTypeHomeMap modName deps srcMod

        -- Detect name collisions between exposing-(..) or exposing-(name)
        -- imports. We tolerate collisions as long as the ambiguous name is
        -- never actually used unqualified in this module — that's exactly
        -- what Elm does. If any use site references a colliding unqualified
        -- name (and it isn't locally defined), we report it with a "qualify
        -- one side" suggestion.
        ambiguous = detectExposingCollisions deps (Src._imports srcMod)
        localNames = Set.fromList
            [ nm
            | A.At _ v <- Src._values srcMod
            , let A.At _ nm = Src._valueName v
            ]
        collisions = checkAmbiguousUses ambiguous localNames srcMod

        -- P2: reject `import M exposing (name)` when M doesn't export name.
        importHidingErrors = checkImportExposingAgainstDep deps (Src._imports srcMod)

        -- Build environment from imports
        env0 = Env.initialEnv modName
        env1 = foldl (processImportWith deps modName) env0 (Src._imports srcMod)

        -- Register top-level declarations in env
        env2 = registerTopLevelNames env1 (Src._values srcMod)

        -- Register unions and their constructors
        env3 = registerUnions tmap env2 (Src._unions srcMod)

        -- Register type aliases
        env4 = registerAliases tmap env3 (Src._aliases srcMod)

        -- Canonicalise declarations
        decls = canonicaliseDecls tmap env4 (Src._values srcMod)

        -- Canonicalise unions
        unions = canonicaliseUnions tmap env4 (Src._unions srcMod)

        -- Canonicalise aliases
        aliases = canonicaliseAliases tmap env4 (Src._aliases srcMod)

        -- Exports
        exports = canonicaliseExports (Src._exports srcMod)

        -- Unbound-name check. Runs against env4 (which has all imports,
        -- exposed names, constructors, and top-level decls registered).
        -- Walking order mirrors collectUnqualExprRegions but also consults
        -- the full env — so typos like `messgae` get caught at the Sky
        -- layer with a line:col, instead of falling through to `go build`.
        --
        -- Guard: only run when deps is non-empty OR the module has no
        -- user-module imports. When deps is empty (LSP single-file path,
        -- or zero-deps `canonicalise`), cross-module constructors aren't
        -- registered in env4, so references like `HomePage` from
        -- `import State exposing (..)` would be false positives.
        hasUserImports = any (not . isKernelImport) (Src._imports srcMod)
        unboundErrs
            | Map.null deps && hasUserImports = []
            | otherwise = collectUnboundNameErrors env4 srcMod
    in case (importHidingErrors, collisions, unboundErrs) of
        (err:_, _, _) -> Left err
        (_, Just err, _) -> Left err
        (_, _, err:_) -> Left err
        _ -> Right $ expandModuleAliases depAliasMap Can.Module
            { Can._name    = modName
            , Can._exports = exports
            , Can._decls   = decls
            , Can._unions  = unions
            , Can._aliases = aliases
            }
  where
    -- Build a cross-module alias map from deps so that when a value
    -- annotation references an imported record alias (e.g. `State.Model`
    -- or via `exposing (..)`), we can still expand TType → TAlias at
    -- canonicalisation time. Only exports-accessible aliases are
    -- considered — private aliases stay opaque.
    depAliasMap = collectDepAliases deps


-- | Build a map from type-name → home module. Combines:
--   * local types (unions + aliases) in the current module → current home
--   * dep types exposed via imports → dep home
buildTypeHomeMap
    :: ModuleName.Canonical
    -> Map.Map String DepInfo
    -> Src.Module
    -> Map.Map String ModuleName.Canonical
buildTypeHomeMap home deps srcMod =
    let
        localUnionNames = [ n | A.At _ u <- Src._unions srcMod
                              , let A.At _ n = Src._unionName u ]
        localAliasNames = [ n | A.At _ a <- Src._aliases srcMod
                              , let A.At _ n = Src._aliasName a ]
        localEntries = [ (n, home) | n <- localUnionNames ++ localAliasNames ]

        importSegs imp = case Src._importName imp of A.At _ segs -> segs
        importPath imp = ModuleName.joinWith "." (importSegs imp)

        -- For each import we know about (in deps), contribute its type names.
        -- We add them unconditionally — qualified access already works via
        -- TType modStr handling; this unconditional entry makes unqualified
        -- references resolve correctly too. If two imports expose the same
        -- type name, the last one wins (acceptable — shadowing is rare).
        depEntries =
            [ (typeName, _dep_name dep)
            | imp <- Src._imports srcMod
            , Just rawDep <- [Map.lookup (importPath imp) deps]
            , let dep = filterDepByExports rawDep
            , typeName <- map fst (_dep_unions dep) ++ _dep_aliases dep
            ]
    in
    Map.fromList (depEntries ++ localEntries)


-- ═══════════════════════════════════════════════════════════
-- IMPORTS
-- ═══════════════════════════════════════════════════════════

-- | P2 enforcement. For every import of the form
--   `import M exposing (a, B(..), C(Ctor1))`
-- verify that `a`, `B`, `C`, and `Ctor1` are actually exported by M.
-- Returns one error string per mismatch (in source order).
checkImportExposingAgainstDep :: Map.Map String DepInfo -> [Src.Import] -> [String]
checkImportExposingAgainstDep deps imps = concatMap check imps
  where
    check imp = case Src._importExposing imp of
        A.At _ Src.ExposingAll -> []
        A.At _ (Src.ExposingList xs) ->
            let A.At _ segs = Src._importName imp
                path = ModuleName.joinWith "." segs
                isKernel = Map.member path Env.kernelModules
            in if isKernel
                then []  -- kernel surface is defined by the registry, skip
                else case fmap filterDepByExports (Map.lookup path deps) of
                    Nothing -> []
                    Just d  ->
                        let values  = Set.fromList (_dep_values d)
                            aliases = Set.fromList (_dep_aliases d)
                            unions  = Map.fromList (_dep_unions d)
                            ctors u = [ c | Can.Ctor c _ _ _ <- Map.findWithDefault [] u unions ]
                        in concatMap (checkItem path values aliases unions ctors) xs

    checkItem path values aliases unions _ctorsOf (A.At _ e) = case e of
        Src.ExposedValue n
            | Set.member n values || Set.member n aliases -> []
            | otherwise ->
                [ "Import error: module `" ++ path ++ "` does not expose `"
                  ++ n ++ "`." ]
        Src.ExposedType n Src.Private
            | Set.member n aliases || Map.member n unions -> []
            | otherwise ->
                [ "Import error: module `" ++ path ++ "` does not expose type `"
                  ++ n ++ "`." ]
        Src.ExposedType n Src.Public
            | Set.member n aliases || Map.member n unions -> []
            | otherwise ->
                [ "Import error: module `" ++ path ++ "` does not expose type `"
                  ++ n ++ "`." ]
        Src.ExposedType n (Src.PublicCtors wanted)
            | Map.member n unions ->
                let present = Set.fromList [ c | Can.Ctor c _ _ _ <- Map.findWithDefault [] n unions ]
                    missing = [ c | c <- wanted, not (Set.member c present) ]
                in [ "Import error: module `" ++ path ++ "` exposes type `" ++ n
                     ++ "` without constructor `" ++ c ++ "`."
                   | c <- missing ]
            | otherwise ->
                [ "Import error: module `" ++ path ++ "` does not expose type `"
                  ++ n ++ "`." ]
        Src.ExposedOperator _ -> []


-- | Back-compat wrapper.
processImport :: ModuleName.Canonical -> Env.Env -> Src.Import -> Env.Env
processImport = processImportWith Map.empty


-- | Process a single import. When the import is a user module (not a
-- kernel) and we have its DepInfo, we contribute its union constructors
-- to the environment according to the exposing clause.
processImportWith :: Map.Map String DepInfo -> ModuleName.Canonical -> Env.Env -> Src.Import -> Env.Env
processImportWith deps _home env imp =
    let
        importSegs = case Src._importName imp of A.At _ segs -> segs
        importPath = ModuleName.joinWith "." importSegs
        importMod = ModuleName.Canonical importPath

        qualifier = case Src._importAlias imp of
            Just alias -> alias
            Nothing -> last importSegs

        isKernel = Map.member importPath Env.kernelModules
        kernelName = Map.findWithDefault "" importPath Env.kernelModules

        qualCtors = kernelCtorsFor kernelName

        -- For non-kernel imports we look up the dep's unions to build
        -- cross-module constructor entries.
        depCtors = case fmap filterDepByExports (Map.lookup importPath deps) of
            Just dep ->
                [ (ctorName, Env.CtorHome importMod typeName ctorName
                    (fromIntegral idx) (fromIntegral nArgs) union annot)
                | (typeName, ctors) <- _dep_unions dep
                , let union = makeUnionFor typeName ctors
                , (idx, ctor) <- zip [0::Int ..] ctors
                , let Can.Ctor ctorName _ nArgs argTys = ctor
                      annot = makeCtorAnnot importMod typeName ctorName argTys
                ]
            Nothing -> []

        -- Dep values: forward record-alias auto-constructors so that
        -- `import OtherMod exposing (..)` or `exposing (AliasName)` makes
        -- `AliasName x y z` resolve to OtherMod.AliasName at use sites.
        depVars :: [(String, Env.VarHome)]
        depVars = case fmap filterDepByExports (Map.lookup importPath deps) of
            Just dep ->
                [ (n, Env.VarTopLevel importMod)
                | n <- _dep_aliases dep ++ _dep_values dep
                ]
            Nothing -> []

        envWithQual = Env.addQualifiedImport qualifier importMod
            (if isKernel then kernelVarsFor kernelName else depVars)
            (qualCtors ++ depCtors)
            env

        -- P2: the dep's own `exposing` list limits what an importer may
        -- pull in. Build the exported-name set (kernels export everything
        -- since their surface is controlled by the kernel registry).
        depExportedNames :: String -> Bool
        depExportedNames =
            if isKernel then const True
            else case fmap filterDepByExports (Map.lookup importPath deps) of
                Nothing  -> const True  -- unknown dep → trust the import
                Just d   -> \n ->
                    n `elem` _dep_values d
                    || n `elem` _dep_aliases d
                    || n `elem` map fst (_dep_unions d)

        envWithExposed = case Src._importExposing imp of
            A.At _ Src.ExposingAll ->
                if isKernel
                then Env.addExposed (kernelVarsFor kernelName) qualCtors envWithQual
                else Env.addExposed depVars depCtors envWithQual
            A.At _ (Src.ExposingList exposed) ->
                let
                    exposedVars = concatMap (resolveExposedVar isKernel kernelName importMod) exposed
                    exposedCtorsFromKernel = concatMap (resolveExposedCtor isKernel kernelName) exposed
                    -- Also allow `exposing (Type(..))` to pull in user-module ctors
                    exposedDepCtors = concatMap (resolveDepCtors depCtors) exposed
                    -- Record-alias auto-ctors exposed via `exposing (AliasName)`
                    exposedAliasVars = concatMap (resolveAliasCtor depVars) exposed
                    -- Enforce dep's own exposing clause.
                    keep (n, _) = depExportedNames n
                    filteredVars  = filter keep (exposedVars ++ exposedAliasVars)
                    filteredCtors = filter keep (exposedCtorsFromKernel ++ exposedDepCtors)
                in Env.addExposed filteredVars filteredCtors envWithQual
    in
    envWithExposed


-- | Build a synthetic Union record for use in CtorHome. We need this to
-- represent "I know about this constructor from another module" — the real
-- Can.Union lives in the other module's canonicalised output.
makeUnionFor :: String -> [Can.Ctor] -> Can.Union
makeUnionFor typeName ctors =
    Can.Union [] ctors (length ctors)
        (if all (\(Can.Ctor _ _ n _) -> n == 0) ctors then Can.Enum else Can.Normal)


-- | Build an annotation for a constructor (T1 -> T2 -> … -> TypeName).
makeCtorAnnot :: ModuleName.Canonical -> String -> String -> [Can.Type] -> Can.Annotation
makeCtorAnnot home typeName _ctorName argTys =
    let result = Can.TType home typeName []
        ty = foldr Can.TLambda result argTys
    in Can.Forall [] ty


-- | Pick record-alias auto-constructors matching `exposing (AliasName)`.
-- If the user wrote the alias name in an exposing list, expose its ctor
-- so calls like `Piece kind colour` resolve without qualification.
resolveAliasCtor :: [(String, Env.VarHome)] -> A.Located Src.Exposed -> [(String, Env.VarHome)]
resolveAliasCtor depVarList (A.At _ exposed) = case exposed of
    Src.ExposedType typeName _ ->
        [ (typeName, vh) | (vn, vh) <- depVarList, vn == typeName ]
    Src.ExposedValue name ->
        [ (name, vh) | (vn, vh) <- depVarList, vn == name ]
    _ -> []


-- | Pick ctors matching `exposing (TypeName(..))` or `(Type(Ctor1, Ctor2))`.
resolveDepCtors :: [(String, Env.CtorHome)] -> A.Located Src.Exposed -> [(String, Env.CtorHome)]
resolveDepCtors allDepCtors (A.At _ exposed) = case exposed of
    Src.ExposedType typeName Src.Public ->
        [ (cname, ch)
        | (cname, ch) <- allDepCtors
        , Env._ch_type ch == typeName
        ]
    Src.ExposedType typeName (Src.PublicCtors wanted) ->
        [ (cname, ch)
        | (cname, ch) <- allDepCtors
        , Env._ch_type ch == typeName
        , cname `elem` wanted
        ]
    _ -> []


-- | Resolve an exposed value to a VarHome
resolveExposedVar :: Bool -> String -> ModuleName.Canonical -> A.Located Src.Exposed -> [(String, Env.VarHome)]
resolveExposedVar isKernel kernelName importMod (A.At _ exposed) = case exposed of
    Src.ExposedValue name ->
        if isKernel
        then [(name, Env.VarKernel kernelName name)]
        else [(name, Env.VarTopLevel importMod)]
    Src.ExposedType _ _ -> []
    Src.ExposedOperator _ -> []


-- | Resolve exposed constructors
resolveExposedCtor :: Bool -> String -> A.Located Src.Exposed -> [(String, Env.CtorHome)]
resolveExposedCtor _isKernel _kernelName (A.At _ exposed) = case exposed of
    Src.ExposedType _ Src.Public -> []  -- TODO: expose union constructors
    _ -> []


-- | Get kernel vars for a stdlib module
kernelVarsFor :: String -> [(String, Env.VarHome)]
kernelVarsFor modName =
    case Map.lookup modName kernelFunctions of
        Just funcs -> map (\f -> (f, Env.VarKernel modName f)) funcs
        Nothing -> []


-- | Get kernel constructors (currently none extra beyond builtins)
kernelCtorsFor :: String -> [(String, Env.CtorHome)]
kernelCtorsFor _ = []


-- | Known functions for each kernel module
-- This drives what names are available via qualified access.
-- Merged with FFI registry entries populated by Sky.Build.Compile
-- before canonicalisation — see Env.ffiKernelFunctionsRef.
{-# NOINLINE kernelFunctions #-}
kernelFunctions :: Map.Map String [String]
kernelFunctions =
    Map.unionWith (++) staticKernelFunctions
        (unsafePerformIO (readIORef Env.ffiKernelFunctionsRef))


-- | Map each unqualified name to the list of distinct canonical sources
-- that contribute it via `exposing (..)` / `exposing (name)`. Only names
-- with ≥2 distinct sources are retained — these are the ambiguous names
-- that trigger an error if referenced unqualified.
--
-- Sources that normalise to the same kernel module (e.g. `Sky.Core.Prelude`
-- re-exports `Basics` names) are treated as the same origin, so re-exports
-- never count as collisions.
detectExposingCollisions :: Map.Map String DepInfo -> [Src.Import] -> Map.Map String [String]
detectExposingCollisions deps imps =
    let contributions :: [(String, String)]
        contributions = concatMap contributionsFor imps

        byName :: Map.Map String [String]
        byName = Map.fromListWith (++)
            [(n, [src]) | (n, src) <- contributions]
    in Map.filter (\srcs -> length (distinct srcs) > 1)
       $ Map.map distinct byName
  where
    canonicalSource path = Map.findWithDefault path path Env.kernelModules

    contributionsFor :: Src.Import -> [(String, String)]
    contributionsFor imp =
        let segs = case Src._importName imp of A.At _ s -> s
            path = ModuleName.joinWith "." segs
            src  = canonicalSource path
        in case Src._importExposing imp of
            A.At _ Src.ExposingAll ->
                [(n, src) | n <- allExposedNames path]
            A.At _ (Src.ExposingList xs) ->
                [(n, src) | n <- concatMap exposedName xs]

    exposedName (A.At _ e) = case e of
        Src.ExposedValue n    -> [n]
        Src.ExposedType n _   -> [n]
        Src.ExposedOperator _ -> []

    allExposedNames path =
        let kernelName = Map.findWithDefault "" path Env.kernelModules
            kernelFns  = Map.findWithDefault [] kernelName kernelFunctions
            depFns = case fmap filterDepByExports (Map.lookup path deps) of
                Just d  -> _dep_aliases d ++ _dep_values d
                            ++ map fst (_dep_unions d)
                Nothing -> []
        in if null kernelName then depFns else kernelFns

    distinct :: Ord a => [a] -> [a]
    distinct = Map.keys . Map.fromList . map (\x -> (x, ()))


-- | Walk every value declaration for unqualified uses of names that
-- are ambiguous across imports. If any such use site exists AND the name
-- isn't defined locally in this module, report an ambiguity error.
checkAmbiguousUses
    :: Map.Map String [String]   -- ambiguous-name → candidate source list
    -> Set.Set String             -- local top-level names (shadow imports)
    -> Src.Module
    -> Maybe String
checkAmbiguousUses ambiguous localNames srcMod
    | Map.null ambiguous = Nothing
    | otherwise =
        let -- Every unqualified reference site with its region.
            allRefs :: [(String, A.Region)]
            allRefs = concatMap
                (\(A.At _ v) ->
                    let pats  = Src._valuePatterns v
                        body  = Src._valueBody v
                        shadowed = Set.union localNames
                            (Set.fromList (concatMap patternNames pats))
                    in collectUnqualExprRegions shadowed body)
                (Src._values srcMod)

            -- name → first region it was referenced at (not locally shadowed).
            firstUse :: Map.Map String A.Region
            firstUse = Map.fromListWith (\_ b -> b) (reverse allRefs)

            usedAmbiguous :: Map.Map String [String]
            usedAmbiguous = Map.filterWithKey
                (\n _ -> not (Set.member n localNames)
                         && Map.member n firstUse)
                ambiguous

            clashes = Map.toList usedAmbiguous
        in case clashes of
            [] -> Nothing
            _  -> Just (formatCollisionError firstUse clashes)
  where
    formatCollisionError :: Map.Map String A.Region -> [(String, [String])] -> String
    formatCollisionError firstUse clashes =
        let header = "Ambiguous imports: " ++ show (length clashes)
                  ++ " name(s) are exposed by more than one import AND used "
                  ++ "unqualified."
            body = concat
                [ "\n  - " ++ posTag n ++ "`" ++ n ++ "` could be from: "
                   ++ joinWithComma srcs
                   ++ "\n      Fix: add `as <Alias>` to one import and call it qualified, e.g. `import "
                   ++ head srcs ++ " as " ++ suggestAlias (head srcs)
                   ++ "` then `" ++ suggestAlias (head srcs) ++ "." ++ n ++ "`."
                | (n, srcs) <- clashes
                ]
            -- Embed the first use's position at the head of the message so
            -- LSP can place the diagnostic at a real location.
            leader = case clashes of
                ((n, _):_) -> case Map.lookup n firstUse of
                    Just (A.Region (A.Position r c) _) -> show r ++ ":" ++ show c ++ ": "
                    Nothing -> ""
                [] -> ""
        in leader ++ header ++ body

    posTag n = case Map.lookup n firstUseRef of
        Just (A.Region (A.Position r c) _) -> "(at " ++ show r ++ ":" ++ show c ++ ") "
        Nothing -> ""

    firstUseRef :: Map.Map String A.Region
    firstUseRef = Map.fromListWith (\_ b -> b) (reverse allRefsRef)

    allRefsRef :: [(String, A.Region)]
    allRefsRef = concatMap
        (\(A.At _ v) ->
            let pats  = Src._valuePatterns v
                body  = Src._valueBody v
                shadowed = Set.union localNames
                    (Set.fromList (concatMap patternNames pats))
            in collectUnqualExprRegions shadowed body)
        (Src._values srcMod)

    joinWithComma = foldr1 (\a b -> a ++ ", " ++ b)

    suggestAlias s =
        let segs = case break (== '.') s of
                (a, "") -> [a]
                (a, _:rest) -> a : splitDots rest
            lastSeg = case segs of [] -> s; _ -> last segs
        in case lastSeg of
            "Tailwind" -> "Tw"
            _          -> lastSeg

    splitDots s = case break (== '.') s of
        (a, "") -> [a]
        (a, _:rest) -> a : splitDots rest


-- | Collect "Undefined name: X" errors (with line:col positions) for every
-- unqualified variable reference that doesn't resolve against env's unqualified
-- var map, ctor map, or a pattern-bound local in scope. This is the Sky-layer
-- fence for typos like `messgae` that otherwise fall through to `go build`
-- (the historic "compiler-side bug" message the user would see).
--
-- Qualified references (e.g. `Module.thing`) and identifiers used inside
-- patterns are intentionally out of scope here — see the broader audit notes
-- in .claude/prompts/soundness-and-lsp-diagnostics.md.
collectUnboundNameErrors :: Env.Env -> Src.Module -> [String]
collectUnboundNameErrors env srcMod =
    let
        isBound n =
               Map.member n (Env._vars env)
            || Map.member n (Env._ctors env)

        collect (A.At _ v) =
            let pats     = Src._valuePatterns v
                body     = Src._valueBody v
                shadowed = Set.fromList (concatMap patternNames pats)
            in collectUnqualExprRegions shadowed body

        allRefs = concatMap collect (Src._values srcMod)
        unbound = [ (n, reg) | (n, reg) <- allRefs, not (isBound n) ]

        formatOne (n, A.Region (A.Position r c) _) =
            "Line " ++ show r ++ ", column " ++ show c
                ++ ": Undefined name: " ++ n
                ++ "\n    I cannot find a `" ++ n
                ++ "` in scope. Check for a typo, or add an import that exposes this name."
    in
        map formatOne (dedupeByName unbound)
  where
    -- If `foo` is used 12 times and all 12 are unbound, report only the
    -- first. Prevents a 12-line wall of the same message.
    dedupeByName :: [(String, A.Region)] -> [(String, A.Region)]
    dedupeByName xs =
        let step (seen, acc) (n, reg)
                | Set.member n seen = (seen, acc)
                | otherwise         = (Set.insert n seen, (n, reg) : acc)
        in reverse (snd (foldl step (Set.empty, []) xs))


-- | Same as collectUnqualExpr but also records each reference's source region.
collectUnqualExprRegions :: Set.Set String -> Src.Expr -> [(String, A.Region)]
collectUnqualExprRegions shadowed (A.At reg e) = case e of
    Src.Var n
        | Set.member n shadowed -> []
        | otherwise             -> [(n, reg)]
    Src.VarQual _ _ -> []
    Src.Call f xs -> collectUnqualExprRegions shadowed f ++ concatMap (collectUnqualExprRegions shadowed) xs
    Src.Binops pairs final ->
        concat [collectUnqualExprRegions shadowed e' | (e', _) <- pairs]
        ++ collectUnqualExprRegions shadowed final
    Src.Lambda pats body ->
        let shadowed' = Set.union shadowed (Set.fromList (concatMap patternNames pats))
        in collectUnqualExprRegions shadowed' body
    Src.If branches elseE ->
        concat [collectUnqualExprRegions shadowed a ++ collectUnqualExprRegions shadowed b | (a, b) <- branches]
        ++ collectUnqualExprRegions shadowed elseE
    Src.Let defs body ->
        let defNames = Set.fromList (concatMap defBoundNames defs)
            shadowed' = Set.union shadowed defNames
        in concatMap (defBodyExprRegions shadowed') defs
        ++ collectUnqualExprRegions shadowed' body
    Src.Case scrut arms ->
        collectUnqualExprRegions shadowed scrut
        ++ concatMap (\(p, rhs) ->
            let shadowed' = Set.union shadowed (Set.fromList (patternNames p))
            in collectUnqualExprRegions shadowed' rhs) arms
    Src.Access target _ -> collectUnqualExprRegions shadowed target
    Src.Update _ fields -> concat [collectUnqualExprRegions shadowed v | (_, v) <- fields]
    Src.Record fields   -> concat [collectUnqualExprRegions shadowed v | (_, v) <- fields]
    Src.Tuple a b cs ->
        collectUnqualExprRegions shadowed a ++ collectUnqualExprRegions shadowed b
        ++ concatMap (collectUnqualExprRegions shadowed) cs
    Src.List xs -> concatMap (collectUnqualExprRegions shadowed) xs
    Src.Negate inner -> collectUnqualExprRegions shadowed inner
    _ -> []


defBodyExprRegions :: Set.Set String -> A.Located Src.Def -> [(String, A.Region)]
defBodyExprRegions shadowed (A.At _ d) = case d of
    Src.Define _ pats body _ ->
        let shadowed' = Set.union shadowed (Set.fromList (concatMap patternNames pats))
        in collectUnqualExprRegions shadowed' body
    Src.Destruct _ body -> collectUnqualExprRegions shadowed body


-- | Collect every unqualified `Var name` reference inside an expression tree.
-- Skips qualified references (those are not ambiguous — the alias is explicit).
-- Also adds pattern-bound variables to a shadow set so a `\x -> x` shadowing
-- doesn't count as a reference to the ambiguous `x`.
collectUnqualExpr :: Set.Set String -> Src.Expr -> [String]
collectUnqualExpr shadowed (A.At _ e) = case e of
    Src.Var n
        | Set.member n shadowed -> []
        | otherwise             -> [n]
    Src.VarQual _ _ -> []
    Src.Call f xs -> collectUnqualExpr shadowed f ++ concatMap (collectUnqualExpr shadowed) xs
    Src.Binops pairs final ->
        concat [collectUnqualExpr shadowed e' | (e', _) <- pairs] ++ collectUnqualExpr shadowed final
    Src.Lambda pats body ->
        let shadowed' = Set.union shadowed (Set.fromList (concatMap patternNames pats))
        in collectUnqualExpr shadowed' body
    Src.If branches elseE ->
        concat [collectUnqualExpr shadowed a ++ collectUnqualExpr shadowed b | (a, b) <- branches]
        ++ collectUnqualExpr shadowed elseE
    Src.Let defs body ->
        let defNames = Set.fromList (concatMap defBoundNames defs)
            shadowed' = Set.union shadowed defNames
        in concatMap (defBodyExprs shadowed') defs ++ collectUnqualExpr shadowed' body
    Src.Case scrut arms ->
        collectUnqualExpr shadowed scrut
        ++ concatMap (\(p, rhs) ->
            let shadowed' = Set.union shadowed (Set.fromList (patternNames p))
            in collectUnqualExpr shadowed' rhs) arms
    Src.Access target _ -> collectUnqualExpr shadowed target
    Src.Update _ fields -> concat [collectUnqualExpr shadowed v | (_, v) <- fields]
    Src.Record fields   -> concat [collectUnqualExpr shadowed v | (_, v) <- fields]
    Src.Tuple a b cs    ->
        collectUnqualExpr shadowed a ++ collectUnqualExpr shadowed b
        ++ concatMap (collectUnqualExpr shadowed) cs
    Src.List xs         -> concatMap (collectUnqualExpr shadowed) xs
    Src.Negate inner    -> collectUnqualExpr shadowed inner
    _ -> []


collectUnqualPattern :: Set.Set String -> Src.Pattern -> [String]
collectUnqualPattern _ _ = []  -- patterns only BIND names; they don't reference


defBoundNames :: A.Located Src.Def -> [String]
defBoundNames (A.At _ d) = case d of
    Src.Define (A.At _ n) _ _ _ -> [n]
    Src.Destruct pat _          -> patternNames pat


defBodyExprs :: Set.Set String -> A.Located Src.Def -> [String]
defBodyExprs shadowed (A.At _ d) = case d of
    Src.Define _ pats body _ ->
        let shadowed' = Set.union shadowed (Set.fromList (concatMap patternNames pats))
        in collectUnqualExpr shadowed' body
    Src.Destruct _ body -> collectUnqualExpr shadowed body


-- | Variable names bound by a pattern (recursively).
patternNames :: Src.Pattern -> [String]
patternNames (A.At _ p) = case p of
    Src.PVar n        -> [n]
    Src.PCtor _ _ xs  -> concatMap patternNames xs
    Src.PCtorQual _ _ xs -> concatMap patternNames xs
    Src.PCons h t     -> patternNames h ++ patternNames t
    Src.PList xs      -> concatMap patternNames xs
    Src.PTuple a b cs -> patternNames a ++ patternNames b ++ concatMap patternNames cs
    Src.PRecord ns    -> map (\(A.At _ n) -> n) ns
    Src.PAlias inner (A.At _ n) -> n : patternNames inner
    _                 -> []


isKernelImport :: Src.Import -> Bool
isKernelImport imp =
    let segs = case Src._importName imp of A.At _ s -> s
        path = ModuleName.joinWith "." segs
    in Map.member path Env.kernelModules


staticKernelFunctions :: Map.Map String [String]
staticKernelFunctions = Map.fromList
    [ ("Basics",  ["identity", "always", "not", "toString", "modBy", "clamp", "fst", "snd",
                    "compare", "negate", "abs", "sqrt", "min", "max"])
    , ("String",  ["length", "reverse", "append", "split", "join", "contains",
                    "startsWith", "endsWith", "toInt", "fromInt", "toFloat", "fromFloat",
                    "toUpper", "toLower", "trim", "replace", "slice", "isEmpty",
                    "toBytes", "fromBytes", "fromChar", "toChar",
                    "left", "right", "padLeft", "padRight", "repeat", "lines", "words",
                    "isValid", "normalize", "normalizeNFD", "casefold", "equalFold",
                    "graphemes", "trimStart", "trimEnd",
                    "isEmail", "isUrl", "slugify",
                    "htmlEscape", "truncate", "ellipsize"])
    , ("List",    ["map", "filter", "foldl", "foldr", "length", "head", "tail",
                    "take", "drop", "append", "concat", "concatMap", "reverse",
                    "sort", "sortBy", "member", "any", "all", "range", "zip", "filterMap",
                    "parallelMap", "isEmpty", "indexedMap", "find"])
    , ("Dict",    ["empty", "insert", "get", "remove", "member", "keys", "values",
                    "toList", "fromList", "map", "foldl", "union"])
    , ("Set",     ["empty", "insert", "remove", "member", "union", "diff", "intersect", "fromList"])
    , ("Maybe",   ["withDefault", "map", "andThen", "map2", "map3", "map4", "map5",
                    "andMap", "combine", "traverse"])
    , ("Result",  ["withDefault", "map", "andThen", "mapError", "map2", "map3", "map4", "map5",
                    "andMap", "combine", "traverse", "andThenTask"])
    , ("Task",    ["succeed", "fail", "map", "andThen", "perform", "sequence", "parallel",
                    "lazy", "run", "map2", "map3", "map4", "map5", "andMap",
                    "fromResult", "andThenResult"])
    , ("Log",     ["println", "debug", "info", "warn", "error", "with", "errorWith"])
    , ("Cmd",     ["none", "batch", "perform"])
    , ("Time",    ["now", "sleep", "every", "unixMillis", "timeString",
                    "formatISO8601", "formatRFC3339", "formatHTTP", "format",
                    "parseISO8601", "parse", "addMillis", "diffMillis"])
    , ("Random",  ["int", "float", "choice", "shuffle"])
    , ("Math",    ["sqrt", "pow", "abs", "floor", "ceil", "round", "sin", "cos", "tan", "pi", "e", "log", "min", "max"])
    , ("Io",      ["readLine", "readBytes", "writeStdout", "writeStderr", "writeString"])
    , ("File",    ["readFile", "readFileLimit", "readFileBytes",
                    "writeFile", "append", "mkdirAll", "readDir", "exists", "remove", "isDir",
                    "tempFile", "copy", "rename"])
    , ("Args",    ["getArg", "getArgs"])
    , ("Process", ["run", "exit", "getEnv", "getCwd", "loadEnv"])
    , ("Http",    ["get", "post", "request"])
    , ("Server",  ["listen", "get", "post", "put", "delete", "static", "text", "json", "html",
                    "withStatus", "redirect", "param", "queryParam", "header",
                    "getCookie", "cookie", "withCookie", "withHeader", "any",
                    "method", "formValue", "body", "path", "group", "use"])
    , ("Crypto",  ["sha256", "sha512", "md5", "hmacSha256",
                    "constantTimeEqual", "randomBytes", "randomToken"])
    , ("Encoding",["base64Encode", "base64Decode", "urlEncode", "urlDecode", "hexEncode", "hexDecode"])
    , ("Regex",   ["match", "find", "findAll", "replace", "split"])
    , ("Char",    ["isUpper", "isLower", "isDigit", "isAlpha", "toUpper", "toLower"])
    , ("Path",    ["join", "dir", "base", "ext", "isAbsolute", "safeJoin"])
    , ("Uuid",    ["v4", "v7", "parse"])
    , ("RateLimit", ["allow"])
    , ("Env",     ["get", "getOrDefault", "require", "getInt", "getBool"])
    , ("Middleware", ["withCors", "withLogging", "withBasicAuth", "withRateLimit"])
    , ("Ffi",     ["call", "callPure", "callTask", "has", "isPure"])
    , ("Html",    ["text", "div", "span", "p", "h1", "h2", "h3", "h4", "h5", "h6",
                    "a", "button", "input", "form", "label", "nav", "section",
                    "article", "header", "footer", "main", "ul", "ol", "li",
                    "img", "br", "hr", "table", "thead", "tbody", "tr", "th", "td",
                    "textarea", "select", "option", "pre", "code", "strong", "em",
                    "small", "styleNode", "node", "raw", "headerNode",
                    "codeNode", "blockquote", "figure", "figcaption", "doctype",
                    "htmlNode", "headNode", "meta", "render", "body", "title",
                    "titleNode", "link", "script",
                    "details", "summary", "dialog", "video", "audio", "canvas",
                    "iframe", "progress", "meter",
                    "aside", "fieldset", "legend", "tfoot",
                    "linkNode", "mainNode", "footerNode", "voidNode",
                    "attrToString", "toString", "escapeHtml", "escapeAttr"])
    , ("Attr",    ["class", "id", "style", "type", "type_", "value", "href", "src",
                    "alt", "name", "placeholder", "title", "for", "checked",
                    "disabled", "readonly", "required", "autofocus", "rel",
                    "target", "method", "action", "attribute",
                    "charset", "content", "httpEquiv", "rel",
                    "rows", "cols", "maxlength", "minlength", "step", "min",
                    "max", "pattern", "accept", "multiple", "size", "tabindex",
                    "ariaLabel", "ariaHidden", "role", "dataAttr", "spellcheck",
                    "dir", "lang", "translate",
                    "hidden", "download", "enctype", "novalidate", "autocomplete",
                    "colspan", "rowspan", "scope", "selected", "height", "width",
                    "ariaDescribedby", "ariaExpanded", "boolAttribute", "dataAttribute"])
    , ("Css",     ["stylesheet", "rule", "property", "px", "rem", "em", "pct", "hex", "rgba",
                    "color", "background", "backgroundColor", "padding", "padding2",
                    "margin", "margin2", "fontSize", "fontWeight", "fontFamily",
                    "lineHeight", "textAlign", "textDecoration", "border", "borderRadius",
                    "borderTop", "borderBottom", "borderLeft", "borderRight", "borderColor",
                    "display", "cursor", "gap", "justifyContent", "alignItems",
                    "width", "height", "maxWidth", "minWidth", "maxHeight", "minHeight",
                    "transform", "transition", "top", "bottom", "left", "right",
                    "position", "zIndex", "opacity", "overflow", "overflowX", "overflowY",
                    "flex", "flexDirection", "flexWrap", "flexGrow", "flexShrink", "flexBasis",
                    "gridTemplateColumns", "gridTemplateRows", "gridColumn", "gridRow",
                    "gridGap", "gap", "rowGap", "columnGap", "boxShadow", "boxSizing",
                    "media", "shadow", "zero", "borderBox", "systemFont",
                    "borderCollapse", "borderSpacing",
                    "marginTop", "marginBottom", "marginLeft", "marginRight",
                    "paddingTop", "paddingBottom", "paddingLeft", "paddingRight",
                    "visibility", "content", "auto", "none", "transparent",
                    "inherit", "initial", "monoFont",
                    "textTransform", "letterSpacing",
                    "linearGradient", "repeat", "fr",
                    "margin4", "fontStyle", "styles",
                    "transitionProp", "transitionDuration", "transitionTimingFunction",
                    "outline", "outlineOffset", "filter", "backdropFilter",
                    "pointerEvents", "objectFit", "objectPosition",
                    "backgroundSize", "backgroundPosition", "backgroundRepeat",
                    "listStyle", "listStyleType", "listStylePosition",
                    "verticalAlign",
                    "vh", "vw", "ch", "deg", "ms", "sec",
                    "rgb", "hsl", "hsla",
                    "alignSelf", "alignContent", "order", "gridArea",
                    "borderWidth", "borderStyle", "textOverflow", "textShadow",
                    "clear", "float", "right_", "animation",
                    "minmax", "rotate", "scale", "translateX", "translateY",
                    "cssVar", "cssVarOr", "defineVar", "calc", "important",
                    "shadows", "borderRadius4", "padding4",
                    "keyframes", "frame", "boxSizingBorderBox"])
    , ("Live",    ["app", "route", "api"])
    , ("Event",   ["onClick", "onInput", "onChange", "onSubmit", "onDblClick",
                    "onMouseOver", "onMouseOut", "onKeyDown", "onKeyUp",
                    "onFocus", "onBlur",
                    "on", "onContextMenu", "onError", "onKeyPress", "onLoad",
                    "onMouseDown", "onMouseUp", "onReset", "onResize", "onScroll",
                    "onSelect", "onImage", "fileMaxWidth", "fileMaxHeight"])
    , ("Sub",     ["none", "every"])
    , ("Set",     ["empty", "fromList", "insert", "remove", "member", "toList",
                    "size", "union", "intersect", "diff"])
    , ("JsonEnc", ["string", "int", "float", "bool", "null", "list", "object", "encode"])
    , ("JsonDec", ["decodeString", "string", "int", "float", "bool", "field",
                    "index", "list", "map", "andThen", "succeed", "fail",
                    "oneOf", "at", "map2", "map3", "map4", "map5"])
    , ("Sha256",  ["sum256", "sum256String"])
    , ("Hex",     ["encode", "encodeToString", "decode"])
    , ("Os",      ["args", "getenv", "cwd", "exit"])
    , ("Slog",    ["info", "warn", "error", "debug"])
    , ("Context", ["background", "todo", "withValue", "withCancel"])
    , ("Fmt",     ["sprint", "sprintf", "sprintln", "errorf"])
    , ("Db",      ["connect", "open", "close", "exec", "execRaw", "query", "queryDecode",
                    "insertRow", "getById", "updateById", "deleteById",
                    "findWhere", "withTransaction",
                    "getField", "getFieldOr", "getString", "getInt", "getBool"])
    , ("Auth",    ["hashPassword", "verifyPassword", "signToken", "verifyToken",
                    "register", "login", "setRole",
                    "hashPasswordCost", "passwordStrength"])
    , ("JsonDecP",["required", "optional", "custom", "requiredAt"])
    ]


-- ═══════════════════════════════════════════════════════════
-- TOP-LEVEL REGISTRATION
-- ═══════════════════════════════════════════════════════════

-- | Register all top-level function names so they can be referenced before definition
registerTopLevelNames :: Env.Env -> [A.Located Src.Value] -> Env.Env
registerTopLevelNames env values =
    let home = Env._home env
        names = map (\(A.At _ v) -> case Src._valueName v of A.At _ n -> n) values
        varEntries = map (\n -> (n, Env.VarTopLevel home)) names
    in env { Env._vars = foldr (\(n, v) -> Map.insert n v) (Env._vars env) varEntries }


-- | Register union types and their constructors
registerUnions :: Map.Map String ModuleName.Canonical -> Env.Env -> [A.Located Src.Union] -> Env.Env
registerUnions tmap env unions =
    foldl registerUnion env unions
  where
    registerUnion e (A.At _ u) =
        let
            home = Env._home e
            typeName = case Src._unionName u of A.At _ n -> n
            vars = map (\(A.At _ v) -> v) (Src._unionVars u)
            ctorSrcs = Src._unionCtors u
            numAlts = length ctorSrcs
            ctors = zipWith (\(A.At _ (name, args)) i ->
                Can.Ctor name i (length args) (map (CanType.canonicaliseTypeAnnotationWith tmap home) args))
                ctorSrcs [0..]
            opts = if all (\(Can.Ctor _ _ arity _) -> arity == 0) ctors
                   then Can.Enum
                   else if numAlts == 1 then case ctors of [Can.Ctor _ _ 1 _] -> Can.Unbox; _ -> Can.Normal
                   else Can.Normal
            union = Can.Union vars ctors numAlts opts

            -- Build constructor annotations and env entries
            ctorEntries = map (mkCtorEntry home typeName union vars) ctors
        in e { Env._ctors = foldr (\(n, c) -> Map.insert n c) (Env._ctors e) ctorEntries }

    mkCtorEntry home typeName union vars (Can.Ctor name idx arity argTypes) =
        let resultType = Can.TType home typeName (map Can.TVar vars)
            fullType = foldr Can.TLambda resultType argTypes
            annot = Can.Forall vars fullType
        in (name, Env.CtorHome home typeName name idx arity union annot)


-- | Register type aliases. Record aliases double as constructor functions
-- (Elm convention: `type alias Foo = { a : A, b : B }` auto-generates
-- `Foo : A -> B -> Foo`). We register the alias name in `_vars` so
-- `Decode.succeed UserProfile` resolves at canonicalise time instead of
-- leaking through to Go codegen and tripping the unbound-name check.
registerAliases :: Map.Map String ModuleName.Canonical -> Env.Env -> [A.Located Src.Alias] -> Env.Env
registerAliases tmap env aliases =
    foldl registerAlias env aliases
  where
    registerAlias e (A.At _ a) =
        let
            home = Env._home e
            name = case Src._aliasName a of A.At _ n -> n
            vars = map (\(A.At _ v) -> v) (Src._aliasVars a)
            body = case Src._aliasType a of A.At _ t -> CanType.canonicaliseTypeAnnotationWith tmap home t
            info = Env.AliasInfo home vars body
            e1 = e { Env._aliases = Map.insert name info (Env._aliases e) }
            -- Record aliases expose their name as an auto-ctor value.
            -- Non-record aliases are purely type-level and don't contribute
            -- a constructor (e.g. `type alias Id = String`).
            isRecordAlias = case body of
                Can.TRecord{} -> True
                _             -> False
        in if isRecordAlias
            then e1 { Env._vars = Map.insert name (Env.VarTopLevel home) (Env._vars e1) }
            else e1


-- ═══════════════════════════════════════════════════════════
-- DECLARATIONS
-- ═══════════════════════════════════════════════════════════

-- | Canonicalise all value declarations
canonicaliseDecls :: Map.Map String ModuleName.Canonical -> Env.Env -> [A.Located Src.Value] -> Can.Decls
canonicaliseDecls tmap env values =
    foldr (\v rest -> Can.Declare (canonicaliseValue tmap env v) rest) Can.SaveTheEnvironment values


-- | Canonicalise a single value declaration
canonicaliseValue :: Map.Map String ModuleName.Canonical -> Env.Env -> A.Located Src.Value -> Can.Def
canonicaliseValue tmap env (A.At _ val) =
    let
        name = Src._valueName val
        params = Src._valuePatterns val
        body = Src._valueBody val
        mType = Src._valueType val

        -- Add parameters to environment
        paramNames = concatMap CanPat.patternNames params
        bodyEnv = Env.addLocals paramNames env

        -- Canonicalise patterns and body
        canPatterns = map (CanPat.canonicalisePattern env) params
        canBody = CanExpr.canonicaliseExpr bodyEnv body
    in
    case mType of
        Nothing ->
            Can.Def name canPatterns canBody

        Just (A.At _ srcType) ->
            let
                home = Env._home env
                canType = CanType.canonicaliseTypeAnnotationWith tmap home srcType
                freeVars = CanType.freeTypeVars srcType
                typedPatterns = zip canPatterns (arrowArgs canType)
            in
            Can.TypedDef name freeVars typedPatterns canBody (arrowResult canType)


-- | Extract argument types from a function type
arrowArgs :: Can.Type -> [Can.Type]
arrowArgs (Can.TLambda from to) = from : arrowArgs to
arrowArgs _ = []


-- | Extract the result type from a function type
arrowResult :: Can.Type -> Can.Type
arrowResult (Can.TLambda _ to) = arrowResult to
arrowResult t = t


-- ═══════════════════════════════════════════════════════════
-- UNIONS & ALIASES
-- ═══════════════════════════════════════════════════════════

canonicaliseUnions :: Map.Map String ModuleName.Canonical -> Env.Env -> [A.Located Src.Union] -> Map.Map String Can.Union
canonicaliseUnions tmap env unions =
    Map.fromList $ map (canonicaliseUnion env) unions
  where
    canonicaliseUnion e (A.At _ u) =
        let
            home = Env._home e
            name = case Src._unionName u of A.At _ n -> n
            vars = map (\(A.At _ v) -> v) (Src._unionVars u)
            ctorSrcs = Src._unionCtors u
            numAlts = length ctorSrcs
            ctors = zipWith (\(A.At _ (cname, args)) i ->
                Can.Ctor cname i (length args)
                    (map (CanType.canonicaliseTypeAnnotationWith tmap home) args))
                ctorSrcs [0..]
            opts = if all (\(Can.Ctor _ _ arity _) -> arity == 0) ctors
                   then Can.Enum
                   else Can.Normal
        in (name, Can.Union vars ctors numAlts opts)


canonicaliseAliases :: Map.Map String ModuleName.Canonical -> Env.Env -> [A.Located Src.Alias] -> Map.Map String Can.Alias
canonicaliseAliases tmap env aliases =
    Map.fromList $ map (canonicaliseAlias env) aliases
  where
    canonicaliseAlias e (A.At _ a) =
        let
            home = Env._home e
            name = case Src._aliasName a of A.At _ n -> n
            vars = map (\(A.At _ v) -> v) (Src._aliasVars a)
            body = case Src._aliasType a of A.At _ t -> CanType.canonicaliseTypeAnnotationWith tmap home t
        in (name, Can.Alias vars body)


-- ═══════════════════════════════════════════════════════════
-- ALIAS EXPANSION (post-canonicalisation)
-- ═══════════════════════════════════════════════════════════
--
-- Type annotations like `update : Msg -> Model -> (Model, Cmd Msg)`
-- canonicalise the `Model` reference into `Can.TType h "Model" []`,
-- which is a nominal reference. For HM unification to propagate
-- record-field types from Model into callers (e.g. Live.app's
-- model param), the annotation must carry the alias body so the
-- solver can unfold it on unification.
--
-- We post-process the canonical module here: walk every type that
-- appears in decls/unions/aliases and rewrite `TType h n []` to
-- `TAlias h n [] (Filled body)` whenever `n` is a known 0-arg
-- alias. The rewrite is recursive (the alias body itself gets
-- walked) with a visited-set guard so self-referential aliases
-- don't cause non-termination.
--
-- Parameterised aliases (`type alias Foo a = { x : a }`) are NOT
-- expanded — applying them requires type-var substitution that we
-- can add later. Treating them as nominal is correct, just
-- pessimistic for inference.
expandModuleAliases :: Map.Map String Can.Alias -> Can.Module -> Can.Module
expandModuleAliases depAliases m =
    let localAliases = Can._aliases m
        -- Merge local and dep aliases; local wins on name collision (unlikely).
        allAliases = Map.union localAliases depAliases
        expand = expandTypeAliases allAliases Set.empty
    in m
        { Can._decls   = mapDeclsTypes expand (Can._decls m)
        , Can._unions  = Map.map (mapUnionTypes expand) (Can._unions m)
        , Can._aliases = Map.map (mapAliasBody expand) (Can._aliases m)
        }


-- | Expand nominal type refs into TAlias nodes when they match an
-- alias in the alias map. Carries a visited-set so a recursive
-- alias (unusual but possible) can't loop.
expandTypeAliases :: Map.Map String Can.Alias -> Set.Set String -> Can.Type -> Can.Type
expandTypeAliases aliasMap visited ty = case ty of
    Can.TType home name []
        | not (Set.member name visited)
        , Just (Can.Alias vars body) <- Map.lookup name aliasMap
        , null vars ->
            let body' = expandTypeAliases aliasMap (Set.insert name visited) body
            in Can.TAlias home name [] (Can.Filled body')
    Can.TType home name args ->
        Can.TType home name (map recur args)
    Can.TLambda a b ->
        Can.TLambda (recur a) (recur b)
    Can.TTuple a b rest ->
        Can.TTuple (recur a) (recur b) (map recur rest)
    Can.TRecord fields mExt ->
        Can.TRecord
            (Map.map (\(Can.FieldType i t) -> Can.FieldType i (recur t)) fields)
            mExt
    Can.TAlias home name pairs aliasType ->
        Can.TAlias home name
            [ (n, recur t) | (n, t) <- pairs ]
            (case aliasType of
                Can.Filled  inner -> Can.Filled (recur inner)
                Can.Hoisted inner -> Can.Hoisted (recur inner))
    Can.TUnit -> Can.TUnit
    Can.TVar n -> Can.TVar n
  where
    recur = expandTypeAliases aliasMap visited


mapDeclsTypes :: (Can.Type -> Can.Type) -> Can.Decls -> Can.Decls
mapDeclsTypes f decls = case decls of
    Can.SaveTheEnvironment -> Can.SaveTheEnvironment
    Can.Declare d rest -> Can.Declare (mapDefTypes f d) (mapDeclsTypes f rest)
    Can.DeclareRec d ds rest ->
        Can.DeclareRec (mapDefTypes f d) (map (mapDefTypes f) ds) (mapDeclsTypes f rest)


mapDefTypes :: (Can.Type -> Can.Type) -> Can.Def -> Can.Def
mapDefTypes f def = case def of
    Can.TypedDef name freeVars typedPats body retType ->
        Can.TypedDef name freeVars
            [ (mapPatternLoc f p, f t) | (p, t) <- typedPats ]
            (mapExprTypes f body)
            (f retType)
    Can.Def name pats body ->
        Can.Def name (map (mapPatternLoc f) pats) (mapExprTypes f body)
    Can.DestructDef pat body ->
        Can.DestructDef (mapPatternLoc f pat) (mapExprTypes f body)


mapPatternLoc :: (Can.Type -> Can.Type) -> Can.Pattern -> Can.Pattern
mapPatternLoc f (A.At r pat) = A.At r (mapPattern_ f pat)


mapPattern_ :: (Can.Type -> Can.Type) -> Can.Pattern_ -> Can.Pattern_
mapPattern_ f pat = case pat of
    Can.PAlias inner name -> Can.PAlias (mapPatternLoc f inner) name
    Can.PTuple a b rest ->
        Can.PTuple (mapPatternLoc f a) (mapPatternLoc f b) (map (mapPatternLoc f) rest)
    Can.PList xs -> Can.PList (map (mapPatternLoc f) xs)
    Can.PCons h t -> Can.PCons (mapPatternLoc f h) (mapPatternLoc f t)
    Can.PCtor home typeName union ctorName idx args ->
        Can.PCtor home typeName union ctorName idx
            (map (\(Can.PatternCtorArg i ty p) ->
                Can.PatternCtorArg i (f ty) (mapPatternLoc f p)) args)
    other -> other


-- | Walk an expression, rewriting Type references (notably the
-- constructor annotations carried on Can.VarCtor). Without this, a
-- constructor like `Error Io (mkInfo msg)` that takes an ErrorInfo
-- argument keeps its arg-type as `TType ErrorInfo` (nominal), while
-- mkInfo's HM-inferred return type is `TAlias ErrorInfo` (expanded);
-- the unifier can't reconcile TRecord-after-alias-unfold with the
-- nominal TType, so the ctor call fails to type-check.
mapExprTypes :: (Can.Type -> Can.Type) -> Can.Expr -> Can.Expr
mapExprTypes f (A.At r e) = A.At r (mapExpr_ f e)


mapExpr_ :: (Can.Type -> Can.Type) -> Can.Expr_ -> Can.Expr_
mapExpr_ f e = case e of
    Can.VarCtor opts home typeName ctorName (Can.Forall vars ty) ->
        Can.VarCtor opts home typeName ctorName (Can.Forall vars (f ty))
    Can.Binop op home name (Can.Forall vars ty) l r ->
        Can.Binop op home name (Can.Forall vars (f ty))
            (mapExprTypes f l) (mapExprTypes f r)
    Can.List xs -> Can.List (map (mapExprTypes f) xs)
    Can.Negate inner -> Can.Negate (mapExprTypes f inner)
    Can.Lambda pats body ->
        Can.Lambda (map (mapPatternLoc f) pats) (mapExprTypes f body)
    Can.Call fn args ->
        Can.Call (mapExprTypes f fn) (map (mapExprTypes f) args)
    Can.If branches elseBr ->
        Can.If
            [ (mapExprTypes f c, mapExprTypes f t) | (c, t) <- branches ]
            (mapExprTypes f elseBr)
    Can.Let def body ->
        Can.Let (mapDefTypes f def) (mapExprTypes f body)
    Can.LetRec defs body ->
        Can.LetRec (map (mapDefTypes f) defs) (mapExprTypes f body)
    Can.LetDestruct pat val body ->
        Can.LetDestruct (mapPatternLoc f pat)
            (mapExprTypes f val) (mapExprTypes f body)
    Can.Case subj branches ->
        Can.Case (mapExprTypes f subj)
            [ Can.CaseBranch (mapPatternLoc f p) (mapExprTypes f b)
            | Can.CaseBranch p b <- branches ]
    Can.Access target field -> Can.Access (mapExprTypes f target) field
    Can.Update name base fields ->
        Can.Update name (mapExprTypes f base)
            (Map.map (\(Can.FieldUpdate reg expr) ->
                Can.FieldUpdate reg (mapExprTypes f expr)) fields)
    Can.Record fields ->
        Can.Record (Map.map (mapExprTypes f) fields)
    Can.Tuple a b rest ->
        Can.Tuple (mapExprTypes f a) (mapExprTypes f b) (map (mapExprTypes f) rest)
    other -> other


mapUnionTypes :: (Can.Type -> Can.Type) -> Can.Union -> Can.Union
mapUnionTypes f u = u
    { Can._u_alts = map (\(Can.Ctor n idx arity argTypes) ->
        Can.Ctor n idx arity (map f argTypes)) (Can._u_alts u)
    }


mapAliasBody :: (Can.Type -> Can.Type) -> Can.Alias -> Can.Alias
mapAliasBody f (Can.Alias vars body) = Can.Alias vars (f body)


-- | Collect the canonicalised alias bodies from dep modules so a
-- value annotation can refer to an imported record alias and still
-- have its body expanded for HM unification. Local aliases win on
-- collision (unlikely in practice).
collectDepAliases :: Map.Map String DepInfo -> Map.Map String Can.Alias
collectDepAliases deps =
    Map.unions [ _dep_aliasDefs d | d <- Map.elems deps ]


-- ═══════════════════════════════════════════════════════════
-- EXPORTS
-- ═══════════════════════════════════════════════════════════

canonicaliseExports :: A.Located Src.Exposing -> Can.Exports
canonicaliseExports (A.At _ Src.ExposingAll) = Can.ExportEverything
canonicaliseExports (A.At _ (Src.ExposingList exposed)) =
    Can.ExportExplicit $ Map.fromList $
        concatMap (\(A.At r e) -> case e of
            Src.ExposedValue name -> [(name, r)]
            Src.ExposedType name _ -> [(name, r)]
            Src.ExposedOperator name -> [(name, r)]
        ) exposed
