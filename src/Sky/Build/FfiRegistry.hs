{-# LANGUAGE OverloadedStrings #-}
-- | Reads ffi/*.kernel.json files into a registry used by the canonicaliser
-- and Kernel.lookup so FFI packages flow through the same resolution path as
-- stdlib kernel modules.
module Sky.Build.FfiRegistry
    ( FfiRegistry(..)
    , FfiModule(..)
    , FfiFunction(..)
    , loadRegistry
    , emptyRegistry
    , lookupFunction
    ) where

import qualified Data.Aeson as A
import Data.Aeson ((.:), (.:?), (.!=))
import qualified Data.ByteString.Lazy as BL
import Control.Monad (filterM)
import Data.List (isSuffixOf)
import qualified Data.Map.Strict as Map
import System.Directory (doesDirectoryExist, listDirectory)
import System.FilePath ((</>))


data FfiFunction = FfiFunction
    { _ffn_name  :: !String  -- Sky-side name, e.g. "newString"
    , _ffn_arity :: !Int     -- Sky-side arity (unit param for zero-Go-arg)
    }
    deriving (Show, Eq)


data FfiModule = FfiModule
    { _fm_moduleName :: !String  -- e.g. "Github.Com.Google.Uuid"
    , _fm_kernelName :: !String  -- e.g. "Uuid"
    , _fm_package    :: !String  -- e.g. "github.com/google/uuid"
    , _fm_functions  :: ![FfiFunction]
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
-- JSON decoding
-- ═══════════════════════════════════════════════════════════

instance A.FromJSON FfiFunction where
    parseJSON = A.withObject "FfiFunction" $ \o -> do
        n <- o .: "name"
        a <- o .:? "arity" .!= 1
        return (FfiFunction n a)


instance A.FromJSON FfiModule where
    parseJSON = A.withObject "FfiModule" $ \o -> do
        m  <- o .: "moduleName"
        k  <- o .: "kernelName"
        p  <- o .:? "package" .!= ""
        fs <- o .:? "functions" .!= []
        return (FfiModule m k p fs)


-- ═══════════════════════════════════════════════════════════
-- Disk scanning
-- ═══════════════════════════════════════════════════════════

-- | Load the FfiRegistry from `.skycache/ffi/*.kernel.json` in the current
-- working directory. Silently returns an empty registry if the cache
-- directory is absent — this is the common case for projects with no
-- FFI deps.
loadRegistry :: IO FfiRegistry
loadRegistry = do
    let ffiDir = ".skycache/ffi"
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
