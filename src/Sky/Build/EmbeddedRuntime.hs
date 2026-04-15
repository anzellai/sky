{-# LANGUAGE TemplateHaskell #-}
{-# LANGUAGE OverloadedStrings #-}

-- | The Sky Go runtime (runtime-go/) and Sky-source stdlib (sky-stdlib/)
-- bundled into the sky binary at build time via Template Haskell.
-- Released binaries are fully standalone — no on-disk runtime-go/ or
-- sky-stdlib/ required.
--
-- Audit P3-3: plain `cabal build` must re-embed when runtime files
-- are *modified*. `embedDir` calls `qAddDependentFile` on every file
-- it walks, so cabal rebuilds the splice when any tracked file's
-- mtime changes. The scripts/build.sh mtime dance that touched this
-- module is therefore redundant and was removed.
--
-- New-file edge case: TH can't watch directory listings, so adding
-- a fresh file without modifying any existing runtime file leaves
-- the splice stale. The `Sky.Build.EmbeddedRuntimeSpec` test locks
-- the invariant — if the embedded tree drifts from disk, it fails
-- with a concrete path diff rather than shipping a broken binary.
module Sky.Build.EmbeddedRuntime
    ( embeddedRuntime
    , embeddedSkyStdlib
    ) where

import Data.ByteString (ByteString)
import Data.FileEmbed (embedDir)


embeddedRuntime :: [(FilePath, ByteString)]
embeddedRuntime = $(embedDir "runtime-go")


embeddedSkyStdlib :: [(FilePath, ByteString)]
embeddedSkyStdlib = $(embedDir "sky-stdlib")
