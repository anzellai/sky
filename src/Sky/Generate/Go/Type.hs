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

import qualified Data.Map.Strict as Map
import qualified Sky.Type.Type as T
import qualified Sky.Sky.ModuleName as ModuleName


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

    (_, "Cmd") -> case args of
        [msg] -> "rt.SkyCmd[" ++ typeToGo msg ++ "]"
        _     -> "rt.SkyCmd[any]"

    (_, "Sub") -> case args of
        [msg] -> "rt.SkySub[" ++ typeToGo msg ++ "]"
        _     -> "rt.SkySub[any]"

    -- Std.Html.Html — the Layer-3 HTML ADT. htmlType in the
    -- constraint generator carries an empty home (so it unifies
    -- with a user `Html Msg` annotation regardless of import path),
    -- which would otherwise render unqualified and break go build
    -- ("undefined: Html"). Map it to the generated type here, the
    -- same way Cmd/Sub are. It codegens non-generic (`= rt.SkyADT`),
    -- so the `msg` arg is dropped.
    (_, "Html") -> "Std_Html_Html"

    -- User-defined types: Module_Name or Module_Name[T1, T2]
    _ ->
        let prefix = goModulePrefix home
            goName = prefix ++ "_" ++ name
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


-- | Module name to Go prefix: Sky.Core.List -> Sky_Core_List
goModulePrefix :: ModuleName.Canonical -> String
goModulePrefix home =
    map (\c -> if c == '.' then '_' else c) (ModuleName.toString home)


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
    | GoFunc GoType GoType        -- ^ @func(A) B@
    | GoNamed String [GoType]     -- ^ @Module_Name@ or @rt.SkyList[T]@
    | GoStruct [(String, GoType)] -- ^ anonymous @struct{ Name T; ... }@
    | GoTypeVar String            -- ^ @T1@, @T2@ — Go type-parameter ident
    | GoRaw String                -- ^ escape hatch — verbatim Go type string
    deriving (Eq, Show)


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
renderGoType env (GoNamed n args)   =
    case args of
        [] -> n
        _  -> n ++ "[" ++ commaJoin (map (renderGoType env) args) ++ "]"
renderGoType env (GoStruct fields)  =
    "struct{ " ++ joinWords (map (renderField env) fields) ++ " }"
  where
    renderField e (name, ty) = name ++ " " ++ renderGoType e ty ++ ";"
renderGoType _   (GoTypeVar n)      = n
renderGoType _   (GoRaw s)          = s
