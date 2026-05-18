{-# LANGUAGE TemplateHaskell #-}
{-# LANGUAGE OverloadedStrings #-}

-- | The Sky Go runtime (runtime-go/) and Sky-source stdlib (sky-stdlib/)
-- bundled into the sky binary at build time via Template Haskell.
-- Released binaries are fully standalone — no on-disk runtime-go/ or
-- sky-stdlib/ required.
--
-- Issue #58: empirical bug found 2026-05-18 — `embedDir` from
-- `file-embed-0.0.16.0` does NOT recurse into subdirectories when
-- TH-spliced into a cabal-installed binary (works correctly in
-- `runghc`, so the bug is in optimised-compile path / TH state
-- interaction). The compiled binary's `embeddedRuntime` had 97
-- entries (only top-level + go.mod/go.sum) instead of the 114 on
-- disk — `rt/jobs/*.go` and `rt/telemetry/*.go` were silently
-- missing. Any user app would `go build` fail with `package
-- sky-app/rt/jobs is not in std`.
--
-- Workaround: use a custom TH splice that explicitly walks the
-- tree at TH compile time and emits the full file list (rather
-- than trusting `embedDir`'s recursive walk).
--
-- Audit P3-3: cabal must re-embed when runtime files are
-- *modified*. The new `embedDirRecursive` calls
-- `qAddDependentFile` on every file it walks, so cabal rebuilds
-- the splice when any tracked file's mtime changes. New files
-- still need a touch of this module to invalidate the cache
-- (Haskell TH can't watch directory listings) — but at least the
-- splice itself is now correct on first walk.
module Sky.Build.EmbeddedRuntime
    ( embeddedRuntime
    , embeddedSkyStdlib
    ) where

import Data.ByteString (ByteString)
import Sky.Build.EmbedDirTH (embedDirRecursive)


embeddedRuntime :: [(FilePath, ByteString)]
embeddedRuntime = $(embedDirRecursive "runtime-go")


embeddedSkyStdlib :: [(FilePath, ByteString)]
embeddedSkyStdlib = $(embedDirRecursive "sky-stdlib")
