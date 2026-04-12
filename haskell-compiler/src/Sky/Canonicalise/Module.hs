-- | Canonicalise a parsed module — resolve all names, qualify variables.
-- Source AST → Canonical AST
module Sky.Canonicalise.Module
    ( canonicalise
    )
    where

import qualified Data.Map.Strict as Map
import qualified Sky.AST.Source as Src
import qualified Sky.AST.Canonical as Can
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Sky.ModuleName as ModuleName
import qualified Sky.Canonicalise.Environment as Env
import qualified Sky.Canonicalise.Expression as CanExpr
import qualified Sky.Canonicalise.Pattern as CanPat
import qualified Sky.Canonicalise.Type as CanType


-- | Canonicalise a source module into a canonical module
canonicalise :: Src.Module -> Either String Can.Module
canonicalise srcMod =
    let
        modName = case Src._name srcMod of
            Just (A.At _ segs) -> ModuleName.fromRaw segs
            Nothing -> ModuleName.Canonical "Main"

        -- Build environment from imports
        env0 = Env.initialEnv modName
        env1 = foldl (processImport modName) env0 (Src._imports srcMod)

        -- Register top-level declarations in env
        env2 = registerTopLevelNames env1 (Src._values srcMod)

        -- Register unions and their constructors
        env3 = registerUnions env2 (Src._unions srcMod)

        -- Register type aliases
        env4 = registerAliases env3 (Src._aliases srcMod)

        -- Canonicalise declarations
        decls = canonicaliseDecls env4 (Src._values srcMod)

        -- Canonicalise unions
        unions = canonicaliseUnions env4 (Src._unions srcMod)

        -- Canonicalise aliases
        aliases = canonicaliseAliases env4 (Src._aliases srcMod)

        -- Exports
        exports = canonicaliseExports (Src._exports srcMod)
    in
    Right $ Can.Module
        { Can._name    = modName
        , Can._exports = exports
        , Can._decls   = decls
        , Can._unions  = unions
        , Can._aliases = aliases
        }


-- ═══════════════════════════════════════════════════════════
-- IMPORTS
-- ═══════════════════════════════════════════════════════════

-- | Process a single import declaration into the environment
processImport :: ModuleName.Canonical -> Env.Env -> Src.Import -> Env.Env
processImport _home env imp =
    let
        importSegs = case Src._importName imp of A.At _ segs -> segs
        importPath = ModuleName.joinWith "." importSegs
        importMod = ModuleName.Canonical importPath

        -- Determine the qualifier (alias or last segment)
        qualifier = case Src._importAlias imp of
            Just alias -> alias
            Nothing -> last importSegs

        -- Check if this is a kernel (stdlib) module
        isKernel = Map.member importPath Env.kernelModules
        kernelName = Map.findWithDefault "" importPath Env.kernelModules

        -- Build qualified ctor maps for this import
        qualCtors = kernelCtorsFor kernelName

        -- Add qualified access: Task.succeed, String.fromInt, etc.
        envWithQual = Env.addQualifiedImport qualifier importMod
            (if isKernel then kernelVarsFor kernelName else [])
            qualCtors
            env

        -- Handle exposing
        envWithExposed = case Src._importExposing imp of
            A.At _ Src.ExposingAll ->
                if isKernel
                then Env.addExposed (kernelVarsFor kernelName) qualCtors envWithQual
                else envWithQual
            A.At _ (Src.ExposingList exposed) ->
                let
                    exposedVars = concatMap (resolveExposedVar isKernel kernelName importMod) exposed
                    exposedCtors = concatMap (resolveExposedCtor isKernel kernelName) exposed
                in Env.addExposed exposedVars exposedCtors envWithQual
    in
    envWithExposed


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
-- This drives what names are available via qualified access
kernelFunctions :: Map.Map String [String]
kernelFunctions = Map.fromList
    [ ("Basics",  ["identity", "always", "not", "toString", "modBy", "clamp", "fst", "snd",
                    "compare", "negate", "abs", "sqrt", "min", "max"])
    , ("String",  ["length", "reverse", "append", "split", "join", "contains",
                    "startsWith", "endsWith", "toInt", "fromInt", "toFloat", "fromFloat",
                    "toUpper", "toLower", "trim", "replace", "slice", "isEmpty"])
    , ("List",    ["map", "filter", "foldl", "foldr", "length", "head", "tail",
                    "take", "drop", "append", "concat", "concatMap", "reverse",
                    "sort", "member", "any", "all", "range", "zip", "filterMap",
                    "parallelMap"])
    , ("Dict",    ["empty", "insert", "get", "remove", "member", "keys", "values",
                    "toList", "fromList", "map", "foldl", "union"])
    , ("Set",     ["empty", "insert", "remove", "member", "union", "diff", "intersect", "fromList"])
    , ("Maybe",   ["withDefault", "map", "andThen"])
    , ("Result",  ["withDefault", "map", "andThen", "mapError", "map2", "map3", "map4", "map5",
                    "andMap", "combine", "traverse"])
    , ("Task",    ["succeed", "fail", "map", "andThen", "perform", "sequence", "parallel",
                    "lazy", "run", "map2", "map3", "map4", "map5", "andMap"])
    , ("Log",     ["println"])
    , ("Cmd",     ["none", "batch", "perform"])
    , ("Time",    ["now", "sleep", "every", "unixMillis"])
    , ("Random",  ["int", "float", "choice", "shuffle"])
    , ("Math",    ["sqrt", "pow", "abs", "floor", "ceil", "round", "sin", "cos", "tan", "pi", "e", "log", "min", "max"])
    , ("Io",      ["readLine", "readBytes", "writeStdout", "writeStderr"])
    , ("File",    ["readFile", "writeFile", "append", "mkdirAll", "readDir", "exists", "remove", "isDir"])
    , ("Process", ["run", "exit", "getEnv", "getCwd", "loadEnv"])
    , ("Http",    ["get", "post", "request"])
    , ("Server",  ["listen", "get", "post", "put", "delete", "static", "text", "json", "html"])
    , ("Crypto",  ["sha256", "sha512", "md5", "hmacSha256"])
    , ("Encoding",["base64Encode", "base64Decode", "urlEncode", "urlDecode", "hexEncode", "hexDecode"])
    , ("Regex",   ["match", "find", "findAll", "replace", "split"])
    , ("Char",    ["isUpper", "isLower", "isDigit", "isAlpha", "toUpper", "toLower"])
    , ("Path",    ["join", "dir", "base", "ext", "isAbsolute"])
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
registerUnions :: Env.Env -> [A.Located Src.Union] -> Env.Env
registerUnions env unions =
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
                Can.Ctor name i (length args) (map (CanType.canonicaliseTypeAnnotation home) args))
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


-- | Register type aliases
registerAliases :: Env.Env -> [A.Located Src.Alias] -> Env.Env
registerAliases env aliases =
    foldl registerAlias env aliases
  where
    registerAlias e (A.At _ a) =
        let
            home = Env._home e
            name = case Src._aliasName a of A.At _ n -> n
            vars = map (\(A.At _ v) -> v) (Src._aliasVars a)
            body = case Src._aliasType a of A.At _ t -> CanType.canonicaliseTypeAnnotation home t
            info = Env.AliasInfo home vars body
        in e { Env._aliases = Map.insert name info (Env._aliases e) }


-- ═══════════════════════════════════════════════════════════
-- DECLARATIONS
-- ═══════════════════════════════════════════════════════════

-- | Canonicalise all value declarations
canonicaliseDecls :: Env.Env -> [A.Located Src.Value] -> Can.Decls
canonicaliseDecls env values =
    foldr (\v rest -> Can.Declare (canonicaliseValue env v) rest) Can.SaveTheEnvironment values


-- | Canonicalise a single value declaration
canonicaliseValue :: Env.Env -> A.Located Src.Value -> Can.Def
canonicaliseValue env (A.At _ val) =
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
                canType = CanType.canonicaliseTypeAnnotation home srcType
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

canonicaliseUnions :: Env.Env -> [A.Located Src.Union] -> Map.Map String Can.Union
canonicaliseUnions env unions =
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
                    (map (CanType.canonicaliseTypeAnnotation home) args))
                ctorSrcs [0..]
            opts = if all (\(Can.Ctor _ _ arity _) -> arity == 0) ctors
                   then Can.Enum
                   else Can.Normal
        in (name, Can.Union vars ctors numAlts opts)


canonicaliseAliases :: Env.Env -> [A.Located Src.Alias] -> Map.Map String Can.Alias
canonicaliseAliases env aliases =
    Map.fromList $ map (canonicaliseAlias env) aliases
  where
    canonicaliseAlias e (A.At _ a) =
        let
            home = Env._home e
            name = case Src._aliasName a of A.At _ n -> n
            vars = map (\(A.At _ v) -> v) (Src._aliasVars a)
            body = case Src._aliasType a of A.At _ t -> CanType.canonicaliseTypeAnnotation home t
        in (name, Can.Alias vars body)


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
