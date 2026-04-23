-- | Environment for canonicalisation (name resolution).
-- Tracks imports, aliases, constructors, and local bindings.
module Sky.Canonicalise.Environment where

import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import Data.IORef (IORef, newIORef, readIORef)
import System.IO.Unsafe (unsafePerformIO)
import qualified Sky.AST.Canonical as Can
import qualified Sky.Sky.ModuleName as ModuleName


-- | The canonicalisation environment
data Env = Env
    { _home       :: !ModuleName.Canonical
    , _vars       :: !(Map.Map String VarHome)
    , _types      :: !(Map.Map String TypeHome)
    , _ctors      :: !(Map.Map String CtorHome)
    , _aliases    :: !(Map.Map String AliasInfo)
    , _qualVars   :: !(Map.Map String (Map.Map String VarHome))
    , _qualTypes  :: !(Map.Map String (Map.Map String TypeHome))
    , _qualCtors  :: !(Map.Map String (Map.Map String CtorHome))
    , _importAliases :: !(Map.Map String ModuleName.Canonical)  -- alias → full module name
    }
    deriving (Show)


-- | Where a variable lives
data VarHome
    = VarLocal
    | VarTopLevel !ModuleName.Canonical
    | VarKernel !String !String   -- kernel module, function
    deriving (Show)


-- | Where a type lives
data TypeHome = TypeHome
    { _th_home :: !ModuleName.Canonical
    , _th_name :: !String
    , _th_arity :: !Int
    }
    deriving (Show)


-- | Where a constructor lives
data CtorHome = CtorHome
    { _ch_home  :: !ModuleName.Canonical
    , _ch_type  :: !String       -- the union type it belongs to
    , _ch_name  :: !String       -- constructor name
    , _ch_index :: !Int          -- constructor index in the union
    , _ch_arity :: !Int          -- number of arguments
    , _ch_union :: !Can.Union    -- the full union info
    , _ch_annot :: !Can.Annotation  -- constructor type
    }
    deriving (Show)


-- | Type alias info
data AliasInfo = AliasInfo
    { _ai_home :: !ModuleName.Canonical
    , _ai_vars :: [String]
    , _ai_type :: !Can.Type
    }
    deriving (Show)


-- ═══════════════════════════════════════════════════════════
-- CONSTRUCTION
-- ═══════════════════════════════════════════════════════════

-- | Create a base environment with Sky's built-in types and constructors
initialEnv :: ModuleName.Canonical -> Env
initialEnv home = Env
    { _home      = home
    , _vars      = Map.fromList builtinVars
    , _types     = Map.fromList builtinTypes
    , _ctors     = Map.fromList builtinCtors
    , _aliases   = Map.empty
    , _qualVars  = Map.fromList preludeQualVars
    , _qualTypes = Map.empty
    , _qualCtors = Map.empty
    , _importAliases = Map.empty
    }


-- | Qualifier aliases auto-available without explicit import.
-- Matches Elm/Sky convention where `String.join`, `List.map`, etc.
-- work without writing `import String`. Kernel functions resolve the
-- same way as if the user had written `import Sky.Core.<Mod> as <Mod>`.
preludeQualVars :: [(String, Map.Map String VarHome)]
preludeQualVars =
    [ (qual, Map.fromList [(fn, VarKernel qual fn) | fn <- funcs])
    | (qual, funcs) <- preludeQualifiers
    ]


-- | Auto-qualified kernel modules. Only names commonly used unqualified
-- from the Sky convention.
preludeQualifiers :: [(String, [String])]
preludeQualifiers =
    [ ("String", ["length", "reverse", "append", "split", "join", "contains",
                    "startsWith", "endsWith", "toInt", "fromInt", "toFloat", "fromFloat",
                    "toUpper", "toLower", "trim", "replace", "slice", "isEmpty",
                    "toBytes", "fromBytes", "fromChar", "toChar",
                    "left", "right", "padLeft", "padRight", "repeat", "lines", "words",
                    "htmlEscape", "truncate", "ellipsize"])
    , ("List",   ["map", "filter", "foldl", "foldr", "length", "head", "tail",
                    "take", "drop", "append", "concat", "concatMap", "reverse",
                    "sort", "member", "any", "all", "range", "zip", "filterMap",
                    "parallelMap", "isEmpty", "cons"])
    , ("Dict",   ["empty", "insert", "get", "remove", "member", "keys", "values",
                    "toList", "fromList", "map", "foldl", "union"])
    , ("Set",    ["empty", "insert", "remove", "member", "union", "diff", "intersect", "fromList"])
    , ("Maybe",  ["withDefault", "map", "andThen"])
    , ("Result", ["withDefault", "map", "andThen", "mapError",
                    "map2", "map3", "map4", "map5", "andMap", "combine", "traverse",
                    "andThenTask"])
    , ("Basics", ["identity", "always", "not", "toString", "modBy", "clamp",
                    "fst", "snd", "compare", "negate", "abs", "sqrt", "min", "max"])
    , ("Cmd",    ["none", "batch", "perform"])
    , ("Sub",    ["none", "every"])
    , ("Task",   ["succeed", "fail", "map", "andThen", "perform", "sequence",
                    "parallel", "lazy", "run", "map2", "map3", "map4", "map5", "andMap",
                    "fromResult", "andThenResult"])
    ]


-- | Add a local variable binding
addLocal :: String -> Env -> Env
addLocal name env =
    env { _vars = Map.insert name VarLocal (_vars env) }


-- | Add multiple local variable bindings
addLocals :: [String] -> Env -> Env
addLocals names env = foldr addLocal env names


-- | Add a qualified import alias
addQualifiedImport :: String -> ModuleName.Canonical -> [(String, VarHome)] -> [(String, CtorHome)] -> Env -> Env
addQualifiedImport alias modName vars ctors env = env
    { _qualVars = Map.insertWith Map.union alias (Map.fromList vars) (_qualVars env)
    , _qualCtors = Map.insertWith Map.union alias (Map.fromList ctors) (_qualCtors env)
    , _importAliases = Map.insert alias modName (_importAliases env)
    }


-- | Add exposed names from an import
addExposed :: [(String, VarHome)] -> [(String, CtorHome)] -> Env -> Env
addExposed vars ctors env = env
    { _vars = foldr (\(n, v) -> Map.insert n v) (_vars env) vars
    , _ctors = foldr (\(n, c) -> Map.insert n c) (_ctors env) ctors
    }


-- ═══════════════════════════════════════════════════════════
-- LOOKUP
-- ═══════════════════════════════════════════════════════════

lookupVar :: String -> Env -> Maybe VarHome
lookupVar name env = Map.lookup name (_vars env)


lookupQualVar :: String -> String -> Env -> Maybe VarHome
lookupQualVar qualifier name env = do
    modVars <- Map.lookup qualifier (_qualVars env)
    Map.lookup name modVars


lookupCtor :: String -> Env -> Maybe CtorHome
lookupCtor name env = Map.lookup name (_ctors env)


lookupQualCtor :: String -> String -> Env -> Maybe CtorHome
lookupQualCtor qualifier name env = do
    modCtors <- Map.lookup qualifier (_qualCtors env)
    Map.lookup name modCtors


lookupImportAlias :: String -> Env -> Maybe ModuleName.Canonical
lookupImportAlias alias env = Map.lookup alias (_importAliases env)


lookupType :: String -> Env -> Maybe TypeHome
lookupType name env = Map.lookup name (_types env)


lookupAlias :: String -> Env -> Maybe AliasInfo
lookupAlias name env = Map.lookup name (_aliases env)


-- ═══════════════════════════════════════════════════════════
-- BUILT-INS
-- ═══════════════════════════════════════════════════════════

-- | Built-in variables (from Prelude)
builtinVars :: [(String, VarHome)]
builtinVars =
    [ ("identity",    VarKernel "Basics" "identity")
    , ("always",      VarKernel "Basics" "always")
    , ("not",         VarKernel "Basics" "not")
    , ("toString",    VarKernel "Basics" "toString")
    , ("modBy",       VarKernel "Basics" "modBy")
    , ("clamp",       VarKernel "Basics" "clamp")
    , ("fst",         VarKernel "Basics" "fst")
    , ("snd",         VarKernel "Basics" "snd")
    , ("errorToString", VarKernel "Basics" "errorToString")
    , ("println",     VarKernel "Log" "println")
    , ("js",          VarKernel "Basics" "js")
    ]


-- | Built-in types
builtinTypes :: [(String, TypeHome)]
builtinTypes =
    [ ("Int",    TypeHome ModuleName.basics "Int" 0)
    , ("Float",  TypeHome ModuleName.basics "Float" 0)
    , ("Bool",   TypeHome ModuleName.basics "Bool" 0)
    , ("String", TypeHome ModuleName.basics "String" 0)
    , ("Char",   TypeHome ModuleName.basics "Char" 0)
    , ("List",   TypeHome ModuleName.list "List" 1)
    , ("Maybe",  TypeHome ModuleName.maybe_ "Maybe" 1)
    , ("Result", TypeHome ModuleName.result_ "Result" 2)
    , ("Task",   TypeHome ModuleName.task "Task" 2)
    ]


-- | Built-in constructors (Ok, Err, Just, Nothing, True, False)
builtinCtors :: [(String, CtorHome)]
builtinCtors =
    let
        boolUnion = Can.Union [] [Can.Ctor "True" 0 0 [], Can.Ctor "False" 1 0 []] 2 Can.Enum
        boolType = Can.TType ModuleName.basics "Bool" []

        maybeUnion = Can.Union ["a"]
            [ Can.Ctor "Just" 0 1 [Can.TVar "a"]
            , Can.Ctor "Nothing" 1 0 []
            ] 2 Can.Normal
        maybeAnnotJust = Can.Forall ["a"] (Can.TLambda (Can.TVar "a") (Can.TType ModuleName.maybe_ "Maybe" [Can.TVar "a"]))
        maybeAnnotNothing = Can.Forall ["a"] (Can.TType ModuleName.maybe_ "Maybe" [Can.TVar "a"])

        resultUnion = Can.Union ["e", "a"]
            [ Can.Ctor "Ok" 0 1 [Can.TVar "a"]
            , Can.Ctor "Err" 1 1 [Can.TVar "e"]
            ] 2 Can.Normal
        resultAnnotOk = Can.Forall ["e", "a"] (Can.TLambda (Can.TVar "a") (Can.TType ModuleName.result_ "Result" [Can.TVar "e", Can.TVar "a"]))
        resultAnnotErr = Can.Forall ["e", "a"] (Can.TLambda (Can.TVar "e") (Can.TType ModuleName.result_ "Result" [Can.TVar "e", Can.TVar "a"]))
    in
    [ ("True",    CtorHome ModuleName.basics "Bool" "True" 0 0 boolUnion (Can.Forall [] boolType))
    , ("False",   CtorHome ModuleName.basics "Bool" "False" 1 0 boolUnion (Can.Forall [] boolType))
    , ("Just",    CtorHome ModuleName.maybe_ "Maybe" "Just" 0 1 maybeUnion maybeAnnotJust)
    , ("Nothing", CtorHome ModuleName.maybe_ "Maybe" "Nothing" 1 0 maybeUnion maybeAnnotNothing)
    , ("Ok",      CtorHome ModuleName.result_ "Result" "Ok" 0 1 resultUnion resultAnnotOk)
    , ("Err",     CtorHome ModuleName.result_ "Result" "Err" 1 1 resultUnion resultAnnotErr)
    ]


-- | Dynamic kernel-module extensions (populated from ffi/*.kernel.json by
-- Sky.Build.Compile before canonicalisation begins). Looked up via
-- unsafePerformIO so downstream callers see a merged static ∪ dynamic map
-- with no threading churn.
{-# NOINLINE ffiKernelModulesRef #-}
ffiKernelModulesRef :: IORef (Map.Map String String)
ffiKernelModulesRef = unsafePerformIO (newIORef Map.empty)


{-# NOINLINE ffiKernelFunctionsRef #-}
ffiKernelFunctionsRef :: IORef (Map.Map String [String])
ffiKernelFunctionsRef = unsafePerformIO (newIORef Map.empty)


-- | P7: names of FFI kernel functions (in the <Kernel>_<func> shape,
-- e.g. "Go_Uuid_newString") for which a typed T-suffix wrapper has
-- been emitted by FfiGen. Call-site codegen consults this set to
-- decide whether to emit the typed variant directly.
--
-- Populated by `Sky.Build.Compile.seedTypedFfiNames`, which scans
-- the examples' ffi/*.go files and records every `^func Go_X_yT(`
-- definition. Empty unless seeded, in which case the fallback is the
-- any/any wrapper (safe default — Go build will surface a missing
-- T name if the caller wrongly assumes it exists).
{-# NOINLINE ffiTypedWrapperNamesRef #-}
ffiTypedWrapperNamesRef :: IORef (Set.Set String)
ffiTypedWrapperNamesRef = unsafePerformIO (newIORef Set.empty)


-- | P7: per-typed-wrapper param Go types, for call-site coercion of
-- non-literal args. Keyed by the T-suffix wrapper name (e.g.
-- "Go_Uuid_parseT" → ["string"]). Populated by seedTypedFfiNames
-- alongside ffiTypedWrapperNamesRef.
{-# NOINLINE ffiTypedWrapperParamsRef #-}
ffiTypedWrapperParamsRef :: IORef (Map.Map String [String])
ffiTypedWrapperParamsRef = unsafePerformIO (newIORef Map.empty)


-- | Kernel module mappings: Sky import path → kernel module name.
-- Merged on every read so FFI-registered modules resolve the same way as
-- stdlib kernel modules (Sky.Core.String etc.).
{-# NOINLINE kernelModules #-}
kernelModules :: Map.Map String String
kernelModules = Map.union staticKernelModules (unsafePerformIO (readIORef ffiKernelModulesRef))


staticKernelModules :: Map.Map String String
staticKernelModules = Map.fromList
    [ ("Sky.Core.Basics",  "Basics")
    , ("Sky.Core.String",  "String")
    , ("Sky.Core.List",    "List")
    , ("Sky.Core.Dict",    "Dict")
    , ("Sky.Core.Set",     "Set")
    , ("Sky.Core.Maybe",   "Maybe")
    , ("Sky.Core.Result",  "Result")
    , ("Sky.Core.Task",    "Task")
    , ("Sky.Core.Math",    "Math")
    , ("Sky.Core.Regex",   "Regex")
    , ("Sky.Core.Crypto",  "Crypto")
    , ("Sky.Core.Encoding","Encoding")
    , ("Sky.Core.Char",    "Char")
    , ("Sky.Core.Path",    "Path")
    , ("Std.Log",          "Log")
    , ("Std.Cmd",          "Cmd")
    , ("Std.Sub",          "Sub")
    , ("Std.Db",           "Db")
    , ("Std.Auth",         "Auth")
    , ("Sky.Core.Io",      "Io")
    , ("Io",               "Io")
    , ("Sky.Core.Args",    "Args")
    , ("Args",             "Args")
    , ("Sky.Core.File",    "File")
    , ("Sky.Core.Process", "Process")
    , ("Sky.Core.Time",    "Time")
    , ("Std.Time",         "Time")
    , ("Sky.Core.Random",  "Random")
    , ("Sky.Core.Http",    "Http")
    , ("Sky.Http.Server",  "Server")
    , ("Std.Html",             "Html")
    , ("Std.Html.Attributes",  "Attr")
    , ("Std.Css",              "Css")
    , ("Std.Live",             "Live")
    , ("Std.Live.Events",      "Event")
    , ("Std.Html.Events",      "Event")
    , ("Sky.Core.Json.Encode", "JsonEnc")
    , ("Sky.Core.Json.Decode", "JsonDec")
    , ("Sky.Core.Json.Decode.Pipeline", "JsonDecP")
    , ("Sky.Core.Uuid",        "Uuid")
    , ("Sky.Core.Crypto.Sha256", "Sha256")
    , ("Crypto.Sha256",          "Sha256")
    , ("Sky.Core.Encoding.Hex",  "Hex")
    , ("Encoding.Hex",           "Hex")
    , ("Sky.Core.Os",            "Os")
    , ("Std.Os",                 "Os")
    , ("Os",                     "Os")
    , ("Sky.Core.Slog",          "Slog")
    , ("Std.Slog",               "Slog")
    , ("Std.Log.Slog",           "Slog")
    , ("Log.Slog",               "Slog")
    , ("Slog",                   "Slog")
    , ("Context",                "Context")
    , ("Fmt",                    "Fmt")
    , ("Time",                   "Time")
    , ("Crypto",                 "Crypto")
    , ("Encoding",               "Encoding")
    , ("Sky.Http.RateLimit",   "RateLimit")
    , ("Std.Env",              "Env")
    , ("Sky.Http.Middleware",  "Middleware")
    , ("Sky.Ffi",              "Ffi")
    , ("Sky.Core.Prelude", "Basics")  -- Prelude maps to Basics
    ]
