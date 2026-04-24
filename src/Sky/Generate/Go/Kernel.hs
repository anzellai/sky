-- | Kernel function registry for Sky's standard library.
-- Maps (Module, Function) to Go runtime calls with full type information.
-- These are direct calls — no sky_call runtime dispatch.
module Sky.Generate.Go.Kernel where

import qualified Data.Map.Strict as Map


-- | Information about a kernel function
data KernelInfo = KernelInfo
    { _ki_goName :: !String     -- Go function name in runtime: "rt.List_map"
    , _ki_arity  :: !Int        -- argument count
    , _ki_typed  :: !Bool       -- whether it uses typed generics
    }
    deriving (Show)


-- | Look up a kernel function
lookup :: String -> String -> Maybe KernelInfo
lookup modName funcName =
    Map.lookup (modName, funcName) registry


-- | The complete kernel registry
-- Over 100 functions mapped to typed Go runtime calls
registry :: Map.Map (String, String) KernelInfo
registry = Map.fromList
    -- ═══════════════════════════════════════════════════════
    -- Basics
    -- ═══════════════════════════════════════════════════════
    [ (("Basics", "add"),         KernelInfo "rt.Basics_add" 2 True)
    , (("Basics", "sub"),         KernelInfo "rt.Basics_sub" 2 True)
    , (("Basics", "mul"),         KernelInfo "rt.Basics_mul" 2 True)
    , (("Basics", "fdiv"),        KernelInfo "rt.Basics_fdiv" 2 True)
    , (("Basics", "idiv"),        KernelInfo "rt.Basics_idiv" 2 False)
    , (("Basics", "modBy"),       KernelInfo "rt.Basics_modBy" 2 False)
    , (("Basics", "negate"),      KernelInfo "rt.Basics_negate" 1 True)
    , (("Basics", "abs"),         KernelInfo "rt.Basics_abs" 1 True)
    , (("Basics", "sqrt"),        KernelInfo "rt.Basics_sqrt" 1 False)
    , (("Basics", "not"),         KernelInfo "rt.Basics_not" 1 False)
    , (("Basics", "identity"),    KernelInfo "rt.Basics_identity" 1 True)
    , (("Basics", "always"),      KernelInfo "rt.Basics_always" 2 True)
    , (("Basics", "compare"),     KernelInfo "rt.Basics_compare" 2 True)
    , (("Basics", "append"),      KernelInfo "rt.Basics_append" 2 True)
    , (("Basics", "toString"),    KernelInfo "rt.Debug_toString" 1 True)

    -- ═══════════════════════════════════════════════════════
    -- String
    -- ═══════════════════════════════════════════════════════
    , (("String", "length"),      KernelInfo "rt.String_length" 1 False)
    , (("String", "reverse"),     KernelInfo "rt.String_reverse" 1 False)
    , (("String", "append"),      KernelInfo "rt.String_append" 2 False)
    , (("String", "split"),       KernelInfo "rt.String_split" 2 False)
    , (("String", "join"),        KernelInfo "rt.String_join" 2 False)
    , (("String", "contains"),    KernelInfo "rt.String_contains" 2 False)
    , (("String", "startsWith"),  KernelInfo "rt.String_startsWith" 2 False)
    , (("String", "endsWith"),    KernelInfo "rt.String_endsWith" 2 False)
    , (("String", "toInt"),       KernelInfo "rt.String_toInt" 1 False)
    , (("String", "fromInt"),     KernelInfo "rt.String_fromInt" 1 False)
    , (("String", "toFloat"),     KernelInfo "rt.String_toFloat" 1 False)
    , (("String", "fromFloat"),   KernelInfo "rt.String_fromFloat" 1 False)
    , (("String", "toUpper"),     KernelInfo "rt.String_toUpper" 1 False)
    , (("String", "toLower"),     KernelInfo "rt.String_toLower" 1 False)
    , (("String", "trim"),        KernelInfo "rt.String_trim" 1 False)
    , (("String", "isEmpty"),     KernelInfo "rt.String_isEmpty" 1 False)
    , (("String", "replace"),     KernelInfo "rt.String_replace" 3 False)
    , (("String", "slice"),       KernelInfo "rt.String_slice" 3 False)
    , (("String", "left"),        KernelInfo "rt.String_left" 2 False)
    , (("String", "right"),       KernelInfo "rt.String_right" 2 False)
    , (("String", "padLeft"),     KernelInfo "rt.String_padLeft" 3 False)
    , (("String", "padRight"),    KernelInfo "rt.String_padRight" 3 False)
    , (("String", "repeat"),      KernelInfo "rt.String_repeat" 2 False)
    , (("String", "lines"),       KernelInfo "rt.String_lines" 1 False)
    , (("String", "words"),       KernelInfo "rt.String_words" 1 False)
    , (("String", "isValid"),     KernelInfo "rt.String_isValid" 1 False)
    , (("String", "normalize"),   KernelInfo "rt.String_normalize" 1 False)
    , (("String", "normalizeNFD"), KernelInfo "rt.String_normalizeNFD" 1 False)
    , (("String", "casefold"),    KernelInfo "rt.String_casefold" 1 False)
    , (("String", "equalFold"),   KernelInfo "rt.String_equalFold" 2 False)
    , (("String", "graphemes"),   KernelInfo "rt.String_graphemes" 1 False)
    , (("String", "trimStart"),   KernelInfo "rt.String_trimStart" 1 False)
    , (("String", "trimEnd"),     KernelInfo "rt.String_trimEnd" 1 False)
    , (("String", "isEmail"),     KernelInfo "rt.String_isEmail" 1 False)
    , (("String", "isUrl"),       KernelInfo "rt.String_isUrl" 1 False)
    , (("String", "slugify"),     KernelInfo "rt.String_slugify" 1 False)
    , (("String", "htmlEscape"),  KernelInfo "rt.String_htmlEscape" 1 False)
    , (("String", "truncate"),    KernelInfo "rt.String_truncate" 2 False)
    , (("String", "ellipsize"),   KernelInfo "rt.String_ellipsize" 2 False)

    -- Sky.Core.Uuid
    , (("Uuid", "v4"),            KernelInfo "rt.Uuid_v4" 0 False)
    , (("Uuid", "v7"),            KernelInfo "rt.Uuid_v7" 0 False)
    , (("Uuid", "parse"),         KernelInfo "rt.Uuid_parse" 1 False)

    -- Sky.Http.RateLimit
    , (("RateLimit", "allow"),    KernelInfo "rt.RateLimit_allow" 4 False)

    -- Std.Env
    , (("Env", "get"),            KernelInfo "rt.Env_get" 1 False)
    , (("Env", "getOrDefault"),   KernelInfo "rt.Env_getOrDefault" 2 False)
    , (("Env", "require"),        KernelInfo "rt.Env_require" 1 False)
    , (("Env", "getInt"),         KernelInfo "rt.Env_getInt" 2 False)
    , (("Env", "getBool"),        KernelInfo "rt.Env_getBool" 2 False)

    -- Sky.Http.Middleware
    , (("Middleware", "withCors"),        KernelInfo "rt.Middleware_withCors" 2 False)
    , (("Middleware", "withLogging"),     KernelInfo "rt.Middleware_withLogging" 1 False)
    , (("Middleware", "withBasicAuth"),   KernelInfo "rt.Middleware_withBasicAuth" 3 False)
    , (("Middleware", "withRateLimit"),   KernelInfo "rt.Middleware_withRateLimit" 4 False)
    -- audit P1-2: simple per-IP fixed-window rate limit
    , (("Middleware", "rateLimit"),        KernelInfo "rt.Middleware_rateLimit" 2 False)

    -- Sky.Ffi — name-based dispatch to user-supplied Go bindings
    , (("Ffi", "call"),           KernelInfo "rt.Ffi_call" 2 False)
    , (("Ffi", "callPure"),       KernelInfo "rt.Ffi_callPure" 2 False)
    , (("Ffi", "callTask"),       KernelInfo "rt.Ffi_callTask" 2 False)
    , (("Ffi", "has"),            KernelInfo "rt.Ffi_has" 1 False)
    , (("Ffi", "isPure"),         KernelInfo "rt.Ffi_isPure" 1 False)

    -- ═══════════════════════════════════════════════════════
    -- List
    -- ═══════════════════════════════════════════════════════
    -- List: use any-typed runtime functions until type checker provides types
    , (("List", "map"),           KernelInfo "rt.List_mapAny" 2 False)
    , (("List", "filter"),        KernelInfo "rt.List_filterAny" 2 False)
    , (("List", "foldl"),         KernelInfo "rt.List_foldl" 3 False)
    , (("List", "foldr"),         KernelInfo "rt.List_foldr" 3 False)
    , (("List", "length"),        KernelInfo "rt.List_length" 1 False)
    , (("List", "head"),          KernelInfo "rt.List_headAny" 1 False)
    , (("List", "tail"),          KernelInfo "rt.List_tail" 1 False)
    , (("List", "indexedMap"),   KernelInfo "rt.List_indexedMap" 2 False)
    , (("List", "find"),         KernelInfo "rt.List_find" 2 False)
    , (("List", "take"),          KernelInfo "rt.List_take" 2 False)
    , (("List", "drop"),          KernelInfo "rt.List_drop" 2 False)
    , (("List", "append"),        KernelInfo "rt.List_append" 2 False)
    , (("List", "concat"),        KernelInfo "rt.List_concat" 1 False)
    , (("List", "concatMap"),     KernelInfo "rt.List_concatMap" 2 False)
    , (("List", "reverse"),       KernelInfo "rt.List_reverseAny" 1 False)
    , (("List", "sort"),          KernelInfo "rt.List_sort" 1 False)
    , (("List", "sortBy"),        KernelInfo "rt.List_sortBy" 2 False)
    , (("List", "member"),        KernelInfo "rt.List_member" 2 False)
    , (("List", "any"),           KernelInfo "rt.List_any" 2 False)
    , (("List", "all"),           KernelInfo "rt.List_all" 2 False)
    , (("List", "range"),         KernelInfo "rt.List_range" 2 False)
    , (("List", "zip"),           KernelInfo "rt.List_zip" 2 False)
    , (("List", "filterMap"),     KernelInfo "rt.List_filterMap" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Dict
    -- ═══════════════════════════════════════════════════════
    , (("Dict", "empty"),         KernelInfo "rt.Dict_empty" 0 False)
    , (("Dict", "insert"),        KernelInfo "rt.Dict_insert" 3 False)
    , (("Dict", "get"),           KernelInfo "rt.Dict_get" 2 False)
    , (("Dict", "remove"),        KernelInfo "rt.Dict_remove" 2 False)
    , (("Dict", "member"),        KernelInfo "rt.Dict_member" 2 False)
    , (("Dict", "keys"),          KernelInfo "rt.Dict_keys" 1 False)
    , (("Dict", "values"),        KernelInfo "rt.Dict_values" 1 False)
    , (("Dict", "toList"),        KernelInfo "rt.Dict_toList" 1 False)
    , (("Dict", "fromList"),      KernelInfo "rt.Dict_fromList" 1 False)
    , (("Dict", "map"),           KernelInfo "rt.Dict_map" 2 False)
    , (("Dict", "foldl"),         KernelInfo "rt.Dict_foldl" 3 False)
    , (("Dict", "union"),         KernelInfo "rt.Dict_union" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Maybe
    -- ═══════════════════════════════════════════════════════
    , (("Maybe", "withDefault"),  KernelInfo "rt.Maybe_withDefault" 2 False)
    , (("Maybe", "map"),          KernelInfo "rt.Maybe_map" 2 False)
    , (("Maybe", "andThen"),      KernelInfo "rt.Maybe_andThen" 2 False)
    , (("Maybe", "map2"),         KernelInfo "rt.Maybe_map2" 3 False)
    , (("Maybe", "map3"),         KernelInfo "rt.Maybe_map3" 4 False)
    , (("Maybe", "map4"),         KernelInfo "rt.Maybe_map4" 5 False)
    , (("Maybe", "map5"),         KernelInfo "rt.Maybe_map5" 6 False)
    , (("Maybe", "andMap"),       KernelInfo "rt.Maybe_andMap" 2 False)
    , (("Maybe", "combine"),      KernelInfo "rt.Maybe_combine" 1 False)
    , (("Maybe", "traverse"),     KernelInfo "rt.Maybe_traverse" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Result
    -- ═══════════════════════════════════════════════════════
    , (("Result", "withDefault"), KernelInfo "rt.Result_withDefault" 2 False)
    , (("Result", "map"),         KernelInfo "rt.Result_map" 2 False)
    , (("Result", "andThen"),     KernelInfo "rt.Result_andThen" 2 False)
    , (("Result", "mapError"),    KernelInfo "rt.Result_mapError" 2 False)
    , (("Result", "map2"),        KernelInfo "rt.Result_map2" 3 False)
    , (("Result", "map3"),        KernelInfo "rt.Result_map3" 4 False)
    , (("Result", "map4"),        KernelInfo "rt.Result_map4" 5 False)
    , (("Result", "map5"),        KernelInfo "rt.Result_map5" 6 False)
    , (("Result", "andMap"),      KernelInfo "rt.Result_andMap" 2 False)
    , (("Result", "combine"),     KernelInfo "rt.Result_combine" 1 False)
    , (("Result", "traverse"),    KernelInfo "rt.Result_traverse" 2 False)
    , (("Result", "andThenTask"), KernelInfo "rt.Result_andThenTask" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Task
    -- ═══════════════════════════════════════════════════════
    -- Task: use any-typed wrappers until type checker provides real types
    , (("Task", "succeed"),       KernelInfo "rt.AnyTaskSucceed" 1 False)
    , (("Task", "fail"),          KernelInfo "rt.AnyTaskFail" 1 False)
    , (("Task", "map"),           KernelInfo "rt.Task_map" 2 True)
    , (("Task", "andThen"),       KernelInfo "rt.AnyTaskAndThen" 2 False)
    , (("Task", "perform"),       KernelInfo "rt.AnyTaskRun" 1 False)
    , (("Task", "sequence"),      KernelInfo "rt.Task_sequence" 1 True)
    , (("Task", "parallel"),      KernelInfo "rt.Task_parallel" 1 True)
    , (("Task", "lazy"),          KernelInfo "rt.Task_lazy" 1 True)
    , (("Task", "run"),           KernelInfo "rt.AnyTaskRun" 1 False)
    , (("Task", "fromResult"),    KernelInfo "rt.Task_fromResult" 1 False)
    , (("Task", "andThenResult"), KernelInfo "rt.Task_andThenResult" 2 False)
    , (("Task", "mapError"),      KernelInfo "rt.Task_mapError" 2 False)
    , (("Task", "onError"),       KernelInfo "rt.Task_onError" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Cmd
    -- ═══════════════════════════════════════════════════════
    , (("Cmd", "none"),           KernelInfo "rt.Cmd_none" 0 True)
    , (("Cmd", "batch"),          KernelInfo "rt.Cmd_batch" 1 True)
    , (("Cmd", "perform"),        KernelInfo "rt.Cmd_perform" 2 True)

    -- ═══════════════════════════════════════════════════════
    -- Time
    -- ═══════════════════════════════════════════════════════
    -- Time.now / Time.unixMillis arity 1: kernel sig is
    -- `() -> Result Error Int`. Pre-2026-04-24 these were arity 0 +
    -- the runtime was a `func() any` — the codegen happily emitted
    -- `rt.Time_now()` for `Time.now` (no args). Adding the kernel sig
    -- + bumping arity here means `Time.now ()` lowers to
    -- `rt.Time_now(struct{}{})` (the right shape) and bare `Time.now`
    -- as a value reference becomes a type error (was previously a
    -- silent eager call). Two-tier doctrine: clock reads are sync
    -- convenience effects, Result-flavoured for panic-recover only.
    , (("Time", "now"),           KernelInfo "rt.Time_now" 1 False)
    , (("Time", "sleep"),         KernelInfo "rt.Time_sleep" 1 False)
    , (("Time", "every"),         KernelInfo "rt.Time_every" 2 True)
    , (("Time", "unixMillis"),    KernelInfo "rt.Time_unixMillis" 1 False)
    , (("Time", "formatISO8601"), KernelInfo "rt.Time_formatISO8601" 1 False)
    , (("Time", "formatRFC3339"), KernelInfo "rt.Time_formatRFC3339" 1 False)
    , (("Time", "formatHTTP"),    KernelInfo "rt.Time_formatHTTP" 1 False)
    , (("Time", "format"),        KernelInfo "rt.Time_format" 2 False)
    , (("Time", "parseISO8601"),  KernelInfo "rt.Time_parseISO8601" 1 False)
    , (("Time", "parse"),         KernelInfo "rt.Time_parse" 2 False)
    , (("Time", "addMillis"),     KernelInfo "rt.Time_addMillis" 2 False)
    , (("Time", "diffMillis"),    KernelInfo "rt.Time_diffMillis" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Random
    -- ═══════════════════════════════════════════════════════
    , (("Random", "int"),         KernelInfo "rt.Random_int" 2 False)
    , (("Random", "float"),       KernelInfo "rt.Random_float" 2 False)
    , (("Random", "choice"),      KernelInfo "rt.Random_choice" 1 False)
    , (("Random", "shuffle"),     KernelInfo "rt.Random_shuffle" 1 False)

    , (("Process", "run"),        KernelInfo "rt.Process_run" 2 False)
    -- Process.exit / getEnv / getCwd / loadEnv all moved to System
    -- in v0.10.0. Process keeps only `run` (subprocess execution).
    -- Migration: rewrite as System.exit / System.getenv / System.cwd /
    -- System.loadEnv.

    , (("File", "readFile"),      KernelInfo "rt.File_readFile" 1 False)
    , (("File", "readFileLimit"), KernelInfo "rt.File_readFileLimit" 2 False)
    , (("File", "readFileBytes"), KernelInfo "rt.File_readFileBytes" 1 False)
    , (("File", "writeFile"),     KernelInfo "rt.File_writeFile" 2 False)
    , (("File", "append"),        KernelInfo "rt.File_append" 2 False)
    , (("File", "exists"),        KernelInfo "rt.File_exists" 1 False)
    , (("File", "remove"),        KernelInfo "rt.File_remove" 1 False)
    , (("File", "mkdirAll"),      KernelInfo "rt.File_mkdirAll" 1 False)
    , (("File", "readDir"),       KernelInfo "rt.File_readDir" 1 False)
    , (("File", "isDir"),         KernelInfo "rt.File_isDir" 1 False)
    , (("File", "tempFile"),     KernelInfo "rt.File_tempFile" 1 False)
    , (("File", "copy"),         KernelInfo "rt.File_copy" 2 False)
    , (("File", "rename"),       KernelInfo "rt.File_rename" 2 False)

    -- `Args.*` deprecated 2026-04-24: `Args.getArgs ()` and
    -- `System.args ()` did the same job. Use `System.args ()` instead.
    -- For Args.getArg n, use `List.head (List.drop n (System.args ()))`.

    , (("Io", "readLine"),        KernelInfo "rt.Io_readLine" 1 False)
    , (("Io", "writeStdout"),     KernelInfo "rt.Io_writeStdout" 1 False)
    , (("Io", "writeStderr"),     KernelInfo "rt.Io_writeStderr" 1 False)

    , (("Crypto", "sha256"),      KernelInfo "rt.Crypto_sha256" 1 False)
    , (("Crypto", "sha512"),      KernelInfo "rt.Crypto_sha512" 1 False)
    , (("Crypto", "md5"),         KernelInfo "rt.Crypto_md5" 1 False)
    , (("Crypto", "hmacSha256"),  KernelInfo "rt.Crypto_hmacSha256" 2 False)
    , (("Crypto", "constantTimeEqual"), KernelInfo "rt.Crypto_constantTimeEqual" 2 False)
    , (("Crypto", "randomBytes"), KernelInfo "rt.Crypto_randomBytes" 1 False)
    , (("Crypto", "randomToken"), KernelInfo "rt.Crypto_randomToken" 1 False)

    , (("Encoding", "base64Encode"), KernelInfo "rt.Encoding_base64Encode" 1 False)
    , (("Encoding", "base64Decode"), KernelInfo "rt.Encoding_base64Decode" 1 False)
    , (("Encoding", "urlEncode"),    KernelInfo "rt.Encoding_urlEncode" 1 False)
    , (("Encoding", "urlDecode"),    KernelInfo "rt.Encoding_urlDecode" 1 False)
    , (("Encoding", "hexEncode"),    KernelInfo "rt.Encoding_hexEncode" 1 False)
    , (("Encoding", "hexDecode"),    KernelInfo "rt.Encoding_hexDecode" 1 False)

    , (("Regex", "match"),        KernelInfo "rt.Regex_match" 2 False)
    , (("Regex", "find"),         KernelInfo "rt.Regex_find" 2 False)
    , (("Regex", "findAll"),      KernelInfo "rt.Regex_findAll" 2 False)
    , (("Regex", "replace"),      KernelInfo "rt.Regex_replace" 3 False)
    , (("Regex", "split"),        KernelInfo "rt.Regex_split" 2 False)

    , (("Char", "isUpper"),       KernelInfo "rt.Char_isUpper" 1 False)
    , (("Char", "isLower"),       KernelInfo "rt.Char_isLower" 1 False)
    , (("Char", "isDigit"),       KernelInfo "rt.Char_isDigit" 1 False)
    , (("Char", "isAlpha"),       KernelInfo "rt.Char_isAlpha" 1 False)
    , (("Char", "toUpper"),       KernelInfo "rt.Char_toUpper" 1 False)
    , (("Char", "toLower"),       KernelInfo "rt.Char_toLower" 1 False)

    , (("Math", "sqrt"),          KernelInfo "rt.Math_sqrt" 1 False)
    , (("Math", "pow"),           KernelInfo "rt.Math_pow" 2 False)
    , (("Math", "floor"),         KernelInfo "rt.Math_floor" 1 False)
    , (("Math", "ceil"),          KernelInfo "rt.Math_ceil" 1 False)
    , (("Math", "round"),         KernelInfo "rt.Math_round" 1 False)
    , (("Math", "sin"),           KernelInfo "rt.Math_sin" 1 False)
    , (("Math", "cos"),           KernelInfo "rt.Math_cos" 1 False)
    , (("Math", "pi"),            KernelInfo "rt.Math_pi" 0 False)
    , (("Math", "log"),           KernelInfo "rt.Math_log" 1 False)

    , (("Server", "listen"),      KernelInfo "rt.Server_listen" 2 False)
    , (("Server", "get"),         KernelInfo "rt.Server_get" 2 False)
    , (("Server", "post"),        KernelInfo "rt.Server_post" 2 False)
    , (("Server", "put"),         KernelInfo "rt.Server_put" 2 False)
    , (("Server", "delete"),      KernelInfo "rt.Server_delete" 2 False)
    , (("Server", "text"),        KernelInfo "rt.Server_text" 1 False)
    , (("Server", "json"),        KernelInfo "rt.Server_json" 1 False)
    , (("Server", "html"),        KernelInfo "rt.Server_html" 1 False)
    , (("Server", "withStatus"),  KernelInfo "rt.Server_withStatus" 2 False)
    , (("Server", "redirect"),    KernelInfo "rt.Server_redirect" 1 False)
    , (("Server", "param"),       KernelInfo "rt.Server_param" 2 False)
    , (("Server", "queryParam"),  KernelInfo "rt.Server_queryParam" 2 False)
    , (("Server", "header"),      KernelInfo "rt.Server_header" 2 False)
    , (("Server", "static"),      KernelInfo "rt.Server_static" 2 False)
    , (("Server", "getCookie"),   KernelInfo "rt.Server_getCookie" 2 False)
    , (("Server", "cookie"),      KernelInfo "rt.Server_cookie" 2 False)
    , (("Server", "withCookie"),  KernelInfo "rt.Server_withCookie" 2 False)
    , (("Server", "withHeader"),  KernelInfo "rt.Server_withHeader" 3 False)
    , (("Server", "any"),         KernelInfo "rt.Server_any" 2 False)
    -- audit P1-1: CSRF support (double-submit cookie pattern)
    , (("Server", "csrfIssue"),   KernelInfo "rt.Server_csrfIssue" 1 False)
    , (("Server", "csrfVerify"),  KernelInfo "rt.Server_csrfVerify" 1 False)
    , (("List", "isEmpty"),       KernelInfo "rt.List_isEmpty" 1 False)
    , (("Io", "writeString"),     KernelInfo "rt.Io_writeString" 1 False)

    , (("Http", "get"),           KernelInfo "rt.Http_get" 1 False)
    , (("Http", "post"),          KernelInfo "rt.Http_post" 2 False)
    -- Http.request takes a single record argument
    -- `{ method, url, headers, body }` — the Elm-style API documented
    -- in templates/CLAUDE.md. The Go runtime helper is variadic so it
    -- still accepts the legacy 4-positional call shape, but kernel
    -- arity 1 keeps call-site codegen emitting the record unchanged.
    , (("Http", "request"),       KernelInfo "rt.Http_request" 1 False)

    , (("Path", "join"),          KernelInfo "rt.Path_join" 1 False)
    , (("Path", "dir"),           KernelInfo "rt.Path_dir" 1 False)
    , (("Path", "base"),          KernelInfo "rt.Path_base" 1 False)
    , (("Path", "ext"),           KernelInfo "rt.Path_ext" 1 False)
    , (("Path", "isAbsolute"),    KernelInfo "rt.Path_isAbsolute" 1 False)
    , (("Path", "safeJoin"),      KernelInfo "rt.Path_safeJoin" 2 False)

    , (("Debug", "log"),          KernelInfo "rt.Debug_log" 2 True)
    , (("Debug", "toString"),     KernelInfo "rt.Debug_toString" 1 True)
    , (("Log", "println"),        KernelInfo "rt.Log_println" 1 False)
    -- v0.10.0: Log.{debug,info,warn,error} stay single-arg
    -- (msg only) so existing call sites keep compiling. The
    -- (msg, attrs) shape from the dropped Slog kernel landed on
    -- the new Log.{debugWith,infoWith,warnWith,errorWith} variants
    -- — same convention as the previously-existing Log.with /
    -- Log.errorWith helpers, generalised to all four levels.
    , (("Log", "debug"),          KernelInfo "rt.Log_debug" 1 False)
    , (("Log", "info"),           KernelInfo "rt.Log_info" 1 False)
    , (("Log", "warn"),           KernelInfo "rt.Log_warn" 1 False)
    , (("Log", "error"),          KernelInfo "rt.Log_error" 1 False)
    , (("Log", "debugWith"),      KernelInfo "rt.Log_debugWith" 2 False)
    , (("Log", "infoWith"),       KernelInfo "rt.Log_infoWith" 2 False)
    , (("Log", "warnWith"),       KernelInfo "rt.Log_warnWith" 2 False)
    , (("Log", "errorWith"),      KernelInfo "rt.Log_errorWith" 2 False)
    , (("Log", "with"),           KernelInfo "rt.Log_with" 2 False)
    -- Slog.{info,warn,error,debug} dropped in v0.10.0 — use Log.*
    -- equivalents directly. Slog was just a name-alias for Log
    -- with the same arity/shape; runtime delegated through.
    , (("Context", "background"), KernelInfo "rt.Context_background" 1 False)
    , (("Context", "todo"),       KernelInfo "rt.Context_todo" 1 False)
    , (("Context", "withValue"),  KernelInfo "rt.Context_withValue" 3 False)
    , (("Context", "withCancel"), KernelInfo "rt.Context_withCancel" 1 False)
    , (("Fmt", "sprint"),         KernelInfo "rt.Fmt_sprint" 1 False)
    , (("Fmt", "sprintf"),        KernelInfo "rt.Fmt_sprintf" 2 False)
    , (("Fmt", "sprintln"),       KernelInfo "rt.Fmt_sprintln" 1 False)
    , (("Fmt", "errorf"),         KernelInfo "rt.Fmt_errorf" 2 False)
    , (("Basics", "errorToString"), KernelInfo "rt.Basics_errorToString" 1 False)
    , (("Basics", "js"),          KernelInfo "rt.Basics_js" 1 False)
    -- Sha256.* / Hex.* dropped in v0.10.0 — Crypto.sha256 and
    -- Encoding.hexEncode/Decode are the consolidated surface.
    -- Migration: `Sha256.sum256 (String.toBytes s)
    --              |> Result.andThen Hex.encodeToString`
    -- collapses to `Crypto.sha256 s`.
    -- Sky kernel `Os` was renamed to `System` in 2026-04-24 to free
    -- the `Os` qualifier for the Go FFI `os` package (sky-log et al.
    -- need stdin / stderr / fileWriteString from Go's std library).
    -- Use `System.exit`, `System.getenv`, `System.cwd`, `System.args`.
    , (("System", "args"),        KernelInfo "rt.System_args" 1 False)
    , (("System", "getArg"),      KernelInfo "rt.System_getArg" 1 False)
    , (("System", "getenv"),      KernelInfo "rt.System_getenv" 1 False)
    , (("System", "getenvOr"),    KernelInfo "rt.System_getenvOr" 2 False)
    , (("System", "getenvInt"),   KernelInfo "rt.System_getenvInt" 1 False)
    , (("System", "getenvBool"),  KernelInfo "rt.System_getenvBool" 1 False)
    , (("System", "cwd"),         KernelInfo "rt.System_cwd" 1 False)
    , (("System", "exit"),        KernelInfo "rt.System_exit" 1 False)
    , (("System", "loadEnv"),     KernelInfo "rt.System_loadEnv" 1 False)
    , (("Time", "timeString"),    KernelInfo "rt.Time_timeString" 1 False)
    , (("String", "toBytes"),     KernelInfo "rt.String_toBytes" 1 False)
    , (("String", "fromBytes"),   KernelInfo "rt.String_fromBytes" 1 False)
    , (("String", "fromChar"),    KernelInfo "rt.String_fromChar" 1 False)
    , (("String", "toChar"),      KernelInfo "rt.String_toChar" 1 False)
    , (("Basics", "modBy"),       KernelInfo "rt.Basics_modBy" 2 False)
    , (("Basics", "fst"),         KernelInfo "rt.Basics_fst" 1 False)
    , (("Basics", "snd"),         KernelInfo "rt.Basics_snd" 1 False)
    , (("List", "cons"),          KernelInfo "rt.List_cons" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Std.Html — VNode element builders
    -- ═══════════════════════════════════════════════════════
    , (("Html", "text"),          KernelInfo "rt.Html_text" 1 False)
    , (("Html", "div"),           KernelInfo "rt.Html_div" 2 False)
    , (("Html", "span"),          KernelInfo "rt.Html_span" 2 False)
    , (("Html", "p"),             KernelInfo "rt.Html_p" 2 False)
    , (("Html", "h1"),            KernelInfo "rt.Html_h1" 2 False)
    , (("Html", "h2"),            KernelInfo "rt.Html_h2" 2 False)
    , (("Html", "h3"),            KernelInfo "rt.Html_h3" 2 False)
    , (("Html", "h4"),            KernelInfo "rt.Html_h4" 2 False)
    , (("Html", "h5"),            KernelInfo "rt.Html_h5" 2 False)
    , (("Html", "h6"),            KernelInfo "rt.Html_h6" 2 False)
    , (("Html", "a"),             KernelInfo "rt.Html_a" 2 False)
    , (("Html", "button"),        KernelInfo "rt.Html_button" 2 False)
    , (("Html", "input"),         KernelInfo "rt.Html_input" 2 False)
    , (("Html", "form"),          KernelInfo "rt.Html_form" 2 False)
    , (("Html", "label"),         KernelInfo "rt.Html_label" 2 False)
    , (("Html", "nav"),           KernelInfo "rt.Html_nav" 2 False)
    , (("Html", "section"),       KernelInfo "rt.Html_section" 2 False)
    , (("Html", "article"),       KernelInfo "rt.Html_article" 2 False)
    , (("Html", "header"),        KernelInfo "rt.Html_header" 2 False)
    , (("Html", "footer"),        KernelInfo "rt.Html_footer" 2 False)
    , (("Html", "main"),          KernelInfo "rt.Html_main" 2 False)
    , (("Html", "ul"),            KernelInfo "rt.Html_ul" 2 False)
    , (("Html", "ol"),            KernelInfo "rt.Html_ol" 2 False)
    , (("Html", "li"),            KernelInfo "rt.Html_li" 2 False)
    , (("Html", "img"),           KernelInfo "rt.Html_img" 1 False)
    , (("Html", "br"),            KernelInfo "rt.Html_br" 2 False)
    , (("Html", "hr"),            KernelInfo "rt.Html_hr" 2 False)
    , (("Html", "table"),         KernelInfo "rt.Html_table" 2 False)
    , (("Html", "thead"),         KernelInfo "rt.Html_thead" 2 False)
    , (("Html", "tbody"),         KernelInfo "rt.Html_tbody" 2 False)
    , (("Html", "tr"),            KernelInfo "rt.Html_tr" 2 False)
    , (("Html", "th"),            KernelInfo "rt.Html_th" 2 False)
    , (("Html", "td"),            KernelInfo "rt.Html_td" 2 False)
    , (("Html", "textarea"),      KernelInfo "rt.Html_textarea" 2 False)
    , (("Html", "select"),        KernelInfo "rt.Html_select" 2 False)
    , (("Html", "option"),        KernelInfo "rt.Html_option" 2 False)
    , (("Html", "pre"),           KernelInfo "rt.Html_pre" 2 False)
    , (("Html", "code"),          KernelInfo "rt.Html_code" 2 False)
    , (("Html", "strong"),        KernelInfo "rt.Html_strong" 2 False)
    , (("Html", "em"),            KernelInfo "rt.Html_em" 2 False)
    , (("Html", "small"),         KernelInfo "rt.Html_small" 2 False)
    , (("Html", "styleNode"),     KernelInfo "rt.Html_styleNode" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Std.Html.Attributes
    -- ═══════════════════════════════════════════════════════
    , (("Attr", "class"),         KernelInfo "rt.Attr_class" 1 False)
    , (("Attr", "id"),            KernelInfo "rt.Attr_id" 1 False)
    , (("Attr", "style"),         KernelInfo "rt.Attr_style" 1 False)
    , (("Attr", "type"),          KernelInfo "rt.Attr_type" 1 False)
    , (("Attr", "type_"),         KernelInfo "rt.Attr_type" 1 False)
    , (("Attr", "value"),         KernelInfo "rt.Attr_value" 1 False)
    , (("Attr", "href"),          KernelInfo "rt.Attr_href" 1 False)
    , (("Attr", "src"),           KernelInfo "rt.Attr_src" 1 False)
    , (("Attr", "alt"),           KernelInfo "rt.Attr_alt" 1 False)
    , (("Attr", "name"),          KernelInfo "rt.Attr_name" 1 False)
    , (("Attr", "placeholder"),   KernelInfo "rt.Attr_placeholder" 1 False)
    , (("Attr", "title"),         KernelInfo "rt.Attr_title" 1 False)
    , (("Attr", "for"),           KernelInfo "rt.Attr_for" 1 False)
    , (("Attr", "checked"),       KernelInfo "rt.Attr_checked" 1 False)
    , (("Attr", "disabled"),      KernelInfo "rt.Attr_disabled" 1 False)
    , (("Attr", "readonly"),      KernelInfo "rt.Attr_readonly" 1 False)
    , (("Attr", "required"),      KernelInfo "rt.Attr_required" 1 False)
    , (("Attr", "autofocus"),     KernelInfo "rt.Attr_autofocus" 1 False)
    , (("Attr", "rel"),           KernelInfo "rt.Attr_rel" 1 False)
    , (("Attr", "target"),        KernelInfo "rt.Attr_target" 1 False)
    , (("Attr", "method"),        KernelInfo "rt.Attr_method" 1 False)
    , (("Attr", "action"),        KernelInfo "rt.Attr_action" 1 False)

    -- ═══════════════════════════════════════════════════════
    -- Std.Css
    -- ═══════════════════════════════════════════════════════
    , (("Css", "stylesheet"),     KernelInfo "rt.Css_stylesheet" 1 False)
    , (("Css", "rule"),           KernelInfo "rt.Css_rule" 2 False)
    , (("Css", "property"),       KernelInfo "rt.Css_property" 2 False)
    , (("Css", "px"),             KernelInfo "rt.Css_px" 1 False)
    , (("Css", "rem"),            KernelInfo "rt.Css_rem" 1 False)
    , (("Css", "em"),             KernelInfo "rt.Css_em" 1 False)
    , (("Css", "pct"),            KernelInfo "rt.Css_pct" 1 False)
    , (("Css", "hex"),            KernelInfo "rt.Css_hex" 1 False)
    , (("Css", "color"),          KernelInfo "rt.Css_color" 1 False)
    , (("Css", "background"),     KernelInfo "rt.Css_background" 1 False)
    , (("Css", "backgroundColor"), KernelInfo "rt.Css_backgroundColor" 1 False)
    , (("Css", "padding"),        KernelInfo "rt.Css_padding" 1 False)
    , (("Css", "padding2"),       KernelInfo "rt.Css_padding2" 2 False)
    , (("Css", "margin"),         KernelInfo "rt.Css_margin" 1 False)
    , (("Css", "margin2"),        KernelInfo "rt.Css_margin2" 2 False)
    , (("Css", "fontSize"),       KernelInfo "rt.Css_fontSize" 1 False)
    , (("Css", "fontWeight"),     KernelInfo "rt.Css_fontWeight" 1 False)
    , (("Css", "fontFamily"),     KernelInfo "rt.Css_fontFamily" 1 False)
    , (("Css", "lineHeight"),     KernelInfo "rt.Css_lineHeight" 1 False)
    , (("Css", "textAlign"),      KernelInfo "rt.Css_textAlign" 1 False)
    , (("Css", "border"),         KernelInfo "rt.Css_border" 1 False)
    , (("Css", "borderRadius"),   KernelInfo "rt.Css_borderRadius" 1 False)
    , (("Css", "borderBottom"),   KernelInfo "rt.Css_borderBottom" 1 False)
    , (("Css", "display"),        KernelInfo "rt.Css_display" 1 False)
    , (("Css", "cursor"),         KernelInfo "rt.Css_cursor" 1 False)
    , (("Css", "gap"),            KernelInfo "rt.Css_gap" 1 False)
    , (("Css", "justifyContent"), KernelInfo "rt.Css_justifyContent" 1 False)
    , (("Css", "alignItems"),     KernelInfo "rt.Css_alignItems" 1 False)
    , (("Css", "width"),          KernelInfo "rt.Css_width" 1 False)
    , (("Css", "height"),         KernelInfo "rt.Css_height" 1 False)
    , (("Css", "maxWidth"),       KernelInfo "rt.Css_maxWidth" 1 False)
    , (("Css", "minWidth"),       KernelInfo "rt.Css_minWidth" 1 False)
    , (("Css", "transform"),      KernelInfo "rt.Css_transform" 1 False)

    -- ═══════════════════════════════════════════════════════
    -- Std.Live
    -- ═══════════════════════════════════════════════════════
    , (("Live", "app"),           KernelInfo "rt.Live_app" 1 False)
    , (("Live", "route"),         KernelInfo "rt.Live_route" 2 False)
    , (("Live", "api"),           KernelInfo "rt.Live_api"   2 False)

    -- ═══════════════════════════════════════════════════════
    -- Std.Live.Events
    -- ═══════════════════════════════════════════════════════
    , (("Event", "onClick"),      KernelInfo "rt.Event_onClick" 1 False)
    , (("Event", "onInput"),      KernelInfo "rt.Event_onInput" 1 False)
    , (("Event", "onChange"),     KernelInfo "rt.Event_onChange" 1 False)
    , (("Event", "onSubmit"),     KernelInfo "rt.Event_onSubmit" 1 False)
    , (("Event", "onDblClick"),   KernelInfo "rt.Event_onDblClick" 1 False)
    , (("Event", "onMouseOver"),  KernelInfo "rt.Event_onMouseOver" 1 False)
    , (("Event", "onMouseOut"),   KernelInfo "rt.Event_onMouseOut" 1 False)
    , (("Event", "onKeyDown"),    KernelInfo "rt.Event_onKeyDown" 1 False)
    , (("Event", "onKeyUp"),      KernelInfo "rt.Event_onKeyUp" 1 False)
    , (("Event", "onFocus"),      KernelInfo "rt.Event_onFocus" 1 False)
    , (("Event", "onBlur"),       KernelInfo "rt.Event_onBlur" 1 False)

    -- ═══════════════════════════════════════════════════════
    -- Std.Sub
    -- ═══════════════════════════════════════════════════════
    , (("Sub", "none"),           KernelInfo "rt.Sub_none" 0 False)
    , (("Sub", "every"),          KernelInfo "rt.Sub_every" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Set
    -- ═══════════════════════════════════════════════════════
    , (("Set", "empty"),          KernelInfo "rt.Set_empty" 0 False)
    , (("Set", "fromList"),       KernelInfo "rt.Set_fromList" 1 False)
    , (("Set", "insert"),         KernelInfo "rt.Set_insert" 2 False)
    , (("Set", "remove"),         KernelInfo "rt.Set_remove" 2 False)
    , (("Set", "member"),         KernelInfo "rt.Set_member" 2 False)
    , (("Set", "toList"),         KernelInfo "rt.Set_toList" 1 False)
    , (("Set", "size"),           KernelInfo "rt.Set_size" 1 False)
    , (("Set", "union"),          KernelInfo "rt.Set_union" 2 False)
    , (("Set", "intersect"),      KernelInfo "rt.Set_intersect" 2 False)
    , (("Set", "diff"),           KernelInfo "rt.Set_diff" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Json.Encode
    -- ═══════════════════════════════════════════════════════
    , (("JsonEnc", "string"),     KernelInfo "rt.JsonEnc_string" 1 False)
    , (("JsonEnc", "int"),        KernelInfo "rt.JsonEnc_int" 1 False)
    , (("JsonEnc", "float"),      KernelInfo "rt.JsonEnc_float" 1 False)
    , (("JsonEnc", "bool"),       KernelInfo "rt.JsonEnc_bool" 1 False)
    , (("JsonEnc", "null"),       KernelInfo "rt.JsonEnc_null" 0 False)
    , (("JsonEnc", "list"),       KernelInfo "rt.JsonEnc_list" 1 False)
    , (("JsonEnc", "object"),     KernelInfo "rt.JsonEnc_object" 1 False)
    , (("JsonEnc", "encode"),     KernelInfo "rt.JsonEnc_encode" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Json.Decode
    -- ═══════════════════════════════════════════════════════
    , (("JsonDec", "decodeString"), KernelInfo "rt.JsonDec_decodeString" 2 False)
    , (("JsonDec", "string"),     KernelInfo "rt.JsonDec_string" 0 False)
    , (("JsonDec", "int"),        KernelInfo "rt.JsonDec_int" 0 False)
    , (("JsonDec", "float"),      KernelInfo "rt.JsonDec_float" 0 False)
    , (("JsonDec", "bool"),       KernelInfo "rt.JsonDec_bool" 0 False)
    , (("JsonDec", "field"),      KernelInfo "rt.JsonDec_field" 2 False)
    , (("JsonDec", "index"),      KernelInfo "rt.JsonDec_index" 2 False)
    , (("JsonDec", "list"),       KernelInfo "rt.JsonDec_list" 1 False)
    , (("JsonDec", "map"),        KernelInfo "rt.JsonDec_map" 2 False)
    , (("JsonDec", "andThen"),    KernelInfo "rt.JsonDec_andThen" 2 False)
    , (("JsonDec", "succeed"),    KernelInfo "rt.JsonDec_succeed" 1 False)
    , (("JsonDec", "fail"),       KernelInfo "rt.JsonDec_fail" 1 False)
    , (("JsonDec", "oneOf"),      KernelInfo "rt.JsonDec_oneOf" 1 False)
    , (("JsonDec", "at"),         KernelInfo "rt.JsonDec_at" 2 False)
    , (("JsonDec", "map2"),       KernelInfo "rt.JsonDec_map2" 3 False)
    , (("JsonDec", "map3"),       KernelInfo "rt.JsonDec_map3" 4 False)
    , (("JsonDec", "map4"),       KernelInfo "rt.JsonDec_map4" 5 False)
    , (("JsonDec", "map5"),       KernelInfo "rt.JsonDec_map5" 6 False)

    -- ═══════════════════════════════════════════════════════
    -- Std.Db (SQLite via modernc.org/sqlite)
    -- ═══════════════════════════════════════════════════════
    , (("Db", "connect"),         KernelInfo "rt.Db_connect" 1 False)
    , (("Db", "open"),            KernelInfo "rt.Db_open" 1 False)
    , (("Db", "close"),           KernelInfo "rt.Db_close" 1 False)
    , (("Db", "exec"),            KernelInfo "rt.Db_exec" 3 False)
    , (("Db", "execRaw"),         KernelInfo "rt.Db_execRaw" 2 False)
    , (("Db", "getField"),        KernelInfo "rt.Db_getField" 2 False)
    , (("Db", "getFieldOr"),      KernelInfo "rt.Db_getFieldOr" 3 False)
    , (("Db", "getString"),       KernelInfo "rt.Db_getString" 2 False)
    , (("Db", "getInt"),          KernelInfo "rt.Db_getInt" 2 False)
    , (("Db", "getBool"),         KernelInfo "rt.Db_getBool" 2 False)
    , (("Db", "query"),           KernelInfo "rt.Db_query" 3 False)
    , (("Db", "queryDecode"),     KernelInfo "rt.Db_queryDecode" 4 False)
    , (("Db", "insertRow"),       KernelInfo "rt.Db_insertRow" 3 False)
    , (("Db", "getById"),         KernelInfo "rt.Db_getById" 3 False)
    , (("Db", "updateById"),      KernelInfo "rt.Db_updateById" 4 False)
    , (("Db", "deleteById"),      KernelInfo "rt.Db_deleteById" 3 False)
    , (("Db", "findWhere"),       KernelInfo "rt.Db_findWhere" 4 False)
    -- audit P1-3: parameterised safe alternatives to findWhere
    , (("Db", "findOneByField"),    KernelInfo "rt.Db_findOneByField" 4 False)
    , (("Db", "findManyByField"),   KernelInfo "rt.Db_findManyByField" 4 False)
    , (("Db", "findByConditions"),  KernelInfo "rt.Db_findByConditions" 3 False)
    , (("Db", "unsafeFindWhere"),   KernelInfo "rt.Db_unsafeFindWhere" 4 False)
    , (("Db", "withTransaction"), KernelInfo "rt.Db_withTransaction" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Std.Auth (bcrypt + JWT)
    -- ═══════════════════════════════════════════════════════
    , (("Auth", "hashPassword"),   KernelInfo "rt.Auth_hashPassword" 1 False)
    , (("Auth", "verifyPassword"), KernelInfo "rt.Auth_verifyPassword" 2 False)
    , (("Auth", "signToken"),      KernelInfo "rt.Auth_signToken" 3 False)
    , (("Auth", "verifyToken"),    KernelInfo "rt.Auth_verifyToken" 2 False)
    , (("Auth", "register"),       KernelInfo "rt.Auth_register" 3 False)
    , (("Auth", "login"),          KernelInfo "rt.Auth_login" 3 False)
    , (("Auth", "setRole"),        KernelInfo "rt.Auth_setRole" 3 False)
    , (("Auth", "hashPasswordCost"), KernelInfo "rt.Auth_hashPasswordCost" 2 False)
    , (("Auth", "passwordStrength"), KernelInfo "rt.Auth_passwordStrength" 1 False)

    -- ═══════════════════════════════════════════════════════
    -- Json.Decode.Pipeline
    -- ═══════════════════════════════════════════════════════
    , (("JsonDecP", "required"),   KernelInfo "rt.JsonDecP_required" 3 False)
    , (("JsonDecP", "optional"),   KernelInfo "rt.JsonDecP_optional" 4 False)
    , (("JsonDecP", "custom"),     KernelInfo "rt.JsonDecP_custom" 2 False)
    , (("JsonDecP", "requiredAt"), KernelInfo "rt.JsonDecP_requiredAt" 3 False)
    ]
