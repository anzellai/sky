{-# LANGUAGE TemplateHaskell #-}
{-# LANGUAGE OverloadedStrings #-}

-- | The Sky Console mini-app (a Std.Ui Sky.Live app under
-- @sky-bundled/console/@) bundled into the sky binary at TH compile
-- time. Released binaries materialise these files into a per-version
-- cache dir on first @sky console@ invocation; no on-disk source is
-- needed.
--
-- The mini-app is intentionally NOT under @sky-stdlib/@ — it is not a
-- user-importable module, just a self-contained app the runtime ships
-- with. Keeping it separate avoids polluting user namespace with
-- console internals.
module Sky.Build.EmbeddedConsole
    ( embeddedConsoleApp
    ) where

import Data.ByteString (ByteString)
import Sky.Build.EmbedDirTH (embedDirRecursive)


-- | File-relative paths under @sky-bundled/console/@ paired with their
-- byte contents. Recurses; @qAddDependentFile@ ensures cabal rebuilds
-- the splice when any file's mtime changes.
embeddedConsoleApp :: [(FilePath, ByteString)]
embeddedConsoleApp = $(embedDirRecursive "sky-bundled/console")
