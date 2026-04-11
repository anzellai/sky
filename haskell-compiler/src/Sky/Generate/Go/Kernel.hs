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
    , (("String", "replace"),     KernelInfo "rt.String_replace" 3 False)
    , (("String", "slice"),       KernelInfo "rt.String_slice" 3 False)

    -- ═══════════════════════════════════════════════════════
    -- List
    -- ═══════════════════════════════════════════════════════
    , (("List", "map"),           KernelInfo "rt.List_map" 2 True)
    , (("List", "filter"),        KernelInfo "rt.List_filter" 2 True)
    , (("List", "foldl"),         KernelInfo "rt.List_foldl" 3 True)
    , (("List", "foldr"),         KernelInfo "rt.List_foldr" 3 True)
    , (("List", "length"),        KernelInfo "rt.List_length" 1 True)
    , (("List", "head"),          KernelInfo "rt.List_head" 1 True)
    , (("List", "tail"),          KernelInfo "rt.List_tail" 1 True)
    , (("List", "take"),          KernelInfo "rt.List_take" 2 True)
    , (("List", "drop"),          KernelInfo "rt.List_drop" 2 True)
    , (("List", "append"),        KernelInfo "rt.List_append" 2 True)
    , (("List", "concat"),        KernelInfo "rt.List_concat" 1 True)
    , (("List", "concatMap"),     KernelInfo "rt.List_concatMap" 2 True)
    , (("List", "reverse"),       KernelInfo "rt.List_reverse" 1 True)
    , (("List", "sort"),          KernelInfo "rt.List_sort" 1 True)
    , (("List", "member"),        KernelInfo "rt.List_member" 2 True)
    , (("List", "any"),           KernelInfo "rt.List_any" 2 True)
    , (("List", "all"),           KernelInfo "rt.List_all" 2 True)
    , (("List", "range"),         KernelInfo "rt.List_range" 2 False)
    , (("List", "zip"),           KernelInfo "rt.List_zip" 2 True)
    , (("List", "filterMap"),     KernelInfo "rt.List_filterMap" 2 True)

    -- ═══════════════════════════════════════════════════════
    -- Dict
    -- ═══════════════════════════════════════════════════════
    , (("Dict", "empty"),         KernelInfo "rt.Dict_empty" 0 True)
    , (("Dict", "insert"),        KernelInfo "rt.Dict_insert" 3 True)
    , (("Dict", "get"),           KernelInfo "rt.Dict_get" 2 True)
    , (("Dict", "remove"),        KernelInfo "rt.Dict_remove" 2 True)
    , (("Dict", "member"),        KernelInfo "rt.Dict_member" 2 True)
    , (("Dict", "keys"),          KernelInfo "rt.Dict_keys" 1 True)
    , (("Dict", "values"),        KernelInfo "rt.Dict_values" 1 True)
    , (("Dict", "toList"),        KernelInfo "rt.Dict_toList" 1 True)
    , (("Dict", "fromList"),      KernelInfo "rt.Dict_fromList" 1 True)
    , (("Dict", "map"),           KernelInfo "rt.Dict_map" 2 True)
    , (("Dict", "foldl"),         KernelInfo "rt.Dict_foldl" 3 True)
    , (("Dict", "union"),         KernelInfo "rt.Dict_union" 2 True)

    -- ═══════════════════════════════════════════════════════
    -- Maybe
    -- ═══════════════════════════════════════════════════════
    , (("Maybe", "withDefault"),  KernelInfo "rt.Maybe_withDefault" 2 True)
    , (("Maybe", "map"),          KernelInfo "rt.Maybe_map" 2 True)
    , (("Maybe", "andThen"),      KernelInfo "rt.Maybe_andThen" 2 True)

    -- ═══════════════════════════════════════════════════════
    -- Result
    -- ═══════════════════════════════════════════════════════
    , (("Result", "withDefault"), KernelInfo "rt.Result_withDefault" 2 True)
    , (("Result", "map"),         KernelInfo "rt.Result_map" 2 True)
    , (("Result", "andThen"),     KernelInfo "rt.Result_andThen" 2 True)
    , (("Result", "mapError"),    KernelInfo "rt.Result_mapError" 2 True)

    -- ═══════════════════════════════════════════════════════
    -- Task
    -- ═══════════════════════════════════════════════════════
    , (("Task", "succeed"),       KernelInfo "rt.Task_succeed" 1 True)
    , (("Task", "fail"),          KernelInfo "rt.Task_fail" 1 True)
    , (("Task", "map"),           KernelInfo "rt.Task_map" 2 True)
    , (("Task", "andThen"),       KernelInfo "rt.Task_andThen" 2 True)
    , (("Task", "perform"),       KernelInfo "rt.Task_perform" 1 True)
    , (("Task", "sequence"),      KernelInfo "rt.Task_sequence" 1 True)
    , (("Task", "parallel"),      KernelInfo "rt.Task_parallel" 1 True)
    , (("Task", "lazy"),          KernelInfo "rt.Task_lazy" 1 True)

    -- ═══════════════════════════════════════════════════════
    -- Cmd
    -- ═══════════════════════════════════════════════════════
    , (("Cmd", "none"),           KernelInfo "rt.Cmd_none" 0 True)
    , (("Cmd", "batch"),          KernelInfo "rt.Cmd_batch" 1 True)
    , (("Cmd", "perform"),        KernelInfo "rt.Cmd_perform" 2 True)

    -- ═══════════════════════════════════════════════════════
    -- Time
    -- ═══════════════════════════════════════════════════════
    , (("Time", "now"),           KernelInfo "rt.Time_now" 0 False)
    , (("Time", "sleep"),         KernelInfo "rt.Time_sleep" 1 False)
    , (("Time", "every"),         KernelInfo "rt.Time_every" 2 True)

    -- ═══════════════════════════════════════════════════════
    -- Random
    -- ═══════════════════════════════════════════════════════
    , (("Random", "int"),         KernelInfo "rt.Random_int" 2 False)
    , (("Random", "float"),       KernelInfo "rt.Random_float" 2 False)
    , (("Random", "choice"),      KernelInfo "rt.Random_choice" 1 True)
    , (("Random", "shuffle"),     KernelInfo "rt.Random_shuffle" 1 True)

    -- ═══════════════════════════════════════════════════════
    -- Debug / Log
    -- ═══════════════════════════════════════════════════════
    , (("Debug", "log"),          KernelInfo "rt.Debug_log" 2 True)
    , (("Debug", "toString"),     KernelInfo "rt.Debug_toString" 1 True)
    , (("Log", "println"),        KernelInfo "rt.Log_println" 1 False)
    ]
