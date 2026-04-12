-- | Canonicalise a parsed module — resolve all names, qualify variables.
-- Source AST → Canonical AST
module Sky.Canonicalise.Module
    ( canonicalise
    , canonicaliseWithDeps
    , DepInfo(..)
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


-- | Information about a dependency module extracted by a prior canonicalisation
-- pass. We only need the union-constructor info to resolve cross-module ADT
-- constructors when another module imports this one with `exposing (..)`.
data DepInfo = DepInfo
    { _dep_name  :: !ModuleName.Canonical
    , _dep_unions :: ![(String, [Can.Ctor])]   -- (type name, constructors)
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

        -- Build environment from imports
        env0 = Env.initialEnv modName
        env1 = foldl (processImportWith deps modName) env0 (Src._imports srcMod)

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
        depCtors = case Map.lookup importPath deps of
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

        -- Dep values (top-level bindings) — a fuller pass; for now we just
        -- forward the top-level names if the dep exposes them. The current
        -- registerTopLevelNames elsewhere already handles values at the
        -- module-merging step in Compile.hs, so we deliberately leave
        -- dep-value import as a no-op here.
        depVars :: [(String, Env.VarHome)]
        depVars = []

        envWithQual = Env.addQualifiedImport qualifier importMod
            (if isKernel then kernelVarsFor kernelName else depVars)
            (qualCtors ++ depCtors)
            env

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
                in Env.addExposed exposedVars (exposedCtorsFromKernel ++ exposedDepCtors) envWithQual
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


-- | Pick ctors matching `exposing (TypeName(..))`.
resolveDepCtors :: [(String, Env.CtorHome)] -> A.Located Src.Exposed -> [(String, Env.CtorHome)]
resolveDepCtors allDepCtors (A.At _ exposed) = case exposed of
    Src.ExposedType typeName Src.Public ->
        -- Dep ctors are already keyed by ctor name; filter those whose
        -- home type matches. We tagged them with the type name during
        -- construction via CtorHome._ch_typeName.
        [ (cname, ch)
        | (cname, ch) <- allDepCtors
        , Env._ch_type ch == typeName
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
-- This drives what names are available via qualified access
kernelFunctions :: Map.Map String [String]
kernelFunctions = Map.fromList
    [ ("Basics",  ["identity", "always", "not", "toString", "modBy", "clamp", "fst", "snd",
                    "compare", "negate", "abs", "sqrt", "min", "max"])
    , ("String",  ["length", "reverse", "append", "split", "join", "contains",
                    "startsWith", "endsWith", "toInt", "fromInt", "toFloat", "fromFloat",
                    "toUpper", "toLower", "trim", "replace", "slice", "isEmpty",
                    "left", "right", "padLeft", "padRight", "repeat", "lines", "words",
                    "isValid", "normalize", "normalizeNFD", "casefold", "equalFold",
                    "graphemes", "trimStart", "trimEnd",
                    "isEmail", "isUrl", "slugify",
                    "htmlEscape", "truncate", "ellipsize"])
    , ("List",    ["map", "filter", "foldl", "foldr", "length", "head", "tail",
                    "take", "drop", "append", "concat", "concatMap", "reverse",
                    "sort", "member", "any", "all", "range", "zip", "filterMap",
                    "parallelMap", "isEmpty"])
    , ("Dict",    ["empty", "insert", "get", "remove", "member", "keys", "values",
                    "toList", "fromList", "map", "foldl", "union"])
    , ("Set",     ["empty", "insert", "remove", "member", "union", "diff", "intersect", "fromList"])
    , ("Maybe",   ["withDefault", "map", "andThen"])
    , ("Result",  ["withDefault", "map", "andThen", "mapError", "map2", "map3", "map4", "map5",
                    "andMap", "combine", "traverse"])
    , ("Task",    ["succeed", "fail", "map", "andThen", "perform", "sequence", "parallel",
                    "lazy", "run", "map2", "map3", "map4", "map5", "andMap"])
    , ("Log",     ["println", "debug", "info", "warn", "error", "with", "errorWith"])
    , ("Cmd",     ["none", "batch", "perform"])
    , ("Time",    ["now", "sleep", "every", "unixMillis",
                    "formatISO8601", "formatRFC3339", "formatHTTP", "format",
                    "parseISO8601", "parse", "addMillis", "diffMillis"])
    , ("Random",  ["int", "float", "choice", "shuffle"])
    , ("Math",    ["sqrt", "pow", "abs", "floor", "ceil", "round", "sin", "cos", "tan", "pi", "e", "log", "min", "max"])
    , ("Io",      ["readLine", "readBytes", "writeStdout", "writeStderr", "writeString"])
    , ("File",    ["readFile", "readFileLimit", "readFileBytes",
                    "writeFile", "append", "mkdirAll", "readDir", "exists", "remove", "isDir"])
    , ("Process", ["run", "exit", "getEnv", "getCwd", "loadEnv"])
    , ("Http",    ["get", "post", "request"])
    , ("Server",  ["listen", "get", "post", "put", "delete", "static", "text", "json", "html",
                    "withStatus", "redirect", "param", "queryParam", "header",
                    "getCookie", "cookie", "withCookie", "withHeader", "any"])
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
                    "small", "styleNode"])
    , ("Attr",    ["class", "id", "style", "type", "type_", "value", "href", "src",
                    "alt", "name", "placeholder", "title", "for", "checked",
                    "disabled", "readonly", "required", "autofocus", "rel",
                    "target", "method", "action"])
    , ("Css",     ["stylesheet", "rule", "property", "px", "rem", "em", "pct", "hex",
                    "color", "background", "backgroundColor", "padding", "padding2",
                    "margin", "margin2", "fontSize", "fontWeight", "fontFamily",
                    "lineHeight", "textAlign", "border", "borderRadius",
                    "borderBottom", "display", "cursor", "gap", "justifyContent",
                    "alignItems", "width", "height", "maxWidth", "minWidth", "transform"])
    , ("Live",    ["app", "route"])
    , ("Event",   ["onClick", "onInput", "onChange", "onSubmit", "onDblClick",
                    "onMouseOver", "onMouseOut", "onKeyDown", "onKeyUp",
                    "onFocus", "onBlur"])
    , ("Sub",     ["none", "every"])
    , ("Set",     ["empty", "fromList", "insert", "remove", "member", "toList",
                    "size", "union", "intersect", "diff"])
    , ("JsonEnc", ["string", "int", "float", "bool", "null", "list", "object", "encode"])
    , ("JsonDec", ["decodeString", "string", "int", "float", "bool", "field", "list",
                    "map", "andThen", "succeed", "fail",
                    "at", "map2", "map3", "map4", "map5"])
    , ("Db",      ["connect", "open", "close", "exec", "query", "queryDecode",
                    "insertRow", "getById", "updateById", "deleteById",
                    "findWhere", "withTransaction"])
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
