//! Static kernel + builtin tables, ported verbatim from
//! `Sky.Canonicalise.Environment` (doc 05 §9, §10). These change rarely; a diff
//! against the Haskell lists is the compat check. Data only, no logic.

/// `staticKernelModules` (Environment.hs:348-503): Sky import path (and bare
/// alias) → kernel pseudo-module. Drives the `Res::Kernel` qualifier fallback
/// (doc 05 §9) so `Crypto.sha256` resolves with no `import`.
pub const KERNEL_MODULES: &[(&str, &str)] = &[
    ("Sky.Core.Basics", "Basics"),
    ("Sky.Core.String", "String"),
    ("Sky.Core.List", "List"),
    ("Sky.Core.Dict", "Dict"),
    ("Sky.Core.Set", "Set"),
    ("Sky.Core.Maybe", "Maybe"),
    ("Sky.Core.Result", "Result"),
    ("Sky.Core.Task", "Task"),
    ("Sky.Core.Math", "Math"),
    ("Sky.Core.Regex", "Regex"),
    ("Sky.Core.Crypto", "Crypto"),
    ("Sky.Core.Encoding", "Encoding"),
    ("Sky.Core.Char", "Char"),
    ("Sky.Core.Path", "Path"),
    ("Std.Log", "Log"),
    ("Std.Cmd", "Cmd"),
    ("Std.Sub", "Sub"),
    ("Std.Db", "Db"),
    ("Std.Auth", "Auth"),
    ("Sky.Core.Io", "Io"),
    ("Io", "Io"),
    ("Sky.Core.File", "File"),
    ("Sky.Core.Process", "Process"),
    ("Sky.Core.Time", "Time"),
    ("Std.Time", "Time"),
    ("Sky.Core.Random", "Random"),
    ("Sky.Core.Http", "Http"),
    ("Sky.Http.Server", "Server"),
    ("Std.Live", "Live"),
    ("Std.Jobs", "Jobs"),
    ("Sky.Cli", "Cli"),
    ("Std.Cli", "Cli"),
    ("Sky.Tui", "Tui"),
    ("Std.Tui", "Tui"),
    ("Sky.Webview", "Webview"),
    ("Std.Webview", "Webview"),
    ("Sky.Core.Json.Encode", "JsonEnc"),
    ("Sky.Core.Json.Decode", "JsonDec"),
    ("Sky.Core.Json.Decode.Pipeline", "JsonDecP"),
    ("Sky.Core.Uuid", "Uuid"),
    ("Sky.Core.System", "System"),
    ("Std.System", "System"),
    ("System", "System"),
    ("Context", "Context"),
    ("Fmt", "Fmt"),
    ("Time", "Time"),
    ("Crypto", "Crypto"),
    ("Encoding", "Encoding"),
    ("Sky.Http.RateLimit", "RateLimit"),
    ("Sky.Http.Middleware", "Middleware"),
    ("Sky.Ffi", "Ffi"),
    ("Sky.Core.Prelude", "Basics"),
    // ---- bare-name aliases ----
    ("Log", "Log"),
    ("Cmd", "Cmd"),
    ("Sub", "Sub"),
    ("Db", "Db"),
    ("Auth", "Auth"),
    ("File", "File"),
    ("Process", "Process"),
    ("Random", "Random"),
    ("Http", "Http"),
    ("Server", "Server"),
    ("Live", "Live"),
    ("Jobs", "Jobs"),
    ("Cli", "Cli"),
    ("Tui", "Tui"),
    ("Webview", "Webview"),
    ("JsonEnc", "JsonEnc"),
    ("JsonDec", "JsonDec"),
    ("JsonDecP", "JsonDecP"),
    ("Uuid", "Uuid"),
    ("RateLimit", "RateLimit"),
    ("Middleware", "Middleware"),
    ("Ffi", "Ffi"),
    ("Basics", "Basics"),
    ("String", "String"),
    ("List", "List"),
    ("Dict", "Dict"),
    ("Set", "Set"),
    ("Maybe", "Maybe"),
    ("Result", "Result"),
    ("Task", "Task"),
    ("Math", "Math"),
    ("Regex", "Regex"),
    ("Char", "Char"),
    ("Path", "Path"),
];

/// `builtinVars` (Environment.hs:212): unconditional Prelude values, each a
/// `Res::Kernel`. `(name, kernel-module, kernel-func)`.
pub const BUILTIN_VARS: &[(&str, &str, &str)] = &[
    ("identity", "Basics", "identity"),
    ("always", "Basics", "always"),
    ("not", "Basics", "not"),
    ("toString", "Basics", "toString"),
    ("modBy", "Basics", "modBy"),
    ("clamp", "Basics", "clamp"),
    ("fst", "Basics", "fst"),
    ("snd", "Basics", "snd"),
    ("errorToString", "Basics", "errorToString"),
    ("println", "Log", "println"),
    ("js", "Basics", "js"),
];

/// `builtinTypes` (Environment.hs:229): `(name, arity)`. `Error` is auto-imported
/// from `Sky.Core.Error` (C19) so `Result Error a` needs no explicit import.
pub const BUILTIN_TYPES: &[(&str, u16)] = &[
    ("Int", 0),
    ("Float", 0),
    ("Bool", 0),
    ("String", 0),
    ("Char", 0),
    ("List", 1),
    ("Maybe", 1),
    ("Result", 2),
    ("Task", 2),
    ("Error", 0),
];

/// `builtinCtors` (Environment.hs:249): `(ctor, type, index, arity)`.
pub const BUILTIN_CTORS: &[(&str, &str, u16, u16)] = &[
    ("True", "Bool", 0, 0),
    ("False", "Bool", 1, 0),
    ("Just", "Maybe", 0, 1),
    ("Nothing", "Maybe", 1, 0),
    ("Ok", "Result", 0, 1),
    ("Err", "Result", 1, 1),
];

/// `preludeQualifiers` (Environment.hs:114): auto-available `Qual.fn` kernel
/// functions with no explicit `import` (doc 05 §10). `(qualifier, [funcs])`.
pub const PRELUDE_QUALIFIERS: &[(&str, &[&str])] = &[
    (
        "String",
        &[
            "length", "reverse", "append", "split", "join", "contains", "startsWith", "endsWith",
            "toInt", "fromInt", "toFloat", "fromFloat", "toUpper", "toLower", "trim", "replace",
            "slice", "isEmpty", "toBytes", "fromBytes", "fromChar", "toChar", "left", "right",
            "padLeft", "padRight", "repeat", "lines", "words", "htmlEscape", "truncate", "ellipsize",
        ],
    ),
    (
        "List",
        &[
            "map", "filter", "foldl", "foldr", "length", "head", "tail", "take", "drop", "append",
            "concat", "concatMap", "reverse", "sort", "member", "any", "all", "range", "zip",
            "filterMap", "parallelMap", "isEmpty", "cons",
        ],
    ),
    (
        "Dict",
        &[
            "empty", "insert", "get", "remove", "member", "keys", "values", "toList", "fromList",
            "map", "foldl", "union",
        ],
    ),
    (
        "Set",
        &["empty", "insert", "remove", "member", "union", "diff", "intersect", "fromList"],
    ),
    ("Maybe", &["withDefault", "map", "andThen"]),
    (
        "Result",
        &[
            "withDefault", "map", "andThen", "mapError", "map2", "map3", "map4", "map5", "andMap",
            "combine", "traverse", "andThenTask",
        ],
    ),
    (
        "Basics",
        &[
            "identity", "always", "not", "toString", "modBy", "clamp", "fst", "snd", "compare",
            "negate", "abs", "sqrt", "min", "max",
        ],
    ),
    ("Cmd", &["none", "batch", "perform"]),
    ("Sub", &["none", "every", "batch"]),
    (
        "Task",
        &[
            "succeed", "fail", "map", "andThen", "perform", "sequence", "parallel", "lazy", "run",
            "map2", "map3", "map4", "map5", "andMap", "fromResult", "andThenResult", "mapError",
            "onError",
        ],
    ),
];

/// Kernel-implicit Prelude types (#576, Module.hs:520): globally-available
/// runtime types with no `type alias` in any `.sky` source. Accepted as a no-op
/// in `exposing (…)` and resolvable unqualified (C12).
pub const KERNEL_IMPLICIT_TYPES: &[&str] = &[
    "Decoder",
    "Value",
    "Attribute",
    "Handler",
    "Middleware",
    "Session",
    "Store",
    "Route",
    "VNode",
    "Request",
    "Response",
    "Cmd",
    "Sub",
    "Db",
    "Error",
];

/// Prelude-protected names (Module.hs:872): a user union whose type or ctor name
/// collides with one of these is a hard error (C14), except at the name's own
/// canonical home.
pub const PRELUDE_PROTECTED: &[&str] = &[
    "Int", "Float", "Bool", "String", "Char", "List", "Maybe", "Result", "Task", "Error", "True",
    "False", "Just", "Nothing", "Ok", "Err",
];
