{-# LANGUAGE TemplateHaskell #-}
{-# LANGUAGE OverloadedStrings #-}

-- | The bundled doc-server (a tiny Sky.Http.Server app under
-- @sky-bundled/doc/@) embedded into the sky binary at TH compile
-- time. `sky doc --serve` materialises these files into a
-- per-version cache dir, builds them once, and runs the binary
-- — the same pattern as `sky console`. The app itself just does
-- `Server.static` over the doc-out directory the compiler-side
-- renderer wrote.
module Sky.Build.EmbeddedDocServer
    ( embeddedDocServerApp
    ) where

import Data.ByteString (ByteString)
import Sky.Build.EmbedDirTH (embedDirRecursive)


embeddedDocServerApp :: [(FilePath, ByteString)]
embeddedDocServerApp = $(embedDirRecursive "../sky-bundled/doc")
