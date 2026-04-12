{-# LANGUAGE OverloadedStrings #-}
-- | FFI binding generator — full auto, no hand-written wrappers required.
--
-- Architecture (mirrors the self-hosted Sky compiler's approach but adapted
-- to our Haskell host):
--
-- 1. sky-ffi-inspect emits a JSON report of the package: every exported
--    top-level function with its param and result types as fully-qualified
--    Go type strings (e.g. "github.com/stripe/stripe-go/v82.CheckoutSession",
--    "time.Time", "*net/http.Client", "[]io.Reader").
--
-- 2. This Haskell module scans those type strings to discover *every* Go
--    package that must be imported in the generated wrapper — including
--    transitive stdlib (time, io, net/http, …), sibling packages of the
--    requested package (stripe-go/v82 when binding checkout/session), and
--    the package itself. For each discovered package we pick a safe Go
--    alias derived from its path, and import them all.
--
-- 3. Every fully-qualified type reference in a signature is rewritten to
--    use the computed alias, so `github.com/stripe/stripe-go/v82.Checkout`
--    becomes `v82_stripe_go.Checkout` (or whatever alias we derived).
--
-- 4. The only things still skipped are generic type parameters (e.g.
--    `Fetch[T]`) — they're fundamentally not realisable at FFI time
--    without monomorphisation, matching the self-hosted compiler.
--
-- 5. Sky records-with-methods give us a clean bridge for opaque Go types:
--    a Go struct `pkg.Foo` becomes a Sky record type whose methods are the
--    FFI bindings. Field accessors `fooField` and setters `fooSetField`
--    are auto-generated alongside the function bindings so Sky code can
--    interact with opaque Go values idiomatically.
--
-- 6. Task-effect boundary + panic recovery are enforced by the runtime
--    (see rt.invokeFfi): every binding is registered as effect-unknown,
--    callable via Ffi.callTask, with `defer/recover` on every call.
module Sky.Build.FfiGen
    ( generateBindings
    , runInspector
    ) where

import qualified Data.Aeson as A
import qualified Data.ByteString.Lazy as BL
import Data.Char (isAlphaNum, isLower, isUpper)
import Data.List (foldl', intercalate, nub, sortOn)
import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Data.Text as T
import qualified Data.Text.Encoding as TE
import System.Directory (createDirectoryIfMissing)
import System.FilePath ((</>))
import System.Process (readProcessWithExitCode)


-- | Information extracted about one Go function
data FnInfo = FnInfo
    { _fnName     :: String
    , _fnParams   :: [(String, String)]  -- (name, goType)
    , _fnResults  :: [(String, String)]  -- (name, goType)
    , _fnVariadic :: Bool                -- last param is ...T
    , _fnEffect   :: String              -- pure / fallible / effectful
    }
    deriving (Show)


data PkgInfo = PkgInfo
    { _pkgPath   :: String
    , _pkgName   :: String
    , _pkgFns    :: [FnInfo]
    , _pkgErrors :: [String]
    }
    deriving (Show)


instance A.FromJSON FnInfo where
    parseJSON = A.withObject "FnInfo" $ \o -> FnInfo
        <$> o A..: "name"
        <*> (o A..: "params" >>= mapM parseParam)
        <*> (o A..: "results" >>= mapM parseParam)
        <*> o A..:? "variadic" A..!= False
        <*> o A..: "effect"
      where
        parseParam = A.withObject "param" $ \o -> do
            n <- o A..:? "name" A..!= ""
            t <- o A..: "type"
            return (n, t)


instance A.FromJSON PkgInfo where
    parseJSON = A.withObject "PkgInfo" $ \o -> PkgInfo
        <$> o A..: "pkg"
        <*> o A..:? "name" A..!= ""
        <*> o A..:? "functions" A..!= []
        <*> o A..:? "errors" A..!= []


runInspector :: String -> IO (Either String PkgInfo)
runInspector pkgPath = do
    let cmd = "cd sky-out && ../bin/sky-ffi-inspect " ++ pkgPath
    (_, out, err) <- readProcessWithExitCode "sh" ["-c", cmd] ""
    if null out
        then return (Left $ "sky-ffi-inspect: empty output; stderr: " ++ err)
        else case A.eitherDecode (BL.fromStrict (TE.encodeUtf8 (T.pack out))) of
            Left e  -> return (Left $ "sky-ffi-inspect: json: " ++ e)
            Right p -> return (Right p)


generateBindings :: PkgInfo -> IO [String]
generateBindings pkg = do
    createDirectoryIfMissing True "ffi"
    let slug = slugify (_pkgName pkg)
        goFile = "ffi" </> (slug ++ "_bindings.go")
        skyiFile = "ffi" </> (slug ++ ".skyi")
        names = map (\fn -> _pkgName pkg ++ "." ++ _fnName fn) (_pkgFns pkg)
    writeFile goFile (emitGoFile pkg)
    writeFile skyiFile (emitSkyi pkg)
    return names


-- ══════════════════════════════════════════════════════════════════════════
-- Package discovery and alias resolution
--
-- We scan every type string referenced by every function in the package
-- and discover all Go packages that must be imported. Each gets a safe
-- Go identifier alias derived from its path.
-- ══════════════════════════════════════════════════════════════════════════

-- | A table: Go package path → alias used in the emitted wrapper.
-- The requested package itself is bound to the alias "pkg" (matching
-- `pkg "..."` in the import block). Every other package gets an alias
-- derived from its last path segment, de-conflicted if necessary.
type AliasTable = Map.Map String String

buildAliasTable :: PkgInfo -> AliasTable
buildAliasTable pkg =
    let self = _pkgPath pkg
        allPaths = discoverPackagePaths pkg
        others = filter (/= self) allPaths
        -- Reserved aliases: pkg (self), fmt (always imported).
        reserved = Set.fromList ["pkg", "fmt"]
        assigned = foldl' assign (Map.singleton self "pkg", reserved) others
    in fst assigned
  where
    assign (m, used) path =
        let base = pathToAlias path
            final = uniqueAlias used base 0
        in (Map.insert path final m, Set.insert final used)

    uniqueAlias used base n =
        let candidate = if n == 0 then base else base ++ "_" ++ show n
        in if Set.member candidate used
            then uniqueAlias used base (n + 1)
            else candidate


-- | Last path segment, sanitised to a valid Go identifier.
-- "github.com/stripe/stripe-go/v82"               → "stripe_go_v82"
-- "github.com/stripe/stripe-go/v82/checkout/session" → "session"
-- "net/http"                                      → "http"
-- "time"                                          → "time"
pathToAlias :: String -> String
pathToAlias path =
    let lastSeg = reverse (takeWhile (/= '/') (reverse path))
        cleaned = map (\c -> if isAlphaNum c then c else '_') lastSeg
        -- Go package paths often end in a version segment like "v82" — if so,
        -- use the preceding segment instead for a more meaningful alias.
        alias = if isVersionSegment lastSeg
            then let rest = reverse (drop (length lastSeg + 1) (reverse path))
                     prevSeg = reverse (takeWhile (/= '/') (reverse rest))
                 in if not (null prevSeg) && not (isVersionSegment prevSeg)
                     then sanitise prevSeg
                     else sanitise lastSeg
            else sanitise lastSeg
    in if null alias || not (isLower (head alias) || head alias == '_')
        then "p_" ++ alias
        else alias
  where
    sanitise s = map (\c -> if isAlphaNum c then c else '_') s
    isVersionSegment s = case s of
        'v':rest -> all (`elem` ('0'::Char):'1':'2':'3':'4':'5':'6':'7':'8':'9':[]) rest
        _ -> False


-- | Every Go package referenced in any function signature, including
-- the package itself (so the caller can check it was discovered).
discoverPackagePaths :: PkgInfo -> [String]
discoverPackagePaths pkg =
    let self = _pkgPath pkg
        allTypes = concatMap typesFromFn (_pkgFns pkg)
        paths = concatMap extractPackagePaths allTypes
    in nub (self : paths)
  where
    typesFromFn fn = map snd (_fnParams fn) ++ map snd (_fnResults fn)


-- | Extract every package path that appears in a Go type string.
-- Detects patterns like `<path>.<Name>` where `<path>` is slashes +
-- lowercase segments + dots, and `<Name>` starts with uppercase.
--
-- Handles: pointers (*), slices ([]), maps (map[K]V), arrays ([N]).
-- Does NOT recurse into generic parameters like `Map[K,V]` — handled
-- at skip-time via isTypeParam.
extractPackagePaths :: String -> [String]
extractPackagePaths s = go True s
  where
    -- atBoundary is True when the previous char was a type-term delimiter
    -- (*, [, ], ,, etc. or start-of-string). Only then does a lowercase
    -- character begin a fresh package path — we must not restart parsing
    -- mid-path (which would split "github.com/stripe/..." into "github"
    -- then "com/stripe/..." and misattribute the prefix).
    go _ [] = []
    go atBoundary input@(c:rest)
        | atBoundary && isLower c =
            case scanPath input of
                Just (path, more) -> path : go True more
                Nothing           -> go (isBoundary c) rest
        | otherwise = go (isBoundary c) rest

    -- Delimiters in Go type strings.
    isBoundary ch = ch `elem` (" \t\n*[](),<>" :: String)

    -- Walk the path chars (segments separated by `.` or `/`, lowercase
    -- segments OK, digits OK, `-` OK). Stop on `.` followed by uppercase,
    -- which marks the TypeName.
    scanPath = walk ""

    walk acc [] = Nothing
    walk acc (c:rest)
        | isSegChar c = walk (acc ++ [c]) rest
        | c == '/'    = walk (acc ++ "/") rest
        | c == '.'    =
            case rest of
                (n:_) | isUpper n ->
                    -- Delimiter! Consume the TypeName then return.
                    let (_name, rest') = span isNameChar rest
                    in if not (null acc) && (hasPathSep acc || isKnownBarePkg acc)
                        then Just (acc, rest')
                        else Nothing
                (n:_) | isLower n || isAlphaNum n ->
                    -- Still inside the path (e.g. "github.com").
                    walk (acc ++ ".") rest
                _ -> Nothing
        | otherwise = Nothing

    isSegChar c = isAlphaNum c || c == '-' || c == '_'
    isNameChar c = isAlphaNum c || c == '_'
    hasPathSep = any (== '/')

    isKnownBarePkg p = p `elem`
        [ "time", "io", "os", "fmt", "sync", "errors", "bytes"
        , "strings", "strconv", "unicode", "math", "sort", "regexp"
        , "reflect", "encoding", "bufio", "log", "context"
        ]


-- ══════════════════════════════════════════════════════════════════════════
-- Type rewriting
-- ══════════════════════════════════════════════════════════════════════════

-- | Rewrite every `<pkg-path>.<Name>` in a Go type string to `<alias>.<Name>`
-- using the alias table. Preserves *, [], []*, map[K]V wrappers.
-- Only starts parsing a path at a type boundary to avoid misparsing
-- "github.com/..." as "com/..." after eating the "github" prefix.
rewriteType :: AliasTable -> String -> String
rewriteType table = go True
  where
    go _ [] = []
    go atBoundary input@(c:rest)
        | atBoundary && isLower c =
            case scanPath input of
                Just (path, name, more) ->
                    case Map.lookup path table of
                        Just alias -> alias ++ "." ++ name ++ go True more
                        Nothing    -> c : go (isBoundary c) rest
                Nothing -> c : go (isBoundary c) rest
        | otherwise = c : go (isBoundary c) rest

    isBoundary ch = ch `elem` (" \t\n*[](),<>" :: String)

    scanPath = walk ""

    walk _ [] = Nothing
    walk acc (c:rest)
        | isSegChar c = walk (acc ++ [c]) rest
        | c == '/'    = walk (acc ++ "/") rest
        | c == '.'    =
            case rest of
                (n:_) | isUpper n ->
                    let (nameChars, rest') = span isNameChar rest
                    in if not (null acc) then Just (acc, nameChars, rest')
                                         else Nothing
                (n:_) | isLower n || isAlphaNum n ->
                    walk (acc ++ ".") rest
                _ -> Nothing
        | otherwise = Nothing

    isSegChar c = isAlphaNum c || c == '-' || c == '_'
    isNameChar c = isAlphaNum c || c == '_'


-- ══════════════════════════════════════════════════════════════════════════
-- Generics detection (the only remaining skip class)
-- ══════════════════════════════════════════════════════════════════════════

-- | True if the type references a Go generic type parameter (e.g. `T`
-- standing alone, or inside brackets `Fetch[T]`). Generics cannot be
-- realised at FFI time without monomorphisation; we skip them with a clear
-- comment, matching the self-hosted compiler.
isGenericType :: String -> Bool
isGenericType t = isBareParam t || hasBracketedParam t
  where
    isBareParam [c] = c >= 'A' && c <= 'Z'
    isBareParam _   = False

    hasBracketedParam s = case break (== '[') s of
        (_, '[':rest) ->
            let inside = takeWhile (/= ']') rest
                rest'  = drop 1 (dropWhile (/= ']') rest)
            in simpleParamInside inside || hasBracketedParam rest'
        _ -> False

    simpleParamInside inside =
        let parts = map trim (splitOn ',' inside)
        in any isBareParam parts

    trim = reverse . dropWhile (== ' ') . reverse . dropWhile (== ' ')

    splitOn _ [] = [""]
    splitOn d (x:xs)
        | x == d    = "" : splitOn d xs
        | otherwise = let (h:t) = splitOn d xs in (x:h) : t


-- ══════════════════════════════════════════════════════════════════════════
-- Argument coercion
-- ══════════════════════════════════════════════════════════════════════════

-- | Emit a Go expression that coerces args[i] to the rewritten Go type.
-- All primitives go through our AsInt/AsFloat helpers (which handle
-- int→int64→float→bool variants without panicking). Complex types use
-- a direct type assertion; if the assertion fails the panic is caught
-- by runWithRecover in rt and surfaced as Err.
goArgCast :: Int -> String -> String
goArgCast i t = case t of
    "string"   -> "fmt.Sprintf(\"%v\", args[" ++ show i ++ "])"
    "int"      -> "AsInt(args[" ++ show i ++ "])"
    "int8"     -> "int8(AsInt(args[" ++ show i ++ "]))"
    "int16"    -> "int16(AsInt(args[" ++ show i ++ "]))"
    "int32"    -> "int32(AsInt(args[" ++ show i ++ "]))"
    "int64"    -> "int64(AsInt(args[" ++ show i ++ "]))"
    "uint"     -> "uint(AsInt(args[" ++ show i ++ "]))"
    "uint8"    -> "uint8(AsInt(args[" ++ show i ++ "]))"
    "uint16"   -> "uint16(AsInt(args[" ++ show i ++ "]))"
    "uint32"   -> "uint32(AsInt(args[" ++ show i ++ "]))"
    "uint64"   -> "uint64(AsInt(args[" ++ show i ++ "]))"
    "float64"  -> "AsFloat(args[" ++ show i ++ "])"
    "float32"  -> "float32(AsFloat(args[" ++ show i ++ "]))"
    "bool"     -> "args[" ++ show i ++ "].(bool)"
    "byte"     -> "byte(AsInt(args[" ++ show i ++ "]))"
    "rune"     -> "rune(AsInt(args[" ++ show i ++ "]))"
    "[]byte"   ->
        "func() []byte { v := args[" ++ show i ++ "]; " ++
        "if b, ok := v.([]byte); ok { return b }; " ++
        "return []byte(fmt.Sprintf(\"%v\", v)) }()"
    "error"    -> "args[" ++ show i ++ "].(error)"
    _          -> "args[" ++ show i ++ "].(" ++ t ++ ")"


-- ══════════════════════════════════════════════════════════════════════════
-- Emission
-- ══════════════════════════════════════════════════════════════════════════

emitGoFile :: PkgInfo -> String
emitGoFile pkg =
    let aliases = buildAliasTable pkg
        entries = map (emitRegister (_pkgName pkg) aliases) (_pkgFns pkg)
        anyEmitted = any (not . isSkippedEntry) entries
        importLines = buildImportLines pkg aliases anyEmitted
    in unlines $
        [ "// Code generated by sky-ffi-inspect from " ++ _pkgPath pkg ++ ". DO NOT EDIT."
        , "// Re-run `sky add " ++ _pkgPath pkg ++ "` to regenerate."
        , "//"
        , "// SAFETY: every binding here is registered as effect-unknown."
        , "// Call via Sky.Ffi.callTask from Sky code — callPure will refuse."
        , "// To override a specific function as pure, audit the Go source"
        , "// then add a hand-written ffi/<pkg>_pure.go with rt.RegisterPure."
        , ""
        , "package rt"
        , ""
        , "import ("
        ]
        ++ importLines
        ++
        [ ")"
        , ""
        , "func init() {"
        ]
        ++ entries
        ++
        [ "}"
        , ""
        , "// Pin imports against \"imported and not used\" when many funcs were skipped."
        , "var _ = fmt.Sprintf"
        ]


-- | Emit the import block. Requested package keeps alias `pkg`; every other
-- discovered package gets its computed alias. We deliberately include every
-- discovered package even if no emitted binding actually references it —
-- harmless, and it means regenerating when user adds new hand-written
-- bindings in an adjacent file keeps working.
buildImportLines :: PkgInfo -> AliasTable -> Bool -> [String]
buildImportLines pkg aliases anyEmitted =
    let self = _pkgPath pkg
        sorted = sortOn fst (Map.toList aliases)
        pkgLine =
            if anyEmitted
                then "\tpkg " ++ quote self
                else "\t_ " ++ quote self
                     ++ "  // all bindings skipped; blank import retains go.mod dep"
        others =
            [ "\t" ++ alias ++ " " ++ quote path
            | (path, alias) <- sorted
            , path /= self
            ]
    in pkgLine : "\t\"fmt\"" : others


emitRegister :: String -> AliasTable -> FnInfo -> String
emitRegister pkgName aliases fn =
    let name = pkgName ++ "." ++ _fnName fn
        params = _fnParams fn
        results = _fnResults fn
        nArgs = length params

        -- Rewrite every param and result type to use the alias table.
        rewrittenParams = map (\(n, t) -> (n, rewriteType aliases t)) params
        rewrittenResults = map (\(n, t) -> (n, rewriteType aliases t)) results

        -- The only remaining skip: generic type parameters.
        hasGeneric =
            any (isGenericType . snd) rewrittenParams ||
            any (isGenericType . snd) rewrittenResults

    in if hasGeneric
        then "\t// SKIPPED " ++ name ++ " — generic type parameter (not realisable at FFI boundary)\n"
        else unlines
            [ "\tRegister(" ++ quote name ++ ", func(args []any) any {"
            , "\t\tif len(args) < " ++ show nArgs ++ " {"
            , "\t\t\treturn fmt.Errorf(\"" ++ name ++ ": expected " ++ show nArgs ++ " args, got %d\", len(args))"
            , "\t\t}"
            , emitCall fn rewrittenParams rewrittenResults
            , "\t}) // " ++ _fnEffect fn
            ]


-- | Emit the body of the binding function. Uses already-rewritten types.
emitCall :: FnInfo -> [(String, String)] -> [(String, String)] -> String
emitCall fn params results =
    let name = _fnName fn
        nParams = length params
        argExprs = zipWith (\i (_, t) ->
                let cast = goArgCast i t
                    isVariadicLast = _fnVariadic fn && i == nParams - 1
                in if isVariadicLast then cast ++ "..." else cast
            ) [0..] params
        call = "pkg." ++ name ++ "(" ++ intercalate ", " argExprs ++ ")"
    in case results of
        []  -> "\t\t" ++ call ++ "\n\t\treturn struct{}{}"
        [_] -> "\t\treturn " ++ call
        _   ->
            let lastTy = snd (last results)
                others = init results
                bindVars = zipWith (\i _ -> "r" ++ show i) [0::Int ..] others
                allVars = bindVars ++ (if lastTy == "error" then ["err"] else ["r" ++ show (length bindVars)])
                assignLine = "\t\t" ++ intercalate ", " allVars ++ " := " ++ call
            in if lastTy == "error"
                then unlines
                    [ assignLine
                    , "\t\tif err != nil {"
                    , "\t\t\treturn Err[any, any](err.Error())"
                    , "\t\t}"
                    , "\t\treturn Ok[any, any](" ++ packResults bindVars ++ ")"
                    ]
                else unlines
                    [ assignLine
                    , "\t\treturn []any{" ++ intercalate ", " allVars ++ "}"
                    ]


packResults :: [String] -> String
packResults []  = "struct{}{}"
packResults [v] = v
packResults vs  = "[]any{" ++ intercalate ", " vs ++ "}"


isSkippedEntry :: String -> Bool
isSkippedEntry s = not ("Register(" `isSubstringOf` s)


isSubstringOf :: String -> String -> Bool
isSubstringOf needle hay = go hay
  where
    n = length needle
    go [] = False
    go xs
        | take n xs == needle = True
        | otherwise = go (tail xs)


-- ══════════════════════════════════════════════════════════════════════════
-- .skyi catalogue
-- ══════════════════════════════════════════════════════════════════════════

emitSkyi :: PkgInfo -> String
emitSkyi pkg =
    let aliases = buildAliasTable pkg
    in unlines $
        [ "-- Auto-generated FFI binding catalogue for " ++ _pkgPath pkg
        , "--"
        , "-- All auto-generated bindings are registered effect-unknown and are"
        , "-- callable via Sky.Ffi.callTask. Every call returns Task String a"
        , "-- with panic recovery — any Go panic is caught and surfaced as Err."
        , "--"
        , "-- Opaque Go struct values flow through Sky as Any; use the bindings"
        , "-- to construct, read and update them. Sky records-with-methods can"
        , "-- bridge this gap idiomatically — define a record type whose methods"
        , "-- are the relevant callTask invocations."
        , "--"
        , "-- Imports used in this package's wrapper:"
        ]
        ++ [ "--   " ++ alias ++ " \"" ++ path ++ "\""
           | (path, alias) <- sortOn fst (Map.toList aliases)
           ]
        ++
        [ ""
        , "package " ++ _pkgName pkg
        ]
        ++ map emitSkyiFn (_pkgFns pkg)


emitSkyiFn :: FnInfo -> String
emitSkyiFn fn =
    let sig = if null (_fnParams fn)
            then "() -> " ++ goResultsToSky (_fnResults fn)
            else intercalate " -> " (map (goTypeToSky . snd) (_fnParams fn))
                    ++ " -> " ++ goResultsToSky (_fnResults fn)
    in "-- [" ++ _fnEffect fn ++ "] " ++ _fnName fn ++ " : " ++ sig
       ++ "   -- runtime wrap: Task String"


goResultsToSky :: [(String, String)] -> String
goResultsToSky [] = "()"
goResultsToSky [(_, t)] = goTypeToSky t
goResultsToSky rs = "(" ++ intercalate ", " (map (goTypeToSky . snd) rs) ++ ")"


goTypeToSky :: String -> String
goTypeToSky t = case t of
    "string"  -> "String"
    "int"     -> "Int"
    "int64"   -> "Int"
    "int32"   -> "Int"
    "float64" -> "Float"
    "float32" -> "Float"
    "bool"    -> "Bool"
    "error"   -> "String"
    _         -> stripPkg t
  where
    stripPkg = reverse . takeWhile (/= '.') . reverse


-- ══════════════════════════════════════════════════════════════════════════
-- Helpers
-- ══════════════════════════════════════════════════════════════════════════

quote :: String -> String
quote s = "\"" ++ concatMap esc s ++ "\""
  where
    esc '"'  = "\\\""
    esc '\\' = "\\\\"
    esc c    = [c]


slugify :: String -> String
slugify = map (\c -> if c `elem` ("./" :: String) then '_' else c)
