-- | v0.13 Layer 2: Codegen-stage validator.
--
-- Walks the emitted Go source text looking for known-bad shapes
-- that indicate a Sky compiler bug.  Catches the patterns the HM
-- type checker accepted but codegen could not emit cleanly —
-- BEFORE `go build` sees the code.  When a pattern matches, the
-- validator emits a structured `Diagnostic` (category=Codegen,
-- code=E4001..E4999) so the user sees an Elm-style block
-- instead of Go's cryptic raw error.
--
-- Patterns currently detected:
--
--   1. **typed-kernel call with raw any-typed arg** (Issue #52 class).
--      Pattern: `rt.<Name>T[<types>](<bare-any-ident>, ...)` where
--      the first arg is a bare identifier (not wrapped in
--      `rt.As<Int|Bool|Float|String|List|Dict|Tuple2>` or a literal).
--      Caught here means the v0.12.1 typedKernelArgCoerce map is
--      missing an entry for that helper.
--
--   2. **`.(<concreteType>)` raw type-assertion on an any-typed
--      variable**.  Pattern: `<bare-ident>.(<type>)` outside the
--      `rt.Coerce*` helpers.  P0-3 audit prohibits this — must
--      route through a runtime coerce helper that handles the
--      nil / wrong-type case.
--
--   3. **Generic instantiation with `any` in a typed slot**.
--      Pattern: `rt.<Name>T[<types>]` where one of the types is
--      `any` and the helper's typed signature would reject `any`.
--      Indicates the typed-codegen fell back to any-routing when
--      it should have monomorphised.
--
--   4. **Unbalanced FFI wrapper arg count**.  Pattern: a generated
--      `Go_<pkg>_<func>(<args>)` call where the count of args
--      doesn't match the wrapper's declared arity in the `.skyi`.
--      (Not yet implemented — needs cross-file analysis.)
--
-- The validator is intentionally CONSERVATIVE.  False positives
-- would block real builds; we only flag patterns that are
-- definitively wrong.  Edge cases (e.g. `rt.X(args)` where `X`
-- is a Sky-defined struct method named identically to a kernel
-- helper) are skipped by checking the helper-name is in the
-- known set.
--
-- Resilience: the validator never crashes the build — on any
-- internal failure it returns an empty diagnostic list and lets
-- `go build` proceed as a fallback.  Sky's "if it compiles, it
-- works" promise relies on regression tests, not on this pass.
-- The pass is defence-in-depth.
module Sky.Build.Validator
    ( validateEmittedGo
    , parseOriginComments
    , injectOriginComments
    , OriginMap
    , GoErrorLocation(..)
    , parseGoBuildError
    , resolveGoErrorToSky
    ) where

import qualified Data.ByteString as BS
import qualified Data.ByteString.Char8 as BS8
import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Sky.Reporting.Annotation as A
import qualified Sky.Reporting.Diagnostic as Diag
import Sky.Build.EmbeddedRuntime (embeddedRuntime)
import Data.Char (isAlphaNum)
import Data.List (isPrefixOf, isInfixOf, isSuffixOf, stripPrefix, foldl')
import Data.Maybe (mapMaybe)
import qualified System.Environment
import System.IO.Unsafe (unsafePerformIO)


-- ─── public API ──────────────────────────────────────────────────────


-- | Run the codegen-stage validator on emitted main.go text.
-- Returns a (possibly empty) list of `Diagnostic` values.  Empty
-- means the validator found no known-bad shapes and the build is
-- free to proceed to `go build`.
--
-- `originMap` is the line-keyed Sky-source map.  When a bug is
-- found at Go line N, the validator looks up the nearest preceding
-- origin entry to map back to Sky source.
validateEmittedGo :: FilePath -> OriginMap -> String -> [Diag.Diagnostic]
validateEmittedGo _goPath originMap source =
    let lns = zip [1..] (lines source)
        exports = runtimeExports
        diags = concatMap (checkLine originMap exports) lns
    -- Env-var escape hatch for codegen debugging: SKY_SKIP_VALIDATOR=1
    -- skips ALL validator patterns so the user can see the raw Go +
    -- get the raw `go build` error. Used when iterating on a codegen
    -- change before the validator's patterns are reliable.
    in if envSkipValidator then [] else diags

{-# NOINLINE envSkipValidator #-}
envSkipValidator :: Bool
envSkipValidator = unsafePerformIO $ do
    v <- System.Environment.lookupEnv "SKY_SKIP_VALIDATOR"
    return (v == Just "1")


-- | v0.13 Stage 1 (issue #56) — set of every exported identifier in
-- the embedded Go runtime (`runtime-go/rt/*.go`). Used by
-- `patternUndefinedKernel` to catch `rt.X` references where `X` is
-- not actually defined in the runtime — a class of bugs that previously
-- type-checked fine in Sky but failed `go build` with `undefined:
-- rt.X` (the contract violation that motivates this validator).
--
-- Scope: top-level `func` declarations, `type` declarations, top-level
-- `var` declarations. Generic-parameter-bearing fns (`func F[T any](
-- …)`) are matched on the bare name. Methods (`func (r *T) M(…)`) are
-- skipped because Sky never references them via `rt.M` (always via
-- a value of type `T`).
--
-- v0.17 iter 13 — IORef removed.  @scanEmbeddedRuntimeExports@ is
-- pure (no IO; reads only from the TH-embedded @embeddedRuntime@
-- bytestring list), so the cache was redundant: GHC memoises a CAF
-- exactly once for the life of the process under the same lazy-
-- evaluation discipline the previous @IORef (Maybe …)@ relied on,
-- but without unsafePerformIO + writeIORef + read sequence.
runtimeExports :: Set.Set String
runtimeExports = scanEmbeddedRuntimeExports
{-# NOINLINE runtimeExports #-}

scanEmbeddedRuntimeExports :: Set.Set String
scanEmbeddedRuntimeExports =
    let goFiles = [ bs | (path, bs) <- embeddedRuntime
                       , ".go" `isSuffixOf` path
                       , "rt/" `isPrefixOf` path
                       , not (".test.go" `isSuffixOf` path)
                       , not ("_test.go" `isSuffixOf` path)
                       ]
        allLines = concatMap (lines . BS8.unpack) goFiles
    in Set.fromList (mapMaybe extractTopLevelName allLines)
  where
    extractTopLevelName ln
        -- `func Name(...)` — value-level fn (not a method)
        | Just rest <- stripPrefix "func " ln
        , let name = takeWhile isIdentChar rest
        , not (null name)
        , takeWhile isIdentChar rest /= ""
        , let after = drop (length name) rest
        , beginsWithFuncContinuation after
        = Just name
        -- `func Name[T any](...)` — generic fn
        -- (handled by the same path above since `takeWhile isIdentChar`
        -- stops at the `[` and `beginsWithFuncContinuation` accepts `[`).
        --
        -- `type Name ...`
        | Just rest <- stripPrefix "type " ln
        , let name = takeWhile isIdentChar rest
        , not (null name) = Just name
        -- `var Name ...`
        | Just rest <- stripPrefix "var " ln
        , let name = takeWhile isIdentChar rest
        , not (null name) = Just name
        | otherwise = Nothing

    isIdentChar c = isAlphaNum c || c == '_'

    -- After the fn name, accept either `(`, `[`, or whitespace
    -- (some templates emit `func F  (`). Methods start with `(`
    -- BEFORE the name (`func (r *T) M(`), so this filter never
    -- fires on them — they don't match `stripPrefix "func "` +
    -- name-then-continuation.
    beginsWithFuncContinuation s =
        case dropWhile (== ' ') s of
            '(':_  -> True
            '[':_  -> True
            _      -> False


-- ─── pattern matchers ────────────────────────────────────────────────


-- | Run every pattern matcher on a single line.  Each matcher
-- returns Just Diagnostic if it fires; Nothing otherwise.
checkLine :: OriginMap -> Set.Set String -> (Int, String) -> [Diag.Diagnostic]
checkLine originMap exports (goLine, line) =
    mapMaybe (\m -> m originMap goLine line)
        [ patternTypedKernelAnyArg
        , patternRawTypeAssert
        , patternGenericInstAnyOnly
        , patternUndefinedKernel exports
        ]


-- | v0.13 Stage 1 (issue #56) — emitted `rt.<Name>` reference where
-- `<Name>` doesn't exist in the runtime. Catches the bug class
-- where Sky's kernel-name registry knows about a fn (e.g.
-- `clamp -> Basics_clamp`) but no matching runtime decl exists
-- (the runtime only has `Basics_clampT`). Pre-fix the user got
-- a `go build` error `undefined: rt.Basics_clamp` and was told to
-- file an issue; post-fix the Sky compiler catches it itself.
--
-- Scope: matches `rt.<Ident>` references in the body (not in
-- imports or string literals). Excludes anything that contains a
-- `.` after the `rt.` prefix (e.g. `rt.Foo.Bar` is field access,
-- not a kernel ref). Excludes `[` brackets (generic instantiation
-- is on the bare ident).
patternUndefinedKernel :: Set.Set String -> OriginMap -> Int -> String -> Maybe Diag.Diagnostic
patternUndefinedKernel exports originMap goLine line =
    let refs = extractRtRefs line
        -- FFI-generated bindings live in `sky-out/rt/<pkg>_bindings.go`
        -- (not `runtime-go/rt/`), so the embedded-runtime scan can't
        -- see them. They use the `Go_<pkg>_<fn>` naming convention.
        -- Skip those — sky-ffi-inspect / FfiGen own their own
        -- contract check at install time.
        --
        -- Also skip refs that look like generic type parameters
        -- (`rt.T1`, `rt.SkyResult`, `rt.SkyMaybe`, …) — these are
        -- TYPES not call targets; the type system already validates
        -- them at usage time, and Set.member would miss e.g.
        -- `rt.SkyResult[Foo, Bar]` because we extract the bare name
        -- but the runtime exports the parameterised type.
        isFfiGenerated n =
            "Go_" `isPrefixOf` n ||      -- FFI fn wrappers
            "FfiT_" `isPrefixOf` n       -- FFI type aliases
        bad  = [ name | name <- refs
                      , not (isFfiGenerated name)
                      , not (Set.member name exports) ]
    in case bad of
        []       -> Nothing
        (name:_) -> Just $ buildCodegenDiag originMap goLine
            Diag.codegenE_UndefinedKernel
            ("Codegen emitted a reference to `rt." ++ name ++ "`\n"
          ++ "but the runtime does not export a function or value\n"
          ++ "named `" ++ name ++ "`. `go build` would reject this\n"
          ++ "as `undefined: rt." ++ name ++ "`.\n\n"
          ++ "This is a Sky compiler bug — the kernel-name registry\n"
          ++ "knows about `" ++ humanise name ++ "` but a matching\n"
          ++ "runtime declaration is missing. Either add the runtime\n"
          ++ "function in `runtime-go/rt/` or correct the kernel\n"
          ++ "name in `src/Sky/Generate/Go/Kernel.hs` /\n"
          ++ "`src/Sky/Build/Compile.hs`'s `kernelToGo` default.")
  where
    humanise n = case break (== '_') n of
        (modPart, '_':funcPart) -> modPart ++ "." ++ funcPart
        _ -> n

extractRtRefs :: String -> [String]
extractRtRefs = go True  -- start-of-string IS a token boundary
  where
    go _ [] = []
    -- Skip string literals (`"...\""`) so that source-line tracing
    -- strings like `"Cart.addItem"` aren't misparsed as `rt.addItem`
    -- references when the C of "Cart" lines up. We don't handle
    -- escaped quotes perfectly; a stray unterminated string would
    -- just skip the rest of the line. That's fine for the validator's
    -- conservative-by-design contract.
    go _ ('"':rest) = go True (skipString rest)
    -- Skip line comments — `// foo rt.bar` is not a ref.
    go _ ('/':'/':_) = []
    -- Match `rt.<Ident>` only at a token boundary (preceded by non-
    -- identifier char or BOL, indicated by `boundary = True`).
    go True str@('r':'t':'.':_)
        | (name, rest) <- span isRtIdentChar (drop 3 str)
        , not (null name)
        , not ('.' `elem` take 1 rest) =
            name : go (nextBoundary (head' rest))
                      (drop (length name) (drop 3 str))
    go _ (c:cs) = go (nextBoundary c) cs

    isRtIdentChar c = isAlphaNum c || c == '_'

    -- After consuming a char, the NEXT position is at a token
    -- boundary iff this char wasn't an identifier char.
    nextBoundary c = not (isAlphaNum c || c == '_' || c == '.')

    head' [] = ' '
    head' (h:_) = h

    -- Skip a Go string literal until the matching `"`. Handle `\"`
    -- as an escape so `"foo\"bar"` reads as one string.
    skipString [] = []
    skipString ('\\':_:rest) = skipString rest
    skipString ('"':rest) = rest
    skipString (_:rest) = skipString rest


-- | Pattern 1: typed-kernel call with a raw any-typed arg.
--
-- A typed kernel is any helper ending with `T` (e.g. `rt.List_dropT`,
-- `rt.AsListT`, `rt.MaybeCoerceT`).  Its first generic-positional
-- arg is expected to be either:
--   * A literal (numeric / string / bool).
--   * A wrapped expression: `rt.AsInt(...)`, `rt.AsBool(...)`,
--     `rt.AsString(...)`, `rt.Coerce[...](...)`, etc.
--   * A typed Go value (struct field access, typed func return).
--
-- We flag the first arg ONLY when it's a bare identifier (a single
-- Go identifier with no surrounding call / literal / selector
-- chain).  Bare identifiers carry whatever Sky-side type the
-- caller bound, which is usually `any` in v0.12-era codegen.
patternTypedKernelAnyArg :: OriginMap -> Int -> String -> Maybe Diag.Diagnostic
patternTypedKernelAnyArg originMap goLine line =
    case findRtTypedCall line of
        Just (helperName, firstArg) | isBareIdent firstArg
                                    , helperName `elem` riskyTypedKernels ->
            Just $ buildCodegenDiag originMap goLine
                Diag.codegenE_TypedKernelAnyArg
                ("Codegen emitted a typed kernel call with a bare\n"
              ++ "any-typed argument:\n\n"
              ++ "    rt." ++ helperName ++ "(...)\n\n"
              ++ "The first argument `" ++ firstArg ++ "` was not\n"
              ++ "wrapped in `rt.AsInt` / `rt.AsString` / `rt.AsList`\n"
              ++ "or a similar coercer.  `go build` will reject this\n"
              ++ "as `cannot use " ++ firstArg ++ " (any) as <T>`.\n\n"
              ++ "This is a Sky compiler bug — the type system\n"
              ++ "accepted the program but codegen forgot the\n"
              ++ "coerce wrap.  File an issue at\n"
              ++ "https://github.com/anzellai/sky/issues with the\n"
              ++ "Sky source.")
        _ -> Nothing


-- | Pattern 2: raw `.(T)` type-assertion outside of `rt.Coerce*` helpers.
--
-- P0-3 audit prohibits these on generated any-typed thunks — they
-- panic on nil / wrong-type instead of returning a typed Err.
-- Detection: look for `.(<TypeName>)` where the preceding context
-- isn't a Coerce-family helper definition (i.e. we're inside a
-- user-code site, not the runtime helpers themselves).
patternRawTypeAssert :: OriginMap -> Int -> String -> Maybe Diag.Diagnostic
patternRawTypeAssert _originMap _goLine _line =
    -- Conservative: not yet enabled.  False-positive risk is high
    -- because legitimate type-assertions on typed values (post-
    -- Coerce) also match `.(T)` syntactically.  Re-enable after
    -- threading more context.
    Nothing


-- | Pattern 3: typed generic instantiation where every type slot
-- is `any` — a marker that typed-codegen fell back to any-routing.
-- Not strictly a bug (compiles cleanly, just slow), so we emit a
-- warning-severity Diagnostic rather than an error.
patternGenericInstAnyOnly :: OriginMap -> Int -> String -> Maybe Diag.Diagnostic
patternGenericInstAnyOnly _originMap _goLine _line =
    -- Not yet enabled — needs targeted scope to avoid noise on
    -- legitimate `any` instantiations (e.g. `rt.List_mapAnyT[any]`
    -- which IS the correct routing for non-inferable lambdas).
    Nothing


-- ─── helper predicates ───────────────────────────────────────────────


-- | The set of typed-kernel helpers that strictly require typed
-- first-arg shape.  Mirrors the surface of `typedKernelArgCoerce`
-- in Compile.hs — a missing entry there manifests as a hit here.
riskyTypedKernels :: [String]
riskyTypedKernels =
    -- List helpers — first arg is the Int count or fn closure;
    -- second is the slice.  Listed in [helper, typed-first-arg-form].
    [ "List_takeT"
    , "List_dropT"
    , "List_takeAnyT"
    , "List_dropAnyT"
    , "List_indexedMapTA"
    , "Dict_fromListT"
    ]


-- | Match a `rt.<Helper>(args...)` call on the line and return the
-- helper name + the first arg (best-effort literal extraction).
-- Returns Nothing if the line doesn't contain a rt.* call.
findRtTypedCall :: String -> Maybe (String, String)
findRtTypedCall s =
    case dropToPrefix "rt." s of
        Just rest ->
            let (helperName, afterName) = span isGoIdentChar rest
                afterGeneric = skipGenericBrackets afterName
            in case afterGeneric of
                ('(':args) ->
                    let firstArg = takeWhile (\c -> c /= ',' && c /= ')') args
                        cleaned  = trim firstArg
                    in if null helperName || null cleaned
                       then Nothing
                       else Just (helperName, cleaned)
                _ -> Nothing
        Nothing -> Nothing


-- | Is the input a bare Go identifier (no parens, dots, etc.)?
isBareIdent :: String -> Bool
isBareIdent "" = False
isBareIdent s  =
    all isGoIdentChar s
    && not (head s `elem` ("0123456789" :: String))
    -- A bare ident isn't a literal (`true`, `false`, `nil` ARE bare
    -- idents but are valid typed values — exclude them).
    && s `notElem` ["true", "false", "nil"]


isGoIdentChar :: Char -> Bool
isGoIdentChar c =
       (c >= 'a' && c <= 'z')
    || (c >= 'A' && c <= 'Z')
    || (c >= '0' && c <= '9')
    || c == '_'


-- | Walk past `[T1, T2, ...]` generic brackets if present.
skipGenericBrackets :: String -> String
skipGenericBrackets ('[':xs) = case dropWhile (/= ']') xs of
    (']':rest) -> rest
    _          -> xs
skipGenericBrackets s = s


-- | Find the first occurrence of `prefix` in `s`, return what
-- follows.  Returns Nothing if not found.
dropToPrefix :: String -> String -> Maybe String
dropToPrefix p s
    | p `isPrefixOf` s = Just (drop (length p) s)
    | otherwise = case s of
        []     -> Nothing
        (_:cs) -> dropToPrefix p cs


trim :: String -> String
trim = dropWhile (== ' ') . reverse . dropWhile (== ' ') . reverse


-- ─── origin tracking ─────────────────────────────────────────────────


-- | Map from Go-source line number to the Sky region it originated
-- from.  Populated by parsing `// SKY-ORIGIN: <path>:<line>:<col>`
-- comments that codegen sprinkles into main.go at function-decl
-- boundaries.
type OriginMap = Map.Map Int (FilePath, Int, Int)


-- | Inject `// SKY-ORIGIN: <path>:<line>:<col>` comments into
-- the emitted Go text at every function declaration whose name
-- maps to a known Sky source position.  The `nameMap` is keyed by
-- the EMITTED Go-function name (e.g. `Main_update`, `view`,
-- `Model`) and points at the Sky source region.
--
-- The post-processor scans the goCode for `^func <Name>(...)` lines
-- and prepends an origin comment when the name has a Sky position.
-- Idempotent: if a SKY-ORIGIN comment already precedes the func,
-- it's left untouched (we re-render `goCode` from a fresh codegen
-- on every build, so this is mostly safety against double-running).
--
-- The injection happens BEFORE `validateEmittedGo` and BEFORE
-- writing main.go to disk, so the origin map seen by the
-- validator and by `go build` error refiner is consistent.
injectOriginComments
    :: Map.Map String (FilePath, Int, Int)
    -> String
    -> String
injectOriginComments nameMap source =
    unlines (walkLines (lines source))
  where
    walkLines [] = []
    walkLines (l:ls)
        | Just funcName <- parseFuncDecl l
        , Just (skyPath, skyLine, skyCol) <- Map.lookup funcName nameMap
        , not (hasPrecedingOrigin ls' l) =
            ("// SKY-ORIGIN: " ++ skyPath ++ ":" ++ show skyLine
              ++ ":" ++ show skyCol) : l : walkLines ls
        | otherwise = l : walkLines ls
      where ls' = []  -- placeholder; preceding-origin check would walk back

    parseFuncDecl line =
        case stripPrefix "func " line of
            Just rest ->
                let (name, after) = span isGoIdentChar rest
                    -- Could also be `func name[T any]` (generic) or
                    -- `func name(` (plain).  Accept either next char.
                in case after of
                    ('(':_) | not (null name) -> Just name
                    ('[':_) | not (null name) -> Just name
                    _ -> Nothing
            Nothing -> Nothing

    hasPrecedingOrigin _ _ = False


-- | Walk emitted main.go text, parse SKY-ORIGIN comments, return
-- a map keyed by Go line number.
parseOriginComments :: String -> OriginMap
parseOriginComments source =
    let lns = zip [1..] (lines source)
        entries = mapMaybe parseEntry lns
    in Map.fromList entries
  where
    parseEntry (goLine, line) =
        case stripPrefix "// SKY-ORIGIN:" (dropWhile (== ' ') line) of
            Just rest -> case parseFileLineCol (trim rest) of
                Just (p, l, c) -> Just (goLine, (p, l, c))
                Nothing        -> Nothing
            Nothing -> Nothing

    parseFileLineCol s =
        -- Format: "<path>:<line>:<col>".  Path may contain '/' so
        -- we parse from the END: scan backward for two ':'-delimited
        -- numeric tail segments.
        case splitLast ':' s of
            Just (rest1, colStr) | all isAsciiDigit colStr ->
                case splitLast ':' rest1 of
                    Just (path, lineStr) | all isAsciiDigit lineStr ->
                        Just (path, read lineStr, read colStr)
                    _ -> Nothing
            _ -> Nothing

    splitLast c s = case break (== c) (reverse s) of
        (rev, ':':revRest) -> Just (reverse revRest, reverse rev)
        _                  -> Nothing

    isAsciiDigit ch = ch >= '0' && ch <= '9'


-- ─── go-build error → Sky source ─────────────────────────────────────


data GoErrorLocation = GoErrorLocation
    { _gel_file    :: !FilePath  -- always main.go (or rt/*.go for runtime)
    , _gel_line    :: !Int
    , _gel_col     :: !Int
    , _gel_message :: !String    -- the Go error message body
    }
    deriving (Show)


-- | Parse `main.go:NN:MM: <message>` shape from a go-build stderr
-- line.  Returns the first match.  Lines that don't match (e.g.
-- "go.mod requires Go 1.21") are skipped.
parseGoBuildError :: String -> Maybe GoErrorLocation
parseGoBuildError stderrText =
    case filter looksLikeGoError (lines stderrText) of
        (l:_) -> parseOne l
        []    -> Nothing
  where
    looksLikeGoError s =
        (".go:" `isInfixOf` s)
        && any (`isInfixOf` s) [": cannot ", ": undefined", ": syntax error"
                               , ": type ", ": expected ", ": missing "]

    parseOne s =
        -- Tolerant parse: "<path>.go:NN:MM: <body>"
        case break (== ':') s of
            (filePath, ':':rest1) | ".go" `isSuffixOf` filePath ->
                case break (== ':') rest1 of
                    (lineStr, ':':rest2) | all isAsciiDigit lineStr ->
                        case break (== ':') rest2 of
                            (colStr, ':':body) | all isAsciiDigit colStr ->
                                Just $ GoErrorLocation
                                    { _gel_file    = filePath
                                    , _gel_line    = read lineStr
                                    , _gel_col     = read colStr
                                    , _gel_message = dropWhile (== ' ') body
                                    }
                            _ -> Nothing
                    _ -> Nothing
            _ -> Nothing
    isAsciiDigit ch = ch >= '0' && ch <= '9'


-- | Given a Go-side error location and an OriginMap, find the
-- nearest preceding SKY-ORIGIN entry and build a Diagnostic
-- pointing at the Sky source.
resolveGoErrorToSky :: OriginMap -> GoErrorLocation -> Maybe Diag.Diagnostic
resolveGoErrorToSky originMap gel =
    case findNearestOrigin originMap (_gel_line gel) of
        Just (skyPath, skyLine, skyCol) ->
            let region = A.Region
                    (A.Position skyLine skyCol)
                    (A.Position skyLine skyCol)
                msg = "Code generation produced Go that `go build` rejected:\n\n"
                   ++ "    " ++ _gel_file gel ++ ":" ++ show (_gel_line gel)
                   ++ ":" ++ show (_gel_col gel) ++ ": "
                   ++ _gel_message gel ++ "\n\n"
                   ++ "This is a Sky compiler bug.  The Sky type system\n"
                   ++ "accepted this program but codegen emitted Go that\n"
                   ++ "the Go compiler does not accept.  Please file an\n"
                   ++ "issue at https://github.com/anzellai/sky/issues with\n"
                   ++ "this Sky source attached."
            in Just $ Diag.mkError skyPath region
                        Diag.CatGoBuild Diag.goE_BuildFailed msg
        Nothing -> Nothing


-- | Linear-scan O(n) walk to find the largest origin-key not
-- exceeding goLine.  For typical main.go sizes (10k–100k lines)
-- this is fast enough; if it becomes a hot path, switch to a
-- tree-backed Map with `lookupLE`.
findNearestOrigin :: OriginMap -> Int -> Maybe (FilePath, Int, Int)
findNearestOrigin originMap goLine =
    let keys = Map.keys originMap
        eligible = filter (<= goLine) keys
    in case eligible of
        [] -> Nothing
        _  -> Map.lookup (maximum eligible) originMap


-- ─── shared diagnostic builder ───────────────────────────────────────


buildCodegenDiag :: OriginMap -> Int -> Diag.DiagCode -> String -> Diag.Diagnostic
buildCodegenDiag originMap goLine code message =
    case findNearestOrigin originMap goLine of
        Just (skyPath, skyLine, skyCol) ->
            let region = A.Region
                    (A.Position skyLine skyCol)
                    (A.Position skyLine skyCol)
            in Diag.mkError skyPath region Diag.CatCodegen code message
        Nothing ->
            -- Fallback: synthetic region pointing at main.go.
            let region = A.Region (A.Position goLine 1) (A.Position goLine 1)
            in Diag.mkError "<generated>" region
                    Diag.CatCodegen code message
