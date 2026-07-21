-- | Sky type to Go type mapping.
-- Maps canonical Sky types to Go type strings for typed code generation.
-- Uses Go generics (1.18+): SkyList[T], SkyResult[E, T], etc.
--
-- v0.17 C1 — Foundation. This module ships TWO surfaces:
--
--   1. The legacy 'typeToGo :: T.Type -> String' (existing — used by
--      'Sky.Build.Compile.typedFuncSig' at parameter / return-type
--      annotation time for pre-specialised canonical types).
--
--   2. A typed 'GoType' ADT + 'RenderEnv' + 'renderGoType' (new —
--      no callers yet; lives alongside (1) to prove the rendering
--      pipeline works in isolation).
--
-- C2 will introduce 'mapSkyTypeToGo :: MappingContext -> T.Type -> GoType'
-- and a differential parity test against 'typeToGo' so future commits can
-- migrate callers off the lossy String-rewriting path without behaviour
-- drift. See @docs/v0.17-fully-typed-codegen-v5-plan.md@.
module Sky.Generate.Go.Type where

import qualified Data.Char as Char
import Data.List (isSuffixOf)
import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Sky.Generate.Go.RuntimeMaps as RuntimeMaps
import qualified Sky.Generate.Go.AnonRecords as AnonRec
import qualified Sky.AST.Canonical as Can
import qualified Sky.Type.Type as T
import qualified Sky.Sky.ModuleName as ModuleName
-- v0.17 PR-4 — buildMappingContext imports CodegenEnv from Record.hs.
-- Record.hs has no Sky.Generate.Go.Type dependency, so this import is
-- acyclic. If Record.hs ever needs to consume MappingContext directly,
-- the right move is to extract the env type to a shared Sky.Generate.Go.Env
-- module — but PR-4 doesn't need that yet.
import qualified Sky.Generate.Go.Record as Rec


-- | Convert a canonical Sky type to a Go type string
typeToGo :: T.Type -> String
typeToGo t = case t of
    T.TVar name ->
        goTypeParam name

    T.TUnit ->
        "struct{}"

    T.TLambda from to ->
        "func(" ++ typeToGo from ++ ") " ++ typeToGo to

    T.TTuple _ _ [] ->
        "rt.SkyTuple2"

    T.TTuple _ _ [_] ->
        "rt.SkyTuple3"

    T.TTuple{} ->
        "rt.SkyTupleN"  -- arity ≥ 4 uses the slice-backed variant

    T.TRecord fields Nothing ->
        goRecordType fields

    T.TRecord fields (Just ext) ->
        -- Extensible record — fall back to interface
        "any /* extensible record */"

    T.TType home name args ->
        goNamedType home name args

    T.TAlias home name pairs (T.Hoisted inner) ->
        typeToGo inner

    T.TAlias home name pairs (T.Filled inner) ->
        typeToGo inner


-- | Map a type variable name to a Go type parameter
-- a -> A, b -> B, comparable -> C, etc.
goTypeParam :: String -> String
goTypeParam name = case name of
    [c] | c >= 'a' && c <= 'z' -> [toEnum (fromEnum c - 32)]  -- a -> A
    "comparable" -> "comparable"
    "number"     -> "rt.SkyNumber"
    "appendable" -> "rt.SkyAppendable"
    _            -> "T_" ++ name


-- | Map a named type constructor to Go
goNamedType :: ModuleName.Canonical -> String -> [T.Type] -> String
goNamedType home name args = case (ModuleName.toString home, name) of
    -- Primitives
    ("Sky.Core.Basics", "Int")    -> "int"
    ("Sky.Core.Basics", "Float")  -> "float64"
    ("Sky.Core.Basics", "Bool")   -> "bool"
    ("Sky.Core.Basics", "String") -> "string"
    ("Sky.Core.Basics", "Char")   -> "rune"
    (_, "Int")    -> "int"
    (_, "Float")  -> "float64"
    (_, "Bool")   -> "bool"
    (_, "String") -> "string"
    (_, "Char")   -> "rune"
    (_, "Bytes")  -> "[]byte"

    -- Parameterised core types
    (_, "List")   -> case args of
        [elem] -> "rt.SkyList[" ++ typeToGo elem ++ "]"
        _      -> "rt.SkyList[any]"

    (_, "Maybe")  -> case args of
        [inner] -> "rt.SkyMaybe[" ++ typeToGo inner ++ "]"
        _       -> "rt.SkyMaybe[any]"

    (_, "Result") -> case args of
        [err, ok] -> "rt.SkyResult[" ++ typeToGo err ++ ", " ++ typeToGo ok ++ "]"
        _         -> "rt.SkyResult[any, any]"

    (_, "Task") -> case args of
        [err, ok] -> "rt.SkyTask[" ++ typeToGo err ++ ", " ++ typeToGo ok ++ "]"
        _         -> "rt.SkyTask[any, any]"

    (_, "Dict") -> case args of
        [k, v] -> "rt.SkyDict[" ++ typeToGo k ++ ", " ++ typeToGo v ++ "]"
        _      -> "rt.SkyDict[any, any]"

    (_, "Set") -> case args of
        [elem] -> "rt.SkySet[" ++ typeToGo elem ++ "]"
        _      -> "rt.SkySet[any]"

    -- v0.17 PR-22 parity — Cmd/Sub typed emission for concrete args,
    -- bare for TVar args (matches the widened structural pipeline
    -- mapSkyTypeToGo).  Runtime exposes `rt.SkyCmd = cmdT` AND
    -- `rt.SkyCmd_T[T any] = cmdT` as aliases, so both forms accept
    -- the same value; the typed form preserves more HM context at
    -- emission sites where Msg is concrete.
    (_, "Cmd") -> case args of
        [T.TVar _] -> "rt.SkyCmd"
        [msg]      -> "rt.SkyCmd_T[" ++ typeToGo msg ++ "]"
        _          -> "rt.SkyCmd"

    (_, "Sub") -> case args of
        [T.TVar _] -> "rt.SkySub"
        [msg]      -> "rt.SkySub_T[" ++ typeToGo msg ++ "]"
        _          -> "rt.SkySub"

    -- Std.Html.Html — the Layer-3 HTML ADT. htmlType in the
    -- constraint generator carries an empty home (so it unifies
    -- with a user `Html Msg` annotation regardless of import path),
    -- which would otherwise render unqualified and break go build
    -- ("undefined: Html"). Map it to the generated type here, the
    -- same way Cmd/Sub are. It codegens non-generic (`= rt.SkyADT`),
    -- so the `msg` arg is dropped.
    (_, "Html") -> "Std_Html_Html"

    -- User-defined types: Module_Name or Module_Name[T1, T2].
    -- v0.17 PR-22 S6 fix: when home is "Main" or empty,
    -- 'goModulePrefix' returns "" — emit the bare name (matches
    -- legacy convention; the entry-module ADT declaration emits as
    -- bare @Page@, not @Main_Page@).
    _ ->
        let prefix = goModulePrefix home
            goName = if null prefix
                        then name
                        else prefix ++ "_" ++ name
        in case args of
            [] -> goName
            _  -> goName ++ "[" ++ commaJoin (map typeToGo args) ++ "]"


-- | Convert a record type to a Go anonymous struct
goRecordType :: Map.Map String T.FieldType -> String
goRecordType fields =
    let fieldStrs = map goFieldStr (Map.toList fields)
    in "struct{ " ++ joinWords fieldStrs ++ " }"
  where
    goFieldStr (name, T.FieldType _ ty) =
        capitalize name ++ " " ++ typeToGo ty ++ ";"

    capitalize [] = []
    capitalize (c:cs) = toEnum (fromEnum c - 32) : cs


-- | Module name to Go prefix: Sky.Core.List -> Sky_Core_List.
--
-- v0.17 PR-22 S6 fix — the entry module ("Main") and the unnamed
-- root module (empty string) STRIP to empty prefix.  Legacy codegen
-- uses this convention at every renderer site that builds a Go
-- module-prefixed identifier (Compile.hs:6643, 7003, 7028, 7946,
-- 8031, 8339, etc.):
--
--     prefix = if null modStr || modStr == "Main"
--                then ""
--                else map (\c -> if c == '.' then '_' else c) modStr ++ "_"
--
-- Without this strip, entry-module ADT references render as
-- @Main_Page@ but the corresponding declaration (emitted via
-- 'generateUnionTypes' in Compile.hs:4569) stays bare @Page@, so
-- @go build@ fails with @undefined: Main_Page@.
goModulePrefix :: ModuleName.Canonical -> String
goModulePrefix home =
    let s = ModuleName.toString home
    in if null s || s == "Main"
        then ""
        else map (\c -> if c == '.' then '_' else c) s


-- HELPERS

commaJoin :: [String] -> String
commaJoin [] = ""
commaJoin [x] = x
commaJoin (x:xs) = x ++ ", " ++ commaJoin xs


joinWords :: [String] -> String
joinWords [] = ""
joinWords [x] = x
joinWords (x:xs) = x ++ " " ++ joinWords xs


-- ============================================================================
-- v0.17 C1 — Typed Go-type ADT + rendering
-- ============================================================================
--
-- 'GoType' is the structural representation of every Go type shape Sky
-- emits.  It is the value object the rest of the typed-codegen pipeline
-- (C2-C25) will produce and consume, replacing the existing String-based
-- 'solvedTypeToGo' at @Sky.Build.Compile@ line 14831.
--
-- 'GoType' is intentionally minimal — every constructor maps to exactly
-- one Go syntactic form.  The 'GoRaw' escape hatch carries verbatim
-- strings for cases the structured constructors don't model (typed
-- comments like @any \/\* extensible record \*\/@).  v0.17 phases drive
-- the GoRaw count to zero.

data GoType
    = GoBare String               -- ^ "int", "string", "rune", "bool", "float64", "[]byte"
    | GoUnit                      -- ^ "struct{}" — Sky's @()@ unit type
    | GoAny                       -- ^ "any" — wildcard / unresolved TVar fallback
    | GoFunc GoType GoType        -- ^ @func(A) B@ — single-arg (Sky's
                                  --   default curried-function shape).
    | GoMultiFunc [GoType] GoType -- ^ v0.17 PR-12 — @func(A, B, ..., N) R@.
                                  --   Sky lambdas with N>1 patterns lower
                                  --   to a single Go function with N
                                  --   params (not a chain of N curried
                                  --   single-arg @GoFunc@s).  Used by
                                  --   @lowerTypedLambda@ to consume the
                                  --   structural slot-type shape without
                                  --   the lossy String-parsing detour
                                  --   through @parseFuncType@ (deleted
                                  --   at PR-12).
    | GoNamed String [GoType]     -- ^ @Module_Name@ or @rt.SkyList[T]@
    | GoStruct [(String, GoType)] -- ^ anonymous @struct{ Name T; ... }@
    | GoTypeVar String            -- ^ @T1@, @T2@ — Go type-parameter ident
    | GoTuple [GoType]            -- ^ @rt.T2[A, B]@ / @rt.T3[A, B, C]@ / @rt.SkyTupleN@.
                                  --   The 'renderTupleGeneric' policy gate on 'RenderEnv'
                                  --   controls whether the generic instantiation OR the
                                  --   back-compat alias (@rt.SkyTuple2 = T2[any, any]@) ships.
                                  --   v0.17 Step 4 (Cause H) widens callers to emit
                                  --   'GoTuple' for concrete-element shapes; tuples of
                                  --   ≥4 elements always render as the slice-backed
                                  --   non-parametric @rt.SkyTupleN@ irrespective of the
                                  --   gate (no Go-side generic variant exists).
    | GoRaw String                -- ^ escape hatch — verbatim Go type string
    deriving (Eq, Show)


-- | v0.17 PR-13 — structural substitution of generic-type-parameter
-- leaves (@T1@, @T2@, …) within a 'GoType'.  Mirrors the legacy
-- token-aware String 'substTVarsInGoType' (in @Sky.Build.Compile@)
-- but operates on the typed ADT, so the substitution is by
-- construction safe against tokeniser edge cases (Unicode identifiers,
-- collision with predeclared Go keywords, multi-character TVars like
-- @T10@).
--
-- The legacy 'substTVarsInGoType' rewrites IDENTIFIER tokens — both
-- 'GoTypeVar' leaves (the canonical T-vars) AND any GoNamed-rendered
-- identifier that happens to equal a key in σ.  The structural form
-- mirrors that behaviour: GoNamed nullary names are looked up in σ
-- (so `T2` rendered through 'GoNamed' still substitutes), but the
-- canonical case is the 'GoTypeVar' arm.
--
-- The "⚠ TCO RISK" pre-mortem on PR-13 (master plan): the
-- continue-block reassignment in tail-recursive functions emits
-- through `coerceCallArgsAt`'s σ-substituted param-type pipeline.
-- Any divergence between this structural substitution and the
-- legacy String form would alter the continue-block byte-shape and
-- regress TCO probes.  The round-trip parity property
-- @renderGoType env (substTVarsInGoTypeStructural σ' g) ==
-- substTVarsInGoType σ (renderGoType env g)@ (where σ' is the
-- GoType-typed sibling of σ) gates the structural cutover.
substTVarsInGoTypeStructural :: Map.Map String GoType -> GoType -> GoType
substTVarsInGoTypeStructural sigma = go
  where
    go t = case t of
        GoTypeVar n ->
            Map.findWithDefault t n sigma
        GoNamed n []
            -- Legacy 'substTVarsInGoType' rewrites identifier tokens;
            -- a nullary GoNamed renders as the bare name, so a TVar
            -- mis-classified as GoNamed (e.g. `T2` produced by the
            -- parser's `parseNameWithArgs` arm) still substitutes.
            -- Composite GoNamed (with args) recurses into args only.
            | Just replacement <- Map.lookup n sigma -> replacement
            | otherwise -> t
        GoNamed n args  -> GoNamed n (map go args)
        GoFunc a b      -> GoFunc (go a) (go b)
        GoMultiFunc as r -> GoMultiFunc (map go as) (go r)
        GoStruct fs     -> GoStruct [(fn, go ft) | (fn, ft) <- fs]
        GoTuple as      -> GoTuple (map go as)
        GoBare _        -> t
        GoUnit          -> t
        GoAny           -> t
        GoRaw _         -> t


-- | Structural accessor — return the type-argument list when 'GoType'
-- carries one ('GoNamed', 'GoTuple'), 'Nothing' otherwise.
--
-- Replaces the lossy String-parsing seam @parseTupleTypeArgs@
-- (currently at @Sky.Build.Compile@): consumers walking a 'GoType'
-- now access the structural args directly without re-tokenising the
-- rendered string.  See @docs/v0.17-cause-h-step4-blocker.md@.
goTypeArgs :: GoType -> Maybe [GoType]
goTypeArgs (GoNamed _ args) = Just args
goTypeArgs (GoTuple args)   = Just args
goTypeArgs _                = Nothing


-- | Rendering policy switches.
--
-- These mirror the data that today's @solvedTypeToGo@ reads ambiently
-- from @getCgEnv@ ('Sky.Build.Compile' line 14831 onward).  In C1 they
-- are policy gates only; C2 widens 'RenderEnv' with the actual env-derived
-- alias / union / runtime-typed maps required for the full mapping fn.
--
-- The boolean defaults reflect the CURRENT runtime shape (pre-v0.17):
--
--   * 'renderCmdGeneric' / 'renderSubGeneric' — False today because
--     @runtime-go/rt/live.go:1445@ declares @type SkyCmd = cmdT@
--     (non-generic).  C15-runtime makes them @True@.
--
--   * 'renderTupleGeneric' — False today because
--     @runtime-go/rt/rt.go:3344@ declares @type SkyTuple2 = T2[any, any]@.
--     C6a makes it @True@.
--
-- A renderer set to a True-future shape ahead of its runtime change
-- emits Go that won't compile.  The defaults guarantee parity with
-- today's emitted code.
data RenderEnv = RenderEnv
    { renderCmdGeneric    :: Bool
    , renderSubGeneric    :: Bool
    , renderTupleGeneric  :: Bool
    }
    deriving (Eq, Show)


-- | Conservative default — every policy switch in its today-shape
-- (no behaviour change vs. existing emitted Go).
defaultRenderEnv :: RenderEnv
defaultRenderEnv = RenderEnv
    { renderCmdGeneric    = False
    , renderSubGeneric    = False
    , renderTupleGeneric  = False
    }


-- | Render a 'GoType' to its Go source string.  Total — every
-- constructor handled, no partial pattern match.
--
-- Invariants:
--
--   * @renderGoType env (GoNamed n [])@ never appends @[]@ — nullary
--     named types render bare.
--
--   * Field order in 'GoStruct' is preserved verbatim.  The caller
--     (C2+ map-fn) is responsible for sorting by @_fieldIndex@ before
--     constructing the GoStruct.  This matches the existing
--     non-regression rule at CLAUDE.md §8 ("Record field enumeration
--     sorts by @_fieldIndex@ before any emission").
--
--   * 'renderGoType' never reads any env state today.  The 'RenderEnv'
--     parameter is threaded for future use by C-N commits that wire
--     policy gates into specific arms — adding a renderer arm that
--     branches on env in a later commit is mechanical.
renderGoType :: RenderEnv -> GoType -> String
renderGoType _   (GoBare s)         = s
renderGoType _   GoUnit             = "struct{}"
renderGoType _   GoAny              = "any"
renderGoType env (GoFunc from to)   =
    "func(" ++ renderGoType env from ++ ") " ++ renderGoType env to
renderGoType env (GoMultiFunc params ret) =
    "func(" ++ commaJoin (map (renderGoType env) params) ++ ") " ++ renderGoType env ret
renderGoType env (GoNamed n args)   =
    case args of
        [] -> n
        _  -> n ++ "[" ++ commaJoin (map (renderGoType env) args) ++ "]"
renderGoType env (GoStruct fields)  =
    "struct{ " ++ joinWords (map (renderField env) fields) ++ " }"
  where
    renderField e (name, ty) = name ++ " " ++ renderGoType e ty ++ ";"
renderGoType _   (GoTypeVar n)      = n
renderGoType env (GoTuple args)     =
    -- v0.17 PR-22 S6 — structural typed-tuple gate.  Mirrors legacy
    -- 'solvedTypeToGoBounded' TTuple arm (Compile.hs:17381-17391):
    -- when every element is a known concrete Go primitive (Int /
    -- Float / Bool / String / rt.T2[..] / rt.T3[..] / record alias
    -- with _R suffix / etc.), emit the typed generic form
    -- @rt.T2[A,B]@.  When any element is a TVar / GoAny / unresolved
    -- name, fall back to @rt.SkyTuple2@.  This matches legacy parity
    -- precisely: the SkyMaybe[(Float,Float)] case in 26-ui-showcase
    -- needs @rt.SkyMaybe[rt.T2[float64,float64]]@ to interop with
    -- struct-literal call sites that hand-write the typed form.
    --
    -- 'renderTupleGeneric env' override (legacy policy switch) is
    -- still honoured: when True, ALWAYS emit the typed form
    -- regardless of element shape.
    let isTypedTupleElem g = case g of
            GoAny                -> False
            GoTypeVar _          -> False
            GoBare "int"         -> True
            GoBare "float64"     -> True
            GoBare "string"      -> True
            GoBare "bool"        -> True
            GoBare "rune"        -> True
            GoBare "[]byte"      -> True
            GoBare s | take 5 s == "rt.T2"
                      || take 5 s == "rt.T3"
                      || take 5 s == "rt.T4"
                      || take 5 s == "rt.T5"
                      || take 5 s == "rt.T6"
                      || take 5 s == "rt.T7"
                      || take 5 s == "rt.T8"
                      || take 5 s == "rt.T9" -> True
            GoNamed n _ | "_R" `isSuffixOf` n -> True
            _                    -> False
        allTyped = not (null args) && all isTypedTupleElem args
        emitTyped = renderTupleGeneric env || allTyped
    in case args of
      [_, _]
        | emitTyped ->
            "rt.T2[" ++ commaJoin (map (renderGoType env) args) ++ "]"
        | otherwise -> "rt.SkyTuple2"
      [_, _, _]
        | emitTyped ->
            "rt.T3[" ++ commaJoin (map (renderGoType env) args) ++ "]"
        | otherwise -> "rt.SkyTuple3"
      _ -> "rt.SkyTupleN"
renderGoType _   (GoRaw s)          = s


-- ============================================================================
-- v0.17 PR-3 — parseGoType (inverse of renderGoType under genericEnv)
-- ============================================================================
--
-- 'parseGoType' is the inverse of 'renderGoType' under the "genericEnv"
-- shape — i.e. RenderEnv with @renderTupleGeneric = True@ (and the Cmd/Sub
-- generic switches also on). Under that shape every constructor renders
-- to a distinct string the parser can recognise.
--
-- The round-trip property (asserted by
-- 'test/Sky/Build/GoTypeRoundTripSpec.hs'):
--
-- @
--     parseGoType (renderGoType genericEnv x)  ==  Just (canonicalise x)
-- @
--
-- where @canonicalise@ rewrites @GoBare s@ to @GoNamed s []@ for any
-- non-primitive @s@ (the parser cannot distinguish 'GoBare "Foo"' from
-- 'GoNamed "Foo" []' — they both render to "Foo" — so canonicalisation
-- collapses to the named form for non-primitives).
--
-- LOSSY CASES (documented exceptions):
--
--   * 'GoTuple' rendered under @renderTupleGeneric = False@ collapses to
--     the back-compat alias @rt.SkyTuple2@ / @rt.SkyTuple3@ / @rt.SkyTupleN@.
--     The parser produces @GoNamed "rt.SkyTuple2" []@ etc. — the element
--     types are LOST in the rendered string and cannot be recovered.
--
--   * 'GoRaw' is an escape hatch carrying verbatim strings; parsing back
--     produces the closest structural match (often @GoNamed@ or @GoBare@)
--     and the round-trip succeeds only when the original GoRaw content
--     happens to be a canonical structural form.
--
-- Implementation: hand-written recursive-descent parser. Total — every
-- input string produces SOME GoType, even if it falls back to @GoRaw@.
--
-- 'parseGoType' returns @Nothing@ ONLY for syntactically malformed input
-- (unbalanced brackets, unterminated 'func(', empty input). Valid input
-- always parses to a structural shape.

-- | The "everything-typed-generic" RenderEnv — the round-trip target.
-- Mirrors the runtime shape v0.17 ships at Phase γ (Cmd/Sub/Tuple
-- generic-typed kernel sigs throughout).
genericRenderEnv :: RenderEnv
genericRenderEnv = RenderEnv
    { renderCmdGeneric    = True
    , renderSubGeneric    = True
    , renderTupleGeneric  = True
    }


-- | The canonical primitive Go-type set. Membership here decides
-- @GoBare s@ vs @GoNamed s []@ at parse time: a string equal to one of
-- these (modulo @[]<prim>@ container forms) parses as @GoBare@; any
-- other bare identifier parses as @GoNamed name []@.
isPrimitiveGoType :: String -> Bool
isPrimitiveGoType s = s `elem` primitiveGoTypes
                   || isByteContainer s
  where
    isByteContainer ('[' : ']' : rest) = rest `elem` ["byte", "rune"]
    isByteContainer _                  = False

primitiveGoTypes :: [String]
primitiveGoTypes =
    [ "int", "int8", "int16", "int32", "int64"
    , "uint", "uint8", "uint16", "uint32", "uint64"
    , "uintptr"
    , "float32", "float64"
    , "string", "rune", "byte", "bool"
    , "complex64", "complex128"
    , "error"
    ]


-- | Canonicalise a 'GoType' against the parser's output convention.
-- Rewrites @GoBare s@ to @GoNamed s []@ for non-primitive @s@, since
-- the rendered string @s@ alone cannot be distinguished from a nullary
-- @GoNamed@. Idempotent.
canonicaliseGoType :: GoType -> GoType
canonicaliseGoType g = case g of
    GoBare s
        | isPrimitiveGoType s -> GoBare s
        | otherwise           -> GoNamed s []
    GoFunc a b      -> GoFunc (canonicaliseGoType a) (canonicaliseGoType b)
    GoMultiFunc as r -> GoMultiFunc (map canonicaliseGoType as) (canonicaliseGoType r)
    GoNamed n args  -> GoNamed n (map canonicaliseGoType args)
    GoStruct fs     -> GoStruct (map (\(n, t) -> (n, canonicaliseGoType t)) fs)
    GoTuple args    -> GoTuple (map canonicaliseGoType args)
    other           -> other


-- | Parse a Go-type string back into a 'GoType'.
--
-- Returns @Just g@ when the input is syntactically well-formed; @Nothing@
-- on unbalanced brackets, unterminated @func(@, or empty input.
parseGoType :: String -> Maybe GoType
parseGoType raw =
    case trimWS raw of
        ""  -> Nothing
        s   -> parseTop s

  where
    -- Complete-string parser: must consume the WHOLE input.
    parseTop :: String -> Maybe GoType
    parseTop s
        | s == "any"           = Just GoAny
        | s == "struct{}"      = Just GoUnit
        | "func(" `isPrefixOf'` s = parseFunc s
        | "struct{" `isPrefixOf'` s && not ("struct{}" `isPrefixOf'` s)
                                 = parseStruct s
        -- v0.17 PR-12 — Go slice prefix `[]T` (e.g. `[]int`, `[]byte`,
        -- `[]rt.SkyMaybe[T]`).  Falls into 'GoBare' so the renderer
        -- emits the exact rendered string back.  Without this arm,
        -- 'parseGoType "[]int"' would route to 'parseNameWithArgs'
        -- which rejects `[` as a name char and returns 'Nothing' —
        -- breaking 'parseFuncType' for slot-types like
        -- `func([]int) []int`.  The bare-string preservation is OK
        -- because all consumers ('destructureSlotFunc' /
        -- 'renderGoType') just round-trip the rendered form.
        | "[]" `isPrefixOf'` s  = Just (GoBare s)
        | isPrimitiveGoType s   = Just (GoBare s)
        | isTypeVarTok s        = Just (GoTypeVar s)
        | otherwise             = parseNameWithArgs s

    -- "func(A) B"   → GoFunc      (single-arg, Sky-curried default)
    -- "func(A, B, ...) R" → GoMultiFunc (PR-12 — Sky lambda with N>1
    --                       patterns lowers to a single Go function
    --                       with N params, not nested GoFuncs).
    parseFunc :: String -> Maybe GoType
    parseFunc s = do
        let afterFunc = drop 4 s  -- drop "func"; opener "(" remains
        (inner, post) <- splitMatching '(' ')' afterFunc
        resT <- parseGoType (trimWS post)
        let trimmedInner = trimWS inner
            argParts = splitTopLevelComma trimmedInner
        case argParts of
            []  -> Nothing  -- "func() R" — not produced by current
                           --   pipeline; reject explicitly.
            [single] -> do
                argT <- parseGoType (trimWS single)
                Just (GoFunc argT resT)
            many -> do
                argTs <- mapM (parseGoType . trimWS) many
                Just (GoMultiFunc argTs resT)

    -- "struct{ Name T; Name2 T2; }"
    parseStruct :: String -> Maybe GoType
    parseStruct s = do
        let afterStruct = drop 6 s  -- drop "struct"; opener "{" remains
        (inner, post) <- splitMatching '{' '}' afterStruct
        if not (null (trimWS post))
            then Nothing  -- struct{...} must be the entire input
            else do
                fields <- parseStructFields (trimWS inner)
                Just (GoStruct fields)

    -- Inside-struct body: fields separated by ";". Each field "Name Type".
    -- Trailing semicolon allowed.
    parseStructFields :: String -> Maybe [(String, GoType)]
    parseStructFields s
        | null (trimWS s) = Just []
        | otherwise =
            let parts = filter (not . null . trimWS) (splitTopLevelSemi s)
            in mapM parseStructField parts
      where
        parseStructField str =
            let (nm, rest) = span (/= ' ') (trimWS str)
                tyStr     = trimWS rest
            in if null nm || null tyStr
                then Nothing
                else do
                    ty <- parseGoType tyStr
                    Just (nm, ty)

    -- "<Name>" or "<Name>[arg1, arg2, ...]"
    --
    -- Lex the longest run of name-chars at the start.  Then either:
    --   * end-of-input → bare nullary
    --   * '[' → split matching ']', parse args, classify head as
    --           rt.T2..rt.T9 (GoTuple) OR generic Named.
    --   * anything else → not a valid GoType
    parseNameWithArgs :: String -> Maybe GoType
    parseNameWithArgs s =
        let (nm, after) = span isNameChar s
        in if null nm
            then Nothing
            else case after of
                "" | isPrimitiveGoType nm -> Just (GoBare nm)
                "" | isTypeVarTok nm      -> Just (GoTypeVar nm)
                "" -> Just (GoNamed nm [])
                '[':rest -> do
                    -- Splitter expects opener as first char.
                    (innerArgs, post) <- splitMatching '[' ']' ('[' : rest)
                    if not (null (trimWS post))
                        then Nothing  -- trailing garbage after ']'
                        else do
                            args <- mapM (parseGoType . trimWS)
                                        (splitTopLevelComma innerArgs)
                            case classifyTupleHead nm (length args) of
                                Just _  -> Just (GoTuple args)
                                Nothing -> Just (GoNamed nm args)
                _ -> Nothing  -- e.g. trailing space + junk

    -- "rt.T2"/"rt.T3"/.../"rt.T9" → tuple arity matches.
    classifyTupleHead :: String -> Int -> Maybe Int
    classifyTupleHead nm n = case nm of
        ['r','t','.','T', d]
            | d >= '2' && d <= '9'
            , (fromEnum d - fromEnum '0') == n
            -> Just n
        _ -> Nothing

    -- "T" + 1+ digits, no other chars → GoTypeVar
    isTypeVarTok :: String -> Bool
    isTypeVarTok ('T':ds@(_:_)) = all isDigit ds
    isTypeVarTok _              = False

    isDigit c = c >= '0' && c <= '9'

    -- Characters that may appear inside an identifier / head name.
    -- Note: '[' ']' '(' ')' ',' ' ' are STRUCTURAL — they end the name.
    -- '*' supported as leading char for pointer notation; '/' for paths
    -- inside synthetic qualifier strings.
    isNameChar :: Char -> Bool
    isNameChar c = (c >= 'a' && c <= 'z')
                || (c >= 'A' && c <= 'Z')
                || (c >= '0' && c <= '9')
                || c == '_' || c == '.' || c == '*' || c == '/'

-- ─────────────────────────────────────────────────────────────────────────────
-- Helpers (parser-private)
-- ─────────────────────────────────────────────────────────────────────────────

isPrefixOf' :: String -> String -> Bool
isPrefixOf' []     _      = True
isPrefixOf' _      []     = False
isPrefixOf' (a:as) (b:bs) = a == b && isPrefixOf' as bs

trimWS :: String -> String
trimWS = dropWhile (== ' ') . reverse . dropWhile (== ' ') . reverse

-- | Given input starting at the FIRST occurrence of @open@, find the
-- matching @close@ and return (inside, after-close). Tracks nested pairs.
splitMatching :: Char -> Char -> String -> Maybe (String, String)
splitMatching open close s = go 0 [] s
  where
    go _     _   []           = Nothing
    go depth acc (c:cs)
        | c == open  && depth == 0 = go 1 acc cs  -- consume opener
        | c == open                = go (depth + 1) (c:acc) cs
        | c == close && depth == 1 = Just (reverse acc, cs)
        | c == close               = go (depth - 1) (c:acc) cs
        | otherwise                = go depth (c:acc) cs

-- | Split a string on top-level commas — commas inside nested brackets
-- are NOT separators.
splitTopLevelComma :: String -> [String]
splitTopLevelComma = splitTopLevel ','

splitTopLevelSemi :: String -> [String]
splitTopLevelSemi = splitTopLevel ';'

splitTopLevel :: Char -> String -> [String]
splitTopLevel sep = go 0 [] []
  where
    go :: Int -> String -> [String] -> String -> [String]
    go _     acc out []        = reverse (reverse acc : out)
    go depth acc out (c:cs)
        | c == sep && depth == 0 = go depth [] (reverse acc : out) cs
        | c `elem` "([{"         = go (depth + 1) (c:acc) out cs
        | c `elem` ")]}"         = go (depth - 1) (c:acc) out cs
        | otherwise              = go depth (c:acc) out cs


-- ============================================================================
-- v0.17 C2 — Structural mapper Sky.Type -> GoType
-- ============================================================================
--
-- 'mapSkyTypeToGo' is the typed-mapping counterpart to the legacy
-- 'typeToGo'.  Same structural shape; same output string when paired
-- with 'renderGoType defaultRenderEnv' AND 'defaultMappingContext'.
--
-- The differential parity property (asserted by
-- 'test/Sky/Build/GoTypeAdtSpec.hs'):
--
-- @
--     typeToGo ty
--         ==
--     renderGoType defaultRenderEnv (mapSkyTypeToGo defaultMappingContext ty)
-- @
--
-- This commit's mapper is structurally minimal — it does NOT consult
-- 'MappingContext' data fields, only the embedded 'RenderEnv'.  C8+
-- widens 'MappingContext' with alias / union / runtime-typed maps that
-- 'solvedTypeToGo' currently reads from 'getCgEnv' ambiently
-- ('Sky.Build.Compile' line 14831 onward).  Each widening landed
-- alongside the call-site migration that needs it; the parity test
-- gates against drift.


-- | Per-call mapping context.  Carries everything 'mapSkyTypeToGo'
-- needs to convert a 'T.Type' to a 'GoType' AND everything
-- 'renderGoType' needs to render it back to a Go source string.
--
-- C2 shipped the minimal shape — just 'mcRenderEnv'.  PR-4 widens
-- with the env-derived data the foundation refactor consumes; each
-- field carries a "consumed by PR-N" comment naming the PR that flips
-- it from ignored-placeholder to actually-read.  PRs 5-10 fill them in.
--
-- Pre-mortem-derived discipline: the widening here is data-only.  No
-- semantic consumer reads any new field yet.  PRs 5-10 each enable
-- exactly one of these channels in lockstep with the renderer arm
-- that needs it, so the parity test gates against drift.
--
-- Show instance only — Can.Alias has no Eq instance.  Tests compare
-- fields individually rather than the whole record.
data MappingContext = MappingContext
    { mcRenderEnv      :: !RenderEnv
      -- ^ Render policy switches.  Always read by 'renderGoType'.

    -- v0.17 PR-4 fields (additive — no current consumer).
    , mcRecordAliases  :: !(Set.Set String)
      -- ^ Record-alias names (current module + every loaded dep,
      -- module-prefixed).  CONSUMED BY: PR-5 (GoTypeBuild emits the
      -- @_R@ suffix when a nominal type matches a member of this
      -- set; otherwise falls back to the bare module-prefixed name).

    , mcUnionNames     :: !(Set.Set String)
      -- ^ Module-prefixed union/ADT names with a corresponding
      -- @type X = rt.SkyADT@ alias emitted somewhere.  CONSUMED BY:
      -- PR-5 (GoTypeBuild reaches for @rt.SkyADT@ instead of @any@
      -- when a nominal type matches a member here).

    , mcEnumNames      :: !(Set.Set String)
      -- ^ Subset of 'mcUnionNames' whose Sky declaration is a pure
      -- enum (every constructor is nullary).  These emit as
      -- @type X = int@ at the runtime so zero value is @0@, not
      -- @X{}@.  CONSUMED BY: PR-5 (decides GoBare "int" vs the
      -- @rt.SkyADT@ alias for enum-shaped unions).

    , mcAliases        :: !(Map.Map String Can.Alias)
      -- ^ Alias declarations from the current module — keyed by
      -- alias name, value is the canonicalised RHS Sky type.
      -- CONSUMED BY: PR-7 (defuse @globalAnonRecords@ IORef —
      -- anon-record structural identity reaches the mapper via this
      -- field so the IORef can be retired).

    , mcTVarsToAny     :: !Bool
      -- ^ v0.17 Phase ε PR-22 — TVar lowering policy.  When True,
      -- 'mapSkyTypeToGo' emits 'GoAny' for every 'T.TVar' instead
      -- of 'GoTypeVar' (the default).  This matches the legacy
      -- 'solvedTypeToGoBounded' / 'typeStrWithAliasesRegBounded'
      -- TVar→\"any\" policy used at sites where leftover unsolved
      -- TVars must not leak as Go type-parameter identifiers
      -- (notably the monomorphisation σ_go substitution, where a
      -- leftover TVar refers to no enclosing type-parameter scope).
      -- CONSUMED BY: 'mapSkyTypeToGo' TVar arm.  Default False
      -- keeps every pre-existing call-site byte-identical.

    -- v0.17 PR-22 S1 fields (close runtimeTypedMap divergence).
    , mcQualRuntimeTyped :: ![((String, String), String)]
      -- ^ Module-qualified overrides — wins over bare-name lookup.
      -- Disambiguates same-named types in different stdlib modules
      -- (Sky.Core.Http.Response → @rt.HttpResponse@ vs
      -- Sky.Http.Server.Response → @rt.SkyResponse@).  CONSUMED BY:
      -- 'mapNamedType' user-ADT fallback — checked BEFORE
      -- 'mcRuntimeTypedMap'.  Source: 'RuntimeMaps.qualifiedRuntimeTypedMap'.

    , mcRuntimeTypedMap :: ![(String, String)]
      -- ^ Bare-name fallback mappings to runtime aliases
      -- (Decoder → @rt.SkyDecoder@, Error → @Sky_Core_Error_Error@,
      -- Db → @*rt.SkyDb@, etc.).  CONSUMED BY: 'mapNamedType' —
      -- checked AFTER 'mcQualRuntimeTyped' and BEFORE the bare-name
      -- fallback.  Closes the 1566+ hits of @undefined: Decoder@,
      -- @undefined: Sky_Core_Error_Error@, etc. that the previous
      -- bulk-replace surfaced.  Source: 'RuntimeMaps.runtimeTypedMap'.

    -- v0.17 PR-22 S2 — anon-record + alias-lookup machinery.
    , mcFieldIndex :: !Rec.RecordRegistry
      -- ^ Map from field-name set → alias name.  Drives the legacy
      -- 'lookupRecordAlias' shape inside 'mapRecordType': when an
      -- anonymous Sky 'T.TRecord' matches a known alias's field set,
      -- the renderer emits @Foo_R@ (or @Foo_R[args]@) instead of
      -- @struct {...}@ or a synthesised @Anon_R_…@ name.
      -- CONSUMED BY: 'mapRecordType'.  Source: 'Rec._cg_fieldIndex'.

    -- v0.17 close — bootstrap-phase parity policy.
    , mcDropTypeArgs :: !Bool
      -- ^ When True, 'mapAliasType' suppresses the @[args]@
      -- suffix on parametric record aliases so @TAlias ... [args]@
      -- emits as @Foo_R@ instead of @Foo_R[any]@.  Needed for
      -- bootstrap-phase callers (Compile.hs:safeReturnTypeBootstrap)
      -- because the corresponding Go declarations at this phase are
      -- non-parametric (@type Foo = rt.SkyADT@).  The full-env
      -- callers want @Foo_R[args]@ — the parametric Go-generic
      -- instantiation — so the default is False.  CONSUMED BY:
      -- 'mapAliasType'.

    , mcAssumeKnownUnion :: !Bool
      -- ^ When True, 'mapNamedType' treats every otherwise-unknown
      -- user-defined TType as if it were in 'mcUnionNames' — i.e.
      -- emits the bare module-qualified base name instead of
      -- @GoAny@.  Needed for bootstrap-phase callers because at
      -- that point 'mcUnionNames' is empty (the union registry
      -- isn't built yet) but every Sky user-ADT WILL be declared
      -- as @type Foo = rt.SkyADT@ by the time the emitted Go
      -- compiles.  Full-env callers want the @GoAny@ fallback
      -- (where unknown means genuinely unresolved), so the default
      -- is False.  CONSUMED BY: 'mapNamedType'.

    -- v0.17 Phase F — typeStrWithAliasesReg pipeline parity policies.
    , mcTVarSubst :: ![(String, String)]
      -- ^ TVar name → Go-type string substitution.  Consumed by
      -- 'mapSkyTypeToGo' TVar arm BEFORE 'mcTVarsToAny' and the
      -- 'goTypeParam' fallback: when name is in this assoc list,
      -- the rendered string is the mapped value verbatim (wrapped
      -- in 'GoBare').  Empty by default — only Phase F's
      -- @typeStrWithAliasesRegBoundedScopedViaPipeline@ populates
      -- it via overlay.  Closes the parametric-monomorphisation
      -- path where TVar @msg@ must render as e.g. @T1@ at the
      -- generic-instantiation site.  Source: caller's per-call
      -- @tvarMap@ argument.

    , mcDepModHint :: !(Maybe String)
      -- ^ Dep-vs-entry module mode hint.  @Nothing@ means we're
      -- emitting code for the entry module (typed Std.Html.Html_T
      -- variant available); @Just modName@ means we're inside a
      -- dep-module emission where typed-Html instantiation would
      -- refer to types not in scope (the dep's Go file doesn't see
      -- entry-module @Msg@).  Mirror of the legacy
      -- @typeStrWithAliasesRegBoundedScoped@'s @depModHint@ param
      -- (see Compile.hs:7944).  Default 'Nothing' BUT typed Html
      -- emission stays gated behind 'mcHtmlTypedEmit'.

    , mcHtmlTypedEmit :: !Bool
      -- ^ Master gate for the typed @Std_Html_Html_T[arg]@ variant
      -- in 'mapNamedType' Html arm.  When False (default), the
      -- bare @Std_Html_Html@ is always emitted — preserves
      -- byte-identical output for every pre-existing pipeline
      -- caller.  When True AND 'mcDepModHint' is 'Nothing' AND
      -- arg is concrete (not a TVar), emits the typed sibling.
      -- Only @typeStrWithAliasesRegBoundedScopedViaPipeline@
      -- enables this.

    , mcAliasSeen :: !(Set.Set String)
      -- ^ v0.17 Phase F-3 cycle guard for 'mapAliasType'.  Set of
      -- module-prefixed alias base names already on the recursion
      -- path.  Re-entering an alias name already in this set
      -- collapses the chain unfold to 'GoAny' — matches the
      -- legacy 'seen' set in
      -- 'typeStrWithAliasesRegBoundedScoped'
      -- (Compile.hs:7994).  Without this guard, self-referential
      -- aliases that fall through to @chainUnfolded@ enter an
      -- unbounded structural recursion and the Haskell runtime
      -- blackholes the thunk (\@\<\<loop\>\>\@).  Empty default —
      -- only 'mapAliasType' threads it through recursive descents.
    }
    deriving (Show)


-- | Conservative default — empty mapping context with today's
-- 'defaultRenderEnv' and empty env-derived maps.  Used by the
-- differential parity test in 'test/Sky/Build/GoTypeAdtSpec.hs'.
--
-- When 'mapSkyTypeToGo' doesn't consult the new PR-4 fields (which is
-- the case at PR-4 itself — only PRs 5-10 wire them up), 'defaultMappingContext'
-- and 'buildMappingContext env' produce byte-identical output.  That's
-- the foundation-no-behaviour-change contract.
defaultMappingContext :: MappingContext
defaultMappingContext = MappingContext
    { mcRenderEnv      = defaultRenderEnv
    , mcRecordAliases  = Set.empty
    , mcUnionNames     = Set.empty
    , mcEnumNames      = Set.empty
    , mcAliases        = Map.empty
    , mcTVarsToAny     = False
    -- v0.17 PR-22 S1 — static runtime-type registries are not
    -- env-derived, so they live as constants populated from the
    -- 'Sky.Generate.Go.RuntimeMaps' module.  Every MappingContext
    -- (default, flat, buildMappingContext-derived) carries them.
    , mcQualRuntimeTyped = RuntimeMaps.qualifiedRuntimeTypedMap
    , mcRuntimeTypedMap  = RuntimeMaps.runtimeTypedMap
    , mcFieldIndex       = Map.empty
    , mcDropTypeArgs     = False
    , mcAssumeKnownUnion = False
    , mcTVarSubst        = []
    , mcDepModHint       = Nothing
    , mcHtmlTypedEmit    = False
    , mcAliasSeen        = Set.empty
    }


-- | v0.17 Phase ε PR-22 — flat MappingContext that mirrors
-- 'solvedTypeToGoBounded' / 'typeStrWithAliasesRegBounded' TVar
-- semantics: every 'T.TVar' lowers to 'GoAny'.
--
-- Same env-derived defaults as 'defaultMappingContext'; only the
-- 'mcTVarsToAny' policy differs.  Used by 'solvedTypeToGoViaPipelineFlat'
-- in @Sky.Build.Compile@ at sites that must match legacy "any"
-- semantics (e.g. monomorphisation σ_go construction).
flatMappingContext :: MappingContext
flatMappingContext = defaultMappingContext { mcTVarsToAny = True }


-- | v0.17 close — bootstrap-phase MappingContext.  Used by
-- 'Sky.Build.Compile.safeReturnTypeBootstrap' (and its callers
-- across cross-module-externals building) at sites that run BEFORE
-- 'globalCgEnv' is finalised — only the alias set is available.
--
-- Compared to 'defaultMappingContext':
-- * 'mcRecordAliases' is populated from the caller's @Set String@.
-- * 'mcTVarsToAny' is True (matches legacy @safeReturnTypeBootstrap@'s
--   TVar-to-"any" semantics).
-- * Every other registry stays empty — no union recovery, no
--   field-index lookup (since 'globalCgEnv' isn't finalised yet).
--
-- This is the structural counterpart to the legacy bootstrap path
-- and ships as part of the Phase D close.
bootstrapMappingContext :: Set.Set String -> MappingContext
bootstrapMappingContext recAliases = defaultMappingContext
    { mcRecordAliases    = recAliases
    , mcTVarsToAny       = True
    , mcDropTypeArgs     = True
    , mcAssumeKnownUnion = True
    }


-- | Build a 'MappingContext' from the codegen environment that today's
-- 'solvedTypeToGo' reads ambient via 'getCgEnv'.  This is the structural
-- counterpart to the IORef-soup approach — same data, threaded explicitly.
--
-- Today this is the ONLY supported way to produce a non-default
-- 'MappingContext' that PRs 5-10 will treat as "real".  Direct field-by-
-- field construction is fine for tests but production code should always
-- go through this constructor so the field set stays in sync with
-- CodegenEnv as it evolves.
--
-- The 'mcRenderEnv' is taken as a separate argument because RenderEnv
-- carries policy switches (renderTupleGeneric, renderCmdGeneric, etc.)
-- that aren't derivable from CodegenEnv alone — they're flipped per-call
-- by Phase γ migrations.
buildMappingContext :: RenderEnv -> Rec.CodegenEnv -> MappingContext
buildMappingContext renderEnv cgEnv = MappingContext
    { mcRenderEnv      = renderEnv
    , mcRecordAliases  = Rec._cg_recordAliases cgEnv
    , mcUnionNames     = Rec._cg_unionNames cgEnv
    , mcEnumNames      = Rec._cg_enumNames cgEnv
    , mcAliases        = Rec._cg_aliases cgEnv
    , mcTVarsToAny     = False
    , mcQualRuntimeTyped = RuntimeMaps.qualifiedRuntimeTypedMap
    , mcRuntimeTypedMap  = RuntimeMaps.runtimeTypedMap
    , mcFieldIndex       = Rec._cg_fieldIndex cgEnv
    , mcDropTypeArgs     = False
    , mcAssumeKnownUnion = False
    , mcTVarSubst        = []
    , mcDepModHint       = Nothing
    , mcHtmlTypedEmit    = False
    , mcAliasSeen        = Set.empty
    }


-- | Map a canonical Sky type to its typed Go-type representation.
--
-- Structural mirror of 'typeToGo' — produces a 'GoType' whose
-- 'renderGoType' output equals 'typeToGo' on the same input, given
-- 'defaultMappingContext'.  C8+ MAY produce different output once
-- 'MappingContext' carries env-derived alias data — the legacy
-- 'typeToGo' has no equivalent path because it never had env access.
mapSkyTypeToGo :: MappingContext -> T.Type -> GoType
mapSkyTypeToGo ctx t = case t of
    T.TVar name
        -- v0.17 Phase F — TVar subst overlay.  When the caller's
        -- per-call tvarMap maps this name to a concrete Go-type
        -- string, emit it verbatim (legacy parity with
        -- 'typeStrWithAliasesRegBoundedScoped').  Falls through to
        -- the 'mcTVarsToAny' / 'goTypeParam' policy when the name
        -- isn't in the subst.
        | Just goStr <- lookup name (mcTVarSubst ctx) -> GoBare goStr
        | mcTVarsToAny ctx -> GoAny
        | otherwise        -> GoTypeVar (goTypeParam name)

    T.TUnit ->
        GoUnit

    T.TLambda from to ->
        GoFunc (mapSkyTypeToGo ctx from) (mapSkyTypeToGo ctx to)

    T.TTuple a b extras ->
        -- v0.17 PR 1 — structural TTuple → GoTuple.  Pre-PR-1 every
        -- arity dropped to a 'GoBare' alias ("rt.SkyTuple2" /
        -- "rt.SkyTuple3" / "rt.SkyTupleN") because there was no
        -- typed-element constructor.  The new 'GoTuple [GoType]'
        -- preserves element types end-to-end; 'renderGoType' still
        -- emits the alias form by default ('renderTupleGeneric'
        -- gate = False, the today-runtime shape), so the C2 parity
        -- property holds.  Cause-H Step 4 flips the gate per call
        -- site once consumers consult 'goTypeArgs' instead of
        -- 'parseTupleTypeArgs'.
        GoTuple (map (mapSkyTypeToGo ctx) (a : b : extras))

    -- v0.17 PR-22 S4 — both closed and open records route through
    -- 'mapRecordType'.  Legacy's @T.TRecord fields _@ arm (single
    -- pattern, ignored row-tail) emits the same alias-then-
    -- synthesised-name result whether the row is closed or open;
    -- the pre-S4 'any /* extensible record */' divergence would
    -- emit @any@ at Go sites where legacy emitted a typed alias
    -- (Surface 2 row-poly Cfg pattern), losing per-record
    -- discrimination at the field-access boundary.
    T.TRecord fields _ ->
        mapRecordType ctx fields

    T.TType home name args ->
        mapNamedType ctx home name args

    -- v0.17 PR-22 S3 — TAlias arm.  Mirror legacy
    -- 'solvedTypeToGoBounded' lookup order from Compile.hs:17394:
    --
    --   1. Match in 'mcRecordAliases' → @aliasName_R[args]@
    --   2. Runtime-typed override (qualified or bare)
    --   3. Aliased TRecord → @base_R[args]@
    --   4. Runtime-only → @any@
    --   5. Known union → @base@
    --   6. Chain unfolding into the aliased inner type
    --   7. Fallback → @any@
    --
    -- The pre-S3 fallback (always unfold to inner) silently dropped
    -- alias names + typeArgs, so the pipeline output diverged from
    -- legacy at every TAlias site.  After S3 the pipeline emits the
    -- same generic instantiation legacy already does for parametric
    -- record aliases (Surface 2 + 3).
    T.TAlias home name typeArgs aliasTy ->
        mapAliasType ctx home name typeArgs aliasTy


-- | Map a closed-record type to its Go form.
--
-- v0.17 PR-22 S2 — mirror the legacy 'solvedTypeToGoBounded' arm
-- (Compile.hs:17356-17366):
--
--   1. Look up @fields@ in 'mcFieldIndex'.  Match → emit
--      @AliasName_R[args]@ or @AliasName_R@ (bare).  Args resolution
--      is deferred to S3 — for now, bare @_R@ when there's a match.
--   2. No match → 'AnonRec.synthAnonRecordName' produces a
--      deterministic @Anon_R_…@ name AND registers the shape in the
--      global registry, so 'generateAnonRecordDecls' emits the
--      backing struct decl.  Emit @GoNamed synthName []@.
--
-- The pre-S2 fallback (inline @GoStruct@) diverged from the legacy
-- renderer: legacy ALWAYS emits a named type — never an inline
-- @struct{...}@.  An inline struct in a top-level type position
-- breaks Go's @type@ rules (a struct literal type is not a
-- well-formed type identifier in many positions).
mapRecordType :: MappingContext -> Map.Map String T.FieldType -> GoType
mapRecordType ctx fields =
    let names = Map.keys fields
    in case Rec.lookupRecordAlias (mcFieldIndex ctx) names of
        Just aliasName ->
            -- S2 narrows to the bare-alias shape — typed-args
            -- resolution lives in S3 because it requires
            -- 'aliasGenericArgs' machinery still in Compile.
            GoNamed (aliasName ++ "_R") []
        Nothing ->
            GoNamed (AnonRec.synthAnonRecordName fields) []


-- | v0.17 PR-22 S3 — Map a 'T.TAlias' to its Go form.
--
-- Lookup mirrors the legacy 'solvedTypeToGoBounded' TAlias arm at
-- 'Compile.hs:17394' (lookup order in the function comment).  Key
-- points:
--
-- * @typeArgs@ is @[(String, T.Type)]@; the @snd@ projection feeds
--   the generic-args suffix (matching legacy's
--   @[ recCycle argTy | (_, argTy) <- typeArgs ]@).
--
-- * Empty-home + cross-module qualified-candidate recovery uses the
--   same trailing-segment match as the legacy: walk every
--   registered alias name (@Mod_Name@) and admit candidates whose
--   last underscore-segment equals @name@.
--
-- * @isRecord@ tests the aliased inner type — if the alias resolves
--   to a 'T.TRecord' shape, we know the base name has a backing
--   record-alias struct (@Foo_R@).
--
-- * No cycle 'seen' set yet — the @Hoisted/Filled@ unwrap is one
--   level so cycles are limited by the canonicaliser's own
--   alias-expansion bound, not the renderer.  Phase ε's
--   structural recursion via 'mapSkyTypeToGo' continues without a
--   'seen' set because every recursive arm bottoms out at
--   primitives.  The legacy 'seen' guard mattered for the
--   @recursedFromChain@ branch which fed the inner @aliasTy@ back
--   into the renderer — we deliberately only invoke the chain
--   fallback when nothing else matches, and the pipeline's
--   bounded slot-by-slot rendering means a self-referential alias
--   would unfold once and the inner @TAlias@ would hit the same
--   match-first → @_R@ branch, terminating cleanly.
mapAliasType
    :: MappingContext
    -> ModuleName.Canonical
    -> String
    -> [(String, T.Type)]
    -> T.AliasType
    -> GoType
mapAliasType ctx home name typeArgs aliasTy =
    let modStr = ModuleName.toString home
        prefix = goModulePrefix home
        base = if null prefix
                  then name
                  else prefix ++ "_" ++ name

        -- v0.17 close — bootstrap-phase parity.  When the context
        -- requests args-suppressed output (legacy
        -- 'safeReturnTypeBootstrap' shape), emit @[]@ instead of the
        -- typed-args list so the rendered name is @Foo_R@ instead of
        -- @Foo_R[any]@.  Full-env callers keep their parametric
        -- instantiation.
        renderedArgs
            | mcDropTypeArgs ctx = []
            | otherwise          = map (mapSkyTypeToGo ctx . snd) typeArgs

        allAliases = mcRecordAliases ctx
        qualifiedCandidates =
            [ p ++ "_" ++ name
            | a <- Set.toList allAliases
            , '_' `elem` a
            , let p = reverse (drop 1 (dropWhile (/= '_') (reverse a)))
            , not (null p)
            ]
        candidates = if null prefix
                       then qualifiedCandidates ++ [name]
                       else base : qualifiedCandidates ++ [name]
        matches = [ c | c <- candidates, Set.member c allAliases ]

        qualHit = lookup (modStr, name) (mcQualRuntimeTyped ctx)
        runtimeHit = lookup name (mcRuntimeTypedMap ctx)

        isRecord = case aliasTy of
            T.Hoisted (T.TRecord _ _) -> True
            T.Filled  (T.TRecord _ _) -> True
            _ -> False

        isRuntimeOnly = name `elem` RuntimeMaps.runtimeOnlyTypes
        isKnownUnion =
            Set.member base (mcUnionNames ctx)
                || Set.member name (mcUnionNames ctx)

        -- v0.17 Phase F-3 cycle guard.  Re-entering the same alias
        -- base name on the recursion path collapses to GoAny so a
        -- self-referential alias chain terminates instead of black-
        -- holing.  Mirror of the legacy seen-set guard at
        -- 'Compile.hs:7994'.
        cycleHit = Set.member base (mcAliasSeen ctx)
        ctxNext  = ctx { mcAliasSeen = Set.insert base (mcAliasSeen ctx) }

        chainUnfolded
            | cycleHit  = Just GoAny
            | otherwise = case aliasTy of
                T.Hoisted inner -> Just (mapSkyTypeToGo ctxNext inner)
                T.Filled  inner -> Just (mapSkyTypeToGo ctxNext inner)
                _ -> Nothing
    in if cycleHit
        then GoAny
        else case matches of
            (m:_) -> GoNamed (m ++ "_R") renderedArgs
            _ -> case qualHit of
                Just q -> GoBare q
                Nothing -> case runtimeHit of
                    Just r -> GoBare r
                    Nothing
                        | isRecord -> GoNamed (base ++ "_R") renderedArgs
                        | isRuntimeOnly -> GoAny
                        | mcAssumeKnownUnion ctx -> GoNamed base []
                        | isKnownUnion -> GoNamed base []
                        | Just unfolded <- chainUnfolded -> unfolded
                        | otherwise -> GoAny


-- | v0.17 PR-22 S4 — detect FFI qualified-mangled flatten names.
--
-- Structural mirror of the legacy 'splitGoMangledQualified' at
-- 'Sky.Build.Compile.hs:7617'.  Names of the shape @Foo_at_pkg@
-- carry a Go-package-qualified import; both legacy renderers
-- short-circuit them to @any@ because:
--
--   * the bare 'Foo' is not a valid Go identifier (the runtime
--     never declares it under the flatten alias), AND
--   * the FFI surface routes the type through the runtime's
--     untyped channel anyway.
--
-- @Just (bare, suffix)@ means the name looks like
-- @\<bare>_at_\<suffix>@ where @bare@ starts uppercase + alnum and
-- @suffix@ is non-empty lowercase/digit/underscore.  We don't
-- consume the result (the legacy site only pattern-matches on
-- @Just _@); 'mapNamedType' fires its @GoAny@ branch on a hit.
splitGoMangledQualified :: String -> Maybe (String, String)
splitGoMangledQualified s = go s ""
  where
    go ('_' : 'a' : 't' : '_' : suffix) acc
        | not (null acc)
        , let bare = reverse acc
        , Char.isUpper (head bare)
        , all (\c -> Char.isAlpha c || Char.isDigit c) bare
        , not (null suffix)
        , all (\c -> Char.isLower c || Char.isDigit c || c == '_') suffix
        = Just (bare, suffix)
    go (c : rest) acc = go rest (c : acc)
    go [] _ = Nothing


-- | Map a named-type application (e.g. @List Int@, @Result Error Foo@,
-- user-defined @Std.Ui.Element msg@) to its Go shape.
--
-- Mirrors 'goNamedType' arm-for-arm so the differential parity
-- property holds.  Future commits ENRICH this mapper with
-- 'MappingContext' lookups (record-alias narrowing, runtime-typed
-- map for opaque FFI types) — each adds a NEW arm above the existing
-- fallthrough rather than changing existing arms.
mapNamedType
    :: MappingContext
    -> ModuleName.Canonical
    -> String
    -> [T.Type]
    -> GoType
mapNamedType ctx home name args
    -- v0.17 PR-22 S4 — early gate for FFI qualified-mangled flatten
    -- names (Compile.hs:17274).  Names of the shape @Foo_at_pkg@
    -- come from the FFI re-export of imported Go types.  Both
    -- legacy renderers short-circuit to @any@ before any other
    -- lookup because the bare 'Foo' is not a valid Go identifier
    -- (it's a flattened path) and the runtime doesn't carry a
    -- typed alias for it.
    | Just _ <- splitGoMangledQualified name = GoAny
    | otherwise =
    case (ModuleName.toString home, name) of
        -- Primitives — both qualified (Sky.Core.Basics) and bare paths
        ("Sky.Core.Basics", "Int")    -> GoBare "int"
        ("Sky.Core.Basics", "Float")  -> GoBare "float64"
        ("Sky.Core.Basics", "Bool")   -> GoBare "bool"
        ("Sky.Core.Basics", "String") -> GoBare "string"
        ("Sky.Core.Basics", "Char")   -> GoBare "rune"
        (_, "Int")    -> GoBare "int"
        (_, "Float")  -> GoBare "float64"
        (_, "Bool")   -> GoBare "bool"
        (_, "String") -> GoBare "string"
        (_, "Char")   -> GoBare "rune"
        (_, "Bytes")  -> GoBare "[]byte"

        -- Parameterised core types — always emit the generic form to
        -- match legacy 'typeToGo'.  Runtime Cmd/Sub/Tuple non-genericity
        -- (root cause F, H) is closed by C13-runtime / C6 — this
        -- commit only mirrors today's output.
        -- v0.17 PR-22 bulk root-cause fix — these three arms previously
        -- emitted `rt.SkyList[T]` / `rt.SkyDict[K, V]` / `rt.SkySet[T]`
        -- which DIVERGE from the legacy `solvedTypeToGoBounded`
        -- output.  The runtime exposes none of those generic
        -- aliases — `List` is implemented as Go's native slice
        -- (`[]T`), `Dict` as `map[string]V` (string keys only — the
        -- key arg is dropped at emission), `Set` as `map[any]bool`
        -- (args dropped entirely; runtime operations route through
        -- `rt.Set_insert` / etc.).  Pre-fix, bulk-replacing
        -- `solvedTypeToGo` with `solvedTypeToGoViaPipelineFlat`
        -- broke `examples/00-standard-libs` with `CODEGEN ERROR
        -- E4005: rt.SkyList undefined`.  The fix routes through
        -- the inner `renderGoType` so the element type recurses
        -- correctly under the same context (TVars→any policy
        -- preserved) and wraps in `GoBare` to match legacy
        -- byte-for-byte.
        (_, "List")   -> case args of
            [elem_] ->
                let innerStr = renderGoType (mcRenderEnv ctx) (mapSkyTypeToGo ctx elem_)
                in GoBare ("[]" ++ innerStr)
            _       -> GoBare "[]any"

        (_, "Maybe")  -> case args of
            [inner] -> GoNamed "rt.SkyMaybe" [mapSkyTypeToGo ctx inner]
            _       -> GoNamed "rt.SkyMaybe" [GoAny]

        (_, "Result") -> case args of
            [err, ok] ->
                GoNamed "rt.SkyResult"
                    [mapSkyTypeToGo ctx err, mapSkyTypeToGo ctx ok]
            _ -> GoNamed "rt.SkyResult" [GoAny, GoAny]

        (_, "Task") -> case args of
            [err, ok] ->
                GoNamed "rt.SkyTask"
                    [mapSkyTypeToGo ctx err, mapSkyTypeToGo ctx ok]
            _ -> GoNamed "rt.SkyTask" [GoAny, GoAny]

        -- Dict — string keys only at the emission layer (drops K).
        (_, "Dict") -> case args of
            [_k, v] ->
                let innerStr = renderGoType (mcRenderEnv ctx) (mapSkyTypeToGo ctx v)
                in GoBare ("map[string]" ++ innerStr)
            _ -> GoBare "map[string]any"

        -- Set — args dropped entirely (runtime uses untyped element).
        (_, "Set") -> GoBare "map[any]bool"

        -- v0.17 PR-18 Cause H5 parity — emit typed `rt.SkyCmd_T[Msg]`
        -- for concrete-arg Cmd, bare `rt.SkyCmd` for TVar-arg under
        -- default policy or 0/many-arg (matches legacy
        -- `safeReturnTypeFull`).  Both runtime aliases resolve to
        -- `cmdT`, so consumers accept either form.  Bare form for
        -- legacy TVar→bare degradation under default policy (TVar
        -- might not be in scope at the emission site, producing
        -- `undefined: T_msg` in Go).  Flat policy maps TVar→GoAny,
        -- so the typed form renders as `rt.SkyCmd_T[any]` — always
        -- valid.
        (_, "Cmd") | isCmdSubConcreteCaseFn ctx args
                   -> GoNamed "rt.SkyCmd_T" [mapSkyTypeToGo ctx (head args)]
                   | otherwise
                   -> GoNamed "rt.SkyCmd" []

        (_, "Sub") | isCmdSubConcreteCaseFn ctx args
                   -> GoNamed "rt.SkySub_T" [mapSkyTypeToGo ctx (head args)]
                   | otherwise
                   -> GoNamed "rt.SkySub" []

        -- Std.Html.Html — codegens non-generic (`= rt.SkyADT`), so
        -- the msg arg is dropped at emission time.  Same special-case
        -- as legacy 'goNamedType'.
        --
        -- v0.17 Phase F — typed @Std_Html_Html_T[arg]@ variant.
        -- Mirrors legacy 'typeStrWithAliasesRegBoundedScoped' at
        -- Compile.hs:8026: when 'mcHtmlTypedEmit' is True AND
        -- depModHint is Nothing (entry-module emission) AND arg is
        -- concrete (not a TVar), emit the typed sibling.  The
        -- runtime's @type Std_Html_Html_T[T any] = rt.SkyADT@ alias
        -- makes this transparent at runtime.  Dep-module emission
        -- stays bare because the entry-module's @Msg@ type isn't in
        -- scope in dep Go files.  Default off — only Phase F's
        -- typeStrWithAliasesRegBoundedScopedViaPipeline enables it.
        (_, "Html")
            | mcHtmlTypedEmit ctx
            , Nothing <- mcDepModHint ctx
            , [arg] <- args
            , (case arg of { T.TVar _ -> False; _ -> True })
            -> GoNamed "Std_Html_Html_T" [mapSkyTypeToGo ctx arg]
            | otherwise
            -> GoNamed "Std_Html_Html" []

        -- User-defined types: Module_Name or Module_Name[T1, T2]
        --
        -- v0.17 PR-22 bulk root-cause fix — env-aware classification.
        -- Mirrors legacy 'solvedTypeToGoBounded' user-ADT fallback at
        -- 'Sky.Build.Compile.hs:17350-17416' — lookup order:
        --
        --   1. 'mcQualRuntimeTyped' module-qualified override (S1).
        --   2. Record alias (in 'mcRecordAliases') → 'Foo_R[args]'
        --      (parametric Go-generic form, args preserved).
        --   3. 'mcRuntimeTypedMap' bare-name fallback (S1) —
        --      Decoder → @rt.SkyDecoder@, Error → @Sky_Core_Error_Error@.
        --   4. Known union (in 'mcUnionNames') → bare 'Foo' (NO args
        --      — Go-side declaration is 'type Foo = rt.SkyADT', so
        --      passing 'Foo[any]' breaks `go build` with "not a
        --      generic type").
        --   5. Unknown name → @any@ (v0.17 PR-22 S6 — matches legacy
        --      'solvedTypeToGoBounded' at Compile.hs:17368).  The
        --      previous "bare-name preserves Cause G" comment was
        --      stale: PR-21b's FFI Cause G satisfaction lives in the
        --      HM unifier ('Sky.Type.Unify.isFfiInterfacePair'), NOT
        --      in codegen.  By the time TType reaches this arm any
        --      FFI-satisfied type has already routed through @qualHit@
        --      / @runtimeHit@ / @runtimeOnly@ above; what's left
        --      really is unknown and leaking the bare token produces
        --      @undefined: T1@ / @undefined: T_e@ (TVar residues from
        --      σ_go).
        _ ->
            let prefix = goModulePrefix home
                base = if null prefix
                          then name
                          else prefix ++ "_" ++ name
                aliasName = base ++ "_R"
                modStr = ModuleName.toString home
                qualHit = lookup (modStr, name) (mcQualRuntimeTyped ctx)
                runtimeHit = lookup name (mcRuntimeTypedMap ctx)
                -- v0.17 Session 7 — registry-key shape widening.
                -- `collectRecordAliases` (Record.hs:391) stores BARE
                -- names; `depRecAliases` (Compile.hs:4503) wraps with
                -- module prefix but NO `_R` suffix.  The legacy
                -- predicate's two `_R`-suffixed probes both miss for
                -- every user/stdlib record alias → execution falls
                -- through to `runtimeHit` → bare-name kernel wins
                -- (`rt.SkyStore` for user State.Store, `rt.SkyRequest`
                -- for Sky.Http.Server.Request).  This is the Problem A
                -- root cause established empirically via
                -- docs/v0.17/session-4-commit-3-bisection-result.md.
                --
                -- The two NEW disjuncts match the registry's actual
                -- key shape (BARE).  Gated on prefix-nullity so the
                -- empty-home case still routes through aliasRecovery's
                -- last-segment walk above (avoiding cross-module
                -- silent wins when home attribution is lost).
                isRecordAlias = Set.member aliasName (mcRecordAliases ctx)
                              || Set.member (name ++ "_R") (mcRecordAliases ctx)
                              || (not (null prefix)
                                   && Set.member base (mcRecordAliases ctx))
                              || (null prefix
                                   && Set.member name (mcRecordAliases ctx))
                isKnownUnion = Set.member base (mcUnionNames ctx)
                             || Set.member name (mcUnionNames ctx)
                -- v0.17 PR-22 bulk root-cause fix — cross-module union
                -- recovery.  Mirrors legacy 'unionRecovery' at
                -- 'Sky.Build.Compile.hs:17394-17403': when 'home' is
                -- empty (TVar resolved to a Sky ADT via cross-decl
                -- constraint propagation but the module attribution
                -- lost), walk mcUnionNames for any 'Mod_Name' entry
                -- whose last underscore-segment equals the bare name.
                -- Empty-home + emit bare 'Foo' would render as
                -- 'undefined: Foo' in Go.
                unionRecovery =
                    if not (null prefix)
                       then Nothing
                       else case [ u | u <- Set.toList (mcUnionNames ctx)
                                     , '_' `elem` u
                                     , let lastSeg =
                                             reverse (takeWhile (/= '_')
                                                 (reverse u))
                                     , lastSeg == name
                                     ] of
                                [u] -> Just u
                                _   -> Nothing
                -- Record alias recovery for empty-home case — same
                -- shape as union recovery but on the record-alias
                -- registry (entries carry the '_R' suffix).
                aliasRecovery =
                    if not (null prefix)
                       then Nothing
                       else case [ a | a <- Set.toList (mcRecordAliases ctx)
                                     , '_' `elem` a
                                     , "_R" `isSuffixOf` a
                                     , let bareAlias = take (length a - 2) a
                                     , let lastSeg =
                                             reverse (takeWhile (/= '_')
                                                 (reverse bareAlias))
                                     , lastSeg == name
                                     ] of
                                [a] -> Just a
                                _   -> Nothing
            in case qualHit of
                Just q -> GoBare q
                Nothing -> case (aliasRecovery, isRecordAlias) of
                    (Just a, _) -> case args of
                        [] -> GoNamed a []
                        _  -> GoNamed a (map (mapSkyTypeToGo ctx) args)
                    (_, True) ->
                        -- v0.17 Session 7 — emit `<base>_R` when the
                        -- registry-bare match fires.  The 4-way
                        -- predicate above admits the BARE forms;
                        -- raliasName must construct the `_R`-suffixed
                        -- emit form for them too.
                        let raliasName =
                                if Set.member aliasName (mcRecordAliases ctx)
                                   then aliasName
                                   else if Set.member (name ++ "_R")
                                                       (mcRecordAliases ctx)
                                       then name ++ "_R"
                                   else if not (null prefix)
                                          && Set.member base
                                                  (mcRecordAliases ctx)
                                       then base ++ "_R"
                                   else if null prefix
                                          && Set.member name
                                                  (mcRecordAliases ctx)
                                       then name ++ "_R"
                                   else name ++ "_R"
                        in case args of
                            [] -> GoNamed raliasName []
                            _  -> GoNamed raliasName
                                    (map (mapSkyTypeToGo ctx) args)
                    _ -> case runtimeHit of
                        Just r -> GoBare r
                        Nothing -> case unionRecovery of
                            Just u -> GoNamed u []
                            Nothing
                                -- v0.17 PR-22 S4 — Route + other
                                -- 'runtimeOnly' types (in the registry
                                -- but with no corresponding Go type
                                -- declaration anywhere — only the FFI
                                -- runtime hand-codes them) fall to
                                -- @any@.  Without this gate the bare
                                -- @GoNamed "Route" []@ would render
                                -- as @Route@ in Go and break
                                -- @go build@ with @undefined: Route@.
                                | name `elem` RuntimeMaps.runtimeOnlyTypes
                                                -> GoAny
                                -- v0.17 close — bootstrap-phase parity.
                                -- When 'mcAssumeKnownUnion' is True the
                                -- caller knows every user-defined ADT
                                -- will get a @type Foo = rt.SkyADT@
                                -- declaration emitted later (the union
                                -- registry isn't built yet at this
                                -- phase).  Emit the bare base name to
                                -- match legacy 'safeReturnTypeBootstrap'
                                -- output (Compile.hs:7095-7097).
                                | mcAssumeKnownUnion ctx
                                                -> GoNamed base []
                                | isKnownUnion -> GoNamed base []
                                -- v0.17 PR-22 S6 — UNKNOWN-NAME COLLAPSE.
                                -- Mirrors legacy 'solvedTypeToGoBounded'
                                -- at Compile.hs:17368: when none of the
                                -- prior arms match (qualHit / runtimeHit
                                -- / record alias / runtimeOnly /
                                -- isKnownUnion / unionRecovery), the
                                -- name is genuinely unknown.  Emit
                                -- @any@ instead of leaking the bare
                                -- token.  Pre-S6 the pipeline emitted
                                -- @GoNamed base []@ which renders as
                                -- the literal identifier — fine when
                                -- @base@ is a declared Sky ADT in
                                -- main.go but produces @undefined: T1@
                                -- (or @T_e@, etc.) when @base@ is a
                                -- TVar residue from σ_go.  Adversary-
                                -- reviewed: this DOES NOT regress
                                -- PR-21b (FFI Cause G satisfaction lives
                                -- in the HM unifier, NOT codegen — by
                                -- the time TType reaches this arm, any
                                -- FFI-satisfied type has already routed
                                -- through @qualHit@/@runtimeHit@/
                                -- @runtimeOnly@ earlier).
                                | otherwise    -> GoAny


-- | Canonical "Sky type → Go type string" entry point — routes a
-- 'T.Type' through 'mapSkyTypeToGo' + 'renderGoType' using
-- 'defaultMappingContext'.  Equivalent to the legacy 'typeToGo'
-- (locked by the C2 parity test in
-- 'test/Sky/Build/GoTypeAdtSpec.hs').
--
-- New call sites SHOULD use this entry point.  Existing 'typeToGo'
-- callers can migrate freely — the C2 parity contract guarantees
-- byte-identical output.  Both surfaces coexist so 'typeToGo' can
-- serve as a parity oracle until C8+ widens 'MappingContext' with
-- env-derived alias data (at which point the new pipeline produces
-- richer output AND 'typeToGo' becomes the "minimal" fallback for
-- env-free contexts).
goTypeString :: T.Type -> String
goTypeString = renderGoType defaultRenderEnv . mapSkyTypeToGo defaultMappingContext


-- | v0.17 PR-22 bulk migration helper — does this Cmd/Sub argument
-- list warrant the typed @rt.SkyCmd_T[Msg]@ form?
--
-- True when there's exactly one arg AND (the arg is non-TVar OR the
-- mapping context uses flat policy mapping TVars to @GoAny@).  False
-- under the legacy "TVar→bare" degradation under default policy
-- (where emitting @T_msg@ outside the enclosing generic's scope
-- would produce @undefined: T_msg@ in Go).
isCmdSubConcreteCaseFn :: MappingContext -> [T.Type] -> Bool
isCmdSubConcreteCaseFn _   []        = False
isCmdSubConcreteCaseFn _   (_:_:_)   = False
isCmdSubConcreteCaseFn ctx [T.TVar _] = mcTVarsToAny ctx
isCmdSubConcreteCaseFn _   [_]       = True
