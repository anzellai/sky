{-# LANGUAGE TemplateHaskell #-}
{-# LANGUAGE OverloadedStrings #-}

-- | The Sky Go runtime (runtime-go/) and Sky-source stdlib (sky-stdlib/)
-- bundled into the sky binary at build time via Template Haskell.
-- Released binaries are fully standalone — no on-disk runtime-go/ or
-- sky-stdlib/ required.
module Sky.Build.EmbeddedRuntime
    ( embeddedRuntime
    , embeddedSkyStdlib
    ) where

import Data.ByteString (ByteString)
import Data.FileEmbed (embedDir)


-- | Pairs of (relative-path-within-runtime-go, file-contents) covering
-- every file the Go build needs: rt/*.go, go.mod, go.sum.
embeddedRuntime :: [(FilePath, ByteString)]
embeddedRuntime = $(embedDir "runtime-go")


-- | Pairs of (relative-path-within-sky-stdlib, file-contents) for the
-- Sky-source stdlib modules (Sky.Core.Error, etc.) that ship with every
-- project. Materialised to <outDir>/.sky-stdlib/ at build start and
-- added to the module discovery roots, so user code can
-- `import Sky.Core.Error as Error` with no extra setup.
-- Bump this comment to force a TH rebuild when sky-stdlib files are
-- added or removed. FileEmbed's dependency tracking doesn't always
-- notice new files otherwise.
-- Version: 2026-04-14 Sky.Core.Error added; Std.IoError deleted
-- Version: 2026-04-15 Sky.Test added
embeddedSkyStdlib :: [(FilePath, ByteString)]
embeddedSkyStdlib = $(embedDir "sky-stdlib")
