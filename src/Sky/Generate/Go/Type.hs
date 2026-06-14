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
    -- The legacy String renderers emit one of three forms depending
    -- on arity + a primitive-only whitelist on every element string:
    --   * 2-tuple, all elements pass the gate → @rt.T2[A, B]@
    --   * 2-tuple, gate fails               → @rt.SkyTuple2@
    --   * 3-tuple analogues                 → @rt.T3[...]@ / @rt.SkyTuple3@
    --   * arity ≥ 4                         → @rt.SkyTupleN@
    --
    -- The C1 'GoTuple' carries the typed element list explicitly, so
    -- the renderer no longer needs to re-tokenise.  Today the
    -- 'renderTupleGeneric' policy gate is False (matches legacy
    -- @typeToGo@ output — alias form for arity 2 / 3); once Cause-H
    -- Step 4 lands across all consumers, callers will construct
    -- 'GoTuple' only when they want the generic instantiation AND
    -- flip the policy gate to True simultaneously.  Until then the
    -- alias form ships verbatim.
    case args of
      [_, _]
        | renderTupleGeneric env ->
            "rt.T2[" ++ commaJoin (map (renderGoType env) args) ++ "]"
        | otherwise -> "rt.SkyTuple2"
      [_, _, _]
        | renderTupleGeneric env ->
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
        | isPrimitiveGoType s   = Just (GoBare s)
        | isTypeVarTok s        = Just (GoTypeVar s)
        | otherwise             = parseNameWithArgs s

    -- "func(A) B" — split on the matching ")" pair, parse the inner
    -- arg as A, parse the post-" " as B.
    parseFunc :: String -> Maybe GoType
    parseFunc s = do
        let afterFunc = drop 4 s  -- drop "func"; opener "(" remains
        (inner, post) <- splitMatching '(' ')' afterFunc
        argT <- parseGoType (trimWS inner)
        resT <- parseGoType (trimWS post)
        Just (GoFunc argT resT)

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
-- C2 ships the minimal shape — just 'mcRenderEnv'.  C8 onward widens
-- with the alias / union / runtime-typed maps that today's
-- @solvedTypeToGo@ reads from 'CodegenEnv' via 'getCgEnv'.  See
-- @docs/v0.17-fully-typed-codegen-v5-plan.md@ §Phase γ.
data MappingContext = MappingContext
    { mcRenderEnv :: RenderEnv
    }
    deriving (Eq, Show)


-- | Conservative default — empty mapping context with today's
-- 'defaultRenderEnv'.  Used by the differential parity test in
-- 'test/Sky/Build/GoTypeAdtSpec.hs'.
defaultMappingContext :: MappingContext
defaultMappingContext = MappingContext { mcRenderEnv = defaultRenderEnv }


-- | Map a canonical Sky type to its typed Go-type representation.
--
-- Structural mirror of 'typeToGo' — produces a 'GoType' whose
-- 'renderGoType' output equals 'typeToGo' on the same input, given
-- 'defaultMappingContext'.  C8+ MAY produce different output once
-- 'MappingContext' carries env-derived alias data — the legacy
-- 'typeToGo' has no equivalent path because it never had env access.
mapSkyTypeToGo :: MappingContext -> T.Type -> GoType
mapSkyTypeToGo ctx t = case t of
    T.TVar name ->
        GoTypeVar (goTypeParam name)

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

    T.TRecord fields Nothing ->
        mapRecordType ctx fields

    T.TRecord _ (Just _) ->
        GoRaw "any /* extensible record */"

    T.TType home name args ->
        mapNamedType ctx home name args

    T.TAlias _ _ _ (T.Hoisted inner) ->
        mapSkyTypeToGo ctx inner

    T.TAlias _ _ _ (T.Filled inner) ->
        mapSkyTypeToGo ctx inner


-- | Map a closed-record type to a 'GoStruct'.
mapRecordType :: MappingContext -> Map.Map String T.FieldType -> GoType
mapRecordType ctx fields =
    GoStruct (map (mapField ctx) (Map.toList fields))
  where
    mapField c (name, T.FieldType _ ty) =
        (capitalise name, mapSkyTypeToGo c ty)

    capitalise [] = []
    capitalise (c:cs) = toEnum (fromEnum c - 32) : cs


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
mapNamedType ctx home name args =
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
        (_, "List")   -> case args of
            [elem_] -> GoNamed "rt.SkyList" [mapSkyTypeToGo ctx elem_]
            _       -> GoNamed "rt.SkyList" [GoAny]

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

        (_, "Dict") -> case args of
            [k, v] ->
                GoNamed "rt.SkyDict"
                    [mapSkyTypeToGo ctx k, mapSkyTypeToGo ctx v]
            _ -> GoNamed "rt.SkyDict" [GoAny, GoAny]

        (_, "Set") -> case args of
            [elem_] -> GoNamed "rt.SkySet" [mapSkyTypeToGo ctx elem_]
            _       -> GoNamed "rt.SkySet" [GoAny]

        (_, "Cmd") -> case args of
            [msg] -> GoNamed "rt.SkyCmd" [mapSkyTypeToGo ctx msg]
            _     -> GoNamed "rt.SkyCmd" [GoAny]

        (_, "Sub") -> case args of
            [msg] -> GoNamed "rt.SkySub" [mapSkyTypeToGo ctx msg]
            _     -> GoNamed "rt.SkySub" [GoAny]

        -- Std.Html.Html — codegens non-generic (`= rt.SkyADT`), so
        -- the msg arg is dropped at emission time.  Same special-case
        -- as legacy 'goNamedType'.
        (_, "Html") -> GoNamed "Std_Html_Html" []

        -- User-defined types: Module_Name or Module_Name[T1, T2]
        _ ->
            let prefix = goModulePrefix home
                goName = prefix ++ "_" ++ name
            in case args of
                [] -> GoNamed goName []
                _  -> GoNamed goName (map (mapSkyTypeToGo ctx) args)


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
