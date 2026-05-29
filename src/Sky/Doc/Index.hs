{-# LANGUAGE DeriveGeneric #-}
{-# LANGUAGE OverloadedStrings #-}
-- | Sky.Doc.Index — doc-server data layer.
--
-- The doc server's catalogue is a thin re-projection of the LSP
-- index. The LSP already extracts module → [symbol{name, sig,
-- doc, location}] for hover + goto-def; we just filter to public
-- symbols, group by module, and JSON-serialise for the bundled
-- Sky.Http.Server doc app.
--
-- v0.14.x MVP: project mode. The doc index is built from the
-- current working directory's project (sky.toml present) +
-- transitively-imported modules. Outside a project, an ephemeral
-- project is scaffolded that imports Sky.Core.Prelude so the
-- stdlib is reachable.
module Sky.Doc.Index
    ( DocIndex(..)
    , DocModule(..)
    , DocSymbol(..)
    , SymbolKind(..)
    , buildDocIndex
    , scaffoldEphemeralProject
    , encodeIndex
    , publicSymbolsByModule
    ) where

import qualified Data.Aeson as J
import qualified Data.ByteString.Lazy as BL
import qualified Data.List as List
import qualified Data.Map.Strict as Map
import           Data.Map.Strict (Map)
import qualified Data.Set as Set
import           GHC.Generics (Generic)
import qualified System.Directory as Dir
import           System.FilePath ((</>))

import qualified Sky.Reporting.Annotation as A
import qualified Sky.Lsp.Index as LIdx
import qualified Sky.Doc.KernelRegistry as KReg


-- | Top-level catalogue. Modules are grouped into buckets so the
-- doc-server's index page can render "this project" before
-- "dependencies" before "stdlib", mirroring `go doc ./...`'s
-- triage.
data DocIndex = DocIndex
    { diVersion  :: !String
    , diRoot     :: !FilePath
    , diProject  :: ![DocModule]   -- modules under the user's repo
    , diDeps     :: ![DocModule]   -- modules from imported Go packages (FFI bindings) — TODO v0.14.x.1
    , diStdlib   :: ![DocModule]   -- Sky.Core.*, Std.*, Sky.Live, Sky.Http.Server, …
    } deriving (Show, Generic)


-- | One module's documented surface.
data DocModule = DocModule
    { dmName     :: !String                -- "Std.Money"
    , dmFile     :: !FilePath              -- absolute path to .sky source
    , dmDoc      :: !(Maybe String)        -- module-header doc comment (-- |)
    , dmSymbols  :: ![DocSymbol]           -- already filtered to public + sorted
    } deriving (Show, Generic)


-- | One exposed symbol within a module.
data DocSymbol = DocSymbol
    { dsName     :: !String                -- "fromMajor"
    , dsKind     :: !SymbolKind
    , dsTypeSig  :: !(Maybe String)        -- "fromMajor : Currency -> Int -> Money"
    , dsDoc      :: !(Maybe String)        -- preceding `-- ` comment block
    , dsFile     :: !FilePath
    , dsLine     :: !Int                   -- 1-based
    , dsCol      :: !Int
    } deriving (Show, Generic)


data SymbolKind
    = KindFunction
    | KindCtor
    | KindType
    deriving (Show, Eq, Generic)


-- JSON instances — the bundled Sky doc-server app parses this
-- via Sky.Core.Json.Decode. Keys are camelCase to match
-- Sky's decoder convention.

instance J.ToJSON SymbolKind where
    toJSON KindFunction = J.String "function"
    toJSON KindCtor     = J.String "ctor"
    toJSON KindType     = J.String "type"

instance J.ToJSON DocSymbol where
    toJSON s = J.object
        [ ("name",    J.toJSON (dsName s))
        , ("kind",    J.toJSON (dsKind s))
        , ("typeSig", J.toJSON (dsTypeSig s))
        , ("doc",     J.toJSON (dsDoc s))
        , ("file",    J.toJSON (dsFile s))
        , ("line",    J.toJSON (dsLine s))
        , ("col",     J.toJSON (dsCol s))
        ]

instance J.ToJSON DocModule where
    toJSON m = J.object
        [ ("name",    J.toJSON (dmName m))
        , ("file",    J.toJSON (dmFile m))
        , ("doc",     J.toJSON (dmDoc m))
        , ("symbols", J.toJSON (dmSymbols m))
        ]

instance J.ToJSON DocIndex where
    toJSON i = J.object
        [ ("version", J.toJSON (diVersion i))
        , ("root",    J.toJSON (diRoot i))
        , ("project", J.toJSON (diProject i))
        , ("deps",    J.toJSON (diDeps i))
        , ("stdlib",  J.toJSON (diStdlib i))
        ]


-- | Serialise the index to JSON bytes ready for atomic write to
-- a temp file (the bundled doc-server app reads
-- $SKY_DOC_INDEX at startup).
encodeIndex :: DocIndex -> BL.ByteString
encodeIndex = J.encode


-- ─── Building the index ──────────────────────────────────────────

-- | The version string baked into the index header for the doc
-- server to surface in its footer.
buildDocIndex :: String -> FilePath -> IO DocIndex
buildDocIndex skyVersion projectRoot = do
    lspIdx <- LIdx.buildIndex projectRoot
    let bucketed = publicSymbolsByModule lspIdx projectRoot
    return DocIndex
        { diVersion = skyVersion
        , diRoot    = projectRoot
        , diProject = _bProject bucketed
        , diDeps    = _bDeps    bucketed
        , diStdlib  = _bStdlib  bucketed
        }


-- ─── Bucketing modules into project / deps / stdlib ─────────────

data Buckets = Buckets
    { _bProject :: ![DocModule]
    , _bDeps    :: ![DocModule]
    , _bStdlib  :: ![DocModule]
    }


-- | Group every documented symbol by module, then split modules
-- into project / deps / stdlib by source-file location:
--
--   * stdlib: file path mentions "/sky-stdlib/" (the embedded
--     stdlib materialised into `.skycache/.sky-stdlib/`) OR a
--     well-known stdlib module name prefix.
--   * deps: file path under `.skydeps/` (FFI binding catalogue) —
--     TODO v0.14.x.1 once FFI sigs are first-class in the doc
--     index.
--   * project: everything else.
publicSymbolsByModule :: LIdx.Index -> FilePath -> Buckets
publicSymbolsByModule idx projectRoot =
    let
        publicSyms = filter isPublic (Map.elems (LIdx.idxByQual idx))
        byMod = groupSymbolsByModule publicSyms
        -- A module's file is the file of its FIRST symbol (good
        -- enough for grouping; we don't render multiple files
        -- per module in the MVP).
        mkModule (modName, syms) =
            let
                file = case syms of
                    (s:_) -> LIdx.symFile s
                    []    -> ""
                docs = lookupModuleDoc idx modName
                sorted = List.sortOn dsName (map symToDoc syms)
            in DocModule
                { dmName    = modName
                , dmFile    = file
                , dmDoc     = docs
                , dmSymbols = sorted
                }
        modules = map mkModule (Map.toAscList byMod)
        -- Kernel-registry symbols (Sky.Core.String, Crypto, etc.)
        -- that aren't on-disk Sky source. The LSP index doesn't
        -- see them; we synthesise DocModules from the registry
        -- enumeration and merge them in below. Modules whose
        -- names overlap with on-disk modules (Sky.Core.List has
        -- both an .sky file AND legacy registry entries) prefer
        -- the on-disk version since it carries real docs.
        kernelModules = kernelModulesFromRegistry
        existingNames = Set.fromList (map dmName modules)
        newKernelModules =
            [ m | m <- kernelModules, not (Set.member (dmName m) existingNames) ]
        allModules = modules ++ newKernelModules
        classified = map (\m -> (classifyModule projectRoot m, m)) allModules
        pick t = [m | (t', m) <- classified, t' == t]
    in Buckets
        { _bProject = pick BucketProject
        , _bDeps    = pick BucketDeps
        , _bStdlib  = pick BucketStdlib
        }


-- | Build DocModules from the scraped kernel registry. Each
-- (module, name) pair gets its real HM signature via
-- `KReg.kernelSigString`; entries with no resolvable type are
-- skipped (shouldn't happen — registry is the source of truth).
--
-- Module names get canonicalised to the fully-qualified
-- Sky.Core.* / Std.* / Sky.Http.* shape (the kernel registry
-- uses bare names like "String", "Log"). This lets the doc
-- index dedupe against on-disk Sky-source modules with the
-- same canonical name (e.g. `Sky.Core.List` exists in both
-- sky-stdlib AND the kernel registry — the Sky-source version
-- wins).
kernelModulesFromRegistry :: [DocModule]
kernelModulesFromRegistry =
    let
        byMod = Map.fromListWith (++)
                    [ (canonicaliseKernelModule modName, [(modName, funcName)])
                    | (modName, funcName) <- KReg.kernelSymbols
                    ]
        mkSym (origMod, funcName) = DocSymbol
            { dsName    = funcName
            , dsKind    = KindFunction
              -- Pass the ORIGINAL (registry-side) module name to
              -- `kernelSigString` because `lookupKernelType`
              -- still keys on bare names.
            , dsTypeSig = KReg.kernelSigString origMod funcName
            , dsDoc     = Just (KReg.kernelDocFor origMod funcName)
            , dsFile    = "<kernel registry: " ++ origMod ++ ">"
            , dsLine    = 0
            , dsCol     = 0
            }
        mkMod (canonMod, entries) = DocModule
            { dmName    = canonMod
            , dmFile    = "<kernel registry>"
            , dmDoc     = Just ("Kernel module — defined in the compiler "
                              ++ "runtime (no on-disk source yet; "
                              ++ "migrating to Sky source is in progress).")
            , dmSymbols = List.sortOn dsName (map mkSym entries)
            }
    in [ mkMod (m, ns) | (m, ns) <- Map.toAscList byMod ]


-- | The kernel registry uses bare module names ("String",
-- "Crypto", "JsonDec") that match how user code references them
-- at call sites. The doc tool shows fully-qualified names so the
-- module list reads consistently. This table maps registry-side
-- → canonical.
--
-- Anything not in the table passes through unchanged (covers
-- Go-stdlib FFI proxies like `Context`, `Fmt` that have no Sky.*
-- alias).
canonicaliseKernelModule :: String -> String
canonicaliseKernelModule m = case m of
    -- Sky.Core.*
    "Basics"        -> "Sky.Core.Basics"
    "String"        -> "Sky.Core.String"
    "List"          -> "Sky.Core.List"
    "Dict"          -> "Sky.Core.Dict"
    "Set"           -> "Sky.Core.Set"
    "Maybe"         -> "Sky.Core.Maybe"
    "Result"        -> "Sky.Core.Result"
    "Math"          -> "Sky.Core.Math"
    "Char"          -> "Sky.Core.Char"
    "Path"          -> "Sky.Core.Path"
    "Crypto"        -> "Sky.Core.Crypto"
    "Encoding"      -> "Sky.Core.Encoding"
    "Regex"         -> "Sky.Core.Regex"
    "Uuid"          -> "Sky.Core.Uuid"
    "Random"        -> "Sky.Core.Random"
    "Task"          -> "Sky.Core.Task"
    "Time"          -> "Sky.Core.Time"
    "File"          -> "Sky.Core.File"
    "Io"            -> "Sky.Core.Io"
    "Http"          -> "Sky.Core.Http"
    "System"        -> "Sky.Core.System"
    "Process"       -> "Sky.Core.Process"
    "JsonEnc"       -> "Sky.Core.Json.Encode"
    "JsonDec"       -> "Sky.Core.Json.Decode"
    "JsonDecP"      -> "Sky.Core.Json.Decode.Pipeline"
    -- Std.*
    "Cmd"           -> "Std.Cmd"
    "Sub"           -> "Std.Sub"
    "Log"           -> "Std.Log"
    "Live"          -> "Std.Live"
    "Tui"           -> "Std.Tui"
    "Cli"           -> "Std.Cli"
    "Webview"       -> "Std.Webview"
    "Auth"          -> "Std.Auth"
    "Db"            -> "Std.Db"
    -- Sky.Http.*
    "Server"        -> "Sky.Http.Server"
    "Middleware"    -> "Sky.Http.Middleware"
    "RateLimit"     -> "Sky.Http.RateLimit"
    -- Sky.*
    "Test"          -> "Sky.Test"
    "Ffi"           -> "Sky.Ffi"
    -- Anything else — Go FFI proxies (Context, Fmt) — stays as-is.
    other           -> other


data BucketKind = BucketProject | BucketDeps | BucketStdlib
    deriving (Eq)


classifyModule :: FilePath -> DocModule -> BucketKind
classifyModule projectRoot m
    | isStdlibModule m  = BucketStdlib
    | isDepsFile (dmFile m) = BucketDeps
    | isInsideProject projectRoot (dmFile m) = BucketProject
    | otherwise = BucketProject  -- default: surface to project so nothing is hidden


isInsideProject :: FilePath -> FilePath -> Bool
isInsideProject projectRoot f =
    (projectRoot ++ "/") `List.isPrefixOf` f
    -- macOS symlink: /private/var/... vs /var/...
    || ("/private" ++ projectRoot ++ "/") `List.isPrefixOf` f


-- | Filter to symbols that warrant a doc page: top-level
-- functions, ADT constructors, and type names. Skip locals +
-- lambda params (those are scope-internal).
isPublic :: LIdx.Sym -> Bool
isPublic s =
    case LIdx.symKind s of
        LIdx.SymFunction -> True
        LIdx.SymCtor     -> True
        LIdx.SymType     -> True
        LIdx.SymLocal    -> False
        LIdx.SymParam    -> False


groupSymbolsByModule :: [LIdx.Sym] -> Map String [LIdx.Sym]
groupSymbolsByModule = foldr step Map.empty
  where
    step s = Map.insertWith (++) (LIdx.symModule s) [s]


symToDoc :: LIdx.Sym -> DocSymbol
symToDoc s = DocSymbol
    { dsName    = LIdx.symLocalName s
    , dsKind    = case LIdx.symKind s of
        LIdx.SymFunction -> KindFunction
        LIdx.SymCtor     -> KindCtor
        LIdx.SymType     -> KindType
        _                -> KindFunction
    , dsTypeSig = LIdx.symTypeSig s
    , dsDoc     = LIdx.symDoc s
    , dsFile    = LIdx.symFile s
    , dsLine    = A._line (A._start (LIdx.symRegion s))
    , dsCol     = A._col  (A._start (LIdx.symRegion s))
    }


-- | The module-header doc comment (above `module Foo exposing
-- (...)`). The LSP index doesn't surface this today; future
-- iteration will add `idxModDocs :: Map String String` and we
-- wire it here. v0.14.x: leave Nothing.
lookupModuleDoc :: LIdx.Index -> String -> Maybe String
lookupModuleDoc _idx _modName = Nothing


-- ─── Bucket predicates ──────────────────────────────────────────

isStdlibModule :: DocModule -> Bool
isStdlibModule m =
    let f = dmFile m
        n = dmName m
    in "/sky-stdlib/" `List.isInfixOf` f
       || ".sky-stdlib/" `List.isInfixOf` f
       -- Kernel-registry synthetic modules (no on-disk file).
       || "<kernel registry" `List.isPrefixOf` f
       || "Sky.Core." `List.isPrefixOf` n
       || "Std." `List.isPrefixOf` n
       || n `elem` [ "Sky.Live", "Sky.Tui", "Sky.Cli", "Sky.Http"
                   , "Sky.Http.Server", "Sky.Http.RateLimit"
                   , "Sky.Http.Middleware", "Sky.Test", "Sky.Webview"
                   , "Sky.Ffi", "Context", "Fmt", "System", "Log"
                   ]


isDepsFile :: FilePath -> Bool
isDepsFile f = ".skydeps/" `List.isInfixOf` f
            || "/.skycache/ffi/" `List.isInfixOf` f


-- ─── Ephemeral project scaffold (out-of-project mode) ───────────

-- | When `sky doc` runs outside a project, build a minimal
-- scaffold in a temp dir so the LSP indexer has something to
-- typecheck. The scaffold imports Sky.Core.Prelude — the rest of
-- the stdlib is reachable transitively (the canonicaliser pulls
-- every kernel module into scope).
--
-- v0.14.x: shipped as an opt-in fallback. The MVP path is
-- in-project; out-of-project mode is a follow-up commit.
scaffoldEphemeralProject :: FilePath -> IO ()
scaffoldEphemeralProject dir = do
    Dir.createDirectoryIfMissing True (dir </> "src")
    writeFile (dir </> "sky.toml") $
        "name = \"sky-doc-ephemeral\"\n"
        ++ "version = \"0.0.0\"\n"
        ++ "entry = \"src/Main.sky\"\n\n"
        ++ "[source]\nroot = \"src\"\n"
    writeFile (dir </> "src" </> "Main.sky") $
        "module Main exposing (main)\n\n"
        ++ "-- Ephemeral entry — `sky doc --serve` uses this to\n"
        ++ "-- give the LSP indexer a typecheckable project so\n"
        ++ "-- the stdlib's signatures are reachable.\n\n"
        ++ "import Sky.Core.Prelude exposing (..)\n"
        ++ "import Std.Log exposing (println)\n\n"
        ++ "main = println \"sky doc ephemeral entry\"\n"
    return ()


