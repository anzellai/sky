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
    -- Cycle 4 D1 — Layer 3 stdlib entries the registry missed.
    -- `String.toList` / `String.fromList` / `String.concat` are
    -- declared via `Ffi.kernel "String_*"` in
    -- `sky-stdlib/Sky/Core/String.sky` but the kernel lookup
    -- previously fell through to `kernelToGo`'s default,
    -- emitting `rt.String_toList` against a runtime that did not
    -- export it. Closed by adding the runtime helpers + these
    -- registry entries together.
    , (("String", "toList"),      KernelInfo "rt.String_toList" 1 False)
    , (("String", "fromList"),    KernelInfo "rt.String_fromList" 1 False)
    , (("String", "concat"),      KernelInfo "rt.String_concat" 1 False)

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
    -- Ffi.kernel — runtime stub (panics if ever invoked). Codegen
    -- rewrites every Ffi.kernel-wrapped function call to a direct
    -- VarKernel of the named binding, so this entry exists only so
    -- the lowerer has something to dispatch when an unrewritten
    -- reference slips through (e.g. an indirect / partial-app).
    , (("Ffi", "kernel"),         KernelInfo "rt.Ffi_kernel" 1 False)

    -- ═══════════════════════════════════════════════════════
    -- Doc — sky-doc TUI fast catalogue loader + searcher.
    -- Lives at the kernel layer so the 80k-entry Stripe SDK
    -- catalogue parses in ~250 ms (Go) instead of timing out
    -- after minutes (pure-Sky Json.Decode).
    -- ═══════════════════════════════════════════════════════
    , (("Doc", "loadCatalog"),    KernelInfo "rt.Doc_loadCatalog" 1 False)
    , (("Doc", "searchCatalog"),  KernelInfo "rt.Doc_searchCatalog" 3 False)

    -- ═══════════════════════════════════════════════════════
    -- Trace — Std.Trace opt-in span API (observability-design.md
    -- Layer 3). span/event/attr are Task-shaped; the runtime
    -- wraps the OTEL span around the forced Task.
    -- ═══════════════════════════════════════════════════════
    , (("Trace", "span"),         KernelInfo "rt.Trace_span" 2 False)
    , (("Trace", "event"),        KernelInfo "rt.Trace_event" 1 False)
    , (("Trace", "attr"),         KernelInfo "rt.Trace_attr" 2 False)

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
    , (("Dict", "size"),          KernelInfo "rt.Dict_size" 1 False)
    , (("Dict", "isEmpty"),       KernelInfo "rt.Dict_isEmpty" 1 False)

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
    -- v0.15.44 retry combinator.
    -- v0.15.50: retryAlways is now pure Sky (`= RetryAlways` ADT ctor),
    -- no longer a kernel — `retryAlways` only registered on retryWith below.
    , (("Task", "retryWith"),     KernelInfo "rt.Task_retryWith" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Cmd
    -- ═══════════════════════════════════════════════════════
    , (("Cmd", "none"),           KernelInfo "rt.Cmd_none" 0 True)
    , (("Cmd", "batch"),          KernelInfo "rt.Cmd_batch" 1 True)
    , (("Cmd", "perform"),        KernelInfo "rt.Cmd_perform" 2 True)
    , (("Cmd", "publish"),        KernelInfo "rt.Cmd_publish" 2 True)
    , (("Cmd", "publishNoEcho"),  KernelInfo "rt.Cmd_publishNoEcho" 2 True)

    -- ═══════════════════════════════════════════════════════
    -- Webview (Sky.Webview v0.1 MVP — #356)
    -- ═══════════════════════════════════════════════════════
    -- Webview.app is the desktop-backend TEA entry. Declared via
    -- Ffi.kernel in sky-stdlib/Std/Webview.sky; the
    -- KernelStdlibCoverageSpec walks every Ffi.kernel name and
    -- asserts a registry entry exists. (Tui_app / Cli_program /
    -- Live_app don't appear here because they are kernel-only —
    -- no sky-stdlib source — so the default Mod_Func fallback in
    -- kernelToGo handles them transparently.)
    , (("Webview", "app"),        KernelInfo "rt.Webview_app" 1 True)

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
    , (("Random", "choiceMaybe"), KernelInfo "rt.Random_choiceMaybe" 1 False)
    , (("Random", "shuffle"),     KernelInfo "rt.Random_shuffle" 1 False)
    , (("Random", "weighted"),    KernelInfo "rt.Random_weighted" 1 False)
    , (("Random", "seededInt"),   KernelInfo "rt.Random_seededInt" 3 False)
    , (("Random", "seededFloat"), KernelInfo "rt.Random_seededFloat" 1 False)
    , (("Random", "seededChoice"), KernelInfo "rt.Random_seededChoice" 2 False)

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
    , (("Crypto", "hmacSha512"),  KernelInfo "rt.Crypto_hmacSha512" 2 False)
    , (("Crypto", "sha1"),        KernelInfo "rt.Crypto_sha1" 1 False)
    , (("Crypto", "rsaSha256Sign"),   KernelInfo "rt.Crypto_rsaSha256Sign" 2 False)
    , (("Crypto", "rsaSha256Verify"), KernelInfo "rt.Crypto_rsaSha256Verify" 3 False)
    , (("Crypto", "constantTimeEqual"), KernelInfo "rt.Crypto_constantTimeEqual" 2 False)
    , (("Crypto", "randomBytes"), KernelInfo "rt.Crypto_randomBytes" 1 False)
    , (("Crypto", "randomToken"), KernelInfo "rt.Crypto_randomToken" 1 False)
    -- AES-GCM + ChaCha20-Poly1305 (v0.15.44).
    , (("Crypto", "aesGcmEncrypt"),       KernelInfo "rt.Crypto_aesGcmEncrypt" 2 False)
    , (("Crypto", "aesGcmDecrypt"),       KernelInfo "rt.Crypto_aesGcmDecrypt" 2 False)
    , (("Crypto", "chacha20Encrypt"),     KernelInfo "rt.Crypto_chacha20Encrypt" 2 False)
    , (("Crypto", "chacha20Decrypt"),     KernelInfo "rt.Crypto_chacha20Decrypt" 2 False)
    , (("Crypto", "aesKeyFromPassword"),  KernelInfo "rt.Crypto_aesKeyFromPassword" 2 False)
    , (("Crypto", "chachaKeyFromPassword"), KernelInfo "rt.Crypto_chachaKeyFromPassword" 2 False)
    -- Sky.Core.Bytes (v0.15.44).
    , (("Bytes", "toString"),   KernelInfo "rt.Bytes_toString" 1 False)
    , (("Bytes", "fromHex"),    KernelInfo "rt.Bytes_fromHex" 1 False)
    , (("Bytes", "toHex"),      KernelInfo "rt.Bytes_toHex" 1 False)
    , (("Bytes", "fromBase64"), KernelInfo "rt.Bytes_fromBase64" 1 False)
    , (("Bytes", "toBase64"),   KernelInfo "rt.Bytes_toBase64" 1 False)

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
    , (("Char", "toCode"),        KernelInfo "rt.Char_toCode" 1 False)
    , (("Char", "fromCode"),      KernelInfo "rt.Char_fromCode" 1 False)

    , (("Math", "sqrt"),          KernelInfo "rt.Math_sqrt" 1 False)
    , (("Math", "pow"),           KernelInfo "rt.Math_pow" 2 False)
    , (("Math", "floor"),         KernelInfo "rt.Math_floor" 1 False)
    , (("Math", "ceil"),          KernelInfo "rt.Math_ceil" 1 False)
    , (("Math", "round"),         KernelInfo "rt.Math_round" 1 False)
    , (("Math", "sin"),           KernelInfo "rt.Math_sin" 1 False)
    , (("Math", "cos"),           KernelInfo "rt.Math_cos" 1 False)
    , (("Math", "tan"),           KernelInfo "rt.Math_tan" 1 False)
    , (("Math", "pi"),            KernelInfo "rt.Math_pi" 0 False)
    , (("Math", "e"),             KernelInfo "rt.Math_e" 0 False)
    , (("Math", "log"),           KernelInfo "rt.Math_log" 1 False)
    -- Cycle 4 D1 — `abs / min / max` declared via `Ffi.kernel` in
    -- `sky-stdlib/Sky/Core/Math.sky`; runtime helpers exist
    -- (`rt.Math_abs/min/max`) but the registry was empty.
    , (("Math", "abs"),           KernelInfo "rt.Math_abs" 1 False)
    , (("Math", "min"),           KernelInfo "rt.Math_min" 2 False)
    , (("Math", "max"),           KernelInfo "rt.Math_max" 2 False)
    -- #366 — full Go math.* parity.
    -- Inverse trig
    , (("Math", "asin"),          KernelInfo "rt.Math_asin" 1 False)
    , (("Math", "acos"),          KernelInfo "rt.Math_acos" 1 False)
    , (("Math", "atan"),          KernelInfo "rt.Math_atan" 1 False)
    , (("Math", "atan2"),         KernelInfo "rt.Math_atan2" 2 False)
    -- Hyperbolic + inverse hyperbolic
    , (("Math", "sinh"),          KernelInfo "rt.Math_sinh" 1 False)
    , (("Math", "cosh"),          KernelInfo "rt.Math_cosh" 1 False)
    , (("Math", "tanh"),          KernelInfo "rt.Math_tanh" 1 False)
    , (("Math", "asinh"),         KernelInfo "rt.Math_asinh" 1 False)
    , (("Math", "acosh"),         KernelInfo "rt.Math_acosh" 1 False)
    , (("Math", "atanh"),         KernelInfo "rt.Math_atanh" 1 False)
    -- Exp / log family
    , (("Math", "exp"),           KernelInfo "rt.Math_exp" 1 False)
    , (("Math", "exp2"),          KernelInfo "rt.Math_exp2" 1 False)
    , (("Math", "log2"),          KernelInfo "rt.Math_log2" 1 False)
    , (("Math", "log10"),         KernelInfo "rt.Math_log10" 1 False)
    -- Roots + utilities
    , (("Math", "cbrt"),          KernelInfo "rt.Math_cbrt" 1 False)
    , (("Math", "hypot"),         KernelInfo "rt.Math_hypot" 2 False)
    , (("Math", "trunc"),         KernelInfo "rt.Math_trunc" 1 False)
    , (("Math", "mod"),           KernelInfo "rt.Math_mod" 2 False)
    , (("Math", "remainder"),     KernelInfo "rt.Math_remainder" 2 False)
    -- Additional constants
    , (("Math", "phi"),           KernelInfo "rt.Math_phi" 0 False)
    , (("Math", "sqrt2"),         KernelInfo "rt.Math_sqrt2" 0 False)
    , (("Math", "inf"),           KernelInfo "rt.Math_inf" 0 False)
    , (("Math", "nan"),           KernelInfo "rt.Math_nan" 0 False)

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
    -- api routes — CSRF-exempt REST / machine-to-machine endpoints
    , (("Server", "api"),         KernelInfo "rt.Server_api" 2 False)
    , (("List", "isEmpty"),       KernelInfo "rt.List_isEmpty" 1 False)
    , (("Io", "writeString"),     KernelInfo "rt.Io_writeString" 1 False)

    , (("Http", "get"),           KernelInfo "rt.Http_get" 1 False)
    , (("Http", "post"),          KernelInfo "rt.Http_post" 2 False)
    -- Http.request takes a single record argument
    -- `{ method, url, headers, body }` — record-argument API
    -- documented in templates/CLAUDE.md (same shape as Elm's
    -- `Http.request`). The Go runtime helper is variadic so it
    -- still accepts the legacy 4-positional call shape, but kernel
    -- arity 1 keeps call-site codegen emitting the record unchanged.
    , (("Http", "request"),       KernelInfo "rt.Http_request" 1 False)
    , (("Http", "parseQuery"),    KernelInfo "rt.Http_parseQuery" 1 False)

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
    -- Cycle 4 D1 — back-compat alias of `cwd`, exposed by
    -- `sky-stdlib/Sky/Core/System.sky`. Routes to the same runtime
    -- helper via the `System_getcwd` wrapper in `rt.go`.
    , (("System", "getcwd"),      KernelInfo "rt.System_getcwd" 1 False)
    , (("System", "exit"),        KernelInfo "rt.System_exit" 1 False)
    , (("System", "loadEnv"),     KernelInfo "rt.System_loadEnv" 1 False)
    , (("System", "setenv"),      KernelInfo "rt.System_setenv" 2 False)
    , (("System", "unsetenv"),    KernelInfo "rt.System_unsetenv" 1 False)
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
    -- Std.Sub
    -- ═══════════════════════════════════════════════════════
    , (("Sub", "none"),           KernelInfo "rt.Sub_none" 0 False)
    , (("Sub", "every"),          KernelInfo "rt.Sub_every" 2 False)
    , (("Sub", "batch"),          KernelInfo "rt.Sub_batch" 1 False)
    , (("Sub", "subscribeTopic"), KernelInfo "rt.Sub_subscribeTopic" 2 False)
    , (("Sub", "subscribeStream"), KernelInfo "rt.Sub_subscribeStream" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Sky.Core.Http.Stream  (Cycle 4 HS — streaming HTTP bodies)
    -- ═══════════════════════════════════════════════════════
    , (("HttpStream", "open"),    KernelInfo "rt.HttpStream_open" 1 True)
    , (("HttpStream", "close"),   KernelInfo "rt.HttpStream_close" 1 True)
    , (("HttpStream", "forEachChunk"), KernelInfo "rt.HttpStream_forEachChunk" 2 True)

    -- ═══════════════════════════════════════════════════════
    -- Sky.Http.Server.Stream  (Cycle 4 HS-Server — server-side
    -- streaming HTTP responses; mirror of HttpStream above)
    -- ═══════════════════════════════════════════════════════
    , (("ServerStream", "stream"),          KernelInfo "rt.ServerStream_stream" 2 True)
    , (("ServerStream", "emit"),            KernelInfo "rt.ServerStream_emit" 2 True)
    , (("ServerStream", "finish"),          KernelInfo "rt.ServerStream_finish" 1 True)
    , (("ServerStream", "withContentType"), KernelInfo "rt.ServerStream_withContentType" 2 True)

    -- ═══════════════════════════════════════════════════════
    -- Sky.Core.WebSocket  (v0.15.46 — client-side bidirectional
    -- WebSocket; incoming frames flow via Sub.subscribeWebSocket)
    -- ═══════════════════════════════════════════════════════
    , (("WebSocket", "connect"),       KernelInfo "rt.WebSocket_connect" 1 True)
    , (("WebSocket", "connectWith"),   KernelInfo "rt.WebSocket_connectWith" 1 True)
    , (("WebSocket", "send"),          KernelInfo "rt.WebSocket_send" 2 True)
    , (("WebSocket", "sendBinary"),    KernelInfo "rt.WebSocket_sendBinary" 2 True)
    , (("WebSocket", "close"),         KernelInfo "rt.WebSocket_close" 1 True)
    , (("WebSocket", "closeWithCode"), KernelInfo "rt.WebSocket_closeWithCode" 3 True)
    , (("Sub", "subscribeWebSocket"),  KernelInfo "rt.Sub_subscribeWebSocket" 3 False)

    -- ═══════════════════════════════════════════════════════
    -- Sky.Http.Server.WebSocket  (v0.15.46 — server-side
    -- WebSocket upgrade; mirror of WebSocket above)
    -- ═══════════════════════════════════════════════════════
    , (("ServerWebSocket", "upgrade"),             KernelInfo "rt.ServerWebSocket_upgrade" 2 True)
    , (("ServerWebSocket", "sendToClient"),        KernelInfo "rt.ServerWebSocket_sendToClient" 2 True)
    , (("ServerWebSocket", "sendBinaryToClient"),  KernelInfo "rt.ServerWebSocket_sendBinaryToClient" 2 True)
    , (("ServerWebSocket", "broadcast"),           KernelInfo "rt.ServerWebSocket_broadcast" 2 True)
    , (("ServerWebSocket", "closeClient"),         KernelInfo "rt.ServerWebSocket_closeClient" 1 True)

    -- ═══════════════════════════════════════════════════════
    -- Std.PubSub  (Cycle 4 PT — Task-shaped publish, callable from
    -- any context; complements Cmd.publish which only fires from
    -- a Sky.Live update return).
    -- ═══════════════════════════════════════════════════════
    , (("PubSub", "publish"),     KernelInfo "rt.PubSub_publish" 2 True)
    , (("PubSub", "publishNoEcho"), KernelInfo "rt.PubSub_publishNoEcho" 2 True)

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
    , (("Db", "migrateApply"),    KernelInfo "rt.Db_migrateApply" 2 False)
    , (("Db", "execRaw"),         KernelInfo "rt.Db_execRaw" 2 False)
    , (("Db", "getField"),        KernelInfo "rt.Db_getField" 2 False)
    , (("Db", "getFieldOr"),      KernelInfo "rt.Db_getFieldOr" 3 False)
    , (("Db", "getString"),       KernelInfo "rt.Db_getString" 2 False)
    , (("Db", "getInt"),          KernelInfo "rt.Db_getInt" 2 False)
    , (("Db", "getBool"),         KernelInfo "rt.Db_getBool" 2 False)
    , (("Db", "query"),           KernelInfo "rt.Db_query" 3 False)
    , (("Db", "queryDecode"),     KernelInfo "rt.Db_queryDecode" 4 False)
    , (("Db", "getByIdDecode"),   KernelInfo "rt.Db_getByIdDecode" 4 False)
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

    -- ═══════════════════════════════════════════════════════
    -- Std.Db.Decode — typed DB row decoders (v0.15.45 Layer 3)
    -- ═══════════════════════════════════════════════════════
    , (("DbDec", "string"),      KernelInfo "rt.DbDec_string" 1 False)
    , (("DbDec", "int"),         KernelInfo "rt.DbDec_int" 1 False)
    , (("DbDec", "float"),       KernelInfo "rt.DbDec_float" 1 False)
    , (("DbDec", "bool"),        KernelInfo "rt.DbDec_bool" 1 False)
    , (("DbDec", "nullable"),    KernelInfo "rt.DbDec_nullable" 2 False)
    , (("DbDec", "succeed"),     KernelInfo "rt.DbDec_succeed" 1 False)
    , (("DbDec", "fail"),        KernelInfo "rt.DbDec_fail" 1 False)
    , (("DbDec", "map"),         KernelInfo "rt.DbDec_map" 2 False)
    , (("DbDec", "andThen"),     KernelInfo "rt.DbDec_andThen" 2 False)
    , (("DbDec", "andMap"),      KernelInfo "rt.DbDec_andMap" 2 False)
    , (("DbDec", "map2"),        KernelInfo "rt.DbDec_map2" 3 False)
    , (("DbDec", "map3"),        KernelInfo "rt.DbDec_map3" 4 False)
    , (("DbDec", "map4"),        KernelInfo "rt.DbDec_map4" 5 False)
    , (("DbDec", "map5"),        KernelInfo "rt.DbDec_map5" 6 False)
    , (("DbDec", "required"),    KernelInfo "rt.DbDec_required" 3 False)
    , (("DbDec", "optional"),    KernelInfo "rt.DbDec_optional" 4 False)

    -- ═══════════════════════════════════════════════════════
    -- Std.Ui.Lazy (v0.12 — runtime memoisation)
    --
    -- Maps the Sky-side passthrough wrappers to Go runtime helpers
    -- that memoise on (function-pointer, args fingerprint) with an
    -- LRU bound (default 1024 entries; SKY_UI_LAZY_CAP override).
    -- The Sky source in `sky-stdlib/Std/Ui/Lazy.sky` is now a
    -- type-checker reference only — actual calls route through
    -- the kernel registry below.
    -- ═══════════════════════════════════════════════════════
    , (("Lazy", "lazy"),  KernelInfo "rt.Std_Ui_Lazy_lazy"  2 False)
    , (("Lazy", "lazy2"), KernelInfo "rt.Std_Ui_Lazy_lazy2" 3 False)
    , (("Lazy", "lazy3"), KernelInfo "rt.Std_Ui_Lazy_lazy3" 4 False)
    , (("Lazy", "lazy4"), KernelInfo "rt.Std_Ui_Lazy_lazy4" 5 False)
    , (("Lazy", "lazy5"), KernelInfo "rt.Std_Ui_Lazy_lazy5" 6 False)

    -- ═══════════════════════════════════════════════════════
    -- v0.15.47 stdlib quality-of-life batch
    -- ═══════════════════════════════════════════════════════
    --
    -- Std.Compression — gzip + zstd
    , (("Compression", "gzip"),            KernelInfo "rt.Compression_gzip" 1 False)
    , (("Compression", "gunzip"),          KernelInfo "rt.Compression_gunzip" 1 False)
    , (("Compression", "zstdCompress"),    KernelInfo "rt.Compression_zstdCompress" 1 False)
    , (("Compression", "zstdDecompress"),  KernelInfo "rt.Compression_zstdDecompress" 1 False)

    -- Std.Csv — encode + decode
    , (("Csv", "parse"),                   KernelInfo "rt.Csv_parse" 1 False)
    , (("Csv", "parseWithDelimiter"),      KernelInfo "rt.Csv_parseWithDelimiter" 2 False)
    , (("Csv", "encode"),                  KernelInfo "rt.Csv_encode" 1 False)
    , (("Csv", "encodeWithDelimiter"),     KernelInfo "rt.Csv_encodeWithDelimiter" 2 False)
    , (("Csv", "parseStreamFromFile"),     KernelInfo "rt.Csv_parseStreamFromFile" 1 False)

    -- Std.Cache — LRU + TTL
    , (("Cache", "newRaw"),                KernelInfo "rt.Cache_new" 1 False)
    , (("Cache", "get"),                   KernelInfo "rt.Cache_get" 2 False)
    , (("Cache", "put"),                   KernelInfo "rt.Cache_put" 3 False)
    , (("Cache", "remove"),                KernelInfo "rt.Cache_remove" 2 False)
    , (("Cache", "clear"),                 KernelInfo "rt.Cache_clear" 1 False)
    , (("Cache", "size"),                  KernelInfo "rt.Cache_size" 1 False)
    , (("Cache", "stats"),                 KernelInfo "rt.Cache_stats" 1 False)

    -- Std.Email — provider-abstract email send
    , (("Email", "send"),                  KernelInfo "rt.Email_send" 2 False)

    -- Std.Config — typed TOML/YAML/JSON
    , (("Config", "string"),               KernelInfo "rt.Config_string" 0 False)
    , (("Config", "int"),                  KernelInfo "rt.Config_int" 0 False)
    , (("Config", "float"),                KernelInfo "rt.Config_float" 0 False)
    , (("Config", "bool"),                 KernelInfo "rt.Config_bool" 0 False)
    , (("Config", "nullable"),             KernelInfo "rt.Config_nullable" 1 False)
    , (("Config", "field"),                KernelInfo "rt.Config_field" 2 False)
    , (("Config", "at"),                   KernelInfo "rt.Config_at" 2 False)
    , (("Config", "list"),                 KernelInfo "rt.Config_list" 1 False)
    , (("Config", "succeed"),              KernelInfo "rt.Config_succeed" 1 False)
    , (("Config", "fail"),                 KernelInfo "rt.Config_fail" 1 False)
    , (("Config", "map"),                  KernelInfo "rt.Config_map" 2 False)
    , (("Config", "andThen"),              KernelInfo "rt.Config_andThen" 2 False)
    , (("Config", "decodeToml"),           KernelInfo "rt.Config_decodeToml" 2 False)
    , (("Config", "decodeYaml"),           KernelInfo "rt.Config_decodeYaml" 2 False)
    , (("Config", "decodeJson"),           KernelInfo "rt.Config_decodeJson" 2 False)
    , (("Config", "loadFromFile"),         KernelInfo "rt.Config_loadFromFile" 2 False)

    -- ═══════════════════════════════════════════════════════
    -- Hub (v0.16.4 Option B B4) — bundled console's SQLite-backed
    -- Store. Used when the console runs IN-PROCESS with the
    -- `sky console-serve` hub daemon. Same Sky-side shape as the
    -- embedded console's httpStore (Main.sky) — drop-in factory.
    --
    -- Runtime: runtime-go/rt/hub_bridge.go declares the
    -- HubStoreReader interface + the Hub_* kernels; the hub-side
    -- impl lives in runtime-go/rt/hub/bridge.go and registers via
    -- rt.SetHubStore at hub.Run startup.
    -- ═══════════════════════════════════════════════════════
    , (("Hub", "readOverview"),     KernelInfo "rt.Hub_readOverview" 1 False)
    , (("Hub", "readLogs"),         KernelInfo "rt.Hub_readLogs" 2 False)
    , (("Hub", "readMetrics"),      KernelInfo "rt.Hub_readMetrics" 1 False)
    , (("Hub", "readTraces"),       KernelInfo "rt.Hub_readTraces" 1 False)
    , (("Hub", "readErrors"),       KernelInfo "rt.Hub_readErrors" 1 False)
    , (("Hub", "listServices"),     KernelInfo "rt.Hub_listServices" 1 False)
    -- v0.16.4 B5: per-service rollup (req/s, p95, error rate +
    -- sparkline windows). One row per distinct service_name in the
    -- hub's hot store.  Returns List ServiceStat — narrows through
    -- the same rt.Coerce → narrowMapToStruct path as the other
    -- Hub_* readers.
    , (("Hub", "readServiceStats"), KernelInfo "rt.Hub_readServiceStats" 1 False)
    -- v0.16.4 B6: per-service drill-down kernels. Each takes the
    -- service name as a leading String arg; an empty name means
    -- "no filter" (all services). Used by the drill-down tab pages
    -- (LogsTab.sky / MetricsTab.sky / TracesTab.sky / ErrorsTab.sky)
    -- when the user picks a service from the multi-service Overview.
    , (("Hub", "readFilteredLogs"),    KernelInfo "rt.Hub_readFilteredLogs" 3 False)
    , (("Hub", "readFilteredMetrics"), KernelInfo "rt.Hub_readFilteredMetrics" 2 False)
    , (("Hub", "readFilteredTraces"),  KernelInfo "rt.Hub_readFilteredTraces" 2 False)
    , (("Hub", "readFilteredErrors"),  KernelInfo "rt.Hub_readFilteredErrors" 2 False)
    -- v0.16.5 #493: identity-aware kernel.  Returns the currently
    -- signed-in Std.Live.Console.Identity from the live session
    -- (populated at session-mint time by dispatchRoot from r.Context
    -- written by the auth gate).  Sky-side callers use this to read
    -- `claims.tenant` and pre-filter their queries.
    , (("Hub", "currentIdentity"),     KernelInfo "rt.Hub_currentIdentity" 1 False)
    ]
