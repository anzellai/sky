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
    , _kernelMods :: !(Map.Map String String)
        -- ^ v0.17 close P1 step 6b — merged static + FFI kernel modules
        --   map (mirrors @kernelModules ()@ but on the value channel).
        --   Populated by 'Sky.Canonicalise.Module.canonicaliseWithDeps'
        --   at entry; read by 'Canonicalise.Expression.resolveQualVar'
        --   to resolve unqualified-import kernel qualifier references.
        --   Empty for LSP single-module path where no FFI loader is
        --   in scope (kernel resolution falls back to static surface).
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

-- | Create a base environment with Sky's built-in types and constructors.
--
-- 'initialEnv' leaves '_kernelMods' empty by default — the LSP single-
-- module path uses this shape directly.  'canonicaliseWithDeps' replaces
-- the field with the merged static + FFI kernel-modules map per build
-- so kernel-name resolution reads the value channel instead of the
-- legacy @kernelModules ()@ IORef.
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
    , _kernelMods = Map.empty
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
    , ("Sub",    ["none", "every", "batch"])
    , ("Task",   ["succeed", "fail", "map", "andThen", "perform", "sequence",
                    "parallel", "lazy", "run", "map2", "map3", "map4", "map5", "andMap",
                    "fromResult", "andThenResult", "mapError", "onError"])
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
    -- Sky.Core.Error.Error is the canonical Sky error type
    -- (v0.10.0 consolidation). Auto-imported so every Sky source
    -- can write `Result Error a` without an explicit
    -- `import Sky.Core.Error exposing (Error)`.
    , ("Error",  TypeHome (ModuleName.Canonical "Sky.Core.Error") "Error" 0)
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


-- v0.17 close iter 7 (Phase 7 IORef defusing) — the legacy
-- 'ffiKernelModulesRef' + 'ffiKernelFunctionsRef' + 'ffiKernelTypeRef'
-- IORefs (Sky import path → kernel name; kernel name → function names;
-- per-(kernel, fn) Sky annotation) have been deleted. All three values
-- flow purely via 'Sky.Build.Compile.LoadedFfiTables._lft_kernel{Modules,
-- Functions,Types}' → 'Sky.Build.CompileCtx._ctx_kernel{Modules,
-- Functions,Types}', threaded through 'canonPhase' + 'solvePhase' from
-- the v0.17 P1 step 6 land. All three IORefs were paired-mirror writes
-- (3 'writeIORef' calls in 'loadAndSeedFfiRegistry'; ZERO 'readIORef'
-- callsites in non-comment code) before deletion — the threaded value
-- channel had already landed for every reader site (kernelFunctions
-- merge in 'Canonicalise.Module', kernel-name resolution via 'Env._kernelMods'
-- in 'Canonicalise.Expression', kernel-type lookup via 'Constrain.Expression'
-- which now reads from threaded CompileCtx).


-- v0.17 close iter 9 (Phase 7 IORef defusing) — the legacy
-- 'ffiKernelArityRef' IORef (per-FFI-function arity, populated by
-- 'Sky.Build.Compile.loadAndSeedFfiRegistry') has been DELETED.
-- It was WRITE-ONLY: exactly one 'writeIORef' callsite, ZERO
-- 'readIORef' callsites in non-comment code.  The historical
-- type-checker-synthesised default-sig fallback never reached the
-- IORef because every active call site is HM-annotated through the
-- value channel (LoadedFfiTables._lft_kernelArity → CompileCtx._ctx_kernelArity).
-- The pure-channel mirror is preserved in CompileCtx for any future
-- reader that needs the arity map without re-importing the
-- Sky.Build.FfiRegistry surface; today it has zero consumers.


-- v0.17 close iter 6 (Phase 7 IORef defusing) — the legacy
-- 'ffiImplementsRef' + 'ffiPkgAliasRef' IORefs (PR-21b FFI
-- interface-satisfaction registry + Go import-path alias registry)
-- have been deleted. The values flow purely via
-- 'Sky.Build.Compile.LoadedFfiTables._lft_{implements,pkgAlias}' →
-- 'Sky.Build.CompileCtx._ctx_{implements,pkgAlias}' threaded into
-- 'Sky.Type.Solve.SolverState._ffiImplements' (via
-- 'Solve.withImplementsMap') and 'Sky.Type.Unify.UnifyState._us_ffiImplements'
-- (via 'Unify.mkUnifyState'). Both IORefs were write-only (3
-- 'writeIORef' calls in 'loadAndSeedFfiRegistry'; ZERO 'readIORef'
-- callsites in non-comment code) before deletion — paired mirror
-- IORefs that became dead when the threaded value channel landed in
-- v0.17 P1 step 3.


-- v0.17 close iter 5 (Phase 7 IORef defusing) — the legacy
-- 'ffiTypedWrapperNamesRef' + 'ffiTypedWrapperParamsRef' IORefs
-- (P7 typed-FFI wrapper registry; populated by 'seedTypedFfiNames'
-- from .skycache/go/*.go) have been deleted. The value flows purely
-- via 'Sky.Build.Compile.LoadedFfiTables._lft_typedWrapper{Names,Params}'
-- → 'Sky.Build.CompileCtx._ctx_typedWrapper{Names,Params}' →
-- 'Sky.Build.LowerCtx._lc_ffiTypedWrapper{Names,Params}', threaded
-- through every codegen entry point. All 4 reader sites in
-- 'Sky.Build.Compile' consult the threaded LowerCtx.


-- v0.17 close iter 7 — the legacy 'kernelModules ()' wrapper has been
-- deleted alongside 'ffiKernelModulesRef'. The merged static + FFI map
-- now lives on the value channel as 'Env._kernelMods', populated by
-- 'Sky.Canonicalise.Module.canonicaliseWithDeps' from the threaded
-- 'CompileCtx._ctx_kernelModules'. The bare 'staticKernelModules' export
-- remains for the LSP single-module path (no FFI loader; static-only).
--
-- Historical precedence note: 'Map.union' was left-biased so static Sky
-- kernels won on key collision. The disambiguation strategy for the
-- sky-log shape (user wants Go FFI `os` package, not Sky kernel `Os`)
-- is to rename the Sky kernel into a non-colliding namespace — `Os` was
-- renamed to `System` in 2026-04-24. The same merge semantics are
-- preserved at the call site that populates 'Env._kernelMods'.


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
    -- `Args` kernel deprecated 2026-04-24 — collapse onto `System.args ()`.
    -- Aliases removed so `import Sky.Core.Args` / `import Args` are
    -- unbound. Migration: rewrite as `System.args ()` (returns
    -- `Task Error (List String)`).
    , ("Sky.Core.File",    "File")
    , ("Sky.Core.Process", "Process")
    , ("Sky.Core.Time",    "Time")
    , ("Std.Time",         "Time")
    , ("Sky.Core.Random",  "Random")
    , ("Sky.Core.Http",    "Http")
    , ("Sky.Http.Server",  "Server")
    -- v0.13 Layer 3: Std.Html / Std.Html.Attributes / Std.Html.Events
    -- are Sky-source stdlib modules now — NOT kernel pseudo-modules.
    -- They must NOT appear here or the kernel registry shadows the
    -- parsed Sky module on import resolution.  Std.Live.Events was
    -- the old name for Std.Html.Events; fully migrated, no alias.
    -- Std.Css is a Sky-source stdlib module (v0.13 Layer 3) — NOT a
    -- kernel pseudo-module.
    , ("Std.Live",             "Live")
    -- Phase 1.3 — Std.Jobs (background-task module). Sky source:
    -- `Jobs.enqueue myJob payload`. Wired to runtime in
    -- runtime-go/rt/jobs_kernel.go.
    , ("Std.Jobs",             "Jobs")
    -- Sky.Cli — line-oriented TEA backend. Same Cmd/Sub/program shape
    -- as Sky.Live, view returns String (the prompt), onLine maps each
    -- stdin line to a Msg. See runtime-go/rt/cli.go.
    , ("Sky.Cli",              "Cli")
    , ("Std.Cli",              "Cli")
    -- Sky.Tui — full-screen terminal UI backend. Raw mode + alt-screen
    -- + ANSI redraw. view : Model -> String renders the whole frame;
    -- onKey : KeyEvent -> Msg dispatches each keypress as a Msg.
    -- See runtime-go/rt/tui.go.
    , ("Sky.Tui",              "Tui")
    , ("Std.Tui",              "Tui")
    -- Sky.Webview — desktop UI backend. Same TEA shape as
    -- Sky.Live + Sky.Tui (`view : Model -> Element msg`), but the
    -- runtime drives a native webview window via `webview_go`'s
    -- `Bind` / `Eval` bridge. See runtime-go/rt/webview.go.
    , ("Sky.Webview",          "Webview")
    , ("Std.Webview",          "Webview")
    , ("Sky.Core.Json.Encode", "JsonEnc")
    , ("Sky.Core.Json.Decode", "JsonDec")
    , ("Sky.Core.Json.Decode.Pipeline", "JsonDecP")
    -- Std.Db.Decode (v0.15.45) is NOT in staticKernelModules — it's a
    -- Layer 3 Sky-source module whose `Ffi.kernel "DbDec_*"` bodies
    -- get rewritten at Stage 4. Mirrors the Std.PubSub pattern (also
    -- not here). Adding it to staticKernelModules would shadow the
    -- parsed Sky-source module's export list, breaking imports.
    , ("Sky.Core.Uuid",        "Uuid")
    -- `Sha256` and `Hex` modules dropped in v0.10.0 — surface
    -- collapsed onto `Crypto.sha256` and `Encoding.hexEncode/Decode`.
    -- Aliases removed so `import Sky.Core.Crypto.Sha256` is unbound.
    -- Migration: replace `Sha256.sum256 (String.toBytes s) |>
    -- Result.andThen Hex.encodeToString` with `Crypto.sha256 s`.
    -- Sky kernel `Os` was renamed to `System` (2026-04-24) so the
    -- bare `Os` qualifier is free for the Go FFI `os` package
    -- (sky-log et al.). Clean break — no compat alias. Users on
    -- `import Sky.Core.Os` get an unbound-name error and must
    -- migrate to `System.exit` / `System.getenv` / `System.cwd` /
    -- `System.args`.
    , ("Sky.Core.System",        "System")
    , ("Std.System",             "System")
    , ("System",                 "System")
    -- `Slog` module dropped in v0.10.0 — was a straight alias for
    -- `Log` (runtime delegated `Slog_info` → `Log_info` etc.).
    -- Migration: replace `Slog.info "msg" […]` with `Log.info "msg" […]`.
    -- Note: the `Log.Slog` import path is now free for the Go FFI
    -- `log/slog` package — bound automatically when the user adds
    -- `log/slog = "latest"` to their sky.toml `[go.dependencies]`.
    , ("Context",                "Context")
    , ("Fmt",                    "Fmt")
    , ("Time",                   "Time")
    , ("Crypto",                 "Crypto")
    , ("Encoding",               "Encoding")
    , ("Sky.Http.RateLimit",   "RateLimit")
    -- `Env` module dropped in v0.10.0 — folded into `System.*`
    -- (getenv / getenvOr / getenvInt / getenvBool). Migration:
    -- `Env.getOrDefault key def` → `System.getenvOr def key`,
    -- `Env.getInt key` → `System.getenvInt key`, `Env.require key`
    -- → `System.getenv key` (already errors on missing).
    , ("Sky.Http.Middleware",  "Middleware")
    , ("Sky.Ffi",              "Ffi")
    , ("Sky.Core.Prelude", "Basics")  -- Prelude maps to Basics

    -- Bare-name aliases (v0.10.0): every kernel module is reachable
    -- via its short name without an explicit import. The
    -- canonicaliser fallback in `resolveQualVar` checks this map for
    -- unresolved qualifiers — the bare entries make `Log.error`,
    -- `File.readFile`, `System.exit`, etc. resolve to VarKernel
    -- without writing `import Std.Log` / `import Sky.Core.File`.
    -- Bare aliases that would COLLIDE with a Go FFI package alias
    -- (e.g. `Os`, `Log.Slog`) are intentionally OMITTED so the FFI
    -- binding has uncontested ownership when the user opts in via
    -- sky.toml `[go.dependencies]`.
    , ("Log",        "Log")
    , ("Cmd",        "Cmd")
    , ("Sub",        "Sub")
    , ("Db",         "Db")
    , ("Auth",       "Auth")
    , ("File",       "File")
    , ("Process",    "Process")
    , ("Random",     "Random")
    , ("Http",       "Http")
    , ("Server",     "Server")
    -- Html / Attr / Event bare aliases removed (v0.13 Layer 3) —
    -- those are Sky-source modules; resolve them via a real import.
    -- Css bare alias removed (v0.13 Layer 3) — Sky-source module;
    -- resolve via a real `import Std.Css`.
    , ("Live",       "Live")
    , ("Jobs",       "Jobs")
    , ("Cli",        "Cli")
    , ("Tui",        "Tui")
    , ("Webview",    "Webview")
    , ("JsonEnc",    "JsonEnc")
    , ("JsonDec",    "JsonDec")
    , ("JsonDecP",   "JsonDecP")
    , ("Uuid",       "Uuid")
    , ("RateLimit",  "RateLimit")
    , ("Middleware", "Middleware")
    , ("Ffi",        "Ffi")
    , ("Basics",     "Basics")
    , ("String",     "String")
    , ("List",       "List")
    , ("Dict",       "Dict")
    , ("Set",        "Set")
    , ("Maybe",      "Maybe")
    , ("Result",     "Result")
    , ("Task",       "Task")
    , ("Math",       "Math")
    , ("Regex",      "Regex")
    , ("Char",       "Char")
    , ("Path",       "Path")
    ]
