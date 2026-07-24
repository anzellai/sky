{-# LANGUAGE OverloadedStrings #-}
-- | Reads ffi/*.kernel.json files into a registry used by the canonicaliser
-- and Kernel.lookup so FFI packages flow through the same resolution path as
-- stdlib kernel modules.
module Sky.Build.FfiRegistry
    ( FfiRegistry(..)
    , FfiModule(..)
    , FfiFunction(..)
    , loadRegistry
    , loadRegistryFrom
    , emptyRegistry
    , lookupFunction
    -- v0.17 close P1 — pure derived projections.
    -- These mirror the shape of legacy FFI IORefs in
    -- Sky.Canonicalise.Environment (ffiKernelModulesRef,
    -- ffiKernelFunctionsRef, ffiKernelArityRef, ffiImplementsRef,
    -- ffiPkgAliasRef) and the typed-FtyAst stored under
    -- ffiKernelTypeRef before the Compile.hs Can.Annotation
    -- conversion. Threading these as pure values via CompileCtx
    -- (P2+) deletes the IORefs (criterion #3).
    , kernelModulesMap
    , kernelFunctionsMap
    , kernelArityMap
    , kernelTypeFtyMap
    , implementsMap
    , pkgAliasMap
    ) where

import qualified Data.Aeson as A
import Data.Aeson ((.:), (.:?), (.!=))
import qualified Data.ByteString.Lazy as BL
import Control.Monad (filterM)
import Data.List (isSuffixOf)
import qualified Data.Map.Strict as Map
import System.Directory (doesDirectoryExist, listDirectory)
import System.FilePath ((</>))

import Sky.Build.FfiTypeParser (FtyAst, parseFty)


data FfiFunction = FfiFunction
    { _ffn_name    :: !String     -- Sky-side name, e.g. "newString"
    , _ffn_arity   :: !Int        -- Sky-side arity (unit param for zero-Go-arg)
    , _ffn_skyType :: !(Maybe FtyAst)
        -- ^ Parsed Sky-side wrapper type, including the runtime
        -- @Result Error _@ wrap (see Sky.Build.FfiGen.wrapperSkyType).
        -- 'Nothing' when the JSON entry omits @skyType@ — happens
        -- for FFI shapes the inspector can't faithfully render
        -- (channels, deeply-nested inline-struct callback bundles)
        -- and for older kernel.json files written before this field
        -- existed. The HM-wire path falls back to the legacy
        -- "no Sky type known" branch in those cases.
    }
    deriving (Show, Eq)


data FfiModule = FfiModule
    { _fm_moduleName :: !String  -- e.g. "Github.Com.Google.Uuid"
    , _fm_kernelName :: !String  -- e.g. "Uuid"
    , _fm_package    :: !String  -- e.g. "github.com/google/uuid"
    , _fm_functions  :: ![FfiFunction]
    , _fm_implements :: !(Map.Map String [String])
        -- ^ v0.17 PR-21b — qualified-name → list of satisfied
        -- qualified interface names.  Populated by the inspector
        -- (PR-9 + the 2026-06-15 transitive-deps fix); empty for
        -- older kernel.json files.
    , _fm_pkgAlias :: !(Map.Map String String)
        -- ^ v0.17 PR-21b — Go import-path → canonical alias.
        -- Empty for older kernel.json files.
    }
    deriving (Show, Eq)


data FfiRegistry = FfiRegistry
    { _fr_modules :: ![FfiModule]
    }
    deriving (Show, Eq)


emptyRegistry :: FfiRegistry
emptyRegistry = FfiRegistry []


-- | Find function arity by (kernelName, funcName). Nothing if unknown.
lookupFunction :: FfiRegistry -> String -> String -> Maybe Int
lookupFunction reg kname fname =
    let ms = filter (\m -> _fm_kernelName m == kname) (_fr_modules reg)
        fs = concatMap _fm_functions ms
    in  case filter (\f -> _ffn_name f == fname) fs of
            (f:_) -> Just (_ffn_arity f)
            []    -> Nothing


-- ═══════════════════════════════════════════════════════════
-- v0.17 close P1 — pure derived projections over _fr_modules
-- ═══════════════════════════════════════════════════════════
--
-- Each function mirrors the shape of one legacy FFI IORef so the
-- caller-side rewrite in P3+ is mechanical: instead of
-- @readIORef ffiKernelArityRef >>= ...@, the caller does
-- @kernelArityMap _ctx_ffi >>= ...@.  All projections are O(N)
-- pure folds; Compile.hs computes them once at load-time and
-- threads the result through CompileCtx (P2).
--
-- The cycle constraint at Unify.hs:45-47 forbids FfiRegistry from
-- importing Sky.AST.Canonical / Sky.Type.Type, so the Can.Annotation
-- map under @ffiKernelTypeRef@ is NOT projected here — Compile.hs
-- materialises that via its existing @ftyToAnnotation@ converter at
-- load time and threads the resulting map alongside the registry.
-- The 2 typed-wrapper sets (typedWrapperNames, typedWrapperParams)
-- are populated by Compile.seedTypedFfiNames from disk-scanning
-- ffi/*.go (not from _fr_modules), so they thread alongside the
-- registry via LoadedFfiTables → CompileCtx → LowerCtx (v0.17 close
-- iter 5 — Phase 7 IORef defusing; the legacy backing IORefs have
-- been deleted).


-- | Sky import path → kernel module name.  Mirrors
-- @ffiKernelModulesRef@.
kernelModulesMap :: FfiRegistry -> Map.Map String String
kernelModulesMap reg = Map.fromList
    [ (_fm_moduleName m, _fm_kernelName m)
    | m <- _fr_modules reg
    ]


-- | kernel module name → list of exported func names.  Mirrors
-- @ffiKernelFunctionsRef@.
kernelFunctionsMap :: FfiRegistry -> Map.Map String [String]
kernelFunctionsMap reg = Map.fromListWith (++)
    [ (_fm_kernelName m, [_ffn_name f])
    | m <- _fr_modules reg
    , f <- _fm_functions m
    ]


-- | (kernelName, funcName) → arity.  Mirrors @ffiKernelArityRef@.
kernelArityMap :: FfiRegistry -> Map.Map (String, String) Int
kernelArityMap reg = Map.fromList
    [ ((_fm_kernelName m, _ffn_name f), _ffn_arity f)
    | m <- _fr_modules reg
    , f <- _fm_functions m
    ]


-- | (kernelName, funcName) → parsed Sky-side @FtyAst@.  Compile.hs
-- maps this through @ftyToAnnotation@ to obtain the
-- @Can.Annotation@ form previously stored in @ffiKernelTypeRef@.
-- Entries are omitted when the kernel.json @skyType@ field is
-- absent (matches the pre-existing 'Nothing' fall-through at
-- consumer sites).
kernelTypeFtyMap :: FfiRegistry -> Map.Map (String, String) FtyAst
kernelTypeFtyMap reg = Map.fromList
    [ ((_fm_kernelName m, _ffn_name f), fty)
    | m <- _fr_modules reg
    , f <- _fm_functions m
    , Just fty <- [_ffn_skyType f]
    ]


-- | qualified type name → list of qualified interfaces satisfied.
-- Mirrors @ffiImplementsRef@ (and its
-- @Sky.Type.Unify.ffiImplementsRef@ mirror).  Concatenates lists
-- across modules so a type implementing interfaces in multiple
-- kernel.json files surfaces every interface.
implementsMap :: FfiRegistry -> Map.Map String [String]
implementsMap reg = Map.unionsWith (++)
    [ _fm_implements m | m <- _fr_modules reg ]


-- | Go import-path → canonical alias.  Mirrors @ffiPkgAliasRef@.
-- Module order in @_fr_modules@ determines collision resolution
-- (last write wins per @Map.unions@ semantics — matches the
-- pre-existing @writeIORef@ + merge order in
-- @Compile.loadAndSeedFfiRegistry@).
pkgAliasMap :: FfiRegistry -> Map.Map String String
pkgAliasMap reg = Map.unions
    [ _fm_pkgAlias m | m <- _fr_modules reg ]


-- ═══════════════════════════════════════════════════════════
-- JSON decoding
-- ═══════════════════════════════════════════════════════════

instance A.FromJSON FfiFunction where
    parseJSON = A.withObject "FfiFunction" $ \o -> do
        n <- o .: "name"
        a <- o .:? "arity" .!= 1
        rawSky <- o .:? "skyType"
        let parsed = rawSky >>= parseFty
        return (FfiFunction n a parsed)


instance A.FromJSON FfiModule where
    parseJSON = A.withObject "FfiModule" $ \o -> do
        m  <- o .: "moduleName"
        k  <- o .: "kernelName"
        p  <- o .:? "package" .!= ""
        fs <- o .:? "functions" .!= []
        impl <- o .:? "implements" .!= Map.empty
        alias <- o .:? "pkgAlias" .!= Map.empty
        return (FfiModule m k p fs impl alias)


-- ═══════════════════════════════════════════════════════════
-- Disk scanning
-- ═══════════════════════════════════════════════════════════

-- | Load the FfiRegistry from `<projectRoot>/.skycache/ffi/*.kernel.json`.
-- Silently returns an empty registry if the cache directory is absent —
-- the common case for projects with no FFI deps.
--
-- The LSP calls 'loadRegistryFrom' with the workspace root parsed out
-- of @initialize.params.rootUri@ so hover / goto-def / diagnostics on
-- Go-FFI qualified names resolve regardless of what CWD the editor
-- started the LSP process from.  CLI callers keep the CWD-relative
-- default via 'loadRegistry' (which is just @loadRegistryFrom "."@)
-- because @sky check@ / @sky build@ are always invoked from inside the
-- project.
loadRegistry :: IO FfiRegistry
loadRegistry = loadRegistryFrom "."


-- | Same as 'loadRegistry' but reads @<projectRoot>/.skycache/ffi/@
-- instead of the CWD-relative path.  Load-bearing for the LSP: when
-- an editor launches @sky lsp@ from a directory other than the
-- project root (VS Code workspace folders, MCP clients, Neovim opened
-- at a parent) the CWD-relative loader silently returns an empty
-- registry and every Go-FFI qualified call site becomes an "undefined
-- name" false positive.
loadRegistryFrom :: FilePath -> IO FfiRegistry
loadRegistryFrom projectRoot = do
    let ffiDir = projectRoot </> ".skycache" </> "ffi"
    exists <- doesDirectoryExist ffiDir
    if not exists
        then return emptyRegistry
        else do
            entries <- listDirectory ffiDir
            let regs = filter (".kernel.json" `isSuffixOf`) entries
            mods <- mapM (parseOne . (ffiDir </>)) regs
            return (FfiRegistry (concat mods))
  where
    parseOne :: FilePath -> IO [FfiModule]
    parseOne path = do
        bytes <- BL.readFile path
        case A.eitherDecode bytes of
            Left _  -> return []  -- bad JSON: ignore so partial registry still works
            Right m -> return [m]
