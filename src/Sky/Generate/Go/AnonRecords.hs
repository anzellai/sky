{-# LANGUAGE BangPatterns #-}
-- | v0.17 PR-22 S2 — anonymous-record registry shared between the
-- legacy @Sky.Build.Compile@ renderer and the new
-- @Sky.Generate.Go.Type@ pipeline.
--
-- Without a SHARED IORef, the two renderers register anonymous
-- record shapes into separate registries — and
-- @generateAnonRecordDecls@ (driven from Compile) only emits the
-- Compile-side registry's entries.  The pipeline's entries would
-- be referenced in emitted Go code without ever being declared,
-- breaking @go build@ with "undefined: Anon_R_…".
--
-- This module owns the canonical instance.  Both renderers import
-- and operate on the SAME @globalAnonRecords@ IORef.  Naming is
-- deterministic — identical shapes hash to the same name on both
-- sides, so cross-renderer interop is byte-stable.
--
-- v0.13 E lineage — every produced shape is registered via
-- @atomicModifyIORef'@ at synth time, so racing typed-codegen
-- passes (multiple modules computing renderer strings
-- concurrently) accumulate every shape.  Identical shapes
-- collapse to the same key.
module Sky.Generate.Go.AnonRecords
    ( globalAnonRecords
    , synthAnonRecordName
    , resetAnonRecords
    , readAnonRecords
    ) where


import           Data.IORef     (IORef, atomicModifyIORef', atomicWriteIORef,
                                 newIORef, readIORef)
import           Data.List      (intercalate)
import qualified Data.Map.Strict as Map
import           System.IO.Unsafe (unsafePerformIO)

import qualified Sky.Type.Type   as T


-- | Process-wide registry of anonymous-record shapes.  Keyed by the
-- synthesised Go struct name (deterministic — see 'synthAnonRecordName')
-- mapping to the original Sky field map.
--
-- Read by 'generateAnonRecordDecls' in Compile to emit one
-- @type Anon_R_… struct@ declaration per unique shape.  Reset at
-- the start of each codegen pass via 'resetAnonRecords'.
{-# NOINLINE globalAnonRecords #-}
globalAnonRecords :: IORef (Map.Map String (Map.Map String T.FieldType))
globalAnonRecords = unsafePerformIO $ newIORef Map.empty


-- | Synthesise a deterministic Go struct name for an anonymous record.
-- Keyed by the full (fieldName, fieldType) shape so records with the
-- same field names but different field types are distinct Go types
-- (per P4). Format: @Anon_R_\<sorted names\>__\<short hash of types\>@.
--
-- The hash is a simple polynomial over the Show-representation of the
-- field types. It isn't cryptographic — we only need it to discriminate
-- between distinct shapes within a single compile unit.
synthAnonRecordName :: Map.Map String T.FieldType -> String
synthAnonRecordName fields =
    let sorted = Map.toAscList fields
        names  = map fst sorted
        typeStr = concatMap (\(_, T.FieldType _ ty) -> show ty) sorted
        nameTag = case names of
            [] -> "Empty"
            _  -> intercalate "_" (map sanitiseField names)
        nameStr = "Anon_R_" ++ nameTag ++ "__" ++ shortHash (nameTag ++ typeStr)
    in unsafePerformIO $ do
        -- v0.13 E: register the shape so `generateAnonRecordDecls`
        -- can emit a concrete Go struct decl for this name.
        -- atomicModifyIORef' so racing typed-codegen passes (which
        -- compute renderer strings concurrently for different
        -- modules) accumulate every shape; the latest one wins on
        -- collision because identical shapes hash to the same name.
        atomicModifyIORef' globalAnonRecords
            (\m -> (Map.insertWith (\_ old -> old) nameStr fields m, ()))
        return nameStr
  where
    sanitiseField =
        map (\c -> if c == '.' || c == '\'' || c == '"' then '_' else c)


-- | Clear the registry at the start of a codegen pass.  Without
-- this, anon records from previous in-process compilations leak
-- into the current one (caught by Gap 8c).
resetAnonRecords :: IO ()
resetAnonRecords = atomicWriteIORef globalAnonRecords Map.empty


-- | Snapshot the registry for downstream emission.
readAnonRecords :: IO (Map.Map String (Map.Map String T.FieldType))
readAnonRecords = readIORef globalAnonRecords


-- | Simple polynomial hash, base-32 encoded for short readable names.
shortHash :: String -> String
shortHash s =
    let h = foldl (\acc c -> acc * 131 + fromIntegral (fromEnum c)) (17 :: Integer) s
        !absH = abs h
    in take 8 (toBase32 absH)
  where
    toBase32 n
        | n <= 0    = "0"
        | otherwise = reverse (go n)
    go 0 = ""
    go n =
        let (q, r) = n `divMod` 32
            c     = "0123456789abcdefghijklmnopqrstuv" !! fromIntegral r
        in c : go q
