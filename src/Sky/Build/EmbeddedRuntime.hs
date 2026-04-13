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
-- Sky-source stdlib modules (Std.IoError, etc.) that ship with every
-- project. Materialised to <outDir>/.sky-stdlib/ at build start and
-- added to the module discovery roots, so user code can
-- `import Std.IoError exposing (..)` with no extra setup.
embeddedSkyStdlib :: [(FilePath, ByteString)]
embeddedSkyStdlib = $(embedDir "sky-stdlib")
