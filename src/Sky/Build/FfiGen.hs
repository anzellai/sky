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
import Data.Char (isAlphaNum, isLower, isUpper, toUpper, toLower)
import Data.List (foldl', intercalate, nub, sortOn)
import qualified Data.Map.Strict as Map
import qualified Data.Set as Set
import qualified Data.Text as T
import qualified Data.Text.Encoding as TE
import System.Directory (createDirectoryIfMissing, doesFileExist, getCurrentDirectory)
import System.Environment (lookupEnv)
import System.FilePath ((</>), takeDirectory)
import System.Process (readProcessWithExitCode)


-- | Information extracted about one Go function
data FnInfo = FnInfo
    { _fnName     :: String
    , _fnParams   :: [(String, String)]  -- (name, goType)
    , _fnResults  :: [(String, String)]  -- (name, goType)
    , _fnVariadic :: Bool                -- last param is ...T
    , _fnEffect   :: String              -- pure / fallible / effectful
    , _fnRecvType :: String              -- "" for free func, else Go type
    , _fnMethodName :: String            -- "" for free func, else method
    , _fnIsField  :: Bool                -- synthetic struct-field getter
    , _fnIsFieldSet :: Bool              -- synthetic struct-field setter
    , _fnIsPkgVar :: Bool                -- synthetic pkg-level var/const getter
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
        <*> o A..:? "recvType" A..!= ""
        <*> o A..:? "methodName" A..!= ""
        <*> o A..:? "isField" A..!= False
        <*> o A..:? "isFieldSet" A..!= False
        <*> o A..:? "isPkgVar" A..!= False
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
    inspectorPath <- findInspector
    case inspectorPath of
        Nothing -> return (Left "sky-ffi-inspect binary not found on disk. Build bin/sky-ffi-inspect or set SKY_FFI_INSPECTOR=/path.")
        Just bin -> do
            let cmd = "cd sky-out && " ++ bin ++ " " ++ pkgPath
            (_, out, err) <- readProcessWithExitCode "sh" ["-c", cmd] ""
            if null out
                then return (Left $ "sky-ffi-inspect: empty output; stderr: " ++ err)
                else case A.eitherDecode (BL.fromStrict (TE.encodeUtf8 (T.pack out))) of
                    Left e  -> return (Left $ "sky-ffi-inspect: json: " ++ e)
                    Right p -> return (Right p)


-- | Probe common locations for the sky-ffi-inspect binary.
-- Looks at: SKY_FFI_INSPECTOR env var, ./bin, ../bin … walking up ancestors.
findInspector :: IO (Maybe FilePath)
findInspector = do
    envPath <- lookupEnv "SKY_FFI_INSPECTOR"
    case envPath of
        Just p | not (null p) -> do
            ok <- doesFileExist p
            if ok then return (Just p) else walkUp
        _ -> walkUp
  where
    walkUp = do
        cwd <- getCurrentDirectory
        go cwd 12
    go _   0 = return Nothing
    go dir n = do
        let candidate = dir </> "bin" </> "sky-ffi-inspect"
        ok <- doesFileExist candidate
        if ok
            then return (Just candidate)
            else let parent = takeDirectory dir
                 in if parent == dir
                        then return Nothing
                        else go parent (n - 1)


generateBindings :: PkgInfo -> IO [String]
generateBindings pkg = do
    createDirectoryIfMissing True "ffi"
    let slug = slugify (_pkgName pkg)
        kname = kernelNameFromPkg pkg
        mname = pkgToModuleName (_pkgPath pkg)
        goFile  = "ffi" </> (slug ++ "_bindings.go")
        skyiFile = "ffi" </> (slug ++ ".skyi")
        jsonFile = "ffi" </> (slug ++ ".kernel.json")
        names = map (\fn -> mname ++ "." ++ lowerFirst (_fnName fn)) (_pkgFns pkg)
    writeFile goFile (emitGoFile kname pkg)
    writeFile skyiFile (emitSkyi pkg)
    writeFile jsonFile (emitKernelJson mname kname pkg)
    return names


-- | Convert a Go package path to the Sky-side module name using the
-- path-segment → dotted-camel transform Sky users expect.
-- "github.com/google/uuid"           → "Github.Com.Google.Uuid"
-- "github.com/stripe/stripe-go/v84"  → "Github.Com.Stripe.StripeGo.V84"
-- "fyne.io/fyne/v2/app"              → "Fyne.Io.Fyne.V2.App"
-- "net/http"                         → "Net.Http"
--
-- Hyphen handling: drop the hyphen, upper-case the next char — matches
-- the legacy Sky convention and what Sky users write in real code
-- (e.g., `import Github.Com.Stripe.StripeGo.V84 as Stripe`).
pkgToModuleName :: String -> String
pkgToModuleName path =
    let slashed = splitOnChar '/' path
        dotted  = concatMap (splitOnChar '.') slashed
        cleaned = map camelHyphen dotted
        cap     = map capitaliseFirst (filter (not . null) cleaned)
    in  intercalate "." cap
  where
    -- "stripe-go" -> "stripeGo"; non-alphanum (other than '-') -> '_'.
    camelHyphen s = go False s
      where
        go _  []          = []
        go _  ('-':cs)    = go True cs
        go True (c:cs)    = toUpper c : go False cs
        go False (c:cs)
          | isAlphaNum c = c : go False cs
          | otherwise    = '_' : go False cs


-- | Pick the Sky-kernel-name (the prefix used for Go wrapper fns).
-- Always prefixed with "Go_" so FFI-generated wrappers can't collide with
-- hand-written stdlib kernel functions (e.g. the stdlib exposes Uuid_v4 /
-- Uuid_parse from Sky.Core.Uuid — an FFI binding to github.com/google/uuid
-- becomes Go_Uuid_newString etc., never clashing).
kernelNameFromPkg :: PkgInfo -> String
kernelNameFromPkg pkg =
    let segs = filter (not . null) (splitOnChar '/' (_pkgPath pkg))
        capOf s = capitaliseFirst (map (\c -> if isAlphaNum c then c else '_') s)
        baseName = case reverse segs of
            (last1 : prev : _) | isVersion last1 ->
                capOf prev ++ capOf last1
            (last1 : _) -> capOf last1
            []          -> "Ffi"
    in  "Go_" ++ baseName
  where
    isVersion ('v':rest) = all (`elem` ("0123456789" :: String)) rest && not (null rest)
    isVersion _ = False


splitOnChar :: Char -> String -> [String]
splitOnChar _ [] = [""]
splitOnChar sep (x:xs)
    | x == sep = "" : splitOnChar sep xs
    | otherwise = case splitOnChar sep xs of
        (h:t) -> (x:h) : t
        []    -> [[x]]


capitaliseFirst :: String -> String
capitaliseFirst [] = []
capitaliseFirst (c:cs) = toUpper c : cs


lowerFirst :: String -> String
lowerFirst [] = []
lowerFirst (c:cs) = toLower c : cs


-- ══════════════════════════════════════════════════════════════════════════
-- kernel.json emission — consumed by Sky.Build.FfiRegistry at sky build time
-- ══════════════════════════════════════════════════════════════════════════

emitKernelJson :: String -> String -> PkgInfo -> String
emitKernelJson moduleName kernelName pkg =
    let fns = filter (not . shouldSkipFn) (_pkgFns pkg)
        fnEntries = intercalate ",\n" (map emitFnEntry fns)
        emitFnEntry fn =
            "    {\"name\": " ++ quote (lowerFirst (_fnName fn)) ++
            ", \"arity\": " ++ show (max 1 (length (_fnParams fn))) ++ "}"
    in unlines
        [ "{"
        , "  \"moduleName\": " ++ quote moduleName ++ ","
        , "  \"kernelName\": " ++ quote kernelName ++ ","
        , "  \"package\": " ++ quote (_pkgPath pkg) ++ ","
        , "  \"functions\": ["
        , fnEntries
        , "  ]"
        , "}"
        ]


-- | Skip functions that can't be realised at the FFI boundary.
shouldSkipFn :: FnInfo -> Bool
shouldSkipFn fn =
    let hasGeneric = any (isGenericType . snd) (_fnParams fn)
                  || any (isGenericType . snd) (_fnResults fn)
                  || any (genericHint . snd) (_fnParams fn)
                  || any (genericHint . snd) (_fnResults fn)
        isIdPointer = length (_fnParams fn) == 1
                   && length (_fnResults fn) == 1
                   && isBareParam (snd (head (_fnParams fn)))
                   && isStarBareParam (snd (head (_fnResults fn)))
        refsInternal = any (touchesInternal . snd) (_fnParams fn)
                    || any (touchesInternal . snd) (_fnResults fn)
    in (hasGeneric && not isIdPointer) || refsInternal
  where
    -- True when a type string mentions any `<path>/internal[/<more>].Name` or
    -- `<path>/vendor[/<more>].Name` — Go forbids cross-module imports of those.
    touchesInternal t = "/internal." `isSubstringOf` t
                     || "/internal/" `isSubstringOf` t
                     || "/vendor." `isSubstringOf` t
                     || "/vendor/" `isSubstringOf` t
    -- Coarse check for any `[T ...]` or `[T, U]` generic instantiation
    -- anywhere in the type string — `isGenericType` only catches bracketed
    -- params at the top-level position, but Stripe's receivers look like
    -- `*pkg.V2List[T any]` where the generic lives inside a pointer.
    genericHint t = "[T " `isSubstringOf` t
                 || "[T]" `isSubstringOf` t
                 || "[T," `isSubstringOf` t
                 || "[K " `isSubstringOf` t
                 || "[V " `isSubstringOf` t
                 || "[]T" `isSubstringOf` t
                 || endsT t
      where
        endsT s = s == "T" || "*T" `isSuffix` s
        isSuffix suf s = length s >= length suf &&
                        drop (length s - length suf) s == suf


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
        -- Go disallows importing `internal/` subtrees outside their home
        -- module; skip them instead of emitting a go build error. Same
        -- applies to `vendor/` subtrees.
        hasSeg seg p = any (== seg) (splitOnChar '/' p)
        ok p = not (hasSeg "internal" p) && not (hasSeg "vendor" p)
    in nub (self : filter ok paths)
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
        , "hash", "crypto", "net", "mime", "path"
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

emitGoFile :: String -> PkgInfo -> String
emitGoFile kernelName pkg =
    let aliases = buildAliasTable pkg
        entries = map (emitTypedWrapper kernelName aliases) (_pkgFns pkg)
        anyEmitted = any (not . isSkippedEntry) entries
        -- Any alias that doesn't appear in any emitted entry becomes a blank
        -- import so Go doesn't error with "imported and not used". `pkg` and
        -- `fmt` are always considered used (pkg → wrapper calls, fmt → Sprintf).
        emittedBlob = concat entries
        usedAliases = Set.insert "pkg"
                    $ Set.insert "fmt"
                    $ Set.fromList
                        [ alias
                        | alias <- Map.elems aliases
                        , (alias ++ ".") `isSubstringOf` emittedBlob
                        ]
        usesReflect = "reflect.ValueOf" `isSubstringOf` emittedBlob
        importLines =
            buildImportLinesFiltered pkg aliases anyEmitted usedAliases
            ++ [ "\t\"reflect\"" | usesReflect ]
    in unlines $
        [ "// Code generated by sky-ffi-inspect from " ++ _pkgPath pkg ++ ". DO NOT EDIT."
        , "// Re-run `sky add " ++ _pkgPath pkg ++ "` to regenerate."
        , "//"
        , "// Wrapper functions are in `package rt` with names <Kernel>_<lowerFn>."
        , "// Sky source resolves `import " ++ pkgToModuleName (_pkgPath pkg) ++
          " as X` and calls `X.<lowerFn>` — the canonicaliser routes it via"
        , "// the FFI registry to these typed Go functions. Every wrapper wraps"
        , "// panics in Err[any, any] via SkyFfiRecover."
        , ""
        , "package rt"
        , ""
        , "import ("
        ]
        ++ importLines
        ++
        [ ")"
        , ""
        ]
        ++ entries
        ++
        [ ""
        , "// Pin fmt against \"imported and not used\" across partial files."
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


-- | Variant that rewrites unused aliases to `_ "<path>"` blank imports.
buildImportLinesFiltered :: PkgInfo -> AliasTable -> Bool -> Set.Set String -> [String]
buildImportLinesFiltered pkg aliases anyEmitted used =
    let self = _pkgPath pkg
        sorted = sortOn fst (Map.toList aliases)
        pkgLine =
            if anyEmitted
                then "\tpkg " ++ quote self
                else "\t_ " ++ quote self
                     ++ "  // all bindings skipped; blank import retains go.mod dep"
        others =
            [ if Set.member alias used
                then "\t" ++ alias ++ " " ++ quote path
                else "\t_ " ++ quote path ++ "  // aliased " ++ alias ++ "; unused in emitted wrappers"
            | (path, alias) <- sorted
            , path /= self
            ]
    in pkgLine : "\t\"fmt\"" : others


-- | Emit a typed Go wrapper function for a single Go-package binding.
-- The function is named `<Kernel>_<lowerFn>` and takes one `any` param per
-- Sky-level arg (zero-Go-arg becomes one unit param). The body:
--   1. installs SkyFfiRecover so panics → Err
--   2. coerces each Sky-side any to the expected Go type
--   3. calls pkg.<GoFn>(...)
--   4. wraps the result in Ok/Err per (T, error)/pure conventions
emitTypedWrapper :: String -> AliasTable -> FnInfo -> String
emitTypedWrapper kernelName aliases fn =
    let goFnName = _fnName fn
        skyName = lowerFirst goFnName
        params = _fnParams fn
        results = _fnResults fn
        wrapperName = kernelName ++ "_" ++ skyName
        nArgs = max 1 (length params)

        rewrittenParams = map (\(n, t) -> (n, rewriteType aliases t)) params
        rewrittenResults = map (\(n, t) -> (n, rewriteType aliases t)) results

        hasGeneric =
            any (isGenericType . snd) rewrittenParams ||
            any (isGenericType . snd) rewrittenResults

        isIdentityPointer =
            hasGeneric &&
            length rewrittenParams == 1 &&
            length rewrittenResults == 1 &&
            isBareParam (snd (head rewrittenParams)) &&
            isStarBareParam (snd (head rewrittenResults))

        paramList = intercalate ", " [ "p" ++ show i ++ " any" | i <- [0 .. nArgs - 1] ]
        -- When the Go function takes 0 args but Sky passes 1 (unit), silence
        -- the unused-variable warning for the unit param.
        unitSink = if null params
                    then "\t_ = p0\n"
                    else ""

        cls = wrapperClass fn rewrittenParams rewrittenResults
        effectful = any ((== "error") . snd) rewrittenResults
        hasErr = if effectful then "true" else "false"
        skyArgsList =
            "[]any{" ++ intercalate ", "
                [ "p" ++ show i | i <- [0 .. nArgs - 1] ]
                ++ "}"
        reflectCall target =
            [ "// [" ++ _fnEffect fn ++ "] " ++ kernelName ++ "." ++ skyName ++
              " → " ++ target ++ " (via SkyFfiReflectCall)"
            , "func " ++ wrapperName ++ "(" ++ paramList ++ ") (out any) {"
            , "\tdefer SkyFfiRecover(&out)()"
            , "\tout = SkyFfiReflectCall(" ++ target ++ ", " ++ hasErr ++
              ", " ++ skyArgsList ++ ")"
            , "\treturn"
            , "}"
            ]

    in case cls of
        _ | isIdentityPointer -> emitIdentityPointerTyped wrapperName

        _ | _fnIsField fn ->
            -- One-line delegate to SkyFfiFieldGet. Emitting the reflect
            -- dance here per-field blew stripe_bindings.go to 1.9M lines.
            "func " ++ wrapperName ++ "(p0 any) any { return SkyFfiFieldGet(p0, " ++
            quote (_fnMethodName fn) ++ ") }\n"

        _ | _fnIsFieldSet fn ->
            -- One-line delegate to SkyFfiFieldSet — value-first for |>.
            "func " ++ wrapperName ++ "(value any, recv any) any { return SkyFfiFieldSet(value, recv, " ++
            quote (_fnMethodName fn) ++ ") }\n"

        _ | _fnIsPkgVar fn ->
            case (_fnRecvType fn, _fnMethodName fn) of
                -- Zero-value struct constructor: New<TypeName>() -> *TypeName.
                (typeName, "") | not (null typeName) ->
                    "func " ++ wrapperName ++ "(_ any) any { return new(pkg." ++
                    typeName ++ ") }\n"
                -- Setter for a pkg-level var: SetName(value) → pkg.Name = value.
                -- Use reflect to assign through any — no compile-time type
                -- reference needed, handles any Sky-any value generically.
                ("", varName) | not (null varName) ->
                    "func " ++ wrapperName ++ "(value any) any { " ++
                    "reflect.ValueOf(&pkg." ++ varName ++ ").Elem().Set(" ++
                    "reflect.ValueOf(value).Convert(reflect.TypeOf(pkg." ++ varName ++ "))); " ++
                    "return struct{}{} }\n"
                -- Plain pkg-level var/const read: return pkg.Name.
                _ ->
                    "func " ++ wrapperName ++ "(_ any) any { return pkg." ++
                    _fnName fn ++ " }\n"

        DirectCall ->
            unlines
                [ "// [" ++ _fnEffect fn ++ "] " ++ kernelName ++ "." ++ skyName ++
                  " → pkg." ++ goFnName
                , "func " ++ wrapperName ++ "(" ++ paramList ++ ") (out any) {"
                , "\tdefer SkyFfiRecover(&out)()"
                , unitSink ++ emitTypedCall fn rewrittenParams rewrittenResults
                , "\treturn"
                , "}"
                ]

        ReflectTopLevel ->
            unlines (reflectCall ("reflect.ValueOf(pkg." ++ goFnName ++ ")"))

        ReflectGeneric ->
            -- Top-level generic functions can only be instantiated with `any`
            -- when their type-param constraint is `any`. The inspector JSON
            -- doesn't expose the constraint, so we emit a runtime stub that
            -- returns Err. Users who need a specific instantiation can add a
            -- hand-written ffi/<pkg>_manual.go with the concrete type.
            let paramSinks = concat
                    [ "\t_ = p" ++ show i ++ "\n" | i <- [0 .. nArgs - 1] ]
            in unlines
                [ "// [" ++ _fnEffect fn ++ "] " ++ kernelName ++ "." ++ skyName ++
                  " → pkg." ++ goFnName ++
                  " — generic function (stub; instantiate manually if needed)"
                , "func " ++ wrapperName ++ "(" ++ paramList ++ ") (out any) {"
                , paramSinks ++
                  "\tout = Err[any, any](" ++ quote ("generic function " ++ goFnName ++ " requires hand-written instantiation") ++ ")"
                , "\treturn"
                , "}"
                ]

        ReflectMethod methodName ->
            unlines
                [ "// [" ++ _fnEffect fn ++ "] " ++ kernelName ++ "." ++ skyName ++
                  " → " ++ (_fnRecvType fn) ++ "." ++ methodName ++ " (receiver-reflect)"
                , "func " ++ wrapperName ++ "(" ++ paramList ++ ") (out any) {"
                , "\tdefer SkyFfiRecover(&out)()"
                , "\trecv := reflect.ValueOf(p0)"
                , "\tm := recv.MethodByName(" ++ quote methodName ++ ")"
                , "\tif !m.IsValid() {"
                , "\t\tout = Err[any, any](" ++ quote (methodName ++ ": no such method on receiver") ++ ")"
                , "\t\treturn"
                , "\t}"
                , "\tout = SkyFfiReflectCall(m, " ++ hasErr ++
                  ", []any{" ++ intercalate ", "
                    [ "p" ++ show i | i <- [1 .. nArgs - 1] ]
                    ++ "})"
                , "\treturn"
                , "}"
                ]

        Unreachable reason ->
            let paramSinks = concat
                    [ "\t_ = p" ++ show i ++ "\n" | i <- [0 .. nArgs - 1] ]
            in unlines
                [ "// SKIPPED " ++ wrapperName ++ " — " ++ reason ++
                  " (wrapper will return Err at runtime)"
                , "func " ++ wrapperName ++ "(" ++ paramList ++ ") (out any) {"
                , paramSinks ++
                  "\tout = Err[any, any](" ++ quote ("FFI binding unavailable: " ++ reason) ++ ")"
                , "\treturn"
                , "}"
                ]


-- | Classification of how to emit a wrapper for a given function.
data WrapperClass
    = DirectCall                   -- clean signature; today's typed call
    | ReflectTopLevel              -- internal-pkg-ref in non-generic fn
    | ReflectGeneric               -- bare T / [T any] somewhere (top-level)
    | ReflectMethod String         -- method via MethodByName; String is method
    | Unreachable String           -- neither approach compiles; returns Err


wrapperClass :: FnInfo -> [(String, String)] -> [(String, String)] -> WrapperClass
wrapperClass fn rparams rresults
    | not (null (_fnMethodName fn))
    , hasGeneric || hasInternal
    = ReflectMethod (_fnMethodName fn)
    | hasGeneric
    = ReflectGeneric
    | hasInternal
    = ReflectTopLevel
    | otherwise
    = DirectCall
  where
    allTypes = map snd rparams ++ map snd rresults
    hasGeneric = any isGenericType allTypes || any hasGenericMarker allTypes
    hasInternal = any touchesInternal allTypes

    hasGenericMarker t =
        "[T " `isSubstringOf` t
        || "[T]" `isSubstringOf` t
        || "[T," `isSubstringOf` t
        || "[K " `isSubstringOf` t
        || "[V " `isSubstringOf` t
        || "[]T" `isSubstringOf` t
        || t == "T"
        || "*T" `isSuffixOfStr` t

    touchesInternal t =
        "/internal." `isSubstringOf` t
        || "/internal/" `isSubstringOf` t
        || "/vendor." `isSubstringOf` t
        || "/vendor/" `isSubstringOf` t

    isSuffixOfStr suf s =
        length s >= length suf && drop (length s - length suf) s == suf


emitIdentityPointerTyped :: String -> String
emitIdentityPointerTyped wrapperName = unlines
    [ "// Generic identity-pointer helper via reflect."
    , "func " ++ wrapperName ++ "(p0 any) (out any) {"
    , "\tdefer SkyFfiRecover(&out)()"
    , "\trv := reflectValueOfAny(p0)"
    , "\tpv := reflectNewOf(rv.Type())"
    , "\tpv.Elem().Set(rv)"
    , "\tout = pv.Interface()"
    , "\treturn"
    , "}"
    ]


-- | Bare generic type parameter — a single uppercase letter.
isBareParam :: String -> Bool
isBareParam [c] = c >= 'A' && c <= 'Z'
isBareParam _ = False


-- | Pointer to a bare generic type parameter: `*T`.
isStarBareParam :: String -> Bool
isStarBareParam ('*':rest) = isBareParam rest
isStarBareParam _ = False


-- | Emit the body of the typed wrapper. Uses `pN` params (not args[i]) and
-- always assigns to `out` so SkyFfiRecover's deferred closure can intercept.
emitTypedCall :: FnInfo -> [(String, String)] -> [(String, String)] -> String
emitTypedCall fn params results =
    let name = _fnName fn
        recvT = _fnRecvType fn
        methodN = _fnMethodName fn
        nParams = length params
        argExprs = zipWith (\i (_, t) ->
                let cast = typedArgCast i t
                    isVariadicLast = _fnVariadic fn && i == nParams - 1
                in if isVariadicLast then cast ++ "..." else cast
            ) [0::Int ..] params
        call = if null methodN
            then "pkg." ++ name ++ "(" ++ intercalate ", " argExprs ++ ")"
            else
                -- Method call: first arg is the receiver, rest forwarded.
                let recvCast = case params of
                        ((_, rt) : _) -> typedArgCast 0 rt
                        _ -> "p0"
                    methodArgs = drop 1 argExprs
                in recvCast ++ "." ++ methodN ++ "(" ++ intercalate ", " methodArgs ++ ")"
    in case results of
        []  -> "\t" ++ call ++ "\n\tout = Ok[any, any](struct{}{})"
        [(_, t)]
            | t == "error" -> unlines
                [ "\terr := " ++ call
                , "\tif err != nil { out = Err[any, any](err.Error()); return }"
                , "\tout = Ok[any, any](struct{}{})"
                ]
            | otherwise -> "\tout = Ok[any, any](" ++ call ++ ")"
        _   ->
            let lastTy = snd (last results)
                others = init results
                bindVars = zipWith (\i _ -> "r" ++ show i) [0::Int ..] others
                allVars = bindVars ++
                    (if lastTy == "error"
                        then ["err"]
                        else ["r" ++ show (length bindVars)])
                assignLine = "\t" ++ intercalate ", " allVars ++ " := " ++ call
            in if lastTy == "error"
                then unlines
                    [ assignLine
                    , "\tif err != nil { out = Err[any, any](err.Error()); return }"
                    , "\tout = Ok[any, any](" ++ packResults bindVars ++ ")"
                    ]
                else unlines
                    [ assignLine
                    , "\tout = Ok[any, any]([]any{" ++ intercalate ", " allVars ++ "})"
                    ]


-- | Typed-param arg coercion — pN instead of args[i].
typedArgCast :: Int -> String -> String
typedArgCast i t =
    let p = "p" ++ show i
    in case t of
        "string"   -> "fmt.Sprintf(\"%v\", " ++ p ++ ")"
        "int"      -> "AsInt(" ++ p ++ ")"
        "int8"     -> "int8(AsInt(" ++ p ++ "))"
        "int16"    -> "int16(AsInt(" ++ p ++ "))"
        "int32"    -> "int32(AsInt(" ++ p ++ "))"
        "int64"    -> "int64(AsInt(" ++ p ++ "))"
        "uint"     -> "uint(AsInt(" ++ p ++ "))"
        "uint8"    -> "uint8(AsInt(" ++ p ++ "))"
        "uint16"   -> "uint16(AsInt(" ++ p ++ "))"
        "uint32"   -> "uint32(AsInt(" ++ p ++ "))"
        "uint64"   -> "uint64(AsInt(" ++ p ++ "))"
        "float64"  -> "AsFloat(" ++ p ++ ")"
        "float32"  -> "float32(AsFloat(" ++ p ++ "))"
        "bool"     -> "AsBool(" ++ p ++ ")"
        "byte"     -> "byte(AsInt(" ++ p ++ "))"
        "rune"     -> "rune(AsInt(" ++ p ++ "))"
        "[]byte"   -> "SkyFfiArg_bytes(" ++ p ++ ")"
        "error"    -> p ++ ".(error)"
        _          -> p ++ ".(" ++ t ++ ")"


packResults :: [String] -> String
packResults []  = "struct{}{}"
packResults [v] = v
packResults vs  = "[]any{" ++ intercalate ", " vs ++ "}"


isSkippedEntry :: String -> Bool
isSkippedEntry s = not ("func " `isSubstringOf` s)


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
